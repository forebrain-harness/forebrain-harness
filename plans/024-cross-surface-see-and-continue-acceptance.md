# Plan 024（验收，2026-10-11 按项目模型改写，并入原 031）：两端同一份数据——互相看到、续聊、另一端在跑时拒绝；工具在项目根执行；信任按项目根自己的；工作台以项目根为根

> **执行者须知**：这是一份**验收计划**：只用隔离 home 跑真机流程、记录结果；**不改源码**，发现问题就停下报告。
> 脚本与探针放 scratchpad 或 `$TMPDIR`，**不提交、跑完删除**。**不要更新 `plans/README.md`**。**禁止提交**。**绝不碰 owner 的真实 `~/.forebrain`**。
> 需要真实模型的段落用 glm-5.3（key 从仓库 `.env` 注入，见记忆 real-model-acceptance），其余用 driver 的 fake provider。
>
> **前置（先查）**：下列计划已执行（各自的 Done criteria 已过）：
> - 014–018、020、021；
> - 022（改写版）、025、026、027、028、029。
>
> 逐项 grep 确认：
> - `grep -n '"turn-started:"' pkg/tui/*.go`（014）；
> - `grep -n 'first_token_ms' pkg/state/schema.sql`（015）；
> - `grep -n 'func IsPushOnlyStreamEvent' pkg/event/run_events.go`（018）；
> - `grep -n 'session_running_elsewhere' pkg/state/runrt.go`（021）；
> - `grep -n 'func (s \*Server) filesRoot' pkg/gateway/session_files.go`（022）；
> - `grep -n 'func (s \*ProjectStore) FindOrCreateByRoot' pkg/state/project_store.go`（025）；
> - `grep -n 'func WorkingDir' pkg/tool/state.go`（026）；
> - `grep -n 'func (env \*Environment) RunnerForTurn' pkg/process/runner_pool.go`（027）；
> - `grep -n 'func ValidateProjectRoot' pkg/state/*.go`（028）；
> - `grep -n 'project_required' pkg/gateway/api_extra.go`（029）。
>
> 缺哪项，就在报告里把相关断言标"未验收（依赖未完成）"并跳过，其余照跑。

## Status

- **Priority**: P1
- **Effort**: M
- **Depends on**: 见前置
- **Category**: tests
- **Planned at**: commit `005c1a8` + 主树未提交改动，2026-10-11（取代 2026-10-10 版；原计划 031 并入本计划的 D、E 段）

## Why this matters

owner 规则（`docs/plan/SESSION_BIRTH_DIR_CONTINUATION_DESIGN.md` 第 0 节）：
- 本机 web 与 TUI 共用一份会话数据，web 发起的会话在 TUI 看到并续聊，反之亦然；
- 不做跨端实时推送，刷新或重新进入能看到最新即可；
- 一端在跑时另一端拒绝；
- 每个会话属于项目，工作目录 = 项目根；
- TUI 在哪个目录启动，那个目录就是项目，`/resume` 只列本项目；
- web 对话前必须选项目或文件夹；
- 沙箱与审批按项目根自己的信任推导；gateway 启动目录没有任何业务语义。

各计划都有单测，这份验收把它们串成真实的跨端流程。

## 环境搭建

```bash
export FOREBRAIN_RUN_WORK=$TMPDIR/fb-024 FOREBRAIN_RUN_SESSION=fb-024 FOREBRAIN_RUN_PORT=8820
D=.claude/skills/run-forebrain/driver.sh
$D build && $D reset
W=$FOREBRAIN_RUN_WORK
TOKEN=$(openssl rand -hex 32); umask 077; printf 'FOREBRAIN_GATEWAY_TOKEN=%s\n' "$TOKEN" > $W/home/.env; umask 022
mkdir -p $W/proj/sub $W/other $W/gwcwd   # 项目根、子目录项目、另一个项目、gateway 启动目录
git -C $W/proj init -q
```

1. 先 `$D start reply`，让 driver 写好配置并起 fake provider。
2. 再追加 gateway 段：`printf 'gateway:\n  http_addr: 127.0.0.1:8821\n  auth:\n    mode: token\n    token: ${FOREBRAIN_GATEWAY_TOKEN}\n' >> $W/home/forebrain.yaml`。
3. **在 `$W/gwcwd` 下**启动 gateway，后台运行并记下 pid：`(cd $W/gwcwd && env FAKE_LLM_KEY=sk-local-fake FOREBRAIN_HOME=$W/home $W/forebrain gateway start)`。
4. `curl -fsS http://127.0.0.1:8821/healthz` 返回 ok。
5. driver 的 TUI 工作目录设为 `$W/proj`（按 SKILL.md 的说明设置，例如 `FOREBRAIN_RUN_CWD`）。

## Steps

### A：TUI 发起 → web 看到并续聊 → TUI 再看到

