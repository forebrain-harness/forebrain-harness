# Web 端重设计与 gateway 访问修复：实施计划索引

由 improve skill 于 2026-09-29 生成，基线提交 `b92a1b1`。**状态：待 owner 审批，未批准前不得改代码。**

## owner 的原始要求（2026-09-29，逐字）

1. "frontend web的主题色、UI布局和背景色，以及forebrain harness品牌logo都要重新设计一下。整体色调太暗太深沉。背景色要白色，只在菜单栏体现深色主题和浅色主题即可。"
2. "我用forebrain gateway start命令启动后，在web端操作，报错Request failed with status code 401"，日志为
   `msg="gateway control plane auth failed" path=/api/providers auth=""`（`/api/hooks`、`/api/memories/settings` 同样）。
3. "forebrain gateway start命令启动后必须输出banner和全部路由信息以及listen on端口信息，现在什么都没有。"
4. "主题色和品牌logo分别给我三套方案，让我选择"
5. "我对web UI的需求是简洁、大气、必须用纯色，禁止出现渐变色、官方、正式"
6. "对话的layout必须重新设计，现在分了四栏，改成三栏布局，把第2栏（新聊天）删掉，放进左侧菜单栏，只保留左侧菜单栏、对话窗口、工作台"
7. "实施后必须自动化真机验证效果"
8. "心跳不要放进工作台，心跳不是常用功能，入口放在左侧菜单栏即可"
9. "live agents为空时不要在工作台显示live agents卡片"
10. "主题色：A海军蓝 / Logo：1方印"
11. "是否可以加上主题色切换功能？支持切换A海军蓝、B松石青和C石墨黑"
12. "登录页没问题。工作区文件改成目录树，默认展示第一层的目录和文件，点击目录节点展开，懒加载"
13. "工作台必须可以收起可以打开，默认收起"；"默认情况下，只有左侧菜单栏和内容区"
14. "设置和配置必须统一为一个入口"
15. "主要页面必须给到预览页让我确认"；"左侧菜单栏的每一个菜单项和打开的内容页都必须发给我预览链接做确认"
16. "把主代理的增删改查移到/加到设置的tab页里，切换入口只保留左侧菜单栏上方的下拉选择器，删掉其他切换按钮和操作入口"
17. "左侧菜单栏改一下：恢复对话菜单项，点击对话菜单，向右弹出对话抽屉，把新聊天、搜索会话和会话列表等功能和操作入口都移到对话抽屉里，管控和工作区里的子菜单项都打平改成一级菜单，删掉管控和工作区菜单项"
18. "权限页的审批模式只对用户暴露tui侧/permissions里三种模式：Read Only、Default、Full Access，并要加上说明，方便用户理解和选择，可选值确定的必须改成选择框，能选的就不要让用户输入"
19. "技能必须从设置页移到左侧一级菜单"
20. "主代理就是租户，左侧菜单项对应的页面里的所有配置和数据都必须是当前选中的主代理相关的也就是主代理维度的配置和数据，全局性的或者没有与特定主代理绑定的配置和数据都放进设置页"
21. "针对项目维度的所有配置和数据，必须设计单独的项目空间，支持用户在该项目空间内对配置进行读写操作。主代理维度和项目维度的配置和数据不能混在一起，必须边界清晰"；"应该包括规则指令、记忆、权限、MCP和技能等"
22. "项目空间里只展示项目自己的技能，不展示从主代理和全局共享的技能"（此前"展示全部可加载技能""继承项只读"两条已被这一条取代）
23. "项目空间里只展示项目维度的配置和数据，不展示继承的和共享的"（适用于项目空间的所有标签页）
24. "左侧菜单栏必须可以折叠，只显示菜单图标，默认打开"
25. "左侧菜单栏折叠后，鼠标移到菜单图标上方必须显示菜单项名称，方便用户知道这个是什么菜单"
26. "对话窗口消息区的已工作 1 分 12 秒后面要加上完成时间，与tui保持一致"
27. "☑3/3的checkbox图标再大一些"；"web可以用iconfont"；"tui不改，只改web"（网页端改用 lucide 勾选方框图标，终端的 ☑ 字符不动）；随后"跟3/3字体大小保持一致即可"——图标尺寸为 1em，与数字同字号；"checkbox图标跟 3/3没有垂直居中对齐"——改为按数字的视觉中心对齐（`vertical-align: -0.14em`）
28. "主代理下拉框背景色没有了，成了透明的，选项文字无法看清"（根因：下拉渲染在菜单栏元素之外，而菜单栏配色变量只定义在菜单栏元素上；改为定义在外壳根元素上，见计划 003、005）
29. 技能页："删掉创建或编辑私有技能，'安装到本主代理'改成'在线安装'，提示需访问公网，内网环境请使用离线安装，新增离线安装功能，支持上传zip、rar、tar.gz等压缩包安装，技能列表加下载按钮，下载zip压缩包，支持单条下载和多选批量下载"；设置页共享技能、主代理技能、项目技能三处都按此修改
30. "项目空间里展示的技能列表必须包括该项目实际可以加载到的所有技能……主代理维度的技能列表也必须包括全局共享的技能"（取代第 22 条；继承行完全只读，决策 D9）
31. "定时任务页面的计划的自定义选项没有设计完，用户如何自定义没有设计出来"
32. "模型服务里的api key要支持用户明文输入，输入一个字符马上隐藏，采用输入密码的效果，模型输入框必须支持按英文逗号分隔输入多个模型id"
33. "主代理维度和项目维度的记忆页面必须展示记忆文件分页列表，支持分页查询、按创建或更新时间升降序、记忆内容关键词搜索、单条查看和编辑、单条删除和多选批量删除以及重置/清空全部"
34. "左侧菜单栏加上主代理的规则指令和对应页面，UI与项目空间的规则指令保持一致，并且主代理和项目空间的规则指令页的规则文件都必须支持创建和编辑，只能创建forebrain harness支持的规则文件，显示下拉框让用户选择要创建的规则文件"
35. "设计技能工坊，skill-workshop"

