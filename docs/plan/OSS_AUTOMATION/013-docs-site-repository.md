# Plan 013：文档站源码入库并推送到 `forebrain-harness.github.io`，由 GitHub Pages 自动部署，补上安装说明

> **执行者须知**：逐步执行，每一步都要跑"验证"命令并确认结果与预期一致后才进入下一步。
> 出现"STOP 条件"中的任何一种情况，立即停止并汇报，不要自行发挥。完成后把
> `docs/plan/OSS_AUTOMATION/README.md` 里本计划的状态改成 DONE。
>
> **提交（文档站里程碑 S0–S2）**：本计划在**文档站自己的仓库**里提交，路径为
> `/Users/doudou/workspace/unionj-cloud/forebrain-harness/forebrain-harness.github.io`，它有独立的 `.git`，
> 主仓库已通过 M0 的 `.gitignore` 忽略它。提交规则与主仓库全局规则 1 完全相同：
> - Conventional Commits 标题；
> - 正文按主仓库 `.gitmessage` 模板写 Why / What / Prompt cache / Verification，外加
>   `Refs: docs/plan/OSS_AUTOMATION/013-docs-site-repository.md`（主仓库路径）；
> - `git commit -s -F <message-file>`；
> - 不得 `--amend`、`rebase`、`push --force`。
>
> 标题用主仓库的 `scripts/check-commit-message.sh` 校验（主仓库 M6 之后可用）。
>
> **真实仓库实测**：推送到 `git@github.com:forebrain-harness/forebrain-harness.github.io.git`，在真实的 GitHub Pages 站点
> `https://forebrain-harness.github.io` 上验证，证据写入主仓库的 `docs/plan/OSS_AUTOMATION/LIVE_TEST_LOG.md`。
>
> **漂移检查（先做）**：逐条核对"现状"；不一致即 STOP。

## 状态

| 项 | 值 |
| --- | --- |
| 优先级 | P1（owner 要求："文档站源码必须推送到 github 仓库 git@github.com:forebrain-harness/forebrain-harness.github.io.git"） |
| 工作量 | S |
| 风险 | LOW |
| 依赖 | S0、S1 无依赖（关卡 G1 推送）；S2 依赖 004（安装命令文案）与 008 的首个 Release（关卡 G3 推送） |
| 类别 | docs |
| 编写于 | 2026-09-28，基于未提交的工作区 |

## 为什么要做

文档站（README 里链接的 https://forebrain-harness.github.io）的源码目前只躺在 owner 本机，嵌套在主仓库目录里：
- 一个提交都没有，远端也是空的，站点无法部署；
- 如果不先忽略它，主仓库 `git add -A` 时会把它变成坏的 gitlink（已由主仓库 M0 处理）。

owner 要求把它推送到自己的 GitHub 仓库。本期新增了 npm 与 go install 两种安装方式，文档站却全站没有安装说明
（"What You Need" 只写了一句 "a Forebrain Harness installation"），用户在官网上找不到怎么装。CLI 命令面本期不变
（`forebrain exec` 已随 CI 自维护一起推迟，见 README 的"推迟的批次"），CLI 参考页不用改。

## 现状

- 位置：`/Users/doudou/workspace/unionj-cloud/forebrain-harness/forebrain-harness.github.io`
- `git status`：分支 `main`，**没有任何提交**；未跟踪的内容为 `.github/`、`.gitignore`、`README.md`、`docs/`、
  `package-lock.json`、`package.json`。
- `git config --get remote.origin.url` → `git@github.com:forebrain-harness/forebrain-harness.github.io.git`；
  `git ls-remote` 为空（远端仓库存在但没有内容）。
- 技术栈：VitePress（`package.json`：`vitepress ^1.6.4`、`mermaid ^11.15.0`），`npm run docs:build` 产出
  `docs/.vitepress/dist`。
- `.gitignore`：`node_modules`、`dist`、`docs/.vitepress/cache`、`docs/.vitepress/dist`、`.superpowers/`、`.DS_Store`。
  本机的 `node_modules`（228 MB）、dist、cache 都被忽略。
- `.github/workflows/deploy.yml`：push 到 main 时 checkout → setup-node 22（npm 缓存）→ **`npm install`**（没有用锁文件
  严格安装）→ `npm run docs:build` → `actions/configure-pages@v5` → `upload-pages-artifact@v3` → `deploy-pages@v4`。
  权限为 `pages: write`、`id-token: write`。
