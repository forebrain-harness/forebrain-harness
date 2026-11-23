# Plan 103：补齐 CI 门禁——修掉 HEAD 上的 gofmt 违规，前端加类型检查并清掉现有的 4 个类型错误

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不用 worktree，
> 改动直接留在 dev 工作区。
>
> **执行顺序**：本计划的 Step 3 要改 `frontend/src/views/ProvidersView.test.ts`，计划 `100-provider-edit-replaces-in-place.md`
> 也改这个文件——**先做 100，再做本计划**；Step 1、Step 2 与 100 无关，可以先做。
>
> **Drift check（先跑）**：
> ```
> git diff --stat 55019a2 -- pkg/tui/notify.go .github/workflows/ci.yml frontend/package.json     # 预期无输出
> ```
> `frontend/src/views/ProvidersView.test.ts` 在工作区里有未提交改动，不用 git diff，Step 3 按模式定位、以 `vue-tsc` 的输出为准。
> 有输出就对照下面 "Current state" 的摘录：不符即 STOP 1。
> （工作区里 `pkg/gateway/*`、`pkg/config/*`、`pkg/run/*`、`pkg/turn/*` 等的既有改动与本计划无关，不要碰、不要回滚。）

## Status

- **Priority**: P2（Step 1 是 CI 一推就红的东西；Step 2/3 是堵住「类型错误没人发现」的口子）
- **Effort**: S
- **Risk**: LOW（一处纯空白格式、一个 npm script、一个 CI 步骤、4 处测试 mock 的字段补全）
- **Depends on**: `100-provider-edit-replaces-in-place`（仅 Step 3，同改一个测试文件）
- **Category**: dx
- **Planned at**: commit `55019a2` + 未提交工作区，2026-10-23

## Why this matters

1. **HEAD 上有一处 gofmt 违规**：`pkg/tui/notify.go:53-56` 的结构体字段对齐不对（`gofmt -d` 实测）。CI 的 `go vet` 之后有
   `test -z "$(gofmt -l cmd pkg third_party)"`（`.github/workflows/ci.yml:61`），下一次推送这一步就会红。是 `f44aa0d` 里 TUI 卡片改动带进来的。
2. **前端 CI 不做类型检查**：`.github/workflows/ci.yml:216-221` 的前端任务只跑 `pnpm test` 与 `pnpm run build`，
   `frontend/package.json` 的 `scripts` 里也没有类型检查脚本（`vue-tsc` 已在 devDependencies）。后果已经出现：
   工作区里 `forebrainApi.providersPage`（`frontend/src/lib/api.ts`）的返回类型新增了 `defaultModel`，`ProvidersView.test.ts` 的 4 处 mock 没跟着补，
   `npx vue-tsc --noEmit` 报 4 个 `TS2345`——全项目只有这 4 个类型错误，说明现在补门禁的成本几乎为零，越晚越贵。

## Current state

**gofmt 违规**（`gofmt -d pkg/tui/notify.go` 实测，节选）：

```
@@ -50,10 +50,10 @@
-	Title     string
-	ToolMeta  tool.ToolMeta
-	AgentID   string
-	RunID     string // Non-empty for subagent frames so reducer keys on child run, not primary.
+	Title    string
+	ToolMeta tool.ToolMeta
+	AgentID  string
+	RunID    string // Non-empty for subagent frames so reducer keys on child run, not primary.
```

**CI**（`.github/workflows/ci.yml:202-221`）：

```yaml
  frontend:
    name: Frontend install build
    ...
      - name: pnpm install, test and build
        working-directory: frontend
        run: |
          pnpm install --frozen-lockfile
          pnpm test
          pnpm run build
```

**`frontend/package.json`**：

```json
  "scripts": {
    "dev": "vite",
    "test": "vitest run",
    "build": "vite build",
    "preview": "vite preview",
    "e2e": "playwright test -c e2e/playwright.config.ts"
  },
```

`devDependencies` 里已有 `"vue-tsc": "^2.1.10"`。

