//go:build linux && (amd64 || arm64 || arm)

package pcsc

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type nativeBackend struct {
	sysRoot  string
	restarts pcscdRestartLimiter
}

func newNativeBackend() Backend { return &nativeBackend{sysRoot: "/sys"} }

func (backend *nativeBackend) dial(ctx context.Context) (*pcscdClient, error) {
	paths := []string{strings.TrimSpace(os.Getenv("PCSCLITE_CSOCK_NAME")), "/run/pcscd/pcscd.comm", "/var/run/pcscd/pcscd.comm"}
	var failures []error
	seen := make(map[string]bool)
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", path)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		client, err := establishPCSCD(ctx, conn)
		if err == nil {
			return client, nil
		}
		_ = conn.Close()
		failures = append(failures, err)
	}
	return nil, fmt.Errorf("%w: pcscd socket is not reachable: %w", ErrUnavailable, errors.Join(failures...))
}

func ensurePCSCDService(ctx context.Context) {
	if os.Geteuid() != 0 {
		return
	}
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		_ = exec.CommandContext(ctx, "systemctl", "start", "pcscd.socket").Run()
		_ = exec.CommandContext(ctx, "systemctl", "start", "pcscd").Run()
	} else if _, err := os.Stat("/etc/init.d/pcscd"); err == nil {
		_ = exec.CommandContext(ctx, "/etc/init.d/pcscd", "start").Run()
	} else if path, err := exec.LookPath("pcscd"); err == nil {
		_ = exec.CommandContext(ctx, path).Start()
	}
}

var pcscdPIDFiles = []string{"/run/pcscd/pcscd.pid", "/var/run/pcscd/pcscd.pid"}

// restartPCSCDService restarts pcscd so it enumerates the USB readers again.
// With systemd it uses systemctl. Otherwise (for example in the Docker image,
// where VoCat is PID 1 and pcscd is its child) it stops the pcscd named in
// pcscd's PID file, reaps it, and starts a new one.
func restartPCSCDService(ctx context.Context) error {
	if os.Geteuid() != 0 {
		return errors.New("pcscd restart needs root")
	}
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		return exec.CommandContext(ctx, "systemctl", "restart", "pcscd").Run()
	}
	if pid := readPCSCDPID(); pid > 1 {
		stopProcess(pid, 3*time.Second)
	}
	if _, err := os.Stat("/etc/init.d/pcscd"); err == nil {
		return exec.CommandContext(ctx, "/etc/init.d/pcscd", "start").Run()
	}
	path, err := exec.LookPath("pcscd")
	if err != nil {
		return err
	}
	// pcscd daemonizes: the command returns once the daemon is running.
	return exec.CommandContext(ctx, path).Run()
}

func readPCSCDPID() int {
	for _, file := range pcscdPIDFiles {
		if pid := parsePCSCDPID(readTrimmedFile(file)); pid > 0 {
			return pid
		}
	}
	return 0
}

// parsePCSCDPID parses pcscd's PID file, which pcscd pads with NUL bytes.
func parsePCSCDPID(content string) int {
	pid, err := strconv.Atoi(strings.TrimSpace(strings.Trim(content, "\x00")))
	if err != nil {
		return 0
	}
	return pid
}

