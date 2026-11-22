// The /config panel (plan 190): every configuration section the state
// database stores, browsable and editable from the terminal.
//
// A confirmed edit writes the section's JSON through the state store's one
// legal write path — ConfigStore.UpdateSection, with the process-wide section
// validator behind it — and nothing is applied here: the change lands on the
// next turn's configuration sync. The write is the save (plan 321): the panel
// keeps no draft, so what a page shows is what the database holds, and a
// refused candidate leaves the section exactly as it was with the validator's
// own words on the page.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// The panel's fixed rows and notice lines.
const (
	configBackRowLabel = "Back to sections"
	// configAddRowLabel is the add action drawn as a row — the line-mode
	// ladder's stand-in for the footer's Add, in the prototype's own words
	// for the one action an empty state offers (plan 327 D4).
	configAddRowLabel  = "+ Add item"
	configUpRowLabel   = "Up one level"
	configRowEditLabel = "Edit"
	configRowOpenLabel = "Open"
	configRowDropLabel = "Delete this row"
	configRowBackLabel = "Back"

	configBoolOnLabel  = "True"
	configBoolOffLabel = "False"
	// configDefaultActionLabel is how the edit view's footer action reads
	// (plan 326 D4): an action on the editor of a text or number setting,
	// never a value among the real ones and never a row of a choice list
	// (owner 2026-11-18, strict 1:1 with the prototype).
	configDefaultActionLabel = "Default"
	// configDefaultActionKey is how that action's key reads in the footer: the
	// editor reserves Ctrl+U for it (plan 326 D1).
	configDefaultActionKey = "Ctrl+U"
	// The rows page's footer actions (plan 326 D2): add for the collection,
	// remove for the entry row the cursor is on.
	configAddActionLabel    = "Add"
	configRemoveActionLabel = "Remove"
	// The removal confirmation's two rows (plan 326 D5): Keep is
	// preselected, so Enter alone changes nothing.
	configRemoveConfirmLabel = "Remove item"
	configKeepConfirmLabel   = "Keep item"

	configNewTextRow   = "Text"
	configNewNumberRow = "Number"
	configNewTrueRow   = "True"
	configNewFalseRow  = "False"
	configNewListRow   = "List"
	// configNewBooleanRow is the value-type toggle's boolean row (plan 328
	// D4): the type is one row, and its value is picked as the two real
	// ones rather than typed (owner 2026-11-18: a choice lists real values).
	configNewBooleanRow = "Boolean"
	configNewObjectRow  = "Object"
)

// configPanelSnapshot is what the panel reads when it opens: the stored
// runtime sections as their own JSON, whether this machine has Claude
// Code — the condition the interop switch is shown under (owner 2026-10-30) —
// and the install-wide model table, which is state rather than a section and
// is what turns a stored row id into the model name a reader recognises
// (plan 313).
type configPanelSnapshot struct {
	Stored     map[string]json.RawMessage
	ClaudeCode bool
	Models     []appcfg.LLMModelConfig
}

// configPanelSession is what /config needs from a session — an optional
// capability, the way MemorySettings is, so a session double with no
// configuration behind it keeps compiling.
type configPanelSession interface {
	ConfigPanelSections(ctx context.Context) (configPanelSnapshot, error)
	UpdateConfigSection(ctx context.Context, section string, raw json.RawMessage) error
}

// ConfigPanelSections reads the composed runtime configuration once: each
// stored section as the JSON its fb_config row holds. The panel keeps no
// subscription — a save updates its own copy, and anything another surface
// wrote shows up on the next open.
func (s *ChatSession) ConfigPanelSections(ctx context.Context) (configPanelSnapshot, error) {
	if s == nil || s.Env == nil {
		return configPanelSnapshot{}, fmt.Errorf("the configuration panel is unavailable: no runtime environment")
	}
	root, err := s.Env.ComposedPersistedConfig(ctx)
	if err != nil {
		return configPanelSnapshot{}, err
	}
	stored := make(map[string]json.RawMessage, len(configSectionSchemas))
	for _, name := range process.RootConfigSections() {
		raw, err := process.MarshalRootSection(root, name)
		if err != nil {
			return configPanelSnapshot{}, err
		}
		if len(raw) > 0 {
			stored[name] = raw
		}
	}
	// Judged once per open: the interop switch is shown only on a machine
	// that has Claude Code's directory (plan 195 §3.7).
	return configPanelSnapshot{
		Stored: stored, ClaudeCode: process.ClaudeCodeDetected(),
		// The model table is state, not a section: the panel only names row
		// ids with it (plan 313).
		Models: root.LLMModels,
	}, nil
}

// UpdateConfigSection stores one section's JSON as its fb_config row — nil
// removes the row — through the state store, so the write is validated
// against the rest of the installed configuration before it lands.
func (s *ChatSession) UpdateConfigSection(ctx context.Context, section string, raw json.RawMessage) error {
	if s == nil || s.Env == nil {
		return fmt.Errorf("the configuration panel is unavailable: no runtime environment")
	}
	if _, ok := configSectionSchemaFor(section); !ok {
		return fmt.Errorf("unknown configuration section %q", section)
	}
	store, err := s.Env.ConfigStore()
	if err != nil {
		return err
	}
	return store.UpdateSection(ctx, section, func(json.RawMessage) (json.RawMessage, error) {
		return raw, nil
	})
}

// configPanel is the open /config panel: the snapshot it read when it opened
// and the selector it drives. An edit lands in the store as it is confirmed
// (plan 321): the panel holds no draft of its own.
type configPanel struct {
	sel      Selector
	renderer *Renderer
	session  configPanelSession
	raw      map[string]json.RawMessage
	// claudeCode is whether this machine has Claude Code, read when the
	// panel opened: a ClaudeCodeOnly field is offered only while it holds.
	claudeCode bool
	// models is the install-wide model table as the panel opened it: a stored
	// row id is named through it, since no section carries the names (plan
	// 313).
	models []appcfg.LLMModelConfig
	// section is the stored section the open page writes — a projection
	// writes the section it presents — and write is the call that stores it.
	// Together they are the page's one write path (plan 321).
	section string
	write   func(raw json.RawMessage) error
	// overviewRow is the section list's opening row: the section the user
	// last had open, so backing out of a page returns to where they were
	// without the panel keeping any chrome of its own (plan 325).
	overviewRow int
	// tab is the tab of the section page open. Every page of the panel draws it
	// in the header — the tab bar stays however deep the page sits (plan 350).
	tab string
	// tabStep is the arrow a page of the open section asked to step with, taken
	// by the section page that owns the list: a page's ←/→ steps the whole panel
	// to the next tab, the way the prototype's goOverview does (plan 350).
	tabStep int
}

func newConfigPanel(sel Selector, renderer *Renderer, session configPanelSession) *configPanel {
	return &configPanel{sel: sel, renderer: renderer, session: session, raw: map[string]json.RawMessage{}}
}

// handleConfig opens the /config panel. With an argument it opens that
// section's page and returns to the section list afterwards; bare /config
// opens the list.
func (c *commandController) handleConfig(ctx context.Context, args []string) bool {
	if c == nil || c.session == nil || c.selector == nil {
		return true
	}
	session, ok := c.session.(configPanelSession)
	if !ok {
		c.renderer.PrintError(fmt.Errorf("/config is unavailable: this session has no configuration source mounted"))
		return true
	}
	panel := newConfigPanel(c.selector, c.renderer, session)
	snapshot, err := session.ConfigPanelSections(ctx)
	if err != nil {
		c.renderer.PrintError(err)
		return true
	}
	panel.raw = snapshot.Stored
	panel.claudeCode = snapshot.ClaudeCode
	panel.models = snapshot.Models
	start := ""
	if len(args) > 0 {
		schema, known := panelSectionSchemaFor(args[0])
		if !known {
			c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "config", Content: "/config " + args[0] + " is not a known configuration section.\nAvailable: " + strings.Join(configSectionNames(), ", "), Final: true})
			return true
		}
		start = schema.Name
	}
	panel.run(ctx, start)
	return true
}

// run walks the panel: the section list, then one section's page, until the
// user closes it. Esc returns one level — from a section back to the list,
// and from the list out of the panel. The list reopens on the section that
// was last open (plan 325).
func (p *configPanel) run(ctx context.Context, start string) {
	name := start
	if start != "" {
		if _, known := panelSectionSchemaFor(start); !known {
			name = ""
		}
	}
	for {
		if name == "" {
			chosen, ok := p.chooseSection()
			if !ok {
				return
			}
			name = chosen
		}
		if !p.editSection(ctx, name) {
			return
		}
		p.leaveSection(name)
		name = ""
	}
}

// leaveSection is where the list reopens once one section's page closed: the
// section the user left, or — when a page stepped with an arrow — the first
// section of the tab it stepped to (plan 350, the prototype's goOverview).
func (p *configPanel) leaveSection(name string) {
	step, tab := p.tabStep, ""
	p.tabStep = 0
	if step != 0 {
		tab = p.stepTab(step)
	}
	if tab != "" {
		for i, schema := range panelSections() {
			if schema.Tab == tab {
				p.overviewRow = i
				return
			}
		}
	}
	p.overviewRow = p.sectionRow(name)
}

// offeredTabs are the tabs this machine shows, in the bar's order: the whole
// table, minus a tab whose every section the platform hid.
func (p *configPanel) offeredTabs() []string {
	tabs := make([]string, 0, len(configTabLabels))
	for _, tab := range configTabLabels {
		for _, schema := range panelSections() {
			if schema.Tab == tab.Name {
				tabs = append(tabs, tab.Name)
				break
			}
		}
	}
	return tabs
}

// stepTab is the tab a step lands on, wrapping around the tabs this machine
// offers; "" when the open page's tab is not one of them.
func (p *configPanel) stepTab(step int) string {
	tabs := p.offeredTabs()
	at := -1
	for i, tab := range tabs {
		if tab == p.tab {
			at = i
		}
	}
	if at < 0 || len(tabs) == 0 {
		return ""
	}
	n := len(tabs)
	return tabs[((at+step)%n+n)%n]
}

// chrome is the header every page of the panel draws: the tab bar, each offered
// tab carrying the number of sections it holds, with the tab of the open page
// marked. The pages below it carry no title and no summary of their own (plan
// 350; owner 2026-11-18/21 — a command panel stays bare).
func (p *configPanel) chrome() *panelChrome {
	labels := make([]string, 0, len(configTabLabels))
	short := make([]string, 0, len(configTabLabels))
	counts := make([]int, 0, len(configTabLabels))
	active := 0
	for _, tab := range configTabLabels {
		sections := 0
		for _, schema := range panelSections() {
			if schema.Tab == tab.Name {
				sections++
			}
		}
		if sections == 0 {
			continue
		}
		if tab.Name == p.tab {
			active = len(labels)
		}
		labels = append(labels, tab.Name)
		short = append(short, tab.Short)
		counts = append(counts, sections)
	}
	return &panelChrome{Labels: labels, Short: short, Counts: counts, Active: active}
}

// How each kind of page names its two keys in the footer, word for word from
// the prototype's footerHTML (plan 350): a section page's Enter opens a
// setting and its Esc goes back, a choice or an editor saves, a removal
// confirms.
var (
	configFieldKeys   = &configPageKeys{Enter: "open", Esc: "go back"}
	configChoiceKeys  = &configPageKeys{Enter: "save", Esc: "cancel"}
	configConfirmKeys = &configPageKeys{Enter: "confirm", Esc: "cancel"}
)

// page opens one of the panel's pages — the tab bar in its head and the page's
// own rows — and answers the row Enter chose. A page's ←/→ is taken here and
// held for the section page that owns the list, so one arrow steps the whole
// panel to another tab however deep it sits (plan 350); every caller unwinds on
// !ok, which is what that step reports.
func (p *configPanel) page(label string, keys *configPageKeys, items []SelectItem, defaultIdx int) (int, bool, error) {
	res, err := p.plainPage(label, keys, items, defaultIdx, false)
	return res.Row, res.OK, err
}

// actionPage is page's action-aware form, for the pages that read which action
// fired on which row. Only a page that carries footer actions takes the action
// path; every other page draws as the panel's own page, so a selector with no
// footer actions keeps reading the pages it always read.
func (p *configPanel) actionPage(label string, keys *configPageKeys, items []SelectItem, defaultIdx int, actions ...FooterAction) (ActionResult, error) {
	rich, ok := p.sel.(ActionRichSelector)
	if !ok {
		// A selector that draws no panel at all — piped stdin — reads the
		// plain picker: no chrome, no keys, just the rows.
		return p.plainPage(label, keys, items, defaultIdx, false)
	}
	res, err := rich.SelectRichActions(label, items, defaultIdx, ActionOptions{
		Page: true, Chrome: p.chrome(), Keys: keys, FooterActions: actions,
	})
	if err != nil {
		return ActionResult{Row: -1}, err
	}
	// An arrow steps to the tab beside this page's, held for the section page
	// that owns the list (plan 350).
	if res.TabStep != 0 {
		p.tabStep = res.TabStep
		return ActionResult{Row: -1}, nil
	}
	return res, nil
}

// plainPage opens one of the panel's pages that carries no footer actions: the
// tab bar in its head, the page's rows and the keys it names. The selector's
// own config page draws it when it has one, and the plain picker answers a
// selector that draws no panel.
func (p *configPanel) plainPage(label string, keys *configPageKeys, items []SelectItem, defaultIdx int, filter bool) (ActionResult, error) {
	if pages, ok := p.sel.(configPageSelector); ok {
		res, err := pages.SelectConfigPage(label, items, defaultIdx, ActionOptions{Page: true, Chrome: p.chrome(), Keys: keys, Filter: filter})
		if err != nil {
			return ActionResult{Row: -1}, err
		}
		if res.TabStep != 0 {
			p.tabStep = res.TabStep
			return ActionResult{Row: -1}, nil
		}
		return res, nil
	}
	idx, ok, err := p.sel.SelectRich(label, items, defaultIdx)
	if err != nil {
		return ActionResult{Row: -1}, err
	}
	return ActionResult{Row: idx, OK: ok}, nil
}

// sectionRow is the section list's row for one section, so the list reopens
// where the user left it; 0 when the list no longer holds it.
func (p *configPanel) sectionRow(name string) int {
	for i, schema := range panelSections() {
		if schema.Name == name {
			return i
		}
	}
	return 0
}

// chooseSection lists every section, tabbed by the group its descriptor names.
// The page carries no title and no subtitle of its own (owner 2026-11-21): the
// tab bar and the rows say everything, and a command panel stays bare.
func (p *configPanel) chooseSection() (string, bool) {
	items, names := p.sectionItems()
	idx, ok, err := selectRichTabbedPage(p.sel, "", items, TabbedSelectOptions{DefaultIdx: p.overviewRow}, p.chrome())
	if err != nil {
		p.printError(err)
		return "", false
	}
	if !ok || idx < 0 || idx >= len(names) {
		return "", false
	}
	return names[idx], true
}

// panelSections is the section table as this machine offers it: the whole
// table, minus the approval-and-sandbox tab when there is no sandbox to
// configure. A setting that cannot take effect is not shown at all — neither
// its section nor its tab heading, which disappears with the last section
// carrying it (owner 2026-10-31, plan 215 §3.4). The table itself stays whole,
// because it is also the coverage list every configuration section must appear
// in.
func panelSections() []configSectionSchema {
	if safety.SandboxAvailable() {
		return configSectionSchemas
	}
	out := make([]configSectionSchema, 0, len(configSectionSchemas))
	for _, schema := range configSectionSchemas {
		if schema.Tab == configTabApproval {
			continue
		}
		out = append(out, schema)
	}
	return out
}

// panelSectionSchemaFor is configSectionSchemaFor over the sections this
// machine offers: a section hidden by panelSections answers as unknown, so
// /config <hidden-section> behaves like the list that no longer holds it.
func panelSectionSchemaFor(name string) (configSectionSchema, bool) {
	name = strings.TrimSpace(name)
	for _, schema := range panelSections() {
		if schema.Name == name {
			return schema, true
		}
	}
	return configSectionSchema{}, false
}

// sectionItems is the section list: the section's own title, what it is for,
// and whether the database holds a row for it. The name a reader would type to
// /config is an argument, not a row label — the panel speaks in titles, never
// in the names forebrain.yaml happens to use (plan 321).
func (p *configPanel) sectionItems() ([]SelectItem, []string) {
	sections := panelSections()
	items := make([]SelectItem, 0, len(sections))
	names := make([]string, 0, len(sections))
	for _, schema := range sections {
		items = append(items, SelectItem{
			Label:       schema.Title,
			Description: schema.Summary,
			Status:      p.sectionStatus(schema),
			Category:    schema.Tab,
		})
		names = append(names, schema.Name)
	}
	return items, names
}

// sectionStatus is the section list's right-edge word: a section the database
// has no row for is the one reading its defaults, which is what the prototype's
// own status column says. A section it holds says nothing.
func (p *configPanel) sectionStatus(schema configSectionSchema) string {
	if len(p.raw[schema.storeSection()]) > 0 {
		return ""
	}
	return configDefaultStatus
}

