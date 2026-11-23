# Plan: fetch-pdfium 去掉无条件的 api.github.com 探测——缓存命中零网络、403 有可行动指引

> **Executor instructions**: 逐步执行；每步先跑验证命令并确认预期结果再前进。
> 触发任何 STOP condition 立即停下报告，不要即兴发挥。改动只留工作区，不 commit。

## Status

- **Priority**: P1（owner 本机 `make release` 路径当前不可用）
- **Effort**: S
- **Risk**: LOW（单脚本、行为面窄、全部路径可 stub 验证）
- **Depends on**: none
- **Category**: bug（附带构建路径优化）
- **Planned at**: 工作区基线 commit `7c530851`（2026-11-10）；`scripts/fetch-pdfium.sh`
  干净无 WIP，最后触碰 commit `4e4ff2e8`。

## Why this matters

owner 在本机执行 `npm/scripts/build-platform-packages.sh`（即 `make release` 的实现），
在 PDFium 依赖已全部缓存的情况下仍然失败：

```
==> darwin/arm64 already at v1.27.0     ← fetch-libtokenizers 缓存命中（正常）
curl: (56) The requested URL returned error: 403   ×4
fetch-pdfium: could not read release chromium/8100  ← 整个打包构建死在这
```

本机的实际情况：`third_party/pdfium/{darwin_arm64,darwin_amd64,windows_amd64}` 全部
已在 chromium/8100（`.version` 与 `lib/` 均在），本可以零网络完成构建，但脚本在
任何缓存检查之前**无条件**调用 api.github.com 解析 asset id，撞上匿名配额 60 次/小时
耗尽 → 全量构建被一个根本不需要发生的网络请求杀死。

## 根因（file:line 证据链 + 现场证据）

`scripts/fetch-pdfium.sh`（唯一使用 api.github.com 的脚本，`grep -rn api.github.com scripts/ npm/scripts/ Makefile` 已确认）：

1. **L246-253：release-JSON 探测无条件执行**，先于 per-target 幂等检查
   （幂等检查在 `fetch_one` 内部 L130-136，循环在 L255 才开始）：

   ```bash
   release_json="$(mktemp)"
   trap 'rm -f "$release_json"' EXIT
   if [ -z "${FOREBRAIN_GITHUB_MIRROR:-}" ]; then
     curl -fsSL --retry 3 --retry-all-errors \
       ${gh_curl_opts[@]+"${gh_curl_opts[@]}"} \
       "$RELEASES_API/releases/tags/$RELEASE_TAG_ENCODED" -o "$release_json" \
       || die "could not read release $RELEASE_TAG"
   fi
   ```

   即使所有目标已缓存也烧 1 次 API 配额；配额耗尽时 403 硬失败。
2. **L249 `--retry 3 --retry-all-errors`**：403 是永久错误（配额窗口 1 小时），
   重试永远不可能成功——这正是用户看到的 4 行重复 403 噪音。
3. **L252 die 文案无指引**：`GITHUB_TOKEN`（L55-60 已支持）与
   `FOREBRAIN_GITHUB_MIRROR`（L164 已支持）两条既有逃生路一个字没提。
4. **L229-231 jq 前置检查同样无条件**：全缓存运行其实不需要 jq。

现场证据（2026-11-10 实测，本机出口 IP 115.236.69.202）：

- `curl -sS https://api.github.com/rate_limit` → `x-ratelimit-limit: 60,
  remaining: 0, used: 60`；release 端点实测返回 403，body 含
  `"API rate limit exceeded for 115.236.69.202"`。
- `curl https://github.com/.../releases/download/...` → **connect 超时**：
  本网络 github.com:443 直连不通，api.github.com 可达。⇒ API 路径必须保留为
  一等公民（正是这种网络下唯一可达的 GitHub 通道），镜像 env 是新鲜下载的逃生路。
- CI 不受影响：`.github/workflows/release.yml:110` 已给打包步骤注入
  `GITHUB_TOKEN: ${{ github.token }}`。缺陷只在本地匿名路径。
- 同类排查：`fetch-libtokenizers.sh`（直接 github.com 下载、逐目标幂等、无 API
  探测）、`fetch-pdfium-fonts.sh`（npmjs，逐文件 sha256 幂等）、
  `install-dictionary.sh`/`install-assets.sh`（无网络）——均无此缺陷，不碰。

