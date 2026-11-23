# Plan 317: volcengine/workbuddy 测试的 6 条裸 SQLite 字面量包上 dbx.Rebind（248 不变量第二轮回归）

> **Executor instructions**: 按步骤顺序执行，每步的验证命令通过后再做下一步。遇到
> 「STOP conditions」里的情况就停下来报告，不要自己发挥。**不要 commit / push / merge**：
> 改动留在工作区里，做完报告（仓库主人手动提交）。做完后把 `plans/README.md` 里本计划那一行
> 的状态改成 DONE，并附一句执行摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> ```sh
> git rev-parse --short HEAD   # 计划基线 fcb241bc（dev）；后代提交亦可，按内容核对
> grep -n -e 'FROM fb_llm_models WHERE id = ?' -e 'FROM fb_llm_models WHERE source = ?' \
>   -e 'from fb_llm_models where id = ?' \
>   pkg/subscription/volcengine_test.go plugins/subscriptions/workbuddy/workbuddy_test.go
> ```
> 期望恰好 6 处命中（字面量行 volcengine_test.go :332/:363/:399/:435/:459、workbuddy_test.go :1367；
> Step 2 扫描器数的是调用行，各比字面量行小 1：331/362/398/434/458/1366。
> 行号漂移不算漂移，按内容定位）。若某条已被 `dbx.Rebind` 包住（别人顺手修了），跳过该条并报告；
> 若 6 条全部已包好，STOP 并报告，不要重复改。两文件里别人的未提交改动一律保留。
> 另跑下文 Step 2 的扫描脚本（skip 集含 `.worktrees`）：期望 `TOTAL 6`，且命中全部在这两个文件。
> 出现别的文件的命中＝另一处回归，STOP 列出，不要顺手修。

## Status

- **Priority**: P1（248 的 owner 裁令「必须穷举所有同类问题，全部修复」的不变量今天在 HEAD 上是红的）
- **Effort**: S（6 条语句包裹 + 2 个 import + 扫描门）
- **Risk**: LOW（纯测试改动，语义不变：SQLite 上 Rebind 是恒等变换）
- **Depends on**: none（248/301 的修复模式已入库，照抄即可）
- **Category**: Tests (multi-db)
- **Planned at**: commit `fcb241bc`（dev），2026-11-17，`audit reconcile` 发现并立项

## Why this matters

计划 248 按 owner 裁令把仓库里所有「测试自己发原始 SQL 时用 SQLite 的 `?` 占位符」的字面量
统一包上 `dbx.Rebind(dbx.DialectOf(<同一句柄>), …)`，并以枚举脚本 `TOTAL 0` 为不变量。
301 修掉了第一轮回归（plan 268 带进的 1 条，已落地核实：`pkg/process/dotenv_migrate_test.go:263`）。

**2026-11-17 reconcile 在 HEAD `fcb241bc` 主树复跑同一脚本得 `TOTAL 6`**（跳过 `.worktrees`
副本后；不跳会把两个 worktree 各数一遍得 TOTAL 18 假象）：

- `pkg/subscription/volcengine_test.go:331/362/398/434/458` —— plan 281（volcengine 订阅登录，
  随 `09b44fe3` 落 dev）带进的 5 条 SELECT。
- `plugins/subscriptions/workbuddy/workbuddy_test.go:1366` —— workbuddy 插件 in-tree 提交
  （`90f20e9c` 族）带进的 1 条 select。

事实核定（决定修法边界，执行者不需要重查）：

- 两文件的建库助手都走 `state.OpenStateForTest`（volcengine_test.go:75、workbuddy_test.go:75）——
  **引擎感知**（读 `FOREBRAIN_TEST_DATABASE_URL`），与 301 那条恒 SQLite 的不同：一旦有人把这两包
  接进矩阵或带 URL 单跑，MySQL/PG 立刻 `SQLSTATE 42601`（247 实测过的失败类）。
- 但 `scripts/test-db.sh` 的 `engine_arms` 不含 `./pkg/subscription` 与 `./plugins/...`，
  所以**今天不是现网矩阵红灯**——是 248 明令消灭的「今天不红但同类」形状，且让扫描门变红。
- **不要加宽 engine_arms**：248 已裁决；把这两包入行是另一个决策（涉及插件包的矩阵归属），本计划不做。
- `dbx.Rebind`/`dbx.DialectOf` 在 `pkg/state/dbx`；两文件目前都**没有** import dbx，需新增
  `github.com/forebrain-harness/forebrain-harness/pkg/state/dbx`（同一 Go module，plugins/ 无独立 go.mod）。

## Current state（2026-11-17 reconcile 活体摘录）

volcengine_test.go 的 5 条形如（句柄都是 `env.DB`）：

```go
	require.NoError(t, env.DB.QueryRowContext(context.Background(),
		`SELECT api_key, source, source_plan, base_url FROM fb_llm_models WHERE id = ?`, ids[0]).
		Scan(&key, &source, &plan, &baseURL))
```

