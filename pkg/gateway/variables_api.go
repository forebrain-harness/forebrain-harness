// The variables page's API (plan 265): the operator-owned values MCP and LSP
// server entries resolve as ${NAME} (plan 259's fb_env_vars table, which took
// over from the home .env in plan 268).
//
// A value never leaves the gateway: the page is told whether one is set and its
// last four characters, the same rule a stored key follows. Unlike a key it is
// never needed back — a person writes a variable once and the page only edits
// or deletes it — so there is no reveal here at all.
package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// envVarRow is one row of the variables table.
type envVarRow struct {
	Name        string `json:"name"`
	ValueSet    bool   `json:"value_set"`
	ValueLast4  string `json:"value_last4,omitempty"`
	UpdatedAtMs int64  `json:"updated_at"`
}

// handleEnvVars serves one page of the variables table.
func (s *Server) handleEnvVars(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	store, ok := s.envVarStore(w)
	if !ok {
		return
	}
	vars, err := store.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	q := readListQuery(r)
	rows := make([]envVarRow, 0, len(vars))
	for _, variable := range vars {
		if q.Q != "" && !strings.Contains(strings.ToLower(variable.Name), strings.ToLower(q.Q)) {
			continue
		}
		rows = append(rows, envVarRow{
			Name:        variable.Name,
			ValueSet:    variable.Value != "",
			ValueLast4:  secretLast4(variable.Value),
			UpdatedAtMs: variable.UpdatedAtMs,
		})
	}
	page, served := pageOf(rows, q)
	writeListPage(w, "vars", page, len(rows), served, q, nil)
}

// handleEnvVar writes or removes one variable. An empty value is refused rather
// than stored: "set it to nothing" and "it is not there" would otherwise be two
// states of one row, and the page's delete button is what the first one means.
func (s *Server) handleEnvVar(w http.ResponseWriter, r *http.Request) {
	store, ok := s.envVarStore(w)
	if !ok {
		return
	}
	name := pathParam(r, "name")
	switch r.Method {
	case http.MethodPut:
		var body struct {
			Value string `json:"value"`
		}
		if !decodeLLMModelBody(w, r, &body) {
			return
		}
		if strings.TrimSpace(body.Value) == "" {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{
				"error": llmText(r, "变量值不能为空，要删除请用删除", "a variable's value cannot be empty; delete it instead"),
			})
			return
		}
		if err := store.Set(r.Context(), name, body.Value); err != nil {
			if errors.Is(err, state.ErrEnvVarName) {
				writeJSONStatus(w, http.StatusBadRequest, map[string]any{
					"error": llmText(r, "变量名只能由字母、数字和下划线组成，且不能以数字开头",
						"a variable name may hold letters, digits and underscores, and cannot start with a digit"),
				})
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"name": strings.TrimSpace(name)})
	case http.MethodDelete:
		if err := store.Delete(r.Context(), name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// envVarStore is the table, or the 503 a gateway without a state database
// answers.
func (s *Server) envVarStore(w http.ResponseWriter) (*state.EnvVarStore, bool) {
	if s == nil || s.Env == nil || s.Env.SQL == nil {
		http.Error(w, "no state database is bound", http.StatusServiceUnavailable)
		return nil, false
	}
	return &state.EnvVarStore{DB: s.Env.SQL}, true
}

// operatorEnvVars reads the variables table for a path that resolves ${NAME}
// references — starting an MCP server, testing one from its page. An
// unreadable table is no variables rather than a failed request: the caller's
// own report already says which reference did not resolve.
func (s *Server) operatorEnvVars(ctx context.Context) map[string]string {
	if s == nil || s.Env == nil || s.Env.SQL == nil {
		return nil
	}
	vars, err := (&state.EnvVarStore{DB: s.Env.SQL}).Map(ctx)
	if err != nil {
		return nil
	}
	return vars
}
