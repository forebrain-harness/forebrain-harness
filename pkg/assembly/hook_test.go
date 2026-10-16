package assembly

import (
	"context"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// The context panel's conversation size is counted with the session model's
// estimator when the hook has one, the unit the compaction decision uses.
func TestHookConversationTokensUseSessionEstimator(t *testing.T) {
	ctx := context.Background()
	sess := newCompactTestStore(t)
	for i := 0; i < 3; i++ {
		if _, err := sess.Append(ctx, "s1", "user", strings.Repeat("conversation text ", 40)); err != nil {
			t.Fatal(err)
		}
	}
	assemble := func(t *testing.T, h *Hook) int {
		t.Helper()
		res, ok, err := h.Assemble(ctx, hook.HookContext{Trigger: "user", SessionID: "s1"}, "next question")
		if err != nil || !ok {
			t.Fatalf("Assemble = ok %v, err %v", ok, err)
		}
		return res.ConversationTokens
	}

	byDefault := assemble(t, &Hook{Cfg: &appcfg.Root{}, Store: sess})
	if byDefault <= 0 {
		t.Fatalf("default ConversationTokens = %d, want a positive count", byDefault)
	}
	bySession := assemble(t, &Hook{
		Cfg:               &appcfg.Root{},
		Store:             sess,
		EstimatorProvider: func(string) llm.Estimator { return tenfoldEstimator{} },
	})
	if bySession <= byDefault {
		t.Fatalf("ConversationTokens with the session estimator = %d, want more than the default %d", bySession, byDefault)
	}
}
