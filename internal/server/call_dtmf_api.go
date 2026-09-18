package server

import (
	"net/http"
	"strings"

	"vocat/internal/dtmf"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

// handleCallDTMF sends keypad digits into an active call.
//
// Separate from handleCallAction because it is not a call-control command:
// dial, answer and hangup all have an AT equivalent on a native modem, and
// this one runs over the call's own RTP stream. A device on the native
// transport therefore gets a plain "not here" rather than a silent no-op.
func (s *Server) handleCallDTMF(w http.ResponseWriter, r *http.Request, config store.Device) bool {
	if !requireMethod(w, r, http.MethodPost) {
		return true
	}
	var request struct {
		CallID string `json:"call_id"`
		Digits string `json:"digits"`
	}
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return true
	}
	digits := strings.TrimSpace(request.Digits)
	if err := dtmf.Valid(digits); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_digits", err.Error())
		return true
	}
	if transport := s.callTransport(config.ID); transport != "vowifi" {
		writeError(w, http.StatusNotImplemented, "dtmf_unavailable",
			"digits can only be sent on a VoWiFi call, which carries them as RFC 4733 events")
		return true
	}
	controller, ok := s.vowifi.(VoWiFiCallController)
	if !ok {
		writeError(w, http.StatusNotImplemented, "vowifi_voice_unavailable",
			"the active VoWiFi IMS session does not expose voice-call signalling")
		return true
	}
	callID, err := resolveVoWiFiCallID(controller, config.ID, strings.TrimSpace(request.CallID), "active")
	if err != nil {
		writeError(w, http.StatusNotFound, "call_not_found", err.Error())
		return true
	}
	media, ok := s.vowifi.(VoWiFiCallMediaController)
	if !ok {
		writeError(w, http.StatusNotImplemented, "vowifi_media_unavailable",
			"the active VoWiFi IMS session does not expose RTP media")
		return true
	}
	stream, err := media.CallMedia(r.Context(), config.ID, callID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "vowifi_call_failed", err.Error())
		return true
	}
	sender, ok := stream.(vowifi.CallDTMFSender)
	if !ok {
		writeError(w, http.StatusNotImplemented, "dtmf_unavailable",
			"this media implementation cannot send digits")
		return true
	}
	if err := sender.SendDTMF(digits); err != nil {
		// A carrier that declined telephone-event is the common case worth
		// naming: nothing is broken, the call simply cannot carry digits.
		s.logger.Warn("could not send DTMF",
			"category", "call", "device_id", config.ID, "call_id", callID, "error", err)
		writeError(w, http.StatusConflict, "dtmf_failed", err.Error())
		return true
	}
	s.recordAudit(r.Context(), "admin", "call.dtmf", "device", config.ID, "success", "")
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"sent":   true,
		"digits": digits,
		// Digits go out over the call's own RTP stream at one per interval,
		// so a long string takes time rather than arriving at once.
		"call_id": callID,
	}})
	return true
}
