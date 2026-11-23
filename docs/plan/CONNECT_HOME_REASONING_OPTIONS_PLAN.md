# 修复 /connect 思考档位：home models.json 的 reasoning_options 对已存在 key 不生效

- 状态：已实施并真机验收通过（2026-11-21），已合并回 dev（commit `d3aa95ed`，未 push）；方案 A（仅修 merge 机制）。详见文末 ## Implementation Status。
- Planned at：工作区基线 HEAD `4003bcb74719649ba4ee52eded95c4a1ffe1eca5`。**注意：工作区现已脏**——并行会话 forebrain-harness-08 在改 pkg/tui/{reducer,commands,run,notify}.go、pkg/event/run_events.go、pkg/run/peer_tools.go，并新建 pkg/run/zz_peer_ui_repro_test.go、pkg/tui/zz_peer_ui_repro_test.go；主树另有**非我非该会话**的既有未提交 WIP（pkg/run/subagent.go、subagent_test.go 的 effort pinning）。这些都不属本计划 scope，已 peer_send 互认零交集。drift 检查与 git 范围判据一律 **path-scoped**（仅 pkg/llm、FOREBRAIN.md、docs/plan），不做全树 git 门（会假红）。
- 技能：plan-gated-bugfix（审批门控）；本文件为会话审批件，执行第 0 步另存 repo 惯例副本 `docs/plan/CONNECT_HOME_REASONING_OPTIONS_PLAN.md`
- 落盘纪律：批准后直接在当前源码实施，不用 worktree/快照；改动留工作区不 commit；计划文件禁止 commit

## 1. 目标（Why this matters）

用户在 TUI `/connect` 的「Use an API key」组选 DeepSeek → `deepseek-v4.1-flash` 后，「Default reasoning effort」页只显示 `Provider default`，并提示「No discrete reasoning levels are available for this model.」（见截图）。用户已在 `~/.forebrain/models.json` 为该 key 配置 `reasoning_options: [{type:toggle},{type:effort,values:[low,high,max]}]`，期望列出 Low/High/Max 档位。home 文件是文档承诺的「不重新构建即可给模型补 window/output/thinking levels」的通道，此通道对「shipped 已有 key 但该行缺 reasoning_options」的模型失效。修好后，所有读全局 catalog 的表面（/connect effort 页、/model 选择器、web 设置›模型、gateway session options）一并恢复档位。

## 2. 根因（file:line 证据链 + 现场证据 + 复现）

### 现场证据（已验证）
- shipped `assets/models.json` **已含** key `deepseek/deepseek-v4.1-flash`：`reasoning: true`，**无 `reasoning_options` 字段**（`deepseek/deepseek-v4.1-flash-1017` 同样缺）。
- 用户 home `~/.forebrain/models.json` 同 key **带** `reasoning_options: [toggle, effort low/high/max]`。
- 对照：`opencode/deepseek-v4.1-flash`、`opencode-go/deepseek-v4.1-flash` 在 shipped 里**有** effort 档 low/high/max。
- 上游成因：`scripts/update-models-asset.sh` 只从 providers tree lift `reasoning_options`（`options = meta.get("reasoning_options"); if options is None: continue`）；models.dev 的 deepseek providers tree 未给 v4.1-flash 声明 options，故 shipped 行缺该字段。这是上游数据缺口，不是代码 bug；owner 选定不改 shipped 数据（方案 B 搁置）。

