# 修复：/connect 里两个同名 Volcengine Ark 行，Esc 回退后选中错行

> **Executor instructions**: 按 Steps 顺序执行；每步先跑验证命令确认预期结果再前进。
> 触发任何 STOP condition 立即停下报告，不要即兴发挥。改动留在工作区，不 commit。
> 本计划按 owner 裁决**直接在主树实施**（不用 worktree）；若改用 worktree，先 symlink 主树的
> `third_party/libtokenizers` 与 `third_party/pdfium` 再构建，否则 CGO 链接会报
> `library 'tokenizers' not found`。
> 本计划文件在批准后抄录到仓库 `docs/plan/CONNECT_VOLCENGINE_ROW_SELECTION_PLAN.md`（该目录 gitignore，不入库）。

## Status

- **Status**: 已实施并真机验收通过（2026-11-13）
- **Priority**: P1（owner 报障的用户可见交互 bug）
- **Effort**: S（`pkg/tui/setup.go` 一个匹配循环 + 两个回归测试）
- **Risk**: LOW（只改选择恢复的身份判定；不动行、分组、标签与任何 provider 语义）
- **Depends on**: none
- **Planned at**: commit `c3377e14`，2026-11-13；工作区另有其他会话对 `pkg/run/*` 与 `pkg/process/peer_inbound*.go` 的在途改动，与本计划无关
- **Owner decision（2026-11-13）**: 保留两个 Volcengine Ark 行，只修 bug。证据见「为什么不删/不合并」。
- **Review（2026-11-13，glm-5.3）**: 独立 worktree 复现——baseline 上订阅方向 FAIL `the page reopened on row [-1 13], want the sign-in row 0`，落本计划修复后两条 PASS；全量 `./pkg/tui`、vet、gofmt 绿。评审提出的三处补充已并入本版（端点页 `method` 同型路径、在途脏文件补全、worktree 执行备注）。

## 目标（Why this matters）

TUI `/connect` 第一页有两个 `Volcengine Ark` 行：

- `Sign in with a subscription` 分组下的订阅登录行（注册表 id `volcengine`）；
- `Use an API key` 分组下的 provider 组行（5 个端点，含按量付费与 OpenAI 兼容）。

用户选中订阅行、按 Esc 退回第一页时，游标落在 `Use an API key` 分组下的同名行。修复后必须回到用户刚才选的那一行；两行的功能与标签一律不变。

## 根因（file:line 证据链 + 复现）

### 代码链

1. 第一页的行由 `providerRows` 生成（`pkg/tui/setup.go:727-760`）：先列订阅行，再列 API-key provider 组行。
2. 订阅行的 `providerChoice` 由 `subscriptionChoice` 生成（`pkg/tui/setup.go:764-781`）：

   ```go
   choice := providerChoice{
       Group:        label,   // "Volcengine Ark"
       Label:        label,   // "Volcengine Ark"
       Hint:         hint,
       Provider:     sub.ID(),
       Subscription: sub.ID(),
   }
   ```

3. API-key 组行的显示标签来自 `providerGroupsFromRegistry`（`pkg/tui/setup.go:311-341`），`providerGroups` 的 `Volcengine Ark` 组由 `pkg/config/provider_registry.go:224-249` 的 registry 条目生成：`Label: "Volcengine Ark"`，5 个端点（Coding Plan / Agent Plan / 两个 OpenAI 兼容 / Pay as you go）。
4. 回到第一页时，`pickProvider` 用**显示文本**恢复游标（`pkg/tui/setup.go:824-837`）：

   ```go
   chosen := -1 // the provider picked before, when the user came back here
   for i, row := range rows {
       items[i] = row.item
       switch {
       case row.group != nil:
           // A provider with several endpoints: the user came from its
           // endpoint page, so its row is where they were.
           if row.item.Label == s.choice.Group {
               chosen = i
           }
       case row.choice.Group == s.choice.Group && row.choice.Label == s.choice.Label:
           chosen = i
       }
   }
   ```

   订阅行在 `case row.choice.Group == ...` 命中（`Group`/`Label` 都是 `"Volcengine Ark"`）；循环继续到后面的 API-key 组行，`row.item.Label == s.choice.Group` 再次命中并**覆盖** `chosen`。两个行同名时，后出现的 API-key 行获胜。

### 现场复现（tmux 真机，当前源码 c3377e14）

