package gateway

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// errFilesNotFound reports a file view whose root cannot be named: a
// conversation that is not this agent's, one that belongs to no project and is
// not a workshop task, a project that is not this agent's, or a request that
// named neither a session nor a project.
var errFilesNotFound = errors.New("file view not found")

// errFilesDirectoryMissing reports a file view whose root directory no longer
// exists on disk. It mirrors process.ErrSessionDirectoryMissing for the file
// endpoints, which answer the browser a 404 rather than a run's refusal.
type errFilesDirectoryMissing struct{ Dir string }

func (e errFilesDirectoryMissing) Error() string {
	return "file view directory missing: " + e.Dir
}

// filesRoot is the root of a conversation's file view: the root of the project
// the session belongs to, or — for an agent-level skill-workshop session, the
// one session without a project — the agent's skills directory. A conversation
// not started yet names its project instead of a session. Every file view
// (tree, snippet, @ mentions, @ images) and nothing else resolves its root
// here.
func (s *Server) filesRoot(ctx context.Context, sessionID, projectID string) (string, error) {
	if s == nil {
		return "", errFilesNotFound
	}
	sessionID = strings.TrimSpace(sessionID)
	projectID = strings.TrimSpace(projectID)
	switch {
	case sessionID != "":
		owned, err := s.sessionOwned(ctx, sessionID)
		if err != nil {
			return "", err
		}
		if !owned {
			return "", errFilesNotFound
		}
		// One project per session: the root is the project the session was
		// filed under at birth, never a directory some later request chose.
		project, ok, perr := s.projectStore().ProjectForSession(ctx, sessionID)
		if perr != nil {
			return "", perr
		}
		if ok {
			return existingFilesRoot(project.Root)
		}
		// No project: the only session that may still name a root is a
		// primary-agent workshop task, whose directory is the agent's own
		// skills. It is created here so a workshop conversation always has a
		// tree, the same way the pool builds its workshop runner.
		source, serr := s.sessionSource(ctx, sessionID)
		if serr != nil {
			return "", serr
		}
		if strings.EqualFold(source, state.SessionSourceWorkshop) {
			skills := filepath.Join(s.activeWorkspaceRoot(), "skills")
			if err := os.MkdirAll(skills, 0o755); err != nil {
				return "", err
			}
			return skills, nil
		}
		return "", errFilesNotFound
	case projectID != "":
		store := s.projectStore()
		if store == nil {
			return "", errFilesNotFound
		}
		project, err := store.Get(ctx, projectID)
		if err != nil {
			return "", errFilesNotFound
		}
		return existingFilesRoot(project.Root)
	default:
		return "", errFilesNotFound
	}
}

// existingFilesRoot returns a bound root that is still on disk, or the
// directory-missing refusal the file view answers a 404 with.
func existingFilesRoot(root string) (string, error) {
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return "", errFilesDirectoryMissing{Dir: root}
	}
	return root, nil
}

// sessionSource reads one conversation's purpose (fb_sessions.source): empty
// for an ordinary conversation, "workshop" for a primary-agent workshop task.
// The project store owns the session table's project binding but exposes no
// single-row source read, so it is read here under the same agent scope the
// session-ownership check used.
func (s *Server) sessionSource(ctx context.Context, sessionID string) (string, error) {
	if s == nil || s.Sessions == nil || s.Sessions.DB() == nil {
		return "", nil
	}
	var source string
	err := s.Sessions.DB().QueryRowContext(ctx,
		`SELECT COALESCE(source, '') FROM fb_sessions WHERE id = ? AND agent_id = ?`,
		strings.TrimSpace(sessionID), s.Sessions.AgentID()).Scan(&source)
	if err != nil {
		return "", err
	}
	return source, nil
}

// resolveFilesPath pins one file-view-relative path to the conversation's
// files root, resolving symlinks, and rejects anything that lands outside it.
// The root is the project root (or the workshop skills directory): a link
// pointing elsewhere is not part of this view.
//
// A path already spelled absolutely is taken as the full path (and still
// containment-checked) instead of being joined under the root. The image raw
// endpoint needs this: a tool step records an image's ABSOLUTE path, so its
// pixel request arrives absolute while the file editor speaks relative paths.
// Relative paths — every other caller — resolve exactly as before.
func (s *Server) resolveFilesPath(ctx context.Context, sessionID, projectID, rel string) (root, full string, err error) {
	root, err = s.filesRoot(ctx, sessionID, projectID)
	if err != nil {
		return "", "", err
	}
	target := filepath.FromSlash(rel)
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	resolved, resolveErr := tool.ResolveWithinRoots(target, []string{root})
	if resolveErr != nil {
		if errors.Is(resolveErr, tool.ErrPathNotAllowed) {
			return root, "", tool.ErrPathNotAllowed
		}
		return root, "", resolveErr
	}
	return root, resolved, nil
}

// writeFilesRootError maps a filesRoot failure onto the status a file-view
// endpoint answers with: an unnamed root is a 404, a root whose directory is
// gone is a 404 that names it, and anything else is a server error.
func writeFilesRootError(w http.ResponseWriter, err error) {
	var missing errFilesDirectoryMissing
	switch {
	case errors.Is(err, errFilesNotFound):
		http.Error(w, "file view not found", http.StatusNotFound)
	case errors.As(err, &missing):
		http.Error(w, "目录已不存在："+missing.Dir, http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// --- Deliverables (plan 146) ---
//
// The artifact endpoints read what present_files registered. They are the
// REST half of the run-end frames: a card lists rows from /artifacts, a
// browser opens content_url (inline) or download_url (attachment), an
// archive's entries_url lists the directory without extracting anything, and
// preview.pdf is the LibreOffice conversion of an office document. All of it
// sits behind the same gateway token the conversation itself uses.

// artifactContentBase is the URL prefix every artifact route hangs off.
const artifactContentBase = "/api/chat/sessions"

// artifactRow is one delivered file as the REST surface answers it: every
// event.Artifact field (the wire shape the run-end frames carry, re-used by
// marshalling it) plus what a client needs to act — whether the file is still
// on disk and unchanged, and the URLs to fetch it by.
func artifactRow(sessionID string, a state.Artifact) map[string]any {
	wire := event.Artifact{
		ID:          a.ID,
		SessionID:   a.SessionID,
		RunID:       a.RunID,
		ToolCallID:  a.ToolCallID,
		Name:        a.Name,
		Title:       a.Title,
		Description: a.Description,
		Path:        a.Path,
		RelPath:     a.RelPath,
		MIME:        a.MIME,
		Kind:        a.Kind,
		Preview:     a.Preview,
		Size:        a.Size,
		SHA256:      a.SHA256,
		Origin:      a.Origin,
		CreatedAt:   a.CreatedAt,
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		return map[string]any{}
	}
	row := map[string]any{}
	if err := json.Unmarshal(raw, &row); err != nil {
		return map[string]any{}
	}
	// The row registers the file; it never copies it. Reading it reports
	// whether the bytes are still the ones that were presented.
	missing := true
	changed := false
	if st, err := os.Stat(a.Path); err == nil && !st.IsDir() {
		missing = false
		changed = st.Size() != a.Size || st.ModTime().UnixNano() != a.MTimeNS
	}
	row["missing"] = missing
	row["changed"] = changed
	base := fmt.Sprintf("%s/%s/artifacts/%s", artifactContentBase, sessionID, a.ID)
	row["download_url"] = base + "/content?disposition=attachment"
	row["content_url"] = base + "/content?disposition=inline"
	if a.Preview == "office_pdf" {
		row["preview_url"] = base + "/preview.pdf"
	}
	if a.Preview == "archive" {
		row["entries_url"] = base + "/entries"
	}
	return row
}

// artifactFromRequest answers the shared prelude of every artifact route: the
// conversation must be this agent's (404), the row must exist in it (404 with
// a code), and only then does the handler act.
func (s *Server) artifactFromRequest(w http.ResponseWriter, r *http.Request) (state.Artifact, bool) {
	sid := pathParam(r, "id")
	owned, err := s.sessionOwned(r.Context(), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return state.Artifact{}, false
	}
	if !owned {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]string{"error": "session_not_found"})
		return state.Artifact{}, false
	}
	a, err := s.Sessions.GetArtifact(r.Context(), sid, pathParam(r, "aid"))
	if errors.Is(err, state.ErrArtifactNotFound) {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]string{"error": "artifact_not_found"})
		return state.Artifact{}, false
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return state.Artifact{}, false
	}
	return a, true
}

