# Plan 174：CI 里的 web e2e —— owner 决定「不做」（决策记录，非实施计划）

> **执行者须知**：这份文件**没有代码要改**。它是 owner 2026-10-26 的裁定记录：**CI 里不加 web e2e**。
> 执行的唯一动作是把 `plans/README.md` 的状态行标为 REJECTED（若评审方维护索引则不改）。
> 若将来有人想推翻这个决定，从下面「若要重启」一节开始，不要从零论证。

## Status

- **Priority**: — （不做）
- **Effort**: — （若要重启：M，见「若要重启」）
- **Risk**: —
- **Depends on**: —
- **Blocks**: —
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: dx（决策）
- **Decision**: **REJECTED（owner 2026-10-26）**：维持现状——CI 不跑 web e2e。

## 决定与理由

现状（`grep -n "^  [a-z-]*:$" .github/workflows/ci.yml`）有 6 个 job：`go`、`windows-tests`（非阻断）、
`windows`、`macos`、`frontend`、`webui-dist`。其中 `frontend` 跑的是
`pnpm install --frozen-lockfile && pnpm typecheck && pnpm test && pnpm run build`——**vitest 有，浏览器 e2e 没有**。
浏览器 e2e 只在本地：`scripts/acceptance/web_e2e.sh`（47 个 spec，全量约 9 分钟）。

owner 的裁定与既有规则一致（本仓 `AGENTS.md` 的 House rules 原文）：

> **Run only the e2e specs a change touches; the whole browser suite is a release gate, not a per-change loop.**

也就是说"全量 e2e = 发布门禁"这条规则**本来就要求它在发布前跑一次**，而不是每次 PR 都跑。CI 目前没有覆盖它，
owner 选择**保持这个缺口**，理由是收益（PR 拦截）不抵成本（见下"若重启"里的时间与脆弱性），
而两次真的漏到线上的缺陷（154/155）是**真机验收**发现的——那类问题（引擎语义 + 真实模型）本身也不在 e2e 能覆盖的范围里。

## 现在的替代保障（这些都在，不要误以为没有）

1. **`frontend` job**：`pnpm typecheck` + 全量 vitest（约 1100+ 个用例）+ 生产构建。
   网页的绝大多数逻辑（投影、卡片、卡头、审批族、滚动）都在 vitest 里。
2. **`webui-dist` job**：内嵌 UI 产物必须与源码一致（`scripts/check-webui-dist.sh`）。
3. **本地/发布前**：`scripts/acceptance/web_e2e.sh`（全量 47 spec）+ `make test`（Go 全量）。
4. **真机验收**：每个计划的 Done criteria 里按需写死（154/155 就是这样发现问题的）。

## 若要重启：已经量过的成本与形态

（写在这里，将来不用重新调研。）

**一次性/每次运行所需**：

- Go 构建（CGO、`-tags fts5`）：需要 `scripts/fetch-libtokenizers.sh` 与 `scripts/fetch-pdfium.sh`
  （`ci.yml` 的 `go` job 已经这么做，可直接复用那几段 step）。
- `scripts/acceptance/web_e2e.sh` 自己会：起 `forebrain gateway`（隔离 `FOREBRAIN_HOME`）、
  起假 provider（`fake_provider.py`，需要 `python3`）、把 UI 构建到 **scratch 目录**（不写 `pkg/gateway/dist`）、
  用 Playwright 驱动**真 Chrome**（`frontend/package.json` 已带 `@playwright/test 1.64.0`，
  runner 上需要 `pnpm exec playwright install --with-deps chromium`）。
- 时间：构建 ≈ 3–5 分钟 + 全量 ≈ 9 分钟（owner 机器实测）；PR 子集 ≈ 2 分钟 + 构建。

**若重启，推荐形态（当时被否的就是它）**：

```yaml
  web-e2e:
    name: Web e2e (${{ github.event_name == 'pull_request' && 'affected' || 'full' }})
    runs-on: ubuntu-latest
    timeout-minutes: 30
    steps:
      # checkout / setup-go / setup-node / pnpm install（同 frontend job）
      # scripts/fetch-libtokenizers.sh + scripts/fetch-pdfium.sh（同 go job）
      - run: pnpm exec playwright install --with-deps chromium
        working-directory: frontend
      - name: Build the binary
        run: CGO_ENABLED=1 go build -tags fts5 -o /tmp/forebrain ./cmd/forebrain
      - name: Run the specs this PR can reach
        run: scripts/acceptance/web_e2e.sh
        env:
          FOREBRAIN_E2E_SPEC: ${{ github.event_name == 'pull_request' && steps.affected.outputs.specs || '' }}
```

（`FOREBRAIN_E2E_SPEC` 为空 = 全量，这正是脚本已支持的语义；`affected` 那一步是"按改动路径映射 spec"，
需要一个新脚本，例如 `frontend/e2e/affected.sh`——**这是重启时唯一真正的新代码**。）

## 会改变这个决定的触发条件

任一条出现，就值得重启讨论（按可能性排）：

1. 一次线上/发布后才发现的**纯前端接线**回归（不是引擎语义），且 vitest + 真机验收都没拦住。
2. `web_e2e.sh` 的全量时长降到 3 分钟以内（并行化 worker），使 PR 成本可接受。
3. 仓库出现第二个维护者、需要"没有本地环境也能验证"的通道。
4. GitHub 免费额度/runner 策略变化，使 chromium job 的排队成本变得可忽略。

## Done criteria

机器可检查，全部满足：

- [ ] 本文件存在，且 `## Status` 里写着 `REJECTED（owner 2026-10-26）`
- [ ] `plans/README.md` 本行状态为 `REJECTED`，附一句话理由
- [ ] `.github/workflows/` 与 `scripts/acceptance/` **一个字节都没改**（`git status --porcelain .github scripts` 无输出）

## STOP conditions

停下报告，如果：

1. 你以为要加一个 CI job——**不要**，这份文件是"不做"的裁定。
2. 你发现 `web_e2e.sh` 在 CI 里其实已经在跑（`grep -rn "web_e2e" .github/`）——那说明决定被推翻了，
   报告并停手，先确认是谁加的。

## Maintenance notes

- 这条决议的**反面成本**要先说清：CI 不跑 e2e，意味着"PR 能把网页接线改坏"这件事在 CI 里**看不见**。
  与之交换的是 PR 反馈时间与 runner 成本。owner 已知情并选择前者。
- 本仓的 `plans/README.md`「已审核但未计划的发现」里也记了同一条（2026-10-26 分支审计），
  两处都指向本文件；**不要再立第三份**。
