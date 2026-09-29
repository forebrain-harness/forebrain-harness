# 任务 06：内置目录（12 门语言）、配置合并、根解析、二进制探测

> **执行者须知**：逐步执行；每一步运行「验证」命令并确认结果后再进入下一步。出现「STOP 条件」中的任何情况立即停止并汇报。完成后把 `docs/plan/lsp/README.md` 中本任务状态改为 `DONE`。
>
> **规范**：`docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md`。开始前读规范 §5.1、§5.3、§5.4（`enabled.json`、`detect.json`）、§6（全部）、§7.4、§8.5。
>
> **前置**：任务 01（`appcfg.LSPServerConfig`、`LSPJSONObject`）与任务 03（`pkg/lsp/resolve.go` 里的骨架 `ToolEnabled`）已合入。本任务与任务 04/05 可以并行，但若 04/05 已合入也没有冲突（只碰不同文件）。
>
> **漂移检查（先运行）**：`git diff --stat 6305ea9..HEAD -- pkg/config/lsp.go pkg/lsp/resolve.go`，与「现状」对比。

## 状态

- 优先级：P0 · 工作量：M · 风险：LOW（纯数据与纯函数）
- 依赖：03（以及 01）
- 类别：direction
- 计划基于：`6305ea9`，2026-09-29

## 为什么

「哪个文件交给哪个服务器、用什么命令启动、根目录在哪、装没装、怎么装」全部由本任务决定。目录首发覆盖需求点名的 12 门语言。`ToolEnabled` 在本任务后开始返回真实结果——但由于目录服务器默认未启用、`enabled.json` 还没有写入口，所以对现有用户仍然恒为 false，模型可见内容不变。

## 现状

- `pkg/lsp/resolve.go`（任务 03）：只有骨架 `func ToolEnabled(cfg *appcfg.Root, opts ManagerOptions) bool { return false }`。
- `pkg/lsp/manager.go`（任务 03）：`ManagerOptions{Home, AgentWorkspace, ProjectRoot, ProjectKey, Trusted, VersionControlled, ToolRegistered}`。
- `pkg/config/lsp.go`（任务 01）：`LSPServerConfig`（字段见该文件）、`LSPJSONObject`（实现了 yaml v2 的 `UnmarshalYAML`，YAML 对象 → JSON 字节）、`LSPRolePrimary`、`LSPRoleDiagnostics`；`Root.LSP.Servers map[string]LSPServerConfig`；`(*Root).EffectiveFeatures().LSP`。
- YAML 库：`go.yaml.in/yaml/v2`（`pkg/config/load.go:12` 在用）。
- `pkg/lsp` 当前生产文件：任务 03 的 5 个，加上已合入的任务 04/05 文件。本任务新增 2 个（`catalog.go`、`detect.go`），`resolve.go` 为修改。

## 范围

**新建：** `pkg/lsp/catalog.go`、`pkg/lsp/catalog/*.yaml`（11 个文件，C 与 C++ 共用 clangd）、`pkg/lsp/detect.go`，以及 `catalog_test.go`、`detect_test.go`。**修改：** `pkg/lsp/resolve.go`、`pkg/lsp/resolve_test.go`、`pkg/lsp/doc.go`、`pkg/architecture/testdata/graph.json`（重新生成）。

**不要碰：** `pkg/lsp` 的 `instance.go`、`pool.go`、`manager.go`、`control.go`（任务 08 才让它们使用本任务的结果）；`pkg/config`。

## 步骤

### 步骤 1：`catalog.go` —— 类型与加载

```go
//go:embed catalog/*.yaml
var catalogFS embed.FS

type CatalogEntry struct {
	ID                    string               `yaml:"id"`
	DisplayName           string               `yaml:"display_name"`
	Languages             []string             `yaml:"languages"`
	Role                  string               `yaml:"role"`
	Priority              int                  `yaml:"priority"`
	Command               string               `yaml:"command"`
	Args                  []string             `yaml:"args"`
	CommandDarwin         string               `yaml:"command_darwin"`
	ArgsDarwin            []string             `yaml:"args_darwin"`
	ExtensionToLanguage   map[string]string    `yaml:"extension_to_language"`
	Filenames             map[string]string    `yaml:"filenames"`
	RootMarkers           []string             `yaml:"root_markers"`
	RootStrategy          string               `yaml:"root_strategy"` // "" | "nearest" | "cargo-workspace"
	EnvPassthrough        []string             `yaml:"env_passthrough"`
	Detect                DetectSpec           `yaml:"detect"`
	Install               []InstallRecipe      `yaml:"install"`
	StartupTimeout        int                  `yaml:"startup_timeout"` // seconds
	Readiness             string               `yaml:"readiness"`
	AutoAnswers           map[string]string    `yaml:"auto_answers"`
	InitializationOptions appcfg.LSPJSONObject `yaml:"initialization_options"`
	Settings              appcfg.LSPJSONObject `yaml:"settings"`
	Conflicts             []Conflict           `yaml:"conflicts"`
	ProjectWrites         []string             `yaml:"project_writes"`
	Notes                 string               `yaml:"notes"`
}

type DetectSpec struct {
	ExtraDirs   []string `yaml:"extra_dirs"`
	VersionArgs []string `yaml:"version_args"` // empty: found on disk counts as installed, no version
	Xcrun       string   `yaml:"xcrun"`        // darwin: tool name for `xcrun --find`
}

type InstallRecipe struct {
	Requires  string   `yaml:"requires"`  // a command that must be on the PATH
	Argv      []string `yaml:"argv"`      // run without a shell
	Platforms []string `yaml:"platforms"` // GOOS values; empty means all
}

type Conflict struct {
	With           string   `yaml:"with"`
	WhenRootMarker []string `yaml:"when_root_marker"`
}

// Catalog returns every built-in server, sorted by ID. It parses the embedded
// files once; a parse error is a build defect that the catalog test catches.
func Catalog() ([]CatalogEntry, error)

// LookupCatalog returns one built-in server.
func LookupCatalog(id string) (CatalogEntry, bool)
```

加载规则：`yaml.UnmarshalStrict`（拒绝未知字段）；文件名必须等于 `<id>.yaml`；`Role` 为空时取 `primary`；`RootStrategy` 为空时取 `nearest`；`Readiness` 为空时取 `none`；扩展名键转小写。用 `sync.Once` 缓存结果与错误。

测试替换点（后续任务 08、12、13 的测试需要「未启用的目录服务器」与自定义安装配方）：

```go
// catalogOverride replaces the embedded catalog in tests; nil in production.
var catalogOverride []CatalogEntry
```

`Catalog()` 与 `LookupCatalog()` 在 `catalogOverride != nil` 时直接使用它（按 ID 排序后返回），不读嵌入文件。测试用 `t.Cleanup(func() { catalogOverride = nil })` 恢复。

### 步骤 2：写 11 个目录文件 `pkg/lsp/catalog/<id>.yaml`

内容**逐字**如下（可以调整空白，不得改值）：

`gopls.yaml`

```yaml
id: gopls
display_name: gopls
languages: [Go]
priority: 100
command: gopls
args: [serve]
extension_to_language: {.go: go}
filenames: {go.mod: go.mod, go.work: go.work}
root_markers: [go.work, go.mod]
env_passthrough: [GOPATH, GOROOT, GOBIN, GOFLAGS, GOPROXY, GOPRIVATE, GONOPROXY, GONOSUMDB, GOTOOLCHAIN, GOWORK, CGO_ENABLED, CC, CXX, PKG_CONFIG_PATH, HTTP_PROXY, HTTPS_PROXY, NO_PROXY]
detect:
  extra_dirs: ["$GOBIN", "$GOPATH/bin", "~/go/bin"]
  version_args: [version]
install:
  - requires: go
    argv: [go, install, golang.org/x/tools/gopls@latest]
startup_timeout: 60
readiness: progress
notes: "Install with: go install golang.org/x/tools/gopls@latest"
```

`rust-analyzer.yaml`

