package process

import (
	"log/slog"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
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
	if err := llm.InitGlobalCatalog(root); err != nil {
		slog.Warn("model catalog seed failed", "err", err)
	}
	return nil
}
