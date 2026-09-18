package siptrunk

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakePBX is an Asterisk that answers on loopback: it reads whatever the
// trunk sends it and replies with whatever the test wants.
type fakePBX struct {
	conn *net.UDPConn
}

func newFakePBX(t *testing.T) *fakePBX {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &fakePBX{conn: conn}
}

func (p *fakePBX) address() string { return p.conn.LocalAddr().String() }

// receive waits for one message. The deadline is generous: these run on
// loopback, so anything slower than this is a hang rather than a slow machine.
func (p *fakePBX) receive(t *testing.T) (string, *net.UDPAddr) {
	t.Helper()
	_ = p.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buffer := make([]byte, maxMessageBytes)
	count, from, err := p.conn.ReadFromUDP(buffer)
	if err != nil {
		t.Fatalf("the PBX received nothing: %v", err)
	}
	return string(buffer[:count]), from
}

// receiveMethod skips retransmissions of anything else until the wanted
// method arrives.
func (p *fakePBX) receiveMethod(t *testing.T, method string) (string, *net.UDPAddr) {
	t.Helper()
	for attempt := 0; attempt < 8; attempt++ {
		message, from := p.receive(t)
		if strings.HasPrefix(message, method+" ") {
			return message, from
		}
	}
	t.Fatalf("no %s arrived", method)
	return "", nil
}

func (p *fakePBX) send(t *testing.T, message string, to *net.UDPAddr) {
	t.Helper()
	if _, err := p.conn.WriteToUDP([]byte(message), to); err != nil {
		t.Fatal(err)
	}
}

// answer builds a 200 OK for an INVITE, echoing the headers RFC 3261 §8.2.6.2
// requires and carrying an SDP answer pointed at rtpPort.
func (p *fakePBX) answer(t *testing.T, invite string, rtpPort int) string {
	t.Helper()
	body := "v=0\r\n" +
		"o=- 1 1 IN IP4 127.0.0.1\r\n" +
		"s=Asterisk\r\n" +
		"c=IN IP4 127.0.0.1\r\n" +
		"t=0 0\r\n" +
		"m=audio " + strconv.Itoa(rtpPort) + " RTP/AVP 0\r\n" +
		"a=rtpmap:0 PCMU/8000\r\n" +
		"a=ptime:20\r\n"
	return p.respond(t, invite, "200 OK", "Contact: <sip:asterisk@"+p.address()+">\r\n"+
		"Content-Type: application/sdp\r\n", body)
}

func (p *fakePBX) respond(t *testing.T, request, status, extra, body string) string {
	t.Helper()
	var builder strings.Builder
	builder.WriteString("SIP/2.0 " + status + "\r\n")
	for _, line := range strings.Split(request, "\r\n") {
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "via:"), strings.HasPrefix(lower, "from:"),
			strings.HasPrefix(lower, "call-id:"), strings.HasPrefix(lower, "cseq:"):
			builder.WriteString(line + "\r\n")
		case strings.HasPrefix(lower, "to:"):
			if !strings.Contains(lower, ";tag=") {
				line += ";tag=pbx1234"
			}
			builder.WriteString(line + "\r\n")
		}
	}
	builder.WriteString(extra)
	builder.WriteString("Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n")
	builder.WriteString(body)
	return builder.String()
}

