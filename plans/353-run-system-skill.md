# Plan 353: Add a `run` system skill, written against forebrain's own tools, trust model and acceptance harness

> **Executor instructions**: Follow this plan step by step. The complete final
> text of every file you create is inlined below — install it **exactly as
> written**. Do not consult, download, decompile, `strings`-dump or transcribe any
> third-party binary or product; nothing in this plan requires it. Run every
> verification command and confirm the expected result before moving to the next
> step. If anything in the "STOP conditions" section occurs, stop and report — do
> not improvise. When done, update the status row for this plan in
> `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 9b5c4891..HEAD -- pkg/skill/system_assets pkg/skill/system_test.go scripts/acceptance`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: LOW (one new embedded asset plus a test-list entry; no Go logic changes)
- **Depends on**: `plans/356-skill-model-invocation-gate.md` — assertion 4 of the
  new test reads `Activation.ModelInvocation`, which 356 adds. If 356 has not
  landed, drop that one assertion and say so (Step 4). **Plan 354 depends on
  this one.**
- **Category**: feature (skill system)
- **Planned at**: commit `9b5c4891`, 2026-11-22
- **Review 2026-11-23 (`review-plan plans/353-365`, baseline `6541b8ab`)**: two
  false "verified in this repository" facts corrected. ① The registered-tool list
  named `grep`, `explore` and `apply_patch`; none is a registered tool (search is
  `shell` + `rg`/`find`, `explore` is a subagent type at
  `pkg/agent/subagent_defs.go:20`, `apply_patch` is a heredoc convention inside
  `shell`, `shell_tool.go:83`). This also contradicted the batch's own recorded
  fact that forebrain has no `grep`/`glob` tool. ② The driver's isolation switch
  is `FOREBRAIN_RUN_WORK` (`scripts/acceptance/driver.sh:13`), not
  `FOREBRAIN_ACCEPT_HOME` (zero occurrences in that script). The inlined asset
  itself was not touched: re-measured at **170 lines** with every count gate
  unchanged (`## ` 7, `### ` 1, probe 1, `run-skill-generator` 3, the restraint
  sentence 1, `trusted project`/`FOREBRAIN_HOME`/`scripts/acceptance` 1 each,
  forbidden strings 0).

## Provenance — read first, it constrains everything below

The *behavior* implemented here — an agent that reaches the app's real surface
instead of stopping at the test suite, prefers an existing project run skill over
rediscovering the build, builds a repeatable deterministic handle, and recommends
capturing hard-won launch work as a project skill — was studied from how agent
tools structure this workflow. **All text in this plan, and all text in the asset it
produces, is original and authored for this repository.** Workflows, ideas and
command names are not copyrightable expression; copied prose is.

Hard requirements on you:

- Do **not** extract, quote, or closely paraphrase prompt text from any third-party
  product, binary, package or website. There is no carve step and no hash gate in
  this plan, because there is no source text to match.
- Do **not** go looking for a "more faithful" version of the inlined text. The
  inlined text *is* the deliverable. If you think a sentence is wrong, report it.
- Where this plan says "the text must state X", that is a requirement, not an
  invitation to source X from somewhere.

## Why this matters

forebrain's agents can build and test a project, and nothing in the harness tells
them to *run* it. The gap shows up exactly where tests pass and the feature is still
broken: a TUI overlay that renders wrong, a `/config` panel that swallows input, a
web badge that never appears. Plans 350 and 351 both needed real-machine acceptance
to catch that class of bug, and both got it from a driver rather than from a test
suite.

This repository already solved the problem twice over, and the new skill exists to
generalize what it did:

- `.claude/skills/run-forebrain/` — a 511-line project run skill with `driver.sh`,
  `fake_provider.py`, `term_live.py`, `term_replay.py`, `paint_dump.sh` and worked
  examples for reading the rendered screen and locating the composer's caret.
- `scripts/acceptance/` — the same idea as committed infrastructure: `driver.sh`,
  `fake_provider.py`, `fake_mcp_stdio.py`, `flaky_proxy.py`, `live_subagent.sh`,
  `multi_replica_client.py`, `web_e2e.sh`, `ws_slash.py`, `native_advisor_workflows.sh`.

