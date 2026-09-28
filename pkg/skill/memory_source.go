package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// memorySourceType marks a lock entry whose skill was promoted out of the
// memory folder, so a later listing can tell a promoted copy apart from one
// installed from a git repo or created by hand.
const memorySourceType = "memory"

// maxPromotedSkillLines caps SKILL.md at the length its own authoring rules
// ask for. The file is model-authored, and a runaway one would become a tool
// description and an activation payload in the sessions that follow.
const maxPromotedSkillLines = 500

// authoredSkillsDirName mirrors memory.AuthoredSkillsDir: the subdirectory
// name memory uses for authored skill proposals within a scope root. skill
// only needs this literal to build a human-readable provenance string
// (LockEntry.SourceRef); duplicating one stable constant across the C7
// boundary is simpler than routing a naming detail through
// AuthoredSkillSource.
const authoredSkillsDirName = "skills"

// promotedSkillNamePattern is the name a skill may be promoted under. It is
// stricter than what the frontmatter parser accepts because the name becomes a
// directory, a tool name, and a slash command.
var promotedSkillNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// AuthoredSkill mirrors the shape of a memory-authored skill proposal that
// skill needs in order to promote it: a display name and description, the
// directory and SKILL.md it lives in, and a content digest used to detect
// whether the source or a previously promoted copy has since diverged.
//
// This is skill's own copy of memory.AuthoredSkill's fields, not a reference
// to that type: skill must not import memory (C7). pkg/memory converts into
// this shape when it implements AuthoredSkillSource.
type AuthoredSkill struct {
	Name        string
	Description string
	Dir         string
	SkillFile   string
	Digest      string
}

// AuthoredSkillSource lists the memory-authored skill proposals relevant to a
// workspace (its project scope plus the agent's global scope). pkg/memory
// implements it; process registers that implementation once at startup via
// SetAuthoredSkillSource — skill has several independent construction sites
// (tui, run, gateway, process itself), so a package-level registration is
// what keeps every skill.Service instance able to serve /memories without
// threading the dependency through each constructor call.
type AuthoredSkillSource func(workspace, projectKey string) ([]AuthoredSkill, error)

var authoredSkillSource AuthoredSkillSource

// SetAuthoredSkillSource registers the memory-backed implementation of
// AuthoredSkillSource. process calls this exactly once during startup,
// wiring memory.ListAuthoredSkillsForWorkspace in (C7: the direction is
// memory -> skill, never the reverse).
func SetAuthoredSkillSource(src AuthoredSkillSource) { authoredSkillSource = src }

// MemorySkillStatus describes a promotable skill relative to the copy this
// workspace already has, if any.
type MemorySkillStatus string

const (
	// MemorySkillNew has never been promoted.
	MemorySkillNew MemorySkillStatus = "new"
	// MemorySkillUpToDate matches the promoted copy on both sides.
	MemorySkillUpToDate MemorySkillStatus = "up-to-date"
	// MemorySkillOutdated means consolidation rewrote the source after the
	// promotion, so promoting again would bring something new.
	MemorySkillOutdated MemorySkillStatus = "outdated"
	// MemorySkillDiverged means the promoted copy was edited after the
	// promotion. Promoting again overwrites those edits.
	MemorySkillDiverged MemorySkillStatus = "diverged"
)

// MemorySkill is one skill the consolidation agent wrote, described well enough
// for a person to decide whether to promote it: what it is, how it relates to
// any copy already promoted, what promoting it would newly expose, and why it
// cannot be promoted when that is the case.
type MemorySkill struct {
	Name        string
	Description string
	SourceDir   string
	Status      MemorySkillStatus
	// InstalledPath is where the promoted copy lives, empty when there is none.
	InstalledPath string
	// SlashCommand is the command promoting would add. Every skill under a
	// scanned root becomes one, which is why a name colliding with a builtin is
	// refused outright rather than merely flagged.
	SlashCommand string
	// Shadows names an already-installed skill of the same name that a promoted
	// copy would take precedence over, since the workspace root outranks the
	// user, agents, and system roots.
	Shadows string
	// Blocked explains why this skill cannot be promoted. A non-empty value
	// means PromoteMemorySkills will refuse it.
	Blocked string
}

// Promotable reports whether this entry may be handed to PromoteMemorySkills.
func (m MemorySkill) Promotable() bool { return strings.TrimSpace(m.Blocked) == "" }

// PromotedSkill is one skill PromoteMemorySkills copied.
type PromotedSkill struct {
	Name         string
	Path         string
	SlashCommand string
}

