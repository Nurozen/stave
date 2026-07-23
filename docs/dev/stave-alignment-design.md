# Dens + Stave Alignment — Design Overview

Architecture of ContextMarmot's den system and its integration with the `stave` CLI
(bare-repo caches + task-scoped agent workspaces). Dens apply the same pattern to
context: centrally stored vault-plus-link-table workspaces, a shared warren cache, and
PR-reviewed knowledge flow-back. Fully implemented as of 2026-07-22. User-facing docs:
`docs/dens.md`, `docs/warrens.md` (marmot) and `docs/memory.md` (stave).

## Core concept: Dens

A **den** is a per-project context workspace stored centrally — *a vault plus a link
table*, not a vault alone. Vaults move OUT of code repos (no more `.marmot/` subfolder
in the project, which agents aggressively self-read/edit) into a central home:

```
~/.marmot/dens/<den-id>/
  _den.md              # den manifest: project path(s), links + modes, lifetime, den config
  vault/               # this project's OWN identity vault (OPTIONAL — links-only dens exist)
    _config.md         #   vault_id = den identity
    <namespaces>/...
    .marmot-data/      #   vault-scoped: embeddings.db, .env, vault.read.lock
                       #   (DECIDED 2026-07-13: stays inside the vault — a den vault is
                       #    byte-identical in layout to an in-repo vault)
  _bridges/            # den-scoped, consumer-owned bridges (see Bridges)
  .marmot-data/        # den-scoped: daemon lock/socket, link/cache state
                       #   (sole data dir for links-only dens)
```

- At most ONE identity vault per den; multi-vault needs are expressed as links;
  intra-vault partitioning stays namespaces.
- A den contains NO copies of linked material — links resolve to shared caches
  (warren-cache) or live dens.
- Dens must stay near-free to create (manifest + empty DB + pointers) so they are
  safe to destroy — task-scoped/disposable dens are a first-class lifestyle.
- DECIDED 2026-07-13: task dens get a lightweight identity vault BY DEFAULT (the
  accumulation buffer for new knowledge / flow-back); `--no-vault` keeps links-only
  dens available.

## Vault discovery / routing

- `routes.yml` (currently `vaults: {id: {path}}`, internal/routes) grows a reverse
  table: `projects: {<abs-project-path>: <den-or-vault-id>}`.
