package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/stretchr/testify/require"
)

func TestSessionFastAvailableNilSession(t *testing.T) {
	if sessionFastAvailable(nil) {
		t.Fatalf("expected false for nil session, got true")
	}
}

func TestSessionFastAvailableModelGating(t *testing.T) {
	cases := []struct {
		name  string
		model string
		want  bool
	}{
		{"opus", "anthropic / claude-opus-4-7", true},
		{"opus with extra label", "anthropic / claude-opus-4-7 - fast", true},
		{"sonnet", "anthropic / claude-sonnet-4-6", false},
		{"haiku", "anthropic / claude-haiku-4-5", false},
		{"openai", "openai / gpt-5.3", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := &fakeSession{model: tc.model}
			if got := sessionFastAvailable(session); got != tc.want {
				t.Fatalf("sessionFastAvailable(%q)=%v want %v", tc.model, got, tc.want)
			}
		})
	}
}

type stubSelector struct {
	selectValue   string
	selectOK      bool
	selectErr     error
	selectRichIdx int
	selectRichOK  bool
	selectRichErr error

	// Captured from the last SelectRich call so a test can assert on what the
	// picker was actually shown, not just what it returned.
	selectRichItems      []SelectItem
	selectRichDefaultIdx int
}

func (s *stubSelector) Select(string, []string, string) (string, bool, error) {
	return s.selectValue, s.selectOK, s.selectErr
}

func (s *stubSelector) MultiSelect(string, []string, []string) ([]string, bool, error) {
	return nil, false, nil
}

func (s *stubSelector) Input(string, string) (string, bool, error) { return "", false, nil }
func (s *stubSelector) Confirm(string, bool) (bool, bool, error)   { return false, false, nil }
func (s *stubSelector) SelectRich(_ string, items []SelectItem, defaultIdx int) (int, bool, error) {
	s.selectRichItems = items
	s.selectRichDefaultIdx = defaultIdx
	return s.selectRichIdx, s.selectRichOK, s.selectRichErr
}

func (s *stubSelector) Secret(label string, defaultValue string) (string, bool, error) {
	return s.Input(label, defaultValue)
}

func (s *stubSelector) Review(string, []turn.StatusFact, []string, int) (int, bool, error) {
	return -1, false, nil
}

// scriptedSelector plays back a scripted answer per selector primitive, so a
// test can walk the /skills picker the way a user does.
type scriptedSelector struct {
	richSteps    []richStep
	richIdx      int
	inputs       []selectorInput
	inputIdx     int
	multi        []string
	multiOK      bool
	multiOptions []string
	richLabels   []string
	richItems    [][]SelectItem
}

// A step answers by label when set — the row a user meant, wherever the menu
// puts it — and by idx otherwise.
type richStep struct {
	idx   int
	ok    bool
	err   error
	label string
}

func (s *scriptedSelector) Select(string, []string, string) (string, bool, error) {
	return "", false, nil
}

func (s *scriptedSelector) MultiSelect(_ string, options []string, _ []string) ([]string, bool, error) {
	s.multiOptions = append(s.multiOptions, options...)
	return s.multi, s.multiOK, nil
}

func (s *scriptedSelector) Input(string, string) (string, bool, error) {
	if s.inputIdx >= len(s.inputs) {
		return "", false, nil
	}
	step := s.inputs[s.inputIdx]
	s.inputIdx++
	return step.value, step.ok, step.err
}

func (s *scriptedSelector) Confirm(string, bool) (bool, bool, error) { return false, false, nil }

func (s *scriptedSelector) SelectRich(label string, items []SelectItem, _ int) (int, bool, error) {
	s.richLabels = append(s.richLabels, label)
	s.richItems = append(s.richItems, items)
	if s.richIdx >= len(s.richSteps) {
		return -1, false, nil
	}
	step := s.richSteps[s.richIdx]
	s.richIdx++
	if step.label != "" {
		for i, item := range items {
			if item.Label == step.label {
				return i, step.ok, step.err
			}
		}
		return -1, false, nil
	}
	return step.idx, step.ok, step.err
}

func (s *scriptedSelector) Secret(label string, defaultValue string) (string, bool, error) {
	return s.Input(label, defaultValue)
}

func (s *scriptedSelector) Review(string, []turn.StatusFact, []string, int) (int, bool, error) {
	return -1, false, nil
}

func skillsTestSession() *fakeSession {
	return &fakeSession{
		skillToggleOpts: []skill.Entry{
			{Name: "repo", Description: "inspect repo", Origin: skill.OriginProject, Path: "/skills/repo", Enabled: true},
			{Name: "skill-workshop", Description: "work on skills", Origin: skill.OriginBuiltin, Path: "/skills/.system/skill-workshop", Enabled: true},
		},
	}
}

// The whole point of the merge is that one command carries every skill
// affordance, so the top-level picker must offer the manage actions and list
// the installed skills in the same view.
func TestSkillsMenuOffersManageActionsAndInstalledSkills(t *testing.T) {
	session := skillsTestSession()
	selector := &scriptedSelector{}
	var out bytes.Buffer
	ctrl := newCommandController(session, NewRenderer(&out, &out), selector, nil, "")
	if _, submitted := ctrl.handleSkills(context.Background()); submitted {
		t.Fatalf("cancelling the picker must not submit a turn")
	}
	if len(selector.richItems) != 1 {
		t.Fatalf("expected one picker, got %d", len(selector.richItems))
	}
	labels := make([]string, 0, len(selector.richItems[0]))
	for _, item := range selector.richItems[0] {
		labels = append(labels, item.Label)
	}
	joined := strings.Join(labels, "|")
	for _, want := range []string{"Add a skill", "Create a skill", "Improve a skill", "Enable / disable skills", "repo"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q in skills menu, got %q", want, joined)
		}
	}
	if strings.Contains(out.String(), "inspect repo") {
		t.Fatalf("expected no fallback rendering on cancel, got %q", out.String())
	}
	// Skills sit in their layer's tab, actions in the Manage tab at the end,
	// so the labels are exactly the names and no skill row trails a Manage row.
	firstManage := -1
	skills := map[string]string{}
	for i, item := range selector.richItems[0] {
		if item.Category == "Manage" {
			if firstManage < 0 {
				firstManage = i
			}
			continue
		}
		skills[item.Label] = item.Category
	}
	if skills["repo"] != "Project" {
		t.Fatalf("repo tab = %q, want Project", skills["repo"])
	}
	if skills["skill-workshop"] != "Built-in" {
		t.Fatalf("skill-workshop tab = %q, want Built-in", skills["skill-workshop"])
	}
	manageCount := 0
	for _, item := range selector.richItems[0] {
		if item.Category == "Manage" {
			manageCount++
		}
	}
	if manageCount != 4 || firstManage != len(selector.richItems[0])-manageCount {
		t.Fatalf("manage rows = %d, first at %d of %d", manageCount, firstManage, len(selector.richItems[0]))
	}
}

func TestSkillsRunNowSubmitsSkillSelection(t *testing.T) {
	session := skillsTestSession()
	selector := &scriptedSelector{
		richSteps: []richStep{{label: "repo", ok: true}, {idx: 0, ok: true}},
		inputs:    []selectorInput{{value: "summarize the repo", ok: true}},
	}
	var out bytes.Buffer
	ctrl := newCommandController(session, NewRenderer(&out, &out), selector, nil, "")
	sub, submitted := ctrl.handleSkills(context.Background())
	if !submitted {
		t.Fatal("expected running a skill to submit a turn")
	}
	if session.skillActivationName != "repo" {
		t.Fatalf("selection resolved for %q, want repo", session.skillActivationName)
	}
	if sub.SkillName != "repo" || sub.SkillPath != "/skills/repo/SKILL.md" {
		t.Fatalf("submission missing trusted skill selection: %+v", sub)
	}
	if !strings.Contains(sub.Text, "summarize the repo") {
		t.Fatalf("submission text = %q, want the typed request", sub.Text)
	}
}

// Creating and improving skills is the workshop's job now; the command must
// hand off rather than ask the user to paste SKILL.md into a one-line prompt.
func TestSkillsCreateHandsOffToWorkshop(t *testing.T) {
	session := skillsTestSession()
	selector := &scriptedSelector{
		richSteps: []richStep{{label: "Create a skill", ok: true}},
		inputs:    []selectorInput{{value: "write release notes", ok: true}},
	}
	var out bytes.Buffer
	ctrl := newCommandController(session, NewRenderer(&out, &out), selector, nil, "")
	sub, submitted := ctrl.handleSkills(context.Background())
	if !submitted {
		t.Fatal("expected create to submit a turn")
	}
	if session.skillActivationName != skillWorkshopSkillName {
		t.Fatalf("activation loaded for %q, want %s", session.skillActivationName, skillWorkshopSkillName)
	}
	if !strings.Contains(sub.Text, "write release notes") {
		t.Fatalf("submission text = %q, want the described intent", sub.Text)
	}
}

// The old install flow asked for five free-text values with no hint of what a
// valid answer looked like. Installing from a source now takes the reference
// and the scope, and nothing else.
func TestSkillsInstallFromSourceAsksOnlyForSourceAndScope(t *testing.T) {
	session := skillsTestSession()
	selector := &scriptedSelector{
		richSteps: []richStep{
			{label: "Add a skill", ok: true},
			{idx: 2, ok: true}, // From a GitHub repo or URL
			{idx: 0, ok: true}, // global
		},
		inputs: []selectorInput{{value: "openai/skills", ok: true}},
	}
	var out bytes.Buffer
	ctrl := newCommandController(session, NewRenderer(&out, &out), selector, nil, "")
	if _, submitted := ctrl.handleSkills(context.Background()); submitted {
		t.Fatal("a local install must not submit a turn")
	}
	if session.installedSkill != "openai/skills:global" {
		t.Fatalf("install called with %q", session.installedSkill)
	}
	if selector.inputIdx != 1 {
		t.Fatalf("expected exactly one prompt, got %d", selector.inputIdx)
	}
	if !strings.Contains(strings.Join(selector.richLabels, "|"), "Where should it come from?") {
		t.Fatalf("expected a source picker, got %q", selector.richLabels)
	}
}

func TestSkillsSubmenuCancelReturnsToTopLevel(t *testing.T) {
	session := skillsTestSession()
	selector := &scriptedSelector{
		richSteps: []richStep{
			{label: "Add a skill", ok: true},    // into the source picker
			{idx: -1, ok: false},                // cancel the source picker
			{label: "Create a skill", ok: true}, // back on the top menu
		},
		inputs: []selectorInput{{value: "write release notes", ok: true}},
	}
	var out bytes.Buffer
	ctrl := newCommandController(session, NewRenderer(&out, &out), selector, nil, "")
	if _, submitted := ctrl.handleSkills(context.Background()); !submitted {
		t.Fatal("expected the top-level menu to stay active after a submenu cancel")
	}
}

// tabbedRecordingSelector records every tabbed picker call so a test can
// assert the shape the /skills lists were opened with. The tabbed steps
// answer by label when set (the row the user meant, wherever the menu puts
// it) and by idx otherwise; an exhausted queue cancels, as scriptedSelector
// does for SelectRich. Sub-flows keep going through the embedded
// scriptedSelector.
type tabbedRecordingSelector struct {
	*scriptedSelector
	tabbedSteps []richStep

	selectCalls int
	selectItems [][]SelectItem
	selectOpts  []TabbedSelectOptions

	multiCalls int
	multiItems [][]SelectItem
	multiOpts  []TabbedSelectOptions
	multiIdx   []int
	multiOK    bool
}

func (s *tabbedRecordingSelector) SelectRichTabbed(_ string, items []SelectItem, opts TabbedSelectOptions) (int, bool, error) {
	s.selectCalls++
	s.selectItems = append(s.selectItems, items)
	s.selectOpts = append(s.selectOpts, opts)
	if len(s.tabbedSteps) == 0 {
		return -1, false, nil
	}
	step := s.tabbedSteps[0]
	s.tabbedSteps = s.tabbedSteps[1:]
	if step.label != "" {
		for i, item := range items {
			if item.Label == step.label {
				return i, step.ok, step.err
			}
		}
		return -1, false, nil
	}
	return step.idx, step.ok, step.err
}

func (s *tabbedRecordingSelector) MultiSelectRichTabbed(_ string, items []SelectItem, opts TabbedSelectOptions) ([]int, bool, error) {
	s.multiCalls++
	s.multiItems = append(s.multiItems, items)
	s.multiOpts = append(s.multiOpts, opts)
	return s.multiIdx, s.multiOK, nil
}

// The menu opens as a tabbed picker: skills in their layers' tabs, the
// actions in an uncounted Manage tab, and every skill row's label exactly the
// skill's name.
func TestSkillsMenuUsesTabsWithManageLast(t *testing.T) {
	session := skillsTestSession()
	selector := &tabbedRecordingSelector{scriptedSelector: &scriptedSelector{}}
	var out bytes.Buffer
	ctrl := newCommandController(session, NewRenderer(&out, &out), selector, nil, "")
	if _, submitted := ctrl.handleSkills(context.Background()); submitted {
		t.Fatalf("cancelling the picker must not submit a turn")
	}
	if selector.selectCalls != 1 {
		t.Fatalf("expected one tabbed open, got %d", selector.selectCalls)
	}
	opts := selector.selectOpts[0]
	if len(opts.Uncounted) != 1 || opts.Uncounted[0] != "Manage" {
		t.Fatalf("uncounted tabs = %v, want [Manage]", opts.Uncounted)
	}
	if opts.DefaultIdx != 0 {
		t.Fatalf("default row = %d, want the first", opts.DefaultIdx)
	}
	items := selector.selectItems[0]
	if items[0].Label != "repo" {
		t.Fatalf("first row = %q, want repo", items[0].Label)
	}
	skills := map[string]bool{"repo": false, "skill-workshop": false}
	for _, item := range items {
		if _, isSkill := skills[item.Label]; isSkill {
			skills[item.Label] = true
		}
	}
	for name, found := range skills {
		if !found {
			t.Fatalf("skill %q absent or its label carries more than the name: %v", name, items)
		}
	}
}

// A cancelled sub-flow returns to the menu on the row the user left, so
// browsing skills never resets to the first tab's first row.
func TestSkillsMenuReopensOnTheRowItLeft(t *testing.T) {
	session := skillsTestSession()
	selector := &tabbedRecordingSelector{
		scriptedSelector: &scriptedSelector{},
		tabbedSteps: []richStep{
			{idx: 0, ok: true}, // repo's detail page
			{label: "Add a skill", ok: true},
		},
	}
	var out bytes.Buffer
	ctrl := newCommandController(session, NewRenderer(&out, &out), selector, nil, "")
	if _, submitted := ctrl.handleSkills(context.Background()); submitted {
		t.Fatalf("walking out of the menu must not submit a turn")
	}
	if selector.selectCalls != 3 {
		t.Fatalf("expected three tabbed opens, got %d", selector.selectCalls)
	}
	if got := selector.selectOpts[1].DefaultIdx; got != 0 {
		t.Fatalf("reopen after repo defaulted to %d, want 0", got)
	}
	addIdx := -1
	for i, item := range selector.selectItems[0] {
		if item.Label == "Add a skill" {
			addIdx = i
			break
		}
	}
	if addIdx < 0 {
		t.Fatalf("no Add a skill row: %v", selector.selectItems[0])
	}
	if got := selector.selectOpts[2].DefaultIdx; got != addIdx {
		t.Fatalf("reopen after Add a skill defaulted to %d, want %d", got, addIdx)
	}
}

// The toggle is a tabbed multi-select whose checks are item indices, and the
// confirmed set goes to the store as paths.
func TestSkillsToggleUsesTabbedMultiSelect(t *testing.T) {
	session := skillsTestSession()
	selector := &tabbedRecordingSelector{
		scriptedSelector: &scriptedSelector{},
		tabbedSteps:      []richStep{{label: "Enable / disable skills", ok: true}},
		multiIdx:         []int{0},
		multiOK:          true,
	}
	var out bytes.Buffer
	ctrl := newCommandController(session, NewRenderer(&out, &out), selector, nil, "")
	ctrl.handleSkills(context.Background())

	if selector.multiCalls != 1 {
		t.Fatalf("expected one tabbed multi-select, got %d", selector.multiCalls)
	}
	require.Equal(t, []int{0, 1}, selector.multiOpts[0].Checked, "checked rows must be every enabled skill")
	require.Equal(t, []string{"/skills/repo"}, session.appliedEnabledPaths)
}

// Without the tabbed capability the toggle falls back to a plain MultiSelect
// whose options name each row's tab, so no two rows share an option.
func TestSkillsToggleFallbackShowsUniqueLabels(t *testing.T) {
	session := skillsTestSession()
	selector := &scriptedSelector{
		richSteps: []richStep{{label: "Enable / disable skills", ok: true}},
		multiOK:   false,
	}
	var out bytes.Buffer
	ctrl := newCommandController(session, NewRenderer(&out, &out), selector, nil, "")
	ctrl.handleSkills(context.Background())
	require.Equal(t, []string{"repo (Project)", "skill-workshop (Built-in)"}, selector.multiOptions)
}

