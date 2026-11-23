# Plan 343：TUI 交付物点击——文本文件默认走 VS Code，git 未忽略文件直开 Source Control diff（折叠未变区域）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（索引由 advisor/评审方维护）。
> 不 push、不合并回主树；合并由 owner 决定。
>
> **工作目录**：派发方指定的隔离 worktree（惯例 `.worktrees/343-vscode-deliverable-open`，从派发时 dev 尖创建）；
> 若 owner 指定在主树工作区执行则从其指示。下文所有相对路径相对仓库根。
>
> **Drift check（先跑）**：
> ```bash
> git diff --stat 4003bcb7..HEAD -- pkg/tui/image_open.go pkg/tui/image_open_test.go pkg/tui/reducer.go pkg/tui/reducer_test.go pkg/tool/shell_diff.go scripts .gitignore   # 预期：无输出
> grep -n 'var imagePreviewOpener' pkg/tui/image_open.go   # 预期：22:var imagePreviewOpener = openWithSystemViewer
> ```
> 第一条有输出 → 对照下方 "Current state" 摘录逐段核对活体代码；摘录不一致即 STOP。
> 第二条不是 `= openWithSystemViewer` → STOP（接缝已被他人改过）。
>
> **go 不在 PATH**：所有 go 命令用绝对路径 `/usr/local/go/bin/go`（go1.26.9）。cgo + fts5 标签必须带。
> **node/npx 在 PATH**（本机 v22.22.1，vsce@2.32.0 兼容）；`git`、`python3`、`unzip` 本机可用。
>
> **新鲜的 worktree 缺原生构建输入（v1 未写，v2 补）**：`third_party/pdfium/`、`third_party/libtokenizers/`
> 都是 gitignored，新 worktree 里没有；而 `pkg/tui` 的测试二进制同时链接两者（`pkg/pdf` 与
> `github.com/daulet/tokenizers`）。先跑 `scripts/fetch-libtokenizers.sh`、`scripts/fetch-pdfium.sh`，
> 每次编译都 `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(/usr/local/go/bin/go env GOOS)_$(/usr/local/go/bin/go env GOARCH)"`
> （shell 调用之间不共享环境变量）；fetch 因网络失败时可从主树只读拷入同样的两个目录（都 gitignored）。
> 另：架构门的 `TestWebBuildLoadsNoExternalAsset` 需要 `pkg/gateway/dist` 里有已构建前端（worktree 只有
> `.gitkeep`，而 `make build` 属越界+慢）——从主树把已构建的 `pkg/gateway/dist` 只读拷入即可（gitignored，
> `git status` 仍为空）。

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED（改点击路径的默认 opener；但所有失败路径都回落到现状行为，最坏＝回到 OS 默认打开）
- **Depends on**: none
- **Category**: direction（owner 需求）/ UX
- **Planned at**: commit `4003bcb7`, 2026-11-20
- **Reviewed at**: commit `4d65d015`, 2026-11-21（improve `review-plan`：全部锚点核对零漂移；修复基线探针的 macOS 符号链接 bug、Test 1 root 断言、聚焦命令转义等，详见索引当日条目。drift 基线仍用 `4003bcb7`，已核实 in-scope 路径至 `4d65d015` 零改动）
- **v2 修订**: 2026-11-21，`execute 343` 第一次执行（worktree `.worktrees/343-vscode-deliverable-open`，分支
  `343-vscode-deliverable-open`，三笔 commit `473d7f3a`/`fa53d12f`/`016899c0`）——Steps 1–5 完成（聚焦 21 PASS、
  0 FAIL），**Step 6 包级门红**在一个 v1 没预见的既有测试上，执行器按计划 STOP 未即兴改生产代码（做法正确）。
  根因＝v1 Current state §1 对直调点 `image_open_test.go:909` 的普查结论错误，且 `isTextFile` 缺"已知二进制
  扩展"负向规则；两处已就地更正（§1 更正段、Step 3 代码块、Step 5 第 8 条、STOP 3、维护说明）。
  **修订后的补丁只动 in-scope 的新文件 `vscode_open.go` 与 in-scope 的新测试文件**，既有测试零改动；
  修法已用 `-overlay` 复现验证（该测试 0.16s 由红转绿、8 个新测试同跑 2.78s 全绿、零 FAIL）。
- **Executed at**: 分支 `343-vscode-deliverable-open`（worktree `.worktrees/343-vscode-deliverable-open`），
  基线 `4d65d015` + 四笔 commit `473d7f3a`/`fa53d12f`/`016899c0`/`08658aef`；2026-11-21 评审 **APPROVE**
  （评审方独立复跑：聚焦 21 PASS、launcher 0.11s、pkg/tui 197.903s、architecture 2.082s、vet ./... exit 0、
  gofmt 空、VSIX 内嵌源与源文件逐字一致、假 `code` 零调用；`-overlay` 反证负向表为转绿唯一原因）。
  **未 push、未合并**（owner 决定）。**Step 7 真机验收 NOT RUN**（无人值守；URI 一次性确认 dialog 需人点）——
  owner 需在有真 VS Code 的机器上逐腿跑，并注意扩展尚未安装进其 VS Code。
- **Merged at**: 2026-11-21，owner 指令「合并回主树 dev 分支」→ rebase 到当时 dev `ac09eb14`（零冲突；两边文件集
  不相交）→ 门禁复跑（gofmt 空、`vet ./...` exit 0、聚焦 22 PASS/0 FAIL、`pkg/tui` 197.907s 零 FAIL、架构门除他人
  文件外无新增红、VSIX manifest 与内嵌源核对一致）→ **ff-only 落 dev `1e1767ae`**（七文件 +917/−6，未 push）。
  合并后主树复跑：`go build` exit 0、gofmt 空、聚焦 20 PASS/0 FAIL、假 `code` 零调用。**合并后 dev 的
  `pkg/architecture` 仍红，但红在他人文件**——`pkg/run/peer_delivery_origin_test.go`（随 dev `ac09eb14` 落地：
  3 个下划线超上限、缺配对生产文件），已复现在**纯净 dev**（不含本分支）上，与本计划无关，已告知对方修。
  分支 `343-vscode-deliverable-open` 与 worktree `.worktrees/343-vscode-deliverable-open` 已按配方删除。

## Why this matters

owner 原话需求：现在 TUI 里点击交付物列表的文件会用 OS 默认软件打开；期望**本机装了 VS Code 时所有文本类型文件
（markdown、代码等）默认用 VS Code 打开**；且**当前项目是 git 仓库、被点文件未被 git 忽略时，直接打开 VS Code 的
Source Control diff 视图并开启 collapse unchanged regions**（owner 截图：diff 编辑器标题 `subagent.go (Working Tree)`、
折叠条 `106 hidden lines`）。交付物是 agent 刚改出来的文件，"改了哪几行"才是点击者第一想看的东西；OS 默认打开
（macOS 上 .md 常落到别的编辑器、.go 落到 Xcode/TextEdit）把这个意图完全丢掉。

实现路线已由 owner 裁决：**随附迷你扩展**——仓库内置一个 ~100 行 VS Code 扩展（VSIX 用 go:embed 随二进制分发），
TUI 发现缺失时 `code --install-extension --force` 自动安装，再用 `vscode://` 深链调扩展的 URI handler，
由扩展调 `git.openChange` 打开真 SCM diff 并聚焦 SCM 视图、确保 `diffEditor.hideUnchangedRegions.enabled` 开启；
任何一环失败自动降级 `code --diff` → 普通 `code -r` → OS 默认打开。**纯 CLI 做不到 SCM 视图**（见 Current state
的已核实事实），扩展是唯一外部入口，这是本计划存在的原因。

## Current state

### 1. 点击链与测试接缝（改这里，不改调用点）

`pkg/tui/image_open.go` —— 交付物/图片点击的唯一 opener 接缝与 OS 打开实现：

