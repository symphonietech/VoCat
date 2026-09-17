package siptrunk

import (
	"sync"
	"time"
)

// A trunk sees a handful of requests per call, plus one qualify a minute per
// peer. Tens per second is already far outside real traffic; hundreds is a
// loop. Two have happened here -- both times a response provoked a request
// that provoked the same response -- and each ran at several thousand packets
// a second until someone read the log.
//
// The specific bugs are fixed. This is the guard for the next one: a runaway
// becomes bounded and loud instead of a busy core and a gigabyte of identical
// log lines. It is deliberately well above anything legitimate, so it never
// shapes real traffic.
const maxResponsesPerSecond = 50

// responseLimiter caps outbound packets per peer per second. The whole window
// is discarded each second rather than aged per entry, which keeps it to one
// map and no eviction policy -- a trunk has a few peers, not a few thousand.
type responseLimiter struct {
	mu      sync.Mutex
	started time.Time
	counts  map[string]int
	warned  map[string]bool
}

// allow reports whether a packet to peer may be sent, and whether this is the
// first refusal in the current window -- so the caller logs the runaway once
// rather than adding to it.
func (l *responseLimiter) allow(peer string, now time.Time) (ok bool, firstRefusal bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts == nil || now.Sub(l.started) >= time.Second {
		l.started = now
		l.counts = make(map[string]int, len(l.counts))
		l.warned = make(map[string]bool, len(l.warned))
	}
	if l.counts[peer] >= maxResponsesPerSecond {
		if l.warned[peer] {
			return false, false
		}
		l.warned[peer] = true
		return false, true
	}
	l.counts[peer]++
	return true, false
}
