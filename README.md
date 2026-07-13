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
├── completion
├── agent
│   ├── configure
│   └── <query>
├── summon <space-id>
├── review <pr> [space-id]
├── portal
│   ├── init <space-id> [portal-id]
│   ├── attach ssh|ec2 <space-id> ...
│   ├── configure <space-id> [portal-id]
│   ├── drivers
│   ├── doctor <space-id> [portal-id]
│   ├── list [space-id]
│   ├── status <space-id> [portal-id]
│   ├── inspect <space-id> [portal-id]
│   ├── auth status|login|inherit|revoke <space-id> [portal-id]
│   ├── up|sync|shell|exec|summon|logs <space-id> [portal-id]
│   └── down|detach|destroy <space-id> [portal-id]
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
  -k ticket \
  -s ~/notes/ticket-482.md \
  -e api \
  -e web:develop \
  -r api:main \
  --summon codex

# Optional: configure the natural-language agent
stave agent configure
stave agent "create ticket-482 from ~/notes/ticket-482.md with api editable, web as a reference, and summon codex"

# Inspect the workspace
stave space status ticket-482

# Fetch remotes, refresh references, report edit drift
stave space sync ticket-482
```

## Reviewing a pull request

`stave review` collapses the review setup into one command: it registers the
repository if needed, fetches the PR head (`refs/pull/N/head`, so fork PRs
work too), checks it out as an editable worktree whose drift is measured
against the PR's base branch, and writes the PR's metadata (title, author,
size, checks, description) to `spec/pr-<N>.md` via the `gh` CLI when
available.

```bash
# From a URL, owner/repo#N, or a registered repo name
stave review https://github.com/owner/repo/pull/123
stave review owner/repo#123 my-review-space

# Straight into an agent session (the spec primes it with the PR context)
stave review owner/repo#123 --summon claude

