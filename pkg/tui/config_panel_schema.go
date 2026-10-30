// The /config panel's section table (plan 190 §3.1): which configuration
// sections the panel lists, how each one reads in Chinese, and which widget
// each field gets. Pure data — no rendering, no session, no imports — so the
// coverage test can hold it against config.Root and against the store's
// section list without a terminal or a database.
//
// This table is the TUI side's one "add a new section, remember to change
// here" list: TestConfigPanelSchemaCoversEveryRootSection fails the moment a
// Root section has no descriptor, which is what keeps a new section from
// reaching the panel's users as nothing at all.
package tui

import "strings"

// configFieldKind is the widget a field is edited with.
type configFieldKind int

const (
	// configFieldBool3 is an optional boolean, three states: 开, 关, 默认.
	// The third state is not decoration — nil is what selects the setting's
	// default (see config.BoolPtr), so a two-state switch would silently
	// turn "unset" into an explicit choice.
	configFieldBool3 configFieldKind = iota
	// configFieldEnum is one of a fixed set the validator accepts; the
	// picker shows the section's own option list, so no free text can reach
	// the store with a value the loader rejects.
	configFieldEnum
	// configFieldNumber is an integer with the bounds the loader applies.
	configFieldNumber
	// configFieldText is free text: a model name, a path, a command.
	configFieldText
	// configFieldRows is a table or list — a map of profiles, of MCP
	// servers, of hook events — edited as rows: open one to edit its own
	// fields, add a row, delete a row.
	configFieldRows
)

// configField describes one editable field of one section. Path is the
// field's place inside the section's own JSON (dot-separated); the empty path
// means the section's value itself, which is how a section that is a bare
// string — sandbox_mode, approvals_reviewer — carries its one setting.
type configField struct {
	Path    string
	Label   string
	Kind    configFieldKind
	Options []string // configFieldEnum: the values the loader accepts
	Min     int      // configFieldNumber: the smallest accepted value
	Max     int      // configFieldNumber: the largest accepted value, 0 = none
	// Clamp makes an over-Max value land on Max instead of being refused,
	// the way the loader treats a limit it can simply honour: the panel
	// shows the clamped value it stored, never a number that did not land.
	Clamp bool
	// List marks a configFieldRows field whose rows are list elements rather
	// than a map's keys — writable_roots is a list, permissions a map. An
	// unset field has no value to read the shape from, so the descriptor
	// carries it.
	List bool
	// Hint is the one line the row carries under its value: what the field
	// means, and what leaving it unset does.
	Hint string
}

// configSectionSchema is one section as the panel presents it.
type configSectionSchema struct {
	// Name is the section name a reader sees in forebrain.yaml and in
	// fb_config.
	Name string
	// Title is the name in the picker, in Chinese.
	Title string
	// Summary is the one-line "what this is for" the picker shows beside it.
	Summary string
	// Tab groups the sections the picker lists, in the tabs' own order.
	Tab string
	// YAMLOnly marks a section the panel shows but never writes: it is a
	// startup section of forebrain.yaml, and it takes effect when the
	// process restarts (plan 187 §1.1).
	YAMLOnly bool
	// Fields are the editable settings, in the order the section page shows
	// them. A YAML-only section has none.
	Fields []configField
}

// The picker's tabs. A terminal is wide enough for four short headings, and
// four is what keeps the 16 stored sections reachable without a filter.
const (
	configTabApproval = "审批与沙箱"
	configTabSession  = "会话与记忆"
	configTabAgents   = "代理与工具"
	configTabFile     = "仅配置文件"
)

