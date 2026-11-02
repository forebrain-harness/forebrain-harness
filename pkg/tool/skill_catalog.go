// The loaded-skill catalog: which skills this session discovered, which
// directory each one's content is read from, and how a path that names a skill
// is resolved to the file inside it.
package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

// LoadedSkill is the internal catalog record for one discovered skill, keyed by
// skill name. RootDir stays out of anything model-visible so host filesystem
// paths are not advertised in every request.
//
// Version and VersionsRoot are the session's pin: the version the skill was
// at when this runtime loaded it, and where that version's history lives.
// Version 0 means the skill carries no version history — a built-in, a
// cross-tool directory — and its content is read from RootDir as always.
type LoadedSkill struct {
	Name    string
	RootDir string
	// Version is the published version this session reads, 0 when the skill
	// is not versioned.
	Version int
	// VersionsRoot is <skill root>/.versions/<name> for a versioned skill,
	// empty otherwise.
	VersionsRoot string
}

// PinnedDir is the directory this session reads the skill's content from: the
// pinned version's own copy for a versioned skill, the formal directory
// otherwise.
func (item LoadedSkill) PinnedDir() string {
	if item.Version > 0 && strings.TrimSpace(item.VersionsRoot) != "" {
		return filepath.Join(item.VersionsRoot, "v"+strconv.Itoa(item.Version))
	}
	return item.RootDir
}

// PinnedSkillFile is the SKILL.md this session reads for the skill.
func (item LoadedSkill) PinnedSkillFile() string {
	return filepath.Join(item.PinnedDir(), "SKILL.md")
}

// ReplaceLoadedSkillCatalog swaps in the skills discovered for the current
// agent. The catalog exists so skill directories are readable: it is the sole
// source of truth for skill resource access.
func (s *State) ReplaceLoadedSkillCatalog(skills []LoadedSkill) {
	if s == nil {
		return
	}
	next := normalizedLoadedSkills(skills)
	s.mu.Lock()
	s.loadedSkills = next
	s.mu.Unlock()
}

func normalizedLoadedSkills(skills []LoadedSkill) map[string]LoadedSkill {
	next := make(map[string]LoadedSkill, len(skills))
	for _, item := range skills {
		name := strings.TrimSpace(item.Name)
		root := normalizeRootPath(item.RootDir)
		if name == "" || root == "" {
			continue
		}
		item.Name = name
		item.RootDir = root
		if item.Version <= 0 {
			item.Version = 0
			item.VersionsRoot = ""
		}
		next[name] = item
	}
	return next
}

