# 计划 011：技能工坊——内置 skill-workshop 的网页界面（任务会话、技能文件编辑、评测文件、发布与下载）

> **执行者须知**：逐步执行，每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"中的任何情况，立即停止并报告。
> 完成后更新 README 中本计划的状态行。**不要提交代码。**
>
> **漂移检查（先运行）**：
> `git diff --stat b92a1b1..HEAD -- pkg/state/schema.sql pkg/state/schema_migrations.go pkg/gateway/server.go pkg/gateway/api_extra.go frontend/src/components/ChatDrawer.vue frontend/src/views/SkillsView.vue`
> 计划 007-010 都改过 `api_extra.go`；010 改过 `SkillsView.vue` 与下载端点。那是预期的。

## 状态

- **优先级**：P2
- **工作量**：XL
- **风险**：MED-HIGH（涉及状态库 schema 迁移与 WS 提交协议扩展；评测执行交给模型跑技能自带脚本，网关不加重型管线）
- **依赖**：计划 007（`/workshop` 菜单位、`useTenantScope`）、计划 010（技能文件端点模式、zip 下载端点、创建/编辑端点保留的约定）
- **类别**：feature
- **基线**：提交 `b92a1b1`，2026-09-29

## 为什么要做

owner 要求（第 35 条）："设计技能工坊，skill-workshop"。预览 captions 的 `workshop` 条（397 行）是页面合同：左边是工坊对话，模型先问清触发场景、所需权限、完成标准；右边是技能本身——文件、测试用例、评测（使用技能 vs 不用技能，逐条反馈）、触发描述；完成后选择发布到本主代理、某个项目或共享技能库，也可以下载 zip；**工坊任务不出现在对话抽屉里**。

工坊的领域逻辑已由内置技能 `pkg/skill/system_assets/skill-workshop/`（SKILL.md 455 行 + `scripts/run_eval.py`、`run_loop.py`、`package_skill.py` 等 + `references/schemas.md` 的 `evals.json`/`timing.json`/`grading.json`/`benchmark.json` 结构）完整承载，TUI 入口在 `pkg/tui/chat_slash.go:1573`（`workshopHandoff :2048` 通过提交负载的 `SkillName`/`SkillPath` 激活技能）。本计划做的是把这套流程搬到网页：**对话驱动保持不变，Web 补齐面板与文件操作**，不为评测新写 Go 管线。

## 现状

- **会话标记缺口**：`pkg/state/schema.sql:29-47` `fb_sessions` 的 `origin` 只有 `native/migrated` 两值（CHECK 约束），没有"会话用途"标记。对话抽屉列 `GET /api/chat/sessions` 的全部会话——工坊会话若不加标记就会出现在抽屉里，违反预览合同。加列必须走 `pkg/state/schema_migrations.go` 的既有迁移机制（owner 铁律：带版本记录、每步事务、失败整体回滚）。
- **WS 提交协议缺口**：`pkg/gateway/server.go:614` `wsClientMessage{Choice, Content, Role, Attachments, MentionImages}`——没有技能激活字段。网关侧技能激活已存在：提交上下文 `sc.SkillName`/`sc.SkillPath`（`:1449-1450`）目前只由 slash 解析器填充，最终进 `run.WithExplicitSkillSelection`（`:1684`）。Web 端要激活 skill-workshop，需要给 `wsClientMessage` 加可选 `skill_name`/`skill_path` 并接入同一提交路径。
- **技能文件在引擎侧的读写**：`handleSkillsCreate`/`handleSkillsUpdate`（`api_extra.go:2248`/`:2283`，010 保留）创建/更新技能；但没有"按技能名列出/读取/保存技能目录内任意文件"的端点——工坊右侧文件面板需要它。技能目录可能位于共享库/主代理/项目技能根（`pkg/skill/roots.go:62` 优先级），越界防护用 `pkg/home/pathguard.go:51` `ResolveWithinRoots`（同文件 `:36` `ValidateArchiveRelPath`、`:23` `ContainsTraversal`；`hub.go:281` 有使用先例）。
- **zip 下载**：010 已交付 `GET /api/skills/{name}/download`——工坊的"下载 zip"直接复用，不新建。
- **评测**：`run_eval.py` 由模型经 shell 工具执行（技能自己的流程），产物是技能目录下 `evals/` 的 JSON 文件；Web 只做文件查看/编辑，不做执行器。
- **前端**：无 `/workshop` 路由与页面；对话流基础设施（`useChatStream.ts`）可复用；`SkillsView.vue` 在 010 后有"在工坊中改进"的挂点需求（010 留了禁用态提示文字）。
- README 全局规则（90-115 行）全部适用。

