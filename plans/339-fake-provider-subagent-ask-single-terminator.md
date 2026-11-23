# Plan 339：修掉 fake_provider `subagent-ask` 模式应答请求的双 `[DONE]`／双终止块（chunked 流只终止一次）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **工作目录**：`/Users/doudou/workspace/unionj-cloud/forebrain-harness/.worktrees/fix-user-interaction-subagent-ask`
> （分支 `fix-user-interaction-subagent-ask` 的 worktree，HEAD `4de94476`，写作时工作区干净）。下文所有相对路径都相对这个目录。
> 若 owner 另派隔离 worktree，以 `4de94476` 为基线，步骤不变。
> **共用 worktree 提示**：339/340/341/342 可能并行在此 worktree 执行，兄弟计划留下的未提交改动属预期、不触发 STOP；
> 本计划的 drift check 与范围判据只按路径核对自己的 in-scope 文件。
>
> **Drift check（先跑）**：
> ```bash
> cd /Users/doudou/workspace/unionj-cloud/forebrain-harness/.worktrees/fix-user-interaction-subagent-ask
> git diff --stat 4de94476 -- .claude/skills/run-forebrain/fake_provider.py   # 预期：无输出
> ```
> 有输出 → STOP。然后对照下方 "Current state" 摘录逐段核对；不一致即 STOP。

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW（只删两行重复的流终止写入；不改变任何事件内容）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `4de94476`, 2026-11-20
- **Finding tag**: introduced（本分支新增 `serve_subagent_ask` 时带入）

## Why this matters

`subagent-ask` 是本分支为「子代理向用户提问、run 不能死」场景新增的 fake provider 驱动模式
（`.claude/skills/run-forebrain/fake_provider.py`，`MODE == "subagent-ask"` 在 `:567-568` 分发到 `serve_subagent_ask`）。
它用 HTTP chunked 编码逐帧写 SSE：`write_raw(b"")` 写的是**零长度块＝chunked 流的终止块**（见 `write_raw` 定义）。

`serve_subagent_ask` 的「子代理已拿到答案」分支（内层 `else`，`:988-994`）自己写了一遍
`data: [DONE]` + 终止块（`:993-994`），**又没有 `return`**，于是继续落到函数共享尾部（`:1010-1011`）
**再写一遍** `data: [DONE]` + 终止块。结果：这条应答请求的 chunked 流被终止了两次——第一个终止块之后的字节
（第二份 `[DONE]` 帧 + 第二个终止块）是协议非法的尾随数据。严格的 SSE/chunked 客户端会把它当成下一个响应的
 malformed 块，可能报错、挂起或误解析；这正是用来验证「子代理 ask 闭环能跑完」的驱动，自身却在闭环的最后一步发出坏流。

同文件的 `serve_subagent_steer`（`:892-951`）是正确范例：它的内层提前结束分支写完 `[DONE]`+终止块后
**`return`**（`:919`），不会落到尾部。但 `serve_subagent_ask` 的内层 `else` 是该分支的最后一条语句，
后面除了共享尾部没有别的代码——所以最干净的修法是**删掉内层那对重复写入**，让所有分支都只靠共享尾部终止一次
（`if not _has_tool_result` / `elif` / `else` 三个分支本来就是这么做的）。

## Current state

### 缺陷函数（要改）

`.claude/skills/run-forebrain/fake_provider.py:953-1011`（行号为 worktree 当前值）：

```python
    def serve_subagent_ask(self, body):
        """Drive "a subagent asks the user a question", told apart by request
        content rather than request order.
        ...（docstring 省略）...
        """
        system, _ = _system_and_last_user(body)
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        if SUBAGENT_SYSTEM_MARK in system:
            if not _has_tool_result(body):
                sys.stderr.write("SUBAGENT-ASK handing out the gate\n")
                sys.stderr.flush()
                self.write_event(chunk(delta={
                    "role": "assistant",
                    "content": "asking before continuing",
                    "tool_calls": [{
                        "index": 0,
                        "id": "call_ask_sub_0",
                        "type": "function",
                        "function": {"name": "user_interaction", "arguments": SUBAGENT_ASK_ARGUMENTS},
                    }],
                }))
                self.write_event(chunk(finish="tool_calls"))
            else:
                sys.stderr.write("SUBAGENT-ASK answered after the gate\n")
                sys.stderr.flush()
                self.write_event(chunk(delta={"role": "assistant", "content": "answer received, subagent done"}))
                self.write_event(chunk(finish="stop"))
                self.write_raw(b"data: [DONE]\n\n")   # ← :993 重复（删）
                self.write_raw(b"")                    # ← :994 重复（删）
        elif next(SUBAGENT_ASK_MAIN) == 0:
            self.write_event(chunk(delta={
                "role": "assistant",
                "content": "delegating the probe",
                "tool_calls": [{
                    "index": 0,
                    "id": "call_ask_main_0",
                    "type": "function",
                    "function": {"name": "subagent_run", "arguments": SUBAGENT_NET_PROBE_ARGS},
                }],
            }))
            self.write_event(chunk(finish="tool_calls"))
        else:
            self.write_event(chunk(delta={"role": "assistant", "content": "primary done"}))
            self.write_event(chunk(finish="stop"))
        self.write_raw(b"data: [DONE]\n\n")   # ← :1010 共享尾部（保留，唯一终止）
        self.write_raw(b"")                    # ← :1011 共享尾部（保留）
```

