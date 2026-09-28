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

package skill_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
)

func TestParse_ValidSkill(t *testing.T) {
	skillMD := `---
name: pdf-processing
description: Extract text and tables from PDF files, fill forms, merge documents.
license: Apache-2.0
compatibility: Linux, macOS
metadata:
  author: example-org
  version: "1.0"
allowed-tools: pdftk pdftotext
---
BODY`

	s, err := skill.Parse(strings.NewReader(skillMD))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if s.Name != "pdf-processing" {
		t.Fatalf("Name: expected %q, got %q", "pdf-processing", s.Name)
	}
	if s.Description == "" {
		t.Fatalf("Description: expected non-empty")
	}
	if s.License != "Apache-2.0" {
		t.Fatalf("License: expected %q, got %q", "Apache-2.0", s.License)
	}
	if s.Compatibility != "Linux, macOS" {
		t.Fatalf("Compatibility: expected %q, got %q", "Linux, macOS", s.Compatibility)
	}
	if s.AllowedTools == nil || len(s.AllowedTools) != 2 || s.AllowedTools[0] != "pdftk" || s.AllowedTools[1] != "pdftotext" {
		t.Fatalf("AllowedTools: expected [pdftk pdftotext], got %#v", s.AllowedTools)
	}
	if s.Metadata == nil || s.Metadata["author"] != "example-org" {
		t.Fatalf("Metadata.author: expected %q, got %#v", "example-org", s.Metadata)
	}
	if strings.TrimSpace(s.Body) != "BODY" {
		t.Fatalf("Body: expected %q, got %q", "BODY", s.Body)
	}
}

func TestParse_MissingFrontmatter(t *testing.T) {
	_, err := skill.Parse(strings.NewReader("name: x\ndescription: y\n"))
	if !errors.Is(err, skill.ErrMissingYAMLFrontmatter) {
		t.Fatalf("expected ErrMissingYAMLFrontmatter, got: %v", err)
	}
}

func TestParse_InvalidFormat(t *testing.T) {
	_, err := skill.Parse(strings.NewReader("---\nname: x\n"))
	if !errors.Is(err, skill.ErrInvalidSkillFormat) {
		t.Fatalf("expected ErrInvalidSkillFormat, got: %v", err)
	}
}

// The ecosystem spells allowed-tools two ways, and a skill must parse under
// both: forebrain's own comma-separated string and the YAML list of tool names
// that Claude Code skills ship. The list spelling used to fail the whole
// frontmatter, which dropped the skill from every surface that reads a
// SKILL.md.
func TestParse_AllowedToolsListSpelling(t *testing.T) {
	skillMD := `---
name: humanizer-zh
description: 去除文本中的 AI 生成痕迹。
allowed-tools:
  - Read
  - Write
  - Edit
metadata:
  trigger: 编辑或审阅文本
---
BODY`

	s, err := skill.Parse(strings.NewReader(skillMD))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if s.Name != "humanizer-zh" {
		t.Fatalf("Name: expected %q, got %q", "humanizer-zh", s.Name)
	}
	if s.Description == "" {
		t.Fatalf("Description: expected non-empty")
	}
	if s.AllowedTools == nil || len(s.AllowedTools) != 3 || s.AllowedTools[0] != "Read" || s.AllowedTools[1] != "Write" || s.AllowedTools[2] != "Edit" {
		t.Fatalf("AllowedTools: expected [Read Write Edit], got %#v", s.AllowedTools)
	}
	if s.Metadata["trigger"] != "编辑或审阅文本" {
		t.Fatalf("Metadata.trigger: expected %q, got %#v", "编辑或审阅文本", s.Metadata)
	}
	if strings.TrimSpace(s.Body) != "BODY" {
		t.Fatalf("Body: expected %q, got %q", "BODY", s.Body)
	}
}

func TestParse_AllowedToolsAbsentStaysEmpty(t *testing.T) {
	s, err := skill.Parse(strings.NewReader("---\nname: x\ndescription: y\n---\nBODY"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(s.AllowedTools) != 0 {
		t.Fatalf("AllowedTools: expected empty, got %#v", s.AllowedTools)
	}
}
