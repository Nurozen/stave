# Learned repo tethers — design

Status: implemented on branch `feat/repo-tethers` (base `ca2d334`); not yet
merged or committed to the default branch. This document is the
durable extraction of the feature spec and plan
(`artifacts/features/repo-tethers/`, session-local) — the data model, capture
semantics, and the bundled bugfixes, as built.

## Motivation

Stave already sees every `-e`/`-r` at create/add time but threw the information
away. Users re-type the same repo combinations from memory (editing `api` almost
always wants `web` as a read-only reference). Tethers **passively capture** which
editable and reference repos co-occur, **count** how often, and let a new
`-c/--common` flag **auto-pull** the learned references so a common combination
collapses to one short command. All data is local, private, and never leaves the
machine.

## Data model

### File and lock location

Learned tethers live in a single machine-global sidecar at
**`<config.Root>/repo-tethers.yaml`** (default `~/stave/repo-tethers.yaml`), with
a companion lock file `~/stave/repo-tethers.yaml.lock`. The path is computed by
`tether.Path(config.Config)` as `filepath.Join(cfg.Root, "repo-tethers.yaml")`.

A dedicated file (rather than `.stave.yaml` or `config.yaml`) is deliberate:
co-occurrence aggregates across **all** spaces (so per-space manifests are the
wrong home), it grows unboundedly and is rewritten on every create (so folding
it into `config.yaml` would bloat the viper round-trip and risk clobbering
hand-edited config under the unlocked last-writer-wins cycle). Living under
`config.Root` keeps machine-global metadata out of any per-space work tree. The
file is purely machine-generated — safe to delete and regenerate — and no
manifest schema bump is required (`CurrentManifestVersion` is untouched; old
binaries ignore the file entirely).

Human input does **not** live here: per-repo descriptions are stored on the
registry as `config.Repository.Description`, so wiping the sidecar loses nothing
hand-authored.

### Schema (`internal/tether/tether.go`)

```yaml
version: 1
tethers:
  - from: api            # the editable anchor
    to: web              # the associated repo
    toMode: reference    # reference | edit — how `to` co-occurred
    count: 7
    strength: strong     # strong | weak — effective classification (stamped on write)
    pinned: ""           # "", "strong", or "weak" — manual override of derived strength
    lastSeen: 2026-08-26T12:01:02Z
```

- Go types: `File{Version int; Tethers []Tether}` and `Tether{From, To string;
  ToMode Mode; Count int; Strength, Pinned Strength; LastSeen time.Time}`, each
  persisted field carrying dual `yaml`+`json` tags (`json` feeds `--json`
  output). `const CurrentTethersVersion = 1`.
- `Mode` (`"edit"`/`"reference"`) and `Strength` (`"strong"`/`"weak"`) are local
  string types in `internal/tether`, mirroring `space.RepoMode`'s values without
  importing `space` — `internal/space` calls `internal/tether`, so a reverse
  import would cycle. Values are cast at the space boundary.

### Identity and precedence

- A tether is uniquely identified by **`(from, to)`** — never by `(from, to,
  toMode)`. `from == to` is invalid and never recorded. Keys are repo **names**
  (the `config.Repos` map key / `RepoSpec.Name`), not paths, so `space:<id>`
  base sugar (which lives in `Ref`, not `Name`) does not fragment the key.
- `toMode` is a stored attribute with deterministic **edit-over-reference**
  precedence: if the same repo co-occurs as both an edit and a reference in one
  capture, the row records `edit` and is counted once.
- Strength is **derived by threshold**, not stored authoritatively: a tether is
  `strong` iff `count >= tethers.strongThreshold` (default 3), else `weak`. A
  manual `pinned` value wins outright over the derived value
  (`EffectiveStrength`). `count` keeps incrementing regardless of pin. Lowering
  the threshold promotes existing tethers on their next capture, and
  `repos tethers` recomputes effective strength at read time so it is always
  current even without a write.

