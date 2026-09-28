# First Setup 自动进入 Onboarding 修复计划

> 状态：已实施并真机验收通过（2026-09-28）。四个验收场景证据见 §9。
>
> 日期：2026-09-28
>
> 范围：`forebrain`（交互式 TUI）与 `forebrain gateway start` / `forebrain resume` 共享的 first-setup 判定
>
> 基线：仓库 git 尚无 commit（`git rev-parse HEAD` 失败），基线为当前工作区；drift 检查以本文所引 `file:line` 对照为准

## 1. 目标（Why this matters)

全新 home（如 `FOREBRAIN_HOME=~/.forebrain_01 forebrain`）首次启动必须自动进入 onboarding 向导；用户完成后自动、无缝进入 TUI。当前现实相反：启动直接报 `startup config incomplete: main agent LLM is not fully configured; ...` 并退出，且**每次重试都同样报错**（死胡同，用户无法自救）。

根因不是"配置不完整"本身，而是 first-setup 判定用了错误的信号，导致 onboarding 永远不会被触发。

## 2. 根因（已核实的证据链）

1. `pkg/home/seed.go:56-84` `seedWorkspace`：`home.Ensure()` 在 config 文件不存在时，向全新 home 写入**骨架 `forebrain.yaml`**（`provider: ""`、`model: ""`，见同文件 `defaultForebrainYAML`，L86-115）并生成 `.env` 网关 token。即：全新 home 第一次被任何 `process.Resolve()` 触碰，就会落一个"存在但不完整"的 config。
2. `cmd/forebrain/interactive.go:77-97` 启动顺序：`interactiveEnsureProjectMCPConsent`（L77）**先于** `interactiveNeedsFirstSetup`（L82）。consent 链路 `cmd/forebrain/mcp_consent.go:37` → `process.ActiveAgentWorkspace()`（`pkg/process/open.go:479-480`）→ `process.Resolve()`（`pkg/process/runtime.go:82` `homepkg.Ensure(home)`）→ 播种骨架。workspace 信任在 L67 刚建立，`TrustedRoot(launch)` 非空，此路径必然走到 Resolve。
3. `pkg/tui/setup.go:1548-1561` `NeedsFirstSetup`/`NeedsFirstSetupWithConfigPath` **仅以 config 文件是否存在**判定：文件在 → 不需要 first setup。骨架文件已落 → 恒为 false → onboarding 被跳过。
4. L97 `interactiveStartupConfigError` → `StartupConfigError`（`pkg/tui/setup.go:1565-1583`）→ `MainAgentLLMMissingFieldsWithConfig` 非空 → 硬报错、退出码 1。之后每次启动重复同一判定 → 永远报错。
5. 现场证据（用户机器 `~/.forebrain_01/`）：播种骨架 yaml（provider/model 空，mtime 09-27 21:31）、`logs/error.log` 内同款报错、`state/workspace_trust.json`。
6. `forebrain gateway start`（`cmd/forebrain/serve.go:31-50`）的 needs 检查位于任何 Resolve 之前、未被播种污染；但"已播种的不完整 config"同样落入报错分支，同样应修。

关键语义事实：**"是否已完成 first setup"的正确判据早已存在**——
- onboarding 自身的门槛 `mainLLMConfigured`（`pkg/tui/setup.go:286-291`）= `appcfg.ValidateMainAgentLLMConfigured(cfg).Complete()`（`pkg/config/agent_llm.go:479-520`；chatgpt provider 免 api_key）；
- `StartupConfigError` 用同一 validation 的 `MissingFields()`。

三处共享一个语义，唯独 first-setup 判定用"文件存在"这个在播种引入后必然失真的信号。这是根因；修判定，不是修播种、也不是调启动顺序（调整顺序后，用户一旦在播种后中断 onboard，下次仍落入死胡同）。

## 3. 修复设计

`NeedsFirstSetup` 的判据改为与 onboarding 门槛、StartupConfigError 完全一致的主 agent LLM 完整性：