```go
// pkg/tui/image_open.go:17-22（注释省略首行）
// imagePreviewOpener launches a file with the operating system's viewer. It is
// a variable so a test can inject a fake and assert whether (and what) it was
// asked to open; production uses the platform's own handler. An error it
// returns is a launch that never happened; failed is called, from another
// goroutine, when a launch that did start ends unsuccessfully.
var imagePreviewOpener = openWithSystemViewer
```

```go
// pkg/tui/image_open.go:33-45
func openWithSystemViewer(path string, failed func(error)) error {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return errors.New("file no longer exists")
		}
		return err
	}
	name, args := imageViewerCommand(path)
	if name == "" {
		return fmt.Errorf("no system viewer is available on this platform")
	}
	return startDetached(failed, name, args...)
}
```

`startDetached`（`:59-77`）Start + goroutine reap + `headBuffer` stderr，失败经 `failed` 回报；
`imageViewerCommand`（`:95-104`）darwin=`open`、windows=`cmd /c start`、其余=`xdg-open`。**这两个函数原样复用，不改。**

```go
// pkg/tui/image_open.go:139-153（注释省略）
func openImagePreview(renderer *Renderer, path, label string) {
	if renderer == nil || strings.TrimSpace(path) == "" {
		return
	}
	report := func(err error) {
		renderer.ShowComposerHint(
			llm.ExplainError(fmt.Errorf("open %s: %w", label, err)),
			llm.ExplainError(err),
			composerHintTimeout,
		)
	}
	if err := imagePreviewOpener(path, report); err != nil {
		report(err)
	}
}
```

两个点击调用点都在**释放 `r.mu` 之后**调 `openImagePreview`（`pkg/tui/reducer.go:6814-6817` 与 `:7989-7997`）；
交付物行 label→path 的解析在 `transcriptClickLocked`（`reducer.go:6844-6850`，相对 label join `r.cwd`）。
**调用点与解析逻辑零改动**：把接缝默认值换成新路由函数即可同时覆盖两条点击路径。
注意调用点跑在输入循环 goroutine 上——现状 opener 只 Start 不等侯所以不卡；**新路由的 git 探针与 CLI 调用必须放进
goroutine**（见 Step 3），否则一次点击卡输入loop数秒。

测试接缝（既有测试全部经它注入 fake，故改默认值不碰任何既有测试）：

```go
// pkg/tui/image_open_test.go:24-34
func stubImageOpener(t *testing.T) *[]string {
	t.Helper()
	prev := imagePreviewOpener
	var opened []string
	imagePreviewOpener = func(path string, _ func(error)) error {
		opened = append(opened, path)
		return nil
	}
	t.Cleanup(func() { imagePreviewOpener = prev })
	return &opened
}
```

- `pkg/tui/image_open_test.go:819-834` `fakeViewer(t, body)`：把与平台 launcher 同名的脚本插到 PATH 最前（`t.Setenv`），返回脚本目录。新测试照它写 `code` 的 fake（但用接缝变量注入路径，见 Step 3）。
- `pkg/tui/image_open_test.go:852-861` `hintRowsFor(r, width)`：读 composer hint 行（stripANSI+trim）。
- `pkg/tui/image_open_test.go:781-815` `TestOpenWithSystemViewerLetsTheViewerOutliveTheHandler`：轮询 marker 文件等异步腿的范式（10s deadline、25ms 间隔）。
- `pkg/tui/reducer_test.go:6344-6353` `deliverableClickRenderer` + `:6374` `TestDeliverableClickOpensTheClickedFile`：点击交付物行的端到端范式（**必须保持绿且零改动**）。
- 直调 `openImagePreview` 而不 stub 的既有测试已普查（2026-11-20 grep 全仓，2026-11-21 复核并**更正一处**）：四处——
  `image_open_test.go:193` 与 `:877` 是缺文件（stat 同步报错，opener 不被调）；`:331` 在 stub 作用域内；
  `:909` `TestALauncherThatRefusesTheFileIsReported` 见下方更正。
  除 `:909` 外，切换默认值不会让任何既有测试真开 VS Code；若执行时发现新的直调点属文本路径且无 stub → STOP。

  > **更正（2026-11-21 执行期实测，v1 本节的 `:909` 结论是错的）**：该测试写入的 payload 是**4 字节**
  > `"\x89PNG"`（`image_open_test.go:902`、`:901` 文件名 `shot.png`），不是 8 字节 PNG magic。
  > 实测：`utf8.Valid("\x89PNG")` = false、`http.DetectContentType("\x89PNG")` = `"text/plain; charset=utf-8"`
  > （8 字节 magic 才是 `image/png`）→ 计划的 `isTextFile` 判**文本** → 走 VS Code 文本腿 → `code -r <tmp>/shot.png`
  > 成功、`fakeViewer` 从不跑、`failed` 从不回调 → 该测试红（实测 `--- FAIL … (10.02s)`，假 `code` argv 留下
  > `-r <tmp>/…/shot.png` 证据；overlay 把接缝改回 `openWithSystemViewer` 则 0.11s 绿）。
  > 这是**计划的生产代码缺陷**（扩展名白名单缺"已知二进制扩展"这条负向规则：截断/损坏的图片、嗅探器认不出的
  > 二进制会被塞进文本编辑器），不是测试写错。修法＝Step 3 新增 `deliverableBinaryExts` 负向白名单，在扩展名
  > 白名单之后、内容嗅探之前判定 `false`——既有测试零改动转绿（overlay 实测：该测试 0.16s 绿、8 个新测试与
  > 全部既有相关测试同跑 2.78s 全绿、且假 `code` 未被调用一次），同时修掉真实交付物里的同类误路由。

### 2. 仓库先例（照抄形状，不 import 私有helper）

- git 探针超时先例：`pkg/tool/shell_diff.go:296-305` `shellDiffRepoFor` 用 **2s** timeout 跑
  `git -C cwd rev-parse --path-format=absolute --show-toplevel …`；环境先例 `:188-190`
  `shellDiffDiscoveryEnv() = shellDiffScrubEnv() + GIT_TERMINAL_PROMPT=0 + GIT_OPTIONAL_LOCKS=0`。
  本计划只做只读本地探针（rev-parse / check-ignore / show），env 用 `os.Environ()` 追加
  `GIT_TERMINAL_PROMPT=0`、`GIT_OPTIONAL_LOCKS=0` 即可，**不要 import pkg/tool 的私有函数**。
- 临时目录清扫先例：`pkg/tool/shell_diff.go:257-278` `shellDiffSweep`（pid 前缀目录、只扫别人 pid、24h 未动才删）。
- go:embed 先例：`pkg/state/db.go:26` `//go:embed schema.sql`。**反面先例**：`pkg/gateway/http_server.go:709`
  `//go:embed all:dist` 的 dist 是 gitignored（靠 `.gitkeep` 占位）——**本计划的 VSIX 必须 git tracked**，
  否则 fresh clone 编不过。已核实 `.gitignore` 无 vsix 规则（只有 `.worktrees/`、`plans/` 等）；若执行时发现
  `git check-ignore pkg/tui/vscode_ext/deliverable-open.vsix` 有输出 → STOP。
- 内容嗅探先例：`pkg/llm/llm.go:237-247` `ImageMIMEType` 只嗅前 512 字节、扩展名不参与判定（注释写明理由）。
  本计划的文本判定方向相反但同形：扩展名白名单优先 + 前 8KB 嗅探兜底。
- 测试配对硬门：`pkg/architecture` 的 `cache_test.go:1498` `TestTestFilesCorrespondToProductionFiles` 强制
  `X_test.go` 配对 `X.go` → 新生产文件 `vscode_open.go` 必须带 `vscode_open_test.go`（本计划已含）。
- 注释房式：长段 "why" 散文注释解释取舍与历史bug（见 image_open.go 各函数头）；新代码照此写少量 why 注释，不写 what 注释。
- 全局接缝变量的测试**不得 t.Parallel**（既有 stub 测试均串行）。

### 3. VS Code 侧已核实事实（2026-11-20 在本机 VS Code 1.145.3 真机 + bundle 源码核实，勿再质疑/重验路线）

