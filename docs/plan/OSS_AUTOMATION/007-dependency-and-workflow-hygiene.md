# Plan 007：依赖自动更新与工作流卫生——Dependabot、自动合并、actionlint、CI 补强

> **执行者须知**：逐步执行，每一步都要跑"验证"命令并确认结果与预期一致后才进入下一步。
> 出现"STOP 条件"中的任何一种情况，立即停止并汇报，不要自行发挥。完成后把
> `docs/plan/OSS_AUTOMATION/README.md` 里本计划的状态改成 DONE。
>
> **提交（里程碑 M6）**：本地步骤全部通过后，按 `README.md` 全局规则 1 提交**一次**：标题见 README 的
> "里程碑与提交标题"表，正文按 `.gitmessage` 模板（Why / What / Prompt cache / Verification，外加
> `Refs: docs/plan/OSS_AUTOMATION/<本文件名>`），用 `git commit -s -F <message-file>`；只 `git add` 本计划"范围"内的文件。
> 不得 `--amend`、`rebase`、`push --force`。开工前确认 `git status --short` 干净、`git log -1` 是上一个里程碑。
>
> **真实仓库实测**：按 README 的关卡推送后，在 `git@github.com:forebrain-harness/forebrain-harness.git` 上完成本文件"真实仓库实测"一节，
> 证据写入 `LIVE_TEST_LOG.md`。实测通过前本计划状态只能是 `AWAITING PUSH`。
>
> **漂移检查（先做）**：本计划编写时仓库还没有提交，M0 之后才有历史。开工前逐条核对"现状"中的摘录与实际文件一致，并用
> `git log --oneline -- <范围内路径>` 查看 M0 之后是否有别的提交改过这些文件；有且与摘录不符，就按 STOP 处理。

## 状态

| 项 | 值 |
| --- | --- |
| 优先级 | P2 |
| 工作量 | S |
| 风险 | LOW |
| 依赖 | 006（PR 标题规则决定 Dependabot 的提交前缀） |
| 类别 | dx / security |
| 编写于 | 2026-09-28，基于未提交的工作区 |

## 为什么要做

owner 要求"尽可能自动化"。依赖升级是开源项目最常见的重复劳动，也是供应链风险的来源。Dependabot 负责发现
更新并开 PR；补丁版和次版本的升级在 CI 全绿后自动 squash 合并，主版本升级留给维护者手工处理（首发后的 gateway 自维护见 README 的"推迟的批次"）。工作流本身也是代码，要有 lint。第三方 action 要钉到 commit SHA，防止上游
tag 被改写。

## 现状

- `.github/workflows/ci.yml` 有三个 job：`go`（ubuntu：mod verify、vet、包图校验、manifest 报告、build、test）、
  `windows`（build、silk 检查、test）、`frontend`（`pnpm install` 后 `pnpm run build`）。问题如下：
  - `frontend` 用的是 `pnpm install`，**没有** `--frozen-lockfile`，CI 可能用上与锁文件不一致的依赖（既有缺陷）；
  - 没有 `gofmt` 检查；
  - 没有 `concurrency`，同一 PR 连续推送时旧的运行不会取消；
  - 没有 `timeout-minutes`；
  - 没有 macOS 构建。发版要构建 darwin 二进制（计划 008），macOS 专有的编译问题要到发版时才会暴露。
- `.github/workflows/security.yml`：`govulncheck`，用 `@latest`。
- 前端是 pnpm workspace：`frontend/pnpm-workspace.yaml` 包含 `.`、`../ai-elements-vue/packages/elements`、
  `../ai-elements-vue/packages/shadcn-vue`，锁文件是 `frontend/pnpm-lock.yaml`，pnpm 10.24.0。
- `npm/package.json` 的依赖只有本项目自己的五个平台包，不应被 Dependabot 更新（版本由 release-please 管理）。
- `Dockerfile` 的基础镜像：`node:22-bookworm`、`golang:1.26-bookworm`、`debian:bookworm-slim`。
- 计划 006 规定的 PR 标题格式：`<type>(<scope>): <subject>`，≤100 字符。

## 需要的命令

| 用途 | 命令 | 成功时 |
| --- | --- | --- |
| YAML 语法 | `ruby -ryaml -e 'ARGV.each{|f| YAML.load_file(f)}' .github/dependabot.yml .github/workflows/*.yml` | exit 0 |
| workflow lint | `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7` | 无输出 |
| 查 action 的 SHA | `gh api repos/<owner>/<repo>/commits/<tag> --jq .sha` | 40 位 SHA |

