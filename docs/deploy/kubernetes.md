# Kubernetes 部署

在 Kubernetes 上运行网关多副本：两副本共享一个 RWX 卷和一外部 MySQL/PostgreSQL，
前端由 nginx Ingress 暴露。清单在仓库 `deploy/k8s/manifests/`（kustomize 组织），
`kubectl kustomize deploy/k8s/manifests` 渲染即所见。

- [Linux 主机部署](linux-host.md) / [Docker 单机部署](docker.md) —— 单机形态。

## 适用场景与前提

- Kubernetes ≥ 1.30（`appArmorProfile` 字段；更老版本用注解，见「沙箱」）。
- 一个 **ReadWriteMany** 存储（NFS、CephFS 等）。这是多副本的硬前提；默认存储类
  通常是 RWO，第二个副本会因卷锁定而无法调度。
- 一个**外部** MySQL 或 PostgreSQL——数据库不在本清单里部署（理由：企业内网里
  数据库几乎总有现成的实例与备份策略）。
- nginx Ingress Controller，及一个指向它的内部域名。
- 每节点 ≥ 4GB 可用内存（资源示例按代理在 Pod 里跑构建与测试给的）。

## 获取安装物（离线）

镜像在有网机器构建（完整镜像加速命令见仓库 README「Building the container
image」），然后进内网——私有 registry 或逐节点导入：

```bash
# 私有 registry：推上去后改 deployment.yaml 里的 image:
docker tag forebrain:dev registry.internal/forebrain:v<版本> && docker push registry.internal/forebrain:v<版本>

# 或逐节点导入（在每个节点上）：
docker save forebrain:dev | gzip > forebrain-dev.tar.gz
# 节点上（kubernetes 用 containerd）：
gunzip -c forebrain-dev.tar.gz | ctr -n k8s.io images import -
```

清单本身随仓库分发；`kubectl kustomize deploy/k8s/manifests > bundle.yaml` 可以
先渲染检查。

## 数据库

只建库建账号，建表由网关自动完成：

```sql
-- MySQL 8.x
CREATE DATABASE forebrain CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'forebrain'@'%' IDENTIFIED BY '<强口令>';
GRANT ALL PRIVILEGES ON forebrain.* TO 'forebrain'@'%';

-- PostgreSQL 14+
CREATE USER forebrain PASSWORD '<强口令>';
CREATE DATABASE forebrain OWNER forebrain;
```

连接串写进 Secret（下一节）：`mysql://forebrain:<口令>@mysql.internal:3306/forebrain`
或 `postgres://…?sslmode=disable`。

## forebrain.yaml 配置与环境变量

配置在 `deploy/k8s/manifests/secret.yaml`：`forebrain.yaml`（首启内容）加 `env`
（`.env` 内容，`${…}` 引用的密钥都在这里）。**apply 之前把所有占位值换成真值**：
`FOREBRAIN_GATEWAY_TOKEN`、`FOREBRAIN_LLM_API_KEY`、`database.url`、
provider/model/base_url。

Deployment 的 init 容器只在共享卷里**没有** `forebrain.yaml` 时把它们拷进去
（两个副本同时首启不会各写一份 token——全新共享 home 上 token 是后写覆盖）；
之后文件归网页配置编辑器管，改 Secret 不会重新播种，要重来就清掉 PVC 内容。

内网镜像源与企业证书变量加在 Deployment 的 `env`（示例已注释在清单里）：
`GOPROXY`、`PIP_INDEX_URL`、`NPM_CONFIG_REGISTRY`、`SSL_CERT_FILE` 等，白名单见
源码 `pkg/home/envguard.go`；证书文件挂进共享卷（如 `/data/forebrain/certs/`）。

## 沙箱（可选，推荐）

与 Docker 形态同源但形式不同：Pod 通过 `securityContext.seccompProfile.type:
Localhost` 挂仓库提供的最小放行配置，加 `appArmorProfile.type: Unconfined`
（Ubuntu 节点）。**Localhost 配置必须先放到每个节点上**，否则 Pod 卡在
ContainerCreating：

```bash
# 每个会调度 forebrain 的节点（或用 DaemonSet 分发）：
sudo mkdir -p /var/lib/kubelet/seccomp/profiles
sudo cp forebrain-bubblewrap.json /var/lib/kubelet/seccomp/profiles/forebrain-bubblewrap.json
# 文件来自仓库 deploy/seccomp/forebrain-bubblewrap.json
```

- 节点内核仍需允许非特权用户命名空间（`kernel.unprivileged_userns_clone=1`、
  `user.max_user_namespaces>0`、Ubuntu 23.10+ 的
  `kernel.apparmor_restrict_unprivileged_userns=0`）。
- K8s 的 containerd 运行时不像 Docker 那样遮蔽 /proc，无需 `systempaths` 对应项。
- 1.30 以下把 `appArmorProfile` 换成注解
  `container.apparmor.security.beta.kubernetes.io/gateway: unconfined`。
