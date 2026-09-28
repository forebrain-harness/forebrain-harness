# Plan 008：自动发版——release-please、Web UI 随标签入库、多平台构建、GitHub Release 与 npm 发布

> **执行者须知**：逐步执行，每一步都要跑"验证"命令并确认结果与预期一致后才进入下一步。
> 出现"STOP 条件"中的任何一种情况，立即停止并汇报，不要自行发挥。完成后把
> `docs/plan/OSS_AUTOMATION/README.md` 里本计划的状态改成 DONE。
>
> **提交（里程碑 M7）**：本地步骤全部通过后，按 `README.md` 全局规则 1 提交**一次**：标题见 README 的
> "里程碑与提交标题"表，正文按 `.gitmessage` 模板（Why / What / Prompt cache / Verification，外加
> `Refs: docs/plan/OSS_AUTOMATION/<本文件名>`），用 `git commit -s -F <message-file>`；只 `git add` 本计划"范围"内的文件。
> 不得 `--amend`、`rebase`、`push --force`。开工前确认 `git status --short` 干净、`git log -1` 是上一个里程碑。
>
> **真实仓库实测**：按 README 的关卡推送后，在 `git@github.com:forebrain-harness/forebrain-harness.git` 上完成本文件"真实仓库实测"一节，
> 证据写入 `LIVE_TEST_LOG.md`。实测通过前本计划状态只能是 `AWAITING PUSH`。
>
> **不得手动发布**：不 `npm publish`，也不 `gh release create/upload`，发布只能由 `release.yml` 完成。release PR 由 owner 合并。
>
> **漂移检查（先做）**：本计划编写时仓库还没有提交，M0 之后才有历史。开工前逐条核对"现状"中的摘录与实际文件一致，并用
> `git log --oneline -- <范围内路径>` 查看 M0 之后是否有别的提交改过这些文件；有且与摘录不符，就按 STOP 处理。

## 状态

| 项 | 值 |
| --- | --- |
| 优先级 | P1 |
| 工作量 | L |
| 风险 | MED：工作流只能在推到 GitHub 后真实验证 |
| 依赖 | 001、002、005（词典打包脚本）、006（提交规范）、007（钉版规则） |
| 类别 | dx / direction |
| 编写于 | 2026-09-28，基于未提交的工作区 |
| owner 决策 | **D1**（0.x）、**D2**（GitHub App）、**D3**（Intel mac 交叉编译）、**D5**（npm 发布认证）按推荐值编写 |

## 为什么要做

owner 决定：
- 用 release-please 全自动发版；
- 提供 npm install 和 go install 两种安装方式；
- go install 所需的 Web UI 由"发版 CI 把构建好的 UI 提交进该版本源码，供 go:embed"；
- 词典作为 Release 资产供首次下载（下载端见计划 005）。

目标流程：
1. 维护者合并 PR；
2. release-please 自动维护一个 release PR，包含版本号和 CHANGELOG，并由本工作流把重新构建的 Web UI 提交进同一个 PR；
3. 维护者合并 release PR，这是发版的唯一人工动作；
4. 自动打 tag、创建 GitHub Release、构建五个平台的二进制和 npm 包、上传二进制归档与词典包、发布 npm；
5. 最后在干净环境里真实执行一次 `go install …@vX.Y.Z` 作为验收。

### 为什么 Web UI 要进 main，而不是只进 tag

release-please 靠 Release 所在的提交判断上一次发版的位置；这个提交必须在 main 的历史上，否则它会把全部历史当作
"未发版提交"，CHANGELOG 会被污染。所以不能把 UI 放进一个脱离 main 的 tag 提交。

可行的做法是：UI 只由 release PR 更新，squash 合并后进入 main，tag 打在这个提交上。其他 PR 不允许改
`pkg/gateway/dist`，由 CI 守护。仓库体积方面：vite 产物的文件名带内容哈希，没变的 chunk（例如 shiki 语法包）
在不同版本之间是同一个 blob，git 只存一份；每次发版新增的主要是应用代码的 chunk。

