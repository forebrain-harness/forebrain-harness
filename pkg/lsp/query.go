package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// query.go: the lsp tool's lookups and their model-facing text. Query (spec
// §8.1) validates one operation, picks and acquires its server, converts the
// 1-based position (spec §8.2), runs the request and renders the result
// (appendix B.5); every error is appendix C, verbatim.

// operationCapabilities maps each operation to the server capability that
// gates it (spec §8.1); diagnostics needs none.
var operationCapabilities = map[string]string{
	tool.LSPOpDefinition:       "definitionProvider",
	tool.LSPOpDeclaration:      "declarationProvider",
	tool.LSPOpTypeDefinition:   "typeDefinitionProvider",
	tool.LSPOpImplementation:   "implementationProvider",
	tool.LSPOpReferences:       "referencesProvider",
	tool.LSPOpHover:            "hoverProvider",
	tool.LSPOpDocumentSymbols:  "documentSymbolProvider",
	tool.LSPOpWorkspaceSymbols: "workspaceSymbolProvider",
	tool.LSPOpIncomingCalls:    "callHierarchyProvider",
	tool.LSPOpOutgoingCalls:    "callHierarchyProvider",
	tool.LSPOpSupertypes:       "typeHierarchyProvider",
	tool.LSPOpSubtypes:         "typeHierarchyProvider",
}

// locationLabels are appendix B.5's first-line labels of the five
// location-list operations.
var locationLabels = map[string]string{
	tool.LSPOpDefinition:     "definition of",
	tool.LSPOpDeclaration:    "declaration of",
	tool.LSPOpTypeDefinition: "type definition of",
	tool.LSPOpImplementation: "implementations of",
	tool.LSPOpReferences:     "references to",
}

// locationMethods are the LSP methods behind the location-list operations.
var locationMethods = map[string]string{
	tool.LSPOpDefinition:     "textDocument/definition",
	tool.LSPOpDeclaration:    "textDocument/declaration",
	tool.LSPOpTypeDefinition: "textDocument/typeDefinition",
	tool.LSPOpImplementation: "textDocument/implementation",
	tool.LSPOpReferences:     "textDocument/references",
}

// queryResult is one operation's rendered text plus the counts the Display
// fields report.
type queryResult struct {
	text  string
	count int
	files int
}

// queryState carries one Query call through its steps.
type queryState struct {
	m     *Manager
	q     tool.CodeIntelQuery // the caller's values, never mutated
	eff   appcfg.EffectiveLSP
	srv   ServerConfig
	root  string
	entry *poolInstance
	inst  *Instance
	docs  *DocSync
	max   int // effective max_results

	line   int // resolved 1-based line and column (after symbol anchoring)
	column int
	pos    Position // the LSP position sent to the server

	indexing   bool
	noteColumn string // appendix B.6 hint lines, kept apart for the fixed
	noteSymbol string // order: indexing → column → symbol
}

