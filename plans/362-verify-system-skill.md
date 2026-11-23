# Plan 362: A `verify` system skill — drive the change, render a verdict, leave evidence behind

> **Executor instructions**: Follow this plan step by step. Run every verification
> command and confirm the expected result before moving to the next step. If
> anything in the "STOP and report" section occurs, stop and report — do not
> improvise. When done, update the status row for this plan in `plans/README.md`.
>
> **Drift check (run first)**:
> `git status --porcelain -- pkg/skill/system_assets pkg/skill/system_test.go` and
> `git diff --stat 6541b8ab -- pkg/skill/system_assets pkg/skill/system_test.go`.
> A `6541b8ab..HEAD` range diff is **always empty here** (the baseline is HEAD);
> use the two commands above. If an in-scope file moved, compare the "Current
> state" anchors against the live code before proceeding; on a mismatch, STOP.

## Status

- **Category**: feature / skill system
- **Priority**: P1
- **Effort**: M
- **Risk**: LOW (one new asset directory plus its install pins; no Go logic)
- **Depends on**: **353** (the `run` skill is the handle `verify` drives with), **358** (a process that survives the turn), **357** (loopback, so an HTTP surface is reachable). **360** is optional and unlocks visual evidence only. Plan 359 is rejected — `read_file` has returned image parts since `19239c1d`, so reading a capture is already possible; producing one is 360.
- **Blocks**: nothing. **Plan 355 must land after this one** — its A4 edit names `/verify`, and the command has to exist.
- **Supersedes**: plan 355's "What this plan deliberately does not build" ruling, which is reversed by owner decision 2026-11-23
- **Planned at**: commit `6541b8ab`, 2026-11-23

## Why this matters

forebrain can review a diff and it can run a test suite. It cannot currently
answer the only question that matters about most changes: **did the thing
actually work?**

`audit`'s `execute` variant reviews an executor's diff like a tech lead and
re-runs the done criteria — but done criteria are usually commands, and a
command passing is not the same as the feature working. `run` (plan 353) gets
the app started and driven, but it stops at "report what you saw": a narrative,
not a verdict. Nothing in the product renders a judgment, and nothing keeps the
evidence a reviewer could check afterwards.

That gap was recorded as a deliberate non-goal in plan 355, for three stated
reasons: no browser tool, no screenshot tool, and no agreed storage location for
a capture. **All three are now addressed** — 357 makes a local server reachable,
358 holds a process across turns and waits for readiness, 360 produces an image
(a tool result already could — `read_file` has returned image parts since
`19239c1d`, so plan 359 was rejected), and the storage location is the spool
convention `shell` already uses (`pkg/tool/shell_tool.go:592-595`). This plan is
the skill that composes them.

The distinction from its neighbours, which must stay sharp or all three drift:

| Skill | Object | Output |
|---|---|---|
| `run` (353) | the project's app | a narrative of what it saw |
| `audit execute` (355) | an executor's **diff** | a verdict on the diff, from reading code and re-running commands |
| **`verify` (this plan)** | a **change**, observed in the **running app** | a verdict from behavior, plus evidence a reviewer can reopen |

## Current state

Anchors verified at `6541b8ab`:

| File | Line | Role |
|---|---|---|
| `pkg/skill/system_assets/` | — | 13 built-in skills today: `audit caveman context-restore context-save docx frontend-design image-edit pdf pptx review skill-generator skill-workshop xlsx`. No `verify` |
| `pkg/skill/system.go` | `:14`, `:24-65`, `:69-84` | `//go:embed system_assets/*`; `Install` re-extracts when `Fingerprint()` changes — **dropping a directory in is the whole installation, there is no registry** |
| `pkg/skill/system_test.go` | `:323-336` | `topLevelDirs` — the test fails on an unlisted directory *and* on a listed-but-missing one |
| `pkg/skill/system_test.go` | `:46-48`, `:282-287` | the extraction assert and the embedded-vs-disk byte compare (both currently name `review`; plan 355 moves them) |
| `pkg/turn/slash.go` | — | the 26 reserved command names: `agent clear compact config connect context exit fork goal init lsp mcp memories migrate model new peers permissions plan rename resume rewind sandbox skills status subagents`. **`verify` is not among them** — verified by grep |
| `pkg/safety/shell.go` | `:2154` | `case "graph", "why", "verify":` — this is `go mod verify` classification. **Unrelated, not a collision** |
| `pkg/skill/catalog.go` | `:32-37`, `:47-53` | the catalog protocol, and why the catalog is frozen per session |
| `pkg/tool/shell_tool.go` | `:592-595` | `<StateRoot>/state/tool-output` — the spool convention evidence storage mirrors |

