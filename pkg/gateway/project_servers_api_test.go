package gateway

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// The one-screen project servers confirmation: the pending answer lists both
// kinds in one place, the consent records the whole answer at once, and the
// auto-allow switch is the same value everywhere it is shown.

// newServersConsentFixture is one project space over two undecided project
// files: two MCP entries and one language-server entry, so the confirmation
// has something to ask about in both groups.
func newServersConsentFixture(t *testing.T) (*Server, state.Project) {
	t.Helper()
	s, p, _ := projectPagesServer(t)
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, ".forebrain", "mcp_servers.yaml"), []byte(
		"mcp_servers:\n"+
			"  - name: caveman\n    transport: stdio\n    command: npx -y caveman-shrink\n"+
			"  - name: webtool\n    transport: stdio\n    command: /bin/webtool\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, ".forebrain", "lsp_servers.yaml"), []byte(
		"servers:\n"+
			"  gopls: {command: gopls, args: [serve], extension_to_language: {\".go\": go}}\n"), 0o644))
	return s, p
}

// pendingServerRow is one row of the pending answer: the entry as the dialog
// shows it, with the fingerprint its answer has to send back.
type pendingServerRow struct {
	Name        string `json:"name"`
	Detail      string `json:"detail"`
	Fingerprint string `json:"fingerprint"`
}

type serversPendingAnswer struct {
	MCP       []pendingServerRow `json:"mcp"`
	LSP       []pendingServerRow `json:"lsp"`
	AutoAllow bool               `json:"auto_allow"`
}

func projectServersPending(t *testing.T, s *Server, id string) serversPendingAnswer {
	t.Helper()
	rec := doJSON(t, s, http.MethodGet, "/api/v1/projects/"+id+"/servers/pending", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var answer serversPendingAnswer
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &answer))
	return answer
}

func pendingNames(rows []pendingServerRow) []string {
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Name)
	}
	return names
}

// TestProjectServersPendingListsBothKindsWithAutoAllow pins the dialog's one
// read: both groups with the names and fingerprints the answer is bound to —
// a language server named by its id — and the switch's state, which the two
// project pages repeat so the toolbar toggle never guesses.
func TestProjectServersPendingListsBothKindsWithAutoAllow(t *testing.T) {
	s, p := newServersConsentFixture(t)

	answer := projectServersPending(t, s, p.ID)
	require.Equal(t, []string{"caveman", "webtool"}, pendingNames(answer.MCP))
	for _, row := range answer.MCP {
		require.NotEmpty(t, row.Detail, "%s carries no detail", row.Name)
		require.NotEmpty(t, row.Fingerprint, "%s carries no fingerprint", row.Name)
	}
	require.Equal(t, []string{"gopls"}, pendingNames(answer.LSP))
	require.NotEmpty(t, answer.LSP[0].Detail)
	require.NotEmpty(t, answer.LSP[0].Fingerprint)
	require.False(t, answer.AutoAllow)

	// The two project pages carry the switch beside their tables, paged and
	// unpaged alike.
	for _, target := range []string{
		"/api/v1/projects/" + p.ID + "/mcp",
		"/api/v1/projects/" + p.ID + "/mcp?page=1&page_size=10",
		"/api/v1/projects/" + p.ID + "/lsp",
		"/api/v1/projects/" + p.ID + "/lsp?page=1&page_size=10",
	} {
		rec := doJSON(t, s, http.MethodGet, target, nil)
		require.Equal(t, http.StatusOK, rec.Code, target)
		body := decodeBody(t, rec.Body.Bytes())
		require.Equal(t, false, body["auto_allow"], target)
	}
}

