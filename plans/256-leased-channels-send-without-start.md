# Plan 256: 长连接渠道不依赖 Start 也能发出回复（非租约持有副本可用）

> **Executor instructions**: 按步骤顺序执行，每步的验证命令通过后再做下一步。遇到
> 「STOP conditions」里的情况就停下来报告，不要自己发挥。**不要 commit / push / merge**：
> 改动留在工作区里，做完报告（仓库主人手动提交）。做完后把 `plans/README.md` 里本计划那一行
> 的状态改成 DONE，并附一句执行摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> `git diff --stat 236fc32a..HEAD -- pkg/channel/weixin.go pkg/channel/whatsapp.go pkg/channel/mattermost.go pkg/channel/homeassistant.go pkg/channel/weixin_test.go pkg/channel/whatsapp_test.go pkg/channel/mattermost_test.go pkg/channel/homeassistant_test.go`
> → 必须为空。不为空就把下面「Current state」里的摘录和实际代码逐段对照，对不上即 STOP。

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED（动到微信发送路径与它的同步文件写法；租约、路由、收信路径不变）
- **Depends on**: none（plan 238 已落地：`leasedHandler.DeliverOutbound` 转发给 delegate）
- **Category**: bug
- **Planned at**: commit `236fc32a`, 2026-11-06
- **Finding**: 2026-11-06 分支审计（238–245 实现细节），发现 4（`introduced`：238 让这条路径变得可达）

## Why this matters

多副本部署（多个网关共享一个库和同一个 home）下，长连接渠道只在**持有账号租约**的副本上 `Start`。
这些渠道包括微信、whatsapp、mattermost、homeassistant 等 11 种。plan 238 让租约包装器把
`DeliverOutbound` 转发给 delegate，于是**任何副本**都能发回复。

非持有副本确实会发回复，因为这几类消息可以落在任何副本上：
- 定时任务的触发由任意副本抢占（`pkg/turn/scheduler_test.go:581` 起的多副本用例）；
- 跑批通知；
- whatsapp 的 webhook 路由在每个副本上都挂着（plan 234）。

但非持有副本上的 delegate **从没 `Start` 过**，而下面四个适配器的发送都依赖 `Start` 里才做的事：

| 适配器 | 依赖 Start 的东西 | 非持有副本上的结果 |
|---|---|---|
| weixin | `w.api` 只在 `Start` 里建 | `DeliverOutbound` 直接返回 `weixin client not started`，**必然失败** |
| whatsapp | `outbound_url` 为空时的默认值 `http://127.0.0.1:3000`、去尾 `/` | 空配置时请求发到 `"/send"`，报 unsupported protocol scheme |
| mattermost | `normalizeMattermostBase` 去掉 `/api/v4` 后缀 | 配置写成 `https://mm/api/v4` 时，请求发到 `…/api/v4/api/v4/posts`（404） |
| homeassistant | 去尾 `/` | 发到 `…//api/services/…` |

另外，持有者接管租约时 `Start` 会写这些字段，同时别的 goroutine（比如本副本的定时任务）可能正在 `DeliverOutbound` 里读它们，这是数据竞争。

修法有两部分。纯配置的规范化挪进构造函数，构造之后不再变。微信在没启动时，用一个**只发不收**的客户端，
从共享 home 里**只读**地取 context token；它的同步文件改为原子写，这样别的副本读到的不会是半截文件。

## Current state

- 租约包装器（plan 238，**不改**）`pkg/process/lease.go:171-182`：

```go
// DeliverOutbound forwards the send capability to the delegate. … The holder is
// not the only replica that answers: a reply is produced by whichever replica
// runs the conversation.
func (s *leasedHandler) DeliverOutbound(ctx context.Context, o channel.Outbound) error {
	sender, ok := s.handler.(channel.OutboundSender)
	if !ok {
		return channel.ErrNotOutbound
	}
	return sender.DeliverOutbound(ctx, o)
}
```

- `pkg/channel/whatsapp.go:43-49`（构造）与 `:81-99`（`Start`）：

