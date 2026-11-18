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
		{lead: "  ", col: "Coding Plan Global", colWidth: 40, text: "coding-intl.dashscope.aliyuncs.com"},
	}}
	layout := p.layout(75, 0, 0, false)
	want := []string{
		"",
		"  Standard API Key for China (pay-as-you-go)",
		"  dashscope.aliyuncs.com",
		"  Coding Plan Global",
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

// A row's value and status are tracks of their own at the right edge, the value
// first — the /config panel's name, value and status (plans 313, 321): the
// description column narrows by both so they never meet, each is drawn whole on
// the first line, and a row with neither is byte-for-byte what it was.
func TestPanelColumnsDrawTheValueBeforeTheStatus(t *testing.T) {
	const width = 80
	withValue := panelLine{lead: "  ", col: "Auto-compact at", colWidth: 20, text: "Compacts when the window fills", value: "90 %", status: "Default"}
	rows := withValue.rows(width)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %q", len(rows), rows)
	}
	got := stripANSI(rows[0])
	if !strings.HasSuffix(got, "Default") {
		t.Fatalf("row = %q, want the status at its right edge", got)
	}
	if valueAt, statusAt := strings.Index(got, "90 %"), strings.Index(got, "Default"); valueAt < 0 || valueAt > statusAt {
		t.Fatalf("row = %q, want the value left of the status", got)
	}
	if w := displayLineWidth(got); w != width {
		t.Fatalf("row is %d cells wide at width %d: %q", w, width, got)
	}
	if !strings.Contains(got, "Compacts when the window fills") {
		t.Fatalf("row = %q, want the description in its column", got)
	}
	assertRowsFit(t, rows, width)

	// Both tracks are fixed: two rows with different values line their values
	// and their statuses up at the same columns.
	rowB := panelLine{lead: "  ", col: "Auto-compact at", colWidth: 20, text: "Compacts when the window fills", value: "60 %", status: "Default"}
	a, b := stripANSI(withValue.rows(width)[0]), stripANSI(rowB.rows(width)[0])
	if strings.Index(a, "Default") != strings.Index(b, "Default") || strings.Index(a, "90 %") != strings.Index(b, "60 %") {
		t.Fatalf("the tracks are not aligned:\n%q\n%q", a, b)
	}

	// A value longer than the track is elided, never pushed into the status.
	long := panelLine{lead: "  ", col: "Model", colWidth: 20, text: "hint", value: "a-very-long-model-reference-that-cannot-fit", status: "Default"}
	for _, row := range long.rows(width) {
		if !strings.Contains(stripANSI(row), "…") {
			t.Fatalf("a long value must be elided: %q", stripANSI(row))
		}
		assertRowsFit(t, []string{row}, width)
	}

	// A row with neither track is untouched.
	plain := panelLine{lead: "  ", col: "main", colWidth: 14, text: "4 items"}
	if plain.rows(width)[0] != (panelLine{lead: "  ", col: "main", colWidth: 14, text: "4 items"}).rows(width)[0] {
		t.Fatalf("a trackless row changed: %q", plain.rows(width))
	}

	// The picker's own entry point carries both through.
	builder := newPanelBuilder()
	builder.selectableColumnsValue(false, "", "Auto-compact at", 20, "hint", "", "90 %", "Default")
	if len(builder.body) != 1 || builder.body[0].value != "90 %" || builder.body[0].status != "Default" {
		t.Fatalf("the picker row carries no value: %+v", builder.body)
	}
	builder = newPanelBuilder()
	builder.selectableColumns(false, "", "main", 14, "4 items", "")
	if len(builder.body) != 1 || builder.body[0].value != "" || builder.body[0].status != "" {
		t.Fatalf("a trackless row picked one up: %+v", builder.body)
	}
}

// A row's status is a track of its own at the right edge — the /config
// definitions list's "Primary" (plan 313): the description column narrows by it
// so the two never meet, the status is drawn whole on the first line, and a row
// without one is byte-for-byte what it was.
func TestPanelColumnsDrawTheStatusAtTheRightEdge(t *testing.T) {
	const width = 80
	withStatus := panelLine{lead: "  ", col: "main", colWidth: 14, text: "4 items", status: "Primary"}
	rows := withStatus.rows(width)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %q", len(rows), rows)
	}
	got := stripANSI(rows[0])
	if !strings.HasSuffix(got, "Primary") {
		t.Fatalf("row = %q, want the status at its right edge", got)
	}
	if w := displayLineWidth(got); w != width {
		t.Fatalf("row is %d cells wide at width %d: %q", w, width, got)
	}
	if !strings.Contains(got, "4 items") {
		t.Fatalf("row = %q, want the description in its column", got)
	}
	assertRowsFit(t, rows, width)

	// Two rows: both statuses end at the same column, whatever their
	// descriptions are, because the track is fixed rather than pushed along.
	rowB := panelLine{lead: "  ", col: "review-agent", colWidth: 14, text: "2 items", status: "Primary"}
	if a, b := stripANSI(withStatus.rows(width)[0]), stripANSI(rowB.rows(width)[0]); strings.Index(a, "Primary") != strings.Index(b, "Primary") {
		t.Fatalf("statuses are not aligned:\n%q\n%q", a, b)
	}

	// A row without a status is untouched: it renders exactly like a plain row.
	plain := panelLine{lead: "  ", col: "main", colWidth: 14, text: "4 items"}
	bare := panelLine{lead: "  ", col: "main", colWidth: 14, text: "4 items"}
	if len(plain.rows(width)) != len(bare.rows(width)) || plain.rows(width)[0] != bare.rows(width)[0] {
		t.Fatalf("a statusless row changed: %q", plain.rows(width))
	}

	// A stacked row (name too wide for its column, narrow terminal) drops the
	// status instead of wrapping it onto a line of its own.
	stacked := panelLine{lead: "  ", col: "standard-api-key-for-china-pay-as-you-go", colWidth: 14, text: "dashscope", status: "Primary"}
	for _, row := range stacked.rows(20) {
		if strings.Contains(stripANSI(row), "Primary") {
			t.Fatalf("a stacked row must drop its status: %q", stripANSI(row))
		}
		assertRowsFit(t, []string{row}, 20)
	}

	// The picker's own entry point carries the status through.
	b := newPanelBuilder()
	b.selectableColumnsStatus(false, "", "main", 14, "4 items", "", "Primary")
	if len(b.body) != 1 || b.body[0].status != "Primary" {
		t.Fatalf("the picker row carries no status: %+v", b.body)
	}
	b = newPanelBuilder()
	b.selectableColumns(false, "", "main", 14, "4 items", "")
	if len(b.body) != 1 || b.body[0].status != "" {
		t.Fatalf("a statusless row picked one up: %+v", b.body)
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
