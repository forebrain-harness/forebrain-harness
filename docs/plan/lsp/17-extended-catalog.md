# 任务 17：扩展语言、备选服务器与诊断型服务器

> **执行者须知**：逐步执行；每一步运行「验证」命令并确认结果后再进入下一步。出现「STOP 条件」中的任何情况立即停止并汇报。完成后把 `docs/plan/lsp/README.md` 中本任务状态改为 `DONE`。
>
> **规范**：`docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md`。开始前读规范 §1.2、§6.1、§6.3、§6.4、§7.3（`workspace/configuration`）。
>
> **前置**：任务 06 已合入（`pkg/lsp/catalog/*.yaml` 有 11 个文件，`TestCatalog*` 存在）。任务 16 若已合入，本任务同时更新它的映射表（步骤 5）。
>
> **漂移检查（先运行）**：`ls pkg/lsp/catalog/ | wc -l` 应为 11；`git diff --stat 6305ea9..HEAD -- pkg/lsp/catalog.go pkg/lsp/resolve.go`。

## 状态

- 优先级：P2 · 工作量：M · 风险：LOW（目录条目默认不启用；只有用户启用后才会运行）
- 依赖：06
- 类别：direction
- 计划基于：`6305ea9`，2026-09-29

## 为什么

需求是「主流语言都必须支持，且不限于点名的 12 门」。本任务把目录扩展到 Ruby、Lua、Dart、Elixir、Zig、Haskell、OCaml、Bash、Vue、Svelte、Terraform、Clojure、Erlang、Nix、Gleam、YAML、Dockerfile，加入 7 个备选服务器（规范 §1.2 表格右列），以及 3 个只提供诊断的服务器（ruff、ESLint、Biome）。

诊断型服务器有一个新问题：ESLint、Biome 在没有配置文件的项目里会报「找不到配置」一类的错误。目录启用是按 agent 全局生效的，一个用户在 A 项目里需要 ESLint，不代表 B 项目也有 ESLint 配置。所以本任务给目录加一个字段 `require_root_marker`：为 true 时，只有文件所在目录到项目根之间存在该服务器的某个根标记，服务器才负责这个文件。

## 现状

- 任务 06：`CatalogEntry`（`pkg/lsp/catalog.go`）、`ServerConfig` 与 `ResolveServers`、`MatchFile`、`ServersForFile`、`ResolveRoot`（`pkg/lsp/resolve.go`）；目录测试 `TestCatalogLoads`、`TestCatalogFilenameMatchesID` 等（`pkg/lsp/catalog_test.go`）；`conflicts` 规则在 `ServersForFile` 的 primary 选择中生效。
- 任务 05：`workspace/configuration` 对 `section == ""` 返回整个 `settings`（ESLint 服务器按此取配置）。
- 规范 §6.1：安装配方是 argv 数组，不经 shell；`platforms` 缺省为全部平台。

## 范围

**新建：** `pkg/lsp/catalog/` 下 27 个 YAML 文件（见步骤 2–4）。

**修改：** `pkg/lsp/catalog.go`、`resolve.go`、`catalog_test.go`、`resolve_test.go`；若任务 16 已合入，`pkg/migrate/lsp.go` 与 `lsp_test.go`；`pkg/architecture/testdata/graph.json`。

**不要碰：** 已有的 11 个 YAML 文件（`conflicts` 需要写在新条目上，不改旧条目）。

## 步骤

### 步骤 1：`require_root_marker`

