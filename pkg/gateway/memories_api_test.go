package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	state "github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestMemoriesResetRequiresStoreAndLeavesFilesUntouched(t *testing.T) {
	home := t.TempDir()
	marker := filepath.Join(home, memory.MemoryDirName, "MEMORY.md")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{Home: home}
	recorder := httptest.NewRecorder()
	server.handleMemoriesReset(recorder, httptest.NewRequest(http.MethodPost, "/memories/reset", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if body, err := os.ReadFile(marker); err != nil || string(body) != "keep" {
		t.Fatalf("memory file changed: body=%q err=%v", body, err)
	}
}

func TestMemoriesResetClearsStoreAndFiles(t *testing.T) {
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := memory.NewStore(db, "main")
	// Memories are per primary agent, so the reset resolves the calling agent's
	// own root rather than a home-derived one shared by every tenant.
	const projectKey = "-Test-project"
	runner := &run.Runner{Deps: &run.Deps{Home: home, WorkspaceRoot: filepath.Join(home, "workspace"), ProjectKey: projectKey}}
	roots, err := memory.ResolveRootsForAgent(runner.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	projectRoot := roots.Scope(memory.Scope{Kind: memory.ScopeProject, Key: projectKey})
	marker := filepath.Join(projectRoot.MemoryRoot, "MEMORY.md")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("clear"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{Home: home, MemoryStore: store, Runner: runner}
	recorder := httptest.NewRecorder()
	// The default reset (no ?scope=all) clears only the calling session's own
	// project — the common "forget what you learned about this repo" ask.
	server.handleMemoriesReset(recorder, httptest.NewRequest(http.MethodPost, "/memories/reset", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	entries, err := os.ReadDir(projectRoot.MemoryRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("project memory root entries=%#v err=%v", entries, err)
	}
}

func TestMemoriesSettingsValidRequestUpdatesConfig(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "forebrain.yaml")
	if err := os.WriteFile(configPath, []byte("features:\n  memories: true\nmemories:\n  use_memories: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := &process.Environment{Deps: run.Deps{AppCfg: &appcfg.Root{}}, Root: home, ConfigPath: configPath}
	server := &Server{Home: home, Env: env}
	recorder := httptest.NewRecorder()
	server.handleMemoriesSettings(recorder, httptest.NewRequest(http.MethodPost, "/memories/settings", strings.NewReader(`{"use_memories":false}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	loaded, err := appcfg.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.EffectiveMemories().UseMemories {
		t.Fatal("valid request did not update use_memories")
	}
}

func TestMemoriesSettingsStrictJSONAndStorePrecondition(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "forebrain.yaml")
	original := "features:\n  memories: true\nmemories:\n  generate_memories: true\n"
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{Home: home, Env: &process.Environment{Root: home, ConfigPath: configPath}}
	for _, body := range []string{`{"unknown":true}`, `{} {}`} {
		recorder := httptest.NewRecorder()
		server.handleMemoriesSettings(recorder, httptest.NewRequest(http.MethodPost, "/memories/settings", strings.NewReader(body)))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body=%q status=%d response=%q", body, recorder.Code, recorder.Body.String())
		}
	}
	recorder := httptest.NewRecorder()
	server.handleMemoriesSettings(recorder, httptest.NewRequest(http.MethodPost, "/memories/settings", strings.NewReader(`{"thread_id":"thread","generate_memories":false}`)))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing store status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != original {
		t.Fatalf("config changed before store precondition: %q", body)
	}
}

// memoriesFilesServer builds a server with a real memory store, FTS index and
// one registered project, plus the roots its two scopes live at.
func memoriesFilesServer(t *testing.T) (*Server, memory.Root, memory.Root, string) {
	t.Helper()
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	const projectKey = "-Test-memfiles"
	runner := &run.Runner{Deps: &run.Deps{Home: home, WorkspaceRoot: filepath.Join(home, "workspace"), ProjectKey: projectKey}}
	roots, err := memory.ResolveRootsForAgent(runner.StateRoot())
	require.NoError(t, err)
	globalRoot := roots.Scope(memory.GlobalScope())
	projectRoot := roots.Scope(memory.Scope{Kind: memory.ScopeProject, Key: projectKey})
	require.NoError(t, os.MkdirAll(globalRoot.MemoryRoot, 0o755))
	require.NoError(t, os.MkdirAll(projectRoot.MemoryRoot, 0o755))

	store := memory.NewStore(db, "main")
	server := &Server{
		Home:        home,
		MemoryStore: store,
		Runner:      runner,
		Projects:    state.NewProjectStore(db, "main"),
	}
	project, err := server.Projects.Create(context.Background(), state.CreateProjectInput{
		Name: "memfiles", Root: t.TempDir(), ProjectKey: projectKey,
	})
	require.NoError(t, err)
	return server, globalRoot, projectRoot, project.ID
}

func writeMemoryFile(t *testing.T, root memory.Root, rel string, content string, modTime time.Time) string {
	t.Helper()
	path := filepath.Join(root.MemoryRoot, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	if !modTime.IsZero() {
		require.NoError(t, os.Chtimes(path, modTime, modTime))
	}
	return path
}

func memoriesGet(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	s.handleMemoriesFilesList(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

func memoriesFileGet(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	s.handleMemoriesFileRead(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

func memoriesFilePut(t *testing.T, s *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	s.handleMemoriesFileWrite(rr, req)
	return rr
}

func TestMemoriesFilesListPaginatesAndSorts(t *testing.T) {
	s, global, _, _ := memoriesFilesServer(t)
	base := time.Now().Add(-time.Hour)
	writeMemoryFile(t, global, "MEMORY.md", "# core", base)
	writeMemoryFile(t, global, "notes/older.md", "older", base.Add(time.Minute))
	writeMemoryFile(t, global, "notes/newest.md", "newest", base.Add(2*time.Minute))

	rr := memoriesGet(t, s, "/api/memories/files?scope=global&page_size=2")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	body := rr.Body.String()
	require.Contains(t, body, `"total":3`)
	require.Contains(t, body, `"page":1`)
	require.Contains(t, body, `"page_size":2`)
	// Default order: updated desc — the newest two files fill the first
	// page, and the second page carries what the first left out.
	require.Less(t, strings.Index(body, "notes/newest.md"), strings.Index(body, "notes/older.md"))
	require.NotContains(t, body, "MEMORY.md")

	// Ascending: the oldest two (MEMORY.md, notes/older.md) fill page one
	// and the newest file carries over to page two.
	rr = memoriesGet(t, s, "/api/memories/files?scope=global&page_size=2&page=2&sort=updated&order=asc")
	require.Equal(t, http.StatusOK, rr.Code)
	body = rr.Body.String()
	require.Contains(t, body, `"total":3`)
	require.Contains(t, body, "notes/newest.md")
	require.NotContains(t, body, "MEMORY.md")
	require.NotContains(t, body, "notes/older.md")
	// The core files carry their badge for the UI's lock column.
	rr = memoriesGet(t, s, "/api/memories/files?scope=global&page_size=50")
	require.Contains(t, rr.Body.String(), `"path":"MEMORY.md","size_bytes":6`)
	require.Contains(t, rr.Body.String(), `"core":true`)
}

func TestMemoriesFilesListSearchByContentAndName(t *testing.T) {
	s, global, _, _ := memoriesFilesServer(t)
	writeMemoryFile(t, global, "notes/in-body.md", "nothing special here except xylophone-unique-prose", time.Time{})
	writeMemoryFile(t, global, "notes/zebra-named.md", "plain prose", time.Time{})
	writeMemoryFile(t, global, "MEMORY.md", "# core", time.Time{})

	rr := memoriesGet(t, s, "/api/memories/files?scope=global&q=xylophone-unique-prose")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), "notes/in-body.md")
	require.NotContains(t, rr.Body.String(), "zebra-named.md")

	// A keyword that only appears in a file's own name still finds the file —
	// FTS sees inside files, the name match keeps name-only hits reachable.
	rr = memoriesGet(t, s, "/api/memories/files?scope=global&q=zebra-named")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "notes/zebra-named.md")
}

func TestMemoriesFileReadGuards(t *testing.T) {
	s, global, _, _ := memoriesFilesServer(t)
	writeMemoryFile(t, global, "MEMORY.md", "# core content", time.Time{})
	outside := writeMemoryFile(t, memory.Root{MemoryRoot: t.TempDir()}, "secret.md", "x", time.Time{})
	require.NoError(t, os.Symlink(outside, filepath.Join(global.MemoryRoot, "linked.md")))

	rr := memoriesFileGet(t, s, "/api/memories/file?scope=global&path=MEMORY.md")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), "# core content")
	require.Contains(t, rr.Body.String(), `"core":true`)

	rr = memoriesFileGet(t, s, "/api/memories/file?scope=global&path=..%2F..%2Fsecret.md")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = memoriesFileGet(t, s, "/api/memories/file?scope=global&path=linked.md")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestMemoriesFileWriteWhitelistAndGuards(t *testing.T) {
	s, global, _, _ := memoriesFilesServer(t)

	rr := memoriesFilePut(t, s, "/api/memories/file?scope=global&path=notes/fresh.md", `{"content":"new note"}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	raw, err := os.ReadFile(filepath.Join(global.MemoryRoot, "notes", "fresh.md"))
	require.NoError(t, err)
	require.Equal(t, "new note", string(raw))

	// Anything outside the memory-prose whitelist cannot be created here.
	rr = memoriesFilePut(t, s, "/api/memories/file?scope=global&path=notes/run.sh", `{"content":"#!/bin/sh"}`)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.NoFileExists(t, filepath.Join(global.MemoryRoot, "notes", "run.sh"))

	// Overwriting an existing file needs no whitelist — it is already memory.
	writeMemoryFile(t, global, "MEMORY.md", "# before", time.Time{})
	rr = memoriesFilePut(t, s, "/api/memories/file?scope=global&path=MEMORY.md", `{"content":"# after"}`)
	require.Equal(t, http.StatusOK, rr.Code)
	raw, err = os.ReadFile(filepath.Join(global.MemoryRoot, "MEMORY.md"))
	require.NoError(t, err)
	require.Equal(t, "# after", string(raw))

	rr = memoriesFilePut(t, s, "/api/memories/file?scope=global&path=../escape.md", `{"content":"x"}`)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestMemoriesFilesDeleteRefusesCoreAndEscapes(t *testing.T) {
	s, global, _, _ := memoriesFilesServer(t)
	writeMemoryFile(t, global, "MEMORY.md", "# core", time.Time{})
	writeMemoryFile(t, global, "notes/disposable.md", "bye", time.Time{})

	post := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/memories/files/delete?scope=global", strings.NewReader(body))
		s.handleMemoriesFilesDelete(rr, req)
		return rr
	}
	rr := post(`{"paths":["MEMORY.md"]}`)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "core memory files can be edited or cleared, not deleted")
	require.FileExists(t, filepath.Join(global.MemoryRoot, "MEMORY.md"))

	rr = post(`{"paths":["notes/disposable.md","../escape.md"]}`)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"deleted":1`)
	require.NoFileExists(t, filepath.Join(global.MemoryRoot, "notes", "disposable.md"))

	rr = post(`{"paths":[]}`)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestMemoriesFilesProjectScopeBoundaries(t *testing.T) {
	s, _, projectRoot, projectID := memoriesFilesServer(t)
	writeMemoryFile(t, projectRoot, "project-only.md", "mine", time.Time{})

	rr := memoriesGet(t, s, "/api/memories/files?scope=project&project_id="+projectID)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), "project-only.md")

	// Another agent's project is not addressable from this one.
	db2 := s.MemoryStore.DB
	other, err := state.NewProjectStore(db2, "someone-else").Create(context.Background(), state.CreateProjectInput{Name: "theirs", Root: t.TempDir(), ProjectKey: "-Test-theirs"})
	require.NoError(t, err)
	rr = memoriesGet(t, s, "/api/memories/files?scope=project&project_id="+other.ID)
	require.Equal(t, http.StatusNotFound, rr.Code)

	// A project with no independent memory scope has nothing to list.
	empty, err := s.Projects.Create(context.Background(), state.CreateProjectInput{Name: "nokey", Root: t.TempDir()})
	require.NoError(t, err)
	rr = memoriesGet(t, s, "/api/memories/files?scope=project&project_id="+empty.ID)
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = memoriesGet(t, s, "/api/memories/files")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestMemoriesResetGlobalAndProjectScopes(t *testing.T) {
	s, globalRoot, projectRoot, projectID := memoriesFilesServer(t)
	writeMemoryFile(t, globalRoot, "MEMORY.md", "# global", time.Time{})
	writeMemoryFile(t, projectRoot, "MEMORY.md", "# project", time.Time{})

	reset := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/memories/reset", strings.NewReader(body))
		s.handleMemoriesReset(rr, req)
		return rr
	}

	// scope=global clears only the agent's cross-project memory.
	rr := reset(`{"scope":"global"}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"scope":"global"`)
	entries, err := os.ReadDir(globalRoot.MemoryRoot)
	require.NoError(t, err)
	require.Empty(t, entries)
	require.FileExists(t, filepath.Join(projectRoot.MemoryRoot, "MEMORY.md"))

	// scope=project clears the named project; the caller's own project key
	// is what the memory engine keys on, not the route's session.
	rr = reset(`{"scope":"project","project_id":"` + projectID + `"}`)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"scope":"project"`)
	entries, err = os.ReadDir(projectRoot.MemoryRoot)
	require.NoError(t, err)
	require.Empty(t, entries)

	// Malformed asks are refused, not guessed.
	for _, body := range []string{`{"scope":"bogus"}`, `{"scope":"project"}`, `{"unknown":1}`} {
		rr = reset(body)
		require.Equal(t, http.StatusBadRequest, rr.Code, body)
	}
}
