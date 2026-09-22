package siptrunk

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Options configures a trunk listener.
type Options struct {
	// Address is the UDP address to listen on. Bind it to a loopback or
	// private address: a trunk authorises by source IP rather than by
	// credentials, so an interface reachable from the internet would expose
	// SIM-backed calling to whoever can spoof a packet.
	Address string
	// Peers lists the source addresses allowed to send requests. A request
	// from anywhere else is dropped without a response, so a scanner learns
	// nothing about whether anything is listening. Entries are plain IPs or
	// CIDR prefixes; an empty list accepts nothing.
	Peers  []string
	Logger *slog.Logger
	// Gateway places the calls the trunk accepts. Leaving it nil keeps the
	// listener as a reachable peer that answers OPTIONS and refuses calls,
	// which is what a deployment without VoWiFi-capable devices should look
	// like from the PBX.
	Gateway Gateway
	// PBX is where inbound calls are offered: the SIP address of the PBX in
	// front, normally 127.0.0.1:5060. Empty leaves the inbound direction off,
	// so a deployment that only places calls never has VoCat sending INVITEs
	// at whatever happens to be listening there.
	PBX string
}

// Server answers SIP on behalf of VoCat's SIM-backed calling.
type Server struct {
	conn    *net.UDPConn
	peers   []netip.Prefix
	logger  *slog.Logger
	gateway Gateway
	wg      sync.WaitGroup
	closing chan struct{}
	once    sync.Once

	// pbx is the resolved Options.PBX, nil when inbound is off.
	pbx *net.UDPAddr

	mu sync.Mutex
	// dialogs are calls the PBX placed through the trunk, where VoCat is the
	// UAS. outbounds are calls the trunk offered to the PBX, where it is the
	// UAC. Two maps rather than one, because the roles differ in every
	// direction that matters: who sends the BYE, who retransmits, and which
	// side a response belongs to.
	dialogs   map[string]*dialog
	outbounds map[string]*outbound
	// smsWaiters are the SendSMS calls waiting on a final response. A
	// MESSAGE is out of dialog, so there is nothing to put in either map
	// above and nothing to tear down: one request, one response, done.
	smsWaiters map[string]chan *Response
	// smsServed remembers the MESSAGE transactions this side is handling or
	// has answered, so a retransmission is absorbed rather than submitted a
	// second time. Required of any UAS, and unavoidable here: a MESSAGE is a
	// non-INVITE transaction, so the PBX retransmits from T1 onwards until it
	// has a final response, while a modem submission takes seconds.
	smsServed map[string]*smsTransaction
	limiter   responseLimiter
}

// ParsePeers converts textual peer entries into prefixes. A bare address
// becomes a single-host prefix, so "127.0.0.1" and "127.0.0.1/32" mean the
// same thing.
func ParsePeers(entries []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			prefix, err := netip.ParsePrefix(entry)
			if err != nil {
				return nil, fmt.Errorf("siptrunk: invalid peer prefix %q: %w", entry, err)
			}
			prefixes = append(prefixes, prefix.Masked())
			continue
		}
		address, err := netip.ParseAddr(entry)
		if err != nil {
			return nil, fmt.Errorf("siptrunk: invalid peer address %q: %w", entry, err)
		}
		prefixes = append(prefixes, netip.PrefixFrom(address, address.BitLen()))
	}
	return prefixes, nil
}

// Listen starts a trunk listener. It returns an error rather than starting a
// listener that would accept nothing, since a trunk with no peers is a
// configuration mistake rather than a hardened default.
func Listen(options Options) (*Server, error) {
	peers, err := ParsePeers(options.Peers)
	if err != nil {
		return nil, err
	}
	if len(peers) == 0 {
		return nil, errors.New("siptrunk: no trusted peers configured")
	}
	address, err := net.ResolveUDPAddr("udp", options.Address)
	if err != nil {
		return nil, fmt.Errorf("siptrunk: resolve %q: %w", options.Address, err)
	}
	conn, err := net.ListenUDP("udp", address)
	if err != nil {
		return nil, fmt.Errorf("siptrunk: listen on %q: %w", options.Address, err)
	}
	var pbx *net.UDPAddr
	if address := strings.TrimSpace(options.PBX); address != "" {
		if pbx, err = net.ResolveUDPAddr("udp", address); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("siptrunk: resolve PBX address %q: %w", address, err)
		}
	}
	server := &Server{
		conn:      conn,
		peers:     peers,
		logger:    options.Logger,
		gateway:   options.Gateway,
		pbx:       pbx,
		closing:   make(chan struct{}),
		dialogs:   make(map[string]*dialog),
		outbounds: make(map[string]*outbound),
	}
	server.wg.Add(1)
	go server.serve()
	return server, nil
}

// LocalAddr reports the bound address, which the caller logs and tests read to
// find the port the kernel picked.
func (s *Server) LocalAddr() *net.UDPAddr {
	return s.conn.LocalAddr().(*net.UDPAddr)
}

// Close stops the listener and waits for the read loop to finish. Calls in
// progress are ended first: a bridge whose process is going away should hang
// up rather than leave the PBX holding a dialog and the SIM holding a call.
func (s *Server) Close() error {
	s.once.Do(func() {
		close(s.closing)
		for _, current := range s.activeDialogs() {
			current.teardown(true)
		}
		for _, current := range s.activeOutbounds() {
			current.teardown(true)
		}
		_ = s.conn.Close()
	})
	s.wg.Wait()
	return nil
}

func (s *Server) activeDialogs() []*dialog {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := make([]*dialog, 0, len(s.dialogs))
	for _, entry := range s.dialogs {
		current = append(current, entry)
	}
	return current
}

