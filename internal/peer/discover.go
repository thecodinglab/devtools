package peer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Options say where Discover asks and how long it waits.
type Options struct {
	// Port is where queries are broadcast, DefaultPort when zero.
	Port int
	// Peers are machines asked directly as well, host or host:port, for
	// the ones a broadcast does not reach, across a VPN say.
	Peers []string
	// NoBroadcast asks the peers only.
	NoBroadcast bool
	// Self is the hostname of this machine, whose own devtools serve
	// answers too and is skipped.
	Self string
	// Listen is how long answers are waited for, Fetch how long fetching a
	// snapshot may take.
	Listen time.Duration
	Fetch  time.Duration
}

// Result is a machine that answered: its snapshot, or why it could not be
// read.
type Result struct {
	Announce Announce
	Addr     string // where it answered from
	Snapshot Snapshot
	Elapsed  time.Duration // from asking to having the snapshot
	Err      error
}

// resends are when a query goes out, again in case one was lost.
var resends = []time.Duration{0, 150 * time.Millisecond, 500 * time.Millisecond}

// Discover asks the network for devtools and sends a result for every
// machine that answers, as soon as its snapshot is in. The channel closes
// once the answers have been waited for and every snapshot fetched, or ctx
// is done. A machine that sleeps just never answers.
func Discover(ctx context.Context, opts Options) <-chan Result {
	out := make(chan Result)
	go func() {
		defer close(out)
		start := time.Now()
		send := func(r Result) {
			select {
			case out <- r:
			case <-ctx.Done():
			}
		}
		conn, err := net.ListenPacket("udp4", ":0")
		if err != nil {
			send(Result{Err: fmt.Errorf("listen for answers: %w", err)})
			return
		}
		listen, cancel := context.WithTimeout(ctx, opts.listen())
		defer cancel()
		go func() {
			<-listen.Done()
			conn.SetReadDeadline(time.Now())
		}()
		go ask(listen, conn, opts)

		var fetches sync.WaitGroup
		// fetching takes a few at a time, and a flood of announces stops
		// being listened to past what a desk of machines could send
		limit := make(chan struct{}, maxFetching)
		seen := map[string]bool{}
		buf := make([]byte, 4096)
		for len(seen) < maxMachines {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				break
			}
			from, ok := addr.(*net.UDPAddr)
			if !ok {
				continue
			}
			var announce Announce
			if json.Unmarshal(buf[:n], &announce) != nil || announce.Hostname == "" {
				continue
			}
			// a machine answers every broadcast address and every resend
			key := announce.Hostname + "\x00" + announce.Name
			if announce.Hostname == opts.Self || seen[key] {
				continue
			}
			seen[key] = true
			result := Result{Announce: announce, Addr: from.IP.String()}
			if err := announce.validate(); err != nil {
				result.Err = err
				send(result)
				continue
			}
			fetches.Add(1)
			go func() {
				defer fetches.Done()
				select {
				case limit <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-limit }()
				result.Snapshot, result.Err = fetch(ctx, Addr(result.Addr, announce.Port), opts.fetch())
				result.Elapsed = time.Since(start)
				send(result)
			}()
		}
		conn.Close()
		fetches.Wait()
	}()
	return out
}

const (
	// maxMachines is how many machines are listened to at most.
	maxMachines = 64
	// maxFetching is how many snapshots are fetched at once.
	maxFetching = 8
)

// ask sends the query to every broadcast address and peer, a few times.
// Broadcasts go out at once; a peer is asked once its name resolves, so a
// slow lookup holds up only its own machine.
func ask(ctx context.Context, conn net.PacketConn, opts Options) {
	port := opts.Port
	if port == 0 {
		port = DefaultPort
	}
	if !opts.NoBroadcast {
		go resend(ctx, conn, broadcasts(port))
	}
	for _, peer := range opts.Peers {
		host, peerPort := peer, port
		if h, p, err := net.SplitHostPort(peer); err == nil {
			n, err := strconv.Atoi(p)
			if err != nil {
				continue
			}
			host, peerPort = h, n
		}
		go func() {
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
			if err != nil || len(ips) == 0 {
				return
			}
			resend(ctx, conn, []*net.UDPAddr{{IP: ips[0], Port: peerPort}})
		}()
	}
}

func resend(ctx context.Context, conn net.PacketConn, dests []*net.UDPAddr) {
	message := encodeQuery()
	var waited time.Duration
	for _, at := range resends {
		select {
		case <-time.After(at - waited):
		case <-ctx.Done():
			return
		}
		waited = at
		for _, dest := range dests {
			// a network that is down or refuses broadcasts is not an error,
			// the machines on it just do not answer
			conn.WriteTo(message, dest)
		}
	}
}

// broadcasts are the limited broadcast address and the broadcast address
// of every IPv4 network this machine is on; macOS sends the limited one out
// of one interface only.
func broadcasts(port int) []*net.UDPAddr {
	dests := []*net.UDPAddr{{IP: net.IPv4bcast, Port: port}}
	ifaces, err := net.Interfaces()
	if err != nil {
		return dests
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagBroadcast == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			network, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			if b := broadcastOf(network); b != nil {
				dests = append(dests, &net.UDPAddr{IP: b, Port: port})
			}
		}
	}
	return dests
}

func broadcastOf(network *net.IPNet) net.IP {
	ip := network.IP.To4()
	mask := network.Mask
	if len(mask) == net.IPv6len {
		mask = mask[12:]
	}
	if ip == nil || len(mask) != net.IPv4len {
		return nil
	}
	b := make(net.IP, net.IPv4len)
	for i := range b {
		b[i] = ip[i] | ^mask[i]
	}
	return b
}

// maxSnapshot bounds what a machine may send.
const maxSnapshot = 4 << 20

// client fetches snapshots without following redirects: a machine answers
// for itself, it does not point devtools at another address.
var client = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func fetch(ctx context.Context, addr string, timeout time.Duration) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+snapshotPath, nil)
	if err != nil {
		return Snapshot{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Snapshot{}, fmt.Errorf("fetch snapshot: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSnapshot))
	if err != nil {
		return Snapshot{}, fmt.Errorf("fetch snapshot: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Snapshot{}, fmt.Errorf("fetch snapshot: %s: %s", resp.Status, trim(body))
	}
	var snapshot Snapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("unexpected snapshot: %w", err)
	}
	return snapshot, nil
}

func trim(body []byte) string {
	const most = 200
	if len(body) > most {
		body = body[:most]
	}
	return string(body)
}

func (o Options) listen() time.Duration {
	if o.Listen > 0 {
		return o.Listen
	}
	return time.Second
}

func (o Options) fetch() time.Duration {
	if o.Fetch > 0 {
		return o.Fetch
	}
	return 4 * time.Second
}
