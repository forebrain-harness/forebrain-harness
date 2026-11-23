# Plan 354: Add a user-only `run-skill-generator` system skill that writes and drafts this project's run skill

> **Executor instructions**: Follow this plan step by step. The complete final text
> of every file you create is inlined below — install it **exactly as written**. Do
> not consult, download, decompile, `strings`-dump or transcribe any third-party
> binary or product; nothing in this plan requires it. Run every verification command
> and confirm the expected result before moving to the next step. If anything in the
> "STOP conditions" section occurs, stop and report — do not improvise. When done,
> update the status row for this plan in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 9b5c4891..HEAD -- pkg/skill/system_assets pkg/skill/system_test.go pkg/skill/skill.go pkg/skill/activation.go pkg/skill/catalog.go`
> If any in-scope file changed since this plan was written, compare the "Current
> state" excerpts against the live code before proceeding; on a mismatch, treat it as
> a STOP condition.

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED — a system skill that instructs the model to write files into the user's repository; correctness depends on the draft/publish wording being exact
- **Depends on**: `plans/356-skill-model-invocation-gate.md` (the `model-invocation: false` key). Execute after `plans/353-run-system-skill.md` so the wording the two skills share stays consistent.
- **Category**: feature (skill system)
- **Planned at**: commit `9b5c4891`, 2026-11-22
- **Review 2026-11-23 (`review-plan plans/353-365`, baseline `6541b8ab`)**: one
  BLOCKER and five defects fixed in place. ① The inlined SKILL.md `description:`
  contained `User-invocable only: its …` — an unescaped colon-space in a plain
  YAML scalar. Proven with the repo's own parser (`go.yaml.in/yaml/v2`):
  `yaml: line 4: mapping values are not allowed in this context`. Installed
  verbatim it would fail `Parse`, so the skill would never exist and Step 8's
  first assertions would fail. Now an em dash; the frontmatter parses (4 keys).
  ② `FOREBRAIN_ACCEPT_HOME` does not exist in `scripts/acceptance/driver.sh`; the
  switch is `FOREBRAIN_RUN_WORK` (`:13`). ③ `references/drivers.md` taught
  `"$BIN" --project "$PROJ_DIR"`; forebrain has no `--project` flag
  (`cmd/forebrain/root.go:27,68-69` — `cobra.NoArgs`, `--home`, `--yolo`); the
  project is the working directory, and the example now uses a subshell `cd`.
  ④ Step 8's gate said "three tests"; `-run TestRunSkill` matches **two**.
  ⑤ The `^| ` = 5 explanation counted the `|---|` separator, which does not match.
  ⑥ The template's `description:` placeholder modelled the same colon trap; fixed,
  and the rule is now stated in the asset's frontmatter guidance. Line-count gates
  re-measured after the edits: SKILL.md **214**, drivers.md **124**, template.md
  107 unchanged; every other gate re-counted and unchanged.

## Provenance — read first, it constrains everything below

The *behavior* implemented here — a user-invoked generator that produces a
project-specific run skill, refuses to write about an app it never launched, treats
every claim as something to disprove, refines an existing skill rather than rewriting
it, and leaves publishing to the user — was studied from how agent tools structure
this workflow. **All text in this plan, and all text in the assets it produces, is
original and authored for this repository.**

Hard requirements on you:

- Do **not** extract, quote, or closely paraphrase prompt text from any third-party
  product, binary, package or website. There is no carve step and no hash gate here,
  because there is no source text to match.
- Do **not** go looking for a "more faithful" version of the inlined text. The inlined
  text *is* the deliverable. If you think a sentence is wrong, report it.
- The structural model for `template.md` is **this repository's own** run skill,
  `.claude/skills/run-forebrain/SKILL.md`, which is project-authored. Read its heading
  outline; do not copy its forebrain-specific commands into a template that ships to
  every project.

## Why this matters

Plan 353's `run` skill ends by recommending `/run-skill-generator` whenever getting
the app to run took work the project had not recorded. This plan creates the thing
being recommended. Without it, that recommendation names a command that does not
exist, and the work an agent did — the installs, the environment variables, the
configuration patches, the stub provider, the driver it wrote — evaporates when the
session ends and the next agent redoes it from scratch.

Two properties define this skill, and both come from forebrain's own mechanics:

1. **It is user-invocable only.** Its deliverable is committed files in the user's
   repository, so the model must never pick it up on its own initiative. That is what
   `model-invocation: false` (plan 356) implements: the skill is absent from the
   catalog the model reads and refused by the `skill` tool, while `/run-skill-generator`
   keeps working.
2. **It writes a draft and never publishes.** forebrain redirects writes aimed at
   `<project-root>/.forebrain/skills/` into `.drafts/`, drafts are inert because
   discovery skips any path leaf beginning with `.`, and publishing is a user action
   reachable from the web skill page or the `/skills` panel. A published skill becomes
   visible from the **next** session, because the catalog sits in the cached prompt
   prefix and a running session keeps the one it started with.

forebrain already has a general sibling: `pkg/skill/system_assets/skill-generator/`
captures *this session's workflow* as a draft skill. This one is the specialist — it
produces a run skill specifically, with a template, a definition of done that requires
having actually launched the app, and driver patterns. The two are complementary:
`skill-generator` is general and conversational; `run-skill-generator` is narrow and
evidence-driven, and refuses to describe an app it never ran.

## Current state

### What plan 356 must already have landed

- `Skill.ModelInvocation *bool` with tag `yaml:"model-invocation,omitempty"` in
  `pkg/skill/skill.go`.
- `Activation.ModelInvocation bool`, nil-means-yes, in `pkg/skill/activation.go`.
- `mergedSkillEntry.modelInvocation` carried through `pkg/skill/skillrt.go` `collect()`.
- `CatalogEntries` (`pkg/skill/catalog.go:120-147`) skipping entries where it is false.
- `RegisterSkillTool` (`pkg/run/skills.go:171`) refusing such a name with the same
  message as an unknown name.

If any is absent, STOP: `Parse` (`pkg/skill/skill.go:74-109`) uses a non-strict
`yaml.Unmarshal`, so an unrecognized frontmatter key is **silently dropped** and this
skill would be model-invocable — the exact behavior the flag exists to prevent.

### What plan 353 must already have landed

`pkg/skill/system_assets/run/SKILL.md`, whose closing paragraphs recommend
`/run-skill-generator` and state the publish boundary. This plan's asset describes the
same lifecycle from the other end, so the two must agree on: the draft path, who
publishes, when a published skill becomes visible, and the four project-skill
directories.

### The forebrain facts the asset teaches — all verified in this repository

- **Project skill resolution**: `projectSkillDirs` (`pkg/skill/roots.go:21-26`) is
  `.forebrain/skills`, `.agents/skills`, `.claude/skills`, `.codex/skills`, highest
  priority first. `projectSkillRootsForRoot` (`:125-136`) builds exactly those four at
  the session's project root — **no upward walk to the checkout root, no descent into
  subdirectories**. Consequence: a skill written under `apps/billing/` is never loaded
  from a session rooted at the repository root.
- **The model can already look at a saved image.** `read_file` returns image parts
  for png/jpeg/gif/webp and rendered PDF pages (`pkg/tool/file_tools.go:391-437`,
  `:634-650`, shipped in `19239c1d`). There is still **no capture tool**, which is
  why `references/drivers.md` talks about driver-saved screenshots rather than about
  looking at the screen.
- **Trust gating**: `AgentSkillRoots` (`:52-60`) includes project roots only when
  `launch.IsTrusted()`.
- **Draft redirection and its limits**: `VersionedSkillLayers`
  (`pkg/skill/versions.go:849-873`) covers `<projectRoot>/.forebrain/skills`,
  `<workspaceRoot>/skills`, `<home>/skills`. The comment at `:829-836` states the other
  roots "belong to other tools or ship read-only, so writes aimed at them are not
  redirected". So writing `<project-root>/.forebrain/skills/run-x/SKILL.md` lands in a
  draft, while editing `.claude/skills/run-x/SKILL.md` is a real in-place edit.
  Errors: `ErrVersionStoreReadOnly` (`:882`), `ErrSkillDirDeletion` (`:888`),
  `ErrDraftNotPublished` (`:894`).
- **Drafts are inert**: `pkg/skill/discover.go:77` skips any leaf beginning with `.`;
  `draftsDirName = ".drafts"` (`pkg/skill/versions.go:26`).
- **Naming**: a `description:` is mandatory — skipped on disk
  (`pkg/skill/discover.go:96-98`), refused at creation (`:122-131`,
  `ErrMissingDescription`). The name becomes `/<name>` via `NormalizeToken`
  (`pkg/skill/skillmeta.go:19-24`: lowercase, `_`→`-`, whitespace→`-`). Builtin command
  names a skill must not collide with: `agent clear compact config connect context exit
  fork goal init lsp mcp memories migrate model new peers permissions plan rename
  resume rewind sandbox skills status subagents` (`pkg/turn/slash.go`). On a token
  collision the first-discovered skill wins (`pkg/turn/skills.go:33-38`).
- **Bundled files need no registration**: `renderSkillActivation`
  (`pkg/skill/activation.go:97-118`) plus `listSkillResources` (`:120-166`; max 128
  entries, depth 5, skips dot-dirs, dot-files, symlinks and `SKILL.md`) emit every
  non-dot file's relative path in a JSON `<context>` block, and `catalogUsage`
  (`pkg/skill/catalog.go:32-37`) tells the model to resolve relative paths against the
  skill's own directory. So `[template.md](template.md)` and
  `references/drivers.md` work with no code change.
- **The install mechanism**: `//go:embed system_assets/*` (`pkg/skill/system.go:14`);
  `Install` (`:24-65`) re-extracts whenever `Fingerprint()` (`:69-84`) changes;
  destination `$FOREBRAIN_HOME/skills/.system/`. No registry.