```go
func NewWhatsApp(cfg WhatsAppConfig) *WhatsApp {
	return &WhatsApp{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		seen:       map[string]time.Time{},
	}
}
…
func (w *WhatsApp) Start(ctx context.Context, add RouteAdder, bus Bus) error {
	if !w.cfg.Enabled {
		return nil
	}
	base := strings.TrimRight(strings.TrimSpace(w.cfg.OutboundURL), "/")
	if base == "" {
		base = "http://127.0.0.1:3000"
	}
	w.cfg.OutboundURL = base
	runCtx, cancel := context.WithCancel(ctx)
```

  `DeliverOutbound`（`:117-142`）用 `w.cfg.OutboundURL+"/send"`；`pollLoop` 用 `w.cfg.OutboundURL+"/messages"`。

- `pkg/channel/mattermost.go:44-50`（构造）与 `:53-75`（`Start`）：

```go
	base := normalizeMattermostBase(m.cfg.OutboundURL)
	if base == "" {
		return fmt.Errorf("mattermost outbound_url required")
	}
	m.cfg.OutboundURL = base
	botID, err := m.fetchBotUserID(ctx)
```

  `normalizeMattermostBase`（`:286-293`）：trim、去尾 `/`、去 `/api/v4` 后缀。

- `pkg/channel/homeassistant.go:52-57`（构造）与 `:60-77`（`Start`）：

```go
	base := normalizeHAURL(h.cfg.OutboundURL)
	if base == "" {
		return fmt.Errorf("homeassistant outbound_url required")
	}
	h.cfg.OutboundURL = base
```

- `pkg/channel/weixin.go`：
  - 结构体 `:40-51`，有 `api *ilink.Client`、`contextTokens`、`disk *contextDisk`；
  - `NewWeixin` `:54-59`；
  - `Start` `:90-125`：读同步文件，规范化 `w.cfg.BaseURL`（`:108-112`），`w.api = ilink.NewClient(token, buildILinkOptions(w.cfg)...)`（`:117`），起 `runMonitor`；
  - `DeliverOutbound` `:141-166`：

```go
func (w *Weixin) DeliverOutbound(ctx context.Context, o Outbound) error {
	if w.api == nil {
		return fmt.Errorf("weixin client not started")
	}
	toUser := parseWeixinSession(o.SessionID)
	…
	_, err := w.api.Push(ctx, toUser, text)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ilink.ErrNoContextToken) {
		return err
	}
	ctxTok := w.getContextToken(toUser)
	if ctxTok == "" {
		return fmt.Errorf("weixin: missing context_token for user %s", toUser)
	}
	_, err = w.api.SendText(ctx, toUser, text, ctxTok)
	return err
}
```

  - `storeContextToken` `:251-264`、`getContextToken` `:266-285` 读 `w.api` 时都不加锁；
  - 同步文件路径 `weixinSyncPath(root)` = `<root>/state/weixin_context_tokens.json`（`:586-588`）；`loadWeixinSyncState(root)`（`:590`）返回 `users`、`getUpdatesBuf`；
  - `contextDisk.persistFull`（`:628-647`）写 `users` 加持有者的更新游标 `get_updates_buf`，用的是**非原子**的 `os.WriteFile`：

```go
	b, _ := json.MarshalIndent(f, "", "  ")
	_ = os.WriteFile(weixinSyncPath(d.root), b, 0o600)
```

- `StateRoot` 来自绑定时的 agent state root（`pkg/channel/channel.go:413`）。多副本共享同一个 home，所以每个副本读的是同一个文件。

- 现有测试会受影响、但应保持绿的：
  - `TestWeixinClientGuards`（`weixin_test.go:344`）：`NewWeixin(WeixinConfig{})`（未启用、无 token）的 `DeliverOutbound` 必须报错；
  - `TestWeixinStartEnabled`（`:103`）：Start 后 `w.cfg.BaseURL == srv.URL`；
  - `TestWeixinHTTPBackedClientMethods`（`:453`）：手动给 `w.api` 赋值后走回退发送；
  - `whatsapp_test.go:49,58`：Start 后 `cfg.OutboundURL == "http://127.0.0.1:3000"`；
  - `mattermost_test.go:92-97`：Start 后 `m.cfg.OutboundURL == srv.URL`（配置写的是 `srv.URL + "/api/v4/"`）。