// editSection runs one section's page. It reports whether the panel stays
// open: true means the user asked to go back to the list. Every field edit
// writes the section as it is confirmed, so the page holds no draft and the
// only way out is back (plan 321). On a selector with footer actions the way
// back is the Esc ladder alone (plan 326 D2); without one the page keeps its
// tail row, the line mode's ladder.
func (p *configPanel) editSection(ctx context.Context, name string) bool {
	schema, ok := configSectionSchemaFor(name)
	if !ok {
		p.printError(fmt.Errorf("unknown configuration section %q", name))
		return true
	}
	p.section = schema.storeSection()
	p.tab = schema.Tab
	p.write = func(raw json.RawMessage) error {
		return p.session.UpdateConfigSection(ctx, p.section, raw)
	}
	defer func() {
		p.section, p.write, p.tab = "", nil, ""
	}()
	doc := p.sectionDoc(name)
	notice := ""
	tailRows := p.configActionSelector() == nil
	for {
		items, owners := p.fieldItems(schema, doc, tailRows)
		res, err := p.actionPage(p.sectionNotice(schema, notice), configFieldKeys, items, -1)
		idx, ok := res.Row, res.OK
		if err != nil {
			p.printError(err)
			return false
		}
		// An arrow on this page steps to the tab beside it: the list reopens
		// there, on that tab's first section (plan 350).
		if p.tabStep != 0 {
			return true
		}
		notice = ""
		if !ok || idx < 0 || idx >= len(owners) {
			return true
		}
		switch owners[idx] {
		case configRowBack:
			return true
		default:
			updated, note := p.editField(doc, schema.Fields[owners[idx]])
			doc, notice = updated, note
		}
	}
}

// The page's one action row carries a negative owner so a field index and an
// action never collide. Backing out is also what Esc does; the row is there
// for a reader who is looking for it.
const configRowBack = -2

// configActionSelector is the panel's selector when it can carry footer
// actions, nil when it cannot (piped stdin, a scripted double). Those paths
// keep the tail action rows the interactive pages replace with footer
// actions and the Esc ladder (plan 326 D2).
func (p *configPanel) configActionSelector() ActionRichSelector {
	if actions, ok := p.sel.(ActionRichSelector); ok {
		return actions
	}
	return nil
}

// fieldItems is one section's page: its settings, each with the value it holds
// in the row's value column and its hint beside the label, then the way back
// when the page keeps its tail rows. A field's Group names the fixed page
// section it renders under (plan 325 D2), the prototype's own labels.
func (p *configPanel) fieldItems(schema configSectionSchema, doc any, tailRows bool) ([]SelectItem, []int) {
	items := make([]SelectItem, 0, len(schema.Fields)+1)
	owners := make([]int, 0, len(schema.Fields)+1)
	for i, field := range schema.Fields {
		if !configFieldListed(field, p.claudeCode) {
			continue
		}
		items = append(items, SelectItem{Label: field.Label, Value: p.configFieldRow(doc, field), Category: field.Group})
		owners = append(owners, i)
	}
	if tailRows {
		items = append(items, SelectItem{Label: configBackRowLabel, Description: "Back to the section list"})
		owners = append(owners, configRowBack)
	}
	return items, owners
}

// configFieldRow is one setting's row on the section page: the value it holds
// in the row's value column.
func (p *configPanel) configFieldRow(doc any, field configField) string {
	return p.configFieldDescription(doc, field)
}

// configTypePinRow is one install-wide type pin's row (plan 324 D7): the value
// column names the model row the pin stores — or the sentence for a pin nobody
// set — and the status names the thinking level in force. The help sentence
// belongs to the type's editor, not to its row (plan 350).
func (p *configPanel) configTypePinRow(doc any, field configField, path []string) (string, string, string) {
	ref := refOfRow(doc, path...)
	if ref == "" {
		return "", configFollowConversation, ""
	}
	row, _ := configLookupValue(doc, path)
	settings, _ := row.(map[string]any)
	return "", p.configModelRowLabel(ref), configEffortSummary(configStringField(settings, configEffortKey))
}

// configRowKey is the key a row's path ends at — a definition's own name in the
// map it sits in, "" for a path that names none.
func configRowKey(path []string) string {
	if len(path) == 0 {
		return ""
	}
	return path[len(path)-1]
}

// refOfRow is one stored row's model reference, empty when it names none.
func refOfRow(doc any, path ...string) string {
	row, _ := configLookupValue(doc, path)
	settings, _ := row.(map[string]any)
	return strings.TrimSpace(configStringField(settings, "model"))
}

// editBuiltinSubagents is the Built-in subagents entry's page (P4, the
// prototype's builtinSubagents kind): the six built-in types, each one row
// naming the install-wide model row and thinking level it runs on, each
// opening the model-and-level page its kind fixes — a type's own template for
// the four the model may dispatch, the system row's for the two the system
// dispatches. The rows live under three agents-section keys, which is why the
// page is rendered here rather than read off the descriptor: the four public
// types under subagent_models, the plan reviewer under plan_reviewer, and the
// goal evaluator under goal_evaluator.
func (p *configPanel) editBuiltinSubagents(doc any, field configField) (any, string) {
	names := make([]string, 0, len(agent.PublicSubagentNames)+len(agent.SystemSubagentNames))
	names = append(names, agent.PublicSubagentNames...)
	names = append(names, agent.SystemSubagentNames...)
	tailRows := p.configActionSelector() == nil
	for {
		items := p.builtinSubagentItems(doc)
		rows := make([]struct {
			name     string
			path     []string
			template string
		}, 0, len(names))
		for _, name := range names {
			path := configBuiltinRowPath(name)
			template := configTemplateTypeDef
			if slices.Contains(agent.SystemSubagentNames, name) {
				template = configTemplateGlobalSystemRow
			}
			rows = append(rows, struct {
				name     string
				path     []string
				template string
			}{name, path, template})
		}
		if tailRows {
			items = append(items, SelectItem{Label: configUpRowLabel, Description: "Back up one level"})
			rows = append(rows, struct {
				name     string
				path     []string
				template string
			}{})
		}
		idx, ok, err := p.page("", configFieldKeys, items, -1)
		if err != nil {
			p.printError(err)
			return doc, ""
		}
		if !ok || idx < 0 || idx >= len(rows) {
			return doc, ""
		}
		row := rows[idx]
		if row.name == "" {
			return doc, ""
		}
		doc, _ = p.editTemplatePage(doc, row.path, row.template, row.name)
	}
}

// builtinSubagentItems is the entry page's six rows (P4): one per built-in
// type, in the prototype's order — the four the model may dispatch, then the
// two the system dispatches — each naming the model row its pin stores and
// the thinking level in force, with the prototype's help sentence beside the
// name.
func (p *configPanel) builtinSubagentItems(doc any) []SelectItem {
	names := make([]string, 0, len(agent.PublicSubagentNames)+len(agent.SystemSubagentNames))
	names = append(names, agent.PublicSubagentNames...)
	names = append(names, agent.SystemSubagentNames...)
	items := make([]SelectItem, 0, len(names))
	for _, name := range names {
		description, value, status := p.configTypePinRow(doc, configField{Hint: ""}, configBuiltinRowPath(name))
		items = append(items, SelectItem{Label: name, Description: description, Value: value, Status: status})
	}
	return items
}

// configBuiltinRowPath is the agents-section key one built-in type's
// install-wide row lives under (the prototype's builtinPath): the four public
// types are per-type rows, the two system types are global rows of their own.
func configBuiltinRowPath(name string) []string {
	switch name {
	case "plan-reviewer":
		return []string{configPlanReviewerKey}
	case "goal-evaluator":
		return []string{configGoalEvaluatorKey}
	default:
		return []string{configSubagentModelsKey, name}
	}
}

// configEffortSummary names a stored thinking level: empty is the model row's
// own default, and everything else is the level through turn's one table.
func configEffortSummary(level string) string {
	if strings.TrimSpace(level) == "" {
		return configRowDefaultEffort
	}
	return turn.EffortEnglishLabel(level)
}

// configFieldListed reports whether the panel offers one field on this machine.
// "Allow network access" is not offered on Windows: the restricted-token sandbox cannot take
// the network away, so the switch would control nothing (plan 206 §3.2). The
// platform that decides is the one running the sandbox, not the one a browser
// happens to be on. A ClaudeCodeOnly field is offered only where Claude Code
// is installed: without its directory the interop switch would point at
// nothing (plan 195 §3.7) — and the stored value rides along untouched.
func configFieldListed(field configField, claudeCode bool) bool {
	if runtime.GOOS == "windows" && field.Path == "network_access" {
		return false
	}
	if field.ClaudeCodeOnly && !claudeCode {
		return false
	}
	return true
}

// sectionNotice is what a section page carries under its tab bar: the last
// outcome — a value that did not land, or the row a page refused — and nothing
// else. The page's own title and summary are gone (owner 2026-11-18/21): the
// tab bar above it and the rows below it say everything. A section whose every
// field the Claude Code gate hid is the prototype's empty state (plan 327 D4,
// prototype html:157): it says what is missing instead of showing nothing.
func (p *configPanel) sectionNotice(schema configSectionSchema, notice string) string {
	if !p.claudeCode && len(schema.Fields) > 0 {
		gated := true
		for _, field := range schema.Fields {
			if !field.ClaudeCodeOnly {
				gated = false
				break
			}
		}
		if gated {
			return joinNotice(notice, "No compatible installation found\nClaude Code interop appears when a local installation is available.")
		}
	}
	return notice
}

// joinNotice is a page's outcome and its state sentence as one label: the
// outcome first, since it answers the key just pressed, and nothing left empty
// when both are.
func joinNotice(notice, state string) string {
	switch {
	case notice == "":
		return state
	case state == "":
		return notice
	}
	return notice + "\n" + state
}

// save writes the open page's document as its section's row — the one write
// path an edit takes (plan 321) — and keeps the panel's own copy in step so the
// section list's marker is right the moment it is shown again. A projection
// saves under the section it presents (plan 314): the document is that
// section's whole row, so keys the projection does not show travel back
// untouched and nothing is lost.
func (p *configPanel) save(doc any) error {
	raw, err := configMarshalDoc(doc)
	if err != nil {
		return err
	}
	if err := p.write(raw); err != nil {
		return err
	}
	if len(raw) == 0 {
		delete(p.raw, p.section)
	} else {
		p.raw[p.section] = raw
	}
	return nil
}

// reloadSection re-reads the open page's document from what the store still
// holds: a refused write is not applied, so the page must not keep showing the
// value that did not land.
func (p *configPanel) reloadSection() any {
	return p.sectionDoc(p.section)
}

func (p *configPanel) printError(err error) {
	if p.renderer != nil && err != nil {
		p.renderer.PrintError(err)
	}
}

// sectionDoc decodes the section's stored row into the generic tree the
// editor stages edits in. An absent row is a nil document: every field then
// reads as unset. A projection decodes the row of the section it presents
// (plan 314), which is the document its fields are paths into.
func (p *configPanel) sectionDoc(name string) any {
	store := name
	if schema, ok := configSectionSchemaFor(name); ok {
		store = schema.storeSection()
	}
	raw := p.raw[store]
	if len(raw) == 0 {
		return nil
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	if store == "agents" {
		// The panel edits whatever keys the stored row holds, so a setting the
		// schema retired is removed here rather than left to the descriptor
		// table (plan 315): it can then be neither shown as a row nor written
		// back by configMarshalDoc, and a save drops it from the store.
		dropRetiredAgentKeys(doc)
	}
	return doc
}

// dropRetiredAgentKeys removes the per-agent settings the configuration no
// longer carries from a decoded agents section. The one such key is an agent
// definition's provider list: every model is a row of the global table now, and
// every model choice is a reference to one (plan 315). It lives here rather
// than in the schema because the panel's generic row editor is driven by the
// document, not by a descriptor per row.
func dropRetiredAgentKeys(doc any) {
	section, ok := doc.(map[string]any)
	if !ok {
		return
	}
	definitions, ok := section["definitions"].(map[string]any)
	if !ok {
		return
	}
	for _, value := range definitions {
		definition, ok := value.(map[string]any)
		if !ok {
			continue
		}
		delete(definition, "llm_providers")
		// A public built-in's entry is a model-and-level pin (P5): the
		// prompt rewrite and the switch it once carried are retired, so the
		// panel drops them here — the same in-memory retirement the loader's
		// normalize applies, and a save persists the stripped shape.
		entries, ok := definition["subagents"].([]any)
		if !ok {
			continue
		}
		for _, row := range entries {
			entry, ok := row.(map[string]any)
			if !ok {
				continue
			}
			name, _ := entry["name"].(string)
			if !slices.Contains(agent.PublicSubagentNames, strings.ToLower(strings.TrimSpace(name))) {
				continue
			}
			delete(entry, "system_prompt")
			delete(entry, "enabled")
		}
	}
}

// configSectionNames lists the section names in the panel's own order, for
// the sentence an unknown /config argument gets.
func configSectionNames() []string {
	sections := panelSections()
	names := make([]string, 0, len(sections))
	for _, schema := range sections {
		names = append(names, schema.Name)
	}
	return names
}

// editField routes one setting to its widget: a table opens its rows, a value
// that turns out to be a table is edited as rows whatever the descriptor says
// (that is how approval_policy's granular object is reached), and a scalar
// gets the widget its kind names. A table whose own place a template names
// opens that template's fixed-field page instead of its rows (plan 329 D2):
// the projection's per-type rows and the plan reviewer's global row are
// two-field pages, not key lists.
func (p *configPanel) editField(doc any, field configField) (any, string) {
	path := configFieldPath(field)
	cur, _ := configLookupValue(doc, path)
	if field.Kind == configFieldBuiltinSubagents {
		return p.editBuiltinSubagents(doc, field)
	}
	if field.Kind == configFieldRows || configIsContainer(cur) {
		if template := configTemplateFor(p.section, path); configTemplateIsObject(template) && configIsContainer(cur) {
			// The field is the row here (a projection's per-type field, the
			// plan reviewer's global row), so its own label names the page.
			return p.editTemplatePage(doc, path, template, field.Label)
		}
		return p.editRows(doc, path, field)
	}
	switch field.Kind {
	case configFieldBool3:
		return p.editBool3(doc, path, field, cur)
	case configFieldEnum:
		return p.editEnum(doc, path, field, cur)
	case configFieldNumber:
		return p.editNumber(doc, path, field, cur)
	case configFieldEffort:
		return p.editEffort(doc, path, field, p.fieldPrompt(field), cur)
	case configFieldModel:
		return p.editModel(doc, path, field, cur)
	default:
		return p.editText(doc, path, field, cur)
	}
}

// editBool3 is the switch: on or off, the two real values alone (plan 326
// D4, owner 2026-11-18 — no Default row, no choice-footer Default: going
// back to a setting's own default is the edit view's action, and a boolean's
// page offers none).
func (p *configPanel) editBool3(doc any, path []string, field configField, cur any) (any, string) {
	items := []SelectItem{
		{Label: configBoolOnLabel},
		{Label: configBoolOffLabel},
	}
	defaultIdx := 0
	if value, ok := cur.(bool); ok {
		defaultIdx = 1
		if value {
			defaultIdx = 0
		}
		// The option the field already holds carries the check (plan 327 D2);
		// an unset switch names neither On nor Off.
		items[defaultIdx].Status = configChoiceCheckStatus
	}
	idx, ok, err := p.page("", configChoiceKeys, items, defaultIdx)
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok {
		return doc, ""
	}
	if idx == 0 {
		return p.assign(doc, path, true, false, field.Label)
	}
	return p.assign(doc, path, false, false, field.Label)
}

// editEnum picks one of the values the loader accepts, so free text can never
// reach the store with a mode it would reject. An option with a Chinese label
// shows the label alone: the reader is choosing a behavior, not a
// configuration key (plan 203 §0.8). The list holds the real values only
// (plan 326 D4): no Default row — the edit view's footer owns that.
func (p *configPanel) editEnum(doc any, path []string, field configField, cur any) (any, string) {
	items := make([]SelectItem, 0, len(field.Options))
	for _, option := range field.Options {
		item := SelectItem{Label: option}
		if label := field.OptionLabels[option]; label != "" {
			item = SelectItem{Label: label}
		}
		items = append(items, item)
	}
	defaultIdx := 0
	current := -1
	if text, ok := cur.(string); ok {
		for i, option := range field.Options {
			if option == text {
				defaultIdx = i
				current = i
			}
		}
	} else if field.DefaultValue != "" {
		for i, option := range field.Options {
			if option == field.DefaultValue {
				defaultIdx = i
			}
		}
	}
	// The option the field already holds carries the check (plan 327 D2); an
	// unset enum names none of them, preselected or not.
	if current >= 0 {
		items[current].Status = configChoiceCheckStatus
	}
	idx, ok, err := p.page("", configChoiceKeys, items, defaultIdx)
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok {
		return doc, ""
	}
	return p.assign(doc, path, field.Options[idx], false, field.Label)
}

// editNumber asks for an integer and holds it to the loader's bounds: an
// out-of-range value is refused in the field's own words, and a bound the
// loader simply honours (a clamp) lands on the bound with the panel saying
// which value it stored. The editor's footer carries Default (plan 326 D4):
// Ctrl+U unsets the key; on a selector without footer actions an empty input
// is the unset, as it always was.
func (p *configPanel) editNumber(doc any, path []string, field configField, cur any) (any, string) {
	text, ok, unset, err := p.editScalar(field, configScalarInput(cur))
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok {
		return doc, ""
	}
	if unset {
		return p.assign(doc, path, nil, true, field.Label)
	}
	value, convErr := strconv.Atoi(text)
	if convErr != nil {
		return doc, "Enter a valid whole number."
	}
	if value < field.Min {
		return doc, fmt.Sprintf("Use a value of at least %d.", field.Min)
	}
	notice := ""
	if field.Max > 0 && value > field.Max {
		if !field.Clamp {
			return doc, fmt.Sprintf("Use a value no greater than %d.", field.Max)
		}
		value = field.Max
		notice = fmt.Sprintf("Adjusted to the maximum: %d.", value)
	}
	// The LSP idle exit's own floor (plan 327 D3, prototype html:137): 0 is
	// the default and everything else must be a real timeout.
	if field.Path == "idle_timeout" && value != 0 && value < 60 {
		return doc, "Use 0 for the default, or at least 60 seconds."
	}
	updated, note := p.assign(doc, path, value, false, field.Label)
	if note != "" {
		return updated, note
	}
	return updated, notice
}

// editText takes free text. The editor's footer carries Default (plan 326
// D4): Ctrl+U unsets the key rather than storing an empty string the loader
// would read as an empty setting; on a selector without footer actions an
// empty input is the unset, as it always was. A multiline field keeps its own
// whitespace and an empty save is its unset (plan 328 D2): an empty prompt
// means no prompt, not a prompt that says nothing.
func (p *configPanel) editText(doc any, path []string, field configField, cur any) (any, string) {
	text, ok, unset, err := p.editScalar(field, configScalarInput(cur))
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok {
		return doc, ""
	}
	if unset {
		return p.assign(doc, path, nil, true, field.Label)
	}
	if field.Multiline {
		if strings.TrimSpace(text) == "" {
			return p.assign(doc, path, nil, true, field.Label)
		}
		return p.assign(doc, path, text, false, field.Label)
	}
	if text == "" {
		// Reached only on the action path — a fallback empty is the unset
		// above — where the footer's Default is the way to unset (plan 326
		// D4): an empty Enter saves nothing rather than an empty string.
		return doc, "the value may not be empty; nothing was written"
	}
	return p.assign(doc, path, text, false, field.Label)
}

// editPath edits one row at a path's place: the panel's editor, then the
// prototype's absolute check before anything is stored (html:149) — a relative
// path is refused in the field's own words and nothing is written.
func (p *configPanel) editPath(doc any, rowPath []string, field configField, cur any) (any, string) {
	text, ok, unset, err := p.editScalar(field, configScalarInput(cur))
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok {
		return doc, ""
	}
	if unset {
		return p.assign(doc, rowPath, nil, true, field.Label)
	}
	if !configAbsolutePath(text) {
		return doc, configPathRefusal
	}
	return p.assign(doc, rowPath, text, false, field.Label)
}

// configRoleHelp is a template field's help sentence on the row it is opened
// on. The system prompt is the one field both roles carry, and the prototype
// gives each its own sentence (helpPrimary/helpSubagent): a primary agent's is
// appended to the built-in prompt, a subagent's replaces it whole.
func configRoleHelp(field configField, settings map[string]any) string {
	if field.Path != "system_prompt" {
		return configFieldHint(field)
	}
	if configAgentRolePrimary(settings) {
		return "Appended to the end of the built-in system prompt. It never replaces it."
	}
	return "Replaces the built-in system prompt whole."
}

// configAbsolutePath reports whether a path is one the loader takes whole: a
// rooted path, or a Windows drive path (the prototype's absolute rule).
func configAbsolutePath(path string) bool {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "/") {
		return true
	}
	return len(path) > 2 && path[1] == ':' && (path[2] == '\\' || path[2] == '/')
}

