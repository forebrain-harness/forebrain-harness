# Forebrain Harness 改进计划索引

本目录包含由 `improve` skill 生成的代码改进计划。每个计划都是自包含的实施指南，可由其他执行器模型独立完成。

## 最新审核信息

- **2026-10-16 批次执行（055–064，全部 APPROVE，未提交）**：按建议顺序由独立执行者实施 + 评审侧逐 diff 复核、亲跑闸门（整树 `FOREBRAIN_HOME=$(mktemp -d) CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 零 FAIL、vet 0、gofmt 空、前端 vue-tsc + 465 测试全绿）。**执行期新发现并同批根因修复**：① **064**（056 容器复现暴露的既有 Windows 链接序缺陷——`-lntdll` 先于 `-ltokenizers`，Rust≥1.87 的 libtokenizers.a 引用 7 个 NT 符号，GNU ld 左到右解析必失败，此前被 `cannot find -lpdfium` 掩盖；`pkg/llm/hftok/link.go` 改 `--start-group` 分组包裹，容器红/绿 + objdump import 表含 pdfium.dll 与 ntdll.dll 双证据，评审侧挂宿主模块缓存独立复验通过；**Windows CI 变绿前提＝056 别名 + 064 分组，056 Step 4 推分支后由 owner 观察**）；② 062 执行者发现 `make pdfium` 在 HEAD 因下划线三元组（`darwin_arm64`）传给要求斜杠的脚本而确定性失败（CI 直呼脚本所以从未暴露），已改裸脚本调用并实测 no-op 通过。**两处评审裁定（均有 owner 事前批准的语义背书，明示备查）**：058 的 STOP-2（钉住缺陷的用例 `text content, png ext` 期望改 ""、测试改名 SniffIsAuthoritative）；061 Step 1 因既有测试注释记载**有意设计**（标记行是展开态 wrapped 图片卡唯一的鼠标收拢路径）而**留给 owner 裁决**——Step 2（0×0）已完成。**遗留（owner 需知）**：057 存量工具行 base64 保持原样读取（新行落 file_reference）；063 存量超大附件会话升级后有一次缓存前缀位移；gateway 三个测试的 `t.TempDir` 清理竞态（既有 flake，053 期已报未修，本轮再次复现并附机理证据）；`fetch-pdfium.sh` 幂等短路前仍会发一次匿名元数据请求（403 时需 gh 凭据，非阻断）。**PR 描述必须补记（提交 6d1730e 主题为 "…"、无 body）**：本分支包含"go install 自 PDFium 原生依赖起不再是受支持的安装方式"的决定——`go install` 无法携带/定位 `third_party/pdfium` 的库与字体，因此 ci.yml/release.yml 的 go-install job、`scripts/check-go-install.sh`、README/RELEASING/bug_report 的相应段落一并移除；安装走 Release 档案 / npm 包 / `make build`。

- **2026-10-16 分支审计（bd2b8b8..fa4af8a + 未提交工作区）→ 新增 055–059**：4 个只读子代理分域审计（图像/PDF 管道、MCP/网关/TUI/前端、migrate/state 在途改动、依赖/打包/CI）+ 逐条人工复核引用代码。20 项发现中 5 份计划覆盖 12 项：**055**（解码炸弹/PDF 渲染预算/死旋钮 env/双读，P1 安全）、**056**（Windows `pdfium.dll.lib` 不在 MinGW ld 搜索序列 → required job 必红；CI 匿名拉取 403 已现场复现。owner 已确认 Docker 交叉验证路线，P1 合并阻断）、**057**（MCP/read_file/PDF 图片以全量 base64 落 parts_json 并随 messages API 外带，违反分支自身"无 base64"不变量；改走既有 file_reference+rehydrate，硬约束 replay/live 1:1 与缓存前缀稳定，P1）、**058**（`ImageMIMEType` 非图片嗅探仍回退扩展名——文本字节命名 .png 直发 provider；raw 端点吞掉 foreign session_id 后仍以整个 workspace 为根服务，P1）、**059**（子会话绑离父项目/DeletePreview 计数与列表脱节/OnlyProject 空 root 误诊/回填盲区与 dry-run 钉，P2）。**同日 owner 裁决「除 #14 外其余全部入期」→ 追加 060–063**：**060**（#8 MCP 无效 base64 块穿透为截断文本：裸 `continue` 改降级行，工具结果与资源两条路径，P2/S）、**061**（#11/#15 TUI 可折叠图片卡标记行点击"既折叠又开图" + `0×0` 与 web 卡省略不对齐，P2/S）、**062**（#13/#17/#19 README 与 CONTRIBUTING 裸 `go build` 指引对 pdfium 硬依赖失实（缺 pkg-config/jq）、plans/README:16「check-go-install 均已接线」失实、RELEASING re-tag 前提悬空、提交 6d1730e 主题"…"的 PR 描述补记文案，P2/S）、**063**（#18 用户附件/粘贴路径无图片预算：`ImageFile` 内部改走 `ImagePartWithin(…, 0)` 同契约——小文件逐字节直发、超预算降采样、`FOREBRAIN_IMAGE_MAX_BYTES` 生效；owner 已拍板附件与工具结果同契约；升级后存量超大附件会话有一次性缓存前缀位移，如实记录不修，P2/S-M）。**仍未入期**：#14 catalog 修正到不了存量 home（`Install` 永不覆盖，需版本化送达机制，owner 明确排除）；#20 的 web 缩略图 `@error` 兜底（investigate 项，未单独获批，记在 061 out of scope）。

- **2026-10-15 054 执行完毕（DONE，未提交）**：分两批派执行者完成 Step 0–4（PDFium spike / fetch 脚本 / `pkg/pdf` 渲染池 / 中文字体资产与播种 / 非-tofu 测试）与 Step 5–8（`read_file` 的 PDF 分支 / 打包与 CI 接线 / 真机验收脚本 / 全量闸门），评审侧独立复跑并做了真机验收。结果与证据：
  - **库与字体**：PDFium 取自 `bblanchon/pdfium-binaries` 的 **`chromium/8100`**（Apache-2.0），`scripts/fetch-pdfium.sh` 按目标三元组 pin SHA256、运行期从该 tag 的资产表解析 asset id（github.com 直连被墙，走 `api.github.com` 的资产端点）、生成缺失的 `pdfium.pc`、幂等；go-pdfium 的 cgo 走 `// #cgo pkg-config: pdfium`（无 build tag）。中文字体改成 **npm `@embedpdf/fonts-sc/tc@1.0.0`（SIL OFL 1.1，即 noto-cjk 上游同一份 NotoSansHans/Hant-Regular.otf）**——计划写的 Google Fonts 官方直链在本机不可达（上游 OTF 是 Git-LFS、jsdelivr 404、fonts.gstatic 只给 woff2），owner 2026-10-15 已批准这处代替；字体不入库，`assets/fonts/*.otf` 与 `third_party/pdfium/` 均已 gitignore。
  - **Step 0 三条证明（实测）**：① darwin/arm64 编译链接 PDFium 成功（`single_threaded.Init` + `GetInstance` + `OpenDocument` + `RenderPageInDPI`）；② 中文渲染正确——**不内嵌字体那份在喂入自带 Noto 后，页面墨迹像素与内嵌字体那份完全一致**（11792），证明回退用的就是我们自带的字体，不是系统兜底的侥幸；③ 池是进程级一次，`Close()`/`FPDF_CloseDocument` 资源释放路径可行。**本机（macOS）不设 `UserFontPaths` 也能渲染中文**（系统 Hiragino/Songti 在），所以 `FontDirs` 按计划收窄为「只有自带字体存在时才设」，并**在 Linux 上把平台字体目录一并追加**（`UserFontPaths` 是替换语义，不追加会让英文 PDF 一起坏）——有 `TestFontDirsIncludePlatformDefaults` 单测。
  - **非-tofu 测试是真证据，不是装饰**：`TestChineseGlyphsAreNotTofu` = **10/10 个字形的列墨迹签名互不相同**（阈值 ≥6）。计划指定的 `[]bool` 列投影在自带 Noto 上只有 5 个互异掩码（会误判），执行者改为「每列墨迹像素计数、24 分箱」的列投影（豆腐块时仍然塌缩成 1 个签名 → 失败），并按评审要求**补了检测器的红例**：`TestColumnProfileFiresOnTofu` 用合成图（十块相同方框 → 1 个签名；十种不同图案 → 10 个），并且执行者**实测把检测器临时改成常量后两个测试同时 FAIL、随后还原**——失败路径可达是被证明过的。
  - **owner 决策（2026-10-15）：放宽 `pkg/tool` 的 fan-out 棘轮 8→9**，并新增 `packageLayer["pdf"]=2`、`sameLayerEdges["tool"] += "pdf"`。原因：`pkg/tool` 恰好卡在 8/8 且测试明写「never raise」，而「改成注入」也无处可去（`pkg/run` 14/14、`pkg/process` 19/19、tui/gateway 全部满额）。三处各写了理由注释；`TestEveryPackageHasAnImporter` 在 Step 5 落地前是红的，现在全绿。**这条是本次唯一被放宽的架构预算，其余棘轮（含 test→prod 对应、同层边枚举、层上限）一律未动。**
  - **Step 5 的关键取舍**：PDF 分支在图片分支之后、闸门（`ImageInputRefusal`）先问；字体目录来自 **tool State 自己的 home**（新增 `State.PrimaryHome()`）而不是 `os.Getenv("FOREBRAIN_HOME")`——否则 `~/.forebrain` 默认与 `--home` 覆盖都会丢掉自带字体（Linux 上就是豆腐块）；**渲染出的页 PNG 落盘到 `<agent workspace>/state/tool-outputs/`，`preview_path` 指向第 1 页的真 PNG**（web 缩略图与 TUI `Ctrl+O` 都指向一个真实可取的图片文件；没有落盘目录时直接报错，绝不发一个取图端点会 415 的标记）；模型可见的那行说明仍点名 PDF 与已渲染页范围。
  - **Step 7 真机验收 = `read pdf: PASS`**（`scripts/acceptance/read_pdf.sh`，真模型 `deepseek-v4-flash` + 真 TUI + 全新 scratch home；内嵌与不内嵌两份中文 fixture **各 7 条断言全过**，共 14 条）：① TUI 头部点名该 PDF、步骤画出行内标记（`▤ … · Ctrl+O`）；② 读它**不弹审批**；③ **工具行的 `parts` 真的带 `{"type":"image"}`**（模型拿到的是渲染出的页图像）；④ 行内说明点名 PDF 与页数；⑤ **模型逐字复述出 `甲乙丙丁戊己庚辛壬癸`**（执行者按评审要求把提示词改成「只调用 read_file、不要执行 shell」——否则模型可以绕道 `pdftotext` 拿文本层，这条断言就被别的路径满足了；改后模型自己在回复里写明「我是从渲染出的页面图像上逐字读出的」）。
  - **打包/CI**：`Makefile`（`pdfium`/`pdfium-fonts` 目标，`build`/`test` 依赖它们并导出 pkg-config 环境）、`Dockerfile`、`npm/scripts/build-platform-packages.sh`（每目标取库 + rpath `@loader_path`/`$ORIGIN` + 随包带 `libpdfium` 与 `fonts`）、`ci.yml`/`lsp-integration.yml`/`release.yml`/`security.yml` 均已接线（go-install 相关 job 与 `scripts/check-go-install.sh` 已随后续提交 6d1730e 随 go install 支持一并下架）。**本机实测可构建 darwin/arm64 与 darwin/amd64**（后者交叉 + 各自 PDFium），产物内含 `libpdfium.dylib`(7 360 768 B) 与两份字体，`otool` 显示 `@rpath/libpdfium.dylib` + 两条 rpath。**linux/amd64、linux/arm64 无本机交叉工具链**（已接线，需 CI 验证）；**windows/amd64 留了一个待 CI 验证的真问题**：该归档只给 MSVC 格式的 `lib/pdfium.dll.lib` 而 CI 用 MinGW，且 `windows-latest` 上没有 pkg-config（已加 guarded 安装）——按计划的 STOP 条款**接线并如实报告，没有偷偷下架任何目标**。
  - **遗留（owner 需知）**：① **DX 变化**：加了这个原生依赖后，裸 `go build`/`go test` 会在 `Package pdfium was not found in the pkg-config search path` 上失败；必须先 `make pdfium pdfium-fonts`（或手工跑 `scripts/fetch-pdfium.sh` + `PKG_CONFIG_PATH="…/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"`，且本机要有 `pkg-config` 与 `jq`），CI 每个跑 Go 的 job 都补了这一步；② Linux/Windows 五个目标里只有两个在本机验证过（见上）；③ PDF 的 `pdf_pages`/`pdf_rendered`/`pdf_page_count` 三个键**只存在于 live 步骤事件**，不进库（库里留的是行内说明文本与 image block）——web 卡片的「PDF n 页」文案由 `SummarizeToolStep` 提供并有单测；④ `pdfReadNote` 的文案里 DPI 写的是 `DefaultDPI`（read_file 只传 `Options{}`，符合实际；将来若按调用方 DPI 渲染要一起改）；⑤ 二进制/动态库体积：`libpdfium.dylib` 7.36 MB 随包分发（未静态链接）。

- **2026-10-15 053 执行完毕（DONE，未提交）**：按 owner「execute 计划一律当前树当前分支直接实施、禁止 worktree、不 commit」的规则，分两批派执行者完成 Step 1–4c/8 与 Step 5/5b/7/8-3，评审侧独立复跑并做了真机验收。结果与证据：
  - **Step 6 真机探测 = 结论 A**（`FOREBRAIN_E2E_DEEPSEEK_KEY=… go test -tags fts5 ./pkg/llm/openai/ -run TestCompatToolResultImage -count=1 -v`）：**两个子用例都 PASS**（`deepseek-v4-flash` 与 `deepseek-v4-flash-vision-exp` 都接受 `role:"tool"` 消息里的图片，并正确答出颜色）→ **Step 4c 的 catalog 数据修正在线上被证实**，compat 投影无需改动（无 B/C 分支）。
  - **Step 9 真机验收 = `read image: PASS`**（`scripts/acceptance/read_image.sh`，真模型 `deepseek-v4-flash` + 真 TUI + 全新 scratch home，16 条断言全过）：① 步骤行渲染出 `image` 标记与像素尺寸；② 模型答对颜色与左右；③ **库里那条工具消息的 `parts` 真的带 `{"type":"image"}`**（模型确实拿到像素，不是猜的）；④ 标记行写出 `Ctrl+O` 且按键不崩；⑤ **MCP 图片落在 `<home>/workspace/state/mcp-results/`、home 下 `state/mcp-results` 无文件**，随后 `read_file` 该路径**不弹审批**；⑥ 切到 `deepseek-v4-pro` 后 `read_file` 图片得到**那句可执行拒绝文案**（点名模型 + 指路 `/model`），**不是** provider 400。
  - **评审期发现并在本期根因修掉的缺陷**（均超出计划原文，属「执行期发现的缺陷本期修掉」）：① `responses_llm.go:683-689` 对 `role:"tool"` 只取 `TextContent()`，**工具结果里的图片在 Responses provider 上被静默丢弃**（owner 的 chatgpt 主代理走 Responses）→ 改为「文本 `function_call_output` + 随后一条 user 消息带图片」，纯文本工具结果的投影逐字节不变并有单测钉住；② 验收 fixture `fake_mcp_stdio.py` 的图片常量 **IDAT chunk CRC 是坏的**（`file`/`sips` 只看文件头看不出来），DeepSeek 直接 400 拒绝 → 改为运行时生成合法 PNG 并做逐 chunk CRC 校验（评审侧已独立验证）；③ 验收脚本的断言原本能被模型的**推理文本**满足（`wait_for "image"` 命中的是 reasoning 行）→ 改为等 `Ctrl+O` 标记行 + 读库里的 final answer/tool row。
  - **接受的申报偏差**：`llm.ImageMIMEType` 收窄为四种可发送 MIME，且**嗅探出别的图片类型时不再退回扩展名**（BMP 字节套 `.png` 名字不再被标成 image/png）；取图端点 `GET /api/workspace/file/raw` 在**同一个 `tool.ResolveWithinRoots`** 里解析**两个根**（文件视图根 + 主代理 workspace）——评审方授权的偏差，否则 MCP 截图的 web 缩略图必然是破图；`resolveFilesPath` 自身只多了一条「绝对路径按原样（仍做包含性校验）」；预览事实挂在**会被持久化的 `tool.ToolMeta`** 上（不是只在 live 事件里的 `evt.Output`），以满足「replay 与 live 1:1」；Step 6 的集成测试落在 `compat_llm_test.go`（计划里的 `tool_image_integration_test.go` 文件名违反 `pkg/architecture` 的测试命名规则）。
  - **遗留（owner 需知）**：① Responses 那处修复只有请求形状单测（stub server）证据，**没有** Responses 真机回合（本机没有可用的 Responses 凭据）；② 取图端点只服务「文件视图 + 主代理 workspace」两个根（技能的其它根不在内）；③ **已装旧 home 的机器不会自动变**——`llm.Install` 只在新 home 播种、永不覆盖，owner 的真实 home 需删除 `<home>/models.json` 才会重新播种修正后的 catalog（否则闸门会继续拦 `deepseek-v4-flash`）；④ 旧 `~/.forebrain/state/mcp-results/` 按设计不写迁移、可自行删除，但 transcript 里已存在的旧路径若文件还在，读它仍会走一次审批；⑤ `pkg/gateway` 的限流与临时目录用例在整包满载下偶发（既有 flake，非本次改动，未改）。

