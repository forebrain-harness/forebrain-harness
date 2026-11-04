# 沙箱 seccomp 配置（forebrain-bubblewrap.json）

`forebrain-bubblewrap.json` 是让网关镜像里的 bubblewrap 沙箱在容器内可用的 seccomp
配置：以 Docker/containerd 的默认 seccomp 配置为底（moby/profiles 仓库的
`seccomp/default.json`，与 Docker 内置的是同一份；基底版本
`6fe7deb1b9fb7c0397a4593480d7d22b9ee8caef`，2026-09-17），只额外放行了创建用户
命名空间和在其内搭建沙箱根文件系统所需的系统调用。

不挂这个配置时网关照常运行，只是沙箱不可用：每条命令都是完全访问（等同 YOLO），
界面不会出现审批预设。沙箱是推荐项，不是前置条件。

## 为什么需要

默认 seccomp 配置把创建用户命名空间的一族系统调用（`clone`/`clone3`/`unshare`/
`mount`/`umount2`/`setns` 等）整个交给「容器具备 CAP_SYS_ADMIN 才放行」的规则组。
网关镜像以 uid 10001 无特权运行，所以默认情况下 bubblewrap 的第一条 `clone` 就被
拒（EPERM），探测报告 `bubblewrap_unusable`，产品退化为无沙箱模式。

## 放行清单（逐条经过「去掉即失败」验证）

验证方法：在 `forebrain:dev` 镜像内以 uid 10001 运行产品自己的探测命令
（`pkg/safety/process.go` 的 `probeSandbox`，即
`bwrap --unshare-user --unshare-pid --die-with-parent --ro-bind / / --dev /dev --proc /proc -- true`），
对每条放行单独移除后重跑。全部在 Docker Desktop 28.4.0（linux/arm64）、
bubblewrap 0.8.0、Docker `--security-opt systempaths=unconfined` 下实测：

| 放行项 | 作用 | 去掉后的失败 |
| --- | --- | --- |
| `clone`（仅当 flags 含 `CLONE_NEWUSER`） | bubblewrap 用它派生进入新用户命名空间的沙箱进程。比较位只看 `CLONE_NEWUSER`，其余 clone 标志不受约束（默认配置里那条无条件 clone 规则要求含 `CLONE_NEWCGROUP` 在内的全部七个命名空间标志位同时置位，bubblewrap 从不满足） | `bwrap: No permissions to create new namespace, likely because the kernel does not allow non-privileged user namespaces.` |
| `unshare`（仅当 flags 含 `CLONE_NEWUSER`） | bubblewrap 在派生出的进程里调用 `unshare(CLONE_NEWUSER)` 成为该命名空间内的 uid 0，后续挂载才被允许 | `bwrap: unshare user ns: Operation not permitted` |
| `mount` | 沙箱根的搭建：把挂载传播改为 slave（`Failed to make / slave`）、tmpfs 根、`--ro-bind`/`--bind`、挂载新的 `/proc`、`/dev` | `bwrap: Failed to make / slave: Operation not permitted`（第一个被挡的挂载） |
| `umount2` | `pivot_root` 之后卸载旧根 | `bwrap: unmount old root: Operation not permitted` |
| `pivot_root` | 切换到沙箱自己的根文件系统 | `bwrap: pivot_root: Operation not permitted` |

`s390/s390x` 上 clone 的 flags 寄存器是第二个参数，因此同规则按默认配置自己的写法
镜像了一份（`index: 1`）。镜像本身只发布 amd64/arm64，这两条只是保持配置可移植。

明确**不**放行的：

- `setns`：去掉后探测仍通过（bubblewrap 无特权模式不使用它），按最小集口径不放行。
- `clone3`：默认配置让它返回 ENOSYS，bubblewrap 0.8.0 使用 `clone`，探测通过；
  未来的 bubblewrap 若改用 `clone3`，本配置需要随之修订。
- 新挂载 API（`fsopen`/`fsconfig`/`fsmount`/`move_mount`/`open_tree`）：bubblewrap
  不使用。

在用户命名空间之外，`mount` 等仍需要 CAP_SYS_ADMIN，容器并不具备，因此无条件放行
这几个调用不会把宿主暴露给容器里的普通进程。

## 用法（Docker 必须同时给 systempaths=unconfined）

```
docker run --rm \
  --security-opt seccomp=path/to/forebrain-bubblewrap.json \
  --security-opt systempaths=unconfined \
  --security-opt apparmor=unconfined \
  forebrain:dev gateway start
```

`systempaths=unconfined` 不是可选的：Docker 默认把 `/proc` 的若干路径遮蔽
（`/proc/keys`、`/proc/timer_list` 等 tmpfs 覆盖）并把 `/proc/sys` 等挂为只读。
这些挂载在 `CLONE_NEWNS` 复制出的挂载命名空间里成为 locked mounts，内核的
`mount_too_revealing` 检查随之拒绝挂载一份「更完整」的新 proc 实例：

```
bwrap: Can't mount proc on /newroot/proc: Operation not permitted
```

只有 seccomp、没有 `systempaths=unconfined` 时失败如上（实测）。Kubernetes 的
containerd 运行时默认不做这类遮蔽，清单里不需要对应项。

## AppArmor（Ubuntu 宿主）

Ubuntu 的 docker-default AppArmor 配置会挡住 bubblewrap 在用户命名空间内的挂载，
Ubuntu 23.10+ 还默认开启 `kernel.apparmor_restrict_unprivileged_userns`。Docker 侧用
`--security-opt apparmor=unconfined` 绕开；Kubernetes 侧用注解
`container.apparmor.security.beta.kubernetes.io/<container>: unconfined`，或 1.30+
的 `securityContext.appArmorProfile.type: Unconfined`。

本目录没有附带「最小放行」的 AppArmor 配置：编写 LSM 配置无法在本仓库的验证环境
（Docker Desktop/macOS，无 AppArmor）里按上面的方法逐条验证，交付未经验证的安全
配置违背本文件的方法论。宿主侧更稳妥的替代是关掉对非特权用户命名空间的 AppArmor
限制（见下），只影响这一个开关。

## 宿主内核要求

沙箱需要内核允许非特权用户创建用户命名空间：

```bash
# Debian 系补丁提供的开关（Docker Desktop / 上游内核没有此项）
sysctl kernel.unprivileged_userns_clone        # 需要 =1；开启：sudo sysctl -w ... 并写入 /etc/sysctl.d/
# 每个用户命名空间数配额（上游内核），为 0 同样创建失败
sysctl user.max_user_namespaces                # 需要 >0
# Ubuntu 23.10+ / 24.04：AppArmor 对非特权用户命名空间的限制
sysctl kernel.apparmor_restrict_unprivileged_userns   # 需要 =0（或容器加 apparmor=unconfined）
```

一条命令的自检（容器内、无特权用户执行，成功无输出）：

```bash
bwrap --unshare-user --unshare-pid --die-with-parent \
  --ro-bind / / --dev /dev --proc /proc -- true
```

## 重新生成

基底取自 <https://github.com/moby/profiles>（`seccomp/default.json`），在其
`syscalls` 数组末尾追加上面清单中的四组规则（两组 clone、一组 unshare、一组
mount/umount2/pivot_root，含 `forebrain:` 前缀的 comment）即得本文件；然后用上面
的探测命令和「逐条去掉」的表格复验。
