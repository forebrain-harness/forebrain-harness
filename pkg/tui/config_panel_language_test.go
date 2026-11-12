package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// configPanelNonEnglishRune answers the first CJK or fullwidth rune in text,
// and -1 when there is none. The ranges are what an East Asian input produces:
// CJK punctuation (U+3000–U+303F), unified ideographs (U+4E00–U+9FFF) and the
// fullwidth forms (U+FF00–U+FFEF) — the three the owner named when the panel
// became English-only.
func configPanelNonEnglishRune(text string) (int, rune) {
	for i, r := range text {
		switch {
		case r >= 0x3000 && r <= 0x303F, r >= 0x4E00 && r <= 0x9FFF, r >= 0xFF00 && r <= 0xFFEF:
			return i, r
		}
	}
	return -1, 0
}

// TestConfigPanelSaysEverythingInEnglish is the panel's language gate (owner
// 2026-11-12: 「TUI /config面板必须全英文」). Everything the panel puts on the
// screen is English, and this is what keeps it that way: the descriptor table
// structurally, then every string literal in the panel's own two source files,
// so a new constant cannot arrive in Chinese just by not being listed here.
//
// It does not scan a rendered page, and that is deliberate: a rendered page
// carries the user's own data — agent ids, subagent descriptions, model names,
// and whatever a section's values happen to hold — and those may legitimately
// be Chinese. The rule is about the panel's own vocabulary, not about the
// configuration it displays. For the same reason it reads literals rather than
// file text: comments in these files may stay Chinese, and are not the UI.
func TestConfigPanelSaysEverythingInEnglish(t *testing.T) {
	check := func(what, text string) {
		t.Helper()
		if _, r := configPanelNonEnglishRune(text); r != 0 {
			t.Errorf("%s carries %q (U+%04X) — the /config panel is English-only: %q", what, r, r, text)
		}
	}

	for _, schema := range configSectionSchemas {
		check("section "+schema.Name+" title", schema.Title)
		check("section "+schema.Name+" summary", schema.Summary)
		check("section "+schema.Name+" tab", schema.Tab)
		for _, field := range schema.Fields {
			check(schema.Name+" field label "+field.Label, field.Label)
			check(schema.Name+" field hint of "+field.Label, field.Hint)
			// The keys are stored values and never shown; only the labels are.
			for stored, label := range field.OptionLabels {
				check(schema.Name+" option label "+stored, label)
			}
		}
	}

	// The chrome around the table: the fixed rows, the notices and the
	// three-state words. Named so a failure points at the constant rather than
	// at a line number that moves.
	chrome := map[string]string{
		"configSaveRowLabel":   configSaveRowLabel,
		"configBackRowLabel":   configBackRowLabel,
		"configAddRowLabel":    configAddRowLabel,
		"configUpRowLabel":     configUpRowLabel,
		"configRowEditLabel":   configRowEditLabel,
		"configRowOpenLabel":   configRowOpenLabel,
		"configRowDropLabel":   configRowDropLabel,
		"configRowBackLabel":   configRowBackLabel,
		"configSavedNotice":    configSavedNotice,
		"configFileOnlyNote":   configFileOnlyNote,
		"configBoolOnLabel":    configBoolOnLabel,
		"configBoolOffLabel":   configBoolOffLabel,
		"configBoolUnsetLabel": configBoolUnsetLabel,
		"configUnsetLabel":     configUnsetLabel,
		"configNewTextRow":     configNewTextRow,
		"configNewNumberRow":   configNewNumberRow,
		"configNewTrueRow":     configNewTrueRow,
		"configNewFalseRow":    configNewFalseRow,
		"configNewObjectRow":   configNewObjectRow,
	}
	for name, text := range chrome {
		check(name, text)
	}

	// The sweep: no string literal in the panel's own files may carry one of
	// those runes. This is the half that cannot rot — a constant added later is
	// covered without being named above.
	set := token.NewFileSet()
	for _, path := range []string{"config_panel.go", "config_panel_schema.go"} {
		file, err := parser.ParseFile(set, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if _, r := configPanelNonEnglishRune(value); r != 0 {
				t.Errorf("%s:%d carries %q (U+%04X) in a string literal — the /config panel is English-only: %q",
					path, set.Position(lit.Pos()).Line, r, r, value)
			}
			return true
		})
	}
}
