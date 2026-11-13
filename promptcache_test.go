package main

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

func pcPayload(t *testing.T, raw string) *payload {
	t.Helper()
	var p payload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	return &p
}

func TestPayloadCacheSegment(t *testing.T) {
	exp := time.Unix(1794577548, 0)
	clock := exp.Local().Format("15:04")
	base := `{"prompt_cache":{"warm":true,"caching_observed":true,"ttl":"1h","expires_at":1794577548,"misses":0,"expected_rebuilds":0,"recache_tokens_if_cold":66157,"last_miss_at":null,"last_miss_cause":null}}`

	t.Run("warm shows the absolute clock", func(t *testing.T) {
		s, ok := cacheSegmentFor(pcPayload(t, base), nil, exp.Add(-time.Minute))
		if !ok || s.text != "cache→"+clock || s.state != stateNormal {
			t.Errorf("got %+v ok=%v", s, ok)
		}
	})
	t.Run("warm flag is ignored once expires_at has passed", func(t *testing.T) {
		s, _ := cacheSegmentFor(pcPayload(t, base), nil, exp)
		if s.text != "cache cold · 66k recache" || s.state != stateWarming {
			t.Errorf("got %+v", s)
		}
	})
	t.Run("cold without recache cost", func(t *testing.T) {
		p := pcPayload(t, `{"prompt_cache":{"warm":true,"caching_observed":true,"expires_at":1794577548,"recache_tokens_if_cold":0}}`)
		s, _ := cacheSegmentFor(p, nil, exp.Add(time.Hour))
		if s.text != "cache cold" {
			t.Errorf("got %q", s.text)
		}
	})
	t.Run("expected rebuilds alone are silent", func(t *testing.T) {
		p := pcPayload(t, `{"prompt_cache":{"caching_observed":true,"expires_at":1794577548,"misses":0,"expected_rebuilds":3}}`)
		s, _ := cacheSegmentFor(p, nil, exp.Add(-time.Minute))
		if s.state != stateNormal || s.text != "cache→"+clock {
			t.Errorf("got %+v", s)
		}
	})
	t.Run("recent unexpected miss warms and shows count and cause", func(t *testing.T) {
		now := exp.Add(-30 * time.Minute)
		raw := `{"prompt_cache":{"caching_observed":true,"expires_at":1794577548,"misses":2,"last_miss_at":` +
			strconv.FormatInt(now.Add(-2*time.Minute).Unix(), 10) + `,"last_miss_cause":"tools"}}`
		s, _ := cacheSegmentFor(pcPayload(t, raw), nil, now)
		if want := "cache→" + clock + " ✗2 tools"; s.text != want || s.state != stateWarming {
			t.Errorf("got %+v, want text %q", s, want)
		}
	})
	t.Run("long cause is dropped", func(t *testing.T) {
		p := pcPayload(t, `{"prompt_cache":{"caching_observed":true,"expires_at":1794577548,"misses":1,"last_miss_cause":"system_prompt_changed"}}`)
		s, _ := cacheSegmentFor(p, nil, exp.Add(-time.Minute))
		if want := "cache→" + clock + " ✗1"; s.text != want {
			t.Errorf("got %q, want %q", s.text, want)
		}
	})
	t.Run("old miss keeps the count but not the alarm", func(t *testing.T) {
		now := exp.Add(-10 * time.Minute)
		raw := `{"prompt_cache":{"caching_observed":true,"expires_at":1794577548,"misses":1,"last_miss_at":` +
			strconv.FormatInt(now.Add(-time.Hour).Unix(), 10) + `}}`
		s, _ := cacheSegmentFor(pcPayload(t, raw), nil, now)
		if s.state != stateNormal || s.text != "cache→"+clock+" ✗1" {
			t.Errorf("got %+v", s)
		}
	})
	t.Run("falls back to the transcript", func(t *testing.T) {
		tail := []byte(line("2026-08-27T11:00:00Z", 964, 0))
		at, _ := time.Parse(time.RFC3339, "2026-08-27T11:00:00Z")
		for _, raw := range []string{
			`{}`,
			`{"prompt_cache":{"caching_observed":false,"expires_at":1794577548}}`,
			`{"prompt_cache":{"caching_observed":true}}`,
		} {
			s, ok := cacheSegmentFor(pcPayload(t, raw), tail, at.Add(time.Minute))
			if !ok || s.text != "cache→"+at.Add(time.Hour).Local().Format("15:04") {
				t.Errorf("%s: got %+v ok=%v", raw, s, ok)
			}
		}
		if _, ok := cacheSegmentFor(pcPayload(t, `{}`), nil, time.Now()); ok {
			t.Error("segment present with no data at all")
		}
	})
}

func TestFastModeMarker(t *testing.T) {
	p := pcPayload(t, `{"model":{"display_name":"Opus 5"},"effort":{"level":"high"},"fast_mode":true}`)
	s, _ := modelSegment(p, turn{}, false)
	if got := withFastMode(p, s).text; got != "opus·high ⚡" {
		t.Errorf("fast on: %q", got)
	}
	p.FastMode = false
	if got := withFastMode(p, s).text; got != "opus·high" {
		t.Errorf("fast off: %q", got)
	}
}
