# Plan 模式 shell 拦截误判修复计划

状态：待实施
定位日期：2026-09-20
涉及包：`pkg/tool`、`pkg/safety`、`pkg/run`、`pkg/tui`、`frontend`

---

## 1. 结论

截图里的两条命令被拦是**误判**，而且是两类误判里更严重的一类：不是策略判断得严，而是**判据本身问的不是同一个问题**。

- 命令 A：`go env GOPATH GOMODCACHE && go list -m -f '{{.Dir}} {{.Version}}' github.com/modelcontextprotocol/go-sdk 2>/dev/null`
- 命令 B：`git ls-files 'pkg/**/*.go' | awk -F/ '{print $1 "/" $2}' | sort | uniq -c | sort -k2 && git ls-files 'pkg/*/*.go' | awk -F/ '{print $1 "/" $2}' | sort | uniq -c`

两条都是纯读命令，不写工作区任何文件。它们被拒的唯一原因是：`go` 和 `awk` 这两个命令名不在 `pkg/tool/shell_tool.go` 的只读命令白名单里。

与此同时，同一个分类器把 `find . -name '*.go' -delete`、`sed -n '1,5w out.txt' go.mod`、`sed 's/a/b/w out.txt' go.mod` 判成**只读**——也就是说，plan 模式在 `danger-full-access` 下号称的「绝不修改代码库，这是硬保证」当前是不成立的。误判在两个方向上同时存在。

---

## 2. 复现与证据

在 `pkg/tool` 包内直接调用分类器（临时探针，已删除）：

```
readOnly=false  go env GOPATH GOMODCACHE && go list -m -f '{{.Dir}} {{.Version}}' github.com/modelcontextprotocol/go-sdk 2>/dev/null
readOnly=false  go env GOPATH
readOnly=false  go version
readOnly=false  go doc net/http
readOnly=false  git --no-pager ls-files 'pkg/**/*.go' | awk -F/ '{print $1 "/" $2}' | sort | uniq -c | sort -k2 && ...
readOnly=false  awk -F/ '{print $1}' foo.txt
readOnly=false  jq . package.json
readOnly=false  xargs -n1 echo
readOnly=true   git ls-files 'pkg/**/*.go' | sort | uniq -c
readOnly=true   rg TODO internal | head -20
readOnly=true   cat go.mod | grep module
```

反向（写命令被判成只读）：

```
readOnly=true   find . -name '*.go' -delete
readOnly=true   sed -n '1,5w out.txt' go.mod
readOnly=true   sed 's/a/b/w out.txt' go.mod
```

说明管道/`&&` 的拆分、引号扫描、`2>/dev/null` 剥离都正常工作（`git ls-files | sort | uniq -c` 判 true 即为证），问题**只**出在单条命令的判据上。

### 命令 A 的判定路径

`pkg/tool/shell_tool.go:1568` `shellCommandReadOnlyWithinRoots`
→ `stripDevNullRedirects` 去掉 `2>/dev/null`
→ `shellReadOnlyClauses`（`:1799`）按 `&&` 拆成两个 clause
→ 每个 clause 进 `shellSimpleCommandReadOnly`（`:1585`）
→ 命令名 `go` 落到 `default: return false`
→ `guardPlanModeShellCommand`（`:2175`）看到 `readOnlyCommand == false` → 硬拒。

### 命令 B 的判定路径

同上，`|` 也是 clause 分隔符，`git --no-pager ls-files …`、`sort`、`uniq -c`、`sort -k2` 四种 clause 全部判 true，唯独 `awk -F/ '{print $1 "/" $2}'` 落到 `default: return false` → 整条判 false → 硬拒。

---

## 3. 根因

### R1（直接根因）：用「证明不了只读」当硬拒谓词，而契约说的是「会写」

`pkg/run/config.go:875` / `:953` 里 plan 模式提示词对模型的承诺是：

> `- mutating shell commands and write_file/edit_file to other paths: BLOCKED until exit_plan_mode`

拦的应该是 **mutating**。但 `guardPlanModeShellCommand`（`pkg/tool/shell_tool.go:2175`）拦的是 `!readOnlyCommand`。

`shellCommandReadOnlyWithinRoots` 是一个**白名单 + 默认 false** 的分类器，它的 `false` 语义是「我证明不了这是只读」，不是「这会写」。这个分类器原本服务的是**软结果**——`LoadedSkillShellAccessReason`（`pkg/tool/loaded_skills.go:346`）拿它去决定「要不要多弹一次审批」，判错了最多多问一次。plan 模式把同一个 `false` 拿去做**硬结果**，于是「未知」被当成「有害」，所有不在表里的无害命令一律变成拒绝。

这是一个三态问题被压成二值的结构性错误：**只读 / 会写 / 证明不了**，当前只有两态。

### R2：白名单缺口巨大，且两条截图正好各撞一个

`pkg/tool/shell_tool.go:1585-1657` 的表里没有：`awk`、`go`、`jq`、`env`、`printenv`、`column`、`expand`、`fold`、`rev`、`seq`、`expr`、`base64`、`ps`、`sw_vers`、`getconf`、`locale`、`tty`、`comm` 之外的常见只读工具。`git` 子命令也只认 `status/diff/log/show/rev-parse/ls-files/grep/worktree list/branch`，缺 `ls-tree`、`cat-file`、`blame`、`describe`、`shortlog`、`for-each-ref`、`rev-list`、`merge-base`、`name-rev`、`diff-tree`、`show-ref`、`stash list`、`remote -v`、`config --get*`。

