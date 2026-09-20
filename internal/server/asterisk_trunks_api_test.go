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
	`"transport":"udp","match":["203.0.113.10"],"username":"acme","password":"a-long-enough-secret",` +
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
		`"match":["203.0.113.10"],"username":"acme",` +
		`"destinations":["_1NXXNXXXXXX","_44X."],"devices":["slot1"],` +
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

// A peer with no address to match on could never be identified at all, so it
// is refused rather than stored as a trunk that can never carry a call.
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

// Two credentials, kept independently: rotating one must not wipe the other
// for being blank, and neither may come back out.
func TestTrunkOutboundPasswordIsWriteOnlyAndIndependent(t *testing.T) {
	server, database, _ := trunksTestServer(t)

	both := `{"trunks":[{"name":"acme","host":"203.0.113.10","port":5060,"transport":"udp",` +
		`"match":["203.0.113.10"],"username":"acme","password":"inbound-long-secret",` +
		`"outbound_username":"acme-out","outbound_password":"outbound-long-secret",` +
		`"destinations":["_1NXXNXXXXXX"],"devices":["slot1"],` +
		`"max_concurrent":4,"timeout_seconds":60}]}`
	created := putTrunks(t, server, both)
	if created.Code != http.StatusOK {
		t.Fatalf("save status = %d, body = %s", created.Code, created.Body.String())
	}
	for _, secret := range []string{"inbound-long-secret", "outbound-long-secret"} {
		if strings.Contains(created.Body.String(), secret) {
			t.Fatalf("%s came back out: %s", secret, created.Body.String())
		}
	}
	if !strings.Contains(created.Body.String(), "has_outbound_password") {
		t.Errorf("the editor cannot tell an outbound password is stored: %s", created.Body.String())
	}

	// Rotate only the outbound credential. The inbound one is blank in this
	// request because the browser never had it.
	rotated := `{"trunks":[{"name":"acme","host":"203.0.113.10","port":5060,"transport":"udp",` +
		`"match":["203.0.113.10"],"username":"acme","outbound_username":"acme-out","outbound_password":"a-new-long-secret",` +
		`"destinations":["_1NXXNXXXXXX"],"devices":["slot1"],` +
		`"max_concurrent":4,"timeout_seconds":60}]}`
	if response := putTrunks(t, server, rotated); response.Code != http.StatusOK {
		t.Fatalf("rotate status = %d, body = %s", response.Code, response.Body.String())
	}
	stored := storedTrunks(t, database)
	if len(stored) != 1 {
		t.Fatalf("stored = %+v", stored)
	}
	if stored[0].Password != "inbound-long-secret" {
		t.Errorf("rotating the outbound credential wiped the inbound one: %q", stored[0].Password)
	}
	if stored[0].OutboundPassword != "a-new-long-secret" {
		t.Errorf("the outbound credential was not rotated: %q", stored[0].OutboundPassword)
	}
	if stored[0].HasPassword || stored[0].HasOutboundPassword {
		t.Errorf("an output marker was stored: %+v", stored[0])
	}

	// The preview must redact both, not only the first.
	response := httptest.NewRecorder()
	server.handleGetAsteriskTrunks(response, httptest.NewRequest(http.MethodGet, "/api/asterisk/trunks", nil))
	for _, secret := range []string{"inbound-long-secret", "a-new-long-secret"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Errorf("%s is readable through the API: %s", secret, response.Body.String())
		}
	}
}

// Forwarding to a trunk that does not exist renders a dial to an endpoint
// Asterisk has never heard of, so it is refused at save rather than at call
// time, where the only signal is a caller hearing nothing.
func TestInboundForwardRequiresAnExistingTrunk(t *testing.T) {
	server, _, _ := trunksTestServer(t)

	missing := `{"mode":"forward","forward_trunk":"nope","forward_number":"2001","ring_seconds":60}`
	request := httptest.NewRequest(http.MethodPut, "/api/asterisk/inbound", strings.NewReader(missing))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handlePutAsteriskInbound(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "not configured") {
		t.Errorf("the error does not say the trunk is missing: %s", response.Body.String())
	}

	// With the trunk in place it saves, and forwards without needing a single
	// extension configured.
	if created := putTrunks(t, server, trunkWithSecret); created.Code != http.StatusOK {
		t.Fatal(created.Body.String())
	}
	good := `{"mode":"forward","forward_trunk":"acme","forward_number":"2001","ring_seconds":60}`
	request = httptest.NewRequest(http.MethodPut, "/api/asterisk/inbound", strings.NewReader(good))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.handlePutAsteriskInbound(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "PJSIP/2001@trunk-acme") {
		t.Fatalf("the preview does not forward to the trunk: %s", response.Body.String())
	}
}

