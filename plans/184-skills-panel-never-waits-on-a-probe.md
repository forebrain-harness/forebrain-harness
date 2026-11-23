# Plan 184：/skills 面板不再等网络——git 探针有界 + 面板只读缓存

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**——本轮开工时
> `git status --porcelain` 已有 298 条 `.forebrain/skills/**` 的删除（`D`），以及 `pkg/tui/render.go`、
> `pkg/tui/overlays.go` 两份别人正在改的未提交修改（面板三行 filter bar / 鼠标选区）。它们都是**别人/owner** 的现场，
> 不属于本计划，不要 `git checkout`／`git restore`／`stash` 它们；你的 in-scope 文件如果被它们顺带改了，
> 以现场代码为准重新核对摘录（STOP 1）。
>
> **Drift check（先跑）**：
> `git diff --stat 31370fb1..HEAD -- pkg/skill/git_install.go pkg/tui/skills_versions.go pkg/tui/chat_slash.go pkg/tui/run.go`
> 有输出就把 "Current state" 的摘录与现场代码逐条对照；对不上按 STOP 1 处理。

## Status

- **Priority**: P1（owner 报的现网卡死：TUI 里 `/skills` 后整机冻结数分钟、面板不出现、Ctrl+C 无效）
- **Effort**: S–M（`pkg/skill` 一个等待上限 + 两个新访问器；`pkg/tui` 一处探针调用改缓存读；测试为主）
- **Risk**: MED（动的是「列表面板要不要等网络」这条 UX 契约：**首次打开不再等待，标记在下一次打开/返回列表时出现**）
- **Depends on**: 无
- **Blocks**: 185（同族：面板里的 pull 动作仍在内联跑 git）
- **Planned at**: commit `31370fb1`，2026-10-28
- **Category**: bug（交互冻结 + 超时不生效）

## Why this matters

TUI 里敲 `/skills` 回车后，技能面板要等**每一条已安装技能的「上游是否有新版本」网络探针**跑完才画第一帧。
这个探针在**主事件循环 goroutine** 上同步执行（`pkg/tui/run.go` 的斜杠派发 → `handleSkills` →
`skillEntryStatus` → `SkillVersionState` → `skillUpdateMarker` → `skill.RemoteHead`），所以探针不返回，
整个 TUI 就不接收按键、不重画（唯一还在动的是 500 ms 软件光标 ticker 自己的 goroutine，见
`pkg/tui/reducer.go:3978-4013`）——正是 owner 描述的「只有 composer 里的光标在闪，无法输入，不显示面板」。

更糟的是探针的 2 秒 deadline **根本不生效**：`skill.RemoteHead` 用 `exec.CommandContext` 跑
`git ls-remote`，stdout/stderr 接到 `bytes.Buffer`（`exec` 因此建管道 + 拷贝 goroutine）；deadline 到点只杀掉
`git` 本身，而它派生的 `git-remote-https` 仍持有管道写端，`cmd.Wait()` 要等这个孙进程退出才返回。
本计划起草时在 owner 的机器上实测（同一份 `pkg/skill` 代码、同一个二进制）：

- 带 2 秒 deadline 的 `RemoteHead`，目标换成不可路由的 `https://10.255.255.1/x.git`：**1 分 15 秒**后才返回
  （`RemoteHead returned after 1m15.024614917s (ok=false rev="")`）。
- 用 owner 真实 home 跑面板路径（当时 `.forebrain/skills` 里 46 个 samber 技能，全部同一个 source）：
  第一次探针 **35.6 秒**，面板整段冻结。
- 独立复现「管道被孙进程持有」这一机制：100 ms deadline 的 `CommandContext` + `bytes.Buffer`
  在**可达**的 GitHub 上要 630 ms 才返回；加 `WaitDelay` 后是 1.1 s；按进程组 kill 后是 100 ms。

也就是说：用户看到的是「几分钟」的量级，取决于目标主机的 TCP 重试，而不是代码里写的 2 秒。

