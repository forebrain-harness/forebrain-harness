package lsp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Position is a zero-based line and character offset in the negotiated
// position encoding.
type Position struct {
	Line      uint32 `json:"line"`
	Character uint32 `json:"character"`
}

// Range is a half-open range between two positions.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location points at a range in a document.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// LocationLink is the richer location form servers with linkSupport return.
type LocationLink struct {
	OriginSelectionRange *Range `json:"originSelectionRange,omitempty"`
	TargetURI            string `json:"targetUri"`
	TargetRange          Range  `json:"targetRange"`
	TargetSelectionRange Range  `json:"targetSelectionRange"`
}

// TextDocumentIdentifier names a document by URI.
type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

// VersionedTextDocumentIdentifier names a document and its version.
type VersionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int32  `json:"version"`
}

// TextDocumentItem is a full document as sent with didOpen.
type TextDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int32  `json:"version"`
	Text       string `json:"text"`
}

// TextDocumentPositionParams points at a position in a document.
type TextDocumentPositionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// ReferenceParams asks for references to the symbol at a position.
type ReferenceParams struct {
	TextDocumentPositionParams
	Context struct {
		IncludeDeclaration bool `json:"includeDeclaration"`
	} `json:"context"`
}

// Diagnostic is one reported problem. Severity 1 is error, 2 warning,
// 3 information, 4 hint; 0 is unspecified and treated as an error.
type Diagnostic struct {
	Range    Range           `json:"range"`
	Severity int             `json:"severity,omitempty"`
	Code     json.RawMessage `json:"code,omitempty"` // number or string; see DiagnosticCode
	Source   string          `json:"source,omitempty"`
	Message  string          `json:"message"`
}

// PublishDiagnosticsParams is the textDocument/publishDiagnostics payload.
type PublishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Version     *int32       `json:"version,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// DocumentDiagnosticReport is one document's slice of a pull-diagnostics
// report; Kind is "full" or "unchanged".
type DocumentDiagnosticReport struct {
	Kind             string                              `json:"kind"`
	ResultID         string                              `json:"resultId,omitempty"`
	Items            []Diagnostic                        `json:"items,omitempty"`
	RelatedDocuments map[string]DocumentDiagnosticReport `json:"relatedDocuments,omitempty"`
}

// DocumentSymbol is the hierarchical form of a document symbol.
type DocumentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           int              `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

// SymbolInformation is the flat form of a document symbol.
type SymbolInformation struct {
	Name          string   `json:"name"`
	Kind          int      `json:"kind"`
	Location      Location `json:"location"`
	ContainerName string   `json:"containerName,omitempty"`
}

// WorkspaceSymbol is one workspace/symbol hit. Servers that resolve lazily
// send a location without a range, so Location stays raw.
type WorkspaceSymbol struct {
	Name          string          `json:"name"`
	Kind          int             `json:"kind"`
	ContainerName string          `json:"containerName,omitempty"`
	Location      json.RawMessage `json:"location"`
}

// CallHierarchyItem is one node of a call hierarchy.
type CallHierarchyItem struct {
	Name           string          `json:"name"`
	Kind           int             `json:"kind"`
	Detail         string          `json:"detail,omitempty"`
	URI            string          `json:"uri"`
	Range          Range           `json:"range"`
	SelectionRange Range           `json:"selectionRange"`
	Data           json.RawMessage `json:"data,omitempty"`
}

// CallHierarchyIncomingCall is one caller of a call-hierarchy item.
type CallHierarchyIncomingCall struct {
	From       CallHierarchyItem `json:"from"`
	FromRanges []Range           `json:"fromRanges"`
}

// CallHierarchyOutgoingCall is one callee of a call-hierarchy item.
type CallHierarchyOutgoingCall struct {
	To         CallHierarchyItem `json:"to"`
	FromRanges []Range           `json:"fromRanges"`
}

// TypeHierarchyItem has the same shape as CallHierarchyItem.
type TypeHierarchyItem = CallHierarchyItem

// WorkspaceFolder is one root the client tells the server about.
type WorkspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

// ProgressParams carries one progress notification.
type ProgressParams struct {
	Token json.RawMessage `json:"token"`
	Value json.RawMessage `json:"value"`
}

// WorkDoneProgressValue is the begin/report/end shape of work done progress.
type WorkDoneProgressValue struct {
	Kind       string `json:"kind"`
	Title      string `json:"title,omitempty"`
	Message    string `json:"message,omitempty"`
	Percentage *int   `json:"percentage,omitempty"`
}

// MessageActionItem is one button of a window/showMessageRequest.
type MessageActionItem struct {
	Title string `json:"title"`
}