## 现状

- `.gitignore`：
  ```
  dist
  # Embedded frontend build output: ignore built assets but keep the placeholder
  # so `go:embed all:dist` and `go build` work on a clean checkout.
  !pkg/gateway/dist/
  pkg/gateway/dist/*
  !pkg/gateway/dist/.gitkeep
  ```
- `pkg/gateway/embed.go:14`：`//go:embed all:dist`。本地构建的 dist 约 13 MB（545 个 assets，最大的是 shiki 语法包）。
- `frontend/vite.config.ts:47–48`：`outDir: resolve(__dirname, '../pkg/gateway/dist')`，`emptyOutDir: false`
  （保留 .gitkeep）。
- `Makefile`：`ui:` 先删除 dist 下除 `.gitkeep` 外的所有文件，再 `corepack pnpm install --frozen-lockfile`（仅在没有
  node_modules 时）并 `pnpm build`；`clean:` 也会删除 dist 内容；`release:` 调用 `npm/scripts/build-platform-packages.sh`。
- `npm/scripts/build-platform-packages.sh`（计划 002 之后）：
  - 默认版本从 `VERSION` 读取；
  - **每次运行都会先删掉并重建前端**（第 40–43 行的 `find … -exec rm` 和 `pnpm build`）；
  - 然后按 `FOREBRAIN_BUILD_TARGETS` 逐个目标用 CGO 编译。非本机目标读取 `FOREBRAIN_CC_<OS>_<ARCH>` 作为 CC；
  - 产物放在 `npm/dist/@forebrain-harness/<pkg>/vendor/<triple>/bin/{forebrain[.exe],dict/}` 和 `package.json`。
- 平台与包名、triple 的对应关系：darwin/arm64→`forebrain-darwin-arm64`/`aarch64-apple-darwin`，darwin/amd64→`forebrain-darwin-x64`/`x86_64-apple-darwin`，
  linux/amd64→`forebrain-linux-x64`/`x86_64-unknown-linux-gnu`，linux/arm64→`forebrain-linux-arm64`/`aarch64-unknown-linux-gnu`，
  windows/amd64→`forebrain-win32-x64`/`x86_64-pc-windows-gnu`。
- `npm/package.json`：launcher `@forebrain-harness/forebrain`，`files: ["bin/", "README.md"]`（`npm/README.md` 目前不存在，
  由计划 012 补上）。
- 计划 002 之后：`VERSION`=`0.0.0`，`scripts/check-release-versions.sh` 校验六处版本一致。
- 计划 005 之后：`scripts/build-dict-bundle.sh <out-dir>` 产出 `forebrain-dict.tar.gz`；
  `FOREBRAIN_RELEASE_DICT_CHECK=<tag> go test -tags fts5 ./pkg/memory -run TestReleasedDictionaryBundleMatchesDictionaryFiles`
  会核对已发布的词典包。
- 仓库是公开仓库，目前为空（还没推送），默认分支将是 `main`。

## 设计

### 需要 owner 在 GitHub 上准备的东西（计划 012 写成文档和脚本，本计划只引用这些名字）

- 一个 GitHub App（D2），安装到本仓库，权限：Contents 读写、Pull requests 读写、Issues 读写。用它的 token 推送
  和开 PR，才能触发 CI（`GITHUB_TOKEN` 推送的提交不会触发其他工作流）。
- 仓库变量 `FOREBRAIN_APP_ID`；仓库密钥 `FOREBRAIN_APP_PRIVATE_KEY`、`NPM_TOKEN`（D5）。
- 仓库环境 `npm`，可以配置必需审批人，作为发布前的最后一道闸。

### 文件清单

