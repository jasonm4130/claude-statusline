package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	d, err := os.MkdirTemp("", "statusline-test-")
	if err != nil {
		panic(err)
	}
	dirtyCacheDir = func() (string, error) { return d, nil }
	code := m.Run()
	_ = os.RemoveAll(d)
	os.Exit(code)
}

// paceEnv points the state dir at a fresh temp dir for one test.
func paceEnv(t *testing.T) string {
	d := t.TempDir()
	old := dirtyCacheDir
	dirtyCacheDir = func() (string, error) { return d, nil }
	t.Cleanup(func() { dirtyCacheDir = old })
	return d
}

var paceT0 = time.Date(2026, 8, 27, 9, 0, 0, 0, time.Local)

// step records a render at t0+d with the given windows and returns the segment texts.
func step(t *testing.T, d time.Duration, five, seven *window) string {
	t.Helper()
	now := paceT0.Add(d)
	p := &payload{}
	p.RateLimits = &struct {
		FiveHour *window `json:"five_hour"`
		SevenDay *window `json:"seven_day"`
	}{five, seven}
	segs := limitSegmentsWith(p, now, trackPaceFor(p, now))
	var out []string
	for _, s := range segs {
		out = append(out, s.text)
	}
	return strings.Join(out, " | ")
}

func w5(used float64) *window {
	return &window{used, paceT0.Add(5 * time.Hour).Unix()}
}

func TestPaceEarlyBurstThenIdle(t *testing.T) {
	paceEnv(t)
	// 40% in the first 20 minutes, then idle. Cumulative would stay alarming.
	step(t, 0, w5(0), nil)
	step(t, 20*time.Minute, w5(40), nil)
	for m := 25; m <= 100; m += 5 {
		step(t, time.Duration(m)*time.Minute, w5(40), nil)
	}
	got := step(t, 105*time.Minute, w5(40), nil)
	if got != "5h 40%→40%" {
		t.Errorf("got %q", got)
	}
	// the cumulative figure would have been 40/0.35 = 114%
	if c, _ := projected(w5(40), fiveHourLen, paceT0.Add(105*time.Minute)); c < 100 {
		t.Errorf("cumulative should be alarming, got %v", c)
	}
}

func TestPaceIdleThenLateBurst(t *testing.T) {
	paceEnv(t)
	for m := 0; m <= 180; m += 5 {
		step(t, time.Duration(m)*time.Minute, w5(10), nil)
	}
	step(t, 185*time.Minute, w5(20), nil)
	step(t, 195*time.Minute, w5(35), nil)
	got := step(t, 200*time.Minute, w5(42), nil)
	if !strings.HasPrefix(got, "▲ 5h 42% cap ") {
		t.Fatalf("expected cap warning, got %q", got)
	}
	// cumulative projection would say 42/(200/300)=63%: no warning
	if c, _ := projected(w5(42), fiveHourLen, paceT0.Add(200*time.Minute)); c >= 100 {
		t.Errorf("cumulative should be calm, got %v", c)
	}
	// 32 points in 15 minutes from 185-> only counts inside the 40 min window.
	if want := paceT0.Add(200 * time.Minute).Format("15:04"); strings.Contains(got, want) {
		t.Errorf("cap time should be later than now: %q", got)
	}
}

