package llm

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The runtime asset files that ship beside the forebrain binary rather than
// being compiled into it.
//
// models.json — the built-in model catalog — is a data file, so like the
// word-segmentation dictionaries memory search reads it is not embedded: every
// build that produces a binary someone runs installs it beside the binary, and
// the runtime reads it from there. A build that ships no assets beside the
// binary downloads models.json from that version's GitHub release on first
// need.
// Plan 041 adds the tokenizer files here; assetEntries is the one list of
// them, and the installer, the release packaging and the runtime read all
// follow it, so they cannot drift apart.

// AssetsDirEnv names an environment variable that overrides where the runtime
// assets are read from. Tests set it to the repository's assets/ directory,
// and a developer running from source can too:
//
//	FOREBRAIN_ASSETS_DIR=$PWD/assets go run ./cmd/forebrain
const AssetsDirEnv = "FOREBRAIN_ASSETS_DIR"

// assetEntries lists every file (or directory) InstallAssets places beside the
// binary and the runtime reads from there. "tokenizers" arrived with plan 041;
// "fonts" is the PDF renderer's CJK fallback set, seeded into $FOREBRAIN_HOME/
// fonts by pkg/process at startup (scripts/fetch-pdfium-fonts.sh fetches it,
// assets/fonts/README.md records its source and licence).
var assetEntries = []string{"models.json", "tokenizers", "fonts"}

// AssetsDir is the directory holding the runtime assets beside the running
// binary. FOREBRAIN_ASSETS_DIR overrides it; otherwise the assets live next to
// the executable, resolved through symlinks so a linked launcher (the npm bin
// shim, /usr/local/bin) finds the directory holding the real binary.
func AssetsDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv(AssetsDirEnv)); dir != "" {
		return dir, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	return filepath.Dir(executable), nil
}

// InstallAssets copies every assetEntries entry from srcDir — the repository's
// assets/ directory — beside the binary in binDir. A directory entry is copied
// recursively. Existing destinations are overwritten: every build installs the
// current version's assets. A missing source is an error, so a build that
// forgot to add an entry fails here instead of shipping a binary whose runtime
// asset is absent.
func InstallAssets(srcDir, binDir string) error {
	for _, name := range assetEntries {
		src := filepath.Join(srcDir, name)
		dst := filepath.Join(binDir, name)
		if err := copyAsset(src, dst); err != nil {
			return fmt.Errorf("llm: install assets: %w", err)
		}
	}
	return nil
}

// copyAsset copies src — a file or a directory, recursively — to dst,
// replacing whatever is there.
func copyAsset(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst, info.Mode().Perm())
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := copyAsset(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// copyFile writes src's bytes to dst (creating parent directories), with the
// given file mode.
func copyFile(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, mode)
}

// RepoAssetsDirForTest returns the repository's assets/ directory, found by
// walking up from the working directory to the module root (the directory
// holding go.mod). It is only for tests run from inside the repository; a
// build that installs real assets uses AssetsDir and InstallAssets.
func RepoAssetsDirForTest() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "assets"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("llm: repository root (go.mod) not found")
		}
		dir = parent
	}
}

// RepoModelsJSONForTest reads the repository's assets/models.json — the same
// catalog the build ships — so a test can parse it without a home directory or
// the global catalog.
func RepoModelsJSONForTest() ([]byte, error) {
	dir, err := RepoAssetsDirForTest()
	if err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(dir, "models.json"))
}

// modelsReleaseVersion matches the versions that have a GitHub release to
// download an asset from: strict vX.Y.Z tags. A pseudo-version, a prerelease,
// a "+dirty" build or a checkout build ("(devel)") has no release, so it must
// not try.
var modelsReleaseVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// modelsJSONURLTemplate is where a release's models.json is downloaded from.
// It is a variable, not a constant, so a test can point it at an httptest
// server.
var modelsJSONURLTemplate = "https://raw.githubusercontent.com/forebrain-harness/forebrain-harness/%s/assets/models.json"

// modelsJSONTimeout bounds one whole download of models.json. The asset is a
// few hundred kilobytes, so half a minute is already generous; what matters is
// that an install which cannot reach the release page — an offline intranet —
// fails within a bounded time instead of holding startup for the two minutes
// the default client allowed. It is a variable so a test can prove that path
// without waiting for the real bound.
var modelsJSONTimeout = 30 * time.Second

