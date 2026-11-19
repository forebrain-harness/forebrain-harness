package process

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// defaultEffortInstall is one throwaway install a save runs against: the home it
// writes and the state database its rows land in.
type defaultEffortInstall struct {
	t    *testing.T
	rt   Context
	home string
}

func newDefaultEffortInstall(t *testing.T) *defaultEffortInstall {
	t.Helper()
	ResetResolve()
	t.Cleanup(ResetResolve)
	// A row the save writes must not fail on a catalog question, so the plan
	// model tables are installed the way the process installs them.
	installBuiltinModelTables()
	home := t.TempDir()
	return &defaultEffortInstall{
		t:    t,
		home: home,
		rt:   Context{Home: home, ConfigPath: filepath.Join(home, "forebrain.yaml"), Config: appcfg.Root{}},
	}
}

func (i *defaultEffortInstall) open() *sql.DB {
	i.t.Helper()
	db, err := StateDB(i.rt)
	if err != nil {
		i.t.Fatalf("open state: %v", err)
	}
	return db
}

// seed writes one row the save is about to configure over.
func (i *defaultEffortInstall) seed(in LLMModelInput) string {
	i.t.Helper()
	db := i.open()
	defer db.Close()
	id, err := UpsertLLMModel(context.Background(), db, in)
	if err != nil {
		i.t.Fatalf("seed row: %v", err)
	}
	return id
}

// row reads one row back from a database opened after the save, which is what
// proves the value reached the table rather than only the input.
func (i *defaultEffortInstall) row(id string) state.LLMModel {
	i.t.Helper()
	db := i.open()
	defer db.Close()
	row, err := (&state.LLMModelStore{DB: db}).Get(context.Background(), id)
	if err != nil {
		i.t.Fatalf("read row %q: %v", id, err)
	}
	return row
}

// effortOf is the effort a request on this row actually carries: the row's own
// default filled into params that name none (pkg/config's projection).
func effortOf(t *testing.T, row state.LLMModel) string {
	t.Helper()
	configRow, err := row.ConfigRow()
	if err != nil {
		t.Fatalf("config row: %v", err)
	}
	return appcfg.ReasoningEffort(appcfg.ProviderFromModelRow(configRow).Params)
}

func strPtr(value string) *string { return &value }