## 方案预览

三套主题色、三套 logo 和新三栏布局的可交互示意：[`brand-options.html`](brand-options.html)
（本地用浏览器直接打开；同一页面也发布在 https://claude.ai/artifact/8ao7nKTbdW5SwXRzy3dNSf ）。
登录页（计划 001）的可交互预览：[`login-preview.html`](login-preview.html)（同一页面也发布在 https://claude.ai/artifact/G9oHFK7Ez1ySxMFAvvWLv5 ），**owner 已确认**。
全站预览（菜单栏每一项及其内容页、工作台开合、目录树、合并后的设置页）：[`app-preview.html`](app-preview.html)
（同一页面也发布在 https://claude.ai/artifact/TfrVeR7jaH6a9DdmarnZSA ，Version 12），**owner 已确认（2026-09-29"预览通过，请继续"）**；确认后的调整同时改预览与计划。

## 需要 owner 拍板的决策

| 编号 | 问题 | 选项 | 推荐 | 结论 |
| --- | --- | --- | --- | --- |
| D1 | 主题色 | A 海军蓝 / B 松石青 / C 石墨黑 | A | **三套都做、可在设置页切换，默认 A 海军蓝**（owner 2026-09-29："主题色：A海军蓝"，随后"支持切换A海军蓝、B松石青和C石墨黑"） |
| D2 | 品牌 logo | 1 方印 / 2 前脑 / 3 枢纽 | 1 | **1 方印**（owner 2026-09-29："Logo：1方印"） |
| D3 | `gateway start` 是否打印带 token 的一键登录链接 | 仅当 stdout 是终端时打印 / 总是打印 / 从不打印 | 仅终端 | **仅当 stdout 是终端时打印**（owner 2026-09-29） |
| D4 | 界面字体 | 系统无衬线 / 保留现有衬线 | 无衬线 | **系统无衬线**（owner 2026-09-29） |
| D5 | 审批模式放哪里 | 设置里设全局默认 + 对话输入框旁按会话切换 / 新增主代理级默认 / 只在设置设全局默认 | 前者 | **设置默认 + 会话切换**（核实：终端 `/permissions` 只作用于当前会话，默认值来自全局 `forebrain.yaml`） |
| D6 | 项目会话列在哪里 | 只在项目空间 / 抽屉里按项目分组 | 只在项目空间 | **只在项目空间** |
| D7 | MCP 与钩子（配置是全局的） | 移到设置 / 改成主代理级 | 移到设置 | **移到设置**；项目 MCP 在项目空间 |
| D8 | 技能页划分 | 主代理技能在菜单栏 + 共享库在设置 / 全放菜单栏 | 前者 | **主代理技能在菜单栏，共享库在设置，项目技能在项目空间** |
| D9 | 继承来的技能行能否在下一层启停 | 完全只读，每个技能只在归属层启停 / 允许在本层停用继承技能 | 完全只读 | **完全只读**：共享技能在"设置 → 共享技能"启停（新增全局启停状态），主代理技能在主代理技能页，项目技能在项目空间 |
| D10 | 记忆清空/重置的端点形态（2026-09-29 计划复核时拍板） | 扩展既有 `POST /api/memories/reset` 为唯一入口（body 显式 scope）/ 新增独立 `POST /api/memories/clear` | 统一收口 | **统一收口为 `POST /api/memories/reset` 一个端点**：body `{"scope": "session\|all\|global\|project", "project_id"?}`，`?scope=` 查询参数删除（owner 2026-09-29："POST /api/memories/clear 与既有的 POST /api/memories/reset?scope=统一收口为一个接口"）；实现见计划 012 设计第 2 节 |

十项决策均已由 owner 拍板。其余设计取舍已在各计划里给出默认做法，owner 审批时可逐条否决。

## 执行顺序与状态

