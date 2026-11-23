# 03 LSP 修复：Windows 可执行判定、consent 口径、安装观察器、停机/重启/Close 窗口

来源发现：I-1、I-3、I-7、I-12、I-13、I-14（全部 introduced，随 `bda9505` 的 pkg/lsp 新包进入）。
涉及包：`pkg/lsp`、`pkg/tui`、`cmd/forebrain`、`pkg/process`（C2 的解析入口）。全部写进已有文件。
硬约束：`tool.CodeIntelligence`/`CodeIntelControl` 端口接口**不得加方法**；`pkg/lsp` 已在 20 文件上限，不新建生产文件。

## 前置检查（漂移核对）

1. `pkg/lsp/instance.go`：`executableFile` 要求 `st.Mode()&0o111 != 0`（约 1394-1400 行）；`resolveCommand` 手工遍历 PATH、miss 直接报错不回退（约 1360-1378 行）。
2. `pkg/lsp/detect.go`：`tryExecutable(candidate, goos, env)` 已带 goos 参数与 PATHEXT 循环（约 305-326 行）；`detect_test.go` 多处在 Windows skip。
3. `cmd/forebrain/mcp_consent.go`：MCP 提示（约 27 行）与 LSP 孪生（约 83 行）都用 `safety.ResolveProjectContext`；`pkg/process/runner_pool.go:665-673` 运行时改用 `safety.ResolveRegisteredContext`（自带动机注释）。
4. `pkg/tui/chat_session.go` 的 `watchLSPInstall`（约 2622-2671 行）：只在快照观察到 `Installing=true` 后置 `wasRunning`。
5. `pkg/lsp/instance.go`：`Restart` 以 `i.gen == nil` 判停（约 1070-1076 行）；`Shutdown(ctx)` 不消费 ctx（约 1113 行）；`stopGeneration` 的最终 `<-gen.waitDone` 无界（约 1180 行），调用点在 `Shutdown`、`Restart` 与 `pkg/lsp/pool.go`（约 848-875 行，外层 10 秒预算）。
6. `pkg/lsp/control.go` 的 `detectInfo`（约 251-278 行）：`m.mu` 内查 `closed`/`detecting`，解锁后才 `m.bg.Add(1)`。

## C1 [CORRECTNESS] Windows 上让 LSP 二进制探测与启动真正可用（I-1）

**根因**：Go 在 Windows 的文件 mode 恒无 0111 位，`executableFile` 的可执行位检查在 Windows 永远失败；`resolveCommand` 的手工 PATH 遍历不识 PATHEXT 且 miss 后不回退。效果：探测永远"未安装"、推荐永不发、acquire 永远 start-failed。

**改动**（对照 `os/exec` 的 `lp_windows.go` 语义逐条镜像，全部在已有文件）：

1. `pkg/lsp/instance.go` 的 `executableFile`：按 GOOS 分派——POSIX 维持现判定；Windows 改为 `st 拿得到 && !st.IsDir()`（exec 位检查只属 POSIX；扩展名合法性交给调用方的 PATHEXT 逻辑，与 `findExecutable` 一致）。
2. `pkg/lsp/detect.go` 的 `tryExecutable`：改成 `LookPath` 的精确形状——Windows 下若 `filepath.Ext(candidate) == ""` 则**跳过裸候选**、只试 `candidate+pathExt`；若已带扩展名则只试裸候选。POSIX 不变（先裸候选、无 PATHEXT）。
3. `pkg/lsp/instance.go` 的 `resolveCommand`：手工遍历的命中判断在 Windows 下复用 `tryExecutable`（`detect.go` 已有同形逻辑，消除重复），从而裸命令名只按 PATHEXT 命中；不新增 `exec.LookPath` 回退（镜像语义已正确，少一条路径）。
4. 测试解封：`detect_test.go` 的 Windows skip 用例改为 goos 参数化断言（在 darwin 上以 `goos: "windows"` 跑），另在 `instance_test.go` 补 `resolveCommand` 的 Windows 形状用例：临时目录放 `gopls`（无扩展名）与 `gopls.exe`，`goos=windows` + PATHEXT 环境下命中 `.exe`、拒绝裸名；POSIX 形状不回归。`os.Stat` 对 `.exe` 命名文件在 darwin 上工作正常，无需注入。

**已知限制**：本机无 Windows，真机验证缺失；语义以 `os/exec` 源码为准绳，单测钉形状。如实记档。

**缓存影响**：无。

## C2 [CORRECTNESS] 启动期 consent 提示与运行时按同一口径解析项目（I-3）

**根因**：运行时给已注册项目的边界是注册根（`ResolveRegisteredContext`，从不向上走），启动提示仍用 launch 口径（`ResolveProjectContext`，向上走到最近 `.git`）。注册的"无自身 `.git` 的 checkout 子目录"项目：allow 记在上层 checkout 的 project key 下、静默不生效（fail-closed，无安全绕过）；deny 则错共享给整个 checkout。

**改动**：

1. `pkg/process`（写进已有文件，建议 `runner_pool.go` 或 `open.go`）：新增导出函数 `ResolveConsentProjectContext(root, cwd)`（命名从仓内风格）——**注册优先、launch 兜底**：cwd 命中某个已注册项目根（相等或位于其下，嵌套取最深；用现有项目注册表逐项 `filepath.Rel` 判包含，注册表查询入口与 `runner_pool.go` 构建 runner 时同源）→ `safety.ResolveRegisteredContext(root, 注册根)`；否则 `safety.ResolveProjectContext`。
2. `cmd/forebrain/mcp_consent.go`：两个提示函数（MCP 27 行、LSP 83 行）换调新入口。fail-closed 注释保留。
3. 前置核对一条：gateway 侧项目页 consent 路径若也用 launch 口径构建提示上下文，一并换新入口；若本就与 runner 同源则不动（审计未判其有缺陷，只核对）。

**缓存影响**：无。

**测试**（`pkg/process` 白盒）：注册一个 gitless 子目录项目 → `ResolveConsentProjectContext` 返回注册根口径（TrustedRoot/project key 与 `runner_pool` 构建该 runner 时一致）；未注册目录 → launch 口径不变。`cmd/forebrain` 侧按现有 `interactive_test.go` 夹具补一条提示与 runner 读同一文件、同一 key 的端到端小测。

## C3 [CORRECTNESS] TUI 安装观察器补上"两快照之间完成"的终态（I-7）

**根因**：观察器只信"先见 Installing=true 再见 false"的时序；安装在两次快照之间完成（或从未真正起跑但已带错误终态）时既不发收尾帧也不退出——UI 永卡 Installing，每复现一次泄漏一个订阅（`control.go:326` 注册表仅 Close 清理）。

**改动**（两处，都不动 `tool.CodeIntelControl` 接口）：

1. `pkg/lsp/control.go` 的 `Subscribe`：订阅即推当前快照（作为第一条消息，或同步回调一次）——观察器的种子时序问题在 Manager 侧一次修好，所有订阅者受益。核对 `notifySoon` 的去抖路径不因首推而双发。
2. `pkg/tui/chat_session.go` 的 `watchLSPInstall`：终态判定加一条——`!srv.Installing && srv.InstallError != ""` 即使未见 `wasRunning` 也按失败终态处理（发 `LSPInstallDoneMsg{Err: …}`、置 `done`、退订）。成功终态仍要求"见过 Installing=true"（由第 1 条的首推快照天然满足：accept → 安装起跑 → Subscribe 的首推已带 Installing=true；执行时核对 accept 处理器里"安装起跑"与"Subscribe"的先后，若安装是异步起跑，在起跑调用返回后再订阅）。

**缓存影响**：无。

**测试**：`pkg/lsp/control_test.go`——Subscribe 首推当前快照（假服务器夹具）。`pkg/tui/chat_session_test.go`——(a) 首帧 Installing=true → 后续 false → 成功帧；(b) 首帧 `Installing=false && InstallError!=""` → 失败帧且退订；(c) 首帧 false 无错误 → 继续等待不误报。

## C4 [CORRECTNESS] Shutdown 尊重 ctx、stopGeneration 的最终等待有界（I-12）

**改动**（`pkg/lsp/instance.go`、`pkg/lsp/pool.go` 调用点）：