- `CatalogEntry` 加 `RequireRootMarker bool \`yaml:"require_root_marker"\``；`ServerConfig` 加 `RequireRootMarker bool`，`ResolveServers` 从目录条目复制（全局与项目条目不能设置它）。它参与配置指纹（照任务 06 的指纹规则，不需要额外处理）。
- `resolve.go` 加未导出函数 `hasRootMarker(absFile, projectRoot string, srv ServerConfig) bool`：从文件所在目录逐级向上到项目根（含），任一级存在 `RootMarkers` 中的任一项（含 `*` 的用 `filepath.Glob`）即为 true。
- `ServersForFile`：候选（primary 与 diagnostics 两类）中 `RequireRootMarker && !hasRootMarker(...)` 的条目剔除。`MatchFile` 不变（推荐与 `/lsp` 仍能看到它们）。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/lsp -run 'Resolve|ServersForFile' -count=1` → `ok`。

### 步骤 2：扩展语言（17 个文件）

每个文件内容**逐字**如下（可以调整空白）。写完后按步骤 6 核对官方文档。

`ruby-lsp.yaml`

```yaml
id: ruby-lsp
display_name: Ruby LSP
languages: [Ruby]
priority: 100
command: ruby-lsp
extension_to_language: {.rb: ruby, .rake: ruby, .gemspec: ruby, .ru: ruby}
filenames: {Gemfile: ruby, Rakefile: ruby}
root_markers: [Gemfile, .ruby-version, Rakefile]
env_passthrough: [GEM_HOME, GEM_PATH, BUNDLE_GEMFILE, RUBYOPT, RBENV_ROOT, RBENV_VERSION, RUBY_VERSION, ASDF_DATA_DIR, MISE_DATA_DIR]
install:
  - requires: gem
    argv: [gem, install, ruby-lsp]
readiness: progress
project_writes: [.ruby-lsp]
notes: "Install with: gem install ruby-lsp"
```

`lua-language-server.yaml`

```yaml
id: lua-language-server
display_name: Lua Language Server
languages: [Lua]
priority: 100
command: lua-language-server
extension_to_language: {.lua: lua}
root_markers: [.luarc.json, .luarc.jsonc, .luacheckrc, .stylua.toml, stylua.toml, selene.toml]
detect:
  version_args: [--version]
install:
  - requires: brew
    platforms: [darwin, linux]
    argv: [brew, install, lua-language-server]
readiness: progress
notes: "Install from your package manager or the LuaLS releases page."
```

`dart.yaml`

```yaml
id: dart
display_name: Dart analysis server
languages: [Dart]
priority: 100
command: dart
args: [language-server, --protocol=lsp]
extension_to_language: {.dart: dart}
root_markers: [pubspec.yaml]
env_passthrough: [FLUTTER_ROOT, PUB_CACHE]
detect:
  version_args: [--version]
startup_timeout: 120
readiness: progress
notes: "Comes with the Dart or Flutter SDK."
```

`elixir-ls.yaml`

```yaml
id: elixir-ls
display_name: ElixirLS
languages: [Elixir]
priority: 100
command: elixir-ls
extension_to_language: {.ex: elixir, .exs: elixir}
root_markers: [mix.exs]
env_passthrough: [MIX_ENV, MIX_HOME, HEX_HOME, ERL_LIBS, ASDF_DATA_DIR, MISE_DATA_DIR]
install:
  - requires: brew
    platforms: [darwin, linux]
    argv: [brew, install, elixir-ls]
startup_timeout: 180
readiness: progress
project_writes: [.elixir_ls]
notes: "Install with brew, or download a release and put its language_server.sh on the PATH as elixir-ls."
```

`zls.yaml`

```yaml
id: zls
display_name: ZLS
languages: [Zig]
priority: 100
command: zls
extension_to_language: {.zig: zig, .zon: zig}
root_markers: [build.zig, build.zig.zon]
detect:
  version_args: [--version]
install:
  - requires: brew
    platforms: [darwin, linux]
    argv: [brew, install, zls]
