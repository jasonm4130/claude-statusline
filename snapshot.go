package main

import (
	"bytes"
	"os"
	"path/filepath"
)

// saveSnapshot persists the raw stdin payload to $CLAUDE_STATUSLINE_SNAPSHOT
// when it carries rate_limits. Failures are deliberately silent.
func saveSnapshot(data []byte) {
	path := os.Getenv("CLAUDE_STATUSLINE_SNAPSHOT")
	if path == "" || !bytes.Contains(data, []byte(`"rate_limits"`)) {
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	f, err := os.CreateTemp(dir, ".claude-statusline.*")
	if err != nil {
		return
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil || cerr != nil || os.Chmod(tmp, 0o600) != nil || os.Rename(tmp, path) != nil {
		_ = os.Remove(tmp)
	}
}
