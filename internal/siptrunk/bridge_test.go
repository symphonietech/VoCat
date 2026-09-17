package siptrunk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMedia stands in for the IMS RTP bridge: what the trunk writes to it is
// what the SIM would have sent, and what it returns is what the SIM heard.
type fakeMedia struct {
	inbound chan []int16

	mu      sync.Mutex
	written [][]int16
	closed  bool
}

func newFakeMedia() *fakeMedia {
	return &fakeMedia{inbound: make(chan []int16, 8)}
}

func (m *fakeMedia) ReadPCM(ctx context.Context) ([]int16, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case samples, ok := <-m.inbound:
		if !ok {
			return nil, io.EOF
		}
		return samples, nil
	}
}

func (m *fakeMedia) WritePCM(samples []int16) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return io.EOF
	}
	m.written = append(m.written, append([]int16(nil), samples...))
	return nil
}

func (m *fakeMedia) frames() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.written)
}

type fakeGateway struct {
	media *fakeMedia

	resolveErr error
	dialErr    error
	answerErr  error
	// answerAfter delays the answer so a test can look at the ringing state.
	answerAfter time.Duration

	mu       sync.Mutex
	hints    []string
	numbers  []string
	hangups  int
	answered bool
}

func newFakeGateway() *fakeGateway { return &fakeGateway{media: newFakeMedia()} }

func (g *fakeGateway) ResolveDevice(hint string) (string, error) {
	g.mu.Lock()
	g.hints = append(g.hints, hint)
	g.mu.Unlock()
	if g.resolveErr != nil {
		return "", g.resolveErr
	}
	if hint != "" {
		return hint, nil
	}
	return "SLOT1-1", nil
}

func (g *fakeGateway) Dial(_ context.Context, _, number string) (string, error) {
	g.mu.Lock()
	g.numbers = append(g.numbers, number)
	g.mu.Unlock()
	if g.dialErr != nil {
		return "", g.dialErr
	}
	return "ims-1", nil
}

func (g *fakeGateway) WaitAnswered(ctx context.Context, _, _ string) error {
	if g.answerErr != nil {
		return g.answerErr
	}
	if g.answerAfter > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(g.answerAfter):
		}
	}
	g.mu.Lock()
	g.answered = true
	g.mu.Unlock()
	return nil
}

func (g *fakeGateway) Media(context.Context, string, string) (Media, error) { return g.media, nil }

func (g *fakeGateway) Hangup(context.Context, string, string) error {
	g.mu.Lock()
	g.hangups++
	g.mu.Unlock()
	return nil
}

func (g *fakeGateway) hangupCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.hangups
}

func (g *fakeGateway) dialledNumbers() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.numbers...)
}

func (g *fakeGateway) deviceHints() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.hints...)
}

// peer is a minimal PBX: one socket that sends requests and collects whatever
// the trunk sends back, so a test can assert on the sequence of responses.
type peer struct {
	t    *testing.T
	conn *net.UDPConn
}

func newPeer(t *testing.T, server *Server) *peer {
	t.Helper()
	conn, err := net.DialUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, server.LocalAddr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &peer{t: t, conn: conn}
}

func (p *peer) send(message string) {
	p.t.Helper()
	if _, err := p.conn.Write([]byte(message)); err != nil {
		p.t.Fatal(err)
	}
}

// await reads until a message whose first line contains want arrives, so an
// interleaved retransmission does not fail an otherwise correct exchange.
func (p *peer) await(want string, timeout time.Duration) string {
	p.t.Helper()
	deadline := time.Now().Add(timeout)
	buffer := make([]byte, maxMessageBytes)
	for time.Now().Before(deadline) {
		if err := p.conn.SetReadDeadline(deadline); err != nil {
			p.t.Fatal(err)
		}
		count, err := p.conn.Read(buffer)
		if err != nil {
			break
		}
		message := string(buffer[:count])
		if first, _, _ := strings.Cut(message, "\r\n"); strings.Contains(first, want) {
			return message
		}
	}
	p.t.Fatalf("no %q within %s", want, timeout)
	return ""
}

func (p *peer) localPort() int { return p.conn.LocalAddr().(*net.UDPAddr).Port }