// handleSessionArtifacts answers GET /api/chat/sessions/:id/artifacts — one
// page of the conversation's delivered files in delivery order, with the
// total a pager needs. Server-side pagination with a count is the rule every
// list endpoint here follows.
func (s *Server) handleSessionArtifacts(w http.ResponseWriter, r *http.Request) {
	sid := pathParam(r, "id")
	owned, err := s.sessionOwned(r.Context(), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !owned {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]string{"error": "session_not_found"})
		return
	}
	limit := artifactListLimit(r)
	page := artifactListPage(r)
	offset := (page - 1) * limit
	rows, total, err := s.Sessions.ListArtifactsPage(r.Context(), sid, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, a := range rows {
		items = append(items, artifactRow(sid, a))
	}
	writeJSON(w, map[string]any{"items": items, "total": total})
}

// artifactListLimit clamps ?limit= to the window the list serves: 50 by
// default, 200 at most.
func artifactListLimit(r *http.Request) int {
	limit := 50
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	if limit <= 0 {
		limit = 50
	}
	return min(limit, 200)
}

// artifactListPage is the 1-based ?page= the pager sends.
func artifactListPage(r *http.Request) int {
	page := 1
	if v := strings.TrimSpace(r.URL.Query().Get("page")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			page = n
		}
	}
	return page
}

// handleSessionArtifact answers GET /api/chat/sessions/:id/artifacts/:aid —
// one delivered file.
func (s *Server) handleSessionArtifact(w http.ResponseWriter, r *http.Request) {
	a, ok := s.artifactFromRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, artifactRow(pathParam(r, "id"), a))
}

// artifactImageMIME names the image type a file's head actually shows, and ""
// when it shows none of the five the inline rule accepts. The signature, not
// the row's extension, decides: text wearing a .png name is a download.
func artifactImageMIME(head []byte) string {
	switch {
	case len(head) >= 8 && string(head[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(head) >= 3 && head[0] == 0xFF && head[1] == 0xD8 && head[2] == 0xFF:
		return "image/jpeg"
	case len(head) >= 6 && (string(head[:6]) == "GIF87a" || string(head[:6]) == "GIF89a"):
		return "image/gif"
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return "image/webp"
	case len(head) >= 2 && string(head[:2]) == "BM":
		return "image/bmp"
	}
	return ""
}

// handleSessionArtifactContent answers GET
// /api/chat/sessions/:id/artifacts/:aid/content?disposition=inline|attachment
// — the delivered file's bytes.
//
// The default is a download. Inline is earned, never claimed: only a type a
// browser draws without executing anything (or one served inside a CSP
// sandbox) is handed over in place, and each of those still proves itself —
// an image by its signature, a PDF by its header. Anything else, including a
// disguise that fails the check, downloads.
func (s *Server) handleSessionArtifactContent(w http.ResponseWriter, r *http.Request) {
	a, ok := s.artifactFromRequest(w, r)
	if !ok {
		return
	}
	f, err := os.Open(a.Path)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]string{"error": "artifact_file_missing"})
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]string{"error": "artifact_file_missing"})
		return
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	disposition := "attachment"
	ctype := a.MIME
	if attachmentWantsInline(r) {
		switch {
		case a.Preview == "image" && a.MIME == "image/svg+xml":
			// SVG is markup the browser parses, so it runs inside a sandbox
			// CSP: an opaque origin with no network and no data: images
			// beyond its own. It can never read the gateway's cookies.
			disposition = "inline"
			ctype = "image/svg+xml"
			w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:")
		case a.Preview == "image":
			if detected := artifactImageMIME(head); detected != "" {
				disposition = "inline"
				ctype = detected
			}
		case a.Preview == "pdf":
			if len(head) >= 5 && string(head[:5]) == "%PDF-" {
				disposition = "inline"
				ctype = "application/pdf"
			}
		case a.Preview == "video" || a.Preview == "audio":
			// The browser's own player draws it; Range comes free from
			// ServeContent.
			disposition = "inline"
		case a.Preview == "html":
			// Delivered HTML runs scripts the author wrote, so it lives in a
			// sandbox with scripts and popups but no same-origin access: the
			// page is an opaque origin that cannot reach the gateway's
			// cookies or its API. Same-origin access is the one sandbox
			// escape there is, and it must never be granted here.
			disposition = "inline"
			ctype = "text/html; charset=utf-8"
			w.Header().Set("Content-Security-Policy", "sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox")
		case a.Preview == "markdown" || a.Preview == "text":
			// Markdown and text are data for the client to render; the
			// gateway serves bytes, never a page of its own origin.
			disposition = "inline"
			ctype = "text/plain; charset=utf-8"
		}
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": a.Name}))
	w.Header().Set("Cache-Control", "private, max-age=60")
	http.ServeContent(w, r, a.Name, st.ModTime(), f)
}