| 计划 | 标题 | 优先级 | 工作量 | 依赖 | 状态 |
| --- | --- | --- | --- | --- | --- |
| [001](001-gateway-web-login.md) | 浏览器通过一次性登录获得会话凭据，消除 401；建立自动化真机验证脚手架 | P0 | L | — | **DONE**（2026-09-29：`POST /api/auth/session` 换取 HttpOnly Cookie；HTTP/WS 统一 `authorizeControlPlane`、删除 `?token=`；登录页+路由守卫；`web_e2e.sh` 7/7 全绿 18s。附带根因修复：`gateway start` 子 shell 改 `exec` 否则 trap 杀不掉真进程、孤儿 gateway 毒化后续 run；App.vue `<transition out-in>` 与 bare 分支/异步路由组件交互导致冷加载视图空挂载，已删除该装饰性过渡） |
| [002](002-gateway-startup-banner.md) | `gateway start` 输出 banner、完整路由表和监听地址；删除无人读取的 gateway 配置项 | P1 | M | 001 | **DONE**（2026-09-29：banner+104 路由+渠道路由+Web UI/Auth/Listening；`Run` 返回错误不再 panic；登录链接仅 TTY 打印；18 个死配置字段+`cors_origins` 全删；文档站同步。e2e 8/8 PASS） |
| [003](003-web-visual-system.md) | 白底纯色视觉体系：三套可切换主题色（默认海军蓝），深浅只作用于菜单栏，删除渐变与毛玻璃，统一字体 | P1 | L | 001 | **DONE**（2026-09-30：内容区恒白、6 块 rail 变量定义在外壳根、`data-brand` 三套切换、渐变/毛玻璃/装饰阴影清零、全部硬编码色收敛为变量、系统无衬线、`.dark` 删除、外观卡进设置页。e2e 10/10 PASS，visual 截图 32 张） |
| [004](004-brand-mark.md) | "方印"品牌 logo 落到 favicon、菜单栏、登录页 | P2 | S | 003 | **DONE**（2026-09-30：BrandMark 组件 + favicon.svg/文档站 mark.svg 固定海军蓝正色、深色菜单栏反白、删除旧鲸标。e2e 11/11 PASS） |
| [005](005-chat-three-column-layout.md) | 对话页重排：会话列表并入菜单栏，工作台可收起且默认收起，心跳入口移到菜单栏，空的运行中代理卡片不显示 | P1 | L | 003 | **DONE**（2026-09-30：对话抽屉/一级菜单打平/折叠 rail+自绘 tooltip/心跳浮层+共享心跳状态/工作台组件化默认收起/空 live-agents 隐藏/结束行与终端逐段一致（pkg/event.PlanProgressOf 共享引擎 + plan_done/total/active 贯穿事件、历史与 UI，SquareCheck -0.14em 对齐）/ChatSidebar 删除。**根因修复**：send 发送会话从地址栏取值 + sessionId watch 流式期间不回拨 URL + loadMessages 旧快照 epoch 丢弃——"新聊天后立即发送"竞态清除。附带环境修复：vite dev ws 代理 changeOrigin。e2e 19/19 PASS） |
| [006](006-workspace-tree-lazy.md) | 工作台的"工作区文件"改为懒加载目录树（后端按目录列一层，修复片段接口的符号链接越界） | P1 | M | 005 | **DONE**（2026-09-30：`GET /api/workspace/tree?path=` 单层列出、目录优先排序、.git 隐藏、越界/失效符号链接不列出且 `?path=` 拒绝；片段接口改用 `tool.ResolveWithinRoots`（修复符号链接越界与 `..` 前缀误拒）；全局缓存与递归遍历删除；前端 useWorkspaceTree 模块单例 + WorkspaceTree/WorkspaceRow 组件（懒加载、单次请求、roving tabindex 键盘、空态/错误/重试、data-path 精确定位）；ChatWorkbench 接线、ChatView 的 loadWorkspaceTree 移除；轮次结束 refresh。e2e 25/25 PASS，截图 workspace-tree.png） |
| [007](007-tenant-shell-and-settings.md) | 租户化应用外壳：最终一级菜单、设置页合并为标签页（外观/主代理/MCP/钩子/记忆开关/配置文件/运行状态）、主代理增删改查（ID 不可变，删除拒绝 main 与当前主代理）、删除 /agents /mcp /hooks /config 旧路由、子代理页、切换主代理的前端范围重置 | P1 | L | 003、005 | **DONE**（2026-09-30：CRUD 后端 POST/PUT/DELETE /api/agents/primary（写 persistedConfig→saveAndReload，创建置 Primary:true，工作区根按 ID 约定派生并校验调用方传入值）；Summary/AgentDefinition 增 name/description；设置页 8 标签组件化（外观默认、主代理、MCP、钩子、记忆开关、配置文件、运行状态、技能临时）；权限记忆区删除；旧四路由四视图全删；子代理页（空态/跳会话/停止/全部停止+5s 轮询）；useTenantScope 重置总线（会话列表已接）；scope 徽标。e2e 31/31 PASS，shell-menu/shell-subagents 截图） |
| [008](008-project-space.md) | 独立项目空间：项目列表重排、空间壳（黑底横条 + 面包屑 + 八个标签的嵌套路由）、概览与项目设置、项目会话、项目 MCP；除技能标签外只展示项目自己的数据 | P1 | L | 007 | **DONE**（2026-09-30：`/projects/:id/(overview|rules|sessions|memory|perm|mcp|skills|cron)` 嵌套路由 + 壳（深色横条/面包屑/根路径展示），五未交付标签占位；列表页网格化 + 置顶/归档筛选 + 主代理 scope 徽标；overview=概览+项目设置（含 memory_scope 单选、resource_access、危险区）+persist；sessions=仅本项目会话 + 新建即跳转；mcp=信任门（PATCH trust）+ preview + 空态；useTenantScope 增 activeProjectId。零后端改动。e2e 37/37 PASS，projects-list/project-mcp 截图） |
| [009](009-rules-and-permissions.md) | 规则指令与权限：主代理 AGENTS/SOUL/USER 与项目 FOREBRAIN.md 链的新建/编辑（白名单下拉）、权限规则页收口（选择框化）、审批三档预设（设置全局默认 + 会话级切换，TUI 不改） | P1 | L | 007、008 | **DONE**（2026-09-30：`/api/rules/agent(/:name)` + `/api/rules/project/:id(/file?dir=)`（白名单/一级目录校验/64KiB 上限/预算 warning）；`GET/PUT /api/permissions/approval-default`（MatchApprovalPreset 语义持久化到 forebrain.yaml）；`POST /api/permissions/session-preset`（镜像 TUI ApplyPermissionPreset，内存态不落盘）；`/rules` 页 + RulesFileEditor 公共组件 + 项目 rules 标签替换占位；权限页动作/工具下拉化 + localSettings 添加/删除；项目 perm 标签（信任门 + projectSettings 规则）；设置"审批默认"标签三卡；composer 旁会话级三档下拉。e2e 43/43 PASS，rules-agent 截图） |
| [010](010-skill-lifecycle.md) | 技能生命周期：三页有效集合视图（继承行只读，D9）、在线安装文案、离线压缩包安装（zip/tar.gz/tar，防 zip-slip/炸弹/symlink）、zip 单条与批量下载、归属层启停与删除、移除创建/编辑入口 | P1 | L | 007、008 | **DONE**（2026-10-01：`pkg/skill/offline.go` 离线安装（扩展名+魔数分派、64/64/256MiB/20000 预算、pathguard 校验、symlink 拒绝、`.incoming-` 同层临时目录、同名整体失败、多技能包与根级 SKILL.md 布局）+ 安全测试；`pkg/gateway/skills_download.go`（origin/editable/download_url/shadows 装饰、单条与批量 zip 下载（X-Skipped-Symlinks）、归属层删除（builtin 403）、multipart 上传 415/409/400）；toggle/delete/upload 响应统一 decorated 列表；三页 UI（/skills 主代理页、设置 shared-skills 标签、项目空间 skills 标签）+ SkillsTable/InstallDialogs 共用组件，删旧 SkillsTempTab/SkillsInstalledList/SkillInstallProgressPanel 与 24 个死文案键。顺带根因修复 3 个既有缺陷：home.ValidateArchiveRelPath 把 normalize 后路径传 ContainsTraversal 致 `../..` 互消漏检穿越（改传 raw）；GET /v1/projects/:id 不回传 trusted（补 trusted）；项目空间把登记根交给向上找 .git 的 safety.Resolve（skill 包 ProjectSkillRootsForDir/TrustedProjectSkillRoots 同病）——新增 safety.ResolveRegisteredProject/Context 确切根语义替换 3 处调用，skill 包两根函数改把传入根当确切边界（launch 输入已是 git 根，零行为变化）。e2e 51/51 PASS（新增 skills-lifecycle 8 用例 + zip fixture）） |
| [011](011-skill-workshop.md) | 技能工坊：内置 skill-workshop 的网页界面——工坊会话（不出现在对话抽屉）、WS 技能激活、技能文件编辑、测试用例与评测产物查看、发布与 zip 下载；评测执行由模型跑技能自带脚本，不新增 Go 管线 | P2 | XL | 007、008、010 | **DONE**（2026-10-02：schema v3→v4 迁移（fb_sessions.source 列，幂等防 fixture 重复建列；SetSessionSource + 会话 API source 白名单 workshop + 列表回传；ChatDrawer/useChatSessions 过滤 workshop 行）；wsClientMessage 加 skill_name/skill_path（服务端对照有效技能集校验，canonical 路径比较）；skills_workshop 文件三端点（files/file 读写、内置 .system 只读 403、pathguard 防线）；WorkshopView（任务列表=source=workshop 会话、新任务弹层从零/改进、复用 useChatStream 对话流、首条消息带技能激活）+ WorkshopSkillPanel（文件树/编辑器/SKILL.md 描述/下载复用 010 端点）；/skills 页工坊链接挂点；e2e 5 用例（真 LLM 用例 E2E_REAL_LLM 门控）。**顺带根因修复一批**：①面板 open() GET 返回覆盖用户输入的竞态（loadingFile 禁用编辑器，同 012 类）+ savedDraft 死引用；②ListChildSessionsRecent SELECT/Scan 列数不匹配（migrate 测试抓出）；③**架构测试五类违规全清**（001-009 命令清单未含 pkg/architecture 而积累）：gateway fan-out 超上限（pathguard 经 pkg/tool 既有转发接入，新增 tool.ValidateArchiveRelPath 一行转发）、gateway 生产文件 31>20（11 个文件按域合并：web_session→controlplane、banner→serve_run、plan_progress_lookup→wsevents、skills_workshop→skills_download、cron_preview/rules_api/workspace_tree 等测试→api_extra 等纯拼接）、文件名下划线>2（memtimes_darwin/other 等）、孤儿测试文件对应缺失（11 处并入对应测试）、import 分组/重复导入（goimports+手工去重 config 双别名）；graph.json 按全局规则 9 再生成。全量 Go 测试绿 + e2e 73/73） |
| [012](012-memory-files.md) | 记忆文件管理：主代理 global 与项目 scope 的分页列表（创建/更新时间升降序）、关键词搜索、查看编辑、单删批删、清空全部；核心文件 MEMORY.md / memory_summary.md 不可删；清空/重置统一走扩展后的 `POST /api/memories/reset`（body 显式 scope，决策 D10） | P1 | L | 007、008 | **DONE**（2026-10-02：`pkg/gateway/memories_files_api.go`（files 列表分页/排序/关键词、file 读写 2MiB 上限+pathguard 防线+新建 .md/.txt/.json 白名单、批量删除核心文件 403 逐条结果）+ darwin/other birth time 平台文件；`handleMemoriesReset` 改 body 形态（空 body=session 原语义，新增 global/project 分支，删 `?scope=` 查询参数，响应带 scope）；MemoryFilesPanel 公共面板 + MemoriesView 重写（设置开关已在设置页，删重复卡）+ ProjectMemory 替换占位（无 projectKey 空态）；e2e memory-files 7 用例（E2E_HOME 导出）。设计偏差一处：列表搜索用字面内容/文件名匹配而非 FTS——引擎 MinIndexRelevance 是为真实规模库校准的召回阈值，小库用户关键词会被整批滤空，字面匹配才是文件管理的正确语义（已在计划维护说明补充）） |
| [013](013-cron-schedule-builder.md) | 定时任务计划编辑器：七种自定义模式（一次/相对/间隔/每天/工作日/每周多选/Cron）、服务端预览 API（ParseSchedule+Next，引擎零改动）、主代理与项目任务分离、投递通道下拉 | P1 | M | 007、008 | **DONE**（2026-10-02：`pkg/gateway/cron_preview.go`（POST /api/cron/preview：引擎 ParseSchedule+Next×3，valid:false 走 200+error，raw 原样回显）；handleCronJobs GET 支持 `?project_id=`（归属校验 404 + 内存分层过滤——CronService 未暴露项目列表方法且范围禁改 pkg/process，过滤等价）；ScheduleBuilder 七模式（周多选>1 生成 cron dow 列表、=1 用 every <day> 文法、12 小时制 clock、防抖 400ms 服务端预览）；CronView 重写（编辑器弹层/启用暂停/立即执行/执行记录/投递通道下拉=已启用通道+只记录选项/次数上限，projectId+scope props 使项目标签直接复用同一视图）；ProjectCron 替换占位；e2e cron-builder 6 用例。引擎与存储零改动已验证） |
| [014](014-provider-keys-and-models.md) | 模型服务：API Key 密码框输入（明文转 `${ENV}` 引用写 `.env`，空 = 保留旧值，永不明文回显）、多模型逗号标签输入、模型目录快速加入、服务排序 | P1 | M | 007 | **DONE**（2026-10-02：handleProviders 重写为 DTO 往返（GET 折叠相邻同签名条目为多模型行 + api_key_set/hint（引用经 .env 解析末 4 位，短 key/未解析引用全遮）；PUT 展开models 为每模型一条（yaml 无 model 列表序列化，展开即引擎持久形态）、api_key_plain→${ENV}、空=旧值回填、显式 api_key 引用透传，删 [REDACTED] 往返）；ModelChipsInput（逗号即时切 chip/失焦/Enter/去重/删除 + 目录建议）+ 组件 7 测试；ProvidersView 重写（主用/回退徽标、上下移、keyEdit 更换交互、password+autocomplete=new-password、高级折叠、datalist 目录）。**owner 拍板**：pkg/process 加导出包装 ProviderAPIKeyConfigReference（照 SecretConfigReferenceForOnboard 先例）。**顺带根因修复**：ProviderKeyEnvMap 对连字符 provider 生成非法 env 名（E2E-SVC_API_KEY）致 .env 整文件解析失败、热载崩——`-`→`_` 清洗（与 ChannelSecretEnvName 同法）+ 回归测试。e2e providers-keys 5 用例（yaml/${ENV}/0600/轮换/排序）） |

