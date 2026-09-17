package siptrunk

import (
	"strings"
	"testing"
)

const optionsRequest = "OPTIONS sip:vocat@127.0.0.1 SIP/2.0\r\n" +
	"Via: SIP/2.0/UDP 127.0.0.1:5060;branch=z9hG4bK1a2b3c;rport\r\n" +
	"Via: SIP/2.0/UDP 10.0.0.9:5060;branch=z9hG4bKouter\r\n" +
	"From: <sip:asterisk@127.0.0.1>;tag=fromtag\r\n" +
	"To: <sip:vocat@127.0.0.1>\r\n" +
	"Call-ID: 6c4b8f2e@asterisk\r\n" +
	"CSeq: 102 OPTIONS\r\n" +
	"Content-Length: 0\r\n\r\n"

func TestParseRequestReadsStartLineAndHeaders(t *testing.T) {
	request, err := ParseRequest([]byte(optionsRequest))
	if err != nil {
		t.Fatal(err)
	}
	if request.Method != "OPTIONS" {
		t.Fatalf("method %q", request.Method)
	}
	if request.URI != "sip:vocat@127.0.0.1" {
		t.Fatalf("uri %q", request.URI)
	}
	if got := request.Values("via"); len(got) != 2 || !strings.Contains(got[0], "z9hG4bK1a2b3c") {
		t.Fatalf("via headers %q", got)
	}
	if request.Value("call-id") != "6c4b8f2e@asterisk" {
		t.Fatalf("call-id %q", request.Value("call-id"))
	}
	if len(request.Body) != 0 {
		t.Fatalf("body %q", request.Body)
	}
}

func TestParseRequestAcceptsCompactHeadersAndFolding(t *testing.T) {
	packet := "INVITE sip:639171234567@127.0.0.1 SIP/2.0\r\n" +
		"v: SIP/2.0/UDP 127.0.0.1:5060;branch=z9hG4bKabc\r\n" +
		"f: <sip:1001@127.0.0.1>;tag=a\r\n" +
		"t: <sip:639171234567@127.0.0.1>\r\n" +
		"i: call-42\r\n" +
		"CSeq: 1 INVITE\r\n" +
		"Subject: a value that\r\n continues here\r\n" +
		"l: 3\r\n\r\nabc"
	request, err := ParseRequest([]byte(packet))
	if err != nil {
		t.Fatal(err)
	}
	if request.Value("via") == "" || request.Value("from") == "" || request.Value("call-id") != "call-42" {
		t.Fatalf("compact forms not resolved: %+v", request.Headers)
	}
	if request.Value("subject") != "a value that continues here" {
		t.Fatalf("folded header %q", request.Value("subject"))
	}
	if string(request.Body) != "abc" {
		t.Fatalf("body %q", request.Body)
	}
}

// A datagram that claims more body than it carries must be rejected rather than
// silently treated as a shorter, complete message.
func TestParseRequestRejectsTruncatedBody(t *testing.T) {
	packet := "INVITE sip:x@127.0.0.1 SIP/2.0\r\nCSeq: 1 INVITE\r\nContent-Length: 500\r\n\r\nshort"
	if _, err := ParseRequest([]byte(packet)); err == nil {
		t.Fatal("accepted a message whose body was shorter than Content-Length")
	}
}

func TestParseRequestRejectsResponsesAndJunk(t *testing.T) {
	for name, packet := range map[string]string{
		"response":          "SIP/2.0 200 OK\r\nCSeq: 1 OPTIONS\r\n\r\n",
		"empty":             "",
		"no body separator": "OPTIONS sip:x SIP/2.0\r\nCSeq: 1 OPTIONS\r\n",
		"bad start line":    "NOT-SIP\r\n\r\n",
		"oversized":         strings.Repeat("A", maxMessageBytes+1),
	} {
		if _, err := ParseRequest([]byte(packet)); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func TestBuildResponseEchoesRoutingHeadersInOrder(t *testing.T) {
	request, err := ParseRequest([]byte(optionsRequest))
	if err != nil {
		t.Fatal(err)
	}
	response, err := BuildResponse(request, 200, "OK", "vocattag", nil)
	if err != nil {
		t.Fatal(err)
	}
	text := string(response)
	if !strings.HasPrefix(text, "SIP/2.0 200 OK\r\n") {
		t.Fatalf("status line: %q", text)
	}
	// Both Via headers, in the order they arrived: the response has to retrace
	// the request's path hop by hop.
	first := strings.Index(text, "z9hG4bK1a2b3c")
	second := strings.Index(text, "z9hG4bKouter")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("Via headers missing or reordered: %q", text)
	}
	for _, want := range []string{
		"From: <sip:asterisk@127.0.0.1>;tag=fromtag\r\n",
		"To: <sip:vocat@127.0.0.1>;tag=vocattag\r\n",
		"Call-ID: 6c4b8f2e@asterisk\r\n",
		"CSeq: 102 OPTIONS\r\n",
		"Content-Length: 0\r\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
}

// A To header that already carries a tag belongs to an established dialog;
// appending a second tag would break matching at the peer.
func TestBuildResponseKeepsAnExistingToTag(t *testing.T) {
	packet := strings.Replace(optionsRequest,
		"To: <sip:vocat@127.0.0.1>\r\n", "To: <sip:vocat@127.0.0.1>;tag=existing\r\n", 1)
	request, err := ParseRequest([]byte(packet))
	if err != nil {
		t.Fatal(err)
	}
	response, err := BuildResponse(request, 200, "OK", "vocattag", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(response), "vocattag") {
		t.Fatalf("overwrote an established dialog tag: %q", response)
	}
}

func TestBuildResponseCarriesSDP(t *testing.T) {
	request, err := ParseRequest([]byte(optionsRequest))
	if err != nil {
		t.Fatal(err)
	}
	response, err := BuildResponse(request, 200, "OK", "t", []byte("v=0\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(response)
	if !strings.Contains(text, "Content-Type: application/sdp\r\n") ||
		!strings.Contains(text, "Content-Length: 5\r\n") ||
		!strings.HasSuffix(text, "\r\n\r\nv=0\r\n") {
		t.Fatalf("SDP not carried correctly: %q", text)
	}
}
