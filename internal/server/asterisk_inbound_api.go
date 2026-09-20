package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"vocat/internal/store"
)

// handlePutAsteriskInbound saves where a call arriving on a SIM rings.
//
// Validated against the configured extensions before it is stored: a ring
// group naming an account that does not exist rings nothing, and the only way
// to discover that is a caller who never reaches anyone.
func (s *Server) handlePutAsteriskInbound(w http.ResponseWriter, r *http.Request) {
	var request asteriskInbound
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	extensions := s.storedAsteriskExtensions(r.Context())
	configured := toConfigExtensions(extensions)
	plan := request.toConfig()
	if err := plan.Validate(configured); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_inbound", err.Error())
		return
	}
	// Refused at save rather than at call time: forwarding to a trunk that
	// does not exist renders a dial to an endpoint Asterisk has never heard
	// of, and the only other signal is a caller hearing nothing.
	if err := s.inboundTrunkExists(r.Context(), plan); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_inbound", err.Error())
		return
	}
	files, err := renderAsteriskExtensions(extensions, plan)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_inbound", err.Error())
		return
	}
	body, err := json.Marshal(inboundToWire(plan))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not encode the inbound plan")
		return
	}
	if err := s.store.UpsertAppSetting(r.Context(), store.AppSetting{
		Key: asteriskInboundKey, Value: body,
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
	s.recordAudit(r.Context(), "admin", "asterisk.inbound.save", "asterisk", "inbound", "success", "")
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"saved": true, "written": written,
		"inbound":         inboundToWire(plan),
		"inbound_preview": files.inbound,
	}})
}

// asteriskExtensionCandidate is one SIM number VoCat has learned, and whether
// an extension already exists for it.
type asteriskExtensionCandidate struct {
	Number     string `json:"number"`
	DeviceID   string `json:"device_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
	ICCID      string `json:"iccid,omitempty"`
	// Exists is true when an extension of this name is already configured, so
	// the page can offer only what is missing without hiding the rest.
	Exists bool `json:"exists"`
}

// handleAsteriskExtensionCandidates lists the numbers an extension could be
// created for.
//
// VoCat already learns each SIM's own number from IMS registration. Making
// someone copy those between two screens is how a digit gets transposed and a
// DID silently rings nothing, so the page offers them directly.
func (s *Server) handleAsteriskExtensionCandidates(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	associations, err := s.store.ListPhoneAssociations(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	existing := map[string]bool{}
	for _, extension := range s.storedAsteriskExtensions(r.Context()) {
		existing[strings.ToLower(strings.TrimSpace(extension.Name))] = true
	}
	names := map[string]store.Device{}
	if devices, err := s.store.ListDevices(r.Context()); err == nil {
		for _, device := range devices {
			names[device.ID] = device
		}
	}

	seen := map[string]bool{}
	candidates := make([]asteriskExtensionCandidate, 0, len(associations))
	for _, association := range associations {
		// An extension name is the number with nothing else in it: a leading
		// "+" cannot be a PJSIP section name here, and the inbound dialplan
		// matches both forms anyway.
		number := normalizeExtensionNumber(association.Number)
		if number == "" || seen[number] {
			continue
		}
		seen[number] = true
		candidate := asteriskExtensionCandidate{
			Number: number, DeviceID: association.DeviceID,
			ICCID: association.ICCID, Exists: existing[strings.ToLower(number)],
		}
		if device, ok := names[association.DeviceID]; ok {
			candidate.DeviceName = strings.TrimSpace(device.Name)
		}
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(first, second int) bool {
		return candidates[first].Number < candidates[second].Number
	})
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"candidates": candidates,
	}})
}

// normalizeExtensionNumber turns a learned number into something that can be
// a PJSIP section name: digits only. A number VoCat cannot reduce to that is
// skipped rather than offered as an extension that would fail to save.
func normalizeExtensionNumber(number string) string {
	var builder strings.Builder
	for _, value := range strings.TrimSpace(number) {
		if value >= '0' && value <= '9' {
			builder.WriteRune(value)
		}
	}
	digits := builder.String()
	if len(digits) < 3 || len(digits) > 20 {
		return ""
	}
	return digits
}
