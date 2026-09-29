# 任务 01：新增 `lsp` 配置段与 `features.lsp`

> **执行者须知**：逐步执行；每一步运行「验证」命令并确认结果后再进入下一步。出现「STOP 条件」中的任何情况立即停止并汇报，不要自行发挥。完成后把 `docs/plan/lsp/README.md` 中本任务状态改为 `DONE`。
>
> **规范**：`docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md`（下称「规范」）。开始前读规范 §2（术语）、§5.1（全局配置与数值约束）。
>
> **漂移检查（先运行）**：`git diff --stat 6305ea9..HEAD -- pkg/config/config.go pkg/config/load.go pkg/config/agent_llm.go`。有输出时，先把下文「现状」摘录与实际代码逐段对比，对不上即 STOP。

## 状态

- 优先级：P0 · 工作量：S · 风险：LOW
- 依赖：无
- 类别：direction（新功能的配置底座）
- 计划基于：`6305ea9`，2026-09-29

## 为什么

后续所有 LSP 任务都从 `forebrain.yaml` 读配置。本任务只加类型、默认值、校验和序列化，不改任何运行时行为，因此可以最先、最安全地合入。关键点：`lsp.diagnostics.wait_ms` 要区分「没写」（默认 2500）和「显式写 0」（不等待），所以必须是 `*int`。

## 现状

- `pkg/config/config.go:113-134` —— `Root` 结构体，各配置段按声明顺序排列：

  ```go
  type Root struct {
  	SourceFiles           []string              `yaml:"-" json:"-"`
  	ApprovalPolicy        ApprovalPolicyConfig  `yaml:"approval_policy,omitempty" json:"approval_policy,omitempty"`
  	...
  	Tools                 ToolsSection          `yaml:"tools,omitempty" json:"tools,omitempty"`
  	Features              FeaturesSection       `yaml:"features,omitempty" json:"features,omitempty"`
  	Windows               WindowsSection        `yaml:"windows,omitempty" json:"windows,omitempty"`
  	Credentials           CredentialsSection    `yaml:"credentials,omitempty" json:"credentials,omitempty"`
  }
  ```

- `pkg/config/config.go:148-177` —— 特性开关与默认值：

  ```go
  type FeaturesSection struct {
  	ExecPermissionApprovals *bool                     `yaml:"exec_permission_approvals,omitempty" json:"exec_permission_approvals,omitempty"`
  	RequestPermissionsTool  *bool                     `yaml:"request_permissions_tool,omitempty" json:"request_permissions_tool,omitempty"`
  	Memories                *bool                     `yaml:"memories,omitempty" json:"memories,omitempty"`
  	SkillOffer              *bool                     `yaml:"skill_offer,omitempty" json:"skill_offer,omitempty"`
  	NetworkProxy            NetworkProxyFeatureConfig `yaml:"network_proxy,omitempty" json:"network_proxy,omitempty"`
  }

  func (r *Root) EffectiveFeatures() EffectiveFeaturesConfig {
  	...
  		SkillOffer:              boolValue(f.SkillOffer, true),
  	}
  }
  ```

- `pkg/config/load.go:97-144` —— `validateLoadedRoot` 依次调用各段的校验函数，第一个错误即返回（例如 `validateMCPToolApprovalModes(r)`）。
- `pkg/config/load.go:448-490` —— `normalize(r *Root)`：加载后规范化（去空白、小写）。
- `pkg/config/load.go:577-601` —— `materializeOptionalDefaults`：`Save` 前把可选设置的**生效值**写回，保证落盘的 YAML 写明实际默认值，例如 `r.Features.Memories = BoolPtr(features.Memories)`。
- `pkg/config/agent_llm.go:296-410` —— `LLMRequestParams []byte`：YAML 里写对象、内存里存 JSON 字节的范例，含 `MarshalJSON`、`UnmarshalJSON`、`MarshalYAML`、`UnmarshalYAML`，以及辅助函数 `toJSONCompatibleValue`（:387）与 `toYAMLCompatibleMap`（:412）。`initialization_options`、`settings` 照它实现。
- 测试范例：`pkg/config/config_test.go:1123 TestEffectiveFeaturesSkillOfferDefaultsOn`（特性默认值）、`:402 TestLLMRequestParamsYAMLAndErrors`（YAML ⇄ JSON 字节）、`:229 TestLoadAndSaveJSONYAML`（加载/保存往返）。

## 范围

**只修改 / 新建：**

- `pkg/architecture/testdata/graph.json`（由 `scripts/package-graph.sh` 重新生成）
- `pkg/config/lsp.go`（新建）
- `pkg/config/lsp_test.go`（新建）
- `pkg/config/config.go`（`Root` 加字段；`FeaturesSection`、`EffectiveFeatures`、`EffectiveFeaturesConfig` 加 `LSP`）
- `pkg/config/load.go`（`validateLoadedRoot` 调用新校验；`normalize` 调用新规范化；`materializeOptionalDefaults` 写回新默认值）
- 现有测试中**仅因** `Save` 输出多了 `features.lsp` / `lsp` 键而失败的期望值

**不要碰：**

- `pkg/lsp`、`pkg/process` 等任何其他包——本任务不接入运行时。
- `pkg/config/marshal.go`——`Root.MarshalYAML` 用反射遍历字段，新字段自动输出，不需要改。

## 步骤

### 步骤 1：新建 `pkg/config/lsp.go`，定义类型

写入下列类型（字段名、yaml/json tag 必须完全一致；注释可按仓库风格补充）：

```go
// Language-server configuration: the lsp section of forebrain.yaml and the
// resolved values the runtime reads. See docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md §5.
package config

import "time"

type LSPSection struct {
	Recommendations *bool                      `yaml:"recommendations,omitempty" json:"recommendations,omitempty"`
	MaxServers      int                        `yaml:"max_servers,omitempty" json:"max_servers,omitempty"`
	IdleTimeout     int                        `yaml:"idle_timeout,omitempty" json:"idle_timeout,omitempty"`
	RequestTimeout  int                        `yaml:"request_timeout,omitempty" json:"request_timeout,omitempty"`
	Diagnostics     LSPDiagnosticsConfig       `yaml:"diagnostics,omitempty" json:"diagnostics,omitempty"`
	Servers         map[string]LSPServerConfig `yaml:"servers,omitempty" json:"servers,omitempty"`
}

type LSPDiagnosticsConfig struct {
	AfterEdit    *bool  `yaml:"after_edit,omitempty" json:"after_edit,omitempty"`
	WaitMS       *int   `yaml:"wait_ms,omitempty" json:"wait_ms,omitempty"`
	MinSeverity  string `yaml:"min_severity,omitempty" json:"min_severity,omitempty"`
	MaxPerFile   int    `yaml:"max_per_file,omitempty" json:"max_per_file,omitempty"`
	MaxFiles     int    `yaml:"max_files,omitempty" json:"max_files,omitempty"`
	LateDelivery *bool  `yaml:"late_delivery,omitempty" json:"late_delivery,omitempty"`
}

type LSPServerConfig struct {
	Enabled               *bool             `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Command               string            `yaml:"command,omitempty" json:"command,omitempty"`
	Args                  []string          `yaml:"args,omitempty" json:"args,omitempty"`
	ExtensionToLanguage   map[string]string `yaml:"extension_to_language,omitempty" json:"extension_to_language,omitempty"`
	Filenames             map[string]string `yaml:"filenames,omitempty" json:"filenames,omitempty"`
	RootMarkers           []string          `yaml:"root_markers,omitempty" json:"root_markers,omitempty"`
	WorkspaceFolder       string            `yaml:"workspace_folder,omitempty" json:"workspace_folder,omitempty"`
	Env                   map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	EnvPassthrough        []string          `yaml:"env_passthrough,omitempty" json:"env_passthrough,omitempty"`
	InitializationOptions LSPJSONObject     `yaml:"initialization_options,omitempty" json:"initialization_options,omitempty"`
	Settings              LSPJSONObject     `yaml:"settings,omitempty" json:"settings,omitempty"`
	StartupTimeout        int               `yaml:"startup_timeout,omitempty" json:"startup_timeout,omitempty"`
	ShutdownTimeout       int               `yaml:"shutdown_timeout,omitempty" json:"shutdown_timeout,omitempty"`
	RestartOnCrash        *bool             `yaml:"restart_on_crash,omitempty" json:"restart_on_crash,omitempty"`
	MaxRestarts           int               `yaml:"max_restarts,omitempty" json:"max_restarts,omitempty"`
	Diagnostics           *bool             `yaml:"diagnostics,omitempty" json:"diagnostics,omitempty"`
	Role                  string            `yaml:"role,omitempty" json:"role,omitempty"`
	Priority              *int              `yaml:"priority,omitempty" json:"priority,omitempty"`
	Prewarm               bool              `yaml:"prewarm,omitempty" json:"prewarm,omitempty"`
}

// LSPJSONObject is a JSON object written as YAML in forebrain.yaml and kept
// as JSON bytes in memory, the same contract as LLMRequestParams.
type LSPJSONObject []byte

const (
	LSPRolePrimary     = "primary"
	LSPRoleDiagnostics = "diagnostics"
)

