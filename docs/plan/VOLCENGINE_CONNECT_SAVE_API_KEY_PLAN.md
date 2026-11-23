# 修复：/connect 火山 Coding Plan 完成选择与配置后报 `api_key required in non-interactive mode`

> **Executor instructions**: 按步骤执行；每步先跑验证命令确认预期结果再前进。
> 触发任何 STOP condition 立即停下报告，不要即兴发挥。改动留在工作区，不 commit。
>
> **Drift check（先跑）**:
> `git rev-parse HEAD` 应为 `d2281835`（或工作区等价基线）；
> `git status --short pkg/process/setup.go pkg/process/session_context_test.go pkg/tui/setup_test.go` 应为空。
> 内容与本文「Current state」摘录不符（行号漂移不算）→ STOP。

## Status

- **Priority**: P1（owner 报障的用户可见 bug）
- **Effort**: S
- **Risk**: LOW（单点删除一条重复校验 + 回归测试；行为矩阵已穷举）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `d2281835`, 2026-11-12（工作区基线，无未提交改动）
- **执行方式**: owner 裁决的 plan-gated-bugfix 流程——批准后**直接在当前源码实施**（不用 worktree、不 commit）；实施前将本计划抄录到仓库 `docs/plan/VOLCENGINE_CONNECT_SAVE_API_KEY_PLAN.md`（gitignore 内，不入库）

## Why this matters

TUI `/connect` 选「火山方舟」订阅完成设备码登录、勾选模型、贴完 Coding Plan Key 之后，最后一保存就报
`connect: failed to save config: api_key required in non-interactive mode`。签名、取 plan、写行全部成功，
唯独收尾的 `process.Execute` 把已带 key 的行拒之门外：模型行写进了表、默认模型没设、会话没切换——用户视角是
「登录成功却白登录」。同一向导也是首启 onboarding 的入口（`pkg/tui/setup.go:134`），新装机首配选火山方舟会在
同一处死胡同。

## 根因（file:line 证据链 + 复现）

用户路径与失败点：

1. `/connect` → `runConnect`（`pkg/tui/setup.go:2133`）→ `PromptMainLLMSetup` 向导 → 选火山方舟订阅 →
   设备码登录成功 → offers 页勾选 → `pickOffers`（`pkg/tui/setup.go:1302`）→ `provisionWithPastedKey`
   （:1362）→ `volcengine.Provision`（`pkg/subscription/volcengine.go:170`）：个人版贴的 key / 团队席位 key
   随 `process.UpsertLLMModel` 写入行（:210-218，`APIKey: &key`）。**这一步成功，行已带 key。**
2. 向导收尾 `options()`（`pkg/tui/setup.go:607-618`）：订阅分支**刻意不带 APIKey**（注释原话
   "the rows themselves are already written"），只带 Provider/Model/BaseURL/APIPath/SubagentAllowed。
3. `runConnect` → `process.Execute(rt, opts)`（`pkg/tui/setup.go:2170`）→ 入口校验
   **`pkg/process/setup.go:70-73`**：

   ```go
   if strings.TrimSpace(opts.APIKey) == "" && !appcfg.CredentialsBackedProvider(opts.Provider) {
       logSetupError(rt.Home, "setup.execute.validation_error", opts, "api_key required in non-interactive mode")
       return rep, fmt.Errorf("api_key required in non-interactive mode")
   }
   ```

   `CredentialsBackedProvider("volcengine")`（`pkg/config/agent_llm.go:440-451`）= **false**——这是**正确**的：
   火山的设计（plan 281）就是「行带 key」，登录不是凭据本身；registry 条目
   （`pkg/config/provider_registry.go:224-249`）刻意没有 `LoginBacked: true`。于是校验只看
   `opts.APIKey`（恒空）→ 硬拒 → `runConnect` 包装成用户看到的报错（`pkg/tui/setup.go:2172`）。

**为什么这条校验在这里是错的**：「非登录行必须有 key」这个语义的真正归属是
`UpsertLLMModel`（`pkg/process/llm_models.go:651-745`）：命中既有行且 `APIKey == nil` 时**保留存储 key**
（:674-676，:705 前 `key==""` 才拒）——恰恰正确接受订阅已写好的行；只有「行将落成无 key」才报同文案错误
（:678/:706）。`Execute` 入口这条是它的 key-blind 副本，恰好拒绝了写路径接受的场景。