- 内容：
  - `docs/guide/getting-started.md` 的 "What You Need" 一节只写了 "a Forebrain Harness installation"，
    **全站没有安装说明**（`grep -rn "npm install -g\|go install" docs` 无结果）；
  - `docs/guide/cli-command-reference.md` 开头写着 "The supported commands are exactly:"，列出 `forebrain`、
    `forebrain resume <session-id>`、`forebrain gateway start|status|stop`，并有一节 "Deliberately Unsupported Commands"。
  - 中文镜像 `docs/zh/` 目前只有 `guide/memory-internals.md`、`guide/skills-internals.md` 和 `config/`，没有入门页与
    CLI 参考。
- 事实来源（文档必须与之逐字一致）：
  - go install 命令：`CGO_ENABLED=1 go install -tags fts5 github.com/forebrain-harness/forebrain-harness/cmd/forebrain@latest`
    （主仓库 `pkg/state/db.go` 的 `errSQLiteBuild`，计划 004）；
  - npm 命令：`npm install -g @forebrain-harness/forebrain`；

## 文档站里程碑

| 里程碑 | 内容 | 提交标题 | 推送关卡 |
| --- | --- | --- | --- |
| S0 | 当前全部站点源码 | `chore: import the documentation site` | G1 |
| S1 | 部署工作流改为严格按锁文件安装，并增加 PR 构建检查 | `ci: build the site from the lockfile on every change` | G1 |
| S2 | 安装说明（npm、go install、Release 归档） | `docs: document npm and go install` | G3（首个 Release 实测通过之后，保证文档所写的都已可用） |

## 需要的命令（都在文档站目录内执行）

| 用途 | 命令 | 成功时 |
| --- | --- | --- |
| 严格安装 | `npm ci` | exit 0 |
| 构建（含死链检查） | `npm run docs:build` | exit 0 |
| workflow lint | 在主仓库目录运行 `go run github.com/rhysd/actionlint/cmd/actionlint@<版本> <站点目录>/.github/workflows/deploy.yml` | 无输出 |

## 范围

**允许修改**（文档站仓库内）：`.github/workflows/deploy.yml`、`docs/guide/getting-started.md`、
`docs/guide/cli-command-reference.md`、`docs/.vitepress/config.mjs`（仅当需要在侧栏里加链接时）。

**不许碰**：主仓库任何文件（除了 `LIVE_TEST_LOG.md` 和 README 的状态列）；文档站其他页面的内容。

## 步骤

### 步骤 1：S0 基线提交（文档站仓库）

1. `cd` 到文档站目录。`git status --short` 应只显示"现状"里列出的六项。
2. `git add -A`，然后逐项检查：
   - `git ls-files | grep -E '^(node_modules|docs/\.vitepress/(dist|cache))/'` → 无输出；
   - 大文件：`git diff --cached --name-only -z | xargs -0 du -k | awk '$1>5120'` → 无输出，否则 STOP；
   - 密钥扫描：`go run github.com/zricethezav/gitleaks/v8@latest detect --source . --no-git --redact` → 无发现，否则 STOP，
     只报告 `file:line` 与类型。
3. `npm ci && npm run docs:build` → exit 0，确认入库的源码可以独立构建。
4. `git commit -s -F <message-file>`，标题为 `chore: import the documentation site`，四节正文：`Prompt cache` 写
   `unchanged: documentation site only`；`Verification` 写第 2–3 步实际跑过的命令。

**验证**：`git log --oneline` 恰好 1 个提交；`git show --stat HEAD | tail -1` 的文件数与第 2 步暂存的一致

### 步骤 2：S1 部署工作流

修改 `.github/workflows/deploy.yml`：
- `Install` 步骤：`npm install` → `npm ci`；
- 触发加上 `pull_request: branches: [main]`；
- `deploy` job 加 `if: github.event_name != 'pull_request'`，保证 PR 只构建、不部署；
- `build` job 加 `timeout-minutes: 15`；
- `actions/*` 保持主版本 tag（与主仓库计划 007 的规则一致）。

`npm run docs:build` 通过后提交 S1。

**验证**：actionlint 无输出；`grep -n 'npm ci' .github/workflows/deploy.yml` 有 1 处；`git log --oneline` 为 2 个提交

### 步骤 3：S2 内容更新（G3 之后再做）

