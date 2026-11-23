# Plan 363: An image that a search hit or an MCP resource names becomes pixels — `read_file` takes a list, and embedded-resource images are parts

> **Executor instructions**: Follow this plan step by step. Run every verification
> command and confirm the expected result before moving to the next step. If
> anything in the "STOP conditions" section occurs, stop and report — do not
> improvise. When done, update the status row for this plan in `plans/README.md`.
>
> **Drift check (run first)**:
> `git status --porcelain -- pkg/tool pkg/mcp` and
> `git diff --stat 6541b8ab -- pkg/tool pkg/mcp`.
> A `6541b8ab..HEAD` range diff is **always empty here** (the baseline is HEAD);
> use the two commands above, which see uncommitted work. If an in-scope file
> moved, compare the "Current state" anchors against the live code before
> proceeding; on a mismatch, STOP.

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: LOW–MED — one optional input field on `read_file` (which changes the
  cached tool schema **once**) and one added block shape in the MCP result
  converter. Both paths already exist for the single-path / `type: "image"` case,
  so this is an extension, not new machinery.
- **Depends on**: none. `plans/359` was rejected (reading an image is shipped);
  this plan covers what 359 was re-scoped to by owner decision 2026-11-23:
  **extend image reading beyond `read_file`**.
- **Blocks**: nothing
- **Category**: feature / multimodal
- **Planned at**: commit `6541b8ab`, 2026-11-23 (owner ruling: 「359 按『扩展到
  read_file 之外（grep/glob 命中、MCP 结果里的图）』另立新计划」)

## Why this matters

Two places hand the model the *name* of an image and nothing it can look at.

1. **A search hit.** forebrain has no `grep`/`glob` tool: a search is the `shell`
   tool running `rg`, `find`, `ls` or `git ls-files`, and its output is paths.
   `read_file` turns one path into pixels — so a hit list of five screenshots is
   five calls, five schema round trips and five separate results to read. The
   model that just searched is the model most likely to want the pictures.
2. **An MCP image delivered as an embedded resource.** An MCP server may return an
   image two ways: a `{"type":"image","data":…}` block (already pixels — see
   Current state) or a `{"type":"resource","resource":{"blob":…,"mimeType":"image/png"}}`
   block. The second is persisted and named in the text envelope but **no image
   part is built**, so the model is told "Binary content saved to …png" and must
   spend a turn reading it back.

Both fixes are small because the hard parts exist: `read_file` already builds an
image part with a budget, a downscale ladder and a one-line note; the MCP
converter already builds parts for `type: "image"` blocks with the same budget,
the same degradation rules and the same per-session refusal gate.

## Current state

Anchors verified at `6541b8ab`:

| File | Line | Role |
|---|---|---|
| `pkg/tool/file_tools.go` | `:64-68` | `FileReadInput` — `file_path`, `offset`, `limit`, `pages`. **One path only** |
| `pkg/tool/file_tools.go` | `:71-75` | `NewFileReadTool`, with the description that already says "or an image (png/jpeg/gif/webp) as an image" |
| `pkg/tool/file_tools.go` | `:100-123` | the image branch: `ImageInputRefusal` gate (`:109`) → `imageReadPart(abs, raw, 0)` (`:116`) → `ImageReadOutput` + `[]llm.ContentPart{Text(imageReadNote(...)), part}` (`:120-123`) |
| `pkg/tool/file_tools.go` | `:121-160` | the PDF branch beside it — the precedent for "several images in one result" (`pdf.DefaultMaxPages = 5`, `pkg/pdf/render.go:21-24`) |
| `pkg/tool/file_tools.go` | `:391-437` | `imageReadPart` — sniff, four accepted types, `llm.ImagePartWithin` (budget `0` = `DefaultMaxImageBytes` 3 MiB + `$FOREBRAIN_IMAGE_MAX_BYTES`) |
| `pkg/tool/file_tools.go` | `:443-466` | `imageReadNote` / `ImageReadOutput` (surface metadata, never base64) |
| `pkg/tool/file_tools.go` | **`:283`** | `authorizeRead(ctx context.Context, st *State, toolName, filePath string) (readPathResolution, error)` — **four arguments**, and it returns a resolution, not a path. The single-path call is `authorizeRead(ctx, st, "read_file", in.FilePath)` (`:82`); the bytes are read from `resolution.Abs` (`permissions.go:225-233`). It can return `*RequiresActionError` (`:324`) — the FOREBRAIN_HOME approval signal |
| `pkg/tool/file_tools.go` | `:198` | `st.RememberReadForSession(...)` — the read-baseline ledger write, on the **text** path only; `write_file`/`edit_file` consume it via `readStateForEdit` (`:945`, `:1170`) |
| `pkg/tool/state.go` | `:2155` | `State.ImageInputRefusal(ctx)` — the per-session "this model takes no pixels" gate (doc comment `:2141-2154`) |
| `pkg/tool/state.go` | `:844`, `:895`, `:899` | `CaptureToolCompletion` **replaces** the whole captured payload (last capture wins); `CaptureToolOutput` and `CaptureToolError` are its two wrappers. `authorizeRead` captures an error on a denied path (`file_tools.go:287`, `:301`, `:313`) |
| `pkg/run/orchestration_llm.go` | `:1424` | `isRequiresActionError` — the layer that turns a returned `*RequiresActionError` into an approval. A list read that swallows it into a text line deletes the approval |
| `pkg/llm/image_scale.go` | `:24-25`, `:46` | `DefaultMaxImageBytes = 3 << 20`, `ImageMaxBytesEnv = "FOREBRAIN_IMAGE_MAX_BYTES"` |
| `pkg/mcp/result_processing.go` | `:78` | `ToolResultContent` calls `extractResultImageParts` **first**, then `processToolResultJSON` |
| `pkg/mcp/result_processing.go` | `:113-226` | `extractResultImageParts` — walks `content[]`, handles `type == "image"` only; persists, builds a part or degrades to a saved-path line |
| `pkg/mcp/result_processing.go` | `:331-400` | `persistBinaryResourceContent` — walks `type == "resource"` blocks with a `blob`, persists it and leaves a saved-path line. **No part, ever** |
| `pkg/mcp/result_processing.go` | `:298-325` | `resultJSONContainsImage` — the "never offload base64 to disk" guard |
| `pkg/mcp/result_processing_test.go` | `:91`, `:128`, `:182`, `:208` | the four tests this extension must keep green: resource **PDF** blob stays a saved-path line; image block → parts; no base64 in text; refusal → text only |

