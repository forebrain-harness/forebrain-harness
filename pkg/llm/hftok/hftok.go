package hftok

import (
	"strings"

	"github.com/daulet/tokenizers"
)

// Tokenizer counts tokens with a HuggingFace tokenizer or a tiktoken model
// through the tokenizers library. Count is safe for concurrent use; Close is not
// and must not race with Count.
type Tokenizer struct{ tk *tokenizers.Tokenizer }

// FromFile loads a tokenizer from a tokenizer.json path.
func FromFile(path string) (*Tokenizer, error) {
	tk, err := tokenizers.FromFile(path)
	if err != nil {
		return nil, err
	}
	return &Tokenizer{tk: tk}, nil
}

// FromBytes loads a tokenizer from tokenizer.json content already in memory.
func FromBytes(data []byte) (*Tokenizer, error) {
	tk, err := tokenizers.FromBytes(data)
	if err != nil {
		return nil, err
	}
	return &Tokenizer{tk: tk}, nil
}

// FromTiktoken loads a tokenizer from a tiktoken model file (modelPath), its
// config (configPath) and the split pattern.
func FromTiktoken(modelPath, configPath, pattern string) (*Tokenizer, error) {
	tk, err := tokenizers.FromTiktoken(modelPath, configPath, pattern)
	if err != nil {
		return nil, err
	}
	return &Tokenizer{tk: tk}, nil
}

// Count returns the number of tokens text encodes to with no special tokens
// added. An empty string, a nil receiver, or one whose library handle is gone
// counts zero.
//
// The library passes the string to Rust as a NUL-terminated C string, so an
// embedded "\x00" would truncate it. Segments split on "\x00" are counted
// separately, one extra token per separator (what encoding the NUL byte itself
// costs): Count(a+"\x00"+b) == Count(a)+Count(b)+1.
func (t *Tokenizer) Count(text string) int {
	if t == nil || t.tk == nil || text == "" {
		return 0
	}
	total := 0
	for i, seg := range strings.Split(text, "\x00") {
		if i > 0 {
			total++
		}
		if seg == "" {
			continue
		}
		total += len(t.tk.EncodeWithOptions(seg, false).IDs)
	}
	return total
}

// Close releases the Rust tokenizer. It must not run concurrently with Count.
func (t *Tokenizer) Close() error {
	if t == nil || t.tk == nil {
		return nil
	}
	return t.tk.Close()
}
