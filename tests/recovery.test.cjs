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
  let current = {
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
    history: { replaceState() {} },
    setInterval() {
      return 1;
    },
    clearInterval() {},
    setTimeout,
    Date,
    Set,
  });
  vm.runInContext(
    fs.readFileSync(path.join(__dirname, "../web/recovery.js"), "utf8"),
    ctx,
  );
  return {
    button: elements.at(-1),
    listeners,
    get started() {
      return started;
    },
    change: () => {
      current = { ...current, magnet: "manually-changed" };
      listeners.playbackstart();
    },
  };
}
test("recovery matches exact episode and retains position, voice and quality", async () => {
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
  assert.equal(h.started.quality, "720");
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
