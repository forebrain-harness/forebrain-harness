# Plan 312: /connect 登录、等待与报告内容统一留在命令面板

> **2026-11-16 范围升级**：用户追加「其他同类问题必须一并修复，写入本期计划」。本版取代最初只修设备码等待页的方案；不能只修 Ark 后宣称完成。必须覆盖本文件的全部已确认问题与矩阵，不拆成未授权的后续计划。
>
> Advisor 仅调查及写计划，未修改源码。执行器须逐步实施、记录验证，触及 STOP 条件时报告，不自行扩大范围。

## Status

- **Status**: DONE（2026-11-16 `execute`：一轮修订 + 一轮 flaky 修复后 APPROVE；**2026-11-17 owner 裁决「主树所有改动都必须提交，进行 2-6」→ 已合并 dev@`813bdd83`**：worktree 内 commit → rebase dev（f437abaa）无冲突 → 新基线门禁全绿 → 主树 ff-only → worktree/分支按常设规则删除。执行与合并记录见 §12/§12.1）
- **Priority**: P1
- **Category**: Bug / Test coverage
- **Confidence**: HIGH（逐项读取生产链路、原型和现有测试）
- **Effort**: M–L（约 3–4 天，包含三种登录流、回调编辑/取消、取模型状态、报告错误及回归）
- **Risk**: MED（终端输入所有权、异步提交与取消；协议和持久化不变）
- **Depends on**: 无未完成技术前置。与 311 共改 `pkg/tui/setup.go` / `setup_test.go`，必须串行；建议 312 → 311。若 311 已开工，待其完成后核对漂移，保留其模型/默认强度页序。
- **Planned at**: `7611d6cc`，2026-11-16，分支 `dev`；扩围调查后再次核对 HEAD 不变，`git status --short` 无输出。
- **交付范围**: 一份计划内完成所有已确认的 `/connect` 同源缺陷；本次不是全仓审计，不包含 Web 或真实账户验证。

## 1. Why this matters

用户截图中 Volcengine Ark 的登录 URL 和 user code 出现在主聊天区，而非 `/connect` 面板。根因不是 ANSI 样式，而是认证阶段把信息交给了 transcript writer；后续等待只接管键鼠，没有创建 overlay。相同路径被 ChatGPT、本机/远程浏览器登录、OpenCode 和无验证码插件共用；取模型提示与部分错误降级也走同一错误容器。

目标是统一不变量：**交互式 `/connect` 中，完成配置前的说明、等待、回调输入和可恢复错误留在当前命令面板；最终成功摘要才可以进入正常会话输出。** 不能通过删除文字、隐藏 URL、取消复制或硬编码 Ark 特例来达成。

## 2. 已确认问题清单（本期全部必修）

| ID | 问题 / 影响 | 证据（均在本轮亲读核实） | 努力 / 修复风险 / 置信度 |
|---|---|---|---|
| F1 | 登录 URL/code 直接写 retained transcript，标题丢失 | `pkg/tui/setup.go:1157-1176,1646-1652,2243-2246`；`pkg/tui/render.go:726-735,7330-7334` | M / MED / HIGH |
| F2 | 所有登录流的等待只暂停输入，不创建面板；等待器参数只有 done，没有展示状态 | `pkg/tui/channels.go:733-745`；`pkg/tui/overlays.go:3943-4001` | M / MED / HIGH |
| F3 | ChatGPT browser/paste、OpenCode device、无 code 插件 browser/device 都受 F1/F2 影响 | `pkg/subscription/chatgpt.go:42,55-70`；`opencode.go:48,60-79`；`plugins/subscriptions/qodercn/plugin.go:67-69,105-151`；`workbuddy/plugin.go:178-180,204-248` | 与 F1/F2 合修 / MED / HIGH |
| F4 | 粘贴回调的说明 URL 在聊天区、输入另起面板，Submit 错误又写回聊天区；输入结束后等待面板消失 | `pkg/tui/setup.go:1179-1197,1219-1232`；`pkg/tui/overlays.go:5438-5447,5480-5483` | M / MED / HIGH |
| F5 | 粘贴页 Esc 返回 `ok=false,err=nil`，helper 当作 nil 成功，随后继续 Wait；遇到实际错误时调用可能为 nil 的 `end()` | `setup.go:1222-1224,1180-1182,1626-1631`；`overlays.go:5482-5483`；`render.go:7263-7267` | S / LOW / HIGH |
| F6 | Ark 到期时间不在 Login 中；URL/code 分两次发布；旧先等 URL、再读一次页面会错过晚到字段，并阻塞准备阶段 Esc | `registry.go:55-60,255-266,293-321`；`volcengine.go:135-141`；`volcengine_oauth.go:215`；`setup.go:1334-1350` | S–M / MED / HIGH |
| F7 | 每次拉取账户模型都把 `Fetching this account's available models…` 写主视图，重试继续堆积 | `setup.go:1555-1575`；与 F1 共用 statusOutput | S / LOW / HIGH |
| F8 | `note` 的 InfoOverlay 出错后降级打印 transcript，错误被吞；影响连接测试报告、模型选择提示、Provision/授权保存失败报告 | `setup.go:1105-1115`；调用点 `:1088,1092,1394,1424,1443` | S / LOW / HIGH |
| F9 | 无 user code 的插件登录已在正文避免 code，但确认/等待文案仍按 device flow 声称有 code | `setup.go:1282-1283,1289-1293,1302-1312`；插件 UserCode 恒空，见 F3 | S / LOW / HIGH |
| F10 | 旧回归固定了错误容器：setup 测试要求 URL/code 留输出，复制 fixture 先手工 seed FrameRaw | `setup_test.go:1008-1030,2165-2195`；`overlays_test.go:3599-3623,3692-3734` | S / LOW / HIGH |

F1–F10 是同一条配置链路的问题，不是十份独立需求。修复入口必须按 selector 能力和 flow 类型统一接线，禁止仅按 provider ID 修 Ark。

### 2.1 根因调用链与漂移锚点

`run.go:1335-1337` 的 `/connect` → `handleConnect` → `runConnect`，后者把 `renderer.OutputWriter()` 传给 `PromptConfig.Output`。确认页返回时会 EndOverlay；随后：

```go
// pkg/tui/setup.go:1157
status := s.statusOutput("Sign in with " + label)
```

```go
// pkg/tui/setup.go:1161-1166
end := s.startConnectProgress(func() string { return signInWaitingText(label, flow) })
address := awaitSignInURL(ctx, login)
if status != nil {
    if page := signInPage(login, address, flow); page != "" {
        fmt.Fprintln(status, page)
    }
}
```

