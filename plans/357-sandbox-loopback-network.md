# Plan 357: Sandboxed commands get loopback network without getting the internet

> **Executor instructions**: Follow this plan step by step. Run every verification
> command and confirm the expected result before moving to the next step. If
> anything in the "STOP and report" section occurs, stop and report — do not
> improvise. When done, update the status row for this plan in `plans/README.md`.
>
> **Drift check (run first)**:
> `git status --porcelain -- pkg/safety pkg/config pkg/tool` and
> `git diff --stat 6541b8ab -- pkg/safety pkg/config pkg/tool`.
> A `6541b8ab..HEAD` range diff is **always empty here** (the baseline is HEAD);
> use the two commands above, which see uncommitted work. If an in-scope file
> moved, compare the "Current state" anchors against the live code before
> proceeding; on a mismatch, STOP.

## Status

- **Category**: infra / sandbox
- **Priority**: P1
- **Effort**: S on macOS, M on Linux (one empirical probe decides whether Linux needs any change at all)
- **Risk**: MED (sandbox policy — a mistake either leaks the internet or blocks every local driver)
- **Owner decision** (2026-11-23): loopback is **default-on**; the opt-in
  alternative was considered and rejected.
- **Depends on**: none
- **Blocks**: 358 (a background server the driver cannot reach is useless), and every HTTP driver written under plans 353/354
- **Planned at**: commit `6541b8ab`, 2026-11-23

## Why this matters

Plans 353 and 354 make the model start the project's app and drive it. For any
project with an HTTP surface, "drive it" means `curl http://127.0.0.1:<port>/…`.
Today that curl is governed by the same switch that governs reaching the
internet:

```go
// pkg/safety/manager.go:392
req.AllowNetwork = req.AllowNetwork || runtimeCfg.NetworkEnabled
```

**Read the default before you read the risk.** `network_access` defaults to
**true** (`pkg/config/config.go:35-37`, materialized at `pkg/config/load.go:948`;
`shell` starts from `runtimeCfg.NetworkEnabled` at `pkg/tool/shell_tool.go:419`),
so an ordinary install already reaches localhost — and the whole internet. The
gap this plan closes is the operator who has turned network off
(`network_access: false`) or whose profile denies it: they get **no** local
access, so a driver that talks to a server the sandbox just started fails in a
way that looks like the app is broken rather than like the sandbox is closed.
There is no middle setting: `AllowNetwork` is one bool, and the two backends
treat it as all-or-nothing:

```go
// pkg/safety/backend_darwin.go:159-162
if req.AllowNetwork {
	b.WriteString("(allow network-outbound)\n(allow network-inbound)\n")
	b.WriteString(seatbeltNetworkSupportPolicy)
}
```

```go
// pkg/safety/backend_linux.go:87-89
if !req.AllowNetwork {
	args = append(args, "--unshare-net")
}
```

So the operator who turns network off to keep an agent off the internet also
loses the app's own localhost — the two are one switch, and the driver then fails
in a way that looks like the app is broken rather than like the sandbox is
closed. `networkDeniedKeywords` (`pkg/safety/process.go:236-245`) exists to
classify exactly this failure, and its own comment concedes the ambiguity: "a
host with no network produces them too."

This plan adds the missing third setting: **loopback only**. It is the smallest
change in this batch and the one that unblocks the most, which is why it runs
first.

## Current state

Anchors verified at `6541b8ab`:

