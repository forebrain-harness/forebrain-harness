package process

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/joho/godotenv"
)

// compositionConfig is the smallest config Open accepts: one agent with one
// provider. The contract under test is about composition, not about any
// particular model.
const compositionConfig = "agents:\n" +
	"  definitions:\n" +
	"    main:\n" +
	"      llm_providers:\n" +
	"        - provider: openai\n" +
	"          model: gpt-test\n" +
	"          api_key: ${FOREBRAIN_COMPOSITION_TEST_KEY}\n" +
	"          base_url: http://127.0.0.1:0/v1\n"

func openForContract(t *testing.T, home, launchDir string, opts OpenOptions) *Environment {
	t.Helper()
	opts.Home = home
	opts.ConfigPath = filepath.Join(home, "forebrain.yaml")
	opts.LaunchDir = launchDir
	env, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatalf("Open(%+v): %v", opts, err)
	}
	t.Cleanup(env.Close)
	return env
}

// TestCompositionIsIdenticalAcrossSurfaces is P2-12. Both surfaces now start
// through process.Open (pkg/tui/process_session.go and
// pkg/gateway/serve_run.go), and the whole point of P2 was that they stop
// composing their own runtimes. This asserts they cannot drift apart again:
// given the same options, the runner configuration, the hook pipeline order,
// the set of stores and the sandbox's effective configuration must agree.
//
// The surfaces legitimately differ in exactly one dimension — the P2-7
// optional modules (uploads, channels, telemetry, approval TTL) and the
// session source. Those are passed as options, so a difference there is a
// caller's choice; a difference in anything else is process.Open having grown
// a surface-specific branch, which is what this test exists to catch.
func TestCompositionIsIdenticalAcrossSurfaces(t *testing.T) {
	t.Setenv("FOREBRAIN_COMPOSITION_TEST_KEY", "test-key")
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "forebrain.yaml"), []byte(compositionConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	launchDir := t.TempDir()

	// The TUI's option shape: no optional modules.
	tui := openForContract(t, home, launchDir, OpenOptions{SessionSource: "tui"})
	// The gateway's option shape, minus telemetry and the TTL sweeper, which
	// start background goroutines that have nothing to do with composition.
	gateway := openForContract(t, home, launchDir, OpenOptions{
		EnableUploads:  true,
		EnableChannels: true,
		SessionSource:  "webchat",
	})

	if tui.Runner == nil || gateway.Runner == nil {
		t.Fatal("Open returned an environment with no runner")
	}
	for _, probe := range []struct {
		name string
		a, b string
	}{
		{"AgentName", tui.Runner.AgentName, gateway.Runner.AgentName},
		{"WorkspaceRoot", tui.Runner.WorkspaceRoot, gateway.Runner.WorkspaceRoot},
		{"ProjectRoot", tui.Runner.ProjectRoot, gateway.Runner.ProjectRoot},
		{"ProjectKey", tui.Runner.ProjectKey, gateway.Runner.ProjectKey},
		{"Root", tui.Root, gateway.Root},
		{"LaunchDir", tui.LaunchDir, gateway.LaunchDir},
	} {
		if probe.a != probe.b {
			t.Errorf("runner/environment %s differs between surfaces: tui=%q gateway=%q", probe.name, probe.a, probe.b)
		}
	}

	// Hook order decides the order context sources are assembled into the
	// prompt, so a divergence here is an I0 problem as well as a correctness
	// one: the two surfaces would describe the same turn differently.
	tuiHooks, gatewayHooks := tui.Hooks.Names(), gateway.Hooks.Names()
	if !reflect.DeepEqual(tuiHooks, gatewayHooks) {
		t.Errorf("hook pipeline order differs between surfaces:\n  tui     = %v\n  gateway = %v", tuiHooks, gatewayHooks)
	}
	if len(tuiHooks) == 0 {
		t.Error("hook pipeline is empty; this test would pass vacuously")
	}

	// The stores every surface gets. Files is deliberately absent from this
	// list: it is the EnableUploads module, and the two option sets differ
	// there on purpose.
	for _, probe := range []struct {
		name string
		a, b bool
	}{
		{"SQL", tui.SQL != nil, gateway.SQL != nil},
		{"Actions", tui.Deps.Actions != nil, gateway.Deps.Actions != nil},
		{"RunRT", tui.Deps.RunRT != nil, gateway.Deps.RunRT != nil},
		{"MemoryStore", tui.Deps.MemoryStore != nil, gateway.Deps.MemoryStore != nil},
		{"Sess", tui.Deps.SessionStore != nil, gateway.Deps.SessionStore != nil},
		{"Sandbox", tui.Sandbox != nil, gateway.Sandbox != nil},
		{"Foreground", tui.Foreground != nil, gateway.Foreground != nil},
		{"Control", tui.Control != nil, gateway.Control != nil},
	} {
		if probe.a != probe.b {
			t.Errorf("store %s present on one surface only: tui=%t gateway=%t", probe.name, probe.a, probe.b)
		}
		if !probe.a {
			t.Errorf("store %s is nil on both surfaces; the assertion above would pass vacuously", probe.name)
		}
	}

	// Sandbox effective configuration. This is the one that decides what the
	// model is allowed to touch, so the two surfaces agreeing is a safety
	// property, not just tidiness.
	tuiSandbox, gatewaySandbox := tui.Sandbox.RuntimeConfig(), gateway.Sandbox.RuntimeConfig()
	if !reflect.DeepEqual(tuiSandbox, gatewaySandbox) {
		t.Errorf("sandbox effective config differs between surfaces:\n  tui     = %+v\n  gateway = %+v",
			tuiSandbox, gatewaySandbox)
	}

	// The optional modules must still differ, otherwise the two option sets
	// above are not actually exercising the surfaces' real difference and the
	// equality assertions prove less than they appear to.
	if (tui.Channels != nil) == (gateway.Channels != nil) {
		t.Errorf("EnableChannels made no difference (tui=%t gateway=%t); this test is no longer comparing two distinct option sets",
			tui.Channels != nil, gateway.Channels != nil)
	}
	if (tui.Files != nil) == (gateway.Files != nil) {
		t.Errorf("EnableUploads made no difference (tui=%t gateway=%t); this test is no longer comparing two distinct option sets",
			tui.Files != nil, gateway.Files != nil)
	}
}

