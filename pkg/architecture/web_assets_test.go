package architecture_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/architecture"
)

// TestWebBuildLoadsNoExternalAsset is the intranet guarantee for the shipped web
// UI (plans/208): the gateway serves pkg/gateway/dist to a browser that has no
// public network, so nothing the build renders may be fetched from outside it.
func TestWebBuildLoadsNoExternalAsset(t *testing.T) {
	dist := filepath.Join(findRepoRoot(t), "pkg", "gateway", "dist")
	references, scanned, err := architecture.ExternalWebAssets(dist)
	if err != nil {
		t.Fatal(err)
	}
	if scanned == 0 {
		t.Fatalf("no documents were read under %s; the guard would pass on anything", dist)
	}
	if len(references) > 0 {
		t.Fatalf("the web build loads %d resources from an absolute address, which an intranet cannot reach:\n%s",
			len(references), formatWebAssets(references))
	}
}

// TestExternalWebAssetsCatchesExternalLoads is the guard's red example: a build
// that does pull a script and a font from outside must be reported — both the
// <script src> and the CSS url(), which is all a browser fetches — while a
// documentation link written in a string stays silent, because nothing loads it.
func TestExternalWebAssetsCatchesExternalLoads(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.html", `<!DOCTYPE html><html><head>`+
		`<script type="module" crossorigin src="https://cdn.example.com/app.js"></script>`+
		`<link rel="stylesheet" href="/assets/app.css">`+
		`</head><body><a href="https://example.com/docs">docs</a></body></html>`)
	write("assets/app.css", `@font-face { src: url("https://fonts.example.com/x.woff2") }`)
	write("assets/app.js", `const help = "https://example.com/docs";`)

	references, scanned, err := architecture.ExternalWebAssets(dir)
	if err != nil {
		t.Fatal(err)
	}
	if scanned != 3 {
		t.Fatalf("scanned %d documents, want 3", scanned)
	}
	if len(references) != 2 {
		t.Fatalf("the scan reported %d references, want the script and the font only:\n%s",
			len(references), formatWebAssets(references))
	}
	for _, want := range []struct{ file, target string }{
		{"assets/app.css", "https://fonts.example.com/x.woff2"},
		{"index.html", "https://cdn.example.com/app.js"},
	} {
		if references[0] != (architecture.WebAssetReference{File: want.file, Target: want.target}) &&
			references[1] != (architecture.WebAssetReference{File: want.file, Target: want.target}) {
			t.Errorf("the scan missed %s in %s; it reported:\n%s", want.target, want.file, formatWebAssets(references))
		}
	}
}

// formatWebAssets renders references one per line for a failure message.
func formatWebAssets(references []architecture.WebAssetReference) string {
	lines := make([]string, len(references))
	for index, reference := range references {
		lines[index] = fmt.Sprintf("%s: %s", reference.File, reference.Target)
	}
	return strings.Join(lines, "\n")
}
