package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"vocat/internal/store"
)

// maxCallRecordPage bounds one listing. A history view shows a page at a
// time, and an unbounded limit turns a long-lived deployment's whole call
// history into one response.
const maxCallRecordPage = 200

type callRecordPayload struct {
	ID              int64  `json:"id"`
	CallID          string `json:"call_id"`
	DeviceID        string `json:"device_id"`
	DeviceName      string `json:"device_name,omitempty"`
	Direction       string `json:"direction,omitempty"`
	Source          string `json:"source,omitempty"`
	PeerNumber      string `json:"peer_number,omitempty"`
	StartedAt       string `json:"started_at"`
	AnsweredAt      string `json:"answered_at,omitempty"`
	EndedAt         string `json:"ended_at,omitempty"`
	DurationSeconds int    `json:"duration_seconds"`
	Disposition     string `json:"disposition,omitempty"`
	SIPCode         int    `json:"sip_code,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

func callRecordToPayload(record store.CallRecord) callRecordPayload {
	payload := callRecordPayload{
		ID: record.ID, CallID: record.CallID, DeviceID: record.DeviceID,
		DeviceName: record.DeviceName, Direction: record.Direction,
		Source: record.Source, PeerNumber: record.PeerNumber,
		StartedAt: record.StartedAt.Format(time.RFC3339),
		// Duration is answer to hang-up. A call that never connected reports
		// zero rather than how long it rang, which is what a bill counts.
		DurationSeconds: record.DurationSeconds,
		Disposition:     record.Disposition, SIPCode: record.SIPCode, Reason: record.Reason,
	}
	if record.AnsweredAt != nil {
		payload.AnsweredAt = record.AnsweredAt.Format(time.RFC3339)
	}
	if record.EndedAt != nil {
		payload.EndedAt = record.EndedAt.Format(time.RFC3339)
	}
	return payload
}

// handleCallRecords lists call history. Read-only: records are written by
// observing calls, and an API that could edit them would make the history a
// claim rather than a record.
func (s *Server) handleCallRecords(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	query := r.URL.Query()
	filter := store.CallRecordFilter{
		DeviceID:    strings.TrimSpace(query.Get("device_id")),
		Direction:   strings.TrimSpace(query.Get("direction")),
		Disposition: strings.TrimSpace(query.Get("disposition")),
		Search:      strings.TrimSpace(query.Get("search")),
		Limit:       50,
	}
	if value := strings.TrimSpace(query.Get("limit")); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 || limit > maxCallRecordPage {
			writeError(w, http.StatusBadRequest, "invalid_limit",
				"limit must be between 1 and "+strconv.Itoa(maxCallRecordPage))
			return
		}
		filter.Limit = limit
	}
	if value := strings.TrimSpace(query.Get("offset")); value != "" {
		offset, err := strconv.Atoi(value)
		if err != nil || offset < 0 {
			writeError(w, http.StatusBadRequest, "invalid_offset", "offset must be zero or more")
			return
		}
		filter.Offset = offset
	}
	records, total, err := s.store.ListCallRecords(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	payload := make([]callRecordPayload, 0, len(records))
	for _, record := range records {
		payload = append(payload, callRecordToPayload(record))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"records": payload,
		"total":   total,
		"limit":   filter.Limit,
		"offset":  filter.Offset,
	}})
}