// query is Manager.Query's implementation.
func (m *Manager) query(ctx context.Context, q tool.CodeIntelQuery) (tool.CodeIntelResult, error) {
	startedAt := time.Now()
	if m == nil || !m.opts.Trusted {
		return tool.CodeIntelResult{}, errors.New("language servers only run in trusted projects") // appendix C, untrusted
	}
	known := false
	for _, op := range tool.LSPOperations {
		if op == q.Operation {
			known = true
			break
		}
	}
	if !known {
		return tool.CodeIntelResult{}, fmt.Errorf("unknown operation %q", q.Operation) // appendix C, unknown-operation
	}
	needsPosition := q.Operation != tool.LSPOpDocumentSymbols &&
		q.Operation != tool.LSPOpWorkspaceSymbols && q.Operation != tool.LSPOpDiagnostics
	if q.Operation == tool.LSPOpDocumentSymbols {
		if q.AbsPath == "" {
			return tool.CodeIntelResult{}, fmt.Errorf("operation %s requires file_path, line, and column or symbol", q.Operation) // appendix C, missing-args
		}
	}
	if needsPosition && (q.AbsPath == "" || q.Line <= 0 || (q.Column <= 0 && q.Symbol == "")) {
		return tool.CodeIntelResult{}, fmt.Errorf("operation %s requires file_path, line, and column or symbol", q.Operation) // appendix C, missing-args
	}
	if q.Operation == tool.LSPOpWorkspaceSymbols && q.Query == "" {
		return tool.CodeIntelResult{}, errors.New("workspace_symbols requires query") // appendix C, missing-query
	}

	s := &queryState{m: m, q: q, max: q.MaxResults, line: q.Line, column: q.Column}
	if s.max <= 0 {
		s.max = 50 // the tool's default, applied here for callers that skip it
	}
	s.eff = m.pool.config().EffectiveLSP()
	ctx, cancel := context.WithTimeout(ctx, s.eff.RequestTimeout)
	defer cancel()

	if q.Operation == tool.LSPOpDiagnostics && q.AbsPath == "" {
		// The no-file summary reads what already runs; it starts nothing.
		return m.queryDiagnosticsSummary(s, startedAt)
	}

	if q.AbsPath != "" {
		if !pathWithin(filepath.Clean(q.AbsPath), m.opts.ProjectRoot) {
			return tool.CodeIntelResult{}, s.outsideProjectError()
		}
		primary, _ := ServersForFile(m.servers(), q.AbsPath, m.opts.ProjectRoot)
		if primary == nil {
			return tool.CodeIntelResult{}, fmt.Errorf("no language server handles %s files in this project; the user can enable one with /lsp", s.fileExt()) // appendix C, no-server
		}
		s.srv = *primary
		root, ok := ResolveRoot(q.AbsPath, m.opts.ProjectRoot, s.srv)
		if !ok {
			return tool.CodeIntelResult{}, s.outsideProjectError()
		}
		s.root = root
	} else { // workspace_symbols without file_path: a running primary answers
		entry := m.runningPrimary()
		if entry == nil {
			return tool.CodeIntelResult{}, errors.New("no language server is running in this project yet; pass file_path to start the one for that file") // appendix C, no-running-server
		}
		s.entry, s.srv, s.root = entry, entry.srv, entry.root
	}

	if s.entry == nil {
		entry, err := m.pool.acquire(ctx, m, s.srv, s.root)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return tool.CodeIntelResult{}, s.timeoutError()
			}
			return tool.CodeIntelResult{}, err // already appendix C: not-installed, start-failed, too-many-servers
		}
		s.entry = entry
	}
	m.pool.begin(s.entry)
	defer m.pool.end(s.entry)
	s.inst, s.docs = s.entry.inst, s.entry.docs

	// Readiness (spec §7.7): wait within half the remaining time; a server
	// still indexing answers anyway, with appendix B.6's note.
	if deadline, ok := ctx.Deadline(); ok {
		waitCtx, waitCancel := context.WithTimeout(ctx, time.Until(deadline)/2)
		_ = s.inst.WaitReady(waitCtx)
		waitCancel()
	}
	switch s.inst.State() {
	case StateStarting, StateInitializing, StateIndexing:
		s.indexing = true
	}

	var (
		res queryResult
		err error
	)
	switch q.Operation {
	case tool.LSPOpDefinition, tool.LSPOpDeclaration, tool.LSPOpTypeDefinition,
		tool.LSPOpImplementation, tool.LSPOpReferences:
		res, err = s.runLocations(ctx)
	case tool.LSPOpHover:
		res, err = s.runHover(ctx)
	case tool.LSPOpDocumentSymbols:
		res, err = s.runDocumentSymbols(ctx)
	case tool.LSPOpWorkspaceSymbols:
		res, err = s.runWorkspaceSymbols(ctx)
	case tool.LSPOpIncomingCalls, tool.LSPOpOutgoingCalls:
		res, err = s.runCallHierarchy(ctx)
	case tool.LSPOpSupertypes, tool.LSPOpSubtypes:
		res, err = s.runTypeHierarchy(ctx)
	case tool.LSPOpDiagnostics:
		res, err = s.runDiagnostics(ctx)
	}
	if err != nil {
		return tool.CodeIntelResult{}, err
	}

	text := res.text
	if notes := s.notes(); len(notes) > 0 {
		text = strings.Join(notes, "\n") + "\n" + text
	}
	return tool.CodeIntelResult{
		Text: text,
		Display: map[string]any{
			"operation":    q.Operation,
			"server":       s.srv.ID,
			"result_count": res.count,
			"files":        res.files,
			"elapsed_ms":   time.Since(startedAt).Milliseconds(),
			"indexing":     s.indexing,
			"target":       s.target(),
			"display_path": s.displayPath(),
		},
	}, nil
}

// notes renders the appendix B.6 hint lines in their fixed order.
func (s *queryState) notes() []string {
	var notes []string
	if s.indexing {
		notes = append(notes, fmt.Sprintf("language server %s is still indexing; results may be incomplete", s.srv.ID))
	}
	if s.noteColumn != "" {
		notes = append(notes, s.noteColumn)
	}
	if s.noteSymbol != "" {
		notes = append(notes, s.noteSymbol)
	}
	return notes
}