## 修复设计

四个改动，全部在 `scripts/fetch-pdfium.sh`：

**A. 缓存预检前置（根因修复）**：新增 `target_cached()` 谓词（唯一一份缓存判定，
原 `fetch_one` L130-136 的分支删除，谓词不再有两处）；主循环先做纯文件系统的
预检——已缓存目标就地收尾（windows 补 alias + log "already at"），未缓存目标进
`needs[]`。**`needs` 为空时全程零网络调用**；release-JSON 探测与 jq 检查只在
`needs` 非空且未设 mirror 时发生。

**B. 探测失败要快、要指路**：JSON 探测去掉 `--retry-all-errors`（保留
`--retry 2`，curl 默认瞬态集含超时/5xx/429，**不含** 403——403 立即失败，
不再 4 连击）；用 `-w '%{http_code}'` 捕获状态码，403 且 body 含
"API rate limit exceeded" 时，die 文案指明两条既有逃生路
（`GITHUB_TOKEN`/`GH_TOKEN` 5000/h；`FOREBRAIN_GITHUB_MIRROR` 完全绕开 API）。

**C. jq 检查移到懒位置**：只有真的要走 API 解析 asset id 时才要求 jq。

**D. 头部注释与 L145-162 "Two ways" 注释块同步更新**（探测变懒、缓存命中零网络、
403 指引），保持文档与行为一致。

明确**不做**（权衡已在根因证据里）：
- 不做"API 失败自动回退 github.com 直连 URL"——本网络 github.com 不通，
  每次新鲜下载白等 15s connect-timeout；mirror env 已是显式逃生路。
- 不删 API 下载路径——它是 github.com 被墙网络上唯一可达通道（本机现状）。
- 不改 `npm/scripts/build-platform-packages.sh` 本体（其行为正确，失败完全来自
  被调脚本）；不改 `fetch-libtokenizers.sh`（无此缺陷）。

### 行为变化矩阵（★=刻意修订）

| 场景 | 旧行为 | 新行为 |
|---|---|---|
| 目标全缓存（owner 本机当前） | 仍打 1 次 API；配额尽→403×4→整个构建死 | ★零网络，逐目标 "already at"，构建继续 |
| 需下载+匿名配额尽+无 mirror | 403×4 + "could not read release chromium/8100"（无指引） | ★立即失败一次，文案给出 GITHUB_TOKEN 与 FOREBRAIN_GITHUB_MIRROR 两条路 |
| 需下载+有 GITHUB_TOKEN | API 正常 | 不变 |
| 需下载+已设 FOREBRAIN_GITHUB_MIRROR | 不打 API，确定性 URL | 不变 |
| 无 jq+全缓存 | 开局即死 "jq is required" | ★正常完成（无下载不需要 jq） |
| 无 jq+需下载（无 mirror） | 死 | 不变（仍要求 jq） |
| CI release 矩阵 | 带 token，正常 | 不变（另省缓存命中时的 API 调用） |
| sha256 校验、幂等语义、mirror URL 形状 | — | 不变 |

## Scope

**In scope**：`scripts/fetch-pdfium.sh`（唯一源码改动）。
**Out of scope（明确不碰）**：`npm/scripts/build-platform-packages.sh`、
`scripts/fetch-libtokenizers.sh`、`scripts/fetch-pdfium-fonts.sh`、
`scripts/install-dictionary.sh`、`scripts/install-assets.sh`、
`.github/workflows/release.yml`、Go 源码与任何测试。

## Steps

Step 0：按仓库惯例把本计划镜像到 `docs/plan/FETCH_PDFIUM_LAZY_API_PROBE_PLAN.md`
（该目录已 gitignore，不入库），后续状态回填写在那里。
验证：`ls docs/plan/FETCH_PDFIUM_LAZY_API_PROBE_PLAN.md`。

Step 1（复现先行）：在改码前跑原脚本锁定失败现状——
`scripts/fetch-pdfium.sh darwin/arm64` → 预期死于 403/"could not read release
chromium/8100"（与本机配额状态一致；只发 GET、只写 mktemp 临时文件，无副作用）。

Step 2：实施 A-D。关键代码形状（executor 照此落，细节以现场为准）：

