package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedWorkshopWorkspace lays out a "<name>-workspace/iteration-1" tree the
// way the skill-workshop skill leaves one: two eval directories, each with a
// with_skill and without_skill run carrying grading.json, timing.json and an
// outputs file, plus a benchmark and a feedback file at the iteration root.
func seedWorkshopWorkspace(t *testing.T, skillsRoot, name string) string {
	t.Helper()
	ws := filepath.Join(skillsRoot, name+"-workspace")
	iter := filepath.Join(ws, "iteration-1")
	writeFile := func(rel, content string) {
		path := filepath.Join(iter, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	writeFile("eval-0/with_skill/grading.json", `{"expectations":[{"text":"a","passed":true},{"text":"b","passed":false}],"summary":{"passed":1,"failed":1,"total":2,"pass_rate":0.5}}`)
	writeFile("eval-0/with_skill/timing.json", `{"total_duration_seconds":42.5}`)
	writeFile("eval-0/with_skill/outputs/answer.md", "the with-skill answer")
	writeFile("eval-0/without_skill/grading.json", `{"expectations":[{"text":"a","passed":false}],"summary":{"passed":0,"failed":1,"total":1,"pass_rate":0}}`)
	writeFile("eval-0/without_skill/outputs/answer.md", "the baseline answer")
	writeFile("eval-0/eval_metadata.json", `{"eval_id":0,"eval_name":"Ocean outage","prompt":"gateway returns 502"}`)
	writeFile("eval-1/with_skill/grading.json", `{"expectations":[{"text":"a","passed":true}],"summary":{"passed":1,"failed":0,"total":1,"pass_rate":1}}`)
	writeFile("eval-1/without_skill/grading.json", `{"expectations":[{"text":"a","passed":true}],"summary":{"passed":1,"failed":0,"total":1,"pass_rate":1}}`)
	writeFile("feedback.json", `{"reviews":[{"run_id":"eval-0-with_skill","feedback":"step 3 should check the deploy","timestamp":"t"}],"status":"complete"}`)
	return ws
}

// TestWorkshopEvalsRouteReadsIterations pins the workshop evaluation read:
// rounds, per-case grading, outputs and the merged feedback the dialog shows.
func TestWorkshopEvalsRouteReadsIterations(t *testing.T) {
	home := t.TempDir()
	s, skillsRoot := newSkillVersionsServer(t, home)
	skillDir := filepath.Join(skillsRoot, "gateway-ops")
	require.NoError(t, os.MkdirAll(filepath.Join(skillDir, "evals"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: gateway-ops\ndescription: runbook\n---\n\nbody\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "evals", "evals.json"),
		[]byte(`{"skill_name":"gateway-ops","evals":[{"id":1,"prompt":"gateway returns 502","expected_output":"health back to 200"}]}`), 0o644))
	seedWorkshopWorkspace(t, skillsRoot, "gateway-ops")

	rr := httptest.NewRecorder()
	s.handleSkillWorkshopEvals(rr, versionRouteReq(http.MethodGet, "/api/skills/gateway-ops/workshop/evals",
		map[string]string{"name": "gateway-ops"}, ""))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var body struct {
		Iterations []struct {
			Name  string `json:"name"`
			Order int    `json:"order"`
			Cases []struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				Prompt   string `json:"prompt"`
				Expected string `json:"expected"`
				Feedback string `json:"feedback"`
				Configs  map[string]struct {
					Present     bool    `json:"present"`
					Passed      int     `json:"passed"`
					Total       int     `json:"total"`
					Output      string  `json:"output"`
					TimeSeconds float64 `json:"time_seconds"`
				} `json:"configs"`
			} `json:"cases"`
		} `json:"iterations"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.Len(t, body.Iterations, 1)
	require.Equal(t, "iteration-1", body.Iterations[0].Name)
	require.Equal(t, 1, body.Iterations[0].Order)
	require.Len(t, body.Iterations[0].Cases, 2)
	first := body.Iterations[0].Cases[0]
	require.Equal(t, "eval-0", first.ID)
	require.Equal(t, "Ocean outage", first.Name)
	require.Equal(t, "gateway returns 502", first.Prompt)
	require.Equal(t, "health back to 200", first.Expected)
	require.Equal(t, "step 3 should check the deploy", first.Feedback)
	require.True(t, first.Configs["with_skill"].Present)
	require.Equal(t, 1, first.Configs["with_skill"].Passed)
	require.Equal(t, 2, first.Configs["with_skill"].Total)
	require.Equal(t, "the with-skill answer", first.Configs["with_skill"].Output)
	require.Equal(t, 42.5, first.Configs["with_skill"].TimeSeconds)
	require.Equal(t, "the baseline answer", first.Configs["without_skill"].Output)
}

// TestWorkshopFeedbackMergesIntoFeedbackJSON pins the write: a PUT updates
// the matching review in place and adds one when the run id is new.
func TestWorkshopFeedbackMergesIntoFeedbackJSON(t *testing.T) {
	home := t.TempDir()
	s, skillsRoot := newSkillVersionsServer(t, home)
	skillDir := filepath.Join(skillsRoot, "gateway-ops")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: gateway-ops\ndescription: runbook\n---\n\nbody\n"), 0o644))
	ws := seedWorkshopWorkspace(t, skillsRoot, "gateway-ops")

	rr := httptest.NewRecorder()
	s.handleSkillWorkshopFeedback(rr, versionRouteReq(http.MethodPut,
		"/api/skills/gateway-ops/workshop/evals/iteration-1/feedback",
		map[string]string{"name": "gateway-ops", "iteration": "iteration-1"},
		`{"run_id":"eval-0-with_skill","feedback":"updated note"}`))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	rr = httptest.NewRecorder()
	s.handleSkillWorkshopFeedback(rr, versionRouteReq(http.MethodPut,
		"/api/skills/gateway-ops/workshop/evals/iteration-1/feedback",
		map[string]string{"name": "gateway-ops", "iteration": "iteration-1"},
		`{"run_id":"eval-1-with_skill","feedback":"new note"}`))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	data, err := os.ReadFile(filepath.Join(ws, "iteration-1", "feedback.json"))
	require.NoError(t, err)
	var doc struct {
		Reviews []struct {
			RunID    string `json:"run_id"`
			Feedback string `json:"feedback"`
		} `json:"reviews"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))
	require.Len(t, doc.Reviews, 2)
	got := map[string]string{}
	for _, review := range doc.Reviews {
		got[review.RunID] = review.Feedback
	}
	require.Equal(t, "updated note", got["eval-0-with_skill"])
	require.Equal(t, "new note", got["eval-1-with_skill"])
}

