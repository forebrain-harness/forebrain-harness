package architecture_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/architecture"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

func TestTUIEntrypointDoesNotImportCoreRuntimeInternals(t *testing.T) {
	repoRoot := findRepoRoot(t)
	targetRoot := filepath.Join(repoRoot, "pkg", "tui")
	if info, err := os.Stat(targetRoot); err != nil || !info.IsDir() {
		t.Fatalf("TUI entrypoint %s is unavailable: %v", targetRoot, err)
	}
	forbidden := []string{
		"github.com/forebrain-harness/forebrain-harness/pkg/gateway",
		"github.com/forebrain-harness/forebrain-harness/pkg/agentrun",
		"github.com/forebrain-harness/forebrain-harness/pkg/clifacade",
		"github.com/forebrain-harness/forebrain-harness/pkg/forkagent",
		"github.com/forebrain-harness/forebrain-harness/pkg/supervisorrun",
	}

	var violations []string
	err := filepath.WalkDir(targetRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fs := token.NewFileSet()
		file, perr := parser.ParseFile(fs, path, nil, parser.ImportsOnly)
		if perr != nil {
			violations = append(violations, "parse error: "+path+": "+perr.Error())
			return nil
		}
		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbidden {
				if importPath == bad {
					rel, _ := filepath.Rel(repoRoot, path)
					violations = append(violations, rel+" imports forbidden "+bad)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk TUI entrypoint: %v", err)
	}

	if len(violations) > 0 {
		t.Fatalf("entrypoint boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestNoTopLevelInternal(t *testing.T) {
	root := findRepoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "internal")); !os.IsNotExist(err) {
		t.Fatalf("top-level internal directory must not exist: %v", err)
	}
}

func TestAgentRunDoesNotImportSurfaces(t *testing.T) {
	root := findRepoRoot(t)
	checkNoImports(t, filepath.Join(root, "pkg", "run"), []string{
		"github.com/forebrain-harness/forebrain-harness/pkg/tui",
		"github.com/forebrain-harness/forebrain-harness/pkg/gateway",
	})
}

func TestGatewayDoesNotImportTUI(t *testing.T) {
	root := findRepoRoot(t)
	checkNoImports(t, filepath.Join(root, "pkg", "gateway"), []string{
		"github.com/forebrain-harness/forebrain-harness/pkg/tui",
	})
}

func checkNoImports(t *testing.T, root string, forbidden []string) {
	t.Helper()
	bad := make(map[string]struct{}, len(forbidden))
	for _, path := range forbidden {
		bad[path] = struct{}{}
	}
	var violations []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			if _, ok := bad[strings.Trim(imp.Path.Value, `"`)]; ok {
				violations = append(violations, path+" imports "+imp.Path.Value)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) > 0 {
		t.Fatalf("surface dependency violations:\n%s", strings.Join(violations, "\n"))
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	cur := wd
	for {
		if _, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			t.Fatalf("could not find repo root from %s", wd)
		}
		cur = parent
	}
}

// fanOutBudget is §3.7's fan-out table expressed as a ratchet, in the same
// spirit as TestRunnerOnlyShrinks.
//
// Four packages are over their §3.7 target today, and that is not a surprise:
// tui and gateway may only import process, turn, event, config, llm and
// channel (six), and they currently reach eighteen packages each because the
// P4–P7 decomposition that would pull that logic down into turn/session/run
// has not landed. Asserting the target outright would just add a permanently
// red test, and rewriting the target to match reality would throw away the
// goal.
//
// So this asserts the *measured* number instead: current may not grow, and
// when it drops the constant has to be lowered with it, which keeps the gap
// between reality and target visible and measured in CI rather than buried in
// prose. A package already at or under its §3.7 target is pinned at that
// target and may shrink further for free; only a package carrying debt has to
// keep its recorded ceiling in step with reality.
type fanOutBudget struct {
	// current is the ceiling this test enforces. Lower it whenever the real
	// number drops; never raise it.
	current int
	// target is §3.7's limit, or -1 for process, which §3.7 exempts as the
	// composition root.
	target int
}

var fanOutBudgets = map[string]fanOutBudget{
	"agent": {current: 1, target: 1},
	"llm":   {current: 8, target: 8},

	"state":     {current: 8, target: 8},
	"config":    {current: 8, target: 8},
	"home":      {current: 8, target: 8},
	"telemetry": {current: 8, target: 8},
	"channel":   {current: 8, target: 8},

	"tool":   {current: 8, target: 8},
	"hook":   {current: 8, target: 8},
	"mcp":    {current: 8, target: 8},
	"event":  {current: 8, target: 8},
	"safety": {current: 8, target: 8},
	"skill":  {current: 8, target: 8},
	"memory": {current: 8, target: 8},
	// migrate's dependency set is the migration contract itself: the four
	// Layer-2 capabilities it reuses plus state/config/llm for writing the
	// imported history. The ceiling is the measured number; it may not grow,
	// and run/process are reached only through injected callbacks (B8).
	// migrate's dependency set is the migration contract itself: the five
	// Layer-2 capabilities it reuses (memory, skill, mcp, tool's durable
	// ToolMeta, safety) plus state/config/llm for writing the imported
	// history. The ceiling is the measured number; it may not grow, and
	// run/process are reached only through injected callbacks (B8).
	"migrate":  {current: 8, target: 8},
	"assembly": {current: 9, target: 8}, // over by one

	"session": {current: 6, target: 6},
	"turn":    {current: 12, target: 12},
	"run":     {current: 14, target: 12}, // over by two

	// The composition root gained pkg/mcp as a deliberate direct dependency:
	// resolving the session's effective MCP list (project files, consents,
	// scope stamps) is assembly work that belongs here, not in a lower layer.
	"process": {current: 18, target: -1}, // §3.7 sets no limit for the composition root

	// The P4–P7 gap. Both may import only process, turn, event, config, llm
	// and channel; both currently reach eighteen. TUI is at nineteen: /migrate
	// is terminal-only and drives the migrate package directly, a dependency
	// the gateway deliberately does not take.
	"tui":     {current: 19, target: 6},
	"gateway": {current: 18, target: 6},
}

func TestFanOutOnlyShrinks(t *testing.T) {
	root := findRepoRoot(t)
	const modulePrefix = "github.com/forebrain-harness/forebrain-harness/pkg/"

	for name, budget := range fanOutBudgets {
		deps := map[string]bool{}
		dir := filepath.Join(root, "pkg", name)
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if perr != nil {
				return perr
			}
			for _, imp := range file.Imports {
				importPath := strings.Trim(imp.Path.Value, `"`)
				if !strings.HasPrefix(importPath, modulePrefix) {
					continue
				}
				if dep := strings.Split(strings.TrimPrefix(importPath, modulePrefix), "/")[0]; dep != name {
					deps[dep] = true
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		got := len(deps)
		if got > budget.current {
			names := make([]string, 0, len(deps))
			for d := range deps {
				names = append(names, d)
			}
			sort.Strings(names)
			t.Errorf("pkg/%s fan-out grew to %d, ceiling is %d (§3.7 target %d): %s\n"+
				"Reach a lower layer through an interface the consumer defines and process injects, "+
				"rather than importing another package here.",
				name, got, budget.current, budget.target, strings.Join(names, ", "))
			continue
		}
		// The "must lower it" half only applies where the ratchet is actually
		// tracking debt — a package whose ceiling sits above its §3.7 target.
		// A package already at or under target has its ceiling pinned to the
		// target, and shrinking further below that is free.
		if budget.target >= 0 && budget.current > budget.target && got < budget.current {
			t.Errorf("pkg/%s fan-out dropped to %d but fanOutBudgets still says %d — lower it "+
				"(§3.7 target %d), otherwise the ratchet silently re-permits the dependencies "+
				"that were just paid off.",
				name, got, budget.current, budget.target)
		}
	}
}

// TestLayer3PackagesDoNotImportEachOther enforces P9-0e: session, turn and run
// are domain siblings (process ⊃ session ⊃ turn ⊃ run), but none of the three
// may import another's package. Each depends on the others only through a
// narrow interface it defines itself (RunExecutor, SessionContext,
// ActiveRunProbe, SessionRepository, SkillCommandHooks, ...), wired together by
// process, the sole composition root. A direct import here means a concrete
// dependency slipped back in behind the interface boundary.
func TestLayer3PackagesDoNotImportEachOther(t *testing.T) {
	root := findRepoRoot(t)
	layer3 := []string{"session", "turn", "run"}
	for _, from := range layer3 {
		var forbidden []string
		for _, to := range layer3 {
			if to == from {
				continue
			}
			forbidden = append(forbidden, "github.com/forebrain-harness/forebrain-harness/pkg/"+to)
		}
		checkNoImports(t, filepath.Join(root, "pkg", from), forbidden)
	}
}

// TestStateDoesNotImportEvent enforces C3: state only writes records. Turning
// step rows into canonical event.RunEvent values is a projection, and the
// projector belongs to turn (the canonical event dispatcher), not the
// persistence layer. See pkg/turn/event_projection.go.
func TestStateDoesNotImportEvent(t *testing.T) {
	root := findRepoRoot(t)
	checkNoImports(t, filepath.Join(root, "pkg", "state"), []string{
		"github.com/forebrain-harness/forebrain-harness/pkg/event",
	})
}

// TestEventDoesNotImportTool keeps the product event protocol a pure payload
// contract: it may describe permission suggestions and diff models, but it must
// not depend on the tool package's registry/execution machinery.
func TestEventDoesNotImportTool(t *testing.T) {
	root := findRepoRoot(t)
	checkNoImports(t, filepath.Join(root, "pkg", "event"), []string{
		"github.com/forebrain-harness/forebrain-harness/pkg/tool",
	})
}

// TestHomeFanOutIsZero enforces C2: home only resolves paths and directory
// layout. It must never trigger seeding (bundled skill installs, model catalog
// writes) — those calls belong in process, the composition root.
func TestHomeFanOutIsZero(t *testing.T) {
	root := findRepoRoot(t)
	checkOnlyStdlibImports(t, filepath.Join(root, "pkg", "home"))
}

// TestAgentFanOutIsLLMOnly enforces the Layer 0 SDK-purity requirement from
// appendix B: agent is a reusable agent kernel whose only forebrain dependency is
// llm. It must not depend on telemetry, tool, run, or anything above it —
// Layer 0's dependency closure has to stay contained to Layer 0 itself.
func TestAgentFanOutIsLLMOnly(t *testing.T) {
	root := findRepoRoot(t)
	allowed := map[string]struct{}{
		"github.com/forebrain-harness/forebrain-harness/pkg/llm": {},
	}
	checkOnlyForebrainImports(t, filepath.Join(root, "pkg", "agent"), allowed)
}

// checkOnlyStdlibImports fails if any production file in root imports a
// forebrain-harness/forebrain-harness package at all.
func checkOnlyStdlibImports(t *testing.T, root string) {
	t.Helper()
	checkOnlyForebrainImports(t, root, map[string]struct{}{})
}

// packageLayer is the layer assignment from docs/plan/TUI_FIRST_RUNTIME_REFACTOR.md §3.1
// and §11.1. A package may only import packages in a strictly lower layer,
// plus whatever same-layer exceptions are separately enforced elsewhere in
// this file (Layer 3's session/turn/run mutual exclusion, event-does-not-
// import-tool, skill-does-not-import-memory). architecture and testutil are
// deliberately absent: §3.2 calls them "non-domain" test-only packages exempt
// from the layer diagram.
var packageLayer = map[string]int{
	"agent": 0, "llm": 0,
	"state": 1, "config": 1, "home": 1, "telemetry": 1, "channel": 1,
	"tool": 2, "hook": 2, "mcp": 2, "event": 2, "assembly": 2, "safety": 2, "skill": 2, "memory": 2, "migrate": 2,
	"session": 3, "turn": 3, "run": 3,
	"process": 4,
	"tui":     5, "gateway": 5,
}

// TestLayerCeiling enforces §11.1's dependency table in general form: every
// pkg/ package may only import packages that sit in a strictly lower layer.
// This is the sweep that would have caught pkg/state -> pkg/event (C3),
// pkg/agent -> pkg/telemetry, and pkg/run -> pkg/turn before they shipped —
// each was a same-or-higher-layer import that no existing test scanned for.
func TestLayerCeiling(t *testing.T) {
	root := findRepoRoot(t)
	base := filepath.Join(root, "pkg")
	const modulePrefix = "github.com/forebrain-harness/forebrain-harness/pkg/"

	var violations []string
	for name, layer := range packageLayer {
		dir := filepath.Join(base, name)
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("layer table references missing package %q", name)
		}
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if perr != nil {
				return perr
			}
			for _, imp := range file.Imports {
				importPath := strings.Trim(imp.Path.Value, `"`)
				if !strings.HasPrefix(importPath, modulePrefix) {
					continue
				}
				importedName := strings.Split(strings.TrimPrefix(importPath, modulePrefix), "/")[0]
				importedLayer, known := packageLayer[importedName]
				if !known || importedName == name {
					continue
				}
				if importedLayer > layer {
					rel, _ := filepath.Rel(root, path)
					violations = append(violations, rel+" (layer "+itoa(layer)+") imports "+importPath+" (layer "+itoa(importedLayer)+")")
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(violations) > 0 {
		t.Fatalf("layer ceiling violations:\n%s", strings.Join(violations, "\n"))
	}
}

// TestSkillDoesNotImportMemory enforces C7: skill and memory are same-layer
// (Layer 2) siblings; skill must reach memory only through an interface it
// defines, injected by process, not by importing the memory package directly.
func TestSkillDoesNotImportMemory(t *testing.T) {
	root := findRepoRoot(t)
	checkNoImports(t, filepath.Join(root, "pkg", "skill"), []string{
		"github.com/forebrain-harness/forebrain-harness/pkg/memory",
	})
}

// TestToolAndHookDoNotImportAgent enforces C4. The design doc states the
// invariant as "tool, hook !-> agent 之上任何执行层", and P9-0b/P9-0c spent two
// tasks establishing it by inverting the dependency into tool-side ports
// (AgentSpawner, ForkRunner) that agentrun implements and process injects.
// The invariant holds today but nothing was enforcing it, so reintroducing a
// direct tool -> agent import would silently undo that work: these are Layer 2
// capability packages, and a capability that can construct an agent can spawn
// execution from underneath the layer that is supposed to own it.
func TestToolAndHookDoNotImportAgent(t *testing.T) {
	root := findRepoRoot(t)
	for _, pkg := range []string{"tool", "hook"} {
		checkNoImports(t, filepath.Join(root, "pkg", pkg), []string{
			"github.com/forebrain-harness/forebrain-harness/pkg/agent",
		})
	}
}

// TestSkillDoesNotImportTurn is the other half of C7/C8 (the design doc pairs
// them as "skill !-> memory, turn"). TestSkillDoesNotImportMemory already
// guards the memory side; turn was left unguarded even though it is the more
// dangerous direction, since turn sits a full layer above skill and importing
// it would invert the layering rather than merely couple two siblings.
func TestSkillDoesNotImportTurn(t *testing.T) {
	root := findRepoRoot(t)
	checkNoImports(t, filepath.Join(root, "pkg", "skill"), []string{
		"github.com/forebrain-harness/forebrain-harness/pkg/turn",
	})
}

// TestTUIDoesNotImportGateway is the other half of tui/gateway isolation:
// they are independent Layer 5 applications (§7 requires channel to start
// without the web UI), so neither may import the other.
func TestTUIDoesNotImportGateway(t *testing.T) {
	root := findRepoRoot(t)
	checkNoImports(t, filepath.Join(root, "pkg", "tui"), []string{
		"github.com/forebrain-harness/forebrain-harness/pkg/gateway",
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := [10]byte{}
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}

// checkOnlyForebrainImports fails if any production file in root imports a
// forebrain-harness/forebrain-harness package outside of allowed.
func checkOnlyForebrainImports(t *testing.T, root string, allowed map[string]struct{}) {
	t.Helper()
	const modulePrefix = "github.com/forebrain-harness/forebrain-harness/pkg/"
	var violations []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if !strings.HasPrefix(importPath, modulePrefix) {
				continue
			}
			if _, ok := allowed[importPath]; ok {
				continue
			}
			violations = append(violations, path+" imports "+importPath)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) > 0 {
		t.Fatalf("fan-out violations:\n%s", strings.Join(violations, "\n"))
	}
}

// sameLayerEdges is §3.6's exhaustive enumeration of the import edges allowed
// *within* a layer ("层内允许的边由实测 import 图导出，唯一枚举，其余禁止").
// TestLayerCeiling only enforces the other half of §3.6 — that no package
// imports a higher layer — so before this every same-layer edge was
// unconstrained. That gap is not theoretical: pkg/channel importing pkg/state
// would sail past the ceiling despite being exactly the sibling coupling C7
// forbids between skill and memory.
//
// This list is deliberately the measured graph rather than the design doc's
// prose, and it is tighter: hook -> tool, mcp -> safety, skill -> tool and
// state -> telemetry are all enumerated in §3.6 but no longer exist, having
// been removed over the P9 line. Recording the measured set is what keeps them
// from coming back.
var sameLayerEdges = map[string][]string{
	// Layer 0. agent -> llm is the whole of Layer 0's dependency closure,
	// which is the SDK property (TestAgentFanOutIsLLMOnly guards the rest).
	"agent": {"llm"},

	// Layer 1.
	"channel":   {"config"},
	"config":    {"home"},
	"state":     {"config"},
	"telemetry": {"home"},

	// Layer 2, whose topological order is
	// safety < event < tool < {hook, mcp, skill} < memory < assembly.
	"assembly": {"event", "hook", "safety", "skill", "tool"},
	"event":    {"safety"},
	"mcp":      {"tool"},
	"memory":   {"skill"},
	// migrate imports its same-layer siblings to reuse what they own: memory
	// for scope keys and the ad-hoc note store, skill for directory install
	// and digests, mcp for name comparison and consent records, tool for the
	// durable ToolMeta shape, and safety for project-trust facts. Nothing
	// above Layer 2 may appear here (consolidation and event persistence are
	// injected as callbacks by the composition root and the calling surface).
	"migrate": {"mcp", "memory", "safety", "skill", "tool"},
	// skill produces no tool metadata, so it needs nothing from event.
	"skill": {"safety"},
	// tool -> memory is the one edge that breaks the order above: it closes
	// the tool -> memory -> skill -> tool cycle §3.6 names, and P9-11a is the
	// task that removes it by moving the product tools out of tool. It is
	// listed so this test passes today, not because it is approved — when
	// P9-11a lands, delete it here and the ratchet below keeps it gone.
	"tool": {"event", "memory", "safety"},

	// Layer 3 has zero edges by design: session, turn and run connect only
	// through injected interfaces. TestLayer3PackagesDoNotImportEachOther
	// states that separately and more loudly, and both should stay.
}

// TestSameLayerEdgesAreEnumerated enforces §3.6's same-layer whitelist in both
// directions. An edge that is not listed fails as new coupling. An edge that is
// listed but no longer exists also fails, which makes this a ratchet like
// TestRunnerOnlyShrinks: removing a dependency forces the list to tighten, so
// it can never silently over-permit.
func TestSameLayerEdgesAreEnumerated(t *testing.T) {
	root := findRepoRoot(t)
	const modulePrefix = "github.com/forebrain-harness/forebrain-harness/pkg/"

	actual := map[string]map[string]bool{}
	for name, layer := range packageLayer {
		dir := filepath.Join(root, "pkg", name)
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if perr != nil {
				return perr
			}
			for _, imp := range file.Imports {
				importPath := strings.Trim(imp.Path.Value, `"`)
				if !strings.HasPrefix(importPath, modulePrefix) {
					continue
				}
				dep := strings.Split(strings.TrimPrefix(importPath, modulePrefix), "/")[0]
				if dep == name || packageLayer[dep] != layer {
					continue
				}
				if actual[name] == nil {
					actual[name] = map[string]bool{}
				}
				actual[name][dep] = true
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	allowed := map[string]map[string]bool{}
	for from, tos := range sameLayerEdges {
		allowed[from] = map[string]bool{}
		for _, to := range tos {
			allowed[from][to] = true
		}
	}

	var unlisted, stale []string
	for from, tos := range actual {
		for to := range tos {
			if !allowed[from][to] {
				unlisted = append(unlisted, from+" -> "+to)
			}
		}
	}
	for from, tos := range allowed {
		for to := range tos {
			if !actual[from][to] {
				stale = append(stale, from+" -> "+to)
			}
		}
	}
	sort.Strings(unlisted)
	sort.Strings(stale)

	if len(unlisted) > 0 {
		t.Errorf("same-layer import edges that §3.6 does not enumerate:\n  %s\n"+
			"Same-layer coupling has to be justified and added to sameLayerEdges, or "+
			"inverted into an interface the consumer defines and process injects.",
			strings.Join(unlisted, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("sameLayerEdges lists edges that no longer exist:\n  %s\n"+
			"Delete them: the list is a ratchet, and leaving a removed edge in it "+
			"silently re-permits the coupling that was just paid off.",
			strings.Join(stale, "\n  "))
	}
}

const maxPackageCount = 30

func TestPackageShape(t *testing.T) {
	root := findRepoRoot(t)
	base := filepath.Join(root, "pkg")
	packages := packageDirs(t, base)
	if len(packages) > maxPackageCount {
		t.Fatalf("pkg contains %d packages; maximum is %d", len(packages), maxPackageCount)
	}

	blacklist := map[string]bool{
		"adapters": true,
		"base":     true,
		"common":   true,
		"core":     true,
		"facade":   true,
		"host":     true,
		"manager":  true,
		"misc":     true,
		"utils":    true,
	}
	for _, dir := range packages {
		rel, err := filepath.Rel(base, dir)
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) > 2 || (len(parts) == 2 && strings.Join(parts, "/") != "llm/anthropic" && strings.Join(parts, "/") != "llm/openai") {
			t.Fatalf("package path %q is deeper than the allowed shape", filepath.ToSlash(rel))
		}
		if blacklist[parts[len(parts)-1]] {
			t.Fatalf("package name %q is too generic", parts[len(parts)-1])
		}
		if parts[len(parts)-1] == "testutil" {
			continue
		}
		if countProductionFiles(t, dir) < 2 {
			t.Fatalf("package %q has fewer than two production files", filepath.ToSlash(rel))
		}
	}
}

func TestLowerLayersDoNotImportSurfaces(t *testing.T) {
	root := findRepoRoot(t)
	for _, name := range []string{
		"agent", "assembly", "channel", "config", "event", "home", "hook", "llm", "mcp", "memory", "migrate", "process", "run", "safety", "session", "skill", "state", "telemetry", "tool", "turn",
	} {
		checkNoImports(t, filepath.Join(root, "pkg", name), []string{
			"github.com/forebrain-harness/forebrain-harness/pkg/tui",
			"github.com/forebrain-harness/forebrain-harness/pkg/gateway",
		})
	}
}

func packageDirs(t *testing.T, base string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != base && (entry.Name() == "node_modules" || entry.Name() == "system_assets") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		dir := filepath.Dir(path)
		for _, current := range out {
			if current == dir {
				return nil
			}
		}
		out = append(out, dir)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func countProductionFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		if _, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, entry.Name()), nil, parser.PackageClauseOnly); err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		count++
	}
	return count
}

// TestEveryPackageHasDocGo enforces P10-6. Every package carries a doc.go
// today, but nothing was checking, so the next package added would silently
// arrive without one. The requirement exists because this refactor's whole
// premise is that a package is a domain: doc.go is where "what domain is this,
// which file groups are inside, and why does the name fit" gets written down,
// and a package that cannot answer those questions is usually one that should
// not exist separately.
func TestEveryPackageHasDocGo(t *testing.T) {
	root := findRepoRoot(t)
	base := filepath.Join(root, "pkg")
	var missing []string
	for _, dir := range packageDirs(t, base) {
		if _, err := os.Stat(filepath.Join(dir, "doc.go")); err != nil {
			rel, _ := filepath.Rel(base, dir)
			missing = append(missing, filepath.ToSlash(rel))
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("packages without doc.go:\n  %s", strings.Join(missing, "\n  "))
	}
}

// Runner's recorded shape. Fields are a pure ratchet: lower whenever the real
// number drops, never raise it. Methods may grow when a genuinely new,
// necessary capability needs exposing -- the ratchet exists to catch
// accidental surface-area creep, not to freeze the type -- but each increase
// must be justified here, not just bumped to make the test pass.
//
// 22 fields is where the R-line lands and stays. R7's original criterion said
// "only main, mainCfg and Load()", but R8 -- dissolving the type -- was closed
// as not-to-do, and the owner separately ruled that mcp.Registry is keyed per
// Runner/session, so mcpReg lives here permanently. Every remaining field is
// one-per-Runner runtime state (the loaded agent, its tools, its MCP sessions,
// its permission runtime, its fork cache, this turn's bookkeeping); moving them
// out could only produce a side table keyed by Runner, which is worse. See the
// R7/R8 note in docs/plan/TUI_FIRST_REFACTOR_TASKS.md.
//
// 33 methods (was 32): MCPRegistry() was added to fix a real regression an
// ultrareview pass caught -- Runner.loadLocked mirrors every MCP session into
// mcp.GlobalRegistry() through Registry.Mirror, which deliberately stores no
// session (so closing the process-wide mirror can never tear down a
// connection some other Runner still owns). FormatMCPRuntimeSnapshot read
// exclusively from that global mirror, so "/mcp tools" and "/mcp resources"
// silently showed nothing on every surface: GetSession always failed because
// the mirror never has a session to give back. The fix routes those calls
// through the caller's own Runner-scoped registry instead, which still holds
// the session -- and that registry has to reach the caller somehow, so
// Runner needed a read accessor for mcpReg where it previously had none.
//
// 34 methods (was 33): ConsolidateMemoriesNow(ctx, sessionID) was added so a
// caller that just wrote memory notes to disk can run one synchronous
// consolidation pass and report the real outcome. The thin wrapper exists
// because the pass lives on the Runner's pipeline (memPipeline +
// buildMemoryPipeline), and the alternative — the caller rebuilding a
// Pipeline from outside pkg/run — would duplicate the assembly the Runner
// owns. It is a wrapper over LaunchMemoryStartup's existing dependency, not
// new surface the Runner did not already have.
//
// 24 fields (was 22): mcpLoad and mcpHub are the MCP startup generation and its
// status fan-out, and they have the same standing as mcpReg — per-Runner
// runtime state that could only be moved into a side table keyed by Runner.
// mcpLoad is what keeps r.main unpublished until the tool table is whole (a
// load whose servers are still starting), and mcpHub re-points a surface's
// subscription when a config reload replaces that generation with a new one.
// closed records that the Runner was shut down, which is what stops a load
// still in flight from publishing into a dead Runner.
//
// 36 methods (was 34): Close() is the Runner's own shutdown (the primary Runner
// had no way to release its MCP sessions, so its children outlived it), and
// MCPStartup() hands out the MCP startup view. That view carries the six calls a
// surface needs — Wait, Snapshot, Progress, Subscribe, CancelOptional,
// RequiredFailures — as methods on MCPStartup rather than on Runner, so this
// baseline grows by two instead of by eight.
//
// 25 fields (was 24): primaryState bundles the published effective
// primary-model snapshot with the fail-closed runtime-health gate. Both are
// atomics on one value type, so the model-selection transaction owns exactly
// one Runner field instead of two, and a literal-constructed Runner stays
// usable.
//
// 39 methods (was 36): SetPrimaryModel, ResetPrimaryModel and LoadConfig are
// the transactional model/config operations every surface funnels through —
// the runner owns the config-pointer swap and the rollback, which callers
// previously did by hand (and got wrong: a discarded rollback error left a
// half-mutated runtime).
const (
	runnerFields  = 25
	runnerMethods = 39
)

func TestRunnerOnlyShrinks(t *testing.T) {
	root := findRepoRoot(t)
	dir := filepath.Join(root, "pkg", "run")
	fs := token.NewFileSet()
	pkgs, err := parser.ParseDir(fs, dir, func(info os.FileInfo) bool {
		return strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg := pkgs["run"]
	if pkg == nil {
		t.Fatal("run package not found")
	}
	fields, methods := runnerShape(pkg)
	if fields > runnerFields {
		t.Fatalf("Runner has %d fields; baseline is %d", fields, runnerFields)
	}
	if methods > runnerMethods {
		t.Fatalf("Runner has %d exported methods; baseline is %d", methods, runnerMethods)
	}
}

func runnerShape(pkg *ast.Package) (int, int) {
	fields := 0
	methods := 0
	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if ok && gen.Tok == token.TYPE {
				for _, spec := range gen.Specs {
					typ, ok := spec.(*ast.TypeSpec)
					if !ok || typ.Name.Name != "Runner" {
						continue
					}
					st, ok := typ.Type.(*ast.StructType)
					if !ok {
						continue
					}
					for _, field := range st.Fields.List {
						if len(field.Names) == 0 {
							fields++
							continue
						}
						fields += len(field.Names)
					}
				}
			}
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() || !isRunner(fn.Recv) {
				continue
			}
			methods++
		}
	}
	return fields, methods
}

func isRunner(recv *ast.FieldList) bool {
	if recv == nil || len(recv.List) != 1 {
		return false
	}
	typ := recv.List[0].Type
	if ptr, ok := typ.(*ast.StarExpr); ok {
		typ = ptr.X
	}
	id, ok := typ.(*ast.Ident)
	return ok && id.Name == "Runner"
}

func TestCacheBaselineCoversSupportedProviders(t *testing.T) {
	root := findRepoRoot(t)
	file, err := os.Open(filepath.Join(root, "pkg", "architecture", "testdata", "cache_baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	report, err := architecture.LoadCacheReport(file)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"anthropic": "anthropic/claude-opus-4-6",
		"openai":    "openai/gpt-5",
		// Measured on glm-5.3-flash (the cache-optimization pass's probe
		// model); re-record all six cases together if this ever moves.
		"zhipuai":  "zhipuai/glm-5.3-flash",
		"deepseek": "deepseek/deepseek-v4-pro",
		"kimi":     "moonshotai/kimi-k3",
		"alibaba":  "alibaba/qwen3.8-max",
	}
	for provider, model := range want {
		run, ok := report.Providers[provider]
		if !ok || run.Model != model {
			t.Fatalf("baseline %s = %#v, want model %q", provider, run, model)
		}
		for _, name := range []string{"long_tool_turn", "approval_resume", "compaction", "agent_switch", "fast_mode", "subagent"} {
			if _, ok := run.Cases[name]; !ok {
				t.Fatalf("baseline %s misses case %q", provider, name)
			}
		}
	}
}

func TestCacheBaselineModelsExistInCatalog(t *testing.T) {
	cat, err := llm.Parse(llm.EmbeddedModelsJSON())
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{
		"anthropic/claude-opus-4-6",
		"openai/gpt-5",
		"zhipuai/glm-5.3-flash",
		"deepseek/deepseek-v4-pro",
		"moonshotai/kimi-k3",
		"alibaba/qwen3.8-max",
	} {
		if _, ok := cat.LookupModel(model); !ok {
			t.Fatalf("catalog misses baseline model %q", model)
		}
	}
}

func TestGraphFixture(t *testing.T) {
	root := findRepoRoot(t)
	file, err := os.Open(filepath.Join(root, "pkg", "architecture", "testdata", "graph.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	graph, err := architecture.LoadGraph(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Packages) == 0 {
		t.Fatal("graph fixture has no packages")
	}
	for _, pkg := range graph.Packages {
		if pkg.Path == "" || pkg.LOC < 0 || pkg.FanIn < 0 || pkg.FanOut < 0 {
			t.Fatalf("invalid package snapshot: %#v", pkg)
		}
	}
	seen := make(map[string]struct{}, len(graph.Edges))
	for _, edge := range graph.Edges {
		if edge.From == "" || edge.To == "" {
			t.Fatal("graph fixture has an incomplete edge")
		}
		key := edge.From + "\x00" + edge.To
		if _, ok := seen[key]; ok {
			t.Fatalf("graph fixture repeats edge %q", key)
		}
		seen[key] = struct{}{}
	}
}

func TestCheckCache(t *testing.T) {
	base := architecture.CacheReport{Providers: map[string]architecture.CacheRun{
		"openai": {Model: "gpt-test", Cases: map[string]architecture.CacheUse{"normal": {Read: 60, Creation: 10, Input: 30}}},
	}}
	if err := architecture.CheckCache(base, base); err != nil {
		t.Fatal(err)
	}
	regressed := architecture.CacheReport{Providers: map[string]architecture.CacheRun{
		"openai": {Model: "gpt-test", Cases: map[string]architecture.CacheUse{"normal": {Read: 50, Creation: 10, Input: 40}}},
	}}
	if err := architecture.CheckCache(base, regressed); err == nil {
		t.Fatal("regression was accepted")
	}
}

func TestCheckCacheRejectsMissingCase(t *testing.T) {
	base := architecture.CacheReport{Providers: map[string]architecture.CacheRun{
		"openai": {Model: "gpt-test", Cases: map[string]architecture.CacheUse{"normal": {Read: 1, Input: 1}}},
	}}
	current := architecture.CacheReport{Providers: map[string]architecture.CacheRun{
		"openai": {Model: "gpt-test", Cases: map[string]architecture.CacheUse{"other": {Read: 1, Input: 1}}},
	}}
	if err := architecture.CheckCache(base, current); err == nil {
		t.Fatal("missing case was accepted")
	}
}

// TestLLMRootDoesNotImportProviderSDKs enforces C10: pkg/llm is Layer 0, the
// package everything else builds on, and it must not drag vendor SDKs into its
// dependency closure. It used to type-assert against *openaiclient.APIError,
// *openaigo.Error and *anthropicapi.Error to classify provider failures, which
// put all three SDKs (42 packages) into `go list -deps ./pkg/llm`. Provider
// packages now normalize their own SDK's error into llm.APIError at the
// boundary, so that knowledge lives with the SDK that produced it.
//
// The check is deliberately non-recursive: pkg/llm/anthropic and
// pkg/llm/openai are exactly the places that may import these SDKs, and a
// recursive walk would forbid the thing the fix depends on. pkg/llm's own
// files import no forebrain package at all, so checking them directly is the
// whole closure -- an SDK could only get back in by being imported here.
//
// Test files are included: a provider error is now constructible only where
// its SDK is, so a test needing one belongs in the provider package too.
func TestLLMRootDoesNotImportProviderSDKs(t *testing.T) {
	root := findRepoRoot(t)
	dir := filepath.Join(root, "pkg", "llm")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"github.com/anthropics/anthropic-sdk-go",
		"github.com/openai/openai-go",
		"github.com/sashabaranov/go-openai",
	}
	var checked int
	var violations []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, imp := range file.Imports {
			got := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbidden {
				if got == bad || strings.HasPrefix(got, bad+"/") {
					violations = append(violations, path+" imports "+got)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatalf("no Go files found in %s; the guard would pass vacuously", dir)
	}
	if len(violations) > 0 {
		t.Fatalf("pkg/llm (Layer 0) must not import a provider SDK; normalize the error in pkg/llm/<provider> instead:\n%s",
			strings.Join(violations, "\n"))
	}
}

// TestNoDuplicateImportsWithinAFile forbids importing the same package twice in
// one file, once bare and once aliased.
//
// Go permits it, so nothing else catches it. It arrived here mechanically: the
// file-consolidation pass merged files that had each imported a package under a
// different name, and both spellings survived the merge. The result was 18
// duplicate imports across 16 files, including pkg/run/config.go referring to
// pkg/llm/openai as both "openai" and "openaillm" within the same function.
//
// It is worth a guard rather than a one-time cleanup because the cost is not
// cosmetic: two names for one package makes call sites look like they reach
// different dependencies, and it hides which local variables shadow a package
// name -- three of the sixteen files turned out to have locals named tool, run
// and state doing exactly that.
func TestNoDuplicateImportsWithinAFile(t *testing.T) {
	root := findRepoRoot(t)
	var checked int
	var violations []string
	for _, dir := range []string{"pkg", "cmd", "internal"} {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			checked++
			seen := map[string][]string{}
			for _, spec := range file.Imports {
				importPath := strings.Trim(spec.Path.Value, `"`)
				name := "<bare>"
				if spec.Name != nil {
					name = spec.Name.Name
				}
				seen[importPath] = append(seen[importPath], name)
			}
			for importPath, names := range seen {
				if len(names) > 1 {
					rel, relErr := filepath.Rel(root, path)
					if relErr != nil {
						rel = path
					}
					violations = append(violations,
						rel+" imports "+importPath+" as "+strings.Join(names, " and "))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if checked == 0 {
		t.Fatal("no Go files walked; the guard would pass vacuously")
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("a package must be imported at most once per file:\n%s", strings.Join(violations, "\n"))
	}
}

// TestImportsAreGroupedStdlibFirst enforces the grouping gofmt does not: a file
// may have at most one standard-library group followed by at most one group of
// everything else, which is what goimports produces by default.
//
// gofmt sorts within a group but never moves an import between groups, so a
// stdlib import stranded among third-party ones stays there forever and no
// existing check notices. Merging files during the consolidation pass
// concatenated their import blocks, which left 169 of 530 files with mixed
// groups -- pkg/tui/render.go had encoding/json, log/slog, path/filepath and
// unicode/utf8 sitting among the third-party imports.
//
// The convention is the repo's own: 360 files already had exactly this shape.
func TestImportsAreGroupedStdlibFirst(t *testing.T) {
	root := findRepoRoot(t)
	var checked int
	var violations []string
	for _, dir := range []string{"pkg", "cmd", "internal"} {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			block := importBlock(string(src))
			if block == "" {
				return nil
			}
			checked++
			var shape []string
			for _, group := range strings.Split(block, "\n\n") {
				kind := importGroupKind(group)
				if kind != "" {
					shape = append(shape, kind)
				}
			}
			ok := len(shape) == 1 && (shape[0] == "std" || shape[0] == "ext")
			ok = ok || (len(shape) == 2 && shape[0] == "std" && shape[1] == "ext")
			if !ok {
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					rel = path
				}
				violations = append(violations, rel+" has groups "+strings.Join(shape, " | "))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if checked == 0 {
		t.Fatal("no import blocks walked; the guard would pass vacuously")
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("imports must be one stdlib group then one group for everything else (run goimports):\n%s",
			strings.Join(violations, "\n"))
	}
}

// importBlock returns the body of the file's parenthesized import block, or ""
// when it has none. Single-line imports carry no grouping to check.
func importBlock(src string) string {
	start := strings.Index(src, "\nimport (\n")
	if start < 0 {
		return ""
	}
	start += len("\nimport (\n")
	end := strings.Index(src[start:], "\n)")
	if end < 0 {
		return ""
	}
	return src[start : start+end]
}

// importGroupKind reports whether a group holds only standard-library imports
// ("std"), only others ("ext"), both ("ext+std"), or no imports at all ("").
// A path whose first segment carries a dot is a domain, hence not stdlib.
func importGroupKind(group string) string {
	var std, ext bool
	for _, line := range strings.Split(group, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		open := strings.Index(line, `"`)
		closeIdx := strings.LastIndex(line, `"`)
		if open < 0 || closeIdx <= open {
			continue
		}
		path := line[open+1 : closeIdx]
		if strings.Contains(strings.Split(path, "/")[0], ".") {
			ext = true
		} else {
			std = true
		}
	}
	switch {
	case std && ext:
		return "ext+std"
	case std:
		return "std"
	case ext:
		return "ext"
	}
	return ""
}

// goFilesUnder returns every .go file under the repo's source directories,
// as paths relative to root.
func goFilesUnder(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	for _, dir := range []string{"pkg", "cmd", "internal"} {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			out = append(out, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(out) == 0 {
		t.Fatal("no Go files found; the guards over them would pass vacuously")
	}
	return out
}

// TestFileNamesHaveAtMostTwoUnderscores enforces the owner's naming rule: a
// file name may contain at most two underscores, counted on the whole name
// including a test file's _test suffix, so chat_session_slash_handlers.go and
// session_store_paged_test.go are out while chat_session.go and
// session_store_test.go are in.
//
// The cap is what forces a name to describe one thing. A file that needs a
// fourth word is a file holding a fourth concern, and renaming it is not the
// fix -- splitting it, or moving the concern to the file that owns it, is.
// Counting the suffix matters because a test file is named after its
// production file: letting _test eat one of the two underscores would allow a
// three-word production name through the test file's name instead.
func TestFileNamesHaveAtMostTwoUnderscores(t *testing.T) {
	root := findRepoRoot(t)
	var violations []string
	for _, rel := range goFilesUnder(t, root) {
		name := strings.TrimSuffix(filepath.Base(rel), ".go")
		if n := strings.Count(name, "_"); n > 2 {
			violations = append(violations, fmt.Sprintf("%s (%d underscores)", rel, n))
		}
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("file names may contain at most two underscores:\n%s", strings.Join(violations, "\n"))
	}
}

// TestPackagesStayUnderTwentyProductionFiles enforces the owner's cap of 20
// production files per package. A package that outgrows it has stopped being
// one domain, and the fix is to merge files that belong together or to split
// the package -- not to raise the number.
func TestPackagesStayUnderTwentyProductionFiles(t *testing.T) {
	root := findRepoRoot(t)
	counts := map[string]int{}
	for _, rel := range goFilesUnder(t, root) {
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		counts[filepath.Dir(rel)]++
	}
	var violations []string
	for dir, n := range counts {
		if n > maxProductionFilesPerPackage {
			violations = append(violations, fmt.Sprintf("%s has %d production files", dir, n))
		}
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("a package may hold at most %d production files:\n%s",
			maxProductionFilesPerPackage, strings.Join(violations, "\n"))
	}
}

// TestTestFilesCorrespondToProductionFiles enforces the owner's rule that a
// unit-test file is named after the production file it covers: X_test.go must
// sit beside X.go.
//
// One production file therefore has at most one test file, and a package can
// never hold more test files than production files -- both follow from the
// naming rule rather than needing separate counting. It also rules out the
// shape this repo had in pkg/tool, where file_unix_test.go and
// file_windows_test.go existed with no production file of either name, purely
// to vary one boolean by build tag.
func TestTestFilesCorrespondToProductionFiles(t *testing.T) {
	root := findRepoRoot(t)
	production := map[string]bool{}
	var tests []string
	for _, rel := range goFilesUnder(t, root) {
		if strings.HasSuffix(rel, "_test.go") {
			tests = append(tests, rel)
			continue
		}
		production[rel] = true
	}
	var violations []string
	for _, rel := range tests {
		want := strings.TrimSuffix(rel, "_test.go") + ".go"
		if !production[want] {
			violations = append(violations, rel+" has no "+filepath.Base(want)+" beside it")
		}
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("every test file must be named after the production file it covers:\n%s",
			strings.Join(violations, "\n"))
	}
}

const maxProductionFilesPerPackage = 20

// TestSurfacesGetToolStateFromTheEnvironment enforces the call-site half of R4:
// a surface asks the composition root which tool state this process uses, and
// never reaches through the Runner to find out.
//
// "Which tool state does this process use" is a composition question. The
// Runner still builds the state during Load, because registering tools, MCP
// servers and skills is execution setup, but routing every caller through
// Environment.Tools() is what lets the Runner eventually stop being the answer
// -- which is R4's actual goal. Twenty-nine production call sites already go
// through the environment; this stops the thirtieth from going back.
//
// pkg/run owns the field and pkg/process implements Tools() on top of it, so
// both are exempt. Test files are exempt too: a test that builds a bare Runner
// with no Environment around it has no composition root to ask.
func TestSurfacesGetToolStateFromTheEnvironment(t *testing.T) {
	root := findRepoRoot(t)
	var checked int
	var violations []string
	for _, rel := range goFilesUnder(t, root) {
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		if strings.HasPrefix(rel, "pkg/run/") || strings.HasPrefix(rel, "pkg/process/") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for i, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if strings.Contains(line, "Runner.Tools()") {
				violations = append(violations, fmt.Sprintf("%s:%d reaches through the Runner for tool state", rel, i+1))
			}
		}
	}
	if checked == 0 {
		t.Fatal("no production files walked outside run/process; the guard would pass vacuously")
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("surfaces must call Environment.Tools() instead:\n%s", strings.Join(violations, "\n"))
	}
}

// noImporterExempt lists the packages allowed to have no importer anywhere in
// the module. Only this one qualifies: it is the guard package itself, so
// anything importing it would be a package asserting on its own architecture.
var noImporterExempt = map[string]struct{}{
	"github.com/forebrain-harness/forebrain-harness/pkg/architecture": {},
}

// TestEveryPackageHasAnImporter is P10-7's "无 importer 包" threshold, enforced
// rather than only reported: a package nothing imports is dead weight that the
// package-count and LOC budgets are still paying for, and the refactor has
// deleted several of exactly that shape (jobs, reviewrt, shellargv, contextmap).
//
// Importers are counted across the whole module including _test.go files, so a
// package that exists only to serve tests (testutil) counts as imported, while
// the surfaces count through cmd/forebrain.
func TestEveryPackageHasAnImporter(t *testing.T) {
	root := findRepoRoot(t)
	const modulePrefix = "github.com/forebrain-harness/forebrain-harness/"

	pkgs := map[string]struct{}{}
	imported := map[string]struct{}{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name != "." && (name == "node_modules" || name == "frontend" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, filepath.Dir(path))
		if rerr != nil {
			return rerr
		}
		if strings.HasPrefix(rel, "pkg/") {
			pkgs[modulePrefix+filepath.ToSlash(rel)] = struct{}{}
		}
		file, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		self := modulePrefix + filepath.ToSlash(rel)
		for _, imp := range file.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			// A package importing itself is impossible in Go, but a _test.go
			// file in the same directory names its own package when it is an
			// external test (foo_test), which would otherwise count as an
			// importer of itself.
			if p != self {
				imported[p] = struct{}{}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var orphans []string
	for p := range pkgs {
		if _, ok := noImporterExempt[p]; ok {
			continue
		}
		if _, ok := imported[p]; !ok {
			orphans = append(orphans, p)
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Fatalf("packages with no importer anywhere in the module (delete them or use them):\n%s",
			strings.Join(orphans, "\n"))
	}
}

// Every way a shipped forebrain is built has to compile SQLite's FTS5 module in.
//
// Memory search declares an FTS5 virtual table as part of the state schema, so
// a binary built without the tag does not merely lose ranking — it cannot open
// the state database at all, and the failure appears only at runtime, on the
// user's machine, long after the build that caused it. The build tag is the
// kind of thing three separate release paths forget one at a time, so the rule
// is checked here instead of remembered.
func TestEveryReleaseBuildCompilesFTS5(t *testing.T) {
	root := findRepoRoot(t)
	for _, file := range []string{
		"Makefile",
		"Dockerfile",
		filepath.Join("npm", "scripts", "build-platform-packages.sh"),
	} {
		path := filepath.Join(root, file)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		text := string(data)
		if !strings.Contains(text, "go build") {
			continue
		}
		if !strings.Contains(text, "-tags fts5") {
			t.Fatalf("%s builds forebrain without -tags fts5; the shipped binary would have no FTS5 and could not open the state database", file)
		}
	}
}

// Every way a shipped forebrain is built has to install the word-segmentation
// dictionaries beside the binary.
//
// They are not compiled in: memory search reads them from dict/ next to the
// executable, so a release path that builds the binary and forgets them ships
// a forebrain whose memory search fails the first time it meets Chinese or
// Japanese — on the user's machine, long after the build. Like the FTS5 tag,
// the step is checked here instead of remembered.
func TestEveryReleaseBuildInstallsTheDictionaries(t *testing.T) {
	root := findRepoRoot(t)
	for _, file := range []string{
		"Makefile",
		"Dockerfile",
		filepath.Join("npm", "scripts", "build-platform-packages.sh"),
	} {
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		text := string(data)
		if strings.Contains(text, "go build") && !strings.Contains(text, "install-dictionary.sh") {
			t.Fatalf("%s builds forebrain without installing its dictionaries (scripts/install-dictionary.sh); the shipped memory search could not segment text", file)
		}
	}
}
