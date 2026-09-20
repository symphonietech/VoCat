package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"vocat/internal/siptrunk"
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
	// The candidates have to be in the message. Refusing to choose is only
	// useful if the operator can act on it without going to look them up.
	for _, want := range []string{"slot1", "slot2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name candidate %q", err, want)
		}
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

// An IMS call has one RTP bridge, and its downlink is a single channel: a
// second reader would take roughly half the frames and leave the PBX's audio
// as choppy as the browser's. The browser bridge must refuse rather than
// quietly degrade a call the trunk is already carrying.
func TestTrunkClaimBlocksTheBrowserMediaBridge(t *testing.T) {
	server := &Server{logger: regionTestLogger()}
	if server.trunkHoldsCall("slot1", "call-1") {
		t.Fatal("a fresh server already holds a call")
	}
	server.claimTrunkCall("slot1", "call-1")
	if !server.trunkHoldsCall("slot1", "call-1") {
		t.Fatal("claimed call is not held")
	}
	// Scoped per device: the same call ID on another SIM is a different call.
	if server.trunkHoldsCall("slot2", "call-1") {
		t.Fatal("a claim on one device leaked to another")
	}
	server.releaseTrunkCall("slot1", "call-1")
	if server.trunkHoldsCall("slot1", "call-1") {
		t.Fatal("released call is still held")
	}
}

// Hangup runs on every teardown path, including ones that never reached
// media, so it is what must release the claim -- otherwise a failed call
// would lock the browser bridge out of that call ID for the process's life.
func TestTrunkHangupReleasesTheClaim(t *testing.T) {
	controller := &trunkVoWiFiController{ready: map[string]bool{"slot1": true}}
	server := &Server{vowifi: controller, logger: regionTestLogger()}
	gateway := &sipTrunkGateway{server: server}
	server.claimTrunkCall("slot1", "call-1")
	if err := gateway.Hangup(context.Background(), "slot1", "call-1"); err != nil {
		t.Fatal(err)
	}
	if server.trunkHoldsCall("slot1", "call-1") {
		t.Fatal("Hangup left the claim in place")
	}
}

func rotationDevices() []store.Device {
	return []store.Device{
		{ID: "slot5", Name: "SIM-5", DeviceType: store.DeviceTypePCIeEC20EC25},
		{ID: "slot6", Name: "SIM-6", DeviceType: store.DeviceTypePCIeEC20EC25},
		{ID: "slot7", Name: "SIM-7", DeviceType: store.DeviceTypePCIeEC20EC25},
		{ID: "slot8", Name: "SIM-8", DeviceType: store.DeviceTypePCIeEC20EC25},
	}
}

// Naming several SIMs is the operator opting into rotation, so successive
// calls must actually land on different cards rather than all on the first.
func TestTrunkRotatesAcrossNamedDevices(t *testing.T) {
	gateway := trunkGatewayForTest(t,
		map[string]bool{"slot5": true, "slot6": true, "slot7": true, "slot8": true},
		rotationDevices()...)

	seen := map[string]int{}
	const calls = 8
	for index := 0; index < calls; index++ {
		got, err := gateway.ResolveDevice("slot5,slot6,slot7,slot8")
		if err != nil {
			t.Fatal(err)
		}
		seen[got]++
	}
	if len(seen) != 4 {
		t.Fatalf("rotation used %d of 4 devices: %v", len(seen), seen)
	}
	for device, count := range seen {
		if count != calls/4 {
			t.Errorf("%s took %d of %d calls; want an even share", device, count, calls)
		}
	}
}

// Commas, spaces or both -- a dial plan should not have to guess the
// separator, and a stray space must not become part of an ID.
func TestTrunkAcceptsEitherHintSeparator(t *testing.T) {
	gateway := trunkGatewayForTest(t,
		map[string]bool{"slot5": true, "slot6": true},
		rotationDevices()...)
	for _, hint := range []string{"slot5,slot6", "slot5 slot6", "slot5, slot6", " slot5 ,slot6 "} {
		got, err := gateway.ResolveDevice(hint)
		if err != nil {
			t.Fatalf("ResolveDevice(%q) = %v", hint, err)
		}
		if got != "slot5" && got != "slot6" {
			t.Fatalf("ResolveDevice(%q) = %q", hint, got)
		}
	}
}