（其余 4 条：`SELECT api_key FROM fb_llm_models WHERE id = ?` ×2、
`SELECT api_key, source_plan FROM fb_llm_models WHERE id = ?`、
`countVolcengineRows` 里的 `SELECT COUNT(*) FROM fb_llm_models WHERE source = ?`。）

workbuddy_test.go:1366（句柄是该测试作用域里的 `db`）：

```go
	if err := db.QueryRowContext(context.Background(),
		`select provider, model, api_key, source from fb_llm_models where id = ?`, ids[0]).
		Scan(&provider, &model, &apiKey, &source); err != nil {
```

仓库既有修复样板（301 落地，`pkg/process/dotenv_migrate_test.go:263`）：

```go
		if _, err := dbx.Exec(ctx, db, dbx.Rebind(dbx.DialectOf(db), `INSERT INTO fb_project_channels
```

即：字面量外包 `dbx.Rebind(dbx.DialectOf(<同一句柄>), …)`，句柄用该语句实际执行所用的 `*sql.DB`。

## Commands you will need

```sh
export CGO_ENABLED=1
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
export FOREBRAIN_HOME="$(mktemp -d)"   # 每个 go test 前缀scratch home，绝不继承真实安装
```

| Purpose | Command | Expected |
|---|---|---|
| 定向测试（volcengine） | `go test -tags fts5 ./pkg/subscription -run 'Volcengine' -count=1 -v` | 命中测试且全 `--- PASS`（裸 `\|` 会假绿，禁用） |
| 定向测试（workbuddy） | `go test -tags fts5 ./plugins/subscriptions/workbuddy -run 'TestProvisionWritesLoginBackedRows' -count=1 -v` | `--- PASS` |
| 包回归 | `go test -tags fts5 ./pkg/subscription ./plugins/subscriptions/workbuddy -count=1` | ok |
| 构建 | `go build -tags fts5 ./...` | exit 0 |
| 格式 | `gofmt -l pkg/subscription plugins` | 空 |

## Scope

**In scope**：`pkg/subscription/volcengine_test.go` 点名的 5 条 SELECT、
`plugins/subscriptions/workbuddy/workbuddy_test.go` 点名的 1 条 select、两文件各自的 dbx import。
**Out of scope**：两文件其它任何改动（含并行会话的未提交工作）；建库助手；`scripts/test-db.sh`
（不加宽 engine_arms）；任何生产代码；248/301 计划文件本身；`.worktrees/` 里的副本
（312 的未合并改动与 peer worktree 会随各自主人收尾，不要动）。

## Steps

### Step 1: 包裹 6 条字面量 + 补 import

每条只加包裹，SQL 文本、参数、错误处理一个字节不动。volcengine 5 条的句柄是 `env.DB`：

```go
	require.NoError(t, env.DB.QueryRowContext(context.Background(),
		dbx.Rebind(dbx.DialectOf(env.DB), `SELECT api_key, source, source_plan, base_url FROM fb_llm_models WHERE id = ?`), ids[0]).
		Scan(&key, &source, &plan, &baseURL))
