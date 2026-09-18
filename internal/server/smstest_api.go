package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"vocat/internal/smstest"
	"vocat/internal/store"
)

const (
	smsTestDefaultResultWindowHours = 24
	smsTestMaxResultWindowHours     = 24 * 90
	smsTestDefaultResultLimit       = 500
)

// routeSMSTestAPI serves the SMS delivery testing surface: gateway endpoint
// definitions, recurring test schedules, and the results those tests produce.
func (s *Server) routeSMSTestAPI(w http.ResponseWriter, r *http.Request, cleanPath string) bool {
	if cleanPath != "smstest" && !strings.HasPrefix(cleanPath, "smstest/") {
		return false
	}
	segments := splitAPIPath(cleanPath)
	if len(segments) < 2 {
		s.handleAPINotFound(w, r)
		return true
	}

	switch segments[1] {
	case "endpoints":
		s.routeSMSTestEndpoints(w, r, segments)
	case "schedules":
		s.routeSMSTestSchedules(w, r, segments)
	case "results":
		s.handleSMSTestResults(w, r)
	default:
		s.handleAPINotFound(w, r)
	}
	return true
}

type smsTestEndpointPayload struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Method     string `json:"method"`
	URL        string `json:"url"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	Headers    string `json:"headers"`
	BodyParams string `json:"body_params"`
}

func (s *Server) routeSMSTestEndpoints(w http.ResponseWriter, r *http.Request, segments []string) {
	if len(segments) == 2 {
		switch r.Method {
		case http.MethodGet:
			endpoints, err := s.store.ListSMSTestEndpoints(r.Context())
			if err != nil {
				s.writeStoreError(w, err)
				return
			}
			payloads := make([]map[string]any, 0, len(endpoints))
			for _, endpoint := range endpoints {
				payloads = append(payloads, smsTestEndpointResponse(endpoint))
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"endpoints": payloads}})
		case http.MethodPost:
			s.saveSMSTestEndpoint(w, r, "")
		default:
			w.Header().Set("Allow", "GET, POST")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
		return
	}
	if len(segments) != 3 {
		s.handleAPINotFound(w, r)
		return
	}

	id := segments[2]
	switch r.Method {
	case http.MethodGet:
		endpoint, err := s.store.SMSTestEndpoint(r.Context(), id)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": smsTestEndpointResponse(endpoint)})
	case http.MethodPut, http.MethodPatch:
		s.saveSMSTestEndpoint(w, r, id)
	case http.MethodDelete:
		if err := s.store.DeleteSMSTestEndpoint(r.Context(), id); err != nil {
			s.writeStoreError(w, err)
			return
		}
		s.audit(r, "smstest.endpoint.delete", "smstest_endpoint", id, "success")
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]bool{"deleted": true}})
	default:
		w.Header().Set("Allow", "GET, PUT, PATCH, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) saveSMSTestEndpoint(w http.ResponseWriter, r *http.Request, id string) {
	var request smsTestEndpointPayload
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if id == "" {
		id = strings.TrimSpace(request.ID)
	}
	if id == "" {
		id = smstest.NewID()
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid_endpoint", "a name is required")
		return
	}
	url := strings.TrimSpace(request.URL)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		writeError(w, http.StatusBadRequest, "invalid_endpoint", "url must start with http:// or https://")
		return
	}
	method := strings.ToUpper(strings.TrimSpace(request.Method))
	if method == "" {
		method = http.MethodPost
	}
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		writeError(w, http.StatusBadRequest, "invalid_endpoint", "unsupported HTTP method")
		return
	}

	existing, err := s.store.SMSTestEndpoint(r.Context(), id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.writeStoreError(w, err)
		return
	}
	password := request.Password
	if password == "" {
		// A blank password on update keeps the stored secret, so the editor
		// can save without re-entering it.
		password = existing.Password
	}

	saved, err := s.store.UpsertSMSTestEndpoint(r.Context(), store.SMSTestEndpoint{
		ID:       id,
		Name:     name,
		Method:   method,
		URL:      url,
		Username: request.Username,
		Password: password,
		// A header or body parameter named like a credential is write-only
		// too: blank keeps what is stored, exactly as the endpoint password
		// does.
		Headers:    mergeSMSTestPairSecrets(request.Headers, existing.Headers),
		BodyParams: mergeSMSTestPairSecrets(request.BodyParams, existing.BodyParams),
		CreatedAt:  existing.CreatedAt,
	})
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	s.audit(r, "smstest.endpoint.save", "smstest_endpoint", id, "success")
	writeJSON(w, http.StatusOK, map[string]any{"data": smsTestEndpointResponse(saved)})
}

type smsTestSchedulePayload struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	EndpointID       string `json:"endpoint_id"`
	Recipient        string `json:"recipient"`
	Sender           string `json:"sender"`
	ContentTemplate  string `json:"content_template"`
	CodeType         string `json:"code_type"`
	CodeLength       int    `json:"code_length"`
	FrequencyMinutes int    `json:"frequency_minutes"`
	StartTime        string `json:"start_time"`
	Enabled          bool   `json:"enabled"`
	IsExternal       bool   `json:"is_external"`
}

func (s *Server) routeSMSTestSchedules(w http.ResponseWriter, r *http.Request, segments []string) {
	if len(segments) == 2 {
		switch r.Method {
		case http.MethodGet:
			schedules, err := s.store.ListSMSTestSchedules(r.Context())
			if err != nil {
				s.writeStoreError(w, err)
				return
			}
			payloads := make([]map[string]any, 0, len(schedules))
			for _, schedule := range schedules {
				payloads = append(payloads, smsTestScheduleResponse(schedule))
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"schedules": payloads}})
		case http.MethodPost:
			s.saveSMSTestSchedule(w, r, "")
		default:
			w.Header().Set("Allow", "GET, POST")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
		return
	}

	if len(segments) == 4 && segments[3] == "run" {
		s.handleSMSTestRun(w, r, segments[2])
		return
	}
	if len(segments) != 3 {
		s.handleAPINotFound(w, r)
		return
	}

	id := segments[2]
	switch r.Method {
	case http.MethodGet:
		schedule, err := s.store.SMSTestSchedule(r.Context(), id)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": smsTestScheduleResponse(schedule)})
	case http.MethodPut, http.MethodPatch:
		s.saveSMSTestSchedule(w, r, id)
	case http.MethodDelete:
		if err := s.store.DeleteSMSTestSchedule(r.Context(), id); err != nil {
			s.writeStoreError(w, err)
			return
		}
		s.audit(r, "smstest.schedule.delete", "smstest_schedule", id, "success")
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]bool{"deleted": true}})
	default:
		w.Header().Set("Allow", "GET, PUT, PATCH, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) saveSMSTestSchedule(w http.ResponseWriter, r *http.Request, id string) {
	var request smsTestSchedulePayload
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if id == "" {
		id = strings.TrimSpace(request.ID)
	}
	if id == "" {
		id = smstest.NewID()
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid_schedule", "a name is required")
		return
	}
	endpointID := strings.TrimSpace(request.EndpointID)
	if endpointID == "" {
		writeError(w, http.StatusBadRequest, "invalid_schedule", "an endpoint is required")
		return
	}
	if _, err := s.store.SMSTestEndpoint(r.Context(), endpointID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "invalid_schedule", "the selected endpoint does not exist")
			return
		}
		s.writeStoreError(w, err)
		return
	}
	if strings.TrimSpace(request.Recipient) == "" {
		writeError(w, http.StatusBadRequest, "invalid_schedule", "a recipient is required")
		return
	}
	if request.FrequencyMinutes < 0 {
		writeError(w, http.StatusBadRequest, "invalid_schedule", "frequency must not be negative")
		return
	}
	if request.CodeLength < 0 || request.CodeLength > 32 {
		writeError(w, http.StatusBadRequest, "invalid_schedule", "code length must be between 1 and 32")
		return
	}
	if request.StartTime != "" && !validSMSTestStartTime(request.StartTime) {
		writeError(w, http.StatusBadRequest, "invalid_schedule", "start time must look like HH:MM")
		return
	}

	existing, err := s.store.SMSTestSchedule(r.Context(), id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.writeStoreError(w, err)
		return
	}

	saved, err := s.store.UpsertSMSTestSchedule(r.Context(), store.SMSTestSchedule{
		ID:               id,
		Name:             name,
		EndpointID:       endpointID,
		Recipient:        strings.TrimSpace(request.Recipient),
		Sender:           strings.TrimSpace(request.Sender),
		ContentTemplate:  request.ContentTemplate,
		CodeType:         strings.TrimSpace(request.CodeType),
		CodeLength:       request.CodeLength,
		FrequencyMinutes: request.FrequencyMinutes,
		StartTime:        strings.TrimSpace(request.StartTime),
		Enabled:          request.Enabled,
		IsExternal:       request.IsExternal,
		LastRunAt:        existing.LastRunAt,
		CreatedAt:        existing.CreatedAt,
	})
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	s.audit(r, "smstest.schedule.save", "smstest_schedule", id, "success")
	writeJSON(w, http.StatusOK, map[string]any{"data": smsTestScheduleResponse(saved)})
}

// handleSMSTestRun starts one test immediately, outside its schedule.
func (s *Server) handleSMSTestRun(w http.ResponseWriter, r *http.Request, id string) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if s.smsTest == nil {
		writeError(w, http.StatusServiceUnavailable, "smstest_unavailable", "the SMS test scheduler is not running")
		return
	}
	result, err := s.smsTest.RunScheduleByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.writeStoreError(w, err)
			return
		}
		s.logger.Warn("smstest: manual run failed", "schedule", id, "error", err)
		writeError(w, http.StatusBadGateway, "smstest_run_failed", err.Error())
		return
	}
	s.audit(r, "smstest.schedule.run", "smstest_schedule", id, "success")
	writeJSON(w, http.StatusAccepted, map[string]any{"data": smsTestResultResponse(result)})
}

// handleSMSTestResults returns test results for the statistics view, newest
// first, optionally narrowed to one schedule.
func (s *Server) handleSMSTestResults(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	query := r.URL.Query()
	hours := smsTestDefaultResultWindowHours
	if raw := strings.TrimSpace(query.Get("hours")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "hours must be a positive integer")
			return
		}
		hours = min(parsed, smsTestMaxResultWindowHours)
	}
	limit := smsTestDefaultResultLimit
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be a positive integer")
			return
		}
		limit = parsed
	}

	since := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)
	results, err := s.store.ListSMSTestResults(
		r.Context(), strings.TrimSpace(query.Get("schedule_id")), since, limit)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	payloads := make([]map[string]any, 0, len(results))
	summary := map[string]int{"total": len(results)}
	for _, result := range results {
		payloads = append(payloads, smsTestResultResponse(result))
		summary[result.Status]++
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"results": payloads,
		"summary": summary,
		"hours":   hours,
	}})
}

func validSMSTestStartTime(value string) bool {
	_, err := time.Parse("15:04", strings.TrimSpace(value))
	return err == nil
}

func smsTestEndpointResponse(value store.SMSTestEndpoint) map[string]any {
	return map[string]any{
		"id":       value.ID,
		"name":     value.Name,
		"method":   value.Method,
		"url":      value.URL,
		"username": value.Username,
		// The stored gateway password is never returned; the editor sends a
		// blank value to keep it.
		"has_password": value.Password != "",
		// A header or body parameter named like a credential has its value
		// removed and is marked has_value instead, the same way the endpoint
		// password is. A gateway that wants a password in a body parameter
		// calls it one, and nothing else distinguishes it from the recipient
		// number beside it.
		"headers":     redactSMSTestPairs(value.Headers),
		"body_params": redactSMSTestPairs(value.BodyParams),
		"created_at":  value.CreatedAt.Format(time.RFC3339),
		"updated_at":  value.UpdatedAt.Format(time.RFC3339),
	}
}

func smsTestScheduleResponse(value store.SMSTestSchedule) map[string]any {
	payload := map[string]any{
		"id":                value.ID,
		"name":              value.Name,
		"endpoint_id":       value.EndpointID,
		"recipient":         value.Recipient,
		"sender":            value.Sender,
		"content_template":  value.ContentTemplate,
		"code_type":         value.CodeType,
		"code_length":       value.CodeLength,
		"frequency_minutes": value.FrequencyMinutes,
		"start_time":        value.StartTime,
		"enabled":           value.Enabled,
		"is_external":       value.IsExternal,
		"last_run_at":       nil,
		"created_at":        value.CreatedAt.Format(time.RFC3339),
		"updated_at":        value.UpdatedAt.Format(time.RFC3339),
	}
	if value.LastRunAt != nil {
		payload["last_run_at"] = value.LastRunAt.Format(time.RFC3339)
	}
	return payload
}

func smsTestResultResponse(value store.SMSTestResult) map[string]any {
	payload := map[string]any{
		"id":            value.ID,
		"schedule_id":   value.ScheduleID,
		"sent_at":       value.SentAt.Format(time.RFC3339),
		"received_at":   nil,
		"elapsed_ms":    nil,
		"code":          value.Code,
		"status":        value.Status,
		"send_response": value.SendResponse,
		"device_id":     value.DeviceID,
	}
	if value.ReceivedAt != nil {
		payload["received_at"] = value.ReceivedAt.Format(time.RFC3339)
		payload["elapsed_ms"] = value.ReceivedAt.Sub(value.SentAt).Milliseconds()
	}
	return payload
}