// editScalar opens a text or number setting's editor and reports what it
// ended with: the text Enter saved, or the Default action having been fired
// (plan 326 D4). On a selector without footer actions the Default action has
// no key to fire, and an empty input stands for it — the line mode's ladder.
// A multiline field opens the textarea instead (plan 328 D2): plain Enter
// starts a new line, Ctrl+Enter saves.
//
// The editor is one of the panel's own pages (plan 350): the tab bar above it,
// the setting's help under its input where the prototype's editor-help sits,
// and the footer's Default on Ctrl+U only where the setting has a default to
// restore. An empty answer unsets the key, the way an empty field reads in the
// prototype.
func (p *configPanel) editScalar(field configField, value string) (text string, ok, unset bool, err error) {
	actions := p.configActionSelector()
	if actions == nil {
		text, ok, err = p.sel.Input(p.fieldPrompt(field), value)
		return text, ok, strings.TrimSpace(text) == "" && ok, err
	}
	res, err := actions.InputActions("", value, p.editorOptions(field, false))
	if err != nil || !res.OK {
		return "", false, false, err
	}
	if res.Action == FooterActionDefault {
		return "", true, true, nil
	}
	return res.Text, true, strings.TrimSpace(res.Text) == "", nil
}

// editorOptions shapes one of the panel's editor pages: its header, the help
// under the input, and the footer's Default where the setting has one (plan
// 350). secret opens the masked editor.
func (p *configPanel) editorOptions(field configField, secret bool) ActionOptions {
	opts := ActionOptions{
		Page: true, Chrome: p.chrome(), Keys: configChoiceKeys,
		Label: configEditorLabel, Help: configFieldHint(field),
		Multiline: field.Multiline, Secret: secret,
	}
	if secret {
		opts.Label = configSecretReplacementLabel
	}
	if configFieldHasDefault(field) && !secret {
		opts.FooterActions = []FooterAction{{Label: configDefaultActionLabel, Key: configDefaultActionKey, Kind: FooterActionDefault}}
	}
	return opts
}

// configFieldHasDefault reports whether a setting has a default of its own to
// restore — the condition the prototype's editor shows its Ctrl+U hint under
// (html:196, hasDefault). The descriptor's DefaultValue is that default: the
// value the panel preselects when the setting holds none.
func configFieldHasDefault(field configField) bool {
	return strings.TrimSpace(field.DefaultValue) != ""
}

// editSecret replaces one secret (plan 328 D3, prototype html:106/118/159).
// The editor opens empty-handed — a replacement is what it asks for, never
// the stored value, which would leak through the field — and the interactive
// selector's Secret field masks what is typed. An empty answer writes
// nothing: removing the secret is the row's Remove action. A line-based
// selector reads its answers in the clear, so on one of those the edit is
// refused outright rather than echoed.
func (p *configPanel) editSecret(doc any, path []string, field configField, cur any) (any, string) {
	actions := p.configActionSelector()
	if actions == nil {
		return doc, configSecretLineNotice
	}
	res, err := actions.InputActions("", "", p.editorOptions(field, true))
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	replacement, ok := res.Text, res.OK
	if !ok {
		return doc, ""
	}
	if strings.TrimSpace(replacement) == "" {
		return doc, configSecretEmptyNotice
	}
	return p.assign(doc, path, replacement, false, field.Label)
}

// editEffort is one thinking level's picker (plan 319): the levels the model
// row beside it can be given, then the empty value — Row default, which follows
// that row's own default — last. The level is a property of the model, never a
// free string: a row without one has nothing to offer, because the loader
// refuses an effort that names no model, so the page says which row to set
// first and opens nothing. Row default stores the empty level itself (plan
// 324 D5): the loader reads "" as no override, and an unset key and an empty
// one mean the same thing everywhere the level is read.
func (p *configPanel) editEffort(doc any, path []string, field configField, prompt string, cur any) (any, string) {
	if configSiblingModelRef(doc, path) == "" {
		return doc, configEffortNeedsModelNotice
	}
	levels := p.configEffortLevels(doc, path, cur)
	items := make([]SelectItem, 0, len(levels)+1)
	stored := configStoredEffort(cur)
	defaultIdx := len(levels)
	for i, level := range levels {
		item := SelectItem{Label: turn.EffortEnglishLabel(level)}
		if level == stored {
			defaultIdx = i
			// The level the field already holds carries the check (plan 327
			// D2) — only when one is stored at all.
			if cur != nil {
				item.Status = configChoiceCheckStatus
			}
		}
		items = append(items, item)
	}
	rowDefault := SelectItem{Label: configRowDefaultEffort}
	if cur != nil && stored == "" {
		rowDefault.Status = configChoiceCheckStatus
	}
	items = append(items, rowDefault)
	idx, ok, err := p.page("", configChoiceKeys, items, defaultIdx)
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok || idx < 0 || idx >= len(items) {
		return doc, ""
	}
	if idx == len(items)-1 {
		return p.assign(doc, path, "", false, field.Label)
	}
	return p.assign(doc, path, levels[idx], false, field.Label)
}

// configEffortLevels is what one stored level may be set to, in the one order
// every effort picker draws: the model row's levels, and the level the row
// already stores when the catalog no longer names it — it stays on the list so
// the page shows what is configured instead of quietly reading as unset.
func (p *configPanel) configEffortLevels(doc any, path []string, cur any) []string {
	levels := p.configModelEfforts(configSiblingModelRef(doc, path))
	if stored := configStoredEffort(cur); stored != "" && !slices.Contains(levels, stored) {
		levels = append(levels, stored)
	}
	return levels
}

// configModelEfforts is what one stored model reference takes: the model row's
// own levels over the catalog's, trimmed to the levels this build can put on the
// wire for that row's protocol and put in the order every picker draws them
// (turn.SetupReasoningEfforts). Off is one of them exactly when the row declares
// it and the protocol can send it, which is the same rule the catalog's `none`
// entry is read by. A reference nothing answers declines to guess: no levels.
func (p *configPanel) configModelEfforts(ref string) []string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	provider, model, apiPath, rowID := "", "", "", ""
	if entry, ok := appcfg.ResolveModelRef(&appcfg.Root{LLMModels: p.models}, "main", ref); ok {
		provider, model, apiPath, rowID = entry.Provider, entry.Model, entry.APIPath, entry.ID
	} else if before, after, pair := strings.Cut(ref, "/"); pair {
		// A reference the model table no longer carries: the pair is still what
		// the catalog is asked about, and nothing is inferred past it.
		provider, model = strings.ToLower(strings.TrimSpace(before)), strings.TrimSpace(after)
	}
	if provider == "" || model == "" {
		return nil
	}
	levels, _ := llm.ReasoningEffortsRow(rowID, provider, model)
	return turn.SetupReasoningEfforts(provider, model, apiPath, levels)
}

// editModel is one model reference's picker (plan 324): the field's own empty
// value first — what empty means is a property of where the reference sits —
// then the install's global model rows, each naming where its requests go. A
// stored reference that names no row stays on the list (the goal evaluator's
// flavor keeps none), so a pin whose row was deleted reads as what it is
// until it is re-picked. Changing or clearing the reference takes the
// thinking level beside it in the same write (D5).
func (p *configPanel) editModel(doc any, path []string, field configField, cur any) (any, string) {
	stored, _ := cur.(string)
	stored = strings.TrimSpace(stored)
	ids := []string{field.ModelEmptyValue}
	items := []SelectItem{{Label: field.ModelEmpty}}
	if field.ModelFlavor == configModelFlavorModel {
		items[0].Description = configModelConversationDesc
	} else {
		// The row templates' own sentence for an unset pin (P4): the type
		// row's and the system row's empty choices each say what it means.
		items[0].Description = field.ModelEmptyDesc
	}
	defaultIdx := 0
	for _, row := range p.models {
		ids = append(ids, row.ID)
		item := SelectItem{
			Label:       p.configModelRowLabel(row.ID),
			Description: row.BaseURL + row.APIPath,
		}
		if row.ID == stored {
			defaultIdx = len(items)
			// The row the reference already names carries the check (plan
			// 327 D2); the empty option's own check is set below, where the
			// flavor's empty value is known.
			item.Status = configChoiceCheckStatus
		}
		items = append(items, item)
	}
	if stored != "" && stored != field.ModelEmptyValue && !slices.Contains(ids, stored) && field.ModelFlavor != configModelFlavorModel {
		ids = append(ids, stored)
		items = append(items, SelectItem{Label: p.configModelRowLabel(stored), Status: configChoiceCheckStatus})
		defaultIdx = len(items) - 1
	}
	// The empty option is the current choice while the reference names no
	// row of the table (plan 327 D2). The memory flavor's empty option is
	// the explicit "current": it is the current choice only when that value
	// is what is stored — an unset key is no choice at all — while every
	// other flavor's empty is what an unset reference already means.
	if field.ModelFlavor == configModelFlavorMemory {
		if stored == field.ModelEmptyValue {
			items[0].Status = configChoiceCheckStatus
		}
	} else if stored == "" {
		items[0].Status = configChoiceCheckStatus
	}
	// The choice page is the option list alone (plan 327 D2, owner
	// 2026-11-18): the field's own label heads it, with no instructional
	// sub copy — the rows' descriptions already say where requests go. The
	// model picker carries a filter box (owner 2026-11-22): hundreds of rows
	// narrow like /model's own list.
	res, err := p.plainPage("", configChoiceKeys, items, defaultIdx, true)
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !res.OK || res.Row < 0 || res.Row >= len(items) {
		return doc, ""
	}
	return p.assignModel(doc, path, field, stored, ids[res.Row])
}

// assignModel stores one picked model reference and writes the section once
// (plan 324 D5): the empty option unsets the key — the memory flavor's empty
// is the explicit "current" — a row stores its id, and a reference that
// changed or cleared drops the thinking level beside it in the same write;
// picking the row it already names keeps the level it was chosen for.
func (p *configPanel) assignModel(doc any, path []string, field configField, stored, picked string) (any, string) {
	var value any
	if picked != "" {
		value = picked
	}
	updated, err := configAssign(doc, path, value, picked == "", field.Label)
	if err != nil {
		p.printError(err)
		return doc, err.Error()
	}
	if picked == "" || picked != stored {
		if updated, err = configDropSiblingEffort(updated, path, field.Label); err != nil {
			p.printError(err)
			return doc, err.Error()
		}
	}
	if err := p.save(updated); err != nil {
		return p.reloadSection(), err.Error()
	}
	return updated, ""
}

// configDropSiblingEffort removes the thinking level stored beside a model
// reference (plan 324 D5): the level was chosen for the model that carried it,
// so a reference that changes or clears takes its level along — one write,
// never a level left beside a model it was not chosen for.
func configDropSiblingEffort(doc any, path []string, label string) (any, error) {
	if len(path) < 2 {
		return doc, nil
	}
	sibling := configJoinPath(path[:len(path)-1], configEffortKey)
	if _, exists := configLookupValue(doc, sibling); !exists {
		return doc, nil
	}
	return configAssign(doc, sibling, nil, true, label)
}

// configModelValueText is a model reference as every value column shows it
// (plan 324 D6): the row's label through the install's table, and nothing at
// all when the setting names no row — an unset value is empty in the
// prototype's display(), and the empty option's own label belongs to the
// picker that offers it, not to a row nobody set (plan 350).
func (p *configPanel) configModelValueText(field configField, value any) string {
	ref, _ := value.(string)
	ref = strings.TrimSpace(ref)
	if ref == "" || (field.ModelEmptyValue != "" && ref == field.ModelEmptyValue) {
		return ""
	}
	return p.configModelRowLabel(ref)
}

// assign applies one edit and writes the section once: the confirmed edit is
// the save (owner 2026-11-16, plan 321), so there is no draft to write later.
// label is the setting's own name, the one the panel showed the user: a refusal
// names it rather than the configuration path it lives at (plan 313 Step 5b). A
// candidate the store refuses is not applied — the page is read back from what
// the database still holds — and the validator's own words become its next line.
func (p *configPanel) assign(doc any, path []string, value any, remove bool, label string) (any, string) {
	updated, err := configAssign(doc, path, value, remove, label)
	if err != nil {
		p.printError(err)
		return doc, err.Error()
	}
	if err := p.save(updated); err != nil {
		return p.reloadSection(), err.Error()
	}
	return updated, ""
}

// fieldPrompt is a field page's heading: its label, then what the field means.
func (p *configPanel) fieldPrompt(field configField) string {
	return configFieldHead(field)
}

// configFieldHead is a field page's head: the label, then the field's help
// line — the hint the descriptor carries, or the allowed-range line a number
// field without one falls back to (plan 327 D3, prototype html:35).
func configFieldHead(field configField) string {
	if hint := configFieldHint(field); hint != "" {
		return field.Label + "\n" + hint
	}
	return field.Label
}

// configFieldHint is one field's help line: the descriptor's own hint, or —
// for a number the prototype gives no words of its own — its allowed range
// with the unit it is counted in (plan 327 D3, prototype html:35).
func configFieldHint(field configField) string {
	if hint := strings.TrimSpace(field.Hint); hint != "" {
		return hint
	}
	if field.Kind == configFieldNumber && field.Max > 0 {
		unit := ""
		if field.Unit != "" {
			unit = " " + field.Unit
		}
		return fmt.Sprintf("Allowed range: %d–%d%s.", field.Min, field.Max, unit)
	}
	return ""
}

