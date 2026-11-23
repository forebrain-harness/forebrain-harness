# Plan 002: Redesign the docs information architecture and rewrite the English content value-first

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If any STOP condition triggers, stop and report — do not improvise. Do NOT
> run Step 8 (index update) — that belongs to the reviewer.
>
> **Drift check (run first)** — the target is the nested site repo
> `forebrain-harness.github.io/` at `/Users/doudou/workspace/unionj-cloud/forebrain-harness/`.
> Plan 001 (graphite theme) has already landed as **uncommitted working-tree
> changes** (`docs/.vitepress/theme/style.css`, `docs/.vitepress/config.mjs`,
> `docs/.vitepress/shiki/monokai-light.mjs`). Confirm they are still present
> (`test -f docs/.vitepress/shiki/monokai-light.mjs && grep -c "1c1c1f" docs/.vitepress/theme/style.css`
> → prints a number ≥ 1). If missing, STOP.

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: LOW-MED (content + config only; build and link checks gate)
- **Depends on**: 001 (DONE)
- **Category**: docs (IA + content)
- **Planned at**: site repo commit `342cad5` + 001 working-tree changes, 2026-10-08
- **zh translations**: NOT in this plan — Plan 003 mirrors every page 1:1.

## Why this matters

The owner ruled (2026-10-08): the docs menus and content must be redesigned
against the **current source implementation**, must be friendly to **new
users**, and must lead with **what the user gets** rather than underlying
principles. Today the site fails all three:

- The landing page argues product philosophy ("This is the product bet")
  instead of showing what a user gets.
- The guide sections lead with internals (`memory-internals`, `skills-internals`,
  `context-and-compaction`) while real capabilities have no page at all:
  code intelligence (38 language servers, `forebrain lsp …`) is **never
  mentioned** on the site; there is no user-facing channels page and no
  gateway quickstart.
- Content has drifted from source: the landing claims channel adapters for
  **Signal, email, SMS — none exist** (`pkg/channel/` has no such files) and
  misses Home Assistant and BlueBubbles (iMessage), which do exist; the CLI
  reference omits the entire `lsp` command group; the term "Active Memory" is
  not used anywhere in `pkg/` source (the README's framing — per-project and
  global memory, distilled from past sessions, multilingual retrieval — is
  current).
- The zh sidebar contains a dead link (快速开始 → `/zh/guide/getting-started`,
  a page that does not exist), masked by `ignoreDeadLinks: true`.

The main repo `README.md` is already the current, value-first product story
(362 models from 33 vendors, one engine across terminal/browser/chat, OS
sandbox, cheap long sessions, multilingual memory). The docs site should tell
that story and prove it page by page.

## Current state

Site repo layout relevant here (all paths relative to
`forebrain-harness.github.io/`):

- `docs/.vitepress/config.mjs` — nav + sidebars are **path-keyed**:
  `/guide/` (12 items), `/config/` (7), `/develop/` (1); zh locale has its own
  sidebar (4 items, one dead). `locales.zh.themeConfig.nav` links
  指南 → `/zh/guide/memory-internals`.
- `docs/index.md` — hero + 7 features + long philosophical body
  ("What Forebrain Harness Is", "Why Forebrain Harness Is Different", "The
  product bet", "Who This Documentation Is For" ×5 personas, "Product Areas",
  mermaid system overview).
- `docs/guide/*.md` (12): getting-started, cli-and-surfaces, architecture,
  context-and-compaction, memory-systems, memory-internals, skills-and-tools,
  skills-internals (verify: this file exists per zh sidebar links
  `/zh/guide/skills-internals` — check `ls docs/guide/`; if the en file is
  named differently, adapt), subagents, agent-collaboration-modes,
  safety-model, cli-command-reference, telemetry.
- `docs/config/*.md` (7), `docs/develop/build-and-release.md`.

Source-of-truth anchors in the main repo (verify during writing, cite nothing
from memory):

- `pkg/channel/` — exactly these adapters:
  `bluebubbles, dingtalk, discord, feishu, homeassistant, http_bridge, matrix,
  mattermost, qq, slack, telegram, wecom, weixin, whatsapp, ws_bridge`.
