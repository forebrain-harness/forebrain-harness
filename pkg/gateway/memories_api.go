package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
)

type memoriesSettingsRequest struct {
	FeatureEnabled   *bool  `json:"feature_enabled"`
	UseMemories      *bool  `json:"use_memories"`
	GenerateMemories *bool  `json:"generate_memories"`
	ThreadID         string `json:"thread_id"`
}

func (s *Server) handleMemoriesSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeMemoriesSettings(w, memory.SettingsFromConfig(s.liveCfg()))
	case http.MethodPost:
		if s == nil || s.Env == nil || strings.TrimSpace(s.Env.ConfigPath) == "" {
			http.Error(w, "memory settings unavailable", http.StatusServiceUnavailable)
			return
		}
		var req memoriesSettingsRequest
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			http.Error(w, "trailing JSON content", http.StatusBadRequest)
			return
		}
		if req.GenerateMemories != nil && strings.TrimSpace(req.ThreadID) != "" && (s.MemoryStore == nil || s.MemoryStore.DB == nil) {
			http.Error(w, "memory settings unavailable", http.StatusServiceUnavailable)
			return
		}
		patch := appcfg.MemoryPatch{
			FeatureEnabled:   req.FeatureEnabled,
			UseMemories:      req.UseMemories,
			GenerateMemories: req.GenerateMemories,
		}
		if err := appcfg.PatchMemory(s.Env.ConfigPath, patch); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := s.Env.ReloadConfig(); err != nil {
			http.Error(w, "settings saved but reload failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if req.GenerateMemories != nil && strings.TrimSpace(req.ThreadID) != "" && s.MemoryStore != nil {
			mode := memory.ThreadMemoryDisabled
			if *req.GenerateMemories {
				mode = memory.ThreadMemoryEnabled
			}
			if err := s.MemoryStore.SetThreadMemoryMode(r.Context(), req.ThreadID, mode); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		writeMemoriesSettings(w, memory.SettingsFromConfig(s.liveCfg()))
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func writeMemoriesSettings(w http.ResponseWriter, settings memory.Settings) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"settings": settings})
}

// handleMemoriesReset clears memory. By default it clears only the calling
// session's project scope, which is the common case (a user asking to forget
// what Forebrain Harness learned about the project they're in). Pass ?scope=all to wipe
// every scope of the calling primary agent instead — every project plus
// global preferences.
func (s *Server) handleMemoriesReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s == nil || s.MemoryStore == nil || s.MemoryStore.DB == nil || s.Runner == nil {
		http.Error(w, "memory reset unavailable", http.StatusServiceUnavailable)
		return
	}
	roots, err := memory.ResolveRootsForAgent(strings.TrimSpace(s.Runner.StateRoot()))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("scope")) == "all" {
		if err := s.MemoryStore.Reset(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := memory.Clear(roots.Scope(memory.GlobalScope())); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		scopes, err := roots.ListProjectScopes()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, scope := range scopes {
			if err := memory.Clear(roots.Scope(scope)); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		return
	}
	key := strings.TrimSpace(s.Runner.ProjectKey)
	if key == "" {
		http.Error(w, "this session has no project to reset; pass ?scope=all", http.StatusBadRequest)
		return
	}
	scope := memory.Scope{Kind: memory.ScopeProject, Key: key}
	if err := s.MemoryStore.ResetScope(r.Context(), scope); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := memory.Clear(roots.Scope(scope)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (s *Server) HandleMemoriesSlash(ctx context.Context, sessionID, channel string, args []string) (string, bool) {
	_ = ctx
	_ = sessionID
	_ = channel
	if len(args) != 0 {
		return "memories: usage /memories", true
	}
	return "Open memory settings to configure memory use and generation.", true
}