只补表是治标：下一个没进表的只读工具（`yq`、`fd`、`bat`、`delta`、`tokei`…）会以完全相同的方式复现。

### R3：同一语义有两份互相漂移的表

- `pkg/safety/shell.go:504` `knownSafeCommand`
- `pkg/tool/shell_tool.go:1585` `shellSimpleCommandReadOnly`

两者都在回答「这条命令是否只读」，但内容已经分叉：

| 差异项 | `pkg/safety` | `pkg/tool` |
| --- | --- | --- |
| `rev` `seq` `expr` `base64` `tac` `numfmt` | 有 | **无** |
| `comm` `hexdump` `objdump` `size` `file` `whereis` `groups` `command` `md5` `sha1sum` `shasum` `cksum` `join` | **无** | 有 |
| `find -delete/-exec` | **拒绝**（`:522`） | **放行**（当只读） |
| `git ls-files/grep/rev-parse/worktree list` | 不认（判不安全） | 认 |
| `git -C <root内路径>` | 一律拒（`gitUnsafeGlobalOptions`） | 校验路径后放行 |
| `sed` | 只认 `sed -n <行号>p` | 只拒 `-i`，`w` 写标志漏网 |

改一处漏一处，`find -delete` 的反向洞正是这条漂移造成的：`pkg/safety` 拦住了，`pkg/tool` 没拦，而 plan 模式用的是后者。

### R4：硬拒违反既定规则，且它自己承诺的审批逃生口不可达

项目的既定规则是「受保护路径/敏感操作一律弹审批交给用户决定，禁止直接硬拒绝」，`98739718 feat(tool): ask about protected paths instead of refusing them` 已经按这条规则改过一次文件写入路径。plan 模式的 shell gate 是同一类里**唯一还在硬拒**的组件。

更关键的是，`guardPlanModeShellCommand` 的注释写着：

> An approval for this exact call is still an escape hatch: the user deliberately allowing one command is a decision the runtime should honor.

但控制流让这个逃生口在**唯一会触发该 guard 的配置下不可达**：

1. guard 只在 `rt.Cfg.DangerFullAccessEnabled()` 时生效（`:2182`）。
2. `danger-full-access` 通常配合自动批准（`ApprovalNever`），此时 `pkg/run/permissions.go` 的中间件走 `BehaviorAllow` 分支，直接 `WithPolicyApproved` 放行，**不创建任何 pending action**。
3. guard 在 `pkg/tool/shell_tool.go:166` 执行，而 shell 自己的 `st.ActionHook()` 在 `:190` 才调用——guard 在前。

于是 `ApprovedActionIDFromContext(ctx)` 永远是空，注释里的逃生口在实际配置里从不存在。用户看到的就是截图那样：没有任何浮层，直接一句红色报错。

### D1（顺带发现）：plan 模式下 Go 缓存能力被整体关掉

`pkg/tool/shell_tool.go:272-274`：

```go
if req.profile == safety.ProfileWorkspaceWrite || (req.profile == safety.ProfileManaged && filesystemAllowsWriteAt(...)) {
    goCache = resolveGoCacheAccess(ctx, rt, req.cwd)
}
```

plan 模式下 `shellSandboxProfile`（`:2136`）返回 `ProfileReadOnly`，因此在**沙箱配置**下 Go 的 `GOCACHE`/`GOMODCACHE`/`GOTMPDIR` 一个都不授予。实测 `go env`、`go list -m`（模块已在本地 mod cache 中）在只读 GOCACHE 下仍可成功，但尚未下载的模块、`go list ./...`、`gofmt -l` 这类需要写构建缓存的读操作会失败。缓存目录都在项目之外，写它们不触碰 plan 模式的承诺。

### D2（顺带发现）：写命令被判成只读

`find . -delete`、`sed … w file`、`sed -n 'Nw file'` 当前判 `readOnly=true`。在 `danger-full-access` + plan 模式下，它们会**不弹审批直接执行**，plan 模式提示词里的「硬保证」当场破功。

### D3（顺带发现，低优先级）：裸 `..` 绕过根校验

`fileArgPathsUnderAllowedRoots` 只校验「看起来像路径」的参数（含 `/` 或以 `./`、`../`、`/` 开头），裸 `..` 不含斜杠被跳过，于是 `ls ..` 判只读通过。`cat ../../x` 会被正确拦下，所以影响面只限于把裸 `..` 当参数的目录列举类命令。

---

## 4. 已确认的设计决策

| # | 决策 | 结论 |
| --- | --- | --- |
| 1 | plan 模式拦截形态 | **分三档**：可证明只读 → 直接执行；可证明会写 → 仍硬拒并提示 `exit_plan_mode`（与 `write_file` 的 `GuardWrite` 一致）；证明不了 → **强制弹审批**交给用户 |
| 2 | 两份只读表 | **合并成一份共享表**，落在 `pkg/safety`，逐条校准后由 `pkg/tool` 复用 |

---

## 5. 目标形态

```
shell 命令
   │
   ├─ 可证明只读 ─────────────────→ 执行，不打扰（今天的两条截图命令走这里）
   │
   ├─ 可证明会写 ─────────────────→ plan 模式硬拒，一句话说明 + 指向 exit_plan_mode
   │                                （find -delete / sed -i / sed w / git commit / go build …）
   │
   └─ 证明不了 ───────────────────→ 强制审批浮层，justification 一句话说清为什么问
                                     批准后按原命令执行；拒绝后把拒绝原因回报给模型
```

