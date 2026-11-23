# 194 — `pkg/peer`：与 Claude Code 互通的本机对等协议库（只建库，不接线）

- **基线**：`202ed4f2`（dev）。工作区有并行会话的未提交改动（`pkg/process/*`、`pkg/migrate/*`、`pkg/architecture/testdata/graph.json` 等）——只归因，不回滚。
- **状态**：TODO
- **依赖**：无（同族 194→195→196→197→198→199→200→201 的第一个）
- **执行隔离**：in-tree，**零 commit**（owner 手动审阅提交）
- **设计全文**：`docs/plan/CROSS_PROCESS_PEER_COORDINATION_DESIGN.md`（下称「设计」）。本计划已把需要的规范内联在 §3；设计 §3 是更完整的逆向参考。

> **执行者须知**：逐步执行，每步跑验证命令、确认结果后再往下走。碰到 §9 STOP 条件就停下报告，不要自行发挥。完成后更新 `plans/README.md` 里本计划那一行的状态。
>
> **Drift check（先跑）**：`git diff --stat 202ed4f2 -- pkg/architecture/cache_test.go go.mod` —— 若 `packageLayer`/`fanOutBudgets`/`maxPackageCount`/`maxProductionFilesPerPackage` 与 §2 摘录不符，按 STOP 处理。

## 0. 背景与 owner 裁决（全族共用，执行期不得再改）

用户同时开多个 forebrain TUI（和 gateway），在同一仓库主树、同一分支上并行做代码任务，进程间互相看不见，互相踩改动。owner 2026-10-30 裁决：

1. 协同范围＝**按工作树、跨 `FOREBRAIN_HOME`**。
2. 空闲收件方**自动唤醒开一轮**；正在跑的按插话送达。
3. 护栏＝同树概况提醒 + 写入提示 + 危险 git 必须审批 + 失败归因。
4. **严格对齐 Claude Code，线协议与 Claude Code 互通**。
5. 网关收到不带扩展寻址的消息时，按发送方工作树路由。
6. 只在 `~/.claude/sessions` 已存在时往里写，从不替它创建（原先的「否则用 forebrain 自己的目录；读两处」已被第 12 条取代）。
7. 工具用 forebrain 风格命名（`peer_list`/`peer_send`），只对齐线协议。
8. 防乒乓只用 Claude Code 那套。
9. 装了/卸了 Claude Code：登记**运行中自动跟进**；**永不创建** Claude Code 的目录。
10. **（同日追加，改写 4、6、9 的适用范围）与 Claude Code 互通是全局开关，默认关闭，用户手动打开**；开关存在状态库 `fb_config` 表 `peers` 行的 `claude_code` 字段，只读写数据库、不经 forebrain.yaml（195 实现）。关闭时只登记、只读取 FB 目录，**不碰** `~/.claude/sessions` 与 `cc-socks`（为决定显示不显示开关而 stat 一次除外，见第 12 条），只和其他 forebrain 进程通信。设计 §7.2。
11. **（同日追加）forebrain 跨进程通信必须自闭环，不依赖 Claude Code 或其他任何进程**：没有中心进程，每个进程自己监听一个 socket、按登记点对点直连；不调用外部命令（不跑 `ps`、`git`）。开关打开后改在 Claude Code 的目录登记（第 12 条），forebrain 之间仍点对点直连，不需要任何 Claude Code 进程在跑。设计 §7.1。
12. **（同日再追加，改写 6、9、10、11 中与目录有关的部分）开关只在检测到本机装了 Claude Code 时显示**（CC 会合目录存在，§3.1 `ClaudeCodeDetected`）；**用户打开后只登记、只读取 CC 目录，socket 放 `cc-socks`，不再碰 FB 目录与 `fb-socks`**。一个进程任何时刻只处在一种登记模式：FB 模式（默认）或 CC 模式（开关打开且检测到 Claude Code）。开关不同的进程互相看不见。设计 §7.2、§7.3。

**本计划只交付协议库 `pkg/peer`**：登记读写、key、socket、存活判定、帧、信封、hop-chain、入站准入/出站限速、收发端，加单元测试与 golden 夹具。**不接进任何进程**（195），不做投递（196）、工具（197）、护栏（198）、界面（199/200）。

## 1. 共享架构（全族一致，摘要）

- `pkg/peer`（本计划）：纯协议库，只依赖标准库、`golang.org/x/sys`、`github.com/google/uuid`、`golang.org/x/text`（NFKC 用），**不 import 任何 `pkg/...`**。
- `pkg/process`：组合根，唯一 import `pkg/peer` 的包（195 起）。`pkg/tool`、`pkg/run`、`pkg/tui`、`pkg/gateway` 都**不得** import 它——它们的扇出预算已满或超标（见 §2），一律走端口。
- 寻址：进程地址＝`uds:<socket 路径>`；forebrain 代理 id＝会话 id 或 `会话 id/roster key`（扩展字段，Claude Code 忽略）。

## 2. 现状事实（已对基线核实）

