# 跨进程同树协同（对等代理通信）设计

- 日期：2026-10-30
- 基线：`dev` / `202ed4f2`（工作区另有并行会话的未提交改动，见各计划 §0）
- 实施计划：`plans/194`–`plans/201`（索引 `plans/README.md`）
- 协议对标：Claude Code 2.1.301 的本机对等消息协议（`peerProtocol: 1`），本文 §3 是从其安装包内嵌 JS 逆向核实的参考规范

## 0. 背景与 owner 裁决

问题：用户同时开多个 forebrain TUI（以及 gateway），在同一仓库主树、同一分支上并行做代码任务。今天这些进程彼此看不见：一个代理的 `go test` 把另一个代理改到一半的文件当成自己的失败去"修"，两个代理重复做同一件事。plan 186 只修了「shell 卡把别人的写入算成自己的」这一个症状。破坏性 git 操作（`git stash`、`git checkout -- .`、`git commit -a`）会冲掉或卷走别人的未提交改动，这类协调**由同树的代理之间用 `peer_send` 自主沟通**（owner 2026-11-05 裁决），运行时不设闸门。

owner 裁决（2026-10-30，两轮提问 + 两条追问 + 同日追加裁决 11、12、13，执行期不得再改）：

| # | 问题 | 裁决 |
|---|---|---|
| 1 | 协同范围 | **按工作树，跨 home**：同一工作树里的进程不论 `FOREBRAIN_HOME` 是否相同都互相可见、可互发消息 |
| 2 | 收件方是空闲的主代理 | **自动唤醒开一轮**；正在跑的收件方一律按插话在下一个工具边界送达 |
| 3 | 防冲突护栏 | **提醒 + 写入提示 + 失败归因 + 自主沟通**：每轮同树概况提醒；写别人近期改过的文件时结果里附提示；命令失败时附上执行期间别的代理改过的文件。同树 git 协调靠代理之间 `peer_send` 自主沟通（owner 2026-11-05 裁决取代本条旧裁决中要求审批的那半句：`--yolo` / 免审批会话里不再为同树 git 命令弹审批，运行时不设 git 闸门） |
| 4 | 对标 | **严格对齐 Claude Code，线协议与 Claude Code 互通**：forebrain 与 Claude Code 会话之间能互发消息（**仅在用户打开互通开关后**，裁决 11；关闭时 forebrain 之间仍用同一套线协议） |
| 5 | 网关收件 | Claude Code 发来、不带 forebrain 扩展寻址的消息，**按发送方工作树路由**到网关里同树最近活跃的会话；找不到就回 refused 并说明原因 |
| 6 | 会合目录 | **只在 `~/.claude/sessions`（或 `$CLAUDE_CONFIG_DIR/sessions`）已存在时往里写**，从不替它创建。原先的「forebrain 自己的目录始终登记、读两处」已被裁决 13 取代：一个进程任何时刻只用一个目录（§7） |
| 7 | 给模型的工具 | **只对齐线协议**；工具按 forebrain 风格命名（`peer_list` / `peer_send`），只管跨进程；**外加：`peer_send` 发给本进程正在跑的子代理就是插话** |
| 8 | 防乒乓护栏 | **只用 Claude Code 那套**（按发送方限速、重复丢弃、hop-chain 防环、队列上限、权限模式不一致时挂起等用户放行）；不加我先前提的「每小时唤醒上限」 |
| 9 | 装了 Claude Code 之后 forebrain 自己的目录还用吗（追问） | 见 §7.3：装上后设置里才出现开关；**用户打开后改用 Claude Code 的目录，不再用自己的**（裁决 13）；登记随目录存在与否**运行中自动跟进** |
| 10 | 之后又卸载 Claude Code 呢（追问） | 见 §7.3：目录还在就继续用；目录被删则开关从设置里隐藏、进程自动退回自己的目录；**forebrain 永不创建 Claude Code 的目录** |
| 11 | 与 Claude Code 互通要不要开关（追加） | **加全局开关，默认关闭，用户手动打开**。开关存在状态库 `fb_config` 表 `peers` 行的 `claude_code` 字段，**只读写数据库，不经 forebrain.yaml 配置**（文件里写了也不导入、不生效）。关闭时 forebrain **只登记、只读取自己的目录**，socket 放 forebrain 自己的命名空间，不碰 `~/.claude/sessions` 与 `cc-socks`（设置界面判断要不要显示开关时 stat 一次除外，裁决 13），**只和其他 forebrain 进程通信**；打开后才按裁决 4、6、9、10、13 与 Claude Code 互通。见 §7 |
| 12 | forebrain 之间的通信依赖谁（追加） | **自闭环**：forebrain 跨进程通信不依赖 Claude Code 进程（跑没跑都一样），也不依赖其他任何进程——没有中心进程（无 broker/daemon），每个进程自己监听、点对点直连，登记是普通文件，不调用外部命令。开关打开后借用 Claude Code 的目录与 socket 命名空间（裁决 13），那个目录不在时自动退回自己的目录。见 §7.1 |
| 13 | 开关何时显示、打开后用哪个目录（追加） | **检测到本机装了 Claude Code 才在 TUI 与网页显示这个开关，没装就不显示**（库里已存的值保留，但不生效）。**用户打开后 forebrain 只登记、只读取 `~/.claude/sessions`（socket 放 `cc-socks`），不再碰自己的登记目录**；关闭时只用自己的目录。一个进程任何时刻只在一个目录登记、只开一个 socket，开关切换或 Claude Code 目录出现/消失时整体换过去。直接后果：**开关不同的 forebrain 进程互相看不见**（开关旁一行提示）。见 §7 |

