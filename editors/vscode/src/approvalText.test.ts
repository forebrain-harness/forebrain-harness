import test from 'node:test';
import assert from 'node:assert/strict';
import { approvalDetail, approvalHeadline } from './approvalText';
import type { PendingApproval } from './sessionState';

function pending(fields: Partial<PendingApproval> = {}): PendingApproval {
  return { actionId: 'a1', runId: 'r1', kind: 'shell', toolName: 'shell', agent: '', permissionInput: undefined, ...fields };
}

test('the headline names the tool the gateway wants to run', () => {
  assert.equal(approvalHeadline(pending()), 'Forebrain: allow shell?');
});

test('with no tool to name, the headline asks for input and names the kind', () => {
  assert.equal(approvalHeadline(pending({ toolName: '' })), 'Forebrain needs your input (shell)');
});

test('the detail is the command, from a string or a {command}, and nothing else', () => {
  assert.equal(approvalDetail('ls -la'), 'ls -la');
  assert.equal(approvalDetail('x'.repeat(500)).length, 200, 'a long input is cut to one line of it');
  assert.equal(approvalDetail({ command: 'npm test', other: 1 }), 'npm test');
  assert.equal(approvalDetail({ path: '/etc/passwd' }), '', 'an input with no command shows nothing');
  assert.equal(approvalDetail(undefined), '');
  assert.equal(approvalDetail(42), '');
});