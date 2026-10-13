package gateway

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

// The workshop artifact endpoints: what the workshop conversation produced,
// read back for the workbench's evaluation and trigger tabs. Nothing here
// runs an evaluation or a trigger test — the model does that by running the
// skill's own scripts (plan 011); these routes only read the files those
// runs leave behind, the same way the workshop's own prompts tell the model
// to write them (pkg/skill/system_assets/skill-workshop/SKILL.md).

// workshopFileMaxBytes bounds one read output so a runaway file cannot blow
// the response; the views truncate further on their own.
const workshopOutputMaxBytes = 64 << 10

type workshopEvalConfig struct {
	Present     bool    `json:"present"`
	Passed      int     `json:"passed"`
	Total       int     `json:"total"`
	PassRate    float64 `json:"pass_rate"`
	Output      string  `json:"output,omitempty"`
	TimeSeconds float64 `json:"time_seconds,omitempty"`
}

type workshopEvalCase struct {
	ID       string                        `json:"id"`
	Name     string                        `json:"name"`
	Prompt   string                        `json:"prompt"`
	Expected string                        `json:"expected"`
	RunID    string                        `json:"run_id"`
	Feedback string                        `json:"feedback,omitempty"`
	Configs  map[string]workshopEvalConfig `json:"configs"`
}

type workshopIteration struct {
	Name     string             `json:"name"`
	Order    int                `json:"order"`
	Modified int64              `json:"modified"`
	Cases    []workshopEvalCase `json:"cases"`
}

type workshopEvalsResponse struct {
	Name       string              `json:"name"`
	Workspace  string              `json:"workspace"`
	Iterations []workshopIteration `json:"iterations"`
}

type workshopTriggerQuery struct {
	Query         string `json:"query"`
	ShouldTrigger bool   `json:"should_trigger"`
}

type workshopTriggerResult struct {
	Query         string  `json:"query"`
	ShouldTrigger bool    `json:"should_trigger"`
	Pass          bool    `json:"pass"`
	TriggerRate   float64 `json:"trigger_rate,omitempty"`
}

type workshopTriggerResults struct {
	CurrentDescription string                  `json:"current_description,omitempty"`
	CurrentScore       float64                 `json:"current_score"`
	BestDescription    string                  `json:"best_description,omitempty"`
	BestScore          float64                 `json:"best_score"`
	Queries            []workshopTriggerResult `json:"queries"`
}

type workshopTriggerResponse struct {
	Name    string                  `json:"name"`
	Queries []workshopTriggerQuery  `json:"queries"`
	Results *workshopTriggerResults `json:"results"`
}

// workshopArtifactsDir locates the workspace directory a skill's runs write
// into: a sibling of the skill's own directory named "<name>-workspace". It
// is computed from the versioned root, so a draft's workshop reads the same
// workspace its formal twin does.
func workshopArtifactsDir(dir, name string) string {
	parent := filepath.Dir(dir)
	if filepath.Base(parent) == ".drafts" {
		parent = filepath.Dir(parent)
	}
	return filepath.Join(parent, name+"-workspace")
}

// handleSkillWorkshopEvals serves the evaluation rounds a skill's workshop
// produced: every iteration directory, its per-case grading, outputs and
// feedback.
func (s *Server) handleSkillWorkshopEvals(w http.ResponseWriter, r *http.Request) {
	s.handleSkillWorkshopEvalsWith(agentSkillScope(), w, r)
}

func (s *Server) handleSkillWorkshopEvalsWith(launch safety.ProjectContext, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("name"))
	if name == "" {
		http.NotFound(w, r)
		return
	}
	dir, _, ok := s.workshopSkillDir(launch, name, false)
	if !ok {
		http.Error(w, "skill not found", http.StatusNotFound)
		return
	}
	ws := workshopArtifactsDir(dir, name)
	resp := workshopEvalsResponse{Name: name, Workspace: ws, Iterations: []workshopIteration{}}
	evals := readSkillEvalsJSON(filepath.Join(dir, "evals", "evals.json"))
	if entries, err := os.ReadDir(ws); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "iteration-") {
				continue
			}
			iter := readWorkshopIteration(filepath.Join(ws, entry.Name()), entry.Name(), evals)
			resp.Iterations = append(resp.Iterations, iter)
		}
	}
	// Newest round first: the round the user just ran is the one on screen.
	sort.Slice(resp.Iterations, func(i, j int) bool { return resp.Iterations[i].Order > resp.Iterations[j].Order })
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleSkillWorkshopFeedback merges one case's feedback into the round's
// feedback.json, the shape generate_review.py produces and run_loop reads.
func (s *Server) handleSkillWorkshopFeedback(w http.ResponseWriter, r *http.Request) {
	s.handleSkillWorkshopFeedbackWith(agentSkillScope(), w, r)
}

