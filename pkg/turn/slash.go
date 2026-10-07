// Slash command surface: registry, matching, resolution, and dynamic commands.
package turn

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

type SlashCommandRecord struct {
	CanonicalName      string   `json:"canonical_name"`
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	Category           string   `json:"category"`
	ArgumentHint       string   `json:"argument_hint"`
	ActionKind         string   `json:"action_kind"`
	AllowedModes       []string `json:"allowed_modes"`
	SupportsInlineArgs bool     `json:"supports_inline_args"`
	AvailableDuringRun bool     `json:"available_during_run"`
	AvailableInSide    bool     `json:"available_in_side_conversation"`
	Visibility         string   `json:"visibility"`
}

func ListSlashCommands(surface Surface, query string, opts DiscoveryOptions) []SlashCommandRecord {
	var cmds []Command
	if query == "" {
		cmds = VisibleWithOptions(surface, opts)
	} else {
		cmds = FilterWithOptions(surface, query, opts)
	}
	out := make([]SlashCommandRecord, 0, len(cmds))
	for _, cmd := range cmds {
		out = append(out, SlashCommandRecord{
			CanonicalName:      cmd.CanonicalName,
			Name:               cmd.Name,
			Description:        cmd.Description,
			Category:           cmd.Category,
			ArgumentHint:       cmd.ArgumentHint,
			ActionKind:         cmd.ActionKind,
			AllowedModes:       append([]string(nil), cmd.AllowedModes...),
			SupportsInlineArgs: cmd.SupportsInlineArgs,
			AvailableDuringRun: cmd.AvailableDuringRun,
			AvailableInSide:    cmd.AvailableInSideConversation,
			Visibility:         string(cmd.Visibility),
		})
	}
	return out
}

func (s *Service) ListSlashCommands(surface Surface, query string, opts DiscoveryOptions) []SlashCommandRecord {
	return ListSlashCommands(surface, query, opts)
}

func ExecuteSlashCommand(ctx Context, content string) Result {
	return Execute(ctx, content)
}

func (s *Service) ExecuteSlashCommand(ctx Context, content string) Result {
	if s == nil {
		return CommandService{}.Execute(ctx, content)
	}
	return s.commands.Execute(ctx, content)
}

// The SubagentView field on each command is the D4 classification: which
// commands act on the subagent whose view they are typed in, which run
// globally as before, and which are hidden there. It is a property of the
// command (not of a surface), so the TUI and the web read one table.
var commands = []Command{
	{Name: "model", Description: "choose what model and reasoning effort to use", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic, SubagentView: SubagentViewHidden},
	{Name: "fast", Description: "toggle Fast mode to enable fastest inference with increased plan usage", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic, SubagentView: SubagentViewHidden},
	{Name: "permissions", Description: "choose what Forebrain Harness is allowed to do", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: true, ArgumentHint: "[explain <tool>]", Visibility: VisibilityPublic, SubagentView: SubagentViewGlobal},
	{Name: "skills", Description: "run, add, create, improve, and toggle skills", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic, SubagentView: SubagentViewGlobal},
	{Name: "rename", Description: "rename the current thread", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: true, ArgumentHint: "<title>", Visibility: VisibilityPublic, SubagentView: SubagentViewHidden},
	{Name: "new", Description: "start a new chat during a conversation", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic, SubagentView: SubagentViewHidden},
	{Name: "resume", Description: "resume a saved chat", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic, SubagentView: SubagentViewHidden},
	{Name: "fork", Description: "fork the current chat", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic, SubagentView: SubagentViewHidden},
	{Name: "init", Description: "analyze the repo and create/refresh FOREBRAIN.md guidance", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic, SubagentView: SubagentViewHidden},
	{Name: "compact", Description: "summarize conversation to prevent hitting the context limit", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic, SubagentView: SubagentViewActs},
	{Name: "clear", Description: "clear conversation history and start fresh", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic, SubagentView: SubagentViewHidden},
	{Name: "context", Description: "inspect current context snapshot and compaction state", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic, SubagentView: SubagentViewActs},
	{Name: "memories", Description: "configure memory use and generation", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic, SubagentView: SubagentViewGlobal},
	{Name: "plan", Description: "switch to Plan mode, optionally starting toward a description", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: true, ArgumentHint: "[description]", Visibility: VisibilityPublic, SubagentView: SubagentViewHidden},
	{Name: "agent", Description: "switch to another primary agent and its workspace", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic, SubagentView: SubagentViewHidden},
	{Name: "diff", Description: "show git diff (including untracked files)", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: true, ArgumentHint: "[path]", AvailableInSideConversation: true, Visibility: VisibilityPublic, SubagentView: SubagentViewGlobal},
	{Name: "status", Description: "show current session configuration and usage", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: false, AvailableInSideConversation: true, Visibility: VisibilityPublic, SubagentView: SubagentViewGlobal},
	{Name: "mcp", Description: "manage MCP servers: status, tools, authentication", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic, SubagentView: SubagentViewGlobal},
	{Name: "lsp", Description: "language servers: status, enable, restart, diagnostics", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic, SubagentView: SubagentViewGlobal},
	{Name: "sandbox", Description: "show sandbox runtime mode and backend", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic, SubagentView: SubagentViewGlobal},
	{Name: "exit", Description: "exit Forebrain Harness", AllowedSurfaces: []Surface{SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic, SubagentView: SubagentViewGlobal},
	{Name: "help", Description: "list every command and skill", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic, SubagentView: SubagentViewGlobal},
	{Name: "subagents", Description: "open one of this chat's subagent sessions", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic, SubagentView: SubagentViewGlobal},
	{Name: "goal", Description: "run continuously toward an objective until done", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: true, ArgumentHint: "<objective>", Visibility: VisibilityPublic, SubagentView: SubagentViewHidden},
	{Name: "connect", Description: "configure or switch LLM provider", AllowedSurfaces: []Surface{SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic, SubagentView: SubagentViewGlobal},
	{Name: "migrate", Description: "migrate sessions, memories, skills, and MCP servers from another agent", AllowedSurfaces: []Surface{SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic, SubagentView: SubagentViewGlobal},
}

var runDisallowedCommands = map[string]bool{
	"new":      true,
	"resume":   true,
	"fork":     true,
	"exit":     true,
	"memories": true,
	// Compaction replaces the active model history and must not race an
	// in-flight turn that is still appending to that same history.
	"compact": true,
	// Migration writes large volumes of state and edits configuration; it
	// must not interleave with a running turn the way compaction must not.
	"migrate": true,
}

// IsBuiltinName reports whether name belongs to a command compiled into the
// registry. A dynamic command shadows a builtin of the same name (see All), so
// anything that turns user- or model-authored content into a slash command must
// check this first: a skill named "compact" would otherwise silently take over
// /assembly.
func IsBuiltinName(name string) bool {
	needle := strings.ToLower(strings.TrimSpace(name))
	if needle == "" {
		return false
	}
	if _, removed := removedSlashCommandNames[needle]; removed {
		return true
	}
	return isBuiltinCommand(needle)
}

// All lists every slash command: the built-in commands first, then the
// skills. A built-in always wins its name: a skill called the same is not a
// slash command at all (dynamicCommands leaves it out), though /skills still
// runs it.
func All() []Command {
	dyn := dynamicCommands()
	out := make([]Command, 0, len(commands)+len(dyn))
	for _, cmd := range commands {
		out = append(out, normalizeCommand(cmd))
	}
	for _, d := range dyn {
		cmd := normalizeCommand(d.Command)
		// A skill command acts on the subagent whose view it is typed in: it
		// is sent to that subagent with the skill explicitly activated (D4).
		cmd.SubagentView = SubagentViewActs
		out = append(out, cmd)
	}
	return out
}

func VisibleWithOptions(surface Surface, opts DiscoveryOptions) []Command {
	all := All()
	out := make([]Command, 0, len(all))
	for _, cmd := range all {
		cmd = normalizeCommand(cmd)
		if cmd.Visibility != VisibilityPublic {
			continue
		}
		if !cmd.AllowedOn(surface) {
			continue
		}
		if opts.SubagentView && cmd.SubagentView == SubagentViewHidden {
			continue
		}
		if opts.DuringRun && !availableDuringRun(cmd) {
			continue
		}
		if opts.SideConversation && !cmd.AvailableInSideConversation {
			continue
		}
		if cmd.Name == "fast" && !opts.FastAvailable {
			continue
		}
		out = append(out, cmd)
	}
	return out
}

// InlineArgsRefusal is the one sentence every surface answers when a command
// that takes no inline arguments is given some. refuse is false when the
// command takes arguments, when none were given, or when it is not a known
// command.
func InlineArgsRefusal(name string, args []string) (reply string, refuse bool) {
	if len(args) == 0 {
		return "", false
	}
	cmd, ok := Find(name)
	if !ok || cmd.SupportsInlineArgs {
		return "", false
	}
	return fmt.Sprintf("/%s does not accept inline arguments. Type just /%s.", cmd.Name, cmd.Name), true
}

func Find(name string) (Command, bool) {
	needle := strings.ToLower(strings.TrimSpace(name))
	if needle == "" {
		return Command{}, false
	}
	for _, cmd := range All() {
		if cmd.Name == needle {
			return cmd, true
		}
	}
	return Command{}, false
}

func normalizeCommand(cmd Command) Command {
	if strings.TrimSpace(cmd.CanonicalName) == "" {
		cmd.CanonicalName = cmd.Name
	}
	if strings.TrimSpace(cmd.Category) == "" {
		cmd.Category = defaultCategory(cmd.Name)
	}
	if strings.TrimSpace(cmd.ActionKind) == "" {
		cmd.ActionKind = defaultActionKind(cmd.Name)
	}
	if cmd.SupportsInlineArgs && strings.TrimSpace(cmd.ArgumentHint) == "" {
		cmd.ArgumentHint = "[args]"
	}
	if len(cmd.AllowedModes) == 0 {
		cmd.AllowedModes = []string{"default", "plan"}
	}
	cmd.AvailableDuringRun = availableDuringRun(cmd)
	return cmd
}

func defaultCategory(name string) string {
	switch name {
	case "new", "resume", "fork", "rename", "migrate", "exit":
		return "session"
	case "compact", "diff", "memories":
		return "context"
	case "context":
		return "context"
	case "plan", "agent", "subagents", "model", "goal", "connect":
		return "agent"
	case "permissions":
		return "permissions"
	case "skills", "mcp", "lsp", "sandbox":
		return "tools"
	case "status":
		return "ui"
	default:
		return "general"
	}
}

func defaultActionKind(name string) string {
	switch name {
	case "permissions", "skills", "model", "resume", "memories", "connect", "migrate", "status", "mcp", "lsp":
		return "open-panel"
	case "plan", "init", "goal":
		return "inject-prompt"
	case "new", "fork", "rename", "fast":
		return "mutate-state"
	default:
		return "immediate"
	}
}

func availableDuringRun(cmd Command) bool {
	if cmd.AvailableDuringRun {
		return true
	}
	return !runDisallowedCommands[cmd.Name]
}

// FilterWithOptions lists the commands whose name holds the whole query,
// case-insensitively: the command named by it, then the names starting with
// it, then the names containing it elsewhere. A name that merely holds the
// query's letters scattered in order does not match.
func FilterWithOptions(surface Surface, query string, opts DiscoveryOptions) []Command {
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return VisibleWithOptions(surface, opts)
	}
	exact := make([]Command, 0, 1)
	prefix := make([]Command, 0, len(commands))
	contains := make([]Command, 0, len(commands))
	for _, cmd := range VisibleWithOptions(surface, opts) {
		name := strings.ToLower(cmd.Name)
		switch {
		case name == needle:
			exact = append(exact, cmd)
		case strings.HasPrefix(name, needle):
			prefix = append(prefix, cmd)
		case strings.Contains(name, needle):
			contains = append(contains, cmd)
		}
	}
	return groupByKind(append(append(exact, prefix...), contains...))
}

// groupByKind orders matched commands the way every menu lists them: the
// built-in commands, then the skills, each in match order — except that the
// group holding the best match leads, so an exact skill match is still what
// Enter runs.
func groupByKind(cmds []Command) []Command {
	if len(cmds) == 0 {
		return cmds
	}
	leadSkills := cmds[0].Category == SkillCategory
	out := make([]Command, 0, len(cmds))
	for _, skills := range []bool{leadSkills, !leadSkills} {
		for _, cmd := range cmds {
			if (cmd.Category == SkillCategory) == skills {
				out = append(out, cmd)
			}
		}
	}
	return out
}

// Resolver maps a mention path spec to an absolute path plus a
// display-relative form. ok=false rejects the mention (silently skipped,
// matching the upstream @ mention behavior for unresolvable paths).
type Resolver interface {
	Resolve(path string) (abs string, rel string, ok bool)
}

// CwdResolver resolves mentions against a local working directory (TUI
// surface). Relative paths join Dir; ~ expands; absolute paths are allowed.
type CwdResolver struct {
	Dir string
}

func (r CwdResolver) Resolve(path string) (string, string, bool) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", "", false
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", false
		}
		p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(r.Dir, filepath.FromSlash(p))
	}
	abs := filepath.Clean(p)
	rel := filepath.ToSlash(abs)
	if rr, err := filepath.Rel(r.Dir, abs); err == nil && rr != ".." && !strings.HasPrefix(rr, ".."+string(filepath.Separator)) {
		rel = filepath.ToSlash(rr)
	}
	return abs, rel, true
}

