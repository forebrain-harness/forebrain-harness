import { describe, expect, it } from 'vitest'

import labels from './channelLabels.json'
import { channelLabel } from './channelLabels'
import { setLocale } from '@/locales'

/** Every channel kind the engine builds, as the page names them. */
const kinds = Object.keys(labels)

describe('channelLabel', () => {
  it('names every channel kind in both languages', () => {
    expect(kinds).toHaveLength(18)
    for (const kind of kinds) {
      expect(labels[kind as keyof typeof labels]).toHaveLength(2)
      for (const name of labels[kind as keyof typeof labels]) {
        expect(name.trim()).not.toBe('')
      }
    }
  })

  it('draws the active language', () => {
    setLocale('zh')
    expect(channelLabel('feishu')).toBe('飞书')
    expect(channelLabel('wecom_callback')).toBe('企业微信（回调）')
    setLocale('en')
    expect(channelLabel('feishu')).toBe('Feishu')
    expect(channelLabel('wecom_callback')).toBe('WeCom callback')
  })

  it('draws a kind the table does not name as itself', () => {
    setLocale('zh')
    expect(channelLabel('not-a-kind')).toBe('not-a-kind')
    setLocale('en')
    expect(channelLabel('not-a-kind')).toBe('not-a-kind')
  })
})
