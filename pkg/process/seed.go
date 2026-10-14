package process

import (
	"log/slog"
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
	return nil
}
