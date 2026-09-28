# 真实仓库实测日志（LIVE_TEST_LOG）

按计划编号分节记录。命令、结果摘要与链接；NOT TESTED 项写明原因。

## 013 文档站（G1 部分，2026-09-28 完成）

仓库：`git@github.com:forebrain-harness/forebrain-harness.github.io.git`（S0=`8fe7629`，S1=`ab85d37`，已推送 main）。

1. Pages 启用：`gh api -X POST .../pages -f build_type=workflow` → 409（已启用，legacy 模式）；
   `gh api -X PUT .../pages -f build_type=workflow` → `workflow`。
   验证 `gh api .../pages --jq '.build_type, .html_url'` → `workflow` / `https://forebrain-harness.github.io/`。
2. `Deploy Docs` 运行 #36455442988：`gh run watch --exit-status` → `success`；`build: success`、`deploy: success`。
3. 线上可达：`curl -fsS https://forebrain-harness.github.io/` → 200，页面含 "Forebrain Harness" ×4；
   `/guide/cli-command-reference` → 200；`/guide/memory-systems` → 200。
4. PR 只构建不部署：分支 `live-test/site-pr`（提交 `fd8138a`，合规标题），PR #1
   （https://github.com/forebrain-harness/forebrain-harness.github.io/pull/1）。
   该 PR 的 Deploy Docs 运行：`build: success`、`deploy: skipped`。PR 已关闭、分支已删除。

S2（安装说明）待 G3 后执行。

## 001–008（G1 主仓库实测）

**NOT TESTED（阻塞）**：G1 `git push -u origin main` 被 GitHub push protection 拒绝
（GH013；`pkg/telemetry/analytics_metadata_test.go:414` 的 Slack token 模式，位于 M0 基线提交
`1227968`）。已核实为测试假值（`xoxb-1234…` 形状，与 AWS EXAMPLEKEY 等示例同列的 redaction fixture）。
放行需要 owner 在推送报错给出的网页链接标记 false positive（或在仓库设置关闭 push protection）。
放行后重推并补全本节。
