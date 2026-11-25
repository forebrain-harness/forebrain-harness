import test from 'node:test';
import assert from 'node:assert/strict';
import {
  EMPTY_SESSION_STATE,
  checklistProgress,
  reconcilePending,
  reduceSessionMessage,
  type SessionState,
} from './sessionState';

type Json = Record<string, unknown>;

/** One observed `run_event` frame, the way the gateway frames it. */
function event(type: string, fields: Json = {}): Json {
  return { op: 'run_event', data: { type, ...fields } };
}

function fold(...messages: readonly unknown[]): SessionState {
  return messages.reduce<SessionState>((state, msg) => reduceSessionMessage(state, msg), EMPTY_SESSION_STATE);
}

const APPROVAL: Json = {
  op: 'run_event',
  data: {
    type: 'approval_requested',
    run_id: 'r1',
    payload: {
      action_id: 'a1',
      action_kind: 'shell',
      agent_id: 'sub-1',
      permission_suggestion: { permission_tool_name: 'shell', permission_input: { command: 'ls' } },
    },
  },
};

test('a turn starts, and each of the three endings records its own run end', () => {
  const started = fold(event('turn_started', { run_id: 'r1' }));
  assert.equal(started.runActive, true);

  const completed = reduceSessionMessage(
    { ...started, lastRunEnd: { op: 'run_error', error: 'stale', planDone: 0, planTotal: 0 } },
    event('turn_completed', { run_id: 'r1', payload: { plan_done: 2, plan_total: 5 } }),
  );
  assert.equal(completed.runActive, false);
  assert.deepEqual(completed.lastRunEnd, { op: 'run_completed', error: '', planDone: 2, planTotal: 5 });

  const cancelled = reduceSessionMessage(completed, event('turn_cancelled', { run_id: 'r2' }));
  assert.deepEqual(cancelled.lastRunEnd, { op: 'run_cancelled', error: '', planDone: 0, planTotal: 0 });

  const errored = reduceSessionMessage(cancelled, event('turn_error', { run_id: 'r3', payload: { error: 'boom' } }));
  assert.deepEqual(errored.lastRunEnd, { op: 'run_error', error: 'boom', planDone: 0, planTotal: 0 });

  const failed = reduceSessionMessage({ ...started, toolsInFlight: 3 }, event('turn_failed', { run_id: 'r4' }));
  assert.equal(failed.runActive, false);
  assert.equal(failed.toolsInFlight, 0, 'a failed turn leaves no tool in flight');
});

test('tool calls are counted while they are in flight and never go below zero', () => {
  const both = fold(event('tool_call_started'), event('tool_call_started'), event('tool_call_completed'));
  assert.equal(both.toolsInFlight, 1);
  assert.equal(reduceSessionMessage(both, event('tool_call_completed')).toolsInFlight, 0);
  assert.equal(fold(event('tool_call_completed')).toolsInFlight, 0, 'a completion with no start stays at zero');
});

test('subagents are counted the same way', () => {
  const one = fold(event('subagent_spawned'));
  assert.equal(one.subagents, 1);
  assert.equal(reduceSessionMessage(one, event('subagent_ended')).subagents, 0);
  assert.equal(fold(event('subagent_ended')).subagents, 0);
});

