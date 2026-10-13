package gateway

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
)

// The skill draft-and-version endpoints. The routes are registered beside the
// skill lifecycle routes in api_extra.go, once for the primary agent's layer
// set and once per project; every handler here answers for both through its
// launch context, exactly the way the rest of the skill lifecycle does.

// handleSkillVersions serves one skill's version state: the published
// versions, which is current, the draft, and the upstream update marker.
func (s *Server) handleSkillVersions(w http.ResponseWriter, r *http.Request) {
	s.handleSkillVersionsWith(agentSkillScope(), w, r)
}

func (s *Server) handleSkillVersionsWith(launch safety.ProjectContext, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("name"))
	if name == "" {
		http.NotFound(w, r)
		return
	}
	svc, err := s.skillLifecycleService(launch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	row, err := svc.SkillVersionDetail(r.Context(), name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name":      row.Name,
		"origin":    row.Origin,
		"root_path": row.RootPath,
		"state":     row.State,
	})
}

// handleSkillPublish releases a skill's draft as its next version. The answer
// carries the version number the draft became.
func (s *Server) handleSkillPublish(w http.ResponseWriter, r *http.Request) {
	s.handleSkillPublishWith(agentSkillScope(), "", w, r)
}

func (s *Server) handleSkillPublishWith(launch safety.ProjectContext, projectID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("name"))
	if name == "" {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Note string `json:"note"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	svc, err := s.skillLifecycleService(launch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	version, err := svc.PublishSkill(name, body.Note)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.publishSkillLifecycleNotify(projectID, "updated", name, map[string]any{
		"kind":    "publish",
		"version": version,
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "version": version})
}

// handleSkillDraftDiscard drops a skill's unpublished changes.
func (s *Server) handleSkillDraftDiscard(w http.ResponseWriter, r *http.Request) {
	s.handleSkillDraftDiscardWith(agentSkillScope(), "", w, r)
}

func (s *Server) handleSkillDraftDiscardWith(launch safety.ProjectContext, projectID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("name"))
	if name == "" {
		http.NotFound(w, r)
		return
	}
	svc, err := s.skillLifecycleService(launch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := svc.DiscardDraft(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.publishSkillLifecycleNotify(projectID, "updated", name, map[string]any{"kind": "discard_draft"})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

// handleSkillVersionRestore copies one published version into the skill's
// draft. replace_draft says an existing draft may be replaced.
func (s *Server) handleSkillVersionRestore(w http.ResponseWriter, r *http.Request) {
	s.handleSkillVersionRestoreWith(agentSkillScope(), w, r)
}

func (s *Server) handleSkillVersionRestoreWith(launch safety.ProjectContext, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("name"))
	if name == "" {
		http.NotFound(w, r)
		return
	}
	version, err := strconv.Atoi(strings.TrimSpace(ParamsFromContext(r.Context()).ByName("v")))
	if err != nil || version <= 0 {
		http.Error(w, "version must be a positive number", http.StatusBadRequest)
		return
	}
	var body struct {
		ReplaceDraft bool `json:"replace_draft"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	svc, err := s.skillLifecycleService(launch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := svc.RestoreVersionAsDraft(name, version, body.ReplaceDraft); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "version": version})
}

// handleSkillPull fetches a skill's upstream content into its draft, carrying
// the upstream revision so publishing records the version as an update.
func (s *Server) handleSkillPull(w http.ResponseWriter, r *http.Request) {
	s.handleSkillPullWith(agentSkillScope(), "", w, r)
}

