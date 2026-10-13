import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

import { expect, test } from '@playwright/test'

import { shot, signIn } from './support'

/**
 * Preview parity: the same six documents the design preview (`app-preview.html`)
 * mounts its editors on, opened through the app's real file editor, so the two
 * can be laid side by side. The rule-file states (1-3) have no modal in the app
 * yet — plan 018 builds it — so they run through the memory editor, which hosts
 * the same component; the chrome around the editor differs, the editor does not.
 */

const home = process.env.E2E_HOME ?? ''
const globalRoot = home ? join(home, 'workspace', 'memories', 'global') : ''

function seed(name: string, content: string): void {
  const path = join(globalRoot, name)
  mkdirSync(path.substring(0, path.lastIndexOf('/')), { recursive: true })
  writeFileSync(path, content)
}

const AGENTS = `# AGENTS

本主代理的行为准则。

## 红线

- 先遵守系统与开发者指令。
- 改代码前先写计划，等 **owner** 审批。
- 不要把密钥写进工作区的 markdown。
- 修 bug 必须修根因，禁止防御性补丁。

## 会话开始时

1. 读 \`USER.md\`，了解称呼和偏好
2. 确认工作区干净：

\`\`\`bash
git status --short
\`\`\`
`

const FOREBRAIN = `# FOREBRAIN.md

Local AI coding agent. Single Go module, Go 1.26, CGO mandatory.

## Commands

\`\`\`bash
# 构建与测试都要带 fts5
make build
CGO_ENABLED=1 go test -tags fts5 ./pkg/... -count=1
\`\`\`

## Checklist

- [x] 测试加 \`-tags fts5\`
- [ ] 前端改动后跑 \`make ui\`

| 目录 | 内容 |
| --- | --- |
| pkg/tui | 终端界面 |
| pkg/gateway | 网页端与网关 |
`

const SKILL = `---
name: gateway-ops
description: 网关 5xx 排障与恢复：先确认没有发布，再看 pod 与健康检查，最后按 runbook 定位根因。
---

# gateway-ops

## 何时使用
网关接口返回 5xx，且不是由发布或配置变更引起。

## 步骤

1. 先确认最近 30 分钟没有发布
2. 看 pod 和健康检查：

\`\`\`bash
kubectl get pods -n gateway
curl -fsS "$GW/healthz"  # 期望 200
\`\`\`

3. 按 \`references/runbook.md\` 定位根因

## 完成标准

健康检查恢复 **200**。
`

const SCRIPT = `#!/usr/bin/env bash
# 网关健康检查：返回 200 才算恢复
set -euo pipefail

curl -fsS "$GW/healthz"
kubectl get pods -n gateway
`

const YAML = `gateway:
  http_addr: 127.0.0.1:6060
  auth:
    mode: token
    token: \${FOREBRAIN_GATEWAY_TOKEN}
approval_policy: on-request
sandbox_mode: workspace-write
agents:
  definitions:
    main:
      primary: true
      llm_providers:
        - provider: deepseek
          model: deepseek-v4
          api_key: \${DEEPSEEK_API_KEY}
`

const STATES: Array<{ key: string; file: string; content: string; source?: boolean }> = [
  { key: 'ruleedit', file: 'zz-AGENTS.md', content: AGENTS },
  { key: 'rulesrc', file: 'zz-AGENTS.md', content: AGENTS, source: true },
  { key: 'projrule', file: 'zz-FOREBRAIN.md', content: FOREBRAIN },
  { key: 'wsfile', file: 'zz-SKILL.md', content: SKILL },
  { key: 'wsscript', file: 'zz-check-health.sh', content: SCRIPT },
  { key: 'cfgedit', file: 'zz-forebrain.yaml', content: YAML },
]

for (const state of STATES) {
  test(`preview parity: ${state.key}`, async ({ page }) => {
    test.skip(!globalRoot, 'E2E_HOME missing')
    await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
    seed(state.file, state.content)
    await signIn(page)
    await page.goto('/memories', { waitUntil: 'networkidle' })
    await page.click(`[data-testid="memories-edit-${state.file}"]`)
    const editor = page.locator('[data-testid="memories-editor"]')
    await expect(editor).toBeVisible()
    if (state.source) {
      await expect(editor.locator('.ProseMirror')).toBeVisible({ timeout: 15_000 })
      await editor.locator('[data-testid="editor-mode-src"]').click()
      await expect(editor.locator('.cm-content')).toBeVisible({ timeout: 15_000 })
    } else if (state.file.endsWith('.md')) {
      await expect(editor.locator('.ProseMirror')).toBeVisible({ timeout: 15_000 })
    } else {
      await expect(editor.locator('.cm-content')).toBeVisible({ timeout: 15_000 })
    }
    await shot(page, `fep-${state.key}`)

    // The same measurements the prototype was probed with, so the two frames
    // can be compared number for number.
    const info = await page.evaluate(() => {
      const mde = document.querySelector('.mde')
      if (!mde) return { none: true }
      const mount = mde.querySelector('.mde-mount')
      const foot = mde.querySelector('.mde-foot')
      const seg = mde.querySelector('.mde-seg')
      const props = mde.querySelector('.mde-props')
      const cm = mde.querySelector('.cm-editor')
      const pm = mde.querySelector('.ProseMirror')
      const cs = (el: Element | null) => (el ? getComputedStyle(el) : null)
      return {
        props: props ? (props as HTMLElement).innerText.replace(/\n/g, ' | ') : null,
        footText: foot ? (foot as HTMLElement).innerText.replace(/\n/g, ' | ') : null,
        segButtons: seg ? Array.from(seg.querySelectorAll('button')).map((b) => `${b.textContent}[${b.getAttribute('aria-pressed')}]`) : null,
        mountBox: mount ? { max: cs(mount)!.maxHeight, min: cs(mount)!.minHeight } : null,
        cmFont: cm ? cs(cm)!.fontSize : null,
        cmColor: cm ? cs(cm)!.color : null,
        cmBg: cm ? cs(cm)!.backgroundColor : null,
        pmPadding: pm ? cs(pm)!.padding : null,
        hasTopBar: Boolean(mde.querySelector('.milkdown-top-bar')),
        toolbarButtons: mde.querySelectorAll('.milkdown-top-bar .top-bar-item').length,
        codeLangButton: Boolean(mde.querySelector('.milkdown-code-block .language-button')),
      }
    })
    console.log(`PARITY ${state.key} ${JSON.stringify(info)}`)
  })
}