// stopProcess sends SIGTERM, then SIGKILL after timeout, and reaps the
// process if it is a child of this process.
func stopProcess(pid int, timeout time.Duration) {
	if !strings.Contains(readTrimmedFile(fmt.Sprintf("/proc/%d/comm", pid)), "pcscd") {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.Now().Add(timeout)
	for processRunning(pid) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			time.Sleep(200 * time.Millisecond)
			processRunning(pid)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// processRunning reaps pid if it is an exited child and reports whether it is
// still running. Zombies count as stopped.
func processRunning(pid int) bool {
	var status syscall.WaitStatus
	if reaped, _ := syscall.Wait4(pid, &status, syscall.WNOHANG, nil); reaped == pid {
		return false
	}
	stat := readTrimmedFile(fmt.Sprintf("/proc/%d/stat", pid))
	if stat == "" {
		return false
	}
	if end := strings.LastIndexByte(stat, ')'); end >= 0 && end+2 < len(stat) && stat[end+2] == 'Z' {
		return false
	}
	return true
}

func reauthorizeUSBDevice(sysRoot, usbPath string) {
	if strings.Contains(usbPath, "..") || strings.Contains(usbPath, "/") || strings.Contains(usbPath, "\\") {
		return
	}
	authPath := filepath.Join(filepath.Clean(sysRoot), "bus", "usb", "devices", usbPath, "authorized")
	if _, err := os.Stat(authPath); err != nil {
		return
	}
	_ = os.WriteFile(authPath, []byte("0\n"), 0o644)
	time.Sleep(100 * time.Millisecond)
	_ = os.WriteFile(authPath, []byte("1\n"), 0o644)
}

func (backend *nativeBackend) waitForPCSCReaders(ctx context.Context, client *pcscdClient, physical []Reader, states []pcscdReaderState) []pcscdReaderState {
	// First pass: wait up to 2 seconds for active driver negotiation.
	pollDeadline := time.Now().Add(2 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(pollDeadline) {
		pollDeadline = dl
	}
	for len(states) < len(physical) && time.Now().Before(pollDeadline) {
		select {
		case <-ctx.Done():
			return states
		case <-time.After(250 * time.Millisecond):
		}
		if updated, err := client.readers(ctx); err == nil {
			states = updated
			if len(states) >= len(physical) {
				return states
			}
		}
	}
	if len(states) >= len(physical) {
		return states
	}

	// Second pass: if readers are still missing from pcscd, trigger a USB re-authorization
	// on the physical devices in sysfs to reset any stalled CCID endpoints, then poll briefly.
	reauthorized := false
	for _, phys := range physical {
		if phys.USBPath != "" {
			reauthorizeUSBDevice(backend.sysRoot, phys.USBPath)
			reauthorized = true
		}
	}
	if !reauthorized {
		return states
	}

	retryDeadline := time.Now().Add(2 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(retryDeadline) {
		retryDeadline = dl
	}
	for len(states) < len(physical) && time.Now().Before(retryDeadline) {
		select {
		case <-ctx.Done():
			return states
		case <-time.After(300 * time.Millisecond):
		}
		if updated, err := client.readers(ctx); err == nil {
			states = updated
			if len(states) >= len(physical) {
				return states
			}
		}
	}
	return states
}

// Readers lists the PC/SC readers and maps each to its USB device. When pcscd
// is still serving readers that were unplugged (it missed the hot-plug
// events, which happens inside containers), every reader stays unusable until
// pcscd is restarted, so Readers restarts it once and lists again. Restarts
// are rate limited with an exponential backoff.
func (backend *nativeBackend) Readers(ctx context.Context) ([]Reader, error) {
	readers, health, err := backend.readersOnce(ctx)
	if err != nil || !health.needsRestart(pcscMaxReaders) {
		if err == nil {
			backend.restarts.healthy()
		}
		return readers, err
	}
	if !backend.restarts.allow(time.Now()) {
		return readers, nil
	}
	slog.Warn("restarting pcscd: its reader list does not match the USB readers present",
		"category", "hardware",
		"stale_readers", health.staleReaders,
		"pcscd_readers", health.pcscdReaders,
		"unmatched_usb_readers", health.unmatchedPhysical)
	if err := restartPCSCDService(ctx); err != nil {
		slog.Warn("pcscd restart failed", "category", "hardware", "error", err)
		return readers, nil
	}
	if !backend.waitForDial(ctx, 3*time.Second) {
		return readers, nil
	}
	if refreshed, _, refreshErr := backend.readersOnce(ctx); refreshErr == nil {
		return refreshed, nil
	}
	return readers, nil
}

// pcscdHealth describes how well pcscd's reader list matches the USB bus.
type pcscdHealth struct {
	// staleReaders counts pcscd readers whose USB device no longer exists, or
	// whose bus/device number now belongs to a device that is not a reader.
	staleReaders int
	// pcscdReaders is the number of readers pcscd reported.
	pcscdReaders int
	// unmatchedPhysical counts USB CCID readers that pcscd does not serve.
	unmatchedPhysical int
}

// needsRestart reports whether pcscd has to be restarted to see the readers
// that are really attached. Readers beyond pcscd's fixed table size are never
// listed, so missing readers only count while the table has free entries.
func (health pcscdHealth) needsRestart(maxReaders int) bool {
	if health.staleReaders > 0 {
		return true
	}
	return health.unmatchedPhysical > 0 && health.pcscdReaders < maxReaders
}

// pcscdRestartLimiter allows the first restart at once, then backs off from
// one minute up to 30 minutes while pcscd keeps needing restarts. A healthy
// listing resets the backoff.
type pcscdRestartLimiter struct {
	mu      sync.Mutex
	last    time.Time
	backoff time.Duration
}

const (
	pcscdRestartMinBackoff = time.Minute
	pcscdRestartMaxBackoff = 30 * time.Minute
)

func (limiter *pcscdRestartLimiter) allow(now time.Time) bool {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if !limiter.last.IsZero() && now.Sub(limiter.last) < limiter.backoff {
		return false
	}
	limiter.last = now
	switch {
	case limiter.backoff < pcscdRestartMinBackoff:
		limiter.backoff = pcscdRestartMinBackoff
	case limiter.backoff < pcscdRestartMaxBackoff:
		limiter.backoff = min(limiter.backoff*2, pcscdRestartMaxBackoff)
	}
	return true
}

func (limiter *pcscdRestartLimiter) healthy() {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	limiter.backoff = 0
}

func (backend *nativeBackend) waitForDial(ctx context.Context, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
		}
		if client, err := backend.dial(ctx); err == nil {
			_ = client.closeContext(context.Background())
			return true
		}
	}
	return false
}

func (backend *nativeBackend) readersOnce(ctx context.Context) ([]Reader, pcscdHealth, error) {
	var health pcscdHealth
	physical := discoverUSBSmartCardReaders(backend.sysRoot, "pcsc_driver_missing")
	client, err := backend.dial(ctx)
	if err != nil && len(physical) > 0 {
		ensurePCSCDService(ctx)
		dialDeadline := time.Now().Add(1500 * time.Millisecond)
		for time.Now().Before(dialDeadline) {
			select {
			case <-ctx.Done():
				break
			case <-time.After(200 * time.Millisecond):
			}
			if c, dialErr := backend.dial(ctx); dialErr == nil {
				client, err = c, nil
				break
			}
		}
	}
	if err != nil {
		if len(physical) > 0 {
			for index := range physical {
				physical[index].DiscoveryIssue = "pcsc_service_unavailable"
			}
			return physical, health, nil
		}
		return nil, health, err
	}
	defer client.closeContext(context.Background())
	states, err := client.readers(ctx)
	if err != nil {
		return nil, health, err
	}
	if len(physical) > 0 && len(states) < len(physical) {
		states = backend.waitForPCSCReaders(ctx, client, physical, states)
	}
	physicalPaths := make(map[string]bool, len(physical))
	for _, usbReader := range physical {
		physicalPaths[usbReader.USBPath] = true
	}
	health.pcscdReaders = len(states)
	readers := make([]Reader, 0, len(states))
	for _, state := range states {
		reader := Reader{
			Name:        state.name,
			CardPresent: state.state&pcscCardPresent != 0,
			ATR:         strings.ToUpper(hex.EncodeToString(state.atr)),
		}
		path, ok, gone := backend.readerUSBPath(ctx, client, state.name)
		if ok && len(physical) > 0 && !physicalPaths[path] {
			// The bus/device number was reused by a device that is not a
			// smart-card reader: pcscd is holding a reader that was unplugged.
			ok, gone = false, true
		}
		if gone {
			health.staleReaders++
		}
		if ok {
			reader.USBPath = path
			reader.VendorID = backend.readSysfsText(path, "idVendor")
			reader.ProductID = backend.readSysfsText(path, "idProduct")
			reader.Manufacturer = backend.readSysfsText(path, "manufacturer")
			reader.Product = backend.readSysfsText(path, "product")
		} else {
			reader.USBPath = "pcsc:" + state.name
		}
		if reader.Product == "" {
			reader.Product = strings.TrimSpace(strings.TrimSuffix(state.name, " 00 00"))
		}
		readers = append(readers, reader)
	}
	merged := filterVirtualPCDReaders(mergePCSCAndUSBReaders(readers, physical))
	for _, reader := range merged {
		if reader.DiscoveryIssue == "pcsc_driver_missing" {
			health.unmatchedPhysical++
		}
	}
	return merged, health, nil
}

// readerUSBPath maps a pcscd reader to its sysfs USB device name through the
// reader's CHANNEL_ID (USB bus and device number). gone is true when the
// channel names a USB device that no longer exists, which means pcscd is
// still serving a reader that was unplugged.
func (backend *nativeBackend) readerUSBPath(ctx context.Context, client *pcscdClient, name string) (path string, ok bool, gone bool) {
	card, _, err := client.connect(ctx, name, pcscShareDirect, 0)
	if err != nil {
		return "", false, false
	}
	disposition := uint32(pcscLeaveCard)
	defer client.simpleCardCommand(context.Background(), pcscCmdDisconnect, card, &disposition)
	attribute, err := client.getAttrib(ctx, card, pcscAttrChannelID)
	if err != nil || len(attribute) < 4 {
		return "", false, false
	}
	channel := binary.LittleEndian.Uint32(attribute[:4])
	if channel>>16 != 0x0020 {
		return "", false, false
	}
	bus, device := int((channel>>8)&0xff), int(channel&0xff)
	usbRoot := filepath.Join(backend.sysRoot, "bus", "usb", "devices")
	entries, err := os.ReadDir(usbRoot)
	if err != nil {
		return "", false, false
	}
	for _, entry := range entries {
		if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		entryPath := filepath.Join(usbRoot, entry.Name())
		entryBus, busErr := readSysfsInt(entryPath, "busnum")
		entryDevice, deviceErr := readSysfsInt(entryPath, "devnum")
		if busErr == nil && deviceErr == nil && entryBus == bus && entryDevice == device {
			return entry.Name(), true, false
		}
	}
	return "", false, true
}

func (backend *nativeBackend) Open(ctx context.Context, selector Selector) (Card, error) {
	readers, err := backend.Readers(ctx)
	if err != nil {
		return nil, err
	}
	reader, ok := matchReader(readers, selector)
	if !ok {
		return nil, ErrReaderNotFound
	}
	if reader.DiscoveryIssue != "" {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, reader.DiscoveryIssue)
	}
	if !reader.CardPresent {
		return nil, ErrNoCard
	}
	client, err := backend.dial(ctx)
	if err != nil {
		return nil, err
	}
	handle, protocol, err := client.connect(ctx, reader.Name, pcscShareShared, pcscProtocolAny)
	if err != nil {
		_ = client.closeContext(context.Background())
		return nil, err
	}
	if err := client.simpleCardCommand(ctx, pcscCmdBeginTransaction, handle, nil); err != nil {
		disposition := uint32(pcscLeaveCard)
		_ = client.simpleCardCommand(context.Background(), pcscCmdDisconnect, handle, &disposition)
		_ = client.closeContext(context.Background())
		return nil, fmt.Errorf("pcsc: begin card transaction: %w", err)
	}
	return &nativeCard{client: client, handle: handle, protocol: protocol}, nil
}

type nativeCard struct {
	client   *pcscdClient
	handle   int32
	protocol uint32
	closed   bool
}

func (card *nativeCard) Transmit(ctx context.Context, command []byte) ([]byte, uint16, error) {
	if card == nil || card.client == nil || card.closed {
		return nil, 0, errors.New("pcsc: card session is closed")
	}
	return card.transmit(ctx, append([]byte(nil), command...), 0)
}

// Protocol reports the protocol pcscd negotiated with the card (T=0 or T=1).
// The eUICC layer needs it because the two differ in how a command that
// returns no data must be framed, and because 61xx/6Cxx are T=0 artefacts.
func (card *nativeCard) Protocol() uint32 {
	if card == nil {
		return 0
	}
	return card.protocol
}

func (card *nativeCard) TransmitRaw(ctx context.Context, command []byte) ([]byte, uint16, error) {
	if card == nil || card.client == nil || card.closed {
		return nil, 0, errors.New("pcsc: card session is closed")
	}
	response, err := card.client.transmit(ctx, card.handle, card.protocol, append([]byte(nil), command...))
	if err != nil {
		return nil, 0, err
	}
	return splitAPDUResponse(response)
}

func (card *nativeCard) transmit(ctx context.Context, command []byte, depth int) ([]byte, uint16, error) {
	if depth > 8 {
		return nil, 0, errors.New("pcsc: too many APDU continuations")
	}
	data, status, err := card.TransmitRaw(ctx, command)
	if err != nil {
		return nil, 0, err
	}
	sw1, sw2 := byte(status>>8), byte(status)
	if sw1 == 0x6c && len(command) >= 5 {
		retry := append([]byte(nil), command...)
		retry[len(retry)-1] = sw2
		return card.transmit(ctx, retry, depth+1)
	}
	if sw1 == 0x61 || sw1 == 0x9f {
		more, sw, err := card.transmit(ctx, []byte{0x00, 0xc0, 0x00, 0x00, sw2}, depth+1)
		if err != nil {
			return nil, 0, err
		}
		return append(data, more...), sw, nil
	}
	return data, status, ctx.Err()
}

func (card *nativeCard) Close() error { return card.close(pcscLeaveCard) }

func (card *nativeCard) CloseWithReset() error { return card.close(pcscResetCard) }

func (card *nativeCard) close(disposition uint32) error {
	if card == nil || card.closed {
		return nil
	}
	card.closed = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var result []error
	if card.client != nil {
		if err := card.client.simpleCardCommand(ctx, pcscCmdEndTransaction, card.handle, &disposition); err != nil {
			result = append(result, err)
		}
		if err := card.client.simpleCardCommand(ctx, pcscCmdDisconnect, card.handle, &disposition); err != nil {
			result = append(result, err)
		}
		if err := card.client.closeContext(ctx); err != nil {
			result = append(result, err)
		}
	}
	return errors.Join(result...)
}

func (backend *nativeBackend) readSysfsText(usbPath, name string) string {
	value, err := os.ReadFile(filepath.Join(backend.sysRoot, "bus", "usb", "devices", usbPath, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(value))
}

func readSysfsInt(path, name string) (int, error) {
	value, err := os.ReadFile(filepath.Join(path, name))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(value)))
}
