package smstest

import (
	"encoding/json"
	"strings"
	"testing"

	"vocat/internal/store"
)

// A gateway that wants a password in a body parameter calls it one, and
// nothing else distinguishes it from the recipient number beside it.
func TestIsSecretParamKeyMatchesCredentialNames(t *testing.T) {
	for _, key := range []string{
		"password", "Password", "PASSWORD", "passwd", "pwd", "pass",
		"api_key", "API-Key", "apiKey", "secret", "app_secret",
		"token", "access_token", "Authorization",
	} {
		if !IsSecretParamKey(key) {
			t.Errorf("%q was not treated as a credential", key)
		}
	}
	// Redacting a field that is not a credential costs the operator the
	// ability to read their own configuration back, so the list is narrow.
	for _, key := range []string{
		"to", "from", "content", "user", "username", "sender", "key", "sign",
		"signature", "mobile", "", "msg",
	} {
		if IsSecretParamKey(key) {
			t.Errorf("%q was treated as a credential", key)
		}
	}
}

func pairValue(t *testing.T, raw, key string) (string, bool) {
	t.Helper()
	var pairs []Pair
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
func TestRedactPairsHidesCredentialValues(t *testing.T) {
	raw := `[{"key":"user","value":"bob"},{"key":"password","value":"s3cret"},{"key":"to","value":"+1"}]`
	redacted := RedactPairs(raw)
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
	if _, marked := pairValue(t, RedactPairs(`[{"key":"password","value":""}]`), "password"); marked {
		t.Error("an unset password was marked as stored")
	}
}

// The browser never had the value, so it cannot send it back: without the
// merge, editing the recipient would wipe the password beside it.
func TestMergePairSecretsKeepsWhatIsStored(t *testing.T) {
	stored := `[{"key":"user","value":"bob"},{"key":"password","value":"s3cret"}]`
	incoming := `[{"key":"user","value":"alice"},{"key":"password","value":"","has_value":true}]`
	merged := MergePairSecrets(incoming, stored)
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
	changed := MergePairSecrets(`[{"key":"password","value":"new-one"}]`, stored)
	if value, _ := pairValue(t, changed, "password"); value != "new-one" {
		t.Fatalf("a new password was not saved: %s", changed)
	}
}

// Two parameters sharing a name must not take each other's secret.
func TestMergePairSecretsKeepsRepeatedNamesApart(t *testing.T) {
	stored := `[{"key":"token","value":"first"},{"key":"token","value":"second"}]`
	merged := MergePairSecrets(`[{"key":"token","value":""},{"key":"token","value":""}]`, stored)
	var pairs []Pair
	if err := json.Unmarshal([]byte(merged), &pairs); err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 || pairs[0].Value != "first" || pairs[1].Value != "second" {
		t.Fatalf("repeated names were crossed: %s", merged)
	}
}

// This code redacts secrets; silently rewriting something it could not read
// would be a different job done badly.
func TestPairsLeaveUnreadableValuesAlone(t *testing.T) {
	broken := `not json at all`
	if got := RedactPairs(broken); got != broken {
		t.Errorf("redact rewrote unreadable input: %q", got)
	}
	if got := MergePairSecrets(broken, `[]`); got != broken {
		t.Errorf("merge rewrote unreadable input: %q", got)
	}
	// An empty list stays an empty list rather than becoming "null".
	if got := RedactPairs(""); got != "" {
		t.Errorf("empty input became %q", got)
	}
	if got := MergePairSecrets("[]", "[]"); got != "[]" {
		t.Errorf("empty list became %q", got)
	}
}

// Most gateways do not use the password field at all -- they take the
// credential as a body parameter or a header -- so redacting only that field
// left the one that mattered in the stored response.
func TestSecretValuesCoversEveryCredentialAnEndpointHolds(t *testing.T) {
	secrets := SecretValues(store.SMSTestEndpoint{
		Password:   "field-password",
		Headers:    `[{"key":"Authorization","value":"Bearer header-token"},{"key":"Accept","value":"application/json"}]`,
		BodyParams: `[{"key":"to","value":"+1"},{"key":"password","value":"body-password"},{"key":"api_key","value":"body-key"}]`,
	})
	for _, want := range []string{"field-password", "Bearer header-token", "body-password", "body-key"} {
		found := false
		for _, secret := range secrets {
			if secret == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%q is not treated as a secret: %v", want, secrets)
		}
	}
	// Not everything: redacting the recipient number out of a gateway's reply
	// would make the record useless.
	for _, unwanted := range []string{"+1", "application/json"} {
		for _, secret := range secrets {
			if secret == unwanted {
				t.Errorf("%q was treated as a secret", unwanted)
			}
		}
	}
	// An endpoint with nothing set has nothing to redact.
	if got := SecretValues(store.SMSTestEndpoint{}); len(got) != 0 {
		t.Errorf("an empty endpoint produced secrets: %v", got)
	}
}
