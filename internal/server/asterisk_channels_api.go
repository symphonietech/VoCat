package server

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"vocat/internal/ami"
)

// asteriskChannel is one live channel: a call leg Asterisk is carrying.
type asteriskChannel struct {
	Name  string `json:"name"`
	State string `json:"state,omitempty"`
	// CallerID and ConnectedLine are the two ends of this leg as Asterisk
	// sees them, which is what makes a stuck channel identifiable.
	CallerID      string          `json:"caller_id,omitempty"`
	ConnectedLine string          `json:"connected_line,omitempty"`
	Context       string          `json:"context,omitempty"`
	Extension     string          `json:"extension,omitempty"`
	Application   string          `json:"application,omitempty"`
	Duration      string          `json:"duration,omitempty"`
	BridgeID      string          `json:"bridge_id,omitempty"`
	UniqueID      string          `json:"unique_id,omitempty"`
	Fields        []asteriskField `json:"fields,omitempty"`
}

// buildAsteriskChannels maps CoreShowChannel events. Field names come from the
// Asterisk 22 manager documentation, with the usual fallbacks: a row that
// blanks because a key moved between versions is worse than one showing a
// value under an older name.
func buildAsteriskChannels(events []ami.Message) []asteriskChannel {
	channels := make([]asteriskChannel, 0, len(events))
	for _, event := range events {
		name := event.First("Channel")
		if name == "" {
			continue
		}
		channels = append(channels, asteriskChannel{
			Name:          name,
			State:         event.First("ChannelStateDesc", "ChannelState"),
			CallerID:      joinIdentity(event.First("CallerIDNum"), event.First("CallerIDName")),
			ConnectedLine: joinIdentity(event.First("ConnectedLineNum"), event.First("ConnectedLineName")),
			Context:       event.First("Context"),
			Extension:     event.First("Exten", "Extension"),
			Application:   event.First("Application"),
			Duration:      event.First("Duration"),
			BridgeID:      event.First("BridgeId", "BridgeID"),
			UniqueID:      event.First("Uniqueid", "UniqueID"),
			Fields:        rawFields(event),
		})
	}
	sort.Slice(channels, func(first, second int) bool {
		return channels[first].Name < channels[second].Name
	})
	return channels
}

// joinIdentity renders a number and a name as one readable value. Asterisk
// uses "<unknown>" for an absent one, which is noise in a table.
func joinIdentity(number, name string) string {
	number, name = cleanIdentity(number), cleanIdentity(name)
	switch {
	case number != "" && name != "":
		return name + " <" + number + ">"
	case name != "":
		return name
	default:
		return number
	}
}

func cleanIdentity(value string) string {
	value = strings.TrimSpace(value)
	if value == "<unknown>" || value == "unknown" {
		return ""
	}
	return value
}

// readAsteriskChannels lists live channels, tolerating the empty case: no
// calls in progress is the normal state and Asterisk reports an empty listing
// as a failed action.
func (s *Server) readAsteriskChannels(ctx context.Context, conn *ami.Conn) ([]asteriskChannel, error) {
	events, err := conn.List(ctx, "CoreShowChannels", nil)
	if err != nil {
		if ami.IsEmptyList(err) {
			return []asteriskChannel{}, nil
		}
		return nil, err
	}
	return buildAsteriskChannels(events), nil
}

// defaultHangupCause is Q.850 16, normal clearing: the call ended the way a
// call normally ends, which is what an operator pressing a button means.
const defaultHangupCause = 16

// handleAsteriskHangup ends one channel.
//
// The channel name is checked against the live list rather than passed
// through. AMI's Hangup takes a regular expression when the value is wrapped
// in slashes, so a channel of "/./" would end every call on the PBX --
// requiring an exact match against a channel that actually exists makes that
// unreachable, because no real channel name starts with a slash.
func (s *Server) handleAsteriskHangup(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(s.asteriskAMI.Address) == "" {
		writeError(w, http.StatusNotImplemented, "ami_not_configured",
			"the Asterisk manager interface is not configured, so VoCat cannot end a channel")
		return
	}
	var request struct {
		Channel string `json:"channel"`
		Cause   int    `json:"cause"`
	}
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	channel := strings.TrimSpace(request.Channel)
	if channel == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "channel is required")
		return
	}
	cause := request.Cause
	if cause == 0 {
		cause = defaultHangupCause
	}
	if cause < 1 || cause > 127 {
		writeError(w, http.StatusBadRequest, "invalid_cause", "cause must be a Q.850 value between 1 and 127")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), asteriskStatusTimeout)
	defer cancel()
	conn, err := ami.Dial(ctx, s.asteriskAMI)
	if err != nil {
		writeError(w, http.StatusBadGateway, "ami_unreachable", err.Error())
		return
	}
	defer conn.Close()

	channels, err := s.readAsteriskChannels(ctx, conn)
	if err != nil {
		writeError(w, http.StatusBadGateway, "channels_failed", err.Error())
		return
	}
	found := false
	for _, live := range channels {
		if live.Name == channel {
			found = true
			break
		}
	}
	if !found {
		// Also the answer for a regular expression, which can never equal a
		// real channel name.
		writeError(w, http.StatusNotFound, "channel_not_found",
			"no channel named "+channel+" is up; it may have ended already")
		return
	}
	response, err := conn.Action(ctx, "Hangup", ami.Message{
		"Channel": channel, "Cause": strconv.Itoa(cause),
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "hangup_failed", err.Error())
		return
	}
	if !strings.EqualFold(response.Get("Response"), "Success") {
		writeError(w, http.StatusBadGateway, "hangup_failed", response.First("Message", "Response"))
		return
	}
	s.recordAudit(r.Context(), "admin", "asterisk.channel.hangup", "asterisk", channel, "success", "")
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"hungup": true, "channel": channel, "cause": cause,
		"message": response.First("Message"),
	}})
}
