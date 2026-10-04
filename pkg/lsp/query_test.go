package lsp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// allowAll is the PreviewAllowed every query test grants unless it is the
// behavior under test.
func allowAll(string) bool { return true }

func wantErr(t *testing.T, err error, text string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %q", text)
	}
	if err.Error() != text {
		t.Fatalf("error = %q, want %q", err.Error(), text)
	}
}

// loc builds one fake-server Location reply.
func loc(uri string, line int) map[string]any {
	return rangeOf(uri, line, line)
}

// rangeOf builds a fake-server Location spanning two zero-based lines.
func rangeOf(uri string, start, end int) map[string]any {
	return map[string]any{
		"uri": uri,
		"range": map[string]any{
			"start": map[string]any{"line": start, "character": 0},
			"end":   map[string]any{"line": end, "character": 3},
		},
	}
}

func TestQueryArgumentErrors(t *testing.T) {
	env := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"capabilities": map[string]any{"definitionProvider": true},
			"errors": map[string]any{
				"textDocument/definition": map[string]any{"code": -32603, "message": "boom"},
			},
		},
	})
	ctx := context.Background()
	file := filepath.Join(env.project, "a.fk")
	mustWrite(t, file, "target\n")

	untrusted := env.pool.NewManager(ManagerOptions{ProjectRoot: env.project, Trusted: false})
	defer untrusted.Close()
	_, err := untrusted.Query(ctx, tool.CodeIntelQuery{Operation: tool.LSPOpDefinition, AbsPath: file, Line: 1, Column: 1})
	wantErr(t, err, "language servers only run in trusted projects")

	_, err = env.manager.Query(ctx, tool.CodeIntelQuery{Operation: "nonsense", AbsPath: file, Line: 1, Column: 1})
	wantErr(t, err, `unknown operation "nonsense"`)

	_, err = env.manager.Query(ctx, tool.CodeIntelQuery{Operation: tool.LSPOpDefinition, AbsPath: file, Line: 0, Column: 1})
	wantErr(t, err, "operation definition requires file_path, line, and column or symbol")

	_, err = env.manager.Query(ctx, tool.CodeIntelQuery{Operation: tool.LSPOpDefinition, AbsPath: file, Symbol: "target"})
	wantErr(t, err, "operation definition requires file_path, line, and column or symbol")

	_, err = env.manager.Query(ctx, tool.CodeIntelQuery{Operation: tool.LSPOpDocumentSymbols})
	wantErr(t, err, "operation document_symbols requires file_path, line, and column or symbol")

	_, err = env.manager.Query(ctx, tool.CodeIntelQuery{Operation: tool.LSPOpWorkspaceSymbols})
	wantErr(t, err, "workspace_symbols requires query")

	_, err = env.manager.Query(ctx, tool.CodeIntelQuery{
		Operation: tool.LSPOpDefinition, AbsPath: filepath.Join(t.TempDir(), "elsewhere.fk"),
		DisplayPath: "../elsewhere.fk", Line: 1, Column: 1,
	})
	wantErr(t, err, "../elsewhere.fk is outside the project, so no language server covers it")

	_, err = env.manager.Query(ctx, tool.CodeIntelQuery{Operation: tool.LSPOpDefinition, AbsPath: filepath.Join(env.project, "b.missing"), Line: 1, Column: 1})
	wantErr(t, err, "no language server handles .missing files in this project; the user can enable one with /lsp")

	_, err = env.manager.Query(ctx, tool.CodeIntelQuery{Operation: tool.LSPOpDefinition, AbsPath: filepath.Join(env.project, "Makefile"), Line: 1, Column: 1})
	wantErr(t, err, "no language server handles Makefile files in this project; the user can enable one with /lsp")

	// A server that answers with a JSON-RPC error maps to appendix C.
	_, err = env.manager.Query(ctx, tool.CodeIntelQuery{
		Operation: tool.LSPOpDefinition, AbsPath: file, Line: 1, Column: 1, PreviewAllowed: allowAll,
	})
	wantErr(t, err, "language server fake could not answer definition: boom")
}

