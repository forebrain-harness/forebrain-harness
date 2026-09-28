// The markdown renderer asks whether mermaid is installed by importing it, and
// renders diagrams when the import succeeds. forebrain does not ship mermaid, but a
// build resolves a missing optional peer dependency to an empty module, which the
// renderer took for mermaid and then failed on when it called initialize. This
// module stands in for mermaid and refuses to load, which is the answer that
// probe is written for: a diagram stays a code block, as it is in the terminal.
export {}

throw new Error('mermaid is not bundled with forebrain')
