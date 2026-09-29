# 任务 18：文档

> **执行者须知**：逐步执行；每一步运行「验证」命令并确认结果后再进入下一步。出现「STOP 条件」中的任何情况立即停止并汇报。完成后把 `docs/plan/lsp/README.md` 中本任务状态改为 `DONE`。
>
> **规范**：`docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md`。开始前通读规范 §0、§1、§5、§8.3.2、§9、§10、§11。
>
> **前置**：任务 01–17 全部合入。先确认：`grep -c "| DONE |" docs/plan/lsp/README.md` 为 17；否则只为已合入的部分写文档，并在 PR 描述里列出未覆盖的任务。
>
> **漂移检查（先运行）**：`git diff --stat 6305ea9..HEAD -- README.md FOREBRAIN.md .gitmessage`。

## 状态

- 优先级：P2 · 工作量：S · 风险：LOW（只改文档）
- 依赖：01–17
- 类别：direction
- 计划基于：`6305ea9`，2026-09-29

## 为什么

用户需要知道：语言服务器是什么时候、以什么身份运行的（受信任项目、沙箱外），怎么启用和安装，编辑工具为什么有时会多等一会儿（等待窗口），以及出问题时用什么命令排查。贡献者需要知道 `pkg/lsp` 在架构里的位置。

## 现状

- `README.md`：`## Features` → `### In the terminal` 的命令表（:121-138，`/mcp` 在 :130）；`### In the browser`（:140，列出 Web 页面）；`### Safety`（:156）；`## Configuration`（:163）。
- `FOREBRAIN.md`：`## Architecture`（:31，逐包一句话）；`## Project-level MCP and projects`（:53）；`## Migrating from another agent`（:57，列出迁移类别）。
- `.gitmessage` 第 5-7 行列出常用 scope（`state, tui, gateway, run, turn, memory, migrate, tool, process, assembly, llm, mcp, config, session, skill, channel, frontend, npm, docs`）。
- 面向用户的完整文档站在另一个仓库（`README.md` 链接的 `forebrain-harness.github.io`），**本任务不改它**。

## 范围

**修改：** `README.md`、`FOREBRAIN.md`、`.gitmessage`、`docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md`（只改文件头的「状态」一行）。

**不要碰：** 任何代码；文档站仓库。

## 步骤

### 步骤 1：`README.md`

1. 命令表在 `/mcp` 之后加一行：`| \`/lsp\`         | Language servers: status, enable, install, restart                     |`（列宽与表中其他行对齐）。
2. `/migrate` 一行改为 `Import sessions, memories, skills, MCP servers and language servers from another agent`。
3. `### In the browser` 的页面列表在 `MCP servers` 之后加 `language servers`。
4. 在 `### Safety` 之前新增一节（英文，逐字可调措辞，但必须包含下列每一个事实）：

   ```markdown
   ### Code intelligence

   Language servers give the agent compiler-grade feedback: after an edit, the
   tool result lists the errors and warnings the edit introduced, and the `lsp`
   tool finds definitions, references, types and symbols without text search.
   The built-in catalog covers Java, Go, Rust, C, C++, Kotlin, Swift, Scala, C#,
   TypeScript, PHP and Python, plus Ruby, Lua, Dart, Elixir, Zig, Haskell,
   OCaml, Bash, Vue, Svelte, Terraform, Clojure, Erlang, Nix, Gleam, YAML and
   Dockerfile; any other server can be added under `lsp.servers` in
   `forebrain.yaml`.

   Servers run on your machine, outside the sandbox, and only in trusted
   projects — opening a project with a language server is like building it.
   None is enabled until you say so: the first time the agent edits a file of a
   language with an available server, Forebrain Harness asks. `/lsp` (or
   `forebrain lsp list`) shows every server; `forebrain lsp doctor` checks one
   end to end.

   An edit waits at most `lsp.diagnostics.wait_ms` (2.5 s by default) for the
   server's answer and returns as soon as it arrives; diagnostics that come
   later reach the agent with its next request.
   ```