- 包架构规则（`pkg/architecture/cache_test.go`）：
  - `:370-382` 层表 `packageLayer`：`"state": 1, "config": 1, "home": 1, "telemetry": 1, "channel": 1` 在第 1 层；新包不在表里时 `TestLayerCeiling` 会静默跳过——**必须把 `"peer": 1` 加进表**。
  - `:169-232` `fanOutBudgets`：`"tool": {current: 9, target: 8}`（超 1，「may not grow again without the owner」）、`"run": {current: 14, target: 12}`、`"tui": {current: 19}`、`"gateway": {current: 18}`、`"process": {current: 19, target: -1}`。`current` 的注释是「Lower it whenever the real number drops; never raise it」。
  - `:681` `maxPackageCount = 32`（当前 `pkg/` 下 26 个目录）；`:1492` `maxProductionFilesPerPackage = 31`；`:1409` 文件名最多两个下划线（`_test` 也算）；`:807` 每个包必须有 `doc.go`；`:693` 包深度只能 1 层（`pkg/peer`，不能 `pkg/peer/x`）。
  - 测试文件必须有同名生产文件（2026-10-29 `pkg/llm/usage_snapshot_record_test.go` 因此被改名为 `stream_test.go`）。
- 依赖：`go.mod:16` `github.com/google/uuid v1.6.0`、`:42` `golang.org/x/sys v0.49.0`、`:44` `golang.org/x/text v0.43.0`，都已在依赖里，**不新增 module**。
- 测试二进制识别先例：`pkg/llm/tokenizers.go:472-473` 用 `testing.Testing()`。
- 186 的机器级目录先例：`pkg/tool/tool_writes.go:56-69`（`os.UserCacheDir()` + 环境变量覆盖 `FOREBRAIN_TOOL_WRITES_DIR`、失败一律静默）。
- 包文档范例：`pkg/lsp/doc.go:1-20`（一段说明 + 「Files:」逐个列文件职责）。

## 3. 规范（实现必须逐条满足；来源 Claude Code 2.1.301，见设计 §3）

### 3.1 目录、环境变量与布局

| 名称 | 默认 | 覆盖变量 |
|---|---|---|
| FB 会合目录 | `os.UserCacheDir()/forebrain/peers` | `FOREBRAIN_PEERS_DIR` |
| FB socket 目录 | `$XDG_RUNTIME_DIR/fb-socks`；未设则 `/tmp/fb-socks-<uid>` | `FOREBRAIN_PEER_SOCKET_DIR`（目录本身） |
| CC 会合目录（CC 模式用；也用来检测 Claude Code 是否安装） | `$CLAUDE_CONFIG_DIR/sessions`，未设则 `~/.claude/sessions` | `FOREBRAIN_PEERS_CC_DIR`（整条路径） |
| CC socket 目录（CC 模式用） | `<XDG_RUNTIME_DIR 或 CLAUDE_CODE_TMPDIR 或 /tmp>/cc-socks` | `FOREBRAIN_PEER_CC_SOCKET_DIR`（目录本身） |
| 总开关 | 开 | `FOREBRAIN_PEERS=off` 关闭 |

- `peer.Enabled() bool`：`FOREBRAIN_PEERS=off` → false；`runtime.GOOS` 不是 darwin/linux → false；**`testing.Testing()` 为真且 `FOREBRAIN_PEERS_DIR`、`FOREBRAIN_PEER_SOCKET_DIR` 有任一未设 → false**（任何测试都不得碰真实 FB 目录或 `/tmp/fb-socks-<uid>`）。
- `ClaudeCodeDetected() bool`：本机是否装了 Claude Code——CC 会合目录（上表，含覆盖变量）`os.Stat` 结果是目录 → true。**只 stat，不读、不写、不建**。`testing.Testing()` 为真且 `FOREBRAIN_PEERS_CC_DIR` 未设 → 直接 false，不做任何文件系统调用。调用方（195）只在两种时候调用：界面决定显示不显示开关时；开关已打开、每 5 秒确认目录还在时。
- `type Layout struct { ClaudeCode bool; Dir, SocketDir string }`；`NewLayout(claudeCode bool) Layout`：`claudeCode` 是调用方算好的**目标模式**（开关打开且 `ClaudeCodeDetected()`）。为真＝CC 模式：`Dir`＝CC 会合目录，`SocketDir`＝CC socket 目录；为假＝FB 模式：`Dir`＝FB 会合目录，`SocketDir`＝FB socket 目录。只读环境变量，**不碰文件系统**。`testing.Testing()` 为真且 `FOREBRAIN_PEERS_CC_DIR`、`FOREBRAIN_PEER_CC_SOCKET_DIR` 有任一未设时强制 FB 模式（任何测试都不得碰真实 `~/.claude/sessions` 或 `/tmp/cc-socks`）。本包不读配置。
- `(l Layout) SocketPath(pid int) string`：见 §3.4。
- 一个进程任何时刻只用一个 `Layout`。**FB 模式下本包对 CC 会合目录与 CC socket 目录不读、不写**（`ClaudeCodeDetected` 的 stat 除外）；**CC 模式下对 FB 会合目录与 FB socket 目录不 stat、不读、不写**。
- `MkdirAll(…, 0o700)` 只许用于 FB 会合目录与 `Layout.SocketDir`；**本包任何函数都不得对 CC 会合目录 `MkdirAll`**（它不在时写入直接返回错误）。

