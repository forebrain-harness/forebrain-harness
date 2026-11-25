// Regression for the /project panel's TUI prototype: the page named after this
// file (plans/381-project-type-tui-prototype.html).
//
//   python3 -m http.server 8765 --bind 127.0.0.1 --directory plans   # background
//   node plans/381-project-type-tui-prototype.test.cjs
//
// The page must be served over HTTP like every other plans/ prototype. A draft that
// lives outside plans/ names the repo in FOREBRAIN_REPO and the server in
// PROTOTYPE_ORIGIN; screenshots are written only with PROTOTYPE_SHOTS=1.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { createRequire } = require('node:module');
const root = process.env.FOREBRAIN_REPO || path.resolve(__dirname, '..');
const { chromium } = createRequire(path.join(root, 'frontend/package.json'))('@playwright/test');
// The page is named after this file (381-project-type-tui-prototype.test.cjs in plans/).
const name = path.basename(__filename, '.test.cjs');
const origin = process.env.PROTOTYPE_ORIGIN || 'http://127.0.0.1:8765';
const html = fs.readFileSync(path.join(__dirname, name + '.html'), 'utf8');
new vm.Script(html.match(/<script>([\s\S]*)<\/script>/)[1]);
assert(/connect-src 'none'/.test(html), 'Prototype must stay zero-network');
assert(!html.includes('·'), 'Middle-dot separators are forbidden');

