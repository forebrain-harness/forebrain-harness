# 任务 16：`/migrate` 导入 Claude Code 的 LSP 插件

> **执行者须知**：逐步执行；每一步运行「验证」命令并确认结果后再进入下一步。出现「STOP 条件」中的任何情况立即停止并汇报。完成后把 `docs/plan/lsp/README.md` 中本任务状态改为 `DONE`。
>
> **规范**：`docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md`。开始前读规范 §1.2、§3.13、§5.1、§5.5、§11.5。
>
> **前置**：任务 01 已合入（`appcfg.Root.LSP`、`appcfg.LSPServerConfig`、`appcfg.LSPJSONObject`、`appcfg.ValidateLSPServers`）。先确认：`grep -n "func ValidateLSPServers" pkg/config/lsp.go` 有输出，否则 STOP。本任务**不**依赖 `pkg/lsp`。
>
> **漂移检查（先运行）**：`git diff --stat 6305ea9..HEAD -- pkg/migrate`。对比「现状」摘录。

## 状态

- 优先级：P2 · 工作量：M · 风险：LOW（只写 `forebrain.yaml`，写前备份；预览与真实运行同一路径）
- 依赖：01
- 类别：direction
- 计划基于：`6305ea9`，2026-09-29

## 为什么

从 Claude Code 迁移过来的用户已经用插件启用过语言服务器（截图里的 `gopls-lsp` 就是其中之一）。`/migrate` 应把这些选择带过来：官方插件对应到内置目录的服务器并启用；其他插件的服务器定义转成自定义条目。

## 现状

- `pkg/migrate` 有 11 个生产文件，fan-out 8/8（**不能新增导入**，所以不能导入 `pkg/lsp`；映射表写在本包）。
- 类别：`pkg/migrate/plan.go:303`（Claude）与 `:376`（Codex）各有一份 `[]string{"sessions", "memories", "skills", "plans", "mcp", "history"}`；`Options.wants(category)`（`pkg/migrate/source.go:317`）；`Options.Only` 的注释列出类别（`source.go:258-259`）。
- Claude 数据发现：`claudeData`（`pkg/migrate/claude.go:14`）；`discoverClaude` 在 `:189` 调用 `discoverPluginSkills(root)`；`discoverPluginSkills`（:355）读 `settings.json` 的 `enabledPlugins`（键为 `<name>@<marketplace>`，值为 true 才算启用）；插件缓存目录 `<root>/plugins/cache/<marketplace>/<name>/<version>/`，取字典序最大的版本目录（`pluginSkillsFor`，:393）。
- 写配置的范例：`importUserMCP`（`pkg/migrate/assets.go:501`）——`appcfg.Load(opts.ConfigPath)`；同名条目存在 → `skipped` + `an entry with this name is already configured; not overwritten`；有改动时非 dry run 先 `backupFile(opts.ConfigPath, opts.now())`（:1159）再 `appcfg.Save`；notice `written MCP entries take effect for new sessions only`。
- 报告：`Report`（`plan.go:11`）与 `Plan`（`:71`）；`reportToPlan`；`Plan.Text()`（:203，预览）；`Report.Text()`（:495）；`outcomeLine(name, status, detail)`（:611）；`countStatus`。
- 明文密钥：`appcfg.RejectPlaintextConfigSecrets(path, b)`（`pkg/config/secrets.go:26`）——`appcfg.Load` 也会拒绝含明文密钥的配置，所以写进去之前必须先检查。
- Claude Code 的 `.lsp.json`（规范 §3.13）：顶层是「服务器名 → 定义」，字段 `command`、`extensionToLanguage`（必填）、`args`、`transport`、`env`、`initializationOptions`、`settings`、`workspaceFolder`、`startupTimeout`、`shutdownTimeout`（毫秒）、`restartOnCrash`、`maxRestarts`、`diagnostics`；插件也可以在 `.claude-plugin/plugin.json` 的 `lspServers` 里内联同样的对象，或给出指向 JSON 文件的相对路径字符串。

## 范围

**新建：** `pkg/migrate/lsp.go`、`pkg/migrate/lsp_test.go`。

