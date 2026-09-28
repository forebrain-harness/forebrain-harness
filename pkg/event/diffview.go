package event

import (
	"strconv"
	"strings"
)

// DiffLineKind classifies a diff line.
type DiffLineKind string

const (
	DiffLineAdd     DiffLineKind = "add"
	DiffLineDelete  DiffLineKind = "del"
	DiffLineContext DiffLineKind = "ctx"
	LineAdd                      = DiffLineAdd
	LineDel                      = DiffLineDelete
	LineContext                  = DiffLineContext
)

// DiffLine is a single line inside a parsed or wire diff.
type DiffLine struct {
	Kind  DiffLineKind `json:"kind"`
	OldNo int          `json:"old_no,omitempty"` // 0 when not applicable
	NewNo int          `json:"new_no,omitempty"` // 0 when not applicable
	Text  string       `json:"text"`
}

// Hunk is a contiguous block of changes introduced by a @@ header.
type Hunk struct {
	OldStart int
	NewStart int
	Lines    []DiffLine
}

// FileStat describes one file entry in the diff.
// Path/Added/Deleted are backward-compatible with all existing callers.
// OldPath/Status/Binary/Hunks are new fields for the renderer and wire payload.
type FileStat struct {
	Path    string // always the new/current path
	OldPath string // non-empty for renames
	// Status is one of: "added", "deleted", "modified", "renamed", "mode-only", "binary"
	Status  string
	Binary  bool
	Added   int
	Deleted int
	Hunks   []Hunk
}

// DiffDoc is the parsed representation of a git unified diff.
type DiffDoc struct {
	Summary string
	Files   []FileStat
	Raw     string
}

// Parse parses a raw git unified diff into a DiffDoc.
// It preserves backward-compat for all callers that read Summary, Files[].Path,
// Files[].Added, and Files[].Deleted, while adding structured Hunk/DiffLine data.
func Parse(raw string) DiffDoc {
	text := strings.TrimSpace(raw)
	if text == "" {
		return DiffDoc{}
	}
	doc := DiffDoc{Raw: text}
	lines := strings.Split(text, "\n")

	var cur *FileStat
	var curHunk *Hunk
	var oldLine, newLine int

	flushHunk := func() {
		if curHunk != nil && cur != nil {
			cur.Hunks = append(cur.Hunks, *curHunk)
			curHunk = nil
		}
	}
	flushFile := func() {
		flushHunk()
		if cur != nil {
			doc.Files = append(doc.Files, *cur)
			cur = nil
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flushFile()
			path := parseDiffPath(line)
			cur = &FileStat{Path: path, Status: "modified"}

		case cur != nil && strings.HasPrefix(line, "new file mode"):
			cur.Status = "added"

		case cur != nil && strings.HasPrefix(line, "deleted file mode"):
			cur.Status = "deleted"

		case cur != nil && strings.HasPrefix(line, "rename from "):
			cur.OldPath = strings.TrimPrefix(line, "rename from ")
			cur.Status = "renamed"

		case cur != nil && strings.HasPrefix(line, "rename to "):
			cur.Path = strings.TrimPrefix(line, "rename to ")

		case cur != nil && strings.HasPrefix(line, "old mode"):
			if cur.Status == "modified" {
				cur.Status = "mode-only"
			}

		case cur != nil && strings.HasPrefix(line, "Binary files"):
			cur.Binary = true
			cur.Status = "binary"

		// skip header lines; path already extracted from diff --git
		case cur != nil && (strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ")):

		case cur != nil && strings.HasPrefix(line, "@@ "):
			flushHunk()
			oldStart, newStart, ok := parseHunkHeader(line)
			if ok {
				oldLine = oldStart
				newLine = newStart
				curHunk = &Hunk{OldStart: oldStart, NewStart: newStart}
			} else {
				oldLine = 0
				newLine = 0
				curHunk = &Hunk{}
			}

		// add line: count always; add to hunk only when inside one.
		// Exclude "+++ " (the diff header line) but not "++++…" content.
		case cur != nil && len(line) > 0 && line[0] == '+' && !strings.HasPrefix(line, "+++ "):
			cur.Added++
			if curHunk != nil {
				curHunk.Lines = append(curHunk.Lines, DiffLine{
					Kind: DiffLineAdd, NewNo: newLine, Text: line[1:],
				})
				newLine++
			}

		// del line: count always; add to hunk only when inside one.
		// Exclude "--- " (the diff header line) but not "----…" content.
		case cur != nil && len(line) > 0 && line[0] == '-' && !strings.HasPrefix(line, "--- "):
			cur.Deleted++
			if curHunk != nil {
				curHunk.Lines = append(curHunk.Lines, DiffLine{
					Kind: DiffLineDelete, OldNo: oldLine, Text: line[1:],
				})
				oldLine++
			}

		// context line (space prefix) inside a hunk
		case cur != nil && curHunk != nil && len(line) > 0 && line[0] == ' ':
			curHunk.Lines = append(curHunk.Lines, DiffLine{
				Kind: DiffLineContext, OldNo: oldLine, NewNo: newLine, Text: line[1:],
			})
			oldLine++
			newLine++

		default:
			_ = trimmed
		}
	}
	flushFile()

	doc.Summary = BuildSummary(doc)
	return doc
}

// BuildSummary returns a one-line human-readable diff summary.
func BuildSummary(doc DiffDoc) string {
	if len(doc.Files) == 0 {
		if strings.TrimSpace(doc.Raw) == "" {
			return "diff: no changes"
		}
		return "diff: changes detected"
	}
	totalAdd, totalDel := 0, 0
	for _, f := range doc.Files {
		totalAdd += f.Added
		totalDel += f.Deleted
	}
	return strings.TrimSpace(
		"diff: files=" + strconv.Itoa(len(doc.Files)) +
			" +" + strconv.Itoa(totalAdd) +
			" -" + strconv.Itoa(totalDel),
	)
}

func parseDiffPath(line string) string {
	parts := strings.Fields(strings.TrimSpace(line))
	if len(parts) < 4 {
		return ""
	}
	path := strings.TrimPrefix(parts[3], "b/")
	return strings.TrimSpace(path)
}

// parseHunkHeader parses "@@ -<old>[,<n>] +<new>[,<n>] @@..." and returns
// the start line numbers for the old and new sides.
func parseHunkHeader(line string) (oldStart, newStart int, ok bool) {
	after, found := strings.CutPrefix(line, "@@ ")
	if !found {
		return 0, 0, false
	}
	// discard the trailing "@@" and any context text
	parts := strings.SplitN(after, " @@", 2)
	ranges := strings.Fields(parts[0])
	if len(ranges) < 2 {
		return 0, 0, false
	}
	oldStr, _ := strings.CutPrefix(ranges[0], "-")
	newStr, _ := strings.CutPrefix(ranges[1], "+")
	oldStr = strings.SplitN(oldStr, ",", 2)[0]
	newStr = strings.SplitN(newStr, ",", 2)[0]
	old, err1 := strconv.Atoi(oldStr)
	nw, err2 := strconv.Atoi(newStr)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return old, nw, true
}
