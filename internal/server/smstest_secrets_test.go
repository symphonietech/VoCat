package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// A gateway that wants a password in a body parameter calls it one, and
// nothing else distinguishes it from the recipient number beside it.
func TestIsSecretParamKeyMatchesCredentialNames(t *testing.T) {
	for _, key := range []string{
		"password", "Password", "PASSWORD", "passwd", "pwd", "pass",
		"api_key", "API-Key", "apiKey", "secret", "app_secret",
		"token", "access_token", "Authorization",
	} {
		if !isSecretParamKey(key) {
			t.Errorf("%q was not treated as a credential", key)
		}
	}
	// Redacting a field that is not a credential costs the operator the
	// ability to read their own configuration back, so the list is narrow.
	for _, key := range []string{
		"to", "from", "content", "user", "username", "sender", "key", "sign",
		"signature", "mobile", "", "msg",
	} {
		if isSecretParamKey(key) {
			t.Errorf("%q was treated as a credential", key)
		}
	}
}

func pairValue(t *testing.T, raw, key string) (string, bool) {
	t.Helper()
	var pairs []smsTestPair
	if err := json.Unmarshal([]byte(raw), &pairs); err != nil {
		t.Fatalf("not valid JSON: %s", raw)
	}
	for _, pair := range pairs {
		if pair.Key == key {
			return pair.Value, pair.HasValue
		}
	}
	t.Fatalf("no %q in %s", key, raw)
	return "", false
}

// The value goes out blank with a marker, so the editor can say "set" without
// being told what it is.
func TestRedactSMSTestPairsHidesCredentialValues(t *testing.T) {
	raw := `[{"key":"user","value":"bob"},{"key":"password","value":"s3cret"},{"key":"to","value":"+1"}]`
	redacted := redactSMSTestPairs(raw)
	if strings.Contains(redacted, "s3cret") {
		t.Fatalf("the password survived: %s", redacted)
	}
	value, marked := pairValue(t, redacted, "password")
	if value != "" || !marked {
		t.Fatalf("password = %q, has_value %v", value, marked)
	}
	// Everything else is untouched: this is a page someone reads to check
	// their own configuration.
	if value, _ := pairValue(t, redacted, "user"); value != "bob" {
		t.Errorf("a non-credential value was redacted: %q", value)
	}
	if value, _ := pairValue(t, redacted, "to"); value != "+1" {
		t.Errorf("a non-credential value was redacted: %q", value)
	}
	// An empty credential is marked as absent rather than as stored.
	if _, marked := pairValue(t, redactSMSTestPairs(`[{"key":"password","value":""}]`), "password"); marked {
		t.Error("an unset password was marked as stored")
	}
}

// The browser never had the value, so it cannot send it back: without the
// merge, editing the recipient would wipe the password beside it.
func TestMergeSMSTestPairSecretsKeepsWhatIsStored(t *testing.T) {
	stored := `[{"key":"user","value":"bob"},{"key":"password","value":"s3cret"}]`
	incoming := `[{"key":"user","value":"alice"},{"key":"password","value":"","has_value":true}]`
	merged := mergeSMSTestPairSecrets(incoming, stored)
	if value, _ := pairValue(t, merged, "password"); value != "s3cret" {
		t.Fatalf("the stored password was lost: %s", merged)
	}
	if value, _ := pairValue(t, merged, "user"); value != "alice" {
		t.Fatalf("the edit was not applied: %s", merged)
	}
	// has_value is an output marker and must never be stored.
	if strings.Contains(merged, "has_value") {
		t.Fatalf("the output marker was stored: %s", merged)
	}
	// A new value replaces the old one, or the field would be write-once.
	changed := mergeSMSTestPairSecrets(`[{"key":"password","value":"new-one"}]`, stored)
	if value, _ := pairValue(t, changed, "password"); value != "new-one" {
		t.Fatalf("a new password was not saved: %s", changed)
	}
}

// Two parameters sharing a name must not take each other's secret.
func TestMergeSMSTestPairSecretsKeepsRepeatedNamesApart(t *testing.T) {
	stored := `[{"key":"token","value":"first"},{"key":"token","value":"second"}]`
	merged := mergeSMSTestPairSecrets(`[{"key":"token","value":""},{"key":"token","value":""}]`, stored)
	var pairs []smsTestPair
	if err := json.Unmarshal([]byte(merged), &pairs); err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 || pairs[0].Value != "first" || pairs[1].Value != "second" {
		t.Fatalf("repeated names were crossed: %s", merged)
	}
}

// This code redacts secrets; silently rewriting something it could not read
// would be a different job done badly.
func TestSMSTestPairsLeaveUnreadableValuesAlone(t *testing.T) {
	broken := `not json at all`
	if got := redactSMSTestPairs(broken); got != broken {
		t.Errorf("redact rewrote unreadable input: %q", got)
	}
	if got := mergeSMSTestPairSecrets(broken, `[]`); got != broken {
		t.Errorf("merge rewrote unreadable input: %q", got)
	}
	// An empty list stays an empty list rather than becoming "null".
	if got := redactSMSTestPairs(""); got != "" {
		t.Errorf("empty input became %q", got)
	}
	if got := mergeSMSTestPairSecrets("[]", "[]"); got != "[]" {
		t.Errorf("empty list became %q", got)
	}
}

// The endpoint editor prints the placeholder vocabulary under the body
// parameter list and tells the operator to write {{password}} there. Hiding
// that reference protects nothing -- the secret it names is the endpoint's
// own password field, which is never returned -- and it turns a working
// template into an empty password box the operator repairs by typing a
// literal secret over it.
func TestRedactSMSTestPairsKeepsPlaceholderReferences(t *testing.T) {
	raw := `[{"key":"password","value":"{{password}}"},{"key":"to","value":"{{to}}"}]`
	if got := redactSMSTestPairs(raw); got != raw {
		t.Fatalf("a placeholder was redacted:\n want %s\n got  %s", raw, got)
	}
	// Text beside a placeholder is exactly where a second, literal credential
	// would sit, so only a value that is nothing but substitutions is kept.
	for _, value := range []string{"s3cret", "{{username}}:s3cret", "Bearer {{password}}"} {
		pairs := `[{"key":"password","value":` + mustJSON(t, value) + `}]`
		if got := redactSMSTestPairs(pairs); strings.Contains(got, value) {
			t.Errorf("%q survived redaction: %s", value, got)
		}
	}
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// A placeholder is visible, so the browser sends it back and it round trips
// like any ordinary value rather than through the blank-keeps-stored path.
func TestMergeSMSTestPairSecretsRoundTripsPlaceholders(t *testing.T) {
	stored := `[{"key":"password","value":"{{password}}"},{"key":"to","value":"{{to}}"}]`
	if got := mergeSMSTestPairSecrets(stored, stored); got != stored {
		t.Fatalf("the placeholder did not survive a save:\n want %s\n got  %s", stored, got)
	}
}
