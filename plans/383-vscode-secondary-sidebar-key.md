# Plan 383: 修复 VS Code 扩展视图容器贡献键，让 Forebrain 出现在右侧面板

> **Executor instructions**: 逐步执行，每步跑验证命令并确认预期结果后再继续。
> 命中「STOP conditions」任一条就停下报告，不得即兴发挥。
>
> **Drift check（先跑）**: 本仓库多 agent 并行、owner 常设规则禁止 commit，基线
> 记工作区状态而非 SHA。逐条跑：
> 1. `grep -c '"auxiliarybar"' editors/vscode/package.json` → 必须 `1`
> 2. `grep -c '"secondarySidebar"' editors/vscode/package.json` → 必须 `0`
> 3. `shasum -a 256 editors/vscode/src/relay.ts` → 必须 `1f53fde3ec48eadc28d8059e5ad2f2b555feee1ab9ff683f9fff160fd0600939`
> 4. `grep -c 'vsce@2.32.0' scripts/build-vscode-forebrain-ext.sh` → 必须 `1`
> 任一不符＝STOP（有人已动过现场）。行号漂移不算 drift，内容不符才算。
> （注：本计划已实施完毕，drift check 仅供复盘/回归参照。）

## Status

- **Priority**: P1（owner 正等着用这个面板）
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none（plans/376、379 已落 dev，本计划是其真机验收暴露的缺陷修复）
- **Category**: bug
- **Planned at**: 工作区 dev `c3176812`（2026-11-26）
- **Status**: 已实施并真机验收通过（2026-11-26，评审 APPROVE）；源码改动在 worktree `.worktrees/exec-383-vscode-sidebar-key` 留工作区未 commit，合并回 dev 待 owner 裁决

## Why this matters

owner 已安装 `forebrain.forebrain-vscode@0.2.0`（扩展面板里可见），但 VS Code 右侧
辅助侧栏只有 Chat / Codex，没有 Forebrain 标签页，扩展完全不可用。根因是 manifest
把视图容器声明在非法贡献键 `auxiliarybar` 下，VS Code 静默忽略，容器不注册、两个
视图被甩进 Explorer。改一个键名＋补一条钉住契约的回归测试＋重打包装机，owner 即可
在右侧面板使用完整 Web UI 与原生集成。

## Current state（根因证据链，全部已现场核实）

1. **症状**：owner 两张截图——右侧面板仅 Chat/Codex；Extensions: Marketplace 搜
   forebrain 可见已安装的 Forebrain 扩展。
2. **已装且启用**：`~/.vscode/extensions/forebrain.forebrain-vscode-0.2.0/` 存在且
   登记于 `extensions.json`；所有工作区 profile 关联均为 `__default__profile__`；
   全局 state.vscdb `extensionsIdentifiers/disabled` 为空；安装 mtime 11-25 12:47
   早于窗口启动 23:18（排除「没重载」）。
3. **渲染器警告**（`~/Library/Application Support/Code/logs/20261125T231839/window2/renderer.log:147`）：
   ```
   [warning] [forebrain.forebrain-vscode]: View container 'forebrain' does not exist
   and all views registered to it will be added to 'Explorer'.
   ```
4. **非法键**（`editors/vscode/package.json:13-22`，修复前摘录）：
   ```json
   "viewsContainers": {
     "auxiliarybar": [{ "id": "forebrain", "title": "Forebrain", "icon": "assets/forebrain.svg" }]
   },
   "views": {
     "forebrain": [
       { "id": "forebrain.chat", "name": "Chat", "type": "webview", "contextualTitle": "Forebrain" },
       { "id": "forebrain.checklist", "name": "Checklist", "contextualTitle": "Forebrain" }
     ]
   },
   ```
   VS Code `contributes.viewsContainers` 只认 `activitybar` / `panel` /
   `secondarySidebar` 三个键；`auxiliarybar` 是 workbench 内部名，不是贡献点。
5. **同机对照**：Codex 扩展（`~/.vscode/extensions/openai.chatgpt-26.1120.50957-darwin-arm64/package.json`）
   声明 `"secondarySidebar": [{ "id": "codexSecondaryViewContainer", ... }]`，其容器
   正常注册——全局 state.vscdb `workbench.auxiliarybar.pinnedPanels` 含
   `workbench.view.extension.codexSecondaryViewContainer`，而 forebrain 的容器
   （应为 `workbench.view.extension.forebrain`）在 pinned/placeholder 两表中均缺席。