// listAuthoredSkills asks the registered AuthoredSkillSource for this
// service's proposals. A service with no source registered (a test, or a
// transient Service built only for its skill-command-registry refresh) sees
// none rather than an error, matching how OnRefresh/IsBuiltin already treat an
// unset optional port as a no-op.
func (s *Service) listAuthoredSkills() ([]AuthoredSkill, error) {
	if authoredSkillSource == nil {
		return nil, nil
	}
	return authoredSkillSource(s.workspaceRoot(), s.ProjectKey)
}

// promotionDestDir is the skill root a promotion writes to. Promotion targets
// the workspace root specifically: it is the root whose skills the loader marks
// the skills catalog, so a promoted skill is one the model can see rather than one it
// has to go looking for.
func (s *Service) promotionDestDir() string {
	return filepath.Join(s.workspaceRoot(), "skills")
}

// ListMemorySkills reports the skills consolidation has proposed, each with the
// information a person needs to approve or refuse it.
func (s *Service) ListMemorySkills() ([]MemorySkill, error) {
	authored, err := s.listAuthoredSkills()
	if err != nil {
		return nil, err
	}
	if len(authored) == 0 {
		return nil, nil
	}
	installed, err := s.NewHub().ListManagedDTO()
	if err != nil {
		installed = nil
	}
	destDir := s.promotionDestDir()
	out := make([]MemorySkill, 0, len(authored))
	for _, item := range authored {
		out = append(out, s.describeMemorySkill(item, destDir, installed))
	}
	return out, nil
}

func (s *Service) describeMemorySkill(item AuthoredSkill, destDir string, installed []SkillDTO) MemorySkill {
	described := MemorySkill{
		Name:        item.Name,
		Description: item.Description,
		SourceDir:   item.Dir,
		Status:      MemorySkillNew,
		Blocked:     s.validatePromotableSkill(item),
	}
	described.SlashCommand = "/" + item.Name
	target := filepath.Join(destDir, item.Name)
	for _, candidate := range installed {
		if !strings.EqualFold(strings.TrimSpace(candidate.Name), item.Name) {
			continue
		}
		if sameSkillPath(candidate.RootPath, target) {
			described.InstalledPath = target
			continue
		}
		described.Shadows = strings.TrimSpace(candidate.RootPath)
	}
	if described.InstalledPath == "" {
		return described
	}
	described.Status = s.promotedSkillStatus(item, target)
	return described
}

// promotedSkillStatus compares the source and the promoted copy against what
// the promotion recorded. Divergence is reported ahead of staleness: a person
// re-promoting needs to know their own edits are about to be overwritten more
// urgently than they need to know the source moved on.
func (s *Service) promotedSkillStatus(item AuthoredSkill, target string) MemorySkillStatus {
	entry, ok := LookupLockEntry(s.workspaceRoot(), item.Name)
	if !ok || entry.SourceType != memorySourceType {
		return MemorySkillDiverged
	}
	if installedDigest, err := directoryDigest(target); err != nil || installedDigest != entry.InstalledDigest {
		return MemorySkillDiverged
	}
	if item.Digest != entry.SourceDigest {
		return MemorySkillOutdated
	}
	return MemorySkillUpToDate
}

// validatePromotableSkill returns why a skill may not be promoted, or "" when
// it may. Everything here guards against content a model wrote: the memory
// folder is the consolidation agent's whole filesystem, and promotion is the
// one path out of it.
func (s *Service) validatePromotableSkill(item AuthoredSkill) string {
	if !promotedSkillNamePattern.MatchString(item.Name) {
		return "name must be lowercase letters, digits and hyphens (max 64)"
	}
	if strings.TrimSpace(item.Description) == "" {
		return "frontmatter has no description"
	}
	// A built-in keeps its name, so a skill called "compact" could never be
	// run as /compact: promoting it under that name would make a skill the
	// user can only reach through /skills.
	if s == nil || s.IsBuiltin == nil {
		return "builtin command checker unavailable"
	}
	if s.IsBuiltin(item.Name) {
		return "name collides with the built-in /" + item.Name + " command"
	}
	body, err := os.ReadFile(item.SkillFile)
	if err != nil {
		return "SKILL.md is unreadable"
	}
	if lines := strings.Count(string(body), "\n") + 1; lines > maxPromotedSkillLines {
		return fmt.Sprintf("SKILL.md is %d lines, over the %d line limit", lines, maxPromotedSkillLines)
	}
	// Recomputing the digest re-runs the walk that refuses symlinks and other
	// non-regular files, so a directory that grew one between the listing and
	// the promotion is still caught.
	if _, err := directoryDigest(item.Dir); err != nil {
		return "directory is not safe to copy: " + err.Error()
	}
	return ""
}

// PromoteMemorySkills copies the named skills out of the memory folder into the
// workspace skill root. Every name must be promotable; one refusal fails the
// whole call rather than leaving the caller to work out which half happened.
//
// Like every other lifecycle operation this writes to disk and refreshes the
// process-wide skill metadata, but does not rebuild the running agent: the
// promoted skill enters the skills catalog in the next session. Until then it
// is reachable by /<name>, which loads it into the turn itself.
func (s *Service) PromoteMemorySkills(names []string) ([]PromotedSkill, error) {
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			wanted[strings.ToLower(trimmed)] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return nil, fmt.Errorf("no skills selected")
	}
	authored, err := s.listAuthoredSkills()
	if err != nil {
		return nil, err
	}
	selected := make([]AuthoredSkill, 0, len(wanted))
	for _, item := range authored {
		if _, ok := wanted[strings.ToLower(item.Name)]; !ok {
			continue
		}
		if reason := s.validatePromotableSkill(item); reason != "" {
			return nil, fmt.Errorf("cannot promote %q: %s", item.Name, reason)
		}
		selected = append(selected, item)
		delete(wanted, strings.ToLower(item.Name))
	}
	if len(wanted) > 0 {
		missing := make([]string, 0, len(wanted))
		for name := range wanted {
			missing = append(missing, name)
		}
		return nil, fmt.Errorf("no such memory skill: %s", strings.Join(missing, ", "))
	}

	destDir := s.promotionDestDir()
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, err
	}
	promoted := make([]PromotedSkill, 0, len(selected))
	for _, item := range selected {
		target, err := s.promoteOne(item, destDir)
		if err != nil {
			return nil, fmt.Errorf("promote %q: %w", item.Name, err)
		}
		promoted = append(promoted, PromotedSkill{
			Name:         item.Name,
			Path:         filepath.Join(target, "SKILL.md"),
			SlashCommand: "/" + item.Name,
		})
	}
	if err := s.Refresh(); err != nil {
		return nil, err
	}
	return promoted, nil
}

func (s *Service) promoteOne(item AuthoredSkill, destDir string) (string, error) {
	target := filepath.Join(destDir, item.Name)
	if err := os.RemoveAll(target); err != nil {
		return "", err
	}
	if err := InstallFromDir(item.Dir, destDir, item.Name); err != nil {
		return "", err
	}
	installedDigest, err := directoryDigest(target)
	if err != nil {
		return "", err
	}
	workspace := s.workspaceRoot()
	if strings.TrimSpace(workspace) == "" {
		return target, nil
	}
	// The record lives in the workspace lock file, never inside the memory
	// folder: that folder is a diff-tracked workspace the consolidation agent
	// owns, and a marker written there would show up in the very diff it
	// describes and could be rewritten by the agent.
	return target, MergeLockEntryV2(workspace, item.Name, LockEntry{
		SourceType:      memorySourceType,
		SourceRef:       authoredSkillsDirName + "/" + filepath.Base(item.Dir),
		SkillSubpath:    authoredSkillsDirName + "/" + filepath.Base(item.Dir),
		Path:            target,
		SourceDigest:    item.Digest,
		InstalledDigest: installedDigest,
		InstalledAt:     time.Now().Unix(),
	})
}

func sameSkillPath(left, right string) bool {
	left = filepath.Clean(strings.TrimSpace(left))
	right = filepath.Clean(strings.TrimSpace(right))
	if left == "" || right == "" {
		return false
	}
	if absLeft, err := filepath.Abs(left); err == nil {
		left = absLeft
	}
	if absRight, err := filepath.Abs(right); err == nil {
		right = absRight
	}
	return strings.EqualFold(left, right)
}

// directoryDigest is skill's own copy of memory.DirectoryDigest: a
// content-addressed hash of every regular file under dir, used to tell
// whether a promoted copy or its memory-authored source has changed since
// promotion. It has no memory-specific behavior — duplicating this ~30-line
// utility once is simpler than routing "hash a directory I already have the
// path to" through the AuthoredSkillSource port (C7: skill must not import
// memory).
//
// It fails on anything that is not a regular file or a directory. A symlink
// here is not a curiosity to skip: the copy that promotion performs reads
// through symlinks, so one pointing outside the source root would land its
// target's contents in a directory the model can read. Refusing the whole
// directory is the only answer that cannot be worked around by placing the
// link where the walk happens to skip it.
func directoryDigest(dir string) (string, error) {
	type fileEntry struct {
		rel  string
		body []byte
	}
	var files []fileEntry
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files = append(files, fileEntry{rel: filepath.ToSlash(rel), body: body})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	sum := sha256.New()
	for _, file := range files {
		fmt.Fprintf(sum, "%s|%d|", file.rel, len(file.body))
		sum.Write(file.body)
		sum.Write([]byte{'\n'})
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}
