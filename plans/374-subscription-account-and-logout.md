# Plan 374: 订阅登录显示当前账号并支持退出（TUI /connect + Web 模型管理）

> 执行器须逐步实施并运行每步验证；命中 STOP 条件时停止，不自行扩大范围。本文只是一份实施计划，尚未修改功能源码。
>
> **先做漂移检查**：`git diff --stat 9552c46c..HEAD -- pkg/subscription/registry.go pkg/subscription/chatgpt.go pkg/subscription/opencode.go pkg/subscription/volcengine_oauth.go pkg/llm/openai/auth.go pkg/llm/opencode/auth.go pkg/gateway/subscriptions_api.go pkg/gateway/subscriptions_api_test.go pkg/gateway/login_api.go pkg/gateway/login_api_test.go pkg/gateway/models_api.go pkg/gateway/api_extra.go pkg/tui/setup.go pkg/tui/setup_test.go plugins/subscriptions/qodercn/plugin.go plugins/subscriptions/qodercn/auth.go plugins/subscriptions/workbuddy/plugin.go plugins/subscriptions/workbuddy/auth.go frontend/src/lib/api.ts frontend/src/locales/index.ts frontend/src/components/models/SubscriptionLoginModal.vue frontend/src/components/models/SubscriptionLoginModal.test.ts frontend/src/components/models/ModelFormDrawer.vue frontend/src/components/models/ModelFormDrawer.test.ts frontend/src/components/models/ModelsTable.vue frontend/src/components/models/ModelsTable.test.ts frontend/src/components/settings/ModelsTab.vue`。同时运行 `git status --short` 检查未提交改动。若相关实现已变化，重新核对本文摘录（按符号重定位），不能机械应用。

## Status

- **Status**: TODO（原型已交付并浏览器实测；**派发执行前 owner 须确认原型并在 §0 留痕**，执行器开工前必须核对 §0 的 SHA-256 闸门。2026-11-24 `review-plan` 复核：七处可执行性缺口已就地修订；同日 owner 三项裁决〔dead 也进账号页、env Key 的 `Detail` 留空、共享凭据文件写明路径〕已落进 §2 契约与两份原型，见 §0.2）
- **Priority**: P1
- **Effort**: L（后端能力契约 + 5 个订阅实现 + gateway + TUI 向导 + Web 三组件 + 双语文案 + 测试）
- **Risk**: MED（删除凭据是不可逆操作，必须确认框先行；登录复用/探测缓存语义不得破坏；模型行保留不删）
- **Category**: Feature
- **Depends on**: 无未完成前置。311（默认强度页序）、312（connect 面板状态机）、265/266（订阅页与登录模态）、281（粘贴 Key）、283（subagent 授权）均已落地，本计划在其页序与面板归属语义上增量插入，不得回退它们。
- **Planned at**: commit `9552c46c`，2026-11-24，分支 `dev`
- **Scope of investigation**: 只调查订阅注册表、五个订阅的凭据存储、gateway 订阅/登录 API、TUI /connect 向导、Web 模型管理三组件与 locales。不是全仓审计；未审计插件私有协议细节、真实厂商服务、安全全景、数据库三引擎实现。

## 0. 高保真可交互原型（两份，待 owner 确认）

用户要求「一份可实施可落地的计划和 tui 端、web 端两份原型稿」。两份原型均为单文件、零依赖、零网络的纯前端模拟：登录/退出全部是页内内存状态，不写真实数据库、不调用任何厂商、不保存任何真实凭据（演示账号均为虚构 example.test 邮箱与掩码 ID）。