func TestQueryDefinitionGolden(t *testing.T) {
	project := t.TempDir()
	file := filepath.Join(project, "a.fk")
	uri := PathToURI(file)
	env := newFakePoolIn(t, project, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"capabilities": map[string]any{"definitionProvider": true},
			"responses":    map[string]any{"textDocument/definition": loc(uri, 1)},
		},
	})
	// The emoji takes two UTF-16 units: column 6 (code points) is character 5.
	mustWrite(t, file, "a😀b foo\nbar\n")
	res, err := env.manager.Query(context.Background(), tool.CodeIntelQuery{
		Operation: tool.LSPOpDefinition, AbsPath: file, DisplayPath: "a.fk",
		Line: 1, Symbol: "foo", MaxResults: 50, PreviewAllowed: allowAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "definition of `foo`: 1 result in 1 file\na.fk\n  2:1  bar"
	if res.Text != want {
		t.Fatalf("text =\n%s\nwant\n%s", res.Text, want)
	}
	recs := pollRecord(t, env.records["fake"], func(recs []map[string]any) bool {
		return findRecv(recs, "textDocument/definition") != nil
	}, "the definition request")
	pos, _ := findRecv(recs, "textDocument/definition")["position"].(map[string]any)
	if pos["line"] != float64(0) || pos["character"] != float64(5) {
		t.Fatalf("recorded position = %v, want line 0 character 5", pos)
	}
	if res.Display["target"] != "foo" || res.Display["server"] != "fake" ||
		res.Display["result_count"] != 1 || res.Display["files"] != 1 || res.Display["indexing"] != false {
		t.Errorf("display = %v", res.Display)
	}
	if _, ok := res.Display["elapsed_ms"]; !ok {
		t.Error("display lacks elapsed_ms")
	}
}

func TestQuerySymbolAnchor(t *testing.T) {
	project := t.TempDir()
	file := filepath.Join(project, "a.fk")
	uri := PathToURI(file)
	env := newFakePoolIn(t, project, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"capabilities": map[string]any{"definitionProvider": true},
			"responses":    map[string]any{"textDocument/definition": loc(uri, 0)},
		},
	})
	mustWrite(t, file, "nothing here\ntarget lives here\n")

	// The symbol sits on the line below the one the model named.
	res, err := env.manager.Query(context.Background(), tool.CodeIntelQuery{
		Operation: tool.LSPOpDefinition, AbsPath: file, DisplayPath: "a.fk",
		Line: 1, Symbol: "target", MaxResults: 50, PreviewAllowed: allowAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "(symbol found on line 2)\ndefinition of `target`: 1 result in 1 file\na.fk\n  1:1  nothing here"
	if res.Text != want {
		t.Fatalf("text =\n%s\nwant\n%s", res.Text, want)
	}

	_, err = env.manager.Query(context.Background(), tool.CodeIntelQuery{
		Operation: tool.LSPOpDefinition, AbsPath: file, DisplayPath: "a.fk",
		Line: 1, Symbol: "nope", MaxResults: 50, PreviewAllowed: allowAll,
	})
	wantErr(t, err, `symbol "nope" not found on line 1 of a.fk (searched lines 1-2)`)
}

func TestQueryReferencesSortedGroupedTruncated(t *testing.T) {
	// Created before the project so its path sorts ahead of every project
	// file: the outside row must survive the truncation.
	outsideDir := t.TempDir()
	project := t.TempDir()
	aURI := PathToURI(filepath.Join(project, "a.fk"))
	bURI := PathToURI(filepath.Join(project, "b.fk"))
	cURI := PathToURI(filepath.Join(project, "c.fk"))
	zURI := PathToURI(filepath.Join(outsideDir, "z.fk"))
	env := newFakePoolIn(t, project, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"capabilities": map[string]any{"referencesProvider": true},
			"responses": map[string]any{
				"textDocument/references": []any{
					loc(bURI, 0), loc(zURI, 0), loc(aURI, 1), loc(cURI, 0),
					loc(aURI, 0), loc(aURI, 0), // the duplicate dedups away
				},
			},
		},
	})
	mustWrite(t, filepath.Join(project, "a.fk"), "alpha line one\nalpha line two\nx\nx\ntarget\n")
	mustWrite(t, filepath.Join(project, "b.fk"), "beta line\n")
	mustWrite(t, filepath.Join(project, "c.fk"), "gamma\n")

	res, err := env.manager.Query(context.Background(), tool.CodeIntelQuery{
		Operation: tool.LSPOpReferences, AbsPath: filepath.Join(project, "a.fk"), DisplayPath: "a.fk",
		Line: 5, Column: 1, MaxResults: 4,
		// b.fk may not be previewed: its row stays bare.
		PreviewAllowed: func(path string) bool { return !strings.HasSuffix(path, "b.fk") },
	})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"references to `5:1`: 4 results in 3 files",
		filepath.ToSlash(filepath.Join(outsideDir, "z.fk")),
		"  1:1  [outside project]",
		"a.fk",
		"  1:1  alpha line one",
		"  2:1  alpha line two",
		"b.fk",
		"  1:1",
		"… 1 more not shown (raise max_results, at most 200)",
	}, "\n")
	if res.Text != want {
		t.Fatalf("text =\n%s\nwant\n%s", res.Text, want)
	}
	if res.Display["result_count"] != 4 || res.Display["files"] != 3 {
		t.Errorf("display = %v", res.Display)
	}
}

