# 修复：/connect 订阅模型页不回显已添加的模型（Volcengine coding/agent plan、OpenCode）

> **Executor instructions**: 按 Steps 顺序执行；每步先跑验证命令确认预期结果再前进。触发任何
> STOP condition 立即停下报告，不要即兴发挥。改动留在工作区，不 commit。
> 本计划按 owner 惯例**直接在主树实施**（不用 worktree）。计划文件批准后抄录到仓库
> `docs/plan/CONNECT_OFFERS_ECHO_PLAN.md`（该目录 gitignore，不入库）。

## Status

- **Status**: 已实施并真机验收通过（2026-11-14）
- **Priority**: P1（owner 报障的用户可见交互 bug；Volcengine + OpenCode 两个订阅，同一根因）
- **Effort**: M（TUI 3 处 + 引擎 1 处共享判定 + gateway 1 个字段 + web 预选；7 个源码文件 + 5 个测试文件）
- **Risk**: MEDIUM（`pkg/process.UpsertLLMModel` 的行识别规则收窄了"登录型 provider 的 api_path
  不参与身份"这一条；会影响所有订阅写入与 gateway 模型页编辑路径，故按 HIGH 的包级回归验证）
- **Owner decision（2026-11-13）**: 回显范围 = **TUI + web 一起**（web 订阅登录弹窗同步改为预选已添加模型）
- **Review（2026-11-13，zhipuai/glm-5.3）**: approve with changes。独立 worktree 复现（基线双红
  `checked rows = [0]`）+ 按设计 A+B 落地后转绿、五个 Go 包全绿；身份设计（volcengine 非 LoginBacked、
  登录型 path 只作展示）与真库双行证据均获独立核实。四处修正已并入本版：drift grep 大小写
  （`rowKey == ""`，小写）/ Step 0 第二条用例改成生产形态（offer 无 APIPath、只有迁移行带 path）/
  真库行数以副本库现值为准 / helper 名统一 `offerRowSeed` / 评审 worktree 清理需 `--force` /
  基线 `git status` 非空时的判读规则。评审留的 worktree `.worktrees/plan-review-382470194` 在本次改动
  落地后由执行者 `git worktree remove --force .worktrees/plan-review-382470194` 清理（它里面有未跟踪的
  `third_party` 符号链接和评审自己的验证改动，属评审草稿、不是交付物；不加 `--force` 会因
  "contains modified or untracked files" 失败）。
- **Planned at**: 工作区基线 commit `c32d23d5`（bulk commit 已落，工作树干净），2026-11-13
- **并行会话**: `pkg/subscription/volcengine.go` 与 `pkg/gateway/subscriptions_api.go`（+ 两者的测试）同时被
  forebrain-harness-81 修改（cap_images 链路：`volcengineModelImages`/`volcengineModelOffers`/`Offers`、
  `registry.go` 的 `offerRowInput`、`Offer.SupportsImages` 改 `*bool` 及其在 `subscriptionOffer` 里的下落）。
  本计划在 volcengine.go 只改 `Provision`（~170-217 行）+ 紧邻新增的一个 helper；在 subscriptions_api.go 只加
  `subscriptionOffer.Held` 字段与 `handleSubscriptionOffers` 里的计算。**等 81 报告这两个文件定稿后再动**
  （Steps 第 3、5 步有同步点）。

## 目标（Why this matters）

TUI `/connect` → `Sign in with a subscription` → 某个订阅 → 模型页（offers 页，标题
`<订阅名>\nChoose the models to add; each row shows where requests go.`）用于勾选要把哪些模型写进
`fb_llm_models`（也就是 `/model` 面板里的模型列表）。owner 报：Volcengine Ark 的 `Coding Plan`
与 `Agent Plan` 两组模型、以及 OpenCode 的模型页，**没有回显已经添加进 `/model` 的模型**——页面
只勾了厂商列表里的第一行，用户看不出哪些已经加过、很容易重复添加。

web（设置 › 模型 › 订阅登录弹窗）是同一个缺陷的第二个 surface（`loadOffers` 只预选第一条 default），
owner 裁决本轮一起统一（2026-11-13）。

## 根因（file:line 证据链 + 复现）

### 代码链

1. 模型页的初始勾选只有两个来源（`pkg/tui/setup.go:1361-1364`，`pickOffers`）：

   ```go
   checked := s.chosen
   if checked == nil {
       checked = defaultOffers(s.offers)
   }
   ```

2. `defaultOffers` 只认厂商列表自带的 `Default` 标记（`pkg/tui/setup.go:1478-1492`）：

   ```go
   func defaultOffers(offers []subscription.Offer) []int {
       checked := make([]int, 0, len(offers))
       for i, offer := range offers {
           if offer.Default { checked = append(checked, i) }
       }
       if len(checked) == 0 && len(offers) > 0 { checked = append(checked, 0) }
       return checked
   }
   ```

3. `Default` 的来源与"本机已有什么行"无关：
   - Volcengine：`pkg/subscription/volcengine.go:335-337` 无条件 `offers[0].Default = true`
     （coding plan + agent plan 合并列表的第一行）；
   - 列表型订阅：`pkg/subscription/registry.go:360-376` `offersFromRecords` 里
     `Default: record.IsDefault`，即厂商目录里的第一个模型（`pkg/turn/models.go:137-139`、
     `:262-266`）。

4. 全仓**没有任何**路径让这个页面读 `fb_llm_models`。行身份规则
   （`appcfg.NormalizeLLMModelIdentity` + `state.LLMModelStore.FindByIdentity`）只被
   写入路径（`subscription.ProvisionRows` → `process.UpsertLLMModel`）和 **subagent 授权页**
   （`pkg/tui/setup.go:1788-1799` `storedOfferAllowed`）用到，模型页没有用。

**结论**：模型页无从"回显"——它只可能显示厂商自己的第一行。这是缺失读取，不是匹配写错。

### 单测复现（已在基线 c32d23d5 上跑通，脚本随后删除）

临时测试 `pkg/tui/zz_echo_repro_test.go`：预先把某条 offer 对应的行写进临时 state 库（模拟上一次
`/connect` 已添加），然后走同一个向导到模型页，断言 `TabbedSelectOptions.Checked` 的初值。

```
--- FAIL: TestZZOffersPageEchoesVolcengineRows (0.01s)
    zz_echo_repro_test.go:62: checked rows = [0], want the already added row [1]
--- FAIL: TestZZOffersPageEchoesOpenCodeRows (0.01s)
    zz_echo_repro_test.go:89: checked rows = [0], want the already added row [1]
```

Volcengine 用例用的是真实形态：`coding-plan` 的 offer（`https://ark.cn-beijing.volces.com/api/coding`
+ `/v1/messages`，`KeyRequired`）+ `agent-plan` 的 offer（`…/api/plan` + `/v1/messages`），已添加的是
**第二条**（agent plan）；页面却勾了第 0 条。

### 现场证据（真实安装，只读）

`~/.forebrain/state/forebrain.state.sqlite`（`sqlite3 -readonly`）里：

