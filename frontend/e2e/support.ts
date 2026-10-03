import type { Page, Response } from '@playwright/test'

/** Save a full-page screenshot into the run's shots directory. */
export async function shot(page: Page, name: string): Promise<void> {
  const dir = process.env.E2E_SHOTS
  if (!dir) throw new Error('E2E_SHOTS must point at the screenshots directory')
  await page.screenshot({ path: `${dir}/${name}.png`, fullPage: true })
}

/** Sign the page in with E2E_TOKEN through the one-click sign-in link. */
export async function signIn(page: Page): Promise<void> {
  const token = process.env.E2E_TOKEN
  if (!token) throw new Error('E2E_TOKEN must hold the gateway token')
  // The link form is deterministic: no form fill to race a first-paint
  // re-render, and the hash is scrubbed by the page itself.
  await page.goto(`/login#token=${encodeURIComponent(token)}`)
  try {
    await page.waitForURL((url) => !url.pathname.endsWith('/login'), { timeout: 15_000 })
  } catch {
    // A cold gateway's first asset burst can break one modulepreload; the
    // session cookie is already set, so a plain reload lands in the app.
    await page.reload({ waitUntil: 'load' })
    await page.waitForURL((url) => !url.pathname.endsWith('/login'), { timeout: 15_000 })
  }
  await page.waitForLoadState('networkidle')
}

/**
 * Collect every 401 on /api/ the page sees. The session probe's own 401 is
 * the sign-in check working, not a leak, so it is filtered out.
 */
export function watch401(page: Page): () => Response[] {
  const seen: Response[] = []
  page.on('response', (response) => {
    if (response.status() === 401 && response.url().includes('/api/')) seen.push(response)
  })
  return () => seen.filter((response) => !response.url().includes('/api/auth/session'))
}

/**
 * Wait for the assistant's answer. With the fake provider the reply is an
 * exact known string; with a real model (E2E_REAL_LLM=1) the content is not
 * predictable, so any non-empty assistant message passes.
 */
export async function expectAssistantReply(page: Page): Promise<void> {
  if (process.env.E2E_REAL_LLM === '1') {
    // A real reply's words are unknown; what is known is how a turn ends.
    // The streaming placeholder ("...") is not a reply, and every run closes
    // with its worked line — so wait for words and for that line.
    await page.waitForFunction(
      () => {
        const replied = Array.from(document.querySelectorAll('.is-assistant'))
          .some((node) => (node.textContent ?? '').replace(/[.…\s]/g, '').length > 0)
        const closed = /已工作 \d|Worked for \d/.test(document.querySelector('.chat-shell main')?.textContent ?? '')
        return replied && closed
      },
      undefined,
      { timeout: 150_000 },
    )
    return
  }
  const reply = process.env.E2E_REPLY_OK ?? 'E2E_REPLY_OK'
  await page.waitForFunction(
    (text) => document.body.innerText.includes(text),
    reply,
    { timeout: 60_000 },
  )
}
