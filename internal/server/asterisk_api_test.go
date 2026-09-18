package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vocat/internal/ami"
)

// Pairing contacts to endpoints is where this can quietly go wrong: a
// mismatch shows every phone as unregistered while Asterisk is perfectly
// happy, which sends someone debugging a problem that does not exist.
func TestBuildAsteriskEndpointsPairsContactsByEndpointName(t *testing.T) {
	endpoints := []ami.Message{
		{"Event": "EndpointList", "ObjectName": "vocat", "DeviceState": "Not in use", "ActiveChannels": "0"},
		{"Event": "EndpointList", "ObjectName": "1001", "DeviceState": "Not in use", "ActiveChannels": "0"},
	}
	// Field names exactly as the Asterisk 22 ContactList event documents them.
	contacts := []ami.Message{
		{"Event": "ContactList", "Endpoint": "1001", "Uri": "sip:1001@192.168.31.110:51369",
			"Status": "Reachable", "RoundtripUsec": "21000",
			"UserAgent": "LinphoneiOS/6.2.2", "ViaAddr": "192.168.31.110",
			"ExpirationTime": "1789699918"},
	}
	result, _ := buildAsteriskEndpoints(endpoints, contacts)
	if len(result) != 2 {
		t.Fatalf("got %d endpoints", len(result))
	}
	// Sorted by name, so 1001 comes first.
	if result[0].Name != "1001" || !result[0].Registered {
		t.Fatalf("1001 = %+v; want registered", result[0])
	}
	contact := result[0].Contacts[0]
	if contact.RoundTripMS != 21 {
		t.Errorf("roundtrip = %v ms, want 21", contact.RoundTripMS)
	}
	// UserAgent and ViaAddr answer "which phone, and from where", which is
	// the question this page exists for.
	if contact.UserAgent != "LinphoneiOS/6.2.2" || contact.ViaAddress != "192.168.31.110" {
		t.Errorf("contact = %+v; want the documented UserAgent and ViaAddr", contact)
	}
	if contact.Expires != "1789699918" {
		t.Errorf("expires = %q", contact.Expires)
	}
	if result[1].Name != "vocat" || result[1].Registered {
		t.Fatalf("vocat = %+v; want no contacts", result[1])
	}
}

// Where a contact does not name its endpoint, the AOR is the only link. Both
// the bare and the "aor/uri" spellings appear in the wild.
func TestBuildAsteriskEndpointsFallsBackToTheAOR(t *testing.T) {
	endpoints := []ami.Message{{"ObjectName": "1001"}}
	for _, aorField := range []ami.Message{
		{"Event": "ContactList", "AOR": "1001", "Uri": "sip:a@b", "Status": "Reachable"},
		{"Event": "ContactList", "Aor": "1001/sip:1001@192.168.31.110", "Uri": "sip:a@b", "Status": "Reachable"},
		{"Event": "ContactList", "ObjectName": "1001/abcdef", "Uri": "sip:a@b", "Status": "Reachable"},
	} {
		result, _ := buildAsteriskEndpoints(endpoints, []ami.Message{aorField})
		if len(result) != 1 || len(result[0].Contacts) != 1 {
			t.Fatalf("contact %v was not paired: %+v", aorField, result)
		}
	}
}

// A field VoCat does not know must still reach the API, or a renamed key
// becomes invisible instead of merely unlabelled.
func TestBuildAsteriskEndpointsKeepsRawFields(t *testing.T) {
	result, _ := buildAsteriskEndpoints(
		[]ami.Message{{"ObjectName": "vocat", "SomethingNew": "42"}}, nil)
	found := ""
	for _, field := range result[0].Fields {
		if field.Name == "SomethingNew" {
			found = field.Value
		}
	}
	if found != "42" {
		t.Fatalf("raw fields dropped: %+v", result[0].Fields)
	}
}

// An unqualified contact reports no status at all. Calling that
// "not registered" would be wrong -- the phone is there, Asterisk just has
// not probed it.
func TestBuildAsteriskEndpointsTreatsUnknownStatusAsRegistered(t *testing.T) {
	result, _ := buildAsteriskEndpoints(
		[]ami.Message{{"ObjectName": "1001"}},
		[]ami.Message{{"Event": "ContactList", "EndpointName": "1001", "Uri": "sip:a@b"}})
	if !result[0].Registered {
		t.Fatal("a contact with no status was reported as not registered")
	}
	result, _ = buildAsteriskEndpoints(
		[]ami.Message{{"ObjectName": "1001"}},
		[]ami.Message{{"Event": "ContactList", "EndpointName": "1001", "Status": "Unreachable"}})
	if result[0].Registered {
		t.Fatal("an Unreachable contact was reported as registered")
	}
}

