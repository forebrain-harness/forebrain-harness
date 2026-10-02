#!/usr/bin/env bash
# Automated acceptance for the gateway's web surface: build the real binary,
# start `forebrain gateway start` with an isolated FOREBRAIN_HOME, serve the
# freshly built UI from a scratch directory (never pkg/gateway/dist), and
# drive the real Chrome through Playwright. Later plans append their cases to
# frontend/e2e/; they all pass here as part of their done criteria.
#
# Real-model mode (FOREBRAIN_E2E_REAL_LLM=1) swaps the fake provider for the
# owner's Zhipu credentials in ~/.forebrain/e2e-zhipu.env (chmod 600, never
# committed); assertions on reply text relax to "a non-empty assistant reply".
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="${FOREBRAIN_E2E_WORK:-${TMPDIR:-/tmp}/forebrain-web-e2e}"
GW_PORT="${FOREBRAIN_E2E_GW_PORT:-8761}"
LLM_PORT="${FOREBRAIN_E2E_LLM_PORT:-8762}"
HOME_DIR="$WORK/home"
PROJ="$WORK/proj"
WEBUI="$WORK/webui"
SHOTS="$WORK/shots"
BIN="$WORK/forebrain"
TOKEN=""

die() { echo "web e2e: $*" >&2; exit 1; }

# Kill anything a previous (crashed or cancelled) run left bound to our ports.
# Scoped to this script's own work tree: the binary path is unique to $WORK.
kill_stale() {
  pkill -f "$BIN gateway start" 2>/dev/null || true
  pkill -f "fake_provider.py reply $LLM_PORT" 2>/dev/null || true
  sleep 0.3
}

cleanup() {
  local code=$?
  [ -n "${GW_PID:-}" ] && kill "$GW_PID" 2>/dev/null || true
  [ -n "${LLM_PID:-}" ] && kill "$LLM_PID" 2>/dev/null || true
  kill_stale
  if [ "$code" -ne 0 ] && [ -f "$WORK/gateway.err" ]; then
    echo '--- gateway.err (last 50 lines) ---' >&2
    tail -50 "$WORK/gateway.err" >&2 || true
  fi
  exit "$code"
}
trap cleanup EXIT

kill_stale

# 1. Fresh scratch tree. The run logs are truncated too, so a failure's
#    gateway.err belongs to THIS run, not appended behind an older one.
rm -rf "$HOME_DIR" "$PROJ" "$WEBUI" "$SHOTS"
mkdir -p "$HOME_DIR" "$PROJ" "$WEBUI" "$SHOTS"
: > "$WORK/gateway.out"
: > "$WORK/gateway.err"

# 2. Build the binary and the UI in parallel (independent toolchains). The UI
#    is served from FOREBRAIN_STATIC_DIST so pkg/gateway/dist (managed by the
#    release flow) is never touched. A failed branch aborts the run with its
#    log; `wait` alone would swallow a nonzero exit.
( cd "$REPO" && CGO_ENABLED=1 go build -tags fts5 -o "$BIN" ./cmd/forebrain ) \
  > "$WORK/go-build.log" 2>&1 &
GO_BUILD_PID=$!
( cd "$REPO/frontend" && corepack pnpm build --outDir "$WEBUI" --emptyOutDir ) \
  > "$WORK/ui-build.log" 2>&1 &
UI_BUILD_PID=$!
wait "$GO_BUILD_PID" || { cat "$WORK/go-build.log" >&2; die "go build failed"; }
wait "$UI_BUILD_PID" || { cat "$WORK/ui-build.log" >&2; die "frontend build failed"; }
"$REPO/scripts/install-dictionary.sh" "$WORK"

# 3. This run's gateway token. The yaml references it via ${ENV}: plaintext
#    secrets in config are rejected at startup by design.
TOKEN="$(openssl rand -hex 32)"
umask 077
printf 'FOREBRAIN_GATEWAY_TOKEN=%s\n' "$TOKEN" > "$HOME_DIR/.env"
umask 022

# 4. Gateway + agent config.
if [ "${FOREBRAIN_E2E_REAL_LLM:-0}" = "1" ]; then
  [ -f "$HOME/.forebrain/e2e-zhipu.env" ] || die "real-LLM mode needs ~/.forebrain/e2e-zhipu.env"
  set -a; . "$HOME/.forebrain/e2e-zhipu.env"; set +a
  cat > "$HOME_DIR/forebrain.yaml" <<YAML
