package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// tuiMarkerGlyphs are the marks the transcript already uses to say what a row
// is or what state it is in. A deliverable glyph that repeated one of them
// would read as that meaning instead of "this file is a spreadsheet".
var tuiMarkerGlyphs = []string{
	toolRunning,            // ○  running tool / waiting
	toolComplete,           // ●  completed tool / system card / worked line
	selectorCheckedGlyph,   // ●  a chosen option
	selectorUncheckedGlyph, // ○  an unchosen one
	"✗",                    // failed
	"•",                    // bullet
	"▤",                    // image marker row
	"◆",                    // plan
	"□",                    // plan item open
	"■",                    // stopped
	"▣",                    // plan item active
	"✓",                    // plan item / goal done
	"✕",                    // plan item dropped
	"▸",                    // collapsed thinking
	"◎",                    // goal started
	"⚠",                    // warning
}

// TestDeliverableGlyphsStayClearOfTUIMarkers keeps the engine's glyph table
// and the terminal's own marks disjoint. The engine cannot import the
// rendering layer, so the check lives here, where both are visible.
func TestDeliverableGlyphsStayClearOfTUIMarkers(t *testing.T) {
	reserved := make(map[string]bool, len(tuiMarkerGlyphs))
	for _, glyph := range tuiMarkerGlyphs {
		reserved[glyph] = true
	}
	for _, kind := range []string{
		"document", "spreadsheet", "presentation", "pdf", "image", "video", "audio",
		"html", "markdown", "text", "archive", "design", "cad", "other",
	} {
		glyph := tool.ArtifactKindGlyph(kind)
		if reserved[glyph] {
			t.Errorf("kind %q draws %q, which is one of the transcript's own marks", kind, glyph)
		}
	}
}

// TestDeliverableGlyphWidthIsMeasured pins that the row is laid out from the
// glyph's measured width, never from an assumed one: ▶, ¶ and friends are
// ambiguous-width, so a terminal may draw them one cell or two, and every row
// must still fit the width it was wrapped to. A glyph measured as zero columns
// (a combining mark) would silently shift the whole row.
func TestDeliverableGlyphWidthIsMeasured(t *testing.T) {
	for kind, glyph := range artifactKindsForTest() {
		if w := lipgloss.Width(glyph); w < 1 {
			t.Fatalf("%s glyph %q measures %d columns", kind, glyph, w)
		}
	}
	f := Frame{Kind: FrameDeliverables, Deliverables: []tool.Deliverable{
		{Title: strings.Repeat("很长的中文标题", 6), Kind: "presentation", RelPath: "输出/季度回顾.pptx", Size: 1259520},
		{Title: "ddl.sql", Kind: "text", RelPath: "db/ddl.sql", Size: 288},
	}}
	for _, width := range []int{40, 80} {
		lines, _ := renderFrameLinesWithAgents(f, width, DiffThemeDark, 0)
		if len(lines) < 2 {
			t.Fatalf("width %d rendered %d lines", width, len(lines))
		}
		for i, line := range lines {
			if w := lipgloss.Width(line); w > width {
				t.Fatalf("width %d: row %d is %d columns: %q", width, i, w, line)
			}
		}
		// The wrapped rows of the long title continue one cell further in than
		// its first row, which is what the fold counts files by.
		plain := make([]string, len(lines))
		for i, line := range lines {
			plain[i] = sgrPattern.ReplaceAllString(line, "")
		}
		if !startsDeliverableRow(plain[0]) {
			t.Fatalf("width %d: first row %q is not a deliverable row", width, plain[0])
		}
		if startsDeliverableRow(plain[1]) {
			t.Fatalf("width %d: wrapped row %q looks like a new deliverable", width, plain[1])
		}
	}
}

// artifactKindsForTest lists the classes the glyph table covers, so the width
// check walks the same set the coverage test does.
func artifactKindsForTest() map[string]string {
	out := map[string]string{}
	for _, kind := range []string{
		"document", "spreadsheet", "presentation", "pdf", "image", "video", "audio",
		"html", "markdown", "text", "archive", "design", "cad", "other",
	} {
		out[kind] = tool.ArtifactKindGlyph(kind)
	}
	return out
}
