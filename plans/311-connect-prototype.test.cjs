const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { createRequire } = require('node:module');
const root = path.resolve(__dirname, '..');
const { chromium } = createRequire(path.join(root, 'frontend/package.json'))('@playwright/test');
const html = fs.readFileSync(path.join(__dirname, '311-connect-prototype.html'), 'utf8');
new vm.Script(html.match(/<script>([\s\S]*)<\/script>/)[1]);
assert(!/sa-model|sa-effort|PINS|SA_TYPES/.test(html), 'Global subagent flow must not live in /connect');

(async () => {
  const browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 1480, height: 1100 } });
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const results = [];
  const check = async (name, fn) => { await fn(); results.push(name); };
  const scene = async id => {
    await page.evaluate(id => window.connectPrototype.scene(id), id);
    await page.evaluate(() => new Promise(requestAnimationFrame));
  };
  const snap = () => page.evaluate(() => window.connectPrototype.snapshot());
  const clickOption = async label => {
    const s = await snap();
    const i = s.options.findIndex(o => o.label === label);
    assert(i >= 0, `Option not visible: ${label} (page ${s.page})`);
    await page.locator(`#grid [data-hit="${i}"]`).first().click();
    await page.evaluate(() => new Promise(requestAnimationFrame));
  };
  const settle = () => page.waitForTimeout(700);
  try {
    await page.goto('http://127.0.0.1:8765/311-connect-prototype.html');
    await check('Every scene renders clean at 120, 88 and 68 columns', async () => {
      const failures = await page.evaluate(() => {
        const failures = [];
        const ids = [...document.querySelectorAll('#nav [data-scene]')].map(b => b.dataset.scene);
        for (const width of [120, 88, 68]) {
          document.querySelector(`[data-width="${width}"]`).click();
          for (const id of ids) {
            window.connectPrototype.scene(id);
            const grid = document.querySelector('#grid');
            if (!grid.textContent.trim()) failures.push(`${width}/${id}: empty grid`);
            if (/[\u4e00-\u9fff]/.test(grid.textContent)) failures.push(`${width}/${id}: CJK inside terminal`);
            for (const row of grid.querySelectorAll('.row')) {
              if (row.scrollWidth > grid.clientWidth + 1) { failures.push(`${width}/${id}: row overflow`); break; }
            }
          }
        }
        document.querySelector('[data-width="120"]').click();
        window.connectPrototype.scene('start');
        return failures;
      });
      assert.deepEqual(failures, []);
    });
    await check('Subscription flow saves the row default and the session effort', async () => {
      await scene('effort');
      assert.equal((await snap()).page, 'effort');
      await clickOption('High');
      assert.equal((await snap()).page, 'subagents');
      await clickOption('Yes, the agent may use it');
      assert.equal((await snap()).page, 'review');
      await clickOption('Save');
      await settle();
      const s = await snap();
      assert.equal(s.page, 'success');
      assert.equal(s.rows.length, 1);
      assert.equal(s.rows[0].cap_default_effort, 'high');
      assert.equal(s.session.effort, 'high');
    });
    await check('Off saves none; Anthropic Messages never offers a fake Off', async () => {
      await scene('off');
      assert((await snap()).options.some(o => o.label === 'Off'));
      await clickOption('Off');
      await clickOption('No, only when you pick it');
      await clickOption('Save');
      await settle();
      assert.equal((await snap()).rows[0].cap_default_effort, 'none');
      await scene('anthropic-off');
      assert(!(await snap()).options.some(o => o.label === 'Off'));
    });
    await check('Reconnect echoes the effective default and clears old overrides', async () => {
      await scene('reconnect');
      const s = await snap();
      assert.equal(s.selectedLabel, 'High');
      await clickOption('Provider default');
      await clickOption('Yes, the agent may use it');
      await clickOption('Save');
      await settle();
      const row = (await snap()).rows[0];
      assert.equal(row.cap_default_effort, '');
      assert.deepEqual(row.params.reasoning, { summary: 'auto' });
      assert.equal(row.params.temperature, 0.2);
    });
    await check('Web mock shares the sendability rule and save payload', async () => {
      await scene('web-align');
      assert(await page.locator('#webmock').isVisible());
      const labels = () => page.locator('#wm-effort option').allTextContents();
      assert((await labels()).some(t => t.includes('关闭思考')));
      await page.locator('#wm-protocol').selectOption('/v1/messages');
      assert(!(await labels()).some(t => t.includes('关闭思考')));
      await page.locator('#wm-protocol').selectOption('/chat/completions');
      await page.locator('#wm-effort').selectOption('');
      await page.locator('#wm-save').click();
      assert.match(await page.locator('#wm-payload').textContent(), /"default_effort": ""/);
    });
    await check('Escape unwinds the flow without writing rows', async () => {
      await scene('api');
      await page.locator('#terminal').focus();
      await page.keyboard.press('Escape');
      assert.equal((await snap()).page, 'provider');
      await page.keyboard.press('Escape');
      assert.equal((await snap()).page, 'cancelled');
      assert.equal((await snap()).rows.length, 0);
    });
    await page.locator('[data-width="120"]').click();
    for (const [id, file] of [
      ['start', '311-connect-prototype-home.png'],
      ['effort', '311-connect-prototype-effort.png'],
      ['off', '311-connect-prototype-off.png'],
      ['web-align', '311-connect-prototype-web.png'],
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