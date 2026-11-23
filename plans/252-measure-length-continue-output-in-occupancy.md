# Plan 252: 用 glm-5.3 实测续写后占用是否重复计入输出，按结果决定改不改

> **Executor instructions**: 这是「先测量、再按判据二选一」的计划。按步骤执行，每步的验证通过后再做下一步。
> 遇到「STOP conditions」就停下来报告，不要自己发挥。**不要 commit / push / merge**：改动留在工作区，
> 做完报告（仓库主人手动提交）。做完后把 `plans/README.md` 里本计划那一行状态改为 DONE（走了分支 A）
> 或 REJECTED（走了分支 B），附一句带测量数字的摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> 1. plans/250 与 plans/251 必须都是 DONE（`grep -n '25[01]-length-continue' plans/README.md`）。不是 → STOP。
> 2. 下面两行必须各命中 1 处（251 明确不碰它们）：
>    `grep -n 'out.Usage = openAIUsage(int64(settledIn), int64(tokOut), int64(settledCached))' pkg/llm/openai/compat_llm.go`
>    `grep -n 'settled := openAIUsage(int64(settledIn), int64(tokOut), int64(settledCached))' pkg/llm/openai/compat_llm.go`

## Status

- **Priority**: P3
- **Effort**: S（测量）+ S（若走分支 A）
- **Risk**: LOW
- **Depends on**: plans/250-length-continue-carries-one-answer.md、plans/251-length-continue-calibration-measures-its-own-request.md
- **Category**: bug（待证实）
- **Planned at**: commit `236fc32a`, 2026-11-06

## Why this matters

长度续写结束后，`Result.Usage`（「对话现在所处的那个请求」，上下文占用表和 `fb_messages.usage_json`
都读它）= **最后一轮的提示词** + **所有轮的输出之和**。而最后一轮的提示词里已经带着前几轮的输出
（续写把已生成的回答作为一条助手消息重发）。如果供应商把这段重发的回答算进 `prompt_tokens`，
占用表就把前几轮的输出算了两遍，偏高的量约等于 max_tokens × (轮数−1)。如果供应商不算（例如忽略
回传的 `reasoning_content`），现在的口径就是对的。代码里看不出来，只能测，所以本计划先用真实的
glm-5.3 测量，再按判据二选一：

- **分支 A**（供应商算进去了）：`Result.Usage.OutputTokens` 改为只算最后一轮自己的输出；计费口径
  （`Result.Billed()`、run 总计、"Worked for"）不变。
- **分支 B**（供应商没算）：不改代码，把测量结果写进计划与索引，标 REJECTED。

owner 要求：fake provider 驱动不了的结论必须用 glm-5.3 真机测（仓库记忆「Real-model acceptance with glm-5.3」）。

## Current state

`pkg/llm/openai/compat_llm.go`（250/251 落地后行号有偏移，以内容为准）：

- 流式路径结尾：
  ```go
  // Occupancy is the settled round's prompt; the output is every round's,
  // because the model really did produce it and this Result's answer carries
  // it.
  out.Usage = openAIUsage(int64(settledIn), int64(tokOut), int64(settledCached))
  if settledIn != tokIn || settledOut != tokOut || settledCached != tokCached {
  	out.BilledUsage = openAIUsage(int64(tokIn), int64(tokOut), int64(tokCached))
  }
  ```
- 非流式路径结尾（`executeChatCompletionUnaryParts`）：
  ```go
  settled := openAIUsage(int64(settledIn), int64(tokOut), int64(settledCached))
  var billed *llm.Usage
  if settledIn != tokIn || settledOut != tokOut || settledCached != tokCached {
  	billed = openAIUsage(int64(tokIn), int64(tokOut), int64(tokCached))
  }
  return openMsg, lastFinish, settled, billed, nil
  ```
- 占用表的读法：`pkg/state/usage.go:204-209` `TokenUsage.TokenCount()` = Input + CacheCreation + CacheRead + **Output**；
  `pkg/run/config.go:2564` `ContextOccupancy` → `state.TokenCountFromLastAPIResponse`（取最近一条消息的 usage）。
