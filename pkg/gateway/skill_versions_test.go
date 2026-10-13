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

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/stretchr/testify/require"
)

// newSkillVersionsServer builds a gateway server whose active primary agent
// works in <home>/workspaces/review, and returns that agent's skill root.
func newSkillVersionsServer(t *testing.T, home string) (*Server, string) {
	t.Helper()
	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{
		"main":   {},
		"review": {Primary: true},
	}
	require.NoError(t, os.MkdirAll(filepath.Join(home, "state"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, "state", "primary-agent.json"), []byte(`{"active":"review"}`+"\n"), 0o600))
	skillsRoot := filepath.Join(home, "workspaces", "review", "skills")
	require.NoError(t, os.MkdirAll(skillsRoot, 0o755))
	return &Server{Home: home, Env: &process.Environment{Deps: run.Deps{AppCfg: cfg}}}, skillsRoot
}

// versionRouteReq builds a request whose context carries the route params the
// handlers read, the way the router would have.
func versionRouteReq(method, target string, params map[string]string, body string) *http.Request {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	ps := Params{}
	for key, value := range params {
		ps = append(ps, Param{Key: key, Value: value})
	}
	return req.WithContext(context.WithValue(req.Context(), ParamsKey, ps))
}

// TestSkillVersionRoutesServeStatePublishDiffDownload walks the draft →
// publish → history flow the web and TUI surfaces drive: the version state,
// the publish, the diff between two versions, a version download, and the
// discard of a draft.
func TestSkillVersionRoutesServeStatePublishDiffDownload(t *testing.T) {
	home := t.TempDir()
	s, skillsRoot := newSkillVersionsServer(t, home)
	skillDir := filepath.Join(skillsRoot, "gateway-ops")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\name: gateway-ops\ndescription: runbook\n---\n\nv1 body\n"), 0o644))
	store := skill.VersionStore{Root: skillsRoot}
	_, err := store.Bootstrap("gateway-ops", skill.VersionRecord{Kind: skill.VersionKindCreated})
	require.NoError(t, err)
	draft, err := store.EnsureDraft("gateway-ops")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(draft, "SKILL.md"),
		[]byte("---\nname: gateway-ops\ndescription: runbook\n---\n\nv2 body\n"), 0o644))

	// The version state answers for the draft.
	rr := httptest.NewRecorder()
	s.handleSkillVersions(rr, versionRouteReq(http.MethodGet, "/api/skills/gateway-ops/versions",
		map[string]string{"name": "gateway-ops"}, ""))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var state struct {
		State struct {
			Current  int  `json:"current"`
			HasDraft bool `json:"has_draft"`
		} `json:"state"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &state))
	require.Equal(t, 1, state.State.Current)
	require.True(t, state.State.HasDraft)

	// Publish promotes the draft.
	rr = httptest.NewRecorder()
	s.handleSkillPublish(rr, versionRouteReq(http.MethodPost, "/api/skills/gateway-ops/publish",
		map[string]string{"name": "gateway-ops"}, `{"note":"v2"}`))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var published struct {
		Version int `json:"version"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &published))
	require.Equal(t, 2, published.Version)

	// The diff between the two versions is file-level.
	rr = httptest.NewRecorder()
	s.handleSkillDiff(rr, versionRouteReq(http.MethodGet, "/api/skills/gateway-ops/diff?from=v1&to=v2",
		map[string]string{"name": "gateway-ops"}, ""))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"path":"SKILL.md"`)

	// A version downloads as a zip.
	rr = httptest.NewRecorder()
	s.handleSkillVersionDownload(rr, versionRouteReq(http.MethodGet, "/api/skills/gateway-ops/versions/1/download",
		map[string]string{"name": "gateway-ops", "v": "1"}, ""))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, "application/zip", rr.Header().Get("Content-Type"))
	require.True(t, strings.HasPrefix(rr.Body.String(), "PK"), "body is not a zip")

	// A restored draft can be discarded.
	rr = httptest.NewRecorder()
	s.handleSkillVersionRestore(rr, versionRouteReq(http.MethodPost, "/api/skills/gateway-ops/versions/1/restore",
		map[string]string{"name": "gateway-ops", "v": "1"}, `{"replace_draft":true}`))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	rr = httptest.NewRecorder()
	s.handleSkillDraftDiscard(rr, versionRouteReq(http.MethodPost, "/api/skills/gateway-ops/draft/discard",
		map[string]string{"name": "gateway-ops"}, ""))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
}

// TestSkillListCarriesVersionStateAndDraftRows pins the listing contract: a
// published row carries its version and state, and a skill that exists only
// as a draft gets a row of its own.
func TestSkillListCarriesVersionStateAndDraftRows(t *testing.T) {
	home := t.TempDir()
	s, skillsRoot := newSkillVersionsServer(t, home)
	published := filepath.Join(skillsRoot, "gateway-ops")
	require.NoError(t, os.MkdirAll(published, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(published, "SKILL.md"),
		[]byte("---\nname: gateway-ops\ndescription: runbook\n---\n\nbody\n"), 0o644))
	store := skill.VersionStore{Root: skillsRoot}
	_, err := store.Bootstrap("gateway-ops", skill.VersionRecord{Kind: skill.VersionKindCreated})
	require.NoError(t, err)
	draftOnly, err := store.EnsureDraft("gateway-5xx")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(draftOnly, "SKILL.md"),
		[]byte("---\nname: gateway-5xx\ndescription: triage\n---\n\nbody\n"), 0o644))

	rr := httptest.NewRecorder()
	s.handleSkillsList(rr, httptest.NewRequest(http.MethodGet, "/api/skills", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var body struct {
		Installed []struct {
			Name         string `json:"name"`
			Version      int    `json:"version"`
			VersionState string `json:"state"`
			DraftOnly    bool   `json:"draft_only"`
			Description  string `json:"description"`
		} `json:"installed"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	rows := map[string]struct {
		Name         string `json:"name"`
		Version      int    `json:"version"`
		VersionState string `json:"state"`
		DraftOnly    bool   `json:"draft_only"`
		Description  string `json:"description"`
	}{}
	for _, item := range body.Installed {
		rows[item.Name] = item
	}
	require.Contains(t, rows, "gateway-ops")
	require.Equal(t, 1, rows["gateway-ops"].Version)
	require.Equal(t, skill.SkillStatePublished, rows["gateway-ops"].VersionState)
	require.Contains(t, rows, "gateway-5xx")
	require.True(t, rows["gateway-5xx"].DraftOnly)
	require.Equal(t, skill.SkillStateDraft, rows["gateway-5xx"].VersionState)
	require.Equal(t, "triage", rows["gateway-5xx"].Description)
}

