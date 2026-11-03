#!/usr/bin/env bash
# Drift check for Claude Code's peer-messaging protocol.
#
# forebrain's cross-process interop is a wire-protocol clone of the peer
# messaging Claude Code implements (see docs/plan/CROSS_PROCESS_PEER_COORDINATION_DESIGN.md
# §3, reverse-engineered from 2.1.301). That protocol is private and moves
# between releases, so after upgrading the local Claude Code -- and before
# changing pkg/peer -- run this first.
#
# It is read-only: it greps the installed binary for the stable literals the
# protocol is recognised by, and inspects Claude Code's own rendezvous records.
# It never writes anything, never starts Claude Code, and skips forebrain's own
# records (they carry a "forebrain" key).
#
# A machine can hold more than one Claude Code -- a Homebrew cask and an npm
# global install, say, and they do not have to agree on version. Every install
# found is checked and reported, because whichever `claude` is first on PATH is
# the one that would actually be run. The exit code is 0 when at least one
# install speaks the full protocol; an install that does not is printed as
# WARNING rather than silently ignored.
#
# Exit 0: at least one Claude Code on this machine carries every literal.
# Exit 1: none does -- the protocol has moved. Read design §3, then re-run the
#         interop acceptance (plans/201) before changing pkg/peer.
#
# FOREBRAIN_CC_SESSIONS_DIR overrides the rendezvous directory that is read
# (default ~/.claude/sessions); it is a read-only override for a machine where
# that directory is not yours to look at.

set -uo pipefail

# The literals below are the protocol's stable identifiers: the envelope tag,
# the control-frame actions, the socket namespace, the inbound rate-limit
# object, and one log line from the messaging server. Each is a fixed string,
# so grep -F is used throughout.
LITERALS=(
  'cross-session-message'
  'peer_message_status'
  'notify_when_idle'
  'peer_idle_notice'
  'cc-socks'
  'bucketCapacity:30,refillPerSecond:0.5,dedupWindowMs:30000,maxSelfHops:10,maxChainLength:28,maxTrackedSenders:256'
  '[uds-messaging] Closing a connection that sent no complete line within'
)

# claude_binaries prints the real path of every Claude Code install it can
# find: the one `claude` resolves to, the npm global install (nvm and friends),
# and the Homebrew cask. Duplicates are removed by real path.
claude_binaries() {
  local p
  if p=$(command -v claude 2>/dev/null) && [ -n "$p" ]; then
    readlink -f "$p" 2>/dev/null || printf '%s\n' "$p"
  fi
  local f
  for f in "$HOME"/.nvm/versions/node/*/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe; do
    [ -x "$f" ] && printf '%s\n' "$f"
  done
  for f in /opt/homebrew/Caskroom/claude-code/*/claude /usr/local/Caskroom/claude-code/*/claude; do
    [ -x "$f" ] && printf '%s\n' "$f"
  done
}

# check_binary prints the version and one OK/MISSING line per literal, and
# returns 0 only when every literal is present. One grep pass collects all the
# matches, which is the same answer `grep -c` per literal would give on a
# 250 MB binary, a seventh of the work.
check_binary() {
  local bin=$1 version matched ok=1 arg
  local args=()
  for arg in "${LITERALS[@]}"; do args+=(-e "$arg"); done

  version=$("$bin" --version 2>/dev/null | head -1)
  printf 'Claude Code: %s\n' "$bin"
  printf '  version: %s\n' "${version:-unknown}"

  matched=$(LC_ALL=C grep -a -F -o "${args[@]}" "$bin" 2>/dev/null | sort -u)
  local lit
  for lit in "${LITERALS[@]}"; do
    if printf '%s\n' "$matched" | grep -qxF "$lit"; then
      printf '  OK      %s\n' "$lit"
    else
      printf '  MISSING %s\n' "$lit"
      ok=0
    fi
  done
  return $((1 - ok))
}

# check_sessions reads Claude Code's own rendezvous records -- those without a
# "forebrain" key -- and checks the two fields the design pins: peerProtocol
# and the kind/status vocabularies. Records that are unreadable are skipped,
# not fatal: the directory is Claude Code's, and a half-written file is not a
# protocol change.
check_sessions() {
  local dir=${FOREBRAIN_CC_SESSIONS_DIR:-$HOME/.claude/sessions}
  [ -d "$dir" ] || { printf 'no rendezvous directory at %s\n' "$dir"; return 0; }
  python3 - "$dir" <<'PY'
import glob, json, os, sys

d = sys.argv[1]
kinds = {"interactive", "bg", "daemon", "daemon-worker"}
statuses = {"busy", "shell", "idle", "waiting"}
checked = 0
problems = []
for path in sorted(glob.glob(os.path.join(d, "*.json"))):
    try:
        with open(path) as fh:
            rec = json.load(fh)
    except Exception:
        continue
    if not isinstance(rec, dict) or "forebrain" in rec:
        continue
    checked += 1
    if rec.get("peerProtocol") != 1:
        problems.append("%s: peerProtocol=%r" % (path, rec.get("peerProtocol")))
    kind = rec.get("kind")
    if kind is not None and kind not in kinds:
        problems.append("%s: kind=%r" % (path, kind))
    status = rec.get("status")
    if status is not None and status not in statuses:
        problems.append("%s: status=%r" % (path, status))
print("rendezvous records checked: %d (%s)" % (checked, d))
for line in problems:
    print("  MISSING " + line)
sys.exit(1 if problems else 0)
PY
}

bins=()
while IFS= read -r b; do
  [ -n "$b" ] && bins+=("$b")
done < <(claude_binaries | awk '!_[$0]++')

if [ "${#bins[@]}" -eq 0 ]; then
  printf 'no Claude Code found on this machine; nothing to check\n'
  exit 0
fi

passing=0
for b in "${bins[@]}"; do
  if check_binary "$b"; then
    passing=$((passing + 1))
  else
    printf '  WARNING this install does not speak the peer protocol forebrain implements;\n'
    printf '          if `claude` resolves here, forebrain cannot interop with it\n'
  fi
done

if ! check_sessions; then
  printf 'Claude Code rendezvous records carry unexpected values.\n'
  exit 1
fi

if [ "$passing" -eq 0 ]; then
  printf 'Claude Code 对等协议可能已变：先读设计 §3，再按 plans/201 重跑互通验收。\n'
  exit 1
fi

printf 'peer protocol OK (%d of %d install(s) checked)\n' "$passing" "${#bins[@]}"
exit 0
