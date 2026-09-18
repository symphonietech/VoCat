package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"vocat/internal/ami"
)

// The two ends of a leg are what make a stuck channel identifiable, and
// Asterisk's "<unknown>" for an absent one is noise in a table.
func TestBuildAsteriskChannelsReadsBothEnds(t *testing.T) {
	channels := buildAsteriskChannels([]ami.Message{{
		"Event": "CoreShowChannel", "Channel": "PJSIP/1001-0000000a",
		"ChannelStateDesc": "Up", "CallerIDNum": "1001", "CallerIDName": "Front Desk",
		"ConnectedLineNum": "12125551234", "ConnectedLineName": "<unknown>",
		"Context": "from-internal", "Exten": "12125551234", "Application": "Dial",
		"Duration": "00:01:23", "BridgeId": "abc", "Uniqueid": "1700000000.1",
	}})
	if len(channels) != 1 {
		t.Fatalf("got %d channels", len(channels))
	}
	channel := channels[0]
	if channel.CallerID != "Front Desk <1001>" {
		t.Errorf("caller = %q", channel.CallerID)
	}
	// A name of "<unknown>" must not be rendered; the number alone is the
	// useful part.
	if channel.ConnectedLine != "12125551234" {
		t.Errorf("connected = %q", channel.ConnectedLine)
	}
	if channel.State != "Up" || channel.Duration != "00:01:23" || channel.BridgeID != "abc" {
		t.Errorf("channel = %+v", channel)
	}
	// Raw fields, so a key that moved between versions stays visible.
	found := false
	for _, field := range channel.Fields {
		if field.Name == "Uniqueid" {
			found = true
		}
	}
	if !found {
		t.Error("raw fields were dropped")
	}
}

// A channel row with no name is not a channel, and sorting keeps the list
// stable between polls rather than jumping about.
func TestBuildAsteriskChannelsSkipsAndSorts(t *testing.T) {
	channels := buildAsteriskChannels([]ami.Message{
		{"Event": "CoreShowChannel", "Channel": "PJSIP/1002-2"},
		{"Event": "CoreShowChannel"},
		{"Event": "CoreShowChannel", "Channel": "PJSIP/1001-1"},
	})
	if len(channels) != 2 {
		t.Fatalf("got %d channels, want 2", len(channels))
	}
	if channels[0].Name != "PJSIP/1001-1" {
		t.Fatalf("not sorted: %+v", channels)
	}
}

// AMI's Hangup takes a regular expression when the value is wrapped in
// slashes, so "/./" would end every call on the PBX. Requiring an exact match
// against a live channel is what makes that unreachable.
func TestAsteriskHangupRefusesWhatIsNotALiveChannel(t *testing.T) {
	server := &Server{
		logger:              regionTestLogger(),
		maxRequestBodyBytes: 4096,
		asteriskAMI:         ami.Options{Address: "127.0.0.1:1", Timeout: 200 * time.Millisecond},
	}
	// Unreachable AMI, so this asserts only that a regular expression is not
	// sent anywhere before the channel list has confirmed it: the request
	// fails at the connection rather than at the Hangup.
	request := httptest.NewRequest(http.MethodPost, "/api/asterisk/channels/hangup",
		strings.NewReader(`{"channel":"/./"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleAsteriskHangup(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 from the unreachable PBX: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "ami_unreachable") {
		t.Fatalf("the request got past the connection: %s", response.Body.String())
	}
}

// Validation happens before anything is dialled, so an obviously wrong
// request costs no round trip.
func TestAsteriskHangupValidatesItsInput(t *testing.T) {
	server := &Server{
		logger:              regionTestLogger(),
		maxRequestBodyBytes: 4096,
		asteriskAMI:         ami.Options{Address: "127.0.0.1:1", Timeout: 200 * time.Millisecond},
	}
	for name, body := range map[string]string{
		"no channel":     `{"channel":"  "}`,
		"cause too big":  `{"channel":"PJSIP/1001-1","cause":900}`,
		"cause negative": `{"channel":"PJSIP/1001-1","cause":-1}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/asterisk/channels/hangup", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.handleAsteriskHangup(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, response.Code)
		}
	}
}

// With no manager address the feature does not exist, which is a different
// answer from "the PBX refused".
func TestAsteriskHangupSaysWhenAMIIsNotConfigured(t *testing.T) {
	server := &Server{logger: regionTestLogger(), maxRequestBodyBytes: 4096}
	request := httptest.NewRequest(http.MethodPost, "/api/asterisk/channels/hangup",
		strings.NewReader(`{"channel":"PJSIP/1001-1"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleAsteriskHangup(response, request)
	if response.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", response.Code)
	}
}
