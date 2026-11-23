# 214 — 部署：gateway 镜像（Dockerfile）、Linux 主机 / Docker / K8s 三份部署文档、K8s 清单重写

- **基线**：`f1a05780`（`exec/213` 顶端；213 未合并入 dev，故本计划从它起）。
- **状态**：TODO
- **优先级 / 工作量 / 风险**：P1 / M / MED（文档与清单为主；容器内沙箱需要真机验证）
- **依赖**：208（离线内网）、213（多副本）、215（沙箱有就用、没有就不用）、216（跑批语义：文档要写清跑批在有 / 无沙箱时的读写边界与「启用沙箱」开关）DONE。
- **设计**：`docs/plan/MULTI_DB_MULTI_REPLICA_DESIGN.md`；`plans/208-enterprise-intranet-offline.md`（镜像变量白名单、离线降级）；`plans/213-multi-replica-gateway.md`（`FOREBRAIN_INTERNAL_ADDR`、多副本集成测试）。
- **执行隔离**：worktree + `exec/214` 分支 + 增量提交（owner 2026-11-03 裁定）；合并在 owner 批准后 ff-only。

> **执行者须知**：逐步执行，每步跑「验证」确认结果再进下一步。碰到 §8 STOP 条件就停下报告。完成后把 `plans/README.md` 里 214 那一行的状态改为 DONE 并写一句结果。
>
> **Drift check（先跑）**：`git diff --stat f1a05780 -- Dockerfile Dockerfile.ubuntu-bubblewrap deploy/ docs/deploy Makefile` 应无输出；`ls deploy/k8s/manifests/` 仍是 7 个文件；`grep -n "static-dist\|OPENAI_API_KEY" deploy/k8s/manifests/deployment.yaml` **有**输出（说明清单确实还是旧时代那一版，本计划要重写它）。任一不符就 STOP 并说明。
>
> **2026-11-03 reconcile（相对 06f1bc57 的漂移，已在本文件原地改正）**：
> 1. 基线从 `06f1bc57` 改为 `f1a05780`（213 的 12 笔提交之后）；213 未合并入 dev，本计划的分支从 `exec/213` 起。
> 2. 执行隔离从「零 commit（owner 手动提交）」改为 worktree + `exec/214` + 增量提交。
> 3. §2 的 `/healthz`、`/readyz` 实际在 `pkg/gateway/server.go:776-777`（不是 `:794-795`）；`forebrain gateway start` 的注册在 `cmd/forebrain/gateway.go:12`（`gatewayCmd.AddCommand(gatewayStartCmd, gatewayStatusCmd, gatewayStopCmd)`）。
> 4. **213 落地后新增的四个部署事实**（清单与文档必须写进去）：
>    - 内部端点挂在网关的**同一个监听**上（`/internal/v1/ws`、`/internal/v1/run-op`），所以 `FOREBRAIN_INTERNAL_ADDR` 指的是网关自己的 HTTP 端口（`http://$(POD_IP):6060`），**不是**另开一个 8787。
>    - 多副本的内部鉴权 = 共享的 `gateway.auth.token` **且** 来源 IP 必须等于某个心跳新鲜的已登记副本的 `internal_addr` 主机（**不采信 `X-Forwarded-For`**）。⇒ 两个副本的 `FOREBRAIN_INTERNAL_ADDR` 必须写**字面 IP**（写主机名会被跳过）；前面有反向代理时，代理必须透传真实源 IP，或让副本直连。
>    - 全新共享 home 的 gateway token 是「后写者赢」：两个副本同时启动会各生成一个。compose / K8s 必须**先**把 token 写进共享配置（213 的 compose 用 `seed` 服务做这件事，本计划的 K8s init 容器同理）。
>    - `fb_leases`（schema v32）承载长连接渠道的「一个账号一个副本」；文档的多副本一节要写明它，并说明 `logs/debug.log` 仍是共享文件（opt-in、带轮转）。

## 0. owner 裁决（执行期不得改）