```yaml
id: rust-analyzer
display_name: rust-analyzer
languages: [Rust]
priority: 100
command: rust-analyzer
extension_to_language: {.rs: rust}
root_markers: [Cargo.toml, rust-project.json]
root_strategy: cargo-workspace
env_passthrough: [CARGO_HOME, RUSTUP_HOME, RUSTUP_TOOLCHAIN, RUSTFLAGS, HTTP_PROXY, HTTPS_PROXY, NO_PROXY]
detect:
  extra_dirs: ["$CARGO_HOME/bin", "~/.cargo/bin"]
  version_args: [--version]
install:
  - requires: rustup
    argv: [rustup, component, add, rust-analyzer]
startup_timeout: 120
readiness: rust-analyzer-status
initialization_options:
  cargo: {targetDir: true}
settings:
  rust-analyzer:
    cargo: {targetDir: true}
project_writes: [target/rust-analyzer]
notes: "Install with: rustup component add rust-analyzer"
```

`clangd.yaml`

```yaml
id: clangd
display_name: clangd
languages: [C, C++, Objective-C, CUDA]
priority: 100
command: clangd
args: [--background-index, --header-insertion=never, --log=error]
extension_to_language: {.c: c, .h: c, .cc: cpp, .cpp: cpp, .cxx: cpp, .c++: cpp, .hpp: cpp, .hh: cpp, .hxx: cpp, .ipp: cpp, .m: objective-c, .mm: objective-cpp, .cu: cuda-cpp, .cuh: cuda-cpp}
root_markers: [compile_commands.json, compile_flags.txt, .clangd, CMakeLists.txt, meson.build, Makefile]
env_passthrough: [CPATH, C_INCLUDE_PATH, CPLUS_INCLUDE_PATH, SDKROOT, DEVELOPER_DIR]
detect:
  xcrun: clangd
  extra_dirs: ["/opt/homebrew/opt/llvm/bin", "/usr/local/opt/llvm/bin"]
  version_args: [--version]
install:
  - requires: brew
    platforms: [darwin]
    argv: [brew, install, llvm]
startup_timeout: 60
readiness: none
project_writes: [.cache/clangd]
notes: "Install clangd with your system package manager (for example apt install clangd), or the LLVM installer on Windows. Generate compile_commands.json for accurate diagnostics: cmake -DCMAKE_EXPORT_COMPILE_COMMANDS=ON, or bear -- make."
```

`jdtls.yaml`

```yaml
id: jdtls
display_name: Eclipse JDT Language Server
languages: [Java]
priority: 100
command: jdtls
args: [-data, "${LSP_CACHE_DIR}"]
extension_to_language: {.java: java}
root_markers: [pom.xml, build.gradle, build.gradle.kts, settings.gradle, settings.gradle.kts, mvnw, gradlew, .project]
env_passthrough: [JAVA_HOME, MAVEN_OPTS, M2_HOME, GRADLE_USER_HOME, GRADLE_OPTS, HTTP_PROXY, HTTPS_PROXY, NO_PROXY]
install:
  - requires: brew
    platforms: [darwin]
    argv: [brew, install, jdtls]
startup_timeout: 180
readiness: jdtls-status
initialization_options:
  settings:
    java:
      import: {generatesMetadataFilesAtProjectRoot: false}
settings:
  java:
    import: {generatesMetadataFilesAtProjectRoot: false}
notes: "Needs JDK 21 or newer and python3. On macOS: brew install jdtls. Elsewhere, download Eclipse JDT LS and put its bin/jdtls on the PATH."
```

`kotlin-lsp.yaml`

```yaml
id: kotlin-lsp
display_name: Kotlin LSP
languages: [Kotlin]
priority: 100
command: kotlin-lsp
args: [--stdio]
extension_to_language: {.kt: kotlin, .kts: kotlin}
root_markers: [settings.gradle.kts, settings.gradle, build.gradle.kts, build.gradle, pom.xml]
env_passthrough: [JAVA_HOME, GRADLE_USER_HOME, GRADLE_OPTS]
install:
  - requires: brew
    platforms: [darwin]
    argv: [brew, install, JetBrains/utils/kotlin-lsp]
startup_timeout: 180
readiness: progress
notes: "Needs a JDK. On macOS: brew install JetBrains/utils/kotlin-lsp. Elsewhere, unpack JetBrains' kotlin-lsp release and put kotlin-lsp on the PATH."
```

