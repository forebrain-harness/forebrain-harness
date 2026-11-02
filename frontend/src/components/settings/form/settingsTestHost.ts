/**
 * A host for the schema-driven settings tabs' tests (plan 191 §5): it builds the
 * page's form the way SettingsView does — one read of GET /api/settings, then
 * the tab the test wants — so a tab's test exercises the same wiring the app
 * uses instead of a hand-made stand-in.
 */
import { defineComponent, onMounted } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'

import { useSettingsForm } from '@/components/settings/form/useSettingsForm'

export interface SettingsHostOptions {
  /** The tab's own key, which decides the groups it draws. */
  tab: string
  /** The module's component under test. */
  component: unknown
  /** The models a 模型 row offers. */
  models?: string[]
}

/** Mounts one schema-driven tab with a real form behind it. */
export async function mountSettingsTab(options: SettingsHostOptions): Promise<VueWrapper> {
  const Host = defineComponent({
    components: { UnderTest: options.component as never },
    setup() {
      const form = useSettingsForm()
      onMounted(() => void form.load())
      return { form }
    },
    template: `<UnderTest :tab="'${options.tab}'" :form="form" :models="${JSON.stringify(options.models ?? [])}" />`,
  })
  const wrapper = mount(Host)
  // The read is a promise; one flush lands its answer in the form.
  await new Promise((resolve) => setTimeout(resolve, 0))
  await wrapper.vm.$nextTick()
  return wrapper
}
