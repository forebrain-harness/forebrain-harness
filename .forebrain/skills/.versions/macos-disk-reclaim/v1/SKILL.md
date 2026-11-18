---
name: macos-disk-reclaim
description: 本机磁盘告急（数据卷可用 < 10GiB 或 95% 以上）或 owner 要求清理 Docker 占用、缓存与日志时用：只读盘点 → 清 Docker 临时测试库 → VM 内 fstrim 让 Docker.raw 真正缩小 → 清可再生缓存与历史日志 → df 前后对照。
---

# macOS 磁盘回收（Docker + 缓存 + 日志）

本机（macOS + Docker Desktop + Go/Node 工具链）磁盘被占满时的可复现流程。
核心经验：**Docker 那 20G 里绝大部分不是镜像，而是 `scripts/test-db.sh` 留下的
匿名卷；而且删完之后宿主 `Docker.raw` 不会自己缩小，必须在 VM 内 fstrim。**

## 前置条件

- macOS，Docker Desktop 已安装（本仓 `scripts/test-db.sh` 依赖它）。
- 在仓库根执行（脚本用 `git rev-parse --show-toplevel` 定位）。
- 需要几十秒到几分钟的扫描时间：`du` 遍历 `~/Library`、`~/workspace` 时要给
  命令 120–300s 超时，默认 10s 必然超时（本会话第一个失败就是这个）。

## 第 1 步：只读盘点

```bash
./scripts/disk-inventory.sh        # 本 skill 自带，不删任何东西
```

关键判据：

- `df -h /System/Volumes/Data` 才是真正的数据卷；`df -h /` 是只读系统快照，
  看它没有意义。
- `Docker.raw` 是 **sparse file**：`ls -lh` 显示表观 228G，`du -h` 显示真实
  占用。永远用 `du` 判断。
- `du -sh ~/Library/Containers/com.docker.docker` 给出 Docker 真实宿主占用。
- `docker system df` 说卷 `RECLAIMABLE 0%`，是因为卷被**已停止**的容器引用；
  先删容器才看得见可回收量。

## 第 2 步：清 Docker（通常是最大一块）

本仓的套路：`fb-mysql`、`fb-pg` 是 `scripts/test-db.sh up` 用 `docker run`
（无 `-v`）起的临时引擎，数据落在匿名卷里，脚本的 `down` 只删容器**不删卷**，
所以每跑一轮矩阵就多攒一份数据（实测累积到 19.6G）。

```bash
docker ps -a --format '{{.Names}}\t{{.Image}}\t{{.Status}}'
docker volume ls                      # 64 位十六进制名 = 匿名卷
docker rm -f fb-mysql fb-pg
docker volume rm <卷ID> <卷ID>         # 删前确认没有你要保留的业务库
docker system prune -af               # 未使用的镜像/容器/网络/构建缓存
```

代价要讲清楚：`prune -af` 会连 `postgres:14`、`mysql:8.0` 一起删掉，下次
`scripts/test-db.sh up` 需要重新拉（本机 docker.io 直连不通，见故障 4）。

## 第 3 步：让 Docker.raw 真正缩小（最容易被漏掉）

删完容器和卷，`docker system df` 已经是 0B，但宿主 `Docker.raw` 仍是 20G——
guest 释放的块没有 TRIM 回 macOS。在 VM 里对根命名空间跑 fstrim：

```bash
docker run --rm --privileged --pid=host "$TRIM_IMAGE" nsenter -t 1 -m -- fstrim -av
du -h ~/Library/Containers/com.docker.docker/Data/vms/0/data/Docker.raw
```

`TRIM_IMAGE` 用一个能拉到的小镜像（例：`alpine:3`，镜像站前缀按本机网络选）。
实测 `fstrim` 执行后宿主文件 20G → 1.2G，几秒内完成；输出里
`the discard operation is not supported` 是 virtiofs 挂载点的正常噪音，只看
`<path>: N MiB trimmed on /dev/vda1` 这行。

