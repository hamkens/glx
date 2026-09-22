# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`glx` is a Bubble Tea terminal UI for GitLab merge requests and GitHub pull
requests, plus their CI (module `github.com/hamkens/glx`). It targets
self-hosted GitLab instances, github.com and GitHub Enterprise. Entry point:
`cmd/glx/main.go`.

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

Three layers: `internal/forge` (the provider-neutral contract),
`internal/gitlab` + `internal/github` (two implementations of it), and
`internal/tui` (Bubble Tea views, which depend only on the interface). They are
wired together in `cmd/glx/main.go` after `internal/config` resolves
host/provider/token.

### internal/forge — the contract everything else speaks

`forge.Forge` (`forge.go`) is the single interface the TUI consumes. Both
backends assert conformance at compile time with
`var _ forge.Forge = (*Client)(nil)`. Adding a method means implementing it in
both backends — or returning `*forge.UnsupportedError` from the one that can't,
and adding a flag to `Capabilities` so the UI can hide the action rather than
surface an error.

The four things that keep the TUI provider-agnostic:

- **Neutral domain types** (`types.go`): `Change`/`ChangeDetail` instead of
  MR/PR, `Discussion`, `FileDiff`, `Pipeline`, `Job`. IDs the user sees
  (GitLab iid / GitHub number) are `string`; internal pipeline and job IDs are
  `int64`, which GitHub's snowflake-sized run IDs need.
- **Normalized enums** (`status.go`): `Status`, `State`, `MergeState`,
  `ReviewState`. Backends translate their own spellings once, on the way in
  (`ParseGitLabStatus`, `ParseGitHubStatus(status, conclusion)`); no view ever
  compares a raw provider string. Prefer the predicates (`Status.Active()`,
  `MergeState.Mergeable()`, `MergeState.CIPending()`, `ReviewState.Pending()`)
  over enumerating cases, so a newly added enum value degrades sanely.
  `MergeStateUnknown` means "no known blocker", not "blocked" — GitHub often
  reports nothing.
- **`Vocabulary`** (`vocabulary.go`): every user-visible provider noun/verb
  ("merge request"/"pull request", `!`/`#`, "pipeline"/"workflow run",
  "rebase"/"update branch", "threads"/"conversations"). Views hold a
  `forge.Vocabulary` from `forge.Vocab(client.Provider())` and interpolate it;
  they never branch on the provider. New user-facing wording that differs
  between hosts belongs here, not in an `if provider == …`.
- **`Capabilities`**: `AutoMerge`, `Unapprove`, `CancelJobIsRunWide`,
  `DraftToggle`. Key handlers check these before acting and flash a clear
  message; `help.go` omits unsupported keys.

`forge.WithForceRefresh(ctx)` / `forge.Forced(ctx)` is the cache-bypass marker
backing the `r` key end-to-end.

**`Fleet`** (`fleet.go`) is itself a `Forge`, wrapping N connections so one
session can show GitLab and GitHub in one list. `cmd/glx/main.go` always wraps
the clients in one, so the TUI never learns how many hosts are behind it.

- Reads fan out concurrently and merge (`Changes` sorted newest-updated first,
  `MergedSince` newest-merged first). Pagination collapses: `pageSize` applies
  per host and `HasNextPage` is always false, because one opaque cursor can't
  address several hosts.
- Writes and per-change reads route to the owning member, resolved from the
  `Change.Host` the fleet stamps on every row, falling back to a repo→host index
  for call paths that carry only a repo (watch poller, pipeline drill-down). An
  unroutable repo is an error, never a guess — guessing means writing to the
  wrong forge.
- Per-host failures are isolated: `setHealth` records them, `Degraded()` names
  them for the status bar, and only an all-hosts-down read returns an error.
  `CurrentUser` succeeds if any one host authenticates.