**修改：** `pkg/migrate/claude.go`（`claudeData` 加字段、发现时填充）、`plan.go`（类别、`Report`、`Plan`、两个 `Text()`、`reportToPlan`）、`source.go`（`Only` 注释与 `Progress.Stage` 注释）、`doc.go`；`plan_test.go`（预览与报告文本）；`pkg/architecture/testdata/graph.json`。

**不要碰：** `pkg/lsp`、Codex 路径的导入逻辑（Codex 没有 LSP；Codex 的类别列表也不加 `lsp`）。

## 步骤

### 步骤 1：发现（`lsp.go` + `claude.go`）

```go
// claudeLSPPlugin is one enabled Claude Code plugin that declares language
// servers.
type claudeLSPPlugin struct {
	Key     string                       // "<name>@<marketplace>"
	Name    string
	Root    string                       // absolute version directory: ${CLAUDE_PLUGIN_ROOT}
	Servers map[string]claudeLSPServer   // from .lsp.json and plugin.json lspServers
	Err     string                       // why its declaration could not be read
}

// claudeLSPServer mirrors one .lsp.json entry.
type claudeLSPServer struct {
	Command               string            `json:"command"`
	Args                  []string          `json:"args"`
	Transport             string            `json:"transport"`
	Env                   map[string]string `json:"env"`
	ExtensionToLanguage   map[string]string `json:"extensionToLanguage"`
	InitializationOptions json.RawMessage   `json:"initializationOptions"`
	Settings              json.RawMessage   `json:"settings"`
	WorkspaceFolder       string            `json:"workspaceFolder"`
	StartupTimeout        int               `json:"startupTimeout"`  // ms
	ShutdownTimeout       int               `json:"shutdownTimeout"` // ms
	RestartOnCrash        *bool             `json:"restartOnCrash"`
	MaxRestarts           *int              `json:"maxRestarts"`
	Diagnostics           *bool             `json:"diagnostics"`
}

func discoverLSPPlugins(root string) []claudeLSPPlugin
```

- 与 `discoverPluginSkills` 读同一个 `settings.json` 的 `enabledPlugins`（抽出一个 `enabledClaudePlugins(root) map[string]bool` 供两者共用）。
- 对每个启用的插件，定位最高版本目录；依次读取 `<dir>/.lsp.json` 与 `<dir>/.claude-plugin/plugin.json` 的 `lspServers`（对象：直接解析；字符串：相对 `<dir>` 读取该 JSON 文件；路径逃出 `<dir>` 时忽略并在 `Err` 说明）。同名服务器以 `.lsp.json` 为准。
- 没有任何服务器声明的插件不返回（技能插件等）。
- 结果按 `Key` 排序；`claudeData` 加 `LSPPlugins []claudeLSPPlugin`，在 `discoverClaude` 中 `data.PluginSkills = …` 之后填充。

### 步骤 2：映射与转换（`lsp.go`）

官方插件映射（常量表，逐字）：

```go
// officialLSPMarketplace is the marketplace Claude Code's own plugins come from.
const officialLSPMarketplace = "claude-plugins-official"

// officialLSPPlugins maps Claude Code's official LSP plugins onto forebrain's
// built-in catalog ids. An empty id means forebrain's catalog has no such
// server yet; the plugin's own declaration is imported as a custom entry.
var officialLSPPlugins = map[string]string{
	"clangd-lsp":        "clangd",
	"csharp-lsp":        "csharp-ls",
	"gopls-lsp":         "gopls",
	"jdtls-lsp":         "jdtls",
	"kotlin-lsp":        "kotlin-lsp",
	"php-lsp":           "intelephense",
	"pyright-lsp":       "pyright",
	"rust-analyzer-lsp": "rust-analyzer",
	"swift-lsp":         "sourcekit-lsp",
	"typescript-lsp":    "typescript-language-server",
	"lua-lsp":           "", // task 17 adds lua-language-server
	"ruby-lsp":          "", // task 17 adds ruby-lsp
	"liquid-lsp":        "",
}
```

