# Plan 157：网页聊天流必须能收摊——视图卸载时关掉自己那条 WebSocket 与两个时钟

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**（行号以本计划的基线为准，以**符号**为准）：
> `grep -c "function startSessionObserver" frontend/src/composables/useChatStream.ts` 为 `1`；
> `grep -c "function stopRuntimeTimer" frontend/src/composables/useChatStream.ts` 为 `1`；
> `grep -c "onScopeDispose" frontend/src/composables/useChatStream.ts` 为 `0`（本计划会把它变成 1）。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- frontend/src/composables/useChatStream.ts frontend/src/composables/useChatStream.test.ts` 必须**无输出**。
> 有输出就按下文"现状"里的**符号名**核对；引用的变量/函数被改名或语义变了即 STOP 1。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 2）**：
> `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts`（超时给足 900 s）。
> 基线里已经红的是存量问题，**不是你要修的**；Done criteria 只要求"不新增红"。

## Status

- **Priority**: P1（每次进出聊天页都会多一条永不关闭的网关订阅，且 send 中的流不会停）
- **Effort**: M（一个 `dispose()` + 一处作用域挂钩 + 两个测试）
- **Risk**: LOW（纯新增释放路径；不动事件语义、不动渲染）
- **Depends on**: 无
- **Blocks**: 无
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: bug（资源泄漏 / 重复扇出）

## Why this matters

`useChatStream()` 是一个**每实例持有活的网络资源**的 composable：一条发送用的 WebSocket、一条会话观察者
WebSocket、一个在飞的 `AbortController`、两个 `setInterval`（运行时钟、子代理时钟）。它没有任何释放路径：

- 全文没有 `onScopeDispose`（`grep -c onScopeDispose` = 0），返回对象里也没有 `dispose`/`close`；
- 观察者只在**换会话**时关闭（`startSessionObserver` 与 `rebindSessionObserver` 内部）；
- 两个视图在卸载时都不碰它：`ChatView.vue:2133` 与 `:2764` 的两个 `onUnmounted` 只处理浏览状态和
  `workbenchMq`；`WorkshopView.vue:294` 是第二个独立实例。

路由没有 keep-alive，因此**每一次进入/离开 `/chat` 或 `/workshop` 都新建一个实例并留下一条仍在订阅的连接**。
网关侧每个连接都会 `s.Notifier.Subscribe(sid, …)`（`pkg/gateway/server.go:1410-1420`），把该会话的**每一个**
事件都推给每一个订阅者——N 次访问 = N 倍事件序列化与 N 份不会消失的 `messages` 数组。更糟的是正在流式输出的
那次 send 也不会中断：socket 没关、`AbortController` 没 abort，它继续往一个已经卸载的组件里 append，
永远不会走到清定时器的那一步。

要做成：实例被释放时（组件卸载，或测试显式调用），**abort 在飞的发送、关掉两条 socket、清掉两个定时器**，
并把这个释放动作挂到 Vue 的作用域上，两个视图一行都不用改。

## Current state

`frontend/src/composables/useChatStream.ts`（6108 行，函数 `useChatStream()` 从 `:2549` 起）

持有的资源（`:2632-2664`、`:2683`）：

```ts
  let streamAbortController: AbortController | null = null   // :2632
  let activeSocket: WebSocket | null = null                  // :2633
  let activeRunId: string | null = null                      // :2634
  let sessionObserver: WebSocket | null = null                // :2635
  let observerSessionId = ''                                  // :2636
  ...
  let observerReady: Promise<void> | null = null              // :2663
  let runtimeTimer: ReturnType<typeof setInterval> | null = null   // :2664
  ...
  let subagentClockTimer: ReturnType<typeof setInterval> | null = null  // :2683