状态取值：TODO | IN PROGRESS | DONE | BLOCKED（附一句原因）。

## 依赖说明

- 001 必须最先做：它消除 401，并建立 `scripts/acceptance/web_e2e.sh` 真机验证脚手架；后面每个计划都往这套脚手架里追加用例，并以它通过作为完成标准。
- 002 依赖 001：路由表里要出现 001 新增的 `POST /api/auth/session`，一键登录链接指向 001 新增的 `/login` 页面。
- 003 先于 004、005：004 的 logo 和 005 的新菜单栏都直接使用 003 定义的色值变量；在旧变量上先做布局再换色会返工。
- 006 依赖 005：目录树放在 005 新建的 `ChatWorkbench.vue` 里，且只在工作台打开时加载。
- 007 是 008–014 的地基：最终菜单、设置页标签结构、主代理 CRUD 与租户切换重置都在 007。
- 008 依赖 007：项目空间壳挂进 007 的菜单与路由；009/010/012/013 分别向项目空间的 rules/skills/memory/cron 标签填充内容（各自替换 008 留下的占位组件），010 另占项目空间的 skills 标签——它是项目空间里唯一显示继承数据的标签（owner 第 30 条）。
- 009 依赖 007（设置页标签结构、`/rules` 菜单位）与 008（项目空间 rules/perm 标签挂点）。
- 010 依赖 007（`/skills` 菜单位、设置页"共享技能"标签挂点）与 008（项目空间 skills 标签挂点）。
- 011 依赖 010（zip 下载端点、技能文件端点模式、"创建/编辑端点保留给工坊"的约定）与 007/008。
- 012 依赖 007（`/memories` 菜单位）与 008（项目空间 memory 标签挂点）。
- 013 依赖 007（`/cron` 菜单位）与 008（项目空间 cron 标签挂点）。
- 014 依赖 007（`/providers` 菜单位）。
- 并行性：009、010、012、013、014 在 008 之后可并行（各占各的标签与文件）；011 必须在 010 之后。

