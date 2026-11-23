# 分支审计修复：实施计划索引

由 improve skill 的 branch 审计生成（合并基 `a4fe61c`，含本地提交 `bda9505`、`1a6d708` 与未提交的 SCHEDULED_RUNS_TRANSCRIPT 实现约 106 文件）。**状态：四份计划已全部执行完毕并逐份通过 reviewer 复核（APPROVE），改动全部留在工作区，等 owner 审阅提交。执行期新并入的任务：01-A6（rows.Err() 同族 + TestCreateRunIsAtomicAcrossStores 饿死型 flake）、04-D4（send() op 路径终态写入属主判断）。**

## 基线与漂移

- 计划引用的 `file:line` 是审计时点工作区快照（`bda9505` + 未提交改动）的样子。执行每一步前先按该计划的"前置检查"核对引文仍在原处（按内容核对，不按行号）；对不上就停下来报 owner，不要猜。
- 本仓库规矩：改动全部留在工作区，**owner 手动提交**。执行者不得运行 `git commit`/`git merge`/`git rebase`。

## owner 的原始要求（本轮，逐字）

1. "1. 心跳也不续"——心跳触发的回合撞用量上限时**不**自动继续，与 cron 触发（D7）同语义。
2. "2. 写进升级文档即可"——"新旧二进制共用同一 FOREBRAIN_HOME 时，旧进程的运行中回合会被新进程收割器判废弃"只写进升级文档，不做兼容代码。
3. "3. 按你的默认建议写成实施计划"——即：I-1…I-7 + P-1…P-4 全部修（同族合并），I-8…I-14 打包为小项；I-8（重生成 graph.json）作为提交前动作不必等计划。

> 更正（owner 撤销）：第 2 项原裁决"写进升级文档"已撤销——E2 现为**不写任何升级文档**，见决策表。

## 决策（已定，执行者不得重开）

| 编号 | 问题 | 结论 |
| --- | --- | --- |
| E1 | 心跳回合撞用量上限是否自动继续 | **不续**。心跳回合的 `Origin.Surface` 必须保持 `webchat`（D1 的标签与流式路径依赖它），所以"无人值守"不能走 surface 白名单表达，改用回合上的显式旗标（计划 01 的 A5）。cron 触发维持 D7 的 channel-surface 机制不变。 |
| E2 | 滚动升级约束怎么处理 | **不写任何升级文档**（owner 撤销原"只写文档"裁决）：README 等一切仓库文档不动；不做宽限期豁免、不做 owner 前缀识别；"新旧二进制共用同一 FOREBRAIN_HOME 时，旧进程的运行中回合会被新进程收割器判废弃"作为已知行为接受，只在本表留档。 |
| E3 | 修复范围 | 默认推荐全集，整理为四份计划 01–04；I-8 不单独立计划，作为"每次提交前的固定动作"写在本文件。 |

## 发现 → 计划映射

| 审计编号 | 一句话 | 标注 | 计划 |
| --- | --- | --- | --- |
| I-2 | CronService.Bind/Stop 持锁等旧调度器退出、旧调度器正被 Maintain 挡在同一把锁上的死锁 | introduced | 01-A1 |
| I-4 | 保留期删除的 TOCTOU：查询时判活、删除事务内不复查 | introduced | 01-A2 |
| I-5 | 收割器收不走"时钟已落、状态未落"的 running 僵尸行 | introduced | 01-A3 |
| I-9 | fb_run_owners 死进程租约行永不清理 | introduced | 01-A4 |
| E1 | 心跳回合不自动继续 | owner 裁决 | 01-A5 |
| P-1 | /resume WS 切会话后对新 sid 无归属复查，可跨租户订阅事件流 | pre-existing | 02-B1 |
| P-2 | 会话作用域权限写入（preset / 权限规则）缺归属校验 | pre-existing | 02-B2 |
| I-6 | /cron-settings PUT 请求体无上限 | introduced | 02-B3 |
| I-1 | Windows 上 LSP 整体不可用（可执行位判定 + PATH 解析不识 PATHEXT） | introduced | 03-C1 |
| I-3 | 启动期 consent 提示与运行时的项目解析口径分歧（注册项目 fail-closed 偏差） | introduced | 03-C2 |
| I-7 | TUI LSP 安装观察器：两快照之间完成则永卡 Installing + 订阅泄漏 | introduced | 03-C3 |
| I-12 | Shutdown 忽略 ctx；stopGeneration 最终等待无界 | introduced | 03-C4 |
| I-13 | Restart 在 crash 自愈窗口误报 "stopped" | introduced | 03-C5 |
| I-14 | detectInfo 的 closed 检查与 bg.Add 不原子，Close 后迟到 detect 复活已删工作区 | introduced | 03-C6 |
| P-3 | 前端 released 续发消息取"此刻"全局 sessionId 而非发起会话 | pre-existing | 04-D1 |
| P-4 | 前端 send 收尾无条件清全局流状态，抹掉另一会话的活动回合 | pre-existing | 04-D1 |
| I-10 | ask 表单 payloadJson 解析缺形状校验 | introduced | 04-D2 |
| I-11 | CronSettingsTab 三处原始 error.message 直显，未走统一错误口径 | introduced | 04-D3 |
| I-8 | graph.json 已漂移（10 包 loc 陈旧） | introduced | 本文件"提交前固定动作" |