// A contact for an endpoint that is not in the listing must not panic or
// invent one.
func TestBuildAsteriskEndpointsIgnoresOrphanContacts(t *testing.T) {
	result, _ := buildAsteriskEndpoints(
		[]ami.Message{{"ObjectName": "1001"}},
		[]ami.Message{{"Event": "ContactList", "EndpointName": "ghost", "Uri": "sip:a@b"}})
	if len(result) != 1 || len(result[0].Contacts) != 0 {
		t.Fatalf("orphan contact was attached: %+v", result)
	}
}

// An endpoint whose AOR is not named after it still has to collect its
// contacts: the AOR is the only link when a contact does not name its
// endpoint, and EndpointList documents an Aor field for exactly this.
func TestBuildAsteriskEndpointsPairsByTheEndpointsOwnAOR(t *testing.T) {
	endpoints := []ami.Message{
		{"Event": "EndpointList", "ObjectName": "handset-a", "Aor": "desk-phone-7"},
	}
	contacts := []ami.Message{
		{"Event": "ContactList", "ObjectName": "desk-phone-7/sip:x@y", "Uri": "sip:x@y", "Status": "Reachable"},
	}
	result, _ := buildAsteriskEndpoints(endpoints, contacts)
	if len(result[0].Contacts) != 1 || !result[0].Registered {
		t.Fatalf("contact not paired through the endpoint AOR: %+v", result[0])
	}
	if result[0].AOR != "desk-phone-7" {
		t.Errorf("aor = %q", result[0].AOR)
	}
}

// The endpoint's own name must win over an AOR belonging to another
// endpoint, or contacts land on whichever was listed first.
func TestBuildAsteriskEndpointsPrefersTheEndpointName(t *testing.T) {
	endpoints := []ami.Message{
		{"Event": "EndpointList", "ObjectName": "aliased", "Aor": "1001"},
		{"Event": "EndpointList", "ObjectName": "1001", "Aor": "1001"},
	}
	contacts := []ami.Message{
		{"Event": "ContactList", "Endpoint": "1001", "Uri": "sip:x@y", "Status": "Reachable"},
	}
	result, _ := buildAsteriskEndpoints(endpoints, contacts)
	for _, endpoint := range result {
		if endpoint.Name == "1001" && len(endpoint.Contacts) != 1 {
			t.Fatalf("1001 did not get its own contact: %+v", endpoint)
		}
		if endpoint.Name == "aliased" && len(endpoint.Contacts) != 0 {
			t.Fatalf("contact landed on the wrong endpoint: %+v", endpoint)
		}
	}
}