Two facts that keep the plan honest:

- **Reading an image is not the gap.** `read_file` has returned png/jpeg/gif/webp
  pixels and rendered PDF pages since `19239c1d` (2026-10-15); `plans/359` was
  rejected for re-proposing it.
- **An MCP `type: "image"` block is not the gap either.** It already reaches the
  model as a part, including the refusal gate and the degrade paths
  (`TestToolResultContentReturnsImageParts`, `…RefusalReturnsTextOnly`).
- **The schema tests that exist will not stop you.** `read_file`'s schema is pinned
  by `TestFileReadSchemaOnlyRequiresFilePath` (`file_tools_test.go:426`, asserts
  `required == ["file_path"]`) and `TestFileReadSchemaDocumentsClamping` (`:441`,
  checks the `offset`/`limit`/`pages` descriptions). An optional `paths,omitempty`
  field changes neither, and both files are in scope. No golden file pins the
  tool's description (`registry.go:129` holds a prefix of it, unchanged).

Repo conventions to match:

- Comments explain **why** and state the invariant — see `imageReadPart`'s doc
  comment and `extractResultImageParts`'s. Match that voice.
- Test names in these packages are sentences: `TestFileReadReturnsImagePartsForImageFile`
  (`pkg/tool/file_tools_test.go:2361`), `TestToolResultContentDegradesUnsendableImageBlock`
  (`pkg/mcp/result_processing_test.go:232`).
- `pkg/architecture` caps a package at **34 production files**
  (`maxProductionFilesPerPackage`, `pkg/architecture/cache_test.go:1532`; the doc
  comment at `:1461-1462` still says 30 and is stale). `pkg/tool` is at 33 now and
  plan 358 takes the last slot, so **this plan adds no production file**;
  `pkg/mcp` is at 20.

### What each adapter does with a tool result's image — verified 2026-11-23

The read → tool-result → model path was verified end to end, because whether the
pixels arrive is not a property of `read_file` but of each adapter's projection.
This plan returns `[]llm.ContentPart` exactly like `read_file` does, so it inherits
this table:

Method: a model asked "what colour" can read the **filename**, so a single
colour proves nothing (the repo's own gated test falls into this: it names
`/tmp/red.png` in the tool arguments and the prompt and asserts "red" —
`pkg/llm/openai/compat_llm_test.go:1614-1658`). Every live result below is a
**colour-tracking
matrix** — the same neutral path `/tmp/pic.png` with a red, then green, then blue
square; an answer that follows the colour is vision.

| Adapter | Projection | Status |
|---|---|---|
| Anthropic Messages (`pkg/llm/anthropic`) | image block **inside `tool_result`** | **live ✓, colour-tracked** on three vendors: volcengine ark `/api/plan` `kimi-k3` (3/3), DeepSeek `https://api.deepseek.com/anthropic` `deepseek-flash` (3/3, and the repo's own `pkg/llm/anthropic` client reproduced it), and Qwen MaaS `https://token-plan.maas.qianwenaiapi.com/apps/anthropic` (the owner's endpoint) `qwen4-max-preview` — **the repo's own `pkg/llm/anthropic` client ran three colours through `tool_result` and answered red/green/blue 3/3**; curl sampling of the same shape was 5/6 (one blue read "red"), the user-message shape 6/6, and a no-image control makes the model refuse ("I can't read that file"), so the answers come from pixels. (The `maas.qianwenaiapi.com` host the owner first named by mistake denies model access to every configured key — 401 on the custom row's key, 403 on the only other Aliyun key, which can list 191 ids there but invoke none. Nothing to verify on that host.) A text-only model there (`deepseek-v4-pro`) gets a 200 with the literal `[Unsupported Image]` in the block's place — degraded, not rejected |
| OpenAI Responses (`pkg/llm/openai/responses_llm.go`) | text `function_call_output` + **following user message** with the image | **live ✓** on `https://api.longcat.com/openai/v1/responses` (`meituan/LongCat-2.5`, which declares `input_modalities: [text,image,video]`): the repo's own Responses client read a 4-digit nonce **drawn in the image and never mentioned in text** — `4287` → "4287", `5518` → "5518" (a third code hit `max_output_tokens` before answering). Projection also pinned by `TestResponsesToolResultImageProjectsAsFollowingUserMessage` (`responses_llm_test.go:1842`). Colour questions are worthless on this model: a **no-image control answered "The shape is blue."** |
| OpenAI Chat Completions / compat (`pkg/llm/openai/compat_llm.go`) | `image_url` **on the tool message** | mixed: **live ✓ colour-tracked 3/3** on DeepSeek `api.deepseek.com` `deepseek-flash`; **live ✓** DashScope `deepseek-v4.1-flash-1017` (red/blue exact, a flat green read "White" in 1 of 3 — the endpoint's vision, not the shape); **live ✓ nonce-tracked 6/6** on `qodercn/GLM-5.3-Flash` (wire id `gfmodel`, the plugin's own client; a 4-digit number drawn only in the image was read correctly 6/6 with the image on the tool message, and the no-image control answers "I don't see any image in our conversation"); **live ✓ colour-tracked 3/3** on the user's own custom row `custom/qwen4-max-preview` (`token-plan.maas.qianwenaiapi.com`), on the tool message **and** in a user message; `deepseek-v4-pro` accepts and degrades to `[Unsupported Image]`; **no rejection anywhere**: Zhipu's VL model `glm-5.3-flash` **validated** the same shape (429 quota, not 400), and the 400 that once looked like a shape error comes from `glm-5.3`, a catalog-text-only model that refuses an image **wherever it sits** — in a user message too (see plan 365, rejected) |
| Google Gemini (`pkg/llm/google`) | **dropped** — `toolResponse` keeps `llm.TextContent` only; the request carries no `inlineData` | offline wire dump proves the drop; no Google credential here → **plan 364** |

**Configuring that endpoint.** It speaks Messages, so a row for it needs
`base_url = https://token-plan.maas.qianwenaiapi.com/apps/anthropic` and
`api_path = /v1/messages`: `isMessagesAPIPath` selects the Anthropic client on a
path ending in `/messages` (`pkg/llm/opencode/endpoints.go:203-205`), and that
client appends `/v1/messages` to the base (`:207-210`). The shipped
`custom/qwen4-max-preview` row points at `/compatible-mode/v1` with
`/chat/completions` instead — the same model over the other protocol, verified
separately above. Both paths deliver pixels; the Messages one is what the owner
asked about.

Consequences for this plan: nothing about `paths` changes, but its Step 6.6 live
check must run on a provider row that appears above as **live ✓** (Anthropic
Messages or DashScope), or wait for 364. Do not report a Gemini/Zhipu failure
of the image as a failure of this plan.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Format | `gofmt -l pkg/tool pkg/mcp` | no output |
| Vet | `go vet ./pkg/tool/... ./pkg/mcp/...` | exit 0 |
| Tool tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/... -count=1` | ok |
| MCP tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/mcp/... -count=1` | ok |
| Full suite | `make test` | exit 0 |

(`make test` is `CGO_ENABLED=1 go test -tags fts5 ./... -count=1`, Makefile:103-104.)

## Scope

**In scope** (the only files you should modify):

- `pkg/tool/file_tools.go` — the `Paths` field, its handler branch, one helper
- `pkg/tool/file_tools_test.go`
- `pkg/mcp/result_processing.go` — embedded-resource image blocks
- `pkg/mcp/result_processing_test.go`

**Out of scope** (do NOT touch, even though they look related):

- **No new tool and no new file.** No `grep`/`glob`/`search_images` tool: forebrain
  has none today (`pkg/tool/registry.go:125-210` registers `read_file`,
  `write_file`, `edit_file`, `shell`, `retrieve_output`, `web_fetch`, `web_search`,
  `request_permissions`, `lsp`, `present_files`, and the session/memory tools), and
  a new tool changes the cached prefix shape for every session.
- `pkg/run/orchestration_llm.go`. `toolResultContentParts` already passes
  `[]llm.ContentPart` through; editing it means you have misdiagnosed something.
- `pkg/llm/image_scale.go`, `pkg/llm/image_heal.go`. The budget, the downscale
  ladder and the degrade-on-refusal retry are already correct; reuse them.
- `pkg/tool/file_tools.go`'s **existing** single-path, text and PDF branches. They
  stay byte-for-byte as they are — `paths` is a new branch in front of them.
- `pkg/skill/system_assets/`. `run` and `run-skill-generator` already say
  `read_file` returns an image's pixels; teaching them the list form is a follow-up
  (Maintenance).
- Docs/READMEs.

## Design decisions the executor must not re-litigate

**1. Pixels go through `llm.ImagePartWithin`; text stays one note per file.** No
new encoder, no new budget, no base64 in the text half.

**2. `paths` is images only.** A path in `paths` that is not an image is answered
with a line naming it and telling the model to use `file_path` for text. Reading
text through `paths` would touch the read-baseline ledger — written by
`st.RememberReadForSession` (`pkg/tool/state.go:2857`) from the text read at
`pkg/tool/file_tools.go:198` and consumed by `write_file`/`edit_file` through
`readStateForEdit` (`:945`, `:1170`) — and that is not this plan's business.

**3. At most five images per call**, the same cap `read_file` already applies to
PDF pages (`pdf.DefaultMaxPages = 5`). Over the cap: refuse with one line naming
the cap and saying to call again with the rest — do not silently truncate.

**4. Order is input order; duplicate paths collapse.** Text lines first (one per
image, in order), then the parts in the same order — the shape the PDF branch
already uses.

**5. A refused session is refused wholly.** If `st.ImageInputRefusal(ctx)` is
non-empty, the whole call fails with that reason before any file is read — the
same rule the single-path branch applies at `:105-110`.

**6. Degrade one bad image, never the batch.** A path that cannot be read, or
whose bytes cannot be encoded, becomes a text line naming the path and the reason;
the other images still arrive. This is `extractResultImageParts`' documented rule,
and the list read must match it. An approval request is the one exception
(decision 9).

**7. MCP embedded-resource images are the same object as `type: "image"` blocks.**
For the images this pass takes over — the four sendable MIME types — there is one
extraction pass, one budget, one degrade path and one refusal gate; do not add a
second converter for them. Every other resource keeps the existing
`persistBinaryResourceContent` path untouched (Step 4 states the boundary
exactly).

**8. `paths` changes the tool schema once.** Adding an optional field changes the
JSON schema derived from `FileReadInput`, which sits in the cached prompt prefix
(`pkg/skill/catalog.go:43-53` explains why that shape is expensive). Ship it in
one commit with the tests; do not add a second schema change to this plan.

**9. An approval is not a degrade.** `authorizeRead` answers a path inside
`FOREBRAIN_HOME` (outside the workspace) with `*RequiresActionError`
(`file_tools.go:324`), and the orchestration layer turns that error into the
approval the user answers (`isRequiresActionError`,
`pkg/run/orchestration_llm.go:1424`). If any path in the list raises it, the whole
call returns that error unchanged — no parts, no text line. Degrading it would
silently delete the approval and tell the model the path was unreadable.

**10. The surface capture is the last capture.** `CaptureToolCompletion`
(`pkg/tool/state.go:844`) replaces the payload, and `authorizeRead` captures an
error whenever it refuses a path. Capture the list's `ImageReadOutput` **after**
the loop, so a denied path later in the list cannot leave the step marked failed
while the call returns pixels.

## Git workflow

- Branch: `advisor/363-image-reading-beyond-read-file`
- Conventional Commits, lowercase imperative, optional `(scope)`, matching recent
  history (`feat(tool): let the model read images and PDF pages`). Use e.g.
  `feat(tool): read a list of images in one call, and MCP resource images as pixels`.
- Do NOT push and do NOT open a PR unless the operator instructed it.
- Dispatched into an isolated workspace, your branch lives in
  `.worktrees/<short-kebab-name>` under the project's main worktree; never push
  or merge into the user's branch.

## Steps

### Step 1: Prove the two gaps on the live tree

No edits yet. Confirm, in your own shell, that the plan is describing reality:

```bash
grep -nE '^\tPaths +\[\]string' pkg/tool/file_tools.go     # expect: no output (no such field)
grep -n "FilePath string" pkg/tool/file_tools.go              # expect: the read and write input fields
grep -n 'EqualFold(strings.TrimSpace(typ), "image")' pkg/mcp/result_processing.go
grep -n 'm\["resource"\]' pkg/mcp/result_processing.go
grep -rn "NewGrepTool\|NewGlobTool" pkg/tool/                # expect: no output (no such tool)
```

**Verify**: the first command prints **nothing** (there is no `Paths` struct field
today — a bare `grep "Paths"` also matches `protectedPaths` locals, so the pattern is
anchored); the second prints the two `file_path` fields; the first MCP grep finds the
`image`-only test inside `extractResultImageParts` (`:133`) and the second finds
the `resource` handling inside `persistBinaryResourceContent` (`:348`) — two
different functions, which is the gap; the last command prints nothing, confirming
there is no `grep`/`glob` tool to extend. If any expectation fails, STOP — the tree
has moved and the plan's premise is stale.

### Step 2: `read_file` takes a list of image paths

In `pkg/tool/file_tools.go`:

2a. Add to `FileReadInput` (after `Pages`):

```go
	// Paths reads several images in one call: the shape a search hit list needs.
	// Images only — a text path here is refused with a line pointing at
	// file_path, because a text read owns the read-baseline ledger and this
	// branch must not touch it. Order is the model's order; duplicates collapse.
	Paths []string `json:"paths,omitempty" jsonschema_description:"Several images in one call (images only, at most 5). For text use file_path."`
```

2b. At the top of the handler, after the `st == nil` / `in == nil` guards and
**before** `authorizeRead` for `in.FilePath`:

```go
			if len(in.Paths) > 0 {
				return readImageList(ctx, st, in.Paths)
			}
```

2c. Add `readImageList` in the same file, near `imageReadPart`. Its contract:

- empty result (nothing readable) is an error, not an empty part list;
- `st.ImageInputRefusal(ctx)` non-empty → `return nil, errors.New(refusal)`,
  before any file is touched;
- more than `maxReadImagePaths = 5` distinct paths → refuse with a line naming the
  cap and "call again with the rest";
- for each distinct path, in input order, resolve it through the **same** gate the
  single path uses — a list must not be a way around the workspace boundary. The
  real signature takes a tool name and returns a resolution:

```go
			resolution, err := authorizeRead(ctx, st, "read_file", p)
			if err != nil {
				var req *RequiresActionError
				if errors.As(err, &req) {
					return nil, err // an approval, not a degrade (decision 9)
				}
				lines = append(lines, fmt.Sprintf("%s: %v", p, err))
				continue
			}
			abs := resolution.Abs
```

  then `os.ReadFile(abs)`, then `isImageFileExtension(strings.ToLower(filepath.Ext(abs)))`
  / `llm.ImageMIMEType(abs, raw)` to decide. Not an image → one text line
  `"<path>: not an image; read it with file_path"`. Image →
  `imageReadPart(abs, raw, 0)`; on error, one text line naming the path and the
  error; on success, `imageReadNote(abs, res)` as that image's line and the part
  appended in order;
- **after** the loop, `CaptureToolOutput(ctx, ImageReadOutput(absOfFirstImage,
  firstInputPath, resOfFirstImage))` once, so a surface can draw one thumbnail —
  the same "first image" rule `extractResultImageParts` uses (`surface` is set
  once), and the last capture wins (decision 10);
- return `[]llm.ContentPart` = the text lines joined by `"\n"` as one `llm.Text`,
  then the parts in order. (One text part, N image parts — the shape
  `extractResultImageParts` produces.)
- the existing single-path, text and PDF branches are untouched, and nothing here
  calls `RememberReadForSession` or the read observers.

**Verify**: `gofmt -l pkg/tool` → no output; `go build ./...` → exit 0;
`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/... -count=1` → PASS with **zero
changes to existing tests** (the new field is optional and unused so far). If an
existing test breaks, your branch is reached by the old path; revert and report.

### Step 3: `read_file` tests for the list

Add to `pkg/tool/file_tools_test.go`, modelled on
`TestFileReadReturnsImagePartsForImageFile` (`:2361`) — same `writeTestPNG` /
`writeNoiseTestPNG` helpers at `:2280-2330`, same `NewFileReadTool` + `Handle`
shape:

1. `TestFileReadTakesAListOfImagePaths` — two PNGs in the JSON `{"paths":[…,…]}` →
   one text part naming both, two image parts in input order, both decodable to the
   files' bytes.
2. `TestFileReadImageListDeduplicates` — the same path twice → one image part, one
   note line.
3. `TestFileReadImageListRefusesTextPaths` — one PNG and one `.go` file → the image
   part arrives, the text path is a line saying to use `file_path`, and the `.go`
   file is **not** added to the read ledger (`read_file`-as-text still reports it
   unread — reuse the ledger assertion style of
   `TestFileReadImageDoesNotEnterReadLedger` at `:2532`).
