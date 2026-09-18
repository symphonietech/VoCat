package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"vocat/internal/siptrunk"
	"vocat/internal/vowifi"
)

// answerPoll is how often the trunk looks at a dialling call. IMS state comes
// from the signalling goroutine rather than a channel, so polling is what the
// call controller offers; 200 ms is fast enough that the PBX's ringback and
// the SIM's answer line up to the ear.
const answerPoll = 200 * time.Millisecond

// sipTrunkGateway adapts the VoWiFi call controller to what internal/siptrunk
// needs. It lives here rather than in siptrunk so that package stays free of
// VoCat's device model and can be tested with a fake.
type sipTrunkGateway struct {
	server *Server
}

// SIPTrunkGateway returns the gateway to hand to siptrunk.Listen, or nil when
// this build has no VoWiFi call controller. A nil gateway is meaningful: the
// trunk still answers OPTIONS so the PBX sees a reachable peer, and refuses
// calls instead of accepting ones it could not place.
func (s *Server) SIPTrunkGateway() siptrunk.Gateway {
	if s == nil || s.vowifi == nil {
		return nil
	}
	if _, ok := s.vowifi.(VoWiFiCallController); !ok {
		return nil
	}
	return &sipTrunkGateway{server: s}
}

func (g *sipTrunkGateway) controller() (VoWiFiCallController, error) {
	controller, ok := g.server.vowifi.(VoWiFiCallController)
	if !ok {
		return nil, errors.New("the VoWiFi session does not expose call signalling")
	}
	return controller, nil
}

