# Plan 087: 审批卡的事实行重新对齐引擎的载荷（路径 / 联网 / 理由 / 危险规则 / 沙箱拒因 / MCP 字段）

> **Executor instructions**: 按步骤执行，每步跑完「Verify」并确认输出符合预期再进入下一步。
> 命中「STOP conditions」任一条就停下来报告，不要即兴改动。全部完成后更新
> `plans/README.md` 里本计划那一行的状态（除非派发你的评审方说他维护索引）。
>
> **Drift check（先跑）**: `git diff --stat 8d3322c..HEAD -- frontend/src/lib/approvalGates.ts frontend/src/lib/approvalGates.test.ts frontend/src/locales/index.ts pkg/tool/shell_tool.go pkg/tool/shell_tool_test.go`
> 若有文件变动，先把「Current state」里的摘录与现码逐行对照；对不上按 STOP 处理。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED
- **Depends on**: none（088 可与本计划并行，但两者都改 `approvalGates.ts` 时请串行）
- **Category**: bug
- **Planned at**: commit `8d3322c`, 2026-10-21

## Why this matters

审批卡是用户**唯一**能看到"这一次要发生什么"的地方，也是放行写文件、跑命令、放宽沙箱、读受保护路径的唯一入口。
web 端的这些事实行由 `frontend/src/lib/approvalGates.ts` 从 action 的 `payload_json` 现场推导，而它已经和引擎的真实载荷脱节：
文件权限的路径被渲染成 `[object Object]`、联网授权一个字都不提、`lsp` 触发的受保护读退化成"要允许这次调用吗？"、
`rm -f` 的句子凭空编造"删完要重新编译"、沙箱拒因说反、MCP 工具把 `repo`/`body` 这类内部字段名和原始值直接摊给用户。
结果用户在无法核实的情况下点"允许"，或者读到引擎从未断言过的结论。修完之后：卡片上的每一行都是引擎载荷里的事实，
或者根本不出现（这是本仓 `plans/README.md` 记录的 owner 硬规则："参数按族语义化渲染，不出现工具内部名/原始 JSON"）。

## Current state

- `frontend/src/lib/approvalGates.ts`（416 行，新建于 `8d3322c`）— 族判定 `approvalFamily`、事实与 diff 抽取 `approvalFacts`、
  问句 `approvalQuestion`、按钮文案在 `frontend/src/composables/usePendingActions.ts:410-460`。
- `frontend/src/components/chat/ApprovalGateCard.vue:44-49` — 只把 `gate.facts.facts` 逐行 `{{ fact }}` 打印；不读 `facts.path`。
- `pkg/safety/permission_types.go:425-431` — `FileSystemPermissionEntry.Path` 是**对象**，不是字符串：

```go
type FileSystemPermissionEntry struct {
	Path                FileSystemPermissionPath                `json:"path"`
	Access              FileSystemPermissionAccess              `json:"access"` // read | write | deny
	MissingPathBehavior FileSystemPermissionMissingPathBehavior `json:"missing_path_behavior,omitempty"`
}
// FileSystemPermissionPath: {Type} + {Path} + {Pattern} + {Value *FileSystemSpecialPath}
```

- `pkg/safety/permission_types.go:441-470`（`FileSystemPermissionProfile.MarshalJSON`）：只有"全部是纯 path + read/write +
  无 `missing_path_behavior` + 无 `glob_scan_max_depth`"时才降级成旧的 `{read:[],write:[]}` 形态；**只要有一个 `deny` 条目、
  或一个 `missing_path_behavior: skip`、或一个 special 路径，整个 profile 就走 `entries[]` 对象形态**——此时每一行都坏。
- `pkg/safety/permission_types.go:524-526` — `NetworkPermissionProfile` **只有** `enabled`，全仓没有任何 `json:"hosts"` 字段：

```go
type NetworkPermissionProfile struct {
	Enabled *bool `json:"enabled,omitempty"`
}
```

- `frontend/src/lib/approvalGates.ts:216-241` — 两处错误推导（`String(entry.path ?? '')`、`network?.hosts`）：

