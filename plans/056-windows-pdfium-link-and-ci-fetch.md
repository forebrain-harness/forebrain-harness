# 056：修通 Windows PDFium 链接（合并阻断项）+ CI 拉取加 token/重试/缓存

> **执行者须知（零上下文）**：本计划自包含。逐步执行，每步先跑"验证"确认预期再进入下一步。
> 命中 STOP condition 就停下报告。**不建 worktree、不 commit**。
>
> **Drift check（先做）**：本计划写于 `git rev-parse --short HEAD` = `fa4af8a`（2026-10-16）。
> 执行前核对 `scripts/fetch-pdfium.sh`、`.github/workflows/{ci,security,lsp-integration,release}.yml`
> 与下文 "Current state" 摘录一致；对不上按 STOP 处理。

## Status

- **Priority**: P1（`windows build test (CGO)` 是 required check，现状大概率红 → 分支无法合并）
- **Effort**: S/M（脚本小改 + 容器红/绿验证 + CI 观察）
- **Risk**: LOW（只动 windows 分支与 curl 参数；darwin/linux 路径零改动）
- **Depends on**: none
- **Category**: CI / Build（审计发现 #3/#12）
- **Planned at**: commit `fa4af8a`，2026-10-16

## Why this matters

1. **Windows 链接必失败**：pinned 归档（sha256 `51621bcc…` 已实测下载核对）解包只有
   `lib/pdfium.dll.lib` + `bin/pdfium.dll`——MSVC 命名。而 `fetch-pdfium.sh` windows 分支写出的
   `pdfium.pc` 是 `Libs: -L${libdir} -lpdfium`。GNU ld（MinGW）对 `-lpdfium` 的搜索序列是
   `libpdfium.dll.a pdfium.dll.a libpdfium.a pdfium.lib libpdfium.lib cygpdfium.dll libpdfium.dll
   pdfium.dll`——**不含 `pdfium.dll.lib`**（binutils 2.44 手册明列，多年未变）。CI 的 windows job
   （ci.yml:147-160，required）与 release windows 构建都用 MinGW gcc 做 cgo → `cannot find
   -lpdfium`。plans/README.md 已如实标注"待 CI 验证的真问题"，但接线本身没解决它。
2. **CI 匿名拉取脆弱**：资产经 `api.github.com/releases/assets/<id>` 匿名下载（60 req/h/IP），
   release 元数据请求（fetch-pdfium.sh:176）无 `--retry`；一个 PR 并发跑 ci+security+lsp×3 共
   6+ 次/运行。审计期间已现场复现 403 rate limit。另外 `third_party/pdfium` 完全无缓存。

## Current state（摘自本次亲自阅读）

### scripts/fetch-pdfium.sh（HEAD fa4af8a）

```bash
# fetch_one 的 windows 分支（:148-153）
    windows)
      # An MSVC/MinGW link uses the import library in lib/ and loads
      # bin/pdfium.dll at run time.
      write_pc "$dir" lib ""
      ;;
```

`write_pc`（:91-106）写 `Libs: -L${libdir} -lpdfium $extra`。解包校验只查
`[ -d "$dir/include" ]`、`[ -d "$dir/lib" ]`（:130-131）。幂等短路在 :112-116
（`.version` 匹配且 `lib/` 存在即 return，**不做任何别名补齐**）。

两处 curl：资产下载 :121-124（有 `--retry 3 --retry-all-errors`，无鉴权头）；release 元数据
:176-178（无 retry、无鉴权头）。`RELEASES_API` 指向 api.github.com。

### CI 接线（HEAD fa4af8a，全部已跑 fetch，问题只在链接与限流）

- `ci.yml:46-50`（linux 主 job）、`:108-115`（windows-tests）、`:150-155`（windows build）、
  `:182-186`（macos，两个 darwin 目标）
- `security.yml:26-27`（govulncheck 前）、`lsp-integration.yml:43-44`（三个 job）
- windows job 已做 `cygpath -w` 转换 `PKG_CONFIG_PATH`（ci.yml:112-113/152-154）。

### 本机环境（写计划时实测）

- 无 mingw 交叉链（`x86_64-w64-mingw32-gcc` 不存在）；`pkg-config`/`jq`/`curl` 有；
  `third_party/pdfium/` 已有 darwin_amd64、darwin_arm64。
