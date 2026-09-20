package asteriskconf

import (
	"strings"
	"testing"
)

func sampleTrunk() Trunk {
	return Trunk{
		Name:           "acme",
		Host:           "203.0.113.10",
		Port:           5060,
		Transport:      "udp",
		Match:          []string{"203.0.113.10"},
		Destinations:   []string{"_1NXXNXXXXXX"},
		Devices:        []string{"SLOT1-1", "SLOT2-1"},
		MaxConcurrent:  4,
		TimeoutSeconds: 60,
	}
}

// A peer that can dial through a SIM spends real money, so an endpoint that
// would accept an INVITE from anywhere reaching the port is refused.
func TestTrunkRequiresAnIdentityCheck(t *testing.T) {
	trunk := sampleTrunk()
	trunk.Match = nil
	err := trunk.Validate()
	if err == nil {
		t.Fatal("a trunk with neither a match address nor credentials was accepted")
	}
	if !strings.Contains(err.Error(), "credentials") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
	// Either one alone is enough.
	trunk.Username = "acme"
	trunk.Password = "a-long-enough-secret"
	if err := trunk.Validate(); err != nil {
		t.Errorf("credentials alone were refused: %v", err)
	}
}

// Which cards an outside party may spend is not something to leave implicit,
// so unlike an outbound route the SIM list is required.
func TestTrunkRequiresAtLeastOneSIM(t *testing.T) {
	trunk := sampleTrunk()
	trunk.Devices = nil
	if err := trunk.Validate(); err == nil {
		t.Fatal("a trunk with no SIMs was accepted")
	}
}

// A newly created trunk has no destinations and must render rather than
// error: refusing would make "deny everything" impossible to save.
func TestRenderTrunkRoutesAllowsNoDestinations(t *testing.T) {
	trunk := sampleTrunk()
	trunk.Destinations = nil
	rendered, err := RenderTrunkRoutes([]Trunk{trunk})
	if err != nil {
		t.Fatalf("RenderTrunkRoutes() error = %v", err)
	}
	if !strings.Contains(rendered, "[from-trunk-acme]") {
		t.Fatalf("the context is missing:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Hangup(1)") {
		t.Errorf("a trunk with no destinations must hang up every call:\n%s", rendered)
	}
	if strings.Contains(rendered, "Dial(") {
		t.Errorf("a trunk with no destinations dialled something:\n%s", rendered)
	}
}

// A comma inside an extensions.conf application call is an argument
// separator, which has already broken this deployment's dial plan once.
func TestRenderTrunkRoutesSeparatesSIMsWithSpaces(t *testing.T) {
	rendered, err := RenderTrunkRoutes([]Trunk{sampleTrunk()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "Set(__VOCATDEV=SLOT1-1 SLOT2-1)") {
		t.Fatalf("SIMs are not space separated:\n%s", rendered)
	}
	if strings.Contains(rendered, "SLOT1-1,SLOT2-1") {
		t.Fatalf("SIMs were joined with a comma:\n%s", rendered)
	}
}

// The group is set before the count is read, so this call is in it and
// "> max" admits exactly max at a time.
func TestRenderTrunkRoutesCapsConcurrency(t *testing.T) {
	rendered, err := RenderTrunkRoutes([]Trunk{sampleTrunk()})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Set(GROUP(trunk)=acme)",
		"GotoIf($[${GROUP_COUNT(acme@trunk)} > 4]?full)",
		"same => n(full),Congestion()",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q:\n%s", want, rendered)
		}
	}
}

// Asterisk walks a context's includes only when the context itself matches
// nothing, so a catch-all beside an include makes the include dead config.
func TestRenderTrunkRoutesOmitsTheCatchAllWhenSharingRoutes(t *testing.T) {
	trunk := sampleTrunk()
	trunk.ShareRoutes = true
	rendered, err := RenderTrunkRoutes([]Trunk{trunk})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "include => "+RoutesContext) {
		t.Fatalf("the shared routes were not included:\n%s", rendered)
	}
	if strings.Contains(rendered, "exten => _.,") {
		t.Fatalf("a catch-all was emitted beside the include, which would shadow it:\n%s", rendered)
	}
	// Without sharing, the catch-all is what denies everything else.
	plain, err := RenderTrunkRoutes([]Trunk{sampleTrunk()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain, "exten => _.,") {
		t.Fatalf("the catch-all is missing without sharing:\n%s", plain)
	}
}

