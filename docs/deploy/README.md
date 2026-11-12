# 部署 forebrain 网关

forebrain 网关（含 Web 界面）支持三种部署方式，按场景选择：

| 方式 | 文档 | 适用 |
| --- | --- | --- |
| Linux 主机 | [linux-host.md](linux-host.md) | 单机、不想引入容器；systemd 直接跑。 |
| Docker 单机 | [docker.md](docker.md) | 单机、有 Docker；一个 compose 文件、一个数据卷。 |
| Kubernetes | [kubernetes.md](kubernetes.md) | 多副本高可用；RWX 共享存储 + 外部 MySQL/PostgreSQL。 |

三种方式共同的模型：

- **一个 FOREBRAIN_HOME**：配置（`forebrain.yaml`）、状态库、工作区、日志都在这一个
  目录/卷里（没有 `.env`：网关令牌在 `gateway.auth.token` 或环境变量；模型密钥在状态库
  的模型表里）。首次启动写一次，之后配置归网页的配置编辑器管。**项目根目录必须在这个
  目录之外**（网关拒绝 home 内部路径作为项目根），三种方式都要为项目存储单独挂卷或目录。
- **一个共享 token**：`FOREBRAIN_GATEWAY_TOKEN` 既是网页登录令牌，也是副本间
  鉴权凭据。
- **一个 LLM 提供方**：网关只有在主代理的模型配好之后才对外服务；三种方式的首次
  配置都围绕这一点。**部署后到网页「模型」页添加模型**（或订阅登录），密码/密钥就存在
  模型表里，改配置也只在那里改。
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

## 日志

每个进程把自己的记录写成 JSON 行，都在 `FOREBRAIN_HOME/logs` 下，三种部署方式一致：

```
logs/<YYYY-MM-DD>/<source>-<host>-<pid>-<proc>.jsonl      一行一条记录（续段 ….<n>.jsonl）
logs/<YYYY-MM-DD>/stderr-<source>-<host>-<pid>-<proc>.log 进程原始 stderr
logs/<YYYY-MM-DD>/x/<调用 ID>.req.gz | .resp.gz            一次模型调用的请求/响应报文
```

- **三种来源**：`tui`（终端）、`gateway`（网页与网关）、`cli`。目录按写入方本地日期命名，
  时区不同的副本各写各的目录。
- **保留**：`SweepLogs` 每小时跑一次，删掉 7 天前的日期目录；整个
  `logs` 树超过 1 GiB 时先删最旧日期的 `x/` 报文，再删整天（今天的永远保留）。所以
  `logs` 的大小上限由这个预算决定，不需要额外的清理任务。
- **多副本**：每个副本写自己的文件（文件名带 pid 与启动随机段），同一时刻不会有两个进程
  写同一个文件；所有副本读同一个共享 home，网页上的日志页因此能看到全部副本的记录。
  副本之间没有进程内缓存，也没有实时推送——换一页就是重新读一次文件。
- **离线环境无额外依赖**：日志是纯文本 JSON 行 + gzip，不需要日志服务、数据库或采集代理。
- **报文开关**：`FOREBRAIN_LLM_HTTP_DEBUG=0` 关掉请求/响应报文捕获，只留记录里的元数据
  （模型、状态码、耗时、字节数）；凭据在写入时已脱敏（`Authorization`、`x-api-key` 等
  按名字规则改写），报文因此可以整份下载来看。
- **在终端里读**：

  ```sh
  tail -f "$FOREBRAIN_HOME/logs/$(date +%F)/gateway-*.jsonl" | jq -c '[.t,.lvl,.session,.msg]'
  ```

  完整报文先解压再看：`gunzip -c <home>/logs/<日期>/x/<调用 ID>.resp.gz`（起始行与头行之后
  的空行下面是响应体）。

网页上的「日志」页读的就是这些文件：`GET /api/logs`（按时间、级别、来源、项目、会话、
轮次、类型、关键字查，一页 50 条，附级别统计与 60 段直方图）、
`GET /api/logs/exchange?day=&id=`（一次调用的请求、响应、摘要与同时段的其他调用）、
`GET /api/logs/blob?day=&name=`（报文解压后按 `offset`/`limit` 取一段，`download=1` 取整份）。
三个接口都在已鉴权的 `/api` 组下，非 GET 一律 405。
