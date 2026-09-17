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

// ResolveDevice turns the PBX's hint into a device ID. A hint may be a device
// ID or a device name, since a dial plan is easier to read with a name in it.
// Without a hint the choice must be unambiguous: guessing between two SIMs
// would place the call on whichever one happened to sort first, and bill it.
func (g *sipTrunkGateway) ResolveDevice(hint string) (string, error) {
	configs, err := g.server.store.ListDevices(context.Background())
	if err != nil {
		return "", fmt.Errorf("list devices: %w", err)
	}
	hint = strings.TrimSpace(hint)
	if hint != "" {
		for _, config := range configs {
			if config.ID != hint && !strings.EqualFold(strings.TrimSpace(config.Name), hint) {
				continue
			}
			if g.server.callTransport(config.ID) != "vowifi" {
				return "", fmt.Errorf("device %q is not registered for VoWiFi calling", hint)
			}
			return config.ID, nil
		}
		return "", fmt.Errorf("no device named %q", hint)
	}
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
		return "", fmt.Errorf(
			"%d devices are registered for VoWiFi calling; name one with an X-VoCat-Device header or a device= URI parameter",
			len(ready))
	}
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
	return media, nil
}

func (g *sipTrunkGateway) Hangup(ctx context.Context, deviceID, callID string) error {
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