// "*" is the same opt-in, without having to list the IDs.
func TestTrunkRotatesAcrossAllWithAsterisk(t *testing.T) {
	gateway := trunkGatewayForTest(t,
		map[string]bool{"slot5": true, "slot7": true},
		rotationDevices()...)
	seen := map[string]int{}
	for index := 0; index < 4; index++ {
		got, err := gateway.ResolveDevice("*")
		if err != nil {
			t.Fatal(err)
		}
		seen[got]++
	}
	// Only the registered two take calls; slot6 and slot8 are not IMS-ready.
	if len(seen) != 2 || seen["slot5"] != 2 || seen["slot7"] != 2 {
		t.Fatalf("rotation over * = %v; want slot5 and slot7 evenly", seen)
	}
}

// A card dropping out should cost one device from the rotation, not every
// Nth call failing on a SIM that cannot dial.
func TestTrunkRotationSkipsUnregisteredDevices(t *testing.T) {
	gateway := trunkGatewayForTest(t,
		map[string]bool{"slot5": true, "slot8": true},
		rotationDevices()...)
	for index := 0; index < 6; index++ {
		got, err := gateway.ResolveDevice("slot5,slot6,slot7,slot8")
		if err != nil {
			t.Fatal(err)
		}
		if got == "slot6" || got == "slot7" {
			t.Fatalf("rotation returned %q, which is not registered", got)
		}
	}
}

// Every named device being unusable has to say which, and why -- unknown and
// offline are different mistakes with different fixes.
func TestTrunkReportsWhyNoNamedDeviceWorks(t *testing.T) {
	gateway := trunkGatewayForTest(t, map[string]bool{}, rotationDevices()...)

	_, err := gateway.ResolveDevice("slot5,slot6")
	if err == nil || !strings.Contains(err.Error(), "VoWiFi") ||
		!strings.Contains(err.Error(), "slot5") {
		t.Fatalf("all-offline error = %v; want the device IDs and the reason", err)
	}
	if _, err = gateway.ResolveDevice("nope1,nope2"); err == nil ||
		!strings.Contains(err.Error(), "nope1") {
		t.Fatalf("all-unknown error = %v; want the names", err)
	}
	if _, err = gateway.ResolveDevice("slot5,nope1"); err == nil ||
		!strings.Contains(err.Error(), "nope1") || !strings.Contains(err.Error(), "slot5") {
		t.Fatalf("mixed error = %v; want both kinds named", err)
	}
}

// The no-hint case keeps refusing. Rotation is opt-in, and an empty hint is
// not the operator asking for it.
func TestTrunkStillRefusesAnEmptyHint(t *testing.T) {
	gateway := trunkGatewayForTest(t,
		map[string]bool{"slot5": true, "slot6": true},
		rotationDevices()...)
	if _, err := gateway.ResolveDevice(""); err == nil {
		t.Fatal("an empty hint picked a device; rotation must be explicit")
	}
}

// setTrunkCalls puts a device's IMS call list in place for the busy filter.
func setTrunkCalls(t *testing.T, gateway *sipTrunkGateway, calls map[string][]vowifi.Call) {
	t.Helper()
	controller, ok := gateway.server.vowifi.(*trunkVoWiFiController)
	if !ok {
		t.Fatal("the gateway is not backed by the test controller")
	}
	controller.calls = calls
}

func activeCall() vowifi.Call {
	return vowifi.Call{ID: "in-progress", State: "active"}
}

func endedCall() vowifi.Call {
	ended := time.Now().UTC()
	return vowifi.Call{ID: "finished", State: "ended", EndedAt: &ended}
}

// A modem carries one voice call at a time, and no Asterisk dial plan can
// know that: Asterisk sees one trunk endpoint.
func TestTrunkSkipsABusyDevice(t *testing.T) {
	gateway := trunkGatewayForTest(t,
		map[string]bool{"slot1": true, "slot2": true},
		store.Device{ID: "slot1", Name: "SLOT1-1", DeviceType: store.DeviceTypePCIeEC20EC25},
		store.Device{ID: "slot2", Name: "SLOT2-4", DeviceType: store.DeviceTypePCIeEC20EC25},
	)
	setTrunkCalls(t, gateway, map[string][]vowifi.Call{"slot1": {activeCall()}})

	// Named explicitly, rotating: the busy one is skipped every time rather
	// than costing every other call.
	for attempt := 0; attempt < 4; attempt++ {
		got, err := gateway.ResolveDevice("slot1 slot2")
		if err != nil || got != "slot2" {
			t.Fatalf("attempt %d: ResolveDevice = %q, %v; want slot2", attempt, got, err)
		}
	}
	// With no hint, two registered cards still means VoCat refuses to choose,
	// whether or not one of them happens to be busy at this instant. Letting
	// busy narrow the field would turn "refuse to guess" into silently
	// billing whichever card was free.
	if _, err := gateway.ResolveDevice(""); err == nil {
		t.Fatal("an unhinted call was placed while two cards were registered")
	}
}