修完的收益：`/skills` 面板**永远秒开**（数据全是本地文件系统读，实测 discovery 5.8 ms / 72 条，
每条版本状态 ≤50 µs）；「有更新」标记改成**后台探针写完缓存后、下一次列表面板读缓存**显示——和本仓
`/mcp`、`/lsp` 面板的既有约定一致（`pkg/tui/run.go:5464-5484` 的 `refreshInventory`/`refreshLSP` 注释：
"never waits on the MCP startup … reports through the next tick"）。同时 `gitOut` 的等待上限让**所有** git
调用（安装、pull、探针）在 ctx 到点后真正返回，不再有「deadline 挂了但调用不返回」的第二类冻结。

## Current state

涉及文件：

- `pkg/skill/git_install.go` —— `gitOut`（所有 git 调用的唯一入口）与 `RemoteHead`（上游 revision 探针 + 1 小时缓存）。
- `pkg/tui/skills_versions.go` —— TUI 侧的版本状态与「有更新」标记（`SkillVersionState` / `skillUpdateMarker`）。
- `pkg/tui/chat_slash.go` —— `/skills` 面板的数据装配（`handleSkills` / `skillEntryStatus`）。
- `pkg/tui/run.go` —— 斜杠命令在主事件循环上的派发（`case "skills":`，以及「哪些斜杠命令允许阻塞」的注释）。
- `pkg/lsp/detect.go` —— **本仓已有的正解先例**：同一种「被杀的子进程留下孙进程持有管道」问题，
  用 `cmd.WaitDelay` 兜住（照它写，不要另发明一套）。

现状代码（逐字摘录，执行前用 `sed -n` 复核行号）：

`pkg/skill/git_install.go:104-126` —— 没有等待上限：

```go
// gitOut runs one git command under ctx. Installs run on a background
// goroutine while the surface stays interactive, so a clone must die with the
// context that owns it rather than outliving the session that started it.
func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		// git's own words, unwrapped and unedited: which failure this was —
		// a private repository, a proxy, DNS, a typo in the reference — is
		// only ever visible in what git printed, so that is what the
		// surfaces show.
		return "", fmt.Errorf("git %v: %s", args, msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}
```

`pkg/skill/git_install.go:204-254` —— 缓存与探针（注意：失败也入缓存，所以「一个仓库一次探针」）：

```go
const remoteHeadTTL = time.Hour

type remoteHeadEntry struct {
	at  time.Time
	rev string
	ok  bool
}

var remoteHeadCache = struct {
	sync.Mutex
	m map[string]remoteHeadEntry
}{m: map[string]remoteHeadEntry{}}

func RemoteHead(ctx context.Context, repo string) (string, bool) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", false
	}
	now := time.Now()
	remoteHeadCache.Lock()
	if entry, ok := remoteHeadCache.m[repo]; ok && now.Sub(entry.at) < remoteHeadTTL {
		remoteHeadCache.Unlock()
		return entry.rev, entry.ok
	}
	remoteHeadCache.Unlock()
	rev, ok := "", false
	if out, err := gitOut(ctx, "", "ls-remote", repo, "HEAD"); err == nil {
		if fields := strings.Fields(out); len(fields) > 0 {
			rev, ok = fields[0], true
		}
	}
	remoteHeadCache.Lock()
	remoteHeadCache.m[repo] = remoteHeadEntry{at: now, rev: rev, ok: ok}
	remoteHeadCache.Unlock()
	return rev, ok
}
```

`pkg/lsp/detect.go:26-30` + `:195` —— 正解先例（`installKillGrace` 是包级变量，只为测试能缩短它）：

```go
// installKillGrace bounds the wait for an install command's orphaned children
// to release the output pipes (exec.Cmd.WaitDelay): a killed installer may
// leave a grandchild holding the pipe, and the timeout must still bound the
// call. A package-level variable only so tests can shorten it.
var installKillGrace = 2 * time.Second
...
	cmd := exec.CommandContext(ctx, recipe.Argv[0], recipe.Argv[1:]...)
	...
	cmd.WaitDelay = installKillGrace
```

`pkg/tui/skills_versions.go:33-37` —— 面板那条探针的预算：

```go
// skillUpdateProbeTimeout bounds the one network step the panel can trigger:
// the cached upstream-head probe behind "update available". The probe is
// cached for an hour upstream, and a source that cannot be reached leaves the
// skill without a marker rather than stalling the panel.
const skillUpdateProbeTimeout = 2 * time.Second
```