func inviteMessage(callID, uri string, rtpPort int, extra ...string) string {
	body := strings.Join([]string{
		"v=0",
		"o=- 1 1 IN IP4 127.0.0.1",
		"s=Asterisk",
		"c=IN IP4 127.0.0.1",
		"t=0 0",
		fmt.Sprintf("m=audio %d RTP/AVP 0 8 101", rtpPort),
		"a=rtpmap:0 PCMU/8000",
		"a=ptime:20",
		"",
	}, "\r\n")
	lines := []string{
		"INVITE " + uri + " SIP/2.0",
		"Via: SIP/2.0/UDP 127.0.0.1:5060;branch=z9hG4bK" + callID,
		"From: <sip:1001@127.0.0.1>;tag=caller",
		"To: <" + uri + ">",
		"Contact: <sip:asterisk@127.0.0.1:5060>",
		"Call-ID: " + callID,
		"CSeq: 1 INVITE",
		"Max-Forwards: 70",
	}
	lines = append(lines, extra...)
	lines = append(lines,
		"Content-Type: application/sdp",
		fmt.Sprintf("Content-Length: %d", len(body)),
		"", body)
	return strings.Join(lines, "\r\n")
}

func inDialogMessage(method, callID, toTag string) string {
	return strings.Join([]string{
		method + " sip:vocat@127.0.0.1 SIP/2.0",
		"Via: SIP/2.0/UDP 127.0.0.1:5060;branch=z9hG4bK" + method + callID,
		"From: <sip:1001@127.0.0.1>;tag=caller",
		"To: <sip:+15551234@127.0.0.1>;tag=" + toTag,
		"Call-ID: " + callID,
		"CSeq: 2 " + method,
		"Max-Forwards: 70",
		"Content-Length: 0",
		"", "",
	}, "\r\n")
}

func bridgeServer(t *testing.T, gateway Gateway) *Server {
	t.Helper()
	server, err := Listen(Options{Address: "127.0.0.1:0", Peers: []string{"127.0.0.1"}, Gateway: gateway})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func toTagOf(t *testing.T, message string) string {
	t.Helper()
	for _, line := range strings.Split(message, "\r\n") {
		if !strings.HasPrefix(strings.ToLower(line), "to:") {
			continue
		}
		_, tag, found := strings.Cut(line, ";tag=")
		if !found {
			t.Fatalf("no To tag in %q", line)
		}
		return strings.TrimSpace(tag)
	}
	t.Fatalf("no To header in %q", message)
	return ""
}

// The whole outbound path: Asterisk offers G.711, VoCat rings, answers with an
// SDP of its own, and audio crosses in both directions.
func TestBridgeAnswersAnInviteAndCarriesAudio(t *testing.T) {
	gateway := newFakeGateway()
	server := bridgeServer(t, gateway)
	pbx := newPeer(t, server)

	rtp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer rtp.Close()

	pbx.send(inviteMessage("call-a", "sip:+15551234@127.0.0.1", rtp.LocalAddr().(*net.UDPAddr).Port))
	pbx.await("100 Trying", 2*time.Second)
	pbx.await("180 Ringing", 2*time.Second)
	ok := pbx.await("200 OK", 2*time.Second)

	if numbers := gateway.dialledNumbers(); len(numbers) != 1 || numbers[0] != "+15551234" {
		t.Fatalf("dialled %v", numbers)
	}
	if !strings.Contains(ok, "m=audio ") || !strings.Contains(ok, "a=rtpmap:0 PCMU/8000") {
		t.Fatalf("answer has no PCMU media: %q", ok)
	}
	if !strings.Contains(ok, "Contact: <sip:vocat@127.0.0.1:") {
		t.Fatalf("answer has no Contact: %q", ok)
	}
	tag := toTagOf(t, ok)
	pbx.send(inDialogMessage("ACK", "call-a", tag))

	// PBX -> SIM: a G.711 frame on the RTP leg reaches the IMS media bridge.
	answerPort := 0
	for _, line := range strings.Split(ok, "\r\n") {
		if strings.HasPrefix(line, "m=audio ") {
			_, _ = fmt.Sscanf(line, "m=audio %d", &answerPort)
		}
	}
	if answerPort == 0 {
		t.Fatalf("no media port in %q", ok)
	}
	packet := make([]byte, 12+frameSamples)
	packet[0], packet[1] = 0x80, payloadPCMU
	for index := 12; index < len(packet); index++ {
		packet[index] = 0xFF // mu-law silence, which still proves the path.
	}
	target := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: answerPort}
	deadline := time.Now().Add(2 * time.Second)
	for gateway.media.frames() == 0 && time.Now().Before(deadline) {
		if _, err := rtp.WriteToUDP(packet, target); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if gateway.media.frames() == 0 {
		t.Fatal("no audio reached the IMS leg")
	}

	// SIM -> PBX: the trunk's transmit pump is isochronous, so RTP arrives
	// whether or not the IMS side has anything to say.
	gateway.media.inbound <- make([]int16, frameSamples)
	if err := rtp.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 2048)
	count, _, err := rtp.ReadFromUDP(buffer)
	if err != nil {
		t.Fatalf("no RTP from the trunk: %v", err)
	}
	if count < 12 || buffer[1]&0x7f != payloadPCMU {
		t.Fatalf("unexpected RTP %v", buffer[:min(count, 16)])
	}

	pbx.send(inDialogMessage("BYE", "call-a", tag))
	pbx.await("200 OK", 2*time.Second)
	waitFor(t, "the SIM leg to hang up", func() bool { return gateway.hangupCount() == 1 })
}