func (s *State) LoadedSkills() []LoadedSkill {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	out := make([]LoadedSkill, 0, len(s.loadedSkills))
	for _, item := range s.loadedSkills {
		out = append(out, item)
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// LoadedSkillRoots lists every directory a loaded skill's content may be read
// from: the skill's formal root, plus the pinned version directory a
// versioned session reads instead. The pinned copy is exactly as much "backed
// by a discovered skill" as the formal directory is.
func (s *State) LoadedSkillRoots() []string {
	items := s.LoadedSkills()
	out := make([]string, 0, 2*len(items))
	seen := make(map[string]struct{}, 2*len(items))
	add := func(root string) {
		root = strings.TrimSpace(root)
		if root == "" {
			return
		}
		if _, ok := seen[root]; ok {
			return
		}
		seen[root] = struct{}{}
		out = append(out, root)
	}
	for _, item := range items {
		add(item.RootDir)
		add(item.PinnedDir())
	}
	sort.Strings(out)
	return out
}

// LoadedSkillForPath returns the most-specific loaded skill containing path.
// A path inside the session's pinned version copy addresses the skill just as
// a path inside its formal directory does.
func (s *State) LoadedSkillForPath(path string) (LoadedSkill, bool) {
	if s == nil {
		return LoadedSkill{}, false
	}
	lexical := strings.TrimSpace(path)
	if abs, err := filepath.Abs(lexical); err == nil {
		lexical = filepath.Clean(abs)
	}
	normalized := normalizeRootPath(path)
	if normalized == "" && lexical == "" {
		return LoadedSkill{}, false
	}
	items := s.LoadedSkills()
	var matched LoadedSkill
	for _, item := range items {
		within := pathWithinRoot(normalized, item.RootDir) || pathWithinRoot(lexical, item.RootDir)
		if !within && item.Version > 0 {
			within = pathWithinRoot(normalized, item.PinnedDir()) || pathWithinRoot(lexical, item.PinnedDir())
		}
		if !within {
			continue
		}
		if matched.RootDir == "" || len(item.RootDir) > len(matched.RootDir) {
			matched = item
		}
	}
	return matched, matched.RootDir != ""
}

func (s *State) PathUnderLoadedSkillRoot(path string) bool {
	_, ok := s.LoadedSkillForPath(path)
	return ok
}

// LoadedSkillMainFile identifies a catalog-backed root SKILL.md. It accepts the
// canonical path and the shortened catalog spelling that read_file repairs, so
// a started event can use the Skill card before the handler finishes resolving
// the file.
func (s *State) LoadedSkillMainFile(path string) (LoadedSkill, string, bool) {
	if s == nil {
		return LoadedSkill{}, "", false
	}
	if item, ok := s.LoadedSkillForPath(path); ok {
		mainFile := normalizeRootPath(filepath.Join(item.RootDir, "SKILL.md"))
		if normalizeRootPath(path) == mainFile {
			return item, mainFile, true
		}
	}
	if resolved, item, ok := s.resolveShortenedSkillPath(path); ok {
		mainFile := normalizeRootPath(filepath.Join(item.RootDir, "SKILL.md"))
		if normalizeRootPath(resolved) == mainFile {
			return item, mainFile, true
		}
	}
	return LoadedSkill{}, "", false
}

// ResolveLoadedSkillRead resolves path only against roots backed by
// the discovered skills.
func (s *State) ResolveLoadedSkillRead(path string) (string, bool) {
	if s == nil {
		return "", false
	}
	abs, err := ResolveWithinRoots(path, s.LoadedSkillRoots())
	if err != nil {
		return "", false
	}
	return abs, true
}

// skillPathMatch is one way a requested path could be naming a file inside a
// loaded skill: the skill whose name appears as a directory in the path, and
// the file the trailing segments name inside that skill's real root.
type skillPathMatch struct {
	Skill LoadedSkill
	File  string
	// Shortened reports that the directory the path puts the skill in really
	// does contain that skill's root — the path is the catalog path with
	// intermediate directories dropped, not an unrelated path that happens to
	// share a skill's name.
	Shortened bool
}

// matchSkillPaths returns every loaded skill that could explain a path that is
// not there. It is the one place that question is answered, for all the answers
// built on it: repairing a read, telling the model where the skill it was
// reaching for actually lives, correcting a write aimed at a path that only
// names a skill, and recognizing a shell command that addresses one. Answering
// it in one place is the point — a path the read side resolves into a skill and
// the write side does not is how a "modify this skill" write became a stray
// directory beside it.
//
// A path that exists is never matched — a real file is never reinterpreted.
func (s *State) matchSkillPaths(path string) []skillPathMatch {
	if s == nil {
		return nil
	}
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil
	}
	requested, err := filepath.Abs(trimmed)
	if err != nil {
		return nil
	}
	// Cleaning here is what keeps the trailing segments a plain suffix: no ".."
	// survives to be re-joined onto a skill root.
	requested = filepath.Clean(requested)
	if _, err := os.Stat(requested); err == nil {
		return nil
	}
	var out []skillPathMatch
	for _, item := range s.LoadedSkills() {
		if match, ok := matchSkillPath(requested, item); ok {
			out = append(out, match)
		}
	}
	return out
}

// matchSkillPath returns how requested could be naming something in item: the
// file inside item's root that its trailing segments name once the dropped
// directories are restored, and whether the directory it put the skill in is
// one that really holds that skill.
//
// A path that names neither — no file there, and a directory that does not hold
// the skill — is an unrelated path that merely shares a name, and is no match.
func matchSkillPath(requested string, item LoadedSkill) (skillPathMatch, bool) {
	dir, rest := requested, ""
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return skillPathMatch{}, false
		}
		base := filepath.Base(dir)
		if base == item.Name {
			candidate := item.RootDir
			if rest != "" {
				candidate = filepath.Join(item.RootDir, rest)
			}
			file := ""
			// The same pathguard an ordinary catalog read goes through, so a
			// symlink inside a skill cannot name a file outside it.
			if abs, err := ResolveWithinRoots(candidate, []string{item.RootDir}); err == nil {
				if fi, statErr := os.Stat(abs); statErr == nil && fi.Mode().IsRegular() {
					file = abs
				}
			}
			shortened := pathWithinRoot(item.RootDir, parent)
			if file != "" || shortened {
				return skillPathMatch{Skill: item, File: file, Shortened: shortened}, true
			}
		}
		if rest == "" {
			rest = base
		} else {
			rest = filepath.Join(base, rest)
		}
		dir = parent
	}
}