const resolveCacheConfig = "agents:\n  definitions:\n    main:\n      primary: true\n      llm_providers:\n" +
	"        - provider: openai\n          model: %MODEL%\n" +
	"          api_key: ${OPENAI_API_KEY}\n          base_url: http://localhost:0/v1\n"

func TestResolveCacheServesTheSameContextUntilReset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")
	cfgPath := filepath.Join(home, "forebrain.yaml")
	writeResolveCacheConfig(t, cfgPath, "gpt-first")

	ResetResolve()
	t.Cleanup(ResetResolve)

	first, err := Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := resolveCacheModel(first); got != "gpt-first" {
		t.Fatalf("model = %q, want gpt-first", got)
	}

	writeResolveCacheConfig(t, cfgPath, "gpt-second")
	cached, err := Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := resolveCacheModel(cached); got != "gpt-first" {
		t.Fatalf("model = %q, want the cached gpt-first", got)
	}

	ResetResolve()
	reread, err := Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := resolveCacheModel(reread); got != "gpt-second" {
		t.Fatalf("model = %q, want gpt-second after reset", got)
	}
}

// The TUI resets this cache from the command goroutine (/connect) while other
// goroutines resolve, so invalidation has to be safe to interleave. Run with
// -race: replacing a sync.Once in place, as this used to, is a race on the Once
// itself and can drop or duplicate the load.
func TestResolveCacheIsSafeUnderConcurrentReset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")
	writeResolveCacheConfig(t, filepath.Join(home, "forebrain.yaml"), "gpt-concurrent")

	ResetResolve()
	t.Cleanup(ResetResolve)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if _, err := Resolve(); err != nil {
					t.Errorf("resolve: %v", err)
					return
				}
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				ResetResolve()
			}
		}()
	}
	wg.Wait()

	final, err := Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := resolveCacheModel(final); got != "gpt-concurrent" {
		t.Fatalf("model = %q, want gpt-concurrent", got)
	}
}

