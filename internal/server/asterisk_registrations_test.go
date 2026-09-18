package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vocat/internal/store"
)

func registrationServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return &Server{store: database, logger: regionTestLogger(), maxRequestBodyBytes: 4096}, database
}

// One row per change, not per poll. A table with a row every thirty seconds
// buries the only thing anyone looks for, which is when it stopped being up.
func TestRegistrationHistoryRecordsTransitions(t *testing.T) {
	_, database := registrationServer(t)
	ctx := context.Background()
	for _, event := range []store.RegistrationEvent{
		{Endpoint: "1001", ContactURI: "sip:1001@a", Status: "Reachable",
			ChangedAt: time.Unix(1000, 0)},
		{Endpoint: "1001", ContactURI: "sip:1001@a", Status: "Unreachable",
			PreviousStatus: "Reachable", ChangedAt: time.Unix(2000, 0)},
		{Endpoint: "vocat", ContactURI: "sip:vocat@b", Status: "Reachable",
			ChangedAt: time.Unix(1500, 0)},
	} {
		if err := database.AppendRegistrationEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	events, err := database.ListRegistrationEvents(ctx, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Status != "Unreachable" {
		t.Fatalf("newest first is wrong: %+v", events)
	}
	// A row reads as a transition rather than a state needing the row above
	// it to interpret.
	if events[0].PreviousStatus != "Reachable" {
		t.Errorf("previous status = %q", events[0].PreviousStatus)
	}
	byEndpoint, err := database.ListRegistrationEvents(ctx, "vocat", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(byEndpoint) != 1 {
		t.Fatalf("filtering by endpoint returned %d", len(byEndpoint))
	}
}

// Without seeding, the first poll after a restart records a change for every
// contact that did not change at all.
func TestLatestRegistrationStatesSeedsTheRecorder(t *testing.T) {
	_, database := registrationServer(t)
	ctx := context.Background()
	for _, event := range []store.RegistrationEvent{
		{Endpoint: "1001", ContactURI: "sip:a", Status: "Reachable", ChangedAt: time.Unix(1000, 0)},
		{Endpoint: "1001", ContactURI: "sip:a", Status: "Unreachable", ChangedAt: time.Unix(2000, 0)},
		{Endpoint: "1002", ContactURI: "sip:b", Status: "Reachable", ChangedAt: time.Unix(1500, 0)},
	} {
		if err := database.AppendRegistrationEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	states, err := database.LatestRegistrationStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := states["1001\x00sip:a"]; got != "Unreachable" {
		t.Errorf("1001 seeded as %q, want the latest", got)
	}
	if got := states["1002\x00sip:b"]; got != "Reachable" {
		t.Errorf("1002 seeded as %q", got)
	}
}

// History that grows for ever is a database nobody asked for.
func TestDeleteRegistrationEventsBeforePrunes(t *testing.T) {
	_, database := registrationServer(t)
	ctx := context.Background()
	for _, when := range []time.Time{time.Now().Add(-90 * 24 * time.Hour), time.Now()} {
		if err := database.AppendRegistrationEvent(ctx, store.RegistrationEvent{
			Endpoint: "1001", ContactURI: "sip:a", Status: "Reachable", ChangedAt: when,
		}); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := database.DeleteRegistrationEventsBefore(ctx, time.Now().Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("pruned %d, want 1", removed)
	}
}

// An empty list on a PBX with no manager interface means history is off, not
// that nothing has happened. The two look identical otherwise.
func TestRegistrationsAPIReportsWhetherItIsRecording(t *testing.T) {
	server, _ := registrationServer(t)
	response := httptest.NewRecorder()
	server.handleAsteriskRegistrations(response,
		httptest.NewRequest(http.MethodGet, "/api/asterisk/registrations", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var body struct {
		Data struct {
			Recording     bool                  `json:"recording"`
			Registrations []registrationPayload `json:"registrations"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data.Recording {
		t.Fatal("recording is true with no manager address configured")
	}
	if body.Data.Registrations == nil {
		t.Fatal("registrations is null rather than an empty list")
	}

	response = httptest.NewRecorder()
	server.handleAsteriskRegistrations(response,
		httptest.NewRequest(http.MethodGet, "/api/asterisk/registrations?limit=0", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("limit=0 gave status %d, want 400", response.Code)
	}
}

// The recorder must not start at all without a manager address, or it would
// dial nothing every thirty seconds for the life of the process.
func TestRegistrationRecorderStopsWithoutAMI(t *testing.T) {
	server, _ := registrationServer(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.StartRegistrationRecorder(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the recorder is running with no manager address configured")
	}
}
