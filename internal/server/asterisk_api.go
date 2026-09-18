package server

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"vocat/internal/ami"
)

// asteriskStatusTimeout bounds the whole AMI exchange. The page polls, so a
// wedged PBX must fail fast rather than pile up requests.
const asteriskStatusTimeout = 6 * time.Second

// asteriskField is one raw AMI field. A list of name/value pairs rather than
// an object, because the API client camelCases every JSON key it decodes --
// which would rewrite the very wire names this exists to expose.
type asteriskField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// rawFields renders an AMI message as stable, sorted pairs.
func rawFields(message ami.Message) []asteriskField {
	keys := message.Keys()
	fields := make([]asteriskField, 0, len(keys))
	for _, key := range keys {
		fields = append(fields, asteriskField{Name: key, Value: message[key]})
	}
	return fields
}

// asteriskContact is one registered location for an endpoint: where a
// softphone actually is, or where the trunk was last seen answering.
type asteriskContact struct {
	URI         string          `json:"uri,omitempty"`
	Status      string          `json:"status,omitempty"`
	RoundTripMS float64         `json:"roundtrip_ms,omitempty"`
	Expires     string          `json:"expires,omitempty"`
	UserAgent   string          `json:"user_agent,omitempty"`
	ViaAddress  string          `json:"via_address,omitempty"`
	Fields      []asteriskField `json:"fields,omitempty"`
}

// asteriskEndpoint is one PJSIP endpoint: the trunk toward VoCat, or a
// softphone account.
type asteriskEndpoint struct {
	Name           string            `json:"name"`
	AOR            string            `json:"aor,omitempty"`
	State          string            `json:"state,omitempty"`
	ActiveChannels string            `json:"active_channels,omitempty"`
	Transport      string            `json:"transport,omitempty"`
	Contacts       []asteriskContact `json:"contacts"`
	Registered     bool              `json:"registered"`
	// Reachable is true when a contact answered a qualify. The trunk has a
	// statically configured contact and never registers with anything, so
	// "registered" says nothing useful about it while "reachable" does.
	Reachable bool            `json:"reachable"`
	Fields    []asteriskField `json:"fields,omitempty"`
}