// TestProjectServersConsentRecordsBothKindsAndTurnsSwitchOn pins the dialog's
// whole answer: every listed entry is decided — checked means allowed,
// unchecked means denied — and the switch turns on with it, so the next
// session is asked nothing.
func TestProjectServersConsentRecordsBothKindsAndTurnsSwitchOn(t *testing.T) {
	s, p := newServersConsentFixture(t)
	pending := projectServersPending(t, s, p.ID)

	rec := doJSON(t, s, http.MethodPost, "/api/v1/projects/"+p.ID+"/servers/consent", map[string]any{
		"mcp": []any{
			map[string]any{"name": "caveman", "fingerprint": pending.MCP[0].Fingerprint, "allow": true},
			map[string]any{"name": "webtool", "fingerprint": pending.MCP[1].Fingerprint, "allow": false},
		},
		"lsp": []any{
			map[string]any{"name": "gopls", "fingerprint": pending.LSP[0].Fingerprint, "allow": true},
		},
		"always_allow": true,
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var answered struct {
		OK bool `json:"ok"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &answered))
	require.True(t, answered.OK)

	// Both groups are decided, so nothing is pending any more, and the
	// switch is on.
	after := projectServersPending(t, s, p.ID)
	require.Empty(t, after.MCP)
	require.Empty(t, after.LSP)
	require.True(t, after.AutoAllow)

	// The project pages show the same answer: one MCP entry allowed and one
	// denied — a denial is a row that can be taken back — and the language
	// server allowed.
	mcpBody := decodeMCPServersBody(t, doJSON(t, s, http.MethodGet,
		"/api/v1/projects/"+p.ID+"/mcp?page=1&page_size=20", nil))
	require.Equal(t, true, mcpServerRow(t, mcpBody, "caveman")["allowed"])
	require.Equal(t, true, mcpServerRow(t, mcpBody, "webtool")["declined"])
	require.Equal(t, true, mcpBody["auto_allow"])
	lspBody := decodeBody(t, doJSON(t, s, http.MethodGet,
		"/api/v1/projects/"+p.ID+"/lsp?page=1&page_size=20", nil).Body.Bytes())
	require.Equal(t, "allowed", lspBody["rows"].([]any)[0].(map[string]any)["status"])
	require.Equal(t, true, lspBody["auto_allow"])
}

// TestProjectServersConsentRefusesAStaleFingerprint pins the binding the whole
// answer carries: each choice names the fingerprint the dialog showed, and a
// project file that moved under the operator is refused whole — 409, nothing
// of either kind recorded, the switch untouched — until the surface reloads.
func TestProjectServersConsentRefusesAStaleFingerprint(t *testing.T) {
	s, p := newServersConsentFixture(t)
	pending := projectServersPending(t, s, p.ID)

	// The dialog read caveman; the file moves under it before the answer.
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, ".forebrain", "mcp_servers.yaml"), []byte(
		"mcp_servers:\n"+
			"  - name: caveman\n    transport: stdio\n    command: /bin/caveman-rewritten\n"+
			"  - name: webtool\n    transport: stdio\n    command: /bin/webtool\n"), 0o644))

	rec := doJSON(t, s, http.MethodPost, "/api/v1/projects/"+p.ID+"/servers/consent", map[string]any{
		"mcp": []any{
			map[string]any{"name": "caveman", "fingerprint": pending.MCP[0].Fingerprint, "allow": true},
			map[string]any{"name": "webtool", "fingerprint": pending.MCP[1].Fingerprint, "allow": true},
		},
		"lsp": []any{
			map[string]any{"name": "gopls", "fingerprint": pending.LSP[0].Fingerprint, "allow": true},
		},
		"always_allow": true,
	})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var refused struct {
		Error string   `json:"error"`
		Code  string   `json:"code"`
		Stale []string `json:"stale"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &refused))
	require.Equal(t, "consent_stale", refused.Code)
	require.Equal(t, []string{"caveman"}, refused.Stale)
	require.NotEmpty(t, refused.Error)

	// Nothing was recorded: both kinds still ask, and the switch is off.
	after := projectServersPending(t, s, p.ID)
	require.Equal(t, []string{"caveman", "webtool"}, pendingNames(after.MCP))
	require.Equal(t, []string{"gopls"}, pendingNames(after.LSP))
	require.False(t, after.AutoAllow)
}

// TestProjectServersAutoAllowRoundTrip pins the project page's switch: on, the
// undecided entries load without asking so both groups read empty; off, they
// ask again. An unknown project is a 404 on all three endpoints.
func TestProjectServersAutoAllowRoundTrip(t *testing.T) {
	s, p := newServersConsentFixture(t)

	rec := doJSON(t, s, http.MethodPut, "/api/v1/projects/"+p.ID+"/servers/auto-allow", map[string]any{"on": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var on struct {
		OK bool `json:"ok"`
		On bool `json:"on"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &on))
	require.True(t, on.OK)
	require.True(t, on.On)

	after := projectServersPending(t, s, p.ID)
	require.Empty(t, after.MCP)
	require.Empty(t, after.LSP)
	require.True(t, after.AutoAllow)

	rec = doJSON(t, s, http.MethodPut, "/api/v1/projects/"+p.ID+"/servers/auto-allow", map[string]any{"on": false})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var off struct {
		OK bool `json:"ok"`
		On bool `json:"on"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &off))
	require.True(t, off.OK)
	require.False(t, off.On)

	again := projectServersPending(t, s, p.ID)
	require.Equal(t, []string{"caveman", "webtool"}, pendingNames(again.MCP))
	require.Equal(t, []string{"gopls"}, pendingNames(again.LSP))
	require.False(t, again.AutoAllow)

	// An unknown project answers 404 on every one of the three endpoints.
	for method, path := range map[string]string{
		http.MethodGet:  "/api/v1/projects/nope/servers/pending",
		http.MethodPost: "/api/v1/projects/nope/servers/consent",
		http.MethodPut:  "/api/v1/projects/nope/servers/auto-allow",
	} {
		rec := doJSON(t, s, method, path, map[string]any{})
		require.Equal(t, http.StatusNotFound, rec.Code, method+" "+path)
	}
}
