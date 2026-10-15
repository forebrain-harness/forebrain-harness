package pdf

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	pdfium "github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/single_threaded"
)

// FontDirsEnv overrides the directories handed to PDFium's font loader. It is
// a list separated by the platform's path-list separator (":" on unix). A
// developer running from source can point it at the repository's fonts:
//
//	FOREBRAIN_PDF_FONT_DIRS=$PWD/assets/fonts go run ./cmd/forebrain
const FontDirsEnv = "FOREBRAIN_PDF_FONT_DIRS"

var (
	poolOnce sync.Once
	pool     pdfium.Pool
)

// renderPool initialises the process-wide PDFium pool on first use. PDFium is
// initialised exactly once per process — the font paths are fixed at that
// moment — so the directories from the first call win; a later call with a
// different set is a no-op. The native library is released by the OS at exit;
// there is no orderly Close, matching the tokenizer runtime.
func renderPool(dirs []string) pdfium.Pool {
	poolOnce.Do(func() {
		var config single_threaded.Config
		if len(dirs) > 0 {
			config.LibraryConfig = &pdfium.LibraryConfig{UserFontPaths: dirs}
		}
		pool = single_threaded.Init(config)
	})
	return pool
}

// FontDirs returns the directories to hand PDFium's fallback-font loader for a
// process whose home directory is home.
//
// Resolution order (see fontDirs): FOREBRAIN_PDF_FONT_DIRS, then
// $home/fonts (where pkg/process seeds this repository's Noto CJK fonts), then
// the fonts directory beside the binary — llm.AssetsDir, the one location
// every packaged asset lives in (FOREBRAIN_ASSETS_DIR overrides it), so a
// packaged install's fonts are found. When none of those is present it returns
// nil, so PDFium keeps its platform defaults. When one is present the platform
// font directories are appended on Linux, because setting UserFontPaths
// replaces PDFium's default font scan — without them a document that relies on
// a system font (any Latin text, not just CJK) would break.
func FontDirs(home string) []string {
	assets, err := llm.AssetsDir()
	if err != nil {
		assets = ""
	}
	return fontDirs(runtime.GOOS, home, os.Getenv, joinFonts(assets))
}

// joinFonts is the fonts subdirectory of the assets dir, or "" when there is
// no assets dir to name (ownFontDirs treats "" as "no candidate").
func joinFonts(assets string) string {
	if assets == "" {
		return ""
	}
	return filepath.Join(assets, "fonts")
}

func fontDirs(goos, home string, getenv func(string) string, assetsFonts string) []string {
	ours := ownFontDirs(home, getenv, assetsFonts)
	if len(ours) == 0 {
		return nil
	}
	return append(ours, platformFontDirs(goos, getenv)...)
}

// ownFontDirs returns this installation's own font directories, or nil.
func ownFontDirs(home string, getenv func(string) string, assetsFonts string) []string {
	if env := strings.TrimSpace(getenv(FontDirsEnv)); env != "" {
		var dirs []string
		for _, d := range strings.Split(env, string(os.PathListSeparator)) {
			if d = strings.TrimSpace(d); d != "" {
				dirs = append(dirs, d)
			}
		}
		return dirs
	}
	if home = strings.TrimSpace(home); home != "" {
		if d := filepath.Join(home, "fonts"); isDir(d) {
			return []string{d}
		}
	}
	if assetsFonts != "" && isDir(assetsFonts) {
		return []string{assetsFonts}
	}
	return nil
}

// platformFontDirs returns the platform's own font directories, if any apply.
// macOS and Windows search their font system when UserFontPaths is ignored, so
// only Linux needs its directories named.
func platformFontDirs(goos string, getenv func(string) string) []string {
	if goos != "linux" {
		return nil
	}
	var dirs []string
	for _, d := range []string{"/usr/share/fonts", "/usr/local/share/fonts"} {
		if isDir(d) {
			dirs = append(dirs, d)
		}
	}
	data := getenv("XDG_DATA_HOME")
	if data == "" {
		if home := getenv("HOME"); home != "" {
			data = filepath.Join(home, ".local", "share")
		}
	}
	if data != "" {
		if d := filepath.Join(data, "fonts"); isDir(d) {
			dirs = append(dirs, d)
		}
	}
	if home := getenv("HOME"); home != "" {
		if d := filepath.Join(home, ".fonts"); isDir(d) {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
