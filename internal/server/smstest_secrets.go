package server

import (
	"encoding/json"
	"strings"
)

// smsTestPair is one header or body parameter of an SMS gateway endpoint.
//
// HasValue is set only on the way out, in place of a secret's value: the
// editor needs to know a secret is stored without being told what it is.
type smsTestPair struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	HasValue bool   `json:"has_value,omitempty"`
}

// secretParamKeys are the parameter names whose value is treated as a
// credential: never returned, and kept when saved blank.
//
// By name, because that is all there is to go on -- a gateway that wants a
// password in a body parameter calls it one, and nothing else distinguishes
// it from the recipient number beside it. Matched with punctuation and case
// removed, so "API-Key", "api_key" and "apikey" are one entry.
//
// Deliberately not "key" or "sign" on their own: one is too common a name for
// something harmless, the other is usually a value computed from a secret
// rather than the secret. Redacting a field that is not a credential costs
// the operator the ability to read their own configuration back.
var secretParamKeys = map[string]bool{
	"password": true, "passwd": true, "pwd": true, "pass": true,
	"secret": true, "apisecret": true, "appsecret": true, "clientsecret": true,
	"token": true, "apitoken": true, "accesstoken": true, "authtoken": true,
	"apikey": true, "appkey": true, "accesskey": true, "secretkey": true,
	"auth": true, "authorization": true, "credential": true,
}

// isSecretParamKey reports whether a parameter name holds a credential.
func isSecretParamKey(key string) bool {
	var builder strings.Builder
	for _, value := range strings.ToLower(strings.TrimSpace(key)) {
		if (value >= 'a' && value <= 'z') || (value >= '0' && value <= '9') {
			builder.WriteRune(value)
		}
	}
	return secretParamKeys[builder.String()]
}

// decodeSMSTestPairs parses a stored header or body parameter list. Invalid
// JSON returns ok false, and callers then leave the value exactly as it was:
// this code redacts secrets, and silently rewriting something it could not
// read would be a different job done badly.
func decodeSMSTestPairs(raw string) ([]smsTestPair, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil, true
	}
	var pairs []smsTestPair
	if err := json.Unmarshal([]byte(raw), &pairs); err != nil {
		return nil, false
	}
	return pairs, true
}

func encodeSMSTestPairs(pairs []smsTestPair) string {
	if len(pairs) == 0 {
		return "[]"
	}
	encoded, err := json.Marshal(pairs)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

// redactSMSTestPairs blanks the value of every credential parameter and marks
// that one is stored. This is what the API returns.
func redactSMSTestPairs(raw string) string {
	pairs, ok := decodeSMSTestPairs(raw)
	if !ok || len(pairs) == 0 {
		// Nothing to redact, so the value is handed back byte for byte:
		// turning "" into "[]" would be this function editing configuration
		// it was not asked to touch.
		return raw
	}
	for index := range pairs {
		if !isSecretParamKey(pairs[index].Key) {
			continue
		}
		pairs[index].HasValue = pairs[index].Value != ""
		pairs[index].Value = ""
	}
	return encodeSMSTestPairs(pairs)
}

// mergeSMSTestPairSecrets fills a blank credential value from what is stored,
// which is what makes a write-only field editable: the browser never had the
// value, so it cannot send it back, and every edit to a neighbouring
// parameter would otherwise wipe it.
//
// Matched by name and by how many times that name has appeared, so a list
// with two parameters of the same name keeps them apart.
func mergeSMSTestPairSecrets(incoming, stored string) string {
	pairs, ok := decodeSMSTestPairs(incoming)
	if !ok || len(pairs) == 0 {
		return incoming
	}
	previous, ok := decodeSMSTestPairs(stored)
	if !ok {
		previous = nil
	}
	seen := map[string]int{}
	for index := range pairs {
		// has_value is an output marker; it must never be stored.
		pairs[index].HasValue = false
		if !isSecretParamKey(pairs[index].Key) {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(pairs[index].Key))
		occurrence := seen[name]
		seen[name] = occurrence + 1
		if pairs[index].Value != "" {
			continue
		}
		if value, found := nthPairValue(previous, name, occurrence); found {
			pairs[index].Value = value
		}
	}
	return encodeSMSTestPairs(pairs)
}

// nthPairValue returns the value of the occurrence-th parameter with this
// name, so two parameters sharing a name do not take each other's secret.
func nthPairValue(pairs []smsTestPair, name string, occurrence int) (string, bool) {
	count := 0
	for _, pair := range pairs {
		if strings.ToLower(strings.TrimSpace(pair.Key)) != name {
			continue
		}
		if count == occurrence {
			return pair.Value, true
		}
		count++
	}
	return "", false
}