**复现（已在当前源码上跑通，测试文件已删）**：临时测试先 `process.UpsertLLMModel` 写入带 key 的
volcengine 行（模拟 `sub.Provision`），再以向导 `options()` 的形状调 `process.Execute`：

```
zz_repro_volcengine_test.go:31: provisioned row id=llm-08f095e1-… (this is sub.Provision's write)
zz_repro_volcengine_test.go:42: Execute (what runConnect calls after the wizard): api_key required in non-interactive mode
--- FAIL: TestReproVolcengineConnectSave
```

与用户报错逐字一致。修复后同一链路应通过。

**已排除的竞争解释**：
- 「Provision 没写 key」——不成立：缺 key 时 Provision 报 `ErrPlanKeyRequired`（引导贴 key），不是本文案；
  复现里行带 key 仍被拒。
- 「两次写入身份不匹配」——不成立：报错发生在开库查询之前的入口校验，根本没走到查找；且 offers 字段
  同源（`volcengineModelOffers` :458-475 → 两次写入同一 Provider/Model/BaseURL/APIPath）。
- 「volcengine 应标 LoginBacked」——不成立：那会全局改语义（`NormalizeLLMModelIdentity` 对 login-backed
  保留空 apiPath、`UpsertLLMModel` 将允许无 key 的火山行——含手填 key 的按量付费端点、gateway 的
  LoginBacked 展示与 `DropLentLoginKeys` 行为全变）。plan 281 的设计是行带 key。

**同缺陷的其他路径**：`process.Execute` 全仓只有两个调用方——`/connect`（`pkg/tui/setup.go:2170`）与
首启 onboarding（`pkg/tui/setup.go:134`，同一向导），一处修复全覆盖。gateway/web 不经 `Execute`
（已 grep 证实），不受影响。

## 修复设计

**删除 `pkg/process/setup.go:70-73` 的 api_key 入口校验，把判定交还给 `UpsertLLMModel`。**
不新增判定、不改 provider 语义；:141-148 的 switch（credentials-backed 显式清 key、其余 nil 保留存储 key）
原样保留，并在删除处以注释说明归属（简短、贴仓库注释风格）。

### 行为变化矩阵

| 场景 | 旧行为 | 新行为 |
|---|---|---|
| `/connect` 或 onboarding 选火山方舟（个人版贴 key / 团队席位 key） | Provision 写行成功后 `Execute` 报 `api_key required…`；默认模型未设、会话未切换 | 保存成功：行保留存储 key，写入 SubagentAllowed 答案，设为 agent 默认（若空），会话切换所选模型 |
| `/connect` 选 ChatGPT / OpenCode 订阅 | 通过（credentials-backed） | 不变 |
| `/connect` 选普通 API key 服务商（向导必问 key） | 正常 | 不变 |
| 直接调 `Execute` 传无 key 的 openai 且无既有行 | `validation_error` 事件 + `api_key required in non-interactive mode` | **同文案**报错，事件名变为 `save_error`（来自 `UpsertLLMModel`）——刻意修订，用户可见文案不变 |
| `Execute` 无 key 但既有行带 key（火山重连/改授权开关） | 误拒（本 bug） | 通过，保留存储 key |

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build ./pkg/process ./pkg/tui` | exit 0 |
| 定点测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run 'TestExecute' -count=1` | all pass |
| TUI 定点 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Volcengine|Subscription' -count=1` | all pass |
| 包级回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/tui -count=1` | all pass |
| vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/process ./pkg/tui` | exit 0 |
| gofmt | `gofmt -l pkg/process pkg/tui` | 无输出 |
| 真机验收构建 | `go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain` | exit 0 |

（直连 go 编译 `pkg/pdf` 才需要 `PKG_CONFIG_PATH`；本计划不碰 `pkg/pdf`。）

## Scope

**In scope**（只改这些）:
- `pkg/process/setup.go` — 删除 :70-73 的 api_key 入口校验 + 归属注释
- `pkg/process/session_context_test.go` — 在 `TestExecuteRequiresModelAPIKeyAndBaseURL`（:365）之后新增两个回归用例
- `pkg/tui/setup_test.go` — 新增一条向导→`process.Execute` 全链回归测试