- **Verification gates**: `make test` is `CGO_ENABLED=1 go test -tags fts5 ./... -count=1`
  (Makefile:103-104); a bare `go test ./...` fails on pdfium on macOS.
  `pkg/architecture` caps packages at **34 production files**
  (`maxProductionFilesPerPackage`, `cache_test.go:1532`; the doc comment at
  `:1461-1462` still says 30 and is stale) and file names at **two underscores**
  (`:1435-1457`).
- **The driving surface**: forebrain has **no browser or screenshot tool**. An agent
  drives a UI through the `shell` tool — tmux pane capture, terminal replay, a
  headless browser driven from a script, a dump of rendered cells — and reads a
  picture a driver saved through `read_file` (see above). This is why a run skill
  almost always needs a driver script, and why `references/drivers.md` exists.
- **The in-repo harness to imitate**: `scripts/acceptance/` — `driver.sh` (usage at
  `:145-160`: `build`, `start [hang|stream|reply|drip] [answer-text] [drip-delay]`,
  `submit`, `send`, `key`, `wait <text> [secs]`, `screen`, `messages|title|sid`,
  `home|proj`, `db <sql>`, `provider-log`, `stop|reset`), `fake_provider.py`,
  `fake_mcp_stdio.py`, `flaky_proxy.py`, `live_subagent.sh`, `multi_replica_client.py`,
  `web_e2e.sh`, `ws_slash.py`, `native_advisor_workflows.sh`. It runs against an
  **isolated home and project directory** (`FOREBRAIN_RUN_WORK` is the switch;
  `home`/`proj` print the two directories under it), so a run cannot touch real state.
- **The structural exemplar**: `.claude/skills/run-forebrain/SKILL.md` (511 lines),
  outline `## Prerequisites` / `## Run (agent path)` / `### Reading the result` /
  `### A worked example: …` / `## Run: <mode>` / `## Run (human path)` / `## Gotchas` /
  `## Troubleshooting` / `## Test suite`. Its prerequisites are one exact install line
  for the OS verified (`brew install tmux` with "`screen` is NOT a substitute") plus an
  explicit statement of what is *not* needed ("no API key, no network"). Its loudest
  gotcha: "**Rebuild after changing Go code, before you drive it** … a stale binary runs
  the old build and your change looks like it did nothing at all."
- **Authoring rules live in one place**: `TestSkillAuthoringRulesLiveInOnePlace`
  (`pkg/skill/system_test.go:509-552`) asserts `skill-workshop/references/authoring.md`
  holds the writing rules ("Anatomy of a Skill", "Progressive Disclosure", "500 lines")
  and that `skill-workshop/SKILL.md` and `skill-generator/SKILL.md` each point at
  `references/authoring.md` while restating none of them. **Do not add
  `run-skill-generator` to that test's loop** — Step 8 explains why and what takes its
  place.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Format | `gofmt -l pkg/skill` | no output |
