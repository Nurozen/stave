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
SPACE_ID="portal-e2e"
SSH_CONTAINER="stave-portal-e2e-ssh"
SSH_IMAGE="stave-portal-e2e-ssh:latest"
SSH_KEY="$TMP/ssh_key"
SSH_KNOWN_HOSTS="$TMP/known_hosts"

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
  stave_env+=("${docker_env[@]}")
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
  run "$label" env "${stave_env[@]}" "$BIN" "$@"
}

run_docker() {
  local label="$1"
  shift
  run "$label" env "${docker_env[@]}" docker "$@"
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
  env "${docker_env[@]}" docker rm -f "$SSH_CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

for portal_container in \
  stave-portal-e2e-default \
  stave-portal-e2e-guided \
  stave-portal-e2e-guided-preview \
  stave-portal-e2e-preview; do
  env "${docker_env[@]}" docker rm -f "$portal_container" >/dev/null 2>&1 || true
done

run "build fixture binary" env GOCACHE=/private/tmp/stave-go-build go build -o "$BIN" ./cmd/stave

if ! command -v devcontainer >/dev/null 2>&1 && command -v npm >/dev/null 2>&1; then
  cat > "$DEVCONTAINER_BIN/devcontainer" <<'EOF'
#!/usr/bin/env sh
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
run_stave "portal logs docker plan" portal logs "$SPACE_ID" default --tail 20
run_stave "portal exec docker destination" portal exec "$SPACE_ID" default -- sh -c "pwd && test -f .stave.yaml && test -d fixture && echo docker-destination-ok > .stave-docker-portal-ok"
run_shell "verify docker destination marker" "test -f '$HOME_DIR/stave/agent-work/$SPACE_ID/.stave-docker-portal-ok'"
run_stave "portal down default dry-run" portal down "$SPACE_ID" default --dry-run
run_stave "portal destroy default dry-run" portal destroy "$SPACE_ID" default --dry-run

run "generate ssh key" ssh-keygen -t ed25519 -N "" -f "$SSH_KEY"
cleanup
run_docker "build ssh portal fixture image" build --build-arg "PUBLIC_KEY=$(cat "$SSH_KEY.pub")" -t "$SSH_IMAGE" "$SSH_FIXTURE"
run_docker "start ssh portal fixture" run -d --name "$SSH_CONTAINER" -p 127.0.0.1:22222:22 "$SSH_IMAGE"
run_shell "wait for ssh host key" "for i in {1..30}; do ssh-keyscan -p 22222 127.0.0.1 > '$SSH_KNOWN_HOSTS' 2>/dev/null && test -s '$SSH_KNOWN_HOSTS' && exit 0; sleep 1; done; exit 1"
run_stave "attach ssh dry-run" portal attach ssh "$SPACE_ID" stave@127.0.0.1 ssh-preview --port 22222 --identity "$SSH_KEY" --known-hosts "$SSH_KNOWN_HOSTS" --strict-host-key yes --remote-root /home/stave/portal-work --sync rsync --dry-run
run_stave "attach ssh write" portal attach ssh "$SPACE_ID" stave@127.0.0.1 ssh-live --port 22222 --identity "$SSH_KEY" --known-hosts "$SSH_KNOWN_HOSTS" --strict-host-key yes --remote-root /home/stave/portal-work --sync rsync
run_stave "portal up ssh" portal up "$SPACE_ID" ssh-live
run_stave "portal status ssh json" portal status "$SPACE_ID" ssh-live --json
run_stave "portal sync ssh dry-run" portal sync "$SPACE_ID" ssh-live --direction to --mode rsync --dry-run
run_stave "portal sync ssh write" portal sync "$SPACE_ID" ssh-live --direction to --mode rsync
run_stave "portal exec ssh destination" portal exec "$SPACE_ID" ssh-live -- sh -lc "test -f .stave.yaml && test -f .stave-portal.yaml && test -d fixture && echo ssh-destination-ok > .stave-ssh-portal-ok"
run_shell "verify ssh destination marker" "ssh -p 22222 -i '$SSH_KEY' -o UserKnownHostsFile='$SSH_KNOWN_HOSTS' -o StrictHostKeyChecking=yes stave@127.0.0.1 'cat /home/stave/portal-work/.stave-ssh-portal-ok' | grep ssh-destination-ok"
run_stave "portal logs ssh plan" portal logs "$SPACE_ID" ssh-live --tail 10
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
  printf '\n### portal devcontainer live checks skipped: devcontainer CLI unavailable\n' | tee -a "$LOG"
fi

run_stave "portal destroy default" portal destroy "$SPACE_ID" default
run_stave "portal detach ssh" portal detach "$SPACE_ID" ssh-live
run_stave "portal list final" portal list "$SPACE_ID"

cat > "$SUMMARY" <<EOF
# Portal E2E Manual Results

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
EOF

printf '\nPortal e2e completed. Summary: %s\n' "$SUMMARY" | tee -a "$LOG"
