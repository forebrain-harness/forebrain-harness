// Memory feature settings, config, and redaction.
package memory

import (
	"context"
	"regexp"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

type Settings struct {
	Enabled bool `json:"enabled"`
}

func SettingsFromConfig(cfg *appcfg.Root) Settings {
	if cfg == nil {
		return Settings{}
	}
	return Settings{Enabled: Enabled(cfg)}
}

// SessionModeFor returns the memory_mode a new session is born with. It is the
// single switch, so there is nothing else to consult here; a session whose
// project is a batch project is born disabled inside the store's own INSERT
// instead, where the project type it is being filed under is resolved (plan
// 205 §3.2).
func SessionModeFor(cfg *appcfg.Root) string {
	if Enabled(cfg) {
		return ThreadMemoryEnabled
	}
	return ThreadMemoryDisabled
}

// Enabled reports whether the memory system is active at all. One switch
// covers reading stored memories, generating new ones, and the dedicated
// memories_* tools (owner 2026-10-26): a configuration that keeps them on
// separate flags lets a turn recall memories it is forbidden to write.
func Enabled(cfg *appcfg.Root) bool {
	return cfg != nil && cfg.EffectiveFeatures().Memories
}

// InstructionOptionsFromConfig maps the memory switch onto every part of the
// per-turn instruction, so the prompt a turn receives cannot disagree with what
// the settings actually allow.
func InstructionOptionsFromConfig(cfg *appcfg.Root) InstructionOptions {
	enabled := Enabled(cfg)
	return InstructionOptions{
		Recall:      enabled,
		Capture:     enabled,
		MemoryTools: enabled,
	}
}

var (
	openAIKeyPattern    = regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`)
	awsAccessKeyPattern = regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)
	bearerTokenPattern  = regexp.MustCompile(`(?i:\bBearer)[ \t]+[A-Za-z0-9._~+/-]{16,}=*`)
	secretValuePattern  = regexp.MustCompile(`(?i)\b(api[_-]?key|token|secret|password)\b(\s*[:=]\s*)(["']?)[^\s"']{8,}`)
)

func redactSecrets(value string) string {
	value = bearerTokenPattern.ReplaceAllString(value, "Bearer [REDACTED_SECRET]")
	value = openAIKeyPattern.ReplaceAllString(value, "[REDACTED_SECRET]")
	value = awsAccessKeyPattern.ReplaceAllString(value, "[REDACTED_SECRET]")
	return secretValuePattern.ReplaceAllString(value, "$1$2$3[REDACTED_SECRET]")
}

// MarkPollutedByExternalContext marks the current session polluted after it
// used external context — web search, web fetch, an MCP tool result, OpenAI's
// own browsing — so its transcript is never extracted into memory. The rule is
// not configurable (owner 2026-10-26, plan 205 §0.3): what a browsing session
// learned may be someone else's content, and keeping it out of durable memory
// is worth the memories this costs. If the session already contributed to the
// Phase 2 baseline, it returns true so callers can trigger a same-primary
// consolidation pass that represents the deletion/forgetting in the workspace
// diff.
func MarkPollutedByExternalContext(ctx context.Context, cfg *appcfg.Root, store *Store, sessionID string) (bool, error) {
	if !Enabled(cfg) || store == nil {
		return false, nil
	}
	scope, selected, err := store.HasSelectedStage1Output(ctx, sessionID)
	if err != nil {
		return false, err
	}
	if err := store.markThreadPolluted(ctx, sessionID); err != nil {
		return false, err
	}
	if selected {
		if err := store.EnqueueGlobalPhase2(ctx, scope, 0); err != nil {
			return false, err
		}
	}
	return selected, nil
}
