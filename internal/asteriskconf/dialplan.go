// Package asteriskconf renders Asterisk configuration from VoCat's own data.
//
// Everything here is generated, never edited by hand, and everything that
// reaches it has been through Validate first. That matters more than it
// sounds: an Asterisk dialplan can invoke System(), so a value that escapes
// its field does not merely corrupt a config file, it runs a command inside
// the PBX container. The validation below is a strict allowlist for exactly
// that reason -- and separately, a comma in the wrong place silently breaks
// the whole context, which has already happened once on this deployment.
package asteriskconf

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Route is one outbound rule: numbers matching Pattern go to the SIMs in
// Devices, rotated between by VoCat.
type Route struct {
	// Pattern is an Asterisk extension pattern, e.g. "_1NXXNXXXXXX" or "_."
	// for everything.
	Pattern string
	// Devices are VoCat device IDs or names. More than one rotates.
	Devices []string
	// TimeoutSeconds is how long to ring before giving up.
	TimeoutSeconds int
	Comment        string
}

const (
	// RoutesContext is the context the generated file defines, included by the
	// shipped extensions.conf.
	RoutesContext = "vocat-routes"
	// predialContext adds the X-VoCat-Device header on the outbound channel.
	// It lives in the static config because it never varies.
	predialContext = "vocat-predial"

	MinTimeoutSeconds = 5
	MaxTimeoutSeconds = 600
	MaxRoutes         = 200
)

// patternRunes are the characters an Asterisk extension pattern may contain.
// Everything outside this set is refused rather than escaped: escaping rules
// differ per field and a mistake is not visible in the rendered file.
//
// Excluded deliberately and individually:
//
//	, ends the extension field, shifting the rest into application arguments
//	; starts a comment, silently truncating the line
//	newline injects an entirely new dialplan line
//	$ ( ) introduce variable and function expansion
//	" ' \ are quoting, whose rules differ between Asterisk's parsers
func patternRune(value rune) bool {
	switch {
	case value >= '0' && value <= '9':
		return true
	case value >= 'a' && value <= 'z', value >= 'A' && value <= 'Z':
		return true
	}
	return strings.ContainsRune("_[]-.!+*#", value)
}

// deviceRune matches VoCat device IDs and names, which are USB topology
// strings like "usb-2c7c-0125-3-4-5" or labels like "SLOT1-1".
func deviceRune(value rune) bool {
	switch {
	case value >= '0' && value <= '9':
		return true
	case value >= 'a' && value <= 'z', value >= 'A' && value <= 'Z':
		return true
	}
	return strings.ContainsRune("_-.", value)
}

// Validate reports why a route cannot be rendered, or nil.
func (r Route) Validate() error {
	pattern := strings.TrimSpace(r.Pattern)
	switch {
	case pattern == "":
		return errors.New("pattern is required")
	case len(pattern) > 64:
		return errors.New("pattern is too long")
	case !strings.HasPrefix(pattern, "_"):
		// Without the underscore Asterisk treats it as a literal extension,
		// so "1NXXNXXXXXX" would match that exact string and nothing else --
		// a rule that looks right and never fires.
		return errors.New(`pattern must start with "_" to be a pattern, e.g. _1NXXNXXXXXX or _. for everything`)
	}
	for _, value := range pattern {
		if !patternRune(value) {
			return fmt.Errorf("pattern contains %q, which is not allowed in an extension pattern", value)
		}
	}
	if len(r.Devices) == 0 {
		return errors.New("at least one device is required")
	}
	if len(r.Devices) > 32 {
		return errors.New("too many devices")
	}
	for _, device := range r.Devices {
		device = strings.TrimSpace(device)
		if device == "" {
			return errors.New("device name is empty")
		}
		if len(device) > 128 {
			return errors.New("device name is too long")
		}
		for _, value := range device {
			if !deviceRune(value) {
				return fmt.Errorf("device %q contains %q, which is not allowed", device, value)
			}
		}
	}
	if r.TimeoutSeconds < MinTimeoutSeconds || r.TimeoutSeconds > MaxTimeoutSeconds {
		return fmt.Errorf("timeout must be between %d and %d seconds",
			MinTimeoutSeconds, MaxTimeoutSeconds)
	}
	for _, value := range r.Comment {
		if value == '\n' || value == '\r' || value == ';' {
			return errors.New("comment cannot contain a newline or a semicolon")
		}
	}
	return nil
}

// RenderRoutes builds the routes file. Order is significant only to a reader:
// Asterisk picks the most specific matching pattern regardless of position,
// so the rules are sorted for a stable diff rather than left in input order.
func RenderRoutes(routes []Route) (string, error) {
	if len(routes) > MaxRoutes {
		return "", fmt.Errorf("too many routes (%d, limit %d)", len(routes), MaxRoutes)
	}
	seen := map[string]bool{}
	for index, route := range routes {
		if err := route.Validate(); err != nil {
			return "", fmt.Errorf("route %d (%s): %w", index+1, route.Pattern, err)
		}
		pattern := strings.TrimSpace(route.Pattern)
		if seen[pattern] {
			// Asterisk keeps only one priority 1 per extension, so a
			// duplicate silently loses. Refusing is better than generating a
			// file whose second rule never runs.
			return "", fmt.Errorf("pattern %s appears more than once", pattern)
		}
		seen[pattern] = true
	}
	ordered := append([]Route(nil), routes...)
	sort.SliceStable(ordered, func(first, second int) bool {
		return ordered[first].Pattern < ordered[second].Pattern
	})

	var out strings.Builder
	out.WriteString("; Generated by VoCat. Edits here are overwritten on the next Apply.\n")
	out.WriteString(";\n")
	out.WriteString("; Device lists are space separated: a comma would end the extension\n")
	out.WriteString("; field and turn the rest into arguments to Set().\n\n")
	fmt.Fprintf(&out, "[%s]\n", RoutesContext)
	if len(ordered) == 0 {
		out.WriteString("; No routes configured, so no number matches and every call is\n")
		out.WriteString("; answered 404 by Asterisk. Add a route, or use _. to match all.\n")
		return out.String(), nil
	}
	for _, route := range ordered {
		pattern := strings.TrimSpace(route.Pattern)
		devices := make([]string, 0, len(route.Devices))
		for _, device := range route.Devices {
			if device = strings.TrimSpace(device); device != "" {
				devices = append(devices, device)
			}
		}
		if comment := strings.TrimSpace(route.Comment); comment != "" {
			fmt.Fprintf(&out, "; %s\n", comment)
		}
		fmt.Fprintf(&out, "exten => %s,1,Set(__VOCATDEV=%s)\n", pattern, strings.Join(devices, " "))
		fmt.Fprintf(&out, " same => n,Dial(PJSIP/${EXTEN}@vocat,%d,b(%s^s^1))\n",
			route.TimeoutSeconds, predialContext)
		out.WriteString(" same => n,Hangup()\n\n")
	}
	return out.String(), nil
}
