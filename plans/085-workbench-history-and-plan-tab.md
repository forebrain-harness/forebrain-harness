# 085 — 工作台与抽屉：问答/计划都是历史、计划原文在抽屉标签页里渲染、工作台按抽屉的方式收起

## Status

- **DONE**（2026-10-20 立项，基线 `cd11539`；2026-10-20 执行完毕，评审 APPROVE，改动留工作区未提交）
- 复审：2026-10-20 冷读复核（基线 `cd11539`）。修正了行号漂移，并改掉一处会照抄出来的缺陷：
  `handleSessionPlanMarkdown` 现在**没有**会话归属校验，计划原写「照它的形状」会把漏洞带进新端点
  （见 Step 2）。
- 优先级 **P1**，工作量 **M–L**，风险 **MED**
- 依据：owner 2026-10-20 已审批的原型
  `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`：
  - 「工作台 · 概览」（`#ij-bench`）里的「问答」「计划」两张历史卡；
  - 计划行 →「查看全文」→ 右侧抽屉的计划标签页（原型函数 `benchPlansCard()` / `planDocBody()` /
    `mdToHtml()`）；
  - 工作台头栏的收起图标与滑入（原型 `.bench.in` / `.sad.in` 复用 `@keyframes dw-in`）。
- 对应 owner 原话（摘三条）：
  - 「工作台里的问答和计划可能在一个长会话里有多次，打算怎么展示多次问答和多份计划？必须设计原型」
  - 「新增一个交互效果，点击"查看原文"，打开子代理视图那个抽屉，以标签页形式展示计划原文，
     需要将 markdown 格式的计划文件渲染成网页」
  - 「工作台的打开收起效果改成抽屉，右上角关闭图标改成抽屉的收起图标」

---

## 为什么

三件事在今天都是错的或不存在的：

1. **计划只显示「当前那一份」。** 服务端 `GET /chat/sessions/:id/plan-md` 只返回
   `state.GetPlanForSession`（最新一份），而一个会话的计划目录里**本来就可能有多个 `.md`**。
   两种写入并存，事实以代码为准（`pkg/state/plan.go`）：
   - 模型写一份新计划 → **新文件**（包注释 `:8-10`：「producing a new plan creates a new file
     rather than overwriting the previous one」）；
   - `SetPlanForSession`（`:156-160`）→ **覆写**「当前那份」，而 `PlanPathForSession`（`:75`）=
     `newestMarkdown`（`:116-138`）找到的最新文件；目录为空时退回 `<dir>/plan.md`。

   于是「上一版计划」在网页上根本回不去：目录里有历史，接口只交最新一份。
2. **「查看全文」是一段裸 Markdown。** `OverviewTab.vue:82-84` 打开弹窗，里面是
   `<pre class="bench-plan-full">{{ planMarkdown }}</pre>` —— 标题、清单、表格全都不渲染；
   原型要求的是「按文档渲染」，并且落在**抽屉标签页**里（与子代理同一个抽屉）。
3. **工作台看起来不像抽屉。** 它与子代理抽屉共用右侧、一次只开一个（owner 2026-10-18 的契约），
   但头栏是一个「×」（关闭外形，`ChatWorkbench.vue:5-7`），而抽屉是 `PanelRightClose`「收起」
   （`SubagentDrawer.vue:60-69`），且两者都没有入场过渡。

另外，工作台概览里**没有「问答」卡**（`OverviewTab.vue` 只有 待办 / 计划 / MCP / 用量 / 运行中的代理），
一次长会话问过的几轮问答在网页上无处可查。

---

## 现状事实（行号基于 `cd11539`）

### 工作台

- `frontend/src/components/chat/ChatWorkbench.vue`
  - 头栏：`:4-8` `<span class="chat-workbench-title">{{ t('chat.workbench') }}</span>` +
    `<button class="chat-workbench-close" :aria-label="t('chat.workbenchClose')"><X/></button>`（按钮 `:5-7`）；
  - 样式：`:317-327` `.chat-workbench { width: 340px; border-left; }`（width 在 `:323`，border-left `:325`），
    `:348-356` `.chat-workbench-close`（hover `:357-360`）；
  - 窄屏浮层：`:328-336` `.chat-workbench--overlay { position: fixed; right:0; z-index:44; box-shadow }`；
  - 标签本地记忆：`:160-172` `storageKey = forebrain.workbench.tab.<scope>`（照这个写法加新键）。