6. **来源**：`plans/376-vscode-extension-chat-panel.md:284` 原样写了 `auxiliarybar`，
   执行器照抄；103 条测试无一断言 manifest 键，故全绿漏过；owner 真机验收（376
   Step 9 / 379 Step 8）本就 pending，此缺陷正在该闸门暴露。
7. **已排除的竞争解释**：未安装（extensions.json＋截图证伪）、错 profile（全默认）、
   被禁用（空）、窗口未重载（安装早于启动且警告证明 manifest 已被处理）、图标缺失
   （装机目录 `assets/forebrain.svg` 存在）。

仓库约定（照抄 exemplar，勿新造风格）：

- 测试＝`node:test`＋`node:assert/strict`，见 `editors/vscode/src/approvalText.test.ts`。
- 测试编译＝`npm test` 的 pretest 钩子 `node build.mjs --test`（esbuild，**format:
  'cjs'**，见 `editors/vscode/build.mjs:11`）→ `dist-test/*.test.js`；故编译产物里
  `__dirname` 可用，读 manifest 用 `path.join(__dirname, '..', 'package.json')`。
- 打包＝仓库根 `bash scripts/build-vscode-forebrain-ext.sh`（内跑 npm ci/typecheck/
  test/compile，vsce@2.32.0，产物 `editors/vscode/out/forebrain-vscode-<version>.vsix`，
  out/ 已 git-ignore）。
- 安全承重件 `editors/vscode/src/relay.ts` 冻结禁改（sha256 见 drift check）。
- owner 常设规则：**不 commit**、改动留工作区。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 扩展门禁 | `cd editors/vscode && npm ci --no-audit --no-fund && npm run compile && npm run typecheck && npm test` | exit 0；`# fail 0`，tests 106 |
| 单跑测试 | `cd editors/vscode && npm test` | 同上（pretest 自动编译） |
| 打包 | `bash scripts/build-vscode-forebrain-ext.sh` | 末行 `built …/out/forebrain-vscode-0.2.1.vsix` |
| 装机 | `code --install-extension editors/vscode/out/forebrain-vscode-0.2.1.vsix` | 含 `was successfully installed`（1.147 为小写，大小写无关匹配） |
| 装机核对 | `code --list-extensions --show-versions \| grep forebrain.forebrain-vscode` | `forebrain.forebrain-vscode@0.2.1` |
| 容器注册核对 | `sqlite3 "$HOME/Library/Application Support/Code/User/globalStorage/state.vscdb" "SELECT value FROM ItemTable WHERE key='workbench.auxiliarybar.placeholderPanels'"` | 含 `workbench.view.extension.forebrain` |
| 警告核对 | 最新窗口 `renderer.log` 中 `View container 'forebrain' does not exist` 只允许出现在修复前时间点；重载边界（`Render performance baseline` 行）之后无新增 | 边界后无匹配 |

## Scope

**In scope（只许改这些）**：
- `editors/vscode/package.json` —— 键名 `auxiliarybar`→`secondarySidebar`；version `0.2.0`→`0.2.1`
- `editors/vscode/src/manifest.test.ts`（新建）
- `plans/383-vscode-secondary-sidebar-key.md`＋`plans/README.md`（只追加索引行）
- 构建产物 `editors/vscode/out/*.vsix`（git-ignored）与本机 `~/.vscode/extensions/`（装机，仓库外）

**Out of scope（看着相关也不碰）**：
- `editors/vscode/src/relay.ts` —— 冻结件，sha256 恒等是 done 判据
- 扩展其余全部源码（extension/panel/connection/gateway* 等）——本 bug 与运行时逻辑无关
- `plans/376-*.md`、`plans/379-*.md` —— 历史记录不回改，偏离由 383 记录
- 全部 Go / frontend / npm 代码；`forebrain.deliverable-open` 扩展；其他 agent 的未提交改动
- 任何 git commit / push / 分支操作（owner 禁 commit）

## 修复设计

1. **键名归一**：`viewsContainers` 的 `auxiliarybar` 改为 VS Code 合法贡献点
   `secondarySidebar`（右侧辅助侧栏的官方键，Codex 同机实证）。容器 id 仍为
   `forebrain`，`views` 键不动——VS Code 会自动加前缀注册为
   `workbench.view.extension.forebrain`。
2. **版本 0.2.0→0.2.1**（patch）：让装机核对无歧义（`--show-versions` 直接区分新旧），
   避免同版本覆盖安装的边界；打包脚本按 version 自动命名 VSIX。
