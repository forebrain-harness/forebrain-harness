# 计划 009：真机验收——断网后在 subagent 视图里继续

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告。
> 完成后更新 `docs/plan/SUBAGENT_CONVERSATION/README.md` 里本计划的状态行。**不要提交代码。** 先读 README 的"全局规则"，尤其是第 11 条（真机、密钥）。
>
> **前置检查**：README 里 001–008、010、011 必须都是 `DONE`，否则 STOP。

## 状态

- **优先级**：P1
- **工作量**：M
- **风险**：LOW（只验证，不改产品代码；会加一个验收用的脚本）
- **依赖**：001–008、010、011
- **类别**：tests
- **基线**：提交 `1a6d708`，2026-10-04

## 为什么要做

owner 的问题是在真实使用里遇到的：真实模型、真实网络中断。单测和假模型只能证明"做出来了"，不能证明"在真实的断网和真实的模型下真的这样工作"（项目长期规则：涉及模型行为或屏幕呈现的修复必须在运行中的 TUI 和网页上对着真实模型证明）。本计划在两个界面上，用真实模型和一个可控的"断网代理"，把七个要求逐条走一遍，并测量缓存命中率。

## 现状

- TUI 驱动：`.claude/skills/run-forebrain/driver.sh`（tmux；计划 001 加了 `click`，007、011 加了 `subagent-net`、`subagent-limit` 假模型模式）。它默认指向假模型；真实模型需要自己写一个隔离的 `FOREBRAIN_HOME`（见下）。
- 网页：`scripts/acceptance/web_e2e.sh`，`FOREBRAIN_E2E_REAL_LLM=1` 时改用智谱，密钥来自 `~/.forebrain/e2e-zhipu.env`（变量 `FOREBRAIN_E2E_ZHIPU_KEY`，权限 600）；参照它 `:78-98` 一带写配置的方式（只写 `${FOREBRAIN_E2E_ZHIPU_KEY}` 引用）。
- 缓存命中率：每个运行的用量在状态库 `fb_runs` / `fb_messages.usage_json` 里有 `CacheReadInputTokens`、`CacheCreationInputTokens`、`InputTokens`；命中率 = `CacheRead / (CacheRead + CacheCreation + Input)`。真机测量只要求 DeepSeek 和 OpenAI，本机没有它们的凭据就记"凭据缺失，已跳过"；智谱的数字照记，作为参考。

## 设计

### 1. 断网代理（新建 `scripts/acceptance/flaky_proxy.py`）

一个本地 HTTP 反向代理，转发到真实服务商的 base URL：

- 命令行：`flaky_proxy.py <listen_port> <upstream_base_url> <control_file>`。
- 每个请求读一次 `control_file` 的内容：`pass` 原样转发（流式逐块转发，不缓冲）；`drop-subagent` 时，**请求体里的 system 含 subagent 的系统提示开头**（`You are a general-purpose subagent`）的请求在转发几个字节后直接断开（模拟流式中途断网），其它请求照常转发；`drop-all` 时所有请求都这样断开。
- 只监听 `127.0.0.1`。不记录请求体和请求头（避免把密钥写进日志）。

### 2. 真实模型的隔离环境

写一个小脚本 `scripts/acceptance/live_subagent.sh`（新建）：建隔离的 `FOREBRAIN_HOME` 和一个 scratch git 项目目录，写 `forebrain.yaml`，主 agent 的 provider 指向 `http://127.0.0.1:<proxy_port>`（经代理）、`api_key: ${FOREBRAIN_E2E_ZHIPU_KEY}`；在 tmux 里 `source ~/.forebrain/e2e-zhipu.env` 后启动 TUI。脚本结束时清理 tmux 会话、代理进程和隔离目录（保留截图和数据库副本到 `$WORK/evidence`）。不要用真实的 `~/.forebrain`，不要在仓库里跑。

## 步骤