// A sole registered card that is busy is capacity, not a missing
// registration, and the two must not read the same.
func TestTrunkReportsASoleBusyDeviceAsBusy(t *testing.T) {
	gateway := trunkGatewayForTest(t,
		map[string]bool{"slot1": true},
		store.Device{ID: "slot1", Name: "SLOT1-1", DeviceType: store.DeviceTypePCIeEC20EC25},
	)
	setTrunkCalls(t, gateway, map[string][]vowifi.Call{"slot1": {activeCall()}})
	_, err := gateway.ResolveDevice("")
	if err == nil {
		t.Fatal("a call was placed on the only card while it was busy")
	}
	if !errors.Is(err, siptrunk.ErrNoIdleDevice) {
		t.Fatalf("a busy card was not reported as busy: %v", err)
	}
	if strings.Contains(err.Error(), "registered for VoWiFi") {
		t.Errorf("a busy card was reported as unregistered: %v", err)
	}
}

// The IMS session keeps finished calls in its list for a retention window, so
// counting calls would report a SIM busy for half a minute after every call.
func TestTrunkTreatsAnEndedCallAsIdle(t *testing.T) {
	gateway := trunkGatewayForTest(t,
		map[string]bool{"slot1": true},
		store.Device{ID: "slot1", Name: "SLOT1-1", DeviceType: store.DeviceTypePCIeEC20EC25},
	)
	setTrunkCalls(t, gateway, map[string][]vowifi.Call{"slot1": {endedCall(), endedCall()}})
	got, err := gateway.ResolveDevice("slot1")
	if err != nil || got != "slot1" {
		t.Fatalf("ResolveDevice = %q, %v; want slot1 -- a finished call is not a busy modem", got, err)
	}
}

// Every card busy is temporary and normal, so it gets its own error rather
// than being reported as a misconfiguration. The trunk turns it into a 503.
func TestTrunkReportsEveryDeviceBusy(t *testing.T) {
	gateway := trunkGatewayForTest(t,
		map[string]bool{"slot1": true, "slot2": true},
		store.Device{ID: "slot1", Name: "SLOT1-1", DeviceType: store.DeviceTypePCIeEC20EC25},
		store.Device{ID: "slot2", Name: "SLOT2-4", DeviceType: store.DeviceTypePCIeEC20EC25},
	)
	setTrunkCalls(t, gateway, map[string][]vowifi.Call{
		"slot1": {activeCall()},
		"slot2": {vowifi.Call{ID: "ringing", State: "ringing"}},
	})
	_, err := gateway.ResolveDevice("slot1 slot2")
	if err == nil {
		t.Fatal("a call was placed on a device that was already on one")
	}
	// Wrapped, so the trunk answers 503 rather than 404: this is capacity, and
	// a proxy told 404 stops looking for another route.
	if !errors.Is(err, siptrunk.ErrNoIdleDevice) {
		t.Errorf("the error is not recognisable as busy: %v", err)
	}
}

// Every state that occupies the modem counts, not only "active": a call that
// is dialling or held is one the card cannot be given away from.
func TestCallOccupiesDeviceCoversEveryLiveState(t *testing.T) {
	for _, state := range []string{"dialing", "ringing", "accepted", "active", "held"} {
		if !vowifi.CallOccupiesDevice(vowifi.Call{State: state}) {
			t.Errorf("state %q was treated as idle", state)
		}
	}
	ended := time.Now().UTC()
	for _, state := range []string{"ended", "failed"} {
		if vowifi.CallOccupiesDevice(vowifi.Call{State: state, EndedAt: &ended}) {
			t.Errorf("terminal state %q was treated as busy", state)
		}
	}
	// A state nobody has invented yet is busy by default, which is the safe
	// direction: the alternative hands a second call to an occupied modem.
	if !vowifi.CallOccupiesDevice(vowifi.Call{State: "transferring"}) {
		t.Error("an unknown state was treated as idle")
	}
}