// A PBX that gives up before the answer sends CANCEL; the INVITE must then be
// finished with 487 and the SIM leg released, or the SIM stays off-hook.
func TestBridgeCancelEndsTheSIMLeg(t *testing.T) {
	gateway := newFakeGateway()
	gateway.answerAfter = 30 * time.Second
	server := bridgeServer(t, gateway)
	pbx := newPeer(t, server)

	pbx.send(inviteMessage("call-b", "sip:+15551234@127.0.0.1", 40000))
	pbx.await("180 Ringing", 2*time.Second)
	pbx.send(inDialogMessage("CANCEL", "call-b", ""))
	pbx.await("487 Request Terminated", 2*time.Second)
	waitFor(t, "the SIM leg to hang up", func() bool { return gateway.hangupCount() == 1 })
}

// The far end rejecting or never answering is not a trunk failure, so it must
// come back as a call-progress status rather than a 500.
func TestBridgeReportsAnUnansweredCall(t *testing.T) {
	gateway := newFakeGateway()
	gateway.answerErr = errors.New("busy")
	server := bridgeServer(t, gateway)
	pbx := newPeer(t, server)

	pbx.send(inviteMessage("call-c", "sip:+15551234@127.0.0.1", 40000))
	pbx.await("480 Temporarily Unavailable", 2*time.Second)
	waitFor(t, "the SIM leg to hang up", func() bool { return gateway.hangupCount() == 1 })
}

// A SIM the PBX named but VoCat does not have is a dial plan mistake, and 404
// is what tells Asterisk to try another route rather than retry this one.
func TestBridgeRejectsAnUnknownDevice(t *testing.T) {
	gateway := newFakeGateway()
	gateway.resolveErr = errors.New("no such device")
	server := bridgeServer(t, gateway)
	pbx := newPeer(t, server)

	pbx.send(inviteMessage("call-d", "sip:+15551234@127.0.0.1", 40000))
	pbx.await("404 Not Found", 2*time.Second)
}

// Both ways a dial plan can name a SIM. The header is what a PJSIP dialplan
// sets most easily; the URI parameter is what a direct dial string carries.
func TestBridgeReadsTheDeviceHint(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		uri    string
		extra  []string
		expect string
	}{
		{name: "header", uri: "sip:+15551234@127.0.0.1",
			extra: []string{"X-VoCat-Device: SLOT2-4"}, expect: "SLOT2-4"},
		{name: "uri parameter", uri: "sip:+15551234@127.0.0.1;device=SLOT1-2", expect: "SLOT1-2"},
		{name: "absent", uri: "sip:+15551234@127.0.0.1", expect: ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			gateway := newFakeGateway()
			gateway.answerErr = errors.New("stop here")
			server := bridgeServer(t, gateway)
			pbx := newPeer(t, server)
			pbx.send(inviteMessage("call-"+testCase.name, testCase.uri, 40000, testCase.extra...))
			pbx.await("480 Temporarily Unavailable", 2*time.Second)
			hints := gateway.deviceHints()
			if len(hints) != 1 || hints[0] != testCase.expect {
				t.Fatalf("device hints %v, want %q", hints, testCase.expect)
			}
		})
	}
}

