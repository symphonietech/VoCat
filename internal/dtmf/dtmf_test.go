package dtmf

import "testing"

// The wire format is fixed by RFC 4733 §2.5.1.4. Getting a bit wrong here
// means an IVR hears a different digit, or none, with nothing to see in a log.
func TestEncodeMatchesTheWireFormat(t *testing.T) {
	payload := Encode(11, true, DefaultVolume, 1280) // "#", ended, 160 ms
	if len(payload) != PayloadBytes {
		t.Fatalf("payload is %d bytes", len(payload))
	}
	if payload[0] != 11 {
		t.Errorf("event = %d, want 11", payload[0])
	}
	if payload[1]&0x80 == 0 {
		t.Error("the end bit was not set")
	}
	if payload[1]&0x3f != DefaultVolume {
		t.Errorf("volume = %d", payload[1]&0x3f)
	}
	if uint16(payload[2])<<8|uint16(payload[3]) != 1280 {
		t.Errorf("duration = %d", uint16(payload[2])<<8|uint16(payload[3]))
	}
	decoded, ok := Decode(payload)
	if !ok || decoded.Event != 11 || !decoded.End || decoded.Duration != 1280 {
		t.Fatalf("round trip = %+v (ok %v)", decoded, ok)
	}
}

// The keypad is not only digits: * and # open most IVR menus, and A-D exist
// on the ones that still use them.
func TestEventCoversTheWholeKeypad(t *testing.T) {
	for digit, want := range map[rune]byte{
		'0': 0, '9': 9, '*': 10, '#': 11, 'A': 12, 'D': 15,
	} {
		got, ok := Event(digit)
		if !ok || got != want {
			t.Errorf("Event(%q) = %d, %v; want %d", digit, got, ok, want)
		}
		back, ok := Digit(want)
		if !ok || back != digit {
			t.Errorf("Digit(%d) = %q, %v; want %q", want, back, ok, digit)
		}
	}
	// Lower case is what a keypad sends when someone types it.
	if event, ok := Event('b'); !ok || event != 13 {
		t.Errorf("lower-case b = %d, %v", event, ok)
	}
	for _, bad := range []rune{'e', ' ', '+', 'é'} {
		if _, ok := Event(bad); ok {
			t.Errorf("%q was accepted as a keypad digit", bad)
		}
	}
}

// Anything that is not a keypad digit has to be refused before it reaches a
// call: a dial string with a letter in it is a mistake worth naming.
func TestValidNamesTheOffendingCharacter(t *testing.T) {
	if err := Valid("123"); err != nil {
		t.Fatal(err)
	}
	if err := Valid(""); err == nil {
		t.Error("an empty sequence was accepted")
	}
	if err := Valid("12x"); err == nil {
		t.Error("a letter was accepted")
	}
}

// One digit is a burst of packets sharing a timestamp, the last few carrying
// the end bit. This is the shape a far-end detector expects.
func TestSenderEmitsOneDigitAsAToneThenAnEnd(t *testing.T) {
	var sender Sender
	if err := sender.Queue("5"); err != nil {
		t.Fatal(err)
	}
	const samples = 160
	timestamp := uint32(1000)
	var packets []Packet
	for step := 0; step < 20; step++ {
		packet, ok := sender.Next(timestamp, samples)
		timestamp += samples
		if ok {
			packets = append(packets, packet)
		}
	}
	if len(packets) != toneFrames+endFrames-1 {
		t.Fatalf("sent %d packets, want %d", len(packets), toneFrames+endFrames-1)
	}
	if !packets[0].Marker {
		t.Error("the first packet of a digit has no marker bit")
	}
	for index, packet := range packets {
		if index > 0 && packet.Marker {
			t.Errorf("packet %d carries a marker bit mid-digit", index)
		}
		// Every packet of one event repeats the timestamp it began at.
		if packet.Timestamp != 1000 {
			t.Errorf("packet %d timestamp = %d, want 1000", index, packet.Timestamp)
		}
		decoded, _ := Decode(packet.Payload)
		if decoded.Event != 5 {
			t.Errorf("packet %d event = %d", index, decoded.Event)
		}
	}
	// The duration grows while the tone is on, and the last packets are the
	// repeated end.
	first, _ := Decode(packets[0].Payload)
	last, _ := Decode(packets[len(packets)-1].Payload)
	if first.End {
		t.Error("the first packet already ended the tone")
	}
	if !last.End {
		t.Error("the last packet does not end the tone")
	}
	if last.Duration <= first.Duration {
		t.Errorf("duration did not grow: %d then %d", first.Duration, last.Duration)
	}
}

// "11" must not arrive as one long 1. The gap between digits is the only
// thing that distinguishes a repeat from a held key.
func TestSenderSeparatesRepeatedDigits(t *testing.T) {
	var sender Sender
	if err := sender.Queue("11"); err != nil {
		t.Fatal(err)
	}
	const samples = 160
	timestamp := uint32(0)
	starts := map[uint32]bool{}
	silences := 0
	sawDigit := false
	for step := 0; step < 40; step++ {
		packet, ok := sender.Next(timestamp, samples)
		timestamp += samples
		if !ok {
			if sawDigit && sender.Pending() {
				silences++
			}
			continue
		}
		sawDigit = true
		if packet.Marker {
			starts[packet.Timestamp] = true
		}
	}
	if len(starts) != 2 {
		t.Fatalf("saw %d digit starts, want 2", len(starts))
	}
	if silences == 0 {
		t.Fatal("the two digits ran together with no gap")
	}
}

// Nothing queued means the pump sends audio, which is the common case on
// every packet of every call.
func TestSenderIsSilentWhenNothingIsQueued(t *testing.T) {
	var sender Sender
	if sender.Pending() {
		t.Fatal("a fresh sender reports work to do")
	}
	if _, ok := sender.Next(0, 160); ok {
		t.Fatal("a fresh sender produced a packet")
	}
}

// A digit arrives as several packets. Reporting it once, as it starts, is
// what keeps "5" from being delivered as five 5s.
func TestReceiverReportsEachDigitOnce(t *testing.T) {
	var receiver Receiver
	tone := Encode(7, false, DefaultVolume, 160)
	end := Encode(7, true, DefaultVolume, 1280)

	digit, ok := receiver.Accept(tone, 5000)
	if !ok || digit != '7' {
		t.Fatalf("first packet = %q, %v", digit, ok)
	}
	for _, packet := range [][]byte{tone, tone, end, end, end} {
		if _, ok := receiver.Accept(packet, 5000); ok {
			t.Fatal("a continuation packet was reported as a second digit")
		}
	}
	// A new timestamp is a new digit, even the same one.
	digit, ok = receiver.Accept(tone, 6400)
	if !ok || digit != '7' {
		t.Fatalf("second digit = %q, %v", digit, ok)
	}
}

// A short or unknown payload is something else on the wire, not a digit.
func TestReceiverIgnoresWhatIsNotAnEvent(t *testing.T) {
	var receiver Receiver
	if _, ok := receiver.Accept([]byte{1, 2}, 0); ok {
		t.Error("a truncated payload was decoded")
	}
	// Event codes above 15 are reserved for things that are not keypad
	// digits, and the low nibble must not be mistaken for one.
	if _, ok := receiver.Accept([]byte{0x20, 0x0a, 0, 160}, 1); !ok {
		t.Error("event 0 in the low nibble was dropped")
	}
}