**当前的 4 个类型错误**（`cd frontend && npx vue-tsc --noEmit`，行号会因计划 100 的新增测试而偏移，以命令输出为准）：

```
src/views/ProvidersView.test.ts(260,61): error TS2345: ... Property 'defaultModel' is missing in type '{ rows: ProviderRecord[]; total: number; page: number; pageSize: number; }' ...
src/views/ProvidersView.test.ts(280,61): error TS2345: ...
src/views/ProvidersView.test.ts(317,61): error TS2345: ...
src/views/ProvidersView.test.ts(444,63): error TS2345: ...
```

四处都是 `vi.mocked(forebrainApi.providersPage).mockResolvedValue({ rows: [...], total: 1, page: 1, pageSize: 10 })` 或
`vi.spyOn(forebrainApi, 'providersPage').mockResolvedValue({ ... })` 少了 `defaultModel` 字段。同文件里别处已经写成
`{ rows: [provider()], total: 1, page: 1, pageSize: 10, defaultModel: 'deepseek/deepseek-v4' }`（约 `:207`）——**那是对的样子**。
缺字段的这几处，页面代码拿到 `undefined` 时 `isDefaultRow` 判定为 false；补成 `''` 与此行为完全等价，不会改变任何断言。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| gofmt | `test -z "$(gofmt -l cmd pkg third_party)"` | exit 0 |
| 前端类型 | `cd frontend && pnpm typecheck`（等价 `npx vue-tsc --noEmit`） | exit 0，无输出 |
| 前端单测 | `cd frontend && npx vitest run src/views/ProvidersView.test.ts` | 全绿 |
| TUI 测试 | `go test -tags fts5 ./pkg/tui/ -count=1` | `ok`（约 70 秒） |
| 工作流语法 | `python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/ci.yml'))"` | exit 0 |

## Scope

**In scope**：
- `pkg/tui/notify.go`（只允许 `gofmt -w` 产生的那几行空白变化）
- `frontend/package.json`（只加一个 `typecheck` 脚本）
- `.github/workflows/ci.yml`（只在前端任务里加一个步骤）
- `frontend/src/views/ProvidersView.test.ts`（只给缺 `defaultModel` 的 mock 补字段）

**Out of scope**：
- 不要对别的文件跑 `gofmt -w`；`gofmt -l cmd pkg third_party` 在执行前只应该列出 `pkg/tui/notify.go` 这一个文件——如果还列出别的，见 STOP 2。
- 不要改 `tsconfig.json`、不要放宽任何类型检查选项来让它通过。
- 不要改 `pnpm-lock.yaml`、不要升级任何依赖。
- 不要给 e2e（`frontend/e2e/*`）加类型检查，除非 `vue-tsc` 本来就覆盖它们且已通过。

## Git workflow

- **不提交**。改动留在 dev 工作区。

## Steps

### Step 1：修 `pkg/tui/notify.go` 的格式

运行 `gofmt -l cmd pkg third_party`，确认输出**恰好**是 `pkg/tui/notify.go` 一行；然后 `gofmt -w pkg/tui/notify.go`。

**Verify**：
- `test -z "$(gofmt -l cmd pkg third_party)"` → exit 0。
- `git diff --stat -- pkg/tui/notify.go` → 只有这一个文件，且改动是 4 行（`-4 +4` 左右）的纯空白对齐；`git diff -w -- pkg/tui/notify.go` 无输出。
- `go build ./pkg/tui/` → exit 0。

### Step 2：加前端类型检查脚本与 CI 步骤

1. `frontend/package.json` 的 `scripts` 里，在 `"build"` 之后加一行：`"typecheck": "vue-tsc --noEmit",`（保持 JSON 逗号正确、缩进与相邻行一致）。
2. `.github/workflows/ci.yml` 的前端任务，把步骤改成（只加一行 `pnpm typecheck`，放在 `pnpm test` 之前）：

```yaml
      - name: pnpm install, typecheck, test and build
        working-directory: frontend
        run: |
          pnpm install --frozen-lockfile
          pnpm typecheck
          pnpm test
          pnpm run build
```