// The shadowing note names the winning layer, so a row says which copy the
// user is really looking at.
func TestSkillRowDescriptionNamesTheShadowingLayer(t *testing.T) {
	project := skill.Entry{
		Name:        "improve",
		Description: "Work on skills",
		Enabled:     true,
		Origin:      skill.OriginProject,
		Path:        "/proj/.forebrain/skills/improve",
		Shadows:     []string{"/home/u/.claude/skills/improve"},
	}
	cross := skill.Entry{
		Name:       "improve",
		Enabled:    false,
		Origin:     skill.OriginCrossTool,
		Path:       "/home/u/.claude/skills/improve",
		ShadowedBy: []string{"/proj/.forebrain/skills/improve"},
	}
	origins := skillOriginsByPath([]skill.Entry{project, cross})
	if got := skillRowDescription(project, origins, true); !strings.HasSuffix(got, "· shadows the Cross-tool copy") {
		t.Fatalf("winning copy description = %q", got)
	}
	got := skillRowDescription(cross, origins, true)
	if !strings.HasPrefix(got, "(off) ") || !strings.HasSuffix(got, "· shadowed by the Project copy") {
		t.Fatalf("shadowed copy description = %q", got)
	}
	if plain := skillRowDescription(cross, origins, false); strings.Contains(plain, "(off)") {
		t.Fatalf("toggle row carries the state twice: %q", plain)
	}
}

// The import picker's rows split the decision across two columns: the name,
// and everything else in the description; blocked proposals are explained,
// not offered.
func TestMemoryImportRowsUseTwoColumns(t *testing.T) {
	rows, names, blocked := memoryImportRows([]MemorySkillOption{
		{Name: "release-check", Description: "Verify a release", Status: "new", SlashCommand: "/release-check"},
		{Name: "compact", Description: "Squeeze things", Blocked: "name collides with the built-in /compact command"},
	})
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Label != "release-check" || rows[0].Category != "Memory" {
		t.Fatalf("row = %+v", rows[0])
	}
	if rows[0].Description != "Verify a release · new · adds /release-check" {
		t.Fatalf("description = %q", rows[0].Description)
	}
	require.Equal(t, []string{"release-check"}, names)
	require.Equal(t, []string{"compact (name collides with the built-in /compact command)"}, blocked)
}

// A slash command's picker is shown in the terminal and a pick goes back to
// the engine; a cancelled one applies nothing and prints nothing.
func TestSlashPickerCancelAppliesNothing(t *testing.T) {
	session := &fakeSession{}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	ctrl := newCommandController(session, renderer, &stubSelector{selectRichOK: false}, nil, "")
	picker := &turn.Picker{Command: "model", Title: "Model", Items: []turn.PickerItem{{Value: "a", Label: "a", Current: true}, {Value: "b", Label: "b"}}}
	handled, _, _, _ := runSlashPicker(context.Background(), ctrl, renderer, nil, &streamState{sessionID: "s1"}, picker)
	if !handled || len(session.choices) != 0 || len(renderer.vm.blocks) != 0 {
		t.Fatalf("cancel: handled=%v choices=%v blocks=%d", handled, session.choices, len(renderer.vm.blocks))
	}
	if !ctrl.handlePermissions("s1") {
		t.Fatalf("expected permissions command handled on cancel")
	}
	if len(session.appliedPresets) != 0 {
		t.Fatalf("expected no preset applied on cancel, got %#v", session.appliedPresets)
	}
}

// A slash command's picker — /model, its reasoning effort, /agent — opens on
// the item in force.
func TestSlashPickerOpensOnTheItemInForce(t *testing.T) {
	session := &fakeSession{}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	selector := &stubSelector{selectRichOK: false}
	ctrl := newCommandController(session, renderer, selector, nil, "")
	picker := &turn.Picker{Command: "model-effort", Title: "Reasoning effort", Items: []turn.PickerItem{
		{Value: "low", Label: "low"}, {Value: "medium", Label: "medium"}, {Value: "high", Label: "high", Current: true},
	}}
	runSlashPicker(context.Background(), ctrl, renderer, nil, &streamState{sessionID: "s1"}, picker)
	if got := selector.selectRichDefaultIdx; got != 2 {
		t.Fatalf("picker opened on %d, want the item in force (2)", got)
	}
	for _, item := range selector.selectRichItems {
		if strings.Contains(item.Label, "current") {
			t.Fatalf("an item is labelled current: %q", item.Label)
		}
	}
}

// A pick is applied through the engine, its reply is said, and a picker the
// choice offers in turn is shown next.
func TestSlashPickerFollowsTheNextPicker(t *testing.T) {
	session := &fakeSession{chooseFn: func(choice turn.SlashChoice) SlashOutcome {
		if choice.Command == "model" {
			return SlashOutcome{Handled: true, Reply: "Switched to b.", Picker: &turn.Picker{Command: "model-effort", Title: "Reasoning effort", Items: []turn.PickerItem{{Value: "b high", Label: "high"}}}}
		}
		return SlashOutcome{Handled: true, Reply: "Reasoning effort set to high."}
	}}
	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.composerSuppressed = true
	selector := &stubSelector{selectRichIdx: 0, selectRichOK: true}
	ctrl := newCommandController(session, renderer, selector, nil, "")
	picker := &turn.Picker{Command: "model", Title: "Model", Items: []turn.PickerItem{{Value: "b", Label: "b"}}}
	runSlashPicker(context.Background(), ctrl, renderer, nil, &streamState{sessionID: "s1"}, picker)
	if len(session.choices) != 2 || session.choices[0] != (turn.SlashChoice{Command: "model", Value: "b"}) || session.choices[1] != (turn.SlashChoice{Command: "model-effort", Value: "b high"}) {
		t.Fatalf("choices = %+v", session.choices)
	}
	var replies []string
	for _, block := range renderer.vm.blocks {
		replies = append(replies, block.frame.Content)
	}
	if strings.Join(replies, "|") != "Switched to b.|Reasoning effort set to high." {
		t.Fatalf("replies = %q", replies)
	}
}

type selectorStep struct {
	value string
	ok    bool
	err   error
}

type selectorInput struct {
	value string
	ok    bool
	err   error
}

type sequenceSelector struct {
	steps []selectorStep
	idx   int
}

func (s *sequenceSelector) Select(string, []string, string) (string, bool, error) {
	if s.idx >= len(s.steps) {
		return "", false, nil
	}
	step := s.steps[s.idx]
	s.idx++
	return step.value, step.ok, step.err
}

func (s *sequenceSelector) MultiSelect(string, []string, []string) ([]string, bool, error) {
	return nil, false, nil
}

func (s *sequenceSelector) Input(string, string) (string, bool, error) { return "", false, nil }
func (s *sequenceSelector) Confirm(string, bool) (bool, bool, error)   { return false, false, nil }
func (s *sequenceSelector) SelectRich(label string, items []SelectItem, _ int) (int, bool, error) {
	labels := make([]string, len(items))
	for i, item := range items {
		labels[i] = item.Label
	}
	chosen, ok, err := s.Select(label, labels, "")
	if err != nil || !ok {
		return -1, ok, err
	}
	for i, l := range labels {
		if l == chosen {
			return i, true, nil
		}
	}
	return -1, false, nil
}

func (s *sequenceSelector) Secret(label string, defaultValue string) (string, bool, error) {
	return s.Input(label, defaultValue)
}

func (s *sequenceSelector) Review(string, []turn.StatusFact, []string, int) (int, bool, error) {
	return -1, false, nil
}

type dualInputSequenceSelector struct {
	selectSteps []selectorStep
	selectIdx   int
	inputs      []selectorInput
	inputIdx    int
}

func (s *dualInputSequenceSelector) Select(string, []string, string) (string, bool, error) {
	if s.selectIdx >= len(s.selectSteps) {
		return "", false, nil
	}
	step := s.selectSteps[s.selectIdx]
	s.selectIdx++
	return step.value, step.ok, step.err
}

func (s *dualInputSequenceSelector) MultiSelect(string, []string, []string) ([]string, bool, error) {
	return nil, false, nil
}

func (s *dualInputSequenceSelector) Input(string, string) (string, bool, error) {
	if s.inputIdx >= len(s.inputs) {
		return "", false, nil
	}
	step := s.inputs[s.inputIdx]
	s.inputIdx++
	return step.value, step.ok, step.err
}

func (s *dualInputSequenceSelector) Confirm(string, bool) (bool, bool, error) {
	return false, false, nil
}

func (s *dualInputSequenceSelector) SelectRich(string, []SelectItem, int) (int, bool, error) {
	return -1, false, nil
}

func (s *dualInputSequenceSelector) Secret(label string, defaultValue string) (string, bool, error) {
	return s.Input(label, defaultValue)
}

func (s *dualInputSequenceSelector) Review(string, []turn.StatusFact, []string, int) (int, bool, error) {
	return -1, false, nil
}

func TestCommandControllerHandleResumeReturnsSelection(t *testing.T) {
	session := &fakeSession{
		recent: []SessionSummary{
			{ID: "s2", Title: "two", UpdatedAt: time.Now().Unix() - 300},
		},
	}
	var out bytes.Buffer
	ctrl := newCommandController(session, NewRenderer(&out, &out), &stubSelector{
		selectRichIdx: 0,
		selectRichOK:  true,
	}, func(context.Context) (string, error) { return "", nil }, "")
	got, ok := ctrl.handleResume(context.Background(), "")
	if !ok || got != "s2" {
		t.Fatalf("unexpected resume selection ok=%v id=%q", ok, got)
	}
}

func TestCommandControllerHandlePermissionsAppliesSelectedPreset(t *testing.T) {
	session := &fakeSession{activePreset: safety.PresetReadOnly}
	var out bytes.Buffer
	// Index 2 is Full Access; the picker opens on Read Only because that is the
	// preset the session reports running under.
	selector := &stubSelector{selectRichIdx: 2, selectRichOK: true}
	ctrl := newCommandController(session, NewRenderer(&out, &out), selector, nil, "")

	if !ctrl.handlePermissions("s1") {
		t.Fatalf("expected permissions command handled")
	}
	if got := selector.selectRichDefaultIdx; got != 0 || selector.selectRichItems[0].Label != "Read Only" {
		t.Fatalf("expected the picker to open on the preset in force, got default idx %d", got)
	}
	if len(session.appliedPresets) != 1 || session.appliedPresets[0] != safety.PresetFullAccess {
		t.Fatalf("unexpected applied presets: %#v", session.appliedPresets)
	}
	if session.activePreset != safety.PresetFullAccess {
		t.Fatalf("expected the session to report the new preset, got %q", session.activePreset)
	}
}

func TestCommandControllerHandlePermissionsShowsEveryPreset(t *testing.T) {
	session := &fakeSession{}
	var out bytes.Buffer
	selector := &stubSelector{}
	ctrl := newCommandController(session, NewRenderer(&out, &out), selector, nil, "")

	if !ctrl.handlePermissions("s1") {
		t.Fatalf("expected permissions command handled")
	}
	want := []string{"Read Only", "Default", "Full Access"}
	if len(selector.selectRichItems) != len(want) {
		t.Fatalf("unexpected picker items: %#v", selector.selectRichItems)
	}
	for i, label := range want {
		if selector.selectRichItems[i].Label != label {
			t.Fatalf("item %d = %q, want %q", i, selector.selectRichItems[i].Label, label)
		}
		if strings.TrimSpace(selector.selectRichItems[i].Description) == "" {
			t.Fatalf("preset %q has no description", label)
		}
	}
}

func TestCommandControllerHandlePermissionsCancelAppliesNothing(t *testing.T) {
	session := &fakeSession{}
	var out bytes.Buffer
	ctrl := newCommandController(session, NewRenderer(&out, &out), &stubSelector{selectRichOK: false}, nil, "")
	if !ctrl.handlePermissions("s1") {
		t.Fatalf("expected permissions command handled on cancel")
	}
	if len(session.appliedPresets) != 0 {
		t.Fatalf("expected no preset applied on cancel, got %#v", session.appliedPresets)
	}
}

func TestMsgKindFromRoleMapping(t *testing.T) {
	cases := []struct {
		role string
		want string
	}{
		{"user", "user"},
		{"USER", "user"},
		{"assistant", "assistant"},
		{"reasoning", "reasoning"},
		{" Tool ", "tool"},
		{"system", "system"},
		{"plan", "plan"},
		{"error", "error"},
		{"", "system"},
		{"unknown", "system"},
	}
	for _, tc := range cases {
		got := string(msgKindFromRole(tc.role))
		if got != tc.want {
			t.Fatalf("msgKindFromRole(%q) = %q, want %q", tc.role, got, tc.want)
		}
	}
}

// Phase 3a redesign: replayTurnsToRenderer feeds every Turn through a fresh
// Reducer and emits structured frames (assistant + user + tool) — not a single
// FrameSystem card. Empty content turns are skipped.
func TestPrintSessionResumeContextEmptyDoesNothing(t *testing.T) {
	session := &fakeSession{}
	var out bytes.Buffer
	ctrl := newCommandController(session, NewRenderer(&out, &out), &stubSelector{}, nil, "")
	ctrl.printSessionResumeContext("s1")
	got := stripANSI(out.String())
	if strings.TrimSpace(got) != "" {
		t.Fatalf("expected no output for empty transcript, got %q", got)
	}
}

func TestPrintSessionResumeContextReplaysEventOnlySession(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	session := &fakeSession{sessionEvents: []event.RunEvent{
		event.NewRunEvent("spawn-only", "child-only", "s1", event.RunEventSubagentSpawned, event.SubagentSpawnedPayload{
			AgentID: "agent-only", AgentType: "explore", Task: "recover event-only child", ExecutionID: "exec-only",
		}, now),
	}}
	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.composerSuppressed = true
	ctrl := newCommandController(session, renderer, &stubSelector{}, nil, "")
	ctrl.printSessionResumeContext("s1")

	if renderer.perAgentVM["agent-only"] == nil {
		t.Fatal("event-only resume did not restore the child VM")
	}
	if len(renderer.vm.blocks) == 0 || renderer.vm.blocks[0].frame.Kind != FrameFanout {
		t.Fatalf("event-only resume did not restore its primary card: %#v", renderer.vm.blocks)
	}
	if got := renderer.vm.blocks[0].frame.FanoutLineAgents[0]; got != "agent-only" {
		t.Fatalf("event-only card row opens %q, want agent-only", got)
	}
}

func TestPrintSessionResumeContextRestoresSavedSubagentView(t *testing.T) {
	session := &fakeSession{
		browseFound: true,
		browseState: RendererBrowseState{
			ActiveView: "agent-only",
			Views: map[string]RendererViewBrowseState{
				"":           {Follow: true},
				"agent-only": {Follow: false, ScrollOffset: 2},
			},
		},
		sessionEvents: []event.RunEvent{
			event.NewRunEvent("spawn-only", "child-only", "s1", event.RunEventSubagentSpawned, event.SubagentSpawnedPayload{
				AgentID: "agent-only", AgentType: "explore", Task: "recover child", ExecutionID: "exec-only",
			}, time.Unix(100, 0).UTC()),
			event.NewRunEvent("answer-only", "child-only", "s1", event.RunEventAssistantDelta, event.AssistantDeltaPayload{
				AgentID: "agent-only", Text: "historical answer",
			}, time.Unix(101, 0).UTC()),
		},
	}
	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.composerSuppressed = true
	ctrl := newCommandController(session, renderer, &stubSelector{}, nil, "")
	ctrl.printSessionResumeContext("s1")

	if renderer.ActiveView() != "agent-only" {
		t.Fatalf("active view = %q, want restored agent-only", renderer.ActiveView())
	}
	if renderer.perAgentVM["agent-only"] == nil {
		t.Fatal("saved subagent view was restored without its historical VM")
	}
}

func TestPrintSessionResumeContextInterleavesPrimaryAndSubagentHistory(t *testing.T) {
	session := &fakeSession{
		transcriptTurns: []state.Message{
			{RowID: 1, Role: "user", Content: "parent prompt", CreatedAt: 100},
			{RowID: 2, Role: "assistant", Content: "parent answer", CreatedAt: 102},
		},
		sessionEvents: []event.RunEvent{
			event.NewRunEvent("spawn-between", "child-1", "s1", event.RunEventSubagentSpawned, event.SubagentSpawnedPayload{
				AgentID: "agent-between", AgentType: "explore", Task: "child prompt", ExecutionID: "exec-between",
			}, time.Unix(101, 0).UTC()),
		},
	}
	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.composerSuppressed = true
	ctrl := newCommandController(session, renderer, &stubSelector{}, nil, "")
	ctrl.printSessionResumeContext("s1")

	userIndex, cardIndex, answerIndex := -1, -1, -1
	for i, block := range renderer.vm.blocks {
		switch {
		case block.frame.Kind == FrameUser && block.frame.Content == "parent prompt":
			userIndex = i
		case block.frame.Kind == FrameFanout:
			cardIndex = i
		case block.frame.Kind == FrameAssistant && block.frame.Content == "parent answer":
			answerIndex = i
		}
	}
	if !(userIndex >= 0 && userIndex < cardIndex && cardIndex < answerIndex) {
		t.Fatalf("resume order user=%d child-card=%d answer=%d, blocks=%#v", userIndex, cardIndex, answerIndex, renderer.vm.blocks)
	}
}

func TestPrintSessionResumeContextUsesActiveContextForFooterWhileReplayingFullHistory(t *testing.T) {
	fullUsage := `{"input_tokens":500000,"output_tokens":100}`
	session := &fakeSession{
		model: "deepseek/deepseek-v4-flash",
		transcriptTurns: []state.Message{
			{Role: "user", Content: "old question"},
			{Role: "assistant", Content: "old answer", UsageJSON: fullUsage},
		},
		// Non-nil empty slice means the active model context was cleared even
		// though the full transcript remains available for visual replay.
		activeContextTurns: []state.Message{},
	}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	ctrl := newCommandController(session, renderer, &stubSelector{}, nil, "")
	ctrl.printSessionResumeContext("s1")

	// Full visual replay is independently covered by replayTurnsToRenderer tests;
	// here the fake's full transcript (non-empty) exercises that path while the
	// footer assertion proves it is NOT used for budget accounting.
	stats := renderer.ComposerTokenStats("")
	if !stats.Active || stats.PercentLeft != 100 || stats.ContextWindow <= 0 {
		t.Fatalf("resume footer used full history instead of cleared active context: %+v", stats)
	}
}

func TestReplayTurnsToRendererUsesPersistedToolTimingAndStructuredMetadata(t *testing.T) {
	toolMetaJSON, err := json.Marshal(tool.ToolMeta{
		ToolName:   "shell",
		Status:     "completed",
		Invocation: `printf TODO`,
		Input: map[string]any{
			"command": "printf TODO",
		},
	})
	if err != nil {
		t.Fatalf("marshal tool meta: %v", err)
	}
	assistantParts := state.MessagePartsJSON(llm.AssistantMessage(nil, llm.ToolCall{
		ID:   "call-shell-1",
		Type: llm.ToolTypeFunction,
		Function: llm.FunctionCall{
			Name:      "shell",
			Arguments: `{"command":"printf TODO"}`,
		},
	}), "")
	toolBody := `{"stdout":"TODO","exit_code":0}`
	toolParts := state.MessagePartsJSON(llm.ToolResultMessage("call-shell-1", llm.Text(toolBody)), toolBody)

	renderer := NewRenderer(nil, nil)
	renderer.EnableViewportMode()
	t.Cleanup(renderer.DisableViewportMode)

	replayTurnsToRenderer(renderer, []state.Message{
		{Role: "assistant", PartsJSON: assistantParts},
		{
			Role:             "tool",
			Content:          toolBody,
			PartsJSON:        toolParts,
			ToolMetaJSON:     string(toolMetaJSON),
			ExecStartedAtMs:  1780308000000,
			ExecFinishedAtMs: 1780308001500,
			ExecDurationMs:   1500,
		},
	})

	if len(renderer.vm.blocks) != 1 {
		t.Fatalf("blocks=%d want 1", len(renderer.vm.blocks))
	}
	frame := renderer.vm.blocks[0].frame
	if frame.Kind != FrameTool {
		t.Fatalf("frame kind=%s want tool", frame.Kind)
	}
	header := ToolDisplayHeader(frame, "")
	if !strings.Contains(header, `Ran printf TODO`) {
		t.Fatalf("header=%q", header)
	}
	if strings.Contains(header, "1 line") {
		t.Fatalf("header leaked result-derived detail: %q", header)
	}
	if !strings.Contains(header, "1.5s") {
		t.Fatalf("header missing persisted duration: %q", header)
	}
	if frame.Duration != 1500*time.Millisecond {
		t.Fatalf("duration=%s want 1.5s", frame.Duration)
	}
	if frame.Content == toolBody || strings.Contains(frame.Content, `{"stdout"`) {
		t.Fatalf("replay exposed raw shell result JSON: %q", frame.Content)
	}
	wantBody, _ := tool.FormatToolStepResult(tool.StepEvent{
		Kind:     tool.StepKindToolCompleted,
		ToolName: "shell",
		Input:    map[string]any{"command": "printf TODO"},
		Output:   map[string]any{"stdout": "TODO", "exit_code": float64(0)},
	}, tool.DefaultMaxFormattedBody)
	if frame.Content != strings.TrimSpace(wantBody) {
		t.Fatalf("replay body differs from live formatter\ngot:  %q\nwant: %q", frame.Content, strings.TrimSpace(wantBody))
	}
	painted := stripANSI(strings.Join(renderViewport(&renderer.vm, 120, 20, 0, DiffThemeDark).lines, "\n"))
	if !strings.Contains(painted, "TODO") || strings.Contains(painted, `{"stdout"`) {
		t.Fatalf("replayed shell viewport did not match live UX:\n%s", painted)
	}
}

// A denied approval's stored row carries the display half of its refusal: the
// model-facing instruction text stays in the row's content, and the replayed
// card shows the sentence the live card showed — never the instruction text,
// and never a review the refusal once carried.
func TestReplayDeniedToolRowShowsItsDisplayHalf(t *testing.T) {
	assistantParts := state.MessagePartsJSON(llm.AssistantMessage(nil, llm.ToolCall{
		ID:   "call-exit-1",
		Type: llm.ToolTypeFunction,
		Function: llm.FunctionCall{
			Name:      "exit_plan_mode",
			Arguments: `{}`,
		},
	}), "")
	denialText := "Tool approval denied by user: the user rejected this exit_plan_mode tool call. Try a different approach or ask the user for guidance.\n\nUser feedback: 改成先写测试"
	denial := llm.ToolResultMessage("call-exit-1", llm.Text(denialText))
	denial.ToolDisplay = &llm.ToolDisplayState{
		Body:         "改成先写测试",
		ToolMetaJSON: `{"tool_name":"exit_plan_mode","status":"denied"}`,
	}
	toolParts := state.MessagePartsJSON(denial, denialText)

	renderer := NewRenderer(nil, nil)
	renderer.EnableViewportMode()
	t.Cleanup(renderer.DisableViewportMode)

	replayTurnsToRenderer(renderer, []state.Message{
		{Role: "assistant", PartsJSON: assistantParts},
		{Role: "tool", Content: denialText, PartsJSON: toolParts},
	})

	if len(renderer.vm.blocks) != 1 {
		t.Fatalf("blocks=%d want 1", len(renderer.vm.blocks))
	}
	frame := renderer.vm.blocks[0].frame
	if frame.Kind != FrameTool {
		t.Fatalf("frame kind=%s want tool", frame.Kind)
	}
	if frame.Content != "改成先写测试" {
		t.Fatalf("replayed denial body = %q, want the user's words", frame.Content)
	}
	if strings.Contains(frame.Content, "Tool approval denied by user") || strings.Contains(frame.Content, "<review") {
		t.Fatalf("replayed denial body leaked model-facing text: %q", frame.Content)
	}
	if got := frame.ToolMeta.Status; got != "denied" {
		t.Fatalf("replayed denial status = %q, want denied", got)
	}
}

// A heartbeat's prompt is not the person's words: replay draws it as what it
// is — a system card titled with its origin — while the person's own rows
// keep replaying as theirs.
func TestReplayDrawsAHeartbeatPromptAsItsOriginCard(t *testing.T) {
	renderer := NewRenderer(nil, nil)
	renderer.EnableViewportMode()
	t.Cleanup(renderer.DisableViewportMode)

	beats := state.WithMessageOrigin(state.MessagePartsJSON(llm.UserMessage(llm.Text("anything new?")), "anything new?"), state.MessageOriginHeartbeat)
	typed := state.MessagePartsJSON(llm.UserMessage(llm.Text("typed by hand")), "typed by hand")

	replayTurnsToRenderer(renderer, []state.Message{
		{Role: "user", Content: "anything new?", PartsJSON: beats},
		{Role: "user", Content: "typed by hand", PartsJSON: typed},
	})

	if len(renderer.vm.blocks) != 2 {
		t.Fatalf("blocks=%d want 2", len(renderer.vm.blocks))
	}
	beat := renderer.vm.blocks[0].frame
	if beat.Kind != FrameSystem {
		t.Fatalf("heartbeat frame kind=%s want system", beat.Kind)
	}
	if beat.Title != "heartbeat" {
		t.Fatalf("heartbeat frame title=%q want heartbeat", beat.Title)
	}
	if beat.Content != "anything new?" {
		t.Fatalf("heartbeat frame content=%q", beat.Content)
	}
	own := renderer.vm.blocks[1].frame
	if own.Kind != FrameUser {
		t.Fatalf("the person's own row kind=%s want user", own.Kind)
	}
	if own.Content != "typed by hand" {
		t.Fatalf("the person's own row content=%q", own.Content)
	}
}

// A scheduled task's prompt is the heartbeat's case again with its own
// origin: replay draws it as a system card titled cron, so a fire's
// conversation reads as what it is — the task talking, not the person.
func TestReplayDrawsACronPromptAsItsOriginCard(t *testing.T) {
	renderer := NewRenderer(nil, nil)
	renderer.EnableViewportMode()
	t.Cleanup(renderer.DisableViewportMode)

	fired := state.WithMessageOrigin(state.MessagePartsJSON(llm.UserMessage(llm.Text("nightly summary")), "nightly summary"), state.MessageOriginCron)

	replayTurnsToRenderer(renderer, []state.Message{
		{Role: "user", Content: "nightly summary", PartsJSON: fired},
	})

	if len(renderer.vm.blocks) != 1 {
		t.Fatalf("blocks=%d want 1", len(renderer.vm.blocks))
	}
	card := renderer.vm.blocks[0].frame
	if card.Kind != FrameSystem {
		t.Fatalf("cron frame kind=%s want system", card.Kind)
	}
	if card.Title != "cron" {
		t.Fatalf("cron frame title=%q want cron", card.Title)
	}
	if card.Content != "nightly summary" {
		t.Fatalf("cron frame content=%q", card.Content)
	}
}

// A subagent's read_file card is rebuilt from the durable event stream, so
// the engine's paging facts must ride the canonical ToolMeta. The call below
// passed no offset/limit — without the facts there is nothing to derive a
// range from and the replayed header lost its "lines 1-200".
func TestReplaySubagentReadFileHeaderKeepsEnginePagingFacts(t *testing.T) {
	agentID := "task-replay-read"
	events := []event.RunEvent{
		event.NewRunEvent("read-done", "child-1", "session-1", event.RunEventToolCompleted, event.ToolCallCompletedPayload{
			Kind: event.RunEventToolCompleted, StepID: "read-1", ToolName: "read_file", DisplayBody: "source body",
			ToolMeta: event.ToolCallMeta{
				ToolName: "read_file", Status: "completed", AgentID: agentID,
				Input:        map[string]any{"file_path": "pkg/tui/render.go"},
				ResultLines:  200,
				ResultOffset: 0,
			},
		}, time.Unix(100, 0)),
	}

	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.composerSuppressed = true
	if got := replaySubagentRunEvents(renderer, events); got != 1 {
		t.Fatalf("replayed events=%d want 1", got)
	}
	vm := renderer.perAgentVM[agentID]
	if vm == nil {
		t.Fatal("resume did not restore the subagent VM")
	}
	for _, block := range vm.blocks {
		if block.frame.Kind != FrameTool {
			continue
		}
		header := ToolDisplayHeader(block.frame, "")
		if !strings.Contains(header, "lines 1-200") {
			t.Fatalf("replayed read_file header lost the returned page: %q", header)
		}
		return
	}
	t.Fatal("replayed subagent view has no tool frame")
}

func TestReplaySubagentEventsRestoreClickableCompleteAgentView(t *testing.T) {
	now := time.Unix(100, 0)
	agentID := "task-replay-1"
	events := []event.RunEvent{
		event.NewRunEvent("spawn", "child-1", "session-1", event.RunEventSubagentSpawned, event.SubagentSpawnedPayload{
			AgentID: agentID, AgentType: "explore", TaskID: agentID, Task: "inspect approval history",
			ParentRunID: "parent-1", ParentToolCallID: "fanout-1", TaskIndex: 0, ExecutionID: "exec-1",
		}, now),
		event.NewRunEvent("reason", "child-1", "session-1", event.RunEventReasoningDelta, event.ReasoningDeltaPayload{AgentID: agentID, Text: "checking the runtime"}, now.Add(time.Second)),
		event.NewRunEvent("reason-done", "child-1", "session-1", event.RunEventReasoningDone, event.ReasoningDonePayload{AgentID: agentID}, now.Add(2*time.Second)),
		event.NewRunEvent("tool-done", "child-1", "session-1", event.RunEventToolCompleted, event.ToolCallCompletedPayload{
			Kind: event.RunEventToolCompleted, StepID: "read-1", ToolName: "read_file", DisplayBody: "source body",
			ToolMeta: event.ToolCallMeta{ToolName: "read_file", Status: "completed", AgentID: agentID},
		}, now.Add(3*time.Second)),
		event.NewRunEvent("approval-request", "child-1", "session-1", event.RunEventApprovalReq, event.ApprovalRequestedPayload{
			ActionID: "action-1", ActionKind: "shell", AgentID: agentID, Message: "run tests",
		}, now.Add(4*time.Second)),
		event.NewRunEvent("approval-resolved", "child-1", "session-1", event.RunEventApprovalResolved, event.ApprovalResolvedPayload{
			ActionID: "action-1", ActionKind: "shell", AgentID: agentID, Decision: "approved",
		}, now.Add(5*time.Second)),
		event.NewRunEvent("answer", "child-1", "session-1", event.RunEventAssistantDelta, event.AssistantDeltaPayload{AgentID: agentID, Text: "all checks passed"}, now.Add(6*time.Second)),
		event.NewRunEvent("ended", "child-1", "session-1", event.RunEventSubagentEnded, event.SubagentEndedPayload{
			AgentID: agentID, AgentType: "explore", TaskID: agentID, Status: "ok",
			ParentRunID: "parent-1", ParentToolCallID: "fanout-1", TaskIndex: 0, ExecutionID: "exec-1",
		}, now.Add(7*time.Second)),
	}

	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.composerSuppressed = true
	if got := replaySubagentRunEvents(renderer, events); got != len(events) {
		t.Fatalf("replayed events=%d want %d", got, len(events))
	}
	vm := renderer.perAgentVM[agentID]
	if vm == nil {
		t.Fatal("resume did not restore the subagent VM")
	}
	var transcript strings.Builder
	for _, block := range vm.blocks {
		transcript.WriteString(block.frame.Title)
		transcript.WriteByte('\n')
		transcript.WriteString(block.frame.Summary)
		transcript.WriteByte('\n')
		transcript.WriteString(block.frame.Content)
		transcript.WriteByte('\n')
	}
	for _, want := range []string{"inspect approval history", "checking the runtime", "source body", "all checks passed"} {
		if !strings.Contains(transcript.String(), want) {
			t.Fatalf("restored subagent transcript missing %q:\n%s", want, transcript.String())
		}
	}
	// An approval is the gate on a call, not a call of its own. Replaying it as
	// a tool card painted a nameless "Ran" block that named neither the command
	// nor what was approved, beside the very call it was gating.
	for _, block := range vm.blocks {
		if strings.EqualFold(strings.TrimSpace(block.frame.ToolMeta.Category), "approval") {
			t.Fatalf("replayed subagent view painted an approval as a tool card: %#v", block.frame)
		}
	}
	foundThinking := false
	for _, block := range vm.blocks {
		if block.frame.Kind == FrameThinking && block.frame.Final {
			foundThinking = true
			if block.frame.Duration != time.Second {
				t.Fatalf("replayed thinking duration=%s want 1s", block.frame.Duration)
			}
		}
	}
	if !foundThinking {
		t.Fatal("replayed subagent view has no final thinking block")
	}
	renderer.vpBodyHeight = 30
	renderer.vpHeight = 30
	renderer.vpLastRender = renderViewport(&renderer.vm, 100, 30, 0, DiffThemeDark)
	// The card's task row is the way back into the agent's view.
	row := rowOfPrimaryText(t, renderer, "inspect approval history")
	if !renderer.ViewportClickToggle(0, row) || renderer.ActiveView() != agentID {
		t.Fatalf("replayed card row did not open %q; active=%q", agentID, renderer.ActiveView())
	}
}

// The note-write result carries the stored path; sessions recorded before the
// tool returned one carry an empty object. Both must replay as the note body.
func TestReplayMemoryNoteUsesIndexedInputAndLiveFormatter(t *testing.T) {
	for _, body := range []string{`{}`, `{"path":"extensions/ad_hoc/notes/2026-08-16T19-12-00-user-profile.md"}`} {
		t.Run(body, func(t *testing.T) { replayMemoryNoteCase(t, body) })
	}
}

