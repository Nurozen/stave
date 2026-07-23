# JSON contracts (stave / automation)

Versioned stdout envelopes for `marmot den` (and later warren) `--json` verbs.

- **Schema field:** every document has `"schema": 1` (integer). Consumers negotiate on
  `schema`, never by parsing the marmot binary version. Evolution is **additive only**.
- **Errors:** structured error objects on stdout with non-zero exit codes (not only stderr).
- **Hand-built:** envelopes are dedicated structs — internal types are never marshalled
  directly.

The verbs these fixtures pin are implemented in marmot; the files are the
shared contract stave's `internal/memory` tests parse against. Update them in
the same PR as intentional schema changes (marmot side first — see the mirror
note below).

### Fixtures

| File | Verb / purpose |
|------|----------------|
| `den_create.v1.json` | `den create --json` success (pointer written) |
| `den_create_no_pointer.v1.json` | stave attach: `den create … --no-pointer --json` (`pointer_written: false`) |
| `den_create_with_links.v1.json` | create + `links[].resolved_via` (S4 `--ref` pass-through outcomes) |
| `den_status.v1.json` | `den status --json` with per-link freshness (`ahead/behind/pending_edits/state/pinned_commit/source_commit`) |
| `den_destroy.v1.json` | destroy + promote counts |
| `den_destroy_contributed.v1.json` | destroy after contribute |
| `dry_run.v1.json` | `--dry-run --json` ops list |
| `error.v1.json` | structured error on stdout (non-zero exit) |
| `warren_status_additive.v1.json` | additive warren status fields (P2/P3) |
| `den_link.v1.json` | `den link --edit --json` success (cache-backed: additive `worktree` + `branch`; legacy checkout links omit both) |
| `den_contribute.v1.json` | `den contribute --json` success (edit branch + counts) |
| `warren_propose.v1.json` | `warren propose --json` success (never auto-pushes) |
| `warren_sync.v1.json` | `warren sync --json` per-warren results (S4 real sync; exit 1 only when every warren failed) |
| `resolve.v1.json` | `resolve --json` reference resolution (`space add` memory-link parity, F5) |

Every fixture mirrors marmot's source-of-truth copy in
`context-marmot/testdata/contracts/`; keep them byte-identical when syncing.
Only envelopes stave PARSES are mirrored (e.g. `den_unlink.v1.json` is not —
stave never issues den unlink).

**Stave** pins these files (or copies) for the `internal/memory` marmot provider.
Negotiate on `schema`, never by parsing the marmot binary version.

See also: `docs/dens.md`, `artifacts/stave_alignment/synthesis_plan.md` §13 OQ15 / §15.6 / §17.