What the repository has is a pattern with no general skill behind it. An agent in a
*different* project gets nothing: no instruction to prefer an existing run skill, no
account of the tools it actually has to drive with, no definition of what counts as
verified, and no prompt to capture the launch work it just did. This plan adds that
skill, written in forebrain's own terms. Plan 354 adds the generator that writes
project-specific instances of it.

The most valuable sentence in the asset is the conditional recommendation at the
end: when getting the app running took real work — installs, environment variables,
configuration patches, a driver it had to write — the agent says so and points the
user at `/run-skill-generator`, so the next session starts from that work. The
restraint clause beside it is what keeps the suggestion from becoming noise on every
uneventful run.

## Current state

### forebrain's driving surface — the facts the asset is built on

Verified in this repository, and the reason the asset reads the way it does:

- **There is no browser or screenshot tool.** `ls pkg/tool/` has no
  browser/screen/shot/visual file, and the registered tool names include `shell`,
  `read_file`, `write_file`, `edit_file`, `skill`,
  `subagent_run`, `subagent_send`, `subagent_fanout`, `user_interaction`,
  `web_fetch`, `web_search`, `lsp`, `peer_list`, `peer_send`, `memories_*`,
  `session_todo`, `present_files`, `request_permissions`, `enter_plan_mode`,
  `exit_plan_mode` — but nothing that renders or captures a screen. There is also
  **no `grep`, `glob` or `explore` tool**: search is `shell` running `rg`/`find`,
  `explore` is a subagent type (`pkg/agent/subagent_defs.go:20`), and `apply_patch`
  is a heredoc convention inside a `shell` call (`shell_tool.go:83`), not a
  registered tool. **An agent
  observes a UI by shelling out** — tmux capture, a terminal replay, a headless
  browser driven from a script, a dump of the rendered cells — **and, when the
  observation is a picture, by `read_file`**: it has returned image parts for
  png/jpeg/gif/webp since `19239c1d` (`pkg/tool/file_tools.go:391-437`), so a
  screenshot a driver saved is something the model can actually look at. What
  does *not* exist is anything that **takes** the screenshot. This is the single
  most important forebrain-specific fact in the asset, and it is why the "handle"
  section exists.
- **`scripts/acceptance/driver.sh`** is the repository's own driver. Its usage
  block (`:145-160`), verbatim:
  ```
  usage: driver.sh <command>
    build                  build the binary into $WORK
    start [hang|stream|reply|drip] [answer-text] [drip-delay]
    submit <text>          type <text> and press Enter
    send <text>            type <text> without pressing Enter
    key <key...>           send keys (Enter, Escape, C-c, Down, ...)
    wait <text> [secs]     poll the screen until <text> appears
    screen                 print the rendered screen
    messages | title | sid read the state DB
    home | proj            print the isolated home / project dir
    db <sql>               run SQL against the state DB
    provider-log           what the fake provider received
    stop | reset           tear down / wipe the isolated home
  ```
  Note `home`/`proj`: the driver runs against an **isolated home and project
  directory** (`FOREBRAIN_RUN_WORK` is the switch; both live under it), so an
  acceptance run cannot touch real state. That isolation is a forebrain semantic
  the asset must teach.
- **Verification gates** (Makefile:103-104): `make test` is
  `CGO_ENABLED=1 go test -tags fts5 ./... -count=1`. On macOS a bare
  `go test ./...` fails on pdfium. `pkg/architecture` enforces repo-wide structure:
  **34 production files per package** (`maxProductionFilesPerPackage`,
  `pkg/architecture/cache_test.go:1532`; the doc comment above the test at
  `:1461-1462` still says "cap of 30" and is stale — do not teach the comment's
  number) and **at most two underscores per file name** (`:1435-1457`).
- **Skill trust gating**: `AgentSkillRoots(home, workspaceRoot, launch)`
  (`pkg/skill/roots.go:52-60`) includes the project skill roots **only when
  `launch.IsTrusted()`**. So in an untrusted project a `SKILL.md` can exist on disk
  and still be absent from the catalog the model sees. The asset must say this, or
  an agent will conclude the project has no run skill when it does.
