package asteriskconf

import (
	"strings"
	"testing"
)

func smsExtensions() []Extension {
	return []Extension{
		{Name: "15551230000", Password: "a-long-enough-secret", MaxContacts: 1},
		{Name: "15551240000", Password: "a-long-enough-secret", MaxContacts: 1},
	}
}

// Off is the default and must forward nothing: delivering texts to handsets
// is new behaviour and must not switch itself on when a deployment upgrades.
func TestRenderMessagesOffForwardsNothing(t *testing.T) {
	rendered, err := RenderMessages(SMSOff, "127.0.0.1:5062", smsExtensions())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered, "MessageSend(pjsip:15551230000") {
		t.Fatalf("a text was delivered with forwarding off:\n%s", rendered)
	}
	// The outbound half still exists: sending from a handset is a separate
	// switch from delivering to one.
	if !strings.Contains(rendered, "[vocat-msg-15551230000]") {
		t.Fatalf("the outbound context is missing:\n%s", rendered)
	}
}

// The request URI holds the SIM's own number, so the extension named after it
// receives the text -- the same convention the did call mode uses.
func TestRenderMessagesDeliversToTheMatchingExtension(t *testing.T) {
	rendered, err := RenderMessages(SMSPerNumber, "127.0.0.1:5062", smsExtensions())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[vocat-messages]",
		"exten => 15551230000,1,",
		"MessageSend(pjsip:15551230000,${MESSAGE(from)})",
		"MessageSend(pjsip:15551240000,${MESSAGE(from)})",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q:\n%s", want, rendered)
		}
	}
}

// The sender is written into the context rather than taken from
// ${MESSAGE(from)}, which is what the handset claimed rather than what
// Asterisk authenticated.
func TestRenderMessagesWritesTheSenderIntoEachContext(t *testing.T) {
	rendered, err := RenderMessages(SMSPerNumber, "127.0.0.1:5062", smsExtensions())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered,
		"MessageSend(pjsip:vocat/sip:${EXTEN}@127.0.0.1:5062,sip:15551230000@vocat)") {
		t.Fatalf("the outbound context does not assert its own identity:\n%s", rendered)
	}
	// If the sender were taken from the request, a handset could send as
	// another extension and spend another SIM's credit.
	outbound := rendered[strings.Index(rendered, "[vocat-msg-"):]
	if strings.Contains(outbound, "MessageSend(pjsip:vocat/") &&
		strings.Contains(outbound, ",${MESSAGE(from)})") {
		t.Fatalf("an outbound context forwarded the claimed sender:\n%s", outbound)
	}
	// The recipient is unconstrained: it is an ordinary number out on the
	// network, which is the point of sending.
	if !strings.Contains(outbound, "exten => _.,1,") {
		t.Fatalf("the recipient pattern is not open:\n%s", outbound)
	}
}

func TestRenderMessagesRejectsABadMode(t *testing.T) {
	if _, err := RenderMessages(SMSMode("everyone"), "127.0.0.1:5062", nil); err == nil {
		t.Fatal("an unknown SMS mode was accepted")
	}
	// A missing trunk address is not an error: it means no trunk, so there is
	// nowhere to submit a text to and the outbound half is simply absent. The
	// file still has to render, or the include stops resolving.
	rendered, err := RenderMessages(SMSPerNumber, "", smsExtensions())
	if err != nil {
		t.Fatalf("a missing trunk address was refused: %v", err)
	}
	if strings.Contains(rendered, "[vocat-msg-") {
		t.Errorf("an outbound context was rendered with no trunk to submit to:\n%s", rendered)
	}
	if !strings.Contains(rendered, "[vocat-messages]") {
		t.Errorf("the inbound context is missing:\n%s", rendered)
	}
	// The address lands in a dial string, where a comma ends the argument.
	if _, err := RenderMessages(SMSPerNumber, "127.0.0.1:5062,1,System(id)", smsExtensions()); err == nil {
		t.Fatal("a trunk address that would escape its field was accepted")
	}
}

// With no extensions there is nobody to deliver to and nobody to send from,
// but the file still has to render so the include resolves.
func TestRenderMessagesHandlesNoExtensions(t *testing.T) {
	rendered, err := RenderMessages(SMSPerNumber, "127.0.0.1:5062", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "[vocat-messages]") {
		t.Fatalf("the inbound context is missing:\n%s", rendered)
	}
	if strings.Contains(rendered, "[vocat-msg-") {
		t.Fatalf("an outbound context was rendered with no extensions:\n%s", rendered)
	}
}

// The bug this guards: VoCat addresses the request URI with the SIM's number
// exactly as it recorded it, which is normally the +E.164 form, while an
// extension name cannot contain a "+" at all. Rendering only the bare form
// sent every such text to the catch-all, where it was dropped without a trace
// on the handset. Both forms must reach the same endpoint.
func TestInboundSMSMatchesBothTheBareAndE164Forms(t *testing.T) {
	out, err := RenderMessages(SMSPerNumber, "127.0.0.1:5062", []Extension{
		{Name: "639524451636", Password: "abcdefghijkl", MaxContacts: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"exten => 639524451636,1,",
		"exten => +639524451636,1,",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered dialplan is missing %q:\n%s", want, out)
		}
	}
	// Both forms deliver to the one PJSIP endpoint, which is named without
	// the plus because that is the only name the endpoint can have.
	if got := strings.Count(out, "MessageSend(pjsip:639524451636,${MESSAGE(from)})"); got != 2 {
		t.Fatalf("MessageSend to the bare endpoint appears %d times, want 2:\n%s", got, out)
	}
	if strings.Contains(out, "pjsip:+639524451636") {
		t.Fatal("MessageSend addressed an endpoint name containing a plus, which cannot exist")
	}
}