func writeResolveCacheConfig(t *testing.T, path, model string) {
	t.Helper()
	body := strings.ReplaceAll(resolveCacheConfig, "%MODEL%", model)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func resolveCacheModel(ctx Context) string {
	def, ok := ctx.Config.Agents.Definitions["main"]
	if !ok || len(def.LLMProviders) == 0 {
		return ""
	}
	return def.LLMProviders[0].Model
}

// Two primary agents can run the same kind of channel with different
// credentials, so the variable each secret is stored under carries the agent's
// id. A shared name would let one agent's token silently become the other's.
func TestChannelSecretEnvNameIsScopedToTheAgent(t *testing.T) {
	cases := []struct {
		agent  string
		suffix string
		want   string
	}{
		{"main", "TELEGRAM_BOT_TOKEN", "FOREBRAIN_MAIN_TELEGRAM_BOT_TOKEN"},
		{"acme", "feishu_app_secret", "FOREBRAIN_ACME_FEISHU_APP_SECRET"},
		{"code-review", "SLACK_SECRET", "FOREBRAIN_CODE_REVIEW_SLACK_SECRET"},
		{" ", "SLACK_SECRET", "FOREBRAIN_SLACK_SECRET"},
	}
	for _, tc := range cases {
		if got := ChannelSecretEnvName(tc.agent, tc.suffix); got != tc.want {
			t.Errorf("ChannelSecretEnvName(%q, %q) = %q, want %q", tc.agent, tc.suffix, got, tc.want)
		}
	}
	if ChannelSecretEnvName("main", "SLACK_SECRET") == ChannelSecretEnvName("acme", "SLACK_SECRET") {
		t.Fatal("two agents share one variable for the same channel secret")
	}
}

func TestAPIKeyConfigReferenceStoresPlaintextInDotEnv(t *testing.T) {
	home := t.TempDir()

	got, err := apiKeyConfigReference(home, "openai", "sk-live-secret")
	if err != nil {
		t.Fatalf("apiKeyConfigReference error: %v", err)
	}
	if got != "${OPENAI_API_KEY}" {
		t.Fatalf("reference = %q, want ${OPENAI_API_KEY}", got)
	}

	envPath := filepath.Join(home, ".env")
	envBytes, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}
	if got := string(envBytes); !strings.Contains(got, "OPENAI_API_KEY=") || !strings.Contains(got, "sk-live-secret") {
		t.Fatalf(".env missing API key, got %q", got)
	}
	info, err := os.Stat(envPath)
	if err != nil {
		t.Fatalf("stat .env: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf(".env mode = %v, want 0600", got)
	}
}

func TestAPIKeyConfigReferencePreservesEnvReference(t *testing.T) {
	home := t.TempDir()

	got, err := apiKeyConfigReference(home, "openai", "${CUSTOM_OPENAI_KEY}")
	if err != nil {
		t.Fatalf("apiKeyConfigReference error: %v", err)
	}
	if got != "${CUSTOM_OPENAI_KEY}" {
		t.Fatalf("reference = %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".env")); !os.IsNotExist(err) {
		t.Fatalf(".env stat error = %v, want not exists", err)
	}
}

func TestAPIKeyConfigReferenceStoresInvalidEnvReferenceAsValue(t *testing.T) {
	home := t.TempDir()

	got, err := apiKeyConfigReference(home, "openai", "${BAD-NAME}")
	if err != nil {
		t.Fatalf("apiKeyConfigReference error: %v", err)
	}
	if got != "${OPENAI_API_KEY}" {
		t.Fatalf("reference = %q, want ${OPENAI_API_KEY}", got)
	}
	envMap, err := godotenv.Read(filepath.Join(home, ".env"))
	if err != nil {
		t.Fatalf("parse .env: %v", err)
	}
	if got := envMap["OPENAI_API_KEY"]; got != "${BAD-NAME}" {
		t.Fatalf("OPENAI_API_KEY = %q", got)
	}
}

func TestExecuteStoresAPIKeyReferenceInConfig(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "forebrain.yaml")
	rt := Context{
		Home:       home,
		ConfigPath: cfgPath,
		Config:     appcfg.Root{},
	}

	_, err := Execute(rt, Options{

		Provider:     "openai",
		Model:        "gpt-5",
		APIKey:       "sk-live-secret",
		BaseURL:      "https://api.openai.com/v1",
		UpdateParams: true,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if got := string(raw); strings.Contains(got, "sk-live-secret") {
		t.Fatalf("config contains plaintext API key: %q", got)
	}
	if got := string(raw); !strings.Contains(got, "${OPENAI_API_KEY}") {
		t.Fatalf("config missing env reference: %q", got)
	}
	envMap, err := godotenv.Read(filepath.Join(home, ".env"))
	if err != nil {
		t.Fatalf("parse .env: %v", err)
	}
	if got := envMap["OPENAI_API_KEY"]; got != "sk-live-secret" {
		t.Fatalf("OPENAI_API_KEY = %q", got)
	}
}

func TestExecuteWritesOnboardLogWithoutSecrets(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "forebrain.yaml")
	rt := Context{
		Home:       home,
		ConfigPath: cfgPath,
		Config:     appcfg.Root{},
	}

	_, err := Execute(rt, Options{

		Provider:     "openai",
		Model:        "qwen3.5-122b",
		APIKey:       "sk-live-secret",
		BaseURL:      "http://10.20.200.100:30122/v1",
		UpdateParams: true,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(home, "logs", "info.log"))
	if err != nil {
		t.Fatalf("read onboard log: %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		"event=setup.execute.start",
		"event=setup.execute.success",
		`provider="openai"`,
		`model="qwen3.5-122b"`,
		`base_url="http://10.20.200.100:30122/v1"`,
		`api_key_set="true"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("log missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "sk-live-secret") {
		t.Fatalf("log leaked api key: %s", text)
	}
}

func TestExecuteRequiresModelAPIKeyAndBaseURL(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "forebrain.yaml")
	rt := Context{
		Home:       home,
		ConfigPath: cfgPath,
		Config:     appcfg.Root{},
	}

	for _, tc := range []struct {
		name string
		opts Options
		want string
	}{
		{
			name: "missing model",
			opts: Options{Provider: "openai", APIKey: "sk-live-secret", BaseURL: "https://api.openai.com/v1", UpdateParams: true},
			want: "model required in non-interactive mode",
		},
		{
			name: "missing api key",
			opts: Options{Provider: "openai", Model: "gpt-5", BaseURL: "https://api.openai.com/v1", UpdateParams: true},
			want: "api_key required in non-interactive mode",
		},
		{
			name: "missing base url",
			opts: Options{Provider: "openai", Model: "gpt-5", APIKey: "sk-live-secret", UpdateParams: true},
			want: "base_url required in non-interactive mode",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Execute(rt, tc.opts)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Execute error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Execute error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestAgentContextDefaults(t *testing.T) {
	root := t.TempDir()
	ctx := AgentContext(nil, root, " ")
	if got := llm.AgentSessionIDFromContext(ctx); got != "default" {
		t.Fatalf("session id=%q", got)
	}
	if got := tool.ModeFromContext(ctx); got != string(state.ModeAgent) {
		t.Fatalf("mode=%q", got)
	}
	// The plan directory is writable in every mode: after leaving plan mode the
	// agent still has to record the plan's implementation status in it.
	if want := state.PlanDirForSession(root, "", "default"); tool.AllowedPlanPathFromContext(ctx) != want {
		t.Fatalf("plan path=%q want=%q", tool.AllowedPlanPathFromContext(ctx), want)
	}
	if got := llm.PromptCacheKeyFromContext(ctx); got != "default" {
		t.Fatalf("prompt cache key=%q", got)
	}
}

func TestAgentContextPlanMode(t *testing.T) {
	home := t.TempDir()
	if err := state.Set(home, "sid", state.State{Mode: state.ModePlan}); err != nil {
		t.Fatalf("set mode: %v", err)
	}
	base := context.WithValue(context.Background(), testKey{}, "kept")
	ctx := AgentContext(base, home, " sid ")

	if got := ctx.Value(testKey{}); got != "kept" {
		t.Fatalf("base context value=%v", got)
	}
	if got := llm.AgentSessionIDFromContext(ctx); got != "sid" {
		t.Fatalf("session id=%q", got)
	}
	if got := tool.ModeFromContext(ctx); got != string(state.ModePlan) {
		t.Fatalf("mode=%q", got)
	}
	if got, want := tool.AllowedPlanPathFromContext(ctx), state.PlanDirForSession(home, "", "sid"); got != want {
		t.Fatalf("plan path=%q want=%q", got, want)
	}
}

// TestAgentContextPlanDirFollowsTheConversation pins the subagent rule: the
// allowed plan directory follows the user-visible conversation, not the
// subagent's worker session id — a worker inheriting plan mode writes into its
// parent conversation's plan directory, where the parent's exit_plan_mode (and
// only the parent's) resolves it. The segment is asserted literally so a
// regression to project-scoped resolution (no session segment at all) fails
// this test rather than collapsing both sides of an equality.
func TestAgentContextPlanDirFollowsTheConversation(t *testing.T) {
	root := t.TempDir()
	base := tool.WithConversationSessionID(context.Background(), "conv-1")
	ctx := AgentContext(base, root, "worker-1")
	got := tool.AllowedPlanPathFromContext(ctx)
	if filepath.Base(got) != "conv-1" || filepath.Dir(got) != state.PlanDirForProject(root, "") {
		t.Fatalf("plan path=%q, want the conversation's own directory %q (base conv-1)", got, state.PlanDirForSession(root, "", "conv-1"))
	}
	if want := state.PlanDirForSession(root, "", "conv-1"); got != want {
		t.Fatalf("plan path=%q want %q", got, want)
	}
	// Without a conversation id the session itself names the directory.
	plain := AgentContext(context.Background(), root, "s2")
	got = tool.AllowedPlanPathFromContext(plain)
	if filepath.Base(got) != "s2" || filepath.Dir(got) != state.PlanDirForProject(root, "") {
		t.Fatalf("plan path=%q, want the session's own directory (base s2)", got)
	}
	if want := state.PlanDirForSession(root, "", "s2"); got != want {
		t.Fatalf("plan path=%q want %q", got, want)
	}
}

func TestAgentContextPreservesParentPromptCacheKey(t *testing.T) {
	base := llm.WithPromptCacheKey(context.Background(), "root-session")
	ctx := AgentContext(base, t.TempDir(), "worker-session")
	if got := llm.AgentSessionIDFromContext(ctx); got != "worker-session" {
		t.Fatalf("agent session id=%q", got)
	}
	if got := llm.PromptCacheKeyFromContext(ctx); got != "root-session" {
		t.Fatalf("prompt cache key=%q", got)
	}
}

func TestAgentContextPinsParentSessionFallbackBeforeChangingWorkerID(t *testing.T) {
	base := llm.WithAgentSessionID(context.Background(), "root-session")
	ctx := AgentContext(base, t.TempDir(), "worker-session")
	if got := llm.PromptCacheKeyFromContext(ctx); got != "root-session" {
		t.Fatalf("prompt cache key=%q", got)
	}
}

func TestAgentContextModeReadErrorFallsBackToAgent(t *testing.T) {
	home := t.TempDir()
	modeDir := filepath.Join(home, "state", "modes")
	if err := os.MkdirAll(modeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Mkdir(filepath.Join(modeDir, "sid.json"), 0o755); err != nil {
		t.Fatalf("mkdir mode path: %v", err)
	}

	ctx := AgentContext(context.Background(), home, "sid")
	if got := tool.ModeFromContext(ctx); got != string(state.ModeAgent) {
		t.Fatalf("mode=%q", got)
	}
}

type testKey struct{}

// TestAgentContextIsolatesTwoAgents proves the core per-agent guarantee: two
// distinct state roots (a main agent at <home>/workspace and a non-main agent
// at <home>/workspaces/review) keep independent mode state, and the allowed
// plan path AgentContext seeds is rooted at each agent's own state root — the
// Fix-2 coupling (GuardWrite trusts this path) only holds if it stays per-agent.
func TestAgentContextIsolatesTwoAgents(t *testing.T) {
	home := t.TempDir()
	mainRoot := filepath.Join(home, "workspace")
	reviewRoot := filepath.Join(home, "workspaces", "review")

	// Main agent enters plan mode; review agent stays in agent mode.
	if err := state.Set(mainRoot, "sid", state.State{Mode: state.ModePlan}); err != nil {
		t.Fatalf("set main mode: %v", err)
	}
	if err := state.Set(reviewRoot, "sid", state.State{Mode: state.ModeAgent}); err != nil {
		t.Fatalf("set review mode: %v", err)
	}

	mainCtx := AgentContext(context.Background(), mainRoot, "sid")
	reviewCtx := AgentContext(context.Background(), reviewRoot, "sid")

	// Modes are independent despite sharing session id "sid".
	if got := tool.ModeFromContext(mainCtx); got != string(state.ModePlan) {
		t.Fatalf("main mode=%q want plan", got)
	}
	if got := tool.ModeFromContext(reviewCtx); got != string(state.ModeAgent) {
		t.Fatalf("review mode=%q want agent", got)
	}

	// Plan paths are rooted in each agent's own state root.
	wantMainPlan := state.PlanDirForSession(mainRoot, "", "sid")
	if got := tool.AllowedPlanPathFromContext(mainCtx); got != wantMainPlan {
		t.Fatalf("main plan path=%q want=%q", got, wantMainPlan)
	}
	wantReviewPlan := state.PlanDirForSession(reviewRoot, "", "sid")
	if got := tool.AllowedPlanPathFromContext(reviewCtx); got != wantReviewPlan {
		t.Fatalf("review plan path=%q want=%q", got, wantReviewPlan)
	}
}

func TestNilAndLightweightWorkerhostPaths(t *testing.T) {
	if _, _, err := RunSubagentSupervised(context.Background(), nil, run.SubagentExecRequest{Task: "task"}); err == nil {
		t.Fatal("expected nil environment subagent error")
	}
	if _, err := (*Environment)(nil).RunSubagentExec(context.Background(), run.SubagentExecRequest{Task: "task"}); err == nil {
		t.Fatal("expected nil environment exec error")
	}
}

func TestEnvironmentConfigHelpers(t *testing.T) {
	StartConfigHotReload(context.Background(), nil)
	StartConfigHotReload(context.Background(), &Environment{})
	if err := (*Environment)(nil).ReloadConfig(); err == nil {
		t.Fatal("expected nil reload error")
	}
	if err := (&Environment{}).ReloadConfig(); err == nil || !strings.Contains(err.Error(), "empty config path") {
		t.Fatalf("reload err=%v", err)
	}
	(&Environment{}).RefreshSandboxRuntime()
	(*Environment)(nil).RefreshSandboxRuntime()
	(&Environment{}).refreshSandboxRuntimeLocked()
}

func TestAssembleContextSnapshot(t *testing.T) {
	if res, ok, err := (*Environment)(nil).AssembleContextSnapshot(hook.HookContext{Trigger: "user"}, "hello"); err != nil || ok || res.SessionID != "" {
		t.Fatalf("nil environment result=%+v ok=%v err=%v", res, ok, err)
	}
	if res, ok, err := (&Environment{}).AssembleContextSnapshot(hook.HookContext{Trigger: "user"}, "hello"); err != nil || ok || res.SessionID != "" {
		t.Fatalf("nil hook result=%+v ok=%v err=%v", res, ok, err)
	}

	cfg := &appcfg.Root{}
	cfg.Agents.Defaults.ContextInject.ModelContextTokens = 100
	cfg.Agents.Defaults.ContextInject.WarnRemainingTokens = 10
	// The conversation's size comes from the session's own messages: the
	// last response's whole prompt — cache reads included — plus its output.
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := state.NewSessionStore(db, "main")
	if _, err := store.Append(context.Background(), "s", "user", "hi"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendStructuredMessage(context.Background(), "s", "assistant", "hello", "m1", "", "m",
		`{"input_tokens":3,"output_tokens":4,"cache_read_input_tokens":40}`, "", "", state.MessageExecTiming{}); err != nil {
		t.Fatal(err)
	}
	ctxHook := assembly.NewHook(cfg, store)
	ctxHook.Home = t.TempDir()
	ctxHook.ModeProvider = func(sessionID string) (string, string) {
		return "agent", "plan"
	}
	ctxHook.PinsProvider = func(sessionID string) []string {
		return []string{"README.md"}
	}
	env := &Environment{ctxHook: ctxHook}

	if _, ok, err := env.AssembleContextSnapshot(hook.HookContext{Trigger: "background", SessionID: "s"}, "hello"); err != nil || ok {
		t.Fatalf("background ok=%v err=%v", ok, err)
	}
	if _, ok, err := env.AssembleContextSnapshot(hook.HookContext{Trigger: "user", SessionID: "s"}, "/status"); err != nil || ok {
		t.Fatalf("slash ok=%v err=%v", ok, err)
	}
	res, ok, err := env.AssembleContextSnapshot(hook.HookContext{Trigger: "user", SessionID: "s", Channel: "webchat", RunKind: "agent", AgentID: "main"}, "please inspect README")
	if err != nil || !ok {
		t.Fatalf("assemble ok=%v err=%v", ok, err)
	}
	if res.SessionID != "s" || res.Mode != "agent" || res.ModelContextTokens != 100 || res.ConversationTokens != 47 {
		t.Fatalf("snapshot=%+v", res)
	}
	if len(res.WorkingSet) == 0 || res.WorkingSet[0] != "README.md" {
		t.Fatalf("working set=%v", res.WorkingSet)
	}
}

func TestEnvironmentClose(t *testing.T) {
	env := &Environment{
		telShutdown: func(context.Context) error { return nil },
	}
	env.Close()
}
