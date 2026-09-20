package asteriskconf

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Trunk is one external SIP peer whose calls VoCat relays out through a SIM.
//
// The threat this type is shaped by: a peer that can dial through a SIM can
// spend real money. Everything here is therefore an allowlist. A trunk names
// the destinations it may reach and the SIMs it may use, and a trunk that
// names no destinations reaches nothing -- which is what a newly created one
// looks like.
type Trunk struct {
	// Name identifies the trunk. It becomes part of a PJSIP object name and
	// of a dial plan context, both prefixed so neither can collide with a
	// softphone account.
	Name string
	// Host is where the peer is, used as the AOR contact. A hostname or an
	// address; the port is separate.
	Host string
	Port int
	// Transport is "udp" or "tcp", pinned rather than left to PJSIP so a
	// peer that opens the other one is refused loudly instead of silently
	// going unanswered.
	Transport string
	// Match lists addresses or CIDR prefixes to identify the peer by. Empty
	// means the peer must authenticate instead.
	Match []string
	// Username and Password authenticate an inbound INVITE. Empty means the
	// peer is identified by address instead.
	Username string
	Password string
	// OutboundUsername and OutboundPassword answer a challenge to an INVITE
	// this side sends, which is what a provider does when a call is
	// forwarded out to it. Kept separate from the inbound pair rather than
	// reused: a provider that issues one credential for both directions is
	// served by entering it twice, while one that issues two cannot be
	// served at all by a single field. Empty means no outbound_auth is
	// emitted, so an unauthenticated peer stays unauthenticated.
	OutboundUsername string
	OutboundPassword string
	// Destinations are the extension patterns this trunk may dial. Empty
	// means none: the context is rendered, matches nothing, and hangs up.
	Destinations []string
	// Devices are the SIMs this trunk may use, by VoCat device ID or name.
	// VoCat rotates between them and skips any that are busy.
	Devices []string
	// MaxConcurrent caps how many calls this trunk may have in flight. It is
	// enforced in the dial plan, because Asterisk is where the trunk's own
	// channels are counted.
	MaxConcurrent int
	// TimeoutSeconds is how long to ring before giving up.
	TimeoutSeconds int
	// ShareRoutes adds the shared outbound routes to this trunk's context.
	// Off by default and deliberately so: it lets the trunk reach every
	// pattern configured for internal use, which is the isolation the
	// private context exists to provide.
	ShareRoutes bool
	Comment     string
}

const (
	// TrunkRoutesContext is not one context but a prefix: each trunk gets its
	// own, sharing no includes with any other.
	TrunkContextPrefix = "from-trunk-"
	// trunkObjectPrefix keeps a trunk's PJSIP objects in a namespace of their
	// own. Without it a trunk called "1001" would collide with the softphone
	// account of that name, and PJSIP answers a duplicate object by refusing
	// it -- taking the rest of the file with it.
	trunkObjectPrefix = "trunk-"
	// trunkOutboundAuthPrefix names the outbound credential's auth object.
	//
	// A prefix rather than a "-out" suffix, and that is not cosmetic: with a
	// suffix, trunk "acme" would produce trunk-acme-out and a trunk actually
	// named "acme-out" would produce the same name for its inbound auth.
	// These two prefixes cannot collide, because they differ at their sixth
	// character ("-" against "o") whatever follows.
	trunkOutboundAuthPrefix = "trunkout-"
	// trunkGroupCategory is the GROUP() category the per-trunk cap counts in.
	trunkGroupCategory = "trunk"

	MinTrunkConcurrent = 1
	MaxTrunkConcurrent = 64
	// DefaultTrunkConcurrent is deliberately small. A trunk cannot carry more
	// concurrent calls than there are idle SIMs anyway, and a low cap is the
	// difference between a bad hour and a bad month if the peer is abused.
	DefaultTrunkConcurrent = 4
	DefaultTrunkPort       = 5060

	MaxTrunks            = 32
	MaxTrunkDestinations = 64
	MaxTrunkMatches      = 32
)

