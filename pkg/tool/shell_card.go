package tool

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// ShellCard is what a shell card shows, in parts, read back from the card's
// own body (formatShellStep): a reloaded card and a live one come from the
// same body, so they always agree. Absent facts are zero.
type ShellCard struct {
	Stdout             string          `json:"stdout,omitempty"`
	Stderr             string          `json:"stderr,omitempty"`
	ExitCode           *int            `json:"exit_code,omitempty"`
	StdoutOmittedBytes int             `json:"stdout_omitted_bytes,omitempty"`
	StderrOmittedBytes int             `json:"stderr_omitted_bytes,omitempty"`
	OmittedBytes       int             `json:"omitted_bytes,omitempty"`       // "output omitted N bytes" (neither stream named)
	RetrieveID         int64           `json:"retrieve_id,omitempty"`         // the full output's retrieval id, when spooled
	SpoolOmittedBytes  int             `json:"spool_omitted_bytes,omitempty"` // past the spool's 64 MB cap: not kept
	Files              []ShellCardFile `json:"files,omitempty"`
	Notes              []string        `json:"notes,omitempty"`
}

// ShellCardFile is one changed file (plan 130's DiffBlock) with JSON names.
type ShellCardFile struct {
	Verb        string `json:"verb"` // "Updated", "Created" or "Deleted"
	Path        string `json:"path"`
	Added       int    `json:"added"`
	Deleted     int    `json:"deleted"`
	UnifiedDiff string `json:"unified_diff,omitempty"`
}

// shellCardOmissionRe reads one count out of the truncation note a clamped
// card carries: "preview truncated: stdout omitted 412000 bytes; …".
var shellCardOmissionRe = regexp.MustCompile(`(?:stdout|stderr|output) omitted (\d+) bytes`)

// ParseShellCard reads formatShellStep's body back into its facts. Labels are
// only read outside fences, so a stream that prints "output:" at a line start
// is still just stream text. A body that is not a shell card reports false.
func ParseShellCard(body string) (ShellCard, bool) {
	if !strings.HasPrefix(body, "**shell**") {
		return ShellCard{}, false
	}
	card := ShellCard{}
	lines := strings.Split(body, "\n")
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if n := diffFenceLen(trimmed); n > 0 {
			i = skipDiffFence(lines, i, n)
			continue
		}
		if strings.HasPrefix(trimmed, "preview truncated: ") {
			for _, m := range shellCardOmissionRe.FindAllStringSubmatch(trimmed, -1) {
				n, _ := strconv.Atoi(m[1])
				switch {
				case strings.HasPrefix(m[0], "stdout omitted"):
					card.StdoutOmittedBytes = n
				case strings.HasPrefix(m[0], "stderr omitted"):
					card.StderrOmittedBytes = n
				default:
					card.OmittedBytes = n
				}
			}
			continue
		}
		if trimmed != "stdout:" && trimmed != "stderr:" && trimmed != "output:" {
			continue
		}
		text, info, last, ok := fencedBodyAt(lines, i+1)
		i = last
		if !ok {
			continue
		}
		switch trimmed {
		case "stdout:":
			card.Stdout = text
		case "stderr:":
			card.Stderr = text
		case "output:":
			if info != "json" {
				continue
			}
			var fields map[string]any
			if json.Unmarshal([]byte(text), &fields) != nil {
				// A clamped body can cut the JSON mid-way; the streams and
				// the note above them still read.
				continue
			}
			if _, ok := fields["exit_code"]; ok {
				code := intFromAny(fields["exit_code"])
				card.ExitCode = &code
			}
			card.RetrieveID = int64(intFromAny(fields["retrieve_id"]))
			card.StdoutOmittedBytes = intFromAny(fields["stdout_omitted_bytes"])
			card.StderrOmittedBytes = intFromAny(fields["stderr_omitted_bytes"])
			card.SpoolOmittedBytes = intFromAny(fields["spool_omitted_bytes"])
		}
	}
	if blocks, ok := ParseDiffBlocks(body); ok {
		for _, f := range blocks.Files {
			card.Files = append(card.Files, ShellCardFile{
				Verb: f.Verb, Path: f.Path,
				Added: f.Added, Deleted: f.Deleted, UnifiedDiff: f.UnifiedDiff,
			})
		}
		card.Notes = blocks.Notes
	}
	return card, true
}

// FileDiff is the change a file tool's card shows (its turn diff), read back
// from the card's body; Rest is the body after that section (diagnostics and
// the like), for the surface to render as it always has.
type FileDiff struct {
	Path        string `json:"path"`
	Added       int    `json:"added"`
	Deleted     int    `json:"deleted"`
	UnifiedDiff string `json:"unified_diff,omitempty"`
	Rest        string `json:"rest,omitempty"`
}

// fileDiffHeaderRe matches the turn diff section's first line:
// turn diff: `a.go` (+2/-1).
var fileDiffHeaderRe = regexp.MustCompile("^turn diff: `(.+)` \\(\\+(\\d+)/-(\\d+)\\)$")

// ParseFileDiff reads the turn diff section a file tool's card opens with
// (turnDiffSection). ok is false when the body does not open with it.
func ParseFileDiff(body string) (FileDiff, bool) {
	lines := strings.Split(body, "\n")
	if len(lines) == 0 {
		return FileDiff{}, false
	}
	m := fileDiffHeaderRe.FindStringSubmatch(strings.TrimSpace(lines[0]))
	if m == nil {
		return FileDiff{}, false
	}
	added, _ := strconv.Atoi(m[2])
	deleted, _ := strconv.Atoi(m[3])
	out := FileDiff{Path: m[1], Added: added, Deleted: deleted}
	last := 0
	if text, info, fenceLast, ok := fencedBodyAt(lines, 1); ok && info == "diff" {
		out.UnifiedDiff = text
		last = fenceLast
	}
	out.Rest = strings.TrimSpace(strings.Join(lines[last+1:], "\n"))
	return out, true
}

// fencedBodyAt reads the fence that follows line i (blank lines allowed
// between): its info string, its body without the opening and closing lines,
// and the index of its closing line (the last line of the body when a clamp
// cut the card before the fence closed).
func fencedBodyAt(lines []string, i int) (body, info string, last int, ok bool) {
	for ; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "" {
			break
		}
	}
	if i >= len(lines) {
		return "", "", i, false
	}
	n := diffFenceLen(strings.TrimSpace(lines[i]))
	if n == 0 {
		return "", "", i, false
	}
	info = strings.TrimSpace(strings.TrimSpace(lines[i])[n:])
	close := strings.Repeat("`", n)
	var out []string
	for i++; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t\r") == close {
			return strings.Join(out, "\n"), info, i, true
		}
		out = append(out, lines[i])
	}
	return strings.Join(out, "\n"), info, len(lines) - 1, true
}

// FencedBodyAt is fencedBodyAt for surfaces outside this package: it reads the
// backtick fence that follows line i (blank lines allowed between), accepting
// any opening run of three or more backticks and closing only on the same
// length — the reader half of fenceFor, which escalates the run when the body
// itself quotes a fence. Keeping one implementation keeps every surface and the
// writer in agreement.
func FencedBodyAt(lines []string, i int) (body, info string, last int, ok bool) {
	return fencedBodyAt(lines, i)
}