**Verify**：
- `python3 -c "import json; json.load(open('frontend/package.json'))"` → exit 0。
- `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/ci.yml'))"` → exit 0（若本机没有 PyYAML，改用 `ruby -ryaml -e 'YAML.load_file(".github/workflows/ci.yml")'`，二者都没有就跳过并在报告里写明）。
- `cd frontend && pnpm typecheck` → 此刻**应当失败**，且恰好是 `ProvidersView.test.ts` 的 4 个 `TS2345`（证明门禁有牙齿）。

### Step 3：清掉 4 个类型错误

在 `frontend/src/views/ProvidersView.test.ts` 里：

```
grep -n "providersPage" frontend/src/views/ProvidersView.test.ts
```

对每一处 `mockResolvedValue({ rows: ..., total: ..., page: ..., pageSize: ... })` 里**没有** `defaultModel` 的，补一个 `defaultModel: ''`
（单行写法就在 `pageSize: 10` 后面加 `, defaultModel: ''`；多行写法加一行 `defaultModel: '',`）。
已经带 `defaultModel` 的（如约 `:207` 那处）不动。**不要**改任何断言。

**Verify**：
- `cd frontend && pnpm typecheck` → exit 0，无输出。
- `cd frontend && npx vitest run src/views/ProvidersView.test.ts` → 全绿，用例数与改动前相同。

### Step 4：收尾

1. `test -z "$(gofmt -l cmd pkg third_party)"` → exit 0。
2. `go test -tags fts5 ./pkg/tui/ -count=1` → `ok`（本计划只改了空白，跑一遍确认）。
3. `cd frontend && pnpm typecheck && npx vitest run` → 都 exit 0（全量前端单测，约 800 例）。
4. `git status --short` 里本计划新增的改动只落在 In scope 的 4 个文件。

## Test plan

本计划不新增测试——它新增的是**门禁本身**。门禁的牙齿在 Step 2 的 Verify 里验证过（加了脚本之后在修复前必须红、修复后必须绿）。

## Done criteria

- [ ] `test -z "$(gofmt -l cmd pkg third_party)"` exit 0
- [ ] `git diff -w --stat -- pkg/tui/notify.go` 无输出（只有空白变化）
- [ ] `cd frontend && pnpm typecheck` exit 0
- [ ] `grep -n '"typecheck"' frontend/package.json` 命中 1 处
- [ ] `grep -n 'pnpm typecheck' .github/workflows/ci.yml` 命中 1 处，且位于 `pnpm test` 之前
- [ ] `cd frontend && npx vitest run` exit 0
- [ ] `git status --short` 里新增的改动只落在 In scope 的 4 个文件
- [ ] 没有任何 `git commit` / `git push` / 新分支

## STOP conditions

1. 摘录对不上现状（`notify.go` 已被格式化过、CI 前端任务已有类型检查步骤等）。
2. `gofmt -l cmd pkg third_party` 在 Step 1 之前列出了 `pkg/tui/notify.go` 以外的文件——说明还有别的格式违规，停下报告，由 owner 决定是否一并处理。
3. `pnpm typecheck` 在 Step 3 之后仍有错误，且不是 `defaultModel` 缺失（比如计划 100 引入的新类型问题）——停下报告，不要为了过门禁去放宽 `tsconfig.json`。
4. 本机没有 `pnpm`：用 `npx vue-tsc --noEmit` 代替验证，并在报告里写明；CI 里仍用 `pnpm typecheck`。
5. 一个步骤的验证在合理修复后仍失败两次。

## Maintenance notes

- 以后任何改了 `frontend/src/lib/api.ts` 返回类型的 PR，类型检查会第一时间告诉你哪些 mock 过期了——这正是 Step 2 要买的东西。
- `vue-tsc` 的耗时随项目增长；若 CI 前端任务明显变慢，可以把 `typecheck` 拆成单独 job 并行，而不是删掉它。
- Go 一侧的 `gofmt` 与 `go vet` 门禁已经在 CI（`ci.yml:60-61`），本计划不重复。
