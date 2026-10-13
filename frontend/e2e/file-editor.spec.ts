import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { expect, test, type Page } from '@playwright/test'

import { shot, signIn } from './support'

/**
 * The shared file editor: Milkdown Crepe for Markdown (WYSIWYG with its top bar
 * and live, highlighted code blocks) and CodeMirror for source and code files.
 * Everything here is exercised through the memory file editor, the one surface
 * that already hosts the editor; the proxy it drives is the same component the
 * rule, workbench and workshop dialogs will use.
 */

const home = process.env.E2E_HOME ?? ''
const globalRoot = home ? join(home, 'workspace', 'memories', 'global') : ''
const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')
const shots = process.env.E2E_SHOTS ?? ''

// The palette, as the browser reports it (rgb). `#272822` is the editor's
// fallback ink (the palette's `foreground`), which a span with no highlight of
// its own inherits; every other colour is a token's.
const PALETTE = [
  'rgb(0, 168, 200)',
  'rgb(249, 38, 114)',
  'rgb(17, 17, 17)',
  'rgb(117, 175, 0)',
  'rgb(174, 129, 255)',
  'rgb(216, 130, 0)',
  'rgb(128, 69, 255)',
  'rgb(117, 113, 94)',
  'rgb(150, 0, 80)',
  'rgb(39, 40, 34)',
]

function seed(name: string, content: string): string {
  const path = join(globalRoot, name)
  mkdirSync(dirname(path), { recursive: true })
  writeFileSync(path, content)
  expect(existsSync(path), `seeded ${path}`).toBe(true)
  return path
}

async function openMemory(page: Page, name: string) {
  await page.goto('/memories', { waitUntil: 'networkidle' })
  await page.click(`[data-testid="memories-edit-${name}"]`)
  const editor = page.locator('[data-testid="memories-editor"]')
  await expect(editor).toBeVisible()
  return editor
}

test('markdown opens in Crepe with its toolbar, a code block, and no plugin errors', async ({ page }) => {
  test.skip(!globalRoot, 'E2E_HOME missing')
  const errors: string[] = []
  page.on('console', (message) => {
    if (message.type() === 'error') errors.push(message.text())
  })
  page.on('pageerror', (error) => errors.push(error.message))

  seed('MEMORY.md', '# 记忆\n\n- 一条记忆\n\n```go\npackage main\n\nfunc main() {}\n```\n')
  await signIn(page)
  const editor = await openMemory(page, 'MEMORY.md')

  // The top bar is on, and the fenced block is a real editor with a language
  // button that is always visible.
  await expect(editor.locator('.milkdown-top-bar')).toBeVisible({ timeout: 15_000 })
  await expect(editor.locator('.milkdown-code-block .language-button')).toBeVisible({ timeout: 15_000 })
  await expect(editor.locator('.milkdown-code-block .cm-content')).toBeVisible({ timeout: 15_000 })

  // Exactly one ProseMirror instance and no keyed-plugin clash: the design
  // preview's duplicated-@milkdown/kit pitfall does not survive Vite bundling.
  expect(await page.locator('.ProseMirror').count()).toBe(1)
  expect(errors.filter((text) => /keyed plugin/i.test(text))).toEqual([])

  await shot(page, 'file-editor-markdown')
})

test('code inside a block is painted with the monokai-light palette', async ({ page }) => {
  test.skip(!globalRoot, 'E2E_HOME missing')
  seed('MEMORY.md', '# 记忆\n\n```go\npackage main\n```\n')
  await signIn(page)
  const editor = await openMemory(page, 'MEMORY.md')

  const block = editor.locator('.milkdown-code-block .cm-content')
  await expect(block).toBeVisible({ timeout: 15_000 })
  await block.click()
  await page.keyboard.press('ControlOrMeta+A')
  await page.keyboard.type('func main() {}')
  await expect(block).toContainText('func')

  const colours = await block.locator('.cm-line span').evaluateAll((nodes) =>
    nodes.map((node) => getComputedStyle(node).color),
  )
  expect(colours.length).toBeGreaterThan(0)
  for (const colour of colours) expect(PALETTE).toContain(colour)
})

test('source and WYSIWYG hold the same document', async ({ page }) => {
  test.skip(!globalRoot, 'E2E_HOME missing')
  seed('MEMORY.md', '# 记忆\n\n- 一条记忆\n')
  await signIn(page)
  const editor = await openMemory(page, 'MEMORY.md')

  const prose = editor.locator('.ProseMirror')
  await expect(prose).toBeVisible({ timeout: 15_000 })
  const richBefore = await prose.innerText()

  await editor.locator('[data-testid="editor-mode-src"]').click()
  const source = editor.locator('.cm-content')
  await expect(source).toBeVisible({ timeout: 15_000 })
  await expect(source).toContainText('# 记忆')
  await expect(source).toContainText('一条记忆')
  await shot(page, 'file-editor-markdown-source')

  await editor.locator('[data-testid="editor-mode-rich"]').click()
  await expect(editor.locator('.ProseMirror')).toBeVisible({ timeout: 15_000 })
  expect(await editor.locator('.ProseMirror').innerText()).toBe(richBefore)
})