func (s *Server) handleSkillWorkshopFeedbackWith(launch safety.ProjectContext, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("name"))
	iteration := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("iteration"))
	if name == "" || iteration == "" {
		http.NotFound(w, r)
		return
	}
	dir, _, ok := s.workshopSkillDir(launch, name, false)
	if !ok {
		http.Error(w, "skill not found", http.StatusNotFound)
		return
	}
	var body struct {
		RunID    string `json:"run_id"`
		Feedback string `json:"feedback"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	runID := strings.TrimSpace(body.RunID)
	if runID == "" {
		http.Error(w, "run_id is required", http.StatusBadRequest)
		return
	}
	ws := workshopArtifactsDir(dir, name)
	iterDir := filepath.Join(ws, filepath.Base(iteration))
	if info, err := os.Stat(iterDir); err != nil || !info.IsDir() {
		http.Error(w, "iteration not found", http.StatusNotFound)
		return
	}
	path := filepath.Join(iterDir, "feedback.json")
	doc := readFeedbackDoc(path)
	replaced := false
	for i := range doc.Reviews {
		if strings.EqualFold(strings.TrimSpace(doc.Reviews[i].RunID), runID) {
			doc.Reviews[i].Feedback = body.Feedback
			replaced = true
			break
		}
	}
	if !replaced {
		doc.Reviews = append(doc.Reviews, feedbackReview{RunID: runID, Feedback: body.Feedback})
	}
	doc.Status = "complete"
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

// handleSkillWorkshopTrigger serves a skill's trigger eval set (the draft's
// evals/trigger_queries.json, so it travels with the version) and the most
// recent trigger run's results.
func (s *Server) handleSkillWorkshopTrigger(w http.ResponseWriter, r *http.Request) {
	s.handleSkillWorkshopTriggerWith(agentSkillScope(), w, r)
}

func (s *Server) handleSkillWorkshopTriggerWith(launch safety.ProjectContext, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("name"))
	if name == "" {
		http.NotFound(w, r)
		return
	}
	dir, _, ok := s.workshopSkillDir(launch, name, false)
	if !ok {
		http.Error(w, "skill not found", http.StatusNotFound)
		return
	}
	resp := workshopTriggerResponse{Name: name, Queries: []workshopTriggerQuery{}}
	resp.Queries = readTriggerQueries(filepath.Join(dir, "evals", "trigger_queries.json"))
	resp.Results = readTriggerResults(filepath.Join(workshopArtifactsDir(dir, name), "trigger-results.json"))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// The project-scoped mirrors.
func (s *Server) handleProjectSkillWorkshopEvals(w http.ResponseWriter, r *http.Request) {
	launch, ok := s.projectSkillLaunch(w, r)
	if !ok {
		return
	}
	s.handleSkillWorkshopEvalsWith(launch, w, r)
}

func (s *Server) handleProjectSkillWorkshopFeedback(w http.ResponseWriter, r *http.Request) {
	launch, ok := s.projectSkillLaunch(w, r)
	if !ok {
		return
	}
	s.handleSkillWorkshopFeedbackWith(launch, w, r)
}

func (s *Server) handleProjectSkillWorkshopTrigger(w http.ResponseWriter, r *http.Request) {
	launch, ok := s.projectSkillLaunch(w, r)
	if !ok {
		return
	}
	s.handleSkillWorkshopTriggerWith(launch, w, r)
}

// ---- reading the files the runs leave behind ----

type skillEvalEntry struct {
	ID       int
	Prompt   string
	Expected string
}

// readSkillEvalsJSON reads the skill's evals/evals.json into an ordered list.
func readSkillEvalsJSON(path string) []skillEvalEntry {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc struct {
		Evals []struct {
			ID             int    `json:"id"`
			Prompt         string `json:"prompt"`
			ExpectedOutput string `json:"expected_output"`
		} `json:"evals"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	out := make([]skillEvalEntry, 0, len(doc.Evals))
	for _, e := range doc.Evals {
		out = append(out, skillEvalEntry{ID: e.ID, Prompt: e.Prompt, Expected: e.ExpectedOutput})
	}
	return out
}