1. **CLI 没有执行命令/开 SCM 的入口**。`code --help` 全量核过：只有 `--diff f1 f2`、`-r/--reuse-window`、
   `--install-extension <id|vsix> [--force]`、`--list-extensions [--show-versions]`、`--open-url`、`--status` 等。
   内置 git 扩展的 URI handler 只认 `vscode://vscode.git/clone`。**开 SCM diff 的唯一外部入口＝第三方扩展的
   `registerUriHandler`**（`vscode://{publisher}.{name}/{path}?{query}`）。
2. **`git.openChange` 接受 `vscode.Uri` 参数**，内部 `getSCMResource` 用 `resourceUri.toString()` 字符串匹配
   SCM resource states；**states 未populate时静默 no-op**（真机复现：新窗口里 repo 对象已存在但 workingTreeChanges
   还空 → 调用"成功"却什么都不开）。→ 扩展必须先轮询 `repo.state` 的四个变更数组等到文件出现再调（Step 2 代码已含）。
   未跟踪文件在 `untrackedChanges` 里，同样能开 diff（对空基线）。
3. **聚焦 SCM 视图的命令是 `workbench.scm.focus`**（view id `workbench.scm`，`.focus` 命令动态生成；
   `workbench.view.scm.focus` **不存在**——真机报 command not found）。
4. **`diffEditor.hideUnchangedRegions.enabled` 在 1.145.3 默认 true**（bundle 配置默认 `enabled:!0`），
   但用户全局设置可能显式 false（owner 本机当时就是 false）→ 扩展发现 false 时 update 成 true（Global target）。
   这正是 owner 要的"开启 collapse unchanged regions"。只翻 false→true，不碰其它键。
5. **外部 URI 有一次性确认 dialog**："Allow 'Forebrain Deliverable Open' extension to open this URI?"
   （Cancel / Open / 复选框 "Do not ask me again for this extension"）。**不点 Open handler 就不跑**（真机截图证实）。
   这是 VS Code 的安全设计，无设置可关；勾选复选框后同扩展后续静默。计划无法绕过，UX 上接受：首次点击多一次确认。
6. **多窗口派发**：`--open-url` 投给**最后活跃窗口**；`code -r <folder>` 会聚焦已持有该 folder 的窗口
   （真机核实）→ 先 `-r root` 再 `--open-url` 把派发钉到项目窗口。两调用之间用户抢焦点仍可能投错窗口：
   投错时该窗口无此 repo → 扩展兜底 `showTextDocument` 普通打开（ graceful，不报错）。
7. **运行中实例的窗口缓存扩展扫描**：`--install-extension --force` 后**已开窗口仍跑旧版本**，新窗口才扫到新版
   （真机日志 "Extensions added from another source" + 旧窗口 handler 跑旧代码证实）。→ 扩展升级后需新窗口/reload
   才生效（maintenance notes）；安装判定用 `--list-extensions --show-versions` 比对版本，不一致就 `--force` 重装。
8. **`code --status` 有运行实例时 exit 0** 并打印 Version/OS 行（真机核实）；用作冷启动 ready 探针（best-effort）。
9. **VSIX 打包配方已真机跑通**：`npx --yes @vscode/vsce@2.32.0 package --allow-missing-repository --out <vsix>`
   （目录里只需 package.json + extension.js；无 repository/LICENSE 只警告不阻断）。
10. `code -r <file>` 把文件开进最后活跃窗口；`code -r --diff <base> <file>` 同窗口开双栏 diff（降级腿用）。
11. macOS 上 `code` 通常在 PATH（`/usr/local/bin/code`）；不在时的兜底绝对路径
    `/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code`（本机存在，已核实）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 格式门 | `gofmt -l cmd pkg third_party plugins` | 无输出 |
| vet | `/usr/local/go/bin/go vet -tags fts5 ./...` | exit 0 |
| 聚焦测试 | 见表下方围栏块「聚焦测试命令」（勿从表格单元格里复制——`\|` 是 markdown 转义） | 数字 ≥ 12（既有腿+新增腿，见 Step 5 清单），且同命令不带 grep 跑 exit 0、零 FAIL |
| 包级 | `FOREBRAIN_HOME=$(mktemp -d) /usr/local/go/bin/go test -tags fts5 ./pkg/tui -count=1 -timeout 10m` | ok（~200s；满负载撞超时则单跑复核，先例见索引） |
| 架构门 | `FOREBRAIN_HOME=$(mktemp -d) /usr/local/go/bin/go test -tags fts5 ./pkg/architecture -count=1` | ok |
| 扩展语法 | `node --check pkg/tui/vscode_ext/extension.js` | exit 0 |
| 脚本语法 | `bash -n scripts/build-vscode-deliverable-ext.sh` | exit 0 |
| 打包 | `bash scripts/build-vscode-deliverable-ext.sh` | exit 0，vsix 存在 |
| VSIX manifest | `python3 -c "import zipfile,json;z=zipfile.ZipFile('pkg/tui/vscode_ext/deliverable-open.vsix');m=json.loads(z.read('extension/package.json'));print(m['publisher']+'.'+m['name'],m['version'])"` | `forebrain.deliverable-open 0.1.0` |
| tracked 检查 | `git check-ignore pkg/tui/vscode_ext/deliverable-open.vsix; printf 'exit=%s\n' $?` | `exit=1`（未被忽略） |

聚焦测试命令（逐字复制这一块；`-run` 的交替是裸 `|`）：

```bash
FOREBRAIN_HOME=$(mktemp -d) /usr/local/go/bin/go test -tags fts5 ./pkg/tui -run 'TestOpenDeliveredFile|TestIsTextFile|TestDeliverableExtVSIX|TestDeliverableClickOpensTheClickedFile|TestClickingTheMarkerOfAFileSinceDeleted|TestOpenWithSystemViewerLetsTheViewerOutlive' -count=1 -v 2>&1 | grep -c -- '--- PASS:'
```

同一命令去掉尾部 `| grep -c -- '--- PASS:'` 再跑一遍：必须 exit 0 且输出零 FAIL（计数门与全绿门都要过）。

## Scope

**In scope**（只许改/建这些）：
- `pkg/tui/image_open.go` —— **仅** `:22` 接缝默认值与其头注释（换成 `openDeliveredFile`）
- `pkg/tui/vscode_open.go`（新建）—— 路由 + git 探针 + CLI 调用 + embed
- `pkg/tui/vscode_open_test.go`（新建）
- `pkg/tui/vscode_ext/package.json`（新建）
- `pkg/tui/vscode_ext/extension.js`（新建）
- `pkg/tui/vscode_ext/deliverable-open.vsix`（新建，**构建产物但 git tracked**）
- `scripts/build-vscode-deliverable-ext.sh`（新建）

**Out of scope**（看着相关也不许碰）：
- `pkg/tui/reducer.go` 两个点击调用点与 `transcriptClickLocked` —— 接缝已覆盖，改它们是无谓风险
- `openWithSystemViewer` / `startDetached` / `imageViewerCommand` / `openImagePreview` 函数体 —— 原样复用
- `pkg/tui/reducer_test.go`、`pkg/tui/image_open_test.go` 既有测试 —— 必须零改动保持绿；新测试全进 `vscode_open_test.go`
- `pkg/tool/shell_diff.go` —— 只抄形状，不 import、不修改
- web 前端 / gateway 的交付物打开行为 —— 本期只治 TUI（owner 需求原文即 TUI）
- `.gitignore` —— 已核实无需改；若发现 vsix 被忽略 → STOP 报告，不要自行加例外
- 用户 VS Code 的其它设置 —— 扩展只翻 `diffEditor.hideUnchangedRegions.enabled` 一个键（false→true）
- VS Code Insiders / Cursor 等 fork 的探测 —— 本期只认 stable `code`

## Git workflow

