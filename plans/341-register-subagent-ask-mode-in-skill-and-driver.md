# Plan 341：把 `subagent-ask` 模式注册进 run-forebrain 的 SKILL.md 模式表与 driver.sh 用法/帮助

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
> **本计划只改文档/用法字符串，不改任何行为代码。**
>
> **工作目录**：`/Users/doudou/workspace/unionj-cloud/forebrain-harness/.worktrees/fix-user-interaction-subagent-ask`
> （分支 `fix-user-interaction-subagent-ask` 的 worktree，HEAD `4de94476`，写作时工作区干净）。下文所有相对路径都相对这个目录。
> **共用 worktree 提示**：339/340/341/342 可能并行在此 worktree 执行，兄弟计划留下的未提交改动属预期、不触发 STOP；
> 本计划的 drift check 与范围判据只按路径核对自己的 in-scope 文件（339 若已执行会改 `fake_provider.py`，不影响本计划的 grep 计数）。
>
> **Drift check（先跑）**：
> ```bash
> cd /Users/doudou/workspace/unionj-cloud/forebrain-harness/.worktrees/fix-user-interaction-subagent-ask
> git diff --stat 4de94476 -- .claude/skills/run-forebrain/SKILL.md .claude/skills/run-forebrain/driver.sh   # 预期：无输出
> grep -c 'subagent-ask' .claude/skills/run-forebrain/fake_provider.py   # 预期：≥2（docstring + 分发，证明模式已存在）
> ```
> 第一条有输出 → STOP。然后对照下方 "Current state" 摘录逐段核对；不一致即 STOP。

## Status

- **Priority**: P3
- **Effort**: S
- **Risk**: LOW（纯文档/用法字符串；模式本身已可用，不改行为）
- **Depends on**: none（与 339/340/342 零文件交集，可并行）
- **Category**: docs / dx
- **Planned at**: commit `4de94476`, 2026-11-20
- **Finding tag**: introduced（本分支新增模式但漏注册）

## Why this matters

本分支给 fake provider 加了 `subagent-ask` 模式（`fake_provider.py:567-568` 分发到 `serve_subagent_ask`，
docstring `:87-96` 已描述它），用来验证「子代理向用户提问、放宽 caps 的 ask 表单能在子代理视图渲染、答完 run 不死」。
**模式本身完全可用**——`driver.sh:110` 把 `$mode` 原样透传给 python，没有白名单。

但面向使用者的两处入口没注册它：
- `SKILL.md` 的模式表（`:63-81`）列了 hang/stream/.../subagent-limit/subagent-steer/gemini/lengthcontinue，**唯独没有 subagent-ask**。
- `driver.sh` 的 `start` 用法字符串（`:122` 的 `cmd_start` 注释、`:329` 的 help 块）与 help 散文（`:349` subagent-limit、`:359-365` subagent-steer）**都没提 subagent-ask**。

结果：照 SKILL.md/driver.sh 帮助选模式的人不知道这个模式存在，只能去翻 python 源码才发现。补上注册，让驱动自描述完整。

## Current state

### SKILL.md 模式表（要加一行）

`.claude/skills/run-forebrain/SKILL.md:79`（subagent-steer 行，新行插在它之后）：

```markdown
| `subagent-steer` | the main agent's first request dispatches a general-purpose `subagent_run` probe; the subagent's first request runs a shell call (the answer text, default `sleep 8`); its next request — carrying the tool result and any steer queued meanwhile — sends one delta and hangs, so the subagent keeps running | a steer queued in a subagent's view (Down, Down, Enter opens it; type and Enter) being taken at its tool boundary: the "submitted after next tool call" preview must drop it with no key pressed, and it appears as a user message in that view. Needs `ENABLE_SUBAGENT=1` |
```

表头（`:63-64`）：`| Mode | Behaviour | Use it to test |` / `|---|---|---|`。下一行 `:80` 是 `gemini`。

