# Stave

<p align="center">
  <img src="docs/images/stave-summons-worktree-hero-concept-v4.png" alt="An arcane staff summoning a tree of isolated workspaces" width="100%">
</p>

CLI for managing **agent workspaces** backed by shared bare Git repositories. Register upstream repos once, then spin up isolated **spaces** with editable worktrees and read-only reference checkouts—each tracked in a manifest and documented for tooling via `AGENTS.md`.

<p align="center">
  <img src="docs/images/stave-guild-seal-staff-mark-concept-v5.png" alt="The Stave guild seal" width="240">
</p>

## Why Stave

Coding agents work best in a dedicated directory with clear rules: what is editable, what is read-only context, and how branches relate to upstream. Stave automates that layout with Git worktrees instead of ad-hoc clones:

- **Bare repo cache** — one local mirror per registered remote; fetch once, reuse across spaces.
- **Spaces** — per-task workspaces under `agent-work/` with `.stave.yaml` manifest.
- **Edit worktrees** — top-level folders on branches like `stave/<space-id>/<repo>`.
- **Reference worktrees** — detached checkouts under `references/` for context only.
- **Lifecycle** — sync, status (dirty / ahead-behind), archive, destroy.
- **Memory** — optional [ContextMarmot](https://github.com/Nurozen/context-marmot) dens
  attached per space (`stave memory`); never writes in-space `.marmot/` trees.

## How Stave works

<p align="center">
  <img src="docs/images/stave-system-staff-rooted-concept-v2.png" alt="A shared rooted repository fanning out into isolated workspaces, portals, and summoned agents" width="100%">
</p>

Stave treats each registered bare repository as a shared root. A space fans that
root into isolated editable worktrees and detached reference worktrees. Portals
can carry the prepared workspace into local containers or remote hosts before a
configured coding agent is summoned into it.

## Install

**Releases** (macOS, Linux, Windows): download the archive for your platform from [GitHub Releases](https://github.com/Nurozen/stave/releases), extract `stave`, and put it on your `PATH`.

**From source** (Go 1.25+):

```bash
go install github.com/Nurozen/stave/cmd/stave@latest
```

Run `stave version` to print the installed version, commit, and build date.

To let `stave space create` and `stave review` enter the new space in your
current shell, inscribe the matching shell integration once:

```bash
stave inscribe shell --zsh  # or --bash
```

Start a new shell or source the updated startup file to activate it. Zsh uses
`${ZDOTDIR:-$HOME}/.zshrc`; Bash uses `$HOME/.bashrc`. Bash login-shell setups
must source `.bashrc`, or you can select the file explicitly with `--rc`.

If your dotfiles are managed elsewhere, use the low-level, non-mutating form
in the appropriate startup file instead:

```bash
eval "$(command stave shell-init zsh)"  # use bash when appropriate
```

The wrapper is necessary because an executable cannot change its parent
shell's directory. Other Stave commands behave exactly as before. Re-running
`stave inscribe` safely updates its marked block without duplicating it.

On Windows, space creation requires permission to create the generated
`CLAUDE.md -> AGENTS.md` symlink; enable Developer Mode or run with equivalent
administrator symlink privileges.

## Command tree

```text
stave
├── setup
├── version
├── inscribe
│   └── shell --zsh|--bash
├── shell-init bash|zsh
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
│   ├── list [--verbose]
│   ├── sync [name]
│   ├── remove <name>
│   ├── describe <repo> [text]
│   ├── tethers <repo> [--json]
│   ├── tether <from> <to> [--strong|--weak|--edit|--reference]
│   └── forget <from> [to] [--all]
├── memory
│   ├── providers
│   ├── attach <space-id>
│   ├── status <space-id> [alias]
│   ├── list [space-id]
│   ├── sync <space-id>
│   ├── propose <space-id>
│   └── detach <space-id> [--keep|--destroy]
├── saga
│   ├── create <saga-id> [--memory ...] [--summon ...] [--no-learn]
│   ├── list [--json]
│   ├── status <saga-id> [--json]
│   ├── sync <saga-id>
│   ├── add <saga-id> <space-id> [--after <member-id>]
│   ├── remove <saga-id> <space-id>
│   ├── archive <saga-id>
│   └── destroy <saga-id> [--memory keep|destroy|contribute]
└── space
    ├── init <space-id>
    ├── create <space-id> [--memory ...] [--saga <saga-id> [--after ...]] [-c/--common] [--include-weak] [--no-learn]
    ├── add <space-id> <repo> [--no-learn]
    ├── sync <space-id>
    ├── status <space-id>
    ├── archive <space-id>
    ├── retarget <space-id> --repo <repo> --base <ref>
    └── destroy <space-id> [--memory keep|destroy|contribute]
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

# Stack a follow-up space on ticket-482's api branch
stave space create ticket-483 -e api:space:ticket-482
```

After the inscribed shell integration is loaded, the create command leaves the
current shell at `~/stave/agent-work/ticket-482/` after it completes.

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

After the inscribed shell integration is loaded, `stave review` enters the
review space root after setup (and after the summoned session exits when
`--summon` is used).

Every review space ships with an embedded `pr-teach` skill (installed at
`.claude/skills/pr-teach/` inside the space), a guided review-comprehension
loop. Summoning Claude in a review space launches straight into it — no
setup and no personal skills required. Pass `--prompt` to launch with
something else instead.

Inside the space, `git diff origin/<base>...HEAD` is the full PR diff and
`stave space status` shows the PR's size as ahead/behind drift. Review
skills (for example a PR walkthrough skill) find everything they need in
`spec/` and the checked-out worktree.


## Memory (ContextMarmot dens)

Stave can attach a **provider-agnostic memory store** to a space. The first provider
is **marmot**: it creates a central den under `$MARMOT_HOME` (not inside the space)
and writes **space-local MCP** configs so agents call `marmot serve --den <id>`.

Requires a dens-aware `marmot` on `PATH` (P1b+). Optional config:

```yaml
# ~/.config/stave/config.yaml (or --config)
memory:
  provider: marmot
  binary: marmot          # or absolute path
  default: false          # if true, space create attaches memory automatically
```

```bash
export MARMOT_HOME=~/.marmot   # dens root; embedded into MCP env when set
export PATH="/path/to/marmot/bin:$PATH"

stave memory providers                 # probe marmot binary / den support
stave memory attach ticket-482         # den create --no-pointer --json
stave memory status ticket-482
stave memory list
stave memory detach ticket-482 --keep     # keep den as durable residue; strip MCP
stave memory detach ticket-482 --destroy  # destroy den + strip MCP + routes
```

### Contracts (S2)

| Behavior | Detail |
|----------|--------|
| Attach argv | `marmot den create <id> --lifetime task --project <spacePath> --no-pointer --json` |
| Pointer | **Never** writes `.marmot-vault` into the space |
| Routes | Registers space root in `$MARMOT_HOME/routes.yml`; archive rewrites via `route set-project --from/--to` |
| MCP | Writes `.mcp.json`, `.cursor/mcp.json`, `.vscode/mcp.json`, `.codex/config.toml` with `serve --den <id>` (+ `MARMOT_HOME` when set) |
| Detach | Removes only the generated `context-marmot` MCP entries; preserves other servers |
| Destroy space | `--memory keep` (default) / `destroy` / `contribute` controls den fate |

See ContextMarmot [docs/dens.md](https://github.com/Nurozen/context-marmot/blob/main/docs/dens.md)
for dens layout, discovery order, and JSON schema fixtures.

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
├── CLAUDE.md -> AGENTS.md
├── spec/                # optional spec file or tree (if --spec was used)
├── api/                 # edit worktree; root AGENTS.md links api/AGENTS.md when present
├── web/                 # edit worktree
└── references/
    └── api/             # detached reference worktree
```

A typical saga. The saga space itself holds no edit worktrees — its members are
ordinary sibling spaces under `agent-work/`, joined by the roster in the saga's
manifest rather than by nesting:

```text
agent-work/checkout-rewrite/     # the saga space
├── .stave.yaml                  # manifest v2: member roster + after edges
├── AGENTS.md                    # coordinator instructions (generated)
├── CLAUDE.md -> AGENTS.md
├── .claude/
│   ├── settings.json            # member dirs as additionalDirectories
│   └── skills/stave-saga/       # embedded coordinator skill
├── .codex/config.toml           # member dirs as writable_roots
├── spec/                        # optional spec file or tree
└── references/                  # optional detached reference worktrees

agent-work/checkout-api/         # member space (sibling, not nested)
agent-work/checkout-web/         # member space, after: checkout-api
```

## Commands

Global flag on all commands: `--config <path>` (default `~/.config/stave/config.yaml`).

### `stave setup`

| Command | Description |
|---------|-------------|
| `stave setup` | Create root directories and write config |

### `stave version`

| Command | Description |
|---------|-------------|
| `stave version` | Print the Stave version, commit, and build date |

Release builds carry the values injected at link time; `go install`ed binaries
fall back to `runtime/debug` build info, and any missing field prints as
`unknown`.

### `stave inscribe`

| Command | Description |
|---------|-------------|
| `stave inscribe shell --zsh` | Add or update Stave's managed block in `${ZDOTDIR:-$HOME}/.zshrc` |
| `stave inscribe shell --bash` | Add or update Stave's managed block in `$HOME/.bashrc` |

Shell inscription accepts `--rc <path>` to target a different startup file and
`--dry-run` to inspect the target without changing it. Existing file content,
permissions, and symlinks are preserved. An exact legacy `shell-init` line is
migrated into the managed block.

### `stave shell-init`

| Command | Description |
|---------|-------------|
| `stave shell-init zsh` | Print the zsh wrapper that enables automatic space entry |
| `stave shell-init bash` | Print the bash wrapper that enables automatic space entry |

`shell-init` only prints integration code; it does not edit a startup file.

### `stave repos`

| Command | Description |
|---------|-------------|
| `stave repos add <name> <url>` | Clone bare mirror and register |
| `stave repos list [--verbose]` | List registered repos; `--verbose` appends each repo's description and learned tether count |
| `stave repos sync [name]` | `git fetch --all --prune` on bare mirror(s) |
| `stave repos remove <name>` | Unregister (does not delete bare cache) |
| `stave repos describe <repo> [text]` | Set the repo's description when text is given; print it otherwise (non-zero exit if unset) |
| `stave repos tethers <repo> [--json]` | List the repo's learned co-occurrence tethers, strong first, with mode, effective strength, and count |
| `stave repos tether <from> <to> [--strong\|--weak] [--edit\|--reference]` | Manually pin a tether without waiting for it to be learned; strength defaults to strong, association mode to reference |
| `stave repos forget <from> [<to>] [--all]` | Remove one `<from> → <to>` tether, or every tether from `<from>` with `--all` |

Flags: `--dry-run` on `add`. `--strong`/`--weak` and `--edit`/`--reference` are
each mutually exclusive on `tether`; `forget` requires exactly one of a `<to>`
argument or `--all`. See [Learned repo tethers](#learned-repo-tethers) for the
model behind these commands.

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
| `stave_repos_tethers` | Read a repo's learned co-occurrence tethers during planning |
| `stave_space_status` | Read one existing space during planning |
| `stave_repos_sync` | Queue repo cache sync for confirmation |
| `stave_space_sync` | Queue space sync for confirmation |
| `stave_space_create` | Queue new space creation for confirmation |
| `stave_space_add` | Queue adding a repo to an existing space for confirmation |
| `stave_summon` | Queue launching Codex, Claude Code, or Cursor Agent in a space |
| `stave_saga_create` | Queue creating a saga — a coordination space for sequenced multi-ticket work whose members are ordinary spaces (references become read-only context; no edit worktrees) |
| `stave_saga_status` | Read one saga during planning: members in dependency order with lifecycle state, drift, and base health (merge detection is ancestry-scoped — no PR lookup) |
| `stave_saga_add` | Queue registering a space as a saga member, optionally sequenced after other members; re-adding an existing member updates its `after` edges |
| `stave_explain_unsupported` | Record unsupported/destructive requests as notes |
| `stave_finish` | Finish planning with a summary, notes, and warnings |

Read-only tools can run immediately while the model is planning. Mutating tools never apply changes inside the model loop; they only create a validated operation plan that Stave executes after confirmation, `--incant`, or `agent.autoIncant: true`. Summoning is interactive, so `--json` and non-interactive executions include the summon command but skip the launch.

`stave_space_create` exposes the `-c/--common` behavior as two boolean args:
`common` expands each editable repo's strong tethers into references, and
`include_weak` widens that to weak tethers (and implies `common`). The planner
and CLI share the same expansion, so a planned create behaves identically to the
typed command.

Destructive operations such as archive, destroy, repo removal, reset, delete, push, PR creation, issue tracker updates, and arbitrary shell commands are intentionally not executable by the agent in v1. The same exclusion covers saga mutation beyond membership: saga archive, saga destroy, saga remove, and space retarget remain non-executable by the agent, which records such requests via `stave_explain_unsupported`.

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
| `stave space retarget <space-id> --repo <repo> --base <ref>` | Repoint an edit repo's recorded base without touching the worktree (rewrites the manifest only) |
| `stave space destroy <space-id>` | Remove worktrees and delete space directory |

Space commands take a plain space ID. Relative-path spellings such as
`.archive/<id>` (previously accepted by some read and lifecycle verbs) are no
longer valid space IDs and are rejected.

Space flags:

| Flag | Commands | Meaning |
|------|----------|---------|
| `--kind`, `-k` | `init`, `create` | Free-form label for the space (e.g. `ticket`, `spike`, `audit`), with two reserved behavioral kinds: `review`, which `stave review` sets and which launches Claude into the `pr-teach` skill, and `saga`, which is rejected here — use `stave saga create` |
| `--spec`, `-s` | `init`, `create` | Copy a spec file or directory into `spec/` |
| `--edit`, `-e` | `create` | Editable worktree from base ref (`repo` or `repo:base`; `base` may be `space:<id>` sugar; repeatable) |
| `--reference`, `-r` | `create` | Detached reference worktree (`repo` or `repo:ref`; repeatable) |
| `--saga` | `create` | Register the new space as a member of this saga |
| `--after` | `create` | Member id the new space lands behind (requires `--saga`; repeatable) |
| `--summon` | `create` | Launch `codex`, `claude`, or `cursor` after the space is created |
| `--common`, `-c` | `create` | Also add reference worktrees for the edited repos' **strong** learned tethers (see [Learned repo tethers](#learned-repo-tethers)) |
| `--include-weak` | `create` | With `--common`, widen the expansion to weak tethers too (implies `-c`) |
| `--no-learn` | `create`, `add` | Do not record co-occurrence tethers for this invocation |
| `--edit`, `-e` / `--reference`, `-r` | `add` | Mode (exactly one required) |
| `--base`, `-b` | `add` | Base branch/ref for edits (`space:<id>` sugar accepted), or ref for references |
| `--branch` | `add` | Branch name for editable repos |
| `--no-fetch` | `add` | Skip fetching the bare repo before adding |
| `--references-only` | `sync` | Only sync reference worktrees |
| `--force` | `archive`, `destroy` | Proceed despite dirty edit worktrees |
| `--dry-run` | `create`, `add`, `destroy` | Print Git operations without changing state |

When `--spec` points at a file, it is copied under `spec/` with its original basename. When it points at a directory, the directory contents are copied into `spec/`. The manifest records `specPath: spec`.

When `--summon` is present, unrecognized trailing flags are passed verbatim to
the selected agent before its launch prompt. For example,
`stave space create ticket-482 --summon codex --yolo` launches Codex in yolo
mode. Stave's own flags remain Stave-owned; use `--` before a colliding agent
flag, such as `--summon codex -- --config agent.toml`.

### Stacking spaces

A follow-up space can base its edit branch on another space's edit branch
instead of a remote branch. The base sugar `space:<id>` resolves to the edit
branch that space owns for the same repo:

```bash
# First ticket branches from the remote default
stave space create pay-1 -e api

# Second ticket stacks on pay-1's api branch
stave space create pay-2 -e api:space:pay-1
```

`pay-2`'s drift then reports against `pay-1`'s branch and live-tracks it: as
`pay-1` gains commits, `stave space sync pay-2` shows how far `pay-2` is ahead
of or behind its sibling.

Two warnings to watch for:

- **Canonicalization.** Hand-typed `stave/...` or `origin/stave/...` bases are
  rewritten to `refs/heads/stave/...` with a notice. Stave branches live only
  in the bare repo — Stave never pushes them — so the `origin/...` spelling
  would never resolve.
- **Stale-branch adoption.** If the edit branch already exists, the new
  worktree adopts its current head; the requested base/start-point is ignored
  and the recorded base is aspirational. Stave warns and reports how far the
  adopted branch is ahead of or behind the requested base.

When the base space's work merges, repoint the stacked space at the merged
branch:

```bash
stave space retarget pay-2 --repo api --base origin/main
```

`retarget` rewrites only the recorded base in the manifest — the ref drift is
measured against — without touching the worktree. `--repo` is required, the
same base sugar and canonicalization apply, and a resolved `stave/...` base
must exist in the bare repo. Archiving or destroying a space that a sibling
still stacks on fails closed unless `--force` is given.

### Learned repo tethers

Stave already sees every `-e`/`-r` you pass, so it quietly remembers which repos
you use together. Every non-dry-run `stave space create` and `stave space add`
(and their agent-planner equivalents, and `stave review` pairings) records a
directed **tether** from each editable repo to every other repo in the space,
counting how often the pairing recurs and whether the other repo showed up as an
edit or a reference. A tether crosses from **weak** to **strong** once its count
reaches `tethers.strongThreshold` (default 3); you can also pin either strength
by hand with `stave repos tether`.

That learning pays off with `-c/--common` on `stave space create`: instead of
re-typing the usual reference set, `-c` auto-pulls every strong tether of the
repos you are editing as reference worktrees (`--include-weak` widens that to
weak ones too), de-duplicated against your explicit `-e`/`-r` and skipping any
repo that is no longer registered. `-c` only ever adds references — it never
materializes extra editable worktrees — and the repos it pulls in do not
themselves reinforce the counts, so the numbers stay a record of what you
actually typed. Inspect what has been learned with `stave repos tethers <repo>`
or `stave repos list --verbose`, and prune with `stave repos forget`. Learning
is passive but easy to opt out of: pass `--no-learn` for a single create/add, or
set `tethers.enabled: false` to turn capture off machine-wide.

### `stave saga`

A **saga** is a coordinating space for work that spans several dependent
spaces: one epic split across three tickets, or a migration whose web change
cannot land before its API change. The saga space holds the spec, the roster of
member spaces, and the ordering between them — but no edit worktrees of its
own. The members are ordinary spaces that each own their branches; the saga
records how they relate. The durable reference for the frozen `saga status
--json` schema, base-health semantics, and merge awareness is
[docs/saga.md](docs/saga.md).

| Command | Description |
|---------|-------------|
| `stave saga create <saga-id>` | Create a saga space with an empty member roster |
| `stave saga list` | List every space with its kind and saga membership |
| `stave saga status <saga-id>` | Members in dependency order with state, drift, and topology notes |
| `stave saga sync <saga-id>` | Fetch each shared bare repo once, then sync every live member |
| `stave saga add <saga-id> <space-id>` | Register an existing space as a member |
| `stave saga remove <saga-id> <space-id>` | Drop a member from the roster |
| `stave saga archive <saga-id>` | Archive every member in reverse topological order, then the saga space |
| `stave saga destroy <saga-id>` | Destroy every member in reverse topological order, then the saga space |

Saga flags:

| Flag | Commands | Meaning |
|------|----------|---------|
| `--spec`, `-s` | `create` | Copy a spec file or directory into the saga's `spec/` |
| `--reference`, `-r` | `create` | Detached reference worktree (`repo` or `repo:ref`; repeatable) |
| `--memory` | `create` | Attach memory: `[provider:]<spec>`; `.` creates a fresh store (repeatable, at most one fresh) |
| `--summon` | `create` | Launch `codex`, `claude`, or `cursor` after the saga is created |
| `--no-learn` | `create` | Suppress tether learning for this invocation only — kept for surface symmetry with space `create`. The saga root is a reference-only space with no editable anchor, so the flag is effectively a no-op for the root and is not persisted to members; each member still learns unless it passes `--no-learn` on its own create |
| `--after` | `add` | Member id this space lands behind (repeatable) |
| `--clear-after` | `add` | Reset the member's `after` edges before applying `--after` |
| `--json` | `list`, `status` | Emit machine-readable JSON (for `status`, the frozen `SagaStatus` contract) |
| `--force` | `archive`, `destroy` | Proceed despite dirty member worktrees, external spaces stacked on member branches, or (destroy) other spaces sharing the saga den |
| `--memory` | `archive`, `destroy` | Saga den fate: `keep` (default) or `contribute` on archive; `keep`, `destroy`, or `contribute` on destroy |
| `--dry-run` | `create`, `sync`, `add`, `remove`, `archive`, `destroy` | Print operations (for `archive`/`destroy`, the ordered teardown plan) without changing state |

A saga takes shape in one of two directions — create the saga first and hang
members off it, or register spaces you already have:

```bash
# Create the saga, then create members directly into it
stave saga create checkout-rewrite -s ~/notes/checkout-epic.md
stave space create checkout-api -e api --saga checkout-rewrite
stave space create checkout-web -e web --saga checkout-rewrite --after checkout-api

# Or adopt an existing space into an existing saga
stave saga add checkout-rewrite billing-cleanup --after checkout-api

# Where everything stands, in dependency order
stave saga status checkout-rewrite

# Fetch each bare repo once, sync every live member
stave saga sync checkout-rewrite
```

`--after` records that `checkout-web` lands behind `checkout-api`, which orders
the roster in `saga status` and lets it flag stacking that crosses the
dependency graph. Ordering must stay acyclic and every `--after` target must
already be a member — both are checked when the manifest is saved. `saga add`
upserts: re-adding a member with no `--after` leaves its existing edges alone,
a non-empty `--after` replaces them, and `--clear-after` resets them first.

On `space create --saga … --after …`, an `--after` edge also picks the base for
any `--edit` repo you did not give an explicit base to — the two members above
edit different repos, so nothing is inferred there, but when a member follows a
predecessor *into the same repo* its branch stacks on that predecessor's branch
instead of the remote default:

```bash
stave space create api-part-2 -e api --saga checkout-rewrite --after checkout-api
# api-part-2's api branch starts from stave/checkout-api/api, not origin/main
```

Its drift is then reported against its predecessor. The rule is deliberately
conservative: exactly one predecessor editing the repo means stack on it, no
predecessor editing it falls back to the usual default-base chain, and several
predecessors editing it makes Stave refuse rather than guess — it asks for an
explicit base (`-e <repo>:space:<id>`, the same sugar described under
[Stacking spaces](#stacking-spaces)). The inference happens only at creation:
`saga add` records edges on a space whose branches already exist and never
repoints them — use `stave space retarget` for that.

Two rejections keep the roles apart. `stave saga create` refuses `-e`/`--edit`,
because a saga holds no edit worktrees — create a member instead. And
`stave space init`/`create` refuse `-k saga`, because the kind is reserved for
`stave saga create` (which sets it, and the member roster that goes with it).
Anything after a literal `--` still forwards to the summoned agent as usual.

`saga create` also rejects `-c`/`--common` and `--include-weak`: tether
expansion is anchored on an editable repo, and a saga has none. Members created
through `stave space create --saga … -e <repo> -c` still expand normally, since
that path does carry an editable anchor.

`saga status` lists members in dependency order, each with its lifecycle state
(`live`, `archived`, `missing`, or `corrupt`), its `after` predecessors, and —
for live members — per-repo dirtiness, the recorded base, whether that base
still exists, and ahead/behind drift. Notes call out topology problems worth
knowing about, such as a member stacked on a branch owned by a space outside
the saga, or on a member that is not one of its declared predecessors.

`saga status` also takes `--json`, emitting the frozen `SagaStatus` contract
(including merge detection); `saga sync` is human-readable output only.
`saga list` already has
`--json`, and it covers every space — not just saga members — so it doubles as
the "what is joined to what" view.

Because members are sibling directories rather than subdirectories of the
saga, an agent summoned in the saga root would not normally be allowed to touch
them. Stave keeps the harness settings at the saga root in step with the roster
on every membership change: `.claude/settings.json` gains the member paths
under `permissions.additionalDirectories`, and `.codex/config.toml` gains them
under `sandbox_workspace_write.writable_roots`. Both edits are merge-aware —
only Stave's own entry is rewritten, and the rest of an existing file survives.

Every saga space ships with an embedded `stave-saga` skill (installed at
`.claude/skills/stave-saga/` inside the space), a coordinator loop for driving
members in order. Summoning Claude in a saga space launches straight into it —
no setup and no personal skills required. Other summoners, and saga spaces
that never received the skill, get a coordinator stance prompt instead. Pass
`--prompt` to launch with something else.

Memory attached to a saga is shared with its members: a member that has no
memory of its own is given MCP configuration pointing at the saga's den, so
every agent in the saga reads and writes one context store. See
[`docs/memory.md`](docs/memory.md) for the full recipe.

Single-space lifecycle stays out of saga state: `stave space archive` and
`stave space destroy` refuse to act on a saga space (use `stave saga archive`
or `stave saga destroy` to tear it down with its members) or on a registered
member (use `stave saga remove` to drop it from the roster first). Pass
`--force` to override the refusal and archive or destroy the space anyway —
the roster is then left as-is and may reference a space that no longer
exists.

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

Agent flags can also follow the direct command, for example
`stave summon ticket-482 --with codex --yolo`.

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
| `stave portal summon <space-id> --with codex --mode tmux` | Launch the agent inside a detached tmux session on the portal target |

`stave portal summon` takes `--mode foreground` (default), `tmux`, `headless`,
or `print`. In `tmux` mode Stave starts the summoner inside a detached,
idempotently-created tmux session named `stave-<space>-<portal>` on the target,
then attaches only when a real terminal is present (headless and non-interactive
runs stay detached and print how to attach). `tmux` mode requires `tmux` on the
target; `stave portal logs` can read the session's pane back only for ssh/ec2
portals (docker/devcontainer logs read container stdout, not the tmux pane).

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
    description: Core HTTP API service   # optional; set via `stave repos describe`
tethers:
  enabled: true          # global kill switch for passive tether learning
  strongThreshold: 3     # co-occurrence count at which a tether becomes "strong"
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
- **`repos.<name>.description`** — optional free-text label shown by `stave repos list --verbose` and `stave repos describe`; set it via `stave repos describe <repo> "text"`.
- **`tethers.enabled`** — global kill switch for passive tether learning (default `true`). When `false`, captures are skipped and `-c/--common` errors with a hint to re-enable.
- **`tethers.strongThreshold`** — co-occurrence count at which a learned tether is classified **strong** (default `3`; values below `1` are clamped back to `3`).

Editable branches default to `stave/<space-id>/<repo>` unless `--branch` is set.

Learned tethers themselves are **not** stored in `config.yaml`. They live in a
machine-local sidecar at `~/stave/repo-tethers.yaml` (under `config.Root`), with
a companion `~/stave/repo-tethers.yaml.lock`. The file is purely machine-generated
co-occurrence data — safe to delete and regenerate, never committed to a repo,
and ignored entirely by older Stave binaries. Human input (repo descriptions)
lives on the registry instead, so wiping the sidecar loses no hand-authored data.

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