- `frontend/src/components/chat/SubagentDrawer.vue:60-69` 的收起按钮已经是
  `PanelRightClose` + `t('chat.subagentDrawerCollapse')`（zh `收起`）。
- 两处互斥：`views/ChatView.vue:497-556`（`drawerVisible` 时画抽屉，否则 `workbench.open` 时画工作台），
  `views/ChatView.vue:1376-1379` `drawerRecords` 由 `drawerTabs` 过滤成 `SubagentTranscript`。
- `locales/index.ts:245` `'chat.workbenchClose': '收起工作台'`（语义已经是「收起」，只是图标是 ×）。

### 概览

- `frontend/src/components/workbench/OverviewTab.vue`
  - 待办卡 `:16-27`（`todos` 来自 `sessionTodos`）；
  - 计划卡 `:30-41`（`planTitle` / `planPoints` 从 `planMarkdown` 里正则切出来），
    「查看全文」按钮 `:33` 打开弹窗 `:82-84`（`<pre>` 裸 Markdown）；
  - 数据加载 `:180-204`：`forebrainApi.sessionPlanMd(sid)` → `{markdown}`；重新加载的 `watch` 在 `:206`。
- 没有「问答」卡：`grep -rn "benchAnswers" frontend/src` 无命中。

### 计划数据

- `frontend/src/lib/api.ts:2247-2249` `sessionPlanMd(sid)` → `GET /chat/sessions/:id/plan-md`。
- `pkg/gateway/api_extra.go:2272-2292` `handleSessionPlanMarkdown`：
  `state.GetPlanForSession(...)` → `{markdown}`（**只有当前一份**）。
  **它没有会话归属校验**：只判了方法 + `sid` 非空，然后 `s.sessionProjectKey(r.Context(), sid)`；
  未知 sid 会得到 200 + `{"markdown":""}`。同文件的 `handleActions`（`:3308-3318`）走的是
  `s.Sessions.HasSession` → 非自有 404。本计划的两个端点都要按后者来（见 Step 2）。
- `pkg/state/plan.go:49-80`：一个会话的计划目录 `…/plans/<projectKey>/<sessionID>/` 里可以有多份 `.md`
  （`PlanDirForSession` 在 `:49`，路径清洗在 `planSessionSegment`）；
  `isPlanMarkdown`（`:103`）是「什么算一份计划」的唯一判定，`newestMarkdown`（`:116-138`）
  就是「最新那份」的判定。**新函数要复用这三个，不要另写一套目录拼接与后缀判断。**

### 问答数据

- 已回答的提问动作在 actions 表里，带 `payload_json`（原表单）与 `answer_json`（答案）：
  `pkg/state/action_service.go:27-47`（`CreatePending` 写 payload）、
  `pkg/gateway/api_extra.go:3290-3337`（`GET /actions?status=…&session_id=…`，行里带 `payload_json` 与
  `answer_json`，`actionListRow` 直接嵌 `state.Action`）。
- 前端只取过 `pending`：`usePendingActions.ts:205` `forebrainApi.actionsList('pending', sid)`
  （`actionsList(status?, sessionId?, agentId?)` 的签名在 `frontend/src/lib/api.ts:2866`）。
- **`/actions` 的 `status` 参数不校验白名单**（handler 原样透传，只是 SQL 谓词）——枚举值是
  `pending / answered / approved / denied / cancelled / expired / error`；传别的值不报错、返回空表。
  本计划只用 `answered`，别顺手加校验（那是另一件事）。

### 抽屉的标签模型

- `SubagentDrawer.vue:113-127`：props `tabs: SubagentTranscript[]`、`active: string`、`records`、
  `seen`、`overlay`、`now`；`:129-135` emits `select | close-tab | open | collapse | escape-key`。
- `SubagentDrawer.vue:11-45` 每个标签：图标 + 标签名 + 未读点 + 关闭按钮。
- `ChatView.vue:497-540`：`<SubagentDrawer>` 的默认插槽里 `v-for record in drawerRecords` 渲染
  `SubagentTab`（`:511-539`）。**正文属于 `ChatView` 的插槽，不属于抽屉组件**——计划标签的正文
  要加在这个插槽里（见 Step 5）。
- **标签键是 `agentId`**，`drawerRecords` 会过滤掉非子代理的记录 —— 所以「计划标签」必须作为
  **并行的一类标签**加进去，不能塞进 `SubagentTranscript`。

---

## Steps

### Step 1 — 引擎：一个会话的计划清单

`pkg/state/plan.go` 新增（照 `newestMarkdown` 的写法，复用 `isPlanMarkdown`）：

```go
// PlanFile is one plan document a conversation wrote, as a list needs it: where
// it lives, when it was written, and its text. A conversation that replans
// writes a new file each time (PlanPathForSession only ever names the newest),
// so the record of earlier plans is the directory, not the newest file.
type PlanFile struct {
    Path      string // absolute
    Name      string // base name, e.g. "add-auth-validation.md"
    UpdatedAt time.Time
    Markdown  string
}

// PlansForSession lists a conversation's plan files, newest first. A missing
// directory is an empty list, not an error: most conversations never plan.
func PlansForSession(workspaceRoot, projectKey, sessionID string) ([]PlanFile, error)
```

规则：
- 目录用 `PlanDirForSession(workspaceRoot, projectKey, sessionID)`（`:49`）拿，不要自己 join；
- 只收 `isPlanMarkdown`（非隐藏、非目录、`.md`，`:103`）。
- 按 `ModTime` 倒序，同 mtime 时按文件名升序（结果稳定，测试可断言）。
- 单个文件上限 1 MiB，超过就跳过并在 `Markdown` 里给空串（不要把一个巨大的计划塞进 JSON）。
- 读失败的文件跳过，不返回错误（一个坏文件不该让整页空白）。

测试 `pkg/state/plan_test.go`：新建三条 `.md`（不同 mtime）→ 顺序正确；隐藏文件与子目录不计；
空目录返回空切片 + nil error。

验证：

```bash
gofmt -l pkg/state && go vet -tags fts5 ./pkg/state
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run PlansForSession -count=1
```

### Step 2 — gateway：`GET /chat/sessions/:id/plans`

`pkg/gateway/api_extra.go`：在 `:171` 的 `chatSessions.Get("/:id/plan-md", …)` 旁边加

```go
chatSessions.Get("/:id/plans", s.handleSessionPlans)
```

处理器照 `handleSessionPlanMarkdown`（`:2272-2292`）的形状，**但归属校验照 `handleActions`
（`:3308-3318`）而不是照 `plan-md`**：

```go
sid := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
if sid == "" {
    http.NotFound(w, r)
    return
}
if s.Sessions != nil { // handleActions 就是这个写法：没有会话仓时不假装校验过
    owned, err := s.Sessions.HasSession(r.Context(), sid)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    if !owned {
        http.NotFound(w, r)
        return
    }
}
```

理由（也是要给 owner 披露的一条现状缺陷）：`plan-md` **今天没有**这一步，未知或不属于本 agent 的
`sid` 会拿到 200 + `{"markdown":""}`。新端点会把「一个会话的全部计划正文」交出去，不能继承这个形状
——所以本步把同一段守卫也补进 `handleSessionPlanMarkdown`（两个端点都校验；前端对 `plan-md` 的
既有调用不受影响，它一直用当前会话的 id）。

**owner 2026-10-20 已裁决：两个端点一起补**，不接受「只守新端点、旧缺口记账」也不接受「两个都保持现状」。
这条是本计划范围内唯一一处**既有行为变更**，Done criteria 里的人工核对要贴出
「未知 sid 现在返回 404」的证据（`curl` 或测试名）。

- `state.PlansForSession(s.stateRoot(), s.sessionProjectKey(r.Context(), sid), sid)`；
- 输出

```json
{"plans":[{"name":"add-auth-validation.md","path":"plans/<projectKey>/<sid>/add-auth-validation.md",
           "updated_at":1729400000,"markdown":"# Plan: …"}]}
```

`path` 用相对于 home 的写法（与原型里 `plans/session-cookie.md` 一致的显示口径），
**不要**吐绝对路径（用户 home 的绝对路径不该进 JSON）。

测试 `pkg/gateway/api_extra_test.go`：种两个计划文件 → 200、顺序、字段齐全；
未知会话 → 404（`plans` 与 `plan-md` 两端点都断言，这也是上面那段守卫的回归测试）。

验证：

```bash
gofmt -l pkg/gateway && go vet -tags fts5 ./pkg/gateway
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run SessionPlans -count=1
```

### Step 3 — 前端 API + 计划/问答的取数

