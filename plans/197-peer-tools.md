# 197 — 出站与工具：`peer_list` / `peer_send`、名字解析、限速、回执汇总

- **基线**：`202ed4f2`（dev）+ 194–196 落地后的工作区。
- **状态**：TODO
- **依赖**：196
- **执行隔离**：in-tree，**零 commit**
- **设计全文**：`docs/plan/CROSS_PROCESS_PEER_COORDINATION_DESIGN.md` §3.7–§3.11、§5.3

> **执行者须知**：逐步执行，每步跑验证；碰到 §9 STOP 条件就停下报告。完成后更新 `plans/README.md` 本行状态。
>
> **Drift check（先跑）**：`git diff --stat 202ed4f2 -- pkg/tool/registry.go pkg/tool/state.go pkg/run/runner.go pkg/run/factory.go pkg/run/subagent.go`，核对 §2 摘录；不符就 STOP。

## 0. 背景与 owner 裁决（全族共用，执行期不得再改）

owner 2026-10-30：线协议与 Claude Code 互通；**给模型的工具只对齐线协议，按 forebrain 风格命名（`peer_list`/`peer_send`），只管跨进程；外加：`peer_send` 发给本进程正在跑的子代理就是插话**；防乒乓只用 Claude Code 那套（出站同样按目标限速）。

**同日三条追加裁决**：①与 Claude Code 互通是存于 `fb_config` 的全局开关 `peers.claude_code`，**默认关闭**——关闭时 `peer_list` 只列 forebrain 代理、`peer_send` 只能发给 forebrain；②forebrain 跨进程通信**自闭环**，不依赖任何其他进程；③检测到本机装了 Claude Code 才显示开关，**打开后只用 Claude Code 的目录与 `cc-socks`，不再碰自己的登记目录**。落到本计划：一个进程任何时刻只有一个登记目录、一个地址（195 的登记模式）；`peer_list`/`peer_send` 只看当前目录（CC 模式下是同样处在 CC 模式的 forebrain + Claude Code 会话），发信一律从当前地址发出；开关不同的进程互相看不见。工具是否注册、工具描述都**不随开关变化**（提示缓存）。设计 §7。

本计划交付两个工具、出站发送、名字解析、回执跟踪与汇总。**不做**：护栏（198）、卡片样式（199/200）。

## 1. 共享架构（摘要）

- `pkg/tool` **不得** import `pkg/peer`（扇出 `current: 9`、已超 target，「may not grow again without the owner」）。工具通过端口 `tool.PeerDirectory` 调用，实现放在 `pkg/process/peer_outbound.go`（新），由组合根经 `run.Deps` → `tool.AgentToolRuntime` 注入——与 LSP 的 `CodeIntel`/`CodeIntelTool` 完全同一条路。
- 工具表在缓存前缀里：是否注册由**每个 runtime 冻结一次**的 `PeerTools bool` 决定，配置变更不改工具表（与 `CodeIntelTool` 同理）。
- 跨进程发送：信封（`from`＝本进程当前的 uds 地址、`from-name`＝发送代理名、`from-mode`＝发送会话模式、`hop-chain`＝继承 + 自身 token）+ user 帧 + forebrain 扩展 `{v:1, from:<发送代理 id>, to:<目标代理 id，仅 forebrain 目标>}`。
- 本进程子代理：不走线协议，按 Claude Code 对进程内代理的做法包成 `<agent-message from="<发送方名字>">…</agent-message>` 插话。

## 2. 现状事实（已对基线核实）