4. `TestFileReadImageListIsBounded` — six distinct PNGs → an error naming the cap
   of 5, no parts.
5. `TestFileReadImageListHonoursTheImageGate` — install the refusal gate with
   `st.SetImageInputGate(func(context.Context) string { … })` the way
   `TestFileReadRefusesImageWhenGateRefuses` (`:2646`, gate at `:2654`) does → the
   whole call fails with that reason and no file is read.
6. `TestFileReadImageListDegradesOneBadImage` — a good PNG plus a `.png` whose
   bytes are not an image → the good image's part arrives and the bad one is a
   line naming the reason (the batch does not fail).
7. `TestFileReadImageListStillAsksInsideHome` — a `paths` list holding a good PNG
   and a file inside `FOREBRAIN_HOME` but outside the workspace, with the action
   hook armed to return `("act-1", true, nil)` → the call returns a
   `*RequiresActionError` (`errors.As`), **no parts and no text line**. Structural
   pattern: `TestFileReadStillAsksInsideHomeOutsideWorkspace`
   (`pkg/tool/file_tools_test.go:2722`) with its `readMCPResultFixture` helper
   (`:2676`). This is decision 9's regression test: a degrade here would delete
   the approval.

**Verify**:
`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -count=1 -run 'TestFileRead' -v | tee /tmp/read.log`
then `grep -c '^--- PASS' /tmp/read.log` → at least the seven new names plus the
existing `TestFileRead…` set all PASS, and `grep -c '^--- FAIL' /tmp/read.log` → 0.
(A bare `-run` exit 0 also happens with **no** tests selected — count the PASS
lines, do not trust the exit code alone.)

### Step 4: MCP embedded-resource images become parts

In `pkg/mcp/result_processing.go`, inside `extractResultImageParts`' loop over
`content[]`, accept the embedded-resource shape as a second image source. Today
the loop skips everything whose `type` is not `image` (`:133`); the resource
shape is handled only later, by `persistBinaryResourceContent` (`:330`), which
persists and names it and never builds a part.

```go
		// A server may deliver an image either as its own image block or as an
		// embedded resource whose blob carries an image MIME. Both are the same
		// object to the model, so both go through the one budget, the one
		// degrade path and the one refusal gate below.
```

The takeover rule, exactly:

- **Read the bytes from the nested resource object, not from the block.** The
  image branch reads `m["data"]` (`:136`) and `m["mimeType"]` (`:150`); an embedded
  resource carries neither. Pull them the way `persistBinaryResourceContent` does
  (`:348-364`):

```go
		resource, _ := m["resource"].(map[string]any)
		blob, _ := resource["blob"].(string)
		mimeType, _ := resource["mimeType"].(string)
```

  Relaxing the `type` guard at `:133` alone is a **silent no-op**: `m["data"]` is
  empty for a resource, the loop `continue`s at `:137-139`, and the block reaches
  `persistBinaryResourceContent` with no part — the gap stays open while every
  existing test still passes.
- **Only the four sendable MIME types are taken over** — `image/png`,
  `image/jpeg`, `image/gif`, `image/webp`, declared in `resource["mimeType"]` or,
  when that is empty, sniffed with `llm.ImageMIMEType("", decoded)` after decoding
  (the image branch does the same at `:165-167`). Every other resource — a PDF, a
  text blob, `image/tiff`, an unlabelled blob that sniffs as nothing — is left
  **untouched** for `persistBinaryResourceContent`, so its behavior, its saved-path
  line and its file name are byte-for-byte today's.
- **Guard the size before decoding**, exactly as both existing passes do:
  `decodedSizeWithin(blob)` (`:41`, cap `maxDecodedBlockBytes =
  llm.MaxImageDecodeBytes`, `:34`) and, over the cap, replace the block with
  `oversizeBlockLine(blob)` (`:48`) and continue. A blob that fails
  `base64.StdEncoding.DecodeString` gets the same "could not be base64-decoded"
  line the image branch leaves (`:153-163`). Neither failure may cost the call its
  envelope or the other blocks' pixels.
- Persist with `persistMCPResult(resultsDir, serverName, "image",
  extensionForMIME(mimeType), decoded)` — the image branch's own call (`:168`).
  **Note the visible consequence**: a sendable image resource's saved file is now
  named `mcp-<server>-image-<n>.<ext>` instead of `mcp-<server>-blob-<n>.<ext>`,
  because the extraction pass owns it. The line's format is unchanged.
- The replacement text line is the **same** `"Binary content saved to %s (%s, %d
  bytes)."` the image branch leaves (`:178-181`), so the envelope stays free of
  base64 (`TestToolResultContentKeepsBase64OutOfText`, `:182`).
