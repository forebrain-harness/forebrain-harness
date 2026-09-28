# Plan 006：贡献规范与社区文件——Conventional Commits、squash、DCO、模板与检查

> **执行者须知**：逐步执行，每一步都要跑"验证"命令并确认结果与预期一致后才进入下一步。
> 出现"STOP 条件"中的任何一种情况，立即停止并汇报，不要自行发挥。完成后把
> `docs/plan/OSS_AUTOMATION/README.md` 里本计划的状态改成 DONE。
>
> **提交（里程碑 M5）**：本地步骤全部通过后，按 `README.md` 全局规则 1 提交**一次**：标题见 README 的
> "里程碑与提交标题"表，正文按 `.gitmessage` 模板（Why / What / Prompt cache / Verification，外加
> `Refs: docs/plan/OSS_AUTOMATION/<本文件名>`），用 `git commit -s -F <message-file>`；只 `git add` 本计划"范围"内的文件。
> 不得 `--amend`、`rebase`、`push --force`。开工前确认 `git status --short` 干净、`git log -1` 是上一个里程碑。
>
> **真实仓库实测**：按 README 的关卡推送后，在 `git@github.com:forebrain-harness/forebrain-harness.git` 上完成本文件"真实仓库实测"一节，
> 证据写入 `LIVE_TEST_LOG.md`。实测通过前本计划状态只能是 `AWAITING PUSH`。
>
> 本计划会新增 git hook。验证 hook 时在**临时仓库**里做（`git init $TMPDIR/hookcheck`）。
>
> **漂移检查（先做）**：本计划编写时仓库还没有提交，M0 之后才有历史。开工前逐条核对"现状"中的摘录与实际文件一致，并用
> `git log --oneline -- <范围内路径>` 查看 M0 之后是否有别的提交改过这些文件；有且与摘录不符，就按 STOP 处理。

## 状态

| 项 | 值 |
| --- | --- |
| 优先级 | P1（发版自动化解析的就是这里规定的提交格式） |
| 工作量 | M |
| 风险 | LOW |
| 依赖 | 无 |
| 类别 | docs / dx |
| 编写于 | 2026-09-28，基于未提交的工作区 |
| owner 决策 | **D6** 已定：行为准则不设举报渠道；**D7**（squash 提交信息取 PR 标题 + PR 正文）按推荐值编写 |

## 为什么要做

owner 要求一套符合 GitHub 开源最佳实践的规范机制，已拍板的约定如下：
- **squash 合并**，PR 标题按 Conventional Commits 校验；
- 用 **DCO 签署**做贡献授权；
- 对外文档一律**英文**。

这些约定是后续自动化的前提：release-please（计划 008）靠 main 上的 Conventional Commits 决定版本号和
生成 CHANGELOG。squash 之后，main 上每个提交的标题就是 PR 标题，所以校验 PR 标题就能保证 main 的提交格式。

## 现状

- 仓库根目录已有：`LICENSE`（Apache-2.0）、`NOTICE`、`README.md`、`FOREBRAIN.md`（项目给 agent 的说明，
  会进入本项目会话的 prompt）、`.gitmessage`（上一轮刚写的**中文**提交模板，节选如下）：
  ```
  # <type>(<scope>): <subject>
  # type   feat | fix | refactor | perf | test | docs | build | ci | chore | revert
  # scope  主要改动所在的包或区域：state, tui, gateway, run, turn, memory, …
  ...
  Why:
  What:
  Prompt cache:
  Verification:
  ```
- 缺少：`CONTRIBUTING.md`、`CODE_OF_CONDUCT.md`、`SECURITY.md`、`.github/PULL_REQUEST_TEMPLATE.md`、
  `.github/ISSUE_TEMPLATE/`、`.github/CODEOWNERS`、`.gitattributes`、任何 git hook。
- `.github/workflows/` 只有 `ci.yml`、`security.yml`。
- `README.md` 第 232–286 行是 "Contributing" 一节，含提 issue、提 PR、"ground rules"（修根因、保护 prompt
  cache、一个引擎两个 surface、架构测试、CGO 必需）和 "Security"（指向 GitHub security advisories）。
