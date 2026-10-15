package process

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

// A home copy that is a true prefix of the shipped bytes is a seed this
// product itself interrupted; the next start rewrites it to the full font.
func TestSeedFontsRepairsTruncatedPrefix(t *testing.T) {
	assets := t.TempDir()
	fonts := filepath.Join(assets, "fonts")
	if err := os.MkdirAll(fonts, 0o755); err != nil {
		t.Fatal(err)
	}
	source := []byte("stand-in otf bytes, long enough to truncate")
	if err := os.WriteFile(filepath.Join(fonts, "NotoSansSC-Regular.otf"), source, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FOREBRAIN_ASSETS_DIR", assets)

	home := t.TempDir()
	dest := filepath.Join(home, "fonts")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	seeded := filepath.Join(dest, "NotoSansSC-Regular.otf")
	if err := os.WriteFile(seeded, source[:9], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := seedFonts(home); err != nil {
		t.Fatalf("seedFonts: %v", err)
	}
	got, err := os.ReadFile(seeded)
	if err != nil {
		t.Fatalf("read seeded font: %v", err)
	}
	if string(got) != string(source) {
		t.Fatalf("truncated prefix was not repaired: %q, want %q", got, source)
	}
}

// An identical home copy is skipped, not rewritten: its mtime must not move
// on a later start.
func TestSeedFontsIdenticalCopyKeepsMtime(t *testing.T) {
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
	before, err := os.Stat(seeded)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := seedFonts(home); err != nil {
		t.Fatalf("seedFonts (second): %v", err)
	}
	after, err := os.Stat(seeded)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("identical copy was rewritten: mtime moved %v -> %v", before.ModTime(), after.ModTime())
	}
}

// A build that ships no fonts seeds nothing and does not fail.
func TestSeedFontsWithoutShippedFontsIsQuiet(t *testing.T) {
	t.Setenv("FOREBRAIN_ASSETS_DIR", t.TempDir())
	if err := seedFonts(t.TempDir()); err != nil {
		t.Fatalf("seedFonts: %v", err)
	}
}
