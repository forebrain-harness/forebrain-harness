package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

func openConfigStoreForTest(t *testing.T) (*ConfigStore, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &ConfigStore{DB: db}, ctx
}

// TestConfigStoreSectionRoundtrip pins the store's basic contract: a section
// written through UpdateSection reads back byte for byte, a section that was
// never written reads as absent, and a nil result removes the section again.
func TestConfigStoreSectionRoundtrip(t *testing.T) {
	store, ctx := openConfigStoreForTest(t)

	write := func(section string, doc string) error {
		return store.UpdateSection(ctx, section, func(cur json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(doc), nil
		})
	}

	if err := write("agents", `{"defaults":{"mcp_servers":[]}}`); err != nil {
		t.Fatalf("write agents: %v", err)
	}
	if err := write("auth.chatgpt", `{"tokens":{"access_token":"a"}}`); err != nil {
		t.Fatalf("write auth.chatgpt: %v", err)
	}

	sections, err := store.LoadSections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(sections["agents"]); got != `{"defaults":{"mcp_servers":[]}}` {
		t.Fatalf("agents roundtrip = %q", got)
	}
	if got := string(sections["auth.chatgpt"]); got != `{"tokens":{"access_token":"a"}}` {
		t.Fatalf("auth.chatgpt roundtrip = %q", got)
	}
	if _, ok := sections["hooks"]; ok {
		t.Fatal("hooks was never written, want it absent")
	}

	if err := store.UpdateSection(ctx, "agents", func(cur json.RawMessage) (json.RawMessage, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("remove agents: %v", err)
	}
	sections, err = store.LoadSections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sections["agents"]; ok {
		t.Fatal("agents removed, want the section gone")
	}
	if _, ok := sections["auth.chatgpt"]; !ok {
		t.Fatal("removing one section must leave the others alone")
	}
}

// TestConfigStoreUpdateSectionRunsReadModifyWrite pins that fn sees the value
// the previous writer left, so a counter built on UpdateSection never loses
// an increment: every writer takes the write lock up front, so the
// read-modify-write exchanges serialize rather than interleave.
func TestConfigStoreUpdateSectionRunsReadModifyWrite(t *testing.T) {
	store, ctx := openConfigStoreForTest(t)

	const writers = 8
	const rounds = 10
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				err := store.UpdateSection(ctx, "counter", func(cur json.RawMessage) (json.RawMessage, error) {
					var n int
					if len(cur) > 0 {
						if err := json.Unmarshal(cur, &n); err != nil {
							return nil, err
						}
					}
					return json.RawMessage(fmt.Sprintf("%d", n+1)), nil
				})
				if err != nil {
					t.Errorf("update: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if t.Failed() {
		t.Fatal("concurrent updates failed")
	}
	sections, err := store.LoadSections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got int
	if err := json.Unmarshal(sections["counter"], &got); err != nil {
		t.Fatal(err)
	}
	if got != writers*rounds {
		t.Fatalf("counter = %d, want %d (a read-modify-write lost a step)", got, writers*rounds)
	}
}

// TestConfigStoreUpdateSectionRollsBack pins the failure half of the
// transaction contract: a fn error leaves the section exactly as it was, and
// nothing else a half-done write touched survives either.
func TestConfigStoreUpdateSectionRollsBack(t *testing.T) {
	store, ctx := openConfigStoreForTest(t)

	if err := store.UpdateSection(ctx, "agents", func(cur json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"reviewer":"user"}`), nil
	}); err != nil {
		t.Fatalf("seed agents: %v", err)
	}

	want := errors.New("reject this write")
	if err := store.UpdateSection(ctx, "agents", func(cur json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"reviewer":"broken"}`), want
	}); !errors.Is(err, want) {
		t.Fatalf("update error = %v, want the fn error back", err)
	}

	sections, err := store.LoadSections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(sections["agents"]); got != `{"reviewer":"user"}` {
		t.Fatalf("agents after rollback = %q, want the pre-write value", got)
	}
}