readiness: none
notes: "Use the ZLS release that matches your Zig version."
```

`haskell-language-server.yaml`

```yaml
id: haskell-language-server
display_name: Haskell Language Server
languages: [Haskell]
priority: 100
command: haskell-language-server-wrapper
args: [--lsp]
extension_to_language: {.hs: haskell, .lhs: literate haskell}
root_markers: [hie.yaml, stack.yaml, cabal.project, package.yaml, "*.cabal"]
env_passthrough: [GHCUP_INSTALL_BASE_PREFIX, STACK_ROOT, CABAL_DIR]
detect:
  extra_dirs: ["~/.ghcup/bin"]
  version_args: [--version]
install:
  - requires: ghcup
    argv: [ghcup, install, hls]
startup_timeout: 300
readiness: progress
notes: "Install with: ghcup install hls"
```

`ocamllsp.yaml`

```yaml
id: ocamllsp
display_name: OCaml LSP
languages: [OCaml]
priority: 100
command: ocamllsp
extension_to_language: {.ml: ocaml, .mli: ocaml.interface}
root_markers: [dune-project, dune-workspace, "*.opam", esy.json]
env_passthrough: [OPAMROOT, OPAM_SWITCH_PREFIX, CAML_LD_LIBRARY_PATH, OCAML_TOPLEVEL_PATH]
install:
  - requires: opam
    argv: [opam, install, --yes, ocaml-lsp-server]
readiness: progress
project_writes: [_build]
notes: "Install into the project's opam switch: opam install ocaml-lsp-server"
```

`bash-language-server.yaml`

```yaml
id: bash-language-server
display_name: Bash Language Server
languages: [Bash]
priority: 100
command: bash-language-server
args: [start]
extension_to_language: {.sh: shellscript, .bash: shellscript}
filenames: {.bashrc: shellscript, .bash_profile: shellscript}
root_markers: []
install:
  - requires: npm
    argv: [npm, install, -g, bash-language-server]
readiness: none
notes: "Uses shellcheck for diagnostics when it is on the PATH."
```

`vue-language-server.yaml`

```yaml
id: vue-language-server
display_name: Vue Language Tools
languages: [Vue]
priority: 100
command: vue-language-server
args: [--stdio]
extension_to_language: {.vue: vue}
root_markers: [package.json]
env_passthrough: [NODE_PATH, NODE_OPTIONS]
install:
  - requires: npm
    argv: [npm, install, -g, "@vue/language-server", typescript]
readiness: progress
notes: "Serves .vue files. TypeScript inside .vue files also needs @vue/typescript-plugin in the TypeScript server's plugins."
```

`svelte-language-server.yaml`

```yaml
id: svelte-language-server
display_name: Svelte Language Server
languages: [Svelte]
priority: 100
command: svelteserver
args: [--stdio]
extension_to_language: {.svelte: svelte}
root_markers: [svelte.config.js, svelte.config.ts, package.json]
env_passthrough: [NODE_PATH, NODE_OPTIONS]
install:
  - requires: npm
    argv: [npm, install, -g, svelte-language-server]
readiness: progress
notes: "Install with: npm install -g svelte-language-server"
```

`terraform-ls.yaml`

```yaml
id: terraform-ls
display_name: Terraform Language Server
languages: [Terraform]
priority: 100
command: terraform-ls
args: [serve]
extension_to_language: {.tf: terraform, .tfvars: terraform-vars}
root_markers: [.terraform.lock.hcl, .terraform]
env_passthrough: [TF_CLI_CONFIG_FILE, TF_PLUGIN_CACHE_DIR, TF_DATA_DIR]
detect:
  version_args: [version]
install:
  - requires: brew
    platforms: [darwin, linux]
    argv: [brew, install, hashicorp/tap/terraform-ls]
readiness: progress
notes: "Install with brew from the hashicorp tap, or download terraform-ls from releases.hashicorp.com."
```

`clojure-lsp.yaml`

```yaml
id: clojure-lsp
display_name: clojure-lsp
languages: [Clojure]
priority: 100
command: clojure-lsp
extension_to_language: {.clj: clojure, .cljs: clojure, .cljc: clojure, .edn: clojure, .bb: clojure}
root_markers: [deps.edn, project.clj, shadow-cljs.edn, bb.edn, build.boot]
env_passthrough: [JAVA_HOME, CLJ_CONFIG]
detect:
  version_args: [--version]