(async () => {
  const browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 1480, height: 1100 } });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const results = [];
  const check = async (name, fn) => { await fn(); results.push(name); };
  const scene = async id => {
    await page.evaluate(id => window.projectTypeTuiPrototype.scene(id), id);
    await page.evaluate(() => new Promise(requestAnimationFrame));
  };
  const snap = () => page.evaluate(() => window.projectTypeTuiPrototype.snapshot());
  const reopen = () => page.evaluate(() => window.projectTypeTuiPrototype.open());
  const clickOption = async label => {
    const s = await snap();
    const i = s.options.findIndex(o => o.label === label);
    assert(i >= 0, `Option not visible: ${label} (page ${s.page})`);
    await page.locator(`#grid [data-hit="${i}"]`).first().click();
    await page.evaluate(() => new Promise(requestAnimationFrame));
  };
  const press = async key => {
    await page.locator('#terminal').focus();
    await page.keyboard.press(key);
    await page.evaluate(() => new Promise(requestAnimationFrame));
  };
  const gridText = async () => (await page.locator('#grid').textContent()).replace(/\s+/g, ' ');
  try {
    await page.goto(`${origin}/${name}.html`);
    await check('Every scene renders clean at 120, 88 and 68 columns', async () => {
      const failures = await page.evaluate(() => {
        const failures = [];
        const ids = [...document.querySelectorAll('#nav [data-scene]')].map(b => b.dataset.scene);
        for (const width of [120, 88, 68]) {
          document.querySelector(`[data-width="${width}"]`).click();
          for (const id of ids) {
            window.projectTypeTuiPrototype.scene(id);
            const grid = document.querySelector('#grid');
            if (!grid.textContent.trim()) failures.push(`${width}/${id}: empty grid`);
            if (/[\u4e00-\u9fff]/.test(grid.textContent)) failures.push(`${width}/${id}: CJK inside terminal`);
            for (const row of grid.querySelectorAll('.row')) {
              if (row.scrollWidth > grid.clientWidth + 1) { failures.push(`${width}/${id}: row overflow`); break; }
            }
          }
        }
        document.querySelector('[data-width="120"]').click();
        window.projectTypeTuiPrototype.scene('picker');
        return failures;
      });
      assert.deepEqual(failures, []);
    });
    await check('A conversation that has not started takes the new type and needs no /new', async () => {
      await scene('fresh');
      let s = await snap();
      assert.equal(s.sessionType, null, 'nothing has been said in it: no session row yet');
      assert.equal(s.cursor, 0);
      assert.match(s.subtitle, /^wenquxing\. A change reaches conversations started after it; this one has not started, so it takes the new type\.$/);
      await clickOption('Coding');
      assert.match((await snap()).subtitle, /get the new type; this one has not started, so it takes the new type\.$/);
      await clickOption('Yes, change to Coding');
      s = await snap();
      assert.equal(s.project.type, 'coding');
      assert.match(s.reply, /^Project type is now Coding\. This conversation takes the new type when it starts\.$/);
      assert.doesNotMatch(s.reply, /\/new/, 'nothing has to be started over');
      assert(s.events.some(e => /re-bound/.test(e.text)), 'the un-born conversation is re-bound to the new runner generation');
      await scene('fresh-saved');
      assert.equal((await snap()).reply, s.reply);
    });
    await check('The picker opens on the project type in force and says what a change reaches', async () => {
      await scene('picker');
      const s = await snap();
      assert.equal(s.page, 'picker');
      assert.deepEqual(s.options.map(o => o.label), ['Office work', 'Coding', 'Batch runs']);
      assert.equal(s.cursor, 0, 'the cursor opens on the type in force');
      assert.deepEqual(s.options.map(o => o.mark), ['✓', '', ''], 'only the type in force carries the check');
      assert.match(await gridText(), /❯ Office work ✓ +Documents, spreadsheets, slide decks/, 'the check sits right after the label');
      assert.match(s.subtitle, /A change reaches conversations started after it; this one keeps Office work\.$/);
    });
    await check('Choosing another type asks first; the cursor starts on No', async () => {
      await scene('picker');
      await clickOption('Coding');
      const s = await snap();
      assert.equal(s.page, 'confirm');
      assert.deepEqual(s.options.map(o => o.label), ['No, go back', 'Yes, change to Coding']);
      assert.equal(s.cursor, 0, 'declining is the default answer');
      assert.match(s.subtitle, /^Change wenquxing from Office work to Coding\? Only conversations started after the change get the new type; this one keeps Office work\.$/);
      assert.equal(s.writes.tried, 0, 'opening the confirm page writes nothing');
    });
    await check('No and Esc write nothing and leave no reply', async () => {
      await scene('confirm');
      await clickOption('No, go back');
      let s = await snap();
      assert.equal(s.page, 'picker');
      assert.equal(s.cursor, 1, 'back on the row that was picked');
      await press('Enter');
      assert.equal((await snap()).page, 'confirm', 'Enter on the picked row asks again');
      await press('Escape');
      assert.equal((await snap()).page, 'picker', 'Esc at the confirm page goes back to the picker');
      await press('Escape');
      s = await snap();
      assert.equal(s.page, 'closed');
      assert.equal(s.reply, '', 'cancelling leaves no reply in the conversation');
      assert.deepEqual(s.writes, { tried: 0, applied: 0 });
      assert.equal(s.project.type, 'office');
    });
    await check('Yes changes the project row; the reply says /new starts the new kind', async () => {
      await scene('confirm');
      await clickOption('Yes, change to Coding');
      let s = await snap();
      assert.equal(s.page, 'closed');
      assert.equal(s.project.type, 'coding');
      assert.equal(s.sessionType, 'office', 'the conversation on screen keeps the type it was born with');
      assert.match(s.reply, /^Project type is now Coding\. This conversation keeps Office work; \/new starts a Coding conversation\.$/);
      assert.deepEqual(s.writes, { tried: 1, applied: 1 });
      assert.equal(s.frameTitle, 'project');
      assert.match(await gridText(), /● project +Project type is now Coding\. This conversation keeps Office work; \/new starts a Coding conversation\./);
      assert.doesNotMatch(await gridText(), /Show this project's type/, 'a handled slash command is not echoed');
      await reopen();
      s = await snap();
      assert.equal(s.page, 'picker');
      assert.equal(s.cursor, 1, 'reopened, the cursor sits on the new type');
      assert.deepEqual(s.options.map(o => o.mark), ['', '✓', '']);
      assert.match(s.subtitle, /this one keeps Office work\.$/, 'the conversation on screen is still the old kind');
    });
    await check('Keyboard only: down, enter, down, enter applies the change', async () => {
      await scene('picker');
      await press('ArrowDown');
      await press('Enter');
      assert.equal((await snap()).page, 'confirm');
      await press('ArrowDown');
      await press('Enter');
      const s = await snap();
      assert.equal(s.page, 'closed');
      assert.equal(s.project.type, 'coding');
      assert.equal(s.writes.applied, 1);
    });
    await check('Enter on the current type closes without a write', async () => {
      await scene('unchanged');
      await press('Enter');
      const s = await snap();
      assert.equal(s.page, 'closed');
      assert.equal(s.reply, '', 'nothing changed, so nothing is said');
      assert.deepEqual(s.writes, { tried: 0, applied: 0 });
    });
    await check('A conversation born with the new kind is named like any other', async () => {
      await scene('reopen');
      const s = await snap();
      assert.equal(s.subtitle, 'wenquxing. A change reaches conversations started after it; this one keeps Coding.');
      assert.deepEqual(s.options.map(o => o.mark), ['', '✓', '']);
    });
    await check('To batch: the confirm names what the terminal becomes, and the reply never points at /new', async () => {
      await scene('to-batch');
      const s = await snap();
      assert.deepEqual(s.options.map(o => o.label), ['No, go back', 'Yes, change to Batch runs']);
      assert.match(s.subtitle, / Batch projects take tasks created on the web; the terminal only views them\.$/);
      await clickOption('Yes, change to Batch runs');
      let after = await snap();
      assert.equal(after.project.type, 'batch');
      assert.match(after.reply, /^Project type is now Batch runs\. Batch projects take tasks created on the web; the terminal only views them\. This conversation keeps Coding\.$/);
      await scene('fresh');
      await clickOption('Batch runs');
      await clickOption('Yes, change to Batch runs');
      after = await snap();
      assert.match(after.reply, /^Project type is now Batch runs\. Batch projects take tasks created on the web; the terminal only views them\.$/, 'a conversation that has not started says nothing about starting over');
      assert.doesNotMatch(after.reply, /\/new/, 'a batch conversation takes no typed message in the terminal');
    });
    await check('From batch: tasks stay readable, and the reply asks for a restart', async () => {
      await scene('from-batch');
      let s = await snap();
      assert.equal(s.sessionType, 'batch');
      assert.match(s.subtitle, /this screen keeps replaying a batch execution; restart Forebrain Harness to start a Coding conversation\./);
      assert.match(s.subtitle, / The task page and task notifications stop for this project; tasks already created stay readable\.$/);
      assert.match(await gridText(), /batch read-only/);
      await clickOption('Yes, change to Coding');
      s = await snap();
      assert.equal(s.project.type, 'coding');
      assert.equal(s.sessionType, 'batch', 'the execution on screen is not reborn');
      assert.match(s.reply, /^Project type is now Coding\. Tasks already created stay readable\. Restart Forebrain Harness to start a Coding conversation\.$/);
    });
    await check('A failed write changes nothing and shows the error', async () => {
      await scene('failed');
      await clickOption('Yes, change to Coding');
      const s = await snap();
      assert.equal(s.page, 'closed');
      assert.equal(s.project.type, 'office', 'a failed write leaves the type alone');
      assert.deepEqual(s.writes, { tried: 1, applied: 0 });
      assert.match(s.reply, /^project type was not changed: database is locked \(5\) \(SQLITE_BUSY\)\.$/);
      assert.equal(s.tone, 'bad');
      assert.equal(s.frameTitle, 'error');
      assert.match(await gridText(), /● error +project type was not changed: database is locked/);
      await reopen();
      assert.deepEqual((await snap()).options.map(o => o.mark), ['✓', '', ''], 'the old type is still the one in force');
    });
    await check('The footer is the picker\'s own lowercase line on every open page', async () => {
      for (const id of ['fresh', 'picker', 'confirm', 'reopen', 'to-batch', 'from-batch', 'failed']) {
        await scene(id);
        const text = await gridText();
        assert.match(text, /↑\/↓ to navigate enter to confirm esc to cancel/, `${id}: footer`);
        assert.doesNotMatch(text, /Enter to|Esc to/, `${id}: no capitalized key name in a footer`);
      }
    });
    await check('Every confirm scene opens on No, whichever row the picker sat on', async () => {
      for (const [id, current] of [['confirm', 'office'], ['to-batch', 'coding'], ['from-batch', 'batch'], ['failed', 'office']]) {
        await scene(id);
        const s = await snap();
        assert.equal(s.page, 'confirm', `${id}: starts at the confirm page`);
        assert.equal(s.project.type, current, `${id}: project type in force`);
        assert.equal(s.cursor, 0, `${id}: the cursor starts on No`);
        assert.match(await gridText(), /❯ No, go back/, `${id}: the cursor is drawn on No`);
      }
    });
    if (process.env.PROTOTYPE_SHOTS === '1') {
      await page.locator('[data-width="120"]').click();
      for (const id of ['fresh', 'fresh-saved', 'picker', 'confirm', 'saved', 'to-batch', 'from-batch', 'failed']) {
        await scene(id);
        await page.screenshot({ path: path.join(__dirname, `project-type-tui-${id}.png`), fullPage: true });
      }
    }
    assert.deepEqual(errors, [], 'Browser console errors');
    console.log(JSON.stringify({ passed: results.length, results, browserErrors: errors }, null, 2));
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
