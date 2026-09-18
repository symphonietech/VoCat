package asteriskconf

import (
	"strings"
	"testing"
)

func validExtension() Extension {
	return Extension{Name: "1001", Password: "correct-horse-battery", MaxContacts: 2}
}

// The three sections are one account. A missing auth registers nothing and a
// missing AOR has nowhere to ring, and neither failure names itself.
func TestRenderExtensionsWritesTheWholeTriple(t *testing.T) {
	rendered, err := RenderExtensions([]Extension{validExtension()})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[1001]\ntype=endpoint\n",
		"[1001]\ntype=auth\n",
		"[1001]\ntype=aor\n",
		"context=from-internal\n",
		"password=correct-horse-battery\n",
		"max_contacts=2\n",
		"qualify_frequency=60\n",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered file is missing %q:\n%s", want, rendered)
		}
	}
}

// A semicolon starts a comment, so a password containing one is silently
// truncated -- Asterisk loads happily and the phone can never register.
// Refusing beats generating a file that is wrong in a way nothing reports.
func TestExtensionRefusesAPasswordTheParserWouldCut(t *testing.T) {
	for _, password := range []string{
		"secret;comment",
		"has a space",
		"tab\there",
		"line\nbreak",
		"unicodeパスワード",
	} {
		extension := validExtension()
		extension.Password = password
		if err := extension.Validate(); err == nil {
			t.Errorf("password %q was accepted", password)
		}
	}
}

// Short passwords are what make a SIP registrar somebody else's long-distance
// plan, and this one has a real SIM behind it.
func TestExtensionRequiresALongEnoughPassword(t *testing.T) {
	extension := validExtension()
	extension.Password = "short"
	if err := extension.Validate(); err == nil {
		t.Fatal("a five-character password was accepted")
	}
	extension.Password = strings.Repeat("a", MinPasswordLength)
	if err := extension.Validate(); err != nil {
		t.Fatalf("a password of exactly the minimum length was refused: %v", err)
	}
}

// The shipped pjsip.conf already defines these. A second object with the same
// name is not an override: Asterisk refuses the duplicate, and depending on
// where it gives up it can take the rest of the file -- including the trunk --
// with it.
func TestExtensionRefusesReservedNames(t *testing.T) {
	for _, name := range []string{"vocat", "VoCat", "transport-udp", "global"} {
		extension := validExtension()
		extension.Name = name
		if err := extension.Validate(); err == nil {
			t.Errorf("reserved name %q was accepted", name)
		}
	}
}

// A name is both a config section and a dialable extension. Anything
// structural in either is refused rather than escaped.
func TestExtensionRefusesAStructuralName(t *testing.T) {
	for _, name := range []string{"10 01", "1001]", "[1001", "1001;x", "1001=x", "a/b", ""} {
		extension := validExtension()
		extension.Name = name
		if err := extension.Validate(); err == nil {
			t.Errorf("name %q was accepted", name)
		}
	}
}

// Two accounts differing only in case would be two PJSIP objects and one
// unpredictable login, because a registrar lookup is not case-sensitive.
func TestRenderExtensionsRefusesDuplicateNames(t *testing.T) {
	first, second := validExtension(), validExtension()
	second.Name = "1001"
	if _, err := RenderExtensions([]Extension{first, second}); err == nil {
		t.Fatal("a duplicate extension was rendered")
	}
	second.Name = "1001"
	first.Name = "1001"
	upper := validExtension()
	upper.Name = "Phone"
	lower := validExtension()
	lower.Name = "phone"
	if _, err := RenderExtensions([]Extension{upper, lower}); err == nil {
		t.Fatal("names differing only in case were rendered as two accounts")
	}
}

// Nothing configured is a valid state -- calls through the trunk do not need
// a softphone -- and it must still produce a file Asterisk can parse, because
// pjsip.conf includes it unconditionally.
func TestRenderExtensionsHandlesAnEmptyList(t *testing.T) {
	rendered, err := RenderExtensions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rendered, GeneratedHeader) {
		t.Fatalf("an empty file is not marked as generated:\n%s", rendered)
	}
	for _, line := range strings.Split(rendered, "\n") {
		if line != "" && !strings.HasPrefix(line, ";") {
			t.Fatalf("an empty list rendered a config line: %q", line)
		}
	}
}

