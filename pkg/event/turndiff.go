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

func Build(path string, before, after []byte) (Summary, error) {
	diffText, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(string(before)),
		B:        difflib.SplitLines(string(after)),
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
