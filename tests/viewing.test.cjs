const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function fixture() {
  const handlers = {}, events = {}, requests = [], intervals = [], timeouts = [];
  let fetchImpl = async () => ({ ok: true });
  const state = { active: true, id: 'tt1234567', magnet: 'magnet:?xt=urn:btih:' + 'a'.repeat(40),
    file: 2, season: 1, episode: 3, position: 345, duration: 1200,
    stream_start: 300, track: 1, subs: -1, quality: 'source' };
  const video = { paused: false, ended: false, seeking: false, readyState: 4,
    addEventListener(name, fn) { (handlers[name] ||= []).push(fn); } };
  let sequence = 0, ready = true, visible = true;
  const context = vm.createContext({
    PP: { available: true, state: () => state, ready: () => ready, playing: () => visible,
      session: () => 'b'.repeat(32) },
    VV: { onAuth() {} },
    document: { getElementById: () => video, addEventListener(name, fn) { (events[name] ||= []).push(fn); } },
    window: { addEventListener(name, fn) { (events[name] ||= []).push(fn); } },
    crypto: { getRandomValues(array) { array.fill(++sequence); return array; } },
    fetch: async (url, options) => { requests.push({ url, ...options, body: JSON.parse(options.body) }); return fetchImpl(url, options); },
    AbortController,
    setTimeout(fn, delay) { const timer = { fn, delay, cleared: false }; timeouts.push(timer); return timer; },
    clearTimeout(timer) { if (timer) timer.cleared = true; },
    setInterval(fn) { intervals.push(fn); },
  });
  vm.runInContext(fs.readFileSync(path.join(__dirname, '../web/viewing.js'), 'utf8'), context);
  const emit = (name, target = events) => { for (const fn of target[name] || []) fn(); };
  return { state, video, requests, intervals, timeouts, emit, media: name => emit(name, handlers),
    setFetch: fn => { fetchImpl = fn; },
    setReady: value => { ready = value; }, setVisible: value => { visible = value; } };
}
const flush = () => new Promise(resolve => setImmediate(resolve));

test('viewing reports source position and excludes pauses, loading and seeking', async () => {
  const f = fixture();
  f.emit('playbackchange'); await flush();
  assert.equal(f.requests.at(-1).body.position, 345);
  assert.equal(f.requests.at(-1).body.playing, true);
  assert.equal(f.requests.at(-1).body.hls_session, 'b'.repeat(32));
  assert.equal(f.requests.at(-1).body.stream_start, 300);
  assert.equal(f.requests.at(-1).body.track, 1);
  assert.equal(f.requests.at(-1).body.subs, -1);
  assert.equal(f.requests.at(-1).body.quality, 'source');
  f.media('stalled'); await flush();
  assert.equal(f.requests.at(-1).body.playing, true);
  assert.equal('username' in f.requests.at(-1).body, false);
  f.media('waiting'); await flush();
  assert.equal(f.requests.at(-1).body.playing, false);
  f.media('playing'); await flush();
  assert.equal(f.requests.at(-1).body.playing, true);
  f.video.paused = true; f.media('pause'); await flush();
  assert.equal(f.requests.at(-1).body.playing, false);
  f.video.paused = false; f.video.seeking = true; f.media('seeking'); await flush();
  assert.equal(f.requests.at(-1).body.playing, false);
  f.video.seeking = false; f.media('seeked'); await flush();
  assert.equal(f.requests.at(-1).body.playing, true);
  f.setReady(false); f.intervals[0](); await flush();
  assert.equal(f.requests.at(-1).body.playing, false);
});

test('switching episodes ends old session; pagehide sends keepalive deletion', async () => {
  const f = fixture();
  f.emit('playbackchange'); await flush();
  const oldSession = f.requests[0].body.session;
  f.state.file = 4; f.state.episode = 4;
  f.emit('playbackchange'); await flush();
  assert.equal(f.requests[1].method, 'DELETE');
  assert.equal(f.requests[1].body.session, oldSession);
  assert.equal(f.requests[2].method, 'POST');
  assert.notEqual(f.requests[2].body.session, oldSession);
  f.emit('pagehide');
  assert.equal(f.requests.at(-1).method, 'DELETE');
  assert.equal(f.requests.at(-1).keepalive, true);
  const count = f.requests.length;
  f.intervals[0](); await flush();
  assert.equal(f.requests.length, count);
  f.emit('pageshow'); await flush();
  assert.equal(f.requests.at(-1).method, 'POST');
});

test('viewing permits automatic movie file and does not report inactive preloading', async () => {
  const f = fixture();
  f.state.active = false; f.emit('playbackchange'); await flush();
  assert.equal(f.requests.length, 0);
  f.state.active = true; f.state.file = -1; f.setVisible(false);
  f.emit('playbackchange'); await flush();
  assert.equal(f.requests[0].body.file, -1);
  assert.equal(f.requests[0].body.playing, false);
  f.state.active = false; f.emit('playbackchange'); await flush();
  assert.equal(f.requests.at(-1).method, 'DELETE');
});

test('a queued playback report cannot recreate a stopped session after pagehide', async () => {
  const f = fixture();
  f.emit('playbackchange');
  f.emit('pagehide');
  await flush();
  assert.deepEqual(f.requests.map(item => item.method), ['DELETE']);
});

test('slow heartbeats coalesce pending reports to the latest playback position', async () => {
  const f = fixture();
  let finish;
  f.setFetch(() => new Promise(resolve => { finish = resolve; }));
  f.emit('playbackchange'); await flush();
  for (let position = 350; position <= 500; position += 10) {
    f.state.position = position;
    f.intervals[0]();
  }
  assert.equal(f.requests.length, 1);
  f.setFetch(async () => ({ ok: true }));
  finish({ ok: true }); await flush();
  assert.equal(f.requests.length, 2);
  assert.equal(f.requests[1].body.position, 500);
});

test('a hung heartbeat cannot prevent renewal from the current playback state', async () => {
  const f = fixture();
  f.setFetch(() => new Promise(() => {}));
  f.emit('playbackchange'); await flush();
  f.state.position = 400;
  f.intervals[0]();
  f.setFetch(async () => ({ ok: true }));
  const timeout = f.timeouts.find(timer => !timer.cleared);
  assert.equal(timeout.delay, 8000);
  timeout.fn(); await flush();
  assert.equal(f.requests[0].signal.aborted, true);
  assert.equal(f.requests.at(-1).body.position, 400);
  assert.equal(f.requests.length, 2);
});
