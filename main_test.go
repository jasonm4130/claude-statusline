package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// line builds one transcript JSONL record. tier1h/tier5m are the ephemeral
// token counts; both zero models a request that only READ cache and wrote no
// new prefix, which reports no tier of its own.
func line(ts string, tier1h, tier5m int) string {
	return fmt.Sprintf(
		`{"timestamp":%q,"message":{"usage":{"input_tokens":2,"cache_read_input_tokens":207756,`+
			`"cache_creation":{"ephemeral_1h_input_tokens":%d,"ephemeral_5m_input_tokens":%d}}}}`,
		ts, tier1h, tier5m)
}

func TestParseLastUsage(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantTTL time.Duration
		wantAt  string
		wantOK  bool
	}{
		{
			name:    "1h tier",
			body:    line("2026-08-27T11:00:00Z", 964, 0),
			wantTTL: time.Hour,
			wantAt:  "2026-08-27T11:00:00Z",
			wantOK:  true,
		},
		{
			name:    "5m tier",
			body:    line("2026-08-27T11:00:00Z", 0, 512),
			wantTTL: 5 * time.Minute,
			wantAt:  "2026-08-27T11:00:00Z",
			wantOK:  true,
		},
		{
			// The clock must come from the NEWEST request (a cache read
			// refreshes the entry's TTL) while the tier comes from the newest
			// request that actually wrote one.
			name: "newest is a pure read, tier from an older line",
			body: strings.Join([]string{
				line("2026-08-27T11:00:00Z", 964, 0),
				line("2026-08-27T11:30:00Z", 0, 0),
			}, "\n"),
			wantTTL: time.Hour,
			wantAt:  "2026-08-27T11:30:00Z",
			wantOK:  true,
		},
		{
			name: "newest tier wins over an older, different tier",
			body: strings.Join([]string{
				line("2026-08-27T10:00:00Z", 0, 512),
				line("2026-08-27T11:00:00Z", 964, 0),
			}, "\n"),
			wantTTL: time.Hour,
			wantAt:  "2026-08-27T11:00:00Z",
			wantOK:  true,
		},
		{
			name: "non-usage and malformed lines are skipped",
			body: strings.Join([]string{
				`{"type":"summary","summary":"whatever"}`,
				`{"timestamp":"broken`,
				line("2026-08-27T11:00:00Z", 964, 0),
				`not json at all`,
			}, "\n"),
			wantTTL: time.Hour,
			wantAt:  "2026-08-27T11:00:00Z",
			wantOK:  true,
		},
		{
			name:   "no usage records",
			body:   `{"type":"summary","summary":"whatever"}`,
			wantOK: false,
		},
		{
			name:   "usage present but no tier anywhere",
			body:   line("2026-08-27T11:00:00Z", 0, 0),
			wantOK: false,
		},
		{
			name:   "empty",
			body:   "",
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseLastUsage([]byte(tc.body))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if got.ttl != tc.wantTTL {
				t.Errorf("ttl = %v, want %v", got.ttl, tc.wantTTL)
			}
			want, err := time.Parse(time.RFC3339, tc.wantAt)
			if err != nil {
				t.Fatalf("bad fixture: %v", err)
			}
			if !got.at.Equal(want) {
				t.Errorf("at = %v, want %v", got.at, want)
			}
		})
	}
}

func TestReadTailDropsPartialFirstLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")

	// Two whole lines behind a filler line long enough to force truncation.
	filler := `{"pad":"` + strings.Repeat("x", 4096) + `"}`
	body := strings.Join([]string{
		filler,
		line("2026-08-27T11:00:00Z", 964, 0),
		line("2026-08-27T11:30:00Z", 0, 0),
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	tail, ok := readTail(path, 2048)
	if !ok {
		t.Fatal("readTail returned false")
	}
	if strings.Contains(string(tail), "xxxx") {
		t.Error("partial first line was not dropped")
	}
	for _, l := range strings.Split(strings.TrimSpace(string(tail)), "\n") {
		if !strings.HasPrefix(l, "{") || !strings.HasSuffix(l, "}") {
			t.Errorf("tail contains a non-whole line: %.60q", l)
		}
	}
}

func TestReadTailMissingFile(t *testing.T) {
	if _, ok := readTail(filepath.Join(t.TempDir(), "nope.jsonl"), 1024); ok {
		t.Error("readTail on a missing file returned true")
	}
}

func TestCacheSegment(t *testing.T) {
	tail := []byte(line("2026-08-27T11:00:00Z", 964, 0))
	at, _ := time.Parse(time.RFC3339, "2026-08-27T11:00:00Z")
	expiry := at.Add(time.Hour)

	t.Run("before expiry shows the absolute clock time", func(t *testing.T) {
		s, ok := cacheSegment(tail, expiry.Add(-30*time.Minute))
		if !ok {
			t.Fatal("segment absent")
		}
		want := "cache→" + expiry.Local().Format("15:04")
		if s.text != want {
			t.Errorf("text = %q, want %q", s.text, want)
		}
		if s.state != stateNormal {
			t.Errorf("state = %v, want stateNormal", s.state)
		}
	})

	t.Run("at expiry is already cold", func(t *testing.T) {
		s, ok := cacheSegment(tail, expiry)
		if !ok {
			t.Fatal("segment absent")
		}
		if s.text != "cache cold" {
			t.Errorf("text = %q, want %q", s.text, "cache cold")
		}
		if s.state != stateWarming {
			t.Errorf("state = %v, want stateWarming", s.state)
		}
	})

	t.Run("past expiry is cold", func(t *testing.T) {
		s, ok := cacheSegment(tail, expiry.Add(90*time.Minute))
		if !ok {
			t.Fatal("segment absent")
		}
		if s.text != "cache cold" {
			t.Errorf("text = %q, want %q", s.text, "cache cold")
		}
	})

	t.Run("no transcript yields no segment", func(t *testing.T) {
		if _, ok := cacheSegment(nil, time.Now()); ok {
			t.Error("segment present without a transcript")
		}
	})
}

func TestProjected(t *testing.T) {
	now := time.Unix(1787912597, 0) // 2026-08-28 20:23 AEST

	cases := []struct {
		name   string
		w      window
		length time.Duration
		want   float64
		ok     bool
	}{
		// 5h window resetting at 23:20 started at 18:20: 41% elapsed, 13% used.
		{"five hour ahead", window{13, 1787923200}, fiveHourLen, 31.6, true},
		// 7d window resetting Sun 06:00 started the previous Sun: 80% elapsed.
		{"seven day ahead", window{23, 1788033600}, sevenDayLen, 28.8, true},
		// Same elapsed fraction, burn that lands past the cap.
		{"five hour over cap", window{60, 1787923200}, fiveHourLen, 146.0, true},
		{"no reset time", window{13, 0}, fiveHourLen, 0, false},
		// Window already reset: resets_at in the past, so the data is stale.
		{"expired window", window{13, 1787900000}, fiveHourLen, 0, false},
		// Only three minutes into a five-hour window.
		{"below pace floor", window{2, 1787930000}, fiveHourLen, 0, false},
		{"clamped", window{100, 1787929697}, fiveHourLen, 999, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := projected(&c.w, c.length, now)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if ok && math.Abs(got-c.want) > 0.1 {
				t.Errorf("projected = %.1f, want %.1f", got, c.want)
			}
		})
	}
}

// pinZone fixes the local zone for the duration of a test. Two segments render
// in local time -- the cache clock and the weekly reset day -- so without this a
// test asserting on either passes in Brisbane and fails on a UTC CI runner.
func pinZone(t *testing.T) {
	t.Helper()
	prev := time.Local
	time.Local = time.FixedZone("AEST", 10*60*60)
	t.Cleanup(func() { time.Local = prev })
}

func TestLimitSegments(t *testing.T) {
	pinZone(t)
	now := time.Unix(1787912597, 0)
	mk := func(five, seven *window) *payload {
		p := &payload{}
		p.RateLimits = &struct {
			FiveHour *window `json:"five_hour"`
			SevenDay *window `json:"seven_day"`
		}{five, seven}
		return p
	}
	texts := func(segs []segment) []string {
		out := make([]string, len(segs))
		for i, s := range segs {
			out[i] = s.text
		}
		return out
	}

	t.Run("one segment per window, weekly first", func(t *testing.T) {
		segs := limitSegments(mk(&window{13, 1787923200}, &window{23, 1788033600}), now)
		want := []string{"7d 23%→29% Sun", "5h 13%→32%"}
		if got := texts(segs); !slices.Equal(got, want) {
			t.Errorf("texts = %q, want %q", got, want)
		}
		for i, s := range segs {
			if s.state != stateNormal {
				t.Errorf("segment %d state = %v, want normal", i, s.state)
			}
		}
	})

	t.Run("a hot window does not repaint a healthy one", func(t *testing.T) {
		segs := limitSegments(mk(&window{72, 1787923200}, &window{23, 1788033600}), now)
		if len(segs) != 2 {
			t.Fatalf("got %d segments, want 2", len(segs))
		}
		if segs[0].state != stateNormal {
			t.Errorf("7d state = %v, want normal", segs[0].state)
		}
		if segs[1].state != stateWarming {
			t.Errorf("5h state = %v, want warming", segs[1].state)
		}
		if !strings.HasPrefix(segs[1].text, overPace) {
			t.Errorf("5h text = %q, want the over-pace glyph", segs[1].text)
		}
		if strings.Contains(segs[0].text, overPace) {
			t.Errorf("7d text = %q, should not be marked over pace", segs[0].text)
		}
	})

	t.Run("high usage is critical even when on pace", func(t *testing.T) {
		// 25 minutes from reset: 92% elapsed against 90% used projects to 98%,
		// so this window really is on pace and the criticality comes from the
		// usage alone.
		segs := limitSegments(mk(&window{90, 1787914097}, nil), now)
		if segs[0].state != stateCritical {
			t.Errorf("state = %v, want critical", segs[0].state)
		}
		if strings.Contains(segs[0].text, overPace) {
			t.Fatalf("text = %q is over pace, so this case is not testing what it says", segs[0].text)
		}
	})

	t.Run("no reset time falls back to bare usage", func(t *testing.T) {
		segs := limitSegments(mk(&window{13, 0}, nil), now)
		if got := texts(segs); !slices.Equal(got, []string{"5h 13%"}) {
			t.Errorf("texts = %q", got)
		}
	})

	t.Run("absent rate limits", func(t *testing.T) {
		if segs := limitSegments(&payload{}, now); len(segs) != 0 {
			t.Errorf("got %d segments, want 0", len(segs))
		}
	})
}

