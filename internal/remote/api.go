// Package remote is one handshaken connection to a devtools server: to the
// local socket, or to another machine's server over
// "ssh -T <machine> devtools bridge" (4.4 of the plan); the classification
// of every failure (3.5.2); and the body of "devtools bridge".
//
// This file is the contract (P2-00) and is read-only for the package that
// implements it (P2-02): NewDialer, SSHArgs, Classify and Bridge are written
// there.
package remote

import (
	"context"

	"devtools/internal/proto"
)

// Reason says why a link could not be made or ended (3.5.2).
type Reason string

const (
	Auth         Reason = "auth"
	HostKey      Reason = "host-key"
	Unreachable  Reason = "unreachable"
	NoDevtools   Reason = "no-devtools"
	NoServer     Reason = "no-server"
	Incompatible Reason = "incompatible"
	Protocol     Reason = "protocol"
)

// Failure is a classified failure of a dial or of a live link.
type Failure struct {
	Reason Reason
	Text   string // one sanitised line for the user
}

// Error returns the line for the user, or the reason when there is none.
func (f *Failure) Error() string {
	if f.Text != "" {
		return f.Text
	}
	return string(f.Reason)
}

// Link is one handshaken connection to a server.
type Link interface {
	Send(m proto.Msg) error // bounded queue; an error means the link is over
	Recv() <-chan proto.Msg // closed when the link ends
	Welcome() proto.Welcome
	Err() *Failure // why it ended, once Recv is closed; nil after Close
	Close()
}

// Dialer opens links. machine "" is the local socket.
type Dialer interface {
	Dial(ctx context.Context, machine string) (Link, *Failure)
}

// DialOptions configure NewDialer.
type DialOptions struct {
	SSH    string // the ssh program, "ssh"
	Socket string // the local socket, proto.SocketPath()
	Hello  proto.Hello
}
