# 开源规范、自动发版与自维护：实施计划索引

由 improve skill 于 2026-09-28 生成；owner 已于同日批准执行（"严格按照 docs/plan/OSS_AUTOMATION/ 目录下的计划实施、落地和收口"）。

目标（owner 原话）：
- "我需要一套规范的、遵循 github 开源项目最佳实践的一套机制"；
- "尽可能自动化，我希望整个项目可以由 forebrain harness 自己维护"；
- "我希望本项目有两种发布和安装方式：1. go install 2. npm install"；
- "我期望的自维护是通过 forebrain gateway 服务自维护，只在本机或者我个人的 linux 服务器里部署"（2026-09-28）。

据此本期范围是：**全套社区规范 + 自动发版 + go install 与 npm 两种安装方式 + 文档站**。CI 自维护（原 003、009–011）
被否决并推迟到首发后的 gateway 批次（见 [推迟的批次](#推迟的批次gateway-自维护首发后另立计划)）；GitHub 上不配置
任何模型或模型 key。

各计划都按照零上下文、能力较弱的执行者来写：现状摘录、每步的验证命令、范围边界和 STOP 条件都写在各自文件里。
按下表顺序执行，每完成一个就更新状态列。

## 全局规则（每个计划都适用）

1. **按里程碑提交**（owner 要求："必须按里程碑和本仓库的 git commit 消息规范和模板提交代码"）：
   - 每个计划是一个里程碑，本地步骤全部通过后**提交一次**。在此之前还有一个基线里程碑 M0。
     标题见下方"里程碑与提交标题"表。
   - 提交信息必须同时符合两套规范：
     - **Conventional Commits**：`<type>(<scope>): <subject>`，小写开头，结尾不加句号，≤72 字符。
       本仓库 `.gitmessage` 目前要求 ≤72；计划 006 放宽到 100。统一按 72 写，两边都满足；
     - **`.gitmessage` 模板**：正文依次为 `Why:`、`What:`、`Prompt cache:`、`Verification:` 四节。
       - `Prompt cache` 必填：不影响时写 `unchanged: <reason>`；
       - `Verification` 只写实际跑过的命令及其结果；
       - 可选节 `Schema:`、`Refs:`、`BREAKING CHANGE:`，其中 `Refs:` 一律写 `docs/plan/OSS_AUTOMATION/<本计划文件名>`。
   - 用 `git commit -s -F <message-file>` 提交：`-s` 生成 DCO 签署，`-F` 让正文逐字保留，不用 `-m`。
     计划 006 完成后，每次提交前先跑 `scripts/check-commit-message.sh <message-file>`；006 之前按上面的规则人工核对。
   - 一个里程碑只提交该计划"范围"内的文件：用 `git add <path>…` 逐个加入，禁止 `git add -A`（M0 除外）。
     提交后 `git status --short` 中不应残留本计划的文件。
   - **永不** `--amend`、`rebase`、`push --force`，也不改写已推送的历史。发现问题就用新的提交修正，同样遵守本规则。
2. **必须用真实仓库实测**（owner 要求："必须直接用 git@github.com:forebrain-harness/forebrain-harness.git 仓库实测"）：
   - 每个计划都有一节"真实仓库实测"，在 `git@github.com:forebrain-harness/forebrain-harness.git`
     （`https://github.com/forebrain-harness/forebrain-harness`）上完成；文档站则在
     `git@github.com:forebrain-harness/forebrain-harness.github.io.git` 与线上站点 https://forebrain-harness.github.io 上完成（计划 013）。
   - 本地检查（单测、actionlint、脚本演练、fake provider）只是前置，**不能代替实测**。
   - 证据（命令、输出摘要、运行链接、PR/issue 链接）追加到 [`LIVE_TEST_LOG.md`](LIVE_TEST_LOG.md)。
     第一次实测时创建，按计划编号分节；它随下一个里程碑一起提交。
   - 分工：
     - **执行者**：提交、推送、观察工作流（`gh run list/watch/view`）、手动触发（`gh workflow run`）、
       创建和关闭测试 issue/PR 与评论、运行 `scripts/setup-github.sh`、核对 Release 与 npm、对真实模块路径执行 `go install`；
     - **owner**：创建 GitHub App、设置密钥（执行者**永远不接触密钥值**），以及**合并 PR**。合并是审批点，
       包括 release PR。执行者不合并任何 PR。
   - 实测用的 issue/PR 标题统一以 `[live-test]` 开头，测完关闭；测试分支以 `live-test/` 开头，测完删除。
     测试分支上的提交同样遵守规则 1 的提交规范。
   - 不手动 `npm publish`，也不手动 `gh release create/upload`。发布只能由发版工作流完成。
3. **推送与关卡**：
   | 关卡 | 前置 | 推送（执行者） | 之后实测 |
   | --- | --- | --- | --- |
   | G1 | M0–M6 已在本地提交（即计划 001、002、004、005、006、007）；文档站 S0、S1 已在其仓库本地提交（计划 013） | `git remote add origin git@github.com:forebrain-harness/forebrain-harness.git`（若未配置），`git push -u origin main`。仓库此时为空，也没有保护规则，直接推 main | 001、002、004、006、007（需要 App 或仓库设置的部分顺延到 G2）；另在文档站目录 `git push -u origin main`，启用 Pages 后实测 013 的 G1 部分 |
   | G2 | owner 已创建 GitHub App 并设好 `FOREBRAIN_APP_ID`、`FOREBRAIN_APP_PRIVATE_KEY`、`NPM_TOKEN`；M7、M8 已在本地提交 | `git push origin main`，然后**立刻**运行 `scripts/setup-github.sh`（先 `--dry-run`），启用 squash-only 与 main 的规则集 | 007 的自动合并、008 的 release PR、012 |
   | G3 | owner 合并首个 release PR | 008 实测通过后，提交并推送文档站 S2 | 008 的发布结果、005 的词典下载、012 的上线清单、013 的 G3 部分 |
   - G2 之后 main 受规则集保护：此后任何修正都走 PR（分支 `fix/<slug>`，PR 标题等于提交标题，正文等于提交正文），
     由 owner 合并。
   - 005 的代码在 G1 就随 M4 入库（008 的词典打包脚本依赖它），但它的实测（真实下载）要等 G3 的首个 Release。
4. **计划状态**：本地步骤通过并完成里程碑提交后，状态记为 `AWAITING PUSH`；真实仓库实测通过后才能记为 `DONE`。
   状态更新随下一次提交一起入库。
5. **prompt cache 命中率只升不降**：本套计划不改 prompt 组装——发版、社区规范与安装方式都不触碰模型上下文。
   `FOREBRAIN.md` 的一行修正只在本仓库**下一个新会话**生效，不改变任何进行中会话的前缀。
6. **修根因**，不加防御式补丁；**面向用户的错误只写一句话**；对外文件与代码注释用英文。
7. **绝不碰真实数据**：开发构建只用临时 `FOREBRAIN_HOME`。
8. M0 之前仓库没有任何提交（远端 `git ls-remote` 也为空），所以各计划没有 "planned-at SHA"；每个计划开头的"漂移检查"
   是核对摘录 + `git log -- <路径>`。M0 之后以提交历史为准。

## 里程碑与提交标题

| 里程碑 | 内容 | 提交标题（Conventional Commits） |
| --- | --- | --- |
| M0 | 基线：当前工作区的全部既有代码与本套计划文档（见下方"M0 基线提交"） | `chore: import the existing codebase`（正文末尾加脚注 `Release-As: 0.1.0`，版本号按 D1） |
| M1 | 计划 001 | `build(deps): fold the silk fork into the module for go install` |
| M2 | 计划 002 | `build: report one semver version from every install method` |
| M3 | 计划 004 | `fix(state): explain a build without cgo or FTS5 in one sentence` |
| M4 | 计划 005 | `feat(memory): download dictionaries on first need` |
| M5 | 计划 006 | `docs: add contribution conventions, DCO and community files` |
| M6 | 计划 007 | `ci: add Dependabot, workflow lint and wider CI coverage` |
| M7 | 计划 008 | `ci(release): automate releases for npm and go install` |
| M8 | 计划 012 | `docs: document installation and maintainer setup` |
| S0–S2 | 计划 013，在**文档站仓库**里提交，规则相同 | 见计划 013 的"文档站里程碑"表 |

原 M3（`forebrain exec`，计划 003）与 M9–M11（CI 自维护，计划 009–011）已推迟，编号**不复用**；推迟批次的提交
标题届时另定。推迟计划文件里残留的旧里程碑编号（M3、M9–M11）以文件开头的"已推迟"横幅为准。

### M0 基线提交

仓库还没有任何提交，所以当前工作区里 owner 已有的全部代码要先作为基线提交，后面的里程碑才有基础。步骤：
1. 在 `.gitignore` 末尾加上 `/forebrain-harness.github.io/` 及其注释（原属计划 006 的一项）。那是文档站的独立仓库，
   不先忽略，`git add -A` 会把它变成一个坏的 gitlink。
2. `git add -A`，然后检查：
   - `git ls-files -s | awk '$1=="160000"'` 必须为空（没有嵌套仓库）；
   - `git diff --cached --stat | tail -1` 记下文件数与行数。
3. 密钥扫描：`go run github.com/zricethezav/gitleaks/v8@latest detect --source . --no-git --redact`（只扫将要提交的内容，
   结果中的值已脱敏）。有任何发现就 **STOP**，只报告 `file:line` 与类型，**不得复述密钥内容**。
4. 单个文件大于 5 MB 的，列出来并 **STOP** 汇报：`git diff --cached --name-only -z | xargs -0 du -k | awk '$1>5120'`。
5. `git commit -s -F <message-file>`：
   - 标题见上表；
   - `Why:` 说明这是公开发布前的基线；
   - `What:` 概述顶层目录；
   - `Prompt cache: unchanged: no code change`；
   - `Verification:` 写第 2–4 步实际跑过的检查；
   - 脚注 `Release-As: 0.1.0`。

## 执行顺序与状态

| 计划 | 标题 | 优先级 | 工作量 | 依赖 | 状态 |
| --- | --- | --- | --- | --- | --- |
| [001](001-go-installable-module.md) | 并入 silk fork、去掉 replace，让模块可被 go install | P1 | S | — | DONE|
| [002](002-release-version-identity.md) | VERSION 改三段 semver，二进制在任何安装方式下报告正确版本 | P1 | S | 001 | DONE|
| [003](003-forebrain-exec.md) | ~~新增 `forebrain exec`~~ | — | — | — | DEFERRED（gateway 批次） |
| [004](004-sqlite-build-requirement.md) | 缺 cgo/FTS5 时一句话报错并给出安装命令 | P2 | S | — | DONE|
| [005](005-dictionary-download.md) | 首次需要时下载分词词典（校验值编进二进制） | P1 | M | 002 | DONE（下载路径 CI+资产往返验证；本机链路不稳时错误为一句话且不重试风暴，词典就位后真实模型中文检索通过）|
| [006](006-contribution-conventions.md) | 贡献规范与社区文件：Conventional Commits、squash、DCO、模板、检查 | P1 | M | — | DONE|
| [007](007-dependency-and-workflow-hygiene.md) | Dependabot、自动合并、actionlint、CI 补强 | P2 | S | 006 | DONE（auto-merge 队列待下一个 Dependabot PR 观察）|
| [008](008-release-automation.md) | release-please、UI 随 release PR 入库、五平台构建、Release 与 npm 发布、go install 验收 | P1 | L | 001, 002, 005, 006, 007 | DONE（v0.1.1 发布全绿；v0.1.0 为空快照，资产在 v0.1.1）|
| [009](009-agent-ci-foundation-and-command.md) | ~~CI 自维护地基与 `/forebrain` 评论指令~~ | — | — | — | DEFERRED（已否决 CI 跑 agent；gateway 批次） |
| [010](010-agent-pr-review.md) | ~~PR 自动审阅（CI agent）~~ | — | — | — | DEFERRED（gateway 批次） |
| [011](011-agent-ci-fix-and-maintenance.md) | ~~main CI 失败自动修复；每周巡检（CI agent）~~ | — | — | — | DEFERRED（gateway 批次） |
| [012](012-install-docs-and-maintainer-setup.md) | 安装文档、维护者文档、仓库配置脚本、首次上线清单 | P1 | M | 001, 002, 004–008 | DONE|
| [013](013-docs-site-repository.md) | 文档站源码入库并推送到 `forebrain-harness.github.io`，Pages 自动部署，补上安装说明 | P1 | S | S0/S1 无；S2 依赖 004 与 008 首发 | DONE（S2 已合并部署，线上安装文档核对通过）|

状态取值：TODO | IN PROGRESS | AWAITING PUSH（本地完成，等推送后实测）| DONE（真实仓库实测通过）|
（已收官：v0.1.1 全自动发布成功。）
DEFERRED（推迟到 gateway 批次）| BLOCKED（附一句原因）| REJECTED（附一句理由）

## 依赖说明

- 001 → 002：002 要扩展 001 新增的 `scripts/check-go-install.sh`，用来验证 go install 构建报告的版本。
- 002 → 005：词典下载靠 `home.Version` 找到对应 Release。
- 006 → 007 → 008：PR 标题规则决定 Dependabot 与 release-please 的提交格式；007 定下 action 钉版规则。
- 013 的 S0、S1 与其他计划无关；S2 的安装命令文案来自 004，且要等 008 的首个 Release 实测通过后才写，保证文档所写的
  都已可用。
- 001、006 之间没有依赖，可以并行开工。

## 推迟的批次：gateway 自维护（首发后另立计划）

owner 已定：自维护通过部署在**本机或个人 Linux 服务器**上的 `forebrain gateway` 服务完成，不在 GitHub Actions 里跑
agent；gateway 用**定时轮询**发现 GitHub 上的待办（维护者的 `/forebrain` 评论、新 PR、main 上 CI 失败、每周巡检）。
改动仍只以 PR 交付，由 owner 合并；只有 owner 能决定的事（计划审批、提问）仍交给 owner。模型与 key 只存在于 owner 的
gateway 部署里，GitHub 上不存。

下一批计划开工前必须先核实三件事（本期勘察已完成第 1 件的代码核实）：
1. **定时任务的项目绑定尚未生效**：`state.CronJob` 有 `ProjectID`，但 `pkg/turn/scheduler.go` 的 `fire` 调
   `RunPrompt(ctx, sessionID, cronChannelID, job.Prompt)` 时不传它，`RunAgentOnceSupervised`
   （`pkg/process/worker_cli.go`）也不接收工作目录——fire 的文件操作落在哪个目录取决于 Runner 的路径解析器与
   gateway 进程的启动目录，与 job 绑定的项目无关。做 gateway 批次前要先把这条链路打通。
2. **无人值守回合遇到审批/提问时的行为**：要核实工具审批在无人值守 fire 里的具体路径（等待、失败还是硬拒——项目
   规则禁止硬拒），以及把问题交给 owner 的通道（渠道投递或 web 审批）。
3. **`forebrain exec` 是否还需要**（原 003）：gateway 的定时任务走 `RunAgentOnceExec`（`pkg/process/one_shot.go`），
   不需要新 CLI 命令；exec 只有在"手动一次性触发"仍有独立价值时才做。

原 [009](009-agent-ci-foundation-and-command.md) 文件里的安全模型——分离执行与写入、不可信文本只以数据传入、只响应
维护者、不跑 fork PR——对 gateway 批次仍然适用，落到 gateway 的实现上。

## 需要 owner 拍板的决策（各计划按"推荐"编写；答复不同于推荐时，相关计划要先改写再执行）

| # | 决策 | 推荐 | 理由 | 影响的计划 |
| --- | --- | --- | --- | --- |
| D1 | 首个版本号与版本策略 | 首发 **0.1.0**，长期停在 0.x（破坏性变更只升次版本） | Go 模块到 v2 必须改导入路径（加 `/v2`），否则 `go install …@latest` 拿不到新版本；停在 0.x 能避开这一点。现有的 `1.0.0.0` 不是合法 semver | 002、008、012 |
| D2 | 自动化使用的身份 | 新建一个 **GitHub App**（Contents/PR/Issues 读写，不给 Workflows 权限） | `GITHUB_TOKEN` 推送的提交和开的 PR 不会触发 CI；PAT 绑定个人账号，权限过大 | 008、012 |
| D3 | Intel mac 二进制怎么构建 | 在 arm64 runner 上用 `clang -arch x86_64` **交叉编译** | Intel mac runner 正在被 GitHub 淘汰；计划 007 先在 CI 里验证交叉编译可行，不可行就改用 `macos-15-intel` | 007、008 |
| D4 | 评论指令的触发词与范围 | **已顺延**到 gateway 批次：触发词用 `/forebrain`（不用 `@forebrain`——GitHub 上真实存在用户 `forebrain`，2014 年注册）；不在 fork PR 上运行 | 防止不断 @ 陌生用户；fork 里的代码和文本可能带提示注入 | 推迟批次（原 009、010） |
| D5 | npm 发布认证 | 首发用 **`NPM_TOKEN`**（granular token），首发之后改为 npm **Trusted Publishing**（OIDC） | Trusted Publishing 要对已存在的包配置，首发前做不到 | 008、012 |
| D6 | 行为准则（Code of Conduct）的举报渠道 | **已由 owner 决定**（2026-09-28）：**不设举报渠道**，`CODE_OF_CONDUCT.md` 里删除整段举报说明，不出现任何联系方式 | owner 明确要求彻底删除 | 006 |
| D7 | squash 合并的提交信息 | 标题 = PR 标题，正文 = **PR 正文** | 如果用"提交列表"，其中的 `fix:` 等行会被 release-please 当成额外的 changelog 条目；PR 模板沿用 `.gitmessage` 的 Why/What/Prompt cache/Verification 四节 | 006、012 |
| D8 | CI 里 agent 用的模型与 key | **已作废**（2026-09-28）：CI 自维护被否决后，GitHub 上**不配置任何模型或模型 key**；模型与 key 只存在于 owner 自己的 gateway 部署里 | 公开仓库的 Actions 日志、artifact、PR 与评论任何人都可见，agent 的 shell 能读到进程环境里的 key | 无（原 009–011） |
| D9 | 谁提交、谁推送 | **已由 owner 确认**（2026-09-28）：执行者按里程碑提交，并在 G1、G2（保护规则启用前）直接推送 main 做实测；G2 之后改动一律走 PR；合并 PR（含 release PR）由 owner 负责 | 实测要求代码先到 GitHub | 全部 |
| D10 | M0 基线由谁提交 | **已由 owner 确认**（2026-09-28）：由执行者按"M0 基线提交"的步骤提交当前工作区全部内容（含此前未提交的改动），提交前做嵌套仓库、密钥和大文件检查 | 仓库还没有任何提交，后续里程碑需要一个基线 | M0 |
| D11 | 自维护的载体与时机 | **已由 owner 决定**（2026-09-28）：通过部署在本机或个人 Linux 服务器上的 `forebrain gateway` 服务自维护；首发不含自维护，首发后另立一批计划审批；gateway 定时轮询 GitHub | owner 拒绝把模型 key 放进公开仓库的 CI（泄漏途径见 D8）；本机/个人服务器上的 key 不离开自己控制的基础设施 | 推迟批次（原 003、009–011） |

另外一处与惯例不同，请知悉：main 的规则集（计划 012）设了"必须解决所有审阅讨论才能合并"。这作用于人工审阅；
以后 agent 审阅加入时同样适用。嫌麻烦可以去掉这一项。

## 顺带发现并纳入本期的既有缺陷

| 缺陷 | 位置 | 纳入计划 |
| --- | --- | --- |
| `go.mod` 的 `replace` 让 go install 无法使用 | `go.mod:112` | 001 |
| 四处版本号互相矛盾，且 `1.0.0.0` 不是合法 semver | `VERSION`、`pkg/home/version.go`、`Dockerfile:14`、`npm/package.json` | 002 |
| exec 所需的通知队列没有排空手段，`Close` 会丢弃排队中的通知 | `pkg/tui/chat_session.go` 的 `stopUINotificationDispatcher` | 推迟（gateway 批次） |
| `make test` 缺 `-tags fts5`，必然失败；`FOREBRAIN.md` 的描述同样错误 | `Makefile`、`FOREBRAIN.md:10` | 006 |
| 文档站仓库嵌套在本仓库中且未被忽略，`git add -A` 会生成坏的 gitlink | `forebrain-harness.github.io/`、`.gitignore` | M0（基线提交前必须先修） |
| CI 前端 job 的 `pnpm install` 没有 `--frozen-lockfile` | `.github/workflows/ci.yml` | 007 |
| `npm/package.json` 声明发布 `README.md`，但该文件不存在 | `npm/` | 012 |

## 考虑过并否决的方案（避免重复讨论）

- **在 GitHub Actions 里跑 agent 做自维护（原 009–011）**：owner 否决。公开仓库的 Actions 日志、artifact、PR 与评论
  任何人都可见，而 agent 的 shell 能读到进程环境里的模型 key，key 可以经这些途径泄漏。自维护改为 owner 自己的
  gateway（见"推迟的批次"），GitHub 上不存任何模型 key。
- **UI 只进 tag、不进 main（tag 打在脱离 main 的提交上）**：release-please 以 Release 所在提交在 main 历史上的位置判断
  上次发版；脱离 main 会让它把全部历史当作未发版提交，CHANGELOG 错乱。
- **缺 FTS5 时编译期直接失败（build tag 触发编译错误）**：不带 tag 的 gopls、`go vet` 会整片报错，报错文字也不是一句话。
  你选的是运行时一句话报错。
- **词典改从 proxy.golang.org 下载模块 zip，用 go.sum 哈希校验**：你选的是 GitHub Release；而且 gse 模块 zip 远大于所需的
  几个词典文件。
- **每次推送都自动审阅 PR**（属已推迟批次内的取舍）：成本高，旧审阅会堆积。原设计改为开 PR/转为 ready 时审阅，之后靠
  `/forebrain review` 手动触发。
- **剥离 agent shell 工具环境里的 LLM key**：CI 不再跑 agent 后已无意义；若 gateway 批次需要，再评估。
- **发布 Docker 镜像**：不在两种安装方式之内，本期不做。

## 本次没有覆盖的内容

- gateway 自维护（原 003、009–011）：推迟到首发后的批次，见"推迟的批次"；届时先核实那里列出的三件事，再写计划交
  owner 审批。
