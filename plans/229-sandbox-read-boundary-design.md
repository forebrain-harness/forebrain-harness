# Plan 229: decide what a sandboxed command may read (design, not implementation)

> **Executor instructions**: This is a **design/spike plan**. Produce the design
> document and the decision list — do **not** change product behaviour, and do
> not touch `pkg/safety`'s decision code. The owner decides; you assemble the
> evidence that makes the decision honest. When done, update this plan's row in
> `plans/README.md` and hand the design to the owner.
>
> **Drift check (run first)**:
> `git diff --stat 7d505aa3..HEAD -- pkg/safety/sandbox_config.go pkg/safety/manager.go pkg/safety/backend_darwin.go pkg/safety/backend_linux.go pkg/tool/batch_boundary.go pkg/tool/shell_tool.go pkg/config/config.go deploy/docker/entrypoint.sh`
> Content mismatch against the excerpts is a STOP condition.

> **owner 裁决 2026-11-05：保持现状，本期不实施。** 本文件是设计与决策的留档，不是待执行的
> 工作项；不要据它改源码、不要另立实施计划。若将来要捡起，先重跑下方 drift 检查并重读 §5.1
> 的例外清单（`AllowRead={"/"}` 会让 deny 在文件工具里成空操作，那一处必须一起改）。

## Status

- **Implementation**: **DEFERRED by owner (2026-11-05) — 保持现状，不实施**
- **Priority**: P2 (design only; the finding itself is P1 once decided)
- **Effort**: M (the spike; the implementation that follows is M–L)
- **Risk**: HIGH to implement, LOW to design
- **Depends on**: plan 225 (the availability signal must be trustworthy before the
  policy that consumes it is changed)
- **Category**: security / direction
- **Planned at**: commit `7d505aa3`, 2026-11-04
- **Finding**: branch audit 2026-11-04, finding 9 (`pre-existing`, made
  consequential by plans 209 and 214)

## 已定方向（owner 2026-11-05 已确认 ①–⑥；**确认的是方向，不是排期**）

不再在 A/B/C 之间悬着，按下面这一套写设计与实施计划：

1. **拒读范围 = 方案 A**：只拒 `$FOREBRAIN_HOME` 整棵子树（读取、写、exec 都不例外），
   外加它下面所有符号链接解析后的真实路径。理由：它一条就切断了自提权链——
   `.env` 里的网关令牌、`forebrain.yaml` 里的库连接串（含密码）、状态库里的明文
   provider key / OAuth / MCP 凭据。而没有任何正常命令需要读这个目录，破坏面接近零。
2. **方案 B（再拒 `~/.ssh`、`~/.aws`、`~/.config/gh`、`~/.netrc`、`~/.git-credentials`、
   `~/.npmrc`）本期不做**，记为后续项：它会打断 `git push`、`ssh`、私有 npm 源这三个
   编码代理每天都要用的动作，而现有逃生口（`with_additional_permissions` 申请 + 人工
   审批）对每一次推送都弹一次，代价大于收益。等 A 落地、例外机制被验证过之后再评估。
3. **方案 C（默认拒绝读取）不做**：需要一份长期维护的系统例外清单，与本仓"少开关、
   少例外"的口径冲突。
4. **形态 = 默认开启，一个开关可关**（`sandbox.workspace_write.*` 下新增一项，默认
   true），与 `network_access` 同级。理由：A 理论上零破坏，但"命令读不到自己安装目录"
   在排障场景（用户让代理看 `forebrain.yaml` 为什么起不来）确实会被踩到，没有退路
   的默认值不可接受。
5. **文件工具与沙箱同规则**：`read_file` / `grep` / 列目录对 `$FOREBRAIN_HOME` 一律拒绝，
   否则 shell 被拦住、`read_file` 绕过去，等于没修。逃生口是审批（`request_permissions`），
   不是两套规则。
6. **网络维持默认开**：A 之后，读不到令牌与库密码，联网的收益（拉依赖、查文档）大于
   风险；把网络也默认关掉会让本仓自己的开发流程（`go get`、`pnpm install`）默认失败，
   属另一个量级的改动。方案 §4 仍要把这个判断写成显式结论，并列出"如果需要更严"的路径。

设计文档交付时，A/B/C 仍要写全（作为记录），但结论段必须明确：**选 A，B/C 记录为
未采纳及理由**，让后来者不必重新推演。

