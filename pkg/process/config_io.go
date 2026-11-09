// Config file reads and the retirements the process runtime runs on them.
package process

import (
	"context"
	"log/slog"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

// MigrateLegacyChannels retires the primary-agent-level channel configuration
// plan 035 replaced with per-project channels. It strips the section from the
// config file (after backing the file up) and reports the names of the channels
// that were enabled when they were removed, so the caller can warn that they
// stopped. A config with no such section is left untouched.
func MigrateLegacyChannels(cfgPath string) []string {
	mig, err := appcfg.MigrateLegacyAgentChannels(cfgPath)
	if err != nil {
		slog.Warn("legacy channel migration failed", "config", cfgPath, "err", err)
		return nil
	}
	if !mig.Removed {
		return nil
	}
	if len(mig.Enabled) > 0 {
		slog.Warn("primary-agent-level channels were removed; reconfigure them in a project's Channels tab",
			"channels", strings.Join(mig.Enabled, ", "), "backup", mig.Backup)
	} else {
		slog.Info("removed the retired primary-agent-level channels section", "backup", mig.Backup)
	}
	return mig.Enabled
}

// channelMigrationBannerNotes turns the enabled channel names a migration
// removed into the one line the startup banner prints.
func channelMigrationBannerNotes(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	return []string{"channels " + strings.Join(names, ", ") +
		" were configured at the primary-agent level and were removed; configure them in a project's Channels tab"}
}

// loadCfg reads the configuration a pre-session reader sees: the composed
// source when the state database holds sections (plan 187), the file-era
// configuration otherwise (plan 207 §3.5). A home nothing can read yet reads as
// no configuration.
func loadCfg(home string) *appcfg.Root {
	cfg, err := StartupConfig(context.Background(), home)
	if err != nil {
		return nil
	}
	return &cfg
}

func isEnvReference(v string) bool {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "${") || !strings.HasSuffix(v, "}") {
		return false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(v, "${"), "}")
	if name == "" {
		return false
	}
	for i, r := range name {
		isAlpha := r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'
		isDigitAfterFirst := i > 0 && r >= '0' && r <= '9'
		if r == '_' || isAlpha || isDigitAfterFirst {
			continue
		}
		return false
	}
	return true
}