- **Project skill resolution**: `projectSkillDirs` (`pkg/skill/roots.go:21-26`) is
  `.forebrain/skills`, `.agents/skills`, `.claude/skills`, `.codex/skills`, highest
  priority first, with the comment "A team that has committed skills for Claude Code
  or Codex has committed skills for this repository". `projectSkillRootsForRoot`
  (`:125-136`) builds exactly those four at the session's project root — **no upward
  walk to the checkout root, no descent into subdirectories**. Anything under
  `.drafts/` is inert (`pkg/skill/discover.go:77` skips any leaf beginning with `.`).
  A skill with no `description:` is skipped on disk (`:96-98`) and refused at
  creation (`:122-131`).
- **Writes to the formal path are draft-redirected; writes to the cross-tool
  directories are not.** `VersionedSkillLayers` (`pkg/skill/versions.go:849-873`)
  covers `<projectRoot>/.forebrain/skills`, `<workspaceRoot>/skills`, `<home>/skills`
  only; the comment at `:829-836` states the others "belong to other tools or ship
  read-only, so writes aimed at them are not redirected". Hence: refining a run skill
  that lives in `.claude/skills/` is an in-place edit, with no draft and no publish.
- **The catalog is frozen per session.** `pkg/run/skills.go:35-132` renders it into
  the cached prompt prefix once, so a skill published mid-session is invisible until
  the next one.
- Relative links inside a skill body need no registration: `catalogUsage`
  (`pkg/skill/catalog.go:32-37`) tells the model to resolve relative paths against
  the skill's own directory, and `renderSkillActivation`
  (`pkg/skill/activation.go:97-118`) emits the skill's `<path>`.

### The install mechanism — no registration needed

`pkg/skill/system.go` embeds `system_assets/*` (line 14). `Install(forebrainHome)`
(`:24-65`) compares a stored marker against `Fingerprint()` (`:69-84` — first 16 hex
of SHA-256 over every embedded path and its contents); on mismatch it stages into
`.<dir>.seed-*`, extracts, writes the marker, then `RemoveAll(dest)` + `Rename`.
Destination is `$FOREBRAIN_HOME/skills/.system/` (`systemSkillsDirName = ".system"`,
`pkg/skill/skill.go:30`). **There is no registry** — dropping a directory under
`system_assets/` is the whole installation. Resist adding an explicit list.

### The test that fails if you skip Step 3

`TestInstall_AllSkillDirsExtracted` (`pkg/skill/system_test.go:314-380`) hardcodes
`topLevelDirs` (`:323-336`: audit, review, context-save, context-restore,
skill-workshop, skill-generator, image-edit, frontend-design, docx, pdf, pptx, xlsx)
and derives `allowedEntries` (`:345-351`) as the marker plus `caveman` plus that list.
It errors on any top-level directory **not** listed and any listed directory that is
**missing**, so the new `"run"` entry and the new directory must land together.

(Plan 355 later deletes `"review"`. Executing out of order and finding it already
gone is expected — add `"run"`, leave the rest alone.)

### The sibling whose vocabulary this must match

