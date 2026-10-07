// Copyright 2026 Simone Vellei
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package skill

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v2"
)

const (
	// systemSkillsDirName is the directory under $FOREBRAIN_HOME/skills the
	// built-in skills are installed in. The scanner never descends into it
	// from its parent; skillLayers lists it as a root of its own.
	systemSkillsDirName  = ".system"
	skillFileName        = "SKILL.md"
	yamlFrontmatterDelim = "---"
	toolNameRead         = "read"
	toolNameWrite        = "write"
	toolNameEdit         = "edit"
	toolNameBash         = "bash"
)

// Skill represents the parsed contents of a SKILL.md file.
//
// Fields correspond to YAML frontmatter keys. Body contains the remaining
// content after the frontmatter.
type Skill struct {
	RootPath      string         `yaml:"-"`
	Name          string         `yaml:"name"`
	Description   string         `yaml:"description"`
	License       string         `yaml:"license,omitempty"`
	Compatibility string         `yaml:"compatibility,omitempty"`
	Metadata      map[string]any `yaml:"metadata,omitempty"`
	// AllowedTools is the skill's declared tool set, always normalized to a
	// slice: the frontmatter spells it either as a YAML list of tool names or
	// as a comma/whitespace-separated string, and both land here. Excluded
	// from the strict frontmatter decode — Parse decodes the key loosely and
	// normalizes — because the list spelling cannot unmarshal into a scalar.
	AllowedTools []string `yaml:"-"`
	// SlashCommand controls whether this skill is also surfaced as its own
	// auto-derived slash command. Nil (the frontmatter key absent) means yes,
	// which is what every ordinary skill wants. Meta-skills that are reached
	// through a builtin command instead set it to false so the command palette
	// does not offer two entry points for one capability.
	SlashCommand *bool  `yaml:"slash-command,omitempty"`
	Body         string `yaml:"-"`
}

// Parse parses a skill definition from an io.Reader.
//
// The input must start with YAML frontmatter delimited by "---" and followed
// by a body.
func Parse(r io.Reader) (*Skill, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	content := string(data)
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, yamlFrontmatterDelim) {
		return nil, ErrMissingYAMLFrontmatter
	}
	parts := strings.SplitN(content, yamlFrontmatterDelim, 3)
	if len(parts) < 3 {
		return nil, ErrInvalidSkillFormat
	}
	yamlPart := parts[1]
	body := strings.TrimSpace(parts[2])

	// Skill.AllowedTools is excluded from the strict decode, so the frontmatter
	// parses whatever the allowed-tools spelling is; the key itself is decoded
	// loosely in a second pass — the list spelling Claude Code skills ship
	// cannot unmarshal into a scalar, and a strict miss failed the whole
	// frontmatter, dropping the skill from every surface that reads SKILL.md.
	var skill Skill
	if err := yaml.Unmarshal([]byte(yamlPart), &skill); err != nil {
		return nil, err
	}
	var allowedTools struct {
		Tools any `yaml:"allowed-tools"`
	}
	if err := yaml.Unmarshal([]byte(yamlPart), &allowedTools); err != nil {
		return nil, err
	}
	skill.AllowedTools = normalizeAllowedTools(allowedTools.Tools)
	skill.Body = body
	return &skill, nil
}

// normalizeAllowedTools folds the two spellings of allowed-tools into the
// slice the rest of forebrain reads: the string form splits on commas and
// whitespace, the list form keeps its items in order, and an absent key stays
// nil.
func normalizeAllowedTools(value any) []string {
	switch tools := value.(type) {
	case nil:
		return nil
	case string:
		return splitAllowedTools(tools)
	case []any:
		names := make([]string, 0, len(tools))
		for _, item := range tools {
			if name := strings.TrimSpace(fmt.Sprint(item)); name != "" {
				names = append(names, name)
			}
		}
		return names
	default:
		return nil
	}
}

func splitAllowedTools(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
}
