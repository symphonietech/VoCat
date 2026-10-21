package device

import (
	"context"
	"encoding/hex"
	"testing"

	"vocat/internal/pcsc"
)

// recordingEuiccTransport stands in for the card behind a real pcsc.Session,
// so these tests exercise the production transmit path rather than a seam cut
// into it. Every APDU is recorded exactly as it would reach a T=0 eUICC.
type recordingEuiccTransport struct {
	sent     [][]byte
	responds func(call int, apdu []byte) ([]byte, int)
}

func (transport *recordingEuiccTransport) Transmit(_ context.Context, command []byte) ([]byte, uint16, error) {
	call := len(transport.sent)
	transport.sent = append(transport.sent, append([]byte(nil), command...))
	if transport.responds == nil {
		return nil, 0x9000, nil
	}
	payload, sw := transport.responds(call, command)
	return payload, uint16(sw), nil
}

func (*recordingEuiccTransport) Close() error { return nil }

// Protocol reports T=0, which is what the Eastcompeace eUICC in the bug report
// offers -- its ATR advertises no T=1 at all.
func (*recordingEuiccTransport) Protocol() uint32 { return 1 }

type recordingBackend struct{ card *recordingEuiccTransport }

func (recordingBackend) Readers(context.Context) ([]pcsc.Reader, error) { return nil, nil }

func (backend recordingBackend) Open(context.Context, pcsc.Selector) (pcsc.Card, error) {
	return backend.card, nil
}

// newScriptedChannel wires a euiccChannel onto a real pcsc.Session backed by
// the recorder. Logical channel 2 rather than 1, so a class byte that forgets
// the channel number is visibly wrong rather than accidentally right.
func newScriptedChannel(t *testing.T, transport *recordingEuiccTransport) *euiccChannel {
	t.Helper()
	service := pcsc.NewWithBackend(recordingBackend{card: transport})
	session, err := service.OpenSession(context.Background(), pcsc.Selector{ReaderName: "AK9563"})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if name := session.ProtocolName(); name != "T=0" {
		t.Fatalf("ProtocolName = %q, want T=0", name)
	}
	return &euiccChannel{channel: 2, pcscSession: session}
}

// The regression this file exists for. A chained STORE DATA must frame its
// intermediate blocks as case 3 -- Lc and data, no Le. Appending Le makes the
// command one byte longer than a T=0 eUICC expects, and it answers 6700. Only
// the final block carries Le, because only the final block returns data.
func TestStoreDataChainedOmitsLeOnIntermediateBlocks(t *testing.T) {
	transport := &recordingEuiccTransport{}
	channel := newScriptedChannel(t, transport)

	// Just over two full blocks, so there are two intermediate blocks and a
	// short final one -- the shape AuthenticateServer produces, which is the
	// first command in the download flow big enough to chain at all.
	request := make([]byte, 2*es10SegmentMSSDirect+90)
	for index := range request {
		request[index] = byte(index)
	}
	if _, err := channel.storeDataChained(context.Background(), request); err != nil {
		t.Fatalf("storeDataChained: %v", err)
	}
	if len(transport.sent) != 3 {
		t.Fatalf("blocks = %d, want 3", len(transport.sent))
	}

	for index, apdu := range transport.sent {
		last := index == len(transport.sent)-1
		wantP1 := byte(0x11)
		if last {
			wantP1 = 0x91
		}
		if apdu[0] != 0x82 { // CLA 0x80 proprietary | channel 2
			t.Errorf("block %d CLA = %02X, want 82", index, apdu[0])
		}
		if apdu[1] != 0xE2 {
			t.Errorf("block %d INS = %02X, want E2", index, apdu[1])
		}
		if apdu[2] != wantP1 {
			t.Errorf("block %d P1 = %02X, want %02X", index, apdu[2], wantP1)
		}
		if apdu[3] != byte(index) {
			t.Errorf("block %d P2 = %02X, want %02X", index, apdu[3], index)
		}
		lc := int(apdu[4])
		wantLc := es10SegmentMSSDirect
		if last {
			wantLc = 90
		}
		if lc != wantLc {
			t.Errorf("block %d Lc = %d, want %d", index, lc, wantLc)
		}
		// The whole point: case 3 for every block but the last.
		wantLen := 5 + lc
		if last {
			wantLen++
		}
		if len(apdu) != wantLen {
			t.Errorf("block %d length = %d, want %d (Lc=%d, Le %s) -- apdu %s",
				index, len(apdu), wantLen, lc,
				map[bool]string{true: "present", false: "absent"}[last],
				hex.EncodeToString(apdu))
		}
	}
}

// A request that fits one block is the last block, so it keeps its Le. This is
// the shape that always worked, and it must keep working.
func TestStoreDataChainedKeepsLeOnASingleBlock(t *testing.T) {
	transport := &recordingEuiccTransport{}
	channel := newScriptedChannel(t, transport)

	if _, err := channel.storeDataChained(context.Background(), []byte{0xBF, 0x2E, 0x00}); err != nil {
		t.Fatalf("storeDataChained: %v", err)
	}
	if len(transport.sent) != 1 {
		t.Fatalf("blocks = %d, want 1", len(transport.sent))
	}
	apdu := transport.sent[0]
	want := []byte{0x82, 0xE2, 0x91, 0x00, 0x03, 0xBF, 0x2E, 0x00, 0x00}
	if hex.EncodeToString(apdu) != hex.EncodeToString(want) {
		t.Fatalf("apdu = %s, want %s", hex.EncodeToString(apdu), hex.EncodeToString(want))
	}
}