func replayMemoryNoteCase(t *testing.T, body string) {
	t.Helper()
	args := `{"filename":"user-profile.md","note":"## Durable preference\n\n- Prefer **concise** updates."}`
	assistantParts := state.MessagePartsJSON(llm.AssistantMessage(nil, llm.ToolCall{
		ID:   "call-memory-note-1",
		Type: llm.ToolTypeFunction,
		Function: llm.FunctionCall{
			Name:      "memories_add_ad_hoc_note",
			Arguments: args,
		},
	}), "")
	toolParts := state.MessagePartsJSON(llm.ToolResultMessage("call-memory-note-1", llm.Text(body)), body)

	renderer := NewRenderer(nil, nil)
	renderer.EnableViewportMode()
	t.Cleanup(renderer.DisableViewportMode)
	replayTurnsToRenderer(renderer, []state.Message{
		{Role: "assistant", PartsJSON: assistantParts},
		{Role: "tool", Content: body, PartsJSON: toolParts},
	})

	if len(renderer.vm.blocks) != 1 {
		t.Fatalf("blocks=%d want 1", len(renderer.vm.blocks))
	}
	frame := renderer.vm.blocks[0].frame
	if frame.Content != "## Durable preference\n\n- Prefer **concise** updates." {
		t.Fatalf("replay body=%q", frame.Content)
	}
	if frame.ToolMeta.Invocation != "save memory note user-profile.md" {
		t.Fatalf("replay invocation=%q", frame.ToolMeta.Invocation)
	}
	// The header names the note by its stored path, which is also the path the
	// note is read back by.
	header := ToolDisplayHeader(frame, "")
	if header != "Saved memory note extensions/ad_hoc/notes/user-profile.md" {
		t.Fatalf("replay header=%q", header)
	}
	painted := stripANSI(strings.Join(renderViewport(&renderer.vm, 100, 20, 0, DiffThemeDark).lines, "\n"))
	if !strings.Contains(painted, "Durable preference") || strings.Contains(painted, "## Durable preference") ||
		strings.Contains(painted, `{}`) || strings.Contains(painted, `{"path"`) || strings.Contains(painted, "unreadable") {
		t.Fatalf("replayed memory note did not match live UX:\n%s", painted)
	}
}

// A resumed thread stores the model-facing memory payload, so replay has to run
// it back through the live formatter — otherwise the card shows the raw result
// JSON where the live session showed a Markdown result.
func TestReplayMemorySearchUsesLiveFormatter(t *testing.T) {
	args := `{"queries":["deploy"],"context_lines":3,"cursor":"0","case_sensitive":false}`
	body := `{"queries":["deploy"],"matches":[{"path":"MEMORY.md","match_line_number":8,"content":"- deploy with staging first"}],"truncated":false}`
	assistantParts := state.MessagePartsJSON(llm.AssistantMessage(nil, llm.ToolCall{
		ID:   "call-memory-search-1",
		Type: llm.ToolTypeFunction,
		Function: llm.FunctionCall{
			Name:      "memories_search",
			Arguments: args,
		},
	}), "")
	toolParts := state.MessagePartsJSON(llm.ToolResultMessage("call-memory-search-1", llm.Text(body)), body)

	renderer := NewRenderer(nil, nil)
	renderer.EnableViewportMode()
	t.Cleanup(renderer.DisableViewportMode)
	replayTurnsToRenderer(renderer, []state.Message{
		{Role: "assistant", PartsJSON: assistantParts},
		{Role: "tool", Content: body, PartsJSON: toolParts},
	})

	if len(renderer.vm.blocks) != 1 {
		t.Fatalf("blocks=%d want 1", len(renderer.vm.blocks))
	}
	frame := renderer.vm.blocks[0].frame
	if strings.Contains(frame.Content, `"match_line_number"`) {
		t.Fatalf("replay body kept transport JSON: %q", frame.Content)
	}
	if header := ToolDisplayHeader(frame, ""); header != `Searched memories "deploy"` {
		t.Fatalf("replay header=%q", header)
	}
	painted := stripANSI(strings.Join(renderViewport(&renderer.vm, 100, 20, 0, DiffThemeDark).lines, "\n"))
	if !strings.Contains(painted, "deploy with staging first") || strings.Contains(painted, `"queries"`) {
		t.Fatalf("replayed memory search did not match live UX:\n%s", painted)
	}
}

func TestReplayToolDisplayBodyKeepsMalformedLegacyShellOutput(t *testing.T) {
	const body = "legacy shell output that is not JSON"
	if got := replayToolDisplayBody("shell", `{"command":"legacy"}`, body); got != body {
		t.Fatalf("legacy body=%q want %q", got, body)
	}
}

func TestReplayWebFetchUsesLiveFormatterInsteadOfRawTransportJSON(t *testing.T) {
	const body = `{"body":"# API reference\n\n- Query","status":200,"content_type":"text/html","final_url":"https://example.com/api"}`
	got := replayToolDisplayBody("web_fetch", `{"url":"https://example.com/api","prompt":"extract API docs"}`, body)
	if got != "# API reference\n\n- Query" {
		t.Fatalf("replayed web_fetch body = %q", got)
	}
	for _, forbidden := range []string{"output:", "```json", `"status"`, `"content_type"`, `"final_url"`} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("replayed web_fetch exposed transport field %q: %q", forbidden, got)
		}
	}
}

func TestReplayToolFilePathDerivesReadPaths(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
		body  string
		want  string
	}{
		{
			name:  "input path",
			input: map[string]any{"file_path": "a.go"},
			body:  "{}",
			want:  "a.go",
		},
		{
			name:  "repaired read names the file actually read",
			input: map[string]any{"file_path": "SKILL.md"},
			body:  `{"repaired_from":"SKILL.md","abs_path":"/skills/x/SKILL.md"}`,
			want:  "/skills/x/SKILL.md",
		},
		{
			name:  "no path yields empty",
			input: map[string]any{"command": "ls"},
			body:  `{"summary":"ok"}`,
			want:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := replayToolFilePath(tc.input, tc.body); got != tc.want {
				t.Fatalf("replayToolFilePath(%v, %q) = %q want %q", tc.input, tc.body, got, tc.want)
			}
		})
	}
}

func TestReplayRequestPermissionsUsesLiveFormatterInsteadOfRawJSON(t *testing.T) {
	args := `{"reason":"run Go tests","scope":"session","permissions":{"file_system":{"read":["/Users/demo/go"],"write":["/Users/demo/Library/Caches/go-build"]}}}`
	body := `{"permissions":{"file_system":{"read":["/Users/demo/go"],"write":["/Users/demo/Library/Caches/go-build"]}},"scope":"session"}`
	assistantParts := state.MessagePartsJSON(llm.AssistantMessage(nil, llm.ToolCall{
		ID:   "call-permissions-1",
		Type: llm.ToolTypeFunction,
		Function: llm.FunctionCall{
			Name:      "request_permissions",
			Arguments: args,
		},
	}), "")
	toolParts := state.MessagePartsJSON(llm.ToolResultMessage("call-permissions-1", llm.Text(body)), body)

	renderer := NewRenderer(nil, nil)
	renderer.EnableViewportMode()
	t.Cleanup(renderer.DisableViewportMode)
	replayTurnsToRenderer(renderer, []state.Message{
		{Role: "assistant", PartsJSON: assistantParts},
		{RowID: 2, Role: "tool", Content: body, PartsJSON: toolParts, ExecDurationMs: 50},
	})

	if len(renderer.vm.blocks) != 1 {
		t.Fatalf("blocks=%d want 1", len(renderer.vm.blocks))
	}
	frame := renderer.vm.blocks[0].frame
	if frame.Title != "request_permissions" {
		t.Fatalf("title=%q", frame.Title)
	}
	for _, want := range []string{
		"reason: run Go tests",
		"- read `/Users/demo/go`",
		"- write `/Users/demo/Library/Caches/go-build`",
		"✔ approved",
		"scope: session",
	} {
		if !strings.Contains(frame.Content, want) {
			t.Fatalf("replay body missing %q:\n%s", want, frame.Content)
		}
	}
	painted := stripANSI(strings.Join(renderViewport(&renderer.vm, 120, 30, 0, DiffThemeDark).lines, "\n"))
	if strings.Contains(painted, `"permissions":`) || strings.Contains(painted, `"file_system":`) {
		t.Fatalf("replayed request_permissions exposed raw JSON:\n%s", painted)
	}
	if !strings.Contains(painted, "Requested permissions") || !strings.Contains(painted, "/Users/demo/go") {
		t.Fatalf("replayed request_permissions did not match live card UX:\n%s", painted)
	}
}

func TestReplayWriteAndEditUsePersistedLiveDiffBody(t *testing.T) {
	for _, tc := range []struct {
		toolName string
		verb     string
		diffBody string
	}{
		{
			toolName: "write_file",
			verb:     "Created",
			diffBody: "turn diff: `internal/demo.go` (+1/-0)\n\n```diff\n--- internal/demo.go:before\n+++ internal/demo.go:after\n@@ -0,0 +1 @@\n+new\n```",
		},
		{
			toolName: "edit_file",
			verb:     "Edited",
			diffBody: "turn diff: `internal/demo.go` (+1/-1)\n\n```diff\n--- internal/demo.go:before\n+++ internal/demo.go:after\n@@ -1 +1 @@\n-old\n+new\n```",
		},
	} {
		t.Run(tc.toolName, func(t *testing.T) {
			args := `{"file_path":"internal/demo.go","content":"package demo"}`
			if tc.toolName == "edit_file" {
				args = `{"file_path":"internal/demo.go","old_string":"old","new_string":"new"}`
			}
			assistantParts := state.MessagePartsJSON(llm.AssistantMessage(nil, llm.ToolCall{
				ID:   "call-write",
				Type: llm.ToolTypeFunction,
				Function: llm.FunctionCall{
					Name:      tc.toolName,
					Arguments: args,
				},
			}), "")
			metaJSON, err := json.Marshal(tool.ToolMeta{
				ToolName: tc.toolName,
				Status:   "completed",
				Input:    map[string]any{"file_path": "internal/demo.go"},
			})
			if err != nil {
				t.Fatal(err)
			}
			toolResult := llm.ToolResultMessage("call-write", llm.Text("ok"))
			toolResult.ToolDisplay = &llm.ToolDisplayState{
				Body:         tc.diffBody,
				Summary:      strings.ToLower(tc.verb) + " internal/demo.go",
				ToolMetaJSON: string(metaJSON),
			}
			toolParts := state.MessagePartsJSON(toolResult, "ok")

			renderer := NewRenderer(nil, nil)
			renderer.EnableViewportMode()
			t.Cleanup(renderer.DisableViewportMode)
			replayTurnsToRenderer(renderer, []state.Message{
				{Role: "assistant", PartsJSON: assistantParts},
				{Role: "tool", Content: "ok", PartsJSON: toolParts, ExecDurationMs: 50},
			})

			if len(renderer.vm.blocks) != 1 {
				t.Fatalf("blocks=%d want 1", len(renderer.vm.blocks))
			}
			frame := renderer.vm.blocks[0].frame
			if frame.Content != tc.diffBody {
				t.Fatalf("replay content=%q want persisted live diff %q", frame.Content, tc.diffBody)
			}
			painted := stripANSI(strings.Join(renderViewport(&renderer.vm, 120, 30, 0, DiffThemeDark).lines, "\n"))
			if !strings.Contains(painted, tc.verb+" internal/demo.go") || !strings.Contains(painted, "Added 1 line") || !strings.Contains(painted, "new") {
				t.Fatalf("replayed write tool did not preserve live diff UX:\n%s", painted)
			}
			if strings.Contains(painted, "└ ok") {
				t.Fatalf("replayed write tool fell back to model-facing result:\n%s", painted)
			}
		})
	}
}

func TestTranscriptLooksLikeUserShellRecordDoesNotCaptureAgentShellToolResult(t *testing.T) {
	meta := tool.ToolMeta{ToolName: "shell"}
	if transcriptLooksLikeUserShellRecord(state.Message{Role: "tool", Content: `{"stdout":"agent output"}`}, meta) {
		t.Fatal("agent shell tool result must use normal tool replay formatting")
	}
	if !transcriptLooksLikeUserShellRecord(state.Message{Role: "user", Content: "<user_shell_command>\n<result>\nuser output\n</result>\n</user_shell_command>"}, meta) {
		t.Fatal("user-initiated shell history must retain its dedicated replay path")
	}
}

func TestReplayTurnsToRendererUnknownToolUsesInvocationWithoutSummarySuffix(t *testing.T) {
	toolMetaJSON, err := json.Marshal(tool.ToolMeta{
		ToolName:   "legacy_tool",
		Status:     "completed",
		Invocation: `legacy_tool {"path":"/repo/demo"}`,
	})
	if err != nil {
		t.Fatalf("marshal tool meta: %v", err)
	}
	assistantParts := state.MessagePartsJSON(llm.AssistantMessage(nil, llm.ToolCall{
		ID:   "call-legacy-1",
		Type: llm.ToolTypeFunction,
		Function: llm.FunctionCall{
			Name:      "legacy_tool",
			Arguments: `{"path":"/repo/demo"}`,
		},
	}), "")
	toolBody := "legacy result line 1\nlegacy result line 2\nlegacy result line 3\nlegacy result line 4"
	toolParts := state.MessagePartsJSON(llm.ToolResultMessage("call-legacy-1", llm.Text(toolBody)), toolBody)

	renderer := NewRenderer(nil, nil)
	renderer.EnableViewportMode()
	t.Cleanup(renderer.DisableViewportMode)

	replayTurnsToRenderer(renderer, []state.Message{
		{Role: "assistant", PartsJSON: assistantParts},
		{
			Role:         "tool",
			Content:      toolBody,
			PartsJSON:    toolParts,
			ToolMetaJSON: string(toolMetaJSON),
		},
	})

	if len(renderer.vm.blocks) != 1 {
		t.Fatalf("blocks=%d want 1", len(renderer.vm.blocks))
	}
	frame := renderer.vm.blocks[0].frame
	header := ToolDisplayHeader(frame, "")
	if header != `legacy_tool {"path":"/repo/demo"}` {
		t.Fatalf("header=%q", header)
	}
	if strings.Contains(header, "legacy result") {
		t.Fatalf("header leaked body text: %q", header)
	}
	painted := stripANSI(strings.Join(renderViewport(&renderer.vm, 80, 20, 0, DiffThemeDark).lines, "\n"))
	if !strings.Contains(painted, `legacy_tool {"path":"/repo/demo"}`) {
		t.Fatalf("viewport missing invocation fallback:\n%s", painted)
	}
	if strings.Contains(painted, `legacy_tool {"path":"/repo/demo"} ·`) {
		t.Fatalf("viewport reintroduced summary suffix:\n%s", painted)
	}
}

func TestResumeReplayUsesSameDeduplicatedToolHeader(t *testing.T) {
	toolMetaJSON, err := json.Marshal(tool.ToolMeta{
		ToolName:   "read_file",
		Status:     "completed",
		Invocation: "read /repo/hello.go",
		Input: map[string]any{
			"file_path": "/repo/hello.go",
			"offset":    float64(20),
			"limit":     float64(50),
		},
	})
	if err != nil {
		t.Fatalf("marshal tool meta: %v", err)
	}
	assistantParts := state.MessagePartsJSON(llm.AssistantMessage(nil, llm.ToolCall{
		ID:   "call-read-1",
		Type: llm.ToolTypeFunction,
		Function: llm.FunctionCall{
			Name:      "read_file",
			Arguments: `{"file_path":"/repo/hello.go"}`,
		},
	}), "")
	toolBody := "1|package main\n2|func main() {}"
	toolParts := state.MessagePartsJSON(llm.ToolResultMessage("call-read-1", llm.Text(toolBody)), toolBody)

	session := &fakeSession{transcriptTurns: []state.Message{
		{Role: "assistant", PartsJSON: assistantParts},
		{
			Role:             "tool",
			Content:          toolBody,
			PartsJSON:        toolParts,
			ToolMetaJSON:     string(toolMetaJSON),
			ExecStartedAtMs:  1780308000000,
			ExecFinishedAtMs: 1780308000500,
			ExecDurationMs:   500,
		},
		{Role: "assistant", Content: "The file defines a main function."},
	}}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	renderer.EnableViewportMode()
	t.Cleanup(renderer.DisableViewportMode)
	ctrl := newCommandController(session, renderer, &stubSelector{}, nil, "")
	ctrl.printSessionResumeContext("s1")

	if len(renderer.vm.blocks) < 2 {
		t.Fatal("expected replayed blocks")
	}
	var toolFrame Frame
	found := false
	for _, block := range renderer.vm.blocks {
		if block.frame.Kind == FrameTool {
			toolFrame = block.frame
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected replayed tool frame, blocks=%#v", renderer.vm.blocks)
	}
	header := ToolDisplayHeader(toolFrame, "")
	if header != "Read /repo/hello.go lines 21-70 · 0.5s" {
		t.Fatalf("header=%q", header)
	}
	if got := renderer.vm.blocks[len(renderer.vm.blocks)-1].frame; got.Kind != FrameStatus || got.Title != "resumed" {
		t.Fatalf("expected trailing resumed status, got %#v", got)
	}
	var toolIndex, assistantIndex, resumedIndex = -1, -1, -1
	for i, block := range renderer.vm.blocks {
		switch {
		case block.frame.Kind == FrameTool:
			toolIndex = i
		case block.frame.Kind == FrameAssistant && strings.Contains(block.frame.Content, "main function"):
			assistantIndex = i
		case block.frame.Kind == FrameStatus && block.frame.Title == "resumed":
			resumedIndex = i
		}
	}
	if toolIndex < 0 || assistantIndex < 0 || resumedIndex < 0 || !(toolIndex < assistantIndex && assistantIndex < resumedIndex) {
		t.Fatalf("expected tool and trailing assistant before resumed status; indexes tool=%d assistant=%d resumed=%d, blocks=%#v", toolIndex, assistantIndex, resumedIndex, renderer.vm.blocks)
	}
}

// A completed session_todo call replays as the plan card it emitted — the same
// card the live run drew — and never as the ordinary tool card the live hook
// suppresses (event.ToolStepRendersAsPlan).
func TestReplayTimelineRendersPlanCardForSessionTodoRow(t *testing.T) {
	require := require.New(t)
	assistantParts := state.MessagePartsJSON(llm.AssistantMessage(nil, llm.ToolCall{
		ID:   "call-todo-1",
		Type: llm.ToolTypeFunction,
		Function: llm.FunctionCall{
			Name:      "session_todo",
			Arguments: `{"action":"set","items":[{"id":"T1","content":"step one","status":"completed"}]}`,
		},
	}), "")
	body := `{"output":"{\"items\":[{\"id\":\"T1\",\"content\":\"step one\",\"status\":\"completed\"}]}"}` + "\n"
	toolParts := state.MessagePartsJSON(llm.ToolResultMessage("call-todo-1", llm.Text(body)), body)
	metaJSON, err := json.Marshal(tool.ToolMeta{ToolName: "session_todo", Status: "completed"})
	require.NoError(err)
	planUpdates := []event.PlanUpdatedPayload{{
		Title: "Updated Plan",
		Items: []event.PlanUpdateItem{{ID: "T1", Content: "step one", Status: "completed"}},
	}}

	renderer := NewRenderer(nil, nil)
	renderer.EnableViewportMode()
	t.Cleanup(renderer.DisableViewportMode)
	reducer := &Reducer{}
	replayTimelineWithReducer(renderer, []state.Message{
		{Role: "assistant", PartsJSON: assistantParts},
		{Role: "tool", Content: body, PartsJSON: toolParts, ToolMetaJSON: string(metaJSON)},
	}, nil, reducer, planUpdates)
	flushReplayReducer(renderer, reducer)

	var plan *Frame
	for i := range renderer.vm.blocks {
		frame := renderer.vm.blocks[i].frame
		if frame.Kind == FrameTool {
			t.Fatalf("session_todo row replayed as ordinary tool card: title=%q", frame.Title)
		}
		if frame.Kind == FramePlan {
			plan = &frame
		}
	}
	require.NotNil(plan, "plan card missing from replay")
	require.Equal("Updated Plan", plan.Title)
	require.Contains(plan.Content, "step one")
}

// A failed session_todo call keeps its own card, exactly as the live hook
// keeps it: a failure has no plan card to be represented by.
func TestReplayTimelineKeepsFailedSessionTodoCard(t *testing.T) {
	require := require.New(t)
	assistantParts := state.MessagePartsJSON(llm.AssistantMessage(nil, llm.ToolCall{
		ID:   "call-todo-1",
		Type: llm.ToolTypeFunction,
		Function: llm.FunctionCall{
			Name:      "session_todo",
			Arguments: `{"action":"set","items":[{"id":"T1","content":"step one","status":"pending"}]}`,
		},
	}), "")
	body := "disk full"
	toolParts := state.MessagePartsJSON(llm.ToolResultMessage("call-todo-1", llm.Text(body)), body)
	metaJSON, err := json.Marshal(tool.ToolMeta{ToolName: "session_todo", Status: "failed"})
	require.NoError(err)

	renderer := NewRenderer(nil, nil)
	renderer.EnableViewportMode()
	t.Cleanup(renderer.DisableViewportMode)
	reducer := &Reducer{}
	replayTimelineWithReducer(renderer, []state.Message{
		{Role: "assistant", PartsJSON: assistantParts},
		{Role: "tool", Content: body, PartsJSON: toolParts, ToolMetaJSON: string(metaJSON)},
	}, nil, reducer, nil)
	flushReplayReducer(renderer, reducer)

	var toolCard *Frame
	for i := range renderer.vm.blocks {
		frame := renderer.vm.blocks[i].frame
		if frame.Kind == FrameTool {
			toolCard = &frame
		}
		if frame.Kind == FramePlan {
			t.Fatal("failed session_todo call must not render a plan card")
		}
	}
	require.NotNil(toolCard, "failed session_todo card missing from replay")
}

// A session recorded before its event log (or migrated into one) has no plan
// event to pair with, so the transcript row stays the only record of the call
// and keeps its ordinary card.
func TestReplayTimelineKeepsSessionTodoCardWithoutPlanEvent(t *testing.T) {
	require := require.New(t)
	assistantParts := state.MessagePartsJSON(llm.AssistantMessage(nil, llm.ToolCall{
		ID:   "call-todo-1",
		Type: llm.ToolTypeFunction,
		Function: llm.FunctionCall{
			Name:      "session_todo",
			Arguments: `{"action":"set","items":[{"id":"T1","content":"step one","status":"completed"}]}`,
		},
	}), "")
	body := `{"output":"{\"items\":[]}"}`
	toolParts := state.MessagePartsJSON(llm.ToolResultMessage("call-todo-1", llm.Text(body)), body)
	metaJSON, err := json.Marshal(tool.ToolMeta{ToolName: "session_todo", Status: "completed"})
	require.NoError(err)

	renderer := NewRenderer(nil, nil)
	renderer.EnableViewportMode()
	t.Cleanup(renderer.DisableViewportMode)
	reducer := &Reducer{}
	replayTimelineWithReducer(renderer, []state.Message{
		{Role: "assistant", PartsJSON: assistantParts},
		{Role: "tool", Content: body, PartsJSON: toolParts, ToolMetaJSON: string(metaJSON)},
	}, nil, reducer, nil)
	flushReplayReducer(renderer, reducer)

	var toolCard *Frame
	for i := range renderer.vm.blocks {
		frame := renderer.vm.blocks[i].frame
		if frame.Kind == FrameTool {
			toolCard = &frame
		}
	}
	require.NotNil(toolCard, "session_todo card missing from replay without its plan event")
}

// A subagent's plan update replays from its persisted event into that
// subagent's own view, the same routing the live delivery used.
func TestReplayTimelineRoutesSubagentPlanEventIntoItsView(t *testing.T) {
	require := require.New(t)
	planPayload, err := json.Marshal(event.PlanUpdatedPayload{
		Title:   "Updated Plan",
		AgentID: "worker-1",
		Items:   []event.PlanUpdateItem{{ID: "T1", Content: "child step", Status: "in_progress"}},
	})
	require.NoError(err)
	events := []event.RunEvent{
		event.NewRunEvent("evt-1", "run-child-1", "sess-1", event.RunEventPlanUpdated, planPayload, time.Now()),
	}

	renderer := NewRenderer(nil, nil)
	renderer.EnableViewportMode()
	t.Cleanup(renderer.DisableViewportMode)
	reducer := &Reducer{}
	replayTimelineWithReducer(renderer, nil, events, reducer, nil)
	flushReplayReducer(renderer, reducer)

	// A subagent's plan frame is retained in that subagent's own view, never
	// in the conversation transcript.
	var plan *Frame
	if vm := renderer.perAgentVM["worker-1"]; vm != nil {
		for i := range vm.blocks {
			frame := vm.blocks[i].frame
			if frame.Kind == FramePlan {
				plan = &frame
			}
		}
	}
	require.NotNil(plan, "subagent plan card missing from its own replayed view")
	require.Equal("worker-1", plan.AgentID)
	require.Contains(plan.Content, "child step")
	for i := range renderer.vm.blocks {
		require.NotEqual(FramePlan, renderer.vm.blocks[i].frame.Kind, "subagent plan card leaked into the conversation")
	}
}

// Phase 3a redesign: surface errors from the session store via PrintError so
// the user sees the failure instead of an empty card.

// The helpers below were production functions that only the tests ever
// called: each is a thin composition of live code. They live here so the
// production files carry no unused code while the tests keep exercising
// the live functions underneath.

// replayTurnsToRenderer feeds each Turn through a fresh Reducer (no Tracker,
// to keep counters clean for the live session that follows) and renders the
// resulting Frames. Frames are rendered through the same path as the live
// session, so replay reproduces the main session's retained blocks exactly.
//
// A tool call is persisted as two rows: an assistant row carrying the tool_calls
// part (tool name + arguments) and a tool-role row carrying the output text plus
// a tool_call_id back-reference. Neither row alone names the tool from the tool
// row's perspective, so a pre-scan indexes every stored tool_call by id (see
// buildToolCallIndex). Each tool-role row is then rendered as one FrameTool whose
// name, invocation, and summary are reconstructed from the indexed call through
// the same agentnotify labeling the live path uses — no content parsing.
func replayTurnsToRenderer(renderer *Renderer, turns []state.Message) {
	if renderer == nil {
		return
	}
	// Batch the repaint: replay feeds every turn through RenderFrame, which
	// otherwise repaints the whole retained viewport on each append (O(n^2)
	// over thousands of turns, freezing the TUI). Suppressing per-frame paints
	// and repainting once at the end makes replay O(n).
	renderer.BeginBatch()
	defer renderer.EndBatch()
	reducer := &Reducer{}
	replayTurnsWithReducer(renderer, turns, reducer)
	flushReplayReducer(renderer, reducer)
}

func replayTurnsWithReducer(renderer *Renderer, turns []state.Message, reducer *Reducer) {
	if renderer == nil || reducer == nil {
		return
	}
	callIndex, _ := buildToolCallIndex(turns)
	subagentCalls := turn.SubagentCallsInTranscript(turns)
	for _, turn := range turns {
		replayTurnWithReducer(renderer, reducer, turn, callIndex, subagentCalls, nil)
	}
}

// replaySubagentRunEvents projects the durable child event stream through the
// same reducer messages as live delivery. Primary transcript rows were already
// rendered above, so only child-owned events are consumed here.
func replaySubagentRunEvents(renderer *Renderer, events []event.RunEvent) int {
	if renderer == nil || len(events) == 0 {
		return 0
	}
	renderer.BeginBatch()
	defer renderer.EndBatch()
	reducer := &Reducer{}
	count := replaySubagentRunEventsWithReducer(renderer, events, reducer)
	flushReplayReducer(renderer, reducer)
	return count
}

func replaySubagentRunEventsWithReducer(renderer *Renderer, events []event.RunEvent, reducer *Reducer) int {
	if renderer == nil || reducer == nil || len(events) == 0 {
		return 0
	}
	count := 0
	for _, evt := range events {
		msg, ok := replaySubagentRunEventMessage(evt)
		if !ok {
			continue
		}
		count++
		result := reducer.Reduce(msg)
		for _, frame := range result.Frames {
			if frame.Kind == FrameThinking && !frame.Final {
				continue
			}
			renderer.RenderFrame(frame)
		}
	}
	return count
}

func classifyStreamCommand(cmd turn.Command) StreamCommandAction {
	name := strings.ToLower(strings.TrimSpace(cmd.Name))
	switch name {
	case "exit":
		return StreamActionTerminalControl
	case "model", "permissions", "skills", "resume", "agent", "subagents", "connect", "sandbox":
		return StreamActionNativeUI
	default:
		return StreamActionRuntimeDispatch
	}
}

func TestPublicCommandsHaveExpectedStreamAction(t *testing.T) {
	seen := map[StreamCommandAction]int{}
	for _, cmd := range turn.All() {
		if cmd.Visibility != turn.VisibilityPublic {
			continue
		}
		if !cmd.AllowedOn(turn.SurfaceTUI) {
			continue
		}
		action := classifyStreamCommand(cmd)
		seen[action]++
	}
	if seen[StreamActionTerminalControl] == 0 {
		t.Fatal("missing terminal control commands in stream matrix")
	}
	if seen[StreamActionNativeUI] == 0 {
		t.Fatal("missing native ui commands in stream matrix")
	}
	if seen[StreamActionRuntimeDispatch] == 0 {
		t.Fatal("missing runtime dispatch commands in stream matrix")
	}
}

// replayedTimelineOrder renders a resume timeline and returns one line per
// painted block, in the order the user would scroll through them.
func replayedTimelineOrder(t *testing.T, turns []state.Message, events []event.RunEvent) []string {
	t.Helper()
	renderer := NewRenderer(nil, nil)
	renderer.EnableViewportMode()
	t.Cleanup(renderer.DisableViewportMode)
	reducer := &Reducer{}
	replayTimelineWithReducer(renderer, turns, events, reducer, nil)
	flushReplayReducer(renderer, reducer)
	out := make([]string, 0, len(renderer.vm.blocks))
	for _, block := range renderer.vm.blocks {
		parts := make([]string, 0, 3)
		for _, part := range []string{block.frame.Title, block.frame.Summary, block.frame.Content} {
			if part = strings.TrimSpace(stripANSI(part)); part != "" {
				parts = append(parts, part)
			}
		}
		out = append(out, strings.Join(parts, " "))
	}
	return out
}

// A run writes every row it produced when it ends, so a whole turn - the user
// message included, once a steer or a follow-up lands inside the same second -
// carries one second-precision created_at. Ordering the replay by that stamp
// hoisted every user row of the block above the answers they had followed,
// which is what a resumed session showed as a wall of repeated questions.
func TestReplayTimelineKeepsStoredOrderWhenAWholeTurnSharesOneTimestamp(t *testing.T) {
	turns := []state.Message{
		{RowID: 1, Role: "user", Content: "first question", CreatedAt: 100},
		{RowID: 2, Role: "assistant", Content: "first answer", CreatedAt: 200},
		{RowID: 3, Role: "user", Content: "second question", CreatedAt: 200},
		{RowID: 4, Role: "assistant", Content: "second answer", CreatedAt: 200},
	}
	got := replayedTimelineOrder(t, turns, nil)
	want := []string{"you first question", "first answer", "you second question", "second answer"}
	if len(got) != len(want) {
		t.Fatalf("blocks=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("replay order=%v want %v", got, want)
		}
	}
}

// A subagent's events are stamped while its parent run is still executing, so
// they always predate the rows that run later flushes. Ordering the merged
// stream by time therefore painted every child card before the tool call that
// dispatched it - and before the user message that asked for it. The spawn
// records the parent call, and that call is a row of this transcript, so the
// card belongs directly after it.
func TestReplayTimelineAnchorsSubagentCardsToTheCallThatSpawnedThem(t *testing.T) {
	callParts := state.MessagePartsJSON(llm.AssistantMessage(nil, llm.ToolCall{
		ID: "fanout-1", Type: llm.ToolTypeFunction,
		Function: llm.FunctionCall{Name: "spawn_subagent", Arguments: `{"agent_type":"explore"}`},
	}), "")
	body := `{"agent_id":"child"}`
	turns := []state.Message{
		{RowID: 1, Role: "user", Content: "run the fanout", CreatedAt: 100},
		{RowID: 2, Role: "assistant", PartsJSON: callParts, CreatedAt: 300},
		{RowID: 3, Role: "tool", Content: body, ToolStepID: "fanout-1", CreatedAt: 300,
			PartsJSON: state.MessagePartsJSON(llm.ToolResultMessage("fanout-1", llm.Text(body)), body)},
		{RowID: 4, Role: "assistant", Content: "all done", CreatedAt: 300},
	}
	spawnedAt := time.Unix(200, 0)
	events := []event.RunEvent{
		event.NewRunEvent("spawned", "exec-1", "sess", event.RunEventSubagentSpawned, event.SubagentSpawnedPayload{
			AgentID: "agent-1", AgentType: "explore", TaskID: "agent-1", Title: "look around",
			ParentRunID: "parent-1", ParentToolCallID: "fanout-1", ExecutionID: "exec-1",
		}, spawnedAt),
		event.NewRunEvent("ended", "exec-1", "sess", event.RunEventSubagentEnded, event.SubagentEndedPayload{
			AgentID: "agent-1", AgentType: "explore", TaskID: "agent-1", Status: "ok",
			ParentRunID: "parent-1", ParentToolCallID: "fanout-1", ExecutionID: "exec-1",
		}, spawnedAt.Add(time.Second)),
	}
	events[0].Sequence = 1
	events[1].Sequence = 2
	got := replayedTimelineOrder(t, turns, events)
	want := []string{
		"you run the fanout",
		`spawn_subagent ran spawn_subagent {"agent_type":"explore"} {"agent_id":"child"}`,
		// One card for the one execution: opened by the spawn, closed by the
		// end, both anchored after the call that dispatched it.
		"Ran 1 explore task ✓ look around",
		"all done",
	}
	if len(got) != len(want) {
		t.Fatalf("blocks=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("replay order=%v want %v", got, want)
		}
	}
}

// A plan review's own events carry the approval action they answer but no tool
// step of their own, so without an anchor they fall back to the clock and land
// wherever their timestamp happens to sort — after the approval that produced
// them. The card belongs to that approval's exchange, so replay anchors it to
// the call the action names and its sequence puts it between the two lines.
func TestReplayTimelinePutsThePlanReviewCardBetweenTheApprovalLines(t *testing.T) {
	callArgs := `{"plan":"do the thing"}`
	body := `{"plan":"do the thing"}`
	turns := []state.Message{
		{RowID: 1, Role: "user", Content: "plan the work", CreatedAt: 100},
		{RowID: 2, Role: "assistant", CreatedAt: 300,
			PartsJSON: state.MessagePartsJSON(llm.AssistantMessage(nil, llm.ToolCall{
				ID: "call-exit", Type: llm.ToolTypeFunction,
				Function: llm.FunctionCall{Name: "exit_plan_mode", Arguments: callArgs},
			}), "")},
		{RowID: 3, Role: "tool", Content: body, ToolStepID: "call-exit", CreatedAt: 300,
			PartsJSON: state.MessagePartsJSON(llm.ToolResultMessage("call-exit", llm.Text(body)), body)},
		{RowID: 4, Role: "assistant", Content: "all done", CreatedAt: 300},
	}
	at := time.Unix(400, 0).UTC()
	events := []event.RunEvent{
		event.NewRunEvent("approval-requested:act-1", "run-1", "s1", event.RunEventApprovalReq,
			event.ApprovalRequestedPayload{ActionID: "act-1", ActionKind: "exit_plan_mode", ToolStepID: "call-exit"}, at),
		event.NewRunEvent("approval-resolved:act-1:review_requested", "run-1", "s1", event.RunEventApprovalResolved,
			event.ApprovalResolvedPayload{
				ActionID: "act-1", ActionKind: "exit_plan_mode", Decision: "review_requested", ToolStepID: "call-exit",
				Confirmation: "✔ You asked zhipuai/glm-5.3-flash to review the plan",
			}, at),
		event.NewRunEvent("plan-review-started", "run-1", "s1", event.RunEventPlanReviewStarted,
			event.PlanReviewStartedPayload{
				ActionID: "act-1", ReviewID: "plan-review:rv-1", Provider: "zhipuai", Model: "glm-5.3-flash",
			}, at),
		event.NewRunEvent("plan-reviewed", "run-1", "s1", event.RunEventPlanReviewed,
			event.PlanReviewedPayload{
				ActionID: "act-1", ReviewID: "plan-review:rv-1", Provider: "zhipuai", Model: "glm-5.3-flash",
				Outcome: "done",
			}, at),
		event.NewRunEvent("approval-resolved:act-1:approved", "run-1", "s1", event.RunEventApprovalResolved,
			event.ApprovalResolvedPayload{
				ActionID: "act-1", ActionKind: "exit_plan_mode", Decision: "approved", ToolStepID: "call-exit",
				Confirmation: "✔ You approved forebrain to exit plan mode",
			}, at),
	}
	for i := range events {
		events[i].Sequence = int64(i + 1)
	}

	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.composerSuppressed = true
	replayTimelineWithReducer(renderer, turns, events, &Reducer{}, nil)

	asked, card, approved, tool := -1, -1, -1, -1
	seq := make([]string, 0, len(renderer.vm.blocks))
	for i, block := range renderer.vm.blocks {
		f := block.frame
		switch {
		case f.Kind == FrameFanout:
			card = i
			seq = append(seq, "card:"+f.StepID)
			if f.Summary != "Ran 1 plan-reviewer task" {
				t.Fatalf("card summary = %q, want Ran 1 plan-reviewer task", f.Summary)
			}
		case f.Kind == FrameTool && f.Title == "exit_plan_mode":
			tool = i
			seq = append(seq, "exit plan mode")
		case f.Kind == FrameStatus && strings.Contains(f.Content, "You asked"):
			asked = i
			seq = append(seq, "asked")
		case f.Kind == FrameStatus && strings.Contains(f.Content, "You approved"):
			approved = i
			seq = append(seq, "approved")
		}
	}
	if asked < 0 || card < 0 || approved < 0 || tool < 0 {
		t.Fatalf("replay lost a record: asked=%d card=%d approved=%d tool=%d %v", asked, card, approved, tool, seq)
	}
	if !(asked < card && card < approved && approved < tool) {
		t.Fatalf("the card must replay between the approval's two confirmation lines, above the call it names: %v", seq)
	}
}

// ---- canceled gates and their confirmation lines belong in the replay ----

// assistantToolCallRow builds the transcript row shape a gate leaves behind: the
// assistant message that issued a tool call, with no tool row to answer it.
func assistantToolCallRow(text, callID, toolName, args string) state.Message {
	return state.Message{
		Role: "assistant", Content: text,
		PartsJSON: state.MessagePartsJSON(llm.AssistantMessage(nil, llm.ToolCall{
			ID: callID, Type: llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: toolName, Arguments: args},
		}), text),
		CreatedAt: 100,
	}
}