### 3.2 登记记录 `<pid>.json`

`type Record struct`，JSON 标签逐字如下（`omitempty` 只用于标注「可选」的字段）：

```
pid int; sessionId string; cwd string; startedAt int64; procStart string;
version string; peerProtocol int; peerFeatures []string; kind string;
entrypoint string; pidDomain string; messagingSocketPath string;
name string; nameSource string; nameSince int64; status string;
updatedAt int64; statusUpdatedAt int64;
forebrain *Extension `json:"forebrain,omitempty"`
```

另需读取（不写）：`formerNames`、`spare`、`parkedJobId`、`jobId`（`spare==true` 或 `parkedJobId!=""` 的记录在列表里跳过，与 Claude Code 一致）。

`type Extension struct { V int "v"; Surface string "surface"; Agents []ExtAgent "agents" }`；`type ExtAgent struct { ID, Name, Kind, Parent, Root, Branch, Status, Task, Mode string; Since int64 }`，JSON 名：`id,name,kind,parent,root,branch,status,task,mode,since`（空值 `omitempty`）。

- 写：目录 0700（只有 FB 目录会被建；CC 目录从不建，不存在时写入返回错误）；文件 0644；原子写（同目录临时文件 `.<pid>.json.tmp.<8hex>` + `os.Rename`）。
- 读 `ReadRecords(dir string) []Record`：只认 `^\d+\.json$` 且 `strconv.Itoa(pid)==名字`；文件 > 262144 字节、JSON 解析失败的跳过。调用方只传 `Layout.Dir`（一个进程只读当前模式的那一个目录）。
- 取值常量：`kind` 只写 `interactive`；`status ∈ {busy, shell, idle, waiting}`；`nameSource` 写 `derived`；`peerProtocol` 写 `1`；`peerFeatures` 由调用方给（元素须匹配 `^[a-z0-9_]{1,32}$`，最多 16 个）。

### 3.3 key 文件

- 名 `<pid>.<hex(sha256(canonicalSocketPath))>.key`，`canonicalSocketPath = filepath.Clean(filepath.Abs(p))`；读者正则 `^(\d+)\.[0-9a-f]{64}\.key$`。
- 内容 `{"peerToken":"<32 位小写十六进制>","procStart":"…","pidDomain":"…"}`，0600，原子写（临时名 `<pid>.<hash>.key.tmp.<hex>`），读上限 4096 字节。
- `NewToken()`：16 字节 `crypto/rand` → 小写十六进制。
- `FindToken(socketPath, dir string) (token string, ok bool)`：在该目录找哈希匹配的 key；多个时取 pid 存活且 `procStart` 吻合者。

### 3.4 socket

- `(l Layout) SocketPath(pid int) string`：`filepath.Join(l.SocketDir, strconv.Itoa(pid)+".sock")`；若字节数 > 103，FB 模式改为 `/tmp/fb-socks-<uid>/<pid>.sock`，CC 模式改为 `/tmp/cc-socks-<uid>/<pid>.sock`（与 Claude Code 相同）。只返回字符串，不建目录。
- 目录校验 `vetSocketDir(dir)`：`Lstat` 是目录（不是符号链接）；属主 uid 是自己或 0；若 `mode&0o022 != 0` 则必须有 sticky 位（`mode&os.ModeSticky`）。不通过 → 返回错误（服务不启动）。
- `Listen(path) (net.Listener, actualPath string, err)`：`EADDRINUSE` → 探活（250ms 拨号）；死的 `os.Remove` 后重试；活的改用旁置名 `<pid>-<8 位十六进制>.sock` 最多 3 次；启动时清理同目录下已死的 `<pid>-<8hex>.sock` 旁置兄弟。监听成功后 `os.Chmod(path, 0o600)`。

### 3.5 存活判定

- `ProcStart(pid int) (string, bool)`：**进程内读取，不跑 `ps`**（裁决 11 自闭环），得到与 Claude Code 用的 `LC_ALL=C TZ=UTC ps -o lstart= -p <pid>` 逐字节相同的字符串：
  - darwin：`kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)`；`time.Unix(int64(kp.Proc.P_starttime.Sec), 0).UTC().Format("Mon Jan _2 15:04:05 2006")`。（2026-10-30 已在本机对 4 个 pid——含 pid 1 与运行中的 Claude Code——核对与 `ps` 输出相等。）
  - linux：`/proc/<pid>/stat` 最后一个 `)` 之后按空白切分，第 20 项（即整行第 22 字段 `starttime`，单位 USER_HZ＝100）；`/proc/stat` 的 `btime` 行；`time.Unix(btime + starttime/100, 0).UTC().Format(同上)`。
  - 读不到（进程不存在等）→ `"", false`。进程级缓存 60s（未命中 5s）。
