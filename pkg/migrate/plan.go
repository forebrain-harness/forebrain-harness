package migrate

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Report is the full outcome of one import, dry run or real.
type Report struct {
	DryRun bool
	Source Kind
	// SourceRoot is the source directory this run actually read (the picker
	// input or --home, resolved); the report and preview both carry it so
	// "which directory was this" stays answerable after the fact.
	SourceRoot string
	Sessions   []SessionOutcome
	// MessageRows counts mapped rows: inserted rows on a real run, mapped
	// rows on a dry run.
	MessageRows int
	// SourceBytes is the transcript volume of the conversations written (a
	// dry run: that would be written), the base of the database-growth
	// estimate.
	SourceBytes int64
	Memories    []MemoryOutcome
	Skills      []SkillOutcome
	Plans       []PlanOutcome
	MCP         []MCPOutcome
	LSP         []LSPOutcome
	// Projects are the projects the import writes forebrain's own project files
	// into (a dry run: would write).
	Projects []ProjectWrite
	// Notices is everything else the person running the import must know.
	Notices []string
	History HistoryOutcome
	// Consolidation states what the post-import memory pass actually did.
	Consolidation string
	// SkippedCategories names categories the caller excluded with Only.
	SkippedCategories []string
}

// ProjectWrite is a project an import writes forebrain's own project files into
// — the MCP servers and permission rules of the source's project
// configuration — or, in a dry run, would write them into.
type ProjectWrite struct {
	Path string
	// Unloaded says why forebrain will not load the files, empty when it will.
	Unloaded string
	// Notes is what else is particular to this project.
	Notes []string
}

// SessionCounts tallies the session outcomes by status.
func (r *Report) SessionCounts() (migrated, skipped, failed, owned int) {
	for _, outcome := range r.Sessions {
		switch outcome.Status {
		case StatusMigrated:
			migrated++
		case StatusFailed:
			failed++
		case StatusOwned:
			owned++
		default:
			skipped++
		}
	}
	return
}

// Plan is the dry-run preview the confirm dialog shows.
type Plan struct {
	Source     Kind
	SourceRoot string
	Sessions   int
	Subagents  int
	// AlreadyHere counts conversations a previous import (or this agent)
	// already has; Refused, the ones that cannot be imported.
	AlreadyHere  int
	Refused      int
	MessageRows  int
	SourceBytes  int64
	DBEstimate   int64
	Memories     int
	Skills       int
	Plans        int
	MCPGlobal    int
	MCPProject   int
	LSPEnabled   int
	LSPWritten   int
	HistoryLines int
	Projects     []ProjectWrite
	Notices      []string
}

// PlanClaude previews a Claude Code import without writing anything. It runs
// the same discovery and parsing the real run uses, so the numbers it shows
// are the numbers a run would produce on the same data.
func PlanClaude(ctx context.Context, opts *Options, progress func(Progress)) (*Plan, *Report, error) {
	runOpts := *opts
	runOpts.DryRun = true
	report, err := RunClaude(ctx, &runOpts, progress)
	if err != nil {
		return nil, nil, err
	}
	return reportToPlan(report), report, nil
}

// estimateDBGrowth sizes the expected database increase from the transcript
// volume: the mapped payload plus SQLite's row and index overhead.
func estimateDBGrowth(sourceBytes int64) int64 {
	return sourceBytes + sourceBytes/4
}

func countMCP(outcomes []MCPOutcome, scope string, native bool) int {
	n := 0
	for _, outcome := range outcomes {
		if outcome.Scope == scope && (outcome.Status == "native") == native {
			n++
		}
	}
	return n
}

// RunSource dispatches an import to its source implementation.
func RunSource(ctx context.Context, source string, opts *Options, progress func(Progress)) (*Report, error) {
	switch Kind(strings.TrimSpace(strings.ToLower(source))) {
	case "", KindClaude:
		return RunClaude(ctx, opts, progress)
	case KindCodex:
		return RunCodex(ctx, opts, progress)
	default:
		return nil, fmt.Errorf("unknown migration source %q", source)
	}
}

