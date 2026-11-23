# Plan 296: 删掉 `modelOverridesFor` 上方「同名多行取第一行」的旧注释

> **Executor instructions**：按步骤执行，每一步都跑完验证命令并确认预期输出再进入下一步。
> 遇到「STOP 条件」里任意一条就停下报告，不要自行发挥。完成后更新 `plans/README.md`
> 里本计划的状态行（除非派发你的评审者说他维护索引）。
>
> **Drift check（开工第一步）**：
> `cd /Users/doudou/workspace/unionj-cloud/forebrain-harness && git status --porcelain -- pkg/process/config_db.go && git diff d715dafd -- pkg/process/config_db.go`
> 若该文件在 `d715dafd` 之后已有改动，把「当前状态」的摘录与活代码比对；对不上即 STOP。
> `plans/` 被 `.gitignore` 忽略，不能用 `git status` 干净推断计划文件没动过。

## Status

- **Priority**: P3
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: docs / tech-debt
- **Planned at**: commit `d715dafd`, 2026-11-11

## Why this matters

本分支把能力表从「按 `provider/model` 建键、同名多行折叠成第一行」改成「按行 id 建键、每行各保留自己的设置」（计划 283 Part E，owner 2026-11-10 裁决）。`pkg/process/config_db.go` 的 `modelOverridesFor` 只替换了函数签名/实现并**追加**了一段新 doc；旧的六行注释作为 context 行被保留下来，于是函数上方同时挂着两段互相矛盾的描述：

- 旧段：「Two rows naming the same provider and model collapse to the first — the one the table lists first, which is the oldest — because the global lookup has one answer per model」
- 新段：「projects every configured row's capabilities, keyed by its row id … same-named rows keep their own settings instead of the first row's winning」

这正是计划 283 实施记录「偏差 5」明确判为缺陷并已用测试改掉的旧行为（`TestInstallModelOverridesPublishesRegistryTablesAndRowCaps` 原断言「同名两行取最旧一行」已改成「各行按行 id 各自生效」）。注释比代码更顽固：下一位给能力表加字段的人若照第一段理解，会误判 `out` / `pairOf` 的键与唯一性规则。审计手册把「actively wrong 的文档」列为比缺失更糟的文档问题，且修复成本为零行为风险。

## Current state

仓库：`/Users/doudou/workspace/unionj-cloud/forebrain-harness`（Go 1.26，CGO，`-tags fts5`）。

- `pkg/process/config_db.go:273-284`（逐字，缺陷）：
  ```go
  // modelOverridesFor builds the override layer from the configured rows: a row
  // that carries any capability becomes one entry under
  // JoinProviderModel(provider, model). Two rows naming the same provider and
  // model collapse to the first — the one the table lists first, which is the
  // oldest — because the global lookup has one answer per model; the client's
  // own output limit still applies per row (AgentLLMProviderConfig.MaxOutputTokens).
  // modelOverridesFor projects every configured row's capabilities, keyed by its
  // row id, and names each row's provider/model so a caller that knows only the
  // pair can still find its row when the pair names exactly one (owner
  // 2026-11-10: same-named rows keep their own settings instead of the first
  // row's winning).
  func modelOverridesFor(rows []appcfg.LLMModelConfig) (map[string]llm.ModelOverride, map[string]string) {
      out := make(map[string]llm.ModelOverride, len(rows))
      pairOf := make(map[string]string, len(rows))
      for _, row := range rows {
          id := strings.TrimSpace(row.ID)
          if id == "" {
              continue
  ```
  精确 diff 佐证：`git diff -U2 origin/dev..HEAD -- pkg/process/config_db.go` 的 `@@ -297,43 +277,35 @@` 显示本分支只替换了 `func` 行并追加新段落，旧段落是 context 行，未删除。

- 实现（**只读，不改**）：`out` 以 `id` 为键；`pairOf` 只在「一个 pair 恰好对应一行」时才给该 pair 指到行 id；`pkg/process/config_db_test.go` 有按行生效的用例。
- 计划依据：`plans/283-plan-approval-hand-off-and-review-again.md` 的「偏差 5」与 Part E（`§797` 起）；`plans/README.md` 2026-11-10 条目也记了 owner「同名多行的能力设置按行生效」的裁决。