- `PIDDomain() string`：darwin → `"darwin"`；linux → `"darwin:" + trim(/etc/machine-id) + ":" + readlink(/proc/self/ns/pid)`（任一读不到就用空串占位，格式仍是两个冒号）。**前缀字面量就是 `darwin`，不要"修正"成 linux。**
- `Alive(r Record) bool`：`r.PidDomain != ""` 且 `!= PIDDomain()` → true；`syscall.Kill(pid,0)` 返回 nil 或 EPERM 才算存在；`r.ProcStart != ""` 时现查值必须相等；`r.MessagingSocketPath != ""` 时 250ms 内能拨通（`EBUSY` 也算）。

### 3.6 地址

- `EncodeUDS(path)`：`"uds:"` + 对 path 的每个 UTF-8 字节：属于 `[A-Za-z0-9:_/.\-]` 原样，否则 `%XX`（大写）。
- `DecodeAddress(addr) (scheme, target string)`：`uds:` 去前缀后百分号解码；`bridge:`/`did:` 原样返回 scheme（本期不支持发送）。
- `ValidAddress(addr)`：`^(?:uds|bridge|did):[A-Za-z0-9%:_/.\\-]{1,200}$`。
- `ReplyTargetAllowed(target, ownSocket string, ownUID int, peerFeatures []string) bool`（`ownSocket`＝收到这条帧的那个 socket）：target 不以 `.sock` 结尾 → false；target 与 ownSocket **同目录** → true；否则当 `peerFeatures` 含 `reply_across_default_dirs` 时，target 所在目录匹配 `^/tmp/cc-socks(?:-(\d+))?$`、`^/private/tmp/cc-socks(?:-(\d+))?$`、`^/run/user/(\d+)/cc-socks$`、`^/tmp/fb-socks-(\d+)$`、`^/private/tmp/fb-socks-(\d+)$`、`^/run/user/(\d+)/fb-socks$` 之一（捕获到的 uid 若有，必须等于 ownUID），且文件名匹配 `^(\d+(-[0-9a-f]{8})?|[0-9a-f]{1,16})\.sock$` → true；其余 false。

### 3.7 帧

- 每行一个 JSON 对象，`\n` 结尾，UTF-8；**含鉴权行在内**单行 ≤ 1,048,576 字节，超出拒发/断开。
- 鉴权行：`{"type":"auth","token":"<32hex>"}`。
- user 帧（发送）：

```json
{"msgV":1,"msg_id":"<uuid.NewString()>","type":"user","message":{"role":"user","content":"<信封>"},"priority":"next","from":"uds:…","forebrain":{"v":1,"from":"<代理 id>","to":"<代理 id>"}}
```

  `forebrain` 键可选。解码时保留所有未知键（用 `map[string]json.RawMessage` 先读，再取已知字段），以便 196 读 `session_id`、`priority`、`file_attachments` 等。
- control 帧：`{"type":"control","action":"<a>",<负载>,"msgV":1,"msg_id":"<uuid>"}`。本包提供构造器：
  - `PeerMessageStatus{Status, Reason, From, OrigMsgID, StatusDetail, DropReason string; DroppedMsgIDs []string}` → JSON 名 `status,reason,from,orig_msg_id,status_detail,drop_reason,dropped_msg_ids`；`Status` 合法值 `held|delivered|denied|expired|dropped`（「拒收」在线上写成 `status:"expired"` + `status_detail:"refused"`，提供 `RefusedStatus(reason, from, origMsgID)` 构造器）。
  - `NotifyWhenIdle{From, MsgID, FromMode}` → `from,msg_id,from_mode`。
  - `PeerIdleNotice{OrigMsgID, State string; FinishedAt *int64; Detail, From, FromMode string}` → `orig_msg_id,state,finished_at,detail,from,from_mode`。

### 3.8 信封

```
<cross-session-message{ from="F"}{ from-session="S"}{ hop-chain="h1,h2"}{ from-name="N"}{ from-mode="M"}{ from-plugin="P"}>
<转义后的正文>
</cross-session-message>
```