// configFieldDescription is a field's row: the label the page lists it under,
// and the value it holds in the row's value column (plan 350). The field's help
// is not part of the row — it belongs to the setting's editor, the one place
// the prototype shows it — and a setting carries no status of its own: the
// status column is reserved for the words a row is marked with (a definition's
// role, a shared subagent's switch, a built-in type's effective model).
func (p *configPanel) configFieldDescription(doc any, field configField) string {
	value, exists := configLookupValue(doc, configFieldPath(field))
	// A section that is its own setting (approval_preset, a bare string)
	// reads the whole document at an empty path, which always answers
	// present; an absent row is still an unset field.
	if doc == nil {
		exists = false
	}
	if field.Kind == configFieldModel {
		return p.configModelValueText(field, value)
	}
	return configFieldValueText(field, value, exists)
}

// configRowAdd and configRowUp own the row page's two action rows; a row's
// own index is never negative, and the section page's own actions use
// different values again.
const (
	configRowAdd = -3
	configRowUp  = -4
)

// editRows edits one table or list as rows. A row that holds a table opens
// its own rows one level down, so a nested section — a subagent entry inside an
// agent's list, an MCP server inside a profile — is reachable without the panel
// knowing that shape in advance. On a selector with footer actions the page is
// the prototype's rows page (plan 326 D2): the footer adds a row (^A Add) and
// removes the entry row under the cursor (^D Remove, through the
// confirmation), Enter opens a row, and Esc backs out one level. Without the
// capability the page keeps its tail action rows instead — the line mode's
// ladder. initialNotice, when given, is the page's first outcome line: the
// guidance a created primary agent opens with (plan 313).
func (p *configPanel) editRows(doc any, path []string, field configField, initialNotice ...string) (any, string) {
	notice := ""
	if len(initialNotice) > 0 {
		notice = initialNotice[0]
	}
	actions := p.configActionSelector()
	for {
		container, _ := configLookupValue(doc, path)
		keys := configContainerKeys(container)
		list := configContainerIsList(field, container)
		// An agent's subagent list carries a row for every shared subagent, the
		// ones it has not enabled included: this page is where they are switched
		// on (plan 314). Those rows have no entry in the document yet.
		if list && agentSubagentsPath(path) {
			keys = append(keys, missingSharedSubagentKeys(doc, container)...)
		}
		label := rowsNotice(keys, notice)
		notice = ""
		if actions == nil {
			items, owners := p.rowItems(doc, path, container, keys, list, true)
			idx, ok, err := p.page(label, configFieldKeys, items, -1)
			if err != nil {
				p.printError(err)
				return doc, ""
			}
			if !ok || idx < 0 || idx >= len(owners) {
				return doc, ""
			}
			switch owners[idx] {
			case configRowAdd:
				doc, notice = p.addRow(doc, path, field)
			case configRowUp:
				return doc, ""
			default:
				doc, notice = p.editRow(doc, path, keys[owners[idx]], field)
			}
			continue
		}
		items, _ := p.rowItems(doc, path, container, keys, list, false)
		pageActions := []FooterAction{
			{Label: configAddActionLabel, Key: "a", Kind: FooterActionAdd},
			{
				Label: configRemoveActionLabel, Key: "d", Kind: FooterActionRemove,
				// Remove serves the collection's entry rows (plan 350): every
				// row that stands for a stored entry, container rows
				// included. A shared subagent's row is its switch, not an
				// entry, and a row Add or Up owns is no entry either.
				RowActive: func(itemIdx int) bool {
					return itemIdx >= 0 && itemIdx < len(keys) && p.configRowIsEntry(doc, path, keys[itemIdx])
				},
			},
		}
		res, err := p.actionPage(label, configFieldKeys, items, -1, pageActions...)
		if err != nil {
			p.printError(err)
			return doc, ""
		}
		switch {
		case res.Action == FooterActionAdd:
			doc, notice = p.addRow(doc, path, field)
		case res.Action == FooterActionRemove:
			if res.Row < 0 || res.Row >= len(keys) {
				continue
			}
			rowPath := configJoinPath(path, keys[res.Row])
			if !p.confirmRemoveRow(items[res.Row].Label, doc, rowPath, field) {
				continue
			}
			doc, notice = p.assign(doc, rowPath, nil, true, field.Label)
		case res.OK && res.Row >= 0 && res.Row < len(keys):
			doc, notice = p.editRow(doc, path, keys[res.Row], field)
		default:
			// Esc: up one level (plan 326 D3).
			return doc, ""
		}
	}
}

// configRowIsEntry reports whether one stored row is an entry of a removable
// collection: every row that stands for a stored entry, a container row — an
// agent definition, an MCP server, a subagent — as much as the scalar rows a
// template opens straight into an editor (plan 350, the prototype's rowField
// marks every collection row entry). A shared subagent's row is its switch,
// not an entry: removing the definition is not this agent's to do.
func (p *configPanel) configRowIsEntry(doc any, path []string, key string) bool {
	value, _ := configLookupValue(doc, configJoinPath(path, key))
	if agentSubagentsPath(path) && sharedRowName(doc, key, value) != "" {
		return false
	}
	return true
}

// rowItems is one container's rows: every entry, then add and back when the
// page keeps its tail rows. doc and the row's own path travel with it because
// what a row means is a property of where it sits: a definition's primary
// flag is its status marker, and a subagent entry's model is read in the
// order dispatch reads it (plan 313). A generic row carries what it holds in
// the value column (plan 325 D4) — a container counted, a scalar as it
// reads — and keeps the description for the rows the panel knows more about.
func (p *configPanel) rowItems(doc any, path []string, container any, keys []string, list bool, tailRows bool) ([]SelectItem, []int) {
	items := make([]SelectItem, 0, len(keys)+2)
	owners := make([]int, 0, len(keys)+2)
	for i, key := range keys {
		value, exists := configLookupValue(container, []string{key})
		if !exists && list {
			// A shared subagent this agent has not enabled has no entry: the row
			// stands for the placeholder that says so, and nothing is written
			// until the switch is turned on (plan 314).
			if isSharedSubagentName(doc, key) {
				value = map[string]any{"name": strings.ToLower(strings.TrimSpace(key)), "enabled": false}
			}
		}
		description, rowValue := p.configRowColumns(doc, path, key, value)
		items = append(items, SelectItem{
			Label:       p.configRowLabelAt(doc, path, key, i, list, value),
			Description: description,
			Value:       rowValue,
			Status:      p.configRowStatus(doc, path, key, value),
		})
		owners = append(owners, i)
	}
	if tailRows {
		items = append(items,
			SelectItem{Label: configAddRowLabel, Description: "Adds a row at this level"},
			SelectItem{Label: configUpRowLabel, Description: "Back up one level"},
		)
		owners = append(owners, configRowAdd, configRowUp)
	}
	return items, owners
}

// configRowStatus is a row's right-edge marker (plan 313): the definitions
// page marks the agents that are primary agents with the prototype's
// primary-listed status, and an agent's subagent list marks the shared
// subagents (plan 314). Every other row has none — a definition keyed by a
// built-in subagent type is that type's install-wide default, not an agent, and
// the loader's own rule is what decides.
func (p *configPanel) configRowStatus(doc any, path []string, key string, value any) string {
	if agentSubagentsPath(path) && sharedRowName(doc, key, value) != "" {
		return configSharedStatus
	}
	if len(path) != 1 || path[0] != configDefinitionsPath {
		return ""
	}
	row, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	// The prototype's three words (its agent-list rows): an explicit false is
	// the install-wide subagent role, a true is a primary agent, and a
	// definition that never chose is just an agent.
	primary, isBool := row["primary"].(bool)
	switch {
	case isBool && !primary:
		return configSubagentStatus
	case primary:
		return configPrimaryStatus
	default:
		return configAgentStatus
	}
}

// configRowLabel names a row: a map's key as written in the configuration, a
// list's element by its 1-based place, so the two never read alike.
func configRowLabel(key string, index int, list bool) string {
	if list {
		return "#" + strconv.Itoa(index+1)
	}
	return key
}

// configRowLabelAt names one stored row of a page. On an agent's subagent list a
// shared subagent is named by the name it carries: that is its identity in the
// definitions map, and a list of "#1, #2" would say nothing about which subagent
// is which (plan 314).
func (p *configPanel) configRowLabelAt(doc any, path []string, key string, index int, list bool, value any) string {
	if agentSubagentsPath(path) {
		if name := sharedRowName(doc, key, value); name != "" {
			return name
		}
	}
	return configRowLabel(key, index, list)
}

// configRowDescription is a row's second column: what it holds right now. A
// container holds no single value to print — the row opens the rows
// themselves (plan 350, the prototype's display() on a container).
func configRowDescription(value any) string {
	if configIsContainer(value) {
		return ""
	}
	if value == nil {
		return "(empty)"
	}
	return configScalarText(value)
}

// The agents section's parts the panel reads by name: the definitions map, the
// subagents list inside one definition, and the install-wide per-type model
// rows. Everything below keys on a row's real path, never on a descriptor's
// Path: editRows hands the same field down at every nesting level, so a guard
// reading field.Path alone would fire inside every agent's own rows (plan 313).
const (
	configDefinitionsPath   = "definitions"
	configSubagentsKey      = "subagents"
	configSubagentModelsKey = "subagent_models"
	// configPlanReviewerKey is the other install-wide row in the agents
	// section the "Subagents" page presents (plans 313, 314): the exit-plan
	// reviewer's global row. The shared subagent definitions live in the
	// definitions map like every other definition (P3).
	configPlanReviewerKey = "plan_reviewer"
	// configGoalEvaluatorKey is the /goal evaluator's global row (plan 299), the
	// plan reviewer's sibling one type over (P4's Built-in subagents page).
	configGoalEvaluatorKey = "goal_evaluator"
	configPrimaryStatus    = "Primary"
	// The other two role words the prototype's definitions list writes (P3):
	// an explicit primary:false is the install-wide subagent role, and a
	// definition that never chose a role is just an agent.
	configSubagentStatus = "Subagent"
	configAgentStatus    = "Agent"
	// The created-fields sentence (the prototype's keyName/createEditable
	// readOnly help, word for word): what opening the name or the primary
	// switch on a saved definition answers.
	configCreatedFieldsNotice = "Set when the definition was created; it cannot be changed."
	// configDefaultStatus is the section list's one status word: the database
	// holds no row for the section, so it reads its built-in defaults — the
	// prototype's own marker (plan 321).
	configDefaultStatus = "Default"
	// configDefaultArrow is how an unset field with a default of its own
	// reads in the value column (plan 327 D6, prototype html:117): the word
	// and the default it names, for a bool, a number with its unit, an
	// enum's option or a prompt's built-in text.
	configDefaultArrow = "Default → "
	// A shared custom subagent is marked on an agent's page, where the switch
	// beside it is the agent's.
	configSharedStatus = "Shared"
	// configRowDefaultEffort is what an empty stored level means on a row that
	// names a model: the model row's own default (plan 313's reading, kept).
	configRowDefaultEffort = "Follow the model default"
	// configEffortKey is the field every thinking level is stored under, and
	// configEffortModelKey the sibling it belongs to: the two are keys of one
	// stored map at every level of the agents section (plan 319).
	configEffortKey      = "reasoning_effort"
	configEffortModelKey = "model"
	// configEffortHeading heads a level's picker when it is opened from a row
	// rather than from a field of its own.
	configEffortHeading = "Thinking level"
	// configChoiceCheckStatus marks the option a choice page's field already
	// holds (plan 327 D2, the prototype's check column): the picker
	// preselects the row and the right edge names it. A choice page shows
	// the option list alone (owner 2026-11-18: no "Choose one" filler sub),
	// so the head is the field's own label and help, nothing more.
	configChoiceCheckStatus = "✓"
	// configModelConversationDesc is the goal evaluator's empty option (plan 324
	// D1): its empty value is not an unset key read as a default, it is the
	// conversation's own model.
	configModelConversationDesc = "Use the model of the current conversation."
	// configEffortNeedsModelNotice is what a level's row answers while the model
	// beside it names none: the level is the model's, and the loader refuses a
	// pair whose level names no model at all.
	configEffortNeedsModelNotice = "Choose a model before setting its thinking level."
	// configSystemSubagentNotice is the prototype's refusal, word for word
	// (plan 313, scene system-stray-entry): the panel says why before the
	// loader does, and the loader still judges the save on its own.
	configSystemSubagentNotice = "Dispatched by the system. Not configurable per agent; remove this entry."
	// The secret column's two words (plan 328 D3, prototype html:118): a set
	// value is hidden and an unset one says so.
	configSecretHiddenStatus = "[hidden]"
	configSecretUnsetStatus  = "Not set"
	// configSecretReplacementLabel is the private editor's own name for what
	// it asks (prototype html:159): a replacement, never the stored value.
	configSecretReplacementLabel = "Replacement value"
	// configSecretEmptyNotice answers an empty replacement (plan 328 D3):
	// nothing was asked for, so nothing is written.
	configSecretEmptyNotice = "no replacement was given; nothing was written"
	// configPathRefusal is the prototype's own words for a path that is not
	// absolute (html:149): the editor keeps what was typed and nothing is
	// stored.
	configPathRefusal = "Enter an absolute path."
	// configEditorLabel names the value an editor asks for, in the prototype's
	// own words for the box (html:173, editor-label): the value typed replaces
	// what the setting holds (plan 350).
	configEditorLabel = "New value"
	// configSecretLineNotice refuses the private edit on a selector that
	// reads its answers in the clear (plan 328 D3): a piped line has no
	// screen to hide a secret on, so the edit is refused rather than echoed.
	configSecretLineNotice = "This value is private and cannot be typed on a non-interactive terminal."
	// configFollowConversation is what a built-in type with no model of its
	// own runs on, in the prototype's words (plan 313; plan 324 D7 keeps the
	// sentence and drops the trailing word).
	configFollowConversation = "Follow conversation"
	// configAgentOverridePrefix and configGlobalDefaultPrefix name where a
	// subagent entry's model comes from. The prototype draws the same fact in a
	// status column; the panel keeps it in the row's description.
	configAgentOverridePrefix = "agent override: "
	configGlobalDefaultPrefix = "global default: "
	// The hooks section's own words (plan 321): the event a matcher list
	// belongs to, the matcher that starts one, the action a matcher runs, and
	// the key a matcher's actions are stored under.
	configHooksKey            = "hooks"
	configHookEventHeading    = "Hook event"
	configHookMatcherHeading  = "Tool matcher"
	configHookAllToolsMatcher = "*"
	configHookTypeHeading     = "Action type"
	// The stored keys the template layer resolves paths by (plan 329 D2):
	// an MCP server list, its OAuth block and its per-tool overrides, an LSP
	// server map, and the writable-roots list. Keys, not descriptors,
	// because the generic editor hands the same field down every level.
	configMCPKey           = "mcp_servers"
	configMCPOAuthKey      = "oauth"
	configMCPToolsKey      = "tools"
	configLSPServersKey    = "servers"
	configWritableRootsKey = "writable_roots"
	// configAdditionalFieldsGroup heads the keys a row holds that its
	// template does not name: they stay listed, editable through the generic
	// editor, and the save writes them back untouched (no data loss).
	configAdditionalFieldsGroup = "Additional fields"
	// configSystemStatus is the right-edge word every row of a system
	// entry's page carries (prototype html:128): the entry is dispatched by
	// the system, so nothing on the page is this agent's to set.
	configSystemStatus = "System"
	// configSharedSubagentNotice is what a read-only shared row answers when one
	// of its definition's fields is opened: the definition is edited on the
	// "Subagents" page, never here (plan 314).
	configSharedSubagentNotice = "A shared subagent: its definition is edited in Subagents, only the switch is set here"
)

// agentSubagentsPath reports whether path is one primary agent's subagent list —
// the one place a shared subagent is switched on or off for that agent (plan
// 314). It is a path check rather than a field check because editRows hands the
// same descriptor down at every nesting level.
func agentSubagentsPath(path []string) bool {
	return len(path) == 3 && path[0] == configDefinitionsPath && path[2] == configSubagentsKey
}

// sharedSubagentNamesIn lists the names of the install-wide shared subagents an
// agents document holds, in their configured order. They are definitions whose
// key plays the subagent role (P3): not a built-in type's shell, not a primary
// agent, and carrying the body a subagent runs on. The panel reaches the section
// as a document, so the rule reads the keys and fields it holds rather than the
// configuration types (appcfg.IsReservedDefinitionID is the same list of built-in
// keys the write paths refuse).
func sharedSubagentNamesIn(doc any) []string {
	definitions, _ := configLookupValue(doc, []string{configDefinitionsPath})
	rows, _ := definitions.(map[string]any)
	names := make([]string, 0, len(rows))
	for key := range rows {
		name := strings.ToLower(strings.TrimSpace(key))
		if configSubagentDefinition(rows[key], name) {
			names = append(names, name)
		}
	}
	sort.SliceStable(names, func(i, j int) bool {
		left, right := configDefinitionOrder(rows[names[i]]), configDefinitionOrder(rows[names[j]])
		if left != right {
			// A definition nobody positioned (0) lists after the ordered ones,
			// the way pkg/config's SubagentDefinitionNames reads the field.
			if left == 0 {
				return false
			}
			if right == 0 {
				return true
			}
			return left < right
		}
		return names[i] < names[j]
	})
	return names
}

