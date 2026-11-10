package llm

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInstallAssetsCopiesEntries(t *testing.T) {
	src := t.TempDir()
	content := []byte(`{"a/b":{"id":"a/b","limit":{"context":1}}}`)
	if err := os.WriteFile(filepath.Join(src, "models.json"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	// assetEntries also copies the tokenizers/ directory.
	gz := []byte("not really gzip, just bytes to compare")
	tokenizerDir := filepath.Join(src, "tokenizers", "test-tok")
	if err := os.MkdirAll(tokenizerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tokenizerDir, "tokenizer.json.gz"), gz, 0o644); err != nil {
		t.Fatal(err)
	}
	// ... and the fonts/ directory PDFium's CJK fallback needs.
	font := []byte("stand-in otf bytes")
	fontsDir := filepath.Join(src, "fonts")
	if err := os.MkdirAll(fontsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fontsDir, "NotoSansSC-Regular.otf"), font, 0o644); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	if err := InstallAssets(src, bin); err != nil {
		t.Fatalf("InstallAssets: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(bin, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("installed models.json differs from the source")
	}
	gotGz, err := os.ReadFile(filepath.Join(bin, "tokenizers", "test-tok", "tokenizer.json.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotGz, gz) {
		t.Fatal("installed tokenizers/ differs from the source")
	}
	gotFont, err := os.ReadFile(filepath.Join(bin, "fonts", "NotoSansSC-Regular.otf"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotFont, font) {
		t.Fatal("installed fonts/ differs from the source")
	}

	if err := InstallAssets(t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("expected an error when the source models.json is missing")
	}
}

// TestModelAssetCoversUpstream is the self-consistency check on the shipped
// catalog: every model the two login surfaces serve has a row, the reasoning
// levels are a closed set, and the sample rows this build's behaviour depends
// on are the ones upstream publishes. It is the test that fails when a refresh
// drops or reshapes data.
func TestModelAssetCoversUpstream(t *testing.T) {
	data, err := RepoModelsJSONForTest()
	if err != nil {
		t.Fatalf("read repo models.json: %v", err)
	}
	var raw map[string]struct {
		Type             string `json:"type"`
		Reasoning        bool   `json:"reasoning"`
		Attachment       bool   `json:"attachment"`
		ReasoningOptions *[]struct {
			Type   string   `json:"type"`
			Values []string `json:"values"`
		} `json:"reasoning_options"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse models.json: %v", err)
	}

	nonChat := 0
	withOptions := 0
	for id, entry := range raw {
		if strings.TrimSpace(entry.Type) != "" {
			nonChat++
		}
		if entry.ReasoningOptions == nil {
			continue
		}
		if len(*entry.ReasoningOptions) > 0 {
			withOptions++
		}
		for _, option := range *entry.ReasoningOptions {
			if option.Type != "effort" {
				continue
			}
			for _, value := range option.Values {
				if !reasoningEffortLevels[value] {
					t.Errorf("%s names an unknown effort %q", id, value)
				}
			}
		}
	}

	// The floors, not the exact counts: upstream adds models every day, so an
	// exact number would turn a legitimate refresh into a red build. These are
	// what a truncated or merged-wrong file would fall under.
	if len(raw) < 700 {
		t.Errorf("catalog has %d entries, want at least 700", len(raw))
	}
	if nonChat < 60 {
		t.Errorf("catalog has %d non-chat entries, want at least 60", nonChat)
	}
	if withOptions < 280 {
		t.Errorf("catalog has %d entries with reasoning options, want at least 280", withOptions)
	}

	// Every model either OpenCode surface serves must be in the file: the
	// static table no longer carries levels or windows, so a missing row is a
	// model with nothing to show.
	for provider, ids := range map[string][]string{
		"opencode":    {"glm-5.3", "glm-5.3-flash-free", "nemotron-3.5-super-vl-free", "qwen3.8-max", "grok-build-0.1"},
		"opencode-go": {"glm-5.3", "glm-5.3-flash", "claude-haiku-5-5", "gemini-4-argon-contributor", "kimi-k3"},
	} {
		for _, id := range ids {
			if _, ok := raw[provider+"/"+id]; !ok {
				t.Errorf("catalog is missing %s/%s", provider, id)
			}
		}
	}

	samples := []struct {
		id      string
		efforts []string
		kind    string
	}{
		// The levels the pickers offer, straight from upstream.
		{id: "openai/gpt-5", efforts: []string{"minimal", "low", "medium", "high"}},
		{id: "openai/gpt-5.5", efforts: []string{"none", "low", "medium", "high", "xhigh"}},
		{id: "xai/grok-4.3", efforts: []string{"none", "low", "medium", "high", "xhigh"}},
		{id: "zhipuai/glm-5.3-flash", efforts: []string{"low", "high", "max"}},
		{id: "anthropic/claude-sonnet-5", efforts: []string{"low", "medium", "high", "xhigh", "max"}},
		{id: "alibaba/qwen3.8-max", efforts: []string{"low", "medium", "xhigh"}},
		{id: "opencode-go/glm-5.3", efforts: []string{"low", "high", "max"}},
		// A toggle, a token budget, or a model that does not reason at all:
		// the row exists, the levels are empty.
		{id: "zhipuai/glm-4.5"},
		{id: "anthropic/claude-haiku-4-5"},
		{id: "deepseek/deepseek-chat"},
		// A non-chat row keeps its type.
		{id: "openai/whisper-large-v3", kind: "transcription"},
		{id: "openai/gpt-image-1", kind: "image"},
		{id: "opencode/jev-1.13", kind: "decision"},
	}
	cat, err := Parse(data)
	if err != nil {
		t.Fatalf("parse catalog: %v", err)
	}
	for _, sample := range samples {
		m, ok := cat.LookupModel(sample.id)
		if !ok {
			t.Errorf("catalog is missing %s", sample.id)
			continue
		}
		if m.Kind != sample.kind {
			t.Errorf("%s kind = %q, want %q", sample.id, m.Kind, sample.kind)
		}
		if levels, known := m.ReasoningEfforts(); len(sample.efforts) == 0 {
			if len(levels) != 0 {
				t.Errorf("%s levels = %v (known=%v), want none", sample.id, levels, known)
			}
		} else if !known || !reflect.DeepEqual(levels, sample.efforts) {
			t.Errorf("%s levels = %v (known=%v), want %v", sample.id, levels, known, sample.efforts)
		}
	}

	// The one upstream row this build deliberately disagrees with.
	flash, ok := raw["deepseek/deepseek-v4-flash"]
	if !ok {
		t.Fatal("catalog is missing deepseek/deepseek-v4-flash")
	}
	if !flash.Attachment {
		t.Fatal("the OVERRIDES table must keep deepseek/deepseek-v4-flash multimodal")
	}
}
