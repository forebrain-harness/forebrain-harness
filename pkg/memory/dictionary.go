package memory

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
)

// The word-segmentation dictionaries memory search reads.
//
// They are tens of megabytes of word lists, so they are not compiled into the
// binary: every package that installs forebrain puts them in DictionaryDir beside
// it, and they are read from there. A `go install` build ships no dictionaries,
// so the first need downloads them from that version's GitHub release.
// DictionaryFiles is the one list of them — what the search reads, what the
// release installs and what the download verifies are the same entries, so the
// three cannot drift apart.

// DictionaryDir is the directory, beside the forebrain binary, that holds the
// dictionaries.
const DictionaryDir = "dict"

// DictionaryFile is one dictionary file: where it lives under DictionaryDir,
// the Go module, at the version go.mod selects, whose file it is, and the
// SHA256 of those bytes. The checksum is compiled in — not read from the
// release that also ships the file — so a tampered release asset cannot
// install dictionaries this binary never declared.
type DictionaryFile struct {
	Name   string
	Module string
	Source string
	SHA256 string
}

const (
	chineseSimplifiedWords  = "zh/s_1.txt"
	chineseTraditionalWords = "zh/t_1.txt"
	chineseStopWords        = "zh/stop_tokens.txt"
	japaneseDictionary      = "ja/ipa.dict"
)

// DictionaryFiles lists every dictionary file: the simplified and traditional
// Chinese word lists and the Chinese stop words, and the Japanese morphological
// dictionary (IPADIC).
var DictionaryFiles = []DictionaryFile{
	{Name: chineseSimplifiedWords, Module: "github.com/go-ego/gse", Source: "data/dict/zh/s_1.txt", SHA256: "2b3063ec552327520bee3c0c5819d6e131ab3db50a60b94641ec90f611c24bcd"},
	{Name: chineseTraditionalWords, Module: "github.com/go-ego/gse", Source: "data/dict/zh/t_1.txt", SHA256: "2c84cef353d2daac62cc62bbeabab6b6a8866cfee8f9f88901e00ed66ed208c6"},
	{Name: chineseStopWords, Module: "github.com/go-ego/gse", Source: "data/dict/zh/stop_tokens.txt", SHA256: "8a05af1a224e40d06fce2081ad4d4b2c5e5c902f0a7501c0dba677ce1ee40c90"},
	{Name: japaneseDictionary, Module: "github.com/ikawaha/kagome-dict/ipa", Source: "ipa.dict", SHA256: "19bacdfe1a33ba50054d77192f062f1696089a48a9d8e20863b716ff4af6adf3"},
}

// dictionaryBundle is the release asset holding every DictionaryFiles entry,
// laid out as under DictionaryDir.
const dictionaryBundle = "forebrain-dict.tar.gz"

const releaseDownloadURL = "https://github.com/forebrain-harness/forebrain-harness/releases/download"

// releaseVersion matches the versions that have a release to download from:
// strict vX.Y.Z tags. A pseudo-version, a prerelease, a "+dirty" build or a
// checkout build ("(devel)") has no release, so it must not try.
var releaseVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// dictionaryBundleURL is where this version's dictionary bundle is downloaded
// from, and false when version is not a release version.
func dictionaryBundleURL(version string) (string, bool) {
	if !releaseVersion.MatchString(version) {
		return "", false
	}
	return releaseDownloadURL + "/" + version + "/" + dictionaryBundle, true
}

// dictionaryPath is where the named dictionary file is read from: under
// DictionaryDir, beside the binary that is running. A binary that did not come
// with dictionaries — one installed with `go install` — downloads them on this
// first need.
func dictionaryPath(name string) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	dictDir := filepath.Join(filepath.Dir(executable), DictionaryDir)
	dictionary := filepath.Join(dictDir, filepath.FromSlash(name))
	if _, err := os.Stat(dictionary); err == nil {
		return dictionary, nil
	}
	if err := installMissingDictionaries(dictDir); err != nil {
		return "", err
	}
	return dictionary, nil
}

// dictionaryInstallOnce makes the process attempt the download at most once:
// the dictionaries are tens of megabytes, and a failed attempt must not repeat
// on every search.
var (
	dictionaryInstallOnce sync.Once
	dictionaryInstallErr  error
)

// installMissingDictionaries downloads this version's dictionary bundle into
// dictDir. A build that is not a release version has no release to download
// from — its dictionaries come from the install script instead.
func installMissingDictionaries(dictDir string) error {
	dictionaryInstallOnce.Do(func() {
		url, ok := dictionaryBundleURL(home.Version)
		if !ok {
			dictionaryInstallErr = fmt.Errorf("memory search dictionaries are missing from %s; install them with scripts/install-dictionary.sh %s", dictDir, filepath.Dir(dictDir))
			return
		}
		client := &http.Client{Timeout: 5 * time.Minute}
		dictionaryInstallErr = downloadDictionaries(context.Background(), client, url, dictDir, DictionaryFiles)
	})
	return dictionaryInstallErr
}