`pkg/skill/system_assets/skill-generator/SKILL.md` establishes the draft/publish
language: write to `<project-root>/.forebrain/skills/.drafts/<name>/SKILL.md`
(`:19-21`, and again in the create example at `:87`); "**Never publish it
yourself**" — publishing is the user's act, from the web skill page's Publish
button or the terminal `/skills` panel — is stated at `:30-33`; read the file
back and leave **one** line for the user, never open or push a panel (`:112-123`);
"visible from the **next** session onward: the skills catalog sits in the cached
prompt prefix" (`:36-39`). The new asset must not contradict any of it.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Format | `gofmt -l pkg/skill` | no output |
| Vet | `go vet ./pkg/skill/...` | exit 0 |
| Skill tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... -count=1` | ok |
| Full suite | `make test` | exit 0 |

## Scope

**In scope** (the only files you should modify):

- `pkg/skill/system_assets/run/SKILL.md` (create)
- `pkg/skill/system_test.go` — `topLevelDirs` (Step 3) and one new test (Step 4)

**Out of scope** (do NOT touch, even though they look related):

- Every other directory under `pkg/skill/system_assets/`. In particular
  `skill-generator/SKILL.md` (read for vocabulary, never edit),
  `skill-workshop/**` including `references/authoring.md`, `audit/**` (plan 355),
  `review/SKILL.md` (plan 355 **deletes** it).
- `.claude/skills/run-forebrain/**` and `scripts/acceptance/**` — both are read as
  exemplars. **Never write to either**, and never copy their repository-specific
  commands into the asset as though they existed in every project.
- `pkg/skill/system.go`, and every other Go file. **No production logic changes.**
- `Makefile`, `pkg/architecture/**`, `NOTICE`, `LICENSE`, `README.md`,
  `FOREBRAIN.md`, `docs/**`.

## Git workflow

- Branch: `advisor/353-run-system-skill`
- Conventional Commits, lowercase imperative subject, optional `(scope)`, matching
  recent history (`feat(config): a footer action that submits a form, and the name
  hint`). Use e.g. `feat(skill): a run skill that drives the app through the shell`.
- Do NOT push and do NOT open a PR unless the operator instructed it.

## Steps

### Step 1: Create the asset directory

```bash
mkdir -p pkg/skill/system_assets/run
```

**Verify**: `ls -d pkg/skill/system_assets/run` prints the path.

One file, no `references/`. Plan 354's generator carries the template and the
driver patterns; duplicating them here would create two copies to keep in sync — the
drift this repository guards against with `TestSkillAuthoringRulesLiveInOnePlace`
(`pkg/skill/system_test.go:509-552`).

### Step 2: Write `run/SKILL.md`

Create `pkg/skill/system_assets/run/SKILL.md` with **exactly** this content. It is
original prose authored for this repository and written against forebrain's actual
tools and semantics. Install it verbatim; do not add sections, do not "improve" it,
and do not source any part of it from elsewhere.

````markdown
---
name: run
description: Build, launch and drive this project's app so a change can be seen working at its own surface. Use when a change has to be observed — a rendered screen, an HTTP response, a CLI invocation, a panel or overlay — rather than only proven by tests, and to check whether an existing run skill for this project still works.
---

# Run the app

A passing test suite says the code is internally consistent. It does not say the
feature works. When a change has a surface a person would look at, reach that
surface and look at it.

## What you have to drive with

forebrain gives you no browser tool and no screenshot tool. You drive an app
through the `shell` tool and observe it through what a shell can print — plus
`read_file`, which returns a png, jpeg, gif or webp file as pixels:

- a terminal UI → run it inside `tmux` and capture the pane, or replay a recorded
  session; the rendered cells are text, so `grep` them
- a web UI → drive a headless browser from a script and dump the DOM, the console
  or a network log; or hit the API behind it and read the response
- a server or CLI → run it, send the request, read the bytes that came back
- state that only exists in a database → query it, rather than trusting the screen

Use `read_file` for logs and captured output that are too large to print inline.

Because observation goes through a shell, **a driver script is usually the
difference between verifying a change and guessing at it.** Writing one is not
overhead; it is the work, and it is what makes the next check cheap.

## Look for a run skill first

Before reading the README, check whether this project already recorded how to
build, launch and drive itself:

```bash
for d in .forebrain .agents .claude .codex; do
  grep -Hm1 '^description:' "$d"/skills/*/SKILL.md 2>/dev/null
