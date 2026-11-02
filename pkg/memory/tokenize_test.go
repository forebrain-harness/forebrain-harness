package memory

import (
	"strings"
	"testing"
)

func terms(t *testing.T, text string) string {
	t.Helper()
	return strings.Join(searchTerms(text), "|")
}

// Each language is cut by its own rules, and a word in a spaced script is kept
// whole wherever it stands.
func TestSearchTermsCutEachLanguageByItsOwnRules(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		// Chinese: dictionary words, stop words dropped.
		{"记忆检索的分词问题已经修复", "记忆|检索|分词|问题|已经|修复"},
		// Japanese: morphemes, particles and auxiliary verbs dropped, the
		// long-vowel mark kept inside its word.
		{"東京都の天気予報を確認してからデプロイします。", "東京|都|天気|予報|確認|し|デプロイ"},
		{"メモリーの検索", "メモリー|検索"},
		// Korean: pairs of syllables within each word.
		{"검색에서 수", "검색|색에|에서|수"},
		// Latin words, a version and a compound pass through whole, and the
		// language between them is still cut.
		{"forebrain 8.4.5 修复了 read-only 的问题", "forebrain|8.4.5|修复|read-only|问题"},
		// Punctuation and symbols alone hold no term.
		{" , $ + = ", ""},
	} {
		if got := terms(t, tc.text); got != tc.want {
			t.Errorf("searchTerms(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

// Without its dictionaries a search cannot find word boundaries, so it cuts each
// stretch of Chinese or Japanese text into the pairs of adjacent characters it is
// made of: the narrowest term that still means something, and one both the index
// and the query are cut by, so a word that is written the same way is still found.
func TestUnsegmentedTermsCutTextIntoCharacterPairs(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"行李托运", "行李|李托|托运"},
		// Spaces and punctuation separate runs: a pair never straddles one.
		{"审批 浮层", "审批|浮层"},
		{"审", "审"},
		{"メモリーの検索", "メモ|モリ|リー|ーの|の検|検索"},
		{" , 。 + ", ""},
	} {
		if got := strings.Join(unsegmentedTerms(tc.text), "|"); got != tc.want {
			t.Errorf("unsegmentedTerms(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}