### 代码链（当前源码摘录）
1. `pkg/llm/catalog.go:358-371` `mergeSupplement` —— 两边都有的 key，home 整行丢弃：
```go
func mergeSupplement(base, supplement map[string]rawEntry) {
	seen := make(map[string]bool, len(base))
	for id := range base {
		seen[strings.ToLower(strings.TrimSpace(id))] = true
	}
	for id, entry := range supplement {
		key := strings.ToLower(strings.TrimSpace(id))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		base[id] = entry
	}
}
```
2. `pkg/llm/catalog.go:209` `rawEntry.ReasoningOptions *[]ReasoningOption`（指针：字段缺失 vs 空数组可区分）；`:276-279` `buildCatalog` 仅当 `e.ReasoningOptions != nil` 才 `ReasoningKnown=true`。
3. `pkg/llm/catalog.go:128-131` `effortsFromOptions`：`if !known { return nil, false }`。
4. `pkg/tui/setup.go:2102-2106` `effortLevelsFor`：`llm.LookupCatalog(t.CatalogProvider, t.Model)` hit ok 但 `hit.ReasoningEfforts()` known=false → `discrete=nil`；新行无 `CapEfforts`、非 custom 无 form 答案、无 subscription offer → `levels=nil` → `effortLevels.empty()`。
5. `pkg/tui/setup.go:2277-2278` `promptDefaultEffort`：`if offered.empty() { title += "\nNo discrete reasoning levels are available for this model." }` —— 即截图文案。
6. 单一源：`globalCat` 由 `InitGlobalCatalog(home)`（`pkg/process/runtime.go:107`、`pkg/process/seed.go:27`）经 `ResolveFor`→`loadCached`→`LoadWithSupplement`（`catalog.go:325`）→`mergeSupplement` 建立；/connect、/model、web、gateway 共读它。**修 merge 层一处即全覆盖，无需改任何表面代码。**

### 真机复现（修复前，HEAD 4003bcb7 重建二进制，已取证）
环境：`FOREBRAIN_HOME=/tmp/fb-repro-home.gwaw4H`（`cp ~/.forebrain/models.json` 原件入内），项目 dir `/tmp/fb-repro-proj.5kT4KH`。
构建：`PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain`
驱动（tmux `fbrepro`，120×42）：onboarding provider 页 → Down×9 到「Use an API key」组 DeepSeek + Enter → key 页输入 `sk-dummy-repro-key` + Enter → Model 页输入过滤 `deepseek-v4.1-flash` + Enter → Step 4 of 6。
失败输出（`/tmp/fb-repro-effort-before.txt`）：
```
Default reasoning effort
Choose the default for deepseek/deepseek-v4.1-flash. No discrete reasoning levels are available for this model.
❯ Provider default
```
vendor API-key 页序：Provider→Credentials→Model→Effort→Subagents→Review（`setupPageConnection` 仅 custom，effort 页前无连接探测，dummy key 即可达）。

## 3. 修复设计（方案 A：仅修 merge 机制）

** refined 规则**：shipped 文件赢它**陈述的每一个值**；`reasoning_options` 是唯一「缺失」可与「陈述的空数组 []」区分的字段（指针），故 shipped 行**未陈述** levels（字段缺失）时取 home 行的该字段；shipped 行已命名 options（含 []）一律保留。其余字段（attachment/window/modalities/reasoning…）一律 shipped 赢，绝不从 home 拷贝。这契合 home 文件文档目的，且不重定义 shipped 已陈述的值——是对 2026-11-16 裁决字面的精确化（owner 已批）。

`pkg/llm/catalog.go` `mergeSupplement` 改为（按 lowercase key 定位 base 原 id，仅补一个字段；`done` 集保留旧码对 supplement 内重复 case-variant key 的「首个处理、其余跳过」语义）：
```go
func mergeSupplement(base, supplement map[string]rawEntry) {
	baseByKey := make(map[string]string, len(base))
	for id := range base {
		baseByKey[strings.ToLower(strings.TrimSpace(id))] = id
	}
	done := make(map[string]bool, len(supplement))
	for id, entry := range supplement {
		key := strings.ToLower(strings.TrimSpace(id))
		if key == "" || done[key] {
			continue
		}
		done[key] = true
		if baseID, exists := baseByKey[key]; exists {
			// The shipped file wins every value it states (owner 2026-11-16).
			// reasoning_options is the one field whose absence is distinguishable
			// from a stated empty list, so a shipped row that says nothing about
			// levels takes the supplement's — the home file's documented job is to
			// give a model its thinking levels without a rebuild. A shipped row
			// that names options (even []) keeps them.
			baseEntry := base[baseID]
			if baseEntry.ReasoningOptions == nil && entry.ReasoningOptions != nil {
				baseEntry.ReasoningOptions = entry.ReasoningOptions
				base[baseID] = baseEntry
			}
			continue
		}
		baseByKey[key] = id
		base[id] = entry
	}
}
```
同步更新描述该规则的注释（均在 pkg/llm/catalog.go，scope 内）：`mergeSupplement` 自身 doc（:354-357「adds every entry … does not already name」）、`LoadWithSupplement`（:315-318）、`ResolveFor` doc（:537-544「never redefines a row the build ships」）、`InitGlobalCatalog`（:625-630）的「home only contributes models the build does not know」措辞，精确化为「shipped 赢其陈述值；home 可补 shipped 行缺失的 reasoning_options」。

