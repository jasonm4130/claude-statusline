package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"
)

// Trailing pace.
//
// The cumulative projection (used / elapsed fraction) averages the whole window,
// so it misleads both ways: an early burst keeps it alarming for hours, and a
// late burst after idle is diluted into a long quiet average and warns late,
// which is the case that actually hits the cap. Here each render records
// (time, used%) per window in one small state file shared by all sessions, and
// the projection uses the rate over a trailing slice of the window instead.
//
// It is a plain two-point slope, not an EWMA: samples are irregular in time and
// used% is quantised to whole percents, so a smoothed estimate would only hide
// that the data is coarse.
//
// Window choice. 5h trails 40 minutes. A one-point step over 40 minutes is a
// resolution of 1.5 %/h, which projects to about +-7.5 points over the 5h
// window: tight enough to be useful, while short enough that a burst dominates
// it within ~15 minutes. 30 minutes doubles that noise; 60 reacts slowly. 7d
// trails 24 hours, a full diurnal cycle, so sleeping does not bias the rate
// downward at night or upward in the morning.
//
// Until the history spans enough time (10 min for 5h, 24h for 7d) the old
// cumulative projection is used.
//
// Frozen renders. The statusline can sit unrefreshed for minutes, so everything
// shown is an absolute fact computed once: a clock time for the cap, or a
// percentage. While idle the true rate only falls (used stays flat as time
// passes), so the true cap moves later and a stale `cap 15:40` is earlier than
// the truth. A frozen reading therefore overstates the burn and errs safe.
const (
	fiveHourTrail = 40 * time.Minute
	sevenDayTrail = 24 * time.Hour
	fiveHourSpan  = 10 * time.Minute
	sevenDaySpan  = 24 * time.Hour

	paceHeartbeat  = 5 * time.Minute // rewrite an unchanged value this often
	paceMaxSamples = 400
	paceStateFile  = "pace.json"
)

type sample struct {
	T int64   `json:"t"` // unix seconds
	U float64 `json:"u"`
}

type paceWindow struct {
	Reset   int64    `json:"reset"`
	Samples []sample `json:"samples"`
}

type paceState map[string]*paceWindow

// paceHist maps a window label ("5h", "7d") to its samples, oldest first.
type paceHist map[string][]sample

func paceTrail(label string) (trail, minSpan time.Duration) {
	if label == "7d" {
		return sevenDayTrail, sevenDaySpan
	}
	return fiveHourTrail, fiveHourSpan
}

func pacePath() string {
	base, err := dirtyCacheDir() // same base dir as the dirty cache
	if err != nil || base == "" {
		return ""
	}
	return filepath.Join(base, paceStateFile)
}

// trackPace records the current sample for each present window and returns the
// history. Absent windows create nothing. Failures return what is known.
func trackPace(fiveHour, sevenDay *window, now time.Time) paceHist {
	if fiveHour == nil && sevenDay == nil {
		return nil
	}
	wins := map[string]*window{"5h": fiveHour, "7d": sevenDay}
	path := pacePath()
	var st paceState
	if path != "" {
		if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &st) != nil {
			st = nil
		}
	}
	if st == nil {
		st = paceState{}
	}
	dirty := false
	hist := paceHist{}
	for label, w := range wins {
		if w == nil || w.ResetsAt <= 0 {
			continue
		}
		pw := st[label]
		// Every session shares this file, but each payload carries the limits
		// from that session's own last API response. With refreshInterval set,
		// an idle session keeps re-rendering an older reading: a lower used% in
		// the same window, or the previous window's resets_at after a reset.
		// Used% never falls within a window and windows only move forward, so
		// both are stale, not news. They are left out of the history rather
		// than clearing it, or concurrent sessions would wipe it on every tick.
		if pw != nil && w.ResetsAt < pw.Reset {
			continue
		}
		if pw == nil || pw.Reset != w.ResetsAt {
			pw = &paceWindow{Reset: w.ResetsAt}
			st[label] = pw
			dirty = true
		}
		n := len(pw.Samples)
		stale := n > 0 && w.UsedPercentage < pw.Samples[n-1].U // keep history, record nothing
		if !stale && (n == 0 || pw.Samples[n-1].U != w.UsedPercentage || now.Sub(time.Unix(pw.Samples[n-1].T, 0)) > paceHeartbeat) {
			pw.Samples = append(pw.Samples, sample{now.Unix(), w.UsedPercentage})
			dirty = true
		}
		trail, _ := paceTrail(label)
		if pruned := pruneSamples(pw.Samples, now.Add(-trail)); len(pruned) != len(pw.Samples) {
			pw.Samples = pruned
			dirty = true
		}
		if len(pw.Samples) > paceMaxSamples {
			pw.Samples = pw.Samples[len(pw.Samples)-paceMaxSamples:]
			dirty = true
		}
		hist[label] = pw.Samples
	}
	// A window that vanishes from the payload keeps its entry; the reset-change
	// rule clears it if it comes back.
	if dirty && path != "" {
		writePace(path, st)
	}
	return hist
}

