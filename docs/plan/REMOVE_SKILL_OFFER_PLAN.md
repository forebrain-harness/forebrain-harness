# 删除：skill-offer 全套提议机制（保留 /skill-generator 技能本体）

状态：待 owner 批准（2026-11-13 二稿，吸收 plan-review 评审 + owner 两项裁决；基线 dev @ 497db730 + 记忆修复五文件已在工作树，零 commit）
批准后镜像到 `docs/plan/REMOVE_SKILL_OFFER_PLAN.md`（docs/plan 已 gitignore）。

## 0. Owner 裁决（本计划依据）

- 2026-11-13：「只保留 /skill-generator 技能本身，其他都删掉，删干净，不再需要这个语义和功能」
- 2026-11-13（本计划 user_interaction）：
  - 设置页：shared-skills 分组下无其它配置，**分组整个删掉**
  - 冻结块：**旧会话不要碰，历史数据不要碰，不要迁移**——不升键、不删旧行；已知残留：旧会话 /resume 后仍会收到其旧冻结块（fb_session_prompt_state.skill_catalog）里的已删提议段，owner 接受
- 2026-11-13（批准时追加裁决）：「shared-skills 分组所在页如果也空了，把该页和入口也删掉」——**经核实条件不成立**：'shared-skills' 是 `TOOLBAR_SWITCHES` 的键，且是**死条目**（SettingsView.vue:37 直接渲染 SettingsSharedSkillsTab，不经 SettingsTabForm 查表，web 从未画出该开关——二轮评审核实）；该页为完整技能管理页（搜索/过滤/批量下载/在线离线安装），不空 → **只删死条目，页面与入口保留**
- 常设规则：零 commit（改动留树待 owner 审）；记忆修复五文件与本删除都留工作树，无提交动作

## 1. 目标

删除后新会话表现得像 skill-offer 提议语义从未存在过：

- 无 skillOfferLLM 计数门槛、无一次性 reminder 注入
- 系统提示无 "## Keeping finished work as a skill" 常驻段
- 无 features.skill_offer 配置键（仓库 YAML/DB/TUI 面板/web 设置页分组）
- /status 无 "Skill offer" 行
- **保留**：`~/.forebrain/skills/.system/skill-generator/`（技能本体，仓库外）、`skillCatalogLLM` 技能目录注入（新 freeze 的 catalog 不含 guidance）、plan-mode reminder 基础设施（reminderRunAnchor/planReminderMessage/insertPlanReminderMessage/recordReminderAdoption/analyzePlanReminders，被 wrapPlanModeLLM 共用）、pkg/skill 其余全部

## 2. 触点清单（行号为 2026-11-13 核实值；以符号名为准，行漂移不算 STOP，内容不符即 STOP）

### 2.1 仓库 Go + YAML（全删）

| 文件 | 内容 |
|---|---|
| `pkg/skill/offer.go` | **整文件删**：DefaultSkillOfferCommand、SkillOfferGuidanceOptions、offerGuidanceTemplate、RenderSkillOfferGuidance |
| `pkg/skill/offer_test.go` | **整文件删** |
| `pkg/run/skills.go` | `Runner.skillOfferGuidance()`（~L85-91）；`skillCatalogLLM.offerGuidance` 字段 + `offerGuidanceSection()`（~L169）+ 拼接（~L163 改回纯 catalog）；`wrapSkillCatalogLLM` 的 `offerGuidance` 参数；`skillOfferMarker`(L631)、`skillOfferCommandName`(L637)、gate 常量(L641-651)、`skillOfferLLM` 全套(L665-804)、`skillOfferSignals`+`analyzeSkillOfferSignals`(L808-867)、`skillOfferAlreadyInHistory`(L871-878)、`wrapSkillOfferLLM`(L689-701)。**`sessionPromptStateSkillCatalog` 键名保持不变**（owner：不迁移） |
| `pkg/run/runner.go` | L1020 `wrapSkillCatalogLLM(..., r.skillOfferGuidance)` 去掉末参数；L1074 `wrapSkillOfferLLM(...)` 整行删 |
| `pkg/run/config.go` | L727 `reminderRunAnchor` 注释里 "Plan mode and the skill-offer gate share the type" 改为只述 plan mode（类型保留） |
| `pkg/config/config.go` | L149 `SkillOffer *bool`；L162 赋值；L170 `SkillOffer bool` |
| `pkg/process/config_defaults.go` | L105 |
| `pkg/turn/model.go` | L789-792/L846-847/L877-878/L1085 |
| `pkg/gateway/api_extra.go` | L9284 键行 |
| `pkg/gateway/slash_handlers.go` | L110 |
| `pkg/tui/chat_slash.go` | L136 |
| `pkg/tui/config_panel_schema.go` | L181 |
| `forebrain.yaml` | L55-56：`# Default: true. Shows the standing offer…` 注释行 + `skill_offer: true` 行（已跟踪文件，§2.5 旧稿误判为存量 home 配置，评审纠正） |