### 行为变化矩阵
| 场景 | 旧行为 | 新行为 | 刻意修订? |
|---|---|---|---|
| shipped 有 key 无 options + home 同 key 有 options | home 整行丢，known=false，无档位 | 补 options→known=true 出档位；其余字段仍 shipped | **是（本修复）** |
| shipped 有 key 有 options [low,high] + home 同 key [max] | shipped [low,high] | shipped [low,high]（不变） | 否（保护） |
| shipped 有 key options [] + home 有 options | shipped []（known true 无档位） | shipped []（不变） | 否（保护） |
| shipped 有 key 无 options + home 同 key 也无 options | 无档位 | 无档位（不变） | 否 |
| shipped 有 key 无 options + home 同 key 有 options 但 attachment/window 不同 | shipped attachment/window | shipped attachment/window（不变，仅补 options） | 否（保护） |
| shipped 有 key options 为 null + home 有 options | null→nil 指针，known=false 无档位 | 按「缺失」补 home options（null 未陈述值，自洽） | 是（同首行） |
| key 仅 home 有 | 整行加入 | 整行加入（不变） | 否 |
| home 内同 key 两个大小写变体 | 首个处理、其余跳过 | 同（`done` 集保留旧语义） | 否 |
| home 非 JSON | logged，shipped 独答 | 不变 | 否 |

受影响表面（均读 globalCat，无代码改动）：/connect effort 页、/model 选择器、web 设置›模型、gateway session options——对此类模型恢复档位。

**两点验收须知（critique 实证）**：
- home 的 `toggle` 型 option **不**产生 Off 行（`effortsFromOptions` 只读 effort 型；Off 来自 "none" 值或行级 CapReasoningOff）。修后 deepseek-v4.1-flash 页 = Low/High/Max + Provider default，**无 Off**，不要期待 Off。
- 用真实 home 比对，450 个共享 key 中本修复只新补 **2 个** key 的 options：`deepseek/deepseek-v4.1-flash` 与 `alibaba/qwen4-max`（home 给后者 [none,low,high,xhigh]，修后该模型会多出 Off 行）。属已批规则的自然结果，验收记录点名第二个 key。

## 4. Scope

**In scope**
- `pkg/llm/catalog.go`：`mergeSupplement` 函数体 + 四处描述注释（`mergeSupplement`/`LoadWithSupplement`/`ResolveFor`/`InitGlobalCatalog`，见 §3）。
- `pkg/llm/catalog_test.go`：新增回归测试（见 Steps）。
- `FOREBRAIN.md`：第 92-94 行契约句精确化（「a key both files name keeps the shipped value」→ 赢其陈述值、home 可补缺失的 reasoning_options）。
- `docs/plan/CONNECT_HOME_REASONING_OPTIONS_PLAN.md`：本计划 repo 惯例副本（执行第 0 步）。
- 构建产物 `build/bin/forebrain`、`build/bin/models.json`（验收重建/同步，非源码）。

**Out of scope（看起来相关但明确不碰）**
- `pkg/tui/**`（含 setup.go/setup_test.go）：表面逻辑正确，根因在 merge 层；且并行会话 forebrain-harness-08 正在改 pkg/tui/reducer|commands|run|notify.go，避免任何重叠。
- `scripts/update-models-asset.sh`、`assets/models.json`：方案 B（OVERRIDES 数据纠正）owner 已搁置。
- `pkg/turn/models.go`、`pkg/gateway/*`、`pkg/run/*`：共读 globalCat，无需改。
- 既有测试 `TestCatalogSupplementAddsOnlyWhatTheShippedFileLacks`（:219）：其 home 行无 options，断言 attachment/window shipped-wins，本改动不触发，**保持不动**。