gateway:
  http_addr: 127.0.0.1:$GW_PORT
  auth:
    mode: token
    token: \${FOREBRAIN_GATEWAY_TOKEN}
agents:
  definitions:
    main:
      primary: true
      enable_subagent: false
      llm_providers:
      - provider: zhipu
        model: glm-5.3-flash
        api_key: \${FOREBRAIN_E2E_ZHIPU_KEY}
        base_url: https://open.bigmodel.cn/api/coding/paas/v4
YAML
else
  cat > "$HOME_DIR/forebrain.yaml" <<YAML
gateway:
  http_addr: 127.0.0.1:$GW_PORT
  auth:
    mode: token
    token: \${FOREBRAIN_GATEWAY_TOKEN}
agents:
  definitions:
    main:
      primary: true
      enable_subagent: false
      llm_providers:
      - provider: deepseek
        model: deepseek-chat
        api_key: \${FAKE_LLM_KEY}
        base_url: http://127.0.0.1:$LLM_PORT
YAML
fi

# 5. The gateway resolves projects relative to its working directory.
git -C "$PROJ" init -q
echo "scratch project for gateway web e2e" > "$PROJ/README.md"

# 5b. A valid single-skill zip for the offline-install browser flow.
mkdir -p "$WORK/fixtures"
python3 - "$WORK/fixtures/demo-skill.zip" <<'PYZIP'
import sys, zipfile
skill = "---\nname: demo-e2e\ndescription: e2e offline install fixture\n---\n\nbody\n"
with zipfile.ZipFile(sys.argv[1], "w") as zf:
    zf.writestr("demo-e2e/SKILL.md", skill)
    zf.writestr("demo-e2e/helper.md", "# helper\n")
PYZIP

# 6. Fake provider (skipped in real-LLM mode). `exec` makes $! the python
#    process itself, so the exit trap kills the real server, not a wrapper.
if [ "${FOREBRAIN_E2E_REAL_LLM:-0}" != "1" ]; then
  ( exec python3 "$REPO/scripts/acceptance/fake_provider.py" reply "$LLM_PORT" E2E_REPLY_OK \
    > "$WORK/provider.log" 2>&1 ) &
  LLM_PID=$!
  for _ in $(seq 1 50); do
    grep -q LISTENING "$WORK/provider.log" 2>/dev/null && break
    sleep 0.1
  done
  grep -q LISTENING "$WORK/provider.log" || die "fake provider never bound port $LLM_PORT"
fi

# 7. Gateway. `exec` is load-bearing: without it $! is the subshell, the trap
#    kills only the wrapper, and the orphaned gateway poisons the next run
#    (bind panic here, and a stale token answering every request).
( cd "$PROJ" && exec env FAKE_LLM_KEY=sk-local-fake FOREBRAIN_HOME="$HOME_DIR" \
    FOREBRAIN_STATIC_DIST="$WEBUI" "$BIN" gateway start \
    > "$WORK/gateway.out" 2> "$WORK/gateway.err" ) &
GW_PID=$!

# 8. Wait for the health probe — of OUR process, not whatever else might be
#    on the port: if the gateway died, say so instead of testing a stranger.
for _ in $(seq 1 150); do
  curl -fsS "http://127.0.0.1:$GW_PORT/healthz" > /dev/null 2>&1 && break
  kill -0 "$GW_PID" 2>/dev/null || { tail -20 "$WORK/gateway.err" >&2; die "gateway exited during startup"; }
  sleep 0.2
done
curl -fsS "http://127.0.0.1:$GW_PORT/healthz" > /dev/null || die "gateway never answered /healthz"

# 9. Browser suite.
( cd "$REPO/frontend" && \
  E2E_BASE_URL="http://127.0.0.1:$GW_PORT" \
  E2E_TOKEN="$TOKEN" \
  E2E_GATEWAY_OUT="$WORK/gateway.out" \
  E2E_SHOTS="$SHOTS" \
  E2E_REAL_LLM="${FOREBRAIN_E2E_REAL_LLM:-0}" \
  E2E_PROJECT_PARENT="$PROJ" \
  E2E_HOME="$HOME_DIR" \
  E2E_SKILL_ZIP="$WORK/fixtures/demo-skill.zip" \
  corepack pnpm e2e )

# 10. The CLI still authenticates with the header token.
FOREBRAIN_HOME="$HOME_DIR" "$BIN" gateway status | grep -q 'status=200' \
  || die "gateway status no longer reports status=200"

echo "screenshots: $SHOTS"
echo "web e2e: PASS"