```go
// pkg/tui/render.go:7330-7334
func (s *rawSelector) StatusWriter(title string) io.Writer {
    if !s.standalone() {
        return nil
    }
    return &pageStatusWriter{sel: s, title: title}
}
```

```go
// pkg/tui/setup.go:1646-1652
func (s *llmSetup) statusOutput(title string) io.Writer {
    if sw, ok := s.cfg.Selector.(setupStatusWriter); ok {
        if w := sw.StatusWriter(title); w != nil {
            return w
        }
    }
    return s.cfg.Output
}
```

```go
// pkg/tui/render.go:732-735
if r.viewportMode {
    r.seedRawViewportLocked(string(p))
    return len(p), nil
}
```

因此文字进入主视图的保留块。此处未证明写入数据库聊天历史或发送给 LLM，不夸大为已确认的数据持久化/外传问题。

第二条同源链：

```go
// pkg/tui/setup.go:1557-1563
status := s.statusOutput("Sign in with " + label)
for {
    fetchCtx, cancel := context.WithTimeout(s.ctx(), modelsDiscoveryTimeout)
    if status != nil {
        fmt.Fprintln(status, "Fetching this account's available models…")
    }
    offers, err := sub.Offers(fetchCtx, s.signInEnv())
```

粘贴取消与错误出口：

```go
// pkg/tui/setup.go:1222-1229
pasted, ok, err := s.cfg.Selector.Input(title, "")
if err != nil || !ok {
    return err
}
if err := login.Submit(ctx, strings.TrimSpace(pasted)); err != nil {
    if status != nil {
        fmt.Fprintln(status, err.Error())
    }
```

```go
// pkg/tui/setup.go:1179-1183
if flow == subscription.FlowPasteCallback {
    if err := s.pasteSignInCallback(ctx, login, status); err != nil {
        end()
        return false, err
    }
}
```

`startConnectProgress` 在 selector 不支持时返回 nil，而上述 `end()` 没有保护；这是可从当前代码直接确认的错误路径，不是猜测网络问题。

## 3. 原型与行为契约

### 3.1 原型基准

`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html`：

- `:1118-1127`，`sub-volcengine`：本地自动开浏览器，URL/code/倒计时同面板，无粘码框，完成自动推进。
- `:1140-1146`，`sub-volcengine-headless`：在任意设备打开 URL，code/倒计时同面板，仍非粘码流程。
- `:1086-1095`，`sub-chatgpt`：同样有独立 browser_callback 等待面板。
- `:1107-1116`，`sub-opencode`：URL、明文 user code、等待和取消提示组成面板。说明文字虽提 ConnectProgress，实际 panel 结构已明确；复用 spinner 的视觉语言，不再额外在聊天区安装同内容全局瞬态行。
- `:476-484`，`overlayScreen`：保留 transcript → 分隔线 → panel → composer，不是 FrameRaw 卡片。
- sha256：`8f169c3e9a90650d43fc3b04f0489ee5fa33a137b79fce148e924703518ec197`。实施前后核对不变。

静态示例里的 code、URL、9:41、回环端口不是运行时常量。ChatGPT 未暴露可靠 callback 地址时显示通用等待文字，不抄原型端口。macOS 没有 DISPLAY 也可以打开浏览器，沿用 `hasLocalBrowser` 的实际平台判断，不照抄原型说明中的简化条件。

只有等待/输入/报告容器纳入本计划。原型后续的多模型选择、Key 池等不能用于回滚现有业务或 311 的已选方案。原型脚手架和静态场景导航不进入产品。

### 3.2 所有登录流的面板归属

| 阶段 | 必须显示 | 输入与推进 |
|---|---|---|
| 准备授权信息 | `Sign in with <label>`、`Preparing sign-in…`，不显示空 URL/code | 立即可取消，不先堵在 awaitSignInURL；backend 失败可立即退出 |
| Ark 本地 device | `Opening your browser…`、`If it did not open, visit: <URL>`、`Code:  <code>`、`Waiting for you to confirm in the browser · expires in <m:ss>` | 无输入框；`Enter when signed in · Esc to cancel`；Wait 成功自动推进 |
| Ark headless device | `Open this URL on any device: <URL>`、code、`Waiting for you to confirm · expires in <m:ss>` | 无输入框，任意设备确认，规则同上 |
| OpenCode device | 动态授权 URL、非空 user code、等待文字，保留原型的直接操作说明 | 无输入框；没有已公开 expiry 则不猜倒计时；同样只由 Wait 成功推进 |
| ChatGPT browser | 标题、打开浏览器状态、可复制 fallback URL、等待回调说明 | 无输入框，无虚构 code/expiry/端口；自动完成 |
| ChatGPT paste | 标题、授权 URL、`Sign in, then paste the address your browser ended on.`、同面板 callback 字段和提交状态/错误 | Enter 提交 callback，Submit 成功仅进入收尾等待；Wait 成功才完成；Esc 始终取消 |
| 无 code 的 browser/device 插件 | 标题、URL、确认等待，不出现 `Code:`、`enter this code` 或空码框 | browser/device 行为由 flow 决定；不为插件改协议或借用他者账户 |
| 拉取已登录账户模型 | `<label>`、`Fetching this account's available models…`，命令面板内状态 | 自动转列表；失败转现有 Retry 面板；Esc 取消本次请求，不重新登录 |
| 配置内报告 | 原有 InfoOverlay 标题与正文 | 关闭/确认语义不变；实际渲染/输入错误向调用者返回，不再降级聊天文本 |

获取 URL/code 前，device 确认文案统一使用不预言 code 的 `opens the sign-in page for you to confirm`。拿到非空 code 后才写需确认 code 的等待说明，修掉 F9；不是把所有 device 流变成粘码流。

### 3.3 不变量

