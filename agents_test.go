package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeAt(t *testing.T, path, body string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestLastTurnCountsParallelAgents(t *testing.T) {
	base, _ := time.Parse(time.RFC3339, "2026-09-07T01:00:00Z")
	main := turnLine("2026-09-07T01:05:00Z", "claude-opus-5", "high", "claude-opus-5", "")
	sub := func(ts, agent string) string {
		return turnLine(ts, "claude-sonnet-5", "medium", "claude-opus-5", agent)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	root := filepath.Join(dir, "s", "subagents")
	writeAt(t, path, main, base.Add(5*time.Minute))
	// Finished before the main agent last spoke: an earlier, sequential run.
	writeAt(t, filepath.Join(root, "agent-old.jsonl"), sub("2026-09-07T01:01:00Z", "Plan"), base.Add(time.Minute))
	// Three agents dispatched by that main turn, one of them a workflow agent.
	writeAt(t, filepath.Join(root, "agent-a.jsonl"), sub("2026-09-07T01:06:00Z", "Explore"), base.Add(6*time.Minute))
	writeAt(t, filepath.Join(root, "agent-b.jsonl"), sub("2026-09-07T01:08:00Z", "Review"), base.Add(8*time.Minute))
	writeAt(t, filepath.Join(root, "workflows", "wf_1", "agent-c.jsonl"), sub("2026-09-07T01:07:00Z", "verify"), base.Add(7*time.Minute))

	got, ok := lastTurn(path, []byte(main))
	if !ok || got.agent != "Review" || got.agents != 3 {
		t.Fatalf("turn = %+v, ok = %v; want Review with 3 agents", got, ok)
	}
}

func TestWithAgentCount(t *testing.T) {
	seg := segment{text: "⤷ explore·sonnet·med"}
	cases := []struct {
		name string
		t    turn
		have bool
		want string
	}{
		{"fan-out", turn{agent: "Explore", agents: 3}, true, "⤷ explore·sonnet·med ×3"},
		{"lone agent says nothing extra", turn{agent: "Explore", agents: 1}, true, "⤷ explore·sonnet·med"},
		{"main agent turn never carries a count", turn{agents: 3}, true, "⤷ explore·sonnet·med"},
		{"no turn", turn{}, false, "⤷ explore·sonnet·med"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := withAgentCount(seg, c.t, c.have).text; got != c.want {
				t.Errorf("text = %q, want %q", got, c.want)
			}
		})
	}
}
