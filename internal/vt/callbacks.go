//go:build cgo

package vt

// The functions the library calls back into, through the trampolines of
// engine.go. They live in their own file because a file with //export may
// hold only declarations in its preamble.
//
// Rules (3.2.1, 5.3.2): a callback runs synchronously inside Write or
// Resize, on the caller's goroutine. It copies what it is handed, because
// every pointer it gets is borrowed for the duration of the call, records it
// or hands it to the callback of Options, and returns. None of these
// functions calls a library function that takes the terminal.

/*
#include <stdint.h>
#include <stddef.h>
*/
import "C"

import (
	"net/url"
	"os"
	"runtime/cgo"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"devtools/internal/proto"
)

// maxTitle is the longest title handed to OnTitle, in runes (3.2.2).
const maxTitle = 256

// termOf returns the terminal of a userdata handle.
func termOf(h C.uintptr_t) *term { return cgo.Handle(h).Value().(*term) }

// borrowed views memory the library lends for the duration of a callback.
// The caller copies what it keeps.
func borrowed(p *C.uint8_t, n C.size_t) []byte {
	if p == nil || n == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(p)), int(n))
}

//export dtWritePty
func dtWritePty(h C.uintptr_t, data *C.uint8_t, n C.size_t) {
	t := termOf(h)
	t.replies = append(t.replies, borrowed(data, n)...) // copied
}

//export dtXtversion
func dtXtversion(h C.uintptr_t, p **C.uint8_t, n *C.size_t) {
	t := termOf(h)
	if t.xtv == nil {
		return // the library's default answer
	}
	// C memory owned by the terminal, valid until Close.
	*p = (*C.uint8_t)(unsafe.Pointer(t.xtv))
	*n = C.size_t(t.xtvLen)
}

//export dtTitle
func dtTitle(h C.uintptr_t, p *C.uint8_t, n C.size_t) {
	t := termOf(h)
	if t.o.OnTitle != nil {
		t.o.OnTitle(sanitizeTitle(string(borrowed(p, n))))
	}
}

// sanitizeTitle makes a title safe to show: invalid UTF-8 becomes U+FFFD,
// control and format characters (C0, C1, DEL, and the class Cf with the
// bidirectional overrides and the zero-width characters) are removed, and it is cut to
// maxTitle runes.
func sanitizeTitle(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == maxTitle {
			break
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

//export dtPwd
func dtPwd(h C.uintptr_t, p *C.uint8_t, n C.size_t) {
	t := termOf(h)
	if t.o.OnCwd == nil {
		return
	}
	if dir, ok := cwdOf(string(borrowed(p, n))); ok {
		t.o.OnCwd(dir)
	}
}

// cwdOf reads OSC 7, which the library hands over as the raw URI (E10
// item 8): only a file URL of this host (an empty host, "localhost" or the
// name of this machine) with an absolute path is a working directory. An
// empty report and anything else is ignored.
func cwdOf(raw string) (string, bool) {
	if raw == "" || !utf8.ValidString(raw) {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	if !isThisHost(u.Host) {
		return "", false
	}
	p := u.Path
	if !strings.HasPrefix(p, "/") || strings.ContainsFunc(p, unicode.IsControl) {
		return "", false
	}
	return p, true
}

var hostname = sync.OnceValue(func() string {
	h, _ := os.Hostname()
	return h
})

// isThisHost reports whether the host of a file URL names this machine.
func isThisHost(host string) bool {
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	me := hostname()
	if me == "" {
		return false
	}
	short, _, _ := strings.Cut(me, ".")
	return strings.EqualFold(host, me) || strings.EqualFold(host, short)
}

//export dtBell
func dtBell(h C.uintptr_t) {
	t := termOf(h)
	if t.o.OnBell != nil {
		t.o.OnBell()
	}
}

// dtClipboard receives an OSC 52 set, already base64-decoded by the
// library. It is kept when it is not empty and at most proto.MaxClip
// (1 MiB) long; it returns 1 when it was kept.
//
//export dtClipboard
func dtClipboard(h C.uintptr_t, p *C.uint8_t, n C.size_t) C.int {
	t := termOf(h)
	if n == 0 || int(n) > proto.MaxClip || t.o.OnClipboard == nil {
		return 0
	}
	t.o.OnClipboard(append([]byte(nil), borrowed(p, n)...))
	return 1
}

// agentStates are the library's program status states in the order of
// GhosttyProgramStatusState; agentKinds the kinds of GhosttyProgramStatusKind.
var (
	agentStates = [...]string{"idle", "working", "done", "blocked", "error", "clear"}
	agentKinds  = [...]string{"", "permission", "question", "auth"}
)

// dtStatus receives one OSC 7501 report the library parsed. Only the root
// record (an empty id) speaks for the pane; a record with an id, its clear
// included, belongs to a subagent or a background task and is dropped (E4,
// contract change 2).
//
//export dtStatus
func dtStatus(h C.uintptr_t, state, kind C.int, idlen C.size_t) {
	t := termOf(h)
	if idlen != 0 || t.o.OnAgent == nil || state < 0 || int(state) >= len(agentStates) {
		return
	}
	r := AgentReport{State: agentStates[state]}
	if kind > 0 && int(kind) < len(agentKinds) {
		r.Kind = agentKinds[kind]
	}
	t.o.OnAgent(r)
}

// dtColorScheme answers CSI ? 996 n from Colors.Dark: 1 dark, 0 light, -1
// no answer while no colours were set.
//
//export dtColorScheme
func dtColorScheme(h C.uintptr_t) C.int {
	t := termOf(h)
	switch {
	case !t.hasColors:
		return -1
	case t.colors.Dark:
		return 1
	}
	return 0
}
