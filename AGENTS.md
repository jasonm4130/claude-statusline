# AGENTS.md

A Claude Code status line: one Go binary that reads the statusline JSON on stdin and prints one ANSI line. README.md holds the design rationale; read its "Design notes" before changing what a segment says.

## Rules that are easy to break

- **Go standard library only.** No modules in `go.mod` beyond the language version.
- **Always exit 0 and always print something.** A parse failure degrades to the directory name; errors in side paths (snapshot, caches, git) are silent and fall back.
- **Latency budget.** The command runs on every state change and every `refreshInterval` tick. No new subprocesses; file reads are bounded (transcripts are read from the tail, 512 KB max). Check `go test -bench Render -benchmem` before and after anything on the render path, and quote both numbers in the PR.
- **True while frozen.** A render can sit unchanged for minutes, and the binary cannot tell whether a timer is configured. Show absolute clock times, not countdowns, and only claims that stay true or err on the safe side as the line goes stale.
- **Each segment disappears when its data is absent.** Payload fields come and go between Claude Code versions and accounts (an Ultra account has no `five_hour` window). Absence is never an error.
- **State files** live under `os.UserCacheDir()/claude-statusline`, written atomically (temp file in the same directory, then rename). Losing one must be harmless.

## Layout

`main.go` holds the payload, the segment builders and `draw()`. Features added later sit in their own files (`snapshot.go`, `promptcache.go`, `gitcache.go`, `agents.go`, `pace.go`) with matching `_test.go` files; follow that pattern rather than growing `main.go`.

## Checks

```sh
gofmt -l .                  # must print nothing
go vet ./...
go test -race ./...
golangci-lint run ./...
```

`docs/statusline.svg` is generated from real render output and CI fails if it is stale. After an intended visual change, run `go test -run TestDemoSVG -update .` and commit the SVG.

## Releasing

Releases are tag-driven: push an annotated `vX.Y.Z` tag on `main` and the Release workflow builds darwin/linux × amd64/arm64 with checksums and SBOMs. Installing on Jason's machine means downloading the darwin_arm64 archive, checking it against `checksums.txt`, and `install -m 755` into `~/.local/bin/`. The settings that call it live in chezmoi (`private_dot_claude/settings.json` in the dotfiles repo), not here.