### 步骤 2：`FOREBRAIN.md`

1. `## Architecture`：
   - `cmd/forebrain` 一行的子命令列表加 `lsp`。
   - 「Supporting:」那一行之后新增一行：`- \`pkg/lsp\` — language-server runtime (Layer 2): the built-in catalog (\`pkg/lsp/catalog/*.yaml\`), JSON-RPC and protocol types, one process pool per forebrain process, a Manager per runner that implements \`tool.CodeIntelligence\`/\`tool.CodeIntelControl\`. Only \`pkg/process\` and \`cmd/forebrain\` import it; tools and surfaces go through the ports in \`pkg/tool/search.go\` and the DTOs in \`pkg/event/lsp.go\`.`
2. 在 `## Project-level MCP and projects` 之后新增：

   ```markdown
   ## Language servers

   Design and task plans: `docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md` and `docs/plan/lsp/`. Rules that keep it safe to change:

   - Never start a server on the `Runner.Load` path or in a first-frame path; everything starts lazily or in the background.
   - The `lsp` tool is registered once per session (`Deps.CodeIntelTool`); its description and schema are constants. Enabling, installing or restarting a server never changes the tool table.
   - Model-visible texts (tool description, diagnostics block, late-diagnostics reminder, error messages) are verbatim from the spec's appendices B and C and have golden tests.
   - Unit tests use the fake server in `pkg/lsp/instance_test.go`; real servers run only with `FOREBRAIN_LSP_INTEGRATION` set (`.github/workflows/lsp-integration.yml`).
   - Project entries (`<root>/.forebrain/lsp_servers.yaml`) apply only in trusted, version-controlled projects and only after per-entry consent.
   ```

3. `## Migrating from another agent`：类别列表加 `language-server plugins`；`--only` 示例里加 `lsp`；在「Project-level configuration…」句子之前加一句：`Enabled Claude Code LSP plugins become enabled catalog servers (official plugins) or custom \`lsp.servers\` entries (other plugins).`

### 步骤 3：`.gitmessage`

scope 列表在 `mcp,` 之后加 `lsp,`（保持每行不超过 80 列；需要换行时照现有缩进）。

**验证**：`scripts/check-commit-message.sh .gitmessage` → 退出码 0（模板里的注释行不影响检查；若脚本要求非空标题而失败，改为只目测确认格式，不改脚本）。

### 步骤 4：规范状态

规范文件头的「状态」一行（以 `> 状态：` 开头）整行替换为：

```markdown
> 状态：**已实施**（任务 01–18，见 [`docs/plan/lsp/README.md`](lsp/README.md)）。后续改动先改本规范再改代码
```

### 步骤 5：检查

**验证**：

- `git diff --stat` 只列出范围内的四个文件。
- `grep -n "/lsp" README.md` 至少两处；`grep -n "pkg/lsp" FOREBRAIN.md` 至少一处。
- Markdown 表格列宽对齐（目测 `README.md` 命令表）。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`（确认文档改动没有意外触碰被测试引用的文件）。

## 测试计划

无代码改动。PR 描述里写：README 新节的每一句都能在规范或实现里找到依据（逐句列出对应的规范章节号）。

## 完成判据

- [ ] README、FOREBRAIN.md、.gitmessage、规范状态行已更新
- [ ] PR 描述里有「文档站需要同步的页面」清单（配置参考加 `lsp` 段与 `features.lsp`；CLI 参考加 `forebrain lsp`；安全模型加「语言服务器在沙箱外运行」；项目配置页加 `lsp_servers.yaml`），交给维护者在文档站仓库处理
- [ ] README 中任务 18 状态为 `DONE`

## STOP 条件

- 发现实现与规范不一致（例如默认等待时长、目录语言列表不同）：不要按实现改文档，也不要按规范改代码，停下汇报差异。

## 维护说明

- `README.md` 的语言列表与 `pkg/lsp/catalog/` 必须一致；目录增删条目时同步。
- 评审重点：安全表述（「沙箱外」「仅受信任项目」「默认不启用」）三点都在。