用 `.claude/skills/run-forebrain/driver.sh`（隔离 home/项目、fake provider、端口 8765）：

1. `$D submit '/connect'`，第一页显示：

   ```
   Sign in with a subscription
   ❯ ChatGPT                 Use Codex included with your ChatGPT plan
     OpenCode                Device-code login; use your Go/Go Plus quota
     Volcengine Ark          ark.cn-beijing.volces.com
     Qoder CN                Browser authorization; use your Qoder CN plan quota

   Use an API key
     OpenAI                  GPT models via OpenAI API
     Anthropic               Claude models via Anthropic API
     Qwen Cloud              4 plans by region and billing
     ↓ 14 more
   ```

2. `Down` `Down` `Enter` → 进入订阅登录页：`Sign in with Volcengine Ark`。
3. `Escape` 退回第一页，屏幕显示（列表已滚动到错误行）：

   ```
   Google                  Gemini via OpenAI-compatible endpoint
   ...
   Tencent Cloud TokenHub  2 plans by region and billing
   ❯ Volcengine Ark          5 plans by region and billing
     ↑ 10 more · ↓ 4 more
   ```

   期望是订阅行（`Volcengine Ark  ark.cn-beijing.volces.com`），实际是 API-key 组行（`5 plans by region and billing`）。

### 单测复现（已跑通，临时测试文件已删）

用 `withSubscriptions(t, volcengineLike())` 把注册表缩成只剩 volcengine，直接调两次 `pickProvider`：

```
row 0 label="Volcengine Ark" group=false subscription="volcengine"
row 13 label="Volcengine Ark" group=true subscription=""
default indexes = [-1 13], choice = {Group:Volcengine Ark Label:Volcengine Ark ... Subscription:volcengine}
--- FAIL: TestConnectVolcengineEscRepro
```

第二次 `pickProvider` 传给选择器的 `defaultIdx` 是 13（API-key 组行），不是 0（订阅行）。修复后应为 0。

### 已排除的竞争解释

- **选择器/渲染层 bug**：不是。`pickProvider` 自己把 `defaultIdx=13` 传给 `SelectRich`；渲染只是忠实打开该行。
- **Esc 键映射错**：不是。Esc 确实退回了第一页，且同一路径对其它订阅（ChatGPT/OpenCode/Qoder CN）回到正确行。
- **注册表把两行当成了同一行**：不是。`row 0` 与 `row 13` 是两条独立记录，`Subscription` 字段只在订阅行有值。

### 为什么不删/不合并（owner 已裁决保留两行）

`Use an API key` 分组下的 Volcengine Ark 不是完全冗余：

- `pkg/subscription/volcengine.go:280-339` 的 `Offers` 只列账号持有的 Coding Plan / Agent Plan / 团队席位；账号没有计划时返回 `this Volcengine account holds no Ark plan`（:329-334）。**Pay as you go 不是计划，订阅路径不提供。**
- `pkg/config/provider_registry.go:230-236` 的 5 个端点里，`Pay as you go`（`/api/v3`）和两个 OpenAI 兼容端点只有 API-key 行提供；且 API-key 行不需要控制台登录。
- `docs/plan/VOLCENGINE_CONNECT_SAVE_API_KEY_PLAN.md` 也把「含手填 key 的按量付费端点」列为 volcengine 不能标 `LoginBacked` 的原因。

删掉该行会让按量付费 / OpenAI 兼容 / 免登录用户失去入口；合并成一行则要把 5 个 API-key 端点塞进订阅入口页，属更大的 UI 改动。owner 选择保留两行、只修 bug。

## 修复设计

在 `providerRows` 附近新增一个身份判定函数，并让 `pickProvider` 的恢复循环用它：

```go
// providerRowHolds reports whether row is the one the user picked before. A
// sign-in is named by its subscription, a provider with several endpoints by
// its group, and a single-endpoint provider by its choice. Display labels
// cannot name it: a subscription and an API-key group may share one
// (Volcengine Ark), and the later row would win.
func providerRowHolds(row providerRow, choice providerChoice) bool {
    switch {
    case choice.Subscription != "":
        return row.choice.Subscription == choice.Subscription
    case row.group != nil:
        return row.item.Label == choice.Group
    default:
        return row.choice.Subscription == "" &&
            row.choice.Group == choice.Group && row.choice.Label == choice.Label
    }
}
```

`pickProvider` 内改为：

