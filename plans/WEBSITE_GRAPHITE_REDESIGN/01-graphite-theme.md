# Plan 001: Rebuild the docs site theme on the frontend graphite system

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/WEBSITE_GRAPHITE_REDESIGN/README.md` — unless a reviewer
> dispatched you and told you they maintain the index.
>
> **Drift check (run first)** — the target lives in a **separate nested git
> repo** at `forebrain-harness.github.io/` (gitignored by the outer repo):
>
> ```bash
> cd forebrain-harness.github.io
> git rev-parse --short HEAD        # expected: 342cad5
> git status --short                # record this output as your baseline
> git diff --stat 342cad5..HEAD -- docs/.vitepress/
> ```
>
> If HEAD is not `342cad5`, or `docs/.vitepress/` has commits newer than
> `342cad5`, compare the "Current state" excerpts below against the live
> files; on a mismatch, treat it as a STOP condition.
> The baseline `git status --short` is expected to already contain two
> **owner-made, uncommitted** changes — `M docs/config/runtime-gateway-and-channels.md`
> and `M docs/public/mark.svg`. Those files are not yours; see Scope.

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: LOW (static theme CSS + build-time config; no runtime code)
- **Depends on**: none
- **Category**: docs (visual redesign)
- **Planned at**: site repo commit `342cad5`, outer repo `5ce55b5`, 2026-10-07
- **Visual acceptance target**: `plans/WEBSITE_GRAPHITE_REDESIGN/design-mockup.html`
  (open in a browser; every screen there is the pixel-level reference for this plan)

## Why this matters

`forebrain-harness.github.io` is the product's public documentation site
(VitePress 1.6.4, deployed to GitHub Pages from its own repo). Its current
theme is a teal-gradient look — gradient hero name, three body radial-gradients,
glass-blur navbar, serif display font, gradient feature cards. The product's
own web frontend (`frontend/` in the main repo) defines a strict design system:
the **graphite** brand scheme (#1C1C1F near-black on white, solid colors only,
no gradients anywhere, hairline dividers, system sans stack, Monokai code
pair). The docs site currently looks like a different product. This plan
rewires the site's entire visual layer onto the frontend graphite tokens, with
zero content changes.

## Current state

Files that matter (all paths relative to `forebrain-harness.github.io/`):

- `docs/.vitepress/theme/style.css` — the entire current theme (~200 lines).
  Its defining excerpts, all of which this plan **removes**:
  ```css
  :root {
    --vp-c-brand-1: #0d91b0;
    --vp-c-brand-2: #19b7d2;
    --vp-font-family-base: "Source Serif 4", "Iowan Old Style", "Palatino Linotype", serif;
    --vp-font-family-mono: "IBM Plex Mono", "SFMono-Regular", monospace;
    --vp-home-hero-name-color: transparent;
    --vp-home-hero-name-background: linear-gradient(120deg, #045c79 0%, #13a9cb 42%, #9df5ee 100%);
    --vp-home-hero-image-background-image:
      radial-gradient(circle at 18% 18%, rgba(104, 246, 255, 0.34), transparent 28%), ...
  }
  body { background: radial-gradient(...), linear-gradient(...), var(--vp-c-bg); }
  .VPNavBar { background: rgba(234, 244, 245, 0.78) !important; backdrop-filter: blur(16px); }
  .VPFeature { background: linear-gradient(...); box-shadow: 0 18px 40px rgba(9, 52, 74, 0.08); }
  ```
- `docs/.vitepress/config.mjs` — site config; the only line this plan changes:
  ```js
  markdown: {
    theme: {
      light: "github-light",
      dark: "github-dark"
    }
  },
  ```
- `docs/.vitepress/theme/index.js` — extends the default theme and renders
  Mermaid blocks. **Out of scope; do not touch.** (Its mermaid theme is
  `"neutral"`, which already fits graphite.)
- Content pages: `docs/index.md`, `docs/guide/*.md` (12), `docs/config/*.md`
  (7), `docs/develop/*.md`, `docs/zh/**` — **all out of scope.**
- `.github/workflows/deploy.yml` — CI: `npm ci && npm run docs:build` on every
  push/PR to main; push to main **deploys** the site. Out of scope.
- `node_modules/` already exists in the site repo (no install step needed).

Verified VitePress 1.6.4 facts this plan relies on (checked in the installed
package):

- `markdown.theme` values are passed straight into shiki's
  `createHighlighter({ themes: [light, dark] })`, and shiki's `themes` option
  accepts a raw `ThemeRegistrationAny` **object** — so an imported theme
  object can be used directly as the light theme.
- All CSS variable names used in Step 4 exist in
  `node_modules/vitepress/dist/client/theme-default/styles/vars.css`
  (including `--vp-code-bg`, `--vp-code-block-bg`, `--vp-nav-bg-color`,
  `--vp-sidebar-bg-color`, `--vp-home-hero-name-color`,
  `--vp-home-hero-image-background-image`, `--vp-home-hero-image-filter`).

## Design source of truth (inlined — the executor needs no other document)

Authoritative file: `/Users/doudou/workspace/unionj-cloud/forebrain-harness/frontend/src/assets/main.css`.
Its own rule, quoted: *"Content area: always white, always flat. One surface,
one alt, hairlines."* Product rulings that bind this redesign: **no gradients
anywhere**, restrained solid-color UI, system sans stack, Monokai as the one
code theme.

Token table (hex values are copied, not invented):

| Role | Light | Dark | Frontend source |
|---|---|---|---|
| Brand / links / active | `#1C1C1F`, hover `#3A3A40` | `#EDEDEF` | `:root[data-brand="graphite"]` / rail-mark |
| Brand soft | `#F1F1F2` | `#2A2A2E` (rail-active-bg) | graphite brand-soft / rail-dark |
| Page bg | `#FFFFFF` | `#141416` (rail-dark bg) | `--forebrain-bg` / rail-dark |
| Alt surface | `#F7F8FA` | `#1C1C1F` | `--forebrain-bg-alt` / rail field/pop |
| Soft surface | `#F2F4F7`, hover `#EAECF0` | `#1E1E21`, hover `#2A2A2E` | `--forebrain-code-surface` / rail hover |
| Hairline | `#E4E7EC`, strong `#D0D5DD` | `#26262A` | `--forebrain-divider(-strong)` / rail-line |
| Text | `#101828` / `#475467` / `#667085` | `#EDEDEF` / `#A1A1A8` / `#A1A1A8` | `--forebrain-text(-2, muted)` / rail fg, fg-2 |
| Sidebar (= the product's rail) | `#F6F6F7`, line `#E4E4E7`, hover `#ECECEE`, active-bg `#E6E6E8`, active text `#18181B` | `#141416`, line `#26262A`, hover `#1E1E21`, active-bg `#2A2A2E`, active text `#FFFFFF` | rail-light / rail-dark blocks |
| Brand button | bg `#1C1C1F`→`#3A3A40`, text `#FFFFFF` | bg `#FFFFFF`→`#E4E4E7`, text `#141416` (inverted — the rail-dark signature) | rail btn tokens |
| Alt button | bg `#F2F4F7`→`#EAECF0`, text `#101828` | bg `#1E1E21`→`#2A2A2E`, text `#EDEDEF` | alt-button tokens / rail |
| Custom block tip | bg `#ECFDF3`, accent `#067647` | bg `#1E1E21`, accent `#47CD89` | success(-soft) / one step up the same scale |
| Custom block warning | bg `#FFFAEB`, accent `#B54708` | bg `#1E1E21`, accent `#FDB022` | warning(-soft) / one step up |
| Custom block danger | bg `#FEF3F2`, accent `#B42318` | bg `#1E1E21`, accent `#F04438` | danger; `#FEF3F2` is the 50-step of the same scale as `#B42318`; `#F04438` is the frontend's own dark-surface danger (`--rail-danger`) |
| Fonts | sans: `-apple-system, BlinkMacSystemFont, "PingFang SC", "Hiragino Sans GB", "Segoe UI", "Microsoft YaHei", "Noto Sans SC", Roboto, "Helvetica Neue", Arial, sans-serif`; mono: `ui-monospace, SFMono-Regular, "SF Mono", Menlo, monospace` | same | `--forebrain-ui-font` / `--forebrain-mono-font` |
| Code canvas | `#FAFAFA` (Monokai Light), text `#111111` | `#272822` (Monokai), text `#F8F8F2` | `--forebrain-code-bg`/`-text` + Monokai pair contract |

Decisions already made (do not re-litigate in the executor):

1. Docs sidebar and right TOC take the product's **rail** palette (light:
   `#F6F6F7`, dark: `#141416`).
2. **Dark mode stays**, mapped to the rail-dark palette (graphite defines a
   dark rail surface; the docs dark mode is its extension, including the
   inverted white brand button).
3. Mermaid stays `"neutral"` (gray-scale; fits graphite; file untouched).
4. Links are the near-black brand + underline (without underline they are
   indistinguishable from body text); underline color = strong hairline.
5. The product's code-highlight contract: light = **monokailight** (custom
   registration; shiki bundles no light Monokai), dark = **monokai**. Canvas
   `#FAFAFA`/`#272822`. The Monokai pink `#F92672` lives only inside code
   tokens, never in UI chrome.

## Commands you will need

Run everything from `/Users/doudou/workspace/unionj-cloud/forebrain-harness/forebrain-harness.github.io/`.

| Purpose | Command | Expected on success |
|---|---|---|
| Build (the CI gate) | `npm run docs:build` | exit 0; writes `docs/.vitepress/dist/` |
| Dev server | `npm run docs:dev` | prints a local URL (default `http://localhost:5173/`) |
| Preview build | `npm run docs:preview` | prints a local preview URL |
| Theme object sanity | `node` one-liners given in Step 2 | prints the asserted values, exit 0 |

## Scope

**In scope** (the only files you may create/modify — all inside the site repo):

- `docs/.vitepress/theme/style.css` — full rewrite (Step 4)
- `docs/.vitepress/config.mjs` — the `markdown` block + one import line only (Step 3)
- `docs/.vitepress/shiki/monokai-light.mjs` — new file (Step 2)

**Out of scope** (do NOT touch, even though they look related):

- `docs/.vitepress/theme/index.js` — Mermaid logic; its `neutral` theme stays.
- Every `docs/**/*.md` content page — zero content changes.
- `docs/public/mark.svg` and `docs/config/runtime-gateway-and-channels.md` —
  the **owner has uncommitted changes** in exactly these two files; any edit
  here destroys their work.
- `.github/workflows/deploy.yml`, `README.md`, `package.json`, `package-lock.json`.
- Anything outside `forebrain-harness.github.io/` (the outer repo's `plans/`
  directory excepted for status updates).

## Git workflow

**Do not run `git commit`, `git push`, `git merge`, or `git rebase` in either
repo.** The owner reviews and commits by hand. Leave all changes in the site
repo's working tree. Do not create branches. Never touch the two owner-modified
files listed above.

## Steps

### Step 1: Baseline

Run the drift-check block at the top of this plan and store its output. Your
end-of-task `git status --short` must show only your three in-scope paths
added on top of the recorded baseline.

**Verify**: baseline recorded; `git diff --stat 342cad5..HEAD -- docs/.vitepress/` printed empty (or drifted files were compared and matched — otherwise STOP).

### Step 2: Port the Monokai Light theme registration

Create `docs/.vitepress/shiki/monokai-light.mjs`.

**Route A (preferred)**: copy the object from the frontend source of truth at
`/Users/doudou/workspace/unionj-cloud/forebrain-harness/ai-elements-vue/packages/elements/src/code-block/monokai-light.ts`,
then strip its TypeScript: remove the `import type { ThemeRegistration } from 'shiki'`
line, remove the `: ThemeRegistration` annotation, and keep `export const monokaiLight = { ... }`.
Keep the object **byte-identical** otherwise (same `name: 'monokailight'`,
same `colors`, same `tokenColors` order).

**Route B (only if Route A's path is unreadable)**: derive it from the site's
own shiki bundle `node_modules/shiki/themes/monokai.mjs` by applying this
color-for-color substitution to the bundled Monokai (scope table untouched):

```
#f8f8f2, #cfcfc2 → #111111   names, punctuation, plain text
#272822          → #fafafa   canvas
#66d9ef          → #00a8c8   storage and keyword cyan
#a6e22e          → #75af00   functions, classes, attributes
#e6db74, #fd971f → #d88200   strings, parameters
#f44747          → #960050   invalid
#6796e6          → #2f6fd0   editor annotation tokens
#cd9731          → #a06a00
#b267e6          → #7b3fbf
#88846f          → #75715e   comments
#ae81ff, #f92672 unchanged   literals, operators and tags
```

The registration must carry: `name: 'monokailight'`, `displayName: 'Monokai Light'`,
`type: 'light'`, `colors: { 'editor.background': '#fafafa', 'editor.foreground': '#111111' }`.

**Verify** (from the site repo root):

```bash
node --input-type=module -e "
import { monokaiLight as t } from './docs/.vitepress/shiki/monokai-light.mjs';
const fg = (t.tokenColors ?? []).map(e => e.settings?.foreground).filter(Boolean).map(c => c.toLowerCase());
if (t.name !== 'monokailight') throw new Error('bad name');
if (t.colors['editor.background'] !== '#fafafa' || t.colors['editor.foreground'] !== '#111111') throw new Error('bad canvas');
for (const c of ['#f8f8f2','#272822','#66d9ef','#a6e22e','#e6db74']) if (fg.includes(c)) throw new Error('dark monokai leaked: ' + c);
for (const c of ['#75af00','#00a8c8','#d88200','#f92672','#75715e']) if (!fg.includes(c)) throw new Error('missing accent: ' + c);
console.log('MONOKAILIGHT-OK');
"
```

Expected output: `MONOKAILIGHT-OK`.

### Step 3: Rewire `config.mjs` to the Monokai pair

In `docs/.vitepress/config.mjs`:

1. Add as the first line: `import { monokaiLight } from "./shiki/monokai-light.mjs";`
2. Replace the markdown block:

```js
  markdown: {
    theme: {
      light: "github-light",
      dark: "github-dark"
    }
  },
```

with:

```js
  markdown: {
    theme: {
      light: monokaiLight,
      dark: "monokai"
    }
  },
```

Change nothing else in the file (nav, sidebar, locales, search, footer all stay).

**Verify**: `npm run docs:build` → exit 0. If the build fails with a shiki
theme-resolution error, STOP and report (see STOP conditions).

### Step 4: Rewrite `docs/.vitepress/theme/style.css`

Replace the file's **entire contents** with exactly this (it is the complete
target; do not "improve" it):

```css
/* Forebrain Harness docs — graphite theme.
   Source of truth: frontend/src/assets/main.css in the main repository
   (brand scheme "graphite" plus the shared base palette).
   Rules: solid colours only — no gradients anywhere; the content area is
   white and flat; one alt surface; hairline dividers; system sans; code is
   always the Monokai pair (Monokai Light canvas in light mode). */

:root {
  /* Surfaces — light mode is the white content area. */
  --vp-c-bg: #ffffff;
  --vp-c-bg-alt: #f7f8fa;
  --vp-c-bg-elv: #ffffff;
  --vp-c-bg-soft: #f2f4f7;
  --vp-c-divider: #e4e7ec;
  --vp-c-gutter: #e4e7ec;
  --vp-c-border: #d0d5dd;
  --vp-c-text-1: #101828;
  --vp-c-text-2: #475467;
  --vp-c-text-3: #667085;

  /* Graphite brand (frontend :root[data-brand="graphite"]). */
  --vp-c-brand-1: #1c1c1f;
  --vp-c-brand-2: #3a3a40;
  --vp-c-brand-3: #a1a1aa;
  --vp-c-brand-soft: #f1f1f2;

  /* Typography — the product's system stacks. */
  --vp-font-family-base: -apple-system, BlinkMacSystemFont, "PingFang SC",
    "Hiragino Sans GB", "Segoe UI", "Microsoft YaHei", "Noto Sans SC", Roboto,
    "Helvetica Neue", Arial, sans-serif;
  --vp-font-family-mono: ui-monospace, SFMono-Regular, "SF Mono", Menlo, monospace;

  /* Flat chrome. */
  --vp-nav-bg-color: #ffffff;
  --vp-sidebar-bg-color: #f6f6f7; /* graphite rail, light */

  /* Buttons — brand is the graphite ink block; alt is the neutral chip. */
  --vp-button-brand-border: #1c1c1f;
  --vp-button-brand-text: #ffffff;
  --vp-button-brand-bg: #1c1c1f;
  --vp-button-brand-hover-border: #3a3a40;
  --vp-button-brand-hover-text: #ffffff;
  --vp-button-brand-hover-bg: #3a3a40;
  --vp-button-brand-active-border: #3a3a40;
  --vp-button-brand-active-text: #ffffff;
  --vp-button-brand-active-bg: #3a3a40;

  --vp-button-alt-border: #f2f4f7;
  --vp-button-alt-text: #101828;
  --vp-button-alt-bg: #f2f4f7;
  --vp-button-alt-hover-border: #eaecf0;
  --vp-button-alt-hover-text: #101828;
  --vp-button-alt-hover-bg: #eaecf0;
  --vp-button-alt-active-border: #eaecf0;
  --vp-button-alt-active-text: #101828;
  --vp-button-alt-active-bg: #eaecf0;

  /* Code — Monokai Light canvas in light mode. */
  --vp-code-color: #101828;
  --vp-code-bg: #f2f4f7;
  --vp-code-block-bg: #fafafa;
  --vp-code-lang-color: #667085;
  --vp-code-copy-code-bg: #f2f4f7;
  --vp-code-copy-code-hover-bg: #eaecf0;
  --vp-code-copy-code-border-color: #e4e7ec;

  /* Custom blocks — soft, flat, one accent each. */
  --vp-custom-block-info-bg: #f2f4f7;
  --vp-custom-block-info-border: #d0d5dd;
  --vp-custom-block-info-text: #475467;
  --vp-custom-block-info-code-bg: #eaecf0;
  --vp-custom-block-tip-bg: #ecfdf3;
  --vp-custom-block-tip-border: #067647;
  --vp-custom-block-tip-text: #067647;
  --vp-custom-block-tip-code-bg: #f2f4f7;
  --vp-custom-block-warning-bg: #fffaeb;
  --vp-custom-block-warning-border: #b54708;
  --vp-custom-block-warning-text: #b54708;
  --vp-custom-block-warning-code-bg: #f2f4f7;
  --vp-custom-block-danger-bg: #fef3f2;
  --vp-custom-block-danger-border: #b42318;
  --vp-custom-block-danger-text: #b42318;
  --vp-custom-block-danger-code-bg: #f2f4f7;

  /* Hero — no gradient name, no glow behind the mark. */
  --vp-home-hero-name-color: var(--vp-c-text-1);
  --vp-home-hero-image-background-image: none;
  --vp-home-hero-image-filter: none;
}

/* Dark mode = the graphite rail's dark palette (#141416 family), including
   its signature: brand buttons invert to white. */
.dark {
  --vp-c-bg: #141416;
  --vp-c-bg-alt: #1c1c1f;
  --vp-c-bg-elv: #1c1c1f;
  --vp-c-bg-soft: #1e1e21;
  --vp-c-divider: #26262a;
  --vp-c-gutter: #26262a;
  --vp-c-border: #26262a;
  --vp-c-text-1: #ededef;
  --vp-c-text-2: #a1a1a8;
  --vp-c-text-3: #a1a1a8;

  --vp-c-brand-1: #ededef;
  --vp-c-brand-2: #ffffff;
  --vp-c-brand-3: #a1a1a8;
  --vp-c-brand-soft: #2a2a2e;

  --vp-nav-bg-color: #141416;
  --vp-sidebar-bg-color: #141416;

  --vp-button-brand-border: #ffffff;
  --vp-button-brand-text: #141416;
  --vp-button-brand-bg: #ffffff;
  --vp-button-brand-hover-border: #e4e4e7;
  --vp-button-brand-hover-text: #141416;
  --vp-button-brand-hover-bg: #e4e4e7;
  --vp-button-brand-active-border: #e4e4e7;
  --vp-button-brand-active-text: #141416;
  --vp-button-brand-active-bg: #e4e4e7;

  --vp-button-alt-border: #26262a;
  --vp-button-alt-text: #ededef;
  --vp-button-alt-bg: #1e1e21;
  --vp-button-alt-hover-border: #2a2a2e;
  --vp-button-alt-hover-text: #ededef;
  --vp-button-alt-hover-bg: #2a2a2e;
  --vp-button-alt-active-border: #2a2a2e;
  --vp-button-alt-active-text: #ededef;
  --vp-button-alt-active-bg: #2a2a2e;

  /* Inline code on the dark neutral; blocks sit on the Monokai canvas. */
  --vp-code-color: #ededef;
  --vp-code-bg: #1c1c1f;
  --vp-code-block-bg: #272822;
  --vp-code-lang-color: #a1a1a8;
  --vp-code-copy-code-bg: #1e1e21;
  --vp-code-copy-code-hover-bg: #2a2a2e;
  --vp-code-copy-code-border-color: #26262a;

  /* Status hues step one rung up the same scales the light tokens come from. */
  --vp-custom-block-info-bg: #1e1e21;
  --vp-custom-block-info-border: #3a3a40;
  --vp-custom-block-info-text: #a1a1a8;
  --vp-custom-block-info-code-bg: #1c1c1f;
  --vp-custom-block-tip-bg: #1e1e21;
  --vp-custom-block-tip-border: #47cd89;
  --vp-custom-block-tip-text: #47cd89;
  --vp-custom-block-tip-code-bg: #1c1c1f;
  --vp-custom-block-warning-bg: #1e1e21;
  --vp-custom-block-warning-border: #fdb022;
  --vp-custom-block-warning-text: #fdb022;
  --vp-custom-block-warning-code-bg: #1c1c1f;
  --vp-custom-block-danger-bg: #1e1e21;
  --vp-custom-block-danger-border: #f04438;
  --vp-custom-block-danger-text: #f04438;
  --vp-custom-block-danger-code-bg: #1c1c1f;
}

/* Feature cards: one surface, one hairline, no shadow, no gradient. */
.VPFeature {
  border: 1px solid var(--vp-c-divider);
  background: var(--vp-c-bg-elv);
  box-shadow: none;
}
.VPFeature:hover {
  border-color: var(--vp-c-border);
}

/* Prose links: the brand ink needs an underline to stay scannable. */
.vp-doc a {
  text-decoration: underline;
  text-underline-offset: 0.14em;
  text-decoration-color: var(--vp-c-border);
}
.vp-doc a:hover {
  text-decoration-color: currentColor;
}

.vp-doc h1,
.vp-doc h2,
.vp-doc h3,
.VPHomeHero .name,
.VPHomeHero .text {
  letter-spacing: -0.02em;
}

.vp-doc table {
  display: table;
  width: 100%;
}

/* Code blocks sit on the Monokai canvas inside a hairline frame. */
.vp-doc div[class*='language-'] {
  border: 1px solid var(--vp-c-divider);
}

/* Mermaid blocks keep their frame; the diagram theme itself stays neutral. */
.vp-doc div.language-mermaid {
  overflow-x: auto;
  border: 1px solid var(--vp-c-divider);
  background: var(--vp-c-bg-elv);
  padding: 18px;
}

.language-mermaid > pre,
.language-mermaid .copy,
.language-mermaid .lang {
  display: none !important;
}

.language-mermaid .mermaid-render {
  overflow-x: auto;
}

.language-mermaid .mermaid-render svg {
  display: block;
  min-width: 640px;
  margin: 0 auto;
}
```

**Verify**: `npm run docs:build` → exit 0, and:

```bash
grep -ci gradient docs/.vitepress/theme/style.css   # expected: 0
grep -ci "0d91b0\|Source Serif\|IBM Plex" docs/.vitepress/theme/style.css  # expected: 0
```

### Step 5: Machine checks on the built output

```bash
npm run docs:build
grep -rlo "shiki-dark" docs/.vitepress/dist/assets/ | head -1   # expected: a file path (dual-theme vars emitted)
grep -o "1c1c1f" docs/.vitepress/dist/assets/chunks/*.css 2>/dev/null | head -1 || \
  grep -rlo "1c1c1f" docs/.vitepress/dist/assets/ | head -1      # expected: a hit (graphite tokens in the bundle)
```

Expected: both commands print a hit, exit 0.

### Step 6: Visual acceptance against the design sheet

Open `plans/WEBSITE_GRAPHITE_REDESIGN/design-mockup.html` in a browser (it is
the pixel reference: four screens — light home, light doc, dark doc, dark
home). Then run `npm run docs:dev`, open the printed URL, and check:

- Home: hero name solid near-black (no gradient), mark flat (no glow), black
  pill "Get Started", gray alt pills, white hairline feature cards, no page
  background tint.
- Doc page: sidebar on `#F6F6F7` (light) with near-black active item + left
  bar; content column white; code blocks on `#FAFAFA` with `#111` text;
  tip/warning/danger blocks green/amber/red soft with matching border; links
  near-black underlined.
- Dark toggle: page on `#141416`, sidebar `#141416`, active items white with
  white bar, **brand CTA buttons invert to white with dark text**, code blocks
  on the Monokai canvas `#272822`, mermaid diagrams still render.
- No gradients anywhere; no serif text anywhere; no leftover teal (#0d91b0).

### Step 7: Update the index

Set the plan's row to DONE in `plans/WEBSITE_GRAPHITE_REDESIGN/README.md`
(one-line status edit in the outer repo's `plans/` directory is allowed).

## Test plan

This repo has no unit tests; CI gates on the build (`npm ci && npm run
docs:build` in `.github/workflows/deploy.yml`), which Step 3/5 run locally.
Additional machine checks: the Step 2 node assertions (theme registration
contract, mirroring the frontend's own `codeTheme.test.ts` assertions), the
Step 4/5 greps (no gradients, no old palette, dual-theme vars in dist).
Visual: Step 6 against the design sheet.

## Done criteria

- [ ] `git -C forebrain-harness.github.io status --short` shows exactly three
      changed paths vs the Step 1 baseline: modified `docs/.vitepress/theme/style.css`,
      modified `docs/.vitepress/config.mjs`, new `docs/.vitepress/shiki/monokai-light.mjs`.
      The owner's two pre-existing modified files are untouched.
- [ ] `npm run docs:build` exits 0.
- [ ] Step 2 node assertion prints `MONOKAILIGHT-OK`.
- [ ] `grep -c "gradient(" docs/.vitepress/theme/style.css` → `0` (per Amendment 2).
- [ ] `grep -ci "0d91b0\|Source Serif\|IBM Plex" docs/.vitepress/theme/style.css` → `0`.
- [ ] Step 5 dist checks both hit.
- [ ] Step 6 visual checklist passes; no commit/push was made.
- [ ] `plans/WEBSITE_GRAPHITE_REDESIGN/README.md` status row updated.

## Amendment 1 (review finding, 2026-10-08): brand the site title

Owner acceptance caught the navbar and `<title>` showing **"VitePress"** —
`config.mjs` never sets a site `title`, so VitePress's framework default
leaks into the navbar text and every page title. This contradicts the
approved design sheet (navbar = mark + "Forebrain Harness").

Fix (still inside Step 3's file, one line): in `docs/.vitepress/config.mjs`,
add `title: "Forebrain Harness",` as the first key of `defineConfig({ ... })`.
No locale overrides `title`, so this single line covers en + zh, the navbar
text (falls back to `title`), and all `<title>` tags
("Getting Started | Forebrain Harness").

**Verify**:

```bash
npm run docs:build
grep -o "<title>[^<]*</title>" docs/.vitepress/dist/index.html
# expected: <title>Forebrain Harness</title>
grep -o "<title>[^<]*</title>" docs/.vitepress/dist/guide/getting-started.html
# expected: <title>Getting Started | Forebrain Harness</title>
grep -c "<title>[^<]*VitePress" docs/.vitepress/dist/**/*.html 2>/dev/null || true
# expected: no hits (glob may not match under zsh; use find | xargs grep -l)
```

## Amendment 2 (review verdicts, 2026-10-08)

1. The Step 4 / Done-criteria check `grep -ci gradient → 0` was self-contradictory
   (the mandated CSS comments legitimately contain the word "gradient"). The
   corrected check is `grep -c "gradient("` → 0 (gradient *functions*). The CSS
   stays verbatim; both gates now read:
   ```bash
   grep -c "gradient(" docs/.vitepress/theme/style.css   # expected: 0
   grep -ci "0d91b0\|Source Serif\|IBM Plex" docs/.vitepress/theme/style.css  # expected: 0
   ```
2. `docs/.vitepress/shiki/monokai-light.mjs` carries a three-line provenance
   comment above the registration (naming the frontend source file it was
   ported from). The registration object itself is byte-identical to the
   frontend source of truth — accepted reviewer deviation.
3. Step 5's first dist check assumed CSS under `assets/chunks/`; VitePress
   1.6.4 emits the stylesheet at `assets/style.*.css`, so the fallback branch
   (`grep -rlo "1c1c1f" docs/.vitepress/dist/assets/`) is the canonical form.

## STOP conditions

Stop and report back (do not improvise) if:

- HEAD is not `342cad5` or any in-scope file's live content no longer matches
  the "Current state" excerpts.
- The build fails with a shiki error like `Theme ... not found` after Step 3
  (theme-object wiring assumption broken) — report; do not switch to
  `loadTheme`/`shikiSetup` workarounds on your own.
- Route A and Route B of Step 2 are both impossible.
- Completing any step appears to require editing an out-of-scope file
  (especially the two owner-modified files).
- Any verification fails twice after one honest retry.

## Maintenance notes

- The single source of truth is `frontend/src/assets/main.css` in the main
  repo. If the graphite palette changes there, update: (a) the token values in
  `style.css`, (b) `shiki/monokai-light.mjs` only if the code-highlight
  contract itself changes (it is regenerated from
  `ai-elements-vue/packages/elements/src/code-block/monokai-light.ts`).
- New brand schemes added to the frontend (navy, teal, …) intentionally do not
  propagate to the docs site — the site is graphite-only by decision. Revisit
  only if the owner asks for a site-side brand switcher.
- If VitePress is ever upgraded past 1.x, re-verify the shiki theme-object
  wiring (`markdown.theme.light` as an imported object) before touching
  anything else.
- Reviewers should scrutinize: zero content diffs, `config.mjs` diff limited
  to the import + `markdown` block, and no diff at all in
  `theme/index.js`, `docs/public/mark.svg`,
  `docs/config/runtime-gateway-and-channels.md`.
