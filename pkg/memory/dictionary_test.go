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
)

// TestDictionaryFilesDeclareTheBytesTheirModulesShip guards the compiled
// checksums against dependency drift: when an upgrade of a dictionary's module
// changes its bytes, the checksum in DictionaryFiles must be recomputed with
// it, or a `go install` binary would refuse its own release bundle.
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
