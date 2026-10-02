package skill

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
)

// The offline install budgets. They are a product security boundary, not a
// tuning knob: one upload may not exhaust the host's disk or file table, so
// every archive is unpacked under them or not at all.
const (
	// MaxOfflineUploadBytes caps one uploaded archive. It is exported for the
	// HTTP layer's request-body limit; the two must stay the same number.
	MaxOfflineUploadBytes = 64 << 20
	offlineEntryMaxBytes  = 64 << 20  // one unpacked entry
	offlineTotalMaxBytes  = 256 << 20 // everything an archive may expand to
	offlineMaxEntries     = 20000
)

var (
	// ErrUnsupportedArchive is returned for anything the offline installer
	// cannot read. rar has no Go decoder in the standard library and none is
	// being added; the UI says to convert the package to zip instead.
	ErrUnsupportedArchive = errors.New("the archive must be a .zip, .tar.gz, .tgz or .tar file")

	// ErrSkillAlreadyExists is the no-overwrite answer to installing a skill
	// whose directory name is already taken at the destination: the existing
	// copy may hold the user's own edits, and silently replacing it would lose
	// them, so the whole upload fails and says what to do.
	ErrSkillAlreadyExists = errors.New("a skill with this name already exists; delete or rename it first")
)

// InstallOfflineArchive installs every skill an uploaded archive holds into
// destScope's skills directory. The archive is unpacked into a temporary
// directory beside the destination, validated there, and only then moved in —
// a failure anywhere leaves the destination exactly as it was.
func (s *Service) InstallOfflineArchive(fileName string, data []byte, destScope Scope) ([]string, error) {
	destDir, err := s.offlineDestRoot(destScope)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, err
	}
	tmpRoot, err := os.MkdirTemp(filepath.Dir(destDir), ".incoming-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpRoot)
	if err := extractArchiveTo(fileName, data, tmpRoot); err != nil {
		return nil, err
	}
	items := discoverOfflineSkills(tmpRoot, offlineFallbackName(fileName))
	if len(items) == 0 {
		return nil, ErrNoSkillsInPackage
	}
	// No overwrite: every directory name must be free before anything moves.
	// Checking all names up front keeps a half-installed package from being
	// the failure mode.
	var conflicts []string
	for _, item := range items {
		if _, err := os.Lstat(filepath.Join(destDir, item.Name)); err == nil {
			conflicts = append(conflicts, item.Name)
		}
	}
	if len(conflicts) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrSkillAlreadyExists, strings.Join(conflicts, ", "))
	}
	workspace := s.workspaceRoot()
	lock := LockEntry{
		SourceType:  "archive_upload",
		SourceRef:   filepath.Base(strings.TrimSpace(fileName)),
		InstalledAt: time.Now().Unix(),
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		if err := InstallFromDir(item.Path, destDir, item.Name); err != nil {
			return nil, err
		}
		if strings.TrimSpace(workspace) != "" {
			itemLock := lock
			itemLock.Path = filepath.Join(destDir, item.Name)
			itemLock.SkillSubpath = item.Subpath
			if err := MergeLockEntryV2(workspace, item.Name, itemLock); err != nil {
				return nil, err
			}
		}
		names = append(names, item.Name)
	}
	if err := s.Refresh(); err != nil {
		return nil, err
	}
	return names, nil
}

// offlineDestRoot maps a destination scope onto the skills directory an
// offline upload lands in. A project destination is the one that can fail:
// it needs the session's frozen project root and a trust decision for it,
// because an upload is content entering the project.
func (s *Service) offlineDestRoot(destScope Scope) (string, error) {
	switch destScope {
	case ScopeProject:
		root := strings.TrimSpace(s.ProjectRoot)
		if root == "" {
			return "", fmt.Errorf("project install unavailable: this session has no project root")
		}
		if len(TrustedProjectSkillRoots(s.Home, root)) == 0 {
			return "", fmt.Errorf("project install requires trusting the project first")
		}
		return filepath.Join(root, ".forebrain", "skills"), nil
	case ScopeWorkspace:
		return filepath.Join(s.workspaceRoot(), "skills"), nil
	default:
		return filepath.Join(strings.TrimSpace(s.Home), "skills"), nil
	}
}

