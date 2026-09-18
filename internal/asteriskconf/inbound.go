package asteriskconf

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// InboundMode decides what a call arriving on a SIM rings.
type InboundMode string

const (
	// InboundPerNumber rings the extension named after the number that was
	// dialled -- the SIM's own. It needs no mapping table: VoCat puts the
	// dialled number in the request URI, so the extension name is the map.
	InboundPerNumber InboundMode = "did"
	// InboundRingAll rings every extension in the list at once, whatever was
	// dialled. First to answer wins.
	InboundRingAll InboundMode = "ring_all"
	// InboundHunt rings the list in order, moving on when one does not
	// answer.
	InboundHunt InboundMode = "hunt"
)

const (
	// InboundContext is the context the generated file defines. The shipped
	// extensions.conf routes [from-vocat] into it.
	InboundContext = "vocat-inbound"

	MinRingSeconds = 5
	MaxRingSeconds = 300
	// DefaultRingSeconds is long enough to reach a handset in a pocket and
	// short enough that a carrier's own timer does not fire first.
	DefaultRingSeconds = 30
	// DefaultHuntSeconds is per step: three steps at this length still finish
	// inside a typical network timeout.
	DefaultHuntSeconds = 15
)

// InboundPlan is the whole inbound configuration.
type InboundPlan struct {
	Mode InboundMode
	// Extensions is the ring group. In hunt mode it is the order and is
	// required. In ring-all mode an empty list means every configured
	// extension, which keeps a new handset in the group without a second
	// edit. It is unused in per-number mode.
	Extensions []string
	// RingSeconds is how long a per-number or ring-all call rings.
	RingSeconds int
	// HuntSeconds is how long each step of a hunt rings before the next.
	HuntSeconds int
}

// DefaultInboundPlan is what a deployment gets before anything is configured:
// ring everything, which is the behaviour least likely to lose a call.
func DefaultInboundPlan() InboundPlan {
	return InboundPlan{
		Mode:        InboundRingAll,
		RingSeconds: DefaultRingSeconds,
		HuntSeconds: DefaultHuntSeconds,
	}
}

// Validate reports why a plan cannot be rendered, or nil. configured is the
// extension list the plan refers to, so a group naming an account that does
// not exist is refused here rather than ringing nothing at three in the
// morning.
func (p InboundPlan) Validate(configured []Extension) error {
	switch p.Mode {
	case InboundPerNumber, InboundRingAll, InboundHunt:
	default:
		return fmt.Errorf("inbound mode %q is not one of did, ring_all or hunt", p.Mode)
	}
	if p.RingSeconds < MinRingSeconds || p.RingSeconds > MaxRingSeconds {
		return fmt.Errorf("ring time must be between %d and %d seconds", MinRingSeconds, MaxRingSeconds)
	}
	if p.Mode == InboundHunt {
		if p.HuntSeconds < MinRingSeconds || p.HuntSeconds > MaxRingSeconds {
			return fmt.Errorf("hunt step must be between %d and %d seconds", MinRingSeconds, MaxRingSeconds)
		}
		if len(p.Extensions) == 0 {
			return errors.New("hunt mode needs an ordered list of extensions")
		}
	}
	known := map[string]bool{}
	for _, extension := range configured {
		known[strings.ToLower(strings.TrimSpace(extension.Name))] = true
	}
	seen := map[string]bool{}
	for _, name := range p.Extensions {
		name = strings.TrimSpace(name)
		if name == "" {
			return errors.New("the ring group has an empty entry")
		}
		for _, value := range name {
			if !extensionRune(value) {
				return fmt.Errorf("extension %q contains %q, which is not allowed", name, value)
			}
		}
		if !known[strings.ToLower(name)] {
			return fmt.Errorf("extension %s is in the ring group but is not configured", name)
		}
		if seen[strings.ToLower(name)] {
			return fmt.Errorf("extension %s appears twice in the ring group", name)
		}
		seen[strings.ToLower(name)] = true
	}
	return nil
}

