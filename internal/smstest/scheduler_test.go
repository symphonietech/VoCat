package smstest

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vocat/internal/store"
)

func newTestScheduler(t *testing.T) (*Scheduler, *store.Store) {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "vocat.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	scheduler := New(database, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return scheduler, database
}

func seedSchedule(t *testing.T, database *store.Store, url string, schedule store.SMSTestSchedule) store.SMSTestSchedule {
	t.Helper()
	ctx := context.Background()
	body, _ := json.Marshal([]keyValue{
		{Key: "to", Value: "{{to}}"},
		{Key: "text", Value: "{{content}}"},
	})
	if _, err := database.UpsertSMSTestEndpoint(ctx, store.SMSTestEndpoint{
		ID:         "ep1",
		Name:       "gateway",
		Method:     http.MethodPost,
		URL:        url,
		BodyParams: string(body),
	}); err != nil {
		t.Fatal(err)
	}
	schedule.ID = "sc1"
	schedule.EndpointID = "ep1"
	if schedule.ContentTemplate == "" {
		schedule.ContentTemplate = "Your code is {{code}}"
	}
	saved, err := database.UpsertSMSTestSchedule(ctx, schedule)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestRunScheduleSubmitsThroughGatewayAndRecordsPending(t *testing.T) {
	ctx := context.Background()
	scheduler, database := newTestScheduler(t)

	var gotBody map[string]string
	var gotMethod string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer gateway.Close()

	schedule := seedSchedule(t, database, gateway.URL, store.SMSTestSchedule{
		Name: "hourly", Recipient: "+15551234567", CodeType: "digits", CodeLength: 6,
	})

	result, err := scheduler.RunSchedule(ctx, schedule)
	if err != nil {
		t.Fatalf("RunSchedule() error = %v", err)
	}
	if result.Status != "pending" {
		t.Fatalf("status = %q, want pending", result.Status)
	}
	if len(result.Code) != 6 {
		t.Fatalf("code = %q, want 6 digits", result.Code)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("gateway method = %q", gotMethod)
	}
	// Placeholders must be substituted before the gateway sees them.
	if gotBody["to"] != "+15551234567" {
		t.Fatalf("gateway body to = %q", gotBody["to"])
	}
	if gotBody["text"] != "Your code is "+result.Code {
		t.Fatalf("gateway body text = %q, code = %q", gotBody["text"], result.Code)
	}

	// The run cursor advances so the ticker will not immediately re-fire.
	reloaded, err := database.SMSTestSchedule(ctx, schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.LastRunAt == nil {
		t.Fatal("last run time was not recorded")
	}
}

func TestRunScheduleRecordsGatewayFailure(t *testing.T) {
	ctx := context.Background()
	scheduler, database := newTestScheduler(t)

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream exploded"))
	}))
	defer gateway.Close()

	schedule := seedSchedule(t, database, gateway.URL, store.SMSTestSchedule{Name: "broken", Recipient: "+1555"})
	result, err := scheduler.RunSchedule(ctx, schedule)
	if err != nil {
		t.Fatalf("RunSchedule() error = %v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("status = %q, want failed", result.Status)
	}
	if result.SendResponse == "" {
		t.Fatal("gateway error detail was not recorded")
	}
}

func TestRunScheduleMarksExternalRecipientAsSent(t *testing.T) {
	ctx := context.Background()
	scheduler, database := newTestScheduler(t)

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer gateway.Close()

	schedule := seedSchedule(t, database, gateway.URL, store.SMSTestSchedule{
		Name: "external", Recipient: "+15550000000", IsExternal: true,
	})
	result, err := scheduler.RunSchedule(ctx, schedule)
	if err != nil {
		t.Fatalf("RunSchedule() error = %v", err)
	}
	// Nothing will arrive on our own modems, so it must not sit pending and
	// later expire as a false failure.
	if result.Status != "sent" {
		t.Fatalf("status = %q, want sent", result.Status)
	}
}