三档判据集中在 `pkg/safety`，`pkg/tool` 只做「拿到档位 → 决定行为」。

---

## 6. 任务分解

### P1 — 在 `pkg/safety` 建立唯一的三态分类器

**新增** `pkg/safety/shell_classify.go`（或并入 `shell.go`，按包内文件组织约定择一）：

```go
type ShellMutation int

const (
    ShellMutationReadOnly ShellMutation = iota // 可证明只读
    ShellMutationWrites                        // 可证明会写
    ShellMutationUnproven                      // 证明不了
)

// ClassifyShellCommand 对整条命令分档。allowedRoots/cwd 用于路径归属校验。
func ClassifyShellCommand(cmd string, allowedRoots []string, cwd string) ShellMutation
```

实现要点：

1. **clause 拆分复用现有实现**：把 `pkg/tool/shell_tool.go` 的 `shellReadOnlyClauses`(`:1799`)、`shellHasUnsafeControlOperator`(`:1881`)、`splitShellWords`、`stripDevNullRedirects`(`:1548`)、`shellEnvAssignment` 整体迁到 `pkg/safety`，保持引号/转义扫描逻辑逐字不变（它们已经被现有测试覆盖，且截图 B 证明其正确）。
2. **合档规则**：任一 clause 为 `Writes` → 整条 `Writes`；否则任一 `Unproven` → 整条 `Unproven`；全部 `ReadOnly` → `ReadOnly`。
3. **重定向**：当前 `shellReadOnlyClauses` 遇到裸 `>` / `<` / `` ` `` / `$` 一律 `return nil,false`（退化成整条走单命令分支）。改为显式识别：
   - `>` / `>>` 指向文件 → `Writes`
   - `<` 读入、`2>&1` 之类 fd 复制 → 不影响档位
   - `` ` `` / `$(` 命令替换、`$VAR` 展开 → `Unproven`（不再是"直接 false"）
4. **正向写命令表**（判 `Writes`）：
   `rm rmdir mv cp mkdir touch ln chmod chown chgrp dd truncate tee install patch shred unlink`；
   `sed` 带 `-i`/`--in-place`，或脚本含 `w`/`W` 写标志（含 `s///w file` 形式）；
   `find` 带 `-delete -exec -execdir -ok -okdir -fls -fprint -fprint0 -fprintf`；
   `sort -o/--output`；`base64 -o/--output`；
   `git`：`add commit checkout switch restore reset rm mv merge rebase cherry-pick revert clean apply am push fetch pull clone init gc prune stash(非 list) tag(非 -l/--list) config(非 --get/--get-all/--get-regexp/--list) worktree(非 list) branch(含非只读选项) remote(非 -v/show/get-url) submodule notes replace update-ref symbolic-ref(非只读) filter-branch`；
   `go`：`build test install generate run vet fix clean work mod(非 graph/why/verify) env -w|-u tool`。
5. **只读表**（合并后的唯一一份，判 `ReadOnly`）：
   现有两表的并集，逐条校准；新增 `awk`（见下）、`go`（`env` 不带 `-w/-u`、`version`、`list`、`doc`、`mod graph|why|verify`）、`jq`（不带 `-f`）、`yq`、`column`、`expand`、`unexpand`、`fold`、`rev`、`seq`、`expr`、`base64`（不带写标志）、`printenv`、`ps`、`getconf`、`locale`、`tty`、`sw_vers`、`arch`、`fd`、`bat --plain`、`tokei`、`gofmt -l/-d`（不带 `-w`）。
   `git` 只读子命令补齐：`ls-tree cat-file blame describe shortlog for-each-ref rev-list merge-base name-rev diff-tree show-ref ls-remote stash list remote -v|show|get-url config --get*`。
6. **`awk` 的专门判据**（不可无条件放行——awk 程序里的 `print > "file"`、`print | "cmd"`、`system()` 都能写盘执行）：
   - `-f progfile` → `Unproven`（程序在文件里，读不到）
   - 程序文本包含 `>`、`|`、`system(`、`close(`、`ENVIRON`、`/dev/std` → `Unproven`
   - 其余 → `ReadOnly`，参数按常规做路径归属校验
   截图 B 的 `awk -F/ '{print $1 "/" $2}'` 走这一档判 `ReadOnly`（程序里无 `>`、无 `|`）。
7. **「执行别的命令」的前缀**：`xargs`、`env CMD`、`nice`、`time`、`timeout`、`sudo`、`nohup`、`watch` → 剥掉自身选项后对被执行命令**递归分档**；剥不干净 → `Unproven`。`env` 不带命令（只 `env`/`env -0`）→ `ReadOnly`。
8. **保留现有的路径归属校验**：`fileArgPathsUnderAllowedRoots`、`cdPathUnderAllowedRoots`、`gitPathUnderAllowedRoots` 一并迁入；顺手修 D3——把裸 `..`、`../` 前缀的参数纳入校验。

**旧入口保留为薄封装**（避免一次性改动过大）：

```go
func ShellCommandIsKnownSafe(cmd string) bool // 现有调用点不变，内部改为 Classify == ReadOnly
```

`pkg/tool/shell_tool.go` 的 `shellCommandReadOnlyWithinRoots` 改为 `safety.ClassifyShellCommand(...) == safety.ShellMutationReadOnly`，`shellSimpleCommandReadOnly`/`gitCommandReadOnly`/`sedCommandReadOnly`/`sortCommandReadOnly` 等重复实现**整体删除**（production 无其它调用者即删净，不留兼容壳）。