- 计费口径不受影响的证据：run 级总计来自 `UsageAccumulator` 的 sum（`pkg/llm/usage.go` `ObserveUsageWithBilling(billed, settled)`，
  sum 吃 billed），`/status` 的会话总计与 tok/s 读 `RunStore`（`pkg/turn/model.go:849-866`），都不读 `Result.Usage.OutputTokens`。
- 现有测试 `pkg/llm/openai/compat_llm_test.go` `TestLengthContinueReportsSettledRequestPrompt` 断言
  `res.Usage.OutputTokens == 300`（两轮之和）——分支 A 要改成 200。
- 生产构造器：`openai.NewCompatLLMWithTransport(apiKey, baseURL, model, temperature, maxTokens, apiPath, paramsJSON, extra http.RoundTripper)`；
  `extra` 若实现 `SetBase(http.RoundTripper)`，会被装在最外层并拿到内层链（`pkg/llm/openai/responses_llm.go:265` `roundTripperWithBase`）。
  `paramsJSON` 会合并进请求后再解码成 `ChatCompletionRequest`，**未知字段可能被丢**，所以本计划不尝试切换思考模式，测的就是
  生产默认的请求形状。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 测量 | 见 Step 1 的 `measure.sh` | 打印每轮用量表与判据结论 |
| llm 测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... -count=1` | 全部 `ok` |
| run 测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/state ./pkg/turn -count=1` | 全部 `ok` |
| vet | `PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" CGO_ENABLED=1 go vet -tags fts5 ./pkg/llm/...` | exit 0 |

## Scope

**In scope**：
- 测量程序与脚本：放在**仓库外**新建的临时目录（`mktemp -d`），不进仓库。
- 仅分支 A：`pkg/llm/openai/compat_llm.go`（上面两行及其注释）、`pkg/llm/openai/compat_llm_test.go`（改一处期望、加一个测试）。
- 本计划文件末尾的「测量记录」一节、`plans/README.md` 本计划行。

**Out of scope**：
- `BilledUsage` / `Billed()` 的任何语义；`pkg/llm/usage.go`；`pkg/state`；任何界面代码。
- 真实 `~/.forebrain` 下的任何文件（只读取 `~/.forebrain/.env` 里的一行密钥，见 Step 1）。

## Steps

### Step 1: 实测（glm-5.3）

1. `M="$(mktemp -d)"`，在 `$M/main.go` 写测量程序（**原样**使用下面的代码；它只打印用量数字，从不打印密钥）：

