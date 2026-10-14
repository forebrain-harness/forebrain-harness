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
	"log/slog"
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

// dictionaryHomeDir is the home-level dictionary directory, <FOREBRAIN_HOME>/dict:
// the same name a binary's own copy uses, and the location every forebrain
// sharing that home reads.
func dictionaryHomeDir() (string, error) {
	root, err := home.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, DictionaryDir), nil
}

// dictionaryShippedDir is the dictionary directory beside the running binary,
// which is where a packaged install puts it — make build, the npm packages, the
// container image, the test drivers. It is read through symlinks so a linked
// launcher (the npm bin shim, /usr/local/bin) finds the directory holding the
// real binary.
func dictionaryShippedDir() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(executable), DictionaryDir), nil
}

// dictionaryPath is where the named dictionary file is read from. The home-level
// copy is read first — seeded from the copy beside the binary, so one install
// serves every forebrain sharing the home — then the copy beside the binary,
// then this version's release bundle. A binary that came with dictionaries
// therefore searches out of the box; one that did not (a `go install` build)
// downloads them on this first need.
func dictionaryPath(name string) (string, error) {
	homeDir, err := dictionaryHomeDir()
	if err != nil {
		return "", err
	}
	shippedDir, err := dictionaryShippedDir()
	if err != nil {
		return "", err
	}
	return resolveDictionaryFile(homeDir, shippedDir, name, DictionaryFiles)
}

// resolveDictionaryFile returns the path of one dictionary file to read from.
// The home copy wins when it holds the bytes this binary declares. Otherwise
// the copy beside the binary is used, and the home is seeded with it so every
// binary sharing that home reads one install — Seed, called at startup,
// normally does this first; this is the fallback for a home that appeared
// later. A home that cannot be written is not a failure: the shipped copy is
// read where it stands, because an install that came with its dictionaries must
// search out of the box. Only when neither holds the declared bytes is this
// version's release bundle downloaded. A copy that does not match the declared
// checksum is never read: the index and the query must be cut by the bytes this
// binary declared, or the two sides spell terms differently.
func resolveDictionaryFile(homeDir, shippedDir, name string, files []DictionaryFile) (string, error) {
	installed := filepath.Join(homeDir, filepath.FromSlash(name))
	if dictionaryBytesMatch(installed, name, files) {
		return installed, nil
	}
	shipped := filepath.Join(shippedDir, filepath.FromSlash(name))
	if dictionaryBytesMatch(shipped, name, files) {
		if err := seedDictionaryFile(homeDir, shipped, name); err != nil {
			slog.Warn("memory search dictionary: keep reading the copy beside the binary",
				"source", shipped, "err", err)
			return shipped, nil
		}
		return installed, nil
	}
	if err := installMissingDictionaries(homeDir); err != nil {
		return "", err
	}
	if !dictionaryBytesMatch(installed, name, files) {
		return "", fmt.Errorf("memory search dictionaries are missing from %s", homeDir)
	}
	return installed, nil
}

// dictionaryBytesMatch reports whether path holds exactly the bytes this binary
// declares for the named dictionary file — the one rule by which any copy, from
// any of the three places, is adopted. A copy that fails the compiled-in
// checksum is never read.
func dictionaryBytesMatch(path, name string, files []DictionaryFile) bool {
	declared, known := DictionaryFile{}, false
	for _, file := range files {
		if file.Name == name {
			declared, known = file, true
			break
		}
	}
	if !known {
		return false
	}
	source, err := os.Open(path)
	if err != nil {
		return false
	}
	defer source.Close()
	checksum := sha256.New()
	if _, err := io.Copy(checksum, source); err != nil {
		return false
	}
	return hex.EncodeToString(checksum.Sum(nil)) == declared.SHA256
}

// dictionarySetMatches reports whether dir holds the declared bytes of every
// file: the condition under which a seed or a download can be skipped.
func dictionarySetMatches(dir string, files []DictionaryFile) bool {
	for _, file := range files {
		if !dictionaryBytesMatch(filepath.Join(dir, filepath.FromSlash(file.Name)), file.Name, files) {
			return false
		}
	}
	return true
}

// CheckDictionaryFile reports whether path holds the bytes this binary declares
// for the named dictionary file. The packaging step runs it so a package missing
// a dictionary — or carrying a stale one — is rejected at build time rather than
// failing the user's first memory search. name must be one of DictionaryFiles.
func CheckDictionaryFile(path, name string) error {
	if dictionaryBytesMatch(path, name, DictionaryFiles) {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("dictionary %s is missing", path)
	}
	return fmt.Errorf("dictionary %s does not match the checksum this binary declares", path)
}

