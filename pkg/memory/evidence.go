package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

type rolloutEvidenceRecord struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

type rolloutEvidenceMetadata struct {
	ID        string `json:"id"`
	Cwd       string `json:"cwd,omitempty"`
	GitBranch string `json:"git_branch,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
}

func writeRolloutEvidenceTo(path string, thread SessionCandidate, messages []llm.Message) error {
	var body strings.Builder
	if err := appendEvidenceRecord(&body, rolloutEvidenceRecord{
		Type: "session_meta",
		Payload: rolloutEvidenceMetadata{
			ID:        thread.ThreadID,
			Cwd:       thread.Cwd,
			GitBranch: thread.GitBranch,
			UpdatedAt: thread.UpdatedAt,
		},
	}); err != nil {
		return err
	}
	for _, message := range messages {
		if err := appendEvidenceRecord(&body, rolloutEvidenceRecord{Type: "message", Payload: message}); err != nil {
			return err
		}
	}
	if err := writePrivateFileAtomic(path, body.String()); err != nil {
		return fmt.Errorf("write rollout evidence: %w", err)
	}
	return nil
}

func stageRolloutEvidence(root Root, thread SessionCandidate, ownershipToken string, messages []llm.Message) (string, string, error) {
	canonical := root.rolloutEvidencePath(thread.ThreadID, thread.UpdatedAt)
	dir := root.rolloutEvidenceRoot()
	if err := ensurePrivateEvidenceDirectory(dir); err != nil {
		return "", "", fmt.Errorf("prepare rollout evidence: %w", err)
	}
	token := rolloutEvidenceID(strings.TrimSpace(ownershipToken))
	staged := filepath.Join(dir, fmt.Sprintf(".%s-%s-%d.staged", filepath.Base(canonical), token, time.Now().UnixNano()))
	if err := writeRolloutEvidenceTo(staged, thread, messages); err != nil {
		return "", "", err
	}
	return staged, canonical, nil
}

func publishStagedRolloutEvidence(staged, canonical string) (restore func() error, finalize func(), err error) {
	staged = filepath.Clean(staged)
	canonical = filepath.Clean(canonical)
	if staged == "." || canonical == "." || filepath.Dir(staged) != filepath.Dir(canonical) {
		return nil, nil, fmt.Errorf("invalid rollout evidence publication paths")
	}
	backup := ""
	if _, statErr := os.Lstat(canonical); statErr == nil {
		backup = canonical + fmt.Sprintf(".%d.backup", time.Now().UnixNano())
		if err := os.Rename(canonical, backup); err != nil {
			return nil, nil, fmt.Errorf("backup rollout evidence: %w", err)
		}
	} else if !os.IsNotExist(statErr) {
		return nil, nil, fmt.Errorf("inspect rollout evidence: %w", statErr)
	}
	if err := os.Rename(staged, canonical); err != nil {
		if backup != "" {
			_ = os.Rename(backup, canonical)
		}
		return nil, nil, fmt.Errorf("publish rollout evidence: %w", err)
	}
	restore = func() error {
		if err := os.Remove(canonical); err != nil && !os.IsNotExist(err) {
			return err
		}
		if backup != "" {
			return os.Rename(backup, canonical)
		}
		return nil
	}
	finalize = func() {
		if backup != "" {
			_ = os.Remove(backup)
		}
	}
	return restore, finalize, nil
}

func ensurePrivateEvidenceDirectory(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("rollout evidence root must be a directory and not a symlink")
	}
	return os.Chmod(dir, 0o700)
}

func appendEvidenceRecord(body *strings.Builder, record rolloutEvidenceRecord) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode rollout evidence: %w", err)
	}
	body.Write(encoded)
	body.WriteByte('\n')
	return nil
}

func writePrivateFileAtomic(path, body string) error {
	dir := filepath.Dir(path)
	if err := ensurePrivateEvidenceDirectory(dir); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".rollout-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
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

func removeThreadEvidence(root Root, threadID string, keepPath string) {
	digest := rolloutEvidenceID(threadID)
	entries, err := os.ReadDir(root.rolloutEvidenceRoot())
	if err != nil {
		return
	}
	keepPath = filepath.Clean(keepPath)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), digest+"-") || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		path := filepath.Join(root.rolloutEvidenceRoot(), entry.Name())
		if keepPath != "." && filepath.Clean(path) == keepPath {
			continue
		}
		_ = os.Remove(path)
	}
}

// clearScopeState removes everything this scope keeps outside its memory
// folder: its rollout evidence and its consolidation watermark. Both describe
// memory that Clear is deleting, so leaving either behind would outlive the
// thing it describes — a watermark in particular claims "everything written
// before T is already consolidated" about a folder that no longer has any
// consolidated content at all.
func clearScopeState(root Root) error {
	if err := os.RemoveAll(root.stateScopeRoot()); err != nil {
		return fmt.Errorf("clear scope state: %w", err)
	}
	return nil
}

// pruneRolloutEvidence removes stale evidence files across every scope this
// agent has state for. versions spans every project (ListStage1Versions is
// agent-wide, not scope-filtered), so it is grouped by project first and each
// project's evidence directory is pruned against only its own versions —
// otherwise a project with zero surviving stage-1 rows would keep none of its
// own evidence live in the keep set while another project's paths (which never
// exist under its directory) would be checked against it for nothing.
func pruneRolloutEvidence(roots Roots, versions []Stage1Version) {
	byProject := make(map[string][]Stage1Version, len(versions))
	for _, version := range versions {
		key := strings.TrimSpace(version.ProjectKey)
		byProject[key] = append(byProject[key], version)
	}
	entries, err := os.ReadDir(roots.stateBase())
	if err != nil {
		return
	}
	globalSegment := GlobalScope().stateSegment()
	for _, entry := range entries {
		// The state root holds one directory per scope, so the global scope's
		// own directory sits among the project ones. It is not a project and
		// has no stage-1 versions to keep, so reading it as a project key
		// would prune it against an empty keep set — deleting whatever it
		// holds. Rollout evidence is project-only by construction, so the
		// global directory is simply not this function's business.
		if !entry.IsDir() || entry.Name() == globalSegment {
			continue
		}
		scope, ok := ProjectScope(entry.Name())
		if !ok {
			continue
		}
		pruneScopeRolloutEvidence(roots.Scope(scope), byProject[scope.Key])
	}
}

func pruneScopeRolloutEvidence(root Root, versions []Stage1Version) {
	keep := make(map[string]struct{}, len(versions))
	for _, version := range versions {
		keep[filepath.Clean(root.rolloutEvidencePath(version.ThreadID, version.SourceUpdatedAt))] = struct{}{}
	}
	entries, err := os.ReadDir(root.rolloutEvidenceRoot())
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		path := filepath.Join(root.rolloutEvidenceRoot(), entry.Name())
		if _, ok := keep[filepath.Clean(path)]; ok {
			continue
		}
		_ = os.Remove(path)
	}
}