执行前先核实 actionlint 的最新稳定版本号，把上表和工作流里的 `v1.7.7` 统一替换成它
（`gh api repos/rhysd/actionlint/releases/latest --jq .tag_name`）。

## 范围

**允许新建/修改**：`.github/dependabot.yml`、`.github/workflows/dependabot-auto-merge.yml`、
`.github/workflows/lint-workflows.yml`、`.github/workflows/ci.yml`、`.github/workflows/security.yml`。

**不许碰**：其他计划负责的工作流（`pr-title.yml`、`dco.yml`、`release.yml`、`forebrain-*.yml`）。它们的 action 钉版
由各自的计划处理，但要遵守本计划第 4 步定下的规则。

## 步骤

### 步骤 1：`.github/dependabot.yml`

```yaml
version: 2
updates:
  - package-ecosystem: gomod
    directory: /
    schedule: { interval: weekly, day: monday }
    commit-message: { prefix: build, include: scope }
    labels: [dependencies]
    groups:
      go-minor-and-patch:
        update-types: [minor, patch]
  - package-ecosystem: npm
    directory: /frontend
    schedule: { interval: weekly, day: monday }
    commit-message: { prefix: build, include: scope }
    labels: [dependencies]
    groups:
      frontend-minor-and-patch:
        update-types: [minor, patch]
  - package-ecosystem: github-actions
    directory: /
    schedule: { interval: weekly, day: monday }
    commit-message: { prefix: ci, include: scope }
    labels: [dependencies]
    groups:
      actions:
        patterns: ["*"]
  - package-ecosystem: docker
    directory: /
    schedule: { interval: weekly, day: monday }
    commit-message: { prefix: build, include: scope }
    labels: [dependencies]
```

Dependabot 产生的标题形如 `build(deps): bump …`、`ci(deps): bump …`，符合计划 006 的规则。

**验证**：YAML 语法通过；`grep -c 'package-ecosystem' .github/dependabot.yml` → `4`

### 步骤 2：Dependabot 补丁/次版本自动合并

`.github/workflows/dependabot-auto-merge.yml`（按 GitHub 官方文档的写法）：
```yaml
name: Dependabot auto-merge
on: pull_request
permissions:
  contents: write
  pull-requests: write
jobs:
  auto-merge:
    if: github.event.pull_request.user.login == 'dependabot[bot]' && github.repository == 'forebrain-harness/forebrain-harness'
    runs-on: ubuntu-latest
    steps:
      - id: meta
        uses: dependabot/fetch-metadata@<SHA>  # vX.Y.Z
        with:
          github-token: ${{ secrets.GITHUB_TOKEN }}
      - name: Queue a squash merge once required checks pass
        if: steps.meta.outputs.update-type == 'version-update:semver-patch' || steps.meta.outputs.update-type == 'version-update:semver-minor'
        env:
          PR_URL: ${{ github.event.pull_request.html_url }}
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: gh pr merge --auto --squash "$PR_URL"
```

说明写进文件头注释：`--auto` 只是排队，要等分支规则要求的检查全部通过才会真正合并。这依赖计划 012 里的仓库
设置（允许 auto-merge，main 的规则集要求 CI）。没有这些设置时，auto-merge 不能启用，这一步会失败，不会误合并。

**验证**：actionlint 无输出

### 步骤 3：CI 补强（`ci.yml`）

- 顶部加：
  ```yaml
  concurrency:
    group: ${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}
    cancel-in-progress: ${{ github.event_name == 'pull_request' }}
  ```
- 给 `go` 设 `timeout-minutes: 40`，`windows` 设 `60`，`frontend` 设 `20`。
- `go` job 在 `go vet` 之后加一步：
  `- name: gofmt` / `run: test -z "$(gofmt -l cmd pkg third_party)" || { gofmt -l cmd pkg third_party; exit 1; }`。
- `frontend` job：`pnpm install` → `pnpm install --frozen-lockfile`；在 build 前加 `pnpm test`
  （README 的贡献说明要求改前端时跑它）。