// WorkspaceResolver resolves mentions strictly inside a workspace root
// (gateway/web surfaces). Absolute paths, ~, and .. escapes are rejected.
type WorkspaceResolver struct {
	Root string
}

func (r WorkspaceResolver) Resolve(path string) (string, string, bool) {
	p := strings.TrimSpace(path)
	if p == "" || filepath.IsAbs(p) || p == "~" || strings.HasPrefix(p, "~/") {
		return "", "", false
	}
	abs := filepath.Clean(filepath.Join(r.Root, filepath.FromSlash(p)))
	rr, err := filepath.Rel(r.Root, abs)
	if err != nil || rr == ".." || strings.HasPrefix(rr, ".."+string(filepath.Separator)) {
		return "", "", false
	}
	return abs, filepath.ToSlash(rr), true
}

type DynamicCommand struct {
	Command Command
	Handler func(ctx Context, line string, toks []string) Result
}

var dynamicRegistry struct {
	mu      sync.RWMutex
	sources map[string][]DynamicCommand
}

var removedSlashCommandNames = map[string]struct{}{
	"copy":       {},
	"side":       {},
	"statusline": {},
	"title":      {},
	"todo":       {},
}

func ReplaceDynamicSource(source string, commands []DynamicCommand) error {
	source = strings.TrimSpace(source)
	if source == "" {
		return fmt.Errorf("dynamic source required")
	}
	accepted, errs := filterValidDynamicCommands(commands)
	dynamicRegistry.mu.Lock()
	if dynamicRegistry.sources == nil {
		dynamicRegistry.sources = map[string][]DynamicCommand{}
	}
	if len(accepted) == 0 {
		delete(dynamicRegistry.sources, source)
	} else {
		cloned := make([]DynamicCommand, len(accepted))
		copy(cloned, accepted)
		dynamicRegistry.sources[source] = cloned
	}
	dynamicRegistry.mu.Unlock()
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// ResetDynamic clears every registered dynamic command. Nothing in production
// unregisters commands; this exists so tests in other packages can start from
// an empty registry, which they cannot do themselves because dynamicRegistry is
// unexported.
func ResetDynamic() {
	dynamicRegistry.mu.Lock()
	defer dynamicRegistry.mu.Unlock()
	dynamicRegistry.sources = map[string][]DynamicCommand{}
}

// dynamicCommands lists the registered skill commands by name. One that
// carries a built-in's name is left out: the built-in keeps its name, so a
// skill can never take over a command the user relies on.
func dynamicCommands() []DynamicCommand {
	dynamicRegistry.mu.RLock()
	defer dynamicRegistry.mu.RUnlock()
	if len(dynamicRegistry.sources) == 0 {
		return nil
	}
	var out []DynamicCommand
	for _, cmds := range dynamicRegistry.sources {
		for _, cmd := range cmds {
			if isBuiltinCommand(cmd.Command.Name) {
				continue
			}
			out = append(out, cmd)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Command.Name) < strings.ToLower(out[j].Command.Name)
	})
	return out
}

