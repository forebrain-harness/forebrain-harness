# Plan 134：web 新会话页的模型 chip 测试改在「没有会话」的状态下挂载

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在 dev 工作区。
> 工作区里若有与本计划无关的未提交改动，**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）**：`grep -n "shows the server-computed most recently used model on a new chat" frontend/src/views/ChatView.test.ts`
> 恰好一条（`:714`）；`grep -n "const res = sid ? await forebrainApi.sessionModel(sid) : await forebrainApi.chatModelDefault()" frontend/src/views/ChatView.vue`
> 恰好一条（`:1415`）。
>
> **Drift check（先跑）**：`git diff --stat 1c8f5d9 -- frontend/src/views/ChatView.test.ts frontend/src/views/ChatView.vue frontend/src/components/chat/ModelChip.vue`
> 有输出就对照下面的摘录逐条比对；锚点不符即 STOP 1。
>
> **目标文件必须干净（否则 STOP 1）**：`git status --porcelain frontend/src/views/ChatView.test.ts frontend/src/views/ChatView.vue`
> 必须无输出。Step 1 按行号整段替换、Step 3 还要临时改 `ChatView.vue`，文件里有别人未提交的改动就会被
> 覆盖，而本计划禁止 `git checkout` 还原。
>
> **基线（Step 1 之前跑一次，否则 STOP 5）**：`cd frontend && pnpm vitest run src/views/ChatView.test.ts`
> （约 4 s）→ 恰好 `1 failed | 44 passed (45)`，失败就是本用例、报 `expected 'No model selected' to contain
> 'deepseek-chat'`。出现别的失败是环境/别人的存量红，**不是你要修的**。

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW（只改一个前端单测；组件与网关一个字节不动）
- **Depends on**: —（与 133 无依赖，可并行）
- **Category**: tests（存量红测；批次记录里对它的归因是错的，见下）
- **Planned at**: dev `1c8f5d9`，2026-10-26
- **已实测（评审方在 `/tmp` 的树副本上跑，未动用户工作区）**：Step 1 给出的用例文本原样落地后
  `vitest run src/views/ChatView.test.ts` → 45 passed；Step 3 的变异按预期红
  （`expected 'No model selected' to contain 'deepseek-chat'`）；把 `finally` 里的
  `sessionIdRef.value = 's1'` 去掉后，紧跟其后的 `draws an image refusal …` 会红——还原这行不是可选项。

## Why this matters

前端的 1060 个单测里长期有 1 个红的，就是这条。它被记成"期望里的模型 `deepseek-chat` 已被禁用"，
**但这不是原因**——实测红的是

```
AssertionError: expected 'No model selected' to contain 'deepseek-chat'
```

即模型 chip 压根没拿到服务器给的值。真正的原因是**挂载状态不对**：mock 的 stream `sessionId` 是
`'s1'`，于是组件认为"这是一个已经打开的会话"，去读 `sessionModel('s1')`（测试没 mock 它 → jsdom 里
请求失败 → 数据置空 → chip 退到 `No model selected`），而测试断言的是新会话页那条读法
（`chatModelDefault`）。测试从来没真正跑在它声称的页面上。

改成显式的新会话页状态后：这条测试才第一次真的验证"新会话页的模型 chip 显示服务器算出来的
最近使用模型"，`pnpm vitest run` 回到全绿，批次终验不必再带一个"存量失败"的尾巴。

## Current state

### 红的现状（已实测）

```
$ cd frontend && pnpm vitest run src/views/ChatView.test.ts
 FAIL  src/views/ChatView.test.ts > ChatView conversation bubbles > shows the server-computed most recently used model on a new chat
AssertionError: expected 'No model selected' to contain 'deepseek-chat'
 ❯ src/views/ChatView.test.ts:735:27
 Tests  1 failed | 44 passed (45)
```

（单跑 `-t 'most recently used model on a new chat'` 与整文件跑结果一致，不是用例顺序造成的偶发。）

### 组件：新会话页与已开会话走两条读法（现行，正确，不要改）

`frontend/src/views/ChatView.vue:1402-1434`：

