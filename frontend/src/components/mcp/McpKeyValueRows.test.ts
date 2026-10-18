import { beforeAll, describe, expect, it } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'

import tip from '@/directives/tip'
import { setLocale } from '@/locales'

import McpKeyValueRows from './McpKeyValueRows.vue'
import { mcpKeyValuePayload, MCP_REDACTED, type McpKeyValueRow } from './mcpRows'

/**
 * One MCP entry's key/value map (plan 074): a row per pair, a delete button per
 * row, one add button under them — and, for a viewer, none of the two buttons.
 * The component only reports what the table now holds; what that means for a
 * save is `mcpKeyValuePayload`'s decision, so the last test walks the two
 * together.
 */
function mountRows(props: { modelValue?: McpKeyValueRow[]; addLabel?: string; readonly?: boolean } = {}): VueWrapper {
  return mount(McpKeyValueRows, {
    props: { modelValue: [], addLabel: '添加环境变量', ...props },
    global: { directives: { tip } },
  })
}

/** The pairs the component last reported. */
function reported(wrapper: VueWrapper): McpKeyValueRow[] {
  const events = wrapper.emitted('update:modelValue') ?? []
  return events[events.length - 1][0] as McpKeyValueRow[]
}

describe('McpKeyValueRows', () => {
  beforeAll(() => setLocale('zh'))

  it('adds a blank pair, edits one field at a time and removes a row', async () => {
    const wrapper = mountRows({ modelValue: [{ key: 'A', value: '1' }] })

    expect(wrapper.find('[data-kv-add]').text()).toBe('添加环境变量')
    await wrapper.find('[data-kv-add]').trigger('click')
    expect(reported(wrapper)).toEqual([
      { key: 'A', value: '1' },
      { key: '', value: '' },
    ])

    const two = mountRows({ modelValue: [{ key: 'A', value: '1' }, { key: '', value: '' }] })
    await two.findAll('[data-kv-key]')[1].setValue('B')
    expect(reported(two)).toEqual([
      { key: 'A', value: '1' },
      { key: 'B', value: '' },
    ])
    await two.findAll('[data-kv-value]')[0].setValue('9')
    expect(reported(two)).toEqual([
      { key: 'A', value: '9' },
      { key: '', value: '' },
    ])

    await two.findAll('[data-kv-remove]')[0].trigger('click')
    expect(reported(two)).toEqual([{ key: '', value: '' }])
  })

  it('shows a stored secret as the placeholder a save sends back, and a removal as an empty value', async () => {
    const stored = { TOKEN: MCP_REDACTED, GONE: 'x' }
    const asRead: McpKeyValueRow[] = [
      { key: 'TOKEN', value: MCP_REDACTED },
      { key: 'GONE', value: 'x' },
    ]
    const wrapper = mountRows({ modelValue: asRead })

    // The value the form holds is the placeholder itself: nothing else about
    // the stored secret is knowable, and the payload reads it back as "keep".
    expect(wrapper.findAll('[data-kv-value]')[0].element).toHaveProperty('value', MCP_REDACTED)
    expect(mcpKeyValuePayload(stored, asRead)).toBeNull()

    await wrapper.findAll('[data-kv-remove]')[1].trigger('click')
    const rows = reported(wrapper)
    expect(rows).toEqual([{ key: 'TOKEN', value: MCP_REDACTED }])
    expect(mcpKeyValuePayload(stored, rows)).toEqual({ TOKEN: MCP_REDACTED, GONE: '' })
  })

  it('draws a viewer without the add and delete buttons, and says 未设置 for an empty map', () => {
    const map = mountRows({ modelValue: [{ key: 'A', value: '1' }], readonly: true })
    expect(map.find('[data-kv-add]').exists()).toBe(false)
    expect(map.find('[data-kv-remove]').exists()).toBe(false)
    const fields = map.findAll('input')
    expect(fields).toHaveLength(2)
    expect(fields.every((field) => field.attributes('readonly') !== undefined)).toBe(true)
    // Read-only still shows the pair, as text in a field that cannot be typed in.
    expect(fields[0].element).toHaveProperty('value', 'A')

    const empty = mountRows({ modelValue: [], readonly: true })
    expect(empty.find('[data-kv-empty]').text()).toBe('未设置')
    expect(empty.findAll('input')).toHaveLength(0)

    // An empty map being edited offers the add button instead of the note.
    const editable = mountRows({ modelValue: [] })
    expect(editable.find('[data-kv-empty]').exists()).toBe(false)
    expect(editable.find('[data-kv-add]').exists()).toBe(true)
  })
})
