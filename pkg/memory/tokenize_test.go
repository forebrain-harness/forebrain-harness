package memory

import (
	"strings"
	"testing"
)

func terms(t *testing.T, text string) string {
	t.Helper()
	got, err := searchTerms(text)
	if err != nil {
		t.Fatalf("searchTerms(%q): %v", text, err)
	}
	return strings.Join(got, "|")
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
