package ims

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"vocat/internal/g711"
)

const (
	rtpClockRate     = 8000
	rtpPacketSamples = 160
	// One packet per 20 ms, matching the a=ptime:20 in the SDP offer.
	rtpPacketInterval = time.Duration(rtpPacketSamples) * time.Second / rtpClockRate
	// Cap on queued uplink audio (160 ms). A browser whose sample clock runs
	// slightly fast would otherwise build an ever-growing delay between
	// speaking and transmission; dropping the oldest audio keeps the
	// conversation current instead.
	maxPendingSamples = 8 * rtpPacketSamples
)

type rtpMedia struct {
	conn *net.UDPConn

	mu          sync.RWMutex
	remote      *net.UDPAddr
	codec       string
	payloadType byte

	writeMu   sync.Mutex
	pending   []int16
	sequence  uint16
	timestamp uint32
	ssrc      uint32

	downlink    chan []int16
	closed      chan struct{}
	close       sync.Once
	pump        sync.Once
	sendErrOnce sync.Once
	logger      *slog.Logger
}

func newRTPMedia(local net.IP, logger *slog.Logger) (*rtpMedia, error) {
	address := &net.UDPAddr{IP: local, Port: 0}
	connection, err := net.ListenUDP("udp", address)
	if err != nil {
		return nil, fmt.Errorf("ims: open RTP socket: %w", err)
	}
	seed := make([]byte, 10)
	if _, err := io.ReadFull(cryptorand.Reader, seed); err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("ims: initialize RTP state: %w", err)
	}
	media := &rtpMedia{
		conn: connection, sequence: binary.BigEndian.Uint16(seed[:2]),
		timestamp: binary.BigEndian.Uint32(seed[2:6]), ssrc: binary.BigEndian.Uint32(seed[6:]),
		downlink: make(chan []int16, 64), closed: make(chan struct{}),
		logger: logger,
	}
	go media.receive()
	return media, nil
}

func (media *rtpMedia) Codec() string {
	media.mu.RLock()
	defer media.mu.RUnlock()
	return media.codec
}

func (media *rtpMedia) ready() bool {
	media.mu.RLock()
	defer media.mu.RUnlock()
	return media.remote != nil && media.codec != ""
}

func (media *rtpMedia) offerSDP(local net.IP) []byte {
	return media.buildSDP(local, "8 0 104 102 100", []string{
		"a=rtpmap:8 PCMA/8000",
		"a=rtpmap:0 PCMU/8000",
		"a=rtpmap:104 AMR-WB/16000",
		"a=fmtp:104 mode-change-capability=2;max-red=220",
		"a=rtpmap:102 AMR/8000",
		"a=fmtp:102 mode-change-capability=2;max-red=220",
		"a=rtpmap:100 telephone-event/8000",
		"a=fmtp:100 0-15",
	})
}

func (media *rtpMedia) answerSDP(local net.IP) []byte {
	media.mu.RLock()
	codec, payload := media.codec, media.payloadType
	media.mu.RUnlock()
	if codec == "" {
		return media.offerSDP(local)
	}
	rate := 8000
	if codec == "AMR-WB" {
		rate = 16000
	}
	return media.buildSDP(local, strconv.Itoa(int(payload)), []string{
		fmt.Sprintf("a=rtpmap:%d %s/%d", payload, codec, rate),
	})
}

func (media *rtpMedia) buildSDP(local net.IP, formats string, attributes []string) []byte {
	if local == nil || local.IsUnspecified() {
		if udp, ok := media.conn.LocalAddr().(*net.UDPAddr); ok {
			local = udp.IP
		}
	}
	if local == nil || local.IsUnspecified() {
		local = net.IPv4zero
	}
	family := "IP4"
	if local.To4() == nil {
		family = "IP6"
	}
	port := media.conn.LocalAddr().(*net.UDPAddr).Port
	sessionID := time.Now().UnixNano()
	lines := []string{
		"v=0",
		fmt.Sprintf("o=- %d %d IN %s %s", sessionID, sessionID, family, local.String()),
		"s=VoCat",
		fmt.Sprintf("c=IN %s %s", family, local.String()),
		"t=0 0",
		fmt.Sprintf("m=audio %d RTP/AVP %s", port, formats),
	}
	if attributes == nil {
		lines = append(lines,
			"a=rtpmap:8 PCMA/8000",
			"a=rtpmap:0 PCMU/8000",
			"a=rtpmap:104 AMR-WB/16000",
			"a=fmtp:104 mode-change-capability=2;max-red=220",
			"a=rtpmap:102 AMR/8000",
			"a=fmtp:102 mode-change-capability=2;max-red=220",
			"a=rtpmap:100 telephone-event/8000",
			"a=fmtp:100 0-15",
		)
	} else {
		lines = append(lines, attributes...)
	}
	lines = append(lines, "a=ptime:20", "a=sendrecv", "")
	return []byte(strings.Join(lines, "\r\n"))
}

