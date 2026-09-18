package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// CallRecord is one call, from the first signalling to the last. It is written
// by observing live call state rather than by whoever started the call, so a
// call placed from the browser, through the SIP trunk, or by an automatic task
// all land here the same way.
type CallRecord struct {
	ID int64
	// CallID is the IMS session's identifier. Unique per device, which is what
	// lets the recorder update a row it already wrote rather than appending a
	// new one on every poll.
	CallID     string
	DeviceID   string
	DeviceName string
	// Direction is "outgoing" or "incoming", as the IMS session reports it.
	Direction string
	// Source says who drove the call: "browser" for VoCat's own Calls page,
	// "trunk" for one the PBX placed or answered. Blank until known.
	Source string
	// PeerNumber is the other party: dialled for an outgoing call, calling
	// for an incoming one.
	PeerNumber string
	StartedAt  time.Time
	AnsweredAt *time.Time
	EndedAt    *time.Time
	// DurationSeconds is answer to hang-up, which is what a carrier bills.
	// Zero for a call that was never answered -- ringing is not duration.
	DurationSeconds int
	// Disposition is the outcome: answered, no_answer, busy, failed or
	// cancelled. Derived from the last state and SIP code seen.
	Disposition string
	SIPCode     int
	Reason      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const callRecordColumns = `id, call_id, device_id, device_name, direction, source,
	peer_number, started_at, answered_at, ended_at, duration_seconds,
	disposition, sip_code, reason, created_at, updated_at`

// UpsertCallRecord writes a call, replacing the row for the same device and
// call ID.
//
// Fields that are already set are not cleared by a later write: the recorder
// sees a call several times as it progresses, and a poll that catches it
// mid-teardown may report less than the one before. Keeping the answered time
// and the number once seen is what stops a completed call turning into a
// blank row at the end.
func (s *Store) UpsertCallRecord(ctx context.Context, value CallRecord) error {
	value.CallID = strings.TrimSpace(value.CallID)
	value.DeviceID = strings.TrimSpace(value.DeviceID)
	if value.CallID == "" || value.DeviceID == "" {
		return fmt.Errorf("store: a call record needs a device and a call ID")
	}
	now := time.Now().UTC()
	if value.CreatedAt.IsZero() {
		value.CreatedAt = now
	}
	value.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO call_records (
			call_id, device_id, device_name, direction, source, peer_number,
			started_at, answered_at, ended_at, duration_seconds,
			disposition, sip_code, reason, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(device_id, call_id) DO UPDATE SET
			device_name = CASE WHEN excluded.device_name != '' THEN excluded.device_name ELSE call_records.device_name END,
			direction = CASE WHEN excluded.direction != '' THEN excluded.direction ELSE call_records.direction END,
			source = CASE WHEN excluded.source != '' THEN excluded.source ELSE call_records.source END,
			peer_number = CASE WHEN excluded.peer_number != '' THEN excluded.peer_number ELSE call_records.peer_number END,
			answered_at = COALESCE(call_records.answered_at, excluded.answered_at),
			ended_at = COALESCE(excluded.ended_at, call_records.ended_at),
			duration_seconds = MAX(excluded.duration_seconds, call_records.duration_seconds),
			disposition = CASE WHEN excluded.disposition != '' THEN excluded.disposition ELSE call_records.disposition END,
			sip_code = CASE WHEN excluded.sip_code != 0 THEN excluded.sip_code ELSE call_records.sip_code END,
			reason = CASE WHEN excluded.reason != '' THEN excluded.reason ELSE call_records.reason END,
			updated_at = excluded.updated_at
	`,
		value.CallID, value.DeviceID, value.DeviceName, value.Direction, value.Source,
		value.PeerNumber, value.StartedAt.UTC().Unix(), unixOrNil(value.AnsweredAt),
		unixOrNil(value.EndedAt), value.DurationSeconds, value.Disposition,
		value.SIPCode, value.Reason, value.CreatedAt.UTC().Unix(), value.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("store: upsert call record: %w", err)
	}
	return nil
}

// CallRecordFilter narrows a listing. Limit is capped by the caller.
type CallRecordFilter struct {
	DeviceID string
	// Direction and Disposition are exact matches when set.
	Direction   string
	Disposition string
	// Search matches the peer number as a substring, which is how someone
	// looks for "that call to 555".
	Search string
	Limit  int
	Offset int
}

// ListCallRecords returns calls newest first, with the total before paging so
// a UI can say how many there are rather than only how many it showed.
func (s *Store) ListCallRecords(ctx context.Context, filter CallRecordFilter) ([]CallRecord, int, error) {
	where := []string{"1 = 1"}
	var args []any
	if device := strings.TrimSpace(filter.DeviceID); device != "" {
		where = append(where, "device_id = ?")
		args = append(args, device)
	}
	if direction := strings.TrimSpace(filter.Direction); direction != "" {
		where = append(where, "direction = ?")
		args = append(args, direction)
	}
	if disposition := strings.TrimSpace(filter.Disposition); disposition != "" {
		where = append(where, "disposition = ?")
		args = append(args, disposition)
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		where = append(where, "peer_number LIKE ?")
		args = append(args, "%"+search+"%")
	}
	condition := strings.Join(where, " AND ")

	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM call_records WHERE `+condition, args...,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: count call records: %w", err)
	}
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+callRecordColumns+` FROM call_records WHERE `+condition+
			` ORDER BY started_at DESC, id DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: list call records: %w", err)
	}
	defer rows.Close()
	records := make([]CallRecord, 0, limit)
	for rows.Next() {
		record, err := scanCallRecord(rows)
		if err != nil {
			return nil, 0, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: list call records: %w", err)
	}
	return records, total, nil
}

// DeleteCallRecordsBefore prunes history. Call records accumulate for ever
// otherwise, and a year of them on a device with several SIMs is a database
// nobody asked for.
func (s *Store) DeleteCallRecordsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM call_records WHERE started_at < ?`, cutoff.UTC().Unix())
	if err != nil {
		return 0, fmt.Errorf("store: prune call records: %w", err)
	}
	removed, _ := result.RowsAffected()
	return removed, nil
}

func scanCallRecord(row rowScanner) (CallRecord, error) {
	var record CallRecord
	var started, created, updated int64
	var answered, ended sql.NullInt64
	if err := row.Scan(
		&record.ID, &record.CallID, &record.DeviceID, &record.DeviceName,
		&record.Direction, &record.Source, &record.PeerNumber, &started,
		&answered, &ended, &record.DurationSeconds, &record.Disposition,
		&record.SIPCode, &record.Reason, &created, &updated,
	); err != nil {
		return CallRecord{}, fmt.Errorf("store: scan call record: %w", err)
	}
	record.StartedAt = time.Unix(started, 0).UTC()
	record.CreatedAt = time.Unix(created, 0).UTC()
	record.UpdatedAt = time.Unix(updated, 0).UTC()
	if answered.Valid {
		value := time.Unix(answered.Int64, 0).UTC()
		record.AnsweredAt = &value
	}
	if ended.Valid {
		value := time.Unix(ended.Int64, 0).UTC()
		record.EndedAt = &value
	}
	return record, nil
}

func unixOrNil(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC().Unix()
}
