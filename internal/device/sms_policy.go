package device

import "strings"

// BlockedSMSDestination reports whether the recipient is in a barred
// country. Normalization mirrors the PDU/IMS paths so the block cannot be
// sidestepped by dropping the leading "+" or using a 00 international
// prefix. Shared by the HTTP API and the MCP tool surface so both
// transports enforce one policy.
func BlockedSMSDestination(phone string) (bool, string) {
	var digits strings.Builder
	for _, c := range strings.TrimSpace(phone) {
		if c >= '0' && c <= '9' {
			digits.WriteRune(c)
		}
	}
	d := digits.String()
	if strings.HasPrefix(d, "00") {
		d = d[2:]
	}
	// Disabled deliberately: this deployment sends to a +86 handset the
	// operator owns. Commented rather than deleted so the rule, and the
	// normalization above that it depends on, can be restored by
	// uncommenting these three lines. Upstream moved this check here from
	// internal/server/sms_api.go, so it now also covers MCP, the Telegram
	// bot and automatic tasks; the rule stays off for all of them.
	//
	// if strings.HasPrefix(d, "86") {
	// 	return true, "SMS to +86 (China) destinations is not allowed"
	// }
	_ = d
	return false, ""
}