**Out of scope**（看起来相关，明确不碰）:
- `pkg/subscription/volcengine.go` / `registry.go` — Provision 语义正确，是受害方不是病因
- `pkg/config/agent_llm.go`（`CredentialsBackedProvider`）、`pkg/config/provider_registry.go`（volcengine 不加 `LoginBacked`）— 语义正确，动了会放大面
- `pkg/process/llm_models.go`（`UpsertLLMModel`）— 它就是正确语义的归属，不改
- gateway / web / 前端 — 不经 `process.Execute`
- `Execute` 的 provider/model/base_url 入口校验 — 无病，保留

## Git workflow

owner 常设规则：**零 commit**，改动留工作区由 owner 审阅。目录树有并行 agent（forebrain-harness-a3，
**busy**）：其任务「/connect Qoder CN 面板 Esc 无法返回 + 模型列显示」大概率改 `pkg/tui/setup.go`（向导翻页）
与 `pkg/tui/setup_test.go`——与本计划 Step 3 的测试文件重叠。动手前：
1. 重跑 drift check 的 `git status`；2. `peer_send forebrain-harness-a3` 告知本计划将新增
   `pkg/tui/setup_test.go` 的一条测试（纯追加函数，不动其改动区），确认无冲突窗口；若其改动已落盘且
   `pkg/tui/setup_test.go` 出现非本计划diff，只 stage/编辑自己的追加函数，绝不回退他人改动；归属不明即停。

## Steps

### Step 1: 删除 `Execute` 的 api_key 入口校验

`pkg/process/setup.go`，删除 :70-73 四行（保留 :62-68 provider/model 校验与 :74-77 base_url 校验），
在 `key := strings.TrimSpace(opts.APIKey)` 的 switch（:138）上方补一行注释说明归属，形如：

```go
	// The api_key requirement is the write's to judge, not a gate here: a row
	// a subscription's Provision already wrote keeps its stored key when the
	// input names none, and a row that would end up keyless is refused by
	// UpsertLLMModel with the same sentence.
```

**Verify**: `CGO_ENABLED=1 go build ./pkg/process` → exit 0；
`grep -n "api_key required" pkg/process/setup.go` → 无输出（该文案只剩 `llm_models.go`）。

### Step 2: process 层回归测试

`pkg/process/session_context_test.go`，紧跟 `TestExecuteRequiresModelAPIKeyAndBaseURL` 新增：

1. `TestExecuteSavesSubscriptionRowAlreadyProvisioned` —— 复现链的固化：
   - `t.TempDir()` 建 home/cfgPath/`Context`（照抄 :366-372 骨架），`t.Cleanup(process.ResetResolve)`（进程级缓存纪律）；
   - 先 `installBuiltinModelTables()`（同包未导出函数，`pkg/process/config_db.go:253`，幂等）——保证
     `VerifyProvider("volcengine")` 在任何全局 catalog 状态下都过；
   - `StateDB(rt)` → `UpsertLLMModel`（volcengine / doubao-seed-2.1-pro / `https://ark.cn-beijing.volces.com/api/coding`
     + `/v1/messages`，`APIKey:&key`，`Source:"volcengine"`，`SourcePlan:"coding-plan"`）→ 关库；
   - `Execute(rt, Options{…同身份…, SubagentAllowed:true})`（**无 APIKey**，即向导 `options()` 形状）；
   - 断言：err==nil；`rep.ModelID` == 先前写入的行 id；重读该行 key 仍是贴入值、`SubagentAllowed`==true。
   - 结构模式照抄同文件 `TestExecuteRequiresModelAPIKeyAndBaseURL`。
2. `TestExecuteStillRefusesKeylessNewVolcengineRow` —— 防松过头：空 home 上直接 `Execute` 同形状 opts
   （无既有行、无 key）→ 断言 err 含 `api_key required in non-interactive mode`。

