# 指令文件预算改为按 rune 计（单文件 12k 与合并总量 120k 都保留），标签显示实留字数

> 状态：**计划待 owner 批准**。批准前零源码改动。
> 取代：`instruction-rules-no-per-file-cap.md`（上一版按「删掉单文件 12k」写；owner 2026-11-09 改判：**12k 保留，只把计数单位改成 rune**）。
> owner 本轮三项裁决（2026-11-09）：
> 1. `/status` 的截断标签**本期改成显示「该文件实留字数」**，不再显示「触发的那道上限」。
> 2. 网关写规则文件时的 12k 告警**跟随真实单文件预算**（`clamp(总量/10, 2048, 12000)`），不再硬编码。
> 3. `/status` 文案里的单位改成 **runes**（`(truncated to 12k runes)`，未载入原因同步为 `-rune rules limit`）。
> 基线：commit `9cc45fd9`。主树有并行会话在改**别的**文件（`forebrain-harness-c6`：`pkg/turn/input_parts.go`、`pkg/turn/approval.go`、`pkg/tui/*`、`pkg/gateway/run_control.go`）——本计划的 in-scope 文件与它无交集；Step 0 只要求 in-scope 路径干净。
> 实施纪律：改动留工作区，不 commit、不 push；批准后另存 `docs/plan/INSTRUCTION_RULES_RUNE_BUDGET_PLAN.md`（`docs/plan` 已 gitignore）。

## 0. 执行前 drift check

```bash
PATHS=(
  pkg/home/workspace_bootstrap.go pkg/home/workspace_bootstrap_test.go
  pkg/assembly/rules.go pkg/assembly/rules_hook.go pkg/assembly/rules_bootstrap.go pkg/assembly/rules_test.go
  pkg/turn/model.go pkg/turn/model_test.go
  pkg/gateway/api_extra.go pkg/gateway/api_extra_test.go
  frontend/src/lib/api.ts frontend/src/components/project/ProjectRules.vue \
  frontend/src/views/RulesView.vue frontend/src/locales/index.ts
  forebrain-harness.github.io/docs/config/agents-and-models.md
  forebrain-harness.github.io/docs/zh/config/agents-and-models.md
)
git rev-parse --short HEAD                      # 仅供参考（并行会话在树上作业）
git status --porcelain -- "${PATHS[@]}"         # 期望空
git diff --stat 9cc45fd9..HEAD -- "${PATHS[@]}" # 期望空
```

期望：两条都为空。非空时按第 2 节的**符号**（不是行号）重新定位；内容不一致 = **STOP**。

## 1. 目标

owner 2026-11-09 裁决：

1. 「必须改成按rune计算」
2. 「必须支持中文、日文、韩文等场景」
3. 「单文件 12k 注入上限保留，不删了。但是计数要改成以rune计算」
4. 标签显示实留字数；网关告警跟随真实预算（本轮两项选择）

即：**两道预算都保留**——单文件 `clamp(总量/10, 2048, 12000)`、合并总量 `max_pre_hook_rules_chars` 默认 120000——**单位从字节改成 rune（Unicode 码点）**，切点永远落在字符边界上；`/status` 里 `(truncated to N chars)` 的 N 改成**该文件实际保留的 rune 数**；web 规则编辑器的「超出预算」告警改成按**真实单文件预算**判断并显示该数字。

今天 `FOREBRAIN.md (truncated to 12k chars)` 的真实含义是「切到 12000 **字节**」：一个汉字 3 字节，中文规则只保留约 4000 字（面板却说 12k 字）；`s[:max]` 还可能切在三字节字符中间，注入正文尾部出现半个字符（JSON 编码时变 U+FFFD）。改完以后 12000 就是 12000 个字，日文、韩文同理。

## 2. 现状与根因（file:line 证据链）

### 2.1 单文件预算：值对、单位错

`pkg/assembly/rules_bootstrap.go:9-18`
```go
func bootstrapMaxPerFile(maxBody int) int {
	n := maxBody / 10
	if n < 2048 { n = 2048 }
	if n > 12000 { n = 12000 }
	return n
}
```
`maxBody` 是合并总量（配置 `max_pre_hook_rules_chars`，默认 120000）。所以「单文件 12k」只在总量 ≥ 120000 时成立；总量 8000 → 单文件 2048；总量 500000 → 仍 12000。

### 2.2 三处按字节切

`pkg/assembly/rules.go:150`
```go
	return TruncateString(body, maxBytes), maxBytes > 0 && len(body) > maxBytes, true
```
`pkg/assembly/rules.go:153-158`
```go
func TruncateString(s string, max int) string {
	if max <= 0 || len(s) <= max { return s }
	return s[:max] + "\n…"
}
```
`pkg/home/workspace_bootstrap.go:361-368`
```go
	raw, err := readAllFn(io.LimitReader(f, int64(maxBytes)+1))
	if err != nil { return BootstrapFile{}, false }
	truncated := len(raw) > maxBytes
	if truncated { raw = append(raw[:maxBytes], []byte("\n…")...) }
```
`pkg/assembly/rules_hook.go:238-258`（合并总量，字节偏移判切点）
```go
	if maxBody > 0 && len(merged) > maxBody {
		start := 0
		for i, part := range parts {
			end := start + len(part)
			src := &build.sources[partSource[i]]
			switch {
			case start >= maxBody:
				src.Loaded = false
				src.NotLoadedReason = "over the " + FormatTokenCount(maxBody) + "-char rules limit"
				src.TruncatedTo = 0
			case end > maxBody:
				src.TruncatedTo = maxBody
			}
			start = end + len("\n\n")
		}
	}
```

### 2.3 标签今天显示的是「上限」不是「实留」，单位还写 chars

`pkg/assembly/rules_hook.go:31-33` `TruncatedTo` 注释写的是「the character limit that cut the file's text」；`pkg/turn/model.go:1176-1198` 直接渲染它，单位是 `chars`。合并总量切点下，被切文件实际只保留 `maxBody − 它前面的字数`，面板却写 `maxBody`（如 `120k chars`）——owner 本轮裁决：显示实留字数，单位改 **runes**。

