package process

const (
	StartupResolveDataDir           = "resolve_data_dir"
	StartupEnsureDataDir            = "ensure_data_dir"
	StartupResolveConfigPath        = "resolve_config_path"
	StartupLoadConfig               = "load_config"
	StartupMergeConfigEnv           = "merge_config_env"
	StartupValidateEntrypointConfig = "validate_entrypoint_config"
)

type StartupHooks struct {
	ResolveDataDir           func() error
	EnsureDataDir            func() error
	ResolveConfigPath        func() error
	LoadConfig               func() error
	MergeConfigEnv           func() error
	ValidateEntrypointConfig func() error
}

func runStartupHook(stage string, hooks StartupHooks) error {
	switch stage {
	case StartupResolveDataDir:
		return callStartupHook(hooks.ResolveDataDir)
	case StartupEnsureDataDir:
		return callStartupHook(hooks.EnsureDataDir)
	case StartupResolveConfigPath:
		return callStartupHook(hooks.ResolveConfigPath)
	case StartupLoadConfig:
		return callStartupHook(hooks.LoadConfig)
	case StartupMergeConfigEnv:
		return callStartupHook(hooks.MergeConfigEnv)
	case StartupValidateEntrypointConfig:
		return callStartupHook(hooks.ValidateEntrypointConfig)
	default:
		return nil
	}
}

func callStartupHook(fn func() error) error {
	if fn == nil {
		return nil
	}
	return fn()
}