```ts
/**
 * What a new chat's model and effort chips have been set to, until its first
 * message opens the conversation and the page writes it as that conversation's
 * own choice. The chips themselves work before there is a session: they read
 * the agent's default (`chatModelDefault`) and the pick is held here.
 */
const draftModel = ref<{ provider: string; model: string; effort: string } | null>(null)

async function loadSessionModel(sid: string | null) {
  const generation = ++sessionModelGeneration
  if (!sid) draftModel.value = null
  try {
    // No session yet: the model a new conversation would start on.
    const res = sid ? await forebrainApi.sessionModel(sid) : await forebrainApi.chatModelDefault()   // ← :1415
    if (generation === sessionModelGeneration) sessionModelData.value = res
  } catch {
    // Without the facts the chips stay hidden: nothing is invented for a
    // conversation whose model could not be read.
    if (generation === sessionModelGeneration) sessionModelData.value = null
  }
}

watch(sessionId, (sid) => {
  void loadSessionModel(sid)
}, { immediate: true })
```

chip 的文案只读这份数据（`frontend/src/components/chat/ModelChip.vue:77`）：

```ts
const label = computed(() => props.selection?.model || t('chat.modelNone'))   // 'chat.modelNone' = 'No model selected'
```

模板把 `sessionModelData` 直接传给它（`ChatView.vue:418-427`，`:selection="sessionModelData"`）。

两个 API（`frontend/src/lib/api.ts:2327-2339`）：

```ts
  /** The model a conversation that does not exist yet would start on, with
   *  the same options a session's own read lists ... */
  chatModelDefault() {
    return api.get<SessionModelResponse>('/chat/model').then((res) => res.data)
  },

  sessionModel(sessionId: string) {
    return api.get<SessionModelResponse>(`/chat/sessions/${encodeURIComponent(sessionId)}/model`).then((res) => res.data)
  },
```

服务端 `/chat/model` 本身是对的（`pkg/gateway/api_extra.go:1827-1860`：`run.NewSessionModel(runner, …)` 取
该 agent 最近使用的模型，取不到才退 `PrimaryModelSelection`）。**所以本计划不动任何产品代码。**

### 测试：mock 的 sessionId 是 `'s1'`（这就是病根）

`frontend/src/views/ChatView.test.ts:41`（`useChatStream` 的 mock 里）：

```ts
  f.sessionId = ref('s1')
```

`:555-559`（**上一个** describe 的收尾，把状态设回"已开会话"）：

```ts
  afterEach(() => {
    runtimeStatus().value = { kind: 'idle', elapsedMs: 0 }
    ;(fake as { sessionId: { value: string | null } }).sessionId.value = 's1'
    ;(fake as { messages: { value: unknown[] } }).messages.value = []
  })
```

`:639-642`（本用例所属的 describe——它自己的 `afterEach` **只清 messages**，不碰 sessionId）：

```ts
describe('ChatView conversation bubbles', () => {
  afterEach(() => {
    ;(fake as { messages: { value: unknown[] } }).messages.value = []
  })
```

`:714-743`（现行用例全文）：

```ts
  it('shows the server-computed most recently used model on a new chat', async () => {
    // The new-chat page never decides a model itself: the chip shows whatever
    // /api/chat/model answers — which the server computes as the agent's most
    // recently used model. Here the server names the chain's second entry, so
    // the chip must say deepseek-chat, not the chain's first.
    const defaultModel = vi.spyOn(forebrainApi, 'chatModelDefault').mockResolvedValue({
      provider: 'deepseek',
      model: 'deepseek-chat',
      effort: '',
      options: [
        { provider: 'zhipuai', model: 'glm-5.3-flash', label: 'GLM 5.3 Flash', efforts: [], supportsImages: true },
        { provider: 'deepseek', model: 'deepseek-chat', label: 'DeepSeek Chat', efforts: [], supportsImages: false },
      ],
    })
    const bySession = vi.spyOn(forebrainApi, 'sessionModel')
    try {
      const wrapper = await mountView()
      await flushPromises()

      const chip = wrapper.find('[data-testid="model-chip"]')
      expect(chip.exists()).toBe(true)
      expect(chip.text()).toContain('deepseek-chat')
      expect(defaultModel).toHaveBeenCalled()
      expect(bySession).not.toHaveBeenCalled()
      wrapper.unmount()
    } finally {
      defaultModel.mockRestore()
      bySession.mockRestore()
    }
  })
```