`sourcekit-lsp.yaml`

```yaml
id: sourcekit-lsp
display_name: SourceKit-LSP
languages: [Swift]
priority: 100
command: sourcekit-lsp
command_darwin: xcrun
args_darwin: [sourcekit-lsp]
extension_to_language: {.swift: swift}
root_markers: [Package.swift, buildServer.json, compile_commands.json]
env_passthrough: [DEVELOPER_DIR, SDKROOT, TOOLCHAINS]
detect:
  xcrun: sourcekit-lsp
startup_timeout: 60
readiness: none
project_writes: [.build]
notes: "Ships with Xcode on macOS and with the Swift toolchain from swift.org elsewhere."
```

`metals.yaml`

```yaml
id: metals
display_name: Metals
languages: [Scala]
priority: 100
command: metals
extension_to_language: {.scala: scala, .sc: scala, .sbt: scala, .mill: scala}
root_markers: [build.sbt, build.sc, build.mill, project/build.properties, .bsp, project.scala]
env_passthrough: [JAVA_HOME, SBT_OPTS, JAVA_OPTS, COURSIER_CACHE, HTTP_PROXY, HTTPS_PROXY, NO_PROXY]
detect:
  extra_dirs: ["~/.local/share/coursier/bin", "~/Library/Application Support/Coursier/bin"]
install:
  - requires: cs
    argv: [cs, install, metals]
startup_timeout: 180
readiness: none
auto_answers: {"Import build": "Import build", "New sbt workspace detected": "Import build"}
initialization_options: {statusBarProvider: log-message, isHttpEnabled: false}
project_writes: [.metals, .bloop, .bsp]
notes: "Install with Coursier: cs install metals"
```

`csharp-ls.yaml`

```yaml
id: csharp-ls
display_name: csharp-ls
languages: [C#]
priority: 100
command: csharp-ls
extension_to_language: {.cs: csharp, .csx: csharp}
root_markers: ["*.sln", "*.slnx", "*.csproj", global.json, Directory.Build.props]
env_passthrough: [DOTNET_ROOT, DOTNET_CLI_HOME, NUGET_PACKAGES]
detect:
  extra_dirs: ["~/.dotnet/tools"]
  version_args: [--version]
install:
  - requires: dotnet
    argv: [dotnet, tool, install, --global, csharp-ls]
startup_timeout: 120
readiness: progress
notes: "Needs the .NET SDK. Run dotnet restore for complete diagnostics."
```

`typescript-language-server.yaml`

```yaml
id: typescript-language-server
display_name: typescript-language-server
languages: [TypeScript, JavaScript]
priority: 100
command: typescript-language-server
args: [--stdio]
extension_to_language: {.ts: typescript, .mts: typescript, .cts: typescript, .tsx: typescriptreact, .js: javascript, .mjs: javascript, .cjs: javascript, .jsx: javascriptreact}
root_markers: [tsconfig.json, jsconfig.json, package.json]
env_passthrough: [NODE_PATH, NODE_OPTIONS, NPM_CONFIG_PREFIX]
detect:
  version_args: [--version]
install:
  - requires: npm
    argv: [npm, install, -g, typescript-language-server, typescript]
startup_timeout: 60
readiness: none
notes: "Install with: npm install -g typescript-language-server typescript"
```

`intelephense.yaml`

```yaml
id: intelephense
display_name: Intelephense
languages: [PHP]
priority: 100
command: intelephense
args: [--stdio]
extension_to_language: {.php: php, .phtml: php}
root_markers: [composer.json]
detect:
  version_args: [--version]
install:
  - requires: npm
    argv: [npm, install, -g, intelephense]
startup_timeout: 60
readiness: progress
initialization_options: {storagePath: "${LSP_CACHE_DIR}", globalStoragePath: "${LSP_CACHE_DIR}/global"}
notes: "Install with: npm install -g intelephense"
```

