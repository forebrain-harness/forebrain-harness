# Plan 105：（设计 spike）OpenCode / ChatGPT 两套登录文件要不要收成一个存储层——量一量，给出裁决，不改生产代码

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不用 worktree，
> 改动直接留在 dev 工作区。
>
> **这是 spike**：唯一允许新增的文件是设计文档 `docs/plan/OAUTH_LOGIN_FILE_DESIGN.md`（见 Scope）；原型代码只能写在会话的 scratchpad 里，**不进仓库**。
> `git status --short` 里除了这份文档（以及你必须保留的、开始前就存在的脏文件）不应出现新条目。
>
> **前置**：计划 `101-share-the-credential-refresh-lock-per-file` 必须已落地（本 spike 要对着它之后的代码量）。
> 先跑：`grep -rn 'CredentialMutex' pkg --include='*.go' | grep -v _test` 必须有命中（至少定义 + opencode + openai 三处）；没有 → STOP 1。
> 建议计划 `108-login-flows-survive-strays-and-cancel` 也已落地（登录流程的形状稳定后再比较）。
>
> **Drift check（先跑）**：`git diff --stat 55019a2 -- pkg/llm/opencode pkg/llm/openai` 的输出应当**只**来自计划 101（和 108/104，若已落地）；
> 出现别的改动 → 在设计文档里如实记下，不是 STOP。

## Status

- **Priority**: P3
- **Effort**: S（读 + 量 + 写文档；约半天）
- **Risk**: LOW（不改生产代码）
- **Depends on**: `101`（硬）；`108`（建议）
- **Category**: direction（设计裁决）
- **Planned at**: commit `55019a2`，2026-10-23
- **产出**：`docs/plan/OAUTH_LOGIN_FILE_DESIGN.md` + 一份给 owner 的三选一建议；owner 选 B 或 C 之后，才另立实施计划

## Why this matters

仓库里有两套「登录文件 + 自动刷新」：OpenCode（`pkg/llm/opencode`：`Credentials`/`Load`/`Save`/`refresh`/`Transport.token`）和
ChatGPT（`pkg/llm/openai`：同一组名字，`Transport.current`）。两者的骨架几乎一样——读文件、没到期就用、快到期就刷新并写回、被拒绝就报「登录失效」——
计划 101 又给它们各加了一份「按路径加锁 + 被轮换后采用磁盘上的新登录」。再来第三个 OAuth 供应商，就会有第三份拷贝。

但「骨架一样」不等于「抽出来就划算」：两边在到期来源（文件字段 vs JWT 声明）、刷新请求形状（JSON vs 表单）、落盘格式（自有 vs 与 Codex CLI 互通的嵌套结构）、
拒绝的识别方式（401 的 `APIError` vs `Code == "invalid_grant"`）、锁定后的收尾（合并字段）上都有差别。抽象的钩子数量一多，抽出来的东西可能比两份重复还难读。
所以先**量**，再让 owner 选：不动 / 抽共享骨架 / 骨架 + 登录状态与跨进程协调。

## Current state

（以 101 落地之后的形状为准；下面是 `55019a2` 上的原貌，供你核对出发点。）

**OpenCode**（`pkg/llm/opencode/transport.go:110-131`、`auth.go:58-120,440-476`）：

```go
func (t *Transport) token(ctx context.Context) (string, error) {
	...
	c, err := Load(t.Path)                          // "OpenCode login required; run /connect to log in: %w"
	if time.Until(c.ExpiresAt) > refreshMargin {    // refreshMargin = 60s；ExpiresAt 是文件里的字段
		return c.AccessToken, nil
	}
	updated, err := refresh(ctx, c)                 // POST JSON → consoleAPIBase+"/auth/device/token"；invalid_grant → 401 APIError
	...
	Save(t.Path, updated)                           // 自有格式 opencode.json，0600，临时文件 + Rename
}
```

**ChatGPT**（`pkg/llm/openai/auth.go:570-621,135-200`）：

```go
func (t *Transport) current(ctx context.Context) (Credentials, error) {
	c, err := Load(t.Path)                          // "ChatGPT login required: %w"
	if time.Until(c.ExpiresAt) > 2*time.Minute {    // ExpiresAt 由 access token（JWT）的 exp 声明推出，文件里没有这个字段
		return c, nil
	}
	tokens, err := exchange(ctx, url.Values{...})   // POST 表单 → tokenEndpoint；invalid_grant 以 APIError.Code 表达
	...                                             // 刷新后要合并 IDToken / AccountID / Email / Plan（旧值回填）
	Save(t.Path, updated)                           // 与 Codex CLI 的 auth.json 同形（嵌套 tokens、auth_mode、last_refresh），Codex 与本程序共用
}
```

**调用方**：`pkg/run/config.go`（每次构建 LLM 新建 Transport）、`pkg/process/models.go`（模型发现新建 Transport）、
`pkg/gateway/login_api.go` 与 `pkg/tui/setup.go`（登录流程与「已有登录可复用」的探测，经 `pkg/turn.Probe*`）。