> 执行前核对：在 Claude Code 官方文档（plugins / code intelligence 页面）确认官方市场名与这 13 个插件名。不一致时按文档改常量，并在 PR 描述里写明依据。

每个插件的处理（产生若干 `LSPOutcome{Name, Status, Detail}`，`Status` 为 `enabled` | `written` | `skipped`）：

1. `marketplace == officialLSPMarketplace` 且映射到非空 id：只写 `lsp.servers.<id>: {enabled: true}`，**不复制字段**；`cfg.LSP.Servers` 已有该 id → `skipped`，Detail `an entry with this id is already configured; not overwritten`；否则 `enabled`，Detail `built-in <id> server enabled (from <plugin key>)`。
2. 其他情况（非官方插件，或官方插件映射为空）：对插件的每个服务器转成自定义条目：
   - id：`.lsp.json` 里的服务器名转小写，非 `[a-z0-9._-]` 的字符替换为 `-`，去掉首尾的 `-`；结果为空时用插件名。
   - 跳过条件（`skipped`，Detail 逐字）：`transport` 非空且不是 `stdio` → `only stdio language servers are supported`；`command`、`args`、`env` 值、`workspaceFolder` 中出现 `${CLAUDE_PROJECT_DIR}` → `uses ${CLAUDE_PROJECT_DIR}, which forebrain does not provide`；出现 `${user_config.` → `uses plugin user settings, which forebrain does not provide`；`command` 为空或 `extensionToLanguage` 为空 → `the declaration has no command or extensionToLanguage`；id 已在 `cfg.LSP.Servers` 中 → `an entry with this id is already configured; not overwritten`。
   - 字段转换（规范 §5.5）：`${CLAUDE_PLUGIN_ROOT}` 在 `command`、`args`、`env` 值、`workspaceFolder` 中替换为插件版本目录的绝对路径；`extensionToLanguage` → `ExtensionToLanguage`（键转小写）；`initializationOptions`/`settings` → `LSPJSONObject`（原 JSON）；`startupTimeout`/`shutdownTimeout` 毫秒 ÷ 1000 向上取整；`restartOnCrash`、`maxRestarts`、`diagnostics` 原样；`Enabled: appcfg.BoolPtr(true)`。
   - 写入前校验：`appcfg.ValidateLSPServers("lsp.servers", map[string]appcfg.LSPServerConfig{id: entry})` 失败 → `skipped`，Detail 为错误文本；把条目 `yaml.Marshal` 后交给 `appcfg.RejectPlaintextConfigSecrets("forebrain.yaml", b)`，失败 → `skipped`，Detail `its env holds a plaintext secret; add it by hand with ${ENV_NAME} from ~/.forebrain/.env`。
   - 通过 → `written`，Detail `<command> for <扩展名，逗号分隔> (from <plugin key>)`（不打印 env 值）。
3. 插件 `Err` 非空 → 一条 `skipped`，Name 为插件 key，Detail 为 `Err`。

```go
// LSPOutcome reports one language-server entry of an import.
type LSPOutcome struct {
	Name   string
	Status string // enabled | written | skipped
	Detail string
}

// importLSP applies the enabled plugins' language servers to forebrain.yaml.
func importLSP(data *claudeData, opts *Options, progress func(Progress)) ([]LSPOutcome, []string, error)
```

`importLSP`：`!opts.wants("lsp")` → 空；`progress(Progress{Stage: "lsp", Detail: "language servers"})`；`appcfg.Load(opts.ConfigPath)`；按上面的规则修改 `cfg.LSP.Servers`（为 nil 时先建 map）；有改动时：dry run 只加 notice，否则 `backupFile` + `appcfg.Save`；notice（逐字）：`enabled language servers apply to edits from the next session; run forebrain lsp doctor to check them`。

### 步骤 3：接入报告（`plan.go`、`source.go`）

