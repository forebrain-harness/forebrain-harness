# Plan 225: the sandbox probe answers for the commands that will actually run

> **Executor instructions**: Follow the steps in order and confirm each
> verification result. If a STOP condition occurs, stop and report. When done,
> update this plan's row in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 7d505aa3..HEAD -- pkg/safety/process.go pkg/safety/manager.go pkg/safety/process_test.go`
> Content mismatch against the excerpts is a STOP condition.

## Status

- **Priority**: P2 (was P1 — the argument-mismatch half was disproved by measurement)
- **Effort**: S
- **Risk**: LOW — the only behavioural change is "one retry before caching"; the
  probe's arguments stay exactly as they are.
- **Depends on**: none. **225 must land before 229** (both change how a sandbox
  decision is made; 225 fixes the signal, 229 changes the policy that consumes it).
- **Category**: correctness
- **Planned at**: commit `7d505aa3`, 2026-11-04 (revised same day after the Linux
  measurement below)
- **Finding**: branch audit 2026-11-04, finding 4 (`introduced` by plan 215)

## Why this matters

Since plan 215, "no usable sandbox" means every command runs **uncontained with
full access** and the approval picker is hidden — a deliberate owner decision.
That makes the availability answer the single most load-bearing bit in the
product: a false "unusable" silently removes all containment from a machine that
has a working sandbox, and nothing on any surface says so.

One defect makes the answer wrong today: it is cached for the life of the process
on the first failure — a 5-second timeout, a momentarily busy host, or a probe
launched before the temp dirs exist is permanent for that process. A second,
smaller gap is the *reason*: it is a coarse code. It already reaches `/status`
(`SandboxStatusLine`, `pkg/safety/process.go:414-429`) and `/sandbox`
(`FormatSandboxReport`, `:449-452`) through `SandboxReasonInWords`, but not the
TUI footer or Settings, and "bubblewrap unusable" does not distinguish a missing
binary from a kernel that refuses namespaces from a probe that timed out.

The `--unshare-user` hypothesis was **measured and rejected** — see "The defect
and the reason gap" below. The probe's argv does not change the answer and must
not be touched.

## Current state

`pkg/safety/process.go:628-690` — the probe and its cache:

```go
const sandboxProbeTimeout = 5 * time.Second

// sandboxProbe caches the probe for the life of the process: the answer is a
// property of this machine, and running it per command would cost a process
// launch each time. forced is what a test pinned it to — nil in the product.
var sandboxProbe struct {
	mu     sync.Mutex
	done   bool
	reason string
	forced *string
}

func probeSandboxReason(platform Platform) string {
	sandboxProbe.mu.Lock()
	defer sandboxProbe.mu.Unlock()
	if sandboxProbe.forced != nil {
		return *sandboxProbe.forced
	}
	if !sandboxProbe.done {
		sandboxProbe.reason = probeSandbox(platform)
		sandboxProbe.done = true
	}
	return sandboxProbe.reason
}

func probeSandbox(platform Platform) string {
	switch platform {
	case PlatformDarwin:
		return runSandboxProbe("sandbox-exec", "-p", "(version 1)(allow default)", "/usr/bin/true")
	case PlatformLinux, PlatformWSL:
		bwrap, err := lookBubblewrap()
		if err != nil {
			return "bubblewrap_not_found"
		}
		return runSandboxProbe(bwrap,
			"--unshare-user", "--unshare-pid", "--die-with-parent",
			"--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--", "true")
	}
	return ""
}
```

`pkg/safety/backend_linux.go:78-98` — the arguments a **real** command gets:

```go
func bubblewrapBaseArgs(cfg *appcfg.Root, req CommandRequest, workDir string) []string {
	req = constrainRequestToFilesystemProfile(req, workDir)
	args := []string{
		"--die-with-parent",
		"--new-session",
		"--unshare-pid",
		"--unshare-uts",
		"--unshare-ipc",
	}
	if !req.AllowNetwork {
		args = append(args, "--unshare-net")
	}
	if filesystemRulesContainRootRead(req) {
		args = append(args, "--ro-bind", "/", "/")
	} else {
		args = append(args, "--tmpfs", "/")
	}
	args = append(args, "--proc", "/proc", "--dev", "/dev")
	args = appendFilesystemPolicyArgs(args, req)
	return append(args, "--chdir", workDir)
}
```

`--unshare-user` appears **only** in the probe (`grep -rn "unshare-user" pkg/safety`
returns `process.go:672` and nothing else).

`pkg/safety/manager.go:280-296` — what an unusable sandbox does. This behaviour is
the owner's decision and must not change:

```go
	if decision.UnavailableReason != "" {
		// This machine has no usable sandbox, so the command runs uncontained —
		// the same way it runs in full access (owner 2026-10-31, plan 215 §0.1).
		// ...
		decision.UseSandbox = false
	}
```