- `cmd/forebrain/` + `root.go` registration set — commands: `forebrain`
  (TUI), `resume <session-id>`, `gateway start|status|stop`,
  `lsp list|doctor|enable <id>|disable <id>|install <id>|recommendations|reset`,
  plus whatever `serve.go` registers (read `root.go`'s `AddCommand` calls —
  enumerate from source, not from this plan).
- `pkg/lsp/catalog/*.yaml` — 38 language servers (gopls, pyright, rust-analyzer,
  typescript-language-server, jdtls, clangd, …).
- `README.md` — the current value story and quick start (ChatGPT sign-in or
  API keys; trust flow; 362 models / 33 vendors; `/model`, `/permissions`,
  `/plan`, `/goal`; sandbox on macOS/Linux/Windows; multilingual memory).
- Memory terms: use the README's terms (per-project and global memory,
  distilled from past sessions, multilingual retrieval EN/中文/日本語/한국어).
  Do **not** use "Active Memory" (absent from source).

Protected files — **never touch** (owner's uncommitted work in the site repo):
`docs/public/mark.svg`, `docs/config/runtime-gateway-and-channels.md`.
Also out of scope: `docs/.vitepress/theme/*` (frozen by 001), every existing
`docs/zh/**` page (Plan 003 replaces the zh tree wholesale — leave the three
existing zh pages untouched this plan), `.github/`, `README.md`, `package*.json`.

## Commands you will need

Run from the site repo root.

| Purpose | Command | Expected |
|---|---|---|
| Build gate | `npm run docs:build` | exit 0 |
| Enumerate CLI | `go run ./cmd/forebrain --help` (from the **main repo** root) | the real command list |
| Sidebar link check | script in Step 8 | every sidebar link resolves to a file |
| Stale-claim sweep | `grep -rn "Signal\|SMS\|email, SMS" docs/guide docs/index.md` | only pages you rewrote, zero false channel claims |

## Scope

**In scope**:
- `docs/.vitepress/config.mjs` — nav + sidebar restructure (en side), locale
  descriptions update. Do not touch the zh sidebar this plan (Plan 003).
- `docs/index.md` — full rewrite.
- `docs/guide/` — rewrite: `getting-started.md`, `cli-and-surfaces.md`,
  `memory-systems.md`, `skills-and-tools.md`, `subagents.md`, `safety-model.md`,
  `cli-command-reference.md`; create: `what-you-can-do.md`, `gateway.md`,
  `chat-channels.md`, `code-intelligence.md`; leave untouched (Internals
  group, demoted in menus only): `architecture.md`, `context-and-compaction.md`,
  `memory-internals.md`, `skills-internals.md`, `agent-collaboration-modes.md`,
  `telemetry.md`.
- `docs/config/*.md` and `docs/develop/build-and-release.md` — light
  accuracy pass ONLY: fix statements that contradict current source
  (CLI surface, channel list, LSP absence). No restructuring, no tone rewrite.

**Out of scope**: everything in Protected files above; any zh page; any
rename/move of existing files (inbound links and search keep working);
theme/CSS.

## Writing contract (applies to every rewritten/new page)

1. Lead with the outcome: every page opens with what the reader can **do or
   get** after it ("After this page you will have X running", "With /goal the
   agent keeps working toward an objective while you watch").
2. Task-oriented sections, plain verbs, active voice, no philosophy essays.
   Cut multi-persona "who this is for" walls; one short line at most.
3. Every capability claim must be verifiable in the main repo source or
   README. No invented feature names. Numbers come from README/source (362
   models / 33 vendors, 15 channel adapters, 38 language servers).
4. Headings keep the site's Title Case; body ≤ 8th-grade plainness; code
   blocks verbatim-correct (install commands from README's Quick start).
5. No new CSS classes or inline styles; use existing VitePress containers
   (tip/warning/danger) sparingly.
6. Cross-link generously: each new-user page ends with "Where next" (2–3 links).
7. Mermaid system overview may stay on the landing page only if it reads at a
   glance; otherwise drop it — do not redraw it.

## Steps

### Step 1: Restructure `config.mjs` (en side)

Replace the **path-keyed** en sidebars with **one global sidebar array**
(applies to all non-zh doc paths), grouped:

```js
const sidebar = [
  {
    text: "Guide",
    items: [
      { text: "Getting Started", link: "/guide/getting-started" },
      { text: "What You Can Do", link: "/guide/what-you-can-do" },
      { text: "Choose Your Surface", link: "/guide/cli-and-surfaces" },
      { text: "Run It As A Gateway", link: "/guide/gateway" },
      { text: "Your Agent In Team Chat", link: "/guide/chat-channels" },
      { text: "Code Intelligence", link: "/guide/code-intelligence" },
      { text: "Remember Across Sessions", link: "/guide/memory-systems" },
      { text: "Delegate To Subagents", link: "/guide/subagents" },
      { text: "Extend With Skills", link: "/guide/skills-and-tools" },
      { text: "Stay In Control", link: "/guide/safety-model" }
    ]
  },
  {
    text: "Reference",
    items: [
      { text: "CLI Command Reference", link: "/guide/cli-command-reference" },
      { text: "Configuration Reference", link: "/config/overview" },
      { text: "Runtime, Gateway, And Channels", link: "/config/runtime-gateway-and-channels" },
      { text: "Agents and Models", link: "/config/agents-and-models" },
      { text: "Projects And Project-Level MCP", link: "/config/projects-and-mcp" },
      { text: "Sandbox and Permissions", link: "/config/sandbox-and-permissions" },
      { text: "Memory, Compaction, And Runtime Features", link: "/config/memory-and-runtime-features" },
      { text: "Hooks And Skill Extensions", link: "/config/hooks-and-skill-extensions" }
    ]
  },
  {
    text: "Internals",
    items: [
      { text: "Architecture", link: "/guide/architecture" },
      { text: "Context and Compaction", link: "/guide/context-and-compaction" },
      { text: "Memory Internals", link: "/guide/memory-internals" },
      { text: "Skills Internals", link: "/guide/skills-internals" },
      { text: "Agent Collaboration Modes", link: "/guide/agent-collaboration-modes" },
      { text: "Telemetry", link: "/guide/telemetry" }
    ]
  },
  {
    text: "Develop",
    items: [ { text: "Build and Release", link: "/develop/build-and-release" } ]
  }
];
```

Wire it as `themeConfig.sidebar` (global, not path-keyed) for the root locale;
keep the zh locale's `sidebar` **exactly as-is** (Plan 003 owns it). Update
the root locale `description` to one value-first sentence consistent with the
README's opening line. Keep `nav` links as-is except point Guide at
`/guide/getting-started` (already true). Do not touch the zh `nav` this plan.

Note: `skills-internals.md` — verify the en file's exact name on disk
(`ls docs/guide/`) and use the real filename in the sidebar link. If the en
page does not exist (only zh does), drop that sidebar row and note it in your
report (the zh page stays; Plan 003 will handle it).

**Verify**: `npm run docs:build` → exit 0 (dead sidebar links are invisible to
the build, so also run the Step 8 link check after creating pages).

### Step 2: Rewrite `docs/index.md`

Keep the existing frontmatter `hero` block **byte-identical** (it is the
approved design-sheet copy: name/text/tagline/actions/image). Keep the
`features` list's seven titles but fix the Multi-Channel entry's channel list
to the real 15 adapters (README wording is the model: "Telegram, Discord,
Slack, WhatsApp, Feishu/Lark, DingTalk, WeCom, WeChat, QQ, Matrix, Mattermost,
iMessage via BlueBubbles, Home Assistant, and generic HTTP/WebSocket bridges").

Replace the entire body below `features` with, in order:

1. **"Get Running In Five Minutes"** — the README Quick start verbatim
   (npm/go install + `cd your-project && forebrain` + trust + provider setup:
   ChatGPT sign-in or API key), as numbered steps with the real commands.
2. **"What You Get"** — six short value blocks, each 2–3 sentences + one link:
   one engine across terminal/browser/chat; any of 362 models from 33 vendors
   (or ChatGPT plan sign-in), switchable mid-session with `/model`; long
   sessions stay cheap (prompt-cache-first design); a real OS sandbox with
   approvals (`/permissions`, `/plan`); memory that persists per project and
   globally and retrieves across English/Chinese/Japanese/Korean; your agent
   in 15 chat platforms + REST/WebSocket gateway.
3. **"Choose How You Work"** — 3 rows: terminal (TUI), browser (gateway web
   UI), team chat (channels) — each one sentence + link to its guide page.
4. **"Where Next"** — Getting Started / What You Can Do / Configuration
   Reference links. Nothing else.

Delete: "What Forebrain Harness Is", "Why Forebrain Harness Is Different",
"Product Advantages" (×6), "Who This Documentation Is For" (×5 personas),
"Reading Paths", "Product Areas At A Glance", the mermaid block, "Continue
Reading".

**Verify**: `grep -c "Signal\|SMS" docs/index.md` → 0; `grep -c "product bet" docs/index.md` → 0; build exit 0.

### Step 3: New page `docs/guide/what-you-can-do.md` — the capability hub

One screen per capability, each with: what it is (1 sentence), what you get
(2–3 bullets), one real command or path, one "read more" link. Capabilities:
code in the terminal (TUI: `/model`, `/permissions`, `/plan`, `/goal`, resume);
serve as an API (`forebrain gateway start`, REST + WebSocket, web UI); chat
from team platforms (15 adapters); code intelligence (38 language servers,
`forebrain lsp list`); remember across sessions (per-project + global,
multilingual); delegate to subagents; extend with skills; stay in control
(sandbox, approvals, plan mode). Ground every row against README + source
anchors listed above.

**Verify**: build exit 0; `grep -c "forebrain lsp" docs/guide/what-you-can-do.md` → ≥ 1.

### Step 4: New page `docs/guide/gateway.md` — "Run It As A Gateway"

Open with the outcome: "The same agent you use in the terminal becomes a
service: a web UI, a REST API, and WebSocket chat — on your machine or your
server." Cover: `forebrain gateway start|status|stop` (from the CLI
reference's semantics: start is the only starter; setup runs first if
needed); what the web UI gives you; what the HTTP/WS surface exposes
(**enumerate the real route families by reading `pkg/gateway/` server
registration** — sessions, runs/events, files, permissions, skills, memory,
channels; list them by family, not exhaustively); channels pointer to
`/guide/chat-channels`; production note (cluster deployment is supported —
see config reference). End: where next → chat-channels, config runtime page.

**Verify**: build exit 0; the route families listed match at least 5
`grep`-verifiable families in `pkg/gateway/`.

### Step 5: New page `docs/guide/chat-channels.md` — "Your Agent In Team Chat"

Open with the outcome: "Point your team's chat app at your agent — same
engine, same tools, same memory, same approvals." Body: the real adapter
table (15 rows: platform, what it is, adapter config key — read
`pkg/channel/*.go` + `docs/config/runtime-gateway-and-channels.md` **read-only**
for the config keys); a worked example (Telegram or Slack, smallest real
config from the config reference); voice notes note where relevant (silk
decoder exists for WeChat-family voice); approvals still apply in chat.
Honest limits: no Signal/email/SMS adapters exist — do not mention them.

**Verify**: the page's platform list, compared row-by-row against
`ls pkg/channel/*.go | grep -v _test`, has no extra and no missing platform;
`grep -c "Signal" docs/guide/chat-channels.md` → 0.

### Step 6: New page `docs/guide/code-intelligence.md` — "Code Intelligence"

Open with the outcome: "The agent reads your code with a real language server
behind it — definitions, references, call hierarchies, diagnostics — instead
of guessing from grep." Cover: `forebrain lsp list|doctor|enable|install`
(real subcommands); the catalog is 38 servers (name a dozen popular ones:
gopls, pyright, rust-analyzer, typescript-language-server, clangd, jdtls,
ruby-lsp, elixir-ls, vue-language-server, yaml-language-server, …); trusted-
project + per-entry consent gate (read `docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md`
intro or `pkg/lsp/doc.go` for the one-paragraph model — never start on first
frame, lazy background start); what tools the agent gains (the `lsp` tool's
public behavior: definitions, references, hover, diagnostics…). End: where
next → safety model, sandbox config.

**Verify**: `ls pkg/lsp/catalog/*.yaml | wc -l` → 38 and the page says
thirty-eight; every named server exists in `pkg/lsp/catalog/`.

### Step 7: Rewrite the six existing guide pages + CLI reference sync

Per page, keep the file path, apply the writing contract, and sync facts:

- `getting-started.md` — install (README verbatim) → first session (trust →
  provider) → a 10-minute value tour (try `/model`, give one coding task,
  `/plan`, ask it to remember something) → where next. Cut runtime-internals
  prose (gated startup details belong to Internals/TUI docs, one link suffices).
- `cli-and-surfaces.md` → retitle "Choose Your Surface": terminal vs browser
  vs chat vs scripting (`forebrain resume` in pipelines), one decision table,
  when-to-use-each. Cut the shared-runtime internals essay.
- `memory-systems.md` → retitle "Remember Across Sessions". Value-first:
  what the agent remembers, where it lives (per-project vs global), that it
  is distilled from past sessions and retrieved across EN/中文/日本語/한국어,
  how to steer it in a session. Replace "Active Memory" wording with the
  README's terms. Mechanism details → one link to Memory Internals.
- `skills-and-tools.md` → retitle "Extend With Skills": what a skill is from
  the user's seat ("a reusable instruction pack your agent loads when
  relevant"), where skills come from (project `.forebrain/skills/`, global),
  how to check what loaded, tools as the built-in action set. Internals → link.
- `subagents.md` → retitle "Delegate To Subagents": what delegation buys
  (bounded parallel child work, isolated context, reviewable child runs), how
  to trigger it, where to watch child runs. Coordination-mode theory → link
  to Internals.
- `safety-model.md` → retitle "Stay In Control": the controls a user actually
  touches — trust prompt, sandbox presets, `/permissions`, approvals and
  approval memory, plan mode, path protections, audit surfaces. Layer diagram
  only if it fits in one glance.
- `cli-command-reference.md` — sync with `go run ./cmd/forebrain --help`
  output from the main repo: add the `lsp` group with all its subcommands and
  one-line purposes; add `serve` **if and as** root.go registers it; keep the
  "Deliberately Unsupported Commands" section updated to stay truthful.

**Verify**: build exit 0; `grep -rn "Active Memory" docs/guide docs/index.md`
→ 0 hits; `grep -c "lsp" docs/guide/cli-command-reference.md` → ≥ 4.

### Step 8: Link check + stale sweep

```bash
# every global-sidebar link resolves to a file
grep -o 'link: "/[^"]*"' docs/.vitepress/config.mjs | sed 's/link: "//;s/"//' | sort -u | while read -r p; do
  [ -f "docs${p}.md" ] || echo "MISSING: $p";
done
# expected: no MISSING lines
grep -rn "Signal\b" docs/guide docs/index.md | grep -v chat-channels || true
grep -rn "Signal" docs/guide/chat-channels.md || true   # expected: nothing
npm run docs:build                                       # exit 0
```

### Step 9 (reviewer-only): update `plans/WEBSITE_GRAPHITE_REDESIGN/README.md`

## Test plan

Build + link check + stale-claim sweeps above are the machine gates. Content
gates: every new/rewritten page satisfies the writing contract (outcome-first
opening; claims anchored to source; numbers 362/33/15/38 appear only where
verified); channel table == `pkg/channel` file list; CLI page == `--help`
output; no "Active Memory" anywhere; Internals group reachable but last.

## Done criteria

- [ ] Global sidebar live with 4 groups; zh sidebar byte-unchanged.
- [ ] 4 new pages exist (`what-you-can-do`, `gateway`, `chat-channels`,
      `code-intelligence`); 7 pages rewritten; 0 files renamed/moved.
- [ ] `npm run docs:build` exit 0; Step 8 link check prints no MISSING.
- [ ] Channel claims anywhere == the 15 real adapters; zero Signal/email/SMS.
- [ ] CLI reference includes the full `lsp` group (and `serve` if registered).
- [ ] "Active Memory" absent from all touched pages.
- [ ] Protected files untouched (`git status --short` shows exactly the
      in-scope paths on top of the 001 working-tree state).
- [ ] No git commit/push; changes left in the working tree.

## STOP conditions

- 001's working-tree files are missing (drift check fails).
- `pkg/gateway/` route families cannot be enumerated confidently (report; do
  not invent endpoints).
- A README claim cannot be verified in source and is needed for a page
  (report the specific claim; do not guess).
- Any verification fails twice after one honest retry.
- The change would require touching a protected file.

## Maintenance notes

- Plan 003 (zh 1:1 mirror) must run after this plan; it owns the zh sidebar,
  the zh nav, and every `docs/zh/**` page, and replaces the three existing
  zh pages.
- After both land: future feature work should update `what-you-can-do.md` in
  the same PR that changes capabilities (site and source move together).
- Reviewers should scrutinize: no invented claims, channel table parity,
  CLI surface parity, tone (outcome-first), and that Internals pages'
  bodies were left untouched (only their menu placement moved).