// RenderInbound builds the context a call arriving on a SIM lands in.
func RenderInbound(plan InboundPlan, configured []Extension) (string, error) {
	if err := plan.Validate(configured); err != nil {
		return "", err
	}
	names := make([]string, 0, len(configured))
	for _, extension := range configured {
		if err := extension.Validate(); err != nil {
			return "", fmt.Errorf("extension %s: %w", extension.Name, err)
		}
		names = append(names, strings.TrimSpace(extension.Name))
	}
	sort.Strings(names)

	var out strings.Builder
	out.WriteString(GeneratedHeader + " Edits here are overwritten on the next Apply.\n")
	out.WriteString(";\n")
	out.WriteString("; Where a call arriving on a SIM rings. VoCat puts the dialled number --\n")
	out.WriteString("; the SIM's own -- in the request URI, so ${EXTEN} here is the DID.\n\n")
	fmt.Fprintf(&out, "[%s]\n", InboundContext)

	switch plan.Mode {
	case InboundPerNumber:
		if len(names) == 0 {
			out.WriteString("; No extensions configured, so no number matches.\n")
		}
		for _, name := range names {
			fmt.Fprintf(&out, "exten => %s,1,Dial(PJSIP/%s,%d)\n", name, name, plan.RingSeconds)
			out.WriteString(" same => n,Hangup()\n")
			// The same number in E.164. Which form arrives is the carrier's
			// choice and not one worth making the operator guess at, so both
			// are matched and both ring the same account.
			fmt.Fprintf(&out, "exten => +%s,1,Dial(PJSIP/%s,%d)\n", name, name, plan.RingSeconds)
			out.WriteString(" same => n,Hangup()\n")
		}
		out.WriteString("\n")
		writeNoDestination(&out)
	case InboundRingAll:
		group := plan.Extensions
		if len(group) == 0 {
			// Empty means everything, so a handset added later joins the
			// group without a second edit.
			group = names
		}
		if len(group) == 0 {
			// Nothing configured at all. Rendering a context that rejects the
			// call is honest -- there is genuinely nothing to ring -- and it
			// keeps deleting every extension from being refused as if it were
			// a mistake.
			writeNoDestination(&out)
			break
		}
		fmt.Fprintf(&out, "exten => _.,1,Dial(%s,%d)\n", dialString(group), plan.RingSeconds)
		out.WriteString(" same => n,Hangup()\n")
	case InboundHunt:
		out.WriteString("exten => _.,1,NoOp(Inbound call for ${EXTEN})\n")
		for index, name := range plan.Extensions {
			fmt.Fprintf(&out, " same => n,Dial(PJSIP/%s,%d)\n", strings.TrimSpace(name), plan.HuntSeconds)
			if index == len(plan.Extensions)-1 {
				break
			}
			// Dial returns when the call ends as well as when it fails, so
			// without this an answered call would ring the next handset the
			// moment the first one hung up.
			out.WriteString(" same => n,GotoIf($[\"${DIALSTATUS}\" = \"ANSWER\"]?done)\n")
		}
		out.WriteString(" same => n(done),Hangup()\n")
	}
	return out.String(), nil
}

// writeNoDestination renders the catch-all for a call nothing can take.
// Cause 1 is "unallocated number", which is true and tells the carrier
// something more useful than a silent drop.
func writeNoDestination(out *strings.Builder) {
	out.WriteString("; Nothing to ring for this number.\n")
	out.WriteString("exten => _.,1,NoOp(No destination for ${EXTEN})\n")
	out.WriteString(" same => n,Hangup(1)\n")
}

// dialString joins a ring group into one Dial argument. "&" is what makes
// Asterisk ring them at once rather than in turn.
func dialString(names []string) string {
	targets := make([]string, 0, len(names))
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			targets = append(targets, "PJSIP/"+name)
		}
	}
	return strings.Join(targets, "&")
}