- **2026-10-14 owner 报告「模型读不到图片」→ 新增 053；同日追加「Out of scope 里的必须加入本期计划」+「必须要支持中文字体」→ 扩为 053（图片）+ 054（PDF/中文）**。
  - **问题**（owner 附 Claude Code 截图：`Read(...corner_zoom.png)` → `Read image (24.2KB)`）：forebrain 里模型**读不到任何图片文件**，包括它自己 `screencapture` / playwright / 生成工具产出的 PNG。
  - **取证（源码只读核实）**：① `pkg/tool/file_tools.go:240-260 validateTextReadableFile` 对 `.png/.jpg/.jpeg/.gif/.webp/.bmp/.ico/.tiff/.tif/.heic/.avif` 一律 `read_file cannot read image files as text`；② `pkg/tool/` 下 **0 处** `[]llm.ContentPart` 返回（全部内置工具只回 string/map）；③ `pkg/turn/compaction.go:366` 的注释自己写下结论：「**No tool can hand the model pixels**」。
  - **但管道已通，缺的只是一个肯交出像素的工具**：`pkg/agent/agent.go:467 anyToContentParts` 与 `pkg/run/orchestration_llm.go:1537 toolResultContentParts` 对 `[]llm.ContentPart` **原样透传**；`pkg/llm/llm.go:89 ImageFile` 已有编码器；provider 三处翻译都在（compat `compat_llm.go:1106-1124`、Responses `responses_llm.go:1190-1206`、Anthropic `agent_llm.go:377-398`，其中 Anthropic 的 tool_result 原生支持图片块）；持久化双向支持（`state/usage.go:92-112` / `state/message_parts.go:464-476`）；`pkg/llm/tokestimate.go:89 ImageTokens=2000` 已给图片记占用。
  - **owner 的默认模型** `deepseek-v4-flash` 走 OpenAI 兼容路径（`~/.forebrain/forebrain.yaml` 的 main 主代理第 0 条），`assets/models.json:1519` 有 `deepseek/deepseek-v4-flash-vision-exp`（`input: ["text","image"]`）。
  - **本期范围（owner 2026-10-14 追加：「Out of scope 里的必须加入本期计划」）**：四类缺口**全部进本期**，拆成两份可执行计划（054 依赖 053 的 Step 1/Step 4）：
    ① `read_file` 读图片（053 Step 1–3）**＋ catalog 数据修正**（053 Step 4c：`deepseek-v4-flash` 的 `attachment: false` 是错的，owner 2026-10-14 确认官方路由到 `deepseek-flash`、实际收图；不修则下面的闸门会拦住 owner 的默认模型）；② **MCP 工具结果的图片透传**（053 Step 5：图片随工具结果作为 part 直接进模型）**＋ mcp-results 目录搬进主代理 workspace**（053 Step 5b：owner 2026-10-14 裁决「全局 MCP、主代理 MCP 和项目级 MCP 的 mcp-results 目录都必须放在主代理的 workspace 目录范围内」——目标 `<主代理 workspace>/state/mcp-results/`，与既有的 `state/tool-outputs`、`state/file-history` 同级；主代理 workspace 本就是 `ProtectedReadReason` 的豁免根（`activeWorkspace := r.workspaceRoot()`，`runner.go:1079`，**项目会话同样是主代理 workspace**），所以模型 `read_file` 自己刚拿到的产出不再弹审批/被拒，而 `ProtectedReadReason` **不需要**加任何豁免；旧 `$FOREBRAIN_HOME/state/mcp-results` 是瞬时产出，不写迁移）；③ **surface 出图**（053 Step 7：web 缩略图 + 新取图端点 `/v1/workspace/file/raw`（复用 `resolveFilesPath`），TUI 标记行 + `o` 键用系统查看器打开——owner 选定不做内联真像素）；④ **模型能力闸门**（053 Step 4：`ImageInputRefusal`，fail-open）；⑤ **PDF 逐页转 PNG**（**054**）。
  - **053 的设计取舍**：扩展 **`read_file`**（不新增工具名）= Claude Code 同形，模型不必学新工具；返回 `[]llm.ContentPart{一行说明, 图片}`；预算 3 MiB（`FOREBRAIN_IMAGE_MAX_BYTES` 可调）＋只用标准库的整数倍块平均缩小（不引依赖）；surface 只显示一行摘要、base64 绝不进事件与 UI。
  - **两个必须实测的不确定点**（计划里是显式步骤，不是假设）：① DeepSeek 兼容接口能否在 **`role:"tool"`** 的消息里收图片（OpenAI 规范里 tool 消息 content 是字符串）；② Responses 路径（owner 的 chatgpt 主代理）今天**静默丢掉**工具结果里的图片（`responses_llm.go:683-689` 只取 `TextContent()`）。053 的 Step 6 用 env-gated 集成测试给结论，并只在结论为 B 时做 provider 层的消息拆分翻译（拆分规则保持在**逐条独立投影**内，见 `compat_llm.go:1049-1060` 的前缀缓存铁律）。
  - **catalog 数据修正（review + owner 2026-10-14）**：`assets/models.json:1023` 的 `deepseek/deepseek-v4-flash` 写着 `"attachment": false`、`input: ["text"]`，**owner 已确认这是错的**（DeepSeek 官方把 `deepseek-v4-flash` 路由到 `deepseek-flash`，实际能理解图片）。后果：053 Step 4 的能力闸门会照这份数据**拦住 owner 自己的默认模型**（读图直接给拒绝文案）。所以 053 新增 **Step 4c**：改对 `assets/models.json` 这一条（`attachment: true` + `input: ["text","image"]`）、加一条数据完整性测试（`RepoModelsJSONForTest`，`pkg/llm/assets.go:133`）、并写清生效路径（`llm.Install` 只在新 home 播种、**永不覆盖已存在的 home 副本**，所以验收必须用全新 scratch home；现有 home 需删 `<home>/models.json` 才重新播种）。同一事实也改了两处验收：Step 6 的探测以 `deepseek-v4-flash` 为主用例（预期 A：接受），Step 9 用 `deepseek-v4-flash`（owner 真实默认）跑，一次同时证明"图片能读 + catalog 修正生效 + 闸门不再误拦"。**若实测与该说明矛盾（v4-flash 被拒）→ STOP**，Step 4c 的改动没有证据支撑，交 owner 判定。
    同日的第二条事实把口径补全：**`deepseek/deepseek-v4-pro` 是纯文本、不能识图**，而 catalog 里它本来就是 `"attachment": false`（`assets/models.json:1129`）——**这条数据是对的**，所以闸门**不能**被拆掉或放宽：v4-pro 正是它存在的理由（且它在 owner 的配置里可直接 `/model` 切到）。于是 053 的闸门有了成对的正反真机证据：v4-flash 该放行（Step 9 断言 ②）、v4-pro 该拒绝并给可执行文案（Step 9 断言 ⑥），再加上 Test plan 19/20 的真模型 + 合成条目 + 未知模型 fail-open 三条单测。
    最后一条事实把 catalog 的账算清了（owner 2026-10-14 第三次补充）：**`deepseek/deepseek-v4-flash-0731` 也是纯文本模型**，所以它现在写的 `"attachment": false` **是对的、不改**。结论：整个 catalog 只有 `deepseek-v4-flash` 一条是错的，改完即齐；`deepseek-v4-pro` 与 `deepseek-v4-flash-0731` 由 `TestCatalogKeepsTextOnlyDeepseekModelsAsTextOnly` 钉住，防止后人"顺手统一 family"——注意 `deepseek-flash` 这个 family 本身就同时含多模态（v4-flash）与纯文本（v4-flash-0731）成员，**能力是逐模型的、不能用 family 推断**。
  - **054（PDF + 中文）的选型已核实**（执行者不要换库）：采用 **PDFium**（Apache-2.0）＋ `klippa-app/go-pdfium` v1.21.3（MIT 包装，v1.21.3 的 `LibraryConfig` 暴露 `UserFontPaths` = `m_pUserFontPaths`，这是把自带字体喂给 PDFium 的唯一入口）；**排除** MuPDF/go-fitz（AGPL，本仓库 Apache-2.0 不可分发）、go-pdfium 的 WebAssembly 后端（`webassembly.Config` 无字体路径 + WASM 版编译为 Linux 拿不到系统字体 → 中文豆腐块）、纯 Go 玩具渲染器（CJK 正是它们的翻车点）。中文字体走 **Noto Sans SC/TC（SIL OFL 1.1）**：home 级播种（plan 047 那条路）+ `UserFontPaths`；macOS/Windows 另有系统 CJK 字体兜底。核心验收是**机器可判定的非-tofu 测试**（`TestChineseGlyphsAreNotTofu`：同一行 10 个汉字的字形掩码不能两两相同）。
- **2026-10-14 迁移完成后的复评（结论：048 撤回、049/050 驳回、051 保留执行、新增 052）**。
  - **实测完成数据**（owner 的库自己跑完了 v10→v14）：备份 13:31:43 → v11 汇总 **15:05:15**（93 分 32 秒）→ v14 完成 + `freed_pages=961977` **15:06:09**（尾巴含 3.9 GB 回收共 **54 秒**）。库文件 5.58 GB → **1.60 GB**，`-wal` 随最后一个连接关闭被清理，可用空间回到 22 GiB。
  - **被推翻的判断**：`main_agent_rows_deleted=0` —— 048 认定的「平方级主清理」在这台库上一行都没匹配；93 分钟几乎全在 **subagent 阶段**（删 1,025,186 行 + 按连续块合并掉 6,493,409 行 / 233 个执行体）。「工具输出每 step 全表扫描 = 天数级」的量级也估算错了：该阶段在 subagent 阶段之后跑，事件表已缩小到可忽略，实测零头都算不上。
  - **因此**：048 的两项查询计划改动（单调游标 + 两条迁移局部表达式索引）**撤回**（SUPERSEDED，保留部分见 052）；049（迁移连接 cache/WAL PRAGMA + 2.2× 预检）与 050（VACUUM 阈值 + 备份保留）**驳回**——实测下 1.1× 预检够用、WAL 在连接关闭时自行清理、回收本身只要几十秒、遗留备份 owner 已手动删除。**唯一保留的是 052**：v11 每阶段的进度日志（93 分钟零输出正是这次被误判为「卡死」的直接原因）；**owner 随后裁决 052 也不做**（理由见「已考虑并拒绝的发现」），本批次至此无待执行项。
  - **051 已执行完毕并已并回主工作区（DONE，未提交）**：运行期正确性缺陷，与迁移无关。首轮派发因**计划夹具缺陷** STOP（`RewindEventStart` 对无事件的 run 返回 0，零范围 rewind 在修复前后都不隐藏任何行 → 测试假通过；执行者自己在模拟库里复现了跨会话隐藏，确认谓词缺陷真实）；计划修正后（先给 `r-rwd` 插一条事件 + `require.Greater(start, 0)` 护栏）执行者完成并提交：`pkg/state/rewind.go`（模板+工厂 → 引用外层表的常量）、`session_events.go`（7 个调用点）、`rewind_test.go`（跨会话回归）。**owner 随即要求并回主树**：以 `git apply` 落成主工作区的未提交改动（未在 main 上产生提交），主树复核全部通过——`pkg/state` 全套 `ok`、修复前 FAIL（`another conversation's rewind hid this session's events`）→ 修复后 PASS、谓词正文除两处表名限定外与旧模板逐字一致、EXPLAIN 由 `SCAN rw` 变为 `SEARCH rw USING INDEX idx_fb_rewinds_session_state`。分支 `advisor/051-rewind-predicate`（commit `39c1a5c`）保留作复核记录；执行者的 worktree `/tmp/fb-adv-051` 已清理。
- **2026-10-14 schema 与数据迁移性能审计（focus: schema + data migration perf；产出 048–051）**。
  - **起因**：owner 的 TUI 启动卡在「database schema migration」超过 1.5 小时。取证结论：**不是 VACUUM**——另一个连接读到已提交快照的 `PRAGMA user_version` 仍是 10（事务未提交），主库文件 mtime 停在 13:27（原地 VACUUM 会重写主文件），`-wal` 3.1 GB 仍在增长，进程 96% CPU 全在 `sqlite3_step`。真正卡住的是 **v10→v11 的两段清理语句**：主代理清理每批 50k 行都从表头重扫（平方级），工具输出清理对每个 tool step 各做一次全表扫描（`COALESCE(run_id,'') = ?` 让 `idx_fb_session_events_run_type` 失效、没有索引以 `event_type` 打头）。该库实测 7,691,906 行事件里 7,565,584 行（98%）是流式增量。
  - **实证数字**：`EXPLAIN QUERY PLAN` 在本仓库 schema 上确认三条语句均为 `SCAN fb_session_events`；加上局部表达式索引后全部变成 `SEARCH ... USING INDEX v11_tool_*`；主清理改单调游标后为 `SEARCH ... USING INTEGER PRIMARY KEY (rowid>? AND rowid<?)`。
  - **产出**：048（P1，清理线性化 + 局部索引 + 进度日志）、049（P2，迁移连接 PRAGMA + 空间预检）、050（P2，VACUUM 阈值 + 备份保留）、051（P1，**本轮审计的副产物**：rewind 可见性谓词 `rw.session_id = session_id` 是恒真式，导致一个会话的 rewind 隐藏其它会话的事件；不在性能范围内但已立计划）。
  - **两处被评估后拒绝的「优化」**：VACUUM INTO 替换原地 VACUUM、`foreign_key_check` 按表收窄——理由见「已考虑并拒绝的发现」。
  - **未计划但已记录**：`pkg/state` 之外的运行期性能发现（memory FTS 重建整库持写锁、`message_sync` 每回合重读 5000 行、gateway 历史接口无上限读全量消息等）逐条列在「已审核但未计划的发现」。
- **2026-10-14 执行批次（044 / 045 / 046 / 047 全部落地，改动留工作区未提交，批次完成）**。
  - **四份全部 DONE**（044、045 由执行者实施；046 因 `subagent_run` 从工具集中消失，改由评审方在主线程按计划实施并自证；047 由执行者实施）。
  - **047 补充**：验收脚本只认 `FOREBRAIN_ACCEPT_HOME`，永不读环境里的 `FOREBRAIN_HOME`。
  - **全量闸门（主线程，`FOREBRAIN_HOME=$(mktemp -d)`）**：29 个包 `ok`；唯一失败 `pkg/mcp` 的 `TestStartStdioConnectsRealMCPServerProcess` 已用 **HEAD 的纯净导出**证明是**既有失败**（与本期四份改动无关：fixture 的协议版本 2026-07-28 拒绝在请求中发 `roots/list`，属依赖升级引入，待单独立项）。`go vet` / `gofmt` 干净。
- **2026-10-14 执行批次（044 / 045 / 047 已落地，046 未开始，批次因一起真实 home 事故中止）**。
  - **已落地并独立复验**：**044** DONE、**045** DONE、**047** DONE（改动全部留工作区，未 commit）。主线程独立复跑：`pkg/run`、`pkg/memory`、`pkg/process`、`pkg/llm`、`cmd/forebrain` 五包 `ok`；三个新回归（`TestForkContinuationSpeaksUnderItsOwnRun`、`TestCompactionGuardKeepsAHistorysCheckpointToItself`、`TestCompactionGuardCachesNothingWithoutARunID`）PASS；047 的三条验收链路（打包二进制启动播种 → 旁无字典的二进制读 home → home 不可写回落到二进制旁）我逐条复跑，全部 `memories search: PASS`。
  - **046 未执行**：依赖 045 已满足、随时可派发；本批次在 047 执行结束时被中止（原因见下）。
  - ⚠️ **事故（047 执行者，本机真实 home）**：验收脚本第一版用环境变量 `FOREBRAIN_HOME` 决定被测 home，而该变量在 agent 会话里指向**真实** `~/.forebrain`，于是三轮验收把 owner 的真实 home 当成了被测 home：① `~/.forebrain/forebrain.yaml` 被 258 字节的测试配置覆盖，执行者用 `forebrain.yaml.bak-20260921T045702Z`（**09-21** 的旧备份）还原——**10-14 10:26 之后写在里面的改动（当时 5238 字节）已无副本可找回**；② 真实库被两个 5.3GB/5.4GB 的「迁移前备份」残留占进 `~/.forebrain/state/backups/`（`user_version` 仍是 10，迁移没有执行，库本身未被改写）；③ 真实 home 的 `skills/.system`、`dict/`、`workspace_trust.json` 被启动播种/信任流程touch。**脚本已改为只认 `FOREBRAIN_ACCEPT_HOME`、永不读环境里的 `FOREBRAIN_HOME`**，之后三轮验收不再触碰真实 home。教训写入 047 的 Maintenance notes：**任何验收/测试脚手架都不得继承环境里的 home 变量**。
- **2026-10-14 owner 拍板：006 排查结论落成三项修复，另加一个 P1 缺陷（044–047）**。006 的决策闸门判定「可达」（fork 续跑 `subagent_continue` 不装自己的 run ID，沿用编排 run ID，经主/fork 共享的 `recoverableLLM` 把主链 checkpoint 拼进续跑请求）。owner 当场把三件一起排进本期：**044**（fork 续跑在自己的 run 身份下发请求）、**045**（`compactRunGuard` 按 (run ID, worker 会话) 多槽）、**046**（分不清暂定尾巴的调用不压缩 ＋ 尾巴归属按历史判定，依赖 045）。006 本身闭环（排查交付完成），索引状态改 SUPERSEDED。同日 owner 报告 `memories_search` 工具失败（`memory search dictionaries are missing from /Users/doudou/go/bin/dict`，P1；根因：字典是唯一只按「可执行文件旁」解析的运行时资产，且下载只认严格 release 版本，本机跑的是 `v0.1.2-…+dirty` 的 GOBIN 构建），拍板修法「home 级位置＋自愈」，并追加两条硬性要求：**本地开发只用 `npm/scripts/build-platform-packages.sh` ＋ `cd npm && npm link` 后 `memory_search` 必须可用**、**`npm install` 装出来的 forebrain 必须开箱即用（不许手工脚本）**，落成 **047**。**owner 追加边界（同日）**：安装脚本**不写 `$FOREBRAIN_HOME`、不写 GOBIN**——home 里的字典由**产品在启动时自己播种**（`pkg/process/seed.go` 里紧跟 `llm.InitGlobalCatalog` 调 `memory.Seed`，与 models.json 同一条路径、同样只 `slog.Warn`），来源是「运行中二进制旁那一份」，打包脚本只做一件事：校验打包产物带全了字典（缺一条即构建失败）。代价：**不含本计划的旧二进制必须先被替换（重建或改走 npm 包）**，否则它既不会读 home 也不会播种。四份计划都未执行。
- **2026-10-13 阻塞：执行者额度用尽**。子代理派发返回 `429 您已达到每周/每月使用上限，限额将在 2026-10-15 15:19:57 重置`，并行执行通道暂时关闭；评审方改在主线程接手。**剩余计划（根 001/006/014–021/024/030/035/037/038–042 与 web 017/018/020）在额度恢复前无法派发。**
  - **036 落地情况**：一次派发在运行中被中断，但它已把 Step 1–6 基本写完（`pkg/skill/versions.go` 1026 行 + 9 个测试含发布原子性故障注入、`service.go` 的 bootstrap/安装即 v1、`git_install.go` 上游记录、两个内置 SKILL.md 改写、`LoadedSkill.Version/VersionsRoot`、工具侧草稿重定向、gateway 主代理层与项目层各 7 条版本路由）。评审方复核时发现两点并**根因修掉**：一是 `TestWorkshopSkillFilesListReadAndWrite` 的草稿路径多退了一级（代码按计划用 `<技能根>/.drafts/<名称>`，测试写成 `<工作区>/.drafts/…`）；二是同一测试的读回断言用子串匹配 JSON 转义后的响应体，改为解码后 `JSONEq`。修后**全量 Go 测试 29 包全 ok、前端 397 用例 + vue-tsc 干净、fake 全量 e2e 114 passed/2 skipped/0 failed**。
  - **待复核（评审缺执行者报告）**：STOP 条件 2 的提示缓存证明（`Version` 字段是否只进会话私有状态、不动冻结前缀）、STOP 3/4 的判定、上游更新检测与 `PullUpdate`、`pkg/turn/skills.go:52` 那处读取是否也应走版本目录（现用于斜杠命令发现，读正式副本）。