- 工具注册：`pkg/tool/registry.go:168-177`（`if rt != nil && rt.CodeIntel != nil && rt.CodeIntelTool { … registerMeta(tLSP, lspToolDescription, "code", event.ToolMeta{ReadOnly: true, ConcurrencySafe: true}) }`）；跑批判断 `:218-220` `batchProject(rt)`；`AgentToolRuntime` 在 `:311-334`（`CodeIntel CodeIntelligence; CodeIntelTool bool` 在末尾）。
- 运行时依赖：`pkg/run/runner.go:124-136` `Deps` 里的 `CodeIntel`/`CodeIntelControl`/`CodeIntelTool` 及注释「Subagents inherit all three」；`runner.go:1333` 把 `CodeIntelTool` 交给工具运行时；`pkg/run/factory.go:47`、`:197` 子 runner 继承。
- 上下文身份：`tool.ConversationSessionIDFromContext(ctx)`（会话）、`llm.AgentSessionIDFromContext(ctx)`（主代理＝会话 id；子代理＝其 worker 会话 id）、`tool.IsSubagentRunFromContext(ctx)`（`pkg/tool/state.go:512`）。
- 子代理工具名单：`pkg/run/subagent.go:445-456` `subagentToolName`（`subagent_*` 八个名字）；嵌套派发守卫按「是否子代理运行」结构性判断（`guardSubagentTool`，`:496`），**发消息不是派发，不要加进这些名单**。
- 子代理插话：196 新增的 `run.SteerSubagentFromPeer`；正在跑的子代理列表：195 新增的 `run.LiveSubagents`（本计划给它的返回值补 `WorkerSessionID string`）。
- jsonschema 描述的坑：`description=` 里带逗号会被 invopop 按逗号截断（2026-10-29 `pkg/tool/artifacts.go` 修过）——描述一律写在 `jsonschema_description:` 标签里。
- 模型可见文本规则（`FOREBRAIN.md`「LLM-facing prompt text must be provider-neutral and byte-stable」）：工具描述是编译期常量，不含模型/厂商/他人工具品牌名，配 golden 测试。
- 文件数：`pkg/tool` 29 个生产文件（上限 31，本计划新增 1 个）。

## 3. 设计

### 3.1 端口（`pkg/tool/peer_tools.go`，新）

```go
// PeerDirectory is the tools' port onto local peer messaging. The composition
// root implements it; nil when peer messaging is off for this runtime.
type PeerDirectory interface {
    // List describes the agents the calling agent can message.
    List(ctx context.Context) (PeerListing, error)
    // Send delivers message to the agent named by to.
    Send(ctx context.Context, to, message string) (PeerSendResult, error)
}

type PeerListing struct {
    SelfName string
    Subagents []PeerEntry // the caller's own running subagents (primary callers only)
    Peers     []PeerEntry // other processes' agents, same work tree first
}

type PeerEntry struct {
    Name, Ref, Kind, Status, Task, Cwd string
    SameTree bool
    ActiveAgo time.Duration // 0 when unknown
}

type PeerSendResult struct {
    Name   string // the resolved recipient, as printed by peer_list
    Status string // the recipient's status when sent: busy | idle | waiting | ""
    Steered bool  // true when delivered in-process to a running subagent
}
```

（198 会给同一个接口追加护栏方法；本计划只定义这两个。）

- `AgentToolRuntime` 加 `Peers PeerDirectory; PeerTools bool`；`run.Deps` 加同名两字段，`runner.go` 交给工具运行时、`factory.go` 子 runner 继承（照 `CodeIntel`/`CodeIntelTool` 的每一处改法逐行对照）。
- 注册：`if rt != nil && rt.Peers != nil && rt.PeerTools && !batchProject(rt)` 才注册两个工具；`peer_list` 用 `event.ToolMeta{ReadOnly: true, ConcurrencySafe: true}`，`peer_send` 用 `event.ToolMeta{ReadOnly: true}`（与 Claude Code 把纯文本消息视为只读一致；不需要审批，计划模式里可用）。分类目录名 `"session"`。

### 3.2 工具描述（常量，逐字）