test('a plan update replaces the list of its own agent, and the primary list stays first', () => {
  const sub = event('plan_updated', {
    run_id: 'r1',
    payload: {
      agent_id: 'sub-1',
      title: 'child plan',
      completed: 0,
      total: 1,
      items: [{ id: 'c1', content: 'child step', status: 'in_progress', active: 'child label' }],
    },
  });
  const primary = event('plan_updated', {
    run_id: 'r1',
    payload: {
      title: 'Fix the bug',
      explanation: 'why',
      completed: 1,
      total: 3,
      items: [
        { id: '1', content: 'read', status: 'completed', active: '' },
        { id: '2', content: 'write', status: 'in_progress', active: 'writing' },
      ],
    },
  });
  const state = fold(sub, primary);
  assert.deepEqual(
    state.checklists.map((c) => c.agent),
    ['primary', 'sub-1'],
    'the subagent list arriving first does not put it first',
  );
  assert.equal(state.checklists[0].title, 'Fix the bug');
  assert.equal(state.checklists[0].explanation, 'why');
  assert.deepEqual(state.checklists[0].items[1], {
    id: '2',
    content: 'write',
    status: 'in_progress',
    active: 'writing',
  });
  assert.equal(state.checklists[1].title, 'child plan');

  const replaced = reduceSessionMessage(
    state,
    event('plan_updated', { payload: { title: 'Fix the bug, again', completed: 2, total: 3 } }),
  );
  assert.equal(replaced.checklists.length, 2, 'a second update replaces, it does not append');
  assert.equal(replaced.checklists[0].title, 'Fix the bug, again');
  assert.deepEqual(replaced.checklists[0].items, [], 'the list is the payload, not a merge');
});

test('an approval event on the observer path lands in pending with its fields', () => {
  const state = fold(APPROVAL);
  assert.deepEqual(state.pending, [
    { actionId: 'a1', runId: 'r1', kind: 'shell', toolName: 'shell', agent: 'sub-1', permissionInput: { command: 'ls' } },
  ]);
});

test('a requires_action op lands in pending once, however often it arrives', () => {
  const requiresAction: Json = {
    op: 'requires_action',
    run_id: 'r1',
    message: 'allow shell?',
    data: { id: 'a2', kind: 'shell', action_id: 'a2', permission_suggestion: { permission_tool_name: 'shell' } },
  };
  const once = fold(requiresAction);
  const twice = reduceSessionMessage(once, requiresAction);
  assert.equal(twice, once, 'the second copy changes nothing, not even identity');
  assert.deepEqual(once.pending, [
    { actionId: 'a2', runId: 'r1', kind: 'shell', toolName: 'shell', agent: '', permissionInput: undefined },
  ]);
});

test('each of the three answers removes its approval and leaves the others alone', () => {
  const seeded = reduceSessionMessage(fold(APPROVAL), { op: 'requires_action', run_id: 'r1', data: { action_id: 'a2' } });
  assert.equal(seeded.pending.length, 2);
  for (const op of ['action_approved', 'action_denied', 'action_cancelled']) {
    const after = reduceSessionMessage(seeded, { op, data: { action_id: 'a1' } });
    assert.deepEqual(after.pending.map((p) => p.actionId), ['a2'], `${op} removes exactly its own`);
  }
  const unknown = reduceSessionMessage(seeded, { op: 'action_approved', data: { action_id: 'nope' } });
  assert.equal(unknown, seeded, 'an answer for an approval that never arrived changes nothing');
});

test('a turn ending with artifacts records the delivery the observer sees', () => {
  const state = fold(
    event('turn_completed', {
      run_id: 'r1',
      payload: {
        artifacts: [
          { path: '/ws/out/report.md', name: 'report.md', title: 'Report' },
          { path: '/ws/out/plot.png', name: 'plot.png' },
        ],
      },
    }),
  );
  assert.deepEqual(state.lastDelivery, {
    runId: 'r1',
    files: [
      { path: '/ws/out/report.md', title: 'Report' },
      { path: '/ws/out/plot.png', title: 'plot.png' },
    ],
  });
  const again = reduceSessionMessage(state, event('turn_completed', { run_id: 'r1', payload: { artifacts: [{}] } }));
  assert.equal(again.lastDelivery, state.lastDelivery, 'the same run is delivered once');
});