func (s *Server) serve() {
	defer s.wg.Done()
	buffer := make([]byte, maxMessageBytes)
	for {
		count, from, err := s.conn.ReadFromUDP(buffer)
		if err != nil {
			select {
			case <-s.closing:
				return
			default:
			}
			// A read error on an unclosed socket is not recoverable per-packet;
			// stopping beats spinning on it.
			s.log("siptrunk read failed", "error", err)
			return
		}
		packet := make([]byte, count)
		copy(packet, buffer[:count])
		s.handle(context.Background(), packet, from)
	}
}

// Trusted reports whether a source address is allowed to reach the trunk.
func (s *Server) Trusted(address net.IP) bool {
	parsed, ok := netip.AddrFromSlice(address)
	if !ok {
		return false
	}
	parsed = parsed.Unmap()
	for _, prefix := range s.peers {
		if prefix.Contains(parsed) {
			return true
		}
	}
	return false
}

func (s *Server) handle(_ context.Context, packet []byte, from *net.UDPAddr) {
	if !s.Trusted(from.IP) {
		// Silence rather than 403: an untrusted sender should not be able to
		// confirm that anything is here.
		s.log("siptrunk rejected an untrusted peer", "peer", from.String())
		return
	}
	if IsResponse(packet) {
		// A response belongs to an INVITE the trunk sent, offering a call
		// that arrived on a SIM. Responses to its own BYE have no call left
		// to deliver to and are dropped there.
		s.deliverResponse(packet)
		return
	}
	request, err := ParseRequest(packet)
	if err != nil {
		s.log("siptrunk rejected a malformed request", "peer", from.String(), "error", err)
		return
	}
	if s.dispatch(request, from) {
		return
	}
	status, reason := s.route(request)
	if status == 0 {
		// route decided this request gets no response at all.
		return
	}
	response, err := BuildResponse(request, status, reason, newTag(), nil)
	if err != nil {
		s.log("siptrunk could not build a response", "peer", from.String(), "error", err)
		return
	}
	if !s.send(response, from) {
		return
	}
	if request.Method == "OPTIONS" {
		// A PBX qualifies its peers on a timer, so these arrive for ever. At
		// info level they would bury the handful of lines that describe an
		// actual call.
		s.logDebug("siptrunk handled a request",
			"peer", from.String(), "method", request.Method, "status", status)
		return
	}
	s.log("siptrunk handled a request",
		"peer", from.String(), "method", request.Method, "status", status)
}

// route decides the status for a request dispatch did not take: OPTIONS, and
// the call methods when no gateway is configured or the trunk does not
// implement them. Answering 501 rather than staying silent tells the peer the
// trunk is alive but cannot do this, instead of making it wait for a timeout.
func (s *Server) route(request *Request) (int, string) {
	switch request.Method {
	case "OPTIONS":
		return 200, "OK"
	case "INVITE", "BYE", "CANCEL", "UPDATE", "INFO", "MESSAGE":
		return 501, "Not Implemented"
	case "ACK":
		// Unreachable: dispatch takes every ACK. Listed anyway so that nobody
		// adds it to the 501 group above, which is what caused a 501-to-ACK
		// packet storm once already.
		return 0, ""
	default:
		return 405, "Method Not Allowed"
	}
}

// mediaAddress picks the local address for the RTP leg toward the PBX. The
// listening address is used when it names an interface; when the trunk is
// bound to a wildcard it is the route to the peer that decides, which an
// unconnected socket reports without sending anything.
func (s *Server) mediaAddress(peer *net.UDPAddr) net.IP {
	if local := s.LocalAddr(); local.IP != nil && !local.IP.IsUnspecified() {
		return local.IP
	}
	probe, err := net.DialUDP("udp", nil, peer)
	if err != nil {
		return nil
	}
	defer probe.Close()
	return probe.LocalAddr().(*net.UDPAddr).IP
}

// viaHost is the host:port the trunk puts in a Via and a Contact, so responses
// and in-dialog requests come back to this listener.
func (s *Server) viaHost(peer *net.UDPAddr) string {
	local := s.LocalAddr()
	host := local.IP
	if host == nil || host.IsUnspecified() {
		host = s.mediaAddress(peer)
	}
	if host == nil {
		return fmt.Sprintf("127.0.0.1:%d", local.Port)
	}
	return net.JoinHostPort(host.String(), strconv.Itoa(local.Port))
}

func (s *Server) contactURI(peer *net.UDPAddr) string {
	return "sip:vocat@" + s.viaHost(peer)
}

// send writes one packet to a peer, subject to the runaway cap. It reports
// whether the packet went out, so callers do not log a success that did not
// happen.
func (s *Server) send(packet []byte, to *net.UDPAddr) bool {
	peer := to.String()
	allowed, firstRefusal := s.limiter.allow(peer, time.Now())
	if !allowed {
		if firstRefusal {
			s.log("siptrunk is dropping packets to a peer, over "+
				"the per-second cap; this is a loop, not load",
				"peer", peer, "cap", maxResponsesPerSecond)
		}
		return false
	}
	if _, err := s.conn.WriteToUDP(packet, to); err != nil {
		s.log("siptrunk could not send a packet", "peer", peer, "error", err)
		return false
	}
	return true
}

func (s *Server) log(message string, args ...any) {
	if s.logger == nil {
		return
	}
	s.logger.Info(message, append([]any{"category", "siptrunk"}, args...)...)
}

// logDebug is for the traffic a healthy trunk generates on a timer, which is
// worth having available but not worth reading every minute.
func (s *Server) logDebug(message string, args ...any) {
	if s.logger == nil {
		return
	}
	s.logger.Debug(message, append([]any{"category", "siptrunk"}, args...)...)
}

func newTag() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return "vocat"
	}
	return hex.EncodeToString(buffer)
}
