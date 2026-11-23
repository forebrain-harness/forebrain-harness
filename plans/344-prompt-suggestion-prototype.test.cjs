const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const http = require('node:http');
const { createRequire } = require('node:module');
const root = path.resolve(__dirname, '..');
const { chromium } = createRequire(path.join(root, 'frontend/package.json'))('@playwright/test');
const html = fs.readFileSync(path.join(__dirname, '344-prompt-suggestion-prototype.html'), 'utf8');
new vm.Script(html.match(/<script>([\s\S]*)<\/script>/)[1]);
assert(!html.includes('\u00b7'), 'Middle-dot separators are forbidden');

const PORT = 8767;
const server = http.createServer((req, res) => {
  const name = req.url === '/' ? '/344-prompt-suggestion-prototype.html' : req.url.split('?')[0];
  const file = path.join(__dirname, path.basename(name));
  if (!file.startsWith(__dirname) || !fs.existsSync(file)) { res.writeHead(404); res.end(); return; }
  res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' });
  res.end(fs.readFileSync(file));
});

(async () => {
  await new Promise(resolve => server.listen(PORT, '127.0.0.1', resolve));
  const browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 1480, height: 1100 } });
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const results = [];
  const check = async (name, fn) => { await fn(); results.push(name); };
  const scene = async id => {
    await page.evaluate(id => {
      const p = window.prototype;
      const s = p.scenes.find(s => s.id === id);
      if (!s) throw Error('Unknown scene: ' + id);
      p.setupScene(s);
    }, id);
    await page.evaluate(() => new Promise(requestAnimationFrame));
  };
  const state = () => page.evaluate(() => {
    const s = window.prototype.state;
    return { draft: s.draft, ghost: s.ghost, shown: s.shown, running: s.running, enabled: s.enabled, globalStreak: s.globalStreak, unusedInARow: s.unusedInARow, eligibleTurns: s.eligibleTurns, throttled: window.prototype.backoffActive(), accepted: s.accepted, generated: s.generated, dismissed: s.dismissed, lastAction: s.lastAction, locale: s.locale, enterSends: s.enterSends, ghostActive: window.prototype.ghostActive() };
  });
  try {
    await page.goto(`http://127.0.0.1:${PORT}/344-prompt-suggestion-prototype.html`);
    await check('All 12 scenes render', async () => {
      const ids = await page.evaluate(() => window.prototype.scenes.map(s => s.id));
      assert.equal(ids.length, 12);
      for (const id of ids) await scene(id);
    });
    await check('Idle ghost shows in both surfaces, draft stays empty', async () => {
      await scene('ghost-idle');
      const s = await state();
      assert(s.ghostActive, 'ghost must be active');
      assert.equal(s.draft, '');
      assert.equal(s.ghost, 'run the tests');
      const ph = await page.locator('#webInput').getAttribute('placeholder');
      assert.equal(ph, 'run the tests', 'web ghost rides the placeholder');
      const tuiGhost = await page.locator('#tuiComposer .ghost').textContent();
      assert.equal(tuiGhost, 'run the tests', 'TUI ghost renders dim after the caret');
    });
    await check('Tab accepts into draft without sending', async () => {
      await scene('ghost-idle');
      await page.locator('#btnAcceptTab').click();
      const s = await state();
      assert.equal(s.draft, 'run the tests');
      assert.equal(s.ghostActive, false, 'a non-empty draft hides the ghost');
      assert.equal(s.running, false, 'accept must not start a turn');
      const ph = await page.locator('#webInput').getAttribute('placeholder');
      assert.equal(ph, '输入消息…', 'placeholder restores once draft is non-empty');
    });
    await check('Right and End accept identically', async () => {
      for (const btn of ['#btnAcceptRight', '#btnAcceptEnd']) {
        await scene('ghost-idle');
        await page.locator(btn).click();
        const s = await state();
        assert.equal(s.draft, 'run the tests');
        assert.equal(s.ghostActive, false);
      }
    });
    await check('Real keyboard Tab/ArrowRight/End work in the web textarea', async () => {
      await scene('ghost-idle');
      await page.locator('#webInput').focus();
      await page.keyboard.press('Tab');
      assert.equal((await state()).draft, 'run the tests');
      await scene('ghost-idle');
      await page.locator('#webInput').focus();
      await page.keyboard.press('ArrowRight');
      assert.equal((await state()).draft, 'run the tests');
      await scene('ghost-idle');
      await page.locator('#webInput').focus();
      await page.keyboard.press('End');
      assert.equal((await state()).draft, 'run the tests');
    });
    await check('Enter on empty direct-sends by default (decided: align CC)', async () => {
      await scene('enter-empty');
      const before = await state();
      assert.equal(before.enterSends, true);
      const r = await page.evaluate(() => window.prototype.enter());
      assert.equal(r, 'sent-suggestion');
      const s = await state();
      assert.equal(s.running, true, 'direct send starts the turn');
      assert.equal(s.shown, false);
      const last = await page.evaluate(() => { const tr = window.prototype.state; return document.querySelectorAll('#webTranscript .u')[document.querySelectorAll('#webTranscript .u').length - 1].textContent; });
      assert.equal(last, 'run the tests');
    });
    await check('Enter-on-empty no-op when the direct-send toggle is off (preview branch)', async () => {
      await scene('enter-empty');
      await page.locator('#enterSends').uncheck();
      const r = await page.evaluate(() => window.prototype.enter());
      assert.equal(r, 'noop');
      const s = await state();
      assert.equal(s.draft, '');
      assert.equal(s.running, false);
      assert(s.ghostActive, 'ghost survives a no-op Enter');
    });
    await check('Typing dismisses permanently; clearing back to empty does not restore', async () => {
      await scene('typing-dismiss');
      const s = await state();
      assert.equal(s.draft, '');
      assert.equal(s.dismissed, true);
      assert.equal(s.ghostActive, false);
      assert.equal(s.shown, false);
    });
    await check('New turn clears the ghost', async () => {
      await scene('ghost-idle');
      await page.locator('#btnTurnStart').click();
      const s = await state();
      assert.equal(s.running, true);
      assert.equal(s.ghost, '');
      assert.equal(s.shown, false);
      assert.equal(s.ghostActive, false);
    });
    await check('Twenty unused offers arm a SILENT 1-in-10 throttle (no notice)', async () => {
      await scene('backoff');
      const s = await state();
      assert.equal(s.globalStreak, 20, 'the install-wide streak arms at 20');
      assert.equal(s.throttled, true, 'backoff is active');
      assert.equal(s.ghostActive, false, 'this turn was throttled away');
      // Strict CC: the throttle is silent — there is no notice element at all.
      assert.equal(await page.locator('#webNotice').count(), 0, 'no web notice');
      assert.equal(await page.locator('#tuiNotice').count(), 0, 'no TUI notice');
      const gen = s.generated;
      await page.locator('#btnTurnEnd').click();
      assert.equal((await state()).generated, gen, 'a throttled turn generates nothing');
      // Advance to the next eligible turn (a multiple of the period) and it offers again.
      const eligible = (await state()).eligibleTurns;
      for (let i = eligible; i % 10 !== 0; i++) await page.locator('#btnTurnEnd').click();
      await page.locator('#btnTurnEnd').click();
      assert((await state()).generated > gen, 'the 1-in-10 eligible turn offers again');
    });
    await check('Accepting clears the global streak and resumes every turn', async () => {
      await scene('accept-resets');
      const s = await state();
      assert.equal(s.globalStreak, 0);
      assert.equal(s.throttled, false);
      assert(s.accepted >= 1);
      assert(s.ghostActive, 'suggestion returns after an accept');
    });
    await check('Re-enabling the feature toggle clears the streak (config UI path)', async () => {
      await scene('backoff');
      assert.equal((await state()).globalStreak, 20);
      await page.locator('#featureOn').uncheck();
      assert.equal((await state()).enabled, false);
      assert.equal((await state()).ghostActive, false);
      await page.locator('#featureOn').check();
      const s = await state();
      assert.equal(s.enabled, true);
      assert.equal(s.globalStreak, 0, 're-enable is a fresh start');
      assert.equal(s.throttled, false);
    });
    await check('Filter matrix rejects evaluative/voice/meta and sanitizes labels', async () => {
      const f = await page.evaluate(() => {
        const p = window.prototype;
        return {
          eval: p.rejection('looks good'),
          evalZh: p.rejection('谢谢'),
          voice: p.rejection("Let me check the logs"),
          voiceZh: p.rejection('我来看看'),
          lets: p.rejection('让我们继续'),
          multi: p.rejection('I fixed the bug. Now I will run the tests.'),
          meta: p.rejection('nothing found'),
          wrapped: p.rejection('(run the tests)'),
          few: p.rejection('a'),
          yes: p.rejection('yes'),
          ok: p.rejection('run the tests'),
          okZh: p.rejection('跑一下测试'),
          sanitize: p.sanitize('Suggestion: run the tests'),
          sanitizeTag: p.sanitize('<suggestion>run the tests</suggestion>'),
          words: p.wordCount('跑一下测试'),
          err: p.rejection('api error: 429'),
        };
      });
      assert.equal(f.eval, 'evaluative');
      assert.equal(f.evalZh, 'evaluative');
      assert.equal(f.voice, 'claude_voice');
      assert.equal(f.voiceZh, 'claude_voice');
      assert.equal(f.lets, '', '"让我们" is the documented exception');
      assert.equal(f.multi, 'multiple_sentences');
      assert.equal(f.meta, 'meta_text');
      assert.equal(f.wrapped, 'meta_wrapped');
      assert.equal(f.few, 'too_few_words');
      assert.equal(f.yes, '');
      assert.equal(f.ok, '');
      assert.equal(f.okZh, '');
      assert.equal(f.sanitize, 'run the tests');
      assert.equal(f.sanitizeTag, 'run the tests');
      assert.equal(f.words, 3, 'Han half-weight: 5 chars -> ceil(2.5) = 3 words');
      assert.equal(f.err, 'error_message');
    });
    await check('Locale switches UI chrome; ghost is model output, not i18n', async () => {
      await scene('locale');
      const s = await state();
      assert.equal(s.locale, 'en');
      assert.equal(s.ghost, '跑一下测试', 'a Chinese conversation gets a Chinese ghost');
      assert.equal(await page.locator('#webSend').textContent(), 'Send', 'UI chrome is EN');
      await page.locator('#localeEn').uncheck();
      assert.equal(await page.locator('#webSend').textContent(), '发送', 'UI chrome switches to ZH');
      assert.equal((await state()).ghost, '跑一下测试', 'ghost unchanged by UI locale');
      await scene('ghost-idle');
      assert.equal((await state()).ghost, 'run the tests');
      await page.locator('#btnTurnEnd').click();
      assert.equal((await state()).ghost, 'run the tests', 'ghost follows the conversation language, not the UI locale');
    });
    await check('Disabled toggle shows nothing', async () => {
      await scene('disabled');
      const s = await state();
      assert.equal(s.ghostActive, false);
      assert.equal(s.enabled, false);
    });
    await check('No middle-dot separators anywhere', async () => {
      const body = await page.evaluate(() => document.body.innerText);
      assert(!body.includes('\u00b7'), 'middle dot rendered');
    });
    for (const [id, file] of [
      ['ghost-idle', '344-prompt-suggestion-prototype-idle.png'],
      ['accept-tab', '344-prompt-suggestion-prototype-accept.png'],
      ['backoff', '344-prompt-suggestion-prototype-backoff.png'],
      ['filters', '344-prompt-suggestion-prototype-filters.png'],
      ['enter-empty', '344-prompt-suggestion-prototype-enter.png'],
    ]) {
      await scene(id);
      await page.screenshot({ path: path.join(__dirname, file), fullPage: true });
    }
    await page.setViewportSize({ width: 900, height: 1100 });
    await scene('ghost-idle');
    await page.screenshot({ path: path.join(__dirname, '344-prompt-suggestion-prototype-narrow.png'), fullPage: true });
    assert.deepEqual(errors, [], 'Browser console errors');
    console.log(JSON.stringify({ passed: results.length, results, browserErrors: errors }, null, 2));
  } finally {
    await browser.close();
    server.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; server.close(); });