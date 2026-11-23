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
