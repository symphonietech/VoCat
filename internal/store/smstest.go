package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SMSTestEndpoint describes an external SMS gateway API that a test schedule
// submits through. URL, header values and body parameter values may contain
// {{username}}, {{password}}, {{to}}, {{from}} and {{content}} placeholders,
// substituted at send time.
type SMSTestEndpoint struct {
	ID         string
	Name       string
	Method     string
	URL        string
	Username   string
	Password   string
	Headers    string
	BodyParams string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// SMSTestSchedule drives recurring sends through an endpoint. ContentTemplate
// may contain a {{code}} placeholder, replaced with a freshly generated
// one-time code that the inbound matcher looks for.
type SMSTestSchedule struct {
	ID               string
	Name             string
	EndpointID       string
	Recipient        string
	Sender           string
	ContentTemplate  string
	CodeType         string
	CodeLength       int
	FrequencyMinutes int
	StartTime        string
	Enabled          bool
	// IsExternal marks a recipient that is not one of this server's own
	// modems, so no inbound message will ever arrive to match. Such results
	// settle at "sent" instead of waiting to become received or failed.
	IsExternal bool
	LastRunAt  *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// SMSTestResult records one send and, once matched, the inbound message that
// carried the same code. Status is one of pending, sent, received, delayed or
// failed.
type SMSTestResult struct {
	ID           int64
	ScheduleID   string
	SentAt       time.Time
	ReceivedAt   *time.Time
	Code         string
	Status       string
	SendResponse string
	DeviceID     string
	CreatedAt    time.Time
}

const smsTestEndpointColumns = `id, name, method, url, username, password,
	headers, body_params, created_at, updated_at`

func (s *Store) UpsertSMSTestEndpoint(ctx context.Context, value SMSTestEndpoint) (SMSTestEndpoint, error) {
	now := time.Now().UTC()
	if value.Method == "" {
		value.Method = "POST"
	}
	if value.Headers == "" {
		value.Headers = "[]"
	}
	if value.BodyParams == "" {
		value.BodyParams = "[]"
	}
	if value.CreatedAt.IsZero() {
		value.CreatedAt = now
	}
	value.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO smstest_endpoints (`+smsTestEndpointColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			method = excluded.method,
			url = excluded.url,
			username = excluded.username,
			password = excluded.password,
			headers = excluded.headers,
			body_params = excluded.body_params,
			updated_at = excluded.updated_at
	`, value.ID, value.Name, value.Method, value.URL, value.Username, value.Password,
		value.Headers, value.BodyParams, value.CreatedAt.Unix(), value.UpdatedAt.Unix())
	if err != nil {
		return SMSTestEndpoint{}, fmt.Errorf("upsert sms test endpoint: %w", err)
	}
	return value, nil
}

func (s *Store) SMSTestEndpoint(ctx context.Context, id string) (SMSTestEndpoint, error) {
	return scanSMSTestEndpoint(s.db.QueryRowContext(ctx, `
		SELECT `+smsTestEndpointColumns+`
		FROM smstest_endpoints WHERE id = ?`, id))
}

func (s *Store) ListSMSTestEndpoints(ctx context.Context) ([]SMSTestEndpoint, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+smsTestEndpointColumns+`
		FROM smstest_endpoints ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list sms test endpoints: %w", err)
	}
	defer rows.Close()
	values := make([]SMSTestEndpoint, 0)
	for rows.Next() {
		value, scanErr := scanSMSTestEndpoint(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sms test endpoints: %w", err)
	}
	return values, nil
}

func (s *Store) DeleteSMSTestEndpoint(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM smstest_endpoints WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete sms test endpoint: %w", err)
	}
	return requireAffected(result)
}

const smsTestScheduleColumns = `id, name, endpoint_id, recipient, sender,
	content_template, code_type, code_length, frequency_minutes, start_time,
	enabled, is_external, last_run_at, created_at, updated_at`

func (s *Store) UpsertSMSTestSchedule(ctx context.Context, value SMSTestSchedule) (SMSTestSchedule, error) {
	now := time.Now().UTC()
	if value.CodeType == "" {
		value.CodeType = "digits"
	}
	if value.CodeLength <= 0 {
		value.CodeLength = 6
	}
	if value.FrequencyMinutes <= 0 {
		value.FrequencyMinutes = 60
	}
	if value.CreatedAt.IsZero() {
		value.CreatedAt = now
	}
	value.UpdatedAt = now
	var lastRun any
	if value.LastRunAt != nil {
		lastRun = value.LastRunAt.UTC().Unix()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO smstest_schedules (`+smsTestScheduleColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			endpoint_id = excluded.endpoint_id,
			recipient = excluded.recipient,
			sender = excluded.sender,
			content_template = excluded.content_template,
			code_type = excluded.code_type,
			code_length = excluded.code_length,
			frequency_minutes = excluded.frequency_minutes,
			start_time = excluded.start_time,
			enabled = excluded.enabled,
			is_external = excluded.is_external,
			updated_at = excluded.updated_at
	`, value.ID, value.Name, value.EndpointID, value.Recipient, value.Sender,
		value.ContentTemplate, value.CodeType, value.CodeLength, value.FrequencyMinutes,
		value.StartTime, value.Enabled, value.IsExternal, lastRun,
		value.CreatedAt.Unix(), value.UpdatedAt.Unix())
	if err != nil {
		return SMSTestSchedule{}, fmt.Errorf("upsert sms test schedule: %w", err)
	}
	return value, nil
}

func (s *Store) SMSTestSchedule(ctx context.Context, id string) (SMSTestSchedule, error) {
	return scanSMSTestSchedule(s.db.QueryRowContext(ctx, `
		SELECT `+smsTestScheduleColumns+`
		FROM smstest_schedules WHERE id = ?`, id))
}

func (s *Store) ListSMSTestSchedules(ctx context.Context) ([]SMSTestSchedule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+smsTestScheduleColumns+`
		FROM smstest_schedules ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list sms test schedules: %w", err)
	}
	defer rows.Close()
	values := make([]SMSTestSchedule, 0)
	for rows.Next() {
		value, scanErr := scanSMSTestSchedule(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sms test schedules: %w", err)
	}
	return values, nil
}

func (s *Store) DeleteSMSTestSchedule(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM smstest_schedules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete sms test schedule: %w", err)
	}
	return requireAffected(result)
}

func (s *Store) TouchSMSTestScheduleRun(ctx context.Context, id string, runAt time.Time) error {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE smstest_schedules SET last_run_at = ? WHERE id = ?
	`, runAt.UTC().Unix(), id); err != nil {
		return fmt.Errorf("update sms test schedule run time: %w", err)
	}
	return nil
}

const smsTestResultColumns = `id, schedule_id, sent_at, received_at, code,
	status, send_response, device_id, created_at`

func (s *Store) CreateSMSTestResult(ctx context.Context, value SMSTestResult) (SMSTestResult, error) {
	now := time.Now().UTC()
	if value.CreatedAt.IsZero() {
		value.CreatedAt = now
	}
	if value.Status == "" {
		value.Status = "pending"
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO smstest_results
			(schedule_id, sent_at, code, status, send_response, device_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, value.ScheduleID, value.SentAt.UTC().Unix(), value.Code, value.Status,
		value.SendResponse, value.DeviceID, value.CreatedAt.Unix())
	if err != nil {
		return SMSTestResult{}, fmt.Errorf("create sms test result: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return SMSTestResult{}, fmt.Errorf("read created sms test result id: %w", err)
	}
	value.ID = id
	return value, nil
}

// SettleSMSTestResult records the inbound match (or the expiry) for one
// pending result.
func (s *Store) SettleSMSTestResult(
	ctx context.Context,
	id int64,
	receivedAt time.Time,
	status string,
	deviceID string,
) error {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE smstest_results
		SET received_at = ?, status = ?, device_id = ?
		WHERE id = ?
	`, receivedAt.UTC().Unix(), status, deviceID, id); err != nil {
		return fmt.Errorf("settle sms test result: %w", err)
	}
	return nil
}

