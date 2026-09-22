package siptrunk

import (
	"strings"
	"testing"
)

// An alphanumeric sender ID cannot be a SIP user part, and sanitising it away
// would destroy the one thing the recipient needs to know.
func TestSMSFromHeaderKeepsTheSender(t *testing.T) {
	cases := map[string]string{
		"+8613800138000": "<sip:+8613800138000@vocat>",
		"13800138000":    "<sip:13800138000@vocat>",
		"支付宝":            `"支付宝" <sip:sms@vocat>`,
		"HSBC":           `"HSBC" <sip:sms@vocat>`,
		"Amazon Web":     `"Amazon Web" <sip:sms@vocat>`,
		"":               "<sip:sms@vocat>",
	}
	for sender, want := range cases {
		if got := smsFromHeader(sender); got != want {
			t.Errorf("smsFromHeader(%q) = %q, want %q", sender, got, want)
		}
	}
}

// A newline in a sender name would end the header and start another one,
// which is how a sender name becomes an injected header.
func TestSMSFromHeaderCannotInjectAHeader(t *testing.T) {
	got := smsFromHeader("evil\r\nContact: <sip:attacker@example>")
	for _, forbidden := range []string{"\r", "\n"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("a line break survived: %q", got)
		}
	}
	if !strings.Contains(got, "Contact:") {
		t.Fatalf("the name was dropped rather than escaped: %q", got)
	}
	// A quote would end the display name early, so the inner ones are
	// escaped and only the wrapping pair is left bare.
	quoted := smsFromHeader(`say "hi"`)
	if !strings.Contains(quoted, `\"hi\"`) {
		t.Errorf("inner quotes were not escaped: %q", quoted)
	}
	if !strings.HasPrefix(quoted, `"say `) {
		t.Errorf("the display name is not quoted: %q", quoted)
	}
}

func TestSMSUserPartRejectsAnythingButANumber(t *testing.T) {
	for _, value := range []string{"+8613800138000", "13800138000", "10086"} {
		if smsUserPart(value) == "" {
			t.Errorf("%q was refused as a user part", value)
		}
	}
	for _, value := range []string{"支付宝", "HSBC", "+", "", "138-0013", "1380 0138"} {
		if smsUserPart(value) != "" {
			t.Errorf("%q was accepted as a user part", value)
		}
	}
}

// A softphone may percent-escape any user-part character, and several escape
// "+" as %2B even though RFC 3261 permits it bare. Left encoded it is not a
// number downstream: the SMS encoder rejects the "%" and the whole send fails
// with a message that blames the modem.
func TestURIUserDecodesPercentEscapes(t *testing.T) {
	cases := []struct{ uri, want string }{
		{"sip:%2B639524451636@127.0.0.1:5062", "+639524451636"},
		{"<sip:%2B639524451636@host>", "+639524451636"},
		{"sip:+639524451636@127.0.0.1:5062", "+639524451636"},
		{"sip:639524451636@127.0.0.1:5062", "639524451636"},
		// Parameters are still stripped, and stripped before decoding.
		{"sip:%2B63952@host;user=phone", "+63952"},
		// A malformed escape is left as-is rather than dropping the address.
		{"sip:%ZZ639@host", "%ZZ639"},
	}
	for _, test := range cases {
		if got := uriUser(test.uri); got != test.want {
			t.Fatalf("uriUser(%q) = %q, want %q", test.uri, got, test.want)
		}
	}
}

// A MESSAGE is a non-INVITE transaction, so the PBX retransmits from T1
// onwards until it holds a final response -- and a modem submission takes
// seconds. Without transaction state the retransmission was handled as a
// fresh message and the recipient got the text twice, about a second apart.
func TestRetransmittedMessageIsNotSubmittedTwice(t *testing.T) {
	server := &Server{}
	request := &Request{
		Method: "MESSAGE",
		URI:    "sip:+639524451636@127.0.0.1:5062",
		Headers: []Header{
			{Name: "via", Value: "SIP/2.0/UDP 127.0.0.1:5060;branch=z9hG4bK-one;rport"},
			{Name: "call-id", Value: "abc@127.0.0.1"},
			{Name: "cseq", Value: "1 MESSAGE"},
		},
	}
	key := smsTransactionKey(request)
	if key != "branch:z9hG4bK-one" {
		t.Fatalf("transaction key = %q", key)
	}

	// First arrival claims the transaction.
	if existing, claimed := server.beginSMSTransaction(key); !claimed || existing != nil {
		t.Fatal("the first arrival did not claim the transaction")
	}
	// The retransmission must not claim it, and while the submission is still
	// running there is no answer to repeat.
	existing, claimed := server.beginSMSTransaction(key)
	if claimed {
		t.Fatal("a retransmission claimed the transaction a second time")
	}
	if existing == nil || existing.done {
		t.Fatal("an in-flight transaction reported an answer it does not have")
	}

	// Once answered, a later retransmission repeats that same answer.
	server.finishSMSTransaction(key, 202, "Accepted")
	existing, claimed = server.beginSMSTransaction(key)
	if claimed {
		t.Fatal("a retransmission after the answer claimed the transaction")
	}
	if existing == nil || !existing.done || existing.code != 202 {
		t.Fatalf("recorded answer = %+v, want a done 202", existing)
	}
}

// A peer that omits an RFC 3261 branch still has to be told apart from the
// next submission, or two different texts would collapse into one.
func TestSMSTransactionKeyFallsBackWithoutABranch(t *testing.T) {
	build := func(via, callID, cseq string) *Request {
		return &Request{Method: "MESSAGE", Headers: []Header{
			{Name: "via", Value: via},
			{Name: "call-id", Value: callID},
			{Name: "cseq", Value: cseq},
		}}
	}
	first := smsTransactionKey(build("SIP/2.0/UDP 10.0.0.1:5060", "a@h", "1 MESSAGE"))
	second := smsTransactionKey(build("SIP/2.0/UDP 10.0.0.1:5060", "b@h", "1 MESSAGE"))
	third := smsTransactionKey(build("SIP/2.0/UDP 10.0.0.1:5060", "a@h", "2 MESSAGE"))
	if first == second || first == third || second == third {
		t.Fatalf("keys collided: %q %q %q", first, second, third)
	}
	if !strings.HasPrefix(first, "legacy:") {
		t.Fatalf("key without a branch = %q, want the legacy form", first)
	}
}