## Scope

**In scope**

- New: `pkg/skill/system_assets/verify/SKILL.md`
- New: `pkg/skill/system_assets/verify/references/evidence.md`
- `pkg/skill/system_test.go` — add `"verify"` to `topLevelDirs`, and the new test
- Nothing else. **No Go logic changes at all**: no changes to `pkg/skill/system.go`,
  `pkg/run/skills.go`, `pkg/turn/skills.go`, or any tool.

**Out of scope — do not touch**

- `pkg/skill/system_assets/audit/`. Plan 355 owns it and edits it. If you touch
  it, 355's byte-level assertions break.
- `pkg/skill/system_assets/run/`, `run-skill-generator/`. Plans 353/354 own them.
- `pkg/skill/system_assets/review/`. Plan 355 deletes it. Leave it alone whether
  or not 355 has landed.
- The other 12 skill directories, and `caveman/`'s sub-skills — note
  `caveman-review` exists and is unrelated.

## Provenance

**All text in this plan and in the assets it produces is original, authored for
this repository.** Do not consult, download, decompile, `strings`-dump or
transcribe any third-party binary, product, package or website. There is no carve
step and no hash gate here, because there is no source text to match. Where this
plan says "the text must state X," that is a requirement, not an invitation to
source X from elsewhere. The verdict *vocabulary* (PASS/FAIL/BLOCKED/SKIP) is a
common convention and not anyone's expression; the sentences around it are ours.

## Git workflow

- Branch: `advisor/362-verify-system-skill`
- Conventional Commits, lowercase imperative, optional `(scope)`, matching recent
  history (`feat(skill): a run skill that drives the app through the shell`).
  Use e.g. `feat(skill): a verify skill that renders a verdict on observed behavior`.
- Do NOT push and do NOT open a PR unless the operator instructed it.
- Dispatched into an isolated workspace, your branch lives in
  `.worktrees/<short-kebab-name>` under the project's main worktree; never push
  or merge into the user's branch.

## Step 1: `pkg/skill/system_assets/verify/SKILL.md`

Install this file exactly. It is original text written against forebrain's own
tools and limits — do not add sections, do not "improve" the wording, and do not
generalize it to tools forebrain does not have.

````markdown
---
name: verify
description: "Check a change by driving the running app, then render PASS/FAIL/BLOCKED/SKIP with evidence a reviewer can reopen. Use after a code change whose correctness is observable at runtime rather than provable from the diff."
argument-hint: "[what to verify]"
---

# Verify

A test suite passing is not the change working. This skill drives the affected
path in the real app and renders a verdict on what it observed.

## The four verdicts

- **PASS** - every criterion held under observation. Name what you drove and
  what it printed.
- **FAIL** - a criterion did not hold. Quote the observed output, not your
  reading of it, and say which criterion.
- **BLOCKED** - you could not reach the point of observation. Name the command
  that failed and its exact error. A BLOCKED is not a soft FAIL: it says the
  question went unanswered, and guessing an answer is the failure this verdict
  exists to prevent.
- **SKIP** - the change has no runtime-observable surface, or the criterion is
  already covered by a gate that ran. Say which.

There is no partial pass. A criterion either held while you watched or it did
not, and a run where three of four held is a FAIL naming the fourth. When the
evidence is ambiguous - a flaky port, a truncated capture, a log line that could
be read two ways - the verdict is FAIL, not PASS, and the ambiguity is the
reason.

## Step 1: Get the criteria before you drive

Read them from the change, not from your memory of it: the plan's done criteria,
the issue, the commit message, or ask. A criterion you invented after seeing the
output is not a criterion. Write the list down before starting anything, and if
there is none to find, say so and render SKIP rather than testing what was
convenient to test.

## Step 2: Find the handle

Look for a project run skill before building anything, the same four-directory
probe `run` uses:

```bash
for d in .forebrain .agents .claude .codex; do
  [ -d "$d/skills" ] && ls "$d/skills" | grep -i '^run' && echo "-> $d/skills"
done
```

