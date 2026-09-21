package asteriskconf

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Extension is one softphone account: a name to register as, a password to
// register with, and how many devices may hold it at once.
//
// The context is not configurable. Every extension lands in from-internal,
// which is the context the outbound routes are reachable from; letting the
// web UI name a context would mean a typo produces an account that registers
// fine and can call nothing, with no error anywhere.
type Extension struct {
	Name     string
	Password string
	// CallerID is the display name, shown on the called handset where the
	// carrier passes it through. Empty leaves it to Asterisk.
	CallerID string
	// MaxContacts is how many devices may register this account at once.
	// Asterisk rings all of them and the first to answer wins.
	MaxContacts int
	Comment     string
}

const (
	// ExtensionsContext is where every generated extension is placed.
	ExtensionsContext = "from-internal"
	// InternalContext holds extension-to-extension dialling. The shipped
	// extensions.conf includes it from ExtensionsContext ahead of the
	// outbound routes.
	InternalContext = "vocat-internal"
	// internalRingSeconds is how long an internal call rings. Short: the
	// caller is in the same building and can see whether anyone is there.
	internalRingSeconds = 30

	// MinPasswordLength matches the check the Asterisk entrypoint makes on
	// the seeded account. A SIP registrar on a public address is scanned
	// constantly, and this one has a real SIM behind it.
	MinPasswordLength = 12
	MaxPasswordLength = 128

	MinContacts   = 1
	MaxContacts   = 10
	MaxExtensions = 100
)

// reservedNames are section names the shipped pjsip.conf already defines.
// Redefining one is not an override -- Asterisk refuses the duplicate object
// and the whole file may fail to load, taking every other account with it.
var reservedNames = map[string]bool{
	"vocat":         true,
	"transport-udp": true,
	"transport-tcp": true,
	"global":        true,
	"system":        true,
	"general":       true,
}

// emergencyNames are refused as extension names. Internal dialling is
// consulted before the outbound routes, so an account with one of these names
// would shadow the route that reaches emergency services -- silently, and
// only discovered by someone dialling it for real.
//
// The list is deliberately short and covers the numbers reachable from the
// networks these SIMs sit on. It is not an attempt at every country.
var emergencyNames = map[string]bool{
	"911": true, // North America
	"933": true, // North America, E911 address verification
	"112": true, // EU, GSM standard
	"999": true, // UK, Ireland, Hong Kong
	"000": true, // Australia
	"110": true, // China, Japan (police)
	"119": true, // China, Japan, Korea (fire and ambulance)
}

// extensionRune matches a SIP username. Deliberately narrower than the RFC
// allows: these become both a config section name and a dialable extension,
// and a character that is legal in one and special in the other is how a
// working account becomes an unreachable one.
func extensionRune(value rune) bool {
	switch {
	case value >= '0' && value <= '9':
		return true
	case value >= 'a' && value <= 'z', value >= 'A' && value <= 'Z':
		return true
	}
	return strings.ContainsRune("_-.", value)
}

// passwordRune allows printable ASCII except the characters that would end
// the value or the line.
//
// Excluded deliberately:
//
//	; starts a comment, so everything after it is silently discarded
//	space is stripped at both ends by the config parser
//	newline ends the line, and anything after it is parsed as config
//
// Non-ASCII is excluded because the digest a phone computes depends on the
// encoding it chose, and a password that works on one handset and not
// another is not worth the debugging.
func passwordRune(value rune) bool {
	return value > ' ' && value < 127 && value != ';'
}

// callerIDRune keeps the display name to text. Angle brackets, quotes and
// commas are all structural in a callerid value.
func callerIDRune(value rune) bool {
	switch {
	case value >= '0' && value <= '9':
		return true
	case value >= 'a' && value <= 'z', value >= 'A' && value <= 'Z':
		return true
	}
	return strings.ContainsRune(" _-.", value)
}