既有用例 `TestExecuteRequiresModelAPIKeyAndBaseURL`（:365）**不改**：「missing api key」用例改由
`UpsertLLMModel` 报同文案，断言依旧成立——跑通即证明。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run 'TestExecute' -count=1 -v`
→ 全部 PASS，含 2 个新用例；`-run TestExecuteSavesSubscriptionRowAlreadyProvisioned` 单跑也 PASS。

### Step 3: TUI 全链回归测试（向导→Execute，bug 的真实骑行路径）

`pkg/tui/setup_test.go` 新增 `TestVolcengineSubscriptionSetupSavesThroughExecute`，模式照抄
`TestConfigureSubscriptionAsksForTheKeyAPlanCannotFetch`（:804-840）：

- `volcengineLike()`（:404，offers 即 Coding Plan 形状；其 `fakeSubscription.Provision` :334-365 **本来就会**经
  `process.UpsertLLMModel` 写带 key 的真行）；
- `selector := &fakeSelector{confirms: []bool{true}, inputs: []string{"ark-pasted-placeholder-key"}}`，
  `cfg := setupTestConfig(t, selector)`；
- `opts, ok, err := runSubscriptionSetup(t, cfg, sub)` → ok；
- 然后**接上 runConnect 缺的那步**：`rt := process.Context{Home: cfg.Home, ConfigPath: cfg.ConfigPath, Config: appcfg.Root{}}`，
  `rep, err := process.Execute(rt, opts)` → 断言 err==nil（修复前此行即用户报错）、`rep.Provider=="volcengine"`、
  `rep.ModelID != ""`。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestVolcengineSubscriptionSetupSavesThroughExecute' -count=1 -v`
→ PASS（修复前跑它应 FAIL 且错误正是 `api_key required in non-interactive mode`——可先写后改顺序验证）。

### Step 4: 包级回归 + 静态检查

**Verify**:
- `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/tui -count=1` → all pass
- `CGO_ENABLED=1 go vet -tags fts5 ./pkg/process ./pkg/tui` → exit 0
- `gofmt -l pkg/process pkg/tui` → 无输出
- `git status --short` → 改动仅限 Scope 三文件 + `docs/plan/VOLCENGINE_CONNECT_SAVE_API_KEY_PLAN.md`

### Step 5: tmux 真机验收（技能第 7 步，按本 bug 裁剪）

构建必须带 `-tags fts5`。全程用临时 `FOREBRAIN_HOME` 与临时项目目录，**不得污染真实 `~/.forebrain*`**：

1. **回归（正常路径不受影响）**：临时 home + 临时项目目录起 TUI，onboarding 向导选一个 API key 服务商
   （如 OpenAI，贴假 key）走完 → 到达主界面；重启直达 TUI（不重跑向导）。逐屏 `capture-pane -p` 取证。
2. **复现→修复（等价链）**：真火山设备码流程需要真实火山账号+浏览器，属 owner 手动验收；机器侧以
   Step 2/3 的测试为该链路的复现→修复证据（Step 3 测试修复前红、修复后绿）。tmux 里补一条只到可达边界：
   `/connect` 打开 provider 页可见「火山方舟」订阅行，Esc 取消干净返回 composer，无卡死。
3. **中断自救**：向导中途 Ctrl+C / Esc 退出 → EXIT_CODE 干净（0 或既定取消码）→ 再启动可恢复，不落死胡同。
4. **非 TTY 不变**：`forebrain --home <临时home> </dev/null` 仍打印 non-interactive 诊断退出，不挂起。

**Verify**: 每场景 capture 屏幕证据存验收记录；`tmux kill-session` + 临时目录清理。

### Step 6: 收口

- 本计划抄录到 `docs/plan/VOLCENGINE_CONNECT_SAVE_API_KEY_PLAN.md`，状态行回填
  「已实施并真机验收通过（日期）」+「验收记录」章节（场景→证据，含工具链坑）。
- 清理验收环境；报告根因链/最小改动/证据/遗留；**改动留工作区不 commit**。

## Done criteria（机器可查）

