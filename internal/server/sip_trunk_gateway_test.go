package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"vocat/internal/store"
	"vocat/internal/vowifi"
)

// trunkVoWiFiController reports IMS readiness per device, which is what
// decides whether a SIM can carry a trunk call at all.
type trunkVoWiFiController struct {
	ready map[string]bool
	calls map[string][]vowifi.Call
}

func (c *trunkVoWiFiController) State(deviceID string) (vowifi.State, error) {
	return vowifi.State{Enabled: true, IMSReady: c.ready[deviceID]}, nil
}

func (c *trunkVoWiFiController) RequestEnabled(string, bool) (vowifi.State, error) {
	return vowifi.State{}, nil
}

func (c *trunkVoWiFiController) RequestReconnect(string) (vowifi.State, error) {
	return vowifi.State{}, nil
}

func (c *trunkVoWiFiController) Calls(deviceID string) ([]vowifi.Call, error) {
	return c.calls[deviceID], nil
}

func (c *trunkVoWiFiController) DialCall(context.Context, string, string) (vowifi.Call, error) {
	return vowifi.Call{ID: "call-1"}, nil
}

func (c *trunkVoWiFiController) AnswerCall(context.Context, string, string) (vowifi.Call, error) {
	return vowifi.Call{}, nil
}

func (c *trunkVoWiFiController) HangupCall(context.Context, string, string) error { return nil }

func trunkGatewayForTest(t *testing.T, ready map[string]bool, devices ...store.Device) *sipTrunkGateway {
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
	server := &Server{
		store:  database,
		vowifi: &trunkVoWiFiController{ready: ready},
		logger: regionTestLogger(),
	}
	gateway, ok := server.SIPTrunkGateway().(*sipTrunkGateway)
	if !ok {
		t.Fatal("SIPTrunkGateway did not return a gateway")
	}
	return gateway
}

func TestTrunkResolvesTheOnlyRegisteredDevice(t *testing.T) {
	gateway := trunkGatewayForTest(t,
		map[string]bool{"slot1": true},
		store.Device{ID: "slot1", Name: "SLOT1-1", DeviceType: store.DeviceTypePCIeEC20EC25},
		store.Device{ID: "slot2", Name: "SLOT2-4", DeviceType: store.DeviceTypePCIeEC20EC25},
	)
	got, err := gateway.ResolveDevice("")
	if err != nil || got != "slot1" {
		t.Fatalf("ResolveDevice(\"\") = %q, %v; want slot1", got, err)
	}
}

// Two SIMs and no hint must be an error rather than a guess: picking one would
// place a real, billed call on whichever happened to sort first.
func TestTrunkRefusesToGuessBetweenTwoDevices(t *testing.T) {
	gateway := trunkGatewayForTest(t,
		map[string]bool{"slot1": true, "slot2": true},
		store.Device{ID: "slot1", Name: "SLOT1-1", DeviceType: store.DeviceTypePCIeEC20EC25},
		store.Device{ID: "slot2", Name: "SLOT2-4", DeviceType: store.DeviceTypePCIeEC20EC25},
	)
	_, err := gateway.ResolveDevice("")
	if err == nil || !strings.Contains(err.Error(), "X-VoCat-Device") {
		t.Fatalf("ResolveDevice(\"\") error = %v; want a hint to name a device", err)
	}
}

func TestTrunkResolvesADeviceByIDOrName(t *testing.T) {
	gateway := trunkGatewayForTest(t,
		map[string]bool{"slot1": true, "slot2": true},
		store.Device{ID: "slot1", Name: "SLOT1-1", DeviceType: store.DeviceTypePCIeEC20EC25},
		store.Device{ID: "slot2", Name: "SLOT2-4", DeviceType: store.DeviceTypePCIeEC20EC25},
	)
	for hint, want := range map[string]string{
		"slot2": "slot2", "SLOT2-4": "slot2", "slot2-4": "slot2", "slot1": "slot1",
	} {
		got, err := gateway.ResolveDevice(hint)
		if err != nil || got != want {
			t.Errorf("ResolveDevice(%q) = %q, %v; want %q", hint, got, err, want)
		}
	}
	if _, err := gateway.ResolveDevice("slot9"); err == nil {
		t.Error("ResolveDevice for an unknown device returned no error")
	}
}

// A SIM that exists but is not IMS-registered cannot carry a trunk call, and
// saying so beats a dial that fails inside the IMS stack.
func TestTrunkRefusesADeviceWithoutIMS(t *testing.T) {
	gateway := trunkGatewayForTest(t,
		map[string]bool{},
		store.Device{ID: "slot1", Name: "SLOT1-1", DeviceType: store.DeviceTypePCIeEC20EC25},
	)
	_, err := gateway.ResolveDevice("slot1")
	if err == nil || !strings.Contains(err.Error(), "VoWiFi") {
		t.Fatalf("ResolveDevice error = %v; want a VoWiFi registration error", err)
	}
}

// Answering the PBX before media is negotiated is the silent-call failure the
// whole bridge exists to avoid, so "active" alone must not satisfy the wait.
func TestTrunkWaitsForMediaNotJustAnswer(t *testing.T) {
	controller := &trunkVoWiFiController{
		ready: map[string]bool{"slot1": true},
		calls: map[string][]vowifi.Call{"slot1": {{ID: "call-1", State: "active"}}},
	}
	gateway := &sipTrunkGateway{server: &Server{vowifi: controller, logger: regionTestLogger()}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := gateway.WaitAnswered(ctx, "slot1", "call-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitAnswered with media not ready = %v; want the wait to continue", err)
	}
	controller.calls["slot1"] = []vowifi.Call{{ID: "call-1", State: "active", MediaReady: true}}
	if err := gateway.WaitAnswered(context.Background(), "slot1", "call-1"); err != nil {
		t.Fatalf("WaitAnswered with media ready = %v", err)
	}
}

// A rejected call must surface the SIP status, since the trunk collapses every
// failure into one status toward the PBX and the log is what is left.
func TestTrunkReportsWhyACallEnded(t *testing.T) {
	controller := &trunkVoWiFiController{
		calls: map[string][]vowifi.Call{"slot1": {
			{ID: "call-1", State: "failed", SIPCode: 486, Reason: "Busy Here"},
		}},
	}
	gateway := &sipTrunkGateway{server: &Server{vowifi: controller, logger: regionTestLogger()}}
	err := gateway.WaitAnswered(context.Background(), "slot1", "call-1")
	if err == nil || !strings.Contains(err.Error(), "486") || !strings.Contains(err.Error(), "Busy Here") {
		t.Fatalf("WaitAnswered error = %v; want the SIP status", err)
	}
}
