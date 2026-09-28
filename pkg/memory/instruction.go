package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

const (
	// ReadPathProjectTokenBudget and ReadPathGlobalTokenBudget split the
	// former single read-path budget across the two scopes a turn can recall
	// from. The project share is the larger of the two because it is where
	// almost all retrieval-relevant content lives; the global share only ever
	// holds a small, slow-growing set of cross-project preferences. Together
	// they equal the old single-scope budget, so splitting scopes does not
	// grow what a turn pays for recall.
	ReadPathProjectTokenBudget = 1900
	ReadPathGlobalTokenBudget  = 600
	// PendingNotesTokenBudget caps the not-yet-consolidated notes injected each
	// turn, drawn from both scopes together. They are the only copy of a
	// freshly captured rule until Phase 2 folds it into the summary, so they
	// are worth a slice of the turn budget — but a store whose consolidation is
	// failing must not be able to grow that slice without bound.
	PendingNotesTokenBudget = 1200
	maxPendingNotes         = 20
)

func SummaryPath(root Root) string {
	return filepath.Join(root.MemoryRoot, "memory_summary.md")
}

// InstructionOptions selects which halves of the per-turn memory instruction get
// rendered. The two halves are independent settings: a user who turned off
// recall still expects an explicit "remember this" to be captured, and a user
// who turned off generation still expects stored memory to be used.
type InstructionOptions struct {
	// Recall renders stored memory — the summary plus notes Phase 2 has not
	// consolidated yet — and the rules for using and citing it. Follows
	// use_memories.
	Recall bool
	// Capture renders the rules for writing new memory during a turn. Follows
	// generate_memories.
	Capture bool
	// DedicatedTools reports whether the memories_* tools are exposed, which
	// decides how the instruction tells the model to address memory files.
	DedicatedTools bool
	// ProjectOnly limits recall to the session's own project scope: the
	// global cross-project preferences block is not rendered. A project whose
	// sessions must not read outside their own memory sets this.
	ProjectOnly bool
}

// Empty reports whether the options would render nothing, so callers can skip
// resolving the memory root at all.
func (o InstructionOptions) Empty() bool { return !o.Recall && !o.Capture }

// readPathToolGuidance reconciles the absolute paths this instruction uses to
// describe the layout with the dedicated tools, which address files relative to
// the project memory root, with the global scope reachable under a "global/"
// prefix (see NewScoped). Without it a model copies an absolute
// path out of the layout above and memories_read rejects it, and it has no way
// to know that a note it just wrote by bare filename lives one directory down.
const readPathToolGuidance = `Reading and writing memory with the memories_* tools:

- ` + "`memories_read`" + `, ` + "`memories_list`" + `, and ` + "`memories_search`" + ` take a path relative to the
  project memory base path above (for example ` + "`MEMORY.md`" + `, ` + "`rollout_summaries/<file>.md`" + `,
  ` + "`skills/<skill-name>/SKILL.md`" + `). Prefix the path with ` + "`global/`" + ` to reach the global
  scope instead (for example ` + "`global/MEMORY.md`" + `). An absolute path inside either
  base path also resolves, but these tools report the relative form back.
- ` + "`memories_add_ad_hoc_note`" + ` takes a bare filename and an optional ` + "`scope`" + ` ("project",
  the default, or "global") and stores the note under ` + "`" + AdHocNotesDir + "/`" + ` in that
  scope. It returns the stored path; pass that path to ` + "`memories_read`" + ` to read the
  note back, not the bare filename.
`

// noteWriteGuidanceWithTools and noteWriteGuidanceWithFiles say how to write a
// note on each of the two paths a session can be on. Without the dedicated
// tools the model has to place the file itself, and the notes directory is the
// only location Phase 2 consolidates from.
const noteWriteGuidanceWithTools = "Write the note with `memories_add_ad_hoc_note`: `filename` is\n" +
	"`<YYYY-MM-DDTHH-MM-SS>-<slug>.md` — a UTC timestamp followed by a\n" +
	"lowercase-hyphen slug naming the lesson — `note` is the Markdown body, and\n" +
	"`scope` picks project (the default) or global.\n"

const noteWriteGuidanceWithFiles = "Write the note as a new file under\n" +
	"`{{ base_path }}/" + AdHocNotesDir + "/<YYYY-MM-DDTHH-MM-SS>-<slug>.md`\n" +
	"(or under `{{ global_base_path }}/" + AdHocNotesDir + "/` instead, for a global-scope note),\n" +
	"a UTC timestamp followed by a lowercase-hyphen slug naming the lesson, with\n" +
	"the lesson as the Markdown body.\n"

// The GlobalOnly variants are for a session with no project scope. The
// project-scoped guidance above would name the same directory twice for such a
// session, and — worse, on the tools path — would point the model at a default
// scope the backend refuses, so every capture it attempted would fail.
const noteWriteGuidanceWithToolsGlobalOnly = "Write the note with `memories_add_ad_hoc_note`, passing `scope` \"global\"\n" +
	"(this session has no project scope, so the default project scope is refused):\n" +
	"`filename` is `<YYYY-MM-DDTHH-MM-SS>-<slug>.md` — a UTC timestamp followed by a\n" +
	"lowercase-hyphen slug naming the lesson — and `note` is the Markdown body.\n"