```go
const peerListToolDescription = "List the agents you can message with peer_send: your own running subagents, and agent sessions in other processes on this machine (other terminals and the web gateway, plus other coding agents if the user has enabled that). Agents working in the same directory tree as you are listed first, with their status and current task. The name is the address: pass a name to peer_send exactly as printed, including its [ref] when two agents share a name."

const peerSendToolDescription = "Send a plain-text message to another agent by the name peer_list prints. A running agent receives it at its next tool boundary; an idle agent starts a turn to handle it. Use it to coordinate work in a shared directory tree: say which files you are about to change, ask before editing files another agent is working on, and report results someone is waiting for. Write each message so it stands on its own: the recipient cannot see your conversation. Replies arrive as user messages wrapped in cross-session-message tags. Sending to one of your own running subagents steers it."
```

入参结构：

```go
type PeerSendInput struct {
    To      string `json:"to" jsonschema_description:"Recipient name exactly as peer_list prints it."`
    Message string `json:"message" jsonschema_description:"The message text. Self-contained; the recipient does not see your conversation."`
}
type PeerListInput struct{}
```

### 3.3 `peer_list` 输出（模板常量，逐字）

```
This agent is <SelfName> — the name other agents use to message it.

Your running subagents:
  <name> [<ref>] — subagent · <status> · task: <task>

Agents in other processes (same directory tree first):
  <name> [<ref>] — <kind> · <status> · <same tree|other directory> <cwd> · active <N>m ago · task: <task>
```

- `kind` 取值：`terminal`（forebrain TUI）、`gateway conversation`（forebrain 网关会话）、`subagent in another process`、`agent session`（非 forebrain 的对端，即 Claude Code 会话；只在 CC 模式下出现）。
- 空段落整段省略；三段都空时只输出第一行再加一行 `No other agents are reachable right now.`。
- `ref` 只在同名（`NormalizeName` 相同）时打印（与 Claude Code 只在需要时显示 ref 的效果一致；forebrain 本地算法 `peer.Ref`）。
- 子代理调用 `peer_list` 时没有「Your running subagents」段。

### 3.4 名字解析（`peer_outbound.go`）

候选集：本进程（调用方是主代理时）正在跑的子代理 + 当前 `layout.Dir` 里的在线对端记录（排除本进程）：**带扩展块的记录**展开成扩展块里的每个代理（地址＝记录的 `messagingSocketPath`）；**不带扩展块的记录**只在 CC 模式下当候选（即 Claude Code 会话），一条记录一个候选，名字＝记录 `name`，没有就 `slug(basename(cwd))`；FB 模式下目录里不带扩展块的记录一律不当候选。候选集在每次调用时按当前模式重算。

`to` 的匹配顺序：

1. `uds:` 开头 → 解码后必须等于某个候选记录的 `messagingSocketPath`（于是 FB 模式下只能是 forebrain 的 socket），否则 `No agent at <to>. Run peer_list to see who is reachable.`；命中就当进程地址（无扩展寻址）。
2. 形如 `名字 [ref]`（正则 `^(.*\S)\s*\[([0-9a-f]{6,12})\]$`）→ 名字 + ref 都要对上。
3. 否则按 `peer.NormalizeName` 比较；唯一命中即用；多个命中 → 工具错误 `"<to>" matches several agents: <name [ref]>, <name [ref]>. Use the name with its [ref].`；无命中 → `No agent named "<to>". Run peer_list to see who is reachable.`。

### 3.5 发送