（`:745` 起的下一个用例 `draws an image refusal under its message …` mock 的是 `sessionModel`，
依赖"会话已打开"这个状态，所以**必须**在收尾把 `sessionId` 还原成 `'s1'`。）

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 安装（**仅当** `frontend/node_modules/.bin/vitest` 不存在时；`--frozen-lockfile` 保证不改 `pnpm-lock.yaml`） | `cd frontend && pnpm install --frozen-lockfile` | exit 0 |
| 本文件单测 | `cd frontend && pnpm vitest run src/views/ChatView.test.ts` | 45 passed |
| 全套单测 | `cd frontend && pnpm vitest run` | 无 `FAIL`（改造前恰好 1 failed，就是本用例） |
| 类型检查 | `cd frontend && pnpm typecheck` | exit 0 |
| 变更范围 | `git status --porcelain frontend/` | 只有 ` M frontend/src/views/ChatView.test.ts` |

## Scope

**In scope**（唯一允许改的文件）：
- `frontend/src/views/ChatView.test.ts`

**临时触碰并必须还原**（Step 3 的变异验证）：
- `frontend/src/views/ChatView.vue`（只改 `loadSessionModel` 里的一处读法，验证完立刻还原）

**Out of scope**（不要动）：
- `frontend/src/views/ChatView.vue` 的任何持久改动——组件的两条读法是对的，问题只在测试的挂载状态。
  若你读下来认为组件有错，**STOP 并报告**。
- `pkg/gateway/api_extra.go`（`/chat/model` 已按 MRU 实现）。
- mock 数据里的模型名（`deepseek-chat` / `glm-5.3-flash`）：它们只是 mock 的标签，与本次失败无关，
  **改不改都不会让测试变绿/变红**；为了把 diff 压到最小，保持原样。
- 把这条用例搬到 `describe('ChatView opens on the new-conversation page')`（`:1043`）之类的重组：
  收益只是归类更整齐，代价是更大的 diff。本期不做。
- `pkg/gateway/dist` 不用重建（没有任何源码/组件改动）。

## Git workflow

不提交。改动留在 dev 工作区，由 owner 手动提交。

## Steps

### Step 1: 让用例显式处在新会话页状态

把 `frontend/src/views/ChatView.test.ts:714-743`（含末行 `  })`）整段替换为：

```ts
  it('shows the server-computed most recently used model on a new chat', async () => {
    // The new-chat page never decides a model itself: the chip shows whatever
    // /api/chat/model answers — which the server computes as the agent's most
    // recently used model. Here the server names the chain's second entry, so
    // the chip must say deepseek-chat, not the chain's first.
    //
    // "New chat" is stated by the state itself: with no session open the
    // component reads /chat/model (loadSessionModel); with one open it reads
    // that conversation's own row, which is a different question.
    const sessionIdRef = (fake as { sessionId: { value: string | null } }).sessionId
    sessionIdRef.value = null
    const defaultModel = vi.spyOn(forebrainApi, 'chatModelDefault').mockResolvedValue({
      provider: 'deepseek',
      model: 'deepseek-chat',
      effort: '',
      options: [
        { provider: 'zhipuai', model: 'glm-5.3-flash', label: 'GLM 5.3 Flash', efforts: [], supportsImages: true },
        { provider: 'deepseek', model: 'deepseek-chat', label: 'DeepSeek Chat', efforts: [], supportsImages: false },
      ],
    })
    const bySession = vi.spyOn(forebrainApi, 'sessionModel')
    try {
      const wrapper = await mountView()
      await flushPromises()

      const chip = wrapper.find('[data-testid="model-chip"]')
      expect(chip.exists()).toBe(true)
      expect(chip.text()).toContain('deepseek-chat')
      expect(defaultModel).toHaveBeenCalled()
      expect(bySession).not.toHaveBeenCalled()
      wrapper.unmount()
    } finally {
      defaultModel.mockRestore()
      bySession.mockRestore()
      // The cases after this one read an open conversation.
      sessionIdRef.value = 's1'
    }
  })
```

**Verify**：`cd frontend && pnpm vitest run src/views/ChatView.test.ts`
→ `Tests 45 passed (45)`，无 `FAIL`。

### Step 2: 全套单测与类型检查

**Verify**：

1. `cd frontend && pnpm vitest run` → 无 `FAIL`（改造前恰好 1 failed，就是本用例；总数以实际为准）
2. `cd frontend && pnpm typecheck` → exit 0

### Step 3: 变异验证——新会话页读错来源时，测试必须变红

