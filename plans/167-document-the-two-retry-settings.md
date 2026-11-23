# Plan 167：把两个新配置项写进文档站（英/中双语）

> **Addendum（2026-10-27 执行批）**：本计划"文档站在本仓里"的前提**不成立**——`forebrain-harness.github.io/`
> 是主仓 `.gitignore:59` 忽略的**嵌套独立 git 仓**（分支 `main`，非子模块），主仓 worktree 无法物化这些文件。
> 执行地点改为**主树的嵌套文档仓工作树**（owner 先例：DeepSeek 批次"在文档仓把这两段落进去"——落工作树、
> **不 commit**，owner 亲自审阅提交；嵌套仓现有 8 个 owner 未提交文件，只许插入、绝不触碰既有内容）。
> 嵌套仓内锚点已核实：两页 `## agents.defaults.sourcegraph` 各为 1、全站无 `network_retry_max` 既有提及。
> 英文页结构：`…web_fetch` → `sourcegraph`（:355）→ `## Related`（:363）——新节插在 :362 之前。

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**：
> `grep -c "NetworkRetryMax \*int" pkg/config/agents.go` 为 `1`；
> `grep -c "StreamIdleTimeoutSeconds \*int" pkg/config/agents.go` 为 `1`；
> `grep -c '^## .agents.defaults.sourcegraph.' forebrain-harness.github.io/docs/config/agents-and-models.md` 为 `1`；
> 同一条 grep 在 `forebrain-harness.github.io/docs/zh/config/agents-and-models.md` 为 `1`。
> （用单引号包 grep 的模式：这些标题里带反引号，双引号会让 shell 去做命令替换。）
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- forebrain-harness.github.io/docs/config/agents-and-models.md forebrain-harness.github.io/docs/zh/config/agents-and-models.md` 必须**无输出**。

## Status

- **Priority**: P2（用户可配但没有一处文档提到它们）
- **Effort**: S（两个页面各加一节；含中文镜像）
- **Risk**: LOW（纯文档）
- **Depends on**: 无（与 `plans/156` 无关：156 改行为，本计划只写现状）
- **Blocks**: 无
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: docs

## Why this matters

本分支加了两个**用户可配**的开关，它们都能改变"卡住时会发生什么"，但文档站里一个字都没有：

- `agents.defaults.network_retry_max`（默认 10，0 关闭，上限 15）；
- `agents.defaults.stream_idle_timeout_seconds`（默认 300，0 关闭看门狗，clamp 到 [10, 1800]）。

用户遇到"弱网下重试很久""思考型模型被掐断"这类问题时，唯一能自助的手段就是这两个键；
没有文档，他们只能读源码或者来问。而且它们的语义**不是自明的**：0 表示"关闭"，越界的值会被 clamp 而不是报错，
`network_retry_max` 还是**整请求重发**（不是续答）——这些都必须写清楚。

文档站在本仓里：`forebrain-harness.github.io/docs/config/agents-and-models.md`（英）
与 `forebrain-harness.github.io/docs/zh/config/agents-and-models.md`（中）。
**注意**：`FOREBRAIN.md:86` 把文档站写成 `docs/config/...`（相对本仓根），那条路径不存在——那是另一个小漂移，
不在本计划范围内（见 Maintenance notes）。

## Current state

字段与语义（`pkg/config/agents.go:118-140`）：

```go
	// NetworkRetryMax caps LLM transport-failure retries (default 10). Nil
	// means the default; 0 disables network retries; load rejects negative
	// values; values above MaxNetworkRetryMax run at MaxNetworkRetryMax.
	NetworkRetryMax *int `yaml:"network_retry_max,omitempty" json:"network_retry_max,omitempty"`
	// StreamIdleTimeoutSeconds bounds how long a streaming LLM request may
	// stay silent — no response headers, or no new data — before it is
	// treated as a dropped connection and handed to the network retry layer.
	// Nil means the default 300; 0 disables the watchdog; load rejects
	// negative values; other values clamp to the [10, 1800] range.
	StreamIdleTimeoutSeconds *int `yaml:"stream_idle_timeout_seconds,omitempty" json:"stream_idle_timeout_seconds,omitempty"`
```

常量与解析（同文件 `:130-175`）：`DefaultNetworkRetryMax = 10`、`MaxNetworkRetryMax = 15`、
`DefaultStreamIdleTimeoutSeconds = 300`、`MinStreamIdleTimeoutSeconds = 10`、`MaxStreamIdleTimeoutSeconds = 1800`；
`NetworkRetryMaxOrDefault()` 与 `StreamIdleTimeout()` 是唯一的解析入口。

文档站结构（英/中同构）：`## \`agents\`` → `## \`agents.definitions\`` → `## \`llm_providers\`` →
`## \`agents.defaults\`` → 各子节（`## \`memory\``、`## \`agents.defaults.context_inject\``、…、
`## \`agents.defaults.sourcegraph\`` → `## 相关页面`（中）/ 英文对应收尾节）。
新节插在 `## \`agents.defaults.sourcegraph\`` 之后、收尾节之前。

## Scope

**In scope（只改这些）**：
- `forebrain-harness.github.io/docs/config/agents-and-models.md`（英文，新增一节）
- `forebrain-harness.github.io/docs/zh/config/agents-and-models.md`（中文镜像，同一节）

**Out of scope（看着相关也不要动）**：
- 任何代码（`pkg/config`、`pkg/llm`、`pkg/run`）——本计划只记录现状
- `FOREBRAIN.md:86` 的 `docs/config/...` 路径漂移（另一个缺陷，见 Maintenance notes）
- 文档站的导航/侧边栏配置（没有新增页面，只加一节）
- `README.md` 的 Configuration 段（它只指向文档站，不需要列举键名）
- 其它页面（`runtime-gateway-and-channels.md` 等）
- `plans/156` 的行为改动——本计划描述的是**今天**的语义；156 落地后语义变成"降级只许一次"，
  届时本节的措辞若涉及降级需要复核（见 Maintenance notes）

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 键名出现（英） | `grep -c "network_retry_max" forebrain-harness.github.io/docs/config/agents-and-models.md` | `2`（表格行 + 正文至少各一，见 Done criteria） |
| 键名出现（中） | 同上，路径换 `docs/zh/` | `2` |
| 中文镜像成对 | `diff <(grep -o "^## .*" a.md) <(grep -o "^## .*" b.md)` | 只差中文副标题的措辞；**新增的节两边都必须有** |
| Markdown 链接 | 无命令；人工看一遍新节里的行内代码与反引号成对 | — |

## Steps

### Step 1：写英文节

在 `forebrain-harness.github.io/docs/config/agents-and-models.md` 的 `## \`agents.defaults.sourcegraph\``
一节之后、收尾节之前插入：

```markdown
## `agents.defaults.network_retry_max` and `agents.defaults.stream_idle_timeout_seconds`

These two keys decide what happens when a model request stops making progress. Both
belong to `agents.defaults`.

| Field | Type | Default | Usage |
| --- | --- | --- | --- |
| `network_retry_max` | integer | `10` | How many times a failed model request is re-sent. `0` turns network retries off; negative values are refused at load; anything above `15` runs at `15`. |
| `stream_idle_timeout_seconds` | integer | `300` | How long a streaming request may stay silent — no response headers, or no new data — before it is treated as a dropped connection. `0` turns the watchdog off; negative values are refused at load; other values are clamped to between `10` and `1800` seconds. |

A retry **re-sends the whole request**; it does not resume the interrupted stream. An
answer that was already complete is kept rather than repeated, and a request whose tools
already ran is never sent again — the state file the run keeps is what makes both true.

Lower `stream_idle_timeout_seconds` if a stuck turn should end faster. Do not go far
below the default for reasoning models: a model that thinks for a long time sends nothing
while it does, and a window shorter than its thinking time will cut it off and resend the
request.

```yaml
agents:
  defaults:
    network_retry_max: 5
    stream_idle_timeout_seconds: 120
```
```

（最后那个 yaml 代码块也在本节内。若英文页已有 `yaml` 代码块的写法惯例，跟着它。）

### Step 2：写中文镜像

在 `forebrain-harness.github.io/docs/zh/config/agents-and-models.md` 的
`## \`agents.defaults.sourcegraph\`` 之后插入同一节的中文版，**表格四列的语义与数字必须与英文逐项一致**：