// ResolveDevice turns the PBX's hint into a device ID.
//
// A hint is one of three things:
//
//   - empty -- use the single IMS-registered device, and refuse if there is
//     more than one. Guessing would place a real, billed call on whichever
//     SIM happened to sort first.
//   - one device ID or name -- use that device. Names are accepted because a
//     dial plan is easier to read with one in it.
//   - several, comma or space separated, or "*" for every registered device
//     -- rotate across them, one call each in turn.
//
// The list form is what makes rotation safe to offer at all: refusing to
// choose stays the default, and naming several is the operator saying they
// have thought about which SIMs may carry which calls. "*" says the same
// thing about every device that happens to be registered.
//
// Rotation skips devices that are not currently IMS-registered rather than
// handing back a SIM that cannot dial, so a card dropping out costs one
// device from the rotation rather than every Nth call.
func (g *sipTrunkGateway) ResolveDevice(hint string) (string, error) {
	configs, err := g.server.store.ListDevices(context.Background())
	if err != nil {
		return "", fmt.Errorf("list devices: %w", err)
	}
	names := splitDeviceHint(hint)

	// No hint: the historical behaviour, and still the safe default.
	if len(names) == 0 {
		var ready []string
		for _, config := range configs {
			if g.server.callTransport(config.ID) == "vowifi" {
				ready = append(ready, config.ID)
			}
		}
		switch len(ready) {
		case 0:
			return "", errors.New("no device is registered for VoWiFi calling")
		case 1:
			return ready[0], nil
		default:
			// Naming them matters: the operator has to pick, and the whole
			// point of refusing is that VoCat must not. Sending them off to
			// find the IDs elsewhere makes the refusal harder to act on than
			// the wrong guess would have been.
			return "", fmt.Errorf(
				"%d devices are registered for VoWiFi calling (%s); name one with an "+
					"X-VoCat-Device header or a device= URI parameter, several to rotate "+
					"between them, or * for all of them",
				len(ready), strings.Join(ready, ", "))
		}
	}

	// "*" means every configured device, which the readiness filter below
	// then narrows to the ones that can actually dial.
	if len(names) == 1 && names[0] == "*" {
		names = names[:0]
		for _, config := range configs {
			names = append(names, config.ID)
		}
	}

	var candidates, unknown, offline []string
	for _, name := range names {
		matched := ""
		for _, config := range configs {
			if config.ID == name || strings.EqualFold(strings.TrimSpace(config.Name), name) {
				matched = config.ID
				break
			}
		}
		switch {
		case matched == "":
			unknown = append(unknown, name)
		case g.server.callTransport(matched) != "vowifi":
			offline = append(offline, matched)
		default:
			candidates = append(candidates, matched)
		}
	}

	if len(candidates) == 0 {
		switch {
		case len(unknown) > 0 && len(offline) == 0:
			return "", fmt.Errorf("no device named %s", strings.Join(unknown, ", "))
		case len(offline) > 0 && len(unknown) == 0:
			return "", fmt.Errorf("no named device is registered for VoWiFi calling (%s)",
				strings.Join(offline, ", "))
		default:
			return "", fmt.Errorf(
				"no named device can place a call: unknown (%s), not registered for VoWiFi (%s)",
				strings.Join(unknown, ", "), strings.Join(offline, ", "))
		}
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return candidates[g.server.nextTrunkDevice()%uint64(len(candidates))], nil
}

// splitDeviceHint accepts commas, spaces or both, so a dial plan can write the
// list whichever way reads best and a stray space does not become part of an
// ID.
func splitDeviceHint(hint string) []string {
	fields := strings.FieldsFunc(hint, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	names := make([]string, 0, len(fields))
	for _, field := range fields {
		if field = strings.TrimSpace(field); field != "" {
			names = append(names, field)
		}
	}
	return names
}

// nextTrunkDevice hands out the rotation position. It counts calls rather than
// tracking which SIM went last, so the rotation is unaffected by a device
// joining or leaving the candidate set between calls.
func (s *Server) nextTrunkDevice() uint64 {
	s.trunkMu.Lock()
	defer s.trunkMu.Unlock()
	position := s.trunkRotation
	s.trunkRotation++
	return position
}

func (g *sipTrunkGateway) Dial(ctx context.Context, deviceID, number string) (string, error) {
	controller, err := g.controller()
	if err != nil {
		return "", err
	}
	call, err := controller.DialCall(ctx, deviceID, number)
	if err != nil {
		return "", err
	}
	if call.ID == "" {
		return "", errors.New("the IMS session returned a call with no identifier")
	}
	g.server.recordAudit(ctx, "siptrunk", "call.dial", "device", deviceID, "success", "vowifi")
	return call.ID, nil
}

// Answer accepts a call that arrived on a SIM, which is what connects the
// caller to the handset the PBX has already picked up. It is deliberately not
// called before that: answering earlier bills the caller for silence.
func (g *sipTrunkGateway) Answer(ctx context.Context, deviceID, callID string) error {
	controller, err := g.controller()
	if err != nil {
		return err
	}
	if _, err := controller.AnswerCall(ctx, deviceID, callID); err != nil {
		return err
	}
	g.server.recordAudit(ctx, "siptrunk", "call.answer", "device", deviceID, "success", "vowifi")
	// The IMS side reports media a moment after the answer, the same way it
	// does for a call placed from the browser. Bridging before then would
	// drop the first frames in both directions.
	return g.WaitAnswered(ctx, deviceID, callID)
}

// WaitAnswered blocks until the call is both answered and carrying media. The
// media condition matters: answering the PBX's INVITE before RTP is negotiated
// would bridge two legs where one has nowhere to send audio, which is the
// silent-call failure this whole path exists to avoid.
func (g *sipTrunkGateway) WaitAnswered(ctx context.Context, deviceID, callID string) error {
	controller, err := g.controller()
	if err != nil {
		return err
	}
	ticker := time.NewTicker(answerPoll)
	defer ticker.Stop()
	for {
		call, found, err := findVoWiFiCall(controller, deviceID, callID)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("the call is no longer known to the IMS session")
		}
		switch call.State {
		case "active":
			if call.MediaReady {
				return nil
			}
		case "ended", "failed":
			return callEndedError(call)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (g *sipTrunkGateway) Media(ctx context.Context, deviceID, callID string) (siptrunk.Media, error) {
	controller, ok := g.server.vowifi.(VoWiFiCallMediaController)
	if !ok {
		return nil, errors.New("the VoWiFi session does not expose RTP media")
	}
	media, err := controller.CallMedia(ctx, deviceID, callID)
	if err != nil {
		return nil, err
	}
	g.server.claimTrunkCall(deviceID, callID)
	return media, nil
}

// An IMS call has one RTP bridge and its downlink is a single channel, so two
// readers would take roughly half the frames each and leave both sides
// choppy. Claiming a call is what lets the browser bridge refuse rather than
// quietly degrade a call the PBX is already carrying.

func trunkCallKey(deviceID, callID string) string { return deviceID + "\x00" + callID }

func (s *Server) claimTrunkCall(deviceID, callID string) {
	s.trunkMu.Lock()
	defer s.trunkMu.Unlock()
	if s.trunkCalls == nil {
		s.trunkCalls = make(map[string]bool)
	}
	s.trunkCalls[trunkCallKey(deviceID, callID)] = true
}

func (s *Server) releaseTrunkCall(deviceID, callID string) {
	s.trunkMu.Lock()
	defer s.trunkMu.Unlock()
	delete(s.trunkCalls, trunkCallKey(deviceID, callID))
}

// trunkHoldsCall reports whether the SIP trunk is carrying a call's audio.
func (s *Server) trunkHoldsCall(deviceID, callID string) bool {
	s.trunkMu.Lock()
	defer s.trunkMu.Unlock()
	return s.trunkCalls[trunkCallKey(deviceID, callID)]
}

func (g *sipTrunkGateway) Hangup(ctx context.Context, deviceID, callID string) error {
	// Released here rather than in the bridge because Hangup is on every
	// teardown path, including the ones that never reached media.
	g.server.releaseTrunkCall(deviceID, callID)
	controller, err := g.controller()
	if err != nil {
		return err
	}
	if call, found, err := findVoWiFiCall(controller, deviceID, callID); err == nil && found {
		if call.State == "ended" || call.State == "failed" {
			// Already over -- the SIM hung up, or the far end did. Asking again
			// would log a spurious failure on every normal teardown.
			return nil
		}
	}
	return controller.HangupCall(ctx, deviceID, callID)
}

func findVoWiFiCall(controller VoWiFiCallController, deviceID, callID string) (vowifi.Call, bool, error) {
	calls, err := controller.Calls(deviceID)
	if err != nil {
		return vowifi.Call{}, false, err
	}
	for _, call := range calls {
		if call.ID == callID {
			return call, true, nil
		}
	}
	return vowifi.Call{}, false, nil
}

// callEndedError turns a terminal call into a message a PBX operator can act
// on, since the SIP status the trunk sends back collapses every one of these
// into "unavailable".
func callEndedError(call vowifi.Call) error {
	switch {
	case call.Reason != "" && call.SIPCode != 0:
		return fmt.Errorf("the call ended: %d %s", call.SIPCode, call.Reason)
	case call.SIPCode != 0:
		return fmt.Errorf("the call ended with SIP status %d", call.SIPCode)
	case call.Reason != "":
		return fmt.Errorf("the call ended: %s", call.Reason)
	default:
		return errors.New("the call ended before it was answered")
	}
}
