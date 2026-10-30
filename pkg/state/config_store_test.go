package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
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