- **TUI 原型**: `plans/374-account-tui-prototype.html`（SHA-256 `7db6a090b584bdb0b655e5af7bb38aeb36389cd43d4ed1bca8f8986eed7eec72`；**选项只留标签、无说明文案；供应商行不放 Signed in 状态标记**——owner 2026-11-24 两次极简裁决；**dead 也进账号页、共享凭据文件把路径写进确认页**——owner 2026-11-24 review-plan 三项裁决，见 §0.2）。沿用 `plans/311-connect-prototype.html` 的终端字符网格约定：深色 chrome、CJK 宽度 2、fg 调色板（110 强调 / 70 成功 / 203 危险 / 214 警告 / 244 暗灰）、左侧场景导航、键盘 ↑↓/Enter/Esc/Tab/Home/End + 过滤 + 鼠标点选 + 滚轮、120/88/68 三档列宽、终端下方观察器（模拟 DB / 事件 / 事实）。**终端面板内文案一律英文**（与真实 TUI 一致，config-tui 回归脚本拒绝面板内 CJK）；chrome 说明文案中文。场景覆盖（13 个）：供应商列表带账号行、账号页（继续/换号/退出）、退出确认、退出后回到列表、换号登录、OpenCode 环境变量 Key 边界、qodercn 外部来源会重新登录的提示（场景初始即退出后提示帧）、dead 行同形、**dead 的账号页（Sign in again / Sign out，无 Continue）**、**共享凭据文件（Codex CLI auth.json）的确认页**、读不出账号名、88/68 窄宽。页内 `window.accountTuiPrototype.snapshot()` 可观察状态（不含凭据）。
- **Web 原型**: `plans/374-account-web-prototype.html`（SHA-256 `9c7aa7baf0b4c9daf3a44e79d9aa5c81bde67f6b85200ddb41dbd280ef3c2f05`）。视觉 1:1 对齐 `frontend/src/assets/main.css` 浅色令牌与 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/llm-models-preview.html` 的框架（navy #1F4A9E 品牌色、#F7F8FA alt、#E4E7EC divider、success #067647 / warning #B54708 / danger #B42318）。中文文案。可操作覆盖（9 场景）：设置·模型页表格（账号登录行显示账号名）、订阅登录模态第 1 步账号行、第 3 步账号条（退出登录/换个账号）、退出确认框、退出后表格源状态翻为未登录、抽屉来源卡账号行与退出、环境变量 Key 边界提示、qodercn 409 外部来源提示、**共享凭据文件的确认框（写明 Codex CLI 的 auth.json 路径）**。页内 `window.accountWebPrototype.snapshot()` 可观察状态。
- **回归脚本**: `plans/374-account-tui-prototype.test.cjs`、`plans/374-account-web-prototype.test.cjs`（node + frontend 的 Playwright，纯断言零写入；运行方式：`python3 -m http.server 8765 --directory plans` 后 `node plans/374-account-tui-prototype.test.cjs`，web 同理）。
- **已浏览器实测**：见 §0.1 实测留痕（2026-11-24）。关键帧截图 `plans/374-shot-*.png` 由实际渲染 DOM 栅格化。
- **确认留痕与执行闸门**：owner 确认原型后在本节记录「原型已确认」与日期。执行器开工前必须 `shasum -a 256 plans/374-account-tui-prototype.html plans/374-account-web-prototype.html` 核对等于上述两个值，不符即 STOP 报告，不得按记忆中的旧稿实施。**原型中的文案、页面顺序、选项标签即实施目标**；如原型与 §2 契约冲突，以 §2 为准并先修订原型、重新冻结哈希、重新留痕。

### 0.1 实测留痕（2026-11-24 交付实测）

- 日期 / 浏览器 / 驱动方式：2026-11-24 / Playwright chromium headless（`frontend/package.json` 的 `@playwright/test`）＋截图逐张目检 / `python3 -m http.server 8765 --directory plans` 后 `node plans/374-account-tui-prototype.test.cjs`、`node plans/374-account-web-prototype.test.cjs`。截图 emulate `prefers-reduced-motion: reduce`，避免入场动画半透明半帧。
- TUI 黄金路径与边界场景结果：**14/14 passed、browserErrors=[]**（2026-11-24 owner 裁决后复跑）。13 场景 × 120/88/68 三宽度渲染非空、终端内无 CJK、行不溢出；供应商行账号副行且无 Signed in 状态标记（owner 裁决）；账号页三选项（Sign out danger）与 Esc 回供应商列表；退出确认 No 不删 / Yes 删凭据回列表且模型行保留（source_state=missing）；Continue 走完 311 页序保存；换号成功替换登录、取消留账号页；envKey 无 Sign out 且提示 unset、供应商行只留 `OPENCODE_API_KEY`（无 `(env)` 后缀）；volcengine 仅掩码 AKLT…f3a9；external 退出后如实提示且向导存活；**dead 进账号页（Sign in again / Sign out 两项、无 Continue），Sign out 删凭据回列表、Sign in again 走登录并清 dead 标记**；**共享凭据文件的确认页写出 `~/.codex/auth.json` 并说明 Codex CLI 一同退出**；no-account 降级不阻断。
- Web 黄金路径与边界场景结果：**12/12 passed、browserErrors=[]**（同上复跑）。9 场景渲染干净且全页无全量 AccessKeyID；表格 key 列账号小字（doudou@example.test / Wang Bin）与套餐 Key 掩码；第 1 步账号副行替代 flow 句、usable 直进第 3 步；第 3 步账号条＋退出确认取消/确认（204 回第 1 步、表格刷新、行保留）；换号 force 走流程替换账号；provision 添加勾选行；envKey 无退出/换号＋unset 提示；409 留第 3 步并刷新为导入账号；抽屉就地退出（抽屉不关、徽章翻未登录）；**共享凭据文件的确认框写明路径并说明 Codex CLI 一同退出，确认后行保留**；Esc 逐层回退不写凭据。
- 回归脚本结果：TUI `passed: 14`、Web `passed: 12`，两者 `browserErrors: []`（2026-11-24 实跑，脚本与原型同目录）。
- 截图清单：`374-shot-tui-{start,account,signout,after,envkey,dead,shared,external}.png`（8 张）＋ `374-shot-web-{table,step1,step3,confirm,after,drawer,envkey,external,shared}.png`（9 张），共 17 张，均由实际渲染 DOM fullPage 栅格化；新增的 dead / shared 两帧已目检。
- 实测期修复（均已冻结进上方哈希）：① TUI 标签列与描述列间距在标签恰为最长时塌缩（pad 目标补前缀 2 列，`nameWidth+4`）；② TUI 供应商行无账号时不再显示虚构描述句（对齐 §2.4.1「行与今天完全一致」）；③ TUI external 场景初始即退出后提示帧；④ TUI snapshot.options 补 desc 字段；⑤ chatgpt 演示数据 plan/api_path 改空值（真实 `offersFromRecords` 不设 Plan/APIPath，行显示「原生协议」、来源卡不重复名称）；⑥ Web dead/unreachable 徽章 variant 对齐 stateVariant、操作列图标收窄消除省略号；⑦ **owner 裁决（2026-11-24，极简风格）**：TUI 选项说明文案全删（账号页三项、API/custom 行、effort 页提示），选项只留标签＋状态；账号名（供应商行）与 endpoint（模型行）属数据显示予以保留，退出后果由确认页文案完整承载；回归复跑 12/12 全绿，SHA 重冻结、6 张 TUI 截图重生成；⑧ **owner 裁决（2026-11-24，第二次极简）**：供应商行右缘 `Signed in` 状态标记删除（账号名即登录态信号），usable/dead 行列表同形、状态进入后分辨；回归断言同步翻转、复跑 12/12 全绿，SHA 再冻结、截图再重生成；⑨ **owner 裁决（2026-11-24，review-plan 三项）**：dead 也进账号页（标题写明登录已失效、选项 `Sign in again`/`Sign out`，无 Continue）、env Key 的 `Detail` 留空（行只留变量名）、共享凭据文件的确认页写明路径；两份原型各加 1–2 个场景、回归 TUI 14/14＋Web 12/12 全绿，哈希再冻结。

### 0.2 复核（2026-11-24 `review-plan`，摘录实读基线 `9552c46c`，复核时 HEAD `a6ba8b9f`）

本轮按符号逐条打开代码核对 §2/§3 的每处引用：`pkg/subscription`、`pkg/state/config_store.go`、`pkg/llm/{openai,opencode}`、`pkg/gateway/{subscriptions_api,login_api,models_api,api_extra}.go`、`pkg/tui/setup.go`、`setup_test.go`、`frontend/src` 的引用位置全部在位（行号见 §3，未修正的个别偏移不影响按符号定位）。两份原型的 SHA-256 与 §0 相符。基线至今只多了 `a6ba8b9f`（375：`pkg/tui/overlays.go` 与三个测试文件），与 §5 In scope 及顶部 drift 清单里的文件零交集，摘录仍成立——唯一擦到的是 `pkg/tui/chat_session_test.go`（新用例的 `fakeSelector` 就在这个文件里，行号已按 HEAD 改写，见 §3.6）。

**三项裁决（owner 2026-11-24 `review-plan` 答复，已落进原型并重冻哈希）**：

1. **dead 也进账号页**（owner 选项：dead 也进账号页）。§2.1.6 的理由是真话——dead 的存储凭据恰恰最需要删掉；原稿只让 usable/unreachable 进账号页，dead 走登录页且登录页没有退出入口，而同一份凭据在 Web 端 `can_logout` 是 true（§2.6），两面不一致。裁决后：账号页条件是「实现 AccountReader 且探测 ≠ missing」（usable/unreachable/dead 三态都进），dead 的标题写明登录已失效、选项只有 `Sign in again`（仍 DefaultIdx 0）与 `Sign out`，没有 Continue（没有可继续的登录），也没有单独的换号项（`Sign in again` 就是换号）。原型加场景 `dead-account`，回归断言断言这条路径。
2. **env Key 的 `Detail` 留空**（owner 选项：留空，行只留变量名）。原稿 `Detail: "env"` 会按 §2.4.1 的 `Name (Detail)` 渲染成 `OPENCODE_API_KEY (env)`，而 Web 同一格是「环境变量提供」。裁决后：`Detail: ""`，env 语义只由 `EnvKey` 承担；两份原型的演示数据同步（TUI 场景 `envkey` 的行只剩变量名）。
3. **共享凭据文件把路径写进确认框**（owner 选项：写明路径）。`credentials.chatgpt` 指到 Codex CLI 的 `~/.codex/auth.json` 时，退出席会删掉那个共用文件；原稿文案只说「本机保存的登录凭据」，读起来是本产品的登录。裁决后：解析出的路径不在安装 home 之下时，TUI Confirm 与 Web `models.logoutMessage` 都把路径写出来并说明那个客户端也会退出登录（两份原型各加一个 `shared` 场景）。

**本轮就地修订（可执行性，不改产品行为）**：§2.1.3 掩码档位、§2.1.4 的 `Detail`、§2.4.1 注释同步、§2.4.2/§2.4.4 的 dead 与退出特判、§2.6 的 409 识别与 405、Step 5 的 ctx 与锁、Step 6 既有测试迁移、Step 9 抽屉刷新、§3.2 transport 已验证、§3.6 引用位置、§9 STOP 3 改为已证事实。

## 1. Why this matters

用户原话：「现在 forebrain harness tui 端的 /connect 面板里登录订阅以后，既没有显示当前登录账号也无法退出账号，web 端的模型管理也不显示当前登录账号，也无法退出账号，请给出一份可实施可落地的计划和 tui 端、web 端两份原型稿」。

现状证据（基线 `9552c46c` 实读）：

1. **账号身份已存储但从不展示**。chatgpt 凭据含 `Email/Plan/AccountID`（`pkg/llm/openai/auth.go:40-51`），opencode 含 `Email/OrgName/AccountID`（`pkg/llm/opencode/auth.go:59-67`），qodercn 含 `UID/Name/Email`（`plugins/subscriptions/qodercn/auth.go:43-55`），workbuddy 含 `UID/Name/EnterpriseID/AccountType`（`plugins/subscriptions/workbuddy/auth.go:48-69`）。volcengine 只存 STS 三元组、无身份字段（`pkg/subscription/volcengine_oauth.go:98-109`）。
2. **两个 surface 都只显示裸状态**。TUI 供应商行只有 `Status: "Signed in"`（`pkg/tui/setup.go:753-756`）；已登录订阅直接跳过登录页进模型列表（`flowPages` :592-594，`signIn` usable 分支 :1228-1230 直接 `return true, nil`），全程没有账号名、没有退出入口。Web 订阅行只有 `subscriptionRow{ID,Name,Flows,State}`（`pkg/gateway/subscriptions_api.go:52-57`），模态第 1 步只显示「已登录/未登录」徽章（`SubscriptionLoginModal.vue` stateText :310-312），抽屉来源卡只有状态徽章与「重新登录」（`ModelFormDrawer.vue:27-36`）。
3. **退出原语已存在**。`state.ConfigStore.SaveCredential(ctx, section, nil)` 即删除该凭据行（`pkg/state/config_store.go:362-377`）；DB 凭据桥 `dbCredentialStore` 把 `auth.chatgpt`/`auth.opencode` 接到 openai/opencode 的 `CredentialsStore`（`pkg/process/config_db.go:869-914`）。qodercn/workbuddy 的凭据同时活在 DB 行与文件两处、取新鲜者（两插件 `auth.go` loadCredentials），**退出必须两处都删**。
4. **订阅接口没有账号/退出能力**。`subscription.Subscription`（`pkg/subscription/registry.go:31-50`）只有 ID/Label/Flows/Probe/Start/Offers/Provision/RefreshKey；可选能力接口的先例是 `DeviceCodeLogin`（:80-82）——本计划照此增加两个可选能力，外部插件不实现也不破坏。

## 2. 已确定的产品与数据契约

### 2.1 账号显示

1. **账号一律本地读凭据，零厂商网络调用**。显示身份不是探测，探测状态（usable/missing/dead/unreachable）语义不变。
2. 每个订阅的账号字段：

   | 订阅 | Name（主显示） | Detail（副显示，原样数据） | 来源 |
   |---|---|---|---|
   | chatgpt | `Email`，空则 `AccountID` | `Plan`（如 plus/pro，厂商原词） | openai 存储凭据 |
   | opencode-go | `Email`，空则 `AccountID` | `OrgName` | opencode 存储凭据；无存储凭据但 `OPENCODE_API_KEY` 非空 → 见第 4 条 |
   | volcengine | `MaskID(AccessKeyID)` | `"STS"` | DB `auth.volcengine` 行 |
   | qodercn | `Email`，空则 `Name`，空则 `UID` | `""` | loadCredentials（DB+文件取新鲜） |
   | workbuddy | `Name`，空则 `UID` | `AccountType`（personal/pro…） | loadCredentials（DB+文件取新鲜） |

3. **掩码规则** `subscription.MaskID`：长度 >12 → 前 4 + `…` + 后 4；8 < 长度 ≤ 12 → `…` + 后 4；4 < 长度 ≤ 8 → 只有 `…`（尾 4 会把 5 个字符里的 4 个露出来，等于没掩）；≤4 → 原样。AccessKeyID 是凭据对的一半，按敏感值对待，任何 surface 不得显示全量（邮箱是用户要求显示的身份，不掩码）。
4. **环境变量 Key 模式**（opencode 无存储凭据但 `OPENCODE_API_KEY` 非空）：账号显示为 `Name: "OPENCODE_API_KEY"`、`Detail: ""`、`EnvKey: true`（env 语义只由 `EnvKey` 承担，`Detail` 不许放 `"env"` 这类内部词——否则 §2.4.1 的行会渲染成 `OPENCODE_API_KEY (env)`，见 §0.2 第 2 条）；**不提供退出按钮**，改为提示「由环境变量提供，请在启动终端 unset 后重启」。qodercn 的 `QODER_PERSONAL_ACCESS_TOKEN` 不是常驻凭据源（Probe 时兑换成存储凭据，`auth.go:507-527`），按第 2.2 节外部来源处理。
5. 凭据存在但读不出身份（损坏/字段全空）：账号视为「无」——TUI 供应商行回到只有标签，账号页写「This sign-in stands; no account name is stored.」；Web 第 1 步保留原 flow 描述句、第 3 步账号条写「登录有效，未存储账号名」。**不得报错阻断列表**。
6. 账号读取时机：探测状态 ≠ missing 时读（dead/unreachable 的登录仍有存储凭据，dead 恰恰最需要退出）。

### 2.2 退出登录

1. **退出 = 删除本机存储的登录凭据**：DB 凭据行（`SaveCredential(section, nil)`）**和**文件兜底（`os.Remove`，容忍 NotExist）两处都删。只删一处会让无 DB 的进程或下次 DB 打开失败时登录复活。
2. **模型行保留不删**。退出后行的 `source_state` 翻为 missing（opencode 环境变量模式除外），调用时 transport 读不到凭据即失败——这是既有语义，无需改 transport。退出**不写模型行**，因此**不需要** `adoptLLMModelWrite`/`SyncConfigFromDB`。
3. **退出必须确认框先行**（TUI Confirm、Web ConfirmDialog danger），文案明示「模型保留、重新登录前无法调用」；凭据路径不在安装 home 之下时（第 5 条）再把该路径与「那个客户端也会退出登录」一并写进去（owner 2026-11-24 裁决，见 §0.2 第 3 条）。
4. **外部来源哨兵** `subscription.ErrExternalLogin`：存储凭据已删（如有），但环境/官方客户端会让机器重新登录。返回该错误时 surface 必须如实提示，不得宣称已退出：
   - opencode：无存储凭据且 env key 非空 → 直接返回（无可删）；有存储凭据 → 先删，env key 仍非空 → 删后返回。
   - qodercn：删 DB 行 + 文件后，若 `QODER_PERSONAL_ACCESS_TOKEN` 非空 → 返回；若官方客户端凭据文件存在（`QoderHomeDir()/.auth/user`，非空）→ 返回（下次 Probe 会重新导入，`auth.go:528-535`）。**只查文件存在，不做解密、不写 machine identity**。
   - chatgpt/workbuddy/volcengine：无外部来源，删完即 nil。
5. **chatgpt 文件路径的共享后果**：默认路径是 `FOREBRAIN_HOME/auth.json`（forebrain 自有）；若用户把 `credentials.chatgpt` 配置指向 Codex CLI 的 `~/.codex/auth.json`（路径解析支持 tilde，`auth_test.go:240`），退出会一并删除该共享文件。这是「退出这个账号」的诚实后果，文案必须让用户看见它：解析出的路径不在安装 home 之下时把路径写进 Confirm 与 `models.logoutMessage`（owner 2026-11-24 裁决，见 §0.2 第 3 条）；维护注记 §10 记录此点。
6. 退出成功后：TUI 清探测/账号/offers 缓存并回到供应商列表；Web 刷新订阅列表与模型表。**不自动触发重新登录**。
7. gateway 退出路由成功 → **204 No Content**（页面自行刷新，避免退出按钮等待一次厂商探测）；`ErrExternalLogin` → **409** + `{"error": 双语句, "external_login": true}`；其他错误 → 500 + `{"error": ...}`；订阅无 LogoutRemover 能力 → 404 双语「这个订阅没有可退出的登录」；非 POST → 405（照 `handleSubscriptions` 的写法）。前端识别 409 走 `GatewayHttpError.payload.externalLogin`（`api.ts:114-143` 已把 JSON 体 camelCase 化，:`2675` 一带是先例），**不许按错误文本判断**。

### 2.3 可选能力接口（照 DeviceCodeLogin 先例）

新文件 `pkg/subscription/account.go`：

```go
// Account names who a stored sign-in belongs to, read locally from the
// credential — no vendor call.
type Account struct {
	Name   string // email, nickname, or masked id
	Detail string // plan, org, or credential kind; "" for none
	// EnvKey marks a sign-in the environment provides (OPENCODE_API_KEY):
	// it cannot be removed here, and a surface offers no logout for it.
	EnvKey bool
	// SharedPath is the credential file this login lives in when that file is
	// not this install's own — the Codex CLI's auth.json, which the same login
	// signs that program in to. A surface that removes the login names the file
	// in its confirmation, so deleting another program's login is never silent.
	// "" means the file is this install's own.
	SharedPath string
}