// ShowMessageRequestParams asks the user to pick an action.
type ShowMessageRequestParams struct {
	Type    int                 `json:"type"`
	Message string              `json:"message"`
	Actions []MessageActionItem `json:"actions,omitempty"`
}

// ConfigurationItem names one configuration section a server asks about.
type ConfigurationItem struct {
	ScopeURI string `json:"scopeUri,omitempty"`
	Section  string `json:"section,omitempty"`
}

// ConfigurationParams is the workspace/configuration request.
type ConfigurationParams struct {
	Items []ConfigurationItem `json:"items"`
}

// Registration is one dynamically registered capability.
type Registration struct {
	ID              string          `json:"id"`
	Method          string          `json:"method"`
	RegisterOptions json.RawMessage `json:"registerOptions,omitempty"`
}

// RegistrationParams is the client/registerCapability request.
type RegistrationParams struct {
	Registrations []Registration `json:"registrations"`
}

// FileEvent is one watched-file change: Type 1 created, 2 changed, 3 deleted.
type FileEvent struct {
	URI  string `json:"uri"`
	Type int    `json:"type"`
}

// ServerCapabilities keeps the raw capability object (spec §7.2).
type ServerCapabilities map[string]json.RawMessage

// Supports reports whether the server declared capability name: a key that is
// missing, null, or false means it did not.
func (c ServerCapabilities) Supports(name string) bool {
	raw, ok := c[name]
	if !ok {
		return false
	}
	v := bytes.TrimSpace(raw)
	return len(v) > 0 && !bytes.Equal(v, []byte("null")) && !bytes.Equal(v, []byte("false"))
}

