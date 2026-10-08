# Plan: 修复 npm 打包脚本静默内嵌过期前端（脚本先重建 UI）— v2（吸收评审）

> 状态：**已批准实施**（2026-10-08 owner 批准，实施于批准后当前会话；验收记录见文末）。
>
> 日期：2026-10-08（v2 同日）
>
> 范围：npm 打包脚本 `npm/scripts/build-platform-packages.sh`、`Makefile` release 目标、
> `.github/workflows/release.yml` build job env、官网 build-and-release.md（EN/ZH）env 表
>
> 基线：工作区 `eafaf46`，工作树干净，`npm/dist` 被 `.gitignore:16` 忽略。
> Drift check 已通过（`git diff --stat eafaf46..HEAD -- <六个文件>` 输出为空）。

> **Executor instructions**: 逐步执行本计划。每步先跑验证命令并确认预期结果，再进入下一步。
> 触发任一 STOP condition 立即停下报告，不要即兴发挥。改动全部留在工作区，**禁止任何
> git commit / merge / rebase**（owner 手动审阅提交）。本计划经 owner 批准后即为实施依据。

> **v2 变更**（外部评审 deepseek-v4-flash，逐条已独立核实采纳）：①验收脚本 heredoc 语法
> 错误修正；②UI 重建移到 `rm -rf npm/dist` **之前**（make ui 失败不再毁掉上一份可用产物）；
> ③证据链更正（b7eda22 仅测试文件，936df40 才是漏掉的源码提交）；④引用官网文档
> 152-153 行已发布契约为根因证据，并把新 env 键补进官网中英 env 表；⑤失败分支带
> FOREBRAIN_SKIP_UI_BUILD 出路提示 + dist 发版管理提醒；⑥`-tags fts5`；⑦Done criteria
> 标注时点；⑧验收配方镜像 web_e2e.sh:138 的 `git init`。

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: MED（触及官方 release 流水线的打包入口；改动面小但语义关键）
- **Depends on**: none
- **Category**: bug（build/packaging）
- **Planned at**: 工作区 `eafaf46`，2026-10-08（v2 同日）
- **批准后实施位置**: 本仓库当前源码直接改（owner 裁决，不用 worktree、不 commit）。

## Why this matters

owner 的本地调试流（README.md:227-238 文档化）：`./npm/scripts/build-platform-packages.sh`
→ `cd npm && npm link` → `forebrain gateway start`。该脚本当前**不重建前端**，只检查
`pkg/gateway/dist/index.html` 存在（build-platform-packages.sh:43），于是把仓库里上次
release 提交的旧 dist 静默编译进"最新"二进制。今天（10-08 18:13）的产物实证：Go 代码是
最新的 eafaf46，内嵌 UI 却是 0.1.1（2026-09-29, 2e67fe8）时代的——浏览器自然显示旧页面。
且这不是"补一个没写过的行为"：官网文档（本仓库 `forebrain-harness.github.io/`）**早已发布
了相反的契约**——EN 版 build-and-release.md:152-153 "The script builds the frontend
**once** up front and embeds it into every platform binary"（ZH 版 :145-146 "脚本把前端
**只构建一次**，然后内嵌进每个平台的二进制"）。代码违背已发布文档，本修复让代码兑现文档。

## 根因（file:line 证据链 + 现场证据）

1. `pkg/gateway/http_server.go:746` — `//go:embed all:dist`：前端从 `pkg/gateway/dist`
   编译进每个二进制。
2. `npm/scripts/build-platform-packages.sh:9-11` — 头注释自述 "The web UI is not built
   here"；`:43` 只做**存在性**检查，无法区分新鲜/过期。
3. 契约证据：`forebrain-harness.github.io/docs/develop/build-and-release.md:152-153` 与
   `docs/zh/develop/build-and-release.md:145-146`（引文见上）——文档说脚本重建前端，代码没有。
