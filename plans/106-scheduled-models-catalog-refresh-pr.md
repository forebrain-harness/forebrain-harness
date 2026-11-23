# Plan 106：每周自动从 models.dev 刷新 `assets/models.json`，有差异就开（或更新）一个 PR

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不用 worktree，
> 改动直接留在 dev 工作区。（工作流文件**将来**在 GitHub 上会自己提交，那不是你现在要做的事。）
>
> **Drift check（先跑）**：
> `git diff --stat 55019a2 -- scripts/update-models-asset.sh .github/workflows/release.yml .github/workflows/ci.yml scripts/check-commit-message.sh`
> 预期**无输出**；并确认 `ls .github/workflows/refresh-models.yml` 报**不存在**（存在 → STOP 1）。
> （工作区里其它既有改动与本计划无关，不要碰、不要回滚。）

## Status

- **Priority**: P3
- **Effort**: S（一个新工作流文件 + 本地验证；无 Go / 前端改动）
- **Risk**: LOW（只新增一个定时工作流；它只会开 PR，不合并、不发布；失败时只是一次失败的运行）
- **Depends on**: none（但工作流要等 `dev` 合入 `main` 之后才会真正按计划运行——定时触发只认默认分支）
- **Category**: dx
- **Planned at**: commit `55019a2`，2026-10-23
- **owner 2026-10-23 选定的做法**：复用 `release.yml` 的 GitHub App 令牌开 PR（不是 `GITHUB_TOKEN`，也不是只开 issue）

## Why this matters

`assets/models.json` 是模型目录（上下文窗口、图片能力、推理档位……），由 `scripts/update-models-asset.sh` 从 models.dev 合成。
这条分支的一次提交（`3d83becf`）就把它整体换了一遍（约 2.8 万行 diff）——靠人记得去跑脚本，目录就会积压：新模型选不到、窗口数值过期、推理档位对不上。
脚本已经是确定性的（`sort_keys`、无时间戳；上游没变就没有 diff）并且对异常响应会大声失败（上游少了 `OVERRIDES` 点名的模型就 `sys.exit`）。
缺的只是「定期跑、有差异就交给人审」这一环。

## Current state

- `scripts/update-models-asset.sh` — 重新生成 `assets/models.json`；不带参数时 `curl -fsS` 下载 `https://models.dev/catalog.json?type=all`，
  输出路径固定为仓库内的 `assets/models.json`，最后向 stdout 打印一行摘要（`entries=… canonical=… injected=… with-options=… …`）。依赖 `bash`、`curl`、`python3`（ubuntu runner 都自带）。
- `.github/workflows/release.yml:28-35` — 已有的 App 令牌用法（**照抄**这一段，包括被固定的提交 SHA）：

```yaml
      # A GitHub App token (not GITHUB_TOKEN) so the release PR and the UI
      # commit pushed by this workflow trigger the other workflows' CI runs.
      # Requires the repo variable FOREBRAIN_APP_ID and the secret
      # FOREBRAIN_APP_PRIVATE_KEY (owner decision D2).
      - id: app
        uses: actions/create-github-app-token@fee1f7d63c2ff003460e3d139729b119787bc349 # v2.2.2
        with:
          app-id: ${{ vars.FOREBRAIN_APP_ID }}
          private-key: ${{ secrets.FOREBRAIN_APP_PRIVATE_KEY }}
```

- `release.yml:62-70` — 用 App 身份提交的写法（`gh api /users/<slug>[bot]` 取 id，拼 noreply 邮箱，`git commit`、`git push`）。**照抄**。
- 仓库对提交与 PR 的约束：`scripts/check-commit-message.sh --strict` 校验 PR 标题（Conventional Commits，`<type>(<scope>): <subject>`，≤100 字符，subject 小写开头不以句号结尾）；
  `scripts/check-dco.sh` 要求每个提交带 `Signed-off-by`，**但 `…[bot]@users.noreply.github.com` 的提交豁免**（脚本开头写明）；
  `.github/workflows/lint-workflows.yml` 对改动工作流的 PR 跑 `actionlint@v1.7.12`；第三方 action 一律固定到完整提交 SHA。