**验收**：`pkg/safety` 里只有一张只读表和一张写表；`grep -rn '"uniq"' --include='*.go'` 在 production 代码里只剩 `pkg/safety` 一处（`pkg/tool/semantic.go:210` 的分页器列表语义不同，保留）。

---

### P2 — plan 模式 gate 改成三档，`Unproven` 走强制审批

**改** `pkg/tool/shell_tool.go:165-172` 与 `:2175 guardPlanModeShellCommand`：

```go
mutation := safety.ClassifyShellCommand(cmd, shellAllowedRoots(ctx, st, rt), wdAbs)
readOnlyCommand := mutation == safety.ShellMutationReadOnly
planGate := planModeShellGate(ctx, rt, mutation) // refuse / ask / allow
if planGate == planGateRefuse {
    err := fmt.Errorf("plan mode: %q modifies files; call exit_plan_mode first", strings.TrimSpace(cmd))
    CaptureToolError(ctx, err)
    return "", err
}
```

`planGate == planGateAsk` 时，**不新造一条审批路径**，而是并入 `:188` 已有的那条：

```go
planAsk := planGate == planGateAsk
approvalRequired := skillAccess != "" || planAsk || shellAssessmentRequiresApproval(...)
...
if planAsk {
    markPlanModeApproval(payload, fmt.Sprintf(
        "plan mode: 无法证明 %q 只读，批准后才会执行。", firstClauseOf(cmd)))
}
```

新增 `markPlanModeApproval`，形状照搬 `pkg/tool/permissions.go:104 markProtectedApproval`：

```go
payload["force_tool_approval"] = true
payload["approval_reason"] = planModeApprovalReason // "plan_mode_unproven_command"
payload["justification"] = <一句话>
```

`force_tool_approval` 的既有语义（`pkg/run/runner.go:371,415,421`）是：

- `ApprovalOnRequest` / `ApprovalUnlessTrusted` / 命中 allow 规则 → **强制弹浮层**，这就是 R4 里那条失效逃生口的修复；
- `RuntimeYOLOEnabled()` → 全局绕过，直接放行（用户自己开的全局旁路）；
- `ApprovalNever` → `permission denied: approval policy is never`，仍然是拒绝，但这是**用户自选策略**的结果，而不是 gate 自己堵死的路——与项目既定规则一致。

也就是说本期修复对 `ApprovalNever` 用户的收益来自 P1：两条截图命令会落进 `ReadOnly` 档直接执行，压根不进审批分支。

**gate 生效范围保持不变**：仍只在 `rt.Cfg.DangerFullAccessEnabled()` 时启用。沙箱配置下 `ProfileReadOnly` 的 OS 沙箱本来就是真实执行边界，那里不需要也不应该再加一层基于文本的判断（会把沙箱能安全跑的命令误拦）。这一点在 `guardPlanModeShellCommand` 的注释里已有论述，保留并更新措辞。

**文案**：错误与 justification 各一句话，不写成多行说明。

**验收**：
- 截图里的两条命令在 plan 模式 + `danger-full-access` 下直接执行，无浮层、无报错。
- `find . -delete` 在同配置下硬拒，错误一句话且提到 `exit_plan_mode`。
- `python3 script.py` 这类未知命令弹出审批浮层，浮层里带那一句 justification；批准后执行，拒绝后模型收到拒绝原因。

---

### P3 — 两个 surface 都要能显示审批理由

当前两处都**只认** `protected_path` 这一个 tag，新 tag 会导致浮层/行显示成裸 action id：

- `pkg/tui/overlays.go:1675 approvalProtectedReason` — `if !strings.EqualFold(in.ApprovalReason, "protected_path") { return "" }`
- `frontend/src/views/ChatView.vue:1408` — `if (String(payload?.approvalReason ?? '').trim() === 'protected_path')`

**改法（根因式，不是再加一个 tag 分支）**：两处都改成「payload 带 `justification` 就渲染它」，不再枚举 `approval_reason` 取值。`approval_reason` 保留作为分类标签供遥测与文案使用，但不再是渲染的门槛。函数随之改名（`approvalProtectedReason` → `approvalJustification`）。

**验收**：TUI 与 Web 两端的审批浮层/待办行都显示那一句 justification；现有 `protected_path` 审批显示不变（回归）。

---

### P4 — plan 模式下恢复 Go 缓存能力（D1）

**改** `pkg/tool/shell_tool.go:272-274`：`ProfileReadOnly` 也调用 `resolveGoCacheAccess`。缓存目录（`resolveGoCacheAccess` 在非 shared 模式下建的是按 workspace 隔离的 `buildDir/modDir/tmpDir`）全部位于项目之外，授予它们不改变「不修改代码库」的承诺。

**前置验证**（先验证再改，避免改一个不存在的问题）：在沙箱配置（非 danger）下进入 plan 模式，依次跑 `go env GOPATH`、`go list -m all`、`go list ./...`、`gofmt -l pkg/tool`，记录哪些因缓存不可写而失败。只对确实失败的场景做上述授予。

**验收**：plan 模式下 `go list ./...` 与 `gofmt -l` 可正常输出；`go build ./...` 仍按 P2 的 `Writes` 档被拒（它是构建动作，不是只读查询）。

---

### P5 — 更新 plan 模式提示词，让模型知道三档规则

**改** `pkg/run/config.go:875`（subagent 版）与 `:953`（主 agent 版）的这一行：

```
- mutating shell commands and write_file/edit_file to other paths: BLOCKED until exit_plan_mode
```

改成明确的三档表述，例如：

