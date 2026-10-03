package gateway

import (
	"archive/zip"
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	state "github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/stretchr/testify/require"
)

// writeGatewaySkill creates one skill directory with a valid SKILL.md under
// root and returns its directory.
func writeGatewaySkill(t *testing.T, root string, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(
		"---\nname: "+name+"\ndescription: "+name+" skill\n---\n\nbody\n"), 0o644))
	return dir
}

func skillDownloadURL(id string, name string) string {
	if strings.TrimSpace(id) != "" {
		return "/api/v1/projects/" + id + "/skills/" + name + "/download"
	}
	return "/api/skills/" + name + "/download"
}

func readZipNames(t *testing.T, body []byte) []string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	require.NoError(t, err)
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names
}

func TestSkillDownloadSingleIsZipWithSkillMd(t *testing.T) {
	s, home, workspace := rulesServer(t)
	writeGatewaySkill(t, filepath.Join(workspace, "skills"), "demo")
	writeGatewaySkill(t, filepath.Join(home, "skills", ".system"), "builtin-demo")

	rr := httptest.NewRecorder()
	s.handleSkillDownload(rr, withNamedParam(httptest.NewRequest(http.MethodGet, skillDownloadURL("", "builtin-demo"), nil), "name", "builtin-demo"))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, "application/zip", rr.Header().Get("Content-Type"))
	require.Contains(t, rr.Header().Get("Content-Disposition"), "builtin-demo.zip")
	names := readZipNames(t, rr.Body.Bytes())
	require.Contains(t, names, "SKILL.md")

	rr = httptest.NewRecorder()
	s.handleSkillDownload(rr, withNamedParam(httptest.NewRequest(http.MethodGet, skillDownloadURL("", "missing"), nil), "name", "missing"))
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestSkillDownloadBatchHasOneTopDirPerSkill(t *testing.T) {
	s, _, workspace := rulesServer(t)
	writeGatewaySkill(t, filepath.Join(workspace, "skills"), "one")
	writeGatewaySkill(t, filepath.Join(workspace, "skills"), "two")

	body := strings.NewReader(`{"names":["one","two"]}`)
	rr := httptest.NewRecorder()
	s.handleSkillsDownloadBatch(rr, httptest.NewRequest(http.MethodPost, "/api/skills/download", body))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	names := readZipNames(t, rr.Body.Bytes())
	require.Contains(t, names, "one/SKILL.md")
	require.Contains(t, names, "two/SKILL.md")
	require.NotContains(t, names, "SKILL.md")
}

func TestSkillDownloadBatchMissingNamesFailsWholeRequest(t *testing.T) {
	s, _, workspace := rulesServer(t)
	writeGatewaySkill(t, filepath.Join(workspace, "skills"), "one")

	body := strings.NewReader(`{"names":["one","gone"]}`)
	rr := httptest.NewRecorder()
	s.handleSkillsDownloadBatch(rr, httptest.NewRequest(http.MethodPost, "/api/skills/download", body))
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "gone")
}

