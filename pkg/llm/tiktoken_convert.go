package llm

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
)

// Converting a HuggingFace byte-level BPE tokenizer.json into the tiktoken form.
//
// A tokenizer.json loaded through the tokenizers library holds the vocabulary,
// the merges and every pre-tokenizer object in memory, several times the size
// of the same vocabulary held as tiktoken's byte-sequence → rank table. For a
// byte-level BPE whose ids are contiguous and whose pre-tokenizer is one split
// regex followed by ByteLevel(use_regex=false), the rank table carries the same
// vocabulary: tiktoken merges the pair whose merged token has the lowest rank
// (its id), HuggingFace the pair listed first in merges, and over the
// repository's own files the two orders agree for every built-in vocabulary
// (see assets/tokenizers/README.md). What differs is the added tokens: a
// tokenizer.json matches an added token's text (<|endoftext|>, <tool_call>, …)
// inside the counted text as one token, while the tiktoken form, counting with
// special tokens off, encodes that text as ordinary bytes. The built-in ids
// that have this shape ship in the tiktoken form (see builtinTokenizers);
// cmd/tokenizer2tiktoken produces their files and the download fallback
// converts the hub's tokenizer.json with the same function.

// tiktokenModelMaxLength is the model_max_length written into a converted
// tokenizer_config.json. Counting never truncates; the field is required.
const tiktokenModelMaxLength = 1000000

// hfTokenizerJSON is the part of a tokenizer.json the conversion reads.
type hfTokenizerJSON struct {
	AddedTokens []struct {
		ID      int    `json:"id"`
		Content string `json:"content"`
	} `json:"added_tokens"`
	PreTokenizer *hfPreTokenizer `json:"pre_tokenizer"`
	Model        struct {
		Type         string         `json:"type"`
		Vocab        map[string]int `json:"vocab"`
		ByteFallback bool           `json:"byte_fallback"`
	} `json:"model"`
}

// hfPreTokenizer is one pre-tokenizer object; only the fields of Sequence,
// Split and ByteLevel the conversion checks are decoded.
type hfPreTokenizer struct {
	Type          string           `json:"type"`
	Pretokenizers []hfPreTokenizer `json:"pretokenizers"`
	Pattern       *struct {
		Regex  *string `json:"Regex"`
		String *string `json:"String"`
	} `json:"pattern"`
	Behavior       string `json:"behavior"`
	Invert         bool   `json:"invert"`
	AddPrefixSpace bool   `json:"add_prefix_space"`
	UseRegex       *bool  `json:"use_regex"`
}

// tiktokenConfig is the tokenizer_config.json hftok.FromTiktoken requires; the
// field order is fixed so the output is byte-for-byte reproducible.
type tiktokenConfig struct {
	AddedTokensDecoder map[string]tiktokenAddedToken `json:"added_tokens_decoder"`
	BOSToken           string                        `json:"bos_token"`
	EOSToken           string                        `json:"eos_token"`
	ModelMaxLength     int                           `json:"model_max_length"`
	TokenizerClass     string                        `json:"tokenizer_class"`
}

type tiktokenAddedToken struct {
	Content    string `json:"content"`
	LStrip     bool   `json:"lstrip"`
	Normalized bool   `json:"normalized"`
	RStrip     bool   `json:"rstrip"`
	SingleWord bool   `json:"single_word"`
	Special    bool   `json:"special"`
}

