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
	// protocol is the pcscd value: 1 for T=0, 2 for T=1. Zero means the
	// backend cannot say, which must leave framing alone.
	protocol uint32
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

func (transport *recordingEuiccTransport) Protocol() uint32 { return transport.protocol }

type recordingBackend struct{ card *recordingEuiccTransport }

func (recordingBackend) Readers(context.Context) ([]pcsc.Reader, error) { return nil, nil }

func (backend recordingBackend) Open(context.Context, pcsc.Selector) (pcsc.Card, error) {
	return backend.card, nil
}

// newScriptedChannel wires a euiccChannel onto a real pcsc.Session backed by
// the recorder. Logical channel 2 rather than 1, so a class byte that forgets
// the channel number is visibly wrong rather than accidentally right.
func newScriptedChannel(t *testing.T, transport *recordingEuiccTransport, wantProtocol string) *euiccChannel {
	t.Helper()
	service := pcsc.NewWithBackend(recordingBackend{card: transport})
	session, err := service.OpenSession(context.Background(), pcsc.Selector{ReaderName: "AK9563"})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if name := session.ProtocolName(); name != wantProtocol {
		t.Fatalf("ProtocolName = %q, want %q", name, wantProtocol)
	}
	return &euiccChannel{channel: 2, pcscSession: session}
}

// t0Channel is the card from the bug report: an Eastcompeace eUICC behind an
// Alcor AK9563, whose ATR advertises no T=1 at all.
func t0Channel(t *testing.T, transport *recordingEuiccTransport) *euiccChannel {
	t.Helper()
	transport.protocol = 1
	return newScriptedChannel(t, transport, "T=0")
}

func t1Channel(t *testing.T, transport *recordingEuiccTransport) *euiccChannel {
	t.Helper()
	transport.protocol = 2
	return newScriptedChannel(t, transport, "T=1")
}

// The regression this file exists for. Over T=0 a command that carries data
// carries no Le -- the two fields are mutually exclusive in a T=0 TPDU, so the
// card answers 61xx and GET RESPONSE collects the body. Appending Le makes the
// command one byte longer than the eUICC expects and it answers 6700.
//
// Measured on the reported card: a 191-byte AuthenticateServer was rejected
// 6700 both as 120+71 chained blocks and as a single 191-byte block. The only
// thing those two shapes share is the trailing byte.
func TestStoreDataChainedOmitsLeOnEveryBlockOverT0(t *testing.T) {
	transport := &recordingEuiccTransport{}
	channel := t0Channel(t, transport)

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
		// The whole point: over T=0 every block is case 3, the last one
		// included. A trailing Le makes the command one byte longer than the
		// card expects and it answers 6700.
		if len(apdu) != 5+lc {
			t.Errorf("block %d length = %d, want %d (Lc=%d, no Le) -- apdu %s",
				index, len(apdu), 5+lc, lc, hex.EncodeToString(apdu))
		}
	}
}

// A single-block request is framed the same way: data, no Le.
func TestStoreDataChainedSingleBlockOverT0CarriesNoLe(t *testing.T) {
	transport := &recordingEuiccTransport{}
	channel := t0Channel(t, transport)

	if _, err := channel.storeDataChained(context.Background(), []byte{0xBF, 0x2E, 0x00}); err != nil {
		t.Fatalf("storeDataChained: %v", err)
	}
	if len(transport.sent) != 1 {
		t.Fatalf("blocks = %d, want 1", len(transport.sent))
	}
	want := []byte{0x82, 0xE2, 0x91, 0x00, 0x03, 0xBF, 0x2E, 0x00}
	if hex.EncodeToString(transport.sent[0]) != hex.EncodeToString(want) {
		t.Fatalf("apdu = %s, want %s",
			hex.EncodeToString(transport.sent[0]), hex.EncodeToString(want))
	}
}

// T=1 can carry a data field and an expected length in the same exchange, so
// the final block keeps its Le there. This is the path that already works on
// other readers, and the T=0 fix must not reach it.
func TestStoreDataChainedKeepsLeOnTheFinalBlockOverT1(t *testing.T) {
	transport := &recordingEuiccTransport{}
	channel := t1Channel(t, transport)

	if _, err := channel.storeDataChained(context.Background(), []byte{0xBF, 0x2E, 0x00}); err != nil {
		t.Fatalf("storeDataChained: %v", err)
	}
	want := []byte{0x82, 0xE2, 0x91, 0x00, 0x03, 0xBF, 0x2E, 0x00, 0x00}
	if hex.EncodeToString(transport.sent[0]) != hex.EncodeToString(want) {
		t.Fatalf("apdu = %s, want %s",
			hex.EncodeToString(transport.sent[0]), hex.EncodeToString(want))
	}
}

// A backend that cannot report its protocol is left alone rather than
// reframed on a guess.
func TestStoreDataChainedKeepsLeWhenTheProtocolIsUnknown(t *testing.T) {
	transport := &recordingEuiccTransport{}
	channel := newScriptedChannel(t, transport, "T=?")

	if _, err := channel.storeDataChained(context.Background(), []byte{0xBF, 0x2E, 0x00}); err != nil {
		t.Fatalf("storeDataChained: %v", err)
	}
	if got := transport.sent[0]; len(got) != 9 {
		t.Fatalf("apdu = %s, want a trailing Le", hex.EncodeToString(got))
	}
}

// No block may exceed the short-form Lc ceiling, whatever the request size:
// a T=0 card has no extended length to fall back on.
func TestStoreDataChainedNeverExceedsShortFormLc(t *testing.T) {
	transport := &recordingEuiccTransport{}
	channel := t0Channel(t, transport)

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
	channel := t0Channel(t, transport)

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
	channel := t0Channel(t, transport)

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
	channel := t0Channel(t, transport)

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

// The half of the T=0 model that makes dropping Le safe: a case 3 command
// still returns its body, the card just announces it with 61xx and hands it
// over on GET RESPONSE. Without this the fix above would frame the command
// correctly and then throw the answer away.
func TestStoreDataChainedCollectsTheResponseOverT0(t *testing.T) {
	body := []byte{0xBF, 0x38, 0x03, 0x80, 0x01, 0x00}
	transport := &recordingEuiccTransport{
		responds: func(call int, apdu []byte) ([]byte, int) {
			switch call {
			case 0:
				// The STORE DATA itself: no data back, just the announcement.
				if apduHasLe(apdu) {
					t.Errorf("STORE DATA carried an Le: %s", hex.EncodeToString(apdu))
				}
				return nil, 0x6100 | len(body)
			case 1:
				return body, 0x9000
			}
			t.Fatalf("unexpected exchange %d", call)
			return nil, 0
		},
	}
	channel := t0Channel(t, transport)

	response, err := channel.storeDataChained(context.Background(), []byte{0xBF, 0x2E, 0x00})
	if err != nil {
		t.Fatalf("storeDataChained: %v", err)
	}
	if hex.EncodeToString(response) != hex.EncodeToString(body) {
		t.Fatalf("response = %s, want %s",
			hex.EncodeToString(response), hex.EncodeToString(body))
	}
	if len(transport.sent) != 2 {
		t.Fatalf("exchanges = %d, want 2 (STORE DATA then GET RESPONSE)", len(transport.sent))
	}
	getResponse := transport.sent[1]
	want := []byte{0x02, 0xC0, 0x00, 0x00, byte(len(body))}
	if hex.EncodeToString(getResponse) != hex.EncodeToString(want) {
		t.Fatalf("GET RESPONSE = %s, want %s",
			hex.EncodeToString(getResponse), hex.EncodeToString(want))
	}
}
