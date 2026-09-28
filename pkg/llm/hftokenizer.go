package llm

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	regexp2 "github.com/dlclark/regexp2"
)

// hfTokenizerJSON is the on-disk format of a HuggingFace tokenizer.json file.
// Only the fields we need are decoded.
type hfTokenizerJSON struct {
	Version     string `json:"version"`
	AddedTokens []struct {
		ID      int    `json:"id"`
		Content string `json:"content"`
		Special bool   `json:"special"`
	} `json:"added_tokens"`
	PreTokenizer  *hfPreTokenizer  `json:"pre_tokenizer"`
	PostProcessor *hfPostProcessor `json:"post_processor"`
	Model         struct {
		Type                  string         `json:"type"`
		Vocab                 map[string]int `json:"vocab"`
		Merges                []string       `json:"merges"`
		ByteFallback          *bool          `json:"byte_fallback,omitempty"`
		FuseUnk               *bool          `json:"fuse_unk,omitempty"`
		ContinuingSubwordPref *string        `json:"continuing_subword_prefix,omitempty"`
		EndOfWordSuffix       *string        `json:"end_of_word_suffix,omitempty"`
		UnkToken              *string        `json:"unk_token,omitempty"`
	} `json:"model"`
}

type hfPreTokenizer struct {
	Type          string           `json:"type"`
	Pretokenizers []hfPreTokenizer `json:"pretokenizers,omitempty"`
	// Split fields
	Pattern  *hfPattern `json:"pattern,omitempty"`
	Behavior string     `json:"behavior,omitempty"`
	Invert   *bool      `json:"invert,omitempty"`
	// ByteLevel fields
	AddPrefixSpace *bool `json:"add_prefix_space,omitempty"`
	TrimOffsets    *bool `json:"trim_offsets,omitempty"`
	UseRegex       *bool `json:"use_regex,omitempty"`
}

type hfPattern struct {
	Regex  *string `json:"Regex,omitempty"`
	String *string `json:"String,omitempty"`
}

type hfPostProcessor struct {
	Type           string `json:"type"`
	AddPrefixSpace *bool  `json:"add_prefix_space,omitempty"`
	TrimOffsets    *bool  `json:"trim_offsets,omitempty"`
	UseRegex       *bool  `json:"use_regex,omitempty"`
}

// HFTokenizer is a native Go implementation of a HuggingFace BPE tokenizer
// that loads from tokenizer.json. It supports the pre-tokenizer pipeline
// (Split + ByteLevel) and BPE merging. Only token counting is supported.
type HFTokenizer struct {
	vocab       map[string]int
	merges      map[string]int // "left right" -> rank
	addedTokens map[string]int // content -> id

	// Pre-tokenizer compiled regexes (nil if not a Sequence)
	splitRegexes                []*regexp2.Regexp
	byteLevel                   bool
	byteLevelAddPrefixSpace     bool
	postProcessorAddPrefixSpace bool

	// Byte-level encoding
	byteToRune [256]rune
	runeToByte map[rune]byte

	label string
}

// LoadHFTokenizer reads and parses a HuggingFace tokenizer.json file.
func LoadHFTokenizer(path string) (*HFTokenizer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read tokenizer file: %w", err)
	}
	return ParseHFTokenizer(data)
}

// ParseHFTokenizer creates an HFTokenizer from tokenizer.json bytes.
func ParseHFTokenizer(data []byte) (*HFTokenizer, error) {
	var raw hfTokenizerJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse tokenizer json: %w", err)
	}

	t := &HFTokenizer{
		vocab:       raw.Model.Vocab,
		merges:      make(map[string]int, len(raw.Model.Merges)),
		addedTokens: make(map[string]int, len(raw.AddedTokens)),
		runeToByte:  make(map[rune]byte, 256),
	}

	// Build merge rank map
	for i, m := range raw.Model.Merges {
		t.merges[m] = i
	}

	// Build added tokens map
	for _, at := range raw.AddedTokens {
		t.addedTokens[at.Content] = at.ID
	}

	// Initialize byte-level encoding
	t.initByteLevelEncoding()

	// Parse pre-tokenizer
	if raw.PreTokenizer != nil {
		if err := t.initPretokenizer(raw.PreTokenizer); err != nil {
			return nil, fmt.Errorf("init pre-tokenizer: %w", err)
		}
	}

	// Parse post-processor
	if raw.PostProcessor != nil && raw.PostProcessor.AddPrefixSpace != nil {
		t.postProcessorAddPrefixSpace = *raw.PostProcessor.AddPrefixSpace
	}

	t.label = "hf-bpe"
	return t, nil
}

