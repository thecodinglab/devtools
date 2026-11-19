package platform

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devtools/internal/testkit"
)

func TestLockIsExclusive(t *testing.T) {
	path := filepath.Join(testkit.ShortTempDir(t), "server.lock")
	f, err := Lock(path)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if g, err := Lock(path); !errors.Is(err, ErrLocked) {
		if g != nil {
			g.Close()
		}
		t.Fatalf("second Lock in the same process = %v, want ErrLocked", err)
	}

	_, out, _ := startHelper(t, "lockhold", nil, "", path)
	line, _ := out.ReadString('\n')
	if !strings.HasPrefix(line, "error "+ErrLocked.Error()) {
		t.Errorf("Lock in another process said %q, want ErrLocked", line)
	}

	f.Close()
	g, err := Lock(path)
	if err != nil {
		t.Fatalf("Lock after the holder closed it: %v", err)
	}
	g.Close()

	if _, err := Lock(filepath.Join(testkit.ShortTempDir(t), "missing", "x.lock")); err == nil || errors.Is(err, ErrLocked) {
		t.Errorf("Lock in a missing directory = %v, want an error that is not ErrLocked", err)
	}
}

func TestLockIsFreeAfterTheHolderDied(t *testing.T) {
	path := filepath.Join(testkit.ShortTempDir(t), "server.lock")
	cmd, out, _ := startHelper(t, "lockhold", nil, "", path)
	if line, err := out.ReadString('\n'); err != nil || line != "locked\n" {
		t.Fatalf("holder said %q, %v; want locked", line, err)
	}
	if f, err := Lock(path); !errors.Is(err, ErrLocked) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("Lock while another process holds it = %v, want ErrLocked", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	testkit.Eventually(t, 5*time.Second, "the lock is free after the holder was killed", func() bool {
		g, err := Lock(path)
		if err != nil {
			return false
		}
		g.Close()
		return true
	})
}
