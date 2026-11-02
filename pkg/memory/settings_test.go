package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	state "github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// TestMemorySwitchIsTheOnlySwitch pins the owner's one-switch rule (plan 205
// §3.1): memory off turns the read path, the write path, and the dedicated
// tools off together, and memory on turns all three on. There is no
// configuration that reads memories it may not write.
func TestMemorySwitchIsTheOnlySwitch(t *testing.T) {
	for _, test := range []struct {
		name    string
		feature *bool
		want    bool
	}{
		{name: "off", feature: appcfg.BoolPtr(false), want: false},
		{name: "on", feature: appcfg.BoolPtr(true), want: true},
		{name: "unset takes the default", feature: nil, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &appcfg.Root{}
			cfg.Features.Memories = test.feature
			if got := Enabled(cfg); got != test.want {
				t.Fatalf("Enabled=%v want %v", got, test.want)
			}
			opts := InstructionOptionsFromConfig(cfg)
			want := InstructionOptions{Recall: test.want, Capture: test.want, MemoryTools: test.want}
			if opts != want {
				t.Fatalf("instruction options = %#v want %#v", opts, want)
			}
			if got, want := SettingsFromConfig(cfg).Enabled, test.want; got != want {
				t.Fatalf("settings.Enabled=%v want %v", got, want)
			}
			if got := SessionModeFor(cfg); (got == ThreadMemoryEnabled) != test.want {
				t.Fatalf("SessionModeFor=%q want enabled==%v", got, test.want)
			}
		})
	}
}

// TestExternalContextPollutionIgnoresConfig pins owner ruling §0.3: using
// external context always keeps a session out of memory generation, with no
// setting left to turn that protection off.
func TestExternalContextPollutionIgnoresConfig(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	now := time.Now().Unix()
	insertMemoryThread(t, store, "thread", ThreadMemoryEnabled, SessionSourceTUI, now-100)
	cfg := &appcfg.Root{}
	cfg.Features.Memories = appcfg.BoolPtr(true)
	forget, err := MarkPollutedByExternalContext(ctx, cfg, store, "thread")
	if err != nil {
		t.Fatal(err)
	}
	if forget {
		t.Fatal("a thread that never contributed asked to enqueue a forgetting pass")
	}
	var mode string
	if err := store.DB.QueryRowContext(ctx, `SELECT memory_mode FROM fb_sessions WHERE id='thread'`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != threadMemoryPolluted {
		t.Fatalf("memory_mode=%q want %q", mode, threadMemoryPolluted)
	}
}

// TestSetThreadMemoryModeRefusesBatchSessions pins the single-session half of
// owner ruling §0.2: a batch project's task conversation cannot be talked into
// generating memories, whatever the process-wide switch says. Storing the
// disabled value it already has is allowed.
func TestSetThreadMemoryModeRefusesBatchSessions(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	projects := state.NewProjectStore(store.DB, store.AgentID())
	project, err := projects.Create(ctx, state.CreateProjectInput{Name: "batch", Root: t.TempDir(), ProjectType: state.ProjectTypeBatch})
	if err != nil {
		t.Fatal(err)
	}
	sessions := state.NewSessionStore(store.DB, store.AgentID())
	if err := sessions.EnsureAt(ctx, "batch-run", "batch-run", state.SessionBirth{ProjectID: project.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetThreadMemoryMode(ctx, "batch-run", ThreadMemoryEnabled); err == nil {
		t.Fatal("a batch task's conversation was switched on")
	} else if !strings.Contains(err.Error(), "后台跑批项目不使用记忆") {
		t.Fatalf("error = %v, want the batch rule's message", err)
	}
	var mode string
	if err := store.DB.QueryRowContext(ctx, `SELECT memory_mode FROM fb_sessions WHERE id='batch-run'`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != ThreadMemoryDisabled {
		t.Fatalf("memory_mode=%q after the refused switch, want disabled", mode)
	}
	if err := store.SetThreadMemoryMode(ctx, "batch-run", ThreadMemoryDisabled); err != nil {
		t.Fatalf("storing the disabled value a batch task already has: %v", err)
	}
}
