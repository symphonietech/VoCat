package asteriskconf

import (
	"strings"
	"testing"
)

func goodRoute() Route {
	return Route{Pattern: "_1NXXNXXXXXX", Devices: []string{"slot-4", "slot-5"}, TimeoutSeconds: 60}
}

// An Asterisk dialplan can call System(), so a value that escapes its field
// runs a command inside the PBX container. Every one of these is refused
// rather than escaped: escaping rules differ per field and a mistake is
// invisible in the rendered output.
func TestValidateRefusesInjectionInThePattern(t *testing.T) {
	for _, pattern := range []string{
		`_X.,1,System(rm -rf /)`,          // comma ends the extension field
		"_X.\nexten => s,1,System(id)",    // newline injects a whole line
		"_X. ; and the rest is a comment", // semicolon truncates the line
		"_${SHELL(id)}",                   // function expansion
		"_X.)",                            // unbalanced application syntax
		`_X."`,                            // quoting
		`_X.\`,                            // escape
		"_X.$(id)",                        // substitution
	} {
		route := goodRoute()
		route.Pattern = pattern
		if err := route.Validate(); err == nil {
			t.Errorf("pattern %q was accepted", pattern)
		}
	}
}

func TestValidateRefusesInjectionInADeviceName(t *testing.T) {
	for _, device := range []string{
		"slot-4 slot-5)\n same => n,System(id)",
		"slot-4,slot-5",
		"slot-4;",
		"${SHELL(id)}",
		"",
	} {
		route := goodRoute()
		route.Devices = []string{device}
		if err := route.Validate(); err == nil {
			t.Errorf("device %q was accepted", device)
		}
	}
}

// A pattern without the leading underscore is a literal extension: it matches
// that exact string and nothing else. It looks correct and never fires, which
// is worse than being rejected.
func TestValidateRequiresAPatternPrefix(t *testing.T) {
	route := goodRoute()
	route.Pattern = "1NXXNXXXXXX"
	err := route.Validate()
	if err == nil || !strings.Contains(err.Error(), `must start with "_"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateBoundsTheTimeout(t *testing.T) {
	for _, seconds := range []int{0, -1, MinTimeoutSeconds - 1, MaxTimeoutSeconds + 1} {
		route := goodRoute()
		route.TimeoutSeconds = seconds
		if err := route.Validate(); err == nil {
			t.Errorf("timeout %d was accepted", seconds)
		}
	}
	for _, seconds := range []int{MinTimeoutSeconds, 60, MaxTimeoutSeconds} {
		route := goodRoute()
		route.TimeoutSeconds = seconds
		if err := route.Validate(); err != nil {
			t.Errorf("timeout %d rejected: %v", seconds, err)
		}
	}
}

// The rendered line must have exactly three comma-separated fields: the
// extension, the priority and the application. A device list joined with
// commas produced six, which broke the whole context on a live system.
func TestRenderRoutesProducesThreeCommaFields(t *testing.T) {
	out, err := RenderRoutes([]Route{{
		Pattern:        "_.",
		Devices:        []string{"usb-2c7c-0125-3-4-4", "usb-2c7c-0125-3-4-5", "usb-2c7c-0125-3-4-6"},
		TimeoutSeconds: 60,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "exten => ") {
			continue
		}
		if fields := strings.Split(line, ","); len(fields) != 3 {
			t.Fatalf("line %q has %d comma fields, want 3", line, len(fields))
		}
		if !strings.Contains(line, "usb-2c7c-0125-3-4-4 usb-2c7c-0125-3-4-5") {
			t.Fatalf("devices are not space separated: %q", line)
		}
	}
}

func TestRenderRoutesRendersEveryPart(t *testing.T) {
	out, err := RenderRoutes([]Route{
		{Pattern: "_011.", Devices: []string{"slot-6"}, TimeoutSeconds: 90, Comment: "international"},
		{Pattern: "_1NXXNXXXXXX", Devices: []string{"slot-4", "slot-5"}, TimeoutSeconds: 45},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[" + RoutesContext + "]",
		"; international",
		"exten => _011.,1,Set(__VOCATDEV=slot-6)",
		" same => n,Dial(PJSIP/${EXTEN}@vocat,90,b(vocat-predial^s^1))",
		"exten => _1NXXNXXXXXX,1,Set(__VOCATDEV=slot-4 slot-5)",
		" same => n,Dial(PJSIP/${EXTEN}@vocat,45,b(vocat-predial^s^1))",
		" same => n,Hangup()",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

// Asterisk keeps one priority 1 per extension, so the second of two
// identical patterns silently never runs.
func TestRenderRoutesRefusesADuplicatePattern(t *testing.T) {
	_, err := RenderRoutes([]Route{goodRoute(), goodRoute()})
	if err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("error = %v", err)
	}
}

// An empty ruleset renders a valid, empty context rather than failing: the
// file has to exist for the include in extensions.conf to resolve.
func TestRenderRoutesWithNoRoutesIsStillValid(t *testing.T) {
	out, err := RenderRoutes(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "["+RoutesContext+"]") {
		t.Fatalf("no context declared:\n%s", out)
	}
	if strings.Contains(out, "exten =>") {
		t.Fatalf("empty ruleset produced an extension:\n%s", out)
	}
}

// Whatever reaches the file, it must never contain a construct that could
// execute something. This is the backstop for the per-field rules above.
func TestRenderRoutesNeverEmitsExpansionBeyondEXTEN(t *testing.T) {
	out, err := RenderRoutes([]Route{goodRoute()})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"System(", "SHELL(", "EVAL(", "`", "$("} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("output contains %q:\n%s", forbidden, out)
		}
	}
	// ${EXTEN} is the one expansion the dialplan needs, and it is ours.
	if strings.Count(out, "${") != strings.Count(out, "${EXTEN}") {
		t.Fatalf("output contains an expansion other than ${EXTEN}:\n%s", out)
	}
}
