//go:build linux && (amd64 || arm64 || arm)

package pcsc

import (
	"os"
	"testing"
	"time"
)

func TestPCSCDHealthNeedsRestart(t *testing.T) {
	cases := []struct {
		name   string
		health pcscdHealth
		want   bool
	}{
		{"healthy", pcscdHealth{pcscdReaders: 16}, false},
		{"no readers at all", pcscdHealth{}, false},
		{"stale reader after replug", pcscdHealth{staleReaders: 16, pcscdReaders: 16, unmatchedPhysical: 16}, true},
		{"one stale reader", pcscdHealth{staleReaders: 1, pcscdReaders: 5}, true},
		{"new reader not picked up", pcscdHealth{pcscdReaders: 4, unmatchedPhysical: 1}, true},
		{"more readers than pcscd's table", pcscdHealth{pcscdReaders: 16, unmatchedPhysical: 2}, false},
	}
	for _, tc := range cases {
		if got := tc.health.needsRestart(16); got != tc.want {
			t.Errorf("%s: needsRestart() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPCSCDRestartLimiterBacksOff(t *testing.T) {
	var limiter pcscdRestartLimiter
	start := time.Unix(1_000_000, 0)
	if !limiter.allow(start) {
		t.Fatal("first restart was refused")
	}
	if limiter.allow(start.Add(59 * time.Second)) {
		t.Fatal("restart allowed within the 1 minute backoff")
	}
	if !limiter.allow(start.Add(61 * time.Second)) {
		t.Fatal("restart refused after the 1 minute backoff")
	}
	if limiter.allow(start.Add(61*time.Second + 119*time.Second)) {
		t.Fatal("restart allowed within the doubled 2 minute backoff")
	}
	now := start.Add(61*time.Second + 121*time.Second)
	if !limiter.allow(now) {
		t.Fatal("restart refused after the 2 minute backoff")
	}
	for i := 0; i < 10; i++ {
		now = now.Add(pcscdRestartMaxBackoff)
		if !limiter.allow(now) {
			t.Fatalf("restart %d refused after the maximum backoff", i)
		}
	}
	if limiter.backoff != pcscdRestartMaxBackoff {
		t.Fatalf("backoff = %v, want cap %v", limiter.backoff, pcscdRestartMaxBackoff)
	}
	limiter.healthy()
	if !limiter.allow(now.Add(time.Minute)) {
		t.Fatal("restart refused one minute after a healthy listing reset the backoff")
	}
}

func TestParsePCSCDPID(t *testing.T) {
	for content, want := range map[string]int{
		"10\x00\x00\x00": 10,
		"1234\n":         1234,
		"":               0,
		"\x00\x00":       0,
		"abc":            0,
	} {
		if got := parsePCSCDPID(content); got != want {
			t.Errorf("parsePCSCDPID(%q) = %d, want %d", content, got, want)
		}
	}
}

func TestProcessRunning(t *testing.T) {
	if !processRunning(os.Getpid()) {
		t.Fatal("own process reported as stopped")
	}
	if processRunning(1 << 30) {
		t.Fatal("nonexistent process reported as running")
	}
}
