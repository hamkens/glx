# glx

An interactive terminal UI for **GitLab merge requests** and **GitHub pull
requests** — built for the instances where the web UI feels clunky and `glab` /
`gh` aren't interactive enough.

`glx` is keyboard-driven: browse changes grouped by project, review diffs with
inline comments, approve/merge/update-branch, watch CI live, and get notified
when it changes — all without leaving the terminal.

One binary talks to both products. The backend is picked from the host name
(`github.com`, `*.ghe.com` and `github.*` hosts → GitHub, everything else →
GitLab) and can be forced with `--provider`. The UI adopts the host's wording,
so the same screens read "merge request", "!42", "pipeline" and "rebase" on
GitLab, and "pull request", "#42", "workflow run" and "update branch" on
GitHub.

## Features

- **Change list** grouped by project, across three scopes
  (Reviews / Authored / Assigned), with live filtering and pagination.
  - Status glyphs at a glance: CI result, approval state (green check =
    approved by *you*, plain check = approved by others), plus `draft`,
    `conflict`, `rebase`/`update`, `blocked`, `changes` and `threads` flags.
  - A **recently-merged** section (last 12h) on the Reviews and Authored tabs.
- **Detail** view with description (rendered markdown), approvals, comment
  threads, and a CI summary.
- **Diff viewer** with per-file navigation, syntax highlighting, and
  **inline comments** anchored to the right line.
- **Pipeline view** (GitLab pipeline / GitHub Actions run): jobs grouped by
  stage, with **auto-refresh every 15s** while running and a discreet change
  alert.
- **Watch list**: mark any pipeline or run with `w`, monitor several at once
  from the `W` view; a single background poller alerts you (with a terminal
  bell) when a watched one passes or fails — from any screen.
- **Write actions**: approve/unapprove (a review dismissal on GitHub), merge,
  auto-merge (merge when checks pass), rebase / update branch, draft toggle,
  comment, retry job, cancel.
- **Open in browser** (`o`) for whatever's in focus.
- Short-TTL caching keeps navigation snappy; `r` forces a refresh.

### Provider differences

Where GitHub has no equivalent for a GitLab concept, `glx` says so rather than
failing with an opaque API error:

| Action | GitLab | GitHub |
| --- | --- | --- |
| Unapprove | withdraws the approval | dismisses your own approving review |
| `b` (bring branch up to date) | rebase | update-branch merge |
| Auto-merge | merge when pipeline succeeds / merge train | auto-merge, or the merge queue when enabled |
| `x` (cancel) | cancels the single job | cancels the whole run — GitHub has no per-job cancel |
| Approval counts | shows `n/m` from approval rules | shows review state; GitHub reports no required count |

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

`glx` resolves a host and token in this order (first hit wins):

**GitLab hosts**

1. `GLX_TOKEN` / `GITLAB_TOKEN`
2. `glx`'s own config: `~/.config/glx/config.yml`
3. `glab`'s config (`~/.config/glab-cli/config.yml`) for that host

**GitHub hosts**

1. `GLX_TOKEN` / `GH_TOKEN` / `GITHUB_TOKEN`
2. `glx`'s own config: `~/.config/glx/config.yml`
3. `gh`'s config (`~/.config/gh/hosts.yml`) for that host
4. `gh auth token`, since `gh` stores tokens in the OS keyring by default

