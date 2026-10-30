// The /config panel (plan 190): every configuration section the state
// database stores, browsable and editable from the terminal, plus the two
// forebrain.yaml sections the panel only shows.
//
// A save writes the section's JSON through the state store's one legal write
// path — ConfigStore.UpdateSection, with the process-wide section validator
// behind it — and nothing is applied here: the change lands on the next
// turn's configuration sync. That is why every successful save says the same
// sentence, and why a rejected write says the validator's own words instead
// of a shape of its own.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"go.yaml.in/yaml/v2"
)

// The panel's fixed rows and notice lines.
const (
	configSaveRowLabel = "保存并生效"
	configBackRowLabel = "返回段列表"
	configAddRowLabel  = "＋ 新增一行"
	configUpRowLabel   = "返回上一级"
	configRowEditLabel = "编辑"
	configRowOpenLabel = "打开"
	configRowDropLabel = "删除此行"
	configRowBackLabel = "返回"

	configSavedNotice = "已写入数据库，下一 turn 生效"
	// configFileOnlyNote is the read-only tail every forebrain.yaml section
	// carries: the panel must not look like an editor for a section it
	// cannot write.
	configFileOnlyNote = "此段只能手动编辑 forebrain.yaml，重启进程后生效"

	configBoolOnLabel    = "开"
	configBoolOffLabel   = "关"
	configBoolUnsetLabel = "默认（不设置）"
	configUnsetLabel     = "默认（不设置）"

	configNewTextRow   = "文本"
	configNewNumberRow = "数字"
	configNewTrueRow   = "开"
	configNewFalseRow  = "关"
	configNewObjectRow = "子对象"
)

// configPanelSnapshot is what the panel reads when it opens: the stored
// runtime sections as their own JSON, and the forebrain.yaml sections as the
// YAML text a reader would edit by hand.
type configPanelSnapshot struct {
	Stored map[string]json.RawMessage
	File   map[string]string
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
		return configPanelSnapshot{}, fmt.Errorf("配置面板不可用：没有运行时环境")
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
	return configPanelSnapshot{Stored: stored, File: file}, nil
}

// UpdateConfigSection stores one section's JSON as its fb_config row — nil
// removes the row — through the state store, so the write is validated
// against the rest of the installed configuration before it lands. A
// forebrain.yaml section is refused: the panel only displays those, and a row
// for one would be a second copy nothing reads.
func (s *ChatSession) UpdateConfigSection(ctx context.Context, section string, raw json.RawMessage) error {
	if s == nil || s.Env == nil {
		return fmt.Errorf("配置面板不可用：没有运行时环境")
	}
	schema, ok := configSectionSchemaFor(section)
	if !ok {
		return fmt.Errorf("未知配置段 %q", section)
	}
	if schema.YAMLOnly {
		return fmt.Errorf("段 %s 只能手动编辑 forebrain.yaml", schema.Name)
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
// and the selector it drives. Every edit is staged in the section's own JSON
// tree and written only when the user chooses to save.
type configPanel struct {
	sel      Selector
	renderer *Renderer
	session  configPanelSession
	raw      map[string]json.RawMessage
	file     map[string]string
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
		c.renderer.PrintError(fmt.Errorf("/config 不可用：这个会话没有挂载配置源"))
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
	start := ""
	if len(args) > 0 {
		schema, known := configSectionSchemaFor(args[0])
		if !known {
			c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "config", Content: "/config " + args[0] + " 不是已知配置段。\n可用：" + strings.Join(configSectionNames(), "、"), Final: true})
			return true
		}
		start = schema.Name
	}
	panel.run(ctx, start)
	return true
}

// run walks the panel: the section list, then one section's page, until the
// user closes it. Esc returns one level — from a section back to the list,
// and from the list out of the panel.
func (p *configPanel) run(ctx context.Context, start string) {
	name := start
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
		name = ""
	}
}

// chooseSection lists every section, tabbed by the group its descriptor names.
func (p *configPanel) chooseSection() (string, bool) {
	items, names := p.sectionItems()
	label := "配置\n运行时配置段。保存写入数据库，下一 turn 生效；标 [YAML] 的段只读。"
	idx, ok, err := selectRichTabbed(p.sel, label, items, TabbedSelectOptions{DefaultIdx: 0})
	if err != nil {
		p.printError(err)
		return "", false
	}
	if !ok || idx < 0 || idx >= len(names) {
		return "", false
	}
	return names[idx], true
}

