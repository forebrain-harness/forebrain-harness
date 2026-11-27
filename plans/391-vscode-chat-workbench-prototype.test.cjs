// Regression for the VS Code chat workbench prototype: the page named after this
// file (plans/391-vscode-chat-workbench-prototype.html).
//
//   python3 -m http.server 8765 --bind 127.0.0.1 --directory plans   # background
//   node plans/391-vscode-chat-workbench-prototype.test.cjs
//
// PROTOTYPE_ORIGIN points at another server; PROTOTYPE_SHOTS=1 also writes the
// screenshots next to this file (light for every scene, dark for the ones whose
// state is a theme decision).
//
// What it pins: every scene renders with no page error; the scenario set covers
// every .vue component ChatView imports (TrustDialog is deferred on purpose);
// consent answers row by row with alwaysAllow; the inline approval and the plan
// dock record their decisions; Sessions searches and selects; light and dark
// are real token swaps and the three brand schemes stay in the sidebar.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { createRequire } = require('node:module');
const root = process.env.FOREBRAIN_REPO || path.resolve(__dirname, '..');
const { chromium } = createRequire(path.join(root, 'frontend/package.json'))('@playwright/test');

const name = path.basename(__filename, '.test.cjs');
const origin = process.env.PROTOTYPE_ORIGIN || 'http://127.0.0.1:8765';
const shots = process.env.PROTOTYPE_SHOTS === '1';
const html = fs.readFileSync(path.join(__dirname, name + '.html'), 'utf8');
new vm.Script(html.match(/<script>([\s\S]*)<\/script>/)[1]);
assert(/connect-src 'none'/.test(html), 'Prototype must stay zero-network');
assert(!html.includes('\u00b7'), 'Middle-dot separators are forbidden');
assert(!/dashed/i.test(html), 'No dashed placeholder markers');

// Every .vue component ChatView.vue imports, minus the one this plan defers.
const INCLUDED = [
  'ProjectPill', 'FolderDialog', 'RewindDialog', 'ProjectServersConsentDialog',
  'SessionHeartbeat', 'BadgeComponent', 'SubagentCallCard', 'SubagentDrawer',
  'SubagentTab', 'RunStatusLine', 'SlashPickerCard', 'ActivityLine', 'ActivityStep',
  'PeerMessageCard', 'PeerDeliveryCard', 'PeerHeldBanner', 'PeerDrawer', 'PeerMark',
  'ToolCallCard', 'ToolDocCommand', 'ToolDocOutput', 'ToolDocTitle',
  'ThinkingCard', 'ApprovalCard', 'PlanApprovalDock', 'BatchRunActions', 'McpNotice',
  'ComposerPlusMenu', 'ModeChip', 'UserAttachments', 'ModelChip', 'EffortChip',
  'ContextRing', 'ForebrainPromptTextarea', 'ChatWorkbench', 'ApprovalPresetPicker',
  'ViewModeChip', 'RunWorkedLine', 'ArtifactList', 'RunErrorBlock',
  'PendingInputQueuePopover', 'AutoContinueBanner', 'LspRecommendationCard',
  'DiffView', 'CompactionCard', 'GoalLine', 'PlanDoc', 'AttachSaveDialog',
];
const SCENES = ['thread', 'fail', 'runtime', 'plan', 'todos-unread', 'handoff-models', 'handoff-effort', 'review-models', 'review-running', 'review-verdict', 'subagent', 'peer', 'consent', 'slash', 'sessions', 'empty', 'api', 'fallback'];
// What each scene exists to show: a scene that lost its reason is a scene that
// silently lost a component.
const EXPECTED = {
  thread: ['ChatWorkbench', 'ThinkingCard', 'ToolCallCard', 'ToolDocCommand', 'ToolDocOutput', 'DiffView', 'ArtifactList', 'ActivityLine', 'ActivityStep', 'ApprovalCard', 'PendingInputQueuePopover', 'RunStatusLine', 'BadgeComponent', 'ForebrainPromptTextarea'],
  fail: ['RunErrorBlock', 'ApprovalCard', 'ToolCallCard', 'RunWorkedLine'],
  runtime: ['CompactionCard', 'GoalLine', 'AutoContinueBanner', 'McpNotice', 'LspRecommendationCard', 'BatchRunActions', 'SessionHeartbeat', 'RunStatusLine'],
  plan: ['PlanApprovalDock', 'PlanDoc', 'RunWorkedLine'],
  'todos-unread': ['ChatWorkbench', 'ThinkingCard', 'ActivityLine', 'ActivityStep', 'ForebrainPromptTextarea'],
  'handoff-models': ['PlanApprovalDock', 'PlanDoc'],
  'handoff-effort': ['PlanApprovalDock', 'PlanDoc'],
  'review-models': ['PlanApprovalDock', 'PlanDoc'],
  'review-running': ['PlanApprovalDock', 'SubagentCallCard'],
  'review-verdict': ['PlanApprovalDock', 'PlanDoc'],
  subagent: ['SubagentDrawer', 'SubagentTab', 'SubagentCallCard'],
  peer: ['PeerDrawer', 'PeerHeldBanner', 'PeerMessageCard', 'PeerDeliveryCard', 'PeerMark'],
  consent: ['ProjectServersConsentDialog'],
  slash: ['SlashPickerCard', 'ForebrainPromptTextarea', 'UserAttachments'],
  sessions: ['SessionsList'],
  empty: ['ProjectPill', 'ForebrainPromptTextarea'],
  api: ['SessionsList'],
  fallback: ['ChatWorkbench'],
};

(async () => {
  const browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 1520, height: 1180 } });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const results = [];
  const check = async (label, fn) => { await fn(); results.push(label); };
  const frame = () => page.evaluate(() => new Promise(requestAnimationFrame));
  const scene = async id => {
    await page.evaluate(id => window.chatWorkbenchPrototype.scene(id), id);
    await frame();
  };
  const snap = () => page.evaluate(() => window.chatWorkbenchPrototype.snapshot());
  const click = async selector => { await page.locator(selector).first().click(); await frame(); };
  const text = async selector => (await page.locator(selector).first().textContent()).replace(/\s+/g, ' ').trim();
  const bg = selector => page.locator(selector).first().evaluate(el => getComputedStyle(el).backgroundColor);
  const shot = async (fileName) => { if (shots) await page.screenshot({ path: path.join(__dirname, fileName), fullPage: true }); };

  try {
    await page.goto(`${origin}/${name}.html`);

    await check('Every scene renders with a caption and no page error', async () => {
      for (const id of SCENES) {
        await scene(id);
        const s = await snap();
        assert.equal(s.scene, id);
        assert(s.comps.length >= 1, `${id}: no components drawn`);
        const missing = EXPECTED[id].filter(c => !s.comps.includes(c));
        assert.deepEqual(missing, [], `${id}: components missing`);
        assert((await text('#cap')).length > 20, `${id}: caption is missing`);
        assert((await text('#vsc')).length > 40, `${id}: window is empty`);
      }
      assert.deepEqual(errors, [], 'Browser console errors');
    });

    await check('The scenario set covers every component ChatView imports, TrustDialog deferred', async () => {
      const seen = new Set();
      for (const id of SCENES) {
        await scene(id);
        for (const c of (await snap()).comps) seen.add(c);
      }
      // Openers this page has no scene of its own for.
      for (const dialog of ['rewind', 'attach', 'folder']) {
        await page.evaluate(d => window.chatWorkbenchPrototype.open(d), dialog);
        await frame();
        (await snap()).comps.forEach(c => seen.add(c));
        await page.keyboard.press('Escape');
        await frame();
      }
      await scene('slash');
      await click('[data-act="plus"]');
      (await snap()).comps.forEach(c => seen.add(c));
      const missing = INCLUDED.filter(c => !seen.has(c));
      assert.deepEqual(missing, [], 'components never drawn');
      assert(!seen.has('TrustDialog'), 'TrustDialog is deferred: it must not be drawn in the panel');
      // Two components are web-only surfaces the extension must not draw: the
      // trust dialog (VS Code's own trust takes its place) and the session_todo
      // message card (the native tree view takes its place, owner 2026-11-26).
      assert(!seen.has('PlanUpdateCard'), 'the webview todo message card must not be drawn in the extension');
      // Web-only surfaces the plugin replaces: the paid-tier offer has no place
      // here (owner: no such scenario), and a delivered file opens in VS Code
      // (editor diff for code, native preview / OS external open for the rest)
      // rather than the web's inline ArtifactPreview.
      assert(!seen.has('PaidTierBanner'), 'the paid-tier offer is not a plugin surface');
      assert(!seen.has('ArtifactPreview'), 'deliverables open in VS Code, not the web inline preview');
    });

    await check('Thread scene is a flat activity flow, failed rows keep danger', async () => {
      await scene('thread');
      assert.match(await text('[data-comp="ThinkingCard"]'), /深度思考/);
      assert.match(await text('.tool.ok'), /已读取|已运行/);
      // No status text: the icon carries the state. In-progress spins; done/fail are static.
      assert.equal(await page.locator('.tool-h .st').count(), 0, 'no 「完成」 status text; the icon carries the state');
      assert.equal(await page.locator('[data-status="run"]').count(), 1, 'an in-progress tool row is shown');
      await page.emulateMedia({ reducedMotion: 'no-preference' });
      assert.equal(await page.locator('[data-status="run"] .tlead').evaluate(el => getComputedStyle(el).animationName), 'todoSpin');
      assert.equal(await page.locator('[data-status="ok"] .tlead').first().evaluate(el => getComputedStyle(el).animationName), 'none');
      await page.emulateMedia({ reducedMotion: 'reduce' });
      assert.equal(await page.locator('[data-status="run"] .tlead').evaluate(el => getComputedStyle(el).animationName), 'none');
      // Collapsed by default: the output body stays in the DOM, hidden until expanded.
      assert.equal(await page.locator('.tool.open').count(), 0);
      await click('.tool.ok .tool-h');
      assert.equal(await page.locator('.tool.open').count(), 1, 'the chevron expands a tool row');
      // 深度思考 expands to its reasoning body, then collapses back.
      assert.equal(await page.locator('.think.open').count(), 0);
      await click('.think .think-h');
      assert.equal(await page.locator('.think.open').count(), 1);
      assert.equal(await page.locator('.think-b').count(), 1, 'the reasoning body shows when expanded');
      await scene('fail');
      assert.equal(await page.locator('[data-status="fail"]').count(), 1);
      const okColor = await page.locator('[data-status="ok"] .tlead').first().evaluate(el => getComputedStyle(el).color);
      const failColor = await page.locator('[data-status="fail"] .tlead').evaluate(el => getComputedStyle(el).color);
      assert.notEqual(okColor, failColor, 'a failed row must not read like a successful one');
      assert.match(await text('[data-comp="RunErrorBlock"]'), /这一轮没有跑完/);
      assert.match(await text('[data-comp="ApprovalCard"]'), /你拒绝了这条命令，它没有运行/);
    });

    await check('Inline approval records the decision and the scene resets it', async () => {
      await scene('thread');
      assert.equal((await snap()).approval, 'waiting');
      assert.match(await text('[data-comp="ApprovalCard"]'), /要运行这条命令吗/);
      await click('[data-act="approve"]');
      let s = await snap();
      assert.equal(s.approval, 'allowed');
      assert.match(await text('[data-comp="ApprovalCard"]'), /已允许 运行 npm/);
      await scene('thread');
      assert.equal((await snap()).approval, 'waiting', 'a fresh scene starts undecided');
      await click('[data-act="deny"]');
      s = await snap();
      assert.equal(s.approval, 'denied');
      assert(s.events.some(e => /拒绝 运行/.test(e.text)));
    });

    await check('Message queue lists two pending inputs', async () => {
      await scene('thread');
      assert.match(await text('[data-comp="PendingInputQueuePopover"]'), /消息队列\s*2 条待发/);
      assert.match(await text('[data-comp="PendingInputQueuePopover"]'), /下一个工具调用之后发出/);
    });

    await check('Project servers consent answers row by row with alwaysAllow', async () => {
      await scene('consent');
      let s = await snap();
      assert.equal(s.dialog, 'consent');
      assert.deepEqual(s.consent.mcp.map(r => r.name), ['filesystem', 'postgres-local']);
      assert.deepEqual(s.consent.lsp.map(r => r.name), ['gopls', 'vue-language-server']);
      assert(s.consent.mcp.concat(s.consent.lsp).every(r => r.allow), 'every row opens checked');
      assert.equal(s.consent.alwaysAllow, false, 'the switch opens off');
      assert.equal(await page.locator('[data-comp="ProjectServersConsentDialog"] .row').count(), 4);
      assert.match(await text('[data-comp="ProjectServersConsentDialog"]'), /4 个服务要在本项目运行/);
      assert.match(await text('[data-comp="ProjectServersConsentDialog"]'), /MCP/);
      assert.match(await text('[data-comp="ProjectServersConsentDialog"]'), /语言服务器/);

      await click('[data-act="consent-row"][data-v="mcp:filesystem"]');
      s = await snap();
      assert.equal(s.consent.mcp[0].allow, false, 'unchecking a row declines it');
      assert.equal(s.consent.alwaysAllow, false, 'unchecking an entry turns the switch off');

      await click('[data-act="consent-always"]');
      s = await snap();
      assert(s.consent.mcp.concat(s.consent.lsp).every(r => r.allow), 'switching on checks every entry');
      assert.equal(s.consent.alwaysAllow, true);

      await click('[data-act="consent-confirm"]');
      s = await snap();
      assert.equal(s.dialog, null);
      assert.equal(s.consentAnswer.body.alwaysAllow, true);
      assert.deepEqual(s.consentAnswer.body.mcp, [{ name: 'filesystem', allow: true }, { name: 'postgres-local', allow: true }]);
      assert.match(s.consentAnswer.note, /重绑会话/);
      assert.match(await page.locator('#consent-body').textContent(), /"alwaysAllow": true/);
      assert(s.events.some(e => /项目服务：已确定/.test(e.text)));
    });

    await check('Declining every service records a whole refusal', async () => {
      await scene('consent');
      await click('[data-act="consent-decline"]');
      const s = await snap();
      assert.equal(s.dialog, null);
      assert(s.consentAnswer.body.mcp.every(r => r.allow === false));
      assert.equal(s.consentAnswer.body.alwaysAllow, false);
      assert.match(s.consentAnswer.note, /全部不允许/);
    });

    await check('Plan dock carries the four exits and records the approval', async () => {
      await scene('plan');
      assert.match(await text('[data-comp="PlanApprovalDock"]'), /要开始执行吗/);
      assert.match(await text('[data-comp="PlanApprovalDock"]'), /anthropic\/claude-sonnet-4-6 的评审结论/);
      for (const label of ['交给其他模型执行', '请其他模型评审', '继续规划']) {
        assert.equal(await page.locator(`[data-comp="PlanApprovalDock"] button:has-text("${label}")`).count(), 1, label);
      }
      assert.equal(await page.locator('[data-comp="PlanDoc"]').count(), 1);
      // The webview checklist card this plan used to draw is gone: the todo
      // surface is the native tree view now (checked below).
      assert.equal(await page.locator('[data-comp="PanelChecklist"]').count(), 0);
      await click('[data-act="plan-approve"]');
      const s = await snap();
      assert.equal(s.planDecision, 'approved');
      assert.match(await text('[data-comp="PlanApprovalDock"]'), /已批准，开始实施/);
      await click('[data-act="open-rewind"]');
      assert.equal((await snap()).dialog, 'rewind');
      assert.match(await text('[data-comp="RewindDialog"]'), /恢复 3 个文件/);
      await page.keyboard.press('Escape');
      await frame();
      assert.equal((await snap()).dialog, null, 'Escape closes the dialog');
    });

    await check('No webview todo message card: the native tree is the only todo surface', async () => {
      // Owner 2026-11-26: session_todo in the extension is the native Tree View
      // only. The web's PlanUpdateCard glyph card must not be drawn anywhere —
      // not as a message, not as a per-call card, not with a tally.
      for (const id of SCENES) {
        await scene(id);
        assert.equal(await page.locator('[data-testid="plan-update-card"]').count(), 0, id + ' draws the todo message card');
        assert.equal(await page.locator('[data-comp="PlanUpdateCard"]').count(), 0, id + ' draws PlanUpdateCard');
        assert.equal(await page.locator('.plan-card').count(), 0, id + ' keeps the card markup');
        // A checklist glyph is a leaf element holding exactly one of these; a
        // tool's own output may contain a tick inside a longer line, which is
        // not a checklist.
        const glyphRows = await page.evaluate(() => {
          const bad = [];
          document.querySelectorAll('.thread *').forEach((el) => {
            const glyph = (el.textContent || '').trim();
            if (el.children.length === 0 && ['✓', '▣', '✕', '□'].includes(glyph)) bad.push(glyph);
          });
          return bad;
        });
        assert.deepEqual(glyphRows, [], id + ' still draws a checklist glyph row');
      }
      // The plan approval gate and the plan-file card are different surfaces and
      // stay: one is the gate, the other the .md card with the editor eye.
      await scene('plan');
      assert.equal(await page.locator('[data-comp="PlanApprovalDock"]').count(), 1);
      for (const button of ['plan-approve', 'plan-handoff', 'plan-review', 'plan-keep']) {
        assert.equal(await page.locator(`[data-act="${button}"]`).count(), 1, button + ' stays on the dock');
      }
      assert.equal(await page.locator('[data-comp="PlanDoc"]').count(), 1, 'the plan-file card stays');
      assert.match(await text('[data-comp="PlanDoc"]'), /把 VS Code 表面切成三个 webview/);
      assert.equal(await page.locator('[data-act="open-plan-editor"]').count(), 1, 'and its eye still opens the editor');
      // The todo surface is the toggle, and only when it is asked for.
      assert.equal(await page.locator('[data-testid="todos-toggle"]').count(), 1);
      assert.equal(await page.locator('[data-testid="todo-tree"]').count(), 0);
    });

    await check('The todo card is the web-styled checklist, toggled from the composer', async () => {
      await scene('plan');
      const chip = page.locator('[data-testid="todos-toggle"]');
      assert.equal(await chip.count(), 1);
      // The web names this surface 待办 (OverviewTab's card, chat.benchTodos); the
      // chip and the card title use that word, with a done/total count.
      assert.equal((await chip.locator('.chip-n').textContent()).trim(), '2/6');
      assert.match(await chip.textContent(), /待办/);
      assert.equal(await chip.getAttribute('aria-expanded'), 'false');
      assert.equal(await chip.getAttribute('aria-controls'), 'forebrain-todos');
      assert.equal(await page.locator('[data-testid="todo-list"]').count(), 0, 'a session-scoped toggle, closed until asked');
      // Not VS Code's tree, and not a row of native checkboxes.
      assert.equal(await page.locator('.vtree-wrap, [role="tree"], [role="treeitem"]').count(), 0);
      await shot('391-shot-light-todos-closed.png');

      await chip.click();
      await frame();
      assert.equal(await chip.getAttribute('aria-expanded'), 'true');
      const card = page.locator('[data-testid="todo-list"]');
      assert.equal(await card.count(), 1);
      assert.equal((await card.locator('.todoCardTitle').textContent()).trim(), '待办');
      assert.equal((await card.locator('[data-testid="todo-count"]').textContent()).trim(), '2/6');
      assert.equal(await card.locator('input').count(), 0, 'no native checkbox in the card');

      const rows = card.locator('[data-testid="todo-row"]');
      assert.equal(await rows.count(), 6);
      // The three states, in the card's own terms: done / in flight / pending.
      assert.deepEqual(
        await rows.locator('[role="checkbox"]').evaluateAll((els) => els.map((el) => el.getAttribute('aria-checked'))),
        ['true', 'true', 'mixed', 'false', 'false', 'false'],
      );
      assert.equal(await rows.nth(0).evaluate((el) => el.classList.contains('done')), true);
      assert.match(await rows.nth(0).locator('.todoText').evaluate((el) => getComputedStyle(el).textDecorationLine), /line-through/);
      assert.equal(await rows.nth(2).evaluate((el) => el.classList.contains('run')), true);
      assert.equal(await rows.nth(2).locator('.todoText').evaluate((el) => getComputedStyle(el).fontWeight), '500');
      assert.equal(await rows.nth(5).evaluate((el) => el.classList.contains('cancel')), true);
      // One label per row: the item, and nothing in a second column (owner
      // 2026-11-26: the step tags were dropped for a minimal card).
      assert.equal(await card.locator('.todoStep, [data-testid="todo-step"]').count(), 0);
      const rowWidths = await rows.evaluateAll((els) => els.map((el) => Math.round(el.querySelector('.todoText').getBoundingClientRect().right)));
      assert(new Set(rowWidths).size > 1, 'no second column is reserving space at the right');
      // Above the composer and inside the chat webview.
      const where = await page.evaluate(() => {
        const listEl = document.querySelector('[data-testid="todo-list"]');
        const wv = document.querySelector('[data-comp="ChatWorkbench"]');
        const box = (el) => el.getBoundingClientRect();
        return {
          inside: wv.contains(listEl),
          aboveComposer: box(listEl).bottom <= box(wv.querySelector('.composer-wrap')).top + 1,
          belowThread: box(listEl).top >= box(wv.querySelector('.thread')).top + 1,
        };
      });
      assert.deepEqual(where, { inside: true, aboveComposer: true, belowThread: true });
      // A webview surface: the app's own palette.
      assert.equal(await card.evaluate((el) => getComputedStyle(el).backgroundColor), 'rgb(255, 255, 255)');
      await shot('391-shot-light-todos-open.png');

      await chip.click();
      await frame();
      assert.equal(await page.locator('[data-testid="todo-list"]').count(), 0, 'the same toggle closes it');
      assert.equal(await chip.getAttribute('aria-expanded'), 'false');
    });

    await check('A todo update lands as unread, and reads on demand', async () => {
      await scene('plan');
      const chip = page.locator('[data-testid="todos-toggle"]');
      const simulate = page.locator('[data-testid="simulate-todo-update"]');
      assert.equal(await simulate.count(), 1, 'the prototype can drive one update');

      // Closed: the chip carries an unread badge and a pulsing dot, and says so
      // to a screen reader — an update is never silent.
      await simulate.click();
      await frame();
      assert.equal(await page.locator('[data-testid="todos-badge"]').count(), 1);
      assert.match(await text('[data-testid="todos-badge"]'), /^2$/);
      assert.equal(await page.locator('[data-testid="todos-dot"]').count(), 0, 'the pulse dot is gone; the badge itself animates');
      // The badge carries the pulse in normal motion and yields to reduced motion.
      await page.emulateMedia({ reducedMotion: 'no-preference' });
      assert.equal(await page.locator('[data-testid="todos-badge"]').evaluate(el => getComputedStyle(el).animationName), 'badgePulse');
      await page.emulateMedia({ reducedMotion: 'reduce' });
      assert.equal(await page.locator('[data-testid="todos-badge"]').evaluate(el => getComputedStyle(el).animationName), 'none');
      assert.match(await chip.getAttribute('aria-label'), /2 项未读/);
      assert.equal((await chip.locator('.chip-n').textContent()).trim(), '3/6', 'the count moved with the data');
      assert.equal((await snap()).todoUnseen, 2);
      await shot('391-shot-light-todos-badge.png');

      // Opening hides the chip badge, but the changed rows stay unread (dot + bold)
      // and flash once. Unread persists until the reader marks it read.
      await chip.click();
      await frame();
      assert.equal(await page.locator('[data-testid="todos-badge"]').count(), 0);
      assert.equal(await page.locator('.todoRow.unread').count(), 2, 'the two changed rows are unread');
      assert.equal(await page.locator('.todoRow.flash').count(), 2, 'the two changed rows flash');
      assert.equal(await page.locator('[data-testid="todo-read-all"]').count(), 1, 'a mark-all-read affordance appears');
      let s = await snap();
      assert.deepEqual(s.todoChanged.slice().sort((a, b) => a - b), [2, 3]);
      assert.deepEqual(s.todoUnread.slice().sort((a, b) => a - b), [2, 3]);
      assert.equal(s.todoFlash, true);
      // The advanced rows really moved: two done, one in flight again.
      assert.deepEqual(
        await page.locator('[data-testid="todo-row"] [role="checkbox"]').evaluateAll((els) => els.map((el) => el.getAttribute('aria-checked'))),
        ['true', 'true', 'true', 'mixed', 'false', 'false'],
      );
      await shot('391-shot-light-todos-updated.png');

      // The flash fades, but the unread mark stays — unlike a transient cue.
      await page.waitForTimeout(3000);
      await frame();
      assert.equal(await page.locator('.todoRow.flash').count(), 0);
      assert.equal(await page.locator('.todoRow.unread').count(), 2, 'unread persists after the flash');
      assert.equal((await snap()).todoUnseen, 2);

      // Clicking one unread row reads just that row.
      await click('.todoRow.unread');
      await frame();
      assert.equal(await page.locator('.todoRow.unread').count(), 1);

      // 全部已读 clears the rest and removes itself.
      await click('[data-testid="todo-read-all"]');
      await frame();
      assert.equal(await page.locator('.todoRow.unread').count(), 0);
      assert.equal(await page.locator('[data-testid="todo-read-all"]').count(), 0);
      assert.equal((await snap()).todoUnseen, 0);

      // Open: a further update flashes in place and grows unread, not a chip badge.
      await page.evaluate(() => window.chatWorkbenchPrototype.updateTodos());
      await frame();
      assert.equal(await page.locator('[data-testid="todos-badge"]').count(), 0);
      assert.equal(await page.locator('.todoRow.flash').count() >= 1, true);
      s = await snap();
      assert.equal(s.todoUnseen >= 1, true);
    });

    await check('The todos-unread scene opens on the unread state, readable in place', async () => {
      await scene('todos-unread');
      assert.equal((await snap()).todosOpen, true);
      // The in-progress row's icon spins in normal motion and yields to reduced motion.
      await page.emulateMedia({ reducedMotion: 'no-preference' });
      assert.equal(await page.locator('.todoRow.run .ic').evaluate(el => getComputedStyle(el).animationName), 'todoSpin');
      await page.emulateMedia({ reducedMotion: 'reduce' });
      assert.equal(await page.locator('.todoRow.run .ic').evaluate(el => getComputedStyle(el).animationName), 'none');
      assert.equal(await page.locator('.todoRow.unread').count(), 2, 'two rows are unread by default');
      assert.equal(await page.locator('[data-testid="todo-read-all"]').count(), 1);
      assert.equal((await snap()).todoUnseen, 2);
      await shot('391-shot-light-todos-unread.png');
      // Read one row in place.
      await click('.todoRow.unread');
      await frame();
      assert.equal(await page.locator('.todoRow.unread').count(), 1);
      // Closing the card puts the remaining unread count on the chip.
      await click('[data-testid="todos-toggle"]');
      await frame();
      assert.equal(await page.locator('[data-testid="todos-badge"]').count(), 1);
      assert.match(await text('[data-testid="todos-badge"]'), /^1$/);
    });

    await check('Clicking the plan eye opens the plan in the editor area', async () => {
      await scene('plan');
      // The centre pane starts on the code file, with no plan tab yet.
      assert.equal((await snap()).editorTab, 'code');
      assert.equal((await snap()).planTabOpen, false);
      assert.equal(await page.locator('[data-testid="editor-tab-plan"]').count(), 0);
      assert.equal(await page.locator('[data-testid="editor-md-preview"]').count(), 0);
      assert.match(await text('[data-testid="editor-area"]'), /import \* as vscode from 'vscode'/);

      const eye = page.locator('[data-act="open-plan-editor"]');
      assert.equal(await eye.count(), 1);
      assert.equal(await eye.getAttribute('title'), '在编辑器中打开计划');
      assert.equal(await eye.getAttribute('aria-label'), '在编辑器中打开计划');
      await eye.click();
      await frame();
      let s = await snap();
      assert.equal(s.editorTab, 'plan');
      assert.equal(s.planTabOpen, true);
      assert(s.events.some(e => /在编辑器中打开计划文件 plans\/391-vscode-chat-workbench\.md/.test(e.text)));

      // The tab is named for the .md and says it is a preview.
      const tab = page.locator('[data-testid="editor-tab-plan"]');
      assert.equal(await tab.count(), 1);
      assert.match(await tab.textContent(), /391-vscode-chat-workbench\.md/);
      assert.match(await tab.textContent(), /预览/);
      assert.equal(await tab.evaluate(el => el.classList.contains('on')), true);
      assert.equal(await tab.evaluate(el => el.classList.contains('md')), true);

      // The pane now renders the plan as markdown, not the .ts source.
      const preview = page.locator('[data-testid="editor-md-preview"]');
      assert.equal(await preview.count(), 1);
      assert.match(await preview.textContent(), /把 VS Code 表面切成三个 webview/);
      assert.match(await preview.textContent(), /plans\/391-vscode-chat-workbench\.md/);
      assert.match(await preview.textContent(), /目标/);
      assert.equal(await page.locator('.ed-md h1').count(), 1);
      assert.equal(await page.locator('.ed-md ol li').count(), 5, 'the five steps travel with the card');
      assert(!(await text('[data-testid="editor-area"]')).includes("import * as vscode"), 'the code pane gave way');

      // The card stays where it was: the editor is a different surface.
      assert.equal(await page.locator('[data-comp="PlanApprovalDock"]').count(), 1);
      assert.equal(await page.locator('[data-comp="PlanDoc"]').count(), 1);

      // Clicking it again is a no-op that says so, and a code tab takes the pane back.
      await eye.click();
      await frame();
      s = await snap();
      assert.equal(s.editorTab, 'plan');
      assert(s.events.some(e => /计划已经在编辑器里打开/.test(e.text)));
      await click('.ed-tab:not(.md)');
      assert.equal((await snap()).editorTab, 'code');
      assert.equal(await page.locator('[data-testid="editor-md-preview"]').count(), 0);
      // The tab survives switching away, and comes back with the preview.
      await click('[data-testid="editor-tab-plan"]');
      assert.equal((await snap()).editorTab, 'plan');
      assert.equal(await page.locator('[data-testid="editor-md-preview"]').count(), 1);
      await shot('391-shot-light-plan-editor.png');
    });

    await check('Handing the plan over walks model then effort, on the dock itself', async () => {
      await scene('plan');
      // Both exits live on the dock's footer, beside the same three other verbs.
      assert.equal(await page.locator('[data-act="plan-handoff"]').count(), 1);
      assert.equal(await page.locator('[data-act="plan-review"]').count(), 1);
      await click('[data-act="plan-handoff"]');
      let s = await snap();
      assert.equal(s.dockPick, 'handoff-models');
      const picker = page.locator('[data-testid="exit-plan-hand-off-models"]');
      assert.match(await picker.textContent(), /交给哪个模型执行？/);
      assert.match(await picker.textContent(), /在新的 worktree 里实施，不动当前分支/);
      assert.equal(await picker.locator('[data-testid="exit-plan-hand-off-model"]').count(), 3);
      const rows = picker.locator('[data-testid="exit-plan-hand-off-model"]');
      // Each row: provider/model, then the endpoint it really dials.
      assert.match(await rows.nth(0).textContent(), /anthropic\/claude-sonnet-4-6/);
      assert.match(await rows.nth(0).textContent(), /https:\/\/api\.anthropic\.com\/v1/);
      assert(!(await rows.nth(0).textContent()).includes('当前'), 'the current model is marked by highlight, not a 「当前」 label');
      assert.equal(await rows.nth(0).locator('.mchk').count(), 0, 'no redundant check: the highlight marks selection');
      // The current model is the highlighted row the picker opens on.
      assert.equal(await picker.locator('[data-testid="exit-plan-hand-off-model"].is-on').count(), 1);
      assert.match(await picker.locator('.is-on').textContent(), /claude-sonnet-4-6/);

      await click('[data-testid="exit-plan-hand-off-model"]');
      s = await snap();
      assert.equal(s.dockPick, 'handoff-effort');
      const effort = page.locator('[data-testid="exit-plan-effort"]');
      assert.match(await text('[data-testid="exit-plan-effort-back"]'), /返回选模型/);
      assert.match(await text('[data-testid="exit-plan-effort-question"]'), /anthropic\/claude-sonnet-4-6 用哪档思考强度？/);
      const options = effort.locator('[data-testid="exit-plan-effort-option"]');
      assert.deepEqual(await options.locator('.elabel').allTextContents(), ['低', '中', '高', '最高'], 'the levels this model offers');
      assert.deepEqual(await options.locator('.ev').allTextContents(), ['low', 'medium', 'high', 'xhigh'], 'each row writes its id');
      // The level the model row itself configures: the gear, and the row it opens on.
      assert.equal(await effort.locator('.etag').count(), 1);
      assert.equal(await effort.locator('.etag').first().getAttribute('title'), '模型设置');
      assert.match(await effort.locator('.xp-model.is-on').textContent(), /高/);
      assert.equal(await effort.locator('.xp-model.is-on .echk').count(), 0, 'the highlight marks the picked level, no redundant check');

      await click('[data-act="back-models"]');
      assert.equal((await snap()).dockPick, 'handoff-models', '返回选模型 goes back one screen');
      await click('[data-testid="exit-plan-hand-off-model"]');
      await click('[data-testid="exit-plan-effort-option"][data-v="medium"]');
      s = await snap();
      assert.equal(s.dockPick, null);
      assert.equal(s.planDecision, 'handoff');
      assert.equal(s.handoffEffort, '中');
      assert(s.events.some(e => /交给 anthropic\/claude-sonnet-4-6 执行，思考强度 中/.test(e.text)));
    });

    await check('Asking another model to review walks model then the review in flight', async () => {
      await scene('plan');
      await click('[data-act="plan-review"]');
      let s = await snap();
      assert.equal(s.dockPick, 'review-models');
      const picker = page.locator('[data-testid="plan-review-models"]');
      assert.match(await picker.textContent(), /用哪个模型评审这份计划？/);
      assert(!(await picker.textContent()).includes('worktree'), 'a review does not touch the tree: no handoff hint');
      assert.equal(await picker.locator('[data-testid="plan-review-model"]').count(), 3);
      assert.match(await picker.locator('[data-testid="plan-review-model"]').first().textContent(), /https:\/\/api\.anthropic\.com\/v1/);

      await click('[data-testid="plan-review-model"][data-v="openai/gpt-5.5"]');
      s = await snap();
      assert.equal(s.dockPick, 'review-running');
      assert.equal(s.reviewModel, 'openai/gpt-5.5');
      const runLine = page.locator('[data-testid="exit-plan-review-running"]');
      assert.match(await runLine.textContent(), /openai\/gpt-5\.5 正在评审这份计划…/);
      assert.equal(await runLine.locator('[data-act="stop-review"]').count(), 1);
      assert.match(await runLine.locator('[data-act="stop-review"]').textContent(), /停止/);
      // The review's own progress is the subagent card in the thread, not the dock.
      assert.equal(await page.locator('[data-comp="SubagentCallCard"]').count(), 1);
      await click('[data-act="stop-review"]');
      assert.equal((await snap()).dockPick, null, 'stopping folds the line back to the four exits');
      assert.equal(await page.locator('[data-act="plan-approve"]').count(), 1);
    });

    await check('The completed review leaves its verdict line above the four exits', async () => {
      await scene('review-verdict');
      const verdict = page.locator('[data-testid="exit-plan-review-verdict"]');
      assert.equal(await verdict.count(), 1);
      assert.match(await verdict.textContent(), /anthropic\/claude-sonnet-4-6 的评审结论/);
      assert.match(await verdict.textContent(), /Approve with changes/);
      assert.equal(await page.locator('[data-act="plan-approve"]').count(), 1, 'the exits are back under the verdict');
      for (const id of ['handoff-models', 'handoff-effort', 'review-models', 'review-running', 'review-verdict']) {
        await scene(id);
        assert.equal(await page.locator('[data-comp="PlanApprovalDock"]').count(), 1, id + ' draws the dock');
        assert.equal(await page.locator('[data-act="plan-approve"]').count() + await page.locator('[data-testid="exit-plan-effort"]').count() + await page.locator('[data-testid="exit-plan-review-running"]').count() + await page.locator('[data-testid="exit-plan-hand-off-models"]').count() + await page.locator('[data-testid="plan-review-models"]').count(), 1, id + ' shows exactly one dock state');
      }
    });

    await check('Subagent and peer drawers open, tab-count and close', async () => {
      await scene('subagent');
      assert.equal((await snap()).drawer, 'subagent');
      assert.equal(await page.locator('[data-comp="SubagentTab"]').count(), 2);
      assert.match(await text('[data-comp="SubagentDrawer"]'), /交给它的任务/);
      assert.match(await text('[data-comp="SubagentDrawer"]'), /尚未给出结论/);
      await page.keyboard.press('Escape');
      await frame();
      assert.equal((await snap()).drawer, null);
      await scene('peer');
      assert.match(await text('[data-comp="PeerDrawer"]'), /正在编辑的文件/);
      assert.match(await text('[data-comp="PeerHeldBanner"]'), /在等你放行/);
      await page.keyboard.press('Escape');
      await frame();
      assert.equal((await snap()).drawer, null, 'the drawer closes and leaves the banner answerable');
      await click('[data-act="peer-release"]');
      assert((await snap()).events.some(e => /放行 vscode-workbench 的来信/.test(e.text)));
    });

    await check('Composer offers / commands, @ files, the plus menu and attachments', async () => {
      await scene('slash');
      assert.equal(await page.locator('[data-comp="SlashPickerCard"] .pk-row').count(), 6);
      assert.match(await text('[data-comp="SlashPickerCard"]'), /plan-gated-bugfix/);
      assert.equal(await page.locator('[data-act="show-at"]').count(), 1);
      await click('[data-act="show-at"]');
      assert.equal((await snap()).atOpen, true);
      assert.equal(await page.locator('[data-comp="ForebrainPromptTextarea"] .pk-row').count(), 4, '@ lists files');
      await click('[data-act="show-slash"]');
      assert.equal(await page.locator('[data-comp="SlashPickerCard"] .pk-row').count(), 6, '/ is back');
      assert.equal(await page.locator('[data-comp="UserAttachments"]').count() >= 1, true);
      await click('[data-act="plus"]');
      assert.match(await text('[data-comp="ComposerPlusMenu"]'), /引用文件/);
      await click('[data-act="open-attach"]');
      assert.equal((await snap()).dialog, 'attach');
      assert.match(await text('[data-comp="AttachSaveDialog"]'), /保存并上传/);
      await page.keyboard.press('Escape');
      await frame();
      for (const chip of ['ModeChip', 'ApprovalPresetPicker', 'ViewModeChip', 'ModelChip', 'EffortChip', 'ContextRing']) {
        assert.equal(await page.locator(`[data-comp="${chip}"]`).count() >= 1, true, chip);
      }
    });

    await check('The approval preset reads Chinese, and the default is the one selected', async () => {
      // locales/index.ts names the tier 「工作区写入」 in zh (「完全访问」 for the
      // other one); the prototype is a Chinese surface, so the English words
      // must not survive anywhere in it.
      await scene('thread');
      const chip = page.locator('[data-comp="ApprovalPresetPicker"]');
      assert.equal((await chip.textContent()).trim(), '工作区写入');
      await chip.click();
      await frame();
      // Concrete presets only: the default is the selected row and the chip's
      // own words, never a third "follow the default" option (owner 2026-11-26).
      const rows = page.locator('[data-testid="approval-picker-menu"] .pk-row');
      assert.equal(await rows.count(), 2, 'the menu lists the two presets and nothing else');
      const rowText = await rows.allTextContents().then(all => all.map(t => t.replace(/\s+/g, ' ').trim()));
      assert.match(rowText[0], /^工作区写入可以读写当前工作区的文件并运行命令；修改工作区外的文件需要审批。/, 'the default tier names itself first');
      assert(!/当前$/.test(rowText[0]), 'the selected tier is marked by highlight, not a trailing 「当前」 label');
      assert.equal(await rows.nth(0).locator('.pchk').count(), 0, 'no redundant check');
      assert.equal(await rows.nth(0).evaluate(el => el.classList.contains('on')), true, 'the default tier is the highlighted one');
      assert.match(rowText[1], /^完全访问可以修改工作区外的文件并访问互联网，无需审批。谨慎使用。/, 'full access keeps its own words');
      assert.equal(await page.locator('[data-testid="approval-option-default"]').count(), 0, 'no follow-default row');
      assert.equal((await snap()).presetMenu, true);
      await click('[data-testid="approval-option-full_access"]');
      let s = await snap();
      assert.equal(s.preset, 'full_access');
      assert.equal(s.presetMenu, false);
      assert.equal((await chip.textContent()).trim(), '完全访问');
      assert(s.events.some(e => /本会话的审批模式：完全访问/.test(e.text)));
      for (const id of SCENES) {
        await scene(id);
        const surface = await text('#vsc');
        assert(!surface.includes('Workspace write'), id + ' still prints the English preset');
        assert(!surface.includes('跟随默认'), id + ' still offers a follow-default option');
      }
      await scene('thread');
    });

    await check('Every model dropdown searches; deliverables open in VS Code', async () => {
      // Composer model menu: the header is 「模型」 (no 「本会话」), a search box narrows rows.
      await scene('slash');
      await click('[data-act="model"]');
      await frame();
      assert.equal((await page.locator('[data-comp="ModelChip"] .picker-h').textContent()).trim(), '模型');
      const mf = page.locator('[data-testid="model-filter"]');
      assert.equal(await mf.count(), 1, 'the composer model menu carries a search box');
      await mf.fill('glm');
      await frame();
      assert.equal(await page.locator('[data-comp="ModelChip"] .pk-row').count(), 1, 'the filter narrows to glm');
      assert.match(await page.locator('[data-comp="ModelChip"] .pk-row').textContent(), /glm-5.3/);
      await mf.fill('');
      await frame();
      await page.keyboard.press('Escape');
      await frame();
      // The handoff picker carries the same search box.
      await scene('plan');
      await click('[data-act="plan-handoff"]');
      await frame();
      const df = page.locator('[data-testid="dock-model-filter"]');
      assert.equal(await df.count(), 1, 'the handoff picker carries a search box');
      // The search box and the model rows share the same horizontal bounds.
      const sb = await page.locator('.xp-pick .picker-search').boundingBox();
      const rb = await page.locator('[data-testid="exit-plan-hand-off-model"]').first().boundingBox();
      assert(Math.abs(sb.x - rb.x) < 1 && Math.abs(sb.width - rb.width) < 1, 'the search box aligns with the model rows');
      await df.fill('openai');
      await frame();
      assert.equal(await page.locator('[data-testid="exit-plan-hand-off-model"]').count(), 1);
      await df.fill('');
      await frame();
      // A code deliverable's eye opens SCM + the editor diff, unchanged regions collapsed.
      await scene('thread');
      await click('[data-act="open-diff"][data-v="editors/vscode/package.json"]');
      await frame();
      let s = await snap();
      assert.equal(s.scmOpen, true);
      assert.equal(s.editorTab, 'diff');
      assert.equal(await page.locator('.scm').count(), 1, 'the source control panel opens');
      assert.equal(await page.locator('[data-testid="editor-tab-diff"]').count(), 1);
      assert.equal(await page.locator('[data-testid="diff-collapse-toggle"]').getAttribute('aria-pressed'), 'true');
      assert.equal(await page.locator('.ed-diff .diff-fold').count() >= 1, true, 'unchanged regions collapse');
      await click('[data-testid="diff-collapse-toggle"]');
      await frame();
      assert.equal(await page.locator('.ed-diff .diff-fold').count(), 0, 'the toggle expands unchanged regions');
      // A non-code deliverable goes to the OS default app instead of a diff.
      await click('[data-act="open-external"][data-v="docs/auth-review.pdf"]');
      await frame();
      assert((await snap()).events.some(e => /交给系统默认应用打开 docs\/auth-review\.pdf/.test(e.text)));
    });

    await check('Sessions searches, counts and switches the conversation', async () => {
      await scene('sessions');
      let s = await snap();
      assert.equal(s.sessions.length, 6);
      assert.match(await text('.sess-f'), /6 个对话/);
      assert.equal(await page.locator('.sess-row.on').count(), 1, 'one selected row');
      await click('.sess-row[data-v="s-588"]');
      s = await snap();
      assert.equal(s.selected, 's-588');
      assert.match(s.title, /批量任务卡片头部要写命令名/);
      await page.locator('#sess-search').fill('deck');
      await frame();
      s = await snap();
      assert.equal(s.search, 'deck');
      assert.deepEqual(s.sessions, ['s-601']);
      assert.match(await text('.sess-list'), /季度报表模板里的公式对不上/);
      assert.equal(await page.locator('.sess-row').count(), 1);
      await page.locator('#sess-search').fill('zzz');
      await frame();
      assert.match(await text('.sess-list'), /没有匹配的对话/);
    });

    await check('Empty state names the project and offers the four starters', async () => {
      await scene('empty');
      assert.equal(await page.locator('.starter').count(), 4);
      assert.match(await text('[data-comp="ProjectPill"]'), /forebrain-harness/);
      assert.match(await text('.empty-hero'), /要说点什么/);
      await click('[data-comp="ProjectPill"]');
      assert.equal((await snap()).dialog, 'folder', 'the pill browses folders');
      assert.match(await text('[data-comp="FolderDialog"]'), /选择文件夹/);
      await click('[data-act="close-folder"]');
      assert.equal((await snap()).dialog, null);
    });

    await check('Host boundary draws the three loader states, the untrusted strip and the RPC whitelist', async () => {
      await scene('api');
      assert.equal(await page.locator('.hostcard').count(), 3);
      assert.match(await text('.hostcard'), /网关 token/);
      assert.match((await page.locator('.hostcard').allTextContents()).join(' '), /重新连接/);
      assert.equal(await page.locator('.untrusted .ph').count(), 3);
      assert.match(await text('.untrusted'), /工作区未受信任/);
      assert.equal(await page.locator('.rpc-row').count(), 9);
      assert.match(await text('.rpc'), /askTrust/);
      assert.match(await text('.rpc'), /Bearer 只在宿主/);
    });

    await check('Fallback puts the same app in the activity bar container', async () => {
      await scene('fallback');
      assert.equal(await page.locator('.sec').count(), 0, 'no secondary sidebar in the fallback');
      // Layout can lag a frame behind the innerHTML swap under screenshot load;
      // poll briefly for a non-zero width instead of racing a single rAF.
      let width = 0;
      for (let i = 0; i < 20 && width === 0; i++) {
        width = await page.locator('.side').evaluate(el => el.getBoundingClientRect().width);
        if (width === 0) await frame();
      }
      assert(width <= 430 && width >= 400, 'the fallback is a narrow activity-bar view, got ' + width);
      assert.match(await text('.vsc-status'), /活动栏容器/);
      assert.equal(await page.locator('[data-comp="ChatWorkbench"]').count(), 1);
    });

    await check('Light and dark are real token swaps on the webview surface', async () => {
      await scene('thread');
      const lightChat = await bg('.sec');
      const lightInk = await page.locator('.md').first().evaluate(el => getComputedStyle(el).color);
      assert.equal(lightChat, 'rgb(255, 255, 255)', 'the flat white surface');
      await page.evaluate(() => window.chatWorkbenchPrototype.setTheme('dark'));
      await frame();
      const darkChat = await bg('.sec');
      const darkInk = await page.locator('.md').first().evaluate(el => getComputedStyle(el).color);
      assert.notEqual(darkChat, lightChat);
      assert.notEqual(darkInk, lightInk);
      const lum = c => Number(c.match(/\d+/g)[0]);
      assert(lum(darkChat) < 60, 'dark surface stays dark: ' + darkChat);
      assert(lum(darkInk) > 180, 'dark ink stays readable: ' + darkInk);
      assert.equal(await page.evaluate(() => document.getElementById('vsc').classList.contains('dark')), true);
      assert.equal(await page.evaluate(() => document.body.classList.contains('dark')), false, 'dark is the webview only: the page around it stays light');
      assert.equal(await page.evaluate(() => getComputedStyle(document.body).backgroundColor), 'rgb(243, 244, 246)', 'the page around the window never turns dark');
      await shot('391-shot-dark-thread.png');
      await page.evaluate(() => window.chatWorkbenchPrototype.setTheme('light'));
      await frame();
      assert.equal(await bg('.sec'), lightChat);
    });

    await check('A brand scheme recolours the chat accents and the sidebar in unison', async () => {
      // main.css 80-82: the schemes are the accent family of the whole app, so
      // switching must recolour the content area and the rail together. The
      // three hex values below are the ones main.css declares.
      const BRAND_RGB = { navy: 'rgb(31, 74, 158)', teal: 'rgb(14, 110, 126)', graphite: 'rgb(28, 28, 31)' };
      await scene('thread');
      const seen = { sidebar: {}, chatInk: {}, chatButton: {}, chatLink: {}, chatSpinner: {} };
      for (const brand of ['navy', 'teal', 'graphite']) {
        await page.evaluate(b => window.chatWorkbenchPrototype.setBrand(b), brand);
        await frame();
        assert.equal((await snap()).brand, brand);
        seen.sidebar[brand] = await page.locator('.sess-row.on .dot').evaluate(el => getComputedStyle(el).backgroundColor);
        seen.chatInk[brand] = await page.locator('.mention').first().evaluate(el => getComputedStyle(el).color);
        seen.chatButton[brand] = await page.locator('[data-comp="ApprovalCard"] .btn.primary').evaluate(el => getComputedStyle(el).backgroundColor);
        seen.chatLink[brand] = await page.locator('.act-step .lbl .link').first().evaluate(el => getComputedStyle(el).color);
        seen.chatSpinner[brand] = await page.locator('.spin').first().evaluate(el => getComputedStyle(el).borderTopColor);
        for (const part of ['sidebar', 'chatInk', 'chatButton', 'chatLink', 'chatSpinner']) {
          assert.equal(seen[part][brand], BRAND_RGB[brand], `${part} must take ${brand} from the token family`);
        }
      }
      assert.equal(new Set(Object.values(seen.chatInk)).size, 3, 'three schemes read as three accents: ' + JSON.stringify(seen.chatInk));
      assert.equal(new Set(Object.values(seen.sidebar)).size, 3, 'the sidebar follows the same three');
      // The page around the window is not part of the scheme or of dark mode.
      assert.equal(await page.evaluate(() => getComputedStyle(document.body).backgroundColor), 'rgb(243, 244, 246)');
      await page.evaluate(() => window.chatWorkbenchPrototype.setBrand('teal'));
      await frame();
      await shot('391-shot-teal-thread.png');
      await page.evaluate(() => window.chatWorkbenchPrototype.setBrand('graphite'));
      await frame();
      await shot('391-shot-graphite-thread.png');
      await scene('sessions');
      await shot('391-shot-graphite-sessions.png');
      await page.evaluate(() => window.chatWorkbenchPrototype.setBrand('teal'));
      await frame();
      await shot('391-shot-teal-sessions.png');
      await page.evaluate(() => window.chatWorkbenchPrototype.setBrand('navy'));
      await frame();
    });

    await check('Dark keeps every scene, and the accent family still reaches both sides', async () => {
      await page.evaluate(() => window.chatWorkbenchPrototype.setTheme('dark'));
      await frame();
      for (const id of ['thread', 'runtime', 'consent', 'sessions', 'api']) {
        await scene(id);
        assert((await text('#vsc')).length > 40, id + ' renders in dark');
      }
      // The dark set lightens the family; the chat and the sidebar still read
      // the same value, which is what "in unison" has to mean in both themes.
      await page.evaluate(() => window.chatWorkbenchPrototype.setBrand('teal'));
      await frame();
      await scene('thread');
      const darkChat = await page.locator('.mention').first().evaluate(el => getComputedStyle(el).color);
      const darkSide = await page.locator('.sess-row.on .dot').evaluate(el => getComputedStyle(el).backgroundColor);
      assert.equal(darkChat, 'rgb(79, 179, 194)', 'dark teal is not the light value: ' + darkChat);
      assert.equal(darkSide, darkChat, 'the sidebar takes the same dark accent');
      await scene('runtime');
      await shot('391-shot-dark-runtime.png');
      await page.evaluate(() => window.chatWorkbenchPrototype.setBrand('navy'));
      await page.evaluate(() => window.chatWorkbenchPrototype.setTheme('light'));
      await frame();
      assert.deepEqual(errors, [], 'Browser console errors');
    });

    if (shots) {
      for (const id of SCENES) {
        await scene(id);
        await shot(`391-shot-light-${id}.png`);
      }
      await scene('thread');
    }

    console.log(JSON.stringify({ passed: results.length, results, browserErrors: errors }, null, 2));
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });