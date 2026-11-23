# Plan 364: A tool result's image reaches Gemini — project it as a following user turn

> **Executor instructions**: Follow this plan step by step. Run every verification
> command and confirm the expected result before moving to the next step. If
> anything in the "STOP conditions" section occurs, stop and report — do not
> improvise. When done, update the status row for this plan in `plans/README.md`.
>
> **Drift check (run first)**:
> `git status --porcelain -- pkg/llm/google` and
> `git diff --stat 6541b8ab -- pkg/llm/google`.
> A `6541b8ab..HEAD` range diff is **always empty here** (the baseline is HEAD);
> use the two commands above. If `pkg/llm/google/agent_llm.go` moved, compare the
> "Current state" anchors against the live code before proceeding; on a mismatch,
> STOP.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW (one projection branch in one adapter; the text-only path is
  untouched and pinned by existing tests)
- **Depends on**: none
- **Category**: bug (`pkg/llm/google` silently drops pixels it was handed)
- **Planned at**: commit `6541b8ab`, 2026-11-23 — **found by live verification**, see
  "Evidence" below (nothing here is a hypothesis)

## Why this matters

`read_file` returns an image's pixels (since `19239c1d`), and the MCP converter
does the same. On every Gemini model those pixels **never reach the request**: the
Google adapter's tool projection keeps only the text half. The model is told
`image 64x64` and sees nothing, so a question about a screenshot is answered from
the note — confidently, and wrongly.

## Evidence — verified, not inferred

**Offline wire dump** (`go test -overlay` probe on `6541b8ab`, zero tree changes):
a tool-role message `ToolResultMessage("call-1", Text("image 64x64"),
ImageBase64("image/png", "iVBORw0KGgo="))` is projected by `buildRequest` to:

```json
{"contents":[
 {"role":"user","parts":[{"text":"read the picture"}]},
 {"role":"model","parts":[{"functionCall":{"name":"read_file","args":{}},"thoughtSignature":"skip_thought_signature_validator"}]},
 {"role":"user","parts":[{"functionResponse":{"name":"read_file","response":{"result":"image 64x64"}}}]}
],"generationConfig":{"maxOutputTokens":256}}
```

No `inlineData`, no base64 anywhere. `toolResponse`
(`pkg/llm/google/agent_llm.go:606-615`) is `llm.TextContent(parts...)` — the
image part is discarded before the request is built.

**Blast radius**: the catalog marks **58** `google/` models `attachment: true`
(`assets/models.json`, 64 `google/` rows in all), i.e. the capability gate lets
`read_file` hand pixels to every one of them. This is not a corner case.

**The parallel round, measured (2026-11-23 review, `go test -overlay` probe, zero
tree changes).** The same probe with **two** parallel `read_file` results, each
carrying an image, shows the unmodified tree producing **3** turns: user text,
model with 2 `functionCall` parts, and **one** user turn holding **2**
`functionResponse` parts. That merge is the invariant
`TestGoogleProjectsOneToolRoundIntoOneUserContent` (`agent_llm_test.go:1022`)
pins, and it exists because Gemini refuses a request whose functionResponse
count does not match the functionCall count of the turn it answers.

Appending an image turn naively after each result **breaks that merge**: the
guard at `:452` reads only the last content, so the second result sees the image
turn, fails `allToolResults`, and opens a *second* functionResponse turn.
Measured with the naive patch: **6 turns** — `[fnResp A] [img A] [fnResp B]
[img B]`. The existing text-only tests still pass (they carry no image), so the
regression ships silently. Step 2 therefore tracks how many image turns trail
the round's content and merges past them; with that correction the same probe
yields **5 turns** — `[user text] [model 2 calls] [fnResp A+B] [img A] [img B]` —
and the whole `pkg/llm/google` suite stays green. Step 3 test 2 pins exactly this.

The repo already encodes Gemini's rule in a fake gateway:
`TestGoogleGatewayAcceptsOneToolRoundAsOneUserContent` (`agent_llm_test.go:1079`)
serves a 400 with Gemini's own message unless the content **immediately after**
the model turn holds as many `functionResponse` parts as that turn has
`functionCall` parts (`:1095-1121`). The corrected shape satisfies it — both
results are in that one content; the naive shape puts one there and would be
refused. That test is text-only today and must stay unmodified.

**No live endpoint was available** on this machine (no Google credential
configured), so the fix is verified by wire shape only — the same standard the
Responses projection was held to, and it is the strongest evidence available here.
State that in your report rather than implying a live Gemini run.

## Current state

Anchors verified at `6541b8ab`:

| File | Line | Role |
|---|---|---|
| `pkg/llm/google/agent_llm.go` | `:382` | `buildRequest` — the one place messages become a request |
| `pkg/llm/google/agent_llm.go` | `:438-456` | the `llm.RoleTool` case: `toolResultPart` then the one-user-turn merge |
| `pkg/llm/google/agent_llm.go` | `:588-601` | `toolResultPart` — builds `part{FunctionResponse: …}` and nothing else |
| `pkg/llm/google/agent_llm.go` | `:606-615` | `toolResponse` — **the drop**: `llm.TextContent(parts...)` only |
| `pkg/llm/google/agent_llm.go` | `:514-540` | `userParts` — how an image becomes `part{InlineData: &inlineData{MIMEType, Data}}` (func at `:516`, signature `userParts(parts []llm.ContentPart) ([]part, error)`) |
| `pkg/llm/google/agent_llm.go` | `:573-583` | `allToolResults` — the merge guard's predicate (only a turn of pure functionResponse parts is extended) |
| `pkg/llm/google/agent_llm.go` | **`:452`** | the merge guard itself: `if n := len(req.Contents); n > 0 && req.Contents[n-1].Role == roleUser && allToolResults(req.Contents[n-1].Parts)` — **it inspects the LAST content only**. This is the line Step 2 must change; see the measured parallel-round fact below |
| `pkg/llm/google/agent_llm_test.go` | `:88` | the message-list builder inside `TestExecuteProjectsMessagesAndParsesMixedParts` (`:54`) — the `llm.Message` literal style to copy |
| `pkg/llm/google/agent_llm_test.go` | **`:1022`**, **`:1079`** | the two existing tool-round tests: `TestGoogleProjectsOneToolRoundIntoOneUserContent` and `TestGoogleGatewayAcceptsOneToolRoundAsOneUserContent`. **These are the structural pattern for the new tests** (both text-only today) |
| `pkg/llm/openai/responses_llm.go` | `:956-990` | **the pattern to copy**: tool output keeps the text, and the image parts go into a following user message with a lead line (`image read from tool result`, `:979`) |
| `pkg/llm/openai/responses_llm.go` | `:1483` | `toolResultImageParts` — the non-text-part filter. It is **private to `pkg/llm/openai`**; do not import it across adapters |

Repo conventions to match:

- Comments explain **why** and state the invariant — see the merge comment at
  `:443-451` and the Responses projection's comment. Match that voice.
- Test names in this package are sentences with a `TestExecute…`, `TestGoogle…`
  or `TestToolResult…` prefix — see
  `TestGoogleProjectsOneToolRoundIntoOneUserContent`
  (`pkg/llm/google/agent_llm_test.go:1022`). There is no `TestBuildRequest…` test
  today; the three new names below introduce that prefix deliberately, because
  they test `buildRequest` directly rather than through `Execute`.
- `pkg/architecture` caps a package at **34 production files**
  (`cache_test.go:1532`); this plan adds no file (`pkg/llm/google` has **3**:
  `agent_llm.go`, `doc.go`, `stream.go` — measure with
  `ls pkg/llm/google/*.go | grep -v _test | wc -l`).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Format | `gofmt -l pkg/llm/google` | no output |
