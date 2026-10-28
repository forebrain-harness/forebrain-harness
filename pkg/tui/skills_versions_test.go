package tui

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/stretchr/testify/require"
)

func TestSkillRootAndName(t *testing.T) {
	cases := []struct {
		path      string
		root      string
		name      string
		draftOnly bool
	}{
		{"/a/skills/gateway-ops", "/a/skills", "gateway-ops", false},
		{"/a/skills/.drafts/gateway-5xx", "/a/skills", "gateway-5xx", true},
		{"", "", "", false},
	}
	for _, tc := range cases {
		root, name, draftOnly := skillRootAndName(tc.path)
		if root != tc.root || name != tc.name || draftOnly != tc.draftOnly {
			t.Fatalf("skillRootAndName(%q) = %q,%q,%v; want %q,%q,%v", tc.path, root, name, draftOnly, tc.root, tc.name, tc.draftOnly)
		}
	}
}

func TestSkillStatusPrefix(t *testing.T) {
	entry := skill.Entry{Name: "gateway-ops", Origin: skill.OriginAgent}
	cases := []struct {
		status skillStatus
		want   string
	}{
		{skillStatus{known: true, builtin: true}, "Built-in · "},
		{skillStatus{known: true, label: skill.SkillStateDraft}, "Draft · "},
		{skillStatus{known: true, label: skill.SkillStatePending, version: 3}, "v3 · unpublished changes · "},
		{skillStatus{known: true, label: skill.SkillStatePublished, version: 3}, "v3 · "},
		{skillStatus{}, ""},
	}
	for _, tc := range cases {
		if got := stripANSI(skillStatusPrefix(entry, tc.status)); got != tc.want {
			t.Fatalf("prefix(%+v) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestVersionHistoryDescription(t *testing.T) {
	at := time.Date(2026, 10, 9, 18, 20, 0, 0, time.Local).Format(time.RFC3339)
	rec := skill.VersionRecord{V: 3, At: at, Kind: skill.VersionKindPublished, Note: "Tighten the trigger", Stat: skill.VersionStat{Added: 12, Removed: 3}}
	got := versionHistoryDescription(rec, 3)
	for _, want := range []string{"Current", "Published", "Tighten the trigger", "+12 −3", "Oct 9 18:20"} {
		if !strings.Contains(got, want) {
			t.Fatalf("version description %q missing %q", got, want)
		}
	}
	// A non-current version carries no "Current".
	if strings.Contains(versionHistoryDescription(rec, 4), "Current") {
		t.Fatal("a non-current version must not read Current")
	}
}

func TestSkillDraftNameFromStep(t *testing.T) {
	if got := skillDraftNameFromStep(nil); got != "" {
		t.Fatalf("nil output = %q", got)
	}
	if got := skillDraftNameFromStep(map[string]any{"status": "ok"}); got != "" {
		t.Fatalf("no draft = %q", got)
	}
	got := skillDraftNameFromStep(map[string]any{"skill_draft": map[string]any{"skill": "gateway-5xx", "draft_path": "/a/.drafts/gateway-5xx"}})
	if got != "gateway-5xx" {
		t.Fatalf("draft name = %q", got)
	}
}

func TestShortRevisionAndUpdateDescription(t *testing.T) {
	if shortRevision("d41c8e0aaaaaa") != "d41c8e0" {
		t.Fatalf("short revision = %q", shortRevision("d41c8e0aaaaaa"))
	}
	if got := skillUpdateDescription(&skill.SkillUpdate{Rev: "d41c8e0aaaaaa", Date: "Oct 10", Files: 2}); got != "d41c8e0 · Oct 10 · 2 files" {
		t.Fatalf("update description = %q", got)
	}
}

// A never-published draft's changes read every file as an addition against
// nothing.
func TestDraftTreeDiff(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "references"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: x\n---\nbody\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "references", "runbook.md"), []byte("one\ntwo\n"), 0o644))

	diffs := draftTreeDiff(dir)
	byPath := map[string]SkillFileDiff{}
	for _, d := range diffs {
		byPath[d.Path] = d
	}
	if len(diffs) != 2 {
		t.Fatalf("draft files = %d, want 2: %+v", len(diffs), diffs)
	}
	if byPath["references/runbook.md"].Added != 2 || byPath["references/runbook.md"].Status != "added" {
		t.Fatalf("runbook diff = %+v", byPath["references/runbook.md"])
	}
	if !strings.Contains(byPath["references/runbook.md"].Text, "+one") {
		t.Fatalf("diff body missing added line: %q", byPath["references/runbook.md"].Text)
	}
}

// The changes page paints added and removed lines and never loses a removed
// line's marker to the info panel's bullet handling.
func TestSkillChangesBodyKeepsDiffMarkers(t *testing.T) {
	r := NewRenderer(io.Discard, io.Discard)
	body := skillChangesBody(r, []SkillFileDiff{{
		Path: "SKILL.md", Added: 1, Removed: 1,
		Text: "--- a\n+++ b\n@@ -1 +1 @@\n-old content\n+new content\n",
	}})
	if !strings.Contains(body, "-old content") || !strings.Contains(body, "+new content") {
		t.Fatalf("diff body lost a line: %q", body)
	}
	// Every diff line opens with an SGR, so infoPanel's bullet strip cannot
	// touch a leading "- ".
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "\x1b") {
			t.Fatalf("diff line not SGR-led (would risk bullet stripping): %q", line)
		}
	}
	if strings.TrimSpace(stripANSI(skillChangesBody(r, nil))) != "No changes." {
		t.Fatalf("empty diff body = %q", skillChangesBody(r, nil))
	}
}

// tuiFakeGit puts a shell script named git at the front of PATH: the panel's
// paths must never run the real one.
func tuiFakeGit(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// skillStateWithSource is a published skill whose current version came from
// repo at revision rev.
func skillStateWithSource(repo, rev string) skill.SkillVersionState {
	return skill.SkillVersionState{
		Name:    "demo",
		Current: 1,
		Versions: []skill.VersionRecord{
			{V: 1, Kind: "installed", Source: repo, UpstreamRev: rev},
		},
	}
}

// TestSkillUpdateMarkerNeverWaitsOnTheNetwork pins the freeze plan 184 fixes:
// the marker is computed for every listed row on the event-loop goroutine, so
// an unanswered probe must return at once and start a background one instead.
func TestSkillUpdateMarkerNeverWaitsOnTheNetwork(t *testing.T) {
	tuiFakeGit(t, "sleep 5")
	skill.ForgetRemoteHeadCache()
	t.Cleanup(skill.ForgetRemoteHeadCache)

	start := time.Now()
	got := (&ChatSession{}).skillUpdateMarker("demo", skillStateWithSource("https://example.invalid/x.git", "deadbeef"))
	elapsed := time.Since(start)
	if got != nil {
		t.Fatalf("marker without a cached answer = %+v, want nil", got)
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("skillUpdateMarker blocked %v; the panel must never wait on the probe", elapsed)
	}
}

// TestSkillUpdateMarkerReadsTheCachedAnswer pins the positive side: once a
// probe has answered, the marker is drawn from the cache alone.
func TestSkillUpdateMarkerReadsTheCachedAnswer(t *testing.T) {
	repo := "file://" + t.TempDir()
	tuiFakeGit(t, "echo "+strings.Repeat("a", 40))
	skill.ForgetRemoteHeadCache()
	t.Cleanup(skill.ForgetRemoteHeadCache)

	// Fill the cache the way a background probe would.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rev, ok := skill.RemoteHead(ctx, repo)
	if !ok || rev == "" {
		t.Fatalf("RemoteHead(%q) = %q, %v", repo, rev, ok)
	}

	got := (&ChatSession{}).skillUpdateMarker("demo", skillStateWithSource(repo, "deadbeef"))
	require.NotNil(t, got, "a cached newer revision must draw the marker")
	require.Equal(t, rev, got.Rev)

	if got := (&ChatSession{}).skillUpdateMarker("demo", skillStateWithSource(repo, rev)); got != nil {
		t.Fatalf("marker at the current revision = %+v, want nil", got)
	}
}

// TestPullSkillUpdateUsesItsDeadline pins plan 185: "Pull the update" clones on
// the event-loop goroutine, so an upstream that never answers must fail at the
// pull's own deadline instead of stalling the terminal for as long as git
// retries.
//
// The verdict is the elapsed time rather than a side effect of the child: a
// child killed the moment the deadline lands may not have run its first line
// yet, so reading a marker it writes would make this test race. The window says
// what the contract is — the call waited for its deadline (so it reached the
// clone instead of failing early) and then returned rather than hanging for the
// script's 30 s.
func TestPullSkillUpdateUsesItsDeadline(t *testing.T) {
	tuiFakeGit(t, "exec sleep 30")

	home := t.TempDir()
	skills := filepath.Join(home, "workspace", "skills")
	skillDir := filepath.Join(skills, "demo")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: demo\ndescription: d\n---\n\nx\n"), 0o644))
	versions := filepath.Join(skills, ".versions", "demo")
	require.NoError(t, os.MkdirAll(versions, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(versions, "manifest.json"), []byte(
		`{"name":"demo","current":1,"versions":[{"v":1,"at":"2026-10-01T00:00:00Z",`+
			`"kind":"installed","source":"https://example.invalid/x.git","upstream_rev":"deadbeef"}]}`), 0o644))

	previous := skillPullTimeout
	skillPullTimeout = 500 * time.Millisecond
	t.Cleanup(func() { skillPullTimeout = previous })

	s := &ChatSession{Env: &process.Environment{Deps: run.Deps{Home: home}}}
	start := time.Now()
	err := s.PullSkillUpdate(skill.Entry{Name: "demo", Path: skillDir}, true)
	elapsed := time.Since(start)

	require.Error(t, err, "a pull that never answers must fail at its deadline")
	require.NotContains(t, err.Error(), "not found", "the pull never resolved the skill, so it never reached the clone")
	if elapsed < 400*time.Millisecond {
		t.Fatalf("PullSkillUpdate returned after %v, before its 500 ms deadline: the timing proves nothing", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("PullSkillUpdate returned after %v: the panel's pull is not bounded", elapsed)
	}
}
