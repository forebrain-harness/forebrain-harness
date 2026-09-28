---
name: skill-generator
description: Turn the workflow that just worked in this session into a reusable project skill. Use when the user asks to keep, save, capture, or formalize the steps just done — "固化这套步骤", "save this workflow as a skill", "turn what we just did into a skill", "把这个流程存下来", or when they accept an offer to keep a run's steps. Writes <project>/.forebrain/skills/<name>/SKILL.md. Not for researching, editing, or benchmarking an existing skill (use /skills), and not for ordinary "save the file" requests.
---

# Skill Generator

Capture the steps that just worked in this session as a project skill, so the
next session starts with them already written down.

The material is **this conversation**. You already did the work; do not go
exploring the repository again, and do not re-run commands to "verify" what you
just did. What matters is what you learned while doing it.

## Step 0: confirm there is a project root

The skill is written into the project's own skills directory:

```
<project-root>/.forebrain/skills/<name>/SKILL.md
```

`<project-root>` is the repository you are working in — the version-controlled
root of the current session. If this session has no version-controlled project
root, say so in one sentence and stop: there is nowhere to put the skill, and
writing it somewhere else would leave it in a place the next session never
looks.

The skill becomes visible from the **next** session onward: the skills catalog
sits in the cached prompt prefix, so this session keeps the one it started with.
The file is on disk immediately, and `/<name>` reads it in this session too.

## Step 1: Extract the reproducible part

Read back over this session and pull out what someone else would need to repeat
the result:

- The commands that mattered, with their arguments and flags.
- The paths and directories that had to be used — the right working directory,
  the ignored scratch dir, the environment file.
- Preconditions: what must exist, be installed, be running.
- Wait and retry conditions: the port that has to be free, the log line that
  means it is ready, the timeout that was too short.
- The failures you hit and how you got past them. This is usually the most
  valuable part; it is exactly what is invisible from a clean checkout.
- How you knew it worked: the assertion, the output, the observable state.

Leave out anything that was true only this once — a ticket number, the branch
name, a temporary directory, the specific bug you were fixing. The skill is
about the *procedure*, not the incident.

## Step 2: Remove what must not be published

A skill is committed to a repository and read by whoever works in it next.
Replace, never paste:

- Secrets, tokens, API keys, passwords → a placeholder plus where the real value
  should come from.
- Personal absolute paths (`/Users/<name>/...`, an internal hostname) → a
  relative path or an environment variable.
- Customer or personal data.

If a step only works with a specific credential, write the step and say which
environment variable or config key holds it.

## Step 3: Name it

- Lowercase letters, digits and hyphens only; at most 64 characters.
- It must be the directory name and the frontmatter `name`, spelled the same.
- It becomes the slash command `/<name>`, so it must not collide with a built-in
  command (`/status`, `/plan`, `/skills`, …). If your name matches one, the
  skill would answer to that command instead — pick another name.
- Prefer a name that says what the procedure is for (`tui-live-verify`,
  `gateway-dual-project-test`), not when it was written (`fix-2026-09`).

## Step 4: Write the files

```
<project-root>/.forebrain/skills/<name>/
├── SKILL.md          required
├── scripts/          optional — deterministic steps, ready to run
└── references/       optional — detail that does not fit the body
```

`SKILL.md` needs frontmatter with both `name` and `description`. The
description is how the skill is found: it is the line in the skills catalog and
the text of `/<name>`, so it says when to use the skill, concretely. A file
without a description is refused, because it would be invisible everywhere.

The body carries, in order: preconditions, the steps, why each non-obvious step
is done that way, the failure modes and how to avoid them, and how to verify
success. Anyone following it should be able to reproduce the result without
re-deriving the parts that were hard the first time.

Rules of thumb that keep it usable (the full guide is
`references/authoring.md` in the skill-workshop skill):

- Under 500 lines. Longer means split detail into `references/`.
- If a step is a command that will be typed again, put it in `scripts/` instead
  of describing it — the next run should execute it, not rewrite it.
- Prefer the imperative. Explain why for anything surprising.

## Step 5: Check it and report

Read the file back and confirm:

- The frontmatter parses, and has both `name` and `description`.
- `name` matches the directory name.
- The description says when to use it, in the words someone would actually think
  in.

Then say so in one sentence: which file you wrote and that it takes effect in
the new session. If the write was refused, fix what the error names and write
again rather than reporting the failure as the outcome.

## Arguments

An argument names the skill or narrows what to capture:

- `/skill-generator tui-live-verify` — use that name.
- `/skill-generator only the fake-provider part` — capture just that part of the
  session.