done
```

Those four directories at the project root are where forebrain resolves project
skills, highest priority first. It does not walk up to the checkout root and does
not descend into subdirectories, and anything under `.drafts/` is a draft — inert
until the user publishes it.

Two forebrain specifics that otherwise read as a contradiction:

- **Project skills load only in a trusted project.** If the probe prints a run skill
  that is not in your skills catalog, the project is not trusted; say so and ask,
  rather than concluding the skill is broken.
- **A published skill is visible from the next session onward**, because the catalog
  sits in the cached prompt prefix. A skill published during this session will not
  appear in it.

Do this check **even when you are sure you already know how to run the project.** A
`run-*` match is the project's own evidence: the install line that actually worked,
the driver someone wrote, the gotchas that cost an hour last time. Match on the
description rather than the name, because these skills get named variously.

- **A run skill that fits the change** → follow it, using its commands as written.
- **One that fits a different surface** (a browser driver for a CLI change) → ignore
  that one and keep looking.
- **One that fails on mechanics unrelated to your task** — a dependency moved, an
  install line rotted, a port changed → it is stale. Tell the user, offer to refresh
  it with `/run-skill-generator`, and do not charge its breakage to the change you
  were asked about.
- **A match in `.claude/skills/` or `.agents/skills/`** → those directories belong to
  other tools and forebrain does not redirect writes there, so refine the skill in
  place. Never copy it into a forebrain draft: two copies of one runbook is how a
  project ends up with two contradictory answers.
- **Nothing** → cold start, below.

## Cold start

Work the app out from what the project says about itself: `README.md`, the manifest's
script or task list (`Makefile`, `justfile`, `package.json`, `pyproject.toml`,
`go.mod`), `Dockerfile`, `docker-compose.yml`, and `.github/workflows/`. **CI
configuration is usually more accurate than the README** — it has to work unattended,
so it does not drift the way prose does.

Timebox this. Roughly fifteen minutes of trying to get the app up is worth it; half a
day is not. If you are still stuck at that point, report where exactly you stopped —
the command and its error — instead of quietly falling back to the test suite and
describing that as verification.

## Build a handle you can repeat

An app that needs live credentials, a real network, or a human at a device is hard to
verify once and impossible to verify twice. Where the project talks to an external
service, stand up a fake one:

- **A local stub that speaks the same protocol** and returns scripted responses — an
  OpenAI-compatible HTTP endpoint, a stub MCP server over stdio, a proxy that drops
  or delays requests. A run then needs no API key, no network and no money, and it
  reproduces.
- **Scripted modes on the stub**, so one driver can produce a normal reply, a slow
  stream, a tool call, an approval prompt, an error, a rate limit, a dropped
  connection. The mode is how you *choose* the scenario instead of hoping for it.
- **Environment variables for thresholds and delays**, so an edge case that normally
  takes an hour can be provoked in seconds.
- **An isolated home and project directory** for the run, so nothing it writes can
  reach real state. Point `FOREBRAIN_HOME` at a scratch directory and wipe it
  afterwards; a run that mutates the user's real configuration is not a test.

forebrain's own repository does all four, and it is the shape to imitate — not a set
of commands that exist in other projects. `scripts/acceptance/` holds a committed
driver (`driver.sh`: `build`, `start [hang|stream|reply|drip]`, `submit`, `send`,
`key`, `wait`, `screen`, `messages`, `db`, `provider-log`, `home`, `proj`, `stop`,
`reset`) plus `fake_provider.py`, `fake_mcp_stdio.py` and `flaky_proxy.py`, and
`web_e2e.sh` for the web frontend; `.claude/skills/run-forebrain/` is the same idea
written up as a project skill, including pixel-level terminal capture. Note what its
prerequisites section is: one exact install line for the operating system it was
verified on, followed by an explicit statement of what is *not* needed. That
specificity is the point.

### Rebuild before you drive

The failure that looks like success is a **stale build**. If the app is compiled,
rebuild after changing source and before driving it — a driver that launches a cached
binary faithfully shows the old behavior, and your change looks like it did nothing at
all. A `reset` or `stop` command usually clears state without rebuilding; do not
mistake one for the other.

## Drive the smallest path that executes the change

Do not smoke-test the app. Make the changed code run:

- changed a flag, setting or configuration panel → run with it set, and once unset
- changed a handler, route or gateway endpoint → hit that route and read the response
- changed error handling or a retry path → provoke the error, with the stub
- changed a rendering path, overlay or panel → look at the rendered output, not at the
  data behind it
- changed a slash command or key binding → send those keys and read the screen
- changed an internal function → find the command, request or screen that reaches it

Then **read what came back.** A command that exited 0 with the wrong thing on screen
is a failure. Compare what you observed with what the change claims to do, and say
which of the two you are reporting on.

## Verify with the project's own gates

Running the app does not replace the checks; it is the check they cannot do. In this
repository the gates are `make test` (which is
`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` — a bare `go test ./...` fails on
pdfium here), `gofmt -l`, `go vet`, and `pkg/architecture`, which enforces at most 34
production files per package and at most two underscores in a file name and is worth
running after adding files. In another project, find the equivalents from CI and run
those.

## Report what you saw

Lead with the observation, not the verdict. "The overlay appears while the ticks keep
arriving; Enter approves; the card shows the approved command" is worth more than
"works". Include the command you ran, so the user can repeat it, and say what you did
**not** manage to observe.

If getting the app to run took work the project had not already recorded — installing
packages, setting environment variables, patching configuration, writing a driver or a
stub — say so, and recommend `/run-skill-generator` in your report so that work becomes
a project skill and the next session starts from it instead of repeating it.

**If the app just ran, recommend nothing.** A run skill is worth writing when getting
there was hard. Suggesting one after an uneventful run teaches the user to ignore the
suggestion.

Never write or publish a skill on your own initiative. `/run-skill-generator` is the
user's command to run; a draft it writes lands under
`<project-root>/.forebrain/skills/.drafts/`, and publishing it — from the web skill
page's Publish button or the terminal `/skills` panel — is the user's decision.
````

**Verify**:

```bash
f=pkg/skill/system_assets/run/SKILL.md
head -4 "$f"
grep -c "^## " "$f"; grep -c "^### " "$f"
grep -c "for d in .forebrain .agents .claude .codex" "$f"
grep -c "run-skill-generator" "$f"; grep -c "just ran, recommend nothing" "$f"
grep -c "trusted project" "$f"; grep -c "FOREBRAIN_HOME" "$f"
grep -c "scripts/acceptance" "$f"; grep -c "stale build" "$f"
grep -c "^make test\|make test" "$f"; wc -l "$f"
```

Expected: frontmatter shows `name: run` and a one-line `description:`; `## ` = **7**
(`What you have to drive with`, `Look for a run skill first`, `Cold start`, `Build a
handle you can repeat`, `Drive the smallest path that executes the change`, `Verify
with the project's own gates`, `Report what you saw`); `### ` = **1** (`Rebuild before
you drive`); discovery probe = 1; `run-skill-generator` = **3** (the Stale bullet,
the closing recommendation, the closing publish paragraph); `just ran, recommend
nothing` = 1 (the restraint clause); `trusted project` = 1; `FOREBRAIN_HOME` = 1;
`scripts/acceptance` = 1; `stale build` = 1; `make test` = 1; line count **150–185**
(170 as authored).