- 校验过的草稿已通过 `actionlint@v1.7.12`（见 Step 1 的内容）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 工作流语法 | `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 .github/workflows/refresh-models.yml` | 无输出，exit 0（首次需要联网拉取） |
| YAML 可解析 | `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/refresh-models.yml'))"` | exit 0 |
| 提交/PR 标题合规 | `printf '%s\n' 'chore(models): refresh models.json from models.dev' \| scripts/check-commit-message.sh --strict -` | exit 0 |
| 脚本确定性 | 见 Step 2 | 两次运行产物哈希一致 |

## Scope

**In scope**：
- `.github/workflows/refresh-models.yml`（新建）

**Out of scope**：
- `scripts/update-models-asset.sh` 本身——它的行为已经满足需要，不改。
- `assets/models.json`——**不要**把脚本跑出来的差异留在工作区（Step 2 验证完要还原）。
- `ci.yml`、`release.yml`、`dependabot-auto-merge.yml`：**不要**给这个 PR 开自动合并。上游是第三方数据，必须有人看过 diff 才合。
- 任何测试 / 构建步骤放进这个工作流——PR 自己会触发完整 CI（因为用的是 App 令牌），重复跑没意义。

## Git workflow

- **不提交**。改动留在 dev 工作区。

## Steps

### Step 1：新建工作流

创建 `.github/workflows/refresh-models.yml`，内容**逐字**如下（包括被固定的 SHA）：

```yaml
# Refresh assets/models.json from models.dev on a schedule and propose the
# result as a pull request. The data is third-party, so the pull request is
# reviewed and merged by a person: nothing here enables auto-merge.
#
# A GitHub App token (not GITHUB_TOKEN) so the pull request triggers the other
# workflows' checks; it needs the repo variable FOREBRAIN_APP_ID and the secret
# FOREBRAIN_APP_PRIVATE_KEY, the same pair release.yml uses.
name: Refresh models catalog
on:
  schedule:
    - cron: "17 3 * * 1"
  workflow_dispatch:
permissions:
  contents: read
concurrency:
  group: refresh-models
  cancel-in-progress: false

jobs:
  refresh:
    name: Refresh assets/models.json
    if: github.repository == 'forebrain-harness/forebrain-harness'
    runs-on: ubuntu-latest
    timeout-minutes: 15
    steps:
      - id: app
        uses: actions/create-github-app-token@fee1f7d63c2ff003460e3d139729b119787bc349 # v2.2.2
        with:
          app-id: ${{ vars.FOREBRAIN_APP_ID }}
          private-key: ${{ secrets.FOREBRAIN_APP_PRIVATE_KEY }}
      - uses: actions/checkout@v4
        with:
          ref: main
          token: ${{ steps.app.outputs.token }}
      - name: Regenerate the catalog
        run: scripts/update-models-asset.sh | tee "$RUNNER_TEMP/summary.txt"
      - name: Open or update the pull request
        env:
          GH_TOKEN: ${{ steps.app.outputs.token }}
          APP_SLUG: ${{ steps.app.outputs.app-slug }}
          BRANCH: chore/refresh-models
        run: |
          if git diff --quiet -- assets/models.json; then
            echo "assets/models.json is already current"
            exit 0
          fi
          uid="$(gh api "/users/${APP_SLUG}%5Bbot%5D" --jq .id)"
          git config user.name "${APP_SLUG}[bot]"
          git config user.email "${uid}+${APP_SLUG}[bot]@users.noreply.github.com"
          git checkout -B "$BRANCH"
          git add assets/models.json
          git commit -m "chore(models): refresh models.json from models.dev"
          git push --force origin "$BRANCH"
          body="$(printf 'Regenerated by `scripts/update-models-asset.sh` from https://models.dev/catalog.json.\n\n```\n%s\n```\n\nThe data is third-party: review the diff before merging. This pull request is not auto-merged.\n' "$(cat "$RUNNER_TEMP/summary.txt")")"
          if [ "$(gh pr view "$BRANCH" --json state --jq .state 2>/dev/null)" = "OPEN" ]; then
            gh pr edit "$BRANCH" --body "$body"
          else
            gh pr create --head "$BRANCH" --base main --title "chore(models): refresh models.json from models.dev" --body "$body"
          fi
```

