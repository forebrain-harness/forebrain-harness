package lsp

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
)

// Problem is one diagnostic as the runtime keeps it.
type Problem struct {
	ServerID  string
	Path      string // absolute
	Severity  int    // 1..4; 0 from the server is stored as 1
	Line      int    // LSP 0-based
	Character int    // LSP 0-based, in the server's encoding
	Source    string
	Code      string
	Message   string
}

// diagFile is the latest publish for one (server, file).
type diagFile struct {
	problems []Problem
	version  *int32
	seq      uint64 // the store sequence this state arrived in
}

// DiagStore keeps the latest problems per (server, file) and lets callers
// wait for them.
type DiagStore struct {
	mu        sync.Mutex
	files     map[string]map[string]*diagFile // serverID → clean absolute path → latest
	seq       uint64
	last      map[string]time.Time // serverID → last publish, for the quiet period
	wake      chan struct{}        // closed and replaced on every publish
	subs      map[int]func(serverID, absPath string)
	nextSubID int
}

// NewDiagStore returns an empty store.
func NewDiagStore() *DiagStore {
	return &DiagStore{
		files: map[string]map[string]*diagFile{},
		last:  map[string]time.Time{},
		wake:  make(chan struct{}),
		subs:  map[int]func(serverID, absPath string){},
	}
}

// Publish records a publishDiagnostics (or a pull result) and wakes waiters.
func (s *DiagStore) Publish(serverID, absPath string, version *int32, diags []Diagnostic) {
	path := filepath.Clean(absPath)
	problems := make([]Problem, 0, len(diags))
	for _, d := range diags {
		severity := d.Severity
		if severity == 0 {
			severity = 1
		}
		problems = append(problems, Problem{
			ServerID:  serverID,
			Path:      path,
			Severity:  severity,
			Line:      int(d.Range.Start.Line),
			Character: int(d.Range.Start.Character),
			Source:    d.Source,
			Code:      DiagnosticCode(d.Code),
			Message:   d.Message,
		})
	}
	var v *int32
	if version != nil {
		value := *version
		v = &value
	}
	s.mu.Lock()
	byServer := s.files[serverID]
	if byServer == nil {
		byServer = map[string]*diagFile{}
		s.files[serverID] = byServer
	}
	s.seq++
	byServer[path] = &diagFile{problems: problems, version: v, seq: s.seq}
	s.last[serverID] = time.Now()
	close(s.wake)
	s.wake = make(chan struct{})
	subscribers := make([]func(string, string), 0, len(s.subs))
	for _, fn := range s.subs {
		subscribers = append(subscribers, fn)
	}
	s.mu.Unlock()
	for _, fn := range subscribers {
		fn(serverID, path)
	}
}

// Get returns the latest problems for one file.
func (s *DiagStore) Get(serverID, absPath string) (problems []Problem, version *int32, seq uint64, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.files[serverID][filepath.Clean(absPath)]
	if f == nil {
		return nil, nil, 0, false
	}
	problems = append([]Problem(nil), f.problems...)
	if f.version != nil {
		value := *f.version
		version = &value
	}
	return problems, version, f.seq, true
}

// Seq is the number of publishes so far; callers remember it to ask
// "since when".
func (s *DiagStore) Seq() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq
}

// WaitFor blocks until (serverID, absPath) is published after afterSeq with
// a version >= minVersion (any version when the publish carried none), or
// ctx ends.
func (s *DiagStore) WaitFor(ctx context.Context, serverID, absPath string, minVersion int32, afterSeq uint64) bool {
	path := filepath.Clean(absPath)
	for {
		s.mu.Lock()
		f := s.files[serverID][path]
		wake := s.wake
		s.mu.Unlock()
		if f != nil && f.seq > afterSeq && (f.version == nil || *f.version >= minVersion) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-wake:
		}
	}
}

// WaitQuiet blocks until a publish for serverID has arrived and none
// followed for quiet, or until deadline/ctx. Before the first publish the
// server is not quiet yet: the caller's deadline is what bounds that wait.
func (s *DiagStore) WaitQuiet(ctx context.Context, serverID string, quiet time.Duration, deadline time.Time) {
	for {
		s.mu.Lock()
		last, published := s.last[serverID]
		wake := s.wake
		s.mu.Unlock()
		now := time.Now()
		var timer *time.Timer
		var timerC <-chan time.Time // nil before the first publish with no deadline: wait for a wake
		if published {
			if now.Sub(last) >= quiet {
				return
			}
			wait := quiet - now.Sub(last)
			if !deadline.IsZero() {
				if !deadline.After(now) {
					return
				}
				if until := deadline.Sub(now); until < wait {
					wait = until
				}
			}
			timer = time.NewTimer(wait)
			timerC = timer.C
		} else if !deadline.IsZero() {
			if !deadline.After(now) {
				return
			}
			timer = time.NewTimer(deadline.Sub(now))
			timerC = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-wake:
			if timer != nil {
				timer.Stop()
			}
			continue
		case <-timerC:
			return
		}
	}
}