1. **企业内网三种部署方式**：Linux 主机、Docker、K8s；**必须生成三份部署说明文档**，并**生成 gateway 的 Dockerfile**。
2. 企业内网＝**离线**（不通公网），有 npm / pip / go 内网镜像源。
3. **镜像带 bubblewrap**，仓库提供**最小放行的 seccomp 配置**，Docker / K8s 部署时挂上即可启用沙箱；**不挂、或宿主不允许用户命名空间时，沙箱不可用，网关照常运行**——任何环境沙箱都是有就用、没有就不用，跑批也不例外（215）。文档把「挂 seccomp 配置」写成推荐项而非必需项，并向运维写明：**没有沙箱时 forebrain 一律按完全访问运行（等同 YOLO），界面上不出现审批预设、也不提示**——要让用户能选「默认」并受沙箱保护，就必须启用沙箱。
4. **镜像自带常用工具链**：git、Node.js/npm、Python3/pip、Go（镜像约 1.5GB 可接受）。
5. 多副本只用于 MySQL / PostgreSQL，共享存储、共享同一个 FOREBRAIN_HOME。
6. 文档用中文（仓库已有中文文档先例：`docs/integration/batch-tasks.md`）。

## 1. 交付

- `Dockerfile`（重写；`make docker` 用它）。
- `deploy/seccomp/forebrain-bubblewrap.json`（最小放行配置）。
- `deploy/docker/compose.yaml`（单机：网关 + 可选 PostgreSQL）。
- `deploy/k8s/manifests/`（重写：多副本 + RWX 共享卷 + 外部 MySQL / PG + seccomp + 内部地址）。
- `docs/deploy/linux-host.md`、`docs/deploy/docker.md`、`docs/deploy/kubernetes.md`，以及 `docs/deploy/README.md`（三种方式怎么选，一页）。
- 真机验收记录。

## 2. 现状事实（已对基线核实）

- 启动命令：`forebrain gateway start`（`cmd/forebrain/gateway.go:20-30`；另有 `status`、`stop`）。默认监听 `127.0.0.1:6060`（`pkg/config/load.go:632`、`pkg/gateway/serve_run.go:229`）。健康检查 `/healthz`、`/readyz`（`pkg/gateway/server.go:794-795`）。
- home：环境变量 `FOREBRAIN_HOME`（`pkg/home/data.go:27`）；配置文件在 home 下按 `forebrain.yaml` / `.yml` / `.json` 顺序查找（`pkg/home/data.go:77-91`）。
- forebrain.yaml 只认三项 + 数据库（203–209 之后）：`gateway.http_addr`、`gateway.auth.token`、`credentials.chatgpt`、`database.url`。其余配置都在状态库里，由网页 / 终端改。
- 现有 `Dockerfile`：三段构建（node:22 构前端 → golang:1.27 带 CGO / fts5 / libtokenizers / pdfium 构二进制并装词典与 `models.json` / tokenizers / fonts → `debian:bookworm-slim` 运行），`USER 65534`、`EXPOSE 6060`、`CMD ["gateway","start"]`；**没有 bubblewrap，也没有 git / npm / pip / go**。
- `Dockerfile.ubuntu-bubblewrap`：只装了 bubblewrap / ca-certificates / curl 的 ubuntu:24.04（执行时 `grep -rn "Dockerfile.ubuntu-bubblewrap" .` 确认用途；仅供测试则保留不动）。
- 现有 `deploy/k8s/manifests/`：单副本、`ReadWriteOnce` PVC、init 容器每次启动覆盖 `forebrain.yaml` 与 `agents/default.yaml`、args `["serve", "--static-dist=/app/static"]`（**命令已不存在**）、`OPENAI_API_KEY` 环境变量、configmap 里是旧时代的 `agents.definitions.main.channels` 配置——整体过时，重写。
- Linux 沙箱后端找 `bwrap` / `bubblewrap`（`pkg/safety/backend_linux.go:30-32,91-94`），找不到报 `bubblewrap_not_found`。
- npm 包：`@forebrain-harness/forebrain` + 按平台的可选依赖包，包里已带词典、`models.json`、tokenizers、pdfium（`npm/scripts/build-platform-packages.sh:5-40`）——离线安装不需要再下载这些。

## 3. 设计

### 3.1 Dockerfile

