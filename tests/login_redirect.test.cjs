const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../web/login.js'), 'utf8');

async function signIn(next, response = { ok: true, status: 200, json: async () => ({ user: { id: 7, username: 'tester' } }) }, detail = false) {
  const elements = new Map();
  const element = id => {
    if (!elements.has(id)) elements.set(id, { value: id === 'auth-username' ? 'tester' : 'secret123',
      classList: { toggle() {} }, handlers: {}, addEventListener(name, fn) { this.handlers[name] = fn; }, focus() {} });
    return elements.get(id);
  };
  const location = { origin: 'https://kinoteka.example', search: '?next=' + encodeURIComponent(next), href: '' };
  let invalidated = false, cached = undefined;
  const context = vm.createContext({ document: { getElementById: element }, location, URL, URLSearchParams,
    t: key => key, onLang() {}, applyLang() {}, initAuth() {},
    invalidateAuthChecks() { invalidated = true; }, cacheUser(user) { cached = user; },
    fetch: async () => response });
  vm.runInContext(source, context);
  await element('auth-form').handlers.submit({ preventDefault() {} });
  return detail ? { href: location.href, error: element('auth-error').textContent, disabled: element('auth-submit').disabled, invalidated, cached } : location.href;
}

test('successful login retains the local destination, query and fragment', async () => {
  assert.equal(await signIn('/film.html?id=tt1#player'), '/film.html?id=tt1#player');
});

test('login distinguishes a temporary outage from disabled authentication', async () => {
  for (const [code, key] of [['auth_unavailable', 'authErrorUnavailable'], ['auth_disabled', 'authErrorAuth']]) {
    const result = await signIn('/', { ok: false, status: 503, json: async () => ({ code }) }, true);
    assert.equal(result.error, key);
    assert.equal(result.href, '');
    assert.equal(result.disabled, false);
    assert.equal(result.invalidated, false);
  }
});

test('successful login invalidates previous identity checks and caches the new account', async () => {
  const result = await signIn('/', undefined, true);
  assert.equal(result.invalidated, true);
  assert.equal(result.cached.id, 7);
  assert.equal(result.cached.username, 'tester');
});

test('successful login rejects browser-normalized external destinations', async () => {
  for (const next of ['/\\evil.example', '//evil.example', '///evil.example', 'https://evil.example',
    '\\evil.example', '/\t/evil.example', '/\n/evil.example', '/\\kinoteka.example']) {
    assert.equal(await signIn(next), '/', JSON.stringify(next));
  }
});
