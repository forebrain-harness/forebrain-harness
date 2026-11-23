# Plan 360: A `capture` tool — put a picture of the running app into the model's own eyes

> **Executor instructions**: Follow this plan step by step. Run every verification
> command and confirm the expected result before moving to the next step. If
> anything in the "STOP and report" section occurs, stop and report — do not
> improvise. When done, update the status row for this plan in `plans/README.md`.
>
> **Drift check (run first)**:
> `git status --porcelain -- pkg/run pkg/tool pkg/architecture/cache_test.go` and
> `git diff --stat 6541b8ab -- pkg/run pkg/tool pkg/architecture/cache_test.go`.
> A `6541b8ab..HEAD` range diff is **always empty here** (the baseline is HEAD);
> use the two commands above, which see uncommitted work. If an in-scope file
> moved, compare the "Current state" anchors against the live code before
> proceeding; on a mismatch, STOP.

## Status

- **Category**: feature / tool
- **Priority**: P2
- **Effort**: M
- **Risk**: MED (platform-specific binaries, macOS screen-recording permission)
- **Depends on**: **358** (in `browser` mode the server and the headless browser are background processes, and `wait` is how the capture knows the page is up). Do **not** depend on plan 359: it is rejected — `read_file` has returned image parts since `19239c1d` (2026-10-15), see `pkg/tool/file_tools.go:391-437` and `pkg/tool/file_tools_test.go:2361`.
- **Blocks**: the runtime-verification skill's visual evidence (plan 362, optional there)
- **Closes**: the producer half of gap 2 from the 2026-11-22 tool audit
- **Planned at**: commit `6541b8ab`, 2026-11-23

## Why this matters

forebrain has no browser tool and — still — no screenshot tool. Plans 353/354 tell
the model to drive a UI through `shell` and to assert on printed text; that advice
is now half-stale, because `read_file` on a png/jpeg/gif/webp returns the pixels.
The remaining gap is specifically the **producer**: an image on disk is reachable,
but nothing creates one. A driver that shells out to `screencapture` can, and on
macOS that is enough today — which is why this plan's job is to make capturing a
first-class, permission-aware, cross-platform tool rather than a recipe each
project reinvents. A model that wants to know whether the button rendered has
three bad options: read the DOM as text and infer, run a headless browser and
print the DOM, or guess. None of them can answer "is it *visually* right," which
is the question `pkg/skill/system_assets/frontend-design/` exists to serve.

## Current state

Anchors verified at `6541b8ab`:

| Fact | Evidence |
|---|---|
| No capture tool exists | `grep -rn 'screenshot\|Screencapture' pkg/tool/ pkg/run/` → nothing relevant |
| `pkg/tool` is at **33 of a 34** cap | `ls pkg/tool/*.go \| grep -v _test \| wc -l`; cap at `pkg/architecture/cache_test.go:1532`. Plan 358 takes the last slot |
| `pkg/run` has **2 free slots** (32 of 34) | `ls pkg/run/*.go \| grep -v _test \| wc -l` |
| A tool may be registered from `pkg/run` | `pkg/run/skills.go:171` — `RegisterSkillTool`, with the Layer-2 reason spelled out at `:268-269` ("tool sits below skill in Layer 2's order and cannot import the store"); the registration itself uses `st.RegisterToolMeta` at `:215-227` |
| Image parts already flow from a tool result | `pkg/run/orchestration_llm.go:2405-2420` (converter), and `pkg/tool/file_tools.go:391-437` already returns them — plan 359 is rejected, this is shipped |
| The harness already owns a spool directory convention | `pkg/tool/shell_tool.go:592-595` — `<StateRoot>/state/tool-output` |
| The sandbox denies shell writes under `.forebrain` | `pkg/tool/git_worktrees.go:25-27` comment |
| macOS capture binary | `/usr/sbin/screencapture` — present on this machine |
| Linux capture binaries | `import`, `grim`, `gnome-screenshot`, `spectacle` — **none present on this macOS host, so unverified.** Step 1 handles this |

## Scope

**In scope**: one new file `pkg/run/capture.go` (takes one of `pkg/run`'s two
slots), its registration, `pkg/run/capture_test.go`, and the four platform
branches.

**Out of scope — do not touch**

- `pkg/tool/`. It is at cap; this plan must add **zero** files there. If your
  design seems to need one, the design is wrong — register from `pkg/run` the
  way `RegisterSkillTool` does.
- A full browser-automation tool. Driving a browser (click, type, navigate) is a
  much larger plan. This one **takes a picture**; plan 358's `process` tool is
  what runs the browser, and a script is what drives it.
