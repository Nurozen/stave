# Sagas

A **saga** is a coordinating space for work that spans several dependent
spaces — a web change that cannot land before its API change, a migration
split across repos. The saga space itself is a *logical grouping*: it holds
the spec, the member roster with `after` dependency edges (which imply merge
order), and reference checkouts, but **no edit worktrees of its own**. Members
are ordinary sibling spaces under `agent-work/` — flat siblings on disk, not
nested under the saga — each owning its branches; the saga knows who they are
and in what order their work lands.

This document is the durable reference for the saga status contract, merge
awareness, and their operational caveats. For command-by-command usage see the
`stave saga` section of the [README](../README.md).

## The frozen `saga status --json` schema

`stave saga status <saga-id> --json` emits one `SagaStatus` object (defined in
`internal/space/saga_status.go`). The shape is **frozen**: fields are additive
only; existing fields never change name, type, or meaning. The freeze applies
to `saga status` alone — the other `--json` surfaces (`saga list`, `saga
archive|destroy`, `space list`) are additive-compatible but evolve; their
current shapes are documented in the README's
[Machine-readable output](../README.md#machine-readable-output) and, for the
lifecycle verbs, [below](#machine-readable-lifecycle-output).

Top level (`SagaStatus`):

| Field | Type | Meaning |
|---|---|---|
| `saga_id` | string | The saga space id. |
| `members` | array of member | Members in **topological order** of the `after` graph (roster order breaks ties; a cycle — impossible via validation — appends its members in roster order rather than dropping them). |
| `notes` | array of note, omitted when empty | Saga-level advisory findings: degradation and suggestions. |

Member row (`SagaMemberStatus`):

| Field | Type | Meaning |
|---|---|---|
| `id` | string | Member space id. |
| `after` | array of string, omitted when empty | Member ids this member lands behind. |
| `state` | string | Lifecycle state by filesystem evidence: `live`, `archived`, `missing`, or `corrupt`. |
| `error` | string, omitted when empty | The read error (`corrupt`) or the archive path (`archived`). |
| `dirty` | bool | True when **any** of the member's edit worktrees is dirty; always false for non-live members. |
| `repos` | array of repo, omitted when empty | Drift and base health per edit repo; empty for non-live members. |
| `prs` | array of PR, omitted when empty | Live PR state for the member's branches; populated only when a PR lookup ran this invocation. |

Repo row (`SagaRepoStatus`):

| Field | Type | Meaning |
|---|---|---|
| `name` | string | Repo name from the member manifest. |
| `branch` | string | The member's edit branch. |
| `base` | string | The recorded base as spelled in the manifest (`origin/main`, a bare branch name, or a full `refs/...` ref). |
| `ahead` | int | Commits the branch is ahead of its base. |
| `behind` | int | Commits the branch is behind its base. |
| `base_health` | string | `ok`, `merged`, `missing`, `owner_archived`, or `unknown` — see below. |
| `merged_via` | string, omitted when empty | `ancestry` or `pr`; set **only** when `base_health` is `merged`. |
| `note` | string, omitted when empty | Topology warnings, unknown reasons, and degraded probes for this repo (multiple findings join with `"; "`). |

PR row (`SagaPRStatus`):

| Field | Type | Meaning |
|---|---|---|
| `repo` | string | Repo name the PR belongs to. |
| `number` | int | PR number. |
| `state` | string, omitted when empty | gh PR state (`OPEN`, `MERGED`, `CLOSED`). |
| `merged_at` | string, omitted when empty | RFC 3339 merge timestamp. |
| `base_ref_name` | string, omitted when empty | The PR's live target branch on GitHub. |

Note (`SagaNote`):

| Field | Type | Meaning |
|---|---|---|
| `kind` | string | `degraded` (a probe could not run) or `suggestion` (advisory next step). |
| `member` | string, omitted when empty | The member the note is about; absent for saga-wide notes. |
| `text` | string | Human-readable finding. |

Representative example:

```json
{
  "saga_id": "checkout-rewrite",
  "members": [
    {
      "id": "checkout-api",
      "state": "live",
      "dirty": false,
      "repos": [
        {
          "name": "api",
          "branch": "stave/checkout-api/api",
          "base": "origin/main",
          "ahead": 3,
          "behind": 2,
          "base_health": "merged",
          "merged_via": "pr"
        },
        {
          "name": "web",
          "branch": "stave/checkout-api/web",
          "base": "origin/main",
          "ahead": 1,
          "behind": 0,
          "base_health": "ok"
        }
      ],
      "prs": [
        {
          "repo": "api",
          "number": 412,
          "state": "MERGED",
          "merged_at": "2026-07-20T18:04:11Z",
          "base_ref_name": "main"
        }
      ]
    },
    {
      "id": "checkout-web",
      "after": ["checkout-api"],
      "state": "live",
      "dirty": true,
      "repos": [
        {
          "name": "web",
          "branch": "stave/checkout-web/web",
          "base": "refs/heads/stave/checkout-api/web",
          "ahead": 3,
          "behind": 0,
          "base_health": "ok"
        }
      ]
    }
  ],
  "notes": [
    {
      "kind": "suggestion",
      "member": "checkout-api",
      "text": "repo api: the PR for branch stave/checkout-api/api merged but the branch is not an ancestor of refs/remotes/origin/main (permanently divergent after a squash merge); consider 'stave saga archive' or retargeting dependents"
    }
  ]
}
```

`saga status` is read-only by design: it performs zero manifest writes.

## `base_health` semantics

| Value | Meaning |
|---|---|
| `ok` | The base ref exists and the branch's work has not landed in its merge target. |
| `merged` | The base work has landed; `merged_via` says how (see below). |
| `missing` | The canonicalized base ref is **absent from the bare repo** (deleted branch, pruned remote ref). |
| `owner_archived` | The base is a sibling space's edit branch, and that **owner space is archived or destroyed while the ref persists** in the bare repo. The branch is orphaned: nobody is driving it toward a merge. |
| `unknown` | No verdict could be computed; `note` always carries the reason (no recorded base, self-referential base, base owned by no space, unreadable owner manifest, failed existence or ancestry check). |

`merged_via` distinguishes the two detection layers:

- `ancestry` — the relevant tip is a commit ancestor of its merge target *and*
  strictly behind it (a fresh branch whose tip merely equals its base is not
  reported merged — that would be noise on every new saga; the trade-off is
  that a just-fast-forwarded merge reads `ok` until the target moves).
- `pr` — a pull request with that head branch reports `MERGED` via the GitHub
  CLI. This is the layer that catches **squash merges**, which never become
  commit ancestors.

## The stacked-base merge-target rule

When a member's base is another sibling space's edit branch (a *stacked*
member), "merged" means the base landed in the **owner's own recorded base**
for that repo — one hop from the owner's manifest, never a transitive walk.
Two refinements from live PR state on the base branch:

1. A PR on the base branch that reports `MERGED` decides the verdict outright
   (`merged_via: "pr"`).
2. An open PR's `baseRefName` **overrides the recorded target** — retargeting
   a PR on GitHub redirects the ancestry check without any manifest edit.

If the owner records no base for the repo (and no PR supplies a target), the
verdict is `unknown` with a note. An archived/destroyed owner is
`owner_archived`; an owner with an unreadable manifest is `unknown`.

Status also flags stacking topology in repo `note`s: a base owned by a space
outside the saga, or by a member that is not among the member's
`after`-predecessors.

## Merged verdicts in human output

The human rendering of `saga status` prints the health inline on the base
line, e.g. `base: origin/main (merged (ancestry))` or
`(merged (PR #41))` — falling back to `(merged (PR))` when the merged PR row lives
on the owning member (stacked bases); `saga sync`'s summaries print
`base ... merged (ancestry)` / `merged (pr)` with a nudge to retarget
dependents or archive. The PR rows underneath carry the concrete PR numbers,
so a PR-merged verdict is attributable to a specific `#N` wherever the row exists.

## Squash-merge suggestion

A squash merge leaves the source branch permanently divergent: its PR is
`MERGED` but the branch never becomes an ancestor of its base. When status
detects this combination it emits a `suggestion` note recommending
`stave saga archive` for the member or retargeting its dependents — the
branch will report drift forever otherwise. Nothing is done automatically.

## GitHub CLI caveats

The PR layer runs through the `gh` binary (`internal/gh`), and only for
**GitHub-hosted repos** — clone URLs that don't parse as a GitHub host
(github.com, `*.ghe.com`, or any host with a `github` label, covering
conventional GitHub Enterprise names) skip PR detection entirely.

- **`--state all`**: `gh pr list` defaults to open PRs only; the very case
  this lookup exists for — an already-merged PR — would be invisible without
  it.
- **Host-qualified enterprise handling**: the host parsed from the clone URL
  is preserved and passed as `<host>/<owner>/<repo>` to `gh --repo`, so
  GitHub Enterprise clones address their own host instead of leaking to
  github.com via `GH_HOST`.
- **Degrade ladder**: a missing `gh` binary, or the *first* lookup failure of
  any kind, turns the PR layer off for the rest of the run. Detection falls
  back to commit ancestry only, and one aggregated `degraded` note reports it
  ("PR lookup unavailable (...); merge detection degraded to commit ancestry
  only"). Merge awareness never fails a status or sync — it degrades.
- Lookups are cached per clone-URL+branch within one invocation, so a base
  branch shared by several members costs one `gh` call.

## Saga-aware lifecycle: `saga archive` and `saga destroy`

`stave saga archive <saga-id>` and `stave saga destroy <saga-id>` tear down a
whole saga in **reverse topological order** of the `after` graph — dependents
go before the members they land after, so a stacked branch never outlives its
base mid-walk — with the saga space itself last. The entire walk runs under
one membership + per-saga lock hold.

**Fail-fast guards run before anything is torn down**, so a refusal leaves the
whole saga intact:

- A member with a **corrupt manifest aborts the operation up front** with
  nothing torn down.
- **Dirty edit worktrees across all live members** refuse before any teardown
  (`--force` overrides).
- Members' recorded bases owned by other members are exempt from the usual
  dependent-base guard (they are torn down in the same operation), but an
  **external dependent — a space outside the saga stacked on a member's
  branch — still refuses** exactly as single-space archive/destroy would.

**Skip semantics** differ by verb, and they are what make retries converge:

- `saga archive` **skips members that are already archived or missing**, with
  a printed note.
- `saga destroy` **skips missing members only**; archived members are never
  silently skipped and never auto-removed — each is **reported with its
  `.archive/` path** and left in place for you to keep or delete.

On a **partial failure** (a member teardown errors mid-walk), the operation
stops at the failing member, prints which members completed this run, and
leaves the saga record intact. Re-running the same command converges: members
torn down earlier now resolve as archived/missing and skip, so the retry picks
up where the failure stopped.

**Memory fates** apply to the saga space's own attachments (the saga den);
member teardowns always run with the default keep fate, so member-owned dens
survive as durable residue. `saga archive` refuses `--memory destroy` exactly
as single-space archive does — use `stave saga destroy --memory destroy`. A
den-destroying `saga destroy` fate hits the **den-refcount guard**: if any
*non-member* space still holds an unowned attachment of the saga den (a former
member after `saga remove`, or a sibling that attached it manually), the
destroy refuses and names the sharer, because destroying the den would strand
that attachment (`--force` overrides; unreadable sibling manifests fail
closed).

For a den-destroying fate the **saga den is destroyed first**, before any
member teardown. Marmot refuses the destroy with `source_in_use` while any
live agent session serves the den — and since every member's MCP config points
at the saga den, *any member's* live serve blocks it (see
[memory.md](memory.md#concurrent-serve)) — so the refusal fails the operation
with the **whole saga intact**: no member torn down, the attachment record
kept, and the members' saga-den MCP wiring (stripped just before the destroy
so no stale client can re-acquire the den mid-teardown) restored. `--force`
skips stave's own guards above — dirty worktrees, external dependents, the
den-refcount guard — but **marmot's refusal is not force-bypassable**: a
`--force` run still fails fast before any member teardown. Close the live
sessions and re-run; a successful den destroy records itself in the saga
manifest, so retries never re-destroy it. The same member-wiring strip/restore
applies to `stave memory detach <saga-id> --destroy`, which destroys the same
den outside the saga walk.

`--dry-run` prints the ordered plan — for a den-destroying fate the den-destroy
step first, then each member step with its skip/report disposition, then the
saga space — without changing state, followed by the provider's real dry-run
command lines for the den. One caveat: a destroying-fate dry-run **cannot
predict a live-serve refusal** — marmot's dry-run returns before lock
acquisition, while the real run fails fast before any member teardown.

### Machine-readable lifecycle output

`saga archive|destroy <saga-id> --json` emits, on success:

```json
{ "sagaId": "epic-js", "action": "archived", "memory": "keep",
  "sagaPath": "/…/agent-work/epic-js",
  "sagaArchivedPath": "/…/agent-work/.archive/epic-js",
  "members": [
    { "id": "js-2", "action": "archived", "path": "/…/agent-work/js-2", "archivedPath": "/…/agent-work/.archive/js-2" },
    { "id": "js-1", "action": "archived", "path": "/…/agent-work/js-1", "archivedPath": "/…/agent-work/.archive/js-1" },
    { "id": "js-0", "action": "skipped", "note": "already archived at /…/.archive/js-0", "path": "/…/agent-work/js-0", "archivedPath": "/…/agent-work/.archive/js-0" }
  ],
  "notes": ["archived js-2 to …", "…"] }
```

Members appear in teardown order. `path` is the member's live root before the
step (for a skipped member, where it would live); `archivedPath` is the
`.archive/` destination for members archived this run and the existing archive
for skipped already-archived members; `sagaArchivedPath` is the saga space's
own destination. Destroy rows and the destroy payload omit the archived paths.
`saga list --json` rows carry `path` and `logicalId` for the same reason.

On a **mid-walk failure** the error envelope's `details` merge the cause's own
details with the walk's progress:

```json
{ "error": { "code": "unknown", "message": "saga archive: member js-1: …",
  "details": {
    "completed": [{ "id": "js-2", "action": "archived", "path": "/…/agent-work/js-2", "archivedPath": "/…/agent-work/.archive/js-2" }],
    "failedAt": "member",
    "failedMember": "js-1" } } }
```

`completed` is always present (an empty array when the failure hit before any
member was torn down) and lists steps in walk order; `failedAt` is `member`,
`saga` (the saga space's own final step, `failedMember` = the saga id) or
`den` (den-first destroy refused; no `failedMember`). Preflight guard refusals
are not wrapped: they carry only their usual `code`/`details` because nothing
was torn down. In Go the same information is `space.SagaTeardownReport`,
returned by `SagaArchiveWithReport`/`SagaDestroyWithReport` and carried by the
`*space.SagaTeardownError` those return on a step failure (`errors.As` still
reaches the underlying coded error through `Unwrap`).

## The agent planner and sagas

The `stave agent` planner speaks sagas through three tools:

- `stave_saga_create` — propose creating a saga: a coordination space for
  sequenced multi-ticket work whose members are ordinary spaces. Sagas hold no
  editable worktrees; reference repos become read-only context under
  `references/`. Members are created with `stave_space_create` and registered
  with `stave_saga_add`.
- `stave_saga_status` — read-only inspection of an existing saga (members in
  dependency order with lifecycle state, drift, and base health), meant to run
  before proposing changes to a saga.
- `stave_saga_add` — propose registering a space as a member, optionally
  sequenced after other members. Re-adding an existing member with new `after`
  edges updates its edges — that edge update is a permitted mutation. Sagas
  cannot be members of other sagas.

**Merge detection in the planner is ancestry-scoped only**: `stave_saga_status`
performs no PR lookup, so a squash merge — whose branch never becomes a commit
ancestor of its target — reads as ordinary drift there. The planner payload
carries a note saying exactly this; for squash-merge detection use
`stave saga status --json`, which runs the full PR layer.

**No destructive saga operations are executable by the agent.** Saga archive,
saga destroy, saga remove, and space retarget have no planner tools; the
system prompt directs the model to record such requests via
`stave_explain_unsupported` instead of proposing them.

## The old-binary residual hazard

Saga manifests are schema version 2 with a **write ceiling**: a binary
refuses to *save* a manifest whose on-disk version exceeds what it
understands, so an old (pre-saga) `stave` can never load a v2 manifest,
drop the fields it doesn't know, and write back a stripped roster.

The ceiling protects the **data**, not the **operation**: reads are
permissive, so an old binary still runs `archive` or `destroy` against a v2
saga or member *version-blind* — it moves or deletes the directory without
consulting the roster, leaving dangling member entries (which status then
reports as `missing`) or an orphaned base (`owner_archived`). The current CLI
additionally redirects single-space archive/destroy of saga spaces and
registered members toward the saga-aware verbs; old binaries predate that
guard, so the residual hazard is precisely: *don't run lifecycle verbs from an
old binary inside a saga-bearing stave root*.