// configSectionSchemas lists every section of the runtime configuration: the
// 16 the state database stores, then the two forebrain.yaml keeps. The order
// inside a tab is the order a user tunes these settings in, roughly: the
// switch that decides everything else first.
var configSectionSchemas = []configSectionSchema{
	{
		Name: "approval_policy", Title: "审批策略", Tab: configTabApproval,
		Summary: "什么情况下向人请求批准",
		Fields: []configField{{
			Label: "模式", Kind: configFieldEnum,
			Options: []string{"untrusted", "on-request", "never"},
			Hint:    "默认按项目信任度推导；granular 是子对象，选中后按行编辑",
		}},
	},
	{
		Name: "approvals_reviewer", Title: "审批复核人", Tab: configTabApproval,
		Summary: "批准请求由谁复核",
		Fields: []configField{{
			Label: "复核人", Kind: configFieldEnum,
			Options: []string{"user", "auto_review"},
			Hint:    "user 由你亲自批准；auto_review 交给模型复核",
		}},
	},
	{
		Name: "auto_review", Title: "自动复核", Tab: configTabApproval,
		Summary: "自动复核的策略文本",
		Fields: []configField{{
			Path: "policy", Label: "策略", Kind: configFieldText,
			Hint: "原样交给复核模型；留空用内置策略",
		}},
	},
	{
		Name: "sandbox_mode", Title: "沙箱模式", Tab: configTabApproval,
		Summary: "命令在什么沙箱里运行",
		Fields: []configField{{
			Label: "模式", Kind: configFieldEnum,
			Options: []string{"read-only", "workspace-write", "danger-full-access"},
			Hint:    "默认跟随 default_permissions",
		}},
	},
	{
		Name: "sandbox_workspace_write", Title: "沙箱可写工作区", Tab: configTabApproval,
		Summary: "workspace-write 沙箱的写权限与缓存",
		Fields: []configField{
			{Path: "writable_roots", Label: "额外可写根目录", Kind: configFieldRows, List: true, Hint: "每行一个绝对路径"},
			{Path: "network_access", Label: "允许联网", Kind: configFieldBool3, Hint: "默认开"},
			{Path: "exclude_tmpdir_env_var", Label: "排除 TMPDIR 环境变量", Kind: configFieldBool3, Hint: "默认关"},
			{Path: "exclude_slash_tmp", Label: "排除 /tmp", Kind: configFieldBool3, Hint: "默认关"},
			{
				Path: "go_cache_mode", Label: "Go 缓存模式", Kind: configFieldEnum,
				Options: []string{"isolated", "shared", "disabled"},
				Hint:    "默认 isolated；shared 让沙箱命令复用宿主 Go 缓存",
			},
		},
	},
	{
		Name: "permissions", Title: "权限档案", Tab: configTabApproval,
		Summary: "具名权限档案（profiles）",
		Fields: []configField{{
			Label: "权限档案", Kind: configFieldRows,
			Hint: "每行一个档案名；行内是 description、extends、filesystem、network",
		}},
	},
	{
		Name: "default_permissions", Title: "默认权限档案", Tab: configTabApproval,
		Summary: "默认使用哪个权限档案",
		Fields: []configField{{
			Label: "档案名", Kind: configFieldText,
			Hint: ":read-only / :workspace / :danger-full-access，或 permissions 段里的档案名",
		}},
	},
	{
		Name: "compact", Title: "上下文压缩", Tab: configTabSession,
		Summary: "自动压缩的阈值与提示词",
		Fields: []configField{
			{Path: "prompt", Label: "交接提示词", Kind: configFieldText, Hint: "留空用内置提示词"},
			{
				Path: "model_auto_compact_token_limit", Label: "自动压缩阈值(令牌)", Kind: configFieldNumber,
				Min: 0, Hint: "0 表示按模型上下文窗口决定；上限为窗口的 90%",
			},
			{
				Path: "model_auto_compact_token_limit_scope", Label: "阈值口径", Kind: configFieldEnum,
				Options: []string{"total", "body_after_prefix"},
				Hint:    "默认 total",
			},
			{Path: "remote_compaction", Label: "远程压缩", Kind: configFieldBool3, Hint: "默认关；开启后只能由同一家 provider 回放"},
			{Path: "remote_compaction_v2", Label: "远程压缩 v2", Kind: configFieldBool3, Hint: "默认开"},
		},
	},
	{
		Name: "memories", Title: "记忆", Tab: configTabSession,
		Summary: "记忆的开关、预算与模型",
		Fields: []configField{
			{Path: "use_memories", Label: "使用记忆", Kind: configFieldBool3, Hint: "默认开"},
			{Path: "generate_memories", Label: "生成记忆", Kind: configFieldBool3, Hint: "默认开"},
			{Path: "dedicated_tools", Label: "专用工具", Kind: configFieldBool3, Hint: "默认开"},
			{Path: "disable_on_external_context", Label: "外部上下文时停用", Kind: configFieldBool3, Hint: "默认关"},
			{Path: "search_top_k", Label: "检索返回条数", Kind: configFieldNumber, Min: 1, Max: 20, Clamp: true, Hint: "默认 5；超过 20 按 20 存"},
			{Path: "max_raw_memories_for_consolidation", Label: "整合的原始记忆上限", Kind: configFieldNumber, Min: 1, Max: 4096, Clamp: true, Hint: "默认 256"},
			{Path: "max_unused_days", Label: "未使用过期天数", Kind: configFieldNumber, Min: 0, Max: 365, Clamp: true, Hint: "默认 30"},
			{Path: "max_rollout_age_days", Label: "记录保留天数", Kind: configFieldNumber, Min: 0, Max: 90, Clamp: true, Hint: "默认 10"},
			{Path: "max_rollouts_per_startup", Label: "每次启动处理的记录数", Kind: configFieldNumber, Min: 1, Max: 128, Clamp: true, Hint: "默认 2"},
			{Path: "min_rollout_idle_hours", Label: "记录空闲小时数", Kind: configFieldNumber, Min: 1, Max: 48, Clamp: true, Hint: "默认 6"},
			{Path: "min_rate_limit_remaining_percent", Label: "限流余量百分比", Kind: configFieldNumber, Min: 0, Max: 100, Clamp: true, Hint: "默认 25"},
			{Path: "extract_model", Label: "抽取模型", Kind: configFieldText, Hint: "留空用当前模型"},
			{Path: "consolidation_model", Label: "整合模型", Kind: configFieldText, Hint: "留空用当前模型"},
		},
	},
	{
		Name: "cron", Title: "定时任务", Tab: configTabSession,
		Summary: "定时任务的保留期",
		Fields: []configField{{
			Path: "retention_days", Label: "保留天数", Kind: configFieldNumber,
			Min: 1, Max: 3650, Hint: "默认 30；范围 1–3650",
		}},
	},
	{
		Name: "features", Title: "功能开关", Tab: configTabSession,
		Summary: "特性开关与网络代理",
		Fields: []configField{
			{Path: "memories", Label: "记忆功能", Kind: configFieldBool3, Hint: "默认开"},
			{Path: "skill_offer", Label: "技能推荐", Kind: configFieldBool3, Hint: "默认开"},
			{Path: "lsp", Label: "语言服务器", Kind: configFieldBool3, Hint: "默认开"},
			{Path: "exec_permission_approvals", Label: "命令执行审批", Kind: configFieldBool3, Hint: "默认关"},
			{Path: "request_permissions_tool", Label: "权限申请工具", Kind: configFieldBool3, Hint: "默认关"},
			{Path: "network_proxy.enabled", Label: "网络代理", Kind: configFieldBool3, Hint: "默认关"},
			{
				Path: "network_proxy.mode", Label: "网络代理模式", Kind: configFieldEnum,
				Options: []string{"limited", "full"}, Hint: "留空不限",
			},
			{Path: "network_proxy.proxy_url", Label: "代理地址", Kind: configFieldText, Hint: "例如 http://127.0.0.1:3128"},
			{Path: "network_proxy.socks_url", Label: "SOCKS 地址", Kind: configFieldText, Hint: "例如 http://127.0.0.1:8081"},
			{Path: "network_proxy.allow_upstream_proxy", Label: "允许上游代理", Kind: configFieldBool3, Hint: "默认关"},
			{Path: "network_proxy.allow_local_binding", Label: "允许本地监听", Kind: configFieldBool3, Hint: "默认关"},
			{Path: "network_proxy.dangerously_allow_non_loopback_proxy", Label: "允许非回环代理", Kind: configFieldBool3, Hint: "默认关；危险选项"},
			{Path: "network_proxy.dangerously_allow_all_unix_sockets", Label: "允许所有 Unix socket", Kind: configFieldBool3, Hint: "默认关；危险选项"},
			{Path: "network_proxy.domains", Label: "域名规则", Kind: configFieldRows, Hint: "每行一个域名模式，行内填 allow 或 deny"},
			{Path: "network_proxy.unix_sockets", Label: "Unix socket 规则", Kind: configFieldRows, Hint: "每行一个绝对路径，行内填 allow 或 deny"},
		},
	},
	{
		Name: "windows", Title: "Windows 沙箱", Tab: configTabSession,
		Summary: "Windows 上的沙箱形态",
		Fields: []configField{
			{
				Path: "sandbox", Label: "沙箱形态", Kind: configFieldEnum,
				Options: []string{"unelevated", "elevated"}, Hint: "留空按平台决定",
			},
			{Path: "sandbox_private_desktop", Label: "独立桌面", Kind: configFieldBool3, Hint: "默认开"},
		},
	},
	{
		Name: "agents", Title: "代理", Tab: configTabAgents,
		Summary: "默认参数、代理定义与其 provider、子代理、MCP",
		Fields: []configField{
			{
				Path: "definitions", Label: "代理定义", Kind: configFieldRows,
				Hint: "每行一个代理 id；行内是 provider、子代理、MCP 与其默认模型",
			},
			{
				Path: "defaults.network_retry_max", Label: "网络重试上限", Kind: configFieldNumber,
				Min: 0, Max: 15, Clamp: true, Hint: "默认 10；0 关闭重试，超过 15 按 15 跑",
			},
			{
				Path: "defaults.stream_idle_timeout_seconds", Label: "流空闲超时(秒)", Kind: configFieldNumber,
				Min: 0, Max: 1800, Clamp: true, Hint: "默认 300；0 关闭看门狗",
			},
			{Path: "defaults.enable_subagent", Label: "启用子代理", Kind: configFieldBool3, Hint: "默认按内置设置"},
			{Path: "defaults.mcp_servers", Label: "共享 MCP 服务器", Kind: configFieldRows, Hint: "每行一个服务器"},
		},
	},
	{
		Name: "tools", Title: "工具", Tab: configTabAgents,
		Summary: "内置工具的行为",
		Fields: []configField{{
			Path: "command_rewrite.enabled", Label: "命令改写", Kind: configFieldBool3,
			Hint: "默认开；在审批前插入仅影响呈现的参数",
		}},
	},
	{
		Name: "hooks", Title: "钩子", Tab: configTabAgents,
		Summary: "事件钩子（命令 / 提示）",
		Fields: []configField{{
			Label: "钩子事件", Kind: configFieldRows,
			Hint: "每行一个事件名（如 pre_tool_use），行内是 matcher 列表",
		}},
	},
	{
		Name: "lsp", Title: "语言服务器", Tab: configTabAgents,
		Summary: "语言服务器的开关、限额与诊断",
		Fields: []configField{
			{Path: "recommendations", Label: "推荐", Kind: configFieldBool3, Hint: "默认开"},
			{Path: "servers", Label: "服务器", Kind: configFieldRows, Hint: "每行一个服务器 id；行内是 command、args、env 等"},
			{Path: "max_servers", Label: "并发服务器上限", Kind: configFieldNumber, Min: 0, Max: 32, Hint: "默认 6；范围 1–32"},
			{Path: "idle_timeout", Label: "空闲退出(秒)", Kind: configFieldNumber, Min: 0, Max: 86400, Hint: "默认 600；非 0 时范围 60–86400"},
			{Path: "request_timeout", Label: "请求超时(秒)", Kind: configFieldNumber, Min: 0, Max: 600, Hint: "默认 30；范围 1–600"},
			{Path: "diagnostics.after_edit", Label: "编辑后诊断", Kind: configFieldBool3, Hint: "默认开"},
			{Path: "diagnostics.wait_ms", Label: "诊断等待(毫秒)", Kind: configFieldNumber, Min: 0, Max: 30000, Hint: "默认 2500；0 表示不等待"},
			{
				Path: "diagnostics.min_severity", Label: "最低严重度", Kind: configFieldEnum,
				Options: []string{"error", "warning", "information", "hint"}, Hint: "默认 warning",
			},
			{Path: "diagnostics.max_per_file", Label: "每文件上限", Kind: configFieldNumber, Min: 0, Max: 200, Hint: "默认 20；范围 1–200"},
			{Path: "diagnostics.max_files", Label: "文件数上限", Kind: configFieldNumber, Min: 0, Max: 50, Hint: "默认 10；范围 1–50"},
			{Path: "diagnostics.late_delivery", Label: "迟到诊断", Kind: configFieldBool3, Hint: "默认开"},
		},
	},
	{
		Name: "gateway", Title: "网关", Tab: configTabFile,
		Summary:  "监听地址、鉴权与对外链接（只读）",
		YAMLOnly: true,
	},
	{
		Name: "credentials", Title: "凭据文件", Tab: configTabFile,
		Summary:  "外部凭据文件的位置（只读）",
		YAMLOnly: true,
	},
}

// configSectionSchemaFor returns one section's descriptor.
func configSectionSchemaFor(name string) (configSectionSchema, bool) {
	name = strings.TrimSpace(name)
	for _, schema := range configSectionSchemas {
		if schema.Name == name {
			return schema, true
		}
	}
	return configSectionSchema{}, false
}