// isBuiltinCommand reports whether name is one of the built-in commands.
func isBuiltinCommand(name string) bool {
	needle := strings.ToLower(strings.TrimSpace(name))
	for _, cmd := range commands {
		if strings.EqualFold(strings.TrimSpace(cmd.Name), needle) {
			return true
		}
	}
	return false
}

func findDynamic(name string) (DynamicCommand, bool) {
	needle := strings.ToLower(strings.TrimSpace(name))
	if needle == "" {
		return DynamicCommand{}, false
	}
	for _, cmd := range dynamicCommands() {
		if strings.EqualFold(strings.TrimSpace(cmd.Command.Name), needle) {
			return cmd, true
		}
	}
	return DynamicCommand{}, false
}

func filterValidDynamicCommands(commands []DynamicCommand) ([]DynamicCommand, []error) {
	seen := map[string]struct{}{}
	accepted := make([]DynamicCommand, 0, len(commands))
	var errs []error
	for _, item := range commands {
		name := strings.ToLower(strings.TrimSpace(item.Command.Name))
		if name == "" {
			errs = append(errs, fmt.Errorf("dynamic command name required"))
			continue
		}
		if _, removed := removedSlashCommandNames[name]; removed {
			errs = append(errs, fmt.Errorf("dynamic command %q uses a removed slash command name", name))
			continue
		}
		if _, ok := seen[name]; ok {
			errs = append(errs, fmt.Errorf("duplicate dynamic command %q", name))
			continue
		}
		if item.Handler == nil {
			errs = append(errs, fmt.Errorf("dynamic command %q missing handler", name))
			continue
		}
		seen[name] = struct{}{}
		item.Command.Name = name
		accepted = append(accepted, item)
	}
	return accepted, errs
}

