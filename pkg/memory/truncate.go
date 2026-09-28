package memory

import (
	"fmt"
	"unicode/utf8"
)

const approxBytesPerToken = 4

// MiddleTokens truncates text using the common four-bytes-per-token estimate
// while retaining context from both ends of the input.
func MiddleTokens(value string, maxTokens int) string {
	if value == "" {
		return ""
	}
	maxBytes := max(0, maxTokens) * approxBytesPerToken
	if maxTokens > 0 && len(value) <= maxBytes {
		return value
	}
	if maxBytes == 0 {
		return tokenMarker(approxTokens(len(value)))
	}

	leftBudget := maxBytes / 2
	rightBudget := maxBytes - leftBudget
	leftEnd, rightStart := split(value, leftBudget, rightBudget)
	removed := approxTokens(max(0, len(value)-maxBytes))
	return value[:leftEnd] + tokenMarker(removed) + value[rightStart:]
}

func split(value string, leftBudget, rightBudget int) (int, int) {
	tailTarget := max(0, len(value)-rightBudget)
	leftEnd := 0
	rightStart := len(value)
	suffixStarted := false
	for index, char := range value {
		charEnd := index + utf8.RuneLen(char)
		if charEnd <= leftBudget {
			leftEnd = charEnd
			continue
		}
		if index >= tailTarget && !suffixStarted {
			rightStart = index
			suffixStarted = true
		}
	}
	if rightStart < leftEnd {
		rightStart = leftEnd
	}
	return leftEnd, rightStart
}

func approxTokens(bytes int) int {
	return (bytes + approxBytesPerToken - 1) / approxBytesPerToken
}

func tokenMarker(removed int) string {
	return fmt.Sprintf("…%d tokens truncated…", removed)
}
