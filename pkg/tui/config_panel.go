// The /config panel (plan 190): every configuration section the state
// database stores, browsable and editable from the terminal, plus the two
// forebrain.yaml sections the panel only shows.
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
	"go.yaml.in/yaml/v2"
)

// The panel's fixed rows and notice lines.
const (
	configBackRowLabel = "Back to sections"
	configAddRowLabel  = "+ Add a row"
	configUpRowLabel   = "Up one level"
	configRowEditLabel = "Edit"
	configRowOpenLabel = "Open"
	configRowDropLabel = "Delete this row"
	configRowBackLabel = "Back"

	// configFileOnlyNote is the read-only tail every forebrain.yaml section
	// carries: the panel must not look like an editor for a section it
	// cannot write.
	configFileOnlyNote = "This section is edited in forebrain.yaml by hand only; it applies after the process restarts"

	configBoolOnLabel  = "On"
	configBoolOffLabel = "Off"
	// configDefaultActionLabel and its one line are how the panel goes back to
	// a setting's own default: an action on the choice page, never a value
	// among the real ones (owner 2026-11-16 — "Default", not "Use default").
	// The terminal's picker carries its actions as rows, so the action is the
	// choice list's last row rather than a footer button.
	configDefaultActionLabel = "Default"
	configDefaultActionHint  = "Uses the setting's built-in default"

	configNewTextRow   = "Text"
	configNewNumberRow = "Number"
	configNewTrueRow   = "On"
	configNewFalseRow  = "Off"
	configNewObjectRow = "Object"
)

// configPanelSnapshot is what the panel reads when it opens: the stored
// runtime sections as their own JSON, the forebrain.yaml sections as the
// YAML text a reader would edit by hand, whether this machine has Claude
// Code — the condition the interop switch is shown under (owner 2026-10-30) —
// and the install-wide model table, which is state rather than a section and
// is what turns a stored row id into the model name a reader recognises
// (plan 313).
type configPanelSnapshot struct {
	Stored     map[string]json.RawMessage
	File       map[string]string
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
// stored section as the JSON its fb_config row holds, and the file's startup
// sections as YAML text. The panel keeps no subscription — a save updates its
// own copy, and anything another surface wrote shows up on the next open.
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
	file := map[string]string{}
	if body, err := yaml.Marshal(struct {
		Gateway appcfg.Gateway `yaml:"gateway"`
	}{Gateway: root.Gateway}); err == nil {
		file["gateway"] = string(body)
	}
	if body, err := yaml.Marshal(struct {
		Credentials appcfg.CredentialsSection `yaml:"credentials"`
	}{Credentials: root.Credentials}); err == nil {
		file["credentials"] = string(body)
	}
	// Judged once per open: the interop switch is shown only on a machine
	// that has Claude Code's directory (plan 195 §3.7).
	return configPanelSnapshot{
		Stored: stored, File: file, ClaudeCode: process.ClaudeCodeDetected(),
		// The model table is state, not a section: the panel only names row
		// ids with it (plan 313).
		Models: root.LLMModels,
	}, nil
}

