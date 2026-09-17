package siptrunk

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// Media is the audio bridge the trunk needs from a call. It is declared here
// rather than imported so this package stays independent of VoCat's IMS types;
// vowifi.CallMedia satisfies it as it stands.
type Media interface {
	ReadPCM(context.Context) ([]int16, error)
	WritePCM([]int16) error
}

// Gateway is everything the trunk needs from VoCat's SIM-backed calling. The
// server implements it against the VoWiFi call controller; tests implement it
// with a fake, which is the point of it being an interface.
type Gateway interface {
	// ResolveDevice maps the hint a PBX sent -- an X-VoCat-Device header or a
	// device= URI parameter -- to a device ID. An empty hint asks the gateway
	// to pick, which is unambiguous only when one device is registered.
	ResolveDevice(hint string) (string, error)
	// Dial places a call and returns its ID as soon as the INVITE is away,
	// without waiting for an answer.
	Dial(ctx context.Context, deviceID, number string) (string, error)
	// WaitAnswered blocks until the call is active, fails, or ctx ends.
	WaitAnswered(ctx context.Context, deviceID, callID string) error
	// Media returns the RTP bridge for an active call.
	Media(ctx context.Context, deviceID, callID string) (Media, error)
	// Hangup ends the call. It is called on every teardown path, including
	// ones where the call never reached the network, so it tolerates an ID
	// that is already gone.
	Hangup(ctx context.Context, deviceID, callID string) error
}

const (
	// RFC 3261 §17: T1 is the round-trip estimate a 2xx retransmission backs
	// off from, T2 the ceiling, and 64*T1 the point at which an unacknowledged
	// 2xx means the peer is gone.
	timerT1 = 500 * time.Millisecond
	timerT2 = 4 * time.Second
	timerH  = 64 * timerT1
	// How long the far end gets to answer. Asterisk's own Dial() timeout is
	// usually shorter and sends CANCEL first; this is the backstop for when it
	// does not.
	answerTimeout = 90 * time.Second
)

// dialog is one call between the PBX and a SIM. The trunk is the UAS for it:
// the PBX sent the INVITE, so the local identity is the INVITE's To plus the
// tag generated here, and the remote identity is its From.
type dialog struct {
	server   *Server
	callID   string
	invite   *Request
	peer     *net.UDPAddr
	localTag string
	// target is the peer's Contact: where in-dialog requests are sent, which
	// is not necessarily where the INVITE came from.
	target string

	deviceID  string
	imsCallID string
	leg       *rtpLeg

	cancel context.CancelFunc
	done   chan struct{}
	finish sync.Once

	mu       sync.Mutex
	answer   []byte
	acked    bool
	answered bool
}

// dispatch handles the methods that carry their own responses. It reports
// whether it took the request; anything it declines falls through to route.
func (s *Server) dispatch(request *Request, from *net.UDPAddr) bool {
	// An ACK is answered by nothing, ever -- RFC 3261 §17.1.1.3 gives it no
	// response at all. This is checked before the gateway, because a response
	// to an ACK does not merely violate the spec: the peer answers it with
	// another ACK, and two processes on a loopback then trade packets as fast
	// as the kernel allows until someone notices the CPU. Taking the request
	// here is what keeps route() from replying to it.
	if request.Method == "ACK" {
		if s.gateway != nil {
			if current := s.dialog(request.Value("call-id")); current != nil {
				current.markACKed()
			}
		}
		return true
	}
	if s.gateway == nil {
		return false
	}
	switch request.Method {
	case "INVITE":
		return s.handleInvite(request, from)
	case "BYE":
		return s.handleBye(request, from)
	case "CANCEL":
		return s.handleCancel(request, from)
	}
	return false
}

