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
	"strings"
	"sync"
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
}

// Server answers SIP on behalf of VoCat's SIM-backed calling.
type Server struct {
	conn    *net.UDPConn
	peers   []netip.Prefix
	logger  *slog.Logger
	wg      sync.WaitGroup
	closing chan struct{}
	once    sync.Once
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
	server := &Server{conn: conn, peers: peers, logger: options.Logger, closing: make(chan struct{})}
	server.wg.Add(1)
	go server.serve()
	return server, nil
}

// LocalAddr reports the bound address, which the caller logs and tests read to
// find the port the kernel picked.
func (s *Server) LocalAddr() *net.UDPAddr {
	return s.conn.LocalAddr().(*net.UDPAddr)
}

// Close stops the listener and waits for the read loop to finish.
func (s *Server) Close() error {
	s.once.Do(func() {
		close(s.closing)
		_ = s.conn.Close()
	})
	s.wg.Wait()
	return nil
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
	request, err := ParseRequest(packet)
	if err != nil {
		s.log("siptrunk rejected a malformed request", "peer", from.String(), "error", err)
		return
	}
	status, reason := s.route(request)
	response, err := BuildResponse(request, status, reason, newTag(), nil)
	if err != nil {
		s.log("siptrunk could not build a response", "peer", from.String(), "error", err)
		return
	}
	if _, err := s.conn.WriteToUDP(response, from); err != nil {
		s.log("siptrunk could not send a response", "peer", from.String(), "error", err)
		return
	}
	s.log("siptrunk handled a request",
		"peer", from.String(), "method", request.Method, "status", status)
}

// route decides the status for a request. Call handling arrives in a later
// change; for now the trunk exists so a PBX can see it as a reachable peer,
// which is what OPTIONS answers.
func (s *Server) route(request *Request) (int, string) {
	switch request.Method {
	case "OPTIONS":
		return 200, "OK"
	case "INVITE", "ACK", "BYE", "CANCEL", "UPDATE", "INFO":
		// Honest about the gap: the peer learns the trunk is alive but cannot
		// yet place a call through it, instead of timing out on silence.
		return 501, "Not Implemented"
	default:
		return 405, "Method Not Allowed"
	}
}

func (s *Server) log(message string, args ...any) {
	if s.logger == nil {
		return
	}
	s.logger.Info(message, append([]any{"category", "siptrunk"}, args...)...)
}

func newTag() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return "vocat"
	}
	return hex.EncodeToString(buffer)
}
