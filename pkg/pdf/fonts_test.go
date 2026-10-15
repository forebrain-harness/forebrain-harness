package pdf

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFontDirsResolutionOrder pins the fallback order the CJK tofu fix
// depends on: FOREBRAIN_PDF_FONT_DIRS wins, then $home/fonts, then the fonts
// directory beside the binary. Each candidate must actually exist (isDir),
// and when none does PDFium keeps its platform defaults (nil).
func TestFontDirsResolutionOrder(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	envDir := filepath.Join(root, "env-fonts")
	assets := filepath.Join(root, "bin")
	for _, d := range []string{filepath.Join(home, "fonts"), envDir, filepath.Join(assets, "fonts")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	assetsFonts := filepath.Join(assets, "fonts")
	noEnv := func(string) string { return "" }
	envOnly := func(k string) string {
		if k == FontDirsEnv {
			return envDir
		}
		return ""
	}

	// The env override wins even when home and the binary's fonts both exist.
	if got := fontDirs("darwin", home, envOnly, assetsFonts); len(got) != 1 || got[0] != envDir {
		t.Fatalf("env override: fontDirs = %v, want [%s]", got, envDir)
	}
	// Without the env, a seeded $home/fonts wins over the binary's fonts.
	if got := fontDirs("darwin", home, noEnv, assetsFonts); len(got) != 1 || got[0] != filepath.Join(home, "fonts") {
		t.Fatalf("home fonts: fontDirs = %v, want [%s]", got, filepath.Join(home, "fonts"))
	}
	// With neither env nor home fonts, the binary's fonts are found — the
	// packaged-install case the old assetsFontsDir path could never hit.
	if got := fontDirs("darwin", "", noEnv, assetsFonts); len(got) != 1 || got[0] != assetsFonts {
		t.Fatalf("assets fonts: fontDirs = %v, want [%s]", got, assetsFonts)
	}
	// When no candidate exists, nil keeps PDFium's platform defaults.
	if got := fontDirs("darwin", "", noEnv, ""); got != nil {
		t.Fatalf("no candidates: fontDirs = %v, want nil", got)
	}
	// A candidate that does not exist on disk is not offered.
	if got := fontDirs("darwin", home, noEnv, filepath.Join(assets, "absent")); len(got) != 1 || got[0] != filepath.Join(home, "fonts") {
		t.Fatalf("absent assets dir: fontDirs = %v, want home fonts only", got)
	}
}

// TestFontDirsAppendsLinuxPlatformFonts pins the Linux-only platform append:
// setting UserFontPaths replaces PDFium's default scan, so the platform's own
// font directories ride along with ours or Latin text breaks.
func TestFontDirsAppendsLinuxPlatformFonts(t *testing.T) {
	home := t.TempDir()
	ours := filepath.Join(home, "fonts")
	if err := os.MkdirAll(ours, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	xdg := t.TempDir()
	xdgFonts := filepath.Join(xdg, "fonts")
	if err := os.MkdirAll(xdgFonts, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	getenv := func(k string) string {
		switch k {
		case "XDG_DATA_HOME":
			return xdg
		case "HOME":
			return home
		}
		return ""
	}
	got := fontDirs("linux", home, getenv, "")
	if len(got) == 0 || got[0] != ours {
		t.Fatalf("fontDirs = %v, want ours first", got)
	}
	if !contains(got, xdgFonts) {
		t.Fatalf("fontDirs = %v, want the XDG fonts dir %s appended", got, xdgFonts)
	}
	// The same call on a non-Linux OS names no platform dirs.
	if got := fontDirs("darwin", home, getenv, ""); len(got) != 1 || got[0] != ours {
		t.Fatalf("darwin fontDirs = %v, want ours only", got)
	}
}

// TestFontDirsUsesAssetsDir proves the F3 wiring end to end: FontDirs reaches
// the binary's fonts through llm.AssetsDir (the one assets-location contract
// every packaged asset shares), with FOREBRAIN_ASSETS_DIR overriding — the
// same knob the asset installer honours.
func TestFontDirsUsesAssetsDir(t *testing.T) {
	t.Setenv(FontDirsEnv, "")
	bin := t.TempDir()
	fonts := filepath.Join(bin, "fonts")
	if err := os.MkdirAll(fonts, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("FOREBRAIN_ASSETS_DIR", bin)

	if got := FontDirs(""); !contains(got, fonts) {
		t.Fatalf("FontDirs = %v, want it to contain the assets fonts dir %s", got, fonts)
	}
}