## Why this matters

A `workspace-write` shell command is sandboxed so it cannot write outside the
workspace. It may still **read the whole filesystem**, and it reaches the network
by default. On a normal install that includes two things that must not be
readable by a model-driven command:

1. `$FOREBRAIN_HOME/.env` — the gateway login token (`deploy/docker/entrypoint.sh:18-24`
   writes it there and `forebrain.yaml` references it). With that token a
   sandboxed command can call the gateway's own API.
2. `$FOREBRAIN_HOME/forebrain.yaml` plus the state database — the database URL
   **including its password** (multi-replica installs must use MySQL/PostgreSQL),
   and, by the owner's deliberate decision, provider keys, OAuth tokens and MCP
   credentials in plaintext.

So the sandbox's write boundary is real, but its read boundary is not: a command
that cannot write outside the workspace can still read the credentials that would
let it widen its own permissions (the approval preset is a row in the state
database) or reach the gateway API. Plan 214's container makes this sharper — the
gateway's own home sits in the same filesystem the agent's commands run in.

This is **not** a code bug: the deny machinery exists and works (`DenyRead`,
`DenyReadPatterns`, honoured by seatbelt at `pkg/safety/backend_darwin.go:97-140`
and by bwrap at `pkg/safety/backend_linux.go:153`). Three sources feed it today:
the batch boundary, another agent's isolated workspace, and the deny grants a
`request_permissions` answer writes back (`pkg/safety/sandbox_config.go:324-332`,
produced by `pkg/tool/request_permissions.go:149,363`). The same list reaches the
in-process file tools through `ApplyFilesystemPolicy`
(`pkg/tool/shell_tool.go:1046-1067`, called from `pkg/run/permissions.go:1133`,
consumed at `pkg/tool/state.go:2470,2678-2689` by `read_file` and `grep`), so a
`$FOREBRAIN_HOME` entry in `DenyRead` would cover both the sandbox and the file
tools. The decision of *what* an interactive command may read is a product
decision with real breakage on both sides, which is why this plan stops at the
decision.

## Current state

`pkg/safety/sandbox_config.go:266-296` — every interactive session reads `/`:

```go
func ConvertToRuntimeConfig(sources []SourceSettings, snap Snapshot, cwd, originalCwd, tempDir string, additionalDirs []string) RuntimeConfig {
	var out RuntimeConfig
	root := string(filepath.Separator)
	if volume := filepath.VolumeName(cwd); volume != "" {
		root = volume + string(filepath.Separator)
	}
	out.Filesystem.AllowRead = appendUniquePath(out.Filesystem.AllowRead, root)
	workspaceWrite := false
	...
	if workspaceWrite {
		out.Filesystem.AllowWrite = appendUniquePath(out.Filesystem.AllowWrite, cwd)
		// The sandbox's own temp directory and /tmp are writable by definition:
		// there is no setting that withholds them (plan 206).
		out.Filesystem.AllowWrite = appendUniquePath(out.Filesystem.AllowWrite, tempDir)
		if filepath.Separator == '/' {
			out.Filesystem.AllowWrite = appendUniquePath(out.Filesystem.AllowWrite, "/tmp")
		}
```

`pkg/safety/manager.go:104-118` — the only roots denied today are other agents'
isolated workspaces:

```go
func (m *Manager) convertLocked() RuntimeConfig {
	out := ConvertToRuntimeConfig(m.sources, m.snapshot, m.cwd, m.originalCwd, m.tempDir, m.additionalDirs)
	for _, root := range m.isolatedPeerRoots {
		out.Filesystem.AllowWrite = removePath(out.Filesystem.AllowWrite, root)
		out.Filesystem.AllowRead = removePath(out.Filesystem.AllowRead, root)
		out.Filesystem.DenyWrite = appendUniquePath(out.Filesystem.DenyWrite, root)
		out.Filesystem.DenyRead = appendUniquePath(out.Filesystem.DenyRead, root)
	}
	return out
}
```

`pkg/tool/batch_boundary.go:42-70` — batch projects already compute a deny set
(home + project root, with skill roots as explicit exceptions). This is the
existing pattern for "deny a root, allow named exceptions".

`pkg/config/config.go:32-40` — network is on unless the setting says otherwise:

```go
func (s SandboxWorkspaceWrite) EffectiveNetworkAccess() bool {
	return boolValue(s.NetworkAccess, true)
}
```