4. 现场证据（2026-10-08 核实，外部评审独立复核确认）：
   - 工作树 `pkg/gateway/dist` = 提交 `2e67fe8`（2026-09-29, release 0.1.1）内容，`git status` 干净；其 `index.html` 引用 `assets/index-Dc10V6pS.js`（0.1.1 时代 hash）；
   - 其后改了 **frontend 源码**（进 bundle）的提交：`936df40`(10-05：PendingActionsPanel.vue、
     useChatStream.ts、CronSettingsTab.vue、locales/index.ts)、`6d433ad`(10-07：AgentViewTabs.vue、
     PlanProgressSegments.vue、RunWorkedLine.vue 等)、`1ec4a3b`(10-07：lib/api.ts)。
     （`b7eda22`(10-08) 对 frontend/ 只有 *.test.ts，不进 bundle，不算 UI 可见差异——v1 误列，更正。）
   - `npm/dist/@forebrain-harness/forebrain-darwin-arm64/vendor/aarch64-apple-darwin/bin/forebrain`
     mtime=10-08 18:13，`go version -m` = `v0.1.2-0.20261008101221-eafaf46339a4`（Go 代码最新），
     且二进制内 grep 命中 `assets/index-Dc10V6pS.js`（= 0.1.1 旧 UI，评审独立复核一致）；
   - 全局 `forebrain` = npm link → 本仓库 `npm/`；launcher `npm/bin/forebrain.js` 解析：
     ①`npm/node_modules/@forebrain-harness`（不存在）→ ②`npm/dist/@forebrain-harness/forebrain-darwin-arm64`
     （存在=脚本产物）→ 用户运行的正是 18:13 的脚本产物。
5. 结论：Go 新、UI 旧（0.1.1），症状（浏览器旧页面）与证据完全吻合。根因 = 打包脚本缺
   "先重建 UI"（已发布契约 + Makefile:12-13 自述不变量 "`make build` always rebuilds the
   UI first so the binary serves the current frontend"），存在性检查放行过期 dist。
6. 同类入口穷举（评审确认无遗漏）：CI 侧唯一调用方是 release.yml:110（tag checkout，dist
   由 webui-into-release-pr 保证新鲜）；`make build`（build: ui）、`make docker`（Dockerfile
   内建前端）、acceptance（web_e2e.sh 走 `FOREBRAIN_STATIC_DIST` 独立路径）都不缺这条语义。

## Current state（现场摘录，执行前已核对一致）

**`npm/scripts/build-platform-packages.sh`**（bash，`set -euo pipefail`；`ROOT_DIR`=仓库根）：

```bash
# 行 9-11（头注释）
# The web UI is not built here: pkg/gateway/dist must already contain a build
# (`make ui`, or the release workflow) because go:embed compiles it into every
# platform binary.

# 行 19-26（env 文档块，以 FOREBRAIN_VERSION 起头、FOREBRAIN_CC_DARWIN_AMD64 收尾）
# Env:
#   FOREBRAIN_VERSION            — package version (default: the VERSION file's content)
#   ...（FOREBRAIN_BUILD_TARGETS / FOREBRAIN_CC_* 共 7 行）

# 行 39-43（注意顺序：先删 npm/dist，后检查——本计划要调整这个顺序）
DIST_DIR="$NPM_DIR/dist"
rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"

[ -f "$ROOT_DIR/pkg/gateway/dist/index.html" ] || { echo "pkg/gateway/dist has no web UI build; run make ui first" >&2; exit 1; }
```

**`Makefile`**：

```make
# 行 8-9
#   make release  build per-platform npm packages (runs make ui first; the UI
#                 is embedded in each binary)
# 行 12-13（不变量自述）
# The frontend is embedded via go:embed (pkg/gateway). `make build` always
# rebuilds the UI first so the binary serves the current frontend.
# 行 31-36（ui 目标，单一事实源，本计划复用、不改）
ui:
	find $(WEBUI_DIST) -mindepth 1 ! -name .gitkeep -exec rm -rf {} +
	cd frontend && [ -d node_modules ] || CI=true corepack pnpm install --frozen-lockfile
	cd frontend && corepack pnpm build
# 行 61-64
## release: build per-platform npm packages. Depends on ui: the script embeds
## whatever web UI build is in pkg/gateway/dist.
release: ui
	FOREBRAIN_VERSION=$(VERSION) npm/scripts/build-platform-packages.sh
```

**`.github/workflows/release.yml`** build job（5 平台矩阵，defaults shell bash，只 setup Go、
无 Node/pnpm；windows runner 无 make）：

