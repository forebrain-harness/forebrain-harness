// The variables page's API (plan 265): the fb_env_vars table the MCP and LSP
// entries resolve as ${NAME}.
package gateway

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// A variable's value never leaves the gateway, and an empty one is a delete.
func TestEnvVarsAPI(t *testing.T) {
	_, do := modelsPageServer(t)

	rec := do(http.MethodPut, "/api/env-vars/GITHUB_TOKEN", `{"value":"ghp-secret-value"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = do(http.MethodGet, "/api/env-vars?page=1&page_size=10&q=GITHUB")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "GITHUB_TOKEN")
	require.Contains(t, rec.Body.String(), "alue")
	require.NotContains(t, rec.Body.String(), "ghp-secret-value")

	require.NotContains(t, do(http.MethodGet, "/api/env-vars?q=NOPE").Body.String(), "GITHUB_TOKEN")

	rec = do(http.MethodPut, "/api/env-vars/GITHUB_TOKEN", `{"value":"   "}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "不能为空")

	rec = do(http.MethodPut, "/api/env-vars/9BAD", `{"value":"x"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "变量名")

	require.Equal(t, http.StatusNoContent, do(http.MethodDelete, "/api/env-vars/GITHUB_TOKEN").Code)
	require.Equal(t, http.StatusNoContent, do(http.MethodDelete, "/api/env-vars/GITHUB_TOKEN").Code)
	require.NotContains(t, do(http.MethodGet, "/api/env-vars?page=1").Body.String(), "GITHUB_TOKEN")
}