// EffectiveLSP is the lsp section with every default applied.
type EffectiveLSP struct {
	Recommendations bool
	MaxServers      int
	IdleTimeout     time.Duration
	RequestTimeout  time.Duration
	AfterEdit       bool
	Wait            time.Duration
	MinSeverity     string // "error" | "warning" | "information" | "hint"
	MaxPerFile      int
	MaxFiles        int
	LateDelivery    bool
}
```

然后实现：

1. `LSPJSONObject` 的四个方法 `MarshalJSON`、`UnmarshalJSON`、`MarshalYAML`、`UnmarshalYAML`，逐行照抄 `LLMRequestParams` 的实现（`agent_llm.go:302-385`），只改两处：类型名；错误文案中的 `llm params` 改为 `lsp object`。同样必须拒绝非对象（数组、数字、非 JSON 字符串），`null` 解码为 `nil`。复用同包的 `toJSONCompatibleValue`、`toYAMLCompatibleMap`，不要复制它们。
2. `func (r *Root) EffectiveLSP() EffectiveLSP`：`r` 为 nil 时也要返回默认值。默认值：`Recommendations` true；`MaxServers` 6；`IdleTimeout` 600 秒；`RequestTimeout` 30 秒；`AfterEdit` true；`Wait`：`WaitMS == nil` 时 2500 毫秒，否则 `*WaitMS` 毫秒（可以是 0）；`MinSeverity` 空时 `"warning"`；`MaxPerFile` 0 时 20；`MaxFiles` 0 时 10；`LateDelivery` true。
3. `func normalizeLSPSection(s *LSPSection)`：对每个服务器——`Command`、`WorkspaceFolder`、`Role` 去首尾空白，`Role` 转小写；`ExtensionToLanguage` 的键去空白并转小写（值只去空白）；`MinSeverity` 去空白并转小写。map 的键（服务器 id）去首尾空白并转小写（重建 map）。
4. `func ValidateLSPServers(source string, servers map[string]LSPServerConfig) error`（导出，任务 15 用它校验项目文件），规则：
   - 服务器 id 匹配 `^[a-z0-9][a-z0-9._-]{0,63}$`，否则 `fmt.Errorf("%s.%s: server id must match [a-z0-9][a-z0-9._-]{0,63}", source, id)`。
   - `Command` 不得包含 `\n`、`\r`、`\x00`。
   - `ExtensionToLanguage` 的每个键必须以 `.` 开头且长度 ≥ 2；值非空。
   - `Filenames` 的键不得包含 `/` 或 `\`；值非空。
   - `Role` 只能是 `""`、`primary`、`diagnostics`。
   - `StartupTimeout` 0–600；`ShutdownTimeout` 0–60；`MaxRestarts` 0–20。
   - 错误文案格式统一为 `"%s.%s.<字段 yaml 名> <问题>"`，例如 `lsp.servers.gopls.startup_timeout must be between 0 and 600`。
5. `func validateLSPSection(r *Root) error`：数值约束按规范 §5.1——`max_servers` 0 或 1–32；`idle_timeout` 0 或 60–86400；`request_timeout` 0 或 1–600；`wait_ms`（非 nil 时）0–30000；`min_severity` 为空或四个值之一；`max_per_file` 0 或 1–200；`max_files` 0 或 1–50；最后调用 `ValidateLSPServers("lsp.servers", r.LSP.Servers)`。

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/config` → 退出码 0（此时 `Root` 还没有 `LSP` 字段，若编译报 `r.LSP` 未定义，先做步骤 2 再回来验证）。

### 步骤 2：把新段接入 `Root` 与特性开关

- `config.go`：在 `Root` 中 `Features` 字段**之后**加 `LSP LSPSection \`yaml:"lsp,omitempty" json:"lsp,omitempty"\``。
- `FeaturesSection` 在 `SkillOffer` 之后加 `LSP *bool \`yaml:"lsp,omitempty" json:"lsp,omitempty"\``。
- `EffectiveFeaturesConfig` 加 `LSP bool`；`EffectiveFeatures()` 里 `LSP: boolValue(f.LSP, true)`。
- `load.go`：`validateLoadedRoot` 在 `validateMCPToolApprovalModes` 之后调用 `validateLSPSection(r)`；`normalize` 末尾调用 `normalizeLSPSection(&r.LSP)`；`materializeOptionalDefaults` 加：

  ```go
  r.Features.LSP = BoolPtr(features.LSP)
  lsp := r.EffectiveLSP()
  r.LSP.Recommendations = BoolPtr(lsp.Recommendations)
  r.LSP.Diagnostics.AfterEdit = BoolPtr(lsp.AfterEdit)
  r.LSP.Diagnostics.LateDelivery = BoolPtr(lsp.LateDelivery)
  waitMS := int(lsp.Wait / time.Millisecond)
  r.LSP.Diagnostics.WaitMS = &waitMS
  ```

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./...` → 退出码 0；`go vet ./pkg/config` → 退出码 0。

### 步骤 3：写测试 `pkg/config/lsp_test.go`

按「测试计划」一节写全部用例。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/config -run 'LSP' -count=1 -v` → 全部 PASS，至少 8 个 `--- PASS`。

