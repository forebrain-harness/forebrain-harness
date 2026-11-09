package assembly

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

func resolve(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("eval symlinks %q: %v", p, err)
	}
	return filepath.Clean(r)
}

func TestProjectRoot(t *testing.T) {
	t.Parallel()

	// (a) finds the nearest .git ancestor.
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := ProjectRoot(deep), resolve(t, repo); got != want {
		t.Fatalf("ProjectRoot(deep) = %q, want git root %q", got, want)
	}

	// (b) no .git anywhere up the tree → returns the cleaned CWD itself.
	plain := t.TempDir()
	if got, want := ProjectRoot(plain), resolve(t, plain); got != want {
		t.Fatalf("ProjectRoot(plain) = %q, want %q", got, want)
	}
}

func TestProjectForebrainFilesRootFirst(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "FOREBRAIN.md"), []byte("ROOT RULES"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "FOREBRAIN.md"), []byte("PKG RULES"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := ProjectForebrainFiles(sub, true, 4096)
	if len(files) != 2 {
		t.Fatalf("chain: got %d files, want 2", len(files))
	}
	if files[0].Body != "ROOT RULES" || files[1].Body != "PKG RULES" {
		t.Fatalf("chain order wrong: %+v", files)
	}

	// Without chaining, only the project (git) root's FOREBRAIN.md is read.
	noChain := ProjectForebrainFiles(sub, false, 4096)
	if len(noChain) != 1 || noChain[0].Body != "ROOT RULES" {
		t.Fatalf("no-chain: got %+v, want only ROOT RULES", noChain)
	}
}

