# 真实仓库实测日志（LIVE_TEST_LOG）

按计划编号分节记录。命令、结果摘要与链接；NOT TESTED 项写明原因。

## 001（G1，2026-09-29 完成）

- 推送 `fecd07b`（M1）后 CI 的 `go install from a module proxy` job → success。
- 真实安装（本机经系统代理走默认源 `proxy.golang.org,direct`；本机直连 GitHub 被网络阻断，
  `HTTPS_PROXY=http://127.0.0.1:7897` 是环境事实，不改变安装语义）：
  `go install -tags fts5 github.com/forebrain-harness/forebrain-harness/cmd/forebrain@fecd07b8db08…`
  → 编译安装成功，二进制可执行（当时的 `--version` 仍报 `v0.0.1`，这正是 002 要修的）。

## 002（G1，2026-09-29 完成）

- 真实安装 M2 提交 `aa2b706`：`--version` → `v0.0.0-20260928163946-aa2b7060d66b`
  （go 命令记录的模块伪版本，非硬编码 `v0.0.1`）。
- CI 的 `Go mod vet build test` job 含 `scripts/check-release-versions.sh` 步骤 → success。

## 004（G1，2026-09-29 完成）

- 从 M3 提交 `56d62c2` 用真实 go install 构建两个坏二进制：
  `CGO_ENABLED=1`（无 `-tags fts5`）与 `CGO_ENABLED=0`（带 tag）。
- 各自配临时 `FOREBRAIN_HOME`（假 provider 配置）执行 `gateway start`：
  两者 stderr 均恰为一行
  `open database: this forebrain was built without cgo or SQLite FTS5; reinstall it with: CGO_ENABLED=1 go install -tags fts5 github.com/forebrain-harness/forebrain-harness/cmd/forebrain@latest`，
  `exit=1`。

## 006（G1，2026-09-29 完成）

- `gh api repos/…/community/profile` → health 87，files 含 code_of_conduct、contributing、
  issue_template、pull_request_template、license、readme。
- 测试 PR #12（分支 `live-test/006-dco`）：
  - 标题 `[live-test] bad title` → `Conventional Commits title` fail（一行规则说明）；
    改为 `test: check the pull request gates` → pass。
  - 无签署提交 `a5b5a6b` → `DCO sign-off` fail，点名该提交并给出 `git rebase --signoff` 提示；
    追加签署提交后仍 fail（首个提交未签，符合预期）。
  - PR 已关闭、分支已删除。
- 仓库设置（见 012）：`squash_merge_commit_title=PR_TITLE`、`squash_merge_commit_message=PR_BODY`（D7）。

## 007（G1/G2，2026-09-29）

- `Lint workflows`（actionlint）在 main 与 PR 上均 success；SC2035 缺陷由它抓出并修复（`b2de485`）。
- Dependabot 已生效：开出并（owner 手动）合并 #3（docker golang 1.27）与 #10（go 依赖组 25 项）。
- **待观察**：Dependabot auto-merge 队列（`--auto --squash`）要在规则集启用后的下一个 Dependabot PR 上
  才能观察到实际自动合并；此前 auto-merge 为 off，#3/#10 由 owner 手动合并。

## CI 首跑暴露并修复的缺陷（计划外，纳入本期；PR #14）

1. **可达漏洞**（Security 工作流抓出，17 项 symbol-level）：grpc v1.80.0（2）、x/net v0.53.0（6）、
   Go 1.26.2 stdlib（9）。修复：grpc→v1.83.1、x/net→v0.58.0（含 Dependabot #10 的组升级）、
   go 指令→1.26.6；本地 govulncheck `0 vulnerabilities`（`b92a1b1`，rebase 后含冲突修正）。
2. **Linux 缺沙箱后端**（约 50 个测试 `bubblewrap_not_found`）：CI 装 bubblewrap。
3. **Ubuntu 24.04 AppArmor 限制 unprivileged userns**（`bwrap: setting up uid map: Permission denied`）：
   `sysctl kernel.apparmor_restrict_unprivileged_userns=0`。
