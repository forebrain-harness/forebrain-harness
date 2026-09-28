package memory

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	rolloutSummariesDir = "rollout_summaries"
	extensionsDir       = "extensions"
	// globalCandidatesFileName is the file a project's own consolidation pass
	// declares its cross-project preference candidates into (see the
	// "global_candidates.md FORMAT" section of the project consolidation
	// prompt). It is rewritten fresh each pass, never appended to.
	globalCandidatesFileName = "global_candidates.md"
	// promotionCandidatesFileName is the global scope's generated input:
	// every project's own globalCandidatesFileName, gathered together.
	promotionCandidatesFileName = "promotion_candidates.md"
)

func syncPhase2WorkspaceInputs(root string, rows []Stage1Output) error {
	if err := os.MkdirAll(filepath.Join(root, rolloutSummariesDir), 0o755); err != nil {
		return err
	}
	if err := syncRolloutSummaries(root, rows); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(root, "raw_memories.md"), renderRawMemories(rows)); err != nil {
		return err
	}
	pruneOldExtensionResources(root, time.Now().UTC())
	return nil
}

// syncGlobalPhase2Inputs rebuilds the global scope's generated input from
// every project scope's own global_candidates.md, gathered under one project
// heading each so the global consolidation agent can weigh corroboration
// across projects. It is declarative like its per-project source: rebuilt
// from scratch each pass, not appended to, so a project that stops asserting
// a candidate (or is deleted outright) stops contributing it here too.
// It reports whether any project actually asserted a candidate, so a pass with
// nothing to promote can be skipped rather than asked to consolidate an empty
// list.
func syncGlobalPhase2Inputs(roots Roots, globalRoot string) (bool, error) {
	scopes, err := roots.ListProjectScopes()
	if err != nil {
		return false, err
	}
	sort.Slice(scopes, func(i, j int) bool { return scopes[i].Key < scopes[j].Key })
	var body strings.Builder
	body.WriteString("# Promotion Candidates\n\n")
	body.WriteString("Generated input for global consolidation: every project's own global_candidates.md.\n")
	found := false
	for _, scope := range scopes {
		path := filepath.Join(roots.Scope(scope).MemoryRoot, globalCandidatesFileName)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false, err
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		found = true
		fmt.Fprintf(&body, "\n## Project `%s`\n\n%s\n", scope.Key, content)
	}
	if !found {
		body.WriteString("\nNo project currently asserts a cross-project candidate.\n")
	}
	if err := writeFileAtomic(filepath.Join(globalRoot, promotionCandidatesFileName), body.String()); err != nil {
		return false, err
	}
	pruneOldExtensionResources(globalRoot, time.Now().UTC())
	return found, nil
}

// hasExtensionInput reports whether any extension folder holds real input — an
// ad-hoc note or an extension resource. The instructions.md files Forebrain Harness seeds
// into every extension folder do not count: they describe how to read a memory
// source rather than being memory themselves, and treating them as input would
// make every scope look like it had something to consolidate from the moment it
// was created.
func hasExtensionInput(memoryRoot string) bool {
	found := false
	_ = filepath.WalkDir(filepath.Join(memoryRoot, extensionsDir), func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Name() == "instructions.md" {
			return nil
		}
		found = true
		return filepath.SkipAll
	})
	return found
}

// consolidationArtifactsExist reports whether this scope has already-stored
// memory. It only asks whether the files are there, not whether they are
// well-formed: a scope holding a malformed summary still needs a pass to
// repair it, while a scope holding nothing needs one only once it has input.
func consolidationArtifactsExist(memoryRoot string) bool {
	for _, name := range []string{"MEMORY.md", "memory_summary.md"} {
		if info, err := os.Stat(filepath.Join(memoryRoot, name)); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

func renderRawMemories(rows []Stage1Output) string {
	rows = append([]Stage1Output(nil), rows...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].ThreadID < rows[j].ThreadID })
	var body strings.Builder
	body.WriteString("# Raw Memories\n\n")
	if len(rows) == 0 {
		body.WriteString("No raw memories yet.\n")
		return body.String()
	}
	body.WriteString("Merged stage-1 raw memories (stable ascending thread-id order):\n\n")
	for _, row := range rows {
		fmt.Fprintf(&body, "## Thread `%s`\n", row.ThreadID)
		fmt.Fprintf(&body, "updated_at: %s\n", formatUTCTimestamp(row.SourceUpdatedAt))
		fmt.Fprintf(&body, "cwd: %s\n", row.Cwd)
		fmt.Fprintf(&body, "rollout_path: %s\n", row.RolloutPath)
		fmt.Fprintf(&body, "rollout_summary_file: %s.md\n\n", rolloutSummaryFileStem(row))
		body.WriteString(strings.TrimSpace(row.RawMemory))
		body.WriteString("\n\n")
	}
	return body.String()
}