// TestExecuteModelDefaultEffort is the save contract the wizard's default-effort
// page relies on: a non-nil pointer states the row's default effort, an empty
// one states "the provider decides", and both replace whatever the row's params
// pinned so the new answer is the one a request carries.
func TestExecuteModelDefaultEffort(t *testing.T) {
	const (
		provider = "openai"
		model    = "gpt-5"
		baseURL  = "https://api.openai.com/v1"
		key      = "sk-test-default-effort"
	)
	newRow := func() LLMModelInput {
		return LLMModelInput{
			Provider: provider, Model: model, BaseURL: baseURL,
			APIKey: strPtr(key),
		}
	}
	options := func(effort *string) Options {
		return Options{
			Provider: provider, Model: model, BaseURL: baseURL, APIKey: key,
			CapDefaultEffort: effort,
		}
	}

	t.Run("a new row stores the chosen level", func(t *testing.T) {
		install := newDefaultEffortInstall(t)
		rep, err := Execute(install.rt, options(strPtr("high")))
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		row := install.row(rep.ModelID)
		if row.CapDefaultEffort != "high" {
			t.Fatalf("cap_default_effort = %q, want high", row.CapDefaultEffort)
		}
		if got := effortOf(t, row); got != "high" {
			t.Fatalf("request effort = %q, want high", got)
		}
	})

	t.Run("an existing row's level is replaced and its params effort cleared", func(t *testing.T) {
		install := newDefaultEffortInstall(t)
		seeded := newRow()
		seeded.CapDefaultEffort = strPtr("high")
		params := `{"temperature":0.2,"reasoning":{"effort":"high","summary":"auto"}}`
		seeded.ParamsJSON = &params
		id := install.seed(seeded)

		rep, err := Execute(install.rt, options(strPtr("low")))
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if rep.ModelID != id {
			t.Fatalf("ModelID = %q, want the seeded row %q", rep.ModelID, id)
		}
		row := install.row(id)
		if row.CapDefaultEffort != "low" {
			t.Fatalf("cap_default_effort = %q, want low", row.CapDefaultEffort)
		}
		if strings.Contains(row.ParamsJSON, `"effort"`) {
			t.Fatalf("params = %q, want the stale effort removed", row.ParamsJSON)
		}
		if !strings.Contains(row.ParamsJSON, `"temperature":0.2`) || !strings.Contains(row.ParamsJSON, `"summary":"auto"`) {
			t.Fatalf("params = %q, want the other fields kept", row.ParamsJSON)
		}
		if got := effortOf(t, row); got != "low" {
			t.Fatalf("request effort = %q, want the new level", got)
		}
	})

	t.Run("provider default clears the level and the stale params effort", func(t *testing.T) {
		install := newDefaultEffortInstall(t)
		seeded := newRow()
		seeded.CapDefaultEffort = strPtr("high")
		params := `{"temperature":0.2,"reasoning":{"effort":"high"}}`
		seeded.ParamsJSON = &params
		id := install.seed(seeded)

		if _, err := Execute(install.rt, options(strPtr(""))); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		row := install.row(id)
		if row.CapDefaultEffort != "" {
			t.Fatalf("cap_default_effort = %q, want it cleared", row.CapDefaultEffort)
		}
		if strings.Contains(row.ParamsJSON, `"effort"`) {
			t.Fatalf("params = %q, want the stale effort removed", row.ParamsJSON)
		}
		if !strings.Contains(row.ParamsJSON, `"temperature":0.2`) {
			t.Fatalf("params = %q, want the other fields kept", row.ParamsJSON)
		}
		if got := effortOf(t, row); got != "" {
			t.Fatalf("request effort = %q, want none", got)
		}
	})

	t.Run("no pointer leaves the row exactly as it was", func(t *testing.T) {
		install := newDefaultEffortInstall(t)
		seeded := newRow()
		seeded.CapDefaultEffort = strPtr("high")
		params := `{"reasoning":{"effort":"high"}}`
		seeded.ParamsJSON = &params
		id := install.seed(seeded)

		if _, err := Execute(install.rt, options(nil)); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		row := install.row(id)
		if row.CapDefaultEffort != "high" {
			t.Fatalf("cap_default_effort = %q, want the stored value kept", row.CapDefaultEffort)
		}
		if row.ParamsJSON != params {
			t.Fatalf("params = %q, want the stored text untouched", row.ParamsJSON)
		}
	})

	t.Run("explicit params and a level both land, the level winning", func(t *testing.T) {
		install := newDefaultEffortInstall(t)
		id := install.seed(newRow())
		opts := options(strPtr("low"))
		opts.UpdateParams = true
		opts.ParamsJSON = `{"temperature":0.5,"reasoning":{"effort":"max"}}`

		if _, err := Execute(install.rt, opts); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		row := install.row(id)
		if row.CapDefaultEffort != "low" {
			t.Fatalf("cap_default_effort = %q, want the chosen level", row.CapDefaultEffort)
		}
		if !strings.Contains(row.ParamsJSON, `"temperature":0.5`) {
			t.Fatalf("params = %q, want the caller's params kept", row.ParamsJSON)
		}
		if strings.Contains(row.ParamsJSON, `"effort"`) {
			t.Fatalf("params = %q, want the caller's own effort removed", row.ParamsJSON)
		}
		if got := effortOf(t, row); got != "low" {
			t.Fatalf("request effort = %q, want the chosen level", got)
		}
	})

	t.Run("sibling rows keep their own levels and params", func(t *testing.T) {
		install := newDefaultEffortInstall(t)
		otherModel := newRow()
		otherModel.Model = "gpt-5-mini"
		otherModel.CapDefaultEffort = strPtr("medium")
		otherParams := `{"reasoning":{"effort":"medium"}}`
		otherModel.ParamsJSON = &otherParams
		otherID := install.seed(otherModel)

		otherEndpoint := newRow()
		otherEndpoint.BaseURL = "https://gateway.example.test/v1"
		otherEndpoint.CapDefaultEffort = strPtr("max")
		otherEndpointID := install.seed(otherEndpoint)

		rep, err := Execute(install.rt, options(strPtr("high")))
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if rep.ModelID == otherID || rep.ModelID == otherEndpointID {
			t.Fatalf("saved row = %q, want a row of its own", rep.ModelID)
		}
		if row := install.row(otherID); row.CapDefaultEffort != "medium" || row.ParamsJSON != otherParams {
			t.Fatalf("sibling row = %+v, want it untouched", row)
		}
		if row := install.row(otherEndpointID); row.CapDefaultEffort != "max" {
			t.Fatalf("sibling endpoint row = %+v, want it untouched", row)
		}
	})

	t.Run("a login-backed row is matched with no path in the input", func(t *testing.T) {
		install := newDefaultEffortInstall(t)
		seeded := LLMModelInput{
			Provider: "chatgpt", Model: "gpt-5.5", BaseURL: "https://chatgpt.com/backend-api/codex",
			APIPath: "/responses", Source: "chatgpt",
		}
		params := `{"reasoning":{"effort":"low"}}`
		seeded.ParamsJSON = &params
		id := install.seed(seeded)

		rep, err := Execute(install.rt, Options{
			Provider: "chatgpt", Model: "gpt-5.5",
			BaseURL:          "https://chatgpt.com/backend-api/codex",
			CapDefaultEffort: strPtr("high"),
		})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if rep.ModelID != id {
			t.Fatalf("ModelID = %q, want the login row %q", rep.ModelID, id)
		}
		row := install.row(id)
		if row.CapDefaultEffort != "high" {
			t.Fatalf("cap_default_effort = %q, want high", row.CapDefaultEffort)
		}
		if strings.Contains(row.ParamsJSON, `"effort"`) {
			t.Fatalf("params = %q, want the stale effort removed", row.ParamsJSON)
		}
		if row.APIPath != "/responses" {
			t.Fatalf("api_path = %q, want the display path kept", row.APIPath)
		}
	})

	t.Run("stored params that are not JSON refuse the save", func(t *testing.T) {
		install := newDefaultEffortInstall(t)
		seeded := newRow()
		garbage := `not json`
		seeded.ParamsJSON = &garbage
		id := install.seed(seeded)

		if _, err := Execute(install.rt, options(strPtr("high"))); err == nil {
			t.Fatal("Execute saved over params it could not read")
		}
		row := install.row(id)
		if row.CapDefaultEffort != "" || row.ParamsJSON != garbage {
			t.Fatalf("row = %+v, want it left alone", row)
		}
	})
}