// turnLine builds one assistant transcript record. An empty agent models the
// main agent, whose records carry no attributionAgent key at all.
func turnLine(ts, model, effort, advisor, agent string) string {
	attr := ""
	if agent != "" {
		attr = fmt.Sprintf(`"attributionAgent":%q,`, agent)
	}
	return fmt.Sprintf(
		`{"type":"assistant","timestamp":%q,"effort":%q,"advisorModel":%q,%s`+
			`"message":{"role":"assistant","model":%q}}`,
		ts, effort, advisor, attr, model)
}

func TestParseLastTurn(t *testing.T) {
	t.Run("newest assistant record wins", func(t *testing.T) {
		body := strings.Join([]string{
			turnLine("2026-09-07T01:00:00Z", "claude-opus-5", "high", "claude-opus-5", ""),
			`{"type":"user","timestamp":"2026-09-07T01:05:00Z","message":{"role":"user"}}`,
			turnLine("2026-09-07T01:07:00Z", "claude-sonnet-5", "medium", "claude-opus-5", "Explore"),
		}, "\n")
		got, ok := parseLastTurn([]byte(body))
		if !ok {
			t.Fatal("no turn found")
		}
		if got.agent != "Explore" || got.model != "claude-sonnet-5" || got.effort != "medium" {
			t.Errorf("turn = %+v", got)
		}
		if got.advisor != "claude-opus-5" {
			t.Errorf("advisor = %q", got.advisor)
		}
		want, _ := time.Parse(time.RFC3339, "2026-09-07T01:07:00Z")
		if !got.at.Equal(want) {
			t.Errorf("at = %v, want %v", got.at, want)
		}
	})

	t.Run("malformed and modelless records are skipped", func(t *testing.T) {
		body := strings.Join([]string{
			turnLine("2026-09-07T01:00:00Z", "claude-opus-5", "high", "", ""),
			`{"type":"assistant","timestamp":"2026-09-07T01:02:00Z","message":{"role":"assistant"}}`,
			`{"type":"assistant","timestamp":"broken`,
		}, "\n")
		got, ok := parseLastTurn([]byte(body))
		if !ok || got.model != "claude-opus-5" {
			t.Errorf("turn = %+v, ok = %v", got, ok)
		}
	})

	t.Run("no assistant record", func(t *testing.T) {
		if _, ok := parseLastTurn([]byte(`{"type":"user","message":{"role":"user"}}`)); ok {
			t.Error("found a turn where there is none")
		}
	})
}

