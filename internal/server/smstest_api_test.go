package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"vocat/internal/store"
)

func newSMSTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return &Server{store: database, logger: regionTestLogger(), maxRequestBodyBytes: 4096}, database
}

// callSMSTest routes one request through the SMS Test dispatcher exactly as
// handleAPI would, so the tests cover path parsing as well as the handlers.
func callSMSTest(t *testing.T, server *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	cleanPath := strings.Trim(strings.TrimPrefix(path, "/api"), "/")
	if index := strings.Index(cleanPath, "?"); index >= 0 {
		cleanPath = cleanPath[:index]
	}
	if !server.routeSMSTestAPI(response, request, cleanPath) {
		t.Fatalf("routeSMSTestAPI did not claim %s %s", method, path)
	}
	return response
}

func decodeSMSTestData(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response %s: %v", response.Body.String(), err)
	}
	return envelope.Data
}

func TestSMSTestEndpointCRUDOverHTTP(t *testing.T) {
	server, _ := newSMSTestServer(t)

	created := callSMSTest(t, server, http.MethodPost, "/api/smstest/endpoints",
		`{"name":"gateway","url":"https://example.test/send","method":"post","password":"secret"}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	data := decodeSMSTestData(t, created)
	id, _ := data["id"].(string)
	if id == "" {
		t.Fatalf("created endpoint has no id: %v", data)
	}
	if data["method"] != "POST" {
		t.Fatalf("method was not normalized: %v", data["method"])
	}
	// The gateway password must never come back over the wire.
	if _, leaked := data["password"]; leaked {
		t.Fatalf("response leaked the gateway password: %v", data)
	}
	if data["has_password"] != true {
		t.Fatalf("has_password = %v, want true", data["has_password"])
	}

	listed := callSMSTest(t, server, http.MethodGet, "/api/smstest/endpoints", "")
	endpoints, _ := decodeSMSTestData(t, listed)["endpoints"].([]any)
	if len(endpoints) != 1 {
		t.Fatalf("listed %d endpoints, want 1", len(endpoints))
	}

	// A blank password on update keeps the stored secret.
	updated := callSMSTest(t, server, http.MethodPut, "/api/smstest/endpoints/"+id,
		`{"name":"renamed","url":"https://example.test/send"}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updated.Code, updated.Body.String())
	}
	if got := decodeSMSTestData(t, updated); got["name"] != "renamed" || got["has_password"] != true {
		t.Fatalf("update dropped the stored password or name: %v", got)
	}

	deleted := callSMSTest(t, server, http.MethodDelete, "/api/smstest/endpoints/"+id, "")
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status = %d", deleted.Code)
	}
	missing := callSMSTest(t, server, http.MethodGet, "/api/smstest/endpoints/"+id, "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("read after delete status = %d, want 404", missing.Code)
	}
}