The forebrain-specific assertions are the point of this plan: `trusted project`,
`FOREBRAIN_HOME`, `scripts/acceptance` and the "no browser tool" framing are what
distinguish this asset from tool-agnostic prose that could belong to any agent.

### Step 3: Register the directory in the install test

Add `"run",` to `topLevelDirs` in `pkg/skill/system_test.go:323-336`, after
`"review",`. `allowedEntries` (`:345-351`) is derived from that list, so no second
edit is needed.

**Verify**:
`CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... -count=1 -run TestInstall -v` → PASS.
If it fails naming an unexpected directory, a stray file (editor backup, `.DS_Store`)
got picked up by the embed — remove it; do not widen the list.

### Step 4: Add the contract test

Add `TestRunSkillDrivesTheAppAndOffersToCaptureIt` to `pkg/skill/system_test.go`,
modeled on `TestSystemAssetsCarryTheGeneratorSkill` (`:443-478`): `Install` into a
`t.TempDir()`, then `ValidateSkillDir` and `LoadActivation` against
`<home>/skills/.system/run`.

It must assert:

1. `ValidateSkillDir(<home>/skills/.system/run)` returns nil — frontmatter parses and
   carries a description, so discovery will not skip it
   (`pkg/skill/discover.go:96-98`).
2. `LoadActivation` reports `Name == "run"`.
3. `activation.SlashCommand == true` — `/run` is an entry point.
4. `activation.ModelInvocation == true`. Unlike plan 354's generator, this is a skill
   the model *should* pick up on its own initiative. **If plan 356 has not landed the
   field does not exist**: drop this one assertion and say so in your report rather
   than inventing a field.