func inboundServer(t *testing.T, gateway Gateway, pbx string) *Server {
	t.Helper()
	server, err := Listen(Options{
		Address: "127.0.0.1:0", Peers: []string{"127.0.0.1"}, Gateway: gateway, PBX: pbx,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func sampleInbound() InboundCall {
	return InboundCall{
		DeviceID: "usb-2c7c-0125-3-4-4", DeviceName: "SLOT1-1",
		IMSCallID: "ims-1", Caller: "12125551234", Called: "13105557777",
	}
}

// The whole point of the inbound direction: a call on a SIM becomes an INVITE
// the PBX can route, carrying the caller, the dialled number and which SIM it
// arrived on.
func TestOfferSendsAnINVITEThePBXCanRoute(t *testing.T) {
	pbx := newFakePBX(t)
	server := inboundServer(t, newFakeGateway(), pbx.address())
	if err := server.Offer(sampleInbound()); err != nil {
		t.Fatal(err)
	}
	invite, _ := pbx.receiveMethod(t, "INVITE")

	if !strings.HasPrefix(invite, "INVITE sip:13105557777@") {
		t.Errorf("request URI does not carry the dialled number:\n%s", invite)
	}
	for _, want := range []string{
		"From: <sip:12125551234@",
		"X-VoCat-Device: usb-2c7c-0125-3-4-4",
		"X-VoCat-Device-Name: SLOT1-1",
		"CSeq: 1 INVITE",
		"Content-Type: application/sdp",
	} {
		if !strings.Contains(invite, want) {
			t.Errorf("INVITE is missing %q:\n%s", want, invite)
		}
	}
	// Both G.711 flavours, because the trunk is asking rather than agreeing
	// and which one a PBX prefers is its own configuration.
	if !strings.Contains(invite, "m=audio ") || !strings.Contains(invite, "RTP/AVP 0 8") {
		t.Errorf("INVITE does not offer both G.711 payloads:\n%s", invite)
	}
}

// Answering the SIM leg before the PBX picks up connects the caller to silence
// and bills them for it. The order is the behaviour, so it is asserted
// directly.
func TestOfferAnswersTheSIMOnlyAfterThePBXAnswers(t *testing.T) {
	pbx := newFakePBX(t)
	gateway := newFakeGateway()
	server := inboundServer(t, gateway, pbx.address())
	if err := server.Offer(sampleInbound()); err != nil {
		t.Fatal(err)
	}
	invite, from := pbx.receiveMethod(t, "INVITE")
	if gateway.acceptCount() != 0 {
		t.Fatal("the SIM leg was answered before the PBX had responded")
	}
	// Ringing changes nothing: the carrier is already giving the caller
	// ringback because the call has not been answered.
	pbx.send(t, pbx.respond(t, invite, "180 Ringing", "", ""), from)
	time.Sleep(50 * time.Millisecond)
	if gateway.acceptCount() != 0 {
		t.Fatal("the SIM leg was answered on a provisional response")
	}

	pbx.send(t, pbx.answer(t, invite, 24000), from)
	deadline := time.Now().Add(3 * time.Second)
	for gateway.acceptCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if gateway.acceptCount() != 1 {
		t.Fatal("the SIM leg was never answered after the PBX answered")
	}
	// A 2xx is confirmed by an ACK, or the PBX retransmits it for ever.
	ack, _ := pbx.receiveMethod(t, "ACK")
	if !strings.Contains(ack, "CSeq: 1 ACK") {
		t.Errorf("ACK does not match the INVITE transaction:\n%s", ack)
	}
	if !strings.Contains(ack, ";tag=pbx1234") {
		t.Errorf("ACK does not carry the PBX's To tag:\n%s", ack)
	}
}

// A PBX that refuses the call must not leave the caller ringing at the
// carrier: the SIM leg is released on every path, answered or not.
func TestOfferReleasesTheSIMWhenThePBXRejects(t *testing.T) {
	pbx := newFakePBX(t)
	gateway := newFakeGateway()
	server := inboundServer(t, gateway, pbx.address())
	if err := server.Offer(sampleInbound()); err != nil {
		t.Fatal(err)
	}
	invite, from := pbx.receiveMethod(t, "INVITE")
	pbx.send(t, pbx.respond(t, invite, "486 Busy Here", "", ""), from)

	deadline := time.Now().Add(3 * time.Second)
	for gateway.hangupCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if gateway.hangupCount() == 0 {
		t.Fatal("a rejected call left the SIM leg up")
	}
	if gateway.acceptCount() != 0 {
		t.Fatal("a rejected call answered the SIM leg")
	}
}

// A BYE from the PBX ends the SIM leg, and gets a 200 rather than the 481 an
// unknown dialog would.
func TestOfferEndsTheSIMWhenThePBXHangsUp(t *testing.T) {
	pbx := newFakePBX(t)
	gateway := newFakeGateway()
	server := inboundServer(t, gateway, pbx.address())
	if err := server.Offer(sampleInbound()); err != nil {
		t.Fatal(err)
	}
	invite, from := pbx.receiveMethod(t, "INVITE")
	pbx.send(t, pbx.answer(t, invite, 24002), from)
	pbx.receiveMethod(t, "ACK")

	callID := headerOf(t, invite, "Call-ID")
	bye := "BYE sip:vocat@" + server.LocalAddr().String() + " SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP " + pbx.address() + ";branch=z9hG4bKbye1\r\n" +
		"From: <sip:asterisk@" + pbx.address() + ">;tag=pbx1234\r\n" +
		"To: <sip:12125551234@vocat>;tag=" + tagOf(headerOf(t, invite, "From")) + "\r\n" +
		"Call-ID: " + callID + "\r\n" +
		"CSeq: 2 BYE\r\n" +
		"Content-Length: 0\r\n\r\n"
	pbx.send(t, bye, from)

	response, _ := pbx.receive(t)
	if !strings.HasPrefix(response, "SIP/2.0 200") {
		t.Fatalf("BYE was not accepted:\n%s", response)
	}
	deadline := time.Now().Add(3 * time.Second)
	for gateway.hangupCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if gateway.hangupCount() == 0 {
		t.Fatal("the SIM leg survived the PBX hanging up")
	}
}

// Inbound is opt-in. Without a PBX address VoCat must not send INVITEs at
// whatever happens to be listening on the usual port.
func TestOfferIsRefusedWithoutAPBXAddress(t *testing.T) {
	server := bridgeServer(t, newFakeGateway())
	if server.InboundEnabled() {
		t.Fatal("inbound reported as enabled with no PBX configured")
	}
	if err := server.Offer(sampleInbound()); err == nil {
		t.Fatal("a call was offered with nowhere to offer it")
	}
}

// A trunk with no gateway answers OPTIONS and refuses calls. Offering one
// would mean inviting the PBX to a call nothing can answer.
func TestOfferIsRefusedWithoutAGateway(t *testing.T) {
	pbx := newFakePBX(t)
	server, err := Listen(Options{Address: "127.0.0.1:0", Peers: []string{"127.0.0.1"}, PBX: pbx.address()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	if server.InboundEnabled() {
		t.Fatal("inbound reported as enabled with no gateway")
	}
	if err := server.Offer(sampleInbound()); err == nil {
		t.Fatal("a call was offered with nothing to answer it")
	}
}

// An INVITE that goes unanswered must be retransmitted: this is UDP, and the
// first datagram is the one most likely to be lost while the PBX is starting.
func TestOfferRetransmitsAnUnansweredINVITE(t *testing.T) {
	pbx := newFakePBX(t)
	server := inboundServer(t, newFakeGateway(), pbx.address())
	if err := server.Offer(sampleInbound()); err != nil {
		t.Fatal(err)
	}
	first, _ := pbx.receiveMethod(t, "INVITE")
	second, _ := pbx.receiveMethod(t, "INVITE")
	// Same transaction, not a second call: the branch and Call-ID are what
	// say so.
	if headerOf(t, first, "Call-ID") != headerOf(t, second, "Call-ID") {
		t.Fatal("the retransmission started a second call")
	}
	if headerOf(t, first, "Via") != headerOf(t, second, "Via") {
		t.Fatal("the retransmission used a different branch")
	}
}

func headerOf(t *testing.T, message, name string) string {
	t.Helper()
	for _, line := range strings.Split(message, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), strings.ToLower(name)+":") {
			_, value, _ := strings.Cut(line, ":")
			return strings.TrimSpace(value)
		}
	}
	t.Fatalf("no %s header in:\n%s", name, message)
	return ""
}

// The call is only real if audio crosses. This drives the whole path: offer,
// answer, ACK, then an RTP frame from the PBX that has to come out of the SIM
// side of the bridge.
func TestOfferBridgesAudioFromThePBXToTheSIM(t *testing.T) {
	pbx := newFakePBX(t)
	gateway := newFakeGateway()
	server := inboundServer(t, gateway, pbx.address())

	media, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	mediaPort := media.LocalAddr().(*net.UDPAddr).Port

	if err := server.Offer(sampleInbound()); err != nil {
		t.Fatal(err)
	}
	invite, from := pbx.receiveMethod(t, "INVITE")
	pbx.send(t, pbx.answer(t, invite, mediaPort), from)
	pbx.receiveMethod(t, "ACK")

	// Where the trunk asked for audio, which is what the RTP goes to.
	trunkRTP := sdpAudioPort(t, invite)

	// One 20 ms PCMU frame of silence. mu-law 0xff is linear zero, so the
	// content does not matter -- that it arrives at all does.
	packet := make([]byte, 12+frameSamples)
	packet[0], packet[1] = 0x80, payloadPCMU
	for index := 12; index < len(packet); index++ {
		packet[index] = 0xff
	}
	target := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: trunkRTP}
	deadline := time.Now().Add(3 * time.Second)
	for gateway.media.frames() == 0 && time.Now().Before(deadline) {
		if _, err := media.WriteToUDP(packet, target); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if gateway.media.frames() == 0 {
		t.Fatal("audio from the PBX never reached the SIM leg")
	}
}

func sdpAudioPort(t *testing.T, message string) int {
	t.Helper()
	for _, line := range strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n") {
		if !strings.HasPrefix(line, "m=audio ") {
			continue
		}
		fields := strings.Fields(line)
		port, err := strconv.Atoi(fields[1])
		if err != nil {
			t.Fatal(err)
		}
		return port
	}
	t.Fatalf("no m=audio line in:\n%s", message)
	return 0
}

// A lost ACK looks to the PBX exactly like a lost 200, so it sends the 200
// again. Nothing else reads responses once the call is up, and without an
// answer the PBX tears the call down at Timer H -- about thirty seconds in.
func TestOfferReACKsARetransmittedAnswer(t *testing.T) {
	pbx := newFakePBX(t)
	server := inboundServer(t, newFakeGateway(), pbx.address())
	if err := server.Offer(sampleInbound()); err != nil {
		t.Fatal(err)
	}
	invite, from := pbx.receiveMethod(t, "INVITE")
	answer := pbx.answer(t, invite, 24004)
	pbx.send(t, answer, from)
	first, _ := pbx.receiveMethod(t, "ACK")

	pbx.send(t, answer, from)
	second, _ := pbx.receiveMethod(t, "ACK")
	if first != second {
		t.Fatalf("the second ACK differs from the first:\n%s\n---\n%s", first, second)
	}
}
