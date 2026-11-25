package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"devtools/internal/handover"
	"devtools/internal/proto"
)

// The measurements of P8 (PLAN-live-upgrade.md 4.1, 8): a live upgrade of
// a server with many panes full of scrollback, one of them on the
// alternate screen: the size of the payload, the time from the freeze to
// the new image serving, the pause a client sees, the peak resident size
// of the server. Opt-in: DEVTOOLS_E2E_P8=1; the numbers are logged.

const envP8 = "DEVTOOLS_E2E_P8"

const (
	p8Panes    = 40
	p8FloodMiB = 1 // about 13,000 lines of 80 columns: more than the history keeps
)

// rssOf is rssBytes without a test: 0 when it cannot be read.
func rssOf(pid int) int64 {
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			return 0
		}
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "VmRSS:"); ok {
				kib, _ := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")), 10, 64)
				return kib << 10
			}
		}
		return 0
	}
	out, err := exec.Command("/bin/ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	kib, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return kib << 10
}

// logLine is one line of the server's log: its time and attributes.
type logLine struct {
	at    time.Time
	msg   string
	attrs map[string]string
}

// parseLog reads the text handler's lines (time=... level=... msg="..." k=v).
func parseLog(log string) []logLine {
	var out []logLine
	for _, line := range strings.Split(log, "\n") {
		l := logLine{attrs: map[string]string{}}
		for rest := line; rest != ""; {
			k, after, ok := strings.Cut(rest, "=")
			if !ok {
				break
			}
			var v string
			if strings.HasPrefix(after, `"`) {
				end := strings.Index(after[1:], `"`)
				if end < 0 {
					break
				}
				v, rest = after[1:end+1], strings.TrimPrefix(after[end+2:], " ")
			} else {
				v, rest, _ = strings.Cut(after, " ")
			}
			l.attrs[strings.TrimSpace(k)] = v
		}
		at, err := time.Parse(time.RFC3339Nano, l.attrs["time"])
		if err != nil {
			continue
		}
		l.at, l.msg = at, l.attrs["msg"]
		out = append(out, l)
	}
	return out
}

func mibStr(b int64) string { return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20)) }