// hostRune matches a hostname or an address. No spaces, no semicolons, and
// nothing that could end the value: this lands in a PJSIP contact URI.
func hostRune(value rune) bool {
	switch {
	case value >= '0' && value <= '9':
		return true
	case value >= 'a' && value <= 'z', value >= 'A' && value <= 'Z':
		return true
	}
	return strings.ContainsRune(".-:", value)
}

// matchRune is hostRune plus the slash a CIDR prefix needs.
func matchRune(value rune) bool {
	return hostRune(value) || value == '/'
}

// Validate reports why a trunk cannot be rendered, or nil.
func (t Trunk) Validate() error {
	name := strings.TrimSpace(t.Name)
	switch {
	case name == "":
		return errors.New("trunk name is required")
	case len(name) > 48:
		return errors.New("trunk name is too long")
	}
	for _, value := range name {
		if !extensionRune(value) {
			return fmt.Errorf("trunk name contains %q, which is not allowed", value)
		}
	}
	host := strings.TrimSpace(t.Host)
	switch {
	case host == "":
		return errors.New("peer host is required")
	case len(host) > 255:
		return errors.New("peer host is too long")
	}
	for _, value := range host {
		if !hostRune(value) {
			return fmt.Errorf("peer host contains %q, which is not allowed", value)
		}
	}
	if t.Port < 1 || t.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	switch strings.ToLower(strings.TrimSpace(t.Transport)) {
	case "udp", "tcp":
	default:
		return errors.New(`transport must be "udp" or "tcp"`)
	}

	// One of the two has to hold, or the endpoint would accept an INVITE from
	// anywhere that guessed the host -- which is the whole attack.
	hasMatch := len(t.Match) > 0
	hasAuth := strings.TrimSpace(t.Username) != "" || t.Password != ""
	if !hasMatch && !hasAuth {
		return errors.New("a trunk needs an address to match on, credentials, or both; " +
			"without either, anything that reaches this port could dial through a SIM")
	}
	if len(t.Match) > MaxTrunkMatches {
		return errors.New("too many match addresses")
	}
	for _, match := range t.Match {
		match = strings.TrimSpace(match)
		if match == "" {
			return errors.New("match address is empty")
		}
		if len(match) > 64 {
			return errors.New("match address is too long")
		}
		for _, value := range match {
			if !matchRune(value) {
				return fmt.Errorf("match address %q contains %q, which is not allowed", match, value)
			}
		}
	}
	if hasAuth {
		if err := validateTrunkCredential("", t.Username, t.Password); err != nil {
			return err
		}
	}
	// The outbound pair is independent: a trunk identified by address alone
	// may still have to authenticate when this side forwards a call out to
	// it, which is exactly what an inbound call relayed to a provider does.
	if strings.TrimSpace(t.OutboundUsername) != "" || t.OutboundPassword != "" {
		if err := validateTrunkCredential("outbound ", t.OutboundUsername, t.OutboundPassword); err != nil {
			return err
		}
	}

	if len(t.Destinations) > MaxTrunkDestinations {
		return errors.New("too many destination patterns")
	}
	for _, pattern := range t.Destinations {
		pattern = strings.TrimSpace(pattern)
		switch {
		case pattern == "":
			return errors.New("destination pattern is empty")
		case len(pattern) > 64:
			return errors.New("destination pattern is too long")
		case !strings.HasPrefix(pattern, "_"):
			return fmt.Errorf(`destination %q must start with "_" to be a pattern, `+
				`e.g. _1NXXNXXXXXX`, pattern)
		}
		for _, value := range pattern {
			if !patternRune(value) {
				return fmt.Errorf("destination %q contains %q, which is not allowed in an extension pattern",
					pattern, value)
			}
		}
	}

	// Required, unlike an outbound route where an empty list asks VoCat to
	// choose. A trunk is an outside party; which cards it may spend is not
	// something to leave implicit.
	if len(t.Devices) == 0 {
		return errors.New("at least one SIM is required")
	}
	if len(t.Devices) > 32 {
		return errors.New("too many SIMs")
	}
	for _, device := range t.Devices {
		device = strings.TrimSpace(device)
		if device == "" {
			return errors.New("SIM name is empty")
		}
		if len(device) > 128 {
			return errors.New("SIM name is too long")
		}
		for _, value := range device {
			if !deviceRune(value) {
				return fmt.Errorf("SIM %q contains %q, which is not allowed", device, value)
			}
		}
	}
	if t.MaxConcurrent < MinTrunkConcurrent || t.MaxConcurrent > MaxTrunkConcurrent {
		return fmt.Errorf("concurrent calls must be between %d and %d",
			MinTrunkConcurrent, MaxTrunkConcurrent)
	}
	if t.TimeoutSeconds < MinTimeoutSeconds || t.TimeoutSeconds > MaxTimeoutSeconds {
		return fmt.Errorf("timeout must be between %d and %d seconds",
			MinTimeoutSeconds, MaxTimeoutSeconds)
	}
	for _, value := range t.Comment {
		if value == '\n' || value == '\r' || value == ';' {
			return errors.New("comment cannot contain a newline or a semicolon")
		}
	}
	return nil
}

