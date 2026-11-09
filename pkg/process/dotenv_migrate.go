// Retirement of the home's .env (owner 2026-11-07, plan 268): the one-time
// migration that moves what it held into the state database and the
// configuration file.
package process

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/channel"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	homepkg "github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

// gatewayTokenEnvNames are the two names the retired .env merge gave the
// gateway's token. A token stored under either one moves into
// gateway.auth.token, in plaintext, in a file only its owner can read.
var gatewayTokenEnvNames = []string{"FOREBRAIN_GATEWAY_TOKEN", "GATEWAY_TOKEN"}

// The project channel rows the migration rewrites. It reads every project of
// every primary agent, because a channel secret has to become plaintext
// whatever owner it belongs to.
var (
	legacyChannelListSQL = dbx.SQL{
		SQLite:   `SELECT project_id, kind, config_json FROM fb_project_channels ORDER BY project_id, kind`,
		MySQL:    `SELECT project_id, kind, config_json FROM fb_project_channels ORDER BY project_id, kind`,
		Postgres: `SELECT project_id, kind, config_json FROM fb_project_channels ORDER BY project_id, kind`,
	}
	legacyChannelSecretSQL = dbx.SQL{
		SQLite:   `UPDATE fb_project_channels SET config_json=?, account_key=? WHERE project_id=? AND kind=?`,
		MySQL:    `UPDATE fb_project_channels SET config_json=?, account_key=? WHERE project_id=? AND kind=?`,
		Postgres: `UPDATE fb_project_channels SET config_json=$1, account_key=$2 WHERE project_id=$3 AND kind=$4`,
	}
)

// migrateLegacyDotEnv moves what <home>/.env held into the state database and
// the configuration file, then renames the file aside: after this nothing
// reads .env again. Gated on the file existing; idempotent per value.
//
// Nothing here is best-effort: a step that cannot finish fails the start, and
// the file is renamed only after every value has a home, so a failed migration
// never loses the operator's secrets. The renamed file is the user's to delete;
// no code reads it again.
func migrateLegacyDotEnv(ctx context.Context, db *sql.DB, home, cfgPath string) error {
	if db == nil {
		return fmt.Errorf("no state database")
	}
	envPath := homepkg.LegacyEnvPath(home)
	if _, err := os.Stat(envPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", envPath, err)
	}
	// One process migrates at a time: two replicas sharing a database would
	// otherwise both read the same values and race to store them.
	release, err := dbx.AcquireMigrationLock(ctx, db)
	if err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer release()

	dotenv := readLegacyDotEnv(home)
	// consumed names the variables an earlier step already moved somewhere of
	// their own; what is left over belongs in the operator's variables.
	consumed := map[string]bool{}
	if err := migrateGatewayToken(cfgPath, dotenv, consumed); err != nil {
		return err
	}
	if err := migrateChannelSecrets(ctx, db, dotenv); err != nil {
		return err
	}
	if err := migrateSearchKeys(ctx, db, dotenv, consumed); err != nil {
		return err
	}
	stored, err := migrateVariableTable(ctx, db, dotenv, consumed)
	if err != nil {
		return err
	}
	renamed, err := renameLegacyEnvFile(envPath)
	if err != nil {
		return err
	}
	slog.Info("moved .env values into the state database", "variables", stored, "renamed_to", renamed)
	return nil
}

// MigrateLegacyHome runs the one-time migrations a pre-plan-268 home needs —
// the model table's move out of the file, and the .env's move into the state
// database and the configuration file — for a surface that has to judge the
// configuration BEFORE it opens one. Both startup gates refuse a home whose
// main agent has no key, and a home that kept its key in .env now reads as
// keyless until this has run; judging first would leave it unable to start the
// very process that migrates it. Running them here is the same work Open does
// moments later, and both are gated on content, so the second run is a no-op.
//
// A home with no configuration file at all is left alone: there is nothing to
// migrate, and the caller's first-setup gate is the right answer.
func MigrateLegacyHome(ctx context.Context, home, cfgPath string) error {
	if strings.TrimSpace(cfgPath) == "" {
		return nil
	}
	if _, err := os.Stat(cfgPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", cfgPath, err)
	}
	db, err := StateDB(Context{Home: home, ConfigPath: cfgPath})
	if err != nil {
		return err
	}
	defer db.Close()
	if err := migrateLegacyLLMModels(ctx, db, home); err != nil {
		return fmt.Errorf("move model configurations into the state database: %w", err)
	}
	if err := migrateLegacyDotEnv(ctx, db, home, cfgPath); err != nil {
		return fmt.Errorf("move .env values into the state database: %w", err)
	}
	return nil
}

