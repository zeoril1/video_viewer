const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../web/shared.js'), 'utf8');

function logger() {
  const panel = { hidden: true, textContent: '' };
  const context = vm.createContext({ location: { search: '' }, URLSearchParams,
    console: { log() {} }, document: { getElementById: () => panel } });
  vm.runInContext(source.slice(source.indexOf('const DEBUG ='), source.indexOf('// ---- Локализация')), context);
  return { context, panel };
}

test('fast prefetch retains current launch metadata and the first fatal failure in bounded diagnostics', () => {
  const { context, panel } = logger();
  context.dbg('tracks: id=tt1 video=h264/1080p');
  context.dbg('playHls: id=tt1 file=0 track=2 start=1139.51');
  context.dbg('hls.js: создаём поток v1.5.17');
  context.dbg('playback: выбранный источник | file=140 audio="Kravec"');
  context.dbg('player: кадры @ 600s | frames=14400 dropped=4');
  context.dbg('hls: ERROR type=mediaError details=bufferAppendError fatal=true');
  context.dbg('hls: причина: message=SourceBuffer error');
  context.dbg('hls: медиабуфер: mediaError=3 ranges=[0.000-268.900]');
  for (let i = 0; i < 500; i++) context.dbg('hls: фрагмент ' + i);
  context.showDebug();
  assert.match(panel.textContent, /video=h264\/1080p/);
  assert.match(panel.textContent, /start=1139\.51/);
  assert.match(panel.textContent, /v1\.5\.17/);
  assert.match(panel.textContent, /file=140 audio="Kravec"/);
  assert.match(panel.textContent, /frames=14400 dropped=4/);
  assert.match(panel.textContent, /bufferAppendError fatal=true/);
  assert.match(panel.textContent, /message=SourceBuffer error/);
  assert.match(panel.textContent, /mediaError=3 ranges=\[0\.000-268\.900\]/);
  assert.doesNotMatch(panel.textContent, /фрагмент 0\n/);
  assert.ok(panel.textContent.split('\n').length <= 215);
  context.dbg('playHls: id=tt1 file=0 track=2 start=1200');
  context.dbg('player: кадры @ 1200s | frames=28800 dropped=7');
  for (let i = 0; i < 250; i++) context.dbg('hls: фрагмент ' + i);
  context.renderDebug();
  assert.match(panel.textContent, /start=1200/);
  assert.doesNotMatch(panel.textContent, /start=1139\.51/);
  assert.match(panel.textContent, /frames=28800 dropped=7/);
  assert.doesNotMatch(panel.textContent, /frames=14400 dropped=4/);
  assert.match(panel.textContent, /bufferAppendError fatal=true/, 'a retry does not erase the original failure');
});

test('retained failures stay bounded while diagnostics is hidden', () => {
  const { context, panel } = logger();
  for (let i = 0; i < 50; i++) context.dbg('hls: ERROR type=mediaError details=bufferAppendError fatal=true attempt=' + i);
  for (let i = 0; i < 250; i++) context.dbg('hls: фрагмент ' + i);
  context.showDebug();
  assert.doesNotMatch(panel.textContent, /attempt=0\n/);
  assert.match(panel.textContent, /attempt=49/);
  assert.equal(panel.textContent.split('\n').length, 212);
});