- **同批续（2026-10-12）**：**033** 执行完毕、评审 **APPROVE**。迁移 **v10**：新表 `fb_file_changes`/`fb_rewinds`，`fb_messages.visibility` 增加 `'rewound'`（走 rename-aside 重建，我逐字核对重建 DDL 与 `schema.sql` 一致、列序一致、两个索引重建、无触发器、`legacy_alter_table` 用法正确）。事件过滤收口为 `state.RewindEventsPredicate`（7 个展示读取方全部接入，插入去重那处按设计不接）；`sequence` 是 `AUTOINCREMENT` 故序号不会复用，过滤高水位与分页谓词一致、无空洞。web 与 TUI `/rewind` 共用 `pkg/turn/rewind.go`；`rewind-last`/`LatestRewindableToolCall`/`LatestSnapshot` 全部删除（我的独立 grep 为空）。fake 全量 e2e **114 passed / 2 skipped / 0 failed**。
  - **打开新构建时会一次性迁移真实库**（我在 4.3GB 真库上核对：`user_version=9`、`freelist_count=0`、磁盘余量 4.6GiB）：迁移本体约 45s，框架随后的 `VACUUM` 需再写一份约 3.6GB 的临时文件（峰值 original+temp ≈ 7.9GB，但 VACUUM 写的是新增占用，4.6GiB 余量下约剩 1.3GB 空间，能过但很紧）。迁移在 VACUUM **之前**已 COMMIT，且 VACUUM 只在版本变化时执行——所以万一磁盘满，只会让**那一次**启动失败，重试即跳过。**建议先腾出磁盘空间再启动**。
- **同批后续（2026-10-12）**：**022** 执行完毕、评审 **APPROVE**（文件视图唯一的根解析 `pkg/gateway/session_files.go` 的 `filesRoot`/`resolveFilesPath`；旧 `mentionRoot`/`resolveWorkspacePath` 删除；越界检查仍走 `tool.ResolveWithinRoots`，新增用例覆盖项目根、工坊技能目录、无项目会话三种根与符号链接逃逸）。**web 016** 执行完毕、**APPROVE**：016 的 STOP 条件 3（审批入口）由 owner 裁决「审批进消息流」后化解——`ApprovalCard.vue` 改为可交互（承接 `PendingActionsPanel` 全部能力，testid 与行为零丢失、`exit-plan-approval.spec.ts` 未改且全绿），另加顶栏注入、composer 五个 chip、`McpNotice`、会话模型接口。**web 019** 执行完毕、**APPROVE**（Crepe + CodeMirror，monokai-light 单一调色板；`vaul-vue` 的 UNKNOWN 许可证是既有依赖）。**执行中发现并根因修掉的既有缺陷（无计划文件）**：记忆管理器把记忆仓库自身的 git 元数据当记忆文件——`collectMemoryFiles` 列出 `.git/config`、`.git/objects/*`（真实 home 里 41 个普通文件），`resolveMemoryFilePath` 也只查包含性，于是读/写/删接口都能改到仓库簿记。修法：gateway 侧与代理侧对齐，根下点号前缀条目一律隐藏（权威规则在 `pkg/memory/backend.go:511`），列表与路径解析共用一条规则。**e2e harness**：内存后台管线在首个回合写入记忆根（脚手架文件 + `.git`）并污染记忆 spec 的种子隔离；harness 两个配置分支改为 `features: memories: false`（只关 `generate_memories` 不够——启动管线由特性开关闸住）。
- **项目模型一批（2026-10-11/12，owner 指令「按 plans/README.md 顺序派发」）**：025 → 026 → 027 → 028 → 029 全部执行完毕、改动未提交留在工作区，评审 **全部 APPROVE**。执行中发现并**本期根因修掉**两处 027 引入的回归：① 计划 043（新建，原编号 038 与另一会话冲突）——027 去掉 gateway 启动项目后，`s.projectKey()`/`env.projectScope()` 恒为空，与项目 runner 的 `ProjectKey` 分叉，web 计划文件/审批/记忆/cron 会话全坏（我独立复现：`cron-fire` ×2 + `exit-plan-approval` ×2 失败）；修法是会话级 `SessionProjectKey`/`sessionProjectKey`，删掉 `Server.projectKey()` 与 `Environment.projectScope()`。② 池条目缺线（无计划文件，定点修复）——`buildEntryLocked` 手搓 runner 没装 `FileResolver`/`SubagentExecutor`，gateway 项目会话里 `subagent_run` 直接失败（`subagent requires an in-process executor`）；改为一处共用 `Environment.wireRunner`，`Open` 与 `buildEntryLocked` 都调它。评审侧独立复跑全量 web e2e：**95 passed / 2 skipped / 0 failed，`web e2e: PASS`**（基线 87/5/2）。028 与 029 **未并行**：两者共用 `pkg/gateway/api_extra.go`（028 删 `validateProjectRoot`、029 加端点），同一工作树并行会互相覆盖，改为串行。web 015 也已完成（同一批，见 web 目录 README）；web 019 尚未开始。
- **审核日期**：2026-10-10
- **基准 commit**：9656051（工作区含 owner 未提交改动：`pkg/tui/notify.go` 的 `markEventShownOnce` 等，与 007 无关）
- **审核类型**：exit_plan_mode 审批期间 TUI 出现假 "Exited plan mode" 卡、取消后多一张 "Canceled" 卡的根因定位与修复（`plan` 模式，单计划 007）
- **问题报告**：owner 报告（附截图）"running plan-review subagent 时并没有退出 plan 模式，但是显示了 Exited plan mode 消息卡片"；"按下 ctrl+c 取消审批后，已经显示了 You canceled forebrain's request to exit plan mode，不需要重复显示 Canceled 消息卡片"
- **结论（007）**：两个 bug 同一根因——主 agent 的工具步骤经 `runAuditStepHook → notifyToolStepHooks` 直接进 UI，不经 d501e09 加了过滤的 `publishRunEvent` 漏斗，gate 的等待帧被画成卡（标题落进 "Exited plan mode"），取消时被 `FinalizePendingTools` 改成 "Canceled"。去掉假卡会暴露确认行"插到最后一张工具卡之前"的落位错位（回放今天已错位，真机复现），所以 007 一并修 live 与回放的落位。web 同源错位未纳入，待 owner 决定是否另立计划
- **同日追加（008）**：owner 要求另写 web 同步计划。web 除了同源的落位错位，还会丢行（同一 gate 的多条决定只留最后一条，review 交付会抹掉 "You asked … review"）、重载时 review 卡落到整轮末尾。008 用不渲染的 gate 锚点块标记无卡调用的位置，exit gate 的决定行逐条插在锚点之前
- **008 执行（2026-10-10，owner 指定 worktree）**：已派发执行者在独立 worktree `../forebrain-harness-008`（detached 于 9656051）完成，评审 **APPROVE**。5 条 Done criteria 评审侧复跑全过（326 tests 全绿 / vue-tsc 无输出 / build 成功 / grep gateStepIds 与旧注释均无输出）；diff 逐段对照计划、范围干净（4 个 in-scope 前端文件 + `pkg/gateway/dist` 构建产物）；两处申报偏差均接受（`messageHasBubble` doc 注释同步一句；计划第 11 条"live 与重载各一遍"拆成两个用例，新增共 7 个）。**改动未提交、留在 worktree，由 owner 决定并回方式**（主树 ChatView.vue / WorkshopView.vue 有 owner 未提交改动，并回时注意）；007（TUI 侧）仍 TODO，两端应一起交付。
- **007 执行（2026-10-10，owner 指定 worktree）**：已派发执行者在独立 worktree `wt/improve-007-exit-gate`（`$TMPDIR/fb-007-wt`，基于 9656051）完成，评审 **APPROVE**。全部 Done criteria 评审侧独立复跑通过（8 个新/改写测试 PASS、`./pkg/... ./cmd/forebrain` 全绿、vet/gofmt 干净、graph.json 无 diff、grep 项全过）；diff 逐段对照计划（9 个 in-scope 文件 + 2 个勘误文档，无 import 增删）；driver 真机 A–F 全过（A/B/D 假卡与 Canceled 卡为 0、C 回放同序、E review 落位与批准卡正确、F 拒绝带反馈 live/回放一致）。三处申报偏差均接受（test 8 首匹配调整——回放合并 assistant 文本为 9656051 既有行为，非本改动引入；场景 C 的 "Worked for" 行落位为既有 run_id 缺失投影；channels.go 注释补两句）。**改动未提交、留在 worktree，由 owner 决定并回方式**（主树 `pkg/tui/notify.go` 有 owner 未提交的 `markEventShownOnce` 改动，并回时注意；`$TMPDIR` 重启会清空，尽快落地）。待 owner：用真实模型复核一次成功 review 路径（review 期间无 "Exited plan mode" 卡、交付后修订与新 gate 依次出现在 review 卡之后）。
- **007 branch 审核（2026-10-10，第二轮，`/improve branch` 针对 worktree `$TMPDIR/fb-007-wt`）**：逐段复核 007 实现，发现 1 处语义缺口 + 1 处文档落位问题，立计划 009 并派执行者在同一 worktree 叠加修复。① **回放未封存发出无卡 gate 调用的那段回答**（探针实证）：live 里 gate 的 running 步骤进 reducer 时封存文本，回放没有这条步骤的行（等待不画卡、取消/交付结果行被丢弃），owner 截图 1 的流程（review → 交付 → 修订 → 批准）回放成"残留 non-final 的 `here is the plan` 块 + 粘连块 `here is the planrevised plan`"，第一段回答出现两次；只有 transcript 时也会把两段回答粘成一块。缺口在 9656051 已存在，但 007 的行为矩阵与 test 8 明确要求修好——执行者把 test 8 改成"只取第一个匹配"，测试在 bug 存在时仍通过（上一轮评审接受的"test 8 首匹配调整"偏差即此，应驳回）。② `docs/plan/PLAN_REVIEW_DELIVERY_SILENT_PLAN.md` 的勘误段插进了第一个引用块中间，把"评审记录 / owner 裁决"并进了勘误引用块。其余逐项核对无问题：live 入口过滤取对缓冲区；漏斗删除后发布事件的 started 帧 `ToolMeta.Status` 恒为 `running`（`BuildToolMeta → stepStatusLabel`），仍被 reducer 拦截；所有带 `Confirmation` 的审批记录都写 `action_kind` 与 `tool_step_id`；gofmt/vet 干净。修法原型已在 scratchpad 副本验证：`./pkg/tui` 全绿，交付流程回放与 live 一致。
- **009 执行（2026-10-10，叠加在 007 的 worktree `$TMPDIR/fb-007-wt` 上）**：评审 **APPROVE**。执行者先让收紧后的 test 8 与新测试 `TestReplaySealsTheResponseThatIssuedACardlessGate` 在修复前失败（`here is the plan` 块 `Final:false`；`["first answersecond answer"]`），修复后通过；评审侧独立复跑：`./pkg/tui`、`./pkg/architecture` ok，007+009 相关 12 个测试全部 PASS，vet/gofmt/graph.json 干净，`git status` 仍是同样 11 个文件（009 只改 `commands.go`、`commands_test.go` 和勘误文档的位置）。真机：C'（取消后回放）由执行者跑通；E'（review → 批准 → 回放）被执行者误判跳过（driver 里 review 失败本来就在预期内），由评审补跑：live 与回放都是 `calling exit_plan_mode` → `You asked` → `Failed to start 1 plan-reviewer task` → `You approved` → 唯一一张 `◆ Exited plan mode`，`calling exit_plan_mode` 只出现 1 次，无 `◆ Canceled`。**007 + 009 的改动都未提交、留在同一个 worktree**，由 owner 决定并回方式（`$TMPDIR` 重启会清空，尽快落地；主树 `pkg/tui/notify.go` 有 owner 未提交改动，并回时注意）。上一轮接受的 007 偏差「test 8 首匹配调整」应视为驳回，已由 009 纠正。**已并回主树工作区**（2026-10-10，`git apply` 到 main 工作区、未提交、未动暂存区；与 owner 未提交的 `notify.go` 改动无冲突）。并回后 worktree `$TMPDIR/fb-007-wt` 与分支 `wt/improve-007-exit-gate` 已删除（删除前核对：分支无额外提交，worktree 改动在主树逐文件一致）。**review 交付成功路径已在主树用 glm-5.3 真机跑通**：review 期间 0 张假卡、交付静默、修订回答独立成块，live 与 `resume` 回放关键行逐行相同（记录见 009 末尾）。附带发现：home 在符号链接路径下时计划写入守卫只做字面比较（`pkg/tool/state.go:916`），既有问题，未立计划。
- **008 branch 审核（2026-10-10，`/improve branch` 针对 worktree `../forebrain-harness-008`，对照 plan 008）**：实现与计划逐行一致；TUI 发起的会话用本机真实数据（`cli-388cb9d1`）重载，块序正确。发现 3 处计划前提/范围缺口 + 1 组并回风险，均经探针（scratchpad 副本，未碰 worktree）或真实事件实证：① **gateway 发出的 `approval_requested` 不带 `tool_step_id`**（`pkg/gateway/wsevents.go:92-103`；只有 TUI 写），web 发起的会话重载后锚点学不到 actionId，review 卡落到整轮末尾、live≠重载——008 的问题 3 在 web 会话里原样存在（008 写"已核实"只核了 TUI）；TUI 回放 gateway 会话同样落不了位。② **拒绝带反馈时 web live 不画 settled 卡**：运行时注入拒绝结果不发步骤事件（`pkg/run/orchestration_llm.go:111-145`），TUI 的 "Kept planning" 是本地合成（`pkg/tui/chat_turn.go:1026` 起）；008 推演表声称 live 有这张卡，实际只有重载有（9656051 既有）。③ **ChatView 的块分支仍按 `msg.blocks?.length` 选择**（`ChatView.vue:184`），只剩锚点的回答（不说话直接调 exit_plan_mode 后在 web 停止）画空气泡、藏掉 "Request cancelled"；`hasVisibleBlocks` 还把无确认行的审批块算作可见。④ 并回：WorkshopView 那一 hunk 与主树 `005c1a8` 的 `rounded-lg` 改动相邻，`git apply --check` 失败；worktree 的 `pkg/gateway/dist` 由旧源码构建，不能拷进主树（并回后在主树重新 `pnpm build`）；上一条 008 记录里"007 仍 TODO"已过时（007+009 已随 `005c1a8` 提交）；008 Done criteria 的"TUI 回放 vs web 重载对照"尚未做。**Owner 裁决（2026-10-10）**：① gateway 补 `tool_step_id`（改 Go，不做前端启发式）；② 由**引擎**在注入拒绝结果处发 settled 步骤事件（不由 web 补画）；写完即派执行者在同一 worktree 叠加。立计划 011/012/013。写 012 时发现关键坑：拒绝完成事件与"等待审批"完成事件的规范事件 ID 相同（`tool:<run>:<step>:tool_call_completed`，真实数据核实），会被 `ON CONFLICT DO NOTHING` 吞掉——012 Step 1 加 `:denied` 边界。
- **011 执行（2026-10-10，worktree `../forebrain-harness-008`，叠加在 008 上）**：评审 **APPROVE**。评审侧独立复跑：`./pkg/gateway`、`./pkg/turn`、`./pkg/architecture` ok，vet/gofmt 无输出，前端 40 文件 327 用例全绿，vue-tsc 无输出；diff 7 个 in-scope 文件逐条对照计划（`turn.ApprovalToolStepID`、`withApprovalToolStepID` 定义 1 + 调用 2、wsevents 转换 1 行、3 个新测试）。执行者做了反证：去掉 `tool_step_id` 后新前端用例失败，review 卡掉到重载末尾，与探针一致。一处申报偏差接受：完成标准里 `grep withApprovalToolStepID` 实得 4 行而非 3 行——计划自己给的 doc 注释含该标识符，属计划笔误。未提交、HEAD 仍为 `9656051`。
- **012 执行（2026-10-10，同一 worktree，叠加在 008+011 上）**：评审 **APPROVE**，无申报偏差。源码 diff 逐条对照计划：`StepEvent.Denied/DenyFeedback`、`DeniedToolStep`、状态/正文/summary 三处、`:denied` 事件边界、拒绝注入循环；delivered 分支只改成提前 `continue`，Body 和 meta 的构造逐字节不变；TUI 主 agent 分支只落库不画。6 个新测试逐条读过，断言都打在关键行为上（只发一条、落库与步骤同源、delivered 不发、ID 不撞、TUI 有哨兵证明 UI 通道是通的、gateway 两条完成事件都在库里）。评审侧复跑：`pkg/tool`、`run`、`gateway`、`turn`、`event`、`architecture` 均 ok；`pkg/tui` 只有 `TestCharacterizationNetworkApproval` 失败，原因是 curl 访问 example.com 超时（走批准后真实执行的路径，与拒绝无关），单跑三次都通过；vet/gofmt 无输出，前端 328 个全绿。**评审侧独立 driver 真机复跑**（隔离目录 `$TMPDIR/fb-012r`，端口 8813）：live 和回放都是 `calling exit_plan_mode` → `✗ You did not approve` → 唯一一张 `◆ Kept planning └ 先拆分里程碑`；库里同一调用 `call_fake_1` 同时有 `…:tool_call_completed` 与 `…:tool_call_completed:denied` 两条，证实 `:denied` 边界防住了被吞。临时目录已删。未提交，HEAD 仍为 `9656051`。
- **013 执行（2026-10-10，同一 worktree，叠加在 008+011+012 上）**：评审 **APPROVE**。diff：`hasVisibleBlocks` 判断 + doc、`ChatView.vue:184` 一处条件、008 用例补 3 条断言、`ChatView.test.ts` mock 暴露 `messages` 并新增用例。评审侧复跑：前端 40 文件 329 用例全绿，vue-tsc 无输出，`msg.blocks?.length` 只剩打字点那一处；**评审侧在 scratchpad 副本里独立做了反证**：把 `:184` 改回原样，新用例就失败在 `toContain('Request cancelled')`。dist 在最后一次源码改动之后重建（16:51:31）。两处申报偏差接受：① 用 `vi.waitFor` 代替计划样例的 `nextTick`——Markdown 组件逐步打字输出，仓库已有同样写法的先例（`ToolOutput.test.ts`）；② `mountView` 与共用的 `beforeAll` 提到文件顶层（计划允许"提升或照抄"二选一）。未提交，HEAD 仍为 `9656051`。**011–013 全部完成**，连同 008 留在 worktree `../forebrain-harness-008` 待 owner 并回。
- **008 系列并回主树（2026-10-10，owner 指令"008 系列并回主树"）**：从 worktree `../forebrain-harness-008` 导出 `git diff 9656051 -- . ':!pkg/gateway/dist'`（20 个源码/测试文件），其中 18 个 `git apply` 到主树工作区时干净应用；`WorkshopView.vue` 与 `ChatView.test.ts` 按 README 并回须知手工合并：前者保留主树的 `rounded-lg`、取 `hasVisibleBlocks`，后者取 worktree 版本并把主树的 `SessionHeartbeat: true` 补进顶层 `mountView` 的 stubs。合并后逐一核对：两个手工合并的文件都保留了双方的改动。dist 按仓库约定用 `make ui` 重建（先清空再构建，196 个资源换了哈希，`index.html` 引用的文件都在，`.gitkeep` 保留），嵌入新 dist 的二进制编译通过。主树验证：`go build ./...` 通过；`pkg/tool`、`run`、`gateway`、`turn`、`event`、`state`、`architecture`、`tui` 全部 ok（含 007/009 的 TUI 改动一起跑）；vet/gofmt 无输出；前端 40 个文件 339 个用例全绿，vue-tsc 无输出。**未提交、未动暂存区**，HEAD 仍为 `005c1a8`。跨端对照已做，结果写在 008 末尾"验收记录"：F 拒绝带反馈、E review 失败后批准两段真实 TUI 会话，TUI 回放与 web 重载逐行一致。**影响 plan 010**：010 的 drift check 要求 `pkg/tool/state.go` 相对 9656051 无改动；012 在 `StepEvent` 末尾加了两个字段（与 010 要改的区域无关），010 的执行者会在 drift check 停下，需评审方先刷新 010 的 drift check。worktree `../forebrain-harness-008` 仍保留（改动已全部进入主树工作区），是否删除由 owner 决定。**worktree 已于同日按 owner 指令删除**（删除前逐文件核对：18 个文件与主树一致；另 3 个文件的差异全部来自主树 `005c1a8` 的 owner 改动，其中 `ChatView.test.ts` 仅缩进不同；worktree 为 detached HEAD、无额外提交）。
- **"本机 web 与 TUI 同源"一轮（2026-10-10，owner 连续裁决，立计划 014–024）**：起因是 owner 追问"TUI 会话不落主 agent 文字增量是否是 bug"。现状核实：TUI 主 agent 只落工具步骤/审批/plan review/压缩/`plan_updated`/`turn_error`/`turn_cancelled`；没有 `turn_started`/`turn_completed`、文字增量、diff、预算、队列、模式事件；gateway 的事件总线只转发本进程发布的事件，`bind_session` 只在绑定时补快照，不读其他进程写入的数据；subagent 的逐 token 增量已有约 450 万行（每行约 4 个字符），与 transcript 重复存储；web 工作台文件树、`@` 补全、`@` 图片以 primary agent workspace 为根，不是会话实际目录（owner 截图）；非项目会话的工具一律在进程启动目录执行。**Owner 裁决**：① 两端所有会话数据落库、同源；② 流式文字只存 transcript（文字块结束时写入，不按空行切；增量不再落库，主 agent 与 subagent 本期一并改；旧增量迁移清理，迁移前备份）；③ 首 token 延迟一轮一值、只存 `fb_runs.first_token_ms`（不写 `usage_json`），web 与 TUI 都在 Worked 行显示，live 首 token 到达即显示；④ 两端会话互相可见、可续聊；⑤ **不做**跨端实时推送（刷新/重新进入看到最新即可；owner 先要求实时观看，后撤回）；⑥ 一端在跑时另一端拒绝并提示"正在另一端运行"；⑦ 续聊时工具在会话出生目录执行，目录不存在则拒绝；web 工作台跟随会话目录；⑧ 文件型会话数据（`state/modes`、hook transcript、intermediate、输入历史）入库**暂不做**。期间被否决的方案见"已考虑并拒绝的发现"。数据核实（本机只读）：238 次 subagent 执行引用 222 个 worker 会话，库里只剩 70 个；约 150 次执行的文字只存在于增量中——迁移不能直接删除（019 对这部分改为合并）；cron 调度读取 `turn_completed.Text` 作为定时任务的输出（018 先改读者，再去掉 Text）。
- **同日追加裁决（2026-10-10）**：① 工具输出增量（`tool_output_delta`）本期也只推不存——018 纳入（含在途工具输出由 gateway 内存补发），019 迁移一并清理（有完成事件的删除、没有的按步骤合并；本机 19,394 行、15,425 个步骤，只有 6 个步骤没有完成事件）；判定函数改名 `event.IsPushOnlyStreamEvent`。② 迁移后**不**自动 VACUUM，也不加压缩入口。③ 023 的方案先出、owner 拍板后再实施：评审方已直接完成调研并写出 `docs/plan/SESSION_BIRTH_DIR_CONTINUATION_DESIGN.md`。关键发现：工具执行目录完全由进程当前目录决定（全仓库无 `os.Chdir`），gateway 一个进程跑多个会话，必须引入每轮的"会话工作目录"上下文并按目录建 runner；TUI 推荐"在出生目录重新打开"。附带发现一个**已有缺陷**：gateway 已注册项目会话的 shell 在 gateway 启动目录执行、沙箱/审批取 gateway 启动目录的信任。4 个待决问题（信任、TUI 方案、渠道会话范围、项目会话是否同修）等 owner 回答。
- **项目模型裁决（2026-10-10～11，owner 多轮）**：设计文档已改写为定稿（`docs/plan/SESSION_BIRTH_DIR_CONTINUATION_DESIGN.md` 第 0 节 R1–R10）。要点：每个会话都属于项目，出生目录 = 项目根 = 工作目录；TUI 启动目录即项目根（不找 git 根），自动登记进 `fb_projects`；web 对话前必须选项目或文件夹（文件夹自己没有项目就自动建以它为根的项目，与 TUI 同口径），项目根按用户所选原样；FOREBRAIN_HOME 内不能做项目根/启动 TUI；gateway 启动目录无任何业务语义；信任按项目根自己的；项目级配置去掉"受版本控制"门槛；TUI `/resume` 只列当前项目；渠道/定时任务必选项目；附件上传选目录保存在项目里；老会话迁移归入项目，确定不了的删除。**已执行**：本机 63 个 cwd 被旧版本错写成 agent workspace 的会话按 owner 指示直接在本机真实库删除（2026-10-11，未备份，owner 明确要求）。影响：022、024 需改写；新实现计划 025–031 待写。补充裁决（R11）：去掉项目"归档"，删除项目连同它的对话一起删（目录文件不动）。补充裁决（Q1–Q4，已定）：迁移时目录不存在也照样建项目，只删 cwd 为空的；FOREBRAIN_HOME 内出生的会话只本机有，迁移不写分支；工作坊会话也必须选项目；TUI 粘贴的截图存 `<项目根>/attachments/`。
- **全部计划落笔（2026-10-11，owner 指令"完成所有计划文件的编写，我确认后实施"）**：按项目模型与设计稿第 18 版写完 025–030、033、035–037，改写 022、024（原 031 并入 024 的 D、E 段）；web 改版计划写在 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/015-020`（外壳与页面结构、对话页、工作台、列表加弹窗、文件编辑器、技能与工坊）。同轮 owner 裁决：
  - ① 撤回"迁移后不自动 VACUUM"，照框架行为回收（019 已改）；
  - ② 项目键按项目根本身生成，不再向上找 git 根（025、027）；本机 2 个子目录会话没有计划文件，无需搬；
  - ③ 技能正式目录的改动自动进草稿（036、017）；
  - ④ 旧的主代理级通道配置迁移时删掉（035）；
  - ⑤ 设置去掉"主代理"标签，顶栏"新建主代理"只填名称、建好自动切换，删除在菜单栏下拉里（web 015）；
  - ⑥ 所有页面去掉标题层、减少嵌套（web 015）；
  - ⑦ 文件编辑用 Milkdown Crepe + CodeMirror 6（MIT，web 019）；web 代码高亮一律 monokai-light（web 015/019）。

  写计划时核实的新问题：
  - 迁移在外键关闭下运行，删会话必须逐表显式删除（025）；
  - v0→v1 旧迁移执行当前 schema 并写 `archived_at`，删列时要同步改（025）；
  - 主 agent 的 PermissionRequest 钩子 `cwd` 落到主代理工作区（026 一起修）；
  - TUI 每轮注入的 `project_root` 是 git 根（028）；
  - gateway 无会话模型 / 推理强度接口（web 016）。
- **自动压缩误触发与本地 token 估算一轮（2026-10-11，立计划 038–042）**：owner 报告 deepseek-v4-flash（1M 窗口）续聊从 Claude Code 迁移来的会话，第一句就压缩（卡片 "398.2k → 3.3k"）。
  根因（本机状态库与日志只读核实）：轮前判定以最后一条带 `usage_json` 的 assistant 行为锚点，那是迁移导入的 Claude Code 行（opus，900089 = CC 的系统提示/工具 + 257 个保留的加密 thinking 块 + 图片），≥ 900000 阈值；forebrain 自己的估算只有 398170。
  本机实测 15 次原生换模型，原生锚点 13 次比纯本地估算准，所以判据是"用量是否由 forebrain 自己的请求产生（有 run_id）"而非"模型是否相同"（038）。
  本地估算对所有模型都用 cl100k，增量偏差中位数 deepseek 1.63、glm 1.30。**Owner 裁决**：
  - ① 删除自研 `pkg/llm/hftokenizer.go`，改用 daulet/tokenizers（Rust HF tokenizers 的 cgo 绑定），**所有平台强制链接**：Windows 在 CI 用 Rust 现编，`go install` 不再开箱即用（040）；
  - ② 主流模型（deepseek/glm/gpt/claude/kimi/qwen/minimax/grok/gemini/xiaomi）的分词器文件提交进仓库、随 npm 包分发、首次运行放进缓存目录；先读缓存，缺失才按 HuggingFace → ModelScope 联网下载；内网可拷缓存目录（041）；
  - ③ Claude、Grok、Gemini 无当代公开分词器，全部用近似文件：Claude 2 旧分词器、Grok-2 的 HF 转换版、Gemma 4（041）；
  - ④ **禁止嵌入二进制**：models.json 也改成词典那种随包分发（039）。
  
  评审方补充的处理：
  - MiniMax-M3 是非商用许可，改用词表 id 完全相同、许可为 MIT 的 MiniMax-M2 文件；
  - Grok 转换版已逐个核对 130944 个 token id 与官方一致；
  - 11 个文件的 sha256 在 HF 与 ModelScope 两处一致，已写进 041；
  - daulet v1.27.0 四个平台预编译包的 sha256 与版本符号已核实，写进 040；
  - `${SRCDIR}` 相对路径链接方案已在本机独立模块里验证可用。
- **038–042 复核与代码评审（2026-10-16，`/improve reconcile`，立计划 065–069）**：五份都已 DONE 并随 `9ae6406` 等提交；完成标准逐项在 HEAD `fa4af8a` 上成立（相关测试全绿，`-race` 下 `pkg/llm/...`、`pkg/state` 通过；`pkg/assembly` 的 `-race` 因别的会话引入的 go-pdfium 缺 pkg-config 无法编译，属环境问题）。
  评审发现（均已实测）：
  - ① **内存**：HF tokenizer.json 每个加载后常驻 +92～423MB，默认 gpt-o200k +340MB 且几乎每个进程都会加载（本机 glm 会话的 TUI 实测 590MB）。字节级 BPE 改用 tiktoken 格式加载只需 +39～83MB、计数快约 3 倍；gpt-o200k、grok-2 与原版逐 token 一致，glm/qwen/minimax/mimo 在 630 万 token 语料上只差 3–37 token。**Owner 裁决：转换这 6 个**（065）；
  - ② **性能**：每次 LLM 调用对整段历史重复计数 3 遍以上，65 万 token 历史约 1s/调用（066）；
  - ③ **配置**：`hubs: []` 在示例配置里写作"默认两个 hub"，实际禁止下载，保存一次后语义又翻回；附带日志误报、告警刷屏、相对路径、失败不可重试（067）；
  - ④ **脚本**：`fetch-libtokenizers.sh` 在 bash 3.2 下空数组 + `set -u` 退出、`MINGW` 判断永不成立（068）；
  - ⑤ **口径**：观测开销不分估算器；压缩内部与上下文面板仍用默认估算器（069）。
  
  另见（不在 038–042 范围，未立计划）：`pkg/state` 的 `TestLastActiveByIDs` 在 `-race` 下偶发失败（时间戳只精确到秒）。
  **执行结果（2026-10-16）**：065–069 全部 APPROVE，经执行者在基于 dev 的 worktree 中实施，评审方逐个复跑完成标准后以 `git apply` 并回主树工作区（dev `7155be9`，未提交；`graph.json` 每次在主树重新生成）。worktree 起初误从 main（`bd2b8b8`）建出，已切到 dev；owner 要求今后一律从 dev 建 worktree。065 计划误把计数差异归因于 merges 顺序，实为 tokenizer.json 把正文里的 added token 字面量当单个 token（量级不变）。

  035 的通道凭据存储 D-035-1：owner 已选方案 A（状态库新表 + `.env` 引用）。
- **上上轮（004）**：多 TUI 进程 plan 模式计划文件串台
- **Owner 裁决（004）**：隔离方案选「每会话子目录」`plans/<projectKey>/<sessionID>/`（评估过保持平铺的后缀命名 / 绑定文件名 / SQLite 归属表后仍维持子目录）；旧会话不迁移（扁平目录里的旧计划只作历史）；`/migrate` 导入的计划进入所属对话的目录，一个 Claude slug 对应多个会话时复制到每个会话；删会话不碰计划目录
- **上一轮（003）**：TUI message queue 在途 steer 无法撤回；owner 裁决：压缩窗口选「压缩不吞未送达 steer」；「清空队列对在途 steer 不撤回」和 web/gateway 同类问题并入本期一起修，web 送达的 steer 实时拆成独立气泡

## 计划列表

| # | 标题 | 类别 | 状态 | 优先级 | 工作量 | 风险 |
|---|------|------|------|--------|--------|------|
| [001-fix-message-recall-after-detach](001-fix-message-recall-after-detach.md) | 修复 runtime detach 后消息无法撤回 | Bug Fix | DONE | **HIGH** | S | LOW |
| [001-frontend-component-design-spec-alignment](001-frontend-component-design-spec-alignment.md) | 前端通用组件样式规范对齐 | Design System | TODO | HIGH | M (4-6h) | MEDIUM |
| [002-subagent-view-input-freeze-ledger-read](002-subagent-view-input-freeze-ledger-read.md) | subagent 视图的 composer 重绘不再整读 subagent 账本（修复快速滚轮后输入卡死） | Bug Fix / Perf | DONE | **P1** | S | LOW |
| [003-recall-in-flight-steer](003-recall-in-flight-steer.md) | 队列里显示的 steer 在模型开始回答前随时能撤回、清空时一并撤走，web/gateway 与 TUI 一致 | Bug Fix | DONE | **P1** | L | MED |
| [004-session-scoped-plan-files](004-session-scoped-plan-files.md) | 计划文件按会话隔离：多个 TUI / 会话同时在 plan 模式时 exit_plan_mode 审批不再显示别人的计划 | Bug Fix | DONE | **P1** | M | MED |
| [005-serialize-run-queue-hook-and-drop-retract-last-steer](005-serialize-run-queue-hook-and-drop-retract-last-steer.md) | gateway 的 run 队列 hook 一次只发一个变化；删掉已无人使用的 `RetractLastSteer` | Bug Fix / Tech Debt | DONE | P3 | S | LOW |
| [006-shared-compaction-guard-and-unseparable-provisional-tail](006-shared-compaction-guard-and-unseparable-provisional-tail.md) | 排查 Runner 共享的压缩 guard 会否被同 run ID 的另一段历史覆盖；不可达则让分不清暂定尾巴的调用不压缩。**SUPERSEDED（2026-10-14）：排查已交付、决策闸门判定「可达」；修复拆为 044（fork 续跑的 run 身份）、045（guard 按历史多槽）、046（分不清尾巴就不压缩，承接本计划的 Step 5–7）** | Investigation | SUPERSEDED | P3 | S | LOW |
| [007-exit-gate-live-card-and-cardless-anchoring](007-exit-gate-live-card-and-cardless-anchoring.md) | exit_plan_mode 审批期间不再画假 "Exited plan mode" 卡、取消后不再多 "Canceled" 卡；无卡 gate 的确认行与 review 卡按真实顺序落位（live 与回放一致） | Bug Fix | DONE | **P1** | M | MED |
| [008-web-cardless-exit-gate-anchoring](008-web-cardless-exit-gate-anchoring.md) | web 端 exit gate 的决定行逐条显示、按真实顺序落在 gate 调用处，review 卡跟在请求它的行之后；live 与重载一致（对齐 007 后的 TUI） | Bug Fix | DONE | P2 | M | MED |
| [009-replay-seals-response-before-cardless-gate](009-replay-seals-response-before-cardless-gate.md) | 回放在发出无卡 exit gate 调用的行之后补喂 running 步骤、封存那段回答（修 review 交付流程回放文本重复 / 粘连）；收紧 007 test 8；勘误段放回原位 | Bug Fix | DONE | **P1** | S | LOW |
| [011-gateway-approval-request-names-gated-call](011-gateway-approval-request-names-gated-call.md) | gateway 的 `approval_requested` 带上被审批调用的 `tool_step_id`（与 TUI 同一推导 `turn.ApprovalToolStepID`），web 发起的会话重载后 review 卡不再落到整轮末尾 | Bug Fix | DONE | P2 | S | LOW |
| [012-engine-emits-settled-step-for-refused-calls](012-engine-emits-settled-step-for-refused-calls.md) | 用户拒绝的调用由运行时发出 settled "denied" 步骤（`:denied` 事件边界防吞），落库显示与之同源；web live 补上 "Kept planning"/denied 卡，TUI 只落库不重复画 | Bug Fix | DONE | P2 | M | MED |
| [013-chatview-blocks-that-draw-nothing-yield-to-content](013-chatview-blocks-that-draw-nothing-yield-to-content.md) | ChatView 只在有可见块时走块分支；`hasVisibleBlocks` 不再把锚点与无确认行的审批块算作可见（修空气泡） | Bug Fix | DONE | P3 | S | LOW |
| [014-tui-persists-main-agent-turn-lifecycle](014-tui-persists-main-agent-turn-lifecycle.md) | TUI 主 agent 的 `turn_started`/`turn_completed` 落库（不带回答文字），取消/失败事件带计划进度；计划进度计算下沉到 `pkg/turn` | Bug Fix | DONE（2026-10-14 执行完毕） | **P1** | S | LOW |
| [015-first-token-latency-per-turn](015-first-token-latency-per-turn.md) | 首 token 延迟：引擎在 `Submit` 测一次，存 `fb_runs.first_token_ms`（加列+迁移），两端运行中状态行与 Worked 行显示 | Feature | DONE（2026-10-14 引擎、迁移 v13、gateway 与 web 已提交 main；TUI 侧显示因 020 占用 pkg/tui 未做，待串行补齐；2026-10-15 已补齐落地：Submit 经 FirstTokenStore 上报，TUI 收 FirstTokenMsg，运行中状态行与 live Worked 行显示该值，回放从 fb_runs 读，实测 live/replay 同为 0.2s）| P2 | M | MED |
| [016-transcript-written-at-text-block-end](016-transcript-written-at-text-block-end.md) | 回答/思考在文字块结束时写入 transcript（引擎层，两端与 subagent 共用，幂等对账）；每个思考块单独一行并记录耗时 | Bug Fix | DONE（2026-10-13 执行完毕）| **P1** | M | MED |
| [017-subagent-views-from-worker-transcript](017-subagent-views-from-worker-transcript.md) | subagent 视图（web 与 TUI）重载/回放时从 worker 会话 transcript 重建文字；worker 会话缺失时退回事件重放 | Bug Fix | DONE（2026-10-13 执行完毕） | **P1** | L | HIGH |
| [018-stream-deltas-pushed-not-persisted](018-stream-deltas-pushed-not-persisted.md) | 回答/思考/工具输出增量只推送不落库；持久化的生命周期事件不带回答文字（cron 改读 transcript）；断线重连与刷新时 gateway 从内存补发在途文字与在途工具输出 | Bug Fix | DONE（2026-10-13 执行完毕） | **P1** | L | HIGH |
| [019-migrate-away-stored-stream-deltas](019-migrate-away-stored-stream-deltas.md) | 状态库迁移：先备份，再处理旧增量（含工具输出）——已有完整记录的删除，没有的合并成段；迁移后照框架自动 VACUUM（owner 2026-10-11 撤回"不回收"） | Migration | DONE（2026-10-14 执行完毕） | P2 | M | HIGH |
| [020-tui-persists-main-agent-state-events](020-tui-persists-main-agent-state-events.md) | TUI 主 agent 的 turn diff、预算、队列、steer 送达、模式事件落库；gateway 审批续跑补上 turn diff 伴随事件 | Bug Fix | DONE（2026-10-14 执行完毕，未提交） | P2 | M | LOW-MED |
| [021-busy-session-names-the-other-surface](021-busy-session-names-the-other-surface.md) | 会话被另一端（另一个进程）运行或停在审批上时，按 `fb_runs.owner` 判断并提示"正在另一端运行" | Bug Fix | DONE（2026-10-13 执行完毕） | P2 | S | LOW |
| [022-workbench-files-follow-session-directory](022-workbench-files-follow-session-directory.md) | web 文件视图（树、片段、`@` 补全、`@` 图片）以会话所属项目的根为准，主代理级工坊为主代理技能目录；新对话按所选项目（2026-10-11 改写） | Bug Fix | DONE（2026-10-12 执行完毕，未提交；评审 APPROVE） | **P1** | M | MED |
| [023-continue-in-session-birth-directory-design](023-continue-in-session-birth-directory-design.md) | 调研与方案：续聊时在会话出生目录执行（gateway runner 池 + TUI 单 runner），产出设计文档，信任处理交 owner 拍板 | Design | DONE（定稿设计已写，所有待决项已定；下一步写实现计划 025–031） | **P1** | M | — |
| [024-cross-surface-see-and-continue-acceptance](024-cross-surface-see-and-continue-acceptance.md) | 验收：两端双向看到并续聊、另一端在跑时拒绝、工具在项目根执行、信任按项目根、子目录项目、工作台根（2026-10-11 改写，并入原 031） | Tests | DONE（2026-10-14 验收执行完毕，fake provider；A–F 全过，无源码改动） | **P1** | M | — |
| [025-projects-own-every-session-migration](025-projects-own-every-session-migration.md) | 状态库：老会话归入项目（cwd 为空的删除）、项目根唯一、去掉归档、删除项目连同对话、项目键按项目根本身 | Migration | DONE（2026-10-11 执行完毕，未提交）| **P1** | M | HIGH |
| [026-working-dir-travels-with-every-turn](026-working-dir-travels-with-every-turn.md) | 每一轮带上项目根，工具、权限、规则、git 上下文都在项目根执行，不再读进程目录；修 PermissionRequest 钩子 cwd | Bug Fix | DONE（2026-10-11 执行完毕，未提交）| **P1** | M–L | MED |
| [027-every-session-runs-on-its-project-runner](027-every-session-runs-on-its-project-runner.md) | 每个会话跑在自己项目的 runner 上、按项目根自己的信任推导沙箱与审批、gateway 启动目录去语义、去掉"受版本控制"门槛、工坊 runner | Bug Fix / Security | DONE（2026-10-11 执行完毕，未提交）| **P1** | L | HIGH |
| [028-tui-launch-dir-is-the-project](028-tui-launch-dir-is-the-project.md) | TUI 启动目录登记为项目、数据目录内拒绝启动、`/resume` 只列本项目、跨项目 resume 拒绝、粘贴截图存进项目 | Feature | DONE（2026-10-11 执行完毕，未提交）| **P1** | M | MED |
| [029-web-chat-picks-a-project-first](029-web-chat-picks-a-project-first.md) | web 对话前必须选项目或文件夹：新对话页、项目胶囊、系统式文件夹对话框、信任提示；会话只能带项目创建 | Feature | DONE（2026-10-11 执行完毕，未提交）| **P1** | L | MED |
| [030-attachments-save-into-the-project](030-attachments-save-into-the-project.md) | web 附件"保存到"项目里选的目录，记住上次目录 | Feature | DONE（2026-10-13 执行完毕：附件保存到项目目录、记住上次目录、`/api/projects/:id/dirs`）| P2 | M | LOW–MED |
| [033-rewind-to-before-a-message](033-rewind-to-before-a-message.md) | 回到这里：文件工具改动按会话记录、三种恢复方式、可撤销；web 与 TUI `/rewind` 共用；"变更文件"数据源；删 rewind-last | Feature | DONE（2026-10-12 执行完毕，未提交；评审 APPROVE） | **P1** | L | MED |
| [035-channels-and-cron-belong-to-a-project](035-channels-and-cron-belong-to-a-project.md) | 通道与定时任务属于项目；进来的对话出生在项目根；旧主代理级通道配置迁移时删掉 | Feature | DONE（2026-10-14 执行完毕并提交 main；`fb_project_channels` v14、`project_id` 必填、`/projects/:id/{cron,channels}` 路由；设置里的"定时任务"（保留天数全局设置）按 STOP-2 保留原地待 owner 决定）| P2 | L | MED |
| [036-skill-drafts-publish-and-versions](036-skill-drafts-publish-and-versions.md) | 技能草稿 / 发布 / 版本：存储、发布原子、会话按版本加载、正式目录改动自动进草稿、gateway 接口 | Feature | DONE（2026-10-12/13；改动已落地并复跑全绿，评审待补：执行者运行被限额中断，无交付报告） | **P1** | L | MED |
| [037-tui-skills-panel-publish-and-versions](037-tui-skills-panel-publish-and-versions.md) | TUI `/skills` 面板：状态段、随状态变化的子菜单、发布、查看改动、版本记录（分页）、恢复、拉取更新 | Feature | DONE（2026-10-13 执行完毕，未提交）| **P1** | M | LOW–MED |
| [032-remove-working-set-tools](032-remove-working-set-tools.md) | 删除 working_set_* 工具与 WorkingSetSource（模型从未调用），连带 TUI 显示、安全白名单、上下文 API 字段与 web 面板区块 | Tech Debt | DONE（2026-10-13 执行完毕，未提交）| P2 | S | LOW |
| [034-composer-pickers-anywhere](034-composer-pickers-anywhere.md) | TUI 与 web 输入框任意位置输入 / 和 @ 都能呼出选择器；中间的 / 只列技能，发送时加载（一轮可多个）；两端共用一份记号用例表 | Bug Fix / Feature | DONE（2026-10-14 执行完毕）| **P1** | M | MED |
| [038-pre-turn-anchor-only-trusts-own-usage](038-pre-turn-anchor-only-trusts-own-usage.md) | 上下文占用只用 forebrain 自己请求（有 run_id）的用量当锚点，迁移会话续聊第一句不再被误压缩；压缩卡片显示判定用的数 | Bug Fix | DONE（2026-10-13 执行完毕；评审 APPROVE；已随 `9ae6406` 提交。2026-10-16 复核：HEAD `fa4af8a` 上 11 个相关测试 PASS、`TestEmptyReplacementMustNotPersistDeadCheckpoint` 无 SKIP）| **P1** | S | LOW |
| [039-models-json-ships-beside-binary](039-models-json-ships-beside-binary.md) | models.json 不再嵌入二进制：随 npm 包 / 归档 / 镜像装在二进制旁（`install-assets.sh`），首次运行种入 home；go install 发行版缺文件时下载 | Packaging | DONE（2026-10-13 执行完毕，未提交）| **P1** | M | MED |
| [040-link-hf-tokenizers-library](040-link-hf-tokenizers-library.md) | 所有平台强制链接 daulet/tokenizers v1.27.0（`fetch-libtokenizers.sh`，Windows 用 Rust 现编），建 `pkg/llm/hftok` 包装 | Dependency / Build | DONE（2026-10-13 执行完毕，未提交）| **P1** | L | HIGH |
| [041-tokenizer-assets-and-per-model-estimator](041-tokenizer-assets-and-per-model-estimator.md) | 10 个家族的分词器文件随包分发、缓存优先、缺失按 HF→ModelScope 下载；按模型选分词器；删除 `hftokenizer.go` 与 tiktoken-go | Feature / Tech Debt | DONE（2026-10-13 执行完毕，未提交）| **P1** | L | MED |
| [042-compaction-estimates-with-session-model-tokenizer](042-compaction-estimates-with-session-model-tokenizer.md) | 轮前 / 轮中压缩判定与用量观测按本次调用所用模型的分词器估算（同口径）；glm-5.3 真机验收 | Bug Fix | DONE（2026-10-13 执行完毕）| P2 | M | MED |
| [044-fork-continuation-owns-its-run](044-fork-continuation-owns-its-run.md) | fork 续跑（`subagent_continue`）在自己的 run 身份下发请求：补上首跑已有、续跑漏掉的 run ID，压缩 guard / 审批队列 / 等待行不再落到派发者的 run 上（006 闸门后的修复之一） | Bug Fix | DONE（2026-10-14 执行完毕，未提交；主线程独立复验：五包 ok + 新回归 PASS） | P2 | S | LOW |
| [045-compaction-guard-per-history](045-compaction-guard-per-history.md) | 压缩 guard 按 (run ID, worker 会话) 多槽：一段历史的 checkpoint 只属于它自己（探针 A 的跨历史拼接、`body_after_prefix` 基线互相清空） | Bug Fix | DONE（2026-10-14 执行完毕，未提交；主线程独立复验） | P2 | S | MED |
| [046-unseparable-provisional-tail](046-unseparable-provisional-tail.md) | 分不清暂定尾巴的调用不压缩（fail-closed），且尾巴归属按历史判定（子代理继承父调用尾巴时照常压缩）；依赖 045 | Bug Fix | DONE（2026-10-14 执行完毕，未提交。**执行者通道不可用（`subagent_run` 从本轮工具集中消失），改由评审方在主线程按计划实施**；证据：新回归修复前 FAIL（`compaction 0 folded the provisional steer into its checkpoint`）→ 修复后 PASS，护栏测试前后均 PASS，`grep -c "compaction skipped: provisional"` = 1，`pkg/run -race` ok，gofmt/vet 干净） | P2 | S | LOW |
| [047-dictionaries-are-a-home-install](047-dictionaries-are-a-home-install.md) | 分词字典成为 home 级安装件：产品**启动时播种**进 `$FOREBRAIN_HOME/dict`（与 models.json 同路），本地开发（打包＋npm link）与 `npm install` 两条路径都开箱即用 memory search；安装脚本不写 home / GOBIN，打包脚本只校验产物带全字典 | Bug Fix / Packaging | DONE（2026-10-14 执行完毕，未提交；三条验收链路主线程独立复跑 PASS；**执行期发生真实 home 事故，见本文件顶部审核信息**） | **P1** | L | MED |

| [048-v11-purge-is-linear-and-indexed](048-v11-purge-is-linear-and-indexed.md) | 状态库 v10→v11 清理线性化 + 迁移局部表达式索引 + 分步进度日志（修「大库启动卡死数小时」；主批游标单调前进、工具输出语句全部走索引；语义与计数逐字不变） | Perf / Migration | **SUPERSEDED**（2026-10-14 复评：实测 `main_agent_rows_deleted=0`、迁移自行在 93 分钟内跑完，「平方级/天数级」量级估算被推翻；保留的部分拆为 052） | ~~P1~~ | M | MED |
| [049-migration-connection-pragmas-and-space-preflight](049-migration-connection-pragmas-and-space-preflight.md) | 迁移连接带上 128MiB 页缓存与 64MiB WAL 限额（结束即还原），预检按「备份 + WAL」= 库大小 2.2× 计算并在拒绝前记录空间快照 | Perf / Migration | **REJECTED**（2026-10-14 复评：实测 1.1× 预检够用、`-wal` 随连接关闭自行清理、迁移瓶颈是行级删除而非缓存/IO；理由见「已考虑并拒绝的发现」） | ~~P2~~ | S | LOW |
| [050-reclaim-and-backup-retention-policy](050-reclaim-and-backup-retention-policy.md) | 只有回收量 ≥ 64MiB 或 ≥ 页数 10% 才跑尾部 VACUUM（跳过时记日志）；迁移成功后同一来源版本的旧备份只留最新一份（严格模式匹配，不碰用户文件） | Perf / Migration | **REJECTED**（2026-10-14 复评：回收实测 54 秒内完成、库已缩到 1.6GB、遗留备份已被 owner 清掉，阈值与保留策略失去标的） | ~~P2~~ | S–M | LOW–MED |
| [051-rewind-visibility-predicate-names-the-session](051-rewind-visibility-predicate-names-the-session.md) | rewind 可见性谓词的会话项限定为外层表（今天是 `rw.session_id = session_id` 恒真式 → 一个会话的 rewind 隐藏所有会话 sequence 更大的事件；附带让 `idx_fb_rewinds_session_state` 重新可用）**（本轮审计副产物，非性能范围）** | Bug Fix | DONE（2026-10-14 执行完毕，并**已按 owner 要求并回主工作区**——`git apply`，**未提交**，仓库惯例；分支 `advisor/051-rewind-predicate`（commit `39c1a5c`）保留作复核记录。并回后主树复核：3 个文件 +78/−25、`pkg/state` 全套 `ok`、两个 rewind 测试 PASS、vet/gofmt 干净；EXPLAIN 由 `SCAN rw` 变为 `SEARCH rw USING INDEX idx_fb_rewinds_session_state`） | **P1** | S | LOW |
| [052-v11-migration-progress-logs](052-v11-migration-progress-logs.md) | v10→v11 每个阶段留进度日志（起始 + 主清理每批 + 每 25 个执行体 + 每 200 个 step）：93 分钟零输出不再被当成卡死。只加日志，清理语义不动 | DX / Perf | **REJECTED**（owner 2026-10-14 裁决不做：价值只在「下一个大库」的首次升级上，而迁移已在唯一已知的受影响库上完成。方案原样保留在本文件，路径若再次被走到可直接复活） | ~~P3~~ | S | LOW |
| [053-read-images-as-pixels](053-read-images-as-pixels.md) | 模型能读图（本期第一部分）：**修正 catalog 里 `deepseek/deepseek-v4-flash` 的 `attachment`（Step 4c，owner 确认收图；不修则闸门会拦住 owner 默认模型）**；`read_file` 对图片返回「一行说明 + image part」而不是拒读；**MCP 工具结果的图片透传**（`ToolResultContent`/`CallToolContent`，`pkg/run` 签名加宽到 `any`）＋**mcp-results 搬进主代理 workspace**（Step 5b：owner 裁决「全局/主代理/项目级三层的 mcp-results 都必须放在主代理 workspace 范围内」；主代理 workspace 本就是读豁免根，所以 `ProtectedReadReason` 一个字不改，审批这一环自然消失；旧 `$FOREBRAIN_HOME/state/mcp-results` 不写迁移）；**surface 出图**（web 缩略图 + `/v1/workspace/file/raw`；TUI 标记行 + `o` 键）；**能力闸门** `ImageInputRefusal`（fail-open）；另钉住用户附件通道 | Bug Fix（能力缺口） | **DONE**（2026-10-15 执行完毕 + 评审通过，**未提交**，改动留在工作区；Step 6 真机探测=结论 A、Step 9 真机验收 `read image: PASS`；详见上方「最新审核信息」） | **P1** | L | HIGH |
| [054-pdf-pages-as-images-with-cjk](054-pdf-pages-as-images-with-cjk.md) | 模型能读 PDF（本期第二部分）：PDFium（通过 `klippa-app/go-pdfium` v1.21.3 原生/cgo 后端）+ 自带 Noto Sans SC/TC 经 `LibraryConfig.UserFontPaths` 喂字体 → 前 5 页渲染成 PNG 交给模型；**中文字体是硬约束**，核心验收是机器可判定的非-tofu 测试；含 5 平台 fetch/打包接线与 `pkg/pdf` 新包 | Bug Fix（能力缺口）+ Feature | **DONE**（2026-10-15 执行完毕 + 评审通过，**未提交**；Step 4 非-tofu 测试 10/10 + 检测器红例、Step 7 真机 `read pdf: PASS`（内嵌/不内嵌各 7 条断言）；owner 已放宽 `pkg/tool` fan-out 8→9；linux/windows 目标待 CI 验证；详见上方「最新审核信息」） | **P1** | XL | HIGH（新原生依赖 + 跨平台打包） |
| [055-image-pixel-and-pdf-budgets](055-image-pixel-and-pdf-budgets.md) | 补齐图片/PDF 管道的**像素与输入预算**：`image.Decode` 前加 ~64 MP 上限（3.1–48 MiB 伪造头 PNG = ~14 GB 分配 → 进程级 OOM，可达路径是模型 read_file 与恶意 MCP 图片块）；PDF 渲染前按 `FPDF_GetPageSizeByIndex` 算像素并拒绝超限页（14400pt@150dpi = 3.6 GB）；修活 `FOREBRAIN_IMAGE_MAX_BYTES`（三个生产调用方全传死常量，env 永不生效）；`RenderFile` 改吃已读字节消灭整文件双读 + 128 MiB PDF 输入上限 | Security / Correctness / Perf | **DONE**（2026-10-16 执行+评审 APPROVE，未提交） | **P1** | M | MED |
| [056-windows-pdfium-link-and-ci-fetch](056-windows-pdfium-link-and-ci-fetch.md) | 修通 **Windows PDFium 链接（合并阻断项）**：归档只有 MSVC 命名 `lib/pdfium.dll.lib`，GNU ld 的 `-l` 搜索序列永远找不到它 → required 的 `windows build test (CGO)` 必红；fetch 脚本 windows 分支补 GNU 命名别名（幂等路径同样补齐）+ `.pc` prefix cygpath 转换；CI 拉取加 `GITHUB_TOKEN`（匿名 api.github.com 403 已现场复现）与元数据 `--retry` + 主 job 缓存。验证走 owner 批准的 Docker mingw 容器红/绿 → CI windows job 权威确认 | CI / Build | **DONE（代码）**（2026-10-16；Step 4 CI 确认待 owner push——变绿前提＝056 别名 + 064 分组） | **P1** | S/M | LOW |
| [057-tool-image-file-references](057-tool-image-file-references.md) | 把 base64 赶出库与 messages API：read_file/PDF/MCP 三条路的图片 part 今天以全量 base64 落 `parts_json`（每图 ~4 MB）并随 `/api/chat/sessions/:id/messages` 原样外带，违反分支自身"no event or JSON response ever carries base64"不变量。改造：`ContentPart.SourcePath` → 持久化为既有 `file_reference`（file_id=路径）→ rehydrate 扩展到工具行并**用同一 `ImagePartWithin` 阶梯重推**（确定性阶梯保证与 live 轮次逐字节一致——replay/live 1:1 与缓存前缀铁律）；存量 base64 行不迁移、读路径兼容 | Correctness / Storage | **DONE**（2026-10-16 执行+评审 APPROVE，未提交；遗留：存量工具行 base64 保持原样读取，新行落 file_reference） | **P1** | M/L | MED-HIGH |
| [058-image-mime-and-raw-endpoint](058-image-mime-and-raw-endpoint.md) | 图片 MIME 嗅探收紧 + raw 端点会话归属：`ImageMIMEType` 对**非图片**嗅探结果（text/plain 等）仍回退扩展名——文本字节命名 `.png` 被直发 provider（不透明 400）且让 raw 端点 415 门按扩展名放行任意工作区文件；改为嗅探全权（四种可发送格式合法文件必过魔数嗅探）。`resolveRawImagePath` 先过 `sessionOwned`，不再把 foreign session_id 吞成"无文件视图"后仍以整个 agent workspace 为根服务绝对路径 | Security / Correctness | **DONE**（2026-10-16；STOP-2 处置：钉住缺陷的用例 `text content, png ext` 期望改 ""，测试改名 SniffIsAuthoritative——owner 已批准嗅探全权语义） | **P1** | S/M | LOW-MED |
| [059-migrate-project-binding-followups](059-migrate-project-binding-followups.md) | migrate/空会话过滤收尾（建立在未提交的两计划实施之上）：IsChild 单元绑定以**父**为准（子 transcript 自带 cwd 时今天会绑离父项目，与 v9 先例/注释/报告 ↳ 承诺相悖，claude+codex 双侧）；`DeletePreview` 计数对齐列表谓词（"将同时删除 N 个对话"不再把空壳行计成对话）；`OnlyProject`+空 root 在 Run 入口统一报错（codex 侧今天静默关过滤器并误诊"无目录"）；源 transcript 已删的存量 NULL 行在报告点名 + dry-run 对 rebind 候选的只读性用快照测试钉住 | Correctness / Tests | **DONE**（2026-10-16 执行+评审 APPROVE，未提交） | **P2** | S/M | LOW-MED |
| [060-mcp-invalid-base64-degrade](060-mcp-invalid-base64-degrade.md) | MCP 图片块 `data` 为非法 base64 时今天走裸 `continue`：块原样保留 → 结果 JSON 带 `data` 被 `resultJSONContainsImage` 改判截断——**垃圾 base64 作为文本到达模型**，打穿"pixels to the model, text stays clean"保证。改为与 oversize/persist 失败同款的降级文本行 + `changed=true`（工具结果与资源 blob 两条路径），无截断、不落盘、其余块不受累 | Correctness | **DONE**（2026-10-16 执行+评审 APPROVE，未提交） | **P2** | S | LOW |
| [061-tui-image-marker-fixes](061-tui-image-marker-fixes.md) | TUI 图片标记行两个小修：①可折叠图片卡（长文件名换行 >3 行）的标记行点击**既折叠又开图**——`toggleAtScreenRow` 对 FrameTool 全 body 可点击，排除标记行使其"只开图"，提示行维持"只折叠"（含 `ViewportSelectEnd` 返回语义的先核实步骤）；②`imagePreviewFrameLine` 无条件印 `0×0`，对齐 surface 契约与 web 卡的"两值 >0 才拼段" | Correctness / TUI parity | **DONE**（2026-10-16；Step 1 按 owner 终裁实施：**图片卡 output 区域取消折叠、默认全量展示**——`frameCollapsible` 对 image 预览卡恒 false（tty_session.go 三个接线点含 replaceOrAppendBlock 原地换帧），标记行点击只开图、永不折叠，既有折叠测试按新语义重写；Step 2：0×0 省略） | **P2** | S | LOW |
| [062-docs-reality-alignment](062-docs-reality-alignment.md) | 文档对齐 pdfium 现实：README/CONTRIBUTING 的 Requirements 补 `pkg-config`+`jq`、day-to-day 裸 `go build`/vet 块补 `make pdfium pdfium-fonts` 与 `PKG_CONFIG_PATH`；Makefile `PKG_CONFIG_PATH` 未设时的尾随空 entry；归档脚本头注释补 fonts/libpdfium；plans/README:16「check-go-install 均已接线」改如实陈述；RELEASING re-tag 段不再断言"proxy 已记录"（go-install 管线已删）；6d1730e（主题"…"）的"go install 下架"决定写成 PR 描述补记文案 | DX / Docs | **DONE**（2026-10-16；附根因修复：make pdfium 在 HEAD 因下划线三元组必败，已改裸脚本调用） | **P2** | S | LOW |
| [063-attachment-image-budget](063-attachment-image-budget.md) | 用户附件/粘贴路径统一走图片预算（owner 2026-10-16 拍板：附件与工具结果同契约）：`llm.ImageFile` 内部改走 `ImagePartWithin(…, 0)`——≤预算（默认 3 MiB，`FOREBRAIN_IMAGE_MAX_BYTES` 可调）**逐字节直发**、超预算走既有确定性阶梯、>48 MiB 可执行拒绝；live 与 rehydrate 同函数保证 resume 后 part 逐字节一致；存量超大附件会话升级后有一次缓存前缀位移（如实记录，不做兼容开关） | Correctness / Cost | **DONE**（2026-10-16；遗留：存量超大附件会话升级后一次缓存前缀位移，如实记录不修） | **P2** | S/M | MED |
| [064-windows-ntdll-link-order](064-windows-ntdll-link-order.md) | **执行期新发现**（056 容器复现暴露）：`pkg/llm/hftok/link.go` 的 `-lntdll` 先于 daulet 模块的 `-ltokenizers`，Rust≥1.87 构建的 libtokenizers.a 引用 7 个 NT 符号（NtCreateFile…），GNU ld 左到右解析 → Windows 链接必失败（此前被 `cannot find -lpdfium` 掩盖，056 修好后必然浮出）。修复：windows LDFLAGS 改 `--start-group/-ltokenizers -lntdll …/--end-group` 分组包裹；darwin/linux 旗标逐字节不动 | Build / Correctness | **DONE**（2026-10-16 执行+评审 APPROVE，未提交；容器红/绿 + objdump 双 DLL import + 评审侧挂宿主模块缓存独立复验；**Windows CI 变绿前提＝056+064**） | **P1** | S/M | MED |
| [065-tokenizers-as-tiktoken](065-tokenizers-as-tiktoken.md) | gpt-o200k、glm-5、grok-2、qwen-3.8、minimax-m2、mimo-v2.5 改以 tiktoken 格式随包分发与加载（常驻内存降到约 1/5、计数快约 3 倍）；下载回退取源 tokenizer.json 本地转换；清理旧缓存文件 | Perf / Resource | DONE（2026-10-16 执行完毕，评审 APPROVE；worktree `.claude/worktrees/agent-a757f0d0c2c046bc0`（detached 于 dev `fa4af8a`）；**已与 066 一起 `git apply` 并回主树工作区（dev `7155be9`），未提交**，主树复跑 10 个包全绿、`graph.json` 按主树重新生成。端到端实测 glm 会话 + 默认估算器常驻约 520MB→约 170MB；STOP 4 系语料混入新测试文件的特殊 token 字面量所致，原始语料与计划表一致；计划把差异原因误写为 merges 顺序，实为 tokenizer.json 把正文里的 added token 字面量当单个 token，已在代码注释与 assets README 改正）| **P1** | M | MED |
| [066-memoize-token-counts](066-memoize-token-counts.md) | 估算器按内容记忆计数；prefill 基线只在需要时才算（长会话每次 LLM 调用省约 1s CPU） | Perf | DONE（2026-10-16 执行完毕，评审 APPROVE；基准 2.36MB 历史首遍 91ms→第二遍 2.9ms；**已与 065 一起并回主树工作区，未提交**）| **P1** | S | LOW |
| [067-token-estimate-config-semantics](067-token-estimate-config-semantics.md) | `hubs` 为空即默认、`download: false` 关闭下载、覆盖路径必须绝对、日志如实、废弃键只告警一次、失败可在重载后重试 | Bug Fix / Config | DONE（2026-10-16 执行完毕，评审 APPROVE；**已并回主树工作区（dev `7155be9`），未提交**；偏差：新增 `pkg/run/runner_test.go`、映射提为 `tokenEstimateOptions`，均接受）| **P1** | S | LOW |
| [068-fetch-libtokenizers-shell-fixes](068-fetch-libtokenizers-shell-fixes.md) | `fetch-libtokenizers.sh` 在 bash 3.2 下空数组不再退出；Windows 编译器提示的 `MINGW` 判断真正生效 | Bug Fix / Build | DONE（2026-10-16 执行完毕，评审 APPROVE；worktree `.claude/worktrees/agent-aacb0c98b12b26520`（detached 于 dev `fa4af8a`）；**已按 owner 2026-10-16 指示 `git apply` 并回主树工作区（dev `7155be9`），未提交**）| P3 | S | LOW |
| [069-estimator-consistency-in-compaction](069-estimator-consistency-in-compaction.md) | 观测开销按估算器分开；压缩内部（摘要预算、裁剪、卡片数字）与上下文面板用会话模型的估算器 | Correctness | DONE（2026-10-16 执行完毕，评审 APPROVE；**已并回主树工作区（dev `7155be9`），未提交**；新增 `pkg/assembly/hook_test.go`，旧测试改读 `ProjectFor`，均接受）| P2 | M | LOW–MED |

## web 实现与设计稿的偏差（owner 2026-10-11：必须修，实施须 1:1 还原设计稿）

基准设计稿：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`（2026-10-11 已按项目模型迭代，原型 https://claude.ai/artifact/KKfPW8dXWScuRdTzonsk2F ，待 owner 确认）。web 端任何改动先在该稿上迭代、owner 确认后再写计划。已知偏差（owner 截图指出），待全量对照后单独立项：