// fuzzyMatch returns (matched, score). Score combines subsequence presence
// with bonus for contiguous runs, leading-char hits, and word-boundary hits.
// Higher score = better match. Score 0 with matched=false means no match.
func fuzzyMatch(target, query string) (bool, int) {
	if query == "" {
		return true, 0
	}
	if target == "" {
		return false, 0
	}
	q := []rune(strings.ToLower(query))
	t := []rune(strings.ToLower(target))
	tOrig := []rune(target)
	score := 0
	qi := 0
	consecutive := 0
	prevMatchIdx := -1
	for ti := 0; ti < len(t) && qi < len(q); ti++ {
		if t[ti] == q[qi] {
			bonus := 1
			if ti == 0 || isWordBoundary(tOrig, ti) {
				bonus += 8
			}
			if prevMatchIdx >= 0 && ti == prevMatchIdx+1 {
				consecutive++
				bonus += consecutive * 5
			} else {
				consecutive = 0
			}
			score += bonus
			prevMatchIdx = ti
			qi++
		}
	}
	if qi < len(q) {
		return false, 0
	}
	// Tie-breaker: shorter targets rank slightly higher when scores tie.
	score = score*100 - len(t)
	return true, score
}

func isWordBoundary(t []rune, i int) bool {
	if i == 0 {
		return true
	}
	prev := t[i-1]
	cur := t[i]
	if prev == '/' || prev == '\\' || prev == '_' || prev == '-' || prev == '.' || prev == ' ' {
		return true
	}
	if unicode.IsLower(prev) && unicode.IsUpper(cur) {
		return true
	}
	return false
}