// TestSkillToggleRefusesDraftOnlySkill pins the 400: a draft cannot be
// enabled, because no session could load it.
func TestSkillToggleRefusesDraftOnlySkill(t *testing.T) {
	home := t.TempDir()
	s, skillsRoot := newSkillVersionsServer(t, home)
	draftOnly, err := (skill.VersionStore{Root: skillsRoot}).EnsureDraft("gateway-5xx")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(draftOnly, "SKILL.md"),
		[]byte("---\nname: gateway-5xx\ndescription: triage\n---\n\nbody\n"), 0o644))

	rr := httptest.NewRecorder()
	s.handleSkillsToggle(rr, httptest.NewRequest(http.MethodPost, "/api/skills/toggle",
		strings.NewReader(`{"enabled_paths":["`+draftOnly+`"]}`)))
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), "publish it first")
}

// TestWorkspaceTreeHidesVersionStore pins the tree rule: .versions never
// lists, the way .git never has.
func TestWorkspaceTreeHidesVersionStore(t *testing.T) {
	home := t.TempDir()
	s, skillsRoot := newSkillVersionsServer(t, home)
	require.NoError(t, os.MkdirAll(filepath.Join(skillsRoot, ".versions", "gateway-ops", "v1"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillsRoot, ".versions", "gateway-ops", "manifest.json"),
		[]byte("{}"), 0o644))

	sid := scopeServerToRoot(t, s, home, filepath.Join(home, "workspaces", "review"))
	// The tree's path parameter is relative to the session's project root.
	rr := getWorkspaceTree(t, s, sid, "skills")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.NotContains(t, rr.Body.String(), ".versions")
}
