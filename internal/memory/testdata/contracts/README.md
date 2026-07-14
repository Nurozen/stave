# JSON contracts (stave / automation)

Versioned stdout envelopes for `marmot den` (and later warren) `--json` verbs.

- **Schema field:** every document has `"schema": 1` (integer). Consumers negotiate on
  `schema`, never by parsing the marmot binary version. Evolution is **additive only**.
- **Errors:** structured error objects on stdout with non-zero exit codes (not only stderr).
- **Hand-built:** envelopes are dedicated structs — internal types are never marshalled
  directly.

These fixtures are **normative sketches** until P1b implements the verbs. When
implementation lands, tests should load these files as golden outputs (or update them in
the same PR as intentional schema changes).

### Fixtures

| File | Verb / purpose |
|------|----------------|
| `den_create.v1.json` | `den create --json` success (pointer written) |
| `den_create_no_pointer.v1.json` | stave attach: `den create … --no-pointer --json` (`pointer_written: false`) |
| `den_create_with_links.v1.json` | create + `links[].resolved_via` |
| `den_status.v1.json` | `den status --json` |
| `den_destroy.v1.json` | destroy + promote counts |
| `den_destroy_contributed.v1.json` | destroy after contribute |
| `dry_run.v1.json` | `--dry-run --json` ops list |
| `error.v1.json` | structured error on stdout (non-zero exit) |
| `warren_status_additive.v1.json` | additive warren status fields (P2/P3) |

**Stave** pins these files (or copies) for the `internal/memory` marmot provider.
Negotiate on `schema`, never by parsing the marmot binary version.

See also: `docs/dens.md`, `artifacts/stave_alignment/synthesis_plan.md` §13 OQ15 / §15.6 / §17.