- 目标是**本进程正在跑的子代理** → `<agent-message from="<SelfName>">\n<转义后的正文>\n</agent-message>`（转义规则同信封，标签名换成 `agent-message`；在 `pkg/peer` 已有 `EscapeBody` 基础上加一个按标签名参数化的版本——若需改 194 的导出函数，只加不改）作为 `Input{Text, Parts, Origin:"peer"}` 交给 `run.SteerSubagentFromPeer`；`Steered=true`。
- 目标是本进程**已结束**的子代理 → 工具错误 `<name> has finished. Use subagent_continue to give it more work.`。
- 调用方是子代理、目标是**它自己的主代理** → 工具错误 `Your result returns to the agent that dispatched you; use peer_send only for other agents.`。
- 跨进程：
  1. `Pacer.Reserve(目标 socket 规范路径)` 不足 → 工具错误 `Not sent: too many messages to <name> in a short time. Wait a little and send one combined message.`。
  2. hop-chain：取本会话（`ConversationSessionIDFromContext`）最近一条 `origin` 为 `peer` 的 user 行，`peer.Parse` 它的内容拿 `HopChain`（没有就空），`peer.ExtendChain(继承, 自身 token)`——自身 token 用当前地址算出的那个。
  2a. 发信地址：一律是本进程当前地址（195）。开关刚切换时，旧模式里的目标在重算的候选集里自然解析不到（`No agent named …`），不另设错误文本。
  3. `from-mode`：发送会话审批模式为 `ApprovalNever` → `bypass`，否则 `prompting`。
  4. `from-name`：主代理＝记录名（TUI）或会话代理名（gateway）；子代理＝扩展块里它的名字。
  5. 帧：`peer` 包的 user 帧构造器；目标是 forebrain 代理时带 `forebrain.to`。
  6. `peer.Send(socket, frame, SendOptions{NoFollowSymlink: true})`；`ErrPeerGone` → 工具错误 `<name> is no longer running.`；`ErrRefused`/其它 → `Not sent: <原因>`。
  7. 记入出站表（每进程最近 200 条：`msg_id → {发送会话 id, 目标名, 目标地址, 发送时间}`）。
  8. 工具结果：`Sent to <name> (<status>).`；`Steered` 时 `Sent to your running subagent <name>.`。

### 3.6 回执汇总（Claude Code 的做法：汇总成一条 meta 提示交给发送方模型）

- 196 留的 `onReceipt` 钩子在本计划实现：按 `orig_msg_id` 找出站表；找不到就丢弃；找到 → 加入该发送会话的待汇总列表，750ms 防抖后生成一条消息，按 196 §3.2 第 7 步同一张表投递给发送会话（运行中插话、空闲唤醒），`Origin:"peer_status"`。
- 文本（常量模板，逐字；每条一行）：

```
<system-reminder>
Peer message delivery update:
- to <name>: held by the receiving session; it has not seen your message yet. Usually the two sessions run with different permission modes, or the receiving session holds every peer message.
- to <name>: released after approval and delivered.
- to <name>: declined by the recipient's user; it was not delivered.
- to <name>: expired without approval; it was not delivered.
- to <name>: refused (<reason>); it was not delivered.
- to <name>: dropped (<drop reason>); it was not delivered.
</system-reminder>
```

（只输出实际出现的状态行。）

## 4. 步骤