// The page reports "not configured" purely from the address VoCat was given,
// so this is the one path that decides whether the whole feature appears to
// exist. Nothing else tested that the option actually reaches the handler.
func TestAsteriskStatusReportsConfiguredFromTheAddress(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		address    string
		configured bool
	}{
		{name: "unset", address: "", configured: false},
		{name: "whitespace only", address: "   ", configured: false},
		// Unreachable on purpose: "configured but down" must read as
		// configured, or a stopped PBX looks like a missing setting.
		{name: "set", address: "127.0.0.1:1", configured: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := &Server{
				logger:      regionTestLogger(),
				asteriskAMI: ami.Options{Address: testCase.address, Timeout: 200 * time.Millisecond},
			}
			recorder := httptest.NewRecorder()
			server.handleAsteriskStatus(recorder, httptest.NewRequest(http.MethodGet, "/api/asterisk/status", nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d", recorder.Code)
			}
			var body struct {
				Data struct {
					Configured bool   `json:"configured"`
					Error      string `json:"error"`
				} `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Data.Configured != testCase.configured {
				t.Fatalf("configured = %v, want %v (body %s)",
					body.Data.Configured, testCase.configured, recorder.Body.String())
			}
			if testCase.configured && body.Data.Error == "" {
				t.Fatal("an unreachable PBX reported no error; the page would look connected")
			}
		})
	}
}

// The trunk read as "Not registered" on a live system while its own
// EndpointList event listed a contact. A statically configured contact is not
// covered by a ContactList event that names a matching endpoint or AOR, so
// the endpoint's inline Contacts field is the only link.
func TestBuildAsteriskEndpointsShowsAStaticallyConfiguredContact(t *testing.T) {
	endpoints := []ami.Message{{
		"Event": "EndpointList", "ObjectName": "vocat", "Aor": "vocat",
		"DeviceState": "Not in use", "ActiveChannels": "0",
		"Contacts": "vocat/sip:vocat@127.0.0.1:5062,",
	}}
	result, _ := buildAsteriskEndpoints(endpoints, nil)
	if len(result) != 1 {
		t.Fatalf("got %d endpoints", len(result))
	}
	if len(result[0].Contacts) != 1 {
		t.Fatalf("the declared contact was dropped: %+v", result[0])
	}
	if result[0].Contacts[0].URI != "sip:vocat@127.0.0.1:5062" {
		t.Fatalf("contact uri = %q", result[0].Contacts[0].URI)
	}
	if !result[0].Registered {
		t.Fatal("an endpoint with a configured contact reported none")
	}
}

// When a ContactList event does cover the same URI, it must win -- it carries
// the status and round-trip the declared entry has no way to know.
func TestBuildAsteriskEndpointsDoesNotDuplicateADeclaredContact(t *testing.T) {
	endpoints := []ami.Message{{
		"Event": "EndpointList", "ObjectName": "vocat", "Aor": "vocat",
		"Contacts": "vocat/sip:vocat@127.0.0.1:5062,",
	}}
	// Names neither a matching endpoint nor AOR: only the URI links it.
	contacts := []ami.Message{{
		"Event": "ContactList", "ObjectName": "vocat;@1b2c3d",
		"Uri": "sip:vocat@127.0.0.1:5062", "Status": "Reachable", "RoundtripUsec": "900",
	}}
	result, _ := buildAsteriskEndpoints(endpoints, contacts)
	if len(result[0].Contacts) != 1 {
		t.Fatalf("contact duplicated: %+v", result[0].Contacts)
	}
	if result[0].Contacts[0].Status != "Reachable" || !result[0].Reachable {
		t.Fatalf("the ContactList status was lost: %+v", result[0])
	}
}

// Reachable is a stronger claim than Registered and must not be asserted from
// a contact that has never been qualified.
func TestBuildAsteriskEndpointsSeparatesReachableFromRegistered(t *testing.T) {
	result, _ := buildAsteriskEndpoints(
		[]ami.Message{{"ObjectName": "1001"}},
		[]ami.Message{{"Event": "ContactList", "Endpoint": "1001", "Uri": "sip:a@b"}})
	if !result[0].Registered {
		t.Fatal("a contact with no status should still count as registered")
	}
	if result[0].Reachable {
		t.Fatal("an unqualified contact was reported reachable")
	}
}

// A contact matching no endpoint used to vanish. It is the difference
// between "Asterisk never reported this contact" and "VoCat failed to pair
// it", which is not answerable once it is dropped.
func TestBuildAsteriskEndpointsReturnsUnpairedContacts(t *testing.T) {
	_, unpaired := buildAsteriskEndpoints(
		[]ami.Message{{"ObjectName": "1001"}},
		[]ami.Message{{"Event": "ContactList", "Endpoint": "ghost", "Uri": "sip:ghost@nowhere"}})
	if len(unpaired) != 1 || unpaired[0].URI != "sip:ghost@nowhere" {
		t.Fatalf("unpaired = %+v", unpaired)
	}
}

// The trunk's contact is configured, not registered, so PJSIPShowContacts
// never mentions it and its row read "Not qualified" for ever while the trunk
// was carrying calls. PJSIPShowEndpoint reports the same contact's qualify
// state as a ContactStatusDetail, which is the only source for it.
func TestMergeContactStatusResolvesAStaticContact(t *testing.T) {
	endpoint := asteriskEndpoint{
		Name:     "vocat",
		Contacts: []asteriskContact{{URI: "sip:vocat@127.0.0.1:5062"}},
	}
	// Field names exactly as the Asterisk 22 ContactStatusDetail event
	// documents them -- note URI, ViaAddress and RegExpire, which ContactList
	// spells Uri, ViaAddr and ExpirationTime.
	mergeContactStatus(&endpoint, []ami.Message{{
		"Event": "ContactStatusDetail", "AOR": "vocat",
		"URI": "sip:vocat@127.0.0.1:5062", "Status": "Reachable",
		"RoundtripUsec": "1500", "EndpointName": "vocat",
		"UserAgent": "VoCat", "ViaAddress": "127.0.0.1:5062",
		"RegExpire": "0",
	}})
	if len(endpoint.Contacts) != 1 {
		t.Fatalf("the detail was appended instead of merged: %+v", endpoint.Contacts)
	}
	contact := endpoint.Contacts[0]
	if contact.Status != "Reachable" {
		t.Fatalf("status = %q", contact.Status)
	}
	if contact.RoundTripMS != 1.5 {
		t.Errorf("roundtrip = %v ms, want 1.5", contact.RoundTripMS)
	}
	if contact.UserAgent != "VoCat" || contact.ViaAddress != "127.0.0.1:5062" {
		t.Errorf("contact = %+v; want the documented UserAgent and ViaAddress", contact)
	}
	if contact.Expires != "0" {
		t.Errorf("expires = %q; RegExpire was not read", contact.Expires)
	}
	if !endpoint.Reachable || !endpoint.Registered {
		t.Fatalf("endpoint = %+v; want reachable and registered", endpoint)
	}
}

// What the contact listing already said is the more specific answer, and a
// detail must not overwrite it -- the round-trip in particular is measured
// per contact and would otherwise flip between two sources on every poll.
func TestMergeContactStatusKeepsWhatTheListingReported(t *testing.T) {
	endpoint := asteriskEndpoint{
		Name: "1001",
		Contacts: []asteriskContact{{
			URI: "sip:a@b", Status: "Reachable", RoundTripMS: 21,
			UserAgent: "LinphoneiOS/6.2.2",
		}},
	}
	mergeContactStatus(&endpoint, []ami.Message{{
		"Event": "ContactStatusDetail", "URI": "sip:a@b",
		"Status": "Unreachable", "RoundtripUsec": "99000", "UserAgent": "other",
	}})
	contact := endpoint.Contacts[0]
	if contact.Status != "Reachable" || contact.RoundTripMS != 21 || contact.UserAgent != "LinphoneiOS/6.2.2" {
		t.Fatalf("the listing was overwritten: %+v", contact)
	}
}

// A contact Asterisk reports in the detail but that no listing covered is
// real. Dropping it would hide the very contact this query exists to find.
func TestMergeContactStatusAppendsAnUnknownContact(t *testing.T) {
	endpoint := asteriskEndpoint{Name: "vocat", Contacts: []asteriskContact{}}
	mergeContactStatus(&endpoint, []ami.Message{{
		"Event": "ContactStatusDetail", "URI": "sip:vocat@127.0.0.1:5062",
		"Status": "Reachable", "Something": "new",
	}})
	if len(endpoint.Contacts) != 1 || endpoint.Contacts[0].URI != "sip:vocat@127.0.0.1:5062" {
		t.Fatalf("contacts = %+v", endpoint.Contacts)
	}
	if !endpoint.Reachable {
		t.Fatal("an appended Reachable contact did not mark the endpoint reachable")
	}
	found := ""
	for _, field := range endpoint.Contacts[0].Fields {
		if field.Name == "Something" {
			found = field.Value
		}
	}
	if found != "new" {
		t.Fatalf("raw fields dropped: %+v", endpoint.Contacts[0].Fields)
	}
}

// PJSIPShowEndpoint answers with every kind of detail there is. Only the
// contact ones say anything about reachability, and an AorDetail carrying a
// Contact field must not be mistaken for one.
func TestMergeContactStatusIgnoresOtherDetailEvents(t *testing.T) {
	endpoint := asteriskEndpoint{Name: "vocat", Contacts: []asteriskContact{{URI: "sip:vocat@127.0.0.1:5062"}}}
	mergeContactStatus(&endpoint, []ami.Message{
		{"Event": "EndpointDetail", "ObjectName": "vocat", "DeviceState": "Not in use"},
		{"Event": "AorDetail", "ObjectName": "vocat", "Contacts": "sip:vocat@127.0.0.1:5062"},
		{"Event": "AuthDetail", "ObjectName": "vocat-auth", "Password": "secret"},
	})
	if endpoint.Contacts[0].Status != "" || endpoint.Reachable {
		t.Fatalf("a non-contact detail was read as a status: %+v", endpoint)
	}
}

// An Unreachable contact is exactly the state the page has to show, and
// calling it reachable would make the amber row this fixes lie the other way.
func TestMergeContactStatusReportsUnreachable(t *testing.T) {
	endpoint := asteriskEndpoint{Name: "vocat", Contacts: []asteriskContact{{URI: "sip:vocat@127.0.0.1:5062"}}}
	mergeContactStatus(&endpoint, []ami.Message{{
		"Event": "ContactStatusDetail", "URI": "sip:vocat@127.0.0.1:5062", "Status": "Unreachable",
	}})
	if endpoint.Reachable {
		t.Fatal("an Unreachable contact was reported reachable")
	}
	if endpoint.Contacts[0].Status != "Unreachable" {
		t.Fatalf("status = %q", endpoint.Contacts[0].Status)
	}
}

// The extra query costs a round trip per endpoint, so the usual case -- every
// contact dynamic, every status already known -- must not spend any.
func TestNeedsContactStatusOnlyWhenSomethingIsUnknown(t *testing.T) {
	known := asteriskEndpoint{Contacts: []asteriskContact{{URI: "sip:a@b", Status: "Reachable"}}}
	if needsContactStatus(known) {
		t.Error("an endpoint with a known status would be queried again")
	}
	unknown := asteriskEndpoint{Contacts: []asteriskContact{
		{URI: "sip:a@b", Status: "Reachable"},
		{URI: "sip:c@d", Status: "  "},
	}}
	if !needsContactStatus(unknown) {
		t.Error("an endpoint with an unknown status would never be resolved")
	}
	// No contacts at all means nothing to qualify, not a missing status.
	if needsContactStatus(asteriskEndpoint{Contacts: []asteriskContact{}}) {
		t.Error("an endpoint with no contacts would be queried for nothing")
	}
}
