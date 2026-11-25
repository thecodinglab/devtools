package pane

import (
	"bufio"
	"context"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"devtools/internal/platform"
	"devtools/internal/testkit"
	"devtools/internal/vt"

	"golang.org/x/sys/unix"
)

func init() {
	testkit.RegisterHelper("drain", helperDrain)
	testkit.RegisterHelper("late", helperLate)
}

// helperDrain puts its terminal in raw mode, prints "ready" and does not
// read until the file args[0] exists; then it reads up to the first "!"
// and prints how many bytes it read before it and their checksum.
func helperDrain(args []string) int {
	rawNoRead()
	os.Stdout.WriteString("ready\r\n")
	for {
		if _, err := os.Stat(args[0]); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	r := bufio.NewReader(os.Stdin)
	data, _ := r.ReadBytes('!')
	data = data[:max(len(data)-1, 0)]
	fmt.Printf("got %d %08x\r\n", len(data), crc32.ChecksumIEEE(data))
	for {
		time.Sleep(time.Hour)
	}
}

// helperLate prints "ready", waits args[0] milliseconds and exits with
// status args[1].
func helperLate(args []string) int {
	os.Stdout.WriteString("ready\n")
	ms, _ := strconv.Atoi(args[0])
	time.Sleep(time.Duration(ms) * time.Millisecond)
	code, _ := strconv.Atoi(args[1])
	return code
}

func freeze(t *testing.T, p *Pane) Frozen {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	f, err := p.Freeze(ctx)
	if err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	return f
}

// adopt does in this process what the new server image does: duplicate the
// master, stage, abandon the old pane object, start the new one.
func adopt(t *testing.T, old *Pane, f Frozen, o Options) *Pane {
	t.Helper()
	fd, err := platform.DupFile(f.Master)
	if err != nil {
		t.Fatal(err)
	}
	m := os.NewFile(uintptr(fd), "/dev/ptmx")
	platform.SetCloexec(fd, true)
	s, err := StageAdopt(o, Adoption{Kind: f.Kind, Master: m, Pid: f.Pid, Info: f.Info, Screen: f.Screen, First: f.First, Base: f.Base, Pending: f.Pending})
	if err != nil {
		m.Close()
		t.Fatalf("StageAdopt: %v", err)
	}
	old.Abandon()
	p := s.Start()
	t.Cleanup(p.Close)
	return p
}

var tickRE = regexp.MustCompile(`tick (\d{6})`)

// ticksContiguous checks that the ticks on the screen count up by one.
func ticksContiguous(t *testing.T, text string) int {
	t.Helper()
	ms := tickRE.FindAllStringSubmatch(text, -1)
	prev := -1
	for _, m := range ms {
		n, _ := strconv.Atoi(m[1])
		if prev >= 0 && n != prev+1 {
			t.Fatalf("tick %d after tick %d: output was lost or doubled", n, prev)
		}
		prev = n
	}
	return prev
}

func TestFreezeAndThawLoseNoOutput(t *testing.T) {
	p, _ := startHelper(t, "ticks")
	waitScreen(t, p, "tick 000010")
	for range 3 {
		f := freeze(t, p)
		if f.Kind != FrozenRunning || f.Master == nil || len(f.Screen) == 0 {
			t.Fatalf("Frozen %+v", f)
		}
		time.Sleep(150 * time.Millisecond) // the helper blocks or fills the buffer meanwhile
		p.Thaw()
	}
	last := ticksContiguous(t, screen(p))
	waitScreen(t, p, fmt.Sprintf("tick %06d", last+20))
	ticksContiguous(t, screen(p))
}

func TestAPaneIsAdoptedWithItsProgramScreenAndInput(t *testing.T) {
	o, _ := options(t, "cat")
	p := start(t, o)
	p.Paste("before the upgrade\r", false, false)
	waitScreen(t, p, "before the upgrade")
	pid := p.Info().Pid
	f := freeze(t, p)
	o2, r2 := options(t, "cat")
	o2.Cols, o2.Rows = o.Cols, o.Rows
	q := adopt(t, p, f, o2)
	if got := q.Info().Pid; got != pid {
		t.Fatalf("pid after adoption %d, want %d", got, pid)
	}
	if !strings.Contains(screen(q), "before the upgrade") {
		t.Fatalf("the adopted screen lacks the old output:\n%s", screen(q))
	}
	q.Paste("after the upgrade\r", false, false)
	waitScreen(t, q, "after the upgrade")
	// The old pane object is gone without touching the program.
	p.Close()
	if q.Info().State != "running" {
		t.Fatalf("the program ended with the old pane object: %+v", q.Info())
	}
	q.Close()
	if n := r2.count(Exited); n != 1 {
		t.Fatalf("%d Exited events of the adopted pane, want 1", n)
	}
	if ex := q.Info().Exit; ex == nil || !ex.ByServer {
		t.Fatalf("exit %+v, want one caused by the server", ex)
	}
}

func TestPendingInputSurvivesTheAdoptionByteExact(t *testing.T) {
	dir := t.TempDir()
	trigger := filepath.Join(dir, "go")
	o, _ := options(t, "drain", trigger)
	p := start(t, o)
	waitScreen(t, p, "ready")
	var b strings.Builder
	for i := 0; b.Len() < 200<<10; i++ {
		b.WriteByte(byte('a' + i%26))
	}
	data := b.String()
	go func() {
		p.Paste(data, false, false)
		p.Paste("!", false, false)
	}()
	time.Sleep(300 * time.Millisecond) // the kernel's buffer is full, the writer waits
	f := freeze(t, p)
	if len(f.Pending) == 0 {
		t.Fatalf("nothing pending after a paste of %d bytes into a program that does not read", len(data))
	}
	o2, _ := options(t, "drain", trigger)
	o2.Cols, o2.Rows = o.Cols, o.Rows
	q := adopt(t, p, f, o2)
	os.WriteFile(trigger, nil, 0o600)
	want := fmt.Sprintf("got %d %08x", len(data), crc32.ChecksumIEEE([]byte(data)))
	waitScreen(t, q, want)
}

func TestAChildThatEndsWhileFrozenIsReapedByTheAdopter(t *testing.T) {
	o, _ := options(t, "late", "200", "3")
	p := start(t, o)
	waitScreen(t, p, "ready")
	f := freeze(t, p)
	time.Sleep(600 * time.Millisecond) // the child ends and is a zombie: nobody may reap it
	if f.Kind != FrozenRunning {
		t.Fatalf("kind %s", f.Kind)
	}
	o2, r2 := options(t, "late", "200", "3")
	q := adopt(t, p, f, o2)
	info := waitExit(t, q, r2)
	if info.Exit == nil || info.Exit.Code != 3 || info.Exit.ByServer {
		t.Fatalf("exit of the adopted pane %+v, want code 3", info.Exit)
	}
	if n := r2.count(Exited); n != 1 {
		t.Fatalf("%d Exited events, want 1", n)
	}
}

func TestAReapedButUnreportedExitIsReportedOnce(t *testing.T) {
	// The old image reaped the child, and its exit had not reached the
	// actor when the pane froze: the payload says "reaped" with the exit,
	// and the new image reports it once, after the output was read. The
	// race is made here by hand: the child is reaped by the test while the
	// pane is frozen.
	o, r := options(t, "late", "100", "4")
	p := start(t, o)
	waitScreen(t, p, "ready")
	f := freeze(t, p)
	var ws unix.WaitStatus
	if _, err := unix.Wait4(f.Pid, &ws, 0, nil); err != nil {
		t.Fatal(err)
	}
	f.Kind = FrozenReaped
	f.Info.Exit = &Exit{Code: ws.ExitStatus()}
	o2, r2 := options(t, "late", "100", "4")
	q := adopt(t, p, f, o2)
	info := waitExit(t, q, r2)
	if info.Exit == nil || info.Exit.Code != 4 {
		t.Fatalf("exit %+v", info.Exit)
	}
	if n := r2.count(Exited); n != 1 {
		t.Fatalf("%d Exited events, want 1", n)
	}
	if r.count(Exited) != 0 {
		t.Fatalf("the old pane object reported the exit too")
	}
}

func TestAnExitedPaneIsRestoredWithItsScreenAndExit(t *testing.T) {
	p, r := startHelper(t, "killself", "KILL")
	waitExit(t, p, r)
	f := freeze(t, p)
	if f.Kind != FrozenExited || f.Master != nil || f.Info.Exit == nil || f.Info.Exit.Signal == "" {
		t.Fatalf("Frozen %s %+v master %v", f.Kind, f.Info.Exit, f.Master)
	}
	o2, r2 := options(t, "killself", "KILL")
	s, err := StageRestore(o2, Restored{Info: f.Info, Screen: f.Screen, First: f.First, Base: f.Base})
	if err != nil {
		t.Fatal(err)
	}
	p.Thaw()
	q := s.Start()
	t.Cleanup(q.Close)
	info := waitExit(t, q, r2)
	if info.State != "exited" || info.Exit == nil || info.Exit.Signal != f.Info.Exit.Signal {
		t.Fatalf("restored %+v", info)
	}
}

func TestAResizeWhileFrozenWaitsForThaw(t *testing.T) {
	p, _ := startHelper(t, "size")
	waitScreen(t, p, "200 x 24")
	f := freeze(t, p)
	p.Resize(100, 30)
	time.Sleep(50 * time.Millisecond)
	var c, rr int
	probe(p, func(a *actor) { c, rr = a.term.Size() })
	if c != 200 || rr != 24 {
		t.Fatalf("size while frozen %dx%d, want 200x24", c, rr)
	}
	_ = f
	p.Thaw()
	testkit.Eventually(t, 5*time.Second, "the resize after the thaw", func() bool {
		probe(p, func(a *actor) { c, rr = a.term.Size() })
		return c == 100 && rr == 30
	})
}

func TestStagingChecksTheTerminal(t *testing.T) {
	p, _ := startHelper(t, "cat")
	f := freeze(t, p)
	defer p.Thaw()
	o2, _ := options(t, "cat")
	fd, err := platform.DupFile(f.Master)
	if err != nil {
		t.Fatal(err)
	}
	m := os.NewFile(uintptr(fd), "/dev/ptmx")
	defer m.Close()
	info := f.Info
	info.TTY = "/dev/not-this-one"
	if _, err := StageAdopt(o2, Adoption{Kind: f.Kind, Master: m, Pid: f.Pid, Info: info, Screen: f.Screen}); err == nil {
		t.Fatalf("an adoption of the wrong terminal was staged")
	}
	if _, err := StageAdopt(o2, Adoption{Kind: f.Kind, Master: m, Pid: f.Pid, Info: f.Info, Screen: []byte("junk")}); err == nil {
		t.Fatalf("an adoption with a broken screen was staged")
	}
	// Nothing of the staging touched the pane: it still runs after Thaw.
	p.Thaw()
	p.Paste("still here\r", false, false)
	waitScreen(t, p, "still here")
	_ = vt.Pos{}
}

// A write that the deadline of a freeze interrupted after a Thaw already
// ended the freeze (a Freeze that timed out and rolled back) is not a
// broken terminal: the writer puts the rest back and goes on, and every
// byte arrives. The deadline is set here without a freeze, which is the
// writer's view of exactly that moment.
func TestADeadlineAfterThawLosesNoInput(t *testing.T) {
	dir := t.TempDir()
	trigger := filepath.Join(dir, "go")
	o, _ := options(t, "drain", trigger)
	p := start(t, o)
	waitScreen(t, p, "ready")
	var b strings.Builder
	for i := 0; b.Len() < 200<<10; i++ {
		b.WriteByte(byte('a' + i%26))
	}
	data := b.String()
	go func() {
		p.Paste(data, false, false)
		p.Paste("!", false, false)
	}()
	time.Sleep(300 * time.Millisecond) // the kernel's buffer is full, the writer waits
	p.master.SetWriteDeadline(time.Unix(1, 0))
	time.Sleep(50 * time.Millisecond)
	p.master.SetWriteDeadline(time.Time{})
	os.WriteFile(trigger, nil, 0o600)
	waitScreen(t, p, fmt.Sprintf("got %d %08x", len(data), crc32.ChecksumIEEE([]byte(data))))
}