| 文件 | 作用 |
| --- | --- |
| `release-please-config.json` | release-please 配置 |
| `.release-please-manifest.json` | 当前已发布版本（`{".": "0.0.0"}`） |
| `.github/workflows/release.yml` | 发版流水线 |
| `scripts/check-webui-dist.sh` | 守护 `pkg/gateway/dist`：普通 PR 不许改；release PR 必须与源码一致 |
| `scripts/package-release-archive.sh` | 把一个平台包打成 GitHub Release 归档 |
| `.gitignore` / `.gitattributes` | dist 改为入库，标记为生成物 |
| `Makefile`、`npm/scripts/build-platform-packages.sh` | UI 构建从打包脚本里拆出去 |
| `.github/workflows/ci.yml` | 新增 `webui-dist` job 与 `check-release-versions` 已在 002 加过 |

## 需要的命令

| 用途 | 命令 | 成功时 |
| --- | --- | --- |
| JSON 语法 | `jq empty release-please-config.json .release-please-manifest.json` | exit 0 |
| workflow lint | `go run github.com/rhysd/actionlint/cmd/actionlint@<计划 007 定的版本>` | 无输出 |
| 本机打包 | `make release`（只构建本机平台） | 产出 `npm/dist/@forebrain-harness/forebrain-<本机>/` |
| 归档 | `scripts/package-release-archive.sh <target> <version> <out>` | 产出归档 |

## 范围

**允许新建/修改**：上面"文件清单"中的全部文件；`.gitattributes`（在计划 006 建立的基础上追加）。

**不许碰**：`frontend/` 源码与 `vite.config.ts`（输出位置不变）；`pkg/gateway/embed.go`；计划 006/007 的工作流。

## 步骤

### 步骤 1：release-please 配置

`release-please-config.json`：
```json
{
  "$schema": "https://raw.githubusercontent.com/googleapis/release-please/main/schemas/config.json",
  "release-type": "simple",
  "include-component-in-tag": false,
  "include-v-in-tag": true,
  "bump-minor-pre-major": true,
  "changelog-sections": [
    { "type": "feat", "section": "Features" },
    { "type": "fix", "section": "Bug Fixes" },
    { "type": "perf", "section": "Performance" },
    { "type": "revert", "section": "Reverts" },
    { "type": "refactor", "section": "Refactoring", "hidden": true },
    { "type": "docs", "section": "Documentation", "hidden": true },
    { "type": "test", "section": "Tests", "hidden": true },
    { "type": "build", "section": "Build", "hidden": true },
    { "type": "ci", "section": "CI", "hidden": true },
    { "type": "chore", "section": "Chores", "hidden": true }
  ],
  "packages": {
    ".": {
      "package-name": "forebrain",
      "version-file": "VERSION",
      "changelog-path": "CHANGELOG.md",
      "extra-files": [
        { "type": "json", "path": "npm/package.json", "jsonpath": "$.version" },
        { "type": "json", "path": "npm/package.json", "jsonpath": "$.optionalDependencies['@forebrain-harness/forebrain-darwin-arm64']" },
        { "type": "json", "path": "npm/package.json", "jsonpath": "$.optionalDependencies['@forebrain-harness/forebrain-darwin-x64']" },
        { "type": "json", "path": "npm/package.json", "jsonpath": "$.optionalDependencies['@forebrain-harness/forebrain-linux-arm64']" },
        { "type": "json", "path": "npm/package.json", "jsonpath": "$.optionalDependencies['@forebrain-harness/forebrain-linux-x64']" },
        { "type": "json", "path": "npm/package.json", "jsonpath": "$.optionalDependencies['@forebrain-harness/forebrain-win32-x64']" }
      ]
    }
  }
}
```
`.release-please-manifest.json`：`{ ".": "0.0.0" }`

`bump-minor-pre-major: true` 的作用：在 0.x 阶段，破坏性变更只升次版本。这是 D1 推荐的"停在 0.x"策略的关键。
原因是 Go 模块到 v2 及以上必须改导入路径（加 `/v2`），否则 `go install …@latest` 解析不到新版本。

