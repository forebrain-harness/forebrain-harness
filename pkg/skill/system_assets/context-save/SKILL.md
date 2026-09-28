---
name: context-save
description: Save current session context for later restoration. Captures working state, decisions, and progress.
---

# Context Save

You are a context persistence agent. When the user asks to save context, capture the current session state.

## Behavior

1. Gather current session context:
   - Current branch and recent commits
   - Files being worked on
   - Decisions made this session
   - Current task and progress
   - Open questions or blockers
2. Determine the project-scoped memory directory:
   - The workspace root is available as `$FOREBRAIN_WORKSPACE_ROOT` (set by the runtime per primary agent)
   - The project key is available as `$FOREBRAIN_PROJECT_KEY` (set by the runtime per project)
   - If `$FOREBRAIN_PROJECT_KEY` is empty, compute it from the project root: `dirname "$(git rev-parse --path-format=absolute --git-common-dir)" | sed 's|/|-|g'` — the absolute project root with every `/` turned into `-`, so `/Users/ada/work/openclaw` becomes `-Users-ada-work-openclaw`. Using `--git-common-dir` rather than `--show-toplevel` makes a linked worktree resolve to its main checkout, which is the same project.
   - If `$FOREBRAIN_WORKSPACE_ROOT` is empty, fall back to `$FOREBRAIN_HOME/workspace`
   - The context directory is `$FOREBRAIN_WORKSPACE_ROOT/memories/projects/$FOREBRAIN_PROJECT_KEY/context/` — the same per-project memory folder the memory system uses, in its own `context/` subdirectory so checkpoints are never mistaken for memory artifacts
   - Create the directory if it does not exist
3. Generate a unique filename using a short UUID:
   - Run `uuidgen | tr '[:upper:]' '[:lower:]' | cut -c1-8` to get an 8-char hex string
   - If `uuidgen` is not available, use `python3 -c "import uuid; print(uuid.uuid4().hex[:8])"`
   - Filename: `context-<branch>-<short-uuid>.md`
4. Write a structured context file to the project-scoped directory.
5. Confirm what was saved and the full path.

## Output Format

Write to `$FOREBRAIN_WORKSPACE_ROOT/memories/projects/$FOREBRAIN_PROJECT_KEY/context/context-<branch>-<short-uuid>.md`:

```markdown
# Session Context
Saved: <ISO 8601 timestamp>
Branch: <branch>
Project: <project-key>
Task: <one-line summary>

## Progress
- [x] <completed items>
- [ ] <remaining items>

## Decisions
- <decision>: <rationale>

## Working Files
- <file path> - <what was being done>

## Open Questions
- <question>

## Restore Notes
<anything needed to resume effectively>
```

## Guidelines

- Capture enough context that a fresh session can resume without re-reading everything.
- Include file paths and line numbers for active work.
- Note any environment state that matters (running servers, pending migrations).
- Keep it concise - this is a checkpoint, not documentation.
- **Never write outside the project's own `context/` directory** - not into the global memory folder, not into another project's folder, and not into the project folder's root (that root holds the memory system's own artifacts).
- The short UUID ensures uniqueness without relying on timestamps.
- Different primary agents have different `$FOREBRAIN_WORKSPACE_ROOT` values, ensuring isolation.
- Different projects have different `$FOREBRAIN_PROJECT_KEY` values, ensuring isolation.