test('the initiator-only end ops record the run end and the delivery from the top level', () => {
  const completed = fold({
    op: 'run_completed',
    run_id: 'r9',
    plan_done: 3,
    plan_total: 4,
    artifacts: [{ path: '/ws/a.md', title: 'A' }],
  });
  assert.deepEqual(completed.lastRunEnd, { op: 'run_completed', error: '', planDone: 3, planTotal: 4 });
  assert.deepEqual(completed.lastDelivery, { runId: 'r9', files: [{ path: '/ws/a.md', title: 'A' }] });

  const cancelled = reduceSessionMessage(completed, { op: 'run_cancelled', run_id: 'r10' });
  assert.equal(cancelled.lastRunEnd?.op, 'run_cancelled');
  assert.equal(cancelled.lastDelivery, completed.lastDelivery, 'a run with no files does not clear the last one');

  const errored = reduceSessionMessage(cancelled, { op: 'run_error', run_id: 'r11', error: 'engine died' });
  assert.deepEqual(errored.lastRunEnd, { op: 'run_error', error: 'engine died', planDone: 0, planTotal: 0 });
  assert.equal(errored.runActive, false);
});

test('a turn ending voids every pending approval of that run', () => {
  const waiting = fold(APPROVAL);
  assert.equal(waiting.pending.length, 1);
  const ended = reduceSessionMessage(waiting, event('turn_completed', { run_id: 'r1' }));
  assert.deepEqual(ended.pending, []);
});

test('a withdrawn or refused turn is no longer running', () => {
  const running = fold(event('turn_started', { run_id: 'r1' }));
  for (const op of ['turn_withdrawn', 'turn_refused']) {
    const after = reduceSessionMessage(running, { op, error: 'session busy', data: { code: 'session_running' } });
    assert.equal(after.runActive, false, `${op} ends the turn state`);
  }
  assert.equal(reduceSessionMessage(EMPTY_SESSION_STATE, { op: 'turn_withdrawn' }), EMPTY_SESSION_STATE);
});

test('frames this reducer does not consume change nothing, and no field is ever required', () => {
  const state = fold(event('tool_call_started'));
  for (const msg of [
    { op: 'connected' },
    { op: 'session_bound', data: { cursor: 0, high_water: 4 } },
    { op: 'pong' },
    { op: 'run_warning', message: 'session_event_snapshot_unavailable' },
    { op: 'mode_changed', data: { mode: 'plan' } },
    event('mode_changed'),
    event('assistant_delta', { payload: { text: 'hi' } }),
    null,
    undefined,
    42,
    'nonsense',
    { op: 'run_event' },
    { op: 'run_event', data: 'not an object' },
    { op: 'plan_updated' },
  ]) {
    assert.equal(reduceSessionMessage(state, msg), state, `${JSON.stringify(msg)} changes nothing`);
  }
  assert.deepEqual(
    fold(event('plan_updated', { payload: {} })),
    { ...EMPTY_SESSION_STATE, checklists: [{ agent: 'primary', title: '', explanation: '', completed: 0, total: 0, items: [] }] },
    'a plan update with no fields at all still yields an empty primary checklist',
  );
});

test('reconcilePending drops the approvals the gateway no longer lists', () => {
  const seeded = reduceSessionMessage(fold(APPROVAL), { op: 'requires_action', run_id: 'r1', data: { action_id: 'a2' } });
  const reconciled = reconcilePending(seeded, ['a2']);
  assert.deepEqual(reconciled.pending.map((p) => p.actionId), ['a2']);
  assert.equal(reconcilePending(reconciled, ['a2']), reconciled, 'an unchanged reconcile keeps the object');
  assert.deepEqual(reconcilePending(reconciled, []).pending, []);
});

test('checklistProgress reads the primary checklist and names its shortest in-progress item', () => {
  assert.equal(checklistProgress(EMPTY_SESSION_STATE), null, 'no checklist, no progress');
  const state = fold(
    event('plan_updated', {
      payload: {
        agent_id: 'sub-1',
        completed: 9,
        total: 9,
        items: [{ id: 'c1', content: 'child step', status: 'in_progress' }],
      },
    }),
    event('plan_updated', {
      payload: {
        completed: 1,
        total: 4,
        items: [
          { id: '1', content: 'read the file', status: 'completed' },
          { id: '2', content: 'write a much longer label', status: 'in_progress' },
          { id: '3', content: 'short', status: 'in_progress', active: 'wl' },
        ],
      },
    }),
  );
  assert.deepEqual(checklistProgress(state), { done: 1, total: 4, active: 'wl' });
});