要删的就是内层 `else` 里的 `:993-994` 两行。删后内层 `else` 以 `self.write_event(chunk(finish="stop"))`（`:992`）结束，
和 `elif`/`else` 分支一样落到共享尾部 `:1010-1011` 终止一次。

### 终止块的含义（不改，只供理解）

`write_raw` 把 payload 包成一个 HTTP chunk：`self.wfile.write(b"%x\r\n%s\r\n" % (len(payload), payload))`。
`write_raw(b"")` ⇒ `0\r\n\r\n` ⇒ chunked 编码的**终止块**。一条响应只能有一个终止块；写第二个就是往已结束的流后面塞字节。

### 正确范例（不改）

`serve_subagent_steer:914-919`——内层提前结束分支写完一对终止写入后 `return`，避免落到尾部：

```python
                if os.environ.get("FAKE_SUBAGENT_STEER_FINISH") == "1":
                    self.write_event(chunk(delta={"role": "assistant", "content": "steer received, subagent finished"}))
                    self.write_event(chunk(finish="stop"))
                    self.write_raw(b"data: [DONE]\n\n")
                    self.write_raw(b"")
                    return
```

注意：steer 用 `return` 是因为它后面还有 hang/shell 代码必须跳过；ask 的内层 `else` 后面没有别的代码，
**删重复行**比加 `return` 更贴合本函数其余三个分支的写法。两种修法都能消除双终止，本计划选删行。

### 约定

- 这是测试驱动脚本（Python 3），不是产品代码；改动手工核对 + 结构断言即可，无需 Go 门禁。
- 缩进用 4 空格；内层 `else` 体是 16 空格缩进，共享尾部是 8 空格缩进——删行时不要动其余行的缩进。

## Commands you will need

| 用途 | 命令（在 worktree 根目录） | 成功预期 |
|---|---|---|
| 语法检查 | `python3 -m py_compile .claude/skills/run-forebrain/fake_provider.py` | exit 0、无输出 |
| 结构断言（终止块计数） | 见 Step 2 的 python 片段 | 打印 `OK` |

## Scope

**In scope**：
- `.claude/skills/run-forebrain/fake_provider.py`（只删 `serve_subagent_ask` 内层 `else` 的两行重复终止写入）

**Out of scope**：
- `serve_subagent_steer` / `serve_subagent_limit` / 其它任何 `serve_*` 函数——它们没有这个缺陷。
- 共享尾部 `:1010-1011`——保留，它是修后唯一的终止写入。
- `SUBAGENT_ASK_ARGUMENTS`（`:310-327`，放宽 caps 的 ask 表单）、`SUBAGENT_NET_PROBE_ARGS`、模式分发 `:567-568`——不动。
- `SKILL.md` / `driver.sh` 的模式注册——那是 plan 341 的范围，本计划不碰。
- 任何 Go 源码、任何前端文件。

## Git workflow

不建分支、不 commit、不 push、不 stash（owner 手动提交）。改动留在 worktree 工作区。

## Steps

### Step 1：删掉内层 `else` 的重复终止写入

打开 `.claude/skills/run-forebrain/fake_provider.py`，定位 `serve_subagent_ask` 内层 `else`（`SUBAGENT-ASK answered after the gate` 那段）。
删除紧跟在 `self.write_event(chunk(finish="stop"))` 之后的这两行：

```python
                self.write_raw(b"data: [DONE]\n\n")
                self.write_raw(b"")
```

删后该 `else` 分支应为：

```python
            else:
                sys.stderr.write("SUBAGENT-ASK answered after the gate\n")
                sys.stderr.flush()
                self.write_event(chunk(delta={"role": "assistant", "content": "answer received, subagent done"}))
                self.write_event(chunk(finish="stop"))
        elif next(SUBAGENT_ASK_MAIN) == 0:
```

**不要**加 `return`，**不要**动共享尾部 `:1010-1011`，**不要**动其它分支。

**定位警示**：同一对 16 空格缩进的 `write_raw` 终止写入也出现在 `serve_subagent_steer` 的 FINISH 分支（`:917-918`，其后跟 `return`）。
只删 `serve_subagent_ask` 内 `SUBAGENT-ASK answered after the gate` 那段里的一对；**不要做全文件搜索替换**（会误删 steer 的终止写入）。

**Verify**：`python3 -m py_compile .claude/skills/run-forebrain/fake_provider.py` → exit 0、无输出。

### Step 2：结构断言——函数内恰好一次终止

跑下面这段（它把 `serve_subagent_ask` 函数体单独切出来，数终止写入；用字面子串计数，避免转义陷阱）：

