const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const shared = fs.readFileSync(path.join(__dirname, '../web/shared.js'), 'utf8');
const source = shared.slice(shared.indexOf('// ---- Авторизация и история'), shared.indexOf('// ---- Переходы между страницами'));
const turn = async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); };
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; };

function auth(initial = { id: 7, username: 'viewer', role: 'user' }) {
  const timers = new Map(), events = new Map(), store = new Map();
  if (initial) store.set('vv_user', JSON.stringify(initial));
  let next = 1;
  const logout = { addEventListener: (_, callback) => { events.set('logout', callback); } };
  const context = vm.createContext({
    authHooks: [], lang: 'ru', location: { pathname: '/', search: '' },
    localStorage: { getItem: key => store.get(key), setItem: (key, value) => store.set(key, value), removeItem: key => store.delete(key) },
    document: { getElementById: id => id === 'auth-logout' ? logout : null },
    window: { addEventListener: (event, callback) => events.set(event, callback) },
    AbortController, fetch: async () => ({ ok: true }), Math: Object.assign(Object.create(Math), { random: () => 1 }),
    setTimeout: (callback, delay) => { const id = next++; timers.set(id, { callback, delay }); return id; },
    clearTimeout: id => timers.delete(id),
  });
  vm.runInContext(source, context);
  return { context, timers, events, store, read: expression => vm.runInContext(expression, context),
    runRetry: async () => { assert.equal(timers.size, 1); const [id, timer] = timers.entries().next().value; timers.delete(id); timer.callback(); await vm.runInContext('authRequest', context); await turn(); } };
}

test('temporary auth failures preserve cached identity and history and coalesce retry requests', async () => {
  const a = auth();
  let requests = 0;
  const request = deferred();
  a.context.apiGet = async () => { requests++; return request.promise; };
  vm.runInContext("watchHistory = [{ film_id: 'tt1', position: 30 }]; episodeHistory.set('tt1', [{position:30}]);", a.context);
  const first = a.context.initAuth();
  assert.strictEqual(first, a.context.initAuth());
  request.resolve({ ok: false, status: 503, data: { code: 'auth_unavailable' } });
  await first;
  assert.equal(requests, 1);
  assert.equal(a.read('currentUser.username'), 'viewer');
  assert.equal(a.read('watchHistory[0].position'), 30);
  assert.equal(a.read("episodeHistory.get('tt1')[0].position"), 30);
  assert.equal(a.read('authDisabled'), false);
  assert.equal(a.read('authUnavailable'), true);
  assert.equal(a.timers.size, 1);
  assert.equal(a.timers.values().next().value.delay, 2000);
  a.context.apiGet = async () => null;
  await a.runRetry();
  assert.equal(a.timers.values().next().value.delay, 4000);
  for (let i = 0; i < 8; i++) await a.runRetry();
  assert.equal(a.timers.values().next().value.delay, 30000);
});

test('restored service refreshes history and ends the retry cycle', async () => {
  const a = auth();
  a.context.apiGet = async () => null;
  await a.context.initAuth();
  a.context.apiGet = async url => ({ ok: true, status: 200, data: url === '/api/auth/me'
    ? { user: { id: 7, username: 'viewer', role: 'user' } }
    : { items: [{ film_id: 'tt2', position: 42 }] } });
  await a.runRetry();
  assert.equal(a.read('watchHistory[0].film_id'), 'tt2');
  assert.equal(a.read('authUnavailable'), false);
  assert.equal(a.read('authRetryAttempt'), 0);
  assert.equal(a.timers.size, 0);
});

test('only confirmed unauthorized or disabled service clears the displayed user', async () => {
  for (const response of [{ ok: false, status: 401 }, { ok: false, status: 503, data: { code: 'auth_disabled' } }]) {
    const a = auth();
    a.context.apiGet = async () => response;
    await a.context.initAuth();
    assert.equal(a.read('currentUser'), null);
    assert.equal(a.store.has('vv_user'), false);
    assert.equal(a.timers.size, 0);
    assert.equal(a.read('authDisabled'), response.status === 503);
  }
});

test('a failed history refresh retains loaded data and schedules recovery', async () => {
  const a = auth();
  vm.runInContext("watchHistory = [{film_id:'tt1'}];", a.context);
  a.context.apiGet = async url => url === '/api/auth/me'
    ? { ok: true, data: { user: { id: 7, username: 'viewer' } } }
    : { ok: false, status: 503, data: { code: 'auth_unavailable' } };
  await a.context.initAuth();
  assert.equal(a.read('watchHistory[0].film_id'), 'tt1');
  assert.equal(a.read('currentUser.id'), 7);
  assert.equal(a.timers.size, 1);
});

test('logout aborts the pending identity check and ignores its late response', async () => {
  const a = auth();
  const request = deferred();
  let signal;
  a.context.apiGet = async (_, __, value) => { signal = value; return request.promise; };
  const check = a.context.initAuth();
  await a.events.get('logout')();
  assert.equal(signal.aborted, true);
  request.resolve({ ok: true, data: { user: { id: 7, username: 'viewer' } } });
  await check;
  assert.equal(a.read('currentUser'), null);
  assert.equal(a.timers.size, 0);
});