### driver.sh 用法字符串（两处模式列表要加 `|subagent-ask`）

`:122`：
```bash
cmd_start() { # start [hang|stream|reply|usage|drip|toolcall|ask|shell|multishell|streamtools|tool|limit|subagent-limit|subagent-steer] [answer-text] [drip-delay]
```

`:329`（help 块内，2 空格缩进）：
```bash
  start [hang|stream|reply|usage|drip|toolcall|ask|shell|multishell|streamtools|tool|limit|subagent-limit|subagent-steer] [answer-text] [drip-delay]
```

两处都在 `subagent-steer` 之后、`]` 之前插 `|subagent-ask`。

### driver.sh help 散文（要加一段）

`:359-365`（subagent-steer 段，24 空格缩进）：
```
                        subagent-steer = the main agent dispatches a
                        general-purpose probe (needs ENABLE_SUBAGENT=1) that
                        runs a shell call (the answer text, default sleep 8);
                        its next request takes a steer queued meanwhile, sends
                        one delta and hangs, so the subagent keeps running;
                        FAKE_SUBAGENT_STEER_FINISH=1 finishes it instead, so
                        its answer goes back to the main agent
```
`:366` 起是 `                         RESUME_ID=<session> ...`（25 空格缩进）。新段插在 `:365` 之后、`:366` 之前，24 空格缩进。

### 模式行为（注册文案的事实来源，不改）

`serve_subagent_ask:953-1011` 的**代码实际行为** + `SUBAGENT_ASK_ARGUMENTS:310-327`：
主代理首个请求派发 general-purpose `subagent_run` 探针；子代理自己的请求（尚无 tool result）回一个 `user_interaction` 调用，
其第一个选项 label 远超旧的 5 词上限、共五个选项超旧的 4 选项上限（放宽后的形状）；真实问题 overlay 必须出现在子代理视图
（而不是 "missing ask form payload" 失败）；答完（Enter）子代理续跑、以固定行 `answer received, subagent done` 结束
（**answer-text 参数在本模式未被使用**——函数体无 TEXT 引用，评审已 grep 核实）；主代理后续请求回固定行 `primary done`。需 `ENABLE_SUBAGENT=1`。

**docstring 瑕疵（2026-11-20 owner 裁决「要补」，已在 339 的文件里改正）**：模块 docstring 与函数 docstring 原称答完的请求
"gets the answer text"，与代码不符（本分支引入的文案缺陷）。本次注册文案仍以代码实际行为为准、**不照抄 docstring 该短语**；
docstring 本身的修正落在 `fake_provider.py`（plan 339 的文件），owner 2026-11-20 裁决补齐后已改为固定行 `answer received, subagent done`
＋「answer-text 参数在本模式未使用」＋主代理回 `primary done`，纯注释改动（`py_compile` 过、结构断言仍 1/1）。

### 约定

- SKILL.md 模式表是英文三列 markdown 表；新行照 subagent-steer/subagent-limit 行的语气与结构（Behaviour 列描述请求序列，Use it to test 列描述验证什么 + Needs `ENABLE_SUBAGENT=1`）。
- driver.sh help 散文是纯文本、固定缩进（模式段 24 空格）；新段照 subagent-steer 段的折行宽度与缩进。
- 用法字符串的模式列表用 `|` 分隔、方括号包裹；只在末尾追加，不重排既有顺序。

## Commands you will need

| 用途 | 命令（在 worktree 根目录） | 成功预期 |
|---|---|---|
| bash 语法 | `bash -n .claude/skills/run-forebrain/driver.sh` | exit 0、无输出 |
| SKILL.md 命中 | `grep -c 'subagent-ask' .claude/skills/run-forebrain/SKILL.md` | ≥1 |
| driver.sh 命中 | `grep -n 'subagent-ask' .claude/skills/run-forebrain/driver.sh` | 3 行（122、329、散文段） |