### Atomic write + locking

- `Save(path, *File)` marshals YAML, `os.MkdirAll(dir, config.DefaultDirMode)`,
  then `fsio.WriteFileAtomic(path, data, config.DefaultConfigMode)` (0o600).
  Save is deliberately **threshold-blind**: it never recomputes strength (it
  can't know `strongThreshold` without coupling to config). Effective strength is
  stamped by the threshold-aware callers (`stampStrength`) before Save.
- Save follows the manifest **version ratchet**: `saveWithCeiling` refuses to
  write when the on-disk version exceeds `CurrentTethersVersion` (typed
  `ErrTethersVersionTooNew`) and never steps the stamped version down.
- `Load(path)` returns an empty `File{Version: CurrentTethersVersion}` on
  `os.IsNotExist` (first capture creates it) — unlike `space.LoadManifest`, which
  errors on absence.
- All mutation goes through `Update(path, fn)`: `MkdirAll` up front (the lock
  file needs its dir), then `fsio.WithLock(path+".lock", …)` wrapping a
  load → fn → save. Helpers used inside `fn`: `Bump`, `Pin`, `Common`, `Find`,
  `Remove`/`RemoveAll`, `EffectiveStrength`.
- **Windows**: `WithLock` is a no-op, so concurrent captures can lose an
  increment (counts are advisory; accepted for v1). Atomic rename still prevents
  torn reads.

## Capture semantics (`internal/space`)

Capture is passive, best-effort, and lock-free relative to the saga locks. Both
capture helpers live in `internal/space/capture.go`, mirror `linkMemoryOnAdd`
(never return an error — a failure prints `notice: could not record repo
tethers: …` and the space stands), and each does its own single `tether.Update`
transaction.

### Capture points

- **`Service.Create`, non-saga path** — after the space is durably materialized,
  guarded `if !opts.DryRun && !opts.suppressCapture && !opts.NoLearn`. Calls
  `captureCoOccurrence(opts.Edits, opts.References)`. Covers `stave space create`.
- **`Service.Create`, saga path** — `Create` runs `createInSaga` (which holds the
  membership and per-saga locks) and then, once those locks have **released**,
  captures with `if !opts.DryRun && !opts.NoLearn`. This keeps the tethers lock
  from ever nesting under the saga locks (`fsio.WithLock` is non-reentrant).
- **`createInSaga`** sets `inner.suppressCapture = true` on the inner
  `CreateOptions` it composes, so the member's realized set is recorded exactly
  once — by the outer saga path, not the inner non-saga `Create`. This is the
  fix for the spec's double-count.
- **`Service.AddRepo`** — after the manifest is durably saved, guarded
  `if !opts.DryRun && opts.CaptureOnAdd && !opts.NoLearn`. Calls
  `captureDeltaOnAdd`. Create's internal `AddRepo` loop leaves `CaptureOnAdd`
  false, so a multi-repo create captures once (via `captureCoOccurrence`), never
  per repo.
- **Review spaces learn** — `stave review` builds via `InitSpace` + `AddRepo`
  with `CaptureOnAdd: true`, so delta capture records `prHead → sibling(ref)` for
  each `-r`. Review pairings are learned like any other create/add.
- **`CreateSaga`** (references-only saga) reaches `Create` with empty `Edits`;
  with no editable anchor, `captureCoOccurrence` early-returns and nothing is
  learned (v1 decision).

### What is recorded

- `captureCoOccurrence(edits, references)` (full-create form): early-returns when
  `len(edits) == 0` or `!Config.Tethers.IsEnabled()`. It builds the associate set
  `A = edits ∪ references` with **edit precedence** (a name in both sets is an
  edit associate), then for each edit `e` and each associate `a ≠ e` records one
  `Bump(e → a)` in a single `Update`, carrying `a`'s mode as `ToMode`. De-dup is
  by `(from, to)`. If fewer than two distinct repos are present (a solo edit), it
  skips entirely rather than open and rewrite an empty file.
- `captureDeltaOnAdd(prior, addedName, addedMode)` (delta form for `space add`):
  records **only** pairs involving the newly added repo, never old-to-old.
  - Added as a **reference**: each prior editable `p` gains `p → added(ref)`.
  - Added as an **editable**: `added → a` for every prior associate `a`, plus
    `p → added(edit)` for every prior editable `p`.
- After bumping, `stampStrength` writes each touched tether's effective
  `Strength` so the persisted file carries a meaningful classification (the
  threshold lives in the space layer, not in `tether.Save`).

### Gates and failure isolation

- **Dry-run** never writes (all three paths guard on `opts.DryRun`;
  `createDryRun` splits before the capture point).
- **`--no-learn`** (per-invocation) threads `NoLearn` through `CreateOptions`,
  `AddOptions`, and `SagaCreateOptions` and suppresses capture for that command.
- **`tethers.enabled: false`** (global kill switch) makes both capture helpers
  no-ops via `Config.Tethers.IsEnabled()`.
- Capture reflects **durable materialization**, not command success: it fires
  after the manifest is durably saved, so a later best-effort failure (memory
  attach, `writeAgents`) still records the learning. A saga member whose
  *registration* fails (space created but roster update errored) does not learn —
  the create as a whole failed. Acceptable and conscious.

### `-c/--common` expansion

`Service.ExpandCommonRefs(cfg, edits, explicitRefs, includeWeak)` lives on the
service (in `capture.go`), not in `internal/tether` — `internal/cli` imports
`internal/agent` and cannot be imported by it, so a shared CLI helper would
cycle. `internal/space` is the common dependency both the CLI and the agent
executor call, so a planned create and a typed create expand identically.

- It **errors immediately** if `!cfg.Tethers.IsEnabled()` (`-c` is meaningless
  with learning off — a hard error, not a silent empty expansion).
- Otherwise it loads the tethers file read-only, and for each edit collects
  `tether.Common(edit, includeWeak, strongThreshold)` (strong-only by default,
  strong+weak with `--include-weak`).
- Results are de-duplicated by name against the explicit `-r` references and the
  `-e` edits, and any `to` not registered in `config.Repos` is skipped with an
  accumulated note.
- The CLI/agent place the returned specs into **`CreateOptions.CommonReferences`**
  (not `References`). `Create` materializes reference worktrees for
  `References ∪ CommonReferences`, but `captureCoOccurrence` is called with only
  `Edits, References` — so `-c`-expanded refs get worktrees **without** inflating
  their own counts. `-c` only ever adds references; it never materializes
  editable worktrees.

## Config split (`internal/config`)

Two additions, both dual-tagged (`mapstructure` + `yaml`) so neither is silently
dropped on the viper round-trip:

- `Repository.Description string` (`omitempty`) — registry-owned human
  description, set via `stave repos describe`.
- `TethersConfig{Enabled *bool; StrongThreshold int}` on `Config` under the
  `tethers:` key, with `DefaultTethersConfig()` = `{Enabled: ptr(true),
  StrongThreshold: 3}`, wired into `Default()`, the `SetDefault` block, and an
  `ApplyDefaults` step.
  - `Enabled` is a **`*bool`**, not a plain `bool`: `yaml.v3` `omitempty` drops
    `false`, and `SetDefault(true)` would then restore it — so a plain-bool kill
    switch could never persist. `nil` = default-true; `*false` survives a
    round-trip. `IsEnabled()` returns `Enabled == nil || *Enabled`.
  - `ApplyDefaults` fills `Enabled` when nil and **clamps `StrongThreshold` back
    to 3 when `< 1`** (a zero/negative threshold would make everything strong).

## `stave repos` surface (`internal/cli/root.go`)

- `repos describe <repo> [text]` — sets `Repository.Description` (load → mutate
  map entry → `cfg.Save`) when text is given; prints it otherwise (non-zero exit
  if unset).
- `repos tethers <repo> [--json]` — filters the tethers file to `From == repo`,
  computes effective strength, sorts strong-first then by count, and prints text
  rows or a typed `tetherRow` JSON array.
- `repos tether <from> <to> [--strong|--weak] [--edit|--reference]` — pins a
  tether manually; strength defaults to strong, association mode to reference
  (`--strong`/`--weak` and `--edit`/`--reference` are each mutually exclusive).
- `repos forget <from> [<to>] [--all]` — removes one pair or (with `--all`) every
  tether from `<from>`; requires exactly one of `<to>` or `--all`.
- `repos list [--verbose]` — `--verbose` appends each repo's description and its
  learned tether count.

The agent planner exposes a read-only `stave_repos_tethers` tool, and
`stave_space_create` carries `common`/`include_weak` boolean args that map to the
same `ExpandCommonRefs` path used by the CLI.

## Bundled bugfixes (BF1–BF5)

Surfaced by the 2026-08-26 deep-explore as advertised-but-broken or dead code;
implemented alongside because they touch adjacent files.

- **BF1 — real tmux-backed portal summon.** `PlanSummon` previously branched only
  `headless` vs not, so `foreground`/`tmux`/`print` all produced the same
  interactive exec. `--mode tmux` now emits a single idempotent
  `tmux new-session -A -d -s <session> <agentArgv…>` (`-A` = attach-or-create so
  it never errors on an existing session, `-d` = detached — works in both the CLI
  executor and the agent executor, which ignores `ContinueOnError`). It appends
  `tmux attach-session -t <session>` only when a real TTY is present; headless and
  non-interactive runs stay detached and print how to attach (prevents CI hangs
  and keeps the piped e2e harness viable).
  - **Session name** is summoner-independent: `tmuxSessionName(space, portal) =
    "stave-<space>-<portal>"`. Portals do not persist a summoner
    (`SummonOptions.With` is invocation-local), so the name cannot depend on one.
    `PlanLogs` uses the same helper.
  - **Auth env survives the wrapper.** Wrapping the agent in `tmux` makes
    `argv[0]` become `tmux`, which would drop the provider env derived from
    `argv[0]` (docker `-e OPENAI_API_KEY`/`CODEX_HOME`, ssh `SendEnv`).
    `portalExecCommandFor` takes the nested summoner explicitly so the auth env is
    still emitted.
  - **Limitation:** `stave portal logs` pane-capture after a tmux summon works
    only for **ssh/ec2** portals. Docker/devcontainer logs read container stdout,
    not the tmux pane. An info diagnostic notes tmux must be installed on the
    target (it can't be probed at plan time); the e2e ssh-host image adds `tmux`.
- **BF2 — removed dead `portal destroy --delete-remote-data`.** The
  `DestroyOptions.DeleteRemoteData` field was plumbed but never read (remotes are
  attach-only by design). Flag, field, and CLI wiring removed.
- **BF3 — honest cursor notice.** cursor summon/auth was a silent stub.
  `PlanSummon` now appends a `SeverityWarn` diagnostic
  (`Code: summon.cursor_partial`) — and the auth path a matching notice — that
  cursor support is partial (launches `cursor-agent` without prompt/permission
  wiring), instead of behaving silently.
- **BF4 — `stave version`.** `.goreleaser.yml` injects `-X main.version/commit/
  date` into vars that now exist in `cmd/stave/main.go`. A `stave version` command
  (`cobra.NoArgs`) prints them, threaded into `internal/cli` via `cli.Build*`
  package vars seeded onto the `app`. `resolveBuildInfo` prefers the injected
  values and falls back to `runtime/debug.ReadBuildInfo` for `go install`ed
  binaries; missing fields print as `unknown`. Vars stay in `package main` so the
  release ldflags keep resolving.
- **BF5 — removed discarded `portal detach --yes`.** The flag was parsed into a
  var that was immediately `_ =`'d (no prompt to skip). Flag and var removed.