// TestWorkshopTriggerRouteReadsQueriesAndResults pins the trigger read: the
// draft's eval set and the most recent run's scores.
func TestWorkshopTriggerRouteReadsQueriesAndResults(t *testing.T) {
	home := t.TempDir()
	s, skillsRoot := newSkillVersionsServer(t, home)
	skillDir := filepath.Join(skillsRoot, "gateway-ops")
	require.NoError(t, os.MkdirAll(filepath.Join(skillDir, "evals"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: gateway-ops\ndescription: runbook\n---\n\nbody\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "evals", "trigger_queries.json"),
		[]byte(`[{"query":"gateway 502","should_trigger":true},{"query":"restart the gateway","should_trigger":false}]`), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(skillsRoot, "gateway-ops-workspace"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillsRoot, "gateway-ops-workspace", "trigger-results.json"),
		[]byte(`{"original_description":"old desc","best_description":"new desc","best_score":0.92,"history":[{"description":"old desc","results":[{"query":"gateway 502","should_trigger":true,"pass":true,"trigger_rate":1},{"query":"restart the gateway","should_trigger":false,"pass":false,"trigger_rate":1}]}]}`), 0o644))

	rr := httptest.NewRecorder()
	s.handleSkillWorkshopTrigger(rr, versionRouteReq(http.MethodGet, "/api/skills/gateway-ops/workshop/trigger",
		map[string]string{"name": "gateway-ops"}, ""))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var body struct {
		Queries []struct {
			Query         string `json:"query"`
			ShouldTrigger bool   `json:"should_trigger"`
		} `json:"queries"`
		Results *struct {
			CurrentScore    float64 `json:"current_score"`
			BestDescription string  `json:"best_description"`
			BestScore       float64 `json:"best_score"`
			Queries         []struct {
				Query string `json:"query"`
				Pass  bool   `json:"pass"`
			} `json:"queries"`
		} `json:"results"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.Len(t, body.Queries, 2)
	require.Equal(t, "gateway 502", body.Queries[0].Query)
	require.True(t, body.Queries[0].ShouldTrigger)
	require.NotNil(t, body.Results)
	require.Equal(t, 0.5, body.Results.CurrentScore)
	require.Equal(t, "new desc", body.Results.BestDescription)
	require.Equal(t, 0.92, body.Results.BestScore)
	require.Len(t, body.Results.Queries, 2)
	require.False(t, body.Results.Queries[1].Pass)
}

// TestWorkshopArtifactsMissingWorkspaceIsEmpty pins the empty case: with no
// workspace directory the routes answer empty rather than an error.
func TestWorkshopArtifactsMissingWorkspaceIsEmpty(t *testing.T) {
	home := t.TempDir()
	s, skillsRoot := newSkillVersionsServer(t, home)
	skillDir := filepath.Join(skillsRoot, "gateway-ops")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: gateway-ops\ndescription: runbook\n---\n\nbody\n"), 0o644))

	rr := httptest.NewRecorder()
	s.handleSkillWorkshopEvals(rr, versionRouteReq(http.MethodGet, "/api/skills/gateway-ops/workshop/evals",
		map[string]string{"name": "gateway-ops"}, ""))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"iterations":[]`)

	rr = httptest.NewRecorder()
	s.handleSkillWorkshopTrigger(rr, versionRouteReq(http.MethodGet, "/api/skills/gateway-ops/workshop/trigger",
		map[string]string{"name": "gateway-ops"}, ""))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"queries":[]`)
	require.Contains(t, rr.Body.String(), `"results":null`)
}