// A forward plan whose trunk is later deleted must not keep generating a dial
// to an endpoint that is gone; the reader falls back the same way it does for
// a deleted extension.
func TestStoredForwardPlanFallsBackWhenTheTrunkGoes(t *testing.T) {
	server, _, _ := trunksTestServer(t)
	ctx := context.Background()

	if created := putTrunks(t, server, trunkWithSecret); created.Code != http.StatusOK {
		t.Fatal(created.Body.String())
	}
	good := `{"mode":"forward","forward_trunk":"acme","forward_number":"2001","ring_seconds":60}`
	request := httptest.NewRequest(http.MethodPut, "/api/asterisk/inbound", strings.NewReader(good))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handlePutAsteriskInbound(response, request)
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	if plan := server.asteriskInboundPlan(ctx, nil); plan.Mode != "forward" {
		t.Fatalf("the saved plan did not survive a read: %+v", plan)
	}

	// Delete every trunk, as the trunk editor allows.
	if emptied := putTrunks(t, server, `{"trunks":[]}`); emptied.Code != http.StatusOK {
		t.Fatal(emptied.Body.String())
	}
	plan := server.asteriskInboundPlan(ctx, nil)
	if plan.Mode == "forward" {
		t.Fatalf("a forward plan survived the deletion of its trunk: %+v", plan)
	}
}

// A credential has to be removable. Clearing the username is what removes it:
// without that there is no way back from a stored password, and a trunk left
// holding one with no username fails validation on every subsequent save.
func TestTrunkCredentialCanBeCleared(t *testing.T) {
	server, database, _ := trunksTestServer(t)
	if r := putTrunks(t, server, trunkWithSecret); r.Code != http.StatusOK {
		t.Fatal(r.Body.String())
	}
	cleared := `{"trunks":[{"name":"acme","host":"203.0.113.10","port":5060,"transport":"udp",` +
		`"match":["203.0.113.10"],"destinations":["_1NXXNXXXXXX"],"devices":["slot1"],` +
		`"max_concurrent":4,"timeout_seconds":60}]}`
	if r := putTrunks(t, server, cleared); r.Code != http.StatusOK {
		t.Fatalf("clearing the username was refused: %s", r.Body.String())
	}
	stored := storedTrunks(t, database)
	if len(stored) != 1 || stored[0].Password != "" || stored[0].Username != "" {
		t.Fatalf("the credential survived being cleared: %+v", stored)
	}
}

// Every other numeric field is defaulted, so a body omitting the timeout must
// not be the one that is refused.
func TestTrunkSaveDefaultsTheTimeout(t *testing.T) {
	server, database, _ := trunksTestServer(t)
	body := `{"trunks":[{"name":"acme","host":"203.0.113.10","match":["203.0.113.10"],` +
		`"destinations":["_1NXXNXXXXXX"],"devices":["slot1"]}]}`
	if r := putTrunks(t, server, body); r.Code != http.StatusOK {
		t.Fatalf("a body with only the required fields was refused: %s", r.Body.String())
	}
	stored := storedTrunks(t, database)
	if len(stored) != 1 {
		t.Fatalf("stored = %+v", stored)
	}
	if stored[0].TimeoutSeconds == 0 || stored[0].Port == 0 ||
		stored[0].MaxConcurrent == 0 || stored[0].Transport == "" {
		t.Fatalf("a field was left unset: %+v", stored[0])
	}
}

// The trunk is matched case-insensitively but the dial string is rendered
// verbatim, so it is stored in the trunk list's own spelling.
func TestInboundForwardNormalisesTheTrunkName(t *testing.T) {
	server, _, _ := trunksTestServer(t)
	if r := putTrunks(t, server, trunkWithSecret); r.Code != http.StatusOK {
		t.Fatal(r.Body.String())
	}
	body := `{"mode":"forward","forward_trunk":"ACME","forward_number":"2001","ring_seconds":60}`
	request := httptest.NewRequest(http.MethodPut, "/api/asterisk/inbound", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handlePutAsteriskInbound(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "trunk-ACME") {
		t.Fatalf("the dial string names a section that does not exist: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "PJSIP/2001@trunk-acme") {
		t.Fatalf("the trunk name was not normalised: %s", response.Body.String())
	}
}

// Deleting a trunk a forward plan points at must not leave inbound.conf
// dialling an endpoint that no longer exists: the reader falls back, but the
// file on disk does not rewrite itself.
func TestDeletingAForwardedTrunkRewritesInbound(t *testing.T) {
	server, _, _ := trunksTestServer(t)

	if r := putTrunks(t, server, trunkWithSecret); r.Code != http.StatusOK {
		t.Fatal(r.Body.String())
	}
	body := `{"mode":"forward","forward_trunk":"acme","forward_number":"2001","ring_seconds":60}`
	request := httptest.NewRequest(http.MethodPut, "/api/asterisk/inbound", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handlePutAsteriskInbound(response, request)
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	onDisk, err := os.ReadFile(server.asteriskInboundPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(onDisk), "trunk-acme") {
		t.Fatalf("the forward was never written:\n%s", onDisk)
	}

	if r := putTrunks(t, server, `{"trunks":[]}`); r.Code != http.StatusOK {
		t.Fatal(r.Body.String())
	}
	onDisk, err = os.ReadFile(server.asteriskInboundPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(onDisk), "trunk-acme") {
		t.Fatalf("inbound.conf still dials a trunk that was deleted:\n%s", onDisk)
	}
}
