package assembly

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

type WorkingSetSource struct{}

func (s WorkingSetSource) ID() string { return "working_set_source" }

func (s WorkingSetSource) Collect(req AssemblyRequest) ([]ContextItem, error) {
	files := normalizedSet(extractAtRefs(req.Query))
	for _, x := range req.PinnedSet {
		files = append(files, x)
	}
	files = normalizedSet(files)
	if len(files) == 0 {
		return nil, nil
	}
	show := files
	if len(show) > 16 {
		show = show[:16]
	}
	return []ContextItem{
		{
			SourceID:    s.ID(),
			Layer:       LayerWorking,
			Title:       "Task Working Set",
			Content:     strings.Join(show, "\n"),
			Priority:    92,
			Pinned:      len(req.PinnedSet) > 0,
			DebugReason: "refs + pins",
		},
	}, nil
}

type RecentFilesSource struct {
	ReadStatesProvider func() []tool.ReadState
}

func (s RecentFilesSource) ID() string { return "recent_files_source" }

func (s RecentFilesSource) Collect(req AssemblyRequest) ([]ContextItem, error) {
	if s.ReadStatesProvider == nil {
		return nil, nil
	}
	states := s.ReadStatesProvider()
	if len(states) == 0 {
		return nil, nil
	}
	lines := make([]string, 0, min(len(states), 12))
	for i, st := range states {
		if i >= 12 {
			break
		}
		path := filepath.ToSlash(strings.TrimSpace(st.AbsPath))
		if path == "" {
			continue
		}
		kind := strings.TrimSpace(st.TouchKind)
		if kind == "" {
			kind = "read"
		}
		lines = append(lines, kind+": "+path)
	}
	if len(lines) == 0 {
		return nil, nil
	}
	return []ContextItem{{
		SourceID:    s.ID(),
		Layer:       LayerWorking,
		Title:       "Recent File Activity",
		Content:     strings.Join(lines, "\n"),
		Priority:    88,
		DebugReason: "recent read state",
	}}, nil
}

var atRefRe = regexp.MustCompile(`@([A-Za-z0-9_./\\-]+)`)

func extractAtRefs(s string) []string {
	matches := atRefRe.FindAllStringSubmatch(s, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		v := strings.TrimSpace(m[1])
		if v == "" {
			continue
		}
		out = append(out, v)
	}
	return normalizedSet(out)
}

type GitContextSource struct{}

func (GitContextSource) ID() string { return "git_context_source" }

func (GitContextSource) Collect(req AssemblyRequest) ([]ContextItem, error) {
	if strings.EqualFold(strings.TrimSpace(req.Mode), "plan") {
		return nil, nil
	}
	wd, err := os.Getwd()
	if err != nil || strings.TrimSpace(wd) == "" {
		return nil, nil
	}
	// Shares one git collection with the prompt-side hook rather than running
	// its own status/diff pair; only the summarization below differs.
	state := Collect(context.Background(), wd)
	if state.Empty() {
		return nil, nil
	}
	statusLines := nonEmptyLines(state.Status)
	diffLines := nonEmptyLines(state.DiffStat)
	var parts []string
	if len(statusLines) > 0 {
		parts = append(parts, fmt.Sprintf("dirty_files: %d", len(statusLines)))
		if hot := summarizeGitHotPaths(statusLines, 3); hot != "" {
			parts = append(parts, "hot_paths: "+hot)
		}
	}
	if len(diffLines) > 0 {
		if lead := summarizeGitLargestDiffs(diffLines, 3); lead != "" {
			parts = append(parts, "largest_diffs: "+lead)
		}
		if tail := strings.TrimSpace(diffLines[len(diffLines)-1]); tail != "" && strings.Contains(tail, "file") {
			parts = append(parts, "diff_totals: "+tail)
		}
	}
	if len(parts) == 0 {
		return nil, nil
	}
	return []ContextItem{
		{
			SourceID:    "git_context_source",
			Layer:       LayerWorkspace,
			Title:       "Git Snapshot",
			Content:     strings.Join(parts, "\n"),
			Priority:    62,
			DebugReason: "git status and diff stat",
		},
	}, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func nonEmptyLines(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	rows := strings.Split(s, "\n")
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		row = strings.TrimSpace(row)
		if row == "" {
			continue
		}
		out = append(out, row)
	}
	return out
}

func summarizeGitHotPaths(statusLines []string, limit int) string {
	counts := make(map[string]int)
	for _, line := range statusLines {
		path := strings.TrimSpace(line)
		if len(path) > 3 {
			path = strings.TrimSpace(path[3:])
		}
		if idx := strings.Index(path, " -> "); idx >= 0 {
			path = strings.TrimSpace(path[idx+4:])
		}
		if path == "" {
			continue
		}
		key := gitPathBucket(path)
		if key == "" {
			key = path
		}
		counts[key]++
	}
	return formatTopCounts(counts, limit)
}

func summarizeGitLargestDiffs(diffLines []string, limit int) string {
	type entry struct {
		label string
		score int
	}
	var entries []entry
	for _, line := range diffLines {
		if !strings.Contains(line, "|") {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		label := strings.TrimSpace(parts[0])
		if label == "" {
			continue
		}
		score := strings.Count(parts[1], "+") + strings.Count(parts[1], "-")
		if score <= 0 {
			score = len(strings.Fields(parts[1]))
		}
		entries = append(entries, entry{label: label, score: score})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].score != entries[j].score {
			return entries[i].score > entries[j].score
		}
		return entries[i].label < entries[j].label
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	out := make([]string, 0, len(entries))
	for _, item := range entries {
		out = append(out, item.label)
	}
	return strings.Join(out, ", ")
}

func gitPathBucket(path string) string {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" {
		return ""
	}
	parts := strings.Split(path, "/")
	switch {
	case len(parts) >= 2:
		return parts[0] + "/" + parts[1]
	case len(parts) == 1:
		return parts[0]
	default:
		return ""
	}
}

func formatTopCounts(counts map[string]int, limit int) string {
	type entry struct {
		label string
		count int
	}
	if len(counts) == 0 {
		return ""
	}
	rows := make([]entry, 0, len(counts))
	for label, count := range counts {
		if strings.TrimSpace(label) == "" || count <= 0 {
			continue
		}
		rows = append(rows, entry{label: label, count: count})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].count != rows[j].count {
			return rows[i].count > rows[j].count
		}
		return rows[i].label < rows[j].label
	})
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.count > 1 {
			out = append(out, fmt.Sprintf("%s(%d)", row.label, row.count))
			continue
		}
		out = append(out, row.label)
	}
	return strings.Join(out, ", ")
}
