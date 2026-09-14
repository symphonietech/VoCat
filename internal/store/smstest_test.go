package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newSMSTestStore(t *testing.T) *Store {
	t.Helper()
	database, err := Open(context.Background(), filepath.Join(t.TempDir(), "vocat.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestSMSTestEndpointRoundTrip(t *testing.T) {
	ctx := context.Background()
	database := newSMSTestStore(t)

	saved, err := database.UpsertSMSTestEndpoint(ctx, SMSTestEndpoint{
		ID:   "ep1",
		Name: "gateway",
		URL:  "https://example.test/send",
	})
	if err != nil {
		t.Fatalf("UpsertSMSTestEndpoint() error = %v", err)
	}
	// Defaults are applied for the fields the caller left blank.
	if saved.Method != "POST" || saved.Headers != "[]" || saved.BodyParams != "[]" {
		t.Fatalf("defaults not applied: %+v", saved)
	}

	loaded, err := database.SMSTestEndpoint(ctx, "ep1")
	if err != nil {
		t.Fatalf("SMSTestEndpoint() error = %v", err)
	}
	if loaded.Name != "gateway" || loaded.URL != "https://example.test/send" {
		t.Fatalf("loaded = %+v", loaded)
	}

	if _, err := database.UpsertSMSTestEndpoint(ctx, SMSTestEndpoint{ID: "ep1", Name: "renamed", Method: "PUT"}); err != nil {
		t.Fatalf("update error = %v", err)
	}
	loaded, _ = database.SMSTestEndpoint(ctx, "ep1")
	if loaded.Name != "renamed" || loaded.Method != "PUT" {
		t.Fatalf("update did not apply: %+v", loaded)
	}

	list, err := database.ListSMSTestEndpoints(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSMSTestEndpoints() = %+v, %v", list, err)
	}

	if err := database.DeleteSMSTestEndpoint(ctx, "ep1"); err != nil {
		t.Fatalf("DeleteSMSTestEndpoint() error = %v", err)
	}
	if _, err := database.SMSTestEndpoint(ctx, "ep1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read after delete error = %v, want ErrNotFound", err)
	}
	if err := database.DeleteSMSTestEndpoint(ctx, "ep1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete error = %v, want ErrNotFound", err)
	}
}

func TestSMSTestScheduleRoundTrip(t *testing.T) {
	ctx := context.Background()
	database := newSMSTestStore(t)

	saved, err := database.UpsertSMSTestSchedule(ctx, SMSTestSchedule{
		ID:         "sc1",
		Name:       "hourly",
		EndpointID: "ep1",
		Recipient:  "+15551234567",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("UpsertSMSTestSchedule() error = %v", err)
	}
	if saved.CodeType != "digits" || saved.CodeLength != 6 || saved.FrequencyMinutes != 60 {
		t.Fatalf("defaults not applied: %+v", saved)
	}
	if saved.LastRunAt != nil {
		t.Fatal("a new schedule must not have a last run time")
	}

	runAt := time.Now().UTC().Truncate(time.Second)
	if err := database.TouchSMSTestScheduleRun(ctx, "sc1", runAt); err != nil {
		t.Fatalf("TouchSMSTestScheduleRun() error = %v", err)
	}
	loaded, err := database.SMSTestSchedule(ctx, "sc1")
	if err != nil {
		t.Fatalf("SMSTestSchedule() error = %v", err)
	}
	if loaded.LastRunAt == nil || !loaded.LastRunAt.Equal(runAt) {
		t.Fatalf("last run = %v, want %v", loaded.LastRunAt, runAt)
	}

	// An update must not clobber the run cursor the scheduler owns.
	if _, err := database.UpsertSMSTestSchedule(ctx, SMSTestSchedule{ID: "sc1", Name: "renamed", Enabled: false}); err != nil {
		t.Fatalf("update error = %v", err)
	}
	loaded, _ = database.SMSTestSchedule(ctx, "sc1")
	if loaded.Name != "renamed" || loaded.Enabled {
		t.Fatalf("update did not apply: %+v", loaded)
	}
	if loaded.LastRunAt == nil || !loaded.LastRunAt.Equal(runAt) {
		t.Fatalf("update cleared last run time: %v", loaded.LastRunAt)
	}
}

func TestSMSTestResultLifecycleAndCascade(t *testing.T) {
	ctx := context.Background()
	database := newSMSTestStore(t)

	if _, err := database.UpsertSMSTestSchedule(ctx, SMSTestSchedule{ID: "sc1", Name: "hourly"}); err != nil {
		t.Fatal(err)
	}
	sentAt := time.Now().UTC().Add(-30 * time.Second).Truncate(time.Second)
	created, err := database.CreateSMSTestResult(ctx, SMSTestResult{
		ScheduleID: "sc1",
		SentAt:     sentAt,
		Code:       "123456",
	})
	if err != nil {
		t.Fatalf("CreateSMSTestResult() error = %v", err)
	}
	if created.ID == 0 || created.Status != "pending" {
		t.Fatalf("created = %+v", created)
	}

	pending, err := database.ListPendingSMSTestResults(ctx)
	if err != nil || len(pending) != 1 || pending[0].Code != "123456" {
		t.Fatalf("ListPendingSMSTestResults() = %+v, %v", pending, err)
	}

	receivedAt := time.Now().UTC().Truncate(time.Second)
	if err := database.SettleSMSTestResult(ctx, created.ID, receivedAt, "received", "modem-1"); err != nil {
		t.Fatalf("SettleSMSTestResult() error = %v", err)
	}
	if pending, _ = database.ListPendingSMSTestResults(ctx); len(pending) != 0 {
		t.Fatalf("settled result still pending: %+v", pending)
	}

	results, err := database.ListSMSTestResults(ctx, "sc1", sentAt.Add(-time.Hour), 100)
	if err != nil || len(results) != 1 {
		t.Fatalf("ListSMSTestResults() = %+v, %v", results, err)
	}
	if results[0].Status != "received" || results[0].DeviceID != "modem-1" {
		t.Fatalf("settled result = %+v", results[0])
	}
	if results[0].ReceivedAt == nil || !results[0].ReceivedAt.Equal(receivedAt) {
		t.Fatalf("received at = %v, want %v", results[0].ReceivedAt, receivedAt)
	}

	// The "since" window excludes older sends.
	if results, _ = database.ListSMSTestResults(ctx, "", time.Now().UTC().Add(time.Hour), 100); len(results) != 0 {
		t.Fatalf("future since window returned %+v", results)
	}

	// Deleting the schedule cascades its results away.
	if err := database.DeleteSMSTestSchedule(ctx, "sc1"); err != nil {
		t.Fatalf("DeleteSMSTestSchedule() error = %v", err)
	}
	if results, _ = database.ListSMSTestResults(ctx, "", sentAt.Add(-time.Hour), 100); len(results) != 0 {
		t.Fatalf("results survived schedule delete: %+v", results)
	}
}