```yaml
      - run: test -f pkg/gateway/dist/index.html
      - run: scripts/check-release-versions.sh
      - name: Build the platform package
        env:
          FOREBRAIN_BUILD_TARGETS: ${{ matrix.target }}
          FOREBRAIN_VERSION: ${{ needs.release-please.outputs.version }}
          # Read by the script only when it cross-compiles darwin/amd64 on the
          # arm64 runner; empty everywhere else.
          FOREBRAIN_CC_DARWIN_AMD64: ${{ matrix.darwin_amd64_cc }}
        run: npm/scripts/build-platform-packages.sh
```

**官网（本仓库 `forebrain-harness.github.io/`，VitePress 源码）**：

```markdown
<!-- docs/develop/build-and-release.md:152-153 -->
The script builds the frontend **once** up front and embeds it into every
platform binary, so each package ships the complete UI.

<!-- docs/develop/build-and-release.md:157-160（env 表） -->
| Variable | Default | Purpose |
| --- | --- | --- |
| `FOREBRAIN_VERSION` | `0.1.0` | Package and binary version. |
| `FOREBRAIN_BUILD_TARGETS` | host triple | Space-separated targets, e.g. `"darwin/arm64 linux/amd64"`. |

<!-- docs/zh/develop/build-and-release.md:145-146 / 150-153（中文镜像，同构） -->
脚本把前端**只构建一次**，然后内嵌进每个平台的二进制，所以每个包都带完整 UI。
…（env 表同上，中文表头）
```

**`scripts/check-webui-dist.sh`**（CI 门禁，ci.yml:176 调用）：非 release-please PR 改动
`pkg/gateway/dist` 一律失败——本地重建的 dist 绝不能进 PR。

## 修复设计（行为变化矩阵）

脚本默认重建前端（复用 `make ui`，不复制其命令），为"dist 已确保新鲜"的调用方留显式
跳过开关；重建发生在清除 `npm/dist` **之前**，失败不毁上一份产物：

| # | 场景 | 旧行为 | 新行为 |
|---|------|--------|--------|
| 1 | 本地直接跑 `./npm/scripts/build-platform-packages.sh` | 存在性检查放行过期 dist，静默内嵌旧 UI（**本 bug**） | 先 `make -C "$ROOT_DIR" ui` 重建，再编译（当前源码 UI 进包），与官网文档 152-153 对齐 |
| 2 | `FOREBRAIN_SKIP_UI_BUILD=1` + 脚本 | 开关不存在 | 跳过重建，保留原存在性 fail-fast（供 CI 等已确保 dist 新鲜的调用方） |
| 3 | `make ui` 失败（pnpm/网络/无 make） | 直接跑脚本路径不存在 make ui；经 make release 跑则脚本不启动、npm/dist 不动 | 打印失败原因 + `FOREBRAIN_SKIP_UI_BUILD=1` 出路提示，exit 1；**npm/dist 上一份产物保留**（重建先于 `rm -rf`）——刻意强化，不劣于任何旧路径 |
| 4 | `make release` | `release: ui` 先建 UI 再调脚本 | 脚本自带 UI 重建；`release:` 去掉 `ui` 前置，避免 pnpm build 跑两遍（刻意修订，owner 批准本计划即接受） |
| 5 | release CI build job（tag checkout） | 直接跑脚本（tag dist 已新鲜） | env 加 `FOREBRAIN_SKIP_UI_BUILD: "1"`，行为等价；不要求 job 具备 Node/pnpm/make |
| 6 | `make build` / `make ui` / `make docker` | 不变 | 不变 |
| 7 | 本地跑完后的 `pkg/gateway/dist` | —（make ui/make build 本就令其 dirty） | 同样 dirty；脚本 echo 提醒"发版管理、勿提交本地重建（check-webui-dist.sh 会拒）"；脚本绝不执行 git 命令，收口时人工还原 |
| 8 | 官网文档 | 152-153/145-146 与代码相反（文档欠账） | 代码兑现正文；env 表补 `FOREBRAIN_SKIP_UI_BUILD` 一行（EN+ZH） |

## Scope

**In scope**（只允许改这些文件）：
- `npm/scripts/build-platform-packages.sh`
- `Makefile`（仅 release 目标注释与依赖、头注释 8-9 两行）
- `.github/workflows/release.yml`（仅 build job "Build the platform package" 步骤的 env）
- `forebrain-harness.github.io/docs/develop/build-and-release.md`（仅 env 表加一行）
- `forebrain-harness.github.io/docs/zh/develop/build-and-release.md`（仅 env 表加一行）
- `docs/plan/NPM_PACKAGE_UI_REBUILD_PLAN.md`（新建，本计划的仓库惯例落盘）