```

workbuddy 1 条的句柄是 `db`：

```go
	if err := db.QueryRowContext(context.Background(),
		dbx.Rebind(dbx.DialectOf(db), `select provider, model, api_key, source from fb_llm_models where id = ?`), ids[0]).
		Scan(&provider, &model, &apiKey, &source); err != nil {
```

两文件 import 块各加 `"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"`（按既有分组排序）。

**Verify**: Commands 表两条定向测试 → `--- PASS`；`gofmt -l pkg/subscription plugins` → 空。

### Step 2: 不变量扫描归零（修正版脚本）

把下面的脚本存成 `/tmp/248-sweep.py` 跑 `python3 /tmp/248-sweep.py <repo-root>`。
与 248/301 内联脚本同源，**唯一差别＝skip 集补 `.worktrees`**（主树下的 git worktree 副本
不是本仓测试面，不跳会把每条命中数 N 遍）：

```python
import os, re, sys
root=sys.argv[1]
skip={'.git','.worktrees','node_modules','frontend','third_party','build','dist','vendor','docs','plans'}
hits=[]
for dirpath,dirnames,filenames in os.walk(root):
    dirnames[:]=[d for d in dirnames if d not in skip]
    for fn in filenames:
        if not fn.endswith('_test.go'): continue
        p=os.path.join(dirpath,fn)
        text=open(p,encoding='utf-8',errors='replace').read()
        func_starts=[(m.start(),m.group(1)) for m in re.finditer(r'^func (?:\([^)]*\) )?(\w+)\(',text,re.M)]
        for m in re.finditer(r'\.(QueryRow|QueryRowContext|Query|QueryContext|Exec|ExecContext|Prepare|PrepareContext)\(',text):
            i=m.end(); depth=1; j=i; lit=None
            while j<len(text) and depth>0:
                c=text[j]
                if c=='`':
                    k=text.find('`',j+1)
                    if k==-1: break
                    if depth==1 and '?' in text[j+1:k]: lit=text[j+1:k]; break
                    j=k+1; continue
                if c=='"':
                    k=j+1
                    while k<len(text):
                        if text[k]=='\\': k+=2; continue
                        if text[k]=='"': break
                        k+=1
                    if depth==1 and '?' in text[j+1:k]: lit=text[j+1:k]; break
                    j=k+1; continue
                if c=='(': depth+=1
                elif c==')':
                    depth-=1
                    if depth==0: break
                j+=1
            if lit:
                fn_name=''
                for pos,name in func_starts:
                    if pos<=m.start(): fn_name=name
                    else: break
                hits.append((os.path.relpath(p,root),text.count('\n',0,m.start())+1,m.group(1),fn_name,' '.join(lit.split())[:60]))
sm=open(os.path.join(root,'pkg/state/schema_migrations_test.go'),encoding='utf-8',errors='replace').read()
if 'stmt := `INSERT INTO fb_config(section, value_json, updated_at_ms) VALUES(?,?,10)`' in sm:
    hits.append(('pkg/state/schema_migrations_test.go',3676,'var','','(indirect) INSERT INTO fb_config'))
hits.sort()
for rel,line,meth,fn_name,stmt in hits: print(f"{rel}:{line}\t{fn_name}\t{stmt}")
print('TOTAL',len(hits))
```

**Verify**: 输出 `TOTAL 0`。出现别的文件的命中＝另一处回归，STOP 列出，不要顺手修。

### Step 3: 引擎腿证明（Docker 可用时）与回归

两文件的助手读 `FOREBRAIN_TEST_DATABASE_URL`，所以可以不经 engine_arms 直接证明包裹有效：

```sh
scripts/test-db.sh up
# 连接串取 scripts/test-db.sh 顶部既有的 mysql_url / postgres_url 变量值
# （HEAD 版 :28/:29；脚本没有 url 子命令，不要改脚本，也不要把值写进报告/计划）：
PG_URL="$(sed -n "s/^postgres_url='\(.*\)'/\1/p" scripts/test-db.sh)"
MY_URL="$(sed -n "s/^mysql_url='\(.*\)'/\1/p" scripts/test-db.sh)"
FOREBRAIN_TEST_DATABASE_URL="$PG_URL" go test -tags fts5 ./pkg/subscription -run 'Volcengine' -count=1
FOREBRAIN_TEST_DATABASE_URL="$MY_URL" go test -tags fts5 ./plugins/subscriptions/workbuddy -run 'TestProvisionWritesLoginBackedRows' -count=1
```

（环境变量名核定：`pkg/state/db.go:240` `testDatabaseURLVar = "FOREBRAIN_TEST_DATABASE_URL"`，
`OpenStateForTest` 读它。主树 `scripts/test-db.sh` 若有并行会话未提交改动，以不改动它为前提取变量。）
**负控制**：临时去掉 workbuddy 那条的包裹，同一 PG URL 下该测试必须红（`SQLSTATE 42601`），恢复后绿；
把红/绿输出写进报告。Docker 不可用则本步记「未验」，不阻塞 DONE 的其余判据（与 248/301 先例一致），
但必须在报告与 README 行里写明。

**Verify**: `go build -tags fts5 ./...` exit 0；两包回归 ok；`gofmt -l pkg/subscription plugins` 空。

## Done criteria

- [ ] Drift check 的 6 处命中全部包上 `dbx.Rebind(dbx.DialectOf(<同一句柄>), …)`
- [ ] Step 2 扫描输出 `TOTAL 0`
- [ ] 两条定向测试 `-v` 见 `--- PASS`；两包 `-count=1` ok
- [ ] `go build -tags fts5 ./...` exit 0；`gofmt -l pkg/subscription plugins` 空
- [ ] `git diff --stat` 新增改动只有这两个测试文件（叠加在既有未提交改动之上时逐段核对）
- [ ] Docker 可用时：PG/MySQL 腿绿 + 负控制红→绿留痕；不可用时报告写明未验
- [ ] `plans/README.md` 本行已更新

## STOP conditions

- Drift check 发现 6 条已全部被别人包好，或测试被删除/改名。
- Step 2 扫描出现其它文件的命中。
- 同一验证命令连续两次失败且原因不明。
- 发现需要改建库助手、`dbx` 或 `engine_arms` 才能通过——超出范围，停下报告。
- 主树出现归属不明的新未提交改动且与本计划文件重叠——先经 peer 确认归属，不回退不提交。

## Maintenance notes

- 248 的不变量靠扫描脚本守住；**脚本 skip 集必须含 `.worktrees`**（本计划携带修正版；248/301 内联的是历史版）。
  任何新增测试里「`.Exec/.Query…(` 后第一个参数直接是含 `?` 的裸字面量」都会让它变红。
- 订阅/插件族测试在批量落地时反复带进同类字面量（268→301、281/workbuddy→317）；评审新订阅插件计划时
  把「测试 SQL 一律 dbx.Rebind」写进验收判据，从源头止住第三轮回归。
- 若将来把 `pkg/subscription`/插件包接进引擎矩阵（engine_arms 加行），是另一个计划的事，本计划不做。