// A gate that was canceled leaves an assistant row with a tool call and no tool
// row. Live stamped that call's card canceled when the run aborted
// (Renderer.FinalizePendingTools); replay has to rebuild the same card, exactly
// once, and place the confirmation line the user read above it.
func TestResumeRebuildsOneCanceledCardPerOrphanToolCall(t *testing.T) {
	callArgs := `{"plan":"do the thing"}`
	turns := []state.Message{
		{RowID: 1, Role: "user", Content: "plan the work", CreatedAt: 99},
		assistantToolCallRow("here is the plan", "call-exit", "exit_plan_mode", callArgs),
	}
	events := []event.RunEvent{
		event.NewRunEvent("approval-requested:act-1", "run-1", "s1", event.RunEventApprovalReq,
			event.ApprovalRequestedPayload{ActionID: "act-1", ActionKind: "exit_plan_mode", ToolStepID: "call-exit"},
			time.Unix(101, 0).UTC()),
		event.NewRunEvent("approval-resolved:act-1:cancelled", "run-1", "s1", event.RunEventApprovalResolved,
			event.ApprovalResolvedPayload{
				ActionID: "act-1", ActionKind: "exit_plan_mode", Decision: "cancelled", ToolStepID: "call-exit",
				Confirmation: "✗ You canceled forebrain's request to exit plan mode",
			}, time.Unix(102, 0).UTC()),
	}

	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.composerSuppressed = true
	// The gate record paints nothing of its own - the card is the tool call the
	// record names - so only the decision counts as a replayed record here.
	if got := replayTimelineWithReducer(renderer, turns, events, &Reducer{}, nil); got != 1 {
		t.Fatalf("replayed records = %d, want the decision alone", got)
	}

	cardIndex, lineIndex := -1, -1
	for i, block := range renderer.vm.blocks {
		switch {
		case block.frame.Kind == FrameTool:
			if cardIndex >= 0 {
				t.Fatalf("more than one tool card replay: %#v", renderer.vm.blocks)
			}
			cardIndex = i
			if block.frame.Title != "exit_plan_mode" {
				t.Fatalf("rebuilt card title = %q, want the tool that was gated", block.frame.Title)
			}
			if block.frame.ToolMeta.Status != "canceled" {
				t.Fatalf("rebuilt card status = %q, want the one-l 'canceled' Renderer.FinalizePendingTools stamps", block.frame.ToolMeta.Status)
			}
			if block.frame.Content != "" {
				t.Fatalf("a canceled card claims output it never produced: %q", block.frame.Content)
			}
		case block.frame.Kind == FrameStatus && strings.Contains(block.frame.Content, "You canceled"):
			lineIndex = i
			if block.frame.MaxDisplayLines != approvalConfirmationMaxLines {
				t.Fatalf("replayed confirmation lost its line cap: %#v", block.frame)
			}
			if block.frame.AgentID != "" {
				t.Fatalf("a conversation confirmation must not claim an agent: %#v", block.frame)
			}
		}
	}
	if cardIndex < 0 || lineIndex < 0 {
		t.Fatalf("replay lost the gate card (%d) or its confirmation (%d): %#v", cardIndex, lineIndex, renderer.vm.blocks)
	}
	if lineIndex > cardIndex {
		t.Fatalf("the confirmation must read above the card it answers: line=%d card=%d", lineIndex, cardIndex)
	}
}

// The orphan test above is only meaningful if a call that did run is left alone:
// a tool row answers it, so replay must not add a second canceled card beside
// the real one.
func TestResumeDoesNotRebuildACardForAToolCallThatRan(t *testing.T) {
	turns := []state.Message{
		{RowID: 1, Role: "user", Content: "run it", CreatedAt: 99},
		assistantToolCallRow("running it", "call-1", "shell", `{"command":"ls"}`),
		{
			RowID: 3, Role: "tool", Content: `{"stdout":"a.go"}`,
			PartsJSON: state.MessagePartsJSON(llm.ToolResultMessage("call-1", llm.Text(`{"stdout":"a.go"}`)), ""),
			CreatedAt: 101,
		},
	}

	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.composerSuppressed = true
	replayTimelineWithReducer(renderer, turns, nil, &Reducer{}, nil)

	for _, block := range renderer.vm.blocks {
		if block.frame.Kind == FrameTool && block.frame.ToolMeta.Status == "canceled" {
			t.Fatalf("a completed call was rebuilt as canceled: %#v", block.frame)
		}
	}
}

// The same call id can be persisted twice (a duplicated turn), and the same
// assistant row can appear twice. Orphan detection is a set subtraction over the
// whole transcript, so both duplicates still yield exactly one card.
func TestResumeRebuildsOneCanceledCardForADuplicatedOrphanCall(t *testing.T) {
	row := assistantToolCallRow("here is the plan", "call-exit", "exit_plan_mode", `{"plan":"do the thing"}`)
	turns := []state.Message{
		{RowID: 1, Role: "user", Content: "plan the work", CreatedAt: 99},
		row,
		row,
	}

	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.composerSuppressed = true
	replayTimelineWithReducer(renderer, turns, nil, &Reducer{}, nil)

	cards := 0
	for _, block := range renderer.vm.blocks {
		if block.frame.Kind == FrameTool {
			cards++
		}
	}
	if cards != 1 {
		t.Fatalf("duplicated orphan call produced %d cards, want 1: %#v", cards, renderer.vm.blocks)
	}
}

// A confirmation with no tool step to anchor to - the inline network approval
// never resolves one - still replays as the line the user read. It must not
// claim a card, because nothing names which call it answered.
func TestResumeReplaysAConfirmationWithNoResolvableToolStep(t *testing.T) {
	turns := []state.Message{{RowID: 1, Role: "assistant", Content: "let me fetch that", CreatedAt: 100}}
	events := []event.RunEvent{
		event.NewRunEvent("approval-resolved:act-9:cancelled", "run-1", "s1", event.RunEventApprovalResolved,
			event.ApprovalResolvedPayload{
				ActionID: "act-9", ActionKind: "shell", Decision: "cancelled",
				Confirmation: "✗ You canceled the request to run curl example.com",
			}, time.Unix(101, 0).UTC()),
	}

	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.composerSuppressed = true
	replayTimelineWithReducer(renderer, turns, events, &Reducer{}, nil)

	lines := 0
	for _, block := range renderer.vm.blocks {
		if block.frame.Kind == FrameTool {
			t.Fatalf("a record naming no call claimed a card: %#v", block.frame)
		}
		if block.frame.Kind == FrameStatus && strings.Contains(block.frame.Content, "You canceled") {
			lines++
		}
	}
	if lines != 1 {
		t.Fatalf("confirmation lines = %d, want 1: %#v", lines, renderer.vm.blocks)
	}
}

// An approved gate keeps its card: the call ran, so the card comes from the tool
// row that answered it, and the confirmation the user read has to land on that
// card and not on whichever call happened to be drawn before it.
func TestResumePlacesAnApprovedConfirmationOnTheCallItAuthorised(t *testing.T) {
	turns := []state.Message{
		{RowID: 1, Role: "user", Content: "do it", CreatedAt: 99},
		assistantToolCallRow("listing", "call-a", "shell", `{"command":"ls"}`),
		{
			RowID: 3, Role: "tool", Content: `{"stdout":"a.go"}`,
			PartsJSON: state.MessagePartsJSON(llm.ToolResultMessage("call-a", llm.Text(`{"stdout":"a.go"}`)), ""),
			CreatedAt: 100,
		},
		assistantToolCallRow("removing", "call-b", "shell", `{"command":"rm x"}`),
		{
			RowID: 5, Role: "tool", Content: `{"stdout":""}`,
			PartsJSON: state.MessagePartsJSON(llm.ToolResultMessage("call-b", llm.Text(`{"stdout":""}`)), ""),
			CreatedAt: 101,
		},
	}
	events := []event.RunEvent{
		event.NewRunEvent("approval-resolved:act-b:approved", "run-1", "s1", event.RunEventApprovalResolved,
			event.ApprovalResolvedPayload{
				ActionID: "act-b", ActionKind: "shell", Decision: "approved", ToolStepID: "call-b",
				Confirmation: "✔ You approved forebrain to run rm x this time",
			}, time.Unix(102, 0).UTC()),
	}

	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.composerSuppressed = true
	replayTimelineWithReducer(renderer, turns, events, &Reducer{}, nil)

	line, card := -1, -1
	for i, block := range renderer.vm.blocks {
		switch {
		case block.frame.Kind == FrameStatus && strings.Contains(block.frame.Content, "You approved"):
			line = i
		case block.frame.Kind == FrameTool && block.frame.StepID == "call-b":
			card = i
		}
	}
	if line < 0 || card < 0 {
		t.Fatalf("replay lost the confirmation (%d) or the card it answers (%d): %#v", line, card, renderer.vm.blocks)
	}
	if line != card-1 {
		t.Fatalf("the confirmation must read directly above the call it authorised: line=%d card=%d", line, card)
	}
}

// A batch the run never reached replays in the order the model asked for it.
// The calls are read out of a map, so without an explicit order the cards came
// back shuffled on every resume of the same session.
func TestResumeKeepsACanceledBatchInTheOrderItWasIssued(t *testing.T) {
	turns := []state.Message{
		{RowID: 1, Role: "user", Content: "look around", CreatedAt: 99},
		{
			RowID: 2, Role: "assistant", Content: "on it",
			PartsJSON: state.MessagePartsJSON(llm.AssistantMessage(nil,
				llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "shell", Arguments: `{"command":"one"}`}},
				llm.ToolCall{ID: "call-2", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "shell", Arguments: `{"command":"two"}`}},
				llm.ToolCall{ID: "call-3", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "shell", Arguments: `{"command":"three"}`}},
			), "on it"),
			CreatedAt: 100,
		},
	}
	want := []string{"call-1", "call-2", "call-3"}
	for attempt := 0; attempt < 8; attempt++ {
		renderer := NewRenderer(nil, nil)
		renderer.viewportMode = true
		renderer.composerSuppressed = true
		replayTimelineWithReducer(renderer, turns, nil, &Reducer{}, nil)
		got := []string(nil)
		for _, block := range renderer.vm.blocks {
			if block.frame.Kind == FrameTool {
				got = append(got, block.frame.StepID)
			}
		}
		if len(got) != len(want) {
			t.Fatalf("canceled cards = %v, want one per issued call %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("attempt %d replayed the batch as %v, want %v", attempt, got, want)
			}
		}
	}
}

// A canceled card must not claim the call ran. Live built it when the call was
// dispatched and only overwrote its status, so it reads "running ..." under a
// canceled badge - never "ran ...".
func TestCanceledCardSaysTheCallWasRunningNotThatItRan(t *testing.T) {
	turns := []state.Message{
		{RowID: 1, Role: "user", Content: "list", CreatedAt: 99},
		assistantToolCallRow("on it", "call-1", "shell", `{"command":"ls -la"}`),
	}
	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.composerSuppressed = true
	replayTimelineWithReducer(renderer, turns, nil, &Reducer{}, nil)

	for _, block := range renderer.vm.blocks {
		if block.frame.Kind != FrameTool {
			continue
		}
		if strings.HasPrefix(block.frame.Summary, "ran ") {
			t.Fatalf("a canceled card claims the call ran: %q", block.frame.Summary)
		}
		if !strings.Contains(block.frame.Summary, "ls -la") {
			t.Fatalf("a canceled card lost what the call was: %q", block.frame.Summary)
		}
		return
	}
	t.Fatalf("no canceled card replayed: %#v", renderer.vm.blocks)
}

// A dispatch call is not rebuilt. Its card is a fanout block the subagent event
// log owns, and a rebuilt one would report every task it never reached as done.
func TestResumeDoesNotRebuildACardForAnInterruptedDispatch(t *testing.T) {
	turns := []state.Message{
		{RowID: 1, Role: "user", Content: "fan out", CreatedAt: 99},
		assistantToolCallRow("dispatching", "call-fanout", "subagent_fanout",
			`{"tasks":[{"prompt":"one"},{"prompt":"two"}]}`),
	}
	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.composerSuppressed = true
	replayTimelineWithReducer(renderer, turns, nil, &Reducer{}, nil)

	for _, block := range renderer.vm.blocks {
		if block.frame.Kind == FrameTool || block.frame.Kind == FrameFanout {
			t.Fatalf("an interrupted dispatch was rebuilt as a card: %#v", block.frame)
		}
	}
}