// A trunk named like a softphone account must not collide with it: PJSIP
// refuses a duplicate object and can take the rest of the file with it.
func TestTrunkObjectsAreNamespaced(t *testing.T) {
	trunk := sampleTrunk()
	trunk.Name = "1001"
	rendered, err := RenderTrunks([]Trunk{trunk})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "[trunk-1001]") {
		t.Fatalf("the object is not namespaced:\n%s", rendered)
	}
	if strings.Contains(rendered, "\n[1001]\n") {
		t.Fatalf("the object collides with a softphone account:\n%s", rendered)
	}
	if !strings.Contains(rendered, "context=from-trunk-1001") {
		t.Fatalf("the endpoint does not point at its own context:\n%s", rendered)
	}
}

func TestRenderTrunksEmitsIdentifyAndAuth(t *testing.T) {
	trunk := sampleTrunk()
	trunk.Username = "acme"
	trunk.Password = "a-long-enough-secret"
	trunk.Match = []string{"203.0.113.10", "198.51.100.0/24"}
	rendered, err := RenderTrunks([]Trunk{trunk})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"type=endpoint", "type=aor", "type=auth", "type=identify",
		"contact=sip:203.0.113.10:5060",
		"match=203.0.113.10", "match=198.51.100.0/24",
		"auth=trunk-acme", "transport=transport-udp",
		// VoCat bridges the audio itself, so Asterisk stays in the path.
		"direct_media=no",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q:\n%s", want, rendered)
		}
	}
	// Identify alone emits no auth section rather than an empty one.
	plain, err := RenderTrunks([]Trunk{sampleTrunk()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "type=auth") {
		t.Errorf("an auth section was emitted with no credentials:\n%s", plain)
	}
}

// Everything reaching the renderer has been through Validate, because an
// Asterisk dial plan can invoke System().
func TestTrunkRefusesInjection(t *testing.T) {
	cases := map[string]func(*Trunk){
		"name":        func(t *Trunk) { t.Name = "acme;exten => _.,1,System(id)" },
		"host":        func(t *Trunk) { t.Host = "203.0.113.10\nmatch=0.0.0.0/0" },
		"match":       func(t *Trunk) { t.Match = []string{"0.0.0.0/0\npermit=all"} },
		"destination": func(t *Trunk) { t.Destinations = []string{"_X.,1,System(id)"} },
		"device":      func(t *Trunk) { t.Devices = []string{"SLOT1-1,SLOT2-1"} },
		"comment":     func(t *Trunk) { t.Comment = "hi;\nexten => _.,1,System(id)" },
		"password": func(t *Trunk) {
			t.Username = "acme"
			t.Password = "secret;exten => _.,1,System(id)"
		},
	}
	for field, mutate := range cases {
		trunk := sampleTrunk()
		mutate(&trunk)
		if err := trunk.Validate(); err == nil {
			t.Errorf("%s accepted a value that would escape its field", field)
		}
	}
}

func TestTrunkNamesMustBeUnique(t *testing.T) {
	first := sampleTrunk()
	second := sampleTrunk()
	second.Name = "ACME"
	if _, err := RenderTrunks([]Trunk{first, second}); err == nil {
		t.Fatal("two trunks differing only in case were accepted")
	}
}

func TestRenderTrunksHandlesAnEmptyList(t *testing.T) {
	for name, render := range map[string]func([]Trunk) (string, error){
		"objects": RenderTrunks,
		"routes":  RenderTrunkRoutes,
	} {
		rendered, err := render(nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.HasPrefix(rendered, GeneratedHeader) {
			t.Errorf("%s: the generated header is missing:\n%s", name, rendered)
		}
	}
}

// auth= only authenticates what arrives. A call forwarded out to a provider
// that challenges dies at 401 without outbound_auth, and nothing in the
// configuration points at why.
func TestRenderTrunksEmitsOutboundAuth(t *testing.T) {
	trunk := sampleTrunk()
	trunk.OutboundUsername = "acme-out"
	trunk.OutboundPassword = "another-long-secret"
	rendered, err := RenderTrunks([]Trunk{trunk})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"outbound_auth=trunkout-acme",
		"[trunkout-acme]",
		"username=acme-out",
		"password=another-long-secret",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q:\n%s", want, rendered)
		}
	}
	// Identified by address with no outbound credential: an unauthenticated
	// peer stays unauthenticated rather than gaining an empty auth object.
	plain, err := RenderTrunks([]Trunk{sampleTrunk()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "outbound_auth") {
		t.Errorf("outbound_auth was emitted with no credential:\n%s", plain)
	}
}