**Out of scope**（看着相关，明确不碰）：
- `pkg/gateway/http_server.go`、`pkg/gateway/dist/`（go:embed 机制与 dist 内容——dist 只能
  经 release automation 提交）
- `frontend/` 任何源码
- `npm/bin/forebrain.js`（launcher 解析顺序正确，不是本 bug 的一部分）
- `scripts/acceptance/web_e2e.sh`（走 `FOREBRAIN_STATIC_DIST` 的独立路径，语义不同）
- `scripts/check-release-versions.sh`、本仓库 README.md（命令用法没变，只是行为补全）
- 官网页面**既有 drift**：`internal/webui/dist`（:131/:172 等，实际路径是 `pkg/gateway/dist`）、
  env 表里 `FOREBRAIN_VERSION` 默认值写 `0.1.0`（实际是读 VERSION 文件）等——均为先于本
  bug 的文档欠账，只在此报告，不顺手修（owner 裁决过拒绝"while I'm here"式扩张）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 脚本语法 | `bash -n npm/scripts/build-platform-packages.sh` | exit 0 |
| YAML 校验 | `python3 -c 'import yaml; yaml.safe_load(open(".github/workflows/release.yml")); print("yaml ok")'` | `yaml ok` |
| 打包（默认） | `./npm/scripts/build-platform-packages.sh` | 输出含 `==> building the web UI`，exit 0 |
| 打包（跳过 UI） | `FOREBRAIN_SKIP_UI_BUILD=1 ./npm/scripts/build-platform-packages.sh` | 无 UI 构建输出，exit 0 |
| release 依赖静态检查 | `make -n release` | 输出不含 `pnpm build`/`find pkg/gateway/dist`（即不再前置 ui） |

## Steps

### Step 0: 誊抄计划到仓库惯例位置

把本计划全文复制为 `docs/plan/NPM_PACKAGE_UI_REBUILD_PLAN.md`（参照既有计划如
`docs/plan/SKILL_MESSAGE_CARD_UI_PLAN.md` 的结构；Status 加"已批准实施"行）。

**Verify**: `test -f docs/plan/NPM_PACKAGE_UI_REBUILD_PLAN.md && echo ok` → `ok`

### Step 1: `npm/scripts/build-platform-packages.sh` 自带 UI 重建（先重建、后清产物目录）

四处修改（复用 `make ui` 为单一事实源，不内联其命令）：

a) 头注释行 9-11 替换为：

```bash
# The web UI is rebuilt here by default (`make ui`) so every platform binary
# embeds the current frontend. Set FOREBRAIN_SKIP_UI_BUILD=1 to embed the
# pkg/gateway/dist already in the tree instead (the release pipeline does
# this: its tag carries the UI committed by the release PR).
```

b) env 文档块（行 19-26）在 `FOREBRAIN_BUILD_TARGETS` 行后加一行：

```bash
#   FOREBRAIN_SKIP_UI_BUILD      — set to 1 to skip the web UI rebuild and embed pkg/gateway/dist as-is
```

c) 行 39-43 整块替换为（**UI 重建/检查移到 `rm -rf "$DIST_DIR"` 之前**：make ui 失败时
上一份 npm/dist 产物完好，owner 的 npm link 调试流不被打断）：

```bash
if [ "${FOREBRAIN_SKIP_UI_BUILD:-}" = "1" ]; then
  [ -f "$ROOT_DIR/pkg/gateway/dist/index.html" ] || { echo "pkg/gateway/dist has no web UI build; run make ui first" >&2; exit 1; }
else
  echo "==> building the web UI (make ui)"
  make -C "$ROOT_DIR" ui || {
    echo "web UI build failed; fix the frontend build, or set FOREBRAIN_SKIP_UI_BUILD=1 to embed the existing pkg/gateway/dist" >&2
    exit 1
  }
  echo "==> web UI rebuilt into pkg/gateway/dist (release-managed: do not commit local rebuilds; scripts/check-webui-dist.sh rejects PR changes)"
fi

DIST_DIR="$NPM_DIR/dist"
rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"
```