1. `frontend/src/lib/api.ts`：在 `sessionPlanMd`（`:2247`）旁边加

   ```ts
   sessionPlans(sessionId: string) {
     return api.get<{ plans: { name: string; path: string; updatedAt: number; markdown: string }[] }>(
       `/chat/sessions/${sessionId}/plans`,
     ).then((res) => res.data.plans ?? [])
   }
   ```

   （后端字段是 snake_case（`updated_at`），但**响应体会被拦截器统一转成 camelCase**
   ——`api.ts:1821-1823` 的 `response.data = toCamelCase(response.data)`。所以类型里写 `updatedAt`
   就对了，POST body 才需要 `toSnakeCase`。不要再手写一次转换。）
2. 新建 `frontend/src/composables/useSessionPlans.ts`：`{ plans, loading, reload }`，
   调 `sessionPlans(sid)`，并在会话切换时重载（照 `OverviewTab.vue:206` 的 `watch` 写法）。
3. 新建 `frontend/src/composables/useAnsweredAsks.ts`：`forebrainApi.actionsList('answered', sid)`，
   把 `user_interaction` 的行解成 `{ id, at, questions: [{header, prompt, answers: string[]}] }`：
   - 问题与原答案都从 `payloadJson`（表单）与 `answerJson`（`{answers:[{questionId, optionIds, otherText}]}`）里解；
   - `optionIds` 要映射回选项 label（用 `payloadJson` 里的 `options`）；
   - 上限 50 条，最新的在前。

验证：

```bash
cd frontend && corepack pnpm exec vue-tsc --noEmit
cd frontend && corepack pnpm test src/composables/useSessionPlans.test.ts src/composables/useAnsweredAsks.test.ts
```

### Step 4 — 概览：问答与计划都是「一张卡 + 一串历史」

改 `frontend/src/components/workbench/OverviewTab.vue`：

1. 新增「问答」卡（在「待办」之后、「计划」之前）：
   - 卡头：`MessageSquareText` 图标 + `t('chat.benchAnswers')`（zh `问答`）+ 右侧 `t('chat.benchAsksCount', {n})`（`3 次`）。
   - 每一行是一条历史（最新在上）：`<button class="bench-hist-row">` 里依次是
     时间（`10:31`，等宽、muted）、首题 prompt（`truncate`）、右侧 `N 个问题`。
   - **一次只展开一条**：点开一条收起同族的其它条（`openAsk = ref<string>('')`）。
   - 展开体：该次每一题的 `问题` 一行 + 每条答案一行（`→ 答案`），**不换行成段落**。
2. 「计划」卡改成同样的历史形状：
   - 卡头：`Map` + `t('chat.benchPlan')` + 右侧 `t('chat.benchPlansCount', {n})`（`3 份`）。
   - 每行：时间、计划标题、右侧 `☑ 3/5`（`SquareCheck` 图标 + `done/total`；**没有**「最新」这类字）。
   - `done/total` 从该份 markdown 数 `- [x]` / `- [ ]`（`frontend/src/lib/planProgress.ts` 纯函数 + 单测）。
   - 展开体：标题、其要点列表（`planPoints` 逻辑保留），右下角一枚 `t('chat.benchPlanView')`（`查看全文`）。
3. `查看全文` 不再开弹窗：`emit('open-plan', plan)` 交给 `ChatWorkbench` → `ChatView` →
   抽屉的计划标签（Step 5）。**删掉** `ForebrainModal` / `<pre class="bench-plan-full">` /
   `planOpen` 这一段（`:82-84`、`:130`）。
4. 文案键（zh / en 各加一份）：
   `chat.benchAnswers` 问答 / Answers、
   `chat.benchAsksCount` `{n} 次` / `{n}×`、
   `chat.benchPlansCount` `{n} 份` / `{n} plans`、
   `chat.benchAskQuestions` `{n} 个问题` / `{n} questions`、
   `chat.benchPlanProgress` `{done}/{total}` / 同。
   （`chat.benchPlanView` 已存在，文案 `查看全文`。）

测试 `OverviewTab.test.ts`：两条问答 → 只有一条展开；三条计划 → 按 `updatedAt` 倒序；
`☑` 图标存在且 `done/total` 与 markdown 一致；点 `查看全文` 发出 `open-plan`（不再有弹窗）。

验证：

```bash
cd frontend && corepack pnpm test src/components/workbench/OverviewTab.test.ts src/lib/planProgress.test.ts
```

### Step 5 — 抽屉：多一类「计划」标签，正文渲染 Markdown