// validateTrunkCredential checks one username and password pair. The label
// names which pair, because a trunk can carry two and an error that does not
// say which one is an error the operator has to guess at.
func validateTrunkCredential(label, username, password string) error {
	if strings.TrimSpace(username) == "" {
		return fmt.Errorf("an %susername is required alongside a %spassword", label, label)
	}
	for _, value := range strings.TrimSpace(username) {
		if !extensionRune(value) {
			return fmt.Errorf("%susername contains %q, which is not allowed", label, value)
		}
	}
	switch {
	case len(password) < MinPasswordLength:
		return fmt.Errorf("%spassword must be at least %d characters", label, MinPasswordLength)
	case len(password) > MaxPasswordLength:
		return fmt.Errorf("%spassword is too long", label)
	}
	for _, value := range password {
		if !passwordRune(value) {
			return fmt.Errorf("%spassword contains %q, which Asterisk's config parser would cut the line at",
				label, value)
		}
	}
	return nil
}

// ObjectName is what this trunk's PJSIP sections are called.
func (t Trunk) ObjectName() string {
	return trunkObjectPrefix + strings.TrimSpace(t.Name)
}

// OutboundAuthName is the auth object holding the outbound credential.
func (t Trunk) OutboundAuthName() string {
	return trunkOutboundAuthPrefix + strings.TrimSpace(t.Name)
}

// ContextName is the dial plan context this trunk's calls arrive in.
func (t Trunk) ContextName() string {
	return TrunkContextPrefix + strings.TrimSpace(t.Name)
}

// validateTrunks runs the per-trunk checks plus the ones that only make sense
// across the whole list.
func validateTrunks(trunks []Trunk) ([]Trunk, error) {
	if len(trunks) > MaxTrunks {
		return nil, fmt.Errorf("too many trunks (%d, limit %d)", len(trunks), MaxTrunks)
	}
	seen := map[string]bool{}
	for index, trunk := range trunks {
		if err := trunk.Validate(); err != nil {
			return nil, fmt.Errorf("trunk %d (%s): %w", index+1, trunk.Name, err)
		}
		// Case-insensitively, because the object name and the context name
		// both derive from this and Asterisk would end up with two of one and
		// an unpredictable winner.
		name := strings.ToLower(strings.TrimSpace(trunk.Name))
		if seen[name] {
			return nil, fmt.Errorf("trunk %s appears more than once", strings.TrimSpace(trunk.Name))
		}
		seen[name] = true
	}
	ordered := append([]Trunk(nil), trunks...)
	sort.SliceStable(ordered, func(first, second int) bool {
		return ordered[first].Name < ordered[second].Name
	})
	return ordered, nil
}