与既有裁决的关系：

- 「一端正在跑某会话时另一端发消息→拒绝、不做跨进程排队」（all-session-data-in-db 第 6 条）**只管用户亲手发的消息，不变**。代理之间的对等消息按裁决 2 排队/插话/唤醒，这是 owner 本轮明确选择的新语义。
- 「不做跨端实时推送」管的是 UI 镜像；对等投递是进程把消息交给自己的代理，不是 UI 同步，不冲突。
- 「所有会话数据落库」：**已送达**的对等消息作为收件会话的 user 行落库（`is_meta` + `origin: peer`，不改表结构）。被挂起尚未放行的消息只在收件进程内存里（与 Claude Code 一致），进程退出即回 `expired` 回执。
- 「只允许 primary→subagent 一层」：发消息不是派发，不触碰嵌套守卫；子代理也能用 `peer_list`/`peer_send`。

## 1. 术语

- **对端（peer）**：本机上另一个参与协议的进程——forebrain TUI、forebrain gateway；互通开关打开时还有 Claude Code 会话。协议的寻址单位是**进程**（一个进程一个信箱）。
- **代理（agent）**：forebrain 进程内可被单独寻址的主代理（一个会话的主对话）或子代理。forebrain 之间靠扩展字段寻址到代理；Claude Code 只看得到进程。
- **工作树（work tree）**：代理的会话工作目录（TUI＝启动目录，gateway 会话＝项目根）。两个代理「同树」＝一方的目录等于、包含或被包含于另一方的目录（路径包含，不找 git 根，与「项目键按项目根本身」一致）。例外（owner 2026-11-09）：两者之间隔着一个 linked git worktree 的根（agent 统一建在仓库主检出的 `.worktrees/<name>`）时不算同树，两个方向都一样；submodule 仍算同树。台账读取（失败归因、同树提醒的近期文件、网页「谁在编辑」面板）用同一判定（`pkg/process` 的 `workTree`）。
- **会合目录（registry dir）**：登记记录与 key 文件所在目录。CC 目录＝`$CLAUDE_CONFIG_DIR/sessions` 或 `~/.claude/sessions`；FB 目录＝`os.UserCacheDir()/forebrain/peers`。
- **socket 命名空间**：FB＝`$XDG_RUNTIME_DIR/fb-socks`（未设时 `/tmp/fb-socks-<uid>`）；CC＝§3.4 的 `cc-socks`。
- **互通开关**：`fb_config` 表 `peers` 行的 `claude_code` 字段（裁决 11）。只在检测到 Claude Code 时显示（裁决 13）。
- **检测到 Claude Code**：CC 目录存在且是目录（只 `os.Stat`，不读、不写、不建）。
- **登记模式**：**FB 模式**（默认）＝FB 目录 + `fb-socks` 里的 socket；**CC 模式**＝CC 目录 + `cc-socks` 里的 socket，条件是开关打开**且**检测到 Claude Code（裁决 13）。一个进程任何时刻只处在一种模式、只有一条登记、只监听一个 socket。
- **信封（envelope）**：`<cross-session-message …>正文</cross-session-message>`，由发送方生成、原样交给收件方模型。

## 2. 总体架构

```
            ┌──────────────── 机器（同一用户） ────────────────┐
            │ FB 模式（默认）：FB 目录 + fb-socks                 │
            │ CC 模式（开关打开且装了 Claude Code）：CC 目录 + cc-socks │
            │   <pid>.json  登记记录（forebrain 的带扩展块）        │
            │   <pid>.<sha256(sock)>.key  peerToken                │
            └────────────────────────────────────────────────┘
  forebrain TUI ◄──uds NDJSON──► forebrain gateway        （同一模式之间）
  forebrain（CC 模式）◄──uds NDJSON──► Claude Code 会话
   pkg/peer（协议库）      pkg/process（组装、路由、投递）
   pkg/tool 端口（peer_list / peer_send / 同树护栏）
```

分层：

- `pkg/peer`（新，194）：纯协议库——登记、key、socket、存活判定、帧、信封、hop-chain、入站准入、收发端。只依赖标准库、`golang.org/x/sys`、`github.com/google/uuid`。
- `pkg/process`（195/196/197/198）：组合根。进程级 `PeerService`（登记、状态、服务端、两种登记模式的切换）、入站路由与投递、出站发送、同树查询。TUI 与 gateway **共用同一份实现**，各自只提供「唤醒一轮」端口（TUI 走自己的事件循环，gateway 走 `startDetachedTurn`）。
- `pkg/run`（196）：插话投递带 origin；子代理插话入口。
- `pkg/tool`（197/198）：`PeerDirectory` 端口 + 两个工具 + 同树护栏的工具侧钩子（写入提示、失败归因、身份台账）。
- `pkg/tui`（199）、`pkg/gateway` + `frontend`（200，先原型）：呈现。

## 3. Claude Code 对等协议参考（peerProtocol 1，2.1.301 逆向核实）

核实方法：读取 `@anthropic-ai/claude-code/bin/claude.exe`（bun 打包，JS 以文本内嵌）中 `[uds-messaging]`、`[uds-client]`、`[concurrentSessions]`、`[peer-guard]`、`[cross-session-inbound]` 模块的源码，并对照本机运行中会话的真实登记文件。标【已核实】的条目逐字来自源码或真实文件；标【推断】的在 201 真机验收里核实，与本文不符时以真机为准、回写本文。

