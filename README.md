# glx

An interactive terminal UI for GitLab merge requests and pipelines — built for
self-hosted instances where the web UI feels clunky and `glab` isn't
interactive enough.

`glx` is keyboard-driven: browse merge requests grouped by project, review
diffs with inline comments, approve/merge/rebase, watch CI pipelines live, and
get notified when they change — all without leaving the terminal.

## Features

- **Merge request list** grouped by project, across three scopes
  (Reviews / Authored / Assigned), with live filtering and pagination.
  - Status glyphs at a glance: pipeline result, approval state (green check =
    approved by *you*, plain check = approved by others), plus `draft`,
    `conflict`, `rebase`, `blocked`, and `threads` flags.
  - A **recently-merged** section (last 12h) on the Reviews and Authored tabs.
- **MR detail** view with description (rendered markdown), approvals,
  discussion threads, and pipeline summary.
- **Diff viewer** with per-file navigation, syntax highlighting, and
  **inline comments** anchored to the right line.
- **Pipeline view**: jobs grouped by stage, with **auto-refresh every 15s**
  while running and a discreet change alert.
- **Pipeline watch list**: mark any pipeline with `w`, monitor several at once
  from the `W` view; a single background poller alerts you (with a terminal
  bell) when a watched pipeline passes or fails — from any screen.
- **Write actions**: approve/unapprove, merge, auto-merge (merge when pipeline
  succeeds), rebase, comment, retry/cancel jobs.
- **Open in browser** (`o`) for whatever's in focus.
- Short-TTL caching keeps navigation snappy; `r` forces a refresh.

## Install

Requires Go 1.26+.

```sh
go install github.com/hamkens/glx/cmd/glx@latest
```

This installs the `glx` binary to `$(go env GOPATH)/bin` (ensure it's on your
`PATH`). Or build from a clone:

```sh
git clone https://github.com/hamkens/glx
cd glx
go build -o glx ./cmd/glx
```

## Authentication

`glx` resolves a GitLab host and token in this order (first hit wins):

1. `GLX_TOKEN` / `GITLAB_TOKEN` environment variables
2. `glx`'s own config: `~/.config/glx/config.yml`
3. `glab`'s config (`~/.config/glab-cli/config.yml`) for the active host

If you already use [`glab`](https://gitlab.com/gitlab-org/cli), `glx` reuses its
token automatically — no extra setup. Otherwise create
`~/.config/glx/config.yml`:

```yaml
host: gitlab.example.com
token: glpat-xxxxxxxxxxxxxxxxxxxx   # needs the `api` scope for write actions
```

## Usage

```sh
glx                 # launch the TUI
glx --host HOST     # target a specific GitLab host
glx --check         # verify connectivity and exit (no TUI)
```

Press `?` at any time for a context-aware keybinding overlay.

### Keybindings

**Global**

| Key | Action |
| --- | --- |
| `?` | toggle help overlay |
| `o` | open current item in browser |
| `w` | watch / unwatch the focused pipeline |
| `W` | open the watch list |
| `r` | refresh (bypass cache) |
| `q` / `ctrl+c` / `ctrl+d` | quit |

**Merge request list**

| Key | Action |
| --- | --- |
| `enter` | open merge request |
| `←` / `→` (or `tab`) | switch scope |
| `/` | filter |
| `↑` / `↓` | move (auto-loads more) |
| `a` | approve / unapprove |
| `M` | merge now (confirm) |
| `A` | auto-merge when pipeline passes |
| `b` | rebase onto target |
| `d` | open diff |
| `p` | open pipeline |

**MR detail** — `a` approve, `M` merge, `A` auto-merge, `b` rebase, `c` comment,
`d` diff, `p` pipeline, `esc`/`⌫` back.

**Diff** — `←`/`→` switch file, `↑`/`↓` move line cursor, `c` comment on the
current line, `esc`/`⌫` back.

**Pipeline** — `↑`/`↓` move between jobs, `enter` view job log, `R` retry job,
`x` cancel job, `r` refresh now. Active pipelines auto-refresh every 15s.

**Job log** — `↑`/`↓` scroll, `g`/`G` top/bottom, `t` toggle tail, `esc`/`⌫`
back.

**Watch list** — `enter` open, `w`/`x` unwatch, `r` refresh all, `esc`/`⌫` back.

## Architecture

- `cmd/glx` — entry point, flag parsing, auth + connectivity.
- `internal/config` — host/token resolution.
- `internal/gitlab` — dual-backend client: GraphQL for read/list-heavy views
  (one query replaces REST N+1 round trips), REST for writes and diff payloads.
- `internal/cache` — generic short-TTL cache.
- `internal/watch` — in-memory registry + change detection for watched
  pipelines.
- `internal/browser` — cross-platform URL opener.
- `internal/tui` — the [Bubble Tea](https://github.com/charmbracelet/bubbletea)
  views and root model.

Built with Bubble Tea, Lip Gloss, Glamour, and Chroma.
