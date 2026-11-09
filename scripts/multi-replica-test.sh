#!/usr/bin/env bash
# Multi-replica gateway integration test (plan 213 §5).
#
# Two gateway replicas, one state database, one shared provider stub: the
# conversation starts on replica A and the browser is connected to replica B, so
# every assertion crosses the replica boundary — the socket is relayed to A, and
# A is the replica that resumes or stops the run.
#
#   ./scripts/multi-replica-test.sh postgres
#   ./scripts/multi-replica-test.sh mysql
#
# The replicas are local processes rather than containers: a host has one
# listener per address, so each replica gets its own home (and therefore its own
# gateway.http_addr) while sharing the database, which is what the replica
# machinery keys on. deploy/compose/multi-replica.yaml is the container form of
# the same deployment, for a host that can run two containers on one port.
#
# Requirements: CGO + FTS5 (the test binary is built here), python3, and the
# database container scripts/test-db.sh starts (fb-postgres / fb-mysql).
set -euo pipefail

root=$(git rev-parse --show-toplevel)
cd "$root"

engine=${1:-}
case "$engine" in
  postgres) url='postgres://postgres:fb@127.0.0.1:15432/postgres?sslmode=disable' ;;
  mysql)    url='mysql://root:fb@127.0.0.1:13306/fbt' ;;
  *) echo "usage: $0 postgres|mysql" >&2; exit 2 ;;
esac

# PostgreSQL's URL names the server's own postgres database, which always
# exists; MySQL's names `fbt`, which a fresh container does not have — the Go
# dialect tests create a scratch database per test and never need it, while the
# gateway opens its configured database directly. Create it once, through the
# container the URL points at.
if [ "$engine" = mysql ]; then
  docker exec fb-mysql mysql -uroot -pfb -e 'CREATE DATABASE IF NOT EXISTS fbt' 2>/dev/null ||
    { echo "cannot create the fbt database: is the fb-mysql container running? (scripts/test-db.sh up)" >&2; exit 1; }
fi

export PKG_CONFIG_PATH="$root/third_party/pdfium/darwin_arm64"
export CGO_CFLAGS="-I$root/third_party/pdfium/darwin_arm64/include"
export CGO_LDFLAGS="-L$root/third_party/pdfium/darwin_arm64/lib -lpdfium -Wl,-rpath,$root/third_party/pdfium/darwin_arm64/lib"
export CGO_ENABLED=1

port_a=${PORT_A:-16061}
port_b=${PORT_B:-16062}
llm_port=${LLM_PORT:-18741}
token="$(openssl rand -hex 24)"
work=$(mktemp -d)
home_a="$work/a"
home_b="$work/b"
mkdir -p "$home_a" "$home_b"
pids=()
cleanup() {
  for pid in "${pids[@]:-}"; do
    kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

echo "== building the gateway for $engine"
go build -tags fts5 -o "$work/forebrain" ./cmd/forebrain

write_home() {
  local home=$1 port=$2
  # Secrets never live in forebrain.yaml: the gateway token and the provider key
  # are ${ENV_NAME} references resolved from the environment the replica is
  # started with — the home has no .env any more (plan 268).
  cat > "$home/forebrain.yaml" <<YAML
gateway:
  http_addr: 127.0.0.1:$port
  auth:
    token: \${FOREBRAIN_GATEWAY_TOKEN}
database:
  url: "$url"
approval_policy: on-request
approvals_reviewer: user
features:
  memories: false
agents:
  definitions:
    main:
      primary: true
      llm_providers:
        - provider: deepseek
          model: deepseek-v4-flash
          api_key: \${FAKE_LLM_KEY}
          base_url: http://127.0.0.1:$llm_port
YAML
  chmod 600 "$home/forebrain.yaml"
}

write_home "$home_a" "$port_a"
write_home "$home_b" "$port_b"

echo "== provider stub on 127.0.0.1:$llm_port (a turn that parks on an approval)"
# exit_plan_mode is the one tool a gateway turn always gates on: it is the
# approval a person answers from the web page.
#
# The script goes through FAKE_TOOL_SCRIPT rather than FAKE_TOOL_NAME: only the
# multi-shot form streams its tool call, and a gateway turn always asks for a
# stream. With the single-shot form the provider answers with a JSON body where
# the client expects an event stream, the turn ends on "Streaming response ended
# before any complete data was received", and the gate is never reached.
printf '[["exit_plan_mode","{}"]]\n' > "$work/tools.json"
FAKE_TOOL_SCRIPT="$work/tools.json" \
  python3 "$root/scripts/acceptance/fake_provider.py" toolcall "$llm_port" FAKE_ANSWER \
  > "$work/llm.log" 2>&1 &
pids+=("$!")

wait_healthy() {
  local port=$1 home=$2 name=$3 pid=$4
  for _ in $(seq 1 90); do
    if curl -fsS "http://127.0.0.1:$port/healthz" > /dev/null 2>&1; then
      return 0
    fi
    if ! kill -0 "$pid" 2>/dev/null; then
      echo "replica $name exited during startup:" >&2
      tail -40 "$home/logs/error.log" 2>/dev/null >&2 || true
      return 1
    fi
    sleep 1
  done
  echo "replica $name never became healthy" >&2
  tail -40 "$home/logs/error.log" 2>/dev/null >&2 || true
  return 1
}

start_replica() {
  local home=$1 port=$2 name=$3
  echo "== replica $name on 127.0.0.1:$port"
  FOREBRAIN_HOME="$home" FOREBRAIN_INTERNAL_ADDR="http://127.0.0.1:$port" \
    FOREBRAIN_GATEWAY_TOKEN="$token" FAKE_LLM_KEY=fake-key \
    "$work/forebrain" gateway start >> "$work/$name.log" 2>&1 &
  local pid=$!
  pids+=("$pid")
  wait_healthy "$port" "$home" "$name" "$pid"
}

start_replica "$home_a" "$port_a" a
start_replica "$home_b" "$port_b" b

echo "== assertions"
mkdir -p "$work/project"
python3 "$root/scripts/acceptance/multi_replica_client.py" \
  "127.0.0.1:$port_a" "127.0.0.1:$port_b" "$token" "$work/project"

echo "PASS: multi-replica forwarding on $engine"