渲染点（`pkg/turn/model.go:1176-1198`）：
```go
// RenderInstructionsLine lists the instruction files in load order, each with
// what became of it: agent bootstrap files are marked, a file that did not
// make it into the merged body says why, and a cut one says where it was cut.
func RenderInstructionsLine(sources []StatusInstruction) string {
	…
		case src.TruncatedTo > 0:
			name += " (truncated to " + FormatTokens(src.TruncatedTo) + " chars)"
```
未载入原因在 `pkg/assembly/rules_hook.go:250`：`"over the " + FormatTokenCount(maxBody) + "-char rules limit"`。

### 2.4 网关写路径的告警也是硬编码 + 按字节

`pkg/gateway/api_extra.go:7774-7777`
```go
// assemblyBudgetBytes mirrors pkg/home's per-file markdown budget: files
// larger than this still save, but the assembler truncates them, and the
// write answer says so.
const assemblyBudgetBytes = 12_000
```
两处 PUT：`:7858`（agent 规则文件）、`:8024`（项目 `FOREBRAIN.md`）
```go
		if len(body) > assemblyBudgetBytes {
			answer["warning"] = "exceeds_assembly_budget"
		}
```
前端据此显示 `rules.budgetWarning`（`frontend/src/locales/index.ts:687` zh / `:3074` en，文案写「12,000 字节」），消费点是 `frontend/src/components/project/ProjectRules.vue:106,268` 与 `frontend/src/views/RulesView.vue:88,233`。

两个错：单位（字节 vs 字）、阈值（硬编码 12000 vs 真实 `clamp(总量/10,2048,12000)`）。

### 2.5 现场（本机快照，执行时以现场为准）

- 本仓 `FOREBRAIN.md`：`stat -f%z` = 16000 字节，`utf-8` 解码后 **15918 rune**，其中 CJK 仅 4 个（几乎全 ASCII）。15918 > 12000，**改完仍会被切**（只是单位对了），不是「改完就不再截断」。
- `~/.forebrain/forebrain.yaml` 无 `max_pre_hook_rules_chars` → 默认 120000 → 单文件 12000。
- 上一轮评审在基线实测：中文文件在**总量**切点处确实切出非法 UTF-8（含 U+FFFD），单文件 12000 字节对 CJK 只剩 4000 字——根因成立。

### 2.6 可复用的既有实现

`pkg/llm/truncate.go:42-59` 已有 `llm.TruncateRunes(s string, max int) string`（按 rune 切、非法字节转 U+FFFD），且 `pkg/assembly` 已 import `pkg/llm`（`service.go:12`、`hook.go:10` 等）。`pkg/home` 不 import llm（它是被几乎所有包 import 的叶子包），所以 home 侧就地切 rune，不新增包依赖、不动 `pkg/architecture` 的图。

另：`pkg/turn/approval.go` 的 `MaxPlanChars/MaxTaskChars/MaxReviewChars` 与 `turn.Truncate` **本来就是 rune 计数**（`approval.go:1031`、`:1450`、`:1154`），不是同类缺陷，本期不碰。

## 3. 修复设计

### 3.1 单文件预算（home）按 rune 切

`pkg/home/workspace_bootstrap.go`：

- `LoadOptions.MaxBytesPerFile` → `MaxRunesPerFile`；`DefaultMaxBytesPerFile` → `DefaultMaxRunesPerFile`（值仍 12_000）；`BootstrapFile.Truncated` 注释「per-file byte budget」→「per-file rune budget」。
- `readWorkspaceMarkdown` / `readWorkspaceMarkdownPath` 的形参 `maxBytes` → `maxRunes`（签名形状不变）。
- 读取与切点：

```go
	// The budget is in runes and a rune is at most utf8.UTFMax bytes: this
	// prefix holds every rune the budget can keep, plus at least one more when
	// the file is longer — enough to decide the cut without reading it whole.
	raw, err := readAllFn(io.LimitReader(f, int64(maxRunes)*utf8.UTFMax+utf8.UTFMax))
	if err != nil {
		return BootstrapFile{}, false
	}
	runes := []rune(string(raw))
	truncated := len(runes) > maxRunes
	if truncated {
		runes = append(runes[:maxRunes], '\n', '…')
	}
	body := strings.TrimSpace(StripFrontMatter(string(runes)))
	if body == "" {
		return BootstrapFile{}, false
	}
	return BootstrapFile{Name: name, Path: path, Body: body, Truncated: truncated}, true
```

`[]rune` 转换会把非法 UTF-8 字节变成 U+FFFD——注入正文从此总是合法 UTF-8（今天按字节切出的半字符到 JSON 编码时本来也会变 U+FFFD，行为等价且更早暴露）。import 增加 `"unicode/utf8"`（`utf8.UTFMax`）。

### 3.2 项目规则文件（assembly）按 rune 切

`pkg/assembly/rules.go`：

```go
// TruncateString cuts s to at most max runes — Unicode code points, so a
// Chinese, Japanese or Korean character counts once and is never split — and
// marks the cut with an ellipsis. max <= 0 means no limit.
func TruncateString(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	return llm.TruncateRunes(s, max) + "\n…"
}
```
- `readProjectMarkdown(path string, maxRunes int)`：`return TruncateString(body, maxRunes), maxRunes > 0 && utf8.RuneCountInString(body) > maxRunes, true`；函数注释（`rules.go:69-72`）里的「truncated to maxBytes」改成「truncated to maxRunes」。
- `ProjectForebrainFiles(wd string, chain bool, maxRunes int)`（形参改名，签名不变）；`ProjectRuleFile.Truncated` 注释改 rune。
- import 增加 `"unicode/utf8"` 与 `"github.com/forebrain-harness/forebrain-harness/pkg/llm"`。

### 3.3 合并总量（hook）按 rune 切，并记录「实留字数」

`pkg/assembly/rules_bootstrap.go`：`bootstrapMaxPerFile` → 导出 `PerFileRulesBudget`（网关要用同一份算法）。

`pkg/assembly/rules_hook.go`：建 parts 时同时记录「文件自身正文在该 part 内的 rune 偏移」，切点循环按实留记账：