- `Makefile` 的 `test:` 目标是 `CGO_ENABLED=1 go test ./...`，**缺 `-tags fts5`**。状态库 schema 有 FTS5 表，
  所以 `make test` 会失败。这是顺带发现的既有缺陷，按项目规则一并修复。`FOREBRAIN.md` 第 10 行对
  `make test` 的描述也是错的。
- `forebrain-harness.github.io/` 是**另一个 git 仓库**（文档站，`origin` 为
  `git@github.com:forebrain-harness/forebrain-harness.github.io.git`），嵌套放在本仓库里，而 `.gitignore`
  没有忽略它。`git add -A` 时它会被当成一个没有 `.gitmodules` 的 gitlink 加进来，结果是坏掉的子模块。
  这个既有缺陷已在基线提交 M0 之前修复（README 的"M0 基线提交"第 1 步），本计划只核对；文档站本身由计划 013 负责。
- 仓库 owner 的 GitHub 账号是 `wubin1989`，组织是 `forebrain-harness`。
- 机器人账号（Dependabot、release-please 使用的 GitHub App、agent 工作流）的提交作者邮箱都以
  `[bot]@users.noreply.github.com` 结尾。

## 约定（本计划所有文件都要与之一致）

- **提交/PR 标题**：`<type>(<scope>)!: <subject>`，scope 和 `!` 可选。
  - type ∈ `feat fix perf refactor test docs build ci chore revert`
  - scope 匹配 `[a-z0-9._/-]+`
  - subject 以小写字母或数字开头，不以句号结尾
  - 整行 ≤ 100 字符（commitlint 的默认值，能容纳 Dependabot 生成的长标题）
- **破坏性变更**：标题带 `!`，或 PR 正文里有 `BREAKING CHANGE: …` 脚注。
- **合并**：只允许 squash。squash 提交的标题 = PR 标题，正文 = PR 正文（D7），所以 PR 正文要写成好的提交正文。
- **DCO**：PR 里每个提交都要有与作者一致的 `Signed-off-by: Name <email>`（`git commit -s`）。bot 提交豁免。
  squash 后的提交由 GitHub 代为创建，不强制带签署行；签署在 PR 提交层面检查。
- **正文结构**（沿用 `.gitmessage` 的四节）：`Why`、`What`、`Prompt cache`（必填，不影响时写
  `unchanged: <reason>`）、`Verification`（只写实际跑过的命令）。可选节 `Schema`、`Refs`、`BREAKING CHANGE`。

## 需要的命令

| 用途 | 命令 | 成功时 |
| --- | --- | --- |
| 脚本语法 | `bash -n scripts/check-commit-message.sh scripts/check-dco.sh .githooks/*` | exit 0 |
| YAML 语法 | `ruby -ryaml -e 'ARGV.each{|f| YAML.load_file(f)}' .github/workflows/*.yml .github/ISSUE_TEMPLATE/*.yml` | exit 0 |
| workflow lint | `go run github.com/rhysd/actionlint/cmd/actionlint@latest` | 无输出 |

## 范围

**允许新建/修改**：`CONTRIBUTING.md`、`CODE_OF_CONDUCT.md`、`SECURITY.md`、`.gitmessage`、`.gitattributes`、
`.githooks/commit-msg`、`.githooks/prepare-commit-msg`、`scripts/check-commit-message.sh`、`scripts/check-dco.sh`、
`.github/PULL_REQUEST_TEMPLATE.md`、`.github/ISSUE_TEMPLATE/bug_report.yml`、`.github/ISSUE_TEMPLATE/feature_request.yml`、
`.github/ISSUE_TEMPLATE/config.yml`、`.github/CODEOWNERS`、`.github/workflows/pr-title.yml`、`.github/workflows/dco.yml`、
`Makefile`（仅 `test` 和新增 `hooks` 目标）、`README.md`（仅 Contributing 与 Security 两节）、
`FOREBRAIN.md`（仅第 10 行 `make test` 的描述）。