func TestQueryDefinitionGroupsInterleavedFiles(t *testing.T) {
	project := t.TempDir()
	aURI := PathToURI(filepath.Join(project, "a.fk"))
	bURI := PathToURI(filepath.Join(project, "b.fk"))
	env := newFakePoolIn(t, project, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"capabilities": map[string]any{"definitionProvider": true},
			// The server answers interleaved: without grouping a.fk would
			// head two groups and count as two files on its own.
			"responses": map[string]any{"textDocument/definition": []any{
				loc(aURI, 2), loc(bURI, 0), loc(aURI, 0),
			}},
		},
	})
	mustWrite(t, filepath.Join(project, "a.fk"), "alpha one\nalpha two\nalpha three\n")
	mustWrite(t, filepath.Join(project, "b.fk"), "beta one\n")
	res, err := env.manager.Query(context.Background(), tool.CodeIntelQuery{
		Operation: tool.LSPOpDefinition, AbsPath: filepath.Join(project, "a.fk"), DisplayPath: "a.fk",
		Line: 1, Column: 1, MaxResults: 50, PreviewAllowed: allowAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"definition of `1:1`: 3 results in 2 files",
		"a.fk",
		"  3:1  alpha three",
		"  1:1  alpha one",
		"b.fk",
		"  1:1  beta one",
	}, "\n")
	if res.Text != want {
		t.Fatalf("text =\n%s\nwant\n%s", res.Text, want)
	}
	if res.Display["files"] != 2 {
		t.Errorf("display = %v, want files 2", res.Display)
	}
}

