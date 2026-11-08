import { beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import PeerSendCard from './PeerSendCard.vue'

/**
 * The card a sent peer message leaves: the message with its own line breaks,
 * and the delivery in one sentence on the recipient's identity block — the
 * same facts the terminal's card reads, in the reader's language.
 */
describe('PeerSendCard', () => {
  beforeAll(() => setLocale('zh'))

  it('shows the message and what a busy recipient will do with it', () => {
    const wrapper = mount(PeerSendCard, {
      props: {
        send: {
          message: '还在改 open.go 第 146 行附近的 Close()\n大约 10 分钟',
          status: 'delivered',
          to: 'proj-ff',
          peerStatus: 'busy',
          mark: 'ff',
          tone: 3,
        },
      },
    })
    const message = wrapper.find('.message')
    expect(message.text()).toBe('还在改 open.go 第 146 行附近的 Close()\n大约 10 分钟')
    expect(message.attributes('style')).toBeUndefined()
    const foot = wrapper.find('[data-testid="peer-send-foot"]')
    expect(foot.find('[data-testid="peer-mark"]').text()).toBe('ff')
    expect(foot.find('.to').text()).toBe('proj-ff')
    expect(foot.find('.note').text()).toBe('对方正忙，会在下一步读到')
  })

  it('words each delivery the engine can report', () => {
    const cases: [Record<string, string>, string][] = [
      [{ status: 'delivered', to: 'a', peerStatus: 'idle' }, '对方空闲，已开始新一轮'],
      [{ status: 'delivered', to: 'a' }, '已送达'],
      [{ status: 'queued', to: 'a' }, '已排队，对方会在下一步读到'],
      [{ status: 'idle' }, '对方空闲，不收消息'],
    ]
    for (const [send, want] of cases) {
      const wrapper = mount(PeerSendCard, { props: { send } })
      expect(wrapper.find('.note').text()).toBe(want)
    }
  })

  it('draws no footnote for a status this build does not know', () => {
    const wrapper = mount(PeerSendCard, { props: { send: { message: 'hello', status: 'teleported' } } })
    expect(wrapper.find('.message').text()).toBe('hello')
    expect(wrapper.find('[data-testid="peer-send-foot"]').exists()).toBe(false)
  })
})