- [ ] `pkg/process/setup.go` 中 `grep -c "api_key required"` == 0；`pkg/process/llm_models.go` 仍含该文案 2 处
- [ ] Step 2 两个新用例 + Step 3 新用例存在且 PASS；`TestExecuteRequiresModelAPIKeyAndBaseURL` 未修改且 PASS
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/tui -count=1` 全绿
- [ ] `go vet` exit 0；`gofmt -l` 无输出
- [ ] `git status --short` 改动仅限 Scope 三文件（+ docs/plan 抄录件）；未 commit
- [ ] tmux 四场景（按本 bug 裁剪版）取证完成并回填计划文件

## STOP conditions

- drift check：`pkg/process/setup.go` / `session_context_test.go` / `pkg/tui/setup_test.go` 基线内容不符，或
  动手时这些文件已有他人未提交改动（先 peer 确认，归属不明即停）。
- 删除入口校验后发现 `UpsertLLMModel` 的同文案错误在某个既有测试里被以**事件名**（validation_error）断言
  （计划期已 grep 为无；若执行期出现，停下报告而非改断言）。
- Step 2/3 新用例在修复后仍失败两次以上——说明因果链另有分支，停，回报现象。
- 发现需要动 Out-of-scope 文件才能通过验证。

## Maintenance notes

- 「非登录行必须有 key」语义唯一归属 `UpsertLLMModel`；今后任何入口校验想复制它都会复刻本 bug——评审时
  盯住新加的入口校验。
- 事件流变化：无 key 新行的失败事件从 `setup.execute.validation_error` 变为 `setup.execute.save_error`
  （文案不变）。若离线分析依赖该事件名，需同步。
- 真火山端到端（真实账号设备码登录→贴 key→保存→会话切换出模型回复）由 owner 手动验收；agent 侧验收按
  owner 2026-11-07 裁决一律 fake，不烧真配额。
- 遗留观察（不在本期）：`TestConfigureSubscriptionAsksForTheKeyAPlanCannotFetch` 只测到向导返回 opts，
  本计划 Step 3 把保存链接进测试后，此类「向导测了、Execute 没测」的缺口对 ChatGPT/OpenCode 同样补上了范式。

## Implementation Status

- **Verdict**: APPROVE（2026-11-12，tech lead 复核）
- **Worktree**: `.worktrees/fix-volcengine-connect-save-api-key`，分支 `fix-volcengine-connect-save-api-key`，基线 `d2281835`
- **Executor commit**: `497db730` `fix(setup): the save stops asking for a key the row already has`（+145/−4，仅 Scope 三文件）
- **任务**: 全部 Steps 完成（Step 6 按计划由 reviewer 承接）。

### 复核亲自重跑的检查（reviewer's own run）

- `grep -c "api_key required" pkg/process/setup.go` = 0；`pkg/process/llm_models.go` = 2 ✔
- `git diff` session_context_test.go 删除行数 = 0（既有用例未动，纯新增）✔
- 定点：`go test -tags fts5 ./pkg/process -run 'TestExecuteSaves…|TestExecuteStillRefuses…|TestExecuteRequiresModelAPIKeyAndBaseURL'` → ok；`./pkg/tui -run TestVolcengineSubscriptionSetupSavesThroughExecute` → ok ✔
- 包级：`./pkg/process` ok 233.9s；`./pkg/tui` ok 84.7s ✔
- `go vet -tags fts5 ./pkg/process ./pkg/tui` exit 0；`gofmt -l` 无输出 ✔
- 红侧证据：plan 期复现（基线源码，报错与用户逐字一致）+ 执行者换回 HEAD 源码两次红→绿 ✔

### 偏差（已文档化，判定接受）

1. **Step 3 fixture 改用导出的 `llm.SetBuiltinModels("volcengine", …)` + cleanup 还原**（计划原文写
   `installBuiltinModelTables()`，未导出、pkg/tui 不可达）。核验：与 `pkg/run/config_test.go:2637` 的既有
   测试惯例逐字同型；断言未因此放宽。接受。
2. **fresh worktree 需 third_party 原生依赖**：从主树 symlink `libtokenizers`/`pdfium`（gitignored，
   `git status` 干净）+ `PKG_CONFIG_PATH`。环境操作，非代码改动。接受。

### tmux 真机验收（执行者记录，四场景按本 bug 裁剪版）

1. 回归：临时 home onboarding（OpenAI/假 key/gpt-5.5）→ 主界面 footer `openai/gpt-5.5 · medium`；DB
   `fb_llm_models` has_key=1、`fb_config.agents` default_model 指向该行；重启直达 composer 不重跑向导。
2. `/connect` provider 页可见 `Volcengine Ark` 订阅行；Esc 干净返回 composer、后续输入正常。真火山设备码
   端点属 owner 手动验收（机器侧证据 = 红→绿测试）。
3. 中断自救：向导 Ctrl+C / Esc → EXIT=0；同 home 重启向导可恢复。
4. 非 TTY：`</dev/null` → 立即 EXIT=1 + non-interactive 诊断，不挂起。

### 遗留

- ~~合并~~ 已完成（owner 2026-11-12 指令）：dev 自 `d2281835` fast-forward 至 `497db730`（基线未动，无需
  rebase）；主树定点复跑 3 用例绿；worktree `.worktrees/fix-volcengine-connect-save-api-key` 与临时分支已删除，
  主树 status 干净。
- 真火山账号端到端（设备码→贴 key→保存→会话切模型出回复）留 owner 手动验收。