1. `stopGeneration(gen, ctx)`：派生硬上限 `waitCtx, cancel := context.WithTimeout(ctx, timeout)`（timeout 沿用现有 ShutdownTimeout/5s）；三处等待（1s、2s、最终 `<-gen.waitDone`）统一为 `select { case <-gen.waitDone: case <-waitCtx.Done(): }`。**放弃等待不跳过善后**：`tree.kill()`、`tree.release()`、`conn.Close()`、`forgetPID` 照旧无条件执行（SIGKILL 已先行发出，孤儿面与现状持平）。
2. `Shutdown(ctx)`：入口先 `if err := ctx.Err(); err != nil { return err }`；把 ctx 传入 `stopGeneration`。`Restart` 同样传自己的 ctx。
3. `pool.go` 的调用点适配新签名（外层 10 秒预算保持）。

**风险**：提前放弃等待可能留孤儿——已由"SIGKILL 先行 + 善后无条件"兜到与现状持平；`forgetPID` 必须仍执行，测试断言之。

**缓存影响**：无。

**测试**（`pkg/lsp/instance_test.go` 假服务器夹具）：已取消的 ctx 调 `Shutdown` → 立即返回 ctx.Err 且 PID 文件被清（forgetPID 生效）；正常关停路径不回归。

## C5 [CORRECTNESS] Restart 在 crash 自愈窗口报真话（I-13）

**根因**：crash 重启全程（`handleCrash`：置 `gen=nil` → backoff → 重新 launch，约 995-1055 行）`gen==nil` 且 `state==StateStarting`；`Restart` 以 `gen==nil` 判停，报出假错误 "language server X stopped"；`Shutdown` 同窗口会等待接管，两者不对称。

**改动**（`pkg/lsp/instance.go` 的 `Restart`）：`gen == nil && !doneClosed && state == StateStarting` 时不算停——按 `Shutdown` 现成的等待模式（等 `stateCh`/`done`，约 1114-1131 行的形状）等 crash 重启落地（以 Restart 的 ctx 为界），然后对新 generation 继续正常 restart；ctx 到期则返回说明性错误（"language server X is crash-restarting"语义，英文一句，模型/用户可见文案照 spec 风格）。纯 `gen==nil && 终态` 的旧判定保留。

**缓存影响**：无。

**测试**（`pkg/lsp/instance_test.go`）：杀掉假服务器触发 crash，窗口内调 `Restart` → 成功（不返回 stopped）；配合短 ctx → 得到 crash-restarting 错误。

## C6 [CORRECTNESS] 关掉 detect-after-Close 复活已删工作区的窗口（I-14）

**根因**：`detectInfo` 的 `closed` 检查与 `m.bg.Add(1)` 不原子；`Close` → `drainBackground`（`bg.Wait`）承诺后台写者先于 Close 返回结束。迟到 detect 经 `writeFileAtomic` 在已删 `<workspace>` 下重建 `state/lsp/detect.json`。

**改动**（`pkg/lsp/control.go` 的 `detectInfo`，最小原子化）：把 `m.bg.Add(1)` 挪进 `m.mu` 临界区（设置 `m.detecting[sc.ID]` 的同一临界区内、Unlock 之前），goroutine 仍在解锁后启动。`Close` 先持 `m.mu` 置 `closed` 再 `bg.Wait()` 的现有顺序不变——此后"Add 与置 closed 互斥于同一把锁"，不再有 Add-after-Wait，也不再有检查后跨越。

**缓存影响**：无。

**测试**：`go test -race -tags fts5 ./pkg/lsp/ -count=1` 全绿（强制）；`control_test.go` 补一条压力小测：并发 `detectInfo` + `Close`，断言 Close 返回后不再有新 goroutine 进入 detect（用计数 hook 或从简——靠 -race 与既有不变量测试）。

## 验证（本计划全部完成后）

```bash
gofmt -l pkg cmd
go vet -tags fts5 ./...
CGO_ENABLED=1 go test -tags fts5 ./pkg/lsp/ ./pkg/tui/ ./pkg/process/ ./cmd/forebrain/ -count=1
go test -race -tags fts5 ./pkg/lsp/ -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/ -count=1   # 若动了 pkg 结构先跑 scripts/package-graph.sh
```

Windows 说明：本机无 Windows，C1 的真机验证缺失，如实记录；`.github/workflows/lsp-integration.yml` 仅 ubuntu，不改。