`pkg/tui/skills_versions.go:93-119` —— 同步探针（`entry.Path` 的 root 是 `<skills root>`，`name` 是目录名）：

```go
func (s *ChatSession) skillUpdateMarker(name string, state skill.SkillVersionState) *skill.SkillUpdate {
	if state.Current == 0 || len(state.Versions) == 0 {
		return nil
	}
	latest := state.Versions[len(state.Versions)-1]
	repo, rev := strings.TrimSpace(latest.Source), strings.TrimSpace(latest.UpstreamRev)
	if repo == "" || rev == "" {
		if s != nil {
			if entry, ok := skill.LookupLockEntry(s.skillLifecycleService().Workspace(), name); ok && strings.TrimSpace(entry.SourceRef) != "" {
				repo = strings.TrimSpace(entry.SourceRef)
				if rev == "" {
					rev = strings.TrimSpace(entry.Revision)
				}
			}
		}
	}
	if repo == "" || rev == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), skillUpdateProbeTimeout)
	defer cancel()
	remote, ok := skill.RemoteHead(ctx, repo)
	if !ok || remote == "" || remote == rev {
		return nil
	}
	return &skill.SkillUpdate{Rev: remote}
}
```

`pkg/tui/chat_slash.go:1652-1661` —— 面板为**每一行**求一次状态（第一个 miss 就足够冻结整块面板）：

```go
func (c *commandController) skillEntryStatus(entry skill.Entry) skillStatus {
	if entry.Origin == skill.OriginBuiltin {
		return skillStatus{known: true, builtin: true}
	}
	state, ok := c.session.SkillVersionState(entry)
	return skillStatusFor(entry, state, ok)
}
```

`pkg/tui/run.go:1306-1314` —— 派发位置（本仓的既定立场：斜杠命令默认留在主循环上，
只有 `/compact` 走可取消的旁路；本计划**不**改这个立场，改的是面板数据里不要再有网络）：

```go
// dispatchCancelableStreamSlashCommand keeps the TUI event loop alive while a
// blocking slash handler runs. ...
// Only /compact currently uses this path. Other slash commands are either
// immediate or own a modal selector and therefore must remain on the main UI
// goroutine. ...
```

`pkg/tui/reducer.go:3978-4013` —— 为什么「冻结时只有光标还在闪」：

```go
func (r *Renderer) startCursorBlinkLocked() {
	r.cursorBlinkVisible = true
	ticker := time.NewTicker(500 * time.Millisecond)
	...
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				r.mu.Lock()
				...
				r.cursorBlinkVisible = !r.cursorBlinkVisible
				r.vpIMERepairDue = true
				r.paintViewportLocked()
				r.mu.Unlock()
			}
		}
	}()
}
```

本仓约定（照做，不要另立风格）：