- `type Envelope struct { From, FromSession string; HopChain []string; FromName, FromMode, FromPlugin, Body string }`。
- `Build(e) string`：属性固定顺序、各自前置一个空格、空值省略。`From` 必须 `ValidAddress` 字符集 `[A-Za-z0-9%:_/.\-]` 且 ≤300，否则省略；`FromSession` 须匹配 `^[A-Za-z0-9_-]{1,80}$`；`HopChain` 每项 `^[0-9a-f]{24}$` 且最多 32 项（全部合规才写）；`FromName` 先去掉 `"<>`，再去掉 Unicode 类别 Cc/Cf/Cs/Zl/Zp 的字符，`TrimSpace`，超过 64 个字符截成前 64 个再加 `…`；`FromMode ∈ {bypass, prompting}`；`FromPlugin` 同 `FromName` 规则。
- `EscapeBody(s)`：凡是一个 `<` 之后（跳过空白与零宽/格式字符 Cf）是 `/`、再跳过同类字符、再是 `cross-session-message`（不区分大小写，`-` 与 `_` 互换，比较前对候选片段做 NFKC 并去掉 Cf 字符）的，把该 `<` 替换成 `<\`。全角 `＜`（U+FF1C）经 NFKC 是 `<`，也要命中。
- `Parse(content) (Envelope, bool)`：用整串正则 `^<cross-session-message(?: from="([A-Za-z0-9%:_/.\\-]+)")?(?: from-session="([A-Za-z0-9_-]{1,80})")?(?: hop-chain="([0-9a-f]{24}(?:,[0-9a-f]{24}){0,31})")?(?: from-name="([^"<>\n\r]+)")?(?: from-mode="(bypass|prompting)")?(?: from-plugin="([^"<>\n\r]+)")?>\n([\s\S]*)\n</cross-session-message>$`，再 `Build` 一遍（正文按已转义原样放回，不再二次转义）比对字节相等；不等 → `false`。
- `NormalizeName(s)`（等价 Claude Code `io()`）：NFKC → 去掉 Cc/Cf/Cs（空白保留）→ `TrimSpace` → 小写 → 连续空白换成 `-`。名字比较一律比 `NormalizeName`。

### 3.9 hop-chain

- `NewHopKey()`：32 字节随机；`HopToken(key, ownAddr) = hex(HMAC-SHA256(key, ownAddr))[:24]`。
- `ExtendChain(inherited []string, own string) []string`：追加后只留最后 32 个。
- `CheckChain(chain []string, ownTokens map[string]bool, lim Limits) (reason string)`：`len(chain) > lim.MaxChainLength(28)` → `"hop-runaway"`；链中属于 ownTokens 的个数 ≥ `lim.MaxSelfHops(10)` → `"hop-loop"`；否则 `""`。

### 3.10 准入与限速

`type Limits struct`，默认值 `DefaultLimits = {BucketCapacity: 30, RefillPerSecond: 0.5, DedupWindow: 30s, MaxSelfHops: 10, MaxChainLength: 28, MaxTrackedSenders: 256, MaxQueuedPeerMessages: 50}`。

- `Admission.Admit(senderKey, body string, chain []string, ownTokens map[string]bool, queued int, now time.Time) (ok bool, reason string)`，顺序：`CheckChain` → 同一发送方上一条正文完全相同且距今 < 30s → `"duplicate"` → 令牌桶（容量 30，每秒回 0.5；不足 1 → `"rate-limited"`，**不扣**）→ `queued >= 50` → `"queue-full"`；通过才扣 1 个令牌并记住正文与时间。发送方表按 LRU 保留 256 个。
- `Pacer.Reserve(target string, now) (ok bool, refund func())`：出站同一组限额（按目标 socket 规范路径为键），不足返回 false。
- `DropReasons` 映射（英文原文，用于回执 `reason` 与日志）：`rate-limited: sender exceeded the peer message rate limit`、`duplicate: identical to the previous message from this sender`、`hop-loop: message has already passed through this session (a peer messaging loop)`、`hop-runaway: peer relay chain is too long (runaway forwarding)`、`queue-full: this session has too many undelivered peer messages queued`。

### 3.11 服务端与客户端

- `type Server struct { Path string; Token string; FirstLineDeadline time.Duration /*默认 30s*/; MaxLine int /*默认 1048576*/; Handler Handler }`；`Handler` 接口 `HandleFrame(ctx context.Context, in Inbound)`；`type Inbound struct { Frame Frame; PeerPID, PeerUID int; Authenticated bool; Local string /* 收到这条帧的 Server.Path：切换登记模式后，调用方据此认出旧 socket 上迟到的帧 */ }`。一个进程任何时刻只跑一个 `Server`（切换模式时先关旧的再起新的）。
- 连接处理：读不到完整首行超过 `FirstLineDeadline` → 关闭；读对端凭据（darwin：`unix.GetsockoptXucred` 取 uid、`unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)` 取 pid；linux：`unix.GetsockoptUcred(fd, SOL_SOCKET, SO_PEERCRED)`），**uid ≠ 自己 → 立即关闭**；首行若是鉴权帧，与 `Token` 常数时间比较（`crypto/subtle`），结果写进 `Authenticated`，**鉴权错不断开**（POSIX 上鉴权可选）；空行跳过；单行超过 `MaxLine` → 断开；JSON 解析失败的行跳过并记日志。
- 顺序：`type=="control"` 且 `action!="notify_when_idle"`、或 `type=="user"` 且 `priority=="now"` 且无 `file_attachments` 的帧立即交 Handler；其余交给**进程级**单 goroutine 串行队列按到达顺序处理。
- `Send(ctx, socketPath string, f Frame, opt SendOptions) error`：`opt.NoFollowSymlink` 时 `Lstat` 是符号链接即拒；`FindToken` 找得到就先写鉴权行；拨号与写入总超时 5s；`opt.ExpectPID > 0` 时连上后读对端凭据核对 pid 与 uid，不符即断开报错；写完关闭。错误分类：`ErrPeerGone`（ENOENT/ECONNREFUSED）、`ErrTransient`（超时/EAGAIN/EBUSY）、`ErrRefused`（校验不过）。
- 平台：`sys_darwin.go`、`sys_linux.go`、`sys_other.go`——每个文件同时放该平台的对端凭据读取与 `ProcStart` 实现（其它平台返回「不支持」错误，`Enabled()` 已经为 false）。

### 3.12 名字

- `DeriveName(cwd string, taken func(normalized string) bool) string`：`base = slug(filepath.Base(cwd))`（slug：NFKC，非 `[\p{L}\p{N}._-]` 的连续字符换成 `-`，去掉首尾 `._-`，最多 20 个字符；没有字母数字时用 `forebrain`），后缀为 1 字节随机数的两位十六进制；`taken` 为真就换后缀重试（最多 64 次，之后用 4 位十六进制）。
- `ValidAgentName(s)`：非空；不含 `@`、`*`、`"`、`<`、`>`；`NormalizeName` 后不等于 `main`/`team-lead`/`user`/`system`；不以 `uds:`/`bridge:`/`did:` 开头。
- `Ref(kind, id string, all []string) string`：forebrain 本地 ref，`hex(sha256(kind+":"+id))` 的最短唯一前缀（至少 6 位，最多 12 位）。**不上线协议，不追求与 Claude Code 显示一致。**

