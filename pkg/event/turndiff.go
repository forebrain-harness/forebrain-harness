package event

import (
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

type Summary struct {
	Path        string `json:"path"`
	Added       int    `json:"added"`
	Deleted     int    `json:"deleted"`
	UnifiedDiff string `json:"unified_diff,omitempty"`
}

// splitDiffSide splits a side of the diff into its real rows. difflib's
// SplitLines is not used: it appends one phantom empty row to every input —
// and one to the empty input — which made a created file report a deleted row
// and draw a phantom "1 -" whenever the other side ended without a newline,
// and made an empty side leak a phantom into the other side's count (plan
// 180). An empty side has no rows; a side has exactly the rows it shows.
func splitDiffSide(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(b), "\n")
	last := len(lines) - 1
	if lines[last] == "" {
		return lines[:last]
	}
	// An unterminated final row is still a row; the newline is only for the
	// writer, which emits every row as one terminated line.
	lines[last] += "\n"
	return lines
}

func Build(path string, before, after []byte) (Summary, error) {
	diffText, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        splitDiffSide(before),
		B:        splitDiffSide(after),
		FromFile: path + ":before",
		ToFile:   path + ":after",
		Context:  3,
	})
	out := Summary{
		Path:        path,
		UnifiedDiff: diffText,
	}
	for _, line := range strings.Split(diffText, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"), strings.HasPrefix(line, "@@"):
			continue
		case strings.HasPrefix(line, "+"):
			out.Added++
		case strings.HasPrefix(line, "-"):
			out.Deleted++
		}
	}
	return out, nil
}

// FirstHunk returns the first hunk of a unified diff — its `@@` header plus at
// most maxBodyRows body lines — so a caller that can only show one hunk does
// not ship the whole patch. It returns "" when there is no hunk.
//
// The bound is a parameter rather than a constant because the two callers
// read different amounts: a patch card shows a representative eight rows, an
// edit card forty. The slicing lives here so the hunk a payload carries is cut
// by the same reader that produced it.
func FirstHunk(unified string, maxBodyRows int) string {
	if maxBodyRows <= 0 {
		return ""
	}
	lines := strings.Split(unified, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "@@") {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	out := []string{lines[start]}
	for _, line := range lines[start+1:] {
		if strings.HasPrefix(line, "@@") {
			break
		}
		if len(out)-1 >= maxBodyRows {
			break
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
