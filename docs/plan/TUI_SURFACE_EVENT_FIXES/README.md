# TUI 表面事件修复：roster 点击 + 会话闸门——实施计划索引

由 improve skill 于 2026-10-07 生成。两个缺陷都是 SUBAGENT_CONVERSATION 计划 009 真机验收（2026-10-07，真实模型）顺带发现、由 owner 裁定"必须定位根因并彻底修复"的。计划摘录的代码是 2026-10-07 工作区（HEAD `936df40` + SUBAGENT_CONVERSATION 全套未提交改动）的样子；摘录核对按函数名与注释原文，不按行号。

## 全局规则（两份计划都适用，执行者必须遵守）

1. **不要提交代码。** 所有改动留在工作区，owner 手动审阅提交。不运行 `git commit`/`merge`/`rebase`/`stash`/`checkout`/`restore`。
2. **工作区带着 SUBAGENT_CONVERSATION 全套未提交改动（约 116 个文件）。** 只做计划内的事，绝不"顺手清理"或回退任何既有改动；与计划无关的失败先查是否既有，再决定。
3. **修根因，不打补丁。** 不加掩盖症状的 nil 判断/兜底/recover。
4. **每包 20 个生产文件上限**（`pkg/tui` 已满）：不新建生产 `.go` 文件；测试文件必须与被测生产文件同名。
5. **不碰 `pkg/gateway/dist`**；不运行 `make ui` / `pnpm build`。
6. **真机证明**：涉及屏幕呈现的改动必须在运行中的 TUI 上证明（tmux；假模型场景用 `.claude/skills/run-forebrain/driver.sh`，真模型场景用 `scripts/acceptance/live_subagent.sh`）。真模型密钥只在 `~/.forebrain/e2e-zhipu.env`（`FOREBRAIN_E2E_ZHIPU_KEY`，权限 600），配置里只写 `${FOREBRAIN_E2E_ZHIPU_KEY}` 引用，任何输出里不得出现密钥值。端口被占时 `lsof -ti :<port> | xargs kill -9` 再跑（残留 provider 会让所有 submit 秒失败）。
7. **收尾门禁**：`gofmt -l pkg cmd` 无输出；`go vet -tags fts5 ./...` 退出码 0；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全 `ok`；`./pkg/architecture/` `ok`；死代码与 `/tmp/deadcode-before.txt` 按符号名一致（行号位移不算）。用后清理 tmux 会话与临时目录。

## 执行顺序与状态

| 计划 | 标题 | 优先级 | 工作量 | 依赖 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 001 | [roster 行可点击打开对应视图](001-roster-row-click-opens-view.md) | P1 | S | — | REJECTED（owner 2026-10-07 裁定功能多余：↓+Enter 已可进入选中 subagent 的视图，不需要鼠标点击路径；执行者留在工作区的实现与测试已摘除。另：计划前提本身有误——生产纯点击走 `ViewportSelectEnd` 而非 `ViewportClickToggle`，`inputEventMouseClick` 无生产者） |
| 002 | [UI 事件按会话闸门](002-session-gated-ui-events.md) | P1 | M | —（与 001 建议串行：同改 `pkg/tui/reducer.go`，函数不相交） | DONE（含两轮评审裁定的计划修订：①主 turn 工具漏斗直通路径——原计划把 publishRunEvent 之外的直通 notifyUI 全归"自身动作"系误分类，`runAuditStepHook` 的工具卡/plan 卡直画与 subagent 兜底分支同为带会话的漏斗；②主 turn live 增量直通——`prepareTUIAgentBase` 的 stream sink 六回调与 `appendAssistantOutcome` 非流式收尾，主代理 live 增量不经事件漏斗、唯一路径就是这些直通点，一并闸门） |

状态取值：TODO | IN PROGRESS | DONE | BLOCKED（附一句原因）| REJECTED（附一句理由）

## 缺陷来源与证据

- 两个缺陷都在 2026-10-07 计划 009 真机验收中发现并报告：roster 行点击无响应（卡片任务行可点）；`notifyUI` 不过滤跨会话事件（回收器/异步 subagent 会在别的会话屏幕上画幽灵卡）。
- 009 验收的证据目录（只读）：`/tmp/fb009-live/evidence`（验收）、`/tmp/fb009-fix2/evidence`（T23 孤儿卡机制的同类现场）。

## 考虑过但不做的

- **给 roster 行加悬停高亮**：视口行的悬停机制属另一套，超出本缺陷；不加。
- **把闸门做进 reducer（给每条 UI 消息加 SessionID）**：侵入全部消息类型；权威会话在 UI 侧而消息从 ChatSession 侧发出，在漏斗源头（`publishRunEvent`）拦是唯一不动几十处调用点的位置。
- **gateway 侧同类闸门**：gateway 的事件总线已按会话分发（`TestRunEventBusDeliversOnlyToTheBoundSession`），无此缺陷。
