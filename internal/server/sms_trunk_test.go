package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"vocat/internal/siptrunk"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

// smsTrunkController reports a number for a SIM, which is what decides
// whether an extension is allowed to send through it.
type smsTrunkController struct {
	trunkVoWiFiController
	numbers map[string]string
	iccids  map[string]string
}

func (c *smsTrunkController) State(deviceID string) (vowifi.State, error) {
	return vowifi.State{
		Enabled:     true,
		IMSReady:    true,
		ICCID:       c.iccids[deviceID],
		PhoneNumber: c.numbers[deviceID],
	}, nil
}

func smsTrunkServerForTest(t *testing.T, devices ...store.Device) (*Server, *store.Store) {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	for _, config := range devices {
		if err := database.UpsertDevice(ctx, config); err != nil {
			t.Fatal(err)
		}
	}
	controller := &smsTrunkController{
		numbers: map[string]string{"slot1": "+15551230000", "slot2": "+447700900123"},
		iccids:  map[string]string{"slot1": "iccid-1", "slot2": "iccid-2"},
	}
	return &Server{store: database, vowifi: controller, logger: regionTestLogger()}, database
}

// A SIM's own number is what an extension has to send from, and it is matched
// in either the national or the E.164 form because a modem and a network
// disagree about the leading plus all the time.
func TestTrunkSMSSenderSelectsTheSIMThatOwnsTheNumber(t *testing.T) {
	server, _ := smsTrunkServerForTest(t,
		store.Device{ID: "slot1", Name: "SLOT1-1", DeviceType: store.DeviceTypePCIeEC20EC25},
		store.Device{ID: "slot2", Name: "SLOT2-4", DeviceType: store.DeviceTypePCIeEC20EC25},
	)
	ctx := context.Background()
	for _, sender := range []string{"+15551230000", "15551230000"} {
		got, err := server.deviceForNumber(ctx, sender)
		if err != nil || got != "slot1" {
			t.Fatalf("deviceForNumber(%q) = %q, %v; want slot1", sender, got, err)
		}
	}
	if got, err := server.deviceForNumber(ctx, "+447700900123"); err != nil || got != "slot2" {
		t.Fatalf("deviceForNumber(uk) = %q, %v; want slot2", got, err)
	}
}

// The refusal is the whole security property: an extension whose name is not
// a SIM number cannot pick a SIM to bill, so it is not allowed to send.
func TestTrunkSMSRefusesASenderNoSIMOwns(t *testing.T) {
	server, _ := smsTrunkServerForTest(t,
		store.Device{ID: "slot1", Name: "SLOT1-1", DeviceType: store.DeviceTypePCIeEC20EC25},
	)
	for _, sender := range []string{"", "1001", "+15559999999"} {
		if _, err := server.deviceForNumber(context.Background(), sender); err == nil {
			t.Fatalf("deviceForNumber(%q) was allowed", sender)
		}
	}
}

// A short extension must never suffix-match a SIM number. If it did, dialling
// from extension 0000 would silently bill the SIM ending in 0000 and show its
// caller ID to the recipient.
func TestSameSMSNumberWillNotMatchAShortExtension(t *testing.T) {
	cases := []struct {
		left, right string
		want        bool
	}{
		{"+15551230000", "15551230000", true},
		{"15551230000", "5551230000", true},
		{"+15551230000", "0000", false},
		{"+15551230000", "1230000", true},
		{"+15551230000", "230000", false},
		{"+15551230000", "", false},
		{"", "", false},
		{"+15551230000", "+15559999999", false},
	}
	for _, test := range cases {
		if got := sameSMSNumber(test.left, test.right); got != test.want {
			t.Fatalf("sameSMSNumber(%q, %q) = %v; want %v",
				test.left, test.right, got, test.want)
		}
	}
}

