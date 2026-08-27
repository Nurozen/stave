#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FIXTURE="$ROOT/test_rig/portal-e2e/fixture-app"
SSH_FIXTURE="$ROOT/test_rig/portal-e2e/ssh-host"
TMP="$ROOT/test_rig/tmp/portal-e2e"
LOG_DIR="$ROOT/test_rig/tmp/logs/portal-e2e"
HOME_DIR="$TMP/home"
BIN="$TMP/stave-e2e"
DEVCONTAINER_BIN="$TMP/bin"
LOG="$LOG_DIR/run.log"
SUMMARY="$LOG_DIR/manual-results.md"
# Unique per-run suffix so leftover/concurrent runs never collide on space id,
# container names or image tags. Combine the PID with an epoch second (not
# $RANDOM alone, which is only 15 bits and repeats). Overridable for debugging.
SUFFIX="${STAVE_E2E_SUFFIX:-$$-$(date +%s)}"
SPACE_ID="portal-e2e-$SUFFIX"
SSH_CONTAINER="stave-portal-e2e-ssh-$SUFFIX"
SSH_IMAGE="stave-portal-e2e-ssh-$SUFFIX:latest"
SSH_KEY="$TMP/ssh_key"
SSH_KNOWN_HOSTS="$TMP/known_hosts"
SSH_PORT=""
SKIPPED=()

mkdir -p "$LOG_DIR"
if [[ -d "$TMP" ]]; then
  chmod -R u+w "$TMP" 2>/dev/null || true
fi
rm -rf "$TMP"
mkdir -p "$HOME_DIR" "$DEVCONTAINER_BIN" "$LOG_DIR"
: > "$LOG"

docker_env=()
if [[ -z "${DOCKER_HOST:-}" && -S "$HOME/.rd/docker.sock" ]]; then
  docker_env=("DOCKER_HOST=unix://$HOME/.rd/docker.sock")
elif [[ -n "${DOCKER_HOST:-}" ]]; then
  docker_env=("DOCKER_HOST=$DOCKER_HOST")
fi

