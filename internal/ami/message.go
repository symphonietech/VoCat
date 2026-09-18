// Package ami speaks Asterisk's Manager Interface.
//
// VoCat uses it for two things the trunk cannot do for itself: reading live
// Asterisk state (which softphones are registered, whether the trunk is
// reachable) and asking Asterisk to reload its configuration. Both run over
// loopback -- VoCat and Asterisk share the host network namespace -- so the
// credentials never cross the LAN.
//
// Deliberately not an event framework. Every call opens a connection, logs
// in with events disabled, does its work and closes. A status page polling
// every few seconds over loopback does not justify a persistent connection
// with a reconnect state machine, and short-lived connections cannot leak
// goroutines or wedge in a half-authenticated state.
package ami

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// maxMessageFields bounds one message. AMI is trusted (it is our own Asterisk
// on loopback), but a bound turns a confused peer into an error rather than
// unbounded memory.
const maxMessageFields = 512

// Message is one AMI message: the key/value pairs between two blank lines.
// Keys keep the casing Asterisk sent, because they are surfaced to the API
// as-is; lookups are case-insensitive because the documented casing and the
// wire casing do not always agree across versions.
type Message map[string]string

// Get returns the first value whose key matches name, ignoring case.
func (m Message) Get(name string) string {
	if value, ok := m[name]; ok {
		return value
	}
	for key, value := range m {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

// First returns the value of the first name that is present, which is how the
// callers cope with fields Asterisk has renamed between versions without
// hard-coding one spelling and silently showing nothing.
func (m Message) First(names ...string) string {
	for _, name := range names {
		if value := m.Get(name); value != "" {
			return value
		}
	}
	return ""
}

// Keys returns the field names in sorted order, for stable output.
func (m Message) Keys() []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

var errMalformed = errors.New("ami: malformed message")

// readMessage reads one message, ending at a blank line. io.EOF is returned
// only when the stream ends before any field was read, so a caller can tell a
// clean close from a truncated message.
func readMessage(reader *bufio.Reader) (Message, error) {
	message := Message{}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if len(message) == 0 {
				return nil, err
			}
			// A truncated final message is not a clean close.
			return nil, fmt.Errorf("ami: connection ended mid-message: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(message) == 0 {
				// Blank lines between messages; keep reading.
				continue
			}
			return message, nil
		}
		colon := strings.Index(line, ":")
		if colon <= 0 {
			// Asterisk emits a bare banner line on connect and occasional
			// free-text continuation lines inside Response bodies. Neither is
			// a field, and neither is a reason to fail.
			continue
		}
		if len(message) >= maxMessageFields {
			return nil, errMalformed
		}
		key := strings.TrimSpace(line[:colon])
		value := strings.TrimSpace(line[colon+1:])
		if _, exists := message[key]; exists {
			// Repeated keys are rare here and never meaningful for the
			// fields VoCat reads; keeping the first is stable and avoids a
			// later value quietly overwriting an earlier one.
			continue
		}
		message[key] = value
	}
}

// writeMessage renders an action. Field order is not significant to Asterisk,
// but Action goes first because that is what every example and packet capture
// shows, and a reader diffing a capture should not have to look for it.
func writeMessage(writer io.Writer, action string, fields Message) error {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Action: %s\r\n", action)
	for _, key := range fields.Keys() {
		if strings.EqualFold(key, "Action") {
			continue
		}
		fmt.Fprintf(&builder, "%s: %s\r\n", key, fields[key])
	}
	builder.WriteString("\r\n")
	_, err := io.WriteString(writer, builder.String())
	return err
}
