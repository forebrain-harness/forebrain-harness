# Plan 223: the dangerous-git guard sees through the wrappers a shell really uses

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on. If a
> "STOP condition" occurs, stop and report — do not improvise. When done, update
> this plan's row in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 7d505aa3..HEAD -- pkg/safety/git_sweep.go pkg/safety/shell.go pkg/safety/git_sweep_test.go`
> Content mismatch against the excerpts is a STOP condition; line movement is not.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW (more commands get an approval; nothing gets less)
- **Depends on**: none
- **Category**: security
- **Planned at**: commit `7d505aa3`, 2026-11-04
- **Finding**: branch audit 2026-11-04, finding 2 (`introduced` by plan 198)

## Why this matters

When another agent works in the same git work tree, the shell tool must ask the
user before a command that would discard that agent's uncommitted work
(`peer_work_tree`), **even in YOLO / full access** — design §6, implemented in
`pkg/safety/git_sweep.go` and consumed at `pkg/tool/shell_tool.go:216`.

`GitSweepsWorkTree` unwraps only `git`, `sudo`, `env` and `<shell> -c`. A command
written the way this repo itself writes shell commands — `/bin/sh -lc …`, see
`pkg/safety/backend_unix.go:21` — plus the common `VAR=x git …`, `command git …`,
`exec git …`, `timeout 30 git …`, `nohup git …`, `sudo -u me git …` forms all fall
through to "cannot sweep": no approval, no warning, the other agent's work is
gone. The guard is the only thing standing between two parallel agents and a
`git reset --hard`.

## Current state

`pkg/safety/git_sweep.go:56-104` — `GitSweepsWorkTree` → `gitSweepFromCommands`
over `gitSweepWords(command)`, then `gitSweepFromWords` unwraps commands:

```go
func gitSweepFromWords(command []string, depth int) (string, bool) {
	if depth > gitSweepMaxWrapperDeep || len(command) == 0 {
		return "", false
	}
	switch strings.ToLower(filepath.Base(command[0])) {
	case "git":
		return gitSweepFromGitArgs(command[1:])
	case "sudo":
		return gitSweepFromWords(command[1:], depth+1)
	case "env":
		i := 1
		for i < len(command) {
			arg := command[i]
			if arg == "--" {
				i++
				break
			}
			_, isAssignment := parseEnvAssignment(arg)
			if arg == "-i" || arg == "--ignore-environment" || isAssignment {
				i++
				continue
			}
			break
		}
		return gitSweepFromWords(command[i:], depth+1)
	case "sh", "bash", "zsh", "dash", "ksh":
		if script, ok := shellDashCScript(command); ok {
			return gitSweepFromSource(script, depth+1)
		}
	}
	return "", false
}
```

`pkg/safety/shell.go:581-595` — `shellDashCScript` accepts only the exact flag
`-c`; every other `-…` word is skipped as an option, so `-lc`, `-ec`, `-xec`
return false:

```go
func shellDashCScript(command []string) (string, bool) {
	for i := 1; i < len(command); i++ {
		arg := command[i]
		if arg == "-c" {
			if i+1 < len(command) {
				return command[i+1], true
			}
			return "", false
		}
		if !strings.HasPrefix(arg, "-") {
			return "", false
		}
	}
	return "", false
}
```

`pkg/safety/git_sweep.go:216-230` — `checkout` is flagged only for `--`, `.` or
`-f`; `git checkout HEAD path/file.go` (a path checkout that overwrites local
edits) is not:

```go
	case "checkout":
		if argIs(args, "--") || argIs(args, ".") {
			return GitSweepCheckoutPaths, true
		}
		if shortFlagWith(args, 'f') || argIs(args, "--force") {
			return GitSweepCheckoutForce, true
		}
```

Helpers already available: `argIs` (`pkg/safety/git_sweep.go:259`),
`shortFlagWith` (`:271`), `firstNonOptionArg` (`:286`), and
`parseEnvAssignment` (`pkg/safety/shell.go:1246`).

### Measured today (2026-11-04, `GitSweepsWorkTree` on each string)

