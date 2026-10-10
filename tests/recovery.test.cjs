const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
function harness(files) {
  const listeners = {},
    elements = [];
  class Element {
    constructor() {
      this.handlers = {};
      elements.push(this);
    }
    append() {}
    after() {}
    setAttribute() {}
    addEventListener(k, f) {
      this.handlers[k] = f;
    }
  }
  const video = new Element(),
    wrap = new Element();
  let follower = false, current = {
      id: "tt1",
      magnet: "old",
      file: 4,
      season: 2,
      episode: 3,
      voice: "LostFilm",
      position: 157,
      quality: "720",
    },
    started;
  const ctx = vm.createContext({
    document: {
      hidden: false,
      createElement: () => new Element(),
      getElementById: (id) => (id === "player" ? video : wrap),
    },
    window: { addEventListener: (k, f) => (listeners[k] = f) },
    PP: {
      available: true,
      isFollower: () => follower,
      playing: () => true,
      state: () => current,
      start: (o) => {
        started = o;
      },
    },
    Personal: { rank: (x) => x },
    AbortController,
    fetch: async () => ({
      ok: true,
      json: async () => ({
        items: [
          { magnet: "wrong", title: "Season" },
          { magnet: "good", title: "LostFilm" },
        ],
        status: "ready",
      }),
    }),
    fetchFiles: files,
    URLSearchParams,
    location: { search: "?id=tt1", pathname: "/film.html" },
    history: { state: { playback: 'id=tt1&magnet=old&season=2&ep=3&file=4&autoplay=1' }, replaceState(state, _, url) { this.state = state; ctx.location.search = new URL(url, 'http://localhost').search; } },
    sessionStorage: { getItem() {}, removeItem() {} },
    setInterval() {
      return 1;
    },
    clearInterval() {},
    setTimeout,
    Date,
    Set,
  });
  const shared = fs.readFileSync(path.join(__dirname, '../web/shared.js'), 'utf8');
  vm.runInContext(shared.slice(shared.indexOf('function playbackPageUrl('), shared.indexOf('// ---- ТВ-режим')), ctx);
  vm.runInContext(
    fs.readFileSync(path.join(__dirname, "../web/recovery.js"), "utf8"),
    ctx,
  );
  return {
    button: elements.at(-1),
    box: elements[2],
    listeners,
    get started() {
      return started;
    },
    get params() { return ctx.playbackPageParams(); },
    get search() { return ctx.location.search; },
    follow: () => {
      follower = true;
      listeners.playbackrole();
    },
    change: () => {
      current = { ...current, magnet: "manually-changed" };
      listeners.playbackstart();
    },
  };
}
test("recovery matches exact episode, retains position and voice, and uses source quality", async () => {
  const h = harness(async (id, magnet) =>
    magnet === "wrong"
      ? [{ index: 0, season: 2, episode: 4 }]
      : [{ index: 8, season: 2, episode: 3 }],
  );
  h.listeners.playbackfailure();
  await h.button.onclick();
  assert.equal(h.started.magnet, "good");
  assert.equal(h.started.file, 8);
  assert.equal(h.started.pos, 157);
  assert.equal(h.started.voice, "LostFilm");
  assert.equal(h.started.quality, "source");
  assert.equal(h.search, '?id=tt1');
  assert.equal(h.params.get('magnet'), 'good');
  assert.equal(h.params.get('file'), '8');
  assert.equal(h.params.get('season'), '2');
  assert.equal(h.params.get('ep'), '3');
  assert.equal(h.params.get('voice'), 'LostFilm');
  assert.equal(h.params.get('pos'), '157');
});
test("recovery never substitutes another episode", async () => {
  const h = harness(async () => [{ index: 0, season: 3, episode: 3 }]);
  await h.button.onclick();
  assert.equal(h.started, undefined);
});
test("manual source selection cancels in-flight recovery", async () => {
  let resolve;
  const pending = new Promise((r) => (resolve = r));
  const h = harness(() => pending);
  const request = h.button.onclick();
  await new Promise((r) => setImmediate(r));
  h.change();
  resolve([{ index: 8, season: 2, episode: 3 }]);
  await request;
  assert.equal(h.started, undefined);
});

test("room guests cannot see or start source recovery", async () => {
  let requested = false;
  const h = harness(async () => { requested = true; return [{ index: 8, season: 2, episode: 3 }]; });
  h.listeners.playbackfailure();
  assert.equal(h.box.hidden, false);
  h.follow();
  h.listeners.playbackfailure();
  h.listeners.requestsourcechange();
  await h.button.onclick();
  assert.equal(h.box.hidden, true);
  assert.equal(h.button.disabled, true);
  assert.equal(requested, false);
  assert.equal(h.started, undefined);
});

test("joining as a guest cancels recovery already waiting for file selection", async () => {
  let resolve;
  const pending = new Promise(r => { resolve = r; });
  const h = harness(() => pending);
  const request = h.button.onclick();
  await new Promise(r => setImmediate(r));
  h.follow();
  resolve([{ index: 8, season: 2, episode: 3 }]);
  await request;
  assert.equal(h.started, undefined);
  assert.equal(h.box.hidden, true);
  assert.equal(h.button.disabled, true);
});