1. TUI（在 `$W/proj`）：`$D submit 'hello from tui'; sleep 4`；`SID=$($D sid)`；记下屏幕上的回答。
2. 项目登记：`$D db "select root from fb_projects"` 含 `$W/proj`；`$D db "select project_id, cwd from fb_sessions where id='$SID'"` 的项目指向它，`cwd = $W/proj`。
3. web 看到：项目列表里有这个项目；messages 接口返回该会话的 user / assistant 行；用探针重载，块序与 TUI 屏幕一致。
4. web 续聊：Node 脚本向 `SID` 发 `'hello from web'`，收到 `run_completed`。
5. TUI 再看到：`$D stop; RESUME_ID=$SID $D start reply; sleep 4; $D screen` 含 `hello from web` 与它的回答，顺序在 TUI 那一轮之后。
6. TUI 续聊：`$D submit 'again from tui'; sleep 4` 成功；web 探针重载后三轮按序出现。
7. 数据同源：`$D db "select event_type, count(*) from fb_session_events where session_id='$SID' group by event_type"`：
   - 有 `turn_started` / `turn_completed`，两端的轮次都有；
   - **没有** `assistant_delta` / `reasoning_delta` / `reasoning_done` / `tool_output_delta`；
   - `select first_token_ms from fb_runs where session_id='$SID'` 每轮一个值。

### B：web 发起（必须选项目）→ TUI 看到并续聊 → web 再看到

1. `curl -X POST ... -d '{}' .../api/chat/sessions` → 400 `project_required`。
2. `-d '{"folder":"'$W/other'"}'` → 201，返回 `created: true` 的新项目与会话 `WSID`；Node 脚本发 `'hi from web'`。
3. TUI 在 `$W/proj` 里 `/resume`：列表里**没有** `WSID`（不同项目）。
4. `forebrain resume $WSID` 在 `$W/proj` 里执行 → 被拒绝，提示到 `$W/other` 运行。
5. 在 `$W/other` 里 `RESUME_ID=$WSID $D start reply; sleep 4; $D screen`，含 `hi from web` 与回答；`$D submit 'hi from tui'` 成功。
6. web 探针重载 `WSID`，两轮按序。

### C：另一端在跑时拒绝

1. TUI 用慢速模式开一轮（`$D start drip` 或 `hang`，按 SKILL.md）并提交，趁它在跑：
   - Node 脚本向同一会话发消息 → 收到 `run_error`，`data.code == 'session_running_elsewhere'`；
   - web 文案为"这个对话正在另一端运行（终端或网页），等它结束后再发送。"。
2. 反向：Node 脚本发一个慢速轮次，趁它在跑在 TUI `resume` 该会话并提交 → TUI 显示 plan 021 的英文提示，消息退回输入框。

### D：工具在项目根执行、gateway 启动目录无语义（原 031）

1. 真实模型（glm-5.3）。web 在项目 `$W/proj` 的会话里让模型执行 `pwd` 并写 `write_file x.txt`：
   - 输出为 `$W/proj`；
   - 文件出现在 `$W/proj/x.txt`；
   - `$W/gwcwd` 里没有任何新文件。
2. 同一会话在 TUI 续聊，重复第 1 步，结果相同。
3. 子目录项目：在 `$W/proj/sub` 启动 TUI：
   - 信任提示的路径是 `$W/proj/sub`；
   - `pwd` 为 `$W/proj/sub`；
   - 项目表里 `$W/proj/sub` 是一个独立项目，项目键与 `$W/proj` 不同（`select project_key from fb_projects`）。
4. 目录缺失：
   - 建项目 `$W/gone` 和会话后删除该目录；web 续聊收到 `run_error`，code 为 `session_directory_missing`；
   - TUI `forebrain resume` 该会话被拒绝。
5. FOREBRAIN_HOME 内：在 `$W/home/workspace` 启动 TUI，被拒绝。

### E：信任按项目根自己的（原 031）

1. `$W/proj` 标为信任，`$W/other` 保持未判断。分别开会话问模型"当前沙箱模式"，或读运行时配置探针：
   - `$W/proj` 为 workspace-write；
   - `$W/other` 为 read-only；
   - 结果与 gateway 启动目录 `$W/gwcwd` 是否信任无关：把 `$W/gwcwd` 标为信任或不信任各跑一次，结果不变。
2. 非 git 的已信任目录作为项目：放一个项目 MCP 条目，在待同意或已加载列表里出现，不是"未受版本控制"。

### F：工作台与目录

1. `curl ".../api/workspace/tree?session_id=$SID"` 列出 `$W/proj` 的内容，不含 `SOUL.md` / `memories`。
2. `?project_id=<other 的项目>` 列出 `$W/other`。
3. 主代理级工坊会话（`source='workshop'`、不带项目）的树是 `$W/home/workspace/skills`。

### 收尾

停 gateway、`$D stop`、`rm -rf $TMPDIR/fb-024`，删除 Node 脚本与探针。

## Done criteria

- [ ] A–F 每条断言的结果（通过 / 失败 / 未验收及原因）写进报告，附关键输出摘要
- [ ] 失败项附复现步骤与相关日志（`$W/gateway.err`、`$W/tui.err`）
- [ ] 未改任何源码；临时文件已清理

## STOP conditions

1. 前置里 016 / 017 / 018 缺失：跨端"看到"的断言没有意义，停下报告。
2. 任何一步出现数据错乱（另一端看到别人的会话、轮次重复或丢失、文件写进了 gateway 启动目录）：立即停下，保留现场（不删临时目录），报告路径。