func TestSkillDownloadBatchCapsNames(t *testing.T) {
	s, _, _ := rulesServer(t)
	names := make([]string, 51)
	for i := range names {
		names[i] = "skill"
	}
	var buf bytes.Buffer
	buf.WriteString(`{"names":[`)
	buf.WriteString(strings.TrimSuffix(strings.Repeat(`"skill",`, len(names)), ","))
	buf.WriteString("]}")
	rr := httptest.NewRecorder()
	s.handleSkillsDownloadBatch(rr, httptest.NewRequest(http.MethodPost, "/api/skills/download", &buf))
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestSkillListCarriesOriginEditableAndDownloadURL(t *testing.T) {
	s, home, workspace := rulesServer(t)
	writeGatewaySkill(t, filepath.Join(workspace, "skills"), "agent-skill")
	writeGatewaySkill(t, filepath.Join(home, "skills"), "shared-skill")
	writeGatewaySkill(t, filepath.Join(home, "skills", ".system"), "system-skill")

	rr := httptest.NewRecorder()
	s.handleSkillsList(rr, httptest.NewRequest(http.MethodGet, "/api/skills/", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	body := rr.Body.String()
	require.Contains(t, body, `"origin":"agent"`)
	require.Contains(t, body, `"origin":"shared"`)
	require.Contains(t, body, `"origin":"builtin"`)
	require.Contains(t, body, `"download_url":"/api/skills/agent-skill/download"`)
	// The global listing belongs to the primary agent: its rows are the
	// editable ones, everything else is inherited and read-only.
	require.Contains(t, body, `"name":"agent-skill","description":"agent-skill skill","root_path":"`+filepath.Join(workspace, "skills", "agent-skill")+`","source":"workspace","trust":"workspace","enabled":true,"origin":"agent","editable":true`)
	require.Contains(t, body, `"origin":"builtin","editable":false`)
}

// A gateway started inside a checkout carries that checkout as its launch
// project; the primary-agent listing it serves is still the agent's page, so
// agent rows stay the editable ones.
func TestSkillListOwnerFollowsTheRouteNotTheLaunchProject(t *testing.T) {
	s, _, workspace := rulesServer(t)
	writeGatewaySkill(t, filepath.Join(workspace, "skills"), "agent-skill")
	s.Env.LaunchProject = safety.ProjectContext{Project: safety.Project{Root: t.TempDir(), VersionControlled: true}}

	rr := httptest.NewRecorder()
	s.handleSkillsList(rr, httptest.NewRequest(http.MethodGet, "/api/skills/", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"origin":"agent","editable":true`)
}

func TestSkillDeleteOnlyInTheOwningLayer(t *testing.T) {
	s, home, workspace := rulesServer(t)
	agentSkill := writeGatewaySkill(t, filepath.Join(workspace, "skills"), "demo")
	writeGatewaySkill(t, filepath.Join(home, "skills"), "shared-demo")
	writeGatewaySkill(t, filepath.Join(home, "skills", ".system"), "builtin-demo")

	// Not the owning layer: the shared row cannot be deleted through the
	// agent scope, and a builtin is never deletable.
	rr := httptest.NewRecorder()
	req := withNamedParam(httptest.NewRequest(http.MethodDelete, "/api/skills/shared-demo?scope=agent", nil), "name", "shared-demo")
	s.handleSkillDelete(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code)

	rr = httptest.NewRecorder()
	req = withNamedParam(httptest.NewRequest(http.MethodDelete, "/api/skills/builtin-demo?scope=shared", nil), "name", "builtin-demo")
	s.handleSkillDelete(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
	require.DirExists(t, filepath.Join(home, "skills", ".system", "builtin-demo"))

	// Owning layer: the agent row goes away and the listing follows.
	rr = httptest.NewRecorder()
	req = withNamedParam(httptest.NewRequest(http.MethodDelete, "/api/skills/demo?scope=agent", nil), "name", "demo")
	s.handleSkillDelete(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.NoDirExists(t, agentSkill)

	rr = httptest.NewRecorder()
	req = withNamedParam(httptest.NewRequest(http.MethodDelete, "/api/skills/x?scope=agent", nil), "name", "../../etc")
	s.handleSkillDelete(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestSkillInstallUploadInstallsAndRejects(t *testing.T) {
	s, home, workspace := rulesServer(t)
	zipBodyFor := func(dest string) func(t *testing.T, entries map[string]string) (*bytes.Buffer, string) {
		return func(t *testing.T, entries map[string]string) (*bytes.Buffer, string) {
			t.Helper()
			var body bytes.Buffer
			form := multipart.NewWriter(&body)
			require.NoError(t, form.WriteField("dest_scope", dest))
			part, err := form.CreateFormFile("file", "demo.zip")
			require.NoError(t, err)
			zw := zip.NewWriter(part)
			for name, content := range entries {
				w, err := zw.Create(name)
				require.NoError(t, err)
				_, err = w.Write([]byte(content))
				require.NoError(t, err)
			}
			require.NoError(t, zw.Close())
			require.NoError(t, form.Close())
			return &body, form.FormDataContentType()
		}
	}
	zipBody := zipBodyFor("workspace")
	zipBodyShared := zipBodyFor("global")

	body, contentType := zipBody(t, map[string]string{
		"demo/SKILL.md": "---\nname: demo\ndescription: uploaded\n---\n\nbody\n",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/skills/install/upload", body)
	req.Header.Set("Content-Type", contentType)
	rr := httptest.NewRecorder()
	s.handleSkillsInstallUpload(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"installed":["demo"]`)
	require.FileExists(t, filepath.Join(workspace, "skills", "demo", "SKILL.md"))

	// Same name again: conflict, and the first install is untouched.
	body, contentType = zipBody(t, map[string]string{
		"demo/SKILL.md": "---\nname: demo\ndescription: overwritten\n---\n\nbody\n",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/skills/install/upload", body)
	req.Header.Set("Content-Type", contentType)
	rr = httptest.NewRecorder()
	s.handleSkillsInstallUpload(rr, req)
	require.Equal(t, http.StatusConflict, rr.Code)

	// rar is stated as unsupported, with the media-type status saying so.
	var body2 bytes.Buffer
	form := multipart.NewWriter(&body2)
	require.NoError(t, form.WriteField("dest_scope", "workspace"))
	part, err := form.CreateFormFile("file", "demo.rar")
	require.NoError(t, err)
	_, err = part.Write([]byte("Rar!\x1a\x07\x00"))
	require.NoError(t, err)
	require.NoError(t, form.Close())
	req = httptest.NewRequest(http.MethodPost, "/api/skills/install/upload", &body2)
	req.Header.Set("Content-Type", form.FormDataContentType())
	rr = httptest.NewRecorder()
	s.handleSkillsInstallUpload(rr, req)
	require.Equal(t, http.StatusUnsupportedMediaType, rr.Code)

	// dest_scope must name a real destination; anything else is refused.
	var body3 bytes.Buffer
	form = multipart.NewWriter(&body3)
	require.NoError(t, form.WriteField("dest_scope", "bogus"))
	part, err = form.CreateFormFile("file", "demo.zip")
	require.NoError(t, err)
	_, err = part.Write([]byte("PK\x03\x04not-really"))
	require.NoError(t, err)
	require.NoError(t, form.Close())
	req = httptest.NewRequest(http.MethodPost, "/api/skills/install/upload", &body3)
	req.Header.Set("Content-Type", form.FormDataContentType())
	rr = httptest.NewRecorder()
	s.handleSkillsInstallUpload(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// The shared scope installs into <home>/skills.
	body4, contentType := zipBodyShared(t, map[string]string{
		"shared-demo/SKILL.md": "---\nname: shared-demo\ndescription: shared\n---\n\nbody\n",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/skills/install/upload", body4)
	req.Header.Set("Content-Type", contentType)
	rr = httptest.NewRecorder()
	s.handleSkillsInstallUpload(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.FileExists(t, filepath.Join(home, "skills", "shared-demo", "SKILL.md"))
}

// A registered project inside a larger checkout is its own project: the
// skills route must not walk up to the enclosing .git and relocate the
// project onto the checkout.
func TestProjectSkillLaunchKeepsRegisteredRoot(t *testing.T) {
	s, _, _ := rulesServer(t)
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	s.Projects = state.NewProjectStore(db, "main")

	checkout := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(checkout, ".git"), 0o755))
	inner := filepath.Join(checkout, "services", "api")
	writeGatewaySkill(t, filepath.Join(inner, ".forebrain", "skills"), "inner-skill")

	ctx := context.Background()
	created, err := s.Projects.Create(ctx, state.CreateProjectInput{Name: "inner", Root: inner})
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	s.handleProjectSkillsList(rr, withNamedParam(httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+created.ID+"/skills", nil), "id", created.ID))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"name":"inner-skill"`)
}

// The project routes must see project rows as editable and inherited rows as
// read-only, and download URLs must point at the project routes.
func TestProjectSkillListScopesRowsToTheProjectLayer(t *testing.T) {
	s, home, workspace := rulesServer(t)
	writeGatewaySkill(t, filepath.Join(workspace, "skills"), "agent-skill")
	projectRoot := t.TempDir()
	writeGatewaySkill(t, filepath.Join(projectRoot, ".forebrain", "skills"), "project-skill")
	project, err := safety.Resolve(projectRoot)
	require.NoError(t, err)
	require.NoError(t, safety.MarkTrusted(home, project))

	launch, err := safety.ResolveProjectContext(s.Home, projectRoot)
	require.NoError(t, err)
	rr := httptest.NewRecorder()
	s.handleSkillsListWith(launch, "proj-1", rr, httptest.NewRequest(http.MethodGet, "/api/v1/projects/proj-1/skills", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	body := rr.Body.String()
	require.Contains(t, body, `"origin":"project","editable":true`)
	require.Contains(t, body, `"download_url":"/api/v1/projects/proj-1/skills/project-skill/download"`)
	require.Contains(t, body, `"origin":"agent","editable":false`)
}

// workshopServer builds a server with one agent-scope skill (writable) and
// one built-in (read-only), plus a project store for session sources.
func workshopServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	s, home, workspace := rulesServer(t)
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	s.RunRT = &state.RunStore{DB: db}
	agentSkill := writeGatewaySkill(t, filepath.Join(workspace, "skills"), "workshop-skill")
	require.NoError(t, os.MkdirAll(filepath.Join(agentSkill, "evals"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentSkill, "evals", "evals.json"), []byte(`{"cases":[]}`), 0o644))
	builtin := writeGatewaySkill(t, filepath.Join(home, "skills", ".system"), "builtin-skill")
	return s, agentSkill, builtin
}

func TestWorkshopSkillFilesListReadAndWrite(t *testing.T) {
	s, agentSkill, _ := workshopServer(t)

	rr := httptest.NewRecorder()
	s.handleSkillFilesList(rr, withNamedParam(httptest.NewRequest(http.MethodGet, "/api/skills/workshop-skill/files", nil), "name", "workshop-skill"))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"path":"SKILL.md"`)
	require.Contains(t, rr.Body.String(), `"path":"evals/evals.json"`)
	require.Contains(t, rr.Body.String(), `"read_only":false`)

	rr = httptest.NewRecorder()
	s.handleSkillFileRead(rr, withNamedParam(httptest.NewRequest(http.MethodGet, "/api/skills/workshop-skill/file?path=SKILL.md", nil), "name", "workshop-skill"))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), "workshop-skill skill")

	rr = httptest.NewRecorder()
	req := withNamedParam(httptest.NewRequest(http.MethodPut, "/api/skills/workshop-skill/file?path=evals/evals.json", strings.NewReader(`{"content":"{\"cases\":[{\"id\":\"one\"}]}"}`)), "name", "workshop-skill")
	s.handleSkillFileWrite(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	raw, err := os.ReadFile(filepath.Join(agentSkill, "evals", "evals.json"))
	require.NoError(t, err)
	require.JSONEq(t, `{"cases":[{"id":"one"}]}`, string(raw))
}

func TestWorkshopSkillFileGuards(t *testing.T) {
	s, _, _ := workshopServer(t)

	// Escape attempts are refused, not resolved.
	rr := httptest.NewRecorder()
	s.handleSkillFileRead(rr, withNamedParam(httptest.NewRequest(http.MethodGet, "/api/skills/workshop-skill/file?path=../../x", nil), "name", "workshop-skill"))
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = httptest.NewRecorder()
	req := withNamedParam(httptest.NewRequest(http.MethodPut, "/api/skills/workshop-skill/file?path=../escape.md", strings.NewReader(`{"content":"x"}`)), "name", "workshop-skill")
	s.handleSkillFileWrite(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// The built-in layer is read-only.
	rr = httptest.NewRecorder()
	req = withNamedParam(httptest.NewRequest(http.MethodPut, "/api/skills/builtin-skill/file?path=SKILL.md", strings.NewReader(`{"content":"edited"}`)), "name", "builtin-skill")
	s.handleSkillFileWrite(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code)
	require.Contains(t, rr.Body.String(), "read-only")

	// Unknown skill names do not resolve to anything.
	rr = httptest.NewRecorder()
	s.handleSkillFilesList(rr, withNamedParam(httptest.NewRequest(http.MethodGet, "/api/skills/missing/files", nil), "name", "missing"))
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestWorkshopExplicitSkillSelectionValidation(t *testing.T) {
	s, agentSkill, builtin := workshopServer(t)

	// A live skill directory with its discovered name resolves to what the
	// run's explicit load reads: that skill's SKILL.md, which must parse as
	// the very skill named.
	name, path, err := s.resolveExplicitSkillSelection("WORKSHOP-SKILL", agentSkill+string(filepath.Separator))
	require.NoError(t, err)
	require.Equal(t, "workshop-skill", name)
	require.Equal(t, filepath.Join(skill.CanonicalSkillPath(agentSkill), "SKILL.md"), path)
	activation, err := skill.LoadActivation(path)
	require.NoError(t, err)
	require.Equal(t, "workshop-skill", activation.Name)
	// Naming the SKILL.md itself — the slash handoff's spelling — is the
	// same selection.
	_, samePath, err := s.resolveExplicitSkillSelection("workshop-skill", filepath.Join(agentSkill, "SKILL.md"))
	require.NoError(t, err)
	require.Equal(t, path, samePath)
	// The built-in skill is part of the set too.
	_, _, err = s.resolveExplicitSkillSelection("builtin-skill", builtin)
	require.NoError(t, err)

	// An arbitrary path — or a name that disagrees with the path — is
	// refused: the model's instruction source is not client-chosen.
	_, _, err = s.resolveExplicitSkillSelection("not-the-name", agentSkill)
	require.Error(t, err)
	_, _, err = s.resolveExplicitSkillSelection("workshop-skill", "/tmp")
	require.Error(t, err)
	// Half a selection activates nothing in the run, so it is refused rather
	// than starting a turn that silently ignores it.
	_, _, err = s.resolveExplicitSkillSelection("", agentSkill)
	require.ErrorIs(t, err, errSkillSelectionIncomplete)
	_, _, err = s.resolveExplicitSkillSelection("workshop-skill", "")
	require.ErrorIs(t, err, errSkillSelectionIncomplete)
}

func TestChatSessionSourceWhitelistAndEcho(t *testing.T) {
	s, _, _ := workshopServer(t)
	s.Sessions = state.NewSessionStore(s.RunRT.DB, "main")

	create := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		s.handleChatSessionCreate(rr, httptest.NewRequest(http.MethodPost, "/api/chat/sessions", strings.NewReader(body)))
		return rr
	}
	rr := create(`{"title":"w","source":"workshop"}`)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())

	rr = create(`{"source":"secret"}`)
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// The listing carries the source so the drawer can filter it out.
	listRR := httptest.NewRecorder()
	s.handleChatSessions(listRR, httptest.NewRequest(http.MethodGet, "/api/chat/sessions", nil))
	require.Equal(t, http.StatusOK, listRR.Code)
	require.Contains(t, listRR.Body.String(), `"source":"workshop"`)
}

// The primary agent's skill routes work in no project: a gateway launched in
// a trusted checkout neither lists that checkout's skills on the agent's page
// nor lets the agent's toggles answer for them.
func TestAgentSkillRoutesLeaveTheLaunchProjectOut(t *testing.T) {
	s, home, workspace := rulesServer(t)
	writeGatewaySkill(t, filepath.Join(workspace, "skills"), "agent-skill")
	launchRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(launchRoot, ".git"), 0o755))
	writeGatewaySkill(t, filepath.Join(launchRoot, ".forebrain", "skills"), "launch-skill")
	require.NoError(t, safety.MarkTrusted(home, safety.Project{Root: launchRoot}))
	launch, err := safety.ResolveProjectContext(home, launchRoot)
	require.NoError(t, err)
	s.Env.LaunchProject = launch

	rr := httptest.NewRecorder()
	s.handleSkillsList(rr, httptest.NewRequest(http.MethodGet, "/api/skills/", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"name":"agent-skill"`)
	require.NotContains(t, rr.Body.String(), "launch-skill")

	// Turning every agent-page row off leaves the launch project's skill on.
	rr = httptest.NewRecorder()
	s.handleSkillsToggle(rr, httptest.NewRequest(http.MethodPost, "/api/skills/toggle", strings.NewReader(`{"enabled_paths":[]}`)))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	entries, err := skill.DiscoverForWorkspace(home, workspace, launchRoot)
	require.NoError(t, err)
	states := map[string]bool{}
	for _, entry := range entries {
		states[entry.Name] = entry.Enabled
	}
	require.Contains(t, states, "launch-skill")
	require.True(t, states["launch-skill"], "the agent page must not switch off a project's skill")
	require.False(t, states["agent-skill"])
}
