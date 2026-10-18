const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../web/source-identity.js'), 'utf8');
const magnet = 'magnet:?xt=urn:btih:' + 'a'.repeat(40) + '&tr=https://tracker.invalid/private';

function setup(results) {
  const requests = [], logs = [];
  const context = vm.createContext({ URL, AbortController, setTimeout, clearTimeout, dbg: message => logs.push(message),
    fetch: async (url, options) => {
      requests.push({ url, body: JSON.parse(options.body), signal: options.signal });
      const item = results.shift();
      const result = typeof item === 'function' ? await item() : item;
      if (result instanceof Error) throw result;
      return { ok: result?.http !== 503, status: result?.http || 200, json: async () => result };
    },
  });
  vm.runInContext(source + '\nthis.identity = SourceIdentity;', context);
  return { identity: context.identity, requests, logs };
}

test('confirmed wrong series is rejected, including changing tracker parameters', async () => {
  const h = setup([{ status: 'mismatch', title: 'Аватар короля 2' }]);
  const verdict = await h.identity.check({ id: 'tt35854736', magnet, title: 'Аватар короля 2' });
  assert.equal(verdict.status, 'mismatch');
  assert.equal(h.identity.isRejected('tt35854736', magnet.split('&tr=')[0]), true);
  assert.equal(h.identity.isRejected('other-film', magnet), false);
  assert.equal(h.requests.length, 1, 'a confirmed mismatch never opens torrent metadata');
});

test('legacy history with no cached title checks the actual torrent name', async () => {
  const h = setup([{ status: 'unknown' }, { torrent_title: 'Quan Zhi Gao Shou 2' }, { status: 'mismatch' }]);
  const verdict = await h.identity.check({ id: 'tt35854736', magnet });
  assert.equal(verdict.status, 'mismatch');
  assert.equal(h.requests.length, 3);
  assert.ok(h.requests[1].url.endsWith('/files'));
  assert.equal(h.requests[2].body.torrent_title, 'Quan Zhi Gao Shou 2');
  assert.equal(h.requests[2].body.title, undefined, 'a torrent folder name is weaker evidence than a release title');
  assert.ok(h.requests.every(request => !request.url.includes('private')), 'private magnet remains in POST bodies');
});

test('an uninformative legacy torrent folder stays unknown and does not reject playback', async () => {
  const h = setup([{ status: 'unknown' }, { torrent_title: 'Season 1' }, { status: 'unknown' }]);
  assert.equal((await h.identity.check({ id: 'tt1', magnet })).status, 'unknown');
  assert.equal(h.identity.isRejected('tt1', magnet), false);
});

test('known valid release starts without torrent lookup or search membership checks', async () => {
  const h = setup([{ status: 'matched' }]);
  assert.equal((await h.identity.check({ id: 'tt35854736', magnet, title: 'Avatar: Seven Havens' })).status, 'matched');
  assert.equal(h.requests.length, 1);
});

test('catalog outage is not treated as proof that saved playback is wrong', async () => {
  const h = setup([{ http: 503 }, new Error('offline')]);
  assert.equal((await h.identity.check({ id: 'tt1', magnet })).status, 'unknown');
  assert.equal((await h.identity.check({ id: 'tt1', magnet })).status, 'unknown');
  assert.equal(h.requests.length, 2, 'no metadata lookup when the catalog is down');
  assert.equal(h.identity.isRejected('tt1', magnet), false);
  assert.ok(h.logs.every(line => !line.includes('private')));
});

test('confirmed mismatch cannot be forgotten by an outage; a confirmed match can correct it', async () => {
  const h = setup([{ status: 'mismatch' }, new Error('offline'), { status: 'matched' }]);
  const candidate = { id: 'tt1', magnet, title: 'Release' };
  await h.identity.check(candidate);
  assert.equal((await h.identity.check(candidate)).status, 'mismatch');
  assert.equal((await h.identity.check(candidate)).status, 'matched');
  assert.equal(h.identity.isRejected('tt1', magnet), false);
});

test('a late cancelled verdict cannot overwrite the replacement playback identity', async () => {
  let resolve;
  const pending = new Promise(done => { resolve = done; });
  const h = setup([() => pending, { status: 'matched' }]);
  const controller = new AbortController();
  const first = h.identity.check({ id: 'tt1', magnet, title: 'Release', signal: controller.signal });
  controller.abort();
  assert.equal((await h.identity.check({ id: 'tt1', magnet, title: 'Release' })).status, 'matched');
  resolve({ status: 'mismatch' });
  assert.equal((await first).status, 'unknown');
  assert.equal(h.identity.isRejected('tt1', magnet), false);
});
