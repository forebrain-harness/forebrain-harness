package skill

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed system_assets/*
var assetsFS embed.FS

// Install extracts embedded system skills to $FOREBRAIN_HOME/skills/.system/.
// It uses a content-hash marker for idempotency: if the marker matches the
// current Fingerprint(), extraction is skipped. On mismatch or missing marker,
// the .system/ directory is wiped and re-extracted.
//
// Errors are returned but callers should treat them as non-fatal (log warning,
// don't fail startup).
func Install(forebrainHome string) error {
	if strings.TrimSpace(forebrainHome) == "" {
		return fmt.Errorf("systemskills: empty forebrainHome")
	}
	dest := filepath.Join(forebrainHome, "skills", systemSkillsDirName)
	markerPath := filepath.Join(dest, ".forebrain-system-skills.marker")

	expected := Fingerprint()
	if existing, err := os.ReadFile(markerPath); err == nil {
		if strings.TrimSpace(string(existing)) == expected {
			return nil // already up to date
		}
	}

	// Wipe and re-extract
	if err := os.RemoveAll(dest); err != nil {
		return fmt.Errorf("systemskills: remove old .system: %w", err)
	}
	if err := extractEmbedFS(assetsFS, "system_assets", dest); err != nil {
		return fmt.Errorf("systemskills: extract: %w", err)
	}
	if err := os.WriteFile(markerPath, []byte(expected+"\n"), 0o644); err != nil {
		return fmt.Errorf("systemskills: write marker: %w", err)
	}
	return nil
}

// Fingerprint returns a 16-char hex content hash of all embedded skill assets.
// It hashes both file paths and file contents for deterministic output.
func Fingerprint() string {
	h := sha256.New()
	_ = fs.WalkDir(assetsFS, "system_assets", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, readErr := assetsFS.ReadFile(path)
		if readErr != nil {
			return nil
		}
		h.Write([]byte(path))
		h.Write(data)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// extractEmbedFS walks the embed.FS starting at prefix and writes all files
// to destDir on disk, preserving directory structure.
func extractEmbedFS(embedFS embed.FS, prefix string, destDir string) error {
	return fs.WalkDir(embedFS, prefix, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Compute relative path from prefix
		rel, relErr := filepath.Rel(filepath.FromSlash(prefix), filepath.FromSlash(path))
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}

		// Convert embed forward-slash path to OS-native for disk write
		target := filepath.Join(destDir, rel)

		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		// Read from embed (always forward-slash paths)
		data, readErr := embedFS.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		// Ensure parent directory exists
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}

		return os.WriteFile(target, data, 0o644)
	})
}