**验证**：`jq empty release-please-config.json .release-please-manifest.json` exit 0；
`jq -r '.packages["."]["extra-files"] | length' release-please-config.json` → `6`

### 步骤 2：Web UI 入库，并把 UI 构建从打包脚本里拆出去

- `.gitignore`：删掉 `!pkg/gateway/dist/`、`pkg/gateway/dist/*`、`!pkg/gateway/dist/.gitkeep` 三行和它们上面的注释，
  换成：
  ```
  # pkg/gateway/dist is committed: it is the built web UI that go:embed serves,
  # and `go install` builds from the tagged source, which must contain it. Only
  # the release workflow rewrites it (scripts/check-webui-dist.sh).
  !pkg/gateway/dist/
  ```
  必须保留全局的 `dist` 忽略规则（它挡住 `frontend/dist`、`npm/dist`），并用 `!pkg/gateway/dist/` 为这个目录单独放行。
  注意 gitignore 的语义：父目录被忽略时，子路径无法被重新包含。所以放行规则要写在 `dist` 之后。
  还要确认 `git check-ignore -v pkg/gateway/dist/index.html` 没有命中。
- `.gitattributes` 追加：`pkg/gateway/dist/** linguist-generated=true -diff`
- `Makefile`：
  - `release:` 改成依赖 `ui`：`release: ui`
  - `clean:` 删掉清空 dist 的那一行（dist 现在是源码的一部分），并更新文件头注释
  - `ui:` 保持不变（本地开发仍可重建）
- `npm/scripts/build-platform-packages.sh`：删掉第 40–43 行的前端构建，换成一行检查：
  `[ -f "$ROOT_DIR/pkg/gateway/dist/index.html" ] || { echo "pkg/gateway/dist has no web UI build; run make ui first" >&2; exit 1; }`，
  并更新脚本头注释（UI 由 `make ui` 或发版流程提供）。

**验证**：
- `git check-ignore -q pkg/gateway/dist/index.html; echo $?` → `1`（不被忽略）
- `git check-ignore -q frontend/dist/x; echo $?` → `0`（仍被忽略）
- `make release` 在本机成功（它会先 `make ui`），并产出本机平台包

### 步骤 3：dist 守护脚本

`scripts/check-webui-dist.sh <base-sha>`（`chmod +x`）：
- 当前分支以 `release-please--` 开头时（从 `GITHUB_HEAD_REF` 环境变量读取），重新构建 UI（`make ui`），然后
  `git status --porcelain -- pkg/gateway/dist` 必须为空，否则一句话报错
  `pkg/gateway/dist does not match the frontend source; the release workflow rebuilds it on this branch`，exit 1。
- 其他分支：`git diff --quiet "<base-sha>"...HEAD -- pkg/gateway/dist` 必须成立，否则一句话报错
  `pull requests must not change pkg/gateway/dist; restore it with: git checkout origin/main -- pkg/gateway/dist`，exit 1。

`ci.yml` 新增 job（只在 pull_request 触发时运行）：
```yaml
  webui-dist:
    name: Web UI build is release-managed
    if: github.event_name == 'pull_request'
    runs-on: ubuntu-latest
    timeout-minutes: 20
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }
      - uses: actions/setup-node@v4
        with: { node-version: "22" }
      - uses: pnpm/action-setup@<SHA> # v4.x（同计划 007 的钉版）
        with: { version: 10.24.0 }
      - env:
          GITHUB_HEAD_REF: ${{ github.head_ref }}
        run: scripts/check-webui-dist.sh "${{ github.event.pull_request.base.sha }}"
```

**验证**：在一个临时 git 仓库里模拟两种分支，确认两条报错路径各自触发一次。也可以直接审阅脚本逻辑，
然后用 `bash -n` 检查语法。actionlint 无输出。

### 步骤 4：Release 归档脚本

