package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// The process mechanics of the live upgrade (internal/handover): the server
// replaces its image with a new build by execve(2) in the same process and
// keeps an allow-list of descriptors open across it. Everything else of the
// process is close-on-exec.

// SetCloexec sets (on) or clears the close-on-exec flag of fd.
func SetCloexec(fd int, on bool) error {
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		return err
	}
	if on {
		flags |= unix.FD_CLOEXEC
	} else {
		flags &^= unix.FD_CLOEXEC
	}
	_, err = unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags)
	return err
}

// Cloexec reports whether fd has the close-on-exec flag.
func Cloexec(fd int) (bool, error) {
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		return false, err
	}
	return flags&unix.FD_CLOEXEC != 0, nil
}

// HandoverFloor is the lowest descriptor number the kept descriptors are
// duplicated to. syscall.Exec lowers the soft limit of open files to the
// one the process started with (256 under launchd, 1024 under systemd)
// right before execve; the kept descriptors above it leave every low
// number free for the dynamic loader of the new image, and Go raises the
// soft limit again before main.
const HandoverFloor = 4096

// ErrNoRange is returned by DupFile when the open-file limits leave no room
// for the kept descriptors above HandoverFloor.
var ErrNoRange = errors.New("platform: the open-file limit leaves no room for the descriptors of a live upgrade")

// FDLimit returns the soft and the hard limit of open files.
func FDLimit() (cur, hard uint64, err error) {
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &lim); err != nil {
		return 0, 0, err
	}
	return uint64(lim.Cur), uint64(lim.Max), nil
}

// RoomFor reports whether n descriptors fit at or above HandoverFloor
// under the current soft limit.
func RoomFor(n int) error {
	cur, _, err := FDLimit()
	if err != nil {
		return err
	}
	if uint64(HandoverFloor+n+16) >= cur {
		return ErrNoRange
	}
	return nil
}

// DupFD duplicates fd, close-on-exec, to the lowest free number at or
// above HandoverFloor. The duplicate shares the open file description: its
// flags (O_NONBLOCK), its offset and a flock on it.
func DupFD(fd int) (int, error) {
	n, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, HandoverFloor)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EMFILE) {
		return -1, ErrNoRange
	}
	return n, err
}

// DupFile is DupFD of the descriptor of f, read through SyscallConn so
// that a polled descriptor stays non-blocking (File.Fd would switch it).
func DupFile(f *os.File) (int, error) {
	n := -1
	err := control(f, func(fd int) error {
		var err error
		n, err = DupFD(fd)
		return err
	})
	return n, err
}

// DupConn is DupFD of the descriptor of a listener or connection of the
// net package, through its SyscallConn, which leaves O_NONBLOCK alone.
func DupConn(c syscall.Conn) (int, error) {
	rc, err := c.SyscallConn()
	if err != nil {
		return -1, err
	}
	n := -1
	var inner error
	if err := rc.Control(func(fd uintptr) { n, inner = DupFD(int(fd)) }); err != nil {
		return -1, err
	}
	return n, inner
}

// execve is syscall.Exec; the tests replace it to make the exec fail.
var execve = syscall.Exec

// ExecKeeping replaces the process image with path, keeping exactly the
// descriptors of keep (and 0, 1, 2) open: under syscall.ForkLock, so no
// fork of this process can inherit a descriptor while its close-on-exec
// flag is clear, it clears the flag on keep, sets it on every other open
// descriptor above 2 and calls execve. It returns only when execve failed;
// then the flags are as before, and the soft limit of open files, which
// syscall.Exec lowered, is raised back to what it was (children started
// later inherit that raised limit).
func ExecKeeping(path string, argv, env []string, keep []int) error {
	syscall.ForkLock.Lock()
	defer syscall.ForkLock.Unlock()
	kept := map[int]bool{0: true, 1: true, 2: true}
	for _, fd := range keep {
		kept[fd] = true
	}
	var lim unix.Rlimit
	limErr := unix.Getrlimit(unix.RLIMIT_NOFILE, &lim)
	restore := func() {
		for _, fd := range keep {
			SetCloexec(fd, true)
		}
		if limErr == nil {
			syscall.Setrlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{Cur: lim.Cur, Max: lim.Max})
		}
	}
	for _, fd := range keep {
		if err := SetCloexec(fd, false); err != nil {
			restore()
			return fmt.Errorf("keep descriptor %d: %w", fd, err)
		}
	}
	open, err := openFDs()
	if err != nil {
		restore()
		return err
	}
	for _, fd := range open {
		if !kept[fd] {
			SetCloexec(fd, true) // a descriptor closed meanwhile fails; that is fine
		}
	}
	err = execve(path, argv, env)
	restore()
	return fmt.Errorf("exec %s: %w", path, err)
}

