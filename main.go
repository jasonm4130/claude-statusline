// Command claude-statusline renders a powerline-styled status line for Claude Code.
// It reads one JSON payload on stdin and prints a single line to stdout.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type payload struct {
	CWD   string `json:"cwd"`
	Model struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"model"`
	Workspace struct {
		CurrentDir string `json:"current_dir"`
		ProjectDir string `json:"project_dir"`
	} `json:"workspace"`
	Effort struct {
		Level string `json:"level"`
	} `json:"effort"`
	ContextWindow *struct {
		ContextWindowSize int     `json:"context_window_size"`
		UsedPercentage    float64 `json:"used_percentage"`
		CurrentUsage      *struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"current_usage"`
	} `json:"context_window"`
	RateLimits *struct {
		FiveHour *window `json:"five_hour"`
		SevenDay *window `json:"seven_day"`
	} `json:"rate_limits"`
	TranscriptPath string `json:"transcript_path"`
}

type window struct {
	UsedPercentage float64 `json:"used_percentage"`
	ResetsAt       int64   `json:"resets_at"`
}

// Palette (24-bit truecolor), theme: Cyber-Monokai.
type rgb struct{ r, g, b uint8 }

var (
	bgOdd  = rgb{0x17, 0x18, 0x15}
	bgEven = rgb{0x2E, 0x2F, 0x29}

	idDir   = rgb{0x75, 0x71, 0x5E}
	txtDir  = rgb{0xC8, 0xC8, 0xC2}
	idGit   = rgb{0xF9, 0x26, 0x72}
	idModel = rgb{0xAE, 0x81, 0xFF}
	idCtx   = rgb{0xA6, 0xE2, 0x2E}
	idLimit = rgb{0x66, 0xD9, 0xEF}
	idCache = rgb{0xFD, 0x97, 0x1F}

	bgWarm  = rgb{0x3A, 0x35, 0x20}
	fgWarm  = rgb{0xE6, 0xDB, 0x74}
	sepWarm = rgb{0x2A, 0x26, 0x18}
	bgCrit  = rgb{0xF9, 0x26, 0x72}
	fgCrit  = rgb{0xFF, 0xFF, 0xFF}
	sepCrit = rgb{0xC7, 0x1F, 0x5B}
)

const sep = "\ue0bc"

type state int

const (
	stateNormal state = iota
	stateWarming
	stateCritical
)

type segment struct {
	text  string
	id    rgb // identity colour (kept for future accents; text fg carries identity)
	fg    rgb // text colour
	state state
}

// Set by the linker at release time; a local build reports "dev".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-v", "version":
			fmt.Printf("claude-statusline %s (%s, built %s)\n", version, commit, date)
			os.Exit(0)
		}
	}
	out := render()
	_, _ = fmt.Fprintln(os.Stdout, out)
	os.Exit(0)
}

func render() string {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fallback()
	}
	var p payload
	if err := json.Unmarshal(data, &p); err != nil {
		return fallback()
	}
	segs := build(&p)
	if len(segs) == 0 {
		return fallback()
	}
	return draw(segs)
}

func fallback() string {
	wd := os.Getenv("PWD")
	if wd == "" {
		wd, _ = os.Getwd()
	}
	if wd == "" {
		return ""
	}
	return dirLabel(wd)
}

func dirLabel(dir string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if filepath.Clean(dir) == filepath.Clean(home) {
			return "~"
		}
	}
	return filepath.Base(filepath.Clean(dir))
}

func build(p *payload) []segment {
	var segs []segment

	dir := p.Workspace.CurrentDir
	if dir == "" {
		dir = p.CWD
	}
	if dir != "" {
		segs = append(segs, segment{dirLabel(dir), idDir, txtDir, stateNormal})
	}

	if dir != "" {
		if b, ok := gitBranch(dir); ok {
			if gitDirty(dir) {
				b += "*"
			}
			segs = append(segs, segment{b, idGit, idGit, stateNormal})
		}
	}

	if p.Model.DisplayName != "" {
		name := strings.ToLower(strings.Fields(p.Model.DisplayName)[0])
		if e := effortAbbrev(p.Effort.Level); e != "" {
			name += "·" + e
		}
		segs = append(segs, segment{name, idModel, idModel, stateNormal})
	}

	if s, ok := contextSegment(p); ok {
		segs = append(segs, s)
	}
	if s, ok := cacheSegment(p, time.Now()); ok {
		segs = append(segs, s)
	}
	segs = append(segs, limitSegments(p, time.Now())...)
	return segs
}

func effortAbbrev(level string) string {
	switch strings.ToLower(level) {
	case "low":
		return "low"
	case "medium":
		return "med"
	case "high":
		return "high"
	case "xhigh":
		return "xhi"
	case "max":
		return "max"
	}
	return ""
}

// gitBranch resolves the current branch by reading .git/HEAD directly.
func gitBranch(dir string) (string, bool) {
	gitDir, ok := findGitDir(dir)
	if !ok {
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", false
	}
	head := strings.TrimSpace(string(b))
	if ref, found := strings.CutPrefix(head, "ref: "); found {
		return filepath.Base(ref), true
	}
	if len(head) >= 7 {
		return head[:7], true
	}
	return "", false
}

