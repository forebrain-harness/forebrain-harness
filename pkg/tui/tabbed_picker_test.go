package tui

import (
	"reflect"
	"strings"
	"testing"
)

func tabbedFixture() []SelectItem {
	return []SelectItem{
		{Label: "alpha", Description: "First skill.", Category: "Project"},
		{Label: "beta-long-name", Description: "A description long enough to wrap onto a second row at this width.", Category: "Project"},
		{Label: "gamma", Description: "Shared one.", Category: "Shared"},
		{Label: "Add a skill", Description: "Install one.", Category: "Manage"},
	}
}

// The tabbed picker's whole shape in one golden: the label, the tab bar with
// counts, the filter, every row in two columns with the description wrapping
// inside its column, and the keys — never a dash between name and description,
// never an ellipsis standing in for content.
func TestTabbedPickerPanelGolden(t *testing.T) {
	p := newTabbedPickerState(tabbedFixture(), TabbedSelectOptions{Uncounted: []string{"Manage"}}, false, "")
	layout := p.panel("Skills\nRun or manage skills").layout(80, 0, 0, false)
	got := make([]string, len(layout.lines))
	for i, line := range layout.lines {
		got[i] = stripANSI(line)
	}
	want := []string{
		"  Skills",
		"  Run or manage skills",
		"  Project 2   Shared 1   Manage",
		"› Type to filter",
		"",
		"❯ alpha" + strings.Repeat(" ", 11) + "First skill.",
		"  beta-long-name  A description long enough to wrap onto a second row at this",
		strings.Repeat(" ", 18) + "width.",
		"",
		"  Tab to switch · ↑/↓ to navigate · Enter to confirm · Esc to cancel",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("panel =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, line := range got {
		if strings.Contains(line, " — ") {
			t.Fatalf("row glued name to description with a dash: %q", line)
		}
		if strings.Contains(line, "…") {
			t.Fatalf("row truncated with an ellipsis: %q", line)
		}
	}
}

// Every description starts in one column at every width, and every row fits
// the width it was laid out at — the one-physical-row invariant.
func TestTabbedPickerRowsFitAtEveryWidth(t *testing.T) {
	p := newTabbedPickerState(tabbedFixture(), TabbedSelectOptions{Uncounted: []string{"Manage"}}, false, "")
	for _, width := range []int{120, 80, 50, 30} {
		layout := p.panel("Skills\nRun or manage skills").layout(width, 0, 0, false)
		assertRowsFit(t, layout.lines, width)
		if width != 120 && width != 80 {
			continue
		}
		alphaAt, betaAt := -1, -1
		for _, line := range layout.lines {
			plain := stripANSI(line)
			if at := strings.Index(plain, "First skill."); at >= 0 {
				alphaAt = displayLineWidth(plain[:at])
			}
			if at := strings.Index(plain, "A description"); at >= 0 {
				betaAt = displayLineWidth(plain[:at])
			}
		}
		if alphaAt < 0 || betaAt < 0 || alphaAt != betaAt {
			t.Fatalf("width %d: descriptions not in one column: %d vs %d", width, alphaAt, betaAt)
		}
	}
}

// A filter the shown tab cannot answer moves the picker to the first tab that
// can, and a tab bar with no match anywhere keeps the tab while Tab finds
// nothing to switch to.
func TestTabbedPickerFilterMovesToATabWithMatches(t *testing.T) {
	p := newTabbedPickerState(tabbedFixture(), TabbedSelectOptions{Uncounted: []string{"Manage"}}, false, "")
	p.filter = filterField{text: "sha", cursor: 3}
	p.rebuild()
	if p.tabs[p.tab] != "Shared" || !reflect.DeepEqual(p.visible, []int{2}) || !reflect.DeepEqual(p.matches, []int{0, 1, 0}) {
		t.Fatalf("tab=%q visible=%v matches=%v", p.tabs[p.tab], p.visible, p.matches)
	}
	p.switchTab(1)
	if p.tabs[p.tab] != "Shared" {
		t.Fatalf("switched to %q with no matches anywhere", p.tabs[p.tab])
	}
	tabLine := stripANSI(p.panel("Skills\nRun or manage skills").layout(80, 0, 0, false).lines[2])
	if tabLine != "  Project 0   Shared 1   Manage" {
		t.Fatalf("tab line = %q", tabLine)
	}
}

// Tab switching wraps around the tab bar in both directions, landing on the
// first row every time.
func TestTabbedPickerSwitchWrapsAround(t *testing.T) {
	p := newTabbedPickerState(tabbedFixture(), TabbedSelectOptions{Uncounted: []string{"Manage"}}, false, "")
	p.switchTab(-1)
	if p.tab != 2 || p.cursor != 0 {
		t.Fatalf("after Shift+Tab: tab=%d cursor=%d, want Manage at cursor 0", p.tab, p.cursor)
	}
	p.switchTab(1)
	if p.tab != 0 || p.cursor != 0 {
		t.Fatalf("after Tab: tab=%d cursor=%d, want Project at cursor 0", p.tab, p.cursor)
	}
}

// The picker opens on the row the caller names — where the user left it — and
// falls back to the first row of the first tab when the name is absent or
// disabled.
func TestTabbedPickerOpensOnTheDefaultRow(t *testing.T) {
	p := newTabbedPickerState(tabbedFixture(), TabbedSelectOptions{DefaultIdx: 2}, false, "")
	if p.tabs[p.tab] != "Shared" || p.current() != 2 {
		t.Fatalf("DefaultIdx 2: tab=%q current=%d", p.tabs[p.tab], p.current())
	}
	p = newTabbedPickerState(tabbedFixture(), TabbedSelectOptions{DefaultIdx: -1}, false, "")
	if p.tab != 0 || p.current() != 0 {
		t.Fatalf("DefaultIdx -1: tab=%d current=%d, want the first row", p.tab, p.current())
	}
	fixture := tabbedFixture()
	fixture[2].Disabled = true
	p = newTabbedPickerState(fixture, TabbedSelectOptions{DefaultIdx: 2}, false, "")
	if p.tab != 0 || p.current() != 0 {
		t.Fatalf("disabled default: tab=%d current=%d, want the first row", p.tab, p.current())
	}
}

// Checks live on the row, not the tab: a check survives switching tabs and
// Space toggles whatever row the cursor is on in the tab it landed in.
func TestTabbedPickerMultiSelectChecksAcrossTabs(t *testing.T) {
	p := newTabbedPickerState(tabbedFixture(), TabbedSelectOptions{Checked: []int{0, 2}}, true, "")
	lines := p.panel("Skills\nRun or manage skills").layout(80, 0, 0, false).lines
	joined := ""
	for _, line := range lines {
		joined += stripANSI(line) + "\n"
	}
	if !strings.HasPrefix(joined, "  Skills\n") || !strings.Contains(joined, "❯ ● alpha") || !strings.Contains(joined, "  ○ beta-long-name") {
		t.Fatalf("multi rows missing their check boxes:\n%s", joined)
	}
	if !strings.Contains(joined, "Space to select") {
		t.Fatalf("multi hint missing Space: %q", joined)
	}
	if !reflect.DeepEqual(p.checkedIndices(), []int{0, 2}) {
		t.Fatalf("checked=%v", p.checkedIndices())
	}
	p.toggle()
	if !reflect.DeepEqual(p.checkedIndices(), []int{2}) {
		t.Fatalf("after first toggle checked=%v", p.checkedIndices())
	}
	p.switchTab(1)
	p.toggle()
	if !reflect.DeepEqual(p.checkedIndices(), []int{}) {
		t.Fatalf("after Shared toggle checked=%v", p.checkedIndices())
	}
	p.switchTab(1)
	p.toggle()
	if !reflect.DeepEqual(p.checkedIndices(), []int{3}) {
		t.Fatalf("after Manage toggle checked=%v", p.checkedIndices())
	}
}

// The fallback labels carry each row's tab, de-duplicated, because a plain
// MultiSelect cannot tell two same-named copies apart.
func TestTabbedFallbackLabelsAreUnique(t *testing.T) {
	labels := tabbedFallbackLabels([]SelectItem{
		{Label: "improve", Category: "Cross-tool"},
		{Label: "improve", Category: "Cross-tool"},
		{Label: "improve", Category: "Project"},
	})
	want := []string{"improve (Cross-tool)", "improve (Cross-tool) #2", "improve (Project)"}
	if !reflect.DeepEqual(labels, want) {
		t.Fatalf("labels=%v, want %v", labels, want)
	}
}

// A list with only one kind of row shows no tab bar: nothing to switch to.
func TestTabbedPickerSingleTabHasNoTabBar(t *testing.T) {
	items := []SelectItem{
		{Label: "release-check", Description: "Verify a release.", Category: "Memory"},
		{Label: "triage", Description: "Triage bugs.", Category: "Memory"},
	}
	p := newTabbedPickerState(items, TabbedSelectOptions{}, true, "")
	lines := p.panel("Import from Memory\nSkills written by memory consolidation.").layout(80, 0, 0, false).lines
	if got := stripANSI(lines[0]); got != "  Import from Memory" {
		t.Fatalf("title = %q", got)
	}
	if got := stripANSI(lines[1]); got != "  Skills written by memory consolidation." {
		t.Fatalf("subtitle = %q", got)
	}
	if got := stripANSI(lines[2]); got != "› Type to filter" {
		t.Fatalf("line 2 = %q, want the filter with no tab bar before it", got)
	}
	for _, line := range lines {
		if strings.Contains(stripANSI(line), "Tab to switch") {
			t.Fatalf("single tab advertises switching: %q", stripANSI(line))
		}
	}
}