// AccountReader is the optional capability of naming the stored account.
// A subscription without it shows its state alone.
type AccountReader interface {
	Account(ctx context.Context, env Env) (Account, bool, error)
}

// LogoutRemover is the optional capability of removing the stored sign-in.
// A subscription without it offers no logout.
type LogoutRemover interface {
	Logout(ctx context.Context, env Env) error
}

// ErrExternalLogin is answered by Logout when removing the stored login is
// not enough: an external source (an environment token, the official client's
// own login) will sign this machine back in on the next check. The stored
// login is still removed; surfaces explain the source instead of promising
// a signed-out state.
var ErrExternalLogin = errors.New("an external source provides this sign-in")
```

`Account` 返回 `(Account{}, false, nil)` = 无存储登录；`(Account{}, false, err)` = 读取失败（surface 降级不阻断）。**不得**把 Account/Logout 加进 `Subscription` 接口本体——那会破坏一切外部插件。`SharedPath` 今天只有 chatgpt 会置（`credentials.chatgpt` 指到 Codex CLI 的 auth.json 时，见 §2.2 第 5 条），所以文案里点名的就是 Codex CLI；将来若出现第二个共享文件，再把文案参数化（记入 §10）。

### 2.4 TUI /connect 交互

1. **供应商行**：探测 ≠ missing 的订阅行，`SelectItem.Description` = 账号 Name（Detail 非空时追加 ` (Detail)`）；**不再显示 `Status: "Signed in"`**（owner 2026-11-24 裁决：账号名即登录态信号，右缘 Signed in 冗余）——既有 `TestProviderPageMarksASubscriptionAlreadySignedIn`（`setup_test.go:682`）的 Status 断言随之改掉（改成断言账号名在 Description，别留空断言）。**这条不是唯一的既有断言改动**：settled 流程多一页会连带 `TestSubscriptionFlowDropsSettledSignInPage`、`TestEscOnOffersPageReturnsToProviderPage`、三个 connect 面板用例和两个测试起步函数，迁移清单见 Step 6 第 9 条。无账号能力/读不出 → 行只有标签（与今天一致）。usable 与 dead 行在列表同形（都显示存储的账号名），状态在进入后的账号页/登录页分辨（见 §10 第 7 条）。
2. **账号页**（新页 `setupPageAccount`）：**探测 ≠ missing 就进账号页**（usable/unreachable/dead 三态，owner 2026-11-24 裁决），页序变为 `Provider → Account → Offers → Effort → Subagents → Review`（原 `Provider → Offers → …`，311 页序其余不变）；missing 的页序不变，仍走 Credentials 登录页。判据只有一个：这一台机器有没有存储登录——账号名读不出（AccountReader 缺能力或字段空）也照样进账号页，页面按键入状态而不是按「有没有名字」分岔。
3. 账号页标题：`<Label>\nSigned in as <Name> (<Detail>).`；EnvKey → `<Label>\nSigned in with OPENCODE_API_KEY from the environment.`；无账号 → `<Label>\nThis sign-in stands; no account name is stored.`；**dead → `<Label>\nSigned in as <Name> (<Detail>). This sign-in no longer works; sign in again or sign out.`**（无账号名时省掉前半句）。选项（`selectRichTabbed`，DefaultIdx 0）**一律不带 Description**（owner 2026-11-24 极简裁决：选项只留标签；供应商行账号名与模型行 endpoint 是数据显示，不在此列）：
   - `Continue with this account` → 前进到 Offers（仅 usable/unreachable；dead 没有可继续的登录）。
   - `Sign in with another account` → 复用既有登录流程（含 Confirm 询问、面板归属 312 语义）；成功、重探为 settled → 清缓存、前进到 Offers；成功但重探仍不 settled → 留在账号页重选（不要带着空 offers 进 Offers 页）；取消/拒绝 → **留在账号页重选**。EnvKey 时标签改 `Sign in with an account`。
   - **dead 的第二项换成 `Sign in again`**（同一个 `runSignInFlow`，默认项），**不给** `Continue`、也不单列换号项——它就是换号。
   - `Sign out`（`Danger: true`，仅 LogoutRemover 且非 EnvKey）→ Confirm → Logout（退出后果「模型保留、重新登录前无法调用」由 Confirm 文案完整承载，见第 6 步）。成功 → 清缓存、回供应商列表；`ErrExternalLogin` → 清缓存、重探、status 输出提示句、留在账号页；其他错误 → 按既有错误语义返回。
4. **退出后回到供应商列表的实现要点**：退出使订阅不再有登录，账号页从 `flowPages` 消失；若直接 `return false, nil`，`run()` 的 `pageIndex(pages)==0` 分支会把整个向导当作取消退出（`setup.go:563-566`）。特判要用一个显式的信号，**不要**用 `!s.subscriptionSettled()` 反推（dead 本来就不 settled，Esc 也会落进那条判断）：`pickAccount()` 成功退出后返回包级哨兵 `errSignedOut`（照 offers 的 `ErrCancelled` 先例 `setup.go:519-525`），`run()` 的 `case setupPageAccount` 里 `errors.Is(err, errSignedOut)` → `s.page, s.back = setupPageProvider, true; continue`。
5. **signIn 拆分**：`signIn(sub)` 保留探测门（usable/unreachable 早退），门后全部逻辑原样移入 `runSignInFlow(sub) (bool, error)`；账号页换号、dead 的 `Sign in again` 都直接调 `runSignInFlow`（dead 不再经 Credentials 页）。**面板归属语义不变**：等待/编辑/错误一律留在 connect 面板（312），不落 transcript。
6. 缓存失效助手 `forgetSubscription(id)`：删 `probes[id]`、账号缓存、`offers=nil`、`selectedOfferIdx=-1`。换号成功与退出后都要调；换号成功后再 `probeSubscription(sub)` 刷新 settled 判定。
7. 终端面板文案英文（与真实 TUI 一致）；日志事件名 `llmsetup.signed_out`、`llmsetup.signed_out_external`（沿用 `logPromptChoice` 约定）。

### 2.5 Web 交互

1. **模态第 1 步**：订阅行有账号时，副行显示账号名 + detail（envKey → detail 位显示「环境变量提供」），**替代**原 flow 描述句；无账号时保持原样。徽章不变。
2. **模态第 3 步**：OfferPicker 上方加账号条：账号名（无账号 → 「登录有效，未存储账号名」）+ detail + 状态徽章 + 两个按钮：`退出登录`（danger ghost，仅 `canLogout`）与 `换个账号登录`（非 envKey）。envKey 时账号条下显示提示句（含变量名）。
3. **退出流程**：ConfirmDialog（danger）→ `logoutSubscription(id)` → 成功：emit `changed`（父级刷新表格）、重读订阅列表、回第 1 步；409 external_login：提示「本机保存的登录已删除，但环境变量或官方客户端仍会让这台机器重新登录」，同样刷新；其他失败：提示重试。
4. **换个账号**：gateway `POST /api/auth/login/:provider?force=1` 跳过 usable/unreachable 立即完成分支，强制走流程。模态 `startLogin(id, {force:true})`。登录完成响应（立即完成与 poll complete）携带 `account` 与 `can_logout`，第 3 步账号条用最新值。
5. **抽屉来源卡**：head 下加账号行（name + detail）；acts 区在 `sourceCanLogout` 时加 `退出登录` 按钮 → ConfirmDialog → logout → emit `changed`。来源状态徽章既有逻辑不变。
6. **模型表 key 列**：loginBacked 行在「账号登录」下追加账号名小字（`sourceAccount` 存在时）。
7. **ModelsTab**：给模态与抽屉接 `@changed` → `reload()`（不关模态/抽屉；抽屉那一半见 Step 9 的 `drawerRow` 重指）。
8. **dead 在 Web 的退出入口是抽屉**：模态对 dead 仍直接进登录流程（第 1 步点它就是登录），没有账号条可停；模型行的来源卡（`sourceCanLogout`，`can_logout` 对 dead 是 true）才是它的退出与重登入口。这条与 TUI 的三态账号页是同一语义的两种呈现，不要为了对称在模态第 1 步塞按钮。

### 2.6 API 契约（additive，零 schema 迁移）

```go
// gateway 新形状（subscriptions_api.go）
type subscriptionAccount struct {
	Name       string `json:"name"`
	Detail     string `json:"detail,omitempty"`
	EnvKey     bool   `json:"env_key,omitempty"`
	SharedPath string `json:"shared_path,omitempty"`
}
// subscriptionRow 追加：
Account   *subscriptionAccount `json:"account,omitempty"`
CanLogout bool                 `json:"can_logout"`
// llmModelRow 追加：
SourceAccount   *subscriptionAccount `json:"source_account,omitempty"`
SourceCanLogout bool                 `json:"source_can_logout,omitempty"`
// authLoginStatusBody 追加：
Account   *subscriptionAccount `json:"account,omitempty"`
CanLogout bool                 `json:"can_logout"`
```

- `GET /api/subscriptions`：每行带 account/can_logout。canLogout = 实现 LogoutRemover ∧ state ∉ {"", missing} ∧ ¬EnvKey（**dead 允许**，与 TUI 三态账号页同一口径）。
- `POST /api/subscriptions/:id/logout`：见 §2.2 第 7 条。路由注册在 `api_extra.go` 既有 subscriptions 路由旁（:444-446）。
- `POST /api/auth/login/:provider?force=1`：跳过立即完成分支；立即完成响应追加 `account`、`can_logout`。
- `GET /api/auth/login/:provider/:loginId`：complete 时带 `account`、`can_logout`（见 Step 5 第 6 条：在 `handleAuthLoginStatus` 里读，不在 `authLoginFinish` 的锁里）。
- `GET /api/llm-models`：行带 source_account/source_can_logout（复用每请求 `subscriptionStates` 缓存，一订阅一请求只读一次账号）。
- 前端 `api.ts`：`SubscriptionAccount` 类型（含 `sharedPath?`）；`SubscriptionRow.account?/canLogout?`；`LlmModelRow.sourceAccount?/sourceCanLogout?`；`ProviderLoginStatus.account?/canLogout?`；`startProviderLogin(provider, opts?: {force?: boolean})`；`logoutSubscription(id): Promise<void>`（204 成功，其余抛 `GatewayHttpError`，照 `cancelProviderLogin` :4878-4883 的写法）。**可选字段**，现有测试字面量不破。

### 2.7 双语文案（locales/index.ts，zh/en 各加一组，放在既有 models.* 键旁）

| key | zh | en |
|---|---|---|
| models.logout | 退出登录 | Sign out |
| models.logoutTitle | 退出登录？ | Sign out? |
| models.logoutMessage | 将删除本机保存的登录凭据。已添加的模型会保留，但在重新登录前无法调用。 | This removes the login saved on this machine. The models you added stay, but cannot call until you sign in again. |
| models.logoutConfirm | 退出登录 | Sign out |
| models.logoutExternal | 本机保存的登录已删除，但环境变量或官方客户端仍会让这台机器重新登录。 | The stored login was removed, but an environment variable or the official client will sign this machine back in. |
| models.logoutFailed | 退出失败，请重试 | Sign-out failed; try again |
| models.logoutShared | 这份凭据与 Codex CLI 共用（{path}），退出会同时让 Codex CLI 退出登录。 | This credential is shared with the Codex CLI ({path}); signing out here signs the Codex CLI out too. |
| models.switchAccount | 换个账号登录 | Sign in with another account |
| models.accountEnvKey | 环境变量提供 | From the environment |
| models.envKeyHint | 这个登录由环境变量 {name} 提供，无法在这里退出；请在启动 Forebrain Harness 的终端里 unset 后重启。 | This sign-in comes from the {name} environment variable; it cannot be removed here. Unset it in the shell that starts Forebrain Harness, then restart. |
| models.accountUnknown | 登录有效，未存储账号名 | The sign-in stands; no account name is stored |

## 3. 现状证据（基线 `9552c46c` 实读摘录）

### 3.1 注册表与可选能力先例（pkg/subscription/registry.go）

`Subscription` 接口 :31-50（ID/Label/Flows/Probe/Start/Offers/Provision/RefreshKey）；可选能力先例 :80-82：

```go
// DeviceCodeLogin is the optional capability of a sign-in that shows a short
// code ...（接口定义，注释从略）
type DeviceCodeLogin interface {
	DeviceCodeInfo() DeviceCodeInfo
}
```

`Env{Home string; DB *sql.DB}` :152-155；`Register` :225-239；`Registry()` :252-259 = chatGPT{}、openCodeGo{}、volcengine{} + pluginSubs；`ByID` :263-271。错误词汇先例：`ErrKeyChoice` :164、`ErrNoKey` :179、`ErrNoKeyToRefresh` :183、`ErrPlanKeyRequired` :189、`ErrKeyPasted` :206。

### 3.2 凭据存储与删除原语

`pkg/state/config_store.go:362-377`：

```go
func (s *ConfigStore) SaveCredential(ctx context.Context, section string, value json.RawMessage) error {
	...
	if value == nil {
		_, err := dbx.Exec(ctx, s.DB, configSectionDeleteSQL.For(dbx.DialectOf(s.DB)), section)
		return err
	}
	...
}
```

`pkg/process/config_db.go:909-914` `installCredentialStores`：`openaiauth.SetCredentialsStore(dbCredentialStore{store, section: "auth.chatgpt"})`、`opencode.SetCredentialsStore(... "auth.opencode")`。

openai（`pkg/llm/openai/auth.go`）：`Credentials{AccessToken,RefreshToken,IDToken,AccountID,Email,Plan,ExpiresAt,AuthMode}` :40-51；`CredentialsPath(home)` :124-130（支持配置覆盖与 tilde）；`var credentialsStore atomic.Pointer[CredentialsStore]` :148；`LoadStoredCredentials(ctx, home)` :164-173（absent → nil,nil）；`LoadProcessCredentials(ctx, path)` :210-226（store 优先，absent → error）。

opencode（`pkg/llm/opencode/auth.go`）：`Credentials{AccessToken,RefreshToken,ExpiresAt,AccountID,Email,OrgID,OrgName}` :59-67；`DefaultPath(home)` :71；`APIKeyEnv = "OPENCODE_API_KEY"`（endpoints.go:35）；`var credentialsStore atomic.Value` :190 + `credentialsStoreHolder struct{ store CredentialsStore }` :192；`LoadStoredCredentials(ctx, home)` :208-217；`LoadProcessCredentials(ctx, path)` :104-120。

volcengine（`pkg/subscription/volcengine_oauth.go`）：`volcengineCredentialSection = "auth.volcengine"` :40；`volcengineCredential{AccessKeyID,SecretAccessKey,SessionToken,...}` :98-109（**无身份字段**）；`volcengineStore(env) = &state.ConfigStore{DB: env.DB}` :435-437；`loadVolcengineCredential(ctx, env) (volcengineCredential, bool, error)` :441（env.DB==nil → absent）。**无文件兜底、无 env 读取**（grep 证实）。

qodercn（`plugins/subscriptions/qodercn/auth.go`）：`credentialSection = "auth.qodercn"` :34；`personalTokenEnv = "QODER_PERSONAL_ACCESS_TOKEN"` :37；`Credentials{UID,Name,Email,...}` :43-55；`credentialPath(home) = home/qodercn.json` :59-61；`loadCredentials(ctx, db, home)` :68-103（DB+文件取 ExpiresAt 新鲜者）；`saveCredentials` :107-121（两处都写）；`tryAdoptLocalQoderCNCredentials` :507-538（存储 → PAT 兑换 → 官方客户端导入）；官方凭据文件 `filepath.Join(QoderHomeDir(), ".auth", "user")` :547。

workbuddy（`plugins/subscriptions/workbuddy/auth.go`）：`credentialSection = "auth.workbuddy"` :33；`Credentials{UID,Name,EnterpriseID,AccountType,...}` :48-69；`credentialPath(home) = home/workbuddy.json` :73-75；`loadCredentials` :82-117、`saveCredentials` :122-141（同 qodercn 双处）。Probe 只读存储凭据（plugin.go:185-199），**无外部导入**。

**退出是否即时生效（复核已证，不是待查）**：两个 transport 每次请求都重读凭据——`pkg/llm/openai/auth.go:819`、`pkg/llm/opencode/transport.go:116`（并在 :146 按 401 重读一次）；`pkg/run/config.go:555-566` 只在构造客户端时判断「有没有登录」，真正的 token 取自请求时。所以删除凭据后下一次调用即失败，**不需要**任何缓存失效广播（§9 STOP 3 据此改写）。

### 3.3 gateway（pkg/gateway）

`subscriptions_api.go`：状态词常量 :29-34（usable/missing/dead/unreachable）；`subscriptionRow` :52-57；`subscriptionStates` 每请求探测缓存 :102-139（`of(id)` 带 modelsDiscoveryTimeout）；`handleSubscriptions` :143-163；`subscriptionRoute(w,r) (subscription.Subscription, bool)` :356-365（404 双语）；`failSubscriptionWrite` :372-423（错误→状态码词汇表）；`writeJSON/writeJSONStatus`、`llmText(r, zh, en)`（models_api.go:264）。

`login_api.go`：`gatewaySubscriptions`/`gatewaySubscriptionByID` 测试可换变量 :55-58；`authLoginFinish` :134-152（成功置 complete+baseURL）；`authLoginGet` :179-191；`authLoginFlow` :208-216（只驱 device_code/paste_callback）；`subscriptionEnv()` :220-230；`handleAuthLoginStart` :236-307（**usable/unreachable 立即完成分支 :261-269**）；`authLoginStatusBody` :312-321。

`models_api.go`：`llmModelRow` :63-118（Source/SourceName/SourceState :100-103）；`llmModelRowOf` :475-539（:533-537 填 SourceName/SourceState）；`adoptLLMModelWrite` :442-447（= SyncConfigFromDB，**退出不用它**）。

`api_extra.go`：auth 路由 :60-67；subscriptions 路由 :444-446。

### 3.4 TUI（pkg/tui/setup.go）

页常量 :383-397（iota+1）；`llmSetup` :430-479（probes map、offers、selectedOfferIdx、db）；`run()` :487-569（**pageIndex==0 即取消退出 :563-566**；offers ErrCancelled 特判先例 :519-525）；`flowPages()` :579-599（settled → `[Provider,Offers,Effort,Subagents,Review]`）；`subscriptionSettled` :606-616；`providerRows` :749-782（Status 只在 usable）；`probeSubscription` :834-846（缓存）；`signIn` :1224-1336（usable/unreachable 早退 :1228-1235；门后 Confirm→Start→面板/文本双路径）；`signInAnswer` :1341-1353；`pickOffers` :1681-1724（`selectRichTabbed` 用法范本）；`statusOutput(title) io.Writer` :1983-1990；`signInEnv` :1920-1925。`selectRichTabbed(sel, label, items, opts)` 在 tabbed_picker.go:12-17。`SelectItem{Label,Description,Category,Status,Danger,...}` 在 channels.go:616-652。

### 3.5 Web（frontend/src）

`SubscriptionLoginModal.vue`（746 行）：第 1 步行 :7-29（`data-subscription`、`models-sub-state-${id}` 徽章）；describe() :295-302；stateVariant :304-308；stateText :310-312；pickSubscription :375-386（usable → 直接 loadOffers 进第 3 步）；startLogin :388-423（complete → loadOffers）；poll/pollOnce :425-463；loadOffers :513-532；provision :534-587；样式 :622-746。

`ModelFormDrawer.vue`：来源卡 :7-60（`models-source-card`、head :9-13、`models-relogin` :27-36、只读字段 :40-53）；sourceRow computed :416；stateLabel :511-522。

`ModelsTable.vue`：cell-key :39-45（loginBacked → `models-cell-account`「账号登录」）。

`ModelsTab.vue`：ConfirmDialog 用法 :77-85（props: open/title/message/confirmLabel/danger，emits confirm/cancel — ConfirmDialog.vue:42-53）；模态挂载 :75；抽屉 `@open-subscribe="relogin"` :61-73；onSubscribed :234-237；useServerList reload :147-150。

`api.ts`：LlmModelRow :1441-1490；SubscriptionRow :1553-1560；ProviderLoginStatus :1607-1618；subscriptions() :4773-4777；startProviderLogin :4839-4851；providerLoginStatus :4856-4861；cancelProviderLogin :4878-4883（logoutSubscription 的写法范本）。

`locales/index.ts`：zh models.* 状态键 :2537-2540、accountLogin :2442、relogin :2533；en 对应 :5110 一带。

### 3.6 测试范本（实读）

- TUI：`setup_test.go` `fakeSubscription` :357-379（probeCalls/startCalls/offersFn/provisionFn/envs 字段模式）；`TestProviderPageMarksASubscriptionAlreadySignedIn` :682（Status=="Signed in"、probeCalls==1、重渲染走缓存）；`TestSubscriptionFlowDropsSettledSignInPage` :781；`TestConfigureSubscriptionReusesTheSavedSignIn` :915；`withSubscriptions(t, sub)`、`setupTestConfig(t, selector)` 同在 `setup_test.go`（:560、:594），**`fakeSelector` 不在这里**——它在 `pkg/tui/chat_session_test.go:14631`（`SelectRich` :14725、`SelectRichTabbed` :14657、`Confirm` :14788 一带），新用例的脚本答案也写在那里。
- gateway：`subscriptions_api_test.go:18` TestSubscriptionsAPIProvision（`modelsPageServer(t)`、`loginStubs(t)`、换 `gatewaySubscriptions` + t.Cleanup、`do(method, path, body...)`、testify require）；`login_api_test.go:265-286` loginStubs/newLoginSubscription。
- 前端：`SubscriptionLoginModal.test.ts`（mountModal + vi.spyOn(forebrainApi, ...) + flushPromises + data-testid 断言，setLocale('zh')）；`ModelFormDrawer.test.ts`、`ModelsTable.test.ts` 同目录。

## 4. 命令与基线（本机已实跑验证）

Go 不在默认 PATH：所有 Go 命令先 `export PATH=$PATH:/usr/local/go/bin`。CGO 必须开，build tag `fts5`。

| 用途 | 命令 | 基线结果（9552c46c，2026-11-24 实跑） |
|---|---|---|
| 订阅包测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/subscription/... -count=1` | ok 0.621s |
| gateway 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... -count=1` | ok 11.676s |
| TUI 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/... -count=1 -timeout 20m` | ok 201.695s |
| 插件测试 | `CGO_ENABLED=1 go test -tags fts5 ./plugins/... -count=1` | 执行器开工时先跑一次留痕 |
| openai/opencode 包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/openai/... ./pkg/llm/opencode/... -count=1` | 执行器开工时先跑一次留痕 |
| 前端类型 | `cd frontend && pnpm typecheck` | exit 0（vue-tsc --noEmit） |
| 前端测试 | `cd frontend && pnpm test` | 195 files / 1586 tests passed（39.19s） |
| 格式门禁 | `gofmt -l <改动的 .go 文件>` | 输出必须为空 |
| 原型回归 | `python3 -m http.server 8765 --directory plans` 后 `node plans/374-account-tui-prototype.test.cjs` / `node plans/374-account-web-prototype.test.cjs` | TUI 14/14、Web 12/12、browserErrors=[]（2026-11-24 实跑，见 §0.1） |

已知**无关**的预存失败：`pkg/architecture TestTestFilesCorrespondToProductionFiles`（`pkg/tui/deliverables_outcome_test.go` 无配对生产文件，自 3ff6b134/80bb73b4 起）。本计划新增生产文件必须配对测试文件（account.go → account_test.go 等），**不得**顺手修这个预存失败。macOS 链接器关于 libtokenizers 版本的 warning 是良性噪音。

## 5. 范围

**In scope（只许改这些）**：

- 新增：`pkg/subscription/account.go` + `account_test.go`
- `pkg/llm/openai/auth.go`（+ auth_test.go）、`pkg/llm/opencode/auth.go`（+ auth_test.go）：ClearStoredCredentials
- `pkg/subscription/chatgpt.go`、`opencode.go`、`volcengine_oauth.go`（+ 各自 _test.go）：Account/Logout
- `plugins/subscriptions/qodercn/plugin.go`（或 auth.go）、`plugins/subscriptions/workbuddy/plugin.go`（或 auth.go）（+ 测试）：Account/Logout
- `pkg/gateway/subscriptions_api.go`、`login_api.go`、`models_api.go`、`api_extra.go`（+ subscriptions_api_test.go、login_api_test.go、models_api_test.go）
- `pkg/tui/setup.go`、`setup_test.go`
- `frontend/src/lib/api.ts`、`frontend/src/locales/index.ts`
- `frontend/src/components/models/SubscriptionLoginModal.vue`（+ .test.ts）、`ModelFormDrawer.vue`（+ .test.ts）、`ModelsTable.vue`（+ .test.ts）、`frontend/src/components/settings/ModelsTab.vue`
- `plans/` 下本计划与原型、脚本、截图、索引

**Out of scope（明确不碰）**：`Subscription` 接口本体与 `Register/Registry`；Probe 四态语义与 signIn 的 usable/unreachable 早退；312 面板状态机（TryRunConnectPanel 归属）；311 默认强度页与 Review 语义；Provision/ProvisionRows/offerRowInput；模型行 schema 与删除逻辑；transport 凭据读取路径；`pkg/process/config_db.go` 的 installCredentialStores；`pkg/state/config_store.go`；其他 Web 页面；`cmd/forebrain/plugins_local_gen.go` 与生成脚本（插件包名不变，无需重生成）；预存的 architecture 测试失败。

## 6. 实施步骤（每步带验证）

### Step 1 — 能力契约（pkg/subscription/account.go）

按 §2.3 逐字创建 account.go（含 MaskID，规则见 §2.1 第 3 条）。创建 account_test.go：MaskID 四档边界（len 13 → `前4…后4`、12 → `…后4`、9 → `…后4`、8 → 只有 `…`、5 → 只有 `…`、4/0 → 原样），逐档断言「返回串里不出现原值的中段」；ErrExternalLogin 的 errors.Is 包装判定。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/subscription/... -count=1` → ok；`gofmt -l pkg/subscription/account.go pkg/subscription/account_test.go` → 空。

### Step 2 — 凭据清除原语（openai / opencode）

两包各加 `ClearStoredCredentials(ctx context.Context, home string) error`：

```go
// openai 版（opencode 版同构，holder 判定照该包 LoadStoredCredentials :209 的写法）
func ClearStoredCredentials(ctx context.Context, home string) error {
	if s := credentialsStore.Load(); s != nil {
		if err := (*s).SaveCredentials(ctx, nil); err != nil {
			return err
		}
	}
	if err := os.Remove(CredentialsPath(home)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
```

语义：store 行删除（nil 写即删，§3.2）+ 文件删除（容忍不存在）。**store 与文件都清**，理由见 §2.2 第 1 条。测试：装 store 时 nil 写到达 store 且文件被删；未装 store 时文件被删；文件不存在不报错；store 失败则返回错误。照各包 auth_test.go 既有 store 安装/清理写法。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/openai/... ./pkg/llm/opencode/... -count=1` → ok；gofmt 空。

### Step 3 — 内置订阅 Account/Logout

**chatgpt.go**：

```go
func (chatGPT) Account(ctx context.Context, env Env) (Account, bool, error) {
	raw, err := openai.LoadStoredCredentials(ctx, env.Home)
	if err != nil || raw == nil {
		return Account{}, false, err
	}
	c, err := openai.LoadProcessCredentials(ctx, openai.CredentialsPath(env.Home))
	if err != nil {
		return Account{}, false, nil // stored but unreadable: no name to show
	}
	name := c.Email
	if name == "" { name = c.AccountID }
	if name == "" { return Account{}, false, nil }
	return Account{Name: name, Detail: c.Plan, SharedPath: sharedCredentialPath(env.Home)}, true, nil
}

