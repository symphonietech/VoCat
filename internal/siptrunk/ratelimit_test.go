package siptrunk

import (
	"net"
	"strings"
	"testing"
	"time"
)

func TestResponseLimiterCapsAPeerPerSecond(t *testing.T) {
	var limiter responseLimiter
	start := time.Now()

	for index := 0; index < maxResponsesPerSecond; index++ {
		allowed, _ := limiter.allow("a:1", start)
		if !allowed {
			t.Fatalf("packet %d refused while under the cap", index)
		}
	}
	allowed, first := limiter.allow("a:1", start)
	if allowed || !first {
		t.Fatalf("over the cap: allowed=%v firstRefusal=%v; want false, true", allowed, first)
	}
	// Only the first refusal in a window reports itself, or the guard against
	// a flood of packets becomes a flood of log lines.
	if _, first = limiter.allow("a:1", start); first {
		t.Fatal("a second refusal in the same window reported itself again")
	}
	// The cap is per peer: one runaway must not silence a healthy one.
	if allowed, _ = limiter.allow("b:1", start); !allowed {
		t.Fatal("a different peer was caught by another peer's cap")
	}
	// A new window starts clean.
	if allowed, _ = limiter.allow("a:1", start.Add(time.Second)); !allowed {
		t.Fatal("the cap did not reset after a second")
	}
}

// The end-to-end property: however hard a peer pushes, the trunk's replies
// stay bounded. This is what turns a response loop from a busy core into
// something that stops on its own.
func TestServerStopsRespondingToARunawayPeer(t *testing.T) {
	server := listenForTest(t, []string{"127.0.0.1"})
	conn, err := net.DialUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, server.LocalAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	const sent = maxResponsesPerSecond * 4
	for index := 0; index < sent; index++ {
		if _, err := conn.Write([]byte(request("OPTIONS"))); err != nil {
			t.Fatal(err)
		}
	}
	received := 0
	buffer := make([]byte, maxMessageBytes)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		count, readErr := conn.Read(buffer)
		if readErr != nil {
			break
		}
		if strings.HasPrefix(string(buffer[:count]), "SIP/2.0 200") {
			received++
		}
	}
	// UDP may drop some of the flood, so the floor is loose; the ceiling is
	// the assertion that matters.
	if received > maxResponsesPerSecond {
		t.Fatalf("answered %d of %d; the per-second cap is %d", received, sent, maxResponsesPerSecond)
	}
	if received == 0 {
		t.Fatal("answered nothing at all; the cap should bound replies, not stop them")
	}
}