// handleAsteriskStatus reports what the PBX thinks is going on. It is
// read-only: nothing here changes Asterisk's configuration.
//
// Field names come from the Asterisk 22 manager documentation (AMI_Events
// EndpointList and ContactList). The documented name is tried first and other
// spellings after it: Asterisk has renamed manager fields between versions,
// the fallbacks cost one map lookup, and a row that blanks because a key
// moved is worse than one showing a value under an older name. The whole
// message is passed through too, so a field VoCat does not know about stays
// visible rather than dropped.
func (s *Server) handleAsteriskStatus(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	payload := map[string]any{
		"configured": strings.TrimSpace(s.asteriskAMI.Address) != "",
		"address":    s.asteriskAMI.Address,
		"endpoints":  []asteriskEndpoint{},
	}
	if !payload["configured"].(bool) {
		writeJSON(w, http.StatusOK, map[string]any{"data": payload})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), asteriskStatusTimeout)
	defer cancel()
	conn, err := ami.Dial(ctx, s.asteriskAMI)
	if err != nil {
		if errors.Is(err, ami.ErrNotConfigured) {
			payload["configured"] = false
		} else {
			// Reachability is a normal state to report, not a 500: the PBX
			// being down is exactly what someone opens this page to find out.
			payload["error"] = err.Error()
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": payload})
		return
	}
	defer conn.Close()
	payload["reachable"] = true

	if core, err := conn.Action(ctx, "CoreStatus", nil); err == nil {
		payload["core"] = map[string]string{
			"startup_time": core.First("CoreStartupTime", "CoreStartupDate"),
			"reload_time":  core.First("CoreReloadTime", "CoreReloadDate"),
			"calls":        core.First("CoreCurrentCalls"),
		}
	}
	if settings, err := conn.Action(ctx, "CoreSettings", nil); err == nil {
		payload["version"] = settings.First("AsteriskVersion", "Version")
	}

	endpoints, err := conn.List(ctx, "PJSIPShowEndpoints", nil)
	if err != nil {
		payload["error"] = err.Error()
		writeJSON(w, http.StatusOK, map[string]any{"data": payload})
		return
	}
	// A failed contact listing is not fatal: the endpoints alone already say
	// which accounts exist and what state Asterisk thinks they are in.
	contacts, contactsErr := conn.List(ctx, "PJSIPShowContacts", nil)
	if contactsErr != nil {
		payload["contacts_error"] = contactsErr.Error()
	}
	built, unpaired := buildAsteriskEndpoints(endpoints, contacts)
	payload["endpoints"] = built
	if len(unpaired) > 0 {
		payload["unpaired_contacts"] = unpaired
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": payload})
}

// buildAsteriskEndpoints pairs contacts to the endpoints that own them, and
// returns any contact it could not place so the caller can show it rather
// than drop it.
func buildAsteriskEndpoints(endpointEvents, contactEvents []ami.Message) ([]asteriskEndpoint, []asteriskContact) {
	result := make([]asteriskEndpoint, 0, len(endpointEvents))
	// byAOR is the fallback pairing. Contacts name their endpoint directly in
	// most versions, but where they do not, an AOR has the endpoint's name by
	// convention and is the only link available.
	byName := map[string]int{}
	// byURI pairs a contact that names neither an endpoint nor a matching
	// AOR, using the contact list the endpoint itself declares.
	byURI := map[string]int{}
	// declared records those URIs so an endpoint whose contact produced no
	// ContactList event still shows it, rather than looking contactless.
	declared := map[int][]string{}
	for _, event := range endpointEvents {
		name := event.First("ObjectName", "EndpointName", "Endpoint")
		if name == "" {
			continue
		}
		index := len(result)
		byName[strings.ToLower(name)] = index
		aor := event.First("Aor", "Aors", "AOR")
		// EndpointList carries the endpoint's contacts inline, as
		// "aor/uri" entries. That is the only link for a statically
		// configured contact, whose ContactList event names neither an
		// endpoint nor an AOR that matches -- which is why the trunk read as
		// having no contacts at all.
		for _, entry := range strings.Split(event.First("Contacts"), ",") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			if _, uri, found := strings.Cut(entry, "/"); found && uri != "" {
				if _, taken := byURI[uri]; !taken {
					byURI[uri] = index
				}
				declared[index] = append(declared[index], uri)
			}
		}
		// An endpoint's AORs are usually named after it, but not always, and
		// the AOR is the only link when a contact does not name its endpoint.
		for _, entry := range strings.Split(aor, ",") {
			if entry = strings.ToLower(strings.TrimSpace(entry)); entry != "" {
				if _, taken := byName[entry]; !taken {
					byName[entry] = index
				}
			}
		}
		result = append(result, asteriskEndpoint{
			Name:           name,
			AOR:            aor,
			State:          event.First("DeviceState", "State"),
			ActiveChannels: event.First("ActiveChannels"),
			Transport:      event.First("Transport"),
			Contacts:       []asteriskContact{},
			Fields:         rawFields(event),
		})
	}
	seen := map[string]bool{}
	unpaired := []asteriskContact{}
	for _, event := range contactEvents {
		contact := asteriskContact{
			URI:        event.First("Uri", "URI"),
			Status:     event.First("Status", "ContactStatus"),
			Expires:    event.First("ExpirationTime", "RegExpire", "Expiration"),
			UserAgent:  event.First("UserAgent"),
			ViaAddress: event.First("ViaAddr", "ViaAddress"),
			Fields:     rawFields(event),
		}
		if micro := event.First("RoundtripUsec", "RoundTripUsec"); micro != "" {
			if value, err := strconv.ParseFloat(micro, 64); err == nil {
				contact.RoundTripMS = value / 1000
			}
		}
		owner := event.First("Endpoint", "EndpointName")
		if owner == "" {
			// "1001/sip:1001@..." and plain "1001" are both seen; the part
			// before the slash is the AOR, which matches the endpoint name.
			aor := event.First("AOR", "Aor", "ObjectName")
			owner, _, _ = strings.Cut(aor, "/")
		}
		index, ok := byName[strings.ToLower(strings.TrimSpace(owner))]
		if !ok {
			index, ok = byURI[contact.URI]
		}
		if !ok {
			// A contact Asterisk returned that belongs to no endpoint VoCat
			// listed. Dropping it silently is how "why has this endpoint no
			// status" becomes unanswerable; surfacing it says whether the
			// contact was missing or merely mispaired.
			unpaired = append(unpaired, contact)
			continue
		}
		result[index].Contacts = append(result[index].Contacts, contact)
		seen[contact.URI] = true
		// Anything other than an explicitly unreachable contact counts as
		// present: a phone that has not been qualified yet reports an empty
		// status, and calling that "not registered" would be wrong.
		if !strings.EqualFold(contact.Status, "Unreachable") &&
			!strings.EqualFold(contact.Status, "Removed") {
			result[index].Registered = true
		}
		if strings.EqualFold(contact.Status, "Reachable") {
			result[index].Reachable = true
		}
	}
	// A contact the endpoint declared but no ContactList event covered is
	// still real -- a static trunk contact is the usual case. Showing it
	// without a status beats showing nothing.
	for index, uris := range declared {
		for _, uri := range uris {
			if seen[uri] {
				continue
			}
			result[index].Contacts = append(result[index].Contacts, asteriskContact{URI: uri})
			result[index].Registered = true
		}
	}
	sort.Slice(result, func(first, second int) bool {
		return strings.ToLower(result[first].Name) < strings.ToLower(result[second].Name)
	})
	return result, unpaired
}