func syncRolloutSummaries(root string, rows []Stage1Output) error {
	dir := filepath.Join(root, rolloutSummariesDir)
	keep := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		keep[rolloutSummaryFileStem(row)] = struct{}{}
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		if _, ok := keep[strings.TrimSuffix(entry.Name(), ".md")]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	for _, row := range rows {
		var body strings.Builder
		fmt.Fprintf(&body, "thread_id: %s\n", row.ThreadID)
		fmt.Fprintf(&body, "updated_at: %s\n", formatUTCTimestamp(row.SourceUpdatedAt))
		fmt.Fprintf(&body, "rollout_path: %s\n", row.RolloutPath)
		fmt.Fprintf(&body, "cwd: %s\n", row.Cwd)
		if strings.TrimSpace(row.GitBranch) != "" {
			fmt.Fprintf(&body, "git_branch: %s\n", row.GitBranch)
		}
		body.WriteByte('\n')
		body.WriteString(row.RolloutSummary)
		body.WriteByte('\n')
		if err := writeFileAtomic(filepath.Join(dir, rolloutSummaryFileStem(row)+".md"), body.String()); err != nil {
			return err
		}
	}
	return nil
}

func formatUTCTimestamp(unixSeconds int64) string {
	return time.Unix(unixSeconds, 0).UTC().Format("2006-01-02T15:04:05+00:00")
}

func rolloutSummaryFileStem(row Stage1Output) string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	const hashSpace uint32 = 14_776_336
	timestamp := time.Unix(row.SourceUpdatedAt, 0).UTC()
	var seed uint32
	if parsed, err := uuid.Parse(row.ThreadID); err == nil {
		if parsed.Version() == 1 || parsed.Version() == 6 || parsed.Version() == 7 {
			seconds, nanos := parsed.Time().UnixTime()
			timestamp = time.Unix(seconds, int64(nanos)).UTC()
		}
		bytes := parsed[:]
		seed = uint32(bytes[12])<<24 | uint32(bytes[13])<<16 | uint32(bytes[14])<<8 | uint32(bytes[15])
	} else {
		for _, value := range []byte(row.ThreadID) {
			seed = seed*31 + uint32(value)
		}
	}
	value := seed % hashSpace
	hash := [4]byte{}
	for index := len(hash) - 1; index >= 0; index-- {
		hash[index] = alphabet[value%62]
		value /= 62
	}
	prefix := timestamp.Format("2006-01-02T15-04-05") + "-" + string(hash[:])
	if !row.RolloutSlug.Valid {
		return prefix
	}
	var slug strings.Builder
	for _, char := range row.RolloutSlug.String {
		if slug.Len() >= 60 {
			break
		}
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') {
			slug.WriteRune(char)
		} else {
			slug.WriteByte('_')
		}
	}
	cleaned := strings.TrimRight(strings.ToLower(slug.String()), "_")
	if cleaned == "" {
		return prefix
	}
	return prefix + "-" + cleaned
}

func pruneOldExtensionResources(root string, now time.Time) {
	extensions, err := os.ReadDir(filepath.Join(root, extensionsDir))
	if err != nil {
		return
	}
	cutoff := now.Add(-7 * 24 * time.Hour)
	for _, extension := range extensions {
		base := filepath.Join(root, extensionsDir, extension.Name())
		if !extension.IsDir() {
			continue
		}
		if info, err := os.Stat(filepath.Join(base, "instructions.md")); err != nil || !info.Mode().IsRegular() {
			continue
		}
		resources, err := os.ReadDir(filepath.Join(base, "resources"))
		if err != nil {
			continue
		}
		for _, resource := range resources {
			name := resource.Name()
			if resource.IsDir() || filepath.Ext(name) != ".md" || len(name) < 19 {
				continue
			}
			timestamp, err := time.ParseInLocation("2006-01-02T15-04-05", name[:19], time.UTC)
			if err == nil && !timestamp.After(cutoff) {
				_ = os.Remove(filepath.Join(base, "resources", name))
			}
		}
	}
}

// markConsolidationStart records the instant a consolidation pass snapshotted
// its inputs. Notes written after it were not in that snapshot, so they stay
// pending until a later pass.
func markConsolidationStart(root Root, at time.Time) error {
	return writeFileAtomic(root.consolidationMarkerPath(), at.UTC().Format(time.RFC3339Nano)+"\n")
}

// lastConsolidationStart reports the instant recorded by markConsolidationStart,
// or the zero time when no pass has run — which makes every note pending, the
// correct answer for a store nothing has consolidated yet.
func lastConsolidationStart(root Root) time.Time {
	body, err := os.ReadFile(root.consolidationMarkerPath())
	if err != nil {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(body)))
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func writeFileAtomic(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".memory-write-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o644); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.WriteString(body); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
