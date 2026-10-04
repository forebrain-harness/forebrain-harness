package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/lsp"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
)

// runRootCommand executes one forebrain command line against an isolated
// FOREBRAIN_HOME, returning what it printed. rootCmd is global, so the args
// and the output go back the way they were when the test ends.
func runRootCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	t.Setenv("FOREBRAIN_HOME", t.TempDir())
	process.ResetResolve()
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		process.ResetResolve()
	})
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	return buf.String(), err
}

// activeWorkspace is the isolated home's active agent workspace, the one the
// subcommands just acted on.
func activeWorkspace(t *testing.T) string {
	t.Helper()
	workspace, err := process.ActiveAgentWorkspace()
	if err != nil {
		t.Fatalf("ActiveAgentWorkspace: %v", err)
	}
	return workspace
}

func TestLSPListShowsCatalog(t *testing.T) {
	out, err := runRootCommand(t, "lsp", "list")
	if err != nil {
		t.Fatalf("lsp list: %v", err)
	}
	if !strings.Contains(out, "ID") {
		t.Fatalf("lsp list output has no header:\n%s", out)
	}
	for _, id := range []string{"gopls", "pyright"} {
		if !containsLineWithPrefix(out, id) {
			t.Fatalf("lsp list output has no %s row:\n%s", id, out)
		}
	}
	if !strings.Contains(out, "Not inside a project") && !strings.Contains(out, "not trusted") {
		t.Fatalf("lsp list output has no project footer:\n%s", out)
	}
}

func TestLSPEnableDisable(t *testing.T) {
	out, err := runRootCommand(t, "lsp", "enable", "gopls")
	if err != nil {
		t.Fatalf("lsp enable gopls: %v", err)
	}
	first := strings.SplitN(out, "\n", 2)[0]
	if !strings.HasPrefix(first, "Enabled gopls.") {
		t.Fatalf("first line = %q, want it to start with %q", first, "Enabled gopls.")
	}
	if enabled, err := lsp.LoadEnabled(activeWorkspace(t)); err != nil || !enabled["gopls"] {
		t.Fatalf("enabled.json after enable = %v (err %v), want gopls: true", enabled, err)
	}

	if out, err = runRootCommand(t, "lsp", "disable", "gopls"); err != nil {
		t.Fatalf("lsp disable gopls: %v (output %q)", err, out)
	}
	if enabled, err := lsp.LoadEnabled(activeWorkspace(t)); err != nil || enabled["gopls"] {
		t.Fatalf("enabled.json after disable = %v (err %v), want gopls: false", enabled, err)
	}
}

func TestLSPEnableUnknown(t *testing.T) {
	_, err := runRootCommand(t, "lsp", "enable", "nope")
	if err == nil || err.Error() != `unknown language server "nope"; run forebrain lsp list` {
		t.Fatalf("error = %v, want the unknown-server text", err)
	}
}

func TestLSPInstallNeedsYesWithoutTerminal(t *testing.T) {
	prevTTY := rootIsTerminal
	rootIsTerminal = func(*os.File) bool { return false }
	t.Cleanup(func() { rootIsTerminal = prevTTY })

	_, err := runRootCommand(t, "lsp", "install", "gopls")
	if err == nil || err.Error() != "pass --yes to install without a prompt" {
		t.Fatalf("error = %v, want %q", err, "pass --yes to install without a prompt")
	}
	// That error is the guard itself: the command returned before it could
	// run any installer, and no state was touched.
	if enabled, rerr := lsp.LoadEnabled(activeWorkspace(t)); rerr != nil || len(enabled) != 0 {
		t.Fatalf("the aborted install touched state: %v (err %v)", enabled, rerr)
	}
}

func TestLSPDoctorNothingEnabled(t *testing.T) {
	out, err := runRootCommand(t, "lsp", "doctor", "--no-handshake")
	if err != nil {
		t.Fatalf("lsp doctor with nothing enabled: %v", err)
	}
	if !strings.HasPrefix(out, "No language server is enabled.") {
		t.Fatalf("output = %q, want the nothing-enabled note", out)
	}
}

// containsLineWithPrefix reports whether any output line starts with s
// (tabwriter padding follows the id, so plain Contains would be ambiguous).
func containsLineWithPrefix(out, s string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, s) {
			return true
		}
	}
	return false
}

// `forebrain lsp recommendations reset` clears everything the
// recommendations state file remembers, and the group itself demands its
// subcommand.
func TestLSPRecommendationsReset(t *testing.T) {
	if _, err := runRootCommand(t, "lsp", "recommendations"); err == nil || !strings.Contains(err.Error(), "forebrain lsp recommendations needs a subcommand: reset") {
		t.Fatalf("group without a subcommand error = %v", err)
	}

	// A state file that says recommendations are off and gopls is never to
	// be suggested again.
	ws := activeWorkspace(t)
	if err := os.MkdirAll(filepath.Join(ws, "state", "lsp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "state", "lsp", "recommendations.json"),
		[]byte(`{"disabled":true,"disabled_reason":"turned off by the user","dismissed_streak":3,"never":["gopls"]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runRootCommand(t, "lsp", "recommendations", "reset")
	if err != nil {
		t.Fatalf("lsp recommendations reset: %v (output %q)", err, out)
	}
	want := "Language server recommendations are back on; servers marked \"never\" can be suggested again.\n"
	if out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
	data, err := os.ReadFile(filepath.Join(activeWorkspace(t), "state", "lsp", "recommendations.json"))
	if err != nil {
		t.Fatalf("read recommendations.json: %v", err)
	}
	var st struct {
		Disabled        bool     `json:"disabled"`
		DisabledReason  string   `json:"disabled_reason"`
		DismissedStreak int      `json:"dismissed_streak"`
		Never           []string `json:"never"`
	}
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("recommendations.json = %s: %v", data, err)
	}
	if st.Disabled || st.DisabledReason != "" || st.DismissedStreak != 0 || len(st.Never) != 0 {
		t.Fatalf("recommendations.json = %s, want every field reset", data)
	}
}
