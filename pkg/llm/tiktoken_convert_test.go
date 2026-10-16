package llm

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm/hftok"
)

// byteLevelBPETokenizerJSON is a hand-written byte-level BPE tokenizer.json:
// the 256 single-byte tokens (id = byte value), four merges (ids 256–259) and
// two added tokens (ids 260 and 261), pre-tokenized as Split(Regex) then
// ByteLevel(use_regex=false).
const byteLevelBPETokenizerJSON = "testdata/bytelevel_bpe_tokenizer.json"

// byteLevelBPEPattern is the fixture's Split regex.
const byteLevelBPEPattern = `(?i:'s|'t|'re|'ve|'m|'ll|'d)|[^\r\n\p{L}\p{N}]?\p{L}+|\p{N}{1,3}| ?[^\s\p{L}\p{N}]+[\r\n]*|\s*[\r\n]+|\s+(?!\S)|\s+`

func readByteLevelFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(byteLevelBPETokenizerJSON)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// convertFixture converts data and fails the test on an error.
func convertFixture(t *testing.T, data []byte) (model, config []byte, pattern string) {
	t.Helper()
	model, config, pattern, err := ConvertTokenizerJSONToTiktoken(data)
	if err != nil {
		t.Fatalf("ConvertTokenizerJSONToTiktoken: %v", err)
	}
	return model, config, pattern
}

func TestConvertTokenizerJSONToTiktokenSmall(t *testing.T) {
	data := readByteLevelFixture(t)
	model, config, pattern := convertFixture(t, data)

	if !strings.HasSuffix(string(model), "\n") {
		t.Fatal("model does not end with a newline")
	}
	lines := strings.Split(strings.TrimSuffix(string(model), "\n"), "\n")
	if len(lines) != 260 {
		t.Fatalf("model has %d lines, want 260 (the vocab size)", len(lines))
	}
	for i, want := range map[int]string{
		0:   "AA== 0",   // byte 0x00, written as U+0100 in the vocab
		32:  "IA== 32",  // the space byte, "Ġ" in the vocab
		65:  "QQ== 65",  // "A"
		256: "IHQ= 256", // "Ġt" → " t"
		258: "IHRoZQ== 258",
		259: "aW4= 259", // the last id, "in"
	} {
		if lines[i] != want {
			t.Errorf("model line %d = %q, want %q", i, lines[i], want)
		}
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(config, &fields); err != nil {
		t.Fatalf("config is not JSON: %v", err)
	}
	for _, key := range []string{"added_tokens_decoder", "bos_token", "eos_token", "model_max_length", "tokenizer_class"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("config lacks %q", key)
		}
	}
	var cfg tiktokenConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.BOSToken != "<|endoftext|>" || cfg.EOSToken != "<|endoftext|>" {
		t.Errorf("bos/eos = %q/%q, want the lowest-id added token <|endoftext|>", cfg.BOSToken, cfg.EOSToken)
	}
	if cfg.ModelMaxLength != 1000000 || cfg.TokenizerClass != "TiktokenTokenizer" {
		t.Errorf("model_max_length/tokenizer_class = %d/%q", cfg.ModelMaxLength, cfg.TokenizerClass)
	}
	want := map[string]tiktokenAddedToken{
		"260": {Content: "<|endoftext|>", Special: true},
		"261": {Content: "<|pad|>", Special: true},
	}
	if len(cfg.AddedTokensDecoder) != len(want) {
		t.Errorf("added_tokens_decoder = %v, want %v", cfg.AddedTokensDecoder, want)
	}
	for id, token := range want {
		if cfg.AddedTokensDecoder[id] != token {
			t.Errorf("added_tokens_decoder[%s] = %+v, want %+v", id, cfg.AddedTokensDecoder[id], token)
		}
	}

	if pattern != byteLevelBPEPattern {
		t.Errorf("pattern = %q, want %q", pattern, byteLevelBPEPattern)
	}

	model2, config2, pattern2 := convertFixture(t, data)
	if !bytes.Equal(model, model2) || !bytes.Equal(config, config2) || pattern != pattern2 {
		t.Fatal("two conversions of the same tokenizer.json differ")
	}
}

