# 部署 forebrain 网关

forebrain 网关（含 Web 界面）支持三种部署方式，按场景选择：

| 方式 | 文档 | 适用 |
| --- | --- | --- |
| Linux 主机 | [linux-host.md](linux-host.md) | 单机、不想引入容器；systemd 直接跑。 |
| Docker 单机 | [docker.md](docker.md) | 单机、有 Docker；一个 compose 文件、一个数据卷。 |
| Kubernetes | [kubernetes.md](kubernetes.md) | 多副本高可用；RWX 共享存储 + 外部 MySQL/PostgreSQL。 |

三种方式共同的模型：

- **一个 FOREBRAIN_HOME**：配置（`forebrain.yaml`）、密钥（`.env`，配置里以
  `${…}` 引用）、工作区、日志都在这一个目录/卷里。首次启动写一次，之后配置归
  网页的配置编辑器管。**项目根目录必须在这个目录之外**（网关拒绝 home 内部路径作为
  项目根），三种方式都要为项目存储单独挂卷或目录。
- **一个共享 token**：`FOREBRAIN_GATEWAY_TOKEN` 既是网页登录令牌，也是副本间
  鉴权凭据。
- **一个 LLM 提供方**：网关只有在主代理的 LLM（provider/model/api_key/base_url）
  配好之后才对外服务；三种方式的首次配置都围绕这一点。
- **数据库**：单实例默认 SQLite（就在 home 里）；**多副本必须 MySQL 或
  PostgreSQL**（建库即可，建表自动）。
- **沙箱（可选，推荐）**：Linux 容器/主机上由 bubblewrap 提供，需要最小放行
  seccomp（`deploy/seccomp/forebrain-bubblewrap.json`）或内核允许非特权用户
  命名空间。不满足时网关照常运行，但所有命令完全访问（等同 YOLO）、界面不出现
  审批预设——这是可选能力的关闭，不是故障。
- **离线内网**：运行时不需要公网。npm/pip/go 的内网镜像源与企业自签证书通过
  环境变量注入（白名单：`GOPROXY`、`PIP_INDEX_URL`、`NPM_CONFIG_REGISTRY`、
  `SSL_CERT_FILE`、`NODE_EXTRA_CA_CERTS` 等，完整清单见源码
  `pkg/home/envguard.go`）；模型端点写内网地址。镜像构建的离线做法见仓库
  README「Building the container image」。
