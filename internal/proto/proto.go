// Package proto is the wire protocol between a devtools server and its
// clients (section 4 of the plan): the framing, the envelope, one struct per
// message type, the shared state with its deltas, the key names and the
// paths of the socket and the state directory.
//
// The package imports the standard library only (import rule 6).
package proto

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// Errors of ReadMsg and WriteMsg. A receiver that gets either closes the
// connection without reading further (4.1).
var (
	// ErrTooLarge: a length prefix above MaxMessage, or a message to write
	// that is longer than MaxMessage.
	ErrTooLarge = errors.New("proto: message above the 4 MiB limit")
	// ErrMalformed: a length prefix of 0, a message cut short, or a body that
	// is not one JSON object with a string "t" and, if present, a positive
	// integer "id".
	ErrMalformed = errors.New("proto: malformed message")
)

// Msg is one decoded envelope. Body is the whole JSON object.
type Msg struct {
	T    string
	ID   uint64 // 0: no id
	Body json.RawMessage
}

// Typed is implemented by every message struct: Type returns the wire value
// of "t".
type Typed interface{ Type() string }

// Reply is an answer to a request: "ok", "err", or one of the two named
// success replies ("view.ok", "state.snapshot") when it carries an id.
type Reply struct {
	OK         bool
	Code, Text string // only when OK is false
	Msg        Msg    // the whole reply; decode result fields from it
}

// Reply types of 4.1.
const (
	TypeOK  = "ok"
	TypeErr = "err"
)

// Error codes of 4.1 (err replies) and of refuse (4.2.1).
const (
	CodeUnsupported = "unsupported"
	CodeInvalid     = "invalid"
	CodeNoSession   = "no-session"
	CodeNoDir       = "no-dir"
	CodeNoTab       = "no-tab"
	CodeNoPane      = "no-pane"
	CodeLimit       = "limit"
	CodeNoClient    = "no-client"
	CodeInternal    = "internal"
	CodeLost        = "lost" // never on the wire: the fleet answers locally with it

	CodeGeneration = "generation" // refuse only
	CodeNested     = "nested"     // refuse only
)

// header is the part of every message ReadMsg looks at.
type header struct {
	T  *string         `json:"t"`
	ID json.RawMessage `json:"id"`
}

// ReadMsg reads one framed message: a big-endian u32 length and one JSON
// object of that length. It returns io.EOF when r ends cleanly before a
// message, ErrTooLarge for a length above MaxMessage (before anything of the
// body is allocated or read), and ErrMalformed for a length of 0, a message
// cut short or a body that is not an envelope. An unknown "t" is not an
// error: the message decodes as an envelope.
func ReadMsg(r io.Reader) (Msg, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		if err == io.EOF {
			return Msg{}, io.EOF
		}
		if err == io.ErrUnexpectedEOF {
			return Msg{}, fmt.Errorf("%w: length prefix cut short", ErrMalformed)
		}
		return Msg{}, err
	}
	n := binary.BigEndian.Uint32(prefix[:])
	if n == 0 {
		return Msg{}, fmt.Errorf("%w: length 0", ErrMalformed)
	}
	if n > MaxMessage {
		return Msg{}, fmt.Errorf("%w: length %d", ErrTooLarge, n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return Msg{}, fmt.Errorf("%w: message of %d bytes cut short", ErrMalformed, n)
		}
		return Msg{}, err
	}
	return parse(body)
}

