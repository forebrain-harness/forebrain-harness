package process

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	homepkg "github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// resolveCache caches the first Resolve() result so callers that check
// LLM configuration before opening a session (e.g. the TUI startup flow)
// do not load and parse forebrain.yaml three times in a row.
//
// The cache is guarded by a plain mutex rather than a sync.Once because it is
// invalidated: ResetResolve replacing a Once that another goroutine may be
// inside of is a data race on the Once itself, and in a long-lived process
// (the TUI, where /connect resets the cache while other goroutines resolve)
// that is a real interleaving, not a theoretical one. The mutex is held across
// the load so concurrent callers wait for one read instead of racing to
// duplicate it — the same behavior Once gave.
var (
	resolveMu     sync.Mutex
	resolveCached bool
	resolveCtx    Context
	resolveErr    error
)

// ResetResolve clears the cached Resolve() result so the next call
// re-reads from disk. Call this when the config file is known to have
// changed (e.g. after writing a new config via SaveConfig).
func ResetResolve() {
	resolveMu.Lock()
	defer resolveMu.Unlock()
	resolveCached = false
	resolveCtx = Context{}
	resolveErr = nil
}

type Context struct {
	Home       string
	ConfigPath string
	Config     appcfg.Root
}

// Resolve returns the cached runtime context. The first call loads and
// parses the config from disk; subsequent calls return the cached value.
// Call ResetResolve to force a re-read.
func Resolve() (Context, error) {
	resolveMu.Lock()
	defer resolveMu.Unlock()
	if !resolveCached {
		resolveCtx, resolveErr = resolveUncached()
		resolveCached = true
	}
	return resolveCtx, resolveErr
}

// resolveUncached performs the full resolve without caching.
func resolveUncached() (Context, error) {
	return resolveUncachedWithHooks(StartupHooks{})
}

func resolveUncachedWithHooks(hooks StartupHooks) (Context, error) {
	if err := runStartupHook(StartupResolveDataDir, hooks); err != nil {
		return Context{}, err
	}
	home, err := homepkg.Root()
	if err != nil {
		return Context{}, err
	}
	if err := runStartupHook(StartupEnsureDataDir, hooks); err != nil {
		return Context{}, err
	}
	if err := homepkg.Ensure(home); err != nil {
		return Context{}, err
	}
	if err := runStartupHook(StartupResolveConfigPath, hooks); err != nil {
		return Context{}, err
	}
	cfgPath, err := homepkg.ResolveConfigPath(home)
	if err != nil {
		cfgPath = homepkg.ConfigPath(home)
	}
	if err := runStartupHook(StartupLoadConfig, hooks); err != nil {
		return Context{}, err
	}
	MigrateLegacyChannels(cfgPath)
	cfg, err := appcfg.Load(cfgPath)
	if err != nil {
		if os.IsNotExist(err) && cfgPath == homepkg.ConfigPath(home) {
			cfg = appcfg.Root{}
		} else {
			return Context{}, err
		}
	}
	// The composed source wins once the state database holds sections (plan
	// 187): the file's runtime sections are dead after the legacy import, so
	// a reader that kept reading them would see an empty configuration after
	// any database-era write (plan 188 W9).
	if composed, ok, cerr := ComposedStartupConfig(context.Background(), home); cerr != nil {
		return Context{}, cerr
	} else if ok {
		cfg = composed
	}
	// Initialize the global model catalog so that provider validation and
	// token-limit lookups are available to all downstream code: the home gets
	// its copy of models.json when it has none, and the effective catalog is
	// the shipped one with that home copy merged over it, field by field.
	if err := llm.InitGlobalCatalog(home, homepkg.Version); err != nil {
		slog.Warn("failed to initialize global model catalog", "err", err)
	}
	if err := runStartupHook(StartupMergeConfigEnv, hooks); err != nil {
		return Context{}, err
	}
	if err := runStartupHook(StartupValidateEntrypointConfig, hooks); err != nil {
		return Context{}, err
	}
	return Context{Home: home, ConfigPath: cfgPath, Config: cfg}, nil
}

// SaveConfig persists the setup flow's configuration change (plan 188 W9):
// the agents section — the only one a setup or /connect changes — goes into
// the state database, the one write path every configuration surface shares.
// forebrain.yaml is not written; a home whose file does not exist yet gets a
// minimal startup-sections one created, because the gateway binds before a
// database may exist and the file is where its startup sections live.
func SaveConfig(ctx Context) error {
	if ctx.ConfigPath == "" {
		return fmt.Errorf("empty config path")
	}
	if ctx.Home == "" {
		return fmt.Errorf("empty home")
	}
	if err := ensureStartupConfigFile(ctx.ConfigPath); err != nil {
		return err
	}
	// The setup flow may run before anything else has laid the home out, so
	// the state directory's existence cannot be assumed here.
	if err := os.MkdirAll(filepath.Join(ctx.Home, "state"), 0o755); err != nil {
		return err
	}
	db, err := state.OpenStateFromHome(context.Background(), ctx.Home, nil)
	if err != nil {
		return fmt.Errorf("open state database: %w", err)
	}
	defer db.Close()
	InstallConfigSectionValidator(ctx.ConfigPath, db)
	env := &Environment{Root: ctx.Home, ConfigPath: ctx.ConfigPath, SQL: db}
	return env.UpdateRootSection(context.Background(), "agents", func(cfg *appcfg.Root) error {
		cfg.Agents = ctx.Config.Agents
		return nil
	})
}

// ErrorLogPath is where a forebrain home keeps its error log.
func ErrorLogPath(home string) string {
	return filepath.Join(strings.TrimSpace(home), homepkg.LogsDir, "error.log")
}

// AppVersion reports the embedded application version. Gateway surfaces read
// it through process (already a dependency) because their package fan-out
// ceiling has no room for a direct pkg/home import.
func AppVersion() string { return homepkg.Version }