**不得改变**：shipped 已陈述的任何值；home 文件不被改写（既有断言 :248）；rawEntry 其它字段类型。

## 5. Steps（每步带验证命令与预期）

> 通用环境：`export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"`（pkg/llm 单测本身不需 pdfium，但 vet/全量 build 需要）。测试一律 `CGO_ENABLED=1 -tags fts5`。

**Step 0 — 落 repo 惯例计划副本**
把本审批件内容存为 `docs/plan/CONNECT_HOME_REASONING_OPTIONS_PLAN.md`。
验证：`test -f docs/plan/CONNECT_HOME_REASONING_OPTIONS_PLAN.md` 成功。

**Step 1 — 先写回归测试并确认 RED（钉根因，修复前必失败）**
在 `pkg/llm/catalog_test.go` 新增 `TestCatalogSupplementFillsAbsentReasoningOptions`，沿用既有脚手架 `writeShippedCatalog(t, []byte)`（:205，设 `AssetsDirEnv` 临时 shipped）+ `restoreGlobalCatalog(t)`（:360）+ `InitGlobalCatalog(home)` + `ReasoningEfforts(provider,model)`：
- shipped：`{"deepseek/deepseek-v4.1-flash":{"id":"deepseek/deepseek-v4.1-flash","reasoning":true,"limit":{"context":1000000,"output":384000}}}`（无 options）
- home：同 key 带 `"reasoning_options":[{"type":"toggle"},{"type":"effort","values":["low","high","max"]}]`，并带**冲突的** `"limit":{"context":1000}`（使 window 断言有保护力，证明 gap-fill 不拷贝其它字段）。
- 断言：`levels, known := ReasoningEfforts("deepseek","deepseek-v4.1-flash")`；`known==true` 且 `levels==[low high max]`；并断言 `Lookup` 的 `ContextWindow==1000000`（shipped window 不被 home 的 1000 改）。
验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -run TestCatalogSupplementFillsAbsentReasoningOptions -count=1`
预期：**FAIL**（known=false，复现根因）。若 PASS → 根因判断有误，STOP 重查。

**Step 2 — 改 mergeSupplement（§3 代码）+ 同步注释，转 GREEN**
验证：同 Step 1 命令 → **PASS**。

**Step 3 — 保护性不变量测试（防 gap-fill 泄漏到其它字段/覆盖 shipped 值）**
新增 `TestCatalogSupplementKeepsShippedReasoningOptions`（表驱动或子测试）：
- shipped options [low,high] + home [max] → 结果 [low,high]；
- shipped options [] + home [low] → known=true、levels 空；
- shipped 无 options + home 无 options → known=false；
- shipped 无 options + home 有 options 但 home attachment=true/window=1000 → 结果 attachment=false、window=shipped（仅 options 被补）。
验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -run 'TestCatalogSupplement' -count=1` → 全 PASS（含既有 :219/:256/:274 三个 supplement 测试不回归）。

**Step 4 — 文档同步**
更新 `FOREBRAIN.md` 92-94 行契约句与 `catalog.go` 注释（§3）。
验证：`grep -n "reasoning_options" FOREBRAIN.md` 命中新句；人工读 diff 确认仅精确化、未删保护语义。

**Step 5 — 包级回归 + 下游共享基础设施回归 + lint/vet**
```
gofmt -l pkg/llm/catalog.go pkg/llm/catalog_test.go        # 预期：无输出
CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -count=1         # 预期：ok
CGO_ENABLED=1 go vet -tags fts5 ./pkg/llm                   # 预期：无输出
CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/process ./pkg/assembly -count=1   # 必做：共读 catalog 的下游（assembly 有 ResolveFor 消费测试）
```
catalog 属共享基础设施，下游回归与风险相称；gateway/tui 套件可免（无表面改动，且 tui 有并行会话在改）。**按包 scoped，不跑全树 `go test ./...`**（会撞上并行会话在改文件与主树既有 WIP）。

