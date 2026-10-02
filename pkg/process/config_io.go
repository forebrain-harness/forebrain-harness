// Config file IO and secret-reference resolution for the process runtime.
package process

import (
	"os"
	"path/filepath"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	homepkg "github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/joho/godotenv"
)

func loadCfg(home string) *appcfg.Root {
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

func apiKeyConfigReference(home, provider, apiKey string) (string, error) {
	key := strings.TrimSpace(apiKey)
	if key == "" || isEnvReference(key) {
		return key, nil
	}
	envName := firstEnvName(ProviderKeyEnvMap(provider, key))
	return writeEnvReference(home, envName, key)
}

// ProviderAPIKeyConfigReference stores a provider API key in the home's .env
// and returns the ${ENV} reference the config file keeps instead of the
// plaintext. Every surface that takes a provider key from a user — the setup
// flow and the web settings page alike — goes through this one path, so a key
// never lands in the yaml and the env naming stays single-sourced.
func ProviderAPIKeyConfigReference(home, provider, apiKey string) (string, error) {
	return apiKeyConfigReference(home, provider, apiKey)
}

func writeEnvReference(home string, envName string, value string) (string, error) {
	key := strings.TrimSpace(value)
	if key == "" || isEnvReference(key) {
		return key, nil
	}
	envName = strings.TrimSpace(envName)
	envPath := homepkg.DefaultEnvPath(home)
	envMap, err := godotenv.Read(envPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		envMap = map[string]string{}
	}
	envMap[envName] = key
	if err := os.MkdirAll(filepath.Dir(envPath), 0o755); err != nil {
		return "", err
	}
	if err := godotenv.Write(envMap, envPath); err != nil {
		return "", err
	}
	if err := os.Chmod(envPath, 0o600); err != nil {
		return "", err
	}
	return "${" + envName + "}", nil
}

func firstEnvName(m map[string]string) string {
	for k := range m {
		return strings.TrimSpace(k)
	}
	return ""
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

func storeSecretEnvReference(home string, envName string, secret string) (string, error) {
	key := strings.TrimSpace(secret)
	if key == "" || isEnvReference(key) {
		return key, nil
	}
	return writeEnvReference(home, strings.TrimSpace(envName), key)
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