```
- read-only shell commands: free use, no prompt
- shell commands that modify files: BLOCKED until exit_plan_mode
- shell commands whose effect cannot be proven read-only: you will be asked to approve them; proceed if approved
```

同时把 `## Important` 里的「MUST NOT make codebase edits outside the plan directory. This is a hard guarantee.」保留——P2 的 `Writes` 档硬拒 + P1 修掉 D2 的反向洞之后，这句话才第一次真正成立。

---

### P6 — 测试

**单测（表驱动，放 `pkg/safety/shell_classify_test.go`）**

必含回归用例（两张截图的原文，含 rewrite 后形态）：

| 命令 | 期望 |
| --- | --- |
| `go env GOPATH GOMODCACHE && go list -m -f '{{.Dir}} {{.Version}}' github.com/modelcontextprotocol/go-sdk 2>/dev/null` | `ReadOnly` |
| `git --no-pager ls-files 'pkg/**/*.go' \| awk -F/ '{print $1 "/" $2}' \| sort \| uniq -c \| sort -k2 && git --no-pager ls-files 'pkg/*/*.go' \| awk -F/ '{print $1 "/" $2}' \| sort \| uniq -c` | `ReadOnly` |
| `find . -name '*.go' -delete` | `Writes` |
| `sed -n '1,5w out.txt' go.mod` / `sed 's/a/b/w out.txt' go.mod` | `Writes` |
| `sed -i '' s/a/b/ main.go` | `Writes` |
| `go build ./...` / `go test ./...` / `go mod tidy` | `Writes` |
| `go env -w GOFLAGS=-mod=mod` | `Writes` |
| `git commit -m x` / `git checkout -- .` | `Writes` |
| `awk 'BEGIN{print "x" > "/tmp/y"}'` | `Unproven`（含 `>`） |
| `awk -f prog.awk data` | `Unproven` |
| `xargs rm` | `Writes`（递归到 `rm`） |
| `xargs wc -l` | `ReadOnly` |
| `python3 script.py` | `Unproven` |
| `echo x > out.txt` | `Writes` |
| `cat $(ls)` | `Unproven` |
| `ls ..`（D3） | 按根归属判，越界则 `Unproven` |

**行为测（`pkg/tool/shell_output_test.go`，替换现有 `:524-538` 那组）**
- `danger-full-access` + plan：`ReadOnly` 档不触发任何 hook；`Writes` 档返回硬拒错误；`Unproven` 档调用 `ActionHook`，payload 含 `force_tool_approval:true`、`approval_reason`、非空 `justification`。
- `ApprovalOnRequest` / `ApprovalUnlessTrusted` 下 `Unproven` 档**必定**弹审批，不因已有 allow 规则而静默放行（R4 的直接回归）。
- `ApprovalNever` 下 `Unproven` 档返回策略级拒绝（`approval policy is never`），而截图里的两条命令因落在 `ReadOnly` 档仍正常执行。
- 沙箱配置（非 danger）+ plan：三档都不走 gate，交由沙箱裁决（保持现状）。
- 带 `ApprovedActionID` 的 replay：`Writes` 与 `Unproven` 都放行。

**渲染测**：TUI `approvalJustification` 与前端对应逻辑，对 `protected_path` 与新 tag 都返回 justification。

**真机验证（必须做，不能只跑单测）**
用 `run-forebrain` skill 启动真实 TUI，`danger-full-access` + `/plan`：
1. 原样粘贴两条截图命令 → 确认直接出结果，屏幕上无红色报错、无浮层。
2. 跑 `find . -name '*.go' -delete` → 确认硬拒且一句话文案。
3. 跑 `python3 -c "print(1)"` → 确认弹出审批浮层且显示 justification；分别走批准/拒绝两条路径，确认批准后执行、拒绝后模型收到原因。
4. 截图留存，与本文件第 2 节的复现证据配对。

---

## 7. 缓存命中率影响

- P5 改的是 plan 模式 reminder 文案。该 reminder 以 `IsMeta` user message 的形式**追加在消息尾部**（`isPlanReminderMessage`，`pkg/run/config.go:830` 附近），不在 tools/system 前缀里，改文案只在新增位置产生一次新 token，不使既有前缀失效。
- P1/P2/P3/P4 全部不触碰工具定义、system 块与消息装配顺序，对前缀零影响。
- 落地前后各跑一次 `pkg/llm/prefix_fingerprint_test.go` 的 byte-stability golden；如需数字，用 `cmd/cachebaseline` 出改前/改后对照。命中率只能持平或上升。

---

## 8. 风险与缓解

| 风险 | 缓解 |
| --- | --- |
| 合并两表会改变 `unless-trusted` 下的免审批范围（`pkg/run/permissions.go:95` 的 `ShellCommandIsKnownSafe` 分支） | 合并时逐条列出「原来 safety 拒 / 合并后放行」和反向的差集，写进 PR 描述逐条确认；对 `find`、`git -C`、`sed` 这三处已知分叉点取**更严**的一侧 |
| 正向写命令表漏项 → 写命令落进 `Unproven` | 可接受：`Unproven` 会弹审批，不会静默执行；而漏进 `ReadOnly` 才是事故，因此表的默认倾向必须是「拿不准归 `Unproven`，绝不归 `ReadOnly`」 |
| `Unproven` 弹审批过于频繁，打断 plan 流程 | 只读表补齐后常见探索命令都落 `ReadOnly`；上线后收集实际弹审批的命令名，作为下一轮补表依据 |
| `awk` 程序文本扫描过严（含 `>` 即 `Unproven`，比较运算 `$1 > 3` 会误伤） | 接受这一侧误差：误伤的结果是弹一次审批，不是拒绝；不为了少弹一次而放宽到可能写盘 |

