# Plan 359: REJECTED — `read_file` already returns an image's pixels (it has since `19239c1d`)

- **Category**: feature / multimodal
- **Priority**: — (rejected)
- **Status**: **REJECTED on 2026-11-23 by `review-plan plans/353-362`.** The change this plan
  describes is already in the tree; there is nothing to implement.
- **Planned at**: commit `6541b8ab`, 2026-11-23 — rejected at the same commit.

> **Do not execute this plan.** It would re-implement a working feature, delete tests that
> already pin it, and its own done criterion can never pass (`grep -c
> "ContentTypeImageBase64" pkg/tool/file_tools.go` is 0 by design: the part is built by
> `llm.ImagePartWithin`, not by naming the content type).

## Why it is rejected — evidence

`read_file` has returned image parts since commit **`19239c1d` "feat(tool): let the model
read images and PDF pages" (2026-10-15)**, which is an ancestor of this plan's baseline
`6541b8ab`. Verified on `6541b8ab`:

- `pkg/tool/file_tools.go:391-437` — `imageReadPart(abs, raw, budget)`: sniffs with
  `llm.ImageMIMEType`, sends png/jpeg/gif/webp through `llm.ImagePartWithin` (size budget,
  downscale, `$FOREBRAIN_IMAGE_MAX_BYTES`), and returns an actionable error for an image
  extension whose bytes are another image type or are not an image at all.
- `pkg/tool/file_tools.go:634-650` — `appendPDFPagePart` does the same for rendered PDF
  pages.
- `pkg/tool/file_tools_test.go` already pins every case this plan proposed to add:
  `TestFileReadReturnsImagePartsForImageFile` (:2361), `TestFileReadStatesTheRealSizeOfAWebp`
  (:2419), `TestFileReadImageBudgetEnvVar` (:2467), `TestFileReadImageDoesNotEnterReadLedger`
  (:2532), `TestFileReadImageCapturesSurfaceOutputWithoutPixels` (:2556),
  `TestFileReadUndecodableImageExtensionIsActionable` (:2593),
  `TestFileReadMisnamedImageBytesIsActionable` (:2619), `TestFileReadRefusesImageWhenGateRefuses`
  (:2646), `TestFileReadRendersPDFPages` (:2775) and its budget/range/limit siblings.
- `grep -rn ContentTypeImage pkg/tool/` returns **5** hits, all in `file_tools_test.go`
  (the plan claimed zero).

The plan's "correction to the audit" was itself wrong in the other direction: it noticed
that a tool result *could* carry an image, and missed that a tool already does.

## What this means for the rest of the batch

- **`plans/360-capture-tool.md`** no longer depends on this plan: its premise ("a PNG on
  disk is already reachable") is satisfied by the shipped `read_file`. 360 remains valid —
  **nothing in the product produces a screenshot**, which is a different gap.
- **`plans/353` and `plans/354`** described the model as blind to image files ("an agent
  cannot look at an image it captured through a shell"). That sentence is stale and has
  been corrected in both plans' inlined assets; `read_file` on a png/jpeg/gif/webp is a
  second observation channel beside what a shell can print.
- **`plans/355`** (maintenance notes) and **`plans/362`** (dependency list) referenced this
  plan as the thing that makes tool results carry images. Both now say it is already true.
- The runtime-verification skill (362) still needs **360**: it can read a picture, but it
  cannot take one.

## Owner ruling (2026-11-23): the extension is plan 363

The only reading under which this plan is not redundant — "extend image reading beyond
`read_file`" — is now **`plans/363-image-reading-beyond-read-file.md`**:

- **A search hit list** (`shell` + `rg`/`find`/`ls`; forebrain has no `grep`/`glob` tool)
  becomes pixels in one call: `read_file` gains an optional `paths` list, bounded to five
  images, images only.
- **An MCP image delivered as an embedded resource** (`type: "resource"` with an image
  blob) becomes an image part — today it is only saved and named. `type: "image"` blocks
  already are parts, so 363 touches only the resource shape.

Do not re-open this file; 363 carries the work and its own verification gates.