1. 支持面板的 selector：认证 URL、user code、输入 callback、可恢复 submit 错误和 fetch 状态均不得写 `cfg.Output` / FrameRaw。等待期间不能同时安装 `transientSourceConnect`。
2. 每次登录只有一个 `Login.Wait`、一个底层 flow；浏览器最多打开一次；URL/code 晚到时原地更新。browser/paste/device 三种都走新能力，不再给 ChatGPT 留旧 writer 分支。
3. 非 paste 页 Enter 不能伪造成功、不调用 Submit/Offers/Provision、不额外发 token 请求。paste 页 Enter 只提交当前输入，不能将 `Submit==nil` 当 `Wait==nil`。
4. 取消、后台结果、ctx 结束、终端 EOF、渲染/输入错误都释放当前 panel 与共享输入。不得新增不可取消的 stdin 读取 goroutine。
5. Submit 可阻塞，因此在有界结果 channel 的后台调用中执行，同一时间最多一份。UI 同时看 done/context/取消；退出时 cancel submit context，迟到结果不得卡住发送者或更新后续面板。
6. Ark deadline 与实际 poll 同源；倒计时 `max(0, ceil(expiresAt-now))`，显示 m:ss，重绘不续期。到零只展示，不由 UI 自行宣布认证成功或重启 OAuth。
7. 不展示/记录 device token、STS、refresh token、API key；callback URL 可含短期授权码，仅在输入字段中存在，提交完成/退出后清掉，不回显进错误和 transcript。新测试只用 synthetic 数据。
8. 登录已保存后取模型失败，不删除登录、不重开浏览器。取消与成功发生竞态时不承诺撤销已经完成的凭据持久化；未成功不得提前调用 Offers/Provision。
9. 回调/等待页内刷新保留滚动、文本选择、光标与编辑内容。复制 code/URL 的能力不能为迁移容器而退化。

### 3.4 环境边界

- 本次完整交互路径：已有 readiness polling 的 macOS/Linux raw viewport（含 SSH），以及共享 setup 的 Unix standalone raw pages。standalone 复用同一内容/状态机，画到 `paintPage`，不是伪造一个聊天 viewport。
- 纯文本 selector 不具备面板能力，文本是其正常输出，不应禁止输出；仍须修 F5 的取消/空函数错误以及 F9 的无 code 文案。
- Windows 的 `canGateTerminalReads=false` 是既有终端驱动限制（`platform_windows.go:20-31`；`overlays.go:3943-3950`），不是额外 provider 特例。保持现有可用 fallback，不用 `go readKey()` 或把常量改 true 冒充完成。**本计划不能宣称 Windows 已完成新异步面板对齐**；若验收要求该平台完整交互，必须报告这个未完成项并扩充安全输入方案后才能作全平台完成裁决。
- 上述边界不是将 ChatGPT/paste/plugin/fetch 等已确认同类问题延期的许可：它们在支持的新面板路径全部是本期必需项。

## 4. 实现形状（尽量小的共享能力）

### 4.1 可选设备展示快照

保留 `subscription.Login` 的必需方法，不强迫插件升级接口。在 `registry.go` 新增：

```go
type DeviceCodeInfo struct {
    AuthorizeURL string
    UserCode     string
    ExpiresAt    time.Time
}

type DeviceCodeLogin interface {
    DeviceCodeInfo() DeviceCodeInfo
}
```

内部 login 在已有 mu 下一次读写完整快照；添加一次 lock 发布 URL/code/expiry 的 setter。Ark 收到 device 后用 `signIn.now()` 计算一次 deadline，既发布它又传入私有 `poll(ctx, device, deadline)`，删掉 poll 内独立重算，协议/backoff/存储不变。调整 `volcengine_oauth_test.go:153,171,176` 三个直接调用。

OpenCode 在 `pkg/subscription/opencode.go:71-74` 同样改为原子发布 URL/code，expiry 为零。`pkg/llm/opencode/auth.go:169-172` 的 LoginInfo 确实没有 expiry；不把 access-token ExpiresAt 偷换成 user-code 有效期，也不为本次 UI 需求改底层 OAuth 公共 API。其他已有 setters 保留给真实调用方；外部插件不实现快照时仍读取现有两个 getter。

### 4.2 通用但局限于 /connect 的动态面板接口

在 `pkg/tui/channels.go` 可选 selector 能力旁新增（替代早期草案中仅供 device-code 的接口）：

```go
type ConnectPanelPage struct {
    Title      string
    Body       string
    Hint       string
    InputLabel string
}

type ConnectPanelSelector interface {
    TryRunConnectPanel(
        ctx context.Context,
        page func() ConnectPanelPage,
        done <-chan error,
        submit func(context.Context, string) error,
    ) (handled bool, err error)
}
```

- page 回调由当前 modal 顺序调用；返回不可变值，禁止在 renderer 锁内执行它。InputLabel 为空表示当前不显示字段；只有 paste flow 传非 nil submit。
- `handled=false` 只表示没有可用面板能力：在消费 done、调用 page、申请输入或绘制之前返回，调用者安全地走纯文本/既有不支持环境路径。支持能力但 stdin 被占用，返回 `handled=true,errStdinBusy`，不得悄悄把面板内容打印到聊天区。
- 当 submit 在飞时字段冻结，成功后清除输入并改成无字段等待，拒绝则恢复编辑并展示固定安全文案 `That callback was not accepted. Check the address and try again.`；不要把可能包含 pasted URL 的 err.Error 原样插进面板/日志。
- done 的结果是唯一业务完成信号，ctx 的结果是唯一外部取消信号；UI ErrCancelled 由 setup 映射为用户退回，终端错误则传播。
- 登录与 fetch 复用等待循环，fetch 的 submit=nil。不要扩成所有 slash 命令的通用任务框架。

### 4.3 复用现有输入和渲染约定

- `overlays.go:4081-4114` 的 acquireComposerInput / release 是唯一输入所有权机制；开始 reset/discard 一次，不在 tick 重复清输入。
- `overlays.go:3943-4001` 的 50ms poll、60ms Escape-tail settle、dispatchMouse、EOF 处理是等待循环基础；不用不可取消的读键 goroutine。
- `render.go:4582-4603` 的 SetOverlayPanel：首帧 top=true；计时更新 top=false,follow=false，不能每秒强制滚回焦点。standalone 使用 `paintPage`，保持 pageTop。
- `overlays.go:5251-5281` 的 panel builder / pickerHead / panelReportLine 用于正文；`inputPanel:5518-5527` 提供字段结构。
- paste 编辑沿用 `input:5438-5512` 的 rune/cursor、fieldSelection、withCaretPlacer、withPanelFieldProbe、粘贴/删除逻辑。可在此文件内提取小型编辑状态 helper，普通 Input/Secret 与新字段共享，不能复制一套行为逐渐分叉。保持普通 Input/Secret 既有显示和按键不变。
- callback 编辑时 head 只放短标题与 `Paste callback URL`，字段为 panel.field；授权 URL、操作说明、submit 错误/收尾等待放可滚动 body，始终属于同一个面板。`render.go:7822-7836,7876-7893` 的 head 不滚动，不能把长 OAuth URL 塞进 head 挤掉字段或提示；原型没有独立 paste 布局，不为模仿不存在的顺序改通用 renderer。禁止将说明拼进字段 defaultValue。等待态 field=nil。
- done/context 必须在编辑、提交中、等待三态都被检查，不能调用原有阻塞 `Selector.Input` 然后指望 backend 完成能关掉它。也不能直接把 ShowInfo 当 live wait。