- `Provider()` reports `ProviderMixed` for a cross-product fleet, which selects
  neutral vocabulary for fleet-wide chrome; per-row wording comes from
  `VocabFor(change)` / `CapsFor(change)`. `Capabilities()` intersects members
  (except `CancelJobIsRunWide`, which ORs — a warning that applies anywhere must
  show).
- A single-member fleet is transparent: `Single()` reports it, and the views skip
  every multi-host affordance. Keep it that way — the one-host case is still the
  common one.

### internal/gitlab — GitLab backend

`Client` (`client.go`) talks to one GitLab host over two transports and picks
whichever is cheaper for a given view:

- **GraphQL** (`graphql.go`) for read/list-heavy views — MR list (`mr.go`) and
  MR detail (`mr_detail.go`) — where one query replaces REST N+1 round trips.
- **REST** (`xanzy/go-gitlab`) for writes (`mr_actions.go`: approve, merge,
  rebase, comment) and diff payloads (`mr_diff.go`, `mr_diff_comment.go`),
  where endpoints are stable and well documented.

Three per-read-type TTL caches (`internal/cache`, 30s) live on `Client`:
`mrListCache`, `mrDetailCache`, `mrDiffCache`. When adding a new cached read,
follow this pattern: check `forced(ctx)`, otherwise `Get`/`Set` around the API
call.

GraphQL scopes (`forge.ScopeAssigned`/`ScopeReviewer`/`ScopeAuthored`) map to
`currentUser` connection fields via `connectionField()`.

### internal/github — GitHub backend

`Client` (`client.go`) mirrors the GitLab client's shape (same three caches,
same force-refresh handling) over `google/go-github` REST plus raw GraphQL:

- **REST** for list/detail/diff (`pr.go`, `pr_detail.go`, `pr_diff.go`),
  writes (`pr_actions.go`) and Actions runs/jobs (`pipeline.go`).
- **GraphQL** (`graphql.go`) only for what REST cannot do: `enablePullRequestAutoMerge`,
  merge-queue enqueue, and the draft/ready toggle.

GitHub-specific things worth knowing before editing:

- Scopes are issue-search queries (`is:open is:pr review-requested:@me`, …),
  not typed connections, so pagination is search pagination.
- Every list query carries `archived:false` (the `notArchived` const in `pr.go`),
  so PRs in archived repos never appear: they can't be approved, merged or
  re-run, and archiving a repo leaves its open PRs open forever. Keep the filter
  in the query rather than dropping rows after the fetch, which would return
  short pages and make "load more" skip real work.
- `mergeable_state` is overloaded ("blocked" covers missing approvals, failing
  checks and branch rules alike); it's disambiguated against `reviewDecision`
  and the check runs before becoming a `forge.MergeState`.
- Several write endpoints answer `202 Accepted` with no body; `ignoreAccepted()`
  treats that as success.
- There is no per-job cancel, so `CancelJob` cancels the run and
  `CancelJobIsRunWide` is true.
- GHE base URLs (`https://HOST/api/v3/`, `https://HOST/api/graphql`) are set up
  in `New` when the host isn't github.com.

### internal/tui — Bubble Tea views

`rootModel` (`app.go`) is the router. It holds one sub-model per screen
(`mrListModel`, `detailModel`, `diffModel`, `pipelineModel`, `jobLogModel`,
`watchModel`) and a `view` enum selecting the active one; `enter` drills in,
`esc`/backspace steps back. Sub-models each own their file: `mrlist.go`,
`mrdetail.go`, `diffview.go`, `pipelineview.go`, `joblog.go`, `watchview.go`.

Every model holds `client forge.Forge` and, where it renders provider wording,
`vocab forge.Vocabulary` and `caps forge.Capabilities` resolved in its
constructor. Nothing in this package imports `internal/gitlab` or
`internal/github`.

With more than one host connected, wording can't be per-app any more:

- `mrListModel` holds a `fleet *forge.Fleet` (non-nil only when it spans hosts)
  and resolves each row through `vocabFor(mr)` / `capsFor(mr)`. Anything acting
  on a *selected row* must use those, not `m.vocab` / `m.caps` — otherwise a
  GitHub row offers unapprove, or a GitLab row renders `#42`.
