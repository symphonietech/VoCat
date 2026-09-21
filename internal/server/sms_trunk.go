package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"vocat/internal/asteriskconf"
	"vocat/internal/device"
	"vocat/internal/siptrunk"
	"vocat/internal/store"
)

// SMSTrunk is the half of the SIP trunk the server pushes texts into. An
// interface rather than the concrete type because the trunk is created after
// the handler -- it needs the handler's gateway -- so the two are joined
// afterwards rather than at construction.
type SMSTrunk interface {
	SendSMS(ctx context.Context, message siptrunk.OutboundSMS) error
	InboundEnabled() bool
}

// AttachSMSTrunk hands the server the trunk to forward inbound SMS through.
func (s *Server) AttachSMSTrunk(trunk SMSTrunk) {
	s.smsTrunkMu.Lock()
	defer s.smsTrunkMu.Unlock()
	s.smsTrunk = trunk
}

func (s *Server) smsTrunkHandle() SMSTrunk {
	s.smsTrunkMu.Lock()
	defer s.smsTrunkMu.Unlock()
	return s.smsTrunk
}

// smsTrunkPollInterval matches the other SMS dispatchers. A text is not a
// call: a few seconds late is invisible to the person reading it.
const smsTrunkPollInterval = 5 * time.Second

// StartSMSTrunkForwarder delivers newly received SMS to the PBX.
//
// It is a cursor over the same store the notification channels read rather
// than a hook in either ingest path, which is what makes it cover both: IMS
// and AT-modem messages are already rows by the time it sees them. It also
// inherits multipart reassembly, because a part that is not the last one is
// not yet ready to notify, and it inherits the cursor starting at the newest
// row -- so a restart does not deliver yesterday's texts to somebody's phone.
func (s *Server) StartSMSTrunkForwarder(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	go s.runSMSTrunkForwarder(ctx)
}

func (s *Server) runSMSTrunkForwarder(ctx context.Context) {
	var cursor int64
	initialized := false
	lastError := ""
	lastErrorAt := time.Time{}
	report := func(err error) {
		if err == nil {
			lastError = ""
			return
		}
		if err.Error() != lastError || time.Since(lastErrorAt) >= time.Minute {
			s.logger.Warn("forward inbound SMS to the PBX",
				"category", "siptrunk", "error", err)
			lastError, lastErrorAt = err.Error(), time.Now()
		}
	}

	for ctx.Err() == nil {
		if !initialized {
			latest, err := s.store.LatestSMSMessageID(ctx)
			if err != nil {
				report(err)
				if !waitTelegram(ctx, smsTrunkPollInterval) {
					return
				}
				continue
			}
			cursor, initialized = latest, true
			report(nil)
		}
		trunk := s.smsTrunkHandle()
		mode := s.asteriskSMSMode(ctx)
		if trunk == nil || !trunk.InboundEnabled() || mode == asteriskconf.SMSOff {
			// Nothing to deliver to, or delivery is off. Keep the cursor at
			// the newest row so switching it on later starts with the next
			// message rather than replaying the backlog.
			if newest, err := s.store.LatestSMSMessageID(ctx); err == nil {
				cursor = newest
			}
			if !waitTelegram(ctx, smsTrunkPollInterval) {
				return
			}
			continue
		}

		messages, err := s.store.ListInboundSMSAfterID(ctx, cursor, 100)
		if err != nil {
			report(err)
			if !waitTelegram(ctx, smsTrunkPollInterval) {
				return
			}
			continue
		}
		for _, message := range messages {
			if !smsMessageReadyToNotify(message) {
				cursor = message.ID
				continue
			}
			if err := s.forwardSMSToPBX(ctx, trunk, message); err != nil {
				// Not retried: a text the PBX refused is refused, and
				// retrying it forever would block every message behind it.
				// VoCat's own history is the system of record either way.
				report(err)
			}
			cursor = message.ID
		}
		report(nil)
		if !waitTelegram(ctx, smsTrunkPollInterval) {
			return
		}
	}
}