## Scope

**In scope**：
- `.claude/skills/run-forebrain/SKILL.md`（模式表加一行 subagent-ask）
- `.claude/skills/run-forebrain/driver.sh`（`:122`、`:329` 模式列表各加 `|subagent-ask`；help 散文加一段）

**Out of scope**：
- `fake_provider.py`——模式与 docstring 已存在，不改（双终止缺陷是 plan 339 的范围）。
- 用法字符串里**其它**缺漏的模式（streamapprove/netdrop/gemini/lengthcontinue 也没列进 `:122/:329` 的短列表）——
  那是 pre-existing 的非穷尽列表，不是本分支引入的缺口，本计划只补 subagent-ask，不顺手补齐其它。
- `serve` 用法行（`:370`，`serve [mode] ...` 是泛指，无模式列表）——不需要改。
- 任何 Go/前端源码、任何行为逻辑。

## Git workflow

不建分支、不 commit、不 push、不 stash（owner 手动提交）。改动留在 worktree 工作区。

## Steps

### Step 1：SKILL.md 模式表加行

在 `.claude/skills/run-forebrain/SKILL.md` 的 `subagent-steer` 行（`:79`）之后、`gemini` 行（`:80`）之前插入：

```markdown
| `subagent-ask` | the main agent's first request dispatches a general-purpose `subagent_run` probe; the subagent's own request (no tool result yet) answers with a `user_interaction` call whose first option label runs far past the old 5-word cap and which carries five options, past the old 4-option cap; the request carrying the answer finishes with a fixed line (`answer received, subagent done`), and the main agent's later requests answer `primary done` | the relaxed ask shapes rendering in the subagent's own view: the real question overlay appears (the long label wraps, five options plus Other) instead of a "missing ask form payload" failure, and answering it (Enter) resumes the subagent so the run completes rather than dying. Needs `ENABLE_SUBAGENT=1` |
```

**Verify**：`grep -n 'subagent-ask' .claude/skills/run-forebrain/SKILL.md` → 恰 1 行（`:80`，位于 subagent-steer `:79` 与 gemini `:81` 之间）；三列结构与表头一致。

### Step 2：driver.sh 两处模式列表追加

把 `:122` 与 `:329` 两行模式列表里的 `...|subagent-limit|subagent-steer]` 改成 `...|subagent-limit|subagent-steer|subagent-ask]`。
只追加 `|subagent-ask`，不动其余字符与缩进。

**Verify**：`grep -n 'subagent-steer|subagent-ask\]' .claude/skills/run-forebrain/driver.sh` → 2 行（122、329）。

### Step 3：driver.sh help 散文加段

在 `:365`（subagent-steer 段末行 `its answer goes back to the main agent`）之后、`:366`（`RESUME_ID=...`）之前插入（24 空格缩进）：

```
                        subagent-ask = the main agent dispatches a
                        general-purpose probe (needs ENABLE_SUBAGENT=1); the
                        subagent's own request answers with a user_interaction
                        call whose first option label runs far past the old
                        5-word cap and which carries five options, past the old
                        4-option cap, so the real question overlay must render
                        in the subagent's view; answering it (Enter) resumes the
                        subagent, which finishes with a fixed line (the
                        answer-text argument is unused)
```

**Verify**：`bash -n .claude/skills/run-forebrain/driver.sh` → exit 0；
`grep -n 'subagent-ask' .claude/skills/run-forebrain/driver.sh` → 3 行（122、329、新散文段首行）。

### Step 4：核对改动范围

```bash
cd /Users/doudou/workspace/unionj-cloud/forebrain-harness/.worktrees/fix-user-interaction-subagent-ask
git diff --stat 4de94476 -- .claude/skills/run-forebrain/SKILL.md .claude/skills/run-forebrain/driver.sh
git status --short
```