// artifactEntry is one line of an archive's directory listing.
type artifactEntry struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Dir  bool   `json:"dir"`
}

// artifactEntriesMax caps one listing: a directory is a view, not a promise
// to stream an archive of any size.
const artifactEntriesMax = 5000

// handleSessionArtifactEntries answers GET
// /api/chat/sessions/:id/artifacts/:aid/entries — the directory of a zip or
// tar archive, read in place. This endpoint never extracts anything: names
// are listed, bytes are not written anywhere. An entry name the path guard
// would refuse is listed like any other (the listing says what is there);
// only extraction would have to reject it, and there is none.
func (s *Server) handleSessionArtifactEntries(w http.ResponseWriter, r *http.Request) {
	a, ok := s.artifactFromRequest(w, r)
	if !ok {
		return
	}
	if a.Preview != "archive" {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": "artifact_not_an_archive"})
		return
	}
	entries, truncated, err := listArtifactEntries(a.Path)
	if err != nil {
		if os.IsNotExist(err) {
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, map[string]string{"error": "artifact_file_missing"})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"entries": entries, "total": len(entries), "truncated": truncated})
}

// listArtifactEntries reads an archive's central directory or table of
// contents without decompressing payloads beyond what the format requires.
func listArtifactEntries(path string) ([]artifactEntry, bool, error) {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		zr, err := zip.OpenReader(path)
		if err != nil {
			return nil, false, err
		}
		defer zr.Close()
		out := make([]artifactEntry, 0, len(zr.File))
		truncated := false
		for _, f := range zr.File {
			if len(out) == artifactEntriesMax {
				truncated = true
				break
			}
			out = append(out, artifactEntry{Name: f.Name, Size: int64(f.UncompressedSize64), Dir: f.FileInfo().IsDir()})
		}
		return out, truncated, nil
	case strings.HasSuffix(lower, ".tar"), strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		f, err := os.Open(path)
		if err != nil {
			return nil, false, err
		}
		defer f.Close()
		var reader io.Reader = f
		if !strings.HasSuffix(lower, ".tar") {
			gz, err := gzip.NewReader(f)
			if err != nil {
				return nil, false, err
			}
			defer gz.Close()
			reader = gz
		}
		tr := tar.NewReader(reader)
		out := make([]artifactEntry, 0, 64)
		truncated := false
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, false, err
			}
			if len(out) == artifactEntriesMax {
				truncated = true
				break
			}
			out = append(out, artifactEntry{Name: hdr.Name, Size: hdr.Size, Dir: hdr.Typeflag == tar.TypeDir})
		}
		return out, truncated, nil
	}
	return nil, false, fmt.Errorf("archive listing unsupported for %s", filepath.Ext(path))
}

// --- Office previews through LibreOffice ---
//
// A presentation or legacy office document has no browser renderer, so the
// gateway converts it to PDF once per distinct content and serves the cached
// PDF inline. The conversion runs in a scratch profile of its own every time,
// so two conversions never fight over LibreOffice's user-directory lock, and
// a wedged leftover profile cannot poison the next request.

// artifactPreviewMaxSource refuses to convert a file large enough that the
// conversion would be a denial of service rather than a preview.
const artifactPreviewMaxSource = 200 << 20

// artifactPreviewSlots bounds concurrent LibreOffice processes: two is a
// whole conversion audience on a laptop and a rounding error on a server.
var artifactPreviewSlots = make(chan struct{}, 2)

