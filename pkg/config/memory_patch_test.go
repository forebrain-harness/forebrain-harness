package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"go.yaml.in/yaml/v2"
)

// A primary agent is a tenant, so no agent's memory root may equal or nest
// inside another's. Memories resolve from the agent's workspace root for the
// same reason mode/plan/todo state does.
func TestPrimaryAgentMemoryRootsAreIsolatedPerTenant(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Root{}
	cfg.Agents.Definitions = map[string]config.AgentDefinition{
		"main": {}, "acme": {Primary: true}, "globex": {Primary: true},
	}
	resolver, err := config.NewResolver(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	roots := map[string]string{}
	for _, sum := range resolver.All() {
		agentRoots, err := memory.ResolveRootsForAgent(sum.WorkspaceRoot)
		if err != nil {
			t.Fatal(err)
		}
		roots[sum.ID] = agentRoots.Base()
	}
	if want := filepath.Join(home, "workspace", "memories"); roots["main"] != want {
		t.Fatalf("main memory root=%s want %s", roots["main"], want)
	}
	if want := filepath.Join(home, "workspaces", "acme", "memories"); roots["acme"] != want {
		t.Fatalf("acme memory root=%s want %s", roots["acme"], want)
	}
	// A home-derived root is shared by every tenant and must never be produced.
	for id, root := range roots {
		if root == filepath.Join(home, "memories") {
			t.Fatalf("%s resolves a home-derived memory root %s", id, root)
		}
	}
	for a, ra := range roots {
		for b, rb := range roots {
			if a == b {
				continue
			}
			if ra == rb {
				t.Fatalf("%s and %s share memory root %s", a, b, ra)
			}
			rel, err := filepath.Rel(ra, rb)
			if err == nil && rel != ".." && !filepath.IsAbs(rel) && rel != "." && rel[0] != '.' {
				t.Fatalf("%s memory root %s is reachable from %s (%s)", b, rb, a, rel)
			}
		}
	}
}

func TestEffectiveMemoriesDefaults(t *testing.T) {
	got := (*config.Root)(nil).EffectiveMemories()
	if got.DisableOnExternalContext || !got.GenerateMemories || !got.UseMemories || !got.DedicatedTools ||
		got.MaxRawMemoriesForConsolidation != 256 || got.MaxUnusedDays != 30 ||
		got.MaxRolloutAgeDays != 10 || got.MaxRolloutsPerStartup != 2 ||
		got.MinRolloutIdleHours != 6 || got.MinRateLimitRemainingPercent != 25 ||
		got.ExtractModel != "" || got.ConsolidationModel != "" {
		t.Fatalf("defaults = %#v", got)
	}
}

func TestEffectiveMemoriesClampsNumericSettings(t *testing.T) {
	below, above := -10, 10_000
	got := (&config.Root{Memories: config.MemorySection{
		MaxRawMemoriesForConsolidation: &above,
		MaxUnusedDays:                  &below,
		MaxRolloutAgeDays:              &above,
		MaxRolloutsPerStartup:          &below,
		MinRolloutIdleHours:            &above,
		MinRateLimitRemainingPercent:   &below,
	}}).EffectiveMemories()
	if got.MaxRawMemoriesForConsolidation != 4096 || got.MaxUnusedDays != 0 ||
		got.MaxRolloutAgeDays != 90 || got.MaxRolloutsPerStartup != 1 ||
		got.MinRolloutIdleHours != 48 || got.MinRateLimitRemainingPercent != 0 {
		t.Fatalf("clamped settings = %#v", got)
	}
}

func TestPatchMemoryChangesOnlyRequestedSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	if err := os.WriteFile(path, []byte("name: unchanged\nfeatures:\n  memories: false\nmemories:\n  use_memories: false\n  generate_memories: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	enabled, use := true, true
	if err := config.PatchMemory(path, config.MemoryPatch{FeatureEnabled: &enabled, UseMemories: &use}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[interface{}]interface{}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	features := doc["features"].(map[interface{}]interface{})
	memories := doc["memories"].(map[interface{}]interface{})
	if features["memories"] != true || memories["use_memories"] != true || memories["generate_memories"] != true || doc["name"] != "unchanged" {
		t.Fatalf("patched document = %#v", doc)
	}
}