## 全局规则（每个计划都适用）

1. **不提交代码。** 本项目默认由 owner 手动审阅和提交。执行者把所有改动留在工作区，不运行 `git commit`、`git merge`、`rebase`、`--amend`，完成后在状态列写 DONE 并附改动摘要。
2. **`pkg/gateway/dist` 不得出现在最终改动里。** 它是发版流程管理的构建产物（见 `scripts/check-webui-dist.sh`：普通 PR 改动 dist 会被 CI 拒绝）。真机验证一律用 `FOREBRAIN_STATIC_DIST` 指向临时构建目录；如果为了验证嵌入式 UI 跑过 `make build`，结束前必须执行
   `git checkout -- pkg/gateway/dist && git clean -fdq pkg/gateway/dist`，并确认 `git status --short pkg/gateway/dist` 无输出。
3. **自动化真机验证是完成标准的一部分。** 每个计划的"完成标准"都包含 `scripts/acceptance/web_e2e.sh` 全部通过：它构建真实二进制，以隔离的 `FOREBRAIN_HOME` 执行 `forebrain gateway start`，用真实 Chrome（Playwright 驱动）打开页面、操作、断言页面与网络，并保存截图。只跑 vitest/jsdom 或只手点页面都不算验证。
4. **提示缓存命中率不变。** 本期所有改动都在 HTTP 鉴权、进程启动输出和前端样式/布局上，不触及提示词组装。每个计划结束时运行
   `git diff --stat -- pkg/agent pkg/assembly pkg/llm pkg/turn/prompt* internal/agentrun` 应无输出；若有输出，停止并报告。