test('late history and episode results cannot overwrite a different account', async () => {
  const a = auth();
  vm.runInContext('authVerified = true;', a.context);
  const request = deferred();
  a.context.apiGet = async () => request.promise;
  const history = a.context.loadHistory(), episode = a.context.loadEpisodeHistory('tt1');
  vm.runInContext("invalidateAuthChecks(); currentUser = {id:8,username:'second'}; watchHistory=[{film_id:'new'}];", a.context);
  request.resolve({ ok: true, data: { items: [{ film_id: 'private-first-user' }] } });
  await Promise.all([history, episode]);
  assert.equal(a.read('watchHistory[0].film_id'), 'new');
  assert.equal(a.read('episodeHistory.size'), 0);
});

test('temporary episode-history errors retain the previous episode position', async () => {
  const a = auth();
  vm.runInContext("episodeHistory.set('tt1',[{position:25}]);", a.context);
  a.context.apiGet = async () => null;
  await a.context.loadEpisodeHistory('tt1');
  assert.equal(a.read("episodeHistory.get('tt1')[0].position"), 25);
  assert.equal(a.timers.size, 1);
  a.context.apiGet = async url => ({ ok: true, data: url === '/api/auth/me'
    ? { user: { id: 7, username: 'viewer' } } : { items: [{ position: 40 }] } });
  await a.runRetry();
  assert.equal(a.read("episodeHistory.get('tt1')[0].position"), 40);
  assert.equal(a.read('episodeHistoryPending.size'), 0);
  assert.equal(a.timers.size, 0);
});

test('logout in another tab invalidates pending checks without reauthenticating its old cookie', async () => {
  const a = auth();
  const request = deferred();
  let calls = 0;
  a.context.apiGet = async () => { calls++; return request.promise; };
  const pending = a.context.initAuth();
  a.events.get('storage')({ key: 'vv_user', newValue: null });
  request.resolve({ ok: true, data: { user: { id: 7, username: 'viewer' } } });
  await pending;
  assert.equal(a.read('currentUser'), null);
  assert.equal(calls, 1);
});

test('apiGet parses error codes and propagates cancellation to fetch', async () => {
  const a = auth();
  let signal;
  a.context.fetch = async (_, options) => { signal = options.signal; return { ok: false, status: 503, json: async () => ({ code: 'auth_disabled' }) }; };
  const result = await a.context.apiGet('/api/auth/me', 6000);
  assert.equal(result.data.code, 'auth_disabled');
  assert.equal(a.timers.size, 0);
  const controller = new AbortController();
  controller.abort();
  await a.context.apiGet('/api/auth/me', 6000, controller.signal);
  assert.equal(signal.aborted, true);
});

test('late history deletion cannot remove entries from another login, including the same account', async () => {
  for (const id of [8, 7]) {
    const a = auth();
    vm.runInContext("watchHistory=[{film_id:'tt1',position:25}];episodeHistory.set('tt1',[{position:25}]);", a.context);
    const request = deferred();
    a.context.fetch = () => request.promise;
    const pending = a.context.removeHistoryEntry('tt1');
    vm.runInContext(`invalidateAuthChecks();currentUser={id:${id}};watchHistory=[{film_id:'tt1',position:40}];episodeHistory.set('tt1',[{position:40}]);`, a.context);
    request.resolve({ ok: true, status: 204 });
    assert.equal(await pending, false);
    assert.equal(a.read('watchHistory[0].position'), 40);
    assert.equal(a.read("episodeHistory.get('tt1')[0].position"), 40);
  }
});

test('failed history deletion preserves loaded entries and recovery is scheduled', async () => {
  const a = auth();
  vm.runInContext("watchHistory=[{film_id:'tt1',position:25}];episodeHistory.set('tt1',[{position:25}]);", a.context);
  a.context.fetch = async () => ({ ok: false, status: 503 });
  assert.equal(await a.context.removeHistoryEntry('tt1'), false);
  assert.equal(a.read('watchHistory[0].position'), 25);
  assert.equal(a.read("episodeHistory.get('tt1')[0].position"), 25);
  assert.equal(a.timers.size, 1);
});

test('successful history deletion updates only the confirmed current account', async () => {
  const a = auth();
  vm.runInContext("watchHistory=[{film_id:'tt1'},{film_id:'tt2'}];episodeHistory.set('tt1',[{position:25}]);", a.context);
  a.context.fetch = async () => ({ ok: true, status: 204 });
  assert.equal(await a.context.removeHistoryEntry('tt1'), true);
  assert.equal(a.read('watchHistory.length'), 1);
  assert.equal(a.read('watchHistory[0].film_id'), 'tt2');
  assert.equal(a.read("episodeHistory.has('tt1')"), false);
});