```ts
const path = String(entry.path ?? '').trim()
...
function permissionNetworkHosts(permissions: unknown): string[] {
  ...
  return stringList(network?.hosts)
}
```

- `frontend/src/lib/approvalGates.ts:87` — 受保护读只认 `read_file`：

```ts
if (k === 'read_file' && payload.force_tool_approval === true) return 'protected_read'
```

  但 `pkg/tool/search.go:812` 用 `authorizeRead(ctx, st, "lsp", in.FilePath)` 对 `lsp` 工具发起同一类审批，
  payload 由 `pkg/tool/permissions.go:136-147` 的 `markProtectedApproval` 写成
  `{force_tool_approval:true, approval_reason:"protected_path", justification: <reason>}`（`protectedPathApprovalReason`
  常量在 `pkg/tool/permissions.go:163`），调用侧再补 `file_path` / `resolved_file_path`（`pkg/tool/file_tools.go:274-276`）。
  于是 `lsp` 落到 `fallback`：问句退化成"要允许这次调用吗？"、事实行空。前端全仓 `grep justification` **零命中**——
  理由行在 web 上根本不存在。

- `pkg/tui/overlays.go:1812-1818` — 终端侧的规则，本计划照抄它的判据（这是本仓既有的正确实现，不是新设计）：

```
// approvalJustification returns the sentence an approval has to show when the
// runtime supplied one. ... Crucially the test is whether the payload carries a
// reason, not which tag it carries: enumerating tags means every new reason
// silently renders as a bare action id.
```

- `frontend/src/lib/approvalGates.ts:160-206` — `forcedRmFact` 自己写了一个 shell 分词器，命中后输出
  `frontend/src/locales/index.ts:1456` 的句子：`'会强制删掉整个 {path} 目录，删完要重新编译'` /
  `'Force-deletes the whole {path} directory — it has to be rebuilt afterwards'`。
  引擎的规则只有一条（`pkg/safety/shell.go:533-545`）：`rm` 带 `-f`/`--force` ⇒
  `&DangerousCommandMatch{Rule: "forced_rm"}`；它**不**说"整目录"、更不提"重新编译"。
  引擎的分词器遇到 `*`、`$`、`(`、`<`、`>`、`[`、`]`、`~`、`?`、`!`、换行等一律放弃（`pkg/safety/shell.go:505-508`），
  前端不放弃 ⇒ `rm -f *.log` 引擎判"无危险规则"，卡片却断言"整个 *.log 目录"。
- `pkg/tool/shell_tool.go:1027-1047` — 载荷目前**没有**记录命中的规则名：

```go
if assessment.Dangerous != nil {
	payload["force_tool_approval"] = true
	payload["approval_reason"] = "dangerous_shell_syntax"
} else if assessment.UsedComplexParsing { ... }
```

- `frontend/src/lib/approvalGates.ts:298-311` — 沙箱拒因被压成两句固定话；`frontend/src/locales/index.ts:1454-1455`：
  `reason 非空 ⇒ '沙箱只允许改这个项目里的文件'`、`reason 空 ⇒ '沙箱拦下了这条命令'`。
  真实拒因可能是网络（`pkg/tool/shell_tool.go:497`、`pkg/tui/chat_session_test.go:502` 的
  `listen tcp 127.0.0.1:0: bind: operation not permitted`），此时"只允许改项目内文件"是假话；
  引擎自己的句子在 `pkg/tool/shell_tool.go:883-886`。
- `frontend/src/lib/approvalGates.ts:345-355` — MCP 族把载荷前 3 个标量拼成 `${key} ${String(value)}`（原始字段名 + 无长度上限）。
  终端侧的等价实现是 `pkg/tui/overlays.go:1780-1788` 的 `approvalFieldLabel`（`_`→空格、首字母大写）+
  `:1662-1705`/`:1780-1788` 的排序与换行。
