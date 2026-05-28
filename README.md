# Stave

CLI for managing **agent workspaces** backed by shared bare Git repositories. Register upstream repos once, then spin up isolated **spaces** with editable worktrees and read-only reference checkouts—each tracked in a manifest and documented for tooling via `AGENTS.md`.

## Why Stave

Coding agents work best in a dedicated directory with clear rules: what is editable, what is read-only context, and how branches relate to upstream. Stave automates that layout with Git worktrees instead of ad-hoc clones:

- **Bare repo cache** — one local mirror per registered remote; fetch once, reuse across spaces.
- **Spaces** — per-task workspaces under `agent-work/` with `.stave.yaml` manifest.
- **Edit worktrees** — top-level folders on branches like `stave/<space-id>/<repo>`.
- **Reference worktrees** — detached checkouts under `references/` for context only.
- **Lifecycle** — sync, status (dirty / ahead-behind), archive, destroy.

## Install

**Releases** (macOS, Linux, Windows): download the archive for your platform from [GitHub Releases](https://github.com/Nurozen/stave/releases), extract `stave`, and put it on your `PATH`.

**From source** (Go 1.25+):

```bash
go install github.com/Nurozen/stave/cmd/stave@latest
```

## Command tree

```text
stave
├── setup
├── repos
│   ├── add <name> <url>
│   ├── list
│   ├── sync [name]
│   └── remove <name>
└── space
    ├── init <space-id>
    ├── create <space-id>
    ├── add <space-id> <repo>
    ├── sync <space-id>
    ├── status <space-id>
    ├── archive <space-id>
    └── destroy <space-id>
```

Workspace commands live under **`stave space`** (not at the top level).

## Quick start

```bash
# Create ~/stave, bare-repos/, agent-work/, and ~/.config/stave/config.yaml
stave setup

# Register remotes (clones a bare mirror locally)
stave repos add api https://github.com/you/api.git
stave repos add web https://github.com/you/web.git

# Create a space with editable + reference repos in one step
stave space create ticket-482 \
  --kind ticket \
  --spec ~/notes/ticket-482.md \
  --edit api \
  --edit web:develop \
  --reference api:main

# Inspect the workspace
stave space status ticket-482

# Fetch remotes, refresh references, report edit drift
stave space sync ticket-482
```

## Layout

Default paths (overridable in config):

| Path | Purpose |
|------|---------|
| `~/stave/bare-repos/<name>.git` | Bare mirrors of registered remotes |
| `~/stave/agent-work/<space-id>/` | Agent workspace root |
| `~/.config/stave/config.yaml` | Stave configuration |

A typical space:

```text
agent-work/ticket-482/
├── .stave.yaml          # manifest (repos, modes, branches, refs)
├── AGENTS.md            # instructions for agents (generated)
├── spec/                # optional spec file or tree (if --spec was used)
├── api/                 # edit worktree
├── web/                 # edit worktree
└── references/
    └── api/             # detached reference worktree
```

## Commands

Global flag on all commands: `--config <path>` (default `~/.config/stave/config.yaml`).

### `stave setup`

| Command | Description |
|---------|-------------|
| `stave setup` | Create root directories and write config |

### `stave repos`

| Command | Description |
|---------|-------------|
| `stave repos add <name> <url>` | Clone bare mirror and register |
| `stave repos list` | List registered repos |
| `stave repos sync [name]` | `git fetch --all --prune` on bare mirror(s) |
| `stave repos remove <name>` | Unregister (does not delete bare cache) |

Flags: `--dry-run` on `add`.

### `stave space`

| Command | Description |
|---------|-------------|
| `stave space init <space-id>` | Empty space (manifest + `AGENTS.md`) |
| `stave space create <space-id>` | `init` plus `--edit` / `--reference` repos |
| `stave space add <space-id> <repo>` | Add one repo (`--edit` or `--reference`) |
| `stave space sync <space-id>` | Fetch, update references, report edit drift |
| `stave space status <space-id>` | Manifest, dirty state, ahead/behind |
| `stave space archive <space-id>` | Remove worktrees; move space to `.archive/` |
| `stave space destroy <space-id>` | Remove worktrees and delete space directory |

Space flags:

| Flag | Commands | Meaning |
|------|----------|---------|
| `--kind`, `-k` | `init`, `create` | Label the space (e.g. `ticket`, `spike`, `audit`) |
| `--spec`, `-s` | `init`, `create` | Copy a spec file or directory into `spec/` |
| `--edit`, `-e` | `create` | Editable worktree from base ref (`repo` or `repo:base`; repeatable) |
| `--reference`, `-r` | `create` | Detached reference worktree (`repo` or `repo:ref`; repeatable) |
| `--edit`, `-e` / `--reference`, `-r` | `add` | Mode (exactly one required) |
| `--base`, `-b` | `add` | Base branch/ref for edits, or ref for references |
| `--branch` | `add` | Branch name for editable repos |
| `--no-fetch` | `add` | Skip fetching the bare repo before adding |
| `--references-only` | `sync` | Only sync reference worktrees |
| `--force` | `archive`, `destroy` | Proceed despite dirty edit worktrees |
| `--dry-run` | `create`, `add`, `destroy` | Print Git operations without changing state |

When `--spec` points at a file, it is copied under `spec/` with its original basename. When it points at a directory, the directory contents are copied into `spec/`. The manifest records `specPath: spec`.

## Configuration

Example `~/.config/stave/config.yaml`:

```yaml
root: ~/stave
bareReposDir: ~/stave/bare-repos
agentWorkDir: ~/stave/agent-work
defaultBase: main
repos:
  api:
    name: api
    url: https://github.com/you/api.git
    bareRepoPath: ~/stave/bare-repos/api.git
    defaultBranch: main
```

- **`defaultBase`** — fallback ref when a repo or `--edit` / `--reference` spec omits a branch.
- **`defaultBranch`** (per repo) — detected on `repos add` when possible; overrides `defaultBase` for that repo.

Editable branches default to `stave/<space-id>/<repo>` unless `--branch` is set.

## Development

```bash
go test -race ./...
go build -o stave ./cmd/stave
```

Lint: `golangci-lint run` (see `.golangci.yml`).

Releases are built with [GoReleaser](https://goreleaser.com/) (`.goreleaser.yml`); signed macOS binaries when release secrets are configured.

## Default branch

This repository uses **`weirwood`** as its default branch (the staff base), not `main`.

## License

License not yet specified. See repository settings or add a `LICENSE` file before distributing.