// PublishedSince lists files of serverID published after afterSeq.
func (s *DiagStore) PublishedSince(serverID string, afterSeq uint64) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.files[serverID]))
	for path, f := range s.files[serverID] {
		if f.seq > afterSeq {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

// Subscribe calls fn after every publish (outside the store lock); cancel
// stops it.
func (s *DiagStore) Subscribe(fn func(serverID, absPath string)) (cancel func()) {
	s.mu.Lock()
	id := s.nextSubID
	s.nextSubID++
	s.subs[id] = fn
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.subs, id)
		s.mu.Unlock()
	}
}

// Counts sums errors and warnings across a server's files.
func (s *DiagStore) Counts(serverID string) (errors, warnings int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.files[serverID] {
		for _, p := range f.problems {
			switch p.Severity {
			case 1:
				errors++
			case 2:
				warnings++
			}
		}
	}
	return errors, warnings
}

// Forget drops a file's problems (document closed or deleted).
func (s *DiagStore) Forget(serverID, absPath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files[serverID], filepath.Clean(absPath))
}

// Snapshot copies every file's current problems for serverID; DidWrite
// takes one before an edit as the baseline for files it did not write.
func (s *DiagStore) Snapshot(serverID string) map[string][]Problem {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]Problem, len(s.files[serverID]))
	for path, f := range s.files[serverID] {
		out[path] = append([]Problem(nil), f.problems...)
	}
	return out
}

// PullDiagnostics asks the server for one document's diagnostics
// (textDocument/diagnostic) and publishes the full report and any related
// documents into the store. It reports false when the server lacks the
// capability.
func PullDiagnostics(ctx context.Context, inst *Instance, store *DiagStore, serverID, absPath string) (bool, error) {
	if !inst.Capabilities().Supports("diagnosticProvider") {
		return false, nil
	}
	path := filepath.Clean(absPath)
	var report DocumentDiagnosticReport
	params := map[string]any{"textDocument": map[string]any{"uri": PathToURI(path)}}
	if err := inst.Call(ctx, "textDocument/diagnostic", params, &report); err != nil {
		return true, err
	}
	if report.Kind == "full" {
		store.Publish(serverID, path, nil, report.Items)
	}
	// Related documents come along: a full report replaces what the store
	// has for that file, an unchanged one leaves it alone.
	for uri, related := range report.RelatedDocuments {
		if related.Kind != "full" {
			continue
		}
		relatedPath, err := URIToPath(uri)
		if err != nil {
			continue // not a local file: nothing to key the store by
		}
		store.Publish(serverID, relatedPath, nil, related.Items)
	}
	return true, nil
}

// ProblemKey is the identity used by NewProblems (spec §8.3.4): severity,
// source, code, and the message with whitespace normalized. Line numbers are
// deliberately absent, so a shift does not turn old problems into new ones.
func ProblemKey(p Problem) string {
	return fmt.Sprintf("%d\x1f%s\x1f%s\x1f%s", p.Severity, p.Source, p.Code, normalizeMessage(p.Message))
}

// normalizeMessage trims the message and collapses whitespace runs to one
// space.
func normalizeMessage(message string) string {
	return strings.Join(strings.Fields(message), " ")
}

// NewProblems returns the problems in after that are not in before, as a
// multiset: for each key, max(0, len(after)-len(before)) entries, taken
// from the back of after's list (spec §8.3.4). Matching consumes a key's
// count from the front of after, so the entries that survive — the new
// ones — are the later copies.
func NewProblems(before, after []Problem) []Problem {
	counts := make(map[string]int, len(before))
	for _, p := range before {
		counts[ProblemKey(p)]++
	}
	var kept []Problem
	for _, p := range after {
		key := ProblemKey(p)
		if counts[key] > 0 {
			counts[key]--
			continue
		}
		kept = append(kept, p)
	}
	return kept
}

// SeverityThreshold maps "error"/"warning"/"information"/"hint" to 1..4 (""
// means warning).
func SeverityThreshold(name string) int {
	switch name {
	case "error":
		return 1
	case "information":
		return 3
	case "hint":
		return 4
	default: // "warning" and anything else
		return 2
	}
}

// ReportOptions shapes model-facing diagnostic text.
type ReportOptions struct {
	ProjectRoot string
	MinSeverity int // from SeverityThreshold
	MaxPerFile  int
	MaxFiles    int
	FirstPaths  []string // files this edit wrote, listed first in this order
	// Position maps a problem to a 1-based line and column (the caller uses
	// FromLSPPosition with the file content and the server's encoding).
	Position func(p Problem) (line, column int)
}

