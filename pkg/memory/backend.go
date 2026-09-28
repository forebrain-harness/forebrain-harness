package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxListResults = 2000
	// MaxSearchResults is the most matches one search call may return, whatever
	// the configured top K asks for. It is small because results are ranked:
	// the index recalls every line sharing a term with the query, which for a
	// common word is hundreds of them, and BM25 puts the few worth reading at
	// the top. The rest is context the model pays for and should not be sold.
	MaxSearchResults     = 20
	DefaultReadMaxTokens = 20_000
)

// Backend addresses one primary root plus zero or more named mounts. A
// dedicated-tools session gets one Backend with the session's project root as
// Root and the agent's global (cross-project) root mounted at "global/" — see
// NewScoped. Every public method routes an incoming path to whichever root it
// names before touching disk, and stitches the mount prefix back onto paths in
// the response, so from the model's point of view "global/MEMORY.md" reads and
// lists exactly like any other path under the (single) memories root it was
// told about, while the two roots stay on disk exactly as separate as their
// consolidation passes require.
type Backend struct {
	Root   string
	mounts []mount
	index  *searchIndex
}

// WithSearchIndex returns a copy of b whose searches also consult the FTS5
// index in db: lines that share a term with a query become candidates, and
// every result is ordered by its bm25 relevance instead of by path. A Backend
// without one still searches — by exact substring alone, in path order.
func (b Backend) WithSearchIndex(db *sql.DB) Backend {
	b.index = newSearchIndex(db)
	return b
}

type mount struct {
	prefix string
	root   string
}

// NewScoped builds a Backend for one memory-reading session: projectRoot is
// the session's project scope (blank when the session has no project
// identity — see ProjectScopeForCwd), globalRoot is the agent's
// single cross-project scope. globalRoot is always mounted at "global/" when
// non-blank; a blank projectRoot still allows reaching global/, it just has no
// primary root of its own to operate on directly.
func NewScoped(projectRoot, globalRoot string) Backend {
	b := New(projectRoot)
	if root := strings.TrimSpace(globalRoot); root != "" {
		b.mounts = []mount{{prefix: "global", root: filepath.Clean(root)}}
	}
	return b
}

// route resolves which physical root a request path belongs to. A path equal
// to a mount's prefix, or prefixed with "<prefix>/", routes to that mount with
// the prefix stripped; everything else — including a nil or blank path, which
// means "the root itself" — stays on the primary root. mounted is false for
// the primary-root case, which callers use to know a response path needs no
// prefix stitched back on.
func (b Backend) route(path *string) (target Backend, routed *string, prefix string, mounted bool) {
	if path == nil {
		return b, path, "", false
	}
	trimmed := strings.TrimSpace(*path)
	for _, m := range b.mounts {
		if trimmed == m.prefix {
			sub := ""
			return b.mountBackend(m.root), &sub, m.prefix, true
		}
		if rest, ok := strings.CutPrefix(trimmed, m.prefix+"/"); ok {
			return b.mountBackend(m.root), &rest, m.prefix, true
		}
	}
	return b, path, "", false
}

// joinDisplay stitches a mount prefix back onto a path relative to that
// mount's own root, matching the form displayPath uses for the primary root.
func joinDisplay(prefix, path string) string {
	if prefix == "" {
		return path
	}
	if path == "" {
		return prefix
	}
	return prefix + "/" + path
}

type EntryType string

const (
	EntryFile      EntryType = "file"
	EntryDirectory EntryType = "directory"
)

type Entry struct {
	Path      string    `json:"path"`
	EntryType EntryType `json:"entry_type"`
}

type ListRequest struct {
	Path       *string
	Cursor     *string
	MaxResults int
}

type ListResponse struct {
	Path       *string `json:"path"`
	Entries    []Entry `json:"entries"`
	NextCursor *string `json:"next_cursor"`
	Truncated  bool    `json:"truncated"`
}

type ReadRequest struct {
	Path       string
	LineOffset int
	MaxLines   *int
	MaxTokens  int
}

type ReadResponse struct {
	Path            string `json:"path"`
	StartLineNumber int    `json:"start_line_number"`
	Content         string `json:"content"`
	Truncated       bool   `json:"truncated"`
}

