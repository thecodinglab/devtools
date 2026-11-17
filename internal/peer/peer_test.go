package peer

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"devtools/internal/ui"
)

func startServer(t *testing.T, announce Announce, snapshot func(context.Context) (Snapshot, error)) int {
	t.Helper()
	udp, tcp, err := listenFree(t)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	s := &Server{Announce: announce, Snapshot: snapshot, Fresh: time.Minute}
	go func() { done <- s.Serve(ctx, udp, tcp) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	return tcp.Addr().(*net.TCPAddr).Port
}

// listenFree finds a port free for both UDP and TCP on the loopback.
func listenFree(t *testing.T) (net.PacketConn, net.Listener, error) {
	t.Helper()
	var err error
	for range 20 {
		var tcp net.Listener
		tcp, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			continue
		}
		var udp net.PacketConn
		udp, err = net.ListenPacket("udp4", tcp.Addr().String())
		if err != nil {
			tcp.Close()
			continue
		}
		return udp, tcp, nil
	}
	return nil, nil, err
}

func collect(ch <-chan Result) []Result {
	var results []Result
	for r := range ch {
		results = append(results, r)
	}
	return results
}

func TestDiscoverFetchesSnapshotOfPeer(t *testing.T) {
	port := startServer(t, Announce{Name: "macmini", Hostname: "mini.local", SSH: "macmini"}, func(context.Context) (Snapshot, error) {
		return Snapshot{Multiplexer: "herdr", Items: []Item{{
			Kind: "worktree", Project: "xcloud", Worktree: "main", Label: "xcloud/main",
			Path: "/Users/f/dev/xcloud/main", Session: "xcloud-main",
			Git:   ui.Git{Changes: 3, HasUpstream: true},
			State: ui.Session{Open: true, Agent: "blocked"},
		}}}, nil
	})

	results := collect(Discover(context.Background(), Options{
		Peers:       []string{Addr("127.0.0.1", port)},
		NoBroadcast: true,
		Listen:      300 * time.Millisecond,
	}))

	if len(results) != 1 {
		t.Fatalf("results = %+v, want one", results)
	}
	r := results[0]
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	if r.Snapshot.Name != "macmini" || r.Snapshot.SSH != "macmini" || r.Snapshot.Hostname != "mini.local" {
		t.Fatalf("snapshot identity = %+v", r.Snapshot)
	}
	if len(r.Snapshot.Items) != 1 || r.Snapshot.Items[0].Git.Changes != 3 || r.Snapshot.Items[0].State.Agent != "blocked" {
		t.Fatalf("snapshot items = %+v", r.Snapshot.Items)
	}
}

func TestDiscoverSkipsItselfAndAnswersOnce(t *testing.T) {
	var reads atomic.Int32
	snapshot := func(context.Context) (Snapshot, error) { reads.Add(1); return Snapshot{}, nil }
	self := startServer(t, Announce{Name: "desktop", Hostname: "desktop-host", SSH: "desktop"}, snapshot)
	other := startServer(t, Announce{Name: "macbook", Hostname: "macbook-host", SSH: "macbook"}, snapshot)

	results := collect(Discover(context.Background(), Options{
		// asked twice, and every query is resent
		Peers:       []string{Addr("127.0.0.1", self), Addr("127.0.0.1", other), Addr("localhost", other)},
		NoBroadcast: true,
		Self:        "desktop-host",
		Listen:      700 * time.Millisecond,
	}))

	if len(results) != 1 || results[0].Snapshot.Name != "macbook" {
		t.Fatalf("results = %+v, want macbook only", results)
	}
	if n := reads.Load(); n != 1 {
		t.Fatalf("snapshot read %d times, want once", n)
	}
}

func TestDiscoverReportsUnreadableMachines(t *testing.T) {
	broken := startServer(t, Announce{Name: "broken", Hostname: "broken-host", SSH: "broken"}, func(context.Context) (Snapshot, error) {
		return Snapshot{}, errors.New("disk on fire")
	})
	evil := startServer(t, Announce{Name: "evil", Hostname: "evil-host", SSH: "-oProxyCommand=sh"}, func(context.Context) (Snapshot, error) {
		t.Error("the snapshot of a machine with a bad ssh destination was fetched")
		return Snapshot{}, nil
	})

	results := collect(Discover(context.Background(), Options{
		Peers:       []string{Addr("127.0.0.1", broken), Addr("127.0.0.1", evil)},
		NoBroadcast: true,
		Listen:      300 * time.Millisecond,
	}))

	if len(results) != 2 {
		t.Fatalf("results = %+v, want two", results)
	}
	for _, r := range results {
		if r.Err == nil {
			t.Fatalf("%s: no error", r.Announce.Name)
		}
	}
}

func TestDiscoverWithoutAnswersEndsAfterListening(t *testing.T) {
	start := time.Now()
	results := collect(Discover(context.Background(), Options{
		Peers:       []string{"127.0.0.1:9"},
		NoBroadcast: true,
		Listen:      200 * time.Millisecond,
	}))
	if len(results) != 0 {
		t.Fatalf("results = %+v", results)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("took %s", elapsed)
	}
}

func TestValidSSH(t *testing.T) {
	for _, ok := range []string{"macmini", "florian@192.168.48.11", "mac-book.local", "user@[fe80::1%en0]"} {
		if err := ValidSSH(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-oProxyCommand=x", "host name", "a;b", "$(x)", "host\n"} {
		if ValidSSH(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestAllowed(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "192.168.48.10", "10.1.2.3", "172.16.0.5", "169.254.1.1", "100.100.1.1", "::1", "fe80::1", "fd00::1"} {
		if !allowed(net.ParseIP(ip)) {
			t.Errorf("%s refused", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "2a02:168::1"} {
		if allowed(net.ParseIP(ip)) {
			t.Errorf("%s allowed", ip)
		}
	}
}

func TestBroadcastOf(t *testing.T) {
	_, network, _ := net.ParseCIDR("192.168.48.10/24")
	network.IP = net.ParseIP("192.168.48.10")
	if got := broadcastOf(network).String(); got != "192.168.48.255" {
		t.Fatalf("broadcast = %s", got)
	}
}

func TestListenPutsQueriesAndSnapshotsOnOnePort(t *testing.T) {
	udp, tcp, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	defer tcp.Close()
	if u, p := udp.LocalAddr().(*net.UDPAddr).Port, tcp.Addr().(*net.TCPAddr).Port; u != p {
		t.Fatalf("udp port %d, tcp port %d", u, p)
	}
}

func TestFetchRefusesRedirects(t *testing.T) {
	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Store(true) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/admin", http.StatusFound)
	}))
	defer redirect.Close()
	if _, err := fetch(context.Background(), strings.TrimPrefix(redirect.URL, "http://"), time.Second); err == nil {
		t.Fatal("a redirect was taken for a snapshot")
	}
	if followed.Load() {
		t.Fatal("the redirect was followed")
	}
}

func TestBroadcastsDoNotWaitForASlowPeer(t *testing.T) {
	// a peer that never resolves in time must not hold up the others
	port := startServer(t, Announce{Name: "macmini", Hostname: "mini", SSH: "macmini"}, func(context.Context) (Snapshot, error) { return Snapshot{}, nil })
	results := collect(Discover(context.Background(), Options{
		Peers:       []string{"unresolvable.invalid", Addr("127.0.0.1", port)},
		NoBroadcast: true,
		Listen:      300 * time.Millisecond,
	}))
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("results = %+v", results)
	}
}
