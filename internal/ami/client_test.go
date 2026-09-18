package ami

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeManager is an AMI server that replies from a script keyed by action
// name. It exists because the alternative is testing against a real Asterisk,
// which no CI has.
type fakeManager struct {
	listener net.Listener
	// reply is called with the action name and its fields, and returns the
	// raw bytes to write back. ActionID substitution is the test's job.
	reply func(action string, fields Message, actionID string) string
}

func startFakeManager(t *testing.T, reply func(string, Message, string) string) *fakeManager {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	manager := &fakeManager{listener: listener, reply: reply}
	t.Cleanup(func() { _ = listener.Close() })
	go manager.serve()
	return manager
}

func (m *fakeManager) address() string { return m.listener.Addr().String() }

func (m *fakeManager) serve() {
	for {
		conn, err := m.listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			// Asterisk announces itself with a bare line before anything else.
			if _, err := conn.Write([]byte("Asterisk Call Manager/8.0.0\r\n")); err != nil {
				return
			}
			reader := bufio.NewReader(conn)
			for {
				message, err := readMessage(reader)
				if err != nil {
					return
				}
				action := message.Get("Action")
				if strings.EqualFold(action, "Logoff") {
					return
				}
				if _, err := conn.Write([]byte(m.reply(action, message, message.Get("ActionID")))); err != nil {
					return
				}
			}
		}()
	}
}

func okLogin(action string, _ Message, id string) string {
	if strings.EqualFold(action, "Login") {
		return "Response: Success\r\nActionID: " + id + "\r\nMessage: Authentication accepted\r\n\r\n"
	}
	return "Response: Error\r\nActionID: " + id + "\r\nMessage: unexpected\r\n\r\n"
}

func dialFake(t *testing.T, manager *fakeManager) *Conn {
	t.Helper()
	conn, err := Dial(context.Background(), Options{
		Address: manager.address(), Username: "vocat", Secret: "secret", Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestDialLogsIn(t *testing.T) {
	var sawUsername, sawEvents string
	manager := startFakeManager(t, func(action string, fields Message, id string) string {
		if strings.EqualFold(action, "Login") {
			sawUsername = fields.Get("Username")
			sawEvents = fields.Get("Events")
		}
		return okLogin(action, fields, id)
	})
	dialFake(t, manager)
	if sawUsername != "vocat" {
		t.Errorf("login sent Username %q", sawUsername)
	}
	// Events off matters: with them on, unsolicited events interleave with
	// replies and every read has to be filtered.
	if !strings.EqualFold(sawEvents, "off") {
		t.Errorf("login sent Events %q, want off", sawEvents)
	}
}

func TestDialReportsRejectedLogin(t *testing.T) {
	manager := startFakeManager(t, func(_ string, _ Message, id string) string {
		return "Response: Error\r\nActionID: " + id + "\r\nMessage: Authentication failed\r\n\r\n"
	})
	_, err := Dial(context.Background(), Options{
		Address: manager.address(), Username: "vocat", Secret: "wrong", Timeout: 2 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "Authentication failed") {
		t.Fatalf("Dial error = %v; want Asterisk's own reason", err)
	}
	// The secret must never reach a log or an error string.
	if err != nil && strings.Contains(err.Error(), "wrong") {
		t.Fatal("the error leaked the secret")
	}
}

func TestDialWithoutAnAddressIsNotAnError(t *testing.T) {
	_, err := Dial(context.Background(), Options{Address: "  "})
	if err != ErrNotConfigured {
		t.Fatalf("Dial with no address = %v; want ErrNotConfigured", err)
	}
}

// Every AMI listing has the same shape: a Success reply, a run of events, and
// a terminator carrying EventList: Complete.
func TestListCollectsEventsUntilComplete(t *testing.T) {
	manager := startFakeManager(t, func(action string, fields Message, id string) string {
		if strings.EqualFold(action, "Login") {
			return okLogin(action, fields, id)
		}
		return "Response: Success\r\nActionID: " + id + "\r\nEventList: start\r\n\r\n" +
			"Event: EndpointList\r\nActionID: " + id + "\r\nObjectName: vocat\r\nDeviceState: Not in use\r\n\r\n" +
			"Event: EndpointList\r\nActionID: " + id + "\r\nObjectName: 1001\r\nDeviceState: Unavailable\r\n\r\n" +
			"Event: EndpointListComplete\r\nActionID: " + id + "\r\nEventList: Complete\r\nListItems: 2\r\n\r\n"
	})
	items, err := dialFake(t, manager).List(context.Background(), "PJSIPShowEndpoints", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d endpoints, want 2", len(items))
	}
	if items[0].Get("ObjectName") != "vocat" || items[1].Get("DeviceState") != "Unavailable" {
		t.Fatalf("endpoints = %v", items)
	}
	// The terminator is bookkeeping, not an endpoint.
	for _, item := range items {
		if strings.HasSuffix(item.Get("Event"), "Complete") {
			t.Fatal("the Complete event was returned as an item")
		}
	}
}

// A listing that fails must say why rather than return an empty list, which
// would render as "no endpoints" and send someone looking in the wrong place.
func TestListReportsAFailedAction(t *testing.T) {
	manager := startFakeManager(t, func(action string, fields Message, id string) string {
		if strings.EqualFold(action, "Login") {
			return okLogin(action, fields, id)
		}
		return "Response: Error\r\nActionID: " + id + "\r\nMessage: Unknown action\r\n\r\n"
	})
	_, err := dialFake(t, manager).List(context.Background(), "PJSIPShowNonsense", nil)
	if err == nil || !strings.Contains(err.Error(), "Unknown action") {
		t.Fatalf("List error = %v; want the reason", err)
	}
}

// Anything not carrying this action's ID must be skipped, or a stray message
// is mistaken for the reply.
func TestRepliesAreMatchedByActionID(t *testing.T) {
	manager := startFakeManager(t, func(action string, fields Message, id string) string {
		if strings.EqualFold(action, "Login") {
			return okLogin(action, fields, id)
		}
		return "Response: Success\r\nActionID: someone-else\r\nMessage: not yours\r\n\r\n" +
			"Response: Success\r\nActionID: " + id + "\r\nMessage: yours\r\n\r\n"
	})
	reply, err := dialFake(t, manager).Action(context.Background(), "Ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Get("Message") != "yours" {
		t.Fatalf("reply = %v; want the one matching our ActionID", reply)
	}
}

func TestMessageLookupIgnoresCase(t *testing.T) {
	message := Message{"ObjectName": "vocat", "devicestate": "Not in use"}
	if message.Get("objectname") != "vocat" || message.Get("DeviceState") != "Not in use" {
		t.Fatal("case-insensitive lookup failed")
	}
	// First is how callers survive a field Asterisk renamed between versions.
	if got := message.First("Missing", "Absent", "ObjectName"); got != "vocat" {
		t.Fatalf("First = %q", got)
	}
	if got := message.First("Missing", "Absent"); got != "" {
		t.Fatalf("First with no match = %q", got)
	}
}