- Claude 的类别列表加 `"lsp"`（放在 `"mcp"` 之后）；在 `opts.wants("mcp")` 块之后加 `opts.wants("lsp")` 块：`report.LSP, notices, err = importLSP(...)`，错误处理照 MCP 块（错误写进 `report.Notices`，继续）。
- `Report` 加 `LSP []LSPOutcome`；`Plan` 加 `LSPEnabled, LSPWritten int`；`reportToPlan` 计数（`enabled` 与 `written`）。
- `Plan.Text()` 在 MCP 行之后加 `  Language servers: %d enabled · %d added\n`（两者都为 0 时不输出这一行）。
- `Report.Text()`：计数行（MCP 行之后）`  Language servers: %d enabled · %d added · %d skipped`（`len(r.LSP) > 0` 时）；明细段（MCP 段之后）`\nLanguage servers\n` + 每条 `"  " + outcomeLine(name, status, detail)`。
- `source.go`：`Only` 注释加 `"lsp"`；`Progress.Stage` 注释加 `"lsp"`。
- `doc.go`：加一句 `lsp.go imports the language servers of enabled Claude Code plugins into forebrain.yaml.`

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/migrate -count=1` → `ok`。

### 步骤 4：全量检查

**验证**：`gofmt`、`go vet ./...` 干净；`scripts/package-graph.sh` 后提交 `graph.json`（`migrate` 的 fan-out 仍为 8）；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` → 全部 `ok`。

## 测试计划

`pkg/migrate/lsp_test.go`，每个测试在 `t.TempDir()` 里造一个 Claude 根目录（`settings.json`、`plugins/cache/<marketplace>/<name>/<version>/…`）和一个 `forebrain.yaml`：

- `TestDiscoverLSPPlugins`：只返回启用且声明了服务器的插件；取最高版本目录；`.lsp.json` 与 `plugin.json` 内联、字符串路径三种形式；字符串路径逃出目录时 `Err` 非空。
- `TestImportOfficialPluginEnablesCatalogServer`：`gopls-lsp@claude-plugins-official` → `lsp.servers.gopls.enabled: true` 且没有 `command` 字段；报告 `enabled`。
- `TestImportCustomPlugin`：非官方插件 → 自定义条目，`${CLAUDE_PLUGIN_ROOT}` 被替换，`startupTimeout: 1500` → `startup_timeout: 2`，`Enabled` 为 true。
- `TestImportSkips`：已存在的 id、非 stdio、`${CLAUDE_PROJECT_DIR}`、`${user_config.x}`、缺 `extensionToLanguage`、env 含明文密钥（如 `API_TOKEN: abc123`）→ 各自的 `skipped` 文本逐字；文件未被改写（这些都跳过时）。
- `TestImportLSPDryRunWritesNothing`：dry run 后 `forebrain.yaml` 字节不变；计数与真实运行一致。
- `TestImportLSPBacksUpConfig`：真实运行生成备份文件（照 MCP 的备份测试）。
- `TestImportLSPRespectsOnly`：`Only: ["mcp"]` → 不处理 LSP，`SkippedCategories` 含 `lsp`。
- `pkg/migrate/plan_test.go`：预览文本含 `Language servers: 1 enabled · 1 added`；报告文本含 `Language servers` 明细段；没有 LSP 插件时两处都不出现。

## 完成判据

- [ ] 上述测试全部通过
- [ ] `grep -n '"lsp"' pkg/migrate/plan.go` 只出现在 Claude 的类别列表与 `wants` 判断里（Codex 列表没有）
- [ ] `migrate` 的 fan-out 仍为 8；`graph.json` 已提交；全量测试通过
- [ ] README 中任务 16 状态为 `DONE`

## STOP 条件

- 需要让 `pkg/migrate` 导入任何新包。
- 官方文档里的市场名或插件名与映射表不符，且无法确定正确值。
- 需要把 env 的明文值写进 `forebrain.yaml`。

## 维护说明

- 任务 17 给目录加入 `lua-language-server`、`ruby-lsp` 后，把映射表里对应的空字符串改成这两个 id（任务 17 的步骤里已列出）。
- Claude Code 增加新的官方 LSP 插件时在映射表加一行；目录没有对应服务器时先填空字符串。
- 评审重点：报告与预览不打印 env 值；跳过的条目不修改配置。