### 4.4 setup 接线与错误收口

- `signIn`：Start 成功后立即启动唯一 Wait；所有 flow 先尝试动态面板，handled=true 绝不执行旧 statusOutput/ConnectProgress/awaitSignInURL。未支持的 selector 才走旧输出路径，复用同一 done。
- browser-open 状态由 setup 的本次登录闭包持有。同步失败和异步 callback 失败经有缓冲 channel 回到 UI，显示仍可手动用 URL 的提示；不打印主视图、不引入 data race。`image_open.go:109-122` 已有可 stub opener，不改启动器。
- fallback 的 `pasteSignInCallback` 遇 !ok,nil 必须返回 ErrCancelled，实际 err 原样返回；进度 stop 用 defer 且 nil-safe。ErrCancelled 在 signIn 统一转 false,nil，不进入 Wait 的后续推进，不重复调用 Wait。
- `fetchOffers` 每次尝试用原有 modelsDiscoveryTimeout 创建 ctx，后台调用一次 Offers，buffered done 承载 err；slice 只在 done 成功被消费后读取（channel happens-before），取消分支不读取后台正在写的结果。输入取消返回 ErrCancelled，不进入 Retry；真实失败仍显示原有 Retry，且不重复 Start。
- `run` 的 setupPageOffers 分支识别本次 fetch 的 ErrCancelled，直接回 setupPageProvider（back=true），不落回会立即重复登录/取模型的 credentials 页；根 ctx 错误仍按错误传播。不得回滚已完成的凭据。
- `note` 改为返回 error：有 InfoOverlaySelector 时直接返回 ShowInfo 结果；只有根本没有该能力才写文本。更新五处调用方传播错误：customTestConnection 两处、pickOffers 三处。正常信息面板关闭返回 nil，行为不变；仅修异常回退，不为业务错误新设状态机。

## 5. Scope 与漂移检查

**允许修改的文件**：

- `pkg/tui/setup.go` / `setup_test.go`：三类登录、fallback 取消、fetch、note 及调用方；后续模型业务不动。
- `pkg/tui/channels.go`：动态面板可选接口。
- `pkg/tui/overlays.go` / `overlays_test.go`：等待/编辑状态机、局部共享编辑逻辑、渲染/输入/预览测试。
- `pkg/subscription/registry.go` / `registry_test.go`：可选元数据快照及原子更新测试。
- `pkg/subscription/volcengine.go`、`volcengine_oauth.go` / `volcengine_oauth_test.go`：同源 deadline。
- `pkg/subscription/opencode.go`：仅改为原子发布已存在的 URL/code；对应断言可放 registry_test.go，复用该包 fake login seam。
- `pkg/architecture/testdata/graph.json`：仅生成器 loc 刷新，不能新增包边或放宽预算。
- 本文件和 `plans/README.md`：执行结果和状态。

**仅阅读，不修改**：原型 HTML、`render.go`、平台输入驱动、`image_open.go`、ChatGPT/插件协议实现、底层 OpenCode auth、gateway/frontend、state schema、go.mod/go.sum。禁止改模型/套餐发现结果与 Provision/Key 存储语义，禁止引入 311 的模型/默认强度变化；这里只改变 fetch 的展示/等待，不改其返回数据。

开工前运行（比较已提交及未提交漂移；不要覆盖其他工作）：

```sh
git rev-parse --short HEAD
git status --short
git diff --stat 7611d6cc..HEAD -- pkg/tui/setup.go pkg/tui/setup_test.go pkg/tui/channels.go pkg/tui/overlays.go pkg/tui/overlays_test.go pkg/subscription/registry.go pkg/subscription/registry_test.go pkg/subscription/volcengine.go pkg/subscription/volcengine_oauth.go pkg/subscription/volcengine_oauth_test.go pkg/subscription/opencode.go
git diff 7611d6cc -- pkg/tui/setup.go pkg/tui/setup_test.go pkg/tui/channels.go pkg/tui/overlays.go pkg/tui/overlays_test.go pkg/subscription/registry.go pkg/subscription/registry_test.go pkg/subscription/volcengine.go pkg/subscription/volcengine_oauth.go pkg/subscription/volcengine_oauth_test.go pkg/subscription/opencode.go
shasum -a 256 docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html
```

若 311 已修改同文件，不能只改 baseline SHA；逐项核对页序、excerpt、cancel 和保存边界后再继续。

## 6. Verification commands

仓库为 Go 1.26.9 单 module、自研 TUI（不是 Bubble Tea），需要 CGO/fts5。测试放同名文件、用现有 testing/testify 风格；全局 browser/clock stub 不跑 t.Parallel，动态 fake 字段用 mutex/channel，不并发改 `fakeLogin` 的裸字段。

本机已缓存且基线成功的环境：

```sh
export PATH="/Users/doudou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.9.darwin-arm64/bin:/opt/homebrew/bin:$PATH"
export GOTOOLCHAIN=local GOPROXY=off GOFLAGS=-mod=readonly CGO_ENABLED=1
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/darwin_arm64"
export VERIFY_DIR="$(mktemp -d /tmp/forebrain-connect-312.XXXXXX)"
export FOREBRAIN_HOME="$VERIFY_DIR/home"
go version
```

其他机器使用其已安装、满足 go.mod 的 Go 和对应原生库路径；缺依赖时报告，不自动安装/升级，不运行会下载资产的 make build/make test。

