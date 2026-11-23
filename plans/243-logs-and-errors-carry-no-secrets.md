# Plan 243: a failure never writes a credential into the log or the error text

> **Executor instructions**: Follow the steps in order and confirm each
> verification. If a STOP condition occurs, stop and report — do not improvise.
> Do **not** commit, push or merge: leave the change in the working tree and
> report it. When done, update this plan's row in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 69ab0706..HEAD -- pkg/tool/shell_output.go pkg/state/db.go pkg/state/db_test.go pkg/process/config_db.go pkg/process/config_db_test.go`
> Compare the "Current state" excerpts with the live code; content mismatch is a
> STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW — two error messages and one log field; no control flow changes.
- **Depends on**: none
- **Category**: security
- **Planned at**: commit `69ab0706`, 2026-11-05
- **Findings**: branch audit 2026-11-05 (`origin/dev..HEAD`), findings 6 and 7
  (both `introduced`: finding 6 by the new history-save log, finding 7 by plan 236
  making a previously-swallowed open error visible)

## Why this matters

Two new failure paths carry a secret out of the process:

1. `pkg/tool/shell_output.go:615` logs the **raw command** at ERROR when the
   output-history save fails. Commands routinely carry credentials inline
   (`curl -H "Authorization: …"`, `mysql -p…`, `export TOKEN=…`). The same
   file already redacts the *output* before persisting it, and the default slog
   handler writes to a tee that lands on disk (`cmd/forebrain/root.go:117`), so a
   bug report ships the command with it.
2. `pkg/state/db.go:78-80` wraps a `url.Parse` failure with `%w`, and
   `*url.Error`'s message quotes the URL it was given — which is `database.url`,
   the one place the state database password lives. Plan 236 stopped swallowing
   that error, so it now travels: `ComposedStartupConfig` returns it into the
   TUI's first-setup gate (`pkg/tui/setup.go:1621`, `startupJudgeConfig` — a
   failure there fails startup), and `wizardErr` (`:229`, called from `:125`)
   writes `err.Error()` into `<home>/logs/info.log` — on top of the slog tee
   below. Every one of those surfaces would quote the password. A password with
   an unescaped `%` or a space is a common typo, so this is not an exotic path.

Neither value may be reproduced anywhere in this plan, its tests or the index —
reference the location and the credential type only.

## Current state

`pkg/tool/shell_output.go:606-618` — the log line, inside the function that
already redacts:

```go
		// The model still gets the compressed output; what it loses is the
		// retrieval id, so the full original is unreachable for this command.
		…
		if err != nil {
			slog.Error("shell output history not saved; the output has no retrieval id",
				"session_id", sessionID, "command", command, "err", err)
		}
```

`pkg/tool/state.go:3043-3056` — the redactor already in this package:

```go
func Redact(s string) string { … for _, rule := range secretRules { … } }
func RedactEnabled() bool { return !envTruthy(RedactionDisabledEnv) }
```

`pkg/tool/shell_output.go:568-572` — the precedent, three functions above:

```go
	if RedactEnabled() {
		res.Output = Redact(res.Output)
	}
```

`pkg/state/db.go:70-83` — the parse and its `%w`:

```go
func OpenFromConfig(ctx context.Context, home string, databaseURL string, opts *OpenOptions) (*sql.DB, error) {
	target := strings.TrimSpace(databaseURL)
	if target == "" {
		return Open(ctx, StateDBPath(home), opts)
	}
	u, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("parse database.url: %w", err)
	}
```

`pkg/process/config_db.go:472-482` — the caller whose comment states the policy
the `%w` breaks:

```go
		// Name the file and the engine that failed, never the URL: it may
		// carry the password. …
		return appcfg.Root{}, false, fmt.Errorf("open the %s state database named by %s: %w", engine, cfgPath, err)
```

`pkg/process/open.go:352-355` — the same wrapping surface
(`fmt.Errorf("open database: %w", err)`).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./...` | exit 0 |
| Focused | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/state ./pkg/process -run 'ShellOutput|Database|Startup|Config' -count=1` | `ok` |
| Neighbours | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/state ./pkg/process ./pkg/tui -count=1` | `ok` |

The focused pattern uses plain `|`: in a Go regexp (which is what `-run` takes)
`\|` is a *literal* pipe and matches no test name — the command would run zero
tests and still exit 0.

## Scope

**In scope**:
- `pkg/tool/shell_output.go`
- `pkg/state/db.go`
- `pkg/state/db_test.go`
- `pkg/process/config_db_test.go`

**Out of scope**:
- `pkg/process/config_db.go` / `open.go` — their `%w` becomes safe once the
  underlying error no longer quotes the URL; changing them too would be two
  fixes for one leak.
- `pkg/tool/state.go`'s `secretRules` — do not add patterns; the existing table is
  the policy.
- `state.testDatabaseURLFor`'s `fmt.Errorf("parse %s: %w", testDatabaseURLVar, err)`
  (`pkg/state/db.go:261`): test-only, environment-supplied, and out of the operator
  path. Mention it in the report if you disagree.
- The persisted `state.Entry.Command` (unredacted by design in this file's
  history): a separate change with a data-migration angle.

## Steps

### Step 1: redact the command in the history-save log

In `shell_output.go`, replace the `"command", command` field with a value that is
safe by construction:

```go
			logged := command
			if RedactEnabled() {
				logged = Redact(logged)
			}
			slog.Error("shell output history not saved; the output has no retrieval id",
				"session_id", sessionID, "command", logged, "command_bytes", len(command), "err", err)
