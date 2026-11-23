# Plan 109：多模型服务行可以选「哪个模型是默认」（现在只能选第一个，默认落在第二个时按钮整个消失）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不用 worktree，
> 改动直接留在 dev 工作区。
>
> **本计划有一道 owner 闸门**：这是 Web 界面改动，必须先在设计稿 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`
> 上做原型、**经 owner 确认**之后才能写前端（owner 规则；确认前不要把 UI 写死进实现）。所以 **Step 1 产出原型后必须停下**，Step 2 起要等确认。
>
> **执行顺序**：本计划改 `frontend/src/views/ProvidersView.vue` 与它的测试，计划 `100-provider-edit-replaces-in-place`、
> `107-small-hygiene-copy-comment-and-resume-context`、`103-ci-gates-gofmt-and-frontend-typecheck` 也改这些文件——**它们先落地，本计划最后做**。
>
> **Drift check（先跑）**：这些文件在工作区里有未提交改动，不用哈希，**按符号定位**：
> `grep -n 'function isDefaultRow\|function setDefault\|provider-default-set-' frontend/src/views/ProvidersView.vue` 必须三者都命中；
> 且 `isDefaultRow` 仍是 "行内任一模型等于默认就为真"、`setDefault` 仍是 "取 `row.models[0]`"。对不上即 STOP 1。
> （工作区里 `pkg/*` 的既有改动与本计划无关，不要碰、不要回滚。）

## Status

- **Priority**: P3
- **Effort**: S–M（前端一个组件的行为 + 文案 + 3–4 个测试；原型是主要工作量）
- **Risk**: LOW（后端不动：`default_model` 本来就接受列表里任意 `provider/model`）
- **Depends on**: `100`、`107`、`103`（仅顺序）；owner 对原型的确认
- **Category**: direction（补齐 `default_model` 重构留下的 UI 缺口）
- **Planned at**: commit `55019a2` + 未提交工作区，2026-10-23

## Why this matters

`default_model` 重构之后，默认模型是个有名字的值（`provider/model`），模型服务表用「默认」徽标和每行的「设为默认」按钮表达它
（`ProvidersView.vue`）。但一行可以服务**多个模型**（`models: [a, b]`，折叠自相邻条目），而 UI 只按「行」处理：

1. `isDefaultRow(row)`：行内**任一**模型等于默认，整行就是默认行 ⇒ 「设为默认」按钮 `v-if="!isDefaultRow(row)"` **整个隐藏**。
2. `setDefault(row)`：永远取 `row.models[0]`。

后果：行 `[a, b]` 的默认是 `b` 时，没法在这一行把默认切回 `a`（按钮消失）；默认在别处时，只能把 `a` 设成默认，永远选不了 `b`。
想选 `b` 只能先把它删出这一行再单独加——而 `connect` 登录流程恰好会把新选的模型**追加**成行的第二个模型。徽标也只说「这一行里有默认」，不说是哪个模型。

后端不用动：`PUT /api/providers` 的 `default_model` 接受列表里任意一个 `provider/model`，`GET` 也返回精确的 `default_model`。缺的只是前端让用户**指明是哪一个模型**。

## Current state

**表格单元**（`ProvidersView.vue` 约 `:56-85`，节选）：

```vue
        <template #cell-provider="{ row }">
          <span class="forebrain-cell-1" v-tip:top.trunc="row.provider">{{ row.provider }}</span>
          <BadgeComponent v-if="isDefaultRow(row)" class="ml-1.5" :data-testid="`provider-default-${rowIndex(row)}`">{{ t('providers.defaultBadge') }}</BadgeComponent>
        </template>
        <template #cell-models="{ row }">
          <span class="forebrain-cell-1" v-tip:top.trunc="row.models.join('、')">{{ row.models.join('、') }}</span>
        </template>
        ...
        <template #cell-actions="{ row }">
          <RowActions>
            <RowAction
              v-if="!isDefaultRow(row)"
              :icon="Star"
              :label="t('providers.setDefault')"
              :name="row.provider"
              :disabled="saving"
              :data-testid="`provider-default-set-${rowIndex(row)}`"
              @click="setDefault(row)"
            />
            <RowAction :icon="Pencil" … @click="openEdit(row)" />
            <RowAction :icon="Trash2" … @click="askRemove(row)" />
          </RowActions>
        </template>
```

**脚本部分**：

```ts
/** Whether the agent's effective default names any model on this row. */
function isDefaultRow(row: ProviderRecord): boolean {                                  // 约 :625
  const provider = String(row.provider ?? '').trim()
  if (!provider || !defaultModel.value) return false
  return (row.models ?? []).some((model) => defaultModel.value === `${provider}/${String(model ?? '').trim()}`)
}