- 测试 fixture 与真实 wire 不符，因此上面各条都不会红：`frontend/src/lib/approvalGates.test.ts:126-131`、
  `frontend/src/components/chat/ApprovalCard.test.ts:164` 用**字符串**路径和凭空写出的 `network: { hosts: [...] }`；
  `ApprovalCard.test.ts:196` 用合成 payload 锁死 `lsp` 的 fallback 行为。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 前端单测 | `cd frontend && pnpm test -- src/lib/approvalGates.test.ts` | 全 passed |
| 前端全量单测 | `cd frontend && pnpm test` | 全 passed（~140 文件） |
| 类型检查 | `cd frontend && pnpm exec vue-tsc --noEmit` | exit 0，无输出 |
| Go 单包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` | `ok` |
| Go 相关包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run ./pkg/tui -count=1` | 全 `ok` |
| vet | `go vet ./pkg/tool` | 无输出 |
| e2e（任务相关单个 spec） | `FOREBRAIN_E2E_SPEC=e2e/approval-families.spec.ts scripts/acceptance/web_e2e.sh` | 末行 `web e2e: PASS` |

注意：Go 全量套件必须带临时 home（`FOREBRAIN_HOME="$(mktemp -d)"`）——本仓有写坏真实 home 的历史，见 `FOREBRAIN.md` 的 House rules。

## Scope

**In scope**（只改这些文件）：
- `frontend/src/lib/approvalGates.ts`
- `frontend/src/lib/approvalGates.test.ts`
- `frontend/src/components/chat/ApprovalCard.test.ts`（只改 fixture 与断言）
- `frontend/src/locales/index.ts`（只改本族的 8 个键）
- `pkg/tool/shell_tool.go`（只加 `dangerous_rule` 载荷字段）
- `pkg/tool/shell_tool_test.go`（加一条断言）

**Out of scope**（看着相关，但不要碰）：
- `pkg/tui/overlays.go` 的任何渲染：终端侧已经是正确实现，本计划以它为基准。
- `frontend/src/components/chat/ApprovalGateCard.vue`：它只打印 `facts`，不需要改。
- `frontend/src/composables/usePendingActions.ts:443-449` 的按钮档位：owner 已裁决 shell 记住档统一「这类命令不再问」，
  不要再引入 `commandScope`（见 `plans/README.md` 083 执行记录）。
- `frontend/src/components/chat/PendingActionsPanel.vue`：它是死代码，由 093 处理。
- 沙箱/权限/规则判定的 Go 语义：本计划只搬运引擎已有的结论，不新增判定。

## Git workflow

- 分支：当前 `dev` 工作区直接改（本仓 owner 的常态是改动留在工作区等审阅）；**不 commit、不 push**，除非派发方明确要求。
- 若派发方要求提交：Conventional Commits，`fix(web): …`，body 用 `Why:` / `What:` / `Verification:`，并 `git commit -s`。

## Steps

### Step 1: 权限条目按 wire 的对象形态读取

改 `frontend/src/lib/approvalGates.ts` 的 `permissionEntries`：`entry.path` 可能是
`{type:'path',path}` / `{type:'glob_pattern',pattern}` / `{type:'special',value:{kind,subpath}}`，也可能是历史形态的字符串。
新增一个把条目位置读成一句话的函数，例如：

```ts
function entryLocation(raw: unknown): string {
  if (typeof raw === 'string') return raw.trim()
  if (!raw || typeof raw !== 'object') return ''
  const p = raw as Record<string, unknown>
  const type = String(p.type ?? '').trim()
  if (type === 'glob_pattern') return String(p.pattern ?? '').trim()
  if (type === 'special') {
    const value = p.value && typeof p.value === 'object' ? p.value as Record<string, unknown> : {}
    const kind = String(value.kind ?? '').trim()
    const subpath = String(value.subpath ?? '').trim()
    if (!kind) return ''
    return subpath ? `${kind}/${subpath}` : kind
  }
  return String(p.path ?? '').trim()
}
```

`access === 'deny'` 仍然跳过（拒绝条目不放行任何东西，不占事实行）。位置读不出就跳过该条，绝不输出空路径或 `[object Object]`。