func TestReconcileMatchesInboundCodeAndMeasuresLatency(t *testing.T) {
	ctx := context.Background()
	scheduler, database := newTestScheduler(t)

	sentAt := time.Now().UTC().Add(-10 * time.Second)
	if _, err := database.UpsertSMSTestSchedule(ctx, store.SMSTestSchedule{ID: "sc1", Name: "t"}); err != nil {
		t.Fatal(err)
	}
	result, err := database.CreateSMSTestResult(ctx, store.SMSTestResult{
		ScheduleID: "sc1", SentAt: sentAt, Code: "482913",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.SaveSMSMessage(ctx, store.SMSMessage{
		MessageID: "m1",
		DeviceID:  "modem-7",
		Peer:      "+15551234567",
		Direction: "inbound",
		Body:      "Your code is 482913, do not share it",
		Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	scheduler.reconcilePending(ctx)

	results, err := database.ListSMSTestResults(ctx, "sc1", sentAt.Add(-time.Hour), 10)
	if err != nil || len(results) != 1 {
		t.Fatalf("ListSMSTestResults() = %+v, %v", results, err)
	}
	settled := results[0]
	if settled.ID != result.ID {
		t.Fatalf("unexpected result id %d", settled.ID)
	}
	if settled.Status != "received" {
		t.Fatalf("status = %q, want received", settled.Status)
	}
	if settled.DeviceID != "modem-7" {
		t.Fatalf("device = %q, want the receiving modem", settled.DeviceID)
	}
	if settled.ReceivedAt == nil {
		t.Fatal("received time was not recorded")
	}
}

func TestReconcileFlagsSlowRoundTripAsDelayed(t *testing.T) {
	ctx := context.Background()
	scheduler, database := newTestScheduler(t)

	// Sent well over the delayed threshold before the message shows up.
	sentAt := time.Now().UTC().Add(-3 * time.Minute)
	if _, err := database.UpsertSMSTestSchedule(ctx, store.SMSTestSchedule{ID: "sc1", Name: "t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateSMSTestResult(ctx, store.SMSTestResult{
		ScheduleID: "sc1", SentAt: sentAt, Code: "777111",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SaveSMSMessage(ctx, store.SMSMessage{
		MessageID: "m1", DeviceID: "modem-1", Peer: "+15551234567", Direction: "inbound",
		Body: "code 777111", Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	scheduler.reconcilePending(ctx)

	results, _ := database.ListSMSTestResults(ctx, "sc1", sentAt.Add(-time.Hour), 10)
	if len(results) != 1 || results[0].Status != "delayed" {
		t.Fatalf("results = %+v, want one delayed", results)
	}
}

func TestReconcileExpiresPendingResultWithNoMatch(t *testing.T) {
	ctx := context.Background()
	scheduler, database := newTestScheduler(t)

	sentAt := time.Now().UTC().Add(-10 * time.Minute)
	if _, err := database.UpsertSMSTestSchedule(ctx, store.SMSTestSchedule{ID: "sc1", Name: "t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateSMSTestResult(ctx, store.SMSTestResult{
		ScheduleID: "sc1", SentAt: sentAt, Code: "999999",
	}); err != nil {
		t.Fatal(err)
	}

	scheduler.reconcilePending(ctx)

	results, _ := database.ListSMSTestResults(ctx, "sc1", sentAt.Add(-time.Hour), 10)
	if len(results) != 1 || results[0].Status != "failed" {
		t.Fatalf("results = %+v, want one failed", results)
	}
	if pending, _ := database.ListPendingSMSTestResults(ctx); len(pending) != 0 {
		t.Fatalf("expired result still pending: %+v", pending)
	}
}

func TestReconcileLeavesRecentPendingResultAlone(t *testing.T) {
	ctx := context.Background()
	scheduler, database := newTestScheduler(t)

	if _, err := database.UpsertSMSTestSchedule(ctx, store.SMSTestSchedule{ID: "sc1", Name: "t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateSMSTestResult(ctx, store.SMSTestResult{
		ScheduleID: "sc1", SentAt: time.Now().UTC().Add(-5 * time.Second), Code: "555000",
	}); err != nil {
		t.Fatal(err)
	}

	scheduler.reconcilePending(ctx)

	// Still inside the wait window: it must keep waiting, not fail early.
	if pending, _ := database.ListPendingSMSTestResults(ctx); len(pending) != 1 {
		t.Fatalf("recent pending result was settled too early: %+v", pending)
	}
}

func TestReconcileIgnoresOutboundEcho(t *testing.T) {
	ctx := context.Background()
	scheduler, database := newTestScheduler(t)

	sentAt := time.Now().UTC().Add(-5 * time.Second)
	if _, err := database.UpsertSMSTestSchedule(ctx, store.SMSTestSchedule{ID: "sc1", Name: "t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateSMSTestResult(ctx, store.SMSTestResult{
		ScheduleID: "sc1", SentAt: sentAt, Code: "246810",
	}); err != nil {
		t.Fatal(err)
	}
	// The outbound copy carries the same code; matching it would report a
	// successful delivery that never happened.
	if _, err := database.SaveSMSMessage(ctx, store.SMSMessage{
		MessageID: "m1", DeviceID: "modem-1", Peer: "+15551234567", Direction: "outbound",
		Body: "Your code is 246810", Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	scheduler.reconcilePending(ctx)

	if pending, _ := database.ListPendingSMSTestResults(ctx); len(pending) != 1 {
		t.Fatalf("outbound echo settled the test: %+v", pending)
	}
}

func TestScheduleIsDue(t *testing.T) {
	now := time.Date(2026, 9, 14, 10, 30, 0, 0, time.UTC)
	recent := now.Add(-5 * time.Minute)
	old := now.Add(-2 * time.Hour)

	cases := []struct {
		name     string
		schedule store.SMSTestSchedule
		want     bool
	}{
		{"never run, no start time", store.SMSTestSchedule{FrequencyMinutes: 60}, true},
		{"never run, start time passed", store.SMSTestSchedule{FrequencyMinutes: 60, StartTime: "09:00"}, true},
		{"never run, start time later today", store.SMSTestSchedule{FrequencyMinutes: 60, StartTime: "23:00"}, false},
		{"never run, start time this hour but later", store.SMSTestSchedule{FrequencyMinutes: 60, StartTime: "10:45"}, false},
		{"never run, start time this hour already passed", store.SMSTestSchedule{FrequencyMinutes: 60, StartTime: "10:15"}, true},
		{"interval not elapsed", store.SMSTestSchedule{FrequencyMinutes: 60, LastRunAt: &recent}, false},
		{"interval elapsed", store.SMSTestSchedule{FrequencyMinutes: 60, LastRunAt: &old}, true},
		{"malformed start time runs", store.SMSTestSchedule{FrequencyMinutes: 60, StartTime: "not-a-time"}, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := scheduleIsDue(testCase.schedule, now); got != testCase.want {
				t.Fatalf("scheduleIsDue() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestGenerateCode(t *testing.T) {
	for _, testCase := range []struct {
		codeType string
		length   int
		allowed  string
	}{
		{"digits", 6, "0123456789"},
		{"letters", 8, "ABCDEFGHIJKLMNOPQRSTUVWXYZ"},
		{"mixed", 10, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"},
		{"", 0, "0123456789"},
	} {
		code := generateCode(testCase.codeType, testCase.length)
		want := testCase.length
		if want <= 0 {
			want = 6
		}
		if len(code) != want {
			t.Fatalf("generateCode(%q, %d) length = %d, want %d", testCase.codeType, testCase.length, len(code), want)
		}
		for _, char := range code {
			if !containsRune(testCase.allowed, char) {
				t.Fatalf("generateCode(%q) = %q contains %q outside its alphabet", testCase.codeType, code, char)
			}
		}
	}
}

func containsRune(alphabet string, char rune) bool {
	for _, candidate := range alphabet {
		if candidate == char {
			return true
		}
	}
	return false
}

// The gateway password must not come back out through a result row. A gateway
// that takes credentials in the query string puts them in the URL, and Go's
// transport errors quote that URL back -- so a failed send would write the
// password into a record the results page returns.
func TestRedactSecretRemovesTheGatewayPassword(t *testing.T) {
	secret := "s3cret-gateway-pass"
	body := `Post "https://gw.example/send?user=bob&pass=` + secret + `&to=1": dial tcp: refused`
	got := redactSecret(body, secret)
	if strings.Contains(got, secret) {
		t.Fatalf("the password survived redaction: %s", got)
	}
	if !strings.Contains(got, secretMask) {
		t.Fatalf("nothing was redacted: %s", got)
	}
	// Every occurrence, because a gateway may echo the request as well as
	// the transport quoting it.
	twice := redactSecret(secret+" and "+secret, secret)
	if strings.Contains(twice, secret) {
		t.Fatalf("only the first occurrence was redacted: %s", twice)
	}
}

// Blanking every occurrence of a two-character password would mangle the
// response into something unreadable while protecting a secret that is not
// one.
func TestRedactSecretLeavesShortAndEmptyValuesAlone(t *testing.T) {
	if got := redactSecret("no secret here", ""); got != "no secret here" {
		t.Errorf("an empty secret changed the text: %q", got)
	}
	if got := redactSecret("a as in apple", "a"); got != "a as in apple" {
		t.Errorf("a one-character secret mangled the text: %q", got)
	}
	if got := redactSecret("", "longenough"); got != "" {
		t.Errorf("empty text became %q", got)
	}
}