3. **回归测试钉住契约**（`manifest.test.ts`）：白名单校验容器键＋views 引用完整性，
   修复前红、修复后绿。取值覆盖用白名单＋通用默认（owner 既有裁决），不逐键排除。

**行为变化矩阵**：

| 场景 | 旧行为 | 新行为 | 刻意修订？ |
|---|---|---|---|
| 右侧辅助侧栏 | 无 Forebrain 容器 | Forebrain 容器（Chat webview＋Checklist 树） | 是（本修复） |
| Explorer 侧栏 | 兜底甩入的 forebrain Chat/Checklist 视图 | 消失 | 是（随容器归位） |
| 命令/键位/中继/网关逻辑 | — | 不变 | — |
| 装机版本 | 0.2.0 | 0.2.1 | 是（装机可核对） |

非功能面：纯 manifest 键名改动，无运行时热路径/资源/并发影响；回归测试只读一个
JSON，耗时可忽略；可维护性由白名单测试钉住；UX 即 owner 诉求本身。

## Steps

### Step 1: Drift check
跑文首四条命令。**Verify**: 1 / 0 / sha256 恒等 / 1。不符＝STOP。

### Step 2: 先写回归测试，证红
新建 `editors/vscode/src/manifest.test.ts`（照 `approvalText.test.ts` 的 import 与
test() 风格）。注意： parsed JSON 需类型标注（`Record<string, ViewContainer[]>`，
`type ViewContainer = { id: string; title: string; icon: string }`），否则
`Object.values(...).flat()` 产 `unknown[]` 致 typecheck TS2345。三条断言：
```ts
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, existsSync } from 'node:fs';
import { join } from 'node:path';

type ViewContainer = { id: string; title: string; icon: string };

const root = join(__dirname, '..');
const manifest = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'));
const containers: Record<string, ViewContainer[]> = manifest.contributes.viewsContainers ?? {};
const VALID = ['activitybar', 'panel', 'secondarySidebar'];

test('view containers use only valid VS Code contribution points', () => {
  for (const key of Object.keys(containers)) assert.ok(VALID.includes(key), `invalid viewsContainers key: ${key}`);
});

test('the Forebrain container is declared for the secondary sidebar', () => {
  const entry = (containers.secondarySidebar ?? []).find((c) => c.id === 'forebrain');
  assert.ok(entry, 'forebrain container must live in secondarySidebar');
  assert.equal(entry.icon, 'assets/forebrain.svg');
  assert.ok(existsSync(join(root, entry.icon)), 'declared icon must exist');
});

test('every views key references a declared container', () => {
  const ids = Object.values(containers).flat().map((c) => c.id);
  for (const key of Object.keys(manifest.contributes.views ?? {})) assert.ok(ids.includes(key), `views key ${key} has no container`);
});
```
**Verify**: `cd editors/vscode && npm test` → 前两条测试**红**（`invalid viewsContainers key: auxiliarybar`），其余 103 条绿。这是 bug 前置状态的单元级复现。

### Step 3: 改 manifest（键名＋版本）
`editors/vscode/package.json`：`"auxiliarybar"` → `"secondarySidebar"`；
`"version": "0.2.0"` → `"0.2.1"`。其余一字不动。
**Verify**: `grep -c '"secondarySidebar"' editors/vscode/package.json` → 1；`grep -c '"auxiliarybar"' …` → 0；`npm test` → `# fail 0`，tests 106（103＋3）。

### Step 4: 全门禁＋安全回归
**Verify**:
- `cd editors/vscode && npm ci --no-audit --no-fund && npm run compile && npm run typecheck && npm test` → 全 exit 0，`# fail 0`
- `shasum -a 256 editors/vscode/src/relay.ts` → 仍 `1f53fde3…`
- `grep -rn "login#token=" editors/vscode/src --include=*.ts | grep -v .test.ts` → 仅 `extension.ts` 一行

### Step 5: 打包＋装机
**Verify**:
- `bash scripts/build-vscode-forebrain-ext.sh` → 末行 `built …forebrain-vscode-0.2.1.vsix`
- `code --install-extension editors/vscode/out/forebrain-vscode-0.2.1.vsix` → `was successfully installed`（大小写无关）
- `code --list-extensions --show-versions | grep forebrain.forebrain-vscode` → `@0.2.1`