// The whole path, end to end: a gate is shown and canceled, the process ends,
// and a fresh store reads back both records, replays them, and shows the same
// two things the user saw as they quit.
func TestCanceledGateAndConfirmationSurviveAReopenedStore(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dbPath := filepath.Join(home, "state.sqlite")
	db, err := state.OpenStateForTest(ctx, dbPath)
	require.NoError(t, err)

	runs := &state.RunStore{DB: db}
	actions := &state.ActionService{DB: db}
	sess := state.NewSessionStore(db, "main")
	cs := sessionEnv{Home: home, SQL: db, SessStore: sess, ActionSvc: actions, RunSvc: runs}.session()

	require.NoError(t, sess.Ensure(ctx, "s1", "s1"))
	run, err := runs.CreateRun(ctx, "s1", "plan the work")
	require.NoError(t, err)
	act, err := actions.CreatePending(ctx, "s1", "exit_plan_mode", map[string]any{"plan": "do the thing", "session_id": "s1"})
	require.NoError(t, err)
	parked := llm.AssistantMessage([]llm.ContentPart{llm.Text("here is the plan")}, llm.ToolCall{
		ID: "call-exit", Type: llm.ToolTypeFunction,
		Function: llm.FunctionCall{Name: "exit_plan_mode", Arguments: `{"plan":"do the thing"}`},
	})
	require.NoError(t, runs.SetWaitingAction(ctx, run.ID, state.Wait{
		RunID: run.ID, ActionID: act.ID, ToolName: "exit_plan_mode",
		ToolInputJSON:   `{"plan":"do the thing"}`,
		SessionSnapshot: []llm.Message{llm.UserMessage(llm.Text("plan the work")), parked},
	}))
	require.NoError(t, sess.AppendMessageSequence(ctx, "s1",
		[]llm.Message{llm.UserMessage(llm.Text("plan the work")), parked}, "test-model", ""))

	req, err := cs.buildSurfaceToolApprovalRequest(ctx, "s1")
	require.NoError(t, err)
	require.NotNil(t, req)

	f := newFakeRawSelector(t)
	f.write([]byte{0x1b})
	f.close()
	var out bytes.Buffer
	sink := NewInteractiveApprovalSinkWithTTY(&out, f.rs).WithRecorder(cs)
	decision, err := sink.PromptToolApproval(ctx, *req)
	require.NoError(t, err)
	require.True(t, decision.Cancelled)
	require.NoError(t, db.Close())

	reopened, err := state.OpenStateForTest(ctx, dbPath)
	require.NoError(t, err)
	defer reopened.Close()
	restored := sessionEnv{
		Home: home, SQL: reopened, SessStore: state.NewSessionStore(reopened, "main"),
		ActionSvc: &state.ActionService{DB: reopened}, RunSvc: &state.RunStore{DB: reopened},
	}.session()

	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.composerSuppressed = true
	ctrl := newCommandController(restored, renderer, &stubSelector{}, nil, "")
	ctrl.printSessionResumeContext("s1")

	cards, lines := 0, 0
	lineIndex, cardIndex := -1, -1
	for i, block := range renderer.vm.blocks {
		if block.frame.Kind == FrameTool && block.frame.Title == "exit_plan_mode" {
			cards++
			cardIndex = i
			if block.frame.ToolMeta.Status != "canceled" {
				t.Fatalf("replayed card status = %q, want canceled", block.frame.ToolMeta.Status)
			}
		}
		if block.frame.Kind == FrameStatus && strings.Contains(block.frame.Content, "You canceled forebrain's request to exit plan mode") {
			lines++
			lineIndex = i
		}
	}
	if cards != 1 || lines != 1 {
		t.Fatalf("resume replayed %d cards and %d confirmation lines, want one of each: %#v", cards, lines, renderer.vm.blocks)
	}
	if lineIndex > cardIndex {
		t.Fatalf("resume put the confirmation below the card it answers: line=%d card=%d", lineIndex, cardIndex)
	}
}

// A subagent's confirmation belongs in that subagent's own view, beside the call
// it authorises - the conversation never showed the request it answers. Replay
// routes it the same way the live surface did, by the agent the record carries.
func TestResumeKeepsASubagentConfirmationInItsOwnView(t *testing.T) {
	const agentID = "agent-42"
	events := []event.RunEvent{
		event.NewRunEvent("spawn", "child-1", "s1", event.RunEventSubagentSpawned, event.SubagentSpawnedPayload{
			AgentID: agentID, AgentType: "explore", TaskID: agentID, Task: "run the tests", ExecutionID: "exec-1",
		}, time.Unix(100, 0).UTC()),
		event.NewRunEvent("approval-resolved:act-1:approved", "child-1", "s1", event.RunEventApprovalResolved,
			event.ApprovalResolvedPayload{
				ActionID: "act-1", ActionKind: "shell", Decision: "approved", AgentID: agentID, SubagentType: "explore",
				Confirmation: "✔ You approved forebrain to always run go test ./...",
			}, time.Unix(101, 0).UTC()),
	}

	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.composerSuppressed = true
	replayTimelineWithReducer(renderer, []state.Message{{RowID: 1, Role: "user", Content: "run the tests", CreatedAt: 99}}, events, &Reducer{}, nil)

	vm := renderer.perAgentVM[agentID]
	if vm == nil {
		t.Fatal("the subagent view was never restored")
	}
	lines := 0
	for _, block := range vm.blocks {
		if block.frame.Kind == FrameStatus && strings.Contains(block.frame.Content, "You approved forebrain to always run") {
			lines++
		}
	}
	if lines != 1 {
		t.Fatalf("subagent view holds %d confirmations, want 1: %#v", lines, vm.blocks)
	}
	for _, block := range renderer.vm.blocks {
		if block.frame.Kind == FrameStatus && strings.Contains(block.frame.Content, "You approved forebrain") {
			t.Fatalf("the subagent's confirmation leaked into the conversation: %#v", block.frame)
		}
	}
}

// TestReplayTimelineRebuildsCompactionCards pins that a resumed session shows
// each compaction card the live conversation finished: the done card right
// after its checkpoint with the banner the live card ended on, a pre-turn one
// after the message it made room for, and a cancelled one as cancelled. The
// events that only drove the running card replay nothing.
func TestReplayTimelineRebuildsCompactionCards(t *testing.T) {
	require := require.New(t)
	checkpoint := func(windowID string) state.Message {
		part := state.CompactBoundaryPart{Trigger: "auto", Strategy: "local", WindowID: windowID,
			ReplacementHistory: []llm.Message{llm.UserMessage(llm.Text("x"))}}
		return state.Message{Role: "system", Content: "summary", PartsJSON: state.PartsJSONWithCompactPart("summary", state.EncodeCompactBoundaryPart(part)), CreatedAt: 20}
	}
	turns := []state.Message{
		{Role: "user", Content: "first question", CreatedAt: 10},
		{Role: "assistant", Content: "first answer", CreatedAt: 10},
		checkpoint("w-1"),
		{Role: "user", Content: "second question", CreatedAt: 21},
		{Role: "assistant", Content: "second answer", CreatedAt: 22},
	}
	at := time.Unix(20, 0)
	events := []event.RunEvent{
		event.NewRunEvent("e1", "", "s", event.RunEventContextCompacting, event.ContextCompactingPayload{CompactionID: "c1", Trigger: "auto", TokensBefore: 182_400}, at),
		event.NewRunEvent("e2", "", "s", event.RunEventContextCompactProgress, event.ContextCompactProgressPayload{CompactionID: "c1", Percent: 50}, at),
		event.NewRunEvent("e3", "", "s", event.RunEventContextCompacted, event.ContextCompactedPayload{CompactionID: "c1", Trigger: "auto", BoundaryID: "w-1", TokensBefore: 182_400, TokensAfter: 12_300, Duration: "14.2s"}, at),
		event.NewRunEvent("e4", "", "s", event.RunEventContextCompactError, event.ContextCompactFailedPayload{CompactionID: "c2", Trigger: "manual", Cancelled: true}, time.Unix(30, 0)),
	}
	for i := range events {
		events[i].Sequence = int64(i + 1)
	}
	renderer := NewRenderer(nil, nil)
	renderer.EnableViewportMode()
	t.Cleanup(renderer.DisableViewportMode)
	reducer := &Reducer{}
	replayTimelineWithReducer(renderer, turns, events, reducer, nil)
	flushReplayReducer(renderer, reducer)

	var order []string
	for _, b := range renderer.vm.blocks {
		switch b.frame.Kind {
		case FrameUser:
			order = append(order, "user:"+b.frame.Content)
		case FrameAssistant:
			order = append(order, "assistant:"+strings.TrimSpace(b.frame.Content))
		case FrameMemoryCompact:
			require.True(b.frame.Final, "a replayed compaction card is always finished: %#v", b.frame)
			order = append(order, b.frame.Title+":"+b.frame.Summary)
		}
	}
	require.Equal([]string{
		"user:first question", "assistant:first answer",
		"user:second question",
		compactTitleDone + ":182.4k → 12.3k tokens (−93%) · 14.2s",
		"assistant:second answer",
		compactTitleCancelled + ":",
	}, order)
}

// A resumed session draws a /goal exactly as it was drawn live: the goal's
// opening right after the message that asked for it, each round's line before
// that round's answer, and the goal's end after its last round and before the
// user's next message. The runtime's continuation prompt between the rounds
// (row 3) is meta, which SurfaceTranscriptMessages leaves out of the rows a
// replay is given; the round's line still lands where that row was.
func TestReplayTimelineDrawsAGoalWhereItHappened(t *testing.T) {
	require := require.New(t)
	base := time.Unix(1_700_000_000, 0)
	row := func(id int64, role string, msg llm.Message, at time.Duration) state.Message {
		return state.Message{RowID: id, Role: role, Content: msg.TextContent(), PartsJSON: state.MessagePartsJSON(msg, msg.TextContent()), CreatedAt: base.Add(at).Unix()}
	}
	turns := []state.Message{
		row(1, "user", llm.UserMessage(llm.Text("/goal ship")), 0),
		row(2, "assistant", llm.AssistantMessage([]llm.ContentPart{llm.Text("round one answer")}), 2*time.Second),
		row(4, "assistant", llm.AssistantMessage([]llm.ContentPart{llm.Text("round two answer")}), 6*time.Second),
		row(5, "user", llm.UserMessage(llm.Text("thanks")), 60*time.Second),
	}
	events := []event.RunEvent{
		event.NewRunEvent("goal-started:r1", "r1", "s1", event.RunEventGoalStarted, event.GoalStartedPayload{Objective: "ship", AfterRowID: 1}, base),
		event.NewRunEvent("goal-round:r1:2", "r1", "s1", event.RunEventGoalRoundStarted, event.GoalRoundStartedPayload{Round: 2, Why: "two tests fail", CheckAgentID: "check-1", AfterRowID: 2}, base.Add(3*time.Second)),
		event.NewRunEvent("goal-completed:r1", "r1", "s1", event.RunEventGoalCompleted, event.GoalCompletedPayload{Objective: "ship", Status: event.GoalStatusDone, Rounds: 2, Why: "all pass", DurationMs: 8_000, CheckAgentID: "check-2"}, base.Add(8*time.Second)),
	}

	renderer := NewRenderer(nil, nil)
	renderer.EnableViewportMode()
	t.Cleanup(renderer.DisableViewportMode)
	reducer := &Reducer{}
	replayTimelineWithReducer(renderer, turns, events, reducer, nil)
	flushReplayReducer(renderer, reducer)

	var order []string
	for i := range renderer.vm.blocks {
		frame := renderer.vm.blocks[i].frame
		switch frame.Kind {
		case FrameUser, FrameAssistant:
			order = append(order, strings.TrimSpace(frame.Content))
		case FrameGoal:
			order = append(order, frame.Title)
			if frame.Title == "Round 2" {
				require.Equal("check-1", frame.AgentID)
			}
		}
	}
	require.Equal([]string{"/goal ship", goalTitleStarted, "round one answer", "Round 2", "round two answer", goalTitleDone, "thanks"}, order)
}

// A finished import's report is a frame in the conversation, the same frame
// /resume replays from the recorded event.
func TestMigrationReportIsTheFrameReplayDraws(t *testing.T) {
	require := require.New(t)
	evt := event.NewRunEvent("migration-completed:1", "", "s1", event.RunEventMigrationCompleted,
		event.MigrationCompletedPayload{Source: "claude", Title: "Migration complete", Report: "Migrated from Claude Code\n  Conversations: 1 imported"}, time.Now())
	replayed := replayMigrationEventFrames(evt)
	require.Len(replayed, 1)
	live := migrationReportFrame("Migration complete", "Migrated from Claude Code\n  Conversations: 1 imported")
	require.Equal(live, replayed[0])
	require.Equal(FrameSystem, live.Kind)
	require.True(live.Final)
}

// Every run a resumed transcript holds closes with its "Worked for" line, as it
// did live: after the run's own answer, before the next turn, however short.
func TestResumeClosesEveryRunWithItsWorkedLine(t *testing.T) {
	finished := time.Date(2026, 9, 25, 7, 27, 28, 0, time.UTC)
	turns := []state.Message{
		{RowID: 1, Role: "user", Content: "first", CreatedAt: 99},
		{RowID: 2, RunID: "r1", Role: "assistant", Content: "first answer", CreatedAt: 100, RunWorkedMs: 800, RunFinishedAtMs: finished.UnixMilli()},
		{RowID: 3, Role: "user", Content: "second", CreatedAt: 110},
		{RowID: 4, RunID: "r2", Role: "assistant", Content: "second answer", CreatedAt: 111, RunWorkedMs: 72_000, RunFinishedAtMs: finished.Add(time.Minute).UnixMilli()},
	}
	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.composerSuppressed = true
	reducer := &Reducer{}
	replayTimelineWithReducer(renderer, turns, nil, reducer, nil)
	flushReplayReducer(renderer, reducer)

	var got []string
	for _, block := range renderer.vm.blocks {
		switch block.frame.Kind {
		case FrameAssistant:
			got = append(got, block.frame.Content)
		case FrameStatus:
			got = append(got, block.frame.Title)
		}
	}
	want := []string{
		"first answer", "Worked for 1s · " + finished.Local().Format("15:04"),
		"second answer", "Worked for 1m 12s · " + finished.Add(time.Minute).Local().Format("15:04"),
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("replay = %q\nwant   %q", got, want)
	}
}

// TestReplayWorkedLinePerRun pins the "Worked for" closing line of replayed
// runs: one line per run, its duration from the run's worked time, and its
// completion minute from the run's finish time — for a run whose rows carry
// the run id, and for a tool row that belongs to the same run. T4 moves where
// these numbers live (fb_runs instead of every message row); this asserts the
// replayed line itself does not move.
func TestReplayWorkedLinePerRun(t *testing.T) {
	turns := []state.Message{
		{RowID: 1, Role: "user", Content: "review the diff"},
		{RowID: 2, RunID: "run-1", Role: "assistant", Content: "checked", RunWorkedMs: 90_000,
			RunFinishedAtMs: 1790503290000},
		{RowID: 3, RunID: "run-1", Role: "tool", Content: "ok", RunWorkedMs: 90_000,
			RunFinishedAtMs: 1790503290000},
		{RowID: 4, Role: "user", Content: "next"},
	}
	lines := turn.RunWorkedLines(turns)
	line, ok := lines[2]
	if !ok {
		t.Fatal("the tool row closing run-1 must produce its Worked-for line")
	}
	frame := replayWorkedFrame(line)
	if frame.Kind != FrameStatus || frame.RunID != "run-1" || !frame.Final {
		t.Fatalf("worked frame = %+v", frame)
	}
	if !strings.Contains(frame.Title, "1m30s") && !strings.Contains(frame.Title, "90s") && !strings.Contains(frame.Title, "1m") {
		t.Fatalf("worked title = %q, want the run's 90s duration", frame.Title)
	}
	if !strings.Contains(frame.Title, ":01") {
		t.Fatalf("worked title = %q, want the finish minute (local tz)", frame.Title)
	}
	// The assistant row inside the same run must not add a second line.
	if _, ok := lines[1]; ok {
		t.Fatal("a mid-run row must not close the run twice")
	}
	// A row with no run id has no line (rows before run binding existed get
	// their run id backfilled by the migration; the unbackfilled ones never
	// had one to show).
	if len(turn.RunWorkedLines([]state.Message{{RowID: 9, Role: "assistant", RunWorkedMs: 1000}})) != 0 {
		t.Fatal("a row outside any run has no Worked-for line")
	}
}

// A run that failed before it said anything still closed live: its error, then
// its "Worked for" line. The user's message is the run's only row, and replay
// closes the run after it in that same order.
func TestReplayClosesASilentFailedRunAfterItsError(t *testing.T) {
	finished := time.Date(2026, 9, 25, 7, 27, 28, 0, time.UTC)
	turns := []state.Message{
		{RowID: 1, RunID: "r1", Role: "user", Content: "hello", CreatedAt: 100, RunWorkedMs: 2_000, RunFinishedAtMs: finished.UnixMilli()},
		{RowID: 2, Role: "user", Content: "again", CreatedAt: 200},
	}
	payload, err := json.Marshal(event.TurnErrorPayload{Error: "the provider refused the request", Message: "the provider refused the request"})
	if err != nil {
		t.Fatal(err)
	}
	events := []event.RunEvent{{RunID: "r1", Type: event.RunEventTurnError, Payload: payload, CreatedAt: time.Unix(150, 0), Sequence: 1}}
	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.composerSuppressed = true
	reducer := &Reducer{}
	replayTimelineWithReducer(renderer, turns, events, reducer, nil)
	flushReplayReducer(renderer, reducer)

	var got []string
	for _, block := range renderer.vm.blocks {
		switch block.frame.Kind {
		case FrameUser:
			got = append(got, "user:"+block.frame.Content)
		case FrameError:
			got = append(got, "error")
		case FrameStatus:
			got = append(got, block.frame.Title)
		}
	}
	want := []string{"user:hello", "error", "Worked for 2s · " + finished.Local().Format("15:04"), "user:again"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("replay = %q\nwant   %q", got, want)
	}
}

