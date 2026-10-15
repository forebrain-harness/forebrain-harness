package process

import (
	"bytes"
	"errors"
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
// FontDirs looks for them. Per file, exactly one of four cases holds:
//
//   - the home copy is missing: it is written atomically (temp file + rename),
//     so an interrupted seed never leaves a half-written font behind;
//   - it is identical to the shipped bytes: skipped, mtime untouched;
//   - it is a true prefix of the shipped bytes: a write WE interrupted — the
//     only signature our own crash can leave — atomically rewritten;
//   - it differs in any other way: the user replaced it, a legitimate custom
//     override that is never touched and never warned about.
//
// A build that ships no fonts seeds nothing and does not fail. Errors are
// non-fatal — callers log a warning and continue.
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
		srcBytes, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			return err
		}
		out := filepath.Join(dest, name)
		dstBytes, err := os.ReadFile(out)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// missing: fall through to the write
		case err != nil:
			return err
		case bytes.Equal(dstBytes, srcBytes):
			continue
		case len(dstBytes) < len(srcBytes) && bytes.Equal(dstBytes, srcBytes[:len(dstBytes)]):
			// our own interrupted write: fall through to the rewrite
		default:
			// the user's custom override: never touched
			continue
		}
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
		if err := writeFontAtomic(out, srcBytes); err != nil {
			return err
		}
	}
	return nil
}

// writeFontAtomic writes data to path through a temporary file in the same
// directory, so a seed interrupted mid-write leaves nothing behind rather than
// a truncated font a later start would have to detect.
func writeFontAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".font-*")
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
