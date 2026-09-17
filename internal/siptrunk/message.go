// Package siptrunk exposes VoCat's SIM-backed calling as an ordinary SIP
// gateway, so a PBX such as Asterisk can route calls through a modem's IMS
// registration. It is deliberately a trunk rather than a registrar: peers are
// authorised by source address, there are no user accounts, and every identity
// question is left to the PBX in front of it.
//
// This file is the server-side message layer. internal/vowifi/ims has its own
// SIP codec, but that one is a client speaking to a carrier's P-CSCF: it parses
// responses to requests it sent. A trunk has the opposite job — parse requests
// and build well-formed responses to them — so the two share a wire format
// rather than an implementation.
package siptrunk

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// maxMessageBytes bounds a single datagram. RFC 3261 allows SIP over UDP up to
// the path MTU; anything larger is either a fragmented mess or hostile.
const maxMessageBytes = 8192

// Request is an inbound SIP request. Headers keeps every occurrence of a header
// in arrival order, because Via ordering is what routes the response back.
type Request struct {
	Method  string
	URI     string
	Version string
	Headers []Header
	Body    []byte
}

// Header is one header line, with its name folded to lower case for lookup and
// its original spelling preserved for anything echoed back verbatim.
type Header struct {
	Name     string
	Original string
	Value    string
}

var errMalformed = errors.New("siptrunk: malformed SIP message")

// ParseRequest decodes a datagram into a Request. It accepts only requests: a
// trunk that received a response has either been sent something it never asked
// for, or is looking at a stray retransmission.
func ParseRequest(packet []byte) (*Request, error) {
	if len(packet) == 0 || len(packet) > maxMessageBytes {
		return nil, errMalformed
	}
	separator := []byte("\r\n\r\n")
	index := bytes.Index(packet, separator)
	size := 4
	if index < 0 {
		// Tolerate bare-LF framing: some stacks emit it, and rejecting the
		// message outright would look like an unreachable peer rather than a
		// formatting disagreement.
		if index = bytes.Index(packet, []byte("\n\n")); index < 0 {
			return nil, errMalformed
		}
		size = 2
	}
	lines := splitLines(packet[:index])
	if len(lines) == 0 {
		return nil, errMalformed
	}
	start := strings.Fields(lines[0])
	if len(start) != 3 || !strings.HasPrefix(start[2], "SIP/") {
		return nil, errMalformed
	}
	if strings.HasPrefix(lines[0], "SIP/") {
		return nil, errors.New("siptrunk: expected a request, got a response")
	}
	request := &Request{Method: strings.ToUpper(start[0]), URI: start[1], Version: start[2]}
	for _, line := range lines[1:] {
		colon := strings.Index(line, ":")
		if colon <= 0 {
			return nil, errMalformed
		}
		name := strings.TrimSpace(line[:colon])
		request.Headers = append(request.Headers, Header{
			Name:     strings.ToLower(name),
			Original: name,
			Value:    strings.TrimSpace(line[colon+1:]),
		})
	}
	body := packet[index+size:]
	// Content-Length is advisory here: trust the datagram boundary, but refuse a
	// message claiming more body than arrived so a truncated packet cannot be
	// mistaken for a complete one.
	if declared, ok := request.contentLength(); ok && declared > len(body) {
		return nil, errMalformed
	} else if ok {
		body = body[:declared]
	}
	request.Body = body
	return request, nil
}

// splitLines splits on CRLF or bare LF and unfolds continuation lines, which
// RFC 3261 §7.3.1 allows to wrap a header value onto following whitespace.
func splitLines(block []byte) []string {
	raw := strings.Split(strings.ReplaceAll(string(block), "\r\n", "\n"), "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		if line == "" {
			continue
		}
		if (line[0] == ' ' || line[0] == '\t') && len(lines) > 0 {
			lines[len(lines)-1] += " " + strings.TrimSpace(line)
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// Value returns the first value for a header name, accepting the compact form
// RFC 3261 §7.3.3 defines for the headers a trunk actually reads.
func (r *Request) Value(name string) string {
	for _, value := range r.Values(name) {
		return value
	}
	return ""
}

var compactForms = map[string]string{
	"via": "v", "from": "f", "to": "t", "call-id": "i",
	"contact": "m", "content-type": "c", "content-length": "l",
}

// Values returns every value for a header name, in arrival order.
func (r *Request) Values(name string) []string {
	name = strings.ToLower(name)
	compact := compactForms[name]
	var result []string
	for _, header := range r.Headers {
		if header.Name == name || (compact != "" && header.Name == compact) {
			result = append(result, header.Value)
		}
	}
	return result
}

func (r *Request) contentLength() (int, bool) {
	value := r.Value("content-length")
	if value == "" {
		return 0, false
	}
	size, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || size < 0 || size > maxMessageBytes {
		return 0, false
	}
	return size, true
}

// BuildResponse renders a response to request. Per RFC 3261 §8.2.6.2 the Via,
// From, Call-ID and CSeq headers are copied from the request unchanged, and
// every Via is copied in order so the response retraces the path it came by.
// A tag is appended to To when the request has none, which is what lets the
// peer match the response to its transaction. Each extraHeader is a complete
// "Name: value" line, for the Contact a 2xx to an INVITE must carry so the
// peer knows where to send its ACK and its BYE.
func BuildResponse(request *Request, status int, reason string, toTag string, body []byte, extraHeaders ...string) ([]byte, error) {
	if request == nil {
		return nil, errors.New("siptrunk: no request to respond to")
	}
	if status < 100 || status > 699 {
		return nil, fmt.Errorf("siptrunk: invalid status %d", status)
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "SIP/2.0 %d %s\r\n", status, reason)
	for _, via := range request.Values("via") {
		fmt.Fprintf(&builder, "Via: %s\r\n", via)
	}
	fmt.Fprintf(&builder, "From: %s\r\n", request.Value("from"))
	to := request.Value("to")
	if toTag != "" && !strings.Contains(strings.ToLower(to), ";tag=") {
		to += ";tag=" + toTag
	}
	fmt.Fprintf(&builder, "To: %s\r\n", to)
	fmt.Fprintf(&builder, "Call-ID: %s\r\n", request.Value("call-id"))
	fmt.Fprintf(&builder, "CSeq: %s\r\n", request.Value("cseq"))
	for _, header := range extraHeaders {
		if header == "" {
			continue
		}
		builder.WriteString(header + "\r\n")
	}
	if len(body) > 0 {
		builder.WriteString("Content-Type: application/sdp\r\n")
	}
	fmt.Fprintf(&builder, "Content-Length: %d\r\n\r\n", len(body))
	return append([]byte(builder.String()), body...), nil
}
