package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// useCacheDir points the dirty cache at a fresh temp dir for one test.
func useCacheDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	old := dirtyCacheDir
	dirtyCacheDir = func() (string, error) { return d, nil }
	t.Cleanup(func() { dirtyCacheDir = old })
	return d
}

// fakeRepo lays down just enough of a .git dir for the fingerprint.
func fakeRepo(t *testing.T) (dir, gitDir string) {
	t.Helper()
	dir = t.TempDir()
	gitDir = filepath.Join(dir, ".git")
	for name, body := range map[string]string{"index": "idx", "HEAD": "ref: refs/heads/main\n", "logs/HEAD": "log\n"} {
		p := filepath.Join(gitDir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, gitDir
}

type fakeGit struct {
	calls     int
	dirty, ok bool
}

func (f *fakeGit) run(string) (bool, bool) { f.calls++; return f.dirty, f.ok }

func TestCachedGitDirty(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)

	t.Run("a clean answer is reused inside cleanTTL and rechecked after", func(t *testing.T) {
		useCacheDir(t)
		dir, _ := fakeRepo(t)
		g := &fakeGit{ok: true}
		cachedGitDirtyAt(dir, now, g.run)
		cachedGitDirtyAt(dir, now.Add(cleanTTL-time.Millisecond), g.run)
		if g.calls != 1 {
			t.Fatalf("git ran %d times inside cleanTTL, want 1", g.calls)
		}
		cachedGitDirtyAt(dir, now.Add(cleanTTL), g.run)
		if g.calls != 2 {
			t.Fatalf("git ran %d times after cleanTTL, want 2", g.calls)
		}
	})

	t.Run("a dirty answer is kept for dirtyTTL", func(t *testing.T) {
		useCacheDir(t)
		dir, _ := fakeRepo(t)
		g := &fakeGit{dirty: true, ok: true}
		cachedGitDirtyAt(dir, now, g.run)
		g.dirty = false
		if !cachedGitDirtyAt(dir, now.Add(dirtyTTL-time.Second), g.run) || g.calls != 1 {
			t.Fatalf("cached dirty not reused: calls = %d", g.calls)
		}
		if cachedGitDirtyAt(dir, now.Add(dirtyTTL), g.run) || g.calls != 2 {
			t.Fatalf("stale dirty not rechecked: calls = %d", g.calls)
		}
	})

	t.Run("an index change invalidates at once", func(t *testing.T) {
		useCacheDir(t)
		dir, gitDir := fakeRepo(t)
		g := &fakeGit{ok: true}
		cachedGitDirtyAt(dir, now, g.run)
		if err := os.WriteFile(filepath.Join(gitDir, "index"), []byte("index grew"), 0o644); err != nil {
			t.Fatal(err)
		}
		g.dirty = true
		if !cachedGitDirtyAt(dir, now.Add(time.Second), g.run) || g.calls != 2 {
			t.Fatalf("index change not seen: calls = %d", g.calls)
		}
	})

	t.Run("a failed git run is not cached", func(t *testing.T) {
		useCacheDir(t)
		dir, _ := fakeRepo(t)
		g := &fakeGit{ok: false}
		cachedGitDirtyAt(dir, now, g.run)
		cachedGitDirtyAt(dir, now, g.run)
		if g.calls != 2 {
			t.Fatalf("git ran %d times, want 2", g.calls)
		}
	})

	t.Run("an unusable cache dir falls back to git every time", func(t *testing.T) {
		dir, _ := fakeRepo(t)
		blocker := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocker, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		old := dirtyCacheDir
		dirtyCacheDir = func() (string, error) { return filepath.Join(blocker, "sub"), nil }
		t.Cleanup(func() { dirtyCacheDir = old })
		g := &fakeGit{dirty: true, ok: true}
		for i := 0; i < 2; i++ {
			if !cachedGitDirtyAt(dir, now, g.run) {
				t.Fatal("lost the dirty answer")
			}
		}
		if g.calls != 2 {
			t.Fatalf("calls = %d, want 2", g.calls)
		}
	})

	t.Run("a corrupt cache file is ignored", func(t *testing.T) {
		useCacheDir(t)
		dir, gitDir := fakeRepo(t)
		if err := os.WriteFile(dirtyCachePath(gitDir), []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		g := &fakeGit{dirty: true, ok: true}
		if !cachedGitDirtyAt(dir, now, g.run) || g.calls != 1 {
			t.Fatalf("calls = %d", g.calls)
		}
	})
}

// TestCachedGitDirtySeesStaging runs real git: a clean answer is cached, then
// `git add` of an edit must show as dirty on the very next render, because
// staging rewrites the index.
func TestCachedGitDirtySeesStaging(t *testing.T) {
	useCacheDir(t)
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

	if cachedGitDirty(dir) {
		t.Fatal("clean tree reported dirty")
	}
	if err := os.WriteFile(file, []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	if !cachedGitDirty(dir) {
		t.Fatal("staged change hidden by the cache")
	}
}
