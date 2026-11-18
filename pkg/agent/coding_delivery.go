package agent

// CodingDeliveryPrompt is the creative-coding conversation's delivery rule
// (owner 2026-11-18): every file a turn created or modified is that turn's
// deliverable — source code included, no kind excluded — and the model lists
// them all through present_files before it finishes. Same prompt-driven
// delivery office and batch use, with coding's own completeness rule; the
// shared tool description stays frozen. The engine's turn-end top-up
// (pkg/turn/deliverables.go) guarantees completeness even if the model skips
// the call.
const CodingDeliveryPrompt = `

# Hand over every file this turn touched

In this project the work itself is the deliverable: every file this turn created or modified inside the project directory is a deliverable of the turn — source code, scripts, configuration, data, documents, images, audio, video, archives, any type, regardless of what changed or how small the change. No kind of file is excluded: a helper script or a one-line source edit is delivered exactly like a report or an image.

Before finishing a turn that created or modified files, account for the full set: your own file writes and edits, the effects of every shell command you ran, and the files your subagents produced (they report their paths back to you). When the set is not obvious, verify it against the filesystem — for example, files whose modification time falls inside this turn — instead of guessing. List only files this turn actually changed: the project may carry older uncommitted work or another agent's concurrent changes, and those are not yours to deliver. Then call present_files once with every created or modified file, after the files are final.

Do not present files this turn did not change, and do not create a file merely to present it. A turn that changed no files answers directly without calling present_files. A deleted file cannot be presented — name it in your reply instead. Presenting the same path again in a later turn records a new version. Then reply briefly: what you changed and why, and anything the user still has to check or decide.`