- Views below the list work on one repo, so they resolve that repo's host once
  via `vocabForRepo` / `capsForRepo` (`provider.go`) instead of repeating the
  fleet type assertion.
- Row grouping keys on `(host, repo)`, since two hosts can serve the same repo
  path; the host rides on the group header rather than costing every row a
  column.

Cross-cutting root-level concerns to check when touching `app.go`:
- `inputActive()` — suppresses global single-key shortcuts (like `?`) while
  a view has a text input capturing keystrokes (comment mode, filter mode).
- `currentURL()` / `focusedPipeline()` — resolve "what's focused right now"
  across views, backing the global `o` (open in browser), `w` (watch), and
  pipeline-action keys so those don't need per-view duplication.
- `watchReturn` / `pipeReturn` — remember which view to pop back to when
  leaving the watch/pipeline/job-log screens, since those are reachable from
  more than one place.

`internal/watch` is an in-memory `Registry` of pipelines marked with `w`, keyed
by `(repo, pipelineID)`. The root model runs a single background poller
(`watchInterval`, 15s) that refreshes active entries and diffs status to raise
change alerts — this lives independent of whichever view is displayed.

`internal/tui/components` and `internal/tui/views` are currently empty
(reserved namespaces); all view logic today lives directly in
`internal/tui/*.go`.

### internal/config — provider and auth resolution

`LoadAll(hosts, provider)` resolves one `Config` per host to connect to:
explicit `--host` flags (repeatable, comma-separated accepted) or `GLX_HOST`
win, else every host under `hosts:` in glx's config, else the single-host
fallback. Listing a host is enough to select it — a tokenless entry still
resolves through the glab/gh fallbacks. A host whose token resolves to nothing
is returned in `errs` and skipped, not fatal; only an empty result errors.
A per-host `provider:` key pins a backend.

`Load(host, provider)` resolves host, provider and token. The provider comes
from `DetectProvider(host)` (github.com, `*.ghe.*`, `*.ghe.com` and `github.*`
→ GitHub; everything else → GitLab) unless the `--provider` flag supplied one,
which always wins — self-hosted GitLab instances have arbitrary names, so
detection deliberately defaults to GitLab.

Token order, first hit wins:

- GitLab: `GLX_TOKEN`/`GITLAB_TOKEN` → `~/.config/glx/config.yml` →
  `~/.config/glab-cli/config.yml` (reuses `glab`'s login)
- GitHub: `GLX_TOKEN`/`GH_TOKEN`/`GITHUB_TOKEN` → `~/.config/glx/config.yml` →
  `~/.config/gh/hosts.yml` (including the per-user `users:` section newer `gh`
  versions write) → `gh auth token`, because `gh` stores tokens in the OS
  keyring by default and then writes no token to `hosts.yml` at all. That last
  step shells out, so it's indirected through the `ghCLIToken` variable, which
  tests stub via `stubGhCLI` to stay hermetic.

The vendor env vars are provider-scoped, so having both set never crosses the
wires. `glx`'s own config accepts either single-host top-level keys or a
`hosts:` map; the top-level token only applies to the host it names.
`DefaultHost` is pinned to `gitlab.infr.zglbl.net`.

## Testing conventions

Tests are colocated (`foo.go` / `foo_test.go`) per package. `internal/cache`
and `internal/watch` tests inject `time.Now()` explicitly rather than
sleeping, since `Cache`/`Registry` take `now` as a parameter — follow this
pattern instead of adding real sleeps when testing TTL/time-based logic.

Backend tests hit no network: they exercise the translation layer (status
parsing, merge-state disambiguation, payload shaping) on fixture data, which is
where the provider-specific bugs live. `internal/config` tests point
`XDG_CONFIG_HOME` at `t.TempDir()` and clear every token env var via
`t.Setenv`, so resolution order is tested in isolation.
