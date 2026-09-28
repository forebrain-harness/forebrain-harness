package assembly

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
)

func newDirtyRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v: %s", err, out)
		}
	}
	run("init")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "initial")
	// Leave the tree dirty so there is something to report.
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestCollectReadsDirtyState(t *testing.T) {
	root := newDirtyRepo(t)

	state := Collect(context.Background(), root)

	if state.Empty() {
		t.Fatal("dirty repo reported as empty")
	}
	if !strings.Contains(state.Status, "tracked.txt") {
		t.Errorf("status missing the dirty file: %q", state.Status)
	}
}

// A non-repository, a missing path, or an empty root must degrade quietly:
// callers add this context opportunistically and always proceed without it.
func TestCollectDegradesWithoutRepo(t *testing.T) {
	for name, root := range map[string]string{
		"empty root":   "",
		"not a repo":   t.TempDir(),
		"missing path": filepath.Join(t.TempDir(), "absent"),
	} {
		t.Run(name, func(t *testing.T) {
			if !Collect(context.Background(), root).Empty() {
				t.Fatal("expected empty state")
			}
		})
	}
}

func TestRenderTagsAndClampsSections(t *testing.T) {
	if got := Render(State{}); got != "" {
		t.Errorf("clean state rendered %q, want empty so injection is skipped", got)
	}

	var many []string
	for i := 0; i < maxRenderedLines+10; i++ {
		many = append(many, fmt.Sprintf(" M file%d.go", i))
	}
	got := Render(State{Status: strings.Join(many, "\n"), DiffStat: "1 file changed"})

	if !strings.HasPrefix(got, "<git_context>") || !strings.HasSuffix(got, "</git_context>") {
		t.Errorf("missing tags:\n%s", got)
	}
	if !strings.Contains(got, "… and 10 more") {
		t.Errorf("status not clamped:\n%s", got)
	}
	if !strings.Contains(got, "diff_stat:\n1 file changed") {
		t.Errorf("diff section missing:\n%s", got)
	}
}

func runPreHook(t *testing.T, root string, hc hook.HookContext, text string) hook.PreHookResult {
	t.Helper()
	pre := GitPreHook(func(hook.HookContext) string { return root })
	out, err := pre(context.Background(), "pre", hc, text)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The whole point of the hook: context rides a system addendum, and the user's
// text passes through untouched so the prompt prefix stays cacheable.
func TestPreHookAddsAddendumWithoutTouchingText(t *testing.T) {
	root := newDirtyRepo(t)
	const text = "fix the bug"

	out := runPreHook(t, root, hook.HookContext{Trigger: "user"}, text)

	if out.Text != text {
		t.Fatalf("hook rewrote the user's text: %q", out.Text)
	}
	if !strings.HasPrefix(out.SystemAddendum, "<git_context>") {
		t.Fatalf("addendum = %q", out.SystemAddendum)
	}
}

func TestPreHookSkipsSlashCommandsAndCleanTrees(t *testing.T) {
	dirty := newDirtyRepo(t)

	if out := runPreHook(t, dirty, hook.HookContext{Trigger: "user"}, "/status"); out.SystemAddendum != "" {
		t.Errorf("slash command got an addendum: %q", out.SystemAddendum)
	}
	if out := runPreHook(t, dirty, hook.HookContext{Trigger: "user"}, "   "); out.SystemAddendum != "" {
		t.Errorf("empty text got an addendum: %q", out.SystemAddendum)
	}
	if out := runPreHook(t, t.TempDir(), hook.HookContext{Trigger: "user"}, "hello"); out.SystemAddendum != "" {
		t.Errorf("non-repo got an addendum: %q", out.SystemAddendum)
	}
}

func TestPreHookIgnoresNonPrePhase(t *testing.T) {
	root := newDirtyRepo(t)
	pre := GitPreHook(func(hook.HookContext) string { return root })

	out, err := pre(context.Background(), "post", hook.HookContext{Trigger: "user"}, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if out.SystemAddendum != "" {
		t.Fatalf("post phase produced an addendum: %q", out.SystemAddendum)
	}
}

// The prompt hook and the context snapshot both want this state within one
// turn; the second read must not shell out to git again.
func TestCollectCachesWithinATurn(t *testing.T) {
	root := newDirtyRepo(t)

	first := Collect(context.Background(), root)
	if first.Empty() {
		t.Fatal("expected dirty state")
	}
	// Make the tree clean behind the cache. A re-read would report empty.
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if second := Collect(context.Background(), root); second != first {
		t.Fatalf("second read bypassed the cache: %#v", second)
	}

	// A different root is a different repository and must not share the entry.
	if !Collect(context.Background(), t.TempDir()).Empty() {
		t.Fatal("cache leaked across roots")
	}
}
