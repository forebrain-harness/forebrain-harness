package lsp

import (
	"encoding/json"
	"testing"
)

func TestServerCapabilities(t *testing.T) {
	caps := ServerCapabilities{
		"hoverProvider":          json.RawMessage("true"),
		"definitionProvider":     json.RawMessage(`{"workDoneProgress":true}`),
		"referencesProvider":     json.RawMessage("null"),
		"documentSymbolProvider": json.RawMessage("false"),
	}
	supports := []struct {
		key  string
		want bool
	}{
		{"hoverProvider", true},
		{"definitionProvider", true},
		{"referencesProvider", false},
		{"documentSymbolProvider", false},
		{"callHierarchyProvider", false}, // missing
	}
	for _, tc := range supports {
		if got := caps.Supports(tc.key); got != tc.want {
			t.Fatalf("Supports(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}

	encoding := []struct {
		caps ServerCapabilities
		want string
	}{
		{ServerCapabilities{}, "utf-16"},
		{ServerCapabilities{"positionEncoding": json.RawMessage(`"utf-8"`)}, "utf-8"},
		{ServerCapabilities{"positionEncoding": json.RawMessage("null")}, "utf-16"},
	}
	for _, tc := range encoding {
		if got := tc.caps.PositionEncoding(); got != tc.want {
			t.Fatalf("PositionEncoding() = %q, want %q", got, tc.want)
		}
	}

	save := []struct {
		caps ServerCapabilities
		want bool
	}{
		{ServerCapabilities{"textDocumentSync": json.RawMessage(`{"save":{"includeText":true}}`)}, true},
		{ServerCapabilities{"textDocumentSync": json.RawMessage(`{"save":{"includeText":false}}`)}, false},
		{ServerCapabilities{"textDocumentSync": json.RawMessage(`{"save":{}}`)}, false},
		{ServerCapabilities{"textDocumentSync": json.RawMessage("1")}, false},
		{ServerCapabilities{}, false},
	}
	for _, tc := range save {
		if got := tc.caps.SaveIncludesText(); got != tc.want {
			t.Fatalf("SaveIncludesText(%s) = %v, want %v", tc.caps["textDocumentSync"], got, tc.want)
		}
	}

	folders := []struct {
		workspace json.RawMessage
		want      bool
	}{
		{json.RawMessage(`{"workspaceFolders":{"changeNotifications":true}}`), true},
		{json.RawMessage(`{"workspaceFolders":{"changeNotifications":"abc-1"}}`), true},
		{json.RawMessage(`{"workspaceFolders":{"changeNotifications":false}}`), false},
		{json.RawMessage(`{"workspaceFolders":{}}`), false},
		{json.RawMessage(`{}`), false},
	}
	for _, tc := range folders {
		caps := ServerCapabilities{"workspace": tc.workspace}
		if got := caps.WorkspaceFolderChanges(); got != tc.want {
			t.Fatalf("WorkspaceFolderChanges(%s) = %v, want %v", tc.workspace, got, tc.want)
		}
	}
	if (ServerCapabilities{}).WorkspaceFolderChanges() {
		t.Fatal("WorkspaceFolderChanges() = true with no workspace key, want false")
	}
}

func TestDecodeLocations(t *testing.T) {
	location := `{"uri":"file:///a.go","range":{"start":{"line":0,"character":1},"end":{"line":0,"character":4}}}`
	link := `{"originSelectionRange":{"start":{"line":0,"character":0},"end":{"line":0,"character":3}},"targetUri":"file:///b.go","targetRange":{"start":{"line":9,"character":0},"end":{"line":9,"character":9}},"targetSelectionRange":{"start":{"line":9,"character":2},"end":{"line":9,"character":6}}}`

	cases := []struct {
		name string
		raw  string
		want []Location
	}{
		{"null", `null`, nil},
		{"single", location, []Location{{
			URI:   "file:///a.go",
			Range: Range{Start: Position{0, 1}, End: Position{0, 4}},
		}}},
		{"array", `[` + location + `,` + location + `]`, []Location{
			{URI: "file:///a.go", Range: Range{Start: Position{0, 1}, End: Position{0, 4}}},
			{URI: "file:///a.go", Range: Range{Start: Position{0, 1}, End: Position{0, 4}}},
		}},
		{"links", `[` + link + `]`, []Location{{
			URI:   "file:///b.go",
			Range: Range{Start: Position{9, 2}, End: Position{9, 6}}, // the target selection range
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeLocations(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("DecodeLocations: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d locations, want %d: %v", len(got), len(tc.want), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("location %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestDecodeHover(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"null", `null`, ""},
		{"markup content", `{"contents":{"kind":"markdown","value":"hello **world**"}}`, "hello **world**"},
		{"plain string", `{"contents":"plain"}`, "plain"},
		{"marked string", `{"contents":{"language":"go","value":"fmt.Println"}}`, "```go\nfmt.Println\n```"},
		{
			"mixed array",
			`{"contents":[{"kind":"plaintext","value":"one"},"two",{"language":"go","value":"x"}]}`,
			"one\n\ntwo\n\n```go\nx\n```",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeHover(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("DecodeHover: %v", err)
			}
			if got != tc.want {
				t.Fatalf("hover = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDecodeDocumentSymbols(t *testing.T) {
	t.Run("hierarchical", func(t *testing.T) {
		raw := json.RawMessage(`[{"name":"main","kind":12,"range":{"start":{"line":0,"character":0},"end":{"line":3,"character":0}},"selectionRange":{"start":{"line":0,"character":4},"end":{"line":0,"character":8}},"children":[{"name":"inner","kind":8,"range":{"start":{"line":1,"character":1},"end":{"line":2,"character":1}},"selectionRange":{"start":{"line":1,"character":1},"end":{"line":1,"character":6}}}]},{"name":"Other","kind":23}]`)
		hier, flat, err := DecodeDocumentSymbols(raw)
		if err != nil {
			t.Fatalf("DecodeDocumentSymbols: %v", err)
		}
		if flat != nil {
			t.Fatalf("flat symbols = %+v, want nil", flat)
		}
		if len(hier) != 2 {
			t.Fatalf("got %d symbols, want 2", len(hier))
		}
		if hier[0].Name != "main" || hier[0].Kind != 12 || len(hier[0].Children) != 1 || hier[0].Children[0].Name != "inner" {
			t.Fatalf("hierarchical decode = %+v", hier[0])
		}
	})

	t.Run("flat", func(t *testing.T) {
		raw := json.RawMessage(`[{"name":"A","kind":5,"location":{"uri":"file:///a.go","range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}}},"containerName":""},{"name":"B","kind":13,"location":{"uri":"file:///a.go","range":{"start":{"line":2,"character":0},"end":{"line":2,"character":1}}},"containerName":"A"}]`)
		hier, flat, err := DecodeDocumentSymbols(raw)
		if err != nil {
			t.Fatalf("DecodeDocumentSymbols: %v", err)
		}
		if hier != nil {
			t.Fatalf("hierarchical symbols = %+v, want nil", hier)
		}
		if len(flat) != 2 || flat[0].Name != "A" || flat[1].ContainerName != "A" {
			t.Fatalf("flat decode = %+v", flat)
		}
	})

	t.Run("null", func(t *testing.T) {
		hier, flat, err := DecodeDocumentSymbols(json.RawMessage("null"))
		if err != nil {
			t.Fatalf("DecodeDocumentSymbols: %v", err)
		}
		if hier != nil || flat != nil {
			t.Fatalf("null decoded to %+v / %+v, want nil / nil", hier, flat)
		}
	})
}

func TestDiagnosticCodeAndNames(t *testing.T) {
	codes := []struct {
		raw  string
		want string
	}{
		{`42`, "42"},
		{`"E1001"`, "E1001"},
		{`null`, ""},
		{``, ""},
	}
	for _, tc := range codes {
		if got := DiagnosticCode(json.RawMessage(tc.raw)); got != tc.want {
			t.Fatalf("DiagnosticCode(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}

	severities := []struct {
		sev  int
		want string
	}{
		{0, "error"}, {1, "error"}, {2, "warning"}, {3, "info"}, {4, "hint"}, {5, ""},
	}
	for _, tc := range severities {
		if got := SeverityName(tc.sev); got != tc.want {
			t.Fatalf("SeverityName(%d) = %q, want %q", tc.sev, got, tc.want)
		}
	}

	kinds := []struct {
		kind int
		want string
	}{
		{0, "symbol"}, {1, "file"}, {12, "function"}, {21, "null"}, {26, "typeparameter"}, {27, "symbol"},
	}
	for _, tc := range kinds {
		if got := SymbolKindName(tc.kind); got != tc.want {
			t.Fatalf("SymbolKindName(%d) = %q, want %q", tc.kind, got, tc.want)
		}
	}
}
