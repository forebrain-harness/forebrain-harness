#!/bin/sh
# First-start configuration for the gateway image (plan 214 §3.1).
#
# Runs only for `gateway start`: that is the command that binds an address, needs
# a token and reads a database URL. Every other invocation (`--version`,
# `gateway status`, a shell for debugging) is handed straight to the binary
# without touching the home.
#
# A home that already has forebrain.yaml is left exactly as it is. After the
# first boot the file belongs to the web's configuration editor, which writes
# back into it, and to `forebrain.yaml`'s supported keys only
# (pkg/config/file_keys.go). Nothing here ever overwrites it.
#
# Inputs, all optional:
#   FOREBRAIN_HTTP_ADDR     the address the gateway binds (default 0.0.0.0:6060 —
#                           inside a container 127.0.0.1 is reachable only from
#                           inside that container).
#   FOREBRAIN_GATEWAY_TOKEN the shared secret every request carries. Generated
#                           and printed once when it is not supplied.
#   FOREBRAIN_DATABASE_URL  mysql://… or postgres://…; empty means the SQLite
#                           file in the home's state directory.
#
# The token is written to $FOREBRAIN_HOME/.env (0600) and the config references
# it as ${FOREBRAIN_GATEWAY_TOKEN}, so the secret is not in a file an operator
# pastes into a ticket, and a replica that reads the same shared home gets the
# same one (deploy/compose/multi-replica.yaml seeds it the same way).
set -eu

log() { printf 'forebrain-entrypoint: %s\n' "$*" >&2; }

# A 64-hex-character token, the same shape the binary generates for a home it
# seeds itself (pkg/home/seed.go).
generate_token() {
  od -An -tx1 -N32 /dev/urandom | tr -d ' \n'
}

# Put one assignment into .env, replacing an existing one rather than adding a
# second: a home whose config was removed but whose .env survived must not end up
# with two tokens, one of which nothing reads.
set_env_var() {
  file=$1 key=$2 value=$3
  if [ -f "$file" ] && grep -q "^${key}=" "$file"; then
    sed -i "s|^${key}=.*|${key}=${value}|" "$file"
  else
    printf '%s=%s\n' "$key" "$value" >>"$file"
  fi
  chmod 0600 "$file"
}

seed_home() {
  home=${FOREBRAIN_HOME:-/data/forebrain}
  cfg=$home/forebrain.yaml
  mkdir -p "$home"
  if [ -e "$cfg" ]; then
    log "keeping the existing $cfg"
    return 0
  fi

  token=${FOREBRAIN_GATEWAY_TOKEN:-}
  generated=0
  if [ -z "$token" ]; then
    token=$(generate_token)
    generated=1
  fi
  set_env_var "$home/.env" FOREBRAIN_GATEWAY_TOKEN "$token"

  http_addr=${FOREBRAIN_HTTP_ADDR:-0.0.0.0:6060}
  # Written whole and then renamed, so a second replica starting against the
  # same shared home never reads a half-written config (plan 213 §3.6).
  {
    printf 'gateway:\n'
    printf '  http_addr: "%s"\n' "$http_addr"
    printf '  auth:\n'
    printf '    token: "${FOREBRAIN_GATEWAY_TOKEN}"\n'
    if [ -n "${FOREBRAIN_DATABASE_URL:-}" ]; then
      printf 'database:\n'
      printf '  url: "%s"\n' "$FOREBRAIN_DATABASE_URL"
    fi
  } >"$cfg.tmp"
  mv "$cfg.tmp" "$cfg"
  chmod 0600 "$cfg"
  log "wrote $cfg (http_addr $http_addr)"

  if [ "$generated" = 1 ]; then
    # Printed once: the operator needs it to sign in, and there is no other way
    # to learn a token nobody supplied. It is the same value that is now in .env.
    log "no FOREBRAIN_GATEWAY_TOKEN was given; generated one: $token"
  fi
}

if [ "${1:-}" = "gateway" ] && [ "${2:-}" = "start" ]; then
  seed_home
fi

# Everything is handed to the binary: the CMD is `gateway start`, and anything
# an operator overrides the command with (`--version`, `gateway status`, a
# debugging shell) reaches the binary too. Without the /app/forebrain prefix
# `exec "$@"` would try to run `--version` as a program name and fail.
exec /app/forebrain "$@"
