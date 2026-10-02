package gateway

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// The skill download, delete and offline-upload handlers, plus the origin
// classifier the three skill pages share. The routes themselves are
// registered in api_extra.go's skills group.

// skillOrigin maps a listed skill's engine source and path onto the layer the
// web surfaces name it by: project, agent (the primary agent's workspace),
// shared (<home>/skills), builtin (<home>/skills/.system) or cross-tool (the
// user-level directories other agents also read). The engine's Source stays
// the fact; this is the one place that turns it into the UI's vocabulary.
func skillOrigin(homeDir string, item skill.SkillDTO) string {
	rootPath := strings.TrimSpace(item.RootPath)
	switch strings.TrimSpace(item.Source) {
	case string(skill.SourceProject):
		return "project"
	case string(skill.SourceWorkspace):
		return "agent"
	case string(skill.SourceLocal):
		return "cross-tool"
	case string(skill.SourceGlobal):
		if skillPathWithin(rootPath, filepath.Join(strings.TrimSpace(homeDir), "skills", ".system")) {
			return "builtin"
		}
		return "shared"
	default:
		return "shared"
	}
}

// skillPathWithin compares canonical paths: a skill directory reached through
// a symlinked root is still the same directory.
func skillPathWithin(path string, prefix string) bool {
	path = skill.CanonicalSkillPath(path)
	prefix = skill.CanonicalSkillPath(prefix)
	if path == "" || prefix == "" {
		return false
	}
	if path == prefix {
		return true
	}
	rel, err := filepath.Rel(prefix, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// skillDownloadBase is the URL prefix a listed skill's download link carries:
// the global routes, or the project-scoped ones when the listing belongs to a
// project.
func skillDownloadBase(projectID string) string {
	if strings.TrimSpace(projectID) != "" {
		return "/api/v1/projects/" + url.PathEscape(strings.TrimSpace(projectID)) + "/skills"
	}
	return "/api/skills"
}

// skillListEntry is one row of the skill listing responses: the engine's DTO
// plus the layer vocabulary the three pages render by.
type skillListEntry struct {
	skill.SkillDTO
	Origin      string   `json:"origin"`
	Editable    bool     `json:"editable"`
	DownloadURL string   `json:"download_url"`
	Shadows     []string `json:"shadows,omitempty"`
	ShadowedBy  []string `json:"shadowed_by,omitempty"`
}

// decorateSkillList attaches origin, editable, download_url and the shadow
// annotations to a raw listing. editable answers "does this row belong to the
// page asking": the owner layer of a project listing is project, of the
// primary-agent listing agent — every other row is inherited and read-only
// (decision D9).
func (s *Server) decorateSkillList(launch safety.ProjectContext, projectID string, svc *skill.Service, list []skill.SkillDTO) []skillListEntry {
	owner := "agent"
	if strings.TrimSpace(launch.Project.Root) != "" {
		owner = "project"
	}
	base := skillDownloadBase(projectID)
	// Shadow relations live on the discovery entries, not the managed DTO.
	shadowsByPath := make(map[string]skill.Entry)
	if entries, err := skill.DiscoverForWorkspace(svc.Home, svc.Workspace(), svc.ProjectRoot); err == nil {
		for _, entry := range entries {
			shadowsByPath[entry.Path] = entry
		}
	}
	out := make([]skillListEntry, 0, len(list))
	for _, item := range list {
		origin := skillOrigin(s.Home, item)
		row := skillListEntry{
			SkillDTO:    item,
			Origin:      origin,
			Editable:    origin == owner,
			DownloadURL: base + "/" + url.PathEscape(strings.TrimSpace(item.Name)) + "/download",
		}
		if entry, ok := shadowsByPath[filepath.Clean(strings.TrimSpace(item.RootPath))]; ok {
			row.Shadows = entry.Shadows
			row.ShadowedBy = entry.ShadowedBy
		}
		out = append(out, row)
	}
	return out
}

// decoratedSkillList is the post-write refresh every skill mutation answers
// with: the same listing shape the pages loaded in the first place.
func (s *Server) decoratedSkillList(launch safety.ProjectContext, projectID string, svc *skill.Service) ([]skillListEntry, error) {
	list, err := svc.List()
	if err != nil {
		return nil, err
	}
	return s.decorateSkillList(launch, projectID, svc, list), nil
}

// locateSkillDir finds the directory a skill name refers to inside the
// caller's effective set. Names may repeat across layers; the row the user
// sees is the unshadowed one, so that is the one downloads and deletes act
// on.
func locateSkillDir(svc *skill.Service, name string) (string, bool) {
	entries, err := skill.DiscoverForWorkspace(svc.Home, svc.Workspace(), svc.ProjectRoot)
	if err != nil {
		return "", false
	}
	target := strings.TrimSpace(name)
	first := ""
	for i := range entries {
		if !strings.EqualFold(strings.TrimSpace(entries[i].Name), target) {
			continue
		}
		if len(entries[i].ShadowedBy) == 0 {
			return entries[i].Path, true
		}
		if first == "" {
			first = entries[i].Path
		}
	}
	if first != "" {
		return first, true
	}
	return "", false
}

// countSkillZipSkips counts the entries a download zip will leave out —
// symbolic links and anything that no longer resolves inside the skill
// directory — so the response header can say so before the body starts.
func countSkillZipSkips(dir string) int {
	skipped := 0
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			skipped++
			return nil
		}
		if path == dir {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			skipped++
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			skipped++
			return nil
		}
		if !d.IsDir() {
			if _, resolveErr := tool.ResolveWithinRoots(path, []string{dir}); resolveErr != nil {
				skipped++
			}
		}
		return nil
	})
	return skipped
}