install:
  - requires: brew
    platforms: [darwin, linux]
    argv: [brew, install, clojure-lsp/brew/clojure-lsp-native]
startup_timeout: 180
readiness: progress
project_writes: [.lsp/.cache, .clj-kondo/.cache]
notes: "Install with brew, or download the native binary from the clojure-lsp releases."
```

`elp.yaml`

```yaml
id: elp
display_name: Erlang Language Platform
languages: [Erlang]
priority: 100
command: elp
args: [server]
extension_to_language: {.erl: erlang, .hrl: erlang}
filenames: {rebar.config: erlang}
root_markers: [rebar.config, erlang.mk, .elp.toml]
env_passthrough: [ERL_LIBS, REBAR_CACHE_DIR]
detect:
  version_args: [version]
startup_timeout: 180
readiness: progress
notes: "Download ELP from the WhatsApp/erlang-language-platform releases and put elp on the PATH."
```

`nil.yaml`

```yaml
id: nil
display_name: nil
languages: [Nix]
priority: 100
command: nil
extension_to_language: {.nix: nix}
root_markers: [flake.nix, default.nix, shell.nix]
detect:
  version_args: [--version]
install:
  - requires: nix
    argv: [nix, profile, install, "nixpkgs#nil"]
readiness: none
notes: "Install with nix, or build it from the oxalica/nil repository."
```

`gleam.yaml`

```yaml
id: gleam
display_name: Gleam language server
languages: [Gleam]
priority: 100
command: gleam
args: [lsp]
extension_to_language: {.gleam: gleam}
root_markers: [gleam.toml]
detect:
  version_args: [--version]
readiness: none
notes: "Comes with the gleam binary."
```

`yaml-language-server.yaml`

```yaml
id: yaml-language-server
display_name: YAML Language Server
languages: [YAML]
priority: 100
command: yaml-language-server
args: [--stdio]
extension_to_language: {.yaml: yaml, .yml: yaml}
root_markers: []
install:
  - requires: npm
    argv: [npm, install, -g, yaml-language-server]
readiness: none
settings:
  yaml: {validate: true, schemaStore: {enable: false}}
notes: "Schema downloads from schemastore.org are off by default; set lsp.servers.yaml-language-server.settings.yaml.schemaStore.enable to true to allow them."
```

`dockerfile-language-server.yaml`

```yaml
id: dockerfile-language-server
display_name: Dockerfile Language Server
languages: [Dockerfile]
priority: 100
command: docker-langserver
args: [--stdio]
extension_to_language: {.dockerfile: dockerfile}
filenames: {Dockerfile: dockerfile, Containerfile: dockerfile}
root_markers: []
install:
  - requires: npm
    argv: [npm, install, -g, dockerfile-language-server-nodejs]
readiness: none
notes: "Install with: npm install -g dockerfile-language-server-nodejs"
```

### 步骤 3：备选服务器（7 个文件）

优先级都低于默认服务器，所以只有用户停用默认服务器、或满足 `conflicts` 时才被选中（规范 §6.3）。

`ccls.yaml`

```yaml
id: ccls
display_name: ccls
languages: [C, C++, Objective-C]
priority: 50
command: ccls
extension_to_language: {.c: c, .h: c, .cc: cpp, .cpp: cpp, .cxx: cpp, .hpp: cpp, .hh: cpp, .hxx: cpp, .m: objective-c, .mm: objective-cpp}
root_markers: [compile_commands.json, .ccls, .ccls-root]
initialization_options:
  cache: {directory: "${LSP_CACHE_DIR}"}
detect:
  version_args: [--version]