// downloadDialTimeout bounds the connection attempt of every automatic
// download. A firewall that drops packets silently leaves a dial waiting for
// whatever the client's overall timeout is, and that wait is what a start or a
// conversation must never pay: the connection is the step that has to fail
// fast, while the transfer itself keeps the longer bound.
const downloadDialTimeout = 5 * time.Second

// downloadClient builds the client every automatic download uses: a short
// connect timeout, the caller's overall bound, and the process's proxy settings
// (a developer reaching GitHub through a proxy, an intranet reaching a mirror
// directly).
func downloadClient(overall time.Duration) *http.Client {
	return &http.Client{
		Timeout: overall,
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			DialContext:         (&net.Dialer{Timeout: downloadDialTimeout, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout: downloadDialTimeout,
		},
	}
}

// Model-data failures a surface may show: one sentence each, no machine layout
// and no script name (owner 2026-11-07: the person meets model data, not
// models.json, and is not expected to arrange it by hand).
var (
	errModelDataMissing  = errors.New("model data is missing beside the binary; reinstall forebrain")
	errModelDataNotFound = errors.New("model data not found; reinstall forebrain")
)

// sourceModelsJSON returns the bytes to seed $FOREBRAIN_HOME/models.json with:
// the catalog beside the binary when it is there, otherwise a download from
// this version's release. A build that is not a release version has no release
// to download from — its catalog comes from the install script instead — so a
// missing asset is reported with the command that fixes it.
func sourceModelsJSON(version string) ([]byte, error) {
	dir, err := AssetsDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "models.json")
	data, err := os.ReadFile(path)
	if err == nil {
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("modelcatalog: read %q: %w", path, err)
	}
	if !modelsReleaseVersion.MatchString(version) {
		// The path and the install script are for whoever is debugging this
		// build; the error is what a surface may show, so it says what the
		// person can do and nothing about this machine's layout.
		slog.Debug("model data is missing beside the binary", "dir", dir, "hint", "scripts/install-assets.sh "+dir)
		return nil, errModelDataMissing
	}
	return downloadModelsJSON(version)
}

// downloadModelsJSON fetches version's models.json from its GitHub release.
// The attempt is made at most once per process and URL: two startup steps
// install the catalog, and repeating a download that just failed against the
// same unreachable release page can only add a second wait to a start that is
// already degraded.
func downloadModelsJSON(version string) ([]byte, error) {
	url := fmt.Sprintf(modelsJSONURLTemplate, version)
	if err := modelsJSONFailure(url); err != nil {
		return nil, err
	}
	client := downloadClient(modelsJSONTimeout)
	response, err := client.Get(url)
	if err != nil {
		return nil, rememberModelsJSONFailure(url, fmt.Errorf("modelcatalog: download %s: %w", url, err))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, rememberModelsJSONFailure(url, fmt.Errorf("modelcatalog: download %s: %s", url, response.Status))
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return nil, rememberModelsJSONFailure(url, fmt.Errorf("modelcatalog: download %s: %w", url, err))
	}
	return data, nil
}

// modelsJSONFailures records the downloads this process has already given up
// on and the error each one reported, so a later caller is answered from the
// first attempt instead of paying for another one.
var modelsJSONFailures struct {
	mu   sync.Mutex
	errs map[string]error
}

// modelsJSONFailure returns the error the process already got for url, or nil
// when that download has not been tried yet.
func modelsJSONFailure(url string) error {
	modelsJSONFailures.mu.Lock()
	defer modelsJSONFailures.mu.Unlock()
	return modelsJSONFailures.errs[url]
}

// rememberModelsJSONFailure records err for url and returns it.
func rememberModelsJSONFailure(url string, err error) error {
	modelsJSONFailures.mu.Lock()
	defer modelsJSONFailures.mu.Unlock()
	if modelsJSONFailures.errs == nil {
		modelsJSONFailures.errs = map[string]error{}
	}
	modelsJSONFailures.errs[url] = err
	return err
}

// writeCatalogAtomic writes data to path through a temporary file in the same
// directory, so a crash never leaves a half-written catalog and a reader sees
// either the old file or the complete new one.
func writeCatalogAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".models.json-*")
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
