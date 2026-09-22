package siptrunk

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Messenger is the SMS half of a gateway, kept separate from Gateway because a
// deployment can carry calls without carrying texts: the trunk asks for it by
// type assertion and simply answers 405 when it is absent.
type Messenger interface {
	// SendSMS submits a message through the SIM whose own number is sender.
	// It returns ErrSMSSenderUnknown when no SIM has that number, which is
	// the rule that decides whether an extension may send at all.
	SendSMS(ctx context.Context, sender, recipient, body string) error
}

var (
	// ErrSMSSenderUnknown means no SIM carries the number the extension
	// claimed, so there is nothing to send through and nothing to bill.
	ErrSMSSenderUnknown = errors.New("siptrunk: no SIM has the sending number")
	// ErrSMSBadRecipient means the destination cannot be encoded into a
	// submission, which is the caller's mistake rather than the SIM's.
	ErrSMSBadRecipient = errors.New("siptrunk: the recipient is not a usable number")
)

const (
	// smsContentType is what the body of a forwarded SMS is. The charset is
	// not optional: an SMS is frequently not ASCII, and Content-Length counts
	// bytes rather than characters.
	smsContentType = "text/plain;charset=UTF-8"
	// maxSMSBody caps what will be put in one MESSAGE. A concatenated SMS is
	// a few hundred characters; this is generous and exists so a malformed
	// row cannot produce a datagram nothing will accept.
	maxSMSBody = 4096
)

// OutboundSMS is one message to hand to a PBX.
type OutboundSMS struct {
	// Recipient is the extension to deliver to, and becomes the user part of
	// the request URI. For the inbound direction this is the SIM's own
	// number, so the extension named after it receives the message.
	Recipient string
	// Sender is who sent the SMS, as the network reported it.
	Sender string
	Body   string
	// DeviceID names the SIM, carried in X-VoCat-Device so a dial plan can
	// route by it the same way a call can.
	DeviceID   string
	DeviceName string
}

// SendSMS delivers one message to the PBX as a SIP MESSAGE.
//
// Out of dialog, so there is no ACK and nothing to tear down: one request, one
// final response. It blocks until the PBX answers or Timer F expires, because
// the caller records the outcome against the stored message.
func (s *Server) SendSMS(ctx context.Context, message OutboundSMS) error {
	if s.pbx == nil {
		return ErrInboundDisabled
	}
	recipient := strings.TrimSpace(message.Recipient)
	if recipient == "" {
		return errors.New("siptrunk: a forwarded SMS needs a recipient")
	}
	body := message.Body
	if len(body) > maxSMSBody {
		body = body[:maxSMSBody]
	}
	select {
	case <-s.closing:
		return errors.New("siptrunk: shutting down")
	default:
	}

	callID := newCallID()
	waiter := make(chan *Response, 4)
	s.watchSMS(callID, waiter)
	defer s.forgetSMS(callID)

	packet := s.buildMESSAGE(callID, recipient, message, []byte(body))
	if !s.send(packet, s.pbx) {
		return errors.New("siptrunk: the message was not sent")
	}

	// RFC 3261 §17.1.2.2: a non-INVITE client transaction retransmits over
	// UDP from T1, doubling to T2, and gives up at Timer F.
	timer, interval := time.NewTimer(timerT1), timerT1
	defer timer.Stop()
	deadline := time.NewTimer(64 * timerT1)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.closing:
			return errors.New("siptrunk: shutting down")
		case <-deadline.C:
			return fmt.Errorf("siptrunk: the PBX did not answer a MESSAGE for %s", recipient)
		case <-timer.C:
			if interval *= 2; interval > timerT2 {
				interval = timerT2
			}
			timer.Reset(interval)
			s.send(packet, s.pbx)
		case response := <-waiter:
			if response.Status < 200 {
				// A provisional to a MESSAGE is unusual but legal, and it
				// means the peer is alive: stop retransmitting and wait.
				timer.Stop()
				continue
			}
			if response.Status >= 300 {
				return fmt.Errorf("siptrunk: the PBX answered %d %s",
					response.Status, response.Reason)
			}
			return nil
		}
	}
}

func (s *Server) buildMESSAGE(callID, recipient string, message OutboundSMS, body []byte) []byte {
	target := "sip:" + recipient + "@" + s.pbx.String()
	headers := []string{
		"MESSAGE " + target + " SIP/2.0",
		"Via: SIP/2.0/UDP " + s.viaHost(s.pbx) + ";branch=z9hG4bK" + newTag(),
		"Max-Forwards: 70",
		"From: " + smsFromHeader(message.Sender) + ";tag=" + newTag(),
		"To: <" + target + ">",
		"Call-ID: " + callID,
		"CSeq: 1 MESSAGE",
		"X-VoCat-Device: " + message.DeviceID,
		"User-Agent: VoCat",
		"Content-Type: " + smsContentType,
		"Content-Length: " + strconv.Itoa(len(body)),
		"",
	}
	if name := strings.TrimSpace(message.DeviceName); name != "" {
		headers = insertHeader(headers, "X-VoCat-Device-Name: "+name)
	}
	return append([]byte(strings.Join(headers, "\r\n")+"\r\n"), body...)
}