func (media *rtpMedia) configureRemote(body []byte) error {
	address, port, formats, mappings, err := parseAudioSDP(body)
	if err != nil {
		return err
	}
	var codec string
	var payload byte
	for _, value := range formats {
		parsed, parseErr := strconv.Atoi(value)
		if parseErr != nil || parsed < 0 || parsed > 127 {
			continue
		}
		name := strings.ToUpper(mappings[parsed])
		if name == "" {
			switch parsed {
			case 0:
				name = "PCMU"
			case 8:
				name = "PCMA"
			case 100:
				continue
			default:
				name = fmt.Sprintf("PAYLOAD-%d", parsed)
			}
		}
		if name != "TELEPHONE-EVENT" {
			codec, payload = name, byte(parsed)
			break
		}
	}
	if codec == "" && len(formats) > 0 {
		if parsed, parseErr := strconv.Atoi(formats[0]); parseErr == nil && parsed >= 0 && parsed <= 127 {
			codec, payload = fmt.Sprintf("PAYLOAD-%d", parsed), byte(parsed)
		}
	}
	if codec == "" {
		return errors.New("ims: remote SDP has no usable audio format")
	}
	media.mu.Lock()
	media.remote = &net.UDPAddr{IP: address, Port: port}
	media.codec = codec
	media.payloadType = payload
	media.mu.Unlock()
	media.startTransmit()
	return nil
}

func parseAudioSDP(body []byte) (net.IP, int, []string, map[int]string, error) {
	var sessionIP, mediaIP net.IP
	var port int
	var formats []string
	mappings := make(map[int]string)
	inAudio := false
	for _, raw := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "m="):
			fields := strings.Fields(strings.TrimPrefix(line, "m="))
			inAudio = len(fields) >= 4 && strings.EqualFold(fields[0], "audio") && strings.HasPrefix(strings.ToUpper(fields[2]), "RTP/AVP")
			if inAudio {
				port, _ = strconv.Atoi(strings.Split(fields[1], "/")[0])
				formats = append([]string(nil), fields[3:]...)
			}
		case strings.HasPrefix(line, "c="):
			fields := strings.Fields(strings.TrimPrefix(line, "c="))
			if len(fields) >= 3 {
				ip := net.ParseIP(strings.Split(fields[2], "/")[0])
				if inAudio {
					mediaIP = ip
				} else {
					sessionIP = ip
				}
			}
		case inAudio && strings.HasPrefix(strings.ToLower(line), "a=rtpmap:"):
			fields := strings.Fields(strings.TrimPrefix(line, "a=rtpmap:"))
			if len(fields) == 2 {
				pt, parseErr := strconv.Atoi(fields[0])
				if parseErr == nil {
					mappings[pt] = strings.Split(fields[1], "/")[0]
				}
			}
		}
	}
	if mediaIP == nil {
		mediaIP = sessionIP
	}
	if mediaIP == nil || port < 1 || port > 65535 || len(formats) == 0 {
		return nil, 0, nil, nil, errors.New("ims: remote SDP has no usable audio endpoint")
	}
	return mediaIP, port, formats, mappings, nil
}

func (media *rtpMedia) ReadPCM(ctx context.Context) ([]int16, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-media.closed:
		return nil, io.EOF
	case samples := <-media.downlink:
		return samples, nil
	}
}

// WritePCM queues uplink audio. It does not transmit: the pump below owns the
// socket so packets leave at a steady 20 ms cadence regardless of how the
// WebSocket delivers them.
func (media *rtpMedia) WritePCM(samples []int16) error {
	media.mu.RLock()
	negotiated := media.remote != nil && media.codec != ""
	media.mu.RUnlock()
	if !negotiated {
		return errors.New("ims: RTP media is not negotiated")
	}
	media.writeMu.Lock()
	defer media.writeMu.Unlock()
	media.pending = append(media.pending, samples...)
	if len(media.pending) > maxPendingSamples {
		media.pending = media.pending[len(media.pending)-maxPendingSamples:]
	}
	return nil
}

// startTransmit begins the uplink once the remote address and codec are known.
// RTP has to keep flowing for the lifetime of the call: an IMS access gateway
// that stops seeing packets treats the call as dead and sends BYE, typically
// within seconds. Browser audio is optional and intermittent — nobody may be
// attached, the microphone may be muted, WebSocket messages arrive in bursts —
// so the pump sends silence whenever the queue is short, and the call survives
// with no browser attached at all.
func (media *rtpMedia) startTransmit() {
	media.pump.Do(func() { go media.transmit() })
}

