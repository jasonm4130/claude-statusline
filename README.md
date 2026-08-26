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
`~/.claude/settings.json`, else the model's context window size. Context and
rate-limit segments turn amber at 60% and red at 85%.

Git branch comes from reading `.git/HEAD` directly (worktree redirects included);
the dirty marker comes from `git status --porcelain` under a 150 ms timeout and is
dropped rather than waited on.