// PlanSource previews an import from the named source without writing.
func PlanSource(ctx context.Context, source string, opts *Options, progress func(Progress)) (*Plan, *Report, error) {
	switch Kind(strings.TrimSpace(strings.ToLower(source))) {
	case "", KindClaude:
		return PlanClaude(ctx, opts, progress)
	case KindCodex:
		return PlanCodex(ctx, opts, progress)
	default:
		return nil, nil, fmt.Errorf("unknown migration source %q", source)
	}
}

// PlanCodex previews a Codex import without writing anything. It runs the
// same discovery and parsing the real run uses, so the numbers it shows are
// the numbers a run would produce on the same data.
func PlanCodex(ctx context.Context, opts *Options, progress func(Progress)) (*Plan, *Report, error) {
	runOpts := *opts
	runOpts.DryRun = true
	report, err := RunCodex(ctx, &runOpts, progress)
	if err != nil {
		return nil, nil, err
	}
	return reportToPlan(report), report, nil
}

// reportToPlan folds a dry-run report into the confirm dialog's preview.
func reportToPlan(report *Report) *Plan {
	plan := &Plan{
		Source:       report.Source,
		SourceRoot:   report.SourceRoot,
		MessageRows:  report.MessageRows,
		SourceBytes:  report.SourceBytes,
		DBEstimate:   estimateDBGrowth(report.SourceBytes),
		MCPGlobal:    countMCP(report.MCP, "global", false),
		MCPProject:   countMCP(report.MCP, "project", false),
		HistoryLines: report.History.SourceLines,
		Projects:     report.Projects,
		Notices:      append([]string(nil), report.Notices...),
	}
	for _, outcome := range report.Sessions {
		switch {
		case outcome.Status == StatusMigrated && outcome.IsChild:
			plan.Subagents++
		case outcome.Status == StatusMigrated:
			plan.Sessions++
		case outcome.Status == StatusSkipped:
			plan.AlreadyHere++
		default:
			plan.Refused++
		}
	}
	for _, memory := range report.Memories {
		plan.Memories += len(memory.Written) + len(memory.Skipped)
	}
	for _, skill := range report.Skills {
		if skill.Status == "installed" {
			plan.Skills++
		}
	}
	for _, planOutcome := range report.Plans {
		if planOutcome.Status == "installed" {
			plan.Plans++
		}
	}
	for _, outcome := range report.LSP {
		switch outcome.Status {
		case "enabled":
			plan.LSPEnabled++
		case "written":
			plan.LSPWritten++
		}
	}
	return plan
}

// Text renders the preview the confirm dialog shows: what would be imported,
// where from, and what will not take effect the moment it is written.
func (p *Plan) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "From %s", sourceDisplayName(p.Source))
	if root := strings.TrimSpace(p.SourceRoot); root != "" {
		fmt.Fprintf(&b, " (%s)", root)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "  Conversations: %s, plus %s subagent transcripts · %s messages",
		formatCount(int64(p.Sessions)), formatCount(int64(p.Subagents)), formatCount(int64(p.MessageRows)))
	if p.AlreadyHere > 0 {
		fmt.Fprintf(&b, " · %s already here", formatCount(int64(p.AlreadyHere)))
	}
	if p.Refused > 0 {
		fmt.Fprintf(&b, " · %s cannot be imported", formatCount(int64(p.Refused)))
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "  Memories: %d · Skills: %d · Plans: %d\n", p.Memories, p.Skills, p.Plans)
	fmt.Fprintf(&b, "  MCP servers: %d global · %d in projects\n", p.MCPGlobal, p.MCPProject)
	if p.LSPEnabled > 0 || p.LSPWritten > 0 {
		fmt.Fprintf(&b, "  Language servers: %d enabled · %d added\n", p.LSPEnabled, p.LSPWritten)
	}
	fmt.Fprintf(&b, "  Input history: %s lines\n", formatCount(int64(p.HistoryLines)))
	if p.SourceBytes > 0 {
		fmt.Fprintf(&b, "  Database: grows by about %s (%s of transcripts)\n", humanBytes(p.DBEstimate), humanBytes(p.SourceBytes))
	} else {
		b.WriteString("  Database: no new conversations to store\n")
	}
	writeImportNotes(&b, p.Source, true, p.Projects, p.Notices)
	return strings.TrimRight(b.String(), "\n")
}