1. 心跳、工作台按钮：实现放在顶栏下方单独一行；设计稿在顶栏、语言按钮左侧。
2. 输入框上方的“会话上下文、待办与运行诊断”面板：设计稿没有；本轮重新设计并移进工作台（概览 / 上下文 / 工具调用 / 文件四个标签）。
3. MCP 启动失败：实现作为一条红色回答出现在历史记录最下方；MCP 只在新开对话时启动一次，设计稿改为对话开头的提示，原始错误收进“查看详情”，工作台概览同步状态。
4. 消息窗口：用户消息、思考、所有工具调用卡片必须实时流式显示，与终端一致；工作台不重复展示工具调用（owner 2026-10-11）。待核对实现是否有缺口。
5. 现有"撤销最近一次文件改动"（`/api/chat/sessions/:id/rewind-last`）只恢复一个文件、取的是不分会话的最新快照、不可撤回、终端没有对应功能；设计稿改为用户消息上的"回到这里"（owner 已定：只记文件工具改动、对话回退点保留数据可撤销、TUI 用 `/rewind`），见设计文档 2.10，实现计划 033 待写。
6. 菜单栏底部：实现是两行文字（"设置"、"菜单栏切换为深色"）；设计稿是同一行，左边"设置"，右边一个方形图标按钮（深色菜单栏显示太阳，浅色显示月亮），见设计稿 `rail()` 的 `r-foot`。
7. 表单聚焦：实现是深色粗边框加外圈；设计稿不给输入框单独写聚焦样式，用浏览器默认的聚焦外框（owner 2026-10-11 试过"品牌色细边框 + 光晕"后要求改回原稿效果），实现按此还原。
8. 记忆"查看/编辑"、新建项目：实现与原稿都是在页面里展开；设计稿已改为弹窗。
9. 代码高亮：`ai-elements-vue/packages/elements/src/code-block/CodeBlockContent.vue:77,87` 的 `dark:` 变体（Tailwind 默认跟随系统暗色）会把代码块切成深底的 monokai；owner 2026-10-11 要求 web 端不论主题色、菜单栏深浅，代码高亮一律 monokai-light（与 TUI 的 chroma `monokailight` 同色）。
10. 文件编辑：规则文件、记忆文件、技能文件、配置文件现在都是纯文本框；设计稿改为同一个编辑器：.md 用 Milkdown Crepe（MIT，所见即所得，代码块可编辑、选语言、带高亮，可切源码），源码和代码文件用 CodeMirror 6（MIT）。选型与许可证核对见 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/vendor/README.md`。
11. 页面标题上方的"主代理 · main"标识：删掉（owner 2026-10-11：菜单栏顶部的选择器已经显示当前主代理）；设置页的"全局"、项目空间的"项目"保留。
12. 规则指令、模型服务、权限、设置的主代理 / MCP / 钩子：改为"列表 + 列表上方按钮 + 弹窗"，项目空间的规则指令、权限、MCP 同一套。
13. 技能工坊：改为会话页的超集（同一套消息流、输入框、工作台），工作台加文件、测试用例、评测、触发描述、版本。
14. 全量对照：已拆进 web 计划 015–020，每份的完成标准都要求逐页与设计稿并排截图对照，差异写进报告。

上述偏差对应的计划：1、2、3、4 → web 016；5 → 033；6、7、9、11、12 的页面结构 → web 015；8、12 → web 018；10 → web 019；13 → web 020；工作台 → web 017。

## 执行顺序

推荐按以下顺序执行：

```
003-recall-in-flight-steer (独立，无依赖，owner 报告的严重 bug，优先)
004-session-scoped-plan-files (逻辑上独立；与 003 改到同几个文件 api_extra.go / orchestration_llm_test.go，等 003 提交后再执行，或在基于 HEAD 的独立 worktree 里执行)
001-frontend-component-design-spec-alignment (独立，需用户确认命名策略)
005-serialize-run-queue-hook-and-drop-retract-last-steer (依赖 003 的代码在工作区或已提交；与 004 不重叠)
006-shared-compaction-guard-and-unseparable-provisional-tail (依赖 003；排查计划，Step 4 是决策闸门，可达时停下交 owner 拍板；与 005 不重叠，可并行)
007-exit-gate-live-card-and-cardless-anchoring (独立，owner 报告的可见 bug，优先；只改 pkg/tui，与 006 不重叠；notify.go 里有 owner 未提交改动，只删两处过滤条件，不碰其余)
008-web-cardless-exit-gate-anchoring (语义依赖 007、代码不重叠，可与 007 并行，应一起交付；只改 frontend；ChatView.vue / WorkshopView.vue 有 owner 未提交改动，只改点名的行)
009-replay-seals-response-before-cardless-gate (依赖 007 的未提交实现，在同一 worktree $TMPDIR/fb-007-wt 里叠加；与 007 一起并回)
011-gateway-approval-request-names-gated-call (叠加在 worktree ../forebrain-harness-008 的 008 未提交改动上)
012-engine-emits-settled-step-for-refused-calls (011 之后，同一 worktree；两者都改 pkg/gateway/approval_test.go 与 useChatStream.test.ts，必须顺序执行)
013-chatview-blocks-that-draw-nothing-yield-to-content (012 之后，同一 worktree；最后一步 pnpm build 重建 dist)
--- 本机 web/TUI 同源一轮（主树工作区，均基于 005c1a8 + 008 系列未提交改动）---
022-workbench-files-follow-session-directory (独立；owner 截图的可见问题，可最先做；023 复用它的 SessionWorkingDir)
014-tui-persists-main-agent-turn-lifecycle (独立)
021-busy-session-names-the-other-surface (独立)
015-first-token-latency-per-turn (独立；加迁移 v9——与 019 的迁移版本号顺延，谁先执行谁取下一个号)
016-transcript-written-at-text-block-end (独立；017/018 的前提)
017-subagent-views-from-worker-transcript (依赖 016；018 的前提——先换重载数据源，才能停增量)
018-stream-deltas-pushed-not-persisted (依赖 016、017)
019-migrate-away-stored-stream-deltas (依赖 016、017、018；迁移前备份)
020-tui-persists-main-agent-state-events (依赖 014 的写法；与 016–019 无冲突)
023-continue-in-session-birth-directory-design (依赖 022；调研计划，产出设计文档后交 owner 拍板，再写实现计划)
--- 项目模型与设计稿第 18 版（2026-10-11 写）---
025-projects-own-every-session-migration (先做；迁移版本号与 015/019 顺延)
022-workbench-files-follow-session-directory (改写版；依赖 025)
026-working-dir-travels-with-every-turn (依赖 025 无强依赖，建议在 025 后)
027-every-session-runs-on-its-project-runner (依赖 025、026)
028-tui-launch-dir-is-the-project (依赖 025、026、027)
029-web-chat-picks-a-project-first (依赖 025、027；可与 028 并行)
030-attachments-save-into-the-project (依赖 025、029)
033-rewind-to-before-a-message (依赖 025、026)
035-channels-and-cron-belong-to-a-project (依赖 025、027、028)
036-skill-drafts-publish-and-versions (依赖 026)
037-tui-skills-panel-publish-and-versions (依赖 036)
web 015 → 019 → 016 → 017（另需 022、033、036）→ 018（另需 025、029）→ 020（另需 029、036）
024-cross-surface-see-and-continue-acceptance (最后；依赖 014–022、025–029)
--- 自动压缩锚点与本地 token 估算（2026-10-11 写）---
038-pre-turn-anchor-only-trusts-own-usage (独立，owner 报告的可见 bug，可最先做)
039-models-json-ships-beside-binary (独立；建立 assets/ 与 install-assets.sh)
040-link-hf-tokenizers-library (独立；与 039 都改 Makefile/Dockerfile/npm 脚本/acceptance 脚本/README，串行执行，039 先)
041-tokenizer-assets-and-per-model-estimator (依赖 039、040)
042-compaction-estimates-with-session-model-tokenizer (依赖 038、041；含 glm-5.3 真机验收)
--- 038–042 评审修复（2026-10-16 写）---
065-tokenizers-as-tiktoken (先做；6 个分词器改 tiktoken 格式)
066-memoize-token-counts (依赖 065；同改 pkg/llm/tokenizers.go)
067-token-estimate-config-semantics (依赖 065、066；同改 pkg/llm/tokenizers.go)
069-estimator-consistency-in-compaction (依赖 065–067；与 066 同改 pkg/run/llm_middleware.go)
068-fetch-libtokenizers-shell-fixes (独立，只改一个脚本，可与上面任何一个并行)
--- 006 排查结论落成的三项修复 + 字典可达性（2026-10-14 owner 拍板）---
044-fork-continuation-owns-its-run (独立，只改 pkg/run/subagent.go 与其测试；可与 045 并行)
045-compaction-guard-per-history (独立，只改 pkg/run/llm_middleware.go 与其测试；可与 044 并行)
046-unseparable-provisional-tail (依赖 045：复用 historyKey，且与 045 改同一个函数 applyRunScopedCompact，必须串行)
047-dictionaries-are-a-home-install (独立于 044–046，改 pkg/memory / cmd/forebrain / cmd/installdict / npm 打包脚本 / 验收脚本；可与上面任一并行；验收要真跑 memory search)
--- 状态库 schema 与迁移性能（2026-10-14 审计；产出 048–052，同日复评后仅 051/052 保留）---
051-rewind-visibility-predicate-names-the-session (可执行：P1 正确性；只碰 pkg/state 读取路径与其测试，与其它计划无交集)
052-v11-migration-progress-logs (可选：只加日志；无依赖，随时可做)
--- 模型读不到图片（2026-10-14 owner 报告；四类缺口全部进本期，拆两份）---
053-read-images-as-pixels (先做：改 pkg/llm 图片编码器 + pkg/tool read_file/渲染 + pkg/mcp 图片透传 + pkg/gateway 取图端点 + pkg/tui 标记行 + 三段闸门；与 051/052 无交集；Step 6/9 要真机实测，需要 operator 提供 provider key)
054-pdf-pages-as-images-with-cjk (依赖 053 的 Step 1 与 Step 4；新增 go-pdfium 依赖与 pkg/pdf 包、五平台 fetch/打包、中文字体资产；Step 0 spike 与 Step 4 非-tofu 测试是硬闸门；与其它计划无交集)
048-v11-purge-is-linear-and-indexed (SUPERSEDED —— 保留部分见 052；不要执行)
049-migration-connection-pragmas-and-space-preflight (REJECTED —— 不要执行)
050-reclaim-and-backup-retention-policy (REJECTED —— 不要执行)
```

**同一批次说明（2026-10-14 复评后）**：048/049/050 已撤回或驳回（理由见顶部复评与「已考虑并拒绝的发现」），**不再派发**。051 与 052 都只改 `pkg/state`，但文件不重叠（051：`rewind.go` / `session_events.go`；052：`schema_migrations.go`），可并行。

**同一批次的执行约束（2026-10-11 追加）**：025、030、033、035、036 中涉及状态库结构的（025、030、033、035）都改 `pkg/state/schema.sql` 与迁移，**按 025 → 030 → 033 → 035 串行**，迁移版本号依次顺延；026、027、028 都改 `pkg/process`、`pkg/tool`、`pkg/tui` 的同一批文件，**按序串行**；web 015–020 都改 `ChatView.vue` / `App.vue` 等，**按上面的顺序串行**。

**同一批次的执行约束**：014/015/016/018/020/022 都改 `pkg/tui` 或 `pkg/gateway` 的同一批文件（`chat_turn.go`、`chat_session.go`、`notify.go`、`api_extra.go`、`server.go`），**必须在同一工作区按上面的顺序串行执行**，不要并行派发。

**008 系列并回主树须知**（owner 决定方式与时机）：主树 `005c1a8` 已含 007+009（TUI），008/011/012/013 只在 worktree。
- `frontend/src/views/WorkshopView.vue` 的 hunk 与主树 `rounded-2xl→rounded-lg` 改动相邻，需手工合并（保留主树的 `rounded-lg`，取 worktree 的 `hasVisibleBlocks`）；`ChatView.vue`、`ChatView.test.ts` 主树也有 owner 改动，`git apply --check` 先试。
- `pkg/gateway/dist` **不要从 worktree 拷**：它由 worktree 的旧源码构建；并回源码后在主树 `cd frontend && pnpm build` 重新生成。
- `pkg/tui/chat_session.go` 主树已被 007 改动，011/012 均未触碰它；012 改的 `pkg/tui/chat_turn.go`、`pkg/tui/run_test.go` 主树与 9656051 相同。
- `frontend/src/views/ChatView.test.ts`：013 把 `mountView` 提到了文件顶层，主树 `005c1a8` 在原来那份 `mountView` 的 stubs 里加了 `SessionHeartbeat: true`——并回时把这一行补进顶层 `mountView` 的 stubs（否则主树的 `SessionHeartbeat` 组件会在测试里真实挂载）。
- 011/012/013 的 Go 改动所在文件（`pkg/gateway/{approval,server,wsevents}.go`、`pkg/turn/approval.go`、`pkg/tool/{state,format,notification}.go`、`pkg/run/orchestration_llm.go` 及其测试）在主树与 9656051 相同，可直接 `git apply`。
- 并回后补做 008 Done criteria 里的跨表面对照：同一会话 TUI 回放 vs web 重载（含 review 交付、拒绝带反馈两条流程）。

## 状态说明

- **TODO**：待执行
- **IN_PROGRESS**：执行中
- **BLOCKED**：被阻塞（等待决策或依赖）
- **DONE**：已完成
- **REJECTED**：已拒绝（说明原因）

---

## 最新审核发现总结（2026-10-09）

### branch 审核：003 在工作区里的实现（未提交，基于 `71c0449`）

**结论**：没有阻塞问题。实现与 003 计划一致；核心不变式经逐段核对成立（steer 恒在 `pending` 或某个未结算交付里、同一把 `rt.mu`；
「先 Commit 再查 `Retracted()`」两处顺序正确；`Recall`（`q.mu`→`rt.mu`）与 `Commit`（释放 `rt.mu` 后才进 `q.mu`）无锁序反转；
提醒类包装都在 `recoverableLLM` 里面，暂定尾巴的长度校验在今天的链路上成立；压缩的摘要请求会屏蔽 `OnResponseStarted`；
三个流式 provider 都触发 `NotifyResponseStarted`；`TakeAll`/`Next` 只在不可能有在途交付的位置调用）。
验证：`go vet`、`gofmt` 干净；`go test -race ./pkg/run`、`./pkg/gateway`、`./pkg/turn`、`./pkg/event`、`./pkg/architecture` 通过；
`./pkg/tui` 仅 `TestCharacterizationNetworkApproval` 因真实访问 example.com 偶发失败、单跑通过；两个前端测试文件 108 个用例通过。
未做：全量 `go test ./...`、TUI 真机 Step 12、web 浏览器复核清单（003 Maintenance notes ①②③，需 owner 手动）。

| # | 发现 | 归属 | 影响 | 计划 |
|---|------|------|------|------|
| 1 | gateway `watchRunQueue` 的 hook 在多个 goroutine 上并发运行且无互斥：别的请求的 hook 可能抢走刚送达的 steer，`input_delivered` 晚于回答首个 delta 落库，web 实时把回答开头画进上一个气泡；旧预览也可能最后落库 | 本分支引入 | 低（窗口极窄，仅 web 实时，刷新恢复） | 005 |
| 2 | `TurnInputRuntime.RetractLastSteer` 已无生产调用方，且看不到在途交付——003 自己的维护说明把它列为回归陷阱 | 本分支引入（变成死代码） | 低 | 005 |
| 3 | 撤回路径裁剪 adoption 依赖「暂定尾巴被遵守」；`applyRunScopedCompact` 的防御分支会整段压缩并折入 steer。探针证实：共享的单槽 `compactRunGuard` 在同一 run ID 下会把另一段历史的 checkpoint 拼进父请求；尾巴不被遵守时撤回会把 checkpoint 裁掉。生产可达性未定（线索：`own.RunRT == nil` 时 fork 子任务沿用父 run ID，且 fork 链与主链共用同一个 `recoverableLLM`） | 防御分支本分支引入；共享 guard 为既有 | 今天的链路上未见触发；若可达则是更大的既有压缩串台 | 006（排查） |

### 类别：Correctness — 多进程 / 多会话 plan 模式的计划文件串台

**严重程度**：**HIGH**

**根因**：「当前计划」= `<stateRoot>/plans/<projectKey>/` 下 mtime 最新的 `.md`（`pkg/state/plan.go` 的
`PlanPathForProject` → `newestMarkdown`），解析不带会话 ID。同一项目里的所有 TUI 进程、gateway 会话、/fork 出来的会话
共用这个扁平目录，谁最后写计划，谁的文件就成了所有会话的当前计划。同一个函数还决定了：批准后告诉模型去实现哪个文件、
计划评审读哪个文件、每轮 plan-mode 提醒里的 current、实现阶段的提醒、`/plan`、`/status`、web 的 plan-md 与审批卡、
shell 的 `FOREBRAIN_PLAN_FILE`。

**真机证据**（driver `toolcall` 模式，HEAD `71c0449`）：会话自己从没写过计划，在项目计划目录里放一份「别的进程的计划」后，
exit_plan_mode 审批框显示了这份外来计划；批准后工具结果是 `Implement the approved plan in …/other-process-plan.md`。

**修复**：见 `004-session-scoped-plan-files.md`——计划改放 `plans/<projectKey>/<sessionID>/`，会话 ID 一律取对话 ID
（`tool.PlanSessionIDFromContext`，subagent 写进父对话的目录）；所有读写入口切到会话版本，删掉按项目解析的旧 API；
/fork 复制源会话的计划；plan 模式下写不进别的会话目录（`isSanctionedPlanWrite` 只放行可写目录正下方的文件，无需修改）；
`/migrate` 把导入的计划写进 `cli-<id>` 会话目录（slug 对应多个会话时每个一份）；`RemoveSessionStateFiles` 有意不删计划目录。

### 类别：Correctness — 在途 steer 显示在队列里却无法撤回

**严重程度**：**HIGH**

**根因**：steer 在工具边界（或无工具调用的最终回答处）被 `TurnInputRuntime.BeginSteerDelivery` 从 runtime 的
`pending` 取走、拼进下一次模型请求，但要等该请求的**首个输出事件**才 `Commit`、才离开 `InputQueue.steers`
（队列预览的数据源）。这段窗口里消息仍显示在队列中，`InputQueue.Recall` 却用只查 `pending` 的
`RetractLastSteer` 去撤回，必然失败并跳过整条 steer 车道 → Shift+← 无响应，首 token 到达后消息自动显示为已发送。
窗口 = 下一次请求的首 token 延迟，若该请求触发中途自动压缩还要加上整段压缩时间（压缩在 `recoverableLLM` 里，
处于同一次调用之内）。现有子测试 `TestInputQueueRecallPicksNewestByLane/unretractable steer is skipped` 把这个行为钉死了。

**同类缺口（owner 要求并入本期）**：切换会话时 `InputQueue.Discard` 清空旧会话队列，但同样只撤得回 `pending`——
在途 steer 只从预览消失，旧 run 照样把它交给模型，Commit 时队列里已找不到它，模型回答了一条从未进 transcript 的消息；
runtime 已 detach 时 `Discard` 还不计 steer，「queued input discarded」提示少报。

**web / gateway 的同类问题（owner 要求并入本期）**：
- gateway 的主会话与 subagent `edit_last` 都走 `InputQueue.Recall`，同一个在途窗口；引擎修复覆盖，补 gateway 测试锁住。
- web subagent 视图的队列预览只在 HTTP 操作后推送：steer 送达、执行结束后预览不更新，已送达的消息一边显示成用户消息、一边挂在队列里，按撤回必失败（持久化事件回放后依旧）。修法：引擎 `SubagentSurface.OnQueueChanged` + 通道的队列 change hook，gateway 据此推预览。
- web 主会话里 steer 送达后从队列消失、却不进实时对话（TUI 画成 you 消息），要刷新才可见；gateway 会话队列的 delivered 列表只增不减。修法：新增持久事件 `input_delivered`，gateway `watchRunQueue` 取走 delivered 并先于预览发出；web 实时收尾当前回答、插入用户气泡、开新回答气泡（与刷新后的回放一致）。
- web 的 Shift+← 只在本页自己的 run 流式时生效：主会话空闲的 subagent 视图、刷新后进行中的 run，队列可见却按键无效。修法：输入框按「队列可见」放行，`editLastQueuedMessage` 捕获 409。

**顺带发现的潜伏 bug**：中途压缩的输入是整个 session（含在途 steer），checkpoint 落库后若这次请求失败，
`Rollback` 把 steer 放回队列由 turn 边界重发——checkpoint 里一份、重发一份，重复。

**修复**：见 `003-recall-in-flight-steer.md`——runtime 登记未结算的在途交付，新增 `RetractSteer(seq)` 可从在途交付里
拿回 steer 并中止承载它的调用；`Recall` 与 `Discard` 都改用它；被撤回过的交付拒绝 Commit，编排循环丢弃该调用结果、去掉撤回的 steer 后自动重发；
中途/被动压缩只 checkpoint 暂定消息之前的历史，并在不受撤回影响的 ctx 上运行。三个撤回入口（TUI、subagent 视图、
gateway `edit_last`）都走 `InputQueue.Recall`，引擎层一处修复全部生效。

### 类别：Performance / Correctness — subagent 视图输入卡死

**严重程度**：**HIGH**

**根因**：subagent 视图里每处理一次输入事件，TUI 主 goroutine 都会重绘 composer，而 composer 的队列预览
`run.SubagentInputPreview` 经 `agent.GetMerged → ListHistory` **整文件读取并逐行 JSON 解码**
workspace 级账本 `state/subagent-history.jsonl`（owner 环境 3.4 MB / 448 条，只增不减，约 30 ms/次）。
快速滚轮产生上千事件，每轮主循环付 30 ms，事件在输入管线积压 → 输入冻结数秒，之后积压的操作被快速执行。
主视图的预览读内存队列，不受影响。

**证据**（tmux + owner 真实 `~/.forebrain`，同一进程对照）：N=1000 个滚轮事件后探针回显延迟，
subagent 视图 3.97 s，主视图 0.11 s；`sample` + `go tool addr2line` 显示主 goroutine 栈 124/124 落在
`renderComposerWithState → … → agent.ListHistory → json.Unmarshal`。

**修复**：见 `002-subagent-view-input-freeze-ledger-read.md`——给进程内 subagent channel 表加
(会话 id, task id) 二级索引，预览 / 撤回 / Esc / 丢弃 / running 判断只查内存，不再碰账本。

---

## 历史审核记录

### 2026-10-08 - TUI message queue bug（Shift+← 无法撤回）

**根因**：`pkg/run/turn_input.go` 的 `InputQueue.Recall()` 把 `q.rt == nil`（runtime 已 detach）误当作
"不可撤回"，跳过整条 steer lane。runtime detach 后消息仍在 `InputQueue.steers`（未交付），可直接撤回。

**计划**：`001-fix-message-recall-after-detach.md`（已 DONE；单测 `TestRecallSteerAfterRuntimeDetach`，
TUI 真机：提交 → 等待 → Shift+← 撤回）。

### 2026-10-08 - 设计稿与实现一致性审核（通用组件样式）

**审核范围**：`frontend/src/assets/main.css` vs `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`

**发现**：设计稿定义的通用组件规范（`.btn`, `.field`, `.badge`, `.sw`, `.card`）与 `main.css` 实现存在三类问题：

1. **命名空间不一致**：设计稿用简短类名（`.btn`），实现用前缀类名（`.forebrain-btn`）
2. **数值偏差**：按钮、输入框的 `border-radius` 和 `padding` 不匹配
3. **组件缺失**：`.badge`、`.sw`、`.card` 及部分变体完全缺失

**计划**：见 `001-frontend-component-design-spec-alignment.md`（需用户确认命名策略）

---

## 已审核但未计划的发现

### 2026-10-14 schema / 迁移性能审计（已逐条核实、本期不立计划）

- **v11 真正的成本中心是 subagent 增量合并**（`pkg/state/schema_migrations.go:913-985`）：实测 93 分钟里几乎全部耗在此处——233 个执行体，按连续块 `UPDATE ... json_set(payload_json,'$.text',<合并文本>)` 加 `DELETE ... WHERE sequence IN (<成百上千个字面量>)`，合计删掉/折掉 7.5M 行，速率约 1,340 行/秒。若要再优化，方向是「按 sequence 区间分批删除代替逐块拼 IN 列表」与「减少 Go 侧全量物化」；但这是**每个库一辈子一次**的迁移（owner 的库已完成），收益仅是把别人的一次等待从 90 分钟压到几十分钟——除非用户量值得投入，否则不建议立计划。

- **memory FTS 重建整库持写锁**（`pkg/memory/ftsindex.go:78-96`）：`sync` 在一个事务里对每个变更文件做 `replaceFileRows`（按 rowid 区间删旧行 + 逐行插新行），`TermsRevision` 变更时全库重索引；状态库每个事务都是 `BEGIN IMMEDIATE` + 5s busy_timeout（`pkg/state/db.go:154`），于是升级后第一次 `memories_search` 会数秒到数分钟地挡住所有写方。分批提交需要先落一个持久化的 next-rowid 计数器（`ftsindex.go:60-76` 的注释说明 rowid 区间预留依赖「同一事务内读 MAX(rowid)」，并发同 root 的两个搜索会互相覆盖），属 M 级设计改动。
- **v0→v1 事件重建把整个旧日志读进内存再逐行插入**（`pkg/state/schema_migrations.go:2209-2247`、`:2278-2388`）：Go 侧累积全部行 + 每行一次 `ExecContext`，在 7.7M 行的旧库上有 OOM 与分钟级耗时风险。只影响 v0（很老的）库；改造必须保持「cursor A 顺序为准、cursor B 按时间戳归并」的既有语义，M–L / MED。
- **v7→v8 回填一次全表扫描**（`pkg/state/schema_migrations.go:296-309`）：`UPDATE ... WHERE event_type IN ('subagent_spawned','subagent_ended')` 没有可用索引（无任何索引以 `event_type` 打头），5.6GB 库上为几百行更新扫全表；可照 048 的「迁移内建局部索引再 drop」手法。只影响 v7 库，收益有限。
- **v8→v9 按会话 UPDATE 循环**（`pkg/state/schema_migrations.go:1354-1365`）与 **v0→v1 逐回合语句循环**（`:2927-2963`）：O(N) 条语句而非 O(N²)，但 N 为会话/回合数；可用临时表 + 一次 `UPDATE ... FROM` 收敛。低优先。
- **运行期（非 schema/迁移）性能发现**（本轮顺手扫到，仅登记，未核实到行级时序）：
  - `pkg/state/message_sync.go:46-55`：每次 transcript 落盘前在写事务里读最近 5000 行并逐行 JSON 解析（读取在 `pkg/state/session_store.go:1090`、`:1288`），长会话每回合都付。
  - `pkg/gateway/api_extra.go:1650-1651`：历史接口先 `ListAllMessages(ctx, sid, 0)`（0 = 无 LIMIT）再按 `limit` 截断；`historyLimit` 只用在另一个分支。
  - `pkg/memory/backend.go:504-529`：每次 `memories_search` 走遍整个 memory 根并读每个文件；`pkg/memory/pipeline.go:87-94` 每回合做一次 O(全部已提取会话) 的 prune / 版本读取 / 证据目录遍历（`pkg/memory/evidence.go:190-217`）。
  - `pkg/state/session_store.go:2215-2252`：`ForkInto` 全量读入内存后逐行 `insertMessage`（`:1713` 每次重新解析、未 Prepare）。
  - `pkg/tool/loaded_skills.go:631-649`：`output_records` 无索引、无保留策略，`/context` 三次全表扫描（`pkg/turn/context.go:241`、`:255`）。
  - `pkg/state/action_service.go:136-165`：`/api/actions` 的 `ORDER BY a.updated_at DESC` 要 temp b-tree 排序整个 agent 的历史。
  - `pkg/state/db.go:35` 与 `pkg/tool/loaded_skills.go:611`：连接池无 `SetMaxOpenConns`。
  - 测试套件：`OpenStateForTest` 每个测试新建并迁移一个库（全仓 297 处），可「一次建模板 + 每测试拷文件」提速（`pkg/state/session_events_test.go:71-126` 已是 10 万行级夹具，但不是平方级）。

- ~~web 端无卡 exit gate 的审批记录落位错位~~ → 已立计划 008（2026-10-10，owner 要求另写 web 同步计划）。

- **会话内容落在磁盘文件里，而非只在 SQLite**（owner 2026-10-09 定方向："所有会话消息无论是 primary agent
  的还是 subagent 的，都不应该存在磁盘文件里，都应该只存在 sqlite 数据库里"；同日裁决"本期先优先解决
  subagent 视图卡死，其他放在以后处理"）。已盘点到的落盘位置（均在 `<workspaceRoot>/state/` 下，
  owner 环境 2026-10-09 体量）：
  - `subagent-history.jsonl`（3.4 MB）——`pkg/agent/subagent_history.go`，subagent 任务全文与输出；
    读写方遍布 `pkg/run`、`pkg/tui`、`pkg/gateway`、`pkg/turn`。002 只把它移出每事件热路径，迁库待下期。
  - `fork-sidechain/<sid>/*.jsonl`（9.3 MB）——`pkg/hook/dispatch.go:633`
  - `hook-transcripts/<sid>.jsonl`（62 MB）——`pkg/hook/dispatch.go:640`（hook 协议的 transcript_path，迁库涉及兼容性取舍，需 owner 拍板）
  - `intermediate/<sid>.md`——`pkg/state/intermediate.go:14`
  - `cli-input-history.txt`——composer 输入历史
  - 迁库需按仓库约定为用户旧数据写迁移（state 库当前 `user_version = 8`，账本按 agent workspace 分布、
    state 库在 home 级，导入旧 jsonl 只能在运行期做，不能放进纯 SQL 的 schema 迁移）。

## 已考虑并拒绝的发现

### 2026-10-14 撤销备案：原「053 不做的两件事」已并入本期

owner 2026-10-14 裁决「**Out of scope 里的必须加入本期计划**」，以下两条**不再是备案、已进计划**：

- **MCP 工具结果里的图片改成交出像素** → 现在是 **053 Step 5**（`pkg/mcp` 新增 `ToolResultContent` +
  `Session.CallToolContent`，`pkg/run` 的 `mcpToolCaller`/`newMCPDelegatingTool` 签名加宽到 `any`；
  既保留落盘（审计与 `retrieve_output`），又把图片作为 part 交给模型）
  **＋ 053 Step 5b**（`mcp-results` 目录按 owner 裁决搬进**主代理 workspace**：三层作用域共用
  `<主代理 workspace>/state/mcp-results/`，于是读它天然免审批——`ProtectedReadReason` 不加豁免）。
- **模型能力闸门**（`supports_attachments` / `models.json` 的 `modalities`）→ 现在是 **053 Step 4**
  （`pkg/run/image_input.go` 的 `ImageInputRefusal`，fail-open：catalog 不认识的模型一律放行；
  消费点是 read_file 的图片分支、MCP 的图片转换、以及 web/TUI 的附件路径，三处同口径）。
- 另外两项同类缺口也一并进计划：**surface 出图**（053 Step 7：web 缩略图 + 新取图端点，
  TUI 标记行 + `o` 键打开；owner 选定不做内联像素）与 **PDF 逐页转 PNG**（**054**，含 owner 追加的硬约束
  「必须支持中文字体」）。

### 2026-10-14 复评新增的驳回（依据是迁移自己跑完的实测数据）

- **048 的两项查询计划改动（主清理单调游标 + 两条迁移局部表达式索引）——撤回，保留部分见 052**。依据：`main_agent_rows_deleted=0`，主清理在这台库上一行都没删（一次扫描即返回）；「平方级」只在「主代理自己写过海量增量」的库上成立，而这台库的 7.5M 增量全部来自 subagent。工具输出阶段在 subagent 阶段之后运行，那时事件表已从 7.7M 行缩到可忽略，逐 step 全表扫描的实际成本是零头。对一个**已跑完的一次性迁移**，改删数据路径的收益不抵风险。
- **049：迁移连接的 `cache_size` / `journal_size_limit`**。依据：93 分钟是行级删除与合并（≈1,340 行/秒，含每行最多 4 棵 b-tree 的维护与多 GB WAL 写入），不是缺缓存；`-wal` 在最后一个连接关闭时已由 SQLite 清理（复评时 `~/.forebrain/state/` 里已无 WAL 文件），限额针对的是一段临时占用而非泄漏。
- **049：预检从 1.1× 提到 2.2×**。依据：实测全程只需「5.6 GB 备份 + 约 4 GB WAL」，现有 1.1×（6.2 GB）在 16 GiB 可用空间下放行了整段迁移且未失败；2.2× 会把「空间其实够」的用户挡在门外。若将来真出现「跑到一半空间不足」的事故，再按当时数据定阈值。
- **050：VACUUM 阈值（≥64MiB 或 ≥10% 页数）**。依据：`freed_pages=961977`（≈3.9 GB）那次回收，从 v11 结束到 v14 完成总共 **54 秒**（15:05:15 → 15:06:09）；阈值只为未来的小步升级省下「1.6 GB 文件重写」的十几秒。
- **050：备份保留（同版本只留最新）**。依据：owner 已手动删除那份 5.6 GB 备份，`backups/` 目录当前不存在；且备份函数有 `version >= stateSchemaVersion` 的门（`schema_migrations.go:1466-1468`），v11 之后不再写新备份——保护的是一组不会再增长的文件。
- **052：v11 各阶段的进度日志（owner 2026-10-14 裁决不做）**。依据：本计划的价值只落在「下一个大库的首次 v10→v11 升级」上，而这次迁移已在唯一已知的受影响库上完成；为一个不会再回来的一次性路径改迁移代码不划算。方案完整保留在 `plans/052-v11-migration-progress-logs.md`，将来真有第二个大库要走 v11 时可直接复活。

### 2026-10-14 schema / 迁移性能审计的拒绝项（owner 可复核）

- **用 `VACUUM INTO` + 文件替换取代尾部原地 VACUUM**（owner 2026-10-14 提议；实测后拒绝）：(1) WAL 模式下原地 VACUUM **不会**撑大 WAL——源码路径 `sqlite3RunVacuum` → `sqlite3BtreeCopyFile` 把 ATTACH 的临时库整页拷回主库文件（scratch 库实测 WAL 保持 64KB，且 `BTREE_USER_VERSION` / `BTREE_APPLICATION_ID` 保留）；(2) 换 inode 会绕过 SQLite 自己的锁协调——迁移特意用一个 `BEGIN IMMEDIATE` 事务让其它进程等到提交后看到新版本，把主库文件换掉会让正在等待的进程继续看向旧文件，`-wal`/`-shm` 也要一并作废；(3) 真正需要回收时（v11 清理）幸存数据只有百分之几，VACUUM 的临时文件与拷回量都很小。→ 改为「只在回收量可观时跑」（050）。
- **`PRAGMA foreign_key_check` 按「动过的表」收窄**：它是重建类迁移的安全闸门（`schema_migrations.go:1642-1644`、`:1693-1719`），收窄需要 14 个迁移各自申报涉及的表——在删数据的迁移路径上，把「无条件验证」换成「各自声明」不划算；且清理后的事件表已很小，成本是秒级。
- **删掉 `idx_fb_session_events_session`**（与 `UNIQUE (session_id, event_id)` 的自动索引前缀重复，`schema.sql:283`/`:285`）：确实冗余，但属运行期写入收益，需要新迁移版本号 + `schema.sql` 改动；v11 之后事件表写入频率大幅下降，收益不抵一次 schema 版本升级（若将来有 v15 可顺手合并）。
- **迁移连接 `temp_store = MEMORY`**：大 GROUP BY 的排序会从文件变内存驻留，7.7M 行分组可能吃掉数 GB RSS；缓存已由 049 的 `cache_size = -131072` 有界提升，不动 `temp_store`（`mmap_size` 同理，构建上限不明）。
- **把迁移拆成多个事务 / 让删除断点续跑**：单事务是刻意的崩溃安全设计（另一进程靠写锁等到提交后看到完整结果）。拆分要连同「可恢复 + 版本号推进」协议重做，属独立立项；本批用 049 的空间预检替代「跑到一半失败」。
- **迁移后跑 `PRAGMA optimize` / `ANALYZE`**：审计过的运行期查询都是等值 seek，统计信息不改计划；ANALYZE 本身要扫大表。不做。

- **web 实时观看 TUI 会话 / TUI 实时显示 web 的 turn（跨进程实时推送）**（2026-10-10）：owner 先要求"必须实现"，随后撤回，改为"只要数据互通，刷新或重新进入能看到最新即可"。
  若将来重启：gateway 需要跟随数据库里其他进程写入的事件（总线目前只转发本进程发布的事件）。
- **增量逐 token 落库 / 按固定间隔合并落库 / 按文字块合并成事件落库**（2026-10-10）：三者先后被讨论，最终 owner 选择"文字只存 transcript、增量不落库"（016–019），避免与 transcript 重复存储。
- **首 token 延迟写进每条 assistant 行的 `usage_json`**（2026-10-10）：owner 明确否决——一轮只存一次，存 `fb_runs.first_token_ms`（015）。
- **跨进程排队（另一端在跑时把消息排进对方的队列）**（2026-10-10）：owner 选择"拒绝并提示正在另一端运行"（021）。
- **文件型会话数据入库（`state/modes/<sid>.json`、hook transcript、intermediate 笔记、输入历史）**（2026-10-10）：owner 说"先不考虑，以后再说"——本期不做。
- **工作台显示当前根目录路径**（022 起草时加入、随即删除）：owner 没有要求，属于范围外的界面改动。

- **web 端在 `approval_requested` 无 toolStepId 时，把 actionId 绑到本轮最早一个未绑定的锚点**（008 branch 审核）：纯前端，但属启发式——
  exit 调用在请求审批前就失败时会绑错，TUI 回放 gateway 会话也仍错位。owner 2026-10-10 选 gateway 补字段（011）。
- **web 收到 exit gate 的 denied 决定时自己补画 settled 卡**（008 branch 审核）：只动前端、风险小，但只修 web 的 exit gate；owner 2026-10-10 选
  由运行时发步骤事件（012），一并修好 shell 等被拒卡在 web live 停在 "awaiting approval" 的问题。
- **012 让 TUI 也从运行时事件画拒绝卡、删掉本地合成**（008 branch 审核）：更"引擎为源"，但 `toolStepSummary` 在 summary 为空时会退回
  `"ok"`/`"done"` 字样，嵌套 request_permissions 的 `canceled` 措辞也会被 `denied` 覆盖——可见变化需另行裁决；012 让 TUI 主视图只落库不画。
- **gateway 在 live 多建的 `gate:tool:exit_plan_mode` 锚点**（`server.go:1937` 合成等待步骤的 step_id）：看不见、不影响可见块序，记入 011 的
  Maintenance notes，不立计划。

- **007 入口过滤提前返回、跳过了 `Tracker.ObserveToolStep`，取消/交付掉的 exit gate 不再计入工具计数**（007 branch 审核第二轮）：
  属实，但 `Counters.Tools` 只被追踪、从不绘制（`formatWorkingCounters` 只画计划清单进度；回放用的是不带 tracker 的新 `Reducer`），
  没有用户可见后果，不值得为此挪动 007 已评审的过滤位置。
- **回放的封存直接在 `flush` 里调 `reducer.flushBufferedText`**（009）：能修，但在回放侧复制了 live 规则（缓冲区选择、reasoning 时长）；
  009 改为补喂 live 同款的 running 步骤，让回放走 reducer 同一入口。

- **只在 `notifyToolStepHooks`（主 agent 生产者处）补一道 `ToolStepHoldsNoCard` 过滤**（007）：能修这次的两个 bug，
  但和 d501e09 一样是"在某条生产路径上打补丁"，下一条新路径照样漏。007 把过滤放在所有工具消息都必经的
  `Reducer.reduceMessage`，并删掉漏斗处的重复检查。
- **给 exit gate 保留一个不可见的占位块，让确认行继续"插到它之前"**（007）：不用改落位规则，但不可见块要在视口行数、
  点击展开、`FinalizePendingTools` 等处处特判，复杂度和风险都更高。
- **web 用"隐藏的 exit_plan_mode 工具块"代替锚点块**（008）：能复用"插在被审批卡之上"的旧逻辑，但要撤回 d501e09 在
  `handleStepEvent` 的跳过、让运行状态行与各处工具块消费者都学会忽略它，牵连更广；锚点块是一个只有落位语义的新类型，渲染层天然忽略。
- **web 保持"一个 action 一个审批块"，把多条决定行合进同一块**（008）：改动小，但 review 卡无法夹在 "You asked" 与
  "You approved" 之间，读起来与 TUI 不同；008 对 exit gate 改为一条决定一个块。
- **把所有审批确认行的落位改成"按 StepID 找被审批的块，找不到就追加"**（007）：规则更精确，但改变所有工具审批的语义
  （并行调用时位置会变），超出两个 bug 的范围；007 只对无卡 gate 改规则。

- **在 `InputQueue` 引擎层统一串行化所有 change hook**（branch 审核，2026-10-09）：能顺带覆盖 TUI 与 subagent 通道，但 subagent 的 hook
  会在持有通道锁时被调用，要重新论证锁序；问题只在 gateway 有可见后果，005 只在 `watchRunQueue` 里加锁。
- **subagent 队列 hook 在每次执行 Attach/Detach 都落一条 `pending_input_updated`，且 HTTP 处理函数还会再发一次**（branch 审核）：
  事件日志多几行空预览，主会话早就是同样模式，不值得单独改。
- **`Discard` 与「已 Commit、队列尚未收到通知」的瞬间竞争**（branch 审核）：被丢的 steer 已在 session 里、会进 transcript；`Discard`
  只在切换会话时调用，离开的视图本就不再渲染它，回来时从 transcript 回放。既有行为，不改。
- **web 一次送达两条 steer 时两条用户气泡之间留一个空回答气泡**（branch 审核）：核实后不会显示——被收尾的空气泡 content 置空、无 blocks，
  `messageHasBubble` 返回 false。

- **计划文件「会话绑定」方案（扁平目录不变，在 `state/modes/<sid>.json` 里记会话写过的计划文件名）**：改动更集中，
  但两个会话起同名文件时仍会串台，还要额外处理 subagent 并发写绑定。owner 2026-10-09 选了「每会话子目录」。
- **旧会话迁移 / 回退（从 transcript 回填绑定、或无绑定时回退到扁平目录里最新的文件）**：owner 2026-10-09 裁决不迁移；
  回退会让旧会话继续串台。
- **保持平铺目录的串台修法——文件名带会话后缀 `<name>--<指纹>.md`、在 SQLite 里记计划文件归属**：都能修好，但前者让文件名带上
  不可读的后缀、模型写错名要被拒一次，后者要升 schema 并把数据库句柄传进 planModeLLM / shell / 工具等多处。owner 2026-10-09 评估后维持每会话子目录。
- **删会话时清理它的计划目录**：owner 2026-10-09 裁决删会话不碰计划目录，计划作为历史保留（004 只在 `RemoveSessionStateFiles` 注释里写明）。

- **在途 steer 一被取走就移出队列预览（显示为「发送中」）**：能让「显示即可撤回」字面成立，但 owner 要的是能撤回；
  且 `SteerDelivery` 的设计本意就是失败时把 steer 留在队列里由边界重发，提前移出会重新引入「显示已发送、实际没人回答」的问题。- **「checkpoint 落库即视为送达」处理压缩窗口**：改动小，但 PostCompact hook 执行期间撤回存在竞态，已撤回的话可能残留在
  checkpoint 里，且压缩中撤回会白做一次压缩、重跑 PreCompact hook。owner 2026-10-09 选择了「压缩不吞未送达 steer」。

- **"只在裸终端复现、终端排水阻塞 paint 写入"**（a7151f5 asyncPaintWriter，已于 2026-10-09 回滚）：不是本卡死的根因——
  tmux 里稳定复现，且同一进程主视图正常。此前 tmux 复现失败是因为 driver 隔离 home 的账本几乎为空。
- **滚轮 release/motion 报告被重复计档**（b7eda22，已回滚）：只影响滚动行程，与输入冻结无关。
- **transcript 越长 `renderViewport` 越慢**：被对照推翻——主视图 557 条消息 1000 事件 0.11 s，subagent 视图消息更少却 3.97 s。
- **给 `agent.ListHistory` 加 mtime 缓存/增量解析**：账本在每次 subagent 执行开始和结束都追加，缓存频繁失效；
  且 owner 已定迁库方向。002 让热路径根本不读账本，更彻底。

- **把「尾巴长度对不上」一律当成「分不清」**（046 的第一版，即 006 Step 5 的原样）：能堵住探针 B（撤回把 checkpoint 裁掉），但暂定尾巴的标记装在 callCtx 上，父 turn 有在途 steer 时它派发的每个子代理都继承该标记——于是子代理在整个执行期间都不会压缩，且只在日志里静默出现 `compaction skipped: provisional messages cannot be told apart`（006 的计数校验恰好是 1，发现不了）。046 改为按历史归属三分支，并加一条护栏测试钉住「继承别人尾巴时照常压缩」。
- **字典缺失时让 memory search 退化成不切词/只按 ASCII 切**（047 起草时讨论）：FTS 索引里的词条就是同一套切词规则产生的，用另一套规则查询只会返回错误结果（命中错、漏命中都是错的）。宁可让工具报错并给出修复动作，也不给错答案。
- **只为开发构建放开「非 release 版本也能下载字典」**（047 的方案 C）：能自愈 owner 本机的二进制，但字典仍只属于某一个二进制目录，换二进制、重装、多二进制共用 home 都会继续踩；owner 选了「home 级位置＋补种」。
- **以 `make build` / `scripts/install-dictionary.sh` 为本地联调流程的修法**（047 的方案 B）：owner 明确「本地开发联调只使用 `npm/scripts/build-platform-packages.sh` 和 `cd npm && npm link`，从来不用 make build / scripts/install-dictionary.sh」；047 的安装侧按这条唯一流程设计。
- **安装脚本替用户铺 home（打包脚本种 `$FOREBRAIN_HOME/dict` 与本机 GOBIN 的 `dict`）**（047 的第一版 Step 6，已按 owner 裁决删除）：当时理由是「让 owner 正在运行的那个老二进制不用重建就恢复」，但 owner 明确不要安装脚本写用户的 home / GOBIN。047 改为**产品启动时自己播种**（`process.Seed` → `memory.Seed`，与 `models.json` 完全同款）：打包/`npm install` 出来的二进制第一次启动就把「自己旁边那份」搬进 home，安装脚本只剩一行 `installdict -verify` 校验产物完整。被否掉的第三条路是「不播种、只靠第一次中文搜索时补种」——那样 home 在「先跑的是不带字典的二进制」时仍是空的，也没有离线可测的启动保证。代价一致：**旧二进制必须先被替换**。
- **把字典并入 assets 资产族（提交进仓库 `assets/dict/`，+19MB）**（047 的方案 D）：能让 `FOREBRAIN_ASSETS_DIR` 的覆盖一并生效、全族一套解析，但仓库增重，且「字典字节与依赖模块的漂移校验」（`TestDictionaryFilesDeclareTheBytesTheirModulesShip`）要改成对仓库副本；owner 选了 home 级位置方案。

## 下一步

0. **schema / 迁移性能批次已收口，无待执行项**：051 已并回主工作区（未提交）；048 撤回，049 / 050 / 052 驳回（依据见顶部复评与「已考虑并拒绝的发现」）。不要派发这四份。
1. **owner 确认**：本轮写完的计划（022、024 改写版，025–030、033、035–037；web 015–020）。035 的通道凭据存储已定（方案 A）。
2. 确认后按"执行顺序"里的分组派发：先 025，再 026 → 027，然后 028 / 029 并行；web 先 015、019。
3. 仍为 TODO 的早期计划（001 设计规范、007/008 系列之外的 014–021、032、034）照旧可独立执行，与上面不冲突的可穿插。

---

**索引更新时间**：2026-10-14（新增 048–051 与本次 schema/迁移性能审计记录）
**Improve Skill 版本**：按 improve skill 模板生成
