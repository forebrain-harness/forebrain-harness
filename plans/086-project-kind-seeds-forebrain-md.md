# 086 — 新建项目的「项目类型」：一次性种子，只写项目根的 FOREBRAIN.md

## Status

- **REJECTED（owner 撤回，2026-10-20）**：曾于同日执行完毕并评审通过，随后 owner 裁决
  「创建项目时默认自动生成 FOREBRAIN.md 文件的语义和代码都删掉，不需要了」，整份实现已从工作区删除
  （`pkg/gateway/project_seed.go` 与测试、e2e spec、`fb_projects.kind` 列与 v16 迁移、创建弹窗下拉、
  概览只读行、六个文案键、相关单测），schema 回到 v15（实测新库 `user_version = 15`、`fb_projects` 无
  `kind` 列）。**`kind` 一并删除**：它存在的唯一用途就是挑这份种子模板，留下的下拉与列都是空转语义。
  本文件保留作记录，**不要**按它再实施一遍。
- 复审：2026-10-20 冷读复核（基线 `cd11539`）。校准了 3 处行号，并补齐两处会漏改的点：
  `projectColumnCount`（`:241`）与 `projectPayload` 的实际位置（`:6059-6068`）。
- 优先级 **P2**，工作量 **M**，风险 **MED**（改 schema + 写用户的项目目录）
- 依据：owner 2026-10-20 已审批的原型
  `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`（新建项目弹窗的「项目类型」下拉，
  选项 办公创作 / 创意编程；原型说明那一行：「它决定这个项目从哪一套默认开始（提示词与工具、审批画像），
  所以只在这里问一次，紧挨着项目名」）。
- owner 2026-10-20 对本计划的裁决（原话）：
  **「一次性种子，只写项目指令文件，是项目根目录下的 FOREBRAIN.md 文件」**。
  即：**不**改审批画像、**不**改工具集、**不**写 `.forebrain/` 任何文件，只按类型写一份项目指令模板。

---

## 为什么

一个项目在创建时**已经是**一套默认（项目指令、记忆作用域、资源访问），但用户第一次建项目时并不知道
该往指令文件里写什么，于是大量项目从空白开始。「项目类型」就是在这一个时刻给一句起点：
选「办公创作」还是「创意编程」，新项目的 `FOREBRAIN.md` 有相应的骨架。

它只发生一次，之后项目自己说了算：文件是用户可以随手改的普通 Markdown，
`pkg/assembly/rules.go:69-89`（`ProjectForebrainFiles`）每轮读它，改了就生效。

**为什么不叫 AGENTS.md**：本产品的项目指令文件名一直是 `FOREBRAIN.md`
（`pkg/assembly/rules.go:89`、`pkg/turn/executor.go:475` 的 `/init` 都写它）。

---

## 现状事实（行号基于 `cd11539`）

### 项目这一行没有类型

- `pkg/state/schema.sql:8-26`：`CREATE TABLE fb_projects (id, agent_id, name, icon, description,
  instructions, root, project_key, memory_scope, resource_access, pinned, last_attach_dir, created_at, updated_at)`
  （`last_attach_dir` 在 `:23`，`created_at` 之前）。
- `pkg/state/project_store.go:33-50` `Project` 结构体：同样没有类型字段。
- `pkg/state/project_store.go:239` `projectColumns` 是插入用的列清单，`:241` 有配套的
  `projectColumnCount = 14`（目前无引用，但改了列数就要跟着改，别留下一个说谎的常量）；
  `:302` 与 `:368` 两处 `INSERT INTO fb_projects(...)` 都用它。
- 读取侧只有**一个** `Scan` 点：`scanProject`（`:243`），被 `Get`（`:397`）、`GetByRoot`（`:411`）、
  `List`（`:689`）三处 `SELECT `+projectColumns 共用——加列只改这一个函数。
- `pkg/state/project_store.go:257-266` `CreateProjectInput`。
- 迁移：`pkg/state/schema_migrations.go:41-57` 的 `stateSchemaMigrations` 表，最后一步是
  `{version: 15, apply: migrateStateV14ToV15}`（`:56`）；`stateSchemaVersion = 15`（`:21`）。
  `migrateStateV11ToV12`（`:562-582`）是「两个 ADD COLUMN，先查 `pragma_table_info` 再 ALTER」的现成范本。

### 创建项目的入口