// sectionItems is the section list: the name the user types, and a
// description carrying its Chinese title, what it is for, and whether it is
// set today.
func (p *configPanel) sectionItems() ([]SelectItem, []string) {
	items := make([]SelectItem, 0, len(configSectionSchemas))
	names := make([]string, 0, len(configSectionSchemas))
	for _, schema := range configSectionSchemas {
		items = append(items, SelectItem{
			Label:       schema.Name,
			Description: p.sectionDescription(schema),
			Category:    schema.Tab,
		})
		names = append(names, schema.Name)
	}
	return items, names
}

func (p *configPanel) sectionDescription(schema configSectionSchema) string {
	desc := schema.Title + " · " + schema.Summary
	switch {
	case schema.YAMLOnly:
		return "[YAML] " + desc + " · 只读"
	case len(p.raw[schema.Name]) > 0:
		return desc + " · 已设置"
	default:
		return desc + " · 默认"
	}
}

// editSection runs one section's page. It reports whether the panel stays
// open: true means the user asked to go back to the list.
func (p *configPanel) editSection(ctx context.Context, name string) bool {
	schema, ok := configSectionSchemaFor(name)
	if !ok {
		p.printError(fmt.Errorf("未知配置段 %q", name))
		return true
	}
	if schema.YAMLOnly {
		p.showFileSection(schema)
		return true
	}
	doc := p.sectionDoc(name)
	notice := ""
	for {
		items, owners := p.fieldItems(schema, doc)
		idx, ok, err := p.sel.SelectRich(p.sectionPrompt(schema, doc, notice), items, -1)
		if err != nil {
			p.printError(err)
			return false
		}
		notice = ""
		if !ok || idx < 0 || idx >= len(owners) {
			return true
		}
		switch owners[idx] {
		case configRowSave:
			if err := p.save(ctx, name, doc); err != nil {
				// The validator's own words: a refusal is explained, never
				// swallowed into a generic "save failed", and the staged
				// edits stay on the page so they can be corrected.
				notice = err.Error()
				continue
			}
			p.renderSaved(name)
			return true
		case configRowBack:
			return true
		default:
			updated, note := p.editField(doc, schema.Fields[owners[idx]])
			doc, notice = updated, note
		}
	}
}

// The page's two action rows carry negative owners so a field index and an
// action never collide.
const (
	configRowSave = -1
	configRowBack = -2
)

// fieldItems is one section's page: its settings with their current values,
// then save and back.
func (p *configPanel) fieldItems(schema configSectionSchema, doc any) ([]SelectItem, []int) {
	items := make([]SelectItem, 0, len(schema.Fields)+2)
	owners := make([]int, 0, len(schema.Fields)+2)
	for i, field := range schema.Fields {
		items = append(items, SelectItem{Label: field.Label, Description: configFieldDescription(doc, field)})
		owners = append(owners, i)
	}
	items = append(items,
		SelectItem{Label: configSaveRowLabel, Description: "写入数据库（fb_config），下一 turn 生效"},
		SelectItem{Label: configBackRowLabel, Description: "回到段列表；未保存的改动丢弃"},
	)
	owners = append(owners, configRowSave, configRowBack)
	return items, owners
}

// sectionPrompt is the section page's heading: what the section is, what it
// holds now, and the last outcome — a save's sentence or a refusal's text.
func (p *configPanel) sectionPrompt(schema configSectionSchema, doc any, notice string) string {
	head := schema.Name + " · " + schema.Title + "\n" + schema.Summary
	switch {
	case len(p.raw[schema.Name]) == 0:
		head += " · 当前：默认（数据库里没有这一段的记录）"
	default:
		head += " · 当前：已设置"
	}
	if len(schema.Fields) == 0 {
		head += "\n这一段没有可在终端编辑的设置项。"
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
		body = "（未配置）"
	}
	content := body + "\n\n" + configFileOnlyNote
	if overlay, ok := p.sel.(InfoOverlaySelector); ok {
		if err := overlay.ShowInfo(schema.Name+" · "+schema.Title, content); err == nil {
			return
		}
	}
	if p.renderer != nil {
		p.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: schema.Name, Content: content, Final: true})
	}
}

// save writes the staged document through the session, and keeps the panel's
// own copy in step so the section list's marker is right the moment it is
// shown again.
func (p *configPanel) save(ctx context.Context, name string, doc any) error {
	raw, err := configMarshalDoc(doc)
	if err != nil {
		return err
	}
	if err := p.session.UpdateConfigSection(ctx, name, raw); err != nil {
		return err
	}
	if len(raw) == 0 {
		delete(p.raw, name)
	} else {
		p.raw[name] = raw
	}
	return nil
}