// OpenFDs lists the open descriptors of the process.
func OpenFDs() ([]int, error) { return openFDs() }

func openFDs() ([]int, error) {
	d, err := os.Open(fdDir)
	if err != nil {
		return nil, err
	}
	names, err := d.Readdirnames(-1)
	self := -1
	if rc, e := d.SyscallConn(); e == nil {
		rc.Control(func(fd uintptr) { self = int(fd) })
	}
	d.Close()
	if err != nil {
		return nil, err
	}
	var fds []int
	for _, n := range names {
		fd, err := strconv.Atoi(n)
		if err != nil || fd == self {
			continue
		}
		fds = append(fds, fd)
	}
	return fds, nil
}

// Identity is a file: its path and what tells it from another file.
type Identity struct {
	Path  string `json:"path"`
	Dev   uint64 `json:"dev"`
	Ino   uint64 `json:"ino"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"` // unix ns
}

// Same reports whether i and j are the same file.
func (i Identity) Same(j Identity) bool { return i.Dev == j.Dev && i.Ino == j.Ino }

// FileIdentity resolves path (symbolic links followed) and stats the file.
func FileIdentity(path string) (Identity, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Identity{}, err
	}
	var st unix.Stat_t
	if err := unix.Stat(resolved, &st); err != nil {
		return Identity{}, &os.PathError{Op: "stat", Path: resolved, Err: err}
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return Identity{}, fmt.Errorf("%s is not a regular file", resolved)
	}
	return Identity{Path: resolved, Dev: uint64(st.Dev), Ino: uint64(st.Ino), Size: st.Size, Mtime: timespecNs(st.Mtim)}, nil
}

func timespecNs(ts unix.Timespec) int64 { return ts.Sec*1e9 + int64(ts.Nsec) }

// FDIdentity stats an open descriptor: its device and inode.
func FDIdentity(fd int) (dev, ino uint64, err error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return 0, 0, err
	}
	return uint64(st.Dev), uint64(st.Ino), nil
}

// RunCheck runs path with args and env, without a terminal, standard input
// /dev/null, and returns its standard output, at most limit bytes. It is
// how the server asks a newly installed build whether it can take over
// (devtools server upgrade-check); ctx bounds it. A non-zero exit is an
// error that carries the first line the program wrote to standard error.
func RunCheck(ctx context.Context, path string, args, env []string, limit int) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	cmd.Dir = "/"
	out := &capped{limit: limit}
	var errOut capped
	errOut.limit = 4096
	cmd.Stdout = out
	cmd.Stderr = &errOut
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err := cmd.Run()
	if err != nil {
		line, _, _ := strings.Cut(strings.TrimSpace(errOut.b.String()), "\n")
		if line != "" {
			return out.b.Bytes(), fmt.Errorf("%s: %w: %s", filepath.Base(path), err, line)
		}
		return out.b.Bytes(), fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if out.over {
		return out.b.Bytes(), fmt.Errorf("%s: more than %d bytes of output", filepath.Base(path), limit)
	}
	return out.b.Bytes(), nil
}

type capped struct {
	b     bytes.Buffer
	limit int
	over  bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.limit - c.b.Len(); room < len(p) {
		c.over = true
		if room > 0 {
			c.b.Write(p[:room])
		}
		return len(p), nil
	}
	return c.b.Write(p)
}
