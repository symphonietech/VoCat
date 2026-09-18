package server

import (
	"fmt"
	"math"
	"net"
	"sync"
	"time"
)

// liveNetWindow is how far back the "last minute" byte totals reach.
const liveNetWindow = time.Minute

// liveNetMaxGap bounds how far apart two samples may be before a rate computed
// across them stops being "live". The overview SSE ticks every two seconds, so
// a gap beyond this means the tab was closed or the device was idle; treat it
// as a fresh baseline instead of averaging a long dead interval.
const liveNetMaxGap = 15 * time.Second

// netIfSample is one cumulative counter reading for an interface.
type netIfSample struct {
	at    time.Time
	rxCum uint64
	txCum uint64
}

// liveNetDevice holds the per-device sampling state used to derive rates and
// trailing-window totals from cumulative interface counters.
type liveNetDevice struct {
	prev    netIfSample
	hasPrev bool
	window  []netIfSample
}

// liveNetResult is one rendered snapshot of a device's live network state.
type liveNetResult struct {
	ipv4     string
	rxRate   float64 // bytes/sec over the trailing sample interval
	txRate   float64
	minuteRx int64 // bytes over the trailing liveNetWindow
	minuteTx int64
	status   string // "", "waiting_sample", or "stale"
}

// liveNetTracker derives live rates and last-minute totals from cumulative
// /sys interface counters. It is driven on demand by the overview builders, so
// no separate goroutine is required; the SSE overview cadence keeps it warm.
type liveNetTracker struct {
	mu      sync.Mutex
	devices map[string]*liveNetDevice
}

func newLiveNetTracker() *liveNetTracker {
	return &liveNetTracker{devices: map[string]*liveNetDevice{}}
}

// sample reads the interface's current counters and addresses and returns the
// device's live network state. Interface addresses resolve even on the first
// call; rates and totals need a second reading, reported as waiting_sample.
func (t *liveNetTracker) sample(deviceID, iface string, now time.Time) liveNetResult {
	ipv4 := netIfAddrs(iface)
	rxCum, txCum, err := netIfCounters(iface)
	if err != nil {
		// The interface briefly disappears while QMI reconnects. Drop the
		// baseline so the next good read starts fresh rather than counting the
		// reconnect as one giant delta.
		t.mu.Lock()
		delete(t.devices, deviceID)
		t.mu.Unlock()
		return liveNetResult{ipv4: ipv4, status: "stale"}
	}
	rxRate, txRate, minuteRx, minuteTx, status := t.record(deviceID, rxCum, txCum, now)
	return liveNetResult{
		ipv4:   ipv4,
		rxRate: rxRate, txRate: txRate,
		minuteRx: minuteRx, minuteTx: minuteTx,
		status: status,
	}
}

// record folds one cumulative counter reading into the device's sampling state
// and returns the derived rates and trailing-window totals. It is pure (no
// interface I/O) so the rate/window logic is unit-testable.
func (t *liveNetTracker) record(deviceID string, rxCum, txCum uint64, now time.Time) (rxRate, txRate float64, minuteRx, minuteTx int64, status string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	d := t.devices[deviceID]
	if d == nil {
		d = &liveNetDevice{}
		t.devices[deviceID] = d
	}
	current := netIfSample{at: now, rxCum: rxCum, txCum: txCum}

	// First sighting, a counter reset (interface reconnected), or a gap too
	// long to average honestly: establish a baseline and wait for the next
	// reading before reporting a rate.
	if !d.hasPrev || rxCum < d.prev.rxCum || txCum < d.prev.txCum || now.Sub(d.prev.at) > liveNetMaxGap {
		d.prev = current
		d.hasPrev = true
		d.window = []netIfSample{current}
		return 0, 0, 0, 0, "waiting_sample"
	}

	if elapsed := now.Sub(d.prev.at).Seconds(); elapsed > 0 {
		rxRate = float64(rxCum-d.prev.rxCum) / elapsed
		txRate = float64(txCum-d.prev.txCum) / elapsed
	}
	d.prev = current
	d.window = append(d.window, current)

	// Drop samples outside the trailing window, then measure totals against
	// the oldest surviving reading.
	cutoff := now.Add(-liveNetWindow)
	kept := d.window[:0]
	for _, s := range d.window {
		if !s.at.Before(cutoff) {
			kept = append(kept, s)
		}
	}
	d.window = kept
	minuteRx = int64(rxCum - d.window[0].rxCum)
	minuteTx = int64(txCum - d.window[0].txCum)
	return rxRate, txRate, minuteRx, minuteTx, ""
}

// netIfAddrs returns the interface's first global IPv4 address. It uses only
// the net package, so it compiles on every platform; on hosts without the
// interface it returns an empty string.
func netIfAddrs(iface string) (ipv4 string) {
	if iface == "" {
		return ""
	}
	netIf, err := net.InterfaceByName(iface)
	if err != nil {
		return ""
	}
	addrs, err := netIf.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		var ip net.IP
		switch a := addr.(type) {
		case *net.IPNet:
			ip = a.IP
		case *net.IPAddr:
			ip = a.IP
		}
		if ip == nil || ip.IsLoopback() {
			continue
		}
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
	}
	return ""
}

// formatLiveBytes mirrors the SPA's formatBytes so the live strings match the
// chart's formatting: 1024-based units, rounded once the value reaches 100.
func formatLiveBytes(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		value = 0
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	size := value
	unit := 0
	for size >= 1024 && unit < len(units)-1 {
		size /= 1024
		unit++
	}
	if size >= 100 {
		return fmt.Sprintf("%.0f %s", size, units[unit])
	}
	return fmt.Sprintf("%.1f %s", size, units[unit])
}