证明改写后的测试真的在检查"新会话页读 `/chat/model`"，而不是随便绿。

1. 编辑 `frontend/src/views/ChatView.vue:1415`，把
   `const res = sid ? await forebrainApi.sessionModel(sid) : await forebrainApi.chatModelDefault()`
   临时改成 `const res = await forebrainApi.sessionModel(sid ?? '')`。
2. **Verify（期望失败）**：`cd frontend && pnpm vitest run src/views/ChatView.test.ts`
   → 1 failed，`expected 'No model selected' to contain 'deepseek-chat'`（正是本次修掉的那条症状）。
3. **还原**：**不要**用 `git checkout -- frontend/src/views/ChatView.vue`（工作区可能有 owner 未提交改动）。
   用编辑器把该行改回原文，逐字核对与上面摘录一致。
4. **Verify**：`git status --porcelain frontend/src/views/ChatView.vue` → 无输出。
   非空就再逐字对照摘录改一次；仍非空 → STOP 1，**不要**用 `git checkout` / `git restore` 覆盖。

### Step 4: 收尾门禁

**Verify**：

1. `cd frontend && pnpm vitest run src/views/ChatView.test.ts` → 45 passed
2. `git status --porcelain frontend/` → 只有 ` M frontend/src/views/ChatView.test.ts`
3. `git status --porcelain pkg/gateway/dist` → 无输出（没重建 dist）

## Test plan

- 改写（不新增）`frontend/src/views/ChatView.test.ts:714` 的那条用例，不动其它用例。
- 覆盖的用例：无会话（新会话页）时 chip 显示 `/chat/model` 给的模型；`chatModelDefault` 被调用；
  `sessionModel` 未被调用。
- 反例覆盖靠 Step 3 的变异（新会话页改读会话接口 → 红）。
- 结构范例：同文件 `:745` 的 `draws an image refusal …`（同样的 mock/spy 套路与 finally 还原）。

## Done criteria

- [ ] `cd frontend && pnpm vitest run src/views/ChatView.test.ts` → 45 passed，无 FAIL
- [ ] `cd frontend && pnpm vitest run` → 无 FAIL
- [ ] `cd frontend && pnpm typecheck` → exit 0
- [ ] Step 3 的变异为 FAIL，事后 `git status --porcelain frontend/src/views/ChatView.vue` 为空
- [ ] `git status --porcelain frontend/` 只有 `frontend/src/views/ChatView.test.ts`
- [ ] 用例里出现 `sessionIdRef.value = null`（挂载前的显式状态）与 `sessionIdRef.value = 's1'`（收尾还原）

## STOP conditions

1. **STOP 1**：前置 grep 的锚点数量不符，`ChatView.vue:1415` 已不是 `sid ? … : chatModelDefault()`，
   或前置的 `git status --porcelain` 显示两个目标文件不干净。
2. **STOP 2**：改成 `sessionId = null` 后 chip 仍然显示 `No model selected`（说明还有别的东西清掉了
   `sessionModelData`），或 chip 显示的不是 mock 的 `deepseek-chat`：停下报告实测值，
   **不要**为了变绿去放宽断言或 mock 更多接口。
3. **STOP 3**：修复后 `:745` 之后的用例变红（说明 sessionId 没还原或还原位置错）：按 Step 1 的 finally 修，
   仍红则停下报告。
4. **STOP 4**：Step 3 的变异**不变红**：停下报告。
5. **STOP 5**：基线或全套单测里出现与本用例无关的 FAIL（环境/别人的存量红）：报告清单，不要修。
6. **STOP 6**：需要改动本计划 Scope 之外的文件。

## Maintenance notes

- 本文件里多个 describe 靠 `fake.sessionId` 这个共享 ref 表达"有没有打开的会话"。**任何断言新会话页行为的
  用例都必须自己把 `sessionId` 设为 `null` 并还原**，不要依赖上一个 describe 的 `afterEach` 留下的状态——
  本次的存量红就是这种隐式顺序依赖造成的（在 `-t` 单跑时同样红，说明它连"顺序恰好对上"都没有发生过）。
- 评审要点：确认 diff 只有那一条用例，且 `finally` 里同时还原了两个 spy 和 `sessionId`。
- 未来若 `/chat/model` 的响应形状变化（例如新增字段），这条用例的 mock 要同步；它断言的是
  "chip 显示服务器给的模型"，不是某个具体模型名。