// writeSkillDirZip streams one skill directory into a zip writer. prefix is
// the path inside the archive: empty for a single-skill download (the skill
// directory is the archive root, which is the layout the offline installer
// accepts back), "<name>/" for a batch.
func writeSkillDirZip(zw *zip.Writer, dir string, prefix string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		// A symbolic link inside a skill directory is a path chosen by
		// whoever wrote the skill; packing it would ship a link that unpacks
		// somewhere its reader never agreed to.
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		name := prefix + filepath.ToSlash(rel)
		if d.IsDir() {
			_, err := zw.Create(name + "/")
			return err
		}
		if _, err := tool.ResolveWithinRoots(path, []string{dir}); err != nil {
			return nil
		}
		header, headerErr := zip.FileInfoHeader(info)
		if headerErr != nil {
			return headerErr
		}
		header.Name = name
		header.Method = zip.Deflate
		w, createErr := zw.CreateHeader(header)
		if createErr != nil {
			return createErr
		}
		f, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	})
}

func (s *Server) handleSkillDownload(w http.ResponseWriter, r *http.Request) {
	s.handleSkillDownloadWith(s.gatewayLaunchProject(), w, r)
}

func (s *Server) handleSkillDownloadWith(launch safety.ProjectContext, w http.ResponseWriter, r *http.Request) {
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
	dir, ok := locateSkillDir(svc, name)
	if !ok {
		http.Error(w, "skill not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", url.PathEscape(name)+".zip"))
	w.Header().Set("X-Skipped-Symlinks", fmt.Sprintf("%d", countSkillZipSkips(dir)))
	zw := zip.NewWriter(w)
	if err := writeSkillDirZip(zw, dir, ""); err != nil {
		// The headers are already out; all that is left to signal with is a
		// short body, which a truncated zip already is.
		_ = zw.Close()
		return
	}
	_ = zw.Close()
}

// skillsDownloadMaxNames caps one batch download request.
const skillsDownloadMaxNames = 50

func (s *Server) handleSkillsDownloadBatch(w http.ResponseWriter, r *http.Request) {
	s.handleSkillsDownloadBatchWith(s.gatewayLaunchProject(), w, r)
}