- 分支：worktree 分支 `343-vscode-deliverable-open`（派发方创建）；commit 按逻辑单元，message 照仓库 conventional
  commits 房式（例：`feat(tui): open delivered text files in VS Code with SCM diff`、`test(tui): pin deliverable open routing`）。
- 不 push、不开 PR、不合并；owner 手动合并（索引房式）。
- 若派发方明确「不提交、改动留工作区」（339–342 批次的约定），从其指示，其余不变。

## Steps

### Step 1：锚点核对

跑头部 drift check 与下列 grep，逐条对预期：

```bash
grep -n 'func openWithSystemViewer' pkg/tui/image_open.go          # 预期 :33
grep -n 'func openImagePreview' pkg/tui/image_open.go              # 预期 :139
grep -n 'openImagePreview(r, imagePath, imageLabel)' pkg/tui/reducer.go   # 预期 :6816 与 :7997 两行
grep -n 'func stubImageOpener' pkg/tui/image_open_test.go          # 预期 :24
grep -n 'func fakeViewer' pkg/tui/image_open_test.go               # 预期 :819
grep -n '2\*time.Second' pkg/tool/shell_diff.go                    # 预期含 :300
```

**Verify**：全部命中且行号相符（±2 行内按内容核对）；任一内容不符 → STOP。

### Step 2：扩展源文件 + 打包脚本 + 构建 VSIX

建 `pkg/tui/vscode_ext/package.json`（逐字）：

```json
{
  "name": "deliverable-open",
  "displayName": "Forebrain Deliverable Open",
  "description": "Opens Forebrain TUI deliverables as Source Control diffs with unchanged regions collapsed.",
  "version": "0.1.0",
  "publisher": "forebrain",
  "engines": { "vscode": "^1.85.0" },
  "categories": ["Other"],
  "activationEvents": ["onUri"],
  "main": "./extension.js"
}
```

建 `pkg/tui/vscode_ext/extension.js`（逐字；逻辑已真机验证过原型 v0.0.2，此处去掉 /tmp 日志、加兜底普通打开）：

```js
const vscode = require('vscode');
const fs = require('fs');

function real(p) { try { return fs.realpathSync(p); } catch (e) { return p; } }

async function gitAPI() {
  const ext = vscode.extensions.getExtension('vscode.git');
  if (!ext) { return null; }
  const exports = await ext.activate();
  return exports.getAPI(1);
}

// The git extension exposes a repository object before `git status` populates
// its resource states, and git.openChange silently no-ops on a resource it
// cannot find — so wait for the file to appear in a change list first.
async function waitForChange(api, root, path, deadlineMs) {
  const deadline = Date.now() + deadlineMs;
  let matched = null;
  while (Date.now() < deadline) {
    for (const repo of api.repositories) {
      const rr = real(repo.rootUri.fsPath);
      if (rr !== root && !root.startsWith(rr + '/')) { continue; }
      matched = repo;
      const s = repo.state;
      const all = [...s.workingTreeChanges, ...s.indexChanges, ...s.mergeChanges, ...s.untrackedChanges];
      if (all.some(c => real(c.uri.fsPath) === path)) { return { repo, changed: true }; }
    }
    await new Promise(res => setTimeout(res, 250));
  }
  return { repo: matched, changed: false };
}

function activate(context) {
  context.subscriptions.push(vscode.window.registerUriHandler({
    async handleUri(uri) {
      const q = new URLSearchParams(uri.query);
      const path = real(q.get('path') || '');
      const root = real(q.get('root') || '');
      if (!path) { return; }
      try {
        const cfg = vscode.workspace.getConfiguration('diffEditor');
        if (cfg.get('hideUnchangedRegions.enabled') === false) {
          await cfg.update('hideUnchangedRegions.enabled', true, vscode.ConfigurationTarget.Global);
        }
        const api = await gitAPI();
        const found = api ? await waitForChange(api, root, path, 10000) : { repo: null, changed: false };
        if (found.changed) {
          await vscode.commands.executeCommand('git.openChange', vscode.Uri.file(path));
          await vscode.commands.executeCommand('workbench.scm.focus');
          return;
        }
        // Clean file, ignored file, or a window that does not hold the repo:
        // a plain editor is the graceful answer, never a silent no-op.
        await vscode.window.showTextDocument(vscode.Uri.file(path));
      } catch (e) {
        vscode.window.showWarningMessage('Forebrain: could not open the Source Control diff: ' + e);
      }
    }
  }));
}

module.exports = { activate };
```

建 `scripts/build-vscode-deliverable-ext.sh`（逐字，0755）：

```bash
#!/usr/bin/env bash
# Rebuilds the embedded VSIX after editing pkg/tui/vscode_ext/{package.json,extension.js}.
# The .vsix is git-tracked: go:embed needs it present in a fresh clone.
set -euo pipefail
cd "$(dirname "$0")/../pkg/tui/vscode_ext"
# Build outside the source dir: a .vsix left inside it would be packaged into
# the next .vsix by vsce.
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
npx --yes @vscode/vsce@2.32.0 package --allow-missing-repository --out "$tmp/deliverable-open.vsix"
mv -f "$tmp/deliverable-open.vsix" deliverable-open.vsix
```

**Verify**：
- `node --check pkg/tui/vscode_ext/extension.js` → exit 0
- `bash -n scripts/build-vscode-deliverable-ext.sh` → exit 0
- `bash scripts/build-vscode-deliverable-ext.sh` → exit 0（npx 首跑下载 vsce，属预期）
- Commands 表「VSIX manifest」→ `forebrain.deliverable-open 0.1.0`
- Commands 表「tracked 检查」→ `exit=1`；`git status --porcelain pkg/tui/vscode_ext` 列出三个新文件（vsix 未被忽略）

### Step 3：新建 `pkg/tui/vscode_open.go`（路由主体）

目标形状如下（逐字可用；注释为房式 why 注释）。要点：stat 与文本嗅探同步做（快），git/CLI 链进 goroutine；
接缝契约不变——返回 error＝"从未启动"，goroutine 内失败经 `failed` 回报；所有降级腿静默，只有全链失败才报 hint。