**Verify**: `cd frontend && pnpm test -- src/lib/approvalGates.test.ts` → 通过（此时还没有新用例）。

### Step 2: 联网授权写成一件事

把 `permissionNetworkHosts` 换成读 `network.enabled`：

```ts
function permissionNetworkEnabled(permissions: unknown): boolean {
  const profile = permissions && typeof permissions === 'object' ? permissions as Record<string, unknown> : null
  const network = profile?.network && typeof profile.network === 'object' ? profile.network as Record<string, unknown> : null
  return network?.enabled === true
}
```

`ApprovalFacts` 里删掉 `networkHosts`（改成 `network?: boolean`），`approvalFacts` 的 `request_permissions` 分支在
`network === true` 时追加一行 `t('chat.approvalFactsNetwork')`。

**Verify**: `grep -rn "network.hosts\|networkHosts" frontend/src` → 无输出；
`cd frontend && pnpm exec vue-tsc --noEmit` → exit 0。

### Step 3: 族判定与理由行改成"看载荷"，不看工具名

1. `approvalFamily`：把 `if (k === 'read_file' && payload.force_tool_approval === true) return 'protected_read'` 改成
   与工具名无关的判据——`payload.approval_reason === 'protected_path'`（或 `force_tool_approval === true` 且载荷带有
   `resolved_file_path`）即 `protected_read`，放在现有 `shell_outside_sandbox` 判断**之后**（那个分支优先，别打乱顺序）。
2. `approvalFacts` 里给**所有**族统一补一行理由：`payload.justification` 非空则以它作为第一条事实
   （`ApprovalFacts` 增加 `rationale?: string`，`ApprovalGateCard.vue` 不需要改——理由作为 `facts[0]` 即可）。
   `dangerous_shell_syntax`、`plan_mode_unproven_command`、`protected_path` 的 justification 因此全都回到卡上。
3. 问句：`approvalQuestion` 的 `fallback` 分支保持现状（"要允许这次调用吗？"），但 `protected_read` 的问句要能带路径
   （事实行已给路径，问句不必重复）。

**Verify**: `cd frontend && pnpm exec vue-tsc --noEmit` → exit 0。

### Step 4: 引擎把命中的危险规则写进载荷；前端删掉自己的 shell 分词器

1. `pkg/tool/shell_tool.go:1034-1036`：

```go
if assessment.Dangerous != nil {
	payload["force_tool_approval"] = true
	payload["approval_reason"] = "dangerous_shell_syntax"
	if rule := strings.TrimSpace(assessment.Dangerous.Rule); rule != "" {
		payload["dangerous_rule"] = rule
	}
} else if assessment.UsedComplexParsing { ... 保持不变 ... }
```

2. `frontend/src/lib/approvalGates.ts`：**整段删除** `forcedRmFact` / `forcedRmSegmentFact`（:155-206）。
   `shell` 分支改成读 `payload.dangerous_rule`：

```ts
case 'shell': {
  const rule = String(payload.dangerous_rule ?? '').trim().toLowerCase()
  const sentence = rule === 'forced_rm' ? t('chat.approvalFactsForcedRm') : ''
  return { family, facts: sentence ? [sentence] : [] }
}
```

   未知规则 ⇒ 不出现事实行（引擎没给的结论，web 也不许编）。
3. `frontend/src/locales/index.ts` 改 `chat.approvalFactsForcedRm` 的措辞（去掉"整目录""重新编译"与 `{path}` 参数）：
   - zh: `'强制删除文件（rm -f），引擎把这类命令列为危险命令'`
   - en: `'Force-removes files (rm -f) — the engine treats this as a dangerous command'`
   若 `chat.approvalFactsForcedRmNoPath` 因此无人使用，一并删除（`grep` 确认零引用后再删）。

**Verify**: `grep -n "forcedRmSegmentFact\|forcedRmFact" frontend/src/lib/approvalGates.ts` → 无输出；
`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run TestShellDangerousRuleInPayload -count=1` → 见 Step 7 的用例。

### Step 5: 沙箱拒因照抄引擎的句子

`approvalGates.ts` 的 `shell_outside_sandbox` 分支：非 scoped 重试时