// The header is how VoCat tells its own file from the one the Asterisk
// entrypoint seeds out of the environment, which decides whether the web UI
// warns that saving would replace an account it did not create.
func TestRenderExtensionsMarksItsOwnFile(t *testing.T) {
	rendered, err := RenderExtensions([]Extension{validExtension()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rendered, GeneratedHeader) {
		t.Fatalf("generated file is not marked:\n%s", rendered)
	}
}

// A caller ID is a display name, not a place to put syntax.
func TestExtensionValidatesTheCallerID(t *testing.T) {
	extension := validExtension()
	extension.CallerID = "Front Desk"
	if err := extension.Validate(); err != nil {
		t.Fatalf("a plain display name was refused: %v", err)
	}
	rendered, err := RenderExtensions([]Extension{extension})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "callerid=Front Desk <1001>\n") {
		t.Fatalf("caller ID not rendered:\n%s", rendered)
	}
	for _, callerID := range []string{`Bad"Quote`, "Bad<Angle>", "Bad,Comma", "Bad;Semi"} {
		extension.CallerID = callerID
		if err := extension.Validate(); err == nil {
			t.Errorf("caller ID %q was accepted", callerID)
		}
	}
}

// max_contacts bounds how many devices hold one account. Zero would refuse
// every registration, and an unbounded value makes one leaked password ring
// an arbitrary number of phones.
func TestExtensionBoundsTheContactCount(t *testing.T) {
	for _, count := range []int{0, -1, MaxContacts + 1} {
		extension := validExtension()
		extension.MaxContacts = count
		if err := extension.Validate(); err == nil {
			t.Errorf("max_contacts %d was accepted", count)
		}
	}
}

// Exact extensions, not a pattern. A pattern has to guess the numbering plan,
// and guessing wrong either leaves an account undialable or dials an endpoint
// Asterisk has never heard of.
func TestRenderInternalDialplanDialsEachAccountExactly(t *testing.T) {
	first, second := validExtension(), validExtension()
	second.Name = "1002"
	rendered, err := RenderInternalDialplan([]Extension{second, first})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "["+InternalContext+"]\n") {
		t.Fatalf("wrong context:\n%s", rendered)
	}
	for _, want := range []string{
		"exten => 1001,1,Dial(PJSIP/1001,30)\n same => n,Hangup()\n",
		"exten => 1002,1,Dial(PJSIP/1002,30)\n same => n,Hangup()\n",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q:\n%s", want, rendered)
		}
	}
	// No "@vocat": that suffix is what sends a call out through the trunk,
	// and an internal call must stay on the PBX.
	if strings.Contains(rendered, "@vocat") {
		t.Fatalf("an internal call was routed to the trunk:\n%s", rendered)
	}
	// Sorted, so the file has a stable diff regardless of input order.
	if strings.Index(rendered, "exten => 1001") > strings.Index(rendered, "exten => 1002") {
		t.Fatalf("output is not sorted:\n%s", rendered)
	}
}

// pjsip.conf and extensions.conf both include their generated file
// unconditionally, so an empty list must still produce a parseable context.
func TestRenderInternalDialplanHandlesAnEmptyList(t *testing.T) {
	rendered, err := RenderInternalDialplan(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "["+InternalContext+"]") {
		t.Fatalf("the context is missing, so from-internal would include nothing:\n%s", rendered)
	}
	if strings.Contains(rendered, "exten =>") {
		t.Fatalf("an empty list produced an extension:\n%s", rendered)
	}
}

// Internal dialling is consulted before the outbound routes, so an account
// named after an emergency number would shadow the route that reaches it --
// discovered only by someone dialling it for real.
func TestExtensionRefusesEmergencyNumbers(t *testing.T) {
	for _, name := range []string{"911", "933", "112", "999", "000", "110", "119"} {
		extension := validExtension()
		extension.Name = name
		if err := extension.Validate(); err == nil {
			t.Errorf("emergency number %q was accepted as an extension", name)
		}
	}
	// Not a blanket ban on numbers that merely start with one of them.
	extension := validExtension()
	extension.Name = "9110"
	if err := extension.Validate(); err != nil {
		t.Errorf("9110 was refused: %v", err)
	}
}

// Both files come from one list, so a rejected account must stop both --
// otherwise one of them is written from a list the other refused.
func TestRenderInternalDialplanRefusesWhatEndpointsRefuse(t *testing.T) {
	bad := validExtension()
	bad.Name = "vocat"
	if _, err := RenderInternalDialplan([]Extension{bad}); err == nil {
		t.Fatal("a reserved name was rendered into the dialplan")
	}
	duplicate := validExtension()
	if _, err := RenderInternalDialplan([]Extension{validExtension(), duplicate}); err == nil {
		t.Fatal("a duplicate was rendered into the dialplan")
	}
}