- `pkg/skill/system_assets/`. Plans 353/354 own those assets. Updating them to
  mention `capture` is a follow-up, listed in Maintenance.
- Video / animated capture. One still frame per call.

## Design decisions the executor must not re-litigate

**1. One tool, three modes.** `screen` (the whole display or a window), `region`
(a rectangle), `browser` (launch a headless browser at a URL and screenshot it).
Not three tools — the tool array is in the cached prefix
(`pkg/skill/catalog.go:43-47`), and plan 358 already spent product-wide budget
on one entry.

**2. The bytes never touch the sandbox's writable roots.** `screencapture` runs
as a sandboxed shell command, and the sandbox will not let it write under
`.forebrain` (`git_worktrees.go:25-27`). Write captures to
`<StateRoot>/state/capture/<id>.png`, mirroring the existing
`state/tool-output` spool, and pass that directory in the request's
`AdditionalWritablePaths` — the same mechanism `shell_tool.go` already uses for
`extraBinds`.

**3. Return the image part *and* the path.** Same rule as `read_file`
(`pkg/tool/file_tools.go:391-437`): text (path, size, mode, what was captured)
plus the image part. The path matters because a capture the model wants to look
at again, crop, or hand to the user is a file, and `present_files` can deliver
it.

**4. Fail loudly on permission, never silently blank.** macOS gates screen
capture behind a TCC screen-recording permission. A denied capture produces a
*valid PNG of nothing* or a desktop wallpaper with no window — which looks like
success. Detect the denial (exit status plus stderr keywords) and return an
explicit error naming System Settings → Privacy & Security → Screen Recording.
A silent blank frame is the worst failure this tool can have, because the model
will describe it confidently.

**5. Reuse the existing image budget.** Do not invent a second ceiling:
`llm.ImagePartWithin` (`pkg/llm/image_scale.go:90`) already resolves the budget,
applies `$FOREBRAIN_IMAGE_MAX_BYTES`, and downscales — that is what `read_file`
calls.

## Git workflow

- Branch: `advisor/360-capture-tool`
- Conventional Commits, lowercase imperative, optional `(scope)`, matching recent
  history (`fix(web): judge a subagent name by the scope it is written to`). Use
  e.g. `feat(tool): a capture tool for the running app's own surface`.
- Do NOT push and do NOT open a PR unless the operator instructed it.
- Dispatched into an isolated workspace, your branch lives in
  `.worktrees/<short-kebab-name>` under the project's main worktree; never push
  or merge into the user's branch.

## Steps

### Step 1: Platform survey before writing code

Determine, per platform, the one command that works and its denial signature:

- **macOS**: `screencapture -x -o <path>` (`-x` silent, `-o` no shadow). Window
  capture: `-l <windowID>`. Verify `screencapture -h` output on the target
  macOS version and record it. Then verify the TCC denial signature by running
  it from a process that lacks the permission — if you cannot arrange that,
  record the exit code and stderr from a successful run and mark the denial
  branch UNVERIFIED.
- **macOS, inside the sandbox — probe this before designing anything else
  (review 2026-11-23).** The seatbelt profile is `(deny default)`
  (`pkg/safety/backend_darwin.go:513`) and `seatbeltMinimalSupportPolicy`
  (`:304-343`) grants **no** WindowServer/Quartz/SkyLight mach-lookup — the
  services `screencapture` must talk to. So a workspace-write-sandboxed
  `screencapture` is expected to be denied by the sandbox *before* TCC is even
  consulted. Run it through the same sandboxed path the tool will use (a `shell`
  command under the workspace-write profile, writing into a directory passed via
  `AdditionalWritablePaths`) and record the exit code and stderr. Three outcomes:
  - it captures → the design stands; record the transcript.
  - it is denied by seatbelt → **owner ruling 2026-11-23: use the product's
    existing "approval, then run outside the sandbox" path. Do not widen the
    seatbelt profile.** `screen`/`region` first run sandboxed exactly like a shell
    command; on a sandbox denial they ask once and, when authorized, re-run on the
    host. The mechanism to copy is `shell`'s (`pkg/tool/shell_tool.go:625-660`):
    1. `safety.IsLikelySandboxDenied(decision, res)` (`pkg/safety/process.go:321`)
       classifies the denial;
    2. the same policy gate shell uses — `snap.ApprovalPolicy.Mode !=
       safety.ApprovalNever` (shell's `sandboxRetryApprovalAllowed` is exactly
       this switch) — and **never** for a batch conversation (nobody to ask,
       `shell_tool.go:163-169`);
    3. ask through `st.ActionHook()(ctx, "capture", payload)` with the escalation
       fields the approval machinery already understands generically
       (`pkg/tool/permissions.go:182`, `format.go:594`; `session_tools.go:749`
       uses the same convention for another tool): `command`,
       `sandbox_permissions: "require_escalated"`, `justification`,
       `force_tool_approval: true`, `approval_reason: "sandbox_denied"`,
       `sandbox_denial_reason`. A pending answer becomes
       `&tool.RequiresActionError{ActionKind: "capture", ToolName: "capture", …}`;
    4. on the approved replay (`tool.ApprovedActionIDFromContext(ctx) != ""`),
       re-run with `commandRequest(safety.SandboxPermissionsRequireEscalated)`
       (`shell_tool.go:656` is the exemplar).
    TCC's Screen Recording permission is granted per binary anyway, so the host
    run is not a workaround — it is the only path that can ever capture. Record
    the probe transcript and which path produced the frame.
  - it is denied by TCC → the `denied` branch below, with the recorded signature.
  Until this probe runs, `screen`/`region` are **UNVERIFIED**, and the report must
  say so rather than implying a working capture.
