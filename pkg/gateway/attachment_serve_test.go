package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// attachmentRequest builds the request the router would hand the handler for
// GET /api/chat/sessions/:id/messages/:row/attachments/:n.
func attachmentRequest(sid, row, n, query string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/"+sid+"/messages/"+row+"/attachments/"+n+query, nil)
	return r.WithContext(context.WithValue(r.Context(), ParamsKey, Params{
		{Key: "id", Value: sid}, {Key: "row", Value: row}, {Key: "n", Value: n},
	}))
}

// userRowWithAttachments stores one user message that attached the given
// recorded paths (the terminal's file_reference parts) and returns its row id.
func userRowWithAttachments(t *testing.T, s *Server, sid string, paths ...string) int64 {
	t.Helper()
	parts := `[{"text":"look","type":"text"}`
	for _, p := range paths {
		parts += `,{"file_id":"` + p + `","label":"` + filepath.Base(p) + `","mime_type":"image/png","type":"file_reference"}`
	}
	parts += `]`
	res, err := s.Sessions.DB().ExecContext(context.Background(),
		`INSERT INTO fb_messages (session_id, role, visibility, content, parts, created_at) VALUES (?, 'user', 'visible', 'look', ?, 1)`,
		sid, parts)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

// TestChatMessageAttachmentServesWhatTheTerminalRecorded pins the route a
// path-recorded attachment (a screenshot the terminal pasted into the project)
// is opened by: addressed by the message that holds it, inside the
// conversation's file view, a download by default and drawn in place only on
// request and only for a kind a browser draws safely.
func TestChatMessageAttachmentServesWhatTheTerminalRecorded(t *testing.T) {
	s, root := workspaceWriteServer(t)
	image := tinyPNG(t)
	shot := filepath.Join(root, ".forebrain", "attachments", "shot.png")
	require.NoError(t, os.MkdirAll(filepath.Dir(shot), 0o755))
	require.NoError(t, os.WriteFile(shot, image, 0o644))
	// A recorded path outside every served root, and a file with a script in it
	// that claims to be an image.
	outside := filepath.Join(t.TempDir(), "outside.png")
	require.NoError(t, os.WriteFile(outside, image, 0o644))
	page := filepath.Join(root, ".forebrain", "attachments", "page.png")
	require.NoError(t, os.WriteFile(page, []byte("<html><script>alert(1)</script></html>"), 0o644))

	row := strconv.FormatInt(userRowWithAttachments(t, s, "S1", shot, outside, page), 10)

	// A download by default, under the name it was attached by.
	rec := httptest.NewRecorder()
	s.handleChatMessageAttachment(rec, attachmentRequest("S1", row, "0", ""))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, image, rec.Body.Bytes())
	require.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	require.Equal(t, `attachment; filename=shot.png`, rec.Header().Get("Content-Disposition"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))

	// Drawn in place on request.
	rec = httptest.NewRecorder()
	s.handleChatMessageAttachment(rec, attachmentRequest("S1", row, "0", "?disposition=inline"))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, `inline; filename=shot.png`, rec.Header().Get("Content-Disposition"))

	// A path outside the file view is refused, not served.
	rec = httptest.NewRecorder()
	s.handleChatMessageAttachment(rec, attachmentRequest("S1", row, "1", ""))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	// Whatever the name says, bytes that are not an image never go inline: they
	// are a download, so the file cannot run as a page of the gateway's origin.
	rec = httptest.NewRecorder()
	s.handleChatMessageAttachment(rec, attachmentRequest("S1", row, "2", "?disposition=inline"))
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment"), rec.Header().Get("Content-Disposition"))
	require.NotContains(t, rec.Header().Get("Content-Type"), "html")

	// Past the last attachment, an unknown message and a foreign session are all
	// simply not found.
	for _, req := range []*http.Request{
		attachmentRequest("S1", row, "3", ""),
		attachmentRequest("S1", "999999", "0", ""),
		attachmentRequest("no-such-session", row, "0", ""),
	} {
		rec = httptest.NewRecorder()
		s.handleChatMessageAttachment(rec, req)
		require.Equal(t, http.StatusNotFound, rec.Code, req.URL.Path)
	}
}

// TestUserMessageAttachmentsReadsTheOwnedUserRowOnly: the store answers for the
// session's own user messages, in the order they were attached.
func TestUserMessageAttachmentsReadsTheOwnedUserRowOnly(t *testing.T) {
	s, _ := workspaceWriteServer(t)
	id := userRowWithAttachments(t, s, "S1", "/a/one.png", "/a/two.png")

	refs, ok, err := s.Sessions.UserMessageAttachments(context.Background(), "S1", id)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"/a/one.png", "/a/two.png"}, []string{refs[0].FileID, refs[1].FileID})

	_, ok, err = s.Sessions.UserMessageAttachments(context.Background(), "someone-else", id)
	require.NoError(t, err)
	require.False(t, ok, "another session's id must not read this row")
}
