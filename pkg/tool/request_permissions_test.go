package tool

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

func TestDefaultAllowedRootsForWorkspaceIncludesWorkspace(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "workspaces", "review")
	roots := DefaultAllowedRootsForWorkspace(ws)
	if !rootListContains(roots, ws) {
		t.Fatalf("allowed roots %v missing workspace root %q", roots, ws)
	}
}

func TestDefaultAllowedRootsForWorkspaceExcludesSiblingAgents(t *testing.T) {
	home := t.TempDir()
	mainWS := filepath.Join(home, "workspace")
	reviewWS := filepath.Join(home, "workspaces", "review")
	for _, root := range DefaultAllowedRootsForWorkspace(reviewWS) {
		if root == mainWS {
			t.Fatalf("review agent allowed roots leak main workspace %q", mainWS)
		}
	}
}

func TestDefaultAllowedRootsEmptyWorkspace(t *testing.T) {
	for _, root := range DefaultAllowedRootsForWorkspace("") {
		if root == "" {
			t.Fatal("empty workspace produced an empty allowed root")
		}
	}
}

func TestSetPrimaryWorkspaceBoundaryFiltersSiblingOverlaps(t *testing.T) {
	home := t.TempDir()
	mainWS := filepath.Join(home, "workspace")
	reviewWS := filepath.Join(home, "workspaces", "review")
	external := filepath.Join(t.TempDir(), "external")
	st := NewState(home, mainWS, filepath.Join(mainWS, "state", "tool-outputs"), external)

	st.SetPrimaryWorkspaceBoundary(home, reviewWS, []string{mainWS})

	roots := st.AllowedRoots()
	if !rootListContains(roots, reviewWS) {
		t.Fatalf("roots %v missing active review workspace %q", roots, reviewWS)
	}
	for _, blocked := range []string{home, mainWS, filepath.Join(mainWS, "state", "tool-outputs")} {
		if rootListContains(roots, blocked) {
			t.Fatalf("roots %v retained blocked primary-agent path %q", roots, blocked)
		}
	}
	if !rootListContains(roots, external) {
		t.Fatalf("roots %v dropped unrelated baseline root %q", roots, external)
	}
	if !st.PathUnderPrimaryWorkspace(filepath.Join(reviewWS, "state", "note.md")) {
		t.Fatal("active review workspace must be recognized")
	}
	if st.PathUnderPrimaryWorkspace(filepath.Join(mainWS, "state", "note.md")) {
		t.Fatal("sibling main workspace must not be recognized as active")
	}
}

func TestPrependAllowedRootRejectsSiblingWorkspaceAncestor(t *testing.T) {
	home := t.TempDir()
	mainWS := filepath.Join(home, "workspace")
	reviewWS := filepath.Join(home, "workspaces", "review")
	st := NewState(reviewWS)
	st.SetPrimaryWorkspaceBoundary(home, reviewWS, []string{mainWS})
	st.PrependAllowedRoot(home)
	if rootListContains(st.AllowedRoots(), mainWS) || rootListContains(st.AllowedRoots(), home) {
		t.Fatalf("sibling/ancestor root was introduced: %v", st.AllowedRoots())
	}
}

func rootListContains(roots []string, want string) bool {
	want = normalizeRootPath(want)
	for _, root := range roots {
		if normalizeRootPath(root) == want {
			return true
		}
	}
	return false
}

func TestPrependAllowedRootInsertsAtFront(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "ws")
	st := NewState(ws)
	project := filepath.Join(t.TempDir(), "project")
	st.PrependAllowedRoot(project)
	roots := st.AllowedRoots()
	if len(roots) != 2 || roots[0] != filepath.Clean(project) {
		t.Fatalf("unexpected roots: %v", roots)
	}
}

func TestPrependAllowedRootMovesExistingToFront(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "ws")
	project := filepath.Join(t.TempDir(), "project")
	st := NewState(ws, project)
	st.PrependAllowedRoot(project)
	roots := st.AllowedRoots()
	if roots[0] != filepath.Clean(project) {
		t.Fatalf("roots[0] = %q, want %q", roots[0], project)
	}
	count := 0
	for _, root := range roots {
		if root == filepath.Clean(project) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("project appeared %d times: %v", count, roots)
	}
}

func TestPrependAllowedRootNoOpForEmptyOrCurrentFirst(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "ws")
	st := NewState(ws)
	st.PrependAllowedRoot("")
	st.PrependAllowedRoot(ws)
	if roots := st.AllowedRoots(); len(roots) != 1 || roots[0] != filepath.Clean(ws) {
		t.Fatalf("unexpected roots: %v", roots)
	}
}

func TestExtractApplyPatchBody(t *testing.T) {
	body, ok, err := extractApplyPatchBody("apply_patch <<'PATCH'\n*** Begin Patch\n*** End Patch\nPATCH")
	if err != nil || !ok {
		t.Fatalf("extract: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(body, "*** Begin Patch") {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestShellCommandIsApplyPatchUsesExecutionParser(t *testing.T) {
	if !ShellCommandIsApplyPatch("apply_patch <<'PATCH'\n*** Begin Patch\n*** Add File: a.txt\n+x\n*** End Patch\nPATCH") {
		t.Fatal("standalone valid patch was not recognized")
	}
	if ShellCommandIsApplyPatch("apply_patch <<'PATCH'\n*** Begin Patch\n*** End Patch\nPATCH\necho unexpected") {
		t.Fatal("trailing command must not be routed as a patch")
	}
}

func TestParseApplyPatchAddUpdateDelete(t *testing.T) {
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: new.txt",
		"+hello",
		"+world",
		"*** Update File: existing.txt",
		"@@",
		"-old",
		"+new",
		"*** Delete File: removed.txt",
		"*** End Patch",
	}, "\n")
	ops, err := parseApplyPatch(patch)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ops) != 3 {
		t.Fatalf("ops len = %d, want 3", len(ops))
	}
	if ops[0].Kind != applyPatchAdd || ops[0].Path != "new.txt" {
		t.Fatalf("unexpected add op: %+v", ops[0])
	}
	if ops[1].Kind != applyPatchUpdate || ops[1].Path != "existing.txt" {
		t.Fatalf("unexpected update op: %+v", ops[1])
	}
	if ops[2].Kind != applyPatchDelete || ops[2].Path != "removed.txt" {
		t.Fatalf("unexpected delete op: %+v", ops[2])
	}
}

func TestApplyPatchOperations(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	st := NewState(root)
	tool, err := NewShellTool(st, &AgentToolRuntime{Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess}})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	mustWriteFile(t, root+"/existing.txt", "old\n")
	cmd := `apply_patch <<'PATCH'
*** Begin Patch
*** Update File: existing.txt
@@
-old
+new
*** Add File: added.txt
+alpha
+beta
*** End Patch
PATCH`
	args, err := json.Marshal(map[string]string{"command": cmd})
	if err != nil {
		t.Fatalf("encode args: %v", err)
	}
	ctx := context.Background()
	out, err := tool.Handle(ctx, string(args))
	if err != nil {
		t.Fatalf("tool handle: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out.(string)), &payload); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if payload["apply_patch"] != true {
		t.Fatalf("expected apply_patch output, got %#v", payload)
	}
	if got := mustReadFile(t, root+"/existing.txt"); got != "new\n" {
		t.Fatalf("existing content = %q", got)
	}
	if got := mustReadFile(t, root+"/added.txt"); got != "alpha\nbeta\n" {
		t.Fatalf("added content = %q", got)
	}
}

func TestApplyPatchDeleteFile(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	st := NewState(root)
	tool, err := NewShellTool(st, &AgentToolRuntime{Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess}})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	mustWriteFile(t, root+"/removed.txt", "gone\n")
	cmd := `apply_patch <<'PATCH'
*** Begin Patch
*** Delete File: removed.txt
*** End Patch
PATCH`
	args, err := json.Marshal(map[string]string{"command": cmd})
	if err != nil {
		t.Fatalf("encode args: %v", err)
	}
	if _, err := tool.Handle(context.Background(), string(args)); err != nil {
		t.Fatalf("tool handle: %v", err)
	}
	if _, err := os.Stat(root + "/removed.txt"); !os.IsNotExist(err) {
		t.Fatalf("removed.txt should be deleted, stat err=%v", err)
	}
}

func TestApplyPatchMovesUpdatedFile(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	st := NewState(root)
	tool, err := NewShellTool(st, &AgentToolRuntime{Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess}})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	mustWriteFile(t, root+"/old.txt", "old\n")
	cmd := `apply_patch <<'PATCH'
*** Begin Patch
*** Update File: old.txt
*** Move to: nested/new.txt
@@
-old
+new
*** End Patch
PATCH`
	args, err := json.Marshal(map[string]string{"command": cmd})
	if err != nil {
		t.Fatalf("encode args: %v", err)
	}
	if _, err := tool.Handle(context.Background(), string(args)); err != nil {
		t.Fatalf("tool handle: %v", err)
	}
	if _, err := os.Stat(root + "/old.txt"); !os.IsNotExist(err) {
		t.Fatalf("old.txt should be removed, stat err=%v", err)
	}
	if got := mustReadFile(t, root+"/nested/new.txt"); got != "new\n" {
		t.Fatalf("moved content = %q", got)
	}
}

func TestExtractApplyPatchRejectsTrailingShellCommands(t *testing.T) {
	_, ok, err := extractApplyPatchBody("apply_patch <<'PATCH'\n*** Begin Patch\n*** End Patch\nPATCH\necho bad")
	if !ok || err == nil {
		t.Fatalf("expected trailing command error, got ok=%v err=%v", ok, err)
	}
}