// configSubagentDefinition reports whether one definitions entry plays the
// subagent role: the role flag is not set, the key is not a built-in type's, and
// the entry carries the body a subagent runs on.
func configSubagentDefinition(value any, name string) bool {
	entry, ok := value.(map[string]any)
	if !ok {
		return false
	}
	if primary, _ := entry["primary"].(bool); primary {
		return false
	}
	if appcfg.IsReservedDefinitionID(name) {
		return false
	}
	return strings.TrimSpace(configStringField(entry, "description")) != "" ||
		strings.TrimSpace(configStringField(entry, "system_prompt")) != "" ||
		strings.TrimSpace(configStringField(entry, "model")) != "" ||
		strings.TrimSpace(configStringField(entry, "reasoning_effort")) != "" ||
		strings.TrimSpace(configStringField(entry, "tools")) != "" ||
		configListLen(entry["allowed_tools"]) > 0 ||
		entry["continuable"] != nil
}

// configDefinitionOrder reads a definition's order field: the position the
// migration gave it when it came out of the shared-subagent registry. 0 is
// "unordered", and a document the panel loaded as YAML carries the number in
// whatever type that decoder used.
func configDefinitionOrder(value any) int {
	entry, ok := value.(map[string]any)
	if !ok {
		return 0
	}
	switch order := entry["order"].(type) {
	case float64:
		return int(order)
	case int:
		return order
	case int64:
		return int(order)
	default:
		return 0
	}
}

// configListLen counts a document list's elements, 0 for anything that is not a
// list.
func configListLen(value any) int {
	list, _ := value.([]any)
	return len(list)
}

// configSharedSubagent finds one shared subagent definition by name.
func configSharedSubagent(doc any, name string) (map[string]any, bool) {
	definitions, _ := configLookupValue(doc, []string{configDefinitionsPath})
	rows, _ := definitions.(map[string]any)
	key := strings.ToLower(strings.TrimSpace(name))
	entry, ok := rows[key].(map[string]any)
	if !ok || !configSubagentDefinition(entry, key) {
		return nil, false
	}
	return entry, true
}

// isSharedSubagentName reports whether name is one of the shared subagents the
// definitions map holds.
func isSharedSubagentName(doc any, name string) bool {
	return slices.Contains(sharedSubagentNamesIn(doc), strings.ToLower(strings.TrimSpace(name)))
}

// sharedRowName is the shared subagent a row stands for, empty when it stands
// for anything else: the name an entry carries, or — for a placeholder row that
// has no entry yet — the registry name the row is keyed by (plan 314).
func sharedRowName(doc any, key string, value any) string {
	if entry, ok := value.(map[string]any); ok {
		name := strings.ToLower(strings.TrimSpace(configStringField(entry, "name")))
		if name != "" && isSharedSubagentName(doc, name) {
			return name
		}
		return ""
	}
	if name := strings.ToLower(strings.TrimSpace(key)); isSharedSubagentName(doc, name) {
		return name
	}
	return ""
}

// missingSharedSubagentKeys names the registry's subagents one agent's list has
// no entry for: they are rows all the same, because a disabled shared subagent
// is a row that says so with its switch off (owner 2026-11-16, plan 314).
func missingSharedSubagentKeys(doc any, container any) []string {
	rows, _ := container.([]any)
	present := make([]string, 0, len(rows))
	for _, row := range rows {
		entry, _ := row.(map[string]any)
		present = append(present, strings.ToLower(strings.TrimSpace(configStringField(entry, "name"))))
	}
	missing := make([]string, 0, len(rows))
	for _, name := range sharedSubagentNamesIn(doc) {
		if !slices.Contains(present, name) {
			missing = append(missing, name)
		}
	}
	return missing
}

// configRowColumns is one stored row's two text columns for the agents
// section: the description a reader reads and the value the right edge holds
// (plan 325 D4) — or, for one of an agent's subagent entries, the model it
// will actually run on in the order dispatch resolves it, and the verdict a
// system subagent entry gets (plan 313), carried as the description alone.
// Everything else keeps configRowDescription, now as the row's value.
func (p *configPanel) configRowColumns(doc any, path []string, key string, value any) (string, string) {
	// A row whose place names a secret never shows what it holds (plan 328
	// D3): the value column answers with the mask, whatever the row's shape.
	if configIsSecretPath(append(path, key)...) {
		return "", configSecretDisplay(value, true)
	}
	if len(path) != 3 || path[0] != configDefinitionsPath || path[2] != configSubagentsKey {
		return "", configRowDescription(value)
	}
	if shared := sharedRowName(doc, key, value); shared != "" {
		return configSharedRowSummary(doc, shared), ""
	}
	row, ok := value.(map[string]any)
	if !ok {
		return "", configRowDescription(value)
	}
	name := strings.ToLower(strings.TrimSpace(configStringField(row, "name")))
	if name == "" {
		return "", configRowDescription(value)
	}
	if slices.Contains(agent.SystemSubagentNames, name) {
		return configSystemSubagentNotice, ""
	}
	if !slices.Contains(agent.PublicSubagentNames, name) {
		return "", configRowDescription(value)
	}
	// The order run.SubagentOwnModel answers with, read from the same section:
	// this agent's entry, then the install-wide row of that type, then the
	// conversation's model.
	if ref := strings.TrimSpace(configStringField(row, "model")); ref != "" {
		return configAgentOverridePrefix + ref, ""
	}
	sectionRoot, _ := doc.(map[string]any)
	typeRow, _ := configLookupValue(sectionRoot, []string{configSubagentModelsKey, name})
	typeMap, _ := typeRow.(map[string]any)
	if typeRef := strings.TrimSpace(configStringField(typeMap, "model")); typeRef != "" {
		effort := configStringField(typeMap, "reasoning_effort")
		// The level reads through effortLabel (plan 327 D1): an unset
		// install-wide level is the row's own default, not a bare Default.
		return configGlobalDefaultPrefix + p.configModelLabel(typeRef) + " / " + configEffortSummary(effort), ""
	}
	return configFollowConversation, ""
}

// configModelLabel names a stored model reference the way the pickers do: the
// row's provider/model label when the install's table carries it, and the
// reference as written when it does not (a row the settings page deleted is
// skipped at run time, so the panel shows what is stored rather than nothing).
func (p *configPanel) configModelLabel(ref string) string {
	ref = strings.TrimSpace(ref)
	root := appcfg.Root{LLMModels: p.models}
	if entry, ok := appcfg.ResolveModelRef(&root, "main", ref); ok {
		return llm.FormatProviderModelDisplay(entry.Provider, entry.Model)
	}
	return ref
}

// configModelRowLabel names a stored model reference the way the model
// pickers' rows do (owner 2026-11-22: no host suffix): the bare provider/model
// display label, the endpoint carried by the picker's description column rather
// than appended to the name. Two rows of one provider and model read the same
// name and are told apart by the endpoint beside them. A reference the table
// does not carry is shown as written, so a pin whose row was deleted stays
// readable until it is re-picked.
func (p *configPanel) configModelRowLabel(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	root := appcfg.Root{LLMModels: p.models}
	entry, ok := appcfg.ResolveModelRef(&root, "main", ref)
	if !ok {
		return ref
	}
	return llm.FormatProviderModelDisplay(entry.Provider, entry.Model)
}

// configSystemSubagentRow reports whether a row is one of an agent's subagent
// entries naming a type the system dispatches. Its path is the guard's other
// half: the same field descriptor travels into every agent definition, so
// only the real path says which rows these are.
func configSystemSubagentRow(path []string, value any) bool {
	if len(path) != 3 || path[0] != configDefinitionsPath || path[2] != configSubagentsKey {
		return false
	}
	row, ok := value.(map[string]any)
	if !ok {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(configStringField(row, "name")))
	return name != "" && slices.Contains(agent.SystemSubagentNames, name)
}

// configStringField reads one string field of a stored row.
func configStringField(row map[string]any, field string) string {
	if row == nil {
		return ""
	}
	text, _ := row[field].(string)
	return text
}

// rowsNotice is what a row page carries under its tab bar: the empty state a
// collection with nothing in it needs — the footer's Add is the only thing the
// page offers then — and the last outcome. The page's own title and its count
// of rows are gone with every other heading (plan 350): the tab bar above and
// the rows below say where the user is and what is there.
func rowsNotice(keys []string, notice string) string {
	if len(keys) == 0 {
		return joinNotice(notice, "Nothing configured yet\nAdd the first entry.")
	}
	return notice
}

// editRow opens one row of a container (plan 329 D3). A row whose real path
// resolves to a template opens that template's fixed-field page directly —
// the prototype's direct-open, with no menu between the row and its fields —
// and a scalar row inside a known collection opens its text editor the same
// way. A row nothing knows keeps the generic menu: edit or open, delete —
// through the confirmation every removal asks for (plan 326 D5) — and the
// way back, which the Esc ladder carries on a selector with footer actions
// (plan 326 D2).
func (p *configPanel) editRow(doc any, path []string, key string, field configField) (any, string) {
	rowPath := configJoinPath(path, key)
	value, _ := configLookupValue(doc, rowPath)
	// A scalar row at a secret's place never opens the plain text editor
	// (plan 328 D3): its editor asks for a replacement instead, empty-handed,
	// so the stored value is never handed to a field that would show it.
	if !configIsContainer(value) && configIsSecretPath(rowPath...) {
		return p.editSecret(doc, rowPath, field, value)
	}
	if agentSubagentsPath(path) {
		if name := sharedRowName(doc, key, value); name != "" {
			container, _ := configLookupValue(doc, path)
			return p.editSharedSubagentRow(doc, path, container, name, field)
		}
	}
	container := configIsContainer(value)
	switch template := configTemplateFor(p.section, rowPath); {
	case container && configTemplateIsObject(template):
		return p.editTemplatePage(doc, rowPath, template, configTemplateRowTitle(template, key, value))
	case container && template == configTemplateMatcherList:
		// An event's matcher list is a known rows page (plan 329 D2): it
		// opens directly, the way the prototype's hook-event scene does.
		return p.editRows(doc, rowPath, field)
	case !container && template == configTemplatePath:
		return p.editPath(doc, rowPath, configTemplateFields(configTemplatePath)[0], value)
	case !container && configScalarRowTemplate(p.section, rowPath) == configTemplateString:
		return p.editText(doc, rowPath, field, value)
	}
	editLabel := configRowEditLabel
	if container {
		editLabel = configRowOpenLabel
	}
	rowDescription := configRowDescription(value)
	if configIsSecretPath(rowPath...) {
		rowDescription = configSecretDisplay(value, true)
	}
	items := []SelectItem{
		{Label: editLabel, Description: rowDescription},
		{Label: configRowDropLabel, Description: "Removes it from this level"},
	}
	if p.configActionSelector() == nil {
		items = append(items, SelectItem{Label: configRowBackLabel, Description: "Back to the row list"})
	}
	idx, ok, err := p.page(field.Label+" "+configRowLabel(key, 0, false), configFieldKeys, items, 0)
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok {
		return doc, ""
	}
	switch idx {
	case 0:
		if container {
			return p.editRows(doc, rowPath, field)
		}
		return p.editRowValue(doc, rowPath, field, value)
	case 1:
		if !p.confirmRemoveRow(configRowLabel(key, 0, false), doc, rowPath, field) {
			return doc, ""
		}
		return p.assign(doc, rowPath, nil, true, field.Label)
	default:
		return doc, ""
	}
}

// confirmRemoveRow asks before any removal (plan 326 D5): the heading names
// the row and shows what it holds — hidden whole when the field is a secret
// (plan 328 D3: no characters of one are ever on the page) — and the rows are
// Remove item and Keep item with Keep preselected, so Enter alone changes
// nothing. Esc keeps the row too. The Remove row is a danger row (prototype
// .item.danger): the choice that discards the row is the red one.
func (p *configPanel) confirmRemoveRow(label string, doc any, rowPath []string, field configField) bool {
	value, _ := configLookupValue(doc, rowPath)
	preview := configRowDescription(value)
	if field.Secret || configIsSecretPath(rowPath...) {
		preview = configSecretDisplay(value, true)
	}
	items := []SelectItem{
		{Label: configRemoveConfirmLabel, Danger: true},
		{Label: configKeepConfirmLabel},
	}
	idx, ok, err := p.page("Remove "+label+"?\n"+preview, configConfirmKeys, items, 1)
	if err != nil {
		p.printError(err)
		return false
	}
	return ok && idx == 0
}

// configTemplateFor resolves the template a stored row's real path names
// (plan 329 D2). section is the store section the page writes — a projection
// presents the agents document under its own name, and the shapes are the
// stored document's either way — and the path is the row's own place in that
// document, never a descriptor's Path: the generic editor hands the same
// field down at every nesting level, so only the real path says which row is
// being opened. The empty answer means no template: the row keeps the
// generic editor.
func configTemplateFor(section string, path []string) string {
	has := func(n int, head ...string) bool {
		if len(path) < n {
			return false
		}
		for i, key := range head {
			if key != "" && path[i] != key {
				return false
			}
		}
		return true
	}
	is := func(n int, head ...string) bool { return len(path) == n && has(n, head...) }
	switch section {
	case "agents":
		switch {
		case is(2, configDefinitionsPath):
			return configTemplateAgent
		case is(4, configDefinitionsPath, "", configSubagentsKey):
			return configTemplateSubagent
		case is(1, configPlanReviewerKey):
			return configTemplateGlobalSystemRow
		case is(2, configSubagentModelsKey):
			return configTemplateTypeDef
		case has(4, configDefinitionsPath, "", configMCPKey), has(3, "defaults", configMCPKey):
			return configMCPSubtreeTemplate(path)
		}
	case "hooks":
		switch {
		case is(1):
			return configTemplateMatcherList
		case is(2):
			return configTemplateMatcher
		case is(4, "", "", configHooksKey):
			return configTemplateHook
		}
	case "lsp":
		if is(2, configLSPServersKey) {
			return configTemplateLSPServer
		}
	case "sandbox_workspace_write":
		if is(2, configWritableRootsKey) {
			return configTemplatePath
		}
	}
	return ""
}

// configMCPSubtreeTemplate resolves an MCP server's own subtree: the server
// itself, its OAuth block, or one tool's approval override. path starts at
// the server list's own key (definitions.<id>.mcp_servers… or
// defaults.mcp_servers…), so the server's index is the segment after it.
func configMCPSubtreeTemplate(path []string) string {
	at := slices.Index(path, configMCPKey)
	if at < 0 || len(path) < at+2 {
		return ""
	}
	switch tail := path[at+2:]; {
	case len(tail) == 0:
		return configTemplateMCP
	case len(tail) == 1 && tail[0] == configMCPOAuthKey:
		return configTemplateOAuth
	case len(tail) == 2 && tail[0] == configMCPToolsKey:
		return configTemplateToolApproval
	default:
		return ""
	}
}

// configScalarRowTemplate resolves the pseudo-template a scalar row edits
// when its place in a known template is a collection of strings — an
// argument, a scope, an environment value: the row opens the text editor
// directly, under the parent collection's own descriptor, instead of asking
// what type the value is. Called only for a row that resolved to no object
// template and holds a scalar; the walk finds the nearest ancestor a
// template names and crosses exactly one collection field into the element.
func configScalarRowTemplate(section string, path []string) string {
	for cut := len(path) - 1; cut > 0; cut-- {
		template := configTemplateFor(section, path[:cut])
		if !configTemplateIsObject(template) {
			continue
		}
		rest := path[cut:]
		if len(rest) != 2 {
			continue
		}
		for _, field := range configTemplates[template] {
			if field.Path == rest[0] && field.Kind == configFieldRows {
				return configTemplateString
			}
		}
	}
	return ""
}

// configTemplateRowTitle is the title a templated row's page opens under: the
// name the row carries when it has one — an agent's display name, a
// subagent's or a server's name, a matcher's pattern — else the row list's
// own label for it.
func configTemplateRowTitle(template, key string, value any) string {
	if settings, ok := value.(map[string]any); ok {
		for _, candidate := range []string{"display_name", "name", "matcher"} {
			if text := strings.TrimSpace(configStringField(settings, candidate)); text != "" {
				return text
			}
		}
	}
	if index, err := strconv.Atoi(key); err == nil && index >= 0 {
		return configRowLabel(key, index, true)
	}
	return key
}

// configTemplateRow is one row of a template page: the template's own field,
// or a key the template does not name, or the page's one way up. field's Path
// stays the template's own segment; the page joins it onto the row's path
// when the field is opened, so no dotted path is ever built from stored keys.
type configTemplateRow struct {
	field   configField
	unknown string // a key the template does not name, when set
	up      bool
}