func TestQueryCallHierarchyCountsDistinctFiles(t *testing.T) {
	project := t.TempDir()
	aURI := PathToURI(filepath.Join(project, "a.fk"))
	bURI := PathToURI(filepath.Join(project, "b.fk"))
	item := func(uri, name string, line int) map[string]any {
		rng := map[string]any{
			"start": map[string]any{"line": line, "character": 0},
			"end":   map[string]any{"line": line, "character": 4},
		}
		return map[string]any{"name": name, "kind": 12, "uri": uri, "range": rng, "selectionRange": rng}
	}
	env := newFakePoolIn(t, project, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"capabilities": map[string]any{"callHierarchyProvider": true},
			"responses": map[string]any{
				"textDocument/prepareCallHierarchy": []any{item(aURI, "main", 1)},
				// Callers in two files, interleaved: the file count must
				// count a.fk once.
				"callHierarchy/incomingCalls": []any{
					map[string]any{"from": item(aURI, "callerA", 2), "fromRanges": []any{}},
					map[string]any{"from": item(bURI, "callerB", 0), "fromRanges": []any{}},
					map[string]any{"from": item(aURI, "callerC", 4), "fromRanges": []any{}},
				},
			},
		},
	})
	mustWrite(t, filepath.Join(project, "a.fk"), "l1\nl2\nl3\nl4\nl5\n")
	mustWrite(t, filepath.Join(project, "b.fk"), "m1\n")
	res, err := env.manager.Query(context.Background(), tool.CodeIntelQuery{
		Operation: tool.LSPOpIncomingCalls, AbsPath: filepath.Join(project, "a.fk"), DisplayPath: "a.fk",
		Line: 2, Column: 1, MaxResults: 50, PreviewAllowed: allowAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Display["files"] != 2 {
		t.Errorf("display = %v, want files 2", res.Display)
	}
}

func TestQueryLocationLink(t *testing.T) {
	project := t.TempDir()
	file := filepath.Join(project, "a.fk")
	uri := PathToURI(file)
	env := newFakePoolIn(t, project, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"capabilities": map[string]any{"definitionProvider": true},
			"responses": map[string]any{
				"textDocument/definition": []any{map[string]any{
					"targetUri":            uri,
					"targetRange":          map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 3}},
					"targetSelectionRange": map[string]any{"start": map[string]any{"line": 1, "character": 0}, "end": map[string]any{"line": 1, "character": 3}},
				}},
			},
		},
	})
	mustWrite(t, file, "one\ntwo\n")
	res, err := env.manager.Query(context.Background(), tool.CodeIntelQuery{
		Operation: tool.LSPOpDefinition, AbsPath: file, DisplayPath: "a.fk",
		Line: 1, Column: 1, MaxResults: 50, PreviewAllowed: allowAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "definition of `1:1`: 1 result in 1 file\na.fk\n  2:1  two"
	if res.Text != want {
		t.Fatalf("text =\n%s\nwant\n%s", res.Text, want)
	}
}

func TestQueryHover(t *testing.T) {
	ctx := context.Background()

	markdown := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"capabilities": map[string]any{"hoverProvider": true},
			"responses": map[string]any{
				"textDocument/hover": map[string]any{"contents": map[string]any{"kind": "markdown", "value": "```go\nfunc Foo()\n```"}},
			},
		},
	})
	file := filepath.Join(markdown.project, "a.fk")
	mustWrite(t, file, "func Foo() {}\n")
	res, err := markdown.manager.Query(ctx, tool.CodeIntelQuery{
		Operation: tool.LSPOpHover, AbsPath: file, DisplayPath: "a.fk",
		Line: 1, Column: 6, MaxResults: 50, PreviewAllowed: allowAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "hover at a.fk:1:6\n```go\nfunc Foo()\n```"; res.Text != want {
		t.Fatalf("text =\n%s\nwant\n%s", res.Text, want)
	}

	long := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"capabilities": map[string]any{"hoverProvider": true},
			"responses": map[string]any{
				"textDocument/hover": map[string]any{"contents": map[string]any{"kind": "plaintext", "value": strings.Repeat("x", 2001)}},
			},
		},
	})
	file = filepath.Join(long.project, "a.fk")
	mustWrite(t, file, "x\n")
	res, err = long.manager.Query(ctx, tool.CodeIntelQuery{
		Operation: tool.LSPOpHover, AbsPath: file, DisplayPath: "a.fk",
		Line: 1, Column: 1, MaxResults: 50, PreviewAllowed: allowAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantBody := "hover at a.fk:1:1\n" + strings.Repeat("x", 2000) + "…"
	if res.Text != wantBody {
		t.Fatalf("truncation: len = %d, suffix = %q", len(res.Text), res.Text[len(res.Text)-8:])
	}

	empty := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"capabilities": map[string]any{"hoverProvider": true},
			"responses":    map[string]any{"textDocument/hover": nil},
		},
	})
	file = filepath.Join(empty.project, "a.fk")
	mustWrite(t, file, "x\n")
	res, err = empty.manager.Query(ctx, tool.CodeIntelQuery{
		Operation: tool.LSPOpHover, AbsPath: file, DisplayPath: "a.fk",
		Line: 1, Column: 1, MaxResults: 50, PreviewAllowed: allowAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "no hover information at a.fk:1:1" {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestQueryDocumentSymbolsHierarchical(t *testing.T) {
	project := t.TempDir()
	file := filepath.Join(project, "a.fk")
	env := newFakePoolIn(t, project, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"capabilities": map[string]any{"documentSymbolProvider": true},
			"responses": map[string]any{
				"textDocument/documentSymbol": []any{
					map[string]any{
						"name": "main", "kind": 12,
						"range":          map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 4, "character": 1}},
						"selectionRange": map[string]any{"start": map[string]any{"line": 0, "character": 5}, "end": map[string]any{"line": 0, "character": 9}},
						"children": []any{map[string]any{
							"name": "inner", "kind": 6,
							"range":          map[string]any{"start": map[string]any{"line": 1, "character": 0}, "end": map[string]any{"line": 2, "character": 1}},
							"selectionRange": map[string]any{"start": map[string]any{"line": 1, "character": 1}, "end": map[string]any{"line": 1, "character": 6}},
						}},
					},
					map[string]any{
						"name": "Config", "kind": 23,
						"range":          map[string]any{"start": map[string]any{"line": 6, "character": 0}, "end": map[string]any{"line": 8, "character": 1}},
						"selectionRange": map[string]any{"start": map[string]any{"line": 6, "character": 6}, "end": map[string]any{"line": 6, "character": 12}},
					},
				},
			},
		},
	})
	mustWrite(t, file, "x\n")
	res, err := env.manager.Query(context.Background(), tool.CodeIntelQuery{
		Operation: tool.LSPOpDocumentSymbols, AbsPath: file, DisplayPath: "a.fk",
		MaxResults: 50, PreviewAllowed: allowAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"symbols in a.fk",
		"function main  1-5",
		"  method inner  2-3",
		"struct Config  7-9",
	}, "\n")
	if res.Text != want {
		t.Fatalf("text =\n%s\nwant\n%s", res.Text, want)
	}
	if res.Display["result_count"] != 3 || res.Display["files"] != 1 {
		t.Errorf("display = %v", res.Display)
	}
}

