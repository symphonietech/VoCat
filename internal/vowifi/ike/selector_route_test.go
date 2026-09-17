package ike

import (
	"net"
	"testing"
)

func TestSelectorFamilyFlagSeparatesAddressFamilies(t *testing.T) {
	v4 := trafficSelector{StartIP: net.ParseIP("0.0.0.0"), EndIP: net.ParseIP("255.255.255.255")}
	if got := selectorFamilyFlag(v4); got != "-4" {
		t.Fatalf("IPv4 selector reported %q", got)
	}
	v6 := trafficSelector{StartIP: net.ParseIP("::"), EndIP: net.ParseIP("ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff")}
	if got := selectorFamilyFlag(v6); got != "-6" {
		t.Fatalf("IPv6 selector reported %q", got)
	}
}

// The prefixes these become are what gets routed into the tunnel, so an ePDG
// offering the whole address space has to yield a default route rather than an
// error — that is the case that carries media.
func TestSelectorPrefixCoversTheRangesAnEPDGOffers(t *testing.T) {
	for name, testCase := range map[string]struct {
		start, end string
		want       string
	}{
		"IPv6 everything": {"::", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "::/0"},
		"IPv4 everything": {"0.0.0.0", "255.255.255.255", "0.0.0.0/0"},
		"carrier ULA /32": {"fd00:976a::", "fd00:976a:ffff:ffff:ffff:ffff:ffff:ffff", "fd00:976a::/32"},
		"single host":     {"fd00:976a:2:113::4", "fd00:976a:2:113::4", "fd00:976a:2:113::4/128"},
	} {
		selector := trafficSelector{StartIP: net.ParseIP(testCase.start), EndIP: net.ParseIP(testCase.end)}
		got, err := selectorPrefix(selector)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != testCase.want {
			t.Fatalf("%s: prefix %q, want %q", name, got, testCase.want)
		}
	}
}

// A range that is not a clean CIDR block must be reported rather than turned
// into a route that would cover addresses the peer never offered.
func TestSelectorPrefixRejectsARangeThatIsNotAPrefix(t *testing.T) {
	selector := trafficSelector{
		StartIP: net.ParseIP("fd00:976a::1"),
		EndIP:   net.ParseIP("fd00:976a::5"),
	}
	if prefix, err := selectorPrefix(selector); err == nil {
		t.Fatalf("accepted a non-CIDR range as %q", prefix)
	}
}