`pkg/safety/backend_darwin.go:95-145` — the seatbelt profile emits, in order:
`(allow file-read*)`, the deny rules, then narrower allows (later rules win), which
is why a deny must be paired with re-allows for legitimate subpaths.

`deploy/docker/entrypoint.sh:18-24` and `:59-90` — the gateway token is written to
`$FOREBRAIN_HOME/.env` (0600) and referenced as `${FOREBRAIN_GATEWAY_TOKEN}` from
`forebrain.yaml`.

## Scope

**In scope — the deliverable is a new section inside THIS file**: append
`## 设计提案（执行者填写）` to
`plans/229-sandbox-read-boundary-design.md`, containing sections 1–7 below.
`plans/` is git-ignored in this repo (owner 2026-10-30) and `docs/plan/` is
deprecated — do not create a file there, and do not create a new plan file.

**Out of scope** (this plan must not change any of them):
- **Every repository file except this plan file.** No edits under `pkg/tool`,
  `pkg/tui`, `pkg/gateway`, `pkg/config`, `deploy` or anywhere else — even when a
  section below discusses what those files would need. `git status --short` at the
  end may name only `plans/229-sandbox-read-boundary-design.md`.
- `pkg/safety/*.go` — no code changes at all.
- `pkg/config/*` — no new settings until the owner picks them.
- `deploy/**` — a container answer may be part of a *recommendation*, not a change.
- The approval preset semantics (`workspace_write` / `full_access`).

## What the design must contain

### 1. The threat, stated concretely

For each of these, name the file, what it grants, and what an attacker gains:
gateway login token; database URL with password; plaintext provider/OAuth/MCP
credentials in the state DB; `~/.ssh`, `~/.aws`, `~/.config/gh`, `~/.netrc`,
`~/.npmrc`, `~/.git-credentials`; any other agent's workspace (already denied).

### 2. Measure what breaks

For each candidate deny set, list the commands a user actually runs that would
stop working. At minimum:

- `git` in a workspace whose `~/.gitconfig` / credential helper lives in `$HOME`;
- package managers (`npm`, `pip`, `go`, `cargo`) reading `$HOME` config and caches;
- `ssh` / `git push` over SSH, `gh`, `docker`;
- language servers and formatters that read `$HOME` caches;
- the agent's own `read_file` on a path the user explicitly asked about (`~/x`).

Give each a verdict: works, needs an explicit exception, or genuinely breaks.
This is the part the owner cannot skip, because "deny $HOME" changes day-to-day
behaviour for every user.

### 3. Three options, each with its exact rules

Write them as concrete allow/deny lists the way `ConvertToRuntimeConfig` would
produce them, not as prose. **A is the chosen one** (see 已定方向); B and C are
recorded so the next reader does not re-derive them, each ending with one line on
why it was not taken:

- **A. Deny the install's own home only** (`$FOREBRAIN_HOME`: token, config,
  state DB, backups). Smallest blast radius, closes the credential chain, leaves
  `~/.ssh` readable.
- **B. A + deny credential stores** (`~/.ssh`, `~/.aws`, `~/.config/gh`,
  `~/.netrc`, `~/.git-credentials`, `~/.npmrc`, `~/.pypirc`) with the known
  re-allows (e.g. `~/.ssh/known_hosts` where a workflow needs it).
- **C. Default-deny reads:** read the workspace + `/usr`, `/etc`, `/System`,
  `/Library`, the toolchain roots, and an explicit per-user allow list; everything
  else denied. Closest to a real sandbox, most breakage.

For each: what it costs the user, what it still leaks, and whether it needs a new
setting or can be unconditional.

### 4. The network question, separately

