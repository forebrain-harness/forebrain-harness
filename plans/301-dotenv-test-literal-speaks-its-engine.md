# Plan 301: dotenv 迁移测试的裸 SQLite 字面量包上 dbx.Rebind（修复 248 不变量回归）

> **Executor instructions**: 按步骤顺序执行，每步的验证命令通过后再做下一步。遇到
> 「STOP conditions」里的情况就停下来报告，不要自己发挥。**不要 commit / push / merge**：
> 改动留在工作区里，做完报告（仓库主人手动提交）。做完后把 `plans/README.md` 里本计划那一行
> 的状态改成 DONE，并附一句执行摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> ```sh
> git rev-parse --short HEAD   # 计划基线 ecc4e1aa（dev）；后代提交亦可，按内容核对
> grep -n 'INSERT INTO fb_project_channels' pkg/process/dotenv_migrate_test.go
> ```
> 期望恰好一处命中：`TestDotEnvMigrateTurnsChannelSecretsIntoPlaintext` 里的
> `dbx.Exec(ctx, db, \`INSERT INTO fb_project_channels … VALUES (?,?,?,?,?,?,?)\`, …)`。
> 基线 HEAD 上它在 :171；**工作区可能有另一会话的未提交改动把它移到别的行号（起草时在 :263）——
> 行号漂移不算漂移，按内容定位**。若该语句已经被 `dbx.Rebind` 包住（别人顺手修了），STOP 并报告，不要重复改。
> 该文件里别人的未提交改动一律保留，只动本计划点名的这一条语句。

## Status

- **Priority**: P1（248 的 owner 裁令「必须穷举所有同类问题，全部修复」的不变量今天在 HEAD 上是红的）
- **Effort**: S（一条语句 + 一个扫描门）
- **Risk**: LOW（纯测试改动，语义不变：SQLite 上 Rebind 是恒等变换）
- **Depends on**: none（248 的修复模式已入库，照抄即可）
- **Category**: Tests (multi-db)
- **Planned at**: commit `ecc4e1aa`（dev），2026-11-12，`improve reconcile` 发现并立项

## Why this matters

计划 248（`248-every-test-literal-speaks-its-engine.md`，已落地）按 owner 裁令把仓库里所有
「测试自己发原始 SQL 时用 SQLite 的 `?` 占位符」的字面量统一包上
`dbx.Rebind(dbx.DialectOf(<同一句柄>), …)`，并以一个枚举脚本 `TOTAL 0` 为不变量。
2026-11-06 分支审计复跑该脚本得 `TOTAL 0`。

**2026-11-12 reconcile 在 HEAD `ecc4e1aa` 上复跑同一脚本得 `TOTAL 1`**：
`990ec9dd`（plan 268「remove-home-dotenv」随模型配置批量落地）新增的测试
`TestDotEnvMigrateTurnsChannelSecretsIntoPlaintext` 带进了一条未包裹的字面量。
工作区（含另一会话未提交改动）复跑同样是 `TOTAL 1`，同一条语句。

事实核定（决定修法边界，执行者不需要重查）：

- 该测试的建库助手 `dotEnvHomeForTest`（`pkg/process/dotenv_migrate_test.go:19`）走
  `state.OpenStateFromHome(ctx, home, "")` → `OpenFromConfig` 空 URL → **恒开 SQLite**
  （`pkg/state/db.go:233-236`、`:73-76`）；它不读 `FOREBRAIN_TEST_DATABASE_URL`
  （只有 `OpenStateForTest` 读，`db.go:250`）。所以这条语句**今天不会在 MySQL/PG 上跑**，
  不是现网矩阵红灯——但它是 248 明令消灭的「今天不红但同类」形状，且让 248 的扫描门变红。
- `dbx.Exec`（`pkg/state/dbx/tx.go:144`）**不做自动 rebind**：`q` 原样进 `tx.ExecContext`。
  一旦将来有人把该测试接到引擎矩阵，MySQL/PG 会立刻 `SQLSTATE 42601`（247 实测过的失败类）。
- `scripts/test-db.sh` 的 `engine_arms` 里 `./pkg/process` 行过滤是
  `Lease|ImportLegacy|ConfigDB|Startup|PeerInbound|LLMModelsMigrate|UpsertLLMModel`，
  不含 `DotEnvMigrate`。**不要加**：248 已裁决「不加宽 engine_arms（钉死测试入行是同义反复）」，
  且该助手不读测试 URL，入行也只会在 SQLite 上再跑一遍。

## Current state

`pkg/process/dotenv_migrate_test.go`（HEAD :171 附近；工作区行号可能不同）：