const noteWriteGuidanceWithFilesGlobalOnly = "Write the note as a new file under\n" +
	"`{{ global_base_path }}/" + AdHocNotesDir + "/<YYYY-MM-DDTHH-MM-SS>-<slug>.md`,\n" +
	"a UTC timestamp followed by a lowercase-hyphen slug naming the lesson, with\n" +
	"the lesson as the Markdown body.\n"

// RenderTurnInstruction renders the memory instruction injected before a turn:
// the recall half (stored memory and how to use it) and the capture half (which
// user instructions become new memory, and how to write one). projectKey is
// the session's project scope (ProjectKey); empty means the session
// has no project identity, so only the global scope is ever read or written.
func RenderTurnInstruction(roots Roots, projectKey string, opts InstructionOptions) (string, error) {
	projectKey = strings.TrimSpace(projectKey)
	sections := make([]string, 0, 3)
	if opts.Recall {
		recall, err := renderRecallSection(roots, projectKey, opts.DedicatedTools, opts.ProjectOnly)
		if err != nil {
			return "", err
		}
		if recall != "" {
			sections = append(sections, recall)
		}
		if pending := renderPendingNotesSection(roots, projectKey, opts.ProjectOnly); pending != "" {
			sections = append(sections, pending)
		}
	}
	if opts.Capture {
		sections = append(sections, renderCaptureSection(roots, projectKey, opts.DedicatedTools))
	}
	return strings.Join(sections, "\n"), nil
}

// scopeMemoryRoots resolves the concrete Root of the session's project scope
// (nil when it has none) and of the agent's single global scope.
func scopeMemoryRoots(roots Roots, projectKey string) (project *Root, global Root) {
	global = roots.Scope(GlobalScope())
	if projectKey == "" {
		return nil, global
	}
	scope, ok := ProjectScope(projectKey)
	if !ok {
		return nil, global
	}
	root := roots.Scope(scope)
	return &root, global
}

// renderRecallSection renders the stored-memory half from both scopes. It
// renders nothing until at least one scope has produced a summary: before that
// there is no memory to recall, and the layout and citation rules describe
// files that do not exist yet.
func renderRecallSection(roots Roots, projectKey string, dedicatedTools, projectOnly bool) (string, error) {
	projectRoot, globalRoot := scopeMemoryRoots(roots, projectKey)
	blocks := make([]string, 0, 2)
	if projectRoot != nil {
		content, err := readSummary(*projectRoot, ReadPathProjectTokenBudget)
		if err != nil {
			return "", err
		}
		if content != "" {
			blocks = append(blocks, "### Project memory\n\n"+content)
		}
	}
	// A project-only session recalls nothing beyond its own scope: reading
	// the global preferences would be exactly the cross-project recall the
	// setting exists to prevent.
	globalContent := ""
	if !projectOnly || projectRoot == nil {
		var err error
		globalContent, err = readSummary(globalRoot, ReadPathGlobalTokenBudget)
		if err != nil {
			return "", err
		}
	}
	if globalContent != "" {
		blocks = append(blocks, "### Global preferences (apply across every project)\n\n"+globalContent)
	}
	if len(blocks) == 0 {
		return "", nil
	}
	guidance := ""
	if dedicatedTools {
		guidance = readPathToolGuidance
	}
	basePath := globalRoot.MemoryRoot
	if projectRoot != nil {
		basePath = projectRoot.MemoryRoot
	}
	scopelessNote := ""
	if projectRoot == nil {
		scopelessNote = "\nThis session has no project of its own (no launch directory), so only the " +
			"global scope below applies.\n"
	}
	return renderTemplate(readPathInstruction, map[string]string{
		"base_path":        basePath,
		"global_base_path": globalRoot.MemoryRoot,
		"scopeless_note":   scopelessNote,
		"memory_summary":   strings.Join(blocks, "\n\n"),
		"tool_guidance":    guidance,
	}), nil
}

