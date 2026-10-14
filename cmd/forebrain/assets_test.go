package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
)

// assetsChecksum is the declared checksum of a test dictionary's bytes.
func assetsChecksum(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// writeAssetsFile puts content at path, creating the parent directories.
func writeAssetsFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeAssetsDictionaries swaps the compiled-in list for one whose bytes live in
// a temp assets directory. The real checksums cannot be forged, so a test that
// drives the command over test files must swap the list; the original is
// restored when the test ends.
func fakeAssetsDictionaries(t *testing.T, body string) {
	t.Helper()
	original := memory.DictionaryFiles
	t.Cleanup(func() { memory.DictionaryFiles = original })
	memory.DictionaryFiles = []memory.DictionaryFile{{Name: "zh/one.txt", SHA256: assetsChecksum(body)}}
}

// A developer's FOREBRAIN_ASSETS_DIR is installed into the home, so a machine
// the packaging scripts never touched can still be made to search.
func TestAssetsInstallCopiesFromTheAssetsDirIntoTheHome(t *testing.T) {
	body := "words\n"
	fakeAssetsDictionaries(t, body)
	assets := t.TempDir()
	writeAssetsFile(t, filepath.Join(assets, memory.DictionaryDir, "zh", "one.txt"), body)
	t.Setenv("FOREBRAIN_ASSETS_DIR", assets)

	out, err := runRootCommand(t, "assets", "install")
	if err != nil {
		t.Fatalf("assets install: %v\n%s", err, out)
	}
	installed := filepath.Join(os.Getenv("FOREBRAIN_HOME"), memory.DictionaryDir, "zh", "one.txt")
	if data, err := os.ReadFile(installed); err != nil || string(data) != body {
		t.Fatalf("home copy = %q err=%v, want %q seeded from the assets dir", data, err, body)
	}
}

// --bin-dir writes dict/ beside a binary, the placement the build scripts use.
func TestAssetsInstallBinDirWritesBesideTheBinary(t *testing.T) {
	binDir := t.TempDir()
	out, err := runRootCommand(t, "assets", "install", "--bin-dir", binDir)
	if err != nil {
		t.Fatalf("assets install --bin-dir: %v\n%s", err, out)
	}
	for _, file := range memory.DictionaryFiles {
		installed := filepath.Join(binDir, memory.DictionaryDir, filepath.FromSlash(file.Name))
		if err := memory.CheckDictionaryFile(installed, file.Name); err != nil {
			t.Errorf("%s was not installed beside the binary: %v", file.Name, err)
		}
	}
}

// With no source anywhere — no assets dir, no copy beside the binary, no release
// to download from — the install reports failure instead of pretending success.
func TestAssetsInstallWithoutASourceFails(t *testing.T) {
	fakeAssetsDictionaries(t, "words\n")
	t.Setenv("FOREBRAIN_ASSETS_DIR", t.TempDir()) // empty: no dictionary in it

	out, err := runRootCommand(t, "assets", "install")
	if err == nil {
		t.Fatalf("an install with no source at all succeeded:\n%s", out)
	}
}

// status reports each dictionary and, while any is missing, exits non-zero with
// the product command that fixes it.
func TestAssetsStatusReportsMissingDictionaries(t *testing.T) {
	fakeAssetsDictionaries(t, "words\n")
	t.Setenv("FOREBRAIN_ASSETS_DIR", t.TempDir())

	out, err := runRootCommand(t, "assets", "status")
	if err == nil {
		t.Fatalf("status with a missing dictionary exited 0:\n%s", out)
	}
	if !containsLineWithPrefix(out, "zh/one.txt") {
		t.Fatalf("status did not report the declared dictionary:\n%s", out)
	}
}
