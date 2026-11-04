# Docker 单机部署

用 Docker Compose 在一台机器上运行网关：一个 `forebrain-home` 卷保存全部状态，
可选一个 PostgreSQL。这是三种部署方式里最省事的单机形态。

- [Linux 主机部署](linux-host.md) —— 不用容器的单机形态。
- [Kubernetes 部署](kubernetes.md) —— 多副本高可用。

## 适用场景与前提

- Docker 24+ 与 docker compose v2；内存 ≥ 4GB、磁盘 ≥ 10GB（镜像约 1.4GB，
  自带 git / Node.js 22 / Python 3 / Go / bubblewrap，代理的构建与测试都在容器里跑）。
- 宿主内核允许非特权用户命名空间时沙箱可用（见「沙箱」节）；不允许时网关照常
  运行，所有命令完全访问（等同 YOLO），界面不出现审批预设。
- 端口 6060。

## 获取安装物（离线）

镜像在有网络的机器上构建，拷进内网运行（构建本身不需要内网有公网）：

```bash
# 有网机器：构建 + 导出。完整镜像加速命令见仓库 README
# 「Building the container image」一节（DaoCloud/Aliyun/npmmirror/goproxy.cn）。
docker build -t forebrain:dev .
docker save forebrain:dev | gzip > forebrain-dev.tar.gz

# 内网机器：
gunzip -c forebrain-dev.tar.gz | docker load
docker run --rm forebrain:dev --version
```

Compose 文件在仓库 `deploy/docker/compose.yaml`，seccomp 配置在
`deploy/seccomp/forebrain-bubblewrap.json`（两者一起拷，相对路径是配套的）。

## 数据库

默认 SQLite，状态在 `forebrain-home` 卷里，单实例直接可用。

可选 PostgreSQL 由 compose 的 `pg` profile 提供（数据落在 `forebrain-pgdata` 卷）；
用外部 MySQL/PostgreSQL 时只需要建库建账号（建表由网关自动完成）：

```sql
-- MySQL 8.x
CREATE DATABASE forebrain CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'forebrain'@'%' IDENTIFIED BY '<强口令>';
GRANT ALL PRIVILEGES ON forebrain.* TO 'forebrain'@'%';

-- PostgreSQL 14+
CREATE USER forebrain PASSWORD '<强口令>';
CREATE DATABASE forebrain OWNER forebrain;
```

## forebrain.yaml 配置与环境变量

网关只有在主代理的 LLM 配好之后才会对外服务，所以**第一次 `up`** 由 `seed`
服务把 `forebrain.yaml` 和 `.env` 写进卷（之后就不再动，文件归网页配置编辑器管）。
LLM 四项（provider/model/api_key/base_url）通过环境变量交给 seed：

```bash
export FOREBRAIN_GATEWAY_TOKEN=$(openssl rand -hex 32)   # 网页登录与副本间共享的令牌
export FOREBRAIN_LLM_PROVIDER=deepseek                   # 或内网 OpenAI 兼容端点
export FOREBRAIN_LLM_MODEL=<模型名>
export FOREBRAIN_LLM_API_KEY=<模型 API key>
export FOREBRAIN_LLM_BASE_URL=https://api.deepseek.com   # 内网部署换成内网端点
```

改配置在网页里做（配置页写回 `forebrain.yaml`），或直接编辑卷里的文件后
`docker compose restart gateway`。

**项目根目录放在 FOREBRAIN_HOME 之外**：网关拒绝把 `forebrain-home` 内部路径用作项目根
（`root must not be inside the forebrain home directory`）。给项目存储另挂一个卷或绑定
目录，例如在 compose 的 gateway 下加：

```yaml
    volumes:
      - forebrain-home:/data/forebrain
      - ./projects:/workspace        # 项目根目录放这里
```

内网镜像源与企业证书变量加在 compose 的 `gateway.environment`（示例已在文件里）：
`GOPROXY`、`PIP_INDEX_URL`、`NPM_CONFIG_REGISTRY`、`SSL_CERT_FILE`、
`NODE_EXTRA_CA_CERTS` 等，白名单见源码 `pkg/home/envguard.go`；证书文件要一并挂进
容器（如挂到 `/data/forebrain/certs/`）。

## 沙箱（可选，推荐）

compose 里 gateway 的 `security_opt` 三件套就是沙箱开关：

```yaml
security_opt:
  - seccomp=../seccomp/forebrain-bubblewrap.json   # 放行用户命名空间（默认 seccomp 挡住）
  - systempaths=unconfined                         # 解除 /proc 遮蔽，否则沙箱挂不上新 proc
  - apparmor=unconfined                            # Ubuntu 宿主的 docker-default 会挡挂载
```

