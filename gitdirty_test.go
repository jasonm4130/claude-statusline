package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestGitDirtyLeavesIndexUntouched(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-qm", "init")

	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(dir, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}

	if gitDirty(dir) {
		t.Fatal("clean tree reported dirty")
	}

	after, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("gitDirty rewrote .git/index; it must not take the index lock")
	}
}