// sharedCredentialPath is the login file when it is not this install's own
// auth.json — the Codex CLI's file, which this same login signs in.
func sharedCredentialPath(home string) string {
	path := openai.CredentialsPath(home)
	if filepath.Clean(path) == filepath.Clean(openai.DefaultPath(home)) {
		return ""
	}
	return path
}

func (chatGPT) Logout(ctx context.Context, env Env) error {
	return openai.ClearStoredCredentials(ctx, env.Home)
}
```

（`openai.DefaultPath(home)` = `<home>/auth.json`，见 `auth.go:81`。opencode/qodercn/workbuddy 的凭据路径只在自己的 home 下，`SharedPath` 恒空——不要为它们编造共享路径。）

**opencode.go**：Account 先 `opencode.LoadStoredCredentials`；raw==nil 时查 `strings.TrimSpace(os.Getenv(opencode.APIKeyEnv))`，非空 → `Account{Name: opencode.APIKeyEnv, EnvKey: true}, true, nil`（**`Detail` 留空**，见 §2.1.4），空 → false,nil；raw 非 nil → `opencode.LoadProcessCredentials(ctx, opencode.DefaultPath(env.Home))`，Name=Email||AccountID，Detail=OrgName。Logout：

```go
func (openCodeGo) Logout(ctx context.Context, env Env) error {
	raw, err := opencode.LoadStoredCredentials(ctx, env.Home)
	if err != nil { return err }
	envKey := strings.TrimSpace(os.Getenv(opencode.APIKeyEnv)) != ""
	if raw == nil {
		if envKey {
			return fmt.Errorf("%w: unset %s in the shell that starts Forebrain Harness", ErrExternalLogin, opencode.APIKeyEnv)
		}
		return nil
	}
	if err := opencode.ClearStoredCredentials(ctx, env.Home); err != nil { return err }
	if envKey {
		return fmt.Errorf("%w: the stored login was removed; %s still signs this machine in", ErrExternalLogin, opencode.APIKeyEnv)
	}
	return nil
}
```

**volcengine_oauth.go**：Account 用 `loadVolcengineCredential(ctx, env)`，ok 且 AccessKeyID 非空 → `Account{Name: MaskID(cred.AccessKeyID), Detail: "STS"}, true, nil`。Logout：`env.DB == nil → nil`；否则 `volcengineStore(env).SaveCredential(ctx, volcengineCredentialSection, nil)`。

测试（各包 _test.go，照既有测试的 env/DB 搭建方式）：账号字段映射与回退链；absent → false；volcengine 掩码（全量 AKID 不得出现在返回值）；Logout 后 LoadCredential 为 nil；opencode 三种 env 组合（无存储有 env / 有存储有 env / 有存储无 env）的 ErrExternalLogin 判定；`t.Setenv` 管理环境变量。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/subscription/... -count=1` → ok；gofmt 空。