```ts
const reason = String(payload.sandbox_denial_reason ?? '').trim()
const facts = reason
  ? [t('chat.approvalFactsSandboxBlocked'), reason]   // 拒因原文单独一行
  : [t('chat.approvalFactsSandboxBlocked')]
```

删掉 `chat.approvalFactsSandboxOnlyProject`（zh/en 两处），并在删之前 `grep` 确认零引用。
拒因原文来自引擎（`pkg/tool/shell_tool.go:883-886` 的 `The sandbox blocked this command: <detail>` 同源），
它是英文也没关系——终端同样原样显示；**不要**在前端翻译或改写它。

**Verify**: `grep -rn "approvalFactsSandboxOnlyProject" frontend/src` → 无输出。

### Step 6: MCP 参数按语义标签渲染，且限长

`approvalGates.ts` 的 `mcp_tool` 分支改成：

- 只取**标量**字段，按 key 字母序（稳定），最多 3 条；
- 标签：`key.replace(/_/g, ' ')` 去多余空白、首字母大写（与 `pkg/tui/overlays.go:1780-1788` 同规则）；
- 值：`String(value)` 压成单行（`/\s+/g → ' '`），超过 160 字符截断并补 `…`；
- 输出形如 `Repo: unionj-cloud/forebrain-harness`（标签与值之间是 `: `，不是空格）；
- 值里若含有 diff/patch 类多行内容，只保留首行（其余由卡片外的工具卡显示）。

**Verify**: `cd frontend && pnpm test -- src/lib/approvalGates.test.ts` → 通过。

### Step 7: 测试改成引擎真实载荷，并钉住本计划的六个缺陷

1. `pkg/tool/shell_tool_test.go` 新增（名字可微调，保持以 `Test` 开头）：

```go
func TestShellDangerousRuleInPayload(t *testing.T) {
	assessment := safety.AssessShellCommand("rm -f /tmp/x", 0) // 用包内既有的评估入口；若签名不同按实际调用
	payload := shellAssessmentApprovalPayload("rm -f /tmp/x", &ShellInput{}, assessment)
	if payload["dangerous_rule"] != "forced_rm" {
		t.Fatalf("dangerous_rule = %v, want forced_rm", payload["dangerous_rule"])
	}
	// 复杂解析但无危险规则的命令不得带该字段
	payload = shellAssessmentApprovalPayload("echo $(rm -f x)", &ShellInput{}, safety.AssessShellCommand("echo $(rm -f x)", 0))
	if _, ok := payload["dangerous_rule"]; ok {
		t.Fatalf("complex-but-not-dangerous must not name a rule: %v", payload["dangerous_rule"])
	}
}
```

   `safety.AssessShellCommand` 的真实名字以现码为准（`grep -n "func Assess" pkg/safety/shell.go`）；对不上就按现名调用，
   不要为了测试改动生产签名。

2. `frontend/src/lib/approvalGates.test.ts` 重写 fixture 为真实 wire 形态，至少覆盖：
   - `entries` 对象形态 + 一个 `deny` 条目 ⇒ 读/写行显示路径文本，绝不出现 `[object Object]`；
   - `{type:'glob_pattern',pattern:'src/**/*.ts'}` 与 `{type:'special',value:{kind:'project_roots'}}` 各一行；
   - `network: { enabled: true }` ⇒ 出现联网事实行；
   - `agent:"lsp"` + `{force_tool_approval:true, approval_reason:'protected_path', resolved_file_path:'/x/.forebrain/forebrain.yaml', justification:'…'}`
     ⇒ family 是 `protected_read`，事实行含路径与理由；
   - `dangerous_rule:'forced_rm'` ⇒ 事实行是新的引擎措辞，且**不含**命令里的路径片段（`*.log` 这类）；
   - 无 `dangerous_rule` ⇒ shell 族事实行为空；
   - `sandbox_denial_reason:'listen tcp 127.0.0.1:0: bind: operation not permitted'` ⇒ 拒因原文出现在事实行，且不出现"只允许改这个项目里的文件"；
   - MCP：`{repo:'a/b', body:'x'.repeat(500)}` ⇒ 标签为 `Repo:`，值截断且无换行。