**Step 6 — 重建 + tmux 真机验收（必做，测试绿≠真机可用）**
```
PKG_CONFIG_PATH=... CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain
scripts/install-assets.sh ./build/bin
```
复用隔离环境（临时 FOREBRAIN_HOME 含用户 models.json 原件 + 临时项目 dir；**不得污染真实 ~/.forebrain**）。四场景按 bug 裁剪：
1. **复现→修复**：重跑 §2 驱动路径到 Step 4 of 6，capture 断言出现 `Low`/`High`/`Max` 且**不再**含「No discrete reasoning levels」。
2. **回归**：选一个 shipped 自带 options 的模型（如 deepseek-v4-flash）走同路径，档位仍正常；二次启动直达 TUI。
3. **中断自救**：effort 页 Esc 逐级退出 → EXIT_CODE 干净 → 再启动可恢复。
4. **非 TTY 不变**：`FOREBRAIN_HOME=<tmp> ./build/bin/forebrain gateway start </dev/null` 仍报诊断、不挂起。
验收脚手架 home 只用自己 mktemp 的目录，显式设 FOREBRAIN_HOME，不读真实 ~/.forebrain/.env。

**Step 7 — 收口**
本计划文件回填「已实施并真机验收通过（日期）」+「验收记录」章节（场景→证据，含工具链坑；点名 Off 不出现与 alibaba/qwen4-max 第二 key）；`tmux kill-session`、清理临时目录；`git status --porcelain -- pkg/llm FOREBRAIN.md docs/plan` 核对 path-scoped scope；改动留工作区不 commit。