func TestShortModel(t *testing.T) {
	cases := map[string]string{
		"claude-sonnet-5":           "sonnet",
		"claude-opus-5":             "opus",
		"claude-opus-5[1m]":         "opus",
		"claude-haiku-4-5-20251001": "haiku",
		"sonnet":                    "sonnet",
	}
	for in, want := range cases {
		if got := shortModel(in); got != want {
			t.Errorf("shortModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestModelSegment(t *testing.T) {
	mk := func(display, effort string) *payload {
		p := &payload{}
		p.Model.DisplayName = display
		p.Effort.Level = effort
		return p
	}
	at, _ := time.Parse(time.RFC3339, "2026-09-07T01:07:00Z")

	cases := []struct {
		name     string
		p        *payload
		t        turn
		haveTurn bool
		want     string
		wantOK   bool
	}{
		{
			name: "main agent falls back to the payload",
			p:    mk("Opus 5", "high"), want: "opus·high", wantOK: true,
		},
		{
			name:     "advisor stays hidden when it matches the working model",
			p:        mk("Opus 5", "high"),
			t:        turn{at: at, model: "claude-opus-5", effort: "high", advisor: "claude-opus-5"},
			haveTurn: true, want: "opus·high", wantOK: true,
		},
		{
			name: "a subagent turn names the agent and its own model",
			p:    mk("Opus 5", "high"),
			t: turn{at: at, agent: "Explore", model: "claude-sonnet-5", effort: "medium",
				advisor: "claude-opus-5"},
			haveTurn: true, want: "⤷ explore·sonnet·med +adv opus", wantOK: true,
		},
		{
			name: "a subagent on the session model shows no advisor",
			p:    mk("Opus 5", "high"),
			t: turn{at: at, agent: "workhorse", model: "claude-opus-5", effort: "max",
				advisor: "claude-opus-5"},
			haveTurn: true, want: "⤷ workhorse·opus·max", wantOK: true,
		},
		{
			name: "a long agent name is bounded",
			p:    mk("Opus 5", "high"),
			t: turn{at: at, agent: "verify:app/services/letters/send.rb", model: "claude-sonnet-5",
				effort: "low", advisor: "claude-sonnet-5"},
			haveTurn: true, want: "⤷ verify:app/s…·sonnet·low", wantOK: true,
		},
		{
			name: "no model anywhere yields no segment",
			p:    mk("", ""), wantOK: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := modelSegment(c.p, c.t, c.haveTurn)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && got.text != c.want {
				t.Errorf("text = %q, want %q", got.text, c.want)
			}
		})
	}
}

func TestLastTurn(t *testing.T) {
	// write lays a transcript down with a fixed modification time, so the file
	// order the code sees does not depend on how fast the test runs.
	write := func(t *testing.T, path, body string, mtime time.Time) {
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
	base, _ := time.Parse(time.RFC3339, "2026-09-07T01:00:00Z")
	main := turnLine("2026-09-07T01:05:00Z", "claude-opus-5", "high", "claude-opus-5", "")

	t.Run("a fresher subagent turn wins", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "s.jsonl")
		write(t, path, main, base.Add(5*time.Minute))
		write(t, filepath.Join(dir, "s", "subagents", "agent-abc.jsonl"),
			turnLine("2026-09-07T01:07:00Z", "claude-sonnet-5", "medium", "claude-opus-5", "Explore"),
			base.Add(7*time.Minute))

		got, ok := lastTurn(path, []byte(main))
		if !ok || got.agent != "Explore" {
			t.Fatalf("turn = %+v, ok = %v", got, ok)
		}
	})

	t.Run("a finished subagent loses to a newer main turn", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "s.jsonl")
		write(t, path, main, base.Add(5*time.Minute))
		write(t, filepath.Join(dir, "s", "subagents", "agent-abc.jsonl"),
			turnLine("2026-09-07T01:02:00Z", "claude-sonnet-5", "medium", "claude-opus-5", "Explore"),
			base.Add(2*time.Minute))

		got, ok := lastTurn(path, []byte(main))
		if !ok || got.agent != "" || got.model != "claude-opus-5" {
			t.Fatalf("turn = %+v, ok = %v", got, ok)
		}
	})

	t.Run("workflow agents nest one level deeper and still count", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "s.jsonl")
		write(t, path, main, base.Add(5*time.Minute))
		write(t, filepath.Join(dir, "s", "subagents", "workflows", "wf_1", "agent-abc.jsonl"),
			turnLine("2026-09-07T01:09:00Z", "claude-haiku-4-5-20251001", "low", "claude-opus-5", "review:bugs"),
			base.Add(9*time.Minute))

		got, ok := lastTurn(path, []byte(main))
		if !ok || got.agent != "review:bugs" || got.model != "claude-haiku-4-5-20251001" {
			t.Fatalf("turn = %+v, ok = %v", got, ok)
		}
	})

	t.Run("no transcript path", func(t *testing.T) {
		if _, ok := lastTurn("", nil); ok {
			t.Error("found a turn without a transcript")
		}
	})

	t.Run("an unreadable main turn suppresses the subagent lookup", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "s.jsonl")
		write(t, path, "", base)
		write(t, filepath.Join(dir, "s", "subagents", "agent-abc.jsonl"),
			turnLine("2026-09-07T01:07:00Z", "claude-sonnet-5", "medium", "claude-opus-5", "Explore"),
			base.Add(7*time.Minute))

		if got, ok := lastTurn(path, nil); ok {
			t.Errorf("turn = %+v, want none", got)
		}
	})
}