readiness: progress
notes: "Alternative to clangd; chosen only when clangd is disabled. Needs compile_commands.json."
```

`kotlin-language-server.yaml`

```yaml
id: kotlin-language-server
display_name: Kotlin Language Server (fwcd)
languages: [Kotlin]
priority: 50
command: kotlin-language-server
extension_to_language: {.kt: kotlin, .kts: kotlin}
root_markers: [settings.gradle.kts, settings.gradle, build.gradle.kts, build.gradle, pom.xml]
env_passthrough: [JAVA_HOME, GRADLE_USER_HOME, GRADLE_OPTS]
install:
  - requires: brew
    platforms: [darwin, linux]
    argv: [brew, install, kotlin-language-server]
startup_timeout: 180
readiness: progress
initialization_options:
  storagePath: "${LSP_CACHE_DIR}"
notes: "Alternative to kotlin-lsp; chosen only when kotlin-lsp is disabled."
```

`vtsls.yaml`

```yaml
id: vtsls
display_name: vtsls
languages: [TypeScript, JavaScript]
priority: 50
command: vtsls
args: [--stdio]
extension_to_language: {.ts: typescript, .mts: typescript, .cts: typescript, .tsx: typescriptreact, .js: javascript, .mjs: javascript, .cjs: javascript, .jsx: javascriptreact}
root_markers: [tsconfig.json, jsconfig.json, package.json]
env_passthrough: [NODE_PATH, NODE_OPTIONS]
install:
  - requires: npm
    argv: [npm, install, -g, "@vtsls/language-server"]
readiness: progress
notes: "Alternative to typescript-language-server; chosen only when that server is disabled."
```

`deno.yaml`

```yaml
id: deno
display_name: Deno language server
languages: [TypeScript, JavaScript]
priority: 50
command: deno
args: [lsp]
extension_to_language: {.ts: typescript, .mts: typescript, .tsx: typescriptreact, .js: javascript, .mjs: javascript, .jsx: javascriptreact}
root_markers: [deno.json, deno.jsonc]
env_passthrough: [DENO_DIR, DENO_INSTALL_ROOT]
detect:
  version_args: [--version]
readiness: none
initialization_options: {enable: true, lint: true}
conflicts:
  - with: typescript-language-server
    when_root_marker: [deno.json, deno.jsonc]
  - with: vtsls
    when_root_marker: [deno.json, deno.jsonc]
notes: "Chosen over the TypeScript servers in projects with deno.json."
```

`phpactor.yaml`

```yaml
id: phpactor
display_name: Phpactor
languages: [PHP]
priority: 50
command: phpactor
args: [language-server]
extension_to_language: {.php: php, .phtml: php}
root_markers: [composer.json, .phpactor.json, .phpactor.yml]
detect:
  version_args: [--version]
readiness: progress
notes: "Alternative to intelephense; chosen only when intelephense is disabled."
```

`basedpyright.yaml`

```yaml
id: basedpyright
display_name: basedpyright
languages: [Python]
priority: 60
command: basedpyright-langserver
args: [--stdio]
extension_to_language: {.py: python, .pyi: python}
root_markers: [pyproject.toml, pyrightconfig.json, setup.py, setup.cfg, requirements.txt, Pipfile]
env_passthrough: [VIRTUAL_ENV, CONDA_PREFIX, PYTHONPATH]
install:
  - requires: pipx
    argv: [pipx, install, basedpyright]
readiness: progress
notes: "Alternative to pyright; chosen only when pyright is disabled."
```

`pylsp.yaml`

```yaml
id: pylsp
display_name: python-lsp-server
languages: [Python]
priority: 40
command: pylsp
extension_to_language: {.py: python}
root_markers: [pyproject.toml, setup.py, setup.cfg, requirements.txt, Pipfile]
env_passthrough: [VIRTUAL_ENV, CONDA_PREFIX, PYTHONPATH]
install:
  - requires: pipx
    argv: [pipx, install, python-lsp-server]
