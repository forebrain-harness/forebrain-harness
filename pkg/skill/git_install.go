package skill

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type LockEntry struct {
	SourceType   string `json:"source_type,omitempty"`
	SourceRef    string `json:"source_ref,omitempty"`
	SkillSubpath string `json:"skill_subpath,omitempty"`
	Path         string `json:"path,omitempty"`
	Revision     string `json:"revision,omitempty"`
	// SourceDigest and InstalledDigest identify the exact bytes an install
	// copied and the exact bytes it wrote. Sources with no revision of their own
	// need them to answer two different questions: whether the source has moved
	// on since the copy, and whether the installed copy has been edited since.
	// Revision stays what it has always been — a version-control revision — so a
	// git install and a content-addressed one do not have to share one field
	// with two meanings.
	SourceDigest    string `json:"source_digest,omitempty"`
	InstalledDigest string `json:"installed_digest,omitempty"`
	InstalledAt     int64  `json:"installed_at,omitempty"`
}

func MergeLockEntryV2(workspace string, key string, e LockEntry) error {
	rec := map[string]any{
		"source_type":      e.SourceType,
		"source_ref":       e.SourceRef,
		"skill_subpath":    e.SkillSubpath,
		"path":             e.Path,
		"revision":         e.Revision,
		"source_digest":    e.SourceDigest,
		"installed_digest": e.InstalledDigest,
		"installed_at":     e.InstalledAt,
	}
	if strings.TrimSpace(e.SourceDigest) == "" {
		delete(rec, "source_digest")
	}
	if strings.TrimSpace(e.InstalledDigest) == "" {
		delete(rec, "installed_digest")
	}
	if strings.TrimSpace(e.SourceType) == "" {
		delete(rec, "source_type")
	}
	if strings.TrimSpace(e.SourceRef) == "" {
		delete(rec, "source_ref")
	}
	if strings.TrimSpace(e.SkillSubpath) == "" {
		delete(rec, "skill_subpath")
	}
	if strings.TrimSpace(e.Revision) == "" {
		delete(rec, "revision")
	}
	if e.InstalledAt == 0 {
		delete(rec, "installed_at")
	}
	return MergeLockEntry(workspace, key, rec)
}

// LookupLockEntry reads back the record MergeLockEntryV2 wrote for key. It
// reports false when no entry exists, which is how a caller tells a skill this
// workspace installed apart from one that arrived some other way.
func LookupLockEntry(workspace string, key string) (LockEntry, bool) {
	lock, err := ReadLock(strings.TrimSpace(workspace))
	if err != nil {
		return LockEntry{}, false
	}
	raw, ok := entriesMap(lock)[strings.TrimSpace(key)]
	if !ok {
		return LockEntry{}, false
	}
	rec, ok := raw.(map[string]any)
	if !ok {
		return LockEntry{}, false
	}
	text := func(field string) string {
		value, _ := rec[field].(string)
		return strings.TrimSpace(value)
	}
	entry := LockEntry{
		SourceType:      text("source_type"),
		SourceRef:       text("source_ref"),
		SkillSubpath:    text("skill_subpath"),
		Path:            text("path"),
		Revision:        text("revision"),
		SourceDigest:    text("source_digest"),
		InstalledDigest: text("installed_digest"),
	}
	// JSON numbers decode as float64, so installed_at arrives as one.
	if seconds, ok := rec["installed_at"].(float64); ok {
		entry.InstalledAt = int64(seconds)
	}
	return entry, true
}