// migrateGatewayToken writes the token the configuration referenced into
// gateway.auth.token itself, unless the process environment supplies the name
// it references — that is the container arrangement, and the reference is what
// such a deployment wants to keep.
func migrateGatewayToken(cfgPath string, dotenv map[string]string, consumed map[string]bool) error {
	persisted, err := appcfg.LoadPersisted(cfgPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", cfgPath, err)
	}
	name, ok := envReferenceName(persisted.Gateway.Auth.Token)
	if !ok || !isGatewayTokenEnvName(name) {
		return nil
	}
	consumed[name] = true
	if _, inEnv := os.LookupEnv(name); inEnv {
		return nil
	}
	token := strings.TrimSpace(dotenv[name])
	if token == "" {
		return nil
	}
	if err := appcfg.SetGatewayToken(cfgPath, token); err != nil {
		return fmt.Errorf("store the gateway token in %s: %w", cfgPath, err)
	}
	return nil
}

// migrateChannelSecrets replaces every ${NAME} a project channel's secret
// field holds with the plaintext behind it, and recomputes the account identity
// the plaintext names. A reference nothing can resolve stays a reference and is
// reported: guessing would either invent a credential or silently disable a
// channel.
func migrateChannelSecrets(ctx context.Context, db *sql.DB, dotenv map[string]string) error {
	dialect := dbx.DialectOf(db)
	rows, err := db.QueryContext(ctx, legacyChannelListSQL.For(dialect))
	if err != nil {
		return fmt.Errorf("read project channels: %w", err)
	}
	type channelRow struct {
		projectID string
		kind      string
		config    string
	}
	var all []channelRow
	for rows.Next() {
		var row channelRow
		if err := rows.Scan(&row.projectID, &row.kind, &row.config); err != nil {
			rows.Close()
			return fmt.Errorf("read project channels: %w", err)
		}
		all = append(all, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return fmt.Errorf("read project channels: %w", err)
	}
	for _, row := range all {
		fields := map[string]any{}
		if raw := strings.TrimSpace(row.config); raw != "" && raw != "{}" {
			if err := json.Unmarshal([]byte(raw), &fields); err != nil {
				slog.Warn("a project channel's stored configuration could not be read; its secrets were left as they are",
					"project", row.projectID, "kind", row.kind, "err", err)
				continue
			}
		}
		changed := false
		for name, value := range fields {
			if !appcfg.IsSecretLikeConfigKey(name) {
				continue
			}
			str, ok := value.(string)
			if !ok || !isEnvReference(str) {
				continue
			}
			resolved, ok := resolveLegacySecret(str, dotenv)
			if !ok {
				slog.Warn("a channel secret reference did not resolve; re-enter it on the channel page",
					"project", row.projectID, "kind", row.kind)
				continue
			}
			fields[name] = resolved
			changed = true
		}
		if !changed {
			continue
		}
		raw, err := json.Marshal(fields)
		if err != nil {
			return fmt.Errorf("store channel %s of project %s: %w", row.kind, row.projectID, err)
		}
		if _, err := dbx.Exec(ctx, db, legacyChannelSecretSQL.For(dialect),
			string(raw), channel.AccountKeyJSON(row.kind, raw), row.projectID, row.kind); err != nil {
			return fmt.Errorf("store channel %s of project %s: %w", row.kind, row.projectID, err)
		}
	}
	return nil
}

// migrateSearchKeys carries the four web search and fetch keys from .env into
// the agents section, which is where the tools read them (owner ruling
// 2026-10-30: credentials live in the state database in plaintext). A key the
// operator already configured is left alone — the configuration is the later
// and more deliberate statement.
func migrateSearchKeys(ctx context.Context, db *sql.DB, dotenv map[string]string, consumed map[string]bool) error {
	type pendingKey struct {
		envName string
		apply   func(*appcfg.AgentsSection, string)
	}
	keys := []pendingKey{
		{"TAVILY_API_KEY", func(a *appcfg.AgentsSection, v string) {
			if a.Defaults.WebSearch.Tavily == nil {
				a.Defaults.WebSearch.Tavily = &appcfg.WebSearchTavily{}
			}
			if strings.TrimSpace(a.Defaults.WebSearch.Tavily.APIKey) == "" {
				a.Defaults.WebSearch.Tavily.APIKey = v
			}
		}},
		{"BRAVE_SEARCH_API_KEY", func(a *appcfg.AgentsSection, v string) {
			if a.Defaults.WebSearch.Brave == nil {
				a.Defaults.WebSearch.Brave = &appcfg.WebSearchBrave{}
			}
			if strings.TrimSpace(a.Defaults.WebSearch.Brave.APIKey) == "" {
				a.Defaults.WebSearch.Brave.APIKey = v
			}
		}},
		{"BAIDU_WEB_SEARCH_API_KEY", func(a *appcfg.AgentsSection, v string) {
			if a.Defaults.WebSearch.Baidu == nil {
				a.Defaults.WebSearch.Baidu = &appcfg.WebSearchBaidu{}
			}
			if strings.TrimSpace(a.Defaults.WebSearch.Baidu.APIKey) == "" {
				a.Defaults.WebSearch.Baidu.APIKey = v
			}
		}},
		{"DEEPSEEK_API_KEY", func(a *appcfg.AgentsSection, v string) {
			// One key authorizes both DeepSeek surfaces the tools use.
			if a.Defaults.WebSearch.DeepSeek == nil {
				a.Defaults.WebSearch.DeepSeek = &appcfg.WebSearchDeepSeek{}
			}
			if strings.TrimSpace(a.Defaults.WebSearch.DeepSeek.APIKey) == "" {
				a.Defaults.WebSearch.DeepSeek.APIKey = v
			}
			if a.Defaults.WebFetch.DeepSeek == nil {
				a.Defaults.WebFetch.DeepSeek = &appcfg.WebFetchDeepSeek{}
			}
			if strings.TrimSpace(a.Defaults.WebFetch.DeepSeek.APIKey) == "" {
				a.Defaults.WebFetch.DeepSeek.APIKey = v
			}
		}},
	}
	var present []pendingKey
	for _, key := range keys {
		value := strings.TrimSpace(dotenv[key.envName])
		if value == "" {
			continue
		}
		consumed[key.envName] = true
		present = append(present, key)
	}
	if len(present) == 0 {
		return nil
	}
	store := &state.ConfigStore{DB: db}
	return store.UpdateSection(ctx, "agents", func(cur json.RawMessage) (json.RawMessage, error) {
		var agents appcfg.AgentsSection
		if raw := strings.TrimSpace(string(cur)); raw != "" && raw != "{}" {
			if err := json.Unmarshal(cur, &agents); err != nil {
				return nil, fmt.Errorf("read the agents section: %w", err)
			}
		}
		for _, key := range present {
			key.apply(&agents, strings.TrimSpace(dotenv[key.envName]))
		}
		out, err := json.Marshal(agents)
		if err != nil {
			return nil, err
		}
		return out, nil
	})
}

// migrateVariableTable stores every remaining .env variable in the operator's
// variables, the table MCP and LSP entries resolve ${NAME} from. Three kinds
// stay out: the gateway token (it has its own home), the names earlier steps
// already moved, and the model providers' own variables — a key the model table
// carries must not also sit here, because the environment is not a model key's
// source any more (plan 262).
func migrateVariableTable(ctx context.Context, db *sql.DB, dotenv map[string]string, consumed map[string]bool) (int, error) {
	store := &state.EnvVarStore{DB: db}
	existing, err := store.Map(ctx)
	if err != nil {
		return 0, fmt.Errorf("read the variables: %w", err)
	}
	modelNames, err := legacyModelEnvNames(ctx, db)
	if err != nil {
		return 0, err
	}
	names := make([]string, 0, len(dotenv))
	for name := range dotenv {
		names = append(names, name)
	}
	sort.Strings(names)
	stored := 0
	for _, name := range names {
		switch {
		case consumed[name], isGatewayTokenEnvName(name), modelNames[name]:
			continue
		case strings.TrimSpace(dotenv[name]) == "":
			continue
		case existing[name] != "":
			// The operator already has this one; their value wins.
			continue
		}
		if err := store.Set(ctx, name, dotenv[name]); err != nil {
			if err == state.ErrEnvVarName {
				slog.Warn("a .env variable is not a usable ${NAME} reference; it stayed in the renamed file", "name", name)
				continue
			}
			return stored, fmt.Errorf("store variable %s: %w", name, err)
		}
		stored++
	}
	return stored, nil
}

// legacyModelEnvNames names the variables a model row owns: the uniform
// <PROVIDER>_API_KEY and <PROVIDER>_BASE_URL of every provider the model table
// holds, and of every provider that ships with the build.
func legacyModelEnvNames(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	out := map[string]bool{}
	add := func(provider string) {
		p := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(provider), "-", "_"))
		if p == "" {
			return
		}
		out[p+"_API_KEY"] = true
		out[p+"_BASE_URL"] = true
	}
	rows, err := (&state.LLMModelStore{DB: db}).List(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the model table: %w", err)
	}
	for _, row := range rows {
		add(row.Provider)
	}
	for _, provider := range appcfg.BuiltinProviders() {
		add(provider.ID)
	}
	return out, nil
}

// renameLegacyEnvFile puts the .env out of reach once every value has a home:
// the file appears next to it under a name nothing reads, and the operator
// deletes it when they are satisfied.
func renameLegacyEnvFile(envPath string) (string, error) {
	base := homepkg.LegacyEnvFile + ".migrated-" + time.Now().UTC().Format("20060102150405")
	target := filepath.Join(filepath.Dir(envPath), base)
	for i := 2; ; i++ {
		if _, err := os.Stat(target); os.IsNotExist(err) {
			break
		}
		target = filepath.Join(filepath.Dir(envPath), fmt.Sprintf("%s-%d", base, i))
	}
	if err := os.Rename(envPath, target); err != nil {
		return "", fmt.Errorf("rename %s: %w", envPath, err)
	}
	return filepath.Base(target), nil
}

// envReferenceName returns the variable a whole-value ${NAME} reference names.
func envReferenceName(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if !isEnvReference(value) {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(value, "${"), "}"), true
}

func isGatewayTokenEnvName(name string) bool {
	for _, candidate := range gatewayTokenEnvNames {
		if name == candidate {
			return true
		}
	}
	return false
}
