# Plan 365: REJECTED — the 400 came from a text-only model, not from the tool-message shape

- **Category**: bug (`pkg/llm/openai`)
- **Priority**: — (rejected)
- **Status**: **REJECTED on 2026-11-23**, when the owner asked what the Zhipu 400
  actually meant. The measurements below replace this plan's premise; there is no
  defect to fix.
- **Depends on**: none. Related: `plans/364` (the Google adapter **drops** the
  image — proved offline — which is a real bug and stands).
- **Planned at**: commit `6541b8ab`, 2026-11-23; rejected at the same commit.

> **Do not execute this plan.** Moving a tool result's image into a following user
> message would change the wire shape for every Chat Completions provider on a
> premise that two measurements disprove.

## What this plan claimed

That a tool result's image must not travel on the `role: "tool"` message, because
strict Chat Completions endpoints reject it — with a Zhipu 400 as the evidence:

```json
{"error":{"code":"1210","message":"messages.content.type 参数非法，取值范围 ['text']"}}
```

## What the measurements actually show

**1. The 400 is a capability answer, not a shape answer.** It comes from
`zhipuai/glm-5.3`, which the catalog marks **text-only**:

```
assets/models.json  zhipuai/glm-5.3        -> attachment: false, modalities.input: ["text"]
assets/models.json  zhipuai/glm-5.3-flash  -> attachment: true,  modalities.input: ["text","image","video","pdf"]
```

(The key path is `modalities.input`; there is no row-level `input` field in
`assets/models.json`.)

Move the image to a **user** message and `glm-5.3` returns the same 400:

| Model | image on the tool message | image in a user message |
|---|---|---|
| `zhipuai/glm-5.3` (text-only) | 400 `messages.content.type … ['text']` | **400, identical** |
| `zhipuai/glm-5.3-flash` (VL) | **429 quota** — the request validated | **429 quota** — validated |

429 is the account's weekly quota, i.e. the request passed validation and only
billing stopped it. So the image's position is irrelevant; only the model's input
modalities matter.

**2. Every endpoint tested accepts the tool-message shape.**

| Endpoint / model | Result |
|---|---|
| DeepSeek `api.deepseek.com` `deepseek-flash` | colour-tracked 3/3 |
| DashScope `deepseek-v4.1-flash-1017` | red/blue exact, one flat-green misread in 3 |
| custom `qwen4-max-preview` (`/compatible-mode/v1`) | colour-tracked 3/3, both shapes |
| `qodercn/GLM-5.3-Flash` (the plugin's own client) | 4-digit nonce read 6/6; no-image control refuses |
| Zhipu `glm-5.3-flash` | validated (quota 429) |

**3. The product already handles the text-only case.** `Runner.ImageInputRefusal`
refuses images for a model the catalog marks non-VL: `glm-5.3` is
`attachment: false`, so `LookupRow` answers `ok=true, SupportsAttachments=false`
and `read_file` tells the user to switch model with `/model` instead of sending a
request that would 400. The gate's fail-open path is for models the catalog does
not know — verified separately on the capless custom row.

## What remains true, and is already implemented

- **Responses API**: `function_call_output` is text-only by protocol, so the
  Responses adapter already moves a tool result's image into a following user
  message (`pkg/llm/openai/responses_llm.go:956-990`), pinned by
  `TestResponsesToolResultImageProjectsAsFollowingUserMessage`
  (`responses_llm_test.go:1842`) and now verified live on
  `https://api.longcat.com/openai/v1/responses` with a 4-digit nonce. Nothing to do.
- **Anthropic Messages**: an image block inside `tool_result` is valid; three
  vendors were verified live (volcengine, DeepSeek `/anthropic`, Qwen MaaS).
- **Google**: still broken — `toolResponse` keeps `llm.TextContent` only, so the
  request carries no `inlineData`. That is `plans/364`: a drop, not a rejection.

## If the owner still wants the user-turn shape

A user turn is a *portable* shape and two adapters already use it, so preferring it
everywhere is a defensible design choice — but it is a choice, not a bug fix, and it
would touch every provider's wire shape. If you want it, write a new plan that says
so and measures the cost; this file's evidence does not support one.

## Open follow-up this plan used to carry (not filed as its own plan)

The repo's gated live test `TestCompatToolResultImage`
(`pkg/llm/openai/compat_llm_test.go:1614-1658`, gated on
`FOREBRAIN_E2E_DEEPSEEK_KEY`) **names** `/tmp/red.png` in the tool arguments
(`:1635`) and in the user text (`:1639`) — it never writes that file; the pixels
come from `redSquarePNG` (`:1580`) in memory — and asserts the answer contains
"red" or "红" (`:1653`). A model that never received the image passes it by reading
the path (observed: a text-only model answered "red" while its reasoning quoted
`[Unsupported Image]`). The fix is small and fully specified: a neutral path name
in both strings (`/tmp/pic.png`), two squares with different colours per run, and
an assertion that each answer matches **its own** colour, plus a text-only
negative control that must fail. It belongs to whoever next touches that file; it
is not worth a plan of its own.

## Lessons kept from this plan

1. **A 400 is not automatically a shape error.** Read the model's declared
   modalities before blaming the projection: a text-only model refuses an image
   wherever it sits.
2. **A colour answer can come from the filename.** The same review round found the
   repo's own gated test doing exactly that (`/tmp/red.png` + assert "red", which a
   blind model passes). The rigorous discriminator is a nonce drawn only in the
   image, plus a no-image control that must fail to answer.
3. **One sample is not a rate.** A single "1771" for a 4-digit nonce was a
   transient misread: six repeats read it correctly, and a second adapter reading
   the same PNG confirmed the image was legible.