```go
chosen := -1 // the provider picked before, when the user came back here
for i, row := range rows {
    items[i] = row.item
    if providerRowHolds(row, s.choice) {
        chosen = i
    }
}
```

`setChoice`（`pkg/tui/setup.go:682-695`）的 `Group+Label` 早退**不改**：当前注册表下订阅行与 API-key 端点的 `Label` 不同（`"Volcengine Ark"` vs `"Coding Plan"`），该分支不可达；按「不为不可达状态写防御代码」的既有裁决保持最小改动。维护注意里记录该身份比较的遗留问题。

### 行为变化矩阵

| 场景 | 旧行为 | 新行为 |
|---|---|---|
| 选订阅 `Volcengine Ark` → Esc 回第一页 | 游标落在 API-key 组行（row 13） | 游标落在订阅行（真实注册表 row 2；测试注册表 row 0） |
| 同上，用户可见的列表位置 | 列表滚动到 API-key 组行，`❯` 落在 `5 plans by region and billing` | 列表回到订阅行，`❯` 落在 `ark.cn-beijing.volces.com` |
| 选 API-key `Volcengine Ark` → 端点页 → Esc 回第一页 | 游标在组行（row 13） | 不变（仍 row 13） |
| 选 ChatGPT / OpenCode / Qoder CN 订阅 → Esc | 自己的订阅行 | 不变 |
| 选 OpenAI / Anthropic / DeepSeek 等单端点 provider → Esc | 自己的行 | 不变 |
| 第一次打开第一页（无历史选择） | 第一行 | 不变（`chosen == -1`） |
| 选 Custom model → Esc | Custom 行 | 不变 |
| 在端点页 Esc | 刚离开的组行 | 不变（`chosen = idx` 路径，未改） |

## Scope

**In scope**（只改这两个文件）:

- `pkg/tui/setup.go` — 新增 `providerRowHolds`，替换 `pickProvider` 的 `chosen` 恢复循环。
- `pkg/tui/setup_test.go` — 新增 `indexSelector` 测试辅助 + 两条回归测试（订阅行回退、API-key 行回退）。

**Out of scope**（看起来相关但明确不碰）:

- `pkg/config/provider_registry.go`、`pkg/subscription/volcengine.go`、`pkg/gateway/*` — 不改行、端点、标签、订阅语义。
- `pkg/tui/overlays.go`、`pkg/tui/chat_slash.go`、`pkg/tui/chat_session_test.go` 的 `fakeSelector` — 不扩展共享 fake，不碰 `/model` 列表（另一会话在改）。
- `pkg/tui/setup.go` 的 `providerRows`、`setChoice`、`categoryOf` — 不改行集合与分组。
- `pkg/run/*` — 工作区已有其他会话在途改动，绝不触碰。
- 删除/合并 Volcengine Ark 行 — owner 已裁决保留两行。

## Baseline 与 Drift check（先跑）

Baseline: `git rev-parse --short HEAD` = `c3377e14`。工作区可能显示 `pkg/run/*` 与 `pkg/process/peer_inbound*.go` 的在途改动（同属另一会话），与本计划无关。

```bash
git rev-parse --short HEAD
# 预期：c3377e14

git status --short pkg/tui/setup.go pkg/tui/setup_test.go
# 预期：无输出（两个 in-scope 文件干净）

grep -n 'chosen := -1 // the provider picked before' pkg/tui/setup.go
# 预期：824

grep -n 'row.item.Label == s.choice.Group' pkg/tui/setup.go
# 预期：831

grep -n 'func (s \*llmSetup) providerRows' pkg/tui/setup.go
# 预期：727

grep -n 'func (s \*llmSetup) setChoice' pkg/tui/setup.go
# 预期：685

grep -n 'type fakeSubscription struct' pkg/tui/setup_test.go
# 预期：288

grep -n 'type dismissingSelector struct' pkg/tui/setup_test.go
# 预期：501
```

行号漂移不算漂移；上述符号/语句内容不符（例如 `chosen` 循环已被别人改过、`setup_test.go` 已被别人编辑）→ **STOP**，先 `peer_send` 确认归属再重写计划。

## Steps

### Step 1：加回归测试，先在 baseline 上跑成红色

文件：`pkg/tui/setup_test.go`。在 `dismissingSelector`（:498-519）之后新增：

