# Closing the Loop — execute, reconcile, issues

The audit's job doesn't end at the plan. This file covers the three follow-through flows: dispatching an executor and reviewing its work (`execute`), keeping the plan backlog alive (`reconcile`), and publishing plans where work gets picked up (`--issues`).

When the host injects a hand-off contract for an approved plan (the approval card's hand-off flow), that contract is authoritative and this file only supplements it. The procedure below is the same workflow for executing a backlog plan on request.

The founding rule survives unchanged: **the advisor never edits source code.** In `execute`, a *separate executor subagent* edits code in an isolated workspace; the advisor dispatches, reviews, and renders a verdict — like a tech lead who doesn't push commits to your branch.

---

## `execute <plan>` — dispatch and review

### Preconditions (check all before dispatching)

- The project is version-controlled: `git rev-parse --show-toplevel` must succeed (the isolated workspace needs it). If not: stop and say so.
- The plan file exists and its dependencies show DONE in the backlog index. If not: stop, name the missing dependency.
- Run the plan's drift check yourself: its diff command against the planned-at commit plus its status command. If in-scope files changed since the plan was written — committed or not — reconcile the plan first (see below); don't hand a stale plan to an executor.
- Run `git status --porcelain`. A new worktree starts from the last commit, so uncommitted changes in files the plan touches will not be in it. If there are any, ask the user whether to proceed from the last commit or to wait. Never commit, stash or discard their changes yourself.

### Dispatch exactly one executor

Call `subagent_run` with `subagent_type` "general-purpose" and `worktree` set to a short kebab-case name after the plan (the plan file name is the natural source). The dispatch creates `<main worktree>/.worktrees/<name>` on a new branch `<name>` from HEAD and runs the executor inside it: every read, edit, command and commit happens there. Your brief tells the executor it is already in that worktree; never ask it to create one.

Executor model: the one the user named if they named one (`execute 003 <model>`), otherwise the session's model or a configured cheaper model. Never substitute a model or thinking level the user named. If the host cannot dispatch a subagent into an isolated worktree, say so and hand the plan over for manual execution.

The subagent prompt must contain, in order:

1. **The full plan text, inlined, then its file path.** The worktree contains only committed files — if the backlog is uncommitted, the executor can't read it. Never summarize or shorten it.
2. The executor preamble, word for word:

> You are the executor for the implementation plan below. Work only inside the git worktree named above. Follow the plan step by step. Run every verification command and confirm the expected result before moving on. Touch only the files the plan lists as in scope. If any STOP condition occurs, stop immediately and report. Do not improvise around obstacles. A fresh worktree has no installed dependencies or build output: install and build what the verification commands need first. Commit your work in the worktree, on its own branch, following the plan's git workflow when it has one; never push, merge or touch any other branch. Skip any instruction to update a backlog index or the plan file: your reviewer maintains them. Before reporting, audit every claim in your report against an actual tool result from this session; only report what you can point to evidence for, and if a verification failed or was skipped, say so plainly. When finished, reply with exactly the report format below.

3. The report format:

```
STATUS: COMPLETE | STOPPED
WORKTREE: path and branch
STEPS: per step - done/skipped + verification command result
STOPPED BECAUSE: (only if STOPPED) which STOP condition, what was observed
FILES CHANGED: list
NOTES: deviations, surprises, judgment calls
```

Note on fresh worktrees: they share history but not installed dependencies or build output — the executor must install what the verification commands need first, and a check that resolves from a built artifact may need one build even though the plan's command table (recon'd in the main tree) didn't mention it. Expect this; it isn't a deviation.

### Review (the advisor's real job here)

Run your checks inside the executor's worktree; it is isolated and disposable. Review like a tech lead reviewing a change against its spec — never fix anything yourself:

1. **Re-run every done criterion** in the worktree. Don't trust the executor's report — verify.
2. **Scope compliance**, two ways: `git -C <worktree> diff --stat <base>..HEAD` and `git -C <worktree> status --porcelain` against the plan's in-scope list, where `<base>` is the commit the worktree started from. Any file outside scope fails review, full stop.
3. **Read the full diff.** Judge it against "Why this matters" (does it solve the actual problem?) and the project conventions named in the plan (does it look like the rest of the project?). **Every hunk must trace to a plan step; reject any out-of-scope change, however plausible** — an improvement nobody asked for is still a review failure.
4. **Audit the new tests.** Executors game criteria — a test that asserts nothing meaningful proves nothing. Read what the tests assert.

### Verdict

