package main

import "strconv"

// Parallel agents.
//
// The model segment names whichever subagent wrote last, so a fan-out of four
// reads as one. withAgentCount adds how many subagent transcripts were written
// since the main agent's own last turn: "⤷ explore·sonnet·med (3 agents)".
//
// The window is that last main turn, not a fixed span before now, and the
// choice is what keeps the claim true while frozen. A "running now" count
// would outlive the agents on a render nobody refreshes; "N agents wrote
// since the main agent last spoke" is a fact about the past that cannot
// become false, only incomplete, until the main agent speaks again, and that
// re-renders. It also needs no tuning: sequential agents always have a main
// turn between them, because the main agent reads one result before it
// dispatches the next, so only agents working alongside each other share the
// window. A clock window would either merge sequential runs (too long) or
// drop an agent sitting quietly on a slow tool call (too short).
//
// The count rides only on a subagent label and only above one; a lone agent
// is already said by the label itself.
func withAgentCount(s segment, t turn, haveTurn bool) segment {
	if haveTurn && t.agent != "" && t.agents > 1 {
		s.text += " ×" + strconv.Itoa(t.agents)
	}
	return s
}
