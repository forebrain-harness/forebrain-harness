//go:build ignore

// gen_models_static.go regenerates models_static.go from a WorkBuddy product
// config: the answer to GET <endpoint>/v3/config, saved as JSON. It writes the
// catalog a surface falls back to when no live listing answers, and it writes
// exactly the picker's list — the conversation agent's declared models, in the
// agent's order, without the routing alias the official picker hides.
//
// Recipe (docs/plan/WORKBUDDY_MODEL_CATALOG_PLAN.md §5 Step 3 saves the JSON):
//
//	go run -tags fts5 plugins/subscriptions/workbuddy/gen_models_static.go /tmp/wb_product_config.json
package main

import (
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"strings"
	"time"
)

// The product identity the generated header names; keep in step with the
// plugin's constants (plugin.go).
const (
	productName    = "WorkBuddy"
	productVersion = "5.9.3"
	cliVersion     = "2.165.0"
)

// outputPath is where the table lands, relative to the repository root.
const outputPath = "plugins/subscriptions/workbuddy/models_static.go"

// surfaceAgentTag and routerModelID mirror models.go: the agent a picker reads,
// and the alias the picker hides.
const (
	surfaceAgentTag = "model:craft"
	routerModelID   = "auto"
)

// reasoningConfig is a model's reasoning block as the config spells it.
type reasoningConfig struct {
	Effort             string   `json:"effort"`
	SupportedEfforts   []string `json:"supportedEfforts"`
	DefaultEffort      string   `json:"defaultEffort"`
	CanDisableThinking *bool    `json:"canDisableThinking"`
}

// efforts is supportedEfforts, or the single effort the block names when it
// carries no set — the plugin's own fallback.
func (r *reasoningConfig) efforts() []string {
	if r == nil {
		return nil
	}
	if len(r.SupportedEfforts) > 0 {
		return append([]string(nil), r.SupportedEfforts...)
	}
	if effort := strings.TrimSpace(r.Effort); effort != "" {
		return []string{effort}
	}
	return nil
}

// modelConfig is one entry of the config's catalog.
type modelConfig struct {
	ID               string           `json:"id"`
	Name             string           `json:"name"`
	MaxInputTokens   int64            `json:"maxInputTokens"`
	MaxOutputTokens  int64            `json:"maxOutputTokens"`
	SupportsToolCall bool             `json:"supportsToolCall"`
	SupportsImages   bool             `json:"supportsImages"`
	IsDefault        bool             `json:"isDefault"`
	Disabled         bool             `json:"disabled"`
	Reasoning        *reasoningConfig `json:"reasoning"`
}

// agentConfig is one agent of the config: the surfaces it serves and the models
// its picker lists.
type agentConfig struct {
	Name   string   `json:"name"`
	Tags   []string `json:"tags"`
	Models []string `json:"models"`
}

// productConfig is the /v3/config answer, limited to the members read here.
type productConfig struct {
	Data struct {
		Models []modelConfig `json:"models"`
		Agents []agentConfig `json:"agents"`
	} `json:"data"`
}

// row is one table entry, flattened from the config.
type row struct {
	id, name         string
	in, out          int64
	toolCall, images bool
	isDefault        bool
	efforts          []string
	defaultEffort    string
	disableThinking  *bool
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: go run gen_models_static.go <product-config.json>\n")
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fail("read config: %v", err)
	}
	var parsed productConfig
	if err := json.Unmarshal(raw, &parsed); err != nil {
		fail("decode config: %v", err)
	}
	rows, err := surfaceRows(parsed)
	if err != nil {
		fail("%v", err)
	}
	source, err := render(rows)
	if err != nil {
		fail("render table: %v", err)
	}
	if err := os.WriteFile(outputPath, source, 0o644); err != nil {
		fail("write %s: %v", outputPath, err)
	}
	fmt.Printf("wrote %s: %d models\n", outputPath, len(rows))
}