```go
	var parts []string
	var partSource []int // the source index each part came from
	var partOffset []int // runes of synthesized header before the file's own text
	…
			header := "## " + f.Name + "\n\n"
			partSource = append(partSource, len(build.sources))
			partOffset = append(partOffset, utf8.RuneCountInString(header))
			parts = append(parts, header+f.Body)
	…
		partSource = append(partSource, len(build.sources))
		partOffset = append(partOffset, 0)
		parts = append(parts, f.Body)
	…
	merged := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if maxBody > 0 && utf8.RuneCountInString(merged) > maxBody {
		// The body limit cuts the merged text, in runes. A file the cut lands
		// inside keeps only the runes before it, and that kept count — not the
		// limit — is what the report shows.
		start := 0
		for i, part := range parts {
			n := utf8.RuneCountInString(part)
			own := n - partOffset[i] // the file's own runes, header excluded
			kept := min(max(maxBody-(start+partOffset[i]), 0), own)
			src := &build.sources[partSource[i]]
			switch {
			case kept == 0:
				src.Loaded = false
				src.NotLoadedReason = "over the " + FormatTokenCount(maxBody) + "-rune rules limit"
				src.TruncatedTo = 0
			case kept < own:
				src.TruncatedTo = kept
			}
			start += n + 2 // the "\n\n" join between parts, two runes
		}
	}
```
- `home.LoadOptions{MaxBytesPerFile: perFile}` → `MaxRunesPerFile: perFile`；`perFile := PerFileRulesBudget(maxBody)`。
- `RuleSource.TruncatedTo` 注释改成「the runes of the file's own text that made it into the merged body; zero when the whole file is in」（语义改为实留）。
- import 增加 `"unicode/utf8"`。

**渲染与文案**（owner 第 3 条裁决）：

- `pkg/turn/model.go:1193`：`" chars)"` → `" runes)"`，即 `(truncated to 12k runes)`。
- `pkg/turn/model.go:1176-1178` 注释：`a cut one says where it was cut` → `a cut one says how much of it arrived`。
- 未载入原因（`rules_hook.go:250`）：`-char rules limit` → `-rune rules limit`（同一条面板行，词汇统一）。
- `pkg/turn/model_test.go:273-287` 期望串 `(truncated to 120k chars)` → `(truncated to 120k runes)`。

单文件切点那条路径不变：`src.TruncatedTo = perFile`，而 perFile 正是被保留的 rune 数，语义与新的「实留」一致。

### 3.4 网关告警跟随真实预算

`pkg/gateway/api_extra.go`：

```go
// ruleFileBudgetRunes is the per-file instruction budget the assembler applies
// to this install: the configured total divided by ten, clamped to
// [2048, 12000] — the same rule pkg/assembly builds the rules body with, so
// the editor's warning and the assembler's cut can never disagree.
func (s *Server) ruleFileBudgetRunes(ctx context.Context) int {
	total := 120_000
	if s != nil && s.Env != nil {
		if cfg, err := s.Env.ComposedPersistedConfig(ctx); err == nil {
			total = cfg.Agents.Defaults.ContextInject.MaxPreHookRulesCharsOr(120_000)
		}
	}
	return assembly.PerFileRulesBudget(total)
}
```
删掉 `assemblyBudgetBytes` 常量；两处 PUT 改成：

```go
		budget := s.ruleFileBudgetRunes(r.Context())
		answer := map[string]any{"name": name, "bytes": len(body), "budget": budget}
		if utf8.RuneCountInString(string(body)) > budget {
			answer["warning"] = "exceeds_assembly_budget"
		}
```
（项目 PUT 的 answer 同样加 `"budget"`，键名不带 name。）`pkg/gateway/api_extra.go` 的 import 增加 `"unicode/utf8"`。

前端把数字显示出来（vue-i18n 插值，仓库已有 `{n}`/`{count}` 用法）：

- `frontend/src/lib/api.ts:2134-2137` `RuleFileSaveResponse` 加 **必填** `budget: number`（网关总会带；前后端同包发布，不做版本兜底）。
- `frontend/src/components/project/ProjectRules.vue`：新增 `const warningBudget = ref(0)`；`:106` → `{{ t('rules.budgetWarning', { n: warningBudget }) }}`；`:268` 处 `warningBudget.value = res.budget`。
- `frontend/src/views/RulesView.vue`：同样两处（`:88` 模板、`:233` 赋值）。
- `frontend/src/locales/index.ts:687` zh → `'文件超过单文件 {n} 字的组装预算，超出部分不会进入上下文。'`
- `:3074` en → `'This file exceeds the {n}-character per-file assembly budget; the excess will not reach the context.'`

### 3.5 文档

- `forebrain-harness.github.io/docs/config/agents-and-models.md:212` → `… Maximum pre-hook rules characters (Unicode code points).`
- `.../zh/...:203` → `… Pre-hook 规则的最大字符数（Unicode 码点）。`
- `frontend/src/lib/settingsSchema.ts:251`（`unit: '字'`）与 `forebrain.yaml:106`（「cap rendered rule chars」）本来就写「字/字符」，**不改**。

### 3.6 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 性质 |
|---|---|---|---|
| 中文/日文/韩文文件 10000 字（30000 字节） | 切到 12000 字节 = 4000 字 | 完整保留（10000 < 12000 字） | 刻意修订 |
| CJK 文件 13000 字（39000 字节） | 切到 4000 字，切点可能劈开字符 | 切到 12000 字，切点落在字符边界 | 刻意修订 |
| ASCII 文件 13000 字节 | 切到 12000 | 切到 12000 字（等价） | 不变 |
| 合计超总量 | 按字节计，切点可能劈开字符 | 按 rune 计，切点落在字符边界 | 刻意修订 |
| 单文件被切时的标签 | `truncated to 12k chars`（其实是字节） | `truncated to 12k runes`（实留 12000 字） | 刻意修订 |
| **合计**被切时的标签 | `truncated to 120k chars`（是上限，不是该文件实留） | `truncated to 63k runes`（该文件实留字数） | 刻意修订（owner 裁决） |
| 未载入原因 | `over the 120k-char rules limit` | `over the 120k-rune rules limit` | 随契约 |
| 网关「超出预算」告警 | 按字节、阈值硬编码 12000 | 按 rune、阈值 = `clamp(总量/10,2048,12000)`，文案带该数字 | 刻意修订（owner 裁决） |
| 配置字段/设置页 | `int`，0 = 默认 120000 | **完全不变** | 不变 |

### 3.7 非功能面（资源 / 上下文）

