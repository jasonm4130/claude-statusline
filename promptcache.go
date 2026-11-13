package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// promptCache is the payload's own account of the prompt cache (Claude Code
// 2.1.323+). When it is present and has observed caching, it replaces the
// transcript parsing: the expiry is given, not inferred.
type promptCache struct {
	Warm                bool            `json:"warm"`
	CachingObserved     bool            `json:"caching_observed"`
	ExpiresAt           int64           `json:"expires_at"`
	Misses              int             `json:"misses"`
	ExpectedRebuilds    int             `json:"expected_rebuilds"`
	RecacheTokensIfCold int             `json:"recache_tokens_if_cold"`
	LastMissAt          json.RawMessage `json:"last_miss_at"`
	LastMissCause       json.RawMessage `json:"last_miss_cause"`
}

// UnmarshalJSON never fails: a prompt_cache whose shape has drifted from what
// this binary knows costs only the cache segment, never the whole line.
func (pc *promptCache) UnmarshalJSON(b []byte) error {
	type plain promptCache
	var v plain
	if err := json.Unmarshal(b, &v); err != nil {
		*pc = promptCache{}
		return nil
	}
	*pc = promptCache(v)
	return nil
}

// missCause reads last_miss_cause, which Claude Code sends either as a string
// or as {"causes":["ttl_expired_1h", ...]}. It returns the first cause.
func (pc *promptCache) missCause() string {
	var s string
	if json.Unmarshal(pc.LastMissCause, &s) == nil {
		return s
	}
	var o struct {
		Causes []string `json:"causes"`
	}
	if json.Unmarshal(pc.LastMissCause, &o) == nil && len(o.Causes) > 0 {
		return o.Causes[0]
	}
	return ""
}

// Freeze rules. The render can sit frozen, so `warm` is never trusted on its
// own: coldness is decided by comparing expires_at with the clock at render
// time, exactly as the transcript path does. Only absolute facts go on the line.
//
// Miss rule. `misses` counts cache misses Claude Code did not plan for;
// planned rebuilds (compaction, a model switch, an edited system prompt) are
// counted separately in `expected_rebuilds` and are deliberately never shown,
// since a miss you caused on purpose is not news. (A live payload with
// expected_rebuilds=1 and misses=0 is the evidence that the two are disjoint.)
// So misses > 0 is an unexpected miss: the segment appends ✗N, plus the cause
// when it is short, and goes warming while the last miss is recent. The count
// is cumulative and so stays true when frozen; the amber fades to the normal
// style once the last miss is older than missAlarmFor, because a session-long
// amber segment stops carrying information.
const missAlarmFor = 15 * time.Minute

// missCauseMax keeps the cause label from crowding a line of ~7 segments.
const missCauseMax = 8

func (pc *promptCache) usable() bool {
	return pc != nil && pc.CachingObserved && pc.ExpiresAt > 0
}

// lastMiss parses last_miss_at, which may be unix seconds or an RFC3339 string.
func (pc *promptCache) lastMiss() (time.Time, bool) {
	s := strings.Trim(strings.TrimSpace(string(pc.LastMissAt)), `"`)
	if s == "" || s == "null" {
		return time.Time{}, false
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1e11 { // milliseconds
			return time.UnixMilli(n), true
		}
		return time.Unix(n, 0), true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// payloadCacheSegment renders the cache segment from the payload.
func payloadCacheSegment(pc *promptCache, now time.Time) (segment, bool) {
	if !pc.usable() {
		return segment{}, false
	}
	expiry := time.Unix(pc.ExpiresAt, 0)
	text, st, fgc := "", stateNormal, idCache
	if !now.Before(expiry) {
		text, st, fgc = iconCache+"cold", stateWarming, fgWarm
		if pc.RecacheTokensIfCold > 0 {
			text += fmt.Sprintf(" · %dk", roundK(pc.RecacheTokensIfCold))
		}
	} else {
		text = iconCache + expiry.Local().Format("15:04")
	}
	if pc.Misses > 0 {
		text += fmt.Sprintf(" ✗%d", pc.Misses)
		if c := pc.missCause(); c != "" && utf8.RuneCountInString(c) <= missCauseMax {
			text += " " + c
		}
		if t, ok := pc.lastMiss(); !ok || now.Sub(t) < missAlarmFor {
			st, fgc = stateWarming, fgWarm
		}
	}
	return segment{text, idCache, fgc, st}, true
}

// cacheSegmentFor prefers the payload's prompt_cache and falls back to the
// transcript when it is absent, has not observed caching, or lacks expires_at.
func cacheSegmentFor(p *payload, tail []byte, now time.Time) (segment, bool) {
	if s, ok := payloadCacheSegment(p.PromptCache, now); ok {
		return s, true
	}
	return cacheSegment(tail, now)
}

// fastMark flags the model segment when fast mode is on.
const fastMark = " ⚡"

func withFastMode(p *payload, s segment) segment {
	if p.FastMode {
		s.text += fastMark
	}
	return s
}
