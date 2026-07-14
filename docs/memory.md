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
stave memory attach <space-id> [--provider marmot] [--use <existing-den>] [--json]
stave memory status <space-id> [alias]
stave memory list [space-id]
stave memory sync <space-id>
stave memory propose <space-id>   # den contribute + warren propose (P4; no auto-push)
stave memory detach <space-id> [--keep | --destroy] [--force]
```

`space create` / `space destroy` accept memory flags (`--memory`, `--memory keep|destroy|…`)
when configured; see `stave space create --help` / `destroy --help`.

## Attach flow (marmot)

1. Capability probe (`marmot` version / den create help).
2. `marmot den create <store-id> --lifetime task --project <abs-space-path> --no-pointer --json`
3. Register reverse route: space path → den id in `$MARMOT_HOME/routes.yml`.
4. Write space-local MCP (never `.marmot-vault`):
   - `.mcp.json` / `.cursor/mcp.json` — `mcpServers.context-marmot`
   - `.vscode/mcp.json` — `servers.context-marmot`
   - `.codex/config.toml` — `[mcp_servers.context-marmot]` (+ `.env` for `MARMOT_HOME`)
5. Record attachment in `.stave.yaml` and refresh `AGENTS.md`.

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

## Archive

`stave space archive` keeps dens and rewrites reverse routes to the archive path
so discovery from the archived space root still resolves. Manifest memory
entries remain on the archived `.stave.yaml`.

## Error paths

- Marmot missing from `PATH` → provider unavailable; attach exits non-zero; manifest unchanged.
- Den id already exists → structured `den_create_failed`; attach aborts.
- Project path already owned by another den → marmot refuses create (no silent route steal).

## Related

- ContextMarmot dens: sibling `docs/dens.md` / JSON fixtures `testdata/contracts/`
- Portals: [portal.md](./portal.md) — memory remains local-summon-only in v1