func TestLoadRulesMergesBootstrapAndForebrain(t *testing.T) {
	home := t.TempDir()
	wsRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(wsRoot, "AGENTS.md"), []byte("AGENT GUIDANCE"), 0o644); err != nil {
		t.Fatal(err)
	}

	// CWD project root (git repo) with a FOREBRAIN.md and a legacy .forebrain/rules file.
	proj := t.TempDir()
	if err := os.Mkdir(filepath.Join(proj, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "FOREBRAIN.md"), []byte("PROJECT RULES"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacyDir := filepath.Join(proj, ".forebrain", "rules")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "old.md"), []byte("LEGACY DOTRULES"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := safety.MarkTrusted(home, safety.Project{Root: proj, VersionControlled: true}); err != nil {
		t.Fatal(err)
	}
	p := NewPreHook(nil, home, wsRoot)
	out := p.loadRules(proj, hook.HookContext{}, true)

	if !strings.Contains(out, "AGENT GUIDANCE") {
		t.Errorf("missing active-agent bootstrap content:\n%s", out)
	}
	if !strings.Contains(out, "PROJECT RULES") {
		t.Errorf("missing CWD FOREBRAIN.md content:\n%s", out)
	}
	if strings.Contains(out, "LEGACY DOTRULES") {
		t.Errorf(".forebrain/rules must not be read:\n%s", out)
	}
	// Active-agent bootstrap is merged before the CWD FOREBRAIN.md.
	if i, j := strings.Index(out, "AGENT GUIDANCE"), strings.Index(out, "PROJECT RULES"); i < 0 || j < 0 || i > j {
		t.Errorf("expected bootstrap before FOREBRAIN.md, got order i=%d j=%d:\n%s", i, j, out)
	}
}

// TestInstructionSourcesMatchMergedBody guards R-cache: recording where the
// rules body comes from must not change the merged bytes the hook injects.
// The golden body here is asserted against the exact bytes the pre-recording
// implementation produced, and the source list is checked to describe the
// same build.
func TestInstructionSourcesMatchMergedBody(t *testing.T) {
	home := t.TempDir()
	wsRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(wsRoot, "AGENTS.md"), []byte("AGENT GUIDANCE"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	if err := os.Mkdir(filepath.Join(proj, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "FOREBRAIN.md"), []byte("PROJECT RULES"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := safety.MarkTrusted(home, safety.Project{Root: proj, VersionControlled: true}); err != nil {
		t.Fatal(err)
	}
	p := NewPreHook(nil, home, wsRoot)
	// The report resolves the working directory exactly as the hook does.
	t.Chdir(proj)
	merged := p.loadRules(proj, hook.HookContext{}, true)
	golden := "## AGENTS.md\n\nAGENT GUIDANCE\n\nPROJECT RULES"
	if merged != golden {
		t.Fatalf("merged body drifted:\n got  %q\n want %q", merged, golden)
	}
	sources := p.InstructionSources()
	if len(sources) != 2 {
		t.Fatalf("sources = %+v, want the agent bootstrap and the project FOREBRAIN.md", sources)
	}
	if sources[0].Name != "AGENTS.md" || !sources[0].Agent || !sources[0].Loaded || sources[0].TruncatedTo != 0 {
		t.Fatalf("bootstrap source = %+v", sources[0])
	}
	if sources[1].Name != "FOREBRAIN.md" || sources[1].Agent || !sources[1].Loaded || sources[1].TruncatedTo != 0 {
		t.Fatalf("project source = %+v", sources[1])
	}
}

// TestInstructionSourcesRecordWhereTheBodyLimitCuts pins the truncation facts:
// the per-file budget and the whole body's limit are both recorded as the runes
// the file actually kept, and a file that starts past the body limit is
// reported as not loaded — all without moving a byte of the merged body.
func TestInstructionSourcesRecordWhereTheBodyLimitCuts(t *testing.T) {
	home := t.TempDir()
	wsRoot := t.TempDir()
	agents := strings.Repeat("a", 3000) // over the 2048-rune per-file floor
	if err := os.WriteFile(filepath.Join(wsRoot, "AGENTS.md"), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsRoot, "SOUL.md"), []byte("SOUL"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	if err := os.Mkdir(filepath.Join(proj, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "FOREBRAIN.md"), []byte("PROJECT RULES"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := safety.MarkTrusted(home, safety.Project{Root: proj, VersionControlled: true}); err != nil {
		t.Fatal(err)
	}
	cfg := &appcfg.Root{}
	cfg.Agents.Defaults.ContextInject.MaxPreHookRulesChars = 2080
	p := NewPreHook(cfg, home, wsRoot)
	t.Chdir(proj)
	merged := p.loadRules(proj, hook.HookContext{}, true)
	sources := p.InstructionSources()
	if len(sources) != 3 {
		t.Fatalf("sources = %+v", sources)
	}
	if sources[0].TruncatedTo != 2048 || !sources[0].Loaded {
		t.Fatalf("AGENTS.md = %+v, want the per-file budget's 2048 runes kept", sources[0])
	}
	if sources[1].TruncatedTo != 2 || !sources[1].Loaded {
		t.Fatalf("SOUL.md = %+v, want the body limit keeping 2 runes", sources[1])
	}
	if sources[2].Loaded || !strings.Contains(sources[2].NotLoadedReason, "rules limit") {
		t.Fatalf("FOREBRAIN.md = %+v, want not loaded past the body limit", sources[2])
	}
	if strings.Contains(merged, "PROJECT RULES") {
		t.Fatalf("body past the limit leaked: %q", merged)
	}
	if !strings.HasSuffix(merged, "SO\n…") {
		t.Fatalf("merged body = %q, want the cut inside SOUL.md", merged)
	}
}

// TestInstructionSourcesUntrustedRecordsNotLoaded pins the /status semantic: a
// project FOREBRAIN.md that exists but belongs to an untrusted project shows up
// as present-but-not-loaded, never as absent.
func TestInstructionSourcesUntrustedRecordsNotLoaded(t *testing.T) {
	home := t.TempDir()
	wsRoot := t.TempDir()
	proj := t.TempDir()
	if err := os.Mkdir(filepath.Join(proj, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "FOREBRAIN.md"), []byte("PROJECT RULES"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := NewPreHook(nil, home, wsRoot)
	t.Chdir(proj)
	sources := p.InstructionSources()
	if len(sources) != 1 || sources[0].Name != "FOREBRAIN.md" || sources[0].Loaded || sources[0].NotLoadedReason != "project not trusted" {
		t.Fatalf("sources = %+v, want an unloaded FOREBRAIN.md", sources)
	}
	if merged := p.loadRules(proj, hook.HookContext{}, true); strings.Contains(merged, "PROJECT RULES") {
		t.Fatalf("untrusted body leaked: %q", merged)
	}
}

func TestLoadRulesUntrustedKeepsBootstrapAndOmitsProject(t *testing.T) {
	home := t.TempDir()
	wsRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(wsRoot, "AGENTS.md"), []byte("AGENT GUIDANCE"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	if err := os.Mkdir(filepath.Join(proj, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "FOREBRAIN.md"), []byte("PROJECT RULES"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := NewPreHook(nil, home, wsRoot).loadRules(proj, hook.HookContext{}, true)
	if !strings.Contains(out, "AGENT GUIDANCE") || strings.Contains(out, "PROJECT RULES") {
		t.Fatalf("untrusted rules = %q", out)
	}
}

func TestTruncateString(t *testing.T) {
	t.Parallel()
	if got := TruncateString("abcdef", 3); got != "abc\n…" {
		t.Fatalf("got %q", got)
	}
	if got := TruncateString("abcdef", 0); got != "abcdef" {
		t.Fatalf("0 must mean no limit, got %q", got)
	}
	if got := TruncateString("中文规则", 2); got != "中文\n…" {
		t.Fatalf("rune cut = %q", got)
	}
	if got := TruncateString("中文规则", 4); got != "中文规则" {
		t.Fatalf("exact fit = %q", got)
	}
}

// TestRulesPerFileBudgetCountsRunes pins that a CJK file under 12000 runes is
// injected whole even though it is far over 12000 bytes.
func TestRulesPerFileBudgetCountsRunes(t *testing.T) {
	home := t.TempDir()
	wsRoot := t.TempDir()
	proj := t.TempDir()
	if err := os.Mkdir(filepath.Join(proj, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("中文规则。", 2000) // 10000 runes, 30000 bytes
	if err := os.WriteFile(filepath.Join(proj, "FOREBRAIN.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := safety.MarkTrusted(home, safety.Project{Root: proj, VersionControlled: true}); err != nil {
		t.Fatal(err)
	}
	p := NewPreHook(nil, home, wsRoot)
	t.Chdir(proj)
	if merged := p.loadRules(proj, hook.HookContext{}, true); merged != body {
		t.Fatalf("merged body drifted: %d runes, want the whole 10000-rune file", utf8.RuneCountInString(merged))
	}
	sources := p.InstructionSources()
	if len(sources) != 1 || sources[0].TruncatedTo != 0 || !sources[0].Loaded {
		t.Fatalf("sources = %+v, want FOREBRAIN.md loaded whole", sources)
	}
}

// TestRulesBodyLimitCountsRunes pins that the merged body's limit is measured
// in Unicode code points and the cut never splits a character.
func TestRulesBodyLimitCountsRunes(t *testing.T) {
	home := t.TempDir()
	wsRoot := t.TempDir()
	proj := t.TempDir()
	if err := os.Mkdir(filepath.Join(proj, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "FOREBRAIN.md"), []byte(strings.Repeat("中文规则。", 2000)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := safety.MarkTrusted(home, safety.Project{Root: proj, VersionControlled: true}); err != nil {
		t.Fatal(err)
	}
	cfg := &appcfg.Root{}
	cfg.Agents.Defaults.ContextInject.MaxPreHookRulesChars = 100
	p := NewPreHook(cfg, home, wsRoot)
	t.Chdir(proj)
	merged := p.loadRules(proj, hook.HookContext{}, true)
	if !utf8.ValidString(merged) || strings.ContainsRune(merged, '\uFFFD') {
		t.Fatalf("merged body is not clean UTF-8")
	}
	cut := strings.TrimSuffix(merged, "\n…")
	if got := utf8.RuneCountInString(cut); got != 100 {
		t.Fatalf("kept %d runes, want 100", got)
	}
	if got := len(cut); got != 300 {
		t.Fatalf("kept %d bytes, want 300", got)
	}
	if sources := p.InstructionSources(); len(sources) != 1 || sources[0].TruncatedTo != 100 {
		t.Fatalf("sources = %+v, want FOREBRAIN.md keeping 100 runes", sources)
	}
}
