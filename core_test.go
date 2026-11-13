package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitBranchKeepsSlashes(t *testing.T) {
	for head, want := range map[string]string{
		"ref: refs/heads/fix/foo\n":      "fix/foo",
		"ref: refs/heads/main\n":         "main",
		"ref: refs/remotes/origin/x\n":   "refs/remotes/origin/x",
		"0123456789abcdef0123456789ab\n": "0123456",
	} {
		dir := t.TempDir()
		_ = os.Mkdir(filepath.Join(dir, ".git"), 0o755)
		_ = os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte(head), 0o644)
		if got, ok := gitBranch(dir); !ok || got != want {
			t.Errorf("HEAD %q: got %q, %v; want %q", head, got, ok, want)
		}
	}
}

func TestModelSegmentBlankDisplayName(t *testing.T) {
	p := &payload{}
	p.Model.DisplayName = "   "
	if _, ok := modelSegment(p, turn{}, false); ok {
		t.Error("whitespace-only display name should yield no segment")
	}
}

func BenchmarkRender(b *testing.B) {
	dir := b.TempDir() // not a git repo
	tp := filepath.Join(dir, "t.jsonl")
	var body []byte
	for i := 0; i < 400; i++ {
		body = append(body, line("2026-08-27T11:00:00Z", 964, 0)...)
		body = append(body, '\n')
	}
	if err := os.WriteFile(tp, body, 0o600); err != nil {
		b.Fatal(err)
	}
	in := []byte(`{"cwd":"` + dir + `","workspace":{"current_dir":"` + dir + `"},"transcript_path":"` + tp +
		`","model":{"id":"claude-opus-4-1","display_name":"Opus 4.1"},"context_window":{"context_window_size":200000,"used_percentage":40},` +
		`"rate_limits":{"five_hour":{"used_percentage":30,"resets_at":4102444800},"seven_day":{"used_percentage":20,"resets_at":4102444800}}}`)
	pf := filepath.Join(dir, "in.json")
	if err := os.WriteFile(pf, in, 0o600); err != nil {
		b.Fatal(err)
	}
	old := os.Stdin
	defer func() { os.Stdin = old }()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f, err := os.Open(pf)
		if err != nil {
			b.Fatal(err)
		}
		os.Stdin = f
		_ = render()
		_ = f.Close()
	}
}
