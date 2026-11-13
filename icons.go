package main

// Nerd Font glyphs that stand in for the words a segment used to carry. They
// need a Nerd Font in the terminal (Ghostty here); each is a single cell wide.
const (
	iconDir     = " "          // nf-fa-folder
	iconBranch  = " "          // nf-pl-branch
	iconModel   = "\U000f06a9 " // nf-md-robot
	iconContext = "\U000f035b " // nf-md-memory
	iconCache   = "\U000f0150 " // nf-md-clock_outline
	icon7d      = "\U000f051f " // nf-md-timer_sand
	icon5h      = "\U000f051b " // nf-md-timer_outline
	dirtyMark   = "●"
)

// limitIcon maps a rate-limit window label to its glyph.
func limitIcon(label string) string {
	if label == "5h" {
		return icon5h
	}
	return icon7d
}