1. `SubagentDrawer.vue` 加两个 props 与两个 emit：

   ```ts
   planTabs?: { id: string; label: string; path: string; at: string; done: number; total: number; markdown: string }[]
   activePlan?: string
   // emit: ('select-plan', id) | ('close-plan', id)
   ```

   标签渲染在子代理标签**之后**：`Map` 图标 + `label`；`activePlan === id` 时反显。
   **不要**给计划标签加未读点（它不会有新内容）。

   **抽屉只是个外壳**：它的正文是默认插槽，内容由 `ChatView.vue:502-539` 传入
   （每个标签一个 `SubagentTab`）。所以这里只改标签栏与 props/emits，正文不在这个组件里写。
2. 正文：计划文档由 `ChatView.vue` 在同一个插槽里渲染，**不要**塞进 `SubagentDrawer`：
   - 新增一个计划文档块（新建 `frontend/src/components/chat/PlanDoc.vue` 或直接写在 `ChatView` 里），
     `v-if="activePlan"`，与 `SubagentTab` 的 `v-for` 并列；
   - 顶部一行 `.plan-doc-meta`：`<span class="mono">{{ path }}</span> · {{ at }} · ☑ {{ done }}/{{ total }}`；
   - 下面 `<MessageResponse :content="markdown" />`（`@repo/elements/message`，仓库已有的
     Markdown 渲染器——`ApprovalCard.vue:49` 就是这么用的，**不要**新引 Markdown 库）；
   - 子代理标签的 `v-show` 从 `record.agentId === shownActive` 改成
     `!activePlan && record.agentId === shownActive`（它们仍挂载，草稿与滚动位置不丢——
     这正是 `:495-497` 那段注释的意图）。
   - **没有输入框是自然结果**：输入框在 `SubagentTab` 的 `.subagent-composer` 里，
     计划文档显示时没有任何 `SubagentTab` 可见，不要另写「隐藏底栏」的逻辑。
3. `ChatView.vue`：
   - 新增 `planTabs` / `activePlan` 状态；
   - 工作台的 `open-plan` 事件 → `openPlanTab(plan)`（已开则只切过去）；
   - 打开计划标签时收起工作台（`workbench.close()`）——两者互斥是既有契约
     （`ChatView.vue:497-556` 的 `drawerVisible` 分支）；
   - 关掉最后一个计划标签后，抽屉若没有子代理标签就整体收起
     （`drawerVisible` 的判定要把它算进去）。
4. 测试 `SubagentDrawer.test.ts` + `ChatView` 相关用例：
   - 计划标签与子代理标签共存，切换时正文互换；
   - 计划标签下没有输入框；切回子代理标签输入框回来；
   - 关掉计划标签不影响子代理标签。

验证：

```bash
cd frontend && corepack pnpm exec vue-tsc --noEmit
cd frontend && corepack pnpm test src/components/chat/SubagentDrawer.test.ts
```

### Step 6 — 工作台按抽屉的方式收起

1. `ChatWorkbench.vue:5-7`：`<X>` 换成 `<PanelRightClose>`（`lucide-vue-next`），
   `aria-label` / `v-tip` 继续用 `t('chat.workbenchClose')`（已是「收起工作台」）。
2. 头栏几何对齐抽屉：`.chat-workbench-head` 改成 `height: 44px; padding: 0 8px 0 16px`，
   `align-items: center`（对照 `SubagentDrawer.vue` 的 `.subagent-drawer-head`：`height:44px; padding:0 8px`）。
3. 入场动画（两个面板共用一条）：

   ```css
   @keyframes forebrain-drawer-in { from { transform: translateX(32px); opacity: .6 } to { transform: none; opacity: 1 } }
   .chat-workbench, .subagent-drawer { animation: forebrain-drawer-in 160ms ease-out }
   @media (prefers-reduced-motion: reduce) { .chat-workbench, .subagent-drawer { animation: none } }
   ```

   放在各自的 scoped style 里（Vue 的 `<style scoped>` 各自持有一份 keyframes 没关系），
   或提到共用的样式文件——**但不要**在 `App.vue` 里改全局动画。
   注意：动画只在**挂载**时播放一次；工作台切标签（`ChatWorkbench` 内部状态）不会重挂，因此不会重播。
4. 测试：`ChatWorkbench.test.ts` 加一条——头栏按钮渲染的是 `panel-right-close` 图标类，
   `aria-label` 为 `收起工作台`；点它仍 emit `close`。

验证：

```bash
cd frontend && corepack pnpm exec vue-tsc --noEmit
cd frontend && corepack pnpm test src/components/chat/ChatWorkbench.test.ts src/components/chat/SubagentDrawer.test.ts
```