仓库约定：
- 注释用英文，函数 doc 以函数名开头、说明「做什么 + 为什么」，不要堆两段历史。
- 不改代码、不改测试、不改任何注释之外的字节。
- `gofmt` 干净；`pkg/architecture` 限制每包 ≤30 个生产文件，不新增文件。

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| 静态核对 | `grep -nE 'collapse to the first|JoinProviderModel\(provider, model\)|MaxOutputTokens' pkg/process/config_db.go` | 无输出 |
| 保留段落核对 | `grep -n 'projects every configured row' pkg/process/config_db.go` | 1 处命中 |
| 包测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/llm ./pkg/state -count=1` | 全部 `ok` |
| 架构规则 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | `ok` |
| 构建 / Vet / 格式 | `CGO_ENABLED=1 go build -tags fts5 ./...` / `CGO_ENABLED=1 go vet -tags fts5 ./...` / `gofmt -l pkg cmd` | exit 0 / 无输出 / 无输出 |

## Scope

**In scope**（只允许改这一个文件，且只允许删注释）：
- `pkg/process/config_db.go` —— `modelOverridesFor` 函数 doc 的第 1–6 行（`:273-278`）。

**Out of scope**（不许动）：
- `modelOverridesFor` 的函数体、`out` / `pairOf` 的键规则、任何返回值。
- `pkg/process/config_db_test.go`、`pkg/llm/*`、`pkg/state/*`。
- 文件里其它注释；`README` / `plans/` 里对历史行为的记录（那些是历史留痕，不是活文档）。
- 任何用户可见文案、日志与模型可见文本。

## Git workflow

- 按 `plans/README.md` 既有约定：**in-tree、零 commit**，改动留在工作区由 owner 审阅。不要自行 `git merge` / `rebase` / `push` / 删 worktree。
- 若 owner 另行要求提交：Conventional Commits 标题，如 `docs(process): drop the stale first-row capability comment`；subject 小写开头、≤100 字符，`git commit -s`（DCO）。

## Steps

### Step 1: 删除旧段落，只留新段落

在 `pkg/process/config_db.go` 中，把 `:273-278` 这六行整段删掉：

```go
// modelOverridesFor builds the override layer from the configured rows: a row
// that carries any capability becomes one entry under
// JoinProviderModel(provider, model). Two rows naming the same provider and
// model collapse to the first — the one the table lists first, which is the
// oldest — because the global lookup has one answer per model; the client's
// own output limit still applies per row (AgentLLMProviderConfig.MaxOutputTokens).
```

保留并**一字不改**紧随其后的新段落（`:279-283`），让它成为 `modelOverridesFor` 唯一的 doc：

```go
// modelOverridesFor projects every configured row's capabilities, keyed by its
// row id, and names each row's provider/model so a caller that knows only the
// pair can still find its row when the pair names exactly one (owner
// 2026-11-10: same-named rows keep their own settings instead of the first
// row's winning).
func modelOverridesFor(rows []appcfg.LLMModelConfig) (map[string]llm.ModelOverride, map[string]string) {
```

不要「润色」新段落、不要合并两段、不要补写迁移史 —— 本次只做删除。

**Verify**：
- `grep -nE 'collapse to the first|JoinProviderModel\(provider, model\)|MaxOutputTokens' pkg/process/config_db.go` → 无输出
- `grep -c '^// modelOverridesFor' pkg/process/config_db.go` = `1`
- `grep -n 'projects every configured row' pkg/process/config_db.go` → 1 处命中
- `git diff -- pkg/process/config_db.go` 里只有 6 行删除、无 `+` 行（用 `git diff --numstat -- pkg/process/config_db.go` 确认为 `0  6` 或等价的纯删除）

### Step 2: 顺手核对同文件是否还有指向「first row」的旧措辞

**Verify**：`grep -nEi 'first row|oldest|one answer per model' pkg/process/config_db.go` → 无输出。若有命中，打开上下文判断：确属同一旧行为的描述就一并删除（仍是纯注释删除）；若是描述**别处**有意行为的句子，保留并在报告里注明，不要盲删。

### Step 3: 收口回归（证明零行为变化）

**Verify**（全部要跑）：
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/llm ./pkg/state -count=1` → 全部 `ok`
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- `CGO_ENABLED=1 go build -tags fts5 ./...` → exit 0
- `CGO_ENABLED=1 go vet -tags fts5 ./...` → 无输出
- `gofmt -l pkg cmd` → 无输出

## Test plan

- **不新增测试**：本计划是纯注释删除，任何代码行为都不变，新增测试只会制造噪音。
- 既有测试作为回归网：`pkg/process/config_db_test.go` 的按行生效用例、`pkg/llm` / `pkg/state` 的相关用例必须继续通过。
- 验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/llm ./pkg/state -count=1` 全 `ok` + `git diff --numstat` 证明只有删除行。

## Done criteria

- [ ] `grep -nE 'collapse to the first|JoinProviderModel\(provider, model\)|MaxOutputTokens' pkg/process/config_db.go` 无输出
- [ ] `grep -c '^// modelOverridesFor' pkg/process/config_db.go` = `1`
- [ ] `grep -nEi 'first row|oldest|one answer per model' pkg/process/config_db.go` 无输出（或报告里逐条说明保留理由）
- [ ] `git diff --numstat -- pkg/process/config_db.go` 的插入列为 `0`（纯删除，零行为变化）
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/llm ./pkg/state -count=1` 全 `ok`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...`、`CGO_ENABLED=1 go vet -tags fts5 ./...`、`gofmt -l pkg cmd` 全绿
- [ ] `git status --porcelain -- pkg/process` 只列出 `config_db.go`
- [ ] `plans/README.md` 中本计划状态行已更新

## STOP conditions

- Drift check 显示 `pkg/process/config_db.go` 在 `d715dafd` 之后被改过，且摘录对不上。
- 打开文件时发现旧段落已被删除或已被改写 —— 说明有人先修了，报告后把本计划状态改为 REJECTED（已由他人修复），不要重复删。
- 新段落与实现也不一致（例如实现又改回按 pair 折叠）—— 那是真缺陷，先报告，不要在注释里替代码圆场。
- `git diff --numstat` 出现插入行（说明你顺手改了代码或润色了注释）—— 回退到纯删除。
- 同一验证连续两次失败。

## Maintenance notes

- 下次改能力表规则时，函数 doc 应随实现**同一提交**更新；本计划的 bug 来自「只换 func 行 + 追加新段落」这种半更新。CR 时看到 doc 里出现两段并列描述，就当 drift 处理。
- `pairOf` 的语义（只有 pair 恰好对应一行时才回退）与 `llm.Lookup` 的目录值回退是计划 283 明确的有意行为，别把「保守回退」误写成「取第一行」。
- 已核实并拒绝的相邻项：`plans/README.md` 里对旧行为的历史记录是留痕，不是活文档，不动。
