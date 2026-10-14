package process

import (
	"os"
	"path/filepath"
	"testing"
)

// The CJK fallback fonts live in $FOREBRAIN_HOME/fonts after startup, seeded
// from the copy shipped beside the binary (or named by FOREBRAIN_ASSETS_DIR).
func TestSeedFontsCopiesShippedFontsOnce(t *testing.T) {
	assets := t.TempDir()
	fonts := filepath.Join(assets, "fonts")
	if err := os.MkdirAll(fonts, 0o755); err != nil {
		t.Fatal(err)
	}
	source := []byte("stand-in otf bytes")
	if err := os.WriteFile(filepath.Join(fonts, "NotoSansSC-Regular.otf"), source, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FOREBRAIN_ASSETS_DIR", assets)

	home := t.TempDir()
	if err := seedFonts(home); err != nil {
		t.Fatalf("seedFonts: %v", err)
	}
	seeded := filepath.Join(home, "fonts", "NotoSansSC-Regular.otf")
	got, err := os.ReadFile(seeded)
	if err != nil {
		t.Fatalf("read seeded font: %v", err)
	}
	if string(got) != string(source) {
		t.Fatalf("seeded bytes = %q, want %q", got, source)
	}

	// A home copy that already exists is left alone.
	if err := os.WriteFile(seeded, []byte("user replaced"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := seedFonts(home); err != nil {
		t.Fatalf("seedFonts (second): %v", err)
	}
	got, err = os.ReadFile(seeded)
	if err != nil {
		t.Fatalf("reread seeded font: %v", err)
	}
	if string(got) != "user replaced" {
		t.Fatalf("the second seed overwrote the home copy: %q", got)
	}
}

// A build that ships no fonts seeds nothing and does not fail.
func TestSeedFontsWithoutShippedFontsIsQuiet(t *testing.T) {
	t.Setenv("FOREBRAIN_ASSETS_DIR", t.TempDir())
	if err := seedFonts(t.TempDir()); err != nil {
		t.Fatalf("seedFonts: %v", err)
	}
}
