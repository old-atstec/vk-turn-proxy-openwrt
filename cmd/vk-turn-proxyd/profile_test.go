package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The profiles land in the status directory, 0600, all three of them.
func TestProfilesAreWrittenNextToTheStatus(t *testing.T) {
	dir := t.TempDir()
	if err := writeProfiles(dir, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cpu.pprof", "mutex.pprof", "block.pprof"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s is %v, want 0600", name, fi.Mode().Perm())
		}
	}
}
