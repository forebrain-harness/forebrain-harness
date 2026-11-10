package llm

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The runtime asset files that ship beside the forebrain binary rather than
// being compiled into it.
//
// models.json — the built-in model catalog — is a data file, so like the
// word-segmentation dictionaries memory search reads it is not embedded: every
// build that produces a binary someone runs installs it beside the binary, and
// the runtime reads it from there. That copy is the only catalog the runtime
// reads: nothing downloads it, nothing copies it into a home (owner
// 2026-11-09), and a build that ships no assets has no catalog data.
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

// downloadDialTimeout bounds the connection attempt of every automatic
// download. A firewall that drops packets silently leaves a dial waiting for
// whatever the client's overall timeout is, and that wait is what a start or a
// conversation must never pay: the connection is the step that has to fail
// fast, while the transfer itself keeps the longer bound.
const downloadDialTimeout = 5 * time.Second

// downloadClient builds the client an automatic download uses — the tokenizer
// files an estimator fetches when it needs them: a short connect timeout, the
// caller's overall bound, and the process's proxy settings (a developer reaching
// a hub through a proxy, an intranet reaching a mirror directly).
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
