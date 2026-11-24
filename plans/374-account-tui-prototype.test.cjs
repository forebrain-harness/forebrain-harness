const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { createRequire } = require('node:module');
const root = path.resolve(__dirname, '..');
const { chromium } = createRequire(path.join(root, 'frontend/package.json'))('@playwright/test');
const html = fs.readFileSync(path.join(__dirname, '374-account-tui-prototype.html'), 'utf8');
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
    await page.evaluate(id => window.accountTuiPrototype.scene(id), id);
    await page.evaluate(() => new Promise(requestAnimationFrame));
  };
  const snap = () => page.evaluate(() => window.accountTuiPrototype.snapshot());
  const clickOption = async label => {
    const s = await snap();
    const i = s.options.findIndex(o => o.label === label);
    assert(i >= 0, `Option not visible: ${label} (page ${s.page})`);
    await page.locator(`#grid [data-hit="${i}"]`).first().click();
    await page.evaluate(() => new Promise(requestAnimationFrame));
  };
  const settle = () => page.waitForTimeout(700);
  const gridText = async () => (await page.locator('#grid').textContent()).replace(/\s+/g, ' ');
  try {
    await page.goto('http://127.0.0.1:8765/374-account-tui-prototype.html');
    await check('Every scene renders clean at 120, 88 and 68 columns', async () => {
      const failures = await page.evaluate(() => {
        const failures = [];
        const ids = [...document.querySelectorAll('#nav [data-scene]')].map(b => b.dataset.scene);
        for (const width of [120, 88, 68]) {
          document.querySelector(`[data-width="${width}"]`).click();
          for (const id of ids) {
            window.accountTuiPrototype.scene(id);
            const grid = document.querySelector('#grid');
            if (!grid.textContent.trim()) failures.push(`${width}/${id}: empty grid`);
            if (/[\u4e00-\u9fff]/.test(grid.textContent)) failures.push(`${width}/${id}: CJK inside terminal`);
            for (const row of grid.querySelectorAll('.row')) {
              if (row.scrollWidth > grid.clientWidth + 1) { failures.push(`${width}/${id}: row overflow`); break; }
            }
          }
        }
        document.querySelector('[data-width="120"]').click();
        window.accountTuiPrototype.scene('start');
        return failures;
      });
      assert.deepEqual(failures, []);
    });
    await check('Provider rows name the account; no redundant Signed in status', async () => {
      await scene('start');
      const s = await snap();
      const chatgpt = s.options.find(o => o.value === 'subscription:chatgpt');
      assert.match(chatgpt.desc, /doudou@example\.test \(plus\)/);
      assert.equal(chatgpt.status, '', 'the account name is the sign-in signal; Signed in is redundant');
      const volc = s.options.find(o => o.value === 'subscription:volcengine');
      assert.match(volc.desc, /AKLT…f3a9 \(STS\)/);
      assert(!/AKLT[A-Za-z0-9]{8,}/.test(await gridText()), 'Full AccessKeyID must never render');
      assert(!s.options.some(o => o.status === 'Signed in'), 'no subscription row carries a Signed in status');
      await scene('dead');
      const dead = (await snap()).options.find(o => o.value === 'subscription:chatgpt');
      assert.match(dead.desc, /doudou@example\.test/, 'dead row still names the stored account');
      assert.equal(dead.status, '', 'dead row has no status either; state resolves on entry');
    });
    await check('Account page offers continue / switch / sign out; Esc returns to the list', async () => {
      await scene('account');
      const s = await snap();
      assert.equal(s.page, 'account');
      assert.deepEqual(s.options.map(o => o.label), ['Continue with this account', 'Sign in with another account', 'Sign out']);
      assert.equal(s.options[2].danger, true);
      assert.match(await gridText(), /Signed in as doudou@example\.test \(plus\)\./);
      await page.locator('#terminal').focus();
      await page.keyboard.press('Escape');
      assert.equal((await snap()).page, 'provider', 'Esc goes back to the provider list, not out of the wizard');
    });
    await check('Sign out asks first; No keeps the login; Yes removes it and keeps the rows', async () => {
      await scene('account');
      await clickOption('Sign out');
      let s = await snap();
      assert.equal(s.page, 'signout-confirm');
      assert.match(await gridText(), /The models it added stay, but cannot call until you sign in again\./);
      await clickOption('No, go back');
      s = await snap();
      assert.equal(s.page, 'account');
      assert(s.signed.includes('chatgpt'), 'declining removes nothing');
      await clickOption('Sign out');
      await clickOption('Yes, sign out');
      s = await snap();
      assert.equal(s.page, 'provider', 'sign-out returns to the provider list');
      assert(!s.signed.includes('chatgpt'));
      assert.equal(s.rows.length, 2, 'model rows are kept');
      const row = s.rows.find(r => r.source === 'chatgpt');
      assert.equal(row.source_state, 'missing');
      assert(s.events.some(e => /Signed out of ChatGPT/.test(e.text)));
    });
    await check('Continue walks the 311 page order to a saved row', async () => {
      await scene('continue');
      await clickOption('Continue with this account');
      assert.equal((await snap()).page, 'models');
      await clickOption('GPT-5.5');
      assert.equal((await snap()).page, 'effort');
      await clickOption('Medium');
      await clickOption('Yes, the agent may use it');
      assert.equal((await snap()).page, 'review');
      await clickOption('Save');
      await settle();
      const s = await snap();
      assert.equal(s.page, 'success');
      assert.equal(s.phase, 'saved');
    });
    await check('Switching account runs the sign-in again and replaces the login', async () => {
      await scene('switch');
      assert.equal((await snap()).page, 'authorizing');
      await page.locator('#terminal').focus();
      await page.keyboard.press('Enter');
      const s = await snap();
      assert.equal(s.page, 'models');
      assert.equal(s.account.name, 'new.account@example.test');
      assert(s.events.some(e => /Switched account/.test(e.text)));
    });
    await check('Cancelling a switch stays on the account page', async () => {
      await scene('account');
      await clickOption('Sign in with another account');
      assert.equal((await snap()).page, 'signin');
      await page.locator('#terminal').focus();
      await page.keyboard.press('Escape');
      const s = await snap();
      assert.equal(s.page, 'account');
      assert.equal(s.account.name, 'doudou@example.test', 'the old login is untouched');
    });
    await check('Environment key offers no sign out and says where it comes from', async () => {
      await scene('envkey');
      const s = await snap();
      assert.equal(s.page, 'account');
      assert.deepEqual(s.options.map(o => o.label), ['Continue with this account', 'Sign in with an account']);
      assert(!s.options.some(o => o.label === 'Sign out'));
      const text = await gridText();
      assert.match(text, /Signed in with OPENCODE_API_KEY from the environment\./);
      assert.match(text, /Unset it in the shell that starts Forebrain Harness, then restart\./);
    });
    await check('Volcengine shows the masked STS id only', async () => {
      await scene('volcengine');
      const text = await gridText();
      assert.match(text, /Signed in as AKLT…f3a9 \(STS\)\./);
      assert(!/AKLT[A-Za-z0-9]{8,}/.test(text));
    });
    await check('External source: stored login removed, honest notice, wizard stays', async () => {
      await scene('external');
      await clickOption('Sign out');
      await clickOption('Yes, sign out');
      const s = await snap();
      assert.equal(s.page, 'account', 'an external source does not claim a signed-out state');
      assert.match(s.notice, /The saved login was removed\. An external source provides this sign-in/);
      assert.equal(s.account.name, 'adopted@official.example');
      assert(s.events.some(e => /re-imported/.test(e.text)));
    });
    await check('Dead login opens the account page: sign in again, or sign out', async () => {
      await scene('dead-account');
      let s = await snap();
      assert.equal(s.page, 'account', 'a dead login reaches the account page, not the sign-in page');
      assert.deepEqual(s.options.map(o => o.label), ['Sign in again', 'Sign out']);
      assert.equal(s.options[1].danger, true);
      assert.match(await gridText(), /This sign-in no longer works; sign in again or sign out\./);
      await clickOption('Sign out');
      await clickOption('Yes, sign out');
      s = await snap();
      assert.equal(s.page, 'provider', 'a dead login can be removed without signing in again');
      assert(!s.signed.includes('chatgpt'));
      assert.equal(s.rows.length, 2, 'model rows are kept');
      await scene('dead-account');
      await clickOption('Sign in again');
      assert.equal((await snap()).page, 'signin', 'Sign in again reuses the existing flow');
      await clickOption('Continue with browser sign-in');
      assert.equal((await snap()).page, 'authorizing');
      await page.locator('#terminal').focus();
      await page.keyboard.press('Enter');
      s = await snap();
      assert.equal(s.page, 'models');
      assert.equal(s.state, 'usable', 'signing in again clears the dead marker');
    });
    await check('Environment key row shows the variable name alone', async () => {
      await scene('envkey');
      await page.locator('#terminal').focus();
      await page.keyboard.press('Escape');
      const row = (await snap()).options.find(o => o.value === 'subscription:opencode-go');
      assert.equal(row.desc, 'OPENCODE_API_KEY', 'no (env) suffix: the variable name is the whole signal');
    });
    await check('Shared credential file: the confirm names the path and the other app', async () => {
      await scene('shared');
      const text = await gridText();
      assert.match(text, /~\/\.codex\/auth\.json/, 'the confirm names the shared file');
      assert.match(text, /Codex CLI is signed out too/);
    });
    await check('Unreadable identity degrades without blocking', async () => {
      await scene('no-account');
      const s = await snap();
      assert.equal(s.page, 'account');
      assert.match(await gridText(), /This sign-in stands; no account name is stored\./);
      assert.deepEqual(s.options.map(o => o.label), ['Continue with this account', 'Sign in with another account', 'Sign out']);
    });
    await page.locator('[data-width="120"]').click();
    for (const [id, file] of [
      ['start', '374-shot-tui-start.png'],
      ['account', '374-shot-tui-account.png'],
      ['signout', '374-shot-tui-signout.png'],
      ['after-signout', '374-shot-tui-after.png'],
      ['envkey', '374-shot-tui-envkey.png'],
      ['dead-account', '374-shot-tui-dead.png'],
      ['shared', '374-shot-tui-shared.png'],
      ['external', '374-shot-tui-external.png'],
    ]) {
      await scene(id);
      await page.screenshot({ path: path.join(__dirname, file), fullPage: true });
    }
    assert.deepEqual(errors, [], 'Browser console errors');
    console.log(JSON.stringify({ passed: results.length, results, browserErrors: errors }, null, 2));
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });