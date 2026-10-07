# Plan 003: Mirror the entire docs site into Chinese 1:1 and run full-site acceptance

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If any STOP condition triggers, stop and report — do not improvise. Do NOT
> run the final index update — that belongs to the reviewer.
>
> **Drift check (run first)** — Plan 002 must be complete before you start:
> `test -f forebrain-harness.github.io/docs/guide/what-you-can-do.md && test -f forebrain-harness.github.io/docs/guide/code-intelligence.md && echo 002-OK`
> (run from the main repo root). If it does not print `002-OK`, STOP.
>
> **Owner ruling (2026-10-08, verbatim intent)**: 中英文内容必须完全一致，
> 1比1翻译 — every English page exists in Chinese with identical structure;
> the Chinese site is a full mirror, not a subset.

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: LOW (additive pages + locale config; build + parity checks gate)
- **Depends on**: 002
- **Category**: docs (i18n parity)
- **Planned at**: site repo commit `342cad5` + 001/002 working-tree changes, 2026-10-08

## Why this matters

Today the zh site has 3 pages against the en site's 21, a dead nav target
(快速开始 → a page that does not exist), and a zh-only page whose English
counterpart does not exist — the two locales tell different stories. The
owner has ruled the locales must be a 1:1 mirror. After this plan, a Chinese
reader gets the same value-first journey as an English reader: same pages,
same structure, same code, translated prose.

## Current state

- `docs/zh/` contains exactly: `guide/memory-internals.md`,
  `guide/skills-internals.md`, `config/projects-and-mcp.md`.
- `config.mjs` zh locale: nav 指南 → `/zh/guide/memory-internals`; sidebar has
  4 rows incl. dead `/zh/guide/getting-started`.
- The three existing zh pages are competent translations — mine them for
  terminology before writing anything (Step 1).

Site-repo protected files (never touch): `docs/public/mark.svg`,
`docs/config/runtime-gateway-and-channels.md` (owner's uncommitted work —
**translate it? NO: you create `docs/zh/config/runtime-gateway-and-channels.md`
as a NEW file; the protected path is the English one**), `docs/.vitepress/theme/*`.

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| Build gate | `npm run docs:build` | exit 0 |
| File-tree parity | script in Verification | empty diff |
| Per-page structure parity | script in Verification | all rows OK |
| zh link check | same script pattern as 002 Step 8 over zh sidebar | no MISSING |

## Scope

**In scope**:
- `docs/zh/**` — create every zh mirror page of the post-002 en tree;
  rewrite/absorb the 3 existing zh pages; remove zh-only stragglers
  (`zh/guide/skills-internals.md` has no en counterpart under 002's tree —
  its content is superseded by the mirrored `skills-and-tools` page; note the
  removal in your report).
- `docs/.vitepress/config.mjs` — zh locale only: nav, sidebar, description.
  **The en sidebar/nav from 002 must remain byte-identical.**

**Out of scope**: any en page content; theme files; protected files; new
features; anything outside `docs/zh/` + the zh locale block of `config.mjs`.

## Translation contract

1. **Structure is byte-level mirrored**: same files (paths under `/zh/`),
   same heading lines translated one-for-one (an `##` stays `##`, same count,
   same order), same lists, same tables (headers translated, values
   translated where prose), same custom-block types (TIP stays tip).
2. **Code blocks stay verbatim English** (commands, config keys, code);
   translate only surrounding prose and comments where the comment is prose.
3. **Internal links point at zh mirrors** (`/guide/X` → `/zh/guide/X`);
   anchors keep the translated heading's anchor (VitePress generates it from
   the translated text — link to the translated anchor).
4. **Brand and product nouns stay English**: Forebrain Harness, TUI, gateway,
   REST, WebSocket, LSP, skill, subagent, session, run, sandbox, `/model`,
   `/permissions`, `/plan`, `/goal`. Sentence around them is Chinese.
5. **Terminology glossary first**: read the three existing zh pages and
   extract their term choices (记忆/技能/子代理/网关/会话/沙箱/权限/审批 …),
   record the glossary in your report, and apply it uniformly across every
   new page. Where existing pages and README conventions conflict, prefer
   the README's framing translated.
6. Tone: 简体中文技术文档，动词开头、短句、不堆敬语、不营销腔；与英文页
   信息量逐一对应，不增删段落。
7. The zh landing hero: `name` stays "Forebrain Harness"; `text`, `tagline`,
   feature titles/details translated; action labels translated (Get Started →
   快速开始, Architecture → 架构, GitHub stays) with `link` targets switched
   to zh mirrors where they exist.

## Steps

### Step 1: Glossary

Read `docs/zh/guide/memory-internals.md`, `docs/zh/guide/skills-internals.md`,
`docs/zh/config/projects-and-mcp.md`. Write the term table (EN → 简体中文)
into your working notes; you will also include it in the final report.

**Verify**: glossary covers at minimum: guide, gateway, channel, skill,
subagent, memory, session, run, sandbox, permission, approval, trust,
compaction, language server.

### Step 2: Scaffold + translate the Guide group (10 pages)

Create under `docs/zh/guide/`, mirroring 002's en pages one file at a time:
`getting-started.md`, `what-you-can-do.md`, `cli-and-surfaces.md`（选择你的
工作面）, `gateway.md`, `chat-channels.md`, `code-intelligence.md`,
`memory-systems.md`, `subagents.md`, `skills-and-tools.md`,
`safety-model.md`. Rewrite the existing `zh/guide/memory-internals.md` only
where the en Internals page changed in 002 (it did not change — verify and
leave if identical in structure to its en source; update links to point at
zh mirrors).

**Verify** per batch of 3: `npm run docs:build` → exit 0.

### Step 3: Translate Reference (8) + Internals (6) + Develop (1)

Create `docs/zh/config/` mirrors of the 7 config pages (the existing
`projects-and-mcp.md` is absorbed) and `docs/zh/guide/` mirrors of the 6
Internals pages (architecture, context-and-compaction, memory-internals —
already exists, skills-internals — reuse/replace the zh-only one to mirror
its en source if en exists, else per contract note, agent-collaboration-modes,
telemetry) and `docs/zh/develop/build-and-release.md`.
Accuracy rule: these are reference pages; translate faithfully, keep every
config key, path, and command verbatim.

**Verify**: build exit 0 after each of the three batches.

### Step 4: zh landing page

Create `docs/zh/index.md` mirroring en `index.md` per the translation
contract (hero frontmatter translated per contract point 7; features list
includes the real 15-platform channel wording translated; the five body
sections from 002 translated).

**Verify**: build exit 0; `grep -c "Signal\|SMS" docs/zh/index.md` → 0.

### Step 5: zh locale config

In `config.mjs`'s `locales.zh.themeConfig`:
- `nav`: 指南 → `/zh/guide/getting-started`, 配置 → `/zh/config/overview`,
  开发 → `/zh/develop/build-and-release`, GitHub (external, unchanged).
- `sidebar`: replace the path-keyed rows with **one global sidebar array for
  the zh locale** mirroring 002's four groups, labels translated
  (Guide→指南, Reference→参考, Internals→内部原理, Develop→开发; item labels
  per glossary), links pointing at `/zh/…` mirrors.
- `description`: one value-first Chinese sentence mirroring the en one.
- `link` in the locale header points at `/zh/guide/getting-started`.
- En locale block: **byte-identical to 002's result** — diff before/after and
  keep any accidental change out.

**Verify**: build exit 0; the zh link-check (Step 7) prints no MISSING.

### Step 6: Retire zh-only stragglers

Delete `docs/zh/guide/skills-internals.md` **only after** its useful content
is confirmed covered by the mirrored `zh/guide/skills-and-tools.md` (read
both; if the zh-only page contains material with no home in the mirrored
tree, quote it in your report instead of deleting, and leave the file for
the reviewer).

**Verify**: `ls docs/zh/guide/` matches the en guide file list 1:1 (modulo
`what-you-can-do` etc. all present).

### Step 7 (reviewer runs with you shadowing is fine): full-site acceptance

```bash
cd forebrain-harness.github.io
# 1. file-tree parity: en tree minus zh == empty
diff <(cd docs && find . -name '*.md' -not -path '*/zh/*' | sort) \
     <(cd docs && find . -name '*.md' -path '*/zh/*' | sed 's|/zh/|/|' | sort)
# expected: no output
# 2. per-page structure parity: heading + code-fence counts match
for en in $(cd docs && find . -name '*.md' -not -path '*/zh/*' | sort); do
  zh="docs/zh${en#docs}"; zh=${zh/\.md/.md}
  h1=$(grep -c '^#' "docs/$en"); h2=$(grep -c '^#' "$zh")
  c1=$(grep -c '^```' "docs/$en"); c2=$(grep -c '^```' "$zh")
  [ "$h1" = "$h2" ] && [ "$c1" = "$c2" ] || echo "PARITY-GAP: $en headings $h1/$h2 fences $c1/$c2"
done
# expected: no PARITY-GAP lines
# 3. zh sidebar link resolution
grep -o 'link: "/zh/[^"]*"' docs/.vitepress/config.mjs | sed 's/link: "//;s/"//' | sort -u | while read -r p; do
  [ -f "docs${p}.md" ] || echo "MISSING: $p";
done
# expected: no MISSING lines
# 4. dead 快速开始 link gone
grep -c '快速开始' docs/.vitepress/config.mjs   # ≥1 and its target exists
npm run docs:build                              # exit 0
```

## Test plan

Machine gates: build, tree parity diff, per-page heading/fence parity loop,
zh sidebar link check. Human gates: 3 sampled zh pages read against their en
source paragraph-by-paragraph (owner or reviewer) — information identical,
tone per contract, glossary applied consistently.

## Done criteria

- [ ] `docs/zh/` mirrors the en tree file-for-file (21 pages + landing; tree
      parity diff empty).
- [ ] Every zh page passes the heading/fence parity loop.
- [ ] zh nav/sidebar/description live; en locale block byte-identical to 002.
- [ ] Dead 快速开始 target now exists; zh link check clean.
- [ ] `npm run docs:build` exit 0.
- [ ] Glossary recorded in the executor report.
- [ ] Protected files untouched; no git commit/push.

## STOP conditions

- 002 artifacts missing (drift check fails).
- A en page's meaning cannot be carried into Chinese without inventing
  product behavior (report the page + paragraph; do not guess).
- The zh-only `skills-internals` page holds content with no mirrored home
  (report quotes; wait for reviewer).
- Any verification fails twice after one honest retry.

## Maintenance notes

- Standing rule after this campaign: every future docs change lands en+zh in
  the same change (the parity scripts in Step 7 are the CI-able check).
- The glossary from Step 1 should be promoted into the site repo's README
  (a short "翻译术语表" section) by the reviewer if approved.
- `docs/zh/guide/skills-internals.md` fate (absorbed vs quoted-to-reviewer)
  must be resolved before DONE.