```go
// indexSelector answers the provider page by row index and records the row the
// page opened on, which is what a label cannot say when two rows share one.
type indexSelector struct {
	*fakeSelector
	answers  []int
	at       int
	defaults []int
}

func (s *indexSelector) SelectRich(label string, items []SelectItem, defaultIdx int) (int, bool, error) {
	s.defaults = append(s.defaults, defaultIdx)
	if s.at >= len(s.answers) {
		return -1, false, nil
	}
	answer := s.answers[s.at]
	s.at++
	if answer < 0 {
		return -1, false, nil
	}
	return answer, true, nil
}
```

新增两条测试（放在 `TestEscOnOffersPageReturnsToProviderPage` 之后，:725 附近）：

```go
// The sign-in row and the API-key group row are both called "Volcengine Ark".
// Coming back from the sign-in must land on the sign-in row, not on the later
// row that shares its label.
func TestProviderPageComesBackToTheSubscriptionRow(t *testing.T) {
	sub := volcengineLike()
	withSubscriptions(t, sub)
	selector := &indexSelector{fakeSelector: &fakeSelector{}}
	setup := newLLMSetup(PromptConfig{Selector: selector, Output: io.Discard, Home: t.TempDir()})

	subRow, keyRow := -1, -1
	for i, row := range setup.providerRows() {
		if row.choice.Subscription == sub.ID() {
			subRow = i
		}
		if row.group != nil && row.item.Label == sub.label {
			keyRow = i
		}
	}
	if subRow < 0 || keyRow < 0 || subRow == keyRow {
		t.Fatalf("the page no longer holds two Volcengine Ark rows: sign-in=%d key=%d", subRow, keyRow)
	}

	selector.answers = []int{subRow, -1}
	if _, err := setup.pickProvider(); err != nil {
		t.Fatalf("pick the sign-in row: %v", err)
	}
	if setup.choice.Subscription != sub.ID() {
		t.Fatalf("choice = %+v, want the %s sign-in", setup.choice, sub.ID())
	}
	if _, err := setup.pickProvider(); err != nil {
		t.Fatalf("come back: %v", err)
	}
	if len(selector.defaults) != 2 || selector.defaults[1] != subRow {
		t.Fatalf("the page reopened on row %v, want the sign-in row %d", selector.defaults, subRow)
	}
}

// The other direction: a key endpoint's row is the group row, and coming back
// must not be pulled onto the sign-in row by the shared label.
func TestProviderPageComesBackToTheAPIKeyRow(t *testing.T) {
	sub := volcengineLike()
	withSubscriptions(t, sub)
	selector := &indexSelector{fakeSelector: &fakeSelector{}}
	setup := newLLMSetup(PromptConfig{Selector: selector, Output: io.Discard, Home: t.TempDir()})

	keyRow := -1
	for i, row := range setup.providerRows() {
		if row.group != nil && row.item.Label == sub.label {
			keyRow = i
		}
	}
	if keyRow < 0 {
		t.Fatalf("no API-key group row for %s", sub.label)
	}

	selector.answers = []int{keyRow, 0, -1}
	if _, err := setup.pickProvider(); err != nil {
		t.Fatalf("pick the key group: %v", err)
	}
	if setup.choice.Provider != sub.ID() || setup.choice.Subscription != "" {
		t.Fatalf("choice = %+v, want a %s key endpoint", setup.choice, sub.ID())
	}
	if _, err := setup.pickProvider(); err != nil {
		t.Fatalf("come back: %v", err)
	}
	if len(selector.defaults) != 3 || selector.defaults[2] != keyRow {
		t.Fatalf("the page reopened on row %v, want the API-key group row %d", selector.defaults, keyRow)
	}
}
```

验证（在未改 `setup.go` 的 baseline 上）：

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestProviderPageComesBack' -count=1 -v
# 预期：TestProviderPageComesBackToTheSubscriptionRow FAIL（reopened on row 13, want 0）
#       TestProviderPageComesBackToTheAPIKeyRow PASS（旧代码在该方向本来就对）
```

### Step 2：改 `pkg/tui/setup.go`

在 `providerRows`（:727-760）之后、`subscriptionChoice`（:762）之前新增 `providerRowHolds`（见「修复设计」），并把 `pickProvider` 的恢复循环（:824-837）替换为 `if providerRowHolds(row, s.choice) { chosen = i }`。其余代码不动。

验证：

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestProviderPageComesBack' -count=1 -v
# 预期：两条都 PASS
```