type SearchRequest struct {
	Queries []string
	Path    *string
	// TopK is how many of the ranked matches to return. Callers resolve it from
	// configuration; the backend clamps it to the same ceiling so no caller can
	// widen the budget by passing a larger number.
	TopK int
}

type SearchMatch struct {
	Path                   string   `json:"path"`
	MatchLineNumber        int      `json:"match_line_number"`
	ContentStartLineNumber int      `json:"content_start_line_number"`
	Content                string   `json:"content"`
	MatchedQueries         []string `json:"matched_queries"`
	// MatchedTerms are the terms the query and the matched line share, which is
	// what actually occurs in Content. A query and the text that answered it
	// are not the same string once segmentation is involved, so a surface that
	// highlights what matched needs this rather than MatchedQueries.
	MatchedTerms []string `json:"matched_terms,omitempty"`
	// score ranks the result and is not part of the tool payload: the order it
	// produces is the answer, and the number behind it would only spend the
	// model's context.
	score float64
}

type SearchResponse struct {
	Queries []string      `json:"queries"`
	Path    *string       `json:"path"`
	Matches []SearchMatch `json:"matches"`
}

// ErrNotFound marks a path that does not exist under the memories root, so
// callers can tell a missing file apart from an integrity or I/O failure
// instead of matching on message text.
var ErrNotFound = errors.New("was not found")

func notFoundError(path string) error { return fmt.Errorf("path '%s' %w", path, ErrNotFound) }

// rootRelative converts an absolute path naming a file inside root into its
// root-relative form. It reports false for anything that escapes root, so the
// per-component symlink, dot-prefix, and traversal checks in resolve still run
// on exactly the components that stay inside.
func rootRelative(root, path string) (string, bool) {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return "", true
	}
	return rel, true
}

func New(root string) Backend { return Backend{Root: filepath.Clean(strings.TrimSpace(root))} }

// mountBackend is the Backend a mounted root is served by. It carries the
// index: the mount is another memory root of the same agent, indexed in the
// same table under its own root key, so a search of global/ is ranked exactly
// the way a search of the project scope is.
func (b Backend) mountBackend(root string) Backend {
	target := New(root)
	target.index = b.index
	return target
}

