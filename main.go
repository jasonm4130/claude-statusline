// Command claude-statusline renders a powerline-styled status line for Claude Code.
// It reads one JSON payload on stdin and prints a single line to stdout.
package main

import (
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

func main() {
	out := render()
	fmt.Fprintln(os.Stdout, out)
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
	if s, ok := weeklySegment(p); ok {
		segs = append(segs, s)
	}
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

func weeklySegment(p *payload) (segment, bool) {
	rl := p.RateLimits
	if rl == nil || (rl.SevenDay == nil && rl.FiveHour == nil) {
		return segment{}, false
	}
	var parts []string
	key := 0.0
	if rl.SevenDay != nil {
		s := fmt.Sprintf("7d %d%%", int(math.Round(rl.SevenDay.UsedPercentage)))
		if rl.SevenDay.ResetsAt > 0 {
			s += "→" + time.Unix(rl.SevenDay.ResetsAt, 0).Local().Format("Mon")
		}
		parts = append(parts, s)
		key = rl.SevenDay.UsedPercentage
	}
	if rl.FiveHour != nil {
		parts = append(parts, fmt.Sprintf("5h %d%%", int(math.Round(rl.FiveHour.UsedPercentage))))
		if rl.SevenDay == nil {
			key = rl.FiveHour.UsedPercentage
		}
	}
	fg, st := threshold(key, idLimit)
	return segment{strings.Join(parts, " "), idLimit, fg, st}, true
}

func threshold(pct float64, normal rgb) (fg rgb, st state) {
	switch {
	case pct >= 85:
		return fgCrit, stateCritical
	case pct >= 60:
		return fgWarm, stateWarming
	default:
		return normal, stateNormal
	}
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