```bash
cd /Users/doudou/workspace/unionj-cloud/forebrain-harness/.worktrees/fix-user-interaction-subagent-ask
python3 - <<'PY'
import re
src = open(".claude/skills/run-forebrain/fake_provider.py").read()
m = re.search(r"\n    def serve_subagent_ask\(self, body\):\n(.*?)\n    def ", src, re.S)
assert m, "serve_subagent_ask not found"
body = m.group(1)
done = body.count("data: [DONE]")
term = body.count('self.write_raw(b"")')
print("DONE writes:", done, "terminator writes:", term)
assert done == 1 and term == 1, "serve_subagent_ask must terminate the chunked stream exactly once"
print("OK")
PY
```

**Verify**：打印 `DONE writes: 1 terminator writes: 1` 与 `OK`。

**反证（确认断言真的钉住缺陷）**：先 `git stash` 不允许——改用临时还原：`git show 4de94476:.claude/skills/run-forebrain/fake_provider.py > /tmp/fp_orig.py`，
把上面片段里的 `open(...)` 路径换成 `/tmp/fp_orig.py` 再跑一次 → 应打印 `DONE writes: 2 terminator writes: 2` 并 **AssertionError**（修前确实是双终止）。
确认后丢弃 `/tmp/fp_orig.py`，工作区保持 Step 1 的修改。把反证结果写进报告。

## Test plan

- 无新增自动化测试文件（这是手动驱动脚本，仓库没有 fake_provider 的单测 harness）。
- 验证＝Step 2 的结构断言（函数内 `data: [DONE]` 与 `write_raw(b"")` 各恰好 1 次）＋ Step 1 的 `py_compile`。
- 反证＝对 `4de94476` 原始文件跑同一断言应失败（2/2），证明断言钉住了本缺陷。
- 可选真机冒烟（非必须、有副作用，owner 同意才跑）：`ENABLE_SUBAGENT=1 .claude/skills/run-forebrain/driver.sh start subagent-ask 'the answer'`，
  在子代理视图答完问题后 `$D provider-log` 应见 `SUBAGENT-ASK answered after the gate`，且 TUI 不报 chunked 解析错误。
  跑完按 driver 惯例收尾。若环境不具备（无二进制/无终端），跳过并在报告里注明「未跑真机冒烟」。

## Done criteria

- [ ] `python3 -m py_compile .claude/skills/run-forebrain/fake_provider.py` exit 0
- [ ] Step 2 结构断言打印 `DONE writes: 1 terminator writes: 1` + `OK`
- [ ] 反证做过：原始 `4de94476` 文件跑同一断言为 2/2 且 AssertionError，结果写进报告
- [ ] `git diff 4de94476 -- .claude/skills/run-forebrain/fake_provider.py` 只显示删除内层 `else` 的两行 `write_raw`（无其它增删）
- [ ] 范围合规：`git diff --name-only 4de94476` 里本计划只改 `.claude/skills/run-forebrain/fake_provider.py`；若出现其它文件，只能是兄弟计划的 in-scope 文件（340：`pkg/tui/chat_surface_test.go`、`pkg/turn/subagent_approval_test.go`；341：`.claude/skills/run-forebrain/SKILL.md`、`driver.sh`；342：`pkg/gateway/approval.go`、`approval_test.go`），否则 STOP

## STOP conditions

1. Drift check 有输出（`fake_provider.py` 相对 `4de94476` 已改动）。
2. `serve_subagent_ask` 的实际结构与 "Current state" 摘录不符（例如内层 `else` 已有 `return`、或尾部不是共享终止）——停下报告实际代码。
3. Step 2 断言在删行后仍不是 1/1（说明还有第三处终止写入或切函数体的正则没匹配上）——停下报告计数与匹配到的函数体范围。
4. 发现需要改 `serve_subagent_steer` 或共享尾部才能消除双终止——停下报告，不要扩大范围。

## Maintenance notes

- 修后 `serve_subagent_ask` 的四个分支（gate / answered / dispatch / primary-done）统一靠共享尾部 `:1010-1011` 终止一次。
  将来若给某个分支加「提前结束且后面还有代码」的逻辑（像 steer 的 hang），那个分支要自己写终止 + `return`，别再落尾部。
- `write_raw(b"")` 是 chunked 终止块这一事实是隐式契约：任何 `serve_*` 函数都只能在一条响应里写它一次。
  新增模式时照 `serve_subagent_steer` 的形状（提前结束分支 `return`，其余落共享尾部）。
- 本计划只修双终止；`subagent-ask` 模式在 `SKILL.md`/`driver.sh` 的注册缺口由 plan 341 处理，两者互不依赖。
- 同一函数的两段 docstring（模块 `Modes:` 段与 `serve_subagent_ask` 函数注释）原本声称应答请求 "gets the answer text"，
  与代码回固定行 `answer received, subagent done` 不符；owner 2026-11-20 裁决补齐，已随本批改为实际行为（纯注释，
  `py_compile` 过、`serve_subagent_ask` 结构断言仍 1/1）。将来改该模式的应答文案时，这三处（代码、函数 docstring、模块 docstring）要一起改。