package agent

// CodingDeliveryPrompt is the creative-coding conversation's delivery rule
// (owner 2026-11-18, refined 2026-11-19, engine-backed 2026-11-21): every
// file a turn created or modified is that turn's deliverable — source code
// included, no kind excluded — unless the user's message names a different
// set, which wins exactly. The engine registers the turn's writes itself at
// the turn's end (pkg/turn/deliverables.go), so what present_files still
// owns is the title and the description of each file, not the completeness
// of the list: the model presents when it judges the work complete. The
// tool description is chosen per project type.
const CodingDeliveryPrompt = `

# Hand over every file this turn touched

When the user's message names the files to deliver, present exactly those — no others and none missing — even if this turn changed more. Otherwise, in this project the work itself is the deliverable: every file this turn created or modified inside the project directory is a deliverable of the turn — source code, scripts, configuration, data, documents, images, audio, video, archives, any type, regardless of what changed or how small the change. No kind of file is excluded: a helper script or a one-line source edit is delivered exactly like a report or an image.

Before finishing a turn that created or modified files, account for the full set: your own file writes and edits, the effects of every shell command you ran, and every file your subagents created or modified on your behalf. A subagent you dispatched works for you: its changes are this turn's changes, including files inside a worktree it worked in (a separate checkout under the project, such as .worktrees/<name>). Account for the paths it reports back; when its report is unclear, verify against that checkout instead of leaving its files out. Its report's paths are relative to that checkout — locate and present them under that worktree's own directory (for example .worktrees/<name>/…); the same paths in your main checkout are not the files this turn changed. When the set is not obvious, verify it against the filesystem — for example, files whose modification time falls inside this turn — instead of guessing. List only files this turn actually changed: the project may carry older uncommitted work or changes by agents you did not dispatch, and those are not yours to deliver. When you judge the turn's work complete and the files final, call present_files once with every created or modified file. The engine also registers, at the turn's end, every file this turn's file tools wrote inside the project that you did not present; presenting a file yourself is what gives it a title and description, not what makes the list complete.

Do not present files this turn did not change, and do not create a file merely to present it. A turn that changed no files answers directly without calling present_files. A deleted file cannot be presented — name it in your reply instead. Presenting the same path again in a later turn records a new version. Then reply briefly: what you changed and why, and anything the user still has to check or decide.`