`scripts/package-release-archive.sh <target> <version> <out-dir>`（`chmod +x`）：
- 根据 target 算出 npm 包目录 `npm/dist/@forebrain-harness/<pkg>/vendor/<triple>/bin`（映射表见"现状"，放在脚本里）。
- 非 Windows 目标：`tar -C <bin-dir> -czf <out>/forebrain_v<version>_<goos>_<goarch>.tar.gz .`
- Windows 目标：`(cd <bin-dir> && 7z a -tzip <out>/forebrain_v<version>_windows_amd64.zip .)`（windows-latest 自带 7z）
- 打印产物路径。

**验证**：`make release` 之后运行 `scripts/package-release-archive.sh <本机 target> 0.0.0 $TMPDIR/arc`，
`tar -tzf` 能列出 `./forebrain` 和 `./dict/...`

### 步骤 5：`.github/workflows/release.yml`

结构如下（action 钉版遵守计划 007 的规则；`<SHA>` 用 `gh api repos/<o>/<r>/commits/<tag> --jq .sha` 取得）：

```yaml
name: Release
on:
  push:
    branches: [main]
  workflow_dispatch: {}
permissions:
  contents: read
concurrency:
  group: release
  cancel-in-progress: false

jobs:
  release-please:
    runs-on: ubuntu-latest
    outputs:
      release_created: ${{ steps.rp.outputs.release_created }}
      tag_name: ${{ steps.rp.outputs.tag_name }}
      version: ${{ steps.rp.outputs.version }}
      pr: ${{ steps.rp.outputs.pr }}
    steps:
      - id: app
        uses: actions/create-github-app-token@v2
        with:
          app-id: ${{ vars.FOREBRAIN_APP_ID }}
          private-key: ${{ secrets.FOREBRAIN_APP_PRIVATE_KEY }}
      - id: rp
        uses: googleapis/release-please-action@<SHA> # v4.x
        with:
          token: ${{ steps.app.outputs.token }}
          config-file: release-please-config.json
          manifest-file: .release-please-manifest.json

  # Every push to main can reshape the release PR; rebuild the web UI on its
  # branch so the commit that gets tagged carries the UI go:embed serves.
  webui-into-release-pr:
    needs: release-please
    if: needs.release-please.outputs.pr != '' && needs.release-please.outputs.release_created != 'true'
    runs-on: ubuntu-latest
    timeout-minutes: 20
    steps:
      - id: app
        uses: actions/create-github-app-token@v2
        with:
          app-id: ${{ vars.FOREBRAIN_APP_ID }}
          private-key: ${{ secrets.FOREBRAIN_APP_PRIVATE_KEY }}
      - uses: actions/checkout@v4
        with:
          ref: ${{ fromJSON(needs.release-please.outputs.pr).headBranchName }}
          token: ${{ steps.app.outputs.token }}
      - uses: actions/setup-node@v4
        with: { node-version: "22" }
      - uses: pnpm/action-setup@<SHA> # v4.x
        with: { version: 10.24.0 }
      - run: make ui
      - name: Commit the rebuilt UI when it changed
        env:
          GH_TOKEN: ${{ steps.app.outputs.token }}
          APP_SLUG: ${{ steps.app.outputs.app-slug }}
        run: |
          if [ -z "$(git status --porcelain -- pkg/gateway/dist)" ]; then exit 0; fi
          uid="$(gh api "/users/${APP_SLUG}%5Bbot%5D" --jq .id)"
          git config user.name "${APP_SLUG}[bot]"
          git config user.email "${uid}+${APP_SLUG}[bot]@users.noreply.github.com"
          git add -A pkg/gateway/dist
          git commit -m "chore(release): build the web UI"
          git push

  build:
    needs: release-please
    if: needs.release-please.outputs.release_created == 'true'
    strategy:
      fail-fast: true
      matrix:
        include:
          - { target: darwin/arm64,  os: macos-14,         native: true }
          - { target: darwin/amd64,  os: macos-14,         native: false, darwin_amd64_cc: 'clang -arch x86_64' }
          - { target: linux/amd64,   os: ubuntu-22.04,     native: true }
          - { target: linux/arm64,   os: ubuntu-22.04-arm, native: true }
          - { target: windows/amd64, os: windows-latest,   native: true }
    runs-on: ${{ matrix.os }}
    timeout-minutes: 45
    defaults: { run: { shell: bash } }
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ needs.release-please.outputs.tag_name }}
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod, cache-dependency-path: go.sum }
      - if: runner.os == 'Linux'
        run: sudo apt-get update && sudo apt-get install -y build-essential
      - run: test -f pkg/gateway/dist/index.html
      - run: scripts/check-release-versions.sh
      - name: Build the platform package
        env:
          FOREBRAIN_BUILD_TARGETS: ${{ matrix.target }}
          FOREBRAIN_VERSION: ${{ needs.release-please.outputs.version }}
          # Read by the script only when it cross-compiles darwin/amd64 on the
          # arm64 runner; empty everywhere else.
          FOREBRAIN_CC_DARWIN_AMD64: ${{ matrix.darwin_amd64_cc }}
        run: npm/scripts/build-platform-packages.sh
      - if: matrix.native
        name: Smoke test
        run: |
          bin="$(find npm/dist -type f \( -name forebrain -o -name forebrain.exe \) | head -1)"
          got="$("$bin" --version)"
          [ "$got" = "${{ needs.release-please.outputs.tag_name }}" ] || { echo "binary reports $got"; exit 1; }
      - run: scripts/package-release-archive.sh "${{ matrix.target }}" "${{ needs.release-please.outputs.version }}" out
      - uses: actions/upload-artifact@v4
        with:
          name: build-${{ strategy.job-index }}
          path: |
            out/*
            npm/dist/@forebrain-harness/*

  dict:
    needs: release-please
    if: needs.release-please.outputs.release_created == 'true'
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ needs.release-please.outputs.tag_name }}
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod, cache-dependency-path: go.sum }
      - run: scripts/build-dict-bundle.sh out
      - uses: actions/upload-artifact@v4
        with: { name: dict, path: out/forebrain-dict.tar.gz }

  github-assets:
    needs: [release-please, build, dict]
    runs-on: ubuntu-latest
    permissions:
      contents: write
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ needs.release-please.outputs.tag_name }}
      - uses: actions/download-artifact@v4
        with: { path: artifacts }
      - name: Upload binaries, dictionary bundle and checksums
        env:
          GH_TOKEN: ${{ github.token }}
          TAG: ${{ needs.release-please.outputs.tag_name }}
        run: |
          mkdir upload
          find artifacts -maxdepth 3 -type f \( -name 'forebrain_*' -o -name 'forebrain-dict.tar.gz' \) -exec cp {} upload/ \;
          (cd upload && sha256sum * > SHA256SUMS)
          gh release upload "$TAG" upload/* --clobber
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod, cache-dependency-path: go.sum }
      - name: The published dictionary bundle is what this version reads
        env:
          CGO_ENABLED: "1"
          FOREBRAIN_RELEASE_DICT_CHECK: ${{ needs.release-please.outputs.tag_name }}
        run: go test -tags fts5 ./pkg/memory -run TestReleasedDictionaryBundleMatchesDictionaryFiles -count=1

  npm:
    needs: [release-please, build]
    runs-on: ubuntu-latest
    environment: npm
    permissions:
      contents: read
      id-token: write
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ needs.release-please.outputs.tag_name }}
      - uses: actions/setup-node@v4
        with: { node-version: "22", registry-url: "https://registry.npmjs.org" }
      - uses: actions/download-artifact@v4
        with: { path: artifacts }
      # upload-artifact does not keep file modes; the binary must be executable
      # inside the package npm packs, or the launcher's spawn fails with EACCES.
      - run: chmod +x artifacts/build-*/npm/dist/@forebrain-harness/*/vendor/*/bin/forebrain*
      - name: Publish the platform packages, then the launcher
        env:
          NODE_AUTH_TOKEN: ${{ secrets.NPM_TOKEN }}
          VERSION: ${{ needs.release-please.outputs.version }}
        run: |
          publish() {
            local dir="$1" name
            name="$(jq -r .name "$dir/package.json")"
            if npm view "$name@$VERSION" version >/dev/null 2>&1; then
              echo "$name@$VERSION is already published"; return 0
            fi
            (cd "$dir" && npm publish --access public --provenance)
          }
          for dir in artifacts/build-*/npm/dist/@forebrain-harness/*/; do publish "$dir"; done
          publish npm

  go-install:
    needs: [release-please, github-assets]
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ needs.release-please.outputs.tag_name }}
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod }
      - run: sudo apt-get update && sudo apt-get install -y build-essential
      - name: Install the release the way a user does
        env:
          TAG: ${{ needs.release-please.outputs.tag_name }}
        run: |
          cd "$RUNNER_TEMP"
          env -u GOFLAGS GOWORK=off CGO_ENABLED=1 GOBIN="$RUNNER_TEMP/bin" \
            go install -tags fts5 "github.com/forebrain-harness/forebrain-harness/cmd/forebrain@$TAG"
          got="$("$RUNNER_TEMP/bin/forebrain" --version)"
          [ "$got" = "$TAG" ] || { echo "go-installed forebrain reports $got, want $TAG"; exit 1; }
```