| command | flagged? |
|---|---|
| `git reset --hard` | yes |
| `bash -c 'git reset --hard'` | yes |
| `bash -lc 'git reset --hard'` | **no** |
| `/bin/sh -lc 'git stash -u'` | **no** |
| `GIT_PAGER=cat git stash -u` | **no** |
| `FOO=1 git clean -fdx` | **no** |
| `command git reset --hard` | **no** |
| `exec git reset --hard` | **no** |
| `timeout 30 git checkout .` | **no** |
| `nohup git clean -fd` | **no** |
| `sudo -u me git reset --hard` | **no** |
| `env -u X git reset --hard` | **no** |
| `git checkout HEAD pkg/a.go` | **no** |
| `cd sub && git reset --hard` | yes |

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Vet | `go vet ./...` | exit 0, no output |
| Focused tests | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/safety -run 'GitSweep|Shell' -count=1` | `ok` |
| Neighbours (the consumer) | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run -count=1` | `ok` |

## Scope

**In scope**:
- `pkg/safety/git_sweep.go`
- `pkg/safety/shell.go` (only `shellDashCScript`)
- `pkg/safety/git_sweep_test.go`
- `pkg/tool/peer_worktree_test.go` (only the one new case in step 5)

**Out of scope**:
- `pkg/tool/shell_tool.go` — the caller asks `WorkTreePeers` about the working
  directory; making it follow `git -C <dir>` is a separate, wider change (see
  maintenance notes).
- `pkg/safety/shell.go`'s dangerous-command scan (`dangerousCommandFromSource*`) —
  a different detector with its own tests. Note it **shares** `shellDashCScript`
  (`shell.go:573`), so teaching the helper `-lc` widens this detector too: `bash
  -lc 'rm -rf …'` starts matching. That direction only adds approvals; say so in
  the report and add the maintenance note below rather than pretending the other
  detector is untouched.
- The approval payload contract (`ask_even_in_yolo`, `peer_work_tree`) — unchanged.
- Anything in `pkg/gateway` or `pkg/tui` that renders the approval.

## Steps

### Step 1 — write the failing table first

In `pkg/safety/git_sweep_test.go` there are four table tests, and the new rows go
into the wrapper tables, not a fifth one: positives into
`TestGitSweepUnwrapsWrappers` (`:93`), negative controls into
`TestGitSweepWrapperNearMisses` (`:124`), and `git checkout HEAD pkg/a.go` into
`TestGitSweepRulesHit` (`:8`) expecting `GitSweepCheckoutPaths`. Each positive row
expects `(rule, true)` for the rule named in the plan's table.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/safety -run TestGitSweep -count=1`
→ **fails** on exactly the eleven "no" rows. (`TestGitSweep` is the prefix all
four tables share; a `-run` value that matches nothing runs zero tests and exits
0, which would report a vacuous pass.) Record which rows failed; if a row already
passes, drop it from the expectations to the "already covered" list rather than
contorting the test.

### Step 2 — skip leading assignments and transparent wrappers

In `gitSweepFromWords`, before the `switch`:

1. Drop leading `NAME=value` words (`parseEnvAssignment` already exists) — a
   prefix assignment does not change which command runs.
2. Add the wrapper cases, all recursing with `depth+1` on the remaining words:
   `command`, `exec`, `nohup`, `time`, `nice` (skip a leading `-n <n>`), and
   `timeout` (skip its own duration operand, then any `-…` options).
3. `sudo`: skip options, **including those that take a value** (`-u`, `-g`,
   `-h`, `-p`, `-C`, `-U`) — today `sudo -u me git …` recurses on `-u`.
4. `env`: skip `-u NAME` / `--unset=NAME` and any other option, not just `-i`
   and bare assignments — today `env -u X git …` recurses on `-u`.