func TestPaceAbsentFiveHour(t *testing.T) {
	d := paceEnv(t)
	sd := &window{20, paceT0.Add(48 * time.Hour).Unix()}
	got := step(t, 0, nil, sd)
	if !strings.HasPrefix(got, "7d 20%") {
		t.Errorf("got %q", got)
	}
	b, err := os.ReadFile(filepath.Join(d, paceStateFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"5h"`) {
		t.Errorf("absent window created an entry: %s", b)
	}
	// no windows at all: no file is created
	d2 := paceEnv(t)
	step(t, 0, nil, nil)
	if _, err := os.Stat(filepath.Join(d2, paceStateFile)); err == nil {
		t.Error("state file created with no windows")
	}
}

func TestPaceResetClearsHistory(t *testing.T) {
	paceEnv(t)
	step(t, 0, w5(10), nil)
	step(t, 30*time.Minute, w5(30), nil)
	nw := &window{5, paceT0.Add(10 * time.Hour).Unix()}
	step(t, 60*time.Minute, nw, nil)
	h := trackPace(nw, nil, paceT0.Add(61*time.Minute))
	if n := len(h["5h"]); n != 1 {
		t.Errorf("want 1 sample after reset, got %d", n)
	}
}

func TestPaceUsedDropClearsHistory(t *testing.T) {
	paceEnv(t)
	step(t, 0, w5(10), nil)
	step(t, 30*time.Minute, w5(30), nil)
	h := trackPace(w5(12), nil, paceT0.Add(31*time.Minute))
	if n := len(h["5h"]); n != 1 || h["5h"][0].U != 12 {
		t.Errorf("history not cleared: %+v", h["5h"])
	}
}

func TestPaceFallbackBeforeEnoughHistory(t *testing.T) {
	paceEnv(t)
	step(t, 60*time.Minute, w5(20), nil)
	got := step(t, 65*time.Minute, w5(25), nil) // 5 min of history < 10
	// cumulative: elapsed 65/300, 25/0.2167 = 115% -> arrow form with glyph
	if got != "▲ 5h 25%→115%" {
		t.Errorf("got %q", got)
	}
}

func TestPaceSevenDayNeeds24h(t *testing.T) {
	paceEnv(t)
	reset := paceT0.Add(5 * 24 * time.Hour).Unix()
	mk := func(u float64) *window { return &window{u, reset} }
	step(t, 0, nil, mk(10))
	got := step(t, 20*time.Hour, nil, mk(20)) // < 24h: cumulative with weekday suffix
	if !strings.Contains(got, "→") {
		t.Errorf("got %q", got)
	}
	// 25h later the anchor exists; 10 points/day, 5 days left: ~70%, no cap.
	got = step(t, 25*time.Hour, nil, mk(21))
	if got != "7d 21%→"+"68%"+" "+time.Unix(reset, 0).Local().Format("Mon") && !strings.HasPrefix(got, "7d 21%→") {
		t.Errorf("got %q", got)
	}
}

func TestPaceCapBeforeResetShowsClock(t *testing.T) {
	paceEnv(t)
	step(t, 60*time.Minute, w5(40), nil)
	// 10 points in 20 minutes -> 0.5 %/min; 50 left -> cap in 100 min.
	step(t, 70*time.Minute, w5(45), nil)
	got := step(t, 80*time.Minute, w5(50), nil)
	// rate from the oldest in-window sample (60 min): 10 pts / 20 min
	want := "▲ 5h 50% cap " + paceT0.Add(180*time.Minute).Format("15:04")
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestPaceCapLabelWeekday(t *testing.T) {
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.Local)
	if got := capLabel(now.Add(2*time.Hour), now); got != "11:00" {
		t.Errorf("same day: %q", got)
	}
	if got := capLabel(now.Add(48*time.Hour), now); got != "Sat 09:00" {
		t.Errorf("later: %q", got)
	}
}

func TestPaceCorruptStateFallsBack(t *testing.T) {
	d := paceEnv(t)
	if err := os.WriteFile(filepath.Join(d, paceStateFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := step(t, 60*time.Minute, w5(15), nil)
	if !strings.HasPrefix(got, "5h 15%→75%") {
		t.Errorf("got %q", got)
	}
	// and the file heals
	if _, err := os.ReadFile(filepath.Join(d, paceStateFile)); err != nil {
		t.Error(err)
	}
}

func TestPaceUnwritableStateFallsBack(t *testing.T) {
	d := paceEnv(t)
	// a file where the directory should be
	f := filepath.Join(d, "blocked")
	_ = os.WriteFile(f, nil, 0o600)
	old := dirtyCacheDir
	dirtyCacheDir = func() (string, error) { return filepath.Join(f, "sub"), nil }
	t.Cleanup(func() { dirtyCacheDir = old })
	got := step(t, 60*time.Minute, w5(15), nil)
	if !strings.HasPrefix(got, "5h 15%→75%") {
		t.Errorf("got %q", got)
	}
}

func TestPaceSampleWritePolicy(t *testing.T) {
	d := paceEnv(t)
	step(t, 0, w5(10), nil)
	step(t, 2*time.Minute, w5(10), nil) // unchanged, fresh: no new sample
	h := trackPace(w5(10), nil, paceT0.Add(3*time.Minute))
	if len(h["5h"]) != 1 {
		t.Errorf("want 1 sample, got %d", len(h["5h"]))
	}
	h = trackPace(w5(10), nil, paceT0.Add(7*time.Minute)) // heartbeat
	if len(h["5h"]) != 2 {
		t.Errorf("want 2 samples, got %d", len(h["5h"]))
	}
	_ = d
}