/** Retargets agents.definitions.<agent>.default_model at this row. … A row
 * serving several models sets its first one (the tooltip says so). */
function setDefault(row: ProviderRecord) {                                             // 约 :798
  const provider = String(row.provider ?? '').trim()
  const model = String(row.models?.[0] ?? '').trim()
  if (!provider || !model) return
  void writeWholeOrder(() => {}, undefined, `${provider}/${model}`)                    // 第三个参数就是 default_model
}
```

`defaultModel` 是页面级 `ref<string>`，由 `providersPage` 的返回值写入（`"provider/model"`，服务端解析后的有效默认）。
文案键：`providers.setDefault`（"设为默认" / "Set as default"）、`providers.defaultBadge`（"默认" / "Default"）。

**既有测试**（`ProvidersView.test.ts`）：`names the row on every action; the default row shows no set-default`（断言默认行的操作按钮只有「编辑」「删除」）、
`sets the default without touching the order`（点 `provider-default-set-1` 后 `saveProviders` 的第二个实参是 `'openai/gpt-5'`）。
**这两条在新交互下仍应成立**（单模型行的体验不变）。

**设计稿**：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html` 的 `provp` 页（"模型服务"）；该页说明文字里已写 "每行操作列『设为默认』可切换"。
设计稿是 owner 在改的文件，工作区里有未提交改动，不要回滚。

**约定（owner 规则）**：提示与说明文案必须简洁，能用标签表达就不写句子；新建/编辑表单用弹窗；表格不加页面标题层。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 前端单测 | `cd frontend && npx vitest run src/views/ProvidersView.test.ts` | 全绿 |
| 前端类型 | `cd frontend && pnpm typecheck`（计划 103 之前用 `npx vue-tsc --noEmit`） | exit 0 |
| 前端全量 | `cd frontend && npx vitest run` | exit 0 |
| 打开原型 | `open docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html` | 浏览器打开 |

## Scope

**In scope**：
- `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`（Step 1）
- `frontend/src/views/ProvidersView.vue`、`frontend/src/views/ProvidersView.test.ts`、`frontend/src/locales/index.ts`（Step 2 起，owner 确认后）

**Out of scope**：
- 任何后端文件——`default_model` 的写入、校验、读回都已经支持任意模型。
- 表格列、排序、分页、其它行操作（编辑、删除）的行为。
- `/model` 选择器（它选的是**会话**用哪个模型，与 `default_model` 是两件事）。
- `pkg/gateway/dist/*`、`frontend/e2e/*`——不重建 dist，不跑 e2e。

## Git workflow

- **不提交**。改动留在 dev 工作区。

## Steps

### Step 1：在设计稿上做原型，然后**停下**

在 `app-preview.html` 的 `provp`（"模型服务"）页上迭代（不要另起文件；原型外框保持白色/浅色，不加深色模式覆盖）。要表达的是：

- **单模型行**：保持现状——点一下「设为默认」直接生效；默认行不显示该按钮。
- **多模型行**（造一个例子：同一服务两三个模型，默认落在其中第二个）：
  - 「设为默认」在这一行**始终可用**（只要行里还有不是默认的模型），点开后是一个小菜单，列出这一行的模型，当前默认的那个带勾；点另一个即切换。
  - 「默认」徽标能说明**是这一行里的哪个模型**——推荐在徽标上带模型名（如「默认 · b」）或用悬停提示；二选一，原型里给 owner 看。
- 文案按「能用标签就不写句子」写；菜单项就是模型名本身。
- 在 `provp` 页的说明文字里把「每行操作列『设为默认』可切换」补成包含「多模型行可以选其中哪个」的意思。

给 owner 备选的话，最多两个版本，各用一个页面标签区分（页面清单在文件里 `provp` 附近那一长行），并说明推荐哪个、为什么。

**Verify**：在浏览器里打开 `app-preview.html`，能进入「模型服务」页并看到多模型行的菜单与徽标；控制台无报错。

**然后停下**：在报告里写明原型改了什么、有几个备选页、推荐哪个。**未得到 owner 确认不要做 Step 2。**

### Step 2：先写会红的测试（owner 确认后）

在 `ProvidersView.test.ts` 里，仿 `sets the default without touching the order`（`mountPage()` + 点 `data-testid` + `flushPromises()` + 读 `vi.mocked(forebrainApi.saveProviders).mock.calls[0]`）新增，
测试数据用一个两模型的行 `provider({ provider: 'openai', models: ['gpt-5', 'gpt-5-mini'], index: 1 })`，`providersPage` mock 的 `defaultModel` 按用例设置：

