// Deliverables: the present_files tool, the file classification every
// artifact surface shares, and the card formatting for the tool's steps.
//
// A deliverable exists only when the model says so: present_files registers
// each presented file in fb_artifacts, and every surface that shows what a
// conversation delivered — the web card, the run's end frame, a batch task's
// integrator — reads that table. Intermediate scripts and scratch files are
// never artifacts because the model never presents them.
package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// presentFilesToolName is the one deliverable action. It is registered only
// for office and batch conversations (the runner passes the session's project
// type), and only the primary agent may call it.
const presentFilesToolName = "present_files"

// presentFilesToolDescription is the tool's contract with the model, frozen
// verbatim in testdata/present_files_description.golden: teaching the model
// when to deliver is prompt work, but what the tool does is this text.
const presentFilesToolDescription = "Use when your work produced files the user (or the system that started this task) should receive: documents, spreadsheets, slides, PDFs, images, audio or video, HTML or Markdown pages, archives. Call it once, after the files are final, listing every deliverable; the user gets a download and preview card for each one. Do not present intermediate scripts, scratch data or files you did not finish. Paths must be inside the project directory — save or copy a deliverable there first if it was written elsewhere. Presenting the same path again later records a new version."

// artifactClass is one row of the classification table below.
type artifactClass struct {
	kind    string
	mime    string
	preview string
}

