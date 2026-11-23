const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { createRequire } = require('node:module');
const root = path.resolve(__dirname, '..');
const { chromium } = createRequire(path.join(root, 'frontend/package.json'))('@playwright/test');
const html = fs.readFileSync(path.join(__dirname, 'config-tui-prototype.html'), 'utf8');
new vm.Script(html.match(/<script>([\s\S]*)<\/script>/)[1]);
assert(!html.includes('\u00b7'), 'Middle-dot separators are forbidden');

(async () => {
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
  const state = () => page.evaluate(() => ({
    view: window.prototype.state.view,
    draft: window.prototype.state.draft,
    saved: window.prototype.state.saved,
    section: window.prototype.state.section,
    tab: window.prototype.state.tab,
    depth: window.prototype.state.stack.length,
      selected: window.prototype.state.selected,
      inFilter: window.prototype.state.inFilter,
      filter: window.prototype.state.filter,
    path: window.prototype.topFrame()?.path,
    changes: window.prototype.changes(),
    focus: document.activeElement.id,
  }));
  const clickRow = label => page.locator('#panel .item').filter({ has: page.locator('.item-label, .opt-label', { hasText: new RegExp('^' + label.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '$') }) }).click();
  try {
    await page.goto('http://127.0.0.1:8765/config-tui-prototype.html');
    await check('All scenes render at 120, 88 and 68 columns', async () => {
      const failures = await page.evaluate(() => {
        const p = window.prototype, failures = [];
        for (const width of [120, 88, 68]) {
          p.state.width = width;
          for (const s of p.scenes) {
            try {
              p.setupScene(s);
              const panel = document.querySelector('#panel');
              const body = panel.querySelector('.panel-body');
              if (!body.textContent.trim()) failures.push(`${width}/${s.id}: empty body`);
              if (/[\u4e00-\u9fff]/.test(panel.textContent)) failures.push(`${width}/${s.id}: non-English copy`);
              if (body.clientHeight < 40) failures.push(`${width}/${s.id}: no usable body`);
              if (body.scrollWidth > body.clientWidth + 2) failures.push(`${width}/${s.id}: horizontal body overflow`);
              const footer = panel.querySelector('.panel-footer');
              if (footer.scrollWidth > footer.clientWidth + 2) failures.push(`${width}/${s.id}: footer overflow`);
              const header = panel.querySelector('.panel-header');
              if (header.scrollWidth > header.clientWidth + 2) failures.push(`${width}/${s.id}: header overflow`);
            } catch (error) { failures.push(`${width}/${s.id}: ${error.message}`); }
          }
        }
        p.state.width = 120;
        return failures;
      });
      assert.deepEqual(failures, []);
    });
    await check('Every visible nested field opens its own editor', async () => {
      const result = await page.evaluate(() => {
        const p = window.prototype, failures = []; let checked = 0;
        const targets = p.scenes.filter(s => s.id.startsWith('deep-') || s.id.startsWith('section-'));
        for (const s of targets) {
          p.setupScene(s);
          if (p.state.view !== 'fields') continue;
          const labels = p.visible().map(r => r.f?.key);
          for (let i = 0; i < labels.length; i++) {
            p.setupScene(s);
            const row = p.visible()[i];
            if (row.f.requires) continue;
            // A read-only row (the agent template's Name and Primary) draws
            // locked and carries no action: the prototype says so with a
            // notice instead of opening an editor, so it is not a transition.
            const el = document.querySelector(`[data-row="${i}"]`);
            if (!el || el.classList.contains('locked')) continue;
            const before = p.state.stack.length;
            el.click();
            if (!['edit', 'choice'].includes(p.state.view) && p.state.stack.length <= before) failures.push(s.id + '/' + labels[i]);
            checked++;
          }
        }
        return { failures, checked };
      });
      assert.deepEqual(result.failures, []);
      assert(result.checked > 130, `Only ${result.checked} nested fields visited`);
      results.push(`${result.checked} nested field transitions`);
    });
    await check('Tab navigation, selection and Escape work with a keyboard', async () => {
      await scene('overview-safety');
      await page.keyboard.press('ArrowRight');
      assert.equal((await state()).tab, 'session');
      await page.keyboard.press('Enter');
      assert.equal((await state()).section, 'compact');
      await page.keyboard.press('Escape');
      assert.equal((await state()).view, 'overview');
      await page.keyboard.press('Escape');
      assert.equal((await state()).view, 'closed');
      await page.keyboard.press('Enter');
      assert.equal((await state()).view, 'overview');
    });
    await check('Boolean True/False in the list; footer Default restores unset (no restore-default row)', async () => {
      await scene('section-features');
      await clickRow('Memory');
      await clickRow('False');
      assert.equal((await state()).draft.memories, false);
      assert.equal((await state()).saved.features.memories, false);
      await clickRow('Memory');
      assert.equal(await page.locator('#panel .item').filter({ hasText: 'Inherit the built-in' }).count(), 0);
      assert.equal(await page.getByRole('button', { name: 'Default', exact: true }).count(), 0);
      await page.keyboard.press('Escape');
      await clickRow('Memory');
      await clickRow('True');
      assert.equal((await state()).draft.memories, true);
      assert.equal((await state()).saved.features.memories, true);
      await clickRow('Language servers');
      await clickRow('False');
      assert.equal((await state()).saved.features.lsp, false);
      await page.keyboard.press('Escape');
      await clickRow('Feature switches');
      assert.equal((await state()).draft.lsp, false);
    });
    await check('Number validation keeps input; keyboard focus survives Save', async () => {
      await scene('state-number');
      await page.locator('#valueInput').fill('0');
      await page.keyboard.press('Enter');
      assert.equal((await state()).view, 'edit');
      assert.equal((await state()).draft.retention_days, 30);
      assert.match(await page.locator('.inline-error').textContent(), /at least 1/);
      await page.locator('#valueInput').fill('60');
      await page.keyboard.press('Enter');
      assert.equal((await state()).draft.retention_days, 60);
      assert.equal((await state()).saved.cron.retention_days, 60);
      assert.equal((await state()).focus, 'terminal');
      assert.equal((await state()).view, 'fields');
    });
    await check('Clamp reports the effective value', async () => {
      await scene('section-agents');
      await clickRow('Network retries');
      await page.locator('#valueInput').fill('99');
      await page.keyboard.press('Enter');
      assert.equal((await state()).draft.defaults.network_retry_max, 15);
      assert.equal((await state()).saved.agents.defaults.network_retry_max, 15);
      assert.match(await page.locator('.notice').textContent(), /maximum: 15/);
    });
    await check('Multiline text, cancel and restore default', async () => {
      await scene('state-multiline');
      await page.locator('#valueInput').fill('First line\nSecond line');
      await page.keyboard.press('Control+Enter');
      assert.equal((await state()).draft.prompt, 'First line\nSecond line');
      await clickRow('Hand-off prompt');
      await page.locator('#valueInput').fill('Discard this edit');
      await page.keyboard.press('Escape');
      assert.equal((await state()).draft.prompt, 'First line\nSecond line');
      await clickRow('Hand-off prompt');
      // The edit view's Default is a footer key, not a button: Ctrl+U unsets
      // the key the way the TUI's FooterActionDefault does (owner 2026-11-22).
      await page.keyboard.press('Control+u');
      assert(!Object.hasOwn((await state()).draft, 'prompt'));
    });
    await check('Collection add, duplicate protection and delete cancellation', async () => {
      await scene('state-add');
      await page.locator('#valueInput').fill('/workspace/new');
      await page.keyboard.press('Enter');
      assert.equal((await state()).draft.writable_roots.length, 3);
      await page.keyboard.press('d');
      assert.equal((await state()).view, 'delete');
      assert.equal((await state()).selected, 1);
      await page.keyboard.press('Enter');
      assert.equal((await state()).draft.writable_roots.length, 3);
      await page.keyboard.press('d');
      await page.keyboard.press('ArrowUp');
      await page.keyboard.press('Enter');
      assert.equal((await state()).draft.writable_roots.length, 2);
      // A definition is created by the whole form now (plan 351): `a` opens it,
      // the Name row is the key, a duplicate is refused in place, and the
      // footer's Save lands the definition. The seed carries `primary` alone —
      // no invented display name — and the save returns to the list.
      await scene('state-add-object');
      await clickRow('Name');
      await page.locator('#valueInput').fill('main');
      await page.keyboard.press('Enter');
      assert.match(await page.locator('.inline-error').textContent(), /already exists/);
      await page.locator('#valueInput').fill('helper');
      await page.keyboard.press('Enter');
      assert.equal((await state()).draft.definitions.helper.primary, true);
      assert.equal((await state()).path.at(-1), 'helper');
      await page.keyboard.press('s');
      assert.equal((await state()).saved.agents.definitions.helper.primary, true);
      assert.equal((await state()).path.at(-1), 'definitions');
    });
    await check('Global per-type defaults pick global rows; agent override wins', async () => {
      await scene('global-section');
      // The section lists its own seven rows; the install-wide per-type rows live
      // one page down, under Built-in subagents.
      assert.equal(await page.locator('#panel .item').count(), 7);
      await clickRow('Built-in subagents');
      const statuses = await page.locator('#panel .item-status').allTextContents();
      assert(statuses.includes('High') && statuses.includes('Medium'));
      const values = await page.locator('#panel .item-value').allTextContents();
      assert(values.includes('example/reasoner-pro') && values.includes('Follow conversation'));
      assert(!values.some(v => v.includes('…')), 'value column must not truncate: ' + values.join('|'));
      await clickRow('goal-evaluator');
      await clickRow('Thinking level');
      assert.deepEqual(await page.locator('#panel .opt-label').allTextContents(), ['Low', 'Medium', 'High', 'xHigh', 'Follow the model default']);
      await clickRow('xHigh');
      assert.equal((await state()).saved.agents.goal_evaluator.reasoning_effort, 'xhigh');
      await scene('builtin-effective');
      const inherited = await page.evaluate(() => window.prototype.visible().filter(r => ['model', 'reasoning_effort'].includes(r.f.key)).map(r => ({ key: r.f.key, text: r.valueText, status: r.status ?? '' })));
      assert.deepEqual(inherited, [
        { key: 'model', text: 'example/fast-small', status: '' },
        { key: 'reasoning_effort', text: 'Low', status: '' },
      ]);
      await clickRow('Model');
      await page.locator('#panel .item.option').filter({ has: page.locator('.opt-desc', { hasText: /^https:\/\/models\.example\.com\// }) }).first().click();
      assert.equal((await state()).draft.definitions.main.subagents[0].model, 'row-reasoner');
      const overridden = await page.evaluate(() => window.prototype.visible().find(r => r.f.key === 'model'));
      assert.equal(overridden.valueText, 'example/reasoner-pro');
      assert.equal(overridden.status, undefined, 'a built-in pin carries no Agent/Global status (plan 352)');
      await clickRow('Thinking level');
      const efforts = await page.locator('#panel .opt-label').allTextContents();
      assert(efforts.includes('xHigh') && !efforts.includes('Off') && efforts.includes('Follow the model default'));
      await clickRow('xHigh');
      assert.equal((await state()).draft.definitions.main.subagents[0].reasoning_effort, 'xhigh');
      await clickRow('Model');
      await clickRow('Follow primary agent');
      assert(!Object.hasOwn((await state()).draft.definitions.main.subagents[0], 'model'));
      assert(!Object.hasOwn((await state()).draft.definitions.main.subagents[0], 'reasoning_effort'), 'the level goes with the model it was chosen for');
      const back = await page.evaluate(() => window.prototype.visible().filter(r => ['model', 'reasoning_effort'].includes(r.f.key)).map(r => ({ text: r.valueText, status: r.status ?? '' })));
      assert.deepEqual(back, [
        { text: 'example/fast-small', status: '' },
        { text: 'Follow the model default', status: '' },
      ]);
    });
    await check('plan-reviewer global row follows the goal-evaluator pattern', async () => {
      await scene('global-plan-reviewer-effort');
      const labels = await page.locator('#panel .opt-label').allTextContents();
      assert(labels.includes('Medium') && labels.includes('High') && !labels.includes('Off'));
      await clickRow('High');
      assert.equal((await state()).draft.plan_reviewer.reasoning_effort, 'high');
      // The saved level comes back on the install-wide page's row.
      await page.evaluate(() => window.prototype.jump('agents', ['defaults.subagent_models']));
      const saved = await page.evaluate(() => window.prototype.visible().find(r => (r.label || '') === 'plan-reviewer'));
      assert.equal(saved.status, 'High');
      // Back to the row's own fields page (the route above is the level picker).
      await page.evaluate(() => window.prototype.jump('agents', ['defaults.subagent_models', 'plan-reviewer']));
      await clickRow('Model');
      await clickRow('Follow conversation');
      assert.deepEqual((await state()).draft.plan_reviewer, {}, 'clearing the model takes the level too');
      await clickRow('Thinking level');
      assert.equal((await state()).view, 'fields');
      assert.match(await page.locator('.notice').textContent(), /Choose a model/);
    });
    await check('A create form holds every field and writes once on s Save', async () => {
      await scene('create-invalid');
      assert.match(await page.locator('.inline-error').textContent(), /lowercase agent ID/);
      await scene('create-duplicate');
      assert.match(await page.locator('.inline-error').textContent(), /already exists/);
      // Before the footer's save the draft is the only copy (owner 2026-11-22).
      await scene('create-held');
      assert(!Object.hasOwn((await state()).saved.agents.definitions || {}, 'held-agent'));
      // s writes the whole definition once and returns to the list.
      await scene('new-primary-detail');
      const created = (await state()).saved.agents.definitions['review-agent'];
      assert.equal(created.primary, true);
      assert.equal(created.display_name, 'Review agent');
      assert.equal(created.llm_providers, undefined);
      await clickRow('review-agent');
      await clickRow('Model');
      const own = await page.locator('#panel .opt-label').allTextContents();
      assert(own.includes('example/fast-small') && own.includes('Follow global default'));
      assert(!own.includes('First provider entry'));
      await clickRow('example/fast-small');
      assert.equal((await state()).saved.agents.definitions['review-agent'].default_model, 'row-fast');
      await scene('primary-listed');
      const statuses = await page.locator('#panel .item-status').allTextContents();
      assert(statuses.includes('Primary'));
    });
    await check('System types take a model and a level per agent (plan 352)', async () => {
      await scene('system-stray-entry');
      const rows = await page.evaluate(() => window.prototype.visible().map(r => ({ key: r.f.key, text: r.valueText, status: r.status ?? '' })));
      assert.deepEqual(rows, [
        { key: 'model', text: 'example/reasoner-pro', status: '' },
        { key: 'reasoning_effort', text: 'High', status: '' },
      ]);
      await clickRow('Thinking level');
      assert.equal((await state()).view, 'choice');
      await clickRow('Medium');
      const pinned = (await state()).draft.definitions.main.subagents.at(-1);
      assert.equal(pinned.name, 'plan-reviewer');
      assert.equal(pinned.reasoning_effort, 'medium');
    });
    await check('Tab switch preserves immediately-saved values (no unsaved prompt)', async () => {
      await scene('section-features');
      await clickRow('Memory');
      await clickRow('False');
      assert.equal((await state()).saved.features.memories, false);
      await page.locator('.tabs button', { hasText: 'Agents' }).first().click();
      assert.equal((await state()).tab, 'agents');
      assert.equal((await state()).saved.features.memories, false);
      await page.locator('.tabs button', { hasText: 'Session' }).first().click();
      await clickRow('Feature switches');
      assert.equal((await state()).draft.memories, false);
    });
    await check('No filter input box on config pages (owner 2026-11-18 removed it); navigation stays on the list', async () => {
      await scene('section-memories');
      assert.equal(await page.locator('#filterInput').count(), 0);
      assert.equal(await page.locator('.filter').count(), 0);
      assert.equal((await state()).inFilter, false);
      assert.equal(await page.locator('#panel .item.selected').count(), 1);
      assert(!(await page.locator('.panel-footer').textContent()).includes('/ Search'));
      await page.keyboard.press('ArrowUp');
      assert.equal((await state()).inFilter, false);
      assert.equal(await page.locator('#panel .item.selected').count(), 1);
    });
    await check('Model picker carries a filter box; fields pages still do not (owner 2026-11-22)', async () => {
      await scene('section-memories');
      assert.equal(await page.locator('#filterInput').count(), 0);
      await clickRow('Extraction model');
      assert.equal((await state()).view, 'choice');
      assert.equal(await page.locator('#filterInput').count(), 1);
      assert.equal(await page.locator('.filter').count(), 1);
      await page.locator('#filterInput').click();
      await page.keyboard.type('fast');
      assert.equal(await page.evaluate(() => window.prototype.state.query), 'fast', 'the filter box holds what was typed');
      const labels = await page.locator('#panel .opt-label').allTextContents();
      assert(labels.some(l => l.includes('fast-small')) && !labels.some(l => l.includes('reasoner-pro')), 'filter must narrow to fast-small: ' + labels.join('|'));
    });
    await check('Field rows share fixed value/status columns (no drifting value position)', async () => {
      await scene('section-features');
      const geo = await page.locator('.field-table .item').evaluateAll(els => els.map(el => ({
        vLeft: el.querySelector('.item-value')?.getBoundingClientRect().left,
        vRight: el.querySelector('.item-value')?.getBoundingClientRect().right,
        sLeft: el.querySelector('.item-status')?.getBoundingClientRect().left,
      })));
      assert(geo.length >= 2);
      for (const g of geo) {
        assert.equal(g.vLeft, geo[0].vLeft);
        assert.equal(g.vRight, geo[0].vRight);
        assert.equal(g.sLeft, geo[0].sLeft);
      }
    });
    await check('Memory model pickers match the global model picker', async () => {
      await scene('section-memories');
      await clickRow('Extraction model');
      assert.equal((await state()).view, 'choice');
      const labels = await page.locator('#panel .opt-label').allTextContents();
      assert(labels.includes('Follow conversation') && labels.some(l => l.startsWith('example/reasoner-pro')));
      await clickRow('example/fast-small');
      assert.equal((await state()).saved.memories.extract_model, 'row-fast');
      const val = await page.locator('.field-table .item').filter({ hasText: 'Extraction model' }).locator('.item-value').textContent();
      assert.equal(val, 'example/fast-small');
    });
    await check('Groups are separated by one blank line', async () => {
      await scene('section-agents');
      const gaps = await page.locator('.field-table .group-label').evaluateAll(els => els.map(el => parseFloat(getComputedStyle(el).marginTop)));
      const lh = await page.evaluate(() => parseFloat(getComputedStyle(document.querySelector('.terminal')).getPropertyValue('--lh')));
      assert.equal(gaps[0], 0);
      assert(gaps.length > 1);
      for (const g of gaps.slice(1)) assert.equal(g, lh);
    });
    await check('Config inventory matches real config: no fictional or deprecated fields', async () => {
      const inv = await page.evaluate(() => ({
        sections: window.prototype.sections.map(s => s.id),
        fields: Object.fromEntries(window.prototype.sections.map(s => [s.id, s.fields.map(f => f.key)])),
        templates: Object.keys(window.prototype.templates),
        saved: JSON.stringify(window.prototype.state.saved),
      }));
      assert.deepEqual(inv.sections.slice().sort(), ['agents', 'approval_preset', 'batch', 'compact', 'cron', 'features', 'hooks', 'lsp', 'memories', 'peers', 'sandbox_workspace_write'].sort());
      assert.deepEqual(inv.fields.agents.slice().sort(), ['definitions', 'defaults.context_inject.warn_remaining_percent', 'defaults.enable_subagent', 'defaults.mcp_servers', 'defaults.network_retry_max', 'defaults.stream_idle_timeout_seconds', 'defaults.subagent_models'].sort());
      assert.deepEqual(inv.fields.memories.slice().sort(), ['consolidation_model', 'extract_model', 'max_raw_memories_for_consolidation', 'max_rollout_age_days', 'max_rollouts_per_startup', 'max_unused_days', 'min_rollout_idle_hours', 'search_top_k'].sort());
      assert.deepEqual(inv.fields.lsp.slice().sort(), ['diagnostics.after_edit', 'diagnostics.late_delivery', 'diagnostics.max_files', 'diagnostics.max_per_file', 'diagnostics.min_severity', 'diagnostics.wait_ms', 'idle_timeout', 'max_servers', 'recommendations', 'request_timeout', 'servers'].sort());
      assert.deepEqual(inv.fields.compact.slice().sort(), ['auto_compact_percent', 'prompt']);
      assert.deepEqual(inv.fields.batch, ['sandbox_default']);
      assert.deepEqual(inv.fields.cron, ['retention_days']);
      assert.deepEqual(inv.fields.features.slice().sort(), ['lsp', 'memories']);
      assert.deepEqual(inv.fields.peers, ['claude_code']);
      assert.deepEqual(inv.fields.sandbox_workspace_write.slice().sort(), ['network_access', 'network_loopback', 'writable_roots']);
      assert(!inv.templates.includes('provider'), 'provider template must stay deleted');
      assert(!inv.saved.includes('llm_providers'), 'llm_providers must stay deleted');
    });
    await check('No raw config keys on any page: panel body uses friendly labels', async () => {
      await scene('section-agents');
      const bodyText = await page.locator('.panel-body').textContent();
      assert(!/agents\.|defaults\.|subagent_models\.|goal_evaluator\.|plan_reviewer\./.test(bodyText), 'panel must not show dotted config paths');
      await clickRow('Network retries');
      await page.locator('#valueInput').fill('12');
      await page.keyboard.press('Enter');
      assert.equal((await state()).saved.agents.defaults.network_retry_max, 12);
      const bodyText2 = await page.locator('.panel-body').textContent();
      assert(!/agents\.|defaults\.|subagent_models\.|goal_evaluator\.|plan_reviewer\./.test(bodyText2), 'post-save panel must not show dotted config paths');
    });
    await check('Model dependency, clear behavior and secret redaction', async () => {
      // A level needs a model beside it: an unpinned type refuses its picker.
      await scene('global-section');
      await clickRow('Built-in subagents');
      await clickRow('general-purpose');
      await clickRow('Thinking level');
      assert.equal((await state()).view, 'fields');
      assert.match(await page.locator('.notice').textContent(), /Choose a model/);
      await clickRow('Model');
      await clickRow('example/fast-small');
      await clickRow('Thinking level');
      await clickRow('High');
      const pinned = await page.evaluate(() => window.prototype.state.draft.subagent_models['general-purpose']);
      assert.equal(pinned.reasoning_effort, 'high');
      await clickRow('Model');
      await clickRow('Follow conversation');
      const cleared = await page.evaluate(() => window.prototype.state.draft.subagent_models['general-purpose'] || {});
      assert(!Object.hasOwn(cleared, 'reasoning_effort'), 'the level goes with the model it was chosen for');
      await scene('state-secret');
      await page.locator('#valueInput').fill('prototype-secret-example-only');
      assert.equal(await page.locator('#valueInput').getAttribute('type'), 'password');
      await page.keyboard.press('Enter');
      const text = await page.locator('#panel').textContent();
      assert(!text.includes('prototype-secret-example-only'));
      assert(text.includes('[hidden]'));
    });
    await check('All four hook types reveal appropriate fields', async () => {
      await scene('deep-hook-command');
      for (const kind of ['HTTP', 'Agent', 'Prompt', 'Command']) {
        await clickRow('Action type');
        await clickRow(kind);
        const labels = await page.locator('#panel .item-label').allTextContents();
        assert(labels.includes(kind === 'HTTP' ? 'Request URL' : kind === 'Command' ? 'Command' : 'Prompt'));
        assert(!labels.includes(kind === 'HTTP' ? 'Command' : 'Request URL'));
      }
    });
    await check('Write failure keeps the previous saved value', async () => {
      await scene('state-save-error');
      assert.equal((await state()).draft.retention_days, 30);
      assert.equal((await state()).saved.cron.retention_days, 30);
      assert.match(await page.locator('.notice').textContent(), /Could not save/);
      await page.keyboard.press('Escape');
      assert.equal((await state()).view, 'overview');
      assert.equal((await state()).saved.cron.retention_days, 30);
    });
    await check('Conditional platform fields', async () => {
      await scene('state-windows');
      // Windows drops the network-access switch alone (configFieldListed): the
      // writable roots and the loopback switch stay, so the page keeps two rows.
      assert.equal(await page.locator('#panel .item').count(), 2);
      assert.equal((await state()).draft.network_access, true);
      await scene('state-no-sandbox');
      assert.equal(await page.getByRole('tab').count(), 2);
      await scene('state-no-claude');
      assert.equal(await page.locator('#panel .item').count(), 0);
      await scene('state-unavailable');
      await page.getByRole('button', { name: 'Retry', exact: true }).click();
      assert.equal((await state()).view, 'overview');
    });
    await check('No title line above the tab bar and no File only tab', async () => {
      await scene('overview-safety');
      const header = await page.locator('#panel .panel-header').innerText();
      assert(!header.includes('Configuration'), 'the panel draws no title line');
      assert(!/RUNTIME SETTINGS|FILE CONFIGURATION/.test(header), 'the panel draws no state badge');
      const tabs = await page.locator('.tabs button').allInnerTexts();
      assert.equal(tabs.length, 3);
      assert(!tabs.some(t => /File/.test(t)), 'the File only tab is gone with its sections');
      assert.equal(await page.evaluate(() => window.prototype.sections.some(s => s.tab === 'file')), false);
    });
    await check('Narrow screens keep the selected row and footer reachable', async () => {
      await page.locator('[data-size="68"]').click();
      await scene('section-lsp');
      await page.keyboard.press('End');
      const visible = await page.evaluate(() => {
        const row = document.querySelector('.item.selected').getBoundingClientRect();
        const body = document.querySelector('.panel-body').getBoundingClientRect();
        return row.top >= body.top - 1 && row.bottom <= body.bottom + 1;
      });
      assert(visible);
      await page.keyboard.press('Enter');
      assert.equal((await state()).view, 'choice');
      await page.keyboard.press('Escape');
      assert.equal((await state()).view, 'fields');
    });
    await page.locator('[data-size="120"]').click();
    await check('Effort pickers never invent Off and name the empty option Follow the model default', async () => {
      await scene('global-section');
      await clickRow('Built-in subagents');
      await clickRow('explore');
      await clickRow('Model');
      await clickRow('anthropic/claude-sonnet-4-5');
      await clickRow('Thinking level');
      let items = await page.locator('#panel .item').evaluateAll(els => els.map(e => e.textContent.replace(/\s+/g, ' ').trim()));
      assert(!items.some(text => text.includes('Off')), 'Anthropic Messages row must not offer Off');
      assert(items.some(text => text.includes('Follow the model default')), 'the empty option is Follow the model default: ' + items.join('|'));
      assert(!items.some(text => /^(Row|Provider) default/.test(text.replace(/^❯/, ''))), 'no invented default wording');
      await page.keyboard.press('Escape');
      await page.keyboard.press('Escape');
      await scene('global-section');
      await clickRow('Built-in subagents');
      await clickRow('explore');
      await clickRow('Model');
      await clickRow('example/fast-small');
      await clickRow('Thinking level');
      items = await page.locator('#panel .item').evaluateAll(els => els.map(e => e.textContent.replace(/\s+/g, ' ').trim()));
      assert(items[0].includes('Off'), 'an off-capable chat row offers Off first');
    });
    for (const [id, file] of [
      ['overview-safety', 'config-tui-prototype-desktop.png'],
      ['section-agents', 'config-tui-prototype-agents.png'],
      ['section-memories', 'config-tui-prototype-memory.png'],
      ['deep-mcp-detail', 'config-tui-prototype-mcp.png'],
      ['global-section', 'config-tui-prototype-global.png'],
      ['builtin-effective', 'config-tui-prototype-inherit.png'],
      ['create-held', 'config-tui-prototype-create.png'],
    ]) {
      await scene(id);
      await page.screenshot({ path: path.join(__dirname, file), fullPage: true });
    }
    await page.locator('[data-size="68"]').click();
    await scene('section-memories');
    await page.screenshot({ path: path.join(__dirname, 'config-tui-prototype-narrow.png'), fullPage: true });
    assert.deepEqual(errors, [], 'Browser console errors');
    console.log(JSON.stringify({ passed: results.length, results, browserErrors: errors }, null, 2));
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