**Verify**: `bash -n npm/scripts/build-platform-packages.sh && echo syntax-ok` → `syntax-ok`

### Step 2: `Makefile` release 目标去重

a) 行 8-9 头注释改为：

```make
#   make release  build per-platform npm packages (the script rebuilds the UI
#                 first; it is embedded in each binary)
```

b) 行 61-64 改为：

```make
## release: build per-platform npm packages. The script rebuilds the web UI
## first (make ui) so every binary embeds the current frontend.
release:
	FOREBRAIN_VERSION=$(VERSION) npm/scripts/build-platform-packages.sh
```

**Verify**: `make -n release` → 输出只有脚本调用行，**不含** `find pkg/gateway/dist` 或
`pnpm build`（证明 ui 前置已移除、不会双跑）

### Step 3: `.github/workflows/release.yml` CI 走跳过路径

"Build the platform package" 步骤 env 块加（放在 `FOREBRAIN_CC_DARWIN_AMD64` 之后，
缩进与现有条目一致）：

```yaml
          # The release tag already carries the web UI committed by the
          # webui-into-release-pr job; this job has no Node/pnpm (and Windows
          # runners have no make), so the packaging script must not rebuild it.
          FOREBRAIN_SKIP_UI_BUILD: "1"
```

`release.yml:101` 的 `test -f pkg/gateway/dist/index.html` 前置守卫**保留不动**。

**Verify**: `python3 -c 'import yaml; yaml.safe_load(open(".github/workflows/release.yml")); print("yaml ok")'` → `yaml ok`

### Step 4: 官网 env 表补新键（EN + ZH 各一行）

a) `forebrain-harness.github.io/docs/develop/build-and-release.md` env 表（:157-160）
   在 `FOREBRAIN_BUILD_TARGETS` 行后加：

```markdown
| `FOREBRAIN_SKIP_UI_BUILD` | unset | Set `1` to skip the web UI rebuild and embed `pkg/gateway/dist` as-is (the release pipeline does this). |
```

b) `forebrain-harness.github.io/docs/zh/develop/build-and-release.md` env 表（:150-153）
   同位置加中文行：

```markdown
| `FOREBRAIN_SKIP_UI_BUILD` | 未设置 | 设为 `1` 跳过前端重建，直接内嵌现有 `pkg/gateway/dist`（发版流水线如此使用）。 |
```

正文 152-153/145-146 **不改**——它们本来就是本修复兑现的契约。页内其他既有 drift
（internal/webui/dist 等）见 Out of scope，不动。

**Verify**: `grep -c FOREBRAIN_SKIP_UI_BUILD forebrain-harness.github.io/docs/develop/build-and-release.md forebrain-harness.github.io/docs/zh/develop/build-and-release.md` → 两文件各 ≥1

### Step 5: 跳过路径冒烟（快，先于全量默认路径）

```bash
FOREBRAIN_SKIP_UI_BUILD=1 ./npm/scripts/build-platform-packages.sh 2>&1 | tee /tmp/skipui.log
grep -c "building the web UI" /tmp/skipui.log   # 预期 0
test -x npm/dist/@forebrain-harness/forebrain-darwin-arm64/vendor/aarch64-apple-darwin/bin/forebrain && echo bin-ok
```

**Verify**: `0` 与 `bin-ok`（用仓库现存 0.1.1 dist 编译，证明 skip 分支可用且 fail-fast 守卫未误触发）

### Step 6: 失败分支回归（make ui 失败不得毁掉上一份产物）

```bash
FAKEBIN=$(mktemp -d); printf '#!/bin/sh\nexit 1\n' > "$FAKEBIN/make"; chmod +x "$FAKEBIN/make"
BEFORE=$(sha256sum npm/dist/@forebrain-harness/forebrain-darwin-arm64/vendor/aarch64-apple-darwin/bin/forebrain | cut -d' ' -f1)
PATH="$FAKEBIN:$PATH" ./npm/scripts/build-platform-packages.sh >/tmp/fail.log 2>&1; echo "exit=$?"
grep "FOREBRAIN_SKIP_UI_BUILD=1" /tmp/fail.log   # 预期命中出路提示
AFTER=$(sha256sum npm/dist/@forebrain-harness/forebrain-darwin-arm64/vendor/aarch64-apple-darwin/bin/forebrain | cut -d' ' -f1)
[ "$BEFORE" = "$AFTER" ] && echo preserved
rm -rf "$FAKEBIN"
```

