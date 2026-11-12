import type { Component } from 'vue'
import type { SubscriptionRow } from '@/lib/api'
import { localSubscriptionPlugins } from './local_subscriptions.gen'

export interface SubscriptionFrontendPlugin {
  /** Must match backend Subscription.ID() (e.g. 'qodercn', 'zcode', 'workbuddy'). */
  id: string
  /** Optional subtitle shown under the subscription name in Step 1. */
  describe?: (locale: 'zh' | 'en', row: SubscriptionRow) => string
  /** Optional hint shown in Step 2 while waiting for browser authorization. */
  waitingHint?: (locale: 'zh' | 'en') => string
  /** When true, hides the 8-char user_code box if userCode is empty (browser-confirm device flow). */
  hideEmptyUserCode?: boolean
  /** Optional custom Vue component for plugin-specific login steps. */
  customStepComponent?: Component
}

const registry = new Map<string, SubscriptionFrontendPlugin>()
for (const plugin of localSubscriptionPlugins) {
  registry.set(plugin.id.trim().toLowerCase(), plugin)
}

export function getSubscriptionFrontendPlugin(id: string): SubscriptionFrontendPlugin | undefined {
  return registry.get(id.trim().toLowerCase())
}