// PositionEncoding returns the negotiated position encoding; LSP defaults to
// utf-16 when the server does not say.
func (c ServerCapabilities) PositionEncoding() string {
	raw, ok := c["positionEncoding"]
	if !ok {
		return string(EncodingUTF16)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || s == "" {
		return string(EncodingUTF16)
	}
	return s
}

// SaveIncludesText reports whether textDocumentSync asks didSave to include
// the document text.
func (c ServerCapabilities) SaveIncludesText() bool {
	raw, ok := c["textDocumentSync"]
	if !ok {
		return false
	}
	var sync struct {
		Save *struct {
			IncludeText bool `json:"includeText"`
		} `json:"save"`
	}
	if err := json.Unmarshal(raw, &sync); err != nil {
		return false // a bare sync-kind number has no save options
	}
	return sync.Save != nil && sync.Save.IncludeText
}

// WorkspaceFolderChanges reports whether the server watches workspace folder
// additions, removals, and renames (changeNotifications).
func (c ServerCapabilities) WorkspaceFolderChanges() bool {
	raw, ok := c["workspace"]
	if !ok {
		return false
	}
	var ws struct {
		WorkspaceFolders *struct {
			ChangeNotifications json.RawMessage `json:"changeNotifications"`
		} `json:"workspaceFolders"`
	}
	if err := json.Unmarshal(raw, &ws); err != nil || ws.WorkspaceFolders == nil {
		return false
	}
	notifications := bytes.TrimSpace(ws.WorkspaceFolders.ChangeNotifications)
	if bytes.Equal(notifications, []byte("true")) {
		return true
	}
	var id string
	return json.Unmarshal(notifications, &id) == nil && id != ""
}

// DecodeLocations decodes a definition/references-style reply: null, a single
// Location, []Location, or []LocationLink (a link maps to its targetUri and
// targetSelectionRange).
func DecodeLocations(raw json.RawMessage) ([]Location, error) {
	elements, err := rawElements(raw, "locations")
	if err != nil {
		return nil, err
	}
	locs := make([]Location, 0, len(elements))
	for _, element := range elements {
		var probe struct {
			URI                  string `json:"uri"`
			Range                Range  `json:"range"`
			TargetURI            string `json:"targetUri"`
			TargetSelectionRange Range  `json:"targetSelectionRange"`
		}
		if err := json.Unmarshal(element, &probe); err != nil {
			return nil, fmt.Errorf("lsp: decoding location: %w", err)
		}
		if probe.TargetURI != "" {
			locs = append(locs, Location{URI: probe.TargetURI, Range: probe.TargetSelectionRange})
			continue
		}
		locs = append(locs, Location{URI: probe.URI, Range: probe.Range})
	}
	return locs, nil
}

// rawElements normalizes a reply into its elements: null or empty is nothing,
// an array is itself, and a single object becomes a one-element slice.
func rawElements(raw json.RawMessage, what string) ([]json.RawMessage, error) {
	v := bytes.TrimSpace(raw)
	if len(v) == 0 || bytes.Equal(v, []byte("null")) {
		return nil, nil
	}
	if v[0] == '[' {
		var elements []json.RawMessage
		if err := json.Unmarshal(v, &elements); err != nil {
			return nil, fmt.Errorf("lsp: decoding %s: %w", what, err)
		}
		return elements, nil
	}
	return []json.RawMessage{v}, nil
}

// DecodeHover renders a hover reply as plain text: contents may be
// MarkupContent, a plain string, a {language, value} pair, or an array of
// those, joined with blank lines.
func DecodeHover(raw json.RawMessage) (string, error) {
	v := bytes.TrimSpace(raw)
	if len(v) == 0 || bytes.Equal(v, []byte("null")) {
		return "", nil
	}
	var hover struct {
		Contents json.RawMessage `json:"contents"`
	}
	if err := json.Unmarshal(v, &hover); err != nil {
		return "", fmt.Errorf("lsp: decoding hover: %w", err)
	}
	parts, err := hoverParts(hover.Contents)
	if err != nil {
		return "", err
	}
	return strings.Join(parts, "\n\n"), nil
}

func hoverParts(raw json.RawMessage) ([]string, error) {
	elements, err := rawElements(raw, "hover contents")
	if err != nil {
		return nil, err
	}
	var parts []string
	for _, element := range elements {
		v := bytes.TrimSpace(element)
		var s string
		if json.Unmarshal(v, &s) == nil {
			if s != "" {
				parts = append(parts, s)
			}
			continue
		}
		var obj struct {
			Kind     string `json:"kind"`
			Value    string `json:"value"`
			Language string `json:"language"`
		}
		if err := json.Unmarshal(v, &obj); err != nil {
			return nil, fmt.Errorf("lsp: decoding hover contents: %w", err)
		}
		switch {
		case obj.Language != "":
			parts = append(parts, "```"+obj.Language+"\n"+obj.Value+"\n```")
		case obj.Value != "":
			parts = append(parts, obj.Value)
		}
	}
	return parts, nil
}

// DecodeDocumentSymbols decodes a textDocument/documentSymbol reply, which is
// either hierarchical DocumentSymbol[] or flat SymbolInformation[]; the first
// element decides. null decodes to neither.
func DecodeDocumentSymbols(raw json.RawMessage) ([]DocumentSymbol, []SymbolInformation, error) {
	v := bytes.TrimSpace(raw)
	if len(v) == 0 || bytes.Equal(v, []byte("null")) {
		return nil, nil, nil
	}
	var elements []json.RawMessage
	if err := json.Unmarshal(v, &elements); err != nil {
		return nil, nil, fmt.Errorf("lsp: decoding document symbols: %w", err)
	}
	if len(elements) == 0 {
		return nil, nil, nil
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(elements[0], &probe); err != nil {
		return nil, nil, fmt.Errorf("lsp: decoding document symbols: %w", err)
	}
	if _, flat := probe["location"]; flat {
		var symbols []SymbolInformation
		if err := json.Unmarshal(v, &symbols); err != nil {
			return nil, nil, fmt.Errorf("lsp: decoding document symbols: %w", err)
		}
		return nil, symbols, nil
	}
	var symbols []DocumentSymbol
	if err := json.Unmarshal(v, &symbols); err != nil {
		return nil, nil, fmt.Errorf("lsp: decoding document symbols: %w", err)
	}
	return symbols, nil, nil
}

// DiagnosticCode renders a diagnostic code, which may be a number or a
// string, as display text.
func DiagnosticCode(raw json.RawMessage) string {
	v := bytes.TrimSpace(raw)
	if len(v) == 0 || bytes.Equal(v, []byte("null")) {
		return ""
	}
	var num json.Number
	if err := json.Unmarshal(v, &num); err == nil {
		return num.String()
	}
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		return s
	}
	return ""
}

// SeverityName names a diagnostic severity; 0 is unspecified and treated as
// error, and unknown numbers have no name.
func SeverityName(sev int) string {
	switch sev {
	case 0, 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "info"
	case 4:
		return "hint"
	}
	return ""
}

var symbolKindNames = [...]string{
	"file", "module", "namespace", "package", "class", "method", "property",
	"field", "constructor", "enum", "interface", "function", "variable",
	"constant", "string", "number", "boolean", "array", "object", "key",
	"null", "enummember", "struct", "event", "operator", "typeparameter",
}

// SymbolKindName names an LSP SymbolKind number; anything else is "symbol".
func SymbolKindName(kind int) string {
	if kind >= 1 && kind <= len(symbolKindNames) {
		return symbolKindNames[kind-1]
	}
	return "symbol"
}