```bash
# target_cached <goos>/<goarch> — 该目标的 pinned release 已解包就绪（无需网络）。
target_cached() {
  local goos="${1%%/*}" goarch="${1##*/}"
  local dir="$DEST_ROOT/${goos}_${goarch}"
  [ -f "$dir/.version" ] && [ "$(cat "$dir/.version")" = "$RELEASE_TAG" ] && [ -d "$dir/lib" ]
}

# 主循环尾部：先纯文件系统预检；needs 为空则全程零网络。
needs=()
for target in "${targets[@]}"; do
  case "$target" in */*) ;; *) die "target \"$target\" must be <goos>/<goarch>" ;; esac
  if target_cached "$target"; then
    if [ "${target%%/*}" = windows ]; then
      ensure_windows_alias "$DEST_ROOT/${target%%/*}_${target##*/}"
    fi
    log "$target already at $RELEASE_TAG"
  else
    needs+=("$target")
  fi
done

if [ "${#needs[@]}" -gt 0 ] && [ -z "${FOREBRAIN_GITHUB_MIRROR:-}" ]; then
  command -v jq >/dev/null 2>&1 || die "jq is required to resolve asset ids"
  fetch_release_json   # 单发、无 --retry-all-errors、-w 捕获状态码；
                       # 403+rate-limit body → die 文案给出 GITHUB_TOKEN /
                       # FOREBRAIN_GITHUB_MIRROR 两条既有逃生路
fi
for target in "${needs[@]}"; do fetch_one "$target" "$release_json"; done
```

`fetch_one` 删除其内部缓存分支（原 L130-136）；`release_json="$(mktemp)"` 与
EXIT trap 保留在顶层。验证：`bash -n scripts/fetch-pdfium.sh` 无输出。

Step 3（零网络证明）：stub 掉 curl 证明缓存路径不碰网络——
`mkdir /tmp/nocurl && printf '#!/bin/sh\necho "MUST NOT CALL CURL" >&2\nexit 99\n' > /tmp/nocurl/curl && chmod +x /tmp/nocurl/curl`
然后 `PATH="/tmp/nocurl:$PATH" scripts/fetch-pdfium.sh darwin/arm64` →
预期：打印 `==> darwin/arm64 already at chromium/8100`，exit 0，stderr 无
"MUST NOT CALL CURL"。

Step 4（403 指引文案证明）：把脚本拷到 `$TMP/scripts/`（ROOT_DIR 随之指向 $TMP，
third_party 为空 → 目标必然未缓存），用 stub curl：遇到
`releases/tags/...` 参数时向 `-o` 文件写入
`{"message":"API rate limit exceeded for 1.2.3.4"}`、向 stdout 打 `403`、exit 0。
预期：exit 1，stderr 含 `GITHUB_TOKEN` 与 `FOREBRAIN_GITHUB_MIRROR` 字样。

Step 5（端到端，真实现场）：确认 `pkg/gateway/dist/index.html` 存在（否则先
`make ui`），然后 `FOREBRAIN_SKIP_UI_BUILD=1 npm/scripts/build-platform-packages.sh`
→ 预期完整走完 host 三元组打包：`npm/dist/@forebrain-harness/forebrain-darwin-arm64/`
下有 `vendor/aarch64-apple-darwin/bin/forebrain`、`libpdfium.dylib`、`dict/`、
`models.json`、fonts 资产；且 fetch-pdfium 环节打印 "already at chromium/8100"、
零 403。（UI 若必须重建则去掉 SKIP 变量跑，等价。）

Step 6：回填 `docs/plan/FETCH_PDFIUM_LAZY_API_PROBE_PLAN.md` 状态行与验收记录；
清理 /tmp stub。

## Done criteria（机器可查）

1. `bash -n scripts/fetch-pdfium.sh` 通过。
2. Step 3 的 stub 运行 exit 0 且从未调用 curl。
3. Step 4 的 stub 运行 exit 1 且 stderr 同时含 `GITHUB_TOKEN`、`FOREBRAIN_GITHUB_MIRROR`。
4. 本机（配额仍耗尽）`scripts/fetch-pdfium.sh darwin/arm64` 直接运行成功——修复前同命令失败（Step 1 已锁定）。
5. Step 5 打包脚本端到端成功。
6. `git status` 中源码改动仅 `scripts/fetch-pdfium.sh`（plan 文件在 gitignore 目录）；无 commit。