```

Keeping `command_bytes` preserves the diagnostic value (a truncated/oversized
command is visible) without the text.

**Verify**: `go build ./...` → exit 0

### Step 2: stop quoting the URL in the parse error

In `pkg/state/db.go`, wrap only the reason:

```go
	u, err := url.Parse(target)
	if err != nil {
		// A *url.Error's message quotes the URL it was handed, and this URL is
		// the one place the state database's password lives. Surface the reason
		// alone (plan 236 made this error visible, so it must be safe to show).
		reason := err
		var uerr *url.Error
		if errors.As(err, &uerr) && uerr.Err != nil {
			reason = uerr.Err
		}
		return nil, fmt.Errorf("parse database.url: %w", reason)
	}
```

Add `errors` to the imports if absent.

**Verify**: `go build ./...` → exit 0

### Step 3: prove it, in both packages

`pkg/state/db_test.go`:

```go
func TestOpenFromConfigNeverQuotesTheURL(t *testing.T) {
	// A password with a character that is invalid in a URL: parse fails, and
	// the message must not repeat what it was given.
	_, err := OpenFromConfig(context.Background(), "", "mysql://user:pw%zz@127.0.0.1:3306/fbt", nil)
	if err == nil {
		t.Fatal("a malformed database.url must be an error")
	}
	if strings.Contains(err.Error(), "pw%zz") {
		t.Fatalf("the error quoted the credential: %v", err)
	}
}
```

`pkg/process/config_db_test.go`: write a `forebrain.yaml` whose `database.url` is
the same malformed shape, call `ComposedStartupConfig`, and assert the same two
things (error non-nil, credential absent from `err.Error()`). Name the test
`TestComposedStartupConfigNeverQuotesTheURL` so the focused command below runs
it; reuse the file's YAML-writing helper `composeEnvForTest(t, yaml)`
(`config_db_test.go:24`), which writes the yaml to `<home>/forebrain.yaml`.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/process -run 'NeverQuotes' -count=1` → `ok`

## Test plan

- New: `TestOpenFromConfigNeverQuotesTheURL` (state) — fails before step 2 with
  the credential visible.
- New: the `ComposedStartupConfig` case (process) — the operator-visible path.
- Keep green: `pkg/tool/shell_output_test.go`'s history tests and every
  `pkg/process` startup-database test (`TestComposedStartupConfig*`).
- The redaction change has no unit test of its own (the log line is not
  observable); state that in the report and cite the code review instead of
  inventing a log-capture harness.

## Done criteria

ALL must hold:

- [ ] `go build ./...` and `go vet ./...` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/state ./pkg/process ./pkg/tui -count=1` passes
- [ ] `grep -n '"command", logged' pkg/tool/shell_output.go` matches and
      `grep -n '"command", command' pkg/tool/shell_output.go` returns nothing
- [ ] Both new tests fail when their fix is reverted (negative control observed)
- [ ] No credential value appears in any file you changed (`git diff` read through)
- [ ] Only in-scope files modified (`git status --porcelain`)
- [ ] `plans/README.md` row for 243 updated

## STOP conditions

- Stripping the URL loses the only information that identifies *which* database
  misconfigured (e.g. a config with two databases): report it. The file name the
  caller already prints (`config_db.go:482`) is the intended identifier.
- `url.Parse` accepts the malformed string on this Go version (so no error is
  produced): report the probe result and use a different malformed fixture rather
  than weakening the assertion.
- Any test asserts the old error text verbatim: report the test — do not edit it
  to match a leaky message.

## Maintenance notes

- The rule: **an error or log line about a connection may name the engine, the
  file and the reason — never the URL.** Anything wrapping these errors with `%w`
  is safe only while that holds.
- If a credential was exposed through either path before this lands, rotate it:
  treat a log file as public. Do not write the value into the plan or the report.
- A reviewer should check the redaction runs only when `RedactEnabled()` (the
  escape hatch is a documented operator choice), and that `command_bytes` is
  logged either way.
