# Space memory (ContextMarmot dens)

Stave’s `memory` command group attaches a **provider-backed context store** to a
space. The default (and currently only) provider is **marmot**, which owns dens
under `$MARMOT_HOME` rather than an in-space `.marmot/` tree.

## Why dens, not in-tree vaults

- Agents must not self-index or edit vault files under the space worktree.
- Multiple spaces can reference dens without copying embeddings.
- Archive/destroy of a space can **keep** the den as durable task residue.

## Prerequisites

1. Dens-aware `marmot` on `PATH` (ContextMarmot with `den` / `route` / `serve --den`).
2. Optional: `MARMOT_HOME` pointing at the dens root (default `~/.marmot`).
3. Stave config (optional):

```yaml
memory:
  provider: marmot
  binary: marmot
  default: false
```

## CLI

```text
stave memory providers [--json]
stave memory attach <space-id> [--provider marmot] [--use <existing-den>]
                    [--edit <warren>/<project>]... [--link <target>]...
                    [--opt k=v]... [--dry-run] [--json]
stave memory status <space-id> [alias] [--json]   # per-link freshness/skew rows
stave memory list [space-id] [--json]
stave memory sync <space-id> [alias] [--dry-run] [--json]     # marmot warren sync --json (per-warren results)
stave memory propose <space-id> [alias] [--dry-run] [--json]  # den contribute + warren propose (no auto-push)
stave memory detach <space-id> [alias] [--keep | --destroy] [--force] [--dry-run] [--json]
```

### JSON output

Every verb takes `--json` and then prints exactly one JSON document on stdout
and no prose: `attach` and `detach` return `{spaceId, spacePath, manifest,
attachments|detached[], notes[]?}` with the manifest reloaded from disk and the
human notices (not-owned downgrade, portal notice, pointer cleanup) collected
under `notes`; `list` returns `[{spaceId, spacePath, attachments[]}]`; `status`
returns per-attachment `{state?, lifetime?, links[{alias, kind, ahead, behind,
pending, stale, reachable}]}` mirroring what the human rows parse from
`den status --json`; `sync` / `propose` return `{spaceId, results[{alias,
warren?, outcome, detail?, pushCommand?}]}`; `providers` returns the probed
`{available, version?, capabilities[], error?}` per provider. With `--dry-run
--json` the payload is `{dryRun: true, plan[]}`; on failure the command prints
`{"error": {code, message, details?}}` and exits 1 — `memory_in_use` when the
den is held by a live process. See the README's "Machine-readable output" for
the full field lists.

`space create` / `space destroy` accept memory flags (`--memory`, `--memory keep|destroy|…`)
when configured; see `stave space create --help` / `destroy --help`.

### Aliases and repeatable `--memory`

`--memory` is repeatable. Default aliases derive automatically: an explicit
`--name` always wins; attach-existing (`marmot:<den-id>` or `--use`) uses the
den id as the alias; fresh stores use `default`. Derived aliases are
uniquified against the space (`default`, `default-2`, …) so a batch never
collides. The whole batch is validated before anything attaches — duplicate
specs that target the same store (two fresh specs on one provider, or the
same den id twice) reject the batch with nothing attached.

## Attach flow (marmot)