```go
package tui

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// deliverableExtID and deliverableExtVersion name the bundled extension; the
// version is compared against `code --list-extensions --show-versions` so a
// rebuilt VSIX actually reaches the user (a running window keeps its scanned
// version until it reloads — see the plan's maintenance notes).
const (
	deliverableExtID      = "forebrain.deliverable-open"
	deliverableExtVersion = "0.1.0"
)

//go:embed vscode_ext/deliverable-open.vsix
var deliverableExtVSIX []byte

// vscodeCLIPath is a variable so a test can point the router at a fake `code`
// (or at nothing) without touching PATH or the real installation.
var vscodeCLIPath = findVSCodeCLI

func findVSCodeCLI() string {
	if p, err := exec.LookPath("code"); err == nil {
		return p
	}
	if runtime.GOOS == "darwin" {
		const p = "/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code"
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// openDeliveredFile is the default imagePreviewOpener: text files go to VS
// Code (Source Control diff when the project is a git repo and the file is not
// ignored), everything else keeps the platform viewer. The stat and the sniff
// run here; the git probes and CLI calls run in a goroutine because the click
// sites call the opener on the input loop, and a probe that waits on git must
// not hold the terminal hostage.
func openDeliveredFile(path string, failed func(error)) error {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return errors.New("file no longer exists")
		}
		return err
	}
	if !isTextFile(path) {
		return openWithSystemViewer(path, failed)
	}
	code := vscodeCLIPath()
	if code == "" {
		return openWithSystemViewer(path, failed)
	}
	go func() {
		if err := openTextInVSCode(code, path, failed); err != nil && failed != nil {
			failed(err)
		}
	}()
	return nil
}

// openTextInVSCode walks the fallback ladder: SCM diff via the bundled
// extension, then a plain --diff against the index/HEAD baseline, then a plain
// editor, then the platform viewer. Only the last rung's failure is worth a
// hint; the earlier rungs fall through silently because a later rung still
// opens the file.
func openTextInVSCode(code, path string, failed func(error)) error {
	if root, err := deliverableGitRoot(filepath.Dir(path)); err == nil {
		if ignored, ok := deliverableGitIgnored(root, path); ok && !ignored {
			if err := openDeliverableSCMDiff(code, root, path); err == nil {
				return nil
			}
			if err := openDeliverableDiffFallback(code, path); err == nil {
				return nil
			}
		}
	}
	if err := runVSCodeCLI(code, 10*time.Second, "-r", path); err == nil {
		return nil
	}
	return openWithSystemViewer(path, failed)
}

func openDeliverableSCMDiff(code, root, path string) error {
	if err := ensureDeliverableExt(code); err != nil {
		return err
	}
	// -r focuses the window already holding the project; --open-url then lands
	// in that window because the URI goes to the last-active one.
	if err := runVSCodeCLI(code, 15*time.Second, "-r", root); err != nil {
		return err
	}
	waitVSCodeReady(code)
	link := "vscode://" + deliverableExtID + "/change?path=" + url.QueryEscape(path) + "&root=" + url.QueryEscape(root)
	return runVSCodeCLI(code, 10*time.Second, "--open-url", link)
}

var deliverableExtMu struct {
	sync.Mutex
	installed string
}

func ensureDeliverableExt(code string) error {
	deliverableExtMu.Lock()
	defer deliverableExtMu.Unlock()
	if deliverableExtMu.installed == deliverableExtVersion {
		return nil
	}
	if out, err := runVSCodeCLIOutput(code, 20*time.Second, "--list-extensions", "--show-versions"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if strings.TrimSpace(line) == deliverableExtID+"@"+deliverableExtVersion {
				deliverableExtMu.installed = deliverableExtVersion
				return nil
			}
		}
	}
	dir, err := os.MkdirTemp("", "forebrain-deliverable-ext-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	vsix := filepath.Join(dir, "deliverable-open.vsix")
	if err := os.WriteFile(vsix, deliverableExtVSIX, 0o600); err != nil {
		return err
	}
	if err := runVSCodeCLI(code, 60*time.Second, "--install-extension", vsix, "--force"); err != nil {
		return err
	}
	deliverableExtMu.installed = deliverableExtVersion
	return nil
}

// waitVSCodeReady is best-effort: on a cold start the window may still be
// scanning extensions when the URI arrives. --status answers once an instance
// is up; exhausting the deadline proceeds anyway rather than failing the open.
func waitVSCodeReady(code string) {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := runVSCodeCLIOutput(code, 3*time.Second, "--status"); err == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// openDeliverableDiffFallback reproduces the Working Tree diff without the
// extension: baseline is the index version, then HEAD, then empty (untracked).
// The base file outlives the CLI call because a cold-started instance reads it
// asynchronously; leftovers are swept by pid/age like shell_diff's temp dirs.
func openDeliverableDiffFallback(code, path string) error {
	base := deliverableBaseContent(path)
	dir, err := os.MkdirTemp("", "forebrain-deliverable-diff-")
	if err != nil {
		return err
	}
	sweepDeliverableDiffDirs()
	basePath := filepath.Join(dir, "base-"+filepath.Base(path))
	if err := os.WriteFile(basePath, base, 0o600); err != nil {
		return err
	}
	return runVSCodeCLI(code, 15*time.Second, "-r", "--diff", basePath, path)
}

var deliverableSweepOnce sync.Once

func sweepDeliverableDiffDirs() {
	deliverableSweepOnce.Do(func() {
		entries, err := os.ReadDir(os.TempDir())
		if err != nil {
			return
		}
		cutoff := time.Now().Add(-24 * time.Hour)
		prefix := fmt.Sprintf("forebrain-deliverable-diff-%d-", os.Getpid())
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), prefix) {
				continue
			}
			if !strings.HasPrefix(e.Name(), "forebrain-deliverable-diff-") {
				continue
			}
			info, err := e.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			os.RemoveAll(filepath.Join(os.TempDir(), e.Name()))
		}
	})
}

func deliverableGitRoot(dir string) (string, error) {
	out, err := runDeliverableGit(dir, 2*time.Second, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	top := strings.TrimSpace(out)
	if top == "" {
		return "", errors.New("not a git work tree")
	}
	return top, nil
}

// deliverableGitIgnored reports (ignored, known): check-ignore exits 0 for an
// ignored path and 1 for a tracked-or-untracked-but-not-ignored one; any other
// outcome is unknown and the caller takes the plain-open rung.
func deliverableGitIgnored(root, path string) (bool, bool) {
	_, err := runDeliverableGit(root, 2*time.Second, "check-ignore", "-q", "--", path)
	if err == nil {
		return true, true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		switch ee.ExitCode() {
		case 0:
			return true, true
		case 1:
			return false, true
		}
	}
	return false, false
}

// deliverableBaseContent reads the index version, then HEAD, then nothing
// (untracked). The specs are ./-relative and run from the file's own directory
// because git resolves symlinks in the working-tree path while the clicked
// path keeps them: on macOS (/var → /private/var, /tmp → /private/tmp) a
// repo-root-relative path computed from the clicked path lands "outside
// repository" (exit 128, reproduced 2026-11-21). The ./ form lets git resolve.
func deliverableBaseContent(path string) []byte {
	dir, name := filepath.Dir(path), filepath.Base(path)
	for _, spec := range []string{":./" + name, "HEAD:./" + name} {
		if out, err := runDeliverableGit(dir, 2*time.Second, "show", spec); err == nil {
			return []byte(out)
		}
	}
	return nil
}

func runDeliverableGit(dir string, timeout time.Duration, sub ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := append([]string{"-C", dir}, sub...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	return string(out), err
}

func runVSCodeCLI(code string, timeout time.Duration, args ...string) error {
	_, err := runVSCodeCLIOutput(code, timeout, args...)
	return err
}

func runVSCodeCLIOutput(code string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, code, args...).CombinedOutput()
	if err != nil {
		if said := strings.TrimSpace(string(out)); said != "" {
			return "", errors.New(said)
		}
		return "", err
	}
	return string(out), nil
}

// deliverableTextExts names the extensions that are text by convention; the
// sniff below decides everything else (extensionless Makefile, odd suffixes).
var deliverableTextExts = map[string]bool{
	".md": true, ".markdown": true, ".txt": true, ".text": true, ".log": true,
	".csv": true, ".tsv": true, ".go": true, ".mod": true, ".sum": true,
	".js": true, ".mjs": true, ".cjs": true, ".ts": true, ".tsx": true, ".jsx": true,
	".json": true, ".jsonc": true, ".yaml": true, ".yml": true, ".toml": true,
	".ini": true, ".cfg": true, ".conf": true, ".properties": true,
	".sh": true, ".bash": true, ".zsh": true, ".fish": true,
	".py": true, ".rb": true, ".rs": true, ".lua": true, ".pl": true, ".php": true,
	".c": true, ".h": true, ".cpp": true, ".hpp": true, ".cc": true, ".cxx": true,
	".java": true, ".kt": true, ".kts": true, ".swift": true, ".m": true, ".mm": true,
	".cs": true, ".fs": true, ".vb": true, ".r": true, ".jl": true, ".dart": true,
	".ex": true, ".exs": true, ".erl": true, ".hs": true, ".ml": true, ".scala": true, ".sc": true,
	".html": true, ".htm": true, ".css": true, ".scss": true, ".less": true,
	".xml": true, ".svg": true, ".vue": true, ".svelte": true, ".astro": true,
	".sql": true, ".graphql": true, ".gql": true, ".proto": true,
	".tf": true, ".hcl": true, ".nix": true, ".gradle": true, ".cmake": true, ".mk": true,
	".patch": true, ".diff": true, ".env": true, ".lock": true,
	".gitignore": true, ".gitattributes": true, ".editorconfig": true, ".dockerfile": true,
}

// deliverableBinaryExts names the extensions that are never text, whatever
// their first 8KB look like: a truncated or corrupt image still belongs in the
// image viewer, and Go's content sniffer answers text/plain for high bytes that
// are not control characters, so the payload alone cannot decide this. Keep
// .bin/.dat and other ambiguous suffixes OUT of this map — they must keep the
// sniff below, which is what TestIsTextFileSniffMatrix pins.
var deliverableBinaryExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".bmp": true, ".ico": true, ".tif": true, ".tiff": true, ".heic": true, ".avif": true,
	".pdf": true, ".zip": true, ".gz": true, ".tgz": true, ".bz2": true, ".xz": true,
	".7z": true, ".rar": true, ".tar": true,
	".docx": true, ".xlsx": true, ".pptx": true,
	".mp3": true, ".mp4": true, ".mov": true, ".avi": true, ".mkv": true, ".flac": true,
	".wav": true, ".ogg": true, ".webm": true, ".m4a": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true,
	".sqlite": true, ".db": true, ".silk": true,
}

// isTextFile decides "VS Code or the platform viewer": a known text extension
// wins outright, a known binary extension loses outright (the sniff cannot be
// trusted there — see the map above), and everything else has its first 8KB
// sniffed — a NUL byte means binary, valid UTF-8 means text, and the remainder
// goes to the content sniffer the image path already trusts
// (llm.ImageMIMEType's http.DetectContentType).
func isTextFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if deliverableTextExts[ext] {
		return true
	}
	switch strings.ToLower(filepath.Base(path)) {
	case "makefile", "dockerfile", "justfile", "rakefile", "gemfile":
		return true
	}
	if deliverableBinaryExts[ext] {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, _ := f.Read(buf)
	if n == 0 {
		return true
	}
	buf = buf[:n]
	if bytes.IndexByte(buf, 0) >= 0 {
		return false
	}
	if utf8.Valid(buf) {
		return true
	}
	return strings.HasPrefix(http.DetectContentType(buf), "text/")
}
```

