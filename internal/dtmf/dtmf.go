// Package dtmf carries keypad digits as RFC 4733 telephone events.
//
// Digits cannot be sent as audio here. Every leg VoCat bridges is G.711 at
// best and AMR at worst, and a codec designed for speech mangles a pair of
// pure tones just enough that an IVR on the far end hears nothing, or hears
// the wrong digit. RFC 4733 instead sends the digit as a named event in its
// own RTP payload type, which is what every carrier and every PBX expects.
//
// It lives in its own package because both RTP paths need it and neither can
// import the other: internal/vowifi/ims speaks to the carrier, internal/siptrunk
// speaks to the PBX, and a digit pressed on a softphone crosses both.
package dtmf

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

const (
	// PayloadBytes is the size of one telephone-event payload: the event, the
	// end flag and volume, and a 16-bit duration.
	PayloadBytes = 4
	// DefaultVolume is the tone level in -dBm0. RFC 4733 §2.5.1.4 allows 0-63
	// and 10 is what every implementation sends.
	DefaultVolume = 10
	// DefaultPayloadType is what to offer when nothing else is negotiated.
	// 101 is the near-universal choice; the carrier's own SDP names 100, and
	// whatever the answer says wins over both.
	DefaultPayloadType = 101

	// toneFrames is how many packets one digit lasts, at one packet per frame.
	// Eight 20 ms frames is 160 ms of tone: comfortably above the 40 ms most
	// detectors need, and short enough to dial at speed.
	toneFrames = 8
	// endFrames repeats the final packet, which is the one carrying the end
	// bit. RFC 4733 §2.5.1.4 asks for three, because losing it would leave
	// the far end holding a tone that never stops.
	endFrames = 3
	// gapFrames separates digits. Without a gap, "11" arrives as one long 1:
	// the far end has no other way to tell a repeat from a held key.
	gapFrames = 3

	maxQueuedDigits = 64
)

// events maps a dialable character to its RFC 4733 event code.
var events = map[rune]byte{
	'0': 0, '1': 1, '2': 2, '3': 3, '4': 4,
	'5': 5, '6': 6, '7': 7, '8': 8, '9': 9,
	'*': 10, '#': 11,
	'A': 12, 'B': 13, 'C': 14, 'D': 15,
}

// digits is the reverse, for decoding.
var digits = func() map[byte]rune {
	reverse := make(map[byte]rune, len(events))
	for digit, event := range events {
		reverse[event] = digit
	}
	return reverse
}()

// Event returns the event code for a digit, accepting lower-case A-D and the
// letters some keypads label as such.
func Event(digit rune) (byte, bool) {
	if digit >= 'a' && digit <= 'd' {
		digit -= 'a' - 'A'
	}
	event, ok := events[digit]
	return event, ok
}

// Digit returns the character for an event code.
func Digit(event byte) (rune, bool) {
	digit, ok := digits[event&0x0f]
	return digit, ok
}

// Valid reports whether every character of a string can be sent.
func Valid(sequence string) error {
	if strings.TrimSpace(sequence) == "" {
		return errors.New("dtmf: no digits")
	}
	if len([]rune(sequence)) > maxQueuedDigits {
		return fmt.Errorf("dtmf: more than %d digits at once", maxQueuedDigits)
	}
	for _, digit := range sequence {
		if _, ok := Event(digit); !ok {
			return fmt.Errorf("dtmf: %q is not a keypad digit", digit)
		}
	}
	return nil
}

// Encode renders one telephone-event payload.
func Encode(event byte, end bool, volume byte, duration uint16) []byte {
	payload := make([]byte, PayloadBytes)
	payload[0] = event & 0x0f
	payload[1] = volume & 0x3f
	if end {
		payload[1] |= 0x80
	}
	payload[2] = byte(duration >> 8)
	payload[3] = byte(duration)
	return payload
}

// Decoded is one telephone-event payload read off the wire.
type Decoded struct {
	Event    byte
	End      bool
	Volume   byte
	Duration uint16
}

