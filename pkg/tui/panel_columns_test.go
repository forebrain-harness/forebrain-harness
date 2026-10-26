package tui

import (
	"strings"
	"testing"
)

// The two-column row puts every description in one column: the name padded to
// the column's width, the description starting panelColumnGap cells right of
// it, whatever the name looks like.
func TestPanelColumnsAlignDescriptions(t *testing.T) {
	rows := panelLine{lead: "  ", col: "alpha", colWidth: 14, text: "First skill."}.rows(80)
	want := []string{"  alpha" + strings.Repeat(" ", 11) + "First skill."}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %q", len(rows), len(want), rows)
	}
	for i := range want {
		if got := stripANSI(rows[i]); got != want[i] {
			t.Fatalf("row %d = %q, want %q", i, got, want[i])
		}
	}
}

// A description longer than its column wraps inside the column, its
// continuation rows hanging under the column's left edge, never back under
// the name.
func TestPanelColumnsWrapInsideTheDescriptionColumn(t *testing.T) {
	rows := panelLine{
		lead:     "  ",
		col:      "beta-long-name",
		colWidth: 14,
		text:     "A description long enough to wrap onto a second row at this width.",
	}.rows(80)
	want := []string{
		"  beta-long-name  A description long enough to wrap onto a second row at this",
		strings.Repeat(" ", 18) + "width.",
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %q", len(rows), len(want), rows)
	}
	for i := range want {
		if got := stripANSI(rows[i]); got != want[i] {
			t.Fatalf("row %d = %q, want %q", i, got, want[i])
		}
	}
}

// A name that does not fit its column turns the row into a stacked pair: the
// name on its own rows, the description hung under the NAME — never indented
// to the description column, which would float it in the middle of the panel.
func TestPanelColumnsLongNameStacksUnderTheName(t *testing.T) {
	rows := panelLine{
		lead:     "  ",
		col:      "Standard API Key for China (pay-as-you-go)",
		colWidth: 29,
		text:     "dashscope.aliyuncs.com",
	}.rows(75)
	want := []string{
		"  Standard API Key for China (pay-as-you-go)",
		"  dashscope.aliyuncs.com",
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %q", len(rows), len(want), rows)
	}
	for i := range want {
		if got := stripANSI(rows[i]); got != want[i] {
			t.Fatalf("row %d = %q, want %q", i, got, want[i])
		}
	}
	assertRowsFit(t, rows, 75)
}

// The whole list stacks when any name cannot fit: a list must not mix a
// two-column row with a stacked one, or its left edge stops being one edge.
func TestPanelColumnsStackAsOneList(t *testing.T) {
	p := slashPanel{body: []panelLine{
		{lead: "  ", col: "Standard API Key for China (pay-as-you-go)", colWidth: 40, text: "dashscope.aliyuncs.com"},
		{lead: "  ", col: "Coding Plan · Global", colWidth: 40, text: "coding-intl.dashscope.aliyuncs.com"},
	}}
	layout := p.layout(75, 0, 0, false)
	want := []string{
		"",
		"  Standard API Key for China (pay-as-you-go)",
		"  dashscope.aliyuncs.com",
		"  Coding Plan · Global",
		"  coding-intl.dashscope.aliyuncs.com",
		"",
	}
	if len(layout.lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %q", len(layout.lines), len(want), layout.lines)
	}
	for i := range want {
		if got := stripANSI(layout.lines[i]); got != want[i] {
			t.Fatalf("line %d = %q, want %q", i, got, want[i])
		}
	}
	for i, line := range layout.lines {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(stripANSI(line), "  ") {
			t.Fatalf("line %d not hung under the name: %q", i, stripANSI(line))
		}
	}
	assertRowsFit(t, layout.lines, 75)
}

// Short names keep the two columns untouched at the same width.
func TestPanelColumnsKeepColumnsWhenEveryNameFits(t *testing.T) {
	p := slashPanel{body: []panelLine{
		{lead: "  ", col: "Standard API Key for China (pay-as-you-go)", colWidth: 42, text: "dashscope.aliyuncs.com"},
	}}
	layout := p.layout(120, 0, 0, false)
	want := []string{
		"",
		"  Standard API Key for China (pay-as-you-go)  dashscope.aliyuncs.com",
		"",
	}
	if len(layout.lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %q", len(layout.lines), len(want), layout.lines)
	}
	for i := range want {
		if got := stripANSI(layout.lines[i]); got != want[i] {
			t.Fatalf("line %d = %q, want %q", i, got, want[i])
		}
	}
	assertRowsFit(t, layout.lines, 120)
}

// A two-column row without a description is laid out exactly like a plain
// row, so a picker whose rows carry no descriptions (resume entries, model
// names) renders unchanged.
func TestPanelColumnsWithoutDescriptionMatchPlainRows(t *testing.T) {
	for _, col := range []string{"2h ago   a short title", strings.Repeat("word ", 18)} {
		line := panelLine{lead: "  ", col: col, colWidth: 40}
		plain := panelLine{lead: "  ", text: col}
		for _, width := range []int{80, 30} {
			got := line.rows(width)
			want := plain.rows(width)
			if len(got) != len(want) {
				t.Fatalf("width %d: got %d rows, want %d: %q", width, len(got), len(want), got)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("width %d col %q: row %d = %q, want %q", width, col, i, got[i], want[i])
				}
			}
		}
	}
}

// The tab bar wraps whole chips: a chip that no longer fits starts the next
// row, a tab's name and its count never part across rows.
func TestPanelChipsWrapWhole(t *testing.T) {
	line := panelLine{lead: "  ", chips: []string{"Project 2", "Shared 1", "Manage"}}
	rows := line.rows(80)
	if got := stripANSI(rows[0]); got != "  Project 2   Shared 1   Manage" {
		t.Fatalf("width 80 = %q", got)
	}
	rows = line.rows(30)
	if len(rows) != 2 {
		t.Fatalf("width 30: got %d rows: %q", len(rows), rows)
	}
	if got := stripANSI(rows[0]); got != "  Project 2   Shared 1" {
		t.Fatalf("width 30 row 0 = %q", got)
	}
	if got := stripANSI(rows[1]); got != "  Manage" {
		t.Fatalf("width 30 row 1 = %q", got)
	}
}