// smsFromHeader renders the sender so the handset can see who texted.
//
// A numeric sender becomes the user part, which is what a phone matches
// against its address book. An alphanumeric sender ID -- a bank, a carrier,
// "支付宝" -- is not a legal SIP user part, and sanitising it away would
// destroy the one piece of information the recipient needs, so it goes in the
// display name instead. Most softphones show that.
func smsFromHeader(sender string) string {
	sender = strings.TrimSpace(sender)
	if sender == "" {
		return "<sip:sms@vocat>"
	}
	if user := smsUserPart(sender); user != "" {
		return "<sip:" + user + "@vocat>"
	}
	return `"` + quoteDisplayName(sender) + `" <sip:sms@vocat>`
}

// smsUserPart returns the sender as a URI user part, or "" when it cannot be
// one. Deliberately narrow: a number, optionally in E.164.
func smsUserPart(sender string) string {
	trimmed := strings.TrimPrefix(sender, "+")
	if trimmed == "" {
		return ""
	}
	for _, value := range trimmed {
		if value < '0' || value > '9' {
			return ""
		}
	}
	return sender
}

// quoteDisplayName escapes what a quoted string cannot hold verbatim. A
// newline would end the header and start a new one, which is how a sender name
// becomes an injected header.
func quoteDisplayName(value string) string {
	var builder strings.Builder
	for _, symbol := range value {
		switch symbol {
		case '"', '\\':
			builder.WriteByte('\\')
			builder.WriteRune(symbol)
		case '\r', '\n':
			builder.WriteByte(' ')
		default:
			builder.WriteRune(symbol)
		}
	}
	return builder.String()
}

func (s *Server) watchSMS(callID string, waiter chan *Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.smsWaiters == nil {
		s.smsWaiters = map[string]chan *Response{}
	}
	s.smsWaiters[callID] = waiter
}

func (s *Server) forgetSMS(callID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.smsWaiters, callID)
}

// deliverSMSResponse hands a response to whichever SendSMS is waiting for it.
// It reports whether one was, so the caller can fall through for a response
// that belongs to a call instead.
func (s *Server) deliverSMSResponse(response *Response) bool {
	s.mu.Lock()
	waiter := s.smsWaiters[response.Value("call-id")]
	s.mu.Unlock()
	if waiter == nil {
		return false
	}
	select {
	case waiter <- response:
	default:
	}
	return true
}

// handleMessage takes an inbound SIP MESSAGE: a text an extension is sending
// out through its own SIM.
//
// The sender is the From user, which the generated dial plan sets from the
// endpoint Asterisk authenticated rather than from anything the handset
// claimed. VoCat then applies the rule that decides everything here: the
// sender must be the number of a SIM it hosts. The recipient is not
// constrained -- it is an ordinary destination out on the network, which is
// the entire point of sending.
func (s *Server) handleMessage(request *Request, from *net.UDPAddr) bool {
	messenger, ok := s.gateway.(Messenger)
	if !ok {
		s.reply(request, from, 405, "Method Not Allowed", nil)
		return true
	}
	contentType := strings.ToLower(request.Value("content-type"))
	if contentType != "" && !strings.HasPrefix(contentType, "text/plain") {
		// 415 names the problem, where a 400 would leave the peer guessing
		// whether it was the body or the address.
		s.reply(request, from, 415, "Unsupported Media Type", nil)
		return true
	}
	sender := uriUser(request.Value("from"))
	recipient := uriUser(request.URI)
	if recipient == "" {
		recipient = uriUser(request.Value("to"))
	}
	if sender == "" || recipient == "" {
		s.reply(request, from, 400, "Bad Request", nil)
		return true
	}
	body := strings.TrimRight(string(request.Body), "\r\n")
	if strings.TrimSpace(body) == "" {
		s.reply(request, from, 400, "Bad Request", nil)
		return true
	}

	ctx, cancel := context.WithTimeout(context.Background(), smsSubmitTimeout)
	defer cancel()
	switch err := messenger.SendSMS(ctx, sender, recipient, body); {
	case err == nil:
		// 202, not 200: VoCat has accepted the message for delivery. The SIM
		// submission and the carrier's own delivery both happen after this
		// response is already on the wire.
		s.reply(request, from, 202, "Accepted", nil)
	case errors.Is(err, ErrSMSSenderUnknown):
		// 403 rather than 404: the destination exists, this sender may not
		// use it. Saying "not found" would send someone looking for a
		// misrouted request instead of a misnamed extension.
		// The error carries which SIM numbers VoCat does know, which is the
		// whole diagnosis: without it the line says only that the match
		// failed, and the two causes -- a number VoCat never learned, and a
		// number that differs -- are indistinguishable.
		s.log("siptrunk refused an SMS from an extension with no SIM",
			"sender", sender, "peer", from.String(), "error", err)
		s.reply(request, from, 403, "Forbidden", nil)
	case errors.Is(err, ErrSMSBadRecipient):
		s.reply(request, from, 400, "Bad Request", nil)
	default:
		// Recipient included because a send that fails for one destination
		// and not another is a different fault from one that fails for all,
		// and the sender alone cannot tell those apart.
		s.log("siptrunk could not send an SMS",
			"sender", sender, "recipient", recipient, "error", err)
		s.reply(request, from, 503, "Service Unavailable", nil)
	}
	return true
}

// smsSubmitTimeout bounds how long the PBX waits for its final response. A
// modem submission is slower than a dialplan expects, so this is generous, but
// it is still bounded: an unanswered MESSAGE leaves Asterisk retransmitting.
const smsSubmitTimeout = 20 * time.Second