// gitOut runs one git command under ctx. Installs run on a background
// goroutine while the surface stays interactive, so a clone must die with the
// context that owns it rather than outliving the session that started it.
func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		// git's own words, unwrapped and unedited: which failure this was —
		// a private repository, a proxy, DNS, a typo in the reference — is
		// only ever visible in what git printed, so that is what the
		// surfaces show.
		return "", fmt.Errorf("git %v: %s", args, msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func ensureGitAvailable() error {
	if _, err := exec.LookPath("git"); err != nil {
		return ErrGitMissing
	}
	return nil
}

func InstallFromGitRecord(ctx context.Context, workspace, repo, destSkillsDir, name, ref string) error {
	if err := ensureGitAvailable(); err != nil {
		return err
	}
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return fmt.Errorf("empty repo")
	}
	if name == "" {
		base := filepath.Base(strings.TrimSuffix(repo, ".git"))
		name = base
	}
	dst := filepath.Join(destSkillsDir, name)
	if err := os.MkdirAll(destSkillsDir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		// Existing: best-effort fast-forward pull if it's a git repo.
		if _, err := os.Stat(filepath.Join(dst, ".git")); err == nil {
			_, _ = gitOut(ctx, dst, "fetch", "--all", "--prune")
			if strings.TrimSpace(ref) != "" {
				_, _ = gitOut(ctx, dst, "checkout", ref)
			}
			_, _ = gitOut(ctx, dst, "pull", "--ff-only")
		} else {
			return fmt.Errorf("%s already exists and is not a git checkout", dst)
		}
	} else {
		if _, err := gitOut(ctx, destSkillsDir, "clone", "--depth", "1", repo, name); err != nil {
			return err
		}
		if strings.TrimSpace(ref) != "" {
			_, _ = gitOut(ctx, dst, "checkout", ref)
		}
	}
	rev, _ := gitOut(ctx, dst, "rev-parse", "HEAD")
	if workspace != "" {
		_ = MergeLockEntryV2(workspace, name, LockEntry{
			SourceType:  "git_repo",
			SourceRef:   repo,
			Path:        dst,
			Revision:    rev,
			InstalledAt: time.Now().Unix(),
		})
	}
	return nil
}

func InstallFromSkillsShRecord(ctx context.Context, workspace, ownerRepo, destSkillsDir, name, ref string) error {
	ownerRepo = strings.TrimSpace(ownerRepo)
	if ownerRepo == "" {
		return fmt.Errorf("empty skills.sh entry")
	}
	repo := ownerRepo
	if !strings.Contains(repo, "://") {
		repo = "https://github.com/" + strings.TrimPrefix(ownerRepo, "/")
		if !strings.HasSuffix(repo, ".git") {
			repo += ".git"
		}
	}
	if name == "" {
		parts := strings.Split(strings.Trim(ownerRepo, "/"), "/")
		if len(parts) >= 2 {
			name = parts[len(parts)-1]
		}
	}
	return InstallFromGitRecord(ctx, workspace, repo, destSkillsDir, name, ref)
}

// remoteHeadTTL is how long one upstream probe's answer stands. The check
// runs on list interfaces, so an hour keeps a listing cheap while still
// noticing an upstream that moved on.
const remoteHeadTTL = time.Hour

// remoteHeadEntry is one cached upstream probe.
type remoteHeadEntry struct {
	at  time.Time
	rev string
	ok  bool
}

// remoteHeadCache memoizes upstream probes per repository URL, in this
// process, for remoteHeadTTL. A cache entry is only ever a hint about a
// remote, so a stale or racing entry marks an update that may already have
// been pulled, and costs one extra probe an hour later — never a wrong write.
var remoteHeadCache = struct {
	sync.Mutex
	m map[string]remoteHeadEntry
}{m: map[string]remoteHeadEntry{}}

// RemoteHead reports the newest revision a git source's default branch
// advertises, without cloning it. The answer is cached per repository for an
// hour; only a fresh-enough cache hit avoids the probe.
//
// ok=false is a failed probe — the source is unreachable, private or gone —
// and the caller answers "no update" rather than an error: the listing that
// triggered the check must not fail because an upstream is offline.
func RemoteHead(ctx context.Context, repo string) (string, bool) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", false
	}
	now := time.Now()
	remoteHeadCache.Lock()
	if entry, ok := remoteHeadCache.m[repo]; ok && now.Sub(entry.at) < remoteHeadTTL {
		remoteHeadCache.Unlock()
		return entry.rev, entry.ok
	}
	remoteHeadCache.Unlock()
	rev, ok := "", false
	if out, err := gitOut(ctx, "", "ls-remote", repo, "HEAD"); err == nil {
		if fields := strings.Fields(out); len(fields) > 0 {
			rev, ok = fields[0], true
		}
	}
	remoteHeadCache.Lock()
	remoteHeadCache.m[repo] = remoteHeadEntry{at: now, rev: rev, ok: ok}
	remoteHeadCache.Unlock()
	return rev, ok
}

// ForgetRemoteHeadCache drops the cached upstream probes, so a test or an
// explicit re-check can force a fresh probe.
func ForgetRemoteHeadCache() {
	remoteHeadCache.Lock()
	remoteHeadCache.m = map[string]remoteHeadEntry{}
	remoteHeadCache.Unlock()
}
