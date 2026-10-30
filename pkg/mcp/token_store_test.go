package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type fakeOverlayStore struct {
	loaded int
	saved  int
	bytes  json.RawMessage
}

func (f *fakeOverlayStore) LoadOAuth(_ context.Context, projectKey, serverName string) (json.RawMessage, error) {
	f.loaded++
	return f.bytes, nil
}

func (f *fakeOverlayStore) SaveOAuth(_ context.Context, projectKey, serverName string, raw json.RawMessage) error {
	f.saved++
	f.bytes = raw
	return nil
}

// TestStoredOAuthDefaultToFiles pins the port's default: with no store
// installed, a server's stored overlay is the file the tree keeps — absent
// reads as "no overlay", a write lands in the tree under the namespace the
// project key selects.
func TestStoredOAuthDefaultToFiles(t *testing.T) {
	SetOAuthOverlayStore(nil)
	ctx := context.Background()
	home := t.TempDir()

	if raw, err := LoadStoredOAuth(ctx, home, "", "github"); err != nil || raw != nil {
		t.Fatalf("absent overlay = (%q, %v), want (nil, nil)", raw, err)
	}
	if err := SaveStoredOAuth(ctx, home, "", "github", json.RawMessage(`{"access_token":"g"}`)); err != nil {
		t.Fatal(err)
	}
	raw, err := LoadStoredOAuth(ctx, home, "", "github")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"access_token":"g"}` {
		t.Fatalf("roundtrip = %q", raw)
	}
	if _, err := os.Stat(filepath.Join(home, "state", "mcp-oauth", "github.json")); err != nil {
		t.Fatalf("the default backend did not write the tree: %v", err)
	}

	// A project-keyed server lands under its project's directory, not the
	// global one.
	if err := SaveStoredOAuth(ctx, home, "proj-1", "github", json.RawMessage(`{"access_token":"p"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "state", "mcp-oauth", "projects", "proj-1", "github.json")); err != nil {
		t.Fatalf("project-keyed overlay not in its namespace: %v", err)
	}
}

// TestStoredOAuthGoesThroughInstalledStore pins the installed half: once a
// store is installed, loads and saves reach it and the tree is not touched.
func TestStoredOAuthGoesThroughInstalledStore(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	fake := &fakeOverlayStore{bytes: json.RawMessage(`{"access_token":"from-store"}`)}
	SetOAuthOverlayStore(fake)
	t.Cleanup(func() { SetOAuthOverlayStore(nil) })

	raw, err := LoadStoredOAuth(ctx, home, "agent:main", "github")
	if err != nil {
		t.Fatal(err)
	}
	if fake.loaded != 1 || string(raw) != `{"access_token":"from-store"}` {
		t.Fatalf("load = (%d, %q), want the store's bytes", fake.loaded, raw)
	}

	if err := SaveStoredOAuth(ctx, home, "agent:main", "github", json.RawMessage(`{"access_token":"next"}`)); err != nil {
		t.Fatal(err)
	}
	if fake.saved != 1 || string(fake.bytes) != `{"access_token":"next"}` {
		t.Fatalf("store save = (%d, %q)", fake.saved, fake.bytes)
	}
	if _, err := os.Stat(filepath.Join(home, "state")); !os.IsNotExist(err) {
		t.Fatal("the file default ran although a store is installed")
	}
}