**Verify**：`/usr/local/go/bin/go vet -tags fts5 ./pkg/tui` → exit 0（此时接缝还未切换，新函数暂无调用方亦无测试，vet 仍过）。
若 `go build` 报 embed 找不到文件 → 回 Step 2 确认 vsix 已构建（不要改 embed 路径）。

### Step 4：切换接缝默认值（唯一对既有文件的改动）

`pkg/tui/image_open.go:17-22` 改为：

```go
// imagePreviewOpener launches a file with the viewer that fits it: text files
// go to VS Code (Source Control diff when git tracks them), everything else to
// the operating system's viewer. It is a variable so a test can inject a fake
// and assert whether (and what) it was asked to open. An error it returns is a
// launch that never happened; failed is called, from another goroutine, when a
// launch that did start ends unsuccessfully.
var imagePreviewOpener = openDeliveredFile
```

**Verify**：
- `gofmt -l cmd pkg third_party plugins` → 无输出
- `FOREBRAIN_HOME=$(mktemp -d) /usr/local/go/bin/go test -tags fts5 ./pkg/tui -run 'TestDeliverableClickOpensTheClickedFile|TestClickingTheMarkerOfAFileSinceDeletedSaysItInTheHintRow|TestOpenWithSystemViewerLetsTheViewerOutliveTheHandler|TestImageViewerCommandIsPlatformSpecific|TestALauncherThatRefusesTheFileIsReported' -count=1 -v 2>&1 | grep -c -- '--- PASS:'` → ≥ 5（含子测试腿则更多；关键：零 FAIL、exit 0）
  —— 既有点击/缺文件/outlive/launcher-refusal 测试经 stub 或经 OS 腿注入，**必须零改动全绿**；若有红 → STOP（接缝契约被破坏）。
- `TestALauncherThatRefusesTheFileIsReported` 是本版新增的门：它直调 `openImagePreview`（`image_open_test.go:909`）且
  payload 是 4 字节 `"\x89PNG"`，只有 `deliverableBinaryExts` 负向白名单在位时才会走 OS 腿并通过（0.16s 绿）；
  缺它则 10s 后红（v1 的历史失败，证据见 Current state §1 更正）。
- 跑这条门时**先把一个假 `code` 放到 PATH 最前**（例：`printf '#!/bin/sh\nexit 0\n' > /tmp/fakebin/code && chmod +x`，
  然后 `PATH=/tmp/fakebin:$PATH`）：红态下该测试会真的 `code -r <临时文件>`，在装了真 VS Code 的机器上会弹出
  一个临时文件标签页（v1 执行期实测发生过）。

### Step 5：新建 `pkg/tui/vscode_open_test.go`

范式：`fakeViewer`（PATH 注入 OS launcher）、`stubImageOpener`（接缝恢复）、outlive 测试的轮询等侯。
`code` 的 fake 不走 PATH 而走 `vscodeCLIPath` 接缝（避免 darwin 兜底绝对路径绕过 fake 真开 VS Code）。
git 用真 git + `t.TempDir()` 仓库（`exec.LookPath("git")` 失败则 `t.Skip`）。**全部串行，不得 t.Parallel。**

helper（逐字）：

```go
package tui

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeCodeCLI installs a script the vscodeCLIPath seam points at. It records
// every invocation's argv (one arg per line, invocations separated by "---")
// and answers --list-extensions / --install-extension from env knobs so a test
// can steer the install rung without a real VS Code. FB_CODE_ALL_EXIT makes
// every invocation fail (the dead-ladder test). The process-wide install cache
// is reset here and on cleanup so tests cannot inherit each other's state.
func fakeCodeCLI(t *testing.T, listBody string, installExit string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake code CLI is a /bin/sh script")
	}
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	script := filepath.Join(dir, "code")
	body := "#!/bin/sh\n[ -n \"$FB_CODE_ALL_EXIT\" ] && exit \"$FB_CODE_ALL_EXIT\"\n" +
		"printf '%s\\n' \"$@\" >> \"$FB_CODE_ARGV\"\nprintf '%s\\n' '---' >> \"$FB_CODE_ARGV\"\n" +
		"case \"$1\" in\n" +
		"  --list-extensions) printf '%s' \"$FB_CODE_LIST\" ;;\n" +
		"  --install-extension) exit \"$FB_CODE_INSTALL_EXIT\" ;;\n" +
		"esac\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FB_CODE_ARGV", argv)
	t.Setenv("FB_CODE_LIST", listBody)
	t.Setenv("FB_CODE_INSTALL_EXIT", installExit)
	t.Setenv("FB_CODE_ALL_EXIT", "")
	resetExtCache := func() {
		deliverableExtMu.Lock()
		deliverableExtMu.installed = ""
		deliverableExtMu.Unlock()
	}
	resetExtCache()
	prev := vscodeCLIPath
	vscodeCLIPath = func() string { return script }
	t.Cleanup(func() { vscodeCLIPath = prev; resetExtCache() })
	return script, argv
}

func codeInvocations(t *testing.T, argv string) [][]string {
	t.Helper()
	data, err := os.ReadFile(argv)
	if err != nil {
		return nil
	}
	var out [][]string
	for _, chunk := range strings.Split(string(data), "---\n") {
		chunk = strings.TrimSuffix(chunk, "\n")
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		out = append(out, strings.Split(chunk, "\n"))
	}
	return out
}

func waitForCodeArg(t *testing.T, argv, needle string) [][]string {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		inv := codeInvocations(t, argv)
		for _, args := range inv {
			if strings.Contains(strings.Join(args, " "), needle) {
				return inv
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("no code invocation containing %q; got %v", needle, codeInvocations(t, argv))
	return nil
}

// deliverableTestRepo builds a git repo with one committed-then-modified file,
// one untracked file and one ignored file — the three routing cases.
func deliverableTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@example.invalid")
	git("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "a.md", ".gitignore")
	git("commit", "-qm", "base")
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("one\nTWO\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.md"), []byte("fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.md"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}
```

测试清单（名字逐字，全仓已 grep 零冲突）：

