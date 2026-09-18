package siptrunk

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"vocat/internal/dtmf"
	"vocat/internal/g711"
)

const (
	// 20 ms at 8 kHz, matching the a=ptime:20 in the answer.
	frameSamples = 160
	frameGap     = time.Duration(frameSamples) * time.Second / clockRate
	// Cap on queued uplink audio. The far leg and this one are driven by
	// independent clocks, so without a bound the slower one accumulates delay
	// for the rest of the call.
	maxQueuedSamples = 8 * frameSamples
)

// rtpLeg is the media session toward the PBX. It mirrors what the IMS side
// does: RTP in and out on one socket, G.711 on the wire and linear PCM to the
// bridge, and an isochronous transmit that keeps sending when the other leg
// has nothing to give -- an RTP stream that stops is treated as a dead call by
// everything that watches one.
type rtpLeg struct {
	conn *net.UDPConn
	// payload is atomic because an outbound leg learns which G.711 flavour it
	// is carrying only when the answer arrives, by which time the transmit
	// and receive loops are already running against a silent socket.
	payload atomic.Uint32
	// eventPayload is the negotiated RFC 4733 type, or zero when the far end
	// offered none and this leg cannot carry keypad digits.
	eventPayload atomic.Uint32

	dtmfOut dtmf.Sender
	dtmfIn  dtmf.Receiver
	// digits carries what the far end pressed. Lossy on purpose: a keypad
	// press is not worth stalling the receive loop for.
	digits chan rune

	mu     sync.RWMutex
	remote *net.UDPAddr

	writeMu   sync.Mutex
	queued    []int16
	sequence  uint16
	timestamp uint32
	ssrc      uint32

	inbound chan []int16
	closed  chan struct{}
	close   sync.Once
	pump    sync.Once
}

// newRTPLeg opens a socket on local and prepares to talk to remote. The port
// is chosen by the kernel and reported by LocalPort for the SDP answer.
func newRTPLeg(local net.IP, remote *net.UDPAddr, payload byte) (*rtpLeg, error) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: local, Port: 0})
	if err != nil {
		return nil, fmt.Errorf("siptrunk: open RTP socket: %w", err)
	}
	seed := make([]byte, 10)
	if _, err := io.ReadFull(cryptorand.Reader, seed); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("siptrunk: seed RTP state: %w", err)
	}
	leg := &rtpLeg{
		conn:      conn,
		remote:    remote,
		sequence:  binary.BigEndian.Uint16(seed[:2]),
		timestamp: binary.BigEndian.Uint32(seed[2:6]),
		ssrc:      binary.BigEndian.Uint32(seed[6:]),
		inbound:   make(chan []int16, 64),
		digits:    make(chan rune, 32),
		closed:    make(chan struct{}),
	}
	leg.payload.Store(uint32(payload))
	go leg.receive()
	leg.pump.Do(func() { go leg.transmit() })
	return leg, nil
}

// LocalPort is the port to advertise in the SDP answer.
func (leg *rtpLeg) LocalPort() int { return leg.conn.LocalAddr().(*net.UDPAddr).Port }

func (leg *rtpLeg) payloadType() byte { return byte(leg.payload.Load()) }

// setPayload records the codec the far end chose. An offered leg carries
// nothing until its answer arrives, so this always runs before any audio.
func (leg *rtpLeg) setPayload(payload byte) { leg.payload.Store(uint32(payload)) }

func (leg *rtpLeg) eventType() byte { return byte(leg.eventPayload.Load()) }

// setEventPayload records the telephone-event type both sides agreed on. Zero
// means the far end offered none, and digits are refused rather than sent as
// tones nothing is listening for.
func (leg *rtpLeg) setEventPayload(payload byte) { leg.eventPayload.Store(uint32(payload)) }

// SendDTMF queues keypad digits for the far end.
func (leg *rtpLeg) SendDTMF(sequence string) error {
	if err := dtmf.Valid(sequence); err != nil {
		return err
	}
	if leg.eventType() == 0 {
		return errors.New("siptrunk: the PBX did not negotiate RFC 4733 telephone events")
	}
	return leg.dtmfOut.Queue(sequence)
}

// Digits reports what the far end pressed.
func (leg *rtpLeg) Digits() <-chan rune { return leg.digits }

// setRemote points the leg at the address an SDP answer named. Until this is
// called the transmit loop sends nothing, which is what keeps an offered leg
// from spraying RTP at whatever the last call was using.
func (leg *rtpLeg) setRemote(remote *net.UDPAddr) {
	leg.mu.Lock()
	defer leg.mu.Unlock()
	leg.remote = remote
}

// ReadPCM returns the next frame received from the PBX.
func (leg *rtpLeg) ReadPCM(ctx context.Context) ([]int16, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-leg.closed:
		return nil, io.EOF
	case samples := <-leg.inbound:
		return samples, nil
	}
}

// WritePCM queues audio for the PBX. The pump owns the socket, so this never
// transmits directly and never blocks on the network.
func (leg *rtpLeg) WritePCM(samples []int16) error {
	select {
	case <-leg.closed:
		return io.EOF
	default:
	}
	leg.writeMu.Lock()
	defer leg.writeMu.Unlock()
	leg.queued = append(leg.queued, samples...)
	if len(leg.queued) > maxQueuedSamples {
		leg.queued = leg.queued[len(leg.queued)-maxQueuedSamples:]
	}
	return nil
}

