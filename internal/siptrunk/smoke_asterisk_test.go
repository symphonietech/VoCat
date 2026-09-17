package siptrunk

import (
	"net"
	"strings"
	"testing"
	"time"
)

// A byte-for-byte qualify probe as Asterisk 20 sends it, including the headers
// the parser has no special handling for. This is the exchange that decides
// whether `pjsip show endpoint vocat` reads Avail.
func TestServerAnswersAnAsteriskQualifyProbe(t *testing.T) {
	server := listenForTest(t, []string{"127.0.0.1"})
	conn, err := net.DialUDP("udp", nil, server.LocalAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	probe := "OPTIONS sip:vocat@127.0.0.1:5062 SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP 127.0.0.1:5060;rport;branch=z9hG4bKPj7a1b2c3d\r\n" +
		"From: <sip:asterisk@127.0.0.1>;tag=4f2e1a9b\r\n" +
		"To: <sip:vocat@127.0.0.1>\r\n" +
		"Contact: <sip:asterisk@127.0.0.1:5060>\r\n" +
		"Call-ID: 6d1c0b8a5e3f2d4c\r\n" +
		"CSeq: 39102 OPTIONS\r\n" +
		"Route: <sip:127.0.0.1:5062;lr>\r\n" +
		"Max-Forwards: 70\r\n" +
		"User-Agent: Asterisk PBX 20.5.0\r\n" +
		"Content-Length:  0\r\n\r\n"
	if _, err := conn.Write([]byte(probe)); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, maxMessageBytes)
	count, err := conn.Read(buffer)
	if err != nil {
		t.Fatalf("no response to a qualify probe: %v", err)
	}
	response := string(buffer[:count])

	if !strings.HasPrefix(response, "SIP/2.0 200 OK\r\n") {
		t.Fatalf("status line: %q", response)
	}
	// Asterisk matches the response to its transaction on branch and CSeq, and
	// drops it otherwise — which would leave the endpoint Unavail even though
	// VoCat replied.
	for _, want := range []string{
		"z9hG4bKPj7a1b2c3d",
		"CSeq: 39102 OPTIONS\r\n",
		"Call-ID: 6d1c0b8a5e3f2d4c\r\n",
		"From: <sip:asterisk@127.0.0.1>;tag=4f2e1a9b\r\n",
	} {
		if !strings.Contains(response, want) {
			t.Fatalf("response missing %q:\n%s", want, response)
		}
	}
	// The To tag is what marks this as an answer from us rather than an echo.
	if !strings.Contains(response, "To: <sip:vocat@127.0.0.1>;tag=") {
		t.Fatalf("no To tag added:\n%s", response)
	}
}