### Step 4 — 插件 Account/Logout

**qodercn**（方法加在 plugin.go，与 Probe/Offers 并列）：Account 用 `loadCredentials(ctx, env.DB, env.Home)`，Name=Email||Name||UID。Logout：

```go
func (qoderCNSubscription) Logout(ctx context.Context, env subscription.Env) error {
	if env.DB != nil {
		if err := (&state.ConfigStore{DB: env.DB}).SaveCredential(ctx, credentialSection, nil); err != nil {
			return err
		}
	}
	if err := os.Remove(credentialPath(env.Home)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.TrimSpace(os.Getenv(personalTokenEnv)) != "" {
		return fmt.Errorf("%w: unset %s to finish signing out", subscription.ErrExternalLogin, personalTokenEnv)
	}
	if root := QoderHomeDir(); root != "" {
		if fi, err := os.Stat(filepath.Join(root, ".auth", "user")); err == nil && fi.Size() > 0 {
			return fmt.Errorf("%w: the official Qoder CN client is signed in; its login will be imported again", subscription.ErrExternalLogin)
		}
	}
	return nil
}
```

**workbuddy**：Account Name=Name||UID，Detail=AccountType；Logout = 删 DB 行（env.DB!=nil 时）+ 删文件，无外部来源。

测试：照两插件既有测试的 DB/home 搭建；退出后 loadCredentials 返回 false；qodercn PAT 存在 → ErrExternalLogin 且存储已删；官方文件存在（测试自建临时文件）→ ErrExternalLogin。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./plugins/... -count=1` → ok；gofmt 空。

### Step 5 — gateway

1. `subscriptions_api.go`：加 `subscriptionAccount` 结构（§2.6）；`subscriptionRow` 加 Account/CanLogout；加共享助手：

```go
// readSubscriptionAccount reads the stored account locally; a subscription
// without the capability, without a login, or with an unreadable one answers
// nil — the surface shows its state alone.
func readSubscriptionAccount(ctx context.Context, sub subscription.Subscription, env subscription.Env) *subscriptionAccount {
	reader, ok := sub.(subscription.AccountReader)
	if !ok { return nil }
	acct, ok, err := reader.Account(ctx, env)
	if err != nil {
		slog.Debug("subscription account read failed", "id", sub.ID(), "err", err)
		return nil
	}
	if !ok || acct.Name == "" { return nil }
	return &subscriptionAccount{Name: acct.Name, Detail: acct.Detail, EnvKey: acct.EnvKey, SharedPath: acct.SharedPath}
}

func subscriptionCanLogout(sub subscription.Subscription, state string, acct *subscriptionAccount) bool {
	if _, ok := sub.(subscription.LogoutRemover); !ok { return false }
	if state == "" || state == signInStateMissing { return false }
	return acct == nil || !acct.EnvKey
}
```

**ctx 只有一个来源**：`subscriptionStates` 已经持有本请求的 `st.ctx`（`newSubscriptionStates` :102-115 那一段，`of()` :117-139 就是 `ctx == nil → context.Background()` 的写法）——`accountOf` 照 `of()` 的骨架取 `st.ctx`，`handleSubscriptions` / `llmModelRowOf` 里传进来的就是这个 ctx。**不要**在 helper 里写 `context.Background()`，也不要另起一个超时：账号读是本地 DB/文件读，已被请求 ctx 限住。

2. `subscriptionStates` 加账号缓存：`accounts map[string]*subscriptionAccount` + `accountAsked map[string]bool`；新方法 `accountOf(id) *subscriptionAccount`（照 of() 的缓存骨架：先 `gatewaySubscriptionByID` 找不到就不读，读一次，nil 也缓存）。`handleSubscriptions` 填 `Account: states.accountOf(sub.ID())`、`CanLogout: subscriptionCanLogout(sub, states.of(sub.ID()), states.accountOf(sub.ID()))`。
3. `llmModelRow` 加 SourceAccount/SourceCanLogout；`llmModelRowOf` :533-537 块内填（sub 已解析处）。
4. 新 handler `handleSubscriptionLogout`：POST only（其余 405）→ `subscriptionRoute` → LogoutRemover 断言（无 → 404 双语「这个订阅没有可退出的登录」/ "this subscription has no login to remove"）→ `Logout(ctx, s.subscriptionEnv())`（ctx 带 modelsDiscoveryTimeout）→ 错误分派按 §2.2 第 7 条（ErrExternalLogin 用 `errors.Is`，409 + external_login:true，文案用 §2.7 logoutExternal 双语）→ 成功 `w.WriteHeader(http.StatusNoContent)`。**不复用 `failSubscriptionWrite`**：它的兜底文案是「写入失败，请重试」，退出失败说这句话是错的；只复用 `writeJSONStatus` 与 `llmText`。
5. `api_extra.go`：`api.Post("/subscriptions/:id/logout", s.handleSubscriptionLogout)` 加在 :444-446 旁。
6. `login_api.go`：`handleAuthLoginStart` 的探测+立即完成块包进 `if r.URL.Query().Get("force") != "1" { ... }`；立即完成响应加 `"account": readSubscriptionAccount(r.Context(), sub, s.subscriptionEnv())` 与 `"can_logout"`（这里的 `check` 就在手边，用 `subscriptionCanLogout(sub, signInStateName(check.State), acct)` 算，不额外探测）。**完成态不要在 `authLoginFinish` 里读账号**：那个函数整段持有 `s.authLoginMu`（`login_api.go:134-152`），在里面做 DB 读（更不用说探测）会在锁上等 I/O。账号与 `can_logout` 改为在 `handleAuthLoginStatus` 里填：`authLoginGet` 返回的是副本、锁已释放，`login.status == authLoginComplete && login.errText == ""` 时用 `readSubscriptionAccount(r.Context(), sub, s.subscriptionEnv())` 本地读一次，`can_logout = 实现 LogoutRemover ∧ ¬EnvKey`（刚完成的登录一定 settled，不必再探测）；`authLoginStatusBody` 加 Account/CanLogout 两个字段（照 §2.6）。
7. 测试：`subscriptions_api_test.go` 照 TestSubscriptionsAPIProvision 骨架加 TestSubscriptionsAPIAccountAndLogout（stub 订阅实现 AccountReader/LogoutRemover：list 带 account/can_logout；logout 204 后 stub 被调；ErrExternalLogin → 409 + external_login；无能力 → 404；未知 id → 404）；`login_api_test.go` 加 force=1 跳过立即完成（usable stub 仍启动流程）、complete 响应带 account；`models_api_test.go` 加行带 source_account/source_can_logout。stub 扩展照 loginStubs/newLoginSubscription 既有模式（加 accountFn/logoutFn 字段与条件方法）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... -count=1` → ok；gofmt 空。