// TestReplayRendersTheSessionFactSpread is T5's TUI protection net. It replays
// one conversation carrying every kind of fact the event merge touches — a main
// tool call, a main plan update, a subagent's lifecycle and its own tool call,
// a goal, and an approval — and pins what the replay draws. The storage behind
// those facts changes when the run-step ledger merges into the session event
// log; the frames a user sees must not.
func TestReplayRendersTheSessionFactSpread(t *testing.T) {
	callParts := state.MessagePartsJSON(llm.AssistantMessage(nil, llm.ToolCall{
		ID: "call-1", Type: llm.ToolTypeFunction,
		Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"/repo/a.go"}`},
	}), "")
	body := "file body"
	toolParts := state.MessagePartsJSON(llm.ToolResultMessage("call-1", llm.Text(body)), body)
	metaJSON, err := json.Marshal(tool.ToolMeta{ToolName: "read_file", Status: "completed"})
	require.NoError(t, err)
	turns := []state.Message{
		{RowID: 1, Role: "user", Content: "review the diff"},
		{RowID: 2, RunID: "run-1", Role: "assistant", PartsJSON: callParts},
		{RowID: 3, RunID: "run-1", Role: "tool", Content: body, PartsJSON: toolParts, ToolMetaJSON: string(metaJSON)},
		{RowID: 4, RunID: "run-1", Role: "assistant", Content: "done"},
	}
	now := time.Now().UTC()
	valid := func(typ string, payload any) event.RunEvent {
		raw, _ := json.Marshal(payload)
		return event.RunEvent{ID: "e-" + typ, RunID: "run-1", SessionID: "s1", Type: typ, Payload: raw, CreatedAt: now}
	}
	events := []event.RunEvent{
		valid(event.RunEventSubagentSpawned, event.SubagentSpawnedPayload{AgentID: "agent-7", AgentType: "verification", Task: "check it", ParentToolCallID: "call-1"}),
		valid(event.RunEventSubagentEnded, event.SubagentEndedPayload{AgentID: "agent-7", AgentType: "verification", Status: "completed", Output: "looks good", ParentToolCallID: "call-1"}),
		valid(event.RunEventGoalStarted, event.GoalStartedPayload{Objective: "ship it"}),
		valid(event.RunEventApprovalReq, event.ApprovalRequestedPayload{ActionID: "act-1", ActionKind: "shell", ToolStepID: "call-1", Message: "run the tests?"}),
	}

	renderer := NewRenderer(nil, nil)
	renderer.EnableViewportMode()
	t.Cleanup(renderer.DisableViewportMode)
	reducer := &Reducer{}
	replayTimelineWithReducer(renderer, turns, events, reducer, nil)
	flushReplayReducer(renderer, reducer)

	var kinds []string
	var blob strings.Builder
	for _, block := range renderer.vm.blocks {
		f := block.frame
		kinds = append(kinds, string(f.Kind))
		blob.WriteString(string(f.Kind) + "|" + f.Title + "|" + f.Content + "\n")
	}
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(blob.String()), "\n") {
		if len(line) > 160 {
			line = line[:160] + "…"
		}
		b.WriteString(line + "\n")
	}
	t.Logf("frames:\n%s", b.String())

	// The exact frames, in order. The merge changes where these facts are
	// stored, never what they draw.
	want := strings.Join([]string{
		"user|you|review the diff",
		"tool|read_file|file body",
		"assistant||done",
		// One card for the one execution, opened by the spawn and closed by
		// the end — the two lifecycle text lines it used to paint.
		"fanout||\x1b[32m✓\x1b[0m check it\n",
		"goal|Goal|ship it",
	}, "\n")
	require.Equal(t, want, strings.TrimSpace(blob.String()), "replayed frames changed; kinds=%v", kinds)
}

// A stored subagent_send row replays as the card its facts describe, not as
// the JSON body the row recorded: rows written before the card existed carry
// the old "output:\n```json" display, and replaying that verbatim would put the
// raw record back on screen.
func TestReplayedSubagentSendCardIsBuiltFromFacts(t *testing.T) {
	sendArgs := `{"title":"读 README","task":"阅读 README.md 并用三句话总结","subagent_type":"general-purpose"}`
	resultJSON := `{"task_id":"subagent-6e5c","run_id":"exec-1","parent_run_id":"run-9",` +
		`"status":"ok","started_at":1790000000,"agent_type":"general-purpose","agent_kind":"typed"}`
	toolRow := state.Message{
		RowID: 3, Role: "tool", Content: resultJSON,
		PartsJSON: state.MessagePartsJSON(llm.ToolResultMessage("call-send", llm.Text(resultJSON)), ""),
		CreatedAt: 101,
	}
	// The display part is the pre-card shape: the JSON envelope verbatim.
	parts := []map[string]any{
		{
			"type":           "tool_display",
			"body":           "output:\n\n```json\n{" + `\"output\":` + "\"\\\"" + strings.ReplaceAll(resultJSON, `"`, `\"`) + "\\\"}" + "}\n```",
			"summary":        "ran subagent_send",
			"tool_meta_json": `{"tool_name":"subagent_send","status":"completed"}`,
		},
	}
	raw, err := json.Marshal(parts)
	require.NoError(t, err)
	toolRow.PartsJSON = strings.TrimSuffix(toolRow.PartsJSON, "]") + "," + strings.TrimPrefix(string(raw), "[")

	turns := []state.Message{
		{RowID: 1, Role: "user", Content: "summarize the readme", CreatedAt: 99},
		assistantToolCallRow("dispatching", "call-send", "subagent_send", sendArgs),
		toolRow,
	}
	events := []event.RunEvent{
		event.NewRunEvent("spawn-1", "run-9", "s1", event.RunEventSubagentSpawned,
			event.SubagentSpawnedPayload{
				AgentID: "subagent-6e5c", AgentType: "general-purpose", TaskID: "subagent-6e5c",
				Title: "读 README", Task: "阅读 README.md 并用三句话总结",
				ParentToolCallID: "call-send", TaskIndex: 0, ExecutionID: "exec-1",
			}, time.Unix(100, 500).UTC()),
		event.NewRunEvent("end-1", "run-9", "s1", event.RunEventSubagentEnded,
			event.SubagentEndedPayload{
				AgentID: "subagent-6e5c", AgentType: "general-purpose", TaskID: "subagent-6e5c",
				Status: "ok", ParentToolCallID: "call-send", TaskIndex: 0, ExecutionID: "exec-1",
				FinishedAtMs: 1790000042000,
			}, time.Unix(102, 0).UTC()),
	}

	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.composerSuppressed = true
	replayTimelineWithReducer(renderer, turns, events, &Reducer{}, nil)

	var card *Frame
	for i := range renderer.vm.blocks {
		if renderer.vm.blocks[i].frame.Kind == FrameFanout {
			f := renderer.vm.blocks[i].frame
			card = &f
		}
	}
	if card == nil {
		t.Fatalf("the send call replayed without its card: %#v", renderer.vm.blocks)
	}
	if card.Summary != "Ran 1 general-purpose task in background" {
		t.Fatalf("summary = %q, want Ran 1 general-purpose task in background", card.Summary)
	}
	body := card.Content + card.FanoutCallError
	if strings.Contains(body, "agent_id") || strings.Contains(body, "{") {
		t.Fatalf("replayed card shows the stored JSON:\n%s", body)
	}
	if !strings.Contains(stripANSI(body), "读 README") {
		t.Fatalf("replayed card lacks the task name:\n%s", body)
	}
}

// TestReapedExecutionSettlesItsOwnCardOnLiveResume reproduces the order a
// killed process leaves behind: the resume's replay rebuilds the dispatch card
// from the stored rows and the spawned event, and only afterwards — once the
// dead owner's lease expires — does this process's reaper publish the
// execution's ended on the live event path. The live reducer must settle the
// replayed card the way the full replay would, not open an orphan titled by
// the agent type while the dispatch card ticks forever.
func TestReapedExecutionSettlesItsOwnCardOnLiveResume(t *testing.T) {
	sendArgs := `{"title":"慢读文件","task":"慢读多个文件逐步总结","subagent_type":"general-purpose"}`
	resultJSON := `{"task_id":"subagent-d09e","run_id":"child-1","parent_run_id":"run-1",` +
		`"status":"running","started_at":1791330674,"agent_type":"general-purpose"}`
	toolRow := state.Message{
		RowID: 3, Role: "tool", Content: resultJSON,
		PartsJSON: state.MessagePartsJSON(llm.ToolResultMessage("call-send", llm.Text(resultJSON)), ""),
		CreatedAt: 101,
	}
	display, err := json.Marshal([]map[string]any{{
		"type":           "tool_display",
		"body":           "已派发慢读文件",
		"summary":        "ran subagent_send",
		"tool_meta_json": `{"tool_name":"subagent_send","status":"completed"}`,
	}})
	require.NoError(t, err)
	toolRow.PartsJSON = strings.TrimSuffix(toolRow.PartsJSON, "]") + "," + strings.TrimPrefix(string(display), "[")
	turns := []state.Message{
		{RowID: 1, Role: "user", Content: "派发一个慢读任务，不用等它", CreatedAt: 99},
		assistantToolCallRow("派发这个慢速任务", "call-send", "subagent_send", sendArgs),
		toolRow,
	}
	spawnedAt := time.UnixMilli(1791330674000).UTC()
	reapedAt := spawnedAt.Add(9800 * time.Millisecond)
	// The first resume replays the transcript and the spawned event only: the
	// subagent was still running when its process died, so its ended event is
	// still in the future.
	events := []event.RunEvent{
		event.NewRunEvent("spawn-1", "child-1", "s1", event.RunEventSubagentSpawned,
			event.SubagentSpawnedPayload{
				AgentID: "subagent-d09e", AgentType: "general-purpose", TaskID: "subagent-d09e",
				Title: "慢读文件", Task: "慢读多个文件逐步总结",
				ParentToolCallID: "call-send", TaskIndex: 0, ExecutionID: "exec-1",
			}, spawnedAt),
	}
	session := &fakeSession{transcriptTurns: turns, sessionEvents: events}
	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.composerSuppressed = true
	ctrl := newCommandController(session, renderer, &stubSelector{}, nil, "")
	live := &Reducer{}
	ctrl.replayCardsTo = live
	ctrl.printSessionResumeContext("s1")

	// The reaper's ended arrives on the live path after the resume, carrying
	// the spawned event's identity and the run's stamped stop time. The live
	// loop renders what the reducer returns, so the test does too.
	liveResult := live.Reduce(SubagentEndedMsg{
		AgentID: "subagent-d09e", AgentType: "general-purpose", TaskID: "subagent-d09e",
		Status: "failed", Error: "The process running this turn stopped before it finished.",
		ParentToolCallID: "call-send", TaskIndex: 0, ExecutionID: "exec-1",
		FinishedAt: reapedAt, Timestamp: time.UnixMilli(1791330900000).UTC(),
	})
	for _, frame := range liveResult.Frames {
		renderer.RenderFrame(frame)
	}

	// The ended settles the dispatch's own card; an orphan keyed by the
	// execution is the mis-binding this test pins down.
	if orphan := live.findFanoutByStepID(standaloneExecStepID("exec-1", "subagent-d09e")); orphan != nil {
		t.Fatalf("the reaped end opened an orphan card: %#v", orphan.Tasks)
	}
	fs := live.findFanoutByStepID("call-send")
	if fs == nil {
		t.Fatal("the dispatch card is gone from the live reducer")
	}
	task := fs.Tasks[0]
	if task.Status != "failed" || task.Error != "The process running this turn stopped before it finished." {
		t.Fatalf("the dispatch task settled as status=%q error=%q", task.Status, task.Error)
	}
	if !task.StartedAt.Equal(spawnedAt) || !task.EndedAt.Equal(reapedAt) {
		t.Fatalf("task clock = %s..%s, want %s..%s (the stamped runtime, not the reap)",
			task.StartedAt, task.EndedAt, spawnedAt, reapedAt)
	}
	var cards []Frame
	for i := range renderer.vm.blocks {
		if renderer.vm.blocks[i].frame.Kind == FrameFanout {
			cards = append(cards, renderer.vm.blocks[i].frame)
		}
	}
	if len(cards) != 1 || cards[0].StepID != "call-send" {
		t.Fatalf("fanout cards on screen = %d (step ids %v), want exactly the dispatch card", len(cards), fanoutStepIDs(cards))
	}
	if body := stripANSI(cards[0].Content + cards[0].FanoutCallError); !strings.Contains(body, "✗ 慢读文件") ||
		!strings.Contains(body, "The process running this turn stopped before it finished.") {
		t.Fatalf("settled card lacks its outcome:\n%s", body)
	}

	// The same facts through the full replay — the second resume's path —
	// draw the very same card.
	fullEvents := append(append([]event.RunEvent(nil), events...),
		event.NewRunEvent("end-1", "child-1", "s1", event.RunEventSubagentEnded,
			event.SubagentEndedPayload{
				AgentID: "subagent-d09e", AgentType: "general-purpose", TaskID: "subagent-d09e",
				Status: "failed", Error: "The process running this turn stopped before it finished.",
				ParentToolCallID: "call-send", TaskIndex: 0, ExecutionID: "exec-1",
				FinishedAtMs: reapedAt.UnixMilli(),
			}, time.UnixMilli(1791330900000).UTC()))
	replayRenderer := NewRenderer(nil, nil)
	replayRenderer.viewportMode = true
	replayRenderer.composerSuppressed = true
	replayTimelineWithReducer(replayRenderer, turns, fullEvents, &Reducer{}, nil)
	var replayCard Frame
	replayCards := 0
	for i := range replayRenderer.vm.blocks {
		if replayRenderer.vm.blocks[i].frame.Kind == FrameFanout {
			replayCard = replayRenderer.vm.blocks[i].frame
			replayCards++
		}
	}
	if replayCards != 1 {
		t.Fatalf("full replay drew %d fanout cards, want 1", replayCards)
	}
	if cards[0].Summary != replayCard.Summary || cards[0].Content != replayCard.Content ||
		cards[0].Final != replayCard.Final || !equalFanoutClocks(cards[0].FanoutLineClocks, replayCard.FanoutLineClocks) {
		t.Fatalf("live-settled card drifted from the replayed one:\nlive:    %q / %q\nreplay:  %q / %q",
			cards[0].Summary, cards[0].Content, replayCard.Summary, replayCard.Content)
	}
}

func fanoutStepIDs(cards []Frame) []string {
	ids := make([]string, 0, len(cards))
	for i := range cards {
		ids = append(ids, cards[i].StepID)
	}
	return ids
}

func equalFanoutClocks(a, b []fanoutLineClock) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Start.Equal(b[i].Start) || !a[i].End.Equal(b[i].End) {
			return false
		}
	}
	return true
}

// A replayed run flushes its transcript rows together, after the subagent
// events of that turn — so a query row whose snapshot said "running" replays
// after the execution's end already did. The row must settle from that end and
// the header must still carry the call's own duration, byte-identical to the
// live card.
func TestReplayedQueryRowSettlesWhenItsEndCameFirst(t *testing.T) {
	statusArgs := `{"task_id":"subagent-6e5c"}`
	record := `{"task_id":"subagent-6e5c","agent_type":"general-purpose","title":"读 README",` +
		`"status":"running","execution_id":"exec-1","started_at":1790000000}`
	statusRow := state.Message{
		RowID: 3, Role: "tool", Content: record, ExecDurationMs: 16,
		PartsJSON: state.MessagePartsJSON(llm.ToolResultMessage("call-status", llm.Text(record)), ""),
		CreatedAt: 101,
	}
	turns := []state.Message{
		{RowID: 1, Role: "user", Content: "check on it", CreatedAt: 99},
		assistantToolCallRow("checking", "call-status", "subagent_status", statusArgs),
		statusRow,
	}
	events := []event.RunEvent{
		event.NewRunEvent("end-1", "exec-1", "s1", event.RunEventSubagentEnded,
			event.SubagentEndedPayload{
				AgentID: "subagent-6e5c", AgentType: "general-purpose", TaskID: "subagent-6e5c",
				Status: "ok", ExecutionID: "exec-1", FinishedAtMs: 1790000042000,
			}, time.Unix(102, 0).UTC()),
	}

	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.composerSuppressed = true
	replayTimelineWithReducer(renderer, turns, events, &Reducer{}, nil)

	var card *Frame
	for i := range renderer.vm.blocks {
		if f := renderer.vm.blocks[i].frame; f.Kind == FrameFanout && f.StepID == "call-status" {
			card = &renderer.vm.blocks[i].frame
		}
	}
	if card == nil {
		t.Fatalf("the status call replayed without its card: %#v", renderer.vm.blocks)
	}
	if card.Summary != "Checked 1 general-purpose task" {
		t.Fatalf("summary = %q, want Checked 1 general-purpose task", card.Summary)
	}
	if card.Duration != 16*time.Millisecond {
		t.Fatalf("replayed call duration = %v, want the row's 16ms", card.Duration)
	}
	rendered := strings.Join(renderFanoutForTest(*card), "\n")
	if !strings.Contains(rendered, "Checked 1 general-purpose task · <0.1s") {
		t.Fatalf("replayed header lost the call duration:\n%s", rendered)
	}
	if !strings.Contains(rendered, "✓ 读 README · 42s") {
		t.Fatalf("replayed query row did not settle with its execution's end:\n%s", rendered)
	}
}