// Validate reports why an extension cannot be rendered, or nil.
func (e Extension) Validate() error {
	name := strings.TrimSpace(e.Name)
	switch {
	case name == "":
		return errors.New("extension name is required")
	case len(name) > 64:
		return errors.New("extension name is too long")
	case reservedNames[strings.ToLower(name)]:
		return fmt.Errorf("%q is reserved by the shipped configuration", name)
	case strings.HasPrefix(strings.ToLower(name), trunkObjectPrefix),
		strings.HasPrefix(strings.ToLower(name), trunkOutboundAuthPrefix):
		// An external trunk's PJSIP objects are named with these prefixes, so
		// an extension called trunk-acme would be the same section name as the
		// trunk called acme. PJSIP answers a duplicate object by refusing it
		// and can take the rest of the file with it -- which is what the
		// prefixes exist to prevent, and they only work if nothing else may
		// use them.
		return fmt.Errorf("%q starts with %q, which is reserved for external trunks",
			name, trunkObjectPrefix)
	case emergencyNames[name]:
		return fmt.Errorf("%q is an emergency number; an extension with that name would "+
			"shadow the route that reaches it", name)
	}
	for _, value := range name {
		if !extensionRune(value) {
			return fmt.Errorf("extension name contains %q, which is not allowed", value)
		}
	}
	switch {
	case len(e.Password) < MinPasswordLength:
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	case len(e.Password) > MaxPasswordLength:
		return errors.New("password is too long")
	}
	for _, value := range e.Password {
		if !passwordRune(value) {
			// Naming the character is safe: it is not the password.
			return fmt.Errorf("password contains %q, which Asterisk's config parser would cut the line at", value)
		}
	}
	for _, value := range e.CallerID {
		if !callerIDRune(value) {
			return fmt.Errorf("caller ID contains %q, which is not allowed", value)
		}
	}
	if len(e.CallerID) > 64 {
		return errors.New("caller ID is too long")
	}
	if e.MaxContacts < MinContacts || e.MaxContacts > MaxContacts {
		return fmt.Errorf("devices must be between %d and %d", MinContacts, MaxContacts)
	}
	for _, value := range e.Comment {
		if value == '\n' || value == '\r' || value == ';' {
			return errors.New("comment cannot contain a newline or a semicolon")
		}
	}
	return nil
}

// GeneratedHeader marks a file VoCat wrote. The Asterisk entrypoint seeds an
// extensions file from the environment on first start, and the two are
// otherwise indistinguishable -- which matters, because saving from the web
// UI replaces whatever is there.
const GeneratedHeader = "; Generated by VoCat."

