package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"vocat/internal/asteriskconf"
	"vocat/internal/store"
)

// asteriskSMS is the wire shape of the SMS routing mode.
type asteriskSMS struct {
	Mode string `json:"mode"`
}

// handlePutAsteriskSMS saves where a text arriving on a SIM goes.
//
// It writes the same file set the extension editor does, because the SMS
// dialplan is generated from the extension list as well as the mode: the
// inbound half needs one entry per extension, and the outbound half needs one
// context per extension.
func (s *Server) handlePutAsteriskSMS(w http.ResponseWriter, r *http.Request) {
	var request asteriskSMS
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	mode := asteriskconf.SMSMode(strings.TrimSpace(request.Mode))
	if mode == "" {
		mode = asteriskconf.SMSOff
	}
	if err := mode.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_sms", err.Error())
		return
	}
	// Delivering to a handset needs somewhere to deliver to, and sending from
	// one needs somewhere to submit to. Refusing here beats writing a dialplan
	// that renders and then does nothing.
	if mode != asteriskconf.SMSOff && s.asteriskTrunkHost() == "" {
		writeError(w, http.StatusBadRequest, "invalid_sms",
			"the SIP trunk is not configured, so there is nothing to carry SMS; "+
				"set VOCAT_SIP_TRUNK_ADDR first")
		return
	}

	extensions := s.storedAsteriskExtensions(r.Context())
	plan := s.asteriskInboundPlan(r.Context(), toConfigExtensions(extensions))
	files, err := renderAsteriskExtensions(extensions, plan, mode, s.asteriskTrunkHost())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_sms", err.Error())
		return
	}
	body, err := json.Marshal(asteriskSMS{Mode: string(mode)})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not encode the SMS mode")
		return
	}
	if err := s.store.UpsertAppSetting(r.Context(), store.AppSetting{
		Key: asteriskSMSKey, Value: body,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	written := false
	if s.asteriskDialplanDir != "" {
		if err := s.writeAsteriskExtensionFiles(files); err != nil {
			writeError(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
		written = true
	}
	s.recordAudit(r.Context(), "admin", "asterisk.sms.save", "asterisk", "sms", "success", string(mode))
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"saved": true, "written": written,
		"sms":         asteriskSMS{Mode: string(mode)},
		"sms_preview": files.messages,
		"trunk_host":  s.asteriskTrunkHost(),
	}})
}

// asteriskSMSHistoryLimit caps one read of the trunk's SMS history.
//
// The tab groups by extension and renders every message in the selected
// thread, so the limit is a page of the whole history rather than of one
// conversation. Large enough that a busy day is complete, small enough that
// the browser is not asked to lay out a year of texts at once.
const asteriskSMSHistoryLimit = 500

// handleAsteriskSMSHistory returns the texts that crossed the SIP trunk.
//
// Only those: the SMS page already shows everything a SIM sent or received,
// and repeating it here would bury the handful of messages that actually
// involved an extension. The extension each message belongs to is resolved
// from the trunk markers the forwarder and the gateway write.
func (s *Server) handleAsteriskSMSHistory(w http.ResponseWriter, r *http.Request) {
	messages, err := s.store.ListTrunkSMS(r.Context(), asteriskSMSHistoryLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	payload := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		row := storedSMSResponse(message)
		// Which extension this message belongs to, and how it got there.
		// Outbound rows carry the extension that submitted them; inbound
		// rows carry the extension they were delivered to.
		if extension := smsTrunkTag(message.Extra, store.SMSTrunkOriginExtension); extension != "" {
			row["extension"] = extension
			row["trunk_role"] = "sent_by_extension"
		} else if extension := smsTrunkTag(message.Extra, store.SMSTrunkForwardedTo); extension != "" {
			row["extension"] = extension
			row["trunk_role"] = "delivered_to_extension"
		}
		payload = append(payload, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"messages": payload,
		"limit":    asteriskSMSHistoryLimit,
	}})
}