- Refused session (`wantParts == false`): the saved-path line is the whole story,
  and the surface still gets the file facts via `tool.ImageFileOutput` — the image
  branch's own rule (`:182-191`). No part, no error.
- Sendable and wanted: `llm.ImagePartWithin(path, mimeType, decoded, 0)`; on error
  degrade to the image branch's "…but was not sent as an image: %v." line
  (`:198-210`); on success append the part and set `surface` once via
  `tool.ImageReadOutput`.
- Order matters and is already right: `extractResultImageParts` runs before
  `processToolResultJSON` (`:78`), so a resource block converted to `type: "text"`
  is skipped by `persistBinaryResourceContent` — no double persistence. State that
  invariant in a comment.
- `type: "image"` blocks keep their existing behavior exactly.

**Verify**: `go build ./pkg/mcp/...` → exit 0;
`CGO_ENABLED=1 go test -tags fts5 ./pkg/mcp/... -count=1` → PASS, and in
particular `TestProcessToolResultJSONPersistsBinaryResourceContent` (`:91`, a
**PDF** blob) still passes unchanged — non-sendable resources keep the old path.
Know what that test does and does not prove: it calls `ProcessToolResultJSON`, a
**test-only wrapper** over `processToolResultJSON` (`result_processing_test.go:569-571`)
that never runs `extractResultImageParts`, so it cannot catch a double-persistence
regression. The pass that goes through the real entry point is
`TestToolResultContentReturnsImageParts` (`:128`) and the three siblings at
`:182`/`:208`/`:232`; your four new tests must use `ToolResultContent` the same way.

### Step 5: MCP tests for the new block shape

Add to `pkg/mcp/result_processing_test.go`, modelled on `:128` and `:232`:

1. `TestToolResultContentReturnsPartsForEmbeddedImageResource` — a
   `{"type":"resource","resource":{"mimeType":"image/png","blob":"…"}}` block →
   one text part with the saved-path line, one `ContentTypeImageBase64` part whose
   bytes equal the original.
2. `TestToolResultContentSniffsAnUnlabelledImageResource` — a resource blob with
   no `mimeType` → sniffed and sent as an image.
3. `TestToolResultContentRefusalGivesTextForImageResource` — the refusal string
   set → text only, no parts, no error (the twin of `:208`).
4. `TestToolResultContentDegradesUnsendableImageResource` — an `image/tiff` blob →
   the **resource path's own** saved-path line `"Binary content saved to %s (%s, %d
   bytes)."` (no "but was not sent" suffix — that suffix belongs to the image-block
   degrade at `:198-210`, which this block never reaches), no part, no error. The
   structural twin is `TestToolResultContentDegradesUnsendableImageBlock` (`:232`),
   but note that one exercises an **oversized webp** through `ImagePartWithin`'s
   error, not an unsupported MIME: assert the line this plan's rule actually
   produces, and assert the saved file is still named `mcp-<server>-blob-…`.

**Verify**:
`CGO_ENABLED=1 go test -tags fts5 ./pkg/mcp/ -count=1 -run 'TestToolResultContent|TestProcessToolResultJSON' -v | tee /tmp/mcp.log`
then `grep -c '^--- PASS' /tmp/mcp.log` → every existing test above plus the four
new ones PASS; `grep -c '^--- FAIL' /tmp/mcp.log` → 0.

### Step 6: Full gates

1. `gofmt -l pkg/tool pkg/mcp` → no output
2. `go vet ./pkg/tool/... ./pkg/mcp/...` → exit 0
3. `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/... ./pkg/mcp/... ./pkg/llm/... -count=1` → ok
4. `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/... -count=1` → PASS
   (`pkg/tool` must stay at its current count — this plan adds no file)
5. `make test` → exit 0
6. Live, in a real session: `ls` a directory holding two PNGs through `shell`,
   then `read_file` with `{"paths":[…]}` and confirm the model describes both
   pictures; then call an MCP tool that returns an embedded image resource (or
   reply with a stub server) and confirm the model describes the picture without a
   second `read_file`. Paste both transcripts into the report. On a model without
   image input, paste the refusal instead — that is a pass.

## Test plan

New tests, all listed in Steps 3 and 5: **seven** in `pkg/tool/file_tools_test.go`,
**four** in `pkg/mcp/result_processing_test.go`. Structural patterns:
`TestFileReadReturnsImagePartsForImageFile` (`:2361`),
`TestFileReadStillAsksInsideHomeOutsideWorkspace` (`:2722`) and
`TestToolResultContentReturnsImageParts` (`:128`).