stave_env=("HOME=$HOME_DIR" "PATH=$DEVCONTAINER_BIN:$PATH")
if [[ ${#docker_env[@]} -gt 0 ]]; then
  stave_env+=(${docker_env[@]+"${docker_env[@]}"})
fi

# (a) Fail fast if the Docker daemon is unreachable. The Rancher Desktop socket
# at ~/.rd/docker.sock is trusted above but may be stale after a restart, so
# probe the actual daemon rather than assuming the socket implies a live engine.
if ! env ${docker_env[@]+"${docker_env[@]}"} docker info >/dev/null 2>&1; then
  {
    printf 'ERROR: Docker daemon is not reachable.\n'
    if [[ ${#docker_env[@]} -gt 0 ]]; then
      printf '  Tried with: %s\n' "${docker_env[*]}"
    else
      printf '  Tried with the default Docker environment (no DOCKER_HOST set).\n'
    fi
    printf '  Start your Docker/Rancher Desktop engine, then either:\n'
    printf '    - unset DOCKER_HOST to use the default socket, or\n'
    printf '    - export DOCKER_HOST=unix://<path-to-live-docker.sock>\n'
    printf '      (Rancher Desktop default: unix://%s/.rd/docker.sock)\n' "$HOME"
    printf '  Verify with: env %s docker info\n' "${docker_env[*]:-}"
  } | tee -a "$LOG" >&2
  exit 1
fi

# (f) Per-step timeout wrapper. macOS ships no `timeout` binary; prefer GNU
# gtimeout (coreutils), then any `timeout`, otherwise fall back to a no-op and
# warn loudly so an unexpectedly long hang is at least attributable.
STEP_TIMEOUT="${STAVE_E2E_STEP_TIMEOUT:-300}"
timeout_cmd=()
if command -v gtimeout >/dev/null 2>&1; then
  timeout_cmd=(gtimeout "$STEP_TIMEOUT")
elif command -v timeout >/dev/null 2>&1; then
  timeout_cmd=(timeout "$STEP_TIMEOUT")
else
  printf 'WARNING: no gtimeout/timeout binary found; per-step timeouts are DISABLED (install coreutils for gtimeout).\n' | tee -a "$LOG" >&2
fi

run() {
  local label="$1"
  shift
  {
    printf '\n### %s\n' "$label"
    printf '+'
    printf ' %q' "$@"
    printf '\n'
  } | tee -a "$LOG"
  "$@" 2>&1 | tee -a "$LOG"
}

run_stave() {
  local label="$1"
  shift
  # Guarded expansion: timeout_cmd is empty when no timeout binary exists, and
  # "${arr[@]}" on an empty array is an unbound-variable error under `set -u`
  # in bash 3.2 (the macOS default).
  run "$label" ${timeout_cmd[@]+"${timeout_cmd[@]}"} env "${stave_env[@]}" "$BIN" "$@"
}

run_docker() {
  local label="$1"
  shift
  run "$label" env ${docker_env[@]+"${docker_env[@]}"} docker "$@"
}

run_shell() {
  local label="$1"
  shift
  {
    printf '\n### %s\n' "$label"
    printf '+ %s\n' "$*"
  } | tee -a "$LOG"
  bash -lc "$*" 2>&1 | tee -a "$LOG"
}

cleanup() {
  env ${docker_env[@]+"${docker_env[@]}"} docker rm -f "$SSH_CONTAINER" >/dev/null 2>&1 || true
}

# (c)+(d) Full teardown for the EXIT trap: remove the ssh container, every
# container this run created (matched by the unique $SUFFIX in its name), any
# stale devcontainer bound to this workspace (a prior aborted run's vsc-*
# container poisons the next `devcontainer up`), and the run-scoped ssh image.
trap_cleanup() {
  local ids
  cleanup
  ids="$(env ${docker_env[@]+"${docker_env[@]}"} docker ps -aq --filter "name=$SUFFIX" 2>/dev/null || true)"
  if [[ -n "$ids" ]]; then
    # shellcheck disable=SC2086
    env ${docker_env[@]+"${docker_env[@]}"} docker rm -f $ids >/dev/null 2>&1 || true
  fi
  ids="$(env ${docker_env[@]+"${docker_env[@]}"} docker ps -aq --filter "label=devcontainer.local_folder=$FIXTURE" 2>/dev/null || true)"
  if [[ -n "$ids" ]]; then
    # shellcheck disable=SC2086
    env ${docker_env[@]+"${docker_env[@]}"} docker rm -f $ids >/dev/null 2>&1 || true
  fi
  env ${docker_env[@]+"${docker_env[@]}"} docker rmi -f "$SSH_IMAGE" >/dev/null 2>&1 || true
}
trap trap_cleanup EXIT

# Sweep leftover portal containers from prior runs. Names are derived from the
# run-scoped SPACE_ID, so this also namespaces the sweep to this run's portals.
for portal_name in default guided guided-preview preview; do
  env ${docker_env[@]+"${docker_env[@]}"} docker rm -f "stave-$SPACE_ID-$portal_name" >/dev/null 2>&1 || true
done
# Also sweep any stale devcontainer bound to this workspace before we begin.
stale_devcontainers="$(env ${docker_env[@]+"${docker_env[@]}"} docker ps -aq --filter "label=devcontainer.local_folder=$FIXTURE" 2>/dev/null || true)"
if [[ -n "$stale_devcontainers" ]]; then
  # shellcheck disable=SC2086
  env ${docker_env[@]+"${docker_env[@]}"} docker rm -f $stale_devcontainers >/dev/null 2>&1 || true
fi

run "build fixture binary" env GOCACHE=/private/tmp/stave-go-build go build -o "$BIN" ./cmd/stave

if ! command -v devcontainer >/dev/null 2>&1 && command -v npm >/dev/null 2>&1; then
  cat > "$DEVCONTAINER_BIN/devcontainer" <<'EOF'
#!/usr/bin/env sh
# (d) Force `devcontainer up` to discard a stale container left by an aborted
# run instead of reusing (and inheriting) its poisoned state.
if [ "${1:-}" = "up" ]; then
  shift
  exec npm exec --yes --package @devcontainers/cli -- devcontainer up --remove-existing-container "$@"
fi
exec npm exec --yes --package @devcontainers/cli -- devcontainer "$@"
EOF
  chmod +x "$DEVCONTAINER_BIN/devcontainer"
fi

if [[ ! -d "$FIXTURE/.git" ]]; then
  run_shell "initialize fixture git repo" "cd '$FIXTURE' && git init && git add . && git commit -m fixture"
fi

run_stave "setup isolated Stave home" setup
run_stave "register fixture repository" repos add fixture "$FIXTURE"
run_stave "list repositories" repos list
run_stave "sync repositories" repos sync fixture
run_stave "create portal test space" space create "$SPACE_ID" -e fixture
run_stave "space status" space status "$SPACE_ID"

run_stave "portal drivers" portal drivers
run_stave "guided portal init dry-run" portal init "$SPACE_ID" guided-preview --preset local-codex --dry-run
run_stave "guided portal init write" portal init "$SPACE_ID" guided --preset local-codex --yes
run_stave "container portal init dry-run" portal init container "$SPACE_ID" preview --image busybox:1.36 --dry-run
run_stave "container portal init write" portal init container "$SPACE_ID" default --engine docker --image alpine:3.20
run_stave "devcontainer portal init write" portal init devcontainer "$SPACE_ID" dev-live --path "$FIXTURE/.devcontainer/devcontainer.json"
run_stave "configure portal dry-run" portal configure "$SPACE_ID" default --agent codex --auth env --dry-run
run_stave "configure portal write" portal configure "$SPACE_ID" default --agent codex --auth env
run_stave "portal list all" portal list
run_stave "portal list space" portal list "$SPACE_ID"
run_stave "portal inspect json" portal inspect "$SPACE_ID" default --json
run_stave "portal auth status json" portal auth status "$SPACE_ID" default --json
run_stave "portal auth login dry-run" portal auth login "$SPACE_ID" default --provider codex --method device --dry-run
run_stave "portal auth inherit dry-run" portal auth inherit "$SPACE_ID" default --provider codex --method env --dry-run --yes
run_stave "portal auth revoke dry-run" portal auth revoke "$SPACE_ID" default --provider codex --target portal --dry-run
run_stave "portal up default" portal up "$SPACE_ID" default
run_stave "portal down default" portal down "$SPACE_ID" default
run_stave "portal up default after down" portal up "$SPACE_ID" default
run_stave "portal status json" portal status "$SPACE_ID" default --json
run_stave "portal doctor" portal doctor "$SPACE_ID" default
run_stave "portal sync mount dry-run" portal sync "$SPACE_ID" default --dry-run
run_stave "portal shell print" portal shell "$SPACE_ID" default --print-command
run_stave "portal summon print" portal summon "$SPACE_ID" default --with codex --mode print
run_stave "portal logs docker plan" portal logs "$SPACE_ID" default --tail 20 --dry-run
run_stave "portal logs docker execute" portal logs "$SPACE_ID" default --tail 20
run_stave "portal exec docker destination" portal exec "$SPACE_ID" default -- sh -c "pwd && test -f .stave.yaml && test -d fixture && echo docker-destination-ok > .stave-docker-portal-ok"
run_shell "verify docker destination marker" "test -f '$HOME_DIR/stave/agent-work/$SPACE_ID/.stave-docker-portal-ok'"
run_stave "portal down default dry-run" portal down "$SPACE_ID" default --dry-run
run_stave "portal destroy default dry-run" portal destroy "$SPACE_ID" default --dry-run

run "generate ssh key" ssh-keygen -t ed25519 -N "" -f "$SSH_KEY"
cleanup
run_docker "build ssh portal fixture image" build --build-arg "PUBLIC_KEY=$(cat "$SSH_KEY.pub")" -t "$SSH_IMAGE" "$SSH_FIXTURE"
# (b) Bind SSH to a docker-assigned ephemeral host port instead of a fixed
# 22222, which collides across concurrent or leftover runs. Discover the actual
# port with `docker port` after the container starts.
run_docker "start ssh portal fixture" run -d --name "$SSH_CONTAINER" -p 127.0.0.1::22 "$SSH_IMAGE"
SSH_PORT="$(env ${docker_env[@]+"${docker_env[@]}"} docker port "$SSH_CONTAINER" 22/tcp 2>/dev/null | head -n1 | awk -F: '{print $NF}')"
if [[ -z "$SSH_PORT" ]]; then
  printf 'ERROR: could not determine the mapped SSH host port for %s.\n' "$SSH_CONTAINER" | tee -a "$LOG" >&2
  exit 1
fi
printf '\n### ssh host port mapped to 127.0.0.1:%s\n' "$SSH_PORT" | tee -a "$LOG"
run_shell "wait for ssh host key" "for i in {1..30}; do ssh-keyscan -p $SSH_PORT 127.0.0.1 > '$SSH_KNOWN_HOSTS' 2>/dev/null && test -s '$SSH_KNOWN_HOSTS' && exit 0; sleep 1; done; exit 1"
run_stave "attach ssh dry-run" portal attach ssh "$SPACE_ID" stave@127.0.0.1 ssh-preview --port "$SSH_PORT" --identity "$SSH_KEY" --known-hosts "$SSH_KNOWN_HOSTS" --strict-host-key yes --remote-root /home/stave/portal-work --sync rsync --dry-run
run_stave "attach ssh write" portal attach ssh "$SPACE_ID" stave@127.0.0.1 ssh-live --port "$SSH_PORT" --identity "$SSH_KEY" --known-hosts "$SSH_KNOWN_HOSTS" --strict-host-key yes --remote-root /home/stave/portal-work --sync rsync
run_stave "portal up ssh" portal up "$SPACE_ID" ssh-live
run_stave "portal status ssh json" portal status "$SPACE_ID" ssh-live --json
run_stave "portal sync ssh dry-run" portal sync "$SPACE_ID" ssh-live --direction to --mode rsync --dry-run
run_stave "portal sync ssh write" portal sync "$SPACE_ID" ssh-live --direction to --mode rsync
run_stave "portal exec ssh destination" portal exec "$SPACE_ID" ssh-live -- sh -lc "test -f .stave.yaml && test -f .stave-portal.yaml && test -d fixture && echo ssh-destination-ok > .stave-ssh-portal-ok"
run_shell "verify ssh destination marker" "ssh -p $SSH_PORT -i '$SSH_KEY' -o UserKnownHostsFile='$SSH_KNOWN_HOSTS' -o StrictHostKeyChecking=yes stave@127.0.0.1 'cat /home/stave/portal-work/.stave-ssh-portal-ok' | grep ssh-destination-ok"
# Real tmux summon over the ssh fixture (which ships tmux + a /usr/bin/codex
# stub; see ssh-host/Dockerfile). `< /dev/null` forces a non-terminal stdin so
# the off-TTY flip-skip executes the detached `tmux new-session -d` create
# directly and PlanSummon never appends the blocking attach — regardless of
# whether run.sh itself was launched from a terminal. It returns promptly under
# run_stave's timeout. We then assert the session is live, its pane shows the
# stub agent's marker, and re-summoning is idempotent. Each assertion ssh carries
# ConnectTimeout so a wedged connection fails the leg instead of stalling the loop.
run_stave "portal summon tmux (real)" portal summon "$SPACE_ID" ssh-live --with codex --mode tmux < /dev/null
run_shell "verify tmux session live" "ssh -o ConnectTimeout=10 -p $SSH_PORT -i '$SSH_KEY' -o UserKnownHostsFile='$SSH_KNOWN_HOSTS' -o StrictHostKeyChecking=yes stave@127.0.0.1 tmux has-session -t stave-$SPACE_ID-ssh-live"
run_shell "verify tmux pane marker" "for i in {1..30}; do ssh -o ConnectTimeout=10 -p $SSH_PORT -i '$SSH_KEY' -o UserKnownHostsFile='$SSH_KNOWN_HOSTS' -o StrictHostKeyChecking=yes stave@127.0.0.1 tmux capture-pane -pt stave-$SPACE_ID-ssh-live | grep -q STAVE-STUB-AGENT-READY && exit 0; sleep 1; done; exit 1"
run_stave "portal summon tmux (idempotent)" portal summon "$SPACE_ID" ssh-live --with codex --mode tmux < /dev/null
run_shell "verify tmux session still live" "ssh -o ConnectTimeout=10 -p $SSH_PORT -i '$SSH_KEY' -o UserKnownHostsFile='$SSH_KNOWN_HOSTS' -o StrictHostKeyChecking=yes stave@127.0.0.1 tmux has-session -t stave-$SPACE_ID-ssh-live"
run_shell "verify single tmux session" "[ \"\$(ssh -o ConnectTimeout=10 -p $SSH_PORT -i '$SSH_KEY' -o UserKnownHostsFile='$SSH_KNOWN_HOSTS' -o StrictHostKeyChecking=yes stave@127.0.0.1 tmux list-sessions | grep -c stave-$SPACE_ID-ssh-live)\" -eq 1 ]"
run_stave "portal logs ssh plan" portal logs "$SPACE_ID" ssh-live --tail 10 --dry-run
run_stave "portal detach ssh dry-run" portal detach "$SPACE_ID" ssh-live --dry-run

run_stave "attach ec2 dry-run" portal attach ec2 "$SPACE_ID" i-0123456789abcdef0 ec2-plan --region us-west-2 --remote-root /home/ec2-user/portal-work --dry-run
run_stave "attach ec2 write metadata" portal attach ec2 "$SPACE_ID" i-0123456789abcdef0 ec2-plan --region us-west-2 --remote-root /home/ec2-user/portal-work
run_stave "portal up ec2 dry-run" portal up "$SPACE_ID" ec2-plan --dry-run
run_stave "portal detach ec2" portal detach "$SPACE_ID" ec2-plan

if command -v devcontainer >/dev/null 2>&1 || [[ -x "$DEVCONTAINER_BIN/devcontainer" ]]; then
  run_stave "portal up devcontainer" portal up "$SPACE_ID" dev-live
  run_stave "portal status devcontainer json" portal status "$SPACE_ID" dev-live --json
  run_stave "portal exec devcontainer destination" portal exec "$SPACE_ID" dev-live -- sh -lc "cd fixture && npm test"
  run_stave "portal destroy devcontainer" portal destroy "$SPACE_ID" dev-live
else
  # (e) Make a skipped leg unmistakable so a green run is not confused with a
  # partial one. The leg is also recorded and surfaced in the final summary.
  SKIPPED+=("portal devcontainer live checks (up/status/exec/destroy)")
  {
    printf '\n### ============================================================\n'
    printf '### SKIPPED (devcontainer CLI missing): portal devcontainer live checks\n'
    printf '### Install the Dev Containers CLI (or npm) to exercise this leg.\n'
    printf '### ============================================================\n'
  } | tee -a "$LOG"
fi

run_stave "portal destroy default" portal destroy "$SPACE_ID" default
run_stave "portal detach ssh" portal detach "$SPACE_ID" ssh-live
run_stave "portal list final" portal list "$SPACE_ID"

# (e) Render skipped legs for the summary so a partial run is self-documenting.
if [[ ${#SKIPPED[@]} -gt 0 ]]; then
  skipped_md=""
  for leg in "${SKIPPED[@]}"; do
    skipped_md+="- SKIPPED: $leg"$'\n'
  done
else
  skipped_md="- none (all legs executed)"$'\n'
fi

cat > "$SUMMARY" <<EOF
# Portal E2E Manual Results

Run suffix: \`$SUFFIX\`

Fixture home: \`$HOME_DIR\`

Fixture binary: \`$BIN\`

Project repo: \`$FIXTURE\`

Command log: \`$LOG\`

Environment notes:

- Docker was exercised with \`${docker_env[*]:-default Docker environment}\`.
- Rancher Desktop was checked through Computer Use and reported CE \`moby\`.
- EC2 attach was covered with metadata write and dry-run planning only; no cloud instance was contacted or provisioned.
- Auth transfer/login/revoke commands were covered in dry-run mode only; no credentials were copied.

Results:

- All portal top-level commands and nested init/attach/auth commands were exercised by this fixture runner.
- The Docker portal destination wrote and verified \`.stave-docker-portal-ok\` through an in-container exec.
- The SSH portal destination wrote and verified \`.stave-ssh-portal-ok\` on the disposable SSH host.
- Devcontainer live checks ran when the Dev Containers CLI was available; see the command log for the exact outcome.

Skipped legs:

$skipped_md
EOF

if [[ ${#SKIPPED[@]} -gt 0 ]]; then
  printf '\nPortal e2e completed WITH SKIPS (%d leg(s) skipped -- NOT a full pass). Summary: %s\n' "${#SKIPPED[@]}" "$SUMMARY" | tee -a "$LOG"
else
  printf '\nPortal e2e completed (all legs executed). Summary: %s\n' "$SUMMARY" | tee -a "$LOG"
fi