func TestConvertedTiktokenCountsMatchTokenizerJSON(t *testing.T) {
	data := readByteLevelFixture(t)
	model, config, pattern := convertFixture(t, data)
	dir := t.TempDir()
	modelPath := filepath.Join(dir, "tiktoken.model")
	configPath := filepath.Join(dir, "tokenizer_config.json")
	if err := os.WriteFile(modelPath, model, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, config, 0o644); err != nil {
		t.Fatal(err)
	}

	hf, err := hftok.FromFile(byteLevelBPETokenizerJSON)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hf.Close() })
	tk, err := hftok.FromTiktoken(modelPath, configPath, pattern)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tk.Close() })

	for _, text := range []string{
		"the",
		"Then the thing in the inn",
		"It's the end; isn't it? 1234567",
		"the\n\n  in\tthe   \r\n",
		"你好，世界：the 分词器 in 中文",
		"émigré naïve café — the end",
	} {
		want, got := hf.Count(text), tk.Count(text)
		if want <= 0 || got != want {
			t.Errorf("Count(%q): tokenizer.json %d, tiktoken %d", text, want, got)
		}
	}
}

// mutateFixture decodes the fixture, applies edit and re-encodes it.
func mutateFixture(t *testing.T, edit func(doc map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(readByteLevelFixture(t), &doc); err != nil {
		t.Fatal(err)
	}
	edit(doc)
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func fixturePreTokenizers(doc map[string]any) []any {
	return doc["pre_tokenizer"].(map[string]any)["pretokenizers"].([]any)
}

func TestConvertRejectsUnsupportedShapes(t *testing.T) {
	cases := map[string]func(doc map[string]any){
		"byte_fallback": func(doc map[string]any) {
			doc["model"].(map[string]any)["byte_fallback"] = true
		},
		"not BPE": func(doc map[string]any) {
			doc["model"].(map[string]any)["type"] = "WordPiece"
		},
		"two Splits": func(doc map[string]any) {
			pts := fixturePreTokenizers(doc)
			doc["pre_tokenizer"].(map[string]any)["pretokenizers"] = []any{pts[0], pts[0], pts[1]}
		},
		"no pre_tokenizer": func(doc map[string]any) {
			doc["pre_tokenizer"] = nil
		},
		"use_regex true": func(doc map[string]any) {
			fixturePreTokenizers(doc)[1].(map[string]any)["use_regex"] = true
		},
		"use_regex absent": func(doc map[string]any) {
			delete(fixturePreTokenizers(doc)[1].(map[string]any), "use_regex")
		},
		"ByteLevel adds a prefix space": func(doc map[string]any) {
			fixturePreTokenizers(doc)[1].(map[string]any)["add_prefix_space"] = true
		},
		"Split keeps the gaps": func(doc map[string]any) {
			fixturePreTokenizers(doc)[0].(map[string]any)["behavior"] = "Removed"
		},
		"Split string pattern": func(doc map[string]any) {
			fixturePreTokenizers(doc)[0].(map[string]any)["pattern"] = map[string]any{"String": " "}
		},
		"ids not contiguous": func(doc map[string]any) {
			doc["model"].(map[string]any)["vocab"].(map[string]any)["in"] = 300
		},
		"outside the byte-level alphabet": func(doc map[string]any) {
			doc["model"].(map[string]any)["vocab"].(map[string]any)["€"] = 260
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := ConvertTokenizerJSONToTiktoken(mutateFixture(t, edit))
			if err == nil {
				t.Fatal("conversion succeeded, want an error")
			}
			t.Logf("rejected: %v", err)
		})
	}

	t.Run("deepseek-v3 three Splits", func(t *testing.T) {
		data, err := os.ReadFile(deepseekV3TokenizerJSON)
		if err != nil {
			t.Fatal(err)
		}
		_, _, _, err = ConvertTokenizerJSONToTiktoken(data)
		if err == nil {
			t.Fatal("conversion of deepseek_v3_tokenizer.json succeeded, want an error")
		}
		t.Logf("rejected: %v", err)
	})

	// Removed with invert keeps just the matches, as Isolated does.
	t.Run("Removed with invert is accepted", func(t *testing.T) {
		data := mutateFixture(t, func(doc map[string]any) {
			split := fixturePreTokenizers(doc)[0].(map[string]any)
			split["behavior"] = "Removed"
			split["invert"] = true
		})
		if _, _, _, err := ConvertTokenizerJSONToTiktoken(data); err != nil {
			t.Fatalf("conversion failed: %v", err)
		}
	})
}