### Step 7 — e2e

新增 `frontend/e2e/workbench-history.spec.ts`（种数据照 `frontend/e2e/exit-plan-approval.spec.ts`：
它已经示范了怎么把 plan 文件写进 `E2E_HOME/workspace/plans/<projectKey>/<session>/`，见那份 spec 的
`:62-67`）：

1. 种两个计划文件（不同 mtime）与两条已答的 `user_interaction` 动作；
2. 打开工作台概览 → 「问答」卡头写 `2 次`、两条历史、只有一条展开；
3. 「计划」卡头写 `2 份`，行里是 `☑ 1/3` 这类进度；
4. 点某行的「查看全文」→ 工作台收起、抽屉打开、标签名 `计划 · <标题>`、
   正文有渲染后的 `<h1>` 与列表（`querySelector('.plan-doc h1')`），**没有**输入框；
5. 顶栏再点「工作台」→ 抽屉收起、工作台回来；头栏图标是 `panel-right-close`。

验证：

```bash
FOREBRAIN_E2E_SPEC=e2e/workbench-history.spec.ts scripts/acceptance/web_e2e.sh
```

---

## Done criteria

```bash
gofmt -l pkg/state pkg/gateway                          # 无输出
go vet -tags fts5 ./pkg/state ./pkg/gateway              # 无输出
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/gateway -count=1   # ok
cd frontend && corepack pnpm exec vue-tsc --noEmit       # 退出 0
cd frontend && corepack pnpm test                        # 全绿
FOREBRAIN_E2E_SPEC=e2e/workbench-history.spec.ts scripts/acceptance/web_e2e.sh   # web e2e: PASS
```

人工核对：

- `grep -rn "bench-plan-full\|planOpen" frontend/src` 无命中（裸 Markdown 弹窗已删）。
- 抽屉里计划标签的正文是渲染后的文档：贴一张真 gateway 的截图（不是单测）。
- 工作台头栏图标与抽屉头栏图标在同一位置（左对齐同一像素列）。
- **两个端点对未知 sid 都返回 404**（owner 2026-10-20 裁决的那处既有缺口）：贴
  `curl -s -o /dev/null -w '%{http_code}' .../chat/sessions/does-not-exist/plan-md` 与
  `.../plans` 两条的实际数字，或贴那两个测试名及其 PASS 行。

---

## 不在本计划范围

- 审批卡（083）与对话消息卡、视图模式（084）。
- 「问答/计划的每次都会进 transcript」这类引擎语义：本计划只读已有数据，不写新事件。
- 终端 TUI 的工作台（TUI 没有这个概念）。
- 计划文件的编辑能力（只读展示；编辑走「工作目录」标签，已有的编辑器）。

---

## 维护提示

- `PlansForSession` 返回的是**目录**里的事实，不是「当前计划」。任何「当前计划」语义继续用
  `GetPlanForSession` / `PlanPathForSession`（`pkg/state/plan.go:75-160`），两者不要互相替换。
- 抽屉的标签从此有两类：`agentId` 与 `plan:<name>`。任何按 id 找子代理的代码
  （`drawerRecords`、`seenAgentActivity`、`SubagentTab` 的 ref 表）都必须先判断类别，
  否则会把计划 id 当成子代理 id 去查。
- 计划文件名就是标签 id 的一部分。两条写入路径决定标签怎么变：模型写一份新计划 → 新文件 → 新标签；
  `SetPlanForSession` 覆写当前那份（文件名不变）→ 同一个标签内容刷新。所以「编辑计划后标签数不变」
  是正常的，不要为此加去重逻辑。

## 逃生口

1. `actionsList('answered', sid)` 在真机上不返回 `user_interaction` 行（说明服务端按 kind 过滤了）
   → 停，报告实际返回；**不要**改走 transcript 私下解析（那是另一套语义，需要 owner 决定）。
2. `MessageResponse` 渲染计划时对表格/任务清单支持不全 → 先按现状交付并在报告里列出缺的语法，
   不要为此引入新的 Markdown 库（仓库选型要走 owner 审批）。
3. 抽屉加第二类标签时发现 `drawerVisible` 之外还有别处以「标签数 > 0」判定抽屉可见 → 全部找出来
   一起改；只改一处会出现「标签在、面板不显示」。
4. 入场动画在窄屏浮层模式下把面板推出视口（`position: fixed` + transform）→ 停，
   报告实际几何；宁可只给非浮层模式加动画，也不要留下横向滚动条。