**仓库约定**：设计文档放 `docs/plan/`，中文；命名 `<主题>_DESIGN.md` 或 `<主题>_PLAN.md`（例：`docs/plan/REVISED_PLAN_DIFF_DESIGN.md`，开头声明基线、逐项盘点带 `file:line` 证据、最后给裁决与实施拆解）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 行数统计 | `wc -l pkg/llm/opencode/transport.go pkg/llm/opencode/auth.go pkg/llm/openai/auth.go` | 数字 |
| 找重复骨架 | `grep -n 'CredentialMutex\|func adoptRotatedLogin\|time.Until(c.ExpiresAt)' pkg/llm/opencode/*.go pkg/llm/openai/*.go` | 命中位置 |
| 原型编译（scratchpad） | `go vet -overlay <overlay.json> ./pkg/llm/...` | exit 0 |
| 工作区干净度 | `git status --short` | 除本文档与开始前就有的脏文件外无新条目 |

## Scope

**In scope**：
- `docs/plan/OAUTH_LOGIN_FILE_DESIGN.md`（新建）

**Out of scope**：
- 仓库里**任何**代码文件。原型只写在 scratchpad（会话的临时目录），用 `go build -overlay` / `go vet -overlay` 编译验证（overlay 文件的写法见仓库外的任何 Go 文档：`{"Replace": {"<仓库内路径>": "<scratchpad 里的路径>"}}`），**不要**把它复制进仓库。
- 实施：不要开始重构两个 `Transport`。
- 真机实验（见 Step 4）需要 owner 的登录；你不做，只写成可执行的食谱。

## Git workflow

- **不提交**。改动留在 dev 工作区。

## Steps

### Step 1：盘点——骨架里哪些是共有的、哪些是各自的

读 101 落地后的 `Transport.token`（opencode）与 `Transport.current`（openai）及它们依赖的 `Load` / `Save` / `refresh` / `exchange`。
在文档 §1 里列一张表，**每一行带 `file:line`**：

| 环节 | OpenCode | ChatGPT | 共有？ |
|---|---|---|---|
| 并发保护（`llm.CredentialMutex`） | … | … | 共有 |
| 读文件与「未登录」错误文案 | … | … | 骨架共有，文案各自 |
| 到期判断（阈值、到期来源） | 60s / 文件字段 | 2min / JWT exp | 各自 |
| 刷新请求（形状、端点） | JSON | 表单 | 各自 |
| 刷新结果合并 | … | … | 各自 |
| 拒绝的识别与对外错误 | … | … | 各自 |
| 被别人先轮换时采用磁盘上的新登录（101 加的） | … | … | 骨架共有 |
| 落盘（格式、权限、原子性） | … | … | 各自 |

再统计：两份「骨架」代码各多少行（只数骨架，不数登录流程、不数 `RoundTrip` 里的 header 逻辑）。

**Verify**：文档 §1 的表每行都有 `file:line`；用 `grep -n` 对一两行抽查，位置真实。

### Step 2：在 scratchpad 里写一个共享骨架的原型，数钩子

在 scratchpad 里写 `login_file.go`（包 `llm`），形如：

```go
// LoginFile refreshes a login whose refresh token rotates on use.
type LoginFile[C any] struct {
	Path         string
	Load         func(path string) (C, error)
	LoadError    func(error) error              // wraps a Load failure in the surface's "login required" message
	Save         func(path string, c C) error
	ExpiresAt    func(C) time.Time
	RefreshToken func(C) string
	Margin       time.Duration
	Refresh      func(ctx context.Context, c C) (C, error)
	Rejected     func(error) bool               // the server says the refresh token is dead (invalid_grant)
	OnRejected   func(error) error              // what the caller sees when no rotated login can be adopted
}

func (l *LoginFile[C]) Current(ctx context.Context) (C, error) { /* lock → Load → fresh? → Refresh → adopt-or-fail → Save */ }
```

用 `go vet -overlay` 验证它能编译；再在 scratchpad 里把两个 `Transport` 的骨架改写成使用它（同样只在 scratchpad，用 overlay 编译）。记录：

- 钩子（函数字段）总数；
- 改写后两处骨架各剩多少行、共享骨架多少行、**净增减行数**；
- 改写后读代码的人要跳转几层才能看到一次完整刷新（主观，但如实写）。

**Verify**：原型能编译；文档 §2 给出上述三个数字与原型的核心签名（不必贴全部代码）。

### Step 3：写三选一与裁决规则

文档 §3 列三个选项，每个写清「做什么 / 好处 / 代价 / 估工作量」：

- **A. 不动**：保留两份，各自带 101 的锁与采用逻辑；第三个供应商出现时再说。
- **B. 抽共享骨架**（`llm.LoginFile[C]` 或同义）：只抽 Step 2 里的骨架，两个 `Transport` 的 `RoundTrip` 与登录流程不动。
- **C. B + 登录状态与跨进程协调**：在 B 之上加两件事——① 一个统一的「这台机器上某供应商的登录状态」查询（现在 `pkg/turn.Probe*`、`pkg/gateway/login_api.go` 的 `authLoginProbe`、`pkg/tui/setup.go` 各自拼）；
  ② 跨进程刷新锁（锁文件）。注意：仓库支持 Windows（`openBrowserDefault` 有 windows 分支），文件锁要按平台拆文件（`_unix.go` / `_windows.go`），而 101 已选择用「`invalid_grant` 后重读磁盘」覆盖跨进程，所以 ② 只有在 Step 4 的实验证明该方案不够时才成立。

**裁决规则**（写进文档，并据此给出你的推荐）：
- 钩子数 ≤ 6 **且**净行数下降 ≥ 30 → 推荐 B；
- 否则推荐 A（并写明「为什么抽象比重复更糟」）；
- C 的 ① 只在 B 被选中、且 owner 想统一登录状态时才推荐；② 只在 Step 4 的实验结果支持时才推荐。

**Verify**：文档 §3 有三个选项、裁决规则与你的推荐，推荐能由 §1、§2 的数字推出（读者可以复算）。

### Step 4：写跨进程实验的食谱（不执行）

文档 §4 写一份**owner 可以照着跑**的实验，回答「服务器对已被轮换的旧 refresh token，是返回 `invalid_grant`，还是宽限期内返回另一对新令牌」：

1. 用真实的 OpenCode 登录；把 `<FOREBRAIN_HOME>/opencode.json` 备份；
2. 把文件里的 `expires_at` 改成过去的时间；
3. 同时启动两个进程（例如两个 `forebrain` TUI，或一个 TUI 加网关里点「连接 OpenCode」触发的探测），在 1 秒内各发一条消息；
4. 观察：两边是否都成功？磁盘上最后的 `refresh_token` 是谁写的？之后两个进程再发消息是否仍然成功？
5. 记录结果的判读：「第二个得到 `invalid_grant`，且 101 的采用逻辑让它成功」→ 101 足够；「两边都拿到新令牌但后写者覆盖了先写者，先写者随后失效」→ 需要真正的跨进程锁（选项 C②）。

ChatGPT 同理（`auth.json`，注意它与 Codex CLI 共用，备份要仔细）。

**Verify**：食谱里每一步都是具体命令或可观察动作，没有「自行判断」。

### Step 5：落文档并收尾

把 §0（基线声明：本文基于哪个 commit、101/108/104 的落地状态）、§1–§4、§5（结论与推荐、owner 需要拍板的点）写成 `docs/plan/OAUTH_LOGIN_FILE_DESIGN.md`。

**Verify**：
- `test -f docs/plan/OAUTH_LOGIN_FILE_DESIGN.md`；`grep -c 'file:line\|\.go:[0-9]' docs/plan/OAUTH_LOGIN_FILE_DESIGN.md` ≥ 10（证据密度）。
- `git status --short` 除本文档与开始前就有的脏文件外无新条目；`git diff --name-only -- pkg` 与开始前一致。

## Test plan

无（不改生产代码）。文档本身的「可复算」性是验收点：评审方会按 §1、§2 的方法重算一遍钩子数与净行数。

## Done criteria

- [ ] `docs/plan/OAUTH_LOGIN_FILE_DESIGN.md` 存在，含 §0–§5
- [ ] §1 的表每行带 `file:line`；§2 有钩子数、两处骨架行数、共享骨架行数、净增减
- [ ] §3 有 A/B/C 三选项、裁决规则与推荐
- [ ] §4 有可执行的实验食谱
- [ ] `git status --short` 里新增条目只有该文档；仓库内无任何代码文件被修改（scratchpad 的原型不算）
- [ ] 没有任何 `git commit` / `git push` / 新分支

## STOP conditions

1. 计划 101 没落地（`CredentialMutex` 不存在）。
2. 你发现自己想把原型放进仓库、或想「顺手」改两个 `Transport`——不属于 spike，停下。
3. 读代码时发现第三份「登录文件 + 刷新」的拷贝（别的 provider 包里）——在文档里记下并告诉 owner，这会改变推荐。
4. 原型无法编译且两次修复仍不行——说明泛型签名不合适，把这一点写进文档本身（它就是证据），不要硬凑。

## Maintenance notes

- 本 spike 的价值在**裁决依据**而不是抽象本身；若 owner 选 A，这份文档就是「为什么不抽」的记录，下次有人想抽之前先读它。
- 选 B 或 C 后另立实施计划：必须以 101（以及 108/104）的测试为安全网，要求行为零变化（错误文案、落盘格式、Codex 互通格式都不动）。
- 评审重点：Step 2 的钩子计数是否诚实（把 `LoadError`、`OnRejected` 这类「为了保持文案不变」的钩子也算进去）。
