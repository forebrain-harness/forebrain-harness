package memory

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/go-ego/gse"
	"github.com/ikawaha/kagome-dict/dict"
	"github.com/ikawaha/kagome/v2/tokenizer"
)

// Word segmentation for the memory search index.
//
// FTS5's own tokenizers split on non-alphanumeric characters, which never
// separates Chinese or Japanese: a whole sentence arrives as one token, so
// every such memory would be one unsearchable blob. The text is therefore
// segmented here, before it reaches FTS5, and stored as space-separated terms —
// leaving FTS5's tokenizer nothing to do but split on those spaces.
//
// Each language that writes no spaces between its words is cut by its own
// rules: Chinese by a Chinese dictionary, Japanese by a Japanese morphological
// analyzer, Korean into syllable pairs. A dictionary is read the first time its
// language turns up, so a store that never holds that language never pays for
// it.
//
// One function decides where a term begins and ends, and every side calls it —
// the index, the query, and the marker that shows the reader what matched — so
// a term can never be cut, looked up, or marked by different rules.

// TermsRevision is the revision of the rules below. Indexed rows record the
// revision they were written under, so changing how text is split into terms
// invalidates every row that predates the change: the index is derived data,
// and a row produced by older rules answers a query produced by newer ones with
// terms the two sides spell differently.
const TermsRevision = 4

// searchTerms splits text into terms: the terms a line is stored under, and the
// terms a query is asked with, cut by the same rules.
//
// A compound word is one term. The segmenter reads a connector as punctuation
// and drops it, which turns "chatibs_claw_client" into "chatibs", "claw" and
// "client", and "read-only" into "read" and "only" — ordinary words that occur
// all over a store, so the search answers a request for one compound with every
// line that merely says "client", or marks the "read" of an unrelated
// "read_file" as the reason a line came back. Compound words are therefore cut
// out of the text first and kept whole, and only what is left is segmented.
//
// Terms are lowercased because FTS5's tokenizer folds case, so a term that
// reached the index in another case would never be found again. A dictionary
// that cannot be read is an error of the search, reported as the file system
// reported it.
func searchTerms(text string) ([]string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	var raw []string
	for _, run := range splitWordRuns(text) {
		if run.word {
			raw = append(raw, run.text)
			continue
		}
		cut, err := segmentRun(run.text)
		if err != nil {
			return nil, err
		}
		raw = append(raw, cut...)
	}
	terms := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, term := range raw {
		term = strings.ToLower(strings.TrimSpace(term))
		if term == "" {
			continue
		}
		if _, repeat := seen[term]; repeat {
			continue
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}
	return terms, nil
}

// segmentRun cuts the text between two words into terms. What is there is text
// in scripts written without spaces between their words, with the spaces and
// punctuation around it; each stretch of it is cut by the rules of its
// language, and a stretch that is only spaces, punctuation or symbols holds no
// term at all.
func segmentRun(text string) ([]string, error) {
	var terms []string
	for _, piece := range scriptPieces(text) {
		switch piece.language {
		case languageKorean:
			terms = append(terms, koreanTerms(piece.text)...)
		case languageJapanese:
			cut, err := japaneseTerms(piece.text)
			if err != nil {
				return nil, err
			}
			terms = append(terms, cut...)
		case languageChinese:
			cut, err := chineseTerms(piece.text)
			if err != nil {
				return nil, err
			}
			terms = append(terms, cut...)
		}
	}
	return terms, nil
}

type language int

const (
	languageNone language = iota
	languageChinese
	languageJapanese
	languageKorean
)

type scriptPiece struct {
	text     string
	language language
}

// scriptPieces splits text into Korean words and the stretches between them,
// and names the language of each stretch: Japanese when it holds kana, which
// Japanese is never written without and Chinese never uses; Chinese when it
// holds Chinese characters and no kana; none when it holds neither.
func scriptPieces(text string) []scriptPiece {
	var pieces []scriptPiece
	start := 0
	flush := func(end int, korean bool) {
		if end <= start {
			return
		}
		piece := scriptPiece{text: text[start:end]}
		switch {
		case korean:
			piece.language = languageKorean
		case strings.IndexFunc(piece.text, isKana) >= 0:
			piece.language = languageJapanese
		case strings.IndexFunc(piece.text, isHan) >= 0:
			piece.language = languageChinese
		}
		pieces = append(pieces, piece)
		start = end
	}
	inKorean := false
	for index, char := range text {
		if hangul := unicode.Is(unicode.Hangul, char); hangul != inKorean {
			flush(index, inKorean)
			inKorean = hangul
		}
	}
	flush(len(text), inKorean)
	return pieces
}

// koreanTerms cuts one Korean word into the pairs of syllables it is made of.
//
// Korean writes spaces between its words, but a word carries its particles and
// endings with it — "검색에서" is "검색" followed by "에서" — so a whole word
// is rarely what a search asks for. Every pair of adjacent syllables is a term
// instead, which lets "검색" find "검색에서" and "검색을" alike; a word of one
// syllable is its own term.
func koreanTerms(word string) []string {
	syllables := []rune(word)
	if len(syllables) < 2 {
		return []string{word}
	}
	terms := make([]string, 0, len(syllables)-1)
	for index := 0; index+1 < len(syllables); index++ {
		terms = append(terms, string(syllables[index:index+2]))
	}
	return terms
}

// japaneseTerms cuts Japanese text into its words with the morphological
// analyzer, in its search mode, which also splits a long compound into the
// words it is built from. Particles, auxiliary verbs and symbols are dropped:
// they occur in nearly every line, and a term that matches everything adds
// candidates without adding evidence.
func japaneseTerms(text string) ([]string, error) {
	analyzer, err := japaneseAnalyzer()
	if err != nil {
		return nil, err
	}
	var terms []string
	for _, token := range analyzer.Analyze(text, tokenizer.Search) {
		if pos := token.POS(); len(pos) > 0 && japaneseFunctionWords[pos[0]] {
			continue
		}
		if strings.TrimSpace(token.Surface) == "" {
			continue
		}
		terms = append(terms, token.Surface)
	}
	return terms, nil
}

// japaneseFunctionWords are the parts of speech a Japanese term is never made
// of: particles, auxiliary verbs and symbols.
var japaneseFunctionWords = map[string]bool{"助詞": true, "助動詞": true, "記号": true}

// chineseTerms cuts Chinese text into its words with the Chinese dictionary,
// and drops the stop words — function words and punctuation that occur in
// nearly every line.
func chineseTerms(text string) ([]string, error) {
	segmenter, err := chineseSegmenter()
	if err != nil {
		return nil, err
	}
	var terms []string
	for _, term := range segmenter.CutTrim(text, true) {
		if segmenter.IsStop(strings.TrimSpace(term)) {
			continue
		}
		terms = append(terms, term)
	}
	return terms, nil
}

var (
	chineseOnce      sync.Once
	chineseLoaded    *gse.Segmenter
	chineseLoadError error

	japaneseOnce      sync.Once
	japaneseLoaded    *tokenizer.Tokenizer
	japaneseLoadError error
)

// chineseSegmenter reads the Chinese dictionary once, the first time Chinese
// text is cut. It costs seconds and a large share of memory to load, so a
// process that never meets Chinese never loads it.
func chineseSegmenter() (*gse.Segmenter, error) {
	chineseOnce.Do(func() { chineseLoaded, chineseLoadError = loadChineseSegmenter() })
	return chineseLoaded, chineseLoadError
}

func loadChineseSegmenter() (*gse.Segmenter, error) {
	var contents [3]string
	for index, name := range []string{chineseSimplifiedWords, chineseTraditionalWords, chineseStopWords} {
		path, err := dictionaryPath(name)
		if err != nil {
			return nil, dictionaryError(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, dictionaryError(err)
		}
		contents[index] = string(data)
	}
	// The two word lists are loaded as one: the token table is computed once,
	// over every word, exactly as the segmenter computes it for its own
	// Chinese dictionary. The line break keeps the last word of one list and
	// the first of the next apart; a blank line is not a word.
	var segmenter gse.Segmenter
	if err := segmenter.LoadDictStr(contents[0] + "\n" + contents[1]); err != nil {
		return nil, dictionaryError(err)
	}
	if err := segmenter.LoadStopStr(contents[2]); err != nil {
		return nil, dictionaryError(err)
	}
	return &segmenter, nil
}

// japaneseAnalyzer reads the Japanese dictionary once, the first time
// Japanese text is cut. Only what the analyzer cuts by is loaded — each word's
// reading and base form are not, since no term is made of them.
func japaneseAnalyzer() (*tokenizer.Tokenizer, error) {
	japaneseOnce.Do(func() { japaneseLoaded, japaneseLoadError = loadJapaneseAnalyzer() })
	return japaneseLoaded, japaneseLoadError
}

func loadJapaneseAnalyzer() (*tokenizer.Tokenizer, error) {
	path, err := dictionaryPath(japaneseDictionary)
	if err != nil {
		return nil, dictionaryError(err)
	}
	loaded, err := dict.LoadShrink(path)
	if err != nil {
		return nil, dictionaryError(err)
	}
	analyzer, err := tokenizer.New(loaded, tokenizer.OmitBosEos())
	if err != nil {
		return nil, dictionaryError(err)
	}
	return analyzer, nil
}

func dictionaryError(err error) error {
	return fmt.Errorf("memory search dictionary unavailable: %w", err)
}

// wordRun is one stretch of text of a single kind: a word the segmenter must
// not touch, or the text between such words.
type wordRun struct {
	text string
	word bool
}

// splitWordRuns cuts text into whole words and everything else.
//
// A word here is a run of letters, digits and joining marks in a script that
// writes spaces between its words. The joining marks are in that set because
// they join: the underscore is how snake_case writes one compound word, the
// hyphen is how "read-only" and "--dry-run" write theirs, and the dot is how
// "8.4.5", "3.14" and "forebrain.yaml" write theirs — none of them separates the
// parts it stands between. A run has to carry a letter or a digit to count — a
// bullet's hyphen and a rule's row of underscores are punctuation, and the
// segmenter drops them as it always has.
//
// Everything else — Chinese, Japanese, Korean, punctuation, spaces — stays in
// the runs handed to the segmenter, which is the text it exists to cut.
func splitWordRuns(text string) []wordRun {
	var runs []wordRun
	start := 0
	inWord := false
	letters := false
	flush := func(end int, word bool) {
		if end <= start {
			return
		}
		runs = append(runs, wordRun{text: text[start:end], word: word})
		start = end
	}
	for index, char := range text {
		wordRune := wordRuneAt(text, index)
		switch {
		case wordRune && !inWord:
			flush(index, false)
			inWord, letters = true, false
		case !wordRune && inWord:
			// A run of nothing but connectors is not a word. It is handed back
			// to the segmenter as the punctuation it is.
			flush(index, letters)
			inWord = false
		}
		if inWord && (unicode.IsLetter(char) || unicode.IsDigit(char)) {
			letters = true
		}
	}
	flush(len(text), inWord && letters)
	return runs
}

// wordRuneAt reports whether the rune that begins at byte index of text is part
// of a word. It is the one definition of a word's extent: the terms are cut by
// it, and a mark is only drawn where it says a word begins and ends.
//
// Connectors belong to a word wherever they stand. A dot belongs to one only
// between two letters or digits: inside "8.4.5" it joins, while the dot that
// ends a sentence, or opens ".gitignore", stands at a word's edge and is the
// punctuation it looks like.
func wordRuneAt(text string, index int) bool {
	char, size := utf8.DecodeRuneInString(text[index:])
	if char != '.' {
		return isWordRune(char)
	}
	before, _ := utf8.DecodeLastRuneInString(text[:index])
	after, _ := utf8.DecodeRuneInString(text[index+size:])
	return isWordCharacter(before) && isWordCharacter(after)
}

func isWordRune(char rune) bool {
	return isConnectorRune(char) || isWordCharacter(char)
}

// isWordCharacter reports whether char is a letter or digit of a script that
// writes spaces between its words.
func isWordCharacter(char rune) bool {
	if !unicode.IsLetter(char) && !unicode.IsDigit(char) {
		return false
	}
	return !segmentedScript(char)
}

// isConnectorRune reports whether char joins a compound word instead of
// separating two words. Both of these spell one name — "read_file" is a
// filename and "read-only" is one adjective — and a search that cut them apart
// would be searching for words neither line is about.
func isConnectorRune(char rune) bool { return char == '_' || char == '-' }

// segmentedScript reports whether char belongs to a script whose words are
// found by segmentRun rather than by the spaces around them.
func segmentedScript(char rune) bool {
	return isHan(char) || isKana(char) || unicode.Is(unicode.Hangul, char)
}

func isHan(char rune) bool { return unicode.Is(unicode.Han, char) }

// isKana reports whether char is hiragana or katakana. The long-vowel mark
// "ー" (and its half-width form) belongs to no script of its own, but it only
// ever lengthens a kana syllable — "メモリー" is one word.
func isKana(char rune) bool {
	return unicode.In(char, unicode.Hiragana, unicode.Katakana) || char == 'ー' || char == 'ｰ'
}
