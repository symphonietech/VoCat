package siptrunk

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// G.711 is the only codec on this leg. VoCat's IMS media layer decodes A-law
// and mu-law and nothing else, so offering more would mean advertising what
// the other side of the bridge cannot carry. It is also what every PBX
// supports without a codec module.
const (
	payloadPCMU = 0
	payloadPCMA = 8
	clockRate   = 8000
	// payloadEvent is the telephone-event type VoCat offers. Nothing fixes it
	// -- it is a dynamic type -- but 101 is what every PBX uses, and matching
	// the common choice avoids a renegotiation nobody benefits from.
	payloadEvent = 101
)

// mediaOffer is what the PBX asked for: where to send audio, and which of the
// two G.711 flavours it prefers.
type mediaOffer struct {
	Address net.IP
	Port    int
	Payload byte
	// EventPayload is the RFC 4733 telephone-event type, or zero when the
	// offer has none. Without it a call cannot carry keypad digits: G.711
	// would pass the tones, but the PBX is not listening for audio tones and
	// neither is the carrier on the other leg.
	EventPayload byte
	// SendOnly records a=sendonly / a=recvonly, which a PBX uses for hold.
	Direction string
}

var errNoAudio = errors.New("siptrunk: SDP offers no usable audio stream")

// ParseOffer reads the connection address, port and codec from an SDP offer.
// It takes the first G.711 payload the offer lists in preference order, and
// refuses an offer with neither, since transcoding is out of scope.
func ParseOffer(body []byte) (mediaOffer, error) {
	offer := mediaOffer{Direction: "sendrecv"}
	var sessionAddress, mediaAddress net.IP
	inAudio := false
	chosePayload := false
	for _, line := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
		if len(line) < 2 || line[1] != '=' {
			continue
		}
		value := strings.TrimSpace(line[2:])
		switch line[0] {
		case 'c':
			fields := strings.Fields(value)
			if len(fields) != 3 {
				continue
			}
			// A connection line before the first m= belongs to the session and
			// applies to every stream that does not override it.
			address := net.ParseIP(strings.SplitN(fields[2], "/", 2)[0])
			if inAudio {
				mediaAddress = address
			} else {
				sessionAddress = address
			}
		case 'm':
			fields := strings.Fields(value)
			if len(fields) < 4 || fields[0] != "audio" {
				inAudio = false
				continue
			}
			inAudio = true
			port, err := strconv.Atoi(fields[1])
			if err != nil || port <= 0 || port > 65535 {
				return mediaOffer{}, fmt.Errorf("siptrunk: invalid media port %q", fields[1])
			}
			offer.Port = port
			// PCMU's payload type is 0, which is also byte's zero value, so a
			// separate flag is what distinguishes "the offer chose PCMU" from
			// "the offer listed no codec we can carry".
			for _, format := range fields[3:] {
				payload, err := strconv.Atoi(format)
				if err != nil {
					continue
				}
				if payload == payloadPCMU || payload == payloadPCMA {
					offer.Payload = byte(payload)
					chosePayload = true
					break
				}
			}
		case 'a':
			if !inAudio {
				continue
			}
			if mapping, found := strings.CutPrefix(value, "rtpmap:"); found {
				fields := strings.Fields(mapping)
				if len(fields) == 2 && strings.HasPrefix(strings.ToLower(fields[1]), "telephone-event") {
					if number, err := strconv.Atoi(fields[0]); err == nil && number > 0 && number < 128 {
						offer.EventPayload = byte(number)
					}
				}
				continue
			}
			switch value {
			case "sendonly", "recvonly", "inactive", "sendrecv":
				offer.Direction = value
			}
		}
	}
	if offer.Port == 0 {
		return mediaOffer{}, errNoAudio
	}
	if !chosePayload {
		return mediaOffer{}, errors.New("siptrunk: SDP offers no G.711 payload")
	}
	offer.Address = mediaAddress
	if offer.Address == nil {
		offer.Address = sessionAddress
	}
	if offer.Address == nil || offer.Address.IsUnspecified() {
		return mediaOffer{}, errors.New("siptrunk: SDP has no usable connection address")
	}
	return offer, nil
}

// BuildAnswer renders the SDP VoCat returns for an accepted offer, agreeing to
// the single payload type the offer chose. Answering with one format rather
// than a list keeps the PBX from renegotiating to something unsupported.
func BuildAnswer(local net.IP, port int, payload, eventPayload byte) []byte {
	family := "IP4"
	if local.To4() == nil {
		family = "IP6"
	}
	name := "PCMU"
	if payload == payloadPCMA {
		name = "PCMA"
	}
	session := time.Now().UnixNano()
	formats := strconv.Itoa(int(payload))
	// Keyed to the number the offer chose rather than VoCat's own: an answer
	// may only accept a payload type the offer listed.
	if eventPayload != 0 {
		formats += " " + strconv.Itoa(int(eventPayload))
	}
	lines := []string{
		"v=0",
		fmt.Sprintf("o=- %d %d IN %s %s", session, session, family, local.String()),
		"s=VoCat",
		fmt.Sprintf("c=IN %s %s", family, local.String()),
		"t=0 0",
		fmt.Sprintf("m=audio %d RTP/AVP %s", port, formats),
		fmt.Sprintf("a=rtpmap:%d %s/%d", payload, name, clockRate),
	}
	if eventPayload != 0 {
		lines = append(lines,
			fmt.Sprintf("a=rtpmap:%d telephone-event/%d", eventPayload, clockRate),
			fmt.Sprintf("a=fmtp:%d 0-15", eventPayload),
		)
	}
	lines = append(lines, "a=ptime:20", "a=sendrecv", "")
	return []byte(strings.Join(lines, "\r\n"))
}

// BuildOffer renders the SDP VoCat sends when it offers a call arriving on a
// SIM to the PBX. Unlike the answer this lists both G.711 flavours: the trunk
// is asking rather than agreeing, and which one a PBX prefers is its own
// configuration. The leg takes its codec from whichever the answer picks.
func BuildOffer(local net.IP, port int) []byte {
	family := "IP4"
	if local.To4() == nil {
		family = "IP6"
	}
	session := time.Now().UnixNano()
	lines := []string{
		"v=0",
		fmt.Sprintf("o=- %d %d IN %s %s", session, session, family, local.String()),
		"s=VoCat",
		fmt.Sprintf("c=IN %s %s", family, local.String()),
		"t=0 0",
		fmt.Sprintf("m=audio %d RTP/AVP %d %d %d", port, payloadPCMU, payloadPCMA, payloadEvent),
		fmt.Sprintf("a=rtpmap:%d PCMU/%d", payloadPCMU, clockRate),
		fmt.Sprintf("a=rtpmap:%d PCMA/%d", payloadPCMA, clockRate),
		fmt.Sprintf("a=rtpmap:%d telephone-event/%d", payloadEvent, clockRate),
		fmt.Sprintf("a=fmtp:%d 0-15", payloadEvent),
		"a=ptime:20",
		"a=sendrecv",
		"",
	}
	return []byte(strings.Join(lines, "\r\n"))
}
