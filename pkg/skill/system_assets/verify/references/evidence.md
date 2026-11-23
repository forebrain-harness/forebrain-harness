# Evidence

A verdict is checkable only if someone else can open what it rests on. This file
says where that material goes, what each surface produces it with, and what does
not count as any of it.

## Where evidence goes

Write it under `<StateRoot>/state/verify/<session-id>/`, one subdirectory per
verification run: `<StateRoot>/state/verify/<session-id>/<run>/`. `StateRoot` is
the workspace the session runs in — the same root `shell` spools large output
under (`state/tool-output`) and the capture tool writes images under
(`state/capture`). Staying in that family is the whole point: one convention,
one place to look, nothing to invent.

Not the repository. Evidence is not a deliverable, and a capture committed to the
tree puts a picture of somebody's screen under version control permanently.

Not under `.forebrain`. The sandbox denies command writes there, so a `shell`
redirection into it fails — and a write that quietly did not happen is worse
than no evidence at all, because the verdict is then resting on nothing.

## What to keep per surface

One row per surface, and the command that produces the row:

| Surface | What you keep | How it is produced |
|---|---|---|
| Terminal UI | the screen as plain text, and the same dump with attributes when colour, bold or a highlight is the question | `tmux capture-pane -p -t <pane>`, `tmux capture-pane -p -e -t <pane>` |
| Web frontend | the response bytes, or a capture when what it looks like is the question | `curl -sS -D - '<url>'`, and the capture tool's `browser` mode |
| Server or API | the request and the response, plus the tail of the server's log | `curl -sS -D - -X <method> '<url>'`, and the `process` tool's `log` action |
| CLI | stdout, stderr and the exit code | the command with its streams redirected into the run directory, and the exit status recorded beside them |
| Database | the query and the rows it returned | the client's non-interactive form, output redirected into the run directory |

An empty response body is a result; a connection refused is a result. Redirect
the bytes, do not retype them: a transcript of your own reading is the one thing
the reviewer cannot check.

## Naming

`NN-<criterion-slug>.<ext>`, numbered in the order the criteria were written
down before anything was driven: `01-login-returns-200.http`,
`02-dashboard-renders.png`, `03-no-processes-left.txt`. When the material is a
transcript rather than the artifact itself, it gets a companion
`NN-<criterion-slug>.txt` holding the command, its exit status and its raw
output.

The number is the criterion's place in the list, not the order the files happened
to appear. Evidence nobody can locate is evidence that does not exist: someone
holding the verdict walks the criteria and opens one numbered file per criterion
without having to ask where it went.

## Handing it over

`present_files` is how a file reaches the user as a card, and it registers the
file in `fb_artifacts` — a record the conversation can offer again later.

Evidence is normally not a deliverable, so present only the one or two files the
verdict actually rests on: the capture that shows the broken layout, the log line
that says 500. Presenting the whole run directory buries the two that matter
under the twenty that do not. Large text output belongs in the spool: name its
path in the verdict and let whoever needs it open it.

## Retention

Evidence lives in the session's state directory and is cleaned up with it. There
is no retention policy, no rotation job and no configuration knob here — do not
add one. A reviewer who needs the material longer copies it out and says where it
went; the session directory is a working area, not an archive.

## What is not evidence

- A passing test suite. That is a gate, and it was green before the change was
  observed in anything running.
- A capture nobody can open — a path that does not exist, a file the reviewer
  cannot read, an image promised in the verdict and never written.
- A claim about what the code does. A reading of the source is a reading of the
  thing the change already changed.
- A log from a previous run. Its timestamps belong to a build that is not the one
  under verification.

## When you could not keep any

The BLOCKED path still owes evidence: the command that failed, its exact error
text, and what would have been captured had it worked. Quote the error —
"it timed out" is a paraphrase nobody can act on.

An empty run directory with a note explaining why it is empty is honest, and it
is the BLOCKED verdict's own material. A directory filled with something
adjacent — an earlier run's capture, a test summary — is not, and filling one is
the failure this file exists to make visible.