| Vet | `go vet ./pkg/skill/...` | exit 0 |
| Skill tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... -count=1` | ok |
| Full suite | `make test` | exit 0 |

## Scope

**In scope** (the only files you should modify):

- `pkg/skill/system_assets/run-skill-generator/SKILL.md` (create)
- `pkg/skill/system_assets/run-skill-generator/template.md` (create)
- `pkg/skill/system_assets/run-skill-generator/references/drivers.md` (create)
- `pkg/skill/system_test.go` — `topLevelDirs` (Step 7) and one new test (Step 8)

**Out of scope** (do NOT touch, even though they look related):

- `pkg/skill/system_assets/run/**` — plan 353. Read it for consistency; never edit it.
- `pkg/skill/system_assets/skill-generator/SKILL.md` — the general generator. Its
  wording is the model for the draft/publish paragraphs here. Do not "unify" the two.
- `pkg/skill/system_assets/skill-workshop/**` including `references/authoring.md`.
  This skill ships its own `template.md` and must neither restate the workshop's
  authoring rules nor be redirected at them.
- `pkg/skill/system_assets/audit/**` (plan 355) and `pkg/skill/system_assets/review/**`
  (plan 355 **deletes** it).
- `.claude/skills/run-forebrain/**` and `scripts/acceptance/**` — exemplars. Read; never
  write.
- `pkg/skill/{skill,activation,skillrt,catalog,versions,discover,roots}.go`,
  `pkg/run/skills.go`, `pkg/turn/skills.go` — **no Go logic changes at all**. Plan 356
  already made the only one needed.
- `pkg/skill/system.go`, `Makefile`, `NOTICE`, `LICENSE`, `README.md`, `docs/**`.

## Git workflow

- Branch: `advisor/354-run-skill-generator`
- Conventional Commits, lowercase imperative, optional `(scope)`, matching recent
  history (`feat(config): \`a\` draws the whole agent form, and \`s Save\` writes it
  once`). Use e.g.
  `feat(skill): a user-only generator that drafts the project's run skill`.
- Do NOT push and do NOT open a PR unless the operator instructed it.

## Steps

### Step 1: Confirm the dependencies landed

```bash
grep -n "ModelInvocation" pkg/skill/activation.go pkg/skill/skill.go
grep -n "model-invocation" pkg/skill/skill.go
ls pkg/skill/system_assets/run/SKILL.md
grep -n "modelInvocation" pkg/skill/catalog.go pkg/skill/skillrt.go
```

**Verify**: every grep prints at least one line and the `ls` succeeds. Any miss is a
STOP condition.

### Step 2: Create the directory

```bash
mkdir -p pkg/skill/system_assets/run-skill-generator/references
```

**Verify**: `ls -d pkg/skill/system_assets/run-skill-generator/references` prints the path.

### Step 3: Write `run-skill-generator/SKILL.md`

Create the file with **exactly** this content. Original prose, authored for this
repository, written against forebrain's actual skill lifecycle. Install verbatim; do
not add sections, do not "improve" it, do not source any of it from elsewhere.

````markdown
---
name: run-skill-generator
model-invocation: false
argument-hint: "[what the run skill should cover]"
description: Write or improve this project's run skill — the project skill that tells an agent how to build, launch and drive this project's app. Use when the user asks to set the project up, get it running, write run instructions, capture how to drive it, or check that its build and run steps still work from a clean environment. User-invocable only — its deliverable is committed files in the repository, so the model never loads it on its own initiative.
---

# Generate the project's run skill

Your job is to produce a skill that lets a future agent build, launch and **drive**
this project from a clean machine — and to have proven every line of it by doing so
yourself first.

The difference between a run skill and a README is evidence. A README says what the
author believed; a run skill says what you executed, on which machine, with which
commands, and what came back. **If you did not run it, do not write it.**

## Where the skill goes

Write it as a **draft** at:

```
<project-root>/.forebrain/skills/.drafts/run-<project-name>/
  SKILL.md            ← the instructions. Keep it short; point at the driver.
  driver.sh           ← or driver.py, smoke.sh, or nothing at all when the app
  stub_provider.py       needs no driver and a shell one-liner is enough
```

forebrain resolves project skills in four directories at the session's project root —
`.forebrain/skills`, `.agents/skills`, `.claude/skills`, `.codex/skills`, highest
priority first. It does not walk up to the checkout root and it does not descend into
subdirectories, so **a skill written under `apps/billing/` is never loaded from a
session rooted at the repository root**. A repository with several apps therefore gets
**one** run skill at the root with a `## Run: <app>` section per app, not one skill per
app. If it is not clear which apps the skill should cover, ask the user rather than
guessing.

Name the directory by slugifying the project: lowercase, digits and hyphens, no
slashes. The directory name and the frontmatter `name:` must match — that is the slash
command. Two rules beyond that: forebrain normalizes a name by lowercasing it and
turning `_` into `-`, and the name must not collide with a builtin command (`agent`,
`clear`, `compact`, `config`, `connect`, `context`, `exit`, `fork`, `goal`, `init`,
`lsp`, `mcp`, `memories`, `migrate`, `model`, `new`, `peers`, `permissions`, `plan`,
`rename`, `resume`, `rewind`, `sandbox`, `skills`, `status`, `subagents`). A skill with
no `description:` is refused when it is created and skipped when it is discovered, so
it would be invisible everywhere.

Project skills load only in a **trusted** project; if the project is not trusted, say so
instead of writing a skill nobody will see.

**Never publish it yourself.** A draft under `.drafts/` is inert: discovery skips any
directory whose name begins with a dot. Publishing is the user's action — the Publish
button on the web skill page, or the `/skills` panel in the terminal — and a published
skill is visible from the **next** session onward, because the skills catalog sits in
the cached prompt prefix and a running session keeps the one it started with.

If Step 0 finds an existing run skill in `.claude/skills/` or `.agents/skills/`, refine
it **in place there**. forebrain does not redirect writes to those directories — they
belong to other tools — so an in-place edit is a real edit, with no draft and no
publish step. Copying it into a forebrain draft would leave the project with two
runbooks to keep in sync.

## Process

### Step 0 — Look for an existing run skill

```bash
for d in .forebrain .agents .claude .codex; do
  grep -Hm1 '^description:' "$d"/skills/*/SKILL.md 2>/dev/null