func readSummary(root Root, budget int) (string, error) {
	b, err := os.ReadFile(SummaryPath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	content := strings.TrimSpace(string(b))
	if content == "" {
		return "", nil
	}
	return truncateTextToTokenBudget(content, budget), nil
}

// renderCaptureSection renders the write half. Unlike recall it does not depend
// on either store having any content: the first durable rule a user states has
// to be capturable in a workspace whose memory folders are still empty, which
// is exactly the session where nothing else would tell the model that memory
// exists.
func renderCaptureSection(roots Roots, projectKey string, dedicatedTools bool) string {
	projectRoot, globalRoot := scopeMemoryRoots(roots, projectKey)
	basePath := globalRoot.MemoryRoot
	scopelessNote := ""
	guidance := noteWriteGuidanceWithFiles
	if dedicatedTools {
		guidance = noteWriteGuidanceWithTools
	}
	if projectRoot != nil {
		basePath = projectRoot.MemoryRoot
	} else {
		scopelessNote = "\nThis session has no project of its own (no launch directory): every note " +
			"goes to global scope, so hold it to the global bar above.\n"
		guidance = noteWriteGuidanceWithFilesGlobalOnly
		if dedicatedTools {
			guidance = noteWriteGuidanceWithToolsGlobalOnly
		}
	}
	return renderTemplate(captureInstruction, map[string]string{
		"base_path":              basePath,
		"global_base_path":       globalRoot.MemoryRoot,
		"scopeless_capture_note": scopelessNote,
		"note_write_guidance": renderTemplate(guidance, map[string]string{
			"base_path": basePath, "global_base_path": globalRoot.MemoryRoot,
		}),
	})
}

// renderPendingNotesSection surfaces notes written after each scope's last
// consolidation pass. Consolidation runs at session startup behind a cooldown,
// so without this a rule captured now would not reach the summary — the only
// memory injected into a turn — for hours, and the next session would ask the
// user to repeat what it had already written down.
func renderPendingNotesSection(roots Roots, projectKey string, projectOnly bool) string {
	projectRoot, globalRoot := scopeMemoryRoots(roots, projectKey)
	var notes []pendingNote
	// Same boundary as the recall half: a project-only session does not read
	// pending notes from the global scope. A session with no project scope of
	// its own still reads the global one — that is its only scope.
	if !projectOnly || projectRoot == nil {
		notes = append(notes, pendingAdHocNotes(globalRoot, "global")...)
	}
	if projectRoot != nil {
		notes = append(notes, pendingAdHocNotes(*projectRoot, "")...)
	}
	if len(notes) == 0 {
		return ""
	}
	sort.Slice(notes, func(i, j int) bool {
		if notes[i].Modified.Equal(notes[j].Modified) {
			return notes[i].Path > notes[j].Path
		}
		return notes[i].Modified.After(notes[j].Modified)
	})
	if len(notes) > maxPendingNotes {
		notes = notes[:maxPendingNotes]
	}
	var body strings.Builder
	for _, note := range notes {
		fmt.Fprintf(&body, "### %s\n\n%s\n\n", note.Path, note.Body)
	}
	rendered := truncateTextToTokenBudget(strings.TrimSpace(body.String()), PendingNotesTokenBudget)
	return renderTemplate(pendingNotesInstruction, map[string]string{"pending_notes": rendered})
}

type pendingNote struct {
	Path     string
	Body     string
	Modified time.Time
	filename string
}

// pendingAdHocNotes lists one scope's notes Phase 2 has not seen yet, newest
// first, with prefix ("" or "global") stitched onto each returned path so it
// reads back through the same dual-root addressing the memories_* tools use. A
// note counts as pending until a consolidation pass for THIS scope starts
// after it was written; passes record their start instant, not their finish,
// because the diff a pass consolidates from is snapshotted when it starts.
func pendingAdHocNotes(root Root, prefix string) []pendingNote {
	dir := filepath.Join(root.MemoryRoot, filepath.FromSlash(AdHocNotesDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	consolidated := lastConsolidationStart(root)
	notes := make([]pendingNote, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || !info.ModTime().After(consolidated) {
			continue
		}
		path := AdHocNotePath(entry.Name())
		if prefix != "" {
			path = prefix + "/" + path
		}
		notes = append(notes, pendingNote{Path: path, Modified: info.ModTime(), filename: entry.Name()})
	}
	sort.Slice(notes, func(i, j int) bool {
		if notes[i].Modified.Equal(notes[j].Modified) {
			return notes[i].Path > notes[j].Path
		}
		return notes[i].Modified.After(notes[j].Modified)
	})
	if len(notes) > maxPendingNotes {
		notes = notes[:maxPendingNotes]
	}
	// Bodies are read only for the notes that survive the cap: this runs before
	// every model call, so a store with a backlog of notes must not turn each
	// turn into a full read of the notes directory.
	kept := notes[:0]
	for _, note := range notes {
		body, err := os.ReadFile(filepath.Join(dir, note.filename))
		if err != nil {
			continue
		}
		if note.Body = strings.TrimSpace(string(body)); note.Body != "" {
			kept = append(kept, note)
		}
	}
	return kept
}

func InjectDeveloperInstruction(messages []llm.Message, instruction string) []llm.Message {
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return append([]llm.Message(nil), messages...)
	}
	out := append([]llm.Message(nil), messages...)
	msg := llm.Message{Role: llm.RoleDeveloper, Parts: []llm.ContentPart{llm.Text(instruction)}, IsMeta: true}
	if len(out) > 0 && out[0].Role == llm.RoleSystem {
		out = append(out[:1], append([]llm.Message{msg}, out[1:]...)...)
		return out
	}
	return append([]llm.Message{msg}, out...)
}

func truncateTextToTokenBudget(value string, budget int) string {
	return MiddleTokens(value, budget)
}
