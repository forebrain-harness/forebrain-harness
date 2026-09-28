package memory

import _ "embed"

//go:embed templates/stage_one_system.md
var stageOneSystem string

//go:embed templates/stage_one_input.md
var stageOneInput string

//go:embed templates/consolidation_project.md
var consolidationProjectInstruction string

//go:embed templates/consolidation_global.md
var consolidationGlobalInstruction string

//go:embed templates/read_path.md
var readPathInstruction string

//go:embed templates/capture.md
var captureInstruction string

//go:embed templates/pending_notes.md
var pendingNotesInstruction string

//go:embed templates/ad_hoc_instructions.md
var adHocInstructions string

const memoryExtensionsFolderStructure = `
Memory extensions (under {{ memory_extensions_root }}/):

- <extension_name>/instructions.md
  - Source-specific guidance for interpreting additional memory signals. If an
    extension folder exists, you must read its instructions.md to determine how to use this memory
    source.

If the user has any memory extensions, you MUST read the instructions for each extension to
determine how to use the memory source. If the workspace diff shows deleted extension resource files,
remove stale memories derived only from those resources. If it has no extension folders, continue
with the standard memory inputs only.
`

const memoryExtensionsPrimaryInputs = `
Optional source-specific inputs:
Under ` + "`{{ memory_extensions_root }}/`" + `:

- ` + "`<extension_name>/instructions.md`" + `
  - If extension folders exist, read each instructions.md first and follow it when interpreting
    that extension's memory source.

If the workspace diff shows deleted memory extension resources, use that extension-specific deletion
signal to remove stale memories derived only from those resources.
`