// editTemplatePage renders one stored row as its template's fixed-field page
// (plan 329 D3): the fields in the template's order under their groups, the
// value each holds in the row's value column, and the keys the template does
// not name under an additional-fields group — opened through the generic
// editor and preserved on save, because the write is the section's whole row
// (no data loss). Enter opens a field's editor directly; nothing on this
// page deletes the row (that is the footer's action, plan 326). A subagent
// entry naming a system type renders every row read-only with the System
// status and refuses to open any of them (prototype html:128).
func (p *configPanel) editTemplatePage(doc any, rowPath []string, template, title string, initialNotice ...string) (any, string) {
	notice := ""
	if len(initialNotice) > 0 {
		notice = initialNotice[0]
	}
	tailRows := p.configActionSelector() == nil
	for {
		value, _ := configLookupValue(doc, rowPath)
		settings, _ := value.(map[string]any)
		if settings == nil {
			settings = map[string]any{}
		}
		items, rows := p.templateItems(template, configRowKey(rowPath), settings, tailRows)
		idx, ok, err := p.page(p.templateNotice(template, settings, notice), configFieldKeys, items, -1)
		if err != nil {
			p.printError(err)
			return doc, ""
		}
		notice = ""
		if !ok || idx < 0 || idx >= len(rows) {
			return doc, ""
		}
		row := rows[idx]
		if row.up {
			return doc, ""
		}
		if configTemplateRowIsSystem(template, settings) {
			// The page stays open under the verdict: it is a refusal, not a way
			// out of the page (plan 350).
			notice = configSystemSubagentNotice
			continue
		}
		// The two fields a create decides (the prototype's keyName and
		// createEditable): opening either on a saved definition answers with
		// the created-fields sentence instead of an editor, and the page stays
		// where it was.
		if template == configTemplateAgent && (row.field.KeyName || row.field.CreateEditable) {
			notice = configCreatedFieldsNotice
			continue
		}
		if row.unknown != "" {
			doc, notice = p.openUnknownTemplateKey(doc, rowPath, row.unknown)
			continue
		}
		doc, notice = p.openTemplateField(doc, rowPath, row.field)
	}
}

// templateNotice is what a template page carries under its tab bar: the system
// verdict when the whole page is read-only, and the last outcome. The row's own
// title is gone with every other heading (plan 350; the rows list the user came
// from named it, and the tab bar says where they are).
func (p *configPanel) templateNotice(template string, settings map[string]any, notice string) string {
	if configTemplateRowIsSystem(template, settings) {
		return joinNotice(notice, configSystemSubagentNotice)
	}
	return notice
}

// configTemplateRowIsSystem reports whether the whole page renders read-only:
// a subagent entry naming a type the system dispatches (prototype html:128).
func configTemplateRowIsSystem(template string, settings map[string]any) bool {
	if template != configTemplateSubagent {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(configStringField(settings, "name")))
	return name != "" && slices.Contains(agent.SystemSubagentNames, name)
}

// configTemplateNameIsBuiltinOrSystem reports whether a subagent entry's name
// is one the loader keeps to a model, a level, a prompt and a switch: the
// public built-in types and the system-dispatched ones.
func configTemplateNameIsBuiltinOrSystem(name string) bool {
	return name != "" && (slices.Contains(agent.PublicSubagentNames, name) || slices.Contains(agent.SystemSubagentNames, name))
}

// configAgentRolePrimary reports whether a definition's page renders the
// primary role's fields: primary true, or a definition that never chose a role
// (the same reading the loader's IsEffectivePrimaryAgent applies to everything
// but the built-in keys). An explicit false plays the subagent role.
func configAgentRolePrimary(settings map[string]any) bool {
	primary, isBool := settings["primary"].(bool)
	return !isBool || primary
}

// configHookActionType is the action a hook entry performs, in the loader's
// own ids; an entry that names none is a command hook, the template's default
// (prototype html:126 reads value?.type || 'command').
func configHookActionType(settings map[string]any) string {
	if kind := strings.TrimSpace(configStringField(settings, "type")); kind != "" {
		return kind
	}
	return appcfg.HookTypeCommand
}

// templateItems is a template page's rows: the template's fields in their own
// order under their groups, the keys the template does not name under the
// additional-fields group, then the way up when the page keeps its tail rows
// (plan 326 D2: the Esc ladder carries it on a selector with footer
// actions). customOnly hides the fields a built-in or system entry cannot
// carry; showFor hides the fields another action type owns; a system entry's
// every row carries the System status and the verdict in place of its hint.
func (p *configPanel) templateItems(template, key string, settings map[string]any, tailRows bool) ([]SelectItem, []configTemplateRow) {
	fields := configTemplates[template]
	name := strings.ToLower(strings.TrimSpace(configStringField(settings, "name")))
	system := configTemplateRowIsSystem(template, settings)
	hookType := configHookActionType(settings)
	rolePrimary := configAgentRolePrimary(settings)
	known := make(map[string]bool, len(fields))
	items := make([]SelectItem, 0, len(fields)+4)
	rows := make([]configTemplateRow, 0, len(fields)+4)
	for _, field := range fields {
		known[field.Path] = true
		if field.CustomOnly && configTemplateNameIsBuiltinOrSystem(name) {
			continue
		}
		// A built-in's pin retires the prompt rewrite and the switch (P5):
		// hidden on its entry, dropped from the stored row on load.
		if field.HideOnBuiltin && slices.Contains(agent.PublicSubagentNames, name) {
			continue
		}
		// The unified definition renders one role's fields (P3): the flags
		// are mutually exclusive, so a page never shows both roles' Model
		// rows. A role nobody chose reads as the primary role's.
		if template == configTemplateAgent && field.PrimaryOnly && !rolePrimary {
			continue
		}
		if template == configTemplateAgent && field.SubagentOnly && rolePrimary {
			continue
		}
		if len(field.ShowFor) > 0 && !slices.Contains(field.ShowFor, hookType) {
			continue
		}
		value, exists := configLookupValue(settings, []string{field.Path})
		if field.KeyName {
			// The name of a definition is its own key in the map, not a value
			// inside it (plan 350): a row still unnamed reads as nothing.
			value, exists = key, key != ""
		}
		// A template row is a label, its value and — on a read-only page —
		// the system verdict: the field's help belongs to its editor, never to
		// the row (plan 350, the prototype's fields view setting help, not
		// desc), and an unset field carries no status at all.
		status := ""
		if system {
			status = configSystemStatus
		}
		// The two fields a create decides are read-only once the definition
		// exists: the page draws them as the prototype's locked rows.
		locked := !configTemplateRowIsSystem(template, settings) && (field.KeyName || field.CreateEditable) && key != ""
		items = append(items, SelectItem{
			Label:    field.Label,
			Value:    p.configTemplateValueText(field, value, exists),
			Status:   status,
			Category: field.Group,
			Locked:   locked,
		})
		rows = append(rows, configTemplateRow{field: field})
	}
	for _, key := range configContainerKeys(settings) {
		if known[key] {
			continue
		}
		value := settings[key]
		items = append(items, SelectItem{
			Label:    configTitleCase(key),
			Value:    configRowDescription(value),
			Category: configAdditionalFieldsGroup,
		})
		rows = append(rows, configTemplateRow{unknown: key})
	}
	if tailRows {
		items = append(items, SelectItem{Label: configUpRowLabel, Description: "Back up one level"})
		rows = append(rows, configTemplateRow{up: true})
	}
	return items, rows
}

// configTemplateValueText is a template field's value column. A field that
// holds a model row reference names it the way the pickers do, and its empty
// value reads as the sentence the template gives it (the model picker's empty
// option, plan 324); everything else keeps the panel's one value wording.
func (p *configPanel) configTemplateValueText(field configField, value any, exists bool) string {
	if field.ModelEmpty != "" {
		// A model row nobody pinned reads as nothing (plan 350, the
		// prototype's display()): the empty option's own label belongs to the
		// picker it opens, not to the row it was never set on.
		ref, _ := value.(string)
		if strings.TrimSpace(ref) == "" {
			return ""
		}
		return p.configModelRowLabel(ref)
	}
	return configFieldValueText(field, value, exists)
}

// openTemplateField opens one template field's editor directly (plan 329
// D3): the widget is the field's own kind, at the field's real path inside
// the row — a dispatch that mirrors editField without rooting a dotted path
// through stored keys.
func (p *configPanel) openTemplateField(doc any, rowPath []string, field configField) (any, string) {
	path := configJoinPath(rowPath, field.Path)
	cur, _ := configLookupValue(doc, path)
	// The one field both roles carry says something different about each: the
	// page that opened it shows that sentence under its editor, where the
	// prototype's editor-help sits (plan 350) — never on the row it came from.
	row, _ := configLookupValue(doc, rowPath)
	settings, _ := row.(map[string]any)
	field.Hint = configRoleHelp(field, settings)
	if field.Kind == configFieldRows || configIsContainer(cur) {
		return p.editRows(doc, path, field)
	}
	// A scalar field at a secret's place is asked for the private way (plan
	// 328 D3): the plain editor would open on the stored value.
	if field.Secret || configIsSecretPath(path...) {
		return p.editSecret(doc, path, field, cur)
	}
	switch field.Kind {
	case configFieldBool3:
		return p.editBool3(doc, path, field, cur)
	case configFieldEnum:
		return p.editEnum(doc, path, field, cur)
	case configFieldNumber:
		return p.editNumber(doc, path, field, cur)
	case configFieldEffort:
		return p.editEffort(doc, path, field, p.fieldPrompt(field), cur)
	case configFieldModel:
		return p.editModel(doc, path, field, cur)
	default:
		return p.editText(doc, path, field, cur)
	}
}

// openUnknownTemplateKey opens a key the row's template does not name
// through the generic editor — a container as rows, a scalar with its type
// asked — so the key stays reachable and its value travels back on save.
// The descriptor carries the key as its path: the generic guards that read a
// field's own path (the hooks tree's add, primary creation) must not mistake
// this page for the section's own fields.
func (p *configPanel) openUnknownTemplateKey(doc any, rowPath []string, key string) (any, string) {
	path := configJoinPath(rowPath, key)
	value, _ := configLookupValue(doc, path)
	field := configField{Label: configTitleCase(key), Path: key}
	if configIsContainer(value) {
		field.Kind = configFieldRows
		return p.editRows(doc, path, field)
	}
	return p.editRowValue(doc, path, field, value)
}

// editSharedSubagentRow is the whole of what an agent's page may do with a
// shared subagent (plan 314): one switch, and the definition's own facts shown
// read-only. The definition is the install-wide registry's, so it is edited on
// the Subagents page and every other row here says where it lives rather
// than opening an editor this page does not have. The switch is one edit like
// any other (plan 321): throwing it writes the reference entry, throwing it back
// removes it, and Esc between the two writes nothing.
func (p *configPanel) editSharedSubagentRow(doc any, path []string, container any, name string, field configField) (any, string) {
	enabled := sharedReferenceEnabled(container, name)
	items := []SelectItem{
		{Label: "Enabled", Description: configSharedSwitchText(enabled)},
		{Label: "Description", Description: configSharedRowSummary(doc, name)},
		{Label: "Model", Description: p.configSharedModelSummary(doc, name)},
		{Label: "Tools", Description: configSharedToolsText(doc, name)},
	}
	if p.configActionSelector() == nil {
		items = append(items, SelectItem{Label: configRowBackLabel, Description: "Back to the row list"})
	}
	idx, ok, err := p.page(configSharedSubagentNotice, configFieldKeys, items, 0)
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok || idx < 0 || idx >= len(items) {
		return doc, ""
	}
	if idx == 0 {
		updated, note := p.setSharedReference(doc, path, container, name, !enabled, field.Label)
		if note != "" {
			return updated, note
		}
		return updated, configSharedSwitchedNotice(name, !enabled)
	}
	return doc, configSharedSubagentNotice
}

// setSharedReference switches one shared subagent on or off in this agent's
// list: on stores the reference entry — the name and enabled: true, because for
// a shared subagent an absent switch means off — and off removes it.
func (p *configPanel) setSharedReference(doc any, path []string, container any, name string, enabled bool, label string) (any, string) {
	rows, _ := container.([]any)
	next := make([]any, 0, len(rows)+1)
	for _, row := range rows {
		entry, _ := row.(map[string]any)
		if strings.ToLower(strings.TrimSpace(configStringField(entry, "name"))) == name {
			continue
		}
		next = append(next, row)
	}
	if enabled {
		next = append(next, map[string]any{"name": name, "enabled": true})
	}
	return p.assign(doc, path, next, false, label)
}

// sharedReferenceEnabled reports whether this agent's list switches one shared
// subagent on: only the entry's explicit true does.
func sharedReferenceEnabled(container any, name string) bool {
	rows, _ := container.([]any)
	for _, row := range rows {
		entry, _ := row.(map[string]any)
		if strings.ToLower(strings.TrimSpace(configStringField(entry, "name"))) != name {
			continue
		}
		enabled, _ := entry["enabled"].(bool)
		return enabled
	}
	return false
}

// configSharedSwitchText is the switch row's line: what the state means for
// this agent, not merely which way it points.
func configSharedSwitchText(enabled bool) string {
	if enabled {
		return "On: this agent may dispatch it"
	}
	return "Off: this agent does not dispatch it"
}

// configSharedSwitchedNotice says what the edit did, in the terms the page
// uses: the switch is written as it is thrown (plan 321).
func configSharedSwitchedNotice(name string, enabled bool) string {
	if enabled {
		return name + " enabled for this agent"
	}
	return name + " disabled for this agent"
}

// configSharedRowSummary is a shared subagent's row on an agent's page: the
// registry's own description, marked as shared because the definition is not
// this agent's to edit (plan 314).
func configSharedRowSummary(doc any, name string) string {
	if entry, ok := configSharedSubagent(doc, name); ok {
		if text := strings.TrimSpace(configStringField(entry, "description")); text != "" {
			return text + " (shared)"
		}
	}
	return "(shared)"
}

// configSharedModelSummary is the model a shared subagent runs on, named the way
// the model pickers name it, with the level beside it.
func (p *configPanel) configSharedModelSummary(doc any, name string) string {
	entry, ok := configSharedSubagent(doc, name)
	if !ok {
		return configFollowConversation
	}
	ref := strings.TrimSpace(configStringField(entry, "model"))
	if ref == "" {
		return configFollowConversation
	}
	return p.configModelLabel(ref) + " / " + configEffortSummary(configStringField(entry, "reasoning_effort"))
}

// configSharedToolsText is a shared subagent's tool tier as words.
func configSharedToolsText(doc any, name string) string {
	entry, _ := configSharedSubagent(doc, name)
	switch tier := strings.TrimSpace(configStringField(entry, "tools")); tier {
	case appcfg.SubagentToolsEdit:
		return "edit: it may change the workspace"
	case appcfg.SubagentToolsPick:
		allowed, _ := entry["allowed_tools"].([]any)
		return "pick: " + strconv.Itoa(len(allowed)) + " tools"
	default:
		return appcfg.SubagentToolsReadOnly
	}
}

// editRowValue edits a row that holds a scalar. The value's type is the
// user's choice rather than a guess: an env map needs text, a workspace_roots
// map needs a boolean, and nothing in the document says which one this is. A
// stored thinking level is the one scalar whose type is known — and whose
// values are the model row's — so it opens its picker instead (plan 319), and
// so does a stored model reference whose place a model field names (plan 324).
func (p *configPanel) editRowValue(doc any, rowPath []string, field configField, cur any) (any, string) {
	if configEffortPath(rowPath) {
		return p.editEffort(doc, rowPath, field, configEffortHeading, cur)
	}
	if model, ok := configModelFieldAt(p.section, rowPath); ok {
		return p.editModel(doc, rowPath, model, cur)
	}
	// A secret's replacement is asked for the same way at the generic row
	// (plan 328 D3): the type menu would open a plain text editor on the
	// stored value.
	if configIsSecretPath(rowPath...) {
		return p.editSecret(doc, rowPath, field, cur)
	}
	value, _, ok, err := p.chooseNewValue(field.Label, cur)
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok {
		return doc, ""
	}
	return p.assign(doc, rowPath, value, false, field.Label)
}

// configModelFieldAt resolves the model picker's descriptor for a stored
// model reference the generic row editor reaches (plan 324 D4): the field its
// place names — a field of the template the reference's row resolves to, else
// the section schema's — so the picker reads the same as it does on the page
// that names the field. The resolution is the scope: a plain text field that
// happens to be named model (a hook action's optional override) names no
// field of the model kind and keeps its text editor.
func configModelFieldAt(section string, path []string) (configField, bool) {
	if len(path) > 1 {
		for _, field := range configTemplates[configTemplateFor(section, path[:len(path)-1])] {
			if field.Path == path[len(path)-1] && field.Kind == configFieldModel {
				return field, true
			}
		}
	}
	if schema, ok := configSectionSchemaFor(section); ok {
		dotted := strings.Join(path, ".")
		for _, field := range schema.Fields {
			if field.Path == dotted && field.Kind == configFieldModel {
				return field, true
			}
		}
	}
	return configField{}, false
}