**Verify**: `exit=1`、出路提示命中、`preserved`（旧产物原样保留——评审问题②的回归证据）

### Step 7: 默认路径全量跑 + 新鲜度机器证明（本 bug 的直接回归证据）

```bash
./npm/scripts/build-platform-packages.sh 2>&1 | tee /tmp/build.log
grep "building the web UI" /tmp/build.log                  # 预期命中 make ui 行
grep "release-managed" /tmp/build.log                      # 预期命中 dist 提醒
git status --short pkg/gateway/dist | head -3              # 预期非空（dist 已更新为新构建）
ASSET=$(sed -n 's/.*\(assets\/index-[^"]*\.js\).*/\1/p' pkg/gateway/dist/index.html | head -1)
echo "$ASSET"                                             # 预期形如 assets/index-<新hash>.js，≠ index-Dc10V6pS.js
grep -acF "$ASSET" npm/dist/@forebrain-harness/forebrain-darwin-arm64/vendor/aarch64-apple-darwin/bin/forebrain
                                                          # 预期 ≥1：新 asset 引用已内嵌进二进制
```

**Verify**: 各命令输出符合注释预期；`$ASSET` ≠ `assets/index-Dc10V6pS.js`（旧 0.1.1 hash，
即之前内嵌的就是它）。

### Step 8: 真机验收（owner 实际链路：npm link launcher → gateway start）

```bash
cd npm && npm link && cd ..                                # launcher 已指向 npm/dist 产物
WORK=$(mktemp -d); mkdir -p "$WORK/home" "$WORK/proj"
git -C "$WORK/proj" init -q                                # 镜像 web_e2e.sh:138，消除非本因假失败
export FOREBRAIN_GATEWAY_TOKEN=$(openssl rand -hex 32)
cat > "$WORK/home/forebrain.yaml" <<'YAML'
gateway:
  http_addr: 127.0.0.1:8790
  auth:
    mode: token
    token: ${FOREBRAIN_GATEWAY_TOKEN}
YAML
( cd "$WORK/proj" && FOREBRAIN_HOME="$WORK/home" forebrain gateway start >"$WORK/gw.out" 2>"$WORK/gw.err" ) &
GW=$!
for i in $(seq 1 100); do curl -fsS http://127.0.0.1:8790/healthz >/dev/null 2>&1 && break; sleep 0.2; done
curl -fsS http://127.0.0.1:8790/healthz >/dev/null || { tail -20 "$WORK/gw.err"; kill $GW; exit 1; }
curl -s http://127.0.0.1:8790/ | grep -o 'assets/index-[^"]*\.js' | head -1   # 与 Step 7 的 $ASSET 一致
kill $GW; rm -rf "$WORK"
```

（heredoc 定界符用**带引号的 `<<'YAML'`**，`${FOREBRAIN_GATEWAY_TOKEN}` 原样落盘、由
forebrain 启动时解析——明文 token 会被启动拒绝，这是既有语义，复刻
`scripts/acceptance/web_e2e.sh:94-100`。若 `/` 需要鉴权，curl 加
`-H "Authorization: Bearer $FOREBRAIN_GATEWAY_TOKEN"`。）

**Verify**: healthz 探活通过；`curl /` 返回的 index.html 引用的 asset 文件名与 Step 7 的
`$ASSET` **完全一致**（gateway 供的就是刚构建的新前端）；浏览器人工打开同 URL 复核新 UI
特性（subagent 会话视图等 936df40/6d433ad 的改动）可见。非 TTY 路径：gateway 后台起、
`kill` 干净退出、临时目录可删（中断自救场景）。

### Step 9: 收口

- 还原本地 dist（仓库不变量：dist 只经 release automation 提交；check-webui-dist.sh 会拒
  PR 改动）：`git checkout -- pkg/gateway/dist && git clean -fdq pkg/gateway/dist`
- 回填 `docs/plan/NPM_PACKAGE_UI_REBUILD_PLAN.md`：状态行 + "验收记录"章节（场景→证据）。
- `git status --short` 复核改动白名单（见 Done criteria）。**不 commit**。

