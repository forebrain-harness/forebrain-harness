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
#   FOREBRAIN_GATEWAY_TOKEN the shared secret every request carries.
#   FOREBRAIN_DATABASE_URL  mysql://… or postgres://…; empty means the SQLite
#                           file in the home's state directory.
#
# The token has exactly two homes (owner 2026-11-07, plan 268): the environment,
# which is what a container supplies, or gateway.auth.token in forebrain.yaml
# itself, in plaintext, in a file only the container's user can read. Nothing
# writes a .env any more.
#
# Models are added after the first boot, on the web's Models page (or by signing
# in to a subscription there): a key is not an environment input any more.
set -eu

log() { printf 'forebrain-entrypoint: %s\n' "$*" >&2; }

# A 64-hex-character token, the same shape the binary generates for a home it
# seeds itself (pkg/home/seed.go).
generate_token() {
  od -An -tx1 -N32 /dev/urandom | tr -d ' \n'
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

  http_addr=${FOREBRAIN_HTTP_ADDR:-0.0.0.0:6060}
  # An operator-supplied token stays in the environment, and the config keeps
  # the reference that reads it — restarting with another value just works. A
  # generated one has nobody to hand it to through the environment, so it is
  # written into the file, which only this container's user can read.
  if [ "$generated" = 1 ]; then
    token_line="    token: \"$token\""
  else
    token_line='    token: "${FOREBRAIN_GATEWAY_TOKEN}"'
  fi
  # Written whole and then renamed, so a second replica starting against the
  # same shared home never reads a half-written config (plan 213 §3.6).
  {
    printf 'gateway:\n'
    printf '  http_addr: "%s"\n' "$http_addr"
    printf '  auth:\n'
    printf '%s\n' "$token_line"
    if [ -n "${FOREBRAIN_DATABASE_URL:-}" ]; then
      printf 'database:\n'
      printf '  url: "%s"\n' "$FOREBRAIN_DATABASE_URL"
    fi
  } >"$cfg.tmp"
  mv "$cfg.tmp" "$cfg"
  chmod 0600 "$cfg"
  log "wrote $cfg (http_addr $http_addr, token in the file)"

  if [ "$generated" = 1 ]; then
    # Printed once: the operator needs it to sign in, and there is no other way
    # to learn a token nobody supplied. It is the same value that is now in the
    # file, so a restart keeps it.
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