// seedDictionaryFile copies one dictionary file from source into homeDir under
// the same name, through a temporary file in the destination directory so a
// crash never leaves a half-written dictionary and a reader sees either the old
// file or the complete new one. It is how a copy beside the binary — or a
// developer's assets directory — becomes the home-level copy every binary
// sharing the home reads.
func seedDictionaryFile(homeDir, source, name string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	target := filepath.Join(homeDir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return writeDictionaryAtomic(target, data)
}

// writeDictionaryAtomic writes data to path through a temporary file in the
// same directory, so a crash never leaves a half-written file.
func writeDictionaryAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dict-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// dictionariesMissingError is the message for a build that found the declared
// bytes in none of the places a search reads and cannot download them either. It
// names both places it looked and the product command that puts a copy in them.
func dictionariesMissingError(homeDir, shippedDir string) error {
	return fmt.Errorf(
		"memory search dictionaries are missing from %s and beside %s; install them with `forebrain assets install`",
		homeDir, shippedDir)
}

// dictionaryInstallOnce makes the process attempt the download at most once:
// the dictionaries are tens of megabytes, and a failed attempt must not repeat
// on every search.
var (
	dictionaryInstallOnce sync.Once
	dictionaryInstallErr  error
)

// installMissingDictionaries downloads this version's dictionary bundle into
// dictDir, the home-level dictionary directory. A build that is not a release
// version has no release to download from — its message names the product
// command that puts a copy in place.
func installMissingDictionaries(dictDir string) error {
	dictionaryInstallOnce.Do(func() {
		url, ok := dictionaryBundleURL(home.Version)
		if !ok {
			shippedDir, _ := dictionaryShippedDir()
			dictionaryInstallErr = dictionariesMissingError(dictDir, shippedDir)
			return
		}
		client := &http.Client{Timeout: 5 * time.Minute}
		dictionaryInstallErr = downloadDictionaries(context.Background(), client, url, dictDir, DictionaryFiles)
	})
	return dictionaryInstallErr
}

// Seed ensures $FOREBRAIN_HOME/dict holds every dictionary file this binary
// declares, so every forebrain sharing that home searches Chinese and Japanese
// text — including binaries that ship no dictionaries of their own. The bytes
// come from the copy beside this binary when it matches the declared checksums
// (the packaged installs: make build, the npm packages, the container image),
// and only otherwise from this version's release bundle. A home copy that
// already matches is left alone; a home copy that does not is replaced. A build
// that is not a release version and has no usable copy beside it reports the
// install that fixes it instead of downloading. Errors are non-fatal — callers
// should log a warning and continue.
func Seed(forebrainHome, version string) error {
	if strings.TrimSpace(forebrainHome) == "" {
		return fmt.Errorf("memory search dictionary seed: empty forebrain home")
	}
	shippedDir, err := dictionaryShippedDir()
	if err != nil {
		return err
	}
	return seedDictionaries(filepath.Join(forebrainHome, DictionaryDir), shippedDir, version, DictionaryFiles)
}

// seedDictionaries fills dictDir with every file this binary declares, taking
// the bytes from the copy beside the binary (shippedDir) when it holds them and
// only otherwise from this version's release bundle. A home copy that already
// matches is left alone. It never reaches the network while shippedDir holds
// every declared file, so a packaged install seeds its home offline.
func seedDictionaries(dictDir, shippedDir, version string, files []DictionaryFile) error {
	if dictionarySetMatches(dictDir, files) {
		return nil
	}
	if dictionarySetMatches(shippedDir, files) {
		for _, file := range files {
			installed := filepath.Join(dictDir, filepath.FromSlash(file.Name))
			if dictionaryBytesMatch(installed, file.Name, files) {
				continue
			}
			source := filepath.Join(shippedDir, filepath.FromSlash(file.Name))
			if err := seedDictionaryFile(dictDir, source, file.Name); err != nil {
				return err
			}
		}
		return nil
	}
	url, ok := dictionaryBundleURL(version)
	if !ok {
		return dictionariesMissingError(dictDir, shippedDir)
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	return downloadDictionaries(context.Background(), client, url, dictDir, files)
}

// DictionaryStatus is one declared dictionary file and where this binary reads
// it from. Err is set when the file could not be resolved at all.
type DictionaryStatus struct {
	File DictionaryFile
	Path string
	Err  error
}

// DictionariesStatus resolves every declared dictionary file for forebrainHome,
// reading exactly as a search does — the home copy first, then sourceDir (the
// copy beside the binary for the CLI, or a developer's FOREBRAIN_ASSETS_DIR),
// then this version's release bundle — and reports each file's read path. It is
// for the `assets` command: because resolving seeds the home and may download,
// asking for status is also a repair. One entry per declared file is returned,
// in DictionaryFiles order, so a caller can report each and fail only if some
// could not be resolved.
func DictionariesStatus(forebrainHome, sourceDir string) []DictionaryStatus {
	homeDir := filepath.Join(forebrainHome, DictionaryDir)
	statuses := make([]DictionaryStatus, 0, len(DictionaryFiles))
	for _, file := range DictionaryFiles {
		path, err := resolveDictionaryFile(homeDir, sourceDir, file.Name, DictionaryFiles)
		statuses = append(statuses, DictionaryStatus{File: file, Path: path, Err: err})
	}
	return statuses
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
