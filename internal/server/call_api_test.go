package server

import (
	"context"
	"testing"

	"vocat/internal/modem"
	"vocat/internal/vowifi"
)

// The web UI reads one vocabulary for calls whatever carried them, so the
// cellular path reports the same words the VoWiFi one does. It used to hand
// over the raw 27.007 integers, which the page rendered as "4" and never
// matched against "incoming" -- so a cellular call got no Answer button and
// an unreadable state.
func TestParseCLCCSpeaksTheSameVocabularyAsVoWiFi(t *testing.T) {
	calls := parseCLCC(modem.Response{Lines: []string{
		`+CLCC: 1,1,4,0,0,"+447700900000",145`,
		`+CLCC: 2,0,0,0,0,"12345",129`,
	}})
	if len(calls) != 2 {
		t.Fatalf("parseCLCC = %#v", calls)
	}
	if calls[0]["number"] != "+447700900000" ||
		calls[0]["direction"] != "incoming" || calls[0]["state"] != "ringing" {
		t.Errorf("incoming call = %#v", calls[0])
	}
	if calls[1]["direction"] != "outgoing" || calls[1]["state"] != "active" {
		t.Errorf("outgoing call = %#v", calls[1])
	}
	// An id, because the page keys rows by it and hang-up sends it back.
	if calls[0]["id"] != "1" || calls[1]["id"] != "2" {
		t.Errorf("ids = %v, %v", calls[0]["id"], calls[1]["id"])
	}
	// The raw codes survive under their own names: anything that acts on the
	// modem, or explains what it said, needs the number rather than the word.
	if calls[0]["state_code"] != 4 || calls[0]["direction_code"] != 1 {
		t.Errorf("raw codes lost: %#v", calls[0])
	}
}

// A mode outside the standard is kept rather than dropped: hiding a real
// voice call because its mode was unfamiliar is the worse mistake.
func TestParseCLCCKeepsAnUnfamiliarMode(t *testing.T) {
	calls := parseCLCC(modem.Response{Lines: []string{`+CLCC: 1,0,0,9,0,"12345",129`}})
	if len(calls) != 1 {
		t.Fatalf("an unfamiliar call mode was dropped: %#v", calls)
	}
}

func TestValidDialNumber(t *testing.T) {
	for _, value := range []string{"+447700900000", "12345", "*100#"} {
		if !validDialNumber(value) {
			t.Errorf("validDialNumber(%q) = false", value)
		}
	}
	for _, value := range []string{"", "+", "12;ATH", "12 34", "abc"} {
		if validDialNumber(value) {
			t.Errorf("validDialNumber(%q) = true", value)
		}
	}
}

func TestCallTransportRequiresIMSReady(t *testing.T) {
	controller := &fakeVoWiFiController{state: vowifi.State{Enabled: true}}
	server := &Server{vowifi: controller}
	if got := server.callTransport("ec20"); got != "cellular" {
		t.Fatalf("callTransport before IMS registration = %q, want cellular", got)
	}
	controller.state.IMSReady = true
	if got := server.callTransport("ec20"); got != "vowifi" {
		t.Fatalf("callTransport with IMS ready = %q, want vowifi", got)
	}
}

func TestResolveVoWiFiCallIDIgnoresTerminalCalls(t *testing.T) {
	controller := &fakeCallController{calls: []vowifi.Call{
		{ID: "failed", State: "failed"},
		{ID: "active", State: "active"},
	}}
	got, err := resolveVoWiFiCallID(controller, "ec20", "", "")
	if err != nil || got != "active" {
		t.Fatalf("resolveVoWiFiCallID() = %q, %v; want active", got, err)
	}
}

type fakeCallController struct {
	calls []vowifi.Call
}

func (controller *fakeCallController) Calls(string) ([]vowifi.Call, error) {
	return controller.calls, nil
}

func (*fakeCallController) DialCall(context.Context, string, string) (vowifi.Call, error) {
	return vowifi.Call{}, nil
}

func (*fakeCallController) AnswerCall(context.Context, string, string) (vowifi.Call, error) {
	return vowifi.Call{}, nil
}

func (*fakeCallController) HangupCall(context.Context, string, string) error { return nil }
