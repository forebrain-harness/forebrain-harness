// Primary agent identity: resolution, roster, and persisted selection.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type Summary struct {
	ID                string   `json:"id"`
	Name              string   `json:"name,omitempty"`
	Description       string   `json:"description,omitempty"`
	WorkspaceRoot     string   `json:"workspace_root"`
	PrivateSkillsRoot string   `json:"private_skills_root"`
	SharedSkillsRoots []string `json:"shared_skills_roots"`
	Active            bool     `json:"active"`
	Status            string   `json:"status,omitempty"`
}

type Paths struct {
	Active            Summary   `json:"active"`
	All               []Summary `json:"all"`
	SiblingWorkspaces []string  `json:"sibling_workspaces,omitempty"`
}

const (
	mainAgentID     = "main"
	statusActive    = "active"
	statusAvailable = "available"
)

type Resolver struct {
	home       string
	all        []Summary
	byID       map[string]Summary
	lookupByID map[string]string
	activeID   string
}

func NewResolver(home string, cfg *Root) (*Resolver, error) {
	trimmedHome := strings.TrimSpace(home)
	if trimmedHome == "" {
		return nil, fmt.Errorf("primary agent home is empty")
	}
	absHome, err := filepath.Abs(trimmedHome)
	if err != nil {
		return nil, fmt.Errorf("resolve primary agent home: %w", err)
	}
	if cfg == nil {
		cfg = &Root{}
	}
	r := &Resolver{
		home:       filepath.Clean(absHome),
		byID:       make(map[string]Summary),
		lookupByID: make(map[string]string),
	}
	if err := r.build(cfg); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Resolver) Active() (Summary, error) {
	sum, ok := r.byID[r.activeID]
	if !ok {
		return Summary{}, fmt.Errorf("active primary agent unavailable")
	}
	return cloneSummary(sum), nil
}

// ActiveStateRoot returns the directory under which the active primary agent's
// per-agent state/ tree lives — its workspace root. Per-agent stores join
// "state" onto this, so the main agent gets <home>/workspace/state and others
// get <home>/workspaces/<id>/state, isolating state between primary agents.
//
// It is the single source of truth every per-agent-state seam must use so they
// agree on paths (e.g. the plan file resolved by AgentContext must match the one
// write_file/edit_file are gated to in plan mode). On any resolution
// failure it falls back to the main
// agent's default workspace (<home>/workspace), which is always valid.
func ActiveStateRoot(home string, cfg *Root) string {
	fallback := filepath.Join(strings.TrimSpace(home), "workspace")
	resolver, err := NewResolver(home, cfg)
	if err != nil {
		return fallback
	}
	active, err := resolver.Active()
	if err != nil {
		return fallback
	}
	if root := strings.TrimSpace(active.WorkspaceRoot); root != "" {
		return root
	}
	return fallback
}

// ActiveID returns the id of the active primary agent — the tenant that owns
// the sessions it holds and the memories extracted from them.
//
// It falls back to the main agent exactly where ActiveStateRoot falls back to
// main's workspace, so the tenant a surface records in the database always
// names the workspace that surface is actually reading and writing.
func ActiveID(home string, cfg *Root) string {
	resolver, err := NewResolver(home, cfg)
	if err != nil {
		return mainAgentID
	}
	active, err := resolver.Active()
	if err != nil {
		return mainAgentID
	}
	if id := strings.TrimSpace(active.ID); id != "" {
		return id
	}
	return mainAgentID
}

func (r *Resolver) All() []Summary {
	out := make([]Summary, len(r.all))
	for i, sum := range r.all {
		out[i] = cloneSummary(sum)
	}
	return out
}

func (r *Resolver) Paths() (Paths, error) {
	active, err := r.Active()
	if err != nil {
		return Paths{}, err
	}
	siblings := make([]string, 0, len(r.all))
	for _, item := range r.all {
		if item.ID == active.ID {
			continue
		}
		siblings = append(siblings, item.WorkspaceRoot)
	}
	return Paths{
		Active:            cloneSummary(active),
		All:               r.All(),
		SiblingWorkspaces: siblings,
	}, nil
}

