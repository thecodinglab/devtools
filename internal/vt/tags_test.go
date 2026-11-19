package vt

import (
	"bufio"
	"bytes"
	"go/build/constraint"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The cross vet of CHECK runs with cgo off and type-checks test files. A
// test file that calls New, a method of Term or an unexported function of a
// cgo file therefore needs //go:build cgo: without it the cross vet fails
// (the type term does not exist without cgo) or the test would run against
// nocgo.go and panic (5.3.2, 6.1 rule 9).

// The two markers of 5.3.2, built so that this file does not contain them.
var engineMarkers = []string{"New" + "(", "term" + ")"}

// mentionsEngine reports whether a test file mentions one of the markers.
func mentionsEngine(src []byte) bool {
	for _, m := range engineMarkers {
		if bytes.Contains(src, []byte(m)) {
			return true
		}
	}
	return false
}

// requiresCgo reports whether the file's build constraint, which must stand
// before the package clause, is false whenever cgo is off, whatever the
// other tags are.
func requiresCgo(src []byte) bool {
	sc := bufio.NewScanner(bytes.NewReader(src))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "package ") {
			return false
		}
		if !constraint.IsGoBuild(line) {
			continue
		}
		expr, err := constraint.Parse(line)
		if err != nil {
			return false
		}
		for _, others := range []bool{false, true} {
			if expr.Eval(func(tag string) bool { return tag != "cgo" && others }) {
				return false
			}
		}
		return true
	}
	return false
}

func TestEngineTestsAreBehindTheCgoTag(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no test files found; the test runs in the package directory")
	}
	self := "tags_test.go"
	for _, f := range files {
		if f == self {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if mentionsEngine(src) && !requiresCgo(src) {
			t.Errorf("%s calls the engine but does not start with //go:build cgo", f)
		}
	}
	src, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if mentionsEngine(src) {
		t.Fatalf("%s mentions a marker itself and could not be checked by its own rule", self)
	}
}

func TestTheTagCheckReadsTheConstraint(t *testing.T) {
	newCall := "t := " + engineMarkers[0] + "Options{})"
	method := "func (t *" + engineMarkers[1] + " x() {}"
	cases := []struct {
		src      string
		engine   bool
		needsTag bool // requiresCgo
	}{
		{"//go:build cgo\n\npackage vt\n\n" + newCall, true, true},
		{"//go:build cgo && !dynamic\n\npackage vt\n\n" + method, true, true},
		{"// Copyright\n\n//go:build cgo\n\npackage vt\n" + newCall, true, true},
		{"package vt\n\n" + newCall, true, false},
		{"//go:build linux\n\npackage vt\n\n" + newCall, true, false},
		{"//go:build cgo || linux\n\npackage vt\n\n" + method, true, false},
		{"//go:build !cgo\n\npackage vt\n\n" + newCall, true, false},
		{"package vt\n\n//go:build cgo\n" + newCall, true, false},
		{"package vt\n\nfunc TestKeyNames(t *testing.T) {}", false, false},
	}
	for i, c := range cases {
		if got := mentionsEngine([]byte(c.src)); got != c.engine {
			t.Errorf("case %d: mentionsEngine = %v", i, got)
		}
		if got := requiresCgo([]byte(c.src)); got != c.needsTag {
			t.Errorf("case %d: requiresCgo = %v", i, got)
		}
	}
}