// fuzzyRank returns indices into options sorted by descending score for
// entries that match query. Non-matching entries are excluded.
func fuzzyRank(options []string, query string) []int {
	type scored struct {
		idx   int
		score int
	}
	q := strings.TrimSpace(query)
	if q == "" {
		out := make([]int, len(options))
		for i := range options {
			out[i] = i
		}
		return out
	}
	matches := make([]scored, 0, len(options))
	for i, opt := range options {
		ok, sc := fuzzyMatch(opt, q)
		if !ok {
			continue
		}
		matches = append(matches, scored{idx: i, score: sc})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].idx < matches[j].idx
	})
	out := make([]int, len(matches))
	for i, m := range matches {
		out[i] = m.idx
	}
	return out
}

// Kind is the routing decision for one resolved mention target.
type Kind int

const (
	KindMissing Kind = iota // stat failed — nothing to route
	KindImage               // routed to the image attachment pipeline
	KindOther               // path only; the model reads it with its own tools
)

// imageExts must stay a subset of the formats the attachment pipeline can
// encode (llm.ImageFile). Routing a format it rejects — .bmp was one — turns a
// single mention into a failed turn instead of an ignored one.
var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
}

// Classify stats abs and reports whether it is an attachable image. A
// non-image mention costs one stat regardless of how large the target is:
// nothing is read, because nothing but the path reaches the prompt.
func Classify(abs string) Kind {
	info, err := os.Stat(abs)
	if err != nil {
		return KindMissing
	}
	if info.IsDir() {
		return KindOther
	}
	if !imageExts[strings.ToLower(filepath.Ext(abs))] {
		return KindOther
	}
	// Confirm the bytes agree with the extension before promising an
	// attachment. A file merely named .png is rejected downstream by the
	// encoder, and that rejection fails the whole turn rather than one
	// mention, so anything unrecognized degrades to a plain path here.
	if !hasAttachableImageHeader(abs) {
		return KindOther
	}
	return KindImage
}

// hasAttachableImageHeader sniffs the leading bytes against exactly the
// formats llm.ImageFile can encode.
func hasAttachableImageHeader(abs string) bool {
	f, err := os.Open(abs)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 512)
	n, err := f.Read(head)
	if err != nil && n == 0 {
		return false
	}
	switch http.DetectContentType(head[:n]) {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

// CommandService owns canonical slash parsing and dispatch. Surface-specific
// effects remain behind the narrow handlers in Context.
type CommandService struct{}

// Execute parses and runs one slash command.
func (CommandService) Execute(ctx Context, content string) Result {
	return Execute(ctx, content)
}

// ExecuteDynamic runs only registered skill commands for channel adapters.
func (CommandService) ExecuteDynamic(ctx Context, content string) Result {
	return ExecuteDynamicOnly(ctx, content)
}

// ExecuteDiffSlash is /diff: the uncommitted work of the project the agent
// works in, as a unified diff, or one sentence when there is none to show.
// paths narrow it to those files or directories.
//
// fenced wraps a diff in a diff code fence. That is the one thing the two
// surfaces legitimately differ on: the web chat highlights fenced diffs, while
// the terminal parses the raw text itself and would render the fence markers
// literally. A sentence is never fenced; it also never parses as a diff, which
// is how the terminal tells the two apart.
func ExecuteDiffSlash(projectRoot string, paths []string, fenced bool) string {
	root := strings.TrimSpace(projectRoot)
	diff, err := tool.ProjectDiff(root, paths)
	switch {
	case errors.Is(err, tool.ErrNotGitRepository):
		return root + " is not a git repository, so there is no diff to show."
	case err != nil:
		return err.Error()
	case strings.TrimSpace(diff) == "" && len(paths) > 0:
		return "No uncommitted changes in " + strings.Join(paths, ", ") + "."
	case strings.TrimSpace(diff) == "":
		return "No uncommitted changes."
	case fenced:
		return "```diff\n" + strings.TrimRight(diff, "\n") + "\n```"
	default:
		return diff
	}
}
