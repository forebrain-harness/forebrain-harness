# Plan 001: Restrict gateway.log to owner-only permissions

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `docs/plan/LOGGING_HARDENING/README.md` — unless a reviewer dispatched you
> and told you they maintain the index.
>
> **Drift check (run first)**:
> `git diff --stat eafaf46..HEAD -- cmd/forebrain/gateway_log.go pkg/telemetry/logfile.go`
> If either file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: security
- **Planned at**: commit `eafaf46`, 2026-10-08

## Why this matters

`forebrain gateway start` now mirrors its console log to `<home>/logs/gateway.log`
(this file was added in the uncommitted gateway-console-log work). That file is
created **world-readable (0644)** while every other Forebrain Harness error log
is **0600**. Because the log keeps *all* levels — including `ERROR` records and
one INFO line per HTTP request carrying the client IP and request path — any
local user on a shared machine (dev box, jump host, k8s node where the home is
mounted) can read the operator's traffic metadata and error details. The fix is
one option field; after it lands, all Forebrain Harness log files share one
permission policy.

## Current state

Files involved:

- `cmd/forebrain/gateway_log.go` — installs the gateway console log and mirrors
  it into `<home>/logs/gateway.log`. **The file with the defect.**
- `pkg/telemetry/logfile.go` — `Options`/`Open`, the shared rotating-log
  primitive every Forebrain Harness log file goes through. Reference only.

The defect, verbatim (`cmd/forebrain/gateway_log.go`, function
`installGatewayConsoleLog`):

```go
	file, err := telemetry.Open(filepath.Join(root, home.LogsDir, gatewayLogFile), telemetry.Options{
		ExistingParentOnly: true,
		SyncWrites:         true,
	})
```

`Perm` is not set, and `Options.normalized()` fills the zero value with `0o644`
(`pkg/telemetry/logfile.go`):

```go
func (o Options) normalized() Options {
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultFileMaxBytes
	}
	if o.Backups <= 0 {
		o.Backups = DefaultBackups
	}
	if o.Perm == 0 {
		o.Perm = 0o644
	}
	return o
}
```

The two permission policies that already exist in this repo, for contrast:

| Location | Call | Mode |
|---|---|---|
| `cmd/forebrain/gateway_log.go` | `Options{ExistingParentOnly: true, SyncWrites: true}` | **0644 (defect)** |
| `pkg/telemetry/debuglog.go` | `Options{ExistingParentOnly: true, SyncWrites: true}` | 0644 (debug.log — see Scope note) |
| `pkg/telemetry/slog_handler.go` | `os.OpenFile(path, O_APPEND|O_CREATE|O_WRONLY, 0o600)` | 0600 |
| `pkg/telemetry/paniclog.go` | `os.OpenFile(path, O_APPEND|O_CREATE|O_WRONLY, 0o600)` | 0600 |
| `pkg/telemetry/error_write.go` | `os.OpenFile(path, O_APPEND|O_CREATE|O_WRONLY, 0o600)` | 0600 |

Observed on a real home (`ls -l ~/.forebrain/logs`):

```
-rw-r--r--@ 1 doudou  staff    241610 error.log      <- 0600 policy files show as ---
-rw-r--r--  1 doudou  staff       891 gateway.log    <- world-readable
```

**Repo conventions that apply:**

- File permissions on log files are written as literal `0o600` octal in Go source.
- Test naming/structure: table-free, one behaviour per `Test…` function, using
  `t.TempDir()` for the home — see `pkg/telemetry/logfile_test.go:18-42`
  (`TestOpenCreatesParentAndRotatesDuringLifetime`) as the structural exemplar.

## Commands you will need

| Purpose   | Command | Expected on success |
|-----------|---------|---------------------|
| Build     | `go build ./...` | exit 0 |
| Vet       | `go vet ./...` | exit 0, no diagnostics |
| Format    | `gofmt -l cmd pkg` | only `pkg/tui/render.go` (pre-existing at `eafaf46`) |
| Focused tests | `CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain/... -count=1 -run 'GatewayConsoleLog'` | `ok` |
| Package tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry/... ./cmd/forebrain/... -count=1` | `ok` |

Note for this repo: CGO is mandatory (`CONVENTIONS` in `FOREBRAIN.md`); plain
`go test` without `CGO_ENABLED=1 -tags fts5` is not a valid result here.

## Scope

**In scope** (the only file you should modify):

- `cmd/forebrain/gateway_log.go`
- `cmd/forebrain/console_log_test.go` (add one assertion to the existing
  `TestInstallGatewayConsoleLogMirrorsConsoleFormat`)

**Out of scope** (do NOT touch, even though they look related):

- `pkg/telemetry/logfile.go`’s `Perm` normalization to `0o644`. Changing the
  default would silently re-permission `debug.log` and any future caller; this
  plan pins the **caller**, keeping the diff and the blast radius minimal.
- `pkg/telemetry/debuglog.go` (`debug.log` is also 0644 today). That is
  pre-existing behaviour, not introduced by this work. It is deliberately left
  alone here; if the owner wants one uniform policy for *all* log files that is
  a separate plan (see "Maintenance notes").
- `pkg/telemetry/levels.go` / `OpenLevelLogger` (`info.log`, `debug.log`).
- Any change to log *content*, format, rotation parameters, or the
  `ExistingParentOnly` behaviour.

## Git workflow

This repo lands changes only through pull requests against `main`
(`FOREBRAIN.md` → "Development workflow"); `main` is protected.

- Branch: `fix/gateway-log-permissions`
- One commit. Message style is Conventional Commits with a signed-off body
  (`git commit -s`), e.g.
  `fix(gateway): keep the gateway log file owner-readable only`
  with a `Why:` / `What:` / `Verification:` body.
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Pin the permission at the gateway log call site

In `cmd/forebrain/gateway_log.go`, add `Perm` to the options:

```go
	file, err := telemetry.Open(filepath.Join(root, home.LogsDir, gatewayLogFile), telemetry.Options{
		Perm:               0o600,
		ExistingParentOnly: true,
		SyncWrites:         true,
	})
