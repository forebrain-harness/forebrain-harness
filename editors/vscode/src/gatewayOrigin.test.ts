import test from 'node:test';
import assert from 'node:assert/strict';
import { ConfigError, parseGatewayOrigin } from './gatewayOrigin';

test('a bare origin is returned unchanged', () => {
  assert.equal(parseGatewayOrigin('http://127.0.0.1:6060'), 'http://127.0.0.1:6060');
});

test('a trailing slash is dropped', () => {
  assert.equal(parseGatewayOrigin('http://127.0.0.1:6060/'), 'http://127.0.0.1:6060');
});

test('a path is refused', () => {
  assert.throws(() => parseGatewayOrigin('http://127.0.0.1:6060/login'), ConfigError);
});

test('a query is refused', () => {
  assert.throws(() => parseGatewayOrigin('http://127.0.0.1:6060?x=1'), ConfigError);
});

test('a protocol that is not http(s) is refused', () => {
  assert.throws(() => parseGatewayOrigin('ftp://127.0.0.1'), ConfigError);
});

test('credentials in the URL are refused', () => {
  assert.throws(
    () => parseGatewayOrigin('http://user:pw@127.0.0.1:6060'),
    (err: unknown) => err instanceof ConfigError && err.message.includes('must not carry credentials'),
  );
});

test('a value that is not a URL is refused', () => {
  assert.throws(() => parseGatewayOrigin('not a url'), ConfigError);
});

test('a token pasted into the setting is not echoed back', () => {
  let message = '';
  try {
    parseGatewayOrigin('http://127.0.0.1:6060/login#token=SECRETTOKEN');
  } catch (err) {
    message = (err as Error).message;
  }
  assert.ok(message.includes('‹redacted›'), `message redacts the token: ${message}`);
  assert.ok(!message.includes('SECRETTOKEN'), 'the token itself never reaches the message');
});