done
```

Users name these variously, so match on the description, not the name. If one covers
this app: **refine, don't rewrite.** Verify its claims by running them, fix what is
wrong, add what is missing, preserve what works, keep its existing name, and re-run its
driver if it has one. A skill that fails on mechanics unrelated to your task is stale,
not useless — repair it and say what had rotted.

### Step 1 — Survey, and distrust what you read

Read `README.md`, the manifest's script or task list (`Makefile`, `justfile`,
`package.json`, `pyproject.toml`, `go.mod`), `Dockerfile`, `docker-compose.yml`,
`CONTRIBUTING.md` and `.github/workflows/`. **CI configuration is usually more accurate
than the README** — it has to work unattended, so it does not drift the way prose does.

Treat every claim as something to disprove, not something to transcribe:

| Claim | What to actually check |
|---|---|
| "Requires macOS" or "Requires Windows" | Does it, or does it only require a tool that happens to be installed there? Name the real dependency. |
| "Requires a GPU" | Is the GPU needed to *run* the app, or only to train? A run skill usually needs neither. |
| "Requires an account / a paid key / a feature flag" | Can a stub stand in for it? If yes, the skill needs no credentials at all. |
| "Just run `npm start`" | Run it. Note what it needed first — a build, an install, a migration, an environment variable, a running dependency. |

If you give up here, the skill you write is the README with extra steps.

### Step 2 — Get it running, and take notes

Find out where you are before you write anything about it: `uname -s`, the package
manager present, the runtime versions. Write only what you verified on the machine you
are on, and say which machine that was. A prerequisite list that mixes `apt-get` and
`brew` lines helps nobody.

Keep a scratch `NOTES.md` outside the skill directory while you work: every command you
ran, every error, every fix. That file is the raw material for `## Gotchas` and
`## Troubleshooting`, and it is the only record of the fight that you will not remember
an hour later.

Launch the app. Whatever it needs — a display server, a database, a message broker, a
model provider — either supply it or stand up a fake:

- a local stub speaking the same protocol, returning scripted responses, so the run
  needs no credentials, no network and no money and reproduces exactly
- scripted **modes** on the stub, so the driver chooses the scenario — a normal reply, a
  slow stream, a tool call, an approval prompt, a rate limit, a dropped connection —
  instead of hoping for it
- environment variables for thresholds and delays, so an edge case that normally takes an
  hour can be provoked in seconds
- an **isolated home and project directory** for the run, so nothing it writes reaches
  real state; point `FOREBRAIN_HOME` at a scratch directory and wipe it afterwards

`references/drivers.md` has the patterns for each app shape, and forebrain's own
`scripts/acceptance/` is a working example of all four.

### Step 3 — Write the driver

A driver is a small script with a subcommand per thing an agent needs to do: build,
start, drive one step, read the current state, tear down. forebrain has no browser tool
and no screenshot tool, so an agent observes a UI through the `shell` tool — which means
the driver's job is to turn "look at the app" into "print something readable".

Keep it dumb and explicit. Prefer a mode argument over environment-variable soup, print
what it did, exit non-zero on failure, and make `stop`/`reset` idempotent. Put it beside
the skill's `SKILL.md` and reference it by a path relative to the project root.

### Step 4 — Write SKILL.md from the template

Copy [template.md](template.md) into the draft directory and fill it in. Its section
order is the one that works: prerequisites, the agent path, how to read the result, a
worked example, per-mode sections, the human path, gotchas, troubleshooting, test suite.

Two things get this wrong most often:

- **Paths inside the generated SKILL.md must be the published paths**, relative to the
  project root: `.forebrain/skills/run-<project-name>/driver.sh`. You are writing into
  `.drafts/`, and publishing moves the directory — a SKILL.md that names
  `.forebrain/skills/.drafts/run-<project-name>/driver.sh` points at a path that stops
  existing the moment the user publishes.