func findGitDir(dir string) (string, bool) {
	d := filepath.Clean(dir)
	for {
		candidate := filepath.Join(d, ".git")
		if fi, err := os.Stat(candidate); err == nil {
			if fi.IsDir() {
				return candidate, true
			}
			// Worktree/submodule: a file containing "gitdir: <path>".
			b, err := os.ReadFile(candidate)
			if err != nil {
				return "", false
			}
			target, found := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
			if !found {
				return "", false
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(d, target)
			}
			return filepath.Clean(target), true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false
		}
		d = parent
	}
}

func gitDirty(dir string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain", "--untracked-files=no")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}

func contextSegment(p *payload) (segment, bool) {
	cw := p.ContextWindow
	if cw == nil || cw.CurrentUsage == nil {
		return segment{}, false
	}
	u := cw.CurrentUsage
	used := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
	target := autoCompactTarget()
	if target <= 0 {
		target = cw.ContextWindowSize
	} else if cw.ContextWindowSize > 0 && target > cw.ContextWindowSize {
		target = cw.ContextWindowSize
	}
	if target <= 0 {
		return segment{}, false
	}
	pct := float64(used) / float64(target) * 100
	text := fmt.Sprintf("%dk/%dk %d%%", roundK(used), roundK(target), int(math.Round(pct)))
	fg, st := threshold(pct, idCtx)
	return segment{text, idCtx, fg, st}, true
}

func roundK(v int) int {
	return int(math.Round(float64(v) / 1000))
}

func autoCompactTarget() int {
	if v := os.Getenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return 0
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		return 0
	}
	var s struct {
		AutoCompactWindow float64 `json:"autoCompactWindow"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return 0
	}
	return int(s.AutoCompactWindow)
}

// Prompt-cache expiry.
//
// Claude Code runs the statusLine command on state change, never on a timer:
// measured idle, it goes 60+ seconds between invocations. So anything rendered
// here must stay TRUE while frozen, which rules out a countdown ("4:32 left")
// and rules out a warm/expired badge — both silently become lies the moment the
// session goes quiet, and quiet is exactly when you want to know.
//
// An absolute clock time survives the freeze: "cache→22:56" is still true at
// 23:30, because the arithmetic happened once and does not decay. The one claim
// that is safe to make is the negative one — expiry only moves forward when a
// new request lands, and a new request re-renders, so "cold" stays true once
// shown.
const transcriptTailBytes = 512 * 1024

type usageEntry struct {
	at  time.Time
	ttl time.Duration
}

// readTail returns up to limit bytes from the end of path, dropping the partial
// first line so every line handed back is whole.
func readTail(path string, limit int64) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, false
	}
	start := int64(0)
	if fi.Size() > limit {
		start = fi.Size() - limit
	}
	buf := make([]byte, fi.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return nil, false
	}
	if start > 0 {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	return buf, true
}

// parseLastUsage scans transcript lines newest-first for the cache clock: the
// timestamp comes from the most recent request (a cache read refreshes the
// entry's TTL), while the tier comes from the most recent request that actually
// wrote a cache entry — a pure-read request reports neither ephemeral field.
func parseLastUsage(tail []byte) (usageEntry, bool) {
	lines := bytes.Split(tail, []byte("\n"))
	var at time.Time
	var ttl time.Duration
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 || !bytes.Contains(line, []byte(`"usage"`)) {
			continue
		}
		var rec struct {
			Timestamp string `json:"timestamp"`
			Message   struct {
				Usage *struct {
					CacheCreation *struct {
						Ephemeral1h int `json:"ephemeral_1h_input_tokens"`
						Ephemeral5m int `json:"ephemeral_5m_input_tokens"`
					} `json:"cache_creation"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &rec); err != nil || rec.Message.Usage == nil {
			continue
		}
		if at.IsZero() {
			t, err := time.Parse(time.RFC3339, rec.Timestamp)
			if err != nil {
				continue
			}
			at = t
		}
		if cc := rec.Message.Usage.CacheCreation; cc != nil {
			switch {
			case cc.Ephemeral1h > 0:
				ttl = time.Hour
			case cc.Ephemeral5m > 0:
				ttl = 5 * time.Minute
			}
		}
		if ttl > 0 {
			break
		}
	}
	if at.IsZero() || ttl == 0 {
		return usageEntry{}, false
	}
	return usageEntry{at, ttl}, true
}

func cacheSegment(p *payload, now time.Time) (segment, bool) {
	if p.TranscriptPath == "" {
		return segment{}, false
	}
	tail, ok := readTail(p.TranscriptPath, transcriptTailBytes)
	if !ok {
		return segment{}, false
	}
	e, ok := parseLastUsage(tail)
	if !ok {
		return segment{}, false
	}
	expiry := e.at.Add(e.ttl)
	if !now.Before(expiry) {
		return segment{"cache cold", idCache, fgWarm, stateWarming}, true
	}
	return segment{"cache→" + expiry.Local().Format("15:04"), idCache, idCache, stateNormal}, true
}