// resolveShortenedSkillPath repairs the one mistake the skill catalog invites:
// a catalog path quoted with the directories above the skill dropped. The
// catalog hands the model `<skills>/.system/caveman/caveman-commit/SKILL.md`,
// and a model that reads the skill name as the name of the directory holding it
// asks for `<skills>/.system/caveman-commit/SKILL.md`.
//
// The repair is deliberately narrow, because silently reading the wrong file is
// worse than failing a read: it fires only for a path whose own directories say
// it was reaching into this skill, it never leaves the skill root, and two
// skills that both explain the path are an ambiguity rather than a guess
// between them. No location becomes readable that ResolveLoadedSkillRead did
// not already reach, and an existing path is never rewritten.
func (s *State) resolveShortenedSkillPath(path string) (string, LoadedSkill, bool) {
	var (
		resolved string
		matched  LoadedSkill
	)
	for _, match := range s.matchSkillPaths(path) {
		if !match.Shortened || match.File == "" {
			continue
		}
		if resolved != "" && resolved != match.File {
			return "", LoadedSkill{}, false
		}
		resolved, matched = match.File, match.Skill
	}
	if resolved == "" {
		return "", LoadedSkill{}, false
	}
	return resolved, matched, true
}

// loadedSkillForMissingPath names the skill a path that is not there was
// reaching for, when exactly one loaded skill explains it. It answers the reads
// too far off to repair — a skill quoted under the wrong skills root, say —
// where the alternative is handing the model a bare "no such file" about a file
// the catalog told it to read.
func (s *State) loadedSkillForMissingPath(path string) (LoadedSkill, bool) {
	var matched LoadedSkill
	for _, match := range s.matchSkillPaths(path) {
		if matched.RootDir != "" && matched.RootDir != match.Skill.RootDir {
			return LoadedSkill{}, false
		}
		matched = match.Skill
	}
	return matched, matched.RootDir != ""
}

// loadedSkillAddressedByPath returns the loaded skill a path is addressing: the
// one that contains it, or — for a path that is not there — the one it is a
// shortened catalog path into, where the directory it puts the skill in really
// holds that skill.
//
// The second case is what keeps the catalog's boundary from being a matter of
// spelling. A model that quotes the catalog path with the bundle directory
// dropped is addressing that skill, and a write there is a write to the skill:
// answered any other way it becomes a stray directory beside the real one,
// holding an edit that changed nothing and a name that discovery then hides
// behind the skill it was meant to change. A new skill created under another
// root keeps its own name, because no directory there holds the existing one.
func (s *State) loadedSkillAddressedByPath(path string) (LoadedSkill, bool) {
	if item, ok := s.LoadedSkillForPath(path); ok {
		return item, true
	}
	for _, match := range s.matchSkillPaths(path) {
		if match.Shortened {
			return match.Skill, true
		}
	}
	return LoadedSkill{}, false
}

// SkillDraftTarget is where a write aimed at a skill lands once the skill's
// version store has redirected it: the draft path, the skill it belongs to,
// and — when the write addressed the formal directory — the formal path it
// was aimed at.
type SkillDraftTarget struct {
	SkillName string
	Target    string
	Source    string
	// Addressed is the path the write addressed, in its original spelling.
	// The read tools key their read states by that spelling, and a formal
	// read is what satisfies a redirected write's read requirement, so the
	// redirection keeps it beside the canonical source.
	Addressed string
}

// SkillDraftResolver settles one write path against the versioned skill
// roots. handled=false leaves the path to the ordinary write rules; an error
// is a refusal — the version records are read-only, and a whole skill
// directory is the skills list's to delete, not a file write's.
//
// tool cannot reach the skill package (Layer 2 keeps tool below skill), so
// the runner injects the implementation; this port is the whole contract.
type SkillDraftResolver func(path string) (SkillDraftTarget, bool, error)

// SetSkillDraftResolver installs the draft-redirection port. Nil restores the
// un-redirected behavior, which is what a runtime without versioned skill
// roots — and every existing test harness — runs with.
func (s *State) SetSkillDraftResolver(resolve SkillDraftResolver) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.skillDraftResolve = resolve
	s.mu.Unlock()
}

func (s *State) draftResolver() SkillDraftResolver {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	resolve := s.skillDraftResolve
	s.mu.Unlock()
	return resolve
}

// resolveSkillDraftWrite settles one write path against the draft resolver:
// the redirected target when the version store owns the path, and whether any
// loaded-skill repair happened first, so the caller can attribute the write.
func (s *State) resolveSkillDraftWrite(requested string) (writePathResolution, bool, error) {
	resolve := s.draftResolver()
	if resolve == nil {
		return writePathResolution{}, false, nil
	}
	// The loaded-skill catalog's repair runs first: a shortened catalog path
	// is repaired into the formal directory it named, and it is that formal
	// path the draft redirection then answers for.
	base := strings.TrimSpace(requested)
	if target, item, ok := s.LoadedSkillWriteTarget(requested); ok {
		base = target
		_ = item
	}
	draft, handled, err := resolve(base)
	if err != nil || !handled {
		return writePathResolution{}, handled, err
	}
	draft.Addressed = strings.TrimSpace(requested)
	out := writePathResolution{Abs: draft.Target}
	if normalizeRootPath(draft.Source) != normalizeRootPath(draft.Target) && draft.Source != "" {
		out.Draft = &draft
	}
	if cleaned := normalizeRootPath(requested); cleaned != "" && cleaned != normalizeRootPath(draft.Target) && draft.Source == "" {
		// A write that addressed the draft through a different spelling:
		// still the same draft, still worth naming where it landed.
		out.Draft = &draft
	}
	return out, true, nil
}