- **The frontmatter is not decoration.** `name:` becomes the slash command and must equal
  the directory name; `description:` is what the model scans to decide whether the skill
  applies, so put the verbs a user would actually type in it — "run", "start", "build",
  "drive", "screenshot", "smoke test". A generic description ("helpful utilities for this
  project") matches nothing. Keep `description:` one line with **no unescaped colon-space**
  inside it: the frontmatter is plain YAML, and `only: its` inside an unquoted scalar is a
  parse error, so the skill is refused at creation and silently skipped by discovery. Use
  an em dash or rephrase.

### Step 5 — Verify from clean

Wipe the isolated home, delete the built artifacts the driver caches, and run the skill's
own commands in order, exactly as written, on a machine that has not just been through
Steps 2–4. Every command must work. Anything you have to fix, fix in the skill, not in
your head.

Then check the skill's claims against reality one more time: does it name the OS it was
verified on, does every path resolve, does the description contain verbs, does `name:`
match the directory.

### Step 6 — Report

Read the draft back, confirm the frontmatter parses and carries both `name` and
`description`, and leave the user **one** line: where the draft is, and that publishing it
makes it live from the next session. Do not open or push any panel, and do not publish it.
If a write was refused, fix what the error names and write again rather than reporting the
refusal as the outcome.

## Definition of done

All of these, or the skill is not done:

- You **launched the app** and drove the change through it. Not: the tests pass, the build
  is clean, the code looks right.
- Every command in the skill is one you ran, and its output is one you saw.
- A clean re-run — wiped home, no cached build — works end to end from the skill's text
  alone.
- Prerequisites name the exact install line for the operating system you verified on, and
  state what is *not* needed.
- The driver, if there is one, is committed beside the skill and referenced by a
  project-root-relative **published** path.
- The draft is written and unpublished. Publishing is the user's.

## What to include, and what to leave out

Include: the exact build and launch commands; the driver's subcommands with a one-line
purpose each; the modes and what each is for; how to read the result; one worked example
end to end; the gotchas that cost you time; a troubleshooting table mapping symptom to
cause; the project's own test and lint gates.

Leave out: architecture explanation, code tours, history, options you considered,
anything the reader can get from the README without running something, and any command
you did not run. Length is a cost — an agent reads this before it starts work, and every
line it does not need is a line it might act on.

## Red flags

Stop and fix the skill if any of these is true:

- It contains a command you did not run.
- Its prerequisites are a generic list rather than the line that worked.
- It describes the app instead of driving it.
- Every step is build, typecheck or run-a-test-file — that is a CI rerun, not a run skill.
- It names a `.drafts/` path as the driver's location.
- It says "should work" anywhere.
- You ran the test suite and called that done.
````

**Verify**:

```bash
f=pkg/skill/system_assets/run-skill-generator/SKILL.md
head -6 "$f"
grep -c "^## " "$f"; grep -c "^### " "$f"
grep -c "for d in .forebrain .agents .claude .codex" "$f"
grep -c "forebrain/skills/\.drafts" "$f"; grep -c "Never publish it yourself" "$f"
grep -c "template.md" "$f"; grep -c "references/drivers.md" "$f"
grep -c "trusted" "$f"; grep -c "scripts/acceptance" "$f"; grep -c "published" "$f"
grep -c "^| " "$f"; wc -l "$f"
```

Expected (these are the counts of the text as authored above — verified, not
estimated):

- frontmatter, in order: `name: run-skill-generator`, `model-invocation: false`,
  `argument-hint:`, one-line `description:`
- `## ` = **5** — `Where the skill goes`, `Process`, `Definition of done`, `What to
  include, and what to leave out`, `Red flags`
- `### ` = **7** — `Step 0` through `Step 6`, all under `## Process`
- discovery probe = 1; `forebrain/skills/.drafts` = **2**;
  `Never publish it yourself` = 1; `template.md` = **1 line / 2 occurrences** (the
  markdown link in Step 4 and the prose beside it share one line — `grep -c` counts
  lines, `grep -o … | wc -l` counts occurrences); `references/drivers.md` = 1;
  `trusted` = **1 line / 2 occurrences** (same reason); `scripts/acceptance` = 1;
  `published` = 4
- table rows `^| ` = **5** (the header and the four claim rows of Step 1's
  distrust table; the `|---|---|` separator starts with `|-`, so it does not
  match `^| ` — six lines start with `|`, five with `| `)
- line count **214** (accept 185–215)

Confirm the heading structure explicitly, since Step 8's test depends on it:

```bash
grep -n "^## \|^### " pkg/skill/system_assets/run-skill-generator/SKILL.md
```

Expected: the twelve headings listed above, in that order. Do **not** add or remove
sections to make a count match — the content is the deliverable. If your file's counts
differ from these, your transcription differs from the text above; fix the
transcription.

### Step 4: Write `run-skill-generator/template.md`

This is the skeleton the generator fills in. Its section order mirrors **this
repository's own** `.claude/skills/run-forebrain/SKILL.md` outline, which is
project-authored and which plans 350/351 proved works in practice. Create it with
**exactly** this content:

````markdown
<!--
Skeleton for a project run skill. Copy into
<project-root>/.forebrain/skills/.drafts/run-<project-name>/SKILL.md, fill it in,
and delete this comment block plus every <angle-bracket> placeholder and the notes
at the bottom before you finish. Anything you did not run does not belong here.
-->

---
name: run-<project-name>
description: Build, launch and drive <project-name> — <the verbs a user would type — start it, run its tests, drive its UI, take a screenshot, smoke-test a change>. <What it covers, in one clause.>
---

# Run <project-name>

<One or two sentences: what this app is, and what "driving it" means here.>

## Prerequisites

```bash
<the exact install line you ran, for the operating system you verified on>
```

<What is already present on a machine that builds this project, and what is **not**
needed — no API key, no network, no database. Say it explicitly; the absence is as
useful as the presence.>

Verified on: <OS and version>.

## Run (agent path)

```bash
D=.forebrain/skills/run-<project-name>/driver.sh

$D build                 <only if a stale build would silently show old behavior>
$D start <mode>          <launch; the mode chooses the scenario>
$D <drive-one-step>      <the smallest action that reaches the change>
$D <read-state>          <print what the app now shows>
$D stop
```

<The mode table, if the driver has modes:>

| Mode | Behaviour | Use it to check |
|---|---|---|
| `<mode>` | <what the stub does> | <the scenario it provokes> |

### Reading the result

<What good output looks like, and what the failure that looks like success is —
a zero exit code with the wrong thing on screen, a cached build, a log line that
appears whether or not the change worked.>

### A worked example: <the change you actually verified>

```bash
<the exact command sequence, in order>
```

<What you observed, and how that confirms the change.>

## Run: <second surface or mode, if there is one>

<One section per distinct way to drive the app — a different frontend, a live
device, a multi-replica setup. Omit the section entirely if there is only one.>

## Run (human path)

<How a person starts it, when that differs from the agent path.>

## Gotchas

- <Every obstacle that cost you time, as a statement of the trap and the
  workaround. Rebuild-before-driving belongs here if the app is compiled.>

## Troubleshooting

| Symptom | Cause |
|---|---|
| `<the exact error or wrong output>` | <what it means and what to do> |

## Test suite

```bash
<the project's own test and lint gates, exactly as CI runs them>
```

<!--
NOTES — delete before finishing.

Frontmatter: `name:` becomes the slash command and must equal the directory name;
forebrain lowercases it and turns `_` into `-`, and it must not collide with a
builtin command. `description:` is what the model scans to decide whether the
skill applies — put the verbs a user would type in it, not a summary of the
project. A skill with no description is refused on creation and skipped on
discovery.

Paths: everything above is relative to the **project root**, not to this
directory, and must be the **published** path
`.forebrain/skills/run-<project-name>/…`. This file is written under `.drafts/`;
publishing moves it, so a `.drafts/` path here breaks the moment the user
publishes.

Driver: if there is no driver because a shell one-liner is enough, delete the
driver lines rather than leaving a placeholder. If the driver grows past a few
hundred lines it probably belongs in the project's own test or scripts directory,
referenced from here.
-->
````

**Verify**:

```bash
t=pkg/skill/system_assets/run-skill-generator/template.md
grep -c "^## " "$t"; grep -c "^### " "$t"
grep -c "run-<project-name>" "$t"; grep -c "forebrain/skills/run-<project-name>" "$t"
grep -c "\.drafts" "$t"; grep -c "NOTES — delete before finishing" "$t"
head -1 "$t"; wc -l "$t"
```

Expected (verified counts of the text as authored above): `## ` = **7**
(`Prerequisites`, `Run (agent path)`, `Run: <second surface or mode, if there is one>`,
`Run (human path)`, `Gotchas`, `Troubleshooting`, `Test suite`); `### ` = **2**
(`Reading the result`, `A worked example: …`); `run-<project-name>` = **4**; the
published path `forebrain/skills/run-<project-name>` = **2**; `.drafts` = **3** (the
header comment plus two warnings in the author notes about the path trap);
`NOTES — delete before finishing` = 1; first line begins `<!--`; line count **107**
(accept 100–130).

Confirm the structure explicitly:

```bash
grep -n "^## \|^### " pkg/skill/system_assets/run-skill-generator/template.md
```

Expected: the nine headings above, in that order — this is the outline the generator
fills in, and it mirrors `.claude/skills/run-forebrain/SKILL.md`'s.

### Step 5: Write `run-skill-generator/references/drivers.md`

This replaces the role that per-app-shape example files play elsewhere: concrete driver
patterns. It is grounded in forebrain's own `scripts/acceptance/` harness, which is
project-authored. Create it with **exactly** this content:

````markdown
# Driver patterns

forebrain gives an agent no browser tool and no screenshot tool. It drives an app
through the `shell` tool and observes it through what a shell can print — and
through `read_file`, which returns a saved png/jpeg/gif/webp as pixels. A driver
exists to turn "look at the app" into "print something readable".

Pick the section for the app's shape. Every pattern below is a sketch to adapt, not
code to paste — the point is the shape, and the point of the shape is that a run
reproduces.

## The shape every driver shares

Subcommands, one per thing an agent needs to do:

- `build` — compile or bundle, so a stale artifact cannot silently show old behavior
- `start [mode]` — launch, with the mode choosing the scenario
- one or more drive steps (`submit`, `send`, `key`, `click`, `request`) — the smallest
  action that reaches the change
- one or more read steps (`screen`, `messages`, `db`, `log`, `provider-log`) — print
  observable state
- `home` / `proj` — print the isolated directories the run is using
- `stop` / `reset` — tear down, and wipe the isolated home; both idempotent

Rules: print what it did, exit non-zero on failure, prefer a mode argument over
environment-variable soup, and never write outside the isolated home.

## Terminal UI

Run the app inside `tmux` and capture the pane:

```bash
tmux new-session -d -s "$S" "$BIN"
tmux send-keys -t "$S" "$TEXT" Enter
sleep 1
tmux capture-pane -p -t "$S" >"$OUT/screen.txt"
```

`capture-pane -p` prints the rendered cells as text, so the read step is `grep`. For
cursor position, cell attributes or wrapped lines, capture with `-e` and parse, or dump
the app's own render state if it exposes one. `screen` is not a substitute for `tmux`
here — it has no equivalent pane dump.

To watch something change over time, capture twice with a sleep between and diff: a line
that grew is proof of streaming, and one capture is not.

## Web frontend

Two paths, and the second is often enough:

- **Through the API behind it.** Start the server, hit the endpoint the page calls, read
  the JSON. This is fast, deterministic and needs no browser.
- **Through a headless browser driven from a script.** Navigate, wait for a selector,
  then print the DOM, the console messages or the network log. Assert on printed text;
  when the question is visual, save a screenshot and `read_file` it — a saved
  png/jpeg/gif/webp comes back as pixels, so the model can look at it.

For a page whose behavior lives in the browser (a key binding, a focus ring, a panel that
opens on a keystroke), only the second path proves anything.

## Server, API or CLI

```bash
"$BIN" --port "$PORT" &>"$OUT/server.log" &
echo $! >"$OUT/pid"
curl -fsS "http://127.0.0.1:$PORT$path" >"$OUT/resp.json"
```

Read the response bytes and the log; do not infer success from the exit code. For a CLI,
capture stdout, stderr and the exit status separately — a tool that prints its error to
stdout and exits 0 is a real failure mode.

## Standing up a fake where the app needs a real service

The single highest-value move in a run skill. A run that needs live credentials, a real
network or a paid account cannot be repeated, and an agent will quietly skip it.

- **Same protocol, scripted responses.** A local HTTP endpoint that speaks the API the
  app already speaks — for a model provider, an OpenAI-compatible `/chat/completions`
  that streams canned deltas. The app cannot tell, and the run needs no key, no network
  and no money.
- **Modes on the stub.** One stub, many scenarios: a normal reply, a slow word-by-word
  stream, a tool call, an approval prompt, a rate limit that clears after N seconds, a
  connection dropped mid-stream, an empty 200. The mode is how the driver *chooses* the
  edge case instead of waiting for it.
- **A stub for the second protocol too.** If the app also speaks MCP, or opens a
  websocket, or polls a queue, stub that as well — a half-stubbed app makes a
  half-verifiable run.
- **A proxy for the network itself.** To test retry, timeout and backoff, put a proxy in
  front that delays, drops or corrupts on command. This is the only reliable way to
  provoke a network failure.
- **Environment knobs for thresholds.** Anything the app waits for or limits — a
  compaction threshold, a retry maximum, a delay — should be settable by an environment
  variable so an edge case that normally takes an hour takes ten seconds.

forebrain's own `scripts/acceptance/` does all of this: `fake_provider.py` (a scripted
model provider with a mode per scenario), `fake_mcp_stdio.py` (a stub MCP server),
`flaky_proxy.py` (the network itself), and `driver.sh` to drive the real TUI against
them. Read it for the shape.