readiness: none
notes: "Alternative Python server; chosen only when pyright and basedpyright are disabled."
```

### 步骤 4：诊断型服务器（3 个文件）

`ruff.yaml`

```yaml
id: ruff
display_name: Ruff
languages: [Python]
role: diagnostics
priority: 100
command: ruff
args: [server]
extension_to_language: {.py: python, .pyi: python}
root_markers: [ruff.toml, .ruff.toml, pyproject.toml]
require_root_marker: true
env_passthrough: [VIRTUAL_ENV]
detect:
  version_args: [--version]
install:
  - requires: pipx
    argv: [pipx, install, ruff]
readiness: none
notes: "Adds Ruff's lint findings next to the Python server's diagnostics."
```

`eslint.yaml`

```yaml
id: eslint
display_name: ESLint
languages: [JavaScript, TypeScript]
role: diagnostics
priority: 100
command: vscode-eslint-language-server
args: [--stdio]
extension_to_language: {.js: javascript, .mjs: javascript, .cjs: javascript, .jsx: javascriptreact, .ts: typescript, .mts: typescript, .cts: typescript, .tsx: typescriptreact}
root_markers: [eslint.config.js, eslint.config.mjs, eslint.config.cjs, eslint.config.ts, eslint.config.mts, eslint.config.cts, .eslintrc, .eslintrc.js, .eslintrc.cjs, .eslintrc.json, .eslintrc.yml, .eslintrc.yaml]
require_root_marker: true
env_passthrough: [NODE_PATH, NODE_OPTIONS]
install:
  - requires: npm
    argv: [npm, install, -g, vscode-langservers-extracted]
readiness: none
settings:
  validate: "on"
  run: onType
  quiet: false
  onIgnoredFiles: "off"
  useESLintClass: false
  nodePath: null
  workingDirectory: {mode: auto}
  problems: {shortenToSingleLine: false}
  rulesCustomizations: []
  codeAction:
    disableRuleComment: {enable: false, location: separateLine}
    showDocumentation: {enable: false}
  codeActionOnSave: {enable: false, mode: all}
  format: false
  experimental: {}
notes: "Uses the project's own eslint package; run npm install in the project first."
```

`biome.yaml`

```yaml
id: biome
display_name: Biome
languages: [JavaScript, TypeScript, JSON, CSS]
role: diagnostics
priority: 90
command: biome
args: [lsp-proxy]
extension_to_language: {.js: javascript, .mjs: javascript, .cjs: javascript, .jsx: javascriptreact, .ts: typescript, .mts: typescript, .cts: typescript, .tsx: typescriptreact, .json: json, .jsonc: jsonc, .css: css}
root_markers: [biome.json, biome.jsonc]
require_root_marker: true
env_passthrough: [NODE_PATH]
detect:
  version_args: [--version]
install:
  - requires: npm
    argv: [npm, install, -g, "@biomejs/biome"]