- Docker client 在，**daemon 当时未运行**（`docker info` 连不上 sock）。
  owner 已确认走 Docker 验证路线。

## 修复设计

### Step 0：准备验证环境

`open -a Docker` 后轮询 `docker info` 直到就绪（最长 ~2 分钟）。若 Docker Desktop 不可用，
退路：`brew install mingw-w64`（同一套 GNU ld 交叉链，验证等价性相同，仅缺 Windows 运行期）。
两者都不可用 → STOP-1。

### Step 1（红）：容器内复现链接失败

```bash
docker run --rm -v "$PWD":/repo -w /repo golang:1.26-bookworm bash -exc '
  apt-get update -qq && apt-get install -y -qq gcc-mingw-w64-x86-64 pkg-config jq >/dev/null
  scripts/fetch-pdfium.sh windows/amd64
  cd pkg/pdf && cat > /tmp/main.go <<EOF
package main
import _ "github.com/forebrain-harness/forebrain-harness/pkg/pdf"
func main() {}
EOF
  CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc \
    PKG_CONFIG_PATH=/repo/third_party/pdfium/windows_amd64 \
    go build -o /tmp/probe.exe /tmp/main.go'
```

（若 hub 无 `golang:1.26-bookworm` tag，用可用的最新 1.26.x bookworm tag。）

**预期**：`cannot find -lpdfium`（或等价 link error）。这就是 CI windows job 现在的命运。
若容器里**链接成功**——说明审计前提有误，STOP-2 报告，不要改脚本。

### Step 2（绿）：fetch 脚本 windows 分支补 GNU 命名别名

`scripts/fetch-pdfium.sh` windows 分支改为：

```bash
    windows)
      # An MSVC/MinGW link uses the import library in lib/ and loads
      # bin/pdfium.dll at run time. The archive ships it under the MSVC name
      # pdfium.dll.lib, which GNU ld's -l search sequence never tries; pdfium.lib
      # (which it does try) is the same short-import archive, so alias it.
      if [ -f "$dir/lib/pdfium.dll.lib" ] && [ ! -f "$dir/lib/pdfium.lib" ]; then
        cp "$dir/lib/pdfium.dll.lib" "$dir/lib/pdfium.lib"
      fi
      [ -f "$dir/lib/pdfium.lib" ] || die "windows archive has no import library (lib/pdfium.dll.lib)"
      # pkgconfiglite is a native Windows pkg-config: hand it a Windows prefix,
      # not the POSIX path Git Bash computes (PKG_CONFIG_PATH was already
      # cygpath-converted by CI; the .pc contents must match).
      if command -v cygpath >/dev/null 2>&1; then
        write_pc "$(cygpath -w "$dir")" lib ""
      else
        write_pc "$dir" lib ""
      fi
      ;;
```

**关键：幂等路径也要补别名**——已缓存的目标（`.version` 匹配）短路 return（:112-116）不会走到
上面的 cp。在短路 return **之前**加同一段补齐（或把补齐提为函数 `ensure_windows_alias "$dir"`，
两处调用）。否则本机/CI 已缓存的 windows_amd64 永远修不好。

`write_pc` 里 `prefix=` 含 Windows 反斜杠路径时 heredoc 不受影响（无变量展开冲突，`\$` 转义仅针对
`${prefix}` 引用自身）。pkg-config 的 `.pc` 变量引用 `${prefix}` 照常工作。

**验证**：重复 Step 1 的容器命令 → 必须产出 `/tmp/probe.exe`，且
`x86_64-w64-mingw32-objdump -p /tmp/probe.exe | grep -i pdfium.dll` 能看到 DLL Name（import 表
真实引用）。再删 `third_party/pdfium/windows_amd64/.version` 重跑 fetch（重建路径）与不删重跑
（幂等路径）各一次，别名在两条路径下都存在。

### Step 3（#12）：拉取加鉴权与重试

`scripts/fetch-pdfium.sh`：

1. 顶部加：

```bash
# api.github.com is rate-limited hard for anonymous callers (60/h per IP); CI
# runs several fetches per push. Use the workflow token when the environment
# provides one; local fetches stay anonymous.
gh_curl_opts=()
if [ -n "${GITHUB_TOKEN:-}${GH_TOKEN:-}" ]; then
  tok="${GITHUB_TOKEN:-}"
  [ -n "$tok" ] || tok="$GH_TOKEN"
  gh_curl_opts=(-H "Authorization: Bearer $tok")
fi
```