// An offer with no G.711 is a codec disagreement, not a server fault: 488 is
// what makes Asterisk log the real reason.
func TestBridgeRefusesAnOfferWithoutG711(t *testing.T) {
	gateway := newFakeGateway()
	server := bridgeServer(t, gateway)
	pbx := newPeer(t, server)

	body := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=x\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
		"m=audio 40000 RTP/AVP 9\r\na=rtpmap:9 G722/8000\r\n"
	pbx.send(strings.Join([]string{
		"INVITE sip:+15551234@127.0.0.1 SIP/2.0",
		"Via: SIP/2.0/UDP 127.0.0.1:5060;branch=z9hG4bKg722",
		"From: <sip:1001@127.0.0.1>;tag=caller",
		"To: <sip:+15551234@127.0.0.1>",
		"Call-ID: call-g722",
		"CSeq: 1 INVITE",
		"Content-Type: application/sdp",
		fmt.Sprintf("Content-Length: %d", len(body)),
		"", body,
	}, "\r\n"))
	pbx.await("488 Not Acceptable Here", 2*time.Second)
	if numbers := gateway.dialledNumbers(); len(numbers) != 0 {
		t.Fatalf("a refused offer still dialled %v", numbers)
	}
}

// UDP loses packets, so a PBX retransmits its INVITE. Placing a second call on
// the retransmission would bill the SIM twice and ring the callee twice.
func TestBridgeTreatsARetransmittedInviteAsOneCall(t *testing.T) {
	gateway := newFakeGateway()
	gateway.answerAfter = 30 * time.Second
	server := bridgeServer(t, gateway)
	pbx := newPeer(t, server)

	message := inviteMessage("call-e", "sip:+15551234@127.0.0.1", 40000)
	pbx.send(message)
	pbx.await("180 Ringing", 2*time.Second)
	pbx.send(message)
	pbx.await("100 Trying", 2*time.Second)
	if numbers := gateway.dialledNumbers(); len(numbers) != 1 {
		t.Fatalf("dialled %v for one call", numbers)
	}
}

// Without a gateway the trunk is still a reachable peer, and must say so
// rather than accepting calls it cannot place.
func TestServerWithoutAGatewayRefusesCalls(t *testing.T) {
	server := listenForTest(t, []string{"127.0.0.1"})
	response := exchange(t, server, request("INVITE"))
	if !strings.HasPrefix(response, "SIP/2.0 501") {
		t.Fatalf("response %q", response)
	}
}

// A BYE for a call the trunk does not know must be answered, or the peer
// retransmits it for 32 seconds.
func TestBridgeAnswersAnUnknownBye(t *testing.T) {
	server := bridgeServer(t, newFakeGateway())
	pbx := newPeer(t, server)
	pbx.send(inDialogMessage("BYE", "call-missing", "x"))
	pbx.await("481", 2*time.Second)
}

func TestURIHelpers(t *testing.T) {
	for _, testCase := range []struct{ uri, user, device string }{
		{"sip:+15551234@host", "+15551234", ""},
		{"sip:1001@host;device=SLOT1-1", "1001", "SLOT1-1"},
		{"<sip:1001@host;device=SLOT2-4>", "1001", "SLOT2-4"},
		{"\"Alice\" <sip:alice@host>;tag=abc", "alice", ""},
		{"sip:host", "", ""},
	} {
		if got := uriUser(testCase.uri); got != testCase.user {
			t.Errorf("uriUser(%q) = %q, want %q", testCase.uri, got, testCase.user)
		}
		if got := uriParameter(testCase.uri, "device"); got != testCase.device {
			t.Errorf("uriParameter(%q) = %q, want %q", testCase.uri, got, testCase.device)
		}
	}
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
