const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const shared = fs.readFileSync(path.join(__dirname, '../web/shared.js'), 'utf8');
const catalog = fs.readFileSync(path.join(__dirname, '../web/catalog.js'), 'utf8');
const ctx = vm.createContext({URL, encodeURIComponent, escapeHtml: s => String(s).replaceAll('&', '&amp;').replaceAll('"', '&quot;')});
vm.runInContext(shared.slice(shared.indexOf('function posterSrc('), shared.indexOf('/* Общий модуль')), ctx);
vm.runInContext(catalog.slice(catalog.indexOf('const TMDB_IMG'), catalog.indexOf('function makeCard(')), ctx);

test('poster requests use our origin but stored source URLs stay unchanged', () => {
  for (const url of ['https://image.tmdb.org/t/p/w500/abc.jpg', 'https://m.media-amazon.com/images/M/abc@._V1_.jpg']) {
    const local = ctx.posterSrc(url);
    assert.ok(local.startsWith('/api/poster?url='));
    assert.equal(new URL(local, 'https://kino.example').searchParams.get('url'), url);
    assert.equal(ctx.posterSrc(local), local);
  }
  assert.equal(ctx.posterSrc(''), '');
  assert.equal(ctx.posterSrc('https://other.example/poster.jpg'), 'https://other.example/poster.jpg');
});

test('adaptive poster sizes and fallback all use the proxy', () => {
  const html = ctx.posterImgHtml('https://image.tmdb.org/t/p/w500/abc.jpg');
  assert.match(html, /src="\/api\/poster\?url=/);
  assert.match(html, /w185%2Fabc.jpg 185w/);
  assert.doesNotMatch(html, /w200|src="https:/);
  assert.match(html, /data-poster-fallback="\/api\/poster/);
});

test('failed resize retries original once and successful load reveals image', () => {
  const events = {}, removed = [], classes = [];
  const img = {dataset: {posterFallback: '/api/poster?url=original'}, complete:false,
    addEventListener: (name, cb) => events[name] = cb,
    removeAttribute: name => removed.push(name), classList: {add: c => classes.push(c)}};
  ctx.markPosterLoaded({querySelector: () => img});
  events.error();
  assert.equal(img.src, '/api/poster?url=original');
  assert.deepEqual(removed, ['srcset','sizes']);
  events.error();
  assert.equal(removed.length, 2);
  events.load();
  assert.deepEqual(classes, ['loaded']);
});