**不许碰**：`ci.yml`、`security.yml`（其他计划会改）；README 其他各节（计划 012 负责安装一节）。

## 步骤

### 步骤 1：提交信息检查脚本（唯一事实来源）

新建 `scripts/check-commit-message.sh`，`chmod +x`：
- 用法：`scripts/check-commit-message.sh <message-file>`，或 `scripts/check-commit-message.sh -`（从 stdin 读）。
- 取第一条既非空、也不以 `#` 开头的行作为标题。
- 本地 hook 会放行以 `fixup! `、`squash! `、`amend! `、`Merge `、`Revert "` 开头的标题（它们在 squash 时消失，
  或由 git 自动生成）。CI 在检查 PR 标题时传 `--strict`，不放行这些。
- 按"约定"校验。不通过时打印**一句话**说明规则，并给一个例子，exit 1：
  `PR title must look like "fix(state): keep the exec timing triple whole" (type: feat|fix|perf|refactor|test|docs|build|ci|chore|revert); got: <title>`

**验证**（逐条运行，看 exit 码）：
- `printf 'feat(tui): add a thing\n' | scripts/check-commit-message.sh --strict -` → 0
- `printf 'chore(main): release 0.1.0\n' | scripts/check-commit-message.sh --strict -` → 0
- `printf 'build(deps): bump github.com/aws/aws-sdk-go-v2/service/s3 from 1.99.0 to 1.100.0\n' | scripts/check-commit-message.sh --strict -` → 0
- `printf 'feat!: drop the old flag\n' | scripts/check-commit-message.sh --strict -` → 0
- `printf 'Fix stuff\n' | scripts/check-commit-message.sh --strict -` → 1
- `printf 'feat: Add thing.\n' | scripts/check-commit-message.sh --strict -` → 1
- `printf 'fixup! feat: x\n' | scripts/check-commit-message.sh -` → 0；加 `--strict` → 1

### 步骤 2：DCO 检查脚本

新建 `scripts/check-dco.sh <base> <head>`，`chmod +x`：
- 遍历 `git rev-list --no-merges <base>..<head>`。
- 作者邮箱以 `[bot]@users.noreply.github.com` 结尾的提交跳过。
- 否则要求 `git log -1 --format='%(trailers:key=Signed-off-by,valueonly)' <sha>` 中有一行等于
  `"%an <%ae>"`（作者本人签署）。
- 每个不合格的提交打印一行：
  `<short-sha> "<subject>" is missing "Signed-off-by: <author>"; sign it with: git rebase --signoff <base>`，
  最后 exit 1。
- 全部合格时打印 `DCO: <n> commit(s) signed off`。

**验证**：在 `$TMPDIR/dcocheck` 里 `git init`，造三个提交：一个带 `-s`、一个不带、一个作者为
`dependabot[bot] <49699333+dependabot[bot]@users.noreply.github.com>` 且不带签署。运行
`scripts/check-dco.sh <root> HEAD`（从本仓库调用脚本，`cd` 到临时仓库执行）→ exit 1，恰好报告那个未签署的
人类提交。

### 步骤 3：git hooks 与 `make hooks`

- `.githooks/commit-msg`：`exec "$(git rev-parse --show-toplevel)/scripts/check-commit-message.sh" "$1"`，
  外加检查签署：消息里没有 `Signed-off-by:` 时，打印一句提示（`add a DCO sign-off: git commit -s`）并 exit 1。
- `.githooks/prepare-commit-msg`：用
  `git interpret-trailers --in-place --if-exists doNothing --trailer "Signed-off-by: $(git config user.name) <$(git config user.email)>" "$1"`
  自动补签署。第二个参数是 `merge` 或 `squash` 时不处理。注释里说明：装 hook 是开发者自己的选择，
  等于同意 DCO，自动签署只是省去手敲 `-s`。