// The two credentials are independent: a peer identified by address may still
// have to authenticate when this side forwards a call out to it.
func TestTrunkOutboundCredentialIsIndependent(t *testing.T) {
	trunk := sampleTrunk()
	trunk.OutboundUsername = "acme-out"
	trunk.OutboundPassword = "another-long-secret"
	if err := trunk.Validate(); err != nil {
		t.Fatalf("an outbound credential without an inbound one was refused: %v", err)
	}
	// A password with no username is a typo, not a configuration.
	trunk.OutboundUsername = ""
	err := trunk.Validate()
	if err == nil {
		t.Fatal("an outbound password with no username was accepted")
	}
	// The error has to say which of the two pairs is wrong.
	if !strings.Contains(err.Error(), "outbound") {
		t.Errorf("the error does not name the outbound pair: %v", err)
	}
	trunk.OutboundUsername = "acme-out"
	trunk.OutboundPassword = "short"
	if err := trunk.Validate(); err == nil {
		t.Fatal("a short outbound password was accepted")
	}
	trunk.OutboundPassword = "has;a semicolon in it"
	if err := trunk.Validate(); err == nil {
		t.Fatal("an outbound password that would cut the config line was accepted")
	}
}

// With a "-out" suffix, trunk "acme" and a trunk actually named "acme-out"
// would produce the same auth object name, and PJSIP answers a duplicate by
// refusing it.
func TestTrunkOutboundAuthNameCannotCollide(t *testing.T) {
	plain := Trunk{Name: "acme"}
	suffixed := Trunk{Name: "acme-out"}
	if plain.OutboundAuthName() == suffixed.ObjectName() {
		t.Fatalf("the outbound auth object collides with another trunk's inbound auth: %s",
			plain.OutboundAuthName())
	}
	// Both render together without either disappearing.
	first := sampleTrunk()
	first.OutboundUsername = "one"
	first.OutboundPassword = "a-long-enough-secret"
	second := sampleTrunk()
	second.Name = "acme-out"
	second.Username = "two"
	second.Password = "a-long-enough-secret"
	rendered, err := RenderTrunks([]Trunk{first, second})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[trunkout-acme]", "[trunk-acme-out]"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q:\n%s", want, rendered)
		}
	}
}

// A trunk with no destinations can dial nothing and so spends no SIM. It is a
// real configuration -- one used only as a destination for calls arriving on
// a SIM -- rather than an unfinished one.
func TestTrunkNeedsNoSIMWhenItCannotDialIn(t *testing.T) {
	trunk := sampleTrunk()
	trunk.Destinations = nil
	trunk.Devices = nil
	if err := trunk.Validate(); err != nil {
		t.Fatalf("a forward-only trunk was refused: %v", err)
	}
	rendered, err := RenderTrunks([]Trunk{trunk})
	if err != nil {
		t.Fatalf("RenderTrunks() error = %v", err)
	}
	// The endpoint still has to exist, or it cannot be dialled out to.
	if !strings.Contains(rendered, "[trunk-acme]") {
		t.Fatalf("the endpoint is missing:\n%s", rendered)
	}
	routes, err := RenderTrunkRoutes([]Trunk{trunk})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(routes, "Dial(") {
		t.Errorf("a trunk with no destinations dialled something:\n%s", routes)
	}

	// The moment it can dial in, the SIM list is required again.
	trunk.Destinations = []string{"_1NXXNXXXXXX"}
	err = trunk.Validate()
	if err == nil {
		t.Fatal("a trunk that can dial in was accepted with no SIMs")
	}
	if !strings.Contains(err.Error(), "dial in") {
		t.Errorf("the error does not explain when a SIM is needed: %v", err)
	}
}
