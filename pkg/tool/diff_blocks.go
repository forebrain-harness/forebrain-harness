package tool

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// DiffBlock is one changed file a shell card shows as a diff block: a header
// line, then the change as a unified diff.
type DiffBlock struct {
	Verb        string // "Updated", "Created" or "Deleted"
	Path        string // as displayed: relative to the working directory when inside it
	Added       int
	Deleted     int
	UnifiedDiff string // empty: header only (an empty file created/deleted, or a diff too large to show)
}

// DiffBlocks is the file-change part of a shell card: the files, then the short
// notes that follow them.
type DiffBlocks struct {
	Files []DiffBlock
	Notes []string
}

// The per-file hunk budget a card enforces on the write side. shell_diff.go
// keeps its own (identical) copy as a var so tests can shrink the selection
// limits without changing what a card renders.
const (
	diffBlockMaxHunkLines = 400
	diffBlockMaxHunkChars = 64000
)

// The note texts, copied verbatim from Claude Code's bashEditDiff.
const (
	diffNoteSkipped   = "(file diff skipped for this git command)"
	diffNoteShared    = "(another command ran in this repository at the same time; a change made by either may show under either result)"
	diffNoteTreeMoved = "(a git step in this command can move the working tree, so these may be its changes, not edits)"
)

// diffHunkUsage reports how many hunk body lines a unified diff carries and
// how many bytes they span: every line after the first "@@" line that is not
// itself a "@@" line.
func diffHunkUsage(diff string) (lines, chars int) {
	started := false
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "@@") {
			started = true
			continue
		}
		if !started {
			continue
		}
		lines++
		chars += len(line)
	}
	return lines, chars
}

// diffBlocksMarkdown renders blocks as the Markdown a shell card carries:
// one section per file (a header line, then the unified diff in a fence when
// it fits), then one line per note. Sections are separated by one blank line.
// An empty blocks value renders as "". budget <= 0 means unlimited; a positive
// budget that cannot fit a file's hunks still leaves the header line.
func diffBlocksMarkdown(blocks DiffBlocks, budget int) string {
	var sections []string
	total := 0
	for _, f := range blocks.Files {
		header := fmt.Sprintf("%s `%s` (+%d -%d)", f.Verb, f.Path, f.Added, f.Deleted)
		section := header
		if diff := strings.TrimRight(f.UnifiedDiff, "\n"); diff != "" {
			hunkLines, hunkChars := diffHunkUsage(diff)
			if hunkLines <= diffBlockMaxHunkLines && hunkChars <= diffBlockMaxHunkChars {
				fence := "```"
				for strings.Contains(diff, fence) {
					fence += "`"
				}
				candidate := header + "\n\n" + fence + "diff\n" + diff + "\n" + fence
				sep := 0
				if len(sections) > 0 {
					sep = 2
				}
				if budget <= 0 || total+sep+len(candidate) <= budget {
					section = candidate
				}
			}
		}
		if len(sections) > 0 {
			total += 2
		}
		sections = append(sections, section)
		total += len(section)
	}
	for _, note := range blocks.Notes {
		if len(sections) > 0 {
			total += 2
		}
		sections = append(sections, note)
		total += len(note)
	}
	if len(sections) == 0 {
		return ""
	}
	return strings.Join(sections, "\n\n")
}

// diffBlockHeaderRe matches a file header line outside any fence:
// Updated `path` (+1 -2).
var diffBlockHeaderRe = regexp.MustCompile("^(Updated|Created|Deleted) `(.+)` \\(\\+(\\d+) -(\\d+)\\)$")

// diffBlockMoreFilesRe matches the no-files-shown variant of the more-files
// note: "3 files changed (binary, mode only or too large to show)".
var diffBlockMoreFilesRe = regexp.MustCompile(`^\d+ files? changed \(binary, mode only or too large to show\)`)

// diffBlockNote reports whether a fence-outside line is one of the notes
// diffBlocksMarkdown writes.
func diffBlockNote(line string) bool {
	return strings.HasPrefix(line, "(file diff ") ||
		strings.HasPrefix(line, "(another command ran in this repository") ||
		strings.HasPrefix(line, "(a git step in this command") ||
		strings.HasPrefix(line, "… ") ||
		diffBlockMoreFilesRe.MatchString(line)
}

// diffFenceLen returns the number of backticks a fence-opening line starts
// with; 0 when the line does not open a fence.
func diffFenceLen(trimmed string) int {
	n := 0
	for n < len(trimmed) && trimmed[n] == '`' {
		n++
	}
	if n < 3 {
		return 0
	}
	return n
}

