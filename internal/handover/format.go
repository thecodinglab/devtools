// Package handover is the transfer format of the live upgrade: what a
// devtools server image hands the image that replaces it in the same
// process (execve, same pid). The payload is an anonymous file whose
// descriptor survives the exec: a fixed prefix, a JSON header and binary
// sections (the screens and the unwritten input of the panes). The
// descriptors themselves are named in an environment variable (env.go), so
// they are known even when the payload cannot be read.
//
// The schema is frozen per version: the header has its own types, copies of
// the shapes they carry, so that a change elsewhere cannot change the
// transfer format. A reader refuses unknown fields and unknown versions;
// a change of the schema is a new version, and a build keeps the reader and
// the writer of every version it lists in Versions.
//
// The package imports the standard library only.
package handover

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
)

// Magic is the first 8 bytes of a payload.
const Magic = "DTHANDOV"

// Version is the schema version this build writes; Versions are those it
// reads and writes.
const Version = 1

// Versions are the schema versions this build reads and writes.
var Versions = []int{Version}

// Limits of a payload, checked by the reader against the size of the file
// before anything is allocated, and by the writer the same way.
const (
	MaxHeader  = 16 << 20
	MaxPanes   = 1024
	MaxScreen  = 64 << 20
	MaxPending = 2 << 20
	MaxTotal   = 512 << 20
	prefixLen  = 20
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// Header is the JSON header of schema version 1.
type Header struct {
	Format   int                        `json:"format"`
	From     Build                      `json:"from"`
	Created  int64                      `json:"created"` // unix ms
	Boot     string                     `json:"boot"`    // welcome.boot, kept by the new image
	Rev      uint64                     `json:"rev"`     // the revision of the last state delta: the new image goes on from it
	Socket   string                     `json:"socket"`  // the path the listener is bound to
	Attempt  string                     `json:"attempt"` // the attempt id, equal to the environment's
	State    State                      `json:"state"`
	Panes    []Pane                     `json:"panes"`
	HelloEnv map[string]string          `json:"helloEnv"`
	Colors   map[string]Colors          `json:"colors"` // per session
	Ext      map[string]json.RawMessage `json:"ext"`    // per extension
	Upgraded *Upgraded                  `json:"upgraded,omitempty"`
}

// Build names a build of devtools.
type Build struct {
	Version string `json:"version"`
	Path    string `json:"path"` // the store path of the binary
	Pid     int    `json:"pid"`
	GOOS    string `json:"goos"`
	GOARCH  string `json:"goarch"`
}

// Upgraded is what the server says about the last live upgrade.
type Upgraded struct {
	ID    string `json:"id"`
	From  string `json:"from"`
	To    string `json:"to"`
	At    int64  `json:"at"`
	Kept  int    `json:"kept"`
	Error string `json:"error,omitempty"`
}

// Colors are the colours of a session's panes (vt.Colors).
type Colors struct {
	Fg   string `json:"fg"`
	Bg   string `json:"bg"`
	Dark bool   `json:"dark"`
}

// Pane kinds of Pane.Kind.
const (
	PaneRunning = "running" // the program runs; Master is its PTY
	PaneReaped  = "reaped"  // the program ended and was reaped, its exit not yet parsed; Master is its PTY
	PaneExited  = "exited"  // the pane is exited; no Master
)

// Pane is a pane object of the old image.
type Pane struct {
	ID      string  `json:"id"`
	Kind    string  `json:"kind"`
	Pid     int     `json:"pid"`
	Shell   int     `json:"shell"`
	Fg      int     `json:"fg"`
	Master  int     `json:"master"` // descriptor number in the new image, -1 for none
	TTY     string  `json:"tty"`
	Cols    int     `json:"cols"`
	Rows    int     `json:"rows"`
	Title   string  `json:"title"`
	Cwd     string  `json:"cwd"`
	Exit    *Exit   `json:"exit,omitempty"`
	First   int64   `json:"first"`
	Base    int64   `json:"base"`
	Screen  Section `json:"screen"`
	Pending Section `json:"pending"`
}

// Exit is how a pane's program ended (pane.Exit).
type Exit struct {
	Code     int    `json:"code"`
	Signal   string `json:"signal"`
	ByServer bool   `json:"byServer"`
	Text     string `json:"text"`
}

// Section is a range of bytes after the header: Off is counted from the
// first byte after the header's padding.
type Section struct {
	Format string `json:"format"`
	Off    uint64 `json:"off"`
	Len    uint64 `json:"len"`
	CRC    uint32 `json:"crc"`
}

// Raw is the format of a section of plain bytes (Pending).
const Raw = "raw"

// Writer builds a payload: sections are added first, the header last.
type Writer struct {
	data bytes.Buffer
}

// Add appends a section and returns its reference; an empty b is the empty
// section.
func (w *Writer) Add(format string, b []byte) (Section, error) {
	if len(b) == 0 {
		return Section{}, nil
	}
	if uint64(w.data.Len())+uint64(len(b)) > MaxTotal {
		return Section{}, fmt.Errorf("handover: the sections exceed %d bytes", MaxTotal)
	}
	for w.data.Len()%8 != 0 {
		w.data.WriteByte(0)
	}
	s := Section{Format: format, Off: uint64(w.data.Len()), Len: uint64(len(b)), CRC: crc32.Checksum(b, castagnoli)}
	w.data.Write(b)
	return s, nil
}

// Write writes the payload with header h to f, from the start of f, and
// leaves f at its start. The payload is then opened as the reader opens
// it: one the reader would refuse is an error here, before anything is
// exec'd, not in the new image, where only the programs' end is left.
func (w *Writer) Write(f *os.File, h Header) error {
	if err := w.write(f, h); err != nil {
		return err
	}
	if _, err := Open(f); err != nil {
		return fmt.Errorf("the payload would not be read: %w", err)
	}
	return nil
}

func (w *Writer) write(f *os.File, h Header) error {
	h.Format = Version
	if err := checkHeader(&h); err != nil {
		return err
	}
	hb, err := json.Marshal(h)
	if err != nil {
		return err
	}
	if len(hb) > MaxHeader {
		return fmt.Errorf("handover: header of %d bytes, more than %d", len(hb), MaxHeader)
	}
	var out bytes.Buffer
	out.WriteString(Magic)
	binary.Write(&out, binary.LittleEndian, uint16(Version))
	binary.Write(&out, binary.LittleEndian, uint16(0))
	binary.Write(&out, binary.LittleEndian, uint32(len(hb)))
	binary.Write(&out, binary.LittleEndian, crc32.Checksum(hb, castagnoli))
	out.Write(hb)
	for out.Len()%8 != 0 {
		out.WriteByte(0)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Write(out.Bytes()); err != nil {
		return err
	}
	if _, err := f.Write(w.data.Bytes()); err != nil {
		return err
	}
	_, err = f.Seek(0, io.SeekStart)
	return err
}

// Reader is an opened payload.
type Reader struct {
	f      *os.File
	size   int64
	base   int64 // the offset of the first section byte
	header Header
}

// ErrNotPayload is the error of Open for a file that is not a payload.
var ErrNotPayload = errors.New("handover: not a handover payload")

// Open reads and checks the header of the payload in f: magic, version,
// length, checksum, unknown fields, limits, and every section's bounds
// against the size of the file.
func Open(f *os.File) (*Reader, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := fi.Size()
	if size < prefixLen {
		return nil, fmt.Errorf("%w: %d bytes", ErrNotPayload, size)
	}
	var prefix [prefixLen]byte
	if _, err := f.ReadAt(prefix[:], 0); err != nil {
		return nil, err
	}
	if string(prefix[:8]) != Magic {
		return nil, ErrNotPayload
	}
	v := int(binary.LittleEndian.Uint16(prefix[8:10]))
	if v != Version {
		return nil, fmt.Errorf("handover: schema version %d; this build reads %v", v, Versions)
	}
	hl := int64(binary.LittleEndian.Uint32(prefix[12:16]))
	sum := binary.LittleEndian.Uint32(prefix[16:20])
	if hl == 0 || hl > MaxHeader || prefixLen+hl > size {
		return nil, fmt.Errorf("handover: header of %d bytes in a file of %d", hl, size)
	}
	hb := make([]byte, hl)
	if _, err := f.ReadAt(hb, prefixLen); err != nil {
		return nil, err
	}
	if crc32.Checksum(hb, castagnoli) != sum {
		return nil, errors.New("handover: the header's checksum does not match")
	}
	var h Header
	dec := json.NewDecoder(bytes.NewReader(hb))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&h); err != nil {
		return nil, fmt.Errorf("handover: header: %w", err)
	}
	if h.Format != Version {
		return nil, fmt.Errorf("handover: header format %d", h.Format)
	}
	if err := checkHeader(&h); err != nil {
		return nil, err
	}
	base := (prefixLen + hl + 7) &^ 7
	r := &Reader{f: f, size: size, base: base, header: h}
	if err := r.checkSections(); err != nil {
		return nil, err
	}
	return r, nil
}

// Header is the payload's header.
func (r *Reader) Header() Header { return r.header }

// Section reads a section and checks its checksum; the empty section is
// nil.
func (r *Reader) Section(s Section) ([]byte, error) {
	if s.Len == 0 {
		return nil, nil
	}
	if err := r.bounds(s); err != nil {
		return nil, err
	}
	b := make([]byte, s.Len)
	if _, err := r.f.ReadAt(b, r.base+int64(s.Off)); err != nil {
		return nil, err
	}
	if crc32.Checksum(b, castagnoli) != s.CRC {
		return nil, errors.New("handover: a section's checksum does not match")
	}
	return b, nil
}

func (r *Reader) bounds(s Section) error {
	end := s.Off + s.Len
	if end < s.Off || r.base+int64(end) > r.size || int64(end) < 0 {
		return fmt.Errorf("handover: a section at %d+%d beyond the end of the file (%d)", s.Off, s.Len, r.size)
	}
	return nil
}

// checkSections checks every section against the file and the others:
// within the file, not overlapping, each within its limit, in total within
// MaxTotal.
func (r *Reader) checkSections() error {
	type span struct{ off, end uint64 }
	var spans []span
	var total uint64
	for _, p := range r.header.Panes {
		for _, s := range []struct {
			sec   Section
			limit uint64
		}{{p.Screen, MaxScreen}, {p.Pending, MaxPending}} {
			if s.sec.Len == 0 {
				continue
			}
			if s.sec.Len > s.limit {
				return fmt.Errorf("handover: a section of %d bytes, more than %d", s.sec.Len, s.limit)
			}
			if err := r.bounds(s.sec); err != nil {
				return err
			}
			total += s.sec.Len
			for _, o := range spans {
				if s.sec.Off < o.end && o.off < s.sec.Off+s.sec.Len {
					return errors.New("handover: overlapping sections")
				}
			}
			spans = append(spans, span{s.sec.Off, s.sec.Off + s.sec.Len})
		}
	}
	if total > MaxTotal {
		return fmt.Errorf("handover: %d bytes of sections, more than %d", total, MaxTotal)
	}
	return nil
}

// checkHeader checks the limits of a header that do not need the file.
func checkHeader(h *Header) error {
	if len(h.Panes) > MaxPanes {
		return fmt.Errorf("handover: %d panes, more than %d", len(h.Panes), MaxPanes)
	}
	seen := map[string]bool{}
	for _, p := range h.Panes {
		if p.ID == "" || seen[p.ID] {
			return fmt.Errorf("handover: pane id %q missing or twice", p.ID)
		}
		seen[p.ID] = true
		switch p.Kind {
		case PaneRunning, PaneReaped:
			if p.Pid <= 0 || p.Master < 0 {
				return fmt.Errorf("handover: pane %s is %s without a program or a terminal", p.ID, p.Kind)
			}
		case PaneExited:
			if p.Master != -1 {
				return fmt.Errorf("handover: exited pane %s with a terminal", p.ID)
			}
		default:
			return fmt.Errorf("handover: pane %s of kind %q", p.ID, p.Kind)
		}
		if p.Cols < 1 || p.Rows < 1 || p.Cols > 1<<16-1 || p.Rows > 1<<16-1 {
			return fmt.Errorf("handover: pane %s of %dx%d", p.ID, p.Cols, p.Rows)
		}
		if p.First < 0 || p.Base < p.First {
			return fmt.Errorf("handover: pane %s with lines %d..%d", p.ID, p.First, p.Base)
		}
		if p.Pending.Len > 0 && p.Pending.Format != Raw {
			return fmt.Errorf("handover: pane %s with pending input of format %q", p.ID, p.Pending.Format)
		}
	}
	return nil
}

func crcOf(b []byte) uint32 { return crc32.Checksum(b, castagnoli) }
