package siptrunk

import (
	"net"
	"strings"
	"testing"
	"time"
)

func listenForTest(t *testing.T, peers []string) *Server {
	t.Helper()
	server, err := Listen(Options{Address: "127.0.0.1:0", Peers: peers})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func request(method string) string {
	return method + " sip:vocat@127.0.0.1 SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP 127.0.0.1:5060;branch=z9hG4bK" + method + "\r\n" +
		"From: <sip:asterisk@127.0.0.1>;tag=f\r\n" +
		"To: <sip:vocat@127.0.0.1>\r\n" +
		"Call-ID: c-" + method + "\r\n" +
		"CSeq: 1 " + method + "\r\n" +
		"Content-Length: 0\r\n\r\n"
}

// exchange sends one request and returns the response, or "" on timeout.
func exchange(t *testing.T, server *Server, packet string) string {
	t.Helper()
	conn, err := net.DialUDP("udp", nil, server.LocalAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(packet)); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, maxMessageBytes)
	count, err := conn.Read(buffer)
	if err != nil {
		return ""
	}
	return string(buffer[:count])
}

// Asterisk marks a trunk reachable by qualifying it with OPTIONS, so this is
// the exchange that decides whether the endpoint shows as Avail.
func TestServerAnswersOPTIONS(t *testing.T) {
	server := listenForTest(t, []string{"127.0.0.1"})
	response := exchange(t, server, request("OPTIONS"))
	if !strings.HasPrefix(response, "SIP/2.0 200 OK") {
		t.Fatalf("response %q", response)
	}
	if !strings.Contains(response, "z9hG4bKOPTIONS") || !strings.Contains(response, "CSeq: 1 OPTIONS") {
		t.Fatalf("routing headers not echoed: %q", response)
	}
}

// Call handling is not built yet. Answering 501 tells the PBX so, which is
// more useful to operate against than dropping the request.
func TestServerReportsCallMethodsUnimplemented(t *testing.T) {
	server := listenForTest(t, []string{"127.0.0.1"})
	if response := exchange(t, server, request("INVITE")); !strings.HasPrefix(response, "SIP/2.0 501") {
		t.Fatalf("INVITE response %q", response)
	}
	if response := exchange(t, server, request("SUBSCRIBE")); !strings.HasPrefix(response, "SIP/2.0 405") {
		t.Fatalf("SUBSCRIBE response %q", response)
	}
}

// An untrusted source must get silence, not a status: a reply would confirm
// something is listening.
func TestServerIgnoresUntrustedPeers(t *testing.T) {
	server := listenForTest(t, []string{"10.99.99.1"})
	if response := exchange(t, server, request("OPTIONS")); response != "" {
		t.Fatalf("answered an untrusted peer: %q", response)
	}
}

func TestServerIgnoresMalformedRequests(t *testing.T) {
	server := listenForTest(t, []string{"127.0.0.1"})
	if response := exchange(t, server, "this is not SIP\r\n\r\n"); response != "" {
		t.Fatalf("answered junk: %q", response)
	}
}

func TestTrustedMatchesPrefixesAndRejectsOthers(t *testing.T) {
	server := listenForTest(t, []string{"127.0.0.1", "10.8.0.0/24"})
	for _, allowed := range []string{"127.0.0.1", "10.8.0.1", "10.8.0.254"} {
		if !server.Trusted(net.ParseIP(allowed)) {
			t.Fatalf("%s should be trusted", allowed)
		}
	}
	for _, denied := range []string{"127.0.0.2", "10.8.1.1", "192.168.1.5"} {
		if server.Trusted(net.ParseIP(denied)) {
			t.Fatalf("%s should not be trusted", denied)
		}
	}
}

// A trunk with no peers would accept nothing; failing loudly beats starting a
// listener that silently drops every packet.
func TestListenRequiresPeers(t *testing.T) {
	if _, err := Listen(Options{Address: "127.0.0.1:0"}); err == nil {
		t.Fatal("started with no trusted peers")
	}
	if _, err := Listen(Options{Address: "127.0.0.1:0", Peers: []string{"not-an-ip"}}); err == nil {
		t.Fatal("accepted an invalid peer entry")
	}
}
