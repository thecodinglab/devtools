package platform

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildVersionReadsTheFirstLine(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "devtools")
	script := "#!/bin/sh\n[ \"$1\" = version ] || exit 3\nprintf '0.1.0+04c5fef\\nmore\\n'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v, err := BuildVersion(ctx, bin, []string{"PATH=/usr/bin:/bin"})
	if err != nil || v != "0.1.0+04c5fef" {
		t.Fatalf("BuildVersion = %q, %v", v, err)
	}
	if _, err := BuildVersion(ctx, filepath.Join(dir, "missing"), nil); err == nil {
		t.Fatal("a missing build gave no error")
	}
}