// ParseDiffBlocks reads the diff blocks a shell card body carries: one
// DiffBlock per header line, its unified diff taken from the ```diff fence
// that follows the header, plus the notes after the files. Headers and notes
// are only recognized outside fences, so stdout/stderr bodies never parse as
// blocks. ok is false when the body carries neither a header nor a note.
func ParseDiffBlocks(body string) (DiffBlocks, bool) {
	return scanDiffBlocks(body, false)
}

// HasDiffBlocks reports whether a shell card body carries any diff block
// (file header or note) outside its fences.
func HasDiffBlocks(body string) bool {
	_, ok := scanDiffBlocks(body, true)
	return ok
}

// scanDiffBlocks walks body line by line, tracking fences the same
// Markdown does: a line that trims to ``` opens a fence of that many
// backticks, and only a line that right-trims to exactly those backticks
// closes it. stopEarly returns as soon as a header or note is seen.
func scanDiffBlocks(body string, stopEarly bool) (DiffBlocks, bool) {
	lines := strings.Split(body, "\n")
	blocks := DiffBlocks{}
	pending := false
	var pendingFile DiffBlock
	flush := func() {
		if pending {
			blocks.Files = append(blocks.Files, pendingFile)
			pending = false
		}
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if n := diffFenceLen(trimmed); n > 0 {
			// A fence may hold the pending header's unified diff; any other
			// fence leaves the header without hunks.
			if pending && strings.TrimSpace(trimmed[n:]) == "diff" {
				body := make([]string, 0, 8)
				i++
				for ; i < len(lines); i++ {
					if strings.TrimRight(lines[i], " \t\r") == strings.Repeat("`", n) {
						break
					}
					body = append(body, lines[i])
				}
				pendingFile.UnifiedDiff = strings.Join(body, "\n")
				flush()
				continue
			}
			flush()
			i = skipDiffFence(lines, i, n)
			continue
		}
		if m := diffBlockHeaderRe.FindStringSubmatch(trimmed); m != nil {
			flush()
			added, _ := strconv.Atoi(m[3])
			deleted, _ := strconv.Atoi(m[4])
			pendingFile = DiffBlock{Verb: m[1], Path: m[2], Added: added, Deleted: deleted}
			pending = true
			if stopEarly {
				return blocks, true
			}
			continue
		}
		if diffBlockNote(trimmed) {
			flush()
			blocks.Notes = append(blocks.Notes, trimmed)
			if stopEarly {
				return blocks, true
			}
			continue
		}
		if trimmed != "" {
			// Something that is neither header, note nor fence ends the
			// pending header's chance of carrying a diff.
			flush()
		}
	}
	flush()
	return blocks, len(blocks.Files) > 0 || len(blocks.Notes) > 0
}

// skipDiffFence advances past a fence opened at lines[i] with n backticks
// (the opening line itself); the return value is the index of the closing
// line, ready for the caller's i++.
func skipDiffFence(lines []string, i, n int) int {
	close := strings.Repeat("`", n)
	for ; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t\r") == close {
			return i
		}
	}
	return len(lines) - 1
}

// diffFilesWord pluralizes a count for the more-files note.
func diffFilesWord(n int) string {
	if n == 1 {
		return "file"
	}
	return "files"
}

// diffFileCount renders "1 file" / "N files".
func diffFileCount(n int) string {
	return fmt.Sprintf("%d %s", n, diffFilesWord(n))
}

// diffMoreFilesNote is the note for changed files the card did not show:
// when some files are shown it counts the rest; when none are shown it says
// why (binary, mode-only or too large). unavailable appends the marker.
func diffMoreFilesNote(filesShown bool, n int, unavailable bool) string {
	var note string
	if filesShown {
		note = fmt.Sprintf("… %d more %s changed", n, diffFilesWord(n))
	} else {
		note = diffFileCount(n) + " changed (binary, mode only or too large to show)"
	}
	if unavailable {
		note += " (part of the diff is unavailable)"
	}
	return note
}

// diffUnavailableNote is the note a card shows when the diff cannot be
// computed at all, optionally counting the files that did change.
func diffUnavailableNote(n int) string {
	if n > 0 {
		return "(file diff unavailable for this command; " + diffFileCount(n) + " changed)"
	}
	return "(file diff unavailable for this command)"
}