- 两个 hook 都 `chmod +x`。
- `Makefile`：新增 `hooks:` 目标 `git config core.hooksPath .githooks`，加入 `.PHONY`，并在文件头注释里加一行说明。
  同时把 `test:` 改成 `CGO_ENABLED=1 go test -tags fts5 ./... -count=1`（修复既有缺陷）。
- `FOREBRAIN.md` 第 10 行改为 `make test         # CGO_ENABLED=1 go test -tags fts5 ./... -count=1`。

**验证**：在临时仓库 `$TMPDIR/hookcheck` 里 `git init`，`git config core.hooksPath <本仓库>/.githooks`，
设置临时 user.name/email：
- `git commit --allow-empty -m 'feat: ok'` → 成功，`git log -1 --format=%B` 含 `Signed-off-by:`
- `git commit --allow-empty -m 'bad title'` → 失败并打印规则
- `make -n test | grep -q 'tags fts5' && echo ok` → `ok`

### 步骤 4：`.gitmessage` 改为英文

保留四节结构和可选节，改为英文。加上签署说明：`git commit -s` 会补签署；装了 hook 会自动补。
标题上限改为 100 字符，与脚本一致。在文件头注释里写出启用方式：`git config commit.template .gitmessage`。

**验证**：`grep -c '[一-龥]' .gitmessage` → `0`（不含中文）；`grep -n 'Signed-off-by' .gitmessage` 有命中

### 步骤 5：CI 检查——PR 标题与 DCO

`.github/workflows/pr-title.yml`：
```yaml
name: PR title
on:
  pull_request_target:
    types: [opened, edited, synchronize, reopened]
permissions:
  contents: read
jobs:
  conventional-title:
    name: Conventional Commits title
    runs-on: ubuntu-latest
    steps:
      # pull_request_target runs with the base repository's token; check out
      # the base branch only and never execute code from the pull request.
      - uses: actions/checkout@v4
        with:
          ref: ${{ github.event.pull_request.base.sha }}
      - name: Check the title
        env:
          PR_TITLE: ${{ github.event.pull_request.title }}
        run: printf '%s\n' "$PR_TITLE" | scripts/check-commit-message.sh --strict -
```

`.github/workflows/dco.yml`：
```yaml
name: DCO
on:
  pull_request:
permissions:
  contents: read
jobs:
  sign-off:
    name: DCO sign-off
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ github.event.pull_request.head.sha }}
          fetch-depth: 0
      # The checker comes from the base branch, so a pull request cannot
      # change the rule it is checked against.
      - uses: actions/checkout@v4
        with:
          ref: ${{ github.event.pull_request.base.sha }}
          path: .base
      - name: Check sign-off
        run: .base/scripts/check-dco.sh "${{ github.event.pull_request.base.sha }}" "${{ github.event.pull_request.head.sha }}"
```

**验证**：YAML 语法与 actionlint 两条命令都通过。`grep -n 'pull_request.title' .github/workflows/*.yml`
只出现在 `env:` 下，没有被直接插进 `run:`，以防注入。

### 步骤 6：社区文件（英文）

- `CONTRIBUTING.md`，按下列小节写（从 README 现有 Contributing 一节迁移内容，然后扩写）：
  1. *Ways to contribute*：issue、讨论、文档、代码。
  2. *Development setup*：Go 1.26 + CGO + C 工具链、Node 22 + pnpm 10；`make build`；日常
     `CGO_ENABLED=1 go build -tags fts5 …` + `scripts/install-dictionary.sh`；`make hooks`。
  3. *Checks*：`go vet ./...`、`make test`、`scripts/package-graph.sh`（改了 pkg 导入时重新生成 graph.json）、
     前端 `pnpm test && pnpm build`。
  4. *Commit and pull request conventions*：上面"约定"一节的全部内容、完整例子、`BREAKING CHANGE` 写法，
     以及"PR 正文就是 squash 后的提交正文"。
  5. *Developer Certificate of Origin*：说明 DCO 是什么，链接 https://developercertificate.org/ ，
     写明 `git commit -s`、`git rebase --signoff main`，以及 `make hooks` 会自动签署。
  6. *Ground rules*：README 里现有的五条原样迁移（修根因、保护 prompt cache、一个引擎两个 surface、架构测试、
     CGO 必需）。
  7. *Releases*：一句话说明发版由 release-please 自动完成，贡献者不需要改版本号，也不需要写 CHANGELOG。
     维护者侧细节链接 `docs/maintainers/RELEASING.md`（计划 012）。
     不要写任何评论指令或 agent 自动化：CI 自维护已被否决（见 README"推迟的批次"），对外文档不提。
- `CODE_OF_CONDUCT.md`：Contributor Covenant 2.1 原文（取自
  https://www.contributor-covenant.org/version/2/1/code_of_conduct/code_of_conduct.md ），
  **不设举报渠道**（D6，owner 要求彻底删除）："Enforcement" 一节中以 "Instances of abusive, harassing, or otherwise
  unacceptable behavior may be reported" 开头、含占位符 `[INSERT CONTACT METHOD]` 的整段删除，包括该段后面关于
  "All complaints will be reviewed" 与尊重举报人隐私的两句。"Enforcement Guidelines" 及之后的内容保留原文。全文不得出现任何
  联系方式或占位符。
- `SECURITY.md`：支持的版本（只有最新 release），报告方式（GitHub 私密漏洞报告：
  `https://github.com/forebrain-harness/forebrain-harness/security/advisories/new`），请勿公开提 issue，
  会尽快确认收到。不要承诺具体天数。
- `.github/CODEOWNERS`：`* @wubin1989`
- `.github/PULL_REQUEST_TEMPLATE.md`：四个二级标题 `## Why`、`## What`、`## Prompt cache`、`## Verification`，
  每节下用一行 HTML 注释给出填写提示。末尾注释提醒：标题要符合 Conventional Commits；破坏性变更加
  `BREAKING CHANGE:` 脚注；这段正文会成为 squash 提交的正文。不要放 checkbox 清单，因为它会进入提交历史。
- `.github/ISSUE_TEMPLATE/bug_report.yml`：issue form，字段依次为 version（`forebrain --version` 的输出，必填）、
  install method（下拉：npm / go install / source）、OS、provider+model、steps、expected、actual、logs
  （提示先去掉隐私信息；日志在 `~/.forebrain/logs/`）。
- `.github/ISSUE_TEMPLATE/feature_request.yml`：problem、proposal、alternatives。
- `.github/ISSUE_TEMPLATE/config.yml`：`blank_issues_enabled: false`，`contact_links` 两项：安全问题
  （链接 advisories/new）和文档站 https://forebrain-harness.github.io 。

**验证**：
- `ls CONTRIBUTING.md CODE_OF_CONDUCT.md SECURITY.md .github/CODEOWNERS .github/PULL_REQUEST_TEMPLATE.md .github/ISSUE_TEMPLATE/{bug_report,feature_request,config}.yml` 全部存在
- `grep -rn '[一-龥]' CONTRIBUTING.md CODE_OF_CONDUCT.md SECURITY.md .github/` → 无输出
- `grep -rn '@gmail.com\|wubinwill' CONTRIBUTING.md CODE_OF_CONDUCT.md SECURITY.md .github/` → 无输出
- `grep -niE 'INSERT CONTACT METHOD|may be reported|@[a-z0-9.-]+\.(com|net|org|cn)' CODE_OF_CONDUCT.md` → 无输出（没有举报渠道、占位符或邮箱）

### 步骤 7：README 瘦身、核对 `.gitignore`、`.gitattributes`

- README "Contributing" 一节缩成 3–5 行：欢迎贡献，链接 `CONTRIBUTING.md` 和 `CODE_OF_CONDUCT.md`，
  提一句 DCO 与 Conventional Commits。"Security" 小节改为链接 `SECURITY.md`。ground rules 已迁到 CONTRIBUTING，
  README 里删掉。README 是面向用户的介绍，按项目规则只讲用户能得到什么。
