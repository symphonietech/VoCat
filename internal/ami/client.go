package ami

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// Options describes how to reach Asterisk's manager interface.
type Options struct {
	// Address is host:port, normally 127.0.0.1:5038. Bind manager.conf to
	// loopback: AMI is a privileged interface and its password crosses the
	// wire in the clear on a plain connection.
	Address  string
	Username string
	Secret   string
	// Timeout bounds the whole exchange, connect through logoff. AMI is on
	// loopback, so a slow reply means Asterisk is wedged, not that the
	// network is far away.
	Timeout time.Duration
}

// DefaultTimeout is generous for loopback and still short enough that a
// status page does not hang on a stuck Asterisk.
const DefaultTimeout = 5 * time.Second

// Conn is one authenticated AMI session.
type Conn struct {
	conn     net.Conn
	reader   *bufio.Reader
	deadline time.Time
}

// ErrNotConfigured is returned when no manager address is set, so callers can
// report "not configured" rather than a connection error the operator would
// waste time on.
var ErrNotConfigured = errors.New("ami: no manager address configured")

// Dial opens a session and logs in. Events are disabled for the session: this
// client only sends actions and reads their replies, and unsolicited events
// would interleave with those replies for no benefit.
func Dial(ctx context.Context, options Options) (*Conn, error) {
	address := strings.TrimSpace(options.Address)
	if address == "" {
		return nil, ErrNotConfigured
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	dialer := net.Dialer{Timeout: timeout}
	raw, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("ami: connect to %s: %w", address, err)
	}
	deadline := time.Now().Add(timeout)
	if err := raw.SetDeadline(deadline); err != nil {
		_ = raw.Close()
		return nil, err
	}
	conn := &Conn{conn: raw, reader: bufio.NewReader(raw), deadline: deadline}

	// The banner is a bare line, not a message; readMessage skips it, but it
	// has to be consumed before the first reply is read.
	if _, err := conn.reader.ReadString('\n'); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("ami: read greeting: %w", err)
	}
	response, err := conn.Action(ctx, "Login", Message{
		"Username": options.Username,
		"Secret":   options.Secret,
		"Events":   "off",
	})
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	if !strings.EqualFold(response.Get("Response"), "Success") {
		_ = raw.Close()
		// The message is Asterisk's own, and never contains the secret.
		return nil, fmt.Errorf("ami: login rejected: %s", response.First("Message", "Response"))
	}
	return conn, nil
}

// Close logs off and closes the connection. A failed logoff is not reported:
// the connection is going away either way, and Asterisk cleans up.
func (c *Conn) Close() error {
	_ = writeMessage(c.conn, "Logoff", nil)
	return c.conn.Close()
}

// Action sends one action and returns its reply.
func (c *Conn) Action(ctx context.Context, action string, fields Message) (Message, error) {
	id, err := c.send(ctx, action, fields)
	if err != nil {
		return nil, err
	}
	return c.awaitReply(id)
}

// List runs an action whose reply is a series of events terminated by a
// "<something>Complete" event, which is how AMI returns every listing --
// PJSIPShowEndpoints, PJSIPShowContacts and the rest. The terminating event
// is not included in the result.
func (c *Conn) List(ctx context.Context, action string, fields Message) ([]Message, error) {
	id, err := c.send(ctx, action, fields)
	if err != nil {
		return nil, err
	}
	reply, err := c.awaitReply(id)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(reply.Get("Response"), "Success") {
		return nil, fmt.Errorf("ami: %s failed: %s", action, reply.First("Message", "Response"))
	}
	var items []Message
	for {
		message, err := readMessage(c.reader)
		if err != nil {
			return nil, fmt.Errorf("ami: %s listing: %w", action, err)
		}
		if actionID := message.Get("ActionID"); actionID != "" && actionID != id {
			continue
		}
		// Asterisk marks the final event with EventList: Complete. Matching
		// on that rather than on a per-action event name means one
		// implementation covers every listing action.
		if strings.EqualFold(message.Get("EventList"), "Complete") ||
			strings.HasSuffix(message.Get("Event"), "Complete") {
			return items, nil
		}
		if message.Get("Event") == "" {
			continue
		}
		items = append(items, message)
		if len(items) > maxListItems {
			return nil, fmt.Errorf("ami: %s returned more than %d items", action, maxListItems)
		}
	}
}

// maxListItems bounds a listing. A PBX this size has tens of endpoints; tens
// of thousands means something is wrong and the caller should hear about it
// rather than accumulate.
const maxListItems = 5000

func (c *Conn) send(ctx context.Context, action string, fields Message) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	id := newActionID()
	outgoing := Message{"ActionID": id}
	for key, value := range fields {
		outgoing[key] = value
	}
	if err := writeMessage(c.conn, action, outgoing); err != nil {
		return "", fmt.Errorf("ami: send %s: %w", action, err)
	}
	return id, nil
}

// awaitReply skips anything that is not the reply to id. With events off
// there should be nothing to skip, but a stray message must not be mistaken
// for the reply.
func (c *Conn) awaitReply(id string) (Message, error) {
	for {
		message, err := readMessage(c.reader)
		if err != nil {
			return nil, fmt.Errorf("ami: read reply: %w", err)
		}
		if message.Get("Response") == "" {
			continue
		}
		if actionID := message.Get("ActionID"); actionID != "" && actionID != id {
			continue
		}
		return message, nil
	}
}

func newActionID() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return "vocat"
	}
	return hex.EncodeToString(buffer)
}