test('the settings config file opens in the code editor', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)
  await page.goto('/settings', { waitUntil: 'networkidle' })
  await page.locator('.settings-tab', { hasText: '配置文件' }).click()
  const editor = page.locator('[data-testid="file-editor"]')
  await expect(editor.locator('.cm-content')).toBeVisible({ timeout: 15_000 })
  await expect(editor).toContainText('YAML')
  // YAML is not Markdown: there is no WYSIWYG pane, and no edit means no save.
  await expect(editor.locator('[data-testid="editor-mode-src"]')).toHaveCount(0)
  await expect(page.locator('[data-testid="config-apply"]')).toBeDisabled()
  await shot(page, 'file-editor-config')
})

test('an untouched file cannot be saved', async ({ page }) => {
  test.skip(!globalRoot, 'E2E_HOME missing')
  seed('MEMORY.md', '# 记忆\n\n- 一条记忆\n')
  await signIn(page)
  const editor = await openMemory(page, 'MEMORY.md')
  await expect(editor.locator('.ProseMirror')).toBeVisible({ timeout: 15_000 })
  // Nothing was edited, so the save button stays disabled and no write can
  // happen — the no-edit-no-write guarantee.
  await expect(editor.locator('[data-testid="memories-editor-save"]')).toBeDisabled()
})

test('a file Crepe reformats on load is still not dirty', async ({ page }) => {
  test.skip(!globalRoot, 'E2E_HOME missing')
  // Tables, task lists and bullets are exactly what Crepe re-serialises, and
  // that work lands after create() resolves. It must not count as an edit.
  seed('zz-table.md', '# T\n\n| a | b |\n| --- | --- |\n| 1 | 2 |\n\n```go\nfunc main() {}\n```\n\n- one\n- two\n')
  await signIn(page)
  const editor = await openMemory(page, 'zz-table.md')
  await expect(editor.locator('.ProseMirror')).toBeVisible({ timeout: 15_000 })
  await expect(editor.locator('.milkdown-code-block')).toBeVisible({ timeout: 15_000 })
  // Let the post-load re-serialisation settle before asking.
  await page.waitForTimeout(1_500)
  await expect(editor.locator('[data-testid="memories-editor-save"]')).toBeDisabled()
})

/**
 * Round-trip fidelity against real repository files: open the file's actual
 * bytes in the WYSIWYG editor, make a no-op edit (type a character, delete it),
 * save, and write both texts out so the difference can be read. The save is
 * skipped when the editor considers the document unchanged.
 *
 * The memory case uses a memory file's own shape (the repo ships none, and the
 * owner's real memory store is out of bounds for a test); the other two are the
 * repository's real FOREBRAIN.md and a real SKILL.md.
 */
const MEMORY_FILE = `# Community memory\n\n## User preferences\n\n- 用中文回复\n- 先给结论，再说理由\n\n## Project notes\n\n- 构建与测试都要带 \`-tags fts5\`\n\n\`\`\`bash\nmake build\n\`\`\`\n`

const ROUND_TRIP: Array<{ name: string; content: () => string }> = [
  { name: 'FOREBRAIN.md', content: () => readFileSync(join(repoRoot, 'FOREBRAIN.md'), 'utf8') },
  { name: 'SKILL.md', content: () => readFileSync(join(repoRoot, '.forebrain/skills/plan-gated-bugfix/SKILL.md'), 'utf8') },
  // Notably NOT `memory.md`: this filesystem is case-insensitive, so a
  // `memory.md` seed would collide with the core `MEMORY.md`.
  { name: 'notes/community.md', content: () => MEMORY_FILE },
]

for (const { name, content } of ROUND_TRIP) {
  test(`round trip: ${name}`, async ({ page }) => {
    test.skip(!globalRoot || !shots, 'E2E_HOME/E2E_SHOTS missing')
    const before = content()
    const path = seed(name, before)
    await signIn(page)
    const listed = await page.request.get('/api/memories/files?scope=global&page=1&page_size=50')
    const paths = ((await listed.json()).files ?? []).map((row: { path: string }) => row.path)
    expect(paths, `memory listing after seeding ${name}`).toContain(name)
    const editor = await openMemory(page, name)

    const prose = editor.locator('.ProseMirror')
    await expect(prose).toBeVisible({ timeout: 15_000 })
    const save = editor.locator('[data-testid="memories-editor-save"]')
    await expect(save).toBeDisabled()

    // One marker character typed at the start of the document, so the save is
    // allowed and the diff's first hunk is the probe; everything else the save
    // changes is Crepe's own re-serialisation of the untouched document.
    await prose.click({ position: { x: 70, y: 22 } })
    await page.keyboard.type('RTZ')
    await expect(save).toBeEnabled({ timeout: 5_000 })
    await save.click()
    await expect(editor).toHaveCount(0, { timeout: 15_000 })

    const after = readFileSync(path, 'utf8')
    writeFileSync(join(shots, `roundtrip-${name.replace('/', '_')}.before.md`), before)
    writeFileSync(join(shots, `roundtrip-${name.replace('/', '_')}.after.md`), after)
  })
}