// The history tab reads the trunk markers, so a row without one must not
// appear there: the SMS page already shows every message a SIM handled.
func TestTrunkSMSHistoryOnlyListsMessagesThatCrossedTheTrunk(t *testing.T) {
	server, database := smsTrunkServerForTest(t,
		store.Device{ID: "slot1", Name: "SLOT1-1", DeviceType: store.DeviceTypePCIeEC20EC25},
	)
	ctx := context.Background()
	plain, err := database.SaveSMSMessage(ctx, store.SMSMessage{
		MessageID: "plain", DeviceID: "slot1", Peer: "+15550001111",
		Direction: "inbound", Body: "not over the trunk", Timestamp: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	forwarded, err := database.SaveSMSMessage(ctx, store.SMSMessage{
		MessageID: "forwarded", DeviceID: "slot1", Peer: "+15550002222",
		Direction: "inbound", Body: "over the trunk", Timestamp: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.TagSMSTrunkOrigin(ctx, forwarded.ID,
		store.SMSTrunkForwardedTo, "15551230000"); err != nil {
		t.Fatal(err)
	}
	listed, err := database.ListTrunkSMS(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != forwarded.ID {
		t.Fatalf("ListTrunkSMS() returned %d rows; want only %d (not %d)",
			len(listed), forwarded.ID, plain.ID)
	}
	if got := smsTrunkTag(listed[0].Extra, store.SMSTrunkForwardedTo); got != "15551230000" {
		t.Fatalf("forwarded-to marker = %q", got)
	}
	// Tagging must not destroy whatever the ingest path already recorded.
	var fields map[string]any
	if err := json.Unmarshal(listed[0].Extra, &fields); err != nil {
		t.Fatalf("extra is not an object after tagging: %v", err)
	}
	_ = server
}

// A message the forwarder cannot address must be reported rather than sent
// somewhere: without the SIM's own number there is no request URI user, and
// guessing one would deliver a stranger's text to a handset.
func TestForwardSMSRefusesAMessageWithNoLocalNumber(t *testing.T) {
	server, _ := smsTrunkServerForTest(t,
		store.Device{ID: "slot1", Name: "SLOT1-1", DeviceType: store.DeviceTypePCIeEC20EC25},
	)
	trunk := &recordingSMSTrunk{}
	err := server.forwardSMSToPBX(context.Background(), trunk, store.SMSMessage{
		ID: 1, DeviceID: "slot1", Peer: "+15550001111", Body: "hello",
	})
	if err == nil {
		t.Fatal("forwardSMSToPBX accepted a message with no local number")
	}
	if len(trunk.sent) != 0 {
		t.Fatalf("forwardSMSToPBX sent %d messages anyway", len(trunk.sent))
	}
}

type recordingSMSTrunk struct {
	sent []siptrunk.OutboundSMS
	err  error
}

func (t *recordingSMSTrunk) SendSMS(_ context.Context, message siptrunk.OutboundSMS) error {
	if t.err != nil {
		return t.err
	}
	t.sent = append(t.sent, message)
	return nil
}

func (t *recordingSMSTrunk) InboundEnabled() bool { return true }

// The sender the handset sees has to be the person who actually texted, not
// VoCat and not the SIM: replying to the wrong number is worse than not
// showing one.
func TestForwardSMSKeepsTheOriginalSender(t *testing.T) {
	server, _ := smsTrunkServerForTest(t,
		store.Device{ID: "slot1", Name: "SLOT1-1", DeviceType: store.DeviceTypePCIeEC20EC25},
	)
	trunk := &recordingSMSTrunk{}
	if err := server.forwardSMSToPBX(context.Background(), trunk, store.SMSMessage{
		ID: 1, DeviceID: "slot1", LocalPhone: "+15551230000",
		Peer: "支付宝", Body: "hello", Direction: "inbound",
	}); err != nil {
		t.Fatal(err)
	}
	if len(trunk.sent) != 1 {
		t.Fatalf("forwardSMSToPBX sent %d messages; want 1", len(trunk.sent))
	}
	if trunk.sent[0].Sender != "支付宝" || trunk.sent[0].Recipient != "+15551230000" {
		t.Fatalf("forwarded %+v", trunk.sent[0])
	}
	if trunk.sent[0].DeviceName != "SLOT1-1" {
		t.Fatalf("device name = %q", trunk.sent[0].DeviceName)
	}
}