func TestSMSTestEndpointRejectsInvalidInput(t *testing.T) {
	server, _ := newSMSTestServer(t)

	for _, testCase := range []struct{ name, body string }{
		{"missing name", `{"url":"https://example.test"}`},
		{"missing scheme", `{"name":"x","url":"example.test"}`},
		{"unsupported method", `{"name":"x","url":"https://example.test","method":"TRACE"}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response := callSMSTest(t, server, http.MethodPost, "/api/smstest/endpoints", testCase.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestSMSTestScheduleRequiresExistingEndpoint(t *testing.T) {
	server, _ := newSMSTestServer(t)

	response := callSMSTest(t, server, http.MethodPost, "/api/smstest/schedules",
		`{"name":"hourly","endpoint_id":"nope","recipient":"+15551234567"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "endpoint") {
		t.Fatalf("error did not mention the endpoint: %s", response.Body.String())
	}
}

func TestSMSTestScheduleCRUDOverHTTP(t *testing.T) {
	ctx := context.Background()
	server, database := newSMSTestServer(t)
	if _, err := database.UpsertSMSTestEndpoint(ctx, store.SMSTestEndpoint{
		ID: "ep1", Name: "gateway", URL: "https://example.test/send",
	}); err != nil {
		t.Fatal(err)
	}

	created := callSMSTest(t, server, http.MethodPost, "/api/smstest/schedules",
		`{"name":"hourly","endpoint_id":"ep1","recipient":"+15551234567","content_template":"code {{code}}","frequency_minutes":30,"enabled":true}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	data := decodeSMSTestData(t, created)
	id, _ := data["id"].(string)
	if id == "" || data["enabled"] != true || data["frequency_minutes"] != float64(30) {
		t.Fatalf("created schedule = %v", data)
	}
	if data["last_run_at"] != nil {
		t.Fatalf("new schedule reported a last run: %v", data["last_run_at"])
	}

	// Editing must not reset the scheduler's run cursor.
	runAt := time.Now().UTC()
	if err := database.TouchSMSTestScheduleRun(ctx, id, runAt); err != nil {
		t.Fatal(err)
	}
	updated := callSMSTest(t, server, http.MethodPut, "/api/smstest/schedules/"+id,
		`{"name":"renamed","endpoint_id":"ep1","recipient":"+15551234567","enabled":false}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updated.Code, updated.Body.String())
	}
	if got := decodeSMSTestData(t, updated); got["last_run_at"] == nil {
		t.Fatalf("update cleared the run cursor: %v", got)
	}

	if response := callSMSTest(t, server, http.MethodDelete, "/api/smstest/schedules/"+id, ""); response.Code != http.StatusOK {
		t.Fatalf("delete status = %d", response.Code)
	}
}

func TestSMSTestScheduleRejectsBadStartTime(t *testing.T) {
	ctx := context.Background()
	server, database := newSMSTestServer(t)
	if _, err := database.UpsertSMSTestEndpoint(ctx, store.SMSTestEndpoint{
		ID: "ep1", Name: "gateway", URL: "https://example.test/send",
	}); err != nil {
		t.Fatal(err)
	}
	response := callSMSTest(t, server, http.MethodPost, "/api/smstest/schedules",
		`{"name":"x","endpoint_id":"ep1","recipient":"+1555","start_time":"25:99"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", response.Code, response.Body.String())
	}
}

func TestSMSTestResultsSummarizeByStatus(t *testing.T) {
	ctx := context.Background()
	server, database := newSMSTestServer(t)
	if _, err := database.UpsertSMSTestSchedule(ctx, store.SMSTestSchedule{ID: "sc1", Name: "hourly"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	sent, err := database.CreateSMSTestResult(ctx, store.SMSTestResult{
		ScheduleID: "sc1", SentAt: now.Add(-time.Minute), Code: "111111",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SettleSMSTestResult(ctx, sent.ID, now.Add(-30*time.Second), "received", "modem-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateSMSTestResult(ctx, store.SMSTestResult{
		ScheduleID: "sc1", SentAt: now.Add(-2 * time.Minute), Code: "222222", Status: "failed",
	}); err != nil {
		t.Fatal(err)
	}

	response := callSMSTest(t, server, http.MethodGet, "/api/smstest/results", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	data := decodeSMSTestData(t, response)
	results, _ := data["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results = %v, want 2", results)
	}
	summary, _ := data["summary"].(map[string]any)
	if summary["total"] != float64(2) || summary["received"] != float64(1) || summary["failed"] != float64(1) {
		t.Fatalf("summary = %v", summary)
	}

	// A settled result carries the measured round trip for the statistics view.
	first, _ := results[0].(map[string]any)
	if first["status"] == "received" && first["elapsed_ms"] == nil {
		t.Fatalf("received result has no elapsed time: %v", first)
	}
}

func TestSMSTestResultsRejectBadQuery(t *testing.T) {
	server, _ := newSMSTestServer(t)
	for _, query := range []string{"?hours=0", "?hours=abc", "?limit=-5"} {
		response := callSMSTest(t, server, http.MethodGet, "/api/smstest/results"+query, "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", query, response.Code)
		}
	}
}

func TestSMSTestRunWithoutSchedulerReportsUnavailable(t *testing.T) {
	server, _ := newSMSTestServer(t)
	response := callSMSTest(t, server, http.MethodPost, "/api/smstest/schedules/sc1/run", "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body = %s", response.Code, response.Body.String())
	}
}

func TestSMSTestUnknownPathIsNotFound(t *testing.T) {
	server, _ := newSMSTestServer(t)
	response := callSMSTest(t, server, http.MethodGet, "/api/smstest/nope", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}
