# Plan 241: a batch task never receives a write path into the operator's home

> **Executor instructions**: Follow the steps in order and confirm each
> verification. If a STOP condition occurs, stop and report — do not improvise.
> Do **not** commit, push or merge: leave the change in the working tree and
> report it. When done, update this plan's row in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 69ab0706..HEAD -- pkg/tool/shell_tool.go pkg/tool/shell_tool_test.go`
> Compare the "Current state" excerpts with the live code; content mismatch is a
> STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW — one parameter and one early return. The contained case (the one
  plans/208 cares about) keeps its pointers.
- **Depends on**: none
- **Category**: security
- **Planned at**: commit `69ab0706`, 2026-11-05
- **Finding**: branch audit 2026-11-05 (`origin/dev..HEAD`), finding 4
  (`introduced` by plan 235, commit `5329bb66`)

## Why this matters

Plan 235 gave a batch task access to the operator's real package-manager
configuration by pointing `GIT_CONFIG_GLOBAL`, `NPM_CONFIG_USERCONFIG`,
`PIP_CONFIG_FILE` and `GOENV` at the operator's home. The intent was read access
to a mirror (plans/208); when the task is contained, that is what it is, because
the sandbox makes those files read-only.

The pointers are appended unconditionally, though — `batchSandboxed` is computed
right beside them and never consulted. A batch project whose sandbox switch is
off, or any machine without a working sandbox, runs the command **on the host
with full access**, where the same variables are a *write* path:

- `git config --global …`, `npm config set …`, `pip config set …`, `go env -w …`
  write into the operator's real configuration files;
- `npm config get //registry/:_authToken` prints a token out of `~/.npmrc` —
  a credential the environment guard strips from variables (`looksSecretKey`)
  but cannot strip from a file it points at.

That makes an unattended task able to mutate and read the operator's own
configuration, with no approval (a batch turn asks nobody, by design).

## Current state

`pkg/tool/shell_tool.go:319` — containment is decided here:

```go
	batchSandboxed := batch && safety.SandboxAvailable() && (rt == nil || rt.BatchSandbox)
```

`pkg/tool/shell_tool.go:551-575` — the batch block: HOME is moved into the
workspace, then the pointers are appended regardless of containment:

```go
		shellEnv = append(shellEnv, "HOME="+homeDir)
		// … What it costs is the
		// user-level configuration the tools read out of the real home …
		userHome, _ := os.UserHomeDir()
		userConfigDir, _ := os.UserConfigDir()
		shellEnv = append(shellEnv, batchRealHomeConfigEnv(userHome, userConfigDir, boundary)...)
		// A contained task's package managers write their caches into its own
		// workspace as well …
		if batchSandboxed {
			shellEnv = append(shellEnv, batchCacheEnv(boundary.Workspace)...)
		}
```

`pkg/tool/shell_tool.go:817-843` — the helper and its only *boundary* gate
(`DeniesRead`; a path must also exist on disk — `os.Stat` at `:838-839` — and
come from a non-empty home):

```go
func batchRealHomeConfigEnv(userHome, userConfigDir string, boundary BatchBoundary) []string {
	paths := map[string]string{}
	…
	for _, key := range keys {
		…
		if boundary.DeniesRead(path) {
			continue
		}
		env = append(env, key+"="+path)
	}
	return env
}
```

`pkg/tool/batch_boundary.go:73-83` — `DeniesRead` refuses only the Forebrain home
and the project root; the operator's own home is readable **and** writable when
the command is not contained.

`pkg/safety/permission_types.go:908-917` —
`constrainRequestToFilesystemProfile` adds the work dir to
`AdditionalWritablePaths` only for `ProfileWorkspaceWrite`; with
`batchSandboxConfig`'s `SandboxModeDangerFullAccess` (`pkg/tool/shell_tool.go:767-778`)
nothing is contained.

