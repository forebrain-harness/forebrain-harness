package skill

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pathguard "github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/stretchr/testify/require"
)

const demoSkillMarkdown = `---
name: demo
description: A demo skill for offline install tests
---

Demo body.
`

func zipEntries(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func tarGzEntries(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for name, content := range entries {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	var out bytes.Buffer
	gw := gzip.NewWriter(&out)
	_, err := gw.Write(raw.Bytes())
	require.NoError(t, err)
	require.NoError(t, gw.Close())
	return out.Bytes()
}

// assertNoIncomingLeft pins that a failed unpack cleans up after itself: no
// .incoming-* directory may survive beside the destination skills root.
func assertNoIncomingLeft(t *testing.T, destDir string) {
	t.Helper()
	parent := filepath.Dir(destDir)
	items, err := os.ReadDir(parent)
	require.NoError(t, err)
	for _, item := range items {
		require.False(t, strings.HasPrefix(item.Name(), ".incoming-"),
			"leftover unpack directory %s under %s", item.Name(), parent)
	}
}

func TestOfflineInstallZipIntoWorkspace(t *testing.T) {
	home := t.TempDir()
	svc := NewService(home)
	data := zipEntries(t, map[string]string{
		"demo/SKILL.md":       demoSkillMarkdown,
		"demo/scripts/run.sh": "echo run\n",
	})
	names, err := svc.InstallOfflineArchive("demo.zip", data, ScopeWorkspace)
	require.NoError(t, err)
	require.Equal(t, []string{"demo"}, names)

	skillDir := filepath.Join(home, "workspace", "skills", "demo")
	raw, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	require.NoError(t, err)
	require.Contains(t, string(raw), "A demo skill for offline install tests")
	raw, err = os.ReadFile(filepath.Join(skillDir, "scripts", "run.sh"))
	require.NoError(t, err)
	require.Equal(t, "echo run\n", string(raw))

	entries, err := DiscoverForWorkspace(home, filepath.Join(home, "workspace"), "")
	require.NoError(t, err)
	found := false
	for _, entry := range entries {
		if entry.Name == "demo" && entry.Source == string(SourceWorkspace) {
			found = true
		}
	}
	require.True(t, found, "installed skill is not discoverable: %+v", entries)
}

func TestOfflineInstallTarGzIntoGlobal(t *testing.T) {
	home := t.TempDir()
	svc := NewService(home)
	data := tarGzEntries(t, map[string]string{
		"demo/SKILL.md": demoSkillMarkdown,
	})
	names, err := svc.InstallOfflineArchive("demo.tar.gz", data, ScopeGlobal)
	require.NoError(t, err)
	require.Equal(t, []string{"demo"}, names)
	require.FileExists(t, filepath.Join(home, "skills", "demo", "SKILL.md"))
}

// A single-skill archive with SKILL.md at its root is the layout this
// product's own download endpoint produces; installing it back must work.
func TestOfflineInstallRootLevelSkillMd(t *testing.T) {
	home := t.TempDir()
	svc := NewService(home)
	data := zipEntries(t, map[string]string{
		"SKILL.md":  demoSkillMarkdown,
		"helper.md": "# helper\n",
	})
	names, err := svc.InstallOfflineArchive("demo.zip", data, ScopeWorkspace)
	require.NoError(t, err)
	require.Equal(t, []string{"demo"}, names)
	require.FileExists(t, filepath.Join(home, "workspace", "skills", "demo", "SKILL.md"))
	require.FileExists(t, filepath.Join(home, "workspace", "skills", "demo", "helper.md"))
}

// A nameless frontmatter falls back to the archive's own file name.
func TestOfflineInstallRootLevelSkillMdFallsBackToFileName(t *testing.T) {
	home := t.TempDir()
	svc := NewService(home)
	data := zipEntries(t, map[string]string{
		"SKILL.md": "---\ndescription: No name here\n---\n\nbody\n",
	})
	names, err := svc.InstallOfflineArchive("My Tool.zip", data, ScopeWorkspace)
	require.NoError(t, err)
	require.Equal(t, []string{"My-Tool"}, names)
}

func TestOfflineInstallMultiSkillPackage(t *testing.T) {
	home := t.TempDir()
	svc := NewService(home)
	data := zipEntries(t, map[string]string{
		"one/SKILL.md": "---\nname: one\ndescription: first\n---\n\nbody\n",
		"two/SKILL.md": "---\nname: two\ndescription: second\n---\n\nbody\n",
	})
	names, err := svc.InstallOfflineArchive("pack.zip", data, ScopeWorkspace)
	require.NoError(t, err)
	require.Equal(t, []string{"one", "two"}, names)
	require.FileExists(t, filepath.Join(home, "workspace", "skills", "one", "SKILL.md"))
	require.FileExists(t, filepath.Join(home, "workspace", "skills", "two", "SKILL.md"))
}

func TestOfflineInstallZipSlipRejected(t *testing.T) {
	home := t.TempDir()
	svc := NewService(home)
	data := zipEntries(t, map[string]string{
		"../evil.txt": "escaped",
	})
	_, err := svc.InstallOfflineArchive("evil.zip", data, ScopeWorkspace)
	require.ErrorIs(t, err, pathguard.ErrPathNotAllowed)
	require.NoFileExists(t, filepath.Join(home, "workspace", "evil.txt"))
	destDir := filepath.Join(home, "workspace", "skills")
	assertNoIncomingLeft(t, destDir)
}

func TestOfflineInstallAbsoluteEntryRejected(t *testing.T) {
	home := t.TempDir()
	svc := NewService(home)
	data := zipEntries(t, map[string]string{
		"/etc/fbevil.txt": "absolute",
	})
	_, err := svc.InstallOfflineArchive("evil.zip", data, ScopeWorkspace)
	require.ErrorIs(t, err, pathguard.ErrPathNotAllowed)
	assertNoIncomingLeft(t, filepath.Join(home, "workspace", "skills"))
}

// A high-ratio archive that would expand past the total budget is rejected,
// and the unpack directory it used is removed.
func TestOfflineInstallBombRejected(t *testing.T) {
	home := t.TempDir()
	svc := NewService(home)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	chunk := bytes.Repeat([]byte{0}, 1<<20)
	// 5 entries x 60 MiB = 300 MiB expanded, well past the 256 MiB budget,
	// while the zip itself stays tiny.
	for i := 0; i < 5; i++ {
		w, err := zw.Create(fmt.Sprintf("bomb%d/SKILL.md", i))
		require.NoError(t, err)
		for j := 0; j < 60; j++ {
			_, err = w.Write(chunk)
			require.NoError(t, err)
		}
	}
	require.NoError(t, zw.Close())

	destDir := filepath.Join(home, "workspace", "skills")
	_, err := svc.InstallOfflineArchive("bomb.zip", buf.Bytes(), ScopeWorkspace)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expands beyond")
	assertNoIncomingLeft(t, destDir)
}

func TestOfflineInstallEntryTooLargeRejected(t *testing.T) {
	home := t.TempDir()
	svc := NewService(home)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("big/SKILL.md")
	require.NoError(t, err)
	chunk := bytes.Repeat([]byte{0}, 1<<20)
	for i := 0; i < 65; i++ {
		_, err = w.Write(chunk)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())

	_, err = svc.InstallOfflineArchive("big.zip", buf.Bytes(), ScopeWorkspace)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expands beyond")
	assertNoIncomingLeft(t, filepath.Join(home, "workspace", "skills"))
}

func TestOfflineInstallZipSymlinkEntryRejected(t *testing.T) {
	home := t.TempDir()
	svc := NewService(home)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: "demo/link", Method: zip.Deflate}
	h.SetMode(os.ModeSymlink | 0o777)
	w, err := zw.CreateHeader(h)
	require.NoError(t, err)
	_, err = w.Write([]byte("/etc/passwd"))
	require.NoError(t, err)
	md, err := zw.Create("demo/SKILL.md")
	require.NoError(t, err)
	_, err = md.Write([]byte(demoSkillMarkdown))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	_, err = svc.InstallOfflineArchive("link.zip", buf.Bytes(), ScopeWorkspace)
	require.Error(t, err)
	require.Contains(t, err.Error(), "symbolic link")
	require.NoFileExists(t, filepath.Join(home, "workspace", "skills", "demo", "link"))
	assertNoIncomingLeft(t, filepath.Join(home, "workspace", "skills"))
}

func TestOfflineInstallTarSymlinkEntryRejected(t *testing.T) {
	home := t.TempDir()
	svc := NewService(home)
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "demo/link",
		Linkname: "/etc/passwd",
		Typeflag: tar.TypeSymlink,
		Mode:     0o777,
	}))
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "demo/SKILL.md",
		Mode: 0o644,
		Size: int64(len(demoSkillMarkdown)),
	}))
	_, err := tw.Write([]byte(demoSkillMarkdown))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	var out bytes.Buffer
	gw := gzip.NewWriter(&out)
	_, err = gw.Write(raw.Bytes())
	require.NoError(t, err)
	require.NoError(t, gw.Close())

	_, err = svc.InstallOfflineArchive("link.tgz", out.Bytes(), ScopeWorkspace)
	require.Error(t, err)
	require.Contains(t, err.Error(), "link")
	assertNoIncomingLeft(t, filepath.Join(home, "workspace", "skills"))
}