// artifactPreviewWaits deduplicates concurrent conversions of the same
// content: one request converts, the rest wait and read the cache. The map
// entry is removed by whoever created it, so a later request for the same
// content starts a fresh conversion only after the first one finished.
var (
	artifactPreviewMu    sync.Mutex
	artifactPreviewWaits = map[string]*sync.WaitGroup{}
)

// artifactPreviewWait either registers this caller as the converter for sha
// (leader) or hands back the wait group a leader already holds.
func artifactPreviewWait(sha string) (wg *sync.WaitGroup, leader bool) {
	artifactPreviewMu.Lock()
	defer artifactPreviewMu.Unlock()
	if existing, ok := artifactPreviewWaits[sha]; ok {
		return existing, false
	}
	wg = &sync.WaitGroup{}
	wg.Add(1)
	artifactPreviewWaits[sha] = wg
	return wg, true
}

// artifactPreviewRelease frees the sha's wait group slot.
func artifactPreviewRelease(sha string, wg *sync.WaitGroup) {
	wg.Done()
	artifactPreviewMu.Lock()
	delete(artifactPreviewWaits, sha)
	artifactPreviewMu.Unlock()
}

// findLibreOffice names the converter binary. An explicitly configured
// FOREBRAIN_SOFFICE is authoritative — a deployment that names a path gets
// that path honored or nothing, never a surprise fallback to whatever else
// the host happens to carry.
func findLibreOffice() string {
	if env := strings.TrimSpace(getenvArtifact("FOREBRAIN_SOFFICE")); env != "" {
		if st, err := os.Stat(env); err == nil && !st.IsDir() {
			return env
		}
		return ""
	}
	if p, err := exec.LookPath("soffice"); err == nil {
		return p
	}
	if p, err := exec.LookPath("libreoffice"); err == nil {
		return p
	}
	const macApp = "/Applications/LibreOffice.app/Contents/MacOS/soffice"
	if st, err := os.Stat(macApp); err == nil && !st.IsDir() {
		return macApp
	}
	return ""
}

// getenvArtifact is a seam for tests; production reads the real environment.
var getenvArtifact = os.Getenv

// artifactPreviewCachePath is where a content hash's converted PDF lives.
func (s *Server) artifactPreviewCachePath(sha string) string {
	return filepath.Join(s.Home, "cache", "artifact-previews", sha+".pdf")
}

// hashArtifactFile streams a file through sha256: the preview cache is keyed
// by the bytes being served, so an edited file reconverts.
func hashArtifactFile(f *os.File) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// serveArtifactPDF writes a PDF inline: the browser's own viewer draws it,
// nosniff keeps the bytes from being read as a page.
func serveArtifactPDF(w http.ResponseWriter, r *http.Request, f *os.File, name string) {
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": name}))
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeContent(w, r, name, st.ModTime(), f)
}

// handleSessionArtifactPreviewPDF answers GET
// /api/chat/sessions/:id/artifacts/:aid/preview.pdf — an office document as
// PDF, converted by LibreOffice and cached by the source file's content hash.
func (s *Server) handleSessionArtifactPreviewPDF(w http.ResponseWriter, r *http.Request) {
	a, ok := s.artifactFromRequest(w, r)
	if !ok {
		return
	}
	if a.Preview != "office_pdf" {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": "artifact_not_convertible"})
		return
	}
	src, err := os.Open(a.Path)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]string{"error": "artifact_file_missing"})
		return
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil || st.IsDir() {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]string{"error": "artifact_file_missing"})
		return
	}
	if st.Size() > artifactPreviewMaxSource {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		writeJSON(w, map[string]string{"error": "preview_too_large"})
		return
	}
	sha, err := hashArtifactFile(src)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// A cached conversion serves without the converter: the PDF is already
	// on disk, and whether LibreOffice is installed today changes nothing
	// about yesterday's preview.
	cachePath := s.artifactPreviewCachePath(sha)
	if cached, err := os.Open(cachePath); err == nil {
		defer cached.Close()
		serveArtifactPDF(w, r, cached, pdfPreviewName(a.Name))
		return
	}
	soffice := findLibreOffice()
	if soffice == "" {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, map[string]string{"error": "libreoffice_unavailable"})
		return
	}

	wg, leader := artifactPreviewWait(sha)
	if !leader {
		// Someone is already converting this exact content; the cache is the
		// handoff.
		wg.Wait()
		if cached, err := os.Open(cachePath); err == nil {
			defer cached.Close()
			serveArtifactPDF(w, r, cached, pdfPreviewName(a.Name))
			return
		}
		w.WriteHeader(http.StatusBadGateway)
		writeJSON(w, map[string]string{"error": "preview_failed"})
		return
	}
	defer artifactPreviewRelease(sha, wg)

	if err := s.convertArtifactToPDF(r.Context(), soffice, a.Path, sha); err != nil {
		writeArtifactPreviewError(w, err)
		return
	}
	cached, err := os.Open(cachePath)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		writeJSON(w, map[string]string{"error": "preview_failed"})
		return
	}
	defer cached.Close()
	serveArtifactPDF(w, r, cached, pdfPreviewName(a.Name))
}