| Vet | `go vet ./pkg/llm/google/...` | exit 0 |
| Adapter tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/google/... -count=1` | ok |
| Full suite | `make test` | exit 0 |

## Scope

**In scope**: `pkg/llm/google/agent_llm.go`, `pkg/llm/google/agent_llm_test.go`.

**Out of scope**: every other adapter (`anthropic`, `openai/*`), `pkg/tool`,
`pkg/mcp`, `orchestration_llm.go` (it already passes `[]llm.ContentPart`
through), `assets/models.json`, and the OpenAI-family "strict endpoint" shape
change once proposed as plan 365 (**REJECTED** — its premise was disproved; see
`plans/365-tool-images-leave-the-tool-message.md`).

## Design decisions the executor must not re-litigate

**1. Keep `functionResponse` text-only and add a following user turn.** Gemini
requires the count of `functionResponse` parts to equal the `functionCall` parts of
the turn it answers; putting `inlineData` inside that same user turn risks breaking
that count. A separate following user turn carries the pixels, exactly as the
Responses adapter already does for the same reason.

**2. Reuse `userParts` for the projection.** It is the code that turns an image
part into `inlineData`; do not write a second encoder.

**3. A lead line, byte-identical to the Responses one.** Put
`llm.Text("image read from tool result")` first, then the image parts — the two
adapters then describe the same fact the same way, which is what a future reader
expects. (Responses: `responses_llm.go:979`.)

**4. Text-only tool results project byte-for-byte as they do today.** No session's
request changes when there is no image: the branch must be skipped when
`toolResultImageParts(m.Parts)` is empty. Existing tests prove this.

**5. Do not add a capability check.** Whether the session model takes images is
decided upstream (`read_file`'s `ImageInputRefusal` gate); a part only exists here
if it was allowed. A second check would silently disagree with the first.

## Git workflow

- Branch: `advisor/364-gemini-tool-result-images`
- Conventional Commits, lowercase imperative, optional `(scope)`, matching recent
  history (`fix(tool): read-on-edit/read-on-write replaces must-read gate`). Use
  e.g. `fix(google): a tool result's image reaches the model as its own user turn`.
- Do NOT push and do NOT open a PR unless the operator instructed it.
- Dispatched into an isolated workspace, your branch lives in
  `.worktrees/<short-kebab-name>` under the project's main worktree.

## Steps

### Step 1: Reproduce the drop with a test first

Add to `pkg/llm/google/agent_llm_test.go`, modelled on the existing tool-round
test `TestGoogleProjectsOneToolRoundIntoOneUserContent` (`:1022`) — same
`buildRequest(history, nil, 0, "gemini-3.5-pro")` call, same `llm.Message` literal
style as `:88`:

`TestBuildRequestProjectsToolResultImagesAsAUserTurn` — messages:

```go
[]llm.Message{
    llm.UserMessage(llm.Text("read it")),
    llm.AssistantMessage(nil, llm.ToolCall{ID: "call-1", Type: "function",
        Function: llm.FunctionCall{Name: "read_file", Arguments: `{}`}}),
    llm.ToolResultMessage("call-1", llm.Text("image 64x64"),
        llm.ImageBase64("image/png", "iVBORw0KGgo=")),
}
```

Assert, after `buildRequest(...)` and `json.Marshal`:

1. `contents` has **four** turns: user text, model functionCall, user
   functionResponse, user image turn;
2. the fourth turn's parts are `[{text:"image read from tool result"},
   {inlineData:{mimeType:"image/png", data:"iVBORw0KGgo="}}]`;
3. the third turn's functionResponse `response` holds only `{"result":"image 64x64"}`
   — no base64, no `inlineData`;
4. no other turn carries `inlineData`.

**Verify**: the test **fails** on the current tree (that is the reproduction), and
`CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/google/ -run TestBuildRequestProjectsToolResultImagesAsAUserTurn -v`
prints `--- FAIL` with the missing-turn assertion. If it passes, STOP — the drop
is already fixed and this plan is stale.

### Step 2: Implement the projection without splitting the round

Four edits, all in `pkg/llm/google/agent_llm.go`. The naive version — append an
image turn after each result and leave the merge guard alone — was measured to
split a parallel round into two functionResponse turns (see "The parallel round,
measured" above). Do not write that version.

2a. Beside `turnStart := 0` in `buildRequest`, declare the counter:

```go
	// trailingImageTurns counts the image-only user contents this projection
	// appended after the current round's functionResponse content. The merge
	// below looks past them: a round's results must stay in ONE content.
	trailingImageTurns := 0
```

2b. Reset it wherever a **non-tool** turn is appended — the `llm.RoleUser`
case's `if len(parts) > 0` block, and both `llm.RoleAssistant` append sites (the
replay-layout path and the ordinary path). One line at each: `trailingImageTurns = 0`.
Do **not** reset on the thinking-fence `continue`: that path appends nothing, and
today's guard would still merge into the previous round's content.

2c. In the `llm.RoleTool` case, merge past the trailing image turns, then append
the image turn:

```go
			if n := len(req.Contents) - trailingImageTurns; n > 0 && req.Contents[n-1].Role == roleUser && allToolResults(req.Contents[n-1].Parts) {
				req.Contents[n-1].Parts = append(req.Contents[n-1].Parts, resultPart)
			} else {
				// A tool result is a user turn in generateContent's model.
				req.Contents = append(req.Contents, content{Role: roleUser, Parts: []part{resultPart}})
				trailingImageTurns = 0
			}
			// A tool result's pixels do not fit in functionResponse, which is a
			// JSON object: Gemini would receive the note and nothing else. They
			// follow as their own user turn, the same shape the Responses
			// adapter uses for the same reason. The round's functionResponse
			// content stays the merge target for the next result — the counter
			// above is what keeps this turn from hiding it.
			if imageParts := toolResultImageParts(m.Parts); len(imageParts) > 0 {
				projected := append([]llm.ContentPart{llm.Text("image read from tool result")}, imageParts...)
				parts, err := userParts(projected)
				if err != nil {
					return nil, err
				}
				if len(parts) > 0 {
					req.Contents = append(req.Contents, content{Role: roleUser, Parts: parts})
					trailingImageTurns++
				}
			}
```

2d. Add the filter to the same file (the openai one is private to its package —
**do not import across adapters**). Note what it really does: it keeps **every
non-text part**, not only images — the name is the openai adapter's, and this
plan keeps it so the two adapters read alike. No in-repo tool builds an image-URL
tool result (`grep -rn "ImageURL(\|ContentTypeImageURL" pkg/tool/*.go pkg/mcp/*.go`
outside tests: zero hits), so the wider filter needs no extra branch; if one ever
appears, `userParts` refuses it with an actionable error (`:539-540`) instead of
dropping it silently.

```go
// toolResultImageParts returns the non-text parts of a tool result: its pixels,
// which functionResponse cannot carry.
func toolResultImageParts(parts []llm.ContentPart) []llm.ContentPart {
	var imageParts []llm.ContentPart
	for _, part := range parts {
		if part.Type != llm.ContentTypeText {
			imageParts = append(imageParts, part)
		}
	}
	return imageParts
}
```

Notes the implementation must respect:

- `userParts` returns `([]part, error)` (`:516`); the snippet already matches it.
- `allToolResults` stays in use — the merge guard still calls it. Nothing in this
  change becomes dead code, so nothing may be deleted.
- With no image anywhere, `trailingImageTurns` is always 0 and every expression
  reduces to today's: the text-only projection is byte-identical.
- One image turn per result message, in arrival order, all **after** the round's
  functionResponse content. For two parallel image results that is 5 turns:
  `[user text] [model 2 calls] [fnResp A+B] [img A] [img B]`. Step 3 test 2 pins it.
- This shape is verified offline only. Whether Gemini accepts two consecutive
  user turns after the functionResponse turn is **not** live-verified (no Google
  credential here). If a live run rejects it, report the request and response —
  do not merge the pixels into the functionResponse turn to make it pass.

**Verify**: `go build ./pkg/llm/google/...` → exit 0; the Step 1 test PASSes;
`CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/google/... -count=1` → ok with **zero
changes to existing tests**.

### Step 3: The text-only and parallel-call guards

Add, in the same file:

1. `TestBuildRequestKeepsTextOnlyToolResultsUnchanged` — the same three messages
   without an image: the marshalled request must contain **no** `inlineData` and
   **no** `"image read from tool result"`, and the turn count stays three.
2. `TestBuildRequestGivesEachParallelToolResultItsOwnImageTurn` — one assistant
   turn with two calls (`call-a`, `call-b`), each answered by a result carrying an
   image. Assert the **measured** shape: **5** turns total; turn index 2 is one
   user content holding **2** `functionResponse` parts (assert the count — this is
   the assertion the naive implementation fails); turns 3 and 4 are user contents
   each holding the lead text plus its own `inlineData`; **no other turn holds a
   `functionResponse`**. Model it on `TestGoogleProjectsOneToolRoundIntoOneUserContent`
   (`:1022`), which pins the same merge for text-only results.

**Verify**:
`CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/google/ -count=1 -run 'TestBuildRequest' -v | tee /tmp/google.log`,
then `grep -c '^--- PASS' /tmp/google.log` ≥ the number of `TestBuildRequest…`
tests in the file, and `grep -c '^--- FAIL' /tmp/google.log` → 0. (A `-run` that
selects nothing still exits 0 — count the PASS lines.)

### Step 4: Full gates

1. `gofmt -l pkg/llm/google` → no output
2. `go vet ./pkg/llm/google/...` → exit 0
3. `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/google/... ./pkg/llm/... ./pkg/run/... -count=1` → ok
4. `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/... -count=1` → PASS
5. `make test` → exit 0
6. Live, if and only if a Google credential is available in your environment
   (`FOREBRAIN_...` or the operator's configured model): read a PNG through
   `read_file` in a session on a Gemini model and confirm the model describes it.
   When no credential exists, mark this **UNVERIFIED (no credential)** in the
   report — do not claim a live run you did not make.

## Test plan

New: three tests in `pkg/llm/google/agent_llm_test.go` (Steps 1 and 3). Structural
pattern: `TestGoogleProjectsOneToolRoundIntoOneUserContent` (`:1022`) and the
message builder at `:88`.
Verification: `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/google/... -count=1`.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] The Step 1 test failed before the change and passes after (paste both outputs)
- [ ] `grep -n "image read from tool result" pkg/llm/google/agent_llm.go` finds it
- [ ] No second encoder was added: `grep -c "inlineData{" pkg/llm/google/agent_llm.go`
      is still **2** (the two `userParts` sites, `:525` and `:538`), and
      `grep -c "inlineData" pkg/llm/google/agent_llm.go` is still **5** — the
      projection reuses `userParts` and adds no `inlineData` literal of its own
- [ ] `grep -n "trailingImageTurns" pkg/llm/google/agent_llm.go` shows the
      declaration, the resets and the merge guard's use of it
- [ ] Text-only tool results marshal byte-identically to before (Step 3 test 1)
- [ ] All three new tests pass; `make test` green; `gofmt -l` clean; `go vet` exit 0
- [ ] `git status --porcelain` lists only the two in-scope files
- [ ] The live run, or `UNVERIFIED (no credential)`, is in the report
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- The Step 1 test passes on the unmodified tree — the drop this plan describes is
  gone; report what changed and do not proceed.
- `buildRequest`'s tool case has been restructured since this plan was written (the
  merge/append block no longer matches `:438-460`).
- Adding the user turn breaks Gemini's functionResponse/functionCall count rule in
  any existing test — report the failing test and the request that broke it rather
  than merging the image into the functionResponse turn.
- You need to change `toolResponse`'s JSON contract, `allToolResults`, or the
  reasoning-signature placeholder loop. Those are other invariants; report instead.
- A live Google run is available and shows the model still not seeing the image
  with the new projection in place. That is a new fact about the endpoint; report
  the request and response, do not iterate blindly.

## Maintenance notes

- **The OpenAI-family adapters need no companion fix.** Plan 365 proposed moving
  the compat adapter's image to a user turn and was **REJECTED on 2026-11-23**:
  the 400 it cited came from a catalog-text-only model (`zhipuai/glm-5.3`,
  `attachment: false`), which refuses an image in a user message too, while every
  VL endpoint tested accepted the tool-message shape. The Responses adapter
  already projects a following user message because `function_call_output` is
  text-only by protocol. This plan is the only adapter with a real drop.
- **The lead line is shared vocabulary** with the Responses adapter
  (`pkg/llm/openai/responses_llm.go:979`). If one changes, change both; a future
  reader comparing them should find the same sentence.
- **`userParts` is now on a second path.** If it ever gains a per-turn limit (a
  max parts count, a total-byte budget), the tool-result projection inherits it —
  which is the behavior you want, but say so in the review.
- **Reviewer scrutiny**: the parallel-call test. One functionResponse content per
  round, with one image turn per result message after it, is the invariant that
  keeps a round of N parallel image reads from splitting into N functionResponse
  turns (which Gemini refuses) or collapsing into one turn with N images out of
  order. The `trailingImageTurns` resets are the other place to look: a missed
  reset merges a later round's results into an earlier round's content.