---

## 9. 不在本期范围

- 不改 `write_file`/`edit_file` 在 plan 模式下的 `GuardWrite` 行为（那条路径已按既定规则改过，且 plan 目录内写入本就放行）。
- 不改沙箱配置下 plan 模式的 profile 选择（`ProfileReadOnly` 是正确的）；sandbox 层只是给了「只加路径不加 cwd」的能力，plan 模式仍在只读边界内运行。
- 不引入任何防御性兜底：分类器判不出就是 `Unproven`，由用户在审批里裁决，不加静默 fallback。

---

## 10. 改动文件清单

| 文件 | 内容 |
| --- | --- |
| `pkg/safety/sandbox_config.go` | `CommandRequest.ExclusiveWritablePaths`（sandbox 层「只加路径不加 cwd」） |
| `pkg/safety/manager.go` | `applyRuntimeConfigToRequest` 在该标记为真时不并入已配置的可写根 |
| `pkg/safety/shell.go` | 三态分类器、唯一的只读表/写表、clause 拆分与路径校验（从 `pkg/tool` 迁入）；`knownSafeCommand`/`knownSafeGitCommand`/`validSedPrintArgument` 删除 |
| `pkg/tool/shell_tool.go` | `:165-172` 三档接入；`:1548-2031` 一批重复实现删除；`:2175` gate 改造；`:272-274` Go 缓存 + `promoteReadOnlyWriteScope`/`shellSandboxApplied`/`shellProfilePlumbsGoCache` |
| `pkg/tool/permissions.go` | 新增 `markPlanModeApproval` 与 `planModeApprovalReason` |
| `pkg/tool/loaded_skills.go` | `LoadedSkillShellAccessReason` 的 `readOnly` 参数来源改为新分类器结果（语义不变） |
| `pkg/run/config.go` | `:875` `:953` plan 模式提示词三档表述 |
| `pkg/tui/overlays.go` | `approvalProtectedReason` → `approvalJustification`，去掉 tag 白名单 |
| `frontend/src/views/ChatView.vue` | `:1408` 同上 |
| 测试 | `pkg/safety/shell_test.go`、`pkg/tool/shell_output_test.go`、`pkg/tui/overlays_test.go`、前端对应用例；P4 追加 `pkg/safety/manager_test.go`、`pkg/tool/shell_tool_test.go`（`TestShellSandboxApplied` / `TestShellProfilePlumbsGoCache` / `TestPromoteReadOnlyWriteScopeStatesItsOwnWriteSet`） |

---

## 11. Implementation Status

状态：**已实施**。P1/P2/P3/P5/P6 完成于 2026-09-20；P4 于 2026-09-21 补做（先加 sandbox 层的「只加路径不加 cwd」，再按其改 plan 模式的 Go 缓存），全部落地并有真机沙箱证据。

### 落地内容

| 计划项 | 结果 |
| --- | --- |
| P1 三态分类器 | `pkg/safety/shell.go` 末尾新增 `ShellMutation` / `ClassifyShellCommand`（原计划单开 `shell_classify.go`，但 `pkg/architecture` 限定每包 20 个生产文件且 `pkg/safety` 已到上限，故并入该文件）；clause 拆分、转义/引号扫描、fd 复制、`$`/反引号/子壳/`~` 的 floor、`awk`/`sed`/`go`/`git`/wrapper 递归全部在内。`pkg/tool` 侧 588 行重复实现删除；`pkg/safety/shell.go` 的 `knownSafeCommand`/`knownSafeGitCommand`/`validSedPrintArgument` 删除（141 行），`ShellCommandIsKnownSafe` 改为分类器薄封装 |
| P2 三档 gate | `planModeShellGate` 取代 `guardPlanModeShellCommand`；`markPlanModeApproval` 新增（`plan_mode_unproven_command` + 一句话 justification）；`force_tool_approval` 沿用既有语义 |
| P3 两端渲染 | TUI `approvalProtectedReason` → `approvalJustification`（payload 带 `justification` 即渲染，不再枚举 tag）；前端同名函数落在 `approvalSuggestions.ts` 并接进 `ChatView.vue` |
| P5 提示词 | `pkg/run/config.go` 两处 reminder 改三档表述 |
| P6 测试 | `pkg/safety/shell_test.go`（三态表 + 路径/symlink/git -C/一致性）、`pkg/tool/shell_output_test.go`（三档行为、沙箱配置不走 gate、批准后执行）、`pkg/run/permissions_test.go`（on-request / unless-trusted / never / 已有 allow 规则）、`pkg/tui/overlays_test.go`、`pkg/turn/approval_test.go`、前端 `approvalSuggestions.test.ts` |
| P4 Go 缓存 | `pkg/safety/sandbox_config.go` 新增 `CommandRequest.ExclusiveWritablePaths`（sandbox 层支持「只加路径不加 cwd」）+ `manager.go` 据此不并入已配置可写根；`pkg/tool/shell_tool.go` 的 `ProfileReadOnly` 也在有沙箱时解析 Go 缓存，并把提升逻辑抽成 `promoteReadOnlyWriteScope`（提升同时置 `exclusiveWritablePaths` + 把 cwd 补回可读集）；缓存判定改用实际 `Manager.DecideShellCommand(...).UseSandbox`，新增 per-attempt env helper 并删除 `shellSandboxApplied` |

