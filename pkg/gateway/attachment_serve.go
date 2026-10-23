package gateway

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// inlineAttachmentTypes are the only types a page may be handed for display
// in place: the ones a browser draws without running anything. Every other
// type — whatever its stored media type claims — is a download, so an
// uploaded file can never execute as a page of the gateway's own origin.
var inlineAttachmentTypes = map[string]bool{
	"image/png":       true,
	"image/jpeg":      true,
	"image/gif":       true,
	"image/webp":      true,
	"application/pdf": true,
}

// attachmentContentType names the type a file is served as: its own bytes
// decide for the inline-able kinds (a PDF by its header, an image by its
// signature), the extension for the rest, and what nothing recognises is a
// plain octet stream.
func attachmentContentType(name string, head []byte) string {
	if len(head) >= 5 && string(head[:5]) == "%PDF-" {
		return "application/pdf"
	}
	if sniffed := http.DetectContentType(head); inlineAttachmentTypes[sniffed] {
		return sniffed
	}
	if byExt := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); byExt != "" {
		// The extension never earns an inline type the bytes did not show.
		if base, _, err := mime.ParseMediaType(byExt); err == nil && !inlineAttachmentTypes[base] {
			return byExt
		}
	}
	return "application/octet-stream"
}

// serveAttachmentFile writes one attachment: a download by default, drawn in
// place only when the page asked for it (inline) and the file is one of the
// kinds a browser can show safely.
func serveAttachmentFile(w http.ResponseWriter, r *http.Request, f *os.File, name string, inline bool) {
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ctype := attachmentContentType(name, head)
	disposition := "attachment"
	if inline && inlineAttachmentTypes[ctype] {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	w.Header().Set("Cache-Control", "private, max-age=60")
	http.ServeContent(w, r, name, st.ModTime(), f)
}

// attachmentWantsInline reads the page's `disposition` ask.
func attachmentWantsInline(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("disposition")), "inline")
}

// handleChatMessageAttachment answers GET
// /api/chat/sessions/:id/messages/:row/attachments/:n — the bytes of the n-th
// thing one user message attached, for a file the page cannot name by an upload
// id: the terminal saves a pasted screenshot into the project and records its
// path, which the page never sees. The attachment is addressed by the message
// that holds it, so this is not a way to read an arbitrary path: the row must
// be this conversation's own user message, and the file it recorded is still
// held inside the conversation's file view by the same containment every other
// file read applies.
func (s *Server) handleChatMessageAttachment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s.Sessions == nil {
		http.Error(w, "session store unavailable", http.StatusServiceUnavailable)
		return
	}
	sid := pathParam(r, "id")
	rowID, rowErr := strconv.ParseInt(pathParam(r, "row"), 10, 64)
	index, indexErr := strconv.Atoi(pathParam(r, "n"))
	if sid == "" || rowErr != nil || indexErr != nil || rowID <= 0 || index < 0 {
		http.NotFound(w, r)
		return
	}
	owned, err := s.sessionOwned(r.Context(), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !owned {
		http.NotFound(w, r)
		return
	}
	refs, found, err := s.Sessions.UserMessageAttachments(r.Context(), sid, rowID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found || index >= len(refs) || !filepath.IsAbs(refs[index].FileID) {
		// An upload is read by its own id (/api/files/:id/download); only a
		// recorded path is served here.
		http.NotFound(w, r)
		return
	}
	full, err := s.resolveRawImagePath(r.Context(), sid, "", refs[index].FileID)
	if err != nil {
		writeWorkspacePathError(w, err)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	name := strings.TrimSpace(refs[index].Label)
	if name == "" {
		name = filepath.Base(full)
	}
	serveAttachmentFile(w, r, f, filepath.Base(name), attachmentWantsInline(r))
}
