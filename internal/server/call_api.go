package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"vocat/internal/modem"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

const maxCallDuration = 10 * time.Minute

func (s *Server) handleCalls(w http.ResponseWriter, r *http.Request, config store.Device, physicalID string) bool {
	if !requireMethod(w, r, http.MethodGet) {
		return true
	}
	transport := s.callTransport(config.ID)
	if transport == "vowifi" {
		controller, ok := s.vowifi.(VoWiFiCallController)
		if !ok {
			writeError(w, http.StatusNotImplemented, "vowifi_voice_unavailable", "the active VoWiFi IMS session does not expose voice-call signalling")
			return true
		}
		calls, err := controller.Calls(config.ID)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "vowifi_call_failed", err.Error())
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"device_id": config.ID, "transport": transport, "calls": calls,
		}})
		return true
	}
	response, err := s.devices.ExecuteAT(r.Context(), physicalID, "AT+CLCC")
	if err != nil {
		s.writeDeviceError(w, err)
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"device_id": config.ID,
			"transport": transport,
			"calls":     parseCLCC(response),
			"raw":       response.Text(),
		},
	})
	return true
}

func (s *Server) handleCallAction(w http.ResponseWriter, r *http.Request, config store.Device, physicalID, action string) bool {
	if !requireMethod(w, r, http.MethodPost) {
		return true
	}
	command := ""
	duration := time.Duration(0)
	number := ""
	callID := ""
	switch action {
	case "dial":
		var request struct {
			Number          string `json:"number"`
			DurationSeconds int    `json:"duration_seconds"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return true
		}
		number = strings.TrimSpace(request.Number)
		if !validDialNumber(number) {
			writeError(w, http.StatusBadRequest, "invalid_number", "phone number is invalid")
			return true
		}
		duration = time.Duration(request.DurationSeconds) * time.Second
		if duration < 0 || duration > maxCallDuration {
			writeError(w, http.StatusBadRequest, "invalid_duration", "duration_seconds must be 0 (no automatic hang-up) or between 1 and 600")
			return true
		}
		command = "ATD" + number + ";"
	case "answer":
		var request struct {
			CallID string `json:"call_id"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return true
		}
		callID = strings.TrimSpace(request.CallID)
		command = "ATA"
	case "hangup":
		var request struct {
			CallID string `json:"call_id"`
		}
		if err := s.decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return true
		}
		callID = strings.TrimSpace(request.CallID)
		command = "ATH"
	default:
		writeError(w, http.StatusNotFound, "not_found", "call action not found")
		return true
	}

	transport := s.callTransport(config.ID)
	if transport == "vowifi" {
		controller, ok := s.vowifi.(VoWiFiCallController)
		if !ok {
			writeError(w, http.StatusNotImplemented, "vowifi_voice_unavailable", "the active VoWiFi IMS session does not expose voice-call signalling")
			return true
		}
		var result any
		var err error
		switch action {
		case "dial":
			result, err = controller.DialCall(r.Context(), config.ID, number)
		case "answer":
			callID, err = resolveVoWiFiCallID(controller, config.ID, callID, "ringing")
			if err == nil {
				result, err = controller.AnswerCall(r.Context(), config.ID, callID)
			}
		case "hangup":
			callID, err = resolveVoWiFiCallID(controller, config.ID, callID, "")
			if err == nil {
				err = controller.HangupCall(r.Context(), config.ID, callID)
			}
		}
		if err != nil {
			s.logger.Warn("VoWiFi call operation failed",
				"category", "call", "event", "call."+action,
				"device_id", config.ID, "number", number, "call_id", callID,
				"transport", transport, "raw_error", err,
			)
			writeError(w, http.StatusBadGateway, "vowifi_call_failed", err.Error())
			return true
		}
		if action == "dial" {
			if call, ok := result.(vowifi.Call); ok {
				callID = call.ID
			}
			if duration > 0 {
				go s.hangupVoWiFiAfter(config.ID, callID, duration)
			}
		}
		s.recordAudit(r.Context(), "admin", "call."+action, "device", config.ID, "success", transport)
		writeJSON(w, http.StatusAccepted, map[string]any{"data": map[string]any{
			"accepted": true, "action": action, "number": number, "call_id": callID,
			"duration_seconds": int(duration / time.Second), "transport": transport, "call": result,
		}})
		return true
	}
	operationContext, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	response, err := s.devices.ExecuteAT(operationContext, physicalID, command)
	cancel()
	if err != nil {
		s.writeDeviceError(w, err)
		return true
	}
	if !strings.EqualFold(strings.TrimSpace(response.Final), "OK") {
		s.logger.Warn("cellular call operation rejected",
			"category", "call", "event", "call."+action,
			"device_id", config.ID, "number", number, "transport", transport,
			"modem_final", response.Final, "raw_response", response.Text(),
		)
		writeError(w, http.StatusBadGateway, "call_rejected", "modem did not accept the call action")
		return true
	}
	if action == "dial" {
		if duration > 0 {
			go s.hangupAfter(config.ID, physicalID, duration)
		}
	}
	s.recordAudit(r.Context(), "admin", "call."+action, "device", config.ID, "success", transport)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"data": map[string]any{
			"accepted": true, "action": action, "number": number,
			"duration_seconds": int(duration / time.Second), "transport": transport,
		},
	})
	return true
}