设计要点（评审时对照）：`git push --force` 到固定分支 `chore/refresh-models`——同一时间只有一个刷新 PR，每周把它更新到最新而不是堆新 PR；
`concurrency` 防止手动触发与定时触发撞车；`permissions: contents: read` 是 `GITHUB_TOKEN` 的权限，写操作全走 App 令牌。

**Verify**：
- `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 .github/workflows/refresh-models.yml` → 无输出，exit 0。
- `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/refresh-models.yml'))"` → exit 0。
- `printf '%s\n' 'chore(models): refresh models.json from models.dev' | scripts/check-commit-message.sh --strict -` → exit 0。
- `grep -n 'auto-merge\|--auto' .github/workflows/refresh-models.yml` 只命中注释里的 "auto-merge" 字样，没有任何 `gh pr merge`。

### Step 2：本地验证脚本是确定性的（需要联网；没有网络就跳过并在报告里写明）

先确认 `git diff --quiet -- assets/models.json` 为 exit 0（`assets/models.json` 在工作区里干净）；不干净 → 跳过本步并在报告里写明（STOP 2 的轻量版）。然后：

```
sum="$(mktemp)"
scripts/update-models-asset.sh                 # 打印 entries=… 摘要行
shasum -a 256 assets/models.json > "$sum"
scripts/update-models-asset.sh >/dev/null
shasum -a 256 assets/models.json | diff - "$sum" && echo deterministic
git diff --stat -- assets/models.json          # 看看上游相对仓库有多少差异（仅供报告）
git checkout -- assets/models.json             # 还原：本计划不改这个文件
rm -f "$sum"
```

**Verify**：输出 `deterministic`；`git diff --quiet -- assets/models.json` 在还原后 exit 0。

## Test plan

工作流无法在本地端到端运行（要 App 令牌和 GitHub 环境）。验证靠 Step 1 的静态检查与 Step 2 的脚本确定性。
**首次真实运行由 owner 触发**：合入 `main` 后在 Actions 页对 "Refresh models catalog" 点 "Run workflow"，观察是否开出 PR、PR 的 CI 是否被触发、再点一次是否只更新而不新开。

## Done criteria

- [ ] `.github/workflows/refresh-models.yml` 存在，内容与 Step 1 一致（`diff` 可比对本计划里的代码块）
- [ ] `actionlint@v1.7.12` 对它 exit 0
- [ ] PR 标题 / 提交信息通过 `scripts/check-commit-message.sh --strict -`
- [ ] 文件里没有 `gh pr merge`、没有 `--auto`
- [ ] `git status --short` 里新增的只有这一个文件；`assets/models.json` 未被修改
- [ ] 没有任何 `git commit` / `git push` / 新分支

## STOP conditions

1. `.github/workflows/refresh-models.yml` 已存在，或 `release.yml` 里的 App 令牌写法已变（SHA 不再是 `fee1f7d6…`）。
2. `assets/models.json` 在工作区里不干净，且你无法确认差异是 owner 有意的（Step 2 不要覆盖它）。
3. `actionlint` 报错且不是笔误——比如 `vars.FOREBRAIN_APP_ID` 被判未定义（那是仓库变量，actionlint 不查；真报错再停）。
4. 想往工作流里加测试 / 自动合并 / 别的触发器——不属于本计划。

## Maintenance notes

- **仓库侧前提（工作流本身做不了）**：`vars.FOREBRAIN_APP_ID` 与 `secrets.FOREBRAIN_APP_PRIVATE_KEY` 必须在仓库里存在（release.yml 已经依赖它们）；
  该 GitHub App 需要 `contents: write` 与 `pull requests: write`（release-please 开 release PR 已经要求这两项）。
- 定时触发只认**默认分支**。`dev` 合入 `main` 之前这个工作流不会按计划运行；`workflow_dispatch` 同理要先在默认分支上存在。
- 上游异常时脚本会 `sys.exit`（形状变了、`OVERRIDES` 点名的模型消失），运行显示失败、不开 PR——这是想要的行为；`OVERRIDES` 里的每一项都带理由（见脚本），上游改了要人来决定。
- 评审刷新 PR 的重点：`pkg/llm/catalog_test.go` 钉住的基线模型是否还在、`reasoning_options` 的档位变化、被删掉的模型是否仍被用户配置引用。
- 想要更激进（上游一变就开 PR）把 cron 改成每日即可；想要更保守把触发改成只有 `workflow_dispatch`。