3. `frontend/src/components/chat/ApprovalCard.test.ts`：把 `:164` 附近的字符串路径 fixture 与凭空 `network.hosts`
   换成与上面一致的真实载荷；`:196` 的合成 `lsp` fixture 改成 Step 3 的真实载荷。

**Verify**: `cd frontend && pnpm test -- src/lib/approvalGates.test.ts src/components/chat/ApprovalCard.test.ts` → 全 passed；
`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run TestShellDangerousRuleInPayload -count=1` → `ok`。

### Step 8: 全量回归

**Verify**:
1. `cd frontend && pnpm test` → 全 passed。
2. `cd frontend && pnpm exec vue-tsc --noEmit` → exit 0。
3. `go vet ./pkg/tool` → 无输出。
4. `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/tui -count=1` → 全 `ok`。
5. `FOREBRAIN_E2E_SPEC=e2e/approval-families.spec.ts scripts/acceptance/web_e2e.sh` → 末行 `web e2e: PASS`。

## Test plan

- 新建用例见 Step 7。结构参照 `frontend/src/lib/approvalGates.test.ts` 现有 `describe('approvalFacts')` 的写法
  （table-driven 的 `it.each` 风格）；Go 侧参照 `pkg/tool/request_permissions_test.go:2915-2975` 的断言风格。
- 反向验证（必须做，证明用例真能抓）：把 Step 1 的 `entryLocation` 临时改回 `String(entry.path ?? '')`，
  跑 `pnpm test -- src/lib/approvalGates.test.ts`，确认新用例**变红**，再改回。

## Done criteria

- [ ] `cd frontend && pnpm test` exit 0，新增用例覆盖 Step 7 列出的 8 个场景
- [ ] `cd frontend && pnpm exec vue-tsc --noEmit` exit 0
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` → `ok`
- [ ] `grep -rn "\[object Object\]\|network.hosts\|forcedRmSegmentFact\|approvalFactsSandboxOnlyProject" frontend/src` → 无输出
- [ ] `grep -rn "dangerous_rule" pkg/tool/shell_tool.go frontend/src/lib/approvalGates.ts` → 两侧各命中
- [ ] `git status --porcelain` 只包含 Scope 里列出的文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

- 「Current state」摘录与现码对不上（例如 `permission_types.go` 已经改回字符串路径）。
- `pkg/tool` 的 shell 审批载荷改成"可被严格解码拒绝"的结构（有任何 `DisallowUnknownFields` 拒绝新增字段）——
  需要先确认批准回放不会被新字段打断；不要自行加兼容层。
- 沙箱拒因 `sandbox_denial_reason` 在真实载荷里其实不携带明细（只带 `sandbox_denied` 这类枚举）——此时按 STOP 报告，
  说明"引擎没给明细"，不要在前端猜。
- 某条新用例无论怎么改都红（说明对 wire 的理解有误）。
- 同一个 Step 的 Verify 连试两次仍失败。

## Maintenance notes

- **判据只有一处**：族判定必须继续"看载荷"，不要退回按工具名枚举——终端侧的注释
  `pkg/tui/overlays.go:1812-1818` 记着原因（每加一个 reason 就会退化成裸 action id）。
- 加了新的 approval reason 或新的危险规则时：Go 侧写进载荷（`approval_reason` / `dangerous_rule`），
  前端只在 `approvalFacts` 里加一条映射与一句文案；**不要**在前端重新实现判定逻辑。
- 评审要点：`permissionEntries` 对三种 path 形态的处理、`deny` 条目仍被跳过、理由行不会与族自身的事实重复两遍、
  文案里不再出现 `{path}` 之类未填充的参数。
- 本计划**未**处理：`write_file`/`edit_file` 的 diff（088）、死代码 `PendingActionsPanel`（093）、
  `facts.path` 全族未渲染（如需显示文件名，先在 088 里定成事实行再删该字段）。