`pkg/safety/manager.go:171-180` — a nil config also reports unavailable:

```go
	if cfg == nil {
		return SandboxDecision{Mode: ModeSandboxed, Profile: profile, UseSandbox: false, Reason: "sandbox_unavailable", Backend: backendForPlatform(detectPlatform()), SandboxType: sandboxTypeForPlatform(detectPlatform()), UnavailableReason: "sandbox configuration unavailable"}
	}
```

`pkg/safety/process.go:690-706` — `runSandboxProbe` maps any failure to
`seatbelt_unusable` / `bubblewrap_unusable`, discarding why.

### The defect and the reason gap

> **已实测排除一种猜测（2026-11-04，Docker + `forebrain:dev` + `deploy/seccomp/forebrain-bubblewrap.json`）。**
> 曾怀疑「探针多带 `--unshare-user`、真实命令不带 → 装了沙箱的机器被判不可用」。
> 实测：挂上 seccomp 配置时三种 argv 全部 exit 0；不挂时**两种 argv 都 exit 1**，
> 报错一字不差（`No permissions to create new namespace …`）。bubblewrap 无 setuid
> 模式下无论有没有 `--unshare-user` 都要建用户命名空间，该标志不改变成败。
> **因此探针的 argv 不用改**，也不要为它写测试（那会钉一个与行为无关的性质）。

1. **One failure is permanent.** `sandboxProbe.done = true` regardless of cause,
   so a `sandboxProbeTimeout` (5 s) under load, or a probe that runs before the
   session's temp dir exists, disables containment for the process's life. This is
   the defect worth fixing: the probe's answer is a capability of the machine, but
   a transient failure is cached as if it were.
2. **The reason is too coarse for a surface.** `UnavailableReason` is shown by
   `/status` and `/sandbox`, but it is one of a handful of exact codes
   (`SandboxReasonInWords` is a `switch` with no default translation), so a
   maintainer cannot tell "bubblewrap not installed" from "namespaces denied by
   the container runtime", and the TUI footer / Settings show nothing at all.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Vet | `go vet ./...` | exit 0 |
| Focused | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/safety -count=1` | `ok` |
| Neighbours | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run ./pkg/gateway -count=1` | `ok` |
| Real host check (Linux) | `bwrap --unshare-user -- true; echo "with user ns: $?"` and `bwrap --ro-bind / / --dev /dev --proc /proc -- true; echo "probe args: $?"` | the two shapes must reach the **same** verdict (both 0 on a host that can sandbox, both non-0 on one that cannot); a difference would mean the probe asks a different question, and the 2026-11-04 measurement says it does not |
| Real host check (macOS) | `sandbox-exec -p '(version 1)(allow default)' /usr/bin/true; echo $?` | 0 |

## Scope

**In scope**:
- `pkg/safety/process.go`
- `pkg/safety/manager.go` (only the `UnavailableReason` plumbing)
- `pkg/safety/process_test.go` (or the probe's existing test file)

**Out of scope**:
- `pkg/safety/backend_linux.go` / `backend_darwin.go` / `backend_windows.go` — the
  real command arguments stay as they are. If a fix suggests changing them, STOP.
- Making "no sandbox" refuse to run commands: that rule was deliberately removed
  by the owner (plan 215 §0.1).
- The approval picker's visibility (`approval_presets_visible` in
  `pkg/gateway/api_extra.go:5889`) — plan 215 settled it; this plan only enriches
  the reason string.
- Frontend display of the new reason (a separate plan; keep the API field only).

## Steps

### Step 1 — a failed probe is retried once, then cached

In `probeSandboxReason`, replace the single-shot cache with: on failure, retry
once after `100 * time.Millisecond`; only then set `done = true`. A success is
cached immediately. Keep the `forced` seam untouched. Add a package comment
explaining that a transient `sandboxProbeTimeout` must not disable containment for
the life of the process (this is the defect this plan exists for). **Do not change
`probeSandbox`'s argument lists** — see the measured note above.

**Verify**: add `TestProbeRetriesOnceThenCaches` — a recorder that fails the
first call and succeeds the second returns `""` and is called exactly twice; a
recorder that fails twice returns the reason, is called twice during the first
`probeSandboxReason`, and **not at all** on the second call (cached). Run
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/safety -run TestProbe -count=1`
→ green.

### Step 2 — name the reason so a surface can show it

Keep the existing strings (`bubblewrap_unusable`, `seatbelt_unusable`,
`bubblewrap_not_found`) — they are what `UnavailableReason` already carries — and
add nothing new to `probeSandbox`'s contract. Instead, make `runSandboxProbe`
append a short, non-sensitive discriminator: `"<reason>: timeout"` when
`ctx.Err() == context.DeadlineExceeded`, `"<reason>: exit N"` otherwise. Never
include the command's stderr (it can contain paths, and this string is meant for
display).

The reason is rendered by `SandboxReasonInWords` (`pkg/safety/process.go:476-502`),
which is an **exact** `switch`: an unrecognised string is shown verbatim, so a
suffix would put `bubblewrap_unusable: exit 1` in `/status` and `/sandbox`. Extend
that function in the same step — match the known codes by prefix and append the
translated detail ("timed out after 5s" / "exited with code N") — and add table
cases for the suffixed forms. Unrecognised codes still render as they are.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/safety -count=1`
→ `ok`, including a new assertion that a timeout yields the `: timeout` suffix and
that `SandboxReasonInWords("bubblewrap_unusable: timeout")` is a sentence, not the
raw code.