### Step 3：包级回归 + 静态检查

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestProviderPageListsThreeGroups|TestProviderPageMarksASubscriptionAlreadySignedIn|TestEscOnOffersPageReturnsToProviderPage|TestSubscriptionFlowDropsSettledSignInPage' -count=1
# 预期：all pass

CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1
# 预期：ok（若失败在未改文件，先 peer_send 问归属，不要代修）

CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui
# 预期：exit 0

gofmt -l pkg/tui
# 预期：无输出
```

### Step 4：真机验收（tmux，fake provider）

```bash
export FOREBRAIN_RUN_WORK="$TMPDIR/fb-connect-fix" FOREBRAIN_RUN_SESSION=fb-connect-fix FOREBRAIN_RUN_PORT=8766
D=.claude/skills/run-forebrain/driver.sh
$D reset; $D build
$D start reply ok
$D submit '/connect'
```

**场景 1（复现→修复）**：`Down` `Down` `Enter`（订阅 Volcengine Ark）→ `Escape` 回第一页。预期屏幕出现：

```
❯ Volcengine Ark          ark.cn-beijing.volces.com
```

且**不出现** `❯ Volcengine Ark          5 plans by region and billing`（后者是 API-key 组行）。用 `$D screen | grep -n '❯ Volcengine Ark'` 取证。

**场景 2（另一方向不回归）**：重新 `$D submit '/connect'`，在 `⌕ Type to filter` 里输入 `Volcengine`（两行同名，订阅行在前、API-key 组行在后），按 `Down` 选到 `5 plans by region and billing` 的组行，`Enter` 打开端点页 → `Escape` 回第一页。预期游标仍在该组行，不跳到订阅行。

**场景 3（订阅行仍可用）**：`$D submit '/connect'` → 订阅 `Volcengine Ark` → `Enter`，预期仍进入 `Sign in with Volcengine Ark` 页面；`Escape` 退出向导，TUI 回到 composer，无错误行。

收尾：`$D stop`；`tmux kill-session -t fb-connect-fix 2>/dev/null || true`；临时目录由 driver 管理，不碰真实 `~/.forebrain`。

### Step 5：收口

- 计划文件抄录到 `docs/plan/CONNECT_VOLCENGINE_ROW_SELECTION_PLAN.md`，状态改「已实施并真机验收通过（2026-11-13）」，补「验收记录」。
- `git status --short` 确认改动只在 `pkg/tui/setup.go`、`pkg/tui/setup_test.go`（外加 docs/plan 的 gitignored 计划文件）；`pkg/run/*` 与 `pkg/process/peer_inbound*.go` 的在途改动原样保留。
- 报告根因链、最小改动、验证证据、遗留限制。

## Done criteria（机器可查）

- `git diff --stat` 仅含 `pkg/tui/setup.go` 与 `pkg/tui/setup_test.go`（计划文件在 gitignore 内，不计）。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` 全绿。
- `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui` exit 0；`gofmt -l pkg/tui` 无输出。
- 真机场景 1 的 `capture-pane` 中，`❯` 落在订阅行（描述含 `ark.cn-beijing.volces.com`），不落在 `5 plans by region and billing` 行。
- 真机场景 2/3 通过。
- 未改 `pkg/config`、`pkg/subscription`、`pkg/gateway`、`pkg/tui` 其它文件。

## STOP conditions

- Drift check 任一 in-scope 文件内容与「根因」摘录不符（例如 `chosen` 循环已被别人改过）→ STOP，先 `peer_send` 确认归属。
- Step 1 的 `TestProviderPageComesBackToTheSubscriptionRow` 在 baseline 上**通过**（bug 未复现）→ STOP，说明根因判断或测试构造有误，不要继续改源码。
- 修复需要改 `providerRows`、分组、标签或其它文件才能通过测试 → STOP（owner 已裁决保留两行，超出 scope）。
- `pkg/tui` 包级测试失败在未改动文件（尤其 `chat_session_test.go`/`overlays.go`）→ 报给对应会话，不代修。
- 真机第一页不再同时出现两个 Volcengine Ark 行（注册表被改）→ STOP，重新对齐需求。
- 连续两次修复后测试仍红 → 停下重新评估，不叠加补丁。

## Maintenance notes

- 行身份判定的唯一可靠来源是 `providerChoice.Subscription`（订阅 id）与 `providerRow.group`（端点组）；显示标签可以合法重名（订阅标签 == provider 组标签，Volcengine Ark 是第一个实例）。以后任何「回到上一页恢复游标」的逻辑都必须用身份字段，不能用 `Label`/`Group` 文本。
- `setChoice`（`pkg/tui/setup.go:685-695`）仍用 `Group+Label` 判断「同一个选择」；当前注册表下订阅行与 API-key 端点的 Label 不同，不可达冲突，故本期不动。若未来出现「单端点 provider 的 choice label 等于订阅 label」，需要把它一并改成身份比较。
- 端点页的 `method` 恢复（`pkg/tui/setup.go:853`，`option.Group == s.choice.Group && option.Label == s.choice.Label`）是第三处同型「按显示文本恢复」；当前每个 provider 组内端点 label 互异、且订阅 label 不等于任何端点 label，故无现行 bug。若未来某端点的 `ChoiceLabel` 与订阅 label 撞名，同样要改成身份比较。
- 两个 Volcengine Ark 行是刻意保留的：订阅行负责计划发现/团队席位 key；API-key 行负责按量付费、OpenAI 兼容端点、免控制台登录。删除 API-key 行会移除这些路径（证据：`pkg/subscription/volcengine.go:280-339` 无计划即报错；`pkg/config/provider_registry.go:230-236` 的 5 个端点）。
- 验收用 fake provider + 隔离 `FOREBRAIN_HOME`，不碰真实 `~/.forebrain`；构建必须带 `-tags fts5`。

## 验收记录（2026-11-13）

Drift check（baseline `c3377e14`）：两个 in-scope 文件 `git status --short` 无输出；`chosen` 循环在 `setup.go:824`、`row.item.Label == s.choice.Group` 在 `:831`、`providerRows` 在 `:727`、`setChoice` 在 `:685`、`fakeSubscription` 在 `setup_test.go:288`、`dismissingSelector` 在 `:501`，全部与计划一致。

Step 1（未改 `setup.go` 的 baseline 上跑新测试）：

```
--- FAIL: TestProviderPageComesBackToTheSubscriptionRow
    setup_test.go:782: the page reopened on row [-1 13], want the sign-in row 0
--- PASS: TestProviderPageComesBackToTheAPIKeyRow
```

Step 2（新增 `providerRowHolds` + 替换恢复循环后）：两条均 PASS。

Step 3：

- `go test -tags fts5 ./pkg/tui -run 'TestProviderPageListsThreeGroups|TestProviderPageMarksASubscriptionAlreadySignedIn|TestEscOnOffersPageReturnsToProviderPage|TestSubscriptionFlowDropsSettledSignInPage'` → ok。
- `go test -tags fts5 ./pkg/tui -count=1` → ok（82.992s）。中途一次失败是 `TestAvailableModelOptionsUseConfiguredLLMProvidersOnly`（`/model` 列表排序），归属并行会话 forebrain-harness-21 的在途改动，由该会话修正后本次全量转绿，未由本计划代改。
- `go vet -tags fts5 ./pkg/tui` → exit 0；`gofmt -l pkg/tui` → 无输出。

Step 4（tmux 真机，fake provider，端口 8766，隔离 home）：

- 场景 1：`/connect` → `Down Down Enter`（订阅 Volcengine Ark）→ `Escape`，`capture-pane` 第 32 行为 `❯ Volcengine Ark          ark.cn-beijing.volces.com`，全屏无 `❯ Volcengine Ark          5 plans by region and billing`。
- 场景 2：重新 `/connect` → 过滤框输入 `Volcengine` → `Down Down` 落在 `❯ Volcengine Ark          5 plans by region and billing` → `Enter` 打开 5 端点页（Coding Plan / Agent Plan / 两个 OpenAI 兼容 / Pay as you go）→ `Escape` 回第一页，`❯` 仍在 `5 plans by region and billing` 行。
- 场景 3：`/connect` → 订阅 Volcengine Ark → `Enter` 进入 `Sign in with Volcengine Ark` 页 → `Escape` 回第一页 → 再 `Escape` 退出向导，回到 composer，屏幕无错误行。

改动文件：`pkg/tui/setup.go`（+`providerRowHolds`，恢复循环改为身份判定）、`pkg/tui/setup_test.go`（+`indexSelector` 与两条回归测试）。