func (p *configPanel) renderSaved(name string) {
	if p.renderer == nil {
		return
	}
	p.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: name, Content: configSavedNotice, Final: true})
}

func (p *configPanel) printError(err error) {
	if p.renderer != nil && err != nil {
		p.renderer.PrintError(err)
	}
}

// sectionDoc decodes the section's stored row into the generic tree the
// editor stages edits in. An absent row is a nil document: every field then
// reads as unset.
func (p *configPanel) sectionDoc(name string) any {
	raw := p.raw[name]
	if len(raw) == 0 {
		return nil
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	return doc
}

// configSectionNames lists the section names in the panel's own order, for
// the sentence an unknown /config argument gets.
func configSectionNames() []string {
	names := make([]string, 0, len(configSectionSchemas))
	for _, schema := range configSectionSchemas {
		names = append(names, schema.Name)
	}
	return names
}

// editField routes one setting to its widget: a table opens its rows, a value
// that turns out to be a table is edited as rows whatever the descriptor says
// (that is how approval_policy's granular object is reached), and a scalar
// gets the widget its kind names.
func (p *configPanel) editField(doc any, field configField) (any, string) {
	path := configFieldPath(field)
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
	default:
		return p.editText(doc, path, field, cur)
	}
}

// editBool3 is the three-state switch. "默认" deletes the key instead of
// writing false, because for a *bool that is what "unset" means.
func (p *configPanel) editBool3(doc any, path []string, field configField, cur any) (any, string) {
	items := []SelectItem{
		{Label: configBoolOnLabel, Description: "写入 true"},
		{Label: configBoolOffLabel, Description: "写入 false"},
		{Label: configBoolUnsetLabel, Description: "不写这个键，用内置默认"},
	}
	defaultIdx := len(items) - 1
	if value, ok := cur.(bool); ok {
		defaultIdx = 1
		if value {
			defaultIdx = 0
		}
	}
	idx, ok, err := p.sel.SelectRich(p.fieldPrompt(field), items, defaultIdx)
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok {
		return doc, ""
	}
	switch idx {
	case 0:
		return p.assign(doc, path, true, false)
	case 1:
		return p.assign(doc, path, false, false)
	default:
		return p.assign(doc, path, nil, true)
	}
}

// editEnum picks one of the values the loader accepts, so free text can never
// reach the store with a mode it would reject.
func (p *configPanel) editEnum(doc any, path []string, field configField, cur any) (any, string) {
	items := make([]SelectItem, 0, len(field.Options)+1)
	for _, option := range field.Options {
		items = append(items, SelectItem{Label: option, Description: "写入 " + option})
	}
	items = append(items, SelectItem{Label: configUnsetLabel, Description: "不写这个键，用内置默认"})
	defaultIdx := len(items) - 1
	if text, ok := cur.(string); ok {
		for i, option := range field.Options {
			if option == text {
				defaultIdx = i
			}
		}
	}
	idx, ok, err := p.sel.SelectRich(p.fieldPrompt(field), items, defaultIdx)
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok {
		return doc, ""
	}
	if idx == len(items)-1 {
		return p.assign(doc, path, nil, true)
	}
	return p.assign(doc, path, field.Options[idx], false)
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
		return p.assign(doc, path, nil, true)
	}
	value, convErr := strconv.Atoi(text)
	if convErr != nil {
		return doc, "「" + text + "」不是整数，未写入"
	}
	if value < field.Min {
		return doc, fmt.Sprintf("%s 不能小于 %d，未写入", field.Label, field.Min)
	}
	notice := ""
	if field.Max > 0 && value > field.Max {
		if !field.Clamp {
			return doc, fmt.Sprintf("%s 不能大于 %d，未写入", field.Label, field.Max)
		}
		value = field.Max
		notice = fmt.Sprintf("按上限存为 %d", field.Max)
	}
	updated, note := p.assign(doc, path, value, false)
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
		return p.assign(doc, path, nil, true)
	}
	return p.assign(doc, path, text, false)
}

// assign stages one edit in the document and reports its refusal, if any.
func (p *configPanel) assign(doc any, path []string, value any, remove bool) (any, string) {
	updated, err := configAssign(doc, path, value, remove)
	if err != nil {
		p.printError(err)
		return doc, err.Error()
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

// fieldDescription is a field's picker row: the value in force, then the hint.
func configFieldDescription(doc any, field configField) string {
	value, exists := configLookupValue(doc, configFieldPath(field))
	state := "当前：" + configFieldValueText(field, value, exists)
	if strings.TrimSpace(field.Hint) == "" {
		return state
	}
	return state + " · " + field.Hint
}

// configRowAdd and configRowUp own the row page's two action rows; a row's
// own index is never negative, and the section page's own actions use
// different values again.
const (
	configRowAdd = -3
	configRowUp  = -4
)

// editRows edits one table or list as rows. A row that holds a table opens
// its own rows one level down, so a provider inside an agent's llm_providers
// is reachable without the panel knowing that shape in advance.
func (p *configPanel) editRows(doc any, path []string, field configField) (any, string) {
	notice := ""
	for {
		container, _ := configLookupValue(doc, path)
		keys := configContainerKeys(container)
		items, owners := p.rowItems(container, keys, configContainerIsList(field, container))
		idx, ok, err := p.sel.SelectRich(p.rowsPrompt(field, keys, notice), items, -1)
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

// rowItems is one container's rows: every entry, then add and back.
func (p *configPanel) rowItems(container any, keys []string, list bool) ([]SelectItem, []int) {
	items := make([]SelectItem, 0, len(keys)+2)
	owners := make([]int, 0, len(keys)+2)
	for i, key := range keys {
		value, _ := configLookupValue(container, []string{key})
		items = append(items, SelectItem{Label: configRowLabel(key, i, list), Description: configRowDescription(value)})
		owners = append(owners, i)
	}
	items = append(items,
		SelectItem{Label: configAddRowLabel, Description: "在这一层加一行"},
		SelectItem{Label: configUpRowLabel, Description: "回到上一级；已保存过的行保持已保存"},
	)
	owners = append(owners, configRowAdd, configRowUp)
	return items, owners
}

// configRowLabel names a row: a map's key as written in the configuration, a
// list's element by its 1-based place, so the two never read alike.
func configRowLabel(key string, index int, list bool) string {
	if list {
		return "#" + strconv.Itoa(index+1)
	}
	return key
}

// configRowDescription is a row's second column: what it holds right now.
func configRowDescription(value any) string {
	if configIsContainer(value) {
		return "› " + strconv.Itoa(configContainerLen(value)) + " 项"
	}
	if value == nil {
		return "（空）"
	}
	return "= " + configScalarText(value)
}

// rowsPrompt heads a row page: where the user is, how much is there, and the
// last outcome.
func (p *configPanel) rowsPrompt(field configField, keys []string, notice string) string {
	head := field.Label
	if strings.TrimSpace(field.Hint) != "" {
		head += "\n" + field.Hint
	}
	if len(keys) == 0 {
		head += "\n这一层还没有行"
	} else {
		head += fmt.Sprintf("\n共 %d 行；选中一行可编辑或删除", len(keys))
	}
	if notice != "" {
		head += "\n" + notice
	}
	return head
}

// editRow is one row's menu: edit its value (or open it, when it is a table),
// delete it, or go back.
func (p *configPanel) editRow(doc any, path []string, key string, field configField) (any, string) {
	rowPath := configJoinPath(path, key)
	value, _ := configLookupValue(doc, rowPath)
	editLabel := configRowEditLabel
	if configIsContainer(value) {
		editLabel = configRowOpenLabel
	}
	items := []SelectItem{
		{Label: editLabel, Description: configRowDescription(value)},
		{Label: configRowDropLabel, Description: "从这一层去掉，保存后生效"},
		{Label: configRowBackLabel, Description: "回到行列表"},
	}
	idx, ok, err := p.sel.SelectRich(field.Label+" · "+configRowLabel(key, 0, false), items, 0)
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok {
		return doc, ""
	}
	switch idx {
	case 0:
		if configIsContainer(value) {
			return p.editRows(doc, rowPath, field)
		}
		return p.editRowValue(doc, rowPath, field, value)
	case 1:
		return p.assign(doc, rowPath, nil, true)
	default:
		return doc, ""
	}
}

// editRowValue edits a row that holds a scalar. The value's type is the
// user's choice rather than a guess: an env map needs text, a workspace_roots
// map needs a boolean, and nothing in the document says which one this is.
func (p *configPanel) editRowValue(doc any, rowPath []string, field configField, cur any) (any, string) {
	value, _, ok, err := p.chooseNewValue(field.Label, cur)
	if err != nil {
		p.printError(err)
		return doc, ""
	}
	if !ok {
		return doc, ""
	}
	return p.assign(doc, rowPath, value, false)
}

// addRow appends one row and, when the new row is an object, opens it so the
// user shapes it straight away.
func (p *configPanel) addRow(doc any, path []string, field configField) (any, string) {
	container, _ := configLookupValue(doc, path)
	list := configContainerIsList(field, container)
	key := ""
	if !list {
		name, ok, err := p.sel.Input(field.Label+" · 新行的键名", "")
		if err != nil {
			p.printError(err)
			return doc, ""
		}
		if !ok {
			return doc, ""
		}
		key = strings.TrimSpace(name)
		if key == "" {
			return doc, "键名不能为空，未写入"
		}
		if rows, isMap := container.(map[string]any); isMap {
			if _, dup := rows[key]; dup {
				return doc, "「" + key + "」已存在"
			}
		}
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
	childPath := configJoinPath(path, key)
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
	updated, note := p.assign(doc, path, next, false)
	if note != "" || !open {
		return updated, note
	}
	opened, note := p.editRows(updated, childPath, field)
	return opened, note
}

// chooseNewValue asks what a new (or replaced) value is, and returns it with
// whether the caller should open it for editing.
func (p *configPanel) chooseNewValue(label string, cur any) (any, bool, bool, error) {
	items := []SelectItem{
		{Label: configNewTextRow, Description: "字符串"},
		{Label: configNewNumberRow, Description: "整数"},
		{Label: configNewTrueRow, Description: "布尔 true"},
		{Label: configNewFalseRow, Description: "布尔 false"},
		{Label: configNewObjectRow, Description: "空对象，随后打开编辑"},
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
	idx, ok, err := p.sel.SelectRich(label+"\n这一行的值是什么类型", items, defaultIdx)
	if err != nil || !ok {
		return nil, false, false, err
	}
	switch idx {
	case 0:
		text, entered, err := p.sel.Input(label+" · 值", configScalarText(cur))
		if err != nil || !entered {
			return nil, false, false, err
		}
		return text, false, true, nil
	case 1:
		text, entered, err := p.sel.Input(label+" · 整数", configScalarInput(cur))
		if err != nil || !entered {
			return nil, false, false, err
		}
		value, convErr := strconv.Atoi(strings.TrimSpace(text))
		if convErr != nil {
			return nil, false, false, fmt.Errorf("「%s」不是整数", strings.TrimSpace(text))
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
func configAssign(doc any, path []string, value any, remove bool) (any, error) {
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
		next, err := configAssign(child, rest, value, remove)
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
			return doc, fmt.Errorf("%s 不是列表下标", head)
		}
		if index < 0 || index >= len(node) {
			return doc, fmt.Errorf("列表下标 %s 超出范围", head)
		}
		next, err := configAssign(node[index], rest, value, remove)
		if err != nil {
			return doc, err
		}
		if next == nil && remove {
			return append(node[:index], node[index+1:]...), nil
		}
		node[index] = next
		return node, nil
	}
	return doc, fmt.Errorf("%s 这一层不是对象或列表", strings.Join(path, "."))
}

// configMarshalDoc renders the staged document as the bytes its row holds. A
// section left with nothing in it stores no row at all: an absent row is how
// "this install uses the default" reads everywhere else.
func configMarshalDoc(doc any) (json.RawMessage, error) {
	if doc == nil {
		return nil, nil
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("配置无法序列化：%w", err)
	}
	switch string(raw) {
	case "{}", "[]", "null", `""`:
		return nil, nil
	}
	return raw, nil
}

// configFieldValueText is a field's value as the section page shows it.
func configFieldValueText(field configField, value any, exists bool) string {
	if configIsContainer(value) {
		return strconv.Itoa(configContainerLen(value)) + " 项"
	}
	if field.Kind == configFieldBool3 {
		boolean, ok := value.(bool)
		switch {
		case !ok:
			return "默认"
		case boolean:
			return configBoolOnLabel
		default:
			return configBoolOffLabel
		}
	}
	if !exists || value == nil {
		return "未设置"
	}
	return configScalarText(value)
}

// configScalarText renders one scalar the way a row shows it.
func configScalarText(value any) string {
	switch scalar := value.(type) {
	case nil:
		return ""
	case string:
		if strings.TrimSpace(scalar) == "" {
			return "（空）"
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
// setting is unset, so the field does not carry "未设置" as if it were a value.
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
