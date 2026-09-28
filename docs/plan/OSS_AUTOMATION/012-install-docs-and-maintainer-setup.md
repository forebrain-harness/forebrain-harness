# Plan 012：安装文档、维护者文档、仓库配置脚本与首次上线清单

> **执行者须知**：逐步执行，每一步都要跑"验证"命令并确认结果与预期一致后才进入下一步。
> 出现"STOP 条件"中的任何一种情况，立即停止并汇报，不要自行发挥。完成后把
> `docs/plan/OSS_AUTOMATION/README.md` 里本计划的状态改成 DONE。
>
> **提交（里程碑 M8）**：本地步骤全部通过后，按 `README.md` 全局规则 1 提交**一次**：标题见 README 的
> "里程碑与提交标题"表，正文按 `.gitmessage` 模板（Why / What / Prompt cache / Verification，外加
> `Refs: docs/plan/OSS_AUTOMATION/<本文件名>`），用 `git commit -s -F <message-file>`；只 `git add` 本计划"范围"内的文件。
> 不得 `--amend`、`rebase`、`push --force`。开工前确认 `git status --short` 干净、`git log -1` 是上一个里程碑。
>
> **真实仓库实测**：按 README 的关卡推送后，在 `git@github.com:forebrain-harness/forebrain-harness.git` 上完成本文件"真实仓库实测"一节，
> 证据写入 `LIVE_TEST_LOG.md`。实测通过前本计划状态只能是 `AWAITING PUSH`。
>
> **`scripts/setup-github.sh` 在关卡 G2 由执行者真实运行**（先 `--dry-run`），前提是 owner 已创建 GitHub App 并设置变量与密钥；它会修改公开仓库的设置。
>
> **漂移检查（先做）**：确认计划 001、002、004–008 已完成（003、009–011 已推迟，不在本期）；否则 STOP。

## 状态

| 项 | 值 |
| --- | --- |
| 优先级 | P1（没有它 owner 无法把机制真正启用） |
| 工作量 | M |
| 风险 | LOW（只产出文档和脚本） |
| 依赖 | 001、002、004–008 |
| 类别 | docs / dx |
| 编写于 | 2026-09-28，基于未提交的工作区 |

## 为什么要做

前面各计划产出的是仓库内的文件。机制要真正运转，还要在 GitHub 上做一次性配置：
- 仓库合并策略；
- main 的规则集和必需检查；
- GitHub App；
- 密钥与变量；
- npm 环境；
- 标签；
- 私密漏洞报告。

用户也需要知道两种安装方式怎么用。本计划把这些写成：
- 面向用户的安装说明（英文）；
- 面向维护者的文档（英文）；
- 一个幂等的配置脚本（执行者在关卡 G2 真实运行）；
- 一份首次上线的核对清单。

## 现状

- `README.md` 的 "Quick start" 只写了 `npm install -g @forebrain-harness/forebrain`。"Build from source" 已有源码构建说明。
  项目规则：README 是面向用户的介绍，只讲用户能得到什么，不讲实现机制。
- `npm/package.json` 的 `files` 包含 `README.md`，但 `npm/README.md` 不存在。发布后 npm 包页面会是空的（既有缺陷，一并修）。
- 各计划引入的外部依赖：
  - 仓库变量：`FOREBRAIN_APP_ID`；
  - 仓库密钥：`FOREBRAIN_APP_PRIVATE_KEY`、`NPM_TOKEN`。本期 GitHub 上**不配置任何模型或模型 key**：CI 自维护已被否决，
    自维护改由 owner 自己机器上的 gateway 完成，首发后另立计划；
  - 仓库环境：`npm`；
  - 标签：`dependencies`（Dependabot 使用；release-please 会自己创建它的 `autorelease: *` 标签）；
  - 必需检查（job 的 `name`）：
    - `Go mod vet build test`、`Windows build test (CGO)`、`Frontend install build`、`macOS build (CGO)`（ci.yml）；
    - `go install from a module proxy`（001）；
    - `Web UI build is release-managed`（008）；
    - `Conventional Commits title`（006）；
    - `DCO sign-off`（006）。
- 仓库 `forebrain-harness/forebrain-harness` 是公开仓库，还是空的；owner 的 GitHub 账号是 `wubin1989`。

## 范围

**允许新建/修改**：`README.md`（仅 Quick start / 安装相关段落）、`npm/README.md`、`docs/maintainers/RELEASING.md`、
`scripts/setup-github.sh`、`scripts/setup-github/ruleset-main.json`、`CONTRIBUTING.md`（仅补充指向维护者文档的链接）。

**不许碰**：任何工作流与 Go 代码；文档站仓库 `forebrain-harness.github.io`（由计划 013 负责）。

## 步骤

### 步骤 1：README 安装说明

把 "Quick start" 的安装部分改成两种方式，保持简洁：

````markdown
Install with npm (Node.js 18+; macOS arm64/x64, Linux x64/arm64, Windows x64):

```bash
npm install -g @forebrain-harness/forebrain
```

Or with Go (Go 1.26+ and a C compiler):

```bash
CGO_ENABLED=1 go install -tags fts5 github.com/forebrain-harness/forebrain-harness/cmd/forebrain@latest
```

Prebuilt archives for every platform are attached to each
[GitHub release](https://github.com/forebrain-harness/forebrain-harness/releases).
````

再加一句面向用户的说明：go install 的构建会在首次需要时自动下载分词词典（中文与日文的记忆检索要用），之后离线可用。
不要写实现细节，例如校验方式、目录结构。

**验证**：`grep -n 'go install -tags fts5 github.com/forebrain-harness/forebrain-harness/cmd/forebrain@latest' README.md`
恰好 1 处，并且与 `pkg/state/db.go` 里 `errSQLiteBuild` 的命令逐字一致。检查命令：

```bash
diff <(grep -o 'CGO_ENABLED=1 go install[^`]*@latest' README.md) <(grep -o 'CGO_ENABLED=1 go install[^"]*@latest' pkg/state/db.go)
```

两边一致时 diff 无输出。

### 步骤 2：`npm/README.md`

英文，内容：一句话介绍、安装命令、支持的平台、`forebrain` 启动、链接主仓库和文档站。不超过 40 行。

**验证**：`(cd npm && npm pack --dry-run 2>&1 | grep -c README.md)` ≥ 1

### 步骤 3：`docs/maintainers/RELEASING.md`（英文）

小节：
1. *How a release happens*：合并 PR → release-please 维护 release PR（版本号、CHANGELOG、重建的 Web UI）→ 合并
   release PR → tag、GitHub Release、五平台二进制与 npm 包、词典包、`go install` 验收。
2. *Versioning*：Conventional Commits 如何决定版本；0.x 阶段破坏性变更只升次版本（`bump-minor-pre-major`）；
   升到 v2 需要改 Go 模块路径，所以不要轻易进入 1.0 → 2.0。
3. *The first release*：M0 基线提交的正文已带 `Release-As: 0.1.0` 脚注，release-please 据此把首发定为 0.1.0（D1）。
   以后要指定版本号时，同样在提交正文里加这个脚注。
4. *The web UI in the repository*：`pkg/gateway/dist` 只由 release PR 更新；普通 PR 改了它时 CI 会失败，恢复命令为
   `git checkout origin/main -- pkg/gateway/dist`。
5. *npm publishing*：`npm` 环境、`NPM_TOKEN`；首发之后切换到 Trusted Publishing（OIDC）的步骤。
   另写 *Setup* 小节：
   - 创建 GitHub App 的步骤（Settings → Developer settings → GitHub Apps → New）。权限：Contents 读写、Pull requests
     读写、Issues 读写、Metadata 只读；不开 webhook；只安装到本仓库；**不要**授予 Workflows 权限。
     用途：release-please 用它的 token 开 release PR 并推送 Web UI 提交，这样才能触发 CI；
   - 生成私钥；
   - 设置 `FOREBRAIN_APP_ID`（变量）、`FOREBRAIN_APP_PRIVATE_KEY` 与 `NPM_TOKEN`（密钥）；
   - 运行 `scripts/setup-github.sh`。
6. *If something fails*：各 job 可以单独重跑；npm 已发布的包会被跳过；GitHub Release 资产用 `--clobber` 覆盖；
   **不要删除或移动已经发布的 tag**，因为 Go 校验和数据库已经记录了它，只能发新版本。
7. *First go-live checklist*：见步骤 5。

### 步骤 4：`scripts/setup-github.sh`（幂等；执行者在关卡 G2 真实运行）

用法：`scripts/setup-github.sh [--dry-run] [owner/repo]`，默认仓库为 `forebrain-harness/forebrain-harness`。
`--dry-run` 时只打印将要执行的 gh 命令，不执行。

脚本依次执行：
1. 合并策略：
   ```
   gh api -X PATCH "repos/$R" -F allow_squash_merge=true -F allow_merge_commit=false -F allow_rebase_merge=false \
     -f squash_merge_commit_title=PR_TITLE -f squash_merge_commit_message=PR_BODY \
     -F allow_auto_merge=true -F delete_branch_on_merge=true -F allow_update_branch=true
   ```
2. 安全功能：
   - `gh api -X PUT "repos/$R/private-vulnerability-reporting"`
   - `gh api -X PUT "repos/$R/vulnerability-alerts"`
   - `gh api -X PUT "repos/$R/automated-security-fixes"`
3. 标签：`gh label create dependencies --color 0366d6 --description "Dependency updates" --force -R "$R"`。
4. 环境：`gh api -X PUT "repos/$R/environments/npm"`。
5. main 的规则集：读取 `scripts/setup-github/ruleset-main.json`（见下）。用
   `gh api "repos/$R/rulesets" --jq '.[] | select(.name=="main") | .id'` 查找已有规则集：有就 PUT 更新，没有就 POST 创建。
6. 检查变量和密钥是否齐全：用 `gh variable list` 与 `gh secret list` 对照所需清单，缺了就打印需要执行的
   `gh variable set …`、`gh secret set … < file` 命令。**脚本不读取、不接收任何密钥值**。

`scripts/setup-github/ruleset-main.json`：
```json
{
  "name": "main",
  "target": "branch",
  "enforcement": "active",
  "conditions": { "ref_name": { "include": ["~DEFAULT_BRANCH"], "exclude": [] } },
  "rules": [
    { "type": "deletion" },
    { "type": "non_fast_forward" },
    { "type": "required_linear_history" },
    { "type": "pull_request", "parameters": {
        "required_approving_review_count": 0,
        "dismiss_stale_reviews_on_push": true,
        "require_code_owner_review": false,
        "require_last_push_approval": false,
        "required_review_thread_resolution": true } },
    { "type": "required_status_checks", "parameters": {
        "strict_required_status_checks_policy": false,
        "required_status_checks": [
          { "context": "Go mod vet build test" },
          { "context": "Windows build test (CGO)" },
          { "context": "Frontend install build" },
          { "context": "macOS build (CGO)" },
          { "context": "go install from a module proxy" },
          { "context": "Web UI build is release-managed" },
          { "context": "Conventional Commits title" },
          { "context": "DCO sign-off" }
        ] } }
  ]
}
```
`required_approving_review_count: 0` 的理由写进 RELEASING.md：目前只有一位维护者，GitHub 不允许作者批准自己的 PR；
合并前必须经过 PR 和全部检查，而合并这个动作本身就是人工审批。以后有第二位维护者时改成 1。

**验证**：
- `bash -n scripts/setup-github.sh` 通过；`jq empty scripts/setup-github/ruleset-main.json` 通过
- `scripts/setup-github.sh --dry-run` 打印全部命令，不访问网络；dry-run 下要用 `gh` 的 stub 或直接跳过 gh 调用来确认
- 规则集里的 8 个 context 与各工作流 job 的 `name:` 逐一对应。用下面的命令检查，8 行都要有输出：
  ```bash
  for c in "Go mod vet build test" "Windows build test (CGO)" "Frontend install build" "macOS build (CGO)" \
           "go install from a module proxy" "Web UI build is release-managed" "Conventional Commits title" "DCO sign-off"; do
    grep -rlF "name: $c" .github/workflows >/dev/null && echo "ok: $c"
  done
  ```

### 步骤 5：首次上线核对清单（写进 RELEASING.md 第 7 节）

owner 按顺序执行，每一项都写上"期望看到什么"：
1. 创建 GitHub App 并安装；设置 `FOREBRAIN_APP_ID`、`FOREBRAIN_APP_PRIVATE_KEY`、`NPM_TOKEN`；运行 `scripts/setup-github.sh`。
2. 首次推送 main。CI 各 job 全绿；Release 工作流运行后出现一个 release PR，其中有 `chore(release): build the web UI`
   提交（前提是 dist 与源码不一致）。
3. 开一个测试 PR：
   - 标题故意不合规 → `Conventional Commits title` 失败；
   - 改对 → 通过；
   - 提交不带 `-s` → `DCO sign-off` 失败。
4. 合并 release PR：
   - Release 页面有 5 个归档、`forebrain-dict.tar.gz`、`SHA256SUMS`；
   - npm 上 6 个包都是新版本，且带 provenance 标记；
   - `go-install` job 通过；
   - 在自己的机器上用 go install 装一次，`forebrain --version` 输出正确；
   - 在一个中文内容的会话里触发记忆检索，确认词典被下载到 `$(go env GOPATH)/bin/dict/`。
5. 文档站（计划 013）的安装页已更新，并与 README 的两条安装命令逐字一致。

### 步骤 6：CONTRIBUTING 链接

在 CONTRIBUTING.md 的 *Releases* 一节末尾链接 `docs/maintainers/RELEASING.md`。

## 真实仓库实测（关卡 G2 与 G3）

1. G2：`scripts/setup-github.sh --dry-run`，然后执行 `scripts/setup-github.sh`，接着核对：
   - `gh api repos/forebrain-harness/forebrain-harness --jq '{allow_squash_merge, allow_merge_commit, allow_rebase_merge, squash_merge_commit_title, squash_merge_commit_message, allow_auto_merge, delete_branch_on_merge}'`
     → true、false、false、`PR_TITLE`、`PR_BODY`、true、true；
   - `gh api repos/forebrain-harness/forebrain-harness/rulesets --jq '.[].name'` 含 `main`，其必需检查恰好是那 8 个 context；
   - `gh api repos/forebrain-harness/forebrain-harness/private-vulnerability-reporting --jq .enabled` → `true`；
   - `gh label list --repo forebrain-harness/forebrain-harness` 含 `dependencies`；`gh api repos/forebrain-harness/forebrain-harness/environments/npm --jq .name` → `npm`；
   - 脚本最后打印的"缺失变量/密钥"为空；否则请 owner 补齐后重跑脚本。
2. 保护生效：`git push origin HEAD:main --dry-run` 不足以验证，改为推一个空分支后尝试直接更新 main。更简单的做法是确认
   规则集 `enforcement` 为 `active`，并在报告中说明此后一切修正都走 PR。
3. G3：按 RELEASING.md 第 7 节的上线清单逐项执行（合并由 owner 完成），每项写明"期望 / 实际 / 链接"。
4. README 与 npm 页面：`npm view @forebrain-harness/forebrain readme | head -5` 非空；GitHub 仓库首页 README 的两条安装命令，
   分别在干净环境里复制粘贴执行一次，结果与计划 008 实测第 7、8 步一致。
5. 记录全部输出与链接。

## 完成标准

- [ ] M8 已按规范提交
- [ ] 真实仓库实测第 1–4 步通过并记录
- [ ] 步骤 1、2、4 的验证全部通过
- [ ] 新增文档全部为英文，不含个人邮箱（`grep -rnE '@(gmail|qq)\.com|wubinwill' docs/maintainers npm/README.md README.md` 无输出）
- [ ] `scripts/setup-github.sh` 只在关卡 G2 真实运行过，之前只做过 `--dry-run`
- [ ] 改动全部在允许清单内

## STOP 条件

- 规则集里的某个 context 在工作流里找不到对应的 job name（说明前面某个计划改了 job 名）。
- GitHub API 的端点或字段与脚本不符。以 `gh api` 的官方文档为准，并汇报。

## 维护说明

- 新增必需检查时，要同时改工作流的 job name 和 `ruleset-main.json`，然后重跑 `scripts/setup-github.sh`。
- 文档站（独立仓库，计划 013）的安装页要与 README 逐字同步；改安装命令时两边一起改。