### 第 1 步：TUI，真实模型，断网后继续（要求 1、2、4）

1. 代理设 `pass`，启动 TUI。提交：`派发一个 general-purpose subagent，让它读 README.md 并用三句话总结；你自己等它的结果。`
2. 等 subagent 卡片出现、roster 出现它的行后，把代理切到 `drop-subagent`。
3. 等 subagent 失败（卡片显示失败），把代理切回 `pass`。
4. 点卡片（或 roster 回车）进入它的视图。截图：roster 箭头指向它（要求 5）；roster 行显示的任务与卡片上一字不差（要求 6）；footer 右侧是 `N%/<窗口>`（要求 4）。
5. 在视图里输入 `continue` 回车。截图：`continue` 和它之后的回答都在该 subagent 视图里；按 Esc 回主视图，主视图里没有这两条（要求 1、D3）。
6. 查数据库：这两条在 worker 会话（`main:<对话id>:…`）下；worker 会话里有失败前的工具调用和结果（证明"接着做"而不是"从头来"）。
7. 再进入视图，趁它运行时连发两条消息，截图预览；按召回键召回最新的一条；再发一条后按 Esc，确认它作为下一次执行立即发出（要求 2、D1）。

### 第 2 步：TUI，压缩（要求 3）

1. 在 subagent 视图里 `/compact`，截图压缩卡片在该视图、footer 百分比回升。
2. 把 `compact.model_auto_compact_token_limit` 设得很小重启，让 subagent 在继续时触发回合前自动压缩；截图压缩卡片在该视图。

### 第 3 步：TUI，用量上限后自动继续（要求 7）

真实服务商的额度不能按需耗尽，这一步用假模型的 `subagent-limit` 模式（计划 011 第 6 步）在真机 TUI 上走一遍，截图：提示只在该 subagent 视图；到点后继续的消息和回答在该视图。

### 第 4 步：网页，真实模型

`FOREBRAIN_E2E_REAL_LLM=1 scripts/acceptance/web_e2e.sh`：计划 008 的 `subagent-conversation.spec.ts` 在真实模型模式下运行（断网用同一个代理：`web_e2e.sh` 在真实模型模式下让 provider 经过 `flaky_proxy.py`，用例在等到 subagent 卡片后写控制文件）。断言放宽为"有非空回答"，但"回答出现在 subagent 视图而不在主对话"不放宽。

### 第 5 步：缓存命中率

- 对第 1 步里 subagent 继续的那次执行，从 `usage_json` 算它第一次请求的命中率；与"改动前的继续"对比——改动前的继续发的是新拼的提示加空历史，命中只覆盖工具和 system。记录两组数字。
- 主会话：同一个脚本在改动前后的基线提交上各跑一次（不含 subagent 的普通两回合对话），命中率不得下降。
- DeepSeek / OpenAI：有凭据就按同样步骤测，没有就记"凭据缺失，已跳过"。

## 完成标准

- [ ] 第 1–4 步的截图和数据库查询结果都已附在报告里，逐条对应要求 1–7
- [ ] 第 5 步的命中率数字（含主会话前后对比）已记录；主会话不下降
- [ ] 新建的两个脚本和报告里只出现 `${FOREBRAIN_E2E_ZHIPU_KEY}` 这个引用，不出现密钥本身；代理没有写任何请求日志
- [ ] 隔离目录、tmux 会话、代理进程都已清理
- [ ] README 状态行已更新

## STOP 条件

- 真实模型下某个要求不成立（例如 `continue` 的回答出现在主对话里）：停下，报告现象和证据，不要改产品代码——回到对应计划。
- 主会话命中率下降。
- 需要把密钥写进任何文件才能运行。

## 维护说明

- `flaky_proxy.py` 和 `live_subagent.sh` 以后验证任何"网络中断后恢复"的改动都能复用；新增场景时只加控制文件的取值，不要让代理记录请求内容。
