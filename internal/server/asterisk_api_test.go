package server

import (
	"testing"

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
	contacts := []ami.Message{
		{"Event": "ContactList", "EndpointName": "1001", "Uri": "sip:1001@192.168.31.110:51369",
			"Status": "Reachable", "RoundtripUsec": "21000"},
	}
	result := buildAsteriskEndpoints(endpoints, contacts)
	if len(result) != 2 {
		t.Fatalf("got %d endpoints", len(result))
	}
	// Sorted by name, so 1001 comes first.
	if result[0].Name != "1001" || !result[0].Registered {
		t.Fatalf("1001 = %+v; want registered", result[0])
	}
	if got := result[0].Contacts[0].RoundTripMS; got != 21 {
		t.Errorf("roundtrip = %v ms, want 21", got)
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
		result := buildAsteriskEndpoints(endpoints, []ami.Message{aorField})
		if len(result) != 1 || len(result[0].Contacts) != 1 {
			t.Fatalf("contact %v was not paired: %+v", aorField, result)
		}
	}
}

// A field VoCat does not know must still reach the API, or a renamed key
// becomes invisible instead of merely unlabelled.
func TestBuildAsteriskEndpointsKeepsRawFields(t *testing.T) {
	result := buildAsteriskEndpoints(
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
	result := buildAsteriskEndpoints(
		[]ami.Message{{"ObjectName": "1001"}},
		[]ami.Message{{"Event": "ContactList", "EndpointName": "1001", "Uri": "sip:a@b"}})
	if !result[0].Registered {
		t.Fatal("a contact with no status was reported as not registered")
	}
	result = buildAsteriskEndpoints(
		[]ami.Message{{"ObjectName": "1001"}},
		[]ami.Message{{"Event": "ContactList", "EndpointName": "1001", "Status": "Unreachable"}})
	if result[0].Registered {
		t.Fatal("an Unreachable contact was reported as registered")
	}
}

// A contact for an endpoint that is not in the listing must not panic or
// invent one.
func TestBuildAsteriskEndpointsIgnoresOrphanContacts(t *testing.T) {
	result := buildAsteriskEndpoints(
		[]ami.Message{{"ObjectName": "1001"}},
		[]ami.Message{{"Event": "ContactList", "EndpointName": "ghost", "Uri": "sip:a@b"}})
	if len(result) != 1 || len(result[0].Contacts) != 0 {
		t.Fatalf("orphan contact was attached: %+v", result)
	}
}