- `pkg/gateway/api_extra.go:6199-6263` `POST /api/v1/projects`：解 `projectPayload` →
  校验 root 与 name → `store.Create(CreateProjectInput{...})` → `s.projectRow(p)` 返回；
  `ErrProjectRootTaken` 走 409。`projectPayload` 定义在 `:6059-6068`。
- 响应里已经有同类先例：`trust_recorded` / `trust_error`（`:6250-6259`）——本计划的
  `instructions_seeded` / `instructions_error` 照这个形状加，别另造一种返回结构。
- 另一条：`:1349` `store.FindOrCreateByRoot(...)`（选文件夹时顺带建项目），这条**不带类型**，本计划不改它。
- 前端：`frontend/src/views/ProjectsView.vue:118-142` 新建弹窗（目录 → 名称 → 提示 → 取消/创建），
  `:203-206` `createOpen / draft{name, root}`，`:227-233` `openCreate()`。
- API 包装：`frontend/src/lib/api.ts:3171-3182` `projectCreate(body)`（`toSnakeCase`）。

### 项目指令文件的读取方

- `pkg/assembly/rules.go:69-89`：从项目根（git root，或 cwd 自己）读 `FOREBRAIN.md`。
- `pkg/turn/executor.go:475-514`：`/init` 写/刷新同一个文件，并且它是一段**给模型的提示**，
  不是本计划要的模板（本计划直接写文件，不发起一轮对话）。

---

## 设计

### 类型的取值

- 存两枚**稳定标识**：`office`（办公创作）、`coding`（创意编程）。显示名在 locales。
- 空串 = 没有类型（老项目、以及 `FindOrCreateByRoot` 建出来的项目）。
- Go 侧白名单收尾（owner 规则：覆盖取值用白名单）：
  `var projectKinds = map[string]struct{}{"office": {}, "coding": {}}`，
  不认识的值 → 400，**不要**「悄悄存空串」。

### 种子规则（一次，且只在不覆盖的前提下）

1. 只在**创建**时发生（`POST /api/v1/projects` 且 `kind` 非空）。
2. 目标文件：`<root>/FOREBRAIN.md`。
3. 文件已存在 → **什么都不写**，响应里 `instructions_seeded: false`（用户自己的文件永远不被覆盖）；
   文件不存在 → 写入该类型的模板，`instructions_seeded: true`。
4. 写失败（权限、只读目录）：创建**仍然成功**（项目行已经插入），响应里带
   `instructions_error: "<原因>"`，HTTP 仍是 200。理由：建项目不该因为一个可选文件失败。
5. 不建目录：`root` 由 `state.ValidateProjectRoot` 校验过存在。

### 模板内容（逐字使用，不要改写）

`pkg/gateway/project_seed.go`，两个常量。要求：短、高信号、不点名任何模型或厂商
（仓库规则：面向模型的文本必须 provider-neutral）。中文，因为产品的默认界面语言是中文。

`projectSeedOffice`：

```markdown
# FOREBRAIN.md

本项目的指令。每次对话都会读它，所以保持简短、具体。

## 这个项目在做什么

（一句话说清，例如：季度经营分析的材料与表格）

## 约定

- 交付物放在项目根目录，文件名用中文描述内容，不加日期前缀。
- 改已有文档前先读全文，只改需要改的段落。
- 长文与表格先给结构（标题层级、列名），再填内容。
- 数字与结论以项目里现有的材料为准，缺材料就先问，不要凭印象补。

## 不要

- 不要把草稿命名成 `final_v3` 这类名字；草稿和成稿分目录放。
- 不要在文档里写未经确认的数据。
```

`projectSeedCoding`：

```markdown
# FOREBRAIN.md

本项目的指令。每次对话都会读它，所以保持简短、具体。

## 这个项目在做什么

（一句话说清，例如：一个用 Go 写的本地命令行工具）

## 约定

- 改代码前先读相关文件与测试；能复用已有函数就不要新写一个。
- 每处改动都要能跑：交付时给出实际执行的构建与测试命令和结果。
- 提交信息的格式沿用仓库既有习惯；不要顺手格式化无关文件。

## 不要

- 不要为了让测试变绿改断言。
- 不要在源码里留调试输出或临时代码。
```

### 为什么模板放 `pkg/gateway` 而不是新包