// TestExecuteSavesProviderTheBuildDoesNotKnow pins the save-side half of the
// "configure it and it runs" invariant (owner 2026-11-19): a complete row whose
// provider the build carries no knowledge of is saved and read back, because
// knowledge is a capability reference, not a permission gate. Completeness
// still refuses.
func TestExecuteSavesProviderTheBuildDoesNotKnow(t *testing.T) {
	t.Run("an unknown provider is saved as typed", func(t *testing.T) {
		install := newDefaultEffortInstall(t)
		rep, err := Execute(install.rt, Options{
			Provider: "novendor-xyz", Model: "fake-1",
			BaseURL: "https://api.novendor.example/v1",
			APIPath: "/chat/completions", APIKey: "sk-x",
		})
		if err != nil {
			t.Fatalf("Execute(novendor-xyz): %v", err)
		}
		row := install.row(rep.ModelID)
		if row.Provider != "novendor-xyz" || row.Model != "fake-1" {
			t.Fatalf("row = %s/%s, want novendor-xyz/fake-1", row.Provider, row.Model)
		}
	})

	t.Run("a missing key still refuses the save", func(t *testing.T) {
		install := newDefaultEffortInstall(t)
		if _, err := Execute(install.rt, Options{
			Provider: "novendor-xyz", Model: "fake-1",
			BaseURL: "https://api.novendor.example/v1",
			APIPath: "/chat/completions",
		}); err == nil {
			t.Fatal("Execute saved a row without a key")
		}
	})
}