Tests: `pkg/tool/shell_tool_test.go:1710` `TestBatchRealHomeConfigEnv` (unit,
calls the helper) and `:2148` `TestBatchShellKeepsTheUsersConfigurationReachable`
(end-to-end, expects the pointers — it currently passes only because the pointers
are unconditional).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./...` | exit 0 |
| Focused | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'Batch' -count=1` | `ok` |
| Neighbours | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run -count=1` | `ok` |

## Scope

**In scope**:
- `pkg/tool/shell_tool.go` (the helper's signature and the call site)
- `pkg/tool/shell_tool_test.go`

**Out of scope**:
- `pkg/tool/batch_boundary.go` — adding a write predicate to `BatchBoundary` is
  the right long-term shape, but nothing else needs it yet; this plan gates on the
  containment decision that already exists.
- `batchCacheEnv` and the `HOME` rewrite — both are plan 216/235 decisions and
  both are correct.
- `pkg/home/envguard.go`'s `looksSecretKey` filtering.

## Steps

### Step 1: make containment the condition

Change the helper's signature and add the early return:

```go
// batchRealHomeConfigEnv keeps the user-level configuration of the package
// managers reachable from a *contained* batch task, whose HOME lives in its own
// workspace.
//
// Only a contained task gets these: the sandbox is what turns each pointer into
// a read of the operator's file. An uncontained task runs on the host with full
// access, where the same four variables are a write path — `git config --global`,
// `npm config set`, `go env -w` land in the operator's real home, and
// `npm config get` can print a token out of the file it names. A batch turn asks
// nobody anything, so it must not have that.
func batchRealHomeConfigEnv(userHome, userConfigDir string, boundary BatchBoundary, contained bool) []string {
	if !contained {
		return nil
	}
	… // unchanged
```

Call site (`shell_tool.go:561`):

```go
		shellEnv = append(shellEnv, batchRealHomeConfigEnv(userHome, userConfigDir, boundary, batchSandboxed)...)
```

**Verify**: `go build ./...` → exit 0

### Step 2: update the unit test and pin the uncontained answer

`TestBatchRealHomeConfigEnv` (`shell_tool_test.go:1710`): pass `true` at every
existing call so the current expectations hold, then add:

```go
	if got := batchRealHomeConfigEnv(userHome, configDir, boundary, false); len(got) != 0 {
		t.Fatalf("an uncontained task was handed configuration pointers: %v", got)
	}
```

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run TestBatchRealHomeConfigEnv -count=1` → `ok`

### Step 3: keep the end-to-end test honest about its machine

`TestBatchShellKeepsTheUsersConfigurationReachable` (`shell_tool_test.go:2148`)
asserts the pointers exist; after step 1 that requires a real sandbox. Add at the
top, after the `testing.Short()` skip:

```go
	if !safety.SandboxAvailable() {
		t.Skip("the pointers only exist for a contained task; this machine has no sandbox")
	}
```

Do not weaken its assertions. What it pins today (`shell_tool_test.go:2208-2239`):
`HOME` is the task's workspace home, `GIT_CONFIG_GLOBAL` and
`NPM_CONFIG_USERCONFIG` resolve to the operator's real files, and `cat` of the
gitconfig works inside the sandbox; `PIP_CONFIG_FILE`/`GOENV` are asserted
**unset** when those files do not exist on the machine — that per-machine shape
is the existing behaviour, not something to strengthen into an always-four
assertion (the helper's `os.Stat` gate already drops pointers whose file is
absent).

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run TestBatchShellKeepsTheUsersConfigurationReachable -count=1` → `ok` or `SKIP` (report which)

### Step 4: prove the uncontained case end to end

Add `TestBatchShellWithoutASandboxGetsNoConfigPointers`, modelled on
`TestBatchShellRunsWithoutASandboxAndAsksNobody` (`:1808`), which already forces
the uncontained path with `safety.SetSandboxAvailableForTest(false)`:

- create `userHome` with a `.gitconfig` and an `.npmrc`, and
  `t.Setenv("HOME", userHome)` / `t.Setenv("XDG_CONFIG_HOME", …)`;
- run `printf '%s|%s|%s|%s' "${GIT_CONFIG_GLOBAL:-unset}" "${NPM_CONFIG_USERCONFIG:-unset}" "${PIP_CONFIG_FILE:-unset}" "${GOENV:-unset}"`;
- assert every field is `unset`, and that `$HOME` is still the task's own
  workspace home (the unchanged plan-235 behaviour).

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestBatchShellWithoutASandbox' -count=1` → `ok`

## Test plan

- Changed: `TestBatchRealHomeConfigEnv` — new `contained=false` case.
- Changed: `TestBatchShellKeepsTheUsersConfigurationReachable` — skips without a
  sandbox; assertions unchanged.
- New: `TestBatchShellWithoutASandboxGetsNoConfigPointers` — the regression
  (fails before step 1: it prints the real paths).
- Keep green: every other `TestBatch*` case, especially
  `TestBatchShellIsConfinedToTheTaskWorkspace` and the two full-access cases.

## Done criteria

ALL must hold:

- [ ] `go build ./...` and `go vet ./...` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run -count=1` passes
- [ ] `grep -n 'batchRealHomeConfigEnv(userHome, userConfigDir, boundary, batchSandboxed)' pkg/tool/shell_tool.go` matches
- [ ] The new uncontained test fails when the early return is removed (negative control observed)
- [ ] No file outside the in-scope list is modified (`git status --porcelain`)
- [ ] `plans/README.md` row for 241 updated

## STOP conditions

- A contained batch command turns out to *need* one of these pointers to be
  writable (e.g. `go env -w` is the documented way to configure a Go proxy for a
  task): report which tool and why. The answer then is a workspace-local copy of
  the file, not a writable real path — a different change.
- `safety.SetSandboxAvailableForTest` is unavailable or not package-visible from
  `pkg/tool`'s tests: report it rather than adding a test-only export.
- The skip in step 3 makes the end-to-end test never run on this machine: report
  which case is now unverified so the owner can run it on a sandboxed host
  (`FOREBRAIN_…` acceptance note), instead of deleting assertions.

## Maintenance notes

- The rule: **a batch task gets paths it can read, or none.** Anything that hands
  a batch process a path must state which containment makes it safe.
- A reviewer should check the contained case is byte-identical to today (same
  four keys, same `os.Stat` and `DeniesRead` gates, same order).
- Deliberately deferred: a `BatchBoundary.AllowsWrite(path)` predicate, which is
  what a future pointer-at-a-copy fix would want. Deferring it keeps this plan at
  one condition.