// addRow appends one row and, when the new row is an object, opens it so the
// user shapes it straight away. The definitions map at the top level is the one
// place a row is a primary agent: it asks for an id and a display name and
// seeds the prototype's agent shape, instead of asking what type the value is
// (plan 313, scene create-agent). The hooks section is the one place a row must
// be complete before it exists (plan 321), so it takes its own path.
func (p *configPanel) addRow(doc any, path []string, field configField) (any, string) {
	container, _ := configLookupValue(doc, path)
	if hookRowsPage(field) {
		return p.addHookRow(doc, path, field, container)
	}
	list := configContainerIsList(field, container)
	if !list && configPrimaryCreationPath(field, path) {
		return p.addAgentDefinition(doc, path, field, container)
	}
	key := ""
	if !list {
		// The add view's own copy (plan 328 D4, prototype html:160): the
		// heading names the page it adds to, and the key is a key.
		name, ok, err := p.sel.Input("Add to "+field.Label+"\nKey", "")
		if err != nil {
			p.printError(err)
			return doc, ""
		}
		if !ok {
			return doc, ""
		}
		key = strings.TrimSpace(name)
		if key == "" {
			return doc, "A key is required."
		}
		if rows, isMap := container.(map[string]any); isMap {
			if _, dup := rows[key]; dup {
				return doc, "That key already exists. Choose a different key."
			}
		}
	}
	// A thinking level is not typed even when its key is new: the picker reads
	// the model row beside it, so the value comes from the same list an existing
	// level is edited through (plan 319).
	childPath := configJoinPath(path, key)
	if !list && configEffortPath(childPath) {
		return p.editEffort(doc, childPath, field, configEffortHeading, nil)
	}
	// A row whose place a known template names is not typed either (plan 329
	// D3): a path row or a string row opens its text editor directly, the
	// way opening the same row does. A list's new row sits at the index the
	// append will land at, which is the place the template resolves by.
	if list {
		rows, _ := container.([]any)
		childPath = configJoinPath(path, strconv.Itoa(len(rows)))
		key = strconv.Itoa(len(rows))
	}
	if template := configTemplateFor(p.section, childPath); template == configTemplatePath || (template == "" && configScalarRowTemplate(p.section, childPath) == configTemplateString) {
		// A new row at a secret's place is asked for the private way (plan
		// 328 D3): the value typed into a plain field would read back off
		// the screen. Map rows only — a list's append is not a path yet.
		if !list && configIsSecretPath(childPath...) {
			return p.editSecret(doc, childPath, field, nil)
		}
		text, ok, unset, err := p.editScalar(field, "")
		if err != nil {
			p.printError(err)
			return doc, ""
		}
		if !ok {
			return doc, ""
		}
		if unset {
			return doc, "the value may not be empty; nothing was written"
		}
		if template == configTemplatePath && !configAbsolutePath(text) {
			return doc, configPathRefusal
		}
		if list {
			rows, _ := container.([]any)
			rows = append(append(make([]any, 0, len(rows)+1), rows...), text)
			return p.assign(doc, path, rows, false, field.Label)
		}
		return p.assign(doc, childPath, text, false, field.Label)
	}
	value, open, ok, err := p.chooseNewValue(field.Label, nil)
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok {
		return doc, ""
	}
	var next any = value
	if list {
		rows, _ := container.([]any)
		rows = append(append(make([]any, 0, len(rows)+1), rows...), value)
		next = rows
		childPath = configJoinPath(path, strconv.Itoa(len(rows)-1))
	} else {
		rows, isMap := container.(map[string]any)
		if !isMap || rows == nil {
			rows = make(map[string]any, 1)
		}
		rows[key] = value
		next = rows
	}
	updated, note := p.assign(doc, path, next, false, field.Label)
	if note != "" || !open {
		return updated, note
	}
	// The new row opens the page its place names (plan 329 D3): a templated
	// row opens its fixed-field page, a generic one its rows as before.
	if template := configTemplateFor(p.section, childPath); configTemplateIsObject(template) {
		return p.editTemplatePage(updated, childPath, template, configTemplateRowTitle(template, key, value))
	}
	opened, note := p.editRows(updated, childPath, field)
	return opened, note
}

// configPrimaryCreationPath reports whether an add lands a new definition in
// the agents section's definitions map: the unified create form's one gate.
// The path check is not redundant with the field's — editRows hands the same
// descriptor down to every nested row page, so inside one agent's definition
// the path is longer and the guard must not fire there (plan 313).
func configPrimaryCreationPath(field configField, path []string) bool {
	return field.Path == configDefinitionsPath && len(path) == 1 && path[0] == configDefinitionsPath
}

// The unified create's own copy (P3; every sentence is the prototype's or the
// panel's existing wording, never an invented one).
const (
	// configKeyNameHint is the name field's own help (the prototype's keyName
	// field, word for word).
	configKeyNameHint = "Lowercase letters, numbers and hyphens. This is the configuration key."
	// configReservedIDRefusal is the six-name refusal (the prototype's
	// renameDefinition, word for word); the loader refuses them too, but the
	// form owes the sentence before the write.
	configReservedIDRefusal = "Those names are the 6 built-in subagents. Choose a different ID."
	// configPrimaryIDRefusal is the primary id's rule, the loader's own
	// (plan 328 D4): it takes underscores as well as the prototype's letters,
	// numbers and hyphens, and the help says so — the loader is the authority.
	configPrimaryIDRefusal = "Use a lowercase agent ID with letters, numbers, hyphens or underscores, starting with a letter."
	// configCreateHeldNotice answers a value the create form holds while the
	// name has not landed: the panel's Enter persists, and this says where the
	// value went.
	configCreateHeldNotice = "Held with the new agent; it saves when the name is confirmed."
	// configCreateNameFirstNotice answers a field whose editor needs a saved
	// section to read and write: the model rows and the collections.
	configCreateNameFirstNotice = "Confirm the name first; the rest saves on the page that follows."
	// configCreateRoleNotice answers a role flip that cleared the other
	// role's held values (owner ruling: 切角色时清掉对方的字段).
	configCreateRoleNotice = "Role switched. The other role's held values were cleared."
)

// The two roles' field keys, the sets a role flip clears (the prototype's
// stageValue clears the primary-only keys on primary:false; the same rule
// applies symmetrically).
var (
	configPrimaryOnlyKeys  = []string{"mcp_servers", "enable_subagent", "subagents"}
	configSubagentOnlyKeys = []string{"model", "reasoning_effort", "tools", "allowed_tools", "continuable"}
)

// addAgentDefinition is the unified create (P3, the prototype's agent create
// scene): one single-page form — the same fields a saved definition's page
// renders — held as a draft until the name lands. The name and the primary
// switch are the two fields the create decides; every other field the role's
// page shows is held with the draft and saves with the name, after which the
// panel's own Enter-persists-per-field rule takes over on the saved page.
// Nothing writes until a valid name confirms, so backing out of an unnamed
// draft discards silently, and a refused name keeps the form and every held
// value.
//
// The draft starts as a primary agent (the prototype's startAdd seed) and the
// primary switch flips the role in place, clearing the other role's held
// values. The refusals are the prototype's and the loader's: an empty key, a
// key the role's pattern refuses (the primary's here, the subagent's through
// the loader's own sentence on the write), one of the six reserved subagent
// names, and a duplicate.
func (p *configPanel) addAgentDefinition(doc any, path []string, field configField, container any) (any, string) {
	draft := map[string]any{"primary": true}
	tailRows := p.configActionSelector() == nil
	// The form opens on the name (plan 350, the prototype's startAdd →
	// focusEditor): the `a` that got here already means "a new definition", so
	// the first thing typed is its name, and it lands the definition at once.
	// Esc leaves the form with nothing written and every other field empty.
	updated, key, landed, notice := p.commitAgentName(doc, path, field, container, draft)
	if landed {
		return p.editTemplatePage(updated, configJoinPath(path, key), configTemplateAgent, key)
	}
	doc = updated
	for {
		items, rows := p.templateItems(configTemplateAgent, "", draft, tailRows)
		idx, ok, err := p.page(notice, configFieldKeys, items, -1)
		if err != nil {
			p.printError(err)
			return doc, ""
		}
		notice = ""
		if !ok || idx < 0 || idx >= len(rows) {
			// Back out of an unnamed draft: nothing was written, so nothing
			// is discarded but the held values.
			return doc, ""
		}
		row := rows[idx]
		switch {
		case row.up:
			return doc, ""
		case row.field.KeyName:
			updated, key, landed, held := p.commitAgentName(doc, path, field, container, draft)
			if !landed {
				notice = held
				continue
			}
			// The name landed: the definition exists, and the page it opens
			// is the saved definition's own, where every further confirm
			// persists per field.
			return p.editTemplatePage(updated, configJoinPath(path, key), configTemplateAgent, key)
		case row.field.CreateEditable:
			if !p.editAgentDraftPrimary(draft) {
				return doc, ""
			}
			notice = configCreateRoleNotice
		default:
			held, fieldNotice := p.editAgentDraftField(draft, row.field)
			if !held {
				return doc, ""
			}
			notice = fieldNotice
		}
	}
}

// commitAgentName validates the name the create form holds and, on the first
// good one, writes the definition whole — the draft under its real key, the
// one store write that materialises it. A refusal returns landed=false with
// the notice the form re-renders under, every held value intact. landed=true
// hands back the updated document and the key that landed.
func (p *configPanel) commitAgentName(doc any, path []string, field configField, container any, draft map[string]any) (any, string, bool, string) {
	name, ok, _, err := p.editScalar(field, "")
	if err != nil {
		p.printError(err)
		return doc, "", false, ""
	}
	if !ok {
		return doc, "", false, ""
	}
	key := strings.TrimSpace(name)
	if key == "" {
		return doc, "", false, "A key is required."
	}
	if configAgentRolePrimary(draft) && !appcfg.ValidPrimaryAgentID(key) {
		return doc, "", false, configPrimaryIDRefusal
	}
	if appcfg.IsReservedDefinitionID(key) {
		return doc, "", false, configReservedIDRefusal
	}
	if rows, isMap := container.(map[string]any); isMap {
		if _, dup := rows[key]; dup {
			return doc, "", false, "That key already exists. Choose a different key."
		}
	}
	rows, isMap := container.(map[string]any)
	if !isMap || rows == nil {
		rows = make(map[string]any, 1)
	}
	rows[key] = draft
	updated, note := p.assign(doc, path, rows, false, field.Label)
	if note != "" {
		// The loader refused the shape (a subagent id its pattern refuses, a
		// body it will not take): the form stays with every held value.
		return doc, "", false, note
	}
	return updated, key, true, ""
}

// editAgentDraftPrimary flips the create draft's role and clears the other
// role's held values. It reports false to leave the form, true to re-render it.
func (p *configPanel) editAgentDraftPrimary(draft map[string]any) bool {
	items := []SelectItem{{Label: configBoolOnLabel}, {Label: configBoolOffLabel}}
	if configAgentRolePrimary(draft) {
		items[0].Status = configChoiceCheckStatus
	} else {
		items[1].Status = configChoiceCheckStatus
	}
	idx, ok, err := p.page(configCreatedFieldsNotice, configChoiceKeys, items, 0)
	if err != nil {
		p.printError(err)
		return false
	}
	if !ok || idx < 0 || idx > 1 {
		return false
	}
	next := idx == 0
	if next == configAgentRolePrimary(draft) {
		return true
	}
	draft["primary"] = next
	for _, key := range configPrimaryOnlyKeys {
		if !next {
			delete(draft, key)
		}
	}
	for _, key := range configSubagentOnlyKeys {
		if next {
			delete(draft, key)
		}
	}
	return true
}

// editAgentDraftField holds one non-deciding field's value with the draft.
// Simple values type or pick straight into the draft; the fields whose
// editors need a stored row or a saved section to read and write — the model
// rows and the collections — ask for the name first instead. It reports
// false to leave the form, true to re-render it under the notice it names.
func (p *configPanel) editAgentDraftField(draft map[string]any, field configField) (bool, string) {
	switch field.Kind {
	case configFieldModel, configFieldEffort, configFieldRows:
		return true, configCreateNameFirstNotice
	case configFieldEnum:
		items := make([]SelectItem, 0, len(field.Options))
		current := strings.TrimSpace(configStringField(draft, field.Path))
		if current == "" {
			current = field.DefaultValue
		}
		for _, option := range field.Options {
			item := SelectItem{Label: option}
			if label, ok := field.OptionLabels[option]; ok {
				item.Label = label
			}
			if option == current {
				item.Status = configChoiceCheckStatus
			}
			items = append(items, item)
		}
		idx, ok, err := p.page(field.Label, configChoiceKeys, items, 0)
		if err != nil {
			p.printError(err)
			return false, ""
		}
		if !ok || idx < 0 || idx >= len(field.Options) {
			return false, ""
		}
		draft[field.Path] = field.Options[idx]
		return true, configCreateHeldNotice
	case configFieldBool3:
		items := []SelectItem{{Label: configBoolOnLabel}, {Label: configBoolOffLabel}}
		current := true
		if value, isBool := draft[field.Path].(bool); isBool {
			current = value
		} else if field.DefaultValue == "false" {
			current = false
		}
		defaultIdx := 0
		if !current {
			defaultIdx = 1
		}
		items[defaultIdx].Status = configChoiceCheckStatus
		idx, ok, err := p.page(field.Label, configChoiceKeys, items, defaultIdx)
		if err != nil {
			p.printError(err)
			return false, ""
		}
		if !ok || idx < 0 || idx > 1 {
			return false, ""
		}
		draft[field.Path] = idx == 0
		return true, configCreateHeldNotice
	default:
		text, ok, err := p.sel.Input("New agent\n"+field.Label, configStringField(draft, field.Path))
		if err != nil {
			p.printError(err)
			return false, ""
		}
		if !ok {
			return false, ""
		}
		if strings.TrimSpace(text) == "" {
			delete(draft, field.Path)
			return true, configCreateHeldNotice
		}
		draft[field.Path] = text
		return true, configCreateHeldNotice
	}
}

// hookRowsPage reports whether a row page is the hooks section's own tree. Its
// single descriptor is the only rows field the schema gives no path of its own,
// and editRows hands that same descriptor down at every level, so the path is
// what says which level a row is being added to (plan 321).
func hookRowsPage(field configField) bool {
	return field.Kind == configFieldRows && len(configFieldPath(field)) == 0
}

// addHookRow creates a hooks section row the only way the loader accepts it. A
// hook is validated whole — an action needs the field its type names, a matcher
// needs at least one action, an event needs a matcher list — so every level asks
// everything its row needs and writes the complete row in one candidate. The
// store refuses each partial shape ({} , {type}, a command turned into an HTTP
// hook without a url), which is why there is no draft for hooks and no second
// write to finish one (plan 321, owner 2026-11-17).
func (p *configPanel) addHookRow(doc any, path []string, field configField, container any) (any, string) {
	switch {
	case len(path) == 0:
		return p.addHookEvent(doc, path, field, container)
	case len(path) == 1:
		return p.addHookMatcher(doc, path, field, container)
	case len(path) == 3 && path[2] == configHooksKey:
		return p.addHookAction(doc, path, field, container)
	}
	return doc, ""
}

// addHookEvent adds one event's matcher list. The loader accepts an event with
// no matchers — that is a hook list nobody has configured yet — so the row is
// complete as soon as it exists.
func (p *configPanel) addHookEvent(doc any, path []string, field configField, container any) (any, string) {
	events := appcfg.HookEventNames()
	items := make([]SelectItem, 0, len(events))
	for _, event := range events {
		items = append(items, SelectItem{Label: event})
	}
	idx, ok, err := p.page(configHookEventHeading, configChoiceKeys, items, 0)
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok || idx < 0 || idx >= len(events) {
		return doc, ""
	}
	rows, isMap := container.(map[string]any)
	if !isMap || rows == nil {
		rows = make(map[string]any, 1)
	}
	event := events[idx]
	if _, dup := rows[event]; dup {
		return doc, event + " is already configured"
	}
	rows[event] = []any{}
	return p.assign(doc, path, rows, false, field.Label)
}

// addHookMatcher adds one matcher together with its first action: a matcher that
// carries none is one the loader refuses, so the two are asked as one row and
// written once.
func (p *configPanel) addHookMatcher(doc any, path []string, field configField, container any) (any, string) {
	pattern, ok, err := p.sel.Input(configHookMatcherHeading, configHookAllToolsMatcher)
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok {
		return doc, ""
	}
	action, notice, ok := p.askHookAction()
	if !ok {
		return doc, notice
	}
	rows, _ := container.([]any)
	rows = append(append(make([]any, 0, len(rows)+1), rows...), map[string]any{
		"matcher": strings.TrimSpace(pattern),
		"hooks":   []any{action},
	})
	return p.assign(doc, path, rows, false, field.Label)
}

// addHookAction appends one action to a matcher that already exists.
func (p *configPanel) addHookAction(doc any, path []string, field configField, container any) (any, string) {
	action, notice, ok := p.askHookAction()
	if !ok {
		return doc, notice
	}
	rows, _ := container.([]any)
	rows = append(append(make([]any, 0, len(rows)+1), rows...), action)
	return p.assign(doc, path, rows, false, field.Label)
}