- HTTP 假服务器统一用 `testutil.NewLocalServer(http.HandlerFunc(…))`，见 `weixin_test.go:455`。微信发送的路径是 `/ilink/bot/sendmessage`，返回 `{"ret":0}` 即成功。
- 仓库规则：`X_test.go` 必须与 `X.go` 同名。新测试放进对应的四个已有测试文件。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./...` | exit 0 |
| Focused | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/channel -run 'WithoutStart|DoNotRace|Weixin|WhatsApp|Mattermost|HomeAssistant' -count=1` | `ok` |
| Race | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 -race ./pkg/channel -run 'WithoutStart|DoNotRace' -count=1` | `ok` |
| Neighbours | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/channel ./pkg/process ./pkg/gateway -count=1` | `ok` |
| House rule | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | `ok` |

`-run` 是 Go 正则，用 `|`，**不要**写 `\|`。

## Scope

**In scope**:
- `pkg/channel/whatsapp.go`、`mattermost.go`、`homeassistant.go`：规范化挪进构造函数
- `pkg/channel/weixin.go`：构造时规范化 BaseURL；只发客户端；读 `api` 加锁；同步文件原子写
- 对应的四个 `_test.go`

**Out of scope**:
- `pkg/process/lease.go` 与租约逻辑。
- 微信的其他 API（`SendTyping`、`SendMediaFile`、`UploadFile`、`GetConfig`、`parseToUser`、`sessionContext`）。它们只在持有者上被用到，本计划不给它们加只发客户端，也不改它们读 `w.api` 的方式。
- telegram、discord、matrix、signal、dingtalk、feishu、qq。它们的发送只读构造时就定下的配置，不依赖 Start。step 6 会核一遍，**不要**改它们。
- 微信同步文件的 JSON 格式和字段。

## Steps

### Step 1: whatsapp——规范化挪进构造

```go
func NewWhatsApp(cfg WhatsAppConfig) *WhatsApp {
	// The outbound base is fixed at construction: a replica that does not hold
	// the account's lease never runs Start, yet it sends every reply it produces
	// (pkg/process leasedHandler forwards the send to this adapter).
	cfg.OutboundURL = whatsappOutboundBase(cfg.OutboundURL)
	return &WhatsApp{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		seen:       map[string]time.Time{},
	}
}

// whatsappOutboundBase is the bridge's base URL as every request uses it.
func whatsappOutboundBase(v string) string {
	base := strings.TrimRight(strings.TrimSpace(v), "/")
	if base == "" {
		base = "http://127.0.0.1:3000"
	}
	return base
}
```

`Start` 里删掉 `base := …`、`if base == "" {…}`、`w.cfg.OutboundURL = base` 这几行，其余不动。

**Verify**: `go build ./...` → exit 0；`grep -n 'w.cfg.OutboundURL = ' pkg/channel/whatsapp.go` → 无输出

### Step 2: mattermost 与 homeassistant——同法

- `NewMattermost`：`cfg.OutboundURL = normalizeMattermostBase(cfg.OutboundURL)`；`Start` 里改成

```go
	if m.cfg.OutboundURL == "" {
		return fmt.Errorf("mattermost outbound_url required")
	}
	botID, err := m.fetchBotUserID(ctx)
```

  即删掉 `base :=` 与 `m.cfg.OutboundURL = base`。

- `NewHomeAssistant`：`cfg.OutboundURL = normalizeHAURL(cfg.OutboundURL)`；`Start` 同样只做空值校验，不再写 `h.cfg.OutboundURL`。

两个构造函数上方各加一行注释，说明与 step 1 相同的理由。可以只写「see NewWhatsApp」。

**Verify**: `go build ./...` → exit 0；`grep -nE '(m|h)\.cfg\.OutboundURL = ' pkg/channel/mattermost.go pkg/channel/homeassistant.go` → 无输出

### Step 3: 微信——构造时规范化，`api` 的读写加锁

1. 结构体加两个字段：

```go
	// apiMu guards api and sendOnly: Start builds api when this replica takes the
	// account's lease, while a reply may be going out on another goroutine.
	apiMu    sync.Mutex
	// sendOnly is the client a replica that never started this adapter sends
	// on — see outboundClient.
	sendOnly *ilink.Client
```