### 3.1 会合目录【已核实】

- 目录＝`join(<Claude 配置目录>, "sessions")`。Claude 配置目录默认 `~/.claude`；`CLAUDE_CONFIG_DIR` 覆盖它【推断】。
- 目录权限 0700（`mode:448`），登记文件 0644（真实文件 `-rw-r--r--`），key 文件 0600（`mode:384`）。
- 读者只认文件名 `^\d+\.json$` 且数字是规范十进制 pid 的文件；单文件上限 262144 字节（`Bht`）。

### 3.2 登记记录 `<pid>.json`【已核实】

Claude Code 自己写的字段（`xG()`）：`pid, sessionId, cwd, startedAt(ms), procStart, version, peerProtocol(=1), peerFeatures, kind, entrypoint, hostSessionId?, pidDomain, tmux?, messagingSocketPath, name, nameSource, nameSince(ms), logPath?, agent?, jobId?, spare?`；之后由 `wn()` 合并更新 `status, updatedAt, statusUpdatedAt, name, nameSource, formerNames, bridgeSessionId, parkedJobId`。

读者（`j()`）只取已知字段，**未知字段一律忽略**——forebrain 的扩展块因此安全。取值约束：

- `kind ∈ {interactive, bg, daemon, daemon-worker}`，其它值读成 undefined。
- `status ∈ {busy, shell, idle, waiting}`。
- `nameSource ∈ {user, peer, derived, collision, auto, hook}`。
- `peerFeatures`：元素须匹配 `^[a-z0-9_]{1,32}$`，最多 16 个。2.1.301 自己发布 `notify_idle, reply_across_default_dirs, artifact_yield`。
- `procStart`：`LC_ALL=C TZ=UTC ps -o lstart= -p <pid>` 的输出去首尾空白（如 `Fri Oct 30 06:47:30 2026`）。forebrain 不跑 `ps`，在进程内算出同一字符串（§7.1）。Windows 用 `procStartFt`（不在本期范围）。
- `pidDomain`：macOS 为字面量 `darwin`；Linux/WSL 为 `darwin:<trim(/etc/machine-id)>:<readlink /proc/self/ns/pid>`（前缀字面量就是 `darwin`）；Windows 为 `darwin:<小写主机名>`。读者对 `pidDomain` 不同的记录一律视为在线（不做 pid 检查）。
- 名字派生（`Wl`）：`<slug(basename(cwd)) 或 "claude">-<十六进制>`，十六进制是 1 字节随机数（或稳定地址特性开启时取会话 id 的 sha256 派生）。名字唯一性：派生名被在线记录占用时换后缀。

### 3.3 key 文件【已核实】

- 文件名 `<pid>.<sha256hex(规范 socket 路径)>.key`；规范路径＝`path.resolve(socketPath)`；正则 `^(\d+)\.[0-9a-f]{64}\.key$`。原子写（临时名 `<pid>.<hash>.key.tmp.<hex>`）。
- 内容 JSON：`{"peerToken":"<32 位小写十六进制>","procStart":"…","pidDomain":"…"}`（可选 `procStartFt`），上限 4096 字节。
- 发送方按目标 socket 路径算哈希找 key；多个候选时取 pid 存活且 procStart 吻合的那个。

### 3.4 socket 路径与命名空间【已核实】

- 默认路径：`resolve(<XDG_RUNTIME_DIR 或 CLAUDE_CODE_TMPDIR 或 "/tmp">/cc-socks/<pid>.sock)`；字节长度超过 103 时改用 `/tmp/cc-socks-<uid>/<pid>.sock`。
- 目录 0700 且须过校验：是目录（不是符号链接）、属主是本用户或 root、组/其他可写时必须带 sticky 位。
- 监听后 `chmod 0600` socket；`EADDRINUSE` 时先探活（250ms 内能连上即活），死的就删掉重绑，活的就改用旁置名 `<pid>-<8 位十六进制>.sock`（最多 3 次），并清理已死的旁置兄弟。
- **回执/空闲通知目标的命名空间校验**（`FMo`/`D`）：回执只发往「与自己 socket 同目录、以 `.sock` 结尾」的地址；对端发布了 `reply_across_default_dirs` 时，另允许默认目录族 `/tmp/cc-socks[-<uid>]`、`/private/tmp/cc-socks[-<uid>]`、`/run/user/<uid>/cc-socks`，且文件名匹配 `^(\d+(-[0-9a-f]{8})?|[0-9a-f]{1,16})\.sock$`、uid 属于本用户。**结论：CC 模式下 forebrain 的 socket 必须放在这个命名空间，否则 Claude Code 不会给它回执；FB 模式用 `fb-socks`（§7）。**

### 3.5 连接、鉴权、对端身份【已核实】

- 传输：Unix domain socket，UTF-8 NDJSON（一行一个 JSON）。单行上限 1,048,576 字符（`ann`）。连接建立后 30,000ms 内（`Vnr`）必须收到一整行，否则关闭。
- 鉴权：首行可以是 `{"type":"auth","token":"<32hex>"}`。与自己的 `peerToken`（或给子进程的 `childToken`）常数时间比较。**只有 Windows 强制鉴权**（`authRequired = M4n()`＝windows）；macOS/Linux 上没鉴权、鉴权错都照常处理后续行。
- 发送方：找得到 key 就先写鉴权行；拨号超时 5,000ms；回执类发送拒绝符号链接目标；给了期望 pid 时连上后用对端凭据核对 pid 与 uid，不符拒写。
- 处理顺序：除 `notify_when_idle` 外的 control 帧、以及 `priority:"now"` 且无附件的 user 帧立即处理；其余按到达顺序串行处理。