// Pace.
//
// Each window has a known length, so resets_at fixes its start and the fraction
// already elapsed is arithmetic. Dividing used% by that fraction projects where
// the window lands at reset if the average burn so far continues: under 100% is
// headroom, over 100% means the cap arrives before the reset does.
//
// Like the cache clock this render can sit frozen for minutes, but the drift is
// safe here. While idle, elapsed grows and used does not, so the true projection
// only ever falls — a stale reading overstates the burn and never invites spend
// the window cannot cover.
const (
	fiveHourLen = 5 * time.Hour
	sevenDayLen = 7 * 24 * time.Hour
	// Under this much of a window elapsed, used/elapsed is dominated by
	// whatever landed in the first few minutes and projects noise.
	paceFloor = 0.05
)

func projected(w *window, length time.Duration, now time.Time) (float64, bool) {
	if w == nil || w.ResetsAt <= 0 {
		return 0, false
	}
	elapsed := length - time.Unix(w.ResetsAt, 0).Sub(now)
	e := float64(elapsed) / float64(length)
	if e < paceFloor || e > 1 {
		return 0, false
	}
	return math.Min(w.UsedPercentage/e, 999), true
}

// Each window renders as its own segment so it can carry its own colour: a hot
// five-hour window should not repaint a healthy weekly one, and vice versa.
func limitSegments(p *payload, now time.Time) []segment {
	rl := p.RateLimits
	if rl == nil {
		return nil
	}
	day := ""
	if rl.SevenDay != nil && rl.SevenDay.ResetsAt > 0 {
		day = time.Unix(rl.SevenDay.ResetsAt, 0).Local().Format("Mon")
	}
	var segs []segment
	if s, ok := limitWindow("7d", rl.SevenDay, sevenDayLen, day, now); ok {
		segs = append(segs, s)
	}
	if s, ok := limitWindow("5h", rl.FiveHour, fiveHourLen, "", now); ok {
		segs = append(segs, s)
	}
	return segs
}

// overPace marks a window whose projection has passed the cap. Colour alone
// cannot carry that: the warming amber also means "used a lot", the two
// conditions are different, and a glyph survives a colourblind reader and a
// terminal with a mangled palette.
const overPace = "\u25b2 "

func limitWindow(label string, w *window, length time.Duration, suffix string, now time.Time) (segment, bool) {
	if w == nil {
		return segment{}, false
	}
	st := stateOf(w.UsedPercentage)
	text := fmt.Sprintf("%s %d%%", label, int(math.Round(w.UsedPercentage)))
	if proj, ok := projected(w, length, now); ok {
		text += fmt.Sprintf("\u2192%d%%", int(math.Round(proj)))
		if proj >= 100 {
			text = overPace + text
			if st < stateWarming {
				st = stateWarming
			}
		}
	}
	if suffix != "" {
		text += " " + suffix
	}
	return segment{text, idLimit, stateFG(st, idLimit), st}, true
}

func stateOf(pct float64) state {
	switch {
	case pct >= 85:
		return stateCritical
	case pct >= 60:
		return stateWarming
	default:
		return stateNormal
	}
}

func stateFG(st state, normal rgb) rgb {
	switch st {
	case stateCritical:
		return fgCrit
	case stateWarming:
		return fgWarm
	}
	return normal
}

func threshold(pct float64, normal rgb) (fg rgb, st state) {
	st = stateOf(pct)
	return stateFG(st, normal), st
}

func fg(c rgb) string { return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c.r, c.g, c.b) }
func bg(c rgb) string { return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", c.r, c.g, c.b) }

// segBG resolves a segment's background: threshold states override the
// alternating charcoals, which alternate by render position among present
// segments.
func segBG(s segment, i int) rgb {
	switch s.state {
	case stateCritical:
		return bgCrit
	case stateWarming:
		return bgWarm
	}
	if i%2 == 0 {
		return bgOdd
	}
	return bgEven
}

func draw(segs []segment) string {
	var b strings.Builder
	bgs := make([]rgb, len(segs))
	for i, s := range segs {
		bgs[i] = segBG(s, i)
	}
	for i, s := range segs {
		b.WriteString(bg(bgs[i]))
		if s.state == stateCritical {
			b.WriteString(fg(s.fg) + "\x1b[1m " + s.text + " \x1b[22m")
		} else {
			b.WriteString(" " + fg(s.fg) + s.text + " ")
		}
		if i+1 < len(segs) {
			chev := bgs[i]
			if chev == bgs[i+1] {
				switch s.state {
				case stateCritical:
					chev = sepCrit
				case stateWarming:
					chev = sepWarm
				}
			}
			b.WriteString(bg(bgs[i+1]) + fg(chev) + sep)
		} else {
			b.WriteString("\x1b[0m" + fg(bgs[i]) + sep + "\x1b[0m")
		}
	}
	return b.String()
}