- 构建段照旧（前端、二进制、资源）；运行段改为 `ubuntu:24.04`（或 `debian:bookworm`，二选一并在文件头注释写明理由），安装：`bubblewrap`、`ca-certificates`、`git`、`curl`、`python3`、`python3-pip`、`python3-venv`、Node.js LTS + npm（用发行版包或 NodeSource——构建时联网，运行时不联网）、Go（官方 tarball 解到 `/usr/local/go`，`PATH` 加上）、`libstdc++6`、`libfontconfig1`。
- 非 root 用户 `forebrain`（固定 UID/GID 10001），`HOME=/home/forebrain`，`FOREBRAIN_HOME=/data/forebrain`，`VOLUME /data/forebrain`。
- `EXPOSE 6060`；`ENTRYPOINT ["/app/forebrain"]`；`CMD ["gateway","start"]`；`HEALTHCHECK` 用 `/healthz`。
- 首次启动且 home 里没有 forebrain.yaml 时：用环境变量 `FOREBRAIN_HTTP_ADDR`（默认 `0.0.0.0:6060`）、`FOREBRAIN_GATEWAY_TOKEN`、`FOREBRAIN_DATABASE_URL` 生成一份（一个小的入口脚本 `deploy/docker/entrypoint.sh`；已有文件则**不覆盖**——网页的配置文件编辑器会改它）。没给 token 就随机生成并打印一次到日志。
- 不在镜像里放任何凭据。

### 3.2 seccomp 配置

- 以 Docker / containerd 默认 seccomp 配置为底（复制官方 `default.json` 对应版本），只额外放行 bubblewrap 需要的：`clone` / `clone3` / `unshare` 带 `CLONE_NEWUSER|CLONE_NEWNS|CLONE_NEWPID|CLONE_NEWNET|CLONE_NEWIPC|CLONE_NEWUTS`、`mount` / `umount2` / `pivot_root`（在新用户命名空间内）、`setns`。逐条验证：去掉任何一条，§5 的容器内沙箱测试就失败——用这个方法确定**最小**集合，并在文件旁 `deploy/seccomp/README.md` 列出每条为什么需要。
- Ubuntu 主机上 AppArmor 的 `docker-default` 会挡 bubblewrap 的 mount：文档写明 Docker 用 `--security-opt apparmor=unconfined`、K8s 用 `container.apparmor.security.beta.kubernetes.io/<容器名>: unconfined`（或 1.30+ 的 `securityContext.appArmorProfile.type: Unconfined`）。若能写出一个最小 AppArmor 配置替代 unconfined，优先用它。
- 主机内核要求：允许非特权用户命名空间（`kernel.unprivileged_userns_clone=1`；Ubuntu 24.04 还有 `kernel.apparmor_restrict_unprivileged_userns`）。文档写检查命令与开启方法。

### 3.3 Docker 单机（`deploy/docker/compose.yaml`）

- 服务 `gateway`：镜像、端口 `6060`、卷 `forebrain-home:/data/forebrain`、`security_opt: [seccomp=../seccomp/forebrain-bubblewrap.json, apparmor=unconfined]`、环境变量（token、数据库 URL、208 的镜像变量示例）。
- 可选服务 `postgres`（profile `pg`）：`postgres:14`，数据卷，`FOREBRAIN_DATABASE_URL` 指向它。
- 多实例（同机多容器）只在配了 MySQL / PG 时允许——文档说明，并给出 `deploy: replicas: 2` 的示例与 `FOREBRAIN_INTERNAL_ADDR` 的设置方式。

### 3.4 K8s 清单（`deploy/k8s/manifests/`，kustomize）