// forwardSMSToPBX delivers one message, then records that it went.
func (s *Server) forwardSMSToPBX(ctx context.Context, trunk SMSTrunk, message store.SMSMessage) error {
	recipient := strings.TrimSpace(message.LocalPhone)
	if recipient == "" {
		// Without the SIM's own number there is no request URI user, so
		// there is no extension to deliver to. Skipping is honest; guessing
		// would send somebody else's text to a handset.
		return fmt.Errorf("device %s has no known number, so an SMS from %s was not forwarded",
			message.DeviceID, message.Peer)
	}
	name := ""
	if device, err := s.store.Device(ctx, message.DeviceID); err == nil {
		name = strings.TrimSpace(device.Name)
	}
	if err := trunk.SendSMS(ctx, siptrunk.OutboundSMS{
		Recipient:  recipient,
		Sender:     message.Peer,
		Body:       message.Body,
		DeviceID:   message.DeviceID,
		DeviceName: name,
	}); err != nil {
		return err
	}
	if err := s.store.TagSMSTrunkOrigin(ctx, message.ID,
		store.SMSTrunkForwardedTo, recipient); err != nil {
		s.logger.Warn("could not record that an SMS was forwarded",
			"category", "siptrunk", "message", message.ID, "error", err)
	}
	s.logger.Info("SMS forwarded to the PBX",
		"category", "siptrunk", "event", "sms.forwarded",
		"device_id", message.DeviceID, "extension", recipient, "peer", message.Peer)
	return nil
}

// SendSMS accepts a text an extension is sending out through its own SIM.
//
// The rule, and the only one: the sender has to be the number of a SIM this
// VoCat hosts. That is what selects which SIM sends and which account is
// billed. The recipient is deliberately unconstrained -- it is an ordinary
// number out on the network, which is the point of sending.
func (g *sipTrunkGateway) SendSMS(ctx context.Context, sender, recipient, body string) error {
	sender = strings.TrimSpace(sender)
	deviceID, err := g.server.deviceForNumber(ctx, sender)
	if err != nil {
		return err
	}
	saved, err := g.server.submitTrunkSMS(ctx, deviceID, recipient, body)
	if err != nil {
		return fmt.Errorf("device %s: %w", deviceID, err)
	}
	if saved > 0 {
		if err := g.server.store.TagSMSTrunkOrigin(ctx, saved,
			store.SMSTrunkOriginExtension, sender); err != nil {
			g.server.logger.Warn("could not record which extension sent an SMS",
				"category", "siptrunk", "message", saved, "error", err)
		}
	}
	return nil
}

// deviceForNumber finds the SIM whose own number is the one given.
//
// Matching is on the stored local number rather than on the extension list,
// because the extension list is Asterisk's view and this is VoCat's: a name
// in one that has no SIM behind it in the other is exactly the case that has
// to be refused.
func (s *Server) deviceForNumber(ctx context.Context, number string) (string, error) {
	number = strings.TrimSpace(number)
	if number == "" {
		return "", siptrunk.ErrSMSSenderUnknown
	}
	devices, err := s.store.ListDevices(ctx)
	if err != nil {
		return "", err
	}
	for _, config := range devices {
		if sameSMSNumber(s.deviceLocalNumber(ctx, config.ID), number) {
			return config.ID, nil
		}
	}
	return "", fmt.Errorf("%w: %s", siptrunk.ErrSMSSenderUnknown, number)
}

// sameSMSNumber compares two numbers the way a carrier does: the national and
// E.164 forms of one number are one number. A phone registers as 15551230000
// while the network reports +15551230000, and refusing that pair would make
// the feature look broken on every deployment outside the one it was written
// on.
//
// The suffix rule is deliberately floored at smsSignificantDigits. Without a
// floor a four-digit extension matches any SIM whose number happens to end in
// those digits, and the SIM it picks decides which account is billed and what
// caller ID the recipient sees. A national significant number is at least
// seven digits everywhere, and an extension short enough to collide is
// shorter than that, so the floor separates the two cases exactly.
const smsSignificantDigits = 7

func sameSMSNumber(left, right string) bool {
	left = strings.TrimPrefix(strings.TrimSpace(left), "+")
	right = strings.TrimPrefix(strings.TrimSpace(right), "+")
	if left == "" || right == "" {
		return false
	}
	if left == right {
		return true
	}
	shorter, longer := left, right
	if len(shorter) > len(longer) {
		shorter, longer = longer, shorter
	}
	if len(shorter) < smsSignificantDigits {
		return false
	}
	return strings.HasSuffix(longer, shorter)
}

// deviceLocalNumber is the SIM's own number as VoCat last saw it.
//
// It defers to smsLocalPhone, the resolver every other SMS path already uses,
// so a number that the history shows against a message is the same number an
// extension may send from. Two answers to "whose number is this" would be one
// answer too many.
func (s *Server) deviceLocalNumber(ctx context.Context, deviceID string) string {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return ""
	}
	var snapshot *device.Snapshot
	if s.devices != nil {
		if entry, err := s.devices.Get(deviceID); err == nil {
			snapshot = entry.Snapshot
		}
	}
	identity := smsIdentityFromSnapshot(snapshot)
	if identity.ICCID == "" && s.vowifi != nil {
		// A VoWiFi SIM reports its ICCID through the orchestrator even when
		// the modem entry has no snapshot yet, and without an ICCID the
		// association table cannot be consulted at all.
		if state, err := s.vowifi.State(deviceID); err == nil {
			identity.ICCID = strings.TrimSpace(state.ICCID)
			identity.IMSI = strings.TrimSpace(state.IMSI)
		}
	}
	return strings.TrimSpace(s.smsLocalPhone(ctx, deviceID, identity, snapshot))
}