| File | Line | Role |
|---|---|---|
| `pkg/safety/sandbox_config.go` | `:172` | `AllowNetwork bool` on the sandbox request struct, next to `IncludePlatformDefaults` and `Env` |
| `pkg/safety/sandbox_config.go` | `:265` | `NetworkEnabled bool` on the resolved runtime config |
| `pkg/safety/sandbox_config.go` | `:296` | `out.NetworkEnabled = out.NetworkEnabled \|\| src.SandboxWorkspaceWrite.EffectiveNetworkAccess()` |
| `pkg/safety/manager.go` | `:392` | the OR that folds config into the request |
| `pkg/safety/manager.go` | `:311` | `decision.NetworkBlocked = !req.AllowNetwork` |
| `pkg/safety/backend_darwin.go` | `:159-162` | seatbelt network emission |
| `pkg/safety/backend_darwin.go` | `:630-645` | `seatbeltNetworkSupportPolicy` — the mach-lookup/DNS grants a network client needs |
| `pkg/safety/backend_linux.go` | `:87-89` | bubblewrap `--unshare-net` |
| `pkg/safety/backend_windows.go` | — | **no `AllowNetwork` reference at all** (verified by grep): the restricted-token backend does not filter network, so Windows already permits loopback and needs no change |
| `pkg/config/config.go` | `:24-40` | `SandboxWorkspaceWrite` struct and `EffectiveNetworkAccess()` |
| `pkg/config/load.go` | `:948` | where `NetworkAccess` is resolved during load |
| `pkg/tool/shell_tool.go` | `:419`, `:422`, `:458`, `:492` | the four places `allowNetwork` is computed |
| `pkg/tool/shell_tool.go` | `:605` | where it is put on the `CommandRequest` |

Both backends emit their network policy from `req.AllowNetwork` alone, so the
new flag reaches them through the one request field the shell tool already
fills; nothing in `pkg/safety/shell.go`'s command classification is involved.

## Scope

**In scope**

- `pkg/safety/sandbox_config.go`, `pkg/safety/manager.go`,
  `pkg/safety/backend_darwin.go`, `pkg/safety/backend_linux.go`
- `pkg/config/config.go`, `pkg/config/load.go`
- `pkg/tool/shell_tool.go` (plumbing only — no behavior change to `shell`)
- the corresponding `_test.go` files

**Out of scope — do not touch**

- `pkg/safety/backend_windows.go`. It has no network filtering; adding one is a
  different plan and a different risk.
- `pkg/safety/shell.go`. Command classification is unrelated to what the
  sandbox permits at the socket level.
- The approval flow (`pkg/tool/inline_approval.go`,
  `pkg/tool/request_permissions.go`). Loopback is not an escalation and must
  not become one — see the design note below.
- Anything in `pkg/skill/`. Plans 353–356 own that tree.

## Design decisions the executor must not re-litigate

**1. Loopback is a default-on capability, not an escalation.** A command that
can reach `127.0.0.1` can reach a server the same sandboxed command started.
That is not a widening of what the user's machine is exposed to; it is the
minimum for "run the app and check it answered." Treat it like
`IncludePlatformDefaults`, not like `AllowNetwork`.

**2. `AllowNetwork` implies loopback.** Full network access already includes
localhost. The new flag only matters when `AllowNetwork` is false. Never emit
both.

**3. The resolved value is `NetworkEnabled || NetworkLoopback`.** One place
computes it (`manager.go`), so no backend has to reason about precedence.

## Git workflow

- Branch: `advisor/357-sandbox-loopback-network`
- Conventional Commits, lowercase imperative, optional `(scope)`, matching recent
  history (`fix(web): judge a subagent name by the scope it is written to`). Use
  e.g. `feat(safety): let a sandboxed command reach localhost without the internet`.
- Do NOT push and do NOT open a PR unless the operator instructed it.
- Dispatched into an isolated workspace, your branch lives in
  `.worktrees/<short-kebab-name>` under the project's main worktree; never push
  or merge into the user's branch.

## Steps

### Step 0: Probe before writing any policy (Linux only, but do it first)

bubblewrap's `--unshare-net` creates a fresh network namespace. Whether the
loopback interface inside it is **up** decides whether Linux needs any change at
all. I did not verify this and you must not assume it either way.

On a Linux host with bubblewrap installed:

```bash
bwrap --unshare-net --ro-bind / / --dev /dev --proc /proc \
  /bin/sh -c 'ip link show lo 2>/dev/null; \
    (python3 -m http.server 8931 --bind 127.0.0.1 &) ; sleep 1; \
    curl -sS -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8931/'
```

- Prints `200` → **loopback already works under `--unshare-net`**. Linux needs
  no code change: record the probe output in the completion report, make
  `AllowLoopback` a no-op on Linux, and add a test asserting
  `bubblewrapBaseArgs` still emits `--unshare-net` when loopback is requested.
  Then skip Step 4.