// RenderTrunks builds the PJSIP objects for every external trunk.
//
// Each trunk gets an endpoint pointed at its own context, an AOR holding the
// peer's address, and then whichever of identify and auth it is configured
// for. Codecs are not configurable: VoCat's media layer is G.711 only, so a
// selector could only offer a choice that cannot work.
func RenderTrunks(trunks []Trunk) (string, error) {
	ordered, err := validateTrunks(trunks)
	if err != nil {
		return "", err
	}

	var out strings.Builder
	out.WriteString(GeneratedHeader + " Edits here are overwritten on the next Apply.\n")
	out.WriteString(";\n")
	out.WriteString("; External SIP peers whose calls are relayed out through a SIM. Each one\n")
	out.WriteString("; lands in its own context and can reach nothing but the destinations it\n")
	out.WriteString("; was given.\n")
	out.WriteString(";\n")
	out.WriteString("; This file may contain SIP passwords in the clear, which is what Asterisk\n")
	out.WriteString("; needs to answer a digest challenge. It is written 0600 for that reason.\n\n")
	if len(ordered) == 0 {
		out.WriteString("; No inbound trunks configured.\n")
		return out.String(), nil
	}

	for _, trunk := range ordered {
		object := trunk.ObjectName()
		transport := strings.ToLower(strings.TrimSpace(trunk.Transport))
		if comment := strings.TrimSpace(trunk.Comment); comment != "" {
			fmt.Fprintf(&out, "; %s\n", comment)
		}
		fmt.Fprintf(&out, "[%s]\ntype=endpoint\n", object)
		fmt.Fprintf(&out, "context=%s\n", trunk.ContextName())
		out.WriteString("disallow=all\nallow=ulaw\nallow=alaw\n")
		// The trunk bridges audio itself, so Asterisk must stay in the path.
		out.WriteString("direct_media=no\n")
		fmt.Fprintf(&out, "aors=%s\n", object)
		if strings.TrimSpace(trunk.Username) != "" {
			fmt.Fprintf(&out, "auth=%s\n", object)
		}
		// outbound_auth answers a challenge to an INVITE this side sends,
		// which auth= does not: that one only authenticates what arrives.
		// Without it a forwarded call dies at 401 with nothing in the
		// configuration to point at.
		if strings.TrimSpace(trunk.OutboundUsername) != "" {
			fmt.Fprintf(&out, "outbound_auth=%s\n", trunk.OutboundAuthName())
		}
		fmt.Fprintf(&out, "transport=transport-%s\n", transport)
		// An external peer is on the far side of something. Unlike the
		// loopback trunk to VoCat, symmetric RTP and contact rewriting are
		// what make it work through NAT.
		out.WriteString("rtp_symmetric=yes\nforce_rport=yes\nrewrite_contact=yes\n\n")

		fmt.Fprintf(&out, "[%s]\ntype=aor\n", object)
		fmt.Fprintf(&out, "contact=sip:%s:%d\n", strings.TrimSpace(trunk.Host), trunk.Port)
		out.WriteString("qualify_frequency=60\n\n")

		if strings.TrimSpace(trunk.Username) != "" {
			fmt.Fprintf(&out, "[%s]\ntype=auth\nauth_type=userpass\n", object)
			fmt.Fprintf(&out, "username=%s\npassword=%s\n\n",
				strings.TrimSpace(trunk.Username), trunk.Password)
		}

		if strings.TrimSpace(trunk.OutboundUsername) != "" {
			fmt.Fprintf(&out, "[%s]\ntype=auth\nauth_type=userpass\n", trunk.OutboundAuthName())
			fmt.Fprintf(&out, "username=%s\npassword=%s\n\n",
				strings.TrimSpace(trunk.OutboundUsername), trunk.OutboundPassword)
		}

		if len(trunk.Match) > 0 {
			fmt.Fprintf(&out, "[%s]\ntype=identify\n", object)
			fmt.Fprintf(&out, "endpoint=%s\n", object)
			for _, match := range trunk.Match {
				if match = strings.TrimSpace(match); match != "" {
					fmt.Fprintf(&out, "match=%s\n", match)
				}
			}
			out.WriteString("\n")
		}
	}
	return out.String(), nil
}