// surfaceRows is the picker's list: the conversation agent's model ids, in
// order, without the routing alias, each joined to its catalog entry. An id the
// agent names but the catalog does not carry is an error, not a skipped row:
// the table would silently lose a model the picker shows.
func surfaceRows(parsed productConfig) ([]row, error) {
	agent, ok := surfaceAgent(parsed.Data.Agents)
	if !ok {
		return nil, fmt.Errorf("the config names no conversation agent (tag %q or name \"cli\")", surfaceAgentTag)
	}
	catalog := make(map[string]modelConfig, len(parsed.Data.Models))
	for _, model := range parsed.Data.Models {
		catalog[strings.TrimSpace(model.ID)] = model
	}
	rows := make([]row, 0, len(agent.Models))
	for _, id := range agent.Models {
		id = strings.TrimSpace(id)
		if id == "" || id == routerModelID {
			continue
		}
		model, listed := catalog[id]
		if !listed {
			return nil, fmt.Errorf("the conversation agent names %q, which the catalog does not carry", id)
		}
		if model.Disabled {
			continue
		}
		rows = append(rows, row{
			id:              id,
			name:            strings.TrimSpace(model.Name),
			in:              model.MaxInputTokens,
			out:             model.MaxOutputTokens,
			toolCall:        model.SupportsToolCall,
			images:          model.SupportsImages,
			isDefault:       model.IsDefault,
			efforts:         model.Reasoning.efforts(),
			defaultEffort:   strings.TrimSpace(model.Reasoning.defaultEffort()),
			disableThinking: model.Reasoning.canDisableThinking(),
		})
	}
	return rows, nil
}

// surfaceAgent is the config's conversation agent, by tag first and by name
// second — the same rule models.go applies at run time.
func surfaceAgent(agents []agentConfig) (agentConfig, bool) {
	for _, agent := range agents {
		for _, tag := range agent.Tags {
			if strings.TrimSpace(tag) == surfaceAgentTag {
				return agent, true
			}
		}
	}
	for _, agent := range agents {
		if strings.TrimSpace(agent.Name) == "cli" {
			return agent, true
		}
	}
	return agentConfig{}, false
}

func (r *reasoningConfig) defaultEffort() string {
	if r == nil {
		return ""
	}
	return r.DefaultEffort
}

func (r *reasoningConfig) canDisableThinking() *bool {
	if r == nil {
		return nil
	}
	return r.CanDisableThinking
}

// render writes the Go source. go/format fixes the layout, so the file is
// gofmt-clean the moment it lands.
func render(rows []row) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "// Code generated by gen_models_static.go from the WorkBuddy product config (%s %s, CLI %s, %s); DO NOT EDIT.\n",
		productName, productVersion, cliVersion, time.Now().Format("2006-01-02"))
	b.WriteString("package workbuddy\n\n")
	b.WriteString("var __boolTrue, __boolFalse = true, false\n\n")
	b.WriteString("func staticWorkBuddyModels() []workBuddyModel {\n\treturn []workBuddyModel{\n")
	for _, r := range rows {
		b.WriteString("\t\t{\n")
		fmt.Fprintf(&b, "\t\t\tID: %q, Name: %q,\n", r.id, r.name)
		fmt.Fprintf(&b, "\t\t\tMaxInputTokens: %d, MaxOutputTokens: %d,\n", r.in, r.out)
		fmt.Fprintf(&b, "\t\t\tSupportsToolCall: %t, SupportsImages: %t,\n", r.toolCall, r.images)
		fmt.Fprintf(&b, "\t\t\tIsDefault: %t,\n", r.isDefault)
		fmt.Fprintf(&b, "\t\t\tEfforts: %s,\n", stringList(r.efforts))
		fmt.Fprintf(&b, "\t\t\tDefaultEffort: %q,\n", r.defaultEffort)
		switch {
		case r.disableThinking == nil:
			b.WriteString("\t\t\tCanDisableThinking: nil,\n")
		case *r.disableThinking:
			b.WriteString("\t\t\tCanDisableThinking: &__boolTrue,\n")
		default:
			b.WriteString("\t\t\tCanDisableThinking: &__boolFalse,\n")
		}
		b.WriteString("\t\t},\n")
	}
	b.WriteString("\t}\n}\n")
	return format.Source([]byte(b.String()))
}

// stringList spells a Go string slice, empty when no level was named.
func stringList(values []string) string {
	if len(values) == 0 {
		return "[]string{}"
	}
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = fmt.Sprintf("%q", value)
	}
	return "[]string{" + strings.Join(quoted, ", ") + "}"
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
