package client

// The window size (3.2.4): the pane area is the window minus the status
// row. Below the chrome's minimum the client is "too small": one message is
// drawn, nothing is sent, the session keeps its size. A changed pane area
// goes to the fleet at once the first time, then at most once per
// resizeEvery.

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"devtools/internal/client/screen"
)

const resizeEvery = 50 * time.Millisecond

// resizer holds what was sent to the fleet and what waits.
type resizer struct {
	sent         bool // a size went to the fleet
	cols, rows   int  // the last size sent
	at           time.Time
	waiting      bool // a timer runs; the newest size is sent when it ends
	wantC, wantR int
}

// resize is a new window size.
func (m *Model) resize(w, h int) tea.Cmd {
	if w == m.w && h == m.h && m.sized {
		return nil
	}
	m.w, m.h, m.sized = max(w, 0), max(h, 0), true
	m.comp.full = true
	var cmds []tea.Cmd
	if m.overlay != nil {
		cmds = append(cmds, m.sizeOverlay())
	}
	if m.tooSmall() {
		return tea.Batch(cmds...)
	}
	cols, rows := m.paneSize()
	return tea.Batch(append(cmds, m.sendSize(cols, rows))...)
}

// sendSize sends cols by rows now, or when the debounce ends.
func (m *Model) sendSize(cols, rows int) tea.Cmd {
	rs := &m.rs
	rs.wantC, rs.wantR = cols, rows
	if rs.sent && rs.cols == cols && rs.rows == rows {
		return nil
	}
	now := m.hk.Now()
	if !rs.sent || now.Sub(rs.at) >= resizeEvery {
		if rs.waiting {
			// the timer that runs sends the newest size
			return nil
		}
		m.pushSize(cols, rows)
		return nil
	}
	if rs.waiting {
		return nil
	}
	rs.waiting = true
	return m.hk.After(rs.at.Add(resizeEvery).Sub(now), resizeDue{})
}

// resizeTimer ends the debounce: the newest size goes out unless the
// window is too small now or the size was sent already.
func (m *Model) resizeTimer() tea.Cmd {
	rs := &m.rs
	rs.waiting = false
	if m.tooSmall() || (rs.sent && rs.cols == rs.wantC && rs.rows == rs.wantR) {
		return nil
	}
	m.pushSize(rs.wantC, rs.wantR)
	return nil
}

func (m *Model) pushSize(cols, rows int) {
	rs := &m.rs
	rs.sent, rs.cols, rs.rows, rs.at = true, cols, rows, m.hk.Now()
	m.fleet.Resize(cols, rows)
}

// tooSmall reports whether the window is below the chrome's minimum
// (5a.6.8). Before the first size is known nothing is drawn either.
func (m *Model) tooSmall() bool {
	if !m.sized {
		return false
	}
	c, r := m.chrome.MinSize()
	return m.w < c || m.h < r
}

// paneSize is the pane area: the window minus the status rows.
func (m *Model) paneSize() (cols, rows int) {
	return m.w, max(m.h-m.chrome.StatusRows(), 0)
}

// paneArea is the pane area in canvas coordinates.
func (m *Model) paneArea() screen.Rect {
	c, r := m.paneSize()
	return screen.Rect{W: c, H: r}
}