## 6. Done criteria（机器可查）
- `gofmt -l pkg/llm/catalog.go pkg/llm/catalog_test.go` 无输出。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -run 'TestCatalogSupplement' -count=1` PASS；`./pkg/llm -count=1` PASS。
- `CGO_ENABLED=1 go vet -tags fts5 ./pkg/llm` 无输出。
- tmux 修复后 effort 页 capture grep 命中 `Low`、`High`、`Max`，且不含 `No discrete reasoning levels`。
- **path-scoped** 范围判据（不做全树 git 门——工作区有并行会话与主树既有 WIP，全树必假红）：`git status --porcelain -- pkg/llm FOREBRAIN.md docs/plan` 的改动集合 ⊆ {pkg/llm/catalog.go, pkg/llm/catalog_test.go, FOREBRAIN.md, docs/plan/CONNECT_HOME_REASONING_OPTIONS_PLAN.md}；其中 docs/plan 下若出现兄弟计划文件须逐文件确认属并行会话预期产物，否则 STOP。本计划改动不含任何 pkg/tui、pkg/run、pkg/event 文件。build/bin/* 已 gitignore，不入 git 门。
- 既有 `TestCatalogSupplementAddsOnlyWhatTheShippedFileLacks` 仍 PASS（未改其断言）。

## 7. STOP conditions
- `mergeSupplement`/`rawEntry.ReasoningOptions`/`buildCatalog` 当前内容与 §2 摘录不符（drift）→ STOP 重定位。
- Step 1 回归测试在修复前 PASS → 根因判断有误，STOP 重查（不得继续改）。
- 修复后既有 :219 测试 FAIL → gap-fill 泄漏到其它字段，STOP 修正而非改断言。
- 需要改 pkg/tui 或任何表面才能出档位 → 与「单一源」假设矛盾，STOP 报告。
- tmux 报 `no such module: fts5` → 构建漏 `-tags fts5`（工具链问题，重建），不是产品 bug。
- 并行会话 forebrain-harness-08 的改动触及 pkg/llm/catalog.go 或本计划文件 → 先 peer_send 协调，不擅自合并/回退对方改动。

## Implementation Status

已实施并真机验收通过（2026-11-21）。基线 HEAD `4003bcb74719649ba4ee52eded95c4a1ffe1eca5`，drift 检查全吻合（mergeSupplement/rawEntry.ReasoningOptions/buildCatalog/effortsFromOptions/四处注释/ReasoningEfforts 签名）。改动留工作区**未 commit**；计划文件未 commit。

### 各步结果
- **Step 0** done：`docs/plan/CONNECT_HOME_REASONING_OPTIONS_PLAN.md` 副本已落（`test -f` 成功）。
- **Step 1** done（RED）：新增 `TestCatalogSupplementFillsAbsentReasoningOptions`，修复前 `FAIL … reasoning levels are unknown`（known=false，正是根因）。
- **Step 2** done（GREEN）：`mergeSupplement` 按 §3 改（baseByKey 定位原 id + done 集保留 case-variant 首处理语义 + 仅补 ReasoningOptions），同命令 `ok`。
- **Step 3** done：新增表驱动 `TestCatalogSupplementKeepsShippedReasoningOptions`（4 子测试：shipped options 赢/home 冲突、shipped []、两边皆无、gap-fill 不拷其它字段）；`-run TestCatalogSupplement` 全 PASS，既有 `TestCatalogSupplementAddsOnlyWhatTheShippedFileLacks` 未改仍 PASS。
- **Step 4** done：`FOREBRAIN.md` 契约句精确化（diff 仅该段，保护语义保留）；catalog.go 四处注释（mergeSupplement/LoadWithSupplement/ResolveFor/InitGlobalCatalog）同步。
- **Step 5** done：`gofmt -l` 空、`go vet` 空、`./pkg/llm -count=1` **ok 178.7s**；下游 `./pkg/turn` ok 3.3s、`./pkg/process` ok 228.7s、`./pkg/assembly` ok 1.9s（均 `-tags fts5 -timeout 30m`）。
- **Step 6** done：重建 `build/bin/forebrain` + `install-assets.sh`；tmux 真机四场景 + 第二 key 全 PASS（见下）。
- **Step 7** done：重跑廉价 done 判据（gofmt/vet 空、TestCatalogSupplement ok）；path-scoped 范围核对通过；清理 tmux/临时目录。

### 真机验收记录（场景→证据）
二进制 `v0.1.2-0.20261120125928-4003bcb74719+dirty`；隔离 home（cp 用户 models.json 原件，不污染真实 ~/.forebrain）；tmux `fbaccept` 120×42。
- **场景1 复现→修复**：provider 页 Down×9→DeepSeek（Use an API key 组）→dummy key→filter `deepseek-v4.1-flash`→Step 4 of 6。effort 页 = `Low / High / Max / ❯ Provider default`；grep Low=High=Max=1，「No discrete reasoning levels」=0。**无 Off 行**（home 的 toggle 型 option 不产 Off，`effortsFromOptions` 只读 effort 型——与 §3 预测一致，不要期待 Off）。修复前对照仅 `Provider default` + 「No discrete reasoning levels are available for this model.」。
- **第二 key（点名）**：`alibaba/qwen4-max`（Qwen Cloud→Standard China→dummy→filter qwen4-max）。shipped 有 key 无 options、home `[effort none/low/high/xhigh]`；修后 effort 页 = `Off / Low / High / xHigh / ❯ Provider default`，Off=1（来自 "none" 值），No-discrete=0。catalog provider=alibaba。这是已批规则的自然后果（真实 home 450 共享 key 中仅 deepseek-v4.1-flash 与 qwen4-max 两个被新补）。
- **场景2 回归**：`deepseek/deepseek-v4-flash`（shipped 自带 [low,high,max]）effort 页 Low/High/Max 正常、No-discrete=0（shipped 已陈述值不被 home 改）。完成 onboarding（dummy key 无连接探测，Save and continue→trust 页 Down Enter→composer，footer `deepseek/deepseek-v4-flash 100%/1M`）；C-c×2 退出 EXIT=0；**二次启动直达 TUI**（wizard=0、composer `/ commands`=1、footer 模型在）。
- **场景3 中断自救**：effort 页 Esc 逐级 Step4→3→2→1→quit，`EXIT=0` 干净、tui.err 空；同 home 重启恢复到 wizard Step 1、tui2.err 空（可恢复）。
- **场景4 非 TTY**：`FOREBRAIN_HOME=<新鲜home> forebrain gateway start </dev/null` 报诊断 WARN「no model is configured yet; … Settings › Models」×2，并起 web 服务器；`curl http://127.0.0.1:6060/` = **HTTP 200**（functioning、响应正常，非挂起）；kill 后无残留进程。