- Prints a connection error, or `lo` shows `state DOWN` → Linux needs the
  interface brought up inside the namespace. **STOP and report.** bubblewrap has
  no post-unshare hook, so the fix needs either `--cap-add NET_ADMIN` plus an
  `ip link set lo up` prefix on every command, or dropping `--unshare-net`
  entirely. Both change the security posture of every sandboxed command on
  Linux and neither belongs in this plan. Report the probe output and the two
  options; do not pick one.
- No Linux host available → implement Steps 1–3 and 5–7, mark Linux
  **UNVERIFIED** in the report, and make the Linux branch a no-op that keeps
  today's `--unshare-net` behavior byte-identical. That is safe: it changes
  nothing on Linux.

### Step 1: Config surface

Read `pkg/config/config.go:24-40` first. `SandboxWorkspaceWrite` carries
`NetworkAccess *bool` and `EffectiveNetworkAccess()` resolves it. Add a sibling:

```go
NetworkLoopback *bool `yaml:"network_loopback,omitempty" json:"network_loopback,omitempty"`
```

and an `EffectiveNetworkLoopback() bool` that mirrors
`EffectiveNetworkAccess()` exactly, **defaulting to true**. Match the existing
method's shape and comment style — read it before writing yours.

Then follow `pkg/config/load.go:948` and add the parallel resolution line so the
default is materialized the same way `NetworkAccess` is.

**Verify**: `go build ./pkg/config/...` → exit 0.

### Step 2: The request field

`pkg/safety/sandbox_config.go`, immediately after `AllowNetwork bool` (`:172`):

```go
// AllowLoopback permits traffic to and from 127.0.0.1/::1 only. It is the
// setting a command needs to reach a server another sandboxed command started,
// and it is not an escalation: nothing outside the host's loopback becomes
// reachable. Ignored when AllowNetwork is already true.
AllowLoopback bool
```

And on the resolved runtime config near `NetworkEnabled bool` (`:265`):

```go
NetworkLoopback bool
```

Then extend `:296` with the parallel fold from
`src.SandboxWorkspaceWrite.EffectiveNetworkLoopback()`.

**Verify**: `go build ./pkg/safety/...` → exit 0.

### Step 3: Resolve it in one place

`pkg/safety/manager.go`, next to `:392`:

```go
req.AllowNetwork = req.AllowNetwork || runtimeCfg.NetworkEnabled
req.AllowLoopback = req.AllowLoopback || runtimeCfg.NetworkLoopback
```

Do **not** change `:311` (`decision.NetworkBlocked = !req.AllowNetwork`). That
field feeds `networkDeniedKeywords` classification, and a loopback-only command
that fails to reach the *internet* should still be classified as
network-blocked. Leave it.

**Verify**: `go vet ./pkg/safety/...` → exit 0.

### Step 4: macOS seatbelt emission

`pkg/safety/backend_darwin.go:159-162` becomes three-way:

```go
if req.AllowNetwork {
	b.WriteString("(allow network-outbound)\n(allow network-inbound)\n")
	b.WriteString(seatbeltNetworkSupportPolicy)
} else if req.AllowLoopback {
	b.WriteString(seatbeltLoopbackPolicy)
}
```

with a new const beside `seatbeltNetworkSupportPolicy` (`:630`). The candidate
rules:

```
(allow network-outbound (remote ip "localhost:*"))
(allow network-inbound (local ip "localhost:*"))
(allow network-bind (local ip "localhost:*"))
```