func (b Backend) resolve(path *string) (string, error) {
	if strings.TrimSpace(b.Root) == "" || b.Root == "." {
		return "", fmt.Errorf("memory root is empty")
	}
	// A missing, empty, or blank path all mean the memories root. Empty already
	// resolved to the root by falling through the component loop; blank is
	// spelled out so a model that pads the field instead of omitting it lands
	// in the same place rather than on a "was not found" error.
	if path == nil || strings.TrimSpace(*path) == "" {
		return b.Root, nil
	}
	raw := *path
	rel := filepath.FromSlash(raw)
	if filepath.IsAbs(rel) {
		// The memory instructions describe the layout with absolute paths, so a
		// model that copies one verbatim must land on the same file its
		// root-relative form names instead of on a rejection. Anything outside
		// the root is still refused.
		within, ok := rootRelative(b.Root, rel)
		if !ok {
			return "", fmt.Errorf("path '%s' must stay within the memories root", raw)
		}
		rel = within
	}
	components := strings.FieldsFunc(filepath.ToSlash(rel), func(r rune) bool { return r == '/' })
	current := b.Root
	for index, component := range components {
		if component == "." {
			continue
		}
		if component == ".." {
			return "", fmt.Errorf("path '%s' must stay within the memories root", raw)
		}
		if strings.HasPrefix(component, ".") {
			return "", notFoundError(raw)
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			for _, remaining := range components[index+1:] {
				current = filepath.Join(current, remaining)
			}
			return current, nil
		}
		if err != nil {
			return "", fmt.Errorf("I/O error while reading memories: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("path '%s' must not be a symlink", displayPath(b.Root, current))
		}
		if index+1 < len(components) && !info.IsDir() {
			return "", fmt.Errorf("path '%s' traverses through a non-directory path component", raw)
		}
	}
	return current, nil
}

// List routes to the primary root or, for a "global"-prefixed path, to the
// global mount. Listing the primary root itself (path nil or blank) appends a
// synthetic "global" directory entry when a mount is configured, so the model
// can discover the cross-project scope by listing rather than only from the
// prose that describes it.
func (b Backend) List(request ListRequest) (ListResponse, error) {
	original := request.Path
	target, routed, prefix, mounted := b.route(request.Path)
	request.Path = routed
	response, err := target.list(request)
	if err != nil {
		return ListResponse{}, err
	}
	if mounted {
		for i := range response.Entries {
			response.Entries[i].Path = joinDisplay(prefix, response.Entries[i].Path)
		}
	} else if len(b.mounts) > 0 && (original == nil || strings.TrimSpace(*original) == "") {
		for _, m := range b.mounts {
			response.Entries = append(response.Entries, Entry{Path: m.prefix, EntryType: EntryDirectory})
		}
		sort.Slice(response.Entries, func(i, j int) bool { return response.Entries[i].Path < response.Entries[j].Path })
	}
	response.Path = original
	return response, nil
}

func (b Backend) list(request ListRequest) (ListResponse, error) {
	if request.MaxResults < 0 {
		return ListResponse{}, fmt.Errorf("max_results must be a non-negative integer")
	}
	maxResults := request.MaxResults
	if maxResults > MaxListResults {
		maxResults = MaxListResults
	}
	if maxResults < 0 {
		maxResults = 0
	}
	startIndex, err := parseCursor(request.Cursor)
	if err != nil {
		return ListResponse{}, err
	}
	path, err := b.resolve(request.Path)
	if err != nil {
		return ListResponse{}, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ListResponse{}, notFoundError(stringValue(request.Path))
	}
	if err != nil {
		return ListResponse{}, fmt.Errorf("I/O error while reading memories: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ListResponse{}, fmt.Errorf("path '%s' must not be a symlink", displayPath(b.Root, path))
	}
	var entries []Entry
	if info.Mode().IsRegular() {
		entries = []Entry{{Path: displayPath(b.Root, path), EntryType: EntryFile}}
	} else if info.IsDir() {
		dirEntries, err := os.ReadDir(path)
		if err != nil {
			return ListResponse{}, fmt.Errorf("I/O error while reading memories: %w", err)
		}
		for _, entry := range dirEntries {
			if strings.HasPrefix(entry.Name(), ".") || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			entryType := EntryFile
			if entry.IsDir() {
				entryType = EntryDirectory
			} else if !entry.Type().IsRegular() {
				continue
			}
			entries = append(entries, Entry{Path: displayPath(b.Root, filepath.Join(path, entry.Name())), EntryType: entryType})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	}
	if startIndex > len(entries) {
		return ListResponse{}, fmt.Errorf("cursor '%d' exceeds result count", startIndex)
	}
	end := min(len(entries), startIndex+maxResults)
	var next *string
	if end < len(entries) {
		value := strconv.Itoa(end)
		next = &value
	}
	return ListResponse{Path: request.Path, Entries: entries[startIndex:end], NextCursor: next, Truncated: next != nil}, nil
}

// Read routes to the primary root or, for a "global"-prefixed path, to the
// global mount.
func (b Backend) Read(request ReadRequest) (ReadResponse, error) {
	pathValue := request.Path
	target, routed, prefix, mounted := b.route(&pathValue)
	if routed != nil {
		request.Path = *routed
	}
	response, err := target.read(request)
	if err != nil {
		return ReadResponse{}, err
	}
	if mounted {
		response.Path = joinDisplay(prefix, response.Path)
	}
	return response, nil
}

func (b Backend) read(request ReadRequest) (ReadResponse, error) {
	if request.LineOffset <= 0 {
		return ReadResponse{}, fmt.Errorf("line_offset must be a 1-indexed line number")
	}
	if request.MaxLines != nil && *request.MaxLines <= 0 {
		return ReadResponse{}, fmt.Errorf("max_lines must be a positive integer")
	}
	if request.MaxTokens < 0 {
		return ReadResponse{}, fmt.Errorf("max_tokens must be a non-negative integer")
	}
	pathValue := request.Path
	path, err := b.resolve(&pathValue)
	if err != nil {
		return ReadResponse{}, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ReadResponse{}, notFoundError(request.Path)
	}
	if err != nil {
		return ReadResponse{}, fmt.Errorf("I/O error while reading memories: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ReadResponse{}, fmt.Errorf("path '%s' must not be a symlink", request.Path)
	}
	if !info.Mode().IsRegular() {
		return ReadResponse{}, fmt.Errorf("path '%s' is not a file", request.Path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ReadResponse{}, fmt.Errorf("I/O error while reading memories: %w", err)
	}
	if !utf8.Valid(data) {
		return ReadResponse{}, fmt.Errorf("I/O error while reading memories: file is not valid UTF-8")
	}
	content := string(data)
	start, ok := lineStart(content, request.LineOffset)
	if !ok {
		return ReadResponse{}, fmt.Errorf("line_offset exceeds file length")
	}
	end := lineEnd(content, start, request.MaxLines)
	selected := content[start:end]
	maxTokens := request.MaxTokens
	if maxTokens == 0 {
		maxTokens = DefaultReadMaxTokens
	}
	truncatedText := MiddleTokens(selected, maxTokens)
	return ReadResponse{Path: request.Path, StartLineNumber: request.LineOffset, Content: truncatedText, Truncated: end < len(content) || truncatedText != selected}, nil
}

// Search routes to the primary root or, for a "global"-prefixed path, to the
// global mount. An omitted path searches only the primary root: reaching the
// global scope always requires the explicit "global" (or "global/...") path,
// so a broad, unscoped search never silently pulls another scope's content
// into the results.
func (b Backend) Search(request SearchRequest) (SearchResponse, error) {
	original := request.Path
	target, routed, prefix, mounted := b.route(request.Path)
	request.Path = routed
	response, err := target.search(request)
	if err != nil {
		return SearchResponse{}, err
	}
	if mounted {
		for i := range response.Matches {
			response.Matches[i].Path = joinDisplay(prefix, response.Matches[i].Path)
		}
	}
	response.Path = original
	return response, nil
}

func (b Backend) search(request SearchRequest) (SearchResponse, error) {
	// Recall is the index's job now that nothing matches by substring, so a
	// backend without one cannot answer at all. Saying so is the only honest
	// reply: returning no matches would read as "your memory holds nothing
	// about this", which is a different and much more damaging claim.
	if b.index == nil {
		return SearchResponse{}, fmt.Errorf("memory search index unavailable")
	}
	if request.TopK < 0 {
		return SearchResponse{}, fmt.Errorf("top_k must be a non-negative integer")
	}
	queries := make([]string, len(request.Queries))
	for index, query := range request.Queries {
		queries[index] = strings.TrimSpace(query)
	}
	if len(queries) == 0 || anyEmpty(queries) {
		return SearchResponse{}, fmt.Errorf("queries must not be empty or contain empty strings")
	}
	for _, query := range queries {
		terms, err := searchTerms(query)
		if err != nil {
			return SearchResponse{}, err
		}
		if len(terms) == 0 {
			return SearchResponse{}, fmt.Errorf("query '%s' carries no searchable term", query)
		}
	}
	start, err := b.resolve(request.Path)
	if err != nil {
		return SearchResponse{}, err
	}
	info, err := os.Lstat(start)
	if errors.Is(err, fs.ErrNotExist) {
		return SearchResponse{}, notFoundError(stringValue(request.Path))
	}
	if err != nil {
		return SearchResponse{}, fmt.Errorf("I/O error while reading memories: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return SearchResponse{}, fmt.Errorf("path '%s' must not be a symlink", displayPath(b.Root, start))
	}
	// The walk always covers the whole root, even when the call narrowed the
	// search to one file: relevance is measured against how rare a term is in
	// the store, so an index holding only the narrowed subtree would score
	// every term as common and rank nothing. The narrowing is applied below, to
	// which files may produce matches.
	//
	// It also reads every file exactly once, and those same bytes are what the
	// index is refreshed from, so the index can never describe a version of a
	// file this search is not looking at.
	scope := displayPath(b.Root, start)
	var files []indexedFile
	err = filepath.WalkDir(b.Root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if path != b.Root && strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			if errors.Is(readErr, fs.ErrNotExist) {
				return nil
			}
			return readErr
		}
		if !utf8.Valid(data) {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			if errors.Is(infoErr, fs.ErrNotExist) {
				return nil
			}
			return infoErr
		}
		files = append(files, indexedFile{path: displayPath(b.Root, path), content: string(data), size: info.Size(), modTime: info.ModTime()})
		return nil
	})
	if err != nil {
		return SearchResponse{}, fmt.Errorf("I/O error while reading memories: %w", err)
	}
	signals, err := b.indexSignals(files, queries)
	if err != nil {
		return SearchResponse{}, err
	}
	var matches []SearchMatch
	for _, file := range files {
		if !withinScope(file.path, scope) {
			continue
		}
		for _, match := range searchFile(file.path, file.content, queries, signals[file.path]) {
			if match.score > MinIndexRelevance {
				continue
			}
			matches = append(matches, match)
		}
	}
	rankMatches(matches)
	// Everything past the top K is dropped, not paged: the results are ranked
	// and already filtered by relevance, so what follows is weaker than what a
	// caller has, and offering to fetch it would only invite spending context
	// on it.
	topK := min(max(request.TopK, 0), MaxSearchResults)
	return SearchResponse{Queries: queries, Path: request.Path,
		Matches: matches[:min(len(matches), topK)]}, nil
}

// AdHocNotesDir is the memories-root-relative directory every ad-hoc note is
// written to. Callers need it to hand the model back a path it can feed
// straight into memories_read: a bare filename does not resolve from the root.
const AdHocNotesDir = "extensions/ad_hoc/notes"

// AdHocNotePath is the memories-root-relative path an ad-hoc note with this
// filename is stored at. It owns the join so producers, readers, and telemetry
// cannot drift apart.
func AdHocNotePath(filename string) string { return AdHocNotesDir + "/" + filename }

// AddAdHocNoteScope names which root memories_add_ad_hoc_note writes to.
type AddAdHocNoteScope string

const (
	// AdHocNoteScopeProject writes to the primary (project) root. It is the
	// default: most notes a session captures ("this repo's tests need -tags
	// integration") are project-specific, and defaulting to global would leak
	// them into every other project's memory.
	AdHocNoteScopeProject AddAdHocNoteScope = "project"
	// AdHocNoteScopeGlobal writes to the global mount, for the minority of
	// notes that state a cross-project user preference.
	AdHocNoteScopeGlobal AddAdHocNoteScope = "global"
)

// AddAdHocNote writes a note to the requested scope and returns the
// memories-root-relative path of the created note. A global-scope note comes
// back prefixed with "global/" so it reads straight back through the same
// Backend, exactly like any other mounted path.
func (b Backend) AddAdHocNote(scope AddAdHocNoteScope, filename, note string) (string, error) {
	target := b
	prefix := ""
	switch scope {
	case "", AdHocNoteScopeProject:
		if root := strings.TrimSpace(b.Root); root == "" || root == "." {
			return "", fmt.Errorf("this session has no project memory scope; use scope 'global'")
		}
	case AdHocNoteScopeGlobal:
		matched := false
		for _, m := range b.mounts {
			if m.prefix == "global" {
				target = New(m.root)
				prefix = m.prefix
				matched = true
				break
			}
		}
		if !matched {
			return "", fmt.Errorf("global memory scope is not configured for this session")
		}
	default:
		return "", fmt.Errorf("unknown memory scope '%s'", scope)
	}
	path, err := target.addAdHocNote(filename, note)
	if err != nil {
		return "", err
	}
	return joinDisplay(prefix, path), nil
}

func (b Backend) addAdHocNote(filename, note string) (string, error) {
	if err := validateNoteFilename(filename); err != nil {
		return "", err
	}
	if strings.TrimSpace(note) == "" {
		return "", fmt.Errorf("ad-hoc note must not be empty")
	}
	// b.Root itself (and everything above it) is never model-influenced — it is
	// built entirely from the agent's workspace and the session's scope, and a
	// consolidation agent's file tools are confined inside it, so it cannot
	// reach up to replace this directory or an ancestor with a symlink. Only
	// the path below it (extensions/ad_hoc/notes, walked component-by-component
	// below with symlink checks at each step) is agent-writable and needs that
	// defense. MkdirAll here just creates however many levels a fresh scope
	// root needs; ensureDirectory below still verifies b.Root itself is not a
	// symlink before anything is written under it.
	if err := os.MkdirAll(b.Root, 0o755); err != nil {
		return "", fmt.Errorf("I/O error while reading memories: %w", err)
	}
	dir := b.Root
	for _, component := range strings.Split(AdHocNotesDir, "/") {
		if err := ensureDirectory(dir); err != nil {
			return "", err
		}
		dir = filepath.Join(dir, component)
	}
	if err := ensureDirectory(dir); err != nil {
		return "", err
	}
	file, err := os.OpenFile(filepath.Join(dir, filename), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("ad-hoc note '%s' already exists", filename)
	}
	if err != nil {
		return "", fmt.Errorf("I/O error while reading memories: %w", err)
	}
	// The returned path is a promise that the note is readable, so the write
	// must be fully flushed before it is made: a deferred Close would drop the
	// error that a full or networked filesystem only reports there. Remove the
	// stub on failure so a retry with the same filename is not rejected as a
	// duplicate.
	name := file.Name()
	writeErr := writeAndClose(file, note)
	if writeErr != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("I/O error while reading memories: %w", writeErr)
	}
	return AdHocNotePath(filename), nil
}

func writeAndClose(file *os.File, note string) error {
	if _, err := file.WriteString(note); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func ensureDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(path, 0o755); err != nil {
			return fmt.Errorf("I/O error while reading memories: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("I/O error while reading memories: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("path '%s' must not be a symlink", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("path '%s' must be a directory", path)
	}
	return nil
}

func validateNoteFilename(filename string) error {
	invalid := func(reason string) error { return fmt.Errorf("filename '%s' %s", filename, reason) }
	if len(filename) > 128 {
		return invalid("must be at most 128 bytes")
	}
	if !strings.HasSuffix(filename, ".md") {
		return invalid("must end with .md")
	}
	stem := strings.TrimSuffix(filename, ".md")
	if len(stem) <= 20 || !validTimestampPrefix(stem) {
		return invalid("must use YYYY-MM-DDTHH-MM-SS-<slug>.md")
	}
	slug := stem[20:]
	if len(slug) < 1 || len(slug) > 80 {
		return invalid("slug must be 1 to 80 bytes")
	}
	for _, value := range []byte(slug) {
		if !(value >= 'a' && value <= 'z') && !(value >= '0' && value <= '9') && value != '-' {
			return invalid("slug must contain only lowercase ASCII letters, digits, or hyphens")
		}
	}
	return nil
}

func validTimestampPrefix(stem string) bool {
	if len(stem) <= 20 {
		return false
	}
	for _, index := range []int{4, 7, 13, 16, 19} {
		if stem[index] != '-' {
			return false
		}
	}
	if stem[10] != 'T' {
		return false
	}
	for index, value := range []byte(stem[:19]) {
		if index == 4 || index == 7 || index == 10 || index == 13 || index == 16 {
			continue
		}
		if value < '0' || value > '9' {
			return false
		}
	}
	return true
}

// searchFile finds the matches in one file.
//
// A line answers a query when the two share a segmented term, which is what
// lets a query recall a memory that words the same idea differently — the case
// that matters most in Chinese, where there are no spaces to make two phrasings
// look alike.
//
// One matched line is one match. There is no conjunction to satisfy and no
// multi-line window to assemble: relevance already prefers the line that
// answers more of the query, which is the same preference a conjunction would
// have enforced by deletion instead.
//
// Every match records the terms that actually occur in it, which is what a
// surface highlights, and the relevance the index gave the line, which is what
// orders the results and decides what is too weak to return.
func searchFile(path, content string, queries []string, signals fileSignals) []SearchMatch {
	lines := textLines(content)
	var matches []SearchMatch
	for index := range lines {
		signal := signals[index+1]
		if signal == nil {
			continue
		}
		var matched []string
		var terms []string
		for position, query := range queries {
			shared := signal.terms[position]
			if len(shared) == 0 {
				continue
			}
			matched = append(matched, query)
			terms = append(terms, shared...)
		}
		if len(matched) == 0 {
			continue
		}
		contentStart := max(0, index-searchContextLines)
		contentEnd := min(len(lines), index+searchContextLines+1)
		matches = append(matches, SearchMatch{Path: path, MatchLineNumber: index + 1,
			ContentStartLineNumber: contentStart + 1,
			Content:                strings.Join(lines[contentStart:contentEnd], "\n"),
			MatchedQueries:         matched,
			MatchedTerms:           dedupeStrings(terms),
			score:                  signal.score})
	}
	return matches
}

// searchContextLines is how many lines of the file come back on each side of a
// match.
//
// A matched line on its own is rarely evidence of anything. Memory files are
// written as records — a key under the heading that names the task, a path
// under the line that says what was run there — so the line that shares a word
// with the query is usually the label, and what the reader actually needs is
// the two or three lines it labels. A search that returned the line alone
// would not be a cheaper answer; it would be one that cannot be read, and the
// model would spend a memories_read call to recover what the search already had
// in hand.
//
// The window is the search's, not the caller's, so no argument makes a result
// either unreadable or a page: past a few lines either way the window stops
// being what the matched line means and starts being the rest of the document,
// which memories_read fetches in one call for the one memory that turned out to
// matter. Every returned line is context the turn pays for and keeps paying
// for, multiplied by the number of matches, so a wider search window would let
// a single speculative search crowd out the conversation it was meant to
// inform.
const searchContextLines = 3

// MinIndexRelevance is the weakest bm25 score a match recalled only by the
// index may have. Anything above it — that is, less relevant — is dropped.
//
// The index recalls every line sharing a term with the query, and for a query
// with any common word that is most of the store. Ranking alone does not fix
// it: with nothing better to offer, a page of the least bad noise still comes
// back looking like recalled memory, and the model pays for it and may believe
// it.
//
// The value is measured, not chosen. Against this project's own memory store,
// every match scoring at or below it was on the query's subject, while the band
// just above it held the unrelated lines a shared word had dragged in
// ("applies_to: cwd=…", a test-baseline list, a sentence naming the project).
// It also holds steady as the store grows: over a corpus enlarged twentyfold
// the weakest scores moved by less than 0.1, because a line pulled in by a
// common word has a low IDF however many documents there are — only genuinely
// rare terms score further down, which is the signal, not the noise.
const MinIndexRelevance = -2.5

// rankMatches orders results by relevance: bm25 decides, and path and line
// break ties so that two runs over an unchanged store return the same page.
func rankMatches(matches []SearchMatch) {
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score < matches[j].score
		}
		if matches[i].Path != matches[j].Path {
			return matches[i].Path < matches[j].Path
		}
		return matches[i].MatchLineNumber < matches[j].MatchLineNumber
	})
}

func displayPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func anyEmpty(values []string) bool {
	for _, value := range values {
		if value == "" {
			return true
		}
	}
	return false
}

func dedupeStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	seen := make(map[string]struct{}, len(values))
	out := values[:0]
	for _, value := range values {
		if _, repeat := seen[value]; repeat {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// indexSignals refreshes the index from the files just read and asks it which
// lines share a term with the queries. Recall is the index's alone, so an index
// that cannot be read or written fails the search: answering with no matches
// would claim the memory holds nothing about the question.
func (b Backend) indexSignals(files []indexedFile, queries []string) (map[string]fileSignals, error) {
	ctx := context.Background()
	if err := b.index.sync(ctx, b.Root, files); err != nil {
		return nil, fmt.Errorf("memory search index sync failed: %w", err)
	}
	signals, err := b.index.signals(ctx, b.Root, queries)
	if err != nil {
		return nil, fmt.Errorf("memory search index query failed: %w", err)
	}
	return signals, nil
}

func textLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	endedWithNewline := strings.HasSuffix(content, "\n")
	if endedWithNewline {
		lines = lines[:len(lines)-1]
	}
	terminatedLines := len(lines)
	if !endedWithNewline {
		terminatedLines--
	}
	for index := 0; index < terminatedLines; index++ {
		lines[index] = strings.TrimSuffix(lines[index], "\r")
	}
	return lines
}

// MatchRanges reports where terms occur in text, as ordered, non-overlapping
// [start, end) byte ranges into the original text.
//
// It exists so that a surface showing a search result can mark the text that
// recalled it. Case is folded because index terms are stored folded, so the
// term handed back for a match is rarely spelled the way the line spells it;
// the mapping back to the original offsets is done here, beside the index that
// produced the terms, rather than re-derived by each caller.
//
// A term marks the word it is and never a part of a longer one. The search
// compares whole terms, so marking the "read" inside "read_file" would tell the
// reader a line came back for a word the line does not contain. The boundary is
// the one the terms were cut at, connectors included — "read" is no more a word
// of "read-only" than of "read_file" — and it is only enforced at an end the
// term itself writes with a word character, which leaves a Chinese term free to
// mark inside the run of characters it was segmented out of.
func MatchRanges(text string, terms []string) [][2]int {
	if text == "" || len(terms) == 0 {
		return nil
	}
	prepared, starts, ends := foldedTextIndexed(text)
	if prepared == "" {
		return nil
	}
	var ranges [][2]int
	for _, term := range terms {
		needle := strings.ToLower(strings.TrimSpace(term))
		if needle == "" {
			continue
		}
		for offset := 0; offset < len(prepared); {
			index := strings.Index(prepared[offset:], needle)
			if index < 0 {
				break
			}
			begin := offset + index
			end := begin + len(needle)
			if !wholeTerm(prepared, begin, end) {
				// Not this occurrence, but a later one may still stand on its
				// own, and it can begin before this one ends.
				offset = begin + 1
				continue
			}
			ranges = append(ranges, [2]int{starts[begin], ends[end-1]})
			offset = end
		}
	}
	return mergeRanges(ranges)
}

// wholeTerm reports whether the match at [begin, end) is the whole word and not
// a piece of a longer one. Only an end the term writes with a word character
// can be continued by one, so a term that begins or ends in punctuation — or in
// a script that writes no boundaries between its words — is unconstrained
// there. Both ends are judged in place, by the rule the terms were cut by: the
// "8" of "8.4.5" is not a word of its own, while the "8.4.5" that ends a
// sentence is.
func wholeTerm(text string, begin, end int) bool {
	if wordRuneAt(text, begin) {
		if _, size := utf8.DecodeLastRuneInString(text[:begin]); size > 0 && wordRuneAt(text, begin-size) {
			return false
		}
	}
	if _, size := utf8.DecodeLastRuneInString(text[begin:end]); wordRuneAt(text, end-size) {
		if end < len(text) && wordRuneAt(text, end) {
			return false
		}
	}
	return true
}

// foldedTextIndexed lowercases value and, for every byte of the result, records
// the byte range of the original rune it came from, so a match found in the
// folded text can be reported as a range of the text the reader sees.
func foldedTextIndexed(value string) (string, []int, []int) {
	var prepared strings.Builder
	prepared.Grow(len(value))
	starts := make([]int, 0, len(value))
	ends := make([]int, 0, len(value))
	for index, char := range value {
		before := prepared.Len()
		prepared.WriteRune(unicode.ToLower(char))
		for written := prepared.Len() - before; written > 0; written-- {
			starts = append(starts, index)
			ends = append(ends, index+utf8.RuneLen(char))
		}
	}
	return prepared.String(), starts, ends
}

func mergeRanges(ranges [][2]int) [][2]int {
	if len(ranges) < 2 {
		return ranges
	}
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i][0] != ranges[j][0] {
			return ranges[i][0] < ranges[j][0]
		}
		return ranges[i][1] < ranges[j][1]
	})
	merged := ranges[:1]
	for _, current := range ranges[1:] {
		last := &merged[len(merged)-1]
		if current[0] <= last[1] {
			if current[1] > last[1] {
				last[1] = current[1]
			}
			continue
		}
		merged = append(merged, current)
	}
	return merged
}

// parseCursor turns a caller-supplied cursor into a start index. A missing or
// blank cursor both mean "start at the first page": models sometimes send an
// empty string rather than omitting the field, and failing that call teaches
// them nothing the next paginated call can use.
func parseCursor(cursor *string) (int, error) {
	if cursor == nil || strings.TrimSpace(*cursor) == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(strings.TrimSpace(*cursor))
	if err != nil || value < 0 {
		return 0, fmt.Errorf("cursor '%s' must be a non-negative integer", *cursor)
	}
	return value, nil
}

func lineStart(content string, line int) (int, bool) {
	if line == 1 {
		return 0, true
	}
	current := 1
	for index, char := range content {
		if char == '\n' {
			current++
			if current == line {
				return index + 1, true
			}
		}
	}
	return 0, false
}

func lineEnd(content string, start int, maxLines *int) int {
	if maxLines == nil {
		return len(content)
	}
	seen := 1
	for index, char := range content[start:] {
		if char == '\n' {
			if seen == *maxLines {
				return start + index + 1
			}
			seen++
		}
	}
	return len(content)
}
