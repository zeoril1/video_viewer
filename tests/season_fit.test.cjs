// season_fit.test.cjs — проверка соответствия раздачи сезону по числу серий
// (releaseSeasonFit/canonicalByNumber) и порядка проб (probeOrder).
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const app = fs.readFileSync(path.join(__dirname, '../web/series.js'), 'utf8');
// seasonEpisodeCount … probeOrder — чистые функции без DOM (вырезаем из series.js).
const source = app.slice(app.indexOf('// seasonEpisodeCount'), app.indexOf('// ensureSeasonEpisodes'));

// Сезоны TMDB «Реальных пацанов» (tt1837341): 291 серия в 10 сезонах.
const realPatsany = { 1: 50, 2: 40, 3: 32, 4: 40, 5: 36, 6: 16, 7: 20, 8: 20, 9: 20, 10: 17 };

function setup() {
  const ctx = vm.createContext({ filmSeasonEps: { tt1837341: realPatsany }, currentItem: { tmdb_id: '46005' } });
  vm.runInContext(source, ctx);
  return ctx;
}

// files — список файлов раздачи (season/episode как в ответе сервера).
function files(season, episodes) {
  return episodes.map((ep, i) => ({ index: i, season, episode: ep }));
}

function counter(from, count) {
  const out = [];
  for (let i = 0; i < count; i++) out.push(from + i);
  return out;
}

test('сборник со сквозной нумерацией не считается раздачей сезона', () => {
  const ctx = setup();
  // Наивная раскладка сервера (список открыли без tmdb): 293 файла как
  // «1 сезон, серии 1..293» — серии убегают далеко за границы сезона.
  const fit = ctx.releaseSeasonFit(files(1, counter(1, 293)), 1, 50);
  assert.equal(fit.fits, false);
  assert.equal(fit.overflow, 243); // 293 - 50
  assert.equal(fit.count, 50);
  assert.equal(fit.full, false);
});

test('полная и частичная раздача сезона проходят проверку', () => {
  const ctx = setup();
  const full = ctx.releaseSeasonFit(files(2, counter(1, 40)), 2, 40);
  assert.deepEqual([full.fits, full.full, full.count], [true, true, 40]);
  // Пак «[S01-02x01-41]»: 40 серий из 50 — сезон покрыт частично, но без убегания.
  const partial = ctx.releaseSeasonFit(files(1, counter(1, 40)), 1, 50);
  assert.deepEqual([partial.fits, partial.full, partial.count], [true, false, 40]);
});

test('раздача чужого сезона не покрывает запрошенный', () => {
  const ctx = setup();
  const fit = ctx.releaseSeasonFit([...files(1, counter(1, 50)), ...files(3, counter(1, 32))], 2, 40);
  assert.equal(fit.count, 0);   // серии сезона 2 в раздаче отсутствуют
  assert.equal(fit.overflow, 0);
});

test('без структуры TMDB (want = 0) проверка ничего не отбрасывает', () => {
  const ctx = setup();
  const fit = ctx.releaseSeasonFit(files(1, counter(1, 293)), 1, 0);
  assert.equal(fit.fits, true);
  assert.equal(fit.count, 293);
});

test('probeOrder ставит подходящую раздачу впереди сборника', () => {
  const ctx = setup();
  const seasonPack = { magnet: 'pack', title: 'Реальные пацаны [S01] WEB-DL 1080p', season: 1 };
  const collection = { magnet: 'coll', title: 'Реальные пацаны / S1-14E1-291 of 291', season: 0 };
  // Раскладка сборника «убежала» (его открывали без tmdb), у пака — 40 из 50.
  ctx.rememberSeasonFit('coll', 1, ctx.releaseSeasonFit(files(1, counter(1, 293)), 1, 50));
  ctx.rememberSeasonFit('pack', 1, ctx.releaseSeasonFit(files(1, counter(1, 40)), 1, 50));
  assert.deepEqual(ctx.probeOrder([collection, seasonPack], 1).map((s) => s.magnet), ['pack', 'coll']);
  // Сборник, разложенный правильно (50 серий сезона 1), снова первый.
  ctx.rememberSeasonFit('coll', 1, ctx.releaseSeasonFit(files(1, counter(1, 50)), 1, 50));
  assert.deepEqual(ctx.probeOrder([collection, seasonPack], 1).map((s) => s.magnet), ['coll', 'pack']);
});

test('сквозной номер серии переводится в сезон/серию TMDB', () => {
  const ctx = setup();
  const at = (n) => ctx.canonicalByNumber('tt1837341', n);
  assert.deepEqual([at(1).season, at(1).episode], [1, 1]);
  assert.deepEqual([at(50).season, at(50).episode], [1, 50]);
  assert.deepEqual([at(51).season, at(51).episode], [2, 1]);
  // «Realnye.pacany.088.serya.mkv» — 88-я серия подряд, а не «1 сезон, 88 серия».
  assert.deepEqual([at(88).season, at(88).episode], [2, 38]);
  assert.deepEqual([at(291).season, at(291).episode], [10, 17]);
  assert.equal(at(292), null); // бонусный фильм сборника — вне структуры
  assert.equal(ctx.canonicalByNumber('unknown', 5), null);
});