| 命令编号 | 命令 | 成功判据 |
|---|---|---|
| C0 | 下方基线命令 | 两包相关测试实际命中且 PASS |
| C1 | `go test -tags fts5 ./pkg/subscription -run '^TestDeviceCodeInfo' -count=1 -v -timeout 180s` | atomic snapshot、OpenCode 桥接等新测试 PASS，非零测试 |
| C2 | `go test -tags fts5 ./pkg/subscription -run '^TestVolcengine' -count=1 -v -timeout 180s` | 新 deadline 与原有协议测试 PASS |
| C3 | `go test -tags fts5 ./pkg/tui -run '^TestConnectPanel' -count=1 -v -timeout 180s` | 新渲染/输入/编辑测试 PASS；仅 opt-in preview 可 SKIP |
| C4 | `go test -tags fts5 ./pkg/tui -run '^TestConnectSetupPanel' -count=1 -v -timeout 180s` | 三 flow、fetch、note、fallback 接线测试 PASS，非零测试 |
| C5 | `go test -tags fts5 ./pkg/tui ./pkg/subscription ./pkg/gateway -count=1 -timeout 10m` | 全包 PASS，含共享 Login 合同回归 |
| C6 | 下方 race 命令 | PASS，无 DATA RACE |
| C7 | `go build -tags fts5 ./... && go vet -tags fts5 ./...` | exit 0 |
| C8 | `gofmt -l pkg/tui/setup.go pkg/tui/setup_test.go pkg/tui/channels.go pkg/tui/overlays.go pkg/tui/overlays_test.go pkg/subscription/registry.go pkg/subscription/registry_test.go pkg/subscription/volcengine.go pkg/subscription/volcengine_oauth.go pkg/subscription/volcengine_oauth_test.go pkg/subscription/opencode.go` | 无输出 |
| C9 | `scripts/package-graph.sh && go test -tags fts5 ./pkg/architecture -count=1` | PASS，graph 只有 loc 变化 |
| C10 | `git diff --check && git status --short` | 无空白错误，仅 Scope 内改动 |

C0（扩围基线；正则的竖线不要加反斜杠）：

```sh
go test -tags fts5 ./pkg/tui ./pkg/subscription -run 'TestConfigureSubscription|TestVolcengine|TestSignIn|TestAwaitSignIn|TestCustomModelTestConnection|TestInfoPanel' -count=1 -timeout 180s
```

C6：

```sh
go test -race -tags fts5 ./pkg/tui ./pkg/subscription -run 'TestConnectPanel|TestConnectSetupPanel|TestDeviceCodeInfo|TestVolcengineDevice' -count=1 -timeout 5m
```

**Advisor 已运行**：2026-11-16，临时 FOREBRAIN_HOME 下，扩围 C0 PASS：tui 5.399s、subscription 1.124s；之前较小基线也 PASS（5.851s / 0.717s）。链接器已有重复 `-lm` 与 macOS deployment-target warning，exit 0。未运行全包/race/真实 TUI/真实登录；新用例尚未编写，不能拿旧测试绿当作修复完成。

## 7. Ordered implementation steps

### Step 0 — 先钉住错误容器与取消行为

1. 完成 §5 漂移检查和 C0，确认没有并行 311。
2. 在 `setup_test.go` 用现有 rawSelector/newComposerRenderer、pipe、fake subscription 建立 `TestConnectSetupPanel...`，分别验证 browser/device 等待、paste 输入和 fetch 阶段确实有 overlay，URL/code/说明没有新增 transcript 块。
3. 初始红灯测试不依赖尚不存在的新公开接口：使用已存在能力委托 rawSelector，先观察生产错误容器；不要以缺方法的编译失败为唯一证据。测试必须 defer 释放 fake done/context，避免失败后卡住清理。
4. 另加纯 selector 的 paste Esc/error 用例，断言不假装输入成功、不等待到超时、不 panic；加 InfoOverlay 返回错误而 `cfg.Output` 保持空的用例。

**Verify**：C0 PASS；C4 新用例产生预期 assertion RED，记录失败点。后续步骤逐组转绿，不得忽略仍红用例。

### Step 1 — 原子授权展示数据

1. 实施 §4.1 的可选 snapshot 和内部 atomic setter，Login 必需接口不变。
2. Ark 只算一次 deadline，传入 poll；OpenCode 原子发布 URL/code，expiry 为零。
3. 新 `TestDeviceCodeInfo...` 覆盖并发读完整值、零值、旧 getter 保持、OpenCode onStart 桥接；使用既有可替换 `openCodeLogin` seam，禁止真实登录。
4. 新 `TestVolcengineDeviceInfoUsesPollDeadline` 用既有 fake clock/Sleep 挡住轮询，确认 UI deadline 与 poll 同一个值，读取和重绘不能续期。

**Verify**：C1、C2、C0 PASS；未接线的 C4 红灯保留。

### Step 2 — 建立动态命令面板与可取消 callback 编辑

1. 添加 §4.2 接口，在 overlays 实现单一 modal 状态机：准备 → 等待，或准备 → 编辑 callback → 提交中 → 重试编辑/收尾等待 → 完成。
2. 按 §4.3 接管输入与渲染；首帧立即出现，不等待 URL；按 50ms readiness 同时处理 done/ctx/键鼠。时间秒数或展示内容实际变化才重画。
3. paste 编辑复用现有 rune/caret/selection/paste 行为，Submit 在 ctx 下异步执行，一次一份、buffered result；callback 错误留当前面板。done 先到时退出，不等用户按键或重复提交。
4. 计时刷新保留 scroll 和选择；进入/离开字段才调整 caret/focus，普通 Input/Secret 不回归。
5. viewport 走 SetOverlayPanel，Unix standalone 走 paintPage；真正不支持能力才 handled=false。输入忙不是“无能力”，必须返回错误。

**Verify**：C3 全 PASS；C0 PASS；普通 Input/Secret 的现有测试随 C5 验证。C4 尚未接线部分继续 RED。

### Step 3 — 所有订阅登录流一次接线

1. `signIn` 保留 probe/确认/Start，但在获取 URL 之前就启动唯一 Wait，并对三种 flow 尝试新面板。page 生成按 §3.2；paste 传 login.Submit，其他传 nil。
2. handled=true 路径不再执行 statusOutput/Fprintln/ConnectProgress/awaitSignInURL；浏览器只启动一次，错误经 buffered channel 入 panel。
3. handled=false 复用唯一 done 走文本/旧环境路径；修 paste !ok,nil→ErrCancelled，所有进度释放 nil-safe；统一 ErrCancelled→false,nil，其他错误传播。
4. 修 device 确认/等待的无 code 文案，空码插件不伪装成 OTP 流程。不要硬编码 provider ID 判断。
5. 新/旧路径都不提前 Offers/Provision；登录成功只由 Wait 结果裁决。取消不承诺回滚已经保存的登录。

**Verify**：C4 的 Ark/OpenCode/ChatGPT browser/ChatGPT paste/无码 plugin/fallback 组 GREEN；C3、C0 PASS。新路径断言 no transcript，旧纯文本 fixture 保留有输出断言，两者不能混用。

