package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vocat/internal/store"
	"vocat/internal/vowifi"
)

func callRecordServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return &Server{store: database, logger: regionTestLogger(), maxRequestBodyBytes: 4096}, database
}

func at(seconds int) *time.Time {
	value := time.Unix(int64(seconds), 0).UTC()
	return &value
}

// Duration is answer to hang-up, which is what a carrier bills. A call that
// rang for a minute and was never picked up lasted zero seconds.
func TestCallRecordDurationCountsFromTheAnswer(t *testing.T) {
	config := store.Device{ID: "slot1", Name: "SLOT1-1"}
	record := callRecordFrom(config, vowifi.Call{
		ID: "c1", Direction: "outgoing", Number: "12125551234", State: "ended",
		StartedAt: time.Unix(1000, 0).UTC(), AnsweredAt: at(1010), EndedAt: at(1130),
	})
	if record.DurationSeconds != 120 {
		t.Fatalf("duration = %d, want 120", record.DurationSeconds)
	}
	if record.Disposition != "answered" {
		t.Fatalf("disposition = %q", record.Disposition)
	}

	unanswered := callRecordFrom(config, vowifi.Call{
		ID: "c2", Direction: "outgoing", State: "ended",
		StartedAt: time.Unix(1000, 0).UTC(), EndedAt: at(1060),
	})
	if unanswered.DurationSeconds != 0 {
		t.Fatalf("an unanswered call lasted %d seconds", unanswered.DurationSeconds)
	}
}

// The disposition is how a call finished. A call still in progress has not
// finished, so it gets none rather than a guess that would stick.
func TestCallDispositionNamesTheOutcome(t *testing.T) {
	for name, testCase := range map[string]struct {
		call vowifi.Call
		want string
	}{
		"in progress":   {vowifi.Call{State: "active", AnsweredAt: at(10)}, ""},
		"ringing":       {vowifi.Call{State: "ringing"}, ""},
		"answered":      {vowifi.Call{State: "ended", AnsweredAt: at(10), EndedAt: at(20)}, "answered"},
		"busy":          {vowifi.Call{State: "ended", SIPCode: 486, EndedAt: at(20)}, "busy"},
		"declined":      {vowifi.Call{State: "ended", SIPCode: 600, EndedAt: at(20)}, "busy"},
		"unavailable":   {vowifi.Call{State: "ended", SIPCode: 480, EndedAt: at(20)}, "no_answer"},
		"timed out":     {vowifi.Call{State: "ended", SIPCode: 408, EndedAt: at(20)}, "no_answer"},
		"cancelled":     {vowifi.Call{State: "ended", SIPCode: 487, EndedAt: at(20)}, "cancelled"},
		"no code":       {vowifi.Call{State: "ended", EndedAt: at(20)}, "no_answer"},
		"rejected":      {vowifi.Call{State: "failed", SIPCode: 403, EndedAt: at(20)}, "failed"},
		"failed no end": {vowifi.Call{State: "failed", SIPCode: 503}, "failed"},
	} {
		if got := callDisposition(testCase.call); got != testCase.want {
			t.Errorf("%s: disposition = %q, want %q", name, got, testCase.want)
		}
	}
}