新建一个包要动 `pkg/architecture/testdata/graph.json`（CI 跑 `scripts/package-graph.sh`），
而这两个模板唯一的写入者就是 `POST /api/v1/projects`。放在 `pkg/gateway/project_seed.go`
（小写、不导出）是当前最小改动。维护提示里写清：若将来 TUI 也要问类型，再把模板提到一个两个表面
都能引用的包里，并重生成 `graph.json`。

---

## Steps

### Step 1 — schema 与迁移

1. `pkg/state/schema.sql` 的 `fb_projects` 加一列（放在 `last_attach_dir` 之后）：

   ```sql
   -- 项目类型：只在创建时问一次，决定 FOREBRAIN.md 的种子模板。
   -- '' 表示没有类型（老项目、以及按目录自动建出来的项目）。
   kind TEXT NOT NULL DEFAULT '',
   ```

2. `pkg/state/schema_migrations.go`：
   - `stateSchemaVersion` 15 → **16**；
   - `stateSchemaMigrations` 末尾加 `{version: 16, apply: migrateStateV15ToV16}`；
   - 新增（照 `migrateStateV11ToV12` 的形状，先查 `pragma_table_info` 再 ALTER）：

     ```go
     func migrateStateV15ToV16(ctx context.Context, conn *sql.Conn) error {
         var col int
         if err := conn.QueryRowContext(ctx,
             `SELECT COUNT(*) FROM pragma_table_info('fb_projects') WHERE name='kind'`).Scan(&col); err != nil {
             return err
         }
         if col == 0 {
             if _, err := conn.ExecContext(ctx,
                 `ALTER TABLE fb_projects ADD COLUMN kind TEXT NOT NULL DEFAULT ''`); err != nil {
                 return fmt.Errorf("add fb_projects.kind: %w", err)
             }
         }
         return nil
     }
     ```

3. 测试（`pkg/state` 既有迁移测试的写法：`schema_migrations_test.go` 的
   `TestStateV11UpgradesToV12AddsProjectFileColumns`（`:497`）用「当前 schema 建库 → 手工
   `DROP COLUMN` + `PRAGMA user_version = 11` → 重开 → 断言」，照它做 v15→v16）：
   新库直接是 v16 且有 `kind`；v15 库迁移后行数不变、`kind` 全为 `''`；重复迁移不报错。

验证：

```bash
gofmt -l pkg/state
go vet -tags fts5 ./pkg/state
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'Migrat|Schema' -count=1
```

### Step 2 — 存储层带类型

`pkg/state/project_store.go`：

1. `Project` 加 `Kind string`（放在 `MemoryScope` 之后）。
2. `projectColumns`（`:239`）加 `kind`；`:241` 的 `projectColumnCount` 14 → 15；
   `:302` 与 `:368` 两处 INSERT 的参数列表与占位符同步（**两处都要改**，`FindOrCreateByRoot` 写空串）。
3. `scanProject`（`:243`）按列序补一个 `&p.Kind`——`Get` / `GetByRoot` / `List` 三处 `SELECT` 都走它，
   漏改会在跑测试时直接 `Scan` 报错。
4. `CreateProjectInput` 加 `Kind string`；`Create` 用它。
5. `Update` 只改可编辑字段（既有测试 `TestProjectUpdateEditableFieldsOnly`）：**不要**把 `kind` 加进去，
   类型只在创建时定。

测试：`pkg/state/project_store_test.go` 加一条——建 `kind: "coding"` 的项目，读回来是 `coding`；
不带 kind 的是 `''`。

验证：

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run Project -count=1
```

### Step 3 — gateway：收类型、校验白名单、写种子

`pkg/gateway/api_extra.go`（`POST /api/v1/projects`，`:6199-6263`；`projectPayload` 在 `:6059-6068`）：

1. `projectPayload`（`:6059-6068`）加 `Kind string \`json:"kind"\``。
2. 白名单校验：

   ```go
   kind := strings.ToLower(strings.TrimSpace(body.Kind))
   if kind != "" {
       if _, ok := projectKinds[kind]; !ok {
           http.Error(w, "unknown project kind: "+kind, http.StatusBadRequest)
           return
       }
   }
   ```

   `projectKinds` 放在新文件 `pkg/gateway/project_seed.go` 里（连同两个模板）。
3. `store.Create(state.CreateProjectInput{…, Kind: kind})`。
4. 插入成功后写种子（**在** `s.projectRow(p)` 之前，好把结果带进响应）：

   ```go
   seeded, seedErr := seedProjectInstructions(root, kind)
   ```

   传**校验后的 `root`**（`state.ValidateProjectRoot` 的返回值，`:6214`），不要传 `body.Root`
   ——后者可能是相对路径，写下去的位置和项目行指向的位置会不一致。

