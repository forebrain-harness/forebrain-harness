import test from 'node:test';
import assert from 'node:assert/strict';
import { chmod, mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { findForebrainBinary, parseGatewayBanner, startGateway } from './gatewayProcess';

// dist-test/ sits next to src/, and esbuild bundles only the TypeScript entry
// points, so the fixture is run from the source tree.
const FIXTURE = path.resolve(__dirname, '..', 'src', 'testing', 'fixtures', 'fake-forebrain.js');

/** The banner `forebrain gateway start` prints when it generated a token. */
const GENERATED = [
  'Web UI       http://127.0.0.1:6060/',
  'Sign in      http://127.0.0.1:6060/login#token=abc123',
  'Token        abc123',
  'Listening on http://127.0.0.1:6060',
  '',
].join('\n');

/** The banner a gateway whose forebrain.yaml holds the token prints. */
const CONFIGURED = [
  'Web UI       http://127.0.0.1:6060/',
  'Auth         token',
  'Listening on http://127.0.0.1:6060',
  '',
].join('\n');

test('parseGatewayBanner reads the origin and the generated token', () => {
  assert.deepEqual(parseGatewayBanner(GENERATED), { origin: 'http://127.0.0.1:6060', token: 'abc123' });
});

test('parseGatewayBanner leaves the token undefined for a configured-token banner', () => {
  const banner = parseGatewayBanner(CONFIGURED);
  assert.equal(banner.origin, 'http://127.0.0.1:6060');
  assert.equal(banner.token, undefined);
});

test('a started gateway resolves with its token and never logs the lines that carry it', async (t) => {
  const lines: string[] = [];
  const gateway = await startGateway([process.execPath, FIXTURE, 'banner-with-token'], (line) => lines.push(line));
  t.after(() => gateway.stop());
  assert.equal(gateway.origin, 'http://127.0.0.1:6060');
  assert.equal(gateway.token, 'abc123');
  assert.ok(!lines.some((line) => line.includes('abc123')), `no token in the log: ${JSON.stringify(lines)}`);
  assert.ok(!lines.some((line) => line.includes('Sign in')), 'no sign-in link in the log');
  assert.ok(!lines.some((line) => line.startsWith('Token')), 'no Token line in the log');
  assert.ok(lines.includes('Listening on http://127.0.0.1:6060'), `the rest of the banner arrives: ${JSON.stringify(lines)}`);
});

test('a gateway that prints no Token line settles for a configured token', async (t) => {
  const gateway = await startGateway([process.execPath, FIXTURE, 'banner-configured'], () => {});
  t.after(() => gateway.stop());
  assert.equal(gateway.origin, 'http://127.0.0.1:6060');
  assert.equal(gateway.token, null);
});

test('a gateway that dies before Listening on rejects with its stderr', async () => {
  await assert.rejects(
    startGateway([process.execPath, FIXTURE, 'exit-with-error'], () => {}),
    /address already in use/,
  );
});

test('stop ends the gateway process', async () => {
  const gateway = await startGateway([process.execPath, FIXTURE, 'banner-with-token'], () => {});
  await gateway.stop();
  await gateway.stop();
  assert.throws(() => process.kill(gateway.pid, 0), (err: unknown) => (err as NodeJS.ErrnoException).code === 'ESRCH');
});

test('findForebrainBinary prefers the setting, else the first executable forebrain on PATH', async (t) => {
  const dir = await mkdtemp(path.join(tmpdir(), 'forebrain-path-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const empty = await mkdtemp(path.join(tmpdir(), 'forebrain-empty-'));
  t.after(() => rm(empty, { recursive: true, force: true }));

  assert.equal(await findForebrainBinary('', { PATH: empty }), undefined, 'nothing on PATH, nothing to start');

  const bin = path.join(dir, 'forebrain');
  await writeFile(bin, '#!/bin/sh\n');
  await chmod(bin, 0o755);
  assert.equal(await findForebrainBinary('', { PATH: dir }), bin);
  assert.equal(await findForebrainBinary(bin, { PATH: empty }), bin, 'the setting wins over PATH');

  const sub = path.join(dir, 'sub');
  await mkdir(sub, { recursive: true });
  const notExecutable = path.join(sub, 'forebrain');
  await writeFile(notExecutable, '#!/bin/sh\n');
  await chmod(notExecutable, 0o644);
  assert.equal(await findForebrainBinary(notExecutable, { PATH: empty }), undefined, 'a file that cannot run is not a binary');
});