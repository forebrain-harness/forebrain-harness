# 208 — 企业内网（离线）可用：镜像变量放行、跑批读包管理器配置、离线下载降级

- **基线**：`06f1bc57`（dev）。
- **状态**：TODO
- **优先级 / 工作量 / 风险**：P1 / M / MED（改所有子进程的环境变量白名单与跑批读集合——安全边界相关）
- **依赖**：206 先落地（同改 `pkg/safety` 后端与 `pkg/tool/shell_tool.go`）。与 207 无文件交集，可在 207 前后任意执行。部署文档与 Dockerfile 的计划以本计划为前提。
- **执行隔离**：基于 dev HEAD 的 worktree 或主树 in-tree；**零 commit**（owner 手动提交）。

> **执行者须知**：逐步执行，每步跑「验证」确认结果再进下一步。碰到 §8 STOP 条件就停下报告。完成后把 `plans/README.md` 里 208 那一行的状态改为 DONE 并写一句结果。
>
> **Drift check（先跑）**：`git diff --stat 06f1bc57 -- pkg/home/envguard.go pkg/tool/shell_tool.go pkg/llm/tokenizers.go pkg/llm/assets.go pkg/memory/dictionary.go pkg/llm/openai/client_version.go`。除 203–206 的改动外有输出，就逐条对照 §2；对不上就 STOP。

## 0. 为什么做、owner 裁决（执行期不得改）

owner 2026-10-31：**forebrain gateway 必须支持部署到企业内网使用**。企业内网＝**离线**：无法连接和访问公网、也不需要公网；内网有 **npm / pip / go 的软件镜像源**。部署方式有 Linux 主机、Docker、K8s 三种（文档与镜像另立计划）。裁决：

1. **不需要公司代理**（不传 `HTTP(S)_PROXY`）；沙箱「允许联网」开＝直连内网。
2. **放行镜像相关环境变量**给子进程（沙箱命令、不进沙箱的命令、MCP 本地服务、语言服务器）——固定白名单。
3. **跑批读集合加包管理器配置文件只读**（具体文件，不是整个主目录）。
4. 「允许内网地址」（`agents.defaults.web_fetch.allow_private_ip`）**保留**：内网部署抓内部网页靠它。
5. 离线下各类自动下载必须**缓存优先、超时有界、失败降级**，不阻塞启动与对话（仓库规则：外部依赖挂死时对话必须自行恢复，工具不得无限挂起）。

## 1. 交付

- `pkg/home/envguard.go`：镜像变量白名单（§3.1）。
- 跑批边界：包管理器配置文件与证书文件只读（§3.2）。
- 自动下载：统一短超时、只在后台、失败只记一次日志并降级（§3.3）。
- 网页构建产物不引用任何外部地址的守护测试（§3.4）。
- 离线真机验收（§6）。

## 2. 现状事实（已对基线核实）

### 2.1 子进程环境变量（`pkg/home/envguard.go:14-60`）

```go
var defaultAllow = []string{
	"PATH", "HOME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "TZ", "USER", "SHELL", "TERM",
}
...
func SafeSubprocessEnv(parent []string, opts Options) []string {
	...
		if _, ok := allowSet[key]; !ok {
			continue
		}
		if !opts.AllowSecretEnv && looksSecretKey(key) {
			continue
		}
```

使用者：沙箱命令（`pkg/safety/backend_darwin.go:41`、`backend_linux.go:51`、`backend_windows.go:123`）、不进沙箱的命令（`pkg/safety/manager.go:671`）、MCP 本地服务（`pkg/mcp/stdio_secure.go:98-112`）、语言服务器（`pkg/lsp/instance.go:72`）、TUI（`pkg/tui/chat_session.go:2820`）。运维在网关环境里设的 `GOPROXY`、`PIP_INDEX_URL`、`NPM_CONFIG_REGISTRY` 今天全被删掉。

### 2.2 跑批读集合（`pkg/tool/shell_tool.go:467-479`）

```go
	if batch {
		...
		boundary := batchShellBoundary(req.taskWorkspace, st.LoadedSkillRoots(), denyRead, projectRoot, home)
		extraBinds = boundary.writable
		req.exclusiveWritablePaths = true
		allowRead = boundary.readable
		denyRead = boundary.denied
		allowNetwork = true
	}
```

`batchShellBoundary`：可写只剩任务目录；可读只剩任务目录、共享技能目录与平台运行时默认路径；项目根与 home 拒读。`ExclusiveReadablePaths: batch`（`shell_tool.go:558`）。所以 `~/.npmrc`、`pip.conf`、go 的 env 文件在跑批里读不到。

### 2.3 自动下载

