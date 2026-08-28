package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
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
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	if err := os.WriteFile(path, []byte(line("2026-08-27T11:00:00Z", 964, 0)), 0o644); err != nil {
		t.Fatal(err)
	}
	at, _ := time.Parse(time.RFC3339, "2026-08-27T11:00:00Z")
	expiry := at.Add(time.Hour)

	t.Run("before expiry shows the absolute clock time", func(t *testing.T) {
		s, ok := cacheSegment(&payload{TranscriptPath: path}, expiry.Add(-30*time.Minute))
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
		s, ok := cacheSegment(&payload{TranscriptPath: path}, expiry)
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
		s, ok := cacheSegment(&payload{TranscriptPath: path}, expiry.Add(90*time.Minute))
		if !ok {
			t.Fatal("segment absent")
		}
		if s.text != "cache cold" {
			t.Errorf("text = %q, want %q", s.text, "cache cold")
		}
	})

	t.Run("no transcript path yields no segment", func(t *testing.T) {
		if _, ok := cacheSegment(&payload{}, time.Now()); ok {
			t.Error("segment present without a transcript path")
		}
	})

	t.Run("unreadable transcript yields no segment", func(t *testing.T) {
		p := &payload{TranscriptPath: filepath.Join(dir, "missing.jsonl")}
		if _, ok := cacheSegment(p, time.Now()); ok {
			t.Error("segment present for a missing transcript")
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

func TestLimitsSegment(t *testing.T) {
	now := time.Unix(1787912597, 0)
	mk := func(five, seven *window) *payload {
		p := &payload{}
		p.RateLimits = &struct {
			FiveHour *window `json:"five_hour"`
			SevenDay *window `json:"seven_day"`
		}{five, seven}
		return p
	}

	t.Run("both windows with pace", func(t *testing.T) {
		s, ok := limitsSegment(mk(&window{13, 1787923200}, &window{23, 1788033600}), now)
		if !ok {
			t.Fatal("no segment")
		}
		want := "7d 23%→29% Sun 5h 13%→32%"
		if s.text != want {
			t.Errorf("text = %q, want %q", s.text, want)
		}
		if s.state != stateNormal {
			t.Errorf("state = %v, want normal", s.state)
		}
	})

	t.Run("projection past the cap warms", func(t *testing.T) {
		s, _ := limitsSegment(mk(&window{60, 1787923200}, nil), now)
		if s.state != stateWarming {
			t.Errorf("state = %v, want warming", s.state)
		}
	})

	t.Run("high usage stays critical regardless of pace", func(t *testing.T) {
		s, _ := limitsSegment(mk(&window{90, 1787923200}, nil), now)
		if s.state != stateCritical {
			t.Errorf("state = %v, want critical", s.state)
		}
	})

	t.Run("no reset time falls back to bare usage", func(t *testing.T) {
		s, _ := limitsSegment(mk(&window{13, 0}, nil), now)
		if s.text != "5h 13%" {
			t.Errorf("text = %q, want %q", s.text, "5h 13%")
		}
	})

	t.Run("absent rate limits", func(t *testing.T) {
		if _, ok := limitsSegment(&payload{}, now); ok {
			t.Error("expected no segment")
		}
	})
}