4. **runner 无 ripgrep**（shell 工具只读搜索用 `rg`）：CI 安装 ripgrep。
5. **时区依赖测试**（`TestResetAtFromProse` 两个子测试在 UTC runner 失败）：固定 +08 时区构造。
6. **环境残留依赖测试**（`TestOpenFilesSessionsUnderTheLaunchProjectScope` 依赖开发者 shell 的
   `OPENAI_API_KEY`）：测试内 `t.Setenv`。
7. **全局 git ignore 吞掉测试 fixture**（`~/.config/git/ignore` 的 `**/.claude/settings.local.json`
   使 guangfa fixture 的五条权限规则未进基线提交，CI 上 safety.json 不写）：入库该文件 +
   仓库级 `.gitignore` 反向规则 `!pkg/migrate/testdata/**`（仓库规则优先级高于全局 ignore），
   `.DS_Store` 在其中重新忽略。
8. **Windows 测试套件从未绿过**（POSIX 文件权限断言等几十项；基线自带 Windows job 首次真实运行）：
   owner 拍板（2026-09-29）——必需检查保留 `Windows build test (CGO)`（构建+silk 检查），
   完整套件移入 `Windows test suite (non-blocking)`（continue-on-error，失败可见不阻塞），
   待套件 Windows-ready 后再转必需。

PR #14 三轮 CI：第一轮修复 2/5/8；第二轮修复 3/4/6 并给 guangfa 加诊断输出；第三轮修复 7 后
`Go mod vet build test: success`，全部必需检查 pass（run `36518989535`）。

## 008（G2 完成；G3 待 owner 合并 PR #13）

- App（forebrain-release，id 5114865）安装后重跑 Release：`release-please: success`、
  `webui-into-release-pr: success`（run `36509605801`）。
- Release PR **#13** `chore(main): release 0.1.0`（分支 `release-please--branches--main--components--forebrain`），
  含 `chore(release): build the web UI` 提交；CHANGELOG 列出 memory/state/deps 提交；
  版本文件（VERSION、npm 六处、manifest）如设计更新。
- #13 的 CI 因上述 CI 缺陷一度红；PR #14 合并后 release-please 会把新提交并入 #13 并重跑。

## 012（G2 完成；线上 npm/README 核对随 G3）

- `scripts/setup-github.sh --dry-run`（配合会失败的 gh stub 证实零 gh 调用）→ 真跑 exit 0。
- 核对：squash-only + `PR_TITLE`/`PR_BODY` + auto-merge + delete-branch + update-branch 全部生效；
  `private-vulnerability-reporting` → true；`dependencies` 标签存在；`environments/npm` 存在；
  `main` 规则集 active，必需检查恰为 8 个 context；变量/密钥齐全（FOREBRAIN_APP_ID、
  FOREBRAIN_APP_PRIVATE_KEY、NPM_TOKEN——密钥值从未进入任何文件或日志）。
- G1 推送曾被 push protection 拦截（M0 内 Slack-token 形状的测试假值），owner 标记 false positive 放行。

## 005（待 G3：首个 Release 发布后）

真实二进制首跑下载词典、断网一句话报错、`FOREBRAIN_RELEASE_DICT_CHECK` 资产核对。

## 013（G1 完成；S2 待 G3）

S0/S1 已推送部署（见下）；S2（安装说明）待首个 Release 实测通过后写入。

## 013 文档站（G1 部分，2026-09-28 完成）

仓库：`git@github.com:forebrain-harness/forebrain-harness.github.io.git`（S0=`8fe7629`，S1=`ab85d37`，已推送 main）。

1. Pages 启用：`gh api -X POST .../pages -f build_type=workflow` → 409（已启用，legacy 模式）；
   `gh api -X PUT .../pages -f build_type=workflow` → `workflow`。
2. `Deploy Docs` 运行 #36455442988：`build: success`、`deploy: success`。
3. 线上可达：首页 200（含 "Forebrain Harness" ×4）；`/guide/cli-command-reference` 200；
   `/guide/memory-systems` 200。
4. PR 只构建不部署：分支 `live-test/site-pr`（提交 `fd8138a`），PR #1
   （…/pull/1）：`build: success`、`deploy: skipped`。PR 已关闭、分支已删除。

## NOT TESTED（当前）

- fork PR 拒绝、非维护者评论忽略（属推迟的 gateway 批次的守护项）。
- Windows 完整套件转绿（owner 已裁决非阻塞策略，列为后续工作）。