func TestQueryDocumentSymbolsFlat(t *testing.T) {
	project := t.TempDir()
	file := filepath.Join(project, "a.fk")
	uri := PathToURI(file)
	env := newFakePoolIn(t, project, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"capabilities": map[string]any{"documentSymbolProvider": true},
			"responses": map[string]any{
				"textDocument/documentSymbol": []any{
					map[string]any{
						"name": "main", "kind": 12, "containerName": "",
						"location": rangeOf(uri, 0, 4),
					},
					map[string]any{
						"name": "Config", "kind": 23, "containerName": "",
						"location": rangeOf(uri, 6, 8),
					},
				},
			},
		},
	})
	mustWrite(t, file, "x\n")
	res, err := env.manager.Query(context.Background(), tool.CodeIntelQuery{
		Operation: tool.LSPOpDocumentSymbols, AbsPath: file, DisplayPath: "a.fk",
		MaxResults: 50, PreviewAllowed: allowAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"symbols in a.fk",
		"function main  1-5",
		"struct Config  7-9",
	}, "\n")
	if res.Text != want {
		t.Fatalf("text =\n%s\nwant\n%s", res.Text, want)
	}
}

func TestQueryWorkspaceSymbolsNeedsRunningServer(t *testing.T) {
	ctx := context.Background()
	project := t.TempDir()
	aURI := PathToURI(filepath.Join(project, "a.fk"))
	env := newFakePoolIn(t, project, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"capabilities": map[string]any{"workspaceSymbolProvider": true},
			"responses": map[string]any{
				"workspace/symbol": []any{map[string]any{
					"name": "Config", "kind": 23, "location": loc(aURI, 6),
				}},
			},
		},
	})

	// Nothing runs yet: workspace_symbols must not start anything.
	_, err := env.manager.Query(ctx, tool.CodeIntelQuery{Operation: tool.LSPOpWorkspaceSymbols, Query: "cfg", MaxResults: 50})
	wantErr(t, err, "no language server is running in this project yet; pass file_path to start the one for that file")

	warmInstance(t, env)
	res, err := env.manager.Query(ctx, tool.CodeIntelQuery{Operation: tool.LSPOpWorkspaceSymbols, Query: "cfg", MaxResults: 50})
	if err != nil {
		t.Fatal(err)
	}
	if want := "workspace symbols matching `cfg`: 1\nstruct Config  a.fk:7"; res.Text != want {
		t.Fatalf("text =\n%s\nwant\n%s", res.Text, want)
	}

	// A server that resolves lazily sends no range; resolveProvider fills it.
	lazyProject := t.TempDir()
	lazyURI := PathToURI(filepath.Join(lazyProject, "a.fk"))
	lazy := newFakePoolIn(t, lazyProject, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"capabilities": map[string]any{"workspaceSymbolProvider": map[string]any{"resolveProvider": true}},
			"responses": map[string]any{
				"workspace/symbol": []any{map[string]any{
					"name": "Config", "kind": 23, "data": map[string]any{"id": 7},
					"location": map[string]any{"uri": lazyURI},
				}},
				"workspaceSymbol/resolve": map[string]any{
					"name": "Config", "kind": 23, "location": loc(lazyURI, 6),
				},
			},
		},
	})
	warmInstance(t, lazy)
	res, err = lazy.manager.Query(ctx, tool.CodeIntelQuery{Operation: tool.LSPOpWorkspaceSymbols, Query: "cfg", MaxResults: 50})
	if err != nil {
		t.Fatal(err)
	}
	if want := "workspace symbols matching `cfg`: 1\nstruct Config  a.fk:7"; res.Text != want {
		t.Fatalf("resolved text =\n%s\nwant\n%s", res.Text, want)
	}
	recs := pollRecord(t, lazy.records["fake"], func(recs []map[string]any) bool {
		return findRecv(recs, "workspaceSymbol/resolve") != nil
	}, "the resolve request")
	if params := findRecv(recs, "workspaceSymbol/resolve"); params["name"] != "Config" {
		t.Fatalf("resolve params = %v", params)
	}
}