5. 响应加两个字段（`row` 是 `map[string]any`）：
   `row["instructions_seeded"] = seeded`；`seedErr != nil` 时 `row["instructions_error"] = seedErr.Error()`。
   **不要**因为写种子失败改 HTTP 状态。

`pkg/gateway/project_seed.go` 新增：

```go
var projectKinds = map[string]struct{}{"office": {}, "coding": {}}

// seedProjectInstructions writes the type's starter FOREBRAIN.md into a brand
// new project. It never overwrites: a file the user already has is the user's.
// An unknown or empty kind seeds nothing.
func seedProjectInstructions(root, kind string) (bool, error) {
    template, ok := projectSeedTemplates[strings.ToLower(strings.TrimSpace(kind))]
    if !ok || strings.TrimSpace(root) == "" {
        return false, nil
    }
    path := filepath.Join(root, "FOREBRAIN.md")
    if _, err := os.Stat(path); err == nil {
        return false, nil
    } else if !os.IsNotExist(err) {
        return false, err
    }
    if err := os.WriteFile(path, []byte(template), 0o644); err != nil {
        return false, err
    }
    return true, nil
}
```

测试 `pkg/gateway/project_seed_test.go`：
空 kind 不写；未知 kind 不写；office 写了且内容含 `## 约定`；已存在同名文件时**不覆盖**（写前先放
一个哨兵内容，断言读回来还是哨兵）；只读目录（`chmod 0500`）返回 error 且不 panic。

验证：

```bash
gofmt -l pkg/gateway && go vet -tags fts5 ./pkg/gateway
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'ProjectSeed|CreateProject|Projects' -count=1
```

### Step 4 — 前端：下拉、类型、展示

1. `frontend/src/lib/api.ts`：
   - `projectCreate`（`:3171-3182`）body 加 `kind?: string`；**返回类型也要加两个可选字段**，
     否则 TS 读不到种子结果：

     ```ts
     return postJson<
       ProjectRecord & { instructionsSeeded?: boolean; instructionsError?: string },
       Record<string, unknown>
     >('/v1/projects', toSnakeCase(body))
     ```
   - `ProjectRecord`（`:782`）加 `kind?: string`；
   - `projectUpdate`（`:3188-3194`）的 body 类型**不加** `kind`（类型不可改）。
2. `frontend/src/views/ProjectsView.vue:118-142` 新建弹窗：在**名称**那一行之后、`taken` 提示之前
   插入下拉（紧挨项目名，与原型一致）：

   ```html
   <label class="block">
     <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('projects.kindLabel') }}</span>
     <select v-model="draft.kind" class="forebrain-field h-9 w-full" data-testid="project-kind">
       <option value="">{{ t('projects.kindNone') }}</option>
       <option value="office">{{ t('projects.kindOffice') }}</option>
       <option value="coding">{{ t('projects.kindCoding') }}</option>
     </select>
   </label>
   ```

   `draft` 加 `kind: ''`，`openCreate()`（`:227-233`）重置为 `''`，`create()` 把 `kind` 传给
   `projectCreate`。
3. `frontend/src/components/project/ProjectOverview.vue`：在表单里显示类型，**只读**
   （`t('projects.kindLabel')` + 本地化名字；没有类型就不显示这一行）。
4. locales（zh / en，各一份）：
   `projects.kindLabel` 项目类型 / Project type、
   `projects.kindNone` 不指定 / Not specified、
   `projects.kindOffice` 办公创作 / Office & writing、
   `projects.kindCoding` 创意编程 / Creative coding。
5. 创建成功后的提示：响应里 `instructions_seeded` / `instructions_error` 到前端会变成
   **`instructionsSeeded` / `instructionsError`**（`api.ts:1821-1823` 的响应拦截器统一
   `toCamelCase`，别按 snake_case 去读）。若 `instructionsSeeded` 为真，在项目列表上方给一行
   `projects.kindSeeded`（zh `已按「{kind}」写好 FOREBRAIN.md，随时可以改` / en
   `Wrote a starter FOREBRAIN.md for “{kind}”; edit it any time`），
   若带 `instructionsError`，给一行 `projects.kindSeedFailed`（zh `FOREBRAIN.md 没能写入：{error}`）。
   **只在这两种情况下出现**，不要常驻。