### 3.6 帧【已核实】

所有帧都是一个 JSON 对象，带 `type`。

**user 帧（发送方 `cnn()` 生成）**

```json
{"msgV":1,"msg_id":"<uuid v4>","type":"user",
 "message":{"role":"user","content":"<信封全文>"},
 "priority":"next","from":"uds:<百分号编码的自身 socket 路径>"}
```

可选 `from_plugin`、`file_attachments`（forebrain 不用）。收件方：`message.content` 必须是非空字符串；若帧带 `session_id` 且不等于自己当前会话 id，丢弃；`priority ∈ {now, next, later}`，缺省 `next`；入队为 `isMeta: true`、`skipSlashCommands: true` 的提示，origin 记 `{kind:"peer", from, verifiedPeerPid, msg_id, name, hopChain, fromMode}`（后几项从信封解析）。**收件方空闲时，队列里的提示会自动开一轮**——这就是裁决 2 的「自动唤醒」。

**control 帧**＝`{"type":"control", ...负载, "msgV":1, "msg_id":"<uuid>"}`，`action` 取值：

| action | 负载 | 说明 |
|---|---|---|
| `rename` | `name` | 对端要求改名（forebrain：忽略并记日志） |
| `peer_message_status` | `status, reason, from, orig_msg_id?, status_detail?, drop_reason?, dropped_msg_ids?` | 回执，见 §3.11 |
| `notify_when_idle` | `from, msg_id, from_mode?` | 订阅「对方空闲时通知我」 |
| `peer_idle_notice` | `orig_msg_id, state, finished_at?, detail?, from?, from_mode?` | 空闲通知；`state` 已见 `idle`、`exited`、`unavailable` |
| `yield_artifact_replies` / `unyield_artifact_replies` / `artifact_replies_yielded` | — | claude.ai artifact 专用，forebrain 不实现、不发布对应特性 |

### 3.7 信封【已核实】

```
<cross-session-message{ from="…"}{ from-session="…"}{ hop-chain="…"}{ from-name="…"}{ from-mode="…"}{ from-plugin="…"}>
<转义后的正文>
</cross-session-message>
```

