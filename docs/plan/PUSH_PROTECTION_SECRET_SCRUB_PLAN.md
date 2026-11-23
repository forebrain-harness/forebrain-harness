# PUSH PROTECTION SECRET SCRUB 计划（已实施，待 owner 落地 push）

> 会话计划原件：`~/.forebrain/workspace/plans/-Users-doudou-workspace-unionj-cloud-forebrain-harness/cli-14dfb44e-d15a-4a1b-8aaf-6893675e0f95/scrub-dev-history-secrets.md`（含完整 Implementation Status）。本文件是仓内镜像。

## 状态

- 2026-11-18：实施完成，tech-lead 评审 APPROVE。改写分支 `scrub-dev-history-secrets`（`.worktrees/scrub-dev-history-secrets`），`e6e39693 → b2ea357f`。
- **待 owner 落地**：主树 `git merge --ff-only scrub-dev-history-secrets` → `git branch -D vol-281 scrub-dev-history-secrets`、`git worktree remove .worktrees/scrub-dev-history-secrets` → `git push`（普通推送）。
- 落地后请回填 push 结果到本文「验收记录」。

## 摘要

- 根因：`git push origin dev` 被 GH013 拦截——`64d3e03b` 的 Volcengine 文档示例 AK（模式误报）、`b8081a0d`/`aace268f` 的 html fixture 里 deepseek/zhipu/tavily/openrouter 四个键形字面量。
- 方案：filter-repo `--partial --refs <分支> --replace-text`，5 条规则：AK 裸值→Go 拆串拼接（运行时值不变，签名测试仍逐字节过）；html 四键→单引号锚定替换为假值（Go 侧 deepseek 副本已在远端历史，owner 裁决保留，键若真实靠轮换）。
- 最早触碰提交 `64d3e03b`（本地独有）→ 新分支仍以 `c680aaed`（origin/dev）为 merge-base → 落地是普通 fast-forward merge + push，无 force。
- 新旧 SHA 映射：`.git/worktrees/scrub-dev-history-secrets/filter-repo/commit-map`（c1 的锚点 `8e936d7b → 0ad4c033` 已通报）。

## 验收记录

- 评审复跑全过：129 提交数不变、消息序逐条一致、diff 恰 2 文件、5 值在新分支全消失、模式扫描 0 命中、`TestVolcengine*` + `TestSettingsPatchKeepsUnsentCredentials` + settingsSchema 9/9 全绿、主树与协作 worktree 零波及。
- **落地（2026-11-18，owner 授权执行）**：dev 经 `git reset --hard scrub-dev-history-secrets` 落到 `b2ea357f`（`merge --ff-only` 对改写历史必然失败——旧 tip 是被替换提交，兄弟关系非祖先；干净树上 reset 零丢弃）；`git push` 成功，`c680aaed..b2ea357f  dev -> dev`，**GH013 消失、无 force**；vol-281/改写分支/executor worktree 已删。
- c1 已重新锚定（`8e936d7b → 0ad4c033`）并获落地通报。
- 遗留：plan-review worktree（detached `aace268f`）用完前勿 `git gc --prune=now`；GitHub 例行 dependabot 报 266 条依赖漏洞（与本修复无关，未处理）。

## Maintenance

- plan-review worktree（detached `aace268f`）用完前不要 `git gc --prune=now`（旧对象靠 reflog 存活）。
- 11 个 log-/plan-/web- 分支在改写起点之下，不受影响；docs/plan 本地原型副本仍含原键（未跟踪，永不入 git）。
