// tokenizer2tiktoken converts a HuggingFace byte-level BPE tokenizer.json into
// the tiktoken files a built-in tokenizer ships as: tiktoken.model and
// tokenizer_config.json (llm.ConvertTokenizerJSONToTiktoken). The bundled
// gpt-o200k, glm-5, grok-2, qwen-3.8, minimax-m2 and mimo-v2.5 tokenizers are
// stored this way; when one of them is updated, regenerate its files from the
// source tokenizer.json with this tool (see assets/tokenizers/README.md).
//
// It prints the split pattern (the Pattern of the builtinTokenizers entry) and
// each written file's sha256 and size (its Files entries).
//
// Usage, from the repository root:
//
//	go run ./cmd/tokenizer2tiktoken <tokenizer.json> <out-dir>
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: tokenizer2tiktoken <tokenizer.json> <out-dir>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "tokenizer2tiktoken:", err)
		os.Exit(1)
	}
}

func run(src, outDir string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	model, config, pattern, err := llm.ConvertTokenizerJSONToTiktoken(data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	fmt.Printf("pattern: %s\n", pattern)
	for _, file := range []struct {
		name string
		data []byte
	}{
		{"tiktoken.model", model},
		{"tokenizer_config.json", config},
	} {
		if err := os.WriteFile(filepath.Join(outDir, file.name), file.data, 0o644); err != nil {
			return err
		}
		sum := sha256.Sum256(file.data)
		fmt.Printf("%s: sha256 %s size %d\n", file.name, hex.EncodeToString(sum[:]), len(file.data))
	}
	return nil
}
