import test from 'node:test';
import assert from 'node:assert/strict';
import {
  newestSession,
  projectForFolder,
  resolveTargetSession,
  sessionFromRequestPath,
  sessionTitle,
  type TargetInputs,
} from './sessionTarget';

/** Every input the command would gather, with the ones a case does not name left unknown. */
function target(fields: Partial<TargetInputs> = {}): TargetInputs {
  return { setting: 'current', explicit: undefined, observed: undefined, recent: undefined, ...fields };
}

test('the always-new setting creates a session even when a good target exists', () => {
  assert.equal(resolveTargetSession(target({ setting: 'always-new', explicit: 'e', observed: 'o', recent: 'r' })), null);
});

test('the session the panel is showing wins over both fallbacks', () => {
  assert.equal(resolveTargetSession(target({ explicit: 'e', observed: 'o', recent: 'r' })), 'o');
  assert.equal(resolveTargetSession(target({ observed: 'o', recent: 'r' })), 'o');
});

test('with no session on screen, the one this extension last used wins over the newest', () => {
  assert.equal(resolveTargetSession(target({ explicit: 'e', recent: 'r' })), 'e');
});

test('with nothing else, the newest conversation of the project is the target', () => {
  assert.equal(resolveTargetSession(target({ recent: 'r' })), 'r');
});

test('nothing known anywhere means a new conversation', () => {
  assert.equal(resolveTargetSession(target()), null);
});

test('the panel session is read out of the transcript request, and only that request', () => {
  assert.equal(sessionFromRequestPath('GET', '/api/chat/sessions/abc-123/messages?limit=0'), 'abc-123');
  assert.equal(sessionFromRequestPath('GET', '/api/chat/sessions/s1/messages'), 's1');
  assert.equal(sessionFromRequestPath('POST', '/api/chat/sessions/s1/messages'), undefined, 'only a GET is a read');
  assert.equal(sessionFromRequestPath('GET', '/api/chat/sessions/s1/events'), undefined, 'the event stream is not the read');
  assert.equal(sessionFromRequestPath('GET', '/api/chat/sessions/s1/messages/'), undefined, 'a trailing slash is another path');
  assert.equal(sessionFromRequestPath('GET', `/api/chat/sessions/${'a'.repeat(129)}/messages`), undefined, 'an id of the wrong shape');
  assert.equal(sessionFromRequestPath('GET', '/api/chat/sessions/../messages'), undefined);
  assert.equal(sessionFromRequestPath('GET', '/api/chat/sessions/'), undefined);
});

test('a folder finds its project, a list answers its newest record, and a title names the file', () => {
  const projects = [
    { id: 'p1', root: '/ws/other' },
    { id: 'p2', root: '/ws/repo/' },
  ];
  assert.deepEqual(projectForFolder(projects, '/ws/repo'), { id: 'p2', root: '/ws/repo/' });
  assert.equal(projectForFolder(projects, '/ws'), undefined);
  assert.equal(projectForFolder(projects, '/ws/repo/sub'), undefined, 'a subfolder is not the project root');
  assert.equal(newestSession([{ id: 'newest' }, { id: 'older' }]), 'newest');
  assert.equal(newestSession([]), undefined);
  assert.equal(sessionTitle('a.ts'), 'VS Code: a.ts');
});