Keep `gitSweepMaxWrapperDeep = 8` as the bound; a chain deeper than that reports
no match (it already does).

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/safety -run TestGitSweep -count=1`
→ only the `-lc` rows and the checkout row still fail.

### Step 3 — a short-option cluster containing `c`

Replace `shellDashCScript`'s exact `-c` test with: parse each `-…` word; if it is
`--`, stop; if it starts with `--`, skip it; otherwise, if the cluster (the
characters after the single dash) contains `c`, the script is the **next** word.
Mirror the existing behaviour for a missing operand (return `"", false`). Keep
`-c` working and keep `sh script.sh` (a file operand, no cluster) returning
false. This helper has a second caller — the dangerous-command detector at
`pkg/safety/shell.go:573` — so the widened parsing also makes `bash -lc 'rm -rf
…'` match that detector. Keep the helper's signature, do not weaken the detector,
and record the widening in your report.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/safety -run TestGitSweep -count=1`
→ only the checkout row still fails.

### Step 4 — `checkout` with a path operand

In `gitSweepFromSubcommand`'s `checkout` case, after the existing `--`/`.`
check: if the subcommand has **two or more** non-option operands, treat it as a
path checkout (`GitSweepCheckoutPaths`). `git checkout main` (one operand) and
`git checkout -b feat` stay unflagged; `git checkout HEAD file.go` is flagged.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/safety -run TestGitSweep -count=1`
→ **green**, including every pre-existing case.

### Step 5 — confirm the guard fires end to end

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run -count=1`
→ `ok`. Then extend `TestShellToolAsksBeforeAGitSweepWhileAnotherAgentWorks`
(`pkg/tool/peer_worktree_test.go:218`) with one more case: the same fake port and
`git stash` body, but the command is `bash -lc 'git stash'`. It must still return
a pending approval with `approval_reason == "peer_work_tree"` and
`ask_even_in_yolo == true`.

## Test plan

- New table rows: the eleven forms above plus two negative controls —
  `git log --grep=reset --oneline` and `git checkout main` must stay unflagged
  (the guard must not turn ordinary reads into approvals).
- Structural pattern: the existing table tests in `pkg/safety/git_sweep_test.go`
  (`TestGitSweepRulesHit:8`, `TestGitSweepUnwrapsWrappers:93`,
  `TestGitSweepWrapperNearMisses:124`).
- Regression: every pre-existing case in those tables, plus the
  `dangerousCommandFromSource` tests in `pkg/safety/shell_test.go`. That detector
  is expected to gain matches for `bash -lc '<dangerous>'` (the shared helper);
  any **loss** of a match is a defect, any added match is the intended widening.

## Done criteria

ALL must hold:

- [ ] `go vet ./...` exits 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/safety ./pkg/tool ./pkg/run -count=1` passes
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/safety -run TestGitSweep -count=1 -v` shows every form above flagged with its named rule
- [ ] `git log --grep=reset --oneline` and `git checkout main` are still unflagged (asserted in the test)
- [ ] `pkg/tool`'s peer-work-tree test covers `bash -lc 'git stash'` end to end
- [ ] Only the four in-scope files are modified (`git status --short`)
- [ ] `plans/README.md` row for 223 updated

## STOP conditions

- A new wrapper form turns a **read-only** git command into an approval in the
  existing test suite: report the form instead of weakening the positive cases.
- Changing `shellDashCScript`'s **signature** turns out to be the only way to
  teach it `-lc`: stop. Its other caller (`pkg/safety/shell.go:573`) posts the
  same `(script, ok)` contract; widen the parsing, not the interface.
- `parseWordOnlyCommandSequence` (used by `gitSweepWords`) already normalises
  assignments, so step 2's first sub-step is a no-op: skip it and say so.
- The classifier starts matching inside quoted strings (`git log --format='%s
  reset'`): that means word splitting broke; stop and report.

## Maintenance notes

- Wrapper lists rot. When a new shell idiom appears (a new `timeout`-like
  supervisor), add it here rather than in the tool.
- `shellDashCScript` is shared with the dangerous-command detector
  (`pkg/safety/shell.go:573`). Every widening of it widens both; a change here
  must keep `bash -lc '<read-only command>'` unflagged.
- A reviewer should check that the change only ever *adds* approvals: no
  pre-existing `false` becomes an unexpected `true` for a read-only command, and
  no `true` becomes `false`.
- Deliberately deferred: `git -C <dir> reset --hard` is classified correctly but
  the peer lookup at `pkg/tool/shell_tool.go:216` still asks about the session's
  working directory, not `<dir>`. Following `-C` means resolving it relative to
  the workdir and passing that path to `WorkTreePeers` — a plan of its own,
  because the same resolution is needed by the file tools' write hints.