// askHookAction asks one action's fields — the type it performs, then the field
// that type requires, which is the list the prototype's hook template shows —
// and answers the complete row. The loader's own validator judges the answers
// before anything is written, so an action the store would refuse never reaches
// a write and the page keeps the validator's sentence.
func (p *configPanel) askHookAction() (map[string]any, string, bool) {
	types := appcfg.PersistentHookTypes()
	items := make([]SelectItem, 0, len(types))
	defaultIdx := 0
	for _, kind := range types {
		items = append(items, SelectItem{Label: configHookTypeLabel(kind)})
		if kind == appcfg.HookTypeCommand {
			defaultIdx = len(items) - 1
		}
	}
	idx, ok, err := p.page(configHookTypeHeading, configChoiceKeys, items, defaultIdx)
	if err != nil {
		p.printError(err)
		return nil, "", false
	}
	if !ok || idx < 0 || idx >= len(types) {
		return nil, "", false
	}
	kind := types[idx]
	required, heading := configHookRequiredField(kind)
	text, ok, err := p.sel.Input(heading, "")
	if err != nil {
		p.printError(err)
		return nil, "", false
	}
	if !ok {
		return nil, "", false
	}
	value := strings.TrimSpace(text)
	if value == "" {
		return nil, configHookTypeLabel(kind) + " hooks need a " + required + "; nothing was written", false
	}
	action := map[string]any{"type": kind, required: value}
	if err := appcfg.ValidateHookCommand(configHookCommand(action)); err != nil {
		return nil, err.Error(), false
	}
	return action, "", true
}

// configHookRequiredField is the one field an action of this type cannot do
// without, and the heading its page asks it under: the loader's own rule, read
// back as the panel's question.
func configHookRequiredField(kind string) (string, string) {
	switch kind {
	case appcfg.HookTypeCommand:
		return "command", "Command"
	case appcfg.HookTypeHTTP:
		return "url", "Request URL"
	default:
		return "prompt", "Prompt"
	}
}

// configHookCommand is the typed row the loader judges, built from the same
// answers the stored map is: one validator, so the panel and the store cannot
// disagree about what a complete hook is.
func configHookCommand(action map[string]any) appcfg.HookCommand {
	kind, _ := action["type"].(string)
	hook := appcfg.HookCommand{Type: kind}
	switch kind {
	case appcfg.HookTypeCommand:
		hook.Command, _ = action["command"].(string)
	case appcfg.HookTypeHTTP:
		hook.URL, _ = action["url"].(string)
	default:
		hook.Prompt, _ = action["prompt"].(string)
	}
	return hook
}

// configHookTypeLabel names one action type the way the prototype lists them;
// the stored value stays the loader's own id.
func configHookTypeLabel(kind string) string {
	if kind == appcfg.HookTypeHTTP {
		return "HTTP"
	}
	return configTitleCase(kind)
}

// configTitleCase names a created agent that was given no display name, the
// prototype's way (plan 313): underscores read as spaces, the first character is
// upper-cased and the rest is left as written — review-agent reads Review-agent,
// a_b reads A b.
func configTitleCase(id string) string {
	text := strings.ReplaceAll(strings.TrimSpace(id), "_", " ")
	if text == "" {
		return ""
	}
	runes := []rune(text)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

// chooseNewValue asks what a new (or replaced) value is, and returns it with
// whether the caller should open it for editing. The value-type toggle is the
// prototype's five (plan 328 D4, html:160): text, number, boolean, object,
// list. A boolean's value is picked as the two real ones rather than typed
// (owner 2026-11-18: a choice lists real values), and a number takes decimals
// the way the prototype's generic editor does.
func (p *configPanel) chooseNewValue(label string, cur any) (any, bool, bool, error) {
	items := []SelectItem{
		{Label: configNewTextRow, Description: "String"},
		{Label: configNewNumberRow, Description: "Number"},
		{Label: configNewBooleanRow, Description: "True or false"},
		{Label: configNewObjectRow, Description: "Empty object; open it to edit"},
		{Label: configNewListRow, Description: "Empty list; open it to edit"},
	}
	defaultIdx := 0
	switch cur.(type) {
	case bool:
		defaultIdx = 2
	case float64, int, int64, json.Number:
		defaultIdx = 1
	case map[string]any:
		defaultIdx = 3
	case []any:
		defaultIdx = 4
	}
	idx, ok, err := p.page(label+"\nValue type", configChoiceKeys, items, defaultIdx)
	if err != nil || !ok {
		return nil, false, false, err
	}
	switch idx {
	case 0:
		text, entered, err := p.sel.Input(label+"\nValue", configScalarInput(cur))
		if err != nil || !entered {
			return nil, false, false, err
		}
		return text, false, true, nil
	case 1:
		text, entered, err := p.sel.Input(label+"\nValue", configScalarInput(cur))
		if err != nil || !entered {
			return nil, false, false, err
		}
		// The generic number takes decimals (plan 328 D4, prototype
		// html:125): an editor's own bounds are the descriptor's business,
		// this row has none.
		value, convErr := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if convErr != nil {
			return nil, false, false, fmt.Errorf("Enter a valid number.")
		}
		return value, false, true, nil
	case 2:
		boolItems := []SelectItem{
			{Label: configNewTrueRow, Description: "Boolean true"},
			{Label: configNewFalseRow, Description: "Boolean false"},
		}
		boolIdx := 0
		if boolean, isBool := cur.(bool); isBool && !boolean {
			boolIdx = 1
		}
		picked, ok, err := p.page(label+"\nValue", configChoiceKeys, boolItems, boolIdx)
		if err != nil || !ok {
			return nil, false, false, err
		}
		return picked == 0, false, true, nil
	case 3:
		return map[string]any{}, true, true, nil
	default:
		return []any{}, true, true, nil
	}
}

// configContainerIsList decides whether a container's rows are list elements.
// An existing value answers; an unset one follows the descriptor, because
// there is nothing else to read the shape from.
func configContainerIsList(field configField, container any) bool {
	if _, isList := container.([]any); isList {
		return true
	}
	if container != nil {
		return false
	}
	return field.List
}

// configFieldPath splits a descriptor's dotted path. The empty path — the
// section's value itself — is no segments.
func configFieldPath(field configField) []string {
	path := make([]string, 0, 2)
	for _, segment := range strings.Split(field.Path, ".") {
		if segment != "" {
			path = append(path, segment)
		}
	}
	return path
}

// configJoinPath appends one segment without aliasing the caller's slice: the
// editor holds paths in several pending branches at once.
func configJoinPath(path []string, segment string) []string {
	out := make([]string, len(path), len(path)+1)
	copy(out, path)
	return append(out, segment)
}

// configLookupValue reads the value at a path of the section's JSON tree.
func configLookupValue(doc any, path []string) (any, bool) {
	cur := doc
	for _, segment := range path {
		switch node := cur.(type) {
		case map[string]any:
			value, ok := node[segment]
			if !ok {
				return nil, false
			}
			cur = value
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(node) {
				return nil, false
			}
			cur = node[index]
		default:
			return nil, false
		}
	}
	return cur, true
}

// configIsContainer reports whether a value is a table or a list — something
// the row editor opens rather than edits in place.
func configIsContainer(value any) bool {
	switch value.(type) {
	case map[string]any, []any:
		return true
	}
	return false
}

// configContainerKeys lists a container's row keys: a map's keys sorted, so
// the same table always reads the same, or a list's indexes in order.
func configContainerKeys(container any) []string {
	switch node := container.(type) {
	case map[string]any:
		keys := make([]string, 0, len(node))
		for key := range node {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return keys
	case []any:
		keys := make([]string, 0, len(node))
		for i := range node {
			keys = append(keys, strconv.Itoa(i))
		}
		return keys
	}
	return nil
}

func configContainerLen(value any) int {
	switch node := value.(type) {
	case map[string]any:
		return len(node)
	case []any:
		return len(node)
	}
	return 0
}

// configAssign returns the document with the value at path replaced — making
// the maps on the way when the path does not exist yet — or, with remove, the
// document with that leaf gone. Containers are edited in place and returned,
// so a caller always keeps the value configAssign reported.
func configAssign(doc any, path []string, value any, remove bool, label string) (any, error) {
	if len(path) == 0 {
		return value, nil
	}
	if doc == nil {
		// A section with no row yet starts as an empty object: the first
		// field a user sets creates the shape it lives in.
		if remove {
			return nil, nil
		}
		doc = map[string]any{}
	}
	head, rest := path[0], path[1:]
	switch node := doc.(type) {
	case map[string]any:
		child := node[head]
		if child == nil && !remove && len(rest) > 0 {
			child = map[string]any{}
		}
		next, err := configAssign(child, rest, value, remove, label)
		if err != nil {
			return doc, err
		}
		if next == nil && remove {
			delete(node, head)
			return node, nil
		}
		node[head] = next
		return node, nil
	case []any:
		index, err := strconv.Atoi(head)
		if err != nil {
			return doc, fmt.Errorf("%s is not a list index", head)
		}
		if index < 0 || index >= len(node) {
			return doc, fmt.Errorf("list index %s is out of range", head)
		}
		next, err := configAssign(node[index], rest, value, remove, label)
		if err != nil {
			return doc, err
		}
		if next == nil && remove {
			return append(node[:index], node[index+1:]...), nil
		}
		node[index] = next
		return node, nil
	}
	// The setting's own name, never the dotted path it lives at: a rejection
	// a reader cannot type anywhere is no explanation (plan 313 Step 5b).
	if strings.TrimSpace(label) == "" {
		label = configPathLabel(path[0])
	}
	return doc, fmt.Errorf("%s is not an object or a list", label)
}

// configPathLabel names one segment of a configuration path the way the row list
// does: a map's key as written, a list's place by its 1-based number. A dotted
// configuration path never reaches a reader — the panel speaks in row labels,
// never in the names forebrain.yaml happens to use (plan 313 Step 5b).
func configPathLabel(segment string) string {
	if index, err := strconv.Atoi(segment); err == nil && index >= 0 {
		return configRowLabel(segment, index, true)
	}
	return configRowLabel(segment, 0, false)
}

// configMarshalDoc renders the page's document as the bytes its row holds. A
// section left with nothing in it stores no row at all: an absent row is how
// "this install uses the default" reads everywhere else.
func configMarshalDoc(doc any) (json.RawMessage, error) {
	if doc == nil {
		return nil, nil
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("the configuration cannot be serialized: %w", err)
	}
	switch string(raw) {
	case "{}", "[]", "null", `""`:
		return nil, nil
	}
	return raw, nil
}

// configEffortField reports whether a field stores a thinking level: what it
// holds is a level, and the page names it through turn's table, not as the
// stored id.
func configEffortField(field configField) bool {
	return configEffortPath(configFieldPath(field))
}

// configEffortPath reports whether path is one stored thinking level. It is a
// path check rather than a field check because the level of a model row — a type
// under subagent_models, the plan reviewer, one entry of a definition's
// subagents — arrives through the generic row editor, which hands the same
// descriptor down at every nesting level (plan 319).
func configEffortPath(path []string) bool {
	return len(path) > 0 && path[len(path)-1] == configEffortKey
}

// configSiblingModelRef is the model value beside one stored setting: an effort
// and its model are two keys of the same map, so a level's model is its parent
// path's "model".
func configSiblingModelRef(doc any, path []string) string {
	if len(path) < 2 {
		return ""
	}
	sibling := append(append([]string(nil), path[:len(path)-1]...), configEffortModelKey)
	value, _ := configLookupValue(doc, sibling)
	ref, _ := value.(string)
	return strings.TrimSpace(ref)
}

// configStoredEffort is a stored level as the pickers compare it: lower-cased
// and trimmed, empty when the setting is unset or holds no string.
func configStoredEffort(value any) string {
	level, _ := value.(string)
	return strings.ToLower(strings.TrimSpace(level))
}

// configFieldValueText is a field's value as a page shows it in the row's value
// column: what the setting holds, and nothing at all when it holds nothing
// (plan 350, prototype display() html:124 — an unset value is empty, never the
// word "Default"). A boolean is the one exception: a switch with a default
// reads as the value that default gives it. A stored number carries the unit it
// is counted in (html:125).
func configFieldValueText(field configField, value any, exists bool) string {
	// A secret is never the value the column shows (plan 328 D3): the field's
	// own flag, or its place in the document naming one, answers first —
	// before any default an unset setting would read as.
	if field.Secret || configIsSecretPath(field.Path) {
		return configSecretDisplay(value, exists)
	}
	if configIsContainer(value) {
		// A table or a list holds no single value to print: the row opens the
		// rows themselves (prototype display() on a container).
		return ""
	}
	if field.Kind == configFieldBool3 {
		boolean, ok := value.(bool)
		switch {
		case !ok:
			return configFieldDefaultText(field)
		case boolean:
			return configBoolOnLabel
		default:
			return configBoolOffLabel
		}
	}
	// A thinking level reads through the panel's own effort wording (plan 327
	// D1, prototype html:117-118), never as the raw id: a stored level — the
	// empty string included — is a choice, so "" is the row's own default,
	// while a key nobody stored leaves the choice to the provider.
	if configEffortField(field) {
		level, _ := value.(string)
		if !exists {
			return configRowDefaultEffort
		}
		return configEffortSummary(level)
	}
	// A stored number carries its unit ("10 attempts", plan 327 D6).
	if field.Kind == configFieldNumber && field.Unit != "" {
		if number, ok := value.(float64); ok {
			return configNumberUnitText(number, field.Unit)
		}
	}
	// A stored section always carries its settings (plan 207 §3.2), so this is
	// reached only by a leaf inside a table row — an optional field of an MCP
	// server or an LSP server, which no registered default covers.
	if !exists || value == nil {
		return configFieldDefaultText(field)
	}
	return configScalarText(value)
}

// configFieldDefaultText is an unset field's value column (plan 350,
// prototype display() html:124): a switch with a default of its own reads as the
// value that default gives it, and every other unset setting reads as nothing
// at all — the word "Default" belongs to a section the store holds no row for
// (the overview's status), never to a setting's value.
func configFieldDefaultText(field configField) string {
	if field.Kind != configFieldBool3 {
		return ""
	}
	switch strings.TrimSpace(field.DefaultValue) {
	case "true":
		return configBoolOnLabel
	case "false":
		return configBoolOffLabel
	}
	return ""
}

// configNumberUnitText is a stored number with the unit its descriptor counts
// it in (plan 327 D6, prototype html:118).
func configNumberUnitText(value float64, unit string) string {
	text := strconv.FormatFloat(value, 'f', -1, 64)
	if unit == "" {
		return text
	}
	return text + " " + unit
}

// configScalarText renders one scalar the way a row shows it.
func configScalarText(value any) string {
	switch scalar := value.(type) {
	case nil:
		return ""
	case string:
		if strings.TrimSpace(scalar) == "" {
			return "(empty)"
		}
		// A stored multiline value shows its line breaks as the marker the
		// prototype's one row carries (plan 328 D2, html:118): the row is one
		// line, the editor is where the text keeps its shape.
		return strings.ReplaceAll(scalar, "\n", " ↵ ")
	case bool:
		if scalar {
			return configBoolOnLabel
		}
		return configBoolOffLabel
	case float64:
		return strconv.FormatFloat(scalar, 'f', -1, 64)
	case json.Number:
		return scalar.String()
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return fmt.Sprint(value)
		}
		return string(raw)
	}
}

// configScalarInput is a scalar as a text field opens on it: empty when the
// setting is unset, so the field does not carry "unset" as if it were a value.
// The string is the stored one as it is — the editor, not the row, is where a
// multiline value keeps its shape, so nothing rendered ever feeds a field.
func configScalarInput(value any) string {
	if value == nil {
		return ""
	}
	switch scalar := value.(type) {
	case string:
		return scalar
	case bool:
		if scalar {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(scalar, 'f', -1, 64)
	case json.Number:
		return scalar.String()
	}
	return ""
}

// configSecretPathPattern names the places whose values the panel never shows
// (plan 328 D3, prototype html:106): the prototype's own expression, matched
// against the dotted path, quirk included — token_url and a headers entry
// mask the same as token itself.
var configSecretPathPattern = regexp.MustCompile(`(?i)(api_key|token|secret|password|headers)`)

// configIsSecretPath reports whether a setting's place in the document is one
// the panel masks. The path's own words decide, the way the prototype's
// isSecret does; a descriptor's Secret flag is the second half, for the fields
// a template names.
func configIsSecretPath(path ...string) bool {
	return configSecretPathPattern.MatchString(strings.Join(path, "."))
}

// configSecretDisplay is a secret's value column (plan 328 D3, prototype
// html:118): a set value reads as hidden and an unset one says so — the value
// itself is never on the page.
func configSecretDisplay(value any, exists bool) string {
	if !exists || value == nil {
		return configSecretUnsetStatus
	}
	if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
		return configSecretUnsetStatus
	}
	return configSecretHiddenStatus
}