func (s *Server) handleInvite(request *Request, from *net.UDPAddr) bool {
	callID := request.Value("call-id")
	if callID == "" {
		s.reply(request, from, 400, "Bad Request", nil)
		return true
	}
	if existing := s.dialog(callID); existing != nil {
		// A retransmitted INVITE. Repeating the last answer is correct and
		// placing a second call would not be.
		if answer := existing.currentAnswer(); answer != nil {
			s.send(answer, from)
		} else {
			s.reply(request, from, 100, "Trying", nil)
		}
		return true
	}
	offer, err := ParseOffer(request.Body)
	if err != nil {
		s.log("siptrunk rejected an INVITE", "call_id", callID, "error", err)
		// 488 is the specific "your media is not acceptable", which tells the
		// PBX to fix its codec list rather than to retry.
		s.reply(request, from, 488, "Not Acceptable Here", nil)
		return true
	}
	s.reply(request, from, 100, "Trying", nil)

	ctx, cancel := context.WithCancel(context.Background())
	current := &dialog{
		server:   s,
		callID:   callID,
		invite:   request,
		peer:     from,
		localTag: newTag(),
		target:   headerURI(request.Value("contact")),
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	s.mu.Lock()
	s.dialogs[callID] = current
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		current.run(ctx, offer)
	}()
	return true
}

func (s *Server) handleBye(request *Request, from *net.UDPAddr) bool {
	current := s.dialog(request.Value("call-id"))
	if current == nil {
		// RFC 3261 §12.2.2: a BYE outside a dialog is 481, which stops the
		// peer retransmitting it.
		s.reply(request, from, 481, "Call/Transaction Does Not Exist", nil)
		return true
	}
	s.reply(request, from, 200, "OK", nil)
	s.log("siptrunk call ended by the PBX", "call_id", current.callID)
	// The PBX has hung up, so no BYE goes back to it.
	current.teardown(false)
	return true
}

func (s *Server) handleCancel(request *Request, from *net.UDPAddr) bool {
	current := s.dialog(request.Value("call-id"))
	// A CANCEL is always answered 200 even when there is nothing to cancel:
	// the 487 that follows is what tells the peer the INVITE itself is over.
	s.reply(request, from, 200, "OK", nil)
	if current == nil {
		return true
	}
	if current.wasAnswered() {
		// Too late to cancel an answered INVITE; RFC 3261 §9.2 says the peer
		// must use BYE, and the 200 above is all it gets.
		return true
	}
	s.log("siptrunk call cancelled by the PBX", "call_id", current.callID)
	current.respondToInvite(487, "Request Terminated", nil)
	current.teardown(false)
	return true
}

func (s *Server) dialog(callID string) *dialog {
	if callID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dialogs[callID]
}

func (s *Server) forgetDialog(callID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.dialogs, callID)
}

func (s *Server) reply(request *Request, to *net.UDPAddr, status int, reason string, body []byte, extra ...string) {
	// RFC 3261 §8.2.6.2 exempts 100 Trying from the To tag a UAS otherwise
	// adds: the tag establishes a dialog, and a 100 establishes nothing.
	tag := newTag()
	if status == 100 {
		tag = ""
	}
	response, err := BuildResponse(request, status, reason, tag, body, extra...)
	if err != nil {
		s.log("siptrunk could not build a response", "error", err)
		return
	}
	s.send(response, to)
}