func (media *rtpMedia) transmit() {
	ticker := time.NewTicker(rtpPacketInterval)
	defer ticker.Stop()
	frame := make([]int16, rtpPacketSamples)
	for {
		select {
		case <-media.closed:
			return
		case <-ticker.C:
		}
		media.writeMu.Lock()
		filled := copy(frame, media.pending)
		media.pending = media.pending[filled:]
		media.writeMu.Unlock()
		for index := filled; index < rtpPacketSamples; index++ {
			frame[index] = 0
		}
		// A transient send error must not end the uplink for the rest of the
		// call; a permanently closed socket is caught by the select above.
		// Report the first one though: silently dropping every uplink packet
		// looks exactly like a network-side problem from the outside.
		if err := media.sendFrame(frame); err != nil {
			media.reportSendError(err)
		}
	}
}

// reportSendError logs the first uplink failure of a call, once, so a
// persistently unwritable socket is visible without flooding the log at the
// packet rate.
func (media *rtpMedia) reportSendError(err error) {
	media.sendErrOnce.Do(func() {
		if media.logger == nil {
			return
		}
		media.logger.Warn("IMS RTP uplink send failed",
			"category", "call",
			"error", err,
		)
	})
}

// sendFrame encodes and transmits exactly one packet. The sequence number and
// timestamp are owned by the single pump goroutine, so they need no lock.
func (media *rtpMedia) sendFrame(samples []int16) error {
	media.mu.RLock()
	var remote *net.UDPAddr
	if media.remote != nil {
		clone := *media.remote
		remote = &clone
	}
	codec, payload := media.codec, media.payloadType
	media.mu.RUnlock()
	if remote == nil || codec == "" {
		return nil
	}
	packet := make([]byte, 12+rtpPacketSamples)
	packet[0], packet[1] = 0x80, payload
	binary.BigEndian.PutUint16(packet[2:4], media.sequence)
	binary.BigEndian.PutUint32(packet[4:8], media.timestamp)
	binary.BigEndian.PutUint32(packet[8:12], media.ssrc)
	for index, sample := range samples {
		if codec == "PCMA" {
			packet[12+index] = linearToALaw(sample)
		} else {
			packet[12+index] = linearToMuLaw(sample)
		}
	}
	media.sequence++
	media.timestamp += rtpPacketSamples
	if _, err := media.conn.WriteToUDP(packet, remote); err != nil {
		return fmt.Errorf("ims: send RTP: %w", err)
	}
	return nil
}

func (media *rtpMedia) receive() {
	packet := make([]byte, 2048)
	for {
		count, source, err := media.conn.ReadFromUDP(packet)
		if err != nil {
			return
		}
		media.mu.Lock()
		remote, codec, payload := media.remote, media.codec, media.payloadType
		if remote != nil && remote.IP.Equal(source.IP) && remote.Port != source.Port {
			remote.Port = source.Port // symmetric RTP/NAT port learning
		}
		media.mu.Unlock()
		if remote == nil || !remote.IP.Equal(source.IP) || count < 12 || packet[0]>>6 != 2 || packet[1]&0x7f != payload {
			continue
		}
		header := 12 + int(packet[0]&0x0f)*4
		if packet[0]&0x10 != 0 {
			if count < header+4 {
				continue
			}
			header += 4 + int(binary.BigEndian.Uint16(packet[header+2:header+4]))*4
		}
		if header >= count {
			continue
		}
		samples := make([]int16, count-header)
		for index, encoded := range packet[header:count] {
			if codec == "PCMA" {
				samples[index] = aLawToLinear(encoded)
			} else {
				samples[index] = muLawToLinear(encoded)
			}
		}
		select {
		case media.downlink <- samples:
		default:
			// Keep real-time behavior by dropping the oldest queued packet.
			select {
			case <-media.downlink:
			default:
			}
			select {
			case media.downlink <- samples:
			default:
			}
		}
	}
}

func (media *rtpMedia) Close() error {
	media.close.Do(func() {
		close(media.closed)
		_ = media.conn.Close()
	})
	return nil
}

// The G.711 laws live in internal/g711 so the SIP trunk can use the same
// implementation. These keep the local names the RTP paths already read by.
func linearToMuLaw(sample int16) byte { return g711.LinearToMuLaw(sample) }

func muLawToLinear(value byte) int16 { return g711.MuLawToLinear(value) }

func linearToALaw(sample int16) byte { return g711.LinearToALaw(sample) }

func aLawToLinear(value byte) int16 { return g711.ALawToLinear(value) }