func (t *HFTokenizer) initByteLevelEncoding() {
	// Standard GPT-2 byte-to-unicode mapping
	bs := make([]int, 0, 256)
	for i := 33; i <= 126; i++ {
		bs = append(bs, i)
	}
	for i := 161; i <= 172; i++ {
		bs = append(bs, i)
	}
	for i := 174; i <= 255; i++ {
		bs = append(bs, i)
	}

	inSet := make(map[int]bool, len(bs))
	for _, b := range bs {
		inSet[b] = true
	}

	cs := make([]int, len(bs))
	copy(cs, bs)
	n := 0
	for b := 0; b < 256; b++ {
		if !inSet[b] {
			bs = append(bs, b)
			cs = append(cs, 256+n)
			n++
		}
	}

	for i, b := range bs {
		t.byteToRune[b] = rune(cs[i])
		t.runeToByte[rune(cs[i])] = byte(b)
	}
}

func (t *HFTokenizer) initPretokenizer(pt *hfPreTokenizer) error {
	switch pt.Type {
	case "Sequence":
		for i := range pt.Pretokenizers {
			child := pt.Pretokenizers[i]
			if err := t.initPretokenizer(&child); err != nil {
				return err
			}
		}
	case "Split":
		if pt.Pattern == nil || pt.Pattern.Regex == nil {
			return fmt.Errorf("split pre-tokenizer missing regex pattern")
		}
		re, err := regexp2.Compile(*pt.Pattern.Regex, regexp2.RE2)
		if err != nil {
			return fmt.Errorf("compile regex %q: %w", *pt.Pattern.Regex, err)
		}
		t.splitRegexes = append(t.splitRegexes, re)
	case "ByteLevel":
		t.byteLevel = true
		if pt.AddPrefixSpace != nil {
			t.byteLevelAddPrefixSpace = *pt.AddPrefixSpace
		}
	case "":
		// no pre-tokenizer
	default:
		// Unknown pre-tokenizer types are ignored; BPE will still work on raw text
	}
	return nil
}

// Label returns a human-readable label for this tokenizer.
func (t *HFTokenizer) Label() string {
	return t.label
}

// Encode counts the number of tokens for the given text.
func (t *HFTokenizer) Encode(text string) int {
	if text == "" {
		return 0
	}

	// Check for added tokens (special tokens) in the text.
	// These are matched first and counted as single tokens.
	remaining := text
	count := 0
	for content := range t.addedTokens {
		if idx := strings.Index(remaining, content); idx >= 0 {
			before := remaining[:idx]
			count += t.encodeSegment(before)
			count++ // special token = 1 token
			remaining = remaining[idx+len(content):]
		}
	}
	count += t.encodeSegment(remaining)
	return count
}

// encodeSegment encodes a segment of text (no special tokens) using the
// pre-tokenizer pipeline and BPE.
func (t *HFTokenizer) encodeSegment(text string) int {
	if text == "" {
		return 0
	}

	// Note: The ByteLevel post-processor's add_prefix_space is not applied
	// here. In HuggingFace tokenizers, it only affects offset trimming,
	// not actual tokenization (verified empirically).

	// Apply pre-tokenizer pipeline
	preTokens := []string{text}
	for _, re := range t.splitRegexes {
		var next []string
		for _, pt := range preTokens {
			next = append(next, splitIsolated(re, pt)...)
		}
		preTokens = next
	}

	// Apply byte-level encoding if configured
	if t.byteLevel {
		for i, pt := range preTokens {
			preTokens[i] = t.byteLevelEncode(pt)
		}
	}

	// Apply BPE to each pre-token and count
	total := 0
	for _, pt := range preTokens {
		if pt == "" {
			continue
		}
		tokens := t.bpe(pt)
		total += len(tokens)
	}
	return total
}