// run drives one outbound call from INVITE to answer. Every failure path sends
// a final response and tears down, because a PBX left waiting on a trunk that
// went quiet holds the caller on silence until its own timer fires.
func (d *dialog) run(ctx context.Context, offer mediaOffer) {
	defer d.teardown(true)

	number := uriUser(d.invite.URI)
	if number == "" {
		number = uriUser(d.invite.Value("to"))
	}
	if number == "" {
		d.fail(400, "Bad Request", errors.New("no dialled number in the request URI"))
		return
	}
	deviceID, err := d.server.gateway.ResolveDevice(d.deviceHint())
	if err != nil {
		// 404 rather than 500: the PBX asked for a SIM that is not here, which
		// is a routing mistake on its side.
		d.fail(404, "Not Found", err)
		return
	}
	d.deviceID = deviceID

	callID, err := d.server.gateway.Dial(ctx, deviceID, number)
	if err != nil {
		d.fail(503, "Service Unavailable", err)
		return
	}
	d.imsCallID = callID
	d.server.log("siptrunk placed a call",
		"call_id", d.callID, "device_id", deviceID, "number", number, "ims_call_id", callID)
	// 180 is what makes the caller hear ringback instead of silence.
	d.respondToInvite(180, "Ringing", nil)

	waitCtx, cancelWait := context.WithTimeout(ctx, answerTimeout)
	err = d.server.gateway.WaitAnswered(waitCtx, deviceID, callID)
	cancelWait()
	if err != nil {
		if ctx.Err() != nil {
			// Cancelled or torn down from elsewhere; that path sent its own
			// final response.
			return
		}
		// 480 covers the whole family of "nobody picked up": busy, rejected,
		// timed out. The reason phrase carries the detail into the PBX log.
		d.fail(480, "Temporarily Unavailable", err)
		return
	}

	media, err := d.server.gateway.Media(ctx, deviceID, callID)
	if err != nil {
		d.fail(500, "Server Internal Error", err)
		return
	}
	leg, err := newRTPLeg(d.server.mediaAddress(d.peer), &net.UDPAddr{IP: offer.Address, Port: offer.Port}, offer.Payload)
	if err != nil {
		d.fail(500, "Server Internal Error", err)
		return
	}
	d.leg = leg

	body := BuildAnswer(leg.conn.LocalAddr().(*net.UDPAddr).IP, leg.LocalPort(), offer.Payload)
	d.mu.Lock()
	d.answered = true
	d.mu.Unlock()
	d.respondToInvite(200, "OK", body)
	go d.retransmitAnswer()

	d.server.log("siptrunk bridged a call",
		"call_id", d.callID, "ims_call_id", callID, "rtp_port", leg.LocalPort())
	d.pump(ctx, media, leg)
}

// pump moves audio both ways until either leg stops. The first to fail ends
// the call: half a bridge is worse than none, because neither side can tell.
func (d *dialog) pump(ctx context.Context, media Media, leg *rtpLeg) {
	var once sync.Once
	stop := make(chan struct{})
	end := func() { once.Do(func() { close(stop) }) }

	go func() {
		defer end()
		for {
			samples, err := leg.ReadPCM(ctx)
			if err != nil {
				return
			}
			if err := media.WritePCM(samples); err != nil {
				return
			}
		}
	}()
	go func() {
		defer end()
		for {
			samples, err := media.ReadPCM(ctx)
			if err != nil {
				return
			}
			if err := leg.WritePCM(samples); err != nil {
				return
			}
		}
	}()

	select {
	case <-stop:
	case <-ctx.Done():
	case <-d.done:
	}
}

func (d *dialog) deviceHint() string {
	if hint := strings.TrimSpace(d.invite.Value("x-vocat-device")); hint != "" {
		return hint
	}
	return uriParameter(d.invite.URI, "device")
}

func (d *dialog) fail(status int, reason string, err error) {
	d.server.log("siptrunk call failed",
		"call_id", d.callID, "status", status, "error", err)
	d.respondToInvite(status, reason, nil)
}

// respondToInvite sends a response in the INVITE transaction, remembering the
// final one so a retransmitted INVITE is answered the same way twice.
func (d *dialog) respondToInvite(status int, reason string, body []byte) {
	response, err := BuildResponse(d.invite, status, reason, d.localTag, body, "Contact: <"+d.server.contactURI(d.peer)+">")
	if err != nil {
		d.server.log("siptrunk could not build a response", "call_id", d.callID, "error", err)
		return
	}
	if status >= 200 {
		d.mu.Lock()
		d.answer = response
		d.mu.Unlock()
	}
	d.server.send(response, d.peer)
}

func (d *dialog) currentAnswer() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.answer
}

func (d *dialog) wasAnswered() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.answered
}

func (d *dialog) markACKed() {
	d.mu.Lock()
	d.acked = true
	d.mu.Unlock()
}