If you already use [`glab`](https://gitlab.com/gitlab-org/cli) or
[`gh`](https://cli.github.com), `glx` reuses the existing login automatically —
no extra setup. Otherwise create `~/.config/glx/config.yml`, either for a single
host:

```yaml
host: gitlab.example.com
token: glpat-xxxxxxxxxxxxxxxxxxxx   # needs the `api` scope for write actions
```

…or for several:

```yaml
hosts:
  gitlab.example.com:
    token: glpat-xxxxxxxxxxxxxxxxxxxx   # `api` scope
  github.com:
    token: ghp_xxxxxxxxxxxxxxxxxxxx     # `repo` scope (+ `workflow` for job re-runs)
```

The default host is the GitLab instance compiled into `internal/config`; set
`GLX_HOST` or pass `--host` to change it.

## Multiple hosts at once

Every host under `hosts:` is connected in one session, and their changes appear
in a **single merged list** sorted newest-updated first. Listing a host is
enough to select it — the `token:` key is optional, since the token can still
come from your existing `glab` or `gh` login:

```yaml
hosts:
  gitlab.example.com:
  github.com:
```

```sh
glx                                            # every configured host
glx --host github.com                          # just one (flags override the file)
glx --host github.com --host gitlab.example.com
glx --host github.com,gitlab.example.com       # comma form, for shell aliases
```

In a merged list each row keeps its own host's wording — `!42` and "pipeline"
for GitLab rows, `#42` and "workflow run" for GitHub ones — and every action
routes to the host that owns the row. The host name is shown on each project
group header, and the status bar reads `2 hosts`.

A host that is unreachable or whose token expired does not blank the list: the
hosts that answered still render, and the status bar names the failed one
(`✘ github.com unreachable`). Press `r` to retry it. Only when *no* host answers
does `glx` fail outright. A per-host `provider:` key forces a backend for
installs whose names don't reveal the product:

```yaml
hosts:
  scm.internal.example:
    provider: github
```

## Usage

```sh
glx                          # launch the TUI against every configured host
glx --host github.com        # target a specific host (provider auto-detected)
glx --host ghe.corp.example --provider github   # force a backend
glx --check                  # verify connectivity per host and exit (no TUI)
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

**Change list** (merge requests / pull requests)

| Key | Action |
| --- | --- |
| `enter` | open the change |
| `←` / `→` (or `tab`) | switch scope |
| `/` | filter |
| `↑` / `↓` | move (auto-loads more) |
| `a` | approve / unapprove |
| `M` | merge now (confirm) |
| `A` | auto-merge when checks pass |
| `b` | rebase / update branch |
| `D` | toggle draft / ready |
| `d` | open diff |
| `p` | open pipeline |

**Detail** — `a` approve, `M` merge, `A` auto-merge, `b` rebase / update branch,
`D` draft toggle, `c` comment, `d` diff, `p` pipeline, `esc`/`⌫` back.

**Diff** — `←`/`→` switch file, `↑`/`↓` move line cursor, `c` comment on the
current line, `esc`/`⌫` back.

**Pipeline** — `↑`/`↓` move between jobs, `enter` view job log, `R` retry job,
`x` cancel (the whole run on GitHub), `r` refresh now. Active pipelines
auto-refresh every 15s.

**Job log** — `↑`/`↓` scroll, `g`/`G` top/bottom, `t` toggle tail, `esc`/`⌫`
back.

**Watch list** — `enter` open, `w`/`x` unwatch, `r` refresh all, `esc`/`⌫` back.

## Architecture

- `cmd/glx` — entry point, flag parsing, auth + connectivity.
- `internal/config` — host/provider/token resolution.
- `internal/forge` — the provider-neutral contract (`Forge` interface, neutral
  domain types, normalized status enums, per-provider vocabulary and
  capabilities). Everything above this layer is provider-agnostic.
- `internal/gitlab` — GitLab backend: GraphQL for read/list-heavy views
  (one query replaces REST N+1 round trips), REST for writes and diff payloads.
- `internal/github` — GitHub backend: REST for most reads and writes, GraphQL
  for what REST can't do (auto-merge, merge queue, draft toggle).
- `internal/cache` — generic short-TTL cache.
- `internal/watch` — in-memory registry + change detection for watched
  pipelines.
- `internal/browser` — cross-platform URL opener.
- `internal/tui` — the [Bubble Tea](https://github.com/charmbracelet/bubbletea)
  views and root model.

Built with Bubble Tea, Lip Gloss, Glamour, and Chroma.