func TestQueryCallHierarchy(t *testing.T) {
	project := t.TempDir()
	file := filepath.Join(project, "a.fk")
	uri := PathToURI(file)
	env := newFakePoolIn(t, project, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"capabilities": map[string]any{"callHierarchyProvider": true},
			"responses": map[string]any{
				"textDocument/prepareCallHierarchy": []any{map[string]any{
					"name": "main", "kind": 12, "uri": uri,
					"range":          map[string]any{"start": map[string]any{"line": 1, "character": 0}, "end": map[string]any{"line": 1, "character": 4}},
					"selectionRange": map[string]any{"start": map[string]any{"line": 1, "character": 0}, "end": map[string]any{"line": 1, "character": 4}},
				}},
				"callHierarchy/incomingCalls": []any{map[string]any{
					"from": map[string]any{
						"name": "caller", "kind": 12, "uri": uri,
						"range":          map[string]any{"start": map[string]any{"line": 2, "character": 0}, "end": map[string]any{"line": 2, "character": 6}},
						"selectionRange": map[string]any{"start": map[string]any{"line": 2, "character": 0}, "end": map[string]any{"line": 2, "character": 6}},
					},
					"fromRanges": []any{},
				}},
			},
		},
	})
	mustWrite(t, file, "l1\nl2\nl3\n")
	res, err := env.manager.Query(context.Background(), tool.CodeIntelQuery{
		Operation: tool.LSPOpIncomingCalls, AbsPath: file, DisplayPath: "a.fk",
		Line: 2, Column: 1, MaxResults: 50, PreviewAllowed: allowAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "callers of `main`: 1\ncaller  a.fk:3"
	if res.Text != want {
		t.Fatalf("text =\n%s\nwant\n%s", res.Text, want)
	}
	recs := pollRecord(t, env.records["fake"], func(recs []map[string]any) bool {
		return findRecv(recs, "textDocument/prepareCallHierarchy") != nil && findRecv(recs, "callHierarchy/incomingCalls") != nil
	}, "the call hierarchy requests")
	item, _ := findRecv(recs, "callHierarchy/incomingCalls")["item"].(map[string]any)
	if item["name"] != "main" {
		t.Fatalf("incomingCalls item = %v", item)
	}
}

func TestQueryUnsupported(t *testing.T) {
	env := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{"capabilities": map[string]any{"hoverProvider": true}},
	})
	file := filepath.Join(env.project, "a.fk")
	mustWrite(t, file, "x\n")
	_, err := env.manager.Query(context.Background(), tool.CodeIntelQuery{
		Operation: tool.LSPOpSupertypes, AbsPath: file, DisplayPath: "a.fk",
		Line: 1, Column: 1, MaxResults: 50, PreviewAllowed: allowAll,
	})
	wantErr(t, err, "language server fake does not support supertypes")
}