// splitIsolated implements the HuggingFace Split pre-tokenizer with Isolated behavior.
// It finds all regex matches and returns both matched and non-matched portions.
//
// Note: regexp2's Match.Index/Length are UTF-16 code unit indices (to match .NET
// behavior), NOT byte indices. We avoid this mismatch by using m.String() to get
// the matched text and strings.Index to locate it in the original string.
func splitIsolated(re *regexp2.Regexp, text string) []string {
	if text == "" {
		return nil
	}

	var result []string
	pos := 0

	m, err := re.FindStringMatch(text)
	for m != nil && err == nil {
		matched := m.String()
		if matched == "" {
			m, err = re.FindNextMatch(m)
			continue
		}
		// Find the byte position of this match starting from pos
		idx := strings.Index(text[pos:], matched)
		if idx < 0 {
			break
		}
		absIdx := pos + idx

		// Add text before the match (non-matched portion)
		if absIdx > pos {
			result = append(result, text[pos:absIdx])
		}
		// Add the match itself
		result = append(result, matched)
		pos = absIdx + len(matched)

		m, err = re.FindNextMatch(m)
	}
	if err != nil {
		return []string{text}
	}

	// Add remaining text after last match
	if pos < len(text) {
		result = append(result, text[pos:])
	}

	return result
}

// byteLevelEncode converts a string to its byte-level unicode representation.
// Each byte is mapped to its corresponding unicode character.
func (t *HFTokenizer) byteLevelEncode(s string) string {
	runes := make([]rune, 0, len(s))
	for _, b := range []byte(s) {
		runes = append(runes, t.byteToRune[b])
	}
	return string(runes)
}

// bpe applies Byte-Pair Encoding to a pre-token string and returns the
// resulting token strings.
func (t *HFTokenizer) bpe(preToken string) []string {
	if preToken == "" {
		return nil
	}

	// Check if the entire pre-token is in the vocab
	if _, ok := t.vocab[preToken]; ok {
		return []string{preToken}
	}

	// Convert to runes (each byte-level char is a single rune)
	symbols := []rune(preToken)
	if len(symbols) <= 1 {
		return []string{preToken}
	}

	// Convert to string symbols for merging
	word := make([]string, len(symbols))
	for i, r := range symbols {
		word[i] = string(r)
	}

	// BPE merge loop
	for len(word) > 1 {
		bestRank := -1
		bestIdx := -1

		for i := 0; i < len(word)-1; i++ {
			pair := word[i] + " " + word[i+1]
			if rank, ok := t.merges[pair]; ok {
				if bestRank == -1 || rank < bestRank {
					bestRank = rank
					bestIdx = i
				}
			}
		}

		if bestIdx == -1 {
			break
		}

		// Merge the pair at bestIdx
		merged := word[bestIdx] + word[bestIdx+1]
		newWord := make([]string, 0, len(word)-1)
		newWord = append(newWord, word[:bestIdx]...)
		newWord = append(newWord, merged)
		newWord = append(newWord, word[bestIdx+2:]...)
		word = newWord
	}

	return word
}

// --- HF tokenizer state management (parallel to tiktoken state) ---

var hfState struct {
	mu      sync.Mutex
	tok     *HFTokenizer
	pending bool
	gen     uint64
	label   string
}

// ConfigureHFTokenizer loads a HuggingFace tokenizer from the given path
// in the background. Until loading completes, the heuristic fallback is used.
func ConfigureHFTokenizer(path string) error {
	hfState.mu.Lock()
	hfState.gen++
	gen := hfState.gen
	hfState.pending = true
	hfState.tok = nil
	hfState.label = ""
	hfState.mu.Unlock()

	go func() {
		tok, err := LoadHFTokenizer(path)
		hfState.mu.Lock()
		defer hfState.mu.Unlock()
		if hfState.gen != gen {
			return
		}
		hfState.pending = false
		if err != nil {
			slog.Error("tokestimate: HF tokenizer load failed; using fallback", "path", path, "err", err)
			return
		}
		hfState.tok = tok
		hfState.label = tok.Label()
		slog.Info("tokestimate: HF tokenizer ready", "label", hfState.label, "path", path)
	}()

	return nil
}

// activeHFTokenizer returns the loaded HF tokenizer, or nil if not ready.
func activeHFTokenizer() *HFTokenizer {
	hfState.mu.Lock()
	defer hfState.mu.Unlock()
	return hfState.tok
}