- **内存**：bootstrap 读取上限从 `maxBytes+1` 变为 `maxRunes*4+4` 字节（12k 预算 → 48KB），仍是有界读；`[]rune` 切片最多 `maxRunes+1` 个 rune。项目 `FOREBRAIN.md` 与今天一样整份读（`os.ReadFile`）。
- **时间**：每份文件多一次 rune 扫描（O(文件长度)），发生在 `buildRules` 的 per-key 缓存 miss 时（`rules_hook.go:191-195`），不进热路径。
- **上下文放大（owner 已知取舍）**：同一数值从字节变 rune 后，纯 CJK 正文的最大字节数最多 ×3（4 字节字符 ×4），每轮注入的 system prompt 词元对 CJK 约 ×3。默认总量 120000 意味着最坏 360KB 规则正文。真实规则文件通常几 KB（本仓 16KB / 15918 rune），只有极端文件会吃到这个上限；数值本身按 owner「12k 保留」不动。
- **合法性**：切点由 rune 决定，切出的正文恒为合法 UTF-8；`/status` 与请求体不会再出现半个字符。
- **缓存键**仍含 maxBody（`rules_hook.go:190`）。

## 4. Scope

**In scope**

- `pkg/home/workspace_bootstrap.go`、`pkg/home/workspace_bootstrap_test.go`
- `pkg/assembly/rules.go`、`pkg/assembly/rules_hook.go`、`pkg/assembly/rules_bootstrap.go`、`pkg/assembly/rules_test.go`
- `pkg/turn/model.go`（只改 `RenderInstructionsLine` 的单位串与一处注释）、`pkg/turn/model_test.go`（只改期望串）
- `pkg/gateway/api_extra.go`（常量删除、两处 PUT、新增 `ruleFileBudgetRunes`）、`pkg/gateway/api_extra_test.go`（`TestAgentRuleFileBudgetWarning` + 新增预算测试）
- `frontend/src/lib/api.ts`（`RuleFileSaveResponse` 加 `budget?: number`）、`frontend/src/components/project/ProjectRules.vue`、`frontend/src/views/RulesView.vue`、`frontend/src/locales/index.ts`
- `forebrain-harness.github.io/docs/config/agents-and-models.md`、`.../zh/config/agents-and-models.md`

**Out of scope（明确不碰）**

- `pkg/config`、设置页 schema：`max_pre_hook_rules_chars` 仍是 `int`，`0` 仍是「用调用方默认 120000」，**不引入 `*int`、不引入 0 = 不限**。
- `pkg/turn/model.go` 的其余部分（`StatusFacts` 的其它行、`FormatTokens`）：不动；`StatusInstruction = assembly.RuleSource`（`model.go:667`）的类型不改。
- `pkg/turn/approval.go`（`MaxPlanChars/MaxTaskChars/MaxReviewChars`、`Truncate`）：本来就是 rune 计数；并行会话正在改这个文件的其它部分。
- `pkg/gateway` 的 `rulesBodyLimit = 64<<10`（PUT 请求体上限）：写路径 HTTP 护栏，不是注入预算。
- `pkg/llm/truncate.go`：复用，不改。
- `pkg/architecture/testdata/graph.json`：不新增包、不新增跨包依赖（assembly→llm、gateway→assembly 均已存在），图不变。
- `forebrain.yaml`：注释已写「chars」，示例值 8000 只是示例，无单位需要改。
- 其他按字节的预算（工具输出、skill/memory 读盘；`pkg/safety` 的 `input.max_runes` 已是 rune）。

## 5. Steps

### Step 1：home 按 rune 切单文件

按 §3.1 改 `pkg/home/workspace_bootstrap.go`（字段改名 + 读取上限 + rune 切点 + 注释），同步调用点 `pkg/assembly/rules_hook.go:205`。

**Verify**：
```bash
CGO_ENABLED=1 go build ./...
grep -rn "MaxBytesPerFile\|DefaultMaxBytesPerFile" pkg/ | grep -v _test   # 期望无输出
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/home -count=1
```
→ build exit 0；grep 空；home 包 ok。

### Step 2：assembly 的 TruncateString / 合并总量 / 实留记账 + 面板单位改 runes

按 §3.2、§3.3 改 `pkg/assembly/rules.go`、`rules_hook.go`、`rules_bootstrap.go`（导出 `PerFileRulesBudget`），并按 §3.3 末段改 `pkg/turn/model.go`（`" runes)"` + 注释）与 `pkg/turn/model_test.go`（期望串）。

**Verify**：
```bash
CGO_ENABLED=1 go build ./...
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/assembly ./pkg/turn -count=1
```
→ ok（`TestInstructionSourcesRecordWhereTheBodyLimitCuts` 的数值按 §6.2 更新；`pkg/turn` 的渲染期望已改）。

### Step 3：网关真实预算 + 前端 + 文档

按 §3.4、§3.5 改 `pkg/gateway/api_extra.go`、`api_extra_test.go`、`frontend/src/lib/api.ts`、两个 Vue、locales、两份 docs。

**Verify**：
```bash
CGO_ENABLED=1 go build ./...
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'TestAgentRuleFileBudgetWarning|TestRuleFileBudget' -count=1
cd frontend && pnpm typecheck && cd ..      # node_modules 已存在；不跑 build
```
→ Go ok；`vue-tsc --noEmit` exit 0（`pnpm` 不可用时用 `corepack pnpm typecheck`）。

### Step 4：补测试（§6）

**Verify**：
```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/home ./pkg/assembly ./pkg/gateway ./pkg/turn -count=1
go vet ./...
```
→ 全绿。

### Step 5：真机验收（§7）

## 6. 测试计划

### 6.1 `pkg/home/workspace_bootstrap_test.go`（新增，import 加 `unicode/utf8`）