### Step 6: 真机验收（本 bug 的现场是 VS Code，不是 TUI）
装机后运行中的窗口通常会 live 重载扩展贡献；等 ~10s 核对：
- placeholderPanels 含 `workbench.view.extension.forebrain`
- 最新窗口日志目录（`ls -td "$HOME/Library/Application Support/Code/logs/"*/ | head -2`，跳过只有 cli.log 的）里，`View container 'forebrain' does not exist` 只允许出现在修复前时间点；以 `Render performance baseline` 行定重载边界，边界后无新增（reload 往同一 renderer.log 追加，修复前旧行保留属正常）。

若容器仍未出现，重载窗口（AppleScript 驱动命令面板）：
```bash
osascript -e 'tell application "Visual Studio Code" to activate' \
  -e 'delay 0.5' -e 'tell application "System Events" to keystroke "p" using {command down, shift down}' \
  -e 'delay 0.5' -e 'tell application "System Events" to keystroke "Reload Window"' \
  -e 'delay 0.3' -e 'tell application "System Events" to key code 36'
```
（accessibility 被拒＝STOP 该子步，请 owner 手动 `Developer: Reload Window`，其余核对继续。）

然后逐项取证：
1. **复现→修复**：AppleScript 取 VS Code 窗口 bounds，`capture` mode=region 截右侧面板 → 读图确认 Forebrain 标签页与 Chat/Codex 并列。
2. **面板可用**：命令面板跑 `Forebrain: Open Chat Panel`（forebrain.openPanel）→ webview 应加载网关 Web UI（扩展经 PATH 上的 forebrain 自起网关）；截图取证。若出现 token/登录引导属 376 文档化流程，记录并按需请 owner 输入；白屏/报错＝新缺陷，定位根因并入本计划补记。
3. **回归**：同一截图确认 Chat/Codex 标签仍在；Explorer 里兜底的 forebrain 视图已消失（视图渲染于 Forebrain 容器内＋无兜底警告即成立；截 Explorer 取证时注意截的是 Explorer 而非 Extensions）。
4. **Checklist 视图**：Forebrain 容器内可见 Checklist 分区（空态文案 "No checklist yet…" 属正常）。

### Step 7: 收口
- 计划状态行改「已实施并真机验收通过（日期）」，新增「验收记录」章节（场景→证据路径）。
- `plans/README.md` **末尾追加**一行索引（只追加，不改既有行）。
- `git status --porcelain` 核对改动范围＝Scope；**不 commit**。
- 报告：根因链、最小改动清单、验证证据、遗留限制。

## Test plan

- 新增 `editors/vscode/src/manifest.test.ts` 三条：合法贡献键白名单（bug 直接回归，修复前红）、secondarySidebar 声明 forebrain 容器＋图标存在、views 键引用完整性。
- 结构 exemplar：`editors/vscode/src/approvalText.test.ts`。
- `npm test` → 106/106；typecheck/compile exit 0。

## Done criteria（机器可查，全须成立）

- [x] `grep -c '"secondarySidebar"' editors/vscode/package.json` = 1 且 `grep -c '"auxiliarybar"'` = 0；version = 0.2.1
- [x] `npm test` `# fail 0`（含 manifest 三条）；`npm run typecheck`、`npm run compile` exit 0
- [x] `code --list-extensions --show-versions` 含 `forebrain.forebrain-vscode@0.2.1`
- [x] placeholderPanels 含 `workbench.view.extension.forebrain`；重载边界后无新增 `View container 'forebrain' does not exist` 警告
- [x] 右侧面板截图见 Forebrain 标签；Open Chat Panel 后 webview 渲染网关 UI（截图）
- [x] relay.ts sha256 恒等 `1f53fde3…`
- [x] `git status` 改动范围＝Scope；零 commit
- [x] plans/383 落盘＋README 索引行追加

## STOP conditions

- Drift check 任一不符（现场已被他人改动）。
- 重装＋重载后警告仍在/容器仍缺席 → 停下用新日志重新诊断，不得叠猜测。
- 门禁失败落在本计划未改的文件 → 问该文件的负责 agent（本树多 agent 并行），不自行修。
- 修复需要碰 out-of-scope 文件（尤其 relay.ts）。
- AppleScript accessibility 被拒 → 该子步请 owner 手动重载，其余继续。
- webview 白屏/网关起不来 → 新缺陷：先定位根因再并入本计划补记，不静默扩 scope。

## Maintenance notes