- `marmot serve` resolution order: `--dir` flag → cwd reverse-route lookup → a
  one-line `.marmot-vault` pointer file in the project (vault-id only; committable;
  fallback for MCP clients that don't guarantee cwd) → legacy cwd walk-up for
  in-repo `.marmot/` (backward compat, must keep working).

## Global setup

- `marmot setup --global`: write user-scope MCP config per harness (Claude Code user
  scope, `~/.codex/config.toml`, `~/.cursor/mcp.json`, VS Code user settings) with
  command `marmot serve` and NO embedded path. Project-local setup remains a fallback.
- With a global warren registry + global setup, a project with no personal vault needs
  zero local setup: serve starts, finds no den, still answers from mounted warrens.

## Warrens: bare-mirror cache (stave `repos add` pattern)

- Warren registration moves from per-workspace `_warren.md` state to a GLOBAL registry
  under `~/.marmot/`.
- `marmot warren add <url>` → bare mirror at `~/.marmot/warren-cache/<id>.git`
  (clone --bare, rewrite `remote.origin.fetch` refspec, fetch --all --prune —
  exactly stave's internal/git pattern). Marmot owns the clone; the current
  "user-managed checkout at arbitrary path" registration goes away (compat path TBD).
- `marmot warren sync` = one fetch across all warrens. Bare repos can't be dirty, so
  `refresh --pull` dirty-tree failures disappear from the read path.
- Read path: burrows materialize from ONE shared checkout/worktree per warren
  (provenance-pinned) instead of per-workspace full copies — dedupes the
  embeddings.db copies from N× to 1×.
- Warren manifest v3 (per-project, captured at import): `source_url` (canonical form) and
  `source_commit` — enables reference-repo → warren-project resolution and honest skew
  reporting ("vault snapshot from source commit abc; your checkout is at def").
- Write path: each editable link gets a DEDICATED worktree off the bare on branch
  `marmot/edit/<den-id>/<project>` (stave's `stave/<space>/<repo>` pattern). MCP
  writes land there, never in a user checkout, never blocking sync.
- DECIDED (2026-07-13): auto-commit per MCP write on edit worktrees, pathspec-limited to
  node markdown — embeddings.db and WAL sidecars are NEVER committed, and warren repos stop
  carrying embeddings.db as transport (consumers regenerate embeddings on burrow/sync).
  Granular commits by default, `propose --squash` opt-in; propose stays print-push-only.
  Marmot's no-auto-commit stance protects vault semantic history (supersede chains),
  not git-as-transport for warren edits.

## Den links (stave edit/reference analogy)

```
marmot den create myproject \
  --edit platform-warren/docs \      # editable: worktree + branch, updates-only
  --link platform-warren/billing \   # pinned reference: burrow from shared cache
  --link auth-service-den            # live reference: resolves to that den's vault, zero staleness
```

- Query: federates across own vault + ALL links. KNN embeds the query per linked
  vault's embedding model (per-link model/credentials in den config); traversal
  crosses den bridges; results carry qualified IDs (`@vault-id/node-id`).
- Writes: own vault = full CRUD (unqualified default). `--edit` links = updates to
  existing nodes ONLY for LIVE MCP writes (batch flow-back via `den contribute` may
  create — the PR is the review gate), quarantined on the edit branch until
  propose/push. Read-only links = rejected fail-closed with an error naming the mode;
  author `readonly: true` veto re-checked at write time. Knowledge *about* a read-only
  link → write to own vault + den bridge.
- Live den-to-den links generalize the existing warren identity rule (vault_id match
  → resolve live). Ephemeral dens (`lifetime: task`) should warn/refuse as link targets.

## Bridges (resolves the existing two-surface overlap)

- Consumer-owned bridges live in the den's `_bridges/` and may connect any pair of
  linked vaults (own↔ref, ref↔ref) without write access to sources.
- Warren manifest bridges become author-SUGGESTED bridges that linking activates into
  the den. Vault-internal `_bridges/` shrinks to namespace-level bridging only.

## MCP surface

- Populate the MCP `instructions` field at initialize with LIVE topology: identity
  vault, links + modes, pending edit counts, staleness (behind N commits). No
  resources/files on disk. (Today: `internal/mcp/server.go` registers nothing but 6 tools.)
- Write results name the physical destination (e.g. "edit 4 pending on
  marmot/edit/<den>/<project>").

## Lifecycle / UX

- `den create/link/status/destroy`; status shows per-link mode, freshness
  ahead/behind (stave `space status` pattern).
- `--dry-run` on every mutating warren/den command, printing exact fs/git ops
  (stave prints `(cd dir && git …)`).
- `den destroy`: refuses if edit branches have unpushed commits (unless --force);
  offers promote-on-destroy — fold the task den's vault nodes into a durable den via
  the existing CRUD classifier (ADD/UPDATE/SUPERSEDE/NOOP) so scratch NOOPs away and
  real learnings merge into supersede-chain history.
- `den contribute <link>` (DECIDED 2026-07-13): the warren-bound sibling of promote —
  classify the den vault's nodes against the target warren project's graph and stage the
  results (creates included) as commits on the edit branch; `warren propose` then
  push-preps as usual. Live writes stay conservative; batch flow-back through git review
  can be liberal.
- Confirmation gates ONLY on: warren unregister / vault-den deletion, `index --force`,
  bulk delete. Node-level mutations stay frictionless.

## Stave integration (DECIDED 2026-07-13/21 — planned in the stave repo, mirrored brief there)

- Integration shape: a provider-agnostic `stave memory` command group
  (providers/attach/status/list/sync/propose/detach), marmot as the first provider —
  provider selected by config (`memory.provider`), NEVER by command path. Sugar:
  `stave space create --memory [provider:]<spec>` (repeatable); `--use <id>` attaches an
  existing durable den. IMPLEMENTED (S2, stave `51f49fb`).
- `.stave.yaml` gains `memories: [{name, provider, id, owned}]`. `owned: true`
  (stave-created task den) follows the space's fate flags; attached pre-existing dens are
  NEVER destroyed by stave — detach only.
- Task dens get a lightweight identity vault BY DEFAULT (accumulation buffer for new
  knowledge; still near-free). No vault files in the space root — attach writes
  space-local MCP config pointing at the central den (no `.marmot-vault` pointer needed
  in spaces — attach passes `--no-pointer`; no dependency on `setup --global`).
- Reference repos resolve to memory marmot-side (canonical URL → warren `source_url`;
  else in-checkout vault_id; else skip + warning). Stave passes raw url/path/ref only;
  `config.Repository` gains optional `marmotVault: <id|off>` as override/suppression.
- Failure policy: explicit attach/`--memory` fails hard on probe failure; ambient
  `memory.default: true` degrades with a notice. Archive = detach-but-keep + route fix.
  Destroy: `--memory=keep|destroy|contribute` (default keep); `--force` if unproposed
  edits pend.
- DECIDED 2026-07-21: `stave space archive` also accepts `--memory=keep|contribute` —
  contribute-then-keep: fold learnings into the warren PR at parking time, den survives
  with the archived space.
- DECIDED 2026-07-21: `stave review` supports memory — same `--memory`/`--use` surface
  and ambient `memory.default` as `space create`; review spaces are task spaces, so the
  full attach/status/propose/detach lifecycle and destroy/archive fates apply.
- Flow-back: `stave memory propose` wraps `marmot den contribute` (classifier stages
  ADD/UPDATE/SUPERSEDE from the task-den vault onto the edit branch — creates allowed
  there because the PR reviews them) + `warren propose` (push-prep only). Live MCP writes
  stay updates-only. `warren propose` gains `--json` (schema:1 envelope) because stave's
  fate=contribute path parses all three verbs' stdout.
- Portals: memory is local-summon-only in v1. Devcontainer/docker mounts later, under an
  exclusive-ownership rule (SQLite WAL `-shm`/flock do not span the macOS Docker VM
  boundary — container owns the den while mounted; host commands warn/refuse). ssh/ec2
  out of scope.
- Space `AGENTS.md` gains one line: persistent memory available via context-marmot MCP tools.

## Invariants to respect

- Backward compatibility: in-repo `.marmot/` vaults keep working (walk-up discovery,
  project-local setup).
- No auto-push anywhere; propose prints/executes push only on explicit user action.
- Identity keyed solely on `vault_id`.
- Single-owner daemon model per vault must extend sanely to dens (lock lives with the den).
- Windows: no daemon (standalone) — den paths must not assume unix sockets.
