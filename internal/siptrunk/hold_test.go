package siptrunk

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// reinviteMessage renegotiates an established dialog: the same Call-ID and
// From tag, a higher CSeq, and a new media direction.
func reinviteMessage(callID, toTag string, cseq int, connection, direction string, rtpPort int) string {
	body := strings.Join([]string{
		"v=0",
		"o=- 1 2 IN IP4 127.0.0.1",
		"s=Asterisk",
		"c=IN IP4 " + connection,
		"t=0 0",
		fmt.Sprintf("m=audio %d RTP/AVP 0", rtpPort),
		"a=rtpmap:0 PCMU/8000",
		"a=" + direction,
		"",
	}, "\r\n")
	return strings.Join([]string{
		"INVITE sip:+15551234@127.0.0.1 SIP/2.0",
		fmt.Sprintf("Via: SIP/2.0/UDP 127.0.0.1:5060;branch=z9hG4bKre%d%s", cseq, callID),
		"From: <sip:1001@127.0.0.1>;tag=caller",
		"To: <sip:+15551234@127.0.0.1>;tag=" + toTag,
		"Contact: <sip:asterisk@127.0.0.1:5060>",
		"Call-ID: " + callID,
		fmt.Sprintf("CSeq: %d INVITE", cseq),
		"Max-Forwards: 70",
		"Content-Type: application/sdp",
		fmt.Sprintf("Content-Length: %d", len(body)),
		"", body,
	}, "\r\n")
}

// answeredBridge places one call through the trunk and returns the PBX side
// plus the To tag from the answer.
func answeredBridge(t *testing.T, callID string) (*Server, *peer, string) {
	t.Helper()
	server := bridgeServer(t, newFakeGateway())
	pbx := newPeer(t, server)
	pbx.send(inviteMessage(callID, "sip:+15551234@127.0.0.1", 40000))
	answer := pbx.await("200 OK", 3*time.Second)
	return server, pbx, toTagOf(t, answer)
}

// A re-INVITE carries a new CSeq, so replaying the original 200 answers a
// transaction the PBX does not have open. It retransmits, gives up, and tears
// the call down -- which made hold worse than unimplemented.
func TestReInviteIsAnsweredInItsOwnTransaction(t *testing.T) {
	server, pbx, toTag := answeredBridge(t, "hold-a")
	defer server.Close()

	pbx.send(reinviteMessage("hold-a", toTag, 2, "127.0.0.1", "sendonly", 40002))
	response := pbx.await("200 OK", 3*time.Second)
	if !strings.Contains(response, "CSeq: 2 INVITE") {
		t.Fatalf("the answer belongs to the wrong transaction:\n%s", response)
	}
	// RFC 3264 §6.1: a peer that says it will only send is told it will only
	// receive. Backwards here is how a held call comes back one-way.
	if !strings.Contains(response, "a=recvonly") {
		t.Fatalf("sendonly was not mirrored as recvonly:\n%s", response)
	}
}

// On hold the PBX's music has to reach the SIM, and the SIM's audio must not
// go back to a peer that said it is not listening.
func TestHoldStopsOnlyTheDirectionThePBXRefused(t *testing.T) {
	server, pbx, toTag := answeredBridge(t, "hold-b")
	defer server.Close()

	pbx.send(reinviteMessage("hold-b", toTag, 2, "127.0.0.1", "sendonly", 40002))
	pbx.await("200 OK", 3*time.Second)
	current := server.dialog("hold-b")
	if current == nil {
		t.Fatal("the dialog went away on hold")
	}
	if toPBX, toSIM := current.relayDirection(); toPBX || !toSIM {
		t.Fatalf("held: toPBX=%v toSIM=%v; want music through and nothing back", toPBX, toSIM)
	}

	pbx.send(reinviteMessage("hold-b", toTag, 3, "127.0.0.1", "sendrecv", 40002))
	response := pbx.await("200 OK", 3*time.Second)
	if !strings.Contains(response, "a=sendrecv") {
		t.Fatalf("resume was not answered sendrecv:\n%s", response)
	}
	if toPBX, toSIM := current.relayDirection(); !toPBX || !toSIM {
		t.Fatalf("resumed: toPBX=%v toSIM=%v; want both", toPBX, toSIM)
	}
}

// c=0.0.0.0 is how RFC 2543 held a call and plenty of equipment still does
// it. Refusing it as malformed would tear down a call that was only asking
// for silence.
func TestReInviteAcceptsTheOldStyleHold(t *testing.T) {
	server, pbx, toTag := answeredBridge(t, "hold-c")
	defer server.Close()

	pbx.send(reinviteMessage("hold-c", toTag, 2, "0.0.0.0", "sendrecv", 40002))
	response := pbx.await("200 OK", 3*time.Second)
	if !strings.Contains(response, "a=inactive") {
		t.Fatalf("c=0.0.0.0 was not treated as hold:\n%s", response)
	}
	if toPBX, toSIM := server.dialog("hold-c").relayDirection(); toPBX || toSIM {
		t.Fatalf("an inactive call still relays: toPBX=%v toSIM=%v", toPBX, toSIM)
	}
}

// A retransmitted INVITE repeats its CSeq, and must still be answered by
// replaying the original response: renegotiating on a lost answer would
// restart the media negotiation for no reason.
func TestRetransmittedInviteStillReplaysTheAnswer(t *testing.T) {
	server, pbx, _ := answeredBridge(t, "hold-d")
	defer server.Close()

	pbx.send(inviteMessage("hold-d", "sip:+15551234@127.0.0.1", 40000))
	response := pbx.await("200 OK", 3*time.Second)
	if !strings.Contains(response, "CSeq: 1 INVITE") {
		t.Fatalf("a retransmission was treated as a renegotiation:\n%s", response)
	}
}

// A media description the trunk cannot carry is refused rather than answered,
// and the call carries on with what it had.
func TestReInviteWithNoUsableCodecIsRefused(t *testing.T) {
	server, pbx, toTag := answeredBridge(t, "hold-e")
	defer server.Close()

	broken := strings.Replace(
		reinviteMessage("hold-e", toTag, 2, "127.0.0.1", "sendrecv", 40002),
		"m=audio 40002 RTP/AVP 0", "m=audio 40002 RTP/AVP 9", 1)
	pbx.send(broken)
	pbx.await("488 Not Acceptable Here", 3*time.Second)
	if server.dialog("hold-e") == nil {
		t.Fatal("a refused re-INVITE ended the call")
	}
}