// displayPathRelativeTo is how a card names a file: relative to dir when the
// file is inside it, absolute otherwise.
func displayPathRelativeTo(dir, abs string) string {
	if dir == "" {
		return abs
	}
	if rel, err := filepath.Rel(dir, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return rel
	}
	// The same directory can be spelled two ways when it sits behind a
	// symlink (/tmp vs /private/tmp on macOS, where git resolves the work
	// tree root but the caller's working directory stays logical). Resolve
	// and retry once before falling back to the absolute path.
	if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != dir {
		if rel, err := filepath.Rel(resolved, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return rel
		}
	}
	return abs
}

// displayPathFor is displayPathRelativeTo against the call's working
// directory.
func displayPathFor(ctx context.Context, abs string) string {
	wd, err := WorkingDir(ctx)
	if err != nil {
		return abs
	}
	return displayPathRelativeTo(wd, abs)
}

// applyPatchDiffBlocks turns an apply_patch completion output into the diff
// blocks its card shows: one block per file the patch touched, the per-file
// diff the engine already computed. apply_patch carries no notes.
func applyPatchDiffBlocks(output map[string]any) (DiffBlocks, bool) {
	if !boolFromAny(output["apply_patch"]) {
		return DiffBlocks{}, false
	}
	blocks := DiffBlocks{}
	for _, raw := range sliceFromAny(output["files"]) {
		entry := mapFromAny(raw)
		if len(entry) == 0 {
			continue
		}
		verb := "Updated"
		switch stringFromAny(entry["operation"]) {
		case "add":
			verb = "Created"
		case "delete":
			verb = "Deleted"
		}
		path := stringFromAny(entry["display_path"])
		if path == "" {
			path = stringFromAny(entry["abs_path"])
		}
		if source := stringFromAny(entry["source_abs_path"]); source != "" {
			sourceDisplay := stringFromAny(entry["source_display_path"])
			if sourceDisplay == "" {
				sourceDisplay = source
			}
			path = sourceDisplay + " → " + path
		}
		sum, _ := extractTurnDiff(entry["turn_diff"])
		blocks.Files = append(blocks.Files, DiffBlock{
			Verb: verb, Path: path,
			Added: sum.Added, Deleted: sum.Deleted, UnifiedDiff: sum.UnifiedDiff,
		})
	}
	if len(blocks.Files) == 0 {
		return DiffBlocks{}, false
	}
	return blocks, true
}

// shellEditDiffBlocks turns the "shell_edit_diff" value of a shell completion
// output into the diff blocks its card shows, with Claude Code's note set.
func shellEditDiffBlocks(output map[string]any) (DiffBlocks, bool) {
	diff, ok := DecodeShellEditDiff(output["shell_edit_diff"])
	if !ok {
		return DiffBlocks{}, false
	}
	if diff.Skipped {
		return DiffBlocks{Notes: []string{diffNoteSkipped}}, true
	}
	if len(diff.Files) == 0 && diff.Unavailable {
		return DiffBlocks{Notes: []string{diffUnavailableNote(diff.MoreFiles)}}, true
	}
	if len(diff.Files) == 0 && diff.Shared {
		return DiffBlocks{Notes: []string{diffNoteShared}}, true
	}
	blocks := DiffBlocks{}
	for _, f := range diff.Files {
		path := f.DisplayPath
		if path == "" {
			path = f.Path
		}
		verb := "Updated"
		switch f.Status {
		case "created":
			verb = "Created"
		case "deleted":
			verb = "Deleted"
		}
		blocks.Files = append(blocks.Files, DiffBlock{
			Verb: verb, Path: path,
			Added: f.Added, Deleted: f.Deleted, UnifiedDiff: f.UnifiedDiff,
		})
	}
	if diff.MoreFiles > 0 {
		blocks.Notes = append(blocks.Notes, diffMoreFilesNote(len(blocks.Files) > 0, diff.MoreFiles, diff.Unavailable))
	}
	if diff.TreeMoved {
		blocks.Notes = append(blocks.Notes, diffNoteTreeMoved)
	}
	if diff.Shared {
		blocks.Notes = append(blocks.Notes, diffNoteShared)
	}
	if len(blocks.Files) == 0 && len(blocks.Notes) == 0 {
		return DiffBlocks{}, false
	}
	return blocks, true
}

// shellDiffBlocks picks the diff blocks a shell card shows from its
// completion output: apply_patch reports the files it patched, every other
// command reports the working-tree changes the snapshot pair observed.
func shellDiffBlocks(output map[string]any) (DiffBlocks, bool) {
	if boolFromAny(output["apply_patch"]) {
		return applyPatchDiffBlocks(output)
	}
	return shellEditDiffBlocks(output)
}