```markdown
## `agents.defaults.network_retry_max` 与 `agents.defaults.stream_idle_timeout_seconds`

这两个键决定"模型请求不再前进时会发生什么"，都属于 `agents.defaults`。

| 字段 | 类型 | 默认 | 用途 |
| --- | --- | --- | --- |
| `network_retry_max` | integer | `10` | 失败模型请求的重发次数。`0` 关闭网络重试；负数在加载时被拒；大于 `15` 按 `15` 运行。 |
| `stream_idle_timeout_seconds` | integer | `300` | 流式请求可以静默多久——没有响应头，或没有新数据——之后被视为掉线。`0` 关闭看门狗；负数在加载时被拒；其它值被限制在 `10` 到 `1800` 秒之间。 |

重试是**整请求重发**，不会接着被打断的那段流往下写。已经完整到达的回答会被保留而不是重说一遍，
已经跑过工具调用的请求也不会再发一次——运行中落盘的状态让这两件事都成立。

想让卡住的回合更快结束就调小 `stream_idle_timeout_seconds`；但对思考型模型不要远低于默认值：
模型思考期间不会发送任何数据，窗口短于它的思考时间就会把它掐断并重发请求。

```yaml
agents:
  defaults:
    network_retry_max: 5
    stream_idle_timeout_seconds: 120
```
```

**Verify**：
`grep -c "network_retry_max" forebrain-harness.github.io/docs/config/agents-and-models.md` ≥ 2；
`grep -c "network_retry_max" forebrain-harness.github.io/docs/zh/config/agents-and-models.md` ≥ 2；
两个页面各含 `stream_idle_timeout_seconds` ≥ 2。

### Step 3：对照检查

逐项核对（把结果写进报告）：

1. 默认值：`10` / `300`；
2. 边界：`0` 关闭、负数被拒、`network_retry_max` 上限 15、`stream_idle_timeout_seconds` clamp 到 [10, 1800]；
3. "整请求重发"这句话在两边都在；
4. 中文页没有新增英文以外的术语漂移（「看门狗」在本计划写作时**全站没有先例**——`grep -rn "看门狗" forebrain-harness.github.io/docs/zh/` 为空；
   若执行时仍无先例，就用「看门狗」并在报告里注明是新译，或改用更直白的「闲置超时」，二选一并说明）。

**Verify**：无命令；报告里逐条打勾。

## Test plan

纯文档，无测试。核对手段是上面的 grep 与人工逐项对照。

## Done criteria

机器可检查，全部满足：

- [ ] `grep -c "network_retry_max" forebrain-harness.github.io/docs/config/agents-and-models.md` ≥ `2`
- [ ] `grep -c "stream_idle_timeout_seconds" forebrain-harness.github.io/docs/config/agents-and-models.md` ≥ `2`
- [ ] 两个键在中文页同样各 ≥ `2`
- [ ] `grep -c "整请求重发" forebrain-harness.github.io/docs/zh/config/agents-and-models.md` 为 `1`
- [ ] `grep -c "re-sends the whole request" forebrain-harness.github.io/docs/config/agents-and-models.md` 为 `1`
- [ ] 两个页面新增的节名一致：`grep -c '^## .agents.defaults.network_retry_max.' forebrain-harness.github.io/docs/config/agents-and-models.md` 与同一条对 `docs/zh/` 的路径，各为 `1`（单引号：标题里带反引号）
- [ ] `git status --porcelain` 只显示 Scope 里的两个文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的任一 grep 计数与标注不符，或 drift check 有输出且"Current state"摘录与现场代码对不上。
2. 两个页面里 `## \`agents.defaults.sourcegraph\`` 不是**最后一节**（后面的收尾节换了名字）——按实际结构插在最后一节之前，
   并在报告里说明插在了哪里。
3. 你发现文档站里已经有人在别处写过这两个键（`grep -rn "network_retry_max" forebrain-harness.github.io/`）：
   那就改成补充/合并，不要写第二份。
- 中文页的既有术语与本节不一致（例如把 watchdog 译成别的）：跟随该页既有译法，并在报告里说明。
- 你想顺手改 `FOREBRAIN.md:86` 的路径漂移：**不要**（见下）。

## Maintenance notes

- `plans/156` 落地后，"非流式回退/降级"的措辞需要复核一次：156 之后每次降级只发生一次，
  本节若描述"重试会盲目等多久"就要跟着改。本计划写的是**今天**的事实。
- 已知漂移（不在本计划）：`FOREBRAIN.md:86` 说项目级 MCP 文档在 `docs/config/projects-and-mcp.md`（中文镜像
  `docs/zh/config/`），而本仓里这些文件在 `forebrain-harness.github.io/docs/config/` 下。修法是改那一行路径，
  或者让 `docs/` 指向站点——这是文档结构决策，需 owner 拍板，故不塞进本计划。
- 新增的可配置键都应问一句"文档站写了没有"：`forebrain-harness.github.io/docs/config/agents-and-models.md`
  是 `agents.defaults` 的唯一权威说明页。
