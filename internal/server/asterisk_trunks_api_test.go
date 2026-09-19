package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"vocat/internal/store"
)

func trunksTestServer(t *testing.T) (*Server, *store.Store, string) {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dir := t.TempDir()
	return &Server{
		store:               database,
		logger:              regionTestLogger(),
		maxRequestBodyBytes: 16384,
		asteriskDialplanDir: dir,
	}, database, dir
}

func putTrunks(t *testing.T, server *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPut, "/api/asterisk/trunks", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handlePutAsteriskTrunks(response, request)
	return response
}

func storedTrunks(t *testing.T, database *store.Store) []asteriskTrunk {
	t.Helper()
	setting, err := database.AppSetting(context.Background(), asteriskTrunksKey)
	if err != nil {
		return nil
	}
	var trunks []asteriskTrunk
	if err := json.Unmarshal(setting.Value, &trunks); err != nil {
		t.Fatalf("stored trunks are unreadable: %v", err)
	}
	return trunks
}

const trunkWithSecret = `{"trunks":[{"name":"acme","host":"203.0.113.10","port":5060,` +
	`"transport":"udp","username":"acme","password":"a-long-enough-secret",` +
	`"destinations":["_1NXXNXXXXXX"],"devices":["slot1"],` +
	`"max_concurrent":4,"timeout_seconds":60}]}`

// A trunk with no password keeps the one already stored: the browser never
// had it, so every edit to a destination list would otherwise wipe it.
func TestTrunkPasswordIsWriteOnly(t *testing.T) {
	server, database, _ := trunksTestServer(t)

	created := putTrunks(t, server, trunkWithSecret)
	if created.Code != http.StatusOK {
		t.Fatalf("save status = %d, body = %s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), "a-long-enough-secret") {
		t.Fatalf("the password came back out: %s", created.Body.String())
	}
	if !strings.Contains(created.Body.String(), "has_password") {
		t.Errorf("the editor cannot tell a password is stored: %s", created.Body.String())
	}

	edited := `{"trunks":[{"name":"acme","host":"203.0.113.10","port":5060,"transport":"udp",` +
		`"username":"acme","destinations":["_1NXXNXXXXXX","_44X."],"devices":["slot1"],` +
		`"max_concurrent":4,"timeout_seconds":60}]}`
	if response := putTrunks(t, server, edited); response.Code != http.StatusOK {
		t.Fatalf("edit status = %d, body = %s", response.Code, response.Body.String())
	}
	stored := storedTrunks(t, database)
	if len(stored) != 1 || stored[0].Password != "a-long-enough-secret" {
		t.Fatalf("the stored password was lost: %+v", stored)
	}
	if len(stored[0].Destinations) != 2 {
		t.Fatalf("the edit was not applied: %+v", stored)
	}
	// has_password is an output marker and must never be stored.
	if stored[0].HasPassword {
		t.Errorf("the output marker was stored: %+v", stored[0])
	}
}

// The GET must never carry a credential, whatever else it carries.
func TestTrunkGetNeverReturnsThePassword(t *testing.T) {
	server, _, _ := trunksTestServer(t)
	if response := putTrunks(t, server, trunkWithSecret); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	response := httptest.NewRecorder()
	server.handleGetAsteriskTrunks(response, httptest.NewRequest(http.MethodGet, "/api/asterisk/trunks", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "a-long-enough-secret") {
		t.Fatalf("the password is readable through the API: %s", response.Body.String())
	}
	// The preview exists to answer a syntax question, which does not need the
	// secret.
	if !strings.Contains(response.Body.String(), "password=\\u003chidden\\u003e") {
		t.Errorf("the preview does not redact the password: %s", response.Body.String())
	}
}

// A peer that would accept an INVITE from anywhere is the whole attack.
func TestTrunkSaveRefusesAnUnidentifiedPeer(t *testing.T) {
	server, database, _ := trunksTestServer(t)
	body := `{"trunks":[{"name":"acme","host":"203.0.113.10","port":5060,"transport":"udp",` +
		`"devices":["slot1"],"max_concurrent":4,"timeout_seconds":60}]}`
	response := putTrunks(t, server, body)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", response.Code, response.Body.String())
	}
	// A rejected trunk must not be stored, or the page would show a peer that
	// can never be applied.
	if trunks := storedTrunks(t, database); len(trunks) != 0 {
		t.Fatalf("a rejected trunk was stored: %+v", trunks)
	}
}

// Both files are written, and the one that may hold credentials is not
// world-readable.
func TestTrunkSaveWritesBothFilesAndNarrowsTheSecretOne(t *testing.T) {
	server, _, dir := trunksTestServer(t)
	if response := putTrunks(t, server, trunkWithSecret); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	info, err := os.Stat(server.asteriskTrunksPath())
	if err != nil {
		t.Fatalf("the PJSIP objects were not written: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("trunks.conf mode = %o, want 600 -- it holds a SIP password", mode)
	}
	objects, err := os.ReadFile(server.asteriskTrunksPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(objects), "[trunk-acme]") {
		t.Errorf("the PJSIP object is missing:\n%s", objects)
	}
	routes, err := os.ReadFile(server.asteriskTrunkRoutesPath())
	if err != nil {
		t.Fatalf("the dialplan was not written: %v", err)
	}
	if !strings.Contains(string(routes), "[from-trunk-acme]") {
		t.Errorf("the trunk context is missing:\n%s", routes)
	}
	// The dialplan holds no secret, so it is not narrowed and does not need
	// to be; what matters is that it is separate from the file that does.
	if strings.Contains(string(routes), "a-long-enough-secret") {
		t.Errorf("a credential reached the dialplan file:\n%s", routes)
	}
	_ = dir
}

// The page must render before anything is configured, so an empty list is a
// normal answer rather than an error.
func TestTrunkListStartsEmpty(t *testing.T) {
	server, _, _ := trunksTestServer(t)
	response := httptest.NewRecorder()
	server.handleGetAsteriskTrunks(response, httptest.NewRequest(http.MethodGet, "/api/asterisk/trunks", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"trunks":[]`) {
		t.Fatalf("an empty trunk list did not render: %s", response.Body.String())
	}
}

// Pending drives the Apply button and must account for both files: a stale
// dialplan with fresh PJSIP objects is still a reload waiting to happen.
func TestTrunkPendingCoversBothFiles(t *testing.T) {
	server, _, _ := trunksTestServer(t)
	if response := putTrunks(t, server, trunkWithSecret); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	if err := os.WriteFile(server.asteriskTrunkRoutesPath(), []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.handleGetAsteriskTrunks(response, httptest.NewRequest(http.MethodGet, "/api/asterisk/trunks", nil))
	if !strings.Contains(response.Body.String(), `"pending":true`) {
		t.Fatalf("a stale dialplan did not report as pending: %s", response.Body.String())
	}
}
