package servertest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"devtools/internal/handover"
	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
	"devtools/internal/server"
	"devtools/internal/server/servertest"
	"devtools/internal/testkit"
	"devtools/internal/testkit/golden"
)

// upgradeNow asks the server for a live upgrade with force and
// acknowledges the acceptance, as devtools server restart does.
func upgradeNow(t *testing.T, socket string) (proto.ServerUpgradeResult, proto.Reply) {
	t.Helper()
	o := prototest.Dial(t, socket, prototest.Oneshot(""))
	defer o.Close()
	r := o.Request(proto.ServerUpgrade{Force: true})
	var res proto.ServerUpgradeResult
	if r.OK {
		if err := r.Msg.Decode(&res); err != nil {
			t.Fatal(err)
		}
		o.Send(proto.ServerUpgradeAck{ID: res.ID})
	}
	return res, r
}

// hexOf is what the hex helper prints for s.
func hexOf(s string) string {
	parts := make([]string, len(s))
	for i := 0; i < len(s); i++ {
		parts[i] = fmt.Sprintf("%02x", s[i])
	}
	return strings.Join(parts, " ")
}

func TestALiveUpgradeKeepsTheProgramsAndTheirScreens(t *testing.T) {
	dir := testkit.ShortTempDir(t)
	socket := servertest.Start(t, dir, servertest.WithLiveUpgrade())
	c := prototest.Dial(t, socket, prototest.Interactive())
	boot := c.Welcome().Boot
	if !hasCap(c.Welcome().Caps, proto.CapUpgrade) {
		t.Fatalf("welcome.caps %v lacks %s", c.Welcome().Caps, proto.CapUpgrade)
	}
	v := c.View(proto.ViewSet{Dir: dir, Name: "work", Cols: 80, Rows: 24})
	left := v.Panes[0]
	c.WaitFor(left, "ready")
	split := c.Request(proto.PaneSplit{Pane: left, Direction: proto.SplitRight})
	if !split.OK {
		t.Fatalf("pane.split: %s %s", split.Code, split.Text)
	}
	var sr proto.PaneSplitResult
	split.Msg.Decode(&sr)
	right := sr.Pane
	c.WaitFor(right, "ready")
	c.Type(left, "ab")
	c.WaitFor(left, hexOf("ab"))
	c.Type(right, "\x1b[?2004h") // the helper prints what it reads; the mode is set by the output below
	c.WaitFor(right, hexOf("\x1b[?2004h"))

	res, r := upgradeNow(t, socket)
	if !r.OK {
		t.Fatalf("server.upgrade: %s %s", r.Code, r.Text)
	}
	if res.ID == "" || res.Panes != 2 || res.From != servertest.Version {
		t.Fatalf("result %+v", res)
	}
	c.WaitClosed()
	testkit.Eventually(t, 10*time.Second, "the upgrade completed", func() bool { return servertest.Upgrades(socket) == 1 })

	d := prototest.Dial(t, socket, prototest.Interactive())
	w := d.Welcome()
	if w.Boot != boot {
		t.Fatalf("boot %s after the upgrade, was %s: a window would call it a restart", w.Boot, boot)
	}
	if w.Upgraded == nil || w.Upgraded.ID != res.ID || w.Upgraded.Kept != 2 || w.Upgraded.Error != "" {
		t.Fatalf("welcome.upgraded %+v", w.Upgraded)
	}
	st := d.State()
	for _, id := range []string{left, right} {
		if p, ok := st.Panes[id]; !ok || p.State != proto.PaneRunning {
			t.Fatalf("pane %s after the upgrade: %+v", id, p)
		}
	}
	d.View(proto.ViewSet{Session: v.Session, Cols: 80, Rows: 24})
	// The screens came over: the old output, without a second "ready".
	scr := d.WaitFor(left, hexOf("ab"))
	if n := strings.Count(strings.Join(scr.Lines, "\n"), "ready"); n != 1 {
		t.Fatalf("the left pane shows ready %d times: its program was started again\n%v", n, scr.Lines)
	}
	// The same programs read the input that comes after.
	d.Type(left, "c")
	d.WaitFor(left, hexOf("abc"))
	d.Type(right, "z")
	d.WaitScreen(right, "the right pane's program reads on", func(s golden.Screen) bool {
		return s.Contains(hexOf("\x1b[?2004h") + " 7a")
	})

	// And again: an upgraded server upgrades too.
	res2, r := upgradeNow(t, socket)
	if !r.OK || res2.ID == res.ID {
		t.Fatalf("second server.upgrade: %+v %s %s", res2, r.Code, r.Text)
	}
	d.WaitClosed()
	testkit.Eventually(t, 10*time.Second, "the second upgrade completed", func() bool { return servertest.Upgrades(socket) == 2 })
	e := prototest.Dial(t, socket, prototest.Interactive())
	if e.Welcome().Boot != boot || e.Welcome().Upgraded.ID != res2.ID {
		t.Fatalf("after the second upgrade: %+v", e.Welcome())
	}
	e.View(proto.ViewSet{Session: v.Session, Cols: 80, Rows: 24})
	e.Type(left, "d")
	e.WaitFor(left, hexOf("abcd"))
}

func TestWithoutForceTheRunningBuildIsCurrent(t *testing.T) {
	dir := testkit.ShortTempDir(t)
	socket := servertest.Start(t, dir, servertest.WithLiveUpgrade())
	o := prototest.Dial(t, socket, prototest.Oneshot(""))
	r := o.Request(proto.ServerUpgrade{})
	if r.OK || r.Code != proto.CodeCurrent {
		t.Fatalf("server.upgrade without force: %+v", r)
	}
}

func TestAFailedExecRollsBackAndTheProgramsGoOn(t *testing.T) {
	dir := testkit.ShortTempDir(t)
	socket := servertest.Start(t, dir, servertest.WithLiveUpgrade(), servertest.WithOptions(func(o *server.Options) {
		o.Exec = func(h *server.Handover) error {
			if len(h.Masters) != 1 || h.Header.State.Sessions == nil {
				return errors.New("unexpected handover")
			}
			h.Accept()
			return errors.New("exec: no such file")
		}
	}))
	c := prototest.Dial(t, socket, prototest.Interactive())
	v := c.View(proto.ViewSet{Dir: dir, Name: "work", Cols: 80, Rows: 24})
	p := v.Panes[0]
	c.WaitFor(p, "ready")
	c.Type(p, "ab")
	c.WaitFor(p, hexOf("ab"))
	res, r := upgradeNow(t, socket)
	if !r.OK {
		t.Fatalf("server.upgrade: %+v", r)
	}
	testkit.Eventually(t, 10*time.Second, "the failure is in the welcome", func() bool {
		w, _, err := oneshotWelcome(socket)
		return err == nil && w.Upgraded != nil && w.Upgraded.ID == res.ID && w.Upgraded.Error != ""
	})
	// The window stayed connected, and the program reads on.
	c.Type(p, "c")
	c.WaitFor(p, hexOf("abc"))
	if c.Closed() {
		t.Fatalf("the window's connection ended")
	}
	// And the server still answers mutations and saves.
	if r := c.Request(proto.TabNew{Session: v.Session}); !r.OK {
		t.Fatalf("tab.new after the rollback: %+v", r)
	}
}

func TestAnOpenPasteHoldsTheUpgradeOff(t *testing.T) {
	dir := testkit.ShortTempDir(t)
	socket := servertest.Start(t, dir, servertest.WithLiveUpgrade())
	c := prototest.Dial(t, socket, prototest.Interactive())
	v := c.View(proto.ViewSet{Dir: dir, Name: "work", Cols: 80, Rows: 24})
	p := v.Panes[0]
	c.WaitFor(p, "ready")
	c.Send(proto.Paste{Pane: p, Text: "x", More: true})
	c.WaitFor(p, hexOf("x"))
	if _, r := upgradeNow(t, socket); r.OK || r.Code != proto.CodeBusy {
		t.Fatalf("server.upgrade with a paste open: %+v", r)
	}
	c.Send(proto.Paste{Pane: p, Text: "y"})
	c.WaitFor(p, hexOf("xy"))
	if _, r := upgradeNow(t, socket); !r.OK {
		t.Fatalf("server.upgrade after the paste: %+v", r)
	}
	c.WaitClosed()
}

func TestTheHandoverSavesTheState(t *testing.T) {
	dir := testkit.ShortTempDir(t)
	socket := servertest.Start(t, dir, servertest.WithLiveUpgrade())
	c := prototest.Dial(t, socket, prototest.Interactive())
	v := c.View(proto.ViewSet{Dir: dir, Name: "work", Cols: 80, Rows: 24})
	c.WaitFor(v.Panes[0], "ready")
	os.Remove(filepath.Join(dir, "state.json"))
	if _, r := upgradeNow(t, socket); !r.OK {
		t.Fatalf("server.upgrade: %+v", r)
	}
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil || !strings.Contains(string(b), v.Session) {
		t.Fatalf("state.json after the handover: %s, %v", b, err)
	}
}

func hasCap(caps []string, c string) bool {
	for _, x := range caps {
		if x == c {
			return true
		}
	}
	return false
}

// oneshotWelcome is the welcome of a one-shot connection, without a test
// failure when the server does not answer.
func oneshotWelcome(socket string) (proto.Welcome, bool, error) {
	nc, err := netDial(socket)
	if err != nil {
		return proto.Welcome{}, false, err
	}
	defer nc.Close()
	nc.SetDeadline(time.Now().Add(2 * time.Second))
	if err := proto.WriteMsg(nc, proto.Encode(prototest.Oneshot(""))); err != nil {
		return proto.Welcome{}, false, err
	}
	m, err := proto.ReadMsg(nc)
	if err != nil {
		return proto.Welcome{}, false, err
	}
	var w proto.Welcome
	err = m.Decode(&w)
	return w, m.T == proto.TypeWelcome, err
}

func netDial(socket string) (net.Conn, error) { return net.DialTimeout("unix", socket, 2*time.Second) }

func TestAnExitedPaneKeepsItsStateAcrossTheUpgrade(t *testing.T) {
	dir := testkit.ShortTempDir(t)
	socket := servertest.Start(t, dir, servertest.WithLiveUpgrade(), servertest.WithHelper("killself", "KILL"))
	c := prototest.Dial(t, socket, prototest.Interactive())
	v := c.View(proto.ViewSet{Dir: dir, Name: "work", Cols: 80, Rows: 24})
	p := v.Panes[0]
	c.WaitState("the pane exited", func(s proto.State) bool { return s.Panes[p].State == proto.PaneExited })
	if _, r := upgradeNow(t, socket); !r.OK {
		t.Fatalf("server.upgrade: %+v", r)
	}
	c.WaitClosed()
	testkit.Eventually(t, 10*time.Second, "the upgrade completed", func() bool { return servertest.Upgrades(socket) == 1 })
	d := prototest.Dial(t, socket, prototest.Interactive())
	st := d.State()
	if got := st.Panes[p]; got.State != proto.PaneExited || got.Exit == nil || got.Exit.Signal == "" {
		t.Fatalf("pane after the upgrade %+v", got)
	}
	if w := d.Welcome(); w.Upgraded == nil || w.Upgraded.Kept != 0 {
		t.Fatalf("welcome.upgraded %+v", w.Upgraded)
	}
}

// installed is a program path in dir, a symbolic link to build b, which
// relink moves as a switch of the profile does.
type installed struct {
	t    *testing.T
	dir  string
	link string
}

func newInstalled(t *testing.T, builds ...string) installed {
	t.Helper()
	dir := testkit.ShortTempDir(t)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	for _, b := range builds {
		if err := os.WriteFile(filepath.Join(dir, b), []byte("build "+b), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	in := installed{t: t, dir: dir, link: filepath.Join(dir, "devtools")}
	in.relink(builds[0])
	return in
}

func (in installed) path(b string) string { return filepath.Join(in.dir, b) }

func (in installed) relink(b string) {
	tmp := in.link + ".new"
	os.Remove(tmp)
	if err := os.Symlink(in.path(b), tmp); err != nil {
		in.t.Fatal(err)
	}
	if err := os.Rename(tmp, in.link); err != nil {
		in.t.Fatal(err)
	}
}

// A manual target replaced after its preflight is refused, not exec'd.
func TestATargetReplacedAfterItsCheckIsNotExecd(t *testing.T) {
	in := newInstalled(t, "b")
	var verified []error
	checks := 0
	dir := testkit.ShortTempDir(t)
	socket := servertest.Start(t, dir, servertest.WithLiveUpgrade(), servertest.WithOptions(func(o *server.Options) {
		o.Program = in.link
		check := o.Check
		o.Check = func(ctx context.Context, path string, args []string) ([]byte, error) {
			if checks++; checks == 1 {
				// a local build rebuilt in place while it is being checked
				os.WriteFile(in.path("b")+".tmp", []byte("build b, again"), 0o755)
				os.Rename(in.path("b")+".tmp", in.path("b"))
			}
			return check(ctx, path, args)
		}
		o.Exec = func(h *server.Handover) error {
			err := h.Verify()
			verified = append(verified, err)
			if err != nil {
				return err
			}
			return errors.New("exec: not in this test")
		}
	}))
	_, r := upgradeNow(t, socket)
	if r.OK || r.Code != proto.CodeBusy || !strings.Contains(r.Text, "changed after it was checked") {
		t.Fatalf("server.upgrade of a target replaced during its check: %+v", r)
	}
	if len(verified) != 1 || verified[0] == nil {
		t.Fatalf("Verify said %v", verified)
	}
	if w, _, _ := oneshotWelcome(socket); w.Upgraded != nil {
		t.Fatalf("a refusal before the exec is in the welcome: %+v", w.Upgraded)
	}
}

// The automatic upgrade execs exactly the candidate it settled on: a
// switch to C and back to B while B is checked execs B, and a switch to C
// that stays makes the attempt to B not happen.
func TestTheAutomaticUpgradeExecsTheCandidateItChecked(t *testing.T) {
	for _, back := range []bool{true, false} {
		t.Run(map[bool]string{true: "B, C, B", false: "B, C"}[back], func(t *testing.T) {
			in := newInstalled(t, "b", "c")
			type execd struct {
				path string
				auto bool
				err  error
			}
			var mu sync.Mutex
			var execs []execd
			checks := 0
			dir := testkit.ShortTempDir(t)
			servertest.Start(t, dir, servertest.WithLiveUpgrade(), servertest.WithOptions(func(o *server.Options) {
				o.Program = in.link
				o.AutoUpgrade = true
				o.UpdateInterval, o.UpgradeSettle, o.UpgradeIdle, o.UpgradeEvery = 5*time.Millisecond, 20*time.Millisecond, time.Nanosecond, time.Millisecond
				o.UpgradeTargetOK = func(p string) bool { return strings.HasPrefix(p, in.dir+"/") }
				check := o.Check
				o.Check = func(ctx context.Context, path string, args []string) ([]byte, error) {
					mu.Lock()
					checks++
					first := checks == 1
					mu.Unlock()
					if first {
						in.relink("c")
						if back {
							in.relink("b")
						}
					}
					return check(ctx, path, args)
				}
				o.Exec = func(h *server.Handover) error {
					mu.Lock()
					execs = append(execs, execd{h.Target.Path, h.Auto, h.Verify()})
					mu.Unlock()
					return errors.New("exec: not in this test")
				}
			}))
			testkit.Eventually(t, 10*time.Second, "an automatic attempt", func() bool {
				mu.Lock()
				defer mu.Unlock()
				return len(execs) > 0
			})
			mu.Lock()
			defer mu.Unlock()
			want := in.path("c")
			if back {
				want = in.path("b")
			}
			if e := execs[0]; e.path != want || !e.auto || e.err != nil {
				t.Fatalf("the first automatic exec %+v, want %s", e, want)
			}
		})
	}
}

// The protocol generations are checked against the windows connected
// when the model is held, not those of before the preflight: a window that
// came during it and speaks what the target does not refuses the upgrade.
func TestAWindowThatCameDuringThePreflightIsCounted(t *testing.T) {
	var socket string
	var late *prototest.Client
	checks := 0
	dir := testkit.ShortTempDir(t)
	socket = servertest.Start(t, dir, servertest.WithLiveUpgrade(), servertest.WithOptions(func(o *server.Options) {
		check := o.Check
		o.Check = func(ctx context.Context, path string, args []string) ([]byte, error) {
			if checks++; checks == 1 {
				hello := prototest.Interactive()
				hello.Gen = 2
				late = prototest.Dial(t, socket, hello)
			}
			out, err := check(ctx, path, args)
			if err != nil || len(args) > 3 { // the sample: as it was
				return out, err
			}
			c := server.LocalCheck(servertest.Version)
			c.Gens = []int{proto.Gen} // the target does not speak 2
			return json.Marshal(c)
		}
	}))
	_, r := upgradeNow(t, socket)
	if r.OK || r.Code != proto.CodeIncompatible || !strings.Contains(r.Text, "[2]") {
		t.Fatalf("server.upgrade with a generation-2 window the target cannot serve: %+v", r)
	}
	if late == nil || late.Closed() {
		t.Fatalf("the window was disconnected")
	}
}

// A staging that fails after a pane was staged reports its own reason,
// and frees what it staged.
func TestALateStagingFailureKeepsItsReason(t *testing.T) {
	var stageErr error
	dir := testkit.ShortTempDir(t)
	socket := servertest.Start(t, dir, servertest.WithLiveUpgrade(), servertest.WithOptions(func(o *server.Options) {
		opts := *o
		o.Exec = func(h *server.Handover) error {
			masters := map[int]*os.File{}
			var dups []*os.File
			defer func() {
				for _, f := range dups {
					f.Close()
				}
			}()
			for i, m := range h.Masters {
				fd, err := platform.DupFile(m)
				if err != nil {
					return err
				}
				f := os.NewFile(uintptr(fd), "/dev/ptmx")
				dups = append(dups, f)
				h.Header.Panes[i].Master = fd
				if i == 0 {
					masters[fd] = f // the second pane's terminal is missing
				}
			}
			payload, err := os.CreateTemp(dir, "handover-*")
			if err != nil {
				return err
			}
			os.Remove(payload.Name())
			defer payload.Close()
			if err := h.Data.Write(payload, h.Header); err != nil {
				return err
			}
			rd, err := handover.Open(payload)
			if err != nil {
				return err
			}
			_, stageErr = server.Stage(opts, rd, masters)
			return stageErr
		}
	}))
	c := prototest.Dial(t, socket, prototest.Interactive())
	v := c.View(proto.ViewSet{Dir: dir, Name: "work", Cols: 80, Rows: 24})
	c.WaitFor(v.Panes[0], "ready")
	split := c.Request(proto.PaneSplit{Pane: v.Panes[0], Direction: proto.SplitRight})
	if !split.OK {
		t.Fatalf("pane.split: %+v", split)
	}
	upgradeNow(t, socket)
	if stageErr == nil || strings.Contains(stageErr.Error(), "panicked") || !strings.Contains(stageErr.Error(), "no inherited terminal") {
		t.Fatalf("the staging error %v", stageErr)
	}
}

// The idle watch of the automatic upgrade and the reader's pastes overlap
// without a race (go test -race).
func TestTheIdleWatchAndPastesDoNotRace(t *testing.T) {
	dir := testkit.ShortTempDir(t)
	socket := servertest.Start(t, dir, servertest.WithLiveUpgrade(), servertest.WithOptions(func(o *server.Options) {
		o.AutoUpgrade = true
		o.UpdateInterval = time.Millisecond
	}))
	c := prototest.Dial(t, socket, prototest.Interactive())
	v := c.View(proto.ViewSet{Dir: dir, Name: "work", Cols: 80, Rows: 24})
	p := v.Panes[0]
	c.WaitFor(p, "ready")
	for i := range 200 {
		c.Send(proto.Paste{Pane: p, Text: "x", More: i%2 == 0})
		if i%20 == 0 {
			time.Sleep(2 * time.Millisecond)
		}
	}
	c.Send(proto.Paste{Pane: p, Text: "!"})
	c.WaitFor(p, hexOf("!"))
}