1. **端口与注入**：`pkg/tool/peer_tools.go`（端口、两个工具、描述常量、输出格式化）；`pkg/tool/registry.go` 注册；`AgentToolRuntime`、`run.Deps`、`runner.go`、`factory.go` 加字段并逐处照 `CodeIntel` 传递。golden 测试：两段描述逐字、入参 JSON schema（`pkg/tool` 现有工具 schema golden 的写法）、`peer_list` 三种输出（有子代理+同树对端、只有对端、全空）；断言描述里不含 `Claude`、`Anthropic`、`OpenAI`、`Codex`、`GPT`。用假 `PeerDirectory` 测工具层。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run -count=1` → ok；`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → ok（`pkg/tool`、`pkg/run` 扇出不变、`Runner` 字段数不变——若 `Deps` 加字段导致 `TestRunnerOnlyShrinks` 红，STOP）。
2. **实现**：`pkg/process/peer_outbound.go`（名字解析、发送、出站表、回执汇总）+ `peer_outbound_test.go`；`StartPeers` 把实现装进 `env.Deps`（`Peers`）；`PeerTools`＝`peer.Enabled()` 且配置 `peers.enabled` 未关闭，在每个 runtime 创建时冻结——**不看服务此刻是否在跑**，这样工具表不会随服务启停抖动（提示缓存命中率是最高优先级约束）；服务暂时不在时工具照常注册，调用返回 `Peer messaging is not available right now.`；`run.LiveSubagents` 补 `WorkerSessionID`。测试（两个 `PeerService` 实例跑在同一测试进程里，各自不同 socket，共享覆盖目录）：
   - A `peer_list` 看到 B 的主代理、同树排前；B 的子代理（用假 `LiveSubagents` 源）列为 `subagent in another process`。
   - A `peer_send` 给 B 主代理 → B 的 `Wake` 收到信封，信封 `from-name` 是 A 的名字、`from` 是 A 的地址、帧带 `forebrain.to`。
   - B 处于「已收到 A 一条来信」的会话里回发给 A → 信封 `hop-chain` 含 B 的 token 且继承了 A 那条的链。
   - 同名两个候选 → 歧义错误；带 ref 后成功；不存在的名字 → 错误文本逐字。
   - 第 31 条瞬时发送 → 限速错误。
   - 目标进程退出（stop B）后发送 → `is no longer running.`。
   - B 把来信挂起 → A 的发送会话收到一条 `Origin:"peer_status"` 的 meta 消息，文本含 held 那一行；放行后再收到 delivered 那一行。
   - 发给本进程正在跑的子代理 → `SteerSubagentFromPeer` 收到 `<agent-message from="…">` 包装的文本；已结束的子代理 → 错误文本逐字。
   - **开关关闭**（默认）：在 CC 目录覆盖路径里放一条不带扩展块、指向某个活 `peer.Server` 的记录 → `peer_list` 不列它；`peer_send` 用它的名字 → `No agent named …`；用它的 `uds:` 地址 → `No agent at …`。A、B 两个 forebrain 的全部用例都在开关关闭下通过。
   - **开关打开**（A 的 CC 目录已建，A 处在 CC 模式）：上面那条记录以 `agent session` 列出，发给它的帧与信封 `from` 都是 A 的 CC 地址；仍在 FB 模式的 B **不出现**在 A 的 `peer_list` 里，`peer_send` 用 B 的名字 → `No agent named …`；B 也打开开关、进入 CC 模式后两边重新互相可见，互发成功，帧的 `from` 都在 CC socket 目录。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run Peer -count=1` → ok。
3. **收口**：`go vet ./...`；`bash scripts/package-graph.sh`；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1`。

## 5. 边界

- **只动**：`pkg/tool/peer_tools.go`（新）及测试、`pkg/tool/registry.go`、`pkg/run/runner.go`、`pkg/run/factory.go`、`pkg/run/subagent.go`（`LiveSubagents` 补字段）、`pkg/process/peer_outbound.go`（新）及测试、`pkg/process/peers.go`、`pkg/process/peer_inbound.go`（接 `onReceipt`）、`pkg/peer/envelope.go`（只加按标签名参数化的转义导出函数）、`pkg/architecture/testdata/graph.json`。
- **不动**：`subagent_*` 工具与嵌套守卫名单；审批/权限路径；任何界面文件。
- 不给模型暴露 `notify_when_idle`（owner 裁决 7：工具只对齐 forebrain 需要的部分）。

## 6. 完成判据（机器可查）

