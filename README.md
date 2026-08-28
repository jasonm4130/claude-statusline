# claude-statusline

A powerline-styled status line for Claude Code. Reads the statusline JSON payload on
stdin, writes one ANSI-coloured line to stdout, always exits 0. Stdlib only.

## Build

```sh
go build -o ~/.local/bin/claude-statusline .
```

## Use

In `~/.claude/settings.json`:

```json
"statusLine": { "type": "command", "command": "~/.local/bin/claude-statusline" }
```

## Segments

`dir  branch*  model·effort  132k/400k 33%  cache→22:56  7d 23%→29% Sun 5h 13%→31%`

Each segment is omitted when its data is absent. The directory falls back to
`$PWD`'s basename if stdin cannot be parsed, so the status row never blanks.

The context segment measures usage against the auto-compact window:
`CLAUDE_CODE_AUTO_COMPACT_WINDOW` if set, else `autoCompactWindow` from
`~/.claude/settings.json`, else the model's context window size.

### Rate-limit pace

The limits segment answers how hard you can spend, not just how much you have
spent. Each window has a known length, so `resets_at` fixes its start and the
fraction already elapsed is arithmetic; used% over that fraction projects where
the window lands at reset if the average burn so far continues. `5h 13%→31%`
means the current burn ends the five-hour window at 31% — plenty of headroom.
Over 100% means the cap arrives before the reset does, and the segment goes
warming.

Like the cache clock this render sits frozen for minutes at a time, but the
drift is safe here rather than dangerous. While the session is idle, elapsed
grows and used does not, so the true projection only ever falls: a stale reading
overstates the burn, and never invites spend the window cannot cover.

Projection is suppressed below 5% elapsed, where used/elapsed is dominated by
whatever landed in the first few minutes, and when `resets_at` is missing or
already past — in those cases the segment falls back to bare usage. The value is
clamped at 999%.

### Prompt-cache expiry

The cache segment shows an absolute clock time, not a countdown, because Claude
Code runs this command on state change and never on a timer — measured idle, it
goes 60+ seconds between invocations. A countdown or a warm/expired badge would
freeze mid-session and quietly become a lie, exactly when a quiet session is when
you want to know. `cache→22:56` is still true at 23:30: the arithmetic happened
once and does not decay. The one claim safe to render is the negative one, so
once the clock has passed the segment reads `cache cold` and stays honest —
expiry only moves forward when a new request lands, and a new request re-renders.

Both inputs come from the `transcript_path` the payload already supplies. The
timestamp is the newest request's, since a cache read refreshes the entry's TTL;
the tier is the newest request that actually wrote a cache entry, read from
`message.usage.cache_creation` (`ephemeral_1h_input_tokens` vs
`ephemeral_5m_input_tokens`) — so the TTL is measured, never assumed. Only the
last 512 KB of the transcript is read, from the end, with the partial first line
dropped; the cost is not measurable against the git-status subprocess.

## Palette

24-bit truecolor (`ESC[38;2;R;G;Bm` / `ESC[48;2;R;G;Bm`), theme Cyber-Monokai.
Segments alternate between charcoal backgrounds `#171815` and `#2E2F29` by render
position among the segments actually present, so an omitted segment never leaves
two same-coloured neighbours. Each segment's identity lives in its
text colour, and segments are joined by powerline diagonals (U+E0BC) coloured from the
previous background onto the next.

| Segment | Identity | Text |
|---|---|---|
| dir | `#75715E` | `#C8C8C2` |
| git | `#F92672` | identity |
| model·effort | `#AE81FF` | identity |
| context | `#A6E22E` | identity |
| cache | `#FD971F` | identity |
| limits | `#66D9EF` | identity |

A cold cache borrows the warming style. The context segment keys on its own
percentage; the limits segment takes the worst state across both windows, and
goes warming on its own if either projection reaches 100%. At 60–84% the
segment goes warming: background `#3A3520`, text `#E6DB74`. At 85% and
above it goes critical: background `#F92672` flooded with bold
`#FFFFFF` text. Two adjacent segments in the same state share a background, so
the separator between them darkens (`#2A2618` warming, `#C71F5B` critical) to keep
its shape visible.

Git branch comes from reading `.git/HEAD` directly (worktree redirects included);
the dirty marker comes from `git status --porcelain` under a 150 ms timeout and is
dropped rather than waited on.