## 4. 步骤

> 包内文件名一律 ≤2 个下划线（含 `_test`）；每个 `_test.go` 都有同名生产文件。

1. **骨架与开关**：建 `pkg/peer/doc.go`（仿 `pkg/lsp/doc.go:1-20` 写「Files:」清单，并写一句「协议对标 Claude Code 2.1.301，规范见 docs/plan/CROSS_PROCESS_PEER_COORDINATION_DESIGN.md §3」，再写两句「一个进程任何时刻只用一种登记模式：FB 模式（FB 目录 + fb-socks，默认）或 CC 模式（Claude Code 的目录 + cc-socks）；不依赖任何其他进程，不调用外部命令」「从不创建 Claude Code 的会合目录」）、`paths.go`（§3.1、§3.4 的路径、布局与校验）+ `paths_test.go`：覆盖变量生效；测试二进制里缺 FB 两个覆盖 → `Enabled()==false`；FB 两个都设 → true；`FOREBRAIN_PEERS=off` → false；`ClaudeCodeDetected()`：没设 `FOREBRAIN_PEERS_CC_DIR` → false；指向已存在的目录 → true；指向不存在的路径 → false 且**该路径仍不存在**；指向一个普通文件 → false；`NewLayout(true)` 缺 CC 两个覆盖 → FB 模式（`ClaudeCode==false`、`Dir`＝FB 目录），四个都设 → CC 模式（`Dir`＝CC 目录、`SocketDir`＝CC socket 目录）；`NewLayout(false)` 在四个覆盖都设时仍是 FB 模式，`Dir`、`SocketDir` 都不是 CC 路径；`SocketPath` 超过 103 字节时 FB 模式回退到 `/tmp/fb-socks-<uid>`、CC 模式回退到 `/tmp/cc-socks-<uid>`（只断言返回的字符串，不建目录）；目录校验拒绝 0777 无 sticky 与符号链接。
   验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/peer -count=1` → ok。
2. **架构表**：`pkg/architecture/cache_test.go` 的 `packageLayer` 加 `"peer": 1`；`fanOutBudgets` 加 `"peer": {current: 0, target: 0}`（带一行注释：协议库只依赖标准库与 x/sys、uuid、x/text）。
   验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → ok。
3. **地址 / 信封 / 名字**：`address.go`、`envelope.go`、`names.go` + 同名测试。测试至少包括：`EncodeUDS("/tmp/cc-socks/49280.sock") == "uds:/tmp/cc-socks/49280.sock"`；含空格与中文的路径编码；`Build` 对 §5 夹具逐字节相等；`Parse(Build(e))` 往返；属性乱序/多空格/尾部多换行 → `Parse` 失败；`EscapeBody` 对 `</cross-session-message>`、`</CROSS_SESSION-MESSAGE>`、`<​/cross-session-message>`（中间零宽空格）、`＜/cross-session-message>` 全部插入反斜杠，对 `<b>`、`</cross-session>` 不动；`ReplyTargetAllowed` 同目录/跨默认目录（cc-socks 与 fb-socks 两族各一例）/错 uid/非 .sock 四种情况；`DeriveName` 冲突换后缀；`ValidAgentName` 拒 `main`、`a@b`。
   验证：同上包测试 ok。
4. **hop 与准入**：`hop.go`、`guard.go` + 测试：token 24 位十六进制且同 key 同地址稳定；链长 29 → runaway；10 个自身 token → loop；30 条瞬时通过、第 31 条 rate-limited 且未扣令牌；推进 2s 后再放行 1 条；30s 内同正文 duplicate、31s 后放行；两个发送方互不影响；第 257 个发送方挤掉最久未用的；`queued=50` → queue-full；`Pacer` 同规则。时间一律注入 `now`，不 `time.Sleep`。
   验证：同上包测试 ok。
5. **登记 / key / 存活**：`record.go`、`key.go`、`liveness.go` + 测试：写读往返（含扩展块）；读 `testdata/cc-2.1.301/record.json`（§5）得到所有字段且扩展块为 nil；`012.json`、超 262144 字节、坏 JSON 被跳过；key 文件名哈希等于 `sha256(规范路径)`、权限 0600；`FindToken` 命中；`ProcStart(os.Getpid())` 匹配 `^[A-Z][a-z]{2} [A-Z][a-z]{2} [ 0-9][0-9] [0-9]{2}:[0-9]{2}:[0-9]{2} [0-9]{4}$`，**且**对 `os.Getpid()` 与 pid 1 都等于测试里执行 `LC_ALL=C TZ=UTC ps -o lstart= -p <pid>` 的去空白输出（`ps` 只在测试里用来证明与 Claude Code 的取值一致，生产代码不调用）；不存在的 pid（启动一个 `sleep 0` 子进程等它退出后用它的 pid）→ `Alive==false`；`PIDDomain()` 在 darwin 上等于 `"darwin"`（linux 上以 `"darwin:"` 开头且含两个冒号）。
   验证：同上包测试 ok。
6. **帧与收发端**：`frame.go`、`server.go`、`client.go`、`sys_darwin.go`、`sys_linux.go`、`sys_other.go`（第 5 步的 `ProcStart` 平台实现也放这三个文件）+ 测试。socket 测试目录用 `os.MkdirTemp("/tmp", "fbp")`（macOS 的 `t.TempDir()` 太长会超 104 字节），用 `t.Cleanup` 删除。覆盖：不带鉴权发 user 帧被收到且 `Authenticated=false`；带正确鉴权 `true`；鉴权错仍收到后续帧且 `false`；`Inbound.PeerPID == os.Getpid()`；`Inbound.Local` 等于收到它的 `Server.Path`（先后起两个 Server 各发一条，证明能区分）；首行超时（把 `FirstLineDeadline` 设 200ms）连接被关；超长行断开；control 帧不进串行队列（用阻塞的串行 Handler 证明 control 先到）；`Send` 对符号链接目标且 `NoFollowSymlink` 时拒绝；`ExpectPID` 不符时报 `ErrRefused`；目标不存在报 `ErrPeerGone`；`Listen` 遇到活 socket 时改用旁置名、遇到死 socket 时删除重绑。
   验证：同上包测试 ok；`go vet ./pkg/peer` 无输出。
7. **golden 夹具**：建 `pkg/peer/testdata/cc-2.1.301/`，放 §5 列出的文件，并在 `envelope_test.go`/`frame_test.go`/`record_test.go` 里逐字节比对或字段比对。
   验证：同上包测试 ok。
8. **全量收口**：`bash scripts/package-graph.sh`（会改 `pkg/architecture/testdata/graph.json`，新增 `pkg/peer` 节点——这是预期 diff；该文件工作区本就有并行改动，只追加不回滚）。
   验证：`go vet ./...` 无输出；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/peer ./pkg/architecture -count=1` → ok；`git status --porcelain | grep -v '^??' | grep -v -E 'pkg/peer|pkg/architecture/(cache_test.go|testdata/graph.json)'` 输出与开工前的并行改动清单一致（没有新增的越界文件）。

