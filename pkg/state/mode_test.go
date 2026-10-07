package state

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Switching from a UI, a slash command, or an approval must leave the plan-mode
// bookkeeping intact: a full-record Set erased the pre-plan mode that
// exit_plan_mode restores and the exited flag that makes the next
// enter_plan_mode re-read the existing plan instead of overwriting it.
func TestSwitchPreservesPlanBookkeeping(t *testing.T) {
	home := t.TempDir()
	sid := "sid-switch"

	entered, err := Switch(home, sid, ModePlan)
	if err != nil {
		t.Fatalf("Switch to plan: %v", err)
	}
	if entered.Mode != ModePlan || entered.Phase != "plan" {
		t.Fatalf("entered = %+v, want plan/plan", entered)
	}
	if entered.PrePlanMode != ModeAgent {
		t.Fatalf("PrePlanMode=%q, want agent", entered.PrePlanMode)
	}
	if entered.HasExitedPlan {
		t.Fatal("entering plan mode must clear the exited flag")
	}

	exited, err := Switch(home, sid, ModeAgent)
	if err != nil {
		t.Fatalf("Switch to agent: %v", err)
	}
	if exited.Mode != ModeAgent || exited.Phase != "" {
		t.Fatalf("exited = %+v, want agent with no phase", exited)
	}
	if !exited.HasExitedPlan {
		t.Fatal("leaving plan mode must record the exit so the next enter detects a re-entry")
	}

	stored, err := Get(home, sid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !stored.HasExitedPlan || stored.Mode != ModeAgent {
		t.Fatalf("stored = %+v, want persisted agent mode with the exited flag", stored)
	}

	// Re-entering plan mode from a non-plan mode stashes it again.
	reentered, err := Switch(home, sid, ModePlan)
	if err != nil {
		t.Fatalf("Switch back to plan: %v", err)
	}
	if reentered.PrePlanMode != ModeAgent || reentered.HasExitedPlan {
		t.Fatalf("reentered = %+v, want stashed pre-plan mode and cleared exit flag", reentered)
	}
}

// Switching to the mode already in effect must not overwrite the stashed
// pre-plan mode with "plan" itself.
func TestSwitchToPlanTwiceKeepsOriginalPrePlanMode(t *testing.T) {
	home := t.TempDir()
	sid := "sid-idempotent"
	if _, err := Switch(home, sid, ModePlan); err != nil {
		t.Fatalf("first: %v", err)
	}
	again, err := Switch(home, sid, ModePlan)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if again.PrePlanMode != ModeAgent {
		t.Fatalf("PrePlanMode=%q, want agent", again.PrePlanMode)
	}
}

func TestAllModesOnlyAgentAndPlan(t *testing.T) {
	expected := []Mode{ModeAgent, ModePlan}
	got := AllModes()
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("AllModes = %#v, want %#v", got, expected)
	}
	if IsKnownMode("coordinator") {
		t.Fatal("coordinator must not be a known public mode")
	}
	if CoerceMode("COORDINATOR") != ModeAgent {
		t.Fatal("coordinator should normalize through the generic invalid-mode path to agent")
	}
}

func TestIsKnownMode_Agent(t *testing.T) {
	if !IsKnownMode("agent") {
		t.Error("expected 'agent' to be known")
	}
}

func TestIsKnownMode_Plan(t *testing.T) {
	if !IsKnownMode("plan") {
		t.Error("expected 'plan' to be known")
	}
}

func TestIsKnownMode_Uppercase(t *testing.T) {
	if !IsKnownMode("AGENT") {
		t.Error("expected 'AGENT' (uppercase) to be known")
	}
}

func TestIsKnownMode_Unknown(t *testing.T) {
	if IsKnownMode("unknown_mode") {
		t.Error("expected unknown mode to return false")
	}
}

func TestIsKnownMode_Empty(t *testing.T) {
	if IsKnownMode("") {
		t.Error("expected empty string to return false")
	}
}

func TestIsKnownMode_Whitespace(t *testing.T) {
	if IsKnownMode("   ") {
		t.Error("expected whitespace to return false")
	}
}

func TestCoerceMode_Agent(t *testing.T) {
	if CoerceMode("agent") != ModeAgent {
		t.Error("expected coerce to return ModeAgent")
	}
}

func TestCoerceMode_Plan(t *testing.T) {
	if CoerceMode("PLAN") != ModePlan {
		t.Error("expected uppercase PLAN to coerce to ModePlan")
	}
}

func TestCoerceMode_UnknownDefaultsToAgent(t *testing.T) {
	if CoerceMode("foobar") != ModeAgent {
		t.Error("expected unknown mode to default to ModeAgent")
	}
}

func TestCoerceMode_EmptyDefaultsToAgent(t *testing.T) {
	if CoerceMode("") != ModeAgent {
		t.Error("expected empty mode to default to ModeAgent")
	}
}