`pyright.yaml`

```yaml
id: pyright
display_name: Pyright
languages: [Python]
priority: 100
command: pyright-langserver
args: [--stdio]
extension_to_language: {.py: python, .pyi: python}
root_markers: [pyproject.toml, pyrightconfig.json, setup.py, setup.cfg, requirements.txt, Pipfile]
env_passthrough: [VIRTUAL_ENV, CONDA_PREFIX, PYTHONPATH]
install:
  - requires: npm
    argv: [npm, install, -g, pyright]
startup_timeout: 60
readiness: none
settings:
  python:
    analysis: {diagnosticMode: openFilesOnly}
notes: "Install with: npm install -g pyright (or pipx install pyright)"
```

注意：YAML 中以 `.` 开头的键、`*.sln` 这样的值要按上面的写法（`*` 开头的值必须加引号）。

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/lsp` → 退出码 0。

### 步骤 3：`resolve.go` —— 合并、匹配、根、`ToolEnabled`

```go
// ServerConfig is one server after merging (spec §5.3).
type ServerConfig struct {
	ID, DisplayName       string
	Languages             []string
	Scope                 string // "catalog" | "global" | "project"
	InCatalog             bool
	Role                  string
	Priority              int
	Command               string
	Args                  []string
	ExtensionToLanguage   map[string]string
	Filenames             map[string]string
	RootMarkers           []string
	RootStrategy          string
	WorkspaceFolder       string
	Env                   map[string]string
	EnvPassthrough        []string
	EnvFromProject        bool
	InitializationOptions json.RawMessage
	Settings              json.RawMessage
	StartupTimeout        time.Duration
	ShutdownTimeout       time.Duration
	RestartOnCrash        bool
	MaxRestarts           int
	Diagnostics           bool
	Prewarm               bool
	Readiness             string
	AutoAnswers           map[string]string
	Conflicts             []Conflict
	ProjectWrites         []string
	Install               []InstallRecipe
	Detect                DetectSpec
	Notes                 string
	Enabled               bool
	Fingerprint           string // 12 hex characters
	Invalid               string // why the entry cannot run; "" when usable
}

type ResolveInput struct {
	Config         *appcfg.Root
	ProjectServers map[string]appcfg.LSPServerConfig // consented project entries only (task 15); nil before that
	Enabled        map[string]bool                   // enabled.json
	GOOS           string                            // runtime.GOOS in production
}

// ResolveServers merges catalog, global and project entries; sorted by ID.
func ResolveServers(in ResolveInput) []ServerConfig

// MatchFile returns every server (enabled or not) whose extensions or
// filenames cover absPath, in ID order.
func MatchFile(servers []ServerConfig, absPath string) []ServerConfig

// ServersForFile picks, among enabled and valid matches, the primary server
// (spec §6.3) and every diagnostics-role server. primary is nil when none.
func ServersForFile(servers []ServerConfig, absPath, projectRoot string) (primary *ServerConfig, diagnostics []ServerConfig)

// ResolveRoot finds the workspace root for absFile (spec §7.4). ok is false
// when absFile is outside projectRoot.
func ResolveRoot(absFile, projectRoot string, srv ServerConfig) (root string, ok bool)

// StateDir is <agentWorkspace>/state/lsp.
func StateDir(agentWorkspace string) string

// LoadEnabled reads <StateDir>/enabled.json; a missing file is an empty map.
func LoadEnabled(agentWorkspace string) (map[string]bool, error)

// SaveEnabled writes one server's switch atomically (read-modify-write).
func SaveEnabled(agentWorkspace, id string, enabled bool) error

// CacheDir and LogPath name per-root paths (spec §5.4).
func CacheDir(agentWorkspace, id, root string) string
func LogPath(agentWorkspace, id, root string) string
func PIDFile(agentWorkspace string) string

