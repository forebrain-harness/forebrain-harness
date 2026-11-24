const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { createRequire } = require('node:module');
const root = path.resolve(__dirname, '..');
const { chromium } = createRequire(path.join(root, 'frontend/package.json'))('@playwright/test');
const html = fs.readFileSync(path.join(__dirname, '374-account-web-prototype.html'), 'utf8');
new vm.Script(html.match(/<script>([\s\S]*)<\/script>/)[1]);
assert(/connect-src 'none'/.test(html), 'Prototype must stay zero-network');

(async () => {
  const browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 1480, height: 1100 } });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const results = [];
  const check = async (name, fn) => { await fn(); results.push(name); };
  const scene = async id => {
    await page.evaluate(id => window.accountWebPrototype.scene(id), id);
    await page.evaluate(() => new Promise(requestAnimationFrame));
  };
  const snap = () => page.evaluate(() => window.accountWebPrototype.snapshot());
  const click = async selector => {
    await page.locator(selector).first().click();
    await page.evaluate(() => new Promise(requestAnimationFrame));
  };
  const text = selector => page.locator(selector).first().textContent();
  try {
    await page.goto('http://127.0.0.1:8765/374-account-web-prototype.html');
    await check('Every scene renders clean and never shows a full AccessKeyID', async () => {
      const ids = await page.evaluate(() => [...document.querySelectorAll('#scenes [data-scene]')].map(b => b.dataset.scene));
      assert.equal(ids.length, 9);
      for (const id of ids) {
        await scene(id);
        assert((await page.locator('#app').textContent()).trim(), `${id}: empty app`);
        const body = await page.locator('body').textContent();
        assert(!/AKLT[A-Za-z0-9]{8,}/.test(body), `${id}: full AccessKeyID rendered`);
      }
    });
    await check('Table names the account under 账号登录; key rows keep the mask', async () => {
      await scene('table');
      const names = await page.locator('[data-testid="models-cell-account-name"]').allTextContents();
      assert.deepEqual(names, ['doudou@example.test', 'Wang Bin']);
      assert.match(await text('[data-testid="models-cell-key"]'), /••••d410/);
      const s = await snap();
      assert.equal(s.rows.length, 3);
      assert.equal(s.rows[0].source_state, 'usable');
    });
    await check('Step 1 replaces the flow line with the account; usable goes straight to step 3', async () => {
      await scene('step1');
      assert.match(await text('[data-testid="models-sub-account-chatgpt"]'), /doudou@example\.test/);
      assert.match(await text('[data-testid="models-sub-account-chatgpt"]'), /plus/);
      assert.match(await text('[data-testid="models-sub-account-volcengine"]'), /AKLT…f3a9/);
      await click('[data-subscription="chatgpt"]');
      const s = await snap();
      assert.equal(s.modal.step, 3);
      assert.equal(s.modal.id, 'chatgpt');
    });
    await check('Step 3 account bar offers logout and switch; logout asks first', async () => {
      await scene('step3');
      assert.match(await text('[data-testid="models-login-account"]'), /doudou@example\.test/);
      assert(await page.locator('[data-testid="models-logout"]').isVisible());
      assert(await page.locator('[data-testid="models-switch-account"]').isVisible());
      await click('[data-testid="models-logout"]');
      assert((await snap()).confirm, 'confirm dialog opens');
      assert.match(await text('[role="alertdialog"]'), /已添加的模型会保留，但在重新登录前无法调用/);
      await click('.dlg-f [data-act="confirm-cancel"]');
      let s = await snap();
      assert.equal(s.confirm, null);
      assert.equal(s.modal.step, 3);
      assert(s.signed.includes('chatgpt'), 'cancelling removes nothing');
      await click('[data-testid="models-logout"]');
      await click('[data-testid="confirm-ok"]');
      s = await snap();
      assert.equal(s.modal.step, 1, 'success returns to step 1');
      assert.equal(s.states.chatgpt, 'missing');
      assert.equal(s.rows.length, 3, 'model rows are kept');
      assert.equal(s.rows[0].source_state, 'missing');
      assert.equal(await page.locator('[data-testid="models-cell-account-name"]').count(), 1, 'chatgpt account name is gone from the table');
    });
    await check('Switching account forces the flow and replaces the login', async () => {
      await scene('step3');
      await click('[data-testid="models-switch-account"]');
      let s = await snap();
      assert.equal(s.modal.step, 2);
      assert.equal(s.modal.switching, true);
      await click('[data-testid="models-login-complete"]');
      s = await snap();
      assert.equal(s.modal.step, 3);
      assert.equal(s.accounts.chatgpt.name, 'new.account@example.test');
      assert.match(await text('[data-testid="models-login-account"]'), /new\.account@example\.test/);
    });
    await check('Provision adds the chosen offers as rows', async () => {
      await scene('step1');
      await click('[data-subscription="chatgpt"]');
      await click('input[data-act="offer"][data-v="0:1"]');
      let s = await snap();
      assert.deepEqual(s.modal.chosen, ['0:1']);
      await click('[data-testid="models-provision"]');
      s = await snap();
      assert.equal(s.modal, null);
      assert.equal(s.rows.length, 4);
      assert(s.rows.some(r => r.model === 'gpt-5.5-mini'));
    });
    await check('Environment key: no logout, no switch, an honest hint', async () => {
      await scene('envkey');
      assert.match(await text('[data-testid="models-login-account"]'), /OPENCODE_API_KEY/);
      assert.match(await text('[data-testid="models-login-account"]'), /环境变量提供/);
      assert.equal(await page.locator('[data-testid="models-logout"]').count(), 0);
      assert.equal(await page.locator('[data-testid="models-switch-account"]').count(), 0);
      assert.match(await text('.envhint'), /请在启动 Forebrain Harness 的终端里 unset 后重启/);
      assert.equal((await snap()).canLogout['opencode-go'], false);
      await click('[data-act="sub-back"]');
      assert.match(await text('[data-testid="models-sub-account-opencode-go"]'), /环境变量提供/);
    });
    await check('External source: 409 notice, re-imported account, no signed-out claim', async () => {
      await scene('external');
      assert.match(await text('[data-testid="models-logout-external"]'), /本机保存的登录已删除，但环境变量或官方客户端仍会让这台机器重新登录/);
      assert.match(await text('[data-testid="models-login-account"]'), /adopted@official\.example/);
      await click('[data-testid="models-logout"]');
      await click('[data-testid="confirm-ok"]');
      const s = await snap();
      assert.equal(s.modal.step, 3, '409 stays on the account step');
      assert.equal(s.modal.notice, 'external');
      assert.equal(s.accounts.qodercn.name, 'adopted@official.example');
    });
    await check('Drawer source card names the account and logs out in place', async () => {
      await scene('drawer');
      assert.match(await text('[data-testid="models-source-account"]'), /doudou@example\.test/);
      assert.match(await text('[data-testid="models-source-account"]'), /plus/);
      assert(await page.locator('[data-testid="models-source-logout"]').isVisible());
      await click('[data-testid="models-source-logout"]');
      await click('[data-testid="confirm-ok"]');
      const s = await snap();
      assert(s.drawer, 'drawer stays open after logout');
      assert.equal(s.states.chatgpt, 'missing');
      assert.equal(s.rows.length, 3);
      assert.equal(await page.locator('[data-testid="models-source-logout"]').count(), 0);
      assert.match(await text('[data-testid="models-source-card"]'), /未登录/);
      assert(await page.locator('[data-testid="models-relogin"]').isVisible());
    });
    await check('After sign-out the row stands with a missing source', async () => {
      await scene('after');
      const s = await snap();
      assert.equal(s.rows.length, 3);
      assert.equal(s.rows[0].source_state, 'missing');
      assert.equal(s.rows[0].source_account, null);
      assert.match(await text('[data-testid="models-source-card"]'), /未登录/);
      assert.equal(await page.locator('[data-testid="models-cell-account-name"]').count(), 1);
    });
    await check('Shared credential file: the confirm names the path and the other app', async () => {
      await scene('shared');
      const dialog = await text('[role="alertdialog"]');
      assert.match(dialog, /~\/\.codex\/auth\.json/, 'the confirm names the shared file');
      assert.match(dialog, /退出会同时让 Codex CLI 退出登录/);
      await click('[data-testid="confirm-ok"]');
      const s = await snap();
      assert.equal(s.states.chatgpt, 'missing');
      assert.equal(s.rows.length, 3, 'model rows are kept');
    });
    await check('Escape unwinds confirm, modal and drawer without writing', async () => {
      await scene('confirm');
      await page.keyboard.press('Escape');
      let s = await snap();
      assert.equal(s.confirm, null);
      assert.equal(s.modal.step, 3);
      await page.keyboard.press('Escape');
      s = await snap();
      assert.equal(s.modal, null);
      assert(s.signed.includes('chatgpt'));
      await scene('drawer');
      await page.keyboard.press('Escape');
      assert.equal((await snap()).drawer, null);
    });
    for (const id of ['table', 'step1', 'step3', 'confirm', 'after', 'drawer', 'envkey', 'external', 'shared']) {
      await scene(id);
      await page.screenshot({ path: path.join(__dirname, `374-shot-web-${id}.png`), fullPage: true });
    }
    assert.deepEqual(errors, [], 'Browser console errors');
    console.log(JSON.stringify({ passed: results.length, results, browserErrors: errors }, null, 2));
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });