package skill

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func inferSourceKind(sourceRef string) (string, error) {
	sourceRef = strings.TrimSpace(sourceRef)
	if sourceRef == "" {
		return "", fmt.Errorf("empty sourceRef")
	}
	if u, err := url.Parse(sourceRef); err == nil && strings.TrimSpace(u.Scheme) != "" && (strings.TrimSpace(u.Host) != "" || u.Scheme == "file") {
		return "git_repo", nil
	}
	parts := strings.Split(strings.Trim(sourceRef, "/"), "/")
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return "skills_sh", nil
	}
	return "", fmt.Errorf("%w: %q", ErrUnsupportedSource, sourceRef)
}

func installFromGitRepo(ctx context.Context, workspace, sourceRef, destDir, skillName, name, ref string, emit func(InstallProgress)) ([]string, error) {
	lock := LockEntry{
		SourceType: "git_repo",
		SourceRef:  strings.TrimSpace(sourceRef),
	}
	if emit != nil {
		emit(InstallProgress{
			Phase:      "downloading",
			PhaseLabel: "Downloading",
			Progress:   0.15,
			Skill:      strings.TrimSpace(skillName),
			Name:       strings.TrimSpace(name),
		})
	}
	if strings.TrimSpace(skillName) == "" {
		return installSkillPackageFromFetcher(ctx, workspace, destDir, name, lock, emit, func(tmpDir, repoName string) error {
			return InstallFromGitRecord(ctx, "", sourceRef, tmpDir, repoName, ref)
		})
	}
	tmpDir, err := os.MkdirTemp("", "forebrain-skill-install-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	repoName := "repo"
	if err := InstallFromGitRecord(ctx, "", sourceRef, tmpDir, repoName, ref); err != nil {
		return nil, err
	}
	repoRoot := filepath.Join(tmpDir, repoName)
	lock.Revision = gitRevision(ctx, repoRoot)
	lock.InstalledAt = time.Now().Unix()
	skillDir, err := resolveSkillSubdir(repoRoot, skillName)
	if err != nil {
		return nil, err
	}
	if emit != nil {
		emit(InstallProgress{
			Phase:          "discovered",
			PhaseLabel:     "Skills discovered",
			Progress:       0.55,
			Count:          1,
			InstalledNames: []string{strings.TrimSpace(skillDir.Name)},
			Skill:          strings.TrimSpace(skillName),
			Name:           strings.TrimSpace(name),
		})
	}
	finalName := strings.TrimSpace(name)
	if finalName == "" {
		finalName = skillDir.Name
	}
	lock.SkillSubpath = skillDir.Subpath
	if err := installSkillDirWithLock(workspace, skillDir.Path, destDir, finalName, lock); err != nil {
		return nil, err
	}
	return []string{finalName}, nil
}

func installFromSkillsSh(ctx context.Context, workspace, sourceRef, destDir, skillName, name, ref string, emit func(InstallProgress)) ([]string, error) {
	lock := LockEntry{
		SourceType: "git_repo",
		SourceRef:  normalizeSkillsShRepoURL(sourceRef),
	}
	if emit != nil {
		emit(InstallProgress{
			Phase:      "downloading",
			PhaseLabel: "Downloading",
			Progress:   0.15,
			Skill:      strings.TrimSpace(skillName),
			Name:       strings.TrimSpace(name),
		})
	}
	if strings.TrimSpace(skillName) == "" {
		return installSkillPackageFromFetcher(ctx, workspace, destDir, name, lock, emit, func(tmpDir, repoName string) error {
			return InstallFromSkillsShRecord(ctx, "", sourceRef, tmpDir, repoName, ref)
		})
	}
	tmpDir, err := os.MkdirTemp("", "forebrain-skill-install-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	repoName := "repo"
	if err := InstallFromSkillsShRecord(ctx, "", sourceRef, tmpDir, repoName, ref); err != nil {
		return nil, err
	}
	repoRoot := filepath.Join(tmpDir, repoName)
	lock.Revision = gitRevision(ctx, repoRoot)
	lock.InstalledAt = time.Now().Unix()
	skillDir, err := resolveSkillSubdir(repoRoot, skillName)
	if err != nil {
		return nil, err
	}
	if emit != nil {
		emit(InstallProgress{
			Phase:          "discovered",
			PhaseLabel:     "Skills discovered",
			Progress:       0.55,
			Count:          1,
			InstalledNames: []string{strings.TrimSpace(skillDir.Name)},
			Skill:          strings.TrimSpace(skillName),
			Name:           strings.TrimSpace(name),
		})
	}
	finalName := strings.TrimSpace(name)
	if finalName == "" {
		finalName = skillDir.Name
	}
	lock.SkillSubpath = skillDir.Subpath
	if err := installSkillDirWithLock(workspace, skillDir.Path, destDir, finalName, lock); err != nil {
		return nil, err
	}
	return []string{finalName}, nil
}

func installSkillPackageFromFetcher(ctx context.Context, workspace, destDir, overrideName string, lock LockEntry, emit func(InstallProgress), fetch func(tmpDir, repoName string) error) ([]string, error) {
	tmpDir, err := os.MkdirTemp("", "forebrain-skill-install-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	repoName := "repo"
	if err := fetch(tmpDir, repoName); err != nil {
		return nil, err
	}

	repoRoot := filepath.Join(tmpDir, repoName)
	lock.Revision = gitRevision(ctx, repoRoot)
	lock.InstalledAt = time.Now().Unix()
	collectionSkills, err := discoverSkillDirs(repoRoot)
	if err != nil {
		return nil, err
	}
	if len(collectionSkills) == 0 {
		return nil, ErrNoSkillsInPackage
	}
	if emit != nil {
		discoveredNames := make([]string, 0, len(collectionSkills))
		for _, item := range collectionSkills {
			discoveredNames = append(discoveredNames, strings.TrimSpace(item.Name))
		}
		emit(InstallProgress{
			Phase:          "discovered",
			PhaseLabel:     "Skills discovered",
			Progress:       0.55,
			Count:          len(collectionSkills),
			InstalledNames: discoveredNames,
			Name:           strings.TrimSpace(overrideName),
		})
	}
	if len(collectionSkills) == 1 {
		name := strings.TrimSpace(overrideName)
		if name == "" {
			name = collectionSkills[0].Name
		}
		lock.SkillSubpath = collectionSkills[0].Subpath
		if err := installSkillDirWithLock(workspace, collectionSkills[0].Path, destDir, name, lock); err != nil {
			return nil, err
		}
		return []string{name}, nil
	}
	if strings.TrimSpace(overrideName) != "" {
		return nil, fmt.Errorf("override installed skill name requires selecting a single skill from package")
	}
	names := make([]string, 0, len(collectionSkills))
	for _, item := range collectionSkills {
		itemLock := lock
		itemLock.SkillSubpath = item.Subpath
		if err := installSkillDirWithLock(workspace, item.Path, destDir, item.Name, itemLock); err != nil {
			return nil, err
		}
		names = append(names, item.Name)
	}
	return names, nil
}

type discoveredSkillDir struct {
	Name    string
	Path    string
	Subpath string
}

func discoverSkillDirs(repoRoot string) ([]discoveredSkillDir, error) {
	repoRoot = filepath.Clean(strings.TrimSpace(repoRoot))
	if repoRoot == "" {
		return nil, fmt.Errorf("empty repo root")
	}
	var out []discoveredSkillDir

	rootEntries, err := os.ReadDir(repoRoot)
	if err != nil {
		return nil, err
	}
	for _, entry := range rootEntries {
		if !entry.IsDir() {
			continue
		}
		// Only a directory that would actually load counts as a skill to
		// offer: a SKILL.md with no description installs into nothing.
		if ValidateSkillDir(filepath.Join(repoRoot, entry.Name())) == nil {
			out = append(out, discoveredSkillDir{
				Name:    strings.TrimSpace(entry.Name()),
				Path:    filepath.Join(repoRoot, entry.Name()),
				Subpath: filepath.ToSlash(entry.Name()),
			})
		}
	}
	if len(out) > 0 {
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return out, nil
	}

	collectionsRoot := filepath.Join(repoRoot, "skills")
	entries, err := os.ReadDir(collectionsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if ValidateSkillDir(filepath.Join(collectionsRoot, entry.Name())) == nil {
			out = append(out, discoveredSkillDir{
				Name:    strings.TrimSpace(entry.Name()),
				Path:    filepath.Join(collectionsRoot, entry.Name()),
				Subpath: filepath.ToSlash(filepath.Join("skills", entry.Name())),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func resolveSkillSubdir(repoRoot, skillName string) (*discoveredSkillDir, error) {
	skillName = strings.Trim(strings.TrimSpace(skillName), `/\`)
	if skillName == "" {
		return nil, fmt.Errorf("skill name required")
	}
	candidates := []discoveredSkillDir{
		{
			Name:    filepath.Base(skillName),
			Path:    filepath.Join(repoRoot, filepath.FromSlash(skillName)),
			Subpath: filepath.ToSlash(skillName),
		},
		{
			Name:    filepath.Base(skillName),
			Path:    filepath.Join(repoRoot, "skills", filepath.FromSlash(skillName)),
			Subpath: filepath.ToSlash(filepath.Join("skills", skillName)),
		},
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(filepath.Join(candidate.Path, "SKILL.md")); err != nil {
			continue
		}
		if err := ValidateSkillDir(candidate.Path); err != nil {
			return nil, fmt.Errorf("cannot install %q: %w", candidate.Name, err)
		}
		selected := candidate
		return &selected, nil
	}
	return nil, fmt.Errorf("skill %q not found in package", skillName)
}

func installSkillDirWithLock(workspace, skillDir, destDir, installedName string, lock LockEntry) error {
	// The boundary every install path crosses: nothing enters a skills
	// directory that discovery would then refuse to load.
	if err := ValidateSkillDir(skillDir); err != nil {
		return fmt.Errorf("cannot install %q: %w", strings.TrimSpace(installedName), err)
	}
	targetDir := filepath.Join(destDir, installedName)
	if err := os.RemoveAll(targetDir); err != nil {
		return err
	}
	if err := InstallFromDir(skillDir, destDir, installedName); err != nil {
		return err
	}
	if strings.TrimSpace(workspace) == "" {
		return nil
	}
	lock.Path = targetDir
	return MergeLockEntryV2(workspace, installedName, lock)
}

func normalizeSkillsShRepoURL(sourceRef string) string {
	sourceRef = strings.TrimSpace(sourceRef)
	if sourceRef == "" {
		return ""
	}
	if strings.Contains(sourceRef, "://") {
		return sourceRef
	}
	repo := "https://github.com/" + strings.TrimPrefix(sourceRef, "/")
	if !strings.HasSuffix(repo, ".git") {
		repo += ".git"
	}
	return repo
}

func gitRevision(ctx context.Context, dir string) string {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