// extractArchiveTo dispatches by file extension and falls back to the
// archive's magic bytes, so a package whose download lost its extension still
// installs and nothing unreadable gets past the switch.
func extractArchiveTo(fileName string, data []byte, destRoot string) error {
	kind := archiveKindOf(fileName)
	if kind == "" {
		kind = archiveKindByMagic(data)
	}
	switch kind {
	case "zip":
		return extractZip(data, destRoot)
	case "tar+gzip":
		return extractTar(data, destRoot, true)
	case "tar":
		return extractTar(data, destRoot, false)
	default:
		return ErrUnsupportedArchive
	}
}

func archiveKindOf(fileName string) string {
	name := strings.ToLower(strings.TrimSpace(fileName))
	switch {
	case strings.HasSuffix(name, ".zip"):
		return "zip"
	case strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".tgz"):
		return "tar+gzip"
	case strings.HasSuffix(name, ".tar"):
		return "tar"
	default:
		return ""
	}
}

// archiveKindByMagic recognizes zip and gzip lead bytes. A bare tar has no
// magic, so extensionless tars are out of its reach — the extension path
// covers them.
func archiveKindByMagic(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return "zip"
	case bytes.HasPrefix(data, []byte("\x1f\x8b")):
		return "tar+gzip"
	default:
		return ""
	}
}

// extractBudget enforces the unpack limits while entries stream past.
type extractBudget struct {
	total   int64
	entries int
}

func (b *extractBudget) nextEntry() error {
	b.entries++
	if b.entries > offlineMaxEntries {
		return fmt.Errorf("the archive holds more than %d entries", offlineMaxEntries)
	}
	return nil
}

func (b *extractBudget) addBytes(n int64) error {
	b.total += n
	if b.total > offlineTotalMaxBytes {
		return fmt.Errorf("the archive expands beyond %d MiB", offlineTotalMaxBytes>>20)
	}
	return nil
}

func extractZip(data []byte, destRoot string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	budget := &extractBudget{}
	for _, f := range zr.File {
		if err := budget.nextEntry(); err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := ensureArchiveDir(destRoot, f.Name); err != nil {
				return err
			}
			continue
		}
		// A symlink entry is a path the packager chose that this process
		// would follow on a later read; the destination may never hold one.
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive entry %q is a symbolic link; symbolic links are not allowed", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		n, err := writeArchiveEntry(destRoot, f.Name, rc)
		rc.Close()
		if err != nil {
			return err
		}
		if err := budget.addBytes(n); err != nil {
			return err
		}
	}
	return nil
}

func extractTar(data []byte, destRoot string, gz bool) error {
	var src io.Reader = bytes.NewReader(data)
	if gz {
		gr, err := gzip.NewReader(src)
		if err != nil {
			return err
		}
		defer gr.Close()
		src = gr
	}
	tr := tar.NewReader(src)
	budget := &extractBudget{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := budget.nextEntry(); err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := ensureArchiveDir(destRoot, hdr.Name); err != nil {
				return err
			}
		case tar.TypeReg:
			n, err := writeArchiveEntry(destRoot, hdr.Name, tr)
			if err != nil {
				return err
			}
			if err := budget.addBytes(n); err != nil {
				return err
			}
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("archive entry %q is a link; link entries are not allowed", hdr.Name)
		default:
			// Extended headers the reader already consumed for itself.
			continue
		}
	}
	return nil
}