5. `skill.CatalogEntries([]string{<home>/skills/.system}, "")` contains an entry named
   `run` whose `Description` mentions driving or launching the app. The catalog is what
   the model actually reads, so this is the end-to-end proof.
6. `Content` contains the discovery probe `for d in .forebrain .agents .claude .codex`.
7. `Content` contains the restraint clause `just ran, recommend nothing` — the
   assertion a future editor is most likely to break by "tightening" the prose, so pin
   it by its exact words.
8. `Content` contains `run-skill-generator` at least three times — Stale, closing
   recommendation, and the publish boundary are three distinct sites; dropping any must
   fail.
9. `Content` contains `trusted project` and `FOREBRAIN_HOME` and `scripts/acceptance` —
   the forebrain-specific semantics. These three are what make this asset forebrain's
   rather than generic, and they are the assertions to check first in review.
10. `Content` contains `.drafts` and `Never write or publish a skill on your own
    initiative` — the draft/publish boundary, consistent with the sibling
    `skill-generator`.
11. `Content` does **not** contain `driver.sh start hang`, `brew install tmux`,
    `term_live`, or `paint_dump` — commands specific to
    `.claude/skills/run-forebrain/`. Naming `scripts/acceptance/driver.sh` and its
    subcommand list is intended; reproducing the *other* skill's forebrain-only
    invocation recipes as general instructions is not.

Structural pattern for the whole test: `TestSystemAssetsCarryTheGeneratorSkill`
(`pkg/skill/system_test.go:443-478`).

**Verify**:
`CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... -count=1 -run TestRunSkill -v`
→ PASS, one test.

### Step 5: Full gates

1. `gofmt -l pkg/skill` → no output
2. `go vet ./pkg/skill/...` → exit 0
3. `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... ./pkg/turn/... ./pkg/run/... -count=1` → ok
4. `make test` → exit 0
5. `git status --porcelain` → only the two in-scope paths

## Test plan

New test: `TestRunSkillDrivesTheAppAndOffersToCaptureIt` in
`pkg/skill/system_test.go`, eleven assertions per Step 4. Structural pattern:
`TestSystemAssetsCarryTheGeneratorSkill` (`:443-478`).

Updated: `TestInstall_AllSkillDirsExtracted` (`:314`) gains `"run"`.

Assertions 7, 9 and 11 carry the review weight. 7 pins the restraint clause, which is
what stops the recommendation becoming spam. 9 pins the forebrain-specific semantics
that are the whole reason this is not generic prose. 11 pins the boundary between a
*built-in* skill that ships to every project and this repository's own instance — the
easiest mistake here is pasting `run-forebrain`'s recipes into an asset every
forebrain user receives.