- `.gitignore`：核对 M0 已加入 `/forebrain-harness.github.io/` 及其注释（不要重复添加）。
- 新建 `.gitattributes`：`* text=auto eol=lf`，以及 `*.bat text eol=crlf`（如果仓库里有 .bat；没有就不写这一行）。

**验证**：`git check-ignore forebrain-harness.github.io && echo ignored` → `ignored`；
`git status --short | grep forebrain-harness.github.io` → 无输出

## 测试计划

这里不涉及 Go 代码。三个脚本由步骤 1–3 的命令验证。工作流由 YAML 语法检查和 actionlint 验证。
真实触发要等仓库推上 GitHub 后才能发生，由计划 012 的"首次上线核对清单"覆盖。

## 真实仓库实测（关卡 G1；需要仓库设置的部分顺延到 G2）

1. 社区文件被 GitHub 识别：`gh api repos/forebrain-harness/forebrain-harness/community/profile --jq '{health_percentage, files: (.files|keys)}'`
   → `code_of_conduct`、`contributing`、`issue_template`、`pull_request_template`、`license`、`readme` 都非空。
2. PR 标题检查与 DCO，用一个测试 PR 实测：
   ```bash
   git switch -c live-test/006-dco origin/main
   git commit --allow-empty --no-verify -m "test: check the dco gate without a sign-off"   # 故意不签署
   git push -u origin live-test/006-dco
   gh pr create --repo forebrain-harness/forebrain-harness --base main --head live-test/006-dco --title "[live-test] bad title" --body "Live test for plan 006."
   ```
   - `PR title` 工作流失败，且日志里有那一句规则说明；
   - `DCO` 工作流失败，并点名那个未签署的提交。
   - `gh pr edit <n> --title "test: check the pull request gates"` → PR title 检查重新运行并通过。
   - 再加一个签署过的空提交（`git commit --allow-empty -s -m "test: add a signed-off commit"`），推送后 DCO 仍然失败，
     因为第一个提交没签；在报告里确认它只点名第一个提交。
   - 关闭 PR：`gh pr close <n> --delete-branch`。回到 main：`git switch main && git branch -D live-test/006-dco`。
3. 本地 hook 实测：在一份真实仓库的全新克隆里执行 `make hooks`，然后 `git commit --allow-empty -m "feat: hook check"`，
   检查 `git log -1 --format=%B` 含 `Signed-off-by`；`git commit --allow-empty -m "bad"` 被拒绝。克隆测完删除。
4. G2 之后补测：`gh api repos/forebrain-harness/forebrain-harness --jq .squash_merge_commit_message` → `PR_BODY`（D7）。
5. 记录 PR 链接、各检查的运行链接与结论。

## 完成标准

- [ ] M5 已按规范提交
- [ ] 真实仓库实测第 1–3 步通过并记录（第 4 步在 G2 后补齐）
- [ ] 步骤 1–7 的验证全部通过
- [ ] `actionlint` 对 `.github/workflows/` 无输出
- [ ] 新增的对外文件不含中文，也不含任何个人邮箱
- [ ] 改动全部在允许清单内

## STOP 条件

- actionlint 报告的问题不在本计划新增的两个工作流里。那属于 `ci.yml`/`security.yml` 的既有问题，汇报即可，
  不要顺手改（计划 007 处理）。
- 发现 `forebrain-harness.github.io` 是有意作为子模块使用的，例如出现 `.gitmodules` 或文档提到子模块。

## 维护说明

- 新增 type 或改长度上限时，只改 `scripts/check-commit-message.sh` 一处。hook、CI、CONTRIBUTING 的例子都以它为准，
  CONTRIBUTING 要同步改文字。
- release-please 只认 `feat`/`fix`/`perf`/`revert` 和破坏性标记来决定版本；其他 type 只进 CHANGELOG 的隐藏分组
  （见计划 008 的 changelog-sections）。
- `FOREBRAIN.md` 会进入本项目会话的 prompt。本计划只改其中一行，下一个新会话起生效，当前会话不受影响，
  缓存前缀在会话内保持稳定。