// downloadDictionaries downloads the dictionary bundle at url, checks every
// file in it against the checksum compiled into files, and installs them under
// dictDir. The bundle is unpacked into a temporary directory beside dictDir —
// the same filesystem, so the final renames are atomic — and only after every
// checksum has verified, so a failed or tampered download leaves dictDir
// exactly as it was.
func downloadDictionaries(ctx context.Context, client *http.Client, url, dictDir string, files []DictionaryFile) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("download memory search dictionaries from %s: %w", url, err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download memory search dictionaries from %s: %w", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, response.Status)
	}
	compressed, err := gzip.NewReader(io.LimitReader(response.Body, 128<<20))
	if err != nil {
		return fmt.Errorf("download memory search dictionaries from %s: %w", url, err)
	}
	defer compressed.Close()
	unpacked, err := os.MkdirTemp(filepath.Dir(dictDir), "dict-download-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(unpacked)
	if err := extractDictionaries(tar.NewReader(compressed), unpacked, files); err != nil {
		return err
	}
	for _, file := range files {
		installed := filepath.Join(dictDir, filepath.FromSlash(file.Name))
		if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(unpacked, filepath.FromSlash(file.Name)), installed); err != nil {
			return err
		}
	}
	return nil
}

// extractDictionaries unpacks the regular-file entries of a dictionary bundle
// into dir, writing only the entries files declares, and requires every
// declared entry to be present. An entry whose cleaned name is not declared is
// refused — which also stops "../" paths from escaping dir.
func extractDictionaries(archive *tar.Reader, dir string, files []DictionaryFile) error {
	declared := make(map[string]DictionaryFile, len(files))
	for _, file := range files {
		declared[file.Name] = file
	}
	seen := make(map[string]bool, len(files))
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		file, known := declared[path.Clean(strings.TrimPrefix(header.Name, "./"))]
		if !known {
			return fmt.Errorf("unexpected file %q in %s", header.Name, dictionaryBundle)
		}
		unpacked := filepath.Join(dir, filepath.FromSlash(file.Name))
		if err := os.MkdirAll(filepath.Dir(unpacked), 0o755); err != nil {
			return err
		}
		if err := writeVerifiedEntry(archive, unpacked, file); err != nil {
			return err
		}
		seen[file.Name] = true
	}
	for _, file := range files {
		if !seen[file.Name] {
			return fmt.Errorf("%s is missing from %s", file.Name, dictionaryBundle)
		}
	}
	return nil
}

// writeVerifiedEntry copies one bundle entry into path, hashing as it goes,
// and refuses it when the bytes do not match the declared checksum.
func writeVerifiedEntry(archive *tar.Reader, path string, file DictionaryFile) error {
	unpacked, err := os.Create(path)
	if err != nil {
		return err
	}
	checksum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(unpacked, checksum), archive); err != nil {
		unpacked.Close()
		return err
	}
	if err := unpacked.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(checksum.Sum(nil)); got != file.SHA256 {
		return fmt.Errorf("%s does not match its published checksum", file.Name)
	}
	return nil
}

// InstallDictionary copies every dictionary file into DictionaryDir under
// binDir, the directory that holds a forebrain binary. It is how the release and
// the test binaries alike get their dictionaries, and it runs the go command in
// the forebrain module, which knows where each dictionary's module is.
func InstallDictionary(binDir string) error {
	dirs := map[string]string{}
	for _, file := range DictionaryFiles {
		dir, known := dirs[file.Module]
		if !known {
			var err error
			if dir, err = moduleDir(file.Module); err != nil {
				return err
			}
			dirs[file.Module] = dir
		}
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file.Source)))
		if err != nil {
			return err
		}
		target := filepath.Join(binDir, DictionaryDir, filepath.FromSlash(file.Name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// moduleDir downloads module at the version the forebrain module selects and
// returns the directory it was unpacked into.
func moduleDir(module string) (string, error) {
	out, err := exec.Command("go", "mod", "download", "-json", module).Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("go mod download %s: %w: %s", module, err, exit.Stderr)
		}
		return "", fmt.Errorf("go mod download %s: %w", module, err)
	}
	var download struct{ Dir, Error string }
	if err := json.Unmarshal(out, &download); err != nil {
		return "", fmt.Errorf("go mod download %s: %w", module, err)
	}
	if download.Error != "" {
		return "", fmt.Errorf("go mod download %s: %s", module, download.Error)
	}
	return download.Dir, nil
}