```go
// TestReadWorkspaceMarkdownCountsRunes pins the owner contract: the per-file
// budget counts Unicode code points, so a Chinese/Japanese/Korean file keeps
// the budget's characters and the cut never splits one.
func TestReadWorkspaceMarkdownCountsRunes(t *testing.T) {
	ws := t.TempDir()
	path := filepath.Join(ws, "AGENTS.md")
	// 13000 CJK runes = 39000 bytes: over the budget under either unit, but the
	// kept prefix must be 12000 runes (36000 bytes), not 12000 bytes (4000 runes).
	if err := os.WriteFile(path, []byte(strings.Repeat("中", 13000)), 0o644); err != nil {
		t.Fatal(err)
	}
	item, ok := readWorkspaceMarkdownPath(ws, "AGENTS.md", path, 12_000, false)
	if !ok || !item.Truncated {
		t.Fatalf("item = %+v ok=%v, want truncated", item, ok)
	}
	if !utf8.ValidString(item.Body) || strings.ContainsRune(item.Body, '\uFFFD') {
		t.Fatalf("body is not clean UTF-8: %q", item.Body)
	}
	body := strings.TrimSuffix(item.Body, "\n…")
	if got := utf8.RuneCountInString(body); got != 12_000 {
		t.Fatalf("kept %d runes, want 12000", got)
	}
	if got := len(body); got != 36_000 {
		t.Fatalf("kept %d bytes, want 36000 (three bytes per rune)", got)
	}

	exact := filepath.Join(ws, "SOUL.md")
	if err := os.WriteFile(exact, []byte(strings.Repeat("中", 12_000)), 0o644); err != nil {
		t.Fatal(err)
	}
	if item, ok := readWorkspaceMarkdownPath(ws, "SOUL.md", exact, 12_000, false); !ok || item.Truncated {
		t.Fatalf("exact fit = %+v ok=%v, want untouched", item, ok)
	}
}
```

### 6.2 `pkg/assembly/rules_test.go`

1. `TestInstructionSourcesRecordWhereTheBodyLimitCuts`（`:167-208`）按**实留语义**重写数值（原测试在 rune 下仍会通过，但断言的是旧语义）：
   - 夹具不变（AGENTS 3000×`a`、SOUL `"SOUL"`、FOREBRAIN `"PROJECT RULES"`），`MaxPreHookRulesChars` **2070 → 2080**。
   - rune 记账：AGENTS part = 14 + (2048 + "\n…" = 2050) = 2064；SOUL part 起于 2066，头部 `"## SOUL.md\n\n"` 占 12 rune，正文 `"SOUL"` 在 [2078, 2082)；2080 落在正文内部。
   - 断言：`sources[0].TruncatedTo == 2048`（单文件预算，实留）、`sources[1].TruncatedTo == 2`（实留两个字符）、`sources[2].Loaded == false` 且原因含 `rules limit`；`merged` 不含 `PROJECT RULES`，且以 `"SO\n…"` 结尾。
   - 注释改为「the per-file budget and the body limit are both recorded as the runes the file actually kept」。
2. 新增「单文件预算按 rune」：

```go
// TestRulesPerFileBudgetCountsRunes pins that a CJK file under 12000 runes is
// injected whole even though it is far over 12000 bytes.
func TestRulesPerFileBudgetCountsRunes(t *testing.T) {
	home := t.TempDir()
	wsRoot := t.TempDir()
	proj := t.TempDir()
	if err := os.Mkdir(filepath.Join(proj, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("中文规则。", 2000) // 10000 runes, 30000 bytes
	if err := os.WriteFile(filepath.Join(proj, "FOREBRAIN.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := safety.MarkTrusted(home, safety.Project{Root: proj, VersionControlled: true}); err != nil {
		t.Fatal(err)
	}
	p := NewPreHook(nil, home, wsRoot)
	t.Chdir(proj)
	if merged := p.loadRules(proj, hook.HookContext{}, true); merged != body {
		t.Fatalf("merged body drifted: %d runes, want the whole 10000-rune file", utf8.RuneCountInString(merged))
	}
	sources := p.InstructionSources()
	if len(sources) != 1 || sources[0].TruncatedTo != 0 || !sources[0].Loaded {
		t.Fatalf("sources = %+v, want FOREBRAIN.md loaded whole", sources)
	}
}
```
3. 新增「合并总量按 rune」：

```go
// TestRulesBodyLimitCountsRunes pins that the merged body's limit is measured
// in Unicode code points and the cut never splits a character.
func TestRulesBodyLimitCountsRunes(t *testing.T) {
	home := t.TempDir()
	wsRoot := t.TempDir()
	proj := t.TempDir()
	if err := os.Mkdir(filepath.Join(proj, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "FOREBRAIN.md"), []byte(strings.Repeat("中文规则。", 2000)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := safety.MarkTrusted(home, safety.Project{Root: proj, VersionControlled: true}); err != nil {
		t.Fatal(err)
	}
	cfg := &appcfg.Root{}
	cfg.Agents.Defaults.ContextInject.MaxPreHookRulesChars = 100
	p := NewPreHook(cfg, home, wsRoot)
	t.Chdir(proj)
	merged := p.loadRules(proj, hook.HookContext{}, true)
	if !utf8.ValidString(merged) || strings.ContainsRune(merged, '\uFFFD') {
		t.Fatalf("merged body is not clean UTF-8")
	}
	cut := strings.TrimSuffix(merged, "\n…")
	if got := utf8.RuneCountInString(cut); got != 100 {
		t.Fatalf("kept %d runes, want 100", got)
	}
	if got := len(cut); got != 300 {
		t.Fatalf("kept %d bytes, want 300", got)
	}
	if sources := p.InstructionSources(); len(sources) != 1 || sources[0].TruncatedTo != 100 {
		t.Fatalf("sources = %+v, want FOREBRAIN.md keeping 100 runes", sources)
	}
}
```
4. `TestTruncateString`（`:253-258`）补三种：

```go
	if got := TruncateString("abcdef", 0); got != "abcdef" { t.Fatalf("0 must mean no limit, got %q", got) }
	if got := TruncateString("中文规则", 2); got != "中文\n…" { t.Fatalf("rune cut = %q", got) }
	if got := TruncateString("中文规则", 4); got != "中文规则" { t.Fatalf("exact fit = %q", got) }
```
5. import 加 `"unicode/utf8"`。

### 6.3 `pkg/gateway/api_extra_test.go`

`TestAgentRuleFileBudgetWarning`（`:3619`）改为按 rune，并新增真实预算用例：