- 属性按上面固定顺序、各自前置一个空格、缺省即省略：`from`（地址，字符集 `[A-Za-z0-9%:_/.\-]`，≤300）；`from-session`（`^[A-Za-z0-9_-]{1,80}$`，forebrain 不填）；`hop-chain`（逗号分隔的 24 位小写十六进制，≤32 个）；`from-name`（去掉 `"<>`、控制/格式字符，超过 64 字符截断加 `…`）；`from-mode`（`bypass`｜`prompting`）；`from-plugin`（forebrain 不填）。
- 正文转义：任何能拼出闭合标签 `</cross-session-message` 的 `<`（不区分大小写，`-`/`_` 互换，容忍零宽/填充字符与易混字形）后面插入一个反斜杠，变成 `<\`。
- 收件方解析用整串正则，并**重新渲染比对**；对不上就当作不可信纯文本处理。
- 地址编码：`uds:` + 路径，路径里 `[A-Za-z0-9:_/.\-]` 以外的每个 UTF-8 字节写成 `%XX`（大写）。地址正则 `^(?:uds|bridge|did):[A-Za-z0-9%:_/.\\-]{1,200}$`。

### 3.8 hop-chain（防环）【已核实】

- 每个进程启动时生成 32 字节随机密钥；自身 hop token＝`hex(HMAC-SHA256(密钥, 自身 uds 地址))[:24]`。
- 发送时取「本会话最近一条对等来信」的 hop-chain，追加自身 token，只保留最后 32 个。
- 收件准入：链长 > 28 → `hop-runaway`；链中自身 token 出现次数 ≥ 10 → `hop-loop`。

### 3.9 入站准入（`[peer-guard]`）【已核实】

默认限额（`wWn` + 队列上限）：`bucketCapacity 30, refillPerSecond 0.5, dedupWindowMs 30000, maxSelfHops 10, maxChainLength 28, maxTrackedSenders 256, maxQueuedPeerMessages 50`。

- 发送方键：`from:<from>`（无 from 时 `pid:<对端 pid>`），LRU 256。
- 顺序：hop 检查 → 与该发送方上一条正文完全相同且在 30s 内 → `duplicate` → 令牌桶不足 → `rate-limited` → 已排队未送达的对等消息 ≥ 50 → `queue-full`。
- 未通过：回 `dropped` 回执（带 `drop_reason`）。

### 3.10 入站策略与挂起【已核实，个别常量推断】

- 设置 `crossSessionInbound ∈ {accept, hold, refuse}`。forebrain 对应配置 `peers.inbound`（195）。
- 未设置时按收件方自身权限模式推导：自身模式＝`bypass`（forebrain：会话审批策略为 Never，即 Full Access / YOLO）或 `prompting`。消息信封带 `from-mode` 时：相同 → accept，不同 → hold（`mode-mismatch`）；不带时：自身 bypass → hold（`no-mode-asserted`），否则 accept。（源码里「是否采信 from-mode」受一个开关控制【推断：开启，依据是 2.1.301 给发送方的解释文案「通常原因是两个会话的权限模式不同」】。）
- hold：放进挂起缓冲并回 `held` 回执；终端会话里向用户提问放行/拒绝；放行 → 送达并回 `delivered`；拒绝 → 回 `denied`；进程退出或缓冲溢出淘汰最旧 → 回 `expired`。挂起缓冲上限【推断】forebrain 取 20。
- refuse：回 `expired` 且 `status_detail:"refused"`（线上状态值就是 `expired`）。

### 3.11 回执【已核实】

- 收件方只在 **hold、放行后送达、拒绝、过期、拒收、丢弃** 时发回执；正常 accept 的消息不发回执。
- 发往经命名空间校验（§3.4）的 `from` 地址，帧 `{"type":"control","action":"peer_message_status","status":…,"reason":"<文本>","from":"<自身地址>","orig_msg_id":"<原 msg_id>",…,"msgV":1,"msg_id":"<uuid>"}`。
- 发送方记最近 200 条外发（`msg_id → 目标`），收到回执按状态归类、防抖汇总后，**作为一条 `isMeta` 提示交给自己的模型**（2.1.301 的处理是入队一条 meta 提示，空闲时同样会开一轮）。

### 3.12 空闲通知（特性 `notify_idle`）【schema 已核实，时序推断】

- 订阅帧 `notify_when_idle {from, msg_id, from_mode?}`；通知帧 `peer_idle_notice {orig_msg_id, state, finished_at?, detail?, from?, from_mode?}`。
- 相关常量块：`43200000`（12h，推断为订阅过期）、`32`（推断为订阅上限）。
- forebrain 本期**作为收件方实现**（Claude Code 的 SendMessage 可以对 forebrain 发订阅），工具侧不暴露（裁决 7）。201 真机核实时序后才在 `peerFeatures` 里发布 `notify_idle`。

### 3.13 存活判定【已核实】

一条记录算在线：pid 存在（`kill(pid,0)`，EPERM 也算存在）、记录的 procStart 与现查一致（不一致＝pid 被复用）、socket 250ms 内能连上（`EBUSY` 也算活）。`pidDomain` 与自己不同的记录不做 pid 检查。死记录：Claude Code 会清理其目录里 pid 已死的记录；forebrain 只清理**带 forebrain 扩展块**的死记录（只清当前模式的那个目录）。

### 3.14 列表与 ref

Claude Code 的列表 ref（`name [ref]` 里的 `[3f2a1b]`）只是本地显示用的哈希前缀，不上线协议。forebrain 的 `peer_list` 自己算 ref（`sha256("<kind>:<id>")` 的最短唯一前缀，至少 6 位），只在 forebrain 内部解析，不追求与 Claude Code 显示一致。

### 3.15 本期不实现

- Windows（命名管道 `\\.\pipe\cc-msg-…`，强制鉴权）——单列为后续项，见 §9。
- `bridge:`/`did:` 地址（远程控制、云端会话）、`file_attachments`、`from_plugin`、artifact 系列、teams/swarm 协议（`team-lead`、tmux 队友）。
- `rename` 控制帧：收到只记日志。

## 4. forebrain 扩展

### 4.1 登记记录扩展块（Claude Code 忽略）

```json
"forebrain": {
  "v": 1,
  "surface": "tui",
  "agents": [
    {"id": "cli-9b62dc22…", "name": "forebrain-harness-3f", "kind": "primary",
     "root": "/abs/repo", "branch": "dev", "status": "busy",
     "task": "修复 xxx（当前一轮输入首行，≤120 字）", "mode": "prompting", "since": 1793342857359},
    {"id": "cli-9b62dc22…/<roster key>", "name": "forebrain-harness-3f.review-auth", "kind": "subagent",
     "parent": "cli-9b62dc22…", "root": "/abs/repo", "status": "busy", "task": "<子代理标题>", "since": 1793342990000}
  ]
}
```

- 代理 id：主代理＝会话 id；子代理＝`<会话 id>/<roster key>`。
- 代理名：TUI 主代理＝记录的 `name`；gateway 会话主代理＝`<slug(basename(项目根))>-<会话 id 去前缀后前 4 位>`；子代理＝`<主代理名>.<slug(标题)>`，冲突加 `-2`、`-3`。名字不得含 `@`、`*`，不得以 `uds:`/`bridge:`/`did:` 开头，不得是 `main`/`team-lead`/`user`/`system`。
- `agents` 最多 64 项（gateway 只列最近 24 小时有活动的会话，最近的在前）；整个记录不超过 262144 字节。

### 4.2 帧扩展（Claude Code 忽略）

user 帧可带 `"forebrain": {"v":1, "from":"<发送方代理 id>", "to":"<目标代理 id>"}`。`to` 只在目标是 forebrain 代理时填。

### 4.3 记录的其它取值

- `kind`：TUI 与 gateway 都写 `interactive`（`daemon`/`bg` 在 Claude Code 里另有语义，不碰）；`entrypoint`：`forebrain-tui` / `forebrain-gateway`。
- `sessionId`：TUI＝当前会话 id（`/resume` 或新会话后更新）；gateway＝进程启动时生成的 uuid。
- `cwd`：TUI＝启动目录；gateway＝启动目录（Claude Code 列表里显示用；路由不靠它）。
- `version`：`forebrain <VERSION>`；`peerProtocol: 1`；`peerFeatures`：本期 `["reply_across_default_dirs"]`，201 核实后再加 `notify_idle`。
- `status`：TUI＝当前会话有 primary run 在 Running → `busy`，停在审批 → `waiting`，用户 `!` shell 在跑 → `shell`，否则 `idle`；gateway＝任一会话 busy → `busy`，否则 `idle`。

## 5. 投递语义（196/197）

### 5.1 路由（收件进程内）

1. 帧带 `forebrain.to`：目标必须是本进程托管的代理，否则 refused（原因「unknown agent」）。
2. 不带：TUI → 当前会话的主代理；gateway → 用对端凭据拿到的 pid 找发送方登记记录，取其 `cwd`（发送方是 forebrain 时优先取扩展块里发送代理的 `root`），在本 gateway 的会话里找与之同树、非跑批、未被其它进程占着跑的会话，取最近活跃的那个；一个都没有 → refused（原因「no conversation in this work tree」）。
3. 跑批会话 → refused（跑批不收任何输入）。

### 5.2 投递

| 目标状态 | 动作 |
|---|---|
| 主代理有 run 在 Running | 插话（下一个工具边界送达），user 行带 `is_meta` + `origin: peer` |
| 主代理 run 停在审批 | 插话（审批通过后的下一次模型调用送达，与用户插话同语义；owner 2026-11 裁决修订） |
| 主代理空闲 | 自动唤醒：经表面端口开一轮，提示即信封（TUI 走事件循环，gateway 走 `startDetachedTurn`，`PromptOrigin: peer`） |
| 主代理正被另一个进程跑着（`SessionBusy`） | refused（原因「busy in another process」） |
| 子代理正在跑 | 插话进该子代理（不改它的 surface） |
| 子代理已结束 | 转交给派发它的主代理，信封前加一条中性 meta 说明「发给你的子代理 X 的消息在它结束后到达」 |

- 模型看到的就是信封原文（与 Claude Code 收件方一致）。表面从 user 行的 `origin: peer` + 信封解析出发信人显示卡片。
- 入站准入、入站策略、挂起放行、回执全部按 §3.9–§3.11。

### 5.3 发送（197）

- `peer_send {to, message}`：`to` 可以是代理名、`名字 [ref]`、`uds:` 地址。目标是本进程正在跑的子代理 → 直接插话（不走线协议）；本进程已结束的子代理 → 工具错误，提示用 `subagent_continue`；子代理发给自己的主代理 → 工具错误（结果自然回到派发方）。
- 跨进程：信封 `from`＝自身地址，`from-name`＝发送代理名，`from-mode`＝发送会话的模式，`hop-chain`＝继承 + 自身 token；帧带 forebrain 扩展；按目标 socket 限速（与入站同一组限额）。
- 收到的回执按 §3.11 汇总成一条 meta 消息交给发送会话（运行中插话，空闲时唤醒）。

## 6. 同树护栏（forebrain 独有，198，owner 裁决 3）

- **身份台账**：文件工具每次写入，在机器级目录 `os.UserCacheDir()/forebrain/agent-touches/`（与 186 台账同一套分片/保留/静默失败原则，另立目录以免旧版本读者解析失败）追加 `纳秒\t引用路径\t引用的代理地址\t引用的代理名`。地址＝`<进程 uds 地址>#<代理 id>`。
- **同树概况提醒**：每轮第一次模型调用前、以及本轮内同树对端集合变化后的下一次调用前，在请求末尾追加一条 `<system-reminder>`（仿 LSP 迟到诊断提醒，不动缓存前缀），列出同树对端的名字、类型、状态、任务、近 30 分钟改过的文件（来自台账；Claude Code 对端没有台账，只列在场）。
- **写入提示**：文件工具写成功后，若该路径 30 分钟内被另一个在线代理写过，在结果末尾附一行提示。
- **失败归因**：shell 命令非零退出时，附上执行窗口内其它代理改过的文件。
- **自主沟通（owner 2026-11-05 裁决）**：破坏性 git 操作不再是审批问题——代理之间先用 `peer_list` 看清谁在树里、再用 `peer_send` 打招呼，这是提示而非闸门。`--yolo` / 免审批会话不再为同树 git 命令弹审批，载荷里也不再出现任何越过免审批的标记。

