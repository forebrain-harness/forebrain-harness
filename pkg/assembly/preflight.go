package assembly

import (
	"context"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
)

type Preflight struct {
	SessionID   string
	Snapshot    AssemblyResult
	HasSnap     bool
	Result      Result
	Did         bool
	CompactedAt time.Time
}

type PreflightConfig struct {
	HookContext hook.HookContext
	Input       string
	Now         func() time.Time
	Assemble    func(context.Context, hook.HookContext, string) (AssemblyResult, bool, error)
	AutoCompact func(context.Context, string, string) (Result, bool, error)
}

// RunPreflight is the pre-turn checkpoint. Compaction runs before the
// incoming user input is assembled into the next request.
func RunPreflight(ctx context.Context, cfg PreflightConfig) (Preflight, error) {
	now := time.Now
	if cfg.Now != nil {
		now = cfg.Now
	}
	out := Preflight{SessionID: strings.TrimSpace(cfg.HookContext.SessionID), CompactedAt: now()}
	if cfg.AutoCompact != nil {
		result, did, err := cfg.AutoCompact(ctx, out.SessionID, cfg.Input)
		if err != nil {
			return Preflight{}, err
		}
		out.Result, out.Did = result, did
	}
	if cfg.Assemble != nil {
		snapshot, ok, err := cfg.Assemble(ctx, cfg.HookContext, cfg.Input)
		if err != nil {
			return out, err
		}
		out.Snapshot, out.HasSnap = snapshot, ok
	}
	return out, nil
}