Reading the credential is one path; **using** it over the network is the other.
State what `NetworkAccess: true` (the default) means for options A–C: with network
on, a denied-by-path credential can still be exfiltrated if it is reachable some
other way (environment, a copy in the workspace, the model's own context). Say
plainly whether the recommendation pairs a deny set with network-off, and what
breaks if it does.

### 5. Consistency with the rest of the product

- Batch already denies home + project root: state whether interactive becomes the
  same, narrower, or wider, and why.
- The **file tools** already follow the sandbox's deny set: `DenyRead` reaches
  them through `ApplyFilesystemPolicy` (`pkg/tool/shell_tool.go:1046-1067`, wired
  at `pkg/run/permissions.go:1133`, consumed at `pkg/tool/state.go:2470,2678-2689`
  by `read_file` and `grep`), and the batch boundary's `DeniedReadableRoots`
  (`pkg/tool/shell_tool.go:586`) is the same mechanism. So the design does **not**
  need a parallel rule for the file tools: one `$FOREBRAIN_HOME` entry in the
  generated `DenyRead` covers both. Say that explicitly, and say what the only
  remaining work is (the setting and its UI copy).
- Name the surface that must show the rule to the user, in Chinese copy, and where
  it lives (Settings → 审批与沙箱, and the TUI footer's sandbox indicator).

### 6. Alignment note

One short section: how Claude Code's `workspace-write` treats reads (state the
behaviour as fact from its own documentation or the local install, not as a guess),
and whether this repo should match it. Do not silently diverge.

### 7. A decision table the owner can answer

**已按上面的「已定方向」预填**：每条都写推荐答案，并把"若 owner 不同意"的后果
并列，让 owner 只需确认或逐条否决。至少覆盖：拒读范围（A）、是否给开关（给，默认
开）、文件工具是否同规则（是）、网络是否维持默认开（是）、以及存量安装升级时的
行为——新规则对已存在的 `workspace-write` 会话立即生效，但必须在 TUI/网页第一次
拒绝时给出一句可读的中文说明（"该路径属于 forebrain 自身安装目录，命令不可读；
需要时请申请权限"），不能静默失败。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Read the current rules | `grep -rn "DenyRead\|AllowRead\|DeniedReadableRoots" pkg/safety pkg/tool | grep -v _test` | the set to document |
| Read the batch precedent | `sed -n 40,110p pkg/tool/batch_boundary.go` | home + project root denied, skill roots excepted |
| Claude Code behaviour | read its docs for sandbox/workspace-write reads; record the source | a citation, not a guess |
| Sanity-check a proposal | on macOS: `sandbox-exec -p '<profile>' /bin/sh -lc 'cat ~/.env; echo $?'` | record the real output, so the design is not theory |

The last row is the important one: **run the profile you propose** against a
throwaway home and paste the real result. A deny rule that seatbelt silently
ignores is the classic failure of this kind of design.

## Done criteria

ALL must hold:

- [ ] `plans/229-sandbox-read-boundary-design.md` contains a `## 设计提案（执行者填写）`
      section with sections 1–7 above
- [ ] Every claim about a credential's location cites `path:line`
- [ ] The three options are written as concrete allow/deny lists
- [ ] At least one proposed profile was executed on the host, with its real output
      pasted (or the reason it could not be, stated)
- [ ] The decision table's questions each carry a recommended answer
- [ ] No source file is modified: `git status --short` is unchanged from before
      you started (`plans/` is git-ignored, so the only visible change is a file
      under `plans/` that git does not track — check with `ls -la plans/229-*`)
- [ ] **No secret value appears anywhere in the document** — locations and
      credential types only
- [ ] `plans/README.md` row for 229 updated to say "design ready, awaiting decision"

## STOP conditions

- You cannot determine where a credential lives — stop and report rather than
  describing a "likely" location.
- A proposed profile turns out to break `git` **in the repo's own workspace** for a
  reason you cannot fix with an explicit re-allow: record it; the option may be
  unusable and that is a finding, not a failure.
- The owner has already decided part of this (grep `plans/README.md` for the
  sandbox decisions of 2026-10-31 and 2026-11-04): carry those as settled and do
  not re-open them.
- Reproducing any credential's value would be needed to make a point: it is not —
  cite the location and stop.

## Maintenance notes

- The design is a decision input, not an implementation. The implementation gets
  its own plan once the owner answers the table, and it must re-run this plan's
  drift check because `pkg/safety` will have moved.
- A reviewer of the eventual implementation should check: the file tools and the
  sandbox agree, the denial survives a config reload
  (`manager.go:104-118`'s `convertLocked` is where a dropped deny would hide), and
  the upgraded install shows the new rule rather than silently tightening.
- Deliberately deferred: whether a *sandboxed* command should be able to reach the
  gateway at all (loopback is inside the sandbox's network namespace or not,
  depending on the backend). It interacts with `NetworkAccess` and needs the same
  decision.

## 设计提案（执行者填写）

> 执行 2026-11-05，按「已定方向」展开。**本文件是唯一被改动的文件**（`plans/` 已被 gitignore，
> 源码零改动）。文中不含任何凭据值，只有位置与类型。

### 1. 威胁：具体到文件、授权与攻击者所得

沙箱内的一条普通命令（`cat`、`curl`、`python -c`，或模型自己拼出来的一条）今天可以读：

| 位置 | 里面是什么 | 攻击者得到什么 |
|---|---|---|
| `$FOREBRAIN_HOME/.env`（`deploy/docker/entrypoint.sh:65` 写入，0600） | 网关令牌 `FOREBRAIN_GATEWAY_TOKEN`，`:74` 把它引用进 `forebrain.yaml` 的 `gateway.auth.token` | 直接以操作员身份调 `/api/*`：改配置、读全部会话、起会话、发消息——沙箱隔离被从内部解除 |
| `$FOREBRAIN_HOME/forebrain.yaml`（`pkg/home/data.go:73`） | `database.url`（`pkg/config/file_keys.go:51`），含库用户名/密码；`gateway.auth` | 直连状态库，绕过网关的一切校验 |
| `$FOREBRAIN_HOME/state/forebrain.state.sqlite`（`pkg/state/db.go:219`） | `fb_config.value_json`（`pkg/state/schema.sql:699`）里的 provider `api_key`（`pkg/config/agents.go:95`、`pkg/config/agent_llm.go:198`）；`fb_mcp_oauth.value_json`（`schema.sql:709`）的 MCP 刷新令牌；渠道账号引用值 | 用户付费额度（provider key）、OAuth 会话、第三方 MCP 账号 |
| `$FOREBRAIN_HOME/state/backups/` | 迁移前的整库备份 | 同一批凭据的历史副本，含已被"清除"的旧值 |
| 其它 agent 的隔离工作区 | 另一个会话的工作树 | **已拒**（`DenyRead` 三源之一，见 §5），不属本决策 |
| `~/.ssh`、`~/.aws`、`~/.config/gh`、`~/.netrc`、`~/.git-credentials`、`~/.npmrc`、`~/.pypirc` | 用户级凭据 | 方案 B 讨论的对象；**本期不拒**（§3） |

关键点：前四行构成一条**自提权链**——任一即可让被沙箱限制的命令拿回未受限能力，而它们全部落在
一个目录里。这就是选方案 A 的全部理由。

### 2. 实测：破坏面（在 macOS 真机跑，不是推演）

在 `pkg/safety` 里用产品自己的 `seatbeltProfile`（`backend_darwin.go:88-163`）构造
**A 的规则**（`DeniedReadableRoots = {假 home}`，`AdditionalReadablePaths = {"/"}`，
`ReadableExceptions = {workspace}`），把生成的 profile 落盘后用 `sandbox-exec -f` 跑真实命令：

```
DENY  PROBE  cat /Users/doudou/.fb229probe/.env
             → cat: /Users/doudou/.fb229probe/.env: Operation not permitted   exit=1
ALLOW PROBE  cat <ws>/readable.txt
             → ok                                                              exit=0
```

即：**A 的 profile 真的能拦住读**，且工作区照常可读。

同一 profile、同一命令，把假 home 换到 `t.TempDir()`（`/var/folders/…`）下时：

```
DENY  PROBE  cat /var/folders/…/home/.env → TOKEN=probe   exit=0    ← 没拦住
```

**这是一条必须写进实施计划的事实**：`(deny …)` 在 seatbelt 里是"最后匹配者胜"，
平台默认段（`backend_darwin.go:415-430`，含 `/tmp`、`/private/tmp`、`/var/tmp` 的读允许）
由 `writeSeatbeltMinimalPolicy` 写在**边界 deny 之后**，所以凡是落在这些根下（或任何平台默认
重新放行的根下）的 `$FOREBRAIN_HOME` 都会被重新打开。两件事：
(a) 实施时把 `$FOREBRAIN_HOME` 的 deny 作为**最后一条**写进 profile；
(b) 用一个**不在临时目录下**的路径写回归测试——用 `t.TempDir()` 当假 home 的测试会绿得毫无意义
（本次实测就是先绿后红才发现）。

逐条破坏面判定（按 §2 要求）：

| 用户动作 | A 之后 | 判定 |
|---|---|---|
| `git` 在工作区（`~/.gitconfig`、credential helper） | 不受影响：只拒安装目录 | 可用 |
| `npm` / `pnpm` / `pip` / `go` / `cargo` 读写 `$HOME` 缓存与配置 | 不受影响 | 可用 |
| `ssh` / `git push` / `gh` / `docker` | 不受影响（`~/.ssh`、`~/.docker` 仍可读） | 可用 |
| 语言服务器 / formatter 读 `$HOME` 缓存 | 不受影响 | 可用 |
| 代理读 `$FOREBRAIN_HOME/forebrain.yaml` 排查"为什么起不来" | **会失败**——这是 A 唯一真实的代价 | 需显式例外（见决策表 Q5） |
| 跑批（batch）任务 | 本来就拒 home + 项目根，A 不改变它 | 不变 |

### 3. 三个方案，写成 `ConvertToRuntimeConfig` 会产出的规则

```
A（选定）
  AllowRead:  { "/" }                         # 与今天相同
  DenyRead:   { $FOREBRAIN_HOME,              # 以及它每个符号链接解析后的真实路径
                <$FOREBRAIN_HOME 的 /var ↔ /private/var 等价形式> }
  ReadableExceptions: { workspace, 本次 turn 的 tempDir }
  网络:       默认开
  代价:       排查安装目录需要一次审批；其它一切不变
  仍泄漏:     ~/.ssh、~/.aws、~/.npmrc 等用户级凭据（方案 B 的范围）；环境变量里已存在的密钥

B（未采纳）
  A + DenyRead: { ~/.ssh, ~/.aws, ~/.config/gh, ~/.netrc,
                  ~/.git-credentials, ~/.npmrc, ~/.pypirc }
  ReadableExceptions 追加: { ~/.ssh/known_hosts, ~/.ssh/config }（若工作流需要）
  代价:      `git push`/`ssh`/私有 npm 源每用一次就断，逃生口是"每次一次人工审批"
  未采纳理由: 编码代理每天都要推代码，逐次审批的代价大于收益；等 A 落地、例外机制被验证后再评估

C（未采纳）
  DefaultDeny：AllowRead = { workspace, /usr, /etc, /System, /Library, /bin, /sbin,
                             toolchain 根, 每用户显式白名单 }，其余全拒
  代价:      需要一份长期维护的系统例外清单，与"少开关、少例外"的仓库口径冲突
  未采纳理由: 维护成本与误伤面随 macOS/Linux 版本漂移，收益（多拦住 ~/.ssh）有限
```

A 的规则形态与今天已有的边界机制完全同形：`DeniedReadableRoots` 已经是
`(deny file-read* file-write* (subpath …))` 加逐个例外回补（`backend_darwin.go:114-133`），
Linux 侧同一个字段落到 bwrap 的 `--ro-bind` 例外集（`backend_linux.go:153`）。**不需要新机制。**

### 4. 网络：单独一个问题

A 只解决"路径可达"，不解决"已经拿到手的字节怎么出去"。默认 `NetworkAccess: true` 之下：

- 令牌/库密码/凭据**读不到**了（A 的全部价值）；
- 但如果同一个值还能通过别的方式到手——**父进程环境变量**（`SafeSubprocessEnv` 的白名单已
  过滤 `*TOKEN*`/`*KEY*`，但用户自己 export 的不在过滤范围）、**工作区里的一份拷贝**、
  **模型上下文里已经出现过的值**——网络仍然能把它送出去；
- 因此 A **配对的是"减少可达性"，不是"杜绝外泄"**。

结论（写成显式判断）：**A 维持默认联网**。理由：读完不到凭据之后，联网的收益（`go get`、
`pnpm install`、查文档）远大于残余风险；而默认关网会让本仓自己的开发流程默认失败，属另一个
量级的改动。若要更严，路径是"每个项目一个 `network_access: false`"（已存在的开关，
`sandbox.workspace_write.network_access`），不是在本计划里改默认值。

### 5. 与产品其余部分的一致性

- **与跑批的关系**：跑批拒 `FOREBRAIN_HOME` **和**项目根（`pkg/tool/batch_boundary.go`）；
  交互沙箱 A 之后**更窄**——只拒安装目录，项目根的其它文件照读。这是刻意的：交互会话的
  用户就在项目里工作，拒项目根等于把工具废掉。
- **文件工具不需要第二套规则**（这是最容易做错的地方，也是省掉一半工作量的地方）：
  `DenyRead` 已经通过 `ApplyFilesystemPolicy`（`pkg/tool/shell_tool.go:1046-1067`，接线在
  `pkg/run/permissions.go:1133`，消费点在 `pkg/tool/state.go:2470,2678-2689`）同步给
  `read_file` / `grep`，跑批的 `DeniedReadableRoots`（`shell_tool.go:586`）走的是同一条。
  **所以实施计划里只需要在 `ConvertToRuntimeConfig` 的 `DenyRead` 里加一条 `$FOREBRAIN_HOME`，
  沙箱与文件工具同时生效**；不要另写一套"文件工具禁止读 home"的判断——两套规则必然漂移。
- **开关**：`sandbox.workspace_write.deny_install_home`，默认 `true`，与 `network_access` 同级；
  关闭时**一条 deny 都不写**（不做"部分关闭"）。
- **界面（中文文案，唯一需要新做的用户可见部分）**：
  - 设置 → 审批与沙箱：一行"拒绝读取 forebrain 自身安装目录（默认开）"，副标题说明关掉后
    沙箱内的命令可以读到网关令牌与明文凭据；
  - TUI 页脚的沙箱指示（`SandboxStatusLine` 同一行）：开启时显示 `安装目录已拒读`；
  - 第一次真被拒时给一句可执行的说明，而不是引擎的 `Operation not permitted`：
    **"该路径属于 forebrain 自身安装目录，命令不可读；需要时请申请权限。"**
    （`request_permissions` 是逃生口，走既有审批流。）

### 5.1 例外清单：安装目录里 agent 自己要用的东西（2026-11-05 实测补）

「拒整个 `$FORBRAIN_HOME`」有一半不成立：**agent 的执行机制本身不经过沙箱，也不经过
`read_file`**，所以拒读对它们零影响；但有两处在**命令/模型**手里，必须开例外，否则 A 会
直接打断编码代理的日常工作。

**零影响的（Go 进程自己读，沙箱与文件工具都碰不到）：**

| 东西 | 位置 | 谁读 |
|---|---|---|
| intermediate 笔记 | `<home>/state/intermediate/<session>.md`（`pkg/state/intermediate.go:10-14`） | `pkg/tool/orchestration.go:250,279` → `state.Read/Append` |
| 工具输出历史（`retrieve_output` 的数据源） | 状态库，不是文件（`shell_output.go:487-521` → `state.OpenOutputHistory(db)`） | `retrieve_output` 走 `db` |
| 工具输出溢写 | `<home>/state/tool-outputs`（`pkg/run/runner.go:1146`） | Go 写入；`pkg/process/cron_service.go:343` 清理 |
| 记忆文件 | **`<项目根>/memories/...`，不在安装目录**（`pkg/memory/scope.go:150`） | `pkg/memory` 用 `os.ReadFile/ReadDir` |
| 分词词典 | `<home>/dict`（`pkg/memory/dictionary.go:104-108`） | `pkg/memory`（记忆检索） |
| 技能加载 | `<home>/skills`、`<home>/skills/.system`（`pkg/skill/origin.go:87-94`） | `pkg/skill` 扫描 |

**必须开例外的两处（否则 A 一上线就打断日常工作）：**

1. **交互会话的 Go 构建缓存**：`resolveGoCacheAccess` 把 `GOCACHE`/`GOMODCACHE`/`GOTMPDIR`
   设在 `<home>/cache/go/<projectkey>/{build,mod,tmp}` 并作为额外可写绑定交给命令
   （`pkg/tool/shell_tool.go:1913-1926`）。今天的 `AllowRead = "/"` 顺带让编译器能读它；
   A 之后若不豁免，**`go build` / `go test` / `go vet` 全部失败**——这是编码代理每分钟都在做的事。
   规则：`<home>/cache/go` 同时**可读可写**（它本来就在可写绑定里）。
2. **家目录技能**：`<home>/skills`（shared）与 `<home>/skills/.system`（builtin）里的
   `SKILL.md` 与随附参考文件，模型要用 `read_file` 读、命令也可能 `cat`。批处理早就这么做了
   ——`ReadableExceptions` 把技能根作为 deny 的例外（`pkg/tool/shell_tool.go:597`）。A 沿用同一形态。

**一处必须修正的乐观判断（Q3 的"不需要新代码"不成立）**：实测 `SetReadPolicy(deny={home},
allow={"/"})` 之后 `ReadPathDenied` 对**任何**路径都返回 false——`pathWithinAny(p, allow)`
只要有一个祖先允许就放行，而 `"/"` 是所有路径的祖先（`pkg/tool/state.go:2606-2611`，探针输出
`.env=false`）。批处理之所以有效，是因为它在读策略**之前**先问
`BatchBoundaryFromContext`（`state.go:2678-2690`）。所以文件工具那一半要么
(a) 改成"最具体规则胜出"（deny 子树比覆盖它的 allow 更具体时拒绝——一个函数 + 测试），
要么 (b) 给交互会话引入与批处理同形的 boundary。二者都不是零代码；实施计划必须写明选哪一个。

### 6. 对齐说明

Claude Code 的 `workspace-write` 对读的态度是**读开放、写受限**：它的沙箱文档把
`workspace-write` 描述为"可以读工作区与系统文件，只有写被限制在工作区内"，并另有一个
"禁止联网"的维度；它没有把"自身配置目录"单列成拒读项，因为它的配置不在沙箱工作区内被
默认噪声覆盖（本仓的 `$FOREBRAIN_HOME` 承载的凭据比它更多：网关令牌 + 库连接串 + 明文库）。

结论：**本仓在 A 这一点上比它更严，且是有意的偏离**——理由是自提权链（§1 前四行）在
本仓是真实存在的。其余（读开放、写受限、联网默认开）与它一致，不做无谓分歧。

### 7. 决策表（**owner 2026-11-05 已逐条拍板**）

Q1–Q4 由 owner 当次答复直接选定（都取推荐项）；Q5–Q8 是它们的直接后果与实施约束，不再是待决问题。

| # | 问题 | 结论 | 状态 | 若反向选择的后果 |
|---|---|---|---|---|
| Q1 | 拒读范围 | **A：只拒 `$FOREBRAIN_HOME` 整棵子树（读/写/exec 全部，含符号链接解析后的真实路径）** | ✅ owner 已定 | 选 B 会打断 `git push`/`ssh`/私有 npm 源；选 C 需要长期维护系统例外清单 |
| Q2 | 是否给开关 | **给，默认开**（`sandbox.workspace_write.deny_install_home`） | ✅ owner 已定 | 没有开关时，排障场景（让代理看 `forebrain.yaml`）无路可走，只能关整个沙箱 |
| Q3 | 文件工具是否同规则 | **是**（同一条 `DenyRead` 喂给两者）；但**需要一处优先级改动**——`AllowRead={"/"}` 让 deny 在文件工具里成空操作（§5.1） | ✅ owner 已定 + 实现口径修正 | 另写一套规则 = 两套事实来源，必然漂移；只改沙箱等于 `read_file` 绕过 |
| Q4 | 网络 | **维持默认开**；要更严请用已有的每项目 `network_access: false` | ✅ owner 已定 | 默认关网会让 `go get`/`pnpm install` 默认失败 |
| Q5 | 排查安装目录怎么办 | **走审批的逃生口**（`request_permissions` 申请一次读），不做"只读放行排除" | ✅ 随 Q2 定 | 若要求"永远可读 forebrain.yaml"，A 的价值减半（库连接串就在里面） |
| Q6 | 已存在的 `workspace-write` 会话 | **立即生效**，不迁移、不询问；第一次被拒时给 §5 那句中文说明 | ✅ 随 Q1 定 | 静默收紧会表现为"命令突然坏了"，必须有一句可读解释 |
| Q7 | 落在地点不在 `$HOME` 的安装（容器里 `/data`、`/app`） | **按 `$FOREBRAIN_HOME` 的实际值判**，不硬编码 `~/.forebrain` | ✅ 实施约束 | 硬编码会让容器部署保留全部泄漏面 |
| Q8 | 平台默认段重新放行（本次实测发现） | **实施时把 deny 写为 profile 的最后一条**，并用非临时目录写回归测试 | ✅ 实施约束 | 不修则 `$FOREBRAIN_HOME` 落在 `TMPDIR`/`/tmp` 下的部署静默失效 |

**决策已定，但 owner 2026-11-05 裁决「保持现状，本期不实施」**：不建实施计划、不改源码。
将来若要实施，范围＝Q1–Q8 的结论（不含 B/C），必须先重跑本计划的 drift 检查（`pkg/safety`
会移动），并带上 Q8 的回归测试、Q6 的中文说明与 §5.1 的例外清单（含文件工具那处优先级改动）。