## 7. 自闭环、互通开关与目录（owner 裁决 11、12、13；追问 9、10）

### 7.1 自闭环（裁决 12）

forebrain 之间的通信只靠 forebrain 自己：

- **没有中心进程**：每个 forebrain 进程自己监听一个 socket，按当前模式目录里的登记点对点直连；没有 broker、daemon、网关中转。任何一个进程退出，只是它自己从列表里消失。TUI 之间互通不需要网关在跑。
- **登记是普通文件**：当前模式的目录 + socket 命名空间；forebrain 自己创建（CC 目录除外）、自己清理。
- **不调用外部命令**：存活判定要的进程启动时间在进程内读取（darwin `sysctl kern.proc.pid`，linux `/proc`），格式与 `ps -o lstart=` 逐字节相同（2026-10-30 本机核实，含 Claude Code 自己的记录）；扩展块里的分支名直接读 `.git/HEAD`，不跑 `git`。
- **不依赖 Claude Code 进程**：FB 模式与 Claude Code 完全无关。CC 模式下 forebrain 之间照样点对点直连，只是借用 Claude Code 的目录与 socket 命名空间，Claude Code 跑没跑都一样；那个目录不在了就自动退回 FB 模式（§7.3），不会因为 Claude Code 被卸载或目录被清理而断掉。