Project skills load only in a **trusted** project, so a run skill that is on disk
but absent from the catalog means untrusted, not missing - say which. If a run
skill exists, drive with it and do not reinvent the build. If it fails on
mechanics unrelated to the change under verification, render BLOCKED, report the
skill as stale, and do not charge its failure to the code.

If there is no run skill, cold-start the way `run` describes. That costs time, so
timebox it: if you cannot reach the app, the verdict is BLOCKED with the command
and error you stopped at, plus a filled-in `/run-skill-generator` prompt the user
can paste. Do not spend the whole session building a handle and then call the
result a FAIL.

## Step 3: Drive the smallest path that executes the change

Start the app with the `process` tool, not with a backgrounded `shell` call:
`process` returns a handle that survives the turn, waits for readiness, keeps the
log, and can be killed. Wait for readiness explicitly - `wait: tcp` on the port,
`wait: http` on a health path, or `wait: log` on the line the app prints when it
is up - and never substitute a fixed sleep for a wait you could have observed.

Then exercise only what the change touches. One request, one screen, one command
path. A verification that drives the whole app proves nothing about this change
and takes long enough that it does not get run at all.

Rebuild before you drive. A stale binary runs the old build and your change looks
like it did nothing, which reads as a FAIL and is not one.

## Step 4: Keep the evidence

Write it under the session's own state directory, mirroring where `shell` spools
large output - never into the repository, and never under `.forebrain`, where the
sandbox denies command writes. `references/evidence.md` covers what to keep per
surface and how to hand it over.

Evidence is what makes a verdict checkable by someone who was not in the
session. A verdict with no evidence is an opinion, and the reviewer's only honest
response to an opinion is to redo the work.

## Step 5: Render it

State the verdict first, then the criteria with what each printed, then the
evidence paths, then what you did not check. Leave nothing running: `process
list` at the end, and kill what you started. A verification that leaks a server
on a fixed port breaks the next one.

Recommend `/run-skill-generator` only when you cold-started **and** the verdict
is PASS - a handle you proved works is worth keeping, and one you did not reach
is not. Otherwise recommend nothing. Never publish a skill yourself.

## Red flags

- You ran the test suite and called that a verification.
- You slept instead of waiting, and the first assertion raced the startup.
- The verdict is PASS but you cannot name the line you observed it in.
- You wrote the criteria after seeing the output.
- Something is still running, or a capture is still on disk in the repository.
- You rendered FAIL for a handle that never came up. That is BLOCKED.
````

## Step 2: `pkg/skill/system_assets/verify/references/evidence.md`

**This file is specified, not transcribed.** Author it in the same register as
the SKILL.md above — original prose, forebrain's own tools and paths, no
third-party product named as a source. It must contain these seven sections, in
this order, and each must carry the load-bearing content listed:

1. **`## Where evidence goes`** — `<StateRoot>/state/verify/<session-id>/`, one
   subdirectory per verification run, mirroring `state/tool-output`
   (`pkg/tool/shell_tool.go:592-595`) and `state/capture` (plan 360). State
   plainly why it is not the repository (it is not a deliverable, and committing
   it would put captures under version control) and why it is not under
   `.forebrain` (the sandbox denies command writes there —
   `pkg/tool/git_worktrees.go:25-27`).
2. **`## What to keep per surface`** — a table with one row per surface: terminal
   UI (a `tmux capture-pane -p` text dump, plus `-e` when attributes matter), web
   frontend (the response bytes, or a plan-360 capture when the question is
   visual), server/API (request and response, plus the server log tail from
   `process log`), CLI (stdout, stderr, exit code), database (the query and its
   rows). Each row names the **command** that produces it.
3. **`## Naming`** — a deterministic scheme so a reviewer can find things:
   `NN-<criterion-slug>.<ext>`, numbered in criterion order. Say why: evidence
   nobody can locate is evidence that does not exist.
4. **`## Handing it over`** — `present_files` (`pkg/tool/artifacts.go`) is how a
   capture reaches the user; the log spool is how a large output does. Note that
   `present_files` registers a deliverable in `fb_artifacts` and that evidence is
   normally *not* a deliverable — present only the one or two files the verdict
   actually rests on.
5. **`## Retention`** — evidence lives in the session's state directory and is
   cleaned with it. Do not add a retention policy, a rotation job, or a config
   knob; there is none today and inventing one is out of scope. If a reviewer
   needs it longer, they copy it out, and say so.
6. **`## What is not evidence`** — a passing test suite (that is a gate, not an
   observation), a screenshot nobody can open, a claim about what the code does,
   and a log from a previous run. This section is short and blunt.
