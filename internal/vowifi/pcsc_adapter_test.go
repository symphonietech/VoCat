package vowifi

import (
	"context"
	"errors"
	"testing"

	"vocat/internal/pcsc"
)

type mockPCSCBackend struct {
	card pcsc.Card
}

func (b *mockPCSCBackend) Readers(context.Context) ([]pcsc.Reader, error) {
	return []pcsc.Reader{{Name: "USB Reader", USBPath: "1-1", CardPresent: true}}, nil
}

func (b *mockPCSCBackend) Open(context.Context, pcsc.Selector) (pcsc.Card, error) {
	return b.card, nil
}

type mockCardWithReplies struct {
	replies []mockReply
	calls   [][]byte
}

type mockReply struct {
	data []byte
	sw   uint16
}

func (c *mockCardWithReplies) Transmit(_ context.Context, command []byte) ([]byte, uint16, error) {
	c.calls = append(c.calls, append([]byte(nil), command...))
	if len(c.replies) == 0 {
		return nil, 0, errors.New("unexpected APDU")
	}
	reply := c.replies[0]
	c.replies = c.replies[1:]
	return append([]byte(nil), reply.data...), reply.sw, nil
}

func (*mockCardWithReplies) Close() error { return nil }

func TestPCSCAdapterImplementsPreferredAKAProvider(t *testing.T) {
	service := pcsc.NewWithBackend(&mockPCSCBackend{})
	adapter, err := NewPCSCAdapter(service, func(_ context.Context, _ string) (pcsc.Selector, string, error) {
		return pcsc.Selector{USBPath: "1-1"}, "", nil
	})
	if err != nil {
		t.Fatalf("NewPCSCAdapter: %v", err)
	}

	var _ PreferredAKAProvider = adapter
}

func TestPCSCAdapterAuthenticateWithPreference(t *testing.T) {
	usimRecord := []byte{0x61, 0x12, 0x4F, 0x10, 0xA0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x02, 0xFF, 0xFF, 0xFF, 0xFF, 0x89, 0x00, 0x00, 0x01, 0x00}
	isimRecord := []byte{0x61, 0x12, 0x4F, 0x10, 0xA0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x04, 0xFF, 0xFF, 0xFF, 0xFF, 0x89, 0x00, 0x00, 0x01, 0x00}
	iccidBytes := []byte{0x98, 0x10, 0x32, 0x54, 0x76, 0x98, 0x10, 0x32, 0x54, 0xF6}

	akaSuccess := []byte{0xDB, 0x08, 1, 2, 3, 4, 5, 6, 7, 8, 0x10}
	akaSuccess = append(akaSuccess, make([]byte, 16)...)
	akaSuccess = append(akaSuccess, 0x10)
	akaSuccess = append(akaSuccess, make([]byte, 16)...)

	card := &mockCardWithReplies{
		replies: []mockReply{
			{sw: 0x9000},                   // select MF (readICCID)
			{sw: 0x9000},                   // select EF_ICCID
			{data: iccidBytes, sw: 0x9000}, // read binary EF_ICCID
			{sw: 0x9000},                   // select MF (selectAKAApplication)
			{sw: 0x9000},                   // select EF_DIR
			{data: usimRecord, sw: 0x9000}, // record 1: USIM
			{data: isimRecord, sw: 0x9000}, // record 2: ISIM
			{sw: 0x6A83},                   // record 3: end of EF_DIR
			{sw: 0x9000},                   // select application (ISIM)
			{data: akaSuccess, sw: 0x9000}, // authenticate APDU
		},
	}

	service := pcsc.NewWithBackend(&mockPCSCBackend{card: card})
	adapter, err := NewPCSCAdapter(service, func(_ context.Context, _ string) (pcsc.Selector, string, error) {
		return pcsc.Selector{USBPath: "1-1"}, "", nil
	})
	if err != nil {
		t.Fatalf("NewPCSCAdapter: %v", err)
	}

	identity := SIMIdentity{
		ICCID: "8901234567890123456",
		IMSI:  "310280000000001",
	}
	// Seed binding
	adapter.bindings[identity.ICCID] = "reader-1"

	res, err := adapter.AuthenticateWithPreference(
		context.Background(),
		identity,
		AKAChallenge{},
		"isim_strict",
	)
	if err != nil {
		t.Fatalf("AuthenticateWithPreference: %v", err)
	}
	if len(res.RES) != 8 || len(res.CK) != 16 || len(res.IK) != 16 {
		t.Fatalf("unexpected AKA result: %#v", res)
	}

	// Verify APDU index 8 is SELECT application with ISIM AID (A0000000871004...)
	if len(card.calls) < 9 {
		t.Fatalf("expected >= 9 APDU calls, got %d", len(card.calls))
	}
	selectAPDU := card.calls[8]
	if len(selectAPDU) < 12 || selectAPDU[0] != 0x00 || selectAPDU[1] != 0xA4 || selectAPDU[2] != 0x04 {
		t.Fatalf("expected SELECT application APDU, got: % X", selectAPDU)
	}
	// Check AID has ISIM prefix
	aidBytes := selectAPDU[5 : 5+selectAPDU[4]]
	if string(aidBytes[:7]) != "\xa0\x00\x00\x00\x87\x10\x04" {
		t.Fatalf("expected ISIM AID prefix A0000000871004, got % X", aidBytes)
	}
}