- 13 条 `provider=volcengine` 行：`source=volcengine`、`source_plan=coding-plan|agent-plan`、
  `base_url` 分别是 `…/api/coding`、`…/api/plan`、`api_path=/v1/messages` —— **与
  `volcenginePlanEndpoint`（`pkg/subscription/volcengine.go:489-495`）产出的 offer 身份完全一致**。
  所以 Volcengine 只差"去读它"。
- 16 条 `provider=opencode-go` 行，两种形态并存：
  - 8 条 `api_path=''`：当前 `/connect` 写的（`offersFromRecords` 不设 `APIPath`）；
  - 8 条 `api_path='/chat/completions'` 或 `/v1/messages'`：老 YAML 配置行被
    `process.createLegacyModelRows`（`pkg/process/llm_models.go:398-421`）经
    `loginModelEndpoint`（同文件 `:459-493`，"the table has to carry the endpoint"）抬进表时写入了
    展示用的 path；
  - `deepseek-v4.1-flash` **存在两条**（两种 path 各一条）→ 同一个模型因为"展示用 path"被当成了两行。

### 为什么"登录型 provider 的 api_path"不能算身份

- `pkg/config/llm_models.go:54-65`：`CredentialsBackedProvider` 的行保留 `""`，注释写明
  "their endpoint comes from the login"。
- `pkg/gateway/models_api.go:llmModelProtocol`：登录型行无论 path 是什么，协议列都显示 login/`sign-in`。
- `pkg/run/config.go:661`：opencode 的协议由 `opencode.ProtocolFor(provider, model)` 决定，行里的
  path 在运行期**被忽略**；`pkg/llm/opencode/endpoints.go:96-124` 是这张表。

即：登录型 provider 的行身份是 (provider, model, base_url)，`api_path` 只是 models 页的展示提示。
Volcengine **不是**登录型（`pkg/config/provider_registry.go:226-252` 没有 `LoginBacked`），它的 plan
端点靠 path 区分（`/api/coding` vs `/api/plan`，以及 `/v3` 的 OpenAI 兼容端点），path 必须算身份。

### 已排除的竞争解释

- **选择器/渲染 bug**：不是。断言的是 `pickOffers` 交给选择器的 `Checked` 本身（复现直接打印
  `checked rows = [0]`），渲染只是忠实打开它。
- **offer 列表本身缺模型**：不是。两个复现用例里目标模型都在列表内，只是没被勾。
- **"第一次登录只勾一个"是刻意设计**：是刻意的，但只对**首次登录**成立（web 同一句注释
  `SubscriptionLoginModal.vue:521-526`："A first sign-in needs one model…"）。本机已经有行时仍走
  同一分支，才是 bug。
- **provider 名字对不上**：不是。真实行的 `provider`/`source` 就是 `volcengine` / `opencode-go`。
- **Volcengine 的 plan 标签对不上**：不是。真实行 `source_plan` 就是 `coding-plan`/`agent-plan`，
  offer 的 `Plan` 同值。

### 同族缺陷（本次一并根因修掉）

1. **写入身份**：`process.UpsertLLMModel`（`pkg/process/llm_models.go:667-731`）用四元组
   （含 path）`FindByIdentity`。登录型 provider 的行只要展示 path 不同，重新 provision 就会
   **新插一行**——这就是上面 `deepseek-v4.1-flash` 两条的成因。修好回显后"回车重写已勾选项"会成为
   默认动作，这条不修就会量产重复行。
2. **Volcengine 个人 plan 重复要 key**：`pkg/subscription/volcengine.go:196-215` 只要
   `opts.Key` 为空就返回 `ErrPlanKeyRequired`，即使该行已经存着 key。owner 已在 commit
   `497db730`（"fix(setup): the save stops asking for a key the row already has"）裁定：
   key 的要求属于 `UpsertLLMModel`，"keeps an existing row's stored key, refuses only a row that
   would end up keyless"。回显后若不修这条，用户对着已经添加的模型按回车会被要求重新粘贴
   Coding Plan / Agent Plan 的 key。

### 明确**不**在本期修（相邻问题，报告即可）

- 一次勾选里同时含 **两个** 个人 plan 的新模型（coding + agent）时，`ProvisionOptions.Key` 只有一个，
  两个 plan 的 key 无法区分（pre-existing，`pkg/subscription/registry.go:104-114` 的契约问题，
  要改 ProvisionOptions + gateway API + web 表单）。
- 已经存在的重复行（`deepseek-v4.1-flash` × 2）不删除：删行可能删掉会话当前使用的行，属数据清理，
  另议。

（web 订阅弹窗的同一缺陷本期**一起修**：owner 2026-11-13 裁决两个 surface 同一轮统一，见"修复设计 D/E"。）

## 修复设计

### A. 一处共享判定：`pkg/process/llm_models.go`

新增导出函数（放在 `UpsertLLMModel` 之前），把"本机是否已经有这一行的行"这件事收成**一处**规则：

```go
// LLMModelHeld returns the row this install already holds for one endpoint, as a
// save recognises it. A provider this machine signs in to carries no API path of
// its own — the login decides the endpoint, and a stored path is only what the
// models page displays — so such a row is recognised by its provider, model and
// base URL alone. Everything else is recognised by the identity a save
// normalizes: provider, model, base URL and path.
func LLMModelHeld(ctx context.Context, db *sql.DB, provider, model, baseURL, apiPath string) (state.LLMModel, bool, error) {
	if db == nil {
		return state.LLMModel{}, false, fmt.Errorf("no state database")
	}
	provider, model, baseURL, apiPath = appcfg.NormalizeLLMModelIdentity(provider, model, baseURL, apiPath)
	store := &state.LLMModelStore{DB: db}
	if apiPath != "" || !appcfg.CredentialsBackedProvider(provider) {
		return store.FindByIdentity(ctx, provider, model, baseURL, apiPath)
	}
	rows, err := store.List(ctx)
	if err != nil {
		return state.LLMModel{}, false, err
	}
	for _, row := range rows {
		// Oldest first: the row this endpoint was first configured as is the
		// one a save updates, the same "first wins" rule ConfiguredLLMs uses.
		if row.Provider == provider && row.Model == model && row.BaseURL == baseURL {
			return row, true, nil
		}
	}
	return state.LLMModel{}, false, nil
}
```

`UpsertLLMModel`（同文件 `:683`）把 `store.FindByIdentity(...)` 换成 `LLMModelHeld(...)`，并在更新分支
保留行上已有的展示 path（空 path 表示"登录决定"，不是"清掉展示"）：

```go
existing, found, err := LLMModelHeld(ctx, db, in.Provider, in.Model, in.BaseURL, in.APIPath)
...
if found {
	row := existing
	row.Provider, row.Model, row.BaseURL = provider, model, baseURL
	if apiPath != "" {
		row.APIPath = apiPath
	}
```

（非登录型 provider 的 `apiPath` 归一化后不会是空串——`NormalizeLLMModelIdentity` 会补默认协议，所以这条
条件对它们等价于原来的无条件赋值。）

### B. 模型页回显：`pkg/tui/setup.go`

`pickOffers`（`:1361-1364`）改为：

```go
checked := s.chosen
if checked == nil {
	checked = s.heldOffers()
	if len(checked) == 0 {
		checked = defaultOffers(s.offers)
	}
}
```