**Documented deviations are judged on merit, not reflex-blocked.** "Do not improvise" exists to stop silent drift; an executor that hits a real obstacle, adapts minimally, and explains it in NOTES has done the right thing. Approve it if the adaptation serves the plan's intent and stays in scope; treat *undocumented* deviations as review failures.

| Verdict | When | Action |
|---|---|---|
| **APPROVE** | Criteria pass in your own run, scope is clean, quality holds | Record the implementation in the plan and the index (see below). Present to the user: diff summary, worktree path and branch, anything from NOTES. |
| **REVISE** | Fixable gaps | `subagent_continue` to the same executor with specific, actionable feedback ("criterion 3 fails: X; the error handling in `api.ts:90` swallows the error — use the Result pattern per the plan"). **Max 2 revision rounds**, then BLOCK. |
| **BLOCK** | A STOP condition was hit, scope was violated beyond repair, or the rounds ran out | Mark the plan BLOCKED in the index with the reason. Refine or rewrite the plan with what was learned. Tell the user what happened and what changed in the plan. |

Running verification commands inside the executor's worktree is fine — it's isolated and disposable. The read-only rule protects the user's working tree, not the worktree.

**Never merge, push, rebase or commit to the user's current branch, and never remove the executor's worktree or branch: merging and cleaning up are the user's decision.**

The user can open the executor's conversation and steer it directly; a ruling that arrives through that channel is the user's own — build on it, never silently redo it.

Hand over every file the executor changed as this turn's deliverables, under the worktree's own path (.worktrees/<name>/...), not the main checkout's copies.

For a bug-fix plan, the reproduction is the first verification step, before any edit; if it does not fail, or the source contradicts the plan's causal chain, stop and report instead of improvising.

### Close the plan loop

Where this session's plan rules allow it, record in the plan file and its index: the verdict, the worktree path and branch, the executor's commits, every deviation and why, and each verification result you ran yourself. Where they don't — a read-only backlog, an index a reviewer maintains — report the same in your reply instead.

Finish with this report structure; omit BLOCKED BECAUSE unless blocked:

```
VERDICT: APPROVE | BLOCK
WORKTREE: path and branch (merging it is the user's decision)
CHECKS: each done criterion - pass or fail, from your own run
SCOPE: files changed, and anything outside scope
BLOCKED BECAUSE: what stopped it
NOTES: deviations, revision rounds, anything left unresolved
```

---

## `reconcile` — keep the backlog alive

Process what happened since the last session. Read the index and every plan file, then per status:

- **DONE** — spot-check that the done criteria still hold on the current HEAD (cheap ones only). Mark verified in the index. Don't delete plan files — they're the record.
- **BLOCKED** — read the reason. Investigate the underlying obstacle in the code. Either rewrite the plan around it (new number if the approach changed fundamentally, in-place refresh otherwise) or mark REJECTED with one line of rationale.
- **IN PROGRESS** (stale) — flag it to the user; an executor probably died mid-run. Check the worktree if one exists.
- **TODO** — run the drift check. If drifted: re-verify the finding still exists (it may have been fixed in passing), then refresh the "Current state" excerpts and the planned-at commit. If the finding is gone, mark REJECTED ("fixed independently").

A backlog that arrives under a read-only `inputs/` directory is a snapshot, not a working copy: write the whole refreshed backlog under `outputs/plans/` and record both locations, or report it unable to reconcile. Never edit `inputs/`.

Finish with a short report: what's verified done, what was refreshed, what's rejected, and what's executable right now.

---

## `--issues` — publish plans as issues

Modifier on any planning invocation (`/audit --issues`, `/audit security --issues`). The flag is the user's authorization to create issues — never create them without it.

1. Preflight: `gh auth status` succeeds and the project has a compatible remote. If either fails, write the plan files as normal and say why issues were skipped.
2. Visibility check: `gh repo view --json visibility`. If the project is **public**, warn the user that issues are publicly visible and get explicit confirmation before publishing any plan that describes a security vulnerability, credential location, or other sensitive finding. In an unattended run nobody can confirm: skip the sensitive publication and record the reason — don't ask, and don't publish it anyway.
3. Show the list of titles about to become issues; confirm once if interactive.
4. Per plan: `gh issue create --title "<plan title>" --body-file <plan file>`. Labels: `audit` plus the category — apply only if the labels exist or can be created without erroring; skip labels rather than fail.
5. Record each issue URL in the plan's Status block (`- **Issue**: <url>`) and the index.

The plan file remains the source of truth; the issue is distribution. The self-containment rule pays off here — the issue body needs no edits to make sense to whoever (or whatever) picks it up.