### Step 6 — TUI 账号页

按 §2.4 全部条目实施：

1. 常量块加 `setupPageAccount`（放在 setupPageOffers 行后）；`var errSignedOut = errors.New("signed out")` 放在既有 `ErrCancelled` 旁边。
2. `llmSetup` 加 `accounts map[string]subscription.Account`、`accountsAsked map[string]bool`；加 `accountFor(sub)`（缓存式本地读，AccountReader 断言，err/!ok/Name=="" → false）与 `forgetSubscription(id)`。
3. `providerRows`：check.State != missing 时读账号填 Description（`Name`，`Detail` 非空时 `Name (Detail)`；**EnvKey 账号的 Detail 恒空**，行只有变量名）；订阅行**不再填 Status**（owner 裁决删 Signed in）。`llmSetup.probes` 上方那条注释（`setup.go:453-457`，写着「the first page's «Signed in»」）随之改写——删掉的东西不留旧说法（owner「死件必删」）。
4. `flowPages`：探测 ≠ missing 的分支（usable/unreachable/**dead**）插入 setupPageAccount（Provider 后）；missing 仍走 `Provider → Credentials → Offers → …`。实现上加一个 `subscriptionHasLogin()`（`probes[id].State != missing`，未探测过算 false），别再用 `subscriptionSettled()` 判页——settled 只剩「能不能直接进 Offers」这一个含义。
5. `signIn` 拆出 `runSignInFlow`（门后逻辑原样移动，不改一字语义）。
6. 新页 `pickAccount()`：for 循环 + selectRichTabbed（DefaultIdx 0），选项与分支按 §2.4 第 3 条——usable/unreachable 是「继续 / 换号 / 退出」，**dead 是「Sign in again / 退出」**（`Continue` 与单独换号项都不给）。退出 Confirm 文案：`"Sign out of " + label + "?\nThis removes the login saved on this machine."` ＋（`accountFor(sub).SharedPath != ""` 时）`" It is the same file the Codex CLI signs in with (" + SharedPath + "), so the Codex CLI is signed out too."` ＋ `" The models it added stay, but cannot call until you sign in again."`；ErrExternalLogin → forget + 重探 + statusOutput 打印 `"The saved login was removed. " + err.Error()` + logPromptChoice `llmsetup.signed_out_external` + continue；成功 → forget + logPromptChoice `llmsetup.signed_out` + 返回 `false, errSignedOut`。换号/重登成功后若重探仍不是 settled（新登录立刻失效），回账号页重选，**不要**带着空 offers 进 Offers 页（那儿只会以一个 fetch 错误结束向导）。
7. `run()` 加 `case setupPageAccount`：`forward, err = s.pickAccount()`；`errors.Is(err, errSignedOut)` → `s.page, s.back = setupPageProvider, true; continue`（§2.4 第 4 条）。**不要**用 `!s.subscriptionSettled()` 反推退出——dead 本来就不 settled。
8. 测试（setup_test.go）：fakeSubscription 加 `account subscription.Account`、`hasAccount bool`、`accountErr error`、`logoutErr error`、`logoutCalls int` 字段与 Account/Logout 方法；无能力降级测试用 `struct{ subscription.Subscription }` 包装（只暴露基础接口）。新增：
   - TestProviderPageShowsTheAccountBesideTheSignIn（Description 含邮箱；无账号 fake 行不变；probeCalls 不增）
   - TestFlowInsertsTheAccountPageForAnyStoredLogin（usable/unreachable/dead 三态都得到账号页，照 TestSubscriptionFlowDropsSettledSignInPage）
   - TestAccountPageContinueReachesTheOffers（selects 选 0 → offers 页）
   - TestAccountPageSignOutRemovesTheLoginAndReturnsToProvider（confirms=true → logoutCalls==1、probes 缓存被清、回到 provider 页、**向导不退出**）
   - TestAccountPageSignOutDeclinedKeepsTheLogin（confirms=false → logoutCalls==0、仍在账号页）
   - TestAccountPageSwitchAccountRunsTheFlowAgain（选 1 → startCalls==1；成功后 offers 用新 listing）
   - TestAccountPageEnvKeyOffersNoSignOut（EnvKey 账号 → 无 Sign out 项）
   - TestAccountPageDeadOffersSignInAgainAndSignOut（dead → 只有 `Sign in again`/`Sign out`，无 Continue；`Sign in again` 让 startCalls 增一）
   - TestAccountPageSharedCredentialNamesTheFile（`account.SharedPath` 非空 → Confirm 文案含该路径与 "the Codex CLI is signed out too"）
   - TestAccountPageExternalLoginExplains（logoutErr=ErrExternalLogin 包装 → 不退出向导、缓存已清）

9. **既有测试迁移（复核补；不做这一条 Step 6 的 Verify 必红）**：有存储登录的订阅多了一页，`pkg/tui` 里从 `setupPageCredentials` 起步的既有用例会先落到账号页——`fakeSelector.SelectRich`（`chat_session_test.go:14725`）按顺序消费脚本，`scriptedItem` 对不上就返回 `no item %q` 错误，于是下一问全错位。一起改这几处，其余用例不动：
   - `runSubscriptionSetup`（`setup_test.go:602-609`）与 `startConnectSetup`（:2501-2513）：按订阅状态选起始页——usable/unreachable 从 `setupPageOffers` 起（账号页有它自己的测试，不必每个用例陪跑）、**dead 从 `setupPageAccount` 起**、missing/未探测仍从 `setupPageCredentials`。37 处 `runSubscriptionSetup` 调用点里，只有 dead 那一条要改脚本（见下一条）。
   - `TestSubscriptionFlowDropsSettledSignInPage`（:781-806）：`want` 从「按 settled 与否二选一」改成三态——usable/unreachable/**dead 都是 `[Provider, Account, Offers, Effort, Subagents, Review]`**，missing 与未探测才是 Credentials 那一串（:792 的 `a dead login` 行随之翻转）。
   - `TestConfigureSubscriptionDeadLoginRunsTheFlow`（:976-990）：起点变成账号页，`selects` 前面补一条 `Sign in again`（`[]string{"Sign in again", "gpt-future", reasoningEffortUnset, subagentsNo}`），`confirms` 与 `startCalls == 1` 的断言不变——它现在验证的正是「dead 在账号页重新登录」。
   - `TestEscOnOffersPageReturnsToProviderPage`（:811-830）：Esc 在 Offers 上现在先回账号页、再回供应商页——断言改成两次 Esc 的落点，或从 Offers 起步只验第二步。
   - `TestProviderPageMarksASubscriptionAlreadySignedIn`（:682-697）：不再断言 `Status == "Signed in"`，改为「行上没有 Status，账号名在 Description」，「probe 只问一次」的断言保留；「无账号 fake 行不变」并进新的 `TestProviderPageShowsTheAccountBesideTheSignIn`，**不要**留一个断言空 Status 的空壳测试。
   - 三个 connect 面板用例（`TestConnectSetupPanelSignedInLoginSkipsTheSignIn` :3280、`TestConnectSetupPanelFetchStaysInThePanel` :2765、`TestConnectSetupPanelFetchRetryReusesTheLogin` :3312）从 Offers 起步后不再经过账号页，「模型页直接跟在探测后面、不画登录页」这类注释与断言要改述成「起始页就是模型页」，别让文字继续描述一条测试并不经过的路径。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/... -count=1 -timeout 20m` → ok（约 200s）；gofmt 空。

### Step 7 — 前端 API 层与文案

api.ts 按 §2.6 加类型与函数（logoutSubscription 照 cancelProviderLogin 写法；startProviderLogin 加 opts?.force → `?force=1`，返回类型加 account?/canLogout?）。locales 按 §2.7 加 zh/en 键（放在既有 models.state* 键旁，保持分组注释风格）。

**Verify**: `cd frontend && pnpm typecheck` → exit 0。

### Step 8 — SubscriptionLoginModal

按 §2.5 第 1–4 条：第 1 步账号副行（data-testid `models-sub-account-${id}`）；第 3 步账号条（`models-login-account`）+ 退出按钮（`models-logout`）+ 换号按钮（`models-switch-account`）+ envKey 提示；ConfirmDialog（import 自 `@/components/common/ConfirmDialog.vue`，props 见 §3.5），**message 在 `current.account.sharedPath` 非空时追加 `models.logoutShared`（`{path}` 插值）**；`logoutOpen`/`notice` ref；doLogout 成功/409/失败三分支；switchAccount → cancelRunning + startLogin(force)；poll/start 的 complete 分支用响应里的 account/canLogout 更新 current；emits 加 `changed: []`。样式照模态既有 `.auth`/`.subr` 令牌（bg-alt、divider、11px 14px 内边距），danger 按钮用 danger 色。

测试（SubscriptionLoginModal.test.ts 追加，照既有 mountModal/vi.spyOn 模式）：
- 第 1 步显示账号名（subscriptions() fixture 加 account/canLogout）
- usable 行点击 → 第 3 步账号条含账号名与退出按钮
- 退出：confirm → logoutSubscription 被调、emit changed、回第 1 步
- 409 → 显示 logoutExternal 文案（读 `GatewayHttpError.payload.externalLogin`，不是文本匹配）
- envKey 行 → 无退出按钮、有提示
- 换号 → startProviderLogin 收到 force
- 账号带 `sharedPath` → 确认框出现共用路径与 Codex CLI 字样

**Verify**: `cd frontend && pnpm test -- SubscriptionLoginModal` → 全过；`pnpm typecheck` → 0。

### Step 9 — 抽屉 / 表格 / ModelsTab

ModelFormDrawer：来源卡账号行（`models-source-account`）+ 退出按钮（`models-source-logout`，v-if sourceCanLogout）+ ConfirmDialog（`sourceAccount.sharedPath` 非空时同样追加 `models.logoutShared`）+ emits `changed`。ModelsTable：cell-key loginBacked 分支追加账号名（`models-cell-account-name`）。ModelsTab：模态与抽屉 `@changed="onChanged"`。

**抽屉必须跟着刷新**：`drawerRow` 是一个快照 ref——`ModelFormDrawer` 收到的 `:row="drawerRow"` 是 `openEdit` 那一刻的行对象（`ModelsTab.vue:162`、`:211-214`），`reload()`（:147-150）只换 `rows`，快照不动，于是「退出后抽屉不关、徽章翻未登录」（§2.5.5、§0.1 Web 实测项）做不到。`onChanged` 要写成：

```ts
async function onChanged(): Promise<void> {
  const openId = drawerOpen.value ? drawerRow.value?.id : undefined
  await reload()
  if (openId) drawerRow.value = rows.value.find((r) => r.id === openId) ?? drawerRow.value
}
```

（等价做法是把抽屉的 row 改成按 id 从 `rows` 取的 computed；择一，别两个都加。模态不需要这一步：它自己重读订阅列表。）

测试：ModelFormDrawer.test.ts（账号行渲染、退出 confirm 调 API 并 emit）、ModelsTable.test.ts（loginBacked+sourceAccount 行显示账号名；无 sourceAccount 不显示）。

**Verify**: `cd frontend && pnpm test` → 全绿（基线 1586 + 新增）；`pnpm typecheck` → 0。

### Step 10 — 全量验证与收尾

1. `CGO_ENABLED=1 go test -tags fts5 ./pkg/subscription/... ./pkg/gateway/... ./pkg/tui/... ./pkg/llm/openai/... ./pkg/llm/opencode/... ./plugins/... -count=1 -timeout 20m` → 全 ok。
2. `gofmt -l` 全部改动 .go → 空。
3. `cd frontend && pnpm typecheck && pnpm test` → 全绿。
4. 原型闸门复跑（§0 哈希核对 + 两个 .test.cjs）。
5. 更新 plans/README.md 本行 Status。

## 7. 测试计划汇总

| 层 | 文件 | 范本 | 新增覆盖 |
|---|---|---|---|
| 契约 | pkg/subscription/account_test.go | registry_test.go 风格 | MaskID 边界、ErrExternalLogin 包装 |
| 凭据 | openai/opencode auth_test.go | 各包既有 store 测试 | Clear 双删、store 失败、NotExist |
| 内置 | chatgpt/opencode/volcengine _test.go | 各包既有 probe 测试 | 身份映射/回退、掩码、Logout、env 三组合 |
| 插件 | qodercn/workbuddy 测试 | 既有 DB/home 搭建 | 双删、PAT/官方文件 → ErrExternalLogin |
| gateway | subscriptions_api_test.go / login_api_test.go / models_api_test.go | TestSubscriptionsAPIProvision、loginStubs | account 字段、logout 204/409/404、force、complete 带账号、行 source_account |
| TUI | setup_test.go（脚本答案在 chat_session_test.go 的 fakeSelector） | TestProviderPageMarksASubscriptionAlreadySignedIn 等 | §6 Step 6 八个新测试＋第 9 条既有用例迁移 |
| 前端 | 三个 .test.ts | SubscriptionLoginModal.test.ts | §6 Step 8/9 列表 |

## 8. Done criteria（机器可查）

1. §6 Step 10 四条命令全绿（architecture 预存失败除外，见 §4）。
2. `grep -n "Logout\|Account" pkg/subscription/registry.go` 不得显示 Subscription 接口本体被加方法（能力是独立接口）。
3. `grep -rn "AccessKeyID" pkg/gateway/ frontend/src/` 不得出现在任何显示/序列化路径（只有掩码后的 Name）。
4. TUI：有存储登录（usable/unreachable/dead）的订阅进 /connect 必经过账号页；退出确认后回供应商列表且向导存活；dead 的账号页只有 `Sign in again`/`Sign out`；`go test ./pkg/tui/...` 中新增十个测试全过。
5. Web：`GET /api/subscriptions` 响应含 account/can_logout；`POST /api/subscriptions/:id/logout` 返回 204；模态/抽屉/表格三处账号可见；退出有 ConfirmDialog。
6. 退出后模型行仍在（gateway 测试断言行数不变），source_state 翻 missing。
7. 原型 SHA-256 与 §0 一致，两个 .test.cjs 全过。
8. 全部新增 .go 文件有配对 _test.go（architecture 对应规则）。
9. `pkg/tui` 全包绿：既有用例按 Step 6 第 9 条迁移后无一被删除或改成空断言（`git diff` 里 `setup_test.go` 的删除行只能是迁移掉的旧断言，且每条都有对应的新断言）。
10. §0.2 三项裁决已落进 §2 契约与两份原型（dead 进账号页、env Key 的 `Detail` 留空、共享凭据文件写明路径），原型哈希与 §0 一致；未落进代码即不算完成。

## 9. STOP 条件（命中即停，报告而非即兴发挥）

1. §0 哈希不符或 owner 未确认原型。
2. drift check 显示 in-scope 文件已变且摘录对不上（按符号重定位后仍矛盾）。
3. ~~发现 transport 缓存凭据~~ —— 复核已证否，不必为此 STOP：两个 transport 每次请求重读（§3.2「退出是否即时生效」）。若实现过程中有人为了省读而**新加**进程内凭据缓存，STOP 报告，不得自行加缓存失效广播。
4. 发现 `SaveCredential(ctx, section, nil)` 不再是删除语义（§3.2 摘录失效）。
5. qodercn/workbuddy 之外又出现新的外部导入路径（Probe 会复活登录）而本文未覆盖。
6. 任何步骤诱使你去改 out-of-scope 文件（尤其 Subscription 接口本体、312 面板状态机、模型行删除逻辑）。
7. 前端测试基线（1586）在开工前就不绿。

## 10. 维护注记

1. **新订阅插件**：实现 AccountReader/LogoutRemover 即自动获得两个 surface 的账号与退出；不实现则降级为状态徽章。插件凭据若同时写 DB 与文件，Logout 必须两处都删（qodercn/workbuddy 是范本）。
2. **chatgpt 共享文件**：`credentials.chatgpt` 指向 Codex CLI 的 auth.json 时，退出会删除共享文件（§2.2 第 5 条）。若将来要「只退出 forebrain、保留 Codex」，需要引入 tombstone 或 per-surface 凭据隔离——那是另一个计划，不要在本计划里即兴加。
3. **qodercn 官方导入**：退出后若官方客户端仍登录，下次 Probe 会重新导入（ErrExternalLogin 已提示）。若产品要「退出后禁止再导入」，需要持久化 opt-out 标记，另立计划。
4. **账号缓存**：gateway 每请求缓存（subscriptionStates）、TUI 每 setup 缓存（accounts map）。凭据在别处被刷新/删除后，同一请求/同一次 setup 内不会重读——与 probes 缓存同寿命，可接受；跨请求自然刷新。
5. **force 登录**：`?force=1` 只被模态换号使用；不要把它接进 TUI（TUI 本地直接调 runSignInFlow）。
6. **评审关注点**：退出确认文案是否明示「模型保留但不可调用」、凭据文件被别的客户端共用时是否写出该路径；任何 surface 是否泄露全量 AccessKeyID/ token；退出后是否误删模型行或误调 adoptLLMModelWrite；账号页 Esc 是否回供应商列表而非退出向导；dead 的账号页是否没有 Continue。
7. **列表同形的后果（owner 2026-11-24 裁决接受）**：删掉 Signed in 后，usable 与 dead 的订阅行在供应商列表里同形（标签＋存储账号名），登录是否有效要到进入后的账号页/登录页才分辨；dead 场景的原型说明已如实写明。若将来产品要在列表区分失效登录，应另立计划加非冗余标记（如仅 dead 行一个警告词），不要在本计划里即兴加回 Signed in。
8. **dead 的退出入口（owner 2026-11-24 裁决）**：TUI 是账号页（`Sign in again`/`Sign out`），Web 是模型行的来源卡（模态对 dead 直接进登录流程，没有账号条可停）——两处是同一语义的两种呈现。改任何一面都要问另一面还成不成立。
9. **抽屉的 row 是快照**（Step 9）：`ModelsTab` 的抽屉现在按 id 重新指向刷新后的行；以后若改成别的数据源，这个重指要跟着改，否则退出/保存后抽屉停在旧行。
10. **env Key 的显示**（§0.2 第 2 条）：`Detail` 不放环境说明词，env 语义只有 `EnvKey` 一处；新 surface 渲染 env 提示时读 `EnvKey`，不要从 `Detail` 猜。
11. **共享凭据文件**（§0.2 第 3 条）：`Account.SharedPath` 只有 chatgpt 会置；`models.logoutShared` 与 TUI 确认文案是按「Codex CLI 的 auth.json」这一个来源写的。将来若出现第二个共享文件，先把文案参数化（app 名也要成为数据），别在两个 surface 里各写一套判断。