// TestConfigStoreReplaceAllIsAtomic pins the import path's contract: the
// table ends up holding exactly the given sections, and a write that fails
// partway (a duplicate name here) leaves the previous contents untouched.
func TestConfigStoreReplaceAllIsAtomic(t *testing.T) {
	store, ctx := openConfigStoreForTest(t)

	if err := store.ReplaceAll(ctx, map[string]json.RawMessage{
		"agents": json.RawMessage(`{"seed":true}`),
		"hooks":  json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("first replace: %v", err)
	}
	if err := store.ReplaceAll(ctx, map[string]json.RawMessage{
		"agents":          json.RawMessage(`{"seed":false}`),
		"approval_policy": json.RawMessage(`{"mode":"never"}`),
	}); err != nil {
		t.Fatalf("second replace: %v", err)
	}
	sections, err := store.LoadSections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 2 {
		t.Fatalf("table holds %d sections, want 2 (the replaced set)", len(sections))
	}
	if got := string(sections["agents"]); got != `{"seed":false}` {
		t.Fatalf("agents after replace = %q", got)
	}
	if _, ok := sections["hooks"]; ok {
		t.Fatal("hooks was not in the replaced set, want it gone")
	}

	// A failing replace must roll back: a blank section name is refused
	// before any insert, so the table keeps its previous contents.
	if err := store.ReplaceAll(ctx, map[string]json.RawMessage{
		"  ": json.RawMessage(`{}`),
	}); err == nil {
		t.Fatal("replace with a blank section name should fail")
	}
	sections, err = store.LoadSections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(sections["agents"]); got != `{"seed":false}` {
		t.Fatalf("agents after failed replace = %q, want the rolled-back value", got)
	}
}

// TestConfigStoreCredentialRows pins the credential accessors on their own:
// absent reads as nil, a write reads back byte for byte, and the OAuth rows
// key on (project_key, server_name) so a global and a project entry with the
// same server name never collide.
func TestConfigStoreCredentialRows(t *testing.T) {
	store, ctx := openConfigStoreForTest(t)

	if raw, err := store.LoadCredential(ctx, "auth.chatgpt"); err != nil || raw != nil {
		t.Fatalf("absent credential = (%q, %v), want (nil, nil)", raw, err)
	}
	if err := store.SaveCredential(ctx, "auth.chatgpt", json.RawMessage(`{"tokens":{}}`)); err != nil {
		t.Fatal(err)
	}
	raw, err := store.LoadCredential(ctx, "auth.chatgpt")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != `{"tokens":{}}` {
		t.Fatalf("credential roundtrip = %q", got)
	}

	if err := store.SaveMCPOAuthRow(ctx, "", "github", json.RawMessage(`{"access_token":"g"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMCPOAuthRow(ctx, "proj-1", "github", json.RawMessage(`{"access_token":"p"}`)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ key, want string }{
		{"", `{"access_token":"g"}`},
		{"proj-1", `{"access_token":"p"}`},
	} {
		raw, err := store.LoadMCPOAuthRow(ctx, tc.key, "github")
		if err != nil {
			t.Fatal(err)
		}
		if got := string(raw); got != tc.want {
			t.Fatalf("oauth row (%q, github) = %q, want %q", tc.key, got, tc.want)
		}
	}
	if raw, err := store.LoadMCPOAuthRow(ctx, "proj-2", "github"); err != nil || raw != nil {
		t.Fatalf("absent oauth row = (%q, %v), want (nil, nil)", raw, err)
	}

	if err := store.SaveMCPOAuthRow(ctx, "", "github", nil); err != nil {
		t.Fatal(err)
	}
	if raw, err := store.LoadMCPOAuthRow(ctx, "", "github"); err != nil || raw != nil {
		t.Fatalf("deleted oauth row = (%q, %v), want (nil, nil)", raw, err)
	}
}

// TestConfigStoreUpdateSectionRunsValidator pins the write-side gate (plan
// 188 §1): the installed validator sees every candidate — and every removal
// — and a rejection rolls the transaction back, leaving the section exactly
// as it was, reported as *InvalidSectionError so a surface can answer 4xx.
func TestConfigStoreUpdateSectionRunsValidator(t *testing.T) {
	store, ctx := openConfigStoreForTest(t)

	if err := store.UpdateSection(ctx, "agents", func(cur json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"seed":true}`), nil
	}); err != nil {
		t.Fatalf("seed without a validator: %v", err)
	}

	reject := errors.New("would not compose")
	SetSectionValidator(func(_ context.Context, section string, candidate json.RawMessage) error {
		if section == "hooks" {
			return nil // a section this validator is silent on
		}
		if section == "cron" && candidate == nil {
			return reject // removals are validated too
		}
		if string(candidate) == `{"broken":true}` {
			return reject
		}
		return nil
	})
	t.Cleanup(func() { SetSectionValidator(nil) })

	var invalid *InvalidSectionError
	err := store.UpdateSection(ctx, "agents", func(cur json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"broken":true}`), nil
	})
	if !errors.As(err, &invalid) || !errors.Is(err, reject) {
		t.Fatalf("rejected write = %v, want *InvalidSectionError wrapping the validator's error", err)
	}
	err = store.UpdateSection(ctx, "cron", func(cur json.RawMessage) (json.RawMessage, error) {
		return nil, nil
	})
	if !errors.As(err, &invalid) {
		t.Fatalf("rejected removal = %v, want *InvalidSectionError", err)
	}

	sections, err := store.LoadSections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(sections["agents"]); got != `{"seed":true}` {
		t.Fatalf("agents after rejected write = %q, want the pre-write value", got)
	}
	if _, ok := sections["cron"]; ok {
		t.Fatal("cron was never written, want it absent")
	}

	// A section the validator accepts writes as before.
	if err := store.UpdateSection(ctx, "hooks", func(cur json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}); err != nil {
		t.Fatalf("accepted write: %v", err)
	}
}

// The install-wide model configurations' store, the sibling of ConfigStore.
func newLLMModelStoreForTest(t *testing.T) *LLMModelStore {
	t.Helper()
	db, err := OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &LLMModelStore{DB: db}
}

func boolPointer(v bool) *bool { return &v }

func int64Pointer(v int64) *int64 { return &v }

func TestLLMModelCreateListRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newLLMModelStoreForTest(t)
	want := LLMModel{
		Provider:         "OpenAI",
		Model:            "gpt-5.5",
		BaseURL:          "https://api.openai.com/v1/",
		APIPath:          "chat/completions",
		APIKey:           "sk-test",
		ParamsJSON:       `{"temperature":0.2}`,
		CapImages:        boolPointer(true),
		CapPDF:           boolPointer(false),
		CapReasoning:     boolPointer(true),
		CapContextWindow: int64Pointer(262144),
		Source:           "",
		SourcePlan:       "",
		URLVerbatim:      true,
	}
	created, err := store.Create(ctx, want)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" {
		t.Fatal("Create must assign an id")
	}
	if created.CreatedAt <= 0 || created.UpdatedAt <= 0 {
		t.Fatalf("timestamps not filled: %+v", created)
	}
	list, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("List returned %d rows, want 1", len(list))
	}
	got := list[0]
	if got.ID != created.ID {
		t.Fatalf("id = %q, want %q", got.ID, created.ID)
	}
	if got.Provider != "openai" || got.Model != "gpt-5.5" {
		t.Fatalf("identity = %q/%q, want normalized openai/gpt-5.5", got.Provider, got.Model)
	}
	if got.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("base_url = %q, want the trailing slash trimmed", got.BaseURL)
	}
	if got.APIPath != "/chat/completions" {
		t.Fatalf("api_path = %q, want the leading slash added", got.APIPath)
	}
	if got.APIKey != "sk-test" || got.ParamsJSON != want.ParamsJSON {
		t.Fatalf("key/params not stored: %+v", got)
	}
	if got.CapImages == nil || !*got.CapImages {
		t.Fatal("cap_images did not survive")
	}
	if got.CapPDF == nil || *got.CapPDF {
		t.Fatal("cap_pdf did not survive")
	}
	if got.CapVideo != nil || got.CapAudio != nil || got.CapReasoningOff != nil {
		t.Fatal("unset capability columns must read back nil")
	}
	if got.CapReasoning == nil || !*got.CapReasoning {
		t.Fatal("cap_reasoning did not survive")
	}
	if got.CapContextWindow == nil || *got.CapContextWindow != 262144 {
		t.Fatal("cap_context_window did not survive")
	}
	if got.CapMaxOutput != nil {
		t.Fatal("cap_max_output must read back nil")
	}
	if got.CapEfforts != nil {
		t.Fatal("unset reasoning levels must read back nil")
	}
	if !got.URLVerbatim {
		t.Fatal("url_verbatim did not survive")
	}
}

func TestLLMModelEffortsTriState(t *testing.T) {
	ctx := context.Background()
	store := newLLMModelStoreForTest(t)

	// nil stays nil: catalog decides the levels.
	none, err := store.Create(ctx, LLMModel{Provider: "p1", Model: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	gotNone, err := store.Get(ctx, none.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotNone.CapEfforts != nil {
		t.Fatalf("nil levels read back %#v", gotNone.CapEfforts)
	}

	// An empty slice means "explicitly no levels", not "use the catalog".
	empty, err := store.Create(ctx, LLMModel{Provider: "p2", Model: "m2", CapEfforts: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	gotEmpty, err := store.Get(ctx, empty.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotEmpty.CapEfforts == nil || len(gotEmpty.CapEfforts) != 0 {
		t.Fatalf("empty levels read back %#v, want non-nil empty", gotEmpty.CapEfforts)
	}

	levels, err := store.Create(ctx, LLMModel{Provider: "p3", Model: "m3", CapEfforts: []string{"low", "high"}})
	if err != nil {
		t.Fatal(err)
	}
	gotLevels, err := store.Get(ctx, levels.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotLevels.CapEfforts) != 2 || gotLevels.CapEfforts[0] != "low" || gotLevels.CapEfforts[1] != "high" {
		t.Fatalf("levels read back %#v", gotLevels.CapEfforts)
	}
}

func TestLLMModelSameModelDifferentEndpoints(t *testing.T) {
	ctx := context.Background()
	store := newLLMModelStoreForTest(t)
	first, err := store.Create(ctx, LLMModel{Provider: "openai", Model: "gpt-5.5", BaseURL: "https://direct.example", APIPath: "/v1/chat/completions"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(ctx, LLMModel{Provider: "openai", Model: "gpt-5.5", BaseURL: "https://proxy.example", APIPath: "/v1/chat/completions"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("two endpoints of one model must be two rows")
	}
	if got, err := store.Get(ctx, second.ID); err != nil || got.BaseURL != "https://proxy.example" {
		t.Fatalf("second row = %+v, %v", got, err)
	}
}

func TestLLMModelSameModelDifferentProviders(t *testing.T) {
	ctx := context.Background()
	store := newLLMModelStoreForTest(t)
	for _, provider := range []string{"vendor-a", "vendor-b"} {
		if _, err := store.Create(ctx, LLMModel{Provider: provider, Model: "shared-model"}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("List returned %d rows, want 2", len(list))
	}
}

func TestLLMModelDuplicateIdentityRejected(t *testing.T) {
	ctx := context.Background()
	store := newLLMModelStoreForTest(t)
	first, err := store.Create(ctx, LLMModel{Provider: "openai", Model: "gpt-5.5", BaseURL: "https://api.openai.com", APIPath: "/v1/chat"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Create(ctx, LLMModel{Provider: "openai", Model: "gpt-5.5", BaseURL: "https://api.openai.com", APIPath: "/v1/chat"})
	var exists ErrLLMModelExists
	if !errors.As(err, &exists) {
		t.Fatalf("err = %v, want ErrLLMModelExists", err)
	}
	if exists.ID != first.ID {
		t.Fatalf("conflict id = %q, want the first row %q", exists.ID, first.ID)
	}
}

func TestLLMModelNormalizedIdentityCollides(t *testing.T) {
	ctx := context.Background()
	store := newLLMModelStoreForTest(t)
	first, err := store.Create(ctx, LLMModel{Provider: "OpenAI", Model: "gpt-5.5", BaseURL: "https://api.example.com/", APIPath: "v1/messages"})
	if err != nil {
		t.Fatal(err)
	}
	// Same identity written differently: provider case, trailing slashes and a
	// missing leading slash.
	_, err = store.Create(ctx, LLMModel{Provider: " openai ", Model: "gpt-5.5", BaseURL: "https://api.example.com//", APIPath: "/v1/messages/"})
	var exists ErrLLMModelExists
	if !errors.As(err, &exists) || exists.ID != first.ID {
		t.Fatalf("err = %v (exists %+v), want the first row's id %q", err, exists, first.ID)
	}
}

func TestLLMModelModelCaseIsPartOfTheIdentity(t *testing.T) {
	ctx := context.Background()
	store := newLLMModelStoreForTest(t)
	if _, err := store.Create(ctx, LLMModel{Provider: "longcat", Model: "LongCat-2.0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, LLMModel{Provider: "longcat", Model: "longcat-2.0"}); err != nil {
		t.Fatalf("model names differing only by case must both be accepted: %v", err)
	}
}

func TestLLMModelUpdateKeepsCreationAndRefusesConflicts(t *testing.T) {
	ctx := context.Background()
	store := newLLMModelStoreForTest(t)
	first, err := store.Create(ctx, LLMModel{Provider: "one", Model: "m", APIKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(ctx, LLMModel{Provider: "two", Model: "m", APIKey: "k2"})
	if err != nil {
		t.Fatal(err)
	}

	updated := first
	updated.APIKey = "rotated"
	updated.CapImages = boolPointer(true)
	updated.CreatedAt = 1 // a caller cannot move the creation time
	if _, err := store.Update(ctx, updated); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CreatedAt != first.CreatedAt {
		t.Fatalf("created_at moved: %d → %d", first.CreatedAt, got.CreatedAt)
	}
	if got.APIKey != "rotated" || got.CapImages == nil || !*got.CapImages {
		t.Fatalf("update did not land: %+v", got)
	}

	ontoSecond := got
	ontoSecond.Provider = second.Provider
	if _, err := store.Update(ctx, ontoSecond); err == nil {
		t.Fatal("moving a row onto another row's identity must fail")
	} else {
		var exists ErrLLMModelExists
		if !errors.As(err, &exists) || exists.ID != second.ID {
			t.Fatalf("err = %v, want a conflict naming %q", err, second.ID)
		}
	}

	if _, err := store.Update(ctx, LLMModel{ID: "llm-missing", Provider: "one", Model: "m"}); !errors.Is(err, ErrLLMModelNotFound) {
		t.Fatalf("update of a missing row = %v, want ErrLLMModelNotFound", err)
	}
}

func TestLLMModelUniqueIndexGuardsDirectInsert(t *testing.T) {
	ctx := context.Background()
	store := newLLMModelStoreForTest(t)
	if _, err := store.Create(ctx, LLMModel{Provider: "openai", Model: "gpt-5.5", BaseURL: "https://api.example.com", APIPath: "/v1/chat"}); err != nil {
		t.Fatal(err)
	}
	// Bypass the store's duplicate check: the index itself must refuse.
	_, err := store.DB.ExecContext(ctx, `INSERT INTO fb_llm_models(id, provider, model, base_url, api_path, created_at, updated_at)
		VALUES ('llm-direct', 'openai', 'gpt-5.5', 'https://api.example.com', '/v1/chat', 1, 1)`)
	if err == nil {
		t.Fatal("a duplicate identity must violate the unique index")
	}
	if !dbx.IsUniqueViolation(err) {
		t.Fatalf("err = %v, want a uniqueness violation", err)
	}
}

func TestLLMModelDeleteMissing(t *testing.T) {
	ctx := context.Background()
	store := newLLMModelStoreForTest(t)
	created, err := store.Create(ctx, LLMModel{Provider: "p", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, created.ID); !errors.Is(err, ErrLLMModelNotFound) {
		t.Fatalf("Get after delete = %v, want ErrLLMModelNotFound", err)
	}
	if err := store.Delete(ctx, created.ID); !errors.Is(err, ErrLLMModelNotFound) {
		t.Fatalf("second delete = %v, want ErrLLMModelNotFound", err)
	}
}

// The operator's variables' store, the other sibling of ConfigStore.
func newEnvVarStoreForTest(t *testing.T) *EnvVarStore {
	t.Helper()
	db, err := OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &EnvVarStore{DB: db}
}

func TestEnvVarSetListMapDelete(t *testing.T) {
	ctx := context.Background()
	store := newEnvVarStoreForTest(t)
	if err := store.Set(ctx, "Z_API_TOKEN", "first"); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(ctx, "A_BASE_URL", "https://example.test"); err != nil {
		t.Fatal(err)
	}
	list, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "A_BASE_URL" || list[1].Name != "Z_API_TOKEN" {
		t.Fatalf("List = %+v, want name order", list)
	}
	for _, v := range list {
		if v.UpdatedAtMs <= 0 {
			t.Fatalf("variable %s has no timestamp", v.Name)
		}
	}
	asMap, err := store.Map(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if asMap["A_BASE_URL"] != "https://example.test" || asMap["Z_API_TOKEN"] != "first" {
		t.Fatalf("Map = %#v", asMap)
	}
	if err := store.Set(ctx, "Z_API_TOKEN", "second"); err != nil {
		t.Fatal(err)
	}
	if got := mustEnvVarValue(t, store, "Z_API_TOKEN"); got != "second" {
		t.Fatalf("overwrite = %q, want second", got)
	}
	if err := store.Delete(ctx, "A_BASE_URL"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Map(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "A_BASE_URL"); err != nil {
		t.Fatalf("deleting a missing variable must not fail: %v", err)
	}
}

func mustEnvVarValue(t *testing.T, store *EnvVarStore, name string) string {
	t.Helper()
	asMap, err := store.Map(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return asMap[name]
}

func TestEnvVarRejectsInvalidNames(t *testing.T) {
	ctx := context.Background()
	store := newEnvVarStoreForTest(t)
	for _, name := range []string{"", "1starts_with_digit", "has-dash", "has space", "has.dot", "${X}"} {
		if err := store.Set(ctx, name, "v"); !errors.Is(err, ErrEnvVarName) {
			t.Fatalf("Set(%q) = %v, want ErrEnvVarName", name, err)
		}
	}
	for _, name := range []string{"_underscore", "A1", "lower_case_9"} {
		if err := store.Set(ctx, name, "v"); err != nil {
			t.Fatalf("Set(%q) = %v, want success", name, err)
		}
	}
}
