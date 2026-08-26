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

`dir  branch*  model·effort  132k/400k 33%  7d 34%→Sat 5h 12%`

Each segment is omitted when its data is absent. The directory falls back to
`$PWD`'s basename if stdin cannot be parsed, so the status row never blanks.

The context segment measures usage against the auto-compact window:
`CLAUDE_CODE_AUTO_COMPACT_WINDOW` if set, else `autoCompactWindow` from
`~/.claude/settings.json`, else the model's context window size.

## Palette

24-bit truecolor (`ESC[38;2;R;G;Bm` / `ESC[48;2;R;G;Bm`), theme Cyber-Monokai.
Segments alternate between charcoal backgrounds `#1E1F1C` and `#262723` by render
position among the segments actually present, so an omitted segment never leaves
two same-coloured neighbours. Each segment opens with a `▎` edge glyph in its
identity colour, and segments are joined by powerline diagonals (U+E0BC) coloured from the
previous background onto the next.

| Segment | Identity | Text |
|---|---|---|
| dir | `#75715E` | `#C8C8C2` |
| git | `#F92672` | identity |
| model·effort | `#AE81FF` | identity |
| context | `#A6E22E` | identity |
| limits | `#66D9EF` | identity |

Context and rate-limit segments each key on their own percentage. At 60–84% the
segment goes warming: background `#3A3520`, edge and text `#E6DB74`. At 85% and
above it goes critical: background `#F92672` flooded (no edge glyph) with bold
`#FFFFFF` text. Two adjacent segments in the same state share a background, so
the separator between them darkens (`#2A2618` warming, `#C71F5B` critical) to keep
its shape visible.

Git branch comes from reading `.git/HEAD` directly (worktree redirects included);
the dirty marker comes from `git status --porcelain` under a 150 ms timeout and is
dropped rather than waited on.
