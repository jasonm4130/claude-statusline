package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveSnapshot(t *testing.T) {
	raw := []byte(`{"rate_limits":{"x":1},  "a":1}`)
	path := filepath.Join(t.TempDir(), "sub", "snap.json")
	t.Setenv("CLAUDE_STATUSLINE_SNAPSHOT", path)

	saveSnapshot(raw)
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("snapshot = %q, %v; want raw bytes", got, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("file perm = %v, want 0600", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Dir(path)); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir perm = %v, want 0700", fi.Mode().Perm())
	}
	if ents, _ := os.ReadDir(filepath.Dir(path)); len(ents) != 1 {
		t.Errorf("leftover temp files: %d entries", len(ents))
	}

	saveSnapshot([]byte(`{"model":{}}`))
	got, _ = os.ReadFile(path)
	if string(got) != string(raw) {
		t.Errorf("payload without rate_limits overwrote snapshot: %q", got)
	}
}

func TestSaveSnapshotUnset(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_STATUSLINE_SNAPSHOT", "")
	saveSnapshot([]byte(`{"rate_limits":{}}`))
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("wrote with env unset")
	}
}

func TestSaveSnapshotSkipsWhenAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snap.json")
	t.Setenv("CLAUDE_STATUSLINE_SNAPSHOT", path)
	saveSnapshot([]byte(`{"model":{}}`))
	if _, err := os.Stat(path); err == nil {
		t.Error("snapshot created for payload without rate_limits")
	}
}

func TestSaveSnapshotErrorSilent(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(f, nil, 0o600)
	t.Setenv("CLAUDE_STATUSLINE_SNAPSHOT", filepath.Join(f, "snap.json"))
	saveSnapshot([]byte(`{"rate_limits":{}}`)) // parent is a file; must not panic
}

func TestSaveSnapshotReplacesAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snap.json")
	t.Setenv("CLAUDE_STATUSLINE_SNAPSHOT", path)
	saveSnapshot([]byte(`{"rate_limits":1}`))
	before, _ := os.Stat(path)
	saveSnapshot([]byte(`{"rate_limits":2}`))
	after, _ := os.Stat(path)
	if os.SameFile(before, after) {
		t.Error("snapshot rewritten in place; want rename of a new file")
	}
	if b, _ := os.ReadFile(path); string(b) != `{"rate_limits":2}` {
		t.Errorf("got %q", b)
	}
}
