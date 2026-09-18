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

func extensionsTestServer(t *testing.T) (*Server, *store.Store, string) {
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

func putExtensions(t *testing.T, server *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPut, "/api/asterisk/extensions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handlePutAsteriskExtensions(response, request)
	return response
}

func getExtensions(t *testing.T, server *Server) map[string]any {
	t.Helper()
	response := httptest.NewRecorder()
	server.handleGetAsteriskExtensions(response, httptest.NewRequest(http.MethodGet, "/api/asterisk/extensions", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET status = %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Data
}

// The whole point of the write-only design: a browser session that can open
// the page must not be able to read the SIP credentials out of it, because a
// SIP credential is dialling access to a real SIM.
func TestAsteriskExtensionsNeverReturnsAPassword(t *testing.T) {
	server, _, _ := extensionsTestServer(t)
	secret := "correct-horse-battery"
	if code := putExtensions(t, server,
		`{"extensions":[{"name":"1001","password":"`+secret+`","max_contacts":2}]}`).Code; code != http.StatusOK {
		t.Fatalf("PUT status = %d", code)
	}
	// Neither the save response nor any later read may carry it.
	saved := putExtensions(t, server, `{"extensions":[{"name":"1001","max_contacts":2}]}`)
	if strings.Contains(saved.Body.String(), secret) {
		t.Fatalf("the save response leaked the password: %s", saved.Body.String())
	}
	response := httptest.NewRecorder()
	server.handleGetAsteriskExtensions(response, httptest.NewRequest(http.MethodGet, "/api/asterisk/extensions", nil))
	if strings.Contains(response.Body.String(), secret) {
		t.Fatalf("the extensions listing leaked the password: %s", response.Body.String())
	}
	// It must still be there for Asterisk, or write-only would mean write-once.
	body, err := os.ReadFile(server.asteriskEndpointsPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "password="+secret+"\n") {
		t.Fatalf("the rendered file has no usable password:\n%s", body)
	}
}

// The browser never had the password, so it cannot send it back. Without the
// merge, renaming a caller ID would silently invalidate the account.
func TestAsteriskExtensionsKeepsTheStoredPasswordOnEdit(t *testing.T) {
	server, _, _ := extensionsTestServer(t)
	putExtensions(t, server, `{"extensions":[{"name":"1001","password":"correct-horse-battery","max_contacts":2}]}`)
	if code := putExtensions(t, server,
		`{"extensions":[{"name":"1001","max_contacts":4,"caller_id":"Front Desk"}]}`).Code; code != http.StatusOK {
		t.Fatalf("edit without a password was refused")
	}
	body, _ := os.ReadFile(server.asteriskEndpointsPath())
	if !strings.Contains(string(body), "password=correct-horse-battery\n") {
		t.Fatalf("the password was lost on edit:\n%s", body)
	}
	if !strings.Contains(string(body), "max_contacts=4\n") {
		t.Fatalf("the edit was not applied:\n%s", body)
	}
}

// A new account with no password cannot be rendered at all, and saying which
// one beats a length complaint about an account the operator did not name.
func TestAsteriskExtensionsRefusesANewAccountWithNoPassword(t *testing.T) {
	server, _, _ := extensionsTestServer(t)
	response := putExtensions(t, server, `{"extensions":[{"name":"1002","max_contacts":2}]}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "1002") {
		t.Fatalf("the refusal does not name the account: %s", response.Body.String())
	}
}

// A refused account must not reach the store, or the page would show a rule
// that can never be applied and Apply would keep failing with no way back.
func TestAsteriskExtensionsDoesNotStoreARejectedAccount(t *testing.T) {
	server, _, _ := extensionsTestServer(t)
	if code := putExtensions(t, server,
		`{"extensions":[{"name":"vocat","password":"correct-horse-battery","max_contacts":2}]}`).Code; code != http.StatusBadRequest {
		t.Fatalf("the reserved name was accepted (status %d)", code)
	}
	if got := len(server.storedAsteriskExtensions(context.Background())); got != 0 {
		t.Fatalf("stored %d extensions after a rejected save", got)
	}
	if _, err := os.Stat(server.asteriskEndpointsPath()); !os.IsNotExist(err) {
		t.Fatal("a rejected save wrote the endpoints file")
	}
}

// Saving nothing over the file the container seeded from .env leaves the PBX
// with no account at all, and the symptom is a handset that stops registering
// an hour later. Confirming is the difference between a decision and an
// accident.
func TestAsteriskExtensionsGuardsTheSeededFile(t *testing.T) {
	server, _, _ := extensionsTestServer(t)
	if err := os.WriteFile(server.asteriskEndpointsPath(),
		[]byte("; Seeded by entrypoint.sh\n[1001]\ntype=endpoint\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !server.asteriskEndpointsAreSeeded() {
		t.Fatal("an entrypoint-written file was not recognised as seeded")
	}
	response := putExtensions(t, server, `{"extensions":[]}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", response.Code, response.Body.String())
	}
	if response := putExtensions(t, server, `{"extensions":[],"replace_seeded":true}`); response.Code != http.StatusOK {
		t.Fatalf("confirmed replacement was refused: %s", response.Body.String())
	}
	if server.asteriskEndpointsAreSeeded() {
		t.Fatal("the file is still reported as seeded after VoCat wrote it")
	}
}

// Adding accounts replaces the seeded file too, but that is a decision the
// operator has already made by typing one in -- it must not need confirming.
func TestAsteriskExtensionsReplacesTheSeededFileWhenAccountsAreGiven(t *testing.T) {
	server, _, _ := extensionsTestServer(t)
	if err := os.WriteFile(server.asteriskEndpointsPath(), []byte("; Seeded by entrypoint.sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := putExtensions(t, server,
		`{"extensions":[{"name":"1002","password":"correct-horse-battery","max_contacts":2}]}`).Code; code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if seeded, _ := getExtensions(t, server)["seeded"].(bool); seeded {
		t.Fatal("seeded is still set after VoCat wrote the file")
	}
}

// The preview answers a syntax question without shelling into the container,
// which does not require handing back the secret it was built from.
func TestAsteriskExtensionsPreviewHidesPasswords(t *testing.T) {
	server, _, _ := extensionsTestServer(t)
	putExtensions(t, server, `{"extensions":[{"name":"1001","password":"correct-horse-battery","max_contacts":2}]}`)
	data := getExtensions(t, server)
	preview, _ := data["preview"].(string)
	if preview == "" {
		t.Fatal("no preview was returned")
	}
	if strings.Contains(preview, "correct-horse-battery") {
		t.Fatalf("the preview carries the password:\n%s", preview)
	}
	if !strings.Contains(preview, "password=<hidden>") {
		t.Fatalf("the preview drops the password line entirely:\n%s", preview)
	}
	// has_password is what the browser gets instead.
	extensions, _ := data["extensions"].([]any)
	if len(extensions) != 1 {
		t.Fatalf("extensions = %+v", extensions)
	}
	first, _ := extensions[0].(map[string]any)
	if first["has_password"] != true {
		t.Fatalf("has_password missing: %+v", first)
	}
	if _, present := first["password"]; present {
		t.Fatalf("the password field is present in the listing: %+v", first)
	}
}

// The file holds SIP passwords in the clear. A world-readable one is a
// credential leak to anything else that can see the shared volume.
func TestAsteriskExtensionsFileIsNotWorldReadable(t *testing.T) {
	server, _, _ := extensionsTestServer(t)
	putExtensions(t, server, `{"extensions":[{"name":"1001","password":"correct-horse-battery","max_contacts":2}]}`)
	info, err := os.Stat(server.asteriskEndpointsPath())
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Fatalf("mode = %o, want no group or other access", mode)
	}
}

// Stored sensitively so the generic settings API, which lists every key,
// cannot hand the whole account list out with everything else.
func TestAsteriskExtensionsAreStoredSensitive(t *testing.T) {
	server, database, _ := extensionsTestServer(t)
	putExtensions(t, server, `{"extensions":[{"name":"1001","password":"correct-horse-battery","max_contacts":2}]}`)
	setting, err := database.AppSetting(context.Background(), asteriskExtensionsKey)
	if err != nil {
		t.Fatal(err)
	}
	if !setting.Sensitive {
		t.Fatal("the extension list is not marked sensitive")
	}
	if strings.Contains(string(setting.Redacted().Value), "correct-horse-battery") {
		t.Fatal("the redacted setting still carries the password")
	}
}