func resolveVoWiFiCallID(controller VoWiFiCallController, deviceID, id, requiredState string) (string, error) {
	if id != "" {
		return id, nil
	}
	calls, err := controller.Calls(deviceID)
	if err != nil {
		return "", err
	}
	for _, call := range calls {
		if call.State != "ended" && call.State != "failed" && (requiredState == "" || call.State == requiredState) {
			return call.ID, nil
		}
	}
	return "", errors.New("no matching active call")
}

func (s *Server) hangupVoWiFiAfter(deviceID, callID string, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	<-timer.C
	controller, ok := s.vowifi.(VoWiFiCallController)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := controller.HangupCall(ctx, deviceID, callID); err != nil {
		s.logger.Warn("automatic VoWiFi call hangup failed", "device_id", deviceID, "call_id", callID, "error", err)
	}
}

func (s *Server) callTransport(deviceID string) string {
	if s.vowifi != nil {
		// Enabled is only the desired card policy. Calls can use IMS only after
		// registration has actually completed; otherwise keep using the modem's
		// circuit-switched call path instead of routing into an unavailable IMS
		// session.
		if state, err := s.vowifi.State(deviceID); err == nil && state.IMSReady {
			return "vowifi"
		}
	}
	return "cellular"
}

func (s *Server) hangupAfter(deviceID, physicalID string, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	<-timer.C
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := s.devices.ExecuteAT(ctx, physicalID, "ATH"); err != nil {
		s.logger.Warn("automatic call hangup failed", "device_id", deviceID, "error", err)
	}
}

func validDialNumber(value string) bool {
	if len(value) < 2 || len(value) > 32 {
		return false
	}
	for index, character := range value {
		if character >= '0' && character <= '9' || (index == 0 && character == '+') || character == '*' || character == '#' {
			continue
		}
		return false
	}
	return true
}

// clccStates maps the call state 3GPP TS 27.007 gives +CLCC onto the names the
// VoWiFi path already reports, because the web UI reads one vocabulary.
//
// There is no "ended": a finished call simply stops being listed, which is why
// a cellular call never carries an ended_at and why a call the modem still
// lists is, by definition, still up.
var clccStates = map[int]string{
	0: "active",
	1: "held",
	2: "dialing",
	3: "ringing",
	4: "ringing",
	5: "waiting",
}

// nonVoiceCLCCModes are the +CLCC call modes that are not a voice call.
//
// EC20/EC25 firmware lists an active packet-data session as a CLCC record, so
// a modem with mobile data up reports a "call" for as long as data is
// connected. That record used to reach the Calls page, where it read as a call
// in progress and replaced the Dial button with Hang up -- making it look as
// though a cellular SIM could not place a call at all.
//
// A deny list rather than an allow list: the voice modes are 0 and 3-5, but
// firmware puts values outside the standard here, and hiding a real voice call
// because its mode was unfamiliar is the worse mistake.
var nonVoiceCLCCModes = map[int]bool{
	1: true, // data
	2: true, // fax
	6: true, // voice followed by data, data part
	7: true, // alternating voice/data, data part
	8: true, // alternating voice/fax, fax part
}

// parseCLCC turns +CLCC lines into the same shape the VoWiFi call list has.
//
// It used to return the raw integers under their 27.007 names, which the page
// then rendered as-is: the state showed as "2", an incoming call never matched
// the check that offers an Answer button, and every row looked like a call in
// progress. The raw line is kept alongside, because a modem listing a call
// that is not there is exactly the problem someone needs to see.
func parseCLCC(response modem.Response) []map[string]any {
	result := make([]map[string]any, 0)
	for _, line := range response.Lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToUpper(line), "+CLCC:") {
			continue
		}
		fields := strings.Split(strings.TrimSpace(strings.TrimPrefix(line, "+CLCC:")), ",")
		if len(fields) < 5 {
			continue
		}
		integer := func(index int) int {
			value, _ := strconv.Atoi(strings.TrimSpace(fields[index]))
			return value
		}
		index, direction, state := integer(0), integer(1), integer(2)
		if mode := integer(3); nonVoiceCLCCModes[mode] {
			continue
		}
		name, known := clccStates[state]
		if !known {
			// Rather than an empty state, which would read as "no call".
			name = "unknown"
		}
		call := map[string]any{
			// The call index is the only identifier a modem gives, and it is
			// what AT+CHLD would act on.
			"id":        strconv.Itoa(index),
			"index":     index,
			"direction": "outgoing",
			"state":     name,
			// The raw 27.007 codes, kept under their own names: the web UI
			// reads the words, and anything that has to act on the modem --
			// or explain what it said -- needs the number.
			"direction_code": direction,
			"state_code":     state,
			"mode":           integer(3),
			"multiparty":     integer(4),
			"raw":            line,
		}
		if direction == 1 {
			call["direction"] = "incoming"
		}
		if len(fields) > 5 {
			call["number"] = strings.Trim(strings.TrimSpace(fields[5]), `"`)
		}
		result = append(result, call)
	}
	return result
}
