package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// newHostTestServer builds a server whose forebrain home is home, with a
// project store so directory listings can report which directories are
// already projects. browse is a directory outside the home to list.
func newHostTestServer(t *testing.T, home string) (*Server, *state.ProjectStore) {
	t.Helper()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner := &run.Runner{Deps: &run.Deps{Home: home, AgentName: "main", WorkspaceRoot: filepath.Join(home, "workspace")}}
	s := &Server{
		Home:     home,
		Runner:   runner,
		Projects: state.NewProjectStore(db, "main"),
		Sessions: state.NewSessionStore(db, "main"),
	}
	return s, s.Projects
}

func hostJSON(t *testing.T, s *Server, method, target string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reader *strings.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(b))
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTPForTest(rec, req)
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec, out
}

func canonical(t *testing.T, p string) string {
	t.Helper()
	out, err := safety.CanonicalPath(p)
	if err != nil {
		t.Fatalf("canonicalize %q: %v", p, err)
	}
	return out
}

func TestHostDirsListsOnlyDirectories(t *testing.T) {
	// The home lives inside the browsed directory so the listing can show a
	// blocked row for it.
	browse := canonical(t, t.TempDir())
	home := canonical(t, filepath.Join(browse, ".forebrain"))
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b", ".x"} {
		if err := os.Mkdir(filepath.Join(browse, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(browse, "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, store := newHostTestServer(t, home)

	// A subdirectory that is already a project is reported with that project.
	if _, err := store.Create(context.Background(), state.CreateProjectInput{
		Name: "alpha", Root: filepath.Join(browse, "a"), ProjectKey: state.ProjectKeyForRoot(filepath.Join(browse, "a")),
	}); err != nil {
		t.Fatal(err)
	}

	rec, body := hostJSON(t, s, http.MethodGet, "/api/host/dirs?path="+browse, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if got := body["path"]; got != browse {
		t.Fatalf("path = %v, want %q", got, browse)
	}
	entries, _ := body["entries"].([]any)
	names := map[string]map[string]any{}
	for _, raw := range entries {
		item, _ := raw.(map[string]any)
		names[item["name"].(string)] = item
	}
	// Only directories: no f.txt. Dot directories hidden except the blocked
	// home.
	if _, ok := names["f.txt"]; ok {
		t.Fatalf("a file was listed: %v", names)
	}
	if _, ok := names[".x"]; ok {
		t.Fatalf("a non-blocked dot directory was listed: %v", names)
	}
	if len(names) != 3 {
		t.Fatalf("entries = %v, want a, b, .forebrain", names)
	}
	if blocked, _ := names[".forebrain"]["blocked"].(bool); !blocked {
		t.Fatalf(".forebrain should be blocked: %v", names[".forebrain"])
	}
	if blocked, _ := names["a"]["blocked"].(bool); blocked {
		t.Fatalf("a should not be blocked")
	}
	proj, _ := names["a"]["project"].(map[string]any)
	if proj == nil || proj["name"] != "alpha" {
		t.Fatalf("a should carry project alpha: %v", names["a"])
	}
	// Ancestors run from the current directory up to the root.
	ancestors, _ := body["ancestors"].([]any)
	if len(ancestors) == 0 || ancestors[0] != browse {
		t.Fatalf("ancestors = %v", ancestors)
	}

	// Create a new folder; a second attempt with the same name conflicts.
	rec, created := hostJSON(t, s, http.MethodPost, "/api/host/dirs", map[string]any{"parent": browse, "name": "c"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if got := created["path"]; got != canonical(t, filepath.Join(browse, "c")) {
		t.Fatalf("created path = %v", got)
	}
	if _, err := os.Stat(filepath.Join(browse, "c")); err != nil {
		t.Fatalf("folder was not created: %v", err)
	}
	rec, _ = hostJSON(t, s, http.MethodPost, "/api/host/dirs", map[string]any{"parent": browse, "name": "c"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate create: %d %s", rec.Code, rec.Body.String())
	}
	// Creating inside the forebrain home is refused.
	rec, _ = hostJSON(t, s, http.MethodPost, "/api/host/dirs", map[string]any{"parent": home, "name": "nope"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create inside home: %d %s", rec.Code, rec.Body.String())
	}
}

func TestHostTrustReadsAndWritesThreeStates(t *testing.T) {
	home := canonical(t, t.TempDir())
	dir := canonical(t, t.TempDir())
	s, _ := newHostTestServer(t, home)

	rec, body := hostJSON(t, s, http.MethodGet, "/api/host/trust?path="+dir, nil)
	if rec.Code != http.StatusOK || body["level"] != "unknown" {
		t.Fatalf("initial level: %d %v", rec.Code, body)
	}
	rec, body = hostJSON(t, s, http.MethodPost, "/api/host/trust", map[string]any{"path": dir, "trusted": true})
	if rec.Code != http.StatusOK || body["level"] != "trusted" {
		t.Fatalf("after trust: %d %v", rec.Code, body)
	}
	_, body = hostJSON(t, s, http.MethodGet, "/api/host/trust?path="+dir, nil)
	if body["level"] != "trusted" {
		t.Fatalf("read back trusted: %v", body)
	}
	// Marking untrusted wins even after a trust, and a later trust clears the
	// rejection again.
	_, body = hostJSON(t, s, http.MethodPost, "/api/host/trust", map[string]any{"path": dir, "trusted": false})
	if body["level"] != "untrusted" {
		t.Fatalf("after untrust: %v", body)
	}
	_, body = hostJSON(t, s, http.MethodGet, "/api/host/trust?path="+dir, nil)
	if body["level"] != "untrusted" {
		t.Fatalf("read back untrusted: %v", body)
	}
	_, body = hostJSON(t, s, http.MethodPost, "/api/host/trust", map[string]any{"path": dir, "trusted": true})
	if body["level"] != "trusted" {
		t.Fatalf("re-trust after untrust: %v", body)
	}
}
