package asteriskconf

import (
	"fmt"
	"sort"
	"strings"
)

// SMSMode decides what happens to an SMS arriving on a SIM.
type SMSMode string

const (
	// SMSOff forwards nothing. The default, and deliberately so: delivering
	// texts to handsets is new behaviour and must not switch itself on when a
	// deployment upgrades.
	SMSOff SMSMode = "off"
	// SMSPerNumber delivers to the extension named after the number the SMS
	// arrived on -- the SIM's own. The same convention the did inbound call
	// mode uses, so one extension per SIM serves calls and texts alike.
	SMSPerNumber SMSMode = "did"
)

const (
	// MessagesContext receives SMS arriving from a SIM. It is reached through
	// message_context on the trunk endpoint rather than through its context:
	// the context is a *call* dialplan, and an SMS landing there would make
	// the handset ring.
	MessagesContext = "vocat-messages"
	// messageContextPrefix names the per-extension context an outbound SMS
	// is sent from. One per extension, because the context is what carries
	// the sender's identity -- see RenderMessages.
	messageContextPrefix = "vocat-msg-"
)

// MessageContextName is the context an extension's outbound SMS arrives in.
func MessageContextName(extension string) string {
	return messageContextPrefix + strings.TrimSpace(extension)
}

// Validate reports why an SMS mode cannot be rendered, or nil.
func (m SMSMode) Validate() error {
	switch m {
	case SMSOff, SMSPerNumber:
		return nil
	}
	return fmt.Errorf("SMS mode %q is not one of off or did", m)
}

// RenderMessages builds the dialplan for SMS in both directions.
//
// Two halves, in one file because they regenerate from the same extension list
// and both belong to pbx_config.
//
// Inbound: VoCat sends a MESSAGE whose request URI holds the SIM's own number,
// so the extension named after it is the destination, and MessageSend passes
// the original sender through unchanged.
//
// Outbound: one context per extension, each naming *that* extension as the
// sender. The identity has to come from here rather than from the incoming
// From header, because ${MESSAGE(from)} is what the handset wrote, not what
// Asterisk concluded: Asterisk authenticates the endpoint and uses that to
// choose the context, then hands the dialplan the claimed From unchanged.
// Writing the identity into the context is how the authenticated endpoint
// reaches VoCat, which then refuses any sender that is not a SIM it hosts.
func RenderMessages(mode SMSMode, trunkHost string, extensions []Extension) (string, error) {
	if err := mode.Validate(); err != nil {
		return "", err
	}
	// An empty trunk address is not an error. It means no trunk is configured,
	// so there is nowhere for an extension to submit a text to, and the
	// outbound half below is simply not rendered -- the inbound half, and the
	// file itself, still have to exist for the include to resolve.
	trunkHost = strings.TrimSpace(trunkHost)
	for _, value := range trunkHost {
		if !hostRune(value) {
			return "", fmt.Errorf("trunk address contains %q, which is not allowed", value)
		}
	}
	names := make([]string, 0, len(extensions))
	seen := map[string]bool{}
	for _, extension := range extensions {
		if err := extension.Validate(); err != nil {
			return "", fmt.Errorf("extension %s: %w", extension.Name, err)
		}
		name := strings.TrimSpace(extension.Name)
		if seen[strings.ToLower(name)] {
			return "", fmt.Errorf("extension %s appears more than once", name)
		}
		seen[strings.ToLower(name)] = true
		names = append(names, name)
	}
	sort.Strings(names)

	var out strings.Builder
	out.WriteString(GeneratedHeader + " Edits here are overwritten on the next Apply.\n")
	out.WriteString(";\n")
	out.WriteString("; SMS in both directions. Reached through message_context, never\n")
	out.WriteString("; through a call context: a text landing in one of those would match a\n")
	out.WriteString("; Dial() and make the handset ring.\n\n")

	fmt.Fprintf(&out, "[%s]\n", MessagesContext)
	switch {
	case mode == SMSOff:
		out.WriteString("; Forwarding is off, so a text arriving on a SIM is kept in VoCat's\n")
		out.WriteString("; own history and goes no further.\n")
		out.WriteString("exten => _.,1,NoOp(SMS forwarding is off: ${EXTEN})\n")
		out.WriteString(" same => n,Hangup()\n")
	case len(names) == 0:
		out.WriteString("; No extensions configured, so there is nobody to deliver to.\n")
		out.WriteString("exten => _.,1,NoOp(No extension for ${EXTEN})\n")
		out.WriteString(" same => n,Hangup()\n")
	default:
		out.WriteString("; The request URI holds the SIM's own number, so the extension named\n")
		out.WriteString("; after it is the destination. The sender is passed through as it\n")
		out.WriteString("; arrived, so the handset sees who actually texted.\n")
		for _, name := range names {
			fmt.Fprintf(&out, "exten => %s,1,NoOp(SMS for %s from ${MESSAGE(from)})\n", name, name)
			fmt.Fprintf(&out, " same => n,MessageSend(pjsip:%s,${MESSAGE(from)})\n", name)
			out.WriteString(" same => n,Hangup()\n")
		}
		out.WriteString("exten => _.,1,NoOp(No extension for ${EXTEN})\n")
		out.WriteString(" same => n,Hangup()\n")
	}
	out.WriteString("\n")

	if len(names) == 0 || trunkHost == "" {
		if trunkHost == "" && len(names) > 0 {
			out.WriteString("; No SIP trunk address is configured, so an extension has nowhere to\n")
			out.WriteString("; submit a text to and no outbound context is rendered.\n")
		}
		return out.String(), nil
	}
	out.WriteString("; One context per extension. The sender below is written in rather\n")
	out.WriteString("; than taken from ${MESSAGE(from)}, which is what the handset claimed\n")
	out.WriteString("; rather than what Asterisk authenticated -- so a handset cannot send\n")
	out.WriteString("; as another extension and spend another SIM's credit.\n")
	out.WriteString(";\n")
	out.WriteString("; ${EXTEN} is the recipient and is deliberately unconstrained: it is an\n")
	out.WriteString("; ordinary number out on the network, which is the point of sending.\n\n")
	for _, name := range names {
		fmt.Fprintf(&out, "[%s]\n", MessageContextName(name))
		fmt.Fprintf(&out, "exten => _.,1,NoOp(SMS from %s to ${EXTEN})\n", name)
		fmt.Fprintf(&out, " same => n,MessageSend(pjsip:vocat/sip:${EXTEN}@%s,sip:%s@vocat)\n",
			trunkHost, name)
		out.WriteString(" same => n,Hangup()\n\n")
	}
	return out.String(), nil
}