Verification: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/... ./pkg/mcp/... -count=1`
→ all pass, including **11** new tests.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `FileReadInput` carries an optional `paths` field and the handler branches on
      it before touching `in.FilePath` — `grep -n "readImageList" pkg/tool/file_tools.go`
      finds the definition and the call
- [ ] More than five paths is refused with the cap named; duplicates collapse
- [ ] A non-image path in `paths` leaves the read ledger unchanged (test 3)
- [ ] The refusal gate refuses the whole call (test 5)
- [ ] A `paths` call that names a file inside `FOREBRAIN_HOME` outside the
      workspace returns `*RequiresActionError`, not a degraded line (test 7)
- [ ] `grep -n "resource" pkg/mcp/result_processing.go` shows the image-resource
      acceptance inside `extractResultImageParts` (not only in
      `persistBinaryResourceContent`)
- [ ] `TestProcessToolResultJSONPersistsBinaryResourceContent` still passes
      unmodified (non-image resources keep the old path)
- [ ] All **11** new tests present and passing; `make test` green; `gofmt -l` clean;
      `go vet` exit 0
- [ ] `ls pkg/tool/*.go | grep -v _test | wc -l` and `ls pkg/mcp/*.go | grep -v _test | wc -l`
      are unchanged from before this plan
- [ ] `git status --porcelain` lists only the four in-scope paths
- [ ] The Step 6.6 live transcripts are in the report
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- The code at the locations in "Current state" does not match the excerpts — in
  particular if `read_file` already accepts several paths, or
  `extractResultImageParts` already handles `type: "resource"`. Then this plan is
  describing shipped behavior (the mistake `plans/359` made) and must be rejected,
  not implemented.
- A `paths` branch cannot reuse `authorizeRead` per path without reaching into the
  single-path handler's internals. Report the shape you found; a list read that
  skips the workspace boundary is a security regression, not a shortcut.
- Adding the field changes a **golden** schema or description test you cannot
  update inside the in-scope files. Report which test.
- The MCP change would require touching `processToolResultJSON`,
  `persistBinaryResourceContent`'s non-image branch, or `resultJSONContainsImage`'s
  contract. The extraction pass runs first and must stay the only place parts are
  built; if the ordering invariant does not hold, report it.
- You need a file in `pkg/tool` or `pkg/mcp`. Neither package has room for a new
  production file in this plan's budget; report instead of adding one.
- A test needs a real MCP server. Every case here runs against a JSON string and a
  `t.TempDir()`, exactly like the existing tests; do not introduce a server.
- The live run shows a model that takes images but receives none of them. The
  adapter matrix above is verified, so this would be a *new* fact: report the
  provider, the model and the raw request/response. Do not add an adapter
  workaround here — `plans/364` (Gemini drops the pixels) owns its projection; the
  compat shape needs no fix (plan 365 was rejected).

## Maintenance notes

- **`paths` is the one schema change.** If the tool description or `FileReadInput`
  changes again, that is a second cached-prefix change — batch it with whatever
  else needs it instead of dribbling one field at a time.
- **The count cap is a cost knob, not a correctness one.** Five images at the
  3 MiB default budget is the same volume a PDF read can already produce; if the
  owner wants a different number, change `maxReadImagePaths` and the doc line in
  one edit.
- **`pkg/skill/system_assets/run/SKILL.md` and
  `run-skill-generator/references/drivers.md` should learn the list form** —
  "`rg -l '\.png$'` then one `read_file` with `paths`" — once this lands. They
  currently say only that `read_file` returns a saved image's pixels. A small
  follow-up plan, not an edit here (those assets pin byte-level counts).
- **MCP surface metadata shows one thumbnail.** `extractResultImageParts` captures
  `surface` for the first image only; a multi-image result draws one card
  thumbnail. That is a display question, deferred deliberately.
- **`resultJSONContainsImage` is the offload guard, and it only knows `type:
  "image"`.** `containsImageBlock` (`pkg/mcp/result_processing.go:298-325`) walks
  the JSON for an `image` block; it does **not** recognize a `resource` blob.
  Resource blobs are safe anyway because `persistBinaryResourceContent` strips
  every one of them before the size check, and this plan's takeover strips the
  sendable image ones even earlier. If a future block shape can reach the size
  check with base64 still in it, add it to `containsImageBlock` in that same
  commit — otherwise a large result is offloaded to disk with the base64 inside.
- **Reviewer scrutiny**: read Step 2c's `authorizeRead` call per path (four
  arguments, `resolution.Abs`, and the `*RequiresActionError` branch that must
  return the error unchanged), the position of the single `CaptureToolOutput`
  (after the loop), and Step 4's ordering invariant. Those are the places a
  shortcut would pass the happy-path tests and still be wrong: a list that bypasses
  the boundary, a list that turns an approval into a text line, a step that renders
  failed while it returned pixels, or a second pass that rebuilds parts after the
  text envelope was already rewritten.