7. **`## When you could not keep any`** — the BLOCKED path: record the command,
   its exact error, and what would have been captured. An empty evidence
   directory with a note explaining why is honest; a fabricated one is not.

## Step 3: Install pins

`pkg/skill/system_test.go`:

- Add `"verify"` to `topLevelDirs` (`:323-336`). That test fails both on an
  unlisted directory and on a listed-but-missing one, so the list and the
  directory must land together.
- If plan 355 has already landed, `"review"` is gone from that list — do not
  re-add it. If 355 has not landed, `"review"` is still there — do not remove it.
  Either state is correct; **whichever you find, change only your own entry.**
- Check whether the extraction assert at `:46-48` enumerates skill directories.
  If it does, add `verify` there too. Read it before deciding; do not assume.

## Step 4: Pin the contract

Add `TestVerifySkillDrivesTheAppAndRendersAVerdict` to `pkg/skill/system_test.go`,
modeled on `TestSystemAssetsCarryTheGeneratorSkill` (`:443-478`): `Install` into a
`t.TempDir()`, read `<home>/skills/.system/verify/SKILL.md`, assert on its bytes.

It must assert:

1. `verify/SKILL.md` and `verify/references/evidence.md` both exist after
   `Install`, and `references/evidence.md` is reachable through
   `listSkillResources` (`pkg/skill/activation.go:120-166`) — proof the bundled
   file is discoverable, not just present.
2. `LoadActivation` for `verify` reports `SlashCommand == true` **and**
   `ModelInvocation == true`. Unlike `run-skill-generator`, this skill is one the
   model *should* reach for on its own initiative; it must be in the catalog.
3. A catalog entry named `verify` is returned by `skill.CatalogEntries` for the
   installed root.
4. The body contains all four verdict words `PASS`, `FAIL`, `BLOCKED`, `SKIP`.
5. The body contains `no partial pass` and `the verdict is FAIL` — the two
   rules that stop a model from grading on a curve.
6. The body contains `process` (the tool), `wait: tcp`, and `trusted` (the trust
   gate) — the three forebrain-specific facts that make this asset about this
   harness rather than about apps in general.
7. The body contains `Rebuild before you drive` — the stale-binary trap, which is
   the single most common false FAIL.
8. The body contains `Leave nothing running` and `kill what you started` — the
   no-leaked-process rule. (The asset names the `process list` check too, but
   that phrase wraps across a line, so assert on the two that do not.)
9. The body contains `/run-skill-generator` exactly **twice** — once in the
   BLOCKED path (a filled-in prompt the user can paste) and once in the
   PASS-gated recommendation — and contains `Never publish a skill yourself`
   exactly once. Two is the correct count: the BLOCKED prompt and the
   recommendation are different acts, and collapsing them to one loses the
   case where verification never reached the app.
10. The body does **not** contain `screenshot` as an instruction to take one
    without qualification, and does **not** name any third-party product. Assert
    the absence of `Claude`, `Anthropic`, `chromium-cli`, and `playwright` —
    a headless browser is named generically in `evidence.md`, never by product.
11. `references/evidence.md` contains all seven `## ` section headings from
    Step 2, and contains `state/verify`, `present_files`, and `.forebrain`.

Do **not** add `verify` to any `skillDir` loop in
`TestSkillAuthoringRulesLiveInOnePlace` (`:524`) unless that test's own logic
requires it — read the test first and report what you found.

**Verify**:
`CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... -count=1 -run 'TestVerifySkill|TestInstall|TestSystemAssets|TestSkillAuthoring' -v` → PASS.

## Step 5: Full gates

1. `gofmt -l pkg/skill` → no output
2. `go vet ./pkg/skill/...` → exit 0
3. `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... -count=1` → PASS
4. `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/... -count=1` → PASS
   (asset files are not Go files, so the 34-file cap is untouched — but the
   two-underscores-per-filename rule applies to Go only; `evidence.md` is fine)
5. `make test` → PASS. Bare `go test ./...` fails on pdfium on macOS; always use
   the `CGO_ENABLED=1 -tags fts5` form.
6. Live: in a real session, make a small observable change to this repository,
   invoke `/verify`, and confirm it drives the app via `process`, renders a
   verdict, writes evidence under the session state directory, and leaves
   `process list` empty. Paste the transcript into the completion report.

## Done criteria