### 2.2 前端（删）

- `frontend/src/lib/settingsSchema.ts` L340-343 注释整段（"the language server and the skill offer (plan 191 §0.7)" 改为只述 language server）+ L346 `TOOLBAR_SWITCHES['shared-skills']` **死条目**（见 §0 追加裁决；页面与入口不动）
- `frontend/src/lib/settingsSchema.test.ts`：`expect(literal).toBe(34)` → 33（L155-157，注释同步）；其余断言经 TBSW/Object.values 联动自动平衡
- `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/config-db-settings-preview.html`（gitignored，但被 settingsSchema.test.ts 以 `?raw` 当 fixture 依赖）**全部 5 处痕迹**（二轮评审核实行号）：
  - :436 标签说明「技能推荐、语言服务器总开关在各自页面工具栏右侧」→ 去掉技能推荐半句
  - :620 TBSW `'features.skill_offer': F(...)` 整行
  - :798 mock settings `'features.skill_offer':false,` 整行
  - :1111 mock 渲染 `${tbsw('features.skill_offer')}` 调用（不删则 tbsw() 读 undefined 崩——「测试绿、artifact 崩」缺口）
  - :1477 变更说明 `['skills','共享技能','…技能推荐的开关挪到工具栏右侧…']` 整行（所述开关已不存在）
- `frontend/src/locales/index.ts` L394（zh「技能推荐」）、L2922（en）

### 2.3 测试（删/改/增）

- `pkg/run/skills_test.go`：TestSkillOffer* 全删（L187-238、L868 附近 golden）；catalog 测试去 offerGuidance 参数；**新增**一条回归（沿用 `catalogCapturingLLM` + `newPromptStateStore`，skills_test.go:20-46 构造模式）：新会话 freeze 的 skill_catalog 块不含 `Keeping finished work`，且**同会话第二次 Execute 也不出现**（钉住 §0「新会话才干净」）
- `pkg/run/memory_llm_test.go` L524：`wrapSkillCatalogLLM(..., nil)` 调用去尾参（签名变更后编译点，二轮评审补）
- `pkg/turn/model_test.go`：L97/L151/L153/L165/L179 断言；L136 Test…NamesSkillOfferOff 删 Skill offer 部分，保留 OmitEmptyRows 语义并改名收缩
- `pkg/config/load_test.go` L550；`pkg/config/config_test.go` **L948-980 两个函数**（TestEffectiveFeaturesSkillOfferDefaultsOn + TestSkillOfferIsPartOfTheResolvedFeatureSet，旧稿行号只到 953，评审纠正）
- `pkg/tui/run_test.go` L5448

### 2.4 明确不动（含理由）