```go
func TestAgentRuleFileBudgetWarning(t *testing.T) {
	s, _, _ := rulesServer(t)
	// 12001 CJK runes = 36003 bytes, well under the 64KB request cap: the
	// warning is about the assembler's rune budget, not the body size.
	big := bytes.NewReader([]byte(strings.Repeat("中", 12001)))
	rr := httptest.NewRecorder()
	req := withParamName(httptest.NewRequest(http.MethodPut, "/api/rules/agent/SOUL.md", big), "SOUL.md")
	s.handleAgentRuleFile(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "exceeds_assembly_budget")
	require.Contains(t, rr.Body.String(), `"budget":12000`)

	atLimit := bytes.NewReader([]byte(strings.Repeat("中", 12000)))
	rr = httptest.NewRecorder()
	req = withParamName(httptest.NewRequest(http.MethodPut, "/api/rules/agent/SOUL.md", atLimit), "SOUL.md")
	s.handleAgentRuleFile(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NotContains(t, rr.Body.String(), "warning")
}

// TestRuleFileBudgetFollowsTheConfiguredTotal pins that the editor's warning
// uses the same clamp the assembler applies: a total of 8000 leaves a per-file
// budget of 2048, not 12000.
func TestRuleFileBudgetFollowsTheConfiguredTotal(t *testing.T) {
	s, _, _ := rulesServer(t)
	ctx := context.Background()
	require.Equal(t, 12_000, s.ruleFileBudgetRunes(ctx))

	cfg, err := s.Env.ComposedPersistedConfig(ctx)
	require.NoError(t, err)
	cfg.Agents.Defaults.ContextInject.MaxPreHookRulesChars = 8000
	require.NoError(t, s.Env.WriteRootSections(ctx, []string{"agents"}, cfg))
	require.Equal(t, 2048, s.ruleFileBudgetRunes(ctx))
}

// The project PUT answers the same budget, so the project editor's warning
// cannot drift from the agent editor's.
func TestProjectRuleFileBudgetFollowsTheConfiguredTotal(t *testing.T) {
	s, _, _ := rulesServer(t)
	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "docs"), 0o755))
	p, err := s.Projects.Create(ctx, state.CreateProjectInput{Name: "p", Root: root})
	require.NoError(t, err)

	cfg, err := s.Env.ComposedPersistedConfig(ctx)
	require.NoError(t, err)
	cfg.Agents.Defaults.ContextInject.MaxPreHookRulesChars = 8000
	require.NoError(t, s.Env.WriteRootSections(ctx, []string{"agents"}, cfg))

	rr := httptest.NewRecorder()
	req := withNamedParam(
		httptest.NewRequest(http.MethodPut, "/api/rules/project/"+p.ID+"/file?dir=docs",
			bytes.NewReader([]byte(strings.Repeat("中", 2049)))),
		"projectId", p.ID)
	s.handleProjectRuleFile(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), "exceeds_assembly_budget")
	require.Contains(t, rr.Body.String(), `"budget":2048`)
}
```

### 6.4 `pkg/turn/model_test.go`

`RenderInstructionsLine` 的期望串 `docs/FOREBRAIN.md (truncated to 120k chars)` → `docs/FOREBRAIN.md (truncated to 120k runes)`（`model_test.go:273-287`）。渲染逻辑本身不改。

## 7. 真机验收（tmux + fake provider，不烧真配额）

驱动脚本自建隔离 home 与项目目录：`WORK="${FOREBRAIN_RUN_WORK:-...}"`、`PROJ="$WORK/proj"`（`driver.sh:11-17`），TUI 固定以 `-c "$PROJ"` 启动（`driver.sh:133`），并会自己构建带 `-tags fts5` 的二进制（`driver.sh:36`）。**夹具必须写进 `$WORK/proj`，靠 `FOREBRAIN_RUN_WORK` 指定 `WORK`**——写到别处 TUI 根本看不到。

```bash
export FOREBRAIN_RUN_WORK="$(mktemp -d)"
mkdir -p "$FOREBRAIN_RUN_WORK/proj/.git"
python3 - "$FOREBRAIN_RUN_WORK/proj/FOREBRAIN.md" <<'PY'
import sys
# 16000 个同一汉字 = 16000 rune、48000 字节：按字节只剩 4000 字，按 rune 应保留 12000 字。
open(sys.argv[1], 'w').write("中" * 16000)
PY
D=.claude/skills/run-forebrain/driver.sh
FAKE_DUMP_DIR="$FOREBRAIN_RUN_WORK/dump" $D start reply 'ok'   # 构建 + 起 TUI + 答过 trust 页
$D submit 'hi'          # 触发一轮真实请求（fake provider 回 'ok'）
$D wait 'ok' 20
$D submit '/status'
$D screen | grep -n 'Instructions'
```

场景 A（回归，**不能证明修复**）：`Instructions:` 行出现 `FOREBRAIN.md (truncated to 12k runes)`。坏版本上这一行是 `(truncated to 12k chars)`，所以它只能证明面板文案改了、TUI 不崩；新旧行为的分水岭在场景 B。

场景 B（请求体，**唯一能区分新旧**的验收）：请求体是 `json.Marshal` 的产物，`<` 会被转义成 `\u003c`，且规则正文挂在 user 消息里而不是 system —— 必须**先解析 JSON、再按 marker 找消息**，不能在原始文本上 `split('<forebrain_rules>')`（那样必然 IndexError，或者拿到空串空跑通过）。

```bash
python3 - "$FOREBRAIN_RUN_WORK/dump" <<'PY'
import json, sys, pathlib

req = json.loads(sorted(pathlib.Path(sys.argv[1]).glob('request-*.json'))[0].read_text())
def texts(node):
    if isinstance(node, str):
        yield node
    elif isinstance(node, dict):
        for v in node.values():
            yield from texts(v)
    elif isinstance(node, list):
        for v in node:
            yield from texts(v)

blocks = [t for t in texts(req) if '<forebrain_rules>' in t]
assert blocks, 'no message carries the rules block'
rules = blocks[0].split('<forebrain_rules>')[1].split('</forebrain_rules>')[0]
print('kept chars:', rules.count('中'))                    # 期望 12000
print('replacement chars:', rules.count('\ufffd'))         # 期望 0（解析后不会有转义形态）
print('has ellipsis:', '…' in rules)                       # 期望 True
PY
```
期望：`kept chars: 12000`、`replacement chars: 0`、`has ellipsis: True`。坏版本上这里是 `kept chars: 4000`。