## 提交前固定动作（I-8，不属于任何计划）

工作区相对上次再生成已改变 10 个包的行数（`pkg/architecture/testdata/graph.json` 记录 `pkg/turn` 9362 行，实测 9676）。CI 会对该文件做 diff 检查。**owner 每次提交涉及 `pkg/**` 的改动前**：运行 `scripts/package-graph.sh`，把再生成后的 `pkg/architecture/testdata/graph.json` 并进该次提交。fan-out/边没有变化（已核对），预期 diff 只含 loc 数字。

## 执行结果（2026-10-05，improve execute 变体）

| 计划 | 状态 | 备注 |
| --- | --- | --- |
| 02 gateway 安全 | DONE (APPROVE) | B1-B3 全落地；chooseSession 对非归属 Ensure 错误也 fail-closed（文档化偏差，已接受）；WS 测试断言 slash_reply 而非 turn_withdrawn（引擎防线先行，边界分支成为纵深防御，安全属性由测试直接断言） |
| 01 引擎与状态 | DONE (APPROVE) | A1-A5 全落地 + 执行期并入 A6（见计划文件）；A3 未把计划 SQL 里的永真式写进生产码；A2 连 files/spill/fire 记录一并按存活集合豁免（同一 TOCTOU 的另一半） |
| 04 前端 | DONE (APPROVE) | D1-D3 全落地 + 执行期并入 D4（send() op 路径五处终态写入属主判断，P-4 同族剩余面）；D1 用 `runSession` 取代字面 `sendSession`（字面条件会破坏"新会话不变"不变式，有专属测试） |
| 03 LSP | DONE (APPROVE) | C1-C6 全落地；C4 按计划自相矛盾处的测试要求实现（善后无条件、ctx.Err() 收尾返回）；C1 以 os/exec 源码为准绳（带扩展名也回落后缀尝试）；C3 把 beginInstall 同步化使计划不变式可达 |