```

Match the surrounding style — the options struct literal is already
multi-line and field-aligned; run `gofmt` after editing.

**Verify**: `gofmt -l cmd/forebrain/gateway_log.go` → prints nothing.

### Step 2: Pin the permission in a test so it cannot regress

In `cmd/forebrain/console_log_test.go`, inside the existing
`TestInstallGatewayConsoleLogMirrorsConsoleFormat`, after the log file is read,
add an assertion on its mode. Use `os.Stat`:

```go
	info, err := os.Stat(filepath.Join(root, "logs", gatewayLogFile))
	if err != nil {
		t.Fatalf("stat %s: %v", gatewayLogFile, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("gateway.log mode = %o, want 0600", got)
	}
```

Do not restructure the existing test; append the assertions next to its existing
`os.ReadFile` block so the test keeps flowing "write a record → close → read the
file → inspect it".

**Verify**:
`CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain/... -count=1 -run 'InstallGatewayConsoleLogMirrorsConsoleFormat' -v`
→ `--- PASS`.

### Step 3: Full gates

**Verify**:
```
go build ./... && go vet ./... && gofmt -l cmd pkg
CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry/... ./cmd/forebrain/... -count=1
```
→ build/vet exit 0; `gofmt` lists only `pkg/tui/render.go`;
both packages report `ok`.

## Test plan

- Extend `TestInstallGatewayConsoleLogMirrorsConsoleFormat`
  (`cmd/forebrain/console_log_test.go`) with the mode assertion from Step 2.
  Structural pattern: the same test already builds a temp home, installs the
  logger, writes a record and reads the file back.
- Cases covered: the file exists after install; its mode is exactly `0600`.
- Verification:
  `CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain/... -run 'InstallGatewayConsoleLog' -count=1`
  → all pass.

**Note on the negative test**: do **not** add a test asserting "gateway.log is
not 0644". Assert the positive contract (`0600`) once; that is the repo's rule
for deletions/replacements — never write assertions about a value that must no
longer appear.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `grep -n "Perm:" cmd/forebrain/gateway_log.go` → exactly one match with `0o600`
- [ ] `go build ./...` exits 0
- [ ] `go vet ./...` exits 0
- [ ] `gofmt -l cmd pkg` prints only `pkg/tui/render.go`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain/... -count=1` → `ok`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry/... -count=1` → `ok`
- [ ] `git status --short` shows no modified file outside the in-scope list
- [ ] `docs/plan/LOGGING_HARDENING/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- `cmd/forebrain/gateway_log.go` does not match the "Current state" excerpt (the
  codebase has drifted — this plan was written against uncommitted work, so a
  `git checkout` or rebase may have moved it).
- The `telemetry.Options` struct no longer has a `Perm` field, or
  `Options.normalized()` no longer defaults to `0o644` (the defect's premise).
- The mode assertion fails even though `Perm: 0o600` is set — that means a
  **pre-existing file** with 0644 is being reused rather than created, and the
  fix needs a `chmod`/migration decision that this plan deliberately does not
  make. Report it; do not add a chmod on your own.
- You find yourself editing `pkg/telemetry/logfile.go`'s default to make the
  test pass. That is out of scope; stop and report.

## Maintenance notes

- **Pre-existing files keep their old mode.** `os.OpenFile` does not chmod an
  existing file, so an operator who already ran the gateway ends up with a 0644
  `gateway.log` until it is removed. A reviewer should decide whether that
  warrants a one-time `chmod` on open (not in this plan). Existing homes can be
  fixed with `chmod 600 <home>/logs/gateway.log`.
- **Uniform policy is still open.** `debug.log`, `info.log` and `gateway.log`
  reach `Open` with no `Perm` (0644) while `error.log` writers use 0600. If the
  owner later wants one rule for every log file, the minimal change is to make
  `Options.normalized()` default to `0o600` **and** audit every `Open` caller —
  that is why it is deliberately not done here.
- **Reviewer scrutiny**: confirm no other file in the diff changed; the whole
  change is one option field plus one assertion.