// SkillDraftWriteTarget reports the draft path a write path redirects to, for
// callers that need to read or validate what a write will actually touch
// before the write runs. It answers exactly what the write tools' resolution
// answers, refusals included.
func (s *State) SkillDraftWriteTarget(path string) (target string, handled bool, err error) {
	res, handled, err := s.resolveSkillDraftWrite(path)
	if err != nil || !handled {
		return "", handled, err
	}
	return res.Abs, true, nil
}

// draftRedirectReadState answers the read a redirected write still owes. The
// model read the formal file; the version store seeded the draft from the
// published version; a read state recorded for the formal path whose content
// still matches the draft's bytes is a read of exactly those bytes. Anything
// else — no read at all, or a draft that has already moved on — is not.
func (s *State) draftRedirectReadState(res writePathResolution, abs string) (ReadState, bool) {
	if s == nil || res.Draft == nil {
		return ReadState{}, false
	}
	// The formal read is keyed by the spelling the read resolved — the
	// addressed path, or the canonical source when the write addressed a
	// repaired or differently-spelled path.
	var rs ReadState
	var ok bool
	for _, key := range []string{res.Draft.Addressed, res.Draft.Source} {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if candidate, found := s.GetReadState(key); found {
			rs, ok = candidate, true
			break
		}
	}
	if !ok {
		return ReadState{}, false
	}
	raw, err := os.ReadFile(abs)
	if err != nil || !readStateMatchesContent(rs, raw) {
		return ReadState{}, false
	}
	return rs, true
}

// LoadedSkillWriteTarget reports the loaded skill a write path addresses and the
// path the write lands on, repaired where the requested one was a shortened
// catalog path.
//
// It is resolveShortenedSkillPath's repair, applied to writes: the read side
// already decides what such a path means, and a path the read side resolves
// into a skill while the write side resolves somewhere else is how a "change
// this skill" write became a stray file beside it. The repair carries the same
// narrowness — it fires only for a path whose own directories say it was
// reaching into this skill, never leaves the skill root, treats two skills that
// both explain the path as an ambiguity, and never rewrites a path that exists —
// and the caller reports it the same way, so the model is told which file it
// actually wrote.
func (s *State) LoadedSkillWriteTarget(path string) (string, LoadedSkill, bool) {
	if item, ok := s.LoadedSkillForPath(path); ok {
		normalized := normalizeRootPath(path)
		if normalized == "" {
			normalized = filepath.Clean(strings.TrimSpace(path))
		}
		return normalized, item, true
	}
	return s.resolveShortenedSkillPath(path)
}

// LoadedSkillShellAccessReason reports why a shell command needs the user”'s
// explicit approval before it runs: it addresses a loaded skill and cannot be
// proven read-only, so it may rewrite instructions this session loads. Commands
// that do not address a loaded skill, and provably read-only ones, keep the
// normal shell policy and return "".
func (s *State) LoadedSkillShellAccessReason(command, cwd string, readOnly bool) string {
	item, referenced := s.loadedSkillReferencedByShell(command, cwd)
	if !referenced || readOnly {
		return ""
	}
	return fmt.Sprintf("This command is not provably read-only and reaches the %q skill, whose files this session loads its instructions from.", item.Name)
}

func (s *State) loadedSkillReferencedByShell(command, cwd string) (LoadedSkill, bool) {
	if item, ok := s.loadedSkillAddressedByPath(cwd); ok {
		return item, true
	}
	items := s.LoadedSkills()
	for _, item := range items {
		if strings.Contains(command, item.RootDir) {
			return item, true
		}
	}
	for _, token := range safety.SplitShellWords(command) {
		candidates := []string{strings.TrimSpace(strings.Trim(token, ";|&<>"))}
		if eq := strings.IndexByte(token, '='); eq >= 0 && eq+1 < len(token) {
			candidates = append(candidates, strings.TrimSpace(strings.Trim(token[eq+1:], ";|&<>")))
		}
		for _, candidate := range candidates {
			if candidate == "" || candidate == "-" || strings.HasPrefix(candidate, "$") || !safety.LooksLikeFilePath(candidate) {
				continue
			}
			path := candidate
			if !filepath.IsAbs(path) {
				path = filepath.Join(cwd, path)
			}
			if item, ok := s.loadedSkillAddressedByPath(path); ok {
				return item, true
			}
		}
	}
	return LoadedSkill{}, false
}