```

已有的、可复用的清理片段：

- 停止运行时钟：`stopRuntimeTimer()`（`:2964`）。
- 关观察者 + 清状态：`rebindSessionObserver`（`:3429-3434`）的三段式——`try { ws.close() } catch { /* noop */ }` →
  置 null → 清 `observerSessionId`；`startSessionObserver` 开头（`:3330-3332`）只有其中第一步
  （关旧的再原地重绑）。`dispose()` 要的是 rebind 那种完整三段式。
- 子代理时钟：`syncSubagentClock()`（`:2692-2712`）在没有任何 running 任务时 `clearInterval` 并置 null。

现有的终止函数只有一个 `cancel()`（`:5820-5848`），它是"取消本次运行"，**不是**释放实例：它只在
`runId` 为空时才 abort/关 socket，且从不关观察者、从不清定时器。

返回对象在 `:6058-6108`（`return { sessionId, … , formatRuntimeStatusLabel }`），里面没有释放入口。

两个视图：

- `frontend/src/views/ChatView.vue:1417` `} = useChatStream()`（解构使用），`onUnmounted` 在 `:2133`、`:2764`。
- `frontend/src/views/WorkshopView.vue:274` import、`:294` `const stream = useChatStream()`。

Vue 版本 `^3.5.43`，`onScopeDispose` / `getCurrentScope` 可用（`frontend/package.json:43`）。

## Scope

**In scope（只改这些）**：
- `frontend/src/composables/useChatStream.ts`（新增 `dispose()`、把它挂到作用域、从返回值导出、顶部 import 补两个符号）
- `frontend/src/composables/useChatStream.test.ts`（新增 2 个测试）

**Out of scope（看着相关也不要动）**：
- 两个视图（`ChatView.vue`、`WorkshopView.vue`）——若 `onScopeDispose` 生效，它们不需要改；**不要**改它们的
  `onUnmounted`，那会掩盖作用域挂钩没接上的问题
- `cancel()` 的现有语义（用户按停止按钮）
- `applySessionReset` / `rebindSessionObserver` / `startSessionObserver` 的换会话逻辑（它们已经有自己的关闭点）
- WebSocket 协议、事件投影、渲染与卡片
- 服务端（`pkg/gateway`）的订阅机制

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 定向测试 | `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts -t 'release'` | 全部 PASS |
| 本文件全测 | `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts` | 与基线相比无新增 FAIL |
| 类型检查 | `cd frontend && pnpm typecheck` | exit 0 |
| 构建 | `cd frontend && pnpm build` | exit 0 |

（`pnpm` 脚本定义在 `frontend/package.json:13-15`：`test` = `vitest run`，`typecheck` = `vue-tsc --noEmit`。）

## Steps

### Step 1：先写会红的测试

在 `frontend/src/composables/useChatStream.test.ts` 里新增一个 `describe('the stream releases what it holds', …)`，
两个用例。文件顶部已经 import 了 `useChatStream`、`FakeChatWebSocket`、`forebrainApi`、`vi`；还需要从 `vue`
拿 `effectScope`（新 import：`import { effectScope } from 'vue'`）。照抄文件里既有 send 测试的 fake socket 安装/
还原写法（`Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: FakeChatWebSocket })`
+ `finally` 里还原 + `vi.spyOn(forebrainApi, 'chatMessages')` / `'sessionMode'` 给 `mockResolvedValue`）。

**用例 1：观察者在作用域结束时关闭**

`bind_session` 只在 observer 的 `onopen` 里发送（`useChatStream.ts` 的 `startSessionObserver`），
而 `FakeChatWebSocket` 的 `onopen` 只有测试显式调 `socket.open()` 才触发——所以必须先把新建的
socket open 掉，再去等那帧（同文件既有用例全都这么干）：

```ts
it('closes the session observer when its scope ends', async () => {
  FakeChatWebSocket.instances = []
  // …安装 FakeChatWebSocket 与两个 API spy（照抄同文件既有用例）…
  const scope = effectScope()
  const stream = scope.run(() => useChatStream())!
  stream.switchToSession('release-me')
  await vi.waitFor(() => {
    expect(FakeChatWebSocket.instances.length).toBeGreaterThan(0)
  })
  const observer = FakeChatWebSocket.instances[FakeChatWebSocket.instances.length - 1]
  observer.open()
  await vi.waitFor(() => {
    expect(observer.sent.some((frame) => {
      try { return String((JSON.parse(frame) as Record<string, unknown>).op ?? '') === 'bind_session' } catch { return false }
    })).toBe(true)
  })
  scope.stop()
  expect(FakeChatWebSocket.instances.every((ws) => ws.readyState === FakeChatWebSocket.CLOSED)).toBe(true)
})
```

**用例 2：释放会停掉在飞的发送与运行时钟，且可重复调用**

```ts
it('aborts the send it has in flight and can be released twice', async () => {
  FakeChatWebSocket.instances = []
  // …同样的安装…
  const stream = useChatStream()
  const pending = stream.send('hello').catch(() => { /* the release is the point */ })
  const socket = FakeChatWebSocket.instances[0]
  expect(socket).toBeTruthy()
  socket.open()
  expect(stream.runtimeStatus.value.kind).toBe('working')   // 运行时钟在跑
  stream.dispose()
  expect(socket.readyState).toBe(FakeChatWebSocket.CLOSED)
  stream.dispose()                                          // 第二次不能抛
  await pending
})
```

**Verify**：`cd frontend && pnpm vitest run src/composables/useChatStream.test.ts -t 'releases what it holds'`
→ 两个用例都 FAIL（`stream.dispose is not a function` / 观察者仍 OPEN）。若直接 PASS，STOP 3。

### Step 2：实现 `dispose()` 并挂到作用域

`useChatStream.ts` 顶部 import 行改成一个 import 里加两个符号：

```ts
import { computed, getCurrentScope, onScopeDispose, reactive, ref, shallowRef } from 'vue'
```

在 `stopRuntimeTimer()`（`:2964`）之后、`startRuntimeStatus` 之前插入：

```ts
  // dispose releases everything this instance holds: the send in flight, the
  // two sockets and both clocks. A view that unmounts keeps no keep-alive
  // route, so without this every visit would leave a bound WebSocket behind —
  // the gateway fans every session event out to every subscriber — and a send
  // that was streaming would keep appending to a view nobody looks at.
  // Idempotent: a release may come from the scope and from a test.
  function dispose() {
    if (streamAbortController) {
      streamAbortController.abort()
      streamAbortController = null
    }
    if (activeSocket) {
      try {
        activeSocket.close()
      } catch {
        // already closing
      }
      activeSocket = null
    }
    if (sessionObserver) {
      try {
        sessionObserver.close()
      } catch {
        // already closing
      }
      sessionObserver = null
    }
    observerSessionId = ''
    observerReady = null
    activeRunId = null
    stopRuntimeTimer()
    if (subagentClockTimer != null) {
      clearInterval(subagentClockTimer)
      subagentClockTimer = null
    }
  }
  // A test that calls useChatStream() outside a component has no scope; the
  // guard keeps Vue from warning there. dispose is exported for exactly that.
  if (getCurrentScope()) onScopeDispose(dispose)