// UpdateConfigSection stores one section's JSON as its fb_config row — nil
// removes the row — through the state store, so the write is validated
// against the rest of the installed configuration before it lands. A
// forebrain.yaml section is refused: the panel only displays those, and a row
// for one would be a second copy nothing reads.
func (s *ChatSession) UpdateConfigSection(ctx context.Context, section string, raw json.RawMessage) error {
	if s == nil || s.Env == nil {
		return fmt.Errorf("the configuration panel is unavailable: no runtime environment")
	}
	schema, ok := configSectionSchemaFor(section)
	if !ok {
		return fmt.Errorf("unknown configuration section %q", section)
	}
	if schema.YAMLOnly {
		return fmt.Errorf("section %s is edited in forebrain.yaml by hand only", schema.Name)
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
	file     map[string]string
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
}

func newConfigPanel(sel Selector, renderer *Renderer, session configPanelSession) *configPanel {
	return &configPanel{sel: sel, renderer: renderer, session: session, raw: map[string]json.RawMessage{}, file: map[string]string{}}
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
	panel.file = snapshot.File
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
		p.overviewRow = p.sectionRow(name)
		name = ""
	}
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
func (p *configPanel) chooseSection() (string, bool) {
	items, names := p.sectionItems()
	label := "Configuration\nOpen a section to see and change its settings."
	idx, ok, err := selectRichTabbedPage(p.sel, label, items, TabbedSelectOptions{DefaultIdx: p.overviewRow})
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
	if schema.YAMLOnly || len(p.raw[schema.storeSection()]) > 0 {
		return ""
	}
	return configDefaultStatus
}

// editSection runs one section's page. It reports whether the panel stays
// open: true means the user asked to go back to the list. Every field edit
// writes the section as it is confirmed, so the page holds no draft and the
// only way out is back (plan 321).
func (p *configPanel) editSection(ctx context.Context, name string) bool {
	schema, ok := configSectionSchemaFor(name)
	if !ok {
		p.printError(fmt.Errorf("unknown configuration section %q", name))
		return true
	}
	if schema.YAMLOnly {
		p.showFileSection(schema)
		return true
	}
	p.section = schema.storeSection()
	p.write = func(raw json.RawMessage) error {
		return p.session.UpdateConfigSection(ctx, p.section, raw)
	}
	defer func() {
		p.section, p.write = "", nil
	}()
	doc := p.sectionDoc(name)
	notice := ""
	for {
		items, owners := p.fieldItems(schema, doc)
		idx, ok, err := selectRichPage(p.sel, p.sectionPrompt(schema, doc, notice), items, -1)
		if err != nil {
			p.printError(err)
			return false
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

// fieldItems is one section's page: its settings, each with the value it holds
// in the row's value column and its hint beside the label, then the way back.
// A field's Group names the fixed page section it renders under (plan 325
// D2), the prototype's own labels.
func (p *configPanel) fieldItems(schema configSectionSchema, doc any) ([]SelectItem, []int) {
	items := make([]SelectItem, 0, len(schema.Fields)+1)
	owners := make([]int, 0, len(schema.Fields)+1)
	for i, field := range schema.Fields {
		if !configFieldListed(field, p.claudeCode) {
			continue
		}
		description, value, status := p.configFieldRow(doc, schema, field)
		items = append(items, SelectItem{Label: field.Label, Description: description, Value: value, Status: status, Category: field.Group})
		owners = append(owners, i)
	}
	items = append(items, SelectItem{Label: configBackRowLabel, Description: "Back to the section list"})
	owners = append(owners, configRowBack)
	return items, owners
}

// configFieldRow is one setting's row on the section page: the value it holds
// in the row's value column, the hint beside it, and the status a projection
// carries. A projection's install-wide type pins read as what they resolve to
// (plan 324 D7): the model row in the value column, the thinking level in
// force as the status, and the prototype's help sentence as the description.
func (p *configPanel) configFieldRow(doc any, schema configSectionSchema, field configField) (string, string, string) {
	if schema.StoreSection == "" {
		return p.configFieldDescription(doc, field)
	}
	path := configFieldPath(field)
	if len(path) == 2 && path[0] == configSubagentModelsKey || len(path) == 1 && path[0] == configPlanReviewerKey {
		return p.configTypePinRow(doc, field, path)
	}
	return p.configFieldDescription(doc, field)
}

// configTypePinRow is one install-wide type pin's row on the projection's
// section page (plan 324 D7): the value column names the model row the pin
// stores — or the sentence for a pin nobody set — the status names the
// thinking level in force, and the description carries the field's own help
// sentence.
func (p *configPanel) configTypePinRow(doc any, field configField, path []string) (string, string, string) {
	description := strings.TrimSpace(field.Hint)
	ref := refOfRow(doc, path...)
	if ref == "" {
		return description, configFollowConversation, ""
	}
	row, _ := configLookupValue(doc, path)
	settings, _ := row.(map[string]any)
	return description, p.configModelRowLabel(ref), configEffortSummary(configStringField(settings, configEffortKey))
}

// refOfRow is one stored row's model reference, empty when it names none.
func refOfRow(doc any, path ...string) string {
	row, _ := configLookupValue(doc, path)
	settings, _ := row.(map[string]any)
	return strings.TrimSpace(configStringField(settings, "model"))
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

// sectionPrompt is the section page's heading: what the section is, and the
// last outcome — a value that did not land, or the row a page refused. The
// section's own page names it by title, never by the key forebrain.yaml stores
// it under (plan 321).
func (p *configPanel) sectionPrompt(schema configSectionSchema, doc any, notice string) string {
	head := schema.Title + "\n" + schema.Summary
	if len(schema.Fields) == 0 {
		head += "\nThis section has no setting editable from the terminal."
	}
	if notice != "" {
		head += "\n" + notice
	}
	return head
}

// showFileSection shows a forebrain.yaml section the panel cannot write, with
// the sentence that says where it is edited and when it takes effect.
func (p *configPanel) showFileSection(schema configSectionSchema) {
	body := strings.TrimSpace(p.file[schema.Name])
	if body == "" {
		body = "(not configured)"
	}
	content := body + "\n\n" + configFileOnlyNote
	if overlay, ok := p.sel.(InfoOverlaySelector); ok {
		if err := overlay.ShowInfo(schema.Title, content); err == nil {
			return
		}
	}
	if p.renderer != nil {
		p.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: schema.Name, Content: content, Final: true})
	}
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
		if definition, ok := value.(map[string]any); ok {
			delete(definition, "llm_providers")
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

// editBool3 is the three-state switch: on, off, and the setting's own default,
// which deletes the key instead of writing false. Only the two real values are
// rows in the value list; going back to the default is the choice page's
// `Default` action (owner 2026-11-16).
func (p *configPanel) editBool3(doc any, path []string, field configField, cur any) (any, string) {
	items := []SelectItem{
		{Label: configBoolOnLabel},
		{Label: configBoolOffLabel},
		{Label: configDefaultActionLabel, Description: configDefaultActionHint},
	}
	defaultIdx := len(items) - 1
	if value, ok := cur.(bool); ok {
		defaultIdx = 1
		if value {
			defaultIdx = 0
		}
	}
	idx, ok, err := selectRichPage(p.sel, p.fieldPrompt(field), items, defaultIdx)
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok {
		return doc, ""
	}
	switch idx {
	case 0:
		return p.assign(doc, path, true, false, field.Label)
	case 1:
		return p.assign(doc, path, false, false, field.Label)
	default:
		return p.assign(doc, path, nil, true, field.Label)
	}
}

// editEnum picks one of the values the loader accepts, so free text can never
// reach the store with a mode it would reject. An option with a Chinese label
// shows the label alone: the reader is choosing a behavior, not a
// configuration key (plan 203 §0.8). The list holds the real values only; the
// last row is the `Default` action, which deletes the key (owner 2026-11-16).
func (p *configPanel) editEnum(doc any, path []string, field configField, cur any) (any, string) {
	items := make([]SelectItem, 0, len(field.Options)+1)
	for _, option := range field.Options {
		item := SelectItem{Label: option}
		if label := field.OptionLabels[option]; label != "" {
			item = SelectItem{Label: label}
		}
		items = append(items, item)
	}
	items = append(items, SelectItem{Label: configDefaultActionLabel, Description: configDefaultActionHint})
	defaultIdx := len(items) - 1
	if text, ok := cur.(string); ok {
		for i, option := range field.Options {
			if option == text {
				defaultIdx = i
			}
		}
	} else if field.DefaultValue != "" {
		for i, option := range field.Options {
			if option == field.DefaultValue {
				defaultIdx = i
			}
		}
	}
	idx, ok, err := selectRichPage(p.sel, p.fieldPrompt(field), items, defaultIdx)
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok {
		return doc, ""
	}
	if idx == len(items)-1 {
		return p.assign(doc, path, nil, true, field.Label)
	}
	return p.assign(doc, path, field.Options[idx], false, field.Label)
}

// editNumber asks for an integer and holds it to the loader's bounds: an
// out-of-range value is refused in the field's own words, and a bound the
// loader simply honours (a clamp) lands on the bound with the panel saying
// which value it stored.
func (p *configPanel) editNumber(doc any, path []string, field configField, cur any) (any, string) {
	text, ok, err := p.sel.Input(p.fieldPrompt(field), configScalarInput(cur))
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok {
		return doc, ""
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return p.assign(doc, path, nil, true, field.Label)
	}
	value, convErr := strconv.Atoi(text)
	if convErr != nil {
		return doc, fmt.Sprintf("%q is not an integer; nothing was written", text)
	}
	if value < field.Min {
		return doc, fmt.Sprintf("%s may not be smaller than %d; nothing was written", field.Label, field.Min)
	}
	notice := ""
	if field.Max > 0 && value > field.Max {
		if !field.Clamp {
			return doc, fmt.Sprintf("%s may not be larger than %d; nothing was written", field.Label, field.Max)
		}
		value = field.Max
		notice = fmt.Sprintf("Stored as the maximum, %d", field.Max)
	}
	updated, note := p.assign(doc, path, value, false, field.Label)
	if note != "" {
		return updated, note
	}
	return updated, notice
}

// editText takes free text. Empty means "unset": the key goes away rather
// than storing an empty string the loader would read as an empty setting.
func (p *configPanel) editText(doc any, path []string, field configField, cur any) (any, string) {
	text, ok, err := p.sel.Input(p.fieldPrompt(field), configScalarText(cur))
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok {
		return doc, ""
	}
	if strings.TrimSpace(text) == "" {
		return p.assign(doc, path, nil, true, field.Label)
	}
	return p.assign(doc, path, text, false, field.Label)
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
		items = append(items, SelectItem{Label: turn.EffortEnglishLabel(level)})
		if level == stored {
			defaultIdx = i
		}
	}
	items = append(items, SelectItem{Label: configRowDefaultEffort})
	idx, ok, err := selectRichPage(p.sel, prompt, items, defaultIdx)
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
// stored reference that names no row stays on the list (the goal check's
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
	}
	defaultIdx := 0
	for _, row := range p.models {
		ids = append(ids, row.ID)
		items = append(items, SelectItem{
			Label:       p.configModelRowLabel(row.ID),
			Description: row.BaseURL + row.APIPath,
		})
		if row.ID == stored {
			defaultIdx = len(items) - 1
		}
	}
	if stored != "" && stored != field.ModelEmptyValue && !slices.Contains(ids, stored) && field.ModelFlavor != configModelFlavorModel {
		ids = append(ids, stored)
		items = append(items, SelectItem{Label: p.configModelRowLabel(stored)})
		defaultIdx = len(items) - 1
	}
	idx, ok, err := selectRichPage(p.sel, field.Label+"\n"+configModelPickerSub, items, defaultIdx)
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok || idx < 0 || idx >= len(items) {
		return doc, ""
	}
	return p.assignModel(doc, path, field, stored, ids[idx])
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
// (plan 324 D6): the row's label through the install's table, or the sentence
// the field gives its empty value — the memory flavor's explicit "current"
// reads the same as an unset key.
func (p *configPanel) configModelValueText(field configField, value any) string {
	ref, _ := value.(string)
	ref = strings.TrimSpace(ref)
	if ref == "" || (field.ModelEmptyValue != "" && ref == field.ModelEmptyValue) {
		return field.ModelEmpty
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
	if strings.TrimSpace(field.Hint) == "" {
		return field.Label
	}
	return field.Label + "\n" + field.Hint
}

// configFieldDescription is a field's picker row: the hint that says what the
// setting means beside the label, and the value it holds in the row's value
// column — the panel's descriptions belong to settings, not to the values they
// happen to hold right now (plan 321). A model reference names its row through
// the install's table, the way the pickers do (plan 324 D6).
func (p *configPanel) configFieldDescription(doc any, field configField) (string, string, string) {
	value, exists := configLookupValue(doc, configFieldPath(field))
	if field.Kind == configFieldModel {
		return strings.TrimSpace(field.Hint), p.configModelValueText(field, value), ""
	}
	return strings.TrimSpace(field.Hint), configFieldValueText(field, value, exists), ""
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
// knowing that shape in advance. initialNotice, when given, is the page's first
// outcome line: the guidance a created primary agent opens with (plan 313).
func (p *configPanel) editRows(doc any, path []string, field configField, initialNotice ...string) (any, string) {
	notice := ""
	if len(initialNotice) > 0 {
		notice = initialNotice[0]
	}
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
		items, owners := p.rowItems(doc, path, container, keys, list)
		idx, ok, err := selectRichPage(p.sel, p.rowsPrompt(field, keys, notice), items, -1)
		if err != nil {
			p.printError(err)
			return doc, ""
		}
		notice = ""
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
	}
}

// rowItems is one container's rows: every entry, then add and back. doc and the
// row's own path travel with it because what a row means is a property of where
// it sits: a definition's primary flag is its status marker, and a subagent
// entry's model is read in the order dispatch reads it (plan 313). A generic
// row carries what it holds in the value column (plan 325 D4) — a container
// counted, a scalar as it reads — and keeps the description for the rows the
// panel knows more about.
func (p *configPanel) rowItems(doc any, path []string, container any, keys []string, list bool) ([]SelectItem, []int) {
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
	items = append(items,
		SelectItem{Label: configAddRowLabel, Description: "Adds a row at this level"},
		SelectItem{Label: configUpRowLabel, Description: "Back up one level"},
	)
	owners = append(owners, configRowAdd, configRowUp)
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
	if primary, _ := row["primary"].(bool); primary {
		return configPrimaryStatus
	}
	return ""
}

// configRowLabel names a row: a map's key as written in the configuration, a
// list's element by its 1-based place, so the two never read alike.
func configRowLabel(key string, index int, list bool) string {
	if list {
		return "#" + strconv.Itoa(index+1)
	}
	return key
}

// configRowLabelAt names one stored row of a page. A shared subagent is named by
// the name it carries: that is its identity in the registry, and a list of
// "#1, #2" would say nothing about which subagent is which (plan 314).
func (p *configPanel) configRowLabelAt(doc any, path []string, key string, index int, list bool, value any) string {
	named := agentSubagentsPath(path) || (len(path) == 1 && path[0] == configSharedSubagentsKey)
	if named {
		if name := sharedRowName(doc, key, value); name != "" {
			return name
		}
	}
	return configRowLabel(key, index, list)
}

// configRowDescription is a row's second column: what it holds right now. A
// container is counted in the prototype's own words and carries no arrow — the
// row opens because it is a container, and a glyph promising it only decorates
// the row (plan 321).
func configRowDescription(value any) string {
	if configIsContainer(value) {
		return configContainerText(value)
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
	// configPlanReviewerKey and configSharedSubagentsKey are the two other
	// install-wide rows in the agents section the "Subagent models" page presents
	// (plan 314): the exit-plan reviewer's global row (plan 313) and the shared
	// custom registry.
	configPlanReviewerKey    = "plan_reviewer"
	configSharedSubagentsKey = "shared_subagents"
	configPrimaryStatus      = "Primary"
	// configDefaultStatus is the section list's one status word: the database
	// holds no row for the section, so it reads its built-in defaults — the
	// prototype's own marker (plan 321).
	configDefaultStatus = "Default"
	// A shared custom subagent is marked on an agent's page, where the switch
	// beside it is the agent's.
	configSharedStatus = "Shared"
	// configRowDefaultEffort is what an empty stored level means on a row that
	// names a model: the model row's own default (plan 313's reading, kept).
	configRowDefaultEffort = "Row default"
	// configEffortKey is the field every thinking level is stored under, and
	// configEffortModelKey the sibling it belongs to: the two are keys of one
	// stored map at every level of the agents section (plan 319).
	configEffortKey      = "reasoning_effort"
	configEffortModelKey = "model"
	// configEffortHeading heads a level's picker when it is opened from a row
	// rather than from a field of its own.
	configEffortHeading = "Thinking level"
	// configModelPickerSub is the one sentence under a model picker's title
	// (plan 324): every model page offers the same rows, so the page's own
	// words say what a row's description names.
	configModelPickerSub = "Choose one model; each row shows where requests go."
	// configModelConversationDesc is the goal check's empty option (plan 324
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
	// configFollowConversation is what a built-in type with no model of its
	// own runs on, in the prototype's words (plan 313; plan 324 D7 keeps the
	// sentence and drops the trailing word).
	configFollowConversation = "Follow conversation"
	// configAgentOverridePrefix and configGlobalDefaultPrefix name where a
	// subagent entry's model comes from. The prototype draws the same fact in a
	// status column; the panel keeps it in the row's description.
	configAgentOverridePrefix  = "agent override: "
	configGlobalDefaultPrefix  = "global default: "
	configPrimaryCreatedNotice = "Primary agent created. Choose its default model from the global model table."
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
	// "Subagent models" page, never here (plan 314).
	configSharedSubagentNotice = "A shared subagent: its definition is edited in Subagent models, only the switch is set here"
)

// agentSubagentsPath reports whether path is one primary agent's subagent list —
// the one place a shared subagent is switched on or off for that agent (plan
// 314). It is a path check rather than a field check because editRows hands the
// same descriptor down at every nesting level.
func agentSubagentsPath(path []string) bool {
	return len(path) == 3 && path[0] == configDefinitionsPath && path[2] == configSubagentsKey
}

// sharedSubagentNamesIn lists the names of the shared custom subagents an agents
// document holds, in registry order.
func sharedSubagentNamesIn(doc any) []string {
	registry, _ := configLookupValue(doc, []string{configSharedSubagentsKey})
	rows, _ := registry.([]any)
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		entry, _ := row.(map[string]any)
		if name := strings.ToLower(strings.TrimSpace(configStringField(entry, "name"))); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// configSharedSubagent finds one registry entry by name.
func configSharedSubagent(doc any, name string) (map[string]any, bool) {
	registry, _ := configLookupValue(doc, []string{configSharedSubagentsKey})
	rows, _ := registry.([]any)
	key := strings.ToLower(strings.TrimSpace(name))
	for _, row := range rows {
		entry, ok := row.(map[string]any)
		if !ok {
			continue
		}
		if strings.ToLower(strings.TrimSpace(configStringField(entry, "name"))) == key {
			return entry, true
		}
	}
	return nil, false
}

// isSharedSubagentName reports whether name is one of the registry's subagents.
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
	if len(path) == 1 && path[0] == configSharedSubagentsKey {
		// A registry entry's own description is what a model reads when it
		// chooses a subagent type, so it is what its row says (plan 314).
		if row, ok := value.(map[string]any); ok {
			if text := strings.TrimSpace(configStringField(row, "description")); text != "" {
				return text, ""
			}
		}
		return "", configRowDescription(value)
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
		return configGlobalDefaultPrefix + p.configModelLabel(typeRef) + " / " + turn.EffortEnglishLabel(effort), ""
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
// pickers' rows do (plan 324 D2): the display label every picker names a row
// by, with the row's host appended when more than one row of the install's
// table carries the same provider and model — two endpoints of one model are
// the same name otherwise, and the base URL is the only thing that tells them
// apart. A reference the table does not carry is shown as written, so a pin
// whose row was deleted stays readable until it is re-picked.
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
	label := llm.FormatProviderModelDisplay(entry.Provider, entry.Model)
	shared := 0
	for _, row := range p.models {
		if strings.EqualFold(strings.TrimSpace(row.Provider), entry.Provider) && strings.TrimSpace(row.Model) == entry.Model {
			shared++
		}
	}
	if shared < 2 {
		return label
	}
	host := strings.TrimPrefix(strings.TrimPrefix(entry.BaseURL, "https://"), "http://")
	if host == "" {
		return label
	}
	return label + " (" + host + ")"
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

// rowsPrompt heads a row page: where the user is, how much is there, and the
// last outcome.
func (p *configPanel) rowsPrompt(field configField, keys []string, notice string) string {
	head := field.Label
	if strings.TrimSpace(field.Hint) != "" {
		head += "\n" + field.Hint
	}
	if len(keys) == 0 {
		head += "\nThis level has no rows yet"
	} else {
		head += fmt.Sprintf("\n%d rows; open one to edit or delete it", len(keys))
	}
	if notice != "" {
		head += "\n" + notice
	}
	return head
}

// editRow opens one row of a container (plan 329 D3). A row whose real path
// resolves to a template opens that template's fixed-field page directly —
// the prototype's direct-open, with no menu between the row and its fields —
// and a scalar row inside a known collection opens its text editor the same
// way. A row nothing knows keeps the generic menu: edit or open, delete,
// back. Removal of a templated row is the footer's action, not a menu item
// (plan 326 wires it).
func (p *configPanel) editRow(doc any, path []string, key string, field configField) (any, string) {
	rowPath := configJoinPath(path, key)
	value, _ := configLookupValue(doc, rowPath)
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
		return p.editText(doc, rowPath, configTemplateFields(configTemplatePath)[0], value)
	case !container && configScalarRowTemplate(p.section, rowPath) == configTemplateString:
		return p.editText(doc, rowPath, field, value)
	}
	editLabel := configRowEditLabel
	if container {
		editLabel = configRowOpenLabel
	}
	items := []SelectItem{
		{Label: editLabel, Description: configRowDescription(value)},
		{Label: configRowDropLabel, Description: "Removes it from this level"},
		{Label: configRowBackLabel, Description: "Back to the row list"},
	}
	idx, ok, err := selectRichPage(p.sel, field.Label+" "+configRowLabel(key, 0, false), items, 0)
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
		return p.assign(doc, rowPath, nil, true, field.Label)
	default:
		return doc, ""
	}
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
		case is(2, configSharedSubagentsKey):
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
	for {
		value, _ := configLookupValue(doc, rowPath)
		settings, _ := value.(map[string]any)
		if settings == nil {
			settings = map[string]any{}
		}
		items, rows := p.templateItems(template, settings)
		idx, ok, err := selectRichPage(p.sel, p.templatePrompt(template, title, settings, notice), items, -1)
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
			return doc, configSystemSubagentNotice
		}
		if row.unknown != "" {
			doc, notice = p.openUnknownTemplateKey(doc, rowPath, row.unknown)
			continue
		}
		doc, notice = p.openTemplateField(doc, rowPath, row.field)
	}
}

// templatePrompt heads a template page: the row's own title, the system
// verdict when the whole page is read-only, and the last outcome.
func (p *configPanel) templatePrompt(template, title string, settings map[string]any, notice string) string {
	head := title
	if configTemplateRowIsSystem(template, settings) {
		head += "\n" + configSystemSubagentNotice
	}
	if notice != "" {
		head += "\n" + notice
	}
	return head
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
// additional-fields group, then the way up. customOnly hides the fields a
// built-in or system entry cannot carry; showFor hides the fields another
// action type owns; a system entry's every row carries the System status and
// the verdict in place of its hint.
func (p *configPanel) templateItems(template string, settings map[string]any) ([]SelectItem, []configTemplateRow) {
	fields := configTemplates[template]
	name := strings.ToLower(strings.TrimSpace(configStringField(settings, "name")))
	system := configTemplateRowIsSystem(template, settings)
	hookType := configHookActionType(settings)
	known := make(map[string]bool, len(fields))
	items := make([]SelectItem, 0, len(fields)+4)
	rows := make([]configTemplateRow, 0, len(fields)+4)
	for _, field := range fields {
		known[field.Path] = true
		if field.CustomOnly && configTemplateNameIsBuiltinOrSystem(name) {
			continue
		}
		if len(field.ShowFor) > 0 && !slices.Contains(field.ShowFor, hookType) {
			continue
		}
		description := field.Hint
		status := ""
		if system {
			description, status = configSystemSubagentNotice, configSystemStatus
		}
		value, exists := configLookupValue(settings, []string{field.Path})
		items = append(items, SelectItem{
			Label:       field.Label,
			Description: description,
			Value:       p.configTemplateValueText(field, value, exists),
			Status:      status,
			Category:    field.Group,
		})
		rows = append(rows, configTemplateRow{field: field})
	}
	for _, key := range configContainerKeys(settings) {
		if known[key] {
			continue
		}
		value := settings[key]
		items = append(items, SelectItem{
			Label:       configTitleCase(key),
			Description: "A key this page's template does not name; it is kept as it is",
			Value:       configRowDescription(value),
			Category:    configAdditionalFieldsGroup,
		})
		rows = append(rows, configTemplateRow{unknown: key})
	}
	items = append(items, SelectItem{Label: configUpRowLabel, Description: "Back up one level"})
	rows = append(rows, configTemplateRow{up: true})
	return items, rows
}

// configTemplateValueText is a template field's value column. A field that
// holds a model row reference names it the way the pickers do, and its empty
// value reads as the sentence the template gives it (the model picker's empty
// option, plan 324); everything else keeps the panel's one value wording.
func (p *configPanel) configTemplateValueText(field configField, value any, exists bool) string {
	if field.ModelEmpty != "" {
		ref, _ := value.(string)
		if strings.TrimSpace(ref) == "" {
			return field.ModelEmpty
		}
		return p.configModelLabel(ref)
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
	if field.Kind == configFieldRows || configIsContainer(cur) {
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
// the Subagent models page and every other row here says where it lives rather
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
		{Label: configRowBackLabel, Description: "Back to the row list"},
	}
	idx, ok, err := selectRichPage(p.sel, name+"\n"+configSharedSubagentNotice, items, 0)
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
	if idx == len(items)-1 {
		return doc, ""
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
	if list && len(path) == 1 && path[0] == configSharedSubagentsKey {
		return p.addSharedSubagent(doc, path, field, container)
	}
	if !list && configPrimaryCreationPath(field, path) {
		return p.addPrimaryAgent(doc, path, field, container)
	}
	key := ""
	if !list {
		name, ok, err := p.sel.Input(field.Label+" key of the new row", "")
		if err != nil {
			p.printError(err)
			return doc, ""
		}
		if !ok {
			return doc, ""
		}
		key = strings.TrimSpace(name)
		if key == "" {
			return doc, "the key may not be empty; nothing was written"
		}
		if rows, isMap := container.(map[string]any); isMap {
			if _, dup := rows[key]; dup {
				return doc, fmt.Sprintf("%q already exists", key)
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
		text, ok, err := p.sel.Input(p.fieldPrompt(field), "")
		if err != nil {
			p.printError(err)
			return doc, ""
		}
		if !ok {
			return doc, ""
		}
		if strings.TrimSpace(text) == "" {
			return doc, "the value may not be empty; nothing was written"
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

// configPrimaryCreationPath reports whether an add lands a new primary agent:
// the definitions map, and the top level of the agents section. The path check
// is not redundant with the field's — editRows hands the same descriptor down to
// every nested row page, so inside one agent's definition the path is longer and
// the guard must not fire there (plan 313).
func configPrimaryCreationPath(field configField, path []string) bool {
	return field.Path == configDefinitionsPath && len(path) == 1 && path[0] == configDefinitionsPath
}

// addSharedSubagent seeds one registry entry and opens it (plan 314). A shared
// subagent is a full custom definition, so the row starts with the fields the
// loader requires — a name to type, a description the model reads, a prompt to
// run on, and the read-only tool tier a subagent starts at — rather than asking
// what type of value the row is. Saving is what judges it: the registry's
// validator refuses an entry with no name, no description or no prompt, and its
// own sentence is the one the page shows.
func (p *configPanel) addSharedSubagent(doc any, path []string, field configField, container any) (any, string) {
	seed := map[string]any{
		"name":          "",
		"description":   "",
		"system_prompt": "",
		"tools":         appcfg.SubagentToolsReadOnly,
	}
	rows, _ := container.([]any)
	rows = append(append(make([]any, 0, len(rows)+1), rows...), seed)
	updated, note := p.assign(doc, path, rows, false, field.Label)
	if note != "" {
		return updated, note
	}
	opened, note := p.editRow(updated, path, strconv.Itoa(len(rows)-1), field)
	return opened, note
}

// addPrimaryAgent creates one primary agent the way the prototype's create-agent
// scene does (plan 313): an id and an optional display name, then the agent shape
// the runtime needs — primary, no MCP servers of its own, no subagents yet — and
// the new agent's own page, opened with the guidance that says what to do next.
//
// The two refusals are the ones this page owes the user: an id the loader would
// reject, and an id naming a built-in subagent type. The latter would land a
// definition IsEffectivePrimaryAgent never treats as a primary agent, because
// that key is the type's install-wide default — so it is refused here and on the
// gateway's create endpoint alike.
func (p *configPanel) addPrimaryAgent(doc any, path []string, field configField, container any) (any, string) {
	name, ok, err := p.sel.Input("Agent id of the new primary agent", "")
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok {
		return doc, ""
	}
	key := strings.TrimSpace(name)
	if key == "" {
		return doc, "the key may not be empty; nothing was written"
	}
	if !appcfg.ValidPrimaryAgentID(key) {
		return doc, "use a lowercase agent id: letters, digits, hyphens or underscores, starting with a letter"
	}
	if appcfg.IsBuiltInSubagentDefinition(key) {
		return doc, fmt.Sprintf("%q names a built-in subagent type, not a primary agent; choose a different id", key)
	}
	if rows, isMap := container.(map[string]any); isMap {
		if _, dup := rows[key]; dup {
			return doc, fmt.Sprintf("%q already exists", key)
		}
	}
	display, ok, err := p.sel.Input("Display name (empty uses the title-cased id)", "")
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok {
		return doc, ""
	}
	if display = strings.TrimSpace(display); display == "" {
		display = configTitleCase(key)
	}
	rows, isMap := container.(map[string]any)
	if !isMap || rows == nil {
		rows = make(map[string]any, 1)
	}
	rows[key] = map[string]any{
		"display_name": display,
		"primary":      true,
		"mcp_servers":  []any{},
		"subagents":    []any{},
	}
	updated, note := p.assign(doc, path, rows, false, field.Label)
	if note != "" {
		return updated, note
	}
	// The new agent opens its own template page — the fixed fields the
	// prototype's agent template holds — with the guidance that says what to
	// do next (plan 313); editTemplatePage keeps the notice as the page's
	// first line.
	opened, note := p.editTemplatePage(updated, configJoinPath(path, key), configTemplateAgent, display, configPrimaryCreatedNotice)
	return opened, note
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
	idx, ok, err := selectRichPage(p.sel, configHookEventHeading, items, 0)
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
	idx, ok, err := selectRichPage(p.sel, configHookTypeHeading, items, defaultIdx)
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
// whether the caller should open it for editing.
func (p *configPanel) chooseNewValue(label string, cur any) (any, bool, bool, error) {
	items := []SelectItem{
		{Label: configNewTextRow, Description: "String"},
		{Label: configNewNumberRow, Description: "Integer"},
		{Label: configNewTrueRow, Description: "Boolean true"},
		{Label: configNewFalseRow, Description: "Boolean false"},
		{Label: configNewObjectRow, Description: "Empty object; open it to edit"},
	}
	defaultIdx := 0
	switch value := cur.(type) {
	case bool:
		defaultIdx = 3
		if value {
			defaultIdx = 2
		}
	case float64, int, int64, json.Number:
		defaultIdx = 1
	case map[string]any:
		defaultIdx = 4
	}
	idx, ok, err := selectRichPage(p.sel, label+"\nWhat type is this row's value?", items, defaultIdx)
	if err != nil || !ok {
		return nil, false, false, err
	}
	switch idx {
	case 0:
		text, entered, err := p.sel.Input(label+" value", configScalarText(cur))
		if err != nil || !entered {
			return nil, false, false, err
		}
		return text, false, true, nil
	case 1:
		text, entered, err := p.sel.Input(label+" integer", configScalarInput(cur))
		if err != nil || !entered {
			return nil, false, false, err
		}
		value, convErr := strconv.Atoi(strings.TrimSpace(text))
		if convErr != nil {
			return nil, false, false, fmt.Errorf("%q is not an integer", strings.TrimSpace(text))
		}
		return value, false, true, nil
	case 2:
		return true, false, true, nil
	case 3:
		return false, false, true, nil
	default:
		return map[string]any{}, true, true, nil
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

// configFieldValueText is a field's value as the section page shows it in the
// row's value column. An unset setting reads as the default it follows, which
// is the one word the panel uses for it everywhere else (plan 321).
func configFieldValueText(field configField, value any, exists bool) string {
	if configIsContainer(value) {
		return configContainerText(value)
	}
	if field.Kind == configFieldBool3 {
		boolean, ok := value.(bool)
		switch {
		case !ok:
			return configDefaultStatus
		case boolean:
			return configBoolOnLabel
		default:
			return configBoolOffLabel
		}
	}
	// A thinking level reads through turn's one label table, never as the raw
	// id (plan 313, 2026-11-17 reasoning-semantics correction).
	if configEffortField(field) {
		level, _ := value.(string)
		if strings.TrimSpace(level) == "" {
			return configDefaultStatus
		}
		return turn.EffortEnglishLabel(level)
	}
	// A stored section always carries its settings (plan 207 §3.2), so this is
	// reached only by a leaf inside a table row — an optional field of an MCP
	// server or an LSP server, which no registered default covers.
	if !exists || value == nil {
		return configDefaultStatus
	}
	return configScalarText(value)
}

// configContainerText is a table or list as a row's value column counts it: a
// list's items, a map's entries — the prototype's two words for the two shapes.
func configContainerText(value any) string {
	n := strconv.Itoa(configContainerLen(value))
	if _, isList := value.([]any); isList {
		return n + " items"
	}
	return n + " entries"
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
		return scalar
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