func (s *Server) handleSkillPullWith(launch safety.ProjectContext, projectID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("name"))
	if name == "" {
		http.NotFound(w, r)
		return
	}
	var body struct {
		ReplaceDraft bool `json:"replace_draft"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	svc, err := s.skillLifecycleService(launch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := svc.PullUpdate(r.Context(), name, body.ReplaceDraft); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.publishSkillLifecycleNotify(projectID, "updated", name, map[string]any{"kind": "pull"})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

// handleSkillDiff compares two snapshots of a skill: from and to each name a
// published version ("v3") or the draft ("draft").
func (s *Server) handleSkillDiff(w http.ResponseWriter, r *http.Request) {
	s.handleSkillDiffWith(agentSkillScope(), w, r)
}

func (s *Server) handleSkillDiffWith(launch safety.ProjectContext, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("name"))
	if name == "" {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	from := strings.TrimSpace(q.Get("from"))
	to := strings.TrimSpace(q.Get("to"))
	if from == "" || to == "" {
		http.Error(w, "from and to are required (\"v<N>\" or \"draft\")", http.StatusBadRequest)
		return
	}
	svc, err := s.skillLifecycleService(launch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	files, err := svc.DiffSkill(name, from, to)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name":  name,
		"from":  from,
		"to":    to,
		"files": files,
	})
}

// handleSkillVersionDownload streams one published version as a zip, the same
// archive shape a skill download carries.
func (s *Server) handleSkillVersionDownload(w http.ResponseWriter, r *http.Request) {
	s.handleSkillVersionDownloadWith(agentSkillScope(), w, r)
}

func (s *Server) handleSkillVersionDownloadWith(launch safety.ProjectContext, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("name"))
	if name == "" {
		http.NotFound(w, r)
		return
	}
	version, err := strconv.Atoi(strings.TrimSpace(ParamsFromContext(r.Context()).ByName("v")))
	if err != nil || version <= 0 {
		http.Error(w, "version must be a positive number", http.StatusBadRequest)
		return
	}
	svc, err := s.skillLifecycleService(launch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	dir, err := svc.VersionDir(name, version)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q",
		url.PathEscape(name)+"-v"+strconv.Itoa(version)+".zip"))
	w.Header().Set("X-Skipped-Symlinks", fmt.Sprintf("%d", countSkillZipSkips(dir)))
	zw := zip.NewWriter(w)
	if err := writeSkillDirZip(zw, dir, ""); err != nil {
		_ = zw.Close()
		return
	}
	_ = zw.Close()
}

// writeSkillDraftNotPublished maps the engine's draft-toggle refusal onto the
// 400 the plan fixes for it; every other error keeps its own status.
func writeSkillDraftNotPublished(w http.ResponseWriter, err error, fallback int) {
	if errors.Is(err, skill.ErrDraftNotPublished) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Error(w, err.Error(), fallback)
}

// The project-scoped mirrors: same handlers, the route project's launch
// context, and the project's id so lifecycle notifications land in the right
// project space.
func (s *Server) handleProjectSkillVersions(w http.ResponseWriter, r *http.Request) {
	launch, ok := s.projectSkillLaunch(w, r)
	if !ok {
		return
	}
	s.handleSkillVersionsWith(launch, w, r)
}

func (s *Server) handleProjectSkillPublish(w http.ResponseWriter, r *http.Request) {
	launch, ok := s.projectSkillLaunch(w, r)
	if !ok {
		return
	}
	s.handleSkillPublishWith(launch, strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id")), w, r)
}

func (s *Server) handleProjectSkillDraftDiscard(w http.ResponseWriter, r *http.Request) {
	launch, ok := s.projectSkillLaunch(w, r)
	if !ok {
		return
	}
	s.handleSkillDraftDiscardWith(launch, strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id")), w, r)
}

func (s *Server) handleProjectSkillVersionRestore(w http.ResponseWriter, r *http.Request) {
	launch, ok := s.projectSkillLaunch(w, r)
	if !ok {
		return
	}
	s.handleSkillVersionRestoreWith(launch, w, r)
}

func (s *Server) handleProjectSkillPull(w http.ResponseWriter, r *http.Request) {
	launch, ok := s.projectSkillLaunch(w, r)
	if !ok {
		return
	}
	s.handleSkillPullWith(launch, strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id")), w, r)
}

func (s *Server) handleProjectSkillDiff(w http.ResponseWriter, r *http.Request) {
	launch, ok := s.projectSkillLaunch(w, r)
	if !ok {
		return
	}
	s.handleSkillDiffWith(launch, w, r)
}

func (s *Server) handleProjectSkillVersionDownload(w http.ResponseWriter, r *http.Request) {
	launch, ok := s.projectSkillLaunch(w, r)
	if !ok {
		return
	}
	s.handleSkillVersionDownloadWith(launch, w, r)
}