5. **界面文案双语。** 所有用户可见文字同时写入 `frontend/src/locales/index.ts` 的 `zh` 与 `en` 两张表，组件里不得硬编码。
6. **不留外部产品痕迹。** 代码注释、标识符、文案、文档里不得出现"参考/对齐/模仿某产品"之类的表述；设计理由只用它在 Forebrain Harness 里成立的理由来写。
7. **修根因，不打补丁。** 不为掩盖问题添加兜底、重试或空值保护；受信任边界之外的输入（浏览器请求头、Cookie、Origin）才需要校验。
8. **死代码彻底删除。** 改动后没有生产调用者的函数、类型、CSS 变量、配置字段一并删除，连同只测试它们的测试。
9. **包依赖图。** 如果改动了 `pkg/...` 之间的 import，运行 `scripts/package-graph.sh` 重新生成 `pkg/architecture/testdata/graph.json`。本期计划的设计刻意不引入新的包间依赖，正常情况下该文件不应变化。
10. **禁止粉色/品红。** 任何新增颜色都不得落在粉色、品红色相。
11. **多计划共享文件按"每次重读后追加"的方式改。** 下面这些文件被多份计划写，执行者动它们之前必须重新读一遍当前内容（别人的计划可能已经改过），并且**只追加自己那一项，不重排、不顺手清理别人的改动**：
    - `frontend/src/locales/index.ts`（13 份计划都加文案键：只在自己的区块里加键，zh/en 两表成对）；
    - `frontend/src/App.vue` 的菜单数组（007 建数组，009/010/011 各追加一项，008/012/013/014 只用不排）；
    - `pkg/gateway/api_extra.go` 的路由注册行（9 份计划各加自己的 group，不动别人的行）；
    - `frontend/src/lib/api.ts`、`frontend/src/router/index.ts`、`frontend/src/views/SettingsView.vue`（标签数组）。
    这几份文件里的任何一处冲突都表现为"别人的改动被覆盖"，而单份计划的验证命令抓不到——所以每份计划的漂移检查都已包含它们，执行前先跑。

