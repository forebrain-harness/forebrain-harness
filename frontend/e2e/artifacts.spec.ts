import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'

import { expectAssistantReply, seedSessionInRoot, shot, signIn } from './support'

/**
 * The office conversation's deliverables, on the real page (plan 153): the
 * model hands three files back with one `present_files` call, and the person
 * meets them once, at the end of the turn — the group under the last words,
 * never a second copy inside the tool card. Each row downloads (200) and each
 * one the engine says is previewable opens in the drawer's own tab: Markdown
 * rendered, a PNG as an image, a .docx laid out by docx-preview.
 *
 * The files are made here, in the project's own directory, because
 * `present_files` only delivers what is inside the project: a Markdown note,
 * a one-pixel PNG and a real (minimal) OOXML package. The provider is
 * scripted through the marker the fake provider understands
 * ([[e2e:tool-call]] followed by the tool's name and arguments), so the names
 * and titles below are the ones the model "presents".
 */

const MD_NAME = '季度报告.md'
const PNG_NAME = '渠道分布.png'
const DOCX_NAME = '交付说明.docx'
const MD_TEXT = '本季度三条渠道的交付与结论'
const DOCX_TEXT = '交付说明：三份交付都在本轮的交付物里'

/** A one-pixel PNG: the smallest thing a browser will draw as an image. */
const PNG_BASE64 = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=='

/** The scratch trees this spec made, removed when it is done. */
const scratch: string[] = []

test.afterAll(() => {
  for (const dir of scratch) rmSync(dir, { recursive: true, force: true })
})

/** A minimal but complete .docx: the parts docx-preview insists on, one run of
 *  text. Written by python3 because the spec has no zip writer of its own. */
function writeDocx(path: string): void {
  execFileSync('python3', ['-c', `
import sys, zipfile
document = ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
  '<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">'
  '<w:body><w:p><w:r><w:t>' + sys.argv[2] + '</w:t></w:r></w:p></w:body></w:document>')
content_types = ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
  '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">'
  '<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>'
  '<Default Extension="xml" ContentType="application/xml"/>'
  '<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>'
  '</Types>')
rels = ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
  '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
  '<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>'
  '</Relationships>')
with zipfile.ZipFile(sys.argv[1], 'w') as zf:
    zf.writestr('[Content_Types].xml', content_types)
    zf.writestr('_rels/.rels', rels)
    zf.writestr('word/document.xml', document)
`, path, DOCX_TEXT])
}

/** An office project (a plain directory, so the gateway's own default is 办公
 *  创作) holding the three deliverables, opened in a conversation. */
async function officeSessionWithDeliverables(page: Page): Promise<string> {
  const root = mkdtempSync(join(tmpdir(), 'fb-e2e-153-artifacts-'))
  scratch.push(root)
  const deliverables = join(root, 'deliverables')
  mkdirSync(deliverables, { recursive: true })
  writeFileSync(join(deliverables, MD_NAME), `# ${MD_NAME}\n\n${MD_TEXT}。\n`)
  writeFileSync(join(deliverables, PNG_NAME), Buffer.from(PNG_BASE64, 'base64'))
  writeDocx(join(deliverables, DOCX_NAME))
  const session = await seedSessionInRoot(page, root, 'e2e-153-office')
  const info = await page.request.get(`/api/chat/sessions/${encodeURIComponent(session)}`)
  expect(info.ok(), await info.text()).toBeTruthy()
  // The conversation is an office one: `present_files` is registered for
  // office and batch turns only, and the delivery group is theirs to draw.
  expect(((await info.json()) as { project_type?: string }).project_type).toBe('office')
  return session
}

/** The message that makes the model deliver the three files: the marker the
 *  fake provider turns into one scripted call ([[e2e:tool-call]] followed by
 *  the tool's name and its arguments), plus what the person actually asked. */