- 超时/等待上限用**包级变量**并注明「只为测试能缩短」（`pkg/lsp/detect.go:22-30` 就是范例）。
- 需要后台跑的东西走既有的 `notifyUI` 通道回到 UI 循环；**不要**从后台 goroutine 直接画屏幕。
- `pkg/skill` 的测试是包内测试（`package skill`），可以直接改包级变量、直接读缓存。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 聚焦测试（skill） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill -count=1` | ok，全绿 |
| 聚焦测试（tui） | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Skill|Skills' -count=1` | ok，全绿 |
| 整包回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/skill ./pkg/tui ./pkg/turn -count=1` | ok，全绿 |
| vet | `go vet ./...` | exit 0 |
| 格式 | `gofmt -l pkg/skill pkg/tui` | 无输出 |

`FOREBRAIN_HOME` 必须是 `mktemp -d` 出来的临时目录：测试会开状态库，继承真实 home 会迁移 owner 的安装（见 FOREBRAIN.md「House rules」）。

## Scope

**In scope**（只能改这些）：

- `pkg/skill/git_install.go`
- `pkg/skill/git_install_test.go`（新建；若你更愿意放进 `versions_test.go` 也可以，但**不要**动其中已有测试）
- `pkg/tui/skills_versions.go`
- `pkg/tui/skills_versions_test.go`
- `plans/README.md`（只改本计划的状态行）

**Out of scope**（看着相关也不要动）：

- `pkg/tui/run.go` 的斜杠派发策略（不要让 `/skills` 走 `dispatchCancelableStreamSlashCommand`：面板要 modal selector，抢 stdin 的改造是另一件事）。
- `pkg/skill/service.go` 的 `withUpdate`（gateway/web 路径在 HTTP goroutine 上跑，超时由请求 ctx 管；**不改**它的同步语义）。
- `pkg/tui/skills_versions.go` 的 `pullUpdateFlow` / `PullSkillUpdate`（同族的第二处内联 git，属 plan 185）。
- `.forebrain/skills/**` 的任何改动或恢复。
- 任何 UI 文案/排版调整（本计划不改面板外观）。

## Steps

### Step 1：先量基线（把「现在的行为」变成可对比的数字）

用 driver 造一个**匿名的**隔离 home，并在里面放一个「有版本、source 不可达」的技能：

```bash
export FOREBRAIN_RUN_WORK=$TMPDIR/fb-plan184 FOREBRAIN_RUN_SESSION=fb-184 FOREBRAIN_RUN_PORT=8799
D=.claude/skills/run-forebrain/driver.sh
$D reset; $D build; $D start reply ok; $D stop        # 只为把 home/config 铺出来
H=$FOREBRAIN_RUN_WORK/home
mkdir -p "$H/skills/demo" "$H/skills/.versions/demo"
printf -- '---\nname: demo\ndescription: plan 184 probe\n---\n\nbody\n' > "$H/skills/demo/SKILL.md"
printf '%s\n' '{"name":"demo","current":1,"versions":[{"v":1,"at":"2026-10-01T00:00:00Z","kind":"installed","source":"https://10.255.255.1/x.git","upstream_rev":"deadbeef"}]}' > "$H/skills/.versions/demo/manifest.json"
$D start reply ok
$D submit '/skills'
time $D wait 'Run an installed skill' 8 || echo "BASELINE: panel did not appear within 8s (this is the bug)"
$D stop
```

**Verify**：基线必须**复现**「8 秒内面板不出现」（`BASELINE:` 那行被打印）。这一步记录的是修复前的数字，
不要因为它「失败」而 STOP——它失败才是对的。

### Step 2：给 `gitOut` 加等待上限（`pkg/skill/git_install.go`）

在 `gitOut` 上方加包级变量，并在 `cmd.Run()` 之前设 `WaitDelay`（照 `pkg/lsp/detect.go:22-30,195` 的写法）：

```go
// gitKillGrace bounds the wait for a killed git's orphaned children to release
// the output pipes (exec.Cmd.WaitDelay). git spawns helpers (git-remote-https)
// that inherit stdout/stderr, so killing git alone leaves cmd.Wait blocked
// until the helper exits: a 2 s deadline measured 1m15s against an unreachable
// host, freezing whatever goroutine made the call. A package-level variable
// only so tests can shorten it.
var gitKillGrace = 2 * time.Second
```

`gitOut` 内：

```go
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.WaitDelay = gitKillGrace
```

同时更新 `gitOut` 的 doc comment：说清「ctx 到点后最多再等 `gitKillGrace` 让管道关闭」，因为这就是调用方
（安装、pull、探针）依赖的契约。**不要**引入进程组/Job Object（那是 `pkg/lsp` 为长期运行的 server 才做的；
本计划的契约只是「调用按时返回」）。

**Verify**：`grep -c "cmd.WaitDelay = gitKillGrace" pkg/skill/git_install.go` 为 `1`。

### Step 3：把探针拆成「只读缓存」与「后台预热」（`pkg/skill/git_install.go`）

在 `RemoteHead` 下面加两个导出访问器（名字照抄，测试与 `pkg/tui` 都按这两个名字写）：

```go
// CachedRemoteHead reports what a finished probe knows about repo without
// touching the network. cached=false means no probe has answered yet (or the
// last answer is older than remoteHeadTTL), which is not the same as "no
// update": a surface that must not block draws nothing and asks for a probe.
func CachedRemoteHead(repo string) (rev string, ok bool, cached bool)

// remoteHeadPrimeTimeout bounds one background probe. A package-level variable
// only so tests can shorten it.
var remoteHeadPrimeTimeout = 2 * time.Second

// PrimeRemoteHead asks for a fresh probe without waiting for it: one goroutine
// per repository at a time, and the answer lands in the cache the next call
// reads. It is how a surface that must stay interactive (the terminal's modal
// panels) asks for a hint instead of blocking on one.
func PrimeRemoteHead(repo string)
```

实现要点（照做，不要扩写）：

- `CachedRemoteHead`：`strings.TrimSpace`；空串 → `"", false, false`；持锁读 `remoteHeadCache.m`；
  条目不存在或 `time.Since(entry.at) >= remoteHeadTTL` → `"", false, false`；否则 `entry.rev, entry.ok, true`。
- 去重表：`var remoteHeadPrimeInflight = struct{ sync.Mutex; m map[string]bool }{m: map[string]bool{}}`。
- `PrimeRemoteHead`：空串直接返回；`CachedRemoteHead` 命中（fresh）就直接返回；否则在去重表里登记，
  起一个 goroutine：`ctx, cancel := context.WithTimeout(context.Background(), remoteHeadPrimeTimeout)`，
  调 `RemoteHead(ctx, repo)`（它自己会把成功/失败都写进缓存），最后删掉去重登记。**不要**把结果回传、
  不要新增加锁的读取路径、不要改 `RemoteHead` 的签名。

**Verify**：`grep -c "func CachedRemoteHead\|func PrimeRemoteHead" pkg/skill/git_install.go` 为 `2`。

### Step 4：面板只读缓存（`pkg/tui/skills_versions.go`）

把 `skillUpdateMarker` 结尾那段同步探针换成缓存读 + 后台预热：

```go
	remote, ok, cached := skill.CachedRemoteHead(repo)
	if !cached {
		// The panel is drawn on the event-loop goroutine, so it must never
		// wait on the network: ask for a probe and let the next listing read
		// the answer (the /mcp and /lsp panels work the same way).
		skill.PrimeRemoteHead(repo)
		return nil
	}
	if !ok || remote == "" || remote == rev {
		return nil
	}
	return &skill.SkillUpdate{Rev: remote}
```

同时：

- 删掉 `skillUpdateProbeTimeout` 常量（删后 `grep -rn "skillUpdateProbeTimeout" pkg/` 必须为空）。
- 改 `skillUpdateMarker` 的 doc comment：现在是「后台探针 + 下次列表读缓存」，不是「面板里探一次、缓存一小时」。
- `pkg/tui/skills_versions.go` 里 `context` 包因 `PullSkillUpdate` 仍在用，**不要**删 import。

**Verify**：`grep -rn "skill.RemoteHead" pkg/tui/` 无输出（面板路径不再有同步探针）；
`grep -c "skill.CachedRemoteHead" pkg/tui/skills_versions.go` 为 `1`。

### Step 5：跑完验收（面板必须秒开，标记必须还能出现）

先跑测试（下一节），再重跑 Step 1 的真机脚本：

```bash
export FOREBRAIN_RUN_WORK=$TMPDIR/fb-plan184 FOREBRAIN_RUN_SESSION=fb-184 FOREBRAIN_RUN_PORT=8799
D=.claude/skills/run-forebrain/driver.sh
H=$FOREBRAIN_RUN_WORK/home
# 夹具保持原样（`start` 不清 home，`reset` 才清；两条 printf 是幂等的，重跑无害）
ls "$H/skills/demo/SKILL.md" >/dev/null || {
  mkdir -p "$H/skills/demo" "$H/skills/.versions/demo"
  printf -- '---\nname: demo\ndescription: plan 184 probe\n---\n\nbody\n' > "$H/skills/demo/SKILL.md"
  printf '%s\n' '{"name":"demo","current":1,"versions":[{"v":1,"at":"2026-10-01T00:00:00Z","kind":"installed","source":"https://10.255.255.1/x.git","upstream_rev":"deadbeef"}]}' > "$H/skills/.versions/demo/manifest.json"
}
$D build
$D start reply ok
time $D submit '/skills'
time $D wait 'Run an installed skill' 3
$D screen | sed -n '1,20p'
```

**Verify**：面板在 **3 秒内**出现（`$D wait` 返回 0），屏幕出现 `Skills` 标题与 `demo` 行；
`$D stop` 之后再 `$D start`、再打开一次 `/skills`，`demo` 行仍**没有** "update available"
（source 不可达 ⇒ 探针失败 ⇒ 无标记，这是对的）。

另跑一次「标记能出现」的正例（可达的本地仓库，不依赖外网）：

```bash
cd "$(mktemp -d)" && git init -q src && mkdir -p src/demo && printf -- '---\nname: demo\ndescription: d\n---\n\nx\n' > src/demo/SKILL.md
git -C src add -A && git -C src -c user.email=t@t -c user.name=t commit -qm init
H=$FOREBRAIN_RUN_WORK/home
printf '%s\n' "{\"name\":\"demo\",\"current\":1,\"versions\":[{\"v\":1,\"at\":\"2026-10-01T00:00:00Z\",\"kind\":\"installed\",\"source\":\"file://$PWD/src\",\"upstream_rev\":\"deadbeef\"}]}" > "$H/skills/.versions/demo/manifest.json"
$D start reply ok
$D submit '/skills'; sleep 1; $D key Escape      # 第一次打开：预热
$D submit '/skills'; sleep 1; $D screen | grep -c 'update available'
$D stop
```

**Verify**：`grep -c 'update available'` ≥ `1`（第二次打开读到缓存；探针在第一次打开时后台跑完）。

## Test plan

新建 `pkg/skill/git_install_test.go`（`package skill`），四个测试：

1. `TestGitOutDeadlineHoldsWhenAGrandchildKeepsThePipe` —— 真正钉住根因。
   在 `t.TempDir()` 写一个假 `git`：`#!/bin/sh` + `sleep 5 &` + `wait`（孙进程持有管道），
   `chmod 0o755`，`t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))`；
   把 `gitKillGrace` 临时设成 `200 * time.Millisecond`（`t.Cleanup` 还原）；
   `ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)`；
   调 `gitOut(ctx, "", "ls-remote", "https://example.invalid/x.git", "HEAD")`；
   断言 `time.Since(start) < 2*time.Second`。**未修时这个测试会跑满 5 秒**（写成断言而不是靠 `-timeout`）。
2. `TestCachedRemoteHeadNeverProbes` —— 空缓存：`ForgetRemoteHeadCache()` 后
   `CachedRemoteHead("https://example.invalid/x.git")` 立即返回 `cached=false`，且假 `git`
   （把调用写进一个 marker 文件再退出）的 marker **不存在**。
3. `TestPrimeRemoteHeadFillsTheCache` —— 假 `git` 打印一个 40 位 hex 后退出；
   `ForgetRemoteHeadCache()`；`PrimeRemoteHead(repo)` 本身返回 < 100 ms；
   然后在 5 秒内轮询 `CachedRemoteHead(repo)` 直到 `cached=true` 且 `rev` 等于假 git 打印的值。
4. `TestPrimeRemoteHeadProbesOncePerRepository` —— 假 `git` 每次调用追加一行到计数文件；
   `ForgetRemoteHeadCache()`；连续 `PrimeRemoteHead(repo)` 20 次；
   轮询到 `cached=true` 后断言计数文件**恰好 1 行**（去重表生效）。

`pkg/tui/skills_versions_test.go` 追加两个测试（结构照该文件现有的纯函数测试写法）：

5. `TestSkillUpdateMarkerNeverWaitsOnTheNetwork` —— 假 `git` 脚本 `sleep 5`（可达/不可达都无所谓）；
   `t.Setenv("PATH", …)`；`skill.ForgetRemoteHeadCache()`；
   `state := skill.SkillVersionState{Current: 1, Versions: []skill.SkillVersionRecord{{V: 1, Source: "https://example.invalid/x.git", UpstreamRev: "deadbeef"}}}`（字段名以 `pkg/skill` 现结构为准）；
   `start := time.Now()`；`got := (&ChatSession{}).skillUpdateMarker("demo", state)`；
   断言 `got == nil` 且 `time.Since(start) < 100*time.Millisecond`。
6. `TestSkillUpdateMarkerReadsTheCachedAnswer` —— 用 `file://` 的本地 git 仓库
   （照 `pkg/skill/versions_test.go:750` 的 `initGitRepoWithSkill` 手法，但这里要自己建最小仓库）把缓存填上：
   `skill.ForgetRemoteHeadCache()` → `skill.RemoteHead(ctx, "file://"+repo)` 拿到 rev（`<- 5s`）；
   然后同一份 `state`（`UpstreamRev` 设成别的值）调 `skillUpdateMarker`，断言返回非 nil 且
   `Rev` 等于刚拿到的 rev；把 `UpstreamRev` 换成该 rev 再调一次，断言返回 nil。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/skill -run 'GitOut|CachedRemoteHead|PrimeRemoteHead' -count=1` 全绿；
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'SkillUpdateMarker' -count=1` 全绿。

## Done criteria

ALL must hold:

- [ ] `grep -c "cmd.WaitDelay = gitKillGrace" pkg/skill/git_install.go` 为 `1`
- [ ] `grep -c "func CachedRemoteHead\|func PrimeRemoteHead" pkg/skill/git_install.go` 为 `2`
- [ ] `grep -rn "skill.RemoteHead\|skillUpdateProbeTimeout" pkg/tui/` 无输出
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill -count=1` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/turn -count=1` exit 0
- [ ] `go vet ./...` exit 0；`gofmt -l pkg/skill pkg/tui` 无输出
- [ ] Step 5 的真机 A/B：修复前面板 8 秒内不出现，修复后 3 秒内出现；「可达 source」正例第二次打开出现 `update available`
- [ ] `git status --porcelain` 里除本计划 in-scope 文件外无新增改动（尤其 `.forebrain/skills/**` 的既有删除保持原样）
- [ ] `plans/README.md` 中本计划状态行改为 DONE

## STOP conditions

停下报告（不要即兴修）如果：

- Step 1 的基线**没有**复现（面板本来就秒开）：说明 owner 现场的触发条件与本计划假设不同（例如标记来自
  另一条路径），先把 `pkg/tui` 里所有 `skill.` 调用与 `gitOut` 的 caller 重新枚举，再报告。
- 假 `git` 脚本在你的环境里不被 `exec.LookPath` 找到（`PATH` 没生效）：先确认 `t.Setenv` 用法，仍失败则报告。
- 改完 `gitOut` 后 `pkg/skill` 的既有测试变红：说明某处依赖「ctx 到点后还能读到完整 stderr」，
  把失败的测试名与断言贴回来，不要改测试去迁就实现。
- 你发现 `pkg/tui` 里还有**别的**同步网络调用在面板路径上（`grep -rn "skill\.\|llm\.\|http\." pkg/tui/chat_slash.go`）：
  报告清单，不要顺手改（那需要自己的计划）。
- 需要动 `pkg/tui/run.go` 的派发策略才能达标。

## Maintenance notes

- 以后任何**面板/列表**路径想加「上游有没有更新」这类远端提示，只能走 `CachedRemoteHead` + `PrimeRemoteHead`；
  在 UI 循环上直接调 `RemoteHead`（或任何 git 子进程）就是本 bug 复发。
- `gitKillGrace` 是「ctx 到点后最多再等多久」，不是「git 最多跑多久」：`RemoteHead` 的真上限是
  `ctx deadline + gitKillGrace`。改小它会让管道在孙进程仍在写时被强制关闭（`exec.ErrWaitDelay`），日志里会多一条失败——
  那是可接受的（探针失败 = 无标记）。
- 被 kill 的 `git-remote-https` 仍可能作为孤儿进程继续跑（本计划只保证「调用按时返回」）。若以后要连孤儿一起收，
  照 `pkg/lsp/procgroup_{unix,windows}.go` 用进程组 / Job Object——那是独立改动，别在这里顺手做。
- 评审重点看两处：(a) `PrimeRemoteHead` 的去重表有没有泄漏（goroutine panic 时不删登记），
  (b) `skillUpdateMarker` 在 `cached && !ok`（上次探针失败）时是否安静返回 nil 而不是又去探一次。
