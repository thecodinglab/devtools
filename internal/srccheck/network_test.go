package srccheck

import (
	"go/ast"
	"testing"
)

// unixNetworks are the networks a listener may use: the server's socket
// (import rule 11, MC5, SEC8).
var unixNetworks = map[string]bool{"unix": true, "unixgram": true, "unixpacket": true}

// ipNetworks are the networks that would put a listener on the network.
var ipNetworks = map[string]bool{
	"tcp": true, "tcp4": true, "tcp6": true, "udp": true, "udp4": true, "udp6": true,
	"ip": true, "ip4": true, "ip6": true,
}

// noNetworkListener finds a non-test file that imports net/http, calls
// net.Listen or net.ListenPacket with a network other than a unix one (or
// one that is not a constant), calls a listener of the net package that
// is not a unix one, or calls a Listen method with an IP network.
func noNetworkListener(files []srcFile) []string {
	var vs []string
	for _, f := range files {
		if f.importsPath("net/http") {
			vs = append(vs, f.Rel+": imports net/http")
		}
		names := f.imports()
		ast.Inspect(f.File, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if path, name, ok := f.pkgSelector(names, call.Fun); ok && path == "net" {
				switch name {
				case "Listen", "ListenPacket":
					network, isLit := "", false
					if len(call.Args) > 0 {
						network, isLit = stringLit(call.Args[0])
					}
					if !isLit || !unixNetworks[network] {
						vs = append(vs, f.pos(call.Pos())+": net."+name+" on a network other than unix")
					}
				case "ListenTCP", "ListenUDP", "ListenIP", "ListenMulticastUDP":
					vs = append(vs, f.pos(call.Pos())+": net."+name)
				}
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Listen" || sel.Sel.Name == "ListenPacket") {
				for _, a := range call.Args {
					if s, ok := stringLit(a); ok && ipNetworks[s] {
						vs = append(vs, f.pos(call.Pos())+": "+sel.Sel.Name+" on "+s)
					}
				}
			}
			return true
		})
	}
	return sorted(vs)
}

// TestNoNetworkListenerInSource: nothing listens on the network; the one
// listener is the server's unix socket (MC5, SEC8, import rule 11). It is
// what keeps the LAN discovery of internal/peer from coming back.
func TestNoNetworkListenerInSource(t *testing.T) {
	holds(t, noNetworkListener, map[string]string{
		"internal/server/boot/boot.go": "package boot\nimport \"net\"\nfunc listen(p string) (net.Listener, error) { return net.Listen(\"unix\", p) }\n",
		"internal/peer/server.go":      "package peer\nimport \"net\"\nfunc Listen(addr string) (net.PacketConn, error) { return net.ListenPacket(\"udp\", addr) }\n",
	}, "internal/peer/server.go")
}
