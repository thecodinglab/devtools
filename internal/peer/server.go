package peer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Server answers queries for one machine and serves its snapshot.
type Server struct {
	// Announce is sent to every allowed query; its Port is filled from the
	// listener.
	Announce Announce
	// Snapshot reads the machine's projects and sessions.
	Snapshot func(context.Context) (Snapshot, error)
	// Fresh is how long a snapshot is reused, so the queries of several
	// machines opening their switchers at once read the disk only once.
	Fresh time.Duration
	Logf  func(format string, args ...any)

	mu     sync.Mutex
	cached Snapshot
	at     time.Time
}

// Listen opens the UDP and TCP sockets of addr, a host:port, on the same
// port, the one TCP got when the port is 0.
func Listen(addr string) (net.PacketConn, net.Listener, error) {
	tcp, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("listen for snapshots: %w", err)
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		tcp.Close()
		return nil, nil, err
	}
	udp, err := net.ListenPacket("udp4", Addr(host, tcp.Addr().(*net.TCPAddr).Port))
	if err != nil {
		tcp.Close()
		return nil, nil, fmt.Errorf("listen for queries: %w", err)
	}
	return udp, tcp, nil
}

// Serve answers on udp and tcp until ctx is done.
func (s *Server) Serve(ctx context.Context, udp net.PacketConn, tcp net.Listener) error {
	if s.Announce.Port == 0 {
		if addr, ok := tcp.Addr().(*net.TCPAddr); ok {
			s.Announce.Port = addr.Port
		}
	}
	s.Announce.Protocol = Protocol
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+snapshotPath, s.serveSnapshot)
	httpServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	errs := make(chan error, 2)
	go func() { errs <- s.answer(udp) }()
	go func() { errs <- httpServer.Serve(tcp) }()
	var err error
	select {
	case <-ctx.Done():
	case err = <-errs:
	}
	udp.Close()
	shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	httpServer.Shutdown(shutdown)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		err = nil
	}
	return err
}

func (s *Server) answer(conn net.PacketConn) error {
	reply, err := json.Marshal(s.Announce)
	if err != nil {
		return err
	}
	buf := make([]byte, 1024)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			return err
		}
		udpAddr, ok := addr.(*net.UDPAddr)
		if !ok || !allowed(udpAddr.IP) || !parseQuery(buf[:n]) {
			continue
		}
		if _, err := conn.WriteTo(reply, addr); err != nil {
			s.logf("answer %s: %v", addr, err)
		}
	}
}

func (s *Server) serveSnapshot(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !allowed(net.ParseIP(host)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	snapshot, err := s.snapshot(r.Context())
	if err != nil {
		s.logf("snapshot for %s: %v", r.RemoteAddr, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snapshot)
}

// snapshot reads the machine, or reuses a read young enough; requests
// arriving while one reads wait for it.
func (s *Server) snapshot(ctx context.Context) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.at.IsZero() && time.Since(s.at) < s.Fresh {
		return s.cached, nil
	}
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Name = s.Announce.Name
	snapshot.Hostname = s.Announce.Hostname
	snapshot.SSH = s.Announce.SSH
	s.cached, s.at = snapshot, time.Now()
	return snapshot, nil
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// Addr joins a host and the port, defaulting the port.
func Addr(host string, port int) string {
	if port == 0 {
		port = DefaultPort
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}