```go
// NeedsFirstSetup reports whether this home still has to be set up.
//
// The criterion is the main agent's LLM configuration, not the config file's
// existence: home.Ensure seeds an incomplete forebrain.yaml (empty provider
// and model) the first time anything resolves the runtime — inside this very
// launch, before the first-setup check, the project-MCP consent already does.
// File existence therefore cannot tell a fresh home from a configured one;
// the LLM fields can, and they are the same fields onboarding itself and
// StartupConfigError judge by.
func NeedsFirstSetup() (bool, error) {
	rt, err := process.Resolve()
	if err != nil {
		return false, err
	}
	return !mainLLMConfigured(&rt.Config), nil
}
```

- 删除 `NeedsFirstSetupWithConfigPath`（唯一调用者就是 `NeedsFirstSetup`，无测试直接引用）。
- `process.Resolve()` 容忍 config 文件不存在（`pkg/process/runtime.go:91-97` 置空 `Root{}`）→ `Complete()`=false → needs=true，天然覆盖"无 config"旧分支。
- config 非法（YAML 解析失败）：`Resolve` 返回错误 → `NeedsFirstSetup` 透传错误 → 启动失败并显示同一解析错误（旧行为：needs=false → `StartupConfigError` → 同一错误，用户可见结果等价）。

### 行为变化矩阵（刻意修订）

| 场景 | 旧 | 新 |
|---|---|---|
| 全新 home，无 config | onboard（gateway 路径）/ 跳过报错（interactive 路径，本 bug） | onboard |
| 全新 home，已被播种骨架（本 bug；含用户中断 onboard 后的再次启动） | 硬报错死胡同 | onboard |
| 完整 config | 直达 TUI/gateway | 不变 |
| 手写/拷贝的不完整 config（含 `${ENV}` 不可解析展开为空） | TTY 内也硬报错 | TTY 内进 onboard（Esc/Ctrl+C = `ErrCancelled` 干净退出；补好 `.env` 后下次直达）；非 TTY `gateway start` 仍走 `StartupConfigError` 报错不变 |
| onboard 中途取消 | ErrCancelled 干净退出 | 不变；因判定已改，下次启动会再次 onboard（不再死胡同） |

不做的事（out of scope）：不改播种内容；不改启动顺序（trust → MCP consent → onboard → TUI 维持现状）；不动 onboard 向导页面；不动 gateway wiring。

## 4. Scope

**In scope：**
- `pkg/tui/setup.go` — 重写 `NeedsFirstSetup`，删 `NeedsFirstSetupWithConfigPath`
- `pkg/tui/setup_test.go` — 更新两条受契约修订影响的断言 + 新增回归测试

**Out of scope（看起来相关，明确不碰）：**
- `pkg/home/seed.go` 的播种逻辑（骨架是 onboarding 前的合法占位，问题在判定不在播种）
- `cmd/forebrain/interactive.go` / `serve.go` 的启动顺序与 wiring（其单测全部桩掉谓词，不受影响）
- `pkg/process/*`、`pkg/config/agent_llm.go`（语义三处已一致，无需改动）

## 5. Steps

### Step 1：重写判定

`pkg/tui/setup.go` 以 §3 的实现替换 L1548-1561 的 `NeedsFirstSetup` + `NeedsFirstSetupWithConfigPath`（两个函数合一）。

**Verify**：`CGO_ENABLED=1 go build ./...` → exit 0

### Step 2：更新受影响断言 + 新增回归测试

`pkg/tui/setup_test.go`：
1. `TestMainAgentLLMConfiguredFalseWhenMainLLMFieldsMissing`（L127-133）：断言改为 `needs == true`（不完整 config 即未完成 first setup），错误信息同步改写。
2. `TestStartupConfigErrorReportsMissingDotEnvForCopiedConfig`（L35-41）：断言改为 `needs == true`（`${OPENAI_API_KEY}` 展开为空 → api_key 缺失 → 未完成 first setup；`StartupConfigError` 诊断断言保留）。
3. 新增 `TestNeedsFirstSetupTrueAfterHomeEnsureSeedsSkeleton`：临时 `FOREBRAIN_HOME` → `process.ResetResolve()` → `process.Resolve()`（触发 `home.Ensure` 播种）→ 断言骨架 `forebrain.yaml` 已存在且 `NeedsFirstSetup()` 返回 `(true, nil)`。**这是本 bug 的直接回归测试。**
4. 既有 `TestMainAgentLLMConfiguredTrueWhenMainLLMFullyConfigured`（L96-102，完整 config → needs=false）保持通过。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'NeedsFirstSetup|MainAgentLLMConfigured|StartupConfigError' -count=1` → all pass

### Step 3：包级回归

**Verify**：
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/config ./pkg/process ./cmd/forebrain -count=1` → all pass
- `go vet ./...` → exit 0

