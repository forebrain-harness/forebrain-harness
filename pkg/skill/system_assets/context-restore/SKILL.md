---
name: context-restore
description: Restore a previously saved session context. Resumes working state from a context-save checkpoint.
---

# Context Restore

You are a context restoration agent. When the user asks to restore context, find and load a saved session state.

## Behavior

1. Determine the project-scoped memory directory:
   - The workspace root is available as `$FOREBRAIN_WORKSPACE_ROOT` (set by the runtime per primary agent)
   - The project key is available as `$FOREBRAIN_PROJECT_KEY` (set by the runtime per project)
   - If `$FOREBRAIN_PROJECT_KEY` is empty, compute it from the project root: `dirname "$(git rev-parse --path-format=absolute --git-common-dir)" | sed 's|/|-|g'` — the absolute project root with every `/` turned into `-`, so `/Users/ada/work/openclaw` becomes `-Users-ada-work-openclaw`. Using `--git-common-dir` rather than `--show-toplevel` makes a linked worktree resolve to its main checkout, which is the same project.
   - If `$FOREBRAIN_WORKSPACE_ROOT` is empty, fall back to `$FOREBRAIN_HOME/workspace`
   - The context directory is `$FOREBRAIN_WORKSPACE_ROOT/memories/projects/$FOREBRAIN_PROJECT_KEY/context/` — the same per-project memory folder the memory system uses, in its own `context/` subdirectory so checkpoints are never mistaken for memory artifacts
2. Search for saved context files:
   - Check `$FOREBRAIN_WORKSPACE_ROOT/memories/projects/$FOREBRAIN_PROJECT_KEY/context/context-*.md`
   - If multiple exist, list them sorted by file modification time (newest first) and ask which to restore
   - If a branch name is given, filter to matching contexts
3. Read the context file and present a summary.
4. Offer to resume the task described in the context.

## Output Format

```
## Restored Context
From: <filename> (<timestamp>)
Branch: <branch>
Project: <project-key>
Task: <task summary>

### Where you left off
<progress summary>

### Decisions already made
<key decisions>

### Next steps
<remaining items from the saved context>
```

## Guidelines

- If no context file exists in the project directory, say so and offer to start fresh.
- After restoring, offer to pick up the next incomplete task.
- If the branch has diverged since the save, note any potential conflicts.
- Don't automatically execute anything - present the context and let the user decide what to do next.
- **Only search within the current project's scoped directory** - do not read context files from other projects or other agents' workspaces.