func TestQueryTimeout(t *testing.T) {
	lsp := appcfg.LSPSection{RequestTimeout: 1}
	env := newFakePool(t, lsp, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"capabilities": map[string]any{"hoverProvider": true},
			"hang":         []any{"textDocument/hover"},
		},
	})
	file := filepath.Join(env.project, "a.fk")
	mustWrite(t, file, "x\n")
	start := time.Now()
	_, err := env.manager.Query(context.Background(), tool.CodeIntelQuery{
		Operation: tool.LSPOpHover, AbsPath: file, DisplayPath: "a.fk",
		Line: 1, Column: 1, MaxResults: 50, PreviewAllowed: allowAll,
	})
	elapsed := time.Since(start)
	wantErr(t, err, "language server fake did not answer hover within 1s")
	if elapsed < 900*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("timeout after %s; the request_timeout is 1s", elapsed)
	}
}

func TestQueryIndexingNote(t *testing.T) {
	lsp := appcfg.LSPSection{RequestTimeout: 2}
	project := t.TempDir()
	file := filepath.Join(project, "a.fk")
	uri := PathToURI(file)
	env := newFakePoolIn(t, project, lsp, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"capabilities": map[string]any{"definitionProvider": true},
			"responses":    map[string]any{"textDocument/definition": loc(uri, 0)},
			// A work-done token that never ends keeps the server indexing.
			"after_initialized": []any{map[string]any{
				"notify": "$/progress",
				"params": map[string]any{
					"token": "forever",
					"value": map[string]any{"kind": "begin", "title": "indexing"},
				},
			}},
		},
	})
	mustWrite(t, file, "target\n")
	// Custom servers default to readiness "none"; this one reports progress.
	env.manager.mu.Lock()
	for i := range env.manager.cache {
		env.manager.cache[i].Readiness = ReadinessProgress
	}
	env.manager.mu.Unlock()

	res, err := env.manager.Query(context.Background(), tool.CodeIntelQuery{
		Operation: tool.LSPOpDefinition, AbsPath: file, DisplayPath: "a.fk",
		Line: 1, Column: 1, MaxResults: 50, PreviewAllowed: allowAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(res.Text, "\n")
	if len(lines) < 2 || lines[0] != "language server fake is still indexing; results may be incomplete" {
		t.Fatalf("text =\n%s", res.Text)
	}
	if lines[1] != "definition of `1:1`: 1 result in 1 file" {
		t.Fatalf("second line = %q", lines[1])
	}
	if res.Display["indexing"] != true {
		t.Errorf("display = %v", res.Display)
	}
}

func TestQueryDiagnostics(t *testing.T) {
	ctx := context.Background()

	// Push mode: the didOpen a query syncs publishes into the store.
	push := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: diagScript(map[string]any{"bad": []any{diagRule("bad thing", 0)}}),
	})
	file := filepath.Join(push.project, "a.fk")
	mustWrite(t, file, "contains bad\n")
	res, err := push.manager.Query(ctx, tool.CodeIntelQuery{
		Operation: tool.LSPOpDiagnostics, AbsPath: file, DisplayPath: "a.fk", PreviewAllowed: allowAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"diagnostics for a.fk: 1",
		"a.fk",
		"  error 1:1 bad thing [fake]",
	}, "\n")
	if res.Text != want {
		t.Fatalf("push text =\n%s\nwant\n%s", res.Text, want)
	}

	// Pull mode: the server answers textDocument/diagnostic itself.
	pull := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"capabilities":     map[string]any{"diagnosticProvider": true},
			"pull_diagnostics": true,
			"diagnostics": map[string]any{
				"rules": map[string]any{"bad": []any{diagRule("bad thing", 0)}},
			},
		},
	})
	file = filepath.Join(pull.project, "a.fk")
	mustWrite(t, file, "contains bad\n")
	res, err = pull.manager.Query(ctx, tool.CodeIntelQuery{
		Operation: tool.LSPOpDiagnostics, AbsPath: file, DisplayPath: "a.fk", PreviewAllowed: allowAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != want {
		t.Fatalf("pull text =\n%s\nwant\n%s", res.Text, want)
	}

	// Without a file: every open document's problems, across the manager's
	// running instances.
	summary := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: diagScript(map[string]any{"bad": []any{diagRule("bad thing", 0)}}),
	})
	file = filepath.Join(summary.project, "a.fk")
	mustWrite(t, file, "contains bad\n")
	warmInstance(t, summary)
	summary.manager.DidRead(ctx, file, []byte("contains bad\n"))
	waitForPoll(t, "the diagnostics to land", func() bool {
		_, _, _, ok := summary.pool.diags.Get("fake", file)
		return ok
	})
	res, err = summary.manager.Query(ctx, tool.CodeIntelQuery{Operation: tool.LSPOpDiagnostics, PreviewAllowed: allowAll})
	if err != nil {
		t.Fatal(err)
	}
	want = strings.Join([]string{
		"diagnostics in open files: 1",
		"a.fk",
		"  error 1:1 bad thing [fake]",
	}, "\n")
	if res.Text != want {
		t.Fatalf("summary text =\n%s\nwant\n%s", res.Text, want)
	}
}

func TestQueryNotInstalled(t *testing.T) {
	env := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk", script: map[string]any{},
		cfg: func(c *appcfg.LSPServerConfig) { c.Command = "/nonexistent/forebrain-lsp-test" },
	})
	file := filepath.Join(env.project, "a.fk")
	mustWrite(t, file, "target\n")
	_, err := env.manager.Query(context.Background(), tool.CodeIntelQuery{
		Operation: tool.LSPOpDefinition, AbsPath: file, DisplayPath: "a.fk",
		Line: 1, Column: 1, MaxResults: 50, PreviewAllowed: allowAll,
	})
	wantErr(t, err, `language server fake is enabled but its command "/nonexistent/forebrain-lsp-test" was not found`)
}