func readWorkshopIteration(dir, name string, evals []skillEvalEntry) workshopIteration {
	iter := workshopIteration{Name: name, Order: iterationOrder(name), Cases: []workshopEvalCase{}}
	if info, err := os.Stat(dir); err == nil {
		iter.Modified = info.ModTime().Unix()
	}
	feedback := readFeedbackDoc(filepath.Join(dir, "feedback.json"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return iter
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "eval-") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for i, evalDir := range names {
		iter.Cases = append(iter.Cases, readWorkshopEvalCase(filepath.Join(dir, evalDir), evalDir, i, evals, feedback))
	}
	return iter
}

func readWorkshopEvalCase(dir, evalDir string, index int, evals []skillEvalEntry, feedback feedbackDoc) workshopEvalCase {
	c := workshopEvalCase{
		ID:      evalDir,
		RunID:   evalDir + "-with_skill",
		Configs: map[string]workshopEvalConfig{},
	}
	if index < len(evals) {
		c.Prompt = evals[index].Prompt
		c.Expected = evals[index].Expected
	}
	// eval_metadata.json carries the descriptive name and the prompt the run
	// was actually given; prefer it over the evals.json index guess.
	if meta := readEvalMetadata(filepath.Join(dir, "eval_metadata.json")); meta != nil {
		if strings.TrimSpace(meta.EvalName) != "" {
			c.Name = meta.EvalName
		}
		if strings.TrimSpace(meta.Prompt) != "" {
			c.Prompt = meta.Prompt
		}
	}
	configs, err := os.ReadDir(dir)
	if err == nil {
		for _, entry := range configs {
			if !entry.IsDir() {
				continue
			}
			config := entry.Name()
			switch config {
			case "with_skill", "without_skill", "old_skill":
				c.Configs[config] = readEvalConfig(filepath.Join(dir, config))
			}
		}
	}
	c.Feedback = feedbackFor(feedback, evalDir)
	return c
}

func readEvalConfig(dir string) workshopEvalConfig {
	cfg := workshopEvalConfig{}
	grading := filepath.Join(dir, "grading.json")
	if data, err := os.ReadFile(grading); err == nil {
		var doc struct {
			Expectations []struct {
				Passed bool `json:"passed"`
			} `json:"expectations"`
			Summary *struct {
				Passed   int     `json:"passed"`
				Total    int     `json:"total"`
				PassRate float64 `json:"pass_rate"`
			} `json:"summary"`
		}
		if json.Unmarshal(data, &doc) == nil {
			cfg.Present = true
			passed := 0
			for _, e := range doc.Expectations {
				if e.Passed {
					passed++
				}
			}
			cfg.Passed = passed
			cfg.Total = len(doc.Expectations)
			if doc.Summary != nil {
				cfg.Passed = doc.Summary.Passed
				cfg.Total = doc.Summary.Total
				cfg.PassRate = doc.Summary.PassRate
			}
			if cfg.Total > 0 && cfg.PassRate == 0 {
				cfg.PassRate = float64(cfg.Passed) / float64(cfg.Total)
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, "timing.json")); err == nil {
		var doc struct {
			TotalDurationSeconds float64 `json:"total_duration_seconds"`
		}
		if json.Unmarshal(data, &doc) == nil {
			cfg.TimeSeconds = doc.TotalDurationSeconds
		}
	}
	cfg.Output = readFirstOutputText(filepath.Join(dir, "outputs"))
	return cfg
}

// readFirstOutputText returns the first regular file under outputs/, cut to
// the cap: the view shows one representative artifact's text, not a tree.
func readFirstOutputText(dir string) string {
	var first string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || first != "" {
			return nil
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		first = path
		return fs.SkipAll
	})
	if first == "" {
		return ""
	}
	data, err := os.ReadFile(first)
	if err != nil {
		return ""
	}
	if len(data) > workshopOutputMaxBytes {
		data = data[:workshopOutputMaxBytes]
	}
	return string(data)
}

func iterationOrder(name string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(name, "iteration-"))
	if err != nil {
		return 0
	}
	return n
}