## 设计

### 1. 会话来源标记（schema 迁移 + API）

- `fb_sessions` 加列 `source TEXT NOT NULL DEFAULT ''`（空 = 普通会话；`workshop` = 工坊任务）。迁移必须同时改两处，缺一不可（只改 schema.sql 则旧库永不补列；只加迁移不改 schema.sql 则新建库缺列——新建库按 schema.sql 建表后直接标记为 `stateSchemaVersion`，见 `schema_migrations.go:13-16` 的注释）：
  1. `pkg/state/schema.sql:29` 的 `CREATE TABLE fb_sessions` 里加上该列（新建库走这条路径）；
  2. `pkg/state/schema_migrations.go:16` 把 `const stateSchemaVersion = 3` 改成 `4`，并在 `stateSchemaMigrations`（`:36-40`）追加 `{version: 4, apply: migrateStateV3ToV4}`；`migrateStateV3ToV4` 内只执行 `ALTER TABLE fb_sessions ADD COLUMN source TEXT NOT NULL DEFAULT ''`（SQLite 允许带非 NULL 默认值的 ADD COLUMN）。迁移机制已按"每步一个事务、失败整体回滚、`PRAGMA user_version` 记录版本"实现，照 `migrateStateV2ToV3`（`:333`）的写法即可。
- **结构参照测试**：`pkg/state/schema_migrations_test.go:95` `TestStateMigrationMatchesFreshSchema`（迁移后的库与全新库逐列比对）——新增的 v4 迁移必须被它覆盖，若它按版本白名单枚举迁移，记得把 v4 加进去；`:489` `TestStateOpenIsIdempotentAtCurrentVersion` 也是必须仍通过的既有用例。
- `POST /api/chat/sessions`（`api_extra.go:779`）请求体加可选 `source string`：仅接受 `""` 与 `"workshop"`，其他值 400。
- `GET /api/chat/sessions` 响应条目带 `source`。
- 对话抽屉（005 `ChatDrawer.vue`）过滤 `source === 'workshop'` 的行——抽屉不显示工坊任务；工坊页自己的任务列表显示它们。

### 2. WS 提交负载扩展（技能激活）

`wsClientMessage` 加 `SkillName string \`json:"skill_name,omitempty"\``、`SkillPath string \`json:"skill_path,omitempty"\``；处理路径把它们并入现有 `sc.SkillName`/`sc.SkillPath` 的取值处（slash 解析结果为空时采用消息字段），校验：`skill_path` 必须解析到当前有效技能集合内的一个已存在目录（复用技能发现，防任意路径注入），`skill_name` 与之一致。TUI 不受影响（字段缺省为零值）。

### 3. 技能文件端点（工坊专用）

| 端点 | 语义 |
| --- | --- |
| `GET /api/skills/{name}/files` | 列技能目录内文件树（相对路径、大小、mtime；跳过 symlink；上限 2,000 项） |
| `GET /api/skills/{name}/file?path=<rel>` | 读文本文件（≤ 1 MiB），`path` 过 `ResolveWithinRoots` |
| `PUT /api/skills/{name}/file?path=<rel>` | 保存文本（≤ 1 MiB），同样越界校验；只允许写常规文件 |

`{name}` 定位规则：当前租户有效集合内按名查找，取未遮蔽条目；找不到 404。内置技能（`.system`）只读（PUT 403）——内置技能随版本更新会被覆盖，编辑它没有意义，工坊改进内置技能的路径是"复制到目标层再改"（SKILL.md 流程已如此引导）。

### 4. 工坊页（`/workshop`，菜单一级项 `hammer`）

- **任务列表**（左侧栏上部或下拉）：`source='workshop'` 的会话（标题、更新时间），点击载入该会话的对话流；"新任务"两个入口：
  - **从零创建**：弹层填技能名、一句话用途、发布目标（本主代理/共享技能库/某项目——目标层决定初始提示里的落点）；创建工坊会话后发送首条消息（文本 = 模板化的创建请求 + `skill_name: skill-workshop` + `skill_path`）。
  - **改进现有技能**：技能选择下拉（`GET /api/skills/` 里 `editable` 的行 + 内置行——内置走"复制后改"），确认后同样以 `skill_name/skill_path` 激活工坊技能发首条消息。
  - 首条消息模板由前端拼装（含用户填的参数），内容中立、不含模型/厂商名（全局规则 6）。