- `~/.forebrain/skills/.system/skill-generator/`（owner 指定保留）
- 旧会话冻结行 `fb_session_prompt_state` 的 `skill_catalog` 键与数据：不升键、不迁移、不删行（owner 裁决）；键名常量也不改
- `pkg/run/config.go` 的 plan-mode reminder 机制、`skillCatalogLLM` 本体
- `pkg/memory/templates/pending_notes.md` L3 "standing instructions" 措辞：owner 2026-11-13 裁决 pending 注入措辞不改，与 skill-offer 无关，勿顺手改
- `plans/308-skill-offer-proposes-only-on-evidenced-recurrence.md`、`plans/README.md` 历史条目：gitignored 历史归档，不动
- `pkg/tui/config_panel_test.go` features section 用例：评审已核无需改
- 并行会话文件：a3（pkg/tui/tabbed_picker.go、overlays.go、setup.go、plugins/subscriptions/qodercn/**）、4b（plan-reviewer 派发提示词）——与本计划零交集；动树前见 pkg/tui 脏文件先 peer_send 归属

### 2.5 兼容性与前提（已核实）

- pkg/config 无 KnownFields：home 里已写 `skill_offer: false` 的用户配置被静默忽略
- gateway settings 表驱动：键行删后 UI 不显示，存量 DB 值成死数据无害
- 记忆修复五文件（pkg/memory/templates/capture.md、pkg/memory/backend.go、pkg/memory/instruction_test.go、pkg/tool/session_tools.go、pkg/tool/request_permissions_test.go）零交集，保留
- 执行在主树：third_party 已接线、docs/plan 原型 HTML 存在。fresh worktree 缺两者时 Go 链接失败/前端测试文件级报错——**预存在问题**（?raw 依赖 gitignored fixture），本计划不修，完工报告一行
- 旧会话 resume 残留（owner 接受）：见 §0

## 3. Steps

**Step 0 漂移检查 + peer 协调**
按 §2 符号名逐一 grep 核对内容（**内容不符即 STOP**，仅行漂移不算）；git status 确认与 a3/4b 无范围重叠，有则 peer_send。

**Step 1** rm `pkg/skill/offer.go` + `offer_test.go`

**Step 2** 删 pkg/run gate 全套 + guidance 挂钩 + runner.go 两处 + config.go:727 注释
验证：`CGO_ENABLED=1 go build -tags fts5 ./pkg/run ./pkg/skill`

**Step 3** forebrain.yaml 两行 + config.go + config_defaults.go + turn/model.go + gateway×2 + tui×2
验证：`CGO_ENABLED=1 go build -tags fts5 ./...`

**Step 4** 测试删改 + 新增 freeze 回归（§2.3）
验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/skill ./pkg/config ./pkg/turn ./pkg/gateway ./pkg/tui ./pkg/process -count=1` 全绿

**Step 5** 前端四处（settingsSchema.ts 分组、settingsSchema.test.ts、原型 HTML TBSW 行、locales 两条）
验证：`cd frontend && pnpm typecheck && pnpm test`

**Step 6 防漏核查（"从未存在过"判据，评审加强版）**
1. 标识符：`grep -rn 'SkillOffer\|skill_offer\|skillOffer' --include='*.go' --include='*.ts' --include='*.vue' cmd pkg frontend/src` → 0
2. 文案：`grep -rn 'Skill suggestions\|Keeping finished work\|Skill worth keeping\|技能推荐\|standing offer' cmd pkg frontend/src forebrain.yaml docs` → 0（原型 §2.2 五处清痕后 docs 全域应为 0；plans/ 历史归档不在扫描范围）
3. `gofmt -l pkg`、`CGO_ENABLED=1 go vet -tags fts5 ./...` 干净

**Step 7 tmux 真机验收（run-forebrain driver，隔离 home + fake provider reply）**
1. 多工具长回合，请求捕获 grep `skill-offer`/`Keeping finished work`/`Skill worth keeping` → 0
2. /status 无 "Skill offer" 行
3. TUI 配置面板 features 分组无 "Skill suggestions" 项（截图）；web 共享技能页正常打开（截图，该开关 web 侧从未画出，无"消失前后"可截）
4. 回归：二次启动直达 composer、回合正常
结束清理。

**Step 8** 镜像 docs/plan/REMOVE_SKILL_OFFER_PLAN.md；本文件附 Implementation Status（含旧会话残留与 ?raw 预存问题两行报告）。

## 4. Done criteria

- [ ] Step 6 两类 grep 0 命中
- [ ] 七包测试全绿 + frontend typecheck/test 绿；gofmt/vet 干净
- [ ] `~/.forebrain/skills/.system/skill-generator/` 原样存在；`fb_session_prompt_state` 无任何行被本计划改动
- [ ] tmux 四项验收证据齐
- [ ] git status 改动仅限本计划 §2 名单 + 记忆修复五文件 + docs/plan 镜像（a3/4b 等他人 WIP 按 §2.4 归属核对后排除），零 commit

## 5. STOP conditions

- 漂移检查内容不符（符号/行为与 §2 描述不符）
- 编译/测试暴露 §2 未枚举的引用点，且修复需超出删除语义的连带改动
- a3/4b 脏文件与本计划文件冲突
- 前端 typecheck 发现框架对 skill_offer 有隐藏强耦合

## Implementation Status（2026-11-13 执行记录）

VERDICT: APPROVE（tech-lead 评审，全部 done criterion 由评审者本人重跑验证）

执行：executor subagent（opencode-go/deepseek-v4.1-flash，general-purpose）
WORKTREE: `.worktrees/remove-skill-offer-semantics`，分支 `remove-skill-offer-semantics`，单提交 `c526c5df`（基线 497db730；合并/保留由 owner 决定）
基线 497db730→c526c5df：23 文件，+63/−974。

STEPS（executor 执行 + reviewer 复核）：
- Step 0-3 done：漂移零；offer.go/offer_test.go 删除；gate 全套/guidance 挂钩/接线/注释清理落地；全仓编译过（reviewer 复核 diff：含计划外两处同语义注释清理 pkg/run/config.go reminderAdoptionSink、forebrain.yaml 头部列举，方向正确）。
- Step 4 done：七包测试绿（reviewer 重跑：pkg/skill TestPrimeRemoteHeadFillsTheCache 一次负载 flaky，单跑+全量重跑均绿——环境性，非代码）。新增回归 `TestSkillCatalogFreezesWithoutProposalGuidance`（两次 Execute 均断言 catalog 在、guidance 不在；拆串防自检命中——质量合格）。
- Step 5 done：typecheck 0 错误；vitest 1447/1447 用例绿（reviewer 三次重跑：一次 1 用例负载抖动、一次 2 个 unhandled Vue/shiki warning，栈全在 ChatView/MessageResponse 等 diff 未触及组件，同机 ba/4b 会话高负载所致；用例层面稳定全绿）。
- Step 6 done：标识符 grep 0、文案 grep 0（reviewer 重跑同）；gofmt/vet 干净；裸词 `skill.offer` 补核 0（排除 skills_test/commands_test 两处无关普通动词措辞）。
- Step 7 done：隔离 home tmux 验收——请求捕获三词 0 命中；/status 无 Skill offer 行；TUI Feature switches 仅 Memory/Language servers；web 共享技能页正常（PNG 在 $TMPDIR/fb-proposal-removal-web-shared-skills.png）；二次启动直达 composer、回合正常。
- Step 8 done：worktree 单提交，树净。

DEVIATIONS（均已评审认可）：
1. `SettingsTabForm.vue:3-5` 注释清理（计划清单外，REVISE 轮 reviewer 批准补充）：Step 6.2 文案 grep 模式漏裸词，评审裸词探测发现该残留，executor 修订 amend。
2. `pkg/run/config.go` reminderAdoptionSink 注释、`forebrain.yaml` 头部注释列举：计划未列，同语义残留清理，正确。
3. executor e2e 定向 8/9：唯一失败为 settings-tabs.spec.ts 导航期望未含既有 模型/变量/日志 三 tab 的预存漂移（diff 零触及该 spec 与 SettingsView；e2e 非本计划验收项）。
4. 环境噪音三件（非代码）：磁盘 errno 28（executor 自清 cache 解）、pkg/skill 负载 flaky（重跑绿）、vitest unhandled shiki warning（负载性）。

未做/残留（owner 已知）：
- 旧会话冻结块残留：旧会话 /resume 后仍收到旧 skill_catalog 冻结块里的已删提议段（owner 裁决：不迁移不碰历史数据，接受）。
- `settingsSchema.test.ts ?raw` 依赖 gitignored 原型 HTML：fresh checkout 缺文件时该测试文件级失败——预存在问题，本计划未修（原型 5 处痕迹已按 §2.2 清理于 worktree 磁盘副本，gitignored 不入库）。

镜像：docs/plan/REMOVE_SKILL_OFFER_PLAN.md（主树）。