// ToolEnabled: features.lsp && Trusted && ProjectRoot != "" && at least one
// enabled, valid, primary server (spec §8.5).
func ToolEnabled(cfg *appcfg.Root, opts ManagerOptions) bool
```

合并规则（逐条照规范 §5.3，实现要点）：

1. 起点：目录中每个条目转成 `ServerConfig{Scope: "catalog", InCatalog: true, …}`；`GOOS == "darwin"` 且 `CommandDarwin != ""` 时用 `CommandDarwin`/`ArgsDarwin` 替换 `Command`/`Args`；`StartupTimeout` 为 0 时 60 秒；`ShutdownTimeout` 5 秒；`RestartOnCrash` true；`MaxRestarts` 3；`Diagnostics` true。
2. 叠加全局 `in.Config.LSP.Servers[id]`（存在则 `Scope = "global"`），再叠加 `in.ProjectServers[id]`（存在则 `Scope = "project"`、`EnvFromProject = true`；项目条目的 `EnvPassthrough` 与 `Priority` **忽略**）。叠加时「非零」才覆盖：字符串非空、切片/映射非 nil、`*bool`/`*int` 非 nil、整数非 0、`LSPJSONObject` 非空。
3. 目录中没有的 id：以全局/项目条目为起点，`InCatalog = false`，`Priority` 缺省 50，`Readiness` 缺省 `none`；缺 `Command` 或缺扩展名与文件名时 `Invalid = "custom servers need command and extension_to_language or filenames"`。
4. `Enabled`：`in.Enabled[id]` 存在则用它；否则取叠加后的 `enabled`；否则 `InCatalog` 为 false、自定义为 true。
5. `Fingerprint`：把合并结果复制一份，清零 `Enabled`、`Priority`、`Prewarm`、`Scope`、`Fingerprint`、`Invalid` 后 `json.Marshal`（map 的键 `encoding/json` 会排序），sha256 取前 12 个十六进制字符。

`ServersForFile` 的 primary 选择（规范 §6.3）：候选 = `MatchFile` 中 `Enabled && Invalid == "" && Role == "primary"`；依次按：`Scope == "project"` 优先 → 某候选的 `Conflicts` 里有 `With` 等于另一个候选且其 `WhenRootMarker` 中任一文件存在于「从文件目录向上到项目根」的某一级 → 该候选胜出 → `Priority` 大者 → ID 字典序小者。diagnostics 列表 = 匹配中 `Enabled && Invalid == "" && Role == "diagnostics"`。

`ResolveRoot`：逐条照规范 §7.4（`filepath.EvalSymlinks` 失败时退回 `filepath.Clean`；比较用任务 04 的 `SamePath` 规则判断前缀——darwin/windows 不区分大小写）。

`LoadEnabled`/`SaveEnabled`：文件格式 `{"enabled": {"gopls": true}}`；写入先写同目录临时文件再 `os.Rename`；目录不存在时创建。`CacheDir` = `StateDir/cache/<id>/<roothash8>`，`LogPath` = `StateDir/logs/<id>-<roothash8>.log`，`PIDFile` = `StateDir/pids.json`，`roothash8` = `sha256(root)` 前 8 个十六进制字符。

`ToolEnabled`：用 `LoadEnabled(opts.AgentWorkspace)`（读失败视为空）和 `ResolveServers(ResolveInput{Config: cfg, Enabled: …, GOOS: runtime.GOOS})` 判断；`ProjectServers` 留 nil（任务 15 补上）。

### 步骤 4：`detect.go` —— 探测与安装配方选择

```go
type DetectResult struct {
	Installed      bool
	Path           string
	Version        string
	InstallRecipe  *InstallRecipe // first usable recipe (spec §6.5); nil when none
	InstallCommand string         // strings.Join(recipe.Argv, " ")
}

// Detect locates srv's command on the server's PATH, extra dirs and (darwin)
// xcrun, and reads its version (spec §6.4). cachePath is detect.json ("" disables caching).
func Detect(ctx context.Context, srv ServerConfig, env []string, goos string, cachePath string) DetectResult

// UsableInstallRecipe returns the first recipe whose platform matches goos and
// whose Requires command is on the PATH in env.
func UsableInstallRecipe(srv ServerConfig, env []string, goos string) *InstallRecipe