func (r *Resolver) Resolve(query string) (Summary, error) {
	q := strings.TrimSpace(query)
	switch {
	case strings.EqualFold(q, "default"), strings.EqualFold(q, "agent"):
		q = mainAgentID
	case q == "":
		return Summary{}, fmt.Errorf("primary agent query is empty")
	}
	if id, ok := r.lookupByID[strings.ToLower(q)]; ok {
		return r.byID[id], nil
	}
	matches := make([]Summary, 0, 1)
	prefix := strings.ToLower(q)
	for _, item := range r.all {
		if strings.HasPrefix(strings.ToLower(item.ID), prefix) {
			matches = append(matches, item)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return Summary{}, fmt.Errorf("primary agent %q not found", query)
	default:
		return Summary{}, fmt.Errorf("primary agent query %q is ambiguous", query)
	}
}

func (r *Resolver) Switch(query string) (Summary, error) {
	match, err := r.Resolve(query)
	if err != nil {
		return Summary{}, err
	}
	if err := EnsurePrimaryAgentDirs(match); err != nil {
		return Summary{}, err
	}
	if err := writeActiveState(r.home, match.ID); err != nil {
		return Summary{}, err
	}
	r.setActive(match.ID)
	return cloneSummary(r.byID[match.ID]), nil
}

func (r *Resolver) build(cfg *Root) error {
	defs := primaryDefinitions(cfg)
	ids := make([]string, 0, len(defs))
	for id := range defs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if ids[i] == mainAgentID {
			return true
		}
		if ids[j] == mainAgentID {
			return false
		}
		return ids[i] < ids[j]
	})

	summaries := make([]Summary, 0, len(ids))
	for _, id := range ids {
		sum, err := r.summaryFor(id, defs[id])
		if err != nil {
			return err
		}
		summaries = append(summaries, sum)
	}
	if err := validateNoOverlap(summaries, r.home); err != nil {
		return err
	}

	r.all = summaries
	r.byID = make(map[string]Summary, len(summaries))
	r.lookupByID = make(map[string]string, len(summaries))
	for _, sum := range summaries {
		r.byID[sum.ID] = sum
		r.lookupByID[strings.ToLower(sum.ID)] = sum.ID
	}

	activeID := canonicalActiveID(readActiveState(r.home), r.lookupByID)
	if activeID == "" {
		activeID = mainAgentID
	}
	if _, ok := r.byID[activeID]; !ok {
		activeID = mainAgentID
	}
	if _, ok := r.byID[activeID]; !ok {
		return fmt.Errorf("primary agent %q is not defined", mainAgentID)
	}
	r.setActive(activeID)
	return nil
}

func (r *Resolver) summaryFor(id string, def AgentDefinition) (Summary, error) {
	root, err := workspaceRootFor(r.home, id)
	if err != nil {
		return Summary{}, fmt.Errorf("resolve primary agent %q workspace: %w", id, err)
	}
	return Summary{
		ID:                id,
		Name:              strings.TrimSpace(def.DisplayName),
		Description:       strings.TrimSpace(def.Description),
		WorkspaceRoot:     root,
		PrivateSkillsRoot: filepath.Join(root, "skills"),
		SharedSkillsRoots: []string{
			filepath.Join(r.home, ".forebrain", "skills"),
			filepath.Join(r.home, "skills"),
			filepath.Join(r.home, ".agents", "skills"),
			filepath.Join(r.home, "skills", ".system"),
		},
		Status: statusAvailable,
	}, nil
}

func (r *Resolver) setActive(activeID string) {
	r.activeID = activeID
	for i := range r.all {
		r.all[i].Active = r.all[i].ID == activeID
		if r.all[i].Active {
			r.all[i].Status = statusActive
		} else {
			r.all[i].Status = statusAvailable
		}
		r.byID[r.all[i].ID] = r.all[i]
	}
}

func primaryDefinitions(cfg *Root) map[string]AgentDefinition {
	out := map[string]AgentDefinition{}
	if cfg != nil {
		for id, def := range cfg.Agents.Definitions {
			if IsEffectivePrimaryAgent(id, def) {
				out[strings.TrimSpace(id)] = def
			}
		}
	}
	if _, ok := out[mainAgentID]; !ok {
		def := AgentDefinition{}
		if cfg != nil && cfg.Agents.Definitions != nil {
			def = cfg.Agents.Definitions[mainAgentID]
		}
		out[mainAgentID] = def
	}
	return out
}

func canonicalActiveID(raw string, lookup map[string]string) string {
	if raw == "" {
		return ""
	}
	return lookup[strings.ToLower(strings.TrimSpace(raw))]
}

// workspaceRootFor returns the forebrain-default workspace root for an agent id.
// Workspace location is not user-configurable: "main" lives at <home>/workspace
// and every other primary agent at <home>/workspaces/<id>.
func workspaceRootFor(home, id string) (string, error) {
	workspace := "workspace"
	if id != mainAgentID {
		workspace = filepath.ToSlash(filepath.Join("workspaces", id))
	}
	root := filepath.Clean(filepath.FromSlash(workspace))
	if !filepath.IsAbs(root) {
		root = filepath.Join(home, root)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absRoot), nil
}

