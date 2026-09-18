package siptrunk

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// InboundCall is a call arriving on a SIM that should be offered to the PBX.
type InboundCall struct {
	// DeviceID is the SIM the call arrived on. It travels to the PBX as an
	// X-VoCat-Device header so a dialplan can route by SIM.
	DeviceID string
	// DeviceName is the human-readable label for that SIM, sent alongside the
	// ID because a dialplan is easier to read with one in it.
	DeviceName string
	// IMSCallID identifies the call to the gateway, which is what answers and
	// hangs it up.
	IMSCallID string
	// Caller is the calling number, which becomes the PBX's caller ID.
	Caller string
	// Called is the number that was dialled -- the SIM's own number. It is
	// the request URI, so a dialplan can route by DID.
	Called string
}

// ErrInboundDisabled is returned when no PBX address is configured. Inbound is
// opt-in: a deployment that only places calls should not have VoCat sending
// INVITEs at whatever happens to be on port 5060.
var ErrInboundDisabled = errors.New("siptrunk: no PBX address configured for inbound calls")

const (
	// inboundAnswerTimeout is how long the PBX gets to answer before the call
	// is given up and the SIM leg released. A carrier will usually have given
	// up before this.
	inboundAnswerTimeout = 60 * time.Second
	// inviteTimeout bounds the INVITE transaction before any response at all:
	// RFC 3261 §17.1.1.2 Timer B, 64*T1.
	inviteTimeout = 64 * timerT1
)

// outbound is one call the trunk offered to the PBX. The roles are the mirror
// of dialog: the trunk sent the INVITE, so its own identity is the From plus
// the tag it chose, and the PBX's is the To with the tag it returns.
type outbound struct {
	server   *Server
	callID   string
	localTag string
	call     InboundCall
	leg      *rtpLeg

	from string
	to   string
	// target is where in-dialog requests go: the PBX's Contact once it has
	// sent one, and the original request URI before that.
	target string

	responses chan *Response
	cancel    context.CancelFunc
	done      chan struct{}
	finish    sync.Once

	mu        sync.Mutex
	remoteTag string
	answered  bool
	ack       []byte
	cseq      int
}

// Offer places a call arriving on a SIM in front of the PBX.
//
// It returns as soon as the call is accepted for processing: the IMS runtime
// invokes this from its own signalling path, and blocking there for the
// length of a ringing call would stall everything else on that session.
// Failures after this point end the SIM leg and are logged.
func (s *Server) Offer(call InboundCall) error {
	if s.gateway == nil {
		return errors.New("siptrunk: no gateway, so a call cannot be offered")
	}
	if s.pbx == nil {
		return ErrInboundDisabled
	}
	if strings.TrimSpace(call.IMSCallID) == "" || strings.TrimSpace(call.DeviceID) == "" {
		return errors.New("siptrunk: an offered call needs a device and a call ID")
	}
	select {
	case <-s.closing:
		return errors.New("siptrunk: shutting down")
	default:
	}

	ctx, cancel := context.WithCancel(context.Background())
	current := &outbound{
		server:    s,
		callID:    newCallID(),
		localTag:  newTag(),
		call:      call,
		responses: make(chan *Response, 8),
		cancel:    cancel,
		done:      make(chan struct{}),
		cseq:      1,
	}
	s.mu.Lock()
	s.outbounds[current.callID] = current
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		current.run(ctx)
	}()
	return nil
}

// InboundEnabled reports whether the trunk can offer calls to a PBX, which the
// caller uses to decide whether to try at all.
func (s *Server) InboundEnabled() bool { return s.pbx != nil && s.gateway != nil }

func (s *Server) outboundCall(callID string) *outbound {
	if callID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.outbounds[callID]
}

func (s *Server) forgetOutbound(callID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.outbounds, callID)
}

func (s *Server) activeOutbounds() []*outbound {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := make([]*outbound, 0, len(s.outbounds))
	for _, entry := range s.outbounds {
		current = append(current, entry)
	}
	return current
}

// deliverResponse hands a response to the call it belongs to. A response for
// a call that has ended is dropped: it is a retransmission racing the
// teardown, not an error.
func (s *Server) deliverResponse(packet []byte) {
	response, err := ParseResponse(packet)
	if err != nil {
		s.log("siptrunk rejected a malformed response", "error", err)
		return
	}
	current := s.outboundCall(response.Value("call-id"))
	if current == nil {
		return
	}
	select {
	case current.responses <- response:
	case <-current.done:
	default:
		// The dialog is not reading; a backlog of provisional responses is
		// not worth blocking the read loop for.
	}
}

// run drives one offered call from INVITE to teardown.
func (o *outbound) run(ctx context.Context) {
	defer o.teardown(true)

	leg, err := newRTPLeg(o.server.mediaAddress(o.server.pbx), nil, payloadPCMU)
	if err != nil {
		o.fail(err)
		return
	}
	o.leg = leg

	host := o.server.pbx.String()
	called := strings.TrimSpace(o.call.Called)
	if called == "" {
		// A PBX still needs something to route on. "s" is Asterisk's own
		// convention for a call with no specific destination, and the shipped
		// from-vocat context handles it.
		called = "s"
	}
	o.target = "sip:" + called + "@" + host
	caller := strings.TrimSpace(o.call.Caller)
	if caller == "" {
		caller = "anonymous"
	}
	o.from = fmt.Sprintf("<sip:%s@%s>;tag=%s", caller, o.server.viaHost(o.server.pbx), o.localTag)
	o.to = "<" + o.target + ">"

	offer := BuildOffer(leg.conn.LocalAddr().(*net.UDPAddr).IP, leg.LocalPort())
	invite := o.buildINVITE(offer)
	o.server.log("siptrunk is offering a call to the PBX",
		"call_id", o.callID, "device_id", o.call.DeviceID,
		"caller", caller, "called", called, "ims_call_id", o.call.IMSCallID)

	response, err := o.awaitAnswer(ctx, invite)
	if err != nil {
		o.fail(err)
		return
	}
	// The ACK goes to the Contact the 200 carried, which is not necessarily
	// where the response came from.
	if contact := headerURI(response.Value("contact")); contact != "" {
		o.target = contact
	}
	o.mu.Lock()
	o.remoteTag = tagOf(response.Value("to"))
	o.to = response.Value("to")
	o.answered = true
	o.mu.Unlock()
	o.sendACK()
	// A 2xx that arrives again means the ACK was lost. Nothing else reads
	// responses once the call is up, so without this the PBX retransmits
	// until Timer H and then tears the call down at about thirty seconds.
	go o.reACK(ctx)

	answer, err := ParseOffer(response.Body)
	if err != nil {
		o.server.log("siptrunk could not read the PBX's answer", "call_id", o.callID, "error", err)
		return
	}
	leg.setPayload(answer.Payload)
	// Whatever the PBX's answer named, which may differ from the 101 offered.
	leg.setEventPayload(answer.EventPayload)
	leg.setRemote(&net.UDPAddr{IP: answer.Address, Port: answer.Port})

	// The SIM leg is answered only now. Answering earlier would connect the
	// caller to silence while the handset was still ringing, and bill them
	// for it.
	if err := o.server.gateway.Answer(ctx, o.call.DeviceID, o.call.IMSCallID); err != nil {
		o.server.log("siptrunk could not answer the SIM leg",
			"call_id", o.callID, "ims_call_id", o.call.IMSCallID, "error", err)
		return
	}
	media, err := o.server.gateway.Media(ctx, o.call.DeviceID, o.call.IMSCallID)
	if err != nil {
		o.server.log("siptrunk could not open SIM media",
			"call_id", o.callID, "ims_call_id", o.call.IMSCallID, "error", err)
		return
	}
	o.server.log("siptrunk bridged an inbound call",
		"call_id", o.callID, "ims_call_id", o.call.IMSCallID, "rtp_port", leg.LocalPort())
	o.pump(ctx, media, leg)
}