## 5. golden 夹具（逐字写入 `pkg/peer/testdata/cc-2.1.301/`）

`record.json`（Claude Code 2.1.301 真实记录的形状，值已替换为假数据）：

```json
{"pid":49280,"sessionId":"4693bfa1-4681-4d4d-bac8-d2800c5acf11","cwd":"/Users/u/repo","startedAt":1793342857359,"procStart":"Fri Oct 30 06:47:30 2026","version":"2.1.301","peerProtocol":1,"peerFeatures":["notify_idle","reply_across_default_dirs","artifact_yield"],"kind":"interactive","entrypoint":"cli","pidDomain":"darwin","messagingSocketPath":"/tmp/cc-socks/49280.sock","name":"repo-28","nameSource":"derived","nameSince":1793342857359,"status":"busy","updatedAt":1793344027704,"statusUpdatedAt":1793344027704}
```

`user-frame.json`（单行；`msg_id` 固定为 `11111111-2222-4333-8444-555555555555`）：

```json
{"msgV":1,"msg_id":"11111111-2222-4333-8444-555555555555","type":"user","message":{"role":"user","content":"<cross-session-message from=\"uds:/tmp/cc-socks/49280.sock\" hop-chain=\"0123456789abcdef01234567\" from-name=\"repo-28\" from-mode=\"prompting\">\nhello <\\/cross-session-message> world\n</cross-session-message>"},"priority":"next","from":"uds:/tmp/cc-socks/49280.sock"}
```

（`content` 里的正文是 `hello </cross-session-message> world` 经 `EscapeBody` 后的 `hello <\/cross-session-message> world`；JSON 里反斜杠写成 `\\`。）

`status-held.json`：

```json
{"type":"control","action":"peer_message_status","status":"held","reason":"held for approval","from":"uds:/tmp/cc-socks/50000.sock","orig_msg_id":"11111111-2222-4333-8444-555555555555","msgV":1,"msg_id":"66666666-7777-4888-8999-aaaaaaaaaaaa"}
```