### 计划外但必须修的三处（实施中发现，均已修 + 测）

1. **根路径与 cwd 的符号链接形态不一致**（`pkg/safety/shell.go` `cleanShellAllowedRoots`/`pathUnderAnyShellRoot`/`resolveShellPath`）。
   工具状态的 root 是已 resolve 的（macOS `/var/folders/...` → `/private/var/folders/...`），而 `os.Getwd()` 保留 shell 启动时的写法。纯字符串比较会让**每一条含相对路径参数的命令**判成 `Unproven`——截图里 `github.com/modelcontextprotocol/go-sdk`、`'pkg/**/*.go'` 两个参数正好各撞一次。这是真机验证才暴露的：单测里 cwd 与 root 同一个临时目录，恰好掩盖了它。
2. **审批中间件先于 gate 提问**（`pkg/run/permissions.go`）。
   `danger-full-access` 下没有沙箱，`shellNeedsApprovalDespiteDefault` 认为每条未匹配命令都要问，于是中间件的浮层**先于**工具体的 gate 到达：`find . -delete` 被当成“用户可批准”的命令展示，批准后还会真的执行。现在 gate 非 `Allow` 时中间件直接 `next()`，由工具给出答案（拒绝 / 带 justification 的提问）。新增导出 `tool.PlanModeShellGateForCommand` 保证只有一份判据。
3. **`RequiresActionError.ToolInput` 携带的是 shell 输入结构体而不是审批 payload**（`pkg/tool/shell_tool.go`）。
   TUI 会用 `rae.ToolInput` 重写 wait 行，而 `approval_reason`/`justification`/`force_tool_approval` 只在 payload 里，于是浮层永远显示不出理由。改为携带 payload（同文件里 sandbox-retry 路径本来就是这么做的），`command` 仍是 rewrite 后的形态，前缀记忆的推导不受影响。

### P4（Go 缓存）：为什么不能照原方案做，以及最终怎么做

第一轮的前置验证（真机 seatbelt，临时探针已删除）：

- `ProfileReadOnly`：`go list ./...` 失败（`open /Users/.../Library/Caches/go-build/...: operation not permitted`）；`go env`、`gofmt -l pkg/tool` 成功。
- 把缓存目录作为 `AdditionalWritablePaths` 传给 `ProfileReadOnly` 无效——`constrainRequestToFilesystemProfile` 对 read-only 会清空该字段。
- 升到 `ProfileManaged` + 缓存目录：`go list ./...` 成功，**同时 `printf evil > zz-probe-write.txt`（仓库根目录）也成功**。
  原因：`Manager.RunCommand` → `applyRuntimeConfigToRequest` 会把 `runtimeCfg.Filesystem.AllowWrite` 并入请求，而 `ConvertToRuntimeConfig` 在 workspace-write 且未激活具名 permission profile 时把它填成 **cwd + temp + /tmp**。所以“只授予缓存目录”实际是“授予整个工作区”。这同时暴露一个**既存**问题：`read-only → managed` 提升（会话写入授权）本来就会连带放开整个工作区。

所以分两步做，而不是只改 shell 工具：

**第一步（sandbox 层）** `pkg/safety/sandbox_config.go` 新增 `CommandRequest.ExclusiveWritablePaths`；`pkg/safety/manager.go` 的 `applyRuntimeConfigToRequest` 在该标记为真时不再并入 `runtimeCfg.Filesystem.AllowWrite`（拒绝写清单不受影响）。语义是「本次请求自己解析出的写集合就是完整策略」。这个落点是对的：`constrainRequestToFilesystemProfile` 对 `ProfileManaged` 是空操作，所以标记能一路到达三个平台的 backend；darwin/linux/windows 三个 backend 的可写路径都只来自 `req.AdditionalWritablePaths`（`ProfileWorkspaceWrite` 才额外补 cwd/temp），标记因此在三平台等效。

**第二步（工具层）** `pkg/tool/shell_tool.go`：

- `shellProfilePlumbsGoCache(profile, sandboxApplied, writableRoots, cwd)` 取代原先的内联条件：`WorkspaceWrite`、`Managed`、`ReadOnly` 都只有在本次请求的 `UseSandbox=true` 时才解析缓存；`Managed` 仍需能写 cwd；`ReadOnly` 在有沙箱时解析缓存（D1 修复点；无沙箱时不解析，避免把 `go` 从宿主热缓存挪到冷缓存）。本次 shell attempt 直接调用 `Manager.DecideShellCommand(...).UseSandbox`，不再保留 runtime 级 `shellSandboxApplied` 近似判定。
- `promoteReadOnlyWriteScope` 抽出原来的 read-only → managed 提升：除了换 profile，还置 `exclusiveWritablePaths = true`（否则提升等于把工作区一并授予），并把 `req.cwd` 补进可读集（read-only 的 backend 会隐式让工作目录可读，managed 不会）。提升后写集合 = 结构化授权 + Go 缓存目录，**不含工作区、不含 TMPDIR/`/tmp`**。

**真机验收**（`pkg/tool` 临时探针 `zz_p4_probe_test.go`，`FOREBRAIN_ZZ_PROBE=1`，真实 seatbelt，配置 `workspace-write`；探针跑完已删除）：

