#!/usr/bin/env bash
# The three-database test matrix (plan 209 §3.4, design §2.8).
#
# The store ships on SQLite, MySQL 8 and PostgreSQL 14. SQLite is the default a
# plain `go test ./...` uses; this script starts one scratch server of each of
# the other two and runs the dialect tests against them, so the same store code
# is exercised on all three engines.
#
#   ./scripts/test-db.sh up      start the two containers (idempotent)
#   ./scripts/test-db.sh mysql   run the dialect tests against MySQL
#   ./scripts/test-db.sh postgres  run the dialect tests against PostgreSQL
#   ./scripts/test-db.sh all     both, plus the SQLite suite
#   ./scripts/test-db.sh down    stop and remove the containers
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

run_dialect() {
  local url=$1
  FOREBRAIN_TEST_DATABASE_URL="$url" FOREBRAIN_HOME="$(scratch_home)" \
    "${go_test[@]}" ./pkg/state/dbx/... ./pkg/state/ -run 'Dialect|Schema|Open'
}

run_sqlite() {
  FOREBRAIN_HOME="$(scratch_home)" "${go_test[@]}" ./... -p 2
}

case "${1:-all}" in
  up) up ;;
  mysql) run_dialect "$mysql_url" ;;
  postgres) run_dialect "$postgres_url" ;;
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
    echo "usage: $0 [up|mysql|postgres|all|down]" >&2
    exit 2
    ;;
esac