- 明确要**无沙箱**运行：`seccompProfile.type: RuntimeDefault`、去掉 `appArmorProfile`
  ——网关照常运行，所有命令完全访问、界面没有审批预设。
- 逐条放行的验证记录：[`deploy/seccomp/README.md`](../../deploy/seccomp/README.md)。

## 启动、停止、自启

```bash
# 先改 secret.yaml 的占位值，再：
kubectl apply -k deploy/k8s/manifests
kubectl -n forebrain get pods,svc,pvc,ingress
kubectl -n forebrain rollout status deployment/forebrain-gateway
kubectl -n forebrain logs deploy/forebrain-gateway --follow
```

浏览器打开 Ingress 的域名（或端口转发 `kubectl -n forebrain port-forward
svc/forebrain 6060` 后访问 `http://127.0.0.1:6060`），用 `FOREBRAIN_GATEWAY_TOKEN`
登录。Deployment 的 `replicas`/自愈/滚动更新即自启机制，无需额外配置。

## 反向代理与 HTTPS

Ingress 清单（nginx）已带关键注解：`proxy-read-timeout`/`proxy-send-timeout`
3600（WebSocket 会话与挂起的审批）、`proxy-body-size` 64m。HTTPS 给 Ingress 加
`tls`（证书来自企业 CA）：

```yaml
spec:
  tls:
    - hosts: [forebrain.example.internal]
      secretName: forebrain-tls     # kubectl create secret tls forebrain-tls --cert=… --key=…
```

内部 DNS 把 `forebrain.example.internal` 指到 Ingress Controller 的 Service。

## 多副本

清单按 2 副本写好，机制（213）都在字段里：

- 每个副本把**自己的 Pod IP**（downward API）注册为 `FOREBRAIN_INTERNAL_ADDR`——
  字面 IP、与浏览器同一个 6060 监听器（内部端点是同一监听器上的路径，没有第二个
  端口）；副本间鉴权=共享 token **加** 源 IP 必须落在已注册副本的 internal_addr
  里（不读 X-Forwarded-For）。
- 浏览器的 WebSocket 由持有该会话的副本处理，跨副本转发；长连接的频道账号通过
  `fb_leases` 每副本恰好一个。
- Service 是 ClusterIP 且**无会话粘滞**：副本可互换，粘滞反而破坏转发。
- 扩缩容：`kubectl -n forebrain scale deployment/forebrain-gateway --replicas=3`；
  共享存储与数据库是前提，SQLite 形态不存在多副本。

## 离线内网要点

- 镜像离线导入见「获取安装物」；模型 `base_url` 指内网端点。
- 镜像源/证书变量与证书挂载见「forebrain.yaml 配置与环境变量」。
- 代理在 Pod 里执行 npm/pip/go，拉包走配置的内网源；`nodes`/`pip` 的缓存目录在
  容器层，Pod 重建即清空，属正常现象。

## 升级与备份

- 升级：新镜像导入/推送后改清单 `image:`，`kubectl apply -k …` 触发滚动更新
  （`maxUnavailable: 0`，升级期间始终有副本在线）；状态库随新版自动迁移。
- 备份：共享卷用快照或在低峰 `kubectl cp`/调试 Pod 打 tar；数据库 `mysqldump`/
  `pg_dump`。Secret 与 Ingress 证书一并归档。
- 回滚：`kubectl -n forebrain rollout undo deployment/forebrain-gateway`。

## 排障表

| 现象 | 原因与处理 |
| --- | --- |
| Pod 卡 `ContainerCreating`，事件报 seccomp profile 不存在 | 节点上没有 `/var/lib/kubelet/seccomp/profiles/forebrain-bubblewrap.json`：按「沙箱」分发；不想要沙箱就改 `RuntimeDefault`。 |
| init 容器 `seed-home` 失败 | 看 `kubectl logs <pod> -c seed-home`；通常是 Secret key 缺失或卷未绑好。 |
| 起动报 `startup config incomplete` | Secret 里 LLM 占位值没换真值，或旧卷里已有残缺 `forebrain.yaml`（清掉该文件让它重播种）。 |
| 第二个副本 Pending | PVC 不是 RWX（默认存储类是 RWO）：换支持 RWX 的 `storageClassName`。 |
| 网页登录 401 | token 与 Secret `env` 里的 `FOREBRAIN_GATEWAY_TOKEN` 不一致（首播之后改 Secret 不生效，需同时改卷里 `.env`）。 |
| 副本互相注册不上 / 转发失败 | `FOREBRAIN_INTERNAL_ADDR` 必须是 Pod IP（清单已用 downward API）；别改成 Service 名。源 IP 鉴权不读 X-Forwarded-For。 |
| 文件写入报错 / rename 失败 | NFS 服务端或挂载选项破坏原子 rename（配置与状态文件整写后改名）：修 NFS（`nfsvers=4`、不锁死写入语义）。 |
| Ingress 上一小时后断线 | `proxy-read-timeout` 注解被移除；必须保持 3600。 |
