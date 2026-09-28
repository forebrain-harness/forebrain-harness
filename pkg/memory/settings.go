// Memory feature settings, config, and redaction.
package memory

import (
	"context"
	"regexp"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

type Settings struct {
	Enabled          bool `json:"enabled"`
	UseMemories      bool `json:"use_memories"`
	GenerateMemories bool `json:"generate_memories"`
}

func SettingsFromConfig(cfg *appcfg.Root) Settings {
	if cfg == nil {
		return Settings{}
	}
	return Settings{
		Enabled:          Enabled(cfg),
		UseMemories:      ReadPathEnabled(cfg),
		GenerateMemories: GenerationEnabled(cfg),
	}
}

// SessionModeForConfig returns the memory_mode that should be stored on new
// sessions: "enabled" when generate_memories is on, "disabled" otherwise.
func SessionModeForConfig(cfg *appcfg.Root) string {
	if GenerationEnabled(cfg) {
		return ThreadMemoryEnabled
	}
	return ThreadMemoryDisabled
}

// Enabled reports whether the memory system is active at all.
func Enabled(cfg *appcfg.Root) bool {
	return cfg != nil && cfg.EffectiveFeatures().Memories
}

// ReadPathEnabled reports whether stored memories are made available to the
// model during a turn.
func ReadPathEnabled(cfg *appcfg.Root) bool {
	return Enabled(cfg) && cfg.EffectiveMemories().UseMemories
}

// DedicatedToolsEnabled reports whether the dedicated memory tools are
// registered for the agent.
func DedicatedToolsEnabled(cfg *appcfg.Root) bool {
	return ReadPathEnabled(cfg) && cfg.EffectiveMemories().DedicatedTools
}

// GenerationEnabled reports whether new memories are generated from turns.
func GenerationEnabled(cfg *appcfg.Root) bool {
	return Enabled(cfg) && cfg.EffectiveMemories().GenerateMemories
}

// InstructionOptionsFromConfig maps the memory settings onto the halves of the
// per-turn instruction, so the prompt a turn receives cannot disagree with what
// the settings actually allow.
func InstructionOptionsFromConfig(cfg *appcfg.Root) InstructionOptions {
	return InstructionOptions{
		Recall:         ReadPathEnabled(cfg),
		Capture:        GenerationEnabled(cfg),
		DedicatedTools: DedicatedToolsEnabled(cfg),
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

// MarkPollutedByExternalContext marks the current session polluted when the
// configured policy disables memory generation after external context use. If
// the session already contributed to the Phase 2 baseline, it returns true so
// callers can trigger a same-primary consolidation pass that represents the
// deletion/forgetting in the workspace diff.
func MarkPollutedByExternalContext(ctx context.Context, cfg *appcfg.Root, store *Store, sessionID string) (bool, error) {
	if !Enabled(cfg) || !cfg.EffectiveMemories().DisableOnExternalContext || store == nil {
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
