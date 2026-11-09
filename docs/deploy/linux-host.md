# Linux 主机部署

在裸 Linux 主机上直接运行 forebrain 网关：没有 Docker、没有 Kubernetes，一个
systemd 服务加一个数据目录。适合单机、想要最少层数的部署。

- [Docker 单机部署](docker.md) —— 同样的单机形态，装进容器。
- [Kubernetes 部署](kubernetes.md) —— 多副本高可用。

## 适用场景与前提

- 一台 Linux 主机（x86_64 或 arm64），内存 ≥ 4GB（网关本体很小，但代理会在这台
  机器上跑 Node.js / Go / Python 的构建与测试）。
- 2 GB 以上磁盘给 FOREBRAIN_HOME（工作区、日志、状态）。
- 建议创建专用用户 `forebrain` 运行，不要用 root。
- 网关自带工具链的**容器镜像**里才有 git/Node/Python/Go；主机形态需要你自己安装：
  `git`、`node ≥ 20` 与 `npm`、`python3` 与 `pip`、`go ≥ 1.24`。它们是代理的
  shell 工具要用的，不是网关的运行依赖——缺了网关也能起，只是相关命令失败。
- 端口：网关监听 6060（默认 `0.0.0.0:6060`）。

## 获取安装物（离线）

发布物是 GitHub Release 上的单包压缩档，包含二进制加它旁边需要的全部资产：

```
forebrain_v<版本>_linux_amd64.tar.gz     # 或 linux_arm64
├── forebrain          # 网关二进制（内嵌 Web UI）
├── libpdfium.so       # 二进制的运行库（同目录，rpath 解析）
├── dict/              # 分词词典
├── models.json        # 模型目录
├── tokenizers/        # 分词器
└── fonts/             # CJK 兜底字体
```

有外网的机器上下载并校验（Release 页同时发布 sha256），拷进内网：

```bash
curl -fsSLO https://github.com/forebrain-harness/forebrain-harness/releases/download/v<版本>/forebrain_v<版本>_linux_amd64.tar.gz
sha256sum -c forebrain_v<版本>_linux_amd64.tar.gz.sha256

# 内网机器上：
sudo mkdir -p /opt/forebrain
sudo tar -C /opt/forebrain -xzf forebrain_v<版本>_linux_amd64.tar.gz
sudo useradd --system --home-dir /var/lib/forebrain --shell /usr/sbin/nologin forebrain
sudo mkdir -p /var/lib/forebrain && sudo chown forebrain:forebrain /var/lib/forebrain
/opt/forebrain/forebrain --version
```

也可以在内网的构建机上从源码构建：`make build`（产物在 `build/bin/`，同样需要
`libtokenizers`、`pdfium` 等前置下载，见仓库 README）。

## 数据库

默认 SQLite：状态库文件在 `$FOREBRAIN_HOME/state/` 下，单实例直接可用，什么都不用配。

多实例共享同一个状态库时**必须**用 MySQL 或 PostgreSQL（SQLite 无法多写）。两者
建表都由网关在首次启动和升级时自动完成，只需要建库建账号：

```sql
-- MySQL 8.x
CREATE DATABASE forebrain CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'forebrain'@'%' IDENTIFIED BY '<强口令>';
GRANT ALL PRIVILEGES ON forebrain.* TO 'forebrain'@'%';

-- PostgreSQL 14+
CREATE USER forebrain PASSWORD '<强口令>';
CREATE DATABASE forebrain OWNER forebrain;
```

连接串写进 forebrain.yaml 的 `database.url`：

```yaml
database:
  url: "mysql://forebrain:<口令>@mysql.internal:3306/forebrain"
  # 或 postgres://forebrain:<口令>@pg.internal:5432/forebrain?sslmode=disable
```

## forebrain.yaml 配置与环境变量

配置文件是 `$FOREBRAIN_HOME/forebrain.yaml`（0600）。home 里**没有** `.env`：网关
令牌要么是这个文件里的 `gateway.auth.token` 明文，要么是进程环境变量
`FOREBRAIN_GATEWAY_TOKEN`（容器/systemd 用后者）；模型密钥存在状态库的模型表里，在
网页「模型」页添加或修改。首次在终端里运行 `forebrain gateway start` 会进入交互式
引导（配置 provider/model/api key），也可以手写：

```yaml
gateway:
  http_addr: "0.0.0.0:6060"
  auth:
    token: "<openssl rand -hex 32 的输出>"   # 或 systemd 里设 FOREBRAIN_GATEWAY_TOKEN 并写 ${FOREBRAIN_GATEWAY_TOKEN}
database:                      # 不用外部数据库就删掉这一段
  url: "mysql://forebrain:<口令>@mysql.internal:3306/forebrain"
```

模型不用写在配置里：起服务 → 浏览器打开 `http://<主机>:6060` → 用上面的 token 登录 →
在「设置 · 模型」里添加一个模型（或在那里做订阅登录）。网关在没有模型时也照常对外
服务，正是为了让人能打开这一页。旧配置里若还留着 `agents.definitions.main.llm_providers`，
启动时会把它搬进模型表（密钥也在那时解析进去），之后只在网页里维护。

**项目根目录放在 FOREBRAIN_HOME 之外**（网关拒绝 `$FOREBRAIN_HOME` 内部路径作为项目
根，例如 `/var/lib/forebrain/workspace/...` 会报 `root must not be inside the forebrain
home directory`）：建议单独建 `/srv/forebrain-projects/` 一类目录，属主设为 forebrain
用户，网页里新建项目时填那里的路径。

运维在网关进程里设的环境变量只有「镜像源 / 企业证书」一类会传给代理的子进程
（`GOPROXY`、`PIP_INDEX_URL`、`NPM_CONFIG_REGISTRY`、`SSL_CERT_FILE`、
`NODE_EXTRA_CA_CERTS` 等，完整白名单见源码 `pkg/home/envguard.go`）；其余变量
（代理变量、凭据变量）一律不会透传。它们在 systemd 单元里用 `Environment=`
设置。

## 沙箱（可选，推荐）

网关用 bubblewrap 做 Linux 沙箱。装上并满足内核条件后，会话的「默认」预设把
模型命令关进沙箱（只读系统 + 可写工作区）；不装或条件不满足时网关照常运行，
但**所有命令都是完全访问（等同 YOLO），界面不会出现审批预设**。

```bash
sudo apt-get install bubblewrap          # ≥ 0.7；持续验证用的是 0.8.0

# 内核条件：允许非特权用户命名空间
sysctl kernel.unprivileged_userns_clone  # Debian 系需要 =1（不存在则内核默认允许）
sysctl user.max_user_namespaces          # 需要 >0
sudo sysctl -w kernel.unprivileged_userns_clone=1   # 写入 /etc/sysctl.d/ 持久化

# Ubuntu 23.10+/24.04：AppArmor 限制会挡住非特权用户命名空间
sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0

# 自检（无输出即沙箱可用）
bwrap --unshare-user --unshare-pid --die-with-parent \
  --ro-bind / / --dev /dev --proc /proc -- true
```

细节、逐条放行原因与宿主要求见 [`deploy/seccomp/README.md`](../../deploy/seccomp/README.md)
（seccomp 配置本身是容器部署才用的；主机形态只依赖内核与 AppArmor 开关）。

## 启动、停止、自启

```ini
# /etc/systemd/system/forebrain-gateway.service
[Unit]
Description=Forebrain Harness gateway
After=network-online.target

[Service]
User=forebrain
Group=forebrain
Environment=FOREBRAIN_HOME=/var/lib/forebrain
# 代理的 shell 工具要找到工具链；systemd 默认 PATH 不含它们
Environment=PATH=/opt/forebrain:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin
ExecStart=/opt/forebrain/forebrain gateway start
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now forebrain-gateway
systemctl status forebrain-gateway
journalctl -u forebrain-gateway -f          # 控制台日志；文件日志在 /var/lib/forebrain/logs/
curl -fsS http://127.0.0.1:6060/healthz     # 健康检查
```

