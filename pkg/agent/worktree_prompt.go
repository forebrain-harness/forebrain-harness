package agent

// GitWorktreePrompt fixes where a coding agent puts a git worktree: under the
// repository's .worktrees (tool.WorktreesDirName), which the runner keeps
// ignored. Without it each run picked its own place - /tmp, $TMPDIR, a scratch
// directory, a sibling directory, a folder of the checkout - so worktrees were
// scattered and the ones under /tmp vanished while git still listed them.
//
// It is coding-only and reaches the agents that create worktrees or write the
// plans that tell an executor to: the primary, and the general-purpose and plan
// subagents. The plan reviewer is handed a worktree directory of its own.
const GitWorktreePrompt = `

## Git worktrees

Create every git worktree at .worktrees/<name> in the repository's main worktree - the first path ` + "`git worktree list`" + ` prints, also from inside a linked worktree - and nowhere else: not /tmp, $TMPDIR, a scratch directory or a sibling directory. Name it in short kebab-case after its purpose; a plan that needs a worktree names that path. Git already ignores .worktrees/; never add it to .gitignore. Remove a worktree with ` + "`git worktree remove`" + ` once nothing needs it, unless the user wants it kept.`
