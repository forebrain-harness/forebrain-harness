package memory

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
)

// TestDictionaryFilesDeclareTheBytesTheirModulesShip guards the compiled
// checksums against dependency drift: when an upgrade of a dictionary's module
// changes its bytes, the checksum in DictionaryFiles must be recomputed with
// it, or cmd/installdict -verify (the npm packaging self-check) and
// forebrain assets install would reject this tree's own files.
func TestDictionaryFilesDeclareTheBytesTheirModulesShip(t *testing.T) {
	bin := t.TempDir()
	if err := InstallDictionary(bin); err != nil {
		t.Fatal(err)
	}
	for _, file := range DictionaryFiles {
		data, err := os.ReadFile(filepath.Join(bin, DictionaryDir, filepath.FromSlash(file.Name)))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != file.SHA256 {
			t.Errorf("%s now ships %s at the version go.mod selects; DictionaryFiles declares %s", file.Name, got, file.SHA256)
		}
	}
}

func TestDictionaryBundleURLOnlyForReleases(t *testing.T) {
	url, ok := dictionaryBundleURL("v1.2.3")
	if !ok {
		t.Fatal("a release version must have a bundle URL")
	}
	want := releaseDownloadURL + "/v1.2.3/" + dictionaryBundle
	if url != want {
		t.Errorf("dictionaryBundleURL(v1.2.3) = %s, want %s", url, want)
	}
	for _, version := range []string{"(devel)", "v0.0.0-20260928-abcdef", "v1.2.3-rc.1", "v1.2.3+dirty", ""} {
		if url, ok := dictionaryBundleURL(version); ok {
			t.Errorf("dictionaryBundleURL(%q) = %s; a version without a release must not download", version, url)
		}
	}
}

// bundleEntry is one entry of a test bundle: a directory, or a regular file
// with content.
type bundleEntry struct {
	name    string
	content string
	isDir   bool
}

// dictionaryBundleGzip builds an in-memory dictionary bundle shaped like the
// release asset: a gzip of a tar whose file entries sit under ./-prefixed
// directories.
func dictionaryBundleGzip(t *testing.T, entries []bundleEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	compressed := gzip.NewWriter(&buf)
	archive := tar.NewWriter(compressed)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Mode: 0o644}
		if entry.isDir {
			header.Typeflag = tar.TypeDir
		} else {
			header.Typeflag = tar.TypeReg
			header.Size = int64(len(entry.content))
		}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if !entry.isDir {
			if _, err := archive.Write([]byte(entry.content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// dictionaryServer serves the given bundle at any path.
func dictionaryServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server
}

// checksumOf is the declared checksum of a test bundle file's content.
func checksumOf(t *testing.T, content string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// fakeDictionaries declares the two files the test bundles carry, with the
// checksums of the given contents.
func fakeDictionaries(t *testing.T, words, dict string) []DictionaryFile {
	t.Helper()
	return []DictionaryFile{
		{Name: "zh/one.txt", SHA256: checksumOf(t, words)},
		{Name: "ja/two.dict", SHA256: checksumOf(t, dict)},
	}
}

// downloadedDictionaries installs the bundle served at server into a dict dir
// under a fresh binary directory, returning that binary directory and the dict
// dir beside it.
func downloadedDictionaries(t *testing.T, server *httptest.Server, files []DictionaryFile) (bin, dictDir string) {
	t.Helper()
	bin = t.TempDir()
	dictDir = filepath.Join(bin, DictionaryDir)
	if err := downloadDictionaries(context.Background(), server.Client(), server.URL+"/"+dictionaryBundle, dictDir, files); err != nil {
		t.Fatal(err)
	}
	return bin, dictDir
}

func TestDownloadDictionariesInstallsVerifiedFiles(t *testing.T) {
	files := fakeDictionaries(t, "words\n", "dict\n")
	server := dictionaryServer(t, dictionaryBundleGzip(t, []bundleEntry{
		{name: "./", isDir: true},
		{name: "./zh/", isDir: true},
		{name: "./ja/", isDir: true},
		{name: "./zh/one.txt", content: "words\n"},
		{name: "./ja/two.dict", content: "dict\n"},
	}))
	bin, dictDir := downloadedDictionaries(t, server, files)
	for path, want := range map[string]string{
		filepath.Join(dictDir, "zh", "one.txt"):  "words\n",
		filepath.Join(dictDir, "ja", "two.dict"): "dict\n",
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("%s installed %q, want %q", path, got, want)
		}
	}
	if leftovers, _ := filepath.Glob(filepath.Join(bin, "dict-download-*")); len(leftovers) > 0 {
		t.Errorf("a verified download left temporary directories behind: %v", leftovers)
	}
}

func TestDownloadDictionariesRejectsAChecksumMismatch(t *testing.T) {
	files := fakeDictionaries(t, "words\n", "dict\n")
	files[0].SHA256 = strings.Repeat("0", 64)
	server := dictionaryServer(t, dictionaryBundleGzip(t, []bundleEntry{
		{name: "./zh/one.txt", content: "words\n"},
		{name: "./ja/two.dict", content: "dict\n"},
	}))
	bin := t.TempDir()
	dictDir := filepath.Join(bin, DictionaryDir)
	err := downloadDictionaries(context.Background(), server.Client(), server.URL+"/"+dictionaryBundle, dictDir, files)
	if err == nil {
		t.Fatal("a checksum mismatch was installed")
	}
	if installed, listErr := os.ReadDir(dictDir); listErr == nil && len(installed) > 0 {
		t.Errorf("a rejected download left files in %s: %v", dictDir, installed)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(bin, "dict-download-*")); len(leftovers) > 0 {
		t.Errorf("a rejected download left temporary directories behind: %v", leftovers)
	}
}

func TestDownloadDictionariesRejectsUnexpectedEntries(t *testing.T) {
	for name, extra := range map[string]bundleEntry{
		"a path traversal":    {name: "../evil"},
		"an undeclared entry": {name: "./extra.txt", content: "extra"},
	} {
		t.Run(name, func(t *testing.T) {
			files := fakeDictionaries(t, "words\n", "dict\n")
			server := dictionaryServer(t, dictionaryBundleGzip(t, []bundleEntry{
				{name: "./zh/one.txt", content: "words\n"},
				{name: "./ja/two.dict", content: "dict\n"},
				extra,
			}))
			bin := t.TempDir()
			err := downloadDictionaries(context.Background(), server.Client(), server.URL+"/"+dictionaryBundle, filepath.Join(bin, DictionaryDir), files)
			if err == nil {
				t.Fatal("an unexpected entry was installed")
			}
			if !strings.Contains(err.Error(), "unexpected file") {
				t.Errorf("error %v does not name the unexpected file", err)
			}
			// The entry was refused before a single byte of it was written:
			// nothing may exist beside the dict dir, which was never created.
			written, listErr := os.ReadDir(bin)
			if listErr != nil || len(written) > 0 {
				t.Errorf("a rejected bundle wrote into %s: %v (error %v)", bin, written, listErr)
			}
		})
	}
}

func TestDownloadDictionariesRequiresEveryFile(t *testing.T) {
	files := fakeDictionaries(t, "words\n", "dict\n")
	server := dictionaryServer(t, dictionaryBundleGzip(t, []bundleEntry{
		{name: "./zh/one.txt", content: "words\n"},
	}))
	bin := t.TempDir()
	err := downloadDictionaries(context.Background(), server.Client(), server.URL+"/"+dictionaryBundle, filepath.Join(bin, DictionaryDir), files)
	if err == nil {
		t.Fatal("an incomplete bundle was installed")
	}
	if !strings.Contains(err.Error(), "ja/two.dict is missing") {
		t.Errorf("error %v does not name the missing dictionary", err)
	}
}

func TestDownloadDictionariesReportsTheHTTPStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such release", http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	bin := t.TempDir()
	files := fakeDictionaries(t, "words\n", "dict\n")
	err := downloadDictionaries(context.Background(), server.Client(), server.URL+"/"+dictionaryBundle, filepath.Join(bin, DictionaryDir), files)
	if err == nil {
		t.Fatal("a 404 bundle URL was accepted")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error %v does not report the HTTP status", err)
	}
}

// TestReleasedDictionaryBundleMatchesDictionaryFiles checks a real released
// bundle against the checksums this binary was compiled with. It is skipped
// locally; the release workflow sets FOREBRAIN_RELEASE_DICT_CHECK to the
// version it just published and runs it.
func TestReleasedDictionaryBundleMatchesDictionaryFiles(t *testing.T) {
	version := os.Getenv("FOREBRAIN_RELEASE_DICT_CHECK")
	if version == "" {
		t.Skip("set FOREBRAIN_RELEASE_DICT_CHECK=<version> to check that release's dictionary bundle")
	}
	url, ok := dictionaryBundleURL(version)
	if !ok {
		t.Fatalf("%s is not a release version", version)
	}
	bin := t.TempDir()
	client := &http.Client{Timeout: 5 * time.Minute}
	if err := downloadDictionaries(context.Background(), client, url, filepath.Join(bin, DictionaryDir), DictionaryFiles); err != nil {
		t.Fatal(err)
	}
}

// writeDictionaryFile puts content at path, creating the parent directories, so
// a resolution test can stand up a copy at one of the places a search reads.
func writeDictionaryFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The dictionaries are a home-level install: a binary that came with a copy
// beside it seeds the home, and every later read — in this process or any other
// binary sharing the home — reads the home copy.
func TestDictionaryResolutionSeedsTheHomeFromTheCopyBesideTheBinary(t *testing.T) {
	files := fakeDictionaries(t, "words\n", "dict\n")
	homeDir := filepath.Join(t.TempDir(), DictionaryDir)
	shippedDir := filepath.Join(t.TempDir(), DictionaryDir)
	writeDictionaryFile(t, filepath.Join(shippedDir, filepath.FromSlash(files[0].Name)), "words\n")

	got, err := resolveDictionaryFile(homeDir, shippedDir, files[0].Name, files)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(homeDir, filepath.FromSlash(files[0].Name))
	if got != want {
		t.Fatalf("read from %s, want the home copy %s", got, want)
	}
	if data, err := os.ReadFile(want); err != nil || string(data) != "words\n" {
		t.Fatalf("home copy = %q err=%v, want the seeded bytes", data, err)
	}
}

// A copy that does not match the checksum this binary declares is never
// adopted: the index and the query must be cut by the bytes this binary
// declared, or the two sides spell terms differently.
func TestDictionaryResolutionRefusesAnUndeclaredCopy(t *testing.T) {
	original := home.Version
	t.Cleanup(func() { home.Version = original })
	// No release version to download from, so a checksum that fails can only
	// end in the missing-install error, never in a silent download.
	home.Version = "(devel)"

	files := fakeDictionaries(t, "words\n", "dict\n")
	homeDir := filepath.Join(t.TempDir(), DictionaryDir)
	shippedDir := filepath.Join(t.TempDir(), DictionaryDir)
	writeDictionaryFile(t, filepath.Join(homeDir, filepath.FromSlash(files[0].Name)), "tampered\n")

	// No usable copy anywhere and no release to download from: the call reports
	// the missing install instead of reading undeclared bytes.
	if _, err := resolveDictionaryFile(homeDir, shippedDir, files[0].Name, files); err == nil {
		t.Fatal("a copy that fails its declared checksum was adopted")
	}

	// A home copy whose bytes do match the declaration is adopted, and its path
	// is what a search reads from.
	writeDictionaryFile(t, filepath.Join(homeDir, filepath.FromSlash(files[0].Name)), "words\n")
	got, err := resolveDictionaryFile(homeDir, shippedDir, files[0].Name, files)
	if err != nil || got == "" {
		t.Fatalf("a declared home copy was refused: %q %v", got, err)
	}
	if want := filepath.Join(homeDir, filepath.FromSlash(files[0].Name)); got != want {
		t.Fatalf("read from %s, want the home copy %s", got, want)
	}
}

// Seeding must never be a requirement: a home that cannot be written still
// searches from the copy beside the binary.
func TestDictionaryResolutionReadsTheShippedCopyWhenTheHomeIsUnwritable(t *testing.T) {
	files := fakeDictionaries(t, "words\n", "dict\n")
	// A regular file where the home's dict directory would be: the seeding
	// write cannot create anything under it, so the write fails.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	homeDir := filepath.Join(blocker, DictionaryDir)
	shippedDir := filepath.Join(t.TempDir(), DictionaryDir)
	writeDictionaryFile(t, filepath.Join(shippedDir, filepath.FromSlash(files[0].Name)), "words\n")

	got, err := resolveDictionaryFile(homeDir, shippedDir, files[0].Name, files)
	if err != nil {
		t.Fatalf("an unwritable home must still read the shipped copy: %v", err)
	}
	want := filepath.Join(shippedDir, filepath.FromSlash(files[0].Name))
	if got != want {
		t.Fatalf("read from %s, want the shipped copy %s", got, want)
	}
}

// seedFixture stands up the fake dictionaries beside the binary and returns the
// two directories the seed acts on, plus each file's content.
func seedFixture(t *testing.T, shipped, installed bool) (dictDir, shippedDir string, contents map[string]string) {
	t.Helper()
	files := fakeDictionaries(t, "words\n", "dict\n")
	contents = map[string]string{files[0].Name: "words\n", files[1].Name: "dict\n"}
	dictDir = filepath.Join(t.TempDir(), DictionaryDir)
	shippedDir = filepath.Join(t.TempDir(), DictionaryDir)
	if shipped {
		for name, body := range contents {
			writeDictionaryFile(t, filepath.Join(shippedDir, filepath.FromSlash(name)), body)
		}
	}
	if installed {
		for name, body := range contents {
			writeDictionaryFile(t, filepath.Join(dictDir, filepath.FromSlash(name)), body)
		}
	}
	return dictDir, shippedDir, contents
}

func seededFiles(t *testing.T, dir string, contents map[string]string) {
	t.Helper()
	for name, body := range contents {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("%s was not seeded: %v", name, err)
		}
		if string(got) != body {
			t.Errorf("%s seeded %q, want %q", name, got, body)
		}
	}
}