几点要写进文件注释（英文）：
- 为什么 release-please 用 App token：让 release PR 和 UI 提交能触发 CI。
- 为什么 npm 先发平台包、后发 launcher：launcher 的 optionalDependencies 必须能解析到。
- 为什么 `npm view` 先查一次：失败后重跑 job 时要能跳过已发布的包（npm 不允许覆盖版本）。
- `go-install` job 同时会让 proxy.golang.org 和 sum.golang.org 收录这个版本。

**验证**：actionlint 无输出；`grep -c "needs.release-please.outputs.release_created == 'true'" .github/workflows/release.yml` ≥ 2

### 步骤 6：本机演练（不发布）

- `make release` → 本机平台包
- `scripts/package-release-archive.sh <本机 target> 0.0.0 $TMPDIR/arc`
- `scripts/build-dict-bundle.sh $TMPDIR/arc`
- `(cd npm/dist/@forebrain-harness/forebrain-<本机> && npm pack --dry-run)` 列出 `vendor/...` 下的二进制和 dict
- `(cd npm && npm pack --dry-run)`：若提示缺 README.md，记下来，由计划 012 补

**验证**：以上命令全部 exit 0；二进制 `--version` 输出 `v0.0.0`

## 测试计划

工作流只能在 GitHub 上真实运行。本计划的验证由三部分组成：
- 静态检查：actionlint、jq；
- 本机演练：步骤 6；
- 首次上线清单（计划 012）：推送后先观察 release PR 的生成、UI 提交和 CI；合并首个 release PR 后逐项核对
  Release 资产、npm 页面、`go install`。