`status-refused.json`：同上但 `"status":"expired","status_detail":"refused"`。
`status-dropped.json`：同上但 `"status":"dropped","drop_reason":"rate-limited","dropped_msg_ids":[]`。
`notify-when-idle.json`：`{"type":"control","action":"notify_when_idle","from":"uds:/tmp/cc-socks/49280.sock","msg_id":"…","msgV":1}`。
`idle-notice.json`：`{"type":"control","action":"peer_idle_notice","orig_msg_id":"…","state":"idle","finished_at":1793344027704,"from":"uds:/tmp/cc-socks/50000.sock","msgV":1,"msg_id":"…"}`。

键顺序不作要求（比对用结构化比较）；`content` 字符串必须逐字节一致。

## 6. 边界

- **只动**：新建 `pkg/peer/**`；`pkg/architecture/cache_test.go`（只加两行表项）；`pkg/architecture/testdata/graph.json`（脚本生成）。
- **不动**：任何其它 `pkg/...`、`cmd/...`、前端；`go.mod`/`go.sum`（不新增依赖——若编译提示需要新 module，STOP）。
- 不实现：Windows（命名管道）、`bridge:`/`did:` 发送、`file_attachments`、artifact 系列控制帧、`rename` 的处理（196 只记日志）。

## 7. 完成判据（机器可查）

1. `CGO_ENABLED=1 go test -tags fts5 ./pkg/peer ./pkg/architecture -count=1` → ok。
2. `go vet ./...` 无输出。
3. `grep -rn '"github.com/forebrain-harness/forebrain-harness/pkg/' pkg/peer --include='*.go' | grep -v _test.go` 无输出（协议库不依赖任何内部包）；`grep -rln '"os/exec"' pkg/peer --include='*.go' | grep -v _test.go` 无输出（自闭环：生产代码不调用外部命令）。
4. `grep -rn 'MkdirAll' pkg/peer --include='*.go' | grep -v _test.go` 的每一处都只作用于 FB 会合目录或 `Layout.SocketDir`（人工核对 ≤3 处，并在 doc.go 写明「从不创建 CC 目录」）；`ClaudeCodeDetected` 里只有一次 `os.Stat`、没有任何读写（人工核对）。
5. `ls pkg/peer/*.go | grep -v _test | wc -l` ≤ 20。
6. `plans/README.md` 本行状态更新。

## 8. 测试计划

- 全部是 `pkg/peer` 单元测试，时间注入、无网络、无真实 `~/.claude`。
- socket 用例参照 `pkg/safety/backend_linux.go:325`、`pkg/safety/managed_network.go:1161` 的 `net.ListenUnix`/`DialContext("unix")` 写法。
- 夹具比对参照 `pkg/tool` 的 golden 测试写法（`pkg/tool/testdata`）。

## 9. 维护注意

- 这是**私有协议**的兼容实现。Claude Code 升级后先跑 201 提供的 `scripts/peer-protocol-check.sh`；常量（§3.10 限额、1 MiB 行上限、30s 首行超时、`msgV:1`）有变就改 `DefaultLimits`/常量与夹具目录（新建 `testdata/cc-<新版本>/`，旧的留着做回归）。
- 登记模式（owner 追加裁决）：一个进程只用一个 `Layout`。以后新增任何读写目录或 socket 的函数，都只能从 `Layout.Dir`/`Layout.SocketDir` 取路径，不得自己拼 FB 或 CC 的路径；`ClaudeCodeDetected` 只能 stat，不能读写或创建。
- 自闭环（owner 追加裁决）：forebrain 之间的任何功能都不得依赖 Claude Code 进程或外部命令（CC 模式只借用它的目录与 socket 命名空间）；生产代码不得 import `os/exec`。`ProcStart` 若在某个平台上与 `ps` 对不上，修进程内实现，不要退回去跑 `ps`。
- `pidDomain` 的 `darwin` 前缀、`refused` 在线上写成 `expired+status_detail` 都是 Claude Code 的真实行为，看着像笔误也不要"修"。
- 文件计数上限 31：本包预计 15 个生产文件，后续加功能优先并进现有文件。

## 10. STOP 条件

- `pkg/architecture` 的表结构与 §2 摘录不符（比如层表改名或预算机制变了）。
- 实现 §3 任一条需要 import `pkg/...` 内部包或新增 module。
- macOS 上 `unix.LOCAL_PEERPID`/`GetsockoptXucred`/`SysctlKinfoProc` 在 `golang.org/x/sys v0.49.0` 里不存在（写计划时已确认都在；不要自己写 cgo 去补）。
- 进程内算出的 `ProcStart` 与测试里 `LC_ALL=C TZ=UTC ps -o lstart=` 的输出不相等（先报告两边的值，不要改成调用 `ps`）。

**追加裁决（owner 2026-10-31）：跨进程协同固定开启（always-on）**——不能关闭、不支持关闭、不支持配置；唯一的开关是「与 Claude Code 互通」（`peers.claude_code`，默认关闭，用户可手动打开，只存库，装了 Claude Code 才显示）。本计划受影响：删掉总开关 `FOREBRAIN_PEERS=off`（表格「总开关」一行与 `peer.Enabled()` 里对它的判断）；`peer.Enabled()` 只保留平台判断（darwin/linux）与测试隔离判断（`testing.Testing()` 时要求覆盖目录）——这两条是能力与测试安全，不是开关。