### 7.2 互通开关（裁决 11、13）

- 位置：状态库 `fb_config` 表 `peers` 行的 `claude_code` 字段（`*bool`，没有这一行或字段为空＝**关**）。全局：每个 `FOREBRAIN_HOME` 一份，不分项目，项目侧没有覆盖。
- **只读写数据库，不经 forebrain.yaml**：旧配置导入（187）跳过 `peers` 段；组装配置时文件里的运行时段本来就作废；启动时发现文件里写了 `peers` 段只记一条警告。界面入口：TUI `/config`（面板已由 190 落地，`peers` 段的描述符由 195 加）、Web 设置（191）。网页的原始 YAML 编辑器（188 的 `GET/PUT /api/config`）不显示也不改这个字段，避免绕过显示条件。
- **显示条件（裁决 13）**：检测到 Claude Code 才显示，标准是 CC 目录存在且是目录（只 `os.Stat`，不读、不写、不建）。TUI 在打开 `/config` 时判断一次；网页设置读 `GET /api/config` 返回的 `claude_code_available`。没检测到时这一行整个不出现；库里已存的值保留，但不生效（§7.3）。标准用「目录存在」而不用「`claude` 在 PATH 里」：TUI 与 gateway 的 PATH 可能不同，两边会给出不同结论；而且打开后要用的正是这个目录，目录不在时 forebrain 也不会替它创建。
- 开关旁的提示：「默认关；开关不同的 forebrain 进程互相看不见」。
- 生效：跟随每 turn 同步的配置快照；对等服务在下一个 tick 发现目标模式变了就整体切换（§7.3）。

| | FB 模式（默认） | CC 模式（开关打开且检测到 Claude Code） |
|---|---|---|
| 登记、读取 | FB 目录 | CC 目录；**FB 目录不 stat、不读、不写** |
| socket | `fb-socks` | `cc-socks`（目录不在就建，Claude Code 自己也这么做）；`fb-socks` 不碰 |
| 收信 | 只收 FB 目录里在线 forebrain 进程的帧（来源闸门：对端凭据 pid 对得上一条带 forebrain 扩展块的记录，且帧的 `from` 就是那条记录的 socket）；其它连接不回执、不投递，只记 debug 日志 | 与 Claude Code 相同：同用户的连接按 §3.9 准入 |
| 看得见谁 | FB 模式的 forebrain | CC 模式的 forebrain + Claude Code 会话 |
| CC 目录 | 不读、不写、不建；只在要显示开关、或开关已打开等它出现时 stat | 读写自己的记录与 key；从不创建 |

- forebrain 的记录在两种目录里都带扩展块；CC 模式下靠扩展块区分 forebrain 与 Claude Code 会话。
- 开关不同的进程分处两个目录，**互相看不见**（裁决 13 的直接后果）；要互通就把开关设成一样。同一个 `FOREBRAIN_HOME` 的进程共用一份开关，通常一致；跨 home 协同时要各自设置。

### 7.3 模式切换与 CC 目录跟进（追问 9、10；裁决 13）

- 目标模式＝开关打开**且**检测到 Claude Code。开关关着时不 stat CC 目录；开关打开时每 5 秒 stat 一次。
- 目标模式与当前模式不同 → 整体切换：① 挂起未放行的来信全部回 `expired`；② 删掉旧目录里自己的记录与 key（旧目录是已消失的 CC 目录就跳过）；③ 关旧 socket 并删除 socket 文件；④ 在新命名空间监听新 socket；⑤ 在新目录写记录与 key。名字沿用，新目录里被占用才换后缀。**CC 目录永不创建。**
- 场景：
  - 装 Claude Code 之前启动的进程：目录出现前开关不显示；Claude Code 运行过一次、目录出现后，再打开 `/config` 就能看到；用户打开 → 下一个 tick 切到 CC 模式。
  - 开关已打开但 CC 目录暂时不存在：进程留在 FB 模式，目录出现后 ≤5 秒切过去。
  - 卸载 Claude Code 但目录还在：仍算检测到，留在 CC 模式。
  - `~/.claude` 被删：≤5 秒退回 FB 模式，开关从设置里隐藏；forebrain 不重建目录。

## 8. 计划拆分与依赖

| 计划 | 内容 | 依赖 |
|---|---|---|
| 194 | `pkg/peer` 协议库（无接线） | — |
| 195 | 进程接线：登记、状态、扩展块、服务端起停、两种登记模式与切换、`peers` 配置段（只存数据库）与开关的显示条件（TUI 面板描述符、`GET /api/config` 的 `claude_code_available`） | 194 |
| 196 | 入站：准入、策略/挂起、路由、插话/唤醒、落库、回执、空闲通知 | 195 |
| 197 | 出站与工具：`peer_list`/`peer_send`、名字解析、限速、回执汇总 | 196 |
| 198 | 同树护栏：身份台账、提醒、写入提示、失败归因 | 195（197 之后做更顺） |
| 199 | TUI 呈现：消息卡、回执状态、挂起放行、`/peers` | 196、197 |
| 200 | Web 呈现（先原型，owner 确认后实施） | 199 |
| 201 | 真机互通验收 + 协议漂移检查脚本 | 194–199（200 落地后补 web 场景） |

## 9. 风险与维护