## 真实仓库实测（关卡 G2 与 G3）

**G2（M8 推送、`setup-github.sh` 运行之后）**
1. `Release` 工作流在 main 上成功，并出现 release PR：
   `gh pr list --repo forebrain-harness/forebrain-harness --label "autorelease: pending"`，标题为 `chore(main): release 0.1.0`（首发版本号来自 M0 的
   `Release-As`，按 D1）。
2. release PR 的提交列表里有 `chore(release): build the web UI`（如果 main 上的 dist 已与源码一致，则没有这个提交，这时在
   日志里确认那一步输出 "exit 0"）。
3. release PR 上的全部必需检查通过，其中 `Web UI build is release-managed` 走的是 release 分支那条"重建并比对"路径（查看日志）。
4. 通知 owner 合并 release PR（G3）。

**G3（owner 合并之后）**
5. `Release` 工作流的全部 job 成功：`build`×5、`dict`、`github-assets`、`npm`、`go-install`。
6. Release 资产：`gh release view v0.1.0 --repo forebrain-harness/forebrain-harness --json assets --jq '.assets[].name'` 恰好包含 5 个
   `forebrain_v0.1.0_*` 归档、`forebrain-dict.tar.gz`、`SHA256SUMS`。下载全部资产并执行 `sha256sum -c SHA256SUMS` → 全部 OK。
   解开本机平台的归档，`./forebrain --version` → `v0.1.0`。