// writeImportNotes renders what the person running an import must know
// beyond the counts: the projects that get forebrain's own project files — what
// that means is said once, then each project with what is particular to it —
// and every other note.
func writeImportNotes(b *strings.Builder, source Kind, dryRun bool, projects []ProjectWrite, notices []string) {
	if len(projects) > 0 {
		name := sourceDisplayName(source)
		copied := "are copied"
		if dryRun {
			copied = "would be copied"
		}
		b.WriteString("\nProject files\n")
		fmt.Fprintf(b, "  %s's MCP servers and permission rules %s into a .forebrain folder in each project below. "+
			"The copies load in new sessions only, each server asks before it first starts and is authorized on its own "+
			"even when a global one has the same name, and %s keeps reading its originals, so the two will drift apart.\n", name, copied, name)
		for _, project := range projects {
			line := "  " + project.Path
			if project.Unloaded != "" {
				line += " — will not load: " + project.Unloaded
			}
			b.WriteString(line + "\n")
			for _, note := range project.Notes {
				b.WriteString("    · " + note + "\n")
			}
		}
	}
	seen := map[string]bool{}
	wrote := false
	for _, notice := range notices {
		if seen[notice] {
			continue
		}
		seen[notice] = true
		if !wrote {
			b.WriteString("\nNotes\n")
			wrote = true
		}
		b.WriteString("  · " + notice + "\n")
	}
}

func humanBytes(v int64) string {
	const unit = 1024
	if v < unit {
		return fmt.Sprintf("%d B", v)
	}
	amount := float64(v)
	for _, suffix := range []string{"KB", "MB", "GB", "TB"} {
		amount /= unit
		if amount < unit {
			return fmt.Sprintf("%.1f %s", amount, suffix)
		}
	}
	return fmt.Sprintf("%.1f PB", amount/unit)
}

// RunClaude imports Claude Code's on-disk history under these options. It
// never fails wholesale: category errors land in the report and the run
// moves on, and every session writes in its own transaction (§6).
func RunClaude(ctx context.Context, opts *Options, progress func(Progress)) (*Report, error) {
	root := claudeRootAt(opts.SourceRoot)
	if root == "" {
		return nil, fmt.Errorf("Claude Code's data directory (~/.claude or $CLAUDE_HOME) was not found")
	}
	if progress != nil {
		progress(Progress{Stage: "discovering", Detail: root})
	}
	data, err := discoverClaude(root)
	if err != nil {
		return nil, err
	}
	report := &Report{Source: KindClaude, SourceRoot: root, DryRun: opts.DryRun}
	for _, category := range []string{"sessions", "memories", "skills", "plans", "mcp", "lsp", "history"} {
		if !opts.wants(category) {
			report.SkippedCategories = append(report.SkippedCategories, category)
		}
	}

	if opts.wants("sessions") {
		sessions, rows, bytes := importSessions(ctx, data, opts, progress)
		report.Sessions = sessions
		report.MessageRows = rows
		report.SourceBytes = bytes
	}
	if opts.wants("memories") {
		memories, err := importMemories(ctx, data, opts, data.JSONProjectPaths, progress)
		if err != nil {
			report.Memories = memories
			report.Notices = append(report.Notices, "memory import stopped early: "+err.Error())
		} else {
			report.Memories = memories
		}
		if !opts.DryRun && len(report.Memories) > 0 {
			report.Consolidation = consolidateNow(ctx, opts, progress)
		}
	}
	if opts.wants("skills") {
		report.Skills = importSkills(data, opts, progress)
	}
	if opts.wants("plans") {
		report.Plans = importPlans(data, opts, progress)
	}
	if opts.wants("mcp") {
		mcpOutcomes, notices, projects, err := importMCP(data, opts, progress)
		report.MCP = mcpOutcomes
		report.Projects = projects
		report.Notices = append(report.Notices, notices...)
		if err != nil {
			report.Notices = append(report.Notices, "MCP import stopped early: "+err.Error())
		}
	}
	if opts.wants("lsp") {
		lspOutcomes, notices, err := importLSP(data, opts, progress)
		report.LSP = lspOutcomes
		report.Notices = append(report.Notices, notices...)
		if err != nil {
			report.Notices = append(report.Notices, "LSP import stopped early: "+err.Error())
		}
	}
	if opts.wants("history") {
		history, err := mergeInputHistory(data, opts, progress)
		report.History = history
		if err != nil {
			report.Notices = append(report.Notices, "input-history merge failed: "+err.Error())
		}
		if history.SourceLines == 0 {
			report.History.SourceLines = data.HistoryLines
		}
	}
	// The global instruction file is a memory-category asset (§4.7): both
	// sources implement the same branch.
	if opts.wants("memories") {
		report.Memories = append(report.Memories, importClaudeGlobalInstructions(data, opts))
	}
	return report, nil
}