// lineColumn maps one problem to its displayed coordinates; without a
// Position callback the raw LSP numbers become 1-based (spec §8.2's
// unreadable-file fallback).
func (o ReportOptions) lineColumn(p Problem) (line, column int) {
	if o.Position != nil {
		return o.Position(p)
	}
	return p.Line + 1, p.Character + 1
}

// LateChange is one file's late news: New problems, or Cleared when none
// remain.
type LateChange struct {
	Path    string
	New     []Problem
	Cleared bool
}

// relSlash maps an absolute path to its project-root-relative, slash-separated
// form. ok is false outside the root (or when the two cannot be related).
func relSlash(root, path string) (rel string, ok bool) {
	r, err := filepath.Rel(root, path)
	if err != nil {
		return "", false
	}
	if r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || r == "." {
		return "", false
	}
	return filepath.ToSlash(r), true
}

// fileReport is one file's slice of a rendered report.
type fileReport struct {
	rel   string
	shown []Problem
	more  int // problems this file has beyond MaxPerFile
}

// layoutProblems is the filter and order FormatEditDiagnostics and
// FormatLateDiagnostics share (spec §8.3.4): drop below MinSeverity and
// outside the project root, files this edit wrote first in FirstPaths order
// then the rest by relative path, within a file by severity, line, column,
// MaxPerFile per file and MaxFiles files in total. total is the number of
// problems that survived the severity and root filters, before the caps.
func layoutProblems(problems []Problem, opts ReportOptions) (files []fileReport, hiddenFiles, total int) {
	grouped := map[string][]Problem{}
	rels := map[string]string{}
	for _, p := range problems {
		if p.Severity > opts.MinSeverity {
			continue
		}
		rel, ok := relSlash(opts.ProjectRoot, p.Path)
		if !ok {
			continue
		}
		total++
		grouped[p.Path] = append(grouped[p.Path], p)
		rels[p.Path] = rel
	}
	paths := make([]string, 0, len(grouped))
	for path := range grouped {
		paths = append(paths, path)
	}
	firstIndex := map[string]int{}
	for i, p := range opts.FirstPaths {
		firstIndex[p] = i
	}
	inFirst := func(path string) (int, bool) {
		for first, i := range firstIndex {
			if SamePath(first, path) {
				return i, true
			}
		}
		return 0, false
	}
	sort.Slice(paths, func(a, b int) bool {
		pa, pb := paths[a], paths[b]
		ia, aFirst := inFirst(pa)
		ib, bFirst := inFirst(pb)
		if aFirst != bFirst {
			return aFirst
		}
		if aFirst && ia != ib {
			return ia < ib
		}
		return rels[pa] < rels[pb]
	})
	for _, path := range paths {
		group := grouped[path]
		// Coordinates come from the Position callback, which may read the
		// file; computing them once per problem up front keeps a sort from
		// doing it O(n log n) times.
		type coord struct {
			p         Problem
			line, col int
		}
		coords := make([]coord, len(group))
		for i, p := range group {
			coords[i].p = p
			coords[i].line, coords[i].col = opts.lineColumn(p)
		}
		sort.Slice(coords, func(a, b int) bool {
			if coords[a].p.Severity != coords[b].p.Severity {
				return coords[a].p.Severity < coords[b].p.Severity
			}
			if coords[a].line != coords[b].line {
				return coords[a].line < coords[b].line
			}
			return coords[a].col < coords[b].col
		})
		for i := range coords {
			group[i] = coords[i].p
		}
		shown := group
		more := 0
		if opts.MaxPerFile > 0 && len(group) > opts.MaxPerFile {
			shown = group[:opts.MaxPerFile]
			more = len(group) - opts.MaxPerFile
		}
		if opts.MaxFiles > 0 && len(files) >= opts.MaxFiles {
			hiddenFiles++
			continue
		}
		files = append(files, fileReport{rel: rels[path], shown: shown, more: more})
	}
	return files, hiddenFiles, total
}

// problemLine renders one problem the way appendix B.3 and B.4 print it.
func problemLine(p Problem, opts ReportOptions) string {
	line, column := opts.lineColumn(p)
	text := fmt.Sprintf("  %s %d:%d %s", SeverityName(p.Severity), line, column,
		strings.ReplaceAll(strings.ReplaceAll(p.Message, "\r\n", " / "), "\n", " / "))
	switch {
	case p.Source != "" && p.Code != "":
		text += fmt.Sprintf(" [%s %s]", p.Source, p.Code)
	case p.Source != "":
		text += fmt.Sprintf(" [%s]", p.Source)
	case p.Code != "":
		text += fmt.Sprintf(" [%s]", p.Code)
	}
	return text
}