### Step 4 — 拉取模型与报告异常也收口

1. `fetchOffers` 每次请求用同一 ConnectPanel 能力显示 fetching，异步请求保持既有 timeout 和 env；不要再在交互 viewport 调 statusOutput。
2. 真实失败释放当前等待 panel 后进既有 Retry Confirm；重试只重发 Offers，不重做登录、不留多条 fetching 文本。
3. Esc 取消请求并回 provider 页，ctx 取消传播；修改 run 的 offers 出口时保留 311 已有后续页序，不通过模拟“成功取空列表”实现取消。
4. `note` 返回 error，更新五处调用点；只有无 InfoOverlay 能力才走文本，overlay/input error 不再吞掉并打印主视图。
5. 保持自定义连接测试请求内容、Provision、API key/授权写入语义不变；这些步骤正常已有面板，不重做业务。

**Verify**：C4 的 fetch retry/cancel/note-error 组 GREEN；C0、C3 PASS，C4 全部 GREEN。

### Step 5 — 完整矩阵与真实终端验收

1. 完成 §8 全部测试；C0–C6 全绿。失败要调查本次实际原因，不能引用索引的历史失败豁免。
2. 在 `overlays_test.go` 加默认 skip 的 `TestConnectPanelInteractivePreview`，显式环境变量 + stdin TTY 才运行。用 `newRawTerminalSession`（`tty_session.go:34-70`）及现有 Renderer 建真实终端会话，完整 defer 恢复；**不能用 newComposerRenderer 的 bytes.Buffer 当真机截图**。
3. preview 只用 synthetic login/page、stub browser/clipboard、临时 home，调用生产 panel，不另写展示。环境变量值支持 `ark-headless`、`ark-local`、`browser`、`paste`、`fetch`；fake 后台结果驱动完成，不能由生产 Enter 分支伪造成功。
4. 编译一次测试二进制，在实际终端逐个运行（把下方模式值替换为上述五个值，各记录一次；不是 shell 自动循环）：

```sh
go test -c -tags fts5 -o "$VERIFY_DIR/tui.test" ./pkg/tui
FOREBRAIN_CONNECT_PANEL_PREVIEW=ark-headless "$VERIFY_DIR/tui.test" -test.run '^TestConnectPanelInteractivePreview$' -test.v
```

5. 核验 80×24/120×40、窄屏和 resize；正文在 panel，复制/滚动不中断；paste 的 URL/字段/错误同屏，Esc 退回、fake 成功自动结束；fetch 重试没有聊天残留。截屏只包含 synthetic 数据。
6. 执行 C7–C10，核对原型 hash、Scope；记录验证和平台边界后才可更新状态。无法真机操作时如实列未验，不以单测冒充 UI 完成。

**Verify**：所有自动 gate PASS；五个实际 preview 不 SKIP，视觉/键鼠检查完成；原型不变、无源码范围外改动。

## 8. 必需测试矩阵与测试样板

### 8.1 真实生产接线，前缀 TestConnectSetupPanel

| 场景 | 关键正向与反向断言 |
|---|---|
| Ark local / headless | title/URL/code/期限只在 overlay；browser 0/1 次；无全局 connect spinner |
| OpenCode local / headless | code/URL 同面板，无臆造到期时间；旧文本 fixture 仍有合理输出 |
| ChatGPT browser | fallback URL、等待回调同面板；自动完成无须按键，不编造 loopback 端口 |
| ChatGPT paste | URL/说明/字段同时可见；拒绝可改、成功提交后仍等待；Enter pending 不调用 Offers |
| 无码 plugin browser/device | fixture 模拟 qodercn/workbuddy 的 Flows 与空 UserCode，不 import 私有插件；无 Code 框/确认码误导 |
| 各 flow URL 未就绪 | 先出 preparing，提前失败/ctx/Esc 能退出，不等待旧 30 秒 |
| 晚到 URL/code | thread-safe fake 分次发布并被刷新读到，无空值/重开浏览器 |
| browser 启动错误 | 同步与异步失败都在 panel，URL 仍可用，无数据竞态/文本泄漏 |
| paste fallback | !ok,nil、ErrCancelled、EOF 分别正确传播，不调用 nil stop，不假成功/继续等待 |
| fetch success/retry | fetching 只在 panel，失败后 retry，不新增聊天块，不重新 Start/开浏览器 |
| fetch cancel | cancel request、回 provider、不误报成功/空 offers，不 Provision、不撤销已保存登录 |
| note | 五个调用点错误传播；有能力但 ShowInfo 报错不能写 transcript；无能力才正常文本输出 |
| 已登录/取消/结果竞态 | usable/unreachable 不重登录；一次 Wait、一次结果；没有未授权的后续写入 |

样板：`setup_test.go:247-296` fakeLogin，`:383-449` provider fixtures，`:468-500` browser/headless/config seams，`:910-1003` fetch retry/paste 用例。旧 fake 的裸字段不适合并发，新增/局部替换为受控 channel/mutex fake；不全局重写所有测试。

### 8.2 renderer/输入，前缀 TestConnectPanel

- `overlayPanel != nil` 正向断言与 transcript 无新增块反向断言一起做；测试前放一条聊天内容和 composer draft，退出后不变。
- 用 `reducer_test.go:3090-3099` 的 newComposerRenderer 做自动化布局，不作真实 terminal 证据。
- URL/code 拖选经过真实 SGR parser/dispatchMouse，坐标使用 `overlays_test.go:2246-2273` 的 overlayRowContaining/overlayCellOf；用既有 clipboard stub，不访问用户剪贴板。
- 倒计时 tick 期间复制、选区、wheel/键盘滚动不复位；宽长 URL 完整可见/可滚动；panel hint 固定，resize 能重排。
- callback 编辑测试覆盖 click caret、左右、Backspace/Delete、选择替换、Unicode、bracketed paste；异步刷新不丢文字、不跳光标。既有普通 Input/Secret 测试必须继续通过。
- submit pending 期间 Esc/ctx/后台失败可结束；重复 Enter 不并发 Submit；done 与 submit error 同时到达时以终态为准；迟到 sender 不阻塞。
- 非 paste Enter 不成功；bare Esc 取消，分片 mouse Escape 不误判；Ctrl+C 保留复制优先语义；EOF/read error/input busy 正确清理。
- 每个出口验证 overlay 清除、输入 pause depth 恢复、无悬空 stdin reader/ticker/submit sender；后续输入能交回 composer。
- Unix standalone 有同样状态内容/结束逻辑，纯文本/Windows 等不支持路径 handled=false 无副作用（不消费 done、不触发 page/browser）。

