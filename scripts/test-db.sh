#!/usr/bin/env bash
# The three-database test matrix (plan 209 §3.4, design §2.8).
#
# The store ships on SQLite, MySQL 8 and PostgreSQL 14. SQLite is the default a
# plain `go test ./...` uses; this script starts one scratch server of each of
# the other two and runs the dialect tests against them, so the same store code
# is exercised on all three engines.
#
#   ./scripts/test-db.sh up      start the two containers (idempotent)
#   ./scripts/test-db.sh sqlite  run the whole suite on SQLite only
#   ./scripts/test-db.sh mysql   run every package with an engine arm against MySQL
#   ./scripts/test-db.sh postgres  the same arms against PostgreSQL
#   ./scripts/test-db.sh all     both engines, plus the SQLite suite
#   ./scripts/test-db.sh check   is engine_arms still every package with an arm?
#   ./scripts/test-db.sh down    stop and remove the containers
#
# Each engine run walks the engine_arms table below; a row whose pattern matches
# no test fails the run instead of reporting a silent `ok`.
#
# Everything a run creates lives inside the container: each test derives a
# database of its own (fbt_<hash>) on the server the URL names, so the two
# servers may be shared with another checkout without collisions.
set -euo pipefail

root=$(git rev-parse --show-toplevel)
cd "$root"

mysql_url='mysql://root:fb@127.0.0.1:13306/fbt'
postgres_url='postgres://postgres:fb@127.0.0.1:15432/postgres?sslmode=disable'

# Every Go command needs cgo and FTS5; every test run needs a scratch home so an
# inherited FOREBRAIN_HOME never reaches one.
export CGO_ENABLED=1
go_test=(go test -tags fts5 -count=1)

scratch_home() {
  mktemp -d
}

up() {
  docker inspect fb-mysql >/dev/null 2>&1 || \
    docker run -d --name fb-mysql -e MYSQL_ROOT_PASSWORD=fb -p 13306:3306 mysql:8.0 >/dev/null
  docker inspect fb-pg >/dev/null 2>&1 || \
    docker run -d --name fb-pg -e POSTGRES_PASSWORD=fb -p 15432:5432 postgres:14 >/dev/null
  for _ in $(seq 1 60); do
    if docker exec fb-mysql mysqladmin ping -uroot -pfb --silent >/dev/null 2>&1 &&
       docker exec fb-pg pg_isready -U postgres >/dev/null 2>&1; then
      echo "both servers are up"
      return 0
    fi
    sleep 2
  done
  echo "the servers did not come up" >&2
  return 1
}

# Every package with an engine arm, and the tests in it that need a server.
# "Engine arm" = a test that reaches state.OpenStateForTest or reads
# FOREBRAIN_TEST_DATABASE_URL; on SQLite they pass without a server, which is
# exactly why a missing row here is invisible. `check` (below) enforces this
# list against the same grep, so a package cannot lose its row silently.
# Fragments are case-sensitive (go test -run takes a Go regexp); every fragment
# below was verified to match at least one test name at the planning baseline.
engine_arms=(
  "./pkg/state/dbx/...|Dialect|Open|Tx|Lock"
  "./pkg/state/|Dialect|Schema|Open|Reap|SearchBackslash|StoreRepairs|Repair|Parts|Output|Events|Artifact|Project"
  "./pkg/process|Lease|ImportLegacy|ConfigDB|Startup|PeerInbound"
  "./pkg/gateway|ControlPlane|RunControl|Channels"
  "./pkg/memory|Search|Index|Consolidation"
  "./pkg/migrate|Codex|Sessions|LSP"
  "./pkg/tool|ShellOutput|Artifact"
  "./pkg/turn|Session|Scheduler|Rewind|Events|Batch|Approval"
  "./pkg/run|Subagent|Runner|Config|Skills|Compaction|Permissions|AttemptLedger|Transcript|ImageRefusal|MemoryCitation"
  "./pkg/hook|Transcript|ToolMiddleware|PermissionRequest"
  "./pkg/tui|RecordedLikeTheGateway|PrimaryBudget|SurfaceToolApproval|WithdrawSurface|BindingANewConversation"
  "./pkg/assembly|Compact|Resume|Checkpoint"
)

run_dialect() {
  local url=$1
  local row pkgs pattern log ran
  for row in "${engine_arms[@]}"; do
    pkgs=${row%%|*}
    pattern=${row#*|}
    echo "== $pkgs on ${url%%:*}"
    log=$(mktemp)
    # tee keeps the live output on stderr while $log feeds the count below.
    # pipefail (set at the top of the script) makes this `if` fail when go
    # test fails — a failing leg must fail the run even though it ran tests.
    # Capturing only the grep count (the first draft's `| grep -c … || true`)
    # would have swallowed go test's exit status and turned a red leg green.
    if ! FOREBRAIN_TEST_DATABASE_URL="$url" FOREBRAIN_HOME="$(scratch_home)" \
      "${go_test[@]}" -v $pkgs -run "$pattern" 2>&1 | tee /dev/stderr >"$log"; then
      rm -f "$log"
      return 1
    fi
    ran=$(grep -c '^=== RUN' "$log" || true)
    rm -f "$log"
    # A -run that matches nothing is not a pass: it is the silent skip this
    # whole change exists to remove.
    if [ "${ran:-0}" -eq 0 ]; then
      echo "no test matched '$pattern' in $pkgs — the row is stale" >&2
      return 1
    fi
    echo "   $ran tests matched"
  done
}

run_sqlite() {
  FOREBRAIN_HOME="$(scratch_home)" "${go_test[@]}" ./... -p 2
}

# The anti-rot guard: every package whose tests reach the engine must have a row
# above, or its arms run on SQLite while the matrix reports success.
check() {
  local missing=0 pkg
  local have
  have=$(grep -rlE 'FOREBRAIN_TEST_DATABASE_URL|OpenStateForTest\(' --include='*_test.go' pkg 2>/dev/null \
    | xargs -n1 dirname 2>/dev/null | sort -u)
  for pkg in $have; do
    if ! printf '%s\n' "${engine_arms[@]}" | grep -q "$pkg"; then
      echo "engine arms in ./$pkg but no row in engine_arms" >&2
      missing=1
    fi
  done
  if [ "$missing" -ne 0 ]; then
    echo "add the package to engine_arms (with the tests that need a server)" >&2
    return 1
  fi
  echo "engine arms: $(printf '%s\n' "${engine_arms[@]}" | wc -l | tr -d ' ') rows, every package with arms listed"
}

case "${1:-all}" in
  up) up ;;
  sqlite) run_sqlite ;;
  mysql) run_dialect "$mysql_url" ;;
  postgres) run_dialect "$postgres_url" ;;
  check) check ;;
  all)
    up
    run_sqlite
    run_dialect "$mysql_url"
    run_dialect "$postgres_url"
    ;;
  down)
    docker rm -f fb-mysql fb-pg >/dev/null 2>&1 || true
    ;;
  *)
    echo "usage: $0 [up|sqlite|mysql|postgres|all|check|down]" >&2
    exit 2
    ;;
esac
