package memory

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

const (
	citationOpen  = "<oai-mem-citation>"
	citationClose = "</oai-mem-citation>"
)

var citationRe = regexp.MustCompile(`(?s)<oai-mem-citation>(.*?)</oai-mem-citation>`)

type CitationStreamFilter struct {
	buffer strings.Builder
}

// Push consumes one streamed assistant delta and returns only text that is
// provably outside hidden citation markup. It buffers partial tag prefixes and
// complete citation bodies across arbitrary chunk boundaries.
func (f *CitationStreamFilter) Push(chunk string) string {
	if f == nil || chunk == "" {
		return ""
	}
	f.buffer.WriteString(chunk)
	return f.drain(false)
}

// Finish flushes remaining visible text. An unterminated complete citation tag
// is treated as hidden through EOF; a partial opening-tag prefix remains visible.
func (f *CitationStreamFilter) Finish() string {
	if f == nil {
		return ""
	}
	return f.drain(true)
}

func (f *CitationStreamFilter) drain(eof bool) string {
	value := f.buffer.String()
	f.buffer.Reset()
	var visible strings.Builder
	for value != "" {
		open := strings.Index(value, citationOpen)
		if open >= 0 {
			visible.WriteString(value[:open])
			value = value[open+len(citationOpen):]
			close := strings.Index(value, citationClose)
			if close < 0 {
				if !eof {
					f.buffer.WriteString(citationOpen)
					f.buffer.WriteString(value)
				}
				return visible.String()
			}
			value = value[close+len(citationClose):]
			continue
		}
		if eof {
			visible.WriteString(value)
			return visible.String()
		}
		keep := longestCitationOpenPrefix(value)
		visible.WriteString(value[:len(value)-keep])
		f.buffer.WriteString(value[len(value)-keep:])
		return visible.String()
	}
	return visible.String()
}

func longestCitationOpenPrefix(value string) int {
	max := len(citationOpen) - 1
	if len(value) < max {
		max = len(value)
	}
	for n := max; n > 0; n-- {
		if strings.HasSuffix(value, citationOpen[:n]) {
			return n
		}
	}
	return 0
}

type CitationEntry struct {
	Path      string `json:"path"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
	Note      string `json:"note"`
}

type MemoryCitation struct {
	Entries    []CitationEntry `json:"entries"`
	RolloutIDs []string        `json:"rollout_ids"`
}

func StripAndParseCitations(text string) (string, *MemoryCitation) {
	var bodies []string
	stripped := citationRe.ReplaceAllStringFunc(text, func(match string) string {
		parts := citationRe.FindStringSubmatch(match)
		if len(parts) == 2 {
			bodies = append(bodies, parts[1])
		}
		return ""
	})
	if open := strings.Index(stripped, citationOpen); open >= 0 {
		bodies = append(bodies, stripped[open+len(citationOpen):])
		stripped = stripped[:open]
	}
	return stripped, ParseMemoryCitation(bodies)
}

func ParseMemoryCitation(bodies []string) *MemoryCitation {
	var citation MemoryCitation
	seen := make(map[string]struct{})
	for _, body := range bodies {
		if block, ok := extractCitationBlock(body, "<citation_entries>", "</citation_entries>"); ok {
			for _, line := range strings.Split(block, "\n") {
				if entry, ok := parseCitationEntry(line); ok {
					citation.Entries = append(citation.Entries, entry)
				}
			}
		}
		if block, ok := extractCitationBlock(body, "<rollout_ids>", "</rollout_ids>"); ok {
			appendCitationIDs(&citation, seen, block)
		} else if block, ok := extractCitationBlock(body, "<thread_ids>", "</thread_ids>"); ok {
			appendCitationIDs(&citation, seen, block)
		}
	}
	if len(citation.Entries) == 0 && len(citation.RolloutIDs) == 0 {
		return nil
	}
	return &citation
}

func appendCitationIDs(citation *MemoryCitation, seen map[string]struct{}, block string) {
	if citation == nil {
		return
	}
	for _, raw := range strings.Split(block, "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if _, exists := seen[raw]; exists {
			continue
		}
		seen[raw] = struct{}{}
		citation.RolloutIDs = append(citation.RolloutIDs, raw)
	}
}

// ValidMemoryCitationThreadIDs returns canonical UUID thread IDs suitable for
// database usage accounting. Citation metadata intentionally remains lossless;
// malformed or non-UUID IDs are ignored only at this accounting boundary.
func ValidMemoryCitationThreadIDs(citation *MemoryCitation) []string {
	if citation == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(citation.RolloutIDs))
	out := make([]string, 0, len(citation.RolloutIDs))
	for _, raw := range citation.RolloutIDs {
		parsed, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		canonical := parsed.String()
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		out = append(out, canonical)
	}
	return out
}

func parseCitationEntry(line string) (CitationEntry, bool) {
	line = strings.TrimSpace(line)
	noteIndex := strings.LastIndex(line, "|note=[")
	if noteIndex < 0 {
		return CitationEntry{}, false
	}
	location, noteWithClose := line[:noteIndex], line[noteIndex+len("|note=["):]
	if !strings.HasSuffix(noteWithClose, "]") {
		return CitationEntry{}, false
	}
	colon := strings.LastIndex(location, ":")
	if colon < 0 {
		return CitationEntry{}, false
	}
	rangeText := location[colon+1:]
	dash := strings.Index(rangeText, "-")
	if dash < 0 {
		return CitationEntry{}, false
	}
	start, errStart := strconv.Atoi(strings.TrimSpace(rangeText[:dash]))
	end, errEnd := strconv.Atoi(strings.TrimSpace(rangeText[dash+1:]))
	if errStart != nil || errEnd != nil {
		return CitationEntry{}, false
	}
	return CitationEntry{Path: strings.TrimSpace(location[:colon]), LineStart: start, LineEnd: end, Note: strings.TrimSpace(strings.TrimSuffix(noteWithClose, "]"))}, true
}

func extractCitationBlock(text, open, close string) (string, bool) {
	start := strings.Index(text, open)
	if start < 0 {
		return "", false
	}
	rest := text[start+len(open):]
	end := strings.Index(rest, close)
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}
