package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"vocat/internal/ami"
	"vocat/internal/store"
)

const (
	// registrationPollInterval is how often contact state is sampled. Slower
	// than the page's own poll on purpose: this runs whether or not anyone is
	// looking, and a flap lasting less than this is one the qualify timer
	// (60s) would not have caught either.
	registrationPollInterval = 30 * time.Second
	// registrationRetention is how long transitions are kept.
	registrationRetention   = 30 * 24 * time.Hour
	registrationPrunePeriod = 6 * time.Hour
	// goneStatus is what a contact that vanished from the listing is recorded
	// as. Asterisk simply stops mentioning an unregistered contact, so
	// absence is the only signal, and "no status" would read as unknown
	// rather than as the thing that happened.
	goneStatus = "Unregistered"
)

// StartRegistrationRecorder records contact state changes over time.
//
// The Asterisk page shows what is true right now, which answers "is it
// registered" and not "when did it stop being registered" -- and a handset
// that drops for ninety seconds every hour is invisible unless someone
// happens to be watching at the moment it happens. This runs whether anyone
// is looking or not.
func (s *Server) StartRegistrationRecorder(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(s.asteriskAMI.Address) == "" {
		return
	}
	// Seeded from what was last recorded, so the first poll after a restart
	// does not record a change for everything that did not change.
	known, err := s.store.LatestRegistrationStates(ctx)
	if err != nil {
		s.logger.Warn("could not read registration history",
			"category", "siptrunk", "error", err)
		known = map[string]string{}
	}
	ticker := time.NewTicker(registrationPollInterval)
	defer ticker.Stop()
	prune := time.NewTicker(registrationPrunePeriod)
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.recordRegistrations(ctx, known)
		case <-prune.C:
			if _, err := s.store.DeleteRegistrationEventsBefore(ctx, time.Now().Add(-registrationRetention)); err != nil {
				s.logger.Warn("could not prune registration history",
					"category", "siptrunk", "error", err)
			}
		}
	}
}

// recordRegistrations samples contact state and writes what changed. known is
// mutated in place: it is this loop's memory of the last state per contact.
func (s *Server) recordRegistrations(ctx context.Context, known map[string]string) {
	pollCtx, cancel := context.WithTimeout(ctx, asteriskStatusTimeout)
	defer cancel()
	conn, err := ami.Dial(pollCtx, s.asteriskAMI)
	if err != nil {
		// A PBX that is down is not a registration change: recording one
		// would fill the history with the recorder's own view of the world
		// rather than the handsets'.
		return
	}
	defer conn.Close()

	endpoints, err := conn.List(pollCtx, "PJSIPShowEndpoints", nil)
	if err != nil {
		return
	}
	contacts, contactsErr := conn.List(pollCtx, "PJSIPShowContacts", nil)
	if contactsErr != nil && !ami.IsEmptyList(contactsErr) {
		return
	}
	built, _ := buildAsteriskEndpoints(endpoints, contacts)
	s.fillAsteriskContactStatus(pollCtx, conn, built)

	seen := map[string]bool{}
	for _, endpoint := range built {
		for _, contact := range endpoint.Contacts {
			key := endpoint.Name + "\x00" + contact.URI
			seen[key] = true
			status := strings.TrimSpace(contact.Status)
			if status == "" {
				// Not yet qualified. Recording it as a change would produce a
				// transition every time Asterisk restarts, which says nothing
				// about the handset.
				continue
			}
			previous, had := known[key]
			if had && previous == status {
				continue
			}
			s.appendRegistration(ctx, store.RegistrationEvent{
				Endpoint: endpoint.Name, ContactURI: contact.URI,
				Status: status, PreviousStatus: previous,
				UserAgent: contact.UserAgent, ViaAddress: contact.ViaAddress,
				RoundTripMS: contact.RoundTripMS,
			})
			known[key] = status
		}
	}
	// A contact that stopped being listed has unregistered. Asterisk says
	// nothing at all about it, so absence is the only signal there is.
	for key, previous := range known {
		if seen[key] || previous == goneStatus {
			continue
		}
		endpoint, contact, _ := strings.Cut(key, "\x00")
		s.appendRegistration(ctx, store.RegistrationEvent{
			Endpoint: endpoint, ContactURI: contact,
			Status: goneStatus, PreviousStatus: previous,
		})
		known[key] = goneStatus
	}
}

func (s *Server) appendRegistration(ctx context.Context, event store.RegistrationEvent) {
	if err := s.store.AppendRegistrationEvent(ctx, event); err != nil {
		s.logger.Warn("could not record a registration change",
			"category", "siptrunk", "endpoint", event.Endpoint, "error", err)
		return
	}
	s.logger.Info("Asterisk registration changed",
		"category", "siptrunk", "endpoint", event.Endpoint,
		"contact", event.ContactURI, "status", event.Status, "was", event.PreviousStatus)
}

type registrationPayload struct {
	Endpoint       string  `json:"endpoint"`
	ContactURI     string  `json:"contact_uri,omitempty"`
	Status         string  `json:"status,omitempty"`
	PreviousStatus string  `json:"previous_status,omitempty"`
	UserAgent      string  `json:"user_agent,omitempty"`
	ViaAddress     string  `json:"via_address,omitempty"`
	RoundTripMS    float64 `json:"roundtrip_ms,omitempty"`
	ChangedAt      string  `json:"changed_at"`
}

// handleAsteriskRegistrations lists registration changes, newest first.
func (s *Server) handleAsteriskRegistrations(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	limit := 50
	if value := strings.TrimSpace(r.URL.Query().Get("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 500 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 500")
			return
		}
		limit = parsed
	}
	events, err := s.store.ListRegistrationEvents(r.Context(),
		strings.TrimSpace(r.URL.Query().Get("endpoint")), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	payload := make([]registrationPayload, 0, len(events))
	for _, event := range events {
		payload = append(payload, registrationPayload{
			Endpoint: event.Endpoint, ContactURI: event.ContactURI,
			Status: event.Status, PreviousStatus: event.PreviousStatus,
			UserAgent: event.UserAgent, ViaAddress: event.ViaAddress,
			RoundTripMS: event.RoundTripMS,
			ChangedAt:   event.ChangedAt.Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"registrations": payload,
		// Says whether history is being collected at all, so an empty list on
		// a PBX with no manager interface reads as "off" rather than "quiet".
		"recording": strings.TrimSpace(s.asteriskAMI.Address) != "",
	}})
}