2. `NewWeixin` 里先规范化：

```go
func NewWeixin(cfg WeixinConfig) *Weixin {
	cfg.BaseURL = weixinBaseURL(cfg.BaseURL)
	return &Weixin{ … 原有字段 … }
}

// weixinBaseURL is the iLink base URL every client of this adapter uses.
func weixinBaseURL(v string) string {
	base := strings.TrimSpace(v)
	if base == "" {
		base = ilink.DefaultBaseURL
	}
	return strings.TrimRight(base, "/")
}
```

3. `Start` 里删掉 `:108-112` 那段 `baseURL` 规范化。把 `w.api = ilink.NewClient(...)` 和其后的 `SetContextToken` 循环改为先建到局部变量 `api`，最后在锁内赋给 `w.api`：

```go
	api := ilink.NewClient(token, buildILinkOptions(w.cfg)...)
	for k, v := range users {
		if strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
			api.SetContextToken(k, v)
		}
	}
	w.apiMu.Lock()
	w.api = api
	w.apiMu.Unlock()
	runCtx, cancel := context.WithCancel(ctx)
```

4. 加一个加锁读取函数，并让 `storeContextToken`、`getContextToken` 改用它：把两处的 `if w.api != nil { w.api.… }` 换成 `if api := w.startedClient(); api != nil { api.… }`：

```go
// startedClient is the client Start built, or nil on a replica that never
// started this adapter.
func (w *Weixin) startedClient() *ilink.Client {
	w.apiMu.Lock()
	defer w.apiMu.Unlock()
	return w.api
}
```

**Verify**: `go build ./...` → exit 0；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/channel -run 'Weixin' -count=1` → `ok`

### Step 4: 微信——没启动也能发

把 `DeliverOutbound` 换成：

```go
func (w *Weixin) DeliverOutbound(ctx context.Context, o Outbound) error {
	api, started, err := w.outboundClient()
	if err != nil {
		return err
	}
	toUser := parseWeixinSession(o.SessionID)
	if toUser == "" {
		return fmt.Errorf("weixin session_id invalid")
	}
	text := strings.TrimSpace(o.Text)
	if text == "" {
		return fmt.Errorf("weixin: empty outbound text")
	}
	_, err = api.Push(ctx, toUser, text)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ilink.ErrNoContextToken) {
		return err
	}
	ctxTok := w.getContextToken(toUser)
	if ctxTok == "" && !started {
		ctxTok = w.diskContextToken(toUser)
	}
	if ctxTok == "" {
		return fmt.Errorf("weixin: missing context_token for user %s", toUser)
	}
	_, err = api.SendText(ctx, toUser, text, ctxTok)
	return err
}

// outboundClient is the client a reply goes out on. The replica holding the
// account's lease sends on the client Start built, with the context tokens its
// monitor collected. Every other replica — a lease standby that runs a
// conversation, a cron fire or a batch notification — never started this
// adapter, yet the lease wrapper forwards its sends here (pkg/process
// leasedHandler), so it gets a send-only client of its own: the same token and
// base URL, no monitor, and nothing it ever writes to the account's sync file.
func (w *Weixin) outboundClient() (*ilink.Client, bool, error) {
	w.apiMu.Lock()
	defer w.apiMu.Unlock()
	if w.api != nil {
		return w.api, true, nil
	}
	if w.sendOnly == nil {
		token := strings.TrimSpace(w.cfg.Token)
		if !w.cfg.Enabled || token == "" {
			return nil, false, fmt.Errorf("weixin client not started")
		}
		w.sendOnly = ilink.NewClient(token, buildILinkOptions(w.cfg)...)
	}
	return w.sendOnly, false, nil
}

// diskContextToken reads one user's context token from the sync file the lease
// holder keeps under the shared home. Read-only by design: the file also holds
// the holder's update cursor, and a replica that wrote it would move that
// cursor back.
func (w *Weixin) diskContextToken(toUser string) string {
	root := strings.TrimSpace(w.cfg.StateRoot)
	if root == "" {
		return ""
	}
	users, _ := loadWeixinSyncState(root)
	return strings.TrimSpace(users[toUser])
}
```

注意：未启用或无 token 时的错误文字**保持** `weixin client not started`，`TestWeixinClientGuards` 依赖「报错」这一点。

**Verify**: `go build ./...` → exit 0；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/channel -run 'Weixin' -count=1` → `ok`

