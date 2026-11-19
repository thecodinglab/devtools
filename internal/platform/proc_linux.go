package platform

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// procRoot is the proc file system.
const procRoot = "/proc"

// Procs returns the process table from /proc. A process that ends while
// the table is read is left out; an error is returned only when /proc
// itself cannot be read.
func Procs() ([]Proc, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	procs := make([]Proc, 0, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		p, err := readProc(pid)
		if err != nil {
			continue
		}
		procs = append(procs, p)
	}
	return procs, nil
}

// readProc reads one process: its stat line for the name, the parent and
// the group, its cmdline for the arguments.
func readProc(pid int) (Proc, error) {
	st, err := readStat(pid)
	if err != nil {
		return Proc{}, err
	}
	p := st
	cmdline, err := os.ReadFile(fmt.Sprintf("%s/%d/cmdline", procRoot, pid))
	if err == nil {
		p.Args = splitCmdline(cmdline)
	}
	return p, nil
}

// readStat parses /proc/<pid>/stat: "pid (comm) state ppid pgrp ...". The
// name may hold spaces and parentheses, so it ends at the last ')'.
func readStat(pid int) (Proc, error) {
	b, err := os.ReadFile(fmt.Sprintf("%s/%d/stat", procRoot, pid))
	if err != nil {
		return Proc{}, err
	}
	return parseStat(pid, string(b))
}

func parseStat(pid int, s string) (Proc, error) {
	open := strings.IndexByte(s, '(')
	end := strings.LastIndexByte(s, ')')
	if open < 0 || end < open {
		return Proc{}, errors.New("platform: malformed stat of " + strconv.Itoa(pid))
	}
	fields := strings.Fields(s[end+1:])
	// fields[0] is the state, [1] the parent, [2] the process group.
	if len(fields) < 3 {
		return Proc{}, errors.New("platform: short stat of " + strconv.Itoa(pid))
	}
	ppid, err1 := strconv.Atoi(fields[1])
	pgid, err2 := strconv.Atoi(fields[2])
	if err1 != nil || err2 != nil {
		return Proc{}, errors.New("platform: malformed stat of " + strconv.Itoa(pid))
	}
	return Proc{Pid: pid, PPid: ppid, Pgid: pgid, Name: s[open+1 : end]}, nil
}

// splitCmdline splits the NUL-separated arguments of /proc/<pid>/cmdline.
// A kernel thread has none.
func splitCmdline(b []byte) []string {
	b = bytes.TrimSuffix(b, []byte{0})
	if len(b) == 0 {
		return nil
	}
	parts := bytes.Split(b, []byte{0})
	args := make([]string, len(parts))
	for i, p := range parts {
		args[i] = string(p)
	}
	return args
}

// Cwd returns the working directory of a process, from /proc/<pid>/cwd.
func Cwd(pid int) (string, error) {
	return os.Readlink(fmt.Sprintf("%s/%d/cwd", procRoot, pid))
}

func parentOf(pid int) (int, error) {
	p, err := readStat(pid)
	if err != nil {
		return 0, err
	}
	return p.PPid, nil
}