| 场景 | 结果 |
| --- | --- |
| 反例：managed + 缓存写集合，**不带标记**，`printf evil > zz-probe-write.txt`（cwd=工作区） | exit 0，工作区文件**被写成功** → 标记确实是拦住它的那一环 |
| 正例：同一请求 **带标记** | 工作区与另一个临时目录都是 `Operation not permitted`，缓存目录可写 → 边界成立 |
| 修复前：`ProfileReadOnly` + 默认（isolated）缓存 env，`go list ./...` | exit 0 但 stderr `failed to initialize build cache at <home>/cache/go/<key>/build: mkdir .../00: operation not permitted` → 构建缓存整体不可用（D1 现象） |
| 修复后：`ProfileManaged` + 标记 + isolated 缓存目录，`go list ./...` | exit 0，无权限报错（首次会经 `file://` 本地 proxy 拉取模块） |
| 修复后：同形状 `go vet ./pkg/safety` | exit 0，stderr 干净 |
| 端到端（真工具 `Handle()`，plan 模式 + 沙箱，`go env GOCACHE && go list ./...`） | 默认配置：`exit=0`，`GOCACHE` = 被授予的 isolated 目录，无权限报错；把 `go_cache_mode` 设为 `disabled`（等价于不给缓存）：`pattern ./...: open /Users/.../Library/Caches/go-build/cb/...: operation not permitted`，`exit=1` —— 正是第一轮记录的症状，且 exit≠0 会被 `IsLikelySandboxDenied` 认成沙箱拒绝 |

### 遗留（已确认，未修，均非本改动引入）

- `maybePruneGoCaches` 清理 isolated 模块缓存时会遇到只读目录（探针里 `TempDir` 清理报 `permission denied`），与 P4 无关。


### 共享表合并的语义差集（P1 风险表要求的逐条确认）

变宽（原本要问/被拒，现在算只读）：`env CMD` 前缀（`LANG=C ls`）、`git ls-files|grep|rev-parse|worktree list`、`git -C <root 内路径>`、除 `-n Np` 之外的普通 `sed` 脚本，以及新增的 `awk`/`jq`/`yq`/`go env|version|list|doc|mod graph|why|verify`/`gofmt -l -d`/`fd`/`bat`/`tokei`/`column`/`expand`/… 与 `command -v x` 的递归。

变严（原本会被当只读放行）：`find -delete/-exec/-execdir/-ok/-okdir/-fls/-fprint*` → `Writes`；`sed -i` / `sed` 脚本里的 `w`/`W`（含 `s///w file`）→ `Writes`；`sed -f` → `Unproven`；精确 `--output` → `Writes`，`--files0-from*` → `Unproven`；双引号内的 `$VAR`/反引号 → `Unproven`（原本是 ReadOnly）；裸 `..` 纳入根校验；词首 `~` → `Unproven`。

### 真机验证（`run-forebrain` driver + fake provider，`danger-full-access` + plan 模式会话）

| 场景 | 证据 |
| --- | --- |
| `approval_policy: never`：截图命令 A（`go env … && go list -m … 2>/dev/null`） | `● Ran go env GOPATH GOMODCACHE && go list -m …`，输出 `/Users/doudou/go` / `…/go-sdk@v1.5.0 v1.5.0`；无浮层、无报错 |
| `approval_policy: never`：截图命令 B（`git ls-files … \| awk … \| sort \| uniq -c`） | `● Ran git --no-pager ls-files …`，输出计数；无浮层、无报错 |
| `approval_policy: on-request`：`find . -name '*.go' -delete` | `● Failed to run … └ plan mode: "find . -name '*.go' -delete" modifies files; call exit_plan_mode first`；未弹浮层，工作区文件未被删除 |
| `approval_policy: on-request`：`python3 -c "print(1)"` | 浮层显示 `Command:` + `Reason: Plan mode is active: this command could not be proven read-only, so it will run only if you allow it.`；选 1 批准后 `● Ran python3 -c "print(1)"` → `1`；选 3 拒绝后模型收到 `The user canceled this call before it ran.` |
| `approval_policy: never`：`python3 -c …` | `permission denied: approval policy is never`（用户自选策略的结果，非 gate 堵死） |
| 会话状态 | 真机会话是用 `forebrain resume` 载入并预置 `state/modes/<sid>.json` 的 plan 模式会话（driver 的 home 在校验后被复用；新三档文案已确认注入并落库：`fb_messages` id=5 含 “- read-only shell commands: free use, no prompt / - shell commands that modify files: BLOCKED until exit_plan_mode / - shell commands whose effect cannot be proven read-only: you will be asked to approve them”） |

### 验证命令

- `go build ./...`、`go vet ./...`、`gofmt -l pkg/ cmd/`（无输出）
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/safety ./pkg/tool ./pkg/turn ./pkg/run ./pkg/tui ./pkg/session ./pkg/architecture -count=1` → 全绿（P4 之后的重跑）
- `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → 仅 `pkg/gateway` 的 `TestProjectScopedSkillsWriteIntoTheirOwnProject` 失败；已在干净 HEAD 上 `git stash` 复现，为**既存失败**，与本改动无关
- `cd frontend && npx vitest run` → 97 passed（P4 未触及前端；该结果仍是 P3 那次的）

### 与计划的措辞差异

- P4 按「先 sandbox 层、再工具层」两步实施（见上），而不是只改 shell 工具的条件行。
- 已批准的 replay：`Writes` 档仍放行（计划 P6 明确要求，且 gate 既有注释把「用户明确批准一条命令」当逃生口）。因此 on-request 下用户批准 `find -delete` 会让它执行；`never`/`yolo` 下不会。若要把 `Writes` 做成与 `GuardWrite` 一样**不可批准**，需单独决策——已按计划原文保留放宽的一侧。