| 位置 | 内容 | 超时 | 阻塞谁 |
|---|---|---|---|
| `pkg/llm/tokenizers.go:695,861-960` | 分词器（huggingface / modelscope，`HF_ENDPOINT` 可改） | 10 分钟 | 后台 goroutine |
| `pkg/llm/assets.go:164-190` | `models.json`（二进制旁没有时从 GitHub 发布页下载） | 2 分钟 | 调用方同步 |
| `pkg/memory/dictionary.go:280-292,343` | 中文 / 日文词典（缺失时从 GitHub 发布页下载，进程内只试一次） | 5 分钟 | **首次记忆搜索同步等待** |
| `pkg/llm/openai/client_version.go:27-46` | npm registry 查客户端版本 | 5 秒 | 调用方 |

离线内网里防火墙常常静默丢包：连接要等满超时才失败——词典那一行意味着一次记忆搜索可能卡 5 分钟。

### 2.4 网页资源

`frontend/index.html` 与 `frontend/src` 里没有 Google Fonts / CDN 引用（2026-10-31 核查）；网页由网关内置的 `pkg/gateway/dist` 提供。

## 3. 设计

### 3.1 镜像变量白名单（固定，写在 `envguard.go`，与 `defaultAllow` 并列为 `mirrorEnvAllow`）

- Go：`GOPROXY`、`GONOPROXY`、`GOPRIVATE`、`GONOSUMDB`、`GONOSUMCHECK`、`GOSUMDB`、`GOINSECURE`、`GOTOOLCHAIN`
- pip / uv：`PIP_INDEX_URL`、`PIP_EXTRA_INDEX_URL`、`PIP_TRUSTED_HOST`、`PIP_CERT`、`UV_INDEX_URL`、`UV_DEFAULT_INDEX`、`UV_EXTRA_INDEX_URL`、`UV_INDEX`、`UV_INSECURE_HOST`
- npm / yarn / pnpm：`NPM_CONFIG_REGISTRY`、`npm_config_registry`、`NPM_CONFIG_CAFILE`、`npm_config_cafile`、`NPM_CONFIG_STRICT_SSL`、`npm_config_strict_ssl`、`YARN_REGISTRY`、`YARN_NPM_REGISTRY_SERVER`
- 内网 HTTPS 镜像常用企业自签证书，证书路径变量一并放行：`SSL_CERT_FILE`、`SSL_CERT_DIR`、`REQUESTS_CA_BUNDLE`、`NODE_EXTRA_CA_CERTS`、`CURL_CA_BUNDLE`

规则：名字精确匹配（大小写敏感，npm 的大小写两种都列了）；照旧经过 `looksSecretKey` 过滤（上面没有一个名字会命中）；值里若带账号密码（`https://user:pass@mirror`）原样传递（owner 已同意放行）。**不放行** `HTTP_PROXY` / `HTTPS_PROXY` / `ALL_PROXY` / `NO_PROXY`。

### 3.2 跑批只读文件

`batchShellBoundary` 的可读集合追加（只追加**存在的**具体文件，不追加目录；`home` 取跑批进程看到的 `$HOME`）：

- `~/.npmrc`、`~/.yarnrc`、`~/.yarnrc.yml`、`/etc/npmrc`、`$(npm prefix -g)/etc/npmrc`（不调外部命令：只认 `NPM_CONFIG_GLOBALCONFIG` 指向的文件）
- `~/.config/pip/pip.conf`、`~/.pip/pip.conf`、`/etc/pip.conf`、`/etc/xdg/pip/pip.conf`、macOS `~/Library/Application Support/pip/pip.conf`；`PIP_CONFIG_FILE` 指向的文件
- `~/.config/uv/uv.toml`、`/etc/uv/uv.toml`
- go 的 env 文件：`GOENV` 指向的文件，否则 `os.UserConfigDir()/go/env`
- 3.1 里证书变量指向的文件（`SSL_CERT_FILE`、`REQUESTS_CA_BUNDLE`、`NODE_EXTRA_CA_CERTS`、`CURL_CA_BUNDLE`、`PIP_CERT`、`NPM_CONFIG_CAFILE`）与 `SSL_CERT_DIR` 目录

这份清单写成一个函数 `packageManagerConfigFiles(env, home) []string`，单测覆盖。**home 的其它内容照旧拒读**。

### 3.3 离线下载降级

- 统一：每个下载用 `http.Client` + `net.Dialer{Timeout: 5 * time.Second}`（连接超时短），整体超时：`models.json` 30 秒、词典 / 分词器按文件大小保留较长整体超时但**只在后台跑**。
- 词典：首次记忆搜索**不再同步等下载**——缺词典时本次搜索退回不分词的检索（说明「词典缺失，中文检索精度下降」只记一次日志），后台去下载；下载失败只记一次。若退回检索需要改动 FTS 索引结构，STOP 报告。
- `models.json`：二进制旁没有且下载失败 → 记一次日志，模型目录为空时窗口按「上下文窗口」配置兜底（204 已统一），不阻塞启动。
- 分词器：照旧后台；失败退回启发式估算（今天就有），只记一次日志。
- npm registry 版本查询：5 秒已有界，保持。
- 所有这些「只记一次」用 `sync.Once` 或现有的 once 机制。

### 3.4 网页不引用外部地址

在 `pkg/architecture`（或 `pkg/gateway` 的同名测试文件）加一个测试：扫 `pkg/gateway/dist` 下的 `.html` / `.css` / `.js`，`<script src>`、`<link href>`、CSS `url(` / `@import` 里出现 `http://` 或 `https://` 且主机不是相对路径即失败（允许正文字符串里的文档链接——只看加载资源的位置）。

## 4. 步骤

每步结束都跑：`CGO_ENABLED=1 go build -tags fts5 ./... && go vet -tags fts5 ./pkg/...`（期望：无输出）。

1. 3.1：加 `mirrorEnvAllow`，`SafeSubprocessEnv` 合并它。
2. 3.2：`packageManagerConfigFiles` + 接进 `batchShellBoundary`。
3. 3.3：三个下载点改造。
4. 3.4：守护测试。
5. 包图：`scripts/package-graph.sh`。

## 5. 测试（放在被测文件同名的 `_test.go` 里）

- `pkg/home/envguard_test.go`：父环境里有 `GOPROXY=http://goproxy.corp`、`PIP_INDEX_URL=…`、`npm_config_registry=…`、`SSL_CERT_FILE=…`、`HTTPS_PROXY=…`、`GITHUB_TOKEN=…` → 子进程环境里前四个在、后两个不在。
- `pkg/tool/shell_tool_test.go`：跑批边界的可读集合包含存在的 `~/.npmrc` 与 `pip.conf`（用临时 HOME 造文件），不包含不存在的、不包含 home 目录本身；`~/.ssh` 仍被拒读。
- macOS 真机（`pkg/safety` 已有 seatbelt 真机测试的写法照抄）：跑批命令 `cat ~/.npmrc` 成功、`ls ~` 失败。
- `pkg/memory/dictionary_test.go`：词典缺失且下载地址不可达（指向一个只接受连接不应答的本地监听）→ 搜索在 6 秒内返回结果。
- `pkg/llm/assets_test.go`：`models.json` 缺失且下载不可达 → 30 秒内返回错误、启动路径不 panic。
- 网页守护测试本身要有一个红例（往临时 dist 里放一个外链 `<script>`，断言失败）。

## 6. 完成判据与验收

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/home/... ./pkg/tool/... ./pkg/safety/... ./pkg/memory/... ./pkg/llm/... ./pkg/architecture/... -count=1 -p 2   # 全 ok
go vet -tags fts5 ./...                                                   # 无输出
```

**离线真机验收**（Linux + Docker；没有对应环境的项：把步骤与预期结果写成「用户手动验收清单」，附在本计划末尾并抄进 README 的结果栏，**由用户手动验收**；其余完成判据全部通过即可标 DONE（注明「含用户手动验收项」））：

1. `docker network create --internal fb-offline`（该网络不通外网）。
2. 在该网络里起三个镜像源：npm（verdaccio）、pip（pypiserver 或 devpi）、go（athens），预先灌入一个测试包。
3. 在该网络里起网关（用当前 Dockerfile 构建的镜像），环境变量设 `GOPROXY`、`PIP_INDEX_URL`、`NPM_CONFIG_REGISTRY` 指向这三个源。
4. 在一个普通会话里让模型执行 `npm install <测试包>`、`pip install <测试包>`、`go mod download <测试模块>` → 都成功。
5. 在一个跑批任务里重复第 4 步 → 都成功。
6. 网关启动到可用的时间与联网环境相比不超过 +10 秒；一次 `memories_search` 在 6 秒内返回。
7. 从另一个容器用浏览器（或 curl）打开网页：页面完整加载，开发者工具里没有对外请求。

## 7. 维护注意

- 以后新增任何外网依赖（下载、版本检查、遥测）都要回答「离线内网里怎么办」，并补进 §2.3 那张表的同类处理。
- 白名单只放「镜像 / 证书」类变量；不要借这个口子放代理变量或任何凭据变量。
- 部署文档要写清楚：镜像变量在哪里设（主机：systemd 单元的 `Environment=`；Docker：`-e`；K8s：Deployment 的 `env`），以及「允许内网地址」的用途。

## 8. STOP 条件

- 加白名单后某个现有安全测试（例如「子进程看不到网关的环境变量」一类）失败。
- 词典缺失时的退回检索需要改 FTS 表结构。
- 跑批的读集合在 Linux（bubblewrap）上不能按文件粒度放行（只能按目录挂载）——报告平台差异，不要改成放行整个目录。

**被取代（owner 2026-10-31）**：§3.2「跑批读集合加包管理器配置文件」被 `plans/216-batch-project-semantics.md` §3.1 取代——跑批改为「FOREBRAIN_HOME 与项目目录按拒绝名单处理、其余整个文件系统只读」，这些配置文件天然可读。若 208 先于 216 执行，§3.2 可以跳过；已实现的由 216 删除。§3.1（镜像变量白名单）、§3.3、§3.4 照做。
