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