func (s *Store) ListPendingSMSTestResults(ctx context.Context) ([]SMSTestResult, error) {
	return s.querySMSTestResults(ctx, `
		SELECT `+smsTestResultColumns+`
		FROM smstest_results WHERE status = 'pending' ORDER BY sent_at ASC`)
}

// ListSMSTestResults returns results for one schedule, or for every schedule
// when scheduleID is empty, newest first.
func (s *Store) ListSMSTestResults(
	ctx context.Context,
	scheduleID string,
	since time.Time,
	limit int,
) ([]SMSTestResult, error) {
	if scheduleID != "" {
		return s.querySMSTestResults(ctx, `
			SELECT `+smsTestResultColumns+`
			FROM smstest_results
			WHERE schedule_id = ? AND sent_at >= ?
			ORDER BY sent_at DESC LIMIT ?`, scheduleID, since.UTC().Unix(), normalizedLimit(limit))
	}
	return s.querySMSTestResults(ctx, `
		SELECT `+smsTestResultColumns+`
		FROM smstest_results
		WHERE sent_at >= ?
		ORDER BY sent_at DESC LIMIT ?`, since.UTC().Unix(), normalizedLimit(limit))
}

func (s *Store) querySMSTestResults(ctx context.Context, query string, args ...any) ([]SMSTestResult, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list sms test results: %w", err)
	}
	defer rows.Close()
	values := make([]SMSTestResult, 0)
	for rows.Next() {
		value, scanErr := scanSMSTestResult(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sms test results: %w", err)
	}
	return values, nil
}