## Test plan

本修复是 shell/make/workflow/markdown 四处行为与文档改动，无 Go 代码——不新增单测
（无可测的 Go 单元；为 shell 分支写框架属过度工程，owner 已裁决拒绝测试脚手架）。
回归证据 = Step 5/6/7/8 的机器可查命令。既有 Go 测试不受影响，但按仓库门禁跑一次
编译级检查：

**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./...` → exit 0（证明 embed 产物与脚本
改动未破坏构建）

## Done criteria（机器可查，全部满足；括号内为检查时点）

- [x] `bash -n npm/scripts/build-platform-packages.sh` exit 0（Step 1 后）
- [x] `make -n release` 输出不含 `pnpm build` / `find pkg/gateway/dist`（Step 2 后）
- [x] `FOREBRAIN_SKIP_UI_BUILD=1 ./npm/scripts/build-platform-packages.sh` exit 0 且输出无 `building the web UI`（Step 5）
- [x] Step 6 失败分支：exit=1、出路提示命中、npm/dist 旧产物 sha256 不变（Step 6）
- [x] 默认 `./npm/scripts/build-platform-packages.sh`：输出含 `building the web UI` 与 `release-managed` 提醒；`git status --short pkg/gateway/dist` 非空；二进制内 grep 命中新 asset 引用且 ≠ `index-Dc10V6pS.js`（Step 7 时点；Step 9 会还原，属刻意顺序）
- [x] npm link 链路 `forebrain gateway start`：`/healthz` 探活通过，`curl /` 的 asset 引用与新版 `pkg/gateway/dist/index.html` 一致（Step 8）
- [x] `.github/workflows/release.yml` YAML 解析通过，且 env 含 `FOREBRAIN_SKIP_UI_BUILD: "1"`（Step 3 后）
- [x] 官网 EN/ZH 两页 env 表各含 `FOREBRAIN_SKIP_UI_BUILD` 一行（Step 4 后）
- [x] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0
- [x] `git status --short` 改动仅限：`npm/scripts/build-platform-packages.sh`、`Makefile`、`.github/workflows/release.yml`、两个官网 md、`docs/plan/NPM_PACKAGE_UI_REBUILD_PLAN.md`（`pkg/gateway/dist` 已按 Step 9 还原为干净）
- [x] 零 git commit

## STOP conditions

- 现场代码与本计划 "Current state" 摘录不符（已 drift）。
- `make -C "$ROOT_DIR" ui` 在本机失败（pnpm/corepack/node_modules 环境问题）：停，报告
  报错原文；不得绕过或手抄其命令进脚本。
- 发现必须改 `pkg/gateway/`、`frontend/`、`npm/bin/forebrain.js` 等 out-of-scope 文件
  才能满足任一 Done criterion：停，报告。
- Step 8 gateway 启动失败且与 UI 新旧无关（配置/端口/进程残留）：按 web_e2e.sh 模式排查
  最多两轮，仍不通即停并附 `gw.err`；**不得借机改产品代码**。
- `npm/dist` 出现在 `git status`（未被忽略）：停，报告。
- 官网页面实际行号/内容与本计划锚点不符（网站改造进行中，可能 drift）：只重新定位同段
  落；若"只构建一次"正文已被改写为相反语义，停，报告。

## Maintenance notes

- UI 构建步骤今后只改 Makefile `ui` 目标——脚本经 `make -C` 复用，单一事实源。
- release CI 永远走 `FOREBRAIN_SKIP_UI_BUILD=1`（tag 的 dist 由 webui-into-release-pr
  保证新鲜）。若未来要 CI 重建 UI，须给 build job 补 Node+pnpm（windows 还要 make）——
  那是刻意不做的事。
- 本地 Windows/Git Bash 无 make：`FOREBRAIN_SKIP_UI_BUILD=1` + 手动执行 make ui 的等价
  命令（失败提示已指明这条出路）。
- 官网 build-and-release.md（EN/ZH）与脚本 env 文档块需同步维护：再加脚本 env 键时，
  三处（脚本头、EN 表、ZH 表）一起加。
- Reviewer 关注点：脚本 if/else 分支的 `set -e` 语义与 `|| {}` 包装；重建先于 `rm -rf
  $DIST_DIR` 的顺序不能回退（否则回到评审问题②）；release.yml env 缩进；Makefile 移除
  `release: ui` 后无其他 target 依赖 `release`。
- 语义上被本修复取代的旧文案（脚本头注释 "The web UI is not built here"）必须同步删净，
  不留反义残留。
- 遗留（本计划不处理，仅报告）：官网 build-and-release.md 存在先于本 bug 的路径 drift
  （`internal/webui/dist` 应为 `pkg/gateway/dist`；env 表 `FOREBRAIN_VERSION` 默认值
  `0.1.0` 应为"读 VERSION 文件"）。

## 验收记录（2026-10-08 实施会话）

Drift check：`git diff --stat eafaf46..HEAD -- <六个文件>` 输出为空，HEAD = `eafaf46`，
Current state 五处摘录逐一核对一致。

| 场景 | 命令 | 结果 |
|---|---|---|
| Step 0 誊抄 | `test -f docs/plan/NPM_PACKAGE_UI_REBUILD_PLAN.md` | `ok` |
| Step 1 语法 | `bash -n npm/scripts/build-platform-packages.sh` | `syntax-ok`（exit 0） |
| Step 2 release 去重 | `make -n release` | 输出仅 `FOREBRAIN_VERSION=0.1.1 npm/scripts/build-platform-packages.sh`，无 `pnpm build` / `find pkg/gateway/dist` |
| Step 3 YAML | `python3 -c 'import yaml; yaml.safe_load(...)'` | `yaml ok` |
| Step 4 官网 | `grep -c FOREBRAIN_SKIP_UI_BUILD <EN> <ZH>` | 两文件各 1 |
| Step 5 跳过路径 | `FOREBRAIN_SKIP_UI_BUILD=1 ./npm/scripts/...` | exit 0；`grep -c "building the web UI"` = 0；产物 `bin-ok` |
| Step 6 失败分支 | 假 `make`（exit 1）注入 PATH | `exit=1`；出路提示 `FOREBRAIN_SKIP_UI_BUILD=1` 命中；npm/dist 旧二进制 sha256 前后一致（`preserved`） |
| Step 7 默认路径 | `./npm/scripts/...` | `==> building the web UI (make ui)` 与 `release-managed` 提醒均命中；`git status --short pkg/gateway/dist` 非空（新构建落盘）；新 asset `assets/index-BB_u7ZUq.js` ≠ 旧 `index-Dc10V6pS.js`；二进制内 `grep -acF` 命中 6 处 |
| Step 8 真机验收 | `npm link` → `forebrain gateway start`（127.0.0.1:8790，token auth） | `/healthz` 探活通过；`curl /`（Bearer）返回 `assets/index-BB_u7ZUq.js`，与 Step 7 `$ASSET` 完全一致；gateway 后台起、`kill` 干净退出、临时目录删除 |
| Test plan 门禁 | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0（`build-ok`） |
| Step 9 还原 | `git checkout -- pkg/gateway/dist && git clean -fdq pkg/gateway/dist` | `git status --short pkg/gateway/dist` 空 |

**Step 8 偏差记录**：第一轮 gateway 因 `startup config incomplete: main agent LLM is
not fully configured` 未启动——验收配方缺 main agent LLM 配置（与本 bug 无关的环境
前置）。按 STOP 条款授权的 web_e2e.sh 模式排查一轮补齐：`$HOME/.env` 写
`FOREBRAIN_GATEWAY_TOKEN` + `FAKE_LLM_KEY`，forebrain.yaml 增加 `agents.definitions.main`
（provider deepseek → 假端口 127.0.0.1:8799，静态资源验收不需真 LLM）。第二轮即通过。
此为验收配方问题，非产品缺陷，未改任何产品代码。

**工作区残留清理**：实施开始前发现另一会话误改的 `cmd/forebrain/console_log.go`（新增）
与 `cmd/forebrain/root.go`（修改，属另一未批准计划的半成品）残留在工作区；两者均不在
本计划 Scope，已整体还原（`rm` + `git checkout --`），还原后 `go build -tags fts5 ./...`
复跑通过。

最终 `git status --short` 白名单（零 commit）：

```
 M .github/workflows/release.yml
 M Makefile
 M npm/scripts/build-platform-packages.sh
?? docs/plan/NPM_PACKAGE_UI_REBUILD_PLAN.md
```