// The recorder sees one call several times as it progresses. A later poll
// that reports less than an earlier one must not erase what was already
// recorded, or a finished call ends up a blank row.
func TestUpsertCallRecordKeepsWhatWasAlreadyKnown(t *testing.T) {
	_, database := callRecordServer(t)
	ctx := context.Background()
	full := store.CallRecord{
		CallID: "c1", DeviceID: "slot1", DeviceName: "SLOT1-1",
		Direction: "outgoing", Source: "trunk", PeerNumber: "12125551234",
		StartedAt: time.Unix(1000, 0).UTC(), AnsweredAt: at(1010),
	}
	if err := database.UpsertCallRecord(ctx, full); err != nil {
		t.Fatal(err)
	}
	// A teardown poll carrying only the ending.
	if err := database.UpsertCallRecord(ctx, store.CallRecord{
		CallID: "c1", DeviceID: "slot1", StartedAt: time.Unix(1000, 0).UTC(),
		EndedAt: at(1130), DurationSeconds: 120, Disposition: "answered",
	}); err != nil {
		t.Fatal(err)
	}
	records, total, err := database.ListCallRecords(ctx, store.CallRecordFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(records) != 1 {
		t.Fatalf("got %d records, want one row for one call", total)
	}
	record := records[0]
	if record.PeerNumber != "12125551234" || record.Source != "trunk" || record.DeviceName != "SLOT1-1" {
		t.Fatalf("the second write erased what the first knew: %+v", record)
	}
	if record.AnsweredAt == nil || record.EndedAt == nil || record.DurationSeconds != 120 {
		t.Fatalf("the ending was not recorded: %+v", record)
	}
}

// Filters are what make a history usable at all once there are thousands of
// rows.
func TestListCallRecordsFilters(t *testing.T) {
	_, database := callRecordServer(t)
	ctx := context.Background()
	for _, record := range []store.CallRecord{
		{CallID: "a", DeviceID: "slot1", Direction: "outgoing", PeerNumber: "12125551234",
			Disposition: "answered", StartedAt: time.Unix(3000, 0).UTC()},
		{CallID: "b", DeviceID: "slot1", Direction: "incoming", PeerNumber: "13105557777",
			Disposition: "no_answer", StartedAt: time.Unix(2000, 0).UTC()},
		{CallID: "c", DeviceID: "slot2", Direction: "outgoing", PeerNumber: "12125559999",
			Disposition: "answered", StartedAt: time.Unix(1000, 0).UTC()},
	} {
		if err := database.UpsertCallRecord(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	// Newest first, so the most recent call is the one you see without
	// scrolling.
	records, total, err := database.ListCallRecords(ctx, store.CallRecordFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || records[0].CallID != "a" {
		t.Fatalf("listing = %d records starting with %q", total, records[0].CallID)
	}
	for name, testCase := range map[string]struct {
		filter store.CallRecordFilter
		want   int
	}{
		"by device":      {store.CallRecordFilter{DeviceID: "slot1"}, 2},
		"by direction":   {store.CallRecordFilter{Direction: "incoming"}, 1},
		"by disposition": {store.CallRecordFilter{Disposition: "answered"}, 2},
		"by number":      {store.CallRecordFilter{Search: "1212555"}, 2},
	} {
		_, got, err := database.ListCallRecords(ctx, testCase.filter)
		if err != nil {
			t.Fatal(err)
		}
		if got != testCase.want {
			t.Errorf("%s: %d records, want %d", name, got, testCase.want)
		}
	}
}

// History that grows for ever is a database nobody asked for.
func TestDeleteCallRecordsBeforePrunes(t *testing.T) {
	_, database := callRecordServer(t)
	ctx := context.Background()
	old := time.Now().Add(-200 * 24 * time.Hour)
	for callID, started := range map[string]time.Time{"old": old, "new": time.Now()} {
		if err := database.UpsertCallRecord(ctx, store.CallRecord{
			CallID: callID, DeviceID: "slot1", StartedAt: started,
		}); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := database.DeleteCallRecordsBefore(ctx, time.Now().Add(-90*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("pruned %d records, want 1", removed)
	}
	records, _, _ := database.ListCallRecords(ctx, store.CallRecordFilter{})
	if len(records) != 1 || records[0].CallID != "new" {
		t.Fatalf("the wrong record survived: %+v", records)
	}
}

// The API is read-only and paged. A history that could be edited would be a
// claim rather than a record.
func TestCallRecordsAPIIsReadOnlyAndPaged(t *testing.T) {
	server, database := callRecordServer(t)
	ctx := context.Background()
	for index := 0; index < 5; index++ {
		if err := database.UpsertCallRecord(ctx, store.CallRecord{
			CallID: string(rune('a' + index)), DeviceID: "slot1", PeerNumber: "1212555000" + string(rune('0'+index)),
			StartedAt: time.Unix(int64(1000+index), 0).UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	response := httptest.NewRecorder()
	server.handleCallRecords(response, httptest.NewRequest(http.MethodGet, "/api/calls/records?limit=2", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Data struct {
			Records []callRecordPayload `json:"records"`
			Total   int                 `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data.Records) != 2 {
		t.Fatalf("returned %d records for limit=2", len(body.Data.Records))
	}
	// The total is what lets a page say how many there are rather than only
	// how many it showed.
	if body.Data.Total != 5 {
		t.Fatalf("total = %d, want 5", body.Data.Total)
	}

	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPut} {
		response := httptest.NewRecorder()
		server.handleCallRecords(response, httptest.NewRequest(method, "/api/calls/records", nil))
		if response.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s gave status %d, want 405", method, response.Code)
		}
	}
	// An unbounded limit would turn a long history into one response.
	response = httptest.NewRecorder()
	server.handleCallRecords(response, httptest.NewRequest(http.MethodGet, "/api/calls/records?limit=100000", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("an oversized limit gave status %d", response.Code)
	}
}

// The trunk is the only thing that knows it drove a call; the recorder sees
// the call only through the IMS session. The note has to outlive the hang-up,
// or every trunk call is recorded as the browser's.
func TestCallSourceSurvivesTheHangup(t *testing.T) {
	server, _ := callRecordServer(t)
	if got := server.callSource("slot1", "c1"); got != "browser" {
		t.Fatalf("an unknown call = %q, want browser", got)
	}
	server.noteTrunkCall("slot1", "c1")
	server.claimTrunkCall("slot1", "c1")
	server.releaseTrunkCall("slot1", "c1")
	if got := server.callSource("slot1", "c1"); got != "trunk" {
		t.Fatalf("after hang-up the call reads as %q, want trunk", got)
	}
}