// The startup seed fills the home from the copy beside the binary and stays
// offline: "(devel)" has no release to download from, so a nil error proves the
// seed never reached for the network.
func TestDictionarySeedFromTheShippedCopyStaysOffline(t *testing.T) {
	dictDir, shippedDir, contents := seedFixture(t, true, false)
	if err := seedDictionaries(dictDir, shippedDir, "(devel)", fakeDictionaries(t, "words\n", "dict\n")); err != nil {
		t.Fatal(err)
	}
	seededFiles(t, dictDir, contents)
}

// A home copy that already matches is left exactly as it was — not rewritten
// under a running reader. The old modification time is the proof.
func TestDictionarySeedKeepsAnAlreadyMatchingHomeCopy(t *testing.T) {
	dictDir, shippedDir, contents := seedFixture(t, false, true)
	past := time.Unix(1_000_000, 0)
	for name := range contents {
		path := filepath.Join(dictDir, filepath.FromSlash(name))
		if err := os.Chtimes(path, past, past); err != nil {
			t.Fatal(err)
		}
	}
	if err := seedDictionaries(dictDir, shippedDir, "(devel)", fakeDictionaries(t, "words\n", "dict\n")); err != nil {
		t.Fatal(err)
	}
	for name := range contents {
		info, err := os.Stat(filepath.Join(dictDir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(past) {
			t.Errorf("%s was rewritten even though it already matched", name)
		}
	}
}

// A home copy whose bytes do not match is replaced by the copy beside the
// binary, so the index and the query cannot end up cut differently.
func TestDictionarySeedReplacesAMismatchedHomeCopy(t *testing.T) {
	files := fakeDictionaries(t, "words\n", "dict\n")
	dictDir, shippedDir, contents := seedFixture(t, true, false)
	writeDictionaryFile(t, filepath.Join(dictDir, filepath.FromSlash(files[0].Name)), "tampered\n")
	if err := seedDictionaries(dictDir, shippedDir, "(devel)", files); err != nil {
		t.Fatal(err)
	}
	seededFiles(t, dictDir, contents)
}

// With no copy beside the binary and no release to download from, the seed
// reports the product command that fixes it and leaves no half-written home.
func TestDictionarySeedWithoutASourceReportsTheInstallCommand(t *testing.T) {
	files := fakeDictionaries(t, "words\n", "dict\n")
	dictDir := filepath.Join(t.TempDir(), DictionaryDir)
	shippedDir := filepath.Join(t.TempDir(), DictionaryDir)
	err := seedDictionaries(dictDir, shippedDir, "(devel)", files)
	if err == nil {
		t.Fatal("a seed with no source at all succeeded")
	}
	if !strings.Contains(err.Error(), "forebrain assets install") {
		t.Errorf("error %v does not name the install command", err)
	}
	if entries, listErr := os.ReadDir(dictDir); listErr == nil && len(entries) > 0 {
		t.Errorf("a failed seed left files in %s: %v", dictDir, entries)
	}
}

// Seed is the startup entry point: with the real dictionaries installed beside
// the test binary (TestMain does that), a fresh home ends up holding them all.
func TestSeedInstallsTheDeclaredDictionariesIntoTheHome(t *testing.T) {
	home := t.TempDir()
	if err := Seed(home, "(devel)"); err != nil {
		t.Fatal(err)
	}
	for _, file := range DictionaryFiles {
		if _, err := os.Stat(filepath.Join(home, DictionaryDir, filepath.FromSlash(file.Name))); err != nil {
			t.Errorf("%s was not seeded into the home: %v", file.Name, err)
		}
	}
}
