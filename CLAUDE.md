# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`glx` is a Bubble Tea terminal UI for GitLab merge requests and pipelines
(module `github.com/hamkens/glx`), targeting self-hosted GitLab instances.
Entry point: `cmd/glx/main.go`.

## Commands

No Makefile; use `go` directly.

```sh
go build -o glx ./cmd/glx     # build
go run ./cmd/glx --check      # verify config/auth + connectivity, no TUI
go run ./cmd/glx              # launch the TUI
go test ./...                 # run all tests
go test ./internal/gitlab -run TestName   # run a single test
go vet ./...
gofmt -l internal/ cmd/       # list unformatted files (use -w to fix)
```

## Architecture

Two layers: `internal/gitlab` (API access) and `internal/tui` (Bubble Tea
views), wired together in `cmd/glx/main.go` after `internal/config` resolves
host/token.

### internal/gitlab — dual-backend client

`Client` (`client.go`) talks to one GitLab host over two backends and picks
whichever is cheaper for a given view:

- **GraphQL** (`graphql.go`) for read/list-heavy views — MR list (`mr.go`)
  and MR detail (`mr_detail.go`) — where one query replaces REST N+1 round
  trips.
- **REST** (`xanzy/go-gitlab`) for writes (`mr_actions.go`: approve, merge,
  rebase, comment) and diff payloads (`mr_diff.go`, `mr_diff_comment.go`),
  where endpoints are stable and well documented.

Three per-read-type TTL caches (`internal/cache`, 30s) live on `Client`:
`mrListCache`, `mrDetailCache`, `mrDiffCache`. `WithForceRefresh(ctx)` marks a
context to bypass and repopulate the cache — this is how the `r` key works
end-to-end. When adding a new cached read, follow this pattern: check
`forced(ctx)`, otherwise `Get`/`Set` around the API call.

`MRPage.MRs` (`mr.go`) is the flattened row type the TUI renders; GraphQL
scopes (`ScopeAssigned`/`ScopeReviewer`/`ScopeAuthored`) map to
`currentUser` connection fields via `connectionField()`.

### internal/tui — Bubble Tea views

`rootModel` (`app.go`) is the router. It holds one sub-model per screen
(`mrListModel`, `detailModel`, `diffModel`, `pipelineModel`, `jobLogModel`,
`watchModel`) and a `view` enum selecting the active one; `enter`
drills in, `esc`/backspace steps back. Sub-models each own their file:
`mrlist.go`, `mrdetail.go`, `diffview.go`, `pipelineview.go`, `joblog.go`,
`watchview.go`.

Cross-cutting root-level concerns to check when touching `app.go`:
- `inputActive()` — suppresses global single-key shortcuts (like `?`) while
  a view has a text input capturing keystrokes (comment mode, filter mode).
- `currentURL()` / `focusedPipeline()` — resolve "what's focused right now"
  across views, backing the global `o` (open in browser), `w` (watch), and
  pipeline-action keys so those don't need per-view duplication.
- `watchReturn` / `pipeReturn` — remember which view to pop back to when
  leaving the watch/pipeline/job-log screens, since those are reachable from
  more than one place.

`internal/watch` is an in-memory `Registry` of pipelines marked with `w`.
The root model runs a single background poller (`watchInterval`, 15s) that
refreshes active entries and diffs status to raise change alerts — this
lives independent of whichever view is currently displayed.

`internal/tui/components` and `internal/tui/views` are currently empty
(reserved namespaces); all view logic today lives directly in
`internal/tui/*.go`.

### internal/config — auth resolution

`Load(host)` resolves a token in order: `GLX_TOKEN`/`GITLAB_TOKEN` env vars →
`~/.config/glx/config.yml` → `~/.config/glab-cli/config.yml` for the given
host (reuses `glab`'s login). `DefaultHost` is pinned to
`gitlab.infr.zglbl.net`.

## Testing conventions

Tests are colocated (`foo.go` / `foo_test.go`) per package. `internal/cache`
and `internal/watch` tests inject `time.Now()` explicitly rather than
sleeping, since `Cache`/`Registry` take `now` as a parameter — follow this
pattern instead of adding real sleeps when testing TTL/time-based logic.
