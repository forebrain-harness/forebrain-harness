package turn

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// composerTokenCase is one row of the shared composer tokenisation table. The
// same file drives the Go tokenisers here and the web composer's parsers in
// frontend/src/components/ComposerTokens.test.ts, so the two surfaces cannot
// drift on what a "/" or "@" token is.
type composerTokenCase struct {
	Draft   string `json:"draft"`
	Cursor  int    `json:"cursor"`
	Kind    string `json:"kind"`
	OK      bool   `json:"ok"`
	Start   int    `json:"start"`
	Query   string `json:"query"`
	AtStart bool   `json:"at_start"`
}

// loadComposerTokenCases reads testdata/composer_tokens.json and refuses a
// table too small to be a contract.
func loadComposerTokenCases(t *testing.T) []composerTokenCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "composer_tokens.json"))
	if err != nil {
		t.Fatalf("read composer token table: %v", err)
	}
	var cases []composerTokenCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("parse composer token table: %v", err)
	}
	if len(cases) < 16 {
		t.Fatalf("composer token table holds %d cases, want at least 16", len(cases))
	}
	return cases
}

// TestComposerTokenTable is the contract for both composers: every case in the
// shared table must resolve the same way here as it does in the web composer.
func TestComposerTokenTable(t *testing.T) {
	for _, tc := range loadComposerTokenCases(t) {
		tc := tc
		t.Run(fmt.Sprintf("%s_%d", tc.Kind, tc.Cursor), func(t *testing.T) {
			switch tc.Kind {
			case "/":
				start, query, atStart, ok := SlashTokenAtCursor(tc.Draft, tc.Cursor)
				if ok != tc.OK {
					t.Fatalf("SlashTokenAtCursor(%q, %d) ok = %v, want %v", tc.Draft, tc.Cursor, ok, tc.OK)
				}
				if ok && (start != tc.Start || query != tc.Query || atStart != tc.AtStart) {
					t.Fatalf("SlashTokenAtCursor(%q, %d) = (%d, %q, %v), want (%d, %q, %v)",
						tc.Draft, tc.Cursor, start, query, atStart, tc.Start, tc.Query, tc.AtStart)
				}
			case "@":
				start, query, ok := TokenAtCursor(tc.Draft, tc.Cursor)
				if ok != tc.OK {
					t.Fatalf("TokenAtCursor(%q, %d) ok = %v, want %v", tc.Draft, tc.Cursor, ok, tc.OK)
				}
				if ok && (start != tc.Start || query != tc.Query) {
					t.Fatalf("TokenAtCursor(%q, %d) = (%d, %q), want (%d, %q)",
						tc.Draft, tc.Cursor, start, query, tc.Start, tc.Query)
				}
			default:
				t.Fatalf("unknown token kind %q", tc.Kind)
			}
		})
	}
}

// slashAcceptCase is one row of the shared table of slash completions: the
// draft, the token range an accept replaces, the command name, and the draft
// and caret that result. Like composer_tokens.json, the same file drives the
// web composer's acceptSlash (frontend/src/lib/composerTokens.test.ts).
type slashAcceptCase struct {
	Draft  string `json:"draft"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Name   string `json:"name"`
	Want   string `json:"want"`
	Cursor int    `json:"cursor"`
}

// TestSlashAcceptTable is the contract for what completing a slash command
// does to the draft, on both composers.
func TestSlashAcceptTable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "composer_slash_accept.json"))
	if err != nil {
		t.Fatalf("read slash accept table: %v", err)
	}
	var cases []slashAcceptCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("parse slash accept table: %v", err)
	}
	if len(cases) < 8 {
		t.Fatalf("slash accept table holds %d cases, want at least 8", len(cases))
	}
	for _, tc := range cases {
		got, cursor := AcceptSlash(tc.Draft, tc.Start, tc.End, tc.Name)
		if got != tc.Want || cursor != tc.Cursor {
			t.Errorf("AcceptSlash(%q, %d, %d, %q) = (%q, %d), want (%q, %d)",
				tc.Draft, tc.Start, tc.End, tc.Name, got, cursor, tc.Want, tc.Cursor)
		}
	}
}