### 偏差与判断（均 non-load-bearing，intent/scope/行为/done 判据不变）
1. **docs/plan 被 gitignore**（`.gitignore:76 docs/plan/`）：Step 0 副本是本地惯例产物、不入 git；故 git 范围门实际为 `{pkg/llm/catalog.go, pkg/llm/catalog_test.go, FOREBRAIN.md}`。`git status --porcelain -- pkg/llm FOREBRAIN.md docs/plan` = 这三个 M，⊆ 允许集合；docs/plan 无兄弟计划文件出现在 git（ignored）。全树另仅 `pkg/run/subagent.go`/`subagent_test.go`（基线既有 WIP，非本计划、未碰）。
2. **场景4 语义澄清**：`gateway start` 是长跑服务器命令——新鲜 home（无模型表）与 configured home 均**报诊断并 serve web UI**（设计如此，供用户在 web 加模型）。「不挂起」以 curl HTTP 200 实证（服务器 functioning、非 deadlock）；看门狗 kill 是对服务器的测试界定，非产品挂起。该路径在 `cmd/forebrain/serve.go`/`pkg/process/open.go`（未碰），与 catalog merge 修复正交。
3. **场景2「二次启动直达 TUI」** 需先完成一次 onboarding：dummy key 在 effort 页前无探测、Save 成功；trust 页（「Trust this folder?」）在 onboarding 完成后首次进项目时弹出，Down Enter 信任。均已实证。
4. **验证计时**：全量 `./pkg/llm`（178.7s）与下游 process（228.7s）在 Step 5 于**最终未变代码**上跑过；Step 7 重跑廉价 done 判据（gofmt/vet/TestCatalogSupplement）确认，未重复跑已过的重套件（代码自 Step 5 后字节未变）。
5. 第二 key 的 xhigh 档位 UI 显示标签为 `xHigh`（非 `Xhigh`），属既有渲染措辞，未改。

### 未做 / 遗留
- 方案 B（`scripts/update-models-asset.sh` OVERRIDES 数据纠正、shipped 数据）owner 已搁置，未碰。
- 表面代码（pkg/tui/**、pkg/turn/models.go、pkg/gateway/*、pkg/run/*）无需改（共读 globalCat，单一源修复全覆盖）；未碰。
- 已合并回 dev：owner「合并回 dev」授权后，仅提交本计划 3 文件（catalog.go/catalog_test.go/FOREBRAIN.md）为 commit `d3aa95ed`（dev `4003bcb7`→`d3aa95ed`，**未 push**）；`pkg/run/subagent.*` 既有 WIP 非本计划、保持 unstaged 未碰；docs/plan 副本 gitignore 未提交。并行会话 forebrain-harness-08 在 pkg/tui/pkg/run，与本计划零交集，全程无冲突。

### 工具链坑（记录）
- 构建/测试一律 `CGO_ENABLED=1 -tags fts5` + `PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"`；链接期 `ld: warning … built for newer macOS (15.5) than being linked (15.0)` 为无害噪声，非失败。
- 全量套件加 `-timeout 30m` 防默认 10m 包超时假红。
- onboarding wizard 用新鲜 home（无模型表）触发；trust 页在 onboarding 后才问；`gateway start` 长跑需看门狗界定。

## 8. Maintenance notes
- refined 规则只覆盖 `reasoning_options` 一个字段——它是 rawEntry 里唯一指针、能区分「缺失」与「陈述的空 []」的字段。将来若要给别的字段做 absent-gap-fill，须先把该字段改成指针类型，**不得**用零值当「缺失」静默扩展（会重定义 shipped 陈述值）。
- 方案 B（updater OVERRIDES 给 deepseek/deepseek-v4.1-flash 补 shipped options）owner 已搁置；若日后要让无 home 文件的用户也有档位，走 OVERRIDES（dated reason + golden test），不改 merge。
- reviewer 重点：gap-fill 绝不拷贝 home 的其它字段；lowercase-key 定位 base 原 id 的确定性；shipped [] 不被覆盖。
- 缓存：`loadCached` 以 base+supplement 内容 digest 为 key，本改动不改缓存逻辑；运行中进程需 re-resolve/重启才见新 catalog（验收用 fresh start）。