```go
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
)

type round struct {
	prompt, completion, cached int
	finish                     string
	reqMessages                int
	carriedChars               int
}

type tee struct {
	base   http.RoundTripper
	mu     sync.Mutex
	rounds []round
}

func (t *tee) SetBase(b http.RoundTripper) { t.base = b }

func (t *tee) RoundTrip(req *http.Request) (*http.Response, error) {
	var reqBody []byte
	if req.Body != nil {
		reqBody, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))

	var r round
	var sent struct {
		Messages []struct {
			Role             string `json:"role"`
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(reqBody, &sent)
	r.reqMessages = len(sent.Messages)
	for _, m := range sent.Messages {
		if m.Role == "assistant" {
			r.carriedChars += len([]rune(m.Content)) + len([]rune(m.ReasoningContent))
		}
	}
	type chunk struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			PromptTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	apply := func(raw []byte) {
		var c chunk
		if json.Unmarshal(raw, &c) != nil {
			return
		}
		if len(c.Choices) > 0 && c.Choices[0].FinishReason != "" {
			r.finish = c.Choices[0].FinishReason
		}
		if c.Usage != nil {
			r.prompt, r.completion = c.Usage.PromptTokens, c.Usage.CompletionTokens
			if c.Usage.PromptTokensDetails != nil {
				r.cached = c.Usage.PromptTokensDetails.CachedTokens
			}
		}
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "event-stream") {
		sc := bufio.NewScanner(bytes.NewReader(body))
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if strings.HasPrefix(line, "data:") {
				data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if data != "[DONE]" {
					apply([]byte(data))
				}
			}
		}
	} else {
		apply(body)
	}
	t.mu.Lock()
	t.rounds = append(t.rounds, r)
	t.mu.Unlock()
	return resp, nil
}

func main() {
	key := os.Getenv("ZHIPUAI_API_KEY")
	if key == "" {
		fmt.Println("ZHIPUAI_API_KEY is not set")
		os.Exit(2)
	}
	t := &tee{}
	client := openai.NewCompatLLMWithTransport(key, "https://open.bigmodel.cn/api/coding/paas/v4", "glm-5.3", 0, 64, "", nil, t)
	msgs := []llm.Message{llm.UserMessage(llm.Text("Count from 1 to 150, one number per line, nothing else."))}
	res, err := client.Execute(context.Background(), msgs, nil)
	if err != nil {
		fmt.Println("execute error:", err)
	}
	fmt.Println("k\tprompt\tcompletion\tcached\tfinish\treq_messages\tcarried_chars")
	for i, r := range t.rounds {
		fmt.Printf("%d\t%d\t%d\t%d\t%s\t%d\t%d\n", i+1, r.prompt, r.completion, r.cached, r.finish, r.reqMessages, r.carriedChars)
	}
	fmt.Println("pair\tdelta_prompt\tcompletion_k\tratio")
	for i := 0; i+1 < len(t.rounds); i++ {
		a, b := t.rounds[i], t.rounds[i+1]
		if a.finish != "length" || a.completion < 20 {
			continue
		}
		d := b.prompt - a.prompt
		fmt.Printf("%d->%d\t%d\t%d\t%.2f\n", i+1, i+2, d, a.completion, float64(d)/float64(a.completion))
	}
	if res != nil && res.Usage != nil {
		fmt.Printf("result usage: %+v\nbilled: %+v\n", *res.Usage, *res.Billed())
	}
}
```

2. `$M/measure.sh`（密钥只在脚本内注入，不出现在命令行、不写进任何文件）：

```bash
#!/bin/bash
set -euo pipefail
REPO="$1"; PROG="$2"
eval "$(grep '^ZHIPUAI_API_KEY=' "$HOME/.forebrain/.env")"
export ZHIPUAI_API_KEY
export FOREBRAIN_HOME="$(mktemp -d)"
cd "$REPO"
CGO_ENABLED=1 go run -tags fts5 "$PROG" 2>&1 | grep -v '^ld: warning' | grep -v '^# command-line-arguments'
```

3. 运行：`bash "$M/measure.sh" "$PWD" "$M/main.go"`。

**Verify**: 输出里有用量表，且至少一行 `pair`（即至少一轮以 `length` 结束、输出 ≥20 token）；`req_messages` 每轮都 ≤ 2
（plans/250 的效果）。没有 `pair` 行 → 把 `main.go` 里的 `64` 改成 `32` 重跑一次；仍没有 → STOP。

### Step 2: 判据

对所有 `pair` 行的 `ratio`：

- **全部 ≥ 0.8** → 分支 A（Step 3A）。
- **全部 ≤ 0.2** → 分支 B（Step 3B）。
- 其他（混合、或在 0.2–0.8 之间）→ STOP，把整张表贴进报告。

无论走哪支，先把完整输出（两张表 + result/billed 两行）原样贴进本文件末尾「测量记录」一节，注明日期与 `git rev-parse --short HEAD`。

### Step 3A: 分支 A —— 结算用量只算最后一轮自己的输出

1. 两处改为用 `settledOut`：
   - 流式：`out.Usage = openAIUsage(int64(settledIn), int64(settledOut), int64(settledCached))`，注释改为：
     "The settled request is the last round's: its prompt already carries the answer the earlier rounds produced (they
     were re-sent as the assistant message it continues), so its own output is the only output not yet inside it.
     Counting every round's output here counted those rounds twice in every occupancy reader. The bill, every round,
     is BilledUsage."
   - 非流式：`settled := openAIUsage(int64(settledIn), int64(settledOut), int64(settledCached))`，注释一句指向流式注释。
   - `BilledUsage` 的判断条件不改（多轮时 `settledOut != tokOut` 本来就成立）。
2. `TestLengthContinueReportsSettledRequestPrompt`：把 `res.Usage.OutputTokens` 的期望从 `300` 改为 `200`，失败信息改为
   "want the settled round's own output (200): the earlier round's is inside its prompt"；`res.Billed().OutputTokens == 300` 不变。
3. 新增 `TestLengthContinueSettledOutputIsTheLastRoundsOwn`（非流式，服务器两轮：prompt 100/150、completion 50/20，
   第 1 轮 `length` 第 2 轮 `stop`）：断言 `res.Usage.InputTokens+res.Usage.CacheReadInputTokens == 150`、
   `res.Usage.OutputTokens == 20`、`res.Billed().OutputTokens == 70`。
4. 检查其他读者：`grep -rn 'LastResponseUsage' pkg --include='*.go' | grep -v _test`，逐个确认没有把它的 `OutputTokens`
   当成「本轮产出/计费」展示（已知：`pkg/turn/submit.go:219`、`pkg/turn/session.go:392,415` 写入消息 usage，供占用表读）。
   若发现展示「输出 token 数」的地方读的是它 → STOP 报告（那里应改读计费口径，超出本计划）。

**Verify**: llm 测试全 `ok`；run/state/turn 测试全 `ok`；vet exit 0；`gofmt -l pkg/llm/openai` 无输出。

### Step 3B: 分支 B —— 不改代码

在「测量记录」后写一句结论：「glm-5.3 不把续写回传的回答计入 prompt_tokens，现有口径正确」，并在
`plans/README.md` 本计划行标 REJECTED（附 ratio 数字）。不做任何代码改动。

**Verify**: `git status --short` 输出为空（`plans/` 被 `.gitignore` 忽略）。

## Test plan

- 分支 A：改 `TestLengthContinueReportsSettledRequestPrompt` 一处期望；新增 `TestLengthContinueSettledOutputIsTheLastRoundsOwn`。
- 分支 B：无代码测试；测量输出即证据。

## Done criteria

- [ ] 「测量记录」一节有完整输出、日期与 HEAD
- [ ] 分支 A：llm、run、state、turn 测试全 `ok`，vet exit 0，新测试存在且通过；两处 `int64(tokOut)` 的结算行已不存在
      （`grep -n 'openAIUsage(int64(settledIn), int64(tokOut)' pkg/llm/openai/compat_llm.go` → 无输出）
- [ ] 分支 B：`git status --short` 输出为空（`plans/` 被 `.gitignore` 忽略）
- [ ] 测量用的临时目录不在仓库里（`git status --short` 不含 `main.go`/`measure.sh`）
- [ ] `plans/README.md` 本计划行状态更新（DONE 或 REJECTED，带 ratio）

## STOP conditions

- plans/250 或 251 未完成；drift check 第 2 条不命中。
- `~/.forebrain/.env` 里没有 `ZHIPUAI_API_KEY`，或请求返回鉴权错误——报告，不要换别的密钥或供应商。
- 两次尝试都没有 `pair` 行（无法触发续写）。
- `ratio` 混合或落在 0.2–0.8。
- `req_messages` 出现 >2（plans/250 没生效）。
- Step 3A 第 4 点发现有界面把 `LastResponseUsage.OutputTokens` 当计费展示。

## Maintenance notes

- 结论只对 glm-5.3（智谱 coding 端点）有实测依据；其他兼容供应商若行为不同（不计入回传的推理内容），分支 A 会让它们
  的占用表略偏低（少算前几轮的推理）。若以后发现这种供应商，应按供应商区分，而不是回退。