- `namespace.yaml`。
- `secret.yaml`：`forebrain.yaml` 初始内容（`gateway.http_addr: 0.0.0.0:6060`、`gateway.auth.token`、`database.url`），占位值，文档说明替换。
- `pvc.yaml`：`ReadWriteMany`（注释：需要 NFS / CephFS 等支持 RWX 与原子重命名的存储类）。
- `deployment.yaml`：`replicas: 2`；`strategy: RollingUpdate`；init 容器「home 里没有 forebrain.yaml 才从 Secret 拷贝」（不覆盖）；容器 env：`FOREBRAIN_HOME`、`POD_IP`（downward API）、`FOREBRAIN_INTERNAL_ADDR=http://$(POD_IP):6060`、镜像变量示例；`securityContext`：非 root、`seccompProfile: {type: Localhost, localhostProfile: forebrain-bubblewrap.json}`（文档说明把 json 放到各节点 kubelet 的 seccomp 目录，或用 Security Profiles Operator）、AppArmor 按 3.2；探针 `/readyz`、`/healthz`；资源请求 / 上限示例。
- `service.yaml`：ClusterIP，**不设会话粘滞**（213 的副本间转发负责）。
- `ingress.yaml`：nginx-ingress 示例（WebSocket：`proxy-read-timeout` / `proxy-send-timeout` 3600、`proxy-body-size` 适配附件上传）。
- 数据库不在清单里部署（用企业已有的 MySQL / PG）；文档给建库语句。

### 3.5 文档大纲（三份都按这个顺序，内容按部署方式取舍；简洁，能用表格就不用长句）

1. 适用场景与前提（系统 / 内核版本、CPU / 内存、磁盘、是否离线）。
2. 获取安装物（**离线**）：主机——内网 npm 镜像源安装 `npm install -g @forebrain-harness/forebrain --registry=<内网源>`，或拷贝 `npm pack` 出的 tgz 离线安装；Docker / K8s——在能联网的机器上构建或拉取镜像，`docker save` / `docker load` 或推送到内网镜像仓库。
3. 数据库：默认 SQLite（单机、单实例）；MySQL 8.0+ 建库 `CREATE DATABASE forebrain CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;` 与用户授权；PostgreSQL 14+ 建库与授权；`database.url` 的两种写法；备份交给 DBA。
4. 配置 forebrain.yaml（四项，逐项说明）与环境变量（`FOREBRAIN_HOME`、`FOREBRAIN_INTERNAL_ADDR`、208 的镜像 / 证书变量白名单全表）。
5. 沙箱（可选，推荐）：启用条件（bubblewrap、用户命名空间、容器的 seccomp / AppArmor）、没有沙箱时的行为（一律按完全访问运行、审批预设与沙箱设置不出现、跑批照常跑）与自检方法（启动后在网页「运行状态」或 `forebrain gateway status` 看沙箱是否可用——执行时确认实际能看到沙箱状态的位置）。
6. 启动、停止、开机自启（主机：systemd 单元文件全文，`Environment=` 写镜像变量；Docker：compose；K8s：`kubectl apply -k`）。
7. 反向代理与 HTTPS（nginx 配置全文，含 WebSocket 升级与超时）。
8. 多副本（只在 Docker 多实例 / K8s 章节）：必须 MySQL / PG、RWX 共享存储、`FOREBRAIN_INTERNAL_ADDR`、副本间网络放通 6060。
9. 离线内网要点：不需要公网；「允许内网地址」开关（设置 → 联网工具）用于抓内网网页；镜像源变量；模型服务填内网地址。
10. 升级与备份（home 目录 + 数据库）。
11. 排障表（`bubblewrap_not_found`、用户命名空间被禁、seccomp 拒绝、数据库连不上、多副本 SQLite 被拒、WS 断开）。

## 4. 步骤

1. Dockerfile + 入口脚本；`docker build -t forebrain:dev .` 成功；`docker run --rm forebrain:dev --version` 正常；镜像内 `bwrap --version`、`git --version`、`node --version`、`npm --version`、`python3 --version`、`pip3 --version`、`go version` 都有输出。
2. seccomp 配置（3.2），按「逐条去掉验证」确定最小集合。
3. compose（3.3）。
4. K8s 清单（3.4）；`kubectl kustomize deploy/k8s/manifests` 渲染无错。
5. 三份文档 + `docs/deploy/README.md`。
6. 真机验收（§5）。

## 5. 真机验收（每项记录命令与结果写进 README；没有对应环境的项：把步骤与预期结果写成「用户手动验收清单」，附在本计划末尾并抄进 README 的结果栏，**由用户手动验收**；其余完成判据全部通过即可标 DONE（注明「含用户手动验收项」））

