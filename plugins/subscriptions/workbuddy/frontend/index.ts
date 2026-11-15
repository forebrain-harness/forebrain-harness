import type { SubscriptionFrontendPlugin } from '../../../../frontend/src/plugins/subscriptionPlugin'

const workbuddyPlugin: SubscriptionFrontendPlugin = {
  id: 'workbuddy',
  describe: (locale) =>
    locale === 'zh'
      ? '打开浏览器登录 WorkBuddy 订阅账号，自动同步可用模型'
      : 'Sign in to your WorkBuddy subscription in the browser',
  waitingHint: (locale) =>
    locale === 'zh'
      ? '已在浏览器打开 WorkBuddy 登录页，完成后这里会自动继续。'
      : 'Finish signing in to WorkBuddy in the browser; this page advances by itself.',
  hideEmptyUserCode: true,
}

export default workbuddyPlugin