1. `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全绿；`go vet ./...` 无输出。
2. `grep -rn 'forebrain-harness/pkg/peer"' pkg/tool pkg/run --include='*.go' | grep -v _test.go` 无输出。
3. `grep -n 'peer_send\|peer_list' pkg/run/subagent.go` 无输出（没被塞进 `subagent_*` 名单）。
4. golden 测试文件存在并包含两段描述的全文。
5. `plans/README.md` 本行状态更新。

## 7. 测试计划

见 §4。工具层 golden 参照 `pkg/tool` 现有工具描述/schema golden 用例（`pkg/tool/testdata`）；进程层用例参照 196 的 `peer_inbound_test.go` 搭法。

## 8. 维护注意

- 工具描述在缓存前缀里，改一个字就让所有会话的提示缓存失效一次；改动须同步 golden。**不要**让工具描述或注册与否随互通开关变化。
- 自闭环：发信不依赖任何 Claude Code 进程。**不要**为了让开关不同的进程互相看见去读另一个目录——owner 裁决打开后不再碰自己的登记目录。
- 名字解析依赖 195 的扩展块；扩展块字段改名要同时改这里和设计 §4.1。
- 回执汇总文本是模型可见的；Claude Code 有自己的措辞（含品牌名，不能照抄），这里是中性改写，语义一一对应它的六种状态。

## 9. STOP 条件

- `run.Deps` 加两个字段使 `TestRunnerOnlyShrinks`（或任何字段计数守卫）变红。
- 工具注册需要在 `pkg/tool` 里 import `pkg/peer` 或 `pkg/process`。
- 发送会话最近一条 peer 行的内容无法 `peer.Parse`（说明 196 落库的不是信封原文）。
- `peer_send` 在计划模式或子代理里被现有权限路径拦截（`ReadOnly: true` 不足以放行）——报告拦截点，不要改权限引擎。

## 追加裁决（owner 2026-10-31）：跑批任务内主代理 ⇄ 子代理直接通信

跑批项目**必须**支持主代理与子代理之间直接通信：主代理派发子代理、给运行中的子代理插话、给子代理发消息，并能用全部子代理相关工具。对本计划的修订：

1. **注册条件**：§（工具注册）里的 `!batchProject(rt)` 删除——跑批会话同样注册 `peer_list` / `peer_send`；但在跑批会话里两工具的**目标只限本会话自己的代理**（主代理与它派出的子代理）：`peer_list` 只列这些，`peer_send` 指向本进程之外的对端一律拒绝（「batch tasks talk only to their own agents」）。跑批仍不接收任何人或其它进程的输入（196 的入站拒收不变）。
2. **主代理给运行中的子代理发消息 = 插话**：走 196 已定的进程内投递（子代理通道的 `queue.Steer`）；子代理空闲时照旧返回「not running」，由主代理改用 `subagent_continue`。
3. **子代理工具**：今天跑批已注册全部 8 个（`subagent_run` / `fanout` / `send` / `status` / `wait` / `continue` / `close` / `list`，`pkg/run/subagent.go:334-348`，只受子代理功能开关约束；派出的子代理继承任务目录与任务上下文，同文件 1287-1293、1538-1543、2092-2097 行）——不改，补跑批场景的测试覆盖全部 8 个。
4. **测试**：跑批任务里主代理 `subagent_send` 派发 → `peer_send` 给运行中的子代理插话被子代理收到；`peer_send` 指向别的进程被拒；`peer_list` 只列本会话代理。

**追加裁决（owner 2026-10-31）：跨进程协同固定开启（always-on）**——不能关闭、不支持关闭、不支持配置；唯一的开关是「与 Claude Code 互通」（`peers.claude_code`，默认关闭，用户可手动打开，只存库，装了 Claude Code 才显示）。本计划受影响：`PeerTools` 只取 `peer.Enabled()`（平台 + 测试隔离），不再看配置 `peers.enabled`；进程内主代理 ⇄ 子代理部分由 202 先交付、与 `peer.Enabled()` 无关（任何平台都有）。

**与 202 的关系（2026-10-31）**：`peer_list` / `peer_send` 两个工具由 202 先在 `pkg/run/peer_tools.go` 交付（进程内：主代理 ⇄ 子代理，含跑批）。本计划改为在其上扩展跨进程目标：工具描述**沿用 202 的字节**、名字规则沿用（子代理＝TaskID、派发者＝`parent`）、`<agent-message>` 包装沿用；改为无条件注册；跑批会话里跨进程目标拒绝。原 §3 里在 `pkg/tool` 新建工具文件与端口的设计须据此改写（工具本体在 `pkg/run`，跨进程目录作为端口注入）。