- **Docker 单机**：按 `docs/deploy/docker.md` 原样操作起网关；网页登录；在一个「默认」预设的会话里让模型执行 `touch /data/forebrain/../outside`（应被沙箱拒绝或要求审批）与工作区内写文件（应成功）；跑一个跑批任务成功（证明容器内沙箱可用）。去掉 seccomp 参数重跑 → 网关照常启动、设置里没有「审批与沙箱」、命令不弹审批、跑批照常跑完（215 的行为）。
- **离线**：在 208 §6 的 `--internal` 网络里按文档部署，`npm install` / `pip install` / `go mod download` 走内网源成功。
- **Linux 主机**：在一台 Ubuntu 24.04（虚拟机即可）按 `docs/deploy/linux-host.md` 原样操作：systemd 启动、开机自启、nginx 反代 + HTTPS、沙箱可用。
- **K8s**：kind / minikube 集群 + NFS RWX 存储类 + PostgreSQL，按 `docs/deploy/kubernetes.md` 原样操作起 2 副本；跑 213 的 `scripts/multi-replica-test.sh` 指向该集群的场景 1–3 全部通过。
- **文档可照做**：验收人只照文档操作、不看源码；任何一步需要文档外的知识，就把它补进文档。

## 6. 完成判据（机器可查部分）

```bash
docker build -t forebrain:dev .                                              # 成功
docker run --rm --entrypoint sh forebrain:dev -c 'bwrap --version && git --version && node --version && npm --version && python3 --version && pip3 --version && go version'   # 全部有输出
kubectl kustomize deploy/k8s/manifests > /dev/null                           # 无错
grep -rn "serve\b\|--static-dist\|OPENAI_API_KEY\|agents/default.yaml" deploy/ Dockerfile   # 无输出（旧时代残留清干净）
ls docs/deploy/{README,linux-host,docker,kubernetes}.md                      # 四个文件都在
```

## 7. 维护注意

- 今后改启动参数、默认端口、forebrain.yaml 支持项、镜像变量白名单、多副本要求时，同步改三份文档与清单。
- 镜像里的工具链版本在 Dockerfile 顶部集中声明（ARG），升级只改一处。
- `Dockerfile.ubuntu-bubblewrap` 若只是测试用途，保留并在文件头写明「测试用，不用于部署」。

## 8. STOP 条件

- 在默认 seccomp 基础上无论放行什么，容器内 bubblewrap 都起不来——不阻塞本计划（无沙箱照常运行），但要在 README 结果里报告具体错误与宿主条件，并在文档的沙箱前提一节写明。
- 某个工具链在构建阶段需要的下载源在构建环境不可达（构建环境是联网的；若不是，报告）。
- K8s 的 RWX 存储在验收环境里不支持原子重命名。
- 文档要求的某个自检位置（沙箱状态）在产品里不存在——报告，另立小计划补。

## 9. 执行记录（2026-11-04，评审 APPROVE，含用户手动验收项）

- **提交**（worktree `.worktrees/214`，分支 `exec/214`，**未合并**，8 笔）：`d9104357`（Dockerfile + 入口脚本 + .dockerignore）、`4e4ff2e8`（三个 fetch 脚本的超时与镜像）、`7c4123f7`（README 国内源完整构建命令）、`0675fe4a`（seccomp 最小配置）、`2e6e5693`（单机 compose）、`cd359d09`（K8s 清单重写）、`1fdfeaee`（三份中文部署文档 + 索引）、`f61e2e60`（文档补充）。
- **镜像**：`forebrain:dev` 1.36 GB；冷构建 377s、层缓存命中 38s；`--version` 与工具链全检（bwrap 0.8.0 / git 2.39.5 / node v22.23.4 / npm 10.9.9 / python 3.11.2 / go 1.26.9）通过。
- **seccomp（§3.2）**：以 moby/profiles default.json 为底追加 4 组放行，**逐条去掉验证**出最小集（去掉 clone/unshare/mount/pivot_root/umount2 各自在容器内失败；setns 去掉仍通过故未收录）。**关键发现**：Docker 默认遮蔽的 /proc 使新挂 proc 被 `mount_too_revealing` 拒绝，seccomp 之外 Docker 侧必须 `systempaths=unconfined`（K8s/containerd 无此需求）——已写进 `deploy/seccomp/README.md`、compose 与文档。
- **Docker 单机真机验收（§5）**：网关 healthy、登录全链路（cookie → authenticated）、「默认」预设下 `touch /data/forebrain/../outside` 正确要求审批、工作区内写文件沙箱内成功（sandbox_backend=bubblewrap）、跑批 run_completed；去掉 seccomp 参数重跑 → 照常启动、审批预设消失、跑批照常完成（215 行为）。验收后容器/卷/网络清空。
- **K8s**：`kubectl kustomize`（v1.34.1/kustomize v5.7.1）渲染无错，并对渲染结果断言 replicas=2、`FOREBRAIN_INTERNAL_ADDR=http://$(POD_IP):6060`、uid 10001、seccomp Localhost、AppArmor Unconfined、RWX、无粘滞、nginx 3600s/64m。
- **§6 机器判据**：四项全过（grep 门无旧时代残留；四个文档都在；kustomize 无错；build 已由步骤 1 验证）。

### 9.1 「20 分钟打不出镜像」的根因与修复（owner 2026-11-04 指令）

三个根因，全部修复并已提交：

1. **`objects.githubusercontent.com:443` 被网络阻断**（主因）：GitHub release 下载都重定向到该主机；fetch 脚本的 `curl --retry 3` 无任何超时，每次挂死数分钟、重试再翻倍——「慢」实为「死等」。修复：全部下载加 `--connect-timeout 15 --max-time`，并支持 `FOREBRAIN_GITHUB_MIRROR` 前缀（实测 ghfast.top 拉同资产 1.5s）。
2. **`api.github.com` 匿名限额 60/h 被 pdfium 的资产 id 查询耗尽（403）**：镜像路径改用确定性 release URL，完全不碰 API。
3. **`.dockerignore` 未排除 `third_party/{libtokenizers,pdfium}`**：本地 fetch 过的检出带入 140 MB 死重；worktree（软链）形态直接令 `mkdir` 报 File exists。已排除。

另修三个正确性缺陷：gobuild 段漏声明 `ARG NPM_REGISTRY`（跨段不继承、静默回退公网）；入口脚本 `exec "$@"` 缺二进制前缀（`--version` 报 not found）；npm 启动器按文件 COPY 被解引用断链（改目录级 COPY）。

**国内源完整构建命令已按 owner 要求写进 `README.md`**（"Building the container image" 一节：基础命令、DaoCloud/Aliyun/npmmirror/goproxy.cn/ghfast.top 全套 build-arg 的完整命令、参数对照表、`GITHUB_MIRROR` 语义与 sha256 保证、镜像验证命令）。

### 9.2 验收揪出的产品缺陷：已定位根因并彻底修复（`exec/218` @ `9a82290f`）

- **缺陷**：`pkg/safety/backend_linux.go` 的 `createLinuxMissingDenyMarker` 以 0o000 创建目录 marker；deny 清单在同一轮 prepare 里逐项处理，第一项（`.git/hooks`）先建出 0o000 的 `<root>/.git`，下一项（`.git/config`）的 `os.Lstat` 要以同一个非特权用户穿越该目录 → EACCES（不是 IsNotExist）→ prepare 整体失败 →「工作区无 .git + 网关非 root（容器固定 uid 10001、无 CAP_DAC_OVERRIDE）」时**每条沙箱 shell 直接失败**。root 与 macOS 路径不受影响，故开发机从未暴露。
- **根因**：目录的 mode bits 挡的是**创建者自己的路径穿越**；而 host 侧 mode 本就不属于沙箱边界——bwrap 在 marker 上盖 tmpfs/ro-bind，边界是挂载表。
- **修复**：目录 marker 改 0o700（文件 marker 维持 0o000——文件不挡穿越，stat 也不需要文件自身的权限位）；cleanup 的 chmod 变幂等无需改。
- **验证**（linux/arm64 gobuild 容器、uid 10001）：回归测试 `TestMissingDenyMarkerDirectoryStaysTraversableForTheNextEntry`（按缺陷唯一条件跳过 root）修复后通过；改回 0o000 即以**与生产一字不差的错误**失败（`lstat …/.git/config: permission denied`）；容器内与 darwin 两侧 `./pkg/safety` 全量 ok。CI 主任务在 ubuntu 上跑，该测试随矩阵长期生效。
