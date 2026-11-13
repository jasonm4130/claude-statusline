package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Dirty-check cache.
//
// With statusLine.refreshInterval set, the line re-renders on a timer as well
// as on events, and `git status` is most of each render's cost (about 10 ms of
// 14.5). The answer rarely changes between renders, so it is cached per repo in
// a small state file and reused while the repo's git metadata looks unchanged.
//
// The fingerprint is the size and modification time of the index, HEAD and the
// HEAD reflog. Staging, committing, checking out, resetting, stashing and
// restoring a file all rewrite at least one of them. Our own git call cannot
// disturb it, because --no-optional-locks keeps git from refreshing the index.
//
// What the fingerprint cannot see is a plain edit to a tracked file: that
// touches only the working tree. The TTL covers it, and it is asymmetric on
// purpose. Clean-to-dirty happens through exactly that kind of edit, so a
// clean answer is trusted for only a few seconds. Dirty-to-clean almost always
// goes through git, which moves the fingerprint, so a dirty answer can be kept
// longer; the miss is an edit undone by hand, and it shows `*` for at most
// dirtyTTL. dirtyTTL has to outlast a refreshInterval tick to help at all: at
// 2 minutes a dirty repo skips git on three ticks in four at 30 s. A clean
// repo still runs git on each tick more than cleanTTL apart, which is the
// price of seeing a first edit within seconds.
//
// Every failure falls through to running git, and only an answer git actually
// gave is cached, never a timeout.
const (
	cleanTTL = 5 * time.Second
	dirtyTTL = 2 * time.Minute
)

// dirtyCacheDir is a variable so tests can point it at a temp dir.
var dirtyCacheDir = func() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "claude-statusline"), nil
}

type dirtyEntry struct {
	GitDir      string `json:"git_dir"`
	Fingerprint string `json:"fingerprint"`
	CheckedAt   int64  `json:"checked_at"` // unix nanoseconds
	Dirty       bool   `json:"dirty"`
}

func cachedGitDirty(dir string) bool {
	return cachedGitDirtyAt(dir, time.Now(), gitStatusDirty)
}

func cachedGitDirtyAt(dir string, now time.Time, run func(string) (bool, bool)) bool {
	gitDir, ok := findGitDir(dir)
	if !ok {
		d, _ := run(dir)
		return d
	}
	fp := gitFingerprint(gitDir)
	path := dirtyCachePath(gitDir)
	if path != "" && fp != "" {
		if e, ok := readDirtyEntry(path); ok && e.GitDir == gitDir && e.Fingerprint == fp {
			ttl := cleanTTL
			if e.Dirty {
				ttl = dirtyTTL
			}
			age := now.Sub(time.Unix(0, e.CheckedAt))
			if age >= 0 && age < ttl {
				return e.Dirty
			}
		}
	}
	d, ok := run(dir)
	if ok && path != "" && fp != "" {
		writeDirtyEntry(path, dirtyEntry{gitDir, fp, now.UnixNano(), d})
	}
	return d
}

// gitFingerprint returns "" when the index cannot be read, which disables the
// cache for that render; HEAD and the reflog are optional parts.
func gitFingerprint(gitDir string) string {
	fp := ""
	for i, name := range []string{"index", "HEAD", filepath.Join("logs", "HEAD")} {
		fi, err := os.Stat(filepath.Join(gitDir, name))
		if err != nil {
			if i == 0 {
				return ""
			}
			fp += "-;"
			continue
		}
		fp += strconv.FormatInt(fi.Size(), 10) + ":" + strconv.FormatInt(fi.ModTime().UnixNano(), 10) + ";"
	}
	return fp
}

func dirtyCachePath(gitDir string) string {
	base, err := dirtyCacheDir()
	if err != nil || base == "" {
		return ""
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(gitDir))
	return filepath.Join(base, fmt.Sprintf("dirty-%016x.json", h.Sum64()))
}

func readDirtyEntry(path string) (dirtyEntry, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return dirtyEntry{}, false
	}
	var e dirtyEntry
	if json.Unmarshal(b, &e) != nil {
		return dirtyEntry{}, false
	}
	return e, true
}

// writeDirtyEntry writes through a temp file and a rename, so a concurrent
// render in another session reads either the old entry or the new one.
func writeDirtyEntry(path string, e dirtyEntry) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	f, err := os.CreateTemp(dir, ".dirty-*")
	if err != nil {
		return
	}
	tmp := f.Name()
	_, werr := f.Write(b)
	cerr := f.Close()
	if werr != nil || cerr != nil || os.Rename(tmp, path) != nil {
		_ = os.Remove(tmp)
	}
}
