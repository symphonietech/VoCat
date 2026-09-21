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