// RenderTrunkRoutes builds one context per trunk.
//
// Nothing is shared between them and nothing is included by default. A call
// that matches no destination reaches the catch-all and is hung up as an
// unallocated number, which is what an empty destination list produces for
// every call -- the state a newly created trunk is in.
func RenderTrunkRoutes(trunks []Trunk) (string, error) {
	ordered, err := validateTrunks(trunks)
	if err != nil {
		return "", err
	}

	var out strings.Builder
	out.WriteString(GeneratedHeader + " Edits here are overwritten on the next Apply.\n")
	out.WriteString(";\n")
	out.WriteString("; One context per trunk, sharing no includes: a trunk cannot dial a\n")
	out.WriteString("; softphone extension and cannot reach a route meant for internal use.\n")
	out.WriteString(";\n")
	out.WriteString("; SIM lists are space separated: a comma would end the extension field\n")
	out.WriteString("; and turn the rest into arguments to Set().\n\n")
	if len(ordered) == 0 {
		out.WriteString("; No inbound trunks configured.\n")
		return out.String(), nil
	}

	for _, trunk := range ordered {
		name := strings.TrimSpace(trunk.Name)
		devices := make([]string, 0, len(trunk.Devices))
		for _, device := range trunk.Devices {
			if device = strings.TrimSpace(device); device != "" {
				devices = append(devices, device)
			}
		}
		if comment := strings.TrimSpace(trunk.Comment); comment != "" {
			fmt.Fprintf(&out, "; %s\n", comment)
		}
		fmt.Fprintf(&out, "[%s]\n", trunk.ContextName())

		patterns := make([]string, 0, len(trunk.Destinations))
		for _, pattern := range trunk.Destinations {
			if pattern = strings.TrimSpace(pattern); pattern != "" {
				patterns = append(patterns, pattern)
			}
		}
		sort.Strings(patterns)
		if len(patterns) == 0 {
			out.WriteString("; No destinations permitted, so this trunk can dial nothing.\n")
		}
		for _, pattern := range patterns {
			// The group is set before the count is read, so this call is
			// included in it and "> max" admits exactly max at a time.
			fmt.Fprintf(&out, "exten => %s,1,Set(GROUP(%s)=%s)\n", pattern, trunkGroupCategory, name)
			fmt.Fprintf(&out, " same => n,GotoIf($[${GROUP_COUNT(%s@%s)} > %d]?full)\n",
				name, trunkGroupCategory, trunk.MaxConcurrent)
			fmt.Fprintf(&out, " same => n,Set(__VOCATDEV=%s)\n", strings.Join(devices, " "))
			fmt.Fprintf(&out, " same => n,Dial(PJSIP/${EXTEN}@vocat,%d,b(%s^s^1))\n",
				trunk.TimeoutSeconds, predialContext)
			out.WriteString(" same => n,Hangup()\n")
			out.WriteString(" same => n(full),Congestion()\n")
		}
		if trunk.ShareRoutes {
			out.WriteString("; Shared outbound routes, enabled for this trunk.\n")
			out.WriteString(";\n")
			out.WriteString("; Two consequences, both real: this trunk can dial every pattern the\n")
			out.WriteString("; shared routes define, not only the destinations above; and those\n")
			out.WriteString("; calls use the routes' own SIMs and are not counted by the cap\n")
			out.WriteString("; above, because the cap lives in the extensions this include\n")
			out.WriteString("; bypasses.\n")
			fmt.Fprintf(&out, "include => %s\n", RoutesContext)
			// No catch-all here. Asterisk walks a context's includes only
			// when the context itself matches nothing, so a _. extension
			// would win over the include and make it dead config -- the same
			// trap the shipped [from-internal] carries a comment about.
		} else {
			writeNoDestination(&out)
		}
		out.WriteString("\n")
	}
	return out.String(), nil
}