1. `TestOpenDeliveredFileGitRouting` —— 子测试 `tracked change opens the SCM deep link` /
   `untracked file opens the SCM deep link` / `ignored file opens plain` / `non-git dir opens plain`：
   `fakeCodeCLI(t, deliverableExtID+"@"+deliverableExtVersion+"\n", "0")`；对前两者
   `waitForCodeArg(t, argv, "--open-url")` 后断言最后一invocation 形如
   `["--open-url", "vscode://forebrain.deliverable-open/change?path=…&root=…"]` 且 query 解码后
   path == 传给 `openDeliveredFile` 的文件路径原值、root == `git -C <repoDir> rev-parse --show-toplevel`
   输出 TrimSpace（用 `net/url` 解析）。**root 必须对 git 的物理路径断言，不得对 `t.TempDir()` 字符串断言**：
   macOS 上 git 解析符号链接（`/var/folders/…` → `/private/var/folders/…`），两者不等（2026-11-21 实测）。
   并断言存在 `-r <root>` invocation 排在 open-url 之前、**不存在** `--install-extension`
   （版本已匹配）。ignored/non-git 腿：等 `-r` invocation 出现后断言**无** `--open-url`、无 `--diff`，
   且 `-r` 的参数是文件路径本身。
2. `TestOpenDeliveredFileInstallFailureFallsBackToDiff` —— `fakeCodeCLI(t, "", "1")`（list 空、install 拒）；
   `waitForCodeArg(t, argv, "--diff")`；取 `--diff` 后第一个参数（base 文件）读内容断言 == `"one\ntwo\nthree\n"`
   （index/HEAD 基线），第二个参数 == 文件路径。
3. `TestOpenDeliveredFileBinaryKeepsSystemViewer` —— `fakeViewer(t, ": > \"$0.ran\"\n")` +
   `fakeCodeCLI(t, "", "0")`；写 PNG magic（`\x89PNG\r\n\x1a\n`+垃圾）文件；调 `openDeliveredFile(path, nil)`；
   轮询 viewer marker 出现；断言 `codeInvocations` 为空（文本路由根本没启动）。
4. `TestOpenDeliveredFileNoVSCodeKeepsSystemViewer` —— `vscodeCLIPath` stub 返 `""`（t.Cleanup 恢复）+
   `fakeViewer` marker；文本文件；断言 marker 出现、无 code 调用。
5. `TestOpenDeliveredFileMissingFileReportsSynchronously` —— 调 `openDeliveredFile(不存在路径, rec)`，
   断言**同步返回** error 文案 `file no longer exists`、rec 未被调（契约：返回 error＝从未启动）。
6. `TestOpenDeliveredFileEveryRungDeadFallsBackToSystemViewer` —— 文件用 `deliverableTestRepo(t)` 的 `a.md`；
   `fakeCodeCLI(t, "", "1")` 后
   `t.Setenv("FB_CODE_ALL_EXIT", "1")`（helper 已内置该旋钮：所有 invocation exit 1）；
   `fakeViewer` marker；断言 marker 出现（OS 兜底腿活）。
7. `TestDeliverableExtVSIXManifestMatchesConstant` —— `zip.NewReader(bytes.NewReader(deliverableExtVSIX), …)`
   读 `extension/package.json`，断言 `publisher+"."+name == deliverableExtID`、`version == deliverableExtVersion`。
8. `TestIsTextFileSniffMatrix` —— 表驱动：`x.md`/`x.go`/`Makefile`（无扩展名 utf8）/空文件 → true；
   PNG magic / 含 NUL 的 "text" / 非法 utf8 二进制（`\x01\x02\xfe\xfd`，实测 octet-stream）→ false。
   **v2 更正（v1 这条写错了，实测见 Current state §1 更正）**：`\xff\xfe\xfa` **不是** false——`utf8.Valid` 为
   false 但 `http.DetectContentType` 判 `text/plain; charset=utf-8`，按生产规则应为 **true**（UTF-16 带 BOM 的
   文本也正是这种情况）；高字节非控制字符时 Go 嗅探器一律答 text/plain，故"非法 utf8 就二进制"不成立。
   v2 新增行：`{"truncated.png", []byte("\x89PNG"), false}` —— 钉住"扩展名是二进制就永不走文本腿"，
   与 payload 无关（这正是 v1 缺失的规则）；`.bin`/`.dat` 这类歧义后缀**不进**负向白名单，
   继续由 `nul.bin` / `control.bin` 两行钉嗅探行为。

**Verify**：Commands 表「聚焦测试」→ exit 0 且 `--- PASS:` 计数 ≥ 12（既有 4 条含子腿 + 新增 8 名字含子腿）；
`grep -c 'func Test' pkg/tui/vscode_open_test.go` → 8。

### Step 6：包级与架构门

**Verify**：
- Commands 表「包级」→ ok（pkg/tui 全量，~200s）
- Commands 表「架构门」→ ok（新文件配对门过）
- Commands 表「vet」全仓 → exit 0；「格式门」→ 无输出

### Step 7：真机验收（需人在场；无人值守则如实记 NOT RUN，先例 337 Step 8）

在 owner 机器（有真 VS Code）按序跑，scratch 全部建在 `/tmp/fb343-accept/`：

```bash
REPO_ROOT=$(pwd)
mkdir -p /tmp/fb343-accept/repo && cd /tmp/fb343-accept/repo
git init -q && git config user.email t@example.invalid && git config user.name t
python3 -c "open('a.md','w').write(''.join(f'line {i}\n' for i in range(160)))"
git add a.md && git commit -qm base
python3 -c "p='a.md';s=open(p).read().splitlines(True);s[80]='CHANGED\n';open(p,'w').write(''.join(s))"
printf 'ignored.md\n' > .gitignore && printf 'x\n' > ignored.md && printf 'new\n' > new.md
/usr/local/bin/code --install-extension "$REPO_ROOT/pkg/tui/vscode_ext/deliverable-open.vsix" --force
/usr/local/bin/code -r /tmp/fb343-accept/repo
/usr/local/bin/code --open-url 'vscode://forebrain.deliverable-open/change?path=%2Ftmp%2Ffb343-accept%2Frepo%2Fa.md&root=%2Ftmp%2Ffb343-accept%2Frepo'
```

 acceptance 断言（人眼/截图）：
- 首次深链弹确认 dialog → 点 **Open** 并勾 "Do not ask me again for this extension"；
  随后出现 diff 编辑器标题 `a.md (Working Tree)`、**折叠条 `… hidden lines`**、SCM 视图聚焦（左侧源控图标亮）。
- 重发同一条 `--open-url` → **不再弹 dialog**、diff 重现（重复交互稳定）。
- `new.md`（untracked）同法深链 → diff 对空基线全绿加行。
- `ignored.md`：走 TUI 路由语义＝普通编辑器（可直接 `code -r ignored.md` 核对无 diff）。
- 非 git 目录文本文件、二进制 png：TUI 里点击分别普通 VS Code / OS viewer（或按 Step 5 自动化腿已钉，真机抽一点）。
- 冷启动腿：退出 VS Code 后重跑 `-r` + `--open-url` 两条 → diff 仍出现（waitVSCodeReady 腿）。
- 收尾：`code --uninstall-extension forebrain.deliverable-open`；scratch 窗口请 owner 手动关（CLI 无关闭命令）；
  `rm -rf /tmp/fb343-accept`。**注意**：扩展会把 owner 全局 `diffEditor.hideUnchangedRegions.enabled` 从 false 翻 true
  ——这正是需求本身，保留不回滚。

**Verify**：截图存证＋本步结论写进执行报告（RUN/NOT RUN 逐腿）。

### Step 8：范围审计

```bash
git status --porcelain   # 只允许 in-scope 七路径（+ 派发方 worktree 自身产物如 .gocache 属 gitignored 不算）
git diff --stat 4003bcb7 -- pkg/tui/image_open.go   # 预期仅接缝行与头注释（~+7/-6）
```

**Verify**：越界文件 → STOP 报告（不要自行回滚他人改动）。

## Test plan