func TestLiveUpgradeP8Measurements(t *testing.T) {
	if os.Getenv(envP8) != "1" {
		t.Skipf("%s=1 runs the measurements of P8", envP8)
	}
	needV2(t)
	m := newMachine(t, "desktop")
	guardServices(t, m)
	m.link(fakeFloodName, self())
	m.Start()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("server log:\n%s", m.ServerLog())
		}
	})
	project(t, m, "alpha")
	x := openSession(t, m, plain(m, 100, 30), "alpha")
	fill := func(i int) {
		x.Type(fmt.Sprintf("%s %d; echo p8-filled-$((%d+0))\r", fakeFloodName, p8FloodMiB, i))
		waitText(t, x, 60*time.Second, fmt.Sprintf("p8-filled-%d", i))
	}
	fillStart := time.Now()
	fill(1)
	for i := 2; i <= p8Panes; i++ {
		x.Key("prefix+c")
		eventually(t, 10*time.Second, "a new tab", func() bool { return len(snapshot(t, m).Tabs) == i })
		fill(i)
	}
	x.Type("printf '\\033[?1049h'; echo in-alt-$((6*7))\r")
	waitText(t, x, 10*time.Second, "in-alt-42")
	t.Logf("%d panes filled in %v", p8Panes, time.Since(fillStart).Round(time.Millisecond))
	snap := snapshot(t, m)
	if len(snap.Panes) != p8Panes {
		t.Fatalf("%d panes", len(snap.Panes))
	}
	first := snap.Panes[0].ID
	altID := ""
	for _, p := range snap.Panes {
		if strings.Contains(paneText(t, m, p.ID), "in-alt-42") {
			altID = p.ID
		}
	}
	firstLines := strings.Count(paneText(t, m, first), "\n")
	t.Logf("pane %s holds %d lines (history and screen); the alternate screen is in %s", first, firstLines, altID)

	for _, dir := range []struct {
		name, target, version string
	}{
		{"to the second build", binaryV2, versionV2},
		{"back to the first build", Binary(), m.Welcome().Version},
	} {
		pid := m.ServerPid()
		rss0 := rssOf(pid)
		logBefore := len(m.ServerLog())
		var mu sync.Mutex
		peak := rss0
		type probe struct {
			start, end time.Time
			version    string
			ok         bool
		}
		var probes []probe
		stop := make(chan struct{})
		var wg sync.WaitGroup
		var once sync.Once
		end := func() { once.Do(func() { close(stop); wg.Wait() }) }
		t.Cleanup(end) // also when a check below ends the test
		wg.Add(2)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if r := rssOf(pid); r > 0 {
					mu.Lock()
					peak = max(peak, r)
					mu.Unlock()
				}
				time.Sleep(2 * time.Millisecond)
			}
		}()
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				start := time.Now()
				w, _, err := oneshot(m.Socket(), nil, 20*time.Second)
				mu.Lock()
				probes = append(probes, probe{start: start, end: time.Now(), version: w.Version, ok: err == nil})
				mu.Unlock()
				time.Sleep(5 * time.Millisecond)
			}
		}()
		installBuild(t, m, dir.target)
		start := time.Now()
		r := m.RunWith(RunOptions{Timeout: 120 * time.Second}, "server", "restart")
		restartTook := time.Since(start)
		if r.Exit != 0 {
			end()
			t.Fatalf("server restart %s: exit %d\n%s%s", dir.name, r.Exit, r.Stdout, r.Stderr)
		}
		w := waitServing(t, m, 60*time.Second, "the new build serves", func(w proto.Welcome) bool { return w.Version == dir.version })
		eventually(t, 30*time.Second, "the window reconnected", func() bool { return m.Welcome().Clients >= 1 })
		reconnected := time.Since(start)
		time.Sleep(500 * time.Millisecond)
		end()

		if w.Pid != pid || w.Upgraded == nil || w.Upgraded.Kept != p8Panes || w.Upgraded.Error != "" {
			t.Errorf("%s: welcome %+v, upgraded %+v", dir.name, w, w.Upgraded)
		}
		if !strings.Contains(paneText(t, m, altID), "in-alt-42") {
			t.Errorf("%s: the pane on the alternate screen lost it", dir.name)
		}
		if n := strings.Count(paneText(t, m, first), "\n"); n != firstLines {
			t.Errorf("%s: pane %s holds %d lines, held %d", dir.name, first, n, firstLines)
		}

		var handing, execAt, adopted, listening time.Time
		var bytes, rssOld, rssNew int64
		for _, l := range parseLog(m.ServerLog()[logBefore:]) {
			switch {
			case l.msg == "upgrade: handing over" && handing.IsZero():
				handing = l.at
			case l.msg == "upgrade: exec":
				execAt = l.at
				bytes, _ = strconv.ParseInt(l.attrs["bytes"], 10, 64)
				rssOld, _ = strconv.ParseInt(l.attrs["maxrss"], 10, 64)
			case l.msg == "upgrade: adopted":
				adopted = l.at
				rssNew, _ = strconv.ParseInt(l.attrs["maxrss"], 10, 64)
			case l.msg == "listening" && !adopted.IsZero() && listening.IsZero():
				listening = l.at
			}
		}
		if handing.IsZero() || execAt.IsZero() || adopted.IsZero() || listening.IsZero() {
			t.Fatalf("%s: the log lacks a step:\n%s", dir.name, m.ServerLog()[logBefore:])
		}
		var lastOld, firstNew time.Time
		var longest time.Duration
		failed := 0
		for _, p := range probes {
			if !p.ok {
				failed++
				continue
			}
			if p.end.Before(start) {
				continue
			}
			longest = max(longest, p.end.Sub(p.start))
			if p.version != dir.version && p.end.After(lastOld) {
				lastOld = p.end
			}
			if p.version == dir.version && (firstNew.IsZero() || p.end.Before(firstNew)) {
				firstNew = p.end
			}
		}
		if bytes <= 0 || bytes > handover.MaxTotal {
			t.Errorf("%s: payload %d bytes, the limit %d", dir.name, bytes, handover.MaxTotal)
		}
		if per := bytes / p8Panes; per > handover.MaxScreen {
			t.Errorf("%s: %d bytes per pane, the screen limit %d", dir.name, per, handover.MaxScreen)
		}
		t.Logf("P8 %s on %s/%s, %d panes (%d MiB flood each, one on the alternate screen):", dir.name, runtime.GOOS, runtime.GOARCH, p8Panes, p8FloodMiB)
		t.Logf("  payload            %d bytes (%s, %s per pane; limits: screen %s, total %s)", bytes, mibStr(bytes), mibStr(bytes/p8Panes), mibStr(handover.MaxScreen), mibStr(handover.MaxTotal))
		t.Logf("  handing over->exec %v (hold, input gate, freeze and encode of every pane, payload)", execAt.Sub(handing))
		t.Logf("  exec->adopted      %v (the new image starts, decodes and stages, commits)", adopted.Sub(execAt))
		t.Logf("  adopted->listening %v", listening.Sub(adopted))
		t.Logf("  freeze->serving    %v (handing over to the new image's listening)", listening.Sub(handing))
		t.Logf("  probe pause        %v (last welcome of the old build to the first of the new one; longest single probe %v; %d probes failed)", firstNew.Sub(lastOld), longest, failed)
		t.Logf("  window reconnect   %v after server restart started (restart command took %v)", reconnected, restartTook)
		t.Logf("  rss                before %s, sampled peak %s; maxrss logged at exec %s, after adoption %s", mibStr(rss0), mibStr(peak), mibStr(rssOld), mibStr(rssNew))
	}
}