1. `lets a multi-model row pick which model is the default`：默认在**别的行**；点该行的「设为默认」打开菜单，选第二个模型 → `saveProviders` 第二个实参为 `'openai/gpt-5-mini'`。
2. `keeps set-default available on the row that holds the default model, to pick another of its models`：默认是 `openai/gpt-5-mini`；该行的「设为默认」仍在，菜单里 `gpt-5-mini` 标为当前，选 `gpt-5` → 实参 `'openai/gpt-5'`。
3. `a single-model row still sets the default in one click`：现有行为的护栏（可直接依赖既有用例，若既有用例已覆盖则不用新增）。
4. `names the default model on the badge`（若原型选了「徽标带模型名」）：徽标文字含默认的模型名；（若选了悬停提示，则断言提示文字）。

`data-testid` 约定：菜单触发沿用 `provider-default-set-${index}`；菜单项用 `provider-default-pick-${index}-${model}`。

**Verify**：`cd frontend && npx vitest run src/views/ProvidersView.test.ts 2>&1 | tail -20` → 新增的 1、2（及 4）**失败**，既有用例仍通过。

### Step 3：实现（1:1 还原已确认的原型）

`ProvidersView.vue`：
1. 把 `isDefaultRow(row)` 保留（徽标用），新增 `defaultModelOf(row): string`——行内等于当前默认的那个模型名，没有则 `''`。
2. `setDefault(row)` 改成 `setDefault(row, model)`：只写 `${provider}/${model}`，`model` 为空就返回。单模型行点按钮时传 `row.models[0]`。
3. 操作列：去掉 `v-if="!isDefaultRow(row)"`，改成「行内存在不是默认的模型」时显示（`row.models.length > 1 || !isDefaultRow(row)`）。多模型行点开菜单，单模型行直接生效。
   菜单用项目里**已有**的弹层/菜单模式（看原型里用的是哪个，以及 `frontend/src/components` 里是否已有同类组件；`ai-elements-vue/packages/shadcn-vue/components/ui/` 下的 shadcn-vue 组件是首选，不要手写原生下拉）。
4. 徽标按原型显示模型名或提示。
5. `locales/index.ts` 中英文同步加需要的键（若有）；不要删 `providers.setDefault`、`providers.defaultBadge`。

**Verify**：
- `cd frontend && npx vitest run src/views/ProvidersView.test.ts` → 全绿（含 Step 2 的新用例与既有用例）。
- `cd frontend && npx vue-tsc --noEmit` → 不新增错误。

### Step 4：收尾

1. `cd frontend && npx vitest run` → exit 0（全量前端单测）。
2. `git status --short` 里本计划新增的改动只落在 In scope 的 4 个文件。

## Test plan

见 Step 2。结构样板：`ProvidersView.test.ts` 的 `sets the default without touching the order`。
**可证伪性**：Step 2 的 1、2 在旧实现下必须失败（旧实现永远取第一个、且默认行没有按钮）。

## Done criteria

- [ ] 原型已被 owner 确认（报告里引用确认的原话或日期）
- [ ] `cd frontend && npx vitest run` exit 0；`npx vue-tsc --noEmit` 不新增错误
- [ ] `grep -n 'models?.\[0\]' frontend/src/views/ProvidersView.vue` 不再出现在 `setDefault` 里（单模型行的传参除外）
- [ ] 既有用例 `names the row on every action; the default row shows no set-default` 与 `sets the default without touching the order` **未被修改**且通过
- [ ] 没有任何后端文件被修改（`git status --short pkg` 里本计划不新增条目）
- [ ] 没有任何 `git commit` / `git push` / 新分支

## STOP conditions

1. 找不到 Drift check 里的符号，或 `isDefaultRow` / `setDefault` 的行为已不同（比如 owner 已经改成了别的交互）。
2. Step 1 之后没有得到 owner 的确认就想继续——不许。
3. 为了实现需要改后端、或需要新的 `default_model` 字段形状——说明假设「后端已支持任意 provider/model」不成立，停下报告。
4. 既有的两个默认相关用例不得不改断言才能通过——说明单模型行的体验被改了，停下报告。
5. 一个步骤的验证在合理修复后仍失败两次。

## Maintenance notes

- 「默认」始终是一个 `provider/model`；表格是按「行」折叠展示的，所以多模型行需要在 UI 上指明是哪一个。以后若模型服务表改成一模型一行，这个计划的菜单就可以去掉。
- `connect` 登录流程把新选的模型追加成已有行的第二个模型（计划 100 之后仍在同一行）——这正是这个缺口常被踩到的路径；若产品想让「刚登录选的模型」自动成为默认，那是另一个决定（`saveConnectModel` 里的注释写明了现在有意不写 `default_model`）。
- 评审重点：菜单项是否只列**这一行**的模型；切换默认后是否仍不改变列表顺序（`setDefault` 的 `change` 回调必须是空操作，只带 `default_model`）。
