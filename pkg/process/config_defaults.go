// The stored configuration's default values, in one registry (plan 207 §3.2).
//
// Every runtime section fb_config holds carries an explicit value from the
// first startup, so the settings surfaces show what is in force instead of an
// "unset" placeholder, and a fresh install and a migrated one hold the same
// rows. A setting added to Root gets its default here at the same time:
// TestStoredConfigDefaultsCoverEveryField fails until it does.
package process

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// defaultStoredRoot is the registry: a Root whose every stored setting carries
// the value the runtime runs on when nothing is configured.
//
// Each value is read back through the same accessor the runtime itself
// consults (EffectiveFeatures, EffectiveMemories, EffectiveLSP, …), so a
// default here cannot drift from the default in force. Settings whose "unset"
// value is not one of their own values get the explicit representation plan
// 207 §3.3 defines (none, builtin, current) rather than an empty string.
//
// The leaves left at their zero value — the model catalog's window, the
// tokenizer's own defaults, secrets nothing has set — are the ones listed in
// TestStoredConfigDefaultsCoverEveryField's zeroIsTheDefault table, each with
// the reason its zero is the setting rather than a placeholder.
func defaultStoredRoot() appcfg.Root {
	var root appcfg.Root

	root.ApprovalPreset = root.ApprovalPreset.Effective()
	root.SandboxWorkspaceWrite = appcfg.SandboxWorkspaceWrite{
		NetworkAccess: appcfg.BoolPtr(root.SandboxWorkspaceWrite.EffectiveNetworkAccess()),
	}
	root.Compact = appcfg.CompactSection{
		Prompt:             assembly.DefaultPrompt,
		AutoCompactPercent: root.Compact.EffectiveAutoCompactPercent(),
	}
	root.Cron = appcfg.CronSection{RetentionDays: appcfg.IntPtr(root.CronRetentionDays())}
	root.Agents = defaultStoredAgents()
	// An empty map, not nil: hooks is a section the panel opens, and a stored
	// `{}` is the row that says "no hook is configured" instead of no row.
	root.Hooks = appcfg.HooksSettings{}
	root.Memories = defaultStoredMemories(root)
	root.Batch = appcfg.BatchSection{SandboxDefault: appcfg.BoolPtr(root.Batch.EffectiveSandboxDefault())}
	root.Features = defaultStoredFeatures(root)
	root.LSP = defaultStoredLSP(root)
	return root
}

// defaultStoredAgents is the agents section as an install that configured
// nothing holds it.
func defaultStoredAgents() appcfg.AgentsSection {
	defaults := appcfg.AgentDefaults{}
	defaults.ContextInject.WarnRemainingPercent = appcfg.DefaultWarnRemainingPercent
	// The plan's floor: one subagent slot even on a single-core machine, where
	// NumCPU-1 would leave none.
	defaults.Execution.MaxParallelSubagents = appcfg.IntPtr(max(1, runtime.NumCPU()-1))
	defaults.Guardrails.Input.MaxRunes = appcfg.DefaultGuardrailsMaxRunes
	// Downloading a missing tokenizer is the out-of-the-box behavior; a
	// stored true says so, and an offline install flips it to false.
	defaults.TokenEstimate.Download = appcfg.BoolPtr(true)
	defaults.NetworkRetryMax = appcfg.IntPtr(defaults.NetworkRetryMaxOrDefault())
	defaults.StreamIdleTimeoutSeconds = appcfg.IntPtr(int(defaults.StreamIdleTimeout() / time.Second))
	// Subagent tools are opt-in, so the stored default is the explicit off.
	defaults.EnableSubagent = appcfg.BoolPtr(false)
	// Tavily's depth is the one non-secret setting under a provider block, so
	// the block exists to carry it; the other blocks hold only an api_key and
	// stay absent until one is set.
	defaults.WebSearch.Provider = appcfg.WebSearchProviderNone
	defaults.WebSearch.Tavily = &appcfg.WebSearchTavily{SearchDepth: "basic"}
	defaults.WebFetch.Provider = appcfg.WebFetchProviderBuiltin
	return appcfg.AgentsSection{
		Defaults:    defaults,
		Definitions: map[string]appcfg.AgentDefinition{},
	}
}

func defaultStoredMemories(root appcfg.Root) appcfg.MemorySection {
	effective := root.EffectiveMemories()
	return appcfg.MemorySection{
		MaxRawMemoriesForConsolidation: appcfg.IntPtr(effective.MaxRawMemoriesForConsolidation),
		MaxUnusedDays:                  appcfg.IntPtr(effective.MaxUnusedDays),
		MaxRolloutAgeDays:              appcfg.IntPtr(effective.MaxRolloutAgeDays),
		MaxRolloutsPerStartup:          appcfg.IntPtr(effective.MaxRolloutsPerStartup),
		MinRolloutIdleHours:            appcfg.IntPtr(effective.MinRolloutIdleHours),
		SearchTopK:                     appcfg.IntPtr(effective.SearchTopK),
		ExtractModel:                   appcfg.MemoryModelCurrent,
		ConsolidationModel:             appcfg.MemoryModelCurrent,
	}
}

func defaultStoredFeatures(root appcfg.Root) appcfg.FeaturesSection {
	effective := root.EffectiveFeatures()
	return appcfg.FeaturesSection{
		Memories:   appcfg.BoolPtr(effective.Memories),
		SkillOffer: appcfg.BoolPtr(effective.SkillOffer),
		LSP:        appcfg.BoolPtr(effective.LSP),
	}
}