### Step 4：真机验收（pty 驱动）

`go build -o ./build/bin/forebrain ./cmd/forebrain` 后用 tmux 起真终端，隔离 `FOREBRAIN_HOME`：

1. **fresh home 首启**：信任页选 "Trust and continue" → 自动进入 onboarding 向导（provider/model/api key/base URL/review/channels）→ Finish → **同一进程内自动出现 TUI 横幅（无报错、无重启）**。
2. **二次启动**：同一直接出现 TUI（不再 onboard）。
3. **中断自救**：全新 home，向导中 Esc/Ctrl+C → 干净退出（无栈、无残缺输出）；再次启动重新进入向导（不再死胡同报错）。
4. **非 TTY 回归**：`FOREBRAIN_HOME=新目录 forebrain gateway start < /dev/null` → 仍输出 `StartupConfigError` 类报错（不进入向导、不挂起）。

## 6. Done criteria

- [x] `pkg/tui/setup.go` 中 `NeedsFirstSetupWithConfigPath` 不复存在：`rg -n "NeedsFirstSetupWithConfigPath" --type go` 无匹配
- [x] Step 2/3 全部测试命令 exit 0，含新增回归测试
- [x] Step 4 四个场景全部按预期（tmux capture-pane 证据，§9）
- [x] 改动仅限 in-scope 两文件 + 本计划文件状态行（仓库无 commit 基线，以会话记录核对）
- [x] 全新 home 的一次启动内完成 onboard→TUI 全流程（用户核心诉求）

## 9. 验收记录（2026-09-28，tmux 120×42 pty，`-tags fts5` 构建）

1. **fresh home 首启**（/tmp/fb_acc2）：Trust → 向导 Step 1–5（Z.AI → api key → glm-4.5 → medium → Save → Finish）→ 同进程直接渲染 TUI 横幅（`zhipuai/glm-4.5 · medium` footer），无报错无重启。
2. **二次启动**（/tmp/fb_acc1）：直接 TUI 横幅，不再 onboard。
3. **中断自救**（/tmp/fb_acc3）：向导 Step 1 按 Esc → EXIT_CODE=0 干净退出（home 留播种骨架，即 bug 前置状态）；再次启动直接重回向导 Step 1（旧版此处死胡同报错）。
4. **非 TTY 回归**：`FOREBRAIN_HOME=<播种骨架 home> forebrain gateway start </dev/null` → exit 1，输出 `startup config incomplete: ...` 诊断（行为不变）。

验收备注：首次验收时误用无 `-tags fts5` 的构建，TUI 起不来（`no such module: fts5`）——为验收工具链错误，非产品缺陷；按 Makefile `GOFLAGS := -trimpath -tags fts5` 重打后全过。

## 7. STOP conditions

- §2 所引 `file:line` 与实际代码不符（仓库已 drift）。
- 改动看似必须触及 out-of-scope 文件才能修好。
- 真机验收中 onboard 完成后 `interactiveStartupConfigError` 仍报错（说明向导写盘与判定语义不一致，需停下来重新核对 `process.Execute` 落盘字段，而非放宽判定）。
- 新判定导致 `cmd/forebrain` 或 `pkg/gateway` 中未预期的调用方行为变化（先核实调用面再动）。

## 8. Maintenance notes

- 未来任何"判定是否需要 setup"的新调用方必须复用 `NeedsFirstSetup`（语义单一来源）；不要再引入"文件存在性"判据。
- 若 onboarding 向导新增可跳过步骤（如 channels 全可选），`Complete()` 的字段集与向导必答项需同步审阅（`pkg/config/agent_llm.go`）。
- 播种骨架（`defaultForebrainYAML`）若未来写入非空 provider/model，本修复语义不受影响，但回归测试 Step 2.3 依赖"骨架不完整"这一事实，届时需同步调整。