func scanSMSTestEndpoint(row rowScanner) (SMSTestEndpoint, error) {
	var value SMSTestEndpoint
	var createdAt, updatedAt int64
	err := row.Scan(&value.ID, &value.Name, &value.Method, &value.URL,
		&value.Username, &value.Password, &value.Headers, &value.BodyParams,
		&createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SMSTestEndpoint{}, ErrNotFound
	}
	if err != nil {
		return SMSTestEndpoint{}, fmt.Errorf("scan sms test endpoint: %w", err)
	}
	value.CreatedAt = time.Unix(createdAt, 0).UTC()
	value.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return value, nil
}

func scanSMSTestSchedule(row rowScanner) (SMSTestSchedule, error) {
	var value SMSTestSchedule
	var createdAt, updatedAt int64
	var lastRunAt sql.NullInt64
	err := row.Scan(&value.ID, &value.Name, &value.EndpointID, &value.Recipient,
		&value.Sender, &value.ContentTemplate, &value.CodeType, &value.CodeLength,
		&value.FrequencyMinutes, &value.StartTime, &value.Enabled, &value.IsExternal,
		&lastRunAt, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SMSTestSchedule{}, ErrNotFound
	}
	if err != nil {
		return SMSTestSchedule{}, fmt.Errorf("scan sms test schedule: %w", err)
	}
	if lastRunAt.Valid {
		t := time.Unix(lastRunAt.Int64, 0).UTC()
		value.LastRunAt = &t
	}
	value.CreatedAt = time.Unix(createdAt, 0).UTC()
	value.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return value, nil
}

func scanSMSTestResult(row rowScanner) (SMSTestResult, error) {
	var value SMSTestResult
	var sentAt, createdAt int64
	var receivedAt sql.NullInt64
	err := row.Scan(&value.ID, &value.ScheduleID, &sentAt, &receivedAt, &value.Code,
		&value.Status, &value.SendResponse, &value.DeviceID, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SMSTestResult{}, ErrNotFound
	}
	if err != nil {
		return SMSTestResult{}, fmt.Errorf("scan sms test result: %w", err)
	}
	if receivedAt.Valid {
		t := time.Unix(receivedAt.Int64, 0).UTC()
		value.ReceivedAt = &t
	}
	value.SentAt = time.Unix(sentAt, 0).UTC()
	value.CreatedAt = time.Unix(createdAt, 0).UTC()
	return value, nil
}
