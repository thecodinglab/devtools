package handover

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// EnvVar is the variable that tells a new server image that it takes over
// from an old one, and which descriptors it inherited. Boot reads it first
// and removes it from the environment; no pane ever sees it.
const EnvVar = "DEVTOOLS_HANDOVER"

// Env is the inventory of DEVTOOLS_HANDOVER: every inherited descriptor is
// named here, independently of the payload, so that a new image that cannot
// read the payload still knows what it holds. Every pane terminal lies in
// [Lo, Hi], which holds nothing else.
type Env struct {
	V        int    // the schema version of the payload
	ID       string // the attempt id
	Attempt  int    // 1: the new build; 2: the old build after an exec back
	Payload  int    // the payload's descriptor
	Lock     int    // server.lock, flocked
	Listener int    // the socket
	Lo, Hi   int    // the range of the pane terminals; Lo > Hi when there are none
	Exe      int    // the old build's executable, open: the rollback handle
	From     string // the absolute path of the old build, for the exec back
}

var idForm = regexp.MustCompile(`^[0-9a-f]{8,32}$`)

// String is the value of DEVTOOLS_HANDOVER.
func (e Env) String() string {
	return fmt.Sprintf("v=%d;id=%s;attempt=%d;fd=%d;lock=%d;ln=%d;range=%d-%d;exe=%d;from=%s",
		e.V, e.ID, e.Attempt, e.Payload, e.Lock, e.Listener, e.Lo, e.Hi, e.Exe, e.From)
}

// ParseEnv reads DEVTOOLS_HANDOVER strictly: every key once, in any order,
// nothing else, numbers in range, the path absolute and clean.
func ParseEnv(s string) (Env, error) {
	var e Env
	seen := map[string]bool{}
	num := func(v string, min int) (int, error) {
		n, err := strconv.Atoi(v)
		if err != nil || n < min || n > 1<<24 || strconv.Itoa(n) != v {
			return 0, fmt.Errorf("handover: %q is not a number", v)
		}
		return n, nil
	}
	for _, part := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok || seen[k] {
			return Env{}, fmt.Errorf("handover: %s: malformed or repeated %q", EnvVar, part)
		}
		seen[k] = true
		var err error
		switch k {
		case "v":
			e.V, err = num(v, 1)
		case "id":
			if !idForm.MatchString(v) {
				err = fmt.Errorf("handover: attempt id %q", v)
			}
			e.ID = v
		case "attempt":
			e.Attempt, err = num(v, 1)
			if err == nil && e.Attempt > 2 {
				err = fmt.Errorf("handover: attempt %d", e.Attempt)
			}
		case "fd":
			e.Payload, err = num(v, 3)
		case "lock":
			e.Lock, err = num(v, 3)
		case "ln":
			e.Listener, err = num(v, 3)
		case "exe":
			e.Exe, err = num(v, 3)
		case "range":
			lo, hi, ok := strings.Cut(v, "-")
			if !ok {
				err = fmt.Errorf("handover: range %q", v)
				break
			}
			if e.Lo, err = num(lo, 3); err == nil {
				e.Hi, err = num(hi, 2)
			}
		case "from":
			if v == "" || !filepath.IsAbs(v) || filepath.Clean(v) != v || strings.ContainsAny(v, ";\x00") {
				err = fmt.Errorf("handover: from %q", v)
			}
			e.From = v
		default:
			err = fmt.Errorf("handover: %s: unknown key %q", EnvVar, k)
		}
		if err != nil {
			return Env{}, err
		}
	}
	for _, k := range []string{"v", "id", "attempt", "fd", "lock", "ln", "range", "exe", "from"} {
		if !seen[k] {
			return Env{}, fmt.Errorf("handover: %s lacks %s", EnvVar, k)
		}
	}
	fds := map[int]bool{}
	for _, fd := range []int{e.Payload, e.Lock, e.Listener, e.Exe} {
		if fds[fd] || (e.Lo <= e.Hi && fd >= e.Lo && fd <= e.Hi) {
			return Env{}, errors.New("handover: a descriptor is named twice")
		}
		fds[fd] = true
	}
	if e.Hi >= e.Lo && e.Hi-e.Lo >= MaxPanes {
		return Env{}, fmt.Errorf("handover: range %d-%d", e.Lo, e.Hi)
	}
	return e, nil
}

// InRange reports whether fd is in the range of the pane terminals.
func (e Env) InRange(fd int) bool { return e.Lo <= e.Hi && fd >= e.Lo && fd <= e.Hi }
