package process

import (
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
)

// Seed installs process-owned bundled resources after the data directory exists.
func Seed(root string) error {
	root = strings.TrimSpace(root)
	if err := home.EnsureWorkspace(root, home.EnsureOptions{}); err != nil {
		return err
	}
	if err := skill.Install(root); err != nil {
		slog.Warn("system skills install failed", "err", err)
	}
	if err := llm.InitGlobalCatalog(root, home.Version); err != nil {
		slog.Warn("model catalog seed failed", "err", err)
	}
	// The dictionaries are a home-level install like models.json: seeding them
	// from the copy beside this binary at startup means a search — in this
	// process or any other forebrain sharing the home — finds them before its
	// first Chinese or Japanese query, with no window where a search would fail
	// because the home had not been prepared yet.
	if err := memory.Seed(root, home.Version); err != nil {
		slog.Warn("memory search dictionary seed failed", "err", err)
	}
	// The CJK fallback fonts are the same kind of home-level install: pkg/pdf
	// reads them from $FOREBRAIN_HOME/fonts to render Chinese PDFs, so seeding
	// them from the copy beside this binary means the first PDF read in any
	// process sharing the home already has them. The copy lives here rather
	// than in pkg/pdf so that pkg/process imports nothing new for it (its
	// fan-out is a fixed budget).
	if err := seedFonts(root); err != nil {
		slog.Warn("pdf font seed failed", "err", err)
	}
	return nil
}

// seedFonts copies the CJK fallback fonts shipped beside this binary (or named
// by FOREBRAIN_ASSETS_DIR) into $FOREBRAIN_HOME/fonts, where pkg/pdf's
// FontDirs looks for them. A file already seeded is left alone; a build that
// ships no fonts seeds nothing and does not fail. Errors are non-fatal —
// callers log a warning and continue.
func seedFonts(root string) error {
	assets, err := llm.AssetsDir()
	if err != nil {
		return err
	}
	source := filepath.Join(assets, "fonts")
	entries, err := os.ReadDir(source)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	dest := filepath.Join(root, "fonts")
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(name), ".otf") {
			continue
		}
		out := filepath.Join(dest, name)
		if _, err := os.Stat(out); err == nil {
			continue
		}
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
		if err := copyFont(filepath.Join(source, name), out); err != nil {
			return err
		}
	}
	return nil
}

// copyFont writes src's bytes to dst, replacing nothing: callers stat dst first.
func copyFont(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