`scripts/docker-trim.sh`（本 skill 自带）把第 2、3 步串起来，带确认提示。

## 第 4 步：可再生缓存与历史日志

按"删了只是下次变慢"的标准，可以清：

| 目标 | 说明 |
| --- | --- |
| `go clean -cache`（`~/Library/Caches/go-build`） | 重建即恢复，通常 5G 级 |
| `~/Library/Caches/gopls` | gopls 索引 |
| `~/Library/Caches/com.microsoft.VSCode.ShipIt` | 已下载的 VSCode 更新包 |
| `~/Library/Caches/<*-updater>` | 各类编辑器的更新包（qoder 等） |
| `~/Library/Caches/JetBrains`、`~/Library/Logs/JetBrains` | 先确认 IDEA 没在跑，删了下次重新索引 |
| `~/Library/Caches/*.BundleMigration`、`~/Library/Caches/UDisplayLog` | 应用的一次性迁移/日志缓存 |
| `brew cleanup --prune=all` | 收益小（几十 M），但无风险 |
| `~/.forebrain/logs/<旧日期>` | 只删往前的日期目录，**保留当天**（活跃会话正在写） |

执行后复核：

```bash
df -h /System/Volumes/Data | tail -1
du -sh ~/Library/Caches ~/.forebrain/logs
```

## 不要动（会丢数据或代价远大于收益）

- `~/Library/Containers/<应用>`：微信、QQ、钉钉等聊天/文档数据（可达 5–9G 每个）。
- `~/workspace/*`：客户项目代码。
- `~/.forebrain/state/forebrain.state.sqlite` 及其 `-wal`：**正在写入的会话库**
  （可达 6G+），不是缓存；要压只能走产品自己的维护路径。
- `~/Library/Caches/ms-playwright`：`scripts/acceptance/web_e2e.sh` 的浏览器。
- `~/go/pkg/mod`：重下依赖，网络差时很痛。
- `~/Library/Caches/Google`：Chrome 在跑时跳过。
- `/private/var/vm`（swap）、Finder 本地快照以外的系统路径不要手动删。

## 故障与陷阱

1. **`docker ps` 卡住不返回**：守护进程已挂。用 socket 探活确认：
   `curl -sS --max-time 10 --unix-socket ~/.docker/run/docker.sock http://localhost/_ping`
   （`curl: (28)` = 无响应）。日志在
   `~/Library/Containers/com.docker.docker/Data/log/host/`，`supervisor.log`
   末尾停在哪一步就是卡在哪。
2. **杀不掉 Docker**：`osascript -e 'quit app "Docker"'` 和 `pkill -f com.docker.backend`
   （SIGTERM）都可能无效。要 `kill -9 <backend PID>`，再 `open -a Docker`，
   之后 30–60s 内 `_ping` 返回 `OK`（实测 6s）。
3. **磁盘满导致 Docker 起不来**：先清缓存腾出 ~10G，再重启 Docker，顺序不能反。
4. **docker.io 直连超时**（`context deadline exceeded`）：换镜像站拉取再打回原名，
   例如 `docker pull <mirror>/library/postgres:14 && docker tag <mirror>/library/postgres:14 postgres:14`。
5. **`docker system prune -f` 不删匿名卷**：卷只在 `--volumes` 或显式
   `docker volume rm` 时消失，这正是空间反复回来的原因。
6. **本仓习惯**：跑完 `scripts/test-db.sh all` 后执行 `scripts/test-db.sh down`
   之外，再手动删匿名卷，否则数据一直累积。

## 验收标准

- `df -h /System/Volumes/Data` 的 Available 明显回升（本会话 2.1Gi → 35Gi，99% → 82%）。
- `du -h .../Docker.raw` 回落到 GB 级（不是表观 228G）。
- `docker system df` 四行全 0B，`docker version` 能返回 Server 版本。
- `go build`/`make build` 能重建（缓存已清空，首次会慢）。