// RenderExtensions builds the softphone account file.
//
// Every account gets three sections with the same name: the endpoint (what it
// can do), the auth (how it proves who it is) and the AOR (where it is).
// That triple is how PJSIP models one account, and splitting them across
// files would only make a missing one harder to see.
func RenderExtensions(extensions []Extension) (string, error) {
	if len(extensions) > MaxExtensions {
		return "", fmt.Errorf("too many extensions (%d, limit %d)", len(extensions), MaxExtensions)
	}
	seen := map[string]bool{}
	for index, extension := range extensions {
		if err := extension.Validate(); err != nil {
			return "", fmt.Errorf("extension %d (%s): %w", index+1, extension.Name, err)
		}
		// Case-insensitively: PJSIP matches section names case-sensitively
		// but a registrar lookup does not, so "1001" and "1001" differing
		// only in case would be two objects and one unpredictable login.
		name := strings.ToLower(strings.TrimSpace(extension.Name))
		if seen[name] {
			return "", fmt.Errorf("extension %s appears more than once", strings.TrimSpace(extension.Name))
		}
		seen[name] = true
	}
	ordered := append([]Extension(nil), extensions...)
	sort.SliceStable(ordered, func(first, second int) bool {
		return ordered[first].Name < ordered[second].Name
	})

	var out strings.Builder
	out.WriteString(GeneratedHeader + " Edits here are overwritten on the next Apply.\n")
	out.WriteString(";\n")
	out.WriteString("; This file contains SIP passwords in the clear, which is what Asterisk\n")
	out.WriteString("; needs to answer a digest challenge. It is written 0600 for that reason.\n\n")
	if len(ordered) == 0 {
		out.WriteString("; No extensions configured, so no softphone can register. Calls placed\n")
		out.WriteString("; through the trunk from elsewhere are unaffected.\n")
		return out.String(), nil
	}
	for _, extension := range ordered {
		name := strings.TrimSpace(extension.Name)
		if comment := strings.TrimSpace(extension.Comment); comment != "" {
			fmt.Fprintf(&out, "; %s\n", comment)
		}
		fmt.Fprintf(&out, "[%s]\ntype=endpoint\n", name)
		fmt.Fprintf(&out, "context=%s\n", ExtensionsContext)
		// A text this account sends arrives in a context of its own, which is
		// what carries its identity: see RenderMessages. Without this an SMS
		// would land in the call context above and match a Dial().
		fmt.Fprintf(&out, "message_context=%s\n", MessageContextName(name))
		out.WriteString("disallow=all\nallow=ulaw\nallow=alaw\n")
		fmt.Fprintf(&out, "auth=%s\naors=%s\n", name, name)
		if callerID := strings.TrimSpace(extension.CallerID); callerID != "" {
			fmt.Fprintf(&out, "callerid=%s <%s>\n", callerID, name)
		}
		// A softphone on a phone network is behind NAT essentially always.
		out.WriteString("rtp_symmetric=yes\nforce_rport=yes\nrewrite_contact=yes\ndirect_media=no\n\n")

		fmt.Fprintf(&out, "[%s]\ntype=auth\nauth_type=userpass\n", name)
		fmt.Fprintf(&out, "username=%s\npassword=%s\n\n", name, extension.Password)

		fmt.Fprintf(&out, "[%s]\ntype=aor\n", name)
		fmt.Fprintf(&out, "max_contacts=%d\n", extension.MaxContacts)
		// An old contact that no longer answers would otherwise keep ringing
		// until it expired, delaying every call to this account.
		out.WriteString("remove_existing=yes\n")
		// Without this the contact reads NonQual for ever and the Asterisk
		// page can never say whether the phone is actually there.
		out.WriteString("qualify_frequency=60\n\n")
	}
	return out.String(), nil
}

// RenderInternalDialplan builds the extension-to-extension context.
//
// One exact-match extension per account rather than a pattern: a pattern has
// to guess the numbering plan, and guessing wrong either misses an account
// (it is not dialable and nothing says why) or matches one that does not
// exist (Asterisk dials an endpoint it has never heard of). The account list
// is right here, so neither is necessary.
//
// [from-internal] includes this context before the outbound routes. Asterisk
// consults a context's own extensions first and then walks its includes in
// order, taking the first include that matches -- not the best match across
// all of them -- so an exact extension here wins over any route pattern.
func RenderInternalDialplan(extensions []Extension) (string, error) {
	if len(extensions) > MaxExtensions {
		return "", fmt.Errorf("too many extensions (%d, limit %d)", len(extensions), MaxExtensions)
	}
	names := make([]string, 0, len(extensions))
	seen := map[string]bool{}
	for index, extension := range extensions {
		if err := extension.Validate(); err != nil {
			return "", fmt.Errorf("extension %d (%s): %w", index+1, extension.Name, err)
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
	out.WriteString("; Extension-to-extension dialling, generated from the same account list\n")
	out.WriteString("; as endpoints.conf so an account cannot exist without being dialable.\n\n")
	fmt.Fprintf(&out, "[%s]\n", InternalContext)
	if len(names) == 0 {
		out.WriteString("; No extensions configured. This context matches nothing, so every\n")
		out.WriteString("; number falls through to the outbound routes.\n")
		return out.String(), nil
	}
	for _, name := range names {
		fmt.Fprintf(&out, "exten => %s,1,Dial(PJSIP/%s,%d)\n", name, name, internalRingSeconds)
		out.WriteString(" same => n,Hangup()\n")
	}
	return out.String(), nil
}