2. 资产下载 curl（:121-124）与 release 元数据 curl（:176-178）都追加 `"${gh_curl_opts[@]}"`
   （空数组在 `set -u` 下对带引号的展开安全：bash ≥4.4；CI 的 bash 与 macOS 自带 bash3.2 都要过——
   **注意**：macOS `/bin/bash` 3.2 下空数组展开会炸 `set -u`，改用 `${gh_curl_opts[@]+"${gh_curl_opts[@]}"}`）。
3. 元数据 curl 追加 `--retry 3 --retry-all-errors`（与资产下载对齐）。

CI workflows：每个执行 fetch 的 step 加

```yaml
        env:
          GITHUB_TOKEN: ${{ github.token }}
```

（各 workflow 顶部 permissions 需含 `contents: read`——执行时逐个核对，security.yml 若无
permissions 块则补最小块。）**不引入任何 secret**：`github.token` 是 job 自带的。

4. `ci.yml` 主 linux job（:46-50 附近）加 `third_party/pdfium` 缓存：

```yaml
      - uses: actions/cache@v4
        with:
          path: third_party/pdfium
          key: pdfium-${{ runner.os }}-${{ hashFiles('scripts/fetch-pdfium.sh') }}
```

   `.version` 守卫保证缓存命中时脚本秒过、tag 升级时自动失效（hashFiles 键随脚本变化）。
   其余 job 视红绿情况再补，不强制一次全铺。

**验证**：`bash -n scripts/fetch-pdfium.sh`；`GITHUB_TOKEN= bash -euo pipefail -c 'set -x;
$(grep -n "curl" scripts/fetch-pdfium.sh | head -1)'` 之类只做语法级检查，真实行为由 CI 证明。
本机重跑 `scripts/fetch-pdfium.sh darwin/arm64` 一次确认无回归（darwin 路径零改动）。

### Step 4（权威确认）：CI windows job

推送分支（由 owner 决定时机）后观察：

1. `windows build test (CGO)` 与 `windows-tests` 全绿。
2. 若仍红且错误为 `.pc` 路径/file-not-found 类：对照本计划 Step 2 的 cygpath 处理核查日志里的
   `PKG_CONFIG_PATH` 与 pkg-config 输出（`pkg-config --debug` 可在 job 里临时加），修正后重跑。
   连续两轮同因失败 → STOP-3。
3. `security.yml`/`lsp-integration.yml` 的 fetch step 不再出现 403。

## Test plan

- 容器红/绿 + objdump import 表（Step 1/2）——本计划的核心证据。
- 幂等路径别名补齐的重跑验证（Step 2）。
- `bash -n` + 本机 darwin 重跑（Step 3）。
- CI windows job 绿灯（Step 4，权威）。
- release 五平台构建在 release 管线自然覆盖（不在本计划内主动触发）。

## STOP conditions

1. Docker 与 mingw-w64 都不可用 → 报告，等待 owner 提供环境。
2. 容器内改动前链接**成功**（审计前提不成立）→ 报告实测输出，计划作废。
3. 脚本修正后 CI windows job 连续两轮同因失败（非限流、非基础设施抖动）→ 报告日志。
4. `golang:1.26-bookworm` 镜像不存在且无等价 tag → 改用 `golang:1.26` 或最新可用 tag；全都
   没有时报告。

## Out of scope（不要做）

- linux/arm64、linux/amd64、darwin 交叉目标的任何改动（接线已验证/本机已验证）。
- Windows **运行期**行为（DLL 放置与加载、字体目录解析）——属"待 CI/发布管线验证"的 owner 已知
  遗留，本计划只修到"链接产出合法 PE + import 表引用 pdfium.dll"为止。
- 换 MSVC 工具链、静态链接、换 PDFium 发行版。
- 给全部 CI job 铺缓存（先主 job，红绿说了算）。

## 全量验证闸门

```bash
bash -n scripts/fetch-pdfium.sh
scripts/fetch-pdfium.sh darwin/arm64        # 本机无回归
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/pdf -count=1
CGO_ENABLED=1 go vet -tags fts5 ./...
gofmt -l cmd pkg third_party                # 为空
```