// RunCodex imports a Codex home's history under these options. It never
// fails wholesale: category errors land in the report and the run moves on,
// and every session writes in its own transaction (§6).
func RunCodex(ctx context.Context, opts *Options, progress func(Progress)) (*Report, error) {
	root := codexRootAt(opts.SourceRoot)
	if root == "" {
		return nil, fmt.Errorf("Codex's data directory (~/.codex or $CODEX_HOME) was not found")
	}
	if progress != nil {
		progress(Progress{Stage: "discovering", Detail: root})
	}
	data, err := discoverCodex(root)
	if err != nil {
		return nil, err
	}
	report := &Report{Source: KindCodex, SourceRoot: root, DryRun: opts.DryRun}
	for _, category := range []string{"sessions", "memories", "skills", "plans", "mcp", "history"} {
		if !opts.wants(category) {
			report.SkippedCategories = append(report.SkippedCategories, category)
		}
	}
	report.Notices = append(report.Notices, data.Degraded...)

	// Sessions and plans share one parse (§4.10): the plan extraction reads
	// the same parsed sessions the importer wrote.
	var parsed map[string]*parsedSession
	if opts.wants("sessions") || opts.wants("plans") {
		units := codexUnits(data, opts)
		if opts.wants("sessions") {
			parsed = map[string]*parsedSession{}
			sessions, rows, bytes, captured := importUnitsCapture(ctx, units, parseCodexRollout, opts, progress, parsed)
			report.Sessions = sessions
			report.MessageRows = rows
			report.SourceBytes = bytes
			parsed = captured
		} else {
			parsed = map[string]*parsedSession{}
			for _, unit := range units {
				if p, err := parseCodexRollout(ctx, unit.Path, strings.TrimPrefix(unit.TargetID, "cli-"), nil); err == nil {
					unit.applyDefaults(p)
					parsed[unit.TargetID] = p
				}
			}
		}
	}
	if opts.wants("memories") {
		memories, err := importCodexMemories(ctx, data, opts, progress)
		if err != nil {
			report.Memories = memories
			report.Notices = append(report.Notices, "memory import stopped early: "+err.Error())
		} else {
			report.Memories = memories
		}
		report.Memories = append(report.Memories, importCodexGlobalInstructions(data, opts))
		if !opts.DryRun && len(report.Memories) > 0 {
			report.Consolidation = consolidateNow(ctx, opts, progress)
		}
	}
	if opts.wants("skills") {
		report.Skills = importSkillsFrom(data.PluginSkills, opts, progress, func(*Options) []SkillOutcome {
			return codexSystemSkillNotes(root)
		})
	}
	if opts.wants("plans") {
		report.Plans = extractCodexPlans(parsed, opts, progress)
	}
	if opts.wants("mcp") {
		userOutcomes, userNotices, err := importCodexUserMCP(data, opts)
		report.MCP = append(report.MCP, userOutcomes...)
		report.Notices = append(report.Notices, userNotices...)
		if err != nil {
			report.Notices = append(report.Notices, "user-level MCP import stopped early: "+err.Error())
		}
		projectOutcomes, projects := importCodexProjectConfig(data, opts, progress)
		report.MCP = append(report.MCP, projectOutcomes...)
		report.Projects = projects
	}
	if opts.wants("history") {
		history, err := mergeHistoryEntries(data.History, data.HistoryLines, opts, progress)
		report.History = history
		if err != nil {
			report.Notices = append(report.Notices, "input-history merge failed: "+err.Error())
		}
		if history.SourceLines == 0 {
			report.History.SourceLines = data.HistoryLines
		}
	}
	// Codex-specific accounting the report must state rather than leave the
	// reader to infer (§11): what was dropped because the source encrypted it.
	var encrypted, developer, cancelled int
	for _, session := range parsed {
		encrypted += session.stats.EncryptedReasoning
		developer += session.stats.DeveloperMessages
		cancelled += session.stats.CancelledResults
	}
	if opts.wants("sessions") || opts.wants("plans") {
		if encrypted > 0 {
			report.Notices = append(report.Notices,
				fmt.Sprintf("%d reasoning items were not migrated: the source stored them encrypted and no plaintext exists", encrypted))
		}
		if developer > 0 {
			report.Notices = append(report.Notices,
				fmt.Sprintf("%d developer messages were dropped: forebrain's model context has no developer role", developer))
		}
		if cancelled > 0 {
			report.Notices = append(report.Notices,
				fmt.Sprintf("%d interrupted tool calls were answered with a cancelled result so the sessions stay continuable", cancelled))
		}
	}
	if len(data.PluginSkills) > 0 {
		report.Notices = append(report.Notices,
			"imported skills take effect in new sessions only — the current session's tool table is frozen")
	}
	return report, nil
}

