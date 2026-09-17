package siptrunk

import (
	"net"
	"strings"
	"testing"
)

// An offer as Asterisk 20 sends it, with the session connection line above the
// media section and several codecs in preference order.
const asteriskOffer = "v=0\r\n" +
	"o=- 1727000000 1727000000 IN IP4 192.168.31.50\r\n" +
	"s=Asterisk\r\n" +
	"c=IN IP4 192.168.31.50\r\n" +
	"t=0 0\r\n" +
	"m=audio 14002 RTP/AVP 8 0 101\r\n" +
	"a=rtpmap:8 PCMA/8000\r\n" +
	"a=rtpmap:0 PCMU/8000\r\n" +
	"a=rtpmap:101 telephone-event/8000\r\n" +
	"a=fmtp:101 0-16\r\n" +
	"a=ptime:20\r\n" +
	"a=maxptime:150\r\n" +
	"a=sendrecv\r\n"

func TestParseOfferReadsAddressPortAndPreferredCodec(t *testing.T) {
	offer, err := ParseOffer([]byte(asteriskOffer))
	if err != nil {
		t.Fatal(err)
	}
	if !offer.Address.Equal(net.ParseIP("192.168.31.50")) {
		t.Fatalf("address %v", offer.Address)
	}
	if offer.Port != 14002 {
		t.Fatalf("port %d", offer.Port)
	}
	// 8 is listed first, so PCMA is what the PBX prefers.
	if offer.Payload != payloadPCMA {
		t.Fatalf("payload %d, want PCMA", offer.Payload)
	}
	if offer.Direction != "sendrecv" {
		t.Fatalf("direction %q", offer.Direction)
	}
}

// A media-level connection line overrides the session-level one, which is how
// a PBX steers audio to a different interface from its signalling.
func TestParseOfferPrefersTheMediaConnectionLine(t *testing.T) {
	body := strings.Replace(asteriskOffer,
		"m=audio 14002 RTP/AVP 8 0 101\r\n",
		"m=audio 14002 RTP/AVP 8 0 101\r\nc=IN IP4 10.9.9.9\r\n", 1)
	offer, err := ParseOffer([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if !offer.Address.Equal(net.ParseIP("10.9.9.9")) {
		t.Fatalf("address %v, want the media-level one", offer.Address)
	}
}

func TestParseOfferTakesPCMUWhenListedFirst(t *testing.T) {
	body := strings.Replace(asteriskOffer, "m=audio 14002 RTP/AVP 8 0 101", "m=audio 14002 RTP/AVP 0 8 101", 1)
	offer, err := ParseOffer([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if offer.Payload != payloadPCMU {
		t.Fatalf("payload %d, want PCMU", offer.Payload)
	}
}

func TestParseOfferReadsHoldDirection(t *testing.T) {
	body := strings.Replace(asteriskOffer, "a=sendrecv", "a=sendonly", 1)
	offer, err := ParseOffer([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if offer.Direction != "sendonly" {
		t.Fatalf("direction %q", offer.Direction)
	}
}

// Transcoding is out of scope, so an offer VoCat cannot carry must be refused
// rather than answered with a codec neither side agreed to.
func TestParseOfferRejectsOffersItCannotCarry(t *testing.T) {
	for name, body := range map[string]string{
		"no G.711":              strings.Replace(asteriskOffer, "m=audio 14002 RTP/AVP 8 0 101", "m=audio 14002 RTP/AVP 9 101", 1),
		"no audio stream":       strings.Replace(asteriskOffer, "m=audio 14002", "m=video 14002", 1),
		"no connection address": strings.Replace(asteriskOffer, "c=IN IP4 192.168.31.50\r\n", "", 1),
		"empty":                 "",
	} {
		if offer, err := ParseOffer([]byte(body)); err == nil {
			t.Fatalf("%s: accepted as %+v", name, offer)
		}
	}
}

func TestBuildAnswerAgreesToASingleCodec(t *testing.T) {
	answer := string(BuildAnswer(net.ParseIP("192.168.31.203"), 16000, payloadPCMA))
	for _, want := range []string{
		"c=IN IP4 192.168.31.203\r\n",
		"m=audio 16000 RTP/AVP 8\r\n",
		"a=rtpmap:8 PCMA/8000\r\n",
		"a=ptime:20\r\n",
		"a=sendrecv\r\n",
	} {
		if !strings.Contains(answer, want) {
			t.Fatalf("missing %q in:\n%s", want, answer)
		}
	}
	// Exactly one payload in the m= line: a list would invite the PBX to pick
	// something else later.
	if strings.Contains(answer, "RTP/AVP 8 0") {
		t.Fatalf("answer offered more than one codec:\n%s", answer)
	}
}

func TestBuildAnswerHandlesIPv6(t *testing.T) {
	answer := string(BuildAnswer(net.ParseIP("fd00::1"), 16000, payloadPCMU))
	if !strings.Contains(answer, "c=IN IP6 fd00::1\r\n") || !strings.Contains(answer, "a=rtpmap:0 PCMU/8000") {
		t.Fatalf("IPv6 answer wrong:\n%s", answer)
	}
}
