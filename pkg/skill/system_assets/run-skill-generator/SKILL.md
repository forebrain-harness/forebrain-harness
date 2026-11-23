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