// consolidateNow runs the injected consolidation callback once and renders
// its honest outcome for the report (B8: nil callback or a disabled feature
// is reported as such, never as success).
func consolidateNow(ctx context.Context, opts *Options, progress func(Progress)) string {
	if opts.Consolidate == nil {
		return "notes written; consolidation not available in this session"
	}
	if progress != nil {
		progress(Progress{Stage: "consolidating", Detail: "consolidating memories…"})
	}
	if err := opts.Consolidate(ctx, "migration"); err != nil {
		return "notes written; consolidation did not run: " + err.Error()
	}
	return "consolidated"
}

// Text renders the report a person reads: where the import came from and
// what it counted, then what it did per category, then what the person must
// know before it surprises them.
func (r *Report) Text() string {
	var b strings.Builder
	head := "Migrated from "
	if r.DryRun {
		head = "Would migrate from "
	}
	b.WriteString(head + sourceDisplayName(r.Source))
	if root := strings.TrimSpace(r.SourceRoot); root != "" {
		b.WriteString(" (" + root + ")")
	}
	b.WriteString("\n")

	migrated, skipped, failed, owned := r.SessionCounts()
	imported := "imported"
	if r.DryRun {
		imported = "to import"
	}
	fmt.Fprintf(&b, "  Conversations: %d %s · %d already here · %d failed · %d belong to another agent\n", migrated, imported, skipped, failed, owned)
	fmt.Fprintf(&b, "  Messages: %s\n", formatCount(int64(r.MessageRows)))
	if len(r.Memories) > 0 {
		written, skippedNotes := 0, 0
		for _, memory := range r.Memories {
			written += len(memory.Written)
			skippedNotes += len(memory.Skipped)
		}
		fmt.Fprintf(&b, "  Memories: %d written · %d skipped\n", written, skippedNotes)
	}
	if len(r.Skills) > 0 {
		byStatus := map[string]int{}
		for _, skill := range r.Skills {
			byStatus[skill.Status]++
		}
		fmt.Fprintf(&b, "  Skills: %d installed · %d up to date · %d changed on both sides · %d read natively\n",
			byStatus["installed"], byStatus["up-to-date"], byStatus["diverged"], byStatus["native"])
	}
	if len(r.Plans) > 0 {
		byStatus := map[string]int{}
		for _, plan := range r.Plans {
			byStatus[plan.Status]++
		}
		fmt.Fprintf(&b, "  Plans: %d installed · %d up to date · %d changed on both sides · %d skipped\n",
			byStatus["installed"], byStatus["up-to-date"], byStatus["diverged"], byStatus["skipped"])
	}
	if len(r.MCP) > 0 {
		fmt.Fprintf(&b, "  MCP servers: %d written · %d skipped · %d read natively\n",
			countStatus(r.MCP, "written"), countStatus(r.MCP, "skipped"), countStatus(r.MCP, "native"))
	}
	if len(r.LSP) > 0 {
		byStatus := map[string]int{}
		for _, entry := range r.LSP {
			byStatus[entry.Status]++
		}
		fmt.Fprintf(&b, "  Language servers: %d enabled · %d added · %d skipped\n",
			byStatus["enabled"], byStatus["written"], byStatus["skipped"])
	}
	if r.History.SourceLines > 0 || r.History.Added > 0 {
		fmt.Fprintf(&b, "  Input history: %d added · %d already there, of %d lines\n",
			r.History.Added, r.History.Skipped, r.History.SourceLines)
	}
	if !r.DryRun && r.Consolidation != "" {
		b.WriteString("  Memory consolidation: " + r.Consolidation + "\n")
	}
	if len(r.SkippedCategories) > 0 {
		b.WriteString("  Left out on request: " + strings.Join(r.SkippedCategories, ", ") + "\n")
	}

	if failed > 0 || owned > 0 {
		b.WriteString("\nConversations not imported\n")
		for _, outcome := range r.Sessions {
			if outcome.Status != StatusFailed && outcome.Status != StatusOwned {
				continue
			}
			fmt.Fprintf(&b, "  %s — %s\n", outcome.SourcePath, outcome.Reason)
		}
	}
	if migrated > 0 && !r.DryRun {
		b.WriteString("\nImported conversations — /resume lists them\n")
		for _, outcome := range r.Sessions {
			if outcome.Status != StatusMigrated {
				continue
			}
			prefix := "  "
			if outcome.IsChild {
				prefix = "    ↳ "
			}
			// The id keeps every result identifiable, however titles collide.
			if title := strings.TrimSpace(outcome.Title); title != "" {
				fmt.Fprintf(&b, "%s%s · %d messages · %s\n", prefix, title, outcome.Messages, outcome.TargetID)
			} else {
				fmt.Fprintf(&b, "%s%s · %d messages\n", prefix, outcome.TargetID, outcome.Messages)
			}
		}
	}
	if len(r.Memories) > 0 {
		b.WriteString("\nMemories\n")
		for _, memory := range r.Memories {
			line := fmt.Sprintf("  %s → %s", memory.ProjectDir, firstNonEmpty(memory.ResolvedPath, "global scope"))
			if memory.Reason != "" {
				line += " (" + memory.Reason + ")"
			}
			b.WriteString(line + "\n")
		}
	}
	if len(r.Skills) > 0 {
		b.WriteString("\nSkills\n")
		for _, skill := range r.Skills {
			b.WriteString("  " + outcomeLine(skill.Name, skill.Status, skill.Detail) + "\n")
		}
	}
	if len(r.Plans) > 0 {
		b.WriteString("\nPlans\n")
		for _, plan := range r.Plans {
			b.WriteString("  " + outcomeLine(plan.Name, plan.Status, plan.Detail) + "\n")
		}
	}
	if len(r.MCP) > 0 {
		b.WriteString("\nMCP servers\n")
		for _, entry := range r.MCP {
			b.WriteString("  " + outcomeLine(entry.Name+" ("+entry.Scope+")", entry.Status, entry.Detail) + "\n")
		}
	}
	if len(r.LSP) > 0 {
		b.WriteString("\nLanguage servers\n")
		for _, entry := range r.LSP {
			b.WriteString("  " + outcomeLine(entry.Name, entry.Status, entry.Detail) + "\n")
		}
	}
	writeImportNotes(&b, r.Source, r.DryRun, r.Projects, r.Notices)
	return strings.TrimRight(b.String(), "\n")
}

// outcomeLine is one item of a category: its name, what happened to it, and
// why when there is more to say.
func outcomeLine(name, status, detail string) string {
	line := name + " — " + strings.ReplaceAll(status, "-", " ")
	if detail = strings.TrimSpace(detail); detail != "" {
		line += ": " + detail
	}
	return line
}

func countStatus(outcomes []MCPOutcome, status string) int {
	n := 0
	for _, outcome := range outcomes {
		if outcome.Status == status {
			n++
		}
	}
	return n
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func formatCount(v int64) string {
	digits := fmt.Sprintf("%d", v)
	var parts []string
	for len(digits) > 3 {
		parts = append([]string{digits[len(digits)-3:]}, parts...)
		digits = digits[:len(digits)-3]
	}
	parts = append([]string{digits}, parts...)
	return strings.Join(parts, ",")
}

// SortStrings returns a sorted copy, for deterministic report listings.
func SortStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