// PathFromEnv returns the PATH entry of env, or os.Getenv("PATH").
func PathFromEnv(env []string) string
```

规则：

1. 命令是绝对路径：存在且可执行即找到。否则在 `PathFromEnv(env)` 上查找（实现一个接受 PATH 参数的查找函数；Windows 要考虑 `PATHEXT`，可参照 `exec.LookPath` 的规则），再依次查 `Detect.ExtraDirs`（`$VAR` 从 env 里取，展开后为空或仍含 `$` 的项跳过；`~/` 展开为 `os.UserHomeDir()`）。
2. `goos == "darwin"` 且 `Detect.Xcrun != ""`：执行 `xcrun --find <Xcrun>`（2 秒超时），退出码 0 且输出是存在的文件即找到（`Path` 取该输出）。
3. 找到后：`VersionArgs` 为空 → `Installed = true`，`Version = ""`；否则执行 `<path> <VersionArgs…>`（3 秒超时，`cmd.Env = env`），退出码 0 → `Installed = true`，`Version` 取合并输出的第一行去空白、截到 80 字符；退出码非 0 → `Installed = false`（rustup 代理的情况）。
4. 缓存：`cachePath` 非空时读写 `{"<path>": {"mtime": <unix seconds>, "version": "…", "installed": true}}`；同一路径 mtime 未变则直接用缓存，不执行版本命令。
5. 无论是否安装，都填 `InstallRecipe`/`InstallCommand`。

### 步骤 5：更新 `doc.go`，写测试

`doc.go` 文件清单加：`catalog.go and catalog/*.yaml (built-in servers), detect.go (finding and versioning server binaries)`，并把 `resolve.go` 的描述改为 `resolve.go (merging catalog and configuration, file matching, workspace roots, the lsp tool decision, per-agent state paths)`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/lsp -count=1 -race` → `ok`。

### 步骤 6：全量与包图

**验证**：`gofmt -l pkg/lsp`、`go vet ./pkg/lsp` 干净；`GOOS=windows GOARCH=amd64 go vet ./pkg/lsp` 退出码 0；`scripts/package-graph.sh && git status --short pkg/architecture/testdata/graph.json` → ` M`（提交）；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` → 全部 `ok`。

## 测试计划

- `catalog_test.go`
  - `TestCatalogLoads`：`Catalog()` 无错误；**至少**包含这 11 个 id：`clangd csharp-ls gopls intelephense jdtls kotlin-lsp metals pyright rust-analyzer sourcekit-lsp typescript-language-server`（任务 17 会再加条目，所以不要断言总数）。
  - `TestCatalogCoversRequiredLanguages`：对 `Java Go Rust C C++ Kotlin Swift Scala C# TypeScript JavaScript PHP Python` 每门语言，至少一个条目的 `Languages` 包含它。
  - `TestCatalogEntriesAreComplete`：每个条目 `Command` 非空、至少一个扩展名或文件名、扩展名键以 `.` 开头且小写、`Readiness` 是四个常量之一、`RootStrategy` 合法、`Install` 每条 `Argv` 非空、`Notes` 非空。
  - `TestCatalogNoUnresolvedPrimaryConflicts`：对所有扩展名，若有两个以上 primary 条目，它们 `Priority` 不同或存在覆盖该对的 `Conflicts` 规则。
  - `TestCatalogFilenameMatchesID`：遍历 `catalogFS`，文件名等于 `<id>.yaml`。
  （注：必选的 12 门语言由 11 个条目覆盖——clangd 同时覆盖 C 与 C++。）
- `resolve_test.go`（替换任务 03 的 `TestToolEnabledSkeletonIsFalse`）
  - `TestResolveServersDefaults`：空配置 → 所有目录条目 `Enabled == false`、`Scope == "catalog"`、`Fingerprint` 为 12 位十六进制。
  - `TestResolveServersOverlay`：全局覆盖 gopls 的 `settings` 与 `enabled: true` → `Scope == "global"`、`Enabled`、settings 为覆盖值、`Command` 仍为 `gopls`；指纹变化；`enabled.json` 写 false 时 `Enabled == false`。
  - `TestResolveServersProjectOverlay`：`ProjectServers` 覆盖 `args` 并带 `env_passthrough` 与 `priority` → `Scope == "project"`、`EnvFromProject`、`Args` 被覆盖、`EnvPassthrough` 与 `Priority` 未被覆盖。
  - `TestResolveServersCustom`：自定义条目缺扩展名 → `Invalid` 非空；完整时 `Enabled` 默认 true、`Priority` 50。
  - `TestResolveServersDarwinCommand`：`GOOS: "darwin"` 时 sourcekit-lsp 的 `Command == "xcrun"`、`Args == ["sourcekit-lsp"]`；`linux` 时为 `sourcekit-lsp`。
  - `TestServersForFile`：`.go` 选 gopls（启用时）；未启用时 primary 为 nil；两个 primary 同扩展名按 priority；`Conflicts` 规则在根上存在标记文件时生效；diagnostics 型进入第二个返回值。
  - `TestResolveRoot`：最近标记；`workspace_folder` 相对路径；项目外返回 `ok == false`；`*.sln` 通配；cargo-workspace 选最上层含 `[workspace]` 的目录；没有标记时为项目根。
  - `TestEnabledStateRoundTrip`：`SaveEnabled` 后 `LoadEnabled` 读回；文件不存在读为空 map。
  - `TestToolEnabled`：未受信任 → false；受信任但无启用 → false；`enabled.json` 启用 gopls → true；`features.lsp: false` → false；`ProjectRoot == ""` → false。
- `detect_test.go`
  - `TestDetectOnPath`：临时目录放一个可执行脚本 `fakels`（Unix：`#!/bin/sh\necho fakels 1.2.3`；Windows 跳过或用 `.bat`），`env` 的 PATH 指向它 → `Installed`、`Version == "fakels 1.2.3"`。
  - `TestDetectVersionFailureMeansNotInstalled`：脚本 `exit 1` → `Installed == false`、`Path` 非空。
  - `TestDetectExtraDirsAndHome`：命令只在 `$FOO/bin` 里，`env` 含 `FOO=<dir>` → 找到；`$UNSET/bin` 被跳过。
  - `TestDetectCache`：第二次调用不执行版本命令（脚本每次执行往计数文件追加一行，断言只有一行）。
  - `TestUsableInstallRecipe`：`platforms: [darwin]` 在 linux 上不可用；`requires` 不在 PATH 上不可用；第一条可用的被选中。

## 完成判据

- [ ] `ls pkg/lsp/catalog/*.yaml | wc -l` 为 11，`TestCatalogCoversRequiredLanguages` 通过
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/lsp -count=1 -race` 通过
- [ ] `ls pkg/lsp/*.go | grep -v _test.go | wc -l` 比任务开始时多 2
- [ ] `go vet`（含 `GOOS=windows`）、`gofmt` 干净；`graph.json` 已提交
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` 全部 `ok`
- [ ] README 中任务 06 状态为 `DONE`

（说明：规范说「12 门语言」，是 Java、Go、Rust、C、C++、Kotlin、Swift、Scala、C#、TypeScript/JavaScript、PHP、Python；C 与 C++ 共用 `clangd`，所以是 11 个 YAML 文件。）

## STOP 条件

- `yaml.UnmarshalStrict` 无法解析 `LSPJSONObject` 字段（说明任务 01 的实现与描述不符）。
- 需要改 `pkg/config`。
- 需要 `pkg/lsp` 导入 `config`、`event`、`home`、`tool` 以外的本仓库包。

## 维护说明

- 目录 YAML 的值（命令、参数、根标记、设置）要对照上游文档维护；任务 12 的集成测试与 nightly 作业会暴露过时的条目。
- `ServerConfig` 是任务 08（启动实例）、09（选服务器）、12（doctor）、13（推荐）、15（项目条目）的共同输入；改字段要全局搜索使用处。
- 评审重点：「非零才覆盖」的叠加语义；指纹不含 `Enabled`（否则开关会重启进程）；项目条目不能扩展 `env_passthrough`。