// EnsurePrimaryAgentDirs materialises a primary agent's workspace
// subdirectories. Exported for the gateway's create path so a tenant created
// over the web gets the same directories a switch would.
func EnsurePrimaryAgentDirs(sum Summary) error {
	dirs := []string{
		sum.WorkspaceRoot,
		sum.PrivateSkillsRoot,
	}
	dirs = append(dirs, sum.SharedSkillsRoots...)
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func validateNoOverlap(items []Summary, home string) error {
	reserved := []string{
		filepath.Join(home, "memories"),
		filepath.Join(home, "skills"),
		filepath.Join(home, "state"),
		filepath.Join(home, "runs"),
	}
	canonicalReserved := make([]string, 0, len(reserved))
	for _, path := range reserved {
		canonical, err := canonicalPath(path)
		if err != nil {
			return err
		}
		canonicalReserved = append(canonicalReserved, canonical)
	}

	canonicalItems := make([]string, len(items))
	for i, item := range items {
		root, err := canonicalPath(item.WorkspaceRoot)
		if err != nil {
			return fmt.Errorf("canonicalize workspace for %q: %w", item.ID, err)
		}
		canonicalItems[i] = root
		for _, reservedRoot := range canonicalReserved {
			if sameOrInside(root, reservedRoot) || sameOrInside(reservedRoot, root) {
				return fmt.Errorf("primary agent %q workspace is inside reserved directory %s", item.ID, item.WorkspaceRoot)
			}
		}
	}

	for _, item := range canonicalItems {
		for _, reservedRoot := range canonicalReserved {
			if sameOrInside(reservedRoot, item) && reservedRoot != item {
				return fmt.Errorf("primary agent workspace %s overlaps reserved directory %s", item, reservedRoot)
			}
		}
	}

	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if sameOrInside(canonicalItems[i], canonicalItems[j]) || sameOrInside(canonicalItems[j], canonicalItems[i]) {
				return fmt.Errorf("primary agent workspaces overlap: %s and %s", items[i].ID, items[j].ID)
			}
		}
	}
	return nil
}

func canonicalPath(path string) (string, error) {
	if strings.ContainsRune(path, '\x00') {
		return "", fmt.Errorf("path contains NUL")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	cleaned := filepath.Clean(absPath)
	cleaned = normalizeDarwinSystemSymlinkPrefix(cleaned)
	if _, err := os.Lstat(cleaned); err == nil {
		resolved, err := filepath.EvalSymlinks(cleaned)
		if err != nil {
			return "", err
		}
		cleaned = filepath.Clean(resolved)
		cleaned = normalizeDarwinSystemSymlinkPrefix(cleaned)
	}
	if caseInsensitivePlatform() {
		cleaned = strings.ToLower(cleaned)
	}
	return cleaned, nil
}

func normalizeDarwinSystemSymlinkPrefix(path string) string {
	if runtime.GOOS != "darwin" {
		return path
	}
	switch {
	case path == "/var":
		return "/private/var"
	case strings.HasPrefix(path, "/var/"):
		return "/private" + path
	case path == "/tmp":
		return "/private/tmp"
	case strings.HasPrefix(path, "/tmp/"):
		return "/private" + path
	case path == "/etc":
		return "/private/etc"
	case strings.HasPrefix(path, "/etc/"):
		return "/private" + path
	default:
		return path
	}
}

func sameOrInside(path, parent string) bool {
	if path == parent {
		return true
	}
	rel, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	return rel != ".." && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func caseInsensitivePlatform() bool {
	switch runtime.GOOS {
	case "windows", "darwin":
		return true
	default:
		return false
	}
}

func cloneSummary(sum Summary) Summary {
	clone := sum
	if sum.SharedSkillsRoots != nil {
		clone.SharedSkillsRoots = append([]string(nil), sum.SharedSkillsRoots...)
	}
	return clone
}

type RosterRow struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	ParentID       string `json:"parent_id,omitempty"`
	Label          string `json:"label"`
	Status         string `json:"status"`
	Title          string `json:"title,omitempty"`
	Task           string `json:"task,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	RunID          string `json:"run_id,omitempty"`
	ElapsedSeconds int    `json:"elapsed_seconds,omitempty"`
	TokenCount     int    `json:"token_count,omitempty"`
	ToolCount      int    `json:"tool_count,omitempty"`
	FileCount      int    `json:"file_count,omitempty"`
}

type Roster struct {
	Records []RosterRow `json:"records"`
}

type CancelSummary struct {
	Main      int `json:"main"`
	Subagents int `json:"subagents"`
}

type activeState struct {
	Active    string `json:"active"`
	UpdatedAt string `json:"updated_at"`
}

func statePath(home string) string {
	return filepath.Join(strings.TrimSpace(home), "state", "primary-agent.json")
}

func readActiveState(home string) string {
	raw, err := os.ReadFile(statePath(home))
	if err != nil {
		return ""
	}
	var st activeState
	if err := json.Unmarshal(raw, &st); err != nil {
		return ""
	}
	return strings.TrimSpace(st.Active)
}

func writeActiveState(home, id string) error {
	p := statePath(home)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	body, err := json.Marshal(activeState{
		Active:    strings.TrimSpace(id),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(body, '\n'), 0o600)
}