**验证门（reviewer 亲自复跑）**：gofmt 空、`go vet -tags fts5 ./...` 干净、`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全绿（30 包 ok）、`go test -race -tags fts5 ./pkg/lsp/` 绿、`./pkg/architecture/` 绿、前端 271 测试通过 + vue-tsc exit 0、`web_e2e.sh` PASS。

**留待 owner 的真机/环境步骤**：
- 04-D1 双标签切会话浏览器手工过一遍（执行环境无浏览器，按全局规则 8 记录跳过）。
- 03-C1 Windows 真机验证（本机无 Windows，语义以 os/exec 源码为准绳、单测钉形状——计划已知限制）。
- 01-A5 usage-limit 场景难按需复现，以单测为准（计划内已记）。
- **提交前固定动作别忘**：`scripts/package-graph.sh` 再生成 `pkg/architecture/testdata/graph.json` 并入提交（本期 pkg/** 行数大变）。

**执行期发现、已修**：A6a（rows.Err() 同族：session_store ForkInto、migrate/codex ×3、测试文件 ×6）、A6b（TestCreateRunIsAtomicAcrossStores 饿死型 flake，改测试前提 + t.Fatal 误用）、D4（op 路径终态写入）。

**执行期发现、未动（报备）**：
- `cmd/forebrain/lsp.go:117` 的 `forebrain lsp` 子命令仍用 launch 口径——非 consent 记录路径（显示/探测），与 C2 的两个提示函数不同类；如需对齐另立小任务。
- 既有 lint 风格项（api_extra.go ~2367 nilness 同义反复、executor.go/model.go WriteString 拼接、detect.go scanner.Err 建议[1 MiB 单行上限，属不可达路径的防御]）——基线风格，非缺陷。
- 会话期间磁盘一度 100% 满（ENOSPC 导致两次全量测试链接失败）；reviewer 清了 go 构建缓存（3.1G）后全量转绿。**磁盘余量仍紧张，请 owner 关注。**
- 未跟踪文件 `docs/plan/TUI_BANNER_MASCOT_REDRAW_PLAN.md`（20:15 出现）非本期四份计划产物，未触碰。

## 执行顺序与依赖

- 02（gateway 安全）→ 01（引擎与状态）→ 04（前端）→ 03（LSP）。四份计划互不依赖，此顺序按"小而关键先行"排；02/04 可并行。
- 每份计划完成后各跑自己的验证节；全区间跑一次全量门（见下）。

## 全局规则（每份计划都适用，执行者必须遵守）

1. 不提交代码；改动留工作区，owner 手动审阅提交。
2. 缓存命中率只升不降：任何改动不得改变"对话开始前"的提示前缀（工具表、system、注入在对话前的开发者指令），也不得在会话中途改变它。每份计划的"缓存影响"一节必须落实。
3. 修根因，不打补丁；不为不可达状态写防御代码。
4. 每生产包 ≤20 个生产 .go 文件：**不得新建任何生产 `.go` 文件**，新代码只能写进已有文件；测试文件与所覆盖的生产文件同名。前端同理不新建目录。
5. 包扇出只减不增；`run.Runner` 导出方法数不得增加；`tool.CodeIntelControl`/`CodeIntelligence` 端口接口不得加方法（03-C3 的修法因此选 Manager 侧而不是接口侧）。
6. 双语文案同键写进 `frontend/src/locales/index.ts`（中文前半、英文后半）；界面不显示原始 JSON；运行时给网页的错误带稳定 code，英文句子只作兜底。
7. 不碰 `pkg/gateway/dist`。
8. 涉及模型行为或屏幕呈现的改动，按 `docs/plan/SCHEDULED_RUNS_TRANSCRIPT/README.md` 全局规则 11 做真机证明；做不了的在计划留档处记一句"凭据缺失/场景难复现，已跳过"，不要停下等人。

## 常用验证命令（已在审计时点核实全绿）

| 用途 | 命令 | 成功标志 |
| --- | --- | --- |
| 格式 | `gofmt -l pkg cmd` | 无输出 |
| 静态检查 | `go vet -tags fts5 ./...` | 退出码 0 |
| Go 全量测试 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | 全部 `ok`（审计时点 0 失败；旧"预存失败基线"在本机已不复现，若见失败即新改动引入） |
| 架构约束 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/ -count=1` | `ok`（改完 pkg 后先跑 package-graph.sh 再跑它） |
| 竞态（03-C6） | `go test -race -tags fts5 ./pkg/lsp/ -count=1` | `ok` |
| 前端单测 | `cd frontend && corepack pnpm test` | 全部通过 |
| 前端类型检查 | `cd frontend && corepack pnpm exec vue-tsc --noEmit -p tsconfig.json` | 退出码 0 |
| 网页真机（假模型） | `scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS` |

## 明确不做（本 期）

- 滚动升级的宽限期豁免 / 旧二进制识别，以及**任何形式的升级文档**（E2 裁决：不写；"新旧二进制共用 FOREBRAIN_HOME 时旧进程的运行中回合会被新进程收割器判废弃"作为已知行为接受，只在决策表留档）。
- cron 触发的"不续"机制改造（维持 D7 的 surface 白名单，只给心跳加显式旗标）。
- LSP 前端三组件（`LspTab.vue`、`ProjectLsp.vue`、`LspRecommendationCard.vue`）的逐行深审——本轮只做了 grep 级扫描，列为后续单独切片。
- `ai-elements-vue/.../SchemaDisplayPath.vue:28` 的 `v-html`（MCP schema 元数据进高亮渲染）——基线已有、本分支未触碰该文件，报备不改；若 owner 要修另开计划。