**Prior art — these rules shipped once and were removed (review 2026-11-23).**
`git log --oneline -S 'AllowLoopback' -- pkg/` → `2a3f4b2f` ("bulk commit the
in-tree execution of plans 191-214") and `4d4dea18` (the initial import). The
profile at `2a3f4b2f^:pkg/safety/backend_darwin.go:145-149` was:

```
; allow local binding and loopback traffic
(allow network-bind (local ip "*:*"))
(allow network-inbound (local ip "localhost:*"))
(allow network-outbound (remote ip "localhost:*"))
```

So the syntax is not unknown: the **bind** rule that shipped is
`(local ip "*:*")`, not the `(local ip "localhost:*")` candidate above — a bind
filter names the address being bound, and a server may bind `0.0.0.0`. Start the
probe from the historical form, and treat the `localhost:*` bind candidate as the
fallback to try only if `*:*` fails. The removal came in a bulk sandbox redesign,
not with a recorded "no loopback" ruling; the owner's 2026-11-23 default-on
decision below is the current intent. Still run the probe: macOS version drift is
real, and nothing in today's tree proves the old rules still parse.

**These three lines are a hypothesis, not verified syntax.** Seatbelt's filter
grammar for network operations varies by macOS version, and `network-bind` may
not accept a `local ip` filter at all. Before wiring the const in, prove it:

```bash
cat > /tmp/lo-probe.sb <<'EOF'
(version 1)
(deny default)
(allow process-fork) (allow process-exec) (allow sysctl-read)
(allow file-read* (subpath "/"))
(allow network-outbound (remote ip "localhost:*"))
(allow network-inbound (local ip "localhost:*"))
(allow network-bind (local ip "localhost:*"))
EOF
sandbox-exec -f /tmp/lo-probe.sb /bin/sh -c \
  '(python3 -m http.server 8931 --bind 127.0.0.1 >/dev/null 2>&1 &) ; sleep 1; \
   curl -sS -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8931/'
```

- Prints `200` → the syntax works on this macOS. Ship it, and record the macOS
  version (`sw_vers -productVersion`) in the completion report.
- `sandbox-exec` rejects the profile → drop the offending operation one at a
  time and re-probe; a server needs `network-bind` and a client needs
  `network-outbound`, so find the smallest set that passes. Record which lines
  survived.
- Nothing makes loopback work while `(deny default)` is in effect → **STOP and
  report** the exact `sandbox-exec` error. Do not fall back to
  `(allow network-outbound)`, which silently grants the internet — that is the
  one failure mode this plan exists to avoid.

Note that `seatbeltNetworkSupportPolicy` (`:630-645`) grants mach-lookup for
`com.apple.networkd`, `SystemConfiguration.DNSConfiguration` and friends. A
pure-IP loopback connection needs none of them — there is no DNS for a literal
`127.0.0.1` — so do **not** append that policy to the loopback branch. If the
probe only passes with it, say so in the report; that means something is
resolving a name and the driver should use a literal IP instead.

**Verify**: the probe above prints `200`, and
`go test ./pkg/safety/ -run TestSeatbelt -count=1` passes.

### Step 5: Shell tool plumbing

`pkg/tool/shell_tool.go` computes `allowNetwork` at `:419` and folds in three
more sources at `:422`, `:458`, `:492`. Add a parallel `allowLoopback` that
starts from the resolved runtime config and is set at `:605` on the
`CommandRequest`.

Do not make `shell`'s own behavior depend on it beyond passing the value
through. No new input field on the shell tool: loopback is a session/config
property, not a per-call choice, and giving the model a per-call network switch
is an escalation surface this plan is not asking for.

**Verify**: `gofmt -l pkg/tool` → no output; `go build ./pkg/tool/...` → exit 0.

### Step 6: Tests

Add to `pkg/safety/backend_darwin_test.go`, following the existing seatbelt
policy tests in that file (read one before writing yours — match its assertion
style):

1. `AllowNetwork: true` → output contains `(allow network-outbound)` and the
   loopback const is **absent** (never both).
2. `AllowNetwork: false, AllowLoopback: true` → output contains the loopback
   rules and does **not** contain a bare `(allow network-outbound)`.
3. Both false → output contains neither.

Add to `pkg/safety/backend_linux_test.go`:

4. `AllowNetwork: false, AllowLoopback: true` → `--unshare-net` is still
   present (Step 0's finding, whichever branch it took).

Add to `pkg/safety/manager_test.go` or the existing config-resolution test:

5. `runtimeCfg.NetworkLoopback` true and `req.AllowLoopback` false → resolved
   request has it true (the OR at Step 3).
6. `NetworkEnabled` true → `AllowNetwork` true regardless of the loopback flag.

Add to `pkg/config` tests:

7. `EffectiveNetworkLoopback()` on a zero `SandboxWorkspaceWrite` returns
   **true** — the default is on, and that is the assertion that keeps it on.

**Verify**:
`CGO_ENABLED=1 go test -tags fts5 ./pkg/safety/... ./pkg/config/... ./pkg/tool/... -count=1` → PASS.

### Step 7: Full gates

1. `gofmt -l pkg/safety pkg/config pkg/tool` → no output
2. `go vet ./pkg/safety/... ./pkg/config/... ./pkg/tool/...` → exit 0
3. `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/... -count=1` → PASS
   (`pkg/safety` holds 19 production files against the cap of **34** —
   `maxProductionFilesPerPackage`, `pkg/architecture/cache_test.go:1532`. The
   doc comment above the test at `:1461-1462` still says "cap of 30"; the
   constant is what runs, and this plan adds no file)
4. `make test` → PASS. Bare `go test ./...` fails on pdfium on macOS; always use
   the `CGO_ENABLED=1 -tags fts5` form.

## Done criteria

- [ ] `grep -n "AllowLoopback" pkg/safety/sandbox_config.go pkg/safety/manager.go pkg/safety/backend_darwin.go pkg/tool/shell_tool.go` finds it in all four
- [ ] `grep -c "seatbeltLoopbackPolicy" pkg/safety/backend_darwin.go` returns 2 (const + use)
- [ ] The Step 4 probe prints `200`, and its output plus the macOS version are in the completion report
- [ ] The Step 0 Linux probe result is in the completion report, or Linux is marked UNVERIFIED with a byte-identical no-op branch
- [ ] `grep -n "AllowNetwork" pkg/safety/backend_windows.go` still returns nothing
- [ ] `grep -n "NetworkBlocked = " pkg/safety/manager.go` is unchanged (`:311`)
- [ ] All 7 new tests present and passing; `make test` green; `gofmt -l` clean; `go vet` exit 0
- [ ] The shell tool has **no** new input field: `git diff pkg/tool/shell_tool.go` shows only the plumbing lines, no change to the `jsonschema` struct

## STOP and report instead of improvising

- The Step 4 probe cannot be made to pass while `(deny default)` is in effect.
  Falling back to `(allow network-outbound)` grants the internet and defeats the
  plan.
- Step 0 shows Linux loopback is down and you are tempted to add
  `--cap-add NET_ADMIN`. That changes the capability set of every sandboxed
  Linux command.
- Adding the field pushes `pkg/safety` over the architecture cap (**34**
  production files, `cache_test.go:1532`; it is at 19). Report the count; do not
  split a file to dodge it.
- `EffectiveNetworkAccess()` turns out to be consumed somewhere that assumes
  network access is a single boolean (grep for its callers first). If a caller
  would read loopback as full network, report it rather than patching the caller.

## Maintenance notes

- **This flag will be read by plan 358's process tool.** A long-lived server
  started in the background needs the same loopback grant as the curl that
  reaches it, and both come through `manager.go`'s single resolution point. Keep
  it one place.
- **Default-on is settled — owner decision 2026-11-23: accepted.** An ordinary
  install already has `network_access: true` (`pkg/config/config.go:35-37`), so
  today it reaches localhost and the internet alike. After this plan, the install
  that set `network_access: false` still reaches localhost; that widening is the
  point, not an accident. Do not make it opt-in, do not add a per-command switch,
  and keep test 7 asserting the zero value resolves to `true`.
- **Docs**: if `docs/` or the README documents `sandbox_workspace_write`
  network settings, add `network_loopback` there. Note what a `network_access`
  grep actually finds at `6541b8ab`: only the owner-frozen, gitignored prototype
  HTML under `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/` — **do not edit those**.
  The model-facing description of the setting is not in docs at all; it is
  `SandboxNetworkInWords` (next bullet).
- **The model-facing sentence stays as it is, deliberately.**
  `SandboxNetworkInWords` (`pkg/safety/process.go:545-553`) answers "commands
  cannot reach the internet; approval is required to." when
  `EffectiveNetworkAccess()` is false, and reaches the model through
  `process.go:450` and `permission_types.go:668`. After this plan that sentence
  is still true — loopback is not the internet — so this plan does **not** change
  it. It is pinned byte-for-byte by
  `TestSandboxNetworkInWordsHasTwoSentences`
  (`pkg/safety/permission_rules_test.go:540-556`); adding a loopback clause would
  break that test and is a separate product decision about what the model is
  told. Say in your report that you left it unchanged and why.
- **Do not add per-command network choices to the shell tool later** without
  revisiting the approval flow. The value of this design is that loopback never
  appears in an approval prompt.