// FormatEditDiagnostics renders appendix B.3; text is "" when there is
// nothing to say.
func FormatEditDiagnostics(problems []Problem, pending []string, servers []string, baselineUnavailable bool, opts ReportOptions) (text string, summary event.LSPDiagnosticsSummary) {
	files, hiddenFiles, total := layoutProblems(problems, opts)
	var pendingRels []string
	for _, path := range pending {
		if rel, ok := relSlash(opts.ProjectRoot, filepath.Clean(path)); ok {
			pendingRels = append(pendingRels, rel)
		} else {
			pendingRels = append(pendingRels, filepath.ToSlash(filepath.Clean(path)))
		}
	}
	if total == 0 && len(pendingRels) == 0 {
		return "", summary
	}
	plural := "s"
	if total == 1 {
		plural = ""
	}
	serverList := strings.Join(servers, ", ")

	var b strings.Builder
	b.WriteString("<diagnostics>\n")
	if total > 0 {
		if baselineUnavailable {
			fmt.Fprintf(&b, "%d problem%s reported after this edit (%s); earlier problems could not be told apart\n", total, plural, serverList)
		} else {
			fmt.Fprintf(&b, "%d new problem%s after this edit (%s)\n", total, plural, serverList)
		}
	}
	for _, f := range files {
		b.WriteString(f.rel + "\n")
		for _, p := range f.shown {
			b.WriteString(problemLine(p, opts) + "\n")
		}
		if f.more > 0 {
			fmt.Fprintf(&b, "  … %d more in this file not shown\n", f.more)
		}
	}
	if hiddenFiles > 0 {
		fmt.Fprintf(&b, "… %d more files not shown\n", hiddenFiles)
	}
	for _, rel := range pendingRels {
		fmt.Fprintf(&b, "diagnostics for %s are still being computed and will follow\n", rel)
	}
	b.WriteString("</diagnostics>")

	summary.New = total
	summary.Files = len(files)
	summary.Servers = servers
	for _, f := range files {
		for _, p := range f.shown {
			line, column := opts.lineColumn(p)
			summary.Items = append(summary.Items, event.LSPDiagnostic{
				Path:     f.rel,
				Line:     line,
				Column:   column,
				Severity: SeverityName(p.Severity),
				Source:   p.Source,
				Code:     p.Code,
				Message:  p.Message,
			})
		}
	}
	summary.PendingFiles = pendingRels
	summary.BaselineUnavailable = baselineUnavailable
	return b.String(), summary
}

// FormatLateDiagnostics renders appendix B.4; "" when changes is empty after
// filtering.
func FormatLateDiagnostics(changes []LateChange, opts ReportOptions) string {
	var problems []Problem
	cleared := map[string]bool{} // clean absolute path → no remaining problems
	for _, change := range changes {
		if change.Cleared {
			cleared[filepath.Clean(change.Path)] = true
		}
		problems = append(problems, change.New...)
	}
	files, _, _ := layoutProblems(problems, opts)
	// A file with problems to show keeps its block, a cleared file keeps
	// its marker, and a file whose problems all fell below the threshold
	// without being cleared has nothing to say.
	type entry struct {
		rel    string
		report fileReport
		shown  bool // problems (true) or the cleared marker (false)
	}
	seen := map[string]bool{}
	var entries []entry
	for _, f := range files {
		if len(f.shown) == 0 {
			continue
		}
		entries = append(entries, entry{rel: f.rel, report: f, shown: true})
		seen[f.rel] = true
	}
	var clearedRels []string
	for path := range cleared {
		if rel, ok := relSlash(opts.ProjectRoot, path); ok && !seen[rel] {
			clearedRels = append(clearedRels, rel)
		}
	}
	sort.Strings(clearedRels)
	for _, rel := range clearedRels {
		entries = append(entries, entry{rel: rel})
	}
	if len(entries) == 0 {
		return ""
	}
	sort.Slice(entries, func(a, b int) bool { return entries[a].rel < entries[b].rel })
	if opts.MaxFiles > 0 && len(entries) > opts.MaxFiles {
		entries = entries[:opts.MaxFiles]
	}
	var b strings.Builder
	b.WriteString("Language server diagnostics changed for files you edited earlier in this session:\n")
	for _, e := range entries {
		if !e.shown {
			b.WriteString(e.rel + ": no remaining problems\n")
			continue
		}
		b.WriteString(e.rel + "\n")
		for _, p := range e.report.shown {
			b.WriteString(problemLine(p, opts) + "\n")
		}
		if e.report.more > 0 {
			fmt.Fprintf(&b, "  … %d more in this file not shown\n", e.report.more)
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}
