# Plan 068：`fetch-libtokenizers.sh` 在 macOS 自带 bash 3.2 下能交叉取 Windows 库；Windows 上的编译器提示真正生效

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **Drift check（先跑）**：`git diff --stat fa4af8a -- scripts/fetch-libtokenizers.sh` 无输出；
> `grep -n 'if \[ "$(uname -s)" = "MINGW"\* \]' scripts/fetch-libtokenizers.sh` 有 1 行。不符 → STOP。

## Status

- **Priority**: P3
- **Effort**: S
- **Risk**: LOW
- **Depends on**: 无（与 065–067、069 不冲突，可并行）
- **Category**: bug / build
- **Planned at**: commit `fa4af8a`，2026-10-16

## Why this matters

`scripts/fetch-libtokenizers.sh` 的 `fetch_build_windows` 里有两个已实测的缺陷：
1. `if [ "$(uname -s)" = "MINGW"* ] || [ "$(uname -s)" = "MSYS"* ]`——`[` 不做通配匹配，这个条件**永远为假**，在 Windows（Git Bash）上给 cargo 指定 mingw 编译器的提示从不生效。
2. 非 Windows 主机上 `extra_env=()` 为空，`set -euo pipefail` 下 `env "${extra_env[@]}" cargo ...` 在 **bash 3.2**（macOS 自带 `/bin/bash`）里报 `extra_env[@]: unbound variable` 并退出——从 macOS 交叉取 Windows 静态库（`npm/scripts/build-platform-packages.sh` 以 `FOREBRAIN_BUILD_TARGETS=windows/amd64` 运行时）必然失败。

实测：`/bin/bash -c 'set -euo pipefail; extra_env=(); env "${extra_env[@]}" true'` → `extra_env[@]: unbound variable`；`[ "MINGW64_NT-10.0-26100" = "MINGW"* ]` → 不匹配。

## Current state

`scripts/fetch-libtokenizers.sh` 的 `fetch_build_windows`：

```bash
  # On Windows (Git Bash) cargo may not find a C/C++ compiler for the GNU
  # target unless it is named; mingw's gcc/g++ satisfy it.
  local extra_env=()
  if [ "$(uname -s)" = "MINGW"* ] || [ "$(uname -s)" = "MSYS"* ]; then
    extra_env=(CC_x86_64_pc_windows_gnu=gcc CXX_x86_64_pc_windows_gnu=g++)
  fi
  ( cd "$tmp/src" && env "${extra_env[@]}" cargo build --release --locked -p tokenizers-ffi --target "$triple" ) \
    || die "cargo build for $triple failed"
```

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| 语法 | `/bin/bash -n scripts/fetch-libtokenizers.sh` | 无输出 |
| shellcheck（若已安装） | `shellcheck scripts/fetch-libtokenizers.sh` | 不新增告警 |
| 回归 | `scripts/fetch-libtokenizers.sh` | 宿主目标 "already at v1.27.0" 或下载成功 |

## Scope

**In scope**：`scripts/fetch-libtokenizers.sh`。**Out of scope**：其他脚本、CI 工作流。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：修两处

1. 条件改为 `case "$(uname -s)" in MINGW*|MSYS*|CYGWIN*) extra_env=(...) ;; esac`。
2. 展开改为 bash 3.2 安全的写法：`env ${extra_env[@]+"${extra_env[@]}"} cargo build ...`（`${arr[@]+"${arr[@]}"}` 在空数组且 `set -u` 时展开为空）。
3. 在该段上方注释里写明这两个写法是为 bash 3.2（macOS 自带）与 Git Bash 而选。

**Verify**：

```bash
/bin/bash -c 'set -euo pipefail; extra_env=(); env ${extra_env[@]+"${extra_env[@]}"} true && echo ok'   # → ok
/bin/bash -c 'case "MINGW64_NT-10.0" in MINGW*|MSYS*|CYGWIN*) echo matched ;; esac'                   # → matched
/bin/bash -n scripts/fetch-libtokenizers.sh                                                            # → 无输出
```

### Step 2：回归

`scripts/fetch-libtokenizers.sh`（宿主目标，幂等）成功。若本机有 `cargo`，可额外跑 `/bin/bash scripts/fetch-libtokenizers.sh windows/amd64`，预期越过 `env` 那一行进入 `cargo build`（编不编得过取决于本机是否装了 mingw 工具链，不作为完成条件）。

## Test plan

本脚本无测试框架；以 Step 1 的三条命令为回归。

## Done criteria

- [ ] Step 1 三条命令输出符合预期
- [ ] `grep -n '"MINGW"\*' scripts/fetch-libtokenizers.sh` 无输出
- [ ] `git status --short` 只有 `scripts/fetch-libtokenizers.sh`

## STOP conditions

1. Drift check 不符。

## Maintenance notes

- 脚本 shebang 是 `#!/usr/bin/env bash`，在 macOS 上可能是 3.2；新增数组展开一律用 `${arr[@]+"${arr[@]}"}`。