测试：`ProjectsView.test.ts`（已存在）加三条——下拉三项、创建请求体带 `kind`、`kind` 为空时
请求体不带该键；`ProjectOverview.test.ts`（已存在）一条（有类型显示、无类型不显示）。

验证：

```bash
cd frontend && corepack pnpm exec vue-tsc --noEmit
cd frontend && corepack pnpm test src/views/ProjectsView.test.ts src/components/project/ProjectOverview.test.ts
```

（`ProjectsView.test.ts` 已存在，直接加用例；不要再新建第二个。）

### Step 5 — e2e

新增 `frontend/e2e/project-kind-seed.spec.ts`（用 e2e 的隔离 home 与临时项目目录）：

1. 新建项目，类型选「创意编程」→ 项目里出现 `FOREBRAIN.md`，内容含 `## 约定`；
2. 同一 root 再建一次（换个名字）→ 被 409 拦下（既有行为），且原文件内容不变；
3. 预先在 root 里放一个带哨兵内容的 `FOREBRAIN.md`，再建项目选「办公创作」→ 文件仍是哨兵内容。

验证：

```bash
FOREBRAIN_E2E_SPEC=e2e/project-kind-seed.spec.ts scripts/acceptance/web_e2e.sh
```

---

## Done criteria

```bash
gofmt -l pkg/state pkg/gateway                          # 无输出
go vet -tags fts5 ./pkg/state ./pkg/gateway              # 无输出
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/gateway -count=1   # ok
cd frontend && corepack pnpm exec vue-tsc --noEmit       # 退出 0
cd frontend && corepack pnpm test                        # 全绿
FOREBRAIN_E2E_SPEC=e2e/project-kind-seed.spec.ts scripts/acceptance/web_e2e.sh   # web e2e: PASS
```

人工核对：

- `PRAGMA user_version` 在新库上是 16；用一份 v15 的拷贝库跑一次启动，`kind` 列出现且老项目都是 `''`。
- 拿一个已有 `FOREBRAIN.md` 的真实项目目录试建项目 → 文件**逐字节未变**（贴 `git status` 或 `md5` 前后值）。

---

## 不在本计划范围

- 审批画像与工具集：owner 明确「只写项目指令文件」。**不要**顺手写 `.forebrain/safety.json`。
- 项目类型的编辑（类型只在创建时定，且只影响那一次种子）。
- `FindOrCreateByRoot`（按文件夹自动建项目）不问类型、不写种子。
- 项目指令文件的编辑器（已有，见 `ProjectRules.vue`）。
- 「按类型换一套默认子代理/技能」这类扩展：没有 owner 裁决，不要做。

---

## 维护提示

- `kind` 只影响**创建那一次**写不写模板。任何「按类型改变运行时行为」的后续需求都要重新裁决，
  不要偷偷把它变成运行时约束（那会与「项目指令可编辑」打架）。
- 模板一旦发布就不要再改（老项目的文件已经写下去了，改模板只影响新项目；要改就当成一次产品变更，
  在 CHANGELOG/PR 里说清）。
- 新增类型：加进 `projectKinds` + `projectSeedTemplates` + 两个 locale 键，三处都要；
  少了 `projectKinds` 会被 400 拦，少了模板会静默不写。
- 若将来 TUI 也要问类型：把模板从 `pkg/gateway` 提到一个两个表面都能引用的包，
  并重跑 `scripts/package-graph.sh` 更新 `pkg/architecture/testdata/graph.json`。

## 逃生口

1. `projectPayload` 不在 `pkg/gateway/api_extra.go`（字段名或位置不同）→ 停，报告实际定义再改。
2. `projectColumns` 之外还有一处手写列清单的 `SELECT`/`INSERT`（`grep -rn "fb_projects" pkg/` 找）
   → 一起改；漏了它会在真机上 `Scan` 失败。
3. 迁移测试用的 fixture 机制与本计划描述不同（例如没有「当前 schema + 降低 user_version」的构造器）
   → 停，报告实际机制；**不要**为了让测试跑起来改 `stateSchemaVersion` 的语义。
4. 只读目录下创建项目的行为与预期不同（例如 `ValidateProjectRoot` 就拒绝了）→ 停，报告；
   这种目录本来就建不了项目，不要为此加兜底写入逻辑。
