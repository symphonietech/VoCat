package vowifi

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The shipped database must warn about nothing. It carries a top-level
// "metadata" block, so this also proves that block is modelled rather than
// merely tolerated -- if it were not, every deployment would warn on startup
// and the warning would be worthless.
func TestShippedCarrierDatabaseHasNoUnknownFields(t *testing.T) {
	if found := carrierProfileUnknownFields("builtin", carrierProfilesJSON); len(found) != 0 {
		t.Fatalf("the shipped database reports unknown keys:\n%s", strings.Join(found, "\n"))
	}
}

// The trap this exists for: an override written against a newer VoCat, or with
// a misspelled key. It must load, and it must say so.
func TestOverrideWithAnUnknownKeyLoadsAndWarns(t *testing.T) {
	dir := t.TempDir()
	// "identity" is the field upstream added for DITO. A VoCat without that
	// commit parses this file, applies no rewrite, and used to say nothing.
	profile := `{
	  "version": 1,
	  "profiles": [
	    {
	      "id": "local-dito-test",
	      "match": { "home_plmns": ["51566"] },
	      "identity": { "subscriber_imsi_rewrite": {
	        "from_prefix": "204047616", "to_prefix": "515661015" } },
	      "route": { "mcc": "515", "mnc": "66" },
	      "epdg": { "hostnam": "typo.example.com" }
	    }
	  ]
	}`
	if err := os.WriteFile(filepath.Join(dir, "local.json"), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = LoadCarrierProfileDirectory(t.TempDir()) })

	// Loading must succeed: a misspelled key is not worth a restart loop.
	if err := LoadCarrierProfileDirectory(dir); err != nil {
		t.Fatalf("an override with an unknown key refused to load: %v", err)
	}
	warnings := CarrierProfileWarnings()
	joined := strings.Join(warnings, "\n")
	// The misspelling is reported and names where it is.
	if !strings.Contains(joined, "profiles[0].epdg.hostnam") {
		t.Fatalf("the misspelled nested key was not reported:\n%s", joined)
	}
	if !strings.Contains(joined, "local.json") {
		t.Fatalf("the warning does not name the file:\n%s", joined)
	}
	// Keys the schema does know must not be reported.
	for _, known := range []string{"profiles[0].id", "profiles[0].match", "profiles[0].route"} {
		if strings.Contains(joined, known) {
			t.Fatalf("a known key %q was reported as unknown:\n%s", known, joined)
		}
	}
}

// A corrected file must stop warning, or the warning becomes background noise
// nobody reads.
func TestWarningsClearOnAGoodReload(t *testing.T) {
	dir := t.TempDir()
	good := `{"version":1,"profiles":[{"id":"local-good","match":{"home_plmns":["51502"]},
	  "epdg":{"hostname":"good.example.com"}}]}`
	if err := os.WriteFile(filepath.Join(dir, "good.json"), []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = LoadCarrierProfileDirectory(t.TempDir()) })
	if err := LoadCarrierProfileDirectory(dir); err != nil {
		t.Fatal(err)
	}
	if warnings := CarrierProfileWarnings(); len(warnings) != 0 {
		t.Fatalf("a clean override warned anyway:\n%s", strings.Join(warnings, "\n"))
	}
}

// The known-key set is derived from the structs, so a field added later needs
// no second edit here. This pins that: the walker must follow tags, not names
// it was told about.
func TestUnknownFieldWalkerFollowsStructTags(t *testing.T) {
	type inner struct {
		Wanted string `json:"wanted"`
	}
	type outer struct {
		Named   inner   `json:"named"`
		List    []inner `json:"list"`
		Ignored string  `json:"-"`
	}
	target := reflect.TypeOf(outer{})

	cases := []struct {
		name string
		body string
		want string
	}{
		{"nested", `{"named":{"nope":1}}`, "named.nope"},
		{"indexed", `{"list":[{"wanted":"x"},{"nope":1}]}`, "list[1].nope"},
		{"tag-excluded field is unknown", `{"Ignored":"x"}`, "Ignored"},
	}
	for _, test := range cases {
		got := unknownJSONFields([]byte(test.body), target, "")
		if len(got) != 1 || got[0] != test.want {
			t.Fatalf("%s: unknownJSONFields = %v, want [%s]", test.name, got, test.want)
		}
	}
	// A key the struct does model yields nothing.
	if got := unknownJSONFields([]byte(`{"named":{"wanted":"x"}}`), target, ""); len(got) != 0 {
		t.Fatalf("a modelled key was reported: %v", got)
	}
}
