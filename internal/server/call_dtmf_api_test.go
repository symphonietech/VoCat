package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vocat/internal/store"
)

func dtmfRequest(t *testing.T, server *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/devices/d1/calls/dtmf", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleCallDTMF(response, request, store.Device{ID: "d1"})
	return response
}

// A dial string with a letter in it is a mistake worth naming, and it must be
// caught before anything reaches a live call.
func TestCallDTMFRefusesWhatIsNotAKeypadDigit(t *testing.T) {
	server := &Server{logger: regionTestLogger(), maxRequestBodyBytes: 4096}
	for _, digits := range []string{"", "12x", "+1", "hello"} {
		response := dtmfRequest(t, server, `{"call_id":"c1","digits":"`+digits+`"}`)
		if response.Code != http.StatusBadRequest {
			t.Errorf("digits %q gave status %d, want 400", digits, response.Code)
		}
	}
	// The whole keypad is valid, including the two characters every phone
	// menu is opened with.
	response := dtmfRequest(t, server, `{"call_id":"c1","digits":"*123#"}`)
	if response.Code == http.StatusBadRequest {
		t.Fatalf("a valid keypad string was refused: %s", response.Body.String())
	}
}

// With no IMS session a call runs over the modem's circuit-switched path,
// which has no RTP for VoCat to put events on. Saying so beats a silent no-op
// that looks like the digit was sent.
func TestCallDTMFReportsThatACellularCallCannotCarryDigits(t *testing.T) {
	server := &Server{logger: regionTestLogger(), maxRequestBodyBytes: 4096}
	response := dtmfRequest(t, server, `{"call_id":"c1","digits":"1"}`)
	if response.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "VoWiFi") {
		t.Fatalf("the refusal does not say why: %s", response.Body.String())
	}
}

// Only POST: a GET here would look like a way to read what was pressed.
func TestCallDTMFRequiresPOST(t *testing.T) {
	server := &Server{logger: regionTestLogger(), maxRequestBodyBytes: 4096}
	request := httptest.NewRequest(http.MethodGet, "/api/devices/d1/calls/dtmf", nil)
	response := httptest.NewRecorder()
	server.handleCallDTMF(response, request, store.Device{ID: "d1"})
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", response.Code)
	}
}