# Pull in sibling repos as read-only context
stave review owner/repo#123 -r other-repo --summon claude
```

Every review space ships with an embedded `pr-teach` skill (installed at
`.claude/skills/pr-teach/` inside the space), a guided review-comprehension
loop. Summoning Claude in a review space launches straight into it — no
setup and no personal skills required. Pass `--prompt` to launch with
something else instead.

Inside the space, `git diff origin/<base>...HEAD` is the full PR diff and
`stave space status` shows the PR's size as ahead/behind drift. Review
skills (for example a PR walkthrough skill) find everything they need in
`spec/` and the checked-out worktree.

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

### `stave agent`

Your agentic paraclete for Stave: it reads your workspace state, proposes safe operations, and incants validated plans when allowed.

| Command | Description |
|---------|-------------|
| `stave agent configure` | Interactively choose provider/model and configure the API key |
| `stave agent <query>` | Ask the agent to plan Stave operations from natural language |

Agent query flags:

| Flag | Meaning |
|------|---------|
| `--provider` | Override configured provider (`openai` or `anthropic`) |
| `--model` | Override configured model |
| `--incant` | Execute the validated plan without prompting |
| `--no-incant` | Always print the plan only |
| `--json` | Emit machine-readable JSON |

By default, interactive terminals show the typed plan and equivalent `stave ...` commands, then prompt before execution. Non-interactive runs print the plan only unless `--incant` is set or `agent.autoIncant: true` is configured. `--no-incant` always keeps the run plan-only.

The agent uses provider-native tool calls, not a free-form JSON response. OpenAI and Anthropic both receive the same Stave tool catalog:

| Tool | Behavior |
|------|----------|
| `stave_repos_list` | Read registered repos during planning |
| `stave_space_status` | Read one existing space during planning |
| `stave_repos_sync` | Queue repo cache sync for confirmation |
| `stave_space_sync` | Queue space sync for confirmation |
| `stave_space_create` | Queue new space creation for confirmation |
| `stave_space_add` | Queue adding a repo to an existing space for confirmation |
| `stave_summon` | Queue launching Codex, Claude Code, or Cursor Agent in a space |
| `stave_explain_unsupported` | Record unsupported/destructive requests as notes |
| `stave_finish` | Finish planning with a summary, notes, and warnings |

Read-only tools can run immediately while the model is planning. Mutating tools never apply changes inside the model loop; they only create a validated operation plan that Stave executes after confirmation, `--incant`, or `agent.autoIncant: true`. Summoning is interactive, so `--json` and non-interactive executions include the summon command but skip the launch.

Destructive operations such as archive, destroy, repo removal, reset, delete, push, PR creation, issue tracker updates, and arbitrary shell commands are intentionally not executable by the agent in v1.

`stave agent configure` stores API keys in macOS Keychain when available. If Keychain is unavailable, config stores an environment-variable reference such as `env:OPENAI_API_KEY` or `env:ANTHROPIC_API_KEY`. Stave does not write plaintext API keys to `config.yaml` and does not edit shell startup files.

Current built-in model defaults are `gpt-5.5` for OpenAI and `claude-opus-4-7` for Anthropic.

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
| `--summon` | `create` | Launch `codex`, `claude`, or `cursor` after the space is created |
| `--edit`, `-e` / `--reference`, `-r` | `add` | Mode (exactly one required) |
| `--base`, `-b` | `add` | Base branch/ref for edits, or ref for references |
| `--branch` | `add` | Branch name for editable repos |
| `--no-fetch` | `add` | Skip fetching the bare repo before adding |
| `--references-only` | `sync` | Only sync reference worktrees |
| `--force` | `archive`, `destroy` | Proceed despite dirty edit worktrees |
| `--dry-run` | `create`, `add`, `destroy` | Print Git operations without changing state |

When `--spec` points at a file, it is copied under `spec/` with its original basename. When it points at a directory, the directory contents are copied into `spec/`. The manifest records `specPath: spec`.

### `stave summon`

| Command | Description |
|---------|-------------|
| `stave summon <space-id> --with codex` | Start Codex in the space root |
| `stave summon <space-id> --with claude` | Start Claude Code in the space root |
| `stave review <pr>` | One-step PR review space: fetch the PR head, check it out, record metadata under `spec/` |
| `stave review <pr> --summon claude` | Same, then launch Claude Code in the review space |
| `stave review <pr> -r <repo> --summon claude --prompt "/skill"` | Add read-only reference repos and launch straight into a review skill |
| `stave summon <space-id> --with cursor` | Start Cursor Agent in the space root |

Summoned agents always launch from `agent-work/<space-id>`, not from an individual repo. That gives them the manifest, generated `AGENTS.md`, copied specs, editable top-level repos, and `references/` context in one working directory.

`--print-command` prints the launch command instead of running it. Non-interactive terminals also print instead of launching. `cursor` maps to the Cursor Agent CLI (`cursor-agent`), not the Cursor GUI editor.

### `stave portal`

Attach execution environments to an existing Stave space without changing the
space as the source of truth.

| Command | Description |
|---------|-------------|
| `stave portal init container <space-id>` | Record a Stave-owned Docker portal |
| `stave portal init devcontainer <space-id>` | Record a devcontainer portal |
| `stave portal attach ssh <space-id> <host>` | Attach an existing SSH host |
| `stave portal attach ec2 <space-id> <instance-id> --region <region>` | Attach an existing EC2 instance |
| `stave portal status <space-id> --json` | Emit stable portal status JSON |
| `stave portal summon <space-id> --with codex --mode print` | Print the in-portal agent launch command |

Local login credentials are never copied silently. Use `stave portal auth
login` to authenticate inside the portal target, or explicit `auth inherit`
methods when you really want inherited auth behavior.

See [`docs/portal.md`](docs/portal.md) for a practical portal quickstart and
command guide.

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
agent:
  defaultProvider: openai
  autoIncant: false
  providers:
    openai:
      model: gpt-5.5
      apiKeyRef: keychain:stave/agent/openai
    anthropic:
      model: claude-opus-4-7
      apiKeyRef: env:ANTHROPIC_API_KEY
summon:
  default: codex
  commands:
    codex: codex
    claude: claude
    cursor: cursor-agent
```

- **`defaultBase`** — fallback ref when a repo or `--edit` / `--reference` spec omits a branch.
- **`defaultBranch`** (per repo) — detected on `repos add` when possible; overrides `defaultBase` for that repo.
- **`agent.defaultProvider`** — provider used by `stave agent` unless `--provider` is set.
- **`agent.autoIncant`** — when true, `stave agent` executes validated plans without prompting or requiring `--incant`; `--no-incant` still wins.
- **`agent.providers.*.apiKeyRef`** — secret reference; either `keychain:stave/agent/<provider>` or `env:<NAME>`.
- **`summon.default`** — summoner used when `stave summon` omits `--with`.
- **`summon.commands.*`** — command names or paths for `codex`, `claude`, and `cursor`.

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

Released under the [MIT License](LICENSE). Copyright (c) 2026 Cloud Gatherer Labs LLC.