// artifactClasses maps a lowercase file extension to what the file is and how
// a surface may show it. ClassifyArtifact is the single source: the web card,
// the gateway's content serving and the integration docs all read these
// values, so a new format means one new row here plus its test — never a
// per-surface table.
var artifactClasses = map[string]artifactClass{
	// Documents.
	"docx": {kind: "document", mime: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", preview: "docx"},
	"doc":  {kind: "document", mime: "application/msword", preview: "office_pdf"},
	"odt":  {kind: "document", mime: "application/vnd.oasis.opendocument.text", preview: "office_pdf"},
	"rtf":  {kind: "document", mime: "application/rtf", preview: "office_pdf"},
	// Spreadsheets.
	"xlsx": {kind: "spreadsheet", mime: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", preview: "sheet"},
	"xlsm": {kind: "spreadsheet", mime: "application/vnd.ms-excel.sheet.macroEnabled.12", preview: "sheet"},
	"xls":  {kind: "spreadsheet", mime: "application/vnd.ms-excel", preview: "sheet"},
	"ods":  {kind: "spreadsheet", mime: "application/vnd.oasis.opendocument.spreadsheet", preview: "sheet"},
	"csv":  {kind: "spreadsheet", mime: "text/csv", preview: "text"},
	"tsv":  {kind: "spreadsheet", mime: "text/tab-separated-values", preview: "text"},
	// Presentations.
	"pptx": {kind: "presentation", mime: "application/vnd.openxmlformats-officedocument.presentationml.presentation", preview: "office_pdf"},
	"ppt":  {kind: "presentation", mime: "application/vnd.ms-powerpoint", preview: "office_pdf"},
	"odp":  {kind: "presentation", mime: "application/vnd.oasis.opendocument.presentation", preview: "office_pdf"},
	// PDF renders itself.
	"pdf": {kind: "pdf", mime: "application/pdf", preview: "pdf"},
	// Images.
	"png":  {kind: "image", mime: "image/png", preview: "image"},
	"jpg":  {kind: "image", mime: "image/jpeg", preview: "image"},
	"jpeg": {kind: "image", mime: "image/jpeg", preview: "image"},
	"gif":  {kind: "image", mime: "image/gif", preview: "image"},
	"webp": {kind: "image", mime: "image/webp", preview: "image"},
	"bmp":  {kind: "image", mime: "image/bmp", preview: "image"},
	"svg":  {kind: "image", mime: "image/svg+xml", preview: "image"},
	// Audiovisual.
	"mp4":  {kind: "video", mime: "video/mp4", preview: "video"},
	"m4v":  {kind: "video", mime: "video/mp4", preview: "video"},
	"webm": {kind: "video", mime: "video/webm", preview: "video"},
	"mov":  {kind: "video", mime: "video/quicktime", preview: "video"},
	"mp3":  {kind: "audio", mime: "audio/mpeg", preview: "audio"},
	"wav":  {kind: "audio", mime: "audio/wav", preview: "audio"},
	"m4a":  {kind: "audio", mime: "audio/mp4", preview: "audio"},
	"aac":  {kind: "audio", mime: "audio/aac", preview: "audio"},
	"ogg":  {kind: "audio", mime: "audio/ogg", preview: "audio"},
	"oga":  {kind: "audio", mime: "audio/ogg", preview: "audio"},
	"flac": {kind: "audio", mime: "audio/flac", preview: "audio"},
	// Pages.
	"html":     {kind: "html", mime: "text/html", preview: "html"},
	"htm":      {kind: "html", mime: "text/html", preview: "html"},
	"md":       {kind: "markdown", mime: "text/markdown", preview: "markdown"},
	"markdown": {kind: "markdown", mime: "text/markdown", preview: "markdown"},
	// Plain text: the browser renders it, never interprets it.
	"txt":  {kind: "text", mime: "text/plain", preview: "text"},
	"log":  {kind: "text", mime: "text/plain", preview: "text"},
	"json": {kind: "text", mime: "application/json", preview: "text"},
	"yaml": {kind: "text", mime: "text/plain", preview: "text"},
	"yml":  {kind: "text", mime: "text/plain", preview: "text"},
	"xml":  {kind: "text", mime: "text/plain", preview: "text"},
	"sql":  {kind: "text", mime: "text/plain", preview: "text"},
	"py":   {kind: "text", mime: "text/plain", preview: "text"},
	"js":   {kind: "text", mime: "text/plain", preview: "text"},
	"ts":   {kind: "text", mime: "text/plain", preview: "text"},
	"go":   {kind: "text", mime: "text/plain", preview: "text"},
	"java": {kind: "text", mime: "text/plain", preview: "text"},
	"c":    {kind: "text", mime: "text/plain", preview: "text"},
	"h":    {kind: "text", mime: "text/plain", preview: "text"},
	"cpp":  {kind: "text", mime: "text/plain", preview: "text"},
	"sh":   {kind: "text", mime: "text/plain", preview: "text"},
	"css":  {kind: "text", mime: "text/plain", preview: "text"},
	// Archives: zip and tar list their entries; the rest only download.
	"zip": {kind: "archive", mime: "application/zip", preview: "archive"},
	"tar": {kind: "archive", mime: "application/x-tar", preview: "archive"},
	"tgz": {kind: "archive", mime: "application/gzip", preview: "archive"},
	"7z":  {kind: "archive", mime: "application/x-7z-compressed", preview: ""},
	"rar": {kind: "archive", mime: "application/vnd.rar", preview: ""},
	// Design sources.
	"psd":    {kind: "design", mime: "image/vnd.adobe.photoshop", preview: ""},
	"ai":     {kind: "design", mime: "application/octet-stream", preview: ""},
	"sketch": {kind: "design", mime: "application/octet-stream", preview: ""},
	"fig":    {kind: "design", mime: "application/octet-stream", preview: ""},
	"xd":     {kind: "design", mime: "application/octet-stream", preview: ""},
	"rp":     {kind: "design", mime: "application/octet-stream", preview: ""},
	// CAD drawings.
	"dwg": {kind: "cad", mime: "application/acad", preview: ""},
	"dxf": {kind: "cad", mime: "application/dxf", preview: ""},
}

// ClassifyArtifact names what a delivered file is, for people and for the
// systems that integrate with forebrain: document, spreadsheet, presentation,
// pdf, image, video, audio, html, markdown, text, archive, design, cad or
// other. ArtifactPreview names how a surface may show it in place: image,
// video, audio, pdf, html, markdown, text, docx, sheet, office_pdf (converted
// to PDF by LibreOffice), archive, or "" (download only).
//
// The lowercase extension decides; head is only the fallback for a file with
// no extension, via http.DetectContentType. Signature checks (a renamed
// image, a fake PDF) are the gateway's job when it serves the bytes.
func ClassifyArtifact(name string, head []byte) (kind, mimeType, preview string) {
	lower := strings.ToLower(filepath.Base(strings.TrimSpace(name)))
	if strings.HasSuffix(lower, ".tar.gz") {
		return "archive", "application/gzip", "archive"
	}
	ext := strings.TrimPrefix(filepath.Ext(lower), ".")
	if c, ok := artifactClasses[ext]; ok {
		return c.kind, c.mime, c.preview
	}
	if ext == "" && len(head) > 0 {
		if detected := strings.TrimSpace(http.DetectContentType(head)); detected != "" && detected != "application/octet-stream" {
			return classifyDetected(detected)
		}
	}
	mimeType = ""
	if ext != "" {
		mimeType = mime.TypeByExtension("." + ext)
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return "other", mimeType, ""
}

// classifyDetected maps a sniffed content type onto the class a file with
// that content is: the extension-less branch of ClassifyArtifact.
func classifyDetected(contentType string) (kind, mimeType, preview string) {
	base := contentType
	if i := strings.Index(base, ";"); i >= 0 {
		base = strings.TrimSpace(base[:i])
	}
	switch {
	case base == "application/pdf":
		return "pdf", base, "pdf"
	case base == "application/zip":
		return "archive", base, "archive"
	case strings.HasPrefix(base, "image/"):
		return "image", base, "image"
	case strings.HasPrefix(base, "video/"):
		return "video", base, "video"
	case strings.HasPrefix(base, "audio/"):
		return "audio", base, "audio"
	case strings.HasPrefix(base, "text/"):
		return "text", base, "text"
	}
	return "other", base, ""
}

type presentFileInput struct {
	Path        string `json:"path" jsonschema:"description=The file, relative to the project directory or an absolute path inside it."`
	Title       string `json:"title,omitempty" jsonschema:"description=A short human title for the card, e.g. 2026 Q3 operating review. Defaults to the file name."`
	Description string `json:"description,omitempty" jsonschema:"description=One sentence on what the file is and what it is for."`
}

type presentFilesInput struct {
	Files []presentFileInput `json:"files" jsonschema:"minItems=1,maxItems=20,description=Every final deliverable of this task, in the order the user should see them."`
}

// presentFilesToolNote is the one sentence the tool answers with; the card
// the user sees is drawn from the rows it wrote, not from this text.
const presentFilesToolNote = "The user now sees a download and preview card for each file."

// newPresentFilesTool builds the deliverable tool. It needs the session
// store (artifacts live in the state database) and is only wired up for
// office and batch conversations.
func newPresentFilesTool(st *State, rt *AgentToolRuntime) (*llm.Tool, error) {
	return llm.NewTool(
		presentFilesToolName,
		presentFilesToolDescription,
		func(ctx context.Context, in *presentFilesInput) (string, error) {
			if rt == nil || rt.Sess == nil {
				return "", fmt.Errorf("present_files unavailable")
			}
			if IsSubagentRunFromContext(ctx) {
				return "", fmt.Errorf("present_files is only available to the primary agent; report the files back and let it present them")
			}
			if in == nil || len(in.Files) == 0 {
				return "", fmt.Errorf("present_files needs at least one file")
			}
			root, err := WorkingDir(ctx)
			if err != nil {
				return "", err
			}
			// Validate everything before writing anything: a rejected path
			// must leave the conversation with no half-delivered set.
			type stagedFile struct {
				input    presentFileInput
				resolved string
				size     int64
				mtimeNS  int64
			}
			staged := make([]stagedFile, 0, len(in.Files))
			for _, f := range in.Files {
				p := strings.TrimSpace(f.Path)
				if p == "" {
					return "", fmt.Errorf("present_files needs a path for every file")
				}
				abs, err := ResolvePath(ctx, p)
				if err != nil {
					return "", err
				}
				resolved, err := ResolveWithinRoots(abs, []string{root})
				if err != nil {
					return "", fmt.Errorf("%s is outside the project directory %s; save or copy it into the project first", p, root)
				}
				info, err := os.Stat(resolved)
				if err != nil {
					return "", fmt.Errorf("%s: %w", p, err)
				}
				if info.IsDir() {
					return "", fmt.Errorf("%s is a directory; present the files it holds, not the folder", p)
				}
				staged = append(staged, stagedFile{input: f, resolved: resolved, size: info.Size(), mtimeNS: info.ModTime().UnixNano()})
			}
			type presentedFile struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				RelPath string `json:"rel_path"`
				Title   string `json:"title"`
				Kind    string `json:"kind"`
				Preview string `json:"preview"`
				Size    int64  `json:"size"`
			}
			presented := make([]presentedFile, 0, len(staged))
			for _, s := range staged {
				sum, kind, mimeType, preview, err := hashAndClassifyFile(s.resolved)
				if err != nil {
					return "", err
				}
				name := filepath.Base(s.resolved)
				title := strings.TrimSpace(s.input.Title)
				if title == "" {
					title = name
				}
				rel := ""
				if r, err := filepath.Rel(root, s.resolved); err == nil {
					rel = filepath.ToSlash(r)
				} else {
					rel = name
				}
				row, err := rt.Sess.AddArtifact(ctx, state.Artifact{
					SessionID:   ConversationSessionIDFromContext(ctx),
					RunID:       RunIDFromContext(ctx),
					ToolCallID:  ToolUseIDFromContext(ctx),
					Path:        s.resolved,
					RelPath:     rel,
					Name:        name,
					Title:       title,
					Description: strings.TrimSpace(s.input.Description),
					MIME:        mimeType,
					Kind:        kind,
					Preview:     preview,
					Size:        s.size,
					MTimeNS:     s.mtimeNS,
					SHA256:      sum,
					Origin:      state.ArtifactOriginModel,
				})
				if err != nil {
					return "", err
				}
				presented = append(presented, presentedFile{ID: row.ID, Name: row.Name, RelPath: row.RelPath, Title: row.Title, Kind: row.Kind, Preview: row.Preview, Size: row.Size})
			}
			out, err := json.Marshal(struct {
				Presented []presentedFile `json:"presented"`
				Note      string          `json:"note"`
			}{Presented: presented, Note: presentFilesToolNote})
			if err != nil {
				return "", err
			}
			return string(out), nil
		})
}

// hashAndClassifyFile reads a delivered file once: the first bytes classify
// it and the whole stream feeds the sha256 that identifies this version.
func hashAndClassifyFile(path string) (sum, kind, mimeType, preview string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", "", "", err
	}
	defer f.Close()
	head := make([]byte, 512)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", "", "", "", err
	}
	head = head[:n]
	kind, mimeType, preview = ClassifyArtifact(path, head)
	h := sha256.New()
	if _, err := h.Write(head); err != nil {
		return "", "", "", "", err
	}
	if _, err := io.Copy(h, f); err != nil {
		return "", "", "", "", err
	}
	return hex.EncodeToString(h.Sum(nil)), kind, mimeType, preview, nil
}

