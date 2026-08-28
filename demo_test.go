package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The README's demo image is generated from real render output, not drawn by
// hand, so it cannot drift from what the binary actually prints. The rows below
// go through the same draw() the terminal gets; the SVG is produced by parsing
// that ANSI back into coloured runs. CI runs this test without -update, so a
// change to the palette or the segment logic that is not reflected in the
// committed image fails the build.
//
//	go test -run TestDemoSVG -update
var updateDemo = flag.Bool("update", false, "regenerate "+demoSVGPath)

const demoSVGPath = "docs/statusline.svg"

type demoRow struct {
	caption string
	segs    []segment
}

// demoPayload is the shape of a real captured statusline payload.
func demoPayload(fiveUsed, sevenUsed float64) *payload {
	p := &payload{}
	p.ContextWindow = &struct {
		ContextWindowSize int     `json:"context_window_size"`
		UsedPercentage    float64 `json:"used_percentage"`
		CurrentUsage      *struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"current_usage"`
	}{ContextWindowSize: 1000000}
	p.ContextWindow.CurrentUsage = &struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	}{InputTokens: 2, OutputTokens: 273, CacheCreationInputTokens: 192, CacheReadInputTokens: 131752}
	p.RateLimits = &struct {
		FiveHour *window `json:"five_hour"`
		SevenDay *window `json:"seven_day"`
	}{
		FiveHour: &window{fiveUsed, 1787923200},
		SevenDay: &window{sevenUsed, 1788033600},
	}
	return p
}

func demoRows(t *testing.T, now time.Time) []demoRow {
	t.Helper()
	// The context segment measures against the auto-compact window, which is
	// read from the environment; pin it so CI and a laptop agree.
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "400000")

	// Segments that come from the machine rather than the payload (cwd, git,
	// model, transcript) are fixed here. Everything the README is actually
	// making a claim about -- context, pace, thresholds, colour -- comes from
	// the real functions.
	fixed := []segment{
		{"claude-statusline", idDir, txtDir, stateNormal},
		{"main*", idGit, idGit, stateNormal},
		{"opus·high", idModel, idModel, stateNormal},
	}
	cache := segment{"cache→22:56", idCache, idCache, stateNormal}

	build := func(p *payload) []segment {
		segs := append([]segment{}, fixed...)
		if s, ok := contextSegment(p); ok {
			segs = append(segs, s)
		}
		segs = append(segs, cache)
		return append(segs, limitSegments(p, now)...)
	}

	return []demoRow{
		{"on pace — both windows land well under the cap", build(demoPayload(13, 23))},
		{"five-hour burn projects past the cap; the weekly window is untouched", build(demoPayload(72, 23))},
		{"weekly window nearly spent — and still climbing", build(demoPayload(13, 91))},
	}
}

func TestDemoSVG(t *testing.T) {
	// The image must be identical on a laptop and in CI.
	pinZone(t)

	now := time.Unix(1787912597, 0)
	rows := demoRows(t, now)

	var lines []svgLine
	for _, r := range rows {
		lines = append(lines, svgLine{caption: r.caption, runs: parseANSI(draw(r.segs))})
	}
	got := renderSVG(lines)

	if *updateDemo {
		if err := os.WriteFile(demoSVGPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("wrote " + demoSVGPath)
		return
	}

	want, err := os.ReadFile(demoSVGPath)
	if err != nil {
		t.Fatalf("%v\n\nrun: go test -run TestDemoSVG -update", err)
	}
	if string(want) != got {
		t.Errorf("%s is stale.\n\nrun: go test -run TestDemoSVG -update", demoSVGPath)
	}
}

// --- ANSI -> SVG -------------------------------------------------------------

type run struct {
	text  string
	fg    rgb
	bg    rgb
	hasBG bool
	bold  bool
	sep   bool // the powerline diagonal, drawn as geometry rather than a glyph
}

type svgLine struct {
	caption string
	runs    []run
}

// parseANSI turns draw() output back into coloured runs. It understands only
// what draw() emits: truecolor fg/bg, bold on and off, and full reset.
func parseANSI(s string) []run {
	var (
		runs  []run
		cur   run
		flush = func() {
			if cur.text != "" {
				runs = append(runs, cur)
				cur.text = ""
			}
		}
	)
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			flush()
			applySGR(&cur, strings.Split(s[i+2:j], ";"))
			i = j + 1
			continue
		}
		r, size := decodeRune(s[i:])
		if string(r) == sep {
			flush()
			d := cur
			d.sep, d.text = true, sep
			runs = append(runs, d)
		} else {
			cur.text += string(r)
		}
		i += size
	}
	flush()
	return runs
}

func decodeRune(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 1
}

func applySGR(cur *run, params []string) {
	for i := 0; i < len(params); i++ {
		switch params[i] {
		case "0":
			*cur = run{}
		case "1":
			cur.bold = true
		case "22":
			cur.bold = false
		case "38", "48":
			if i+4 < len(params) && params[i+1] == "2" {
				c := rgb{atoi(params[i+2]), atoi(params[i+3]), atoi(params[i+4])}
				if params[i] == "38" {
					cur.fg = c
				} else {
					cur.bg, cur.hasBG = c, true
				}
				i += 4
			}
		}
	}
}

func atoi(s string) uint8 {
	n, _ := strconv.Atoi(s)
	return uint8(n)
}

const (
	svgCharW    = 8.4
	svgFontSize = 14.0
	svgRowH     = 26.0
	svgCapH     = 20.0
	svgGap      = 12.0
	svgPad      = 16.0
)

func hex(c rgb) string { return fmt.Sprintf("#%02x%02x%02x", c.r, c.g, c.b) }

func esc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func renderSVG(lines []svgLine) string {
	page := rgb{0x14, 0x15, 0x13}

	widest := 0
	for _, l := range lines {
		n := 0
		for _, r := range l.runs {
			n += len([]rune(r.text))
		}
		if n > widest {
			widest = n
		}
	}
	w := float64(widest)*svgCharW + 2*svgPad
	h := float64(len(lines))*(svgCapH+svgRowH+svgGap) - svgGap + 2*svgPad

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%.0f" height="%.0f" viewBox="0 0 %.0f %.0f" role="img" aria-label="claude-statusline rendered in three states">`+"\n", w, h, w, h)
	fmt.Fprintf(&b, `<rect width="%.0f" height="%.0f" rx="8" fill="%s"/>`+"\n", w, h, hex(page))
	fmt.Fprintf(&b, `<g font-family="ui-monospace,SFMono-Regular,Menlo,Consolas,monospace" font-size="%.0f">`+"\n", svgFontSize)

	y := svgPad
	for _, l := range lines {
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="11" fill="%s">%s</text>`+"\n",
			svgPad, y+13, hex(idDir), esc(l.caption))
		y += svgCapH
		x := svgPad
		for _, r := range l.runs {
			rw := float64(len([]rune(r.text))) * svgCharW
			if r.hasBG {
				fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s"/>`+"\n",
					x, y, rw, svgRowH, hex(r.bg))
			}
			if r.sep {
				// U+E0BC fills the upper-left half of the cell with the previous
				// segment's background, leaving the diagonal edge.
				fmt.Fprintf(&b, `<polygon points="%.1f,%.1f %.1f,%.1f %.1f,%.1f" fill="%s"/>`+"\n",
					x, y, x+rw, y, x, y+svgRowH, hex(r.fg))
			} else if strings.TrimSpace(r.text) != "" {
				weight := ""
				if r.bold {
					weight = ` font-weight="600"`
				}
				fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" fill="%s"%s textLength="%.1f" lengthAdjust="spacingAndGlyphs" xml:space="preserve">%s</text>`+"\n",
					x, y+18, hex(r.fg), weight, rw, esc(r.text))
			}
			x += rw
		}
		y += svgRowH + svgGap
	}
	b.WriteString("</g>\n</svg>\n")
	return b.String()
}