- `plans/376:284` 仍写着错误键——历史不回改；383 记录偏离，manifest.test.ts 白名单
  钉住合法键，今后改 manifest 不会再静默回归。
- owner 的 376 Step 9 / 379 Step 8 真机验收除「面板可见」外仍 pending（含「面板 WS ×
  宿主 WS 同绑一会话互不踢」STOP 条件）；本修复只解锁第一项，其余验收另行安排。
- VSIX 命名随 version 走（脚本自动）；下次发版继续 patch 递增。
- `package-lock.json` 根 version 仍 0.2.0（lock 只钉依赖，npm ci 不受影响）；下次
  `npm install` 会同步，无需单独改。
- 审阅重点：manifest diff 应只有键名与版本两处；测试是否真在修复前红过（Step 2 输出）。

## 验收记录（2026-11-26，评审方亲跑/亲读）

| 场景 | 证据 |
|---|---|
| 单元级复现→修复 | /tmp 探针：base `c3176812` manifest＋编译产物 manifest.test.js → 3 tests / 1 pass / 2 fail（`invalid viewsContainers key: auxiliarybar`）；修复后 worktree `npm test` 106 pass / 0 fail |
| 真机复现→修复 | 修复前：owner 截图＋renderer.log:147（23:18:40）警告；修复后：`~/.forebrain/workspace/state/capture/383-right-panel-forebrain-tab.png` 右栏 `Chat \| Forebrain \| Codex` 三标签并列 |
| 面板可用 | `383-open-chat-panel.png`：Forebrain 标签激活、webview 渲染网关 Web UI（中文界面、composer、模型 qwen4-max-preview、Checklist 折叠分区）；exthost.log:70 `onView:forebrain.chat` 激活（01:17:55）；`2-Forebrain.log` gateway started pid 51580 @127.0.0.1:6060、GET / 200 |
| 回归 | Chat/Codex 标签仍在（同截图）；Explorer 兜底视图消失（容器注册＋视图渲染于 Forebrain 容器内＋重载后无兜底警告；`383-explorer-no-forebrain.png` 命名不符实为 Extensions 截图，已注明，不阻断） |
| composer 清理 | AppleScript 部分被拒期间命令面板字符串曾落入 webview composer，已清空：`383-composer-cleared.png` placeholder 恢复、未发送 |
| 门禁/安全 | compile=0 / typecheck=0 / 106 tests 0 fail；relay.ts sha256 恒等；`login#token=` 仅 extension.ts:233 |
| 装机 | `code --list-extensions --show-versions` → `forebrain.forebrain-vscode@0.2.1`；placeholderPanels 含 `workbench.view.extension.forebrain`（两视图） |

## Implementation Status

- **Verdict**: APPROVE（tech lead 独立复核，不采信执行器报告，判据全部亲跑）
- **Executor**: alibaba/deepseek-v4.1-flash-1017（llm-legacy-57524805b7e0c258）；worktree `.worktrees/exec-383-vscode-sidebar-key`，branch 同名，base `c3176812`，**0 commits**（owner 禁 commit，改动留工作区，合并由 owner 裁决）
- **Steps**: 1–6 done；Step 7 的 plans/ 落盘与索引由 reviewer 完成
- **Deviations（执行器报备＋评审按 merit 接受）**:
  1. 计划示例测试代码 `.flat()` 产 `unknown[]` 致 TS2345 → 改 `Record<string, ViewContainer[]>` 标注，三条断言逐字保留（typecheck 是 done 判据，此改必需；落地计划 Step 2 已收录正确写法）。
  2. 「最新窗口日志无警告」字面判据在 reload-only 下不可满足（renderer.log 同文件追加、旧行保留）→ 以重载边界后无新增警告为准；落地计划已改写该判据措辞。
  3. AppleScript 辅助访问部分被拒 → 验证改用日志＋截图；composer 副作用已清空并截图证实。
  4. package-lock.json 根 version 仍 0.2.0（npm ci 亲跑 exit 0）→ 留待下次 npm install 同步，out of scope。
  5. 评审发现：383-explorer-no-forebrain.png 实为 Extensions 面板截图（命名不符）；对应判据由其余三条证据成立，不阻断。
- **Not done / 遗留**: worktree 合并回 dev 待 owner 裁决；376 Step 9 / 379 Step 8 其余真机验收项仍 pending；旧 0.2.0 扩展目录待 VS Code 下次全量启动自清（已不在 extensions.json 登记）。