// parse checks that body is one JSON object with a string "t" and an
// optional positive integer "id"; an id that is present must be one.
func parse(body []byte) (Msg, error) {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return Msg{}, fmt.Errorf("%w: not a JSON object", ErrMalformed)
	}
	var h header
	if err := json.Unmarshal(body, &h); err != nil {
		return Msg{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if h.T == nil || *h.T == "" {
		return Msg{}, fmt.Errorf("%w: no message type", ErrMalformed)
	}
	m := Msg{T: *h.T, Body: json.RawMessage(body)}
	// Only an absent id means "no id". A present one, null included (the
	// decoder hands null to RawMessage as the bytes "null"), must be a
	// positive integer.
	if len(h.ID) > 0 {
		id, err := strconv.ParseUint(string(h.ID), 10, 64)
		if err != nil || id == 0 {
			return Msg{}, fmt.Errorf("%w: id %s is not a positive integer", ErrMalformed, h.ID)
		}
		m.ID = id
	}
	return m, nil
}

// WriteMsg writes m framed, in one Write call. A Msg without a Body is
// written as {"t":T} or {"t":T,"id":ID}. A body above MaxMessage is refused
// with ErrTooLarge and nothing is written.
func WriteMsg(w io.Writer, m Msg) error {
	body := []byte(m.Body)
	if len(body) == 0 {
		if m.T == "" {
			return fmt.Errorf("%w: no message type", ErrMalformed)
		}
		body = envelope(m.T, m.ID, nil)
	}
	if len(body) > MaxMessage {
		return fmt.Errorf("%w: %d bytes", ErrTooLarge, len(body))
	}
	buf := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(buf, uint32(len(body)))
	copy(buf[4:], body)
	_, err := w.Write(buf)
	return err
}

// Encode builds the message of v without an id.
func Encode(v Typed) Msg { return build(v.Type(), 0, v) }

// Request builds the message of v with the id; used for requests and for
// the named replies (view.ok, state.snapshot) that answer one.
func Request(id uint64, v Typed) Msg { return build(v.Type(), id, v) }

// OKReply builds {"t":"ok","id":id, ...fields of result}. result is nil or a
// value that encodes as a JSON object.
func OKReply(id uint64, result any) Msg { return build(TypeOK, id, result) }

// ErrReply builds {"t":"err","id":id,"code":code,"text":text}.
func ErrReply(id uint64, code, text string) Msg {
	return build(TypeErr, id, errBody{Code: code, Text: text})
}

type errBody struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

// Decode decodes the body into v. It is tolerant: unknown fields, "t" and
// "id" are ignored. Enumerated fields with an "unknown" value (pane state,
// agent kind and status) read any other value as "unknown".
func (m Msg) Decode(v any) error {
	if err := json.Unmarshal(m.Body, v); err != nil {
		return fmt.Errorf("proto: decode %s: %w", m.T, err)
	}
	if n, ok := v.(normalizer); ok {
		n.normalize()
	}
	return nil
}

// normalizer is implemented by message types with enumerated fields that
// have an "unknown" value.
type normalizer interface{ normalize() }

// AsReply reports whether m answers a request: "ok", "err", or "view.ok" and
// "state.snapshot" with an id.
func AsReply(m Msg) (Reply, bool) {
	switch m.T {
	case TypeOK:
		return Reply{OK: true, Msg: m}, true
	case TypeErr:
		var b errBody
		if err := json.Unmarshal(m.Body, &b); err != nil {
			return Reply{Code: CodeInvalid, Text: "malformed err reply", Msg: m}, true
		}
		return Reply{Code: b.Code, Text: b.Text, Msg: m}, true
	case TypeViewOK, TypeStateSnapshot:
		if m.ID != 0 {
			return Reply{OK: true, Msg: m}, true
		}
	}
	return Reply{}, false
}

// build encodes v as an object and puts "t" and, when id is not 0, "id" in
// front of its fields. It panics when v does not encode as a JSON object, a
// programming error of the caller. The message structs have no "t" or "id"
// field of their own.
func build(t string, id uint64, v any) Msg {
	var fields []byte
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			panic(fmt.Sprintf("proto: encode %s: %v", t, err))
		}
		if string(b) != "null" {
			if len(b) < 2 || b[0] != '{' {
				panic(fmt.Sprintf("proto: %s does not encode as a JSON object", t))
			}
			fields = b[1 : len(b)-1]
		}
	}
	return Msg{T: t, ID: id, Body: envelope(t, id, fields)}
}

func envelope(t string, id uint64, fields []byte) []byte {
	tq, _ := json.Marshal(t)
	b := make([]byte, 0, len(fields)+len(tq)+32)
	b = append(b, `{"t":`...)
	b = append(b, tq...)
	if id != 0 {
		b = append(b, `,"id":`...)
		b = strconv.AppendUint(b, id, 10)
	}
	if len(bytes.TrimSpace(fields)) > 0 {
		b = append(b, ',')
		b = append(b, fields...)
	}
	return append(b, '}')
}