### 步骤 4：跑全包测试，处理 `Save` 输出变化

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/config ./pkg/process ./pkg/gateway -count=1` → 全部 `ok`。

若有测试失败，且失败信息**只**是期望的 YAML/JSON 输出里缺少新写出的 `lsp:` 段或 `features.lsp` 键，按新输出更新该测试的期望值；其他失败按 STOP 处理。

### 步骤 5：格式、静态检查、全量测试

**验证**：`gofmt -l cmd pkg third_party` → 无输出；`go vet ./...` → 退出码 0；`scripts/package-graph.sh && git status --short pkg/architecture/testdata/graph.json` → ` M …graph.json`（提交它）；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` → 全部 `ok`。

## 测试计划

在 `pkg/config/lsp_test.go`（`package config`，只用标准库 `testing`，风格照 `config_test.go`）：

1. `TestEffectiveFeaturesLSPDefaultsOn`：nil Root、空 Root 为 true；显式 false 为 false（照 `:1123`）。
2. `TestEffectiveLSPDefaults`：空 Root 的 `EffectiveLSP()` 各字段等于步骤 1 列出的默认值；nil `*Root` 同样。
3. `TestEffectiveLSPWaitZeroMeansNoWait`：`WaitMS` 指向 0 时 `Wait == 0`；nil 时 2500ms。
4. `TestLSPJSONObjectYAML`：YAML 嵌套对象 → 合法 JSON 字节；YAML 数组、`'[1]'`、`'not-json'` 报错；`null` → nil；JSON 往返（照 `:402`）。
5. `TestLSPSectionLoadRoundTrip`：写一个含 `features.lsp: false`、`lsp.diagnostics.wait_ms: 0`、`lsp.servers.gopls.settings.gopls.staticcheck: true`、一个自定义服务器的 YAML 到临时目录（设置 `t.Setenv("FOREBRAIN_HOME", dir)`），`Load` → `Save` → `Load`，断言：`WaitMS` 仍为 0（没有被默认值覆盖）、`settings` 的 JSON 语义相等、自定义服务器字段完整。
6. `TestValidateLSPSectionRejects`：表驱动，每行一个非法配置与期望错误子串：`max_servers: 33`、`idle_timeout: 5`、`wait_ms: 30001`、`min_severity: fatal`、服务器 id `Go!`、扩展名键 `go`（不以 `.` 开头）、`role: helper`、`startup_timeout: 601`、`command` 含换行。
7. `TestNormalizeLSPSection`：服务器 id `GoPLS ` 规范为 `gopls`；扩展名键 `.GO` 规范为 `.go`；`role: " Diagnostics "` 规范为 `diagnostics`。
8. `TestSaveMaterializesLSPDefaults`：空配置 `Save` 后文件内容包含 `lsp:`、`wait_ms: 2500`、`recommendations: true`，`features:` 下包含 `lsp: true`。

## 完成判据

- [ ] `pkg/config/lsp.go`、`pkg/config/lsp_test.go` 存在；`grep -n 'LSP ' pkg/config/config.go` 能看到 `Root.LSP` 与 `FeaturesSection.LSP`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/config -run LSP -count=1` 通过且至少 8 个测试
- [ ] `go vet ./...` 退出码 0；`gofmt -l cmd pkg third_party` 无输出
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` 全部 `ok`
- [ ] `git status --short` 只列出「范围」中的文件
- [ ] 已运行 `scripts/package-graph.sh` 并提交更新后的 `pkg/architecture/testdata/graph.json`（`pkg/config` 行数变化会改它）
- [ ] README 中任务 01 状态为 `DONE`

## STOP 条件

- 「现状」摘录与代码对不上。
- `ls pkg/config/*.go | grep -v _test.go | wc -l` 在新建 `lsp.go` 之前不是 19（说明别人已占用名额，新建会超过 20 个文件的上限）。
- `Root.MarshalYAML` 没有自动输出新字段（说明 `marshal.go` 行为与描述不符）。
- 除「只缺新键」之外的既有测试失败。
- 需要修改「范围」之外的文件。

## 维护说明

- 任务 06 的 `lsp.ResolveServers` 读取 `LSPServerConfig`；任务 15 用 `ValidateLSPServers` 校验项目文件；任务 16 往 `Servers` 写迁移结果。改字段时同步检查这三处。
- `WaitMS` 必须保持指针类型，否则「显式 0」会退化成默认值。
- 本任务后 `pkg/config` 有 20 个生产文件（结构测试上限）。后续任务不得再在 `pkg/config` 新建生产文件；任务 15 的项目文件解析放在 `pkg/lsp/project.go`。
- 评审重点：默认值与规范 §5.1 一致；`materializeOptionalDefaults` 只写生效值，不写入服务器条目。