- [ ] `ls pkg/skill/system_assets/verify/` shows `SKILL.md` and `references/`
- [ ] `grep -c 'name: verify' pkg/skill/system_assets/verify/SKILL.md` → 1
- [ ] The four verdict words, `no partial pass`, `process`, `wait: tcp`, `trusted`, `Rebuild before you drive`, `Leave nothing running`, `kill what you started` all present
- [ ] `grep -c '/run-skill-generator' pkg/skill/system_assets/verify/SKILL.md` → **2**; `Never publish a skill yourself` → 1
- [ ] Asset is 106 lines with exactly 7 `## ` sections and 2 inner ``` fences (the probe block) — if your counts differ, your transcription differs from Step 1; fix the transcription, not the numbers
- [ ] `grep -cE 'Claude|Anthropic|chromium-cli|playwright' pkg/skill/system_assets/verify/SKILL.md pkg/skill/system_assets/verify/references/evidence.md` → 0 per file. **Case-sensitive, and product names only** (assertion 10's semantics): the asset's discovery probe names the *directories* `.claude` and `.codex`, which the product really discovers (`pkg/skill/roots.go:21-26`) — a lower-case `grep -i claude` would hit them and is the wrong gate. Do not "fix" the asset to satisfy a case-insensitive grep
- [ ] `evidence.md` has all seven `## ` sections and names `state/verify`, `present_files`, `.forebrain`
- [ ] `"verify"` added to `topLevelDirs`; `"review"` left in whatever state plan 355 put it in
- [ ] All 11 assertions present and passing; `git status --porcelain -- pkg/skill/system_assets/` shows **only** the new `verify/` directory (as `??` while untracked). `git diff --stat` cannot show a new untracked directory, and is empty once it is committed — use `git status --porcelain` before committing and `git diff --stat 6541b8ab -- pkg/skill/system_assets/` after
- [ ] `make test` green, `gofmt -l` clean, `go vet` exit 0
- [ ] The Step 5.6 live transcript is in the report

## STOP and report instead of improvising

- `verify` turns out to collide with an existing skill, command, or agent name
  that my grep missed. Report the collision; do not rename it to `verify-change`
  or similar on your own.
- Plans 357/358 have not landed. This asset instructs the model to use the
  `process` tool; without 358 that instruction names a tool that does not exist,
  and the asset would be actively misleading. Report and wait.
- Adding `"verify"` to `topLevelDirs` makes `TestInstall_AllSkillDirsExtracted`
  fail on a *different* directory. That means 353/354/355 are partly landed;
  report the exact failure rather than editing other entries.
- `references/evidence.md` needs a section the seven do not cover. Add nothing;
  report what is missing. A reference file that grows per-session stops being a
  reference.
- The live run in Step 5.6 shows `verify` recommending `/run-skill-generator`
  when it did not cold-start, or publishing anything. That is the restraint rule
  failing; report the transcript rather than loosening the wording.

## Maintenance notes

- **Plan 355 must land after this one.** Its A4 edit adds "and `/verify` when a
  done criterion is only observable in the running app" to `audit`'s `execute`
  variant, and a recommendation naming a command that does not exist is worse
  than no recommendation. If you are executing 355 first, skip only the A4
  `/verify` clause and note it; do not reorder the rest.
- **Three skills now touch "run the app": `run`, `audit`, `verify`.** The table
  in *Why this matters* is the boundary. If a future edit makes `verify` describe
  how to build a handle, or makes `run` render a verdict, the three have started
  to drift — and this repository has a test,
  `TestSkillAuthoringRulesLiveInOnePlace`, whose whole purpose is to catch that.
- **The verdict vocabulary is load-bearing and easy to soften.** "There is no
  partial pass" and "when the evidence is ambiguous the verdict is FAIL" are the
  two sentences that keep a model from grading its own work on a curve. Assertion
  5 pins both. If a future edit removes either, the test fails on purpose.
- **Evidence storage is a session-state convention now.** `state/tool-output`
  (shell), `state/process` (358), `state/capture` (360), `state/verify` (this
  plan). If a fifth appears, consolidate the convention into one documented place
  rather than adding a sixth silently.
- **Reviewer scrutiny**: read the asset as prose. It is a judgment skill, and a
  judgment skill written as a checklist produces checklist verdicts — a model
  that ticks four boxes and calls it PASS. The *Red flags* section exists to make
  that uncomfortable; keep it last and keep it blunt.