func TestCoerceMode_WhitespaceDefaultsToAgent(t *testing.T) {
	if CoerceMode("  ") != ModeAgent {
		t.Error("expected whitespace mode to default to ModeAgent")
	}
}

// --- Get / Set ---

func TestGet_NonExistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	st, err := Get(tmpDir, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st.Mode != ModeAgent {
		t.Errorf("expected default ModeAgent, got %q", st.Mode)
	}
}

func TestGet_SetAndGet(t *testing.T) {
	tmpDir := t.TempDir()
	err := Set(tmpDir, "test1", State{Mode: ModePlan, Phase: "brainstorming"})
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	st, err := Get(tmpDir, "test1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if st.Mode != ModePlan {
		t.Errorf("expected ModePlan, got %q", st.Mode)
	}
	if st.Phase != "brainstorming" {
		t.Errorf("expected phase 'brainstorming', got %q", st.Phase)
	}
	if st.UpdatedAt <= 0 {
		t.Error("expected UpdatedAt to be set")
	}
}

func TestGet_DefaultsToAgentOnEmptyMode(t *testing.T) {
	tmpDir := t.TempDir()
	// Write a JSON with empty mode
	p := filepath.Join(tmpDir, "state", "modes", "empty_mode.json")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(`{"mode":"","phase":"test"}`), 0o600)
	st, err := Get(tmpDir, "empty_mode")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if st.Mode != ModeAgent {
		t.Errorf("expected empty mode to default to ModeAgent, got %q", st.Mode)
	}
}

func TestGet_DefaultsToAgentOnInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	p := filepath.Join(tmpDir, "state", "modes", "bad_json.json")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(`not valid json`), 0o600)
	st, err := Get(tmpDir, "bad_json")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if st.Mode != ModeAgent {
		t.Errorf("expected invalid JSON to default to ModeAgent, got %q", st.Mode)
	}
}

func TestGet_DefaultSessionID(t *testing.T) {
	tmpDir := t.TempDir()
	err := Set(tmpDir, "", State{Mode: ModePlan})
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	st, err := Get(tmpDir, "")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if st.Mode != ModePlan {
		t.Errorf("expected ModePlan, got %q", st.Mode)
	}
}

func TestGet_NormalizesRemovedModesToAgent(t *testing.T) {
	tmpDir := t.TempDir()
	p := filepath.Join(tmpDir, "state", "modes", "old.json")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(`{"mode":"review","phase":"legacy"}`), 0o600)
	st, err := Get(tmpDir, "old")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if st.Mode != ModeAgent {
		t.Errorf("expected removed mode to normalize to ModeAgent, got %q", st.Mode)
	}
}

func TestSet_NormalizesUnknownMode(t *testing.T) {
	tmpDir := t.TempDir()
	err := Set(tmpDir, "norm_test", State{Mode: Mode("foobar")})
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	st, err := Get(tmpDir, "norm_test")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if st.Mode != ModeAgent {
		t.Errorf("expected unknown mode normalized to ModeAgent, got %q", st.Mode)
	}
}

func TestGet_ReturnsReadError(t *testing.T) {
	tmpDir := t.TempDir()
	p := filepath.Join(tmpDir, "state", "modes", "dir_session.json")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir mode path: %v", err)
	}
	if _, err := Get(tmpDir, "dir_session"); err == nil {
		t.Fatal("expected read error for directory mode path")
	}
}

func TestSet_DefaultsEmptyMode(t *testing.T) {
	tmpDir := t.TempDir()
	if err := Set(tmpDir, "empty_set", State{}); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	st, err := Get(tmpDir, "empty_set")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if st.Mode != ModeAgent {
		t.Fatalf("empty mode stored as %q, want %q", st.Mode, ModeAgent)
	}
}

func TestPath_DefaultSessionID(t *testing.T) {
	p := modePath("", "")
	if filepath.Base(p) != "default.json" {
		t.Errorf("expected base 'default.json', got %q", filepath.Base(p))
	}
}

func TestPath_SessionWithSpaces(t *testing.T) {
	p := modePath("", "  test  ")
	if filepath.Base(p) != "test.json" {
		t.Errorf("expected base 'test.json', got %q", filepath.Base(p))
	}
}

func TestPrePlanModeRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	if err := Set(tmp, "ppm", State{Mode: ModePlan, PrePlanMode: ModeAgent}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	st, err := Get(tmp, "ppm")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if st.PrePlanMode != ModeAgent {
		t.Errorf("PrePlanMode=%q want agent", st.PrePlanMode)
	}
}

func TestBackCompatReadMissingPrePlanMode(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "state", "modes", "old.json")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(`{"mode":"agent"}`), 0o600)
	st, err := Get(tmp, "old")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if st.PrePlanMode != "" {
		t.Errorf("PrePlanMode=%q want empty", st.PrePlanMode)
	}
}

func TestPlanTurnCountRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	if err := Set(tmp, "ptc", State{Mode: ModePlan, PlanTurnCount: 7}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	st, _ := Get(tmp, "ptc")
	if st.PlanTurnCount != 7 {
		t.Errorf("PlanTurnCount=%d want 7", st.PlanTurnCount)
	}
}

func TestSetNormalizesPrePlanMode(t *testing.T) {
	tmp := t.TempDir()
	if err := Set(tmp, "npp", State{Mode: ModePlan, PrePlanMode: Mode("FoOBaR")}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	st, _ := Get(tmp, "npp")
	if st.PrePlanMode != ModeAgent {
		t.Errorf("PrePlanMode=%q want agent (normalized)", st.PrePlanMode)
	}
}

// The helpers below were production functions that only the tests in this
// package ever called. They live here so the production files carry no
// unused code while the tests keep exercising the live code underneath.

func AllModes() []Mode {
	return []Mode{ModeAgent, ModePlan}
}

func IsKnownMode(raw string) bool {
	s := strings.ToLower(strings.TrimSpace(raw))
	for _, m := range AllModes() {
		if s == string(m) {
			return true
		}
	}
	return false
}

func TestGetMissingReturnsZero(t *testing.T) {
	home := t.TempDir()
	st, err := Fast(home, "s1")
	if err != nil {
		t.Fatalf("Get on missing session: %v", err)
	}
	if st.Enabled {
		t.Fatalf("expected Enabled=false on missing, got true")
	}
}

func TestSetThenGetRoundTrip(t *testing.T) {
	home := t.TempDir()
	if err := SetFast(home, "s1", true); err != nil {
		t.Fatalf("Set true: %v", err)
	}
	st, err := Fast(home, "s1")
	if err != nil {
		t.Fatalf("Get after Set: %v", err)
	}
	if !st.Enabled {
		t.Fatalf("expected Enabled=true, got false")
	}
	if st.UpdatedAt == 0 {
		t.Fatalf("expected UpdatedAt to be set, got 0")
	}
}

func TestSetFalseOverridesTrue(t *testing.T) {
	home := t.TempDir()
	if err := SetFast(home, "s1", true); err != nil {
		t.Fatalf("Set true: %v", err)
	}
	if err := SetFast(home, "s1", false); err != nil {
		t.Fatalf("Set false: %v", err)
	}
	st, err := Fast(home, "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if st.Enabled {
		t.Fatalf("expected Enabled=false after override, got true")
	}
}

func TestSessionsAreIsolated(t *testing.T) {
	home := t.TempDir()
	if err := SetFast(home, "s1", true); err != nil {
		t.Fatalf("Set s1: %v", err)
	}
	st2, err := Fast(home, "s2")
	if err != nil {
		t.Fatalf("Get s2: %v", err)
	}
	if st2.Enabled {
		t.Fatalf("expected s2 Enabled=false, got true")
	}
}

func TestEmptySessionDefaultsToDefault(t *testing.T) {
	home := t.TempDir()
	if err := SetFast(home, "", true); err != nil {
		t.Fatalf("Set empty session: %v", err)
	}
	st, err := Fast(home, "default")
	if err != nil {
		t.Fatalf("Get default: %v", err)
	}
	if !st.Enabled {
		t.Fatalf("expected default Enabled=true, got false")
	}
}

func TestPathLayout(t *testing.T) {
	home := t.TempDir()
	if err := SetFast(home, "abc", true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	want := filepath.Join(home, "state", "fast", "abc.json")
	if _, err := Fast(home, "abc"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := fastPath(home, "abc"); got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

// TestRemoveSessionStateFiles pins the whole on-disk footprint a session
// keeps under one agent's state root: all four files go, and a session that
// never wrote some of them still deletes cleanly — removal must be
// repeatable, not a mirror of how the session happened to be used.
func TestRemoveSessionStateFiles(t *testing.T) {
	root := t.TempDir()
	sid := "sid-remove"

	if err := Set(root, sid, State{Mode: ModePlan}); err != nil {
		t.Fatal(err)
	}
	if err := SetFast(root, sid, true); err != nil {
		t.Fatal(err)
	}
	if err := Save(root, sid, List{Items: []Item{{ID: "1", Content: "step"}}}); err != nil {
		t.Fatal(err)
	}
	if err := Append(root, sid, "a note"); err != nil {
		t.Fatal(err)
	}

	if err := RemoveSessionStateFiles(root, sid); err != nil {
		t.Fatalf("RemoveSessionStateFiles: %v", err)
	}
	for _, p := range []string{modePath(root, sid), fastPath(root, sid), todoPath(root, sid), intermediatePath(root, sid)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after removal", p)
		}
	}

	// A session that never wrote any of the files is not an error.
	if err := RemoveSessionStateFiles(root, "never-existed"); err != nil {
		t.Fatalf("removing a file-less session = %v, want nil", err)
	}
}