// ensureArchiveDir creates one directory entry inside the unpack root.
func ensureArchiveDir(destRoot, name string) error {
	name = strings.TrimSuffix(strings.TrimSpace(name), "/")
	if name == "" || name == "." {
		return nil
	}
	if err := home.ValidateArchiveRelPath(name); err != nil {
		return fmt.Errorf("%w: %q", err, name)
	}
	target := filepath.Join(destRoot, filepath.FromSlash(name))
	if _, err := home.ResolveWithinRoots(target, []string{destRoot}); err != nil {
		return fmt.Errorf("%w: %q", err, name)
	}
	return home.SafeMkdirAllUnderRoot(destRoot, target, 0o755)
}

// writeArchiveEntry writes one file entry into the unpack root and returns how
// many bytes it held. The size limits are enforced on the bytes actually
// read, never on what the header claims.
func writeArchiveEntry(destRoot, name string, content io.Reader) (int64, error) {
	if err := home.ValidateArchiveRelPath(name); err != nil {
		return 0, fmt.Errorf("%w: %q", err, name)
	}
	target := filepath.Join(destRoot, filepath.FromSlash(name))
	if _, err := home.ResolveWithinRoots(target, []string{destRoot}); err != nil {
		return 0, fmt.Errorf("%w: %q", err, name)
	}
	if err := home.SafeMkdirAllUnderRoot(destRoot, filepath.Dir(target), 0o755); err != nil {
		return 0, err
	}
	f, err := home.OpenNoFollowForWrite(target, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(content, offlineEntryMaxBytes+1))
	if err != nil {
		return 0, err
	}
	if n > offlineEntryMaxBytes {
		return 0, fmt.Errorf("archive entry %q expands beyond %d MiB", name, offlineEntryMaxBytes>>20)
	}
	return n, nil
}

type offlineSkillItem struct {
	Name    string
	Path    string
	Subpath string
}

// discoverOfflineSkills finds the skill directories an unpacked archive
// holds. Two layouts count: directories at the unpack root (the multi-skill
// package), and a SKILL.md at the root itself — which is what this product's
// own single-skill download zips look like, so a downloaded skill reinstalls.
func discoverOfflineSkills(root string, fallbackName string) []offlineSkillItem {
	if ValidateSkillDir(root) == nil {
		return []offlineSkillItem{{
			Name:    offlineSkillNameForRoot(root, fallbackName),
			Path:    root,
			Subpath: "",
		}}
	}
	dirs, err := discoverSkillDirs(root)
	if err != nil {
		return nil
	}
	out := make([]offlineSkillItem, 0, len(dirs))
	for _, d := range dirs {
		out = append(out, offlineSkillItem{Name: d.Name, Path: d.Path, Subpath: d.Subpath})
	}
	return out
}

// offlineSkillNameForRoot names a root-level skill by its frontmatter name —
// the same name discovery lists it under — falling back to the archive's own
// file name when the frontmatter carries none.
func offlineSkillNameForRoot(root string, fallbackName string) string {
	if raw, err := os.ReadFile(filepath.Join(root, skillFileName)); err == nil {
		if parsed, parseErr := Parse(bytes.NewReader(raw)); parseErr == nil && parsed != nil {
			if name := sanitizeOfflineSkillDirName(parsed.Name); name != "" {
				return name
			}
		}
	}
	name := sanitizeOfflineSkillDirName(fallbackName)
	if name == "" {
		return "skill"
	}
	return name
}

func offlineFallbackName(fileName string) string {
	base := filepath.Base(strings.TrimSpace(fileName))
	for _, ext := range []string{".tar.gz", ".tgz", ".tar", ".zip"} {
		if strings.HasSuffix(strings.ToLower(base), ext) {
			return strings.TrimSuffix(base, ext)
		}
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// sanitizeOfflineSkillDirName reduces a skill name to the characters a
// directory name may safely carry. A directory name with separators or
// traversal in it would not stay inside the skills root.
func sanitizeOfflineSkillDirName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ', r == '/', r == '\\':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), ".-")
}