新增方法（放在 `defaultOffers` 旁）：

```go
// heldOffers is the check list's opening state when this install already holds
// rows for the listed models: what /model already runs is ticked, so /connect
// opens on the truth instead of on the vendor listing's own first row. Nothing
// held — a first sign-in — answers nil and leaves the page on defaultOffers. A
// database it cannot read answers nil for the same reason: the write that
// follows is where a broken install reports itself.
func (s *llmSetup) heldOffers() []int {
	db, err := s.stateDB()
	if err != nil {
		return nil
	}
	held := make([]int, 0, len(s.offers))
	for i, offer := range s.offers {
		_, found, err := process.LLMModelHeld(s.ctx(), db, offer.Provider, offer.Model, offer.BaseURL, offer.APIPath)
		if err != nil {
			return nil
		}
		if found {
			held = append(held, i)
		}
	}
	return held
}
```

### C. Volcengine 不再索要行上已有的 key：`pkg/subscription/volcengine.go`

`Provision` 的写入循环（`:196-215`）改为按行决定 key：行上已有 key 就传 `nil`（`UpsertLLMModel` 保留它），
只有"会变成无 key 的行"才返回 `ErrPlanKeyRequired`。新增 helper：

```go
// volcengineRowKeyHeld reports whether the row this offer would write already
// holds a key. The plan asks for a pasted key only for a row that would end up
// keyless — the same sentence UpsertLLMModel answers with, and the same rule the
// save follows (owner 2026-11-13): a key the row already has is never asked for
// again.
func volcengineRowKeyHeld(ctx context.Context, env Env, offer Offer) (bool, error) {
	if env.DB == nil {
		return false, nil
	}
	row, found, err := process.LLMModelHeld(ctx, env.DB, offer.Provider, offer.Model, offer.BaseURL, offer.APIPath)
	if err != nil || !found {
		return false, err
	}
	return strings.TrimSpace(row.APIKey) != "", nil
}
```

循环内：

```go
var key *string
switch {
case rowKey != "":
	key = &rowKey
case offer.KeyRequired:
	held, err := volcengineRowKeyHeld(ctx, env, offer)
	if err != nil {
		return nil, err
	}
	if !held {
		return nil, ErrPlanKeyRequired{Plan: offer.Plan, PlanLabel: offer.PlanLabel, ConsoleURL: volcengineKeyConsoleURL}
	}
}
id, err := process.UpsertLLMModel(ctx, env.DB, offerRowInput(offer, volcengineID, key))
```

（`offerRowInput(offer, source, key *string)` 签名不变；`key == nil` 时 `UpsertLLMModel` 保留行上原 key。
团队席位路径不变：`seatKey` 非空，照旧写入。）

### D. gateway 下发"本机已存在"：`pkg/gateway/subscriptions_api.go`

`subscriptionOffer`（`:59-83`）增一个字段，与 `default` 并列：

```go
	// Held marks a model this install already holds as a row, so the page opens
	// on the truth instead of on the vendor's own first row. The terminal's
	// models page asks the same question through the same rule
	// (process.LLMModelHeld).
	Held bool `json:"held"`
```

`handleSubscriptionOffers`（`:154-194`）在 `env` 已经取到之后逐条算：

```go
	env := s.subscriptionEnv()
	offers, err := sub.Offers(r.Context(), env)
	...
	for _, offer := range offers {
		held := false
		if env.DB != nil {
			if _, found, herr := process.LLMModelHeld(r.Context(), env.DB, offer.Provider, offer.Model, offer.BaseURL, offer.APIPath); herr == nil {
				held = found
			}
		}
		out = append(out, subscriptionOffer{ /* …既有字段… */ Held: held })
	}
```

读不到库时 `held=false`（页面回到"厂商默认"），与 TUI 同一处理：写入路径才是报告失败的地方。

### E. web 预选：`frontend/src/lib/api.ts` + `SubscriptionLoginModal.vue`

- `SubscriptionOffer`（`api.ts:1543`）增 `held: boolean`（必填，与 `default` 同风格；同时给两个测试工厂补
  `held: false`：`SubscriptionLoginModal.test.ts:34`、`OfferPicker.test.ts:19`）。
- `loadOffers()`（`SubscriptionLoginModal.vue:519-527`）改为已添加优先：

```ts
    offers.value = await forebrainApi.subscriptionOffers(subscription.id)
    // What this install already holds is ticked, the way the terminal's
    // /connect opens on it; a first sign-in has nothing held and preselects the
    // vendor's own default.
    const held = offers.value.map((offer, index) => (offer.held ? index : -1)).filter((index) => index >= 0)
    const preset = offers.value.findIndex((offer) => offer.default)
    chosen.value = held.length > 0 ? held : preset >= 0 ? [preset] : []
```

`OfferPicker` 本身不改（它只渲染 `chosen`）。文案不改。

### 行为变化矩阵

| 场景 | 旧行为 | 新行为 |
|---|---|---|
| 首次登录（库里没有任何行）→ 模型页 | 勾厂商第一行 | **不变**（`heldOffers` 返回空 → `defaultOffers`） |
| Volcengine，已有 coding+agent 若干行 → 模型页 | 只勾合并列表第 0 行 | 已添加的 coding/agent 行按 `(provider, model, base, /v1/messages)` 全部勾上 |
| OpenCode，已有行（`api_path` 为空或迁移写的 `/chat/completions`、`/v1/messages`）→ 模型页 | 只勾目录第 0 个 | 已添加的行全部勾上（登录型 provider 不看 path） |
| ChatGPT / 本地订阅插件 → 模型页 | 只勾第一条 default | 同上，走同一实现 |
| 只添加了部分模型 → 模型页 | 全都不勾（除第一条） | 只勾已添加的那些 |
| 勾住已添加行直接回车（OpenCode，行上的 path 与 offer 不同） | 新插一行（重复行） | **刻意修订**：更新原行，`id` 不变、不再产生重复；行上的展示 path 保留 |
| 勾住已添加行直接回车（Volcengine 个人 plan，行上已有 key） | 弹出 "Paste the … API key" | **刻意修订**：不弹，保留原 key 直接更新（owner 2026-11-13 裁决：不问行上已有的 key） |
| Volcengine 个人 plan，模型是新的（行上无 key） | 弹 key 页 | **不变** |
| Volcengine 团队席位 | 取 seat key 写入 | **不变** |
| 手填 key 的 provider（openai/anthropic/…） | 身份 = 四元组 | **不变**（`apiPath` 非空走 `FindByIdentity`） |
| 同一个模型换 base_url（镜像） | 两行 | **不变**（base 仍是身份的一部分） |
| 向导内 Esc 回到模型页（`s.chosen`） | 保留本次勾选 | **不变** |
| web 订阅登录弹窗（已添加若干模型） | 只勾第一条 default | **刻意修订**（owner 2026-11-13：两 surface 一起修）：勾全部 `held` 的 offer |
| web 订阅登录弹窗（首次登录，库里没有行） | 勾第一条 default | **不变**（`held` 全 false → `findIndex(default)`） |
| 文案/标题（TUI + web） | — | **不变**（`Choose the models to add; …`、`subscribeStep3` 逐字保留） |

