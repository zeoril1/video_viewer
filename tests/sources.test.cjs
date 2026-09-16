const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const app = fs.readFileSync(path.join(__dirname, '../web/film.js'), 'utf8');
const source = app.slice(app.indexOf('let sourcesRequest = null;'), app.indexOf('function updateSourceLabels'));

function setup() {
  const pending = [];
  const rendered = [];
  const ctx = vm.createContext({
    AbortController, Date, currentItem: { id: 'a' }, relPref: {}, voicePref: {}, filmSeasonEps: {}, seriesEpisodes: {},
    sourcesRelWrap: {}, sourcesEl: {}, sourcesEmpty: {}, sourcesTitle: {}, sourcesSeasonWrap: {}, sourcesAudioWrap: {}, sourcesEpisodesWrap: {},
    t: s => s, dbg() {}, syncWatchBtn() {}, sleep: async () => {},
    renderSources: (items, id) => rendered.push(id),
    fetch: (url, options) => new Promise((resolve, reject) => pending.push({ url, options, resolve, reject })),
  });
  vm.runInContext(source, ctx);
  return { ctx, pending, rendered };
}
const ready = id => ({ ok: true, json: async () => ({ status: 'ready', items: [{ magnet: id }] }) });

test('late response from previous film cannot replace current sources', async () => {
  const { ctx, pending, rendered } = setup();
  const first = ctx.loadSources(ctx.currentItem);
  ctx.currentItem = { id: 'b' };
  const second = ctx.loadSources(ctx.currentItem);
  assert.equal(pending[0].options.signal.aborted, true);
  pending[1].resolve(ready('b'));
  await second;
  // Simulate a transport/JSON response that completes despite cancellation.
  pending[0].resolve(ready('a'));
  await first;
  assert.deepEqual(rendered, ['b']);
  assert.equal(ctx.lastSourceId, 'b');
});

test('closing the card ignores an in-flight response', async () => {
  const { ctx, pending, rendered } = setup();
  const task = ctx.loadSources(ctx.currentItem);
  ctx.cancelSources();
  ctx.currentItem = null;
  pending[0].resolve(ready('a'));
  await task;
  assert.deepEqual(rendered, []);
  assert.equal(pending[0].options.signal.aborted, true);
});

test('late failure does not overwrite the new card with an error', async () => {
  const { ctx, pending, rendered } = setup();
  const first = ctx.loadSources(ctx.currentItem);
  ctx.currentItem = { id: 'b' };
  const second = ctx.loadSources(ctx.currentItem);
  pending[1].resolve(ready('b')); await second;
  pending[0].reject(new Error('old request failed')); await first;
  assert.deepEqual(rendered, ['b']);
  assert.equal(ctx.sourcesEmpty.hidden, true);
});
