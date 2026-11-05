#!/usr/bin/env bash
# Does the sandbox probe ask the same question a real command asks?
#
# Since plan 215 "this machine has no usable sandbox" is not a degraded answer
# but a permanent one: every command then runs uncontained with full access and
# the approval picker never appears. That answer comes from a probe
# (pkg/safety/process.go probeSandbox) whose argv is a narrower version of what
# a real command gets (bubblewrapBaseArgs). deploy/seccomp/README.md measured by
# hand that both shapes fail the same way without the seccomp profile and
# succeed with it; this script is that measurement, repeatable on a Mac.
#
# It runs the two argv shapes inside the gateway image as the image's own
# unprivileged uid -- once with deploy/seccomp/forebrain-bubblewrap.json plus
# systempaths=unconfined, once with Docker's default seccomp profile -- and
# prints the four exit codes in the order it ran them:
#
#   probe + profile    the probe's argv                     want 0
#   real  + profile    a real workspace-write command       want 0
#   probe + default    the same probe, no profile           want 1
#   real  + default    the same command, no profile         want 1
#
# The two shapes must reach the same verdict. If they ever disagree, the probe
# is asking a question the engine does not, and this machine's sandbox will be
# reported wrongly -- in the direction where containment silently disappears.
#
# Every flag in the real argv comes from pkg/safety/backend_linux.go; the probe
# argv is the one frozen in probeSandbox, including --unshare-user, which plan
# 225 measured and deliberately left alone. Change either and change this file.
#
# Read-only: it builds nothing, mounts nothing, and writes nothing outside the
# container. It reuses forebrain:dev when that image is already there.
#
# Exit 0: the four codes are exactly 0 0 1 1.
# Exit 1: they are not -- the engine's behaviour and the probe's answer have
#         drifted apart. Re-run deploy/seccomp/README.md's per-syscall table
#         before trusting either.
# Exit 2: Docker is not reachable, or the image is not built. Nothing is
#         skipped silently: the message says what to run.
#
# FOREBRAIN_SANDBOX_PROBE_IMAGE overrides the image (default forebrain:dev).

set -uo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
image=${FOREBRAIN_SANDBOX_PROBE_IMAGE:-forebrain:dev}
seccomp_profile="$repo_root/deploy/seccomp/forebrain-bubblewrap.json"
# The argv runs inside the image, so the workspace it writes to and starts in is
# the image's own /app -- what a session started in the gateway container sees.
inner_workdir=/app

# The probe's argv: probeSandbox, verbatim.
probe_argv=(
  bwrap
  --unshare-user --unshare-pid --die-with-parent
  --ro-bind / / --dev /dev --proc /proc -- true
)

# A real workspace-write command's argv: bubblewrapBaseArgs + buildBubblewrapArgs
# for inner_workdir. The profile defaults are ro-binds of /bin /etc /lib /sbin
# /usr; /lib64 and /nix/store do not exist in this image and are left out the
# same way appendFilesystemPolicyArgs skips a missing path. The network stays
# reachable because EffectiveNetworkAccess defaults to true, so no --unshare-net.
real_argv=(
  bwrap
  --die-with-parent --new-session --unshare-pid --unshare-uts --unshare-ipc
  --tmpfs /
  --proc /proc --dev /dev
  --ro-bind /bin /bin --ro-bind /etc /etc --ro-bind /lib /lib
  --ro-bind /sbin /sbin --ro-bind /usr /usr
  --bind "$inner_workdir" "$inner_workdir"
  --chdir "$inner_workdir"
  /bin/sh -lc true
)

fail() {
  printf 'sandbox-probe-check: %s\n' "$*" >&2
  exit 2
}

command -v docker >/dev/null 2>&1 || fail "docker is not on PATH"
[ -f "$seccomp_profile" ] || fail "missing seccomp profile: $seccomp_profile"
docker info >/dev/null 2>&1 || fail "docker is not reachable (is Docker Desktop running?)"

if ! docker image inspect "$image" >/dev/null 2>&1; then
  cat >&2 <<EOF
sandbox-probe-check: the image $image is not built, and this check does not
build it (it is a ~1.4 GB image). Build it first, then rerun:

  docker build -t $image .

EOF
  exit 2
fi

image_user=$(docker image inspect "$image" --format '{{.Config.User}}')
[ -n "$image_user" ] || image_user=10001:10001
bwrap_version=$(docker run --rm --entrypoint bwrap "$image" --version 2>&1 | head -1)
docker_version=$(docker version --format '{{.Server.Version}}/{{.Server.Arch}}' 2>/dev/null)

# run_shape runs one argv inside the image and sets `shape_code` to its exit
# code and `shape_output` to what it printed. The argv is passed as arguments
# rather than pasted into a shell string, so nothing in it is re-quoted.
# --entrypoint /bin/sh is needed because the image's entrypoint is
# forebrain-entrypoint.
shape_code=
shape_output=
run_shape() {
  local profile=$1
  shift
  local -a cmd=(docker run --rm --entrypoint /bin/sh --user "$image_user")
  if [ "$profile" = profile ]; then
    cmd+=(--security-opt "seccomp=$seccomp_profile" --security-opt systempaths=unconfined)
  fi
  cmd+=("$image" -c 'exec "$@"' sh "$@")
  shape_output=$("${cmd[@]}" 2>&1)
  shape_code=$?
}

# report prints one labelled row, and the first line of output for a shape that
# failed -- bubblewrap's own words, which name the syscall it was refused.
report() {
  local label=$1 code=$2
  printf '  %-38s exit %s\n' "$label" "$code"
  if [ "$code" != 0 ] && [ -n "$shape_output" ]; then
    printf '    %s\n' "$(printf '%s\n' "$shape_output" | head -1)"
  fi
}

printf 'image:    %s (%s, %s, %s)\n' "$image" "$image_user" "${bwrap_version:-bubblewrap unknown}" "${docker_version:-docker unknown}"
printf 'profile:  %s\n' "${seccomp_profile#"$repo_root"/}"
printf '\n'

run_shape profile "${probe_argv[@]}"
probe_with=$shape_code
report 'probe argv + seccomp profile' "$probe_with"

run_shape profile "${real_argv[@]}"
real_with=$shape_code
report 'real argv  + seccomp profile' "$real_with"

run_shape default "${probe_argv[@]}"
probe_without=$shape_code
report 'probe argv + default profile' "$probe_without"

run_shape default "${real_argv[@]}"
real_without=$shape_code
report 'real argv  + default profile' "$real_without"

printf '\nexit codes: %s %s %s %s\n' "$probe_with" "$real_with" "$probe_without" "$real_without"

ok=1
[ "$probe_with" = 0 ] || ok=0
[ "$real_with" = 0 ] || ok=0
[ "$probe_without" = 1 ] || ok=0
[ "$real_without" = 1 ] || ok=0

if [ "$ok" = 1 ]; then
  printf 'ok: with the profile both shapes sandbox, without it neither does\n'
  exit 0
fi

printf 'sandbox-probe-check: the codes are not 0 0 1 1, so the probe and a real\n' >&2
printf 'command do not agree on this machine -- see deploy/seccomp/README.md.\n' >&2
exit 1