Verification: `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... -count=1` → all pass,
including 1 new test.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `pkg/skill/system_assets/run/SKILL.md` exists, 150–185 lines, frontmatter `name: run` plus a one-line `description:`
- [ ] `grep -c "^## " …/run/SKILL.md` returns 7 and `grep -c "^### " …/run/SKILL.md` returns 1
- [ ] `grep -c "for d in .forebrain .agents .claude .codex" …/run/SKILL.md` returns 1
- [ ] `grep -c "run-skill-generator" …/run/SKILL.md` returns 3
- [ ] `grep -c "just ran, recommend nothing" …/run/SKILL.md` returns 1
- [ ] `grep -c "trusted project" …/run/SKILL.md`, `grep -c "FOREBRAIN_HOME" …`, `grep -c "scripts/acceptance" …` each return 1
- [ ] `grep -c "driver.sh start hang\|brew install tmux\|term_live\|paint_dump" …/run/SKILL.md` returns 0
- [ ] `"run"` appears in `topLevelDirs` at `pkg/skill/system_test.go:323-336`
- [ ] `gofmt -l pkg/skill` produces no output
- [ ] `go vet ./pkg/skill/...` exits 0
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... -count=1` exits 0, including `TestRunSkillDrivesTheAppAndOffersToCaptureIt`
- [ ] `make test` exits 0
- [ ] `git status --porcelain` lists only `pkg/skill/system_assets/run/SKILL.md` and `pkg/skill/system_test.go`
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- You are tempted to fetch, extract or transcribe prompt text from any third-party
  product to make this asset "more faithful". **Stop.** The inlined text is the
  deliverable and originality is a hard requirement. Report the impulse.
- Any command, path or limit this plan asserts about forebrain does not hold:
  `scripts/acceptance/driver.sh` missing or its usage block different, `make test`
  not equal to `CGO_ENABLED=1 go test -tags fts5 ./... -count=1`, the
  34-production-file cap or two-underscore rule changed, or `AgentSkillRoots` no
  longer gating project roots on `launch.IsTrusted()`. The asset teaches these as
  facts; a stale fact in a shipped prompt is worse than no fact. Report the
  discrepancy instead of silently editing the text.
- `grep -n "topLevelDirs" pkg/skill/system_test.go` does not land inside
  `TestInstall_AllSkillDirsExtracted`, or the list does not match "Current state".
- The frontmatter fails to parse (`ValidateSkillDir` errors). `Parse`
  (`pkg/skill/skill.go:74-109`) uses a non-strict `yaml.Unmarshal`, so an unknown key
  is dropped but a malformed one is fatal — check `description:` is one line with no
  unescaped colon-space.
- `TestSkillAuthoringRulesLiveInOnePlace` (`:509-552`) fails. It loops only
  `skill-workshop` and `skill-generator`, so a failure means you edited outside scope.
- Any pre-existing test fails. In particular `pkg/skill/system_test.go:282-287`
  compares embedded bytes against on-disk bytes for `review/SKILL.md`; if it fails the
  install did not re-extract, meaning the fingerprint did not change, meaning your new
  file did not land where you think.
- Adding the asset trips a `pkg/architecture` limit. A markdown asset should not, but
  if it does, report it rather than restructuring a package.

## Maintenance notes

- **The recommendation conditional is the feature.** Reject any change that drops the
  restraint clause while keeping the recommendation, or makes the recommendation
  unconditional. The behavior is conditional by design.
- **This asset ships to every forebrain user; `.claude/skills/run-forebrain/` and
  `scripts/acceptance/` are this repository's.** The asset names them once each, as
  the shape to imitate, and assertion 11 keeps their invocation recipes out. If
  forebrain's own driver interface changes, the subcommand list in the "Build a handle
  you can repeat" section should be re-checked — it is the only place in the asset that
  will rot.
- **The forebrain facts in this asset are load-bearing and will drift.** Trust gating
  (`pkg/skill/roots.go:52-60`), the four project roots (`:21-26`), draft redirection
  (`pkg/skill/versions.go:829-873`), the frozen catalog (`pkg/run/skills.go:35-132`),
  the absence of a browser tool, `make test`'s exact form, and the `pkg/architecture`
  caps. If any changes, this asset must change in the same commit — it states them as
  facts to a model that will act on them.
- **The discovery probe is duplicated by design** across this asset and plan 354's
  generator (and, after plan 355, `audit`'s playbook). `pkg/skill/roots.go:21-26` is
  the source of truth; if a fifth project-skill directory is added there, every copy
  changes together.
- **Interaction with plan 354**: the generator produces project-specific instances of
  this skill and its template mirrors `.claude/skills/run-forebrain/`'s outline. If this
  asset's recommendation wording or draft/publish wording changes, plan 354 must stay
  consistent — the two describe one lifecycle from opposite ends.
- **Interaction with plan 355**: plan 355 wires a third copy of the recommendation into
  `audit`, for the case where a *finding* can only be confirmed by running the app.
  That copy must not duplicate this one's trigger.
- **Deliberately not done**: no `verify`-style PASS/FAIL/BLOCKED verdict skill, no
  runtime evidence capture (recordings, screenshots) and no storage location for such
  evidence. forebrain has none of that today; adding it is a plan of its own.