func (s *Server) handleSkillsDownloadBatchWith(launch safety.ProjectContext, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Names []string `json:"names"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(body.Names) == 0 {
		http.Error(w, "names required", http.StatusBadRequest)
		return
	}
	if len(body.Names) > skillsDownloadMaxNames {
		http.Error(w, fmt.Sprintf("at most %d skills per download", skillsDownloadMaxNames), http.StatusBadRequest)
		return
	}
	svc, err := s.skillLifecycleService(launch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	type picked struct {
		name string
		dir  string
	}
	pickedNames := make([]picked, 0, len(body.Names))
	seen := make(map[string]struct{}, len(body.Names))
	var missing []string
	for _, raw := range body.Names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		dir, ok := locateSkillDir(svc, name)
		if !ok {
			missing = append(missing, name)
			continue
		}
		pickedNames = append(pickedNames, picked{name: name, dir: dir})
	}
	if len(missing) > 0 {
		// All or nothing: a partial archive would look complete to whoever
		// unpacks it.
		sort.Strings(missing)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "skills not found", "missing": missing})
		return
	}
	skips := 0
	for _, item := range pickedNames {
		skips += countSkillZipSkips(item.dir)
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "skills.zip"))
	w.Header().Set("X-Skipped-Symlinks", fmt.Sprintf("%d", skips))
	zw := zip.NewWriter(w)
	for _, item := range pickedNames {
		if err := writeSkillDirZip(zw, item.dir, url.PathEscape(item.name)+"/"); err != nil {
			_ = zw.Close()
			return
		}
	}
	_ = zw.Close()
}

func (s *Server) handleSkillDelete(w http.ResponseWriter, r *http.Request) {
	s.handleSkillDeleteWith(s.gatewayLaunchProject(), "", w, r)
}

// handleSkillDeleteWith removes one skill directory from the layer that owns
// it (decision D9: a skill is managed only where it belongs). The scope query
// names the layer the caller believes the skill is in; the effective set —
// not the caller — decides which directory the name points at, and a mismatch
// is a refusal.
func (s *Server) handleSkillDeleteWith(launch safety.ProjectContext, projectID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("name"))
	if name == "" {
		http.NotFound(w, r)
		return
	}
	if err := tool.ValidateArchiveRelPath(name); err != nil {
		http.Error(w, "invalid skill name", http.StatusBadRequest)
		return
	}
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	svc, err := s.skillLifecycleService(launch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	dir, ok := locateSkillDir(svc, name)
	if !ok {
		http.Error(w, "skill not found", http.StatusNotFound)
		return
	}
	origin := skillOrigin(s.Home, skill.SkillDTO{RootPath: dir, Source: string(skill.SourceForPathWithWorkspace(svc.Home, svc.Workspace(), svc.ProjectRoot, dir))})
	layer, allowed := skillDeleteLayer(scope, svc)
	if !allowed {
		http.Error(w, "scope must be agent, shared or project", http.StatusBadRequest)
		return
	}
	if origin == "builtin" {
		http.Error(w, "built-in skills cannot be deleted", http.StatusForbidden)
		return
	}
	if origin != layer {
		http.Error(w, fmt.Sprintf("this skill belongs to the %s layer; delete it there", origin), http.StatusForbidden)
		return
	}
	resolved, err := tool.ResolveWithinRoots(dir, skillDeleteRoots(scope, svc))
	if err != nil {
		http.Error(w, "skill directory is outside its layer", http.StatusBadRequest)
		return
	}
	if err := os.RemoveAll(resolved); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := svc.Refresh(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishSkillLifecycleNotify("", "deleted", name, map[string]any{"scope": scope, "path": resolved})
	list, err := s.decoratedSkillList(launch, projectID, svc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"skills": list,
	})
}

// skillDeleteLayer maps the scope query onto the origin vocabulary and says
// whether it was one of the three layers a delete may target.
func skillDeleteLayer(scope string, svc *skill.Service) (string, bool) {
	switch scope {
	case "agent":
		return "agent", true
	case "shared":
		return "shared", true
	case "project":
		if strings.TrimSpace(svc.ProjectRoot) == "" {
			return "", false
		}
		return "project", true
	default:
		return "", false
	}
}

// skillDeleteRoots is the boundary a deleted directory must resolve inside:
// the owning layer's skills root(s).
func skillDeleteRoots(scope string, svc *skill.Service) []string {
	switch scope {
	case "agent":
		return []string{filepath.Join(svc.Workspace(), "skills")}
	case "shared":
		return []string{filepath.Join(strings.TrimSpace(svc.Home), "skills")}
	case "project":
		return skill.ProjectSkillRootsForDir(svc.ProjectRoot)
	default:
		return nil
	}
}

func (s *Server) handleSkillsInstallUpload(w http.ResponseWriter, r *http.Request) {
	s.handleSkillsInstallUploadWith(s.gatewayLaunchProject(), "", w, r)
}

// handleSkillsInstallUploadWith installs skills from an uploaded archive —
// the offline counterpart of the online install: same destination scopes,
// same listing response, no network.
func (s *Server) handleSkillsInstallUploadWith(launch safety.ProjectContext, projectID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	// The multipart envelope adds overhead around the archive bytes; the cap
	// stays one archive's worth above it.
	r.Body = http.MaxBytesReader(w, r.Body, skill.MaxOfflineUploadBytes+(4<<20))
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file required", http.StatusBadRequest)
		return
	}
	defer file.Close()
	destScopeRaw := strings.TrimSpace(r.FormValue("dest_scope"))
	var destScope skill.Scope
	switch destScopeRaw {
	case "workspace":
		destScope = skill.ScopeWorkspace
	case "global":
		destScope = skill.ScopeGlobal
	case "project":
		destScope = skill.ScopeProject
	default:
		http.Error(w, "dest_scope must be workspace, global or project", http.StatusBadRequest)
		return
	}
	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "could not read the uploaded archive", http.StatusBadRequest)
		return
	}
	svc, err := s.skillLifecycleService(launch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	names, err := svc.InstallOfflineArchive(header.Filename, data, destScope)
	if err != nil {
		switch {
		case errors.Is(err, skill.ErrUnsupportedArchive):
			http.Error(w, err.Error(), http.StatusUnsupportedMediaType)
		case errors.Is(err, skill.ErrSkillAlreadyExists):
			http.Error(w, err.Error(), http.StatusConflict)
		case errors.Is(err, skill.ErrNoSkillsInPackage), errors.Is(err, tool.ErrPathNotAllowed):
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		return
	}
	s.publishSkillLifecycleNotify("", "installed", strings.Join(names, ", "), map[string]any{
		"kind":    "offline_install",
		"dest":    destScopeRaw,
		"file":    strings.TrimSpace(header.Filename),
		"install": names,
	})
	list, err := s.decoratedSkillList(launch, projectID, svc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":    "ok",
		"installed": names,
		"skills":    list,
	})
}

// The skill workshop's file surface: list, read and write one skill
// directory's files. The workshop conversation drives the model — these
// endpoints are the panel beside it, never a general file manager: every
// path is pinned inside a directory the live skill set resolves.

// workshopFileMaxBytes is one skill file's read and write ceiling.
const workshopFileMaxBytes = 1 << 20
const workshopFilesMaxEntries = 2000

// validateExplicitSkillSelection pins a client-named skill to the live
// effective set: the path must be a discovered skill directory and the name
// must be the one discovery lists for it. Anything else is refused rather
// than passed through — an arbitrary path here would be an arbitrary
// instruction source for the model.
func (s *Server) validateExplicitSkillSelection(name, path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	svc, err := s.skillLifecycleService(s.gatewayLaunchProject())
	if err != nil {
		return err
	}
	entries, err := skill.DiscoverForWorkspace(svc.Home, svc.Workspace(), svc.ProjectRoot)
	if err != nil {
		return err
	}
	// Discovery keys directories by their canonical form; the caller's copy
	// of the path may be any spelling of the same directory.
	canonicalPath := skill.CanonicalSkillPath(strings.TrimSpace(path))
	for _, entry := range entries {
		if entry.Path != canonicalPath {
			continue
		}
		if strings.TrimSpace(name) != "" && !strings.EqualFold(entry.Name, strings.TrimSpace(name)) {
			return errSkillNameMismatch
		}
		return nil
	}
	return errSkillPathNotInSet
}

var (
	errSkillPathNotInSet = &wsSkillError{"skill_path does not name a skill in the current set"}
	errSkillNameMismatch = &wsSkillError{"skill_name does not match the skill at skill_path"}
)

type wsSkillError struct{ msg string }

func (e *wsSkillError) Error() string { return e.msg }

// workshopSkillDir resolves the skill directory a workshop file request
// names, using the same unshadowed-entry rule the skill pages list by.
func (s *Server) workshopSkillDir(launch safety.ProjectContext, name string) (string, *skill.Service, bool) {
	svc, err := s.skillLifecycleService(launch)
	if err != nil {
		return "", nil, false
	}
	dir, ok := locateSkillDir(svc, name)
	if !ok {
		return "", nil, false
	}
	return dir, svc, true
}

type workshopFileRow struct {
	Path    string `json:"path"`
	Size    int64  `json:"size_bytes"`
	ModTime int64  `json:"mod_time"`
	IsDir   bool   `json:"is_dir"`
}

func (s *Server) handleSkillFilesList(w http.ResponseWriter, r *http.Request) {
	s.handleSkillFilesListWith(s.gatewayLaunchProject(), w, r)
}

func (s *Server) handleSkillFilesListWith(launch safety.ProjectContext, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("name"))
	dir, _, ok := s.workshopSkillDir(launch, name)
	if !ok {
		http.Error(w, "skill not found", http.StatusNotFound)
		return
	}
	rows := make([]workshopFileRow, 0, 64)
	// Walk with lexical order so the tree the panel builds is stable.
	paths := []string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		if len(paths) >= workshopFilesMaxEntries {
			return filepath.SkipAll
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// A symlink is not followed and not listed: the panel shows the
			// skill's own files, not whatever a link points at.
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		paths = append(paths, rel)
		rows = append(rows, workshopFileRow{
			Path:    filepath.ToSlash(rel),
			Size:    info.Size(),
			ModTime: info.ModTime().Unix(),
			IsDir:   d.IsDir(),
		})
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Path < rows[j].Path })
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name":      name,
		"read_only": s.skillDirIsBuiltin(dir),
		"files":     rows,
	})
}

// skillDirIsBuiltin answers for a concrete directory what the origin rules
// say: only .system is the built-in layer.
func (s *Server) skillDirIsBuiltin(dir string) bool {
	return skillPathWithin(dir, filepath.Join(strings.TrimSpace(s.Home), "skills", ".system"))
}

func (s *Server) handleSkillFileRead(w http.ResponseWriter, r *http.Request) {
	s.handleSkillFileReadWith(s.gatewayLaunchProject(), w, r)
}

func (s *Server) handleSkillFileReadWith(launch safety.ProjectContext, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("name"))
	dir, _, ok := s.workshopSkillDir(launch, name)
	if !ok {
		http.Error(w, "skill not found", http.StatusNotFound)
		return
	}
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	if err := tool.ValidateArchiveRelPath(rel); err != nil {
		http.Error(w, "path must stay inside the skill directory", http.StatusBadRequest)
		return
	}
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	resolved, err := tool.ResolveWithinRoots(abs, []string{dir})
	if err != nil {
		http.Error(w, "path must stay inside the skill directory", http.StatusBadRequest)
		return
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		http.Error(w, "not a regular file", http.StatusBadRequest)
		return
	}
	if info.Size() > workshopFileMaxBytes {
		http.Error(w, "file exceeds the 1 MiB read limit", http.StatusRequestEntityTooLarge)
		return
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name":      name,
		"path":      rel,
		"content":   string(data),
		"read_only": s.skillDirIsBuiltin(dir),
	})
}

func (s *Server) handleSkillFileWrite(w http.ResponseWriter, r *http.Request) {
	s.handleSkillFileWriteWith(s.gatewayLaunchProject(), w, r)
}

func (s *Server) handleSkillFileWriteWith(launch safety.ProjectContext, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("name"))
	dir, svc, ok := s.workshopSkillDir(launch, name)
	if !ok {
		http.Error(w, "skill not found", http.StatusNotFound)
		return
	}
	if s.skillDirIsBuiltin(dir) {
		http.Error(w, "built-in skills are read-only; copy the skill to a layer you own and edit it there", http.StatusForbidden)
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, workshopFileMaxBytes+(1<<16))).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(body.Content) > workshopFileMaxBytes {
		http.Error(w, "content exceeds the 1 MiB write limit", http.StatusRequestEntityTooLarge)
		return
	}
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	if err := tool.ValidateArchiveRelPath(rel); err != nil {
		http.Error(w, "path must stay inside the skill directory", http.StatusBadRequest)
		return
	}
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	resolved, err := tool.ResolveWithinRoots(abs, []string{dir})
	if err != nil {
		http.Error(w, "path must stay inside the skill directory", http.StatusBadRequest)
		return
	}
	if info, statErr := os.Lstat(resolved); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			http.Error(w, "only regular files may be written", http.StatusBadRequest)
			return
		}
	} else if !os.IsNotExist(statErr) {
		http.Error(w, statErr.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(resolved, []byte(body.Content), 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := svc.Refresh(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "path": rel})
}