宿主条件与逐条验证记录见 [`deploy/seccomp/README.md`](../../deploy/seccomp/README.md)。
Ubuntu 宿主还需：

```bash
sudo sysctl -w kernel.unprivileged_userns_clone=1                     # 若存在该项
sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0         # 23.10+
```

整体删掉这三行 → 网关照常启动，但所有命令完全访问、界面没有审批预设。这不是
故障态，是可选能力的关闭。

## 启动、停止、自启

```bash
# SQLite 单机（默认）
docker compose -f deploy/docker/compose.yaml up -d

# 可选 PostgreSQL profile
export FOREBRAIN_DATABASE_URL='postgres://forebrain:forebrain@postgres:5432/forebrain?sslmode=disable'
docker compose -f deploy/docker/compose.yaml --profile pg up -d

docker compose -f deploy/docker/compose.yaml ps
docker compose -f deploy/docker/compose.yaml logs -f gateway
curl -fsS http://127.0.0.1:6060/healthz
```

浏览器打开 `http://<主机>:6060`，用 `FOREBRAIN_GATEWAY_TOKEN` 登录。

- 停止/重启：`docker compose … down`（卷保留）→ 再 `up -d` 恢复；
  `down -v` 会**删除全部数据**（配置、状态、工作区），只在重来时用。
- 自启：`restart: unless-stopped` 已写在 compose 里，Docker 服务开机自启
  （`systemctl enable docker`）即整机自启。

## 反向代理与 HTTPS

宿主 Nginx（或独立的 Nginx 容器）前置，配置与
[Linux 主机部署](linux-host.md#反向代理与-https)完全相同：WebSocket 升级头、
`proxy_read_timeout 3600s`、`client_max_body_size 64m`，`proxy_pass` 指向
`http://127.0.0.1:6060`。

## 多副本

单机多实例**只在配了 MySQL/PostgreSQL 时成立**（SQLite 不能两个写入方）。做法在
`deploy/docker/compose.yaml` 末尾的注释里：`deploy.replicas: 2` 加每实例的
`FOREBRAIN_INTERNAL_ADDR`（字面 IP，同 6060 端口；副本间鉴权=共享 token+源 IP 匹配，
不读 X-Forwarded-For）。经过完整测试的两副本编排是
`deploy/compose/multi-replica.yaml`；跨主机多副本见 [Kubernetes 部署](kubernetes.md)。

## 离线内网要点

- 运行时不需要公网；模型 `base_url` 指内网端点。
- 镜像走 `docker save`/`docker load`（见「获取安装物」）；内网自建 registry 时改
  compose 的 `FOREBRAIN_IMAGE`。
- 镜像源/证书变量与证书挂载见「forebrain.yaml 配置与环境变量」。
- 代理在容器里执行 npm/pip/go 时，拉包走你配置的内网源。

## 升级与备份

- 升级：新镜像构建/导入后改 `FOREBRAIN_IMAGE`（或重新打 `forebrain:dev` 标签），
  `docker compose up -d` 重建容器；卷里的数据不动，状态库自动迁移。
- 备份（网关可在线，建议低峰）：
  ```bash
  docker run --rm -v forebrain_forebrain-home:/data -v "$PWD":/backup \
    --entrypoint tar forebrain:dev -czf /backup/forebrain-home.tgz -C /data .
  ```
  外部数据库用 `mysqldump` / `pg_dump`；pg profile 的数据在 `forebrain-pgdata` 卷。

## 排障表

| 现象 | 原因与处理 |
| --- | --- |
| `seed` 报四个 `FOREBRAIN_LLM_*` 未设 | 首次 `up` 必须带 LLM 四项；这是设计（网关没配好 LLM 不服务）。 |
| gateway 反复重启报 `startup config incomplete` | 卷里的 `forebrain.yaml` 缺 LLM 配置：进网页前请先看 `logs gateway`；直接改卷里文件后 `restart gateway`。 |
| 网页登录 401 | token 与卷里 `.env` 的 `FOREBRAIN_GATEWAY_TOKEN` 不一致。 |
| `bwrap: Can't mount proc on /newroot/proc` | 少了 `systempaths=unconfined`（或宿主不允许用户命名空间）。前者照 compose 补上；后者属宿主限制，网关以无沙箱模式运行。 |
| `--profile pg` 起动时 gateway 反复重启 | 数据库还没就绪，`restart: unless-stopped` 会自己恢复；持续失败查 `FOREBRAIN_DATABASE_URL` 与数据库本身。 |
| 代理执行 npm/pip/go 拉包失败 | 内网源变量没加进 `gateway.environment`，或证书没挂进容器。 |
| 磁盘涨满 | 工作区/日志在 `forebrain-home` 卷；`docker system df` 分辨镜像与卷的占用。 |