## 通用命令（在仓库根目录执行）

| 用途 | 命令 | 成功标志 |
| --- | --- | --- |
| Go 静态检查 | `go vet ./...` | 退出码 0 |
| Go 测试（相关包） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/channel/... ./pkg/config/... ./cmd/forebrain/... -count=1` | 全部 `ok` |
| Go 全量测试 | `make test` | 全部 `ok` |
| 前端依赖 | `cd frontend && corepack pnpm install` | 退出码 0 |
| 前端单测 | `cd frontend && corepack pnpm test` | 全部通过 |
| 前端构建（不写 dist） | `cd frontend && corepack pnpm build --outDir "$TMPDIR/fb-webui" --emptyOutDir` | 退出码 0 |
| 真机验证 | `scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS`，截图目录路径随之打印 |
| 真机验证（真实模型档） | `set -a; . ~/.forebrain/e2e-zhipu.env; set +a; FOREBRAIN_E2E_REAL_LLM=1 scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS`；对模型回复文本的断言自动放宽为非空 assistant 回复 |

真实模型档的凭据在 `~/.forebrain/e2e-zhipu.env`（owner 私有文件，`chmod 600`，**不进仓库**）：`FOREBRAIN_E2E_ZHIPU_KEY` + 智谱 `glm-5.3-flash`（openai chat completions 兼容端点 `https://open.bigmodel.cn/api/coding/paas/v4`）。计划文件与代码里只允许出现 env 引用名（`api_key: ${FOREBRAIN_E2E_ZHIPU_KEY}`），明文密钥不写进仓库任何文件——yaml 里的明文 secret 本就会被 config 守卫拒绝启动。默认档用假模型，无需凭据；真实档供 011 等需要真实 LLM 回合的验收使用。

## 本期顺带修复的既有缺陷

按"顺带发现的缺陷必须纳入本期"的规则，以下问题已写进对应计划，而不是留作已知问题：