// pdfPreviewName is the download name of a converted preview: the source's
// name with a .pdf extension.
func pdfPreviewName(name string) string {
	base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	if base == "" {
		base = "preview"
	}
	return base + ".pdf"
}

// artifactPreviewError carries the status a conversion failure answers with;
// the detail rides only the 502, capped to the first stderr bytes.
type artifactPreviewError struct {
	status int
	code   string
	detail string
}

func (e *artifactPreviewError) Error() string {
	if e.detail != "" {
		return e.code + ": " + e.detail
	}
	return e.code
}

func writeArtifactPreviewError(w http.ResponseWriter, err error) {
	var ae *artifactPreviewError
	if errors.As(err, &ae) {
		w.WriteHeader(ae.status)
		body := map[string]string{"error": ae.code}
		if ae.detail != "" {
			body["detail"] = ae.detail
		}
		writeJSON(w, body)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// convertArtifactToPDF runs one conversion and installs its result in the
// cache. The source is copied to a scratch file with a plain ASCII name so
// the command line never carries the delivered file's spaces or non-ASCII
// characters, and the scratch directory is removed whatever happened.
func (s *Server) convertArtifactToPDF(ctx context.Context, soffice, sourcePath, sha string) error {
	tmp, err := os.MkdirTemp("", "artifact-preview-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(sourcePath), "."))
	if ext == "" {
		ext = "bin"
	}
	input := filepath.Join(tmp, "input."+ext)
	if err := copyFile(sourcePath, input); err != nil {
		return err
	}
	outDir := filepath.Join(tmp, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	// The profile URL must go through net/url: a concatenated path would
	// break on the first space or '#' and the profile is inside the temp dir.
	profileURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(tmp, "profile"))}).String()

	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, soffice,
		"--headless", "--norestore", "--nolockcheck", "--nodefault", "--nofirststartwizard",
		"-env:UserInstallation="+profileURL,
		"--convert-to", "pdf", "--outdir", outDir, input)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded {
			return &artifactPreviewError{status: http.StatusGatewayTimeout, code: "preview_timeout"}
		}
		return &artifactPreviewError{
			status: http.StatusBadGateway, code: "preview_failed",
			detail: firstBytes(stderr.String(), 500),
		}
	}
	produced, err := os.ReadFile(filepath.Join(outDir, "input.pdf"))
	if err != nil || len(produced) == 0 || string(produced[:5]) != "%PDF-" {
		return &artifactPreviewError{status: http.StatusBadGateway, code: "preview_failed", detail: firstBytes(stderr.String(), 500)}
	}

	cachePath := s.artifactPreviewCachePath(sha)
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return err
	}
	// Write inside the cache directory, fsync, then rename: no reader ever
	// sees a half-written PDF, and the rename never crosses filesystems.
	staged, err := os.CreateTemp(filepath.Dir(cachePath), "artifact-preview-*.pdf.tmp")
	if err != nil {
		return err
	}
	stagedName := staged.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(stagedName)
		}
	}()
	if _, err := staged.Write(produced); err != nil {
		staged.Close()
		return err
	}
	if err := staged.Sync(); err != nil {
		staged.Close()
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	if err := os.Rename(stagedName, cachePath); err != nil {
		return err
	}
	remove = false
	return nil
}

// copyFile duplicates a file's bytes under a new name.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// firstBytes caps an error detail at n bytes.
func firstBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