### 8.3 元数据与协议回归

- `TestDeviceCodeInfo...`：原子 snapshot、零 expiry、不暴露额外 credential 字段、旧 getter 保持、OpenCode bridge 原子发布。
- Ark fake clock：deadline 同源、刷新不续期，`10:00→9:59`、`1:00→0:59`、`0:01→0:00`、过期不为负。用显式 now 的 pure formatter，不真实等待十分钟。
- 既有 Ark pending/slow_down/denied/expired/transient/save 测试不放宽；complete URL 优先策略保持。
- callback Submit 的协议校验仍由 Login 实现负责，不在 TUI 复刻 OAuth state/PKCE，也不打印错误中的实际授权值。

## 9. Machine-checkable done criteria

- [ ] C0–C10 达标，新测试实际命中，C4 错误容器用例有先红后绿记录。
- [ ] F1–F10 均有对应测试，不能只完成 Ark 或只测试纯 builder。
- [ ] 三种登录流、无 code 插件、fetch/retry 的主视图均没有过程文字/敏感输入残留；最终成功摘要保持。
- [ ] 用户取消不会继续 Wait 到超时，不会 nil stop panic；非 paste Enter 与 paste Submit 都不能伪造登录成功。
- [ ] snapshot 原子，Ark deadline 与 poll 一致；无静态原型值进入运行时。
- [ ] note 的能力错误向上传播；不会在 panel 失败后把同内容降级写 transcript。
- [ ] 自动复制/编辑/resize/取消/竞态测试与 race PASS；五种真实终端 preview 已实际使用并记录，非 SKIP。
- [ ] Scope 之外无修改；公共 Login 必需接口、协议、凭据存储、模型业务保持；原型 hash 不变。
- [ ] 未宣称完成未验的 Windows 异步面板；执行记录清楚区分自动测试/真实终端/真实账户验证。
- [ ] 更新本计划和索引状态/命令结果；311 冲突处理顺序已记录。

## 10. STOP conditions

- 311 或其他工作已改变本文页序/输入/接口；核实前不套旧 excerpt 或覆盖工作。
- 需要修改通用 renderer、Windows 输入驱动、OAuth 协议/插件、数据库或其他 Scope 外文件才能达标：提供失败用例与最小扩围理由，不能静默修改。
- 实现只覆盖 device/Ark，留下 ChatGPT paste/browser、fetch、note 异常或取消问题；不能以「另一个 flow」为由宣布完成。
- 使用无法取消的 stdin 读 goroutine、嵌套申请同一输入锁、把 Enter/Submit 当成功、把临时内容重新写回 transcript。
- 无法在 tick/submit 更新中保持复制、光标、编辑和滚动；不得删测试换取绿色。
- 验证缺 Go/native 库或相关测试持续失败；不得自动安装升级、放宽断言、改 CI、套用旧审计的失败豁免。
- 需要真实账户/凭据/收费请求才能跑测试；使用 fake/loopback，任何真实登录另需明确授权。
- 真实终端无法验收，或要求 Windows 完整支持但未解决 readiness：报告未完成项，不声明全功能/全平台成功。

## 11. Git workflow / Maintenance / 拒绝项

- 当前仅生成计划；后续执行在操作者指定工作树。improve execute 必须独立 worktree，advisor 只评审，不 merge/push。
- 默认不 commit，得到明确授权后才新建 conventional commit/DCO 提交，不 amend、不跳 hooks。
- 312 → 311 是同文件串行建议，不是功能依赖；不把 311 的模型/默认强度特性混进本提交。
- 未来新增订阅按 flow 自动复用面板，不追加 provider 特判；只有真实提供期限才能显示倒计时，不能拿 connectSetupTimeout 或 access-token expiry 代替。
- callback Submit 与 Wait 的终态关系是维护重点；将来改任何一个，都要运行编辑/提交中取消及终态竞态测试。
- 源码调查范围：全部 `setup.go` 的 statusOutput/ConnectProgress/note 路由及直接 selector/subscription 使用点；不声称审计了所有 slash 命令或全仓安全/性能。
- 未审计/未运行：私有插件协议内部、生产凭据/厂商请求、Web 登录、Windows 实机、真实账户完成认证。插件只核对公开 shape，并用同形 fake 验证宿主面板。

**考虑后拒绝的诊断或修法**：

1. ANSI/主题/行高挤错位置：生产代码本就没有安装登录 panel，不是定位偏差。
2. Ark 给了错误 URL：complete URL 优先是正确设计，不硬编码原型 URL。
3. 只加标题/边框、只改 spinner、只修 Ark：错误容器与其他 flow 都仍在，违反本次追加要求。
4. 用 ShowInfo 包住登录：它阻塞等键，不能满足后台自动完成/动态 callback。
5. 全局改 OutputWriter/StatusWriter：影响普通状态输出且不解决输入生命周期，改为 connect 内显式能力。
6. 把 Custom 测试/Provision 的正常信息页也认成缺失：正常路径已有 InfoOverlay；本期只修其真实异常降级/传播，不重做业务。
7. 给 OpenCode/无码插件编造 expiry 或强制添加 callback 字段：现有公开接口没有该数据或交互，不能类推。
8. 把最终 Connected/Saved 摘要搬进临时等待 panel、声称已写 DB 聊天历史：前者不是同类过程信息，后者没有证据。
9. 复制原型后续旧模型/Key 业务，或迁移 Bubble Tea：与已确认问题无关。

## 12. 执行记录（2026-11-16 `execute`，advisor 派发 → 两轮修订 → APPROVE）

