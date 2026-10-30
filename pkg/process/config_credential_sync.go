// The one-time final credential sync of plan 188 W12: between 187 (reads
// from the database) and 188 (writes to it), token refreshes kept writing the
// login files and the MCP overlay tree. Whichever side is newer wins into the
// database here; after the switch nothing writes those files again, so the
// comparison converges and later runs store nothing.
package process

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	openaiauth "github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/opencode"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// syncLegacyCredentialFilesIfNeeded compares the two login files and the MCP
// overlay tree against their database rows by stamp: a file newer than the row
// its content belongs to is the transition's last word, and its bytes win into
// the row; a row at least as new keeps what it holds. The comparison is
// idempotent — storing stamps the row with now, so the file is never newer
// again unless something writes it, and nothing does after this switch.
func syncLegacyCredentialFilesIfNeeded(ctx context.Context, sqlDB *sql.DB, home string) error {
	store := &state.ConfigStore{DB: sqlDB}
	// ChatGPT's file is wherever the file-era configuration pointed it
	// (credentials.chatgpt) — a login kept at ~/.codex/auth.json refreshed
	// there during the transition, not at the default path.
	chatGPTPath := openaiauth.DefaultPath(home)
	if persisted, err := appcfg.LoadPersisted(filepath.Join(strings.TrimSpace(home), "forebrain.yaml")); err == nil {
		chatGPTPath = openaiauth.ResolvePath(home, persisted.Credentials.ChatGPT)
	}
	for _, item := range []struct{ section, path string }{
		{"auth.chatgpt", chatGPTPath},
		{"auth.opencode", opencode.DefaultPath(home)},
	} {
		if err := syncCredentialFileRow(ctx, sqlDB, store, item.section, item.path); err != nil {
			return err
		}
	}
	return syncLegacyMCPOAuthRows(ctx, sqlDB, store, home)
}

// syncCredentialFileRow syncs one login file with its auth.* row.
func syncCredentialFileRow(ctx context.Context, sqlDB *sql.DB, store *state.ConfigStore, section, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var stamp int64
	switch err := sqlDB.QueryRowContext(ctx, `SELECT updated_at_ms FROM fb_config WHERE section = ?`, section).Scan(&stamp); {
	case err == sql.ErrNoRows:
		// No row: the login happened after the import, on the file side.
	case err != nil:
		return err
	default:
		if info.ModTime().UnixMilli() <= stamp {
			return nil
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	if err := store.SaveCredential(ctx, section, raw); err != nil {
		return fmt.Errorf("sync %s: %w", section, err)
	}
	slog.Info("synced a newer login file into the state database", "section", section)
	return nil
}

// syncLegacyMCPOAuthRows walks the overlay tree the way the legacy import did
// and resolves each file through the same candidates, so a file and its row
// always agree on the pair they belong to. Only a file newer than its row is
// stored; the import's ambiguity rule (a pair nothing configures, or two
// configurable names sharing one slug pair) skips with a warning and leaves
// the file for a hand move.
func syncLegacyMCPOAuthRows(ctx context.Context, sqlDB *sql.DB, store *state.ConfigStore, home string) error {
	persisted, err := appcfg.LoadPersisted(filepath.Join(strings.TrimSpace(home), "forebrain.yaml"))
	if err != nil {
		// A home with no readable file has no servers to name, and the tree's
		// files cannot be resolved to rows; there is nothing to sync.
		return nil
	}
	candidates, projectKeys, err := mcpOverlayCandidates(ctx, sqlDB, persisted)
	if err != nil {
		return err
	}
	overlayDir := filepath.Join(strings.TrimSpace(home), "state", "mcp-oauth")
	top, err := os.ReadDir(overlayDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	synced := 0
	syncFile := func(projectSlugDir, file string) error {
		info, err := os.Stat(file)
		if err != nil {
			return err
		}
		serverSlug := strings.TrimSuffix(filepath.Base(file), ".json")
		var target *overlayTarget
		matches := candidates[slugKey(projectSlugDir, serverSlug)]
		keyMatches := projectKeys[projectSlugDir]
		switch {
		case len(matches) == 1 && len(keyMatches) == 0:
			target = &matches[0]
		case len(matches) == 0 && len(keyMatches) == 1 && projectSlugDir != "":
			target = &overlayTarget{projectKey: keyMatches[0], serverName: serverSlug}
		default:
			return nil
		}
		var stamp int64
		switch err := sqlDB.QueryRowContext(ctx,
			`SELECT updated_at_ms FROM fb_mcp_oauth WHERE project_key = ? AND server_name = ?`,
			target.projectKey, target.serverName).Scan(&stamp); {
		case err == sql.ErrNoRows:
		case err != nil:
			return err
		default:
			if info.ModTime().UnixMilli() <= stamp {
				return nil
			}
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if err := store.SaveMCPOAuthRow(ctx, target.projectKey, target.serverName, raw); err != nil {
			return fmt.Errorf("sync overlay %s: %w", file, err)
		}
		synced++
		return nil
	}
	for _, entry := range top {
		switch {
		case entry.Type().IsDir() && entry.Name() == "projects":
			projects, err := os.ReadDir(filepath.Join(overlayDir, entry.Name()))
			if err != nil {
				return err
			}
			for _, slugDir := range projects {
				if !slugDir.IsDir() {
					continue
				}
				files, err := os.ReadDir(filepath.Join(overlayDir, entry.Name(), slugDir.Name()))
				if err != nil {
					return err
				}
				for _, f := range files {
					if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
						continue
					}
					if err := syncFile(slugDir.Name(), filepath.Join(overlayDir, entry.Name(), slugDir.Name(), f.Name())); err != nil {
						return err
					}
				}
			}
		case !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json"):
			if err := syncFile("", filepath.Join(overlayDir, entry.Name())); err != nil {
				return err
			}
		}
	}
	if synced > 0 {
		slog.Info("synced newer mcp oauth overlays into the state database", "count", synced)
	}
	return nil
}