```

在返回对象里（`:6058-6108`）加一行 `dispose,`（放在 `cancel,` 之后即可）。

**Verify**：
`cd frontend && pnpm vitest run src/composables/useChatStream.test.ts -t 'releases what it holds'` → 两个 PASS；
`cd frontend && pnpm typecheck` → exit 0。

### Step 3：全文件回归 + 构建

**Verify**：
`cd frontend && pnpm vitest run src/composables/useChatStream.test.ts` → 与基线相比无新增 FAIL；
`cd frontend && pnpm build` → exit 0。

（`pnpm build` 会把产物写进 `pkg/gateway/dist/`——这是本仓库的既有约定，**不要**回滚它；提交由 owner 决定。）

## Test plan

- 新增用例 1 断言：作用域结束（组件卸载的等价动作）后，所有 fake socket 都是 `CLOSED`。
- 新增用例 2 断言：`dispose()` 关掉在飞发送的 socket、`dispose()` 可重复调用不抛、`await pending` 能收尾
  （说明 abort 让发送真的结束了，而不是把它永远挂在内存里）。
- 两个用例都照抄同文件既有用例的 fake socket / API spy 安装与还原写法（搜索 `FakeChatWebSocket.instances = []`）。
- 不新增测试工具文件、不引入 `@vue/test-utils` 的 mount：这是 composable 级测试。

## Done criteria

机器可检查，全部满足：

- [ ] `cd frontend && pnpm typecheck` exit 0
- [ ] `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts` 与基线相比无新增 FAIL
- [ ] `cd frontend && pnpm build` exit 0
- [ ] `grep -c "onScopeDispose(dispose)" frontend/src/composables/useChatStream.ts` 为 `1`
- [ ] `grep -c "    dispose," frontend/src/composables/useChatStream.ts` 为 `1`
- [ ] `git status --porcelain` 只显示 `frontend/src/composables/useChatStream.ts` 与其测试文件
      （以及 `pnpm build` 产生的 `pkg/gateway/dist/` 产物）
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的 grep 计数 ≠ 预期（含 `onScopeDispose` 已不是 0），或 drift check 后"现状"里被引用的
   变量/函数被改名或语义变了。
2. 基线命令本身跑不起来，拿不到失败清单。
3. 用例 1 在 Step 1 直接 PASS（`onScopeDispose` 已经被别处接上——重复实施会变成两套释放路径）。
4. `scope.run(() => useChatStream())` 拿不到实例，或 `switchToSession` 在测试里根本不建观察者
   （说明 `buildBrowserForebrainGatewayChatWsUrl` 在本环境被短路；把失败原文与 `FakeChatWebSocket.instances`
   的实际长度贴出来）。
5. 你发现 `dispose()` 之后 `send()` 的 promise 永远不 settle（abort 没接上）——这是要在报告里说清的真实缺陷，
   不要用 `vi.useFakeTimers()` 或轮询把它糊过去。
6. 为了让测试通过而要去改 `cancel()` 或两个视图的 `onUnmounted`——先停下报告。

## Maintenance notes

- 谁将来在这个 composable 里新增长效资源（新的 socket、新的 `setInterval`、`window` 监听），**必须**同时加进
  `dispose()`；评审时把这条当准入条件。
- 评审重点：`dispose()` 里把 `streamAbortController` 置 null 与 send 的 `finally`（`:5751-5755` 的
  `stillMine` 判断）不冲突——释放之后那次 send 只是不再做收尾清理，因为它要清的正是已经关掉的东西。
- `WorkshopView` 与 `ChatView` 各持一个实例；两个实例互不影响，测试只覆盖 composable 级别即可，
  不需要为「两个视图同时打开」再写用例。
