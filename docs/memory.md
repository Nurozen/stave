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
stave memory providers
stave memory attach <space-id> [--provider marmot] [--use <existing-den>]
                    [--edit <warren>/<project>]... [--link <target>]...
                    [--opt k=v]... [--json]
stave memory status <space-id> [alias]   # per-link freshness/skew rows
stave memory list [space-id]
stave memory sync <space-id>             # marmot warren sync --json (per-warren results)
stave memory propose <space-id>   # den contribute + warren propose (no auto-push)
stave memory detach <space-id> [--keep | --destroy] [--force]
```

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
3. Register reverse route: space path → den id in `$MARMOT_HOME/routes.yml`.
4. Write space-local MCP (never `.marmot-vault`):
   - `.mcp.json` / `.cursor/mcp.json` — `mcpServers.context-marmot`
   - `.vscode/mcp.json` — `servers.context-marmot`
   - `.codex/config.toml` — `[mcp_servers.context-marmot]` (+ `.env` for `MARMOT_HOME`)
5. Record attachment in `.stave.yaml` and refresh `AGENTS.md`.

Attach-existing (`--use <den-id>` / `--memory marmot:<den-id>`) skips step 2
(no den create) but still verifies the den, registers the reverse route
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

## Detach and cleanup

| Fate | Den | Reverse route | MCP configs |
|------|-----|---------------|-------------|
| `--keep` (default) | Retained | Removed | **Stripped** (`context-marmot` only) |
| `--destroy` | Destroyed | Removed with den | **Stripped** |
| Archive space | Kept (fate keep) | `route set-project --from old --to .archive/…` | **Preserved** with the moved space |

MCP cleanup preserves unrelated servers in the same JSON/TOML files. Reattach
rewrites the Codex section so a new den id is not left pointing at a stale table.

Owned attachments only are destroyed; unowned (`--use` existing den) force
`fate=keep` with a notice.

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

## Related

- ContextMarmot dens: sibling `docs/dens.md` / JSON fixtures `testdata/contracts/`
- Portals: [portal.md](./portal.md) — memory remains local-summon-only in v1
