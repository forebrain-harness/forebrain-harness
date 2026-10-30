// Config file IO and secret-reference resolution for the process runtime.
package process

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	homepkg "github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/joho/godotenv"
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
// source when the state database holds sections (plan 187), the file
// otherwise. A home nothing can read yet reads as no configuration.
func loadCfg(home string) *appcfg.Root {
	if composed, ok, err := ComposedStartupConfig(context.Background(), home); err != nil {
		return nil
	} else if ok {
		return &composed
	}
	cfgPath, err := homepkg.ResolveConfigPath(home)
	if err != nil {
		cfgPath = homepkg.ConfigPath(home)
	}
	cfg, err := appcfg.Load(cfgPath)
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

func SecretConfigReferenceForOnboard(home string, envName string, secret string) (string, error) {
	return secretConfigReference(home, envName, secret)
}

func secretConfigReference(home string, envName string, secret string) (string, error) {
	key := strings.TrimSpace(secret)
	if key == "" || isEnvReference(key) {
		return key, nil
	}
	name := strings.TrimSpace(envName)
	if name == "" {
		name = "FOREBRAIN_SECRET"
	}
	return storeSecretEnvReference(home, name, key)
}

// storeSecretEnvReference stores a channel secret in the home's .env and
// returns the ${ENV} reference the channel's stored configuration keeps
// instead of the plaintext. This is the one .env write left (plan 188 W14):
// provider keys live in plaintext in the agents section, and channels keep
// the reference/value split their store was built on.
func storeSecretEnvReference(home string, envName string, secret string) (string, error) {
	key := strings.TrimSpace(secret)
	if key == "" || isEnvReference(key) {
		return key, nil
	}
	name := strings.TrimSpace(envName)
	envPath := homepkg.DefaultEnvPath(home)
	envMap, err := godotenv.Read(envPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		envMap = map[string]string{}
	}
	envMap[name] = key
	if err := os.MkdirAll(filepath.Dir(envPath), 0o755); err != nil {
		return "", err
	}
	if err := godotenv.Write(envMap, envPath); err != nil {
		return "", err
	}
	if err := os.Chmod(envPath, 0o600); err != nil {
		return "", err
	}
	return "${" + name + "}", nil
}

// ChannelSecretEnvName builds the ~/.forebrain/.env variable a channel secret is
// stored under, scoped to the primary agent that owns the channel:
// ("main", "TELEGRAM_BOT_TOKEN") becomes FOREBRAIN_MAIN_TELEGRAM_BOT_TOKEN.
//
// The agent id is part of the name because two primary agents may each run the
// same kind of channel with different credentials; a shared name would let one
// agent's token silently become the other's.
func ChannelSecretEnvName(agentID string, suffix string) string {
	id := strings.ToUpper(strings.TrimSpace(agentID))
	id = strings.ReplaceAll(id, "-", "_")
	name := strings.ToUpper(strings.TrimSpace(suffix))
	if id == "" {
		return "FOREBRAIN_" + name
	}
	return "FOREBRAIN_" + id + "_" + name
}

// ChannelSecretEnvNameForProject builds the ~/.forebrain/.env variable a
// project's channel secret is stored under, scoped to the project and kind that
// own it: (project "proj-…", "telegram", "bot_token") becomes
// FOREBRAIN_CH_PROJ_…_TELEGRAM_BOT_TOKEN. Project channels live in the state
// database as ${FOREBRAIN_CH_…} references; the value lives only here.
func ChannelSecretEnvNameForProject(projectID, kind, field string) string {
	id := channelEnvToken(projectID)
	name := strings.ToUpper(strings.TrimSpace(kind) + "_" + strings.TrimSpace(field))
	name = strings.ReplaceAll(name, "-", "_")
	if id == "" {
		return "FOREBRAIN_CH_" + name
	}
	return "FOREBRAIN_CH_" + id + "_" + name
}

// channelEnvToken renders a project id as the env-name segment that scopes its
// channel secrets. Non-alphanumerics become underscores so the name is a legal
// variable, and the id stays whole so two projects never share a segment.
func channelEnvToken(projectID string) string {
	id := strings.ToUpper(strings.TrimSpace(projectID))
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// RemoveChannelSecretsForProject drops every channel secret a project owns from
// the home's .env, so deleting a project leaves no orphaned credential behind.
func RemoveChannelSecretsForProject(home, projectID string) error {
	prefix := "FOREBRAIN_CH_" + channelEnvToken(projectID) + "_"
	envPath := homepkg.DefaultEnvPath(home)
	envMap, err := godotenv.Read(envPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	changed := false
	for k := range envMap {
		if strings.HasPrefix(strings.ToUpper(k), prefix) {
			delete(envMap, k)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := godotenv.Write(envMap, envPath); err != nil {
		return err
	}
	return os.Chmod(envPath, 0o600)
}