// Decode reads one telephone-event payload.
func Decode(payload []byte) (Decoded, bool) {
	if len(payload) < PayloadBytes {
		return Decoded{}, false
	}
	return Decoded{
		Event:    payload[0] & 0x0f,
		End:      payload[1]&0x80 != 0,
		Volume:   payload[1] & 0x3f,
		Duration: uint16(payload[2])<<8 | uint16(payload[3]),
	}, true
}

// Packet is one event packet a transmit pump should send in place of audio.
type Packet struct {
	Payload []byte
	// Marker is set on the first packet of an event, which is how the far end
	// tells a new digit from a continuation of the last.
	Marker bool
	// Timestamp is the event's start, repeated on every packet of it. The
	// pump's own clock keeps advancing underneath; RFC 4733 §2.5.1.2 wants
	// the event frozen at the moment it began.
	Timestamp uint32
}

// Sender turns queued digits into packets, one per call to Next. It is driven
// by an RTP transmit pump that already owns a sequence number and a clock, and
// it deliberately holds neither: two packets of the same call must never
// disagree about either.
type Sender struct {
	mu      sync.Mutex
	pending []byte
	// frame counts packets sent within the current digit.
	frame int
	// start is the RTP timestamp the current digit began at.
	start uint32
	// active is true between the first packet of a digit and the last.
	active bool
	// gap counts frames of silence still owed after a digit.
	gap int
}

// Queue adds digits to send. They go out over the following packet intervals,
// so a long string simply takes longer rather than being dropped.
func (s *Sender) Queue(sequence string) error {
	if err := Valid(sequence); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) >= maxQueuedDigits {
		return errors.New("dtmf: too many digits already queued")
	}
	for _, digit := range sequence {
		event, _ := Event(digit)
		s.pending = append(s.pending, event)
	}
	return nil
}

// Pending reports whether anything is left to send, which a pump can use to
// avoid the mutex on the common path.
func (s *Sender) Pending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active || s.gap > 0 || len(s.pending) > 0
}

// Next returns the packet to send in place of this interval's audio frame, or
// ok false when there is nothing to send and the pump should carry on with
// audio. timestamp is the pump's clock for this interval, and samples is how
// far it advances per interval.
func (s *Sender) Next(timestamp uint32, samples uint32) (Packet, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.gap > 0 {
		// Silence between digits. The audio frame goes out as usual, which is
		// what makes two of the same digit distinguishable.
		s.gap--
		return Packet{}, false
	}
	if !s.active {
		if len(s.pending) == 0 {
			return Packet{}, false
		}
		s.active = true
		s.frame = 0
		s.start = timestamp
	}

	event := s.pending[0]
	duration := uint16((uint32(s.frame) + 1) * samples)
	end := s.frame >= toneFrames-1
	packet := Packet{
		Payload:   Encode(event, end, DefaultVolume, duration),
		Marker:    s.frame == 0,
		Timestamp: s.start,
	}
	s.frame++
	// The end packet is repeated: losing it would leave the far end holding a
	// tone that never stops.
	if s.frame >= toneFrames+endFrames-1 {
		s.pending = s.pending[1:]
		s.active = false
		s.gap = gapFrames
	}
	return packet, true
}

// Receiver turns event packets back into digits. One digit is several packets,
// all sharing a timestamp, so the timestamp is what tells a new digit from a
// retransmission of the one in progress.
type Receiver struct {
	mu      sync.Mutex
	last    uint32
	started bool
}

// Accept feeds one telephone-event payload and its RTP timestamp. It reports a
// digit on the first packet of each event, so a digit is delivered as it
// starts rather than after its tone has finished -- and the packets that
// follow, including the repeated end packets, are ignored.
func (r *Receiver) Accept(payload []byte, timestamp uint32) (rune, bool) {
	decoded, ok := Decode(payload)
	if !ok {
		return 0, false
	}
	digit, ok := Digit(decoded.Event)
	if !ok {
		return 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started && r.last == timestamp {
		return 0, false
	}
	r.started = true
	r.last = timestamp
	return digit, true
}
