package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// RegistrationEvent is one change in an endpoint contact's state: a handset
// registering, going unreachable, or disappearing entirely.
//
// One row per change rather than per poll. A table with a row every thirty
// seconds answers "was it up at 03:14" but buries the thing anyone actually
// looks for, which is when it stopped being up.
type RegistrationEvent struct {
	ID       int64
	Endpoint string
	// ContactURI identifies which of an endpoint's contacts changed: an
	// account registered from a phone and a desk handset has two.
	ContactURI string
	Status     string
	// PreviousStatus is what it was before, so a row reads as a transition
	// rather than a state that needs the row above it to interpret.
	PreviousStatus string
	UserAgent      string
	ViaAddress     string
	RoundTripMS    float64
	ChangedAt      time.Time
}

const registrationColumns = `id, endpoint, contact_uri, status, previous_status,
	user_agent, via_address, roundtrip_ms, changed_at`

// AppendRegistrationEvent records one transition.
func (s *Store) AppendRegistrationEvent(ctx context.Context, value RegistrationEvent) error {
	value.Endpoint = strings.TrimSpace(value.Endpoint)
	if value.Endpoint == "" {
		return fmt.Errorf("store: a registration event needs an endpoint")
	}
	if value.ChangedAt.IsZero() {
		value.ChangedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO asterisk_registrations (
			endpoint, contact_uri, status, previous_status,
			user_agent, via_address, roundtrip_ms, changed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, value.Endpoint, value.ContactURI, value.Status, value.PreviousStatus,
		value.UserAgent, value.ViaAddress, value.RoundTripMS, value.ChangedAt.UTC().Unix())
	if err != nil {
		return fmt.Errorf("store: append registration event: %w", err)
	}
	return nil
}

// LatestRegistrationStates returns the most recent status for every contact
// seen, which is what a restarted recorder seeds itself from: without it the
// first poll after a restart would record a change for everything that had
// not changed at all.
func (s *Store) LatestRegistrationStates(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT endpoint, contact_uri, status
		FROM asterisk_registrations
		WHERE id IN (
			SELECT MAX(id) FROM asterisk_registrations GROUP BY endpoint, contact_uri
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("store: read registration states: %w", err)
	}
	defer rows.Close()
	states := map[string]string{}
	for rows.Next() {
		var endpoint, contact, status string
		if err := rows.Scan(&endpoint, &contact, &status); err != nil {
			return nil, fmt.Errorf("store: scan registration state: %w", err)
		}
		states[endpoint+"\x00"+contact] = status
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read registration states: %w", err)
	}
	return states, nil
}

// ListRegistrationEvents returns transitions newest first.
func (s *Store) ListRegistrationEvents(ctx context.Context, endpoint string, limit int) ([]RegistrationEvent, error) {
	where := ""
	var args []any
	if endpoint = strings.TrimSpace(endpoint); endpoint != "" {
		where = " WHERE endpoint = ?"
		args = append(args, endpoint)
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+registrationColumns+` FROM asterisk_registrations`+where+
			` ORDER BY changed_at DESC, id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("store: list registration events: %w", err)
	}
	defer rows.Close()
	events := make([]RegistrationEvent, 0, limit)
	for rows.Next() {
		var event RegistrationEvent
		var changedAt int64
		if err := rows.Scan(&event.ID, &event.Endpoint, &event.ContactURI, &event.Status,
			&event.PreviousStatus, &event.UserAgent, &event.ViaAddress,
			&event.RoundTripMS, &changedAt); err != nil {
			return nil, fmt.Errorf("store: scan registration event: %w", err)
		}
		event.ChangedAt = time.Unix(changedAt, 0).UTC()
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list registration events: %w", err)
	}
	return events, nil
}

// DeleteRegistrationEventsBefore prunes history.
func (s *Store) DeleteRegistrationEventsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM asterisk_registrations WHERE changed_at < ?`, cutoff.UTC().Unix())
	if err != nil {
		return 0, fmt.Errorf("store: prune registration events: %w", err)
	}
	removed, _ := result.RowsAffected()
	return removed, nil
}