```go
	insertChannel := func(kind, configJSON, accountKey string) {
		t.Helper()
		if _, err := dbx.Exec(ctx, db, `INSERT INTO fb_project_channels
(project_id, kind, enabled, config_json, account_key, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
			project.ID, kind, 1, configJSON, accountKey, 1, 1); err != nil {
			t.Fatalf("insert channel %s: %v", kind, err)
		}
	}
```

仓库既有修复样板（248 落地，`pkg/process/config_db_test.go:53`）：

```go
		dbx.Rebind(dbx.DialectOf(env.SQL), `INSERT INTO fb_config (section, value_json, updated_at_ms) VALUES(?,?,0)
```

即：字面量外包 `dbx.Rebind(dbx.DialectOf(<同一句柄>), …)`，句柄用该语句实际执行所用的 `*sql.DB`。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 定向测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run '^TestDotEnvMigrate' -count=1` | ok |
| 包回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/process -count=1` | ok（本机基线若有既有红灯，逐条对照 `plans/README.md` 顶部 2026-11-11 条目的基线名单，不得新增） |
| 构建 | `go build ./...` | exit 0 |

## Scope

**In scope**：`pkg/process/dotenv_migrate_test.go` 里点名的那一条 INSERT 语句（仅此一处）。
**Out of scope**：该文件其它任何改动（含另一会话的未提交工作）；`dotEnvHomeForTest` 助手
（不改成引擎感知）；`scripts/test-db.sh`（不加宽 engine_arms）；任何生产代码；248 计划文件本身。

## Steps

### Step 1: 包裹字面量

把 Current state 里的语句改成（句柄就是同一作用域里的 `db`）：

```go
		if _, err := dbx.Exec(ctx, db, dbx.Rebind(dbx.DialectOf(db), `INSERT INTO fb_project_channels
(project_id, kind, enabled, config_json, account_key, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`),
			project.ID, kind, 1, configJSON, accountKey, 1, 1); err != nil {
```

SQL 文本、参数、错误处理一个字节不动；只加包裹。`dbx` 已在该文件 import（现语句就在用 `dbx.Exec`）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run '^TestDotEnvMigrate' -count=1` → ok。

### Step 2: 不变量扫描归零

把下面的脚本存成 `/tmp/248-sweep.py`（与 248 计划内联的脚本逐字同源），跑
`python3 /tmp/248-sweep.py <repo-root>`：

```python
import os, re, sys
root=sys.argv[1]
skip={'.git','node_modules','frontend','third_party','build','dist','vendor','docs','plans'}
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

**Verify**: 输出 `TOTAL 0`。若出现**别的文件**的命中（不是本计划这条），STOP 并列出——那是另一处回归，不要顺手修。

### Step 3: 回归与构建

**Verify**: `go build ./...` exit 0；`CGO_ENABLED=1 go test -tags fts5 ./pkg/process -count=1` ok（或基线等价，见 Commands 表）。

## Done criteria

- [ ] `grep -c 'dbx.Exec(ctx, db, `INSERT' pkg/process/dotenv_migrate_test.go` → `0`
- [ ] `grep -c 'dbx.Rebind(dbx.DialectOf(db), `INSERT INTO fb_project_channels' pkg/process/dotenv_migrate_test.go` → `1`
- [ ] Step 2 脚本输出 `TOTAL 0`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run '^TestDotEnvMigrate' -count=1` → ok
- [ ] `go build ./...` exit 0
- [ ] `git diff --stat` 只出现 `pkg/process/dotenv_migrate_test.go`（叠加在既有未提交改动之上时，用 `git diff` 逐段确认新增改动只有这一处包裹）
- [ ] `plans/README.md` 本行已更新

## STOP conditions

- Drift check 发现该语句已被别人包好，或该测试已被删除/改名。
- Step 2 扫描出现其它文件的命中。
- 同一验证命令连续两次失败且原因不明。
- 发现需要改 `dotEnvHomeForTest`、`dbx.Exec` 或 `engine_arms` 才能通过——这些都超出范围，停下报告。

## Maintenance notes

- 248 的不变量靠这个扫描脚本守住；任何新增测试里「`.Exec/.Query…(` 后第一个参数直接是含 `?` 的裸字面量」都会让它变红。新增 DB 测试照 `dbx.Rebind(dbx.DialectOf(db), …)` 样板写。
- 若将来把 dotenv 迁移测试接进引擎矩阵（改助手读 `FOREBRAIN_TEST_DATABASE_URL`），须同时在 `scripts/test-db.sh` 的 `engine_arms` 加 `DotEnvMigrate` 片段——那是另一个计划的事，本计划不做。