- **对话面板**（左主区）：复用现有对话流组件与 `useChatStream`（WS、审批卡、工具卡全部照常）；仅会话来源不同。
- **技能面板**（右栏，随会话中激活的技能上下文显示）：
  - 文件树（`GET /api/skills/{name}/files`）+ 文件编辑器（`GET/PUT .../file`）——编辑器与 009 的 `RulesFileEditor` 同一交互（textarea + 保存按钮）；
  - 触发描述卡：显示 `SKILL.md` front matter 的 `description`，保存 = PUT 该文件（工坊对话里模型改的描述刷新显示）；
  - 测试用例卡：`evals/evals.json` 的结构化查看/编辑（按 `references/schemas.md` 的字段：id/text/... 以文件实际 schema 为准，编辑器提供 JSON 文本模式为主，结构化表格为辅——若 schemas.md 字段复杂则只做 JSON 文本模式，不做半吊子表格）；
  - 评测结果卡：列 `evals/` 目录下的运行产物文件（`timing.json`/`grading.json`/`benchmark.json`/`feedback.json`），点击以只读 JSON 查看；**执行**由用户在对话里让模型跑 `run_eval.py`（页面提供"让模型运行评测"的快捷消息按钮，等价于发送一条预设文本）。
- **发布与下载**：新技能任务在创建时已定目标层（模型按提示落目录）；面板提供"下载 zip"（010 端点）。"移动到其他层"不做——需要时用户在技能页删除后在工坊重建，避免引入移动语义的边界问题（多层同名、遮蔽重算）。工坊页顶部放"主代理"作用域徽标。

### 5. SkillsView 挂点

010 留下的"创建和编辑技能在技能工坊"提示改为可点击：跳 `/workshop` 并带上"改进现有技能"预选该技能（query 参数）。

## 需要的命令