- **协议漂移**：Claude Code 的对等协议是私有、随版本演进的（多处受远程开关控制）。201 提供 `scripts/peer-protocol-check.sh`，从已安装的 `claude.exe` 里检查稳定字面量（`cross-session-message`、`peer_message_status`、限额对象、`msgV` 值、真实记录的 `peerProtocol`）；升级 Claude Code 后先跑它。
- **Windows**：本期不做（命名管道 + 强制鉴权 + `procStartFt`），`pkg/peer` 在 Windows 上编译为「不支持」，服务不启动。是否补做由 owner 决定。
- **安全**：socket 0600 + 目录校验 + 对端 uid 核对；只接受同用户的连接。入站消息对模型是不可信输入（信封标明来源），bypass 会话对模式不一致的来信先挂起（§3.10）。
- **测试隔离**：任何测试都不得写真实 `~/.claude/sessions`、`/tmp/cc-socks`、真实 FB 目录或 `/tmp/fb-socks-<uid>`。`pkg/peer` 在测试二进制里（`testing.Testing()`）没设 `FOREBRAIN_PEERS_DIR` 与 `FOREBRAIN_PEER_SOCKET_DIR` 就不启用；`ClaudeCodeDetected()` 在测试二进制里没设 `FOREBRAIN_PEERS_CC_DIR` 就直接返回 false（不 stat 真实目录）；CC 模式的布局还要求 `FOREBRAIN_PEERS_CC_DIR` 与 `FOREBRAIN_PEER_CC_SOCKET_DIR`，缺一个就按 FB 模式处理。
- **宏观成本**：自动唤醒会消耗 token；防乒乓只用 Claude Code 那套（裁决 8）。
- **生产网关是多副本集群**：本协议只在同一台机器、同一用户内有效，跨副本、跨机器的进程互相看不见。网关路由只在本副本能看到的会话里选；状态库跨副本共享之前，不为集群做任何特殊处理。
- **提示缓存**：同树提醒、来信插话、回执汇总都是追加在请求末尾、并以固定位置并入会话，不动缓存前缀；`peer_list`/`peer_send` 是否注册在 runtime 创建时按「平台支持且配置未关闭」冻结（与 lsp 工具同一规则），不随对等服务启停变化。

## 10. 考虑后拒绝

- **机器级 SQLite 协同库（登记 + 信箱 + 台账一库）**：裁决 1 选的是「机器级、跨 home」的范围；裁决 4 要求与 Claude Code 线协议互通后，登记与投递必须用 Claude Code 的文件登记 + UDS，再建一个库只会变成两套事实来源。台账沿用 186 的分片目录方案。
- **forebrain 扩展信息写进 186 台账同一行**：旧版本读者对多一列的行 `strconv.Unquote` 失败会整行丢弃，混跑新旧版本时 186 的归因会退化，因此另立目录。
- **每个 gateway 会话一个登记记录**：Claude Code 的记录按 pid 命名、按 pid 判存活，伪造 pid 会被当成死记录清掉。
- **在 Claude Code 目录不存在时替它创建**：owner 裁决 6 否决。
- **默认与 Claude Code 互通 / 开关放 forebrain.yaml**：owner 裁决 11 否决——默认关闭，开关只读写 `fb_config`。
- **开关打开时 FB 目录照常登记、CC 目录只放镜像、另开一个 CC socket（「Claude Code 桥」）**：owner 裁决 13 否决——打开后只用 Claude Code 的目录，不再碰自己的。
- **开关不同的进程仍互相看得见（读两个目录）**：要么 CC 模式读 FB 目录，要么 FB 模式读 CC 目录，分别与裁决 13、裁决 11 冲突。
- **以 `claude` 是否在 PATH 里判断装没装**：TUI 与 gateway 的 PATH 可能不同，会对同一台机器给出不同结论；打开后要用的是 CC 目录本身，它不在时又不能替它创建。改为以 CC 目录存在为准。
- **用 `ps` 取进程启动时间、用 `git` 取分支**：依赖外部命令，违反裁决 12；改为进程内读取。
- **工具名对齐 `ListAgents`/`SendMessage`**：owner 裁决 7 选 forebrain 风格命名。
- **每小时唤醒上限 / 同一对来回上限**：owner 裁决 8 否决。

## 追加裁决（owner 2026-10-31）：跑批任务内代理间直接通信

跑批任务内主代理 ⇄ 子代理必须能直接通信（派发、插话、发消息、全部子代理工具）。`peer_list` / `peer_send` 在跑批会话里也注册，但目标只限本会话自己的代理；跨进程入站到跑批（上文第 241 行）与人的输入仍一律拒收。「跑批不收输入」的拒绝从 `SendToSubagent` 底层挪到人的入口。详见 plans/196、197 末尾追加段。

## 追加裁决（owner 2026-10-31）：跨进程协同固定开启

**追加裁决（owner 2026-10-31）：跨进程协同固定开启（always-on）**——不能关闭、不支持关闭、不支持配置；唯一的开关是「与 Claude Code 互通」（`peers.claude_code`，默认关闭，用户可手动打开，只存库，装了 Claude Code 才显示）。据此：`FOREBRAIN_PEERS=off` 总开关、`peers.enabled`、`peers.inbound` 全部删除；入站策略只按收件会话审批模式推导（上文第 170 行「forebrain 对应配置 `peers.inbound`」作废）。各计划的具体修订见 194/195/196/197/201 末尾。