func defaultStoredLSP(root appcfg.Root) appcfg.LSPSection {
	effective := root.EffectiveLSP()
	return appcfg.LSPSection{
		Recommendations: appcfg.BoolPtr(effective.Recommendations),
		MaxServers:      effective.MaxServers,
		IdleTimeout:     int(effective.IdleTimeout / time.Second),
		RequestTimeout:  int(effective.RequestTimeout / time.Second),
		Diagnostics: appcfg.LSPDiagnosticsConfig{
			AfterEdit:    appcfg.BoolPtr(effective.AfterEdit),
			WaitMS:       appcfg.IntPtr(int(effective.Wait / time.Millisecond)),
			MinSeverity:  effective.MinSeverity,
			MaxPerFile:   effective.MaxPerFile,
			MaxFiles:     effective.MaxFiles,
			LateDelivery: appcfg.BoolPtr(effective.LateDelivery),
		},
		Servers: map[string]appcfg.LSPServerConfig{},
	}
}

// emptyExplicitRepresentations names the string settings whose empty value
// used to be the setting's default and now carry an explicit representation
// (plan 207 §3.3), keyed by the section a row belongs to and the setting's path
// inside it. An empty one — from a file-era import, from a row written before
// the representation existed, or from a surface that cleared the field — is
// written as the value it means, so the settings pages never have to explain
// 「留空＝默认」.
var emptyExplicitRepresentations = map[string]map[string]string{
	"agents": {
		"defaults.web_search.provider": appcfg.WebSearchProviderNone,
		"defaults.web_fetch.provider":  appcfg.WebFetchProviderBuiltin,
	},
	"memories": {
		"extract_model":       appcfg.MemoryModelCurrent,
		"consolidation_model": appcfg.MemoryModelCurrent,
	},
}

// ensureStoredConfigDefaults gives every stored section the settings the
// database does not hold yet, keeping every value it does hold (plan 207
// §3.2). It runs on every startup, so a new setting reaches an existing
// install the same way it reaches a fresh one — one path, no per-setting
// migration — and a startup that finds nothing missing writes nothing.
//
// Only Root sections go through the store's UpdateSection: it validates the
// candidate, and a credential row (auth.*) or the meta marker is not a Root
// section to compose.
func ensureStoredConfigDefaults(ctx context.Context, store *state.ConfigStore) error {
	if store == nil {
		return fmt.Errorf("config store unavailable")
	}
	defaults := defaultStoredRoot()
	stored, err := store.LoadSections(ctx)
	if err != nil {
		return fmt.Errorf("read stored config sections: %w", err)
	}
	for _, section := range rootConfigSections {
		raw, err := MarshalRootSection(defaults, section)
		if err != nil {
			return err
		}
		if len(raw) == 0 {
			continue
		}
		merged, changed, err := fillMissingSectionValues(section, stored[section], raw)
		if err != nil {
			return fmt.Errorf("fill section %s defaults: %w", section, err)
		}
		if !changed {
			continue
		}
		next := merged
		if err := store.UpdateSection(ctx, section, func(json.RawMessage) (json.RawMessage, error) {
			return next, nil
		}); err != nil {
			return fmt.Errorf("store section %s defaults: %w", section, err)
		}
	}
	return nil
}

// fillMissingSectionValues returns stored with every key only defaults holds
// added to it — and every empty explicit representation named in
// emptyExplicitRepresentations written out — and reports whether anything
// changed. An absent section takes the defaults whole; a section that is not a
// JSON object (approval_preset is a bare string) is its own value already.
func fillMissingSectionValues(section string, stored, defaults json.RawMessage) (json.RawMessage, bool, error) {
	if len(strings.TrimSpace(string(stored))) == 0 {
		return defaults, true, nil
	}
	var have any
	if err := json.Unmarshal(stored, &have); err != nil {
		return nil, false, fmt.Errorf("read stored value: %w", err)
	}
	var want any
	if err := json.Unmarshal(defaults, &want); err != nil {
		return nil, false, fmt.Errorf("read default value: %w", err)
	}
	merged, changed := fillMissingValues(have, want)
	for path, representation := range emptyExplicitRepresentations[section] {
		if setEmptyRepresentation(merged, path, representation) {
			changed = true
		}
	}
	if !changed {
		return stored, false, nil
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// setEmptyRepresentation writes representation over the empty string at path,
// reporting whether it did. A key that is absent or holds anything else — a
// value the user set — is left alone.
func setEmptyRepresentation(doc any, path, representation string) bool {
	keys := strings.Split(path, ".")
	object, ok := doc.(map[string]any)
	for _, key := range keys[:len(keys)-1] {
		if !ok {
			return false
		}
		object, ok = object[key].(map[string]any)
	}
	if !ok {
		return false
	}
	last := keys[len(keys)-1]
	if value, ok := object[last].(string); ok && value == "" {
		object[last] = representation
		return true
	}
	return false
}

// fillMissingValues adds what only defaults holds. A key the section already
// has keeps its value whatever it is: a stored setting is the user's, and this
// only completes a section written before the setting existed. Nested objects
// are filled key by key; a list is taken whole or not at all, because merging
// list positions would rewrite entries the user owns.
func fillMissingValues(have, defaults any) (any, bool) {
	haveMap, ok := have.(map[string]any)
	if !ok {
		return have, false
	}
	defaultsMap, ok := defaults.(map[string]any)
	if !ok {
		return have, false
	}
	changed := false
	for key, want := range defaultsMap {
		cur, present := haveMap[key]
		if !present {
			haveMap[key] = want
			changed = true
			continue
		}
		if merged, filled := fillMissingValues(cur, want); filled {
			haveMap[key] = merged
			changed = true
		}
	}
	return haveMap, changed
}
