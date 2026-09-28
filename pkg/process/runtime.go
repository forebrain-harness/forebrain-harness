package process

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	homepkg "github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
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
	cfg, err := appcfg.Load(cfgPath)
	if err != nil {
		if os.IsNotExist(err) && cfgPath == homepkg.ConfigPath(home) {
			cfg = appcfg.Root{}
		} else {
			return Context{}, err
		}
	}
	// Initialize the global model catalog from models.json so that provider
	// validation and token-limit lookups are available to all downstream code.
	if err := llm.InitGlobalCatalog(home); err != nil {
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

func SaveConfig(ctx Context) error {
	if ctx.ConfigPath == "" {
		return fmt.Errorf("empty config path")
	}
	return appcfg.Save(ctx.ConfigPath, ctx.Config)
}

// ErrorLogPath is where a forebrain home keeps its error log.
func ErrorLogPath(home string) string {
	return filepath.Join(strings.TrimSpace(home), homepkg.LogsDir, "error.log")
}

// AppVersion reports the embedded application version. Gateway surfaces read
// it through process (already a dependency) because their package fan-out
// ceiling has no room for a direct pkg/home import.
func AppVersion() string { return homepkg.Version }