// retransmitAnswer repeats the 200 OK until the ACK arrives. RFC 3261 §13.3.1.4
// puts this on the UAS rather than the transaction layer, because a lost 200 is
// indistinguishable from a peer that has gone away and only the ACK settles it.
func (d *dialog) retransmitAnswer() {
	interval := timerT1
	deadline := time.After(timerH)
	for {
		select {
		case <-d.done:
			return
		case <-deadline:
			d.server.log("siptrunk saw no ACK for its answer", "call_id", d.callID)
			d.teardown(true)
			return
		case <-time.After(interval):
		}
		d.mu.Lock()
		acked, answer := d.acked, d.answer
		d.mu.Unlock()
		if acked {
			return
		}
		if answer != nil {
			d.server.send(answer, d.peer)
		}
		if interval *= 2; interval > timerT2 {
			interval = timerT2
		}
	}
}

// teardown ends the dialog once. sendBye is false when the PBX is the side
// that hung up, since it is already gone and a BYE back would be answered 481.
func (d *dialog) teardown(sendBye bool) {
	d.finish.Do(func() {
		close(d.done)
		d.cancel()
		if sendBye && d.wasAnswered() {
			d.sendBye()
		}
		if d.leg != nil {
			_ = d.leg.Close()
		}
		if d.imsCallID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := d.server.gateway.Hangup(ctx, d.deviceID, d.imsCallID); err != nil {
				d.server.log("siptrunk could not hang up the SIM leg",
					"call_id", d.callID, "ims_call_id", d.imsCallID, "error", err)
			}
			cancel()
		}
		d.server.forgetDialog(d.callID)
		d.server.log("siptrunk released a call", "call_id", d.callID)
	})
}

// sendBye ends the PBX side of an answered call. The trunk is the UAS here, so
// the roles swap: its own identity comes from the INVITE's To plus the tag it
// chose, and the peer's from the INVITE's From.
func (d *dialog) sendBye() {
	target := d.target
	if target == "" {
		target = d.invite.URI
	}
	local := d.invite.Value("to")
	if !strings.Contains(strings.ToLower(local), ";tag=") {
		local += ";tag=" + d.localTag
	}
	contact := d.server.contactURI(d.peer)
	via := fmt.Sprintf("SIP/2.0/UDP %s;branch=z9hG4bK%s", d.server.viaHost(d.peer), newTag())
	message := strings.Join([]string{
		"BYE " + target + " SIP/2.0",
		"Via: " + via,
		"Max-Forwards: 70",
		"From: " + local,
		"To: " + d.invite.Value("from"),
		"Call-ID: " + d.callID,
		"CSeq: 1 BYE",
		"Contact: <" + contact + ">",
		"User-Agent: VoCat",
		"Content-Length: 0",
		"", "",
	}, "\r\n")
	d.server.send([]byte(message), d.peer)
}

// uriUser extracts the user part of a SIP URI, accepting the "Name <uri>" form
// headers use and stripping any URI parameters.
func uriUser(value string) string {
	uri := headerURI(value)
	if index := strings.Index(uri, ":"); index >= 0 {
		uri = uri[index+1:]
	}
	at := strings.Index(uri, "@")
	if at < 0 {
		return ""
	}
	user := uri[:at]
	if index := strings.IndexAny(user, ";?"); index >= 0 {
		user = user[:index]
	}
	return user
}

// uriParameter reads a ;name=value parameter from a URI. It deliberately does
// not go through headerURI: after a bare URI in a header, a ";" introduces
// header parameters, but in a Request-URI it introduces URI parameters, and
// this reads the Request-URI.
func uriParameter(uri, name string) string {
	if open := strings.Index(uri, "<"); open >= 0 {
		if shut := strings.Index(uri[open:], ">"); shut > 0 {
			uri = uri[open+1 : open+shut]
		}
	}
	for _, part := range strings.Split(strings.TrimSpace(uri), ";")[1:] {
		key, value, found := strings.Cut(part, "=")
		if found && strings.EqualFold(strings.TrimSpace(key), name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// headerURI unwraps the angle brackets a SIP header puts around a URI and
// drops any header parameters that follow them.
func headerURI(value string) string {
	value = strings.TrimSpace(value)
	if open := strings.Index(value, "<"); open >= 0 {
		if shut := strings.Index(value[open:], ">"); shut > 0 {
			return strings.TrimSpace(value[open+1 : open+shut])
		}
	}
	if index := strings.Index(value, ";"); index >= 0 {
		value = value[:index]
	}
	return strings.TrimSpace(value)
}