**Verify**：`git diff --stat 4de94476 -- .claude/skills/run-forebrain/SKILL.md .claude/skills/run-forebrain/driver.sh` 显示
SKILL.md `1 insertion(+)`、driver.sh `11 insertions(+), 2 deletions(-)`（两处列表行原位改写各计 1+/1−，散文净增 9 行；评审已模拟实跑核对）；
`git status --short -- .claude/skills/run-forebrain/SKILL.md .claude/skills/run-forebrain/driver.sh` 只含这两个文件（其它文件的改动若出现须归属兄弟计划，见 Done criteria）。

## Test plan

- 无自动化测试（纯文档/用法字符串）。验证＝grep 命中 + `bash -n` 语法 + diff 范围核对。
- 可选冒烟（非必须）：`bash .claude/skills/run-forebrain/driver.sh` 无参数应打印 help，其中 `start` 行与散文段现在含 subagent-ask。
  若驱动需要二进制/环境而不可用，跳过并注明。

## Done criteria

- [ ] `grep -n 'subagent-ask' .claude/skills/run-forebrain/SKILL.md` 恰 1 行（`:80`，新表行在 subagent-steer 与 gemini 之间）
- [ ] `grep -n 'subagent-ask' .claude/skills/run-forebrain/driver.sh` 恰好 3 行（122、329、散文段）
- [ ] `bash -n .claude/skills/run-forebrain/driver.sh` exit 0
- [ ] 范围合规：`git diff --name-only 4de94476` 里本计划只改 `.claude/skills/run-forebrain/SKILL.md` 与 `driver.sh`；若出现其它文件，只能是兄弟计划的 in-scope 文件（339：`fake_provider.py`；340：`pkg/tui/chat_surface_test.go`、`pkg/turn/subagent_approval_test.go`；342：`pkg/gateway/approval.go`、`approval_test.go`），否则 STOP
- [ ] `fake_provider.py` 未被本计划改动：`git diff 4de94476 -- .claude/skills/run-forebrain/fake_provider.py` 里只允许出现**兄弟计划 339** 的两行删除
  （内层 `else` 的 `write_raw(b"data: [DONE]\n\n")`+`write_raw(b"")`）；出现任何散文/模式/其它增删即 STOP。共用 worktree 里 339 已落地时该 diff 非空属预期，
  不能按「无输出」判定（与本节兄弟白名单一致）

## STOP conditions

1. Drift check 第一条有输出（SKILL.md/driver.sh 相对 `4de94476` 已改动）。
2. `grep -c 'subagent-ask' fake_provider.py` < 2（模式可能不存在或已被改名）——停下报告实际模式名与分发现状。
3. SKILL.md 模式表结构与摘录不符（列数、subagent-steer/gemini 行位置）——停下报告实际表。
4. driver.sh 的 `:122/:329` 模式列表或 help 散文缩进与摘录不符——停下报告实际文本，不要凭猜测插行。
5. 发现需要改 fake_provider.py 或补其它缺漏模式才能「注册完整」——本计划只补 subagent-ask，STOP 报告。

## Maintenance notes

- `:122/:329` 的短模式列表本就非穷尽（streamapprove/netdrop/gemini/lengthcontinue 未列）；本计划只补本分支引入的 subagent-ask。
  若将来想让用法列表穷尽，另立计划统一补齐，别在单模式注册里夹带。
- 新增 fake provider 模式时，注册三处要一起改：SKILL.md 模式表、driver.sh 两处模式列表、driver.sh help 散文（外加 fake_provider.py 自己的 docstring）。
  本分支只改了 python docstring 漏了其余三处，即本发现成因。
- 模式行为文案以 `serve_subagent_ask` **代码实际行为**与 `SUBAGENT_ASK_ARGUMENTS` 为准（docstring 的 "answer text" 短语不实，见 Current state；
  该短语已于 2026-11-20 按 owner 裁决改正，不再是障碍）；若将来放宽/收紧 ask 形状，同步更新这几处描述。