场景 C（回归）：把文件换成 500 个汉字（1500 字节）→ `/status` 无 `truncated` 标签，请求体 `rules.count('中') == 500`。

收尾：`$D stop`；`rm -rf "$FOREBRAIN_RUN_WORK"`。

**降级**：若驱动起不来（端口/依赖），记录原因，改用手工 tmux + 临时 `FOREBRAIN_HOME` 只跑场景 A/C，场景 B 由 §6 单测承担，并在验收记录写明。

## 8. Done criteria（机器可查）

- [ ] `CGO_ENABLED=1 go build ./...` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/home ./pkg/assembly ./pkg/gateway ./pkg/turn -count=1` 全绿
- [ ] `go vet ./...` exit 0
- [ ] `grep -rn "MaxBytesPerFile\|DefaultMaxBytesPerFile\|assemblyBudgetBytes\|bootstrapMaxPerFile" pkg/` 无匹配
- [ ] `grep -rn "len(merged) > maxBody\|len(part)" pkg/assembly/rules_hook.go` 无匹配
- [ ] `grep -rn "s\[:max\]\|raw\[:maxBytes\]" pkg/assembly pkg/home` 无匹配
- [ ] `grep -rn "truncated to .* chars\|-char rules limit" pkg/` 无匹配（面板与未载入原因都已改成 runes）
- [ ] 新测试 `TestReadWorkspaceMarkdownCountsRunes`、`TestRulesPerFileBudgetCountsRunes`、`TestRulesBodyLimitCountsRunes`、`TestRuleFileBudgetFollowsTheConfiguredTotal` 存在且通过
- [ ] 场景 A（回归）面板 `(truncated to 12k runes)`；场景 B（分水岭）`kept chars: 12000` 且 `replacement chars: 0`；场景 C 无标签且 500 字
- [ ] `cd frontend && pnpm typecheck` exit 0（`vue-tsc --noEmit`）
- [ ] `git status --porcelain` 的改动全部落在 §4 In scope
- [ ] 无 commit（`git status --porcelain` 里仍是未提交的工作区改动）

## 9. STOP conditions

- Step 0 的 in-scope `git status`/diff 非空且内容与 §2 摘录不一致（不是行号漂移）。
- `pkg/assembly` 并未 import `pkg/llm`（`grep -rn "pkg/llm" pkg/assembly/*.go` 为空）：复用 `llm.TruncateRunes` 会新增包边，停下来报告，改用 assembly 内局部 `runePrefix`。
- `s.Env.ComposedPersistedConfig` 在 `rulesServer` 的夹具里不可用（返回 error）：说明网关测试拿不到组合配置，停下来报告，不要改测试夹具绕过（那会让告警永远用默认值）。
- `PerFileRulesBudget` 导出后出现第三个调用方（`grep -rn "PerFileRulesBudget" pkg/` 超过 assembly 自身 + gateway）：说明波及面超出计划。
- 任一测试连续两次修不好。
- 需要改 §4 Out of scope 的文件才能继续（尤其 `pkg/config`、设置页、`pkg/turn/approval.go`——后者有并行会话在改）。

## 10. Maintenance notes

- **单位契约**：`max_pre_hook_rules_chars`、`PerFileRulesBudget`、`RuleSource.TruncatedTo`、网关 `ruleFileBudgetRunes` 现在都是 **rune（Unicode 码点）**。任何新增的「按字符」预算复用 `llm.TruncateRunes`（assembly）或 home 的 `[]rune` 切法，不要写 `s[:max]`。
- **标签语义（本轮改过）**：`TruncatedTo` = 该文件**实际保留**的 rune 数；0 = 整份都在（或整份都没进，此时 `Loaded=false` 且带原因）。面板文案的单位是 **runes**（`(truncated to 12k runes)`、`over the 120k-rune rules limit`），以后改 `/status` 不要再把它当「上限」或「字节」用。
- **预算关系**：单文件预算 = `clamp(总量/10, 2048, 12000)`，只在总量 ≥ 120000 时等于 12000。网关、组装、文档三处必须用同一份算法（`assembly.PerFileRulesBudget`）。
- **上下文放大**：同一数值从字节变 rune，纯 CJK 正文最坏 ×3 字节、词元约 ×3。若将来要压回来，正确做法是调小 `max_pre_hook_rules_chars`，而不是把单位改回字节。
- **评审要看**：`readWorkspaceMarkdownPath` 的读取上限 `maxRunes*4+4`（4 = `utf8.UTFMax`）、`[]rune` 对非法 UTF-8 的 U+FFFD 归一、切点循环里 `+2` 是 `\n\n` 两个 rune、`partOffset` 把合成头排除在「实留」之外、网关两处 PUT 都带了 `budget` 字段。
- **已推迟**：`rulesBodyLimit`（PUT 请求体 64KB）是写路径护栏；`max_pre_hook_rules_chars` 的「0 = 不限」不在本期。

## Implementation Status

基线 `9cc45fd9`，Step 0 drift check 通过（in-scope 路径 `git status` 与 `git diff --stat 9cc45fd9..HEAD` 均空，HEAD 即基线）。全部步骤按 §5 顺序实施，改动留在工作区，无 commit / push。批准后已另存 `docs/plan/INSTRUCTION_RULES_RUNE_BUDGET_PLAN.md`（docs/plan 已 gitignore）。

### Step 1 home 按 rune 切单文件 — done

- `pkg/home/workspace_bootstrap.go`：`MaxBytesPerFile`→`MaxRunesPerFile`、`DefaultMaxBytesPerFile`→`DefaultMaxRunesPerFile`（值 12_000）、`BootstrapFile.Truncated` 注释改 rune、`readWorkspaceMarkdown`/`readWorkspaceMarkdownPath` 形参改 `maxRunes`、读取上限 `int64(maxRunes)*utf8.UTFMax+utf8.UTFMax`、`[]rune(string(raw))` 切点 + `'\n','…'` 追加、import `unicode/utf8`。调用点 `pkg/assembly/rules_hook.go` 同步改字段名。
- Verify：`go build ./...` exit 0；`grep -rn "MaxBytesPerFile\|DefaultMaxBytesPerFile" pkg/ | grep -v _test` 空；`go test -tags fts5 ./pkg/home` ok。

### Step 2 assembly 切点/实留记账 + 面板单位 — done

- `pkg/assembly/rules.go`：`TruncateString` 按 rune（`llm.TruncateRunes` + `utf8.RuneCountInString` 判长）、`readProjectMarkdown`/`ProjectForebrainFiles` 形参 `maxRunes`、`ProjectRuleFile.Truncated` 注释、import `unicode/utf8` + `pkg/llm`。
- `pkg/assembly/rules_bootstrap.go`：`bootstrapMaxPerFile` → 导出 `PerFileRulesBudget`（算法不变）+ 文档注释。
- `pkg/assembly/rules_hook.go`：新增 `partOffset`（合成头 rune 数）、合并总量按 rune 判切、切点循环按「实留」记账（`kept = clamp(maxBody-(start+partOffset),0,own)`，`kept==0` 未载入、`0<kept<own` 记实留、`kept==own` 保持 0）、未载入原因 `-rune rules limit`、`RuleSource.TruncatedTo` 语义注释、import `unicode/utf8`。
- `pkg/turn/model.go`：`" chars)"`→`" runes)"` + 注释「says how much of it arrived」；`pkg/turn/model_test.go` 期望串改 runes。
- Verify：`go build ./...` exit 0；`go test -tags fts5 ./pkg/assembly ./pkg/turn` ok（`TestInstructionSourcesRecordWhereTheBodyLimitCuts` 按 §6.2 数值重写：总量 2080，AGENTS 2048 / SOUL 实留 2 / FOREBRAIN 未载入、merged 以 `SO\n…` 结尾，实测通过）。

### Step 3 网关真实预算 + 前端 + 文档 — done

- `pkg/gateway/api_extra.go`：删 `assemblyBudgetBytes`，新增 `(*Server).ruleFileBudgetRunes(ctx)`（`ComposedPersistedConfig` → `MaxPreHookRulesCharsOr(120_000)` → `assembly.PerFileRulesBudget`），两处 PUT 都答 `budget` 并按 `utf8.RuneCountInString` 判 `exceeds_assembly_budget`，import `unicode/utf8`。
- 前端：`api.ts` `RuleFileSaveResponse.budget: number`（必填，按 §3.4；§4 行内写的 `budget?` 与 §3.4 冲突，取 §3.4 的必填——否则 `warningBudget.value = res.budget` 在 `vue-tsc` 下类型不通过）；`ProjectRules.vue`/`RulesView.vue` 新增 `warningBudget` ref、模板 `t('rules.budgetWarning', { n: warningBudget })`、保存处赋值；locales zh/en 文案改插值。
- 文档：`forebrain-harness.github.io/docs/config/agents-and-models.md` 与 zh 版各一行补「Unicode code points / Unicode 码点」。
- Verify：`go test -tags fts5 ./pkg/gateway -run 'TestAgentRuleFileBudgetWarning|TestRuleFileBudget|TestProjectRuleFileBudget' -v` 三个用例全 PASS；`cd frontend && pnpm typecheck` exit 0。

### Step 4 补测试 — done

- 新增 `pkg/home` `TestReadWorkspaceMarkdownCountsRunes`（13000 汉字→实留 12000 rune / 36000 字节、无 U+FFFD；12000 汉字整份保留）。
- 新增 `pkg/assembly` `TestRulesPerFileBudgetCountsRunes`（10000 rune 的 CJK `FOREBRAIN.md` 整份注入）、`TestRulesBodyLimitCountsRunes`（总量 100 → 实留 100 rune / 300 字节、`TruncatedTo==100`、无 U+FFFD）；`TestTruncateString` 补 0/精确/中文三例；import `unicode/utf8`。
- 网关三个预算用例（§6.3）在 Step 3 落地。
- Verify：`go test -tags fts5 ./pkg/home ./pkg/assembly ./pkg/gateway ./pkg/turn -count=1` 四包全 ok；`go vet ./...` exit 0。

### Step 5 真机验收（tmux + fake provider，不烧真配额）— done，场景 A/B/C 全过

隔离 `FOREBRAIN_RUN_WORK=$(mktemp -d)`、`FOREBRAIN_RUN_SESSION=forebrain-runes`、`FOREBRAIN_RUN_PORT=8791`，夹具 `$WORK/proj/FOREBRAIN.md` = 16000 个「中」（16000 rune / 48000 字节），`FAKE_DUMP_DIR=$WORK/dump`。

- 场景 A（回归）：`/status` 面板 `Instructions: AGENTS.md (agent), SOUL.md (agent), USER.md (agent), FOREBRAIN.md (truncated to 12k runes)`。
- 场景 B（分水岭）：解析 `request-0001.json` 后取含 `<forebrain_rules>` 的消息 → `kept chars: 12000`、`replacement chars: 0`、`has ellipsis: True`（坏版本此处为 4000）。
- 场景 C（回归）：换成 500 个「中」并重启 → 面板无 `truncated` 标签，请求体 `kept chars: 500`、无省略号。
- 收尾：`driver.sh stop` + `rm -rf "$FOREBRAIN_RUN_WORK"`，无残留 tmux 会话。

### Done criteria 复核

`go build ./...` exit 0；四包测试全绿；`go vet ./...` exit 0；`grep -rn "MaxBytesPerFile\|DefaultMaxBytesPerFile\|assemblyBudgetBytes\|bootstrapMaxPerFile" pkg/` 空；`grep -rn "len(merged) > maxBody\|len(part)" pkg/assembly/rules_hook.go` 空；`grep -rn "s\[:max\]\|raw\[:maxBytes\]" pkg/assembly pkg/home` 空；`grep -rn "truncated to .* chars\|-char rules limit" pkg/` 空；四个新测试存在且通过；`pnpm typecheck` exit 0；`git status --porcelain` 仅 14 个 in-scope 文件（两份 docs 在 `forebrain-harness.github.io` 独立仓，同样只有这两行改动）；无 commit。

### Deviations

1. §4 行内写 `budget?: number`，§3.4 写必填 `budget: number`：取必填（§3.4 的理由成立，且可选类型会让 `warningBudget.value = res.budget` 类型不通过）。意图、行为、验收不变。
2. 场景 C 需重启 TUI：`buildRules` 的缓存键不含文件 mtime（既有设计，本计划不改），同进程内换文件不会重建。验收用重启复现，结论不变。