浏览器打开 `http://<主机>:6060`，用 `forebrain.yaml` 里（或 `FOREBRAIN_GATEWAY_TOKEN`
环境变量）的 token 登录。部署后到网页「模型」页添加模型（或订阅登录），再开始对话。

## 反向代理与 HTTPS

内网通常由 Nginx 前置并挂企业证书。两处必须改：WebSocket 长连接的超时（会话
socket 会挂一小时以上）和上传体积：

```nginx
server {
    listen 443 ssl;
    server_name forebrain.example.internal;
    ssl_certificate     /etc/nginx/ssl/forebrain.crt;
    ssl_certificate_key /etc/nginx/ssl/forebrain.key;

    location / {
        proxy_pass http://127.0.0.1:6060;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;      # WebSocket
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_read_timeout 3600s;                    # 长会话 / 审批挂起
        proxy_send_timeout 3600s;
        client_max_body_size 64m;                    # 日志、文档上传
    }
}
```

## 多副本

主机形态可以做多副本，条件与容器形态相同：**MySQL/PostgreSQL + 所有实例共享同
一个 FOREBRAIN_HOME（NFS 等共享存储，必须支持原子 rename）+ 每台自己的
`FOREBRAIN_INTERNAL_ADDR`（字面 IP，端口同 6060）**。副本间鉴权是共享 token 加
源 IP 必须落在已注册副本的 internal_addr 里（不读 X-Forwarded-For）。门槛主要在
共享存储，若需要多副本优先考虑 [Kubernetes 部署](kubernetes.md#多副本)。

## 离线内网要点

- 运行时不需要任何公网访问；模型端点写内网地址（`base_url`）。
- 镜像源变量按上一节在 systemd 里设置：`GOPROXY`/`PIP_INDEX_URL`/
  `NPM_CONFIG_REGISTRY` 指内网源；企业自签证书用 `SSL_CERT_FILE`、
  `NODE_EXTRA_CA_CERTS`、`PIP_CERT` 等（白名单见 `pkg/home/envguard.go`）。
- 升级包同样走 Release 压缩档 + sha256 校验。

## 升级与备份

- 升级：停服务 → 解压新版到 `/opt/forebrain`（覆盖）→ 起服务。状态库结构随启动
  自动迁移；跨大版本前先备份。
- 备份：停服务后 `tar -C /var/lib -czf forebrain-home.tgz forebrain/`；外部数据库
  用 `mysqldump` / `pg_dump`。`forebrain.yaml`、状态库与 workspace 都在 home 里，一并备份。

## 排障表

| 现象 | 原因与处理 |
| --- | --- |
| 启动报 `startup config incomplete` | 主代理还没有可用模型。在终端重跑 `forebrain gateway start` 走引导，或启动后到网页「模型」页添加模型（或订阅登录）。 |
| 网页登录 401 / 一直未授权 | 浏览器里的 token 与 `forebrain.yaml` 的 `gateway.auth.token`（或 `FOREBRAIN_GATEWAY_TOKEN` 环境变量）不一致；改过之后要重启服务。 |
| `bwrap: No permissions to create new namespace` | 内核不允许非特权用户命名空间：见「沙箱」节的 sysctl。此时网关以无沙箱模式运行（全访问、无审批预设）。 |
| `Can't mount proc on /newroot/proc` | 容器里挂 seccomp 才会遇到；主机形态出现说明 /proc 挂载异常，检查是否在嵌套容器里运行。 |
| 数据库连不上 / 建表失败 | 检查 `database.url`、账号权限与网络；日志在 `/var/lib/forebrain/logs/`。 |
| 代理执行 npm/pip/go 拉包失败 | 内网源变量没设或子进程拿不到证书：确认 systemd `Environment=` 里的镜像变量与证书路径，证书文件对 forebrain 用户可读。 |
| 端口被占用 | 改 `gateway.http_addr` 端口，反代同步改。 |