- **Linux**: no single standard. Probe for `grim` (Wayland), `import`
  (ImageMagick, X11), `gnome-screenshot`, `spectacle` in that order and use the
  first present. **This host is macOS, so none of these could be verified.**
  Implement the probe, and if no binary is found return an explicit error naming
  the four and the package that provides each. Do not pick a favorite.
- **Windows**: PowerShell's `CopyFromScreen` via `System.Drawing`. If you cannot
  test it, implement it behind the same probe and mark it UNVERIFIED in the
  report rather than omitting it — `pkg/safety/backend_windows.go` shows the
  repo does support Windows.

Record every command and version in the completion report.

### Step 2: `pkg/run/capture.go`

```go
type CaptureInput struct {
	Mode     string `json:"mode" jsonschema:"enum=screen,enum=region,enum=browser,description=…"`
	Path     string `json:"path,omitempty" jsonschema:"description=Where to write the PNG. Default <StateRoot>/state/capture/<id>.png"`
	Rect     string `json:"rect,omitempty" jsonschema:"description=region: x,y,w,h"`
	URL      string `json:"url,omitempty" jsonschema:"description=browser: the page to load"`
	WaitMs   int    `json:"wait_ms,omitempty" jsonschema:"description=browser: settle time before the shot. Default 1500"`
	Width    int    `json:"width,omitempty" jsonschema:"description=browser: viewport width. Default 1280"`
	Height   int    `json:"height,omitempty"`
}
```

Register it from `pkg/run` alongside `RegisterSkillTool`. **Use the exported
method, exactly as that exemplar does** (`pkg/run/skills.go:215-227`):

```go
st.RegisterToolMeta(event.ToolMeta{
	Name:        "capture",
	Description: captureToolDescription,
	Category:    "session",
	Destructive: true,
})
```

`registerToolMeta` (the five-argument helper) is **unexported in `pkg/tool`**
(`registry.go:84`) and does not exist in `pkg/run`, so a call to it from here does
not compile (review 2026-11-23). `st.RegisterToolMeta` is at
`pkg/tool/state.go:1793`. Set `InputSchema` from `json.Marshal(t.InputSchema())`
the way the exemplar does. `Destructive: true` — it writes a file and, in
`browser` mode, spawns a process.

The `browser` mode must **not** embed a browser. Shell out to whatever headless
binary is present, probed in the same order plans 353/354 already assume
(`chrome`/`chromium`/`edge` with `--headless --screenshot=<path>
--window-size=WxH --hide-scrollbars <url>`). If none is found, return the
explicit "no headless browser; install one or use plan 358's `process` tool with
your own driver" error. Long-running browsers go through plan 358's
`StartCommand`, not a blocking wait.

**Verify**: `ls pkg/run/*.go | grep -v _test | wc -l` → **33** (not 35).
`ls pkg/tool/*.go | grep -v _test | wc -l` → unchanged. `gofmt -l pkg/run` → clean.

### Step 3: Denial detection

One function, per platform, mapping exit code + stderr to a typed outcome:
`captured`, `denied` (TCC/permission), `sandbox-denied` (seatbelt refused the
display service — distinct from TCC; per the owner ruling it raises the
escalation approval above rather than failing the call, and only a **refused**
approval is a failure), `no-binary`, `empty-frame`. The last is worth
having: a
PNG whose dimensions are right but whose bytes are a uniform color is almost
certainly a denied or minimized capture. Check it cheaply (decode, sample a
grid, compare) and **warn rather than fail** — a genuinely blank page is a legal
result.

**Verify**: unit tests with canned stderr strings and a synthetic uniform PNG.

### Step 4: Tests

1. `mode: screen` on the host → a PNG exists, decodes, dimensions > 0, and the
   result has 2 parts (text + `ContentTypeImageBase64`). Mark this test
   `t.Skip` when no capture binary is present, and say why in the skip message.
2. `mode: region` with an explicit rect → dimensions match the rect.
3. `mode: browser` with no browser installed → the explicit error, no panic.
4. Denial signature → `denied` outcome and an error naming the System Settings
   path.
5. Uniform-color frame → `empty-frame` warning present, result still returned.
6. Output path lands under `<StateRoot>/state/capture/` and that directory was
   in `AdditionalWritablePaths`.
7. An oversized capture is downscaled, not refused — assert through
   `llm.ImagePartWithin`/`pkg/llm/image_scale.go`, which is the budget
   `read_file` already uses.
8. `pkg/tool` file count unchanged (the architecture gate does this; assert it
   explicitly in the report too).

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run/... -count=1` → PASS.

### Step 5: Full gates

1. `gofmt -l pkg/run` → no output
2. `go vet ./pkg/run/...` → exit 0
3. `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/... -count=1` → PASS
4. `make test` → PASS
5. Live: capture this repository's own web frontend
   (`frontend/` — start it with plan 358's `process` tool), and confirm in a
   real session that the model correctly describes something it could only have
   seen. Paste the exchange into the report. **This is the acceptance test for
   the whole capture path** — the model reading the image is the half that
   already works; producing it is this plan.

## Test plan

New file `pkg/run/capture_test.go`, the eight cases above. Structural patterns:
`pkg/tool/file_tools_test.go:2361` (`TestFileReadReturnsImagePartsForImageFile`)
for asserting a two-part result and decoding the base64; `pkg/run/skills_test.go`
around `:879-916` for a tool registered from `pkg/run` and executed through its
handle. Verification: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run/... -count=1`.

## Done criteria

- [ ] `ls pkg/run/*.go | grep -v _test | wc -l` → 33; `pkg/tool` unchanged
- [ ] Three modes implemented; all four platform branches present (UNVERIFIED ones marked in the report, not omitted)
- [ ] Denial returns an error naming the OS permission path — `grep -n "Screen Recording" pkg/run/capture.go` finds it
- [ ] A capture's bytes land under `<StateRoot>/state/capture/`, passed via `AdditionalWritablePaths`
- [ ] Result carries text **and** an image part, produced through `llm.ImagePartWithin`
- [ ] All 8 tests present; `make test` green; architecture gate PASS
- [ ] The Step 5.5 live exchange is in the report

## STOP and report instead of improvising

- `AdditionalWritablePaths` cannot be threaded to a capture command without
  changing `shell`'s public input. Report the seam you found.
- macOS TCC denial cannot be distinguished from success by exit code or stderr.
  Decision 4 makes this the critical failure mode; if it is undetectable, report
  rather than shipping a tool that silently returns blank frames.
- You need a file in `pkg/tool`. You do not; register from `pkg/run`.
- A headless browser turns out to require a persistent user profile directory
  outside every writable root. Report the path it wants.

## Maintenance notes

- **After this lands, plans 353/354's assets should name it.** They already say
  `read_file` returns an image's pixels (corrected in this review, because it has
  since `19239c1d`); once `capture` exists, the natural next edit is "capture the
  screen, then read the file" instead of "shell out to a capture binary". Write a
  small follow-up plan — do **not** edit those assets here, because plans 353/354
  may not have landed yet and their byte-level test assertions would break.
- **Two `pkg/run` slots existed; this plan spends one.** The remaining slot is
  the last one before `pkg/run` hits the same wall `pkg/tool` is at. The next
  tool-shaped feature should plan for a new package, and the owner should
  consider whether the cap of 34 (whose doc comment at
  `pkg/architecture/cache_test.go:1461-1462` still says 30) is the right number.
- **Privacy.** `mode: screen` captures whatever is on the display, which may
  include another window's secrets. The result goes to a model provider. Say so
  in the tool description, and prefer `browser` and `region` in every example
  you write. This is the one tool in the product that can exfiltrate something
  the user never pointed at.
- **Reviewer scrutiny**: read the denial-detection branch hardest. Every other
  defect here is visible; a blank frame that reports success is not.
