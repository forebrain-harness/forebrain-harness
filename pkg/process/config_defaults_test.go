package process

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/stretchr/testify/require"
)

// zeroIsTheDefault names the stored leaves whose zero value IS the setting, so
// no default is registered for them. Each entry says why, and a leaf that
// appears here without a reason — or a new leaf that appears nowhere — is what
// TestStoredConfigDefaultsCoverEveryField exists to catch.
var zeroIsTheDefault = map[string]string{
	// 0 means "the model catalog decides": the configured window is only a
	// fallback for a model the catalog does not know, so a stored number would
	// pin every model to one window.
	"agents.defaults.context_inject.model_context_tokens": "0 asks the model catalog",
	// The rules hook passes its own budget; the configuration only overrides
	// it, so a stored number would freeze a caller's default in the database.
	"agents.defaults.context_inject.max_pre_hook_rules_chars": "0 keeps the caller's budget",
	// The token estimator's own defaults (pkg/llm): the endpoints follow
	// $HF_ENDPOINT and the shipped URLs, and the tokenizer id follows the
	// binary's list, so storing them would freeze a cache-era decision.
	"agents.defaults.token_estimate.huggingface_endpoint": "empty follows $HF_ENDPOINT",
	"agents.defaults.token_estimate.modelscope_endpoint":  "empty follows the shipped URL",
	"agents.defaults.token_estimate.default_tokenizer":    "empty follows the shipped tokenizer",
	// Deprecated and ignored since the estimator picks a tokenizer per model.
	"agents.defaults.token_estimate.encoding":       "deprecated, ignored",
	"agents.defaults.token_estimate.model":          "deprecated, ignored",
	"agents.defaults.token_estimate.tokenizer_path": "deprecated, ignored",
	// peers is a database-only section (plan 195): nil is the interop
	// switch's own off, so the row never carries a default to hand the
	// settings surfaces.
	"peers.claude_code": "nil is the interop switch off",
}

// TestStoredConfigDefaultsCoverEveryField is the registry's guard (plan 207
// §3.2): every stored setting must carry a value in defaultStoredRoot(), so a
// field added to a stored section without a registered default turns this test
// red instead of reaching installs as a setting whose "unset" placeholder the
// settings surfaces have to explain.
func TestStoredConfigDefaultsCoverEveryField(t *testing.T) {
	encoded, err := json.Marshal(defaultStoredRoot())
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(encoded, &doc))

	rootType := reflect.TypeOf(appcfg.Root{})
	for _, section := range rootConfigSections {
		fieldType := rootType.FieldByIndex(rootConfigSectionFields[section]).Type
		value, present := doc[section]
		checkStoredDefault(t, section, fieldType, value, present)
	}
}

// checkStoredDefault walks one stored value's type against the JSON the
// registry produced. A leaf whose key the encoding dropped (omitempty on a zero
// value) is exactly the setting nobody registered.
func checkStoredDefault(t *testing.T, path string, typ reflect.Type, value any, present bool) {
	t.Helper()
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		object, _ := value.(map[string]any)
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.PkgPath != "" {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			child, ok := object[name]
			checkStoredDefault(t, path+"."+name, field.Type, child, ok)
		}
	case reflect.Slice, reflect.Map:
		// An empty list or map is its own value — "no writable root, no
		// server, no hook" — so an omitted empty collection is registered by
		// being empty, not by carrying an entry.
	default:
		if present {
			return
		}
		name := path[strings.LastIndex(path, ".")+1:]
		if appcfg.IsSecretLikeConfigKey(name) {
			// An unset secret is empty; nothing may invent one.
			return
		}
		if _, ok := zeroIsTheDefault[path]; ok {
			return
		}
		t.Errorf("%s has no registered default (and no entry in zeroIsTheDefault)", path)
	}
}

