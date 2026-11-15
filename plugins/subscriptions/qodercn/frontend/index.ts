import type { SubscriptionFrontendPlugin } from '../../../../frontend/src/plugins/subscriptionPlugin'

const qodercnPlugin: SubscriptionFrontendPlugin = {
  id: 'qodercn',
  describe: (locale) =>
    locale === 'zh'
      ? '打开浏览器授权登录 Qoder CN 订阅账号，自动同步可用模型'
      : 'Authorize your Qoder CN subscription in the browser',
  waitingHint: (locale) =>
    locale === 'zh'
      ? '点击下方按钮在浏览器中确认登录 Qoder CN，授权完成后将自动跳转。'
      : 'Click the button below to confirm sign-in in your browser. This page will advance automatically.',
  hideEmptyUserCode: true,
}

export default qodercnPlugin