// awaitAnswer sends the INVITE, retransmits it until something answers, and
// returns the 2xx. Anything else -- a rejection, a timeout, the SIM caller
// giving up -- is an error, and the caller releases both legs.
func (o *outbound) awaitAnswer(ctx context.Context, invite []byte) (*Response, error) {
	o.server.send(invite, o.server.pbx)
	// RFC 3261 §17.1.1.2: retransmit at T1, doubling, until a response
	// arrives or Timer B expires. Asterisk on loopback answers in
	// milliseconds; this is for the case where it is not there at all.
	interval := timerT1
	retransmit := time.NewTimer(interval)
	defer retransmit.Stop()
	transaction := time.After(inviteTimeout)
	answering := time.After(inboundAnswerTimeout)
	provisional := false

	for {
		select {
		case <-ctx.Done():
			o.sendCANCEL()
			return nil, ctx.Err()
		case <-o.done:
			return nil, errors.New("siptrunk: the call ended before the PBX answered")
		case <-transaction:
			if !provisional {
				return nil, fmt.Errorf("siptrunk: the PBX at %s did not respond", o.server.pbx)
			}
			transaction = nil
		case <-answering:
			o.sendCANCEL()
			return nil, errors.New("siptrunk: the PBX did not answer in time")
		case <-retransmit.C:
			if !provisional {
				o.server.send(invite, o.server.pbx)
				if interval *= 2; interval > timerT2 {
					interval = timerT2
				}
				retransmit.Reset(interval)
			}
		case response := <-o.responses:
			switch {
			case response.Status < 200:
				// Ringing. Nothing to relay: the SIM leg has not been
				// answered, so the carrier is already giving the caller
				// ringback.
				provisional = true
			case response.Status < 300:
				return response, nil
			default:
				return nil, fmt.Errorf("siptrunk: the PBX rejected the call: %d %s",
					response.Status, response.Reason)
			}
		}
	}
}

// pump moves audio both ways until either leg stops, exactly as the outbound
// direction does. The first to fail ends the call.
func (o *outbound) pump(ctx context.Context, media Media, leg *rtpLeg) {
	var once sync.Once
	stop := make(chan struct{})
	end := func() { once.Do(func() { close(stop) }) }

	relayDigits(ctx, stop, leg, media, o.server, o.callID)

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
	case <-o.done:
	}
}

// reACK answers a retransmitted 2xx with the same ACK. It also keeps the
// response channel drained, so deliverResponse never has to drop into its
// default case for a call that is still up.
func (o *outbound) reACK(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-o.done:
			return
		case response := <-o.responses:
			if response.Status < 200 || response.Status >= 300 {
				continue
			}
			o.mu.Lock()
			ack := o.ack
			o.mu.Unlock()
			if ack != nil {
				o.server.send(ack, o.server.pbx)
			}
		}
	}
}

func (o *outbound) fail(err error) {
	o.server.log("siptrunk could not offer a call",
		"call_id", o.callID, "ims_call_id", o.call.IMSCallID, "error", err)
}

func (o *outbound) wasAnswered() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.answered
}

// teardown ends the call once. sendBye is false when the PBX hung up, since
// it is already gone and a BYE back would be answered 481.
func (o *outbound) teardown(sendBye bool) {
	o.finish.Do(func() {
		close(o.done)
		o.cancel()
		if sendBye && o.wasAnswered() {
			o.sendBYE()
		}
		if o.leg != nil {
			_ = o.leg.Close()
		}
		// The SIM leg is released on every path, answered or not: a call the
		// PBX refused must not be left ringing at the carrier.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := o.server.gateway.Hangup(ctx, o.call.DeviceID, o.call.IMSCallID); err != nil {
			o.server.log("siptrunk could not hang up the SIM leg",
				"call_id", o.callID, "ims_call_id", o.call.IMSCallID, "error", err)
		}
		cancel()
		o.server.forgetOutbound(o.callID)
		o.server.log("siptrunk released an inbound call", "call_id", o.callID)
	})
}

func (o *outbound) buildINVITE(body []byte) []byte {
	headers := []string{
		"INVITE " + o.target + " SIP/2.0",
		"Via: " + o.via(),
		"Max-Forwards: 70",
		"From: " + o.from,
		"To: " + o.to,
		"Call-ID: " + o.callID,
		"CSeq: " + strconv.Itoa(o.cseq) + " INVITE",
		"Contact: <" + o.server.contactURI(o.server.pbx) + ">",
		// Which SIM the call arrived on, so a dialplan can route by it. The
		// same header name the outbound direction reads, in the opposite
		// direction.
		"X-VoCat-Device: " + o.call.DeviceID,
		"User-Agent: VoCat",
		"Content-Type: application/sdp",
		"Content-Length: " + strconv.Itoa(len(body)),
		"",
	}
	if name := strings.TrimSpace(o.call.DeviceName); name != "" {
		headers = insertHeader(headers, "X-VoCat-Device-Name: "+name)
	}
	return append([]byte(strings.Join(headers, "\r\n")+"\r\n"), body...)
}

// sendACK confirms a 2xx. It is kept so a retransmitted 200 -- which means the
// first ACK was lost -- can be answered with the same one.
func (o *outbound) sendACK() {
	o.mu.Lock()
	cseq := o.cseq
	to := o.to
	o.mu.Unlock()
	message := strings.Join([]string{
		"ACK " + o.target + " SIP/2.0",
		"Via: " + o.via(),
		"Max-Forwards: 70",
		"From: " + o.from,
		"To: " + to,
		"Call-ID: " + o.callID,
		"CSeq: " + strconv.Itoa(cseq) + " ACK",
		"Contact: <" + o.server.contactURI(o.server.pbx) + ">",
		"User-Agent: VoCat",
		"Content-Length: 0",
		"", "",
	}, "\r\n")
	o.mu.Lock()
	o.ack = []byte(message)
	o.mu.Unlock()
	o.server.send([]byte(message), o.server.pbx)
}

// sendCANCEL gives up on an INVITE that has not been answered. Per RFC 3261
// §9.1 it copies the INVITE's Via branch and CSeq number, which is what tells
// the PBX which transaction to abandon.
func (o *outbound) sendCANCEL() {
	if o.wasAnswered() {
		return
	}
	message := strings.Join([]string{
		"CANCEL " + o.target + " SIP/2.0",
		"Via: " + o.via(),
		"Max-Forwards: 70",
		"From: " + o.from,
		"To: " + o.to,
		"Call-ID: " + o.callID,
		"CSeq: " + strconv.Itoa(o.cseq) + " CANCEL",
		"User-Agent: VoCat",
		"Content-Length: 0",
		"", "",
	}, "\r\n")
	o.server.send([]byte(message), o.server.pbx)
}

func (o *outbound) sendBYE() {
	o.mu.Lock()
	o.cseq++
	cseq := o.cseq
	to := o.to
	o.mu.Unlock()
	message := strings.Join([]string{
		"BYE " + o.target + " SIP/2.0",
		"Via: " + o.via(),
		"Max-Forwards: 70",
		"From: " + o.from,
		"To: " + to,
		"Call-ID: " + o.callID,
		"CSeq: " + strconv.Itoa(cseq) + " BYE",
		"User-Agent: VoCat",
		"Content-Length: 0",
		"", "",
	}, "\r\n")
	o.server.send([]byte(message), o.server.pbx)
}

// via is the branch this dialog uses. It is stable for the life of the call:
// the INVITE, its CANCEL and its ACK must share one, and a BYE reusing it is
// harmless on a trunk that never forks.
func (o *outbound) via() string {
	return fmt.Sprintf("SIP/2.0/UDP %s;rport;branch=z9hG4bK%s", o.server.viaHost(o.server.pbx), o.localTag)
}

// insertHeader puts a header before the blank line that ends the block.
func insertHeader(headers []string, header string) []string {
	return append(headers[:len(headers)-1:len(headers)-1], header, headers[len(headers)-1])
}

// tagOf reads the ;tag= parameter a peer put on its To or From.
func tagOf(value string) string {
	for _, part := range strings.Split(value, ";")[1:] {
		key, tag, found := strings.Cut(part, "=")
		if found && strings.EqualFold(strings.TrimSpace(key), "tag") {
			return strings.TrimSpace(tag)
		}
	}
	return ""
}

func newCallID() string { return newTag() + newTag() + "@vocat" }
