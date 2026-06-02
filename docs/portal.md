# Stave Portal

`stave portal` attaches an execution environment to an existing Stave space.
The local space remains the source of truth. The portal is the place where
commands, shells, and coding agents can run.

Use a portal when you want to:

- run commands in a container, devcontainer, SSH host, or EC2-backed host
- launch Codex, Claude Code, or Cursor Agent from inside that environment
- keep portal metadata separate from the space manifest
- inspect or destroy Stave-owned runtime resources without disturbing the space

Portal metadata is written to `.stave-portal.yaml` in the space root. If you do
not pass a `portal-id`, Stave uses `default`.

## Quick Start: Local Container

Start with an existing space:

```sh
stave space create example -e my-repo
```

Create a Docker-backed portal:

```sh
stave portal init container example --image alpine:3.20
```

Start or validate the portal runtime:

```sh
stave portal up example
```

Run a command inside the portal:

```sh
stave portal exec example -- sh -lc 'pwd && ls'
```

Check status:

```sh
stave portal status example
stave portal status example --json
```

Print the Codex launch command that would run inside the portal:

```sh
stave portal summon example --with codex --mode print
```

Stop or remove the portal when you are done:

```sh
stave portal down example
stave portal destroy example
```

`down` preserves portal data. `destroy` removes recorded Stave-owned runtime
resources.

## Choosing A Portal

Use `init` for Stave-owned local runtimes:

```sh
stave portal init container <space-id> [portal-id]
stave portal init devcontainer <space-id> [portal-id]
```

Use `attach` for existing external resources:

```sh
stave portal attach ssh <space-id> <host> [portal-id]
stave portal attach ec2 <space-id> <instance-id> [portal-id] --region <region>
```

Common portal kinds:

| Kind | Command | Best For |
|------|---------|----------|
| Docker container | `portal init container` | Local isolated execution |
| Devcontainer | `portal init devcontainer` | Projects that already define `.devcontainer/devcontainer.json` |
| SSH host | `portal attach ssh` | Existing remote machines or local disposable SSH fixtures |
| EC2 instance | `portal attach ec2` | Existing EC2 instances; portal does not provision or terminate instances |

See supported drivers:

```sh
stave portal drivers
```

## Guided Setup

The guided command asks only for the portal kind and previews the equivalent
direct command before writing the manifest:

```sh
stave portal init <space-id>
```

Use presets when you already know the intended agent:

```sh
stave portal init <space-id> --preset local-codex
stave portal init <space-id> --preset local-claude
stave portal init <space-id> --preset claude-devcontainer
stave portal attach ssh <space-id> user@example.com --preset ssh-codex
```

Preview without writing:

```sh
stave portal init <space-id> --preset local-codex --dry-run
```

Accept guided defaults without an interactive confirmation:

```sh
stave portal init <space-id> --preset local-codex --yes
```

## Devcontainer Portal

Create portal metadata from a devcontainer file:

```sh
stave portal init devcontainer example \
  --path /path/to/repo/.devcontainer/devcontainer.json
```

Optional flags:

```sh
stave portal init devcontainer example dev \
  --path /path/to/repo/.devcontainer/devcontainer.json \
  --service app \
  --compose-file docker-compose.yml \
  --container-root /workspace/example
```

Start and use it:

```sh
stave portal up example dev
stave portal exec example dev -- sh -lc 'npm test'
```

## SSH Portal

Attach an existing SSH host:

```sh
stave portal attach ssh example user@example.com remote \
  --remote-root /home/user/stave/example \
  --identity ~/.ssh/id_ed25519 \
  --known-hosts ~/.ssh/known_hosts \
  --strict-host-key yes \
  --sync rsync
```

Start or validate the attachment:

```sh
stave portal up example remote
```

Sync the local space to the remote target:

```sh
stave portal sync example remote --direction to --mode rsync
```

Run a command on the remote portal:

```sh
stave portal exec example remote -- sh -lc 'pwd && test -f .stave.yaml'
```

Detach when finished:

```sh
stave portal detach example remote
```

`detach` removes local portal metadata for attach-only targets. It does not
delete the remote machine or arbitrary remote files.

## EC2 Portal

Attach an existing EC2 instance:

```sh
stave portal attach ec2 example i-0123456789abcdef0 ec2 \
  --region us-west-2 \
  --ssh-user ec2-user \
  --identity ~/.ssh/id_ed25519 \
  --remote-root /home/ec2-user/stave/example \
  --sync rsync
```

Portal v1 does not create, start, stop, or terminate EC2 instances. It records
how to reach an existing instance and plans commands against that attachment.

Use dry-run when you want to check the plan without contacting or mutating the
target:

```sh
stave portal attach ec2 example i-0123456789abcdef0 ec2 \
  --region us-west-2 \
  --remote-root /home/ec2-user/stave/example \
  --dry-run
```

## Auth And Summon

Local login credentials are never copied silently. `portal summon` can be
seamless only after the target environment is authenticated.

Check target auth:

```sh
stave portal auth status example
stave portal auth status example --provider codex --json
```

Run provider-native login inside the portal:

```sh
stave portal auth login example --provider codex
stave portal auth login example --provider claude
stave portal auth login example --provider cursor
```

Preview login without running it:

```sh
stave portal auth login example --provider codex --method device --dry-run
```