- 新增 `macos` job，只编译不测试（控制成本）：
  ```yaml
  macos:
    name: macOS build (CGO)
    runs-on: macos-14
    timeout-minutes: 30
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod, cache-dependency-path: go.sum }
      - run: CGO_ENABLED=1 go build -tags fts5 ./...
      - run: CGO_ENABLED=1 GOARCH=amd64 CC="clang -arch x86_64" go build -tags fts5 -o /dev/null ./cmd/forebrain
  ```
  第二步证明 Intel mac 二进制可以在 arm64 runner 上交叉编译，这是计划 008 的做法（D3）。

**验证**：actionlint 无输出。本机为 macOS arm64 时，运行 `CGO_ENABLED=1 GOARCH=amd64 CC="clang -arch x86_64" go build -tags fts5 -o /dev/null ./cmd/forebrain`
→ exit 0。**失败就 STOP**，因为 D3 的推荐方案依赖它。

### 步骤 4：action 钉版规则

在所有由本计划负责的工作流里：
- GitHub 官方 action（`actions/*`）保持主版本 tag，例如 `actions/checkout@v4`。
- 第三方 action（`pnpm/action-setup`、`dependabot/fetch-metadata`）钉完整 commit SHA，并在行尾注释版本，
  例如 `pnpm/action-setup@<40位SHA> # v4.1.0`。SHA 用"查 action 的 SHA"命令取得。

Dependabot（github-actions 生态）会连同注释一起更新 SHA。

**验证**：`grep -nE 'uses: [^a][^ ]*@v[0-9]' .github/workflows/{ci,security,dependabot-auto-merge,lint-workflows}.yml`
→ 无输出（除 `actions/` 外没有按 tag 引用的 action）

### 步骤 5：工作流 lint

`.github/workflows/lint-workflows.yml`：`on: pull_request` 与 `push: branches: [main]`，限定路径
`paths: ['.github/workflows/**', '.github/actions/**']`，`permissions: contents: read`。job 为 checkout +
setup-go + `go run github.com/rhysd/actionlint/cmd/actionlint@<版本>`。

**验证**：本地跑 actionlint 对全部工作流无输出

## 真实仓库实测（关卡 G1；自动合并在 G2 之后）

1. main 上 CI 的 `macOS build (CGO)` job 成功（其中包括 darwin/amd64 交叉编译那一步）；`Frontend install build` 以
   `--frozen-lockfile` 成功；`lint-workflows` 工作流成功。
2. Dependabot 已生效：推送后 Dependabot 会按配置跑一次版本更新。`gh pr list --repo forebrain-harness/forebrain-harness --author app/dependabot`
   在一小时内出现 PR；如果都已是最新，就在 Insights → Dependency graph → Dependabot 页面确认四个生态都显示 "Last checked"，
   截取状态文字记录。
3. G2（`setup-github.sh` 启用 auto-merge 与规则集）之后，挑一个补丁或次版本的 Dependabot PR：
   `gh pr view <n> --repo forebrain-harness/forebrain-harness --json autoMergeRequest --jq .autoMergeRequest.mergeMethod` → `SQUASH`。
   CI 全绿后它被自动合并（`gh pr view <n> --json state` → `MERGED`）。这是由规则允许的自动合并，不是执行者合并。
4. 记录链接与结论。

## 完成标准

- [ ] M6 已按规范提交
- [ ] 真实仓库实测第 1–2 步通过并记录（第 3 步在 G2 后补齐）
- [ ] actionlint 对 `.github/workflows/` 无输出
- [ ] 步骤 3 的 darwin/amd64 交叉编译在本机成功
- [ ] 步骤 4 的 grep 无输出
- [ ] 改动全部在允许清单内

## STOP 条件

- darwin/amd64 交叉编译失败。D3 需要改成 Intel runner（`macos-15-intel`），由 owner 决定。
- actionlint 对 `ci.yml` 报出本计划没有预料到的问题，并且修复需要改变 job 的语义。
- `pnpm install --frozen-lockfile` 在本机失败，说明锁文件与 package.json 不一致。先汇报，不要重生成锁文件。

## 维护说明

- 词典模块（gse、kagome-dict/ipa）的升级会让 `TestDictionaryFilesDeclareTheBytesTheirModulesShip`（计划 005）失败，
  auto-merge 会因此停住。维护者本地按计划 005 的步骤 1 重算校验值并推一个修正 PR 即可。
- 如果 Dependabot 对 pnpm workspace 解析失败（跨出 `frontend/` 的 `../ai-elements-vue` 包），它会在 Insights → Dependency
  graph → Dependabot 里报错。届时改成只更新 `frontend/package.json` 的直接依赖，或加 `ignore`。
