//go:build cgo

package vt

/*
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
#include <ghostty/vt.h>

static GhosttyResult dt_snapshot_encode(GhosttyTerminal t, uint8_t **ptr, size_t *len) {
	*ptr = NULL;
	*len = 0;
	return ghostty_snapshot_encode_alloc(t, NULL, ptr, len);
}

static void dt_snapshot_free(uint8_t *ptr, size_t len) {
	if (ptr != NULL) ghostty_free(NULL, ptr, len);
}

// dt_snapshot_decode restores one terminal from a complete snapshot in C
// memory (the decoder borrows the bytes until it is freed). The options are
// those of the live upgrade: the continuation is kept and stays tracked with
// the same limit, so the next upgrade can encode the terminal mid-sequence;
// history is compressed while it is restored; images are dropped, because
// devtools draws none.
static GhosttyResult dt_snapshot_decode(const uint8_t *ptr, size_t len, size_t cont, GhosttyTerminal *out) {
	*out = NULL;
	GhosttySnapshotDecoder d = NULL;
	GhosttyResult r = ghostty_snapshot_decoder_new_buf(NULL, &d, ptr, len);
	if (r != GHOSTTY_SUCCESS) return r;
	bool yes = true;
	uint64_t noImages = 0;
	if ((r = ghostty_snapshot_decoder_set(d, GHOSTTY_SNAPSHOT_DECODER_OPT_MAX_CONTINUATION_BYTES, &cont)) != GHOSTTY_SUCCESS) goto done;
	if ((r = ghostty_snapshot_decoder_set(d, GHOSTTY_SNAPSHOT_DECODER_OPT_RETAIN_CONTINUATION, &yes)) != GHOSTTY_SUCCESS) goto done;
	if ((r = ghostty_snapshot_decoder_set(d, GHOSTTY_SNAPSHOT_DECODER_OPT_COMPRESS_HISTORY, &yes)) != GHOSTTY_SUCCESS) goto done;
	if ((r = ghostty_snapshot_decoder_set(d, GHOSTTY_SNAPSHOT_DECODER_OPT_KITTY_IMAGE_STORAGE_LIMIT, &noImages)) != GHOSTTY_SUCCESS) goto done;
	r = ghostty_snapshot_decoder_decode(d, out);
done:
	ghostty_snapshot_decoder_free(d);
	return r;
}
*/
import "C"

import (
	"encoding/binary"
	"errors"
	"fmt"
	"runtime/cgo"
	"strconv"
	"sync"
	"unsafe"
)

// The terminal snapshot of the live upgrade (internal/handover): the old
// server image encodes every pane's terminal with the library's own binary
// snapshot, the new image restores it. The format is the library's
// ("ghostty-snapshot/<v>"), versioned apart from the library; a reader of
// version v reads exactly v (snapshot/envelope.zig).

// SnapshotPrefix is the prefix of the screen format names of the library's
// snapshot: "ghostty-snapshot/1" for snapshot version 1.
const SnapshotPrefix = "ghostty-snapshot/"

// maxSnapshot bounds an encoded snapshot: a full 10,000-line history at
// 200 columns is about 12 MiB (E4).
const maxSnapshot = 64 << 20

var snapVersion struct {
	once sync.Once
	v    int
}

// SnapshotVersion is the version of the snapshot format this engine writes
// and reads: bytes 8 and 9 of the envelope ("GHOSTSNP", u16 LE) of a
// snapshot of a 1x1 terminal. 0 when the engine cannot encode.
func SnapshotVersion() int {
	snapVersion.once.Do(func() {
		t := New(Options{Cols: 1, Rows: 1})
		defer t.Close()
		b, err := Encode(t)
		if err != nil || len(b) < 10 || string(b[:8]) != "GHOSTSNP" {
			return
		}
		snapVersion.v = int(binary.LittleEndian.Uint16(b[8:10]))
	})
	return snapVersion.v
}

// ScreenFormats are the screen formats this engine reads, best first.
func ScreenFormats() []string {
	if v := SnapshotVersion(); v > 0 {
		return []string{SnapshotPrefix + strconv.Itoa(v)}
	}
	return nil
}

// Encode returns the snapshot of the terminal: its screens with their
// history, modes, cursor, title, directory, colours and the unfinished
// sequence of its parser. It fails when the parser is inside a sequence
// longer than continuationMax.
func Encode(t Term) ([]byte, error) {
	tt, ok := t.(*term)
	if !ok || tt.h == nil {
		return nil, errors.New("vt: encode: not a terminal of the engine")
	}
	var ptr *C.uint8_t
	var n C.size_t
	if r := C.dt_snapshot_encode(tt.h, &ptr, &n); r != C.GHOSTTY_SUCCESS {
		return nil, fmt.Errorf("vt: encode the snapshot: result %d", int(r))
	}
	defer C.dt_snapshot_free(ptr, n)
	if n > maxSnapshot {
		return nil, fmt.Errorf("vt: the snapshot is %d bytes, more than %d", int(n), maxSnapshot)
	}
	return C.GoBytes(unsafe.Pointer(ptr), C.int(n)), nil
}

// Restore makes a terminal from a snapshot of Encode. o is what New takes:
// the size of the pane (the restored terminal is resized to it when the
// snapshot has another one), the history, the colours and the callbacks,
// which are options of the host and not in the snapshot. first and base
// are the absolute line numbers the old terminal had (Range): a line a
// client held keeps its number. When the alternate screen is active the
// numbering cannot be planted on the primary screen, and it restarts above
// every number handed out once the primary screen returns, as when a
// program erases its history (history.go).
func Restore(o Options, data []byte, first, base int64) (Term, error) {
	if len(data) == 0 || len(data) > maxSnapshot {
		return nil, fmt.Errorf("vt: restore: a snapshot of %d bytes", len(data))
	}
	if first < 0 || base < first {
		return nil, fmt.Errorf("vt: restore: line range %d..%d", first, base)
	}
	t := &term{o: o, cols: clampDim(o.Cols), rows: clampDim(o.Rows), history: max(o.History, 0), dirty: true}
	buf := C.CBytes(data)
	r := C.dt_snapshot_decode((*C.uint8_t)(buf), C.size_t(len(data)), C.size_t(continuationMax), &t.h)
	C.free(buf)
	if r != C.GHOSTTY_SUCCESS || t.h == nil {
		return nil, fmt.Errorf("vt: decode the snapshot: result %d", int(r))
	}
	t.handle = cgo.NewHandle(t)
	if id := identity.Load(); id != nil {
		t.xtv = C.CString(*id)
		t.xtvLen = len(*id)
	}
	if err := t.callbacks(); err != nil {
		t.free()
		return nil, err
	}
	if c, rr := t.libSize(); c != t.cols || rr != t.rows {
		if c < 1 || rr < 1 || c > maxDim || rr > maxDim {
			t.free()
			return nil, fmt.Errorf("vt: restore: the snapshot is %dx%d", c, rr)
		}
		C.ghostty_terminal_resize(t.h, C.uint16_t(t.cols), C.uint16_t(t.rows), cellWidthPx, cellHeightPx)
	}
	if hist := t.scrollbackRows(); hist > t.lineLimit(t.cols)+t.rows {
		t.free()
		return nil, fmt.Errorf("vt: restore: %d history rows, more than the limit %d", hist, t.lineLimit(t.cols))
	}
	if err := t.finish(); err != nil {
		return nil, err
	}
	t.historyState.restore(t, first, base)
	return t, nil
}