func (leg *rtpLeg) Close() error {
	leg.close.Do(func() {
		close(leg.closed)
		_ = leg.conn.Close()
	})
	return nil
}

func (leg *rtpLeg) transmit() {
	ticker := time.NewTicker(frameGap)
	defer ticker.Stop()
	frame := make([]int16, frameSamples)
	for {
		select {
		case <-leg.closed:
			return
		case <-ticker.C:
		}
		// A queued digit takes this interval instead of an audio frame. The
		// clock advances underneath either way, so the tone occupies real
		// time on the far end's timeline.
		if leg.dtmfOut.Pending() && leg.sendEvent() {
			leg.sequence++
			leg.timestamp += frameSamples
			continue
		}
		leg.writeMu.Lock()
		filled := copy(frame, leg.queued)
		leg.queued = leg.queued[filled:]
		leg.writeMu.Unlock()
		for index := filled; index < frameSamples; index++ {
			frame[index] = 0
		}
		leg.sendFrame(frame)
	}
}

// sendEvent transmits one telephone-event packet, reporting whether it took
// this interval. The sequence number advances for it like any other packet;
// the timestamp does not, because every packet of one event repeats the
// timestamp the event began at.
func (leg *rtpLeg) sendEvent() bool {
	leg.mu.RLock()
	var remote *net.UDPAddr
	if leg.remote != nil {
		clone := *leg.remote
		remote = &clone
	}
	leg.mu.RUnlock()
	eventPayload := leg.eventType()
	if remote == nil || eventPayload == 0 {
		return false
	}
	event, ok := leg.dtmfOut.Next(leg.timestamp, frameSamples)
	if !ok {
		return false
	}
	packet := make([]byte, 12+dtmf.PayloadBytes)
	packet[0] = 0x80
	packet[1] = eventPayload
	if event.Marker {
		packet[1] |= 0x80
	}
	binary.BigEndian.PutUint16(packet[2:4], leg.sequence)
	binary.BigEndian.PutUint32(packet[4:8], event.Timestamp)
	binary.BigEndian.PutUint32(packet[8:12], leg.ssrc)
	copy(packet[12:], event.Payload)
	_, _ = leg.conn.WriteToUDP(packet, remote)
	return true
}

// acceptEvent turns an inbound telephone-event packet into a digit, once per
// event rather than once per packet.
func (leg *rtpLeg) acceptEvent(packet []byte) {
	header := 12 + int(packet[0]&0x0f)*4
	if packet[0]&0x10 != 0 {
		if len(packet) < header+4 {
			return
		}
		header += 4 + int(binary.BigEndian.Uint16(packet[header+2:header+4]))*4
	}
	if header+dtmf.PayloadBytes > len(packet) {
		return
	}
	digit, ok := leg.dtmfIn.Accept(packet[header:], binary.BigEndian.Uint32(packet[4:8]))
	if !ok {
		return
	}
	select {
	case leg.digits <- digit:
	default:
	}
}

func (leg *rtpLeg) sendFrame(samples []int16) {
	leg.mu.RLock()
	var remote *net.UDPAddr
	if leg.remote != nil {
		clone := *leg.remote
		remote = &clone
	}
	leg.mu.RUnlock()
	if remote == nil {
		return
	}
	payload := leg.payloadType()
	packet := make([]byte, 12+frameSamples)
	packet[0], packet[1] = 0x80, payload
	binary.BigEndian.PutUint16(packet[2:4], leg.sequence)
	binary.BigEndian.PutUint32(packet[4:8], leg.timestamp)
	binary.BigEndian.PutUint32(packet[8:12], leg.ssrc)
	for index, sample := range samples {
		if payload == payloadPCMA {
			packet[12+index] = g711.LinearToALaw(sample)
		} else {
			packet[12+index] = g711.LinearToMuLaw(sample)
		}
	}
	leg.sequence++
	leg.timestamp += frameSamples
	// A send failure must not end the uplink for the rest of the call; a
	// closed socket is caught by the select in transmit.
	_, _ = leg.conn.WriteToUDP(packet, remote)
}

func (leg *rtpLeg) receive() {
	packet := make([]byte, 2048)
	for {
		count, from, err := leg.conn.ReadFromUDP(packet)
		if err != nil {
			return
		}
		payload := leg.payloadType()
		if count < 12 || packet[0]>>6 != 2 {
			continue
		}
		// A telephone event is not audio and must not be decoded as any: four
		// bytes of event payload through a G.711 table is a click.
		if eventPayload := leg.eventType(); eventPayload != 0 && packet[1]&0x7f == eventPayload {
			leg.acceptEvent(packet[:count])
			continue
		}
		if packet[1]&0x7f != payload {
			continue
		}
		// Symmetric RTP: a PBX behind NAT sends from a port it did not
		// advertise, and expects the reply there.
		leg.mu.Lock()
		if leg.remote != nil && leg.remote.IP.Equal(from.IP) && leg.remote.Port != from.Port {
			leg.remote.Port = from.Port
		}
		leg.mu.Unlock()

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
			if payload == payloadPCMA {
				samples[index] = g711.ALawToLinear(encoded)
			} else {
				samples[index] = g711.MuLawToLinear(encoded)
			}
		}
		select {
		case leg.inbound <- samples:
		default:
			// Keep real-time behaviour by dropping the oldest queued frame.
			select {
			case <-leg.inbound:
			default:
			}
			select {
			case leg.inbound <- samples:
			default:
			}
		}
	}
}
