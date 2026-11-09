package tui

import "testing"

// Temporary review reproduction: checks the plan's expected wrapPanelWords
// outputs for the proposed TestWrapPanelWordsFoldsAtSpacesAndBreaksOnlyLongTokens.
func TestWrapPanelWordsReviewRepro(t *testing.T) {
	cases := []struct {
		text  string
		width int
	}{
		{"hex fast off", 7},
		{"hex fast off", 8},
		{"aaaaaaaaaa", 4},
		{"a  b", 10},
	}
	for _, tc := range cases {
		got := wrapPanelWords(tc.text, tc.width)
		t.Logf("wrapPanelWords(%q, %d) = %q", tc.text, tc.width, got)
	}
}