### Step 5: 微信同步文件原子写

把 `persistFull` 末尾的 `os.WriteFile` 换成「同目录临时文件 + rename」，让别的副本读这个文件时只会看到旧版或新版：

```go
	b, _ := json.MarshalIndent(f, "", "  ")
	// Another replica reads this file to send (diskContextToken): write a
	// sibling and rename it into place, so a reader sees the old file or the
	// new one, never a truncated one.
	path := weixinSyncPath(d.root)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".weixin_context_tokens-*.json")
	if err != nil {
		return
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
	}
```

`os.CreateTemp` 建出的文件权限是 0600，与原来的 `0o600` 相同。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/channel -run 'TestWeixinContextDisk' -count=1` → `ok`；`grep -n 'os.WriteFile(weixinSyncPath' pkg/channel/weixin.go` → 无输出

### Step 6: 核其余七个长连接渠道

逐个打开 telegram、discord、matrix、signal（`messaging.go`）、dingtalk、feishu、qq 的 `Start` 与 `DeliverOutbound`，
确认 `DeliverOutbound` 读的字段**没有一个**是 `Start` 写的。dingtalk、feishu、qq 的 `Start` 只写 `bridge`，
它们的发送走 `getAccessToken`/`getTenantToken` 与构造时的 `httpClient`。

**Verify**：报告里写一句核对结果。如果发现某个 `DeliverOutbound` 读了 `Start` 写的字段，按 STOP 处理。

### Step 7: 测试

在对应测试文件末尾新增：

1. `weixin_test.go`：`TestWeixinDeliversWithoutStart`
   - `root := t.TempDir()`；`(&contextDisk{root: root}).persistFull(map[string]string{"u": "ctx"}, "holder-cursor")`；读出文件字节存为 `before`；
   - 假服务器：`/ilink/bot/sendmessage` 回 `{"ret":0}` 并记下路径，其他路径 404；
   - `w := NewWeixin(WeixinConfig{Enabled: true, Token: "tok", BaseURL: srv.URL + "/", StateRoot: root})`，**不调 `Start`**；
   - `w.DeliverOutbound(ctx, Outbound{SessionID: "user:u", Text: "hi"})` → `nil`；
   - 断言服务器收到了 `/ilink/bot/sendmessage`；
   - 断言 `w.startedClient() == nil`，只发客户端没有冒充 Start 建的那个；
   - 断言同步文件字节与 `before` 完全相同，非持有副本没有写它；
   - 再发给一个文件里没有的用户 `user:nobody` → 错误含 `missing context_token`。

2. `whatsapp_test.go`：`TestWhatsAppDeliversWithoutStart`
   - `NewWhatsApp(WhatsAppConfig{}).cfg.OutboundURL == "http://127.0.0.1:3000"`；
   - 假服务器记路径；`NewWhatsApp(WhatsAppConfig{Enabled: true, OutboundURL: srv.URL + "/"})` 不 Start，`DeliverOutbound(ctx, Outbound{SessionID: "chat:1", Text: "hi"})` → `nil`，路径恰为 `/send`。

3. `whatsapp_test.go`：`TestWhatsAppStartAndDeliverDoNotRace`
   - 假服务器对 `/messages` 回 `[]`、对 `/send` 回 200；
   - 一个 goroutine 调 `Start`，主 goroutine 同时循环 20 次 `DeliverOutbound`；结束后 `Stop`；
   - 断言无错误。这个测试的意义在 `-race` 下：修复前 `Start` 写 `cfg.OutboundURL` 会被报数据竞争。
   - `/messages` 的返回体照 `whatsapp_test.go` 里现有 poll 用例的格式写。

4. `mattermost_test.go`：`TestMattermostDeliversWithoutStart`。配置 `OutboundURL: srv.URL + "/api/v4/"`、`Token: "tok"`，不 Start，`DeliverOutbound(ctx, Outbound{SessionID: "channel:C1", Text: "hi"})` → 路径恰为 `/api/v4/posts`。

5. `homeassistant_test.go`：`TestHomeAssistantDeliversWithoutStart`。配置 `OutboundURL: srv.URL + "/"`，不 Start，路径恰为 `/api/services/persistent_notification/create`。

**Verify**:
- `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/channel -run 'WithoutStart|DoNotRace' -count=1 -v` → 5 个新测试 PASS
- `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 -race ./pkg/channel -run 'WithoutStart|DoNotRace' -count=1` → `ok`

**负对照（必须做，做完复原）**：
- 临时把 step 4 的 `outboundClient` 退回成 `if w.api == nil { return nil, false, fmt.Errorf("weixin client not started") }`，`TestWeixinDeliversWithoutStart` 应红；
- 临时把 step 1 的规范化从构造函数移回 `Start`，`TestWhatsAppDeliversWithoutStart` 应红；`-race` 下 `TestWhatsAppStartAndDeliverDoNotRace` 应报 `DATA RACE`。

观察后复原。

### Step 8: 全量回归

**Verify**：
- `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/channel ./pkg/process ./pkg/gateway -count=1` → 三个 `ok`
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- `go vet ./...` → exit 0

## Test plan

- 新增 5 个测试：4 个「不 Start 也能发」，加 1 个 `-race` 下的竞争用例。各带负对照。
- 保持绿：`pkg/channel` 全包（尤其 `TestWeixinClientGuards`、`TestWeixinStartEnabled`、`TestWeixinHTTPBackedClientMethods`、`TestWeixinContextDisk`、whatsapp 与 mattermost 的 Start 用例），以及 `pkg/process` 的 `TestLeasedChannel*`。

## Done criteria

ALL must hold:

- [ ] `go build ./...`、`go vet ./...` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/channel ./pkg/process ./pkg/gateway -count=1` 全部 `ok`
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 -race ./pkg/channel -run 'WithoutStart|DoNotRace' -count=1` → `ok`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- [ ] `grep -nE '(w|m|h)\.cfg\.(OutboundURL|BaseURL) = ' pkg/channel/{whatsapp,mattermost,homeassistant,weixin}.go` → 无输出
- [ ] `grep -n 'os.WriteFile(weixinSyncPath' pkg/channel/weixin.go` → 无输出
- [ ] 三个负对照已观察到红（含一次 `DATA RACE`）并复原
- [ ] 报告里写了 step 6 的核对结论
- [ ] `git status --porcelain` 只列出 In scope 的文件，外加 `plans/README.md`
- [ ] `plans/README.md` 的 256 行已更新

## STOP conditions

- `ilink.Client` 的 `Push`/`SendText` 依赖 `Monitor` 先跑过（比如需要登录握手、只能在监听连接上发）：只发客户端行不通。报告 SDK 的实际要求，不要在非持有副本上起 `Monitor`，那会变成第二个收信者，正是租约要防的事。
- 第 7 个渠道核对时发现某个 `DeliverOutbound` 读了 `Start` 写的字段：报告是哪一个、哪个字段，不要顺手改。
- 改成原子写后 `TestWeixinContextDisk` 的某个分支变红（比如 root 是文件时的行为变了）：报告差异，不要改测试断言。
- `-race` 在本机跑不起来（工具链或 cgo 问题）：跳过 race 判据，在报告和索引里写明「race 未验」，其余照常。

## Maintenance notes

- 规则：**长连接渠道的 `DeliverOutbound` 只能依赖构造时定下的东西**。`Start` 属于持有租约的那个副本，发送属于所有副本。新增长连接适配器时，配置规范化写在构造函数里，发送需要的运行时状态要能在不 `Start` 的情况下获得。
- 微信的同步文件里，`users`（context token）可由任何副本读，`get_updates_buf` 是持有者的游标。只有持有者写这个文件。评审时检查非持有路径上没有任何 `persistFull` 或 `storeContextToken` 调用。
- 如果将来要让非持有副本也能用微信的打字状态、媒体发送，同样走 `outboundClient` 的模式，不要去复用 `w.api`。