function deliveryMessage(): string {
  const files = [
    { path: `deliverables/${MD_NAME}`, title: '季度报告' },
    { path: `deliverables/${PNG_NAME}`, title: '渠道分布' },
    { path: `deliverables/${DOCX_NAME}`, title: '交付说明' },
  ]
  return `[[e2e:tool-call]]${JSON.stringify({ name: 'present_files', arguments: { files } })}\n\n把这三份文件交给我。`
}

test('the model delivers three files: one group at the end, each downloadable and previewable', async ({ page }) => {
  test.info().setTimeout(240_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const session = await officeSessionWithDeliverables(page)
  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await page.fill('textarea', deliveryMessage())
  await page.press('textarea', 'Enter')
  await expectAssistantReply(page)

  // One call, one group: the three files are the turn's deliverables, drawn
  // once — a second copy inside the tool card is exactly what A7 forbids.
  const group = page.locator('[data-testid="artifact-list"]')
  await expect(group).toHaveCount(1)
  await expect(group).toBeVisible()
  await expect(group).toContainText('本轮交付物')
  await expect(group).toContainText('3 个文件')
  const cards = group.locator('[data-testid="artifact-card"]')
  await expect(cards).toHaveCount(3)
  await expect(cards.nth(0)).toContainText('季度报告')
  await expect(cards.nth(0)).toContainText('Markdown')
  await expect(cards.nth(1)).toContainText('渠道分布')
  await expect(cards.nth(1)).toContainText('图片')
  await expect(cards.nth(2)).toContainText('交付说明')
  await expect(cards.nth(2)).toContainText('文档')
  // Nothing was folded away: three rows fit without the quiet "show the rest".
  await expect(group.locator('[data-testid="artifact-list-more"]')).toHaveCount(0)
  // The call's own card says what it did, once.
  await expect(page.locator('.chat-shell main')).toContainText('已交付文件')

  // The group closes the turn: it sits below the assistant's last words.
  const reply = page.locator('.is-assistant').last()
  const replyBox = await reply.boundingBox()
  const groupBox = await group.boundingBox()
  expect(replyBox && groupBox).toBeTruthy()
  expect(groupBox!.y).toBeGreaterThan(replyBox!.y)
  await shot(page, 'p153-artifacts-group')

  // Every row downloads: the link is the engine's own attachment route.
  for (const [index, name] of [MD_NAME, PNG_NAME, DOCX_NAME].entries()) {
    const link = cards.nth(index).locator('[data-testid="artifact-download"]')
    const href = await link.getAttribute('href')
    expect(href, `${name} has a download link`).toBeTruthy()
    const download = await page.request.get(href!)
    expect(download.status(), `${name} download`).toBe(200)
    expect((await download.body()).length, `${name} download body`).toBeGreaterThan(0)
  }

  // Markdown: the drawer's tab renders the file itself.
  await cards.nth(0).locator('[data-testid="artifact-preview"]').click()
  const markdown = page.locator('[data-testid="artifact-markdown"]')
  await expect(markdown).toBeVisible()
  await expect(markdown).toContainText(MD_TEXT)
  await shot(page, 'p153-artifacts-markdown')

  // The PNG: an <img> pointed at the same content route.
  await cards.nth(1).locator('[data-testid="artifact-preview"]').click()
  const image = page.locator('[data-testid="artifact-image"]')
  await expect(image).toBeVisible()
  await expect(image).toHaveAttribute('src', /\/artifacts\/[^/]+\/content/)

  // The .docx: docx-preview lays the document out in the tab — the words it
  // carries end up in the page, which a download-only row never would.
  await cards.nth(2).locator('[data-testid="artifact-preview"]').click()
  const docx = page.locator('[data-testid="artifact-docx"]')
  await expect(docx).toBeVisible()
  await expect(docx).toContainText(DOCX_TEXT, { timeout: 30_000 })
  await shot(page, 'p153-artifacts-docx')
})