## Scope

**In scope**（只改这 13 个文件）:

- `pkg/process/llm_models.go` — 新增 `LLMModelHeld`；`UpsertLLMModel` 用它，并在更新分支保留登录型行的展示 path。
- `pkg/process/llm_models_test.go` — 2 条回归测试（见 Tests）。
- `pkg/tui/setup.go` — `pickOffers` 初值改走 `heldOffers`；新增 `heldOffers`。
- `pkg/tui/setup_test.go` — 2 条回归测试（Volcengine 两 plan 回显、OpenCode 迁移 path 回显）。
- `pkg/subscription/volcengine.go` — `Provision` 按行决定 key + `volcengineRowKeyHeld`（**等 81 定稿**）。
- `pkg/subscription/volcengine_test.go` — 2 条回归测试（保留已有 key；无 key 仍拒绝）。
- `pkg/gateway/subscriptions_api.go` — `subscriptionOffer.Held` + `handleSubscriptionOffers` 计算它（**等 81 定稿**）。
- `pkg/gateway/subscriptions_api_test.go` — 1 条往返测试：provision 前 `held=false`，provision 后 `held=true`。
- `frontend/src/lib/api.ts` — `SubscriptionOffer.held`。
- `frontend/src/components/models/SubscriptionLoginModal.vue` — `loadOffers` 预选 `held`。
- `frontend/src/components/models/SubscriptionLoginModal.test.ts` — 1 条用例（held 全部勾上；无 held 仍勾 default）+ offer 工厂补 `held: false`。
- `frontend/src/components/models/OfferPicker.test.ts` — 仅工厂补 `held: false`（类型必填的连带）。
- `frontend/e2e/opencode-login.spec.ts` — offers 桩加 `held`，断言弹窗打开时已勾的模型就是 `held` 的那些。

**Out of scope**（看起来相关但明确不碰）:

- `pkg/subscription/registry.go` 的 `offerRowInput`、`pkg/subscription/volcengine.go` 的
  `Offers`/`volcengineModelOffers`/`volcengineModelImages`、`Offer.SupportsImages` 的类型 —— 全归
  forebrain-harness-81，本计划定稿前不碰（Steps 第 3、5 步有同步点）。
- `frontend/src/components/models/OfferPicker.vue`、`ModelFormDrawer.vue`、`ModelsTab.vue`、locales 文案 —— 不改渲染与文案。
- `pkg/tui/chat_session_test.go` 的 `fakeSelector` —— 不改共享 fake（`multiDefaults` 已够用）。
- `pkg/state/*` —— 不加新查询、不改表/索引/迁移；不删既有重复行。
- `pkg/run/*`、`pkg/turn/models.go` 的 `/model` 排序 —— 其它会话在改，不碰。
- `Default` 字段语义、subagent 授权页的预选 —— 不改。

## Baseline 与 Drift check（先跑）

Baseline: `git rev-parse --short HEAD` = `c32d23d5`。工作树应干净（除本计划文件）：

```bash
git rev-parse --short HEAD          # 期望 c32d23d5（若不是，先确认是否只是别人的新提交）
git status --porcelain              # 期望空（docs/plan 与 plans 已 gitignore）
```

`git status` 非空时的判读规则：主树有并行会话（forebrain-harness-81 / -51）在改别的文件，脏文件属于他们
时**不算 drift** —— 先 `peer_send` 认领（不 revert、不 stash、不 commit 别人的改动），只要求 Scope 里这 13 个
文件本身干净（`git status --porcelain -- <这 13 个路径>` 为空）。只有这 13 个文件被别的会话改过时才是 STOP。

Drift 检查（内容不符即 STOP，行号漂移不算）：

```bash
sed -n '1356,1370p' pkg/tui/setup.go            # 期望看到 pickOffers 的 checked := s.chosen
sed -n '1478,1492p' pkg/tui/setup.go            # 期望 defaultOffers 只看 offer.Default
grep -c "existing, found, err := store.FindByIdentity" pkg/process/llm_models.go   # 期望 1（UpsertLLMModel 内）
grep -c 'rowKey == ""' pkg/subscription/volcengine.go                              # 期望 1（大小写敏感，Plan 里不要写 RowKey）
grep -c "func volcengineRowKeyHeld" pkg/subscription/volcengine.go                 # 期望 0（尚未实现）
```

**真库行数以副本库现值为准**：owner 在日常使用中会继续添加模型，现场证据里的行数（13 条 volcengine、
16 条 opencode-go）只是取证当时的快照；执行时以 `sqlite3 -readonly` 查到的当前值为准，形状（`source`、
`source_plan`、`base_url`、`api_path` 的取值集合）才是判据。

## Steps

每一步结束都跑它的验证命令，看到期望结果再前进。**Step 0 是复现**：它必须先失败，否则停下来报告。

### Step 0：先把复现测试落成永久用例（此时红）

在 `pkg/tui/setup_test.go` 末尾新增两条（复用既有 `fakeSubscription`/`fakeSelector`/`runSubscriptionSetup`/
`setupTestConfig`；`offerRowSeed` 这个 helper 也放这里）：

```go
// offerRowSeed writes the row an earlier /connect run would have left for one of
// the subscription's offers.
func offerRowSeed(t *testing.T, cfg PromptConfig, sub *fakeSubscription, offer subscription.Offer, key string) string {
	t.Helper()
	db, err := process.StateDB(process.Context{Home: cfg.Home, ConfigPath: cfg.ConfigPath})
	if err != nil {
		t.Fatalf("state db: %v", err)
	}
	defer db.Close()
	id, err := process.UpsertLLMModel(context.Background(), db, process.LLMModelInput{
		Provider: offer.Provider, Model: offer.Model, BaseURL: offer.BaseURL, APIPath: offer.APIPath,
		APIKey: &key, Source: sub.id, SourcePlan: offer.Plan,
	})
	if err != nil {
		t.Fatalf("seed row: %v", err)
	}
	return id
}

// The models page opens on what /model already runs: the rows a previous sign-in
// wrote are ticked, across both of Ark's plans. (The fake plan still refuses its
// first write and takes the pasted key, so the flow completes; whether a held row
// asks for a key at all is the subscription's own answer, pinned in
// pkg/subscription's tests.)
func TestOffersPageOpensOnTheModelsAlreadyAdded(t *testing.T) {
	sub := volcengineLike()
	sub.check = turn.SignInCheck{State: turn.SignInUsable}
	sub.offers = append(sub.offers, subscription.Offer{
		Subscription: "volcengine", Provider: "volcengine", Model: "glm-5.3",
		BaseURL: "https://ark.cn-beijing.volces.com/api/plan", APIPath: "/v1/messages",
		Plan: "agent-plan", PlanLabel: "Agent Plan",
	})
	selector := &fakeSelector{inputs: []string{"placeholder-key"}, multiSelects: [][]int{{1}}}
	cfg := setupTestConfig(t, selector)
	offerRowSeed(t, cfg, sub, sub.offers[1], "placeholder-plan-key")

	opts, ok, err := runSubscriptionSetup(t, cfg, sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if opts.Model != "glm-5.3" {
		t.Fatalf("options = %+v, want the model that was already added", opts)
	}
	if got := selector.multiDefaults[0]; !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("checked rows = %v, want the already added row [1]", got)
	}
}

// A login-backed row carries the endpoint its model speaks when a legacy
// configuration wrote it, and none when /connect writes it: the login decides
// the endpoint either way, so both are the same model in /model and both are
// ticked. The offer below is the production shape — a login listing names no
// API path (`opencode.go` goes through `offersFromRecords`) — while the row a
// pre-table configuration left behind carries the path it displays.
func TestOffersPageOpensOnALoginRowWhateverPathItShows(t *testing.T) {
	sub := openCodeLike()
	sub.check = turn.SignInCheck{State: turn.SignInUsable}
	// openCodeLike's offers carry a path for the rows they write; a real login
	// listing carries none.
	sub.offers[1].APIPath = ""
	selector := &fakeSelector{multiSelects: [][]int{{1}}}
	cfg := setupTestConfig(t, selector)
	legacy := sub.offers[1]
	legacy.APIPath = "/chat/completions"
	offerRowSeed(t, cfg, sub, legacy, "")

	opts, ok, err := runSubscriptionSetup(t, cfg, sub)
	if err != nil || !ok {
		t.Fatalf("configure err=%v ok=%t", err, ok)
	}
	if opts.Model != "kimi-k3" {
		t.Fatalf("options = %+v, want the model that was already added", opts)
	}
	if got := selector.multiDefaults[0]; !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("checked rows = %v, want the already added row [1]", got)
	}
}
```