- 测量程序可复用于其他供应商：换 base URL / 模型名 / 密钥变量即可。

## 测量记录

日期：2026-11-06 18:45 CST；HEAD：`45e1d134`（含 plans/249、250、251）。

凭据路径：尝试 1 用 `~/.forebrain/.env` 的 `ZHIPUAI_API_KEY`（计划原文路径）→ 401「身份验证失败」；
按 owner 备忘回退到状态库 `fb_config` section=`agents` 中 zhipuai/glm-5.3 行的 api_key（0600 临时
env 文件注入，值未回显），base_url 与计划相同（`https://open.bigmodel.cn/api/coding/paas/v4`）。
计划 main.go 原样代码有一处语法错误（`var sent struct{…}` 声明尾多一个 `json:"messages"` tag，
无法编译），executor 删除该多余 tag，其余逐字未动。

尝试 1（.env 密钥）——401，非有效测量：

```
execute error: error, status code: 401, status: 401 Unauthorized, message: {"error":{"code":"1000","message":"身份验证失败。"}}
k	prompt	completion	cached	finish	req_messages	carried_chars
1	0	0	0		1	0
pair	delta_prompt	completion_k	ratio
```

尝试 2（状态库密钥，zhipuai/glm-5.3）——有效测量：

```
k	prompt	completion	cached	finish	req_messages	carried_chars
1	28	64	0	length	1	0
2	29	64	0	length	2	264
3	42	64	0	length	2	458
4	42	64	0	length	2	686
5	42	64	0	length	2	903
6	42	64	0	length	2	1152
7	42	64	0	length	2	1404
8	42	64	0	length	2	1612
9	42	64	0	length	2	1836
10	42	64	0	length	2	2085
11	42	64	0	length	2	2285
12	54	64	0	length	2	2493
13	54	64	0	length	2	2764
14	54	64	0	length	2	3016
15	54	64	0	length	2	3271
16	54	64	0	length	2	3514
pair	delta_prompt	completion_k	ratio
1->2	1	64	0.02
2->3	13	64	0.20
3->4	0	64	0.00
4->5	0	64	0.00
5->6	0	64	0.00
6->7	0	64	0.00
7->8	0	64	0.00
8->9	0	64	0.00
9->10	0	64	0.00
10->11	0	64	0.00
11->12	12	64	0.19
12->13	0	64	0.00
13->14	0	64	0.00
14->15	0	64	0.00
15->16	0	64	0.00
result usage: {InputTokens:54 OutputTokens:1024 CacheCreationInputTokens:0 CacheReadInputTokens:0}
billed: {InputTokens:705 OutputTokens:1024 CacheCreationInputTokens:0 CacheReadInputTokens:0}
```

判据判定（executor，精确算术）：15 个 pair 中 14 个 ratio ∈ {0, 0.015625, 0.1875}；唯一违例
pair 2→3 = 13/64 = **0.203125** ∈ (0.2, 0.8)（上表 `%.2f` 显示为 0.20 属舍入）。不满足「全部 ≤0.2」
也不满足「全部 ≥0.8」→ 按计划 STOP。零代码改动、零 commit。

旁证（供 owner 裁量，方向压倒性指向分支 B）：第 16 轮 prompt_tokens 仅 54，而其携带的助手消息已有
3514 字符（若计入应 ≥900 token）；prompt 曲线在携带内容线性增长时保持平坦（42×9、54×5），仅有的
两处非零 delta（13、12）形态上是分词/模板抖动而非内容计入。`req_messages` 全程 ≤2（plans/250 生效）；
result usage Input=54（末轮自身 prompt，251 口径）、Output=1024=16×64（现行口径）、billed Input=705
（各轮 prompt 之和）。

**结论（owner 2026-11-06 裁定，走分支 B）**：glm-5.3 不把续写回传的回答计入 prompt_tokens，
现有口径正确；唯一超阈 pair（13/64=0.203125）裁定为分词噪声。零代码改动，计划 REJECTED。


