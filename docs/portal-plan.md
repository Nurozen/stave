# Portal Feature Plan

## Summary

Portal is a Stave-managed execution attachment for an existing space. A
portal does not replace the local Stave space and does not become the source of
truth. It gives a space a doorway into compute: a local container, a
devcontainer, a Docker runtime, an SSH host, or an existing EC2 instance
that is attached through SSH.

The core rule is:

```text
Stave owns the space. Portal owns the execution attachment.
```

The local space remains canonical:

```text
agent-work/<space-id>/
+-- .stave.yaml
+-- .stave-portal.yaml
+-- AGENTS.md
+-- CLAUDE.md -> AGENTS.md
+-- spec/
+-- <editable repo>/
+-- references/
```

Portal commands operate from the space root, preserve Stave's editable vs
reference repo distinction, and make credential and cleanup boundaries explicit.

## Research Process

This plan was assembled from one research agent per command surface:

- `portal auth`
- `portal init`
- `portal up`
- `portal status` / `portal list`
- `portal sync`
- `portal shell` / `portal exec`
- `portal summon`
- `portal down` / `portal destroy`

After command-surface research, two plan-integrator agents checked product
alignment and implementation fit against the current Go repo shape.

The research used current vendor documentation for Codex, Claude Code,
Docker, Dev Containers, OpenSSH, rsync, Git worktrees, and AWS EC2.
Important links are collected in [References](#references).

## Goals

- Attach execution environments to existing Stave spaces.
- Support local container/devcontainer workflows first.
- Support SSH and EC2 attachment without taking ownership of remote machines.
- Launch Codex, Claude Code, or Cursor Agent inside the portal environment.
- Keep auth explicit and auditable. Never silently copy local credentials.
- Make destructive cleanup targeted and limited to Stave-owned resources.
- Preserve scriptability with `--json`, `--dry-run`, and print-command modes.
- Design the natural-language agent contract now, then ship agent execution
  after direct CLI behavior is implemented and tested.
- Keep Go test coverage strictly greater than 90% for the portal work and the
  affected command/agent surfaces.

## Non-Goals For V1

- No cloud provisioning. No VPC, security group, AMI, key pair, or
  `run-instances` lifecycle.
- No silent credential copying from the host to a portal.
- No global Docker prune commands.
- No remote host filesystem deletion outside the recorded portal root.
- No mutation of `.stave.yaml` for transient runtime state.
- No implicit branch moving, reset, rebase, push, or PR creation.

## Command Tree

Recommended initial tree:

```text
stave portal
+-- init <space-id> [portal-id]
|   +-- container <space-id> [portal-id]
|   +-- devcontainer <space-id> [portal-id]
+-- attach
|   +-- ssh <space-id> <host> [portal-id]
|   +-- ec2 <space-id> <instance-id> [portal-id]
+-- configure <space-id> [portal-id]
+-- drivers
+-- doctor <space-id> [portal-id]
+-- list [space-id]
+-- status <space-id> [portal-id]
+-- inspect <space-id> [portal-id]
+-- auth
|   +-- status <space-id> [portal-id]
|   +-- login <space-id> [portal-id]
|   +-- inherit <space-id> [portal-id]
|   +-- revoke <space-id> [portal-id]
+-- up <space-id> [portal-id]
+-- sync <space-id> [portal-id]
+-- shell <space-id> [portal-id]
+-- exec <space-id> [portal-id] -- <command...>
+-- summon <space-id> [portal-id]
+-- logs <space-id> [portal-id]
+-- down <space-id> [portal-id]
+-- detach <space-id> [portal-id]
+-- destroy <space-id> [portal-id]
```

Argument conventions:

- `<space-id>` is always the first positional argument for space-scoped
  commands.
- `[portal-id]` defaults to `default`.
- `portal init <space-id>` is the guided default and should ask only the
  questions needed for the selected portal kind.
- `portal init container` and `portal init devcontainer` create Stave-owned
  local runtime attachments.
- `portal attach ssh` and `portal attach ec2` attach existing remote resources.
  They do not imply that Stave owns those machines.
- Dense flags are escape hatches for scripts. The primary user experience
  should be guided setup, presets, and follow-up `configure` commands.

## UX Principles

- Opening the first portal should feel like choosing a doorway, not configuring
  every runtime detail up front.
- Keep mode-specific vocabulary close to the mode. Container image flags belong
  on `portal init container`; SSH host flags belong on `portal attach ssh`.
- `init` means Stave-owned local runtime setup. `attach` means external resource
  attachment.
- `configure` handles advanced tuning after the portal exists.
- Every guided flow should print the manifest preview and equivalent
  non-interactive command before writing, unless `--yes` is provided.
- Every non-interactive setup path should have `--dry-run`.

## Presets

Presets should cover common first-run flows:

```text
stave portal init <space-id> --preset local-codex
stave portal init <space-id> --preset local-claude
stave portal init <space-id> --preset claude-devcontainer
stave portal attach ssh <space-id> <host> --preset ssh-codex
```

Suggested presets:

| Preset | Driver | Sync | Agent | Auth |
| --- | --- | --- | --- | --- |
| `local-codex` | `docker` | `mount` | `codex` | `native` |
| `local-claude` | `docker` | `mount` | `claude` | `native` |
| `claude-devcontainer` | `devcontainer` | `mount` | `claude` | `native` |
| `ssh-codex` | `ssh` | `rsync` | `codex` | `remote-login` |
| `ssh-claude` | `ssh` | `rsync` | `claude` | `remote-login` |

## Driver Model

| Driver | V1 behavior | Ownership |
| --- | --- | --- |
| `docker` | Create or reuse a local Docker container around the space. | Stave-owned when created by portal. |
| `devcontainer` | Use devcontainer configuration to start or exec into a container. | Stave-owned only for Stave-created runtime resources. |
| `ssh` | Attach to an existing SSH host and remote root. | Attach-only. |
| `ec2-attach` | Attach to an existing instance through AWS metadata plus SSH. | Attach-only. |

`drivers` should list supported drivers and whether each driver is
create-capable or attach-only.

## Portal Manifest

Persist portal metadata separately from `.stave.yaml`:

```yaml
version: 1
spaceID: ticket-482
portals:
  default:
    id: default
    driver: docker
    createdAt: "2026-06-02T00:00:00Z"
    workspace:
      localPath: ~/stave/agent-work/ticket-482
      remoteRoot: ""
      containerRoot: /workspace/ticket-482
      syncMode: mount
    target:
      host: ""
      port: 22
      instanceID: ""
      region: ""
      dockerContext: ""
    runtime:
      engine: docker
      image: ghcr.io/nurozen/stave-dev:latest
      containerName: stave-ticket-482-default
      projectName: stave_ticket_482
      service: workspace
      devcontainerPath: ""
      composeFiles: []
      labels:
        stave.space: ticket-482
        stave.portal: default
    auth:
      mode: native
      providers:
        - provider: codex
          mode: native
          target: portal
          status: unknown
          secretRef: ""
          lastChecked: ""
          warnings: []
    ownership:
      createdContainer: true
      createdVolumes: []
      createdNetworks: []
```

Rules:

- `.stave.yaml` continues to describe the workspace and repos.
- `.stave-portal.yaml` describes portal attachments.
- Runtime IDs can be recorded when needed for later cleanup, but volatile
  status is produced by `portal status`, not trusted blindly from the manifest.
- Every Stave-created runtime resource should have labels such as
  `stave.space=<space-id>` and `stave.portal=<portal-id>`.
- Auth metadata is reference/status data only. The manifest never stores raw API
  keys, OAuth tokens, provider cache bytes, JSON credential blobs, or copied
  config payloads.

## Command Contracts

### `portal init`

Purpose: create portal metadata for a Stave-owned local runtime attached to an
existing space. It does not create a space and does not start compute.

The bare command is guided:

```text
stave portal init <space-id> [portal-id]
  --preset local-codex|local-claude|claude-devcontainer
  --yes
  --dry-run
```

Guided flow:

1. Confirm the target space.
2. Ask for portal kind: local container, devcontainer, SSH host, or EC2
   attachment.
3. Route local runtime choices to the equivalent `portal init ...` command.
4. Route external resource choices to the equivalent `portal attach ...`
   command.
5. Show the manifest preview and equivalent non-interactive command.
6. Write `.stave-portal.yaml` only after confirmation.

Scriptable local-runtime setup:

```text
stave portal init container <space-id> [portal-id]
  --image <image>
  --engine docker
  --container-root <path>
  --preset local-codex|local-claude
  --dry-run

stave portal init devcontainer <space-id> [portal-id]
  --path <devcontainer.json>
  --repo <repo-name>
  --service <name>
  --compose-file <path>       repeatable
  --container-root <path>
  --preset claude-devcontainer
  --dry-run
```

Defaults:

- `portal-id`: `default`
- `container` engine: `docker`
- `container-root`: `/workspace/<space-id>`
- `sync`: `mount` for local Docker/devcontainer

### `portal attach`

Purpose: attach an existing external resource to a Stave space. Stave does not
own the remote machine or cloud resource.

```text
stave portal attach ssh <space-id> <host> [portal-id]
  --remote-root <path>
  --port <port>
  --identity <path>
  --known-hosts <path>
  --strict-host-key yes|no|ask|accept-new
  --sync rsync|reconstruct
  --preset ssh-codex|ssh-claude
  --dry-run

stave portal attach ec2 <space-id> <instance-id> [portal-id]
  --region <region>
  --profile <aws-profile>
  --ssh-user <user>
  --identity <path>
  --remote-root <path>
  --sync rsync|reconstruct
  --preset ssh-codex|ssh-claude
  --dry-run
```

Defaults:

- `remote-root`: `~/stave/agent-work/<space-id>`
- SSH `sync`: `rsync`
- EC2 driver: persisted as `ec2-attach`
- EC2 behavior: attach only; do not provision, stop, terminate, or delete the
  instance.

### `portal configure`

Purpose: tune an existing portal after setup without overloading `init`.

```text
stave portal configure <space-id> [portal-id]
  --sync mount|rsync|reconstruct
  --container-root <path>
  --remote-root <path>
  --agent codex|claude|cursor
  --auth native|env|volume|copy-cache|ssh-forward
  --dry-run
```

Use `configure` for advanced changes that are not required to open the first
portal.

### `portal auth`

Purpose: check, perform, inherit, or revoke provider auth for a portal target.
It is an auditor and orchestrator, not a silent secret transport.

```text
stave portal auth status <space-id> [portal-id]
  --provider codex|claude|cursor|all
  --json
  --show-paths

stave portal auth login <space-id> [portal-id]
  --provider codex|claude|cursor
  --method browser|device|api-key|access-token|oauth-token|console|sso|native

stave portal auth inherit <space-id> [portal-id]
  --provider codex|claude|cursor
  --method env|volume|copy-cache|ssh-forward
  --dry-run
  --yes

stave portal auth revoke <space-id> [portal-id]
  --provider codex|claude|cursor
  --target local|portal|all
  --yes
```

Auth posture:

- Default to provider-native login inside the target.
- Do not mount local `~/.ssh`, cloud credential files, `~/.codex`, or
  `~/.claude` by default.
- `copy-cache` is allowed only with explicit `--method copy-cache --yes` and a
  warning that copied token material must be treated like a password.
- `revoke` removes local cached credentials on the selected target. If the
  provider requires server-side revocation, Stave prints provider-side guidance
  instead of pretending local deletion is enough.

Provider notes:

- Codex: use `codex login`, `codex login --device-auth`,
  `codex login --with-access-token`, `codex login --with-api-key`, and
  `codex login status` where available.
- Claude: use `claude auth login`, `claude auth status`, `claude auth logout`,
  and `claude setup-token` for CI/script token flows.
- Cursor: use `cursor-agent status` and native Cursor Agent auth once portal
  support is added for Cursor.

### `portal up`

Purpose: start or reuse the declared portal runtime. It is not cloud
provisioning.

```text
stave portal up <space-id> [portal-id]
  --attach shell|none
  --workdir <path>
  --print-command
  --dry-run
  --json
```

Semantics:

- Docker/devcontainer may create local declared runtime objects.
- SSH validates reachability and remote root. It does not start or stop the
  remote host.
- `ec2-attach` validates the instance and connection metadata. It does not
  provision instances. Starting a stopped instance should be deferred or require
  a later explicit command/flag such as `--start-existing-instance`.
- Non-interactive or JSON mode does not attach a shell. It reports planned or
  executed commands and status.
- Container attachment should default to `exec` shell, not PID 1 attach.

### `portal status` and `portal list`

Purpose: read-only diagnostics.

`portal list [space-id]` should be dense:

```text
SPACE  PORTAL   DRIVER        STATE     HEALTH      AUTH        NOTES
ex-1   default  docker        running   healthy     codex:ok    -
```

`portal status <space-id> [portal-id]` should expand:

```text
portal default (ok)
space: ex-1
driver: docker
runtime: container=stave-ex-1-default state=running health=healthy
workspace: local=/.../agent-work/ex-1 container=/workspace/ex-1
agents: codex=ok claude=missing cursor=unknown
diagnostics:
  [warn] agent.claude.missing: claude not found in portal PATH
```

Normalized status model:

- `overall`: `ok|warn|error|unknown`
- container state and health
- SSH reachability/authentication
- agent installation and auth status
- diagnostics with `component`, `severity`, `code`, `message`, `evidence`,
  and `nextAction`

### `portal doctor`

Purpose: preflight dependencies and security posture.

Checks:

- Driver binary exists: Docker, devcontainer CLI, SSH, rsync, AWS CLI.
- Portal manifest exists and matches the space.
- Space root exists and `.stave.yaml` loads.
- Container/remote roots are non-empty and safe.
- Docker remote context or SSH host is reachable.
- Credential posture is explicit and no forbidden host secret mounts are
  configured.
- For remote sync, rsync is available on both sides.

### `portal inspect`

Purpose: show resolved configuration and ownership.

It should answer:

- Which driver is selected?
- Which runtime/resource IDs are Stave-owned?
- Which paths can `sync`, `down`, `detach`, and `destroy` affect?
- What would `destroy --dry-run` delete?

### `portal sync`

Purpose: reconcile workspace files between the local space and portal target.

```text
stave portal sync <space-id> [portal-id]
  --direction to|from|both
  --mode auto|mount|rsync|reconstruct
  --references-only
  --include <pattern>       repeatable
  --exclude <pattern>       repeatable
  --delete
  --max-delete <n>
  --allow-dirty
  --dry-run
  --yes
```

Modes:

- `mount`: local container bind mount. No file copy.
- `rsync`: remote content sync. Default direction is `to`.
- `reconstruct`: rebuild the remote Stave space shape from manifests and Git
  configuration, then sync only non-Git working tree deltas.

Safeguards:

- Do not rsync `.git` or worktree admin files as authoritative state.
- Use Git to reconstruct worktrees when possible.
- Refuse destructive pulls when local editable repos are dirty unless
  `--allow-dirty` is set.
- No deletes by default. `--delete` requires dry-run preview or explicit `--yes`
  and supports `--max-delete`.
- References remain conservative: dirty reference worktrees are skipped, matching
  current Stave `space sync` behavior.

### `portal shell` and `portal exec`

Purpose: enter or run commands in the portal environment.

```text
stave portal shell <space-id> [portal-id]
  --cwd <path>
  --user <user>
  --tty auto|always|never
  --print-command

stave portal exec <space-id> [portal-id] -- <command...>
  --cwd <path>
  --user <user>
  --tty auto|always|never
  --json
```

Defaults:

- `shell` is interactive and defaults to `--tty auto`.
- `exec` is script-friendly and defaults to no TTY.
- Working directory is the portal's space root.
- Non-interactive shell invocations print the equivalent command or fail with a
  clear message.

Driver mapping:

- Docker: `exec -it -w <cwd> <container> <shell>`
- Compose/devcontainer: use the service/container selected by the manifest.
- SSH: run `ssh -t <host> 'cd <cwd> && exec ${SHELL:-sh}'`

### `portal summon`

Purpose: launch a coding agent inside the portal environment.

```text
stave portal summon <space-id> [portal-id]
  --with codex|claude|cursor
  --mode foreground|tmux|headless|print
  --permission read-only|workspace-write
  --json
```

Rules:

- The agent starts at the Stave space root inside the portal.
- The prompt reinforces the Stave workspace contract: inspect `.stave.yaml`,
  `AGENTS.md`, `spec/`, editable repos, and read-only `references/`.
- `stave summon <space-id>` remains local. `stave portal summon` is remote or
  container execution.
- Auth preflight runs first. If auth is missing, Stave suggests
  `portal auth login`, not credential copying.
- Use Stave-owned tmux sessions for background interactive sessions:
  `stave-<space-id>-<portal-id>-<summoner>`.

Provider mapping:

- Codex interactive: `codex --cd <space-root> <prompt>`
- Codex headless: `codex exec --cd <space-root> --sandbox workspace-write --json <prompt>`
- Claude interactive: run `claude <prompt>` with process cwd set to the space
  root.
- Claude headless: `claude -p --output-format stream-json <prompt>`
- Cursor: use `cursor-agent` once the command and status behavior are pinned.

### `portal logs`

Purpose: view logs for Stave-owned local runtimes and tmux-backed sessions.

```text
stave portal logs <space-id> [portal-id]
  --agent codex|claude|cursor
  --follow
  --tail <n>
```

For attach-only SSH/EC2 portals, logs are limited to Stave-created tmux/session
logs and explicit portal sync/exec records. Stave does not infer arbitrary
remote service logs.

### `portal down`

Purpose: stop portal runtime while preserving data.

```text
stave portal down <space-id> [portal-id]
  --timeout <seconds>
  --force
  --dry-run
```

Rules:

- Docker: stop Stave-owned containers gracefully.
- Compose/devcontainer: stop/down declared services without deleting named
  volumes unless the driver contract says otherwise and ownership is recorded.
- SSH/EC2 attach: do not stop remote hosts or instances. Use `detach`.
- `--force` may force-stop Stave-owned containers, but does not expand deletion
  scope.

### `portal detach`

Purpose: remove portal attachment metadata for attach-only targets.

```text
stave portal detach <space-id> [portal-id]
  --dry-run
```

Rules:

- `--yes` was removed in the repo-tethers change (see
  `docs/dev/repo-tethers-design.md`, BF5); `detach` never prompted, so the flag
  was inert.
- For SSH/EC2, this removes local portal metadata and optional local sync cache.
- It does not delete remote paths unless the user later requests an explicit
  remote-data deletion command.
- It does not stop, terminate, or modify EC2 instances.

### `portal destroy`

Purpose: delete Stave-owned runtime resources and portal-owned data.

```text
stave portal destroy <space-id> [portal-id]
  --timeout <seconds>
  --delete-volumes
  --force
  --dry-run
```

Rules:

- Destroy only resources Stave can prove it owns from manifest IDs and labels.
- Never run unfiltered `docker system prune`, `docker volume prune`, or global
  network/image prune.
- `--delete-volumes` applies only to Stave-owned named volumes recorded in the
  portal manifest.
- `--delete-remote-data` was removed in the repo-tethers change (see
  `docs/dev/repo-tethers-design.md`, BF2); it was never implemented. Remote paths
  are not deleted by `destroy`.
- For SSH/EC2, default to `detach`, not `destroy`.

## Auth Design

Auth should be explicit, provider-native, and target-scoped.

Recommended auth modes:

| Mode | Meaning | Default? |
| --- | --- | --- |
| `native` | Run the provider login command inside the target. | Yes |
| `env` | Pass a configured env secret for one operation. | Yes for automation |
| `volume` | Use a named container volume for target-local credentials. | Yes for trusted local containers |
| `ssh-forward` | Forward an auth callback/session path explicitly. | Optional |
| `copy-cache` | Copy provider credential cache. | Explicit only |

Credential rules:

- Local login info is never used silently.
- `portal summon` may be seamless once the portal target is authenticated.
- `portal summon` should not perform first-time credential transfer.
- Host secret mounts are not a default.
- Copied Codex or Claude token files must be treated like passwords.

Portal-agent chat secret boundary:

- `agent.providers.*.apiKeyRef` configures the planner model used by
  `stave agent`; it is not portal target auth.
- `stave agent` may resolve OpenAI or Anthropic keys only on the trusted local
  side, in memory, to call its own provider.
- Planner credentials must never enter `.stave-portal.yaml`, portal tool
  arguments, queued operations, JSON output, logs, read results, traces, or
  equivalent commands.
- Codex, Claude, and Cursor auth inside a portal is represented as
  non-secret target auth metadata and checked through `portal auth status`.
- Missing target auth produces guidance such as `stave portal auth login ...`;
  it does not trigger implicit local credential reuse.
- Agent-planned flows cannot queue `copy-cache`. That method requires direct
  CLI intent with `portal auth inherit --method copy-cache --yes`.

## Sync And Workspace Binding

Portal has three binding modes:

```text
mount       local container/devcontainer with host-visible space root
rsync       remote content sync over SSH
reconstruct remote Stave space rebuild from manifest and Git
```

Use `mount` only when the container daemon can see the source path. Remote Docker
hosts require remote host paths or named volumes, not local client paths.

Use `rsync` as remote default:

```text
rsync -aiz --dry-run --delete-delay --delay-updates \
  --partial-dir=.rsync-partial \
  --exclude=.git --exclude=.git/ \
  <space-or-repo>/ user@host:<target>/
```

Use `reconstruct` when correctness matters more than quick copying:

1. Copy `.stave.yaml`, `.stave-portal.yaml`, `AGENTS.md`, `CLAUDE.md`, and specs when present.
2. Ensure remote bare repos or clones exist.
3. Recreate edit and reference worktrees using Git.
4. Sync working-tree deltas without treating `.git` as portable content.

## Agent Planner Integration

`stave portal ...` is the canonical command surface. `stave agent` should be a
conversational planner over that surface, not a second portal wizard and not a
wrapper that immediately summons Codex or Claude to configure things.

The guiding split is:

```text
Stave Agent configures portals. Summoned agents work inside portals.
```

A skill-prompted Codex or Claude summon can still be an escape hatch after a
portal exists, but it should not be the primary setup path. The current Stave
agent execution model is the right base: read tools run during planning,
mutating tools queue validated operations, and confirmation/incant controls
side effects.

### Chat Response Interface

Portal chat should add a typed response state around the existing
`RunResult -> Plan -> Operation -> EquivalentCommand` model.

Suggested response states:

- `needs_input`: the agent needs more information before queuing mutations.
- `plan_ready`: the plan is typed, validated, and ready for confirmation.
- `unsupported`: the request is outside the agent-safe portal surface.
- `error`: planning failed or a required read/preflight failed unexpectedly.

Suggested response fields:

- `status`: one of the states above.
- `message`: short user-facing explanation.
- `questions`: 1-3 structured questions when `status=needs_input`.
- `plan`: the existing typed plan.
- `commands`: deterministic direct `stave portal ...` commands.
- `warnings`: safety/auth/cleanup warnings.
- `toolCalls`, `readResults`, and `results`: existing diagnostic records.

Rules:

- Keep `RunResult` as the `stave agent --json` envelope. If fields are added,
  they must be stable and typed.
- Portal work must appear in `plan.operations[]` and `commands[]`, not only in
  prose notes.
- `ParsePlanText` and text fallback cannot complete portal mutations. A portal
  setup or launch plan requires typed tool output and typed operations.
- Non-JSON output may present conversational prose, but it must also show the
  equivalent direct command for each queued portal mutation.
- `--json` never prompts and never mixes Docker, SSH, tmux, summon, or trace
  chatter into stdout.
- TTY mode may continue a focused question loop inline.
- Non-TTY mode prints `needs_input` questions and stops without executing.
- `--incant` and `agent.autoIncant` execute only `plan_ready` responses.
- `--no-incant` remains plan/chat-only and overrides `agent.autoIncant`.
- High-risk portal operations may require explicit confirmation even when
  `agent.autoIncant` is enabled.

### Prompt Rules

The prompt should preserve the current safety posture while adding a second
terminal path besides `stave_finish`.

- Use read-only tools to resolve ambiguity before asking the user.
- If required slots are missing, call `stave_ask` and stop planning without
  queuing mutations.
- Ask narrowly: one concise question is preferred; use up to three only when
  the answers are independent.
- Do not mix `stave_ask` with queued mutations in the same response.
- Call `stave_finish` only when the plan is executable, fully answered by
  read-only tools, or safely unsupported.
- Preserve the rule that branch moving, reset, rebase, push, PR creation,
  arbitrary shell, and broad deletion are unsupported.

Required portal setup slots:

- `space_id`
- `portal_id` when not `default`
- setup kind: `init` local runtime or `attach` external resource
- driver/target: `container`, `devcontainer`, `ssh`, or `ec2`
- sync mode
- auth mode
- requested summoner when launch is requested
- operation intent: configure, up, sync, summon, down, detach, or inspect

Multi-turn chat state should be explicit in provider input:

- prior answers
- pending question
- resolved slots
- unsupported requests
- read results
- queued operations

### Agent Context Extension

Extend `agent.Context` with manifest-only portal summaries. Live runtime and
auth state stays behind read-only tools such as `stave_portal_status` and
`stave_portal_doctor`.

Suggested context fields:

- `SpaceContext.Portals []PortalContext`
- `PortalContext.ID`
- `PortalContext.Driver`
- `PortalContext.SyncMode`
- `PortalContext.LocalPath`
- `PortalContext.RemoteRoot`
- `PortalContext.ContainerRoot`
- `PortalContext.AuthMode`
- `PortalContext.Providers`
- `PortalContext.Ownership`
- `PortalContext.ManifestExists`
- `PortalRuntimeContext.Engine`
- `PortalRuntimeContext.Image`
- `PortalRuntimeContext.ContainerName`
- `PortalRuntimeContext.ProjectName`
- `PortalRuntimeContext.Service`
- `PortalRuntimeContext.DevcontainerPath`
- `PortalTargetContext.Host`
- `PortalTargetContext.InstanceID`
- `PortalTargetContext.Region`
- `PortalTargetContext.DockerContext`

### Operation Model

Suggested operation constants:

- `OpPortalInit`
- `OpPortalAttach`
- `OpPortalConfigure`
- `OpPortalList`
- `OpPortalStatus`
- `OpPortalDoctor`
- `OpPortalInspect`
- `OpPortalAuthStatus`
- `OpPortalAuthLogin`
- `OpPortalAuthInherit`
- `OpPortalAuthRevoke`
- `OpPortalUp`
- `OpPortalSync`
- `OpPortalSummon`
- `OpPortalDown`
- `OpPortalDetach`
- `OpPortalDestroyPreview`

Portal should extend the current flat `Operation` struct rather than introduce
variant structs. Useful fields include:

- `PortalID`
- `Driver`
- `Preset`
- `Engine`
- `Image`
- `Host`
- `Port`
- `InstanceID`
- `Region`
- `Profile`
- `SSHUser`
- `IdentityPath`
- `RemoteRoot`
- `ContainerRoot`
- `DevcontainerPath`
- `ComposeFiles`
- `Service`
- `SyncMode`
- `Direction`
- `Include`
- `Exclude`
- `Delete`
- `MaxDelete`
- `AllowDirty`
- `AttachMode`
- `Workdir`
- `TTY`
- `Permission`
- `HandoffPrompt`

`EquivalentCommand` must render every supported portal operation
deterministically, shell-safely, and in plan order. It should include
`portal-id` when non-empty, keep repeated flags in stable order, omit `--json`,
and return an unsupported comment for agent-excluded direct commands such as
unbounded `portal exec` or destructive `portal destroy`.

### Tool Contract

Suggested tools:

- `stave_ask`: control, produces `needs_input` and queues no operations
- `stave_portal_list`: read-only
- `stave_portal_status`: read-only
- `stave_portal_doctor`: read-only
- `stave_portal_inspect`: read-only
- `stave_portal_auth_status`: read-only
- `stave_portal_logs`: read-only when bounded by `tail` and no follow mode
- `stave_portal_init`: queued mutation
- `stave_portal_attach`: queued mutation
- `stave_portal_configure`: queued mutation
- `stave_portal_auth_login`: queued mutation/interactive
- `stave_portal_auth_inherit`: queued mutation, excluding `copy-cache`
- `stave_portal_auth_revoke`: queued mutation
- `stave_portal_up`: queued mutation/interactive
- `stave_portal_sync`: queued mutation
- `stave_portal_summon`: queued mutation/interactive
- `stave_portal_down`: queued mutation
- `stave_portal_detach`: queued mutation
- `stave_portal_destroy_preview`: read-only preview only

Direct `portal shell`, `portal exec`, and `portal destroy` remain useful CLI
commands. They are not normal agent v1 mutation tools. Agent requests for
arbitrary exec, interactive shells, or destructive cleanup should use
`stave_explain_unsupported` unless a later design adds narrow, preview-backed
validation.

Schema rules:

- Use strict object schemas with `additionalProperties: false`.
- Prefer enums and discriminators such as `driver` or `target` over
  `oneOf`/`anyOf` unions.
- Do not accept free-form shell commands in agent portal tools.
- Decode portal tool arguments with unknown-field rejection in addition to
  provider-side schemas.
- Keep tool responses concise and high-signal to reduce repeated tool loops.

Agent safety rules:

- Read-only tools can run during planning.
- Mutating portal tools queue validated operations.
- Validation must support same-plan dependencies such as
  `portal init -> portal up -> portal summon`.
- Interactive launch is skipped in JSON/non-interactive executions unless
  explicitly allowed, following current summon behavior.
- Skipped interactive operations are reported as stable JSON results with
  `executed=false` and an explicit message.
- Agent-planned `portal summon` requires auth/status preflight and does not
  transfer credentials.
- Destructive cleanup remains unsupported or preview-only in agent v1.
- Partial execution should be explicit in `results[]`.

Provider loop rules:

- Preserve OpenAI `store:false`, no `previous_response_id`, local tool-call
  history, no default temperature, and disabled parallel tool calls.
- Preserve Anthropic tool ordering: assistant `tool_use` immediately followed
  by user `tool_result`, no default temperature, and disabled parallel tool use.
- Repeated portal read-tool calls should trip the existing loop guard instead
  of spinning.

## Implementation Plan

### Phase 1: Manifest And Service Core

- [x] Add `internal/portal`.
- [x] Add `Manifest`, `Portal`, `Driver`, `Workspace`, `Runtime`, `Auth`,
  `Ownership`, and `Status` types.
- [x] Add `LoadManifest` and `SaveManifest` for `.stave-portal.yaml`.
- [x] Add `Service` with injected `Runner`, `Launcher`, and clock.
- [x] Add validation for space existence, portal IDs, driver names, safe paths, and
  ownership labels.
- [x] Add unit tests for manifest round-trip, defaults, validation, and dry-run
  command planning.

### Phase 2: Direct CLI Surface

- [x] Add `a.portalCommand()` to `internal/cli/root.go`.
- [x] Keep Cobra handlers thin. They parse flags and call `portal.Service`.
- [x] Add guided `init <space-id>` with interactive prompts and manifest preview.
- [x] Add scriptable `init container <space-id>` and `init devcontainer <space-id>`.
- [x] Add `attach ssh <space-id> <host>` and `attach ec2 <space-id> <instance-id>`.
- [x] Add `configure <space-id>` for advanced portal tuning after setup.
- [x] Add `drivers`, `doctor`, `status`, `list`, and `inspect`.
- [x] Add preset handling for `local-codex`, `local-claude`,
  `claude-devcontainer`, `ssh-codex`, and `ssh-claude`.
- [x] Add help tests following existing root help test style.
- [x] Update README command tree once names are stable.

### Phase 3: Runtime Lifecycle

- [x] Add Docker runner implementation.
- [x] Add devcontainer CLI runner.
- [x] Add SSH runner for attach-only commands.
- [x] Add `up`, `down`, `detach`, and `destroy`.
- [x] Make every mutating command support `--dry-run`.
- [x] Add fake runner tests for command generation and ownership filtering.

### Phase 4: Sync

- [x] Add rsync command planning and dry-run parsing.
- [x] Add `mount`, `rsync`, and `reconstruct` modes.
- [x] Add dirty-state checks before destructive pull/delete behavior.
- [x] Add tests for `.git` exclusion, delete safeguards, and references-only mode.

### Phase 5: Shell, Exec, Logs, Summon

- [x] Add `shell` and `exec` with `--tty auto|always|never`.
- [x] Add `logs` for Stave-owned local runtimes and tmux sessions.
- [x] Add `portal summon` as a separate service from local `summon`.
- [x] Reuse the existing prompt shape but add portal-specific wording.
- [x] Add auth preflight and actionable failure messages.
- [x] Add non-interactive behavior tests.

### Phase 6: Agent Planning Tools

- [x] Add `ChatResponse` or equivalent typed response fields to `RunResult`.
- [x] Add `needs_input`, `plan_ready`, `unsupported`, and `error` response
  states.
- [x] Add structured question types for `needs_input`.
- [x] Add `stave_ask` as a control tool that queues no operations.
- [x] Update provider loops so `stave_ask` is a clean terminal state.
- [x] Prevent text fallback from completing portal mutations.
- [x] Add portal operation constants for supported agent-safe actions.
- [x] Add portal-specific fields to the flat `Operation` struct.
- [x] Add deterministic `EquivalentCommand` rendering for supported portal
  operations.
- [x] Return unsupported comments for agent-excluded portal operations.
- [x] Add manifest-only portal summaries to `agent.Context`.
- [x] Keep live status/auth checks behind read-only portal tools.
- [x] Add portal read tool definitions for `list`, `status`, `doctor`,
  `inspect`, `auth status`, and bounded logs.
- [x] Add portal queued mutation tools for `init`, `attach`, `configure`,
  auth login/inherit/revoke, `up`, `sync`, `summon`, `down`, and `detach`.
- [x] Keep arbitrary `portal exec`, foreground shell, and destructive destroy
  outside normal agent v1 tools.
- [x] Add a read-only destroy preview tool if cleanup guidance is needed.
- [x] Reject unknown portal tool arguments during local decode.
- [x] Add validation for same-plan portal dependencies.
- [x] Add validation for unsafe paths, unknown spaces, unknown portals, and
  attach-only ownership boundaries.
- [x] Add executor handling for portal operations.
- [x] Skip interactive portal operations in JSON/non-TTY execution.
- [x] Preserve current confirmation, `--incant`, `--no-incant`, and
  `agent.autoIncant` precedence.
- [x] Update the agent prompt with portal slot-filling and safety rules.
- [x] Keep `agent configure` scoped to planner provider/API-key configuration.
- [x] Avoid new required config keys for portal chat in v1.
- [x] Keep portal target auth out of `agent.providers.*.apiKeyRef`.
- [x] Preserve the current read-now, mutate-later planning model.

### Phase 7: EC2 Attach Extension

- [x] Add `ec2-attach` metadata validation and status.
- [x] Support AWS CLI dry-run/status checks for existing instances.
- [x] Keep start/stop instance behavior out of v1 or gate it behind explicit,
  separate commands with clear billing and data persistence warnings.
- [x] Never terminate instances from portal v1.

## Test Plan

Unit tests:

- [x] Portal manifest load/save and defaulting.
- [x] Driver validation and safe path validation.
- [x] Command generation for Docker, devcontainer, SSH, and EC2 attach.
- [x] Dry-run output for all mutating commands.
- [x] Status normalization from fake runner outputs.
- [x] Auth status parsing for Codex, Claude, and Cursor.
- [x] Sync safeguards: dirty worktrees, `.git` exclusion, delete guardrails.
- [x] Destroy safeguards: label/ownership filtering and no global prune.

CLI tests:

- [x] Help output for every portal command.
- [x] Guided `portal init` previews the manifest and equivalent command before
  writing `.stave-portal.yaml`.
- [x] `portal init container` creates a local-runtime portal manifest.
- [x] `portal init devcontainer` creates a devcontainer portal manifest.
- [x] `portal attach ssh` creates an attach-only SSH portal manifest.
- [x] `portal attach ec2` creates an attach-only EC2 portal manifest.
- [x] `portal configure` updates only the selected portal fields.
- [x] `portal status --json` emits stable keys.
- [x] Non-interactive `portal shell` does not start an interactive process.
- [x] `portal summon --mode print` prints provider commands.

Agent tests:

- [x] Tool definitions include portal read, mutate, and control tools.
- [x] Provider schemas are strict for every portal tool.
- [x] Portal schemas reject unknown fields at local decode time.
- [x] `stave_ask` returns `needs_input`, queues no operations, and stops
  planning.
- [x] `needs_input` emits valid JSON without prompting in `--json` mode.
- [x] TTY `needs_input` enters a focused inline question loop.
- [x] Non-TTY `needs_input` prints questions and stops without executing.
- [x] `RunResult` JSON for portal chat includes stable `status`, `message`,
  `questions`, `plan`, `commands`, `readResults`, `results`, and `executed`
  fields.
- [x] Portal `Operation` values marshal/unmarshal without losing portal fields.
- [x] `Plan.Commands()` renders portal operations in plan order.
- [x] `EquivalentCommand` covers supported portal operations.
- [x] `EquivalentCommand` is shell-safe for spaces, quotes, `$`, backticks,
  paths, hosts, image names, and repeated flags.
- [x] `EquivalentCommand` returns unsupported comments for agent-excluded
  `portal exec`, shell, and destroy requests.
- [x] Read tools append `ReadResults` without queuing operations.
- [x] Mutating tools queue operations only after validation.
- [x] Validation accepts same-plan chains such as
  `portal init -> portal up -> portal summon`.
- [x] Validation rejects portal operations before same-plan init/attach.
- [x] Validation rejects missing spaces, unknown portals, unsafe paths, and
  attach-only ownership violations.
- [x] Validation rejects implicit host secret mounts and silent cache copying.
- [x] Agent-planned `portal summon` asks or fails when target auth is missing.
- [x] Agent-planned auth inherit cannot queue `copy-cache`.
- [x] Auth status read tools return stable, non-secret payloads.
- [x] Portal manifests never persist raw token/key/cache fields.
- [x] Planner provider secrets never appear in tool args, JSON, traces, logs,
  read results, queued operations, or equivalent commands.
- [x] Error paths redact stdout/stderr that may contain secrets.
- [x] OpenAI portal tool loops preserve `store:false`, no
  `previous_response_id`, no default temperature, and disabled parallel tool
  calls.
- [x] Anthropic portal tool loops preserve immediate `tool_result` ordering, no
  default temperature, and disabled parallel tool use.
- [x] Text fallback cannot complete portal mutations.
- [x] Repeated portal read-tool calls trip the repeated-tool guard.
- [x] `--json` produces no trace, prompt, Docker, SSH, tmux, or summon chatter
  on stdout.
- [x] `--incant` and `agent.autoIncant` execute only `plan_ready` responses.
- [x] `--no-incant` overrides `agent.autoIncant` for portal plans.
- [x] Executor skips interactive portal summon/shell in JSON/non-interactive
  mode and reports `executed=false`.
- [x] `agent configure` continues to configure only the planning provider.
- [x] `--provider` and `--model` overrides still work for portal chat.
- [x] Existing space, repo, and local summon agent tests continue to pass.

Config and secret tests:

- [x] Config defaults still include OpenAI/Anthropic provider defaults and
  summon defaults.
- [x] Partial `agent.providers` configs backfill missing model/API key refs.
- [x] Saving/loading config preserves existing `agent` and `summon` behavior.
- [x] Portal chat does not require new config keys to operate.
- [x] Portal auth metadata is never written to `agent.providers.*.apiKeyRef`.
- [x] `ResolveSecret` remains scoped to `env:` and `keychain:` refs for the
  planner provider.

Integration/manual tests:

- [x] Local Docker portal around a throwaway Stave space.
- [x] SSH portal against a disposable local or test host.
- [x] Devcontainer portal with a minimal `.devcontainer/devcontainer.json`.
- [x] Codex/Claude summon print-mode and auth-preflight behavior.

Standard gates:

- [x] All standard tests pass:

```bash
go test ./...
go vet ./...
go test -race -count=1 -timeout 300s ./...
golangci-lint run
go run ./cmd/stave --help
go run ./cmd/stave portal --help
```

- [x] Total Go statement coverage is strictly greater than 90%:

```bash
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out | awk '/^total:/ { sub(/%/, "", $3); exit !($3 > 90) }'
```

## Open Questions

- Should `portal` live at top level forever, or should space-scoped commands
  eventually also be available under `stave space portal <space-id>`?
- Should EC2 start of an existing stopped instance be part of v1, or a separate
  v2 command such as `portal start-instance`?
- Should portal support Cursor in v1 or keep Cursor for the next pass after
  Codex and Claude are solid?
- Should each portal have an isolated `CODEX_HOME`/`CLAUDE_CONFIG_DIR`, or
  should that be opt-in per auth mode?
- Should Stave generate a devcontainer file, or only consume existing
  devcontainer configuration in v1?

## References

- [OpenAI Codex CLI reference](https://developers.openai.com/codex/cli/reference)
- [OpenAI Codex authentication](https://developers.openai.com/codex/auth)
- [OpenAI Codex non-interactive mode](https://developers.openai.com/codex/noninteractive)
- [OpenAI Codex remote connections](https://developers.openai.com/codex/remote-connections)
- [OpenAI function calling](https://developers.openai.com/api/docs/guides/function-calling)
- [OpenAI conversation state](https://developers.openai.com/api/docs/guides/conversation-state)
- [OpenAI Responses create reference](https://developers.openai.com/api/reference/resources/responses/methods/create)
- [Claude Code authentication](https://code.claude.com/docs/en/authentication)
- [Claude Code dev containers](https://code.claude.com/docs/en/devcontainer)
- [Claude Code CLI usage](https://docs.anthropic.com/en/docs/claude-code/cli-usage)
- [Anthropic Messages guide](https://platform.claude.com/docs/en/build-with-claude/working-with-messages)
- [Anthropic tool definition docs](https://platform.claude.com/docs/en/agents-and-tools/tool-use/define-tools)
- [Anthropic tool call handling](https://platform.claude.com/docs/en/agents-and-tools/tool-use/handle-tool-calls)
- [Anthropic strict tool use](https://platform.claude.com/docs/en/agents-and-tools/tool-use/strict-tool-use)
- [Docker bind mounts](https://docs.docker.com/engine/storage/bind-mounts/)
- [Docker contexts](https://docs.docker.com/engine/manage-resources/contexts/)
- [Docker container exec](https://docs.docker.com/reference/cli/docker/container/exec/)
- [Docker container stop](https://docs.docker.com/reference/cli/docker/container/stop/)
- [Docker Compose exec](https://docs.docker.com/reference/cli/docker/compose/exec/)
- [Docker Compose down](https://docs.docker.com/reference/cli/docker/compose/down/)
- [Docker prune unused objects](https://docs.docker.com/engine/manage-resources/pruning/)
- [Dev Container metadata reference](https://containers.dev/implementors/json_reference/)
- [VS Code remote Docker host guidance](https://code.visualstudio.com/remote/advancedcontainers/develop-remote-host)
- [AWS EC2 start-instances](https://docs.aws.amazon.com/cli/latest/reference/ec2/start-instances.html)
- [OpenSSH ssh manual](https://man.openbsd.org/ssh)
- [OpenSSH ssh_config manual](https://man.openbsd.org/ssh_config.5)
- [rsync manual](https://download.samba.org/pub/rsync/rsync.1)
- [Git worktree documentation](https://git-scm.com/docs/git-worktree.html)
- [Git status porcelain documentation](https://www.kernel.org/pub/software/scm/git/docs/git-status.html)