func trackPaceFor(p *payload, now time.Time) paceHist {
	if p.RateLimits == nil {
		return nil
	}
	return trackPace(p.RateLimits.FiveHour, p.RateLimits.SevenDay, now)
}

// pruneSamples drops samples older than cutoff but keeps the newest one before
// it as an anchor, so a night of idleness does not erase the 24h baseline.
func pruneSamples(s []sample, cutoff time.Time) []sample {
	c := cutoff.Unix()
	keep := 0
	for i, x := range s {
		if x.T < c {
			keep = i
		}
	}
	return s[keep:]
}

// writePace writes through a temp file and a rename; a concurrent session reads
// the old file or the new one, and last writer wins.
func writePace(path string, st paceState) {
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	f, err := os.CreateTemp(dir, ".pace-*")
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

// trailingRate returns used%/second over the trailing window, or false when the
// history is too short. The base is the newest sample at or before the window
// start (the anchor) when it is not absurdly old, else the oldest sample inside.
func trailingRate(label string, hist []sample, used float64, now time.Time) (float64, bool) {
	trail, minSpan := paceTrail(label)
	start := now.Add(-trail).Unix()
	base := -1
	for i, x := range hist {
		if x.T <= start && now.Unix()-x.T <= int64((trail+trail/2)/time.Second) {
			base = i
		}
	}
	if base < 0 {
		for i, x := range hist {
			if x.T > start {
				base = i
				break
			}
		}
	}
	if base < 0 {
		return 0, false
	}
	span := now.Sub(time.Unix(hist[base].T, 0))
	if span < minSpan || span <= 0 {
		return 0, false
	}
	du := used - hist[base].U
	if du < 0 {
		return 0, false
	}
	return du / span.Seconds(), true
}

// trailingProjection returns the projected percent at reset and, when the cap
// lands before the reset, the time it does.
func trailingProjection(label string, w *window, hist []sample, now time.Time) (proj float64, capAt time.Time, hasCap, ok bool) {
	if w == nil || w.ResetsAt <= 0 || w.UsedPercentage >= 100 {
		return 0, time.Time{}, false, false
	}
	reset := time.Unix(w.ResetsAt, 0)
	left := reset.Sub(now)
	if left <= 0 {
		return 0, time.Time{}, false, false
	}
	used := w.UsedPercentage
	if n := len(hist); n > 0 && hist[n-1].U > used {
		used = hist[n-1].U // another session has seen a newer reading
	}
	rate, ok := trailingRate(label, hist, used, now)
	if !ok {
		return 0, time.Time{}, false, false
	}
	proj = math.Min(used+rate*left.Seconds(), 999)
	if proj >= 100 && rate > 0 {
		secs := (100 - used) / rate
		capAt = now.Add(time.Duration(secs * float64(time.Second)))
		if capAt.Before(reset) {
			return proj, capAt, true, true
		}
	}
	return proj, time.Time{}, false, true
}

// capLabel renders a cap time: clock only when today, weekday prefixed
// otherwise.
func capLabel(capAt, now time.Time) string {
	c, n := capAt.Local(), now.Local()
	if c.YearDay() == n.YearDay() && c.Year() == n.Year() {
		return c.Format("15:04")
	}
	return c.Format("Mon 15:04")
}

// paceText returns the text after "<label> <used>%" for the trailing path.
func paceText(label string, w *window, hist []sample, now time.Time) (text string, over, ok bool) {
	proj, capAt, hasCap, ok := trailingProjection(label, w, hist, now)
	if !ok {
		return "", false, false
	}
	if hasCap {
		return " cap " + capLabel(capAt, now), true, true
	}
	return fmt.Sprintf("→%d%%", int(math.Round(proj))), proj >= 100, true
}