Explicitly inherit auth when you really want that behavior:

```sh
stave portal auth inherit example \
  --provider codex \
  --method env \
  --yes
```

Supported inherit methods are `env`, `volume`, `ssh-forward`, and `copy-cache`.
Use `copy-cache` only when you intentionally want local auth material copied
into the portal target.

Launch or print an agent command:

```sh
stave portal summon example --with codex
stave portal summon example --with claude --mode tmux
stave portal summon example --with cursor --mode print
```

Summon modes:

| Mode | Behavior |
|------|----------|
| `foreground` | Launch in the current terminal |
| `tmux` | Launch in a Stave-owned tmux session |
| `headless` | Run without an interactive TTY when supported |
| `print` | Print the command instead of launching |

Permissions:

```sh
stave portal summon example --with codex --permission read-only
stave portal summon example --with codex --permission workspace-write
```

## Inspecting Portals

List portals:

```sh
stave portal list
stave portal list example
stave portal list example --json
```

Inspect manifest and ownership details:

```sh
stave portal inspect example
stave portal inspect example --json
```

Run diagnostics:

```sh
stave portal doctor example
stave portal doctor example --json
```

Show logs or log plans:

```sh
stave portal logs example --tail 100
stave portal logs example --agent codex --tail 50
```

## Syncing

Portal sync reconciles files between the local space and the portal target.

Push local files to the portal:

```sh
stave portal sync example --direction to
```

Pull portal files back:

```sh
stave portal sync example --direction from
```

Preview destructive sync behavior:

```sh
stave portal sync example \
  --direction from \
  --delete \
  --max-delete 10 \
  --dry-run
```

Useful sync flags:

| Flag | Meaning |
|------|---------|
| `--direction to|from|both` | Choose sync direction |
| `--mode auto|mount|rsync|reconstruct` | Choose sync mechanism |
| `--references-only` | Sync only references |
| `--include <pattern>` | Include matching paths; repeatable |
| `--exclude <pattern>` | Exclude matching paths; repeatable |
| `--delete` | Delete target files missing from source |
| `--allow-dirty` | Allow destructive pull with dirty local edits |
| `--yes` | Confirm sync behavior |

## Shell And Exec

Open an interactive shell:

```sh
stave portal shell example
```

Print the shell command instead of launching:

```sh
stave portal shell example --print-command
```

Run a command:

```sh
stave portal exec example -- sh -lc 'go test ./...'
```

Pass a portal id before `--`:

```sh
stave portal exec example remote -- sh -lc 'hostname && pwd'
```

Optional execution flags:

```sh
stave portal exec example \
  --cwd /workspace/example \
  --user vscode \
  --tty never \
  -- sh -lc 'id && pwd'
```

The command working directory defaults to the portal's space root.

## Configure After Setup

Use `configure` for advanced tuning after a portal exists:

```sh
stave portal configure example \
  --sync rsync \
  --agent codex \
  --auth env
```

Other useful fields:

```sh
stave portal configure example remote \
  --remote-root /home/user/stave/example

stave portal configure example dev \
  --container-root /workspace/example
```

Preview without writing:

```sh
stave portal configure example --agent claude --dry-run
```

## Cleanup

Stop a Stave-owned runtime while preserving data:

```sh
stave portal down example
```

Detach an attach-only portal:

```sh
stave portal detach example remote
```

Destroy Stave-owned runtime resources:

```sh
stave portal destroy example
```

Preview destroy first:

```sh
stave portal destroy example --dry-run
```

Delete recorded Stave-owned volumes only when you mean it:

```sh
stave portal destroy example --delete-volumes
```

For SSH and EC2 portals, use `detach`. Portal v1 does not delete arbitrary
remote hosts, terminate EC2 instances, or remove files outside the exact
recorded portal root.

## Agent Planning

`stave portal ...` is the canonical command surface. `stave agent` can plan
portal work conversationally when agent support is configured, but mutations
are queued as equivalent `stave portal ...` commands.

Examples:

```sh
stave agent "set up a local codex portal for space example"
stave agent "check portal status for example and print the codex summon command"
```

Agent-planned portal setup still follows the same safety rules:

- missing required details are asked for before mutation
- target auth is checked before summon
- destructive operations require explicit confirmation
- arbitrary `portal exec`, foreground shell, and destructive destroy are direct
  CLI operations rather than free-form agent tools

## Safety Rules

Keep these invariants in mind:

- The local Stave space remains the source of truth.
- `.stave-portal.yaml` stores portal attachment metadata, not secrets.
- Local auth is never copied into a portal unless you explicitly request
  `auth inherit`.
- `portal status`, `portal doctor`, and `portal inspect` report live state
  instead of trusting stale manifest fields.
- `portal destroy` affects Stave-owned runtime resources, not arbitrary user
  paths.
- EC2 portals attach to existing instances; Stave does not provision or
  terminate EC2 instances in portal v1.

## Manual Fixture

The local end-to-end fixture exercises the portal command surface against
container, devcontainer, SSH, and EC2-planning paths:

```sh
test_rig/portal-e2e/run.sh
```

The fixture uses an isolated `HOME` under `test_rig/tmp/portal-e2e/home` and
writes logs under `test_rig/tmp/logs/portal-e2e/`.

For implementation details and the feature backlog, see
[`docs/portal-plan.md`](portal-plan.md).
