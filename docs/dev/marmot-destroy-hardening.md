# Marmot destroy hardening — spec

Status: implemented (2026-07-31). All seven items shipped stave-side; behavior verified
against ContextMarmot commit `48cc12b` ("chore: consolidate local planning artifacts"),
the sibling repo's `main` at implementation time (the destroy-with-live-serve e2e in
`internal/cli/marmot_e2e_test.go` builds and exercises that HEAD directly).
Trigger: ContextMarmot commit `e62d8b9` ("feat: add search sync and impact workflows") on the
sibling repo's `main`. A read-only audit against the built marmot HEAD binary confirmed
stave's CLI/JSON contract surface is intact (envelope still schema-1, argv unchanged, routes
exact-key, no-pointer invariant holds, capability probes pass, contract fixtures undrifted,
stave's marmot e2e green) — but two behavior changes need stave-side work.

## The two marmot behavior changes

1. **`den destroy` now refuses with `source_in_use` while any `marmot serve --den <id>` is
   alive — and `--force` does NOT bypass.** Serve holds shared flocks on the den vault's
   watch + read locks for its whole process lifetime (`cmd/marmot/pipeline.go:452-467,
   510-515` at e62d8b9); the new `acquireDenDestroySourceLeases` (`cmd/marmot/den.go:
   2629-2659`) takes them exclusively. Empirically confirmed: destroy exits 1 with
   `{"code":"source_in_use", ...}` while serve runs, succeeds immediately after serve stops.
   Stave writes exactly that serve invocation into every space's MCP configs, so any live
   agent session holding the den blocks `stave space destroy --memory destroy|contribute`,
   `stave memory detach --destroy`, and `stave saga destroy --memory destroy|contribute`.
   Worst for sagas: every member's MCP configs point at the same den (MCP-only sharing), so
   a saga den almost always has a live consumer while agents work.

2. **`serve --den` now recursively watches and full-scans the den's registered project path
   (= the stave space) at startup.** It writes nothing into the space (the never-write
   invariant survives), but stave-attached dens auto-populate with indexed source-code nodes
   instead of staying purely agent-authored, and startup pays a scan proportional to repo
   size.

## Required changes (from the audit, refs verified pre-fix-commit — re-survey line numbers)

1. **Teardown ordering** (`internal/memory/marmot.go` ~907-923): MCP-config removal
   currently happens AFTER `den destroy`. For destroying fates, strip the MCP configs
   BEFORE destroy so a client cannot re-acquire the den mid-teardown. (Note: `WriteSpaceMCPConfig`
   is now merge-aware as of stave `4108297` — removal must keep using the entry-level strip.)
2. **Contribute idempotency** (`internal/memory/marmot.go` ~807-838): for
   `fate=contribute`, `den contribute` + `warren propose` run BEFORE destroy; a
   `source_in_use` refusal leaves a real warren commit with no idempotency marker, so a
   retry re-contributes (duplicate proposal branches). Make the contribute step idempotent
   across destroy retries (marker, or detect-already-contributed, or reorder — planner to
   evaluate against marmot's actual contribute semantics).
3. **Saga den-liveness preflight** (`internal/space/saga.go`, teardown preflight beside the
   den-refcount guard ~631): today a saga-den `source_in_use` refusal surfaces only after
   all members are already destroyed (saga torn down last). Add a fail-fast liveness check
   for destroying fates so the walk refuses before any teardown. Mechanism open: a cheap
   provider probe (e.g. attempt a non-destructive exclusive-lock acquisition, or a marmot
   verb if one exists) — planner must survey what marmot HEAD offers; if nothing
   non-destructive exists, consider ordering the saga den destroy FIRST (before members)
   for destroying fates, since member teardown never touches the den.
4. **Actionable error surfacing** (`internal/space/service.go` destroy paths): surface
   `source_in_use` as a stave-shaped refusal ("close agent sessions using <space>'s memory
   and retry") instead of a raw provider error. Stave has no process registry — marmot's
   refusal is the only signal; the message must say what to do.
5. **Multi-attachment partial destroy** (`internal/space/service.go` fate loop ~1213):
   if attachment #1 destroys and #2 refuses, #1's den is gone while `.stave.yaml` still
   lists it; a retry re-contributes #1 against a destroyed den. Make the loop
   record/skip already-completed fates so retries converge; add the missing test.
6. **Docs + e2e**: `docs/dev/stave-alignment-design.md` (~124) lists den-destroy refusals
   as unpushed-commits-only — extend for `source_in_use`. `docs/memory.md` documents the
   new source-watching behavior. `internal/cli/marmot_e2e_test.go` gains a
   destroy-with-live-serve case (spawn `serve --den`, attempt destroy, assert the
   stave-shaped refusal, stop serve, destroy succeeds) — exactly the case the current green
   e2e misses.
7. **Optional (decided: include): `watch_sources` opt-out.** Stave-created dens should stay
   agent-authored: set `watch_sources: false` in the den vault config at attach/create time
   so serve does not index the space's source tree. Planner must verify the exact config
   mechanism against marmot HEAD (vault `_config.md` key? den create flag? post-create
   config write?) and whether stave can set it without violating the never-write-into-the-
   space invariant (the den vault lives under MARMOT_HOME, not the space — confirm).
   Make it stave's default for stave-created dens; do not touch attached-existing dens.

## Constraints

- Provider contract posture: prefer zero changes to marmot; all fixes stave-side against
  marmot HEAD's actual behavior. If item 7's mechanism requires writing into the den vault,
  that is provider-owned state — do it through a marmot verb if one exists, else a direct
  vault-config write is acceptable only if marmot documents the file as user-editable.
- The audit's line refs predate stave commit `4108297` (saga validation/dry-run hardening,
  merge-aware MCP writes, git probe semantics) — survey current HEAD, not the refs above.
- Keep the frozen `SagaStatus` contract and all existing saga/lifecycle semantics
  untouched; this is hardening, not redesign.
- All work must keep the marmot e2e green against marmot HEAD, and the new live-serve e2e
  must be hermetic in the existing test_rig/e2e style (real marmot binary built from the
  sibling repo, as the e2e already does).

## Open questions (planner should propose answers)

- Item 3 mechanism: probe vs destroy-den-first ordering (or both).
- Item 2 mechanism: idempotency marker location (stave manifest? den state?) vs reorder.
- Should `stave memory detach --destroy` and `space destroy` get the same preflight as
  sagas, or is actionable-error-surfacing (item 4) sufficient for the single-space cases?
- `watch_sources` default: config knob in stave (`memory:` block) or unconditional?