| 缺陷 | 位置 | 计划 |
| --- | --- | --- |
| 浏览器没有任何途径取得 gateway token，默认安装下所有 `/api` 请求都是 401（本次报错的根因） | `frontend/src/lib/api.ts:8-24,839-846,1431-1454`；`pkg/home/seed.go:66-80` | 001 |
| 6 处裸 `fetch` 与附件 `<img src>` 本来就带不上请求头 | `frontend/src/lib/api.ts:1148,1194,1203,1216,1237,1466`；`frontend/src/views/ChatView.vue:1643-1645` | 001 |
| WebSocket 与 HTTP 各写一套鉴权，`auth.mode: none` 时 WebSocket 仍然要求 token | `pkg/gateway/server.go:651-665` 对比 `pkg/gateway/controlplane.go:67-74` | 001 |
| WebSocket 升级接受任意 Origin | `pkg/gateway/server.go:586-588` | 001 |
| `VITE_FOREBRAIN_GATEWAY_TOKEN` 会把密钥编译进前端产物；一组无调用者的运行时辅助函数（含外来默认端口 18789） | `frontend/src/lib/forebrainGatewayRuntime.ts:3,588-641`；`frontend/src/vite-env.d.ts` | 001 |
| 假登录桩 `authLogin` 生成 `local-<id>` 且无人调用；匿名 clientId 无人使用 | `frontend/src/lib/api.ts:1431-1454`；`frontend/src/composables/useAuth.ts` | 001 |
| `getErrorMessage` 读不到 gateway 的 `{"error": "..."}` 响应体，只能显示 axios 的通用句子 | `frontend/src/lib/api.ts:26-36` | 001 |
| 开发代理里残留一段指向 `localhost:8216` 的旧转发规则 | `frontend/vite.config.ts:53-63` | 001 |
| 端口被占用时 `RestServer.Run` 直接 `panic` | `pkg/gateway/http_server.go:234-242` | 002 |
| 18 个 gateway 配置字段写进了示例配置和文档，但没有任何代码读取；示例和文档里的 `cors_origins`、`auth.password` 在结构体里根本不存在 | `pkg/config/config.go:5-30`；`forebrain.yaml:15-83`；文档站 `docs/config/runtime-gateway-and-channels.md:54-107` | 002 |
| `tailwind.config.ts` 与 `tailwind.config.js` 重复，前者从未生效 | `frontend/tailwind.config.ts` | 003 |
| 声明了从未加载的 `Source Serif 4`，中文实际落到系统宋体 | `frontend/src/assets/main.css:180,626`；`frontend/tailwind.config.js` | 003 |
| 浅色主题下 `--muted`、`--accent` 是深色值，`--secondary-foreground` 是浅色值 | `frontend/src/assets/main.css:90-93` | 003 |
| 5 个 CSS 变量（`--forebrain-badge-bg`、`--forebrain-brand-3`、`--forebrain-brand-outline-shadow-soft`、`--forebrain-nav-bg`、`--forebrain-nav-shadow`）和一批工具类没有任何使用者；shadcn 桥接变量沿用了与本项目无关的配色命名（`--terracotta`、`--ivory`、`--olive` 等） | `frontend/src/assets/main.css` | 003 |
| `favicon.svg` 使用渐变 | `frontend/public/favicon.svg` | 004 |
| 中文表里 `agents.liveTitle` 仍是英文 "Live agents" | `frontend/src/locales/index.ts:111` | 005 |
| "新聊天"创建会话失败时静默清空界面，不报错 | `frontend/src/views/ChatView.vue:1478-1493` | 005 |
| 目录树接口每次请求递归遍历整个工作区（含 `node_modules`），结果放进进程级全局缓存 | `pkg/gateway/api_extra.go:34-41,242-293` | 006 |
| 片段接口只做字面路径检查，工作区内指向外部的符号链接可读到工作区外的文件；还会误拒以 `..` 开头的文件名 | `pkg/gateway/api_extra.go:382-387` | 006 |
| 目录树的"使用当前主代理工作区"测试在两个工作区都成立，测不出问题 | `pkg/gateway/notification_hook_test.go:212-234` | 006 |
| "设置"里的权限记忆与"权限"页重复（两处都有规则列表，"规则评估"与"判定验证"重复） | `frontend/src/views/SettingsView.vue:115-190`；`frontend/src/views/PermissionsView.vue` | 007 |
| providers 的 GET 用 `[REDACTED]` 占位符往返、PUT 把 api_key 明文写进 yaml（未转 `${ENV}` 引用）；`Models` 列表字段 `json:"-"`，HTTP API 无法表达多模型 | `pkg/gateway/api_extra.go:2946-2994`；`pkg/config/agents.go:32`、`pkg/config/agent_llm.go:253` | 014 |
| 定时任务列表接口不支持按项目过滤（永远返回该主代理全部任务） | `pkg/gateway/api_extra.go:3083-3098` | 013 |
| 记忆文件列表只有游标分页，没有排序、时间戳与关键词过滤，Web 无法实现 owner 要求的记忆管理页 | `pkg/memory/backend.go:114-125,277` | 012 |
| `fb_sessions` 没有会话用途标记，工坊会话无法从对话抽屉里排除 | `pkg/state/schema.sql:29-47` | 011 |
| 会话内审批三档切换只有 TUI 路径（`ApplyPermissionPreset`），Web 无端点；设置页无全局默认编辑入口 | `pkg/tui/chat_session.go:2047` | 009 |
| 技能离线安装缺失（安装只支持 git_repo/skills_sh）；`SharedSkillsRoots` 只是展示元数据，"共享技能"在加载链里就是 `<home>/skills`，无独立启停状态文件 | `pkg/skill/service.go:299-330`；`pkg/config/primary_agent.go:251` | 010 |

## 不在本期范围

- 把 `web_e2e.sh` 接入 GitHub Actions：需要在 CI 里安装 Chrome 并管理测试 token，属于另一项决定；本期只要求在 owner 的机器上可一键运行。
- 深色内容区：owner 明确要求内容区始终白底，本期删除内容区的深色变量，不保留"整体深色"模式。

## 复核过的判断，不必再查

2026-09-29 复核全部 14 份计划时逐条核对过以下疑点，结论是**按设计如此**，不是缺陷（后续复核不必重复）：

- **计划里没有 "Git workflow / 分支 / commit" 一节**：本仓库的铁律是 owner 手动提交，所有计划统一改写为"不要提交代码"并引 README 全局规则 1，这是有意替换，不是模板缺失。
- **`docs/config/runtime-gateway-and-channels.md` 在本仓库找不到**：那是文档站独立仓库（`forebrain-harness.github.io/`，本仓库 `.gitignore` 忽略）里的路径；002 的"现状"里已写清所属仓库，路径按该仓库根解析。
- **`pkg/tool/state.go:2478` 与 `pkg/home/pathguard.go:51` 同名 `ResolveWithinRoots`**：前者是一行转发到后者，不是重复实现，006 引用两处都对。
- **`frontend/src/components/project/` 目录尚不存在**：由计划 008 新建，012/013 的漂移检查引用它属于预期（008 之前跑这两份的漂移检查为空即可）。
- **计划里的 `web_e2e.sh` 引用在 001 之前不存在**：该脚手架由 001 建立，后续计划都以它通过为完成标准，顺序即依赖。
