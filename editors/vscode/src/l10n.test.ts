import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';

// Localization completeness: a host message added without a translation, or a
// manifest placeholder without an NLS entry, ships English to a zh-cn user.
// These checks fail on that drift instead of letting it reach the VSIX.
const root = join(__dirname, '..');
const manifest = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'));
const nlsDefault = JSON.parse(readFileSync(join(root, 'package.nls.json'), 'utf8'));
const nlsZh = JSON.parse(readFileSync(join(root, 'package.nls.zh-cn.json'), 'utf8'));
const bundleZh = JSON.parse(readFileSync(join(root, 'l10n/bundle.l10n.zh-cn.json'), 'utf8'));

function placeholders(value: unknown): string[] {
  if (typeof value === 'string') {
    const m = value.match(/^%([^%]+)%$/);
    return m ? [m[1]] : [];
  }
  if (Array.isArray(value)) return value.flatMap(placeholders);
  if (value && typeof value === 'object') return Object.values(value).flatMap(placeholders);
  return [];
}

test('every package.json %key% resolves in both NLS bundles', () => {
  const keys = placeholders(manifest);
  assert.ok(keys.length > 0, 'the manifest uses NLS placeholders');
  for (const key of keys) {
    assert.ok(key in nlsDefault, `package.nls.json missing ${key}`);
    assert.ok(key in nlsZh, `package.nls.zh-cn.json missing ${key}`);
  }
});

test('the two NLS bundles declare the same keys', () => {
  assert.deepEqual(Object.keys(nlsDefault).sort(), Object.keys(nlsZh).sort());
});

test('every l10n.t source string has a zh-cn translation', () => {
  const files = readdirSync(join(root, 'src')).filter((f) => f.endsWith('.ts') && !f.endsWith('.test.ts'));
  const missing: string[] = [];
  let count = 0;
  for (const file of files) {
    const text = readFileSync(join(root, 'src', file), 'utf8');
    for (const m of text.matchAll(/l10n\.t\(\s*(?:'([^']*)'|"([^"]*)")/g)) {
      const source = m[1] ?? m[2];
      count++;
      if (!(source in bundleZh)) missing.push(`${file}: ${source}`);
    }
  }
  assert.ok(count > 0, 'the host wraps messages in l10n.t');
  assert.deepEqual(missing, [], 'untranslated l10n strings');
});