| 用途 | 命令 | 成功标志 |
| --- | --- | --- |
| Go 静态检查 | `go vet ./...` | 退出码 0 |
| Go 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... ./pkg/gateway/... -count=1` | 全部 `ok` |
| 前端单测 | `cd frontend && corepack pnpm test` | 全部通过 |
| 真机验证 | `scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS` |
| 缓存不变式 | `git diff --stat -- pkg/agent pkg/assembly pkg/llm pkg/turn/prompt* internal/agentrun` | 无输出 |
| 包依赖图 | `scripts/package-graph.sh && git diff --exit-code pkg/architecture/testdata/graph.json` | 无 diff |

## 范围

**只允许修改**：
- `pkg/state/schema.sql`（加列）、`pkg/state/schema_migrations.go`（迁移步骤）、`pkg/state/*_test.go`（迁移测试）、会话 store 的读列/写列
- `pkg/gateway/server.go`（`wsClientMessage` 两字段 + 提交路径并入）、`pkg/gateway/skills_workshop.go`（新建：文件三端点）、`pkg/gateway/api_extra.go`（会话 create/list 的 source 字段 + 路由注册行）
- `frontend/src/views/WorkshopView.vue`（新建）、`frontend/src/components/workshop/*`（新建：任务列表、技能面板、文件编辑器、评测卡）、`frontend/src/components/ChatDrawer.vue`（过滤一行）、`frontend/src/views/SkillsView.vue`（挂点）
- `frontend/src/router/index.ts`、`frontend/src/App.vue`（菜单加 `workshop` 项）、`frontend/src/locales/index.ts`、`frontend/src/lib/api.ts`、`frontend/src/composables/useChatStream.ts`（发送负载加两字段）
- `frontend/e2e/skill-workshop.spec.ts`（新建）

**不要碰**：
- `pkg/tui/**`；`pkg/skill/system_assets/**`（技能文本不改）。
- 评测执行器：不写 Go 版 run_eval；模型经 shell 跑脚本是唯一执行路径。
- `pkg/gateway/dist/**`。

## 步骤

### 第 1 步：schema 迁移与会话 source

实现设计第 1 条后端：改 `pkg/state/schema.sql`、`pkg/state/schema_migrations.go`（新增 `migrateStateV3ToV4`）与会话 store 的读列/写列。测试：用 `pkg/state/schema_migrations_test.go:95` `TestStateMigrationMatchesFreshSchema` 的构造方式造一个 v3 旧库（无 `source` 列、含既有会话行），打开后自动补列、原有数据完好；v3 库与全新库的 `sqlite_master` 逐列一致；迁移失败注入后整体回滚（照 `TestStateMigrationRepairsPrematureV1Version` 的注入方式）。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... -count=1` 全部 `ok`。

### 第 2 步：WS 字段与文件端点

实现设计第 2、3 条。测试：`skill_path` 指向有效技能集合内目录被接受、指向 `/tmp` 被拒；文件读取越界（`../../x`）400；内置技能 PUT 403；创建会话 source 非法值 400。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... -count=1` 全部 `ok`；`go vet ./...` 退出码 0。

### 第 3 步：工坊页前端

实现设计第 4、5 条与抽屉过滤。

**验证**：`cd frontend && corepack pnpm test` 全部通过；`grep -n "workshop" frontend/src/router/index.ts` 有输出。

### 第 4 步：真机验证

新建 `frontend/e2e/skill-workshop.spec.ts`。**真实模型门控与凭据**：需要真实 LLM 回合的用例（2、3、5）以 `process.env.E2E_REAL_LLM === '1'` 门控，未设置时 `test.skip` 并注明原因——默认假模型档下 `web_e2e.sh` 仍全绿。真实档由 001 脚手架提供：`set -a; . ~/.forebrain/e2e-zhipu.env; set +a; FOREBRAIN_E2E_REAL_LLM=1 scripts/acceptance/web_e2e.sh`，此时隔离 home 的 `forebrain.yaml` 写入智谱真实配置（`provider: zhipuai`、`model: glm-5.3-flash`、`base_url: https://open.bigmodel.cn/api/coding/paas/v4`、`api_key: ${FOREBRAIN_E2E_ZHIPU_KEY}`）。密钥只存在于 owner 私有的 `~/.forebrain/e2e-zhipu.env`（`chmod 600`，不进仓库、不写进任何仓库文件与文档）；yaml 走 `${ENV}` 引用是 config 守卫的强制要求。用例：
1. 菜单出现"技能工坊"；进入后任务列表为空态。
2. 新任务（从零创建）：填名字（如 `e2e-demo-skill`）、用途、目标层=本主代理；会话创建且**不出现在对话抽屉**（抽屉行数断言）；对话面板出现模型回复（真实 LLM 回合，断言收到 assistant 消息事件）。
3. 技能面板：任务进行到模型创建 `SKILL.md` 后（脚本在对话中追加一条"请直接创建技能文件"的引导消息；断言 `GET /api/skills/e2e-demo-skill/files` 返回含 SKILL.md），文件树出现该文件；编辑器打开 SKILL.md、改触发描述保存、重新拉取一致。
4. 下载 zip：面板下载按钮返回 zip 且含 SKILL.md。
5. 改进现有技能：从 `/skills` 行点"在工坊中改进"进入，会话首条消息带 `skill_name`（服务端 audit/日志断言技能激活生效）。
6. 截图：`workshop-empty.png`、`workshop-conversation.png`、`workshop-files.png`。

**验证**：`scripts/acceptance/web_e2e.sh` → 最后一行 `web e2e: PASS`。

## 完成标准（全部满足）

- [x] 第 1-3 步的验证全部通过
- [x] `scripts/acceptance/web_e2e.sh` 最后一行 `web e2e: PASS`（73/73，2.9 分钟）
- [x] 工坊会话不出现在对话抽屉（e2e 断言）；对话抽屉普通行为不受影响（既有 layout 用例仍过）
- [x] 评测无 Go 执行器：`git grep -n "run_eval" pkg/gateway pkg/run` 无 Go 侧调用
- [x] 迁移测试通过且生产旧库升级路径有测试覆盖（TestStateV3UpgradesToV4WithSessionsIntact 造真 v3 旧库验证补列+数据完好）
- [x] `git status --short pkg/gateway/dist` 无输出；缓存不变式无输出；包依赖图按全局规则 9 再生成（gateway fan-out 保持 18 上限内，graph.json 的 LOC 变化随工作区提交）
- [x] README 状态行已更新

## STOP 条件

- "现状"摘录与代码对不上（尤其 `wsClientMessage` 结构、提交路径里 `sc.SkillName` 的消费点、`fb_sessions` 列集）。
- schema 迁移与既有迁移链冲突（版本号占用、幂等假设破裂）。
- 发现对话流复用会因会话来源产生分支逻辑蔓延（`useChatStream` 需要大幅改造）——先报告改造面，owner 拍板是否缩小本期工坊范围（例如先只做"创建任务 + 文件面板"，评测卡后置）。
- 真机验证无法拿到真实 provider 凭据。
- 需要修改"范围"之外的文件。

## 维护说明

- `source` 列的合法值集合：目前 `''|workshop`；新增会话用途时先扩 CHECK 语义的校验点（gateway 入口），再考虑约束变更——保持白名单收口在 API 层。
- 技能文件端点是工坊专用面：不允许被用于通用文件管理（越界校验 + 技能集合定位是安全边界）。
- 工坊的领域流程以 SKILL.md 为准；Web 只提供面板与文件通道，SKILL.md 升级带来的流程变化不应需要改 Web 代码（除文件 schema 展示）。