// runningPrimary answers the lexicographically-first started primary
// instance this manager references (spec §8.1), or nil.
func (m *Manager) runningPrimary() *poolInstance {
	entries := m.pool.entriesFor(m)
	sort.Slice(entries, func(a, b int) bool { return entries[a].serverID < entries[b].serverID })
	for _, e := range entries {
		if e.srv.Role == appcfg.LSPRolePrimary {
			return e
		}
	}
	return nil
}

// syncDocument opens or re-syncs the query file (spec §7.6) and records it
// for the post-shell sweep; ErrFileTooLarge becomes appendix C.
func (s *queryState) syncDocument(ctx context.Context) (content []byte, version int32, err error) {
	path := filepath.Clean(s.q.AbsPath)
	version, content, err = s.docs.EnsureSynced(ctx, path)
	if err != nil {
		if errors.Is(err, ErrFileTooLarge) {
			return nil, 0, fmt.Errorf("%s is larger than 4 MiB; language servers are not given files that large", s.displayPath()) // appendix C, file-too-large
		}
		return nil, 0, err
	}
	s.m.pool.notePath(s.entry, path)
	return content, version, nil
}

// resolvePosition converts the (possibly symbol-anchored) 1-based position
// to an LSP Position (spec §8.2), collecting the B.6 hints.
func (s *queryState) resolvePosition(content []byte) error {
	line, column := s.q.Line, s.q.Column
	if s.q.Symbol != "" {
		foundLine, foundColumn, err := AnchorSymbol(content, line, s.q.Symbol)
		if e, ok := err.(*SymbolNotFoundError); ok {
			return fmt.Errorf("symbol %q not found on line %d of %s (searched lines %d-%d)", e.Symbol, e.Line, s.displayPath(), e.From, e.To) // appendix C, symbol-not-found
		}
		if err != nil {
			return err
		}
		if foundLine != line {
			s.noteSymbol = fmt.Sprintf("(symbol found on line %d)", foundLine)
		}
		line, column = foundLine, foundColumn
	}
	pos, clamped, err := ToLSPPosition(content, line, column, s.inst.Encoding())
	if e, ok := err.(*LineOutOfRangeError); ok {
		return fmt.Errorf("line %d is past the end of %s (%d lines)", e.Line, s.displayPath(), e.Lines) // appendix C, line-out-of-range
	}
	if err != nil {
		return err
	}
	if clamped {
		s.noteColumn = "(column clamped to end of line)"
	}
	s.line, s.column, s.pos = line, column, pos
	return nil
}

// checkCapability answers appendix C's unsupported error for an operation
// the server never declared.
func (s *queryState) checkCapability() error {
	capability, ok := operationCapabilities[s.q.Operation]
	if !ok {
		return nil // diagnostics: always available
	}
	if s.inst.Capabilities().Supports(capability) {
		return nil
	}
	return fmt.Errorf("language server %s does not support %s", s.srv.ID, s.q.Operation) // appendix C, unsupported
}

// call sends one request and maps its failure to appendix C.
func (s *queryState) call(ctx context.Context, method string, params, result any) error {
	if err := s.inst.Call(ctx, method, params, result); err != nil {
		return s.mapError(err)
	}
	return nil
}

// mapError renders one request failure as appendix C: a JSON-RPC error
// becomes server-error, a deadline becomes request-timeout, anything else
// passes through.
func (s *queryState) mapError(err error) error {
	var rpc *RPCError
	if errors.As(err, &rpc) {
		return fmt.Errorf("language server %s could not answer %s: %s", s.srv.ID, s.q.Operation, rpc.Message) // appendix C, server-error
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return s.timeoutError()
	}
	return err
}

func (s *queryState) timeoutError() error {
	return fmt.Errorf("language server %s did not answer %s within %ds", s.srv.ID, s.q.Operation, int(s.eff.RequestTimeout/time.Second)) // appendix C, request-timeout
}

func (s *queryState) outsideProjectError() error {
	return fmt.Errorf("%s is outside the project, so no language server covers it", s.displayPath()) // appendix C, outside-project
}

// displayPath is what error messages name the file by.
func (s *queryState) displayPath() string {
	if s.q.DisplayPath != "" {
		return s.q.DisplayPath
	}
	return s.q.AbsPath
}

// fileExt is the no-server error's language key: the lowercased extension,
// or the file name when there is none.
func (s *queryState) fileExt() string {
	if ext := strings.ToLower(filepath.Ext(s.q.AbsPath)); ext != "" {
		return ext
	}
	return filepath.Base(s.q.AbsPath)
}

// target is the symbol or line:column the header names.
func (s *queryState) target() string {
	switch {
	case s.q.Symbol != "":
		return s.q.Symbol
	case s.q.Line > 0:
		return fmt.Sprintf("%d:%d", s.q.Line, s.q.Column)
	default:
		return s.q.Query
	}
}

// queryRelPath is the query file's project-relative, slash-separated path.
func (s *queryState) queryRelPath() string {
	if rel, ok := relSlash(s.m.opts.ProjectRoot, filepath.Clean(s.q.AbsPath)); ok {
		return rel
	}
	return filepath.ToSlash(filepath.Clean(s.q.AbsPath))
}

// positionParams is the TextDocumentPositionParams of the resolved position.
func (s *queryState) positionParams() TextDocumentPositionParams {
	return TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: PathToURI(filepath.Clean(s.q.AbsPath))},
		Position:     s.pos,
	}
}

// runLocations answers definition, declaration, type_definition,
// implementation and references (appendix B.5's location list).
func (s *queryState) runLocations(ctx context.Context) (queryResult, error) {
	content, _, err := s.syncDocument(ctx)
	if err != nil {
		return queryResult{}, err
	}
	if err := s.resolvePosition(content); err != nil {
		return queryResult{}, err
	}
	if err := s.checkCapability(); err != nil {
		return queryResult{}, err
	}
	var raw json.RawMessage
	if s.q.Operation == tool.LSPOpReferences {
		var params ReferenceParams
		params.TextDocumentPositionParams = s.positionParams()
		params.Context.IncludeDeclaration = s.q.IncludeDeclaration
		err = s.call(ctx, locationMethods[s.q.Operation], params, &raw)
	} else {
		err = s.call(ctx, locationMethods[s.q.Operation], s.positionParams(), &raw)
	}
	if err != nil {
		return queryResult{}, err
	}
	locs, err := DecodeLocations(raw)
	if err != nil {
		return queryResult{}, err
	}
	return s.renderLocations(s.locationRows(locs)), nil
}

// queryLocation is one decoded location, ready to render.
type queryLocation struct {
	path    string
	display string // relative inside the project, absolute outside
	outside bool
	line    int
	column  int
	preview string
}