type evalMetadata struct {
	EvalName string `json:"eval_name"`
	Prompt   string `json:"prompt"`
}

func readEvalMetadata(path string) *evalMetadata {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var meta evalMetadata
	if json.Unmarshal(data, &meta) != nil {
		return nil
	}
	return &meta
}

// feedbackReview / feedbackDoc mirror the shape generate_review.py writes.
type feedbackReview struct {
	RunID     string `json:"run_id"`
	Feedback  string `json:"feedback"`
	Timestamp string `json:"timestamp,omitempty"`
}

type feedbackDoc struct {
	Reviews []feedbackReview `json:"reviews"`
	Status  string           `json:"status,omitempty"`
}

func readFeedbackDoc(path string) feedbackDoc {
	data, err := os.ReadFile(path)
	if err != nil {
		return feedbackDoc{}
	}
	var doc feedbackDoc
	if json.Unmarshal(data, &doc) != nil {
		return feedbackDoc{}
	}
	return doc
}

// feedbackFor joins every non-empty review whose run_id names one of this
// case's configurations; the case's own run_id is what a save writes back.
func feedbackFor(doc feedbackDoc, evalDir string) string {
	var parts []string
	for _, review := range doc.Reviews {
		if !strings.HasPrefix(strings.TrimSpace(review.RunID), evalDir+"-") {
			continue
		}
		if text := strings.TrimSpace(review.Feedback); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

// readTriggerQueries reads evals/trigger_queries.json. The workshop's own
// convention is {query, should_trigger}; the prototype's short {q, trigger}
// is accepted too so an older set still renders.
func readTriggerQueries(path string) []workshopTriggerQuery {
	data, err := os.ReadFile(path)
	if err != nil {
		return []workshopTriggerQuery{}
	}
	var rows []map[string]any
	if json.Unmarshal(data, &rows) != nil {
		return []workshopTriggerQuery{}
	}
	out := make([]workshopTriggerQuery, 0, len(rows))
	for _, row := range rows {
		q := firstString(row, "query", "q")
		if strings.TrimSpace(q) == "" {
			continue
		}
		out = append(out, workshopTriggerQuery{
			Query:         q,
			ShouldTrigger: firstBool(row, "should_trigger", "trigger"),
		})
	}
	return out
}

// readTriggerResults reads the trigger run_loop output saved beside the
// workspace. It exposes the current/best scores and each query's verdict.
func readTriggerResults(path string) *workshopTriggerResults {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc struct {
		OriginalDescription string  `json:"original_description"`
		BestDescription     string  `json:"best_description"`
		BestScore           float64 `json:"best_score"`
		History             []struct {
			Description string `json:"description"`
			Results     []struct {
				Query         string  `json:"query"`
				ShouldTrigger bool    `json:"should_trigger"`
				Pass          bool    `json:"pass"`
				TriggerRate   float64 `json:"trigger_rate"`
			} `json:"results"`
		} `json:"history"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	out := &workshopTriggerResults{
		BestDescription: doc.BestDescription,
		BestScore:       doc.BestScore,
		Queries:         []workshopTriggerResult{},
	}
	if len(doc.History) > 0 {
		current := doc.History[0]
		out.CurrentDescription = current.Description
		passed := 0
		for _, r := range current.Results {
			if r.Pass {
				passed++
			}
			out.Queries = append(out.Queries, workshopTriggerResult{
				Query:         r.Query,
				ShouldTrigger: r.ShouldTrigger,
				Pass:          r.Pass,
				TriggerRate:   r.TriggerRate,
			})
		}
		if len(current.Results) > 0 {
			out.CurrentScore = float64(passed) / float64(len(current.Results))
		}
	}
	if out.CurrentDescription == "" {
		out.CurrentDescription = doc.OriginalDescription
	}
	return out
}

func firstString(row map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := row[key].(string); ok {
			return v
		}
	}
	return ""
}

func firstBool(row map[string]any, keys ...string) bool {
	for _, key := range keys {
		if v, ok := row[key].(bool); ok {
			return v
		}
	}
	return false
}