func TestApplyPatchCommandRejectsMalformedBody(t *testing.T) {
	body, ok, err := extractApplyPatchBody("apply_patch <<'PATCH'\nnot a patch\nPATCH")
	if err != nil || !ok {
		t.Fatalf("extract: ok=%v err=%v", ok, err)
	}
	if _, err := parseApplyPatch(body); err == nil {
		t.Fatalf("expected parse error")
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestValidateApprovalUpdateForActionRejectsWideningRule(t *testing.T) {
	_, err := ValidateApprovalUpdateForAction("shell", `{"command":"git status","session_id":"s1"}`, "s1", safety.PermissionUpdate{
		Type: safety.UpdateAddRules, Destination: safety.DestinationLocalSettings, Behavior: safety.BehaviorAllow,
		Rules: []safety.PermissionRuleValue{{ToolName: "Bash", RuleContent: "git:*", BypassSandbox: true}},
	})
	if err == nil {
		t.Fatal("expected a widened command rule to be rejected")
	}
}

func TestValidateApprovalUpdateForActionRejectsLegacyExitPlanModeUpdate(t *testing.T) {
	_, err := ValidateApprovalUpdateForAction("exit_plan_mode", `{"session_id":"s1","action":"exit"}`, "s1", safety.PermissionUpdate{
		Type: safety.UpdateSetMode, Destination: safety.DestinationSession,
		Mode: safety.ModeNever,
	})
	if err == nil {
		t.Fatal("exit_plan_mode must not change the current permission mode")
	}
}

// A command whose clauses yield no prefix at all persists as itself, carrying
// the sandbox bypass it was remembered for.
func TestValidateApprovalUpdateForActionPersistsTheExactCommand(t *testing.T) {
	command := "gofmt -w internal/a.go && rm -rf /tmp/x"
	payload := `{"command":"gofmt -w internal/a.go && rm -rf /tmp/x"}`
	update, err := ValidateApprovalUpdateForAction("shell", payload, "s1", safety.PermissionUpdate{
		Type: safety.UpdateAddRules, Destination: safety.DestinationLocalSettings, Behavior: safety.BehaviorAllow,
		Rules: []safety.PermissionRuleValue{{ToolName: "Bash", Command: command, BypassSandbox: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(update.Rules) != 1 || update.Rules[0].Command != command ||
		update.Rules[0].RuleContent != "" || len(update.Rules[0].CommandPrefix) != 0 ||
		!update.Rules[0].BypassSandbox {
		t.Fatalf("normalized update = %+v", update)
	}
}

// A compound command where one clause does yield a prefix persists as one rule
// per clause, which is what the engine needs to allow the command again: it
// requires every clause to match a rule of its own, so a single rule naming one
// of them would leave the prompt returning on the next call.
func TestValidateApprovalUpdateForActionPersistsOneRulePerClause(t *testing.T) {
	payload := `{"command":"gofmt -w internal/a.go && go mod tidy"}`
	want := []safety.PermissionRuleValue{
		{ToolName: "Bash", Command: "gofmt -w internal/a.go", BypassSandbox: true},
		{ToolName: "Bash", CommandPrefix: []string{"go", "mod"}, BypassSandbox: true},
	}
	update, err := ValidateApprovalUpdateForAction("shell", payload, "s1", safety.PermissionUpdate{
		Type: safety.UpdateAddRules, Destination: safety.DestinationLocalSettings, Behavior: safety.BehaviorAllow,
		Rules: want,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(update.Rules) != len(want) {
		t.Fatalf("normalized update = %+v", update)
	}
	for i, rule := range update.Rules {
		if rule.Command != want[i].Command || rule.RuleContent != want[i].RuleContent ||
			!slices.Equal(rule.CommandPrefix, want[i].CommandPrefix) || !rule.BypassSandbox {
			t.Fatalf("rule %d = %+v, want %+v", i, rule, want[i])
		}
	}
}

// A rule set the server did not propose is refused whole: a surface picks among
// proposals, it does not author rules.
func TestValidateApprovalUpdateForActionRejectsAnUnproposedRuleSet(t *testing.T) {
	payload := `{"command":"gofmt -w internal/a.go && go mod tidy"}`
	_, err := ValidateApprovalUpdateForAction("shell", payload, "s1", safety.PermissionUpdate{
		Type: safety.UpdateAddRules, Destination: safety.DestinationLocalSettings, Behavior: safety.BehaviorAllow,
		Rules: []safety.PermissionRuleValue{
			{ToolName: "Bash", Command: "gofmt -w internal/a.go", BypassSandbox: true},
			{ToolName: "Bash", CommandPrefix: []string{"go"}, BypassSandbox: true},
		},
	})
	if err == nil {
		t.Fatal("expected a widened clause rule to be rejected")
	}
}

// The exact rule names one command. A prefix built from it, or any other
// command, would grant more than the row the user chose.
func TestValidateApprovalUpdateForActionRejectsWideningTheExactCommand(t *testing.T) {
	payload := `{"command":"gofmt -w internal/a.go && go mod tidy"}`
	for _, rule := range []safety.PermissionRuleValue{
		{ToolName: "Bash", CommandPrefix: []string{"gofmt"}, BypassSandbox: true},
		{ToolName: "Bash", RuleContent: "gofmt:*", BypassSandbox: true},
		{ToolName: "Bash", RuleContent: "gofmt -w internal/a.go", BypassSandbox: true},
		{ToolName: "Bash", RuleContent: "gofmt -w internal/a.go && go mod tidy"},
	} {
		if _, err := ValidateApprovalUpdateForAction("shell", payload, "s1", safety.PermissionUpdate{
			Type: safety.UpdateAddRules, Destination: safety.DestinationLocalSettings,
			Behavior: safety.BehaviorAllow, Rules: []safety.PermissionRuleValue{rule},
		}); err == nil {
			t.Fatalf("expected %+v to be rejected", rule)
		}
	}
}

// A command whose exact form the rule parser would read as a wildcard proposes
// no persistent rule at all, so none can be submitted for it either.
func TestValidateApprovalUpdateForActionRefusesUnproposedCommandRule(t *testing.T) {
	command := "cp /tmp/scratch/* /tmp/dest && chmod -R 755 /tmp/dest"
	if _, err := ValidateApprovalUpdateForAction("shell", `{"command":"`+command+`"}`, "s1", safety.PermissionUpdate{
		Type: safety.UpdateAddRules, Destination: safety.DestinationLocalSettings, Behavior: safety.BehaviorAllow,
		Rules: []safety.PermissionRuleValue{{ToolName: "Bash", RuleContent: command, BypassSandbox: true}},
	}); err == nil {
		t.Fatal("expected a command with no proposed rule to be rejected")
	}
}

func TestValidateApprovalUpdateForActionScopesReadToSessionAndPath(t *testing.T) {
	update, err := ValidateApprovalUpdateForAction("Read", `{"file_path":"/tmp/notes.txt"}`, "s1", safety.PermissionUpdate{
		Type: safety.UpdateAddRules, Destination: safety.DestinationSession, Behavior: safety.BehaviorAllow,
		Rules: []safety.PermissionRuleValue{{ToolName: "Read", RuleContent: "/tmp/notes.txt"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if update.SessionID != "s1" || len(update.Rules) != 1 || update.Rules[0].RuleContent != "/tmp/notes.txt" ||
		update.Rules[0].BypassSandbox {
		t.Fatalf("normalized update = %+v", update)
	}
	if _, err := ValidateApprovalUpdateForAction("Read", `{"file_path":"/tmp/notes.txt"}`, "s1", safety.PermissionUpdate{
		Type: safety.UpdateAddRules, Destination: safety.DestinationSession, Behavior: safety.BehaviorAllow,
		Rules: []safety.PermissionRuleValue{{ToolName: "Read", RuleContent: "/tmp/*"}},
	}); err == nil {
		t.Fatal("a session read approval must not widen past the requested path")
	}
}

// WebFetch remembers the host the prompt named, not the single URL and not a
// wider domain.
func TestValidateApprovalUpdateForActionPersistsWebFetchDomain(t *testing.T) {
	payload := `{"url":"https://example.com/docs/page"}`
	update, err := ValidateApprovalUpdateForAction("WebFetch", payload, "s1", safety.PermissionUpdate{
		Type: safety.UpdateAddRules, Destination: safety.DestinationLocalSettings, Behavior: safety.BehaviorAllow,
		Rules: []safety.PermissionRuleValue{{ToolName: "WebFetch", RuleContent: "domain:example.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(update.Rules) != 1 || update.Rules[0].RuleContent != "domain:example.com" || update.Rules[0].BypassSandbox {
		t.Fatalf("normalized update = %+v", update)
	}
	if _, err := ValidateApprovalUpdateForAction("WebFetch", payload, "s1", safety.PermissionUpdate{
		Type: safety.UpdateAddRules, Destination: safety.DestinationLocalSettings, Behavior: safety.BehaviorAllow,
		Rules: []safety.PermissionRuleValue{{ToolName: "WebFetch", RuleContent: "domain:*.com"}},
	}); err == nil {
		t.Fatal("a widened WebFetch domain must be rejected")
	}
}

func TestValidateApprovalUpdateForActionScopesPatchPaths(t *testing.T) {
	update, err := ValidateApprovalUpdateForAction("apply_patch", `{"resolved_paths":["/repo/a.go","/repo/b.go"]}`, "s1", safety.PermissionUpdate{
		Type: safety.UpdateAddRules, Destination: safety.DestinationSession, Behavior: safety.BehaviorAllow,
		Rules: []safety.PermissionRuleValue{
			{ToolName: "apply_patch", RuleContent: "/repo/a.go", BypassSandbox: true},
			{ToolName: "apply_patch", RuleContent: "/repo/b.go", BypassSandbox: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if update.SessionID != "s1" || len(update.Rules) != 2 {
		t.Fatalf("unexpected normalized update: %+v", update)
	}
	update.Rules[1].RuleContent = "/repo/c.go"
	if _, err := ValidateApprovalUpdateForAction("apply_patch", `{"resolved_paths":["/repo/a.go","/repo/b.go"]}`, "s1", update); err == nil {
		t.Fatal("expected an unrequested patch path to be rejected")
	}
}

func TestAtomicWritePreservesExistingPermissions(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "script.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if err := atomicWrite(path, []byte("#!/bin/sh\necho new\n"), 0o644); err != nil {
		t.Fatalf("atomicWrite: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if got := info.Mode().Perm(); unixPermsObservable() && got != 0o755 {
		t.Fatalf("perm=%#o want %#o", got, os.FileMode(0o755))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(raw) != "#!/bin/sh\necho new\n" {
		t.Fatalf("content=%q", string(raw))
	}
}

func TestAtomicWriteUsesRequestedPermissionsForNewFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "new.txt")

	if err := atomicWrite(path, []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("atomicWrite: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if got := info.Mode().Perm(); unixPermsObservable() && got != 0o600 {
		t.Fatalf("perm=%#o want %#o", got, os.FileMode(0o600))
	}
}

func TestSplitLineRangeClampsOverrunEnd(t *testing.T) {
	original := "l1\nl2\nl3\nl4\nl5"

	got, ok := SplitLineRange(original, "1-80")
	if !ok {
		t.Fatalf("expected ok=true for overrun end, got false")
	}
	want := []string{"l1", "l2", "l3", "l4", "l5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSplitLineRangeExactBounds(t *testing.T) {
	original := "l1\nl2\nl3"

	got, ok := SplitLineRange(original, "2-3")
	if !ok {
		t.Fatalf("expected ok=true, got false")
	}
	want := []string{"l2", "l3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSplitLineRangeSingleLine(t *testing.T) {
	original := "l1\nl2\nl3"

	got, ok := SplitLineRange(original, "2")
	if !ok {
		t.Fatalf("expected ok=true, got false")
	}
	want := []string{"l2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSplitLineRangeRejectsStartOutOfBounds(t *testing.T) {
	original := "l1\nl2\nl3"

	if _, ok := SplitLineRange(original, "10-20"); ok {
		t.Fatalf("expected ok=false when start exceeds available lines")
	}
	if _, ok := SplitLineRange(original, "0-2"); ok {
		t.Fatalf("expected ok=false for start below 1")
	}
}

func TestSplitLineRangeRejectsInvertedRange(t *testing.T) {
	original := "l1\nl2\nl3"

	if _, ok := SplitLineRange(original, "3-1"); ok {
		t.Fatalf("expected ok=false for inverted range")
	}
}

func TestSplitLineRangeRejectsMalformedSpec(t *testing.T) {
	original := "l1\nl2\nl3"

	if _, ok := SplitLineRange(original, ""); ok {
		t.Fatalf("expected ok=false for empty spec")
	}
	if _, ok := SplitLineRange(original, "abc"); ok {
		t.Fatalf("expected ok=false for non-numeric spec")
	}
}

func TestCoordinatorModeNoLongerUsesNormalMainAgentToolPolicy(t *testing.T) {
	ctx := WithMode(context.Background(), "coordinator")
	st := NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "write_file", ReadOnly: false})
	if err := st.GuardTool(ctx, "write_file"); err == nil {
		t.Fatal("coordinator mode unexpectedly used normal main-agent tool policy")
	}
}

func TestRegisterDefaultToolsHasCorrectCategories(t *testing.T) {
	st := NewState(t.TempDir())
	a, err := agent.New(noopLLM{}, "test", "test")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := RegisterDefaultTools(a, st, &AgentToolRuntime{}); err != nil {
		t.Fatalf("RegisterDefaultTools: %v", err)
	}
	for _, name := range []string{"read_file", "shell"} {
		meta, ok := st.ToolMetaByName(name)
		if !ok {
			t.Fatalf("missing meta for %s", name)
		}
		if meta.Category == "" {
			t.Fatalf("%s should have a non-empty category: %+v", name, meta)
		}
	}
	for _, meta := range st.ToolMetas() {
		if meta.Category == "git" {
			t.Fatalf("git commands should be routed through shell, got git tool meta: %+v", meta)
		}
	}
}

func TestCoordinatorModeToolsAreNotRegistered(t *testing.T) {
	st := NewState(t.TempDir())
	a, err := agent.New(noopLLM{}, "main", "test")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := RegisterDefaultTools(a, st, &AgentToolRuntime{Home: t.TempDir()}); err != nil {
		t.Fatalf("RegisterDefaultTools: %v", err)
	}
	for _, meta := range st.ToolMetas() {
		switch meta.Name {
		case "enter_coordinator_mode", "exit_coordinator_mode":
			t.Fatalf("coordinator mode tool is still registered: %s", meta.Name)
		}
	}
}

// Skills live in the prompt catalog and are read from disk, so the tool table
// holds only tools. That separation is what keeps the tool array fixed for a
// session — it renders ahead of the whole cached prefix, so a skill entering it
// would re-bill every cached token.
func TestToolTableHoldsOnlyTools(t *testing.T) {
	st := NewState(t.TempDir())
	a, err := agent.New(noopLLM{}, "main", "test")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := RegisterDefaultTools(a, st, &AgentToolRuntime{Home: t.TempDir()}); err != nil {
		t.Fatalf("RegisterDefaultTools: %v", err)
	}
	st.ReplaceLoadedSkillCatalog([]LoadedSkill{{Name: "demo", RootDir: t.TempDir()}})
	for _, meta := range st.ToolMetas() {
		if meta.Category == "skill" {
			t.Fatalf("skill-category metadata appeared in the tool table: %+v", meta)
		}
		if meta.Name == "demo" {
			t.Fatalf("the loaded skill %q appeared in the tool table", meta.Name)
		}
	}
	if len(st.LoadedSkills()) != 1 {
		t.Fatalf("loaded skills = %+v, want the catalog to still hold it for resource access", st.LoadedSkills())
	}
}

func TestFindGoProjectRootWalksUp(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "internal", "thing")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := findGoProjectRoot(nested); got != root {
		t.Fatalf("root=%q want=%q", got, root)
	}
}

func TestResolveGoCacheAccessCreatesIsolatedCache(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "go.mod"), []byte("module example.test/project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	rt := &AgentToolRuntime{Home: home, Cfg: &appcfg.Root{SandboxWorkspaceWrite: appcfg.SandboxWorkspaceWrite{GoCacheMode: appcfg.GoCacheModeIsolated}}}
	access := resolveGoCacheAccess(context.Background(), rt, project)
	if len(access.Writable) != 3 {
		t.Fatalf("writable=%v", access.Writable)
	}
	for _, path := range access.Writable {
		if !strings.HasPrefix(path, filepath.Join(home, "cache", "go")) {
			t.Fatalf("cache escaped Forebrain Harness home: %s", path)
		}
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("cache dir %s: %v", path, err)
		}
	}
	env := strings.Join(access.Env, "\n")
	for _, key := range []string{"GOCACHE=", "GOMODCACHE=", "GOTMPDIR="} {
		if !strings.Contains(env, key) {
			t.Fatalf("missing %s in %q", key, env)
		}
	}
}

func TestPrependGoProxy(t *testing.T) {
	if got := prependGoProxy("file:///cache", "https://proxy.golang.org,direct"); got != "file:///cache,https://proxy.golang.org,direct" {
		t.Fatal(got)
	}
	if got := prependGoProxy("file:///cache", "off"); got != "file:///cache,off" {
		t.Fatal(got)
	}
}

// This opt-in test exercises the complete path (project detection, cache
// broker, OS sandbox, and the go tool) without making ordinary unit tests
// depend on sandbox-exec/bwrap being installed on the test host.
func TestSandboxedGoTestUsesIsolatedCaches(t *testing.T) {
	if os.Getenv("FOREBRAIN_RUN_SANDBOX_INTEGRATION") != "1" {
		t.Skip("set FOREBRAIN_RUN_SANDBOX_INTEGRATION=1 to exercise the OS sandbox")
	}
	workspace := t.TempDir()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/cachecheck\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "cache_test.go"), []byte("package cachecheck\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &appcfg.Root{
		ApprovalPolicy: appcfg.NewApprovalPolicy(appcfg.ApprovalPolicyNever),
		SandboxMode:    appcfg.SandboxModeWorkspaceWrite,
	}
	rt := &AgentToolRuntime{Home: home, WorkspaceRoot: workspace, Cfg: cfg}
	got, err := runSandboxedShellCommand(context.Background(), NewState(workspace), rt, sandboxedShellRequest{
		command:            `go test ./... && go env GOCACHE GOMODCACHE GOTMPDIR`,
		cwd:                workspace,
		profile:            safety.ProfileWorkspaceWrite,
		sandboxPermissions: safety.SandboxPermissionsUseDefault,
	})
	if err != nil {
		t.Fatalf("sandboxed go test: %v", err)
	}
	var result struct {
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		ExitCode int    `json:"exit_code"`
	}
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("decode result %q: %v", got, err)
	}
	wantCachePrefix := filepath.Join(home, "cache", "go") + string(filepath.Separator)
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, "ok") || strings.Count(result.Stdout, wantCachePrefix) != 3 {
		t.Fatalf("unexpected result: exit=%d stdout=%q stderr=%q; want all Go caches under %q", result.ExitCode, result.Stdout, result.Stderr, wantCachePrefix)
	}
}

func TestTrimHeadTailPreservesPrefixAndSuffix(t *testing.T) {
	in := "0123456789abcdefghijABCDEFGHIJ"
	got := TrimHeadTail(in, 18)
	if !got.Truncated {
		t.Fatal("expected output to be truncated")
	}
	if got.OmittedBytes <= 0 {
		t.Fatalf("expected omitted bytes, got %d", got.OmittedBytes)
	}
	if !strings.HasPrefix(got.Text, "012") {
		t.Fatalf("expected prefix to be preserved, got %q", got.Text)
	}
	if !strings.HasSuffix(got.Text, "GHIJ") {
		t.Fatalf("expected suffix to be preserved, got %q", got.Text)
	}
}

func TestTrimHeadTailKeepsUTF8Valid(t *testing.T) {
	in := strings.Repeat("Ａ", 20)
	got := TrimHeadTail(in, 25)
	if !got.Truncated {
		t.Fatal("expected output to be truncated")
	}
	if !strings.HasPrefix(got.Text, "Ａ") {
		t.Fatalf("expected valid prefix, got %q", got.Text)
	}
	if !strings.HasSuffix(got.Text, "Ａ") {
		t.Fatalf("expected valid suffix, got %q", got.Text)
	}
}

func TestTrimHeadTailBranches(t *testing.T) {
	for _, tt := range []struct {
		name     string
		text     string
		maxBytes int
		wantText string
	}{
		{name: "non positive", text: "abc", maxBytes: 0, wantText: "abc"},
		{name: "already under budget", text: "abc", maxBytes: 3, wantText: "abc"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := TrimHeadTail(tt.text, tt.maxBytes)
			if got.Text != tt.wantText || got.Truncated || got.OmittedBytes != 0 {
				t.Fatalf("TrimHeadTail = %+v", got)
			}
		})
	}

	shortMarker := TrimHeadTail("0123456789abcdef", 8)
	if !shortMarker.Truncated || !strings.Contains(shortMarker.Text, "...") {
		t.Fatalf("short marker result = %+v", shortMarker)
	}
	longMarker := TrimHeadTail(strings.Repeat("x", 100), 40)
	if !longMarker.Truncated || !strings.Contains(longMarker.Text, "omitted") {
		t.Fatalf("long marker result = %+v", longMarker)
	}
	tinyBudget := TrimHeadTail("abcdef", 1)
	if !tinyBudget.Truncated || tinyBudget.Text != "...f" {
		t.Fatalf("tiny budget result = %+v", tinyBudget)
	}
}

func TestClampRuneBoundaries(t *testing.T) {
	text := "aＡb"
	if got := clampEndToRuneBoundary(text, 0); got != 0 {
		t.Fatalf("end <=0 = %d", got)
	}
	if got := clampEndToRuneBoundary(text, len(text)); got != len(text) {
		t.Fatalf("end >=len = %d", got)
	}
	if got := clampEndToRuneBoundary(text, 3); got != 1 {
		t.Fatalf("end inside rune = %d", got)
	}
	if got := clampStartToRuneBoundary(text, 0); got != 0 {
		t.Fatalf("start <=0 = %d", got)
	}
	if got := clampStartToRuneBoundary(text, len(text)); got != len(text) {
		t.Fatalf("start >=len = %d", got)
	}
	if got := clampStartToRuneBoundary(text, 3); got != 4 {
		t.Fatalf("start inside rune = %d", got)
	}
}

func TestIntermediateToolAppendReadClear(t *testing.T) {
	home := t.TempDir()
	st := NewState(home)
	tool, err := NewIntermediateTool(st, home)
	if err != nil {
		t.Fatalf("NewIntermediateTool: %v", err)
	}
	ctx := llm.WithAgentSessionID(context.Background(), "s1")

	note := "## Finding\n\n- fact one\n- fact two\n"
	args, _ := json.Marshal(map[string]any{
		"action":  "append",
		"content": note,
	})
	if _, err := tool.Handle(ctx, string(args)); err != nil {
		t.Fatalf("append: %v", err)
	}

	gotAny, err := tool.Handle(ctx, `{"action":"read"}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	gotMap, ok := gotAny.(map[string]any)
	if !ok {
		t.Fatalf("read result type = %T", gotAny)
	}
	gotContent, _ := gotMap["content"].(string)
	if !strings.Contains(gotContent, "fact one") || !strings.Contains(gotContent, "fact two") {
		t.Fatalf("content = %q", gotContent)
	}

	if _, err := tool.Handle(ctx, `{"action":"clear"}`); err != nil {
		t.Fatalf("clear: %v", err)
	}
	gotAny, err = tool.Handle(ctx, `{"action":"read"}`)
	if err != nil {
		t.Fatalf("read after clear: %v", err)
	}
	gotMap = gotAny.(map[string]any)
	if gotMap["content"] != "" {
		t.Fatalf("content after clear = %#v", gotMap["content"])
	}
}

func TestIntermediateToolAppendRequiresContent(t *testing.T) {
	home := t.TempDir()
	st := NewState(home)
	tool, err := NewIntermediateTool(st, home)
	if err != nil {
		t.Fatalf("NewIntermediateTool: %v", err)
	}
	ctx := llm.WithAgentSessionID(context.Background(), "s2")

	if _, err := tool.Handle(ctx, `{"action":"append"}`); err == nil {
		t.Fatal("expected error when content is missing for append")
	}
}

func TestIntermediateToolDescriptionMentionsReadBack(t *testing.T) {
	desc := IntermediateToolDescription()
	if !strings.Contains(desc, "Use this tool frequently") {
		t.Fatalf("description missing high-frequency guidance: %q", desc)
	}
	if !strings.Contains(desc, "Before responding to the user") {
		t.Fatalf("description missing read-back guidance: %q", desc)
	}
}

func TestIntermediateToolSchemaHasContentField(t *testing.T) {
	schema := intermediateToolSchema()
	b, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("Marshal schema: %v", err)
	}
	txt := string(b)
	if !strings.Contains(txt, `"content"`) || !strings.Contains(txt, `"action"`) {
		t.Fatalf("schema = %s", txt)
	}
	// The old multi-field schema must be gone.
	if strings.Contains(txt, `"intermediate_product"`) || strings.Contains(txt, `"intermediate_json"`) || strings.Contains(txt, `"intermediate_list"`) {
		t.Fatalf("schema still references old fields: %s", txt)
	}
}

const testMemoryProjectKey = "-Test-project"

func testMemoryScopeRoot(t *testing.T, home string) memory.Root {
	t.Helper()
	roots, err := memory.ResolveRootsForAgent(home)
	if err != nil {
		t.Fatal(err)
	}
	return roots.Scope(memory.Scope{Kind: memory.ScopeProject, Key: testMemoryProjectKey})
}

// memorySearchRuntime is an AgentToolRuntime that can search: the index lives
// in the state database the session store owns.
func memorySearchRuntime(t *testing.T, rt *AgentToolRuntime) *AgentToolRuntime {
	t.Helper()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rt.Sess = state.NewSessionStore(db, "main")
	return rt
}

// memoryFiller pads a memory fixture with lines that share none of its words.
// Search is ranked and filtered by term rarity, so a fixture of a few lines
// that all answer the query is one where nothing is discriminating and nothing
// comes back — which is correct behaviour, and useless as a fixture.
const memoryFiller = "部署 流水线 构建 缓存 产物 校验 回滚 发布 灰度 观测\n" +
	"日志 追踪 采样 告警 阈值 容量 扩缩 降级 熔断 限流\n" +
	"账号 权限 角色 策略 审计 密钥 轮换 证书 网关 路由\n"

func paddedMemory(body string) string { return body + strings.Repeat(memoryFiller, 25) }

func memoryToolHandlers(t *testing.T, home string) map[string]func(context.Context, string) (any, error) {
	t.Helper()
	// Search is entirely index-backed, and the index lives in the state
	// database the session store owns, so a runtime without one cannot search.
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tools, err := newDedicatedMemoryTools(NewState(home), &AgentToolRuntime{
		Home: home, WorkspaceRoot: home, ProjectKey: testMemoryProjectKey, Cfg: &appcfg.Root{},
		Sess: state.NewSessionStore(db, "main"),
	})
	if err != nil {
		t.Fatal(err)
	}
	handlers := make(map[string]func(context.Context, string) (any, error), len(tools))
	for _, tool := range tools {
		handlers[tool.Name()] = tool.Handle
	}
	return handlers
}

// TestAdHocNoteIsReadableAfterWrite covers the reported failure: the agent
// saved a note and then could not read it back, because the write tool takes a
// bare filename while the note lands under extensions/ad_hoc/notes/.
func TestAdHocNoteIsReadableAfterWrite(t *testing.T) {
	home := t.TempDir()
	handlers := memoryToolHandlers(t, home)
	filename := "2026-08-16T19-12-00-sandbox-go-build-method.md"
	result, err := handlers["memories_add_ad_hoc_note"](context.Background(), `{"filename":"`+filename+`","note":"sandbox go build"}`)
	if err != nil {
		t.Fatal(err)
	}
	added, ok := result.(addAdHocNoteResponse)
	if !ok {
		t.Fatalf("response = %#v", result)
	}
	if added.Path != memory.AdHocNotePath(filename) {
		t.Fatalf("path = %q", added.Path)
	}
	// Both the returned path and the bare filename the model passed to the
	// write tool must read back, and both must report the stored path so the
	// model is never handed a path it cannot reuse.
	for _, path := range []string{added.Path, filename} {
		read, err := handlers["memories_read"](context.Background(), `{"path":"`+path+`"}`)
		if err != nil {
			t.Fatalf("read %q: %v", path, err)
		}
		response, ok := read.(memory.ReadResponse)
		if !ok {
			t.Fatalf("read %q response = %#v", path, read)
		}
		if response.Content != "sandbox go build" {
			t.Fatalf("read %q content = %q", path, response.Content)
		}
		if response.Path != added.Path {
			t.Fatalf("read %q reported path = %q, want %q", path, response.Path, added.Path)
		}
	}
	// A miss still names the path the model asked for, not the rewritten one.
	_, err = handlers["memories_read"](context.Background(), `{"path":"missing-note.md"}`)
	if err == nil {
		t.Fatal("missing note read accepted")
	}
	if !strings.Contains(err.Error(), "'missing-note.md'") {
		t.Fatalf("error = %v", err)
	}
}

// The fallback exists for a missing file. A refusal that protects integrity —
// here a non-UTF-8 root file — must not be answered with a same-named note's
// contents.
func TestAdHocNoteFallbackOnlyRetriesMissingPaths(t *testing.T) {
	home := t.TempDir()
	root := testMemoryScopeRoot(t, home)
	filename := "2026-08-16T19-12-00-shadowed.md"
	handlers := memoryToolHandlers(t, home)
	if _, err := handlers["memories_add_ad_hoc_note"](context.Background(), `{"filename":"`+filename+`","note":"note body"}`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root.MemoryRoot, filename), []byte{0xff, 0xfe, 0xfd}, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := handlers["memories_read"](context.Background(), `{"path":"`+filename+`"}`)
	if err == nil {
		t.Fatalf("non-UTF-8 root file answered with %#v", result)
	}
	if !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("error = %v", err)
	}
}

// memories_list and memories_search take the same bare note filename the write
// tool accepts, so the model is not sent back to a "was not found" on the two
// sibling tools.
func TestAdHocNoteResolvesForListAndSearch(t *testing.T) {
	home := t.TempDir()
	root := testMemoryScopeRoot(t, home)
	if err := os.MkdirAll(root.MemoryRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	// A note is only findable while its words are rare, so the store needs a
	// body of unrelated memory for them to be rare against.
	if err := os.WriteFile(filepath.Join(root.MemoryRoot, "MEMORY.md"), []byte(strings.Repeat(memoryFiller, 60)), 0o600); err != nil {
		t.Fatal(err)
	}
	handlers := memoryToolHandlers(t, home)
	filename := "2026-08-16T19-12-00-sibling-tools.md"
	if _, err := handlers["memories_add_ad_hoc_note"](context.Background(), `{"filename":"`+filename+`","note":"sandbox go build"}`); err != nil {
		t.Fatal(err)
	}
	stored := memory.AdHocNotePath(filename)
	listed, err := handlers["memories_list"](context.Background(), `{"path":"`+filename+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	entries := listed.(memory.ListResponse).Entries
	if len(entries) != 1 || entries[0].Path != stored {
		t.Fatalf("entries = %#v", entries)
	}
	found, err := handlers["memories_search"](context.Background(), `{"queries":["sandbox"],"path":"`+filename+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	matches := found.(memory.SearchResponse).Matches
	if len(matches) != 1 || matches[0].Path != stored {
		t.Fatalf("matches = %#v", matches)
	}
}

// The injected memory instructions describe the layout with absolute paths, so
// a path copied out of them must resolve to the same file its relative form
// names — while anything outside the root stays refused.
func TestMemoryToolsAcceptAbsolutePathsInsideTheRoot(t *testing.T) {
	home := t.TempDir()
	root := testMemoryScopeRoot(t, home)
	if err := os.MkdirAll(root.MemoryRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root.MemoryRoot, "MEMORY.md"), []byte("alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handlers := memoryToolHandlers(t, home)
	absolute := filepath.ToSlash(filepath.Join(root.MemoryRoot, "MEMORY.md"))
	read, err := handlers["memories_read"](context.Background(), `{"path":"`+absolute+`"}`)
	if err != nil {
		t.Fatalf("absolute read: %v", err)
	}
	if content := read.(memory.ReadResponse).Content; content != "alpha\n" {
		t.Fatalf("content = %q", content)
	}
	outside := filepath.ToSlash(filepath.Join(filepath.Dir(root.MemoryRoot), "escape.md"))
	if err := os.WriteFile(filepath.FromSlash(outside), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers["memories_read"](context.Background(), `{"path":"`+outside+`"}`); err == nil {
		t.Fatal("path outside the memories root accepted")
	}
}

// TestMemoryToolsCannotReachAnotherProjectsMemory is the tool-side half of the
// cross-project isolation guarantee: whatever path the model asks for, the
// memory tools of a session in one project must never answer with another
// project's file. The dedicated tools are the model's normal way into memory,
// so an escape here would defeat the per-project directories entirely.
func TestMemoryToolsCannotReachAnotherProjectsMemory(t *testing.T) {
	home := t.TempDir()
	roots, err := memory.ResolveRootsForAgent(home)
	if err != nil {
		t.Fatal(err)
	}
	const otherKey = "-Other-project"
	mine := roots.Scope(memory.Scope{Kind: memory.ScopeProject, Key: testMemoryProjectKey})
	theirs := roots.Scope(memory.Scope{Kind: memory.ScopeProject, Key: otherKey})
	for root, body := range map[string]string{
		mine.MemoryRoot:   "my project memory\n",
		theirs.MemoryRoot: "OTHER PROJECT SECRET\n",
	} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "MEMORY.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	handlers := memoryToolHandlers(t, home)
	read, err := handlers["memories_read"](context.Background(), `{"path":"MEMORY.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	if content := read.(memory.ReadResponse).Content; content != "my project memory\n" {
		t.Fatalf("content = %q, want this session's own project memory", content)
	}

	otherAbsolute := filepath.ToSlash(filepath.Join(theirs.MemoryRoot, "MEMORY.md"))
	for _, path := range []string{
		otherAbsolute,
		"../" + otherKey + "/MEMORY.md",
		"../../projects/" + otherKey + "/MEMORY.md",
		"global/../../" + otherKey + "/MEMORY.md",
	} {
		result, err := handlers["memories_read"](context.Background(), `{"path":"`+path+`"}`)
		if err == nil {
			t.Fatalf("path %q reached another project: %#v", path, result)
		}
	}

	// A search with no path must stay inside this session's project too.
	found, err := handlers["memories_search"](context.Background(), `{"queries":["SECRET"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if matches := found.(memory.SearchResponse).Matches; len(matches) != 0 {
		t.Fatalf("search reached another project: %#v", matches)
	}
}

// TestMemoryReadRootsCoverOnlyThisProjectAndGlobal covers the other way into
// memory: plain read_file, whose no-approval allowlist is widened to the memory
// folder so the model can open what the instructions describe. That widening
// must name this session's project and the global scope only.
//
// The assertion is about the allowlist, not about read_file refusing outright:
// for reads, a path outside the allowlist is not denied here but handed to the
// sandbox and approval layer to decide (see resolveFileToolPath). So what this
// pins is that another project's memory is never in the silently-allowed set —
// it stops being ordinary readable state and becomes something the sandbox has
// to rule on, exactly like any other file outside the session's scope.
func TestMemoryReadRootsCoverOnlyThisProjectAndGlobal(t *testing.T) {
	home := t.TempDir()
	roots, err := memory.ResolveRootsForAgent(home)
	if err != nil {
		t.Fatal(err)
	}
	const otherKey = "-Other-project"
	mine := roots.Scope(memory.Scope{Kind: memory.ScopeProject, Key: testMemoryProjectKey})
	theirs := roots.Scope(memory.Scope{Kind: memory.ScopeProject, Key: otherKey})
	global := roots.Scope(memory.GlobalScope())

	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	st := NewState(workspace)
	st.SetAdditionalReadRoots([]string{mine.MemoryRoot, global.MemoryRoot})
	allowed := MergeAllowedRootPaths(st.AllowedRoots(), st.PermissionRoots(safety.FileSystemAccessRead))

	within := func(path string) bool {
		_, err := ResolveWithinRoots(path, allowed)
		return err == nil
	}
	for _, path := range []string{
		filepath.Join(mine.MemoryRoot, "MEMORY.md"),
		filepath.Join(global.MemoryRoot, "MEMORY.md"),
	} {
		if !within(path) {
			t.Fatalf("in-scope memory file %s is not in the read allowlist", path)
		}
	}
	for _, path := range []string{
		filepath.Join(theirs.MemoryRoot, "MEMORY.md"),
		filepath.Join(roots.Base(), "projects", otherKey, "MEMORY.md"),
		filepath.Join(roots.Base(), "MEMORY.md"),
	} {
		if within(path) {
			t.Fatalf("another project's memory %s is silently readable", path)
		}
	}
}

// providerToolNamePattern captures the tool-name constraint enforced by the
// OpenAI Responses API ('^[a-zA-Z0-9_-]+$'). A name outside it fails the whole
// request with invalid_request_error before any turn runs.
var memoryToolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func TestDedicatedMemoryToolNamesMatchProviderPattern(t *testing.T) {
	home := t.TempDir()
	rt := &AgentToolRuntime{
		Home:          home,
		WorkspaceRoot: home,
		Cfg:           &appcfg.Root{},
	}
	tools, err := newDedicatedMemoryTools(NewState(home), memorySearchRuntime(t, rt))
	if err != nil {
		t.Fatalf("newDedicatedMemoryTools: %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("expected dedicated memory tools")
	}
	want := map[string]bool{
		"memories_list":            false,
		"memories_read":            false,
		"memories_search":          false,
		"memories_add_ad_hoc_note": false,
	}
	for _, tool := range tools {
		if tool == nil {
			t.Fatal("nil memory tool")
		}
		name := tool.Name()
		if !memoryToolNamePattern.MatchString(name) {
			t.Fatalf("tool name %q does not match provider pattern %s", name, memoryToolNamePattern)
		}
		if _, ok := want[name]; !ok {
			t.Fatalf("unexpected memory tool name %q", name)
		}
		want[name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("missing memory tool %q", name)
		}
	}
}

func TestDedicatedMemoryToolsRejectUnknownAndOutOfRangeArguments(t *testing.T) {
	home := t.TempDir()
	tools, err := newDedicatedMemoryTools(NewState(home), memorySearchRuntime(t, &AgentToolRuntime{Home: home, WorkspaceRoot: home, Cfg: &appcfg.Root{}}))
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]anyTool, len(tools))
	for _, tool := range tools {
		byName[tool.Name()] = anyTool{handle: tool.Handle}
		if tool.Name() == "memories_read" {
			properties, _ := tool.InputSchema()["properties"].(map[string]any)
			if _, exists := properties["max_tokens"]; exists {
				t.Fatal("memories_read schema must not expose max_tokens")
			}
		}
		if tool.Name() == "memories_search" {
			properties, _ := tool.InputSchema()["properties"].(map[string]any)
			if _, exists := properties["context_lines"]; exists {
				t.Fatal("memories_search schema must not expose context_lines")
			}
		}
	}
	for _, test := range []struct {
		name string
		args string
	}{
		{name: "memories_list", args: `{"unknown":true}`},
		{name: "memories_list", args: `{"max_results":0}`},
		{name: "memories_read", args: `{"path":"MEMORY.md","line_offset":-1}`},
		{name: "memories_read", args: `{"path":"MEMORY.md","max_lines":0}`},
		{name: "memories_read", args: `{"path":"MEMORY.md","max_tokens":1}`},
		// context_lines is retired from the search schema: the window around
		// each hit is the search's own, so a call that still sends the argument
		// is refused rather than quietly ignored.
		{name: "memories_search", args: `{"queries":["x"],"context_lines":3}`},
		{name: "memories_search", args: `{"queries":["x"],"match_mode":{"type":"any","line_count":1}}`},
	} {
		tool := byName[test.name]
		if _, err := tool.handle(context.Background(), test.args); err == nil {
			t.Fatalf("%s accepted %s", test.name, test.args)
		}
	}
}

// TestDedicatedMemoryToolSchemasOnlyRequireMandatoryFields guards the schema
// contract that keeps optional parameters optional. When every property is
// reflected as required, models are forced to emit a value for parameters they
// mean to skip — `"cursor": ""` on a first page, which the backend cannot parse
// as an index.
func TestDedicatedMemoryToolSchemasOnlyRequireMandatoryFields(t *testing.T) {
	home := t.TempDir()
	tools, err := newDedicatedMemoryTools(NewState(home), memorySearchRuntime(t, &AgentToolRuntime{Home: home, WorkspaceRoot: home, Cfg: &appcfg.Root{}}))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"memories_list":            {},
		"memories_read":            {"path"},
		"memories_search":          {"queries"},
		"memories_add_ad_hoc_note": {"filename", "note"},
	}
	for _, tool := range tools {
		expected, ok := want[tool.Name()]
		if !ok {
			t.Fatalf("unexpected memory tool %q", tool.Name())
		}
		required := map[string]bool{}
		for _, name := range asStringSlice(t, tool.InputSchema()["required"]) {
			required[name] = true
		}
		if len(required) != len(expected) {
			t.Fatalf("%s required = %v, want %v", tool.Name(), required, expected)
		}
		for _, name := range expected {
			if !required[name] {
				t.Fatalf("%s required = %v, want %v", tool.Name(), required, expected)
			}
		}
	}
}

// TestDedicatedMemoryToolsAcceptOmittedAndBlankOptionalArguments covers the
// reported failure end to end, plus the shapes a model reaches for when it means
// "no value": omitting an optional field, sending an explicit null, and padding
// it with an empty or blank string.
func TestDedicatedMemoryToolsAcceptOmittedAndBlankOptionalArguments(t *testing.T) {
	home := t.TempDir()
	roots, err := memory.ResolveRootsForAgent(home)
	if err != nil {
		t.Fatal(err)
	}
	root := roots.Scope(memory.Scope{Kind: memory.ScopeProject, Key: testMemoryProjectKey})
	if err := os.MkdirAll(root.MemoryRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root.MemoryRoot, "MEMORY.md"), []byte("alpha\nneedle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// "needle" only counts as a hit while it is rare, so the store needs a body
	// of unrelated lines for it to be rare against.
	if err := os.WriteFile(filepath.Join(root.MemoryRoot, "other.md"), []byte(strings.Repeat(memoryFiller, 60)), 0o600); err != nil {
		t.Fatal(err)
	}
	tools, err := newDedicatedMemoryTools(NewState(home), memorySearchRuntime(t, &AgentToolRuntime{Home: home, WorkspaceRoot: home, ProjectKey: testMemoryProjectKey, Cfg: &appcfg.Root{}}))
	if err != nil {
		t.Fatal(err)
	}
	handlers := make(map[string]func(context.Context, string) (any, error), len(tools))
	for _, tool := range tools {
		handlers[tool.Name()] = tool.Handle
	}
	for _, test := range []struct {
		name string
		tool string
		args string
	}{
		{name: "list omits every optional", tool: "memories_list", args: `{}`},
		{name: "list nulls every optional", tool: "memories_list", args: `{"path":null,"cursor":null,"max_results":null}`},
		{name: "list blank cursor and path", tool: "memories_list", args: `{"path":"","cursor":"","max_results":10}`},
		{name: "list padded path", tool: "memories_list", args: `{"path":" "}`},
		{name: "read omits every optional", tool: "memories_read", args: `{"path":"MEMORY.md"}`},
		{name: "read nulls every optional", tool: "memories_read", args: `{"path":"MEMORY.md","line_offset":null,"max_lines":null}`},
		{name: "search omits every optional", tool: "memories_search", args: `{"queries":["needle"]}`},
		{name: "search blank cursor", tool: "memories_search", args: `{"queries":["needle"],"path":"MEMORY.md"}`},
		{name: "search nulls every optional", tool: "memories_search",
			args: `{"queries":["needle"],"path":null}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := handlers[test.tool](context.Background(), test.args)
			if err != nil {
				t.Fatalf("%s rejected %s: %v", test.tool, test.args, err)
			}
			switch response := result.(type) {
			case memory.ListResponse:
				// The project root always has a synthetic "global" entry
				// alongside its real files: the global scope is reachable
				// even before anything has been written there.
				if len(response.Entries) != 3 || response.Entries[0].Path != "MEMORY.md" || response.Entries[1].Path != "global" {
					t.Fatalf("entries = %#v", response.Entries)
				}
			case memory.ReadResponse:
				if response.Content != "alpha\nneedle\n" {
					t.Fatalf("content = %q", response.Content)
				}
			case memory.SearchResponse:
				if len(response.Matches) != 1 || response.Matches[0].Path != "MEMORY.md" {
					t.Fatalf("matches = %#v", response.Matches)
				}
			default:
				t.Fatalf("response = %#v", result)
			}
		})
	}
}

func asStringSlice(t *testing.T, value any) []string {
	t.Helper()
	raw, ok := value.([]any)
	if !ok && value != nil {
		t.Fatalf("required = %#v, want a list", value)
	}
	names := make([]string, 0, len(raw))
	for _, item := range raw {
		name, ok := item.(string)
		if !ok {
			t.Fatalf("required entry = %#v, want a string", item)
		}
		names = append(names, name)
	}
	return names
}

type anyTool struct {
	handle func(context.Context, string) (any, error)
}

func TestTypedSubagentPolicyBlocksExploreWrites(t *testing.T) {
	st := NewState()
	ctx := WithSubagentType(context.Background(), "explore")
	err := st.GuardTool(ctx, "write_file")
	if err == nil || !strings.Contains(err.Error(), "blocked by policy") {
		t.Fatalf("expected explore write_file blocked, got %v", err)
	}
}

func TestTypedSubagentPolicyAllowsExploreShellUnderReadOnlyProfile(t *testing.T) {
	st := NewState()
	ctx := WithSubagentType(context.Background(), "explore")
	if err := st.GuardTool(ctx, "shell"); err != nil {
		t.Fatalf("expected explore shell allowed by capability policy: %v", err)
	}
}

func TestTypedSubagentPolicyAllowsPlanShellUnderReadOnlyProfile(t *testing.T) {
	st := NewState()
	ctx := WithSubagentType(context.Background(), "plan")
	if err := st.GuardTool(ctx, "shell"); err != nil {
		t.Fatalf("expected plan shell allowed by capability policy: %v", err)
	}
}

func TestTypedSubagentPolicyAllowsVerificationShell(t *testing.T) {
	st := NewState()
	ctx := WithSubagentType(context.Background(), "verification")
	if err := st.GuardTool(ctx, "shell"); err != nil {
		t.Fatalf("expected verification shell allowed: %v", err)
	}
}

func TestTypedSubagentPolicyBlocksExploreSubagentLifecycleTools(t *testing.T) {
	st := NewState()
	ctx := WithSubagentType(context.Background(), "explore")
	for _, tool := range []string{"subagent_send", "subagent_wait", "subagent_status", "subagent_list", "subagent_close"} {
		err := st.GuardTool(ctx, tool)
		if err == nil || !strings.Contains(err.Error(), "blocked by policy") {
			t.Fatalf("expected explore %s blocked, got %v", tool, err)
		}
	}
}

func TestIntermediateToolAllowedInPlanMode(t *testing.T) {
	st := NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "intermediate_tool", ReadOnly: false})
	ctx := WithMode(context.Background(), "plan")
	if err := st.GuardTool(ctx, "intermediate_tool"); err != nil {
		t.Fatalf("GuardTool plan intermediate_tool: %v", err)
	}
}

func TestIntermediateToolAllowedForExploreSubagent(t *testing.T) {
	st := NewState(t.TempDir())
	ctx := WithSubagentType(context.Background(), "explore")
	if err := st.GuardTool(ctx, "intermediate_tool"); err != nil {
		t.Fatalf("GuardTool explore intermediate_tool: %v", err)
	}
}

func TestNetworkSessionDecisionsAreIsolatedBySessionAndTarget(t *testing.T) {
	state := NewState(t.TempDir())
	context := safety.NetworkApprovalContext{Host: "API.Example.COM.", Protocol: safety.NetworkApprovalHTTPS}
	state.SetNetworkSessionDecision("session-a", context, 443, true)
	rules := state.NetworkSessionRules("session-a")
	if len(rules) != 1 || !rules[0].Allow || rules[0].Context.Host != "api.example.com" || rules[0].Port != 443 {
		t.Fatalf("rules=%+v", rules)
	}
	if other := state.NetworkSessionRules("session-b"); len(other) != 0 {
		t.Fatalf("session decision leaked: %+v", other)
	}
	state.SetNetworkSessionDecision("session-a", context, 443, false)
	rules = state.NetworkSessionRules("session-a")
	if len(rules) != 1 || rules[0].Allow {
		t.Fatalf("deny did not replace allow: %+v", rules)
	}
}

func TestParseFilterTOMLDockerBuild(t *testing.T) {
	docs := BuiltinFilterDocs()
	doc, ok := docs["docker-build.toml"]
	if !ok {
		t.Fatal("docker-build.toml missing from embedded filters")
	}
	f, err := CompileFilter(doc, "builtin")
	if err != nil {
		t.Fatalf("compile docker-build: %v", err)
	}
	if f.Name != "docker-build" {
		t.Fatalf("name = %q, want docker-build", f.Name)
	}
	if f.Version != "2" {
		t.Fatalf("version = %q, want 2", f.Version)
	}
	if !f.StripANSI {
		t.Fatal("strip_ansi should be true")
	}
	if len(f.MatchOutputSelect) != 4 {
		t.Fatalf("match_output_select len = %d, want 4", len(f.MatchOutputSelect))
	}
	if len(f.Replace) != 1 {
		t.Fatalf("replace len = %d, want 1", len(f.Replace))
	}
	if len(f.Tests) < 2 {
		t.Fatalf("tests len = %d, want >= 2", len(f.Tests))
	}
}

func TestAllEmbeddedFiltersCompile(t *testing.T) {
	docs := BuiltinFilterDocs()
	if len(docs) < 110 {
		t.Fatalf("embedded filter count = %d, want >= 110", len(docs))
	}
	var failed []string
	for name, doc := range docs {
		if _, err := CompileFilter(doc, "builtin"); err != nil {
			failed = append(failed, name+": "+err.Error())
		}
	}
	if len(failed) > 0 {
		t.Fatalf("filters failed to compile:\n%s", strings.Join(failed, "\n"))
	}
}

// TestEmbeddedTestVectors runs every [[tests.*]] vector shipped inside the
// carved Boost filters through the filter pipeline and checks the expected
// output. Vectors target f.apply directly (the transformation core); the
// command gate is covered by TestEngineFailOpenAndPassthrough and the
// end-to-end tests.
func TestEmbeddedTestVectors(t *testing.T) {
	// Filters whose carved TOML lost content during binary extraction and whose
	// remaining vectors no longer reflect the original rules.
	skip := map[string]bool{
		"tofu-validate": true, // selector list truncated by carving; comment documents the lost gate
	}
	docs := BuiltinFilterDocs()
	var failures []string
	total, run := 0, 0
	for name, doc := range docs {
		f, err := CompileFilter(doc, "builtin")
		if err != nil || skip[f.Name] {
			continue
		}
		for _, tv := range f.Tests {
			if !tv.HasExpected {
				continue
			}
			total++
			// Vectors without expect_match_output exercise the transformation
			// pipeline directly (gate bypassed), mirroring how Boost's embedded
			// tests target a single filter. expect_match_output vectors
			// explicitly exercise the selection gate.
			bypass := tv.ExpectMatchOutput == nil
			got, _, selMiss := f.apply(tv.Input, bypass)
			run++
			if tv.ExpectMatchOutput != nil && !*tv.ExpectMatchOutput {
				// negative selection test: output must pass through unchanged
				if !selMiss || got != tv.Input {
					failures = append(failures, name+"/"+tv.Name+": expected passthrough (selection miss)")
				}
				continue
			}
			// Compare on trimmed content: the line keep/drop decisions are the
			// token-saving behavior under test. Leading/trailing blank-line
			// conventions are a serialization detail that varies with how the
			// corpus was carved from the binary.
			if strings.TrimSpace(got) != strings.TrimSpace(tv.Expected) {
				failures = append(failures, name+"/"+tv.Name)
				if len(failures) <= 3 {
					t.Logf("vector %s/%s:\n--- got ---\n%q\n--- want ---\n%q", name, tv.Name, got, tv.Expected)
				}
			}
		}
	}
	t.Logf("embedded test vectors: %d run of %d total", run, total)
	if len(failures) > 0 {
		t.Fatalf("%d/%d vectors failed:\n%s", len(failures), run, strings.Join(failures, "\n"))
	}
}

func TestEngineFailOpenAndPassthrough(t *testing.T) {
	e, errs := DefaultEngine()
	if e == nil {
		t.Fatalf("nil default engine: %v", errs)
	}
	// Unknown command passes through untouched.
	res := e.Apply("some-unknown-tool --flag", "line1\nline2\n")
	if res.Matched || res.Output != "line1\nline2\n" {
		t.Fatalf("unknown command should pass through, got %+v", res)
	}
}

func TestCompressorEndToEnd(t *testing.T) {
	dir := t.TempDir()
	ResetCompressors()
	defer ResetCompressors()
	c := CompressorFor(dir)
	if c == nil || c.engine == nil {
		t.Fatal("compressor not constructed")
	}
	// Simulate a noisy make run that the make filter collapses.
	var b strings.Builder
	b.WriteString("make[1]: Entering directory '/home/user/app'\n")
	for i := 0; i < 400; i++ {
		b.WriteString("make[2]: progress step xxxxxxxxxxxxxxxxxxxx [ OK ]\n")
	}
	b.WriteString("ERROR: src/api.c:42:18: error: expected ';' before '}' token\n")
	b.WriteString("make[1]: Leaving directory '/home/user/app'\n")
	stdout := b.String()

	out, meta := c.CompressShellOutput("make", stdout, "sess-1")
	if !meta.Applied {
		t.Fatalf("make output should be compressed, meta=%+v", meta)
	}
	if !strings.Contains(out, "ERROR: src/api.c:42:18") {
		t.Fatalf("error line must survive compression:\n%s", out)
	}
	if strings.Contains(out, "progress step") {
		t.Fatalf("progress noise must be stripped:\n%s", out)
	}
	if meta.RetrieveID == 0 {
		t.Fatalf("significant compression must record a retrieve id, meta=%+v", meta)
	}
	if !strings.Contains(out, "retrieve_output") {
		t.Fatalf("marker missing:\n%s", out)
	}
	if meta.SavedTokens <= 0 || meta.CompressedPct < 90 {
		t.Fatalf("expected ~99%% savings, got %+v", meta)
	}
	// Retrieve the original back.
	store := c.Store()
	if store == nil {
		t.Fatal("store nil")
	}
	entry, err := store.Get(meta.RetrieveID)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if !strings.Contains(entry.OriginalOutput, "progress step") {
		t.Fatal("stored original lost noise lines")
	}
	if err := store.RecordRetrieve(meta.RetrieveID, "needed full log"); err != nil {
		t.Fatalf("record retrieve: %v", err)
	}
	// BM25 search inside the cached original.
	hits := BM25Rank("ERROR expected", entry.OriginalOutput, 3)
	if len(hits) == 0 {
		t.Fatal("bm25 found nothing")
	}
	found := false
	for _, h := range hits {
		if strings.Contains(h.Line, "ERROR: src/api.c") {
			found = true
		}
	}
	if !found {
		t.Fatalf("bm25 did not surface the error line: %+v", hits)
	}
	// Stats aggregate the savings.
	st, err := store.StatsSince(time.Time{})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.SavedTokens <= 0 || st.TotalCommands != 1 {
		t.Fatalf("stats unexpected: %+v", st)
	}
}

func TestCompressorSmallOutputUntouched(t *testing.T) {
	dir := t.TempDir()
	ResetCompressors()
	defer ResetCompressors()
	c := CompressorFor(dir)
	out, meta := c.CompressShellOutput("make", "gcc -O2 -c main.c\n", "s")
	if meta.Applied || out != "gcc -O2 -c main.c\n" {
		t.Fatalf("small output must pass through, meta=%+v", meta)
	}
}

func TestRedaction(t *testing.T) {
	awsKey := "AKIA" + "ABCDEFGHIJKLMNOP" // assembled so the fixture is not itself a scannable key
	in := "deploying with ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ123456\n" + awsKey + " also"
	out := Redact(in)
	if strings.Contains(out, "ghp_") || strings.Contains(out, awsKey) {
		t.Fatalf("secrets not redacted: %s", out)
	}
	if !strings.Contains(out, RedactedMarker) {
		t.Fatalf("redaction marker missing: %s", out)
	}
}

func TestCustomFilterDir(t *testing.T) {
	dir := t.TempDir()
	filtersDir := filepath.Join(dir, CustomFilterRelDir)
	if err := os.MkdirAll(filtersDir, 0o755); err != nil {
		t.Fatal(err)
	}
	custom := `
[filters.deploy]
description = "Keep failures from deploy.sh"
match_command = "^deploy\\.sh\\b"
keep_lines_matching = ["^(ERROR|WARN)"]
on_empty = "deploy: ok"
`
	if err := os.WriteFile(filepath.Join(filtersDir, "deploy.toml"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	ResetCompressors()
	defer ResetCompressors()
	c := CompressorFor(dir)
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("==> progress noise line\n")
	}
	b.WriteString("ERROR: pod api failed readiness\n")
	out, meta := c.CompressShellOutput("deploy.sh staging", b.String(), "s")
	if !meta.Applied || meta.Filter != "deploy" {
		t.Fatalf("custom filter not applied: %+v", meta)
	}
	if !strings.Contains(out, "ERROR: pod api failed readiness") || strings.Contains(out, "progress noise") {
		t.Fatalf("unexpected custom-filter output:\n%s", out)
	}
}

func TestOutputGovernorGovernDetailedSpillsAndAnnotates(t *testing.T) {
	t.Parallel()
	g := NewOutputGovernor(t.TempDir())
	out := g.GovernDetailed("shell", "call-1", strings.Repeat("x\n", SpillThresholdBytes))
	if !out.Truncated {
		t.Fatal("expected truncation")
	}
	if out.StoredPath == "" {
		t.Fatal("expected spill path")
	}
	if !strings.Contains(out.Text, "[tool output truncated for context:") {
		t.Fatalf("missing truncation marker: %q", out.Text)
	}
	if !strings.Contains(out.Text, "full output available via read_file:") {
		t.Fatalf("missing read_file hint: %q", out.Text)
	}
}

// FOREBRAIN_HOME is asked about, never refused: the user has to be able to say yes
// to a change or a look at their own settings. The active workspace is already
// reachable by its own route and is not asked about again.
func TestPrimaryHomeAccessIsApprovedRatherThanRefused(t *testing.T) {
	home := t.TempDir()
	mainWS := filepath.Join(home, "workspace")
	target := filepath.Join(home, "forebrain.yaml")
	st := NewState(mainWS)
	st.SetPrimaryWorkspaceBoundary(home, mainWS, nil)
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		return "", false, nil
	})

	abs, err := resolveWriteFilePath(context.Background(), st, target)
	if err != nil {
		t.Fatalf("FOREBRAIN_HOME write was refused instead of asked about: %v", err)
	}
	if st.ProtectedWriteReason(context.Background(), abs) == "" {
		t.Fatalf("FOREBRAIN_HOME write would proceed without asking: %s", abs)
	}

	resolution, readErr := resolveReadFilePath(context.Background(), st, target)
	if readErr != nil {
		t.Fatalf("FOREBRAIN_HOME read was refused instead of asked about: %v", readErr)
	}
	if st.ProtectedReadReason(resolution.Abs) == "" {
		t.Fatalf("FOREBRAIN_HOME read would proceed without asking: %s", resolution.Abs)
	}
	// The active workspace is reachable by its own route and is not asked about.
	if reason := st.ProtectedReadReason(filepath.Join(mainWS, "state", "note.md")); reason != "" {
		t.Fatalf("active workspace read would ask for approval: %q", reason)
	}

	// Once the write is approved, the read it owes is not a second question.
	st.RememberApprovedWrite(abs)
	if reason := st.ProtectedReadReason(abs); reason != "" {
		t.Fatalf("read of an already-approved write target asks again: %q", reason)
	}
	// Only that file: a sibling under the home is still its own decision.
	if st.ProtectedReadReason(filepath.Join(home, "auth.json")) == "" {
		t.Fatal("approving one write opened the rest of FOREBRAIN_HOME to reads")
	}
}

// An approval belongs to the agent it was given to and must not follow the
// runtime into another primary agent's workspace.
func TestApprovedWritesDoNotSurviveAPrimaryAgentSwitch(t *testing.T) {
	home := t.TempDir()
	mainWS := filepath.Join(home, "workspace")
	reviewWS := filepath.Join(home, "workspaces", "review")
	target := filepath.Join(home, "forebrain.yaml")
	st := NewState(mainWS)
	st.SetPrimaryWorkspaceBoundary(home, mainWS, []string{reviewWS})
	st.RememberApprovedWrite(target)
	if !st.ApprovedWrite(target) {
		t.Fatal("approved write was not recorded")
	}

	st.SetPrimaryWorkspaceBoundary(home, reviewWS, []string{mainWS})
	if st.ApprovedWrite(target) {
		t.Fatal("approved write followed the runtime into another primary agent's workspace")
	}
}

func TestResolveWithinRootsSiblingPrimaryWorkspaceIsNotApprovable(t *testing.T) {
	home := t.TempDir()
	mainWS := filepath.Join(home, "workspace")
	reviewWS := filepath.Join(home, "workspaces", "review")
	target := filepath.Join(mainWS, "private.txt")
	st := NewState(reviewWS)
	st.SetPrimaryWorkspaceBoundary(home, reviewWS, []string{mainWS})
	called := false
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		called = true
		return "unexpected", true, nil
	})

	_, err := resolveWriteFilePath(context.Background(), st, target)
	if err == nil || !errors.Is(err, ErrPathNotAllowed) {
		t.Fatalf("expected hard ErrPathNotAllowed, got %v", err)
	}
	var req *RequiresActionError
	if errors.As(err, &req) {
		t.Fatalf("sibling workspace returned approvable action: %+v", req)
	}
	if called {
		t.Fatal("sibling workspace path invoked approval hook")
	}
}

// A path outside the allowed roots is no longer refused by the file tools
// themselves. It resolves, and the approval layer decides whether the write
// happens — the same route apply_patch and shell already take.
func TestResolveWriteFilePathOutsideRootsResolvesForApproval(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outsideDir := t.TempDir()
	target := filepath.Join(outsideDir, "demo.txt")
	st := NewState(root)
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		t.Error("path resolution must not raise an approval action of its own")
		return "", false, nil
	})
	got, err := resolveWriteFilePath(context.Background(), st, target)
	if err != nil {
		t.Fatalf("outside path must resolve for the approval layer: %v", err)
	}
	if got != target {
		t.Fatalf("resolved path = %q, want %q", got, target)
	}
}

// A subagent's call is a question for the operator like any other: a child run
// suspends on the same action queue, so the path resolves here and the approval
// layer decides.
func TestResolveWriteFilePathOutsideRootsResolvesForSubagent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "demo.txt")
	st := NewState(root)
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		return "", false, nil
	})
	for name, ctx := range map[string]context.Context{
		"typed subagent": WithSubagentType(context.Background(), "general-purpose"),
		"fork child":     WithForkChild(context.Background(), true),
	} {
		got, err := resolveWriteFilePath(ctx, st, target)
		if err != nil {
			t.Fatalf("%s: outside path must resolve for the approval layer: %v", name, err)
		}
		if got != target {
			t.Fatalf("%s: resolved path = %q, want %q", name, got, target)
		}
	}
}

// A request_permissions deny entry outranks the relaxed boundary.
func TestResolveWriteFilePathHonorsDenyGrant(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	target := filepath.Join(outside, "demo.txt")
	store := safety.NewStore()
	safety.ApplyUpdate(store, safety.PermissionUpdate{
		Type: safety.UpdateAddPermissionGrants, Destination: safety.DestinationSession, SessionID: "session-1",
		FileSystemGrants: []safety.FileSystemPermissionGrant{{
			Entry: safety.FileSystemPermissionEntry{
				Path:   safety.FileSystemPermissionPath{Path: outside},
				Access: safety.FileSystemAccessDeny,
			},
			Scope: safety.GrantScopeSession,
		}},
	})
	st := NewState(t.TempDir())
	st.SetRuntimeValue("permission_snapshot_for_session", store.SnapshotForSession)
	_, err := resolveWriteFilePath(llm.WithAgentSessionID(context.Background(), "session-1"), st, target)
	if err == nil || !errors.Is(err, ErrPathNotAllowed) {
		t.Fatalf("expected ErrPathNotAllowed, got %v", err)
	}
	if !strings.Contains(err.Error(), "denied by request_permissions grant") {
		t.Fatalf("unexpected error %q", err.Error())
	}
}

// A grant is honoured even where no approval hook could be consulted —
// including a turn-scoped one, which never reaches PermissionRoots.
func TestResolveWriteFilePathHonorsTurnGrantWithoutApprovalHook(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	target := filepath.Join(outside, "demo.txt")
	store := safety.NewStore()
	safety.ApplyUpdate(store, safety.PermissionUpdate{
		Type: safety.UpdateAddPermissionGrants, Destination: safety.DestinationSession, SessionID: "session-1",
		FileSystemGrants: []safety.FileSystemPermissionGrant{{
			Entry: safety.FileSystemPermissionEntry{
				Path:   safety.FileSystemPermissionPath{Path: outside},
				Access: safety.FileSystemAccessWrite,
			},
			Scope: safety.GrantScopeTurn,
			RunID: "run-1",
		}},
	})
	st := NewState(t.TempDir())
	st.SetRuntimeValue("permission_snapshot_for_session", store.SnapshotForSession)
	ctx := llm.WithAgentSessionID(WithRunID(context.Background(), "run-1"), "session-1")

	got, err := resolveWriteFilePath(ctx, st, target)
	if err != nil || got != target {
		t.Fatalf("granted path = %q err = %v", got, err)
	}
	// The grant is bound to run-1; another run must not inherit it, and with no
	// approval hook there is no one left to ask.
	later := llm.WithAgentSessionID(WithRunID(context.Background(), "run-2"), "session-1")
	if _, err := resolveWriteFilePath(later, st, target); err == nil || !errors.Is(err, ErrPathNotAllowed) {
		t.Fatalf("turn grant must not outlive its run, got %v", err)
	}
}

// Without an approval hook there is no one to authorize an out-of-root write,
// so it is refused rather than performed unattended.
func TestResolveWriteFilePathOutsideRootsRefusedWithoutApprovalHook(t *testing.T) {
	t.Parallel()
	st := NewState(t.TempDir())
	target := filepath.Join(t.TempDir(), "demo.txt")
	if _, err := resolveWriteFilePath(context.Background(), st, target); err == nil || !errors.Is(err, ErrPathNotAllowed) {
		t.Fatalf("expected ErrPathNotAllowed, got %v", err)
	}
	// A read has no such gate: deny_read is the boundary that governs reads.
	if _, err := resolveReadFilePath(context.Background(), st, target); err != nil {
		t.Fatalf("read must resolve: %v", err)
	}
	// An approved call may proceed.
	if _, err := resolveWriteFilePath(WithPolicyApproved(context.Background(), true), st, target); err != nil {
		t.Fatalf("approved write must resolve: %v", err)
	}
}

// A confined State (memory consolidation) must resolve bare file names inside
// its own root. Before confinement these resolved against the process working
// directory, which is how consolidation wrote MEMORY.md into whatever repo the
// CLI was started in.
func TestResolveFileToolPathConfinedRootIgnoresProcessWorkingDirectory(t *testing.T) {
	memoryRoot := t.TempDir()
	t.Chdir(t.TempDir())
	st := NewState(memoryRoot)
	st.ConfineToRoot(memoryRoot)

	// ConfineToRoot stores the root with symlinks resolved (/var -> /private/var
	// on macOS), so compare against what it settled on.
	root := st.ConfinedRoot()
	for _, requested := range []string{"MEMORY.md", "skills/sandbox-go-build/SKILL.md"} {
		abs, err := resolveWriteFilePath(context.Background(), st, requested)
		if err != nil {
			t.Fatalf("resolve %q: %v", requested, err)
		}
		if want := filepath.Join(root, requested); abs != want {
			t.Fatalf("resolved %q to %q, want %q", requested, abs, want)
		}
	}
}

// Confinement is a hard boundary, not an approval prompt: the consolidation run
// is policy-approved and has no action hook, so a path outside the root has to
// be refused rather than written unattended.
func TestResolveFileToolPathConfinedRootRefusesOutsidePaths(t *testing.T) {
	memoryRoot := t.TempDir()
	outside := t.TempDir()
	st := NewState(memoryRoot)
	st.ConfineToRoot(memoryRoot)
	ctx := WithPolicyApproved(context.Background(), true)

	for _, requested := range []string{
		filepath.Join(outside, "MEMORY.md"),
		filepath.Join("..", filepath.Base(outside), "MEMORY.md"),
	} {
		abs, err := resolveWriteFilePath(ctx, st, requested)
		if err == nil {
			t.Fatalf("resolved %q to %q, want refusal", requested, abs)
		}
		if !errors.Is(err, ErrPathNotAllowed) {
			t.Fatalf("resolve %q: err=%v, want ErrPathNotAllowed", requested, err)
		}
	}
}

// Reads obey the same boundary: a confined agent cannot read its way out of the
// folder it owns.
func TestResolveFileToolPathConfinedRootAppliesToReads(t *testing.T) {
	memoryRoot := t.TempDir()
	outside := t.TempDir()
	st := NewState(memoryRoot)
	st.ConfineToRoot(memoryRoot)

	if _, err := resolveReadFilePath(context.Background(), st, filepath.Join(outside, "secrets.md")); !errors.Is(err, ErrPathNotAllowed) {
		t.Fatalf("read outside confined root: err=%v, want ErrPathNotAllowed", err)
	}
	resolution, err := resolveReadFilePath(context.Background(), st, "raw_memories.md")
	if err != nil {
		t.Fatalf("read inside confined root: %v", err)
	}
	if want := filepath.Join(st.ConfinedRoot(), "raw_memories.md"); resolution.Abs != want {
		t.Fatalf("resolved to %q, want %q", resolution.Abs, want)
	}
}

// End to end through write_file with the exact setup the memory consolidation
// runner uses: confined state, policy-approved context, no action hook. The
// artifact must appear in the confined root, and the working directory must
// stay untouched.
func TestWriteFileConfinedRootLandsInRootNotWorkingDirectory(t *testing.T) {
	memoryRoot := t.TempDir()
	workdir := t.TempDir()
	t.Chdir(workdir)
	st := NewState(memoryRoot)
	st.ConfineToRoot(memoryRoot)
	tool, err := NewFileWriteTool(st, &AgentToolRuntime{WorkspaceRoot: memoryRoot, YOLO: true})
	if err != nil {
		t.Fatalf("NewFileWriteTool: %v", err)
	}
	ctx := WithPolicyApproved(context.Background(), true)

	if _, err := tool.Handle(ctx, `{"file_path":"MEMORY.md","content":"# handbook\n"}`); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(st.ConfinedRoot(), "MEMORY.md")); err != nil {
		t.Fatalf("artifact missing from memory root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, "MEMORY.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("write_file polluted the working directory: err=%v", err)
	}
}

// planModeWriteCtx builds a context that mimics what wrapPlanModeLLM sets up for
// a tool call during plan mode: mode=plan, a bound session, and the allowed plan
// path pointing at that conversation's plan directory.
func planModeWriteCtx(home, sid string) context.Context {
	ctx := context.Background()
	ctx = llm.WithAgentSessionID(ctx, sid)
	ctx = WithMode(ctx, "plan")
	ctx = WithAllowedPlanPath(ctx, state.PlanDirForSession(home, "", sid))
	return ctx
}

func TestPlanModeWriteFileAllowsPlanFile(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	sid := "sid-plan-write"
	planFile := state.PlanPathForSession(home, "", sid)

	st := NewState(workspace)
	tool, err := NewFileWriteTool(st, &AgentToolRuntime{Home: home})
	if err != nil {
		t.Fatalf("NewFileWriteTool: %v", err)
	}

	args, _ := json.Marshal(FileWriteInput{FilePath: planFile, Content: "# Plan\n\nstep one\n"})
	if _, err := tool.Handle(planModeWriteCtx(home, sid), string(args)); err != nil {
		t.Fatalf("plan-file write should succeed in plan mode, got: %v", err)
	}
	got, err := os.ReadFile(planFile)
	if err != nil {
		t.Fatalf("read plan file: %v", err)
	}
	if !strings.Contains(string(got), "step one") {
		t.Fatalf("plan file content not written, got %q", got)
	}
}

// The real plan directory lives under the agent state root
// (~/.forebrain/workspace/plans/<project>), so every plan path carries a ".forebrain"
// segment — a protected metadata directory name. Reproduce that layout: a plan
// write must still be allowed, while the rest of the state root stays
// protected.
func TestPlanModeWriteFileAllowsPlanFileUnderStateRoot(t *testing.T) {
	stateRoot := filepath.Join(t.TempDir(), ".forebrain", "workspace")
	workspace := t.TempDir()
	sid := "sid-plan-state-root"
	planDir := state.PlanDirForSession(stateRoot, "forebrain", sid)
	planFile := filepath.Join(planDir, "remove-cron-semantics.md")

	ctx := llm.WithAgentSessionID(context.Background(), sid)
	ctx = WithMode(ctx, "plan")
	ctx = WithAllowedPlanPath(ctx, planDir)

	st := NewState(workspace)
	tool, err := NewFileWriteTool(st, &AgentToolRuntime{Home: stateRoot})
	if err != nil {
		t.Fatalf("NewFileWriteTool: %v", err)
	}

	args, _ := json.Marshal(FileWriteInput{FilePath: planFile, Content: "# Plan\n\nstep one\n"})
	if _, err := tool.Handle(ctx, string(args)); err != nil {
		t.Fatalf("plan-file write under the state root should succeed, got: %v", err)
	}
	got, err := os.ReadFile(planFile)
	if err != nil {
		t.Fatalf("read plan file: %v", err)
	}
	if !strings.Contains(string(got), "step one") {
		t.Fatalf("plan file content not written, got %q", got)
	}

	// Sibling state under the same root is not the plan region: still blocked.
	other := filepath.Join(stateRoot, "state", "permissions.json")
	if err := st.GuardWrite(ctx, other); err == nil {
		t.Fatalf("non-plan state path was writable: %s", other)
	}
}

// TestPlanModeWriteFileStaysInOwnSessionPlanDir pins the isolation the
// per-conversation plan directory buys: a session in plan mode may write only
// inside its own directory — never another conversation's, never the flat
// project directory legacy plans live in.
func TestPlanModeWriteFileStaysInOwnSessionPlanDir(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()

	st := NewState(workspace)
	tool, err := NewFileWriteTool(st, &AgentToolRuntime{Home: home})
	if err != nil {
		t.Fatalf("NewFileWriteTool: %v", err)
	}
	ctx := planModeWriteCtx(home, "sid-a")

	mine := filepath.Join(state.PlanDirForSession(home, "", "sid-a"), "mine.md")
	args, _ := json.Marshal(FileWriteInput{FilePath: mine, Content: "# Mine\n"})
	if _, err := tool.Handle(ctx, string(args)); err != nil {
		t.Fatalf("own-session plan write should succeed, got: %v", err)
	}

	for _, target := range []string{
		filepath.Join(state.PlanDirForSession(home, "", "sid-b"), "theirs.md"),
		filepath.Join(state.PlanDirForProject(home, ""), "flat.md"),
	} {
		args, _ := json.Marshal(FileWriteInput{FilePath: target, Content: "# Not mine\n"})
		if _, err := tool.Handle(ctx, string(args)); err == nil {
			t.Fatalf("write outside the session's plan directory should fail: %s", target)
		}
		if _, statErr := os.Stat(target); statErr == nil {
			t.Fatalf("file must not exist after a rejected write: %s", target)
		}
	}
}

func TestPlanModeWriteFileBlocksNonPlanPath(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	sid := "sid-plan-block"

	// A project file inside the workspace root: resolves fine, but GuardWrite
	// must reject it in plan mode.
	target := filepath.Join(workspace, "main.go")
	if err := os.WriteFile(target, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("seed project file: %v", err)
	}

	st := NewState(workspace)
	// No ActionHook is set, so if the code reached the approval prompt this
	// test would still fail on the GuardWrite error — confirming the write is
	// rejected by policy, not merely deferred to a prompt.
	tool, err := NewFileWriteTool(st, &AgentToolRuntime{Home: home})
	if err != nil {
		t.Fatalf("NewFileWriteTool: %v", err)
	}

	args, _ := json.Marshal(FileWriteInput{FilePath: target, Content: "package main // tampered\n"})
	_, err = tool.Handle(planModeWriteCtx(home, sid), string(args))
	if err == nil {
		t.Fatal("non-plan write must be blocked in plan mode")
	}
	if !strings.Contains(err.Error(), "plan mode") {
		t.Fatalf("expected GuardWrite plan-mode rejection, got: %v", err)
	}
	// The project file must be untouched.
	got, _ := os.ReadFile(target)
	if strings.Contains(string(got), "tampered") {
		t.Fatalf("project file was modified in plan mode: %q", got)
	}
}

func TestPlanModeReadOnlySubagentCannotWritePlanFile(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	sid := "sid-plan-subagent"
	planFile := state.PlanPathForSession(home, "", sid)

	st := NewState(workspace)
	tool, err := NewFileWriteTool(st, &AgentToolRuntime{Home: home})
	if err != nil {
		t.Fatalf("NewFileWriteTool: %v", err)
	}

	// An explore subagent is read-only and must not write any file, including
	// the plan file — GuardTool's typed-subagent guard still applies.
	ctx := WithSubagentType(planModeWriteCtx(home, sid), "explore")
	args, _ := json.Marshal(FileWriteInput{FilePath: planFile, Content: "# Plan\n"})
	_, err = tool.Handle(ctx, string(args))
	if err == nil {
		t.Fatal("read-only subagent must not write the plan file")
	}
	if !strings.Contains(err.Error(), "blocked by policy") {
		t.Fatalf("expected typed-subagent policy block, got: %v", err)
	}
	if _, statErr := os.Stat(planFile); statErr == nil {
		t.Fatal("plan file should not have been created by a read-only subagent")
	}
}

func TestPlanModeEditFileAllowsPlanFileBlocksOthers(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	sid := "sid-plan-edit"
	planFile := state.PlanPathForSession(home, "", sid)

	// Seed an existing plan file and register a read state so edit_file's
	// read-before-edit precondition is satisfied.
	if err := os.MkdirAll(filepath.Dir(planFile), 0o755); err != nil {
		t.Fatalf("mkdir plan dir: %v", err)
	}
	if err := os.WriteFile(planFile, []byte("# Plan\n\nold line\n"), 0o600); err != nil {
		t.Fatalf("seed plan file: %v", err)
	}
	fi, err := os.Stat(planFile)
	if err != nil {
		t.Fatalf("stat plan file: %v", err)
	}
	raw, _ := os.ReadFile(planFile)

	st := NewState(workspace)
	st.RememberRead(planFile, fi.ModTime(), fi.Size(), raw)
	tool, err := NewFileEditTool(st)
	if err != nil {
		t.Fatalf("NewFileEditTool: %v", err)
	}

	args, _ := json.Marshal(FileEditInput{FilePath: planFile, OldString: "old line", NewString: "new line"})
	if _, err := tool.Handle(planModeWriteCtx(home, sid), string(args)); err != nil {
		t.Fatalf("plan-file edit should succeed in plan mode, got: %v", err)
	}
	got, _ := os.ReadFile(planFile)
	if !strings.Contains(string(got), "new line") {
		t.Fatalf("plan file not edited, got %q", got)
	}

	// A project file edit must be blocked.
	proj := filepath.Join(workspace, "app.go")
	if err := os.WriteFile(proj, []byte("package app\n"), 0o644); err != nil {
		t.Fatalf("seed project file: %v", err)
	}
	pfi, _ := os.Stat(proj)
	praw, _ := os.ReadFile(proj)
	st.RememberRead(proj, pfi.ModTime(), pfi.Size(), praw)
	args2, _ := json.Marshal(FileEditInput{FilePath: proj, OldString: "package app", NewString: "package tampered"})
	_, err = tool.Handle(planModeWriteCtx(home, sid), string(args2))
	if err == nil {
		t.Fatal("non-plan edit must be blocked in plan mode")
	}
	if !strings.Contains(err.Error(), "plan mode") {
		t.Fatalf("expected GuardWrite plan-mode rejection, got: %v", err)
	}
}

func TestAskUserQuestionToolCreatesPendingActionAndResumesWithAnswer(t *testing.T) {
	db := openToolTestDB(t)
	if err := state.NewSessionStore(db, "main").Ensure(context.Background(), "session-1", "session-1"); err != nil {
		t.Fatal(err)
	}
	actions := &state.ActionService{DB: db}
	runs := &state.RunStore{DB: db}
	askRun, err := runs.CreateRun(context.Background(), "session-1", "ask")
	if err != nil {
		t.Fatal(err)
	}
	askTool, err := newUserInteractionTool(NewState(t.TempDir()), &AgentToolRuntime{
		Actions: actions,
		RunRT:   runs,
	})
	if err != nil {
		t.Fatalf("newUserInteractionTool: %v", err)
	}
	input := map[string]any{
		"questions": []map[string]any{{
			"header":   "q1",
			"question": "Pick one?",
			"options": []map[string]any{
				{"label": "a", "description": "A"},
				{"label": "b", "description": "B"},
			},
		}},
	}
	inputJSON := mustJSON(t, input)
	ctx := WithRunID(WithConversationSessionID(context.Background(), "session-1"), askRun.ID)
	got, err := askTool.Handle(ctx, inputJSON)
	var req *RequiresActionError
	if !errors.As(err, &req) {
		t.Fatalf("got=%#v err=%T %[2]v want RequiresActionError", got, err)
	}
	if req.ActionKind != "user_interaction" || req.ToolName != "user_interaction" || req.ActionID == "" {
		t.Fatalf("requires action mismatch: %+v", req)
	}
	rid, wait, err := runs.FindRunByAction(context.Background(), req.ActionID)
	if err != nil {
		t.Fatalf("FindRunByAction: %v", err)
	}
	if rid != askRun.ID || wait.RunID != askRun.ID || wait.ToolName != "user_interaction" || wait.ToolInputJSON == "" {
		t.Fatalf("wait mismatch: %+v", wait)
	}
	_, err = actions.AnswerAsk(context.Background(), req.ActionID, state.AskAnswer{
		Answers: []state.AskAnswerItem{{QuestionID: "q1", OptionIDs: []string{"b"}}},
	})
	if err != nil {
		t.Fatalf("AnswerAsk: %v", err)
	}
	resumeCtx := WithApprovedActionID(context.Background(), req.ActionID)
	got, err = askTool.Handle(resumeCtx, inputJSON)
	if err != nil {
		t.Fatalf("resume Handle: %v", err)
	}
	out, ok := got.(*Output)
	if !ok {
		t.Fatalf("resume result type=%T", got)
	}
	ans := out.Answers["q1"]
	if len(ans.Selections) != 1 || ans.Selections[0] != "b" {
		t.Fatalf("answer mismatch: %+v", out.Answers)
	}
}

func openToolTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestProtectedMetadataPathsRequireApproval(t *testing.T) {
	root := t.TempDir()
	state := NewState(root)
	for _, path := range []string{
		filepath.Join(root, ".agents", "rules.md"),
		filepath.Join(root, ".forebrain", "permissions.json"),
		filepath.Join(root, ".codex", "state", "config.json"),
	} {
		if !IsProtectedMetadataPath(path) {
			t.Fatalf("path not recognized as protected: %s", path)
		}
		// Protected, but never refused: the user has to be able to say yes.
		if err := state.GuardWrite(context.Background(), path); err != nil {
			t.Fatalf("protected path was refused instead of asked about: %v", err)
		}
		if state.ProtectedWriteReason(context.Background(), path) == "" {
			t.Fatalf("protected path would be written without asking: %s", path)
		}
	}
	ordinary := filepath.Join(root, "src", "main.go")
	if IsProtectedMetadataPath(ordinary) {
		t.Fatalf("ordinary path marked protected: %s", ordinary)
	}
	if err := state.GuardWrite(context.Background(), ordinary); err != nil {
		t.Fatalf("ordinary workspace write rejected: %v", err)
	}
	if reason := state.ProtectedWriteReason(context.Background(), ordinary); reason != "" {
		t.Fatalf("ordinary workspace write would ask for approval: %q", reason)
	}
}

// A home relocated with --home carries no ".forebrain" segment, so the name-based
// rule above cannot see it. It still holds the same settings, agent state and
// skill library, so GuardWrite recognizes it by its real path.
func TestRelocatedForebrainHomeRequiresApproval(t *testing.T) {
	home := filepath.Join(t.TempDir(), "forebrain-data")
	workspace := filepath.Join(home, "workspace")
	project := t.TempDir()
	state := NewState(workspace, project)
	state.SetPrimaryWorkspaceBoundary(home, workspace, nil)

	for _, path := range []string{
		filepath.Join(home, "skills", "release", "SKILL.md"),
		filepath.Join(workspace, "skills", "release", "SKILL.md"),
		filepath.Join(home, "forebrain.yaml"),
		home,
	} {
		if IsProtectedMetadataPath(path) {
			t.Fatalf("relocated home should not be recognized by name: %s", path)
		}
		if state.ProtectedWriteReason(context.Background(), path) == "" {
			t.Fatalf("relocated FOREBRAIN_HOME path would be written without asking: %s", path)
		}
	}

	ordinary := filepath.Join(project, "src", "main.go")
	if reason := state.ProtectedWriteReason(context.Background(), ordinary); reason != "" {
		t.Fatalf("project write would ask for approval: %q", reason)
	}
}

// The plan directory lives under the workspace, so the home guard must not
// swallow the one region plan mode is allowed to write.
func TestRelocatedForebrainHomeStillAllowsPlanWrites(t *testing.T) {
	home := filepath.Join(t.TempDir(), "forebrain-data")
	workspace := filepath.Join(home, "workspace")
	state := NewState(workspace)
	state.SetPrimaryWorkspaceBoundary(home, workspace, nil)

	planDir := filepath.Join(workspace, "plans", "forebrain")
	ctx := WithAllowedPlanPath(context.Background(), planDir)
	planFile := filepath.Join(planDir, "remove-cron-semantics.md")
	if err := state.GuardWrite(ctx, planFile); err != nil {
		t.Fatalf("plan write under a relocated home rejected: %v", err)
	}
	if reason := state.ProtectedWriteReason(ctx, planFile); reason != "" {
		t.Fatalf("plan write would ask for approval: %q", reason)
	}
	if state.ProtectedWriteReason(ctx, filepath.Join(workspace, "skills", "release", "SKILL.md")) == "" {
		t.Fatal("workspace skill root would be written without asking while the plan directory was open")
	}
}

// Repository metadata is ordinary project state except for the places git turns
// into commands: a hook or a config key runs on the host the next time anyone
// runs git, so those stay blocked while index locks and refs do not.
func TestGitExecutionSurfacesAreTheOnlyProtectedPartOfGit(t *testing.T) {
	root := t.TempDir()
	state := NewState(root)
	for _, path := range []string{
		filepath.Join(root, ".git", "hooks", "pre-commit"),
		filepath.Join(root, ".git", "config"),
		filepath.Join(root, ".git", "config.worktree"),
		filepath.Join(root, ".git", "modules", "sub", "hooks", "pre-push"),
		filepath.Join(root, "vendor", "dep", ".git", "hooks", "post-checkout"),
	} {
		if !IsGitExecutionSurfacePath(path) {
			t.Fatalf("path not recognized as a git execution surface: %s", path)
		}
		if state.ProtectedWriteReason(context.Background(), path) == "" {
			t.Fatalf("git execution surface would be written without asking: %s", path)
		}
	}
	for _, path := range []string{
		filepath.Join(root, ".git", "index.lock"),
		filepath.Join(root, ".git", "refs", "heads", "main"),
		filepath.Join(root, ".git", "objects", "ab", "cdef"),
		filepath.Join(root, "src", "hooks", "use_thing.go"),
		filepath.Join(root, "config", "app.yaml"),
	} {
		if IsGitExecutionSurfacePath(path) {
			t.Fatalf("ordinary path marked as a git execution surface: %s", path)
		}
		if err := state.GuardWrite(context.Background(), path); err != nil {
			t.Fatalf("ordinary workspace write rejected: %s: %v", path, err)
		}
	}
}

func TestReadFileToolEnforcesDenyRead(t *testing.T) {
	root := t.TempDir()
	secretDir := filepath.Join(root, "secret")
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	secret := filepath.Join(secretDir, "id_rsa")
	if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	st := NewState(root)
	st.SetReadPolicy([]string{secretDir}, nil, nil, root)
	tool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	_, err = tool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(secret)+`"}`)
	if err == nil {
		t.Fatalf("expected deny_read to block reading %s", secret)
	}
	if _, ok := st.GetReadState(secret); ok {
		t.Fatalf("denied file must not be remembered as read")
	}
}

func TestReadFileToolAllowReadCarvesOutDeniedSubtree(t *testing.T) {
	root := t.TempDir()
	deniedDir := filepath.Join(root, "config")
	publicDir := filepath.Join(deniedDir, "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	secret := filepath.Join(deniedDir, "secret.txt")
	allowed := filepath.Join(publicDir, "ok.txt")
	if err := os.WriteFile(secret, []byte("hidden"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if err := os.WriteFile(allowed, []byte("visible"), 0o600); err != nil {
		t.Fatalf("write allowed: %v", err)
	}

	st := NewState(root)
	st.SetReadPolicy([]string{deniedDir}, []string{publicDir}, nil, root)
	tool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	if _, err := tool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(secret)+`"}`); err == nil {
		t.Fatalf("expected denied subtree file to stay blocked")
	}
	if _, err := tool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(allowed)+`"}`); err != nil {
		t.Fatalf("expected allow_read carve-out to permit %s, got %v", allowed, err)
	}
}

func TestReadPathDeniedNoPolicyAllowsEverything(t *testing.T) {
	st := NewState(t.TempDir())
	if st.ReadPathDenied("/etc/passwd") {
		t.Fatalf("with no deny_read policy, nothing should be denied")
	}
}

func TestReadPathDeniedBlocksSymlinkIntoDeniedSubtree(t *testing.T) {
	root := t.TempDir()
	deniedDir := filepath.Join(root, "secret")
	allowedDir := filepath.Join(root, "allowed")
	if err := os.MkdirAll(deniedDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(allowedDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	secretFile := filepath.Join(deniedDir, "id_rsa")
	if err := os.WriteFile(secretFile, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(allowedDir, "sneaky_link")
	if err := os.Symlink(secretFile, link); err != nil {
		t.Skipf("symlink unsupported on this environment: %v", err)
	}

	st := NewState(root)
	st.SetReadPolicy([]string{deniedDir}, nil, nil, root)

	if !st.ReadPathDenied(link) {
		t.Fatalf("symlink %s pointing into denied subtree %s must be denied", link, deniedDir)
	}
	if !st.ReadPathDenied(secretFile) {
		t.Fatalf("direct path %s must still be denied", secretFile)
	}
}

func TestReadPathDeniedMatchesGlobAtReadTime(t *testing.T) {
	root := t.TempDir()
	st := NewState(root)
	st.SetReadPolicy(nil, nil, []string{filepath.Join(root, "**", "*.env")}, root)

	nested := filepath.Join(root, "app", ".env")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !st.ReadPathDenied(nested) {
		t.Fatalf("newly created glob match %s must be denied", nested)
	}
	if st.ReadPathDenied(filepath.Join(root, "app", "notes.txt")) {
		t.Fatal("non-matching path must remain readable")
	}
}

func TestReadPathDeniedGlobCannotBeOverriddenByReadRoot(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "app")
	secret := filepath.Join(allowed, ".env")
	st := NewState(root)
	st.SetReadPolicy(nil, []string{allowed}, []string{filepath.Join(root, "**", "*.env")}, root)

	if !st.ReadPathDenied(secret) {
		t.Fatalf("glob match %s must remain denied inside a readable root", secret)
	}
}

// A mid-run transition must reach the rest of that run's tool calls.
func TestRuntimeSessionModeAppliesWithinTheRunThatSetIt(t *testing.T) {
	st := &State{}
	sid := "sid-run-scope"
	runCtx := WithRunID(llm.WithAgentSessionID(context.Background(), sid), "run-1")
	st.SetRuntimeSessionMode(runCtx, sid, "plan", "/plans/proj")

	got := st.ContextWithRuntimeSessionMode(runCtx)
	if mode := ModeFromContext(got); mode != "plan" {
		t.Fatalf("mode=%q want plan", mode)
	}
	if plan := AllowedPlanPathFromContext(got); plan != "/plans/proj" {
		t.Fatalf("plan path=%q want /plans/proj", plan)
	}
}

// After the run ends, the mode store (read into the context at the start of the
// next run) is authoritative again: a switch made from the UI, a slash command,
// or the gateway must not be shadowed by the previous run's in-memory value —
// in either direction.
func TestRuntimeSessionModeDoesNotOutliveItsRun(t *testing.T) {
	sid := "sid-later-run"

	enteredPlan := &State{}
	enteredPlan.SetRuntimeSessionMode(WithRunID(llm.WithAgentSessionID(context.Background(), sid), "run-1"), sid, "plan", "/plans/proj")
	// Next run, session switched back to agent mode from the UI.
	nextRun := WithMode(WithRunID(llm.WithAgentSessionID(context.Background(), sid), "run-2"), "agent")
	if mode := ModeFromContext(enteredPlan.ContextWithRuntimeSessionMode(nextRun)); mode != "agent" {
		t.Fatalf("stale plan override leaked into a later run: mode=%q", mode)
	}

	exitedPlan := &State{}
	exitedPlan.SetRuntimeSessionMode(WithRunID(llm.WithAgentSessionID(context.Background(), sid), "run-1"), sid, "agent", "")
	// Next run, session switched into plan mode from the UI.
	planRun := WithAllowedPlanPath(WithMode(WithRunID(llm.WithAgentSessionID(context.Background(), sid), "run-2"), "plan"), "/plans/proj")
	resolved := exitedPlan.ContextWithRuntimeSessionMode(planRun)
	if mode := ModeFromContext(resolved); mode != "plan" {
		t.Fatalf("stale agent override disabled plan mode in a later run: mode=%q", mode)
	}
	if plan := AllowedPlanPathFromContext(resolved); plan != "/plans/proj" {
		t.Fatalf("plan path=%q want /plans/proj", plan)
	}
}

func TestToolExecutionMetadataIncludesYOLOFields(t *testing.T) {
	rt := &AgentToolRuntime{
		Cfg:  &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
		YOLO: true,
	}
	meta := toolExecutionMetadata(context.Background(), nil, rt, safety.ToolKindShell)
	if meta["sandbox_mode"] != string(safety.ModeDangerFullAccess) {
		t.Fatalf("unexpected sandbox mode metadata: %#v", meta)
	}
	if meta["yolo"] != true || meta["approval_bypassed_by_yolo"] != true {
		t.Fatalf("expected yolo metadata flags, got %#v", meta)
	}
}

// A conversation that picked its own sandbox runs its calls under it, while
// every other conversation of the same runtime keeps the configured one.
func TestToolExecutionMetadataFollowsTheConversationsOwnSandbox(t *testing.T) {
	t.Setenv(safety.EnvYOLO, "")
	rt := &AgentToolRuntime{
		Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeWorkspaceWrite},
		PermissionSnapshotForSession: func(sessionID string) safety.Snapshot {
			if sessionID == "s1" {
				return safety.Snapshot{SandboxMode: appcfg.SandboxModeDangerFullAccess}
			}
			return safety.Snapshot{}
		},
	}
	owning := toolExecutionMetadata(WithConversationSessionID(context.Background(), "s1"), nil, rt, safety.ToolKindShell)
	if owning["sandbox_mode"] != string(safety.ModeDangerFullAccess) {
		t.Fatalf("owning conversation sandbox = %#v", owning["sandbox_mode"])
	}
	other := toolExecutionMetadata(WithConversationSessionID(context.Background(), "s2"), nil, rt, safety.ToolKindShell)
	if other["sandbox_mode"] == string(safety.ModeDangerFullAccess) {
		t.Fatalf("another conversation inherited full access: %#v", other)
	}
	if rt.Cfg.SandboxMode != appcfg.SandboxModeWorkspaceWrite {
		t.Fatalf("runtime config moved to %q", rt.Cfg.SandboxMode)
	}
}

// TestRealSavings reproduces the concrete, real-world measurements quoted in
// docs/plan/TOKEN_OPTIMIZATION.md §8.4 using this package's actual compression code.
// It is a documentation aid: run with -v to see the numbers.
//
//	go test ./internal/outfilter/ -run TestRealSavings -v
func TestRealSavings(t *testing.T) {
	engine, errs := DefaultEngine()
	if engine == nil {
		t.Fatalf("nil engine: %v", errs)
	}

	// Case 1: a noisy make build (400 progress lines + 1 compile error).
	var b strings.Builder
	b.WriteString("make[1]: Entering directory '/home/user/app'\n")
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "make[%d]: progress step %d .................. [ OK ]\n", i%3+1, i)
	}
	b.WriteString("ERROR: src/api.c:42:18: error: expected ';' before '}' token\n")
	b.WriteString("make[1]: Leaving directory '/home/user/app'\n")
	makeOut := b.String()

	res := engine.Apply("make", makeOut)
	if !res.Applied || res.FilterName != "make" {
		t.Fatalf("make filter not applied: %+v", res)
	}
	if !strings.Contains(res.Output, "ERROR: src/api.c:42:18") {
		t.Fatalf("error line lost: %s", res.Output)
	}
	if strings.Contains(res.Output, "progress step") {
		t.Fatalf("progress noise survived: %s", res.Output)
	}
	t.Logf("make: %d -> %d bytes (%d%% saved), ~%d tokens kept out, filter=%s",
		res.BeforeBytes, res.AfterBytes, res.CompressedPct, res.SavedTokens, res.FilterName)
	if res.CompressedPct < 95 {
		t.Fatalf("expected ~99%% compression, got %d%%", res.CompressedPct)
	}

	// Case 2: a live `go test -json` run of this very package, if go is present.
	// Guarded by an env var so the child `go test` (which re-runs this test)
	// does not recurse.
	if os.Getenv("FOREBRAIN_OUTFILTER_NO_LIVE_DEMO") != "" {
		t.Log("live go-test-json case skipped (recursion guard)")
		return
	}
	if goBin, err := exec.LookPath("go"); err == nil {
		cmd := exec.Command(goBin, "test", "-json", "./internal/outfilter/")
		// Run from the module root (this file lives in internal/outfilter).
		if _, file, _, ok := runtime.Caller(0); ok {
			cmd.Dir = filepath.Join(filepath.Dir(file), "..", "..")
		}
		cmd.Env = append(os.Environ(), "FOREBRAIN_OUTFILTER_NO_LIVE_DEMO=1")
		var sb strings.Builder
		cmd.Stdout = &sb
		cmd.Stderr = &sb
		_ = cmd.Run()
		raw := sb.String()
		if len(raw) > 2048 {
			got := engine.Apply("go test -json ./internal/outfilter/", raw)
			if got.Applied {
				t.Logf("go test -json (live): %d -> %d bytes (%d%% saved), ~%d tokens, filter=%s",
					got.BeforeBytes, got.AfterBytes, got.CompressedPct, got.SavedTokens, got.FilterName)
				if got.CompressedPct < 50 {
					t.Fatalf("expected strong go-test-json compression, got %d%%", got.CompressedPct)
				}
			} else {
				t.Logf("go test -json (live): no filter applied (%d bytes)", len(raw))
			}
		} else {
			t.Skip("go test -json output too small to measure")
		}
	} else {
		t.Log("go binary not found; skipping live go-test-json case")
	}
}

func TestWebFetchHostAllowedByName(t *testing.T) {
	blocked := map[string]bool{
		"127.0.0.1":                true,
		"10.0.0.1":                 true,
		"192.168.1.1":              true,
		"172.16.0.1":               true,
		"example.com":              false,
		"localhost":                true,
		"metadata.google.internal": true,
		"169.254.169.254":          true,
	}
	for h, want := range blocked {
		if got := webFetchHostAllowed(h, false) != nil; got != want {
			t.Fatalf("%q: blocked=%v want %v", h, got, want)
		}
	}
}

func TestFetchURLAllowed(t *testing.T) {
	if _, err := fetchURLAllowed("https://example.com/x", false); err != nil {
		t.Fatal(err)
	}
	if _, err := fetchURLAllowed("http://127.0.0.1/", false); err == nil {
		t.Fatal("expected block")
	}
	if _, err := fetchURLAllowed("file:///etc/passwd", false); err == nil {
		t.Fatal("expected block scheme")
	}
}

func TestWebFetchIPAllowed(t *testing.T) {
	for _, tt := range []struct {
		name         string
		ip           string
		allowPrivate bool
		wantErr      string
	}{
		{name: "public", ip: "93.184.216.34"},
		{name: "public v6", ip: "2606:2800:220:1:248:1893:25c8:1946"},

		{name: "loopback", ip: "127.0.0.1", wantErr: "private or loopback"},
		{name: "loopback v6", ip: "::1", wantErr: "private or loopback"},
		{name: "loopback mapped", ip: "::ffff:127.0.0.1", wantErr: "private or loopback"},
		{name: "rfc1918", ip: "10.0.0.5", wantErr: "private or loopback"},
		{name: "unique local v6", ip: "fd00::1", wantErr: "private or loopback"},
		{name: "carrier nat", ip: "100.64.0.1", wantErr: "private or loopback"},

		{name: "loopback allowed", ip: "127.0.0.1", allowPrivate: true},
		{name: "rfc1918 allowed", ip: "10.0.0.5", allowPrivate: true},

		// Blocked regardless of allow_private_ip: these serve credentials.
		{name: "aws metadata", ip: "169.254.169.254", wantErr: "link-local"},
		{name: "aws metadata forced", ip: "169.254.169.254", allowPrivate: true, wantErr: "link-local"},
		{name: "link local v6 forced", ip: "fe80::1", allowPrivate: true, wantErr: "link-local"},
		{name: "unspecified forced", ip: "0.0.0.0", allowPrivate: true, wantErr: "unspecified"},
		{name: "multicast forced", ip: "224.0.0.1", allowPrivate: true, wantErr: "multicast"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := webFetchIPAllowed(net.ParseIP(tt.ip), tt.allowPrivate)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("%s should be allowed: %v", tt.ip, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("%s: err=%v want %q", tt.ip, err, tt.wantErr)
			}
		})
	}

	if err := webFetchIPAllowed(nil, true); err == nil {
		t.Fatalf("unresolvable address must be rejected")
	}
}

// A hostname that resolves into private space is exactly what the string-level
// check structurally cannot see, so the dial guard has to be the thing that
// catches it.
func TestWebFetchDialControlChecksResolvedAddress(t *testing.T) {
	guard := webFetchDialControl(false)
	for _, address := range []string{"127.0.0.1:8080", "10.0.0.5:80", "169.254.169.254:80", "[::1]:443"} {
		if err := guard("tcp", address, nil); err == nil {
			t.Fatalf("dial to %s should be refused", address)
		}
	}
	if err := guard("tcp", "93.184.216.34:443", nil); err != nil {
		t.Fatalf("public dial refused: %v", err)
	}

	permissive := webFetchDialControl(true)
	if err := permissive("tcp", "127.0.0.1:8080", nil); err != nil {
		t.Fatalf("loopback dial should be allowed with allow_private_ip: %v", err)
	}
	if err := permissive("tcp", "169.254.169.254:80", nil); err == nil {
		t.Fatalf("metadata endpoint must stay blocked even with allow_private_ip")
	}
}

func TestWebFetchTransportInstallsDialGuard(t *testing.T) {
	if webFetchTransport(false).DialContext == nil {
		t.Fatalf("transport must dial through the guard")
	}
}

// A proxy the operator runs on loopback — where local proxy clients listen —
// carries a fetch to a public host: the guard is for destinations, and the hop
// to the proxy is not one. A destination on loopback stays refused, proxy
// configured or not.
func TestWebFetchDialsTheOperatorsLoopbackProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A forward proxy is asked for the absolute URL of the destination.
		fmt.Fprintf(w, "proxied %s", r.URL.String())
	}))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	get := func(transport *http.Transport, target string) (string, error) {
		defer transport.CloseIdleConnections()
		resp, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Get(target)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		return string(body), err
	}

	body, err := get(newWebFetchTransport(false, http.ProxyURL(proxyURL)), "http://en.wiktionary.org/wiki/afterbrain")
	if err != nil {
		t.Fatalf("fetch through the loopback proxy failed: %v", err)
	}
	if body != "proxied http://en.wiktionary.org/wiki/afterbrain" {
		t.Fatalf("body = %q, want the proxy's answer", body)
	}

	notProxied := func(*http.Request) (*url.URL, error) { return nil, nil }
	if _, err := get(newWebFetchTransport(false, notProxied), proxy.URL); err == nil || !strings.Contains(err.Error(), "private or loopback hosts are not allowed") {
		t.Fatalf("direct loopback destination: err = %v, want it refused", err)
	}
	// With a proxy configured, a destination the proxy does not carry is
	// still dialed through the guard.
	proxyOnlyWiktionary := func(req *http.Request) (*url.URL, error) {
		if req.URL.Hostname() == "en.wiktionary.org" {
			return proxyURL, nil
		}
		return nil, nil
	}
	transport := newWebFetchTransport(false, proxyOnlyWiktionary)
	if _, err := get(transport, "http://en.wiktionary.org/"); err != nil {
		t.Fatalf("proxied fetch: %v", err)
	}
	loopbackTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer loopbackTarget.Close()
	if _, err := get(transport, loopbackTarget.URL); err == nil || !strings.Contains(err.Error(), "private or loopback hosts are not allowed") {
		t.Fatalf("unproxied loopback destination beside a proxy: err = %v, want it refused", err)
	}
}

// allow_private_ip exists for local dev servers. It must not become a way to
// reach metadata services, and the ban has to hold by name as well as by
// address: under an HTTP proxy the dial guard never sees the destination, so
// the name check is the only thing left.
func TestWebFetchMetadataHostsBlockedEvenWithPrivateAccess(t *testing.T) {
	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data/iam/security-credentials/",
		"http://metadata.google.internal/computeMetadata/v1/",
		"http://metadata/computeMetadata/v1/",
		"http://[fe80::1]/",
	} {
		if _, err := fetchURLAllowed(target, true); err == nil {
			t.Fatalf("%s must be refused even with allow_private_ip", target)
		}
	}
	// Local development servers stay reachable, which is the flag's purpose.
	if _, err := fetchURLAllowed("http://127.0.0.1:3000/", true); err != nil {
		t.Fatalf("loopback dev server should be reachable with allow_private_ip: %v", err)
	}
}

func TestCountLinesEmpty(t *testing.T) {
	if got := CountLinesBytes(nil); got != 0 {
		t.Fatalf("bytes lines=%d want 0", got)
	}
	if got := CountLinesString(""); got != 0 {
		t.Fatalf("string lines=%d want 0", got)
	}
}

func TestCountLinesTrailingNewlineConsistent(t *testing.T) {
	raw := []byte("alpha\nbeta\n")
	if got := CountLinesBytes(raw); got != 2 {
		t.Fatalf("bytes lines=%d want 2", got)
	}
	if got := CountLinesString(string(raw)); got != 2 {
		t.Fatalf("string lines=%d want 2", got)
	}
}

func TestCountLinesOnlyNewlinesIsZero(t *testing.T) {
	raw := []byte("\n\n")
	if got := CountLinesBytes(raw); got != 2 {
		t.Fatalf("bytes lines=%d want 2", got)
	}
	if got := CountLinesString(string(raw)); got != 2 {
		t.Fatalf("string lines=%d want 2", got)
	}
}

func TestCountLinesWithoutTrailingNewlineAddsLastLine(t *testing.T) {
	raw := []byte("alpha\nbeta")
	if got := CountLinesBytes(raw); got != 2 {
		t.Fatalf("bytes lines=%d want 2", got)
	}
	if got := CountLinesString(string(raw)); got != 2 {
		t.Fatalf("string lines=%d want 2", got)
	}
}

func TestStoreGetReadsSpooledOutput(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "spool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	spool := filepath.Join(dir, "command.output")
	if err := os.WriteFile(spool, []byte("full output\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := store.Save(Entry{Command: "noisy", SpoolPath: spool, OriginalBytes: 12, SpoolOmittedBytes: 3})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(entry.OriginalOutput, "full output") || !strings.Contains(entry.OriginalOutput, "exceeded the spool quota") {
		t.Fatalf("output=%q", entry.OriginalOutput)
	}
}

func TestStoreGetReportsExpiredSpoolWithoutFailing(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "spool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id, err := store.Save(Entry{
		Command: "noisy", FilteredOutput: "bounded preview", SpoolPath: filepath.Join(dir, "expired.output"),
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(entry.OriginalOutput, "bounded preview") || !strings.Contains(entry.OriginalOutput, "no longer available") {
		t.Fatalf("output=%q", entry.OriginalOutput)
	}
}

func TestStoreKindRoundTrip(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	shellID, err := store.Save(Entry{Kind: KindShell, Command: "make", OriginalOutput: "original-shell", FilteredOutput: "filtered-shell", OriginalBytes: 100, FilteredBytes: 10, SavedTokens: 22, CapabilityID: "toml:make"})
	if err != nil {
		t.Fatalf("Save shell: %v", err)
	}
	mcpID, err := store.Save(Entry{Kind: KindMCP, Command: "github/list_issues", OriginalOutput: "original-mcp", FilteredOutput: "filtered-mcp", OriginalBytes: 200, FilteredBytes: 20, SavedTokens: 44, CapabilityID: MCPCapabilityTOON})
	if err != nil {
		t.Fatalf("Save mcp: %v", err)
	}

	shellEntry, err := store.Get(shellID)
	if err != nil {
		t.Fatalf("Get shell: %v", err)
	}
	if shellEntry.Kind != KindShell {
		t.Errorf("Kind=%q want %q", shellEntry.Kind, KindShell)
	}

	mcpEntry, err := store.Get(mcpID)
	if err != nil {
		t.Fatalf("Get mcp: %v", err)
	}
	if mcpEntry.Kind != KindMCP {
		t.Errorf("Kind=%q want %q", mcpEntry.Kind, KindMCP)
	}
}

func TestStatsSinceKindSeparatesShellAndMCP(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	_, _ = store.Save(Entry{Kind: KindShell, Command: "make", SavedTokens: 10, CapabilityID: "toml:make"})
	_, _ = store.Save(Entry{Kind: KindShell, Command: "make", SavedTokens: 5, CapabilityID: "toml:make"})
	_, _ = store.Save(Entry{Kind: KindMCP, Command: "srv/tool", SavedTokens: 20, CapabilityID: MCPCapabilityTOON})

	all, err := store.StatsSince(time.Time{})
	if err != nil {
		t.Fatalf("StatsSince: %v", err)
	}
	if all.TotalCommands != 3 {
		t.Errorf("TotalCommands=%d want 3", all.TotalCommands)
	}
	if all.SavedTokens != 35 {
		t.Errorf("SavedTokens=%d want 35", all.SavedTokens)
	}

	shellOnly, err := store.StatsSinceKind(time.Time{}, KindShell)
	if err != nil {
		t.Fatalf("StatsSinceKind shell: %v", err)
	}
	if shellOnly.TotalCommands != 2 {
		t.Errorf("shell TotalCommands=%d want 2", shellOnly.TotalCommands)
	}
	if shellOnly.SavedTokens != 15 {
		t.Errorf("shell SavedTokens=%d want 15", shellOnly.SavedTokens)
	}

	mcpOnly, err := store.StatsSinceKind(time.Time{}, KindMCP)
	if err != nil {
		t.Fatalf("StatsSinceKind mcp: %v", err)
	}
	if mcpOnly.TotalCommands != 1 {
		t.Errorf("mcp TotalCommands=%d want 1", mcpOnly.TotalCommands)
	}
	if mcpOnly.SavedTokens != 20 {
		t.Errorf("mcp SavedTokens=%d want 20", mcpOnly.SavedTokens)
	}
}

// Rows written before the kind column existed must survive migration and be
// readable with an empty Kind (effectively KindShell).
func TestStoreMigratesExistingDatabaseWithoutKindColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	// Write a row using a schema that predates the kind column.
	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS commands (
		id INTEGER PRIMARY KEY,
		timestamp TEXT NOT NULL,
		cmd TEXT NOT NULL DEFAULT '',
		original_output TEXT NOT NULL DEFAULT '',
		filtered_output TEXT NOT NULL DEFAULT '',
		original_output_bytes INTEGER NOT NULL DEFAULT 0,
		filtered_output_bytes INTEGER NOT NULL DEFAULT 0,
		saved_tokens INTEGER NOT NULL DEFAULT 0,
		retrieve_count INTEGER NOT NULL DEFAULT 0,
		session_id TEXT NOT NULL DEFAULT '',
		capability_id TEXT NOT NULL DEFAULT '',
		capability_version TEXT NOT NULL DEFAULT '',
		retrieve_reason TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	_, err = db.Exec(`INSERT INTO commands(timestamp,cmd,saved_tokens) VALUES('2025-01-01T00:00:00Z','make',7)`)
	if err != nil {
		t.Fatalf("legacy insert: %v", err)
	}
	db.Close()

	// OpenStore must run migration and not fail.
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore on legacy db: %v", err)
	}
	defer store.Close()

	// Legacy rows should aggregate (kind defaults to 'shell').
	st, err := store.StatsSince(time.Time{})
	if err != nil {
		t.Fatalf("StatsSince: %v", err)
	}
	if st.TotalCommands != 1 {
		t.Errorf("TotalCommands=%d want 1", st.TotalCommands)
	}
}

// A history database from the released binary migrates onto the v1 shape a
// fresh one has — the same sqlite_master objects, byte for byte — while every
// row's data and the aggregate stats stay put.
func TestStoreMigrationMatchesFreshSchemaAndKeepsStats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	// The exact v0 shape the released OpenStore created.
	if _, err := db.Exec(`CREATE TABLE commands (
		id INTEGER PRIMARY KEY,
		timestamp TEXT NOT NULL,
		cmd TEXT NOT NULL DEFAULT '',
		original_output TEXT NOT NULL DEFAULT '',
		filtered_output TEXT NOT NULL DEFAULT '',
		original_output_bytes INTEGER NOT NULL DEFAULT 0,
		filtered_output_bytes INTEGER NOT NULL DEFAULT 0,
		saved_tokens INTEGER NOT NULL DEFAULT 0,
		retrieve_count INTEGER NOT NULL DEFAULT 0,
		session_id TEXT NOT NULL DEFAULT '',
		capability_id TEXT NOT NULL DEFAULT '',
		capability_version TEXT NOT NULL DEFAULT '',
		retrieve_reason TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL DEFAULT 'shell',
		spool_path TEXT NOT NULL DEFAULT '',
		spool_omitted_bytes INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatalf("create v0 table: %v", err)
	}
	if _, err := db.Exec(`CREATE INDEX idx_outfilter_commands_ts ON commands(timestamp)`); err != nil {
		t.Fatalf("create v0 index: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO commands(timestamp,cmd,original_output,filtered_output,original_output_bytes,filtered_output_bytes,saved_tokens,retrieve_count,session_id,kind,spool_path) VALUES
		('2026-01-02T03:04:05Z','make all','orig-a','filt-a',100,40,900,2,'s-1','shell','/tmp/does-not-exist'),
		('2026-01-02T03:05:00Z','server/tool','orig-b','filt-b',200,80,400,0,'s-2','mcp',''),
		('2026-01-02T03:06:00Z','go test','orig-c','filt-c',50,50,0,1,'s-1','shell','')`); err != nil {
		t.Fatalf("seed v0 rows: %v", err)
	}
	db.Close()

	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore on v0 db: %v", err)
	}
	defer store.Close()
	fresh, err := OpenStore(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatalf("OpenStore fresh: %v", err)
	}
	defer fresh.Close()

	schema := func(s *Store) []string {
		t.Helper()
		rows, err := s.db.Query(`SELECT type, name, tbl_name, IFNULL(sql,'') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
		if err != nil {
			t.Fatalf("sqlite_master: %v", err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var typ, name, tbl, ddl string
			if err := rows.Scan(&typ, &name, &tbl, &ddl); err != nil {
				t.Fatal(err)
			}
			out = append(out, typ+" "+name+" "+tbl+" "+ddl)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	got, want := schema(store), schema(fresh)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated history schema differs from a fresh one:\nmigrated=%v\nfresh=%v", got, want)
	}

	st, err := store.StatsSince(time.Time{})
	if err != nil {
		t.Fatalf("StatsSince: %v", err)
	}
	if st.TotalCommands != 3 || st.FilteredCommands != 2 || st.OriginalBytes != 350 ||
		st.FilteredBytes != 170 || st.SavedTokens != 1300 || st.Retrievals != 3 {
		t.Fatalf("stats after migration = %+v, want the seeded totals", st)
	}

	e, err := store.Get(1)
	if err != nil {
		t.Fatalf("Get(1): %v", err)
	}
	if e.Command != "make all" || e.SavedTokens != 900 || e.Kind != KindShell {
		t.Fatalf("record 1 = %+v, want its command and savings", e)
	}
	if got := e.Timestamp.UTC(); !got.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("record 1 timestamp = %v, want the seeded millisecond", got)
	}
	if e2, err := store.Get(2); err != nil || e2.Kind != KindMCP {
		t.Fatalf("record 2 = %+v (%v), want its mcp kind", e2, err)
	}
}

func TestVisibleToolsForSubagentSubtype(t *testing.T) {
	explore := VisibleToolsForSubagentSubtype("explore", []string{"read_file", "shell", "write_file"})
	if containsTool(explore, "write_file") {
		t.Fatalf("explore leaked blocked tools: %v", explore)
	}
	if !containsTool(explore, "read_file") || !containsTool(explore, "shell") {
		t.Fatalf("explore lost read-only-profile shell: %v", explore)
	}

	plan := VisibleToolsForSubagentSubtype("plan", []string{"read_file", "shell"})
	if !containsTool(plan, "shell") || !containsTool(plan, "read_file") {
		t.Fatalf("plan visible tools mismatch: %v", plan)
	}

	verification := VisibleToolsForSubagentSubtype("verification", []string{"read_file", "shell", "request_permissions"})
	if !containsTool(verification, "shell") {
		t.Fatalf("verification visible tools mismatch: %v", verification)
	}
}

// Every typed subagent keeps request_permissions: it is the tool that puts a
// missing capability to the operator, and a worker that cannot ask has no way
// to reach what the user sent it to read.
func TestRequestPermissionsVisibleToEveryTypedSubagent(t *testing.T) {
	for _, subtype := range []string{
		"general-purpose", "explore", "plan", "plan-reviewer", "verification",
		"cavecrew-investigator", "cavecrew-builder", "cavecrew-reviewer",
	} {
		visible := VisibleToolsForSubagentSubtype(subtype, []string{"read_file", "request_permissions"})
		if !containsTool(visible, "request_permissions") {
			t.Fatalf("%s cannot ask for permissions: %v", subtype, visible)
		}
	}
}

func TestVisibleToolsCavecrewInvestigator(t *testing.T) {
	tools := []string{"read_file", "shell", "write_file", "edit_file", "subagent_run"}
	visible := VisibleToolsForSubagentSubtype("cavecrew-investigator", tools)
	if containsTool(visible, "write_file") || containsTool(visible, "edit_file") || containsTool(visible, "subagent_run") {
		t.Fatalf("cavecrew-investigator leaked mutating tools: %v", visible)
	}
	if !containsTool(visible, "read_file") || !containsTool(visible, "shell") {
		t.Fatalf("cavecrew-investigator lost shell: %v", visible)
	}
}

func TestVisibleToolsCavecrewBuilder(t *testing.T) {
	tools := []string{"read_file", "shell", "write_file", "edit_file", "subagent_run", "request_permissions"}
	visible := VisibleToolsForSubagentSubtype("cavecrew-builder", tools)
	if containsTool(visible, "subagent_run") {
		t.Fatalf("cavecrew-builder leaked blocked tools: %v", visible)
	}
	if !containsTool(visible, "read_file") || !containsTool(visible, "edit_file") || !containsTool(visible, "write_file") || !containsTool(visible, "shell") {
		t.Fatalf("cavecrew-builder lost allowed tools: %v", visible)
	}
}

func TestVisibleToolsCavecrewReviewer(t *testing.T) {
	tools := []string{"read_file", "shell", "write_file", "edit_file", "subagent_run"}
	visible := VisibleToolsForSubagentSubtype("cavecrew-reviewer", tools)
	if containsTool(visible, "write_file") || containsTool(visible, "edit_file") || containsTool(visible, "subagent_run") {
		t.Fatalf("cavecrew-reviewer leaked mutating tools: %v", visible)
	}
	if !containsTool(visible, "read_file") || !containsTool(visible, "shell") {
		t.Fatalf("cavecrew-reviewer lost shell: %v", visible)
	}
}

func containsTool(in []string, want string) bool {
	for _, item := range in {
		if item == want {
			return true
		}
	}
	return false
}

// The reviewer has to do the same investigation a planning subagent does, so it
// gets the same tools — except the two plan-mode tools, which would let it act
// on the very approval it was called to advise.
func TestVisibleToolsPlanReviewerMatchesPlanMinusPlanModeTools(t *testing.T) {
	all := []string{
		"read_file", "shell", "web_fetch", "web_search", "session_todo",
		"memories_search", "retrieve_output", "working_set_show",
		"write_file", "edit_file", "request_permissions",
		"enter_plan_mode", "exit_plan_mode",
		"subagent_run", "subagent_fanout",
	}
	plan := VisibleToolsForSubagentSubtype("plan", all)
	reviewer := VisibleToolsForSubagentSubtype("plan-reviewer", all)

	visible := map[string]bool{}
	for _, name := range reviewer {
		visible[name] = true
	}
	for _, name := range []string{"read_file", "shell", "web_fetch", "web_search", "memories_search", "retrieve_output", "working_set_show", "request_permissions"} {
		if !visible[name] {
			t.Fatalf("reviewer must be able to call %q; got %v", name, reviewer)
		}
	}
	for _, name := range []string{"write_file", "edit_file", "enter_plan_mode", "exit_plan_mode", "subagent_run", "subagent_fanout"} {
		if visible[name] {
			t.Fatalf("reviewer must not be able to call %q; got %v", name, reviewer)
		}
	}

	planVisible := map[string]bool{}
	for _, name := range plan {
		planVisible[name] = true
	}
	for name := range planVisible {
		if name == "enter_plan_mode" || name == "exit_plan_mode" {
			continue
		}
		if !visible[name] {
			t.Fatalf("reviewer is missing %q, which the plan subagent has", name)
		}
	}
}

// The reviewer cannot edit files, which is what lets the subagent bridge run it
// in agent mode instead of inheriting the parent's plan mode.
func TestPlanReviewerBlocksFileWrites(t *testing.T) {
	if !TypedSubagentBlocksFileWrites("plan-reviewer") {
		t.Fatal("plan-reviewer must be recognised as write-free")
	}
}

func TestFindActualEditStringWithQuoteNormalization(t *testing.T) {
	got := findActualEditString("say “hello” now", `say "hello" now`)
	if got != "say “hello” now" {
		t.Fatalf("actual=%q", got)
	}
}

func TestPreserveTextEditQuoteStyle(t *testing.T) {
	got := preserveTextEditQuoteStyle(`"hello"`, `“hello”`, `"world"`)
	if got != `“world”` {
		t.Fatalf("got %q", got)
	}
}

func TestPreserveSingleQuoteContraction(t *testing.T) {
	got := preserveTextEditQuoteStyle(`'test'`, `‘test’`, "'don't'")
	if got != `‘don’t’` {
		t.Fatalf("got %q", got)
	}
}

func TestFindActualEditStringTabsVsSpaces(t *testing.T) {
	// File on disk uses tabs (Go convention); the caller retransmits with
	// leading spaces. The matcher should still locate the exact on-disk text.
	file := "func foo() {\n\tbody := \"x\"\n\treturn body\n}\n"
	search := "    body := \"x\"\n    return body\n"
	got := findActualEditString(file, search)
	want := "\tbody := \"x\"\n\treturn body\n"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestFindActualEditStringCRLF(t *testing.T) {
	// File uses CRLF; caller retransmits with LF. Match should return the exact
	// CRLF on-disk bytes.
	file := "line one\r\nline two\r\n"
	search := "line one\nline two\n"
	got := findActualEditString(file, search)
	want := "line one\r\nline two\r\n"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestFindActualEditStringTrailingWhitespace(t *testing.T) {
	// File has trailing spaces on a line; caller omits them.
	file := "func foo() {\n\tbody := \"x\"   \n}\n"
	search := "\tbody := \"x\"\n}\n"
	got := findActualEditString(file, search)
	want := "\tbody := \"x\"   \n}\n"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestFindActualEditStringNoMatch(t *testing.T) {
	got := findActualEditString("alpha beta gamma", "delta")
	if got != "" {
		t.Fatalf("expected empty, got=%q", got)
	}
}

// TestToolMetaMatchesRegisteredTools pins the two-way correspondence between
// registered tools and the metadata registry, across the runtime shapes that
// select different tool sets.
//
// Metadata is not cosmetic: routing and concurrency decisions need tools to have an
// entry, and PartitionToolCalls reads ReadOnly/ConcurrencySafe off it, treating
// a tool with no entry as unsafe to run alongside anything else. So a missing
// entry silently serializes a read-only tool, and an entry with no tool behind
// it hands the model a name it cannot call.
func TestToolMetaMatchesRegisteredTools(t *testing.T) {
	for _, test := range []struct {
		name    string
		runtime func(home string) *AgentToolRuntime
	}{
		{
			name: "full runtime",
			runtime: func(home string) *AgentToolRuntime {
				return &AgentToolRuntime{Home: home, WorkspaceRoot: home, Cfg: &appcfg.Root{}}
			},
		},
		{
			// StateRoot() is empty here, which skips the session/context tools.
			// Their metadata used to be registered unconditionally elsewhere and
			// stayed behind as phantom entries.
			name: "no state root",
			runtime: func(string) *AgentToolRuntime {
				return &AgentToolRuntime{Cfg: &appcfg.Root{}}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			st := NewState(home)
			a, err := agent.New(noopLLM{}, "test", "test")
			if err != nil {
				t.Fatalf("agent.New: %v", err)
			}
			if err := RegisterDefaultTools(a, st, test.runtime(home)); err != nil {
				t.Fatalf("RegisterDefaultTools: %v", err)
			}
			tools := registeredTools(t, a)
			if len(tools) == 0 {
				t.Fatal("no tools registered")
			}
			metas := map[string]json.RawMessage{}
			for _, meta := range st.ToolMetas() {
				metas[meta.Name] = meta.InputSchema
			}
			var missingMeta, phantomMeta []string
			for name := range tools {
				if _, ok := metas[name]; !ok {
					missingMeta = append(missingMeta, name)
				}
			}
			for name := range metas {
				if _, ok := tools[name]; !ok {
					phantomMeta = append(phantomMeta, name)
				}
			}
			sort.Strings(missingMeta)
			sort.Strings(phantomMeta)
			if len(missingMeta) > 0 {
				t.Errorf("registered tools without metadata (never run concurrently): %v", missingMeta)
			}
			if len(phantomMeta) > 0 {
				t.Errorf("metadata without a registered tool (advertised but uncallable): %v", phantomMeta)
			}
			for name, tool := range tools {
				raw, ok := metas[name]
				if !ok {
					continue
				}
				want := comparableSchema(tool.InputSchema())
				var metaSchema map[string]any
				if err := json.Unmarshal(raw, &metaSchema); err != nil {
					t.Errorf("%s: metadata schema is not valid JSON: %v", name, err)
					continue
				}
				if got := comparableSchema(metaSchema); got != want {
					t.Errorf("%s: metadata schema differs from the tool's own schema\n  meta: %s\n  tool: %s", name, got, want)
				}
			}
		})
	}
}

// comparableSchema renders a schema for comparison, dropping the identity keys
// that legitimately differ between a reflected and a marshalled copy.
func comparableSchema(schema map[string]any) string {
	clone := make(map[string]any, len(schema))
	for key, value := range schema {
		switch key {
		case "$schema", "$id", "$defs":
			continue
		}
		clone[key] = value
	}
	out, err := json.Marshal(clone)
	if err != nil {
		return "<unmarshalable>"
	}
	return string(out)
}

// registeredTools reads the agent's tool slice. The field is unexported and the
// agent has no accessor; this test needs the real registration result rather
// than a rebuilt approximation of it.
func registeredTools(t *testing.T, a *agent.Agent) map[string]*llm.Tool {
	t.Helper()
	field := reflect.ValueOf(a).Elem().FieldByName("tools")
	if !field.IsValid() {
		t.Fatal("agent.tools field not found: update this test to match the agent layout")
	}
	field = reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
	tools, ok := field.Interface().([]*llm.Tool)
	if !ok {
		t.Fatalf("agent.tools has unexpected type %s", field.Type())
	}
	out := make(map[string]*llm.Tool, len(tools))
	for _, tool := range tools {
		if tool != nil {
			out[tool.Name()] = tool
		}
	}
	return out
}

func TestAttachTurnDiff(t *testing.T) {
	out := map[string]any{"status": "ok"}
	attachTurnDiff(out, "/tmp/demo.txt", []byte("a\n"), []byte("b\n"))
	payload, ok := out["turn_diff"].(event.Summary)
	if !ok {
		t.Fatalf("expected turndiff summary, got %#v", out["turn_diff"])
	}
	if payload.Path != "/tmp/demo.txt" {
		t.Fatalf("unexpected path: %q", payload.Path)
	}
	if payload.Added != 1 || payload.Deleted != 1 {
		t.Fatalf("unexpected counts: %+v", payload)
	}
}

func TestSessionTodoInputAcceptsTodoWriteShape(t *testing.T) {
	in := sessionTodoInput{
		Action: "set",
		Items: []sessionTodoItem{
			{ID: "1", Content: "Write tests", Status: "in_progress", Title: "Writing tests"},
		},
	}
	got := normalizeSessionTodoItems(in)
	if len(got) != 1 {
		t.Fatalf("items len=%d want 1", len(got))
	}
	if got[0].ID != "1" || got[0].Content != "Write tests" || got[0].Status != "in_progress" {
		t.Fatalf("todo item mismatch: %+v", got[0])
	}
}

// TestSessionTodoListWithItemsWrites reproduces a real debug.log call: the
// caller meant to create a checklist but sent action "list" with the rows in
// items. A read never carries rows, so a payload-bearing call must write the
// list, not be silently dropped to an empty one.
func TestSessionTodoListWithItemsWrites(t *testing.T) {
	st := NewState()
	tool, err := newSessionTodoTool(st, t.TempDir())
	if err != nil {
		t.Fatalf("newSessionTodoTool: %v", err)
	}
	raw := `{"action":"list",` +
		`"items":[{"id":"1","content":"Search codebase","status":"in_progress","title":"Searching"}]}`
	outAny, err := tool.Handle(llm.WithAgentSessionID(context.Background(), "s1"), raw)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	outStr, _ := outAny.(string)
	var got struct {
		Items []struct {
			ID      string `json:"id"`
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(outStr), &got); err != nil {
		t.Fatalf("unmarshal result: %v (raw=%s)", err, outStr)
	}
	if len(got.Items) != 1 || got.Items[0].ID != "1" || got.Items[0].Content != "Search codebase" {
		t.Fatalf("checklist dropped, result=%s", outStr)
	}
}

// TestSessionTodoSchemaMatchesHandlerContract pins the schema to what the
// handler actually accepts. Both fields were reflected as required even though
// the handler reads the checklist back on an empty action and a list/clear call
// carries no rows — a mismatch that pushes the model into sending placeholder
// values for arguments it means to skip.
func TestSessionTodoSchemaMatchesHandlerContract(t *testing.T) {
	st := NewState()
	tool, err := newSessionTodoTool(st, t.TempDir())
	if err != nil {
		t.Fatalf("newSessionTodoTool: %v", err)
	}
	if required, ok := tool.InputSchema()["required"]; ok {
		if list, isList := required.([]any); isList && len(list) > 0 {
			t.Fatalf("session_todo required = %v, want no required fields", list)
		}
	}
	for _, args := range []string{`{}`, `{"action":"list"}`, `{"action":"clear"}`} {
		if _, err := tool.Handle(llm.WithAgentSessionID(context.Background(), "s1"), args); err != nil {
			t.Fatalf("session_todo rejected %s: %v", args, err)
		}
	}
}

// A second write is the whole checklist, not an addition to it. The model
// rewrites its plan with fresh ids as work turns out differently; when the
// store merged, the superseded rows stayed in the progress panel — the
// screenshot that opened this bug showed two plans and two in_progress items at
// once — and the tool result echoed them back, so the model never saw its own
// list either.
func TestSessionTodoSetReplacesPreviousChecklist(t *testing.T) {
	st := NewState()
	var got StepEvent
	st.SetStepHook(func(ctx context.Context, evt StepEvent) {
		got = evt
	})
	tool, err := newSessionTodoTool(st, t.TempDir())
	if err != nil {
		t.Fatalf("newSessionTodoTool: %v", err)
	}
	ctx := llm.WithAgentSessionID(context.Background(), "s1")
	first := `{"action":"set","items":[{"id":"trace","content":"Trace the model list","status":"in_progress"},{"id":"design","content":"Design the integration","status":"pending"}]}`
	if _, err := tool.Handle(ctx, first); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	second := `{"action":"set","items":[{"id":"scan","content":"Locate the code","status":"completed"},{"id":"report","content":"Write it up","status":"in_progress"}]}`
	outAny, err := tool.Handle(ctx, second)
	if err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	var result struct {
		Items []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(outAny.(string)), &result); err != nil {
		t.Fatalf("unmarshal result: %v (raw=%v)", err, outAny)
	}
	if len(result.Items) != 2 || result.Items[0].ID != "scan" || result.Items[1].ID != "report" {
		t.Fatalf("stale rows echoed back to the model: %v", outAny)
	}
	if got.PlanUpdate == nil {
		t.Fatalf("expected plan update, got %#v", got)
	}
	if got.PlanUpdate.Total != 2 || got.PlanUpdate.Completed != 1 {
		t.Fatalf("progress counted stale rows: completed=%d total=%d", got.PlanUpdate.Completed, got.PlanUpdate.Total)
	}
	if got.PlanUpdate.Explanation != "Write it up" {
		t.Fatalf("two plans active at once: %q", got.PlanUpdate.Explanation)
	}
}

// A subagent keeps its own todo list, and its plan card belongs in that
// subagent's view. The attribution has to travel on the payload: it is what the
// surfaces route by, and it is persisted with the step so a replayed view
// rebuilds the same card.
func TestSessionTodoPlanUpdateNamesTheEmittingSubagent(t *testing.T) {
	st := NewState()
	var got StepEvent
	st.SetStepHook(func(ctx context.Context, evt StepEvent) { got = evt })
	todo, err := newSessionTodoTool(st, t.TempDir())
	if err != nil {
		t.Fatalf("newSessionTodoTool: %v", err)
	}
	ctx := WithHookAgentID(llm.WithAgentSessionID(context.Background(), "s1"), "task-1")
	if _, err := todo.Handle(ctx, `{"action":"set","items":[{"id":"1","content":"Read source","status":"in_progress"}]}`); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got.PlanUpdate == nil || got.PlanUpdate.AgentID != "task-1" {
		t.Fatalf("plan update must name the subagent that emitted it: %#v", got.PlanUpdate)
	}

	// The same call from the primary agent stays unattributed.
	if _, err := todo.Handle(llm.WithAgentSessionID(context.Background(), "s2"),
		`{"action":"set","items":[{"id":"1","content":"Ship","status":"pending"}]}`); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got.PlanUpdate == nil || got.PlanUpdate.AgentID != "" {
		t.Fatalf("the conversation's own plan must carry no agent: %#v", got.PlanUpdate)
	}
}

// A child run's steps reach the surfaces through the canonical event stream,
// never through the parent's step ledger, so the plan update has to project
// into a run event or the subagent's view gets no plan card at all — which is
// what left the raw session_todo JSON standing in for it.
func TestRunEventFromStepCarriesPlanUpdateAndDropsItsToolCard(t *testing.T) {
	ctx := WithHookAgentID(context.Background(), "task-1")
	plan := &event.PlanUpdatedPayload{
		Title: "Updated Plan", Total: 1,
		Items: []event.PlanUpdateItem{{Content: "Read source", Status: "in_progress"}},
	}
	evt, ok := RunEventFromStep(ctx, "s1", "run-child", "tui", StepEvent{
		Kind: event.RunEventPlanUpdated, StepID: "plan-update-1",
		ToolName: "session_todo", PlanUpdate: plan,
	})
	if !ok || evt.Type != event.RunEventPlanUpdated {
		t.Fatalf("plan update did not project: ok=%v type=%q", ok, evt.Type)
	}
	var projected event.PlanUpdatedPayload
	if err := json.Unmarshal(evt.Payload, &projected); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if projected.AgentID != "task-1" || len(projected.Items) != 1 {
		t.Fatalf("projected plan lost its attribution or items: %#v", projected)
	}

	for _, kind := range []string{event.RunEventToolStarted, event.RunEventToolCompleted} {
		if _, ok := RunEventFromStep(ctx, "s1", "run-child", "tui", StepEvent{
			Kind: kind, StepID: "step-1", ToolName: "session_todo",
			Output: map[string]any{"output": "{}"},
		}); ok {
			t.Fatalf("%s: a plan-emitting tool must not also get a tool card", kind)
		}
	}
	// A failed call has no plan card, so it keeps its own.
	if _, ok := RunEventFromStep(ctx, "s1", "run-child", "tui", StepEvent{
		Kind: event.RunEventToolCompleted, StepID: "step-2", ToolName: "session_todo",
		Error: "disk full",
	}); !ok {
		t.Fatal("a failed session_todo call must still be reported")
	}
}

func TestSessionTodoSetEmitsPlanUpdatedStep(t *testing.T) {
	st := NewState()
	var got StepEvent
	st.SetStepHook(func(ctx context.Context, evt StepEvent) {
		got = evt
	})
	tool, err := newSessionTodoTool(st, t.TempDir())
	if err != nil {
		t.Fatalf("newSessionTodoTool: %v", err)
	}
	_, err = tool.Handle(llm.WithAgentSessionID(context.Background(), "s1"), `{"action":"set","items":[{"id":"1","content":"Write tests","status":"in_progress"},{"id":"2","content":"Ship","status":"pending"}]}`)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got.Kind != event.RunEventPlanUpdated || got.PlanUpdate == nil {
		t.Fatalf("expected plan update step, got %#v", got)
	}
	if got.PlanUpdate.Title != "Updated Plan" || len(got.PlanUpdate.Items) != 2 {
		t.Fatalf("unexpected plan payload: %#v", got.PlanUpdate)
	}
	if got.PlanUpdate.Items[0].Status != "in_progress" || got.PlanUpdate.Items[0].Content != "Write tests" {
		t.Fatalf("unexpected first item: %#v", got.PlanUpdate.Items[0])
	}
	if got.PlanUpdate.Completed != 0 || got.PlanUpdate.Total != 2 {
		t.Fatalf("unexpected plan progress: completed=%d total=%d", got.PlanUpdate.Completed, got.PlanUpdate.Total)
	}
	if got.PlanUpdate.Explanation != "Write tests" {
		t.Fatalf("unexpected explanation: %#v", got.PlanUpdate.Explanation)
	}
	if got.PlanUpdate.Items[0].Active != "Write tests" {
		t.Fatalf("unexpected first item active label: %#v", got.PlanUpdate.Items[0])
	}
}

func TestSessionTodoSetPersistsTitleIntoPlanUpdate(t *testing.T) {
	st := NewState()
	var got StepEvent
	st.SetStepHook(func(ctx context.Context, evt StepEvent) {
		got = evt
	})
	tool, err := newSessionTodoTool(st, t.TempDir())
	if err != nil {
		t.Fatalf("newSessionTodoTool: %v", err)
	}
	_, err = tool.Handle(llm.WithAgentSessionID(context.Background(), "s1"), `{"action":"set","items":[{"id":"1","content":"Write tests","status":"in_progress","title":"Writing tests"},{"id":"2","content":"Ship","status":"completed"}]}`)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got.PlanUpdate == nil {
		t.Fatalf("expected plan update, got %#v", got)
	}
	if got.PlanUpdate.Completed != 1 || got.PlanUpdate.Total != 2 {
		t.Fatalf("unexpected plan progress: completed=%d total=%d", got.PlanUpdate.Completed, got.PlanUpdate.Total)
	}
	if got.PlanUpdate.Explanation != "Writing tests" {
		t.Fatalf("unexpected explanation: %#v", got.PlanUpdate.Explanation)
	}
	if got.PlanUpdate.Items[0].Active != "Writing tests" {
		t.Fatalf("unexpected item active label: %#v", got.PlanUpdate.Items[0])
	}
}

// runCap runs a command in the repo root and returns its stdout. Failures are
// tolerated because these measurement commands are best-effort samples of real
// noisy output, not assertions.
func runCap(name string, args ...string) string {
	cmd := exec.Command(name, args...)
	cmd.Dir = "/Users/doudou/workspace/unionj-cloud/forebrain-harness"
	out, _ := cmd.Output()
	return string(out)
}

// TestRealWorldSemanticMeasurements measures the new semantic filters against
// genuinely large output produced on this machine, so documented savings come
// from real runs rather than synthetic fixtures.
func TestRealWorldSemanticMeasurements(t *testing.T) {
	cases := []struct {
		label string
		cmd   string
		out   string
	}{
		{
			"find frontend/node_modules -type f",
			"find frontend/node_modules -type f",
			runCap("find", "frontend/node_modules", "-type", "f"),
		},
		{
			"ls -laR frontend/node_modules",
			"ls -laR frontend/node_modules",
			runCap("ls", "-laR", "frontend/node_modules"),
		},
		{
			"grep -rn func internal",
			"grep -rn func internal",
			runCap("grep", "-rn", "func", "internal"),
		},
		{
			"wc -l (go files)",
			"wc -l",
			runCap("bash", "-c", "find internal -name '*.go' | head -400 | xargs wc -l"),
		},
		{
			"env",
			"env",
			runCap("env"),
		},
	}

	totalIn, totalOut := 0, 0
	for _, c := range cases {
		if c.out == "" {
			t.Logf("%-40s SKIP (no output)", c.label)
			continue
		}
		res, ok := ApplySemantic(c.cmd, c.out)
		if !ok {
			t.Logf("%-40s %9d B -> PASSTHROUGH", c.label, len(c.out))
			continue
		}
		savedPct := 100 * (len(c.out) - len(res.Output)) / len(c.out)
		t.Logf("%-40s %9d B -> %8d B  (%2d%% saved, ~%d tokens, filter=%s)",
			c.label, len(c.out), len(res.Output), savedPct,
			(len(c.out)-len(res.Output))/4, res.FilterName)
		totalIn += len(c.out)
		totalOut += len(res.Output)
	}
	if totalIn > 0 {
		t.Logf("TOTAL %d B -> %d B (%d%% saved, ~%d tokens kept out)",
			totalIn, totalOut, 100*(totalIn-totalOut)/totalIn, (totalIn-totalOut)/4)
	}
}

func TestRequestPermissionsRejectsEmptyRequest(t *testing.T) {
	tool, err := NewRequestPermissionsTool(NewState(t.TempDir()), &AgentToolRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Handle(context.Background(), `{"reason":"needed","permissions":{}}`); err == nil {
		t.Fatal("empty permission request must fail")
	}
}

func TestRequestPermissionsInputAcceptsEnvironmentIDAlias(t *testing.T) {
	var input RequestPermissionsInput
	if err := json.Unmarshal([]byte(`{"environmentId":"primary","permissions":{"network":{"enabled":true}}}`), &input); err != nil {
		t.Fatal(err)
	}
	if input.EnvironmentID != "primary" || !input.Permissions.Network.AllowsNetwork() {
		t.Fatalf("input=%+v", input)
	}
}

func TestRequestPermissionsAppliesNetworkGrant(t *testing.T) {
	var update safety.PermissionUpdate
	tool, err := NewRequestPermissionsTool(NewState(t.TempDir()), &AgentToolRuntime{
		ApplyPermissionUpdate: func(got safety.PermissionUpdate) { update = got },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Handle(WithPolicyApproved(context.Background(), true), `{"reason":"download","permissions":{"network":{"enabled":true}}}`)
	if err != nil {
		t.Fatalf("network request: %v", err)
	}
	var response safety.RequestPermissionsResponse
	if json.Unmarshal([]byte(result.(string)), &response) != nil || !response.Permissions.Network.AllowsNetwork() {
		t.Fatalf("response=%+v", response)
	}
	if update.Type != safety.UpdateAddPermissionGrants || len(update.NetworkGrants) != 1 || !update.NetworkGrants[0].Enabled {
		t.Fatalf("update=%+v", update)
	}
}

func TestDeniedRequestPermissionsReturnsEmptyGrantWithoutApplyingUpdate(t *testing.T) {
	root := t.TempDir()
	updates := 0
	tool, err := NewRequestPermissionsTool(NewState(root), &AgentToolRuntime{
		ApplyPermissionUpdate: func(safety.PermissionUpdate) { updates++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithRequestPermissionsDenied(context.Background(), "never_policy_default_deny")
	result, err := tool.Handle(ctx, `{"reason":"inspect","permissions":{"file_system":{"read":["/tmp/external"]}}}`)
	if err != nil {
		t.Fatalf("denied request should resolve as an empty grant: %v", err)
	}
	var response safety.RequestPermissionsResponse
	if json.Unmarshal([]byte(result.(string)), &response) != nil {
		t.Fatalf("invalid response: %#v", result)
	}
	if response.Permissions.FileSystem != nil || response.Scope != safety.GrantScopeTurn || updates != 0 {
		t.Fatalf("denied request granted permissions: response=%+v updates=%d", response, updates)
	}
}

func TestDeniedRequestPermissionsCapturesDenialReasonForUI(t *testing.T) {
	tool, err := NewRequestPermissionsTool(NewState(t.TempDir()), &AgentToolRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithRequestPermissionsDenied(context.Background(), "matched deny rule `request_permissions`")
	ctx = WithToolCompletionCapture(WithPolicyApprovalReasonCapture(ctx))
	if _, err := tool.Handle(ctx, `{"reason":"inspect","permissions":{"file_system":{"read":["/tmp/external"]}}}`); err != nil {
		t.Fatalf("denied request: %v", err)
	}
	completed, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("expected captured tool completion")
	}
	if got := completed.Output["approval_status"]; got != "denied" {
		t.Fatalf("approval_status=%v want denied", got)
	}
	if got := completed.Output["approval_reason"]; !strings.Contains(got.(string), "deny rule") {
		t.Fatalf("approval_reason=%v want deny-rule explanation", got)
	}
}

// TestDeniedRequestPermissionsCapturesRequestedPathsForUI guards the denied
// card against losing the requested paths. A denial grants nothing, so the
// response profile is empty; the captured output must still carry what the
// agent asked for or the card cannot show which access was refused.
func TestDeniedRequestPermissionsCapturesRequestedPathsForUI(t *testing.T) {
	cwd := t.TempDir()
	st := NewState(cwd)
	tool, err := NewRequestPermissionsTool(st, &AgentToolRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	requested := filepath.Join(cwd, "outside")
	// normalizeRequestPermissionsInput resolves symlinks, so resolve the
	// expected path too (macOS: /var/folders → /private/var/folders).
	requestedNorm := requested
	if r, err := filepath.EvalSymlinks(cwd); err == nil {
		requestedNorm = filepath.Join(r, "outside")
	}
	ctx := WithProjectRoot(context.Background(), cwd)
	ctx = WithRequestPermissionsDenied(ctx, "matched deny rule `request_permissions`")
	ctx = WithToolCompletionCapture(WithPolicyApprovalReasonCapture(ctx))
	args := `{"reason":"inspect","permissions":{"file_system":{"read":["` + requested + `"]}}}`
	if _, err := tool.Handle(ctx, args); err != nil {
		t.Fatalf("denied request: %v", err)
	}
	completed, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("expected captured tool completion")
	}
	profile, ok := completed.Output["permissions"].(safety.RequestPermissionProfile)
	if !ok {
		t.Fatalf("permissions=%T want RequestPermissionProfile", completed.Output["permissions"])
	}
	if profile.FileSystem == nil || len(profile.FileSystem.Entries) == 0 {
		t.Fatalf("denied output lost the requested paths: %+v", profile)
	}
	found := false
	for _, entry := range profile.FileSystem.Entries {
		if entry.Path.Path == requestedNorm && entry.Access == safety.FileSystemAccessRead {
			found = true
		}
	}
	if !found {
		t.Fatalf("requested path %q missing from denied output: %+v", requestedNorm, profile.FileSystem.Entries)
	}
}

func TestAutoApprovedRequestPermissionsCapturesExemptionReasonForUI(t *testing.T) {
	tool, err := NewRequestPermissionsTool(NewState(t.TempDir()), &AgentToolRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithPolicyApproved(context.Background(), true)
	ctx = WithToolCompletionCapture(WithPolicyApprovalReasonCapture(ctx))
	CapturePolicyApprovalReason(ctx, "permission mode is auto-approve, which approves all tools without prompting")
	if _, err := tool.Handle(ctx, `{"reason":"inspect","permissions":{"file_system":{"read":["/tmp/external"]}}}`); err != nil {
		t.Fatalf("auto-approved request: %v", err)
	}
	completed, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("expected captured tool completion")
	}
	if got := completed.Output["approval_status"]; got != "auto_approved" {
		t.Fatalf("approval_status=%v want auto_approved", got)
	}
	if got := completed.Output["approval_reason"]; !strings.Contains(got.(string), "auto-approve") {
		t.Fatalf("approval_reason=%v want exemption explanation", got)
	}
}

func TestRequestPermissionsResolvesProjectRootPathAndRaisesAction(t *testing.T) {
	cwd := t.TempDir()
	st := NewState(filepath.Join(cwd, "workspace"))
	st.SetActionHook(func(_ context.Context, kind string, payload any) (string, bool, error) {
		if kind != requestPermissionsActionKind {
			t.Fatalf("kind=%q", kind)
		}
		m := payload.(map[string]any)
		profile := m["permissions"].(safety.RequestPermissionProfile)
		entry := profile.FileSystem.Entries[0]
		if entry.Path.Type != safety.FileSystemPermissionPathTypeSpecial || entry.Path.Value == nil ||
			entry.Path.Value.Kind != safety.FileSystemSpecialPathProjectRoots || entry.Path.Value.Subpath != "outside" ||
			entry.Access != safety.FileSystemAccessRead {
			t.Fatalf("entry=%+v", entry)
		}
		if m["cwd"] != cwd {
			t.Fatalf("cwd=%v want %q", m["cwd"], cwd)
		}
		return "action-1", true, nil
	})
	tool, err := NewRequestPermissionsTool(st, &AgentToolRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithProjectRoot(context.Background(), cwd)
	_, err = tool.Handle(ctx, `{"reason":"inspect","permissions":{"file_system":{"entries":[{"path":{"type":"special","value":{"kind":"project_roots","subpath":"outside"}},"access":"read"}]}}}`)
	var req *RequiresActionError
	if !errors.As(err, &req) || req.ActionKind != requestPermissionsActionKind {
		t.Fatalf("err=%v", err)
	}
}

// A subagent that needs a path outside its scope puts the question to the
// operator, exactly as the primary agent does. Refusing the tool here used to
// leave the worker with nothing to do but abandon the path it was sent to read.
func TestSubagentRequestPermissionsRaisesApprovalAction(t *testing.T) {
	cwd := t.TempDir()
	outside := t.TempDir()
	st := NewState(filepath.Join(cwd, "workspace"))
	raised := ""
	st.SetActionHook(func(_ context.Context, kind string, _ any) (string, bool, error) {
		raised = kind
		return "action-1", true, nil
	})
	tool, err := NewRequestPermissionsTool(st, &AgentToolRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	for _, subtype := range []string{"explore", "general-purpose"} {
		raised = ""
		ctx := WithSubagentType(WithProjectRoot(context.Background(), cwd), subtype)
		_, err := tool.Handle(ctx, `{"reason":"inspect","permissions":{"file_system":{"read":["`+escapeJSONPath(outside)+`"]}}}`)
		var req *RequiresActionError
		if !errors.As(err, &req) || req.ActionKind != requestPermissionsActionKind {
			t.Fatalf("%s: want a pending approval, got %v", subtype, err)
		}
		if raised != requestPermissionsActionKind {
			t.Fatalf("%s: action hook saw kind %q", subtype, raised)
		}
	}
}

// The one thing an approval may not hand a subagent is a capability its type
// does not have: a read-only worker's shell would start writing outside the
// project on a prompt that only mentions a path.
func TestReadOnlySubagentCannotRequestWriteAccess(t *testing.T) {
	cwd := t.TempDir()
	outside := t.TempDir()
	st := NewState(filepath.Join(cwd, "workspace"))
	asked := 0
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		asked++
		return "action-1", true, nil
	})
	tool, err := NewRequestPermissionsTool(st, &AgentToolRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithSubagentType(WithProjectRoot(context.Background(), cwd), "explore")
	_, err = tool.Handle(ctx, `{"reason":"edit","permissions":{"file_system":{"write":["`+escapeJSONPath(outside)+`"]}}}`)
	if err == nil || !strings.Contains(err.Error(), "cannot request write access") {
		t.Fatalf("err=%v, want a refusal naming the type's write policy", err)
	}
	if asked != 0 {
		t.Fatal("a write request from a read-only type must not reach the operator")
	}
	// The same worker may still ask to read that directory.
	_, err = tool.Handle(ctx, `{"reason":"inspect","permissions":{"file_system":{"read":["`+escapeJSONPath(outside)+`"]}}}`)
	var req *RequiresActionError
	if !errors.As(err, &req) {
		t.Fatalf("read request from a read-only type must still ask, got %v", err)
	}
	if asked != 1 {
		t.Fatalf("action hook calls = %d, want 1", asked)
	}
}

func TestRequestPermissionsResolvesRelativePathAgainstProjectRoot(t *testing.T) {
	cwd := t.TempDir()
	st := NewState(cwd)
	st.SetActionHook(func(_ context.Context, kind string, payload any) (string, bool, error) {
		if kind != requestPermissionsActionKind {
			t.Fatalf("kind=%q", kind)
		}
		profile := payload.(map[string]any)["permissions"].(safety.RequestPermissionProfile)
		entry := profile.FileSystem.Entries[0]
		if entry.Path.Path != normalizeRootPath(filepath.Join(cwd, "data", "notes")) || entry.Access != safety.FileSystemAccessWrite {
			t.Fatalf("entry=%+v", entry)
		}
		return "action-relative", true, nil
	})
	tool, err := NewRequestPermissionsTool(st, &AgentToolRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithProjectRoot(context.Background(), cwd)
	_, err = tool.Handle(ctx, `{"permissions":{"file_system":{"write":["data/notes"]}}}`)
	var actionErr *RequiresActionError
	if !errors.As(err, &actionErr) || actionErr.ActionID != "action-relative" {
		t.Fatalf("err=%v", err)
	}
}

func TestApprovedRequestPermissionsAppliesSessionGrant(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	file := filepath.Join(outside, "note.txt")
	if err := os.WriteFile(file, []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := safety.NewStore()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := state.NewSessionStore(db, "main").Ensure(context.Background(), "conv", "conv"); err != nil {
		t.Fatal(err)
	}
	actions := &state.ActionService{DB: db}
	requestedProfile := safety.RequestPermissionProfile{FileSystem: &safety.FileSystemPermissionProfile{Entries: []safety.FileSystemPermissionEntry{{
		Path: safety.FileSystemPermissionPath{Path: outside}, Access: safety.FileSystemAccessRead,
	}}}}
	action, err := actions.CreatePending(context.Background(), "conv", requestPermissionsActionKind, map[string]any{"permissions": requestedProfile, "cwd": root})
	if err != nil {
		t.Fatal(err)
	}
	answer, _ := json.Marshal(safety.RequestPermissionsResponse{Permissions: requestedProfile, Scope: safety.GrantScopeSession})
	if _, err := actions.ApproveWithAnswer(context.Background(), action.ID, "approved", string(answer)); err != nil {
		t.Fatal(err)
	}
	rt := &AgentToolRuntime{
		Actions:                      actions,
		PermissionSnapshot:           store.Snapshot,
		PermissionSnapshotForSession: store.SnapshotForSession,
		ApplyPermissionUpdate: func(update safety.PermissionUpdate) {
			safety.ApplyUpdate(store, update)
		},
	}
	st := NewState(root)
	st.SetRuntimeValue("permission_snapshot_for_session", rt.PermissionSnapshotForSession)
	tool, err := NewRequestPermissionsTool(st, rt)
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"reason": "inspect", "permissions": requestedProfile}
	raw, _ := json.Marshal(input)
	ctx := llm.WithAgentSessionID(WithRunID(WithApprovedActionID(context.Background(), action.ID), "run-1"), "session-1")
	if _, err := tool.Handle(ctx, string(raw)); err != nil {
		t.Fatalf("approved request: %v", err)
	}
	if _, err := resolveReadFilePath(llm.WithAgentSessionID(WithRunID(context.Background(), "run-2"), "session-1"), st, file); err != nil {
		t.Fatalf("session grant should allow later run: %v", err)
	}
}

func TestWriteGrantAlsoAllowsRead(t *testing.T) {
	outside := t.TempDir()
	file := filepath.Join(outside, "note.txt")
	if err := os.WriteFile(file, []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := safety.NewStore()
	safety.ApplyUpdate(store, safety.PermissionUpdate{
		Type: safety.UpdateAddPermissionGrants, Destination: safety.DestinationSession, SessionID: "session-1",
		FileSystemGrants: []safety.FileSystemPermissionGrant{{
			Entry: safety.FileSystemPermissionEntry{Path: safety.FileSystemPermissionPath{Path: outside}, Access: safety.FileSystemAccessWrite},
			Scope: safety.GrantScopeSession,
		}},
	})
	st := NewState(t.TempDir())
	st.SetRuntimeValue("permission_snapshot_for_session", store.SnapshotForSession)
	if !st.PathGranted(file, safety.FileSystemAccessRead, "session-1", "") {
		t.Fatalf("grant not recognized: %+v", store.SnapshotForSession("session-1").FileSystemGrants)
	}
	if _, err := resolveReadFilePath(llm.WithAgentSessionID(context.Background(), "session-1"), st, file); err != nil {
		t.Fatalf("write grant should include read access: %v", err)
	}
}

func TestPermissionUpdateFromApprovedActionDropsWideningSubset(t *testing.T) {
	requested := t.TempDir()
	payload, _ := json.Marshal(map[string]any{
		"scope": "turn",
		"cwd":   requested,
		"permissions": safety.RequestPermissionProfile{FileSystem: &safety.FileSystemPermissionProfile{Entries: []safety.FileSystemPermissionEntry{
			{Path: safety.FileSystemPermissionPath{Path: requested}, Access: safety.FileSystemAccessRead},
		}}},
	})
	answer, _ := json.Marshal(safety.RequestPermissionsResponse{
		Scope: safety.GrantScopeSession,
		Permissions: safety.RequestPermissionProfile{FileSystem: &safety.FileSystemPermissionProfile{Entries: []safety.FileSystemPermissionEntry{
			{Path: safety.FileSystemPermissionPath{Path: filepath.Dir(requested)}, Access: safety.FileSystemAccessRead},
		}}},
	})
	if update, ok := PermissionUpdateFromApprovedAction(requestPermissionsActionKind, string(payload), string(answer), "run-1"); ok {
		t.Fatalf("out-of-scope grant was retained: %+v", update)
	}
}

func TestPermissionUpdateFromApprovedActionUsesGrantedSubset(t *testing.T) {
	requested := t.TempDir()
	granted := filepath.Join(requested, "public")
	payload, _ := json.Marshal(map[string]any{
		"scope": "session",
		"cwd":   requested,
		"permissions": safety.RequestPermissionProfile{FileSystem: &safety.FileSystemPermissionProfile{Entries: []safety.FileSystemPermissionEntry{
			{Path: safety.FileSystemPermissionPath{Path: requested}, Access: safety.FileSystemAccessRead},
		}}},
	})
	answer, _ := json.Marshal(safety.RequestPermissionsResponse{
		Scope: safety.GrantScopeSession,
		Permissions: safety.RequestPermissionProfile{FileSystem: &safety.FileSystemPermissionProfile{Entries: []safety.FileSystemPermissionEntry{
			{Path: safety.FileSystemPermissionPath{Path: granted}, Access: safety.FileSystemAccessRead},
		}}},
	})
	update, ok := PermissionUpdateFromApprovedAction(requestPermissionsActionKind, string(payload), string(answer), "run-1")
	if !ok || len(update.FileSystemGrants) != 1 || update.FileSystemGrants[0].Entry.Path.Path != granted {
		t.Fatalf("update=%+v ok=%v", update, ok)
	}
}

func TestPermissionUpdateFromApprovedActionScopesStrictReviewToTurn(t *testing.T) {
	requested := t.TempDir()
	payload, _ := json.Marshal(map[string]any{
		"cwd": requested,
		"permissions": safety.RequestPermissionProfile{FileSystem: &safety.FileSystemPermissionProfile{Entries: []safety.FileSystemPermissionEntry{{
			Path: safety.FileSystemPermissionPath{Path: requested}, Access: safety.FileSystemAccessRead,
		}}}},
	})
	answer, _ := json.Marshal(safety.RequestPermissionsResponse{
		Scope:            safety.GrantScopeTurn,
		StrictAutoReview: true,
		Permissions: safety.RequestPermissionProfile{FileSystem: &safety.FileSystemPermissionProfile{Entries: []safety.FileSystemPermissionEntry{{
			Path: safety.FileSystemPermissionPath{Path: requested}, Access: safety.FileSystemAccessRead,
		}}}},
	})
	update, ok := PermissionUpdateFromApprovedAction(requestPermissionsActionKind, string(payload), string(answer), "run-strict")
	if !ok || update.StrictAutoReviewRunID != "run-strict" || len(update.FileSystemGrants) != 1 {
		t.Fatalf("update=%+v ok=%v", update, ok)
	}

	var sessionResponse safety.RequestPermissionsResponse
	if err := json.Unmarshal(answer, &sessionResponse); err != nil {
		t.Fatal(err)
	}
	sessionResponse.Scope = safety.GrantScopeSession
	sessionAnswer, _ := json.Marshal(sessionResponse)
	if update, ok := PermissionUpdateFromApprovedAction(requestPermissionsActionKind, string(payload), string(sessionAnswer), "run-strict"); ok {
		t.Fatalf("session-scoped strict review must grant nothing: %+v", update)
	}
}

func TestBeginApprovalContinuationRunsFenceOnce(t *testing.T) {
	var calls atomic.Int32
	ctx := WithToolApprovalResume(context.Background(), &ToolApprovalResumeState{
		BeginContinuation: func(context.Context) error {
			calls.Add(1)
			return nil
		},
	})
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := BeginApprovalContinuation(ctx); err != nil {
				t.Errorf("BeginApprovalContinuation: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("fence calls=%d, want 1", calls.Load())
	}
}

// How many memories a search returns is the operator's to set, bounded, and not
// the model's to widen.
//
// Recall is wide and ranked, so the first few matches carry nearly all the
// value while the rest still cost the model context. That budget therefore
// lives in configuration rather than in the tool's arguments, a configuration
// asking for more than the ceiling is clamped rather than refused, and what
// falls outside the budget is dropped rather than offered as another page.
func TestMemorySearchReturnsTheConfiguredTopK(t *testing.T) {
	home := t.TempDir()
	root := testMemoryScopeRoot(t, home)
	if err := os.MkdirAll(root.MemoryRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for index := 0; index < 30; index++ {
		lines = append(lines, fmt.Sprintf("- deploy note %d", index))
	}
	body := strings.Join(lines, "\n") + "\n" + strings.Repeat(memoryFiller, 200)
	if err := os.WriteFile(filepath.Join(root.MemoryRoot, "MEMORY.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	search := func(t *testing.T, cfg *appcfg.Root) memory.SearchResponse {
		t.Helper()
		tools, err := newDedicatedMemoryTools(NewState(home), memorySearchRuntime(t, &AgentToolRuntime{
			Home: home, WorkspaceRoot: home, ProjectKey: testMemoryProjectKey, Cfg: cfg,
		}))
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range tools {
			if tool.Name() != "memories_search" {
				continue
			}
			result, err := tool.Handle(context.Background(), `{"queries":["deploy"]}`)
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			response, ok := result.(memory.SearchResponse)
			if !ok {
				t.Fatalf("response = %#v", result)
			}
			return response
		}
		t.Fatal("memories_search was not registered")
		return memory.SearchResponse{}
	}
	topK := func(n int) *appcfg.Root {
		return &appcfg.Root{Memories: appcfg.MemorySection{SearchTopK: &n}}
	}

	byDefault := search(t, &appcfg.Root{})
	if len(byDefault.Matches) != appcfg.MemoriesConfig((&appcfg.Root{}).EffectiveMemories()).SearchTopK {
		t.Fatalf("default = %d matches, want the configured default %d",
			len(byDefault.Matches), (&appcfg.Root{}).EffectiveMemories().SearchTopK)
	}

	configured := search(t, topK(3))
	if len(configured.Matches) != 3 {
		t.Fatalf("configured top_k=3 returned %d matches", len(configured.Matches))
	}

	// The corpus holds 30 matching lines, so a request for 30 can only be
	// capped by the ceiling rather than by running out of matches.
	clamped := search(t, topK(30))
	if len(clamped.Matches) != memory.MaxSearchResults {
		t.Fatalf("configured top_k=30 returned %d matches, want the ceiling %d",
			len(clamped.Matches), memory.MaxSearchResults)
	}
}

// TestSessionTodoPlanUpdateLabelsFoldToSingleLine pins the semantics of the
// plan-card labels: a title or content is one line by definition, so a
// model that writes a tab or a newline into one gets it folded to a space when
// the label becomes UI text, not passed through to the terminal row below.
func TestSessionTodoPlanUpdateLabelsFoldToSingleLine(t *testing.T) {
	st := NewState()
	var got StepEvent
	st.SetStepHook(func(ctx context.Context, evt StepEvent) {
		got = evt
	})
	tool, err := newSessionTodoTool(st, t.TempDir())
	if err != nil {
		t.Fatalf("newSessionTodoTool: %v", err)
	}
	_, err = tool.Handle(llm.WithAgentSessionID(context.Background(), "s1"),
		`{"action":"set","items":[{"id":"1","content":"write\nthe\ttests","status":"in_progress","title":"writing\nthe\ttests"},{"id":"2","content":"  spaced   out  ","status":"pending"}]}`)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got.PlanUpdate == nil {
		t.Fatalf("expected plan update, got %#v", got)
	}
	if got.PlanUpdate.Explanation != "writing the tests" {
		t.Fatalf("explanation must be one folded line, got %q", got.PlanUpdate.Explanation)
	}
	if strings.ContainsAny(got.PlanUpdate.Explanation, "\n\t\r") {
		t.Fatalf("explanation carries a raw whitespace control: %q", got.PlanUpdate.Explanation)
	}
	if got.PlanUpdate.Items[0].Content != "write the tests" || got.PlanUpdate.Items[0].Active != "writing the tests" {
		t.Fatalf("item labels must fold to single lines: %#v", got.PlanUpdate.Items[0])
	}
	if got.PlanUpdate.Items[1].Content != "spaced out" {
		t.Fatalf("whitespace runs must collapse to one space: %q", got.PlanUpdate.Items[1].Content)
	}
}