1. Capability probe (`marmot` version / den create help).
2. `marmot den create <store-id> --lifetime task --project <abs-space-path> --no-pointer [--ref …]… --json`
3. Set `watch_sources: false` in the new den's vault `_config.md` (owned
   creates only, before any MCP config lands; see
   [Source watching](#source-watching-stave-owned-dens)).
4. Register reverse route: space path → den id in `$MARMOT_HOME/routes.yml`.
5. Write space-local MCP (never `.marmot-vault`):
   - `.mcp.json` / `.cursor/mcp.json` — `mcpServers.context-marmot`
   - `.vscode/mcp.json` — `servers.context-marmot`
   - `.codex/config.toml` — `[mcp_servers.context-marmot]` (+ `.env` for `MARMOT_HOME`)
6. Record attachment in `.stave.yaml` and refresh `AGENTS.md`.

Attach-existing (`--use <den-id>` / `--memory marmot:<den-id>`) skips steps
2–3 (no den create, and a reused den's vault config is never touched) but
still verifies the den, registers the reverse route
(`marmot route add --project <space-path> --json <den-id>`), and writes the
MCP configs. The route table maps one path to one id, so with multiple
attachments the space's reverse route follows the most recently attached den.
Detach (fate keep) removes the route — attachment-created state is cleaned up
even though the den itself is kept.

### S4 pass-through (capability-probed)

When the installed marmot has the P4 den surface (probe: `marmot den --help`
mentions the `link` verb and the `--ref` flag; cached per process):

- Every **reference repo** on the space passes to `den create` as a repeatable
  `--ref name=<repo>,url=<url>,ref=<gitref>`; marmot resolves each into a
  pinned warren link (`source_url` match), a live checkout-vault link, or a
  skip. Attach prints one line per reference:
  `reference billing → platform/billing (warren-url)` or
  `reference openscad → no memory found`.
- `--edit` / `--link` flags pass through as separate `marmot den link` calls
  after create (they are den link verbs, not den create flags). For
  cache-backed warrens an edit link gets a dedicated **cache edit worktree**:
  contribute never touches a user checkout.
- `--opt k=v` maps to den create flags (`k=true` → bare `--k`, else `--k v`).
- Per-repo config `repos.<name>.marmotVault` overrides resolution: `off`
  omits the `--ref` entirely; an explicit id becomes a direct
  `den link --link <id>` (no resolution).

**Old marmot** (no probe match): all of the above are dropped with a notice
and attach proceeds with the byte-stable S2 argv.

### `space add` parity

Adding a reference repo to a space that **already** has memory attached links
it too (default-on; opt out with `space add --link-memory=false`). Stave runs
`marmot resolve --name <repo> --url <url> [--ref <gitref>] --json` and, when
it resolves via `warren-url`, issues `den link <den> --link <warren>/<project>`
— the same pinned read-only link an attach-time `--ref` would produce.
`checkout-vault` and `none` outcomes print a notice / "no memory found" line
without linking (v1 links only warren-url matches). `repos.<name>.marmotVault`
behaves as at attach: `off` skips, an explicit id links directly. Linking is
soft — any failure prints a notice and the repo stays added — and old marmot
binaries degrade with the same upgrade notice as attach.

### Status and sync intelligence (S4)

`stave memory status` parses `den status --json` per-link freshness: edit
links show `ahead/behind/pending edits (unpushed)`, pinned links show
`pinned <commit> behind N (stale)` plus a source-commit skew note, live links
show reachability. The attachment header (and the `space status` memory row)
gains a compact suffix — `(2 unpushed)`, `(stale)`, `(unreachable)`.

`stave memory sync` runs `marmot warren sync --json` (probe-gated) and renders
per-warren results — `synced w (updated abc1234 → def5678)`, up-to-date, or a
failure line. Exit is non-zero only when **every** warren failed (mirrors
marmot's exit semantics). After a successful sync the per-attachment skew is
re-reported — `<alias> (2 unpushed)` / `(stale)` / `(unreachable)` / `(ok)` —
so the post-sync freshness is visible without a separate `memory status`. Old
binaries fall back to a den-status report with a warning.

Server argv shape:

```json
{
  "command": "/abs/path/to/marmot",
  "args": ["serve", "--den", "<den-id>"],
  "env": { "MARMOT_HOME": "/abs/path/to/marmot-home" }
}
```

## Saga-shared dens

A saga and its members share one den. The saga owns it; the members only point
at it.

```bash
stave saga create checkout-rewrite --memory .
stave space create checkout-api -e api --saga checkout-rewrite
stave space create checkout-web -e web --saga checkout-rewrite --after checkout-api
```

The saga attaches through the ordinary flow above, with one difference: its
owned store is created **durable**, not task-scoped —
`marmot den create <id> --lifetime durable --project <saga-path> --no-pointer --json`
— because the den outlives any single member. At most one fresh `--memory`
spec is allowed per `saga create` (attach-existing specs are unlimited); a
batch with two fresh specs is rejected before anything is created.

Members get **MCP config only**. Joining a saga (via `space create --saga`, or
`saga add` on an existing space) writes the same four space-local files —
`.mcp.json`, `.cursor/mcp.json`, `.vscode/mcp.json`, `.codex/config.toml` —
pointing `serve --den` at the *saga's* den id. Deliberately, a member gets:

| Artifact | On the member |
|----------|---------------|
| Den | None created |
| Attachment in `.stave.yaml` | **None** — the saga holds the record |
| Reverse route in `routes.yml` | **None** — the saga owns the route |
| MCP configs | Written, pointing at the saga den |

So `stave memory list <member>` and the member's `space status` memory row stay
empty: from the manifest's view the member has no memory, while its agents talk
to the saga's den. `saga remove` strips the wiring again (`context-marmot`
entries only, unrelated servers preserved).

**A member's own den always wins.** Wiring happens only when the member has no
memory attachment of its own, and the check runs after the member space is
fully created — so a member that attached its own store, or picked one up
ambiently from `memory.default: true`, keeps its own MCP configs untouched and
is never repointed at the saga den. The same guard applies on removal: such a
member never has its configs stripped by `saga remove`.

### Concurrent serve

Every space in a saga points `serve --den` at the same den id, so running
harnesses in the saga and several members at once means several concurrent
`marmot serve --den <same-id>` processes against one den. That is marmot's
concurrency to manage, not Stave's, and it was already possible with
`memory attach --use <den-id>` — but sagas make it the default topology rather
than the exception.

The flip side at teardown: a den-destroying `saga destroy` destroys the saga
den *first*, so **every member's live serve blocks it** with `source_in_use`
(not `--force`-bypassable) and the whole saga stays intact — see
[saga.md](saga.md#saga-aware-lifecycle-saga-archive-and-saga-destroy).

### Source watching (stave-owned dens)

Owned creates write `watch_sources: false` into the den vault's `_config.md`
frontmatter (all existing keys preserved — `vault_id` is federation identity)
so `marmot serve` never auto-indexes the space workdir into the den:
stave-created dens are agent-authored. Scope and limits:

- The opt-out governs **serve-driven source indexing only**. It does NOT
  avoid the `source_in_use` destroy refusal — the serve owner holds the
  watch lock regardless of the key.
- Dens created before this write keep any residual source nodes (harmless;
  retroactive cleanup is out of scope).
- A standalone `marmot watch` process ignores the key.
- The write is best-effort only for **absent** vaults: no `vault/_config.md`
  (`--no-vault` dens, old binaries) = silent skip, while a
  present-but-unwritable/unparseable config fails the attach cleanly before
  any MCP config is written.

## Detach and cleanup

| Fate | Den | Reverse route | MCP configs |
|------|-----|---------------|-------------|
| `--keep` (default) | Retained | Removed | **Stripped** (`context-marmot` only) |
| `--destroy` | Destroyed | Removed with den | **Stripped before the destroy** |
| `--contribute` | Contribute + propose, then destroyed (destroy runs with `--force` by design) | Removed with den | **Stripped before the destroy** |
| Archive space | Kept (fate keep) | `route set-project --from old --to .archive/…` | **Preserved** with the moved space |

MCP cleanup preserves unrelated servers in the same JSON/TOML files. Reattach
rewrites the Codex section so a new den id is not left pointing at a stale table.

Destroying fates strip (or re-point, when sibling attachments remain) the
space MCP wiring **before** issuing `den destroy`, so no client started from a
stale config can re-acquire the den mid-teardown; a failed strip aborts the
destroy. Marmot refuses the destroy with `source_in_use` while any
`serve --den` against the den is live — `--force` included — and the refusal
leaves the space intact: stave restores the wiring so the space keeps working
and the destroy can simply be re-run. The same restore applies to
`unpushed_edits` / `unpushed_unknown` refusals; ambiguous failures (including
`den_not_found`) never restore, since the den may already be gone.

The contribute fate passes `--force` to its destroy step by design, and the
waiver is wider than the fresh contribute commit: it waives the
`unpushed_edits` refusal for **unpushed work in every one of the den's edit
worktrees**, and the `unpushed_unknown` refusal too — the case where the git
state is degraded enough that marmot cannot verify whether anything is
unpushed. Without it the contribute commit alone would trip the refusal on
every retry. This is safe because den destroy never deletes warren branches:
every edit branch survives in the shared warren cache, so the waiver only
skips a preflight check and never discards published history. `--destroy`'s
`--force` stays user-controlled.

Owned attachments only are destroyed; unowned (`--use` existing den) force
`fate=keep` with a notice.

### Destroy retries converge

A destroying-fate teardown (`space destroy --memory destroy|contribute`, and
`memory detach --destroy`) records each attachment's outcome durably as it
goes: after every successful destroy the attachment is spliced out of
`.stave.yaml` and `AGENTS.md` is rewritten in the same step, so the space
never keeps advertising a den that no longer exists. If a later attachment
refuses (e.g. `source_in_use`), the mid-retry state is well-defined:

- the manifest lists **only the unprocessed attachments** — already-destroyed
  dens are gone from both `.stave.yaml` and `AGENTS.md`;
- the refusing attachment's space MCP wiring is **restored**, so the space
  keeps working while you close the holder;
- remediation is simply to **re-run the same command**: processed attachments
  are never re-destroyed or re-contributed, so retries converge instead of
  wedging.

Because the manifest is spliced as teardown goes, `stave space status` reflects
exactly what remains: after a partial or failed destroy its output is an
accurate picture of the residue — which attachments are still attached and
which dens are still present — so the operator can re-run the destroy and watch
it converge.

A `den_not_found` refusal from the provider is treated as part of the same
convergence: for the destroy fate the den is already gone, so the record is
spliced with a notice and teardown continues. For the contribute fate stave
cannot know whether the vanished den's content was ever contributed — the
notice says exactly that (the record is still spliced so retries converge).

## Propose output

`stave memory propose` (and the archive/destroy contribute paths) surface the
full handoff parsed from both marmot envelopes: contributed counts
(`added/updated/superseded/noop`), the contribution branch/commit, every
provider warning (never dropped — especially before a destroy), and the push
command (or `nothing new to push`). Stave never auto-pushes.

## Archive

`stave space archive` keeps dens and rewrites reverse routes to the archive path
so discovery from the archived space root still resolves. Manifest memory
entries remain on the archived `.stave.yaml`. Route relocation is a
space-level operation: it runs once per provider (not once per attachment)
and only after the space directory has been renamed. If the route update
fails (e.g. no route was registered), the archive still completes and a loud
warning explains how to repair with `marmot route set-project --from … --to …`.

`archive --memory keep|contribute` controls the memory step:

- `keep` (default) — current behavior: dens retained, route rewritten to `.archive/…`.
- `contribute` — contribute-then-keep: for **every** attachment (including
  unowned — contribute is non-destructive) the provider runs `den contribute` +
  `warren propose` (no auto-push, no destroy) *before* any archive mutation,
  then the normal keep-archive proceeds. A propose failure or refusal aborts
  the archive with the space completely untouched (no worktree removal, no
  move, no route rewrite).
- `destroy` is rejected — use `stave space destroy --memory destroy`.

`--dry-run` composes: prints the provider contribute/propose argv plus the
archive plan without invoking anything.

## Error paths

- Marmot missing from `PATH` → provider unavailable; attach exits non-zero; manifest unchanged.
- Den id already exists → structured `den_create_failed`; attach aborts.
- Project path already owned by another den → marmot refuses create (no silent route steal).
- Den held by a live process on destroy/detach → structured `source_in_use`; the space
  and manifest stay untouched. Remediation: close agent sessions using this space's
  memory — or any space sharing its den (saga members do) — and other marmot processes
  (`marmot serve --den`, watch/index), then retry. `--force` does **not** bypass this
  (it covers only `unpushed_edits`/`unpushed_unknown`).

## Related

- ContextMarmot dens: sibling `docs/dens.md` / JSON fixtures `testdata/contracts/`
- Portals: [portal.md](./portal.md) — memory remains local-summon-only in v1