readiness: none
notes: "Adds Biome's lint findings in projects with biome.json."
```

### 步骤 5：`/migrate` 映射（仅当任务 16 已合入）

`pkg/migrate/lsp.go` 的 `officialLSPPlugins` 中 `"lua-lsp": ""` 改为 `"lua-language-server"`，`"ruby-lsp": ""` 改为 `"ruby-lsp"`，删除对应注释；`lsp_test.go` 补一个 `lua-lsp@claude-plugins-official` → `lsp.servers.lua-language-server.enabled: true` 的用例。任务 16 尚未合入时跳过本步，并在任务 16 的计划「维护说明」里已有提示。

### 步骤 6：核对官方文档

对每个新条目，打开该服务器的官方 README / 安装文档，核对四项：`command` 与 `args`（stdio 模式的启动方式）、安装命令、版本参数（`version_args` 为空表示不检查版本）、`project_writes`。

- 与本计划不符时**以官方文档为准**修改 YAML，并在 PR 描述里逐条列出「条目 · 字段 · 原值 → 新值 · 依据链接」。
- 本机装有该服务器时，额外运行 `forebrain lsp doctor --server <id> --project <一个该语言的最小项目>`，把结果贴进 PR 描述。

### 步骤 7：全量检查

**验证**：`gofmt`、`go vet ./...` 干净；`ls pkg/lsp/catalog/*.yaml | wc -l` 为 38；`scripts/package-graph.sh` 后提交 `graph.json`；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` → 全部 `ok`。

## 测试计划

- `pkg/lsp/catalog_test.go`
  - 已有的 `TestCatalogLoads`、`TestCatalogFilenameMatchesID` 自动覆盖新文件。
  - `TestCatalogCoversRequiredLanguages` 扩展：除规范 §1.2 必选的 12 门外，再断言 Ruby、Lua、Dart、Elixir、Zig、Haskell、OCaml、Bash、Vue、Svelte、Terraform、Clojure、Erlang、Nix、Gleam、YAML、Dockerfile 各至少有一个 primary 条目。
  - 已有的 `TestCatalogEntriesAreComplete`（每个条目必须有 `notes`）与 `TestCatalogNoUnresolvedPrimaryConflicts`（同扩展名的 primary 两两优先级不同或有 `conflicts` 覆盖：`vtsls` 与 `deno` 同为 50，由 `deno` 的 `conflicts` 覆盖）自动覆盖新文件。
  - `TestCatalogDefaultsWin`：对 `.ts`、`.py`、`.kt`、`.php`、`.c`，优先级最高的 primary 分别是 `typescript-language-server`、`pyright`、`kotlin-lsp`、`intelephense`、`clangd`。
  - `TestCatalogDiagnosticsServersRequireMarker`：`role: diagnostics` 的条目都 `require_root_marker: true` 且 `root_markers` 非空。
  - `TestCatalogInstallArgv`：所有安装配方 `argv` 非空、第一项等于 `requires` 或是它的别名（`npm`、`gem`、`brew`、`pipx`、`opam`、`ghcup`、`nix`、`go`、`rustup`、`dotnet`、`cs` 之一），不含 shell 元字符（`;`、`|`、`&&`、`` ` ``、`$(`）。
- `pkg/lsp/resolve_test.go`
  - `TestRequireRootMarker`：ESLint 已启用；项目根下没有 ESLint 配置 → `ServersForFile("a.ts")` 的 diagnostics 为空；子目录 `web/` 有 `eslint.config.js` → `web/a.ts` 的 diagnostics 含 `eslint`、根下的 `a.ts` 不含。
  - `TestDenoConflictWins`：`deno` 与 `typescript-language-server` 都启用；项目有 `deno.json` → primary 为 `deno`；没有 → `typescript-language-server`。
  - `TestAlternatesLoseByPriority`：`pyright` 与 `basedpyright` 都启用 → primary 为 `pyright`。

## 完成判据

- [ ] 目录共 38 个 YAML 文件；上述测试全部通过
- [ ] PR 描述里有步骤 6 的核对记录
- [ ] `graph.json` 已提交；全量测试通过
- [ ] README 中任务 17 状态为 `DONE`

## STOP 条件

- 某个服务器没有 stdio 模式（只能走 TCP/socket）。
- 某个服务器需要客户端在运行时动态计算的 `settings`，而任务 05 的 `workspace/configuration` 处理无法提供（例如需要每个工作区不同的绝对路径）。
- 需要修改已有 11 个条目的内容。

## 维护说明

- 新增目录条目的清单：YAML 文件（文件名 = id）、必要时 `conflicts`、诊断型必须 `require_root_marker`、在 `docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md` §1.2 的表格里登记、需要 doctor 提示时改任务 12 的提示表、需要进 CI 时加集成用例。
- 评审重点：优先级没有并列；安装配方不经 shell；诊断型服务器不会在没有配置的项目里报错。