## Isolation

A run that mutates the user's real state is not a test. Give the app a scratch home and a
scratch project directory, print both, and wipe them on `reset`:

```bash
HOME_DIR="$WORK/home"; PROJ_DIR="$WORK/proj"
mkdir -p "$HOME_DIR" "$PROJ_DIR"
(cd "$PROJ_DIR" && FOREBRAIN_HOME="$HOME_DIR" "$BIN")
```

For forebrain itself, `FOREBRAIN_HOME` is the switch that matters: point it at a scratch
directory and the run cannot touch the user's configuration, skills, credentials or state
database. The project is the **working directory** — the binary has no project flag — and
`scripts/acceptance/driver.sh` exposes the two directories as `home` and `proj`
so a failing run can be inspected afterwards.

## The trap that looks like success

A **stale build**. If the app is compiled, a driver that launches a cached binary will
faithfully show the old behavior, and the change under test looks like it did nothing at
all. `reset` and `stop` usually clear state without rebuilding; neither is a substitute
for `build`. Say this in the generated skill's `## Gotchas` — it is the most expensive
false negative a run skill can have.
````

**Verify**:

```bash
r=pkg/skill/system_assets/run-skill-generator/references/drivers.md
grep -c "^## " "$r"; grep -c "scripts/acceptance" "$r"; grep -c "FOREBRAIN_HOME" "$r"
grep -c "tmux" "$r"; grep -c "stale build" "$r"; wc -l "$r"
```

Expected (verified counts of the text as authored above): `## ` = **7** (`The shape
every driver shares`, `Terminal UI`, `Web frontend`, `Server, API or CLI`, `Standing up
a fake where the app needs a real service`, `Isolation`, `The trap that looks like
success`); `### ` = 0; `scripts/acceptance` = **2**; `FOREBRAIN_HOME` = **2**; `tmux` =
**5**; `stale build` = 1; line count **124** (accept 115–145).

### Step 6: Confirm the bundled files are discoverable

No registration is needed, but prove it:

```bash
find pkg/skill/system_assets/run-skill-generator -type f | sort
```

**Verify**: exactly three files — `SKILL.md`, `template.md`,
`references/drivers.md`. Step 8's test asserts the activation `Content` names both
`template.md` and `references/drivers.md`, which is what `listSkillResources`
(`pkg/skill/activation.go:120-166`) produces with no code change.

### Step 7: Register the directory in the install test

Add `"run-skill-generator",` to `topLevelDirs` in
`pkg/skill/system_test.go:323-336` (plan 353 already added `"run"`). `allowedEntries`
(`:345-351`) is derived from that list, so no second edit is needed — but
`TestInstall_AllSkillDirsExtracted` fails on any unlisted top-level directory, which is
why both plans must add their entry.

**Verify**:
`CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... -count=1 -run TestInstall -v` → PASS.

### Step 8: Add the contract test

Add `TestRunSkillGeneratorIsUserInvocableOnly` to `pkg/skill/system_test.go`, modeled on
`TestSystemAssetsCarryTheGeneratorSkill` (`:443-478`): `Install` into a `t.TempDir()`,
then `ValidateSkillDir` and `LoadActivation` against
`<home>/skills/.system/run-skill-generator`.

It must assert:

1. `ValidateSkillDir(<home>/skills/.system/run-skill-generator)` returns nil.
2. `LoadActivation` reports `Name == "run-skill-generator"`.
3. `activation.SlashCommand == true` — `/run-skill-generator` is the only entry point, so
   it must exist.
4. `activation.ModelInvocation == false` — the plan-356 gate is actually set by this
   asset. **This is the assertion that makes the port faithful**; without it the skill is
   one the model can pick up on its own initiative and write files with.
5. `skill.CatalogEntries([]string{<home>/skills/.system}, "")` contains an entry named
   `run` and **no** entry named `run-skill-generator`. The end-to-end proof: the model is
   offered the consumer and never the generator.
6. The activation `Content` names `template.md` and `references/drivers.md` — proof
   `listSkillResources` reached the bundled files with no registration.
7. `Content` contains `.forebrain/skills/.drafts`, `Never publish it yourself`, and
   `publish` — the draft/publish contract. (The existing
   `TestSkillAuthoringRulesLiveInOnePlace` pins the analogous `.forebrain/skills` plus
   "next session" claims for the general generator at `:546-551`; same instinct.)
8. `Content` contains the discovery probe
   `for d in .forebrain .agents .claude .codex`, byte-identical to plan 353's copy in
   `run/SKILL.md`. Assert both files contain the same literal so the two cannot drift.
9. `Content` contains the path-trap warning — assert on `.drafts/run-` appearing in a
   sentence that also contains `published`, or more simply assert that both
   `published path` and `.drafts/` occur. This is the mistake that produces a broken
   skill every time.
10. `Content` contains `trusted` and `scripts/acceptance` — the forebrain-specific
    semantics, same instinct as plan 353's assertion 9.
11. `Content` does **not** contain `brew install tmux`, `term_live`, `paint_dump`, or
    `driver.sh start hang` — recipes from `.claude/skills/run-forebrain/` must not be
    reproduced as general instructions in an asset every forebrain user receives.
12. `references/drivers.md` as extracted on disk contains `FOREBRAIN_HOME` and
    `scripts/acceptance` — the reference doc survived the embed and carries the
    forebrain-specific isolation guidance.

**Do NOT add `run-skill-generator` to the `skillDir` loop in
`TestSkillAuthoringRulesLiveInOnePlace` (`:524`).** That loop asserts each listed skill
points at `references/authoring.md` and restates none of the workshop's rules.
`run-skill-generator` deliberately does neither: it ships its own `template.md` (a
skeleton for a *run* skill) and its own `references/drivers.md` (driver patterns), and
its "Write SKILL.md from the template" section is run-skill-specific structure, not a
second copy of "Anatomy of a Skill" or "Progressive Disclosure". Adding it to that loop
would force a false choice between breaking the test and gutting the content.

**Verify**:
`CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... -count=1 -run 'TestRunSkill' -v`
→ PASS, **two** tests (plan 353's `TestRunSkillDrivesTheAppAndOffersToCaptureIt`
plus the one added here; the probe-identity assertion in
8 covers both files from this test). `-run TestRunSkill` matches exactly these two.

### Step 9: Full gates

1. `gofmt -l pkg/skill` → no output
2. `go vet ./pkg/skill/...` → exit 0
3. `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... ./pkg/turn/... ./pkg/run/... -count=1` → ok
4. `make test` → exit 0
5. `git status --porcelain` → only the four in-scope paths

## Test plan

New test: `TestRunSkillGeneratorIsUserInvocableOnly` in `pkg/skill/system_test.go`,
twelve assertions per Step 8. Structural pattern:
`TestSystemAssetsCarryTheGeneratorSkill` (`:443-478`).

Updated: `TestInstall_AllSkillDirsExtracted` (`:314`) gains `"run-skill-generator"`.

Assertions 4, 5, 9 and 11 are the ones to read first in review. 4 and 5 together prove
the user-only gate end to end — declared in the asset, and absent from the catalog the
model actually sees while `run` is still offered. 9 pins the draft-versus-published path
trap, which is the mistake that silently produces a broken generated skill. 11 pins the
boundary between a built-in asset and this repository's own instance.