验证（**必须红**，输出与"根因/单测复现"一致）：

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestOffersPageOpensOn' -count=1 -timeout 180s
# 期望：两条都 FAIL，checked rows = [0]
```

### Step 1：`pkg/process` 的共享判定 + 写入身份

按"修复设计 A"改 `pkg/process/llm_models.go`（新增 `LLMModelHeld`、`UpsertLLMModel` 改用它并保留展示 path），
并把 `UpsertLLMModel` 现有的 doc（`:657-666`，"an existing row with the same provider, model, base URL and
API path is updated in place … The api path is completed to the protocol's default before the lookup"）
补一句：登录型 provider 的 path 不参与身份（端点由登录决定），空 path 不清掉行上已存的展示 path。
同时新增两条测试到 `pkg/process/llm_models_test.go`：

```go
// A login-backed row is one row whatever path it displays: writing it again
// updates it in place and keeps the path the models page already shows.
func TestUpsertLLMModelFindsALoginRowWhateverItsPath(t *testing.T) {
	store, ctx := upsertEnvForTest(t)
	empty := ""
	legacy, err := UpsertLLMModel(ctx, store.DB, LLMModelInput{
		Provider: "opencode-go", Model: "glm-5.3", BaseURL: "https://opencode.ai/inference/go",
		APIPath: "/chat/completions", APIKey: &empty,
	})
	if err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	again, err := UpsertLLMModel(ctx, store.DB, LLMModelInput{
		Provider: "opencode-go", Model: "glm-5.3", BaseURL: "https://opencode.ai/inference/go", APIKey: &empty,
	})
	if err != nil {
		t.Fatalf("write again: %v", err)
	}
	if again != legacy {
		t.Fatalf("id changed %q -> %q: the login decides the endpoint, so the path is not part of the row", legacy, again)
	}
	rows, err := store.List(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v err=%v, want one", rows, err)
	}
	if rows[0].APIPath != "/chat/completions" {
		t.Fatalf("api_path = %q, want the stored display path kept", rows[0].APIPath)
	}
}