// locationRows decodes locations into display rows: paths relative to the
// project root (absolute, and without a source line, outside it), positions
// converted through the content the server has — or the disk, when the
// session may read it — else the raw numbers plus one (spec §8.2).
func (s *queryState) locationRows(locs []Location) []queryLocation {
	enc := s.inst.Encoding()
	contents := map[string][]byte{}
	rows := make([]queryLocation, 0, len(locs))
	for _, loc := range locs {
		path, err := URIToPath(loc.URI)
		if err != nil {
			continue // not a local file: nothing to point the model at
		}
		var content []byte
		if cached, seen := contents[path]; seen {
			content = cached
		} else {
			if c, _, open := s.docs.Content(path); open {
				content = c
			} else if s.previewAllowed(path) {
				content = readSmallFile(path)
			}
			contents[path] = content
		}
		line, column := FromLSPPosition(content, loc.Range.Start, enc)
		row := queryLocation{path: path, line: line, column: column}
		if rel, inside := relSlash(s.m.opts.ProjectRoot, path); inside {
			row.display = rel
		} else {
			row.display, row.outside = filepath.ToSlash(path), true
		}
		if !row.outside && s.previewAllowed(path) {
			if text, ok := LineText(content, line); ok {
				if trimmed := strings.TrimSpace(text); trimmed != "" {
					row.preview = truncateCodePoints(trimmed, 160)
				}
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// groupLocations groups rows by file, groups in first-appearance order and
// rows inside a group keeping their order (appendix B.5).
func groupLocations(rows []queryLocation) [][]queryLocation {
	groups := make([][]queryLocation, 0, len(rows))
	index := map[string]int{}
	for _, row := range rows {
		i, ok := index[row.path]
		if !ok {
			i = len(groups)
			index[row.path] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], row)
	}
	return groups
}

// renderLocations dedups, orders (references by path, line, column; the
// rest as the server answered), truncates and groups appendix B.5's
// location list.
func (s *queryState) renderLocations(rows []queryLocation) queryResult {
	seen := map[string]bool{}
	unique := make([]queryLocation, 0, len(rows))
	for _, row := range rows {
		key := fmt.Sprintf("%s\x00%d\x00%d", row.path, row.line, row.column)
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, row)
	}
	if s.q.Operation == tool.LSPOpReferences {
		sort.Slice(unique, func(a, b int) bool {
			if unique[a].path != unique[b].path {
				return unique[a].path < unique[b].path
			}
			if unique[a].line != unique[b].line {
				return unique[a].line < unique[b].line
			}
			return unique[a].column < unique[b].column
		})
	}
	more := 0
	if len(unique) > s.max {
		more = len(unique) - s.max
		unique = unique[:s.max]
	}
	if len(unique) == 0 {
		return queryResult{text: fmt.Sprintf("no results for %s at %s:%d:%d", s.q.Operation, s.queryRelPath(), s.line, s.column)}
	}

	var b strings.Builder
	groups := groupLocations(unique)
	files := len(groups)
	fmt.Fprintf(&b, "%s `%s`: %d result%s in %d file%s\n", locationLabels[s.q.Operation], s.target(), len(unique), plural(len(unique)), files, plural(files))
	for _, group := range groups {
		b.WriteString(group[0].display + "\n")
		for _, row := range group {
			line := fmt.Sprintf("  %d:%d", row.line, row.column)
			if row.preview != "" {
				line += "  " + row.preview
			}
			if row.outside {
				line += "  [outside project]"
			}
			b.WriteString(line + "\n")
		}
	}
	if more > 0 {
		fmt.Fprintf(&b, "… %d more not shown (raise max_results, at most 200)\n", more)
	}
	return queryResult{text: strings.TrimRight(b.String(), "\n"), count: len(unique), files: files}
}

// runHover answers hover (appendix B.5).
func (s *queryState) runHover(ctx context.Context) (queryResult, error) {
	content, _, err := s.syncDocument(ctx)
	if err != nil {
		return queryResult{}, err
	}
	if err := s.resolvePosition(content); err != nil {
		return queryResult{}, err
	}
	if err := s.checkCapability(); err != nil {
		return queryResult{}, err
	}
	var raw json.RawMessage
	if err := s.call(ctx, "textDocument/hover", s.positionParams(), &raw); err != nil {
		return queryResult{}, err
	}
	text, err := DecodeHover(raw)
	if err != nil {
		return queryResult{}, err
	}
	at := fmt.Sprintf("%s:%d:%d", s.queryRelPath(), s.line, s.column)
	if text == "" {
		return queryResult{text: "no hover information at " + at}, nil
	}
	return queryResult{text: "hover at " + at + "\n" + truncateCodePoints(text, 2000), count: 1, files: 1}, nil
}

// runDocumentSymbols answers document_symbols (appendix B.5): hierarchical
// symbols indent two spaces a level; flat ones never indent.
func (s *queryState) runDocumentSymbols(ctx context.Context) (queryResult, error) {
	if _, _, err := s.syncDocument(ctx); err != nil {
		return queryResult{}, err
	}
	if err := s.checkCapability(); err != nil {
		return queryResult{}, err
	}
	params := map[string]any{"textDocument": map[string]any{"uri": PathToURI(filepath.Clean(s.q.AbsPath))}}
	var raw json.RawMessage
	if err := s.call(ctx, "textDocument/documentSymbol", params, &raw); err != nil {
		return queryResult{}, err
	}
	hierarchical, flat, err := DecodeDocumentSymbols(raw)
	if err != nil {
		return queryResult{}, err
	}

	type symbolRow struct {
		indent             string
		kind, name         string
		startLine, endLine int
	}
	var rows []symbolRow
	var walk func(syms []DocumentSymbol, depth int)
	walk = func(syms []DocumentSymbol, depth int) {
		for _, sym := range syms {
			rows = append(rows, symbolRow{
				indent: strings.Repeat("  ", depth), kind: SymbolKindName(sym.Kind), name: sym.Name,
				startLine: int(sym.Range.Start.Line) + 1, endLine: int(sym.Range.End.Line) + 1,
			})
			walk(sym.Children, depth+1)
		}
	}
	walk(hierarchical, 0)
	for _, sym := range flat {
		rows = append(rows, symbolRow{
			kind: SymbolKindName(sym.Kind), name: sym.Name,
			startLine: int(sym.Location.Range.Start.Line) + 1, endLine: int(sym.Location.Range.End.Line) + 1,
		})
	}
	more := 0
	if len(rows) > s.max {
		more = len(rows) - s.max
		rows = rows[:s.max]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "symbols in %s\n", s.queryRelPath())
	for _, row := range rows {
		fmt.Fprintf(&b, "%s%s %s  %d-%d\n", row.indent, row.kind, row.name, row.startLine, row.endLine)
	}
	if more > 0 {
		fmt.Fprintf(&b, "… %d more not shown (raise max_results, at most 200)\n", more)
	}
	files := 1
	if len(rows) == 0 {
		files = 0
	}
	return queryResult{text: strings.TrimRight(b.String(), "\n"), count: len(rows), files: files}, nil
}

// runWorkspaceSymbols answers workspace_symbols (appendix B.5), resolving
// the first max_results items whose location came without a range when the
// server offers workspaceSymbol/resolve.
func (s *queryState) runWorkspaceSymbols(ctx context.Context) (queryResult, error) {
	if err := s.checkCapability(); err != nil {
		return queryResult{}, err
	}
	var raw json.RawMessage
	if err := s.call(ctx, "workspace/symbol", map[string]any{"query": s.q.Query}, &raw); err != nil {
		return queryResult{}, err
	}
	elements, err := rawElements(raw, "workspace symbols")
	if err != nil {
		return queryResult{}, err
	}
	resolve := s.resolveSupport()
	type symbolRow struct {
		kind, name, display string
		line                int
		hasLine             bool
		outside             bool
	}
	rows := make([]symbolRow, 0, len(elements))
	for i, element := range elements {
		var sym WorkspaceSymbol
		if err := json.Unmarshal(element, &sym); err != nil {
			return queryResult{}, fmt.Errorf("lsp: decoding workspace symbols: %w", err)
		}
		uri, line, hasRange := decodeWorkspaceLocation(sym.Location)
		if !hasRange && resolve && i < s.max {
			var resolved SymbolInformation
			if err := s.call(ctx, "workspaceSymbol/resolve", json.RawMessage(element), &resolved); err != nil {
				return queryResult{}, err
			}
			uri, line, hasRange = resolved.Location.URI, int(resolved.Location.Range.Start.Line)+1, true
		}
		row := symbolRow{kind: SymbolKindName(sym.Kind), name: sym.Name}
		if path, err := URIToPath(uri); err == nil {
			if rel, inside := relSlash(s.m.opts.ProjectRoot, path); inside {
				row.display = rel
			} else {
				row.display, row.outside = filepath.ToSlash(path), true
			}
		}
		row.line, row.hasLine = line, hasRange
		rows = append(rows, row)
	}
	more := 0
	if len(rows) > s.max {
		more = len(rows) - s.max
		rows = rows[:s.max]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "workspace symbols matching `%s`: %d\n", s.q.Query, len(rows))
	for _, row := range rows {
		fmt.Fprintf(&b, "%s %s  %s", row.kind, row.name, row.display)
		if row.hasLine {
			fmt.Fprintf(&b, ":%d", row.line)
		}
		if row.outside {
			b.WriteString("  [outside project]")
		}
		b.WriteString("\n")
	}
	if more > 0 {
		fmt.Fprintf(&b, "… %d more not shown (raise max_results, at most 200)\n", more)
	}
	displays := map[string]bool{}
	for _, row := range rows {
		displays[row.display] = true
	}
	return queryResult{text: strings.TrimRight(b.String(), "\n"), count: len(rows), files: len(displays)}, nil
}

// resolveSupport reports whether the server declared
// workspaceSymbolProvider.resolveProvider.
func (s *queryState) resolveSupport() bool {
	caps := s.inst.Capabilities()
	raw, ok := caps["workspaceSymbolProvider"]
	if !ok {
		return false
	}
	var provider struct {
		ResolveProvider bool `json:"resolveProvider"`
	}
	return json.Unmarshal(raw, &provider) == nil && provider.ResolveProvider
}

// decodeWorkspaceLocation reads a workspace symbol's raw location, which
// may be a full Location or only a uri.
func decodeWorkspaceLocation(raw json.RawMessage) (uri string, line int, hasRange bool) {
	var loc struct {
		URI   string `json:"uri"`
		Range *Range `json:"range"`
	}
	if json.Unmarshal(raw, &loc) != nil || loc.URI == "" {
		return "", 0, false
	}
	if loc.Range != nil {
		return loc.URI, int(loc.Range.Start.Line) + 1, true
	}
	return loc.URI, 0, false
}

// runCallHierarchy answers incoming_calls and outgoing_calls (appendix
// B.5): prepare, then the first item's callers or calls.
func (s *queryState) runCallHierarchy(ctx context.Context) (queryResult, error) {
	content, _, err := s.syncDocument(ctx)
	if err != nil {
		return queryResult{}, err
	}
	if err := s.resolvePosition(content); err != nil {
		return queryResult{}, err
	}
	if err := s.checkCapability(); err != nil {
		return queryResult{}, err
	}
	var prepared []CallHierarchyItem
	if err := s.call(ctx, "textDocument/prepareCallHierarchy", s.positionParams(), &prepared); err != nil {
		return queryResult{}, err
	}
	name := s.target()
	var rows []namedRow
	if len(prepared) > 0 {
		name = prepared[0].Name
		if s.q.Operation == tool.LSPOpIncomingCalls {
			var calls []CallHierarchyIncomingCall
			if err := s.call(ctx, "callHierarchy/incomingCalls", map[string]any{"item": prepared[0]}, &calls); err != nil {
				return queryResult{}, err
			}
			for _, call := range calls {
				rows = append(rows, s.itemRow(call.From.Name, call.From.URI, call.From.SelectionRange))
			}
		} else {
			var calls []CallHierarchyOutgoingCall
			if err := s.call(ctx, "callHierarchy/outgoingCalls", map[string]any{"item": prepared[0]}, &calls); err != nil {
				return queryResult{}, err
			}
			for _, call := range calls {
				rows = append(rows, s.itemRow(call.To.Name, call.To.URI, call.To.SelectionRange))
			}
		}
	}
	label := "callers of"
	if s.q.Operation == tool.LSPOpOutgoingCalls {
		label = "calls made by"
	}
	return s.renderNamedList(label, name, rows), nil
}

// runTypeHierarchy answers supertypes and subtypes (appendix B.5).
func (s *queryState) runTypeHierarchy(ctx context.Context) (queryResult, error) {
	content, _, err := s.syncDocument(ctx)
	if err != nil {
		return queryResult{}, err
	}
	if err := s.resolvePosition(content); err != nil {
		return queryResult{}, err
	}
	if err := s.checkCapability(); err != nil {
		return queryResult{}, err
	}
	var prepared []TypeHierarchyItem
	if err := s.call(ctx, "textDocument/prepareTypeHierarchy", s.positionParams(), &prepared); err != nil {
		return queryResult{}, err
	}
	name := s.target()
	var rows []namedRow
	if len(prepared) > 0 {
		name = prepared[0].Name
		method := "typeHierarchy/supertypes"
		label := "supertypes of"
		if s.q.Operation == tool.LSPOpSubtypes {
			method, label = "typeHierarchy/subtypes", "subtypes of"
		}
		var items []TypeHierarchyItem
		if err := s.call(ctx, method, map[string]any{"item": prepared[0]}, &items); err != nil {
			return queryResult{}, err
		}
		for _, item := range items {
			rows = append(rows, s.itemRow(item.Name, item.URI, item.SelectionRange))
		}
		return s.renderNamedList(label, name, rows), nil
	}
	label := "supertypes of"
	if s.q.Operation == tool.LSPOpSubtypes {
		label = "subtypes of"
	}
	return s.renderNamedList(label, name, rows), nil
}

// itemRow is one hierarchy row: a name at a file:line, without a preview.
func (s *queryState) itemRow(name, uri string, sel Range) namedRow {
	row := namedRow{name: name, line: int(sel.Start.Line) + 1}
	if path, err := URIToPath(uri); err == nil {
		row.path = path
		if rel, inside := relSlash(s.m.opts.ProjectRoot, path); inside {
			row.display = rel
		} else {
			row.display, row.outside = filepath.ToSlash(path), true
		}
	}
	return row
}

// namedRow is one row of the calls and hierarchy lists.
type namedRow struct {
	name    string
	path    string
	display string
	line    int
	outside bool
}

// renderNamedList renders the calls/hierarchy form: a `{label} `{name}`: {N}`
// header and `{name}  {path}:{line}` rows.
func (s *queryState) renderNamedList(label, name string, rows []namedRow) queryResult {
	more := 0
	if len(rows) > s.max {
		more = len(rows) - s.max
		rows = rows[:s.max]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s `%s`: %d\n", label, name, len(rows))
	for _, row := range rows {
		fmt.Fprintf(&b, "%s  %s", row.name, row.display)
		if row.path != "" {
			fmt.Fprintf(&b, ":%d", row.line)
		}
		if row.outside {
			b.WriteString("  [outside project]")
		}
		b.WriteString("\n")
	}
	if more > 0 {
		fmt.Fprintf(&b, "… %d more not shown (raise max_results, at most 200)\n", more)
	}
	paths := map[string]bool{}
	for _, row := range rows {
		paths[row.path] = true
	}
	return queryResult{text: strings.TrimRight(b.String(), "\n"), count: len(rows), files: len(paths)}
}

// runDiagnostics answers diagnostics for one file: pull when the server
// can, otherwise the store, waiting at most min(remainder, 2s) for the
// synced version (spec §8.1).
func (s *queryState) runDiagnostics(ctx context.Context) (queryResult, error) {
	_, version, err := s.syncDocument(ctx)
	if err != nil {
		return queryResult{}, err
	}
	path := filepath.Clean(s.q.AbsPath)
	if _, err := PullDiagnostics(ctx, s.inst, s.m.pool.diags, s.srv.ID, path); err != nil {
		return queryResult{}, s.mapError(err)
	}
	problems, stored, _, ok := s.m.pool.diags.Get(s.srv.ID, path)
	if !ok || (stored != nil && *stored < version) {
		wait := 2 * time.Second
		if deadline, has := ctx.Deadline(); has && time.Until(deadline) < wait {
			wait = time.Until(deadline)
		}
		waitCtx, waitCancel := context.WithTimeout(ctx, wait)
		s.m.pool.diags.WaitFor(waitCtx, s.srv.ID, path, version, 0)
		waitCancel()
		problems, _, _, _ = s.m.pool.diags.Get(s.srv.ID, path)
	}
	files, hiddenFiles, total := layoutProblems(problems, s.m.reportOptions(s.eff, nil))
	var b strings.Builder
	fmt.Fprintf(&b, "diagnostics for %s: %d\n", s.queryRelPath(), total)
	s.writeProblemFiles(&b, files, hiddenFiles)
	return queryResult{text: strings.TrimRight(b.String(), "\n"), count: total, files: len(files)}, nil
}

// queryDiagnosticsSummary answers diagnostics without a file: the problems
// of every document this manager's running instances hold open.
func (m *Manager) queryDiagnosticsSummary(s *queryState, startedAt time.Time) (tool.CodeIntelResult, error) {
	var problems []Problem
	for _, entry := range m.pool.entriesFor(m) {
		for _, path := range m.pool.openPaths(entry) {
			if stored, _, _, ok := m.pool.diags.Get(entry.serverID, path); ok {
				problems = append(problems, stored...)
			}
		}
	}
	files, hiddenFiles, total := layoutProblems(problems, m.reportOptions(s.eff, nil))
	var b strings.Builder
	fmt.Fprintf(&b, "diagnostics in open files: %d\n", total)
	s.writeProblemFiles(&b, files, hiddenFiles)
	return tool.CodeIntelResult{
		Text: strings.TrimRight(b.String(), "\n"),
		Display: map[string]any{
			"operation":    tool.LSPOpDiagnostics,
			"server":       "",
			"result_count": total,
			"files":        len(files),
			"elapsed_ms":   time.Since(startedAt).Milliseconds(),
			"indexing":     false,
			"target":       s.target(),
			"display_path": s.displayPath(),
		},
	}, nil
}

// writeProblemFiles renders the B.3 file and problem lines a diagnostics
// query reports.
func (s *queryState) writeProblemFiles(b *strings.Builder, files []fileReport, hiddenFiles int) {
	opts := s.m.reportOptions(s.eff, nil)
	for _, f := range files {
		b.WriteString(f.rel + "\n")
		for _, p := range f.shown {
			b.WriteString(problemLine(p, opts) + "\n")
		}
		if f.more > 0 {
			fmt.Fprintf(b, "  … %d more in this file not shown\n", f.more)
		}
	}
	if hiddenFiles > 0 {
		fmt.Fprintf(b, "… %d more files not shown\n", hiddenFiles)
	}
}

// previewAllowed reports whether a result location may carry a preview or
// be read from the disk for conversion.
func (s *queryState) previewAllowed(path string) bool {
	return s.q.PreviewAllowed != nil && s.q.PreviewAllowed(path)
}

// readSmallFile reads at most a 4 MiB file for position conversion; nil
// when it cannot.
func readSmallFile(path string) []byte {
	info, err := os.Stat(path)
	if err != nil || info.Size() > MaxDocumentBytes {
		return nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return content
}

// truncateCodePoints keeps at most max characters, marking a cut with … .
func truncateCodePoints(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}

// plural is appendix B's {s}: empty at one, "s" otherwise.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