- 新测试全在 `pkg/tui/vscode_open_test.go`（8 个 Test 函数，清单见 Step 5），结构照
  `image_open_test.go` 的 `fakeViewer`/轮询范式与 `reducer_test.go` 的交付物点击范式。
- 既有必须保持绿且零改动：`TestDeliverableClickOpensTheClickedFile`、
  `TestClickingTheMarkerOfAFileSinceDeletedSaysItInTheHintRow`、
  `TestOpenWithSystemViewerLetsTheViewerOutliveTheHandler`、`TestImageViewerCommandIsPlatformSpecific`。
- 反证（评审方用，执行者可选）：`go test -overlay` 把 seams 默认值改回 `openWithSystemViewer` →
  新路由测试红、既有测试绿（证明新测试钉的是路由而非旧行为）。
- 反证（v2，评审方用）：`-overlay` 去掉 `deliverableBinaryExts` 整段 → `TestALauncherThatRefusesTheFileIsReported`
  红、其余绿（证明负向白名单是这条测试转绿的唯一原因，不是别的巧合）。
- 验证：Commands 表聚焦/包级/架构三门全绿。

## Done criteria

- [ ] Commands 表全部命令达预期（格式门无输出、vet exit 0、聚焦/包级/架构 ok）
- [ ] `grep -c 'func Test' pkg/tui/vscode_open_test.go` = 8；聚焦跑 `--- PASS:` 计数 ≥ 12 且零 FAIL
- [ ] `TestALauncherThatRefusesTheFileIsReported` 绿（0.2s 量级，不是 10s 超时红），且 `pkg/tui/image_open_test.go` 零 diff
- [ ] `isTextFile` 按"文本白名单 → 无扩展名白名单 → 二进制白名单 → 嗅探"四段顺序实现；
      `TestIsTextFileSniffMatrix` 含 `truncated.png`（4 字节 `\x89PNG`）→ false 一行
- [ ] `git diff 4003bcb7 -- pkg/tui/image_open.go` 仅接缝默认值与头注释；`reducer.go`/既有两测试文件零 diff
- [ ] VSIX manifest = `forebrain.deliverable-open 0.1.0` 且 `git check-ignore` 该 vsix exit 1
- [ ] `node --check` / `bash -n` exit 0
- [ ] Step 8 范围审计零越界
- [ ] Step 7 真机验收逐腿 RUN 并截图，或如实 NOT RUN 留痕
- [ ] 不 push/不合并；commit 留 worktree 分支（或按派发方「留工作区」约定）

## STOP conditions

1. drift check 有输出且活体代码与 "Current state" 摘录不符（接缝/调用点/先例被改）。
2. `git check-ignore pkg/tui/vscode_ext/deliverable-open.vsix` exit 0（vsix 被忽略，fresh clone 会编不过）。
3. Step 4 既有点击/缺文件/outlive/**launcher-refusal**（`TestALauncherThatRefusesTheFileIsReported`）测试任一红
   （接缝契约被破坏，或 `deliverableBinaryExts` 负向白名单缺失/顺序错）。
4. 任一验证命令合理修复后仍失败两次。
5. 需要改 out-of-scope 文件才能完成（尤其 reducer.go 调用点、shell_diff.go、.gitignore）。
6. `pkg/architecture` 因新文件报配对/分层红且修法超出 in-scope（例如要求改 architecture 测试本身）。
7. 真机验收发现扩展 handler 不跑且原因不是确认 dialog/焦点竞争（例如 VS Code 版本 URI 语义变化）——记录版本与现象报告，不要改路线。

## Maintenance notes

- **扩展升级流程**：改 `extension.js`/`package.json` → bump version（两处：package.json 与 Go 常量
  `deliverableExtVersion`）→ 跑 `scripts/build-vscode-deliverable-ext.sh` → 提交 vsix。
  `ensureDeliverableExt` 的版本比对会触发 `--force` 重装，但**已开窗口仍跑旧版直到 reload/新窗口**
  （VS Code 运行实例缓存扩展扫描，真机核实）——用户报"修了没生效"先问是否 reload。
- **确认 dialog** 按扩展 id 记一次性同意；勾复选框后静默。VS Code 若改 URI 同意策略（bundle 里
  `Allow … extension to open this URI` 逻辑），本路线的 UX 随之变，属上游漂移。
- **命令 id 漂移**：`git.openChange`、`workbench.scm.focus`、git API v1 均为 VS Code 内部契约
  （bundle 核实于 1.145.3）；大版本升级后若 SCM 腿静默失败，扩展会兜底普通打开（不炸），按 STOP 7 流程重核。
- **多窗口焦点竞争**：`-r root` 与 `--open-url` 之间用户抢焦点 → URI 投错窗口 → 扩展兜底普通打开。
  已知 graceful 降级，不修（修需要窗口级 IPC，超出 CLI 能力）。
- **hideUnchangedRegions** 只翻 false→true（Global）。owner 若日后手动关回去，下次点击会再翻 true——
  这是需求语义（"开启 collapse unchanged regions"），不是 bug；如 owner 改主意改成 Workspace target 或尊重用户，另立计划。
- **临时基线文件**（--diff 腿）留 pid 前缀目录、24h 清扫（shellDiffSweep 同形）；冷启动实例异步读基线，
  故调用返回后不立即删——review 时勿"优化"成 defer RemoveAll。
- **基线探针的 `:./name` 形状勿改回 repo 根相对**：git 解析工作树路径里的符号链接而点击路径不解析，
  macOS 上 `t.TempDir()`（`/var/folders/…`）与 `/tmp` 都会和物理 root（`/private/…`）错位，
  `filepath.Rel` 算出的 `../` 链让 `git show :rel`/`HEAD:rel` 报 "outside repository"（exit 128，
  2026-11-21 scratch 复现）；`-C 文件所在目录 show :./name` 让 git 自己解析，已实测可用。
  同理 Test 1 的 root 断言对 `rev-parse --show-toplevel` 的物理路径，不对 `t.TempDir()` 字符串。
- **URI handler 的残余风险（已知，owner 本地工具可接受）**：一次性确认勾选后，任何 `vscode://forebrain.deliverable-open/…`
  深链都静默生效——恶意网页可构造链接让编辑器打开任意本机路径（只读视图）或翻那一个设置键。
  本期不防（防需要 handler 侧路径白名单，如只认最近交付物清单）；**若日后把该扩展复用到不可信/多用户面，
  必须先加白名单**。确认 dialog 本身是 VS Code 的安全闸，见 Current state §3.5。
- **web 端交付物打开**本期不动（owner 需求原文限 TUI）；如要平价，另立计划复用同一扩展与深链格式。
- **`isTextFile` 的两张表是一对，别只改一张**：文本扩展白名单（正向）→ 无扩展名文件名白名单 →
  二进制扩展白名单（负向 `deliverableBinaryExts`）→ 内容嗅探。负向表存在的原因：Go 的内容嗅探器对
  "高字节但非控制字符"的 payload 一律答 `text/plain`（实测 4 字节 `"\x89PNG"` → `text/plain; charset=utf-8`），
  所以嗅探不足以把损坏/截断的图片挡在文本编辑器外。新增二进制格式 → 只往 `deliverableBinaryExts` 加一行；
  **`.bin`/`.dat`/`.log` 等歧义后缀不要进负向表**（它们必须继续由嗅探决定，`TestIsTextFileSniffMatrix` 的
  `nul.bin`/`control.bin` 两行就是这条边界的哨兵）。
- **红态验证会真开 VS Code**：`TestALauncherThatRefusesTheFileIsReported` 直调 `openImagePreview` 且不 stub，
  路由正确时走 OS 腿、路由错误时真的执行 `code -r <临时文件>`。跑聚焦/包级门时先放一个假 `code` 到 PATH 最前。
- 评审重点：接缝契约（返回 error vs failed 回调）是否被新路由遵守；goroutine 是否泄漏（每条点击一个、
  全腿有 timeout）；fake code 测试是否可能真开 VS Code（vscodeCLIPath 接缝必须覆盖 darwin 兜底路径）。