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
// string — approval_preset — carries its one setting.
type configField struct {
	Path    string
	Label   string
	Kind    configFieldKind
	Options []string // configFieldEnum: the values the loader accepts
	// OptionLabels names an enum option in Chinese on the picker's rows. The
	// stored value stays the one the loader accepts; only the wording changes,
	// so a reader of this panel chooses a behavior rather than an english
	// configuration key (plan 203 §0.8).
	OptionLabels map[string]string
	// DefaultValue is the option to preselect when the field has no value yet,
	// for a setting whose empty value means one of its own options.
	DefaultValue string
	Min          int // configFieldNumber: the smallest accepted value
	Max          int // configFieldNumber: the largest accepted value, 0 = none
	// Clamp makes an over-Max value land on Max instead of being refused,
	// the way the loader treats a limit it can simply honour: the panel
	// shows the clamped value it stored, never a number that did not land.
	Clamp bool
	// List marks a configFieldRows field whose rows are list elements rather
	// than a map's keys — writable_roots is a list, hooks a map. An unset
	// field has no value to read the shape from, so the descriptor carries
	// it.
	List bool
	// ClaudeCodeOnly hides the field unless this machine has Claude Code
	// installed: the interop switch has nothing to act on without it (owner
	// 2026-10-30). A hidden field's stored value stays as it is; the panel
	// writes it back untouched when it saves the section.
	ClaudeCodeOnly bool
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
// four is what keeps the 11 stored sections reachable without a filter.
const (
	configTabApproval = "审批与沙箱"
	configTabSession  = "会话与记忆"
	configTabAgents   = "代理与工具"
	configTabFile     = "仅配置文件"
)

// configSectionSchemas lists every section of the runtime configuration: the
// 11 the state database stores, then the two forebrain.yaml keeps. The order
// inside a tab is the order a user tunes these settings in, roughly: the
// switch that decides everything else first.
var configSectionSchemas = []configSectionSchema{
	{
		Name: "approval_preset", Title: "审批预设", Tab: configTabApproval,
		Summary: "新会话默认的审批模式",
		Fields: []configField{{
			Label: "审批预设", Kind: configFieldEnum,
			Options:      []string{"workspace_write", "full_access"},
			OptionLabels: map[string]string{"workspace_write": "默认", "full_access": "完全访问"},
			DefaultValue: "workspace_write",
			Hint:         "默认：工作区内的读写照常，超出工作区才问；完全访问：除明确拒绝外都不问。改这里只影响新会话。",
		}},
	},
	{
		Name: "sandbox_workspace_write", Title: "沙箱可写工作区", Tab: configTabApproval,
		Summary: "workspace-write 沙箱的写权限与缓存",
		Fields: []configField{
			{Path: "writable_roots", Label: "额外可写根目录", Kind: configFieldRows, List: true, Hint: "每行一个绝对路径"},
			{Path: "network_access", Label: "允许联网", Kind: configFieldBool3, Hint: "默认开"},
		},
	},
	{
		Name: "compact", Title: "上下文压缩", Tab: configTabSession,
		Summary: "自动压缩的阈值与提示词",
		Fields: []configField{
			{Path: "prompt", Label: "交接提示词", Kind: configFieldText, Hint: "留空用内置提示词"},
			{
				Path: "auto_compact_percent", Label: "自动压缩阈值(%)", Kind: configFieldNumber,
				Min: 50, Max: 90, Clamp: true,
				Hint: "默认 90；窗口用到这个百分比时自动压缩，范围 50–90，超过按 90 存",
			},
		},
	},
	{
		Name: "batch", Title: "跑批项目", Tab: configTabApproval,
		Summary: "新跑批项目默认启用的沙箱",
		Fields: []configField{{
			Path: "sandbox_default", Label: "启用沙箱", Kind: configFieldBool3,
			Hint: "默认开；新建跑批项目沿用这里的值，之后各项目自己管",
		}},
	},
	{
		Name: "memories", Title: "记忆", Tab: configTabSession,
		Summary: "记忆的预算与模型；开关在「功能开关」的「记忆功能」",
		Fields: []configField{
			{Path: "search_top_k", Label: "检索返回条数", Kind: configFieldNumber, Min: 1, Max: 20, Clamp: true, Hint: "默认 5；超过 20 按 20 存"},
			{Path: "max_raw_memories_for_consolidation", Label: "整合的原始记忆上限", Kind: configFieldNumber, Min: 1, Max: 4096, Clamp: true, Hint: "默认 256"},
			{Path: "max_unused_days", Label: "未使用过期天数", Kind: configFieldNumber, Min: 0, Max: 365, Clamp: true, Hint: "默认 30"},
			{Path: "max_rollout_age_days", Label: "记录保留天数", Kind: configFieldNumber, Min: 0, Max: 90, Clamp: true, Hint: "默认 10"},
			{Path: "max_rollouts_per_startup", Label: "每次启动处理的记录数", Kind: configFieldNumber, Min: 1, Max: 128, Clamp: true, Hint: "默认 2"},
			{Path: "min_rollout_idle_hours", Label: "记录空闲小时数", Kind: configFieldNumber, Min: 1, Max: 48, Clamp: true, Hint: "默认 6"},
			// The stored value is a model name or the explicit "current" (the
			// conversation's own model, plan 207 §3.3): the database always
			// holds one, so the row shows what is in force instead of a
			// "leave it empty" placeholder.
			{Path: "extract_model", Label: "抽取模型", Kind: configFieldText, Hint: "current＝当前模型；也可填模型名"},
			{Path: "consolidation_model", Label: "整合模型", Kind: configFieldText, Hint: "current＝当前模型；也可填模型名"},
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
		Summary: "特性开关",
		Fields: []configField{
			{Path: "memories", Label: "记忆功能", Kind: configFieldBool3, Hint: "默认开"},
			{Path: "skill_offer", Label: "技能推荐", Kind: configFieldBool3, Hint: "默认开"},
			{Path: "lsp", Label: "语言服务器", Kind: configFieldBool3, Hint: "默认开"},
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
			{
				Path: "defaults.context_inject.warn_remaining_percent", Label: "提醒余量(%)", Kind: configFieldNumber,
				Min: 1, Max: 50, Clamp: true,
				Hint: "默认 12；剩余可用窗口低于这个百分比时提醒，范围 1–50",
			},
			{Path: "defaults.mcp_servers", Label: "共享 MCP 服务器", Kind: configFieldRows, Hint: "每行一个服务器"},
			// The /goal check's global row (plan 299) is two settings, not a
			// whole subagent entry: it is dispatched by the system, so it has
			// no name, description, prompt or tool policy to edit here, and a
			// rows descriptor on the value would offer all of those.
			{
				Path: "goal_evaluator.model", Label: "目标检查模型", Kind: configFieldText,
				Hint: "模型行 id（设置 · 子代理里选的那一行）；留空跟随主代理",
			},
			{
				Path: "goal_evaluator.reasoning_effort", Label: "目标检查思考强度", Kind: configFieldText,
				Hint: "留空跟随该模型行的默认强度；填 low、high 等覆盖它",
			},
		},
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
		Name: "peers", Title: "对等通信", Tab: configTabAgents,
		Summary: "本机代理进程之间互发消息",
		Fields: []configField{{
			Path: "claude_code", Label: "与 Claude Code 互通", Kind: configFieldBool3, ClaudeCodeOnly: true,
			Hint: "默认关；开关不同的 forebrain 进程互相看不见",
		}},
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