// No block may exceed the short-form Lc ceiling, whatever the request size:
// a T=0 card has no extended length to fall back on.
func TestStoreDataChainedNeverExceedsShortFormLc(t *testing.T) {
	transport := &recordingEuiccTransport{}
	channel := newScriptedChannel(t, transport)

	request := make([]byte, 4096)
	if _, err := channel.storeDataChained(context.Background(), request); err != nil {
		t.Fatalf("storeDataChained: %v", err)
	}
	for index, apdu := range transport.sent {
		if len(apdu) < 5 {
			t.Fatalf("block %d is truncated: %s", index, hex.EncodeToString(apdu))
		}
		if int(apdu[4]) > 255 || int(apdu[4]) == 0 {
			t.Fatalf("block %d Lc = %d", index, apdu[4])
		}
	}
}

// GET RESPONSE is interindustry, so its class is 0x0N with N the logical
// channel. 0x8N is the proprietary class and a strict card may refuse it;
// dropping the channel entirely reads the basic channel, where no response is
// pending.
func TestTransmitGetResponseUsesInterindustryClassAndChannel(t *testing.T) {
	transport := &recordingEuiccTransport{
		responds: func(call int, _ []byte) ([]byte, int) {
			if call == 0 {
				return nil, 0x6105
			}
			return []byte{0xBF, 0x38, 0x02, 0x01, 0x02}, 0x9000
		},
	}
	channel := newScriptedChannel(t, transport)

	payload, sw, err := channel.transmit(context.Background(), []byte{0x80, 0xE2, 0x91, 0x00, 0x01, 0xAA, 0x00}, 0x80)
	if err != nil {
		t.Fatalf("transmit: %v", err)
	}
	if sw != 0x9000 {
		t.Fatalf("sw = %04X, want 9000", sw)
	}
	if len(payload) != 5 {
		t.Fatalf("payload = %s", hex.EncodeToString(payload))
	}
	if len(transport.sent) != 2 {
		t.Fatalf("exchanges = %d, want 2", len(transport.sent))
	}
	getResponse := transport.sent[1]
	want := []byte{0x02, 0xC0, 0x00, 0x00, 0x05}
	if hex.EncodeToString(getResponse) != hex.EncodeToString(want) {
		t.Fatalf("GET RESPONSE = %s, want %s",
			hex.EncodeToString(getResponse), hex.EncodeToString(want))
	}
}

// 6Cxx is the card naming the Le it wants. The same command goes out again
// with that length.
func TestTransmitRetriesOn6CxxWithTheAdvisedLength(t *testing.T) {
	transport := &recordingEuiccTransport{
		responds: func(call int, _ []byte) ([]byte, int) {
			if call == 0 {
				return nil, 0x6C07
			}
			return []byte{1, 2, 3, 4, 5, 6, 7}, 0x9000
		},
	}
	channel := newScriptedChannel(t, transport)

	payload, sw, err := channel.transmit(context.Background(), []byte{0x80, 0xE2, 0x91, 0x00, 0x01, 0xAA, 0x00}, 0x80)
	if err != nil {
		t.Fatalf("transmit: %v", err)
	}
	if sw != 0x9000 || len(payload) != 7 {
		t.Fatalf("sw = %04X payload = %s", sw, hex.EncodeToString(payload))
	}
	retry := transport.sent[1]
	if retry[len(retry)-1] != 0x07 {
		t.Fatalf("retry Le = %02X, want 07 (apdu %s)", retry[len(retry)-1], hex.EncodeToString(retry))
	}
}

// A case 3 command has no Le, so there is no byte for 6Cxx to replace.
// Appending one would corrupt the command rather than correct it.
func TestTransmitDoesNotAppendLeToACaseThreeCommandOn6Cxx(t *testing.T) {
	transport := &recordingEuiccTransport{
		responds: func(int, []byte) ([]byte, int) { return nil, 0x6C07 },
	}
	channel := newScriptedChannel(t, transport)

	caseThree := []byte{0x80, 0xE2, 0x11, 0x00, 0x02, 0xAA, 0xBB}
	_, sw, err := channel.transmit(context.Background(), caseThree, 0x80)
	if err != nil {
		t.Fatalf("transmit: %v", err)
	}
	if sw != 0x6C07 {
		t.Fatalf("sw = %04X, want the 6C07 reported unchanged", sw)
	}
	if len(transport.sent) != 1 {
		t.Fatalf("exchanges = %d, want 1 (no retry)", len(transport.sent))
	}
}

func TestAPDUHasLe(t *testing.T) {
	for _, testCase := range []struct {
		name string
		apdu []byte
		want bool
	}{
		{"case 3, Lc and data only", []byte{0x80, 0xE2, 0x11, 0x00, 0x02, 0xAA, 0xBB}, false},
		{"case 4, Lc data and Le", []byte{0x80, 0xE2, 0x91, 0x00, 0x02, 0xAA, 0xBB, 0x00}, true},
		{"case 2, five bytes is Le", []byte{0x00, 0xC0, 0x00, 0x00, 0x05}, true},
		{"case 1, header only", []byte{0x00, 0x70, 0x80, 0x01}, false},
		{"truncated", []byte{0x80}, false},
	} {
		if got := apduHasLe(testCase.apdu); got != testCase.want {
			t.Errorf("%s: apduHasLe = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}
