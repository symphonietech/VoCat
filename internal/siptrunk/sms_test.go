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