func TestOfflineInstallDuplicateNameRejectedWithoutOverwrite(t *testing.T) {
	home := t.TempDir()
	svc := NewService(home)
	first := zipEntries(t, map[string]string{
		"demo/SKILL.md": demoSkillMarkdown,
	})
	_, err := svc.InstallOfflineArchive("demo.zip", first, ScopeWorkspace)
	require.NoError(t, err)

	edited := strings.Replace(demoSkillMarkdown, "A demo skill for offline install tests", "edited original", 1)
	second := zipEntries(t, map[string]string{
		"demo/SKILL.md":  edited,
		"other/SKILL.md": "---\nname: other\ndescription: also here\n---\n\nbody\n",
	})
	_, err = svc.InstallOfflineArchive("demo.zip", second, ScopeWorkspace)
	require.ErrorIs(t, err, ErrSkillAlreadyExists)

	// The whole upload failed: neither the existing skill was replaced nor
	// the second skill half-installed.
	raw, err := os.ReadFile(filepath.Join(home, "workspace", "skills", "demo", "SKILL.md"))
	require.NoError(t, err)
	require.Contains(t, string(raw), "A demo skill for offline install tests")
	require.NoDirExists(t, filepath.Join(home, "workspace", "skills", "other"))
}

func TestOfflineInstallNoSkillMdRejected(t *testing.T) {
	home := t.TempDir()
	svc := NewService(home)
	data := zipEntries(t, map[string]string{
		"demo/readme.md": "not a skill",
	})
	_, err := svc.InstallOfflineArchive("empty.zip", data, ScopeWorkspace)
	require.ErrorIs(t, err, ErrNoSkillsInPackage)
	assertNoIncomingLeft(t, filepath.Join(home, "workspace", "skills"))
}

func TestOfflineInstallUnsupportedExtensionRejected(t *testing.T) {
	home := t.TempDir()
	svc := NewService(home)
	_, err := svc.InstallOfflineArchive("pack.rar", []byte("Rar!\x1a\x07\x00"), ScopeWorkspace)
	require.ErrorIs(t, err, ErrUnsupportedArchive)
	_, err = svc.InstallOfflineArchive("pack.exe", []byte("MZ\x90\x00"), ScopeWorkspace)
	require.ErrorIs(t, err, ErrUnsupportedArchive)
}

// A download that lost its file name is still recognized by its magic bytes.
func TestOfflineInstallByMagicBytes(t *testing.T) {
	home := t.TempDir()
	svc := NewService(home)
	data := zipEntries(t, map[string]string{"demo/SKILL.md": demoSkillMarkdown})
	names, err := svc.InstallOfflineArchive("download", data, ScopeWorkspace)
	require.NoError(t, err)
	require.Equal(t, []string{"demo"}, names)
}

func TestOfflineInstallProjectScopeRequiresTrust(t *testing.T) {
	home := t.TempDir()
	projectRoot := t.TempDir()
	svc := NewServiceForWorkspace(home, filepath.Join(home, "workspace"))
	svc.ProjectRoot = projectRoot
	data := zipEntries(t, map[string]string{"demo/SKILL.md": demoSkillMarkdown})

	_, err := svc.InstallOfflineArchive("demo.zip", data, ScopeProject)
	require.Error(t, err)
	require.Contains(t, err.Error(), "trust")

	project, err := safety.Resolve(projectRoot)
	require.NoError(t, err)
	require.NoError(t, safety.MarkTrusted(home, project))
	names, err := svc.InstallOfflineArchive("demo.zip", data, ScopeProject)
	require.NoError(t, err)
	require.Equal(t, []string{"demo"}, names)
	require.FileExists(t, filepath.Join(projectRoot, ".forebrain", "skills", "demo", "SKILL.md"))
}

func TestOfflineInstallTooManyEntriesRejected(t *testing.T) {
	home := t.TempDir()
	svc := NewService(home)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i <= offlineMaxEntries; i++ {
		w, err := zw.Create(fmt.Sprintf("demo/f%d.txt", i))
		require.NoError(t, err)
		_, err = w.Write([]byte("x"))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	_, err := svc.InstallOfflineArchive("many.zip", buf.Bytes(), ScopeWorkspace)
	require.Error(t, err)
	require.Contains(t, err.Error(), "entries")
	assertNoIncomingLeft(t, filepath.Join(home, "workspace", "skills"))
}

func TestOfflineInstallWritesLockRecord(t *testing.T) {
	home := t.TempDir()
	svc := NewService(home)
	data := zipEntries(t, map[string]string{"demo/SKILL.md": demoSkillMarkdown})
	_, err := svc.InstallOfflineArchive("demo.zip", data, ScopeWorkspace)
	require.NoError(t, err)
	lock, err := ReadLock(filepath.Join(home, "workspace"))
	require.NoError(t, err)
	entries, ok := lock["entries"].(map[string]any)
	require.True(t, ok, "lock has no entries: %+v", lock)
	entry, ok := entries["demo"].(map[string]any)
	require.True(t, ok, "no lock record for demo: %+v", lock)
	require.Equal(t, "archive_upload", entry["source_type"])
}

func TestOfflineFallbackName(t *testing.T) {
	require.Equal(t, "demo", offlineFallbackName("demo.tar.gz"))
	require.Equal(t, "demo", offlineFallbackName("demo.tgz"))
	require.Equal(t, "demo", offlineFallbackName("demo.tar"))
	require.Equal(t, "demo", offlineFallbackName("demo.zip"))
	require.Equal(t, "My Tool", offlineFallbackName("My Tool.zip"))
}

func TestSanitizeOfflineSkillDirName(t *testing.T) {
	require.Equal(t, "My-Tool", sanitizeOfflineSkillDirName("My Tool"))
	require.Equal(t, "a-b", sanitizeOfflineSkillDirName("a/b"))
	require.Equal(t, "etc", sanitizeOfflineSkillDirName("../../etc"))
	require.Equal(t, "abc", sanitizeOfflineSkillDirName(".abc."))
}