### Step 3 — a Linux leg that runs on a Mac

Add `scripts/sandbox-probe-check.sh` (read-only, no state writes): build or reuse
`forebrain:dev`, then run, inside it as the image's unprivileged uid, the probe's
argv and the real command's argv **twice** — once with
`--security-opt seccomp=deploy/seccomp/forebrain-bubblewrap.json --security-opt systempaths=unconfined`
and once with Docker's default seccomp. Assert the profiles differ — with the
config both commands exit 0, without it both exit 1 — and print the four exit
codes in that order. This is the evidence that
the probe's answer tracks the engine's real behaviour — the property plan 214's
seccomp README asserts by hand, made repeatable on macOS.

**Verify**: `./scripts/sandbox-probe-check.sh` → prints four codes
`0 0 1 1` and exits 0 on this machine (Docker Desktop 28.x, linux/arm64,
bubblewrap 0.8.0). If the image is missing, the script builds it or says which
`docker build` to run — do not silently skip.

### Step 4 — prove the decision path end to end

`pkg/safety/manager.go`'s nil-config branch is the one place where an
*unavailable* answer is produced without the probe. Leave the behaviour, but give
its reason the same display treatment: keep the code
`"sandbox configuration unavailable"`, add it to `SandboxReasonInWords`, and add a
test that `Decide` with a nil config still yields `UseSandbox == false` and a
non-empty `UnavailableReason` — pinning that the fallback is deliberate, not an
accident of a missing config.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run ./pkg/gateway -count=1` → `ok`.

## Test plan

- New: `TestProbeRetriesOnceThenCaches`, a timeout-suffix case, and the nil-config
  `Decide` pin. **Not** an argv test — see the measured note: the flag makes no
  difference to the engine's behaviour, so a test on it would pin noise.
- Structural pattern: the existing tests in `pkg/safety/process_test.go` that use
  the `sandboxProbe.forced` seam — reuse it; do not add a second global.
- Real-machine check: `./scripts/sandbox-probe-check.sh` (step 3) is the Linux leg;
  on macOS also run `sandbox-exec -p '(version 1)(allow default)' /usr/bin/true`
  and record its exit code in your report.

## Done criteria

ALL must hold:

- [ ] `go vet ./...` exits 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/safety ./pkg/tool ./pkg/run ./pkg/gateway -count=1` passes
- [ ] `grep -n "unshare-user" pkg/safety/process.go` still matches (the flag is
      deliberately unchanged; the measured note explains why)
- [ ] The probe is invoked at most twice per process on failure (asserted by the retry test)
- [ ] `./scripts/sandbox-probe-check.sh` prints `0 0 1 1` and exits 0, or the report
      says Docker was unavailable
- [ ] Only the in-scope files (plus the new script) are modified
- [ ] `plans/README.md` row for 225 updated

## STOP conditions

- You find yourself editing `probeSandbox`'s arguments "to match the real path":
  stop. The measurement in this plan shows it changes nothing, and changing it can
  make a working machine look unusable (the probe would start exercising namespaces
  the real command does not use).
- `probeSandbox`/`runSandboxProbe` cannot be observed without adding a production
  seam that does not already exist: stop and report the seam you would add. The
  `sandboxProbe.forced` seam is the precedent for a test-only override.
- The retry changes the answer on a host where the first probe fails and the
  second succeeds **for a real reason** (e.g. a race with a package manager
  installing bwrap): report it; the retry is for transients, not for installing
  software.
- You find another caller of `sandboxProbeReason` that assumes the answer is
  stable before the first command runs: report before changing the timing.

## Maintenance notes

- Whoever adds a sandbox flag to `bubblewrapBaseArgs` must add it to the probe in
  the same change; the probe comment is the reminder. A probe that passes with
  fewer flags than the command is a machine that reports "available" and then
  fails every command — the opposite failure and worse.
- A reviewer should scrutinise: no new flag in the probe, exactly one retry, the
  reason string carries no paths, and `manager.go`'s nil-config branch keeps
  behaving as before.
- Deliberately deferred: showing the reason on the TUI footer and in Settings.
  The string is now good enough to display; wiring it is UI work with its own
  copy rules (界面文案必须中文, no pink, no gradients).
