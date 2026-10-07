const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const path = require("node:path");
function setup(preferences = {}) {
  const context = vm.createContext({
    AbortController,
    setTimeout,
    clearTimeout,
    VV: { user: { id: 1 } },
    window: { dispatchEvent() {} },
    document: {
      querySelector() {
        return null;
      },
    },
    Event: class {},
    fetch: async () => ({
      ok: true,
      status: 200,
      json: async () => ({
        items: [{ kind: "preferences", key: "playback", data: preferences }],
      }),
    }),
  });
  vm.runInContext(
    fs.readFileSync(path.join(__dirname, "../web/personal.js"), "utf8") +
      "\nglobalThis.personal=Personal;",
    context,
  );
  return context.personal;
}
test("source preferences exclude excessive size and quality and prefer requested voice", async () => {
  const p = setup({ voice: "LostFilm", max_quality: 1080, max_size_gb: 15 });
  await p.load();
  const ranked = p.rank([
    { magnet: "4k", title: "LostFilm 2160p", size: "10 GB", seeds: 50 },
    { magnet: "huge", title: "LostFilm 1080p", size: "20 GiB", seeds: 40 },
    { magnet: "other", title: "Other 1080p", size: "10 GB", seeds: 30 },
    { magnet: "wanted", title: "LostFilm 720p", size: "8 GB", seeds: 5 },
  ]);
  assert.deepEqual(
    Array.from(ranked, (x) => x.magnet),
    ["wanted", "other"],
  );
});
test("source limits support parsed quality, byte sizes and unknown metadata", async () => {
  const p = setup({ max_quality: 720, max_size_gb: 2 });
  await p.load();
  assert.equal(p.sizeGB({ size: "1073741824" }), 1);
  assert.equal(p.sizeGB({ size: "1024 MiB" }), 1);
  assert.equal(p.sizeGB({ size: "1,5 ГБ" }), 1.5);
  assert.equal(
    p.rank([{ title: "unknown", quality: "1080", size: "1 GB" }]).length,
    0,
  );
  assert.equal(p.rank([{ title: "unknown", size: "" }]).length, 1);
});
test("front-end JavaScript parses", () => {
  for (const name of fs
    .readdirSync(path.join(__dirname, "../web"))
    .filter((n) => n.endsWith(".js")))
    new vm.Script(
      fs.readFileSync(path.join(__dirname, "../web", name), "utf8"),
      { filename: name },
    );
});

test("pending personal writes and deletes cannot change the next account's preferences", async () => {
  const pending = [], changes = [];
  const context = vm.createContext({
    AbortController, setTimeout, clearTimeout, Event: class {},
    VV: { user: { id: 1 } }, document: { querySelector: () => null },
    window: { dispatchEvent: e => changes.push(e) },
    fetch: async (url, options) => {
      if (options && options.method) return new Promise(resolve => pending.push(() => resolve({ ok: true, status: 204 })));
      return { ok: true, status: 200, json: async () => ({ items: [{ kind: 'preferences', key: 'skip_segments', data: { intro: 'button' } }] }) };
    },
  });
  vm.runInContext(fs.readFileSync(path.join(__dirname, '../web/personal.js'), 'utf8') + '\nglobalThis.personal=Personal;', context);
  const put = context.personal.put('preferences', 'skip_segments', { intro: 'auto' });
  const remove = context.personal.remove('preferences', 'skip_segments');
  context.VV.user = { id: 2 };
  await context.personal.load();
  const count = changes.length;
  for (const complete of pending) complete();
  await Promise.all([put, remove]);
  assert.equal(context.personal.get('preferences', 'skip_segments').data.intro, 'button');
  assert.equal(changes.length, count);
});