// PrimaryOnlyToolName reports whether a tool may never run inside a subagent:
// the deliverable action speaks for the conversation, and a subagent's files
// are the primary agent's to present.
func PrimaryOnlyToolName(name string) bool {
	return strings.TrimSpace(name) == presentFilesToolName
}

// formatPresentFilesStep draws the deliverable card's body: one line per
// presented file. Output the tool did not shape that way still gets a line
// of prose — never the raw JSON.
func formatPresentFilesStep(evt StepEvent, maxBytes int) (string, bool) {
	items, _ := evt.Output["presented"].([]any)
	lines := make([]string, 0, len(items))
	for _, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := row["name"].(string)
		title, _ := row["title"].(string)
		kind, _ := row["kind"].(string)
		rel, _ := row["rel_path"].(string)
		size, _ := row["size"].(float64)
		label := title
		if label == "" || label == name {
			label = name
		}
		if label == "" || rel == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- **%s** · %s · %s · `%s`", label, kind, HumanSize(int64(size)), rel))
	}
	if len(lines) == 0 {
		return "Presented files.", false
	}
	return strings.Join(lines, "\n"), false
}

// HumanSize renders a byte count the way a card reads it: one decimal, the
// unit a person expects. It is exported because a surface that lists
// deliverables outside the card — a batch task's end notification, for one —
// must read a size the same way the card does (plan 151).
func HumanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// presentFilesHeaderTarget names the card's subject: the file itself, or the
// first file plus how many follow it.
func presentFilesHeaderTarget(input map[string]any) string {
	files, _ := input["files"].([]any)
	first := ""
	for _, item := range files {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if p, _ := row["path"].(string); strings.TrimSpace(p) != "" {
			first = filepath.Base(strings.TrimSpace(p))
			break
		}
	}
	if first == "" {
		return ""
	}
	if extra := len(files) - 1; extra > 0 {
		return fmt.Sprintf("%s +%d", first, extra)
	}
	return first
}