在 `docs/guide/getting-started.md` 的 "What You Need" 之前新增 `## Install` 一节（英文），内容：
- npm 安装与平台要求（Node.js 18+；macOS arm64/x64、Linux x64/arm64、Windows x64）；
- go install（Go 1.26+、C 编译器），命令与"事实来源"逐字一致；说明首次需要时会自动下载分词词典（中文和日文记忆检索用），
  下载一次后离线可用；
- 每个 GitHub Release 都附有各平台归档（链接 https://github.com/forebrain-harness/forebrain-harness/releases ）；
- 验证安装：`forebrain --version`。

"What You Need" 中的 "a Forebrain Harness installation" 改成链接到新小节。

`docs/guide/cli-command-reference.md` 本期不改（CLI 命令面未变）。`npm run docs:build` 通过（VitePress 默认遇到死链就让构建失败）后提交 S2。

**验证**：
- `grep -F 'CGO_ENABLED=1 go install -tags fts5 github.com/forebrain-harness/forebrain-harness/cmd/forebrain@latest' docs/guide/getting-started.md`
  有 1 处；
- `git log --oneline` 为 3 个提交

## 真实仓库实测

**G1（推送 S0、S1 之后）**
1. 启用 Pages 并设为由 Actions 构建（在真实仓库上执行）：
   `gh api -X POST repos/forebrain-harness/forebrain-harness.github.io/pages -f build_type=workflow`。
   已存在时返回 409，这时改用 `gh api -X PUT repos/forebrain-harness/forebrain-harness.github.io/pages -f build_type=workflow`。
   **验证**：`gh api repos/forebrain-harness/forebrain-harness.github.io/pages --jq '.build_type, .html_url'` →
   `workflow` 与 `https://forebrain-harness.github.io/`。
2. `git push -u origin main`。`Deploy Docs` 工作流运行成功：
   `gh run watch "$(gh run list --repo forebrain-harness/forebrain-harness.github.io --limit 1 --json databaseId --jq '.[0].databaseId')" --repo forebrain-harness/forebrain-harness.github.io --exit-status`。
3. 线上可访问：`curl -fsS https://forebrain-harness.github.io/ | grep -c 'Forebrain Harness'` ≥ 1；主仓库 README 里链接的
   `https://forebrain-harness.github.io/guide/cli-command-reference` 与 `/guide/memory-systems` 都返回 200
   （`curl -o /dev/null -sw '%{http_code}' <url>`）。
4. PR 构建检查：建分支 `live-test/site-pr`，加一个遵守提交规范的空提交，推送后开 `[live-test]` PR。确认 `Deploy Docs`
   只跑 build、deploy 被跳过。然后关闭 PR、删除分支。

**G3（推送 S2 之后）**
5. 部署成功后，线上入门页含 go install 命令：
   `curl -fsS https://forebrain-harness.github.io/guide/getting-started | grep -F 'go install -tags fts5 github.com/forebrain-harness/forebrain-harness/cmd/forebrain@latest'`
   有输出。
6. 从**线上页面**复制两条安装命令，在干净的临时环境里各执行一次，结果与主仓库计划 008 实测第 7、8 步一致
   （`forebrain --version` → 首个版本号）。
7. 全部链接与输出写入 `LIVE_TEST_LOG.md` 的 013 小节。

## 完成标准

- [ ] 文档站仓库有 S0、S1、S2 三个符合规范的提交，并已推送到 `git@github.com:forebrain-harness/forebrain-harness.github.io.git` 的 `main`
- [ ] 每次推送对应的 `Deploy Docs` 运行成功，站点由 Actions 部署（`build_type=workflow`）
- [ ] 实测第 1–6 步全部通过并记录
- [ ] 主仓库 `git status` 中看不到文档站目录（被 M0 的 `.gitignore` 忽略）

## STOP 条件

- `npm ci` 失败，即锁文件与 `package.json` 不一致。不要重生成锁文件，汇报即可。
- `git add -A` 暂存了 `node_modules` 或构建产物（说明 `.gitignore` 与现状不符）。
- 远端仓库在推送前已经有内容（`git ls-remote` 非空），这时不能直接推送，需要 owner 决定。
- Pages API 返回的错误不是 409（例如组织禁用了 Pages）。

## 维护说明

- 主仓库改动安装方式时，文档站与 README 逐字同步（两侧一起改）。巡检式同步属于 gateway 自维护批次（README 的"推迟的批次"），本期不做。
- 文档站是独立仓库，它的提交与 release-please 无关，也不参与主仓库的版本号。