// ConvertTokenizerJSONToTiktoken converts a HuggingFace byte-level BPE tokenizer.json
// into the tiktoken rank file and tokenizer_config.json hftok.FromTiktoken loads,
// plus the split pattern. It accepts only the shape that converts faithfully:
// a BPE model with contiguous ids, and a pre-tokenizer that is one Split regex
// followed by ByteLevel(use_regex=false).
func ConvertTokenizerJSONToTiktoken(data []byte) (model, config []byte, pattern string, err error) {
	var tj hfTokenizerJSON
	if err := json.Unmarshal(data, &tj); err != nil {
		return nil, nil, "", fmt.Errorf("llm: parse tokenizer.json: %w", err)
	}
	if tj.Model.Type != "BPE" {
		return nil, nil, "", fmt.Errorf("llm: tokenizer model type is %q, want BPE", tj.Model.Type)
	}
	if tj.Model.ByteFallback {
		return nil, nil, "", fmt.Errorf("llm: tokenizer model uses byte_fallback, which the tiktoken form cannot express")
	}
	pattern, err = byteLevelSplitPattern(tj.PreTokenizer)
	if err != nil {
		return nil, nil, "", err
	}

	tokens := make([]string, len(tj.Model.Vocab))
	seen := make([]bool, len(tj.Model.Vocab))
	for token, id := range tj.Model.Vocab {
		if id < 0 || id >= len(tokens) || seen[id] {
			return nil, nil, "", fmt.Errorf("llm: tokenizer vocab ids are not contiguous from 0 (id %d, %d entries)", id, len(tokens))
		}
		tokens[id] = token
		seen[id] = true
	}

	decoder := byteLevelDecoder()
	var buf bytes.Buffer
	raw := make([]byte, 0, 64)
	for id, token := range tokens {
		raw = raw[:0]
		for _, r := range token {
			b, ok := decoder[r]
			if !ok {
				return nil, nil, "", fmt.Errorf("llm: tokenizer vocab id %d holds %q, outside the byte-level alphabet", id, r)
			}
			raw = append(raw, b)
		}
		buf.WriteString(base64.StdEncoding.EncodeToString(raw))
		buf.WriteByte(' ')
		buf.WriteString(strconv.Itoa(id))
		buf.WriteByte('\n')
	}

	cfg := tiktokenConfig{
		AddedTokensDecoder: make(map[string]tiktokenAddedToken, len(tj.AddedTokens)),
		ModelMaxLength:     tiktokenModelMaxLength,
		TokenizerClass:     "TiktokenTokenizer",
	}
	lowest := -1
	for _, added := range tj.AddedTokens {
		cfg.AddedTokensDecoder[strconv.Itoa(added.ID)] = tiktokenAddedToken{Content: added.Content, Special: true}
		if lowest < 0 || added.ID < lowest {
			lowest = added.ID
			cfg.BOSToken = added.Content
			cfg.EOSToken = added.Content
		}
	}
	config, err = json.Marshal(cfg)
	if err != nil {
		return nil, nil, "", fmt.Errorf("llm: encode tokenizer_config.json: %w", err)
	}
	return buf.Bytes(), config, pattern, nil
}

// byteLevelSplitPattern returns the split regex of a pre-tokenizer that is
// exactly Sequence[Split(Regex), ByteLevel(use_regex=false)], where the Split
// keeps only the regex matches (Isolated, or Removed with invert) — the way
// tiktoken splits.
func byteLevelSplitPattern(pt *hfPreTokenizer) (string, error) {
	if pt == nil || pt.Type != "Sequence" || len(pt.Pretokenizers) != 2 {
		return "", fmt.Errorf("llm: tokenizer pre_tokenizer is not a Sequence of one Split and one ByteLevel")
	}
	split, byteLevel := pt.Pretokenizers[0], pt.Pretokenizers[1]
	if split.Type != "Split" || byteLevel.Type != "ByteLevel" {
		return "", fmt.Errorf("llm: tokenizer pre_tokenizer is [%s, %s], want [Split, ByteLevel]", split.Type, byteLevel.Type)
	}
	if split.Pattern == nil || split.Pattern.Regex == nil {
		return "", fmt.Errorf("llm: tokenizer Split pattern is not a regex")
	}
	keepsMatches := (split.Behavior == "Isolated" && !split.Invert) || (split.Behavior == "Removed" && split.Invert)
	if !keepsMatches {
		return "", fmt.Errorf("llm: tokenizer Split behavior %s/invert=%t does not keep just the regex matches", split.Behavior, split.Invert)
	}
	if byteLevel.UseRegex == nil || *byteLevel.UseRegex {
		return "", fmt.Errorf("llm: tokenizer ByteLevel pre-tokenizer splits with its own regex (use_regex is not false)")
	}
	if byteLevel.AddPrefixSpace {
		return "", fmt.Errorf("llm: tokenizer ByteLevel pre-tokenizer adds a prefix space, which the tiktoken form cannot express")
	}
	return *split.Pattern.Regex, nil
}

// byteLevelDecoder maps each character of GPT-2's byte-level alphabet back to
// its byte: the printable bytes '!'..'~', '¡'..'¬' and '®'..'ÿ' stand for
// themselves, and the other 68 bytes, in ascending order, take the code points
// from U+0100 on.
func byteLevelDecoder() map[rune]byte {
	decoder := make(map[rune]byte, 256)
	next := rune(0x100)
	for b := 0; b < 256; b++ {
		if (b >= '!' && b <= '~') || (b >= 0xA1 && b <= 0xAC) || (b >= 0xAE && b <= 0xFF) {
			decoder[rune(b)] = byte(b)
			continue
		}
		decoder[next] = byte(b)
		next++
	}
	return decoder
}