Verification: `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... -count=1` → all pass,
including 1 new test.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `pkg/skill/system_assets/run-skill-generator/` holds exactly three files: `SKILL.md`, `template.md`, `references/drivers.md`
- [ ] `SKILL.md` frontmatter contains, in order, `name: run-skill-generator`, `model-invocation: false`, `argument-hint:` and a one-line `description:`; `grep -c "^## "` returns 5, `grep -c "^### "` returns 7; 214 lines (accept 185–215)
- [ ] `grep -c "for d in .forebrain .agents .claude .codex" …/SKILL.md` returns 1
- [ ] `grep -c "Never publish it yourself" …/SKILL.md` returns 1 and `grep -c "forebrain/skills/\.drafts" …/SKILL.md` returns 2
- [ ] `grep -c "references/drivers.md" …/SKILL.md` returns 1 and `grep -c "template.md" …/SKILL.md` returns 1 line with 2 occurrences (`grep -o "template.md" … | wc -l` → 2)
- [ ] `template.md`: `grep -c "^## "` returns 7, `grep -c "^### "` returns 2, `run-<project-name>` appears 4 times, `forebrain/skills/run-<project-name>` 2 times, `.drafts` 3 times, `NOTES — delete before finishing` once; 107 lines (accept 100–130)
- [ ] `references/drivers.md`: `grep -c "^## "` returns 7, `scripts/acceptance` appears twice, `FOREBRAIN_HOME` twice, `tmux` five times; 124 lines (accept 115–145)
- [ ] The frontmatter parses as YAML: `grep '^description:' …/SKILL.md | grep -cE ': .+: '` returns **0** (no second colon-space inside the scalar). Review 2026-11-23 found `User-invocable only: its …` here, which `go.yaml.in/yaml/v2` rejects with "mapping values are not allowed in this context" — the skill would be refused at creation and silently skipped by discovery. The text now uses an em dash; do not restore the colon
- [ ] `grep -rc "brew install tmux\|term_live\|paint_dump\|driver.sh start hang" pkg/skill/system_assets/run-skill-generator/` reports 0 for every file
- [ ] `grep -c "published path" …/SKILL.md` returns ≥ 1 (the draft-versus-published path trap is stated)
- [ ] `"run-skill-generator"` appears in `topLevelDirs` at `pkg/skill/system_test.go:323-336`
- [ ] `gofmt -l pkg/skill` produces no output
- [ ] `go vet ./pkg/skill/...` exits 0
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... -count=1` exits 0, including `TestRunSkillGeneratorIsUserInvocableOnly`
- [ ] `make test` exits 0
- [ ] `git status --porcelain` lists only the four in-scope paths
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- Plan 356 has not landed: `grep -n "ModelInvocation" pkg/skill/activation.go` or
  `grep -n "model-invocation" pkg/skill/skill.go` returns nothing. Without it the new
  frontmatter key is silently dropped by the non-strict `yaml.Unmarshal` in `Parse`
  (`pkg/skill/skill.go:74-109`) and this skill becomes model-invocable.
- Plan 353 has not landed: `pkg/skill/system_assets/run/SKILL.md` is missing. Assertion 8
  compares the two skills' discovery probes and has nothing to compare against.
- You are tempted to fetch, extract or transcribe prompt text from any third-party product
  to make these assets "more faithful". **Stop.** The inlined text is the deliverable and
  originality is a hard requirement. Report the impulse.
- Any forebrain fact this plan asserts does not hold: `scripts/acceptance/driver.sh`
  missing or its usage different, `AgentSkillRoots` no longer gating on
  `launch.IsTrusted()`, `VersionedSkillLayers` now covering `.claude/skills`, publishing
  no longer moving a draft to `<root>/.forebrain/skills/<name>/`, `NormalizeToken` changed,
  the builtin command list changed, or `listSkillResources` no longer reaching
  `references/`. These assets state those as facts to a model that will act on them; a
  stale fact is worse than no fact. Report the discrepancy instead of editing the text.
- `TestSkillAuthoringRulesLiveInOnePlace` fails. It loops only `skill-workshop` and
  `skill-generator`, so a failure means you edited outside scope, or the new text restated
  the workshop's authoring rules. Report which.
- `TestInstall_AllSkillDirsExtracted` names an unexpected directory you did not create (a
  stray file, an editor backup, a `.DS_Store` picked up by the embed). Report what it names.
- Adding `references/drivers.md` trips a `pkg/architecture` limit. A markdown asset should
  not, but if it does, report it rather than restructuring a package.

## Maintenance notes

- **`model-invocation: false` is not a security boundary.** It hides the skill from the
  catalog and refuses it in the `skill` tool. A model that reads `SKILL.md` by path can
  still follow it. If the owner later wants a hard boundary, that belongs in the write path
  (`pkg/skill/versions.go`, `pkg/tool/tool_writes.go`), not here.
- **The draft/publish wording is load-bearing and shared.** This asset, plan 353's `run`
  skill and `pkg/skill/system_assets/skill-generator/SKILL.md` all describe the same
  lifecycle. If forebrain's publish flow changes — a different destination, a mid-session
  catalog refresh, a CLI publish command — all three must change in the same commit. The
  mechanism lives in `pkg/skill/versions.go:829-873` and `pkg/run/skills.go:35-132`.
- **The path trap (assertion 9) will be re-broken by well-meaning edits.** It is natural to
  write the path you are writing to. The generated skill's paths must be the *published*
  ones. Keep the warning in both `SKILL.md` (Step 4) and `template.md` (author notes);
  they fire at different moments.
- **The refine-in-place rule is what keeps this repository's `.claude/skills/run-forebrain/`
  from being duplicated.** If forebrain ever starts draft-redirecting `.claude/skills`
  writes, that rule breaks and a generated copy would land in a draft while the original
  stays live — two runbooks for one app. Re-read `pkg/skill/versions.go:829-836` before
  changing either side.
- **`references/drivers.md` is the file that will rot.** It names `scripts/acceptance/`
  and its stub scripts, `FOREBRAIN_HOME`, tmux pane capture, and the absence of a browser
  tool. If forebrain grows a browser or screenshot tool, the "no browser tool" premise and
  the whole web-frontend section need rewriting — that would be a genuine improvement, not
  a fix.
- **Interaction with plan 355**: plan 355 wires a third copy of the recommendation into
  `audit`, for findings that only a running app can confirm. If this skill's name changes,
  that text changes too.
- **Deferred**: nested per-unit skill discovery. Because forebrain resolves project skills
  at the session's project root only, a multi-app repository gets one root-level run skill
  with a `## Run: <app>` section per app. If that proves too coarse, the follow-up is to
  extend `projectSkillRootsForRoot` (`pkg/skill/roots.go:125-136`) to descend into nested
  skill directories — which drags in trust gating, the frozen prompt prefix and the
  128-entry resource limit. A plan of its own, not an edit here.