## STOP conditions

- Step 1 原脚本**没有**以 403 失败（如配额已重置返回 200）→ 记录现状后继续，但
  Step 4 的 stub 验证必须覆盖 403 分支。
- 实施时 `scripts/fetch-pdfium.sh` 现场内容与本计划"根因"所引摘录不一致（内容漂移，非行号漂移）。
- Step 3 stub 仍触发 curl、或 Step 4 文案断言失败且两轮聚焦修复无效。
- Step 5 失败原因与 fetch-pdfium 无关（前端构建/Go 编译/工具链）→ 停下报告，不扩 scope。

## Maintenance notes

- 网络事实：本机 github.com:443 直连不通、api.github.com 可达。新鲜下载（缓存
  缺失时）在无 token 无 mirror 的裸机上依然不可行——这是环境约束不是脚本缺陷；
  403 文案已把两条路写明（token 或 `FOREBRAIN_GITHUB_MIRROR=https://ghfast.top/`，
  示例沿用 `fetch-libtokenizers.sh:32` 的同一镜像，勿两处各写一个）。
- 未来若 RELEASE_TAG 升级：换 tag 后所有目标进 needs，走 API/mirror 下载；
  sha256 表照旧是唯一真源，本计划不改变 pin 语义。
- `fetch-libtokenizers.sh` 在本网络做**新鲜**下载同样会因 github.com 不通而超时，
  但它无无条件网络调用、缓存命中零网络，与本次缺陷不同类——遗留为环境事项，
  如 owner 需要“本地新鲜下载也不怕墙”可作为后续独立计划（mirror env 已可用）。

## 验收记录（2026-11-10 执行）

- **Step 1 现状记录**：配额窗口已重置（`x-ratelimit-used: 8, remaining: 52`，计划时 0/60），
  原脚本探针 200 通过、缓存放行 exit 0——未复现 403，属计划预设 STOP 分支，按要求记录后继续；
  403 分支由 Step 4 stub 覆盖（计划要求）。该运行同时实证根因仍在：目标已缓存仍执行了 1 次 API 探测。
- **Step 2**：A-D 全部落地；`bash -n` 通过（含 /bin/bash 3.2 显式检查）。
- **Step 3**：stub curl 下 `darwin/arm64` 与三目标全缓存均 exit 0、零 curl 调用。
- **Step 4**：stub 403+rate-limit body → exit 1，stderr 同时含 `GITHUB_TOKEN`、`FOREBRAIN_GITHUB_MIRROR`，
  探测仅 1 次调用。附加矩阵行实证：jq 完全不在 PATH + 全缓存 → exit 0 正常完成（stub curl 零调用）。
- **Step 5**：`FOREBRAIN_SKIP_UI_BUILD=1 npm/scripts/build-platform-packages.sh` exit 0（两次），
  完整日志含 `==> darwin/arm64 already at chromium/8100`、零 403/零 rate-limit；产物齐全于
  `npm/dist/@forebrain-harness/forebrain-darwin-arm64/vendor/aarch64-apple-darwin/bin/`：
  `forebrain`(111MB)、`libpdfium.dylib`、`dict/`、`models.json`、`fonts/`、`tokenizers`。
- **范围**：源码改动仅 `scripts/fetch-pdfium.sh`；`git status` 中 `pkg/tui/chat_session_test.go`、
  `pkg/tui/overlays.go` 为并行会话既有 WIP，未触碰。无 commit。
- **实施偏离（非承重细节）**：空 `needs` 数组迭代改用守卫展开
  `for target in ${needs[@]+"${needs[@]}"}`（计划草图为裸 `"${needs[@]}"`）——本机
  `#!/usr/bin/env bash` 解析到 bash 3.2.57，`set -u` 下空数组裸展开实测报 unbound variable；
  守卫写法与文件既有 `${gh_curl_opts[@]+…}` 惯例一致。
- **已知角落**：同一目标在一次调用中重复传入时，现在会下载两次（旧代码第二次命中 fetch_one
  内缓存检查）；最终状态不变（sha256 仍强制校验）。无调用方这样传参（CI 五目标互异、
  build-platform-packages.sh 每次单目标），未加去重代码。

**状态：DONE**