- **位置**：工作树 `.worktrees/312-connect-panel`（分支 `312-connect-panel`，从 `7611d6cc` 新建），**零 commit**，改动留在工作区待 owner 审阅；合并/推进由 owner 决定。12 个文件 +2492/−113，全部在 §5 允许清单内。
- **工作树环境**（§6 需按此修正）：`third_party/{pdfium,libtokenizers}` 软链到主树 + `PKG_CONFIG_PATH` 指主树 `third_party/pdfium/darwin_arm64`；`pkg/gateway/dist` 由 reviewer 从主树拷入（git-ignored），否则 C9 的 `TestWebBuildLoadsNoExternalAsset` 因「扫到 0 个文档」必红——与本次 diff 无关。
- **实现**：`ConnectPanelPage`/`ConnectPanelSelector`（channels.go）+ `rawSelector.TryRunConnectPanel` 单一状态机（overlays.go：50ms readiness 同时看 done/ctx/键鼠，内容变了才重绘，`textField` 与普通 Input/Secret 共用编辑规则，submit 单飞 + buffered 结果 + 退出即 cancel）；`signIn` 三种 flow 统一先起唯一 Wait 再交面板，纯文本 selector 保留旧输出路径（setup.go）；`signInPanel`/`signInCountdown` 纯函数倒计时/`connectBrowser`（只开一次、同步与异步失败都进面板）；`fetchOffers` 拆 `fetchOffersOnce` + `runConnectStage`，取消回 provider 页；`note` 返回 error 并传播 5 处；subscription 侧 `DeviceCodeInfo`/`DeviceCodeLogin` 原子快照、Ark 单一 deadline 传给 poll、OpenCode 原子发布 URL/code（无 expiry）。
- **红→绿证据**（baseline `7611d6cc`，verbatim）：device 页 `Open https://opencode.ai/auth and enter this code.` / `QK7M-2PLX` 落在 transcript；paste 页地址同样落 transcript；fetch 行落 transcript；plain selector 的 paste Esc 变成 `context deadline exceeded`；paste 读错误在 `setup.go:1181` 空指针 panic；两处 note 覆盖失败被吞成 `err=<nil>`。
- **闸门（reviewer 独立复跑，非执行器自述）**：C0 PASS；C1 3 tests；C2 29 个 `TestVolcengine*`；C3 11 PASS + 1 opt-in SKIP；C4 18 tests；C5 tui 182.6s / subscription 0.74s / gateway 11.8s；C6 `-race` 多轮 PASS、无 DATA RACE；C7 exit 0；C8 无输出；C9 graph 仅 LOC（subscription 2173→2225、tui 58966→59543，无新边）；C10 干净。
- **真实终端**：五个 preview 均在实际 tmux 终端跑过；reviewer 另独立复跑 `ark-local`（倒计时 9:41→9:36 实测递减、会话区不变、Esc 干净退出）与 `paste`（字段「›」+URL+说明+提示；输入后 Enter → 字段消失转等待、Esc 退出）。
- **评审两轮**：① 补 §8.1「fetch cancel 回 provider」断言（面板重新画 provider 页、无第二/三次列表请求、未 Provision、未写任何配置）+ §4.2「stdin busy 返回 handled=true+errStdinBusy 且不画」与 §8.2「EOF/读错误清理」两个出口测试 + 去掉一个恒真等待；② 修 reviewer 复现的 flaky：`devicePanelAnswers` 用 `sub.label` 当 needle，而确认页文案 `signInPrompt` 本身就含 label，`-race` 下约 1/3 概率断言到确认页 → 三处改唯一 needle + 新增 `waitForSignInPage` 守卫（截到确认页即报「needle 不唯一」），reviewer 3 轮 + 执行器 6 轮复现全绿。
- **接受的偏差**：§3.2 写 `Code:  <code>`（两空格），面板正文经 `wrapPanelWords`（render.go:7997）按单空格切词、连续空格被折叠，且 `render.go` 只读 → 实际渲染 `Code: <code>`；行序、内容、倒计时与 §3.2 一致。
- **未验/边界**：`SetSubagentAllowed` 失败的第五处 `note` 无测试（形状与前四处相同，制造 DB 写失败需新机制）；Windows `canGateTerminalReads=false` 仍走文本路径（§3.4 边界，未声称已对齐）；未跑真实账户登录；未跑全仓 `go test ./...`（超出 §6 门禁，构建由 C7 覆盖）。

### 12.1 合并收尾（2026-11-17，owner 裁决「主树dev分支所有改动都必须提交，进行2-6」）

- **前置①（主树 WIP 处置）**：owner 裁决全部提交。三件他人/owner WIP 取证后按逻辑分三笔落 dev：`bf00055a` fix(tui) 删 provider picker 副标题（无测试钉住旧文案，grep 核实）、`97a1dca4` fix(test-db) `ensure_scratch_container` 启动已停止的 scratch 容器（Docker Desktop 重启后 inspect-only 判定跳过 stopped 容器致 readiness 超时）、`f437abaa` chore(models) catalog 离线刷新（+1043/−53、27 个新 id，JSON 有效、`pkg/llm` 全量 157s 绿含 golden）。
- **② commit**：worktree 12 文件 +2492/−113 提交为 `1e5963d7`（parent 7611d6cc）。
- **③ rebase**：`git rebase dev`（f437abaa）**无冲突**——catalog_provider（setup.go +73/setup_test.go +114）与 312 面板改动区域不相交，3-way 自动合并；合并后核实两套语义共存（`catalogLookupProvider`:1047 与 `ConnectPanelSelector`/`TryRunConnectPanel` 同在、副标题已删、vendor-wins 在 catalog.go:896）。graph.json 经 `scripts/package-graph.sh` 重生成（仅 8 处 loc，无新边）amend 进提交 → **`813bdd83`**。
- **④ 新基线门禁（rebase 后全量复跑，非沿用旧证据）**：`go build -tags fts5 ./...` exit 0；`gofmt -l pkg/tui pkg/subscription` 空；`go vet -tags fts5 ./pkg/tui ./pkg/subscription ./pkg/architecture` exit 0；scratch home 下 `go test -tags fts5 -race ./pkg/tui ./pkg/subscription -count=1` → **tui 191.971s ok / subscription 1.742s ok，无 DATA RACE**；`./pkg/architecture` ok（2.022s）。worktree 的 third_party 系软链主树（pdfium/libtokenizers），依赖齐全。
- **⑤ ff-merge**：主树 `git merge --ff-only 312-connect-panel` → dev `f437abaa`→`813bdd83`（12 文件 +2499/−120，含 graph amend）；合并后主树复跑 build exit 0 + package-graph 无 diff、工作区干净。
- **⑥ 清理（常设规则「合并回主树后 worktree 和分支必须删掉」）**：`git worktree remove .worktrees/312-connect-panel` + `git branch -d 312-connect-panel`（was 813bdd83，合并安全删除）；worktree list 只剩主树。
- **未重跑**：五个真终端 preview 与真实账户登录沿用 2026-11-16 评审证据（rebase 无冲突、面板测试全绿，行为面未变）；全仓 `go test ./...` 仍未跑（发布门禁）。