7. npm：`npm view @forebrain-harness/forebrain@0.1.0 version optionalDependencies`，以及五个平台包各自 `npm view <pkg>@0.1.0 version`
   → 全部存在。`npm view @forebrain-harness/forebrain@0.1.0 dist.attestations` 非空（provenance）。
   真实安装：`cd "$(mktemp -d)" && npm install @forebrain-harness/forebrain@0.1.0 && npx forebrain --version` → `v0.1.0`，
   且 `node_modules/@forebrain-harness/forebrain-<本机>/vendor/*/bin/dict/` 存在。
8. go install：`cd "$(mktemp -d)" && env -u GOFLAGS GOWORK=off CGO_ENABLED=1 GOBIN="$PWD/bin" go install -tags fts5 github.com/forebrain-harness/forebrain-harness/cmd/forebrain@latest && ./bin/forebrain --version`
   → `v0.1.0`（`@latest` 解析到首个正式版本）。
9. main 上的 `CHANGELOG.md`、`VERSION`（`0.1.0`）、`npm/package.json` 六处版本已由 release PR 更新，并且
   `scripts/check-release-versions.sh` 通过。
10. 记录全部链接与输出。

## 完成标准

- [ ] M7 已按规范提交
- [ ] 真实仓库实测 G2 与 G3 两部分全部通过并记录
- [ ] actionlint 对全部工作流无输出
- [ ] 步骤 2 的 `git check-ignore` 两条结果符合预期
- [ ] 步骤 6 本机演练全部成功
- [ ] `release.yml` 中没有任何 `${{ github.event.* }}` 被直接插进 `run:`（可以用 `grep -n 'run:.*github.event' .github/workflows/release.yml` 检查，应无输出）
- [ ] 改动全部在允许清单内

## STOP 条件

- release-please 当前版本的配置 schema 不支持 simple 策略下的 `version-file`：可以用
  `npx -y release-please@latest --help` 查看，或读它的 `schemas/config.json`。这时要汇报，由 owner 决定改用
  `version.txt`，还是改 `release-type`。
- 从步骤 2 起发现 vite 构建产物不确定：同一源码连续两次 `make ui` 得到不同的文件。这样步骤 3 的 release 分支检查
  会永远失败，需要 owner 决定。
- 本机 `make release` 失败，且原因不在本计划改动的范围内。

## 维护说明

- 首次发版：基线提交 M0 的正文末尾带有 `Release-As: 0.1.0` 脚注（D1，见 README 的"M0 基线提交"），release-please
  因此把首个 release PR 定为 0.1.0。之后不再需要。
- 进入 1.0 之前，要先想好 v2 的代价：Go 模块从 v2 起必须改路径。维持 `bump-minor-pre-major` 就能一直停在 0.x。
- npm 首次发布成功后，建议在 npmjs.com 为六个包配置 Trusted Publishing，然后删除 `NPM_TOKEN`，工作流改用 OIDC
  （D5 的第二阶段）。
- `ubuntu-22.04` 用于兼容较老的 glibc（2.35）。以后 GitHub 下线该镜像时，要改用容器构建或更新的镜像，并在 README
  里写明最低 glibc 版本。