// submitTrunkSMS hands the text to the same endpoint the GUI posts to.
//
// Going through the HTTP handler rather than around it is deliberate: the
// destination block list, the global rate limit, the IMS-or-modem choice,
// multipart segmentation and the history row all live in that handler, and a
// second send path would have to grow its own copy of every one of them.
// The returned id is the stored row, so the caller can mark who sent it.
func (s *Server) submitTrunkSMS(ctx context.Context, deviceID, recipient, body string) (int64, error) {
	if s.devices == nil {
		return 0, errSMSSendUnavailable
	}
	payload, err := json.Marshal(map[string]string{
		"device_id": deviceID,
		"phone":     recipient,
		"message":   body,
	})
	if err != nil {
		return 0, err
	}
	request := httptest.NewRequest(http.MethodPost, "/api/sms/send", bytes.NewReader(payload)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	s.handleSMSSend(recorder, request)

	var response struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
		Error *apiError `json:"error"`
	}
	// A body that will not decode is only fatal when the status was a
	// failure too: a 2xx whose id we could not read still sent the text, and
	// reporting that as an error would have the handset retry a delivered
	// message.
	decodeErr := json.Unmarshal(recorder.Body.Bytes(), &response)
	if recorder.Code >= http.StatusBadRequest {
		if decodeErr == nil && response.Error != nil {
			// The code is carried alongside the prose because the prose is
			// often the generic "the device operation failed" branch, which
			// says nothing about which of a dozen causes it was.
			return 0, fmt.Errorf("%s (%s, HTTP %d)",
				response.Error.Message, response.Error.Code, recorder.Code)
		}
		return 0, fmt.Errorf("the SMS endpoint returned HTTP %d", recorder.Code)
	}
	if decodeErr != nil {
		s.logger.Warn("could not read the id of an SMS sent from an extension",
			"category", "siptrunk", "error", decodeErr)
		return 0, nil
	}
	return response.Data.ID, nil
}

// reportSMSDeliveryToExtension tells the extension that sent a message what
// became of it.
//
// A plain text rather than an IMDN receipt: RFC 5438 is the protocol-correct
// answer and almost no softphone implements it, so a phone that cannot parse
// it shows a blank message. The cost of this choice is that the report arrives
// as a new text rather than as a tick beside the original, which SIP has no
// way to do that handsets actually support.
func (s *Server) reportSMSDeliveryToExtension(ctx context.Context, message store.SMSMessage) {
	extension := smsTrunkTag(message.Extra, store.SMSTrunkOriginExtension)
	if extension == "" {
		return
	}
	trunk := s.smsTrunkHandle()
	if trunk == nil || !trunk.InboundEnabled() {
		return
	}
	state := strings.TrimSpace(message.DeliveryState)
	if state == "" {
		state = "unknown"
	}
	// Trimmed by runes, not bytes: an SMS body here is as likely to be
	// Chinese as English, and a byte cut lands mid-character and puts a
	// replacement glyph on the handset.
	preview := []rune(message.Body)
	if len(preview) > 40 {
		preview = append(preview[:40:40], []rune("...")...)
	}
	body := fmt.Sprintf("%s: %s -> %s", strings.ToUpper(state), message.Peer, string(preview))
	if err := trunk.SendSMS(ctx, siptrunk.OutboundSMS{
		Recipient:  extension,
		Sender:     "delivery",
		Body:       body,
		DeviceID:   message.DeviceID,
		DeviceName: "VoCat",
	}); err != nil {
		s.logger.Warn("could not report SMS delivery to the extension",
			"category", "siptrunk", "extension", extension, "error", err)
	}
}

// smsTrunkTag reads one of the trunk markers out of a stored message.
func smsTrunkTag(extra json.RawMessage, key string) string {
	if len(extra) == 0 {
		return ""
	}
	var fields map[string]any
	if err := json.Unmarshal(extra, &fields); err != nil {
		return ""
	}
	value, _ := fields[key].(string)
	return strings.TrimSpace(value)
}

var errSMSSendUnavailable = errors.New("the device manager is unavailable")
