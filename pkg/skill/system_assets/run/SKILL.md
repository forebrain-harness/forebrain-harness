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