// defaultsHomeForTest writes the one file-era section a home needs to get
// through Open — the main agent's provider — and returns the home. Everything
// else it holds comes from the registry under test.
func defaultsHomeForTest(t *testing.T) string {
	t.Helper()
	home := importHomeForTest(t)
	t.Setenv("OPENAI_API_KEY", "sk-defaults-test")
	cfgPath := filepath.Join(home, "forebrain.yaml")
	raw := "agents:\n  definitions:\n    main:\n      primary: true\n      llm_providers:\n" +
		"        - provider: openai\n          model: gpt-main\n          api_key: ${OPENAI_API_KEY}\n          base_url: http://localhost:0/v1\n"
	if err := os.WriteFile(cfgPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// TestOpenSeedsEveryStoredSectionWithDefaults pins the registry end to end: a
// home that configured nothing but its model holds a row for every stored
// section, each carrying the value the runtime runs on.
func TestOpenSeedsEveryStoredSectionWithDefaults(t *testing.T) {
	home := defaultsHomeForTest(t)
	env := importOpenForTest(t, home)
	defer env.Close()

	rows := configRowsForTest(t, env.SQL)
	for _, section := range rootConfigSections {
		// A database-only section whose default is the zero value stores no
		// row (MarshalRootSection): peers' nil is the interop switch's own
		// off, and no row is exactly that (plan 195 §3.1).
		if dbOnlyConfigSections[section] {
			continue
		}
		raw, ok := rows[section]
		if !ok {
			t.Fatalf("section %s has no row after startup (rows: %v)", section, rowNames(rows))
		}
		if strings.TrimSpace(raw) == "" {
			t.Fatalf("section %s holds an empty value", section)
		}
	}
	if got := rows["approval_preset"]; got != `"workspace_write"` {
		t.Fatalf("approval_preset row = %s, want the default preset", got)
	}

	var agents appcfg.AgentsSection
	require.NoError(t, json.Unmarshal([]byte(rows["agents"]), &agents))
	wantParallel := max(1, runtime.NumCPU()-1)
	require.NotNil(t, agents.Defaults.Execution.MaxParallelSubagents)
	require.Equal(t, wantParallel, *agents.Defaults.Execution.MaxParallelSubagents)
	require.Equal(t, appcfg.WebSearchProviderNone, agents.Defaults.WebSearch.Provider)
	require.Equal(t, appcfg.WebFetchProviderBuiltin, agents.Defaults.WebFetch.Provider)
	// The imported provider survived the top-up untouched.
	require.Equal(t, "gpt-main", agents.Definitions["main"].LLMProviders[0].Model)

	var compact appcfg.CompactSection
	require.NoError(t, json.Unmarshal([]byte(rows["compact"]), &compact))
	require.Equal(t, assembly.DefaultPrompt, compact.Prompt)
	require.Equal(t, appcfg.DefaultAutoCompactPercent, compact.AutoCompactPercent)

	var memories appcfg.MemorySection
	require.NoError(t, json.Unmarshal([]byte(rows["memories"]), &memories))
	require.Equal(t, appcfg.MemoryModelCurrent, memories.ExtractModel)
	require.Equal(t, appcfg.MemoryModelCurrent, memories.ConsolidationModel)
}

// TestStoredConfigDefaultsStartupIsIdempotent pins the top-up's second half:
// once every setting is stored, the next startup writes nothing at all. The
// rows' timestamps are the evidence — a write would move them.
func TestStoredConfigDefaultsStartupIsIdempotent(t *testing.T) {
	home := defaultsHomeForTest(t)
	env := importOpenForTest(t, home)
	first := configTimestampsForTest(t, env.SQL)
	env.Close()

	env2 := importOpenForTest(t, home)
	defer env2.Close()
	second := configTimestampsForTest(t, env2.SQL)

	require.Equal(t, len(first), len(second))
	for section, stamp := range first {
		require.Equal(t, stamp, second[section], "section %s was rewritten by a startup that had nothing to add", section)
	}
}

// TestStoredConfigDefaultsKeepStoredValues pins the merge direction: a value
// the database already holds is the user's, and only the keys it lacks are
// filled — including from inside a nested object.
func TestStoredConfigDefaultsKeepStoredValues(t *testing.T) {
	ctx := context.Background()
	home := defaultsHomeForTest(t)
	env := importOpenForTest(t, home)
	defer env.Close()

	store := &state.ConfigStore{DB: env.SQL}
	// agents carries a definition and one setting; everything else it lacks
	// is what the top-up fills.
	require.NoError(t, store.UpdateSection(ctx, "agents", func(json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"definitions":{"main":{"llm_providers":[{"provider":"openai","model":"gpt-kept","api_key":"sk-kept","base_url":"http://localhost:0/v1"}]}},"defaults":{"network_retry_max":3,"web_search":{"provider":""}}}`), nil
	}))
	require.NoError(t, store.UpdateSection(ctx, "approval_preset", func(json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"full_access"`), nil
	}))
	require.NoError(t, ensureStoredConfigDefaults(ctx, store))

	rows := configRowsForTest(t, env.SQL)
	if got := rows["approval_preset"]; got != `"full_access"` {
		t.Fatalf("approval_preset row = %s, want the stored full_access", got)
	}
	var agents appcfg.AgentsSection
	require.NoError(t, json.Unmarshal([]byte(rows["agents"]), &agents))
	require.Equal(t, 3, agents.Defaults.NetworkRetryMaxOrDefault(), "a stored value must not be overwritten by its default")
	require.Equal(t, "sk-kept", agents.Definitions["main"].LLMProviders[0].APIKey)
	require.Equal(t, appcfg.DefaultWarnRemainingPercent, agents.Defaults.ContextInject.WarnRemainingPercent)
	require.NotNil(t, agents.Defaults.Execution.MaxParallelSubagents, "a missing leaf inside a stored section is filled")
	// An empty explicit representation is written out; a set one is left.
	require.Equal(t, appcfg.WebSearchProviderNone, agents.Defaults.WebSearch.Provider, "an empty provider reads as none")
}

func rowNames(rows map[string]string) []string {
	names := make([]string, 0, len(rows))
	for name := range rows {
		names = append(names, name)
	}
	return names
}

func configTimestampsForTest(t *testing.T, db *sql.DB) map[string]int64 {
	t.Helper()
	rows, err := db.Query(`SELECT section, updated_at_ms FROM fb_config`)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var section string
		var stamp int64
		require.NoError(t, rows.Scan(&section, &stamp))
		out[section] = stamp
	}
	require.NoError(t, rows.Err())
	return out
}