// The path is identity for a provider whose endpoints are told apart by it.
func TestLLMModelHeldKeepsThePathForAKeyProvider(t *testing.T) {
	store, ctx := upsertEnvForTest(t)
	key := "ark-placeholder-key"
	row, err := UpsertLLMModel(ctx, store.DB, LLMModelInput{
		Provider: "volcengine", Model: "glm-5.3", BaseURL: "https://ark.cn-beijing.volces.com/api/coding",
		APIPath: "/v1/messages", APIKey: &key,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, found, err := LLMModelHeld(ctx, store.DB, "volcengine", "glm-5.3", "https://ark.cn-beijing.volces.com/api/coding", "")
	if err != nil {
		t.Fatalf("held: %v", err)
	}
	if found {
		t.Fatal("a plan's OpenAI-compatible path is a different endpoint, not the same row")
	}
	got, found, err := LLMModelHeld(ctx, store.DB, "volcengine", "glm-5.3", "https://ark.cn-beijing.volces.com/api/coding", "/v1/messages")
	if err != nil || !found || got.ID != row {
		t.Fatalf("held = %+v found=%t err=%v, want the seeded row", got, found, err)
	}
}
```

验证：

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run 'TestUpsertLLMModel|TestLLMModelHeld' -count=1
# 期望：ok（含既有 TestUpsertLLMModelAddsARowForAnotherEndpoint、TestUpsertLLMModelStoresALoginRowWithoutAKey 全绿）
CGO_ENABLED=1 go test -tags fts5 ./pkg/process -count=1
# 期望：ok
```

### Step 2：模型页回显（TUI）

按"修复设计 B"改 `pkg/tui/setup.go`。验证 Step 0 的两条用例转绿，且既有订阅用例不回归：

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestOffersPageOpensOn|TestConfigureSubscription|TestOffersPageLabels|TestEscOnOffersPage|TestSubscriptionFlow' -count=1
# 期望：ok（Step 0 的两条 PASS；TestConfigureSubscriptionAddsTheModelsTheUserTicks 的
#      checked rows = [0] 断言不动也仍绿——它库里没有任何行）
```

### Step 3：`pkg/subscription` 不再索要已有的 key —— **等 81 定稿后**

先 `peer_send` 给 forebrain-harness-81 确认 `pkg/subscription/volcengine.go` 已定稿（它正在改 cap_images
链路）。拿到确认后按"修复设计 C"只改 `Provision` 与该 helper，并新增两条测试：
```go
// A row that already holds its plan's key is updated without asking for it
// again; only a row that would end up keyless requires the paste.
func TestVolcengineProvisionKeepsTheKeyARowAlreadyHas(t *testing.T) {
	newVolcengineStub(t)
	env := testEnv(t)
	offer := Offer{
		Subscription: volcengineID, Plan: volcengineCodingPlan, PlanLabel: "Coding Plan", Provider: volcengineID,
		Model: "doubao-seed-2.1-pro", BaseURL: volcengineCodingBaseURL, APIPath: "/v1/messages", KeyRequired: true,
	}
	ids, err := (volcengine{}).Provision(context.Background(), env, []Offer{offer}, ProvisionOptions{Key: "first-key-placeholder"})
	require.NoError(t, err)

	// The same offer again, with no key pasted: the row keeps the key it has.
	again, err := (volcengine{}).Provision(context.Background(), env, []Offer{offer}, ProvisionOptions{})
	require.NoError(t, err)
	require.Equal(t, ids, again)
	var key string
	require.NoError(t, env.DB.QueryRowContext(context.Background(),
		`SELECT api_key FROM fb_llm_models WHERE id = ?`, ids[0]).Scan(&key))
	require.Equal(t, "first-key-placeholder", key)
}

// A row this install does not hold yet still needs its plan's key, and the
// refusal names the plan.
func TestVolcengineProvisionStillAsksForAFreshRow(t *testing.T) {
	newVolcengineStub(t)
	env := testEnv(t)
	_, err := (volcengine{}).Provision(context.Background(), env, []Offer{{
		Subscription: volcengineID, Plan: volcengineAgentPlan, PlanLabel: "Agent Plan", Provider: volcengineID,
		Model: "glm-5.3", BaseURL: volcengineAgentBaseURL, APIPath: "/v1/messages", KeyRequired: true,
	}}, ProvisionOptions{})
	var need ErrPlanKeyRequired
	require.ErrorAs(t, err, &need)
	require.Equal(t, volcengineAgentPlan, need.Plan)
}
```

验证：

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/subscription -count=1
# 期望：ok（既有 TestVolcengineProvisionNeedsThePastedKey 仍绿：那条的库里没有行）
```

### Step 4：gateway 下发 `held` —— **等 81 定稿后**

先 `peer_send` 给 forebrain-harness-81 确认 `pkg/gateway/subscriptions_api.go` 已定稿（它要用同一个
`subscriptionOffer` 结构改 `SupportsImages`）。拿到确认后按"修复设计 D"加字段与计算，并在
`pkg/gateway/subscriptions_api_test.go` 的 `TestSubscriptionsAPIProvision` 里加一段往返断言：

```go
	// Before the write nothing is held; after it the same listing says the row
	// is this install's own, which is what the page preselects.
	rec = do(http.MethodGet, "/api/subscriptions/chatgpt/offers")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var before []struct {
		Held bool `json:"held"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &before))
	require.Len(t, before, 1)
	require.False(t, before[0].Held)
	// …（provision 那一步已经在上面的既有断言里写过）…
	rec = do(http.MethodGet, "/api/subscriptions/chatgpt/offers")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var after []struct {
		Held bool `json:"held"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &after))
	require.True(t, after[0].Held, "a model this install holds must be marked for the page")
```

验证：

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'TestSubscriptionsAPI' -count=1
# 期望：ok
```

### Step 5：web 预选（api 类型 + 弹窗 + 单测 + 一条 e2e）

按"修复设计 E"改 `frontend/src/lib/api.ts`、`SubscriptionLoginModal.vue`，给两个测试工厂补 `held: false`，
在 `SubscriptionLoginModal.test.ts` 加一条用例：

```ts
  it('preselects the models this install already holds, not the vendor default', async () => {
    const rows = [
      offer({ model: 'glm-5.3', default: true, held: false }),
      offer({ model: 'kimi-k3', default: false, held: true }),
      offer({ model: 'qwen3.8-max', default: false, held: true }),
    ]
    // …按该文件既有写法 stub 订阅列表 / 登录 / offers，打开弹窗走到第 3 步…
    const boxes = wrapper.findAll('input[type="checkbox"][data-offer]')
    expect(boxes.map((box) => (box.element as HTMLInputElement).checked)).toEqual([false, true, true])
  })
```

并把 `frontend/e2e/opencode-login.spec.ts` 的 offers 桩里给其中一条加 `held: true`，断言弹窗打开时该行已勾、
其余未勾（该 spec 已驱动到第 3 步，改断言即可）。

验证：

```bash
cd frontend && pnpm typecheck && pnpm test -- SubscriptionLoginModal && cd ..
# 期望：typecheck 无错；该 spec 全绿
FOREBRAIN_E2E_SPEC=e2e/opencode-login.spec.ts scripts/acceptance/web_e2e.sh
# 期望：1 spec 通过（只跑任务相关 spec）
```

### Step 6：包级回归 + 静态检查

```bash
gofmt -l cmd pkg third_party        # 期望：无输出
CGO_ENABLED=1 go vet -tags fts5 ./...   # 期望：无输出
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/process ./pkg/subscription ./pkg/state ./pkg/config ./pkg/gateway -count=1
# 期望：ok。若红在本次未改的文件（pkg/run/*、pkg/turn/*、pkg/gateway 的排序相关），按并行会话规则
#       先问对应会话，不要自己改。
```

### Step 7：tmux 真机验收（必做，测试绿不等于真机可用）

构建必须带 `-tags fts5`（见 skill 的坑）。验收**不碰真实 `~/.forebrain`**：把真实安装的 state 库
和登录凭据**复制**到临时 home，只在副本上读；对真实的 home 只读。

```bash
go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain
SCRATCH=$(mktemp -d)/fb-echo && mkdir -p "$SCRATCH/home/state" "$SCRATCH/proj"
cp ~/.forebrain/state/forebrain.state.sqlite* "$SCRATCH/home/state/"   # -wal/-shm 一起
cp ~/.forebrain/forebrain.yaml ~/.forebrain/opencode.json "$SCRATCH/home/" 2>/dev/null
tmux new-session -d -s fbecho -x 120 -y 42 \
  "cd $SCRATCH/proj && FOREBRAIN_HOME=$SCRATCH/home ./build/bin/forebrain; echo EXIT_CODE=\$?; sleep 300"
```

（构造：`$SCRATCH/proj` 里先 `git init` 过一次并接受 workspace trust，或用 `.claude/skills/run-forebrain/driver.sh`
的假 provider 配方；TUI 起来后 `/connect` 逐屏 capture。）

- **场景 1（回显，复现→修复）**：`/connect` → `Volcengine Ark`（订阅行）→ 模型页逐屏
  `tmux capture-pane -t fbecho -p` 取证：`Coding Plan` / `Agent Plan` 两组的行里，**副本库里已存在的
  `(provider, model, base, path)` 必须显示为已勾选**。判据：勾选数 = 副本库里 `source='volcengine'` 的行中
  **同时出现在本次 listing 里的**那部分（用
  `sqlite3 -readonly "file:$SCRATCH/home/state/forebrain.state.sqlite?mode=ro" "select model, source_plan, base_url, api_path from fb_llm_models where source='volcengine'"` 与屏幕逐行对照）；
  库里有、但厂商当前 listing 不再提供的模型不会出现在页面上，属正常。
  再 `Esc` 回订阅列表，选 `OpenCode` → 模型页同样取证（16 条 `opencode-go` 行里，出现在 listing 的都应勾上，
  包括迁移写的 `/chat/completions`、`/v1/messages` 形态）。
  若这一步走了静态回退（页面标注 console 是权威、`FromStaticList`），说明厂商 listing 取不到：记录现场并重试一次，
  仍不行则按静态表的行对照取证，并在报告里写明限制。
- **场景 2（首次登录不回归）**：副本库里
  `sqlite3 "file:$SCRATCH/home/state/forebrain.state.sqlite" "delete from fb_llm_models where source in ('volcengine','opencode-go')"`
  后重开 `/connect` → 模型页只勾厂商默认那一条（与修复前一致）。
- **场景 3（中断自救）**：模型页 `Esc` 回到订阅列表（不死循环、不再要求登录）、`Ctrl+C` 退出，
  `capture-pane` 看到 `EXIT_CODE=0`；再启动一次仍可走同一路径。
- 若副本里的凭据已失效（模型页提示需要重新登录）→ 记录现场、**不要替 owner 登录**，改用单测证据 + 场景 2/3
  并在报告里写明；不要为了验收去跑真模型的推理请求（控制面调用 `GetPersonalPlan`/`List*PlanModel` 不算
  烧配额）。
- **web surface 的验收**：本 bug 的 web 半边由 Step 5 的 e2e spec（stub 驱动的真实浏览器）覆盖；真实登录态
  下的 web 弹窗不手工跑（要真登录 + 真写行），在报告里写明这一点。

收尾清理：`tmux kill-session -t fbecho`、`rm -rf` 临时目录。

### Step 8：回填计划与报告

- 把本计划抄到 `docs/plan/CONNECT_OFFERS_ECHO_PLAN.md`，Status 改"已实施并真机验收通过（日期）"，
  加"验收记录"（场景→证据，含工具链坑）。
- `git status --porcelain` 必须只列 Scope 内的文件；`git diff --stat` 贴进报告。
- 不留 tmux session / 临时目录；不 commit。

## Tests / 验收清单

| 层 | 用例 | 位置 |
|---|---|---|
| 复现（红→绿） | `TestOffersPageOpensOnTheModelsAlreadyAdded` | `pkg/tui/setup_test.go` |
| 复现（红→绿） | `TestOffersPageOpensOnALoginRowWhateverPathItShows` | `pkg/tui/setup_test.go` |
| 写入身份 | `TestUpsertLLMModelFindsALoginRowWhateverItsPath` | `pkg/process/llm_models_test.go` |
| 身份规则反向 | `TestLLMModelHeldKeepsThePathForAKeyProvider` | `pkg/process/llm_models_test.go` |
| key 不重复索要 | `TestVolcengineProvisionKeepsTheKeyARowAlreadyHas` | `pkg/subscription/volcengine_test.go` |
| key 仍要求（新行） | `TestVolcengineProvisionStillAsksForAFreshRow` | `pkg/subscription/volcengine_test.go` |
| gateway `held` 往返 | `TestSubscriptionsAPIProvision` 追加段 | `pkg/gateway/subscriptions_api_test.go` |
| web 预选 | `preselects the models this install already holds, not the vendor default` | `frontend/src/components/models/SubscriptionLoginModal.test.ts` |
| web 浏览器级 | `e2e/opencode-login.spec.ts`（stub 的 offers 带 `held`） | `frontend/e2e/opencode-login.spec.ts` |
| 回归（不动断言） | `TestConfigureSubscriptionAddsTheModelsTheUserTicks`（首次登录仍勾 default）、`TestUpsertLLMModelAddsARowForAnotherEndpoint`、`TestVolcengineProvisionNeedsThePastedKey`、`TestUpsertLLMModelStoresALoginRowWithoutAKey`、`OfferPicker.test.ts` | 原文件 |

## Done criteria（机器可查）

1. Step 0 的两条用例在修复前 FAIL（`checked rows = [0]`）、修复后 PASS。
2. `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/process ./pkg/subscription ./pkg/state ./pkg/config ./pkg/gateway -count=1` 全绿。
3. `gofmt -l cmd pkg third_party` 无输出；`CGO_ENABLED=1 go vet -tags fts5 ./...` 无输出。
4. `cd frontend && pnpm typecheck && pnpm test` 全绿（至少 `SubscriptionLoginModal`、`OfferPicker` 两个 spec）；
   `FOREBRAIN_E2E_SPEC=e2e/opencode-login.spec.ts scripts/acceptance/web_e2e.sh` 通过。
5. tmux 三个场景的 capture 证据在报告里（场景 1 的勾选 = 副本库里出现在 listing 里的那些行）。
6. `git status --porcelain` 只含 Scope 内的 13 个文件；无 commit、无 push、无 worktree。
7. 真实 `~/.forebrain` 未被写入（验收前记录副本来源的 `mtime`，验收后对比）。

## STOP conditions

- **Step 0 不红**（或红的原因与本计划写的不一致）→ 停，重新定位；不要按计划继续改。
- **Step 1 之后 `pkg/process` 既有用例变红且原因指向"登录型行身份"之外** → 停，先判断是不是把
  不该收窄的东西收窄了（例如非登录型 provider 的 `apiPath` 归一化路径）。
- **`pkg/subscription/volcengine.go` / `pkg/gateway/subscriptions_api.go` 与 forebrain-harness-81 的改动冲突**
  （它把 `Offer.SupportsImages`、`subscriptionOffer.SupportsImages` 改成 `*bool`，改 `offerRowInput` 签名）
  → 停，先同步；不要手工合并两边的整文件重写。
- **`pkg/tui`/`pkg/gateway` 红在本次未改的文件**（`/model` 排序、`pkg/run/*`、`pkg/turn/models.go`）→ 找对应会话，
  不要顺手修。
- **`pnpm typecheck`/`pnpm test`/`web_e2e.sh` 红在本次未改的 spec** → 先判断是不是同一改动引起（例如
  `SubscriptionOffer` 必填字段导致别的工厂编译失败——那是本改动的连带，应补；其余按并行会话规则处理）。
- **验收时真实凭据已失效或调用失败** → 报告并落到场景 2/3 + 单测证据，不替 owner 登录、不烧推理配额。
- **发现"回显后回车"会删行/改行 ID/覆盖别的 plan 的 key** → 停，这是本计划没覆盖的破坏性路径。

## Maintenance notes

- **一个概念一处实现**：任何"本机是否已经有这行"的判断都走 `process.LLMModelHeld`；不要再在 surface 里
  拼四元组比较。subagent 授权页的 `storedOfferAllowed`（`pkg/tui/setup.go:1788`）用的是
  `FindByIdentity` 四元组，对登录型行会漏（登录行 `api_path` 常为空、迁移行带 path）——本期不动它，
  但下次改它时应改用 `LLMModelHeld`。
- **为什么空 path 不清展示**：登录型行的协议由登录+模型决定，models 页把这类行显示为 sign-in；
  行上的 path 只是给迁移/展示留的提示。若将来 web 表单要支持"清空 path"，需要一条显式语义
  （例如单独的 flag），不能靠传空串。
- **相邻未修**：①一次勾选含两个个人 plan 时共用一个 `opts.Key`；②已存在的重复行（`deepseek-v4.1-flash`）
  未清理。两者都需要单独决定。
- **web 侧后续**：`OfferPicker` 目前不区分"已添加"，只是被预选；若 owner 想让行上直接标出"已添加"，
  那是 `OfferPicker.vue` + locales 的独立改动（本计划刻意不改文案/渲染）。
- **评审要盯的点**：`UpsertLLMModel` 收窄身份后，gateway 模型页"编辑登录型行"的路径是否受影响
  （`pkg/gateway/models_api.go:915` 只允许编辑已存在的登录型行）；`store.List` 每次判定的开销
  （行数 ~几十，一次页面打开 ≤ 40 次 List，gateway 每次请求 ≤ offers 数量次）是否可接受。

## 验收记录（2026-11-14）

- 基线 `c32d23d5`，主树实施（无 commit、无 worktree）。并行会话 forebrain-harness-81 同期改
  `pkg/subscription` 的 cap_images 链路；已与它书面确认区域划分（它不碰 `pkg/gateway/subscriptions_api.go`，
  `volcengine.go` 只碰 `Offers` 侧三处且不改 `Offer.SupportsImages` 类型），两边改动互不重叠。
- Step 0 复现：新增两条用例先红后绿（`checked rows = [0]` → `[1]`）。
- Step 1-4 单测：`pkg/process`（含 `TestUpsertLLMModelFindsALoginRowWhateverItsPath`、
  `TestLLMModelHeldKeepsThePathForAKeyProvider`）、`pkg/subscription`（含
  `TestVolcengineProvisionKeepsTheKeyARowAlreadyHas`、`TestVolcengineProvisionStillAsksForAFreshRow`）、
  `pkg/gateway`（`held` 往返：写入前 false、写入后 true）全绿。
- Step 5 web：`pnpm typecheck` 无错；`SubscriptionLoginModal.test.ts` + `OfferPicker.test.ts` 14/14；
  `FOREBRAIN_E2E_SPEC=e2e/opencode-login.spec.ts scripts/acceptance/web_e2e.sh` 4/4 PASS
  （offers 桩带 `held: true`，弹窗打开即勾该行、其余不勾）。
- Step 6：`gofmt -l cmd pkg third_party` 无输出；`CGO_ENABLED=1 go vet -tags fts5 ./...` 无输出；
  `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/process ./pkg/subscription ./pkg/state ./pkg/config ./pkg/gateway`
  全绿（tui 85s / process 233s / subscription 2.5s / state 5.0s / config 3.9s / gateway 12s）。
- Step 7 真机（构建带 `-tags fts5`；真实 `~/.forebrain` 全程只读；副本 home 在 `$TMPDIR/<tmp>/fb-echo`，
  验收后删除）：
  - **副本构造的偏差（磁盘不足）**：真实 state 库 5.9GB，而当时磁盘只剩 5.5GB（并行会话正在清盘），
    全量 `.backup` 会写满磁盘。改为**裁剪副本**：用真实库的 DDL（去掉 fts5 影子表与 `sqlite_sequence`）
    建新库，只导入 `fb_llm_models`(67 行) + `fb_config`(15 段凭据) + `fb_env_vars`，`PRAGMA user_version=35`
    与真实库一致；首次尝试连带导入 `fb_sessions` 触发了启动期外键校验
    （`migrated database violates foreign keys: fb_run_waits=1, fb_sessions=132`），故其余表保持空表。
    回显只读 `fb_llm_models`，判据不受影响。
  - **场景 1（Volcengine 回显）**：`/connect` → `Volcengine Ark`（行上 `· Signed in`）→ 模型页。
    Coding Plan 19 行：勾 12 / 不勾 7，勾选集合与副本库 `source_plan='coding-plan'` 的 12 行**逐行相等**
    （`comm -3` 双向为空）；Agent Plan 27 行：勾 13 / 不勾 14，勾选集合与库中 13 行**逐行相等**，
    且"未勾 ∩ 库中"为空。
  - **场景 1b（OpenCode 回显）**：`/connect` → `OpenCode` → 模型页：勾 12 行，与
    "库中 `provider='opencode-go'` ∩ 当前 listing"的 12 行**逐行相等**，"未勾 ∩ 库中"为空。
    其中 `kimi-k3.1-pro` 行上 `api_path='/chat/completions'`、offer 无 path（`offersFromRecords` 的生产形态），
    仍被判为同一行并勾上——登录型行身份不看 path 的现场证据。
    库里有但当前 listing 已不再提供的 3 个模型（`gemini-4-argon-contributor` / `longcat-2.5-preview-free` /
    `minimax-m3.1-flash-free`）用页面过滤器逐个确认查不到，故不显示（计划已注明属正常）。
  - **场景 2（首次登录不回归）**：副本库 `delete from fb_llm_models where source in ('volcengine','opencode-go')`
    后重开 `/connect` → Volcengine 模型页只勾厂商默认 `doubao-seed-2.1-lite` 一行，其余全不勾（与修复前一致）。
  - **场景 3（中断自救）**：模型页 `Esc` → 订阅列表（不重复登录、不死循环）→ `Esc` → 回 composer；
    两次快速 `Ctrl+C` 干净退出，屏幕出现 `EXIT_CODE=0`；重启二进制后 `/connect` 仍正常打开同一路径。
  - **真实 home 未被本会话写入**：所有 TUI 实例都带 `FOREBRAIN_HOME=<副本>`，对真实库只用
    `sqlite3 -readonly` / `file:…?mode=ro`。验收期间真实库 mtime 有变化（09:34→10:00），系并行会话 81 的
    真机验收所致，非本会话；本会话启动的实例已全部结束（`pgrep` 无残留）。
  - **web 半边**由 Step 5 的 e2e（stub 驱动的真实浏览器）覆盖；真实登录态下的 web 弹窗未手工跑
    （需真登录 + 真写行），与计划一致。
- 工具链坑：`pnpm test -- <pattern>` 不会只跑匹配文件（实际跑了全量 189 个 spec 文件）；限定范围要用
  `pnpm exec vitest run <file>`。全量跑出的唯一失败 `src/lib/settingsSchema.test.ts:156`
  （prototype HTML 的 `F('` 计数 34 ≠ 断言 33）是**预存**问题：该 prototype 在 `docs/plan/`（gitignored，
  mtime 11-12 11:20），与本次改动无关。
- 磁盘：owner 授权清临时目录后释放约 29GB（`/tmp/exec307-gocache.*`、`/tmp/review307-gocache`、两个
  Go test 残留目录、`$TMPDIR/fbvolc-repro`（评审 repro 副本，81 确认可删）、7 个过期 TUI run 目录）；
  docker daemon 未运行，无可释放。

## Implementation Status

- Step 0 复现用例（`TestOffersPageOpensOnTheModelsAlreadyAdded` /
  `TestOffersPageOpensOnALoginRowWhateverPathItShows`）：done —— 先红（`checked rows = [0]`），修复后绿。
- Step 1 `pkg/process.LLMModelHeld` + `UpsertLLMModel` 身份收窄：done（含 2 条新用例）。
- Step 2 `pkg/tui` 模型页回显（`heldOffers`）：done。
- Step 3 `pkg/subscription` 不再索要行上已有的 key：done（含 2 条新用例）。
- Step 4 gateway `subscriptionOffer.Held`：done（往返断言）。
- Step 5 web 预选（`api.ts` / `SubscriptionLoginModal.vue` / 2 个单测工厂 / 1 条新用例 / e2e）：done。
- Step 6 静态检查 + 六包回归：done。
- Step 7 tmux 真机三场景：done（副本改为裁剪副本，原因见"验收记录"）。
- Step 8 文档回填：done（本文件）。

计划外改动：无。
偏差（均非 load-bearing，意图/范围/公开行为/完成标准不变）：①以裁剪副本代替全量副本（磁盘只剩 5.5GB）；
②gateway 往返断言复用了既有"写入前"的那次 GET（同一断言，少一次请求）；③`pnpm test -- <pattern>`
实际跑全量，改用 `vitest run <file>`。
