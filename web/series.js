'use strict';

/* Логика работы с раздачами трекеров: какая раздача описывает сезон, какие в ней
 * озвучки, сколько серий сезона по TMDB, сопоставление озвучки с дорожкой файла.
 * Файл общий для страницы фильма (film.js) и страницы просмотра (watch.js). */

// Фильм/сериал, вокруг которого работает страница (id, kind, tmdb_id, seasons, ...).
let currentItem = null;

// Кэш структуры серий (id -> {bySeason, done, probing}): лениво из файлов раздач, живёт до конца сессии.
const seriesEpisodes = {};
// Каноническое число серий сезона (TMDB, из /sources): раздачи трекеров покрывают сезон
// частично, иначе в сетке было бы столько серий, сколько отдала первая открытая раздача.
const filmSeasonEps = {};
// Сколько раздач пробовать, пока не наберём сезон целиком (пак может покрывать его частично).
const episodeProbeLimit = 6;
// Ручной выбор раздачи по сезону (season -> magnet) — автозапуск продолжает играть её.
const relPref = {};
// Ручной выбор озвучки по сезону: по ней подбирается торрент сезона и дорожка файла.
const voicePref = {};

// seasonEpisodeCount — каноническое число серий сезона из TMDB (0 — неизвестно).
// ВАЖНО: текст «// seasonEpisodeCount» — маркер для tests/season_fit.test.cjs (indexOf), не удалять.
function seasonEpisodeCount(id, season) {
  const m = filmSeasonEps[id];
  return (m && season) ? (m[season] || 0) : 0;
}

// Сезон/серия по СКВОЗНОМУ номеру: так нумеруются сборники («088 serya» — 88-я подряд),
// раскладываем по структуре сезонов TMDB.
function canonicalByNumber(id, n) {
  const m = filmSeasonEps[id];
  if (!m || n <= 0) return null;
  let rest = n;
  const seasons = Object.keys(m).map(Number).filter((s) => s > 0).sort((a, b) => a - b);
  for (const s of seasons) {
    const cnt = m[s] || 0;
    if (cnt <= 0) continue;
    if (rest <= cnt) return { season: s, episode: rest };
    rest -= cnt;
  }
  return null;
}

// Итог проверки раздачи на соответствие сезону (magnet|сезон → fit) на время сессии:
// повторные пробы того же торрента ничего не переоткрывают.
const seasonFitCache = {};

function seasonFitKey(magnet, season) {
  return String(magnet || '') + '|' + (season || 0);
}

function knownSeasonFit(magnet, season) {
  return seasonFitCache[seasonFitKey(magnet, season)] || null;
}

function rememberSeasonFit(magnet, season, fit) {
  seasonFitCache[seasonFitKey(magnet, season)] = fit;
  return fit;
}

// Насколько раздача соответствует сезону TMDB (want серий): сколько серий сезона есть,
// покрыт ли он целиком и нет ли номеров > want. Номер больше want («1 сезон, 88 серия»
// при 50 сериях) значит, что раскладка не сошлась со структурой TMDB (список открыли
// без tmdb) либо это трекерный сезон с другой нарезкой — как «раздача сезона» не годится,
// иначе серия убегает за его предел.
function releaseSeasonFit(files, season, want) {
  const fit = { want: want || 0, count: 0, max: 0, overflow: 0, unknown: 0, full: false, fits: true };
  for (const f of files || []) {
    const ss = f.season || 0;
    if (season && ss !== season) continue;
    const ep = f.episode || 0;
    if (ep <= 0) {
      fit.unknown++;
      continue;
    }
    if (want && ep > want) {
      fit.overflow++;
      continue;
    }
    fit.count++;
    if (ep > fit.max) fit.max = ep;
  }
  if (fit.want) {
    fit.fits = fit.overflow === 0;
    fit.full = fit.fits && fit.count >= fit.want;
  }
  return fit;
}

// Порядок раздач по соответствию сезону: полная → частичная → «убегающая»; непроверенная — в середине.
function seasonFitRank(magnet, season) {
  const fit = knownSeasonFit(magnet, season);
  if (!fit || !fit.want) return 1;
  if (!fit.fits) return 0;
  return fit.full ? 3 : 2;
}

// Уйдёт ли запрос файлов с tmdb_id: без него сервер раскладывает серии «на глаз»
// (всё в первый сезон), и releaseSeasonFit нельзя считать достоверным.
function tmdbMappingOn() {
  return !!(currentItem && currentItem.tmdb_id);
}

// Последний сезон диапазона из заголовка («S1-14», «[01-03x01-71]»); 0 — нет диапазона.
// Показывает, сколько сезонов покрывает раздача (полный сборник против частичного пака).
function seasonRangeTo(title) {
  const t = String(title || '');
  let m = t.match(/[Ss](\d{1,2})\s*[-–]\s*[Ss]?(\d{1,2})/);
  if (!m) m = t.match(/\[(\d{1,2})\s*[-–]\s*(\d{1,2})\s*[xх]/);
  if (!m) return 0;
  const to = parseInt(m[2], 10);
  return isFinite(to) ? to : 0;
}

// Порядок раздач для пробы серий: проверенные и соответствующие сезону, затем с большим
// диапазоном сезонов (полный сборник), затем нужный сезон, затем прочие; внутри — живые, по сидам.
// Прежний порядок («точный сезон» и разрешение) ставил вперёд частичные паки и чужие фильмы,
// и сетка серий собиралась именно из них.
function probeOrder(pool, season) {
  const live = (s) => ((s.seeds || 0) > 0 ? 1 : 0);
  return pool.slice().sort((a, b) => {
    // Уже открытые — по соответствию сезону (releaseSeasonFit): «убегающие» в конец.
    const fa = seasonFitRank(a.magnet, season), fb = seasonFitRank(b.magnet, season);
    if (fa !== fb) return fb - fa;
    const ca = seasonRangeTo(a.title), cb = seasonRangeTo(b.title);
    if (ca !== cb) return cb - ca;
    const ea = (a.season || 0) === season ? 1 : 0;
    const eb = (b.season || 0) === season ? 1 : 0;
    if (ea !== eb) return eb - ea;
    const sa = (a.season || 0) === 0 ? 1 : 0;
    const sb = (b.season || 0) === 0 ? 1 : 0;
    if (sa !== sb) return sb - sa;
    if (live(a) !== live(b)) return live(b) - live(a);
    return (b.seeds || 0) - (a.seeds || 0);
  });
}

// ensureSeasonEpisodes — номера серий сезона: открывает раздачи (сначала полный сборник),
// пока не наберём столько же, сколько в TMDB (один пак может отдать не все).
// ВАЖНО: текст «// ensureSeasonEpisodes» — маркер для tests/season_fit.test.cjs (indexOf), не удалять.
async function ensureSeasonEpisodes(id, season, items) {
  if (!id) return;
  const cache = seriesEpisodes[id] || (seriesEpisodes[id] = { bySeason: {}, done: {}, probing: false });
  if (season && cache.done[season]) return;
  if (cache.probing) {
    if (cache._probe) await cache._probe;
    if (season && cache.done[season]) return;
    return ensureSeasonEpisodes(id, season, items);
  }
  cache.probing = true;
  const probe = (async () => {
    try {
      // Сначала раздачи с сидами: «мёртвых» заявителей озвучек открывать впустую — ждать таймаут.
      const pool = probeOrder(seasonReleaseSources(items, season), season);
      const cands = pool.filter((s) => (s.seeds || 0) > 0)
        .concat(pool.filter((s) => !((s.seeds || 0) > 0)));
      const fallback = items.filter((s) => s.magnet).slice(0, 3);
      const list = cands.length ? cands.slice(0, episodeProbeLimit) : (season ? [] : fallback);
      const want = seasonEpisodeCount(id, season);
      for (const src of list) {
        if (!src || !src.magnet) continue;
        // Раздачу уже отбраковали для этого сезона (номера убегают) — не переоткрываем.
        const known = knownSeasonFit(src.magnet, season);
        if (known && !known.fits) continue;
        const files = (await fetchFiles(id, src.magnet, src.title)) || [];
        if (!files || !files.length) continue;
        // У сезона из 50 серий файла с номером 88 быть не может — такая раздача сезон не
        // описывает, и сетка из неё строиться не должна. Верим итогу только при tmdbMappingOn():
        // без tmdb сервер раскладывает файлы «на глаз», и «убегание» врёт.
        const fit = releaseSeasonFit(files, season, want);
        const mapped = tmdbMappingOn();
        if (mapped) rememberSeasonFit(src.magnet, season, fit);
        if (want && mapped && !fit.fits) {
          dbg('серии: раздача «' + (src.title || '?') + '» не подходит сезону ' + season +
            ' (серий сезона ' + fit.count + ' из ' + want + ', за границей сезона ' + fit.overflow + ')');
          continue;
        }
        for (const f of files) {
          const fs = f.season || 0;
          const fe = f.episode || 0;
          if (fs <= 0 || fe <= 0) continue;
          // «Убежавшую» серию в сетку не берём.
          const wantFs = seasonEpisodeCount(id, fs);
          if (wantFs && fe > wantFs) continue;
          const bySeason = cache.bySeason[fs] || (cache.bySeason[fs] = []);
          if (!bySeason.includes(fe)) bySeason.push(fe);
        }
        if (!season) {
          if (Object.keys(cache.bySeason).length) break;
          continue;
        }
        const have = (cache.bySeason[season] || []).length;
        if (!have) continue;
        // Сезон покрыт не полностью — пробуем дальше: полный сборник отдаст остальные серии.
        if (want > have) continue;
        break;
      }
    } finally {
      cache.probing = false;
    }
  })();
  cache._probe = probe;
  await probe;
  if (season && cache.bySeason[season] && cache.bySeason[season].length) {
    cache.done[season] = true;
  }
}

// Файлы раздачи; title/tmdb уходят на сервер — он раскладывает серии по сезонам TMDB
// (у трекеров своя нарезка, сборники нумеруют серии сквозняком: «001 seriya» … «291 seriya»).
async function fetchFiles(id, magnet, title, options) {
  try {
    const tmdb = (currentItem && currentItem.tmdb_id) || '';
    const qs = '?magnet=' + encodeURIComponent(magnet)
      + (title ? '&title=' + encodeURIComponent(title) : '')
      + (tmdb ? '&tmdb=' + encodeURIComponent(tmdb) : '');
    const res = await fetch(`/api/films/${encodeURIComponent(id)}/files${qs}`, options);
    if (!res.ok) throw new Error('HTTP ' + res.status);
    const data = await res.json();
    return data.files || [];
  } catch (e) {
    dbg('files: ошибка ' + id + ': ' + e.message);
    return null;
  }
}

// Сезоны, известные из заголовков раздач, числа сезонов в БД и файлов раздач.
// Нумерация — как в TMDB: у трекеров своя, более дробная нарезка сезонов.
function allKnownSeasons(id, items) {
  const canonical = filmSeasonEps[id];
  if (canonical) return Object.keys(canonical).map(Number).sort((a, b) => a - b);
  const set = new Set();
  for (const s of items) if ((s.season || 0) > 0) set.add(s.season);
  const filmN = (currentItem && currentItem.seasons) || 0;
  const hasFull = items.some((s) => (s.season || 0) === 0);
  if (hasFull && filmN > 0) for (let i = 1; i <= filmN; i++) set.add(i);
  const cache = seriesEpisodes[id];
  if (cache) for (const k in cache.bySeason) if (+k > 0) set.add(+k);
  const top = Math.max(filmN, ...set, 0);
  return Array.from({ length: top }, (_, i) => i + 1);
}

// Высота кадра из строки качества: авто-выбор берёт более высокое разрешение,
// чтобы у плеера были доступные понижения, а не только SRC.
function srcHeight(s) {
  const m = String((s && s.quality) || '').match(/(\d{3,4})/);
  return m ? parseInt(m[1], 10) : 0;
}

// Оценка числа озвучек в раздаче по названию (студии + MVO/Dub/…): для сезона
// выбирается раздача с большим числом переводов, а не первая одиночная.
function sourceRichness(s) {
  const t = String((s && s.title) || '').toLowerCase();
  const set = new Set();
  const add = (re) => { const m = t.match(re); if (m) m.forEach((x) => set.add(x)); };
  add(/lostfilm|newstudio|tvshows|hdrezka|baibako|amedia|novamedia|jaskier|gears\s?media|сыендук|кураж[- ]бамбей|кипарис|red head sound|rhs|newcomers|le-production|невафильм|coldfilm|котов|яроцкий|сербин|доценко|гоблин|кубик в кубе/g);
  add(/\bmvo\b|\bdub\b|\bavo\b|многоголос\w*|дубляж|дублирован\w*|одноголос\w*|двухголос\w*/g);
  return set.size;
}

// Лучшая раздача, покрывающая сезон: максимум озвучек, затем точный сезон,
// разрешение и сиды.
function seasonSourceOf(items, season) {
  let best = null;
  for (const s of items) {
    const ss = s.season || 0;
    if (season && ss !== season && ss !== 0) continue;
    if (!season && ss !== 0) continue;
    // Живая раздача важнее «богатой»: авто-просмотр должен открывать то, что реально играется.
    const live = (s.seeds || 0) > 0 ? 1 : 0;
    const tie = (ss === season ? 1000000 : 900000) + srcHeight(s) + (s.seeds || 0) * 10;
    const score = live * 1e12 + sourceRichness(s) * 1e9 + tie;
    if (!best || score > best._score) { best = s; best._score = score; }
  }
  return best || items[0];
}

// Раздача сезона для автозапуска: ручной выбор пользователя уважаем, иначе — «богатая» по озвучкам.
function pickSeasonSource(items, season) {
  const pref = relPref[season];
  if (pref) {
    const s = items.find((x) => x.magnet === pref && (((x.season || 0) === season) || (x.season || 0) === 0));
    if (s) return s;
  }
  return seasonSourceOf(items, season);
}

// Озвучки, заявленные в заголовке раздачи (без дублей) — для подписи раздачи в списке.
function titleVoices(title) {
  const low = ' ' + String(title || '').toLowerCase().replace(/[`’'']/g, '') + ' ';
  const out = [];
  const add = (d) => { if (!out.includes(d)) out.push(d); };
  const pats = [
    [/lostfilm/, 'LostFilm'], [/newstudi/, 'NewStudio'],
    [/tvshows/, 'TVShows'], [/hdrezka\s*studio/, 'HDRezka Studio'], [/hdrezka/, 'HDRezka'],
    [/baibako/, 'BaibaKo'], [/jaskier/, 'Jaskier'],
    [/gears\s?media/, 'Gears Media'],
    [/novamedia/, 'НоваМедиа'], [/невафильм/, 'Невафильм'], [/coldfilm/, 'ColdFilm'],
    [/newcomers/, 'NewComers'], [/le[- ]production/, 'LE-Production'],
    [/red head sound/, 'Red Head Sound'], [/сыендук/, 'Сыендук'],
    [/кураж[- ]бамбей/, 'Кураж-Бамбей'], [/кипарис/, 'Кипарис'], [/кириллица/, 'Кириллица'],
    [/rg\.?\s?paravozik/, 'RG.Paravozik'], [/котов/, 'Котов'],
    [/яроцкий/, 'Яроцкий'], [/сербин/, 'Сербин'], [/доценко/, 'Доценко'],
    [/гоблин/, 'Гоблин'], [/кубик в кубе/, 'Кубик в кубе'],
  ];
  for (const [re, d] of pats) if (re.test(low)) add(d);
  // Amedia ищем границей слова — иначе ловится внутри novamedia (как и LF/HDr/TVS).
  if (!out.includes('Amedia') && /(?:^|[^a-zа-я0-9])amedia/.test(low)) add('Amedia');
  if (!out.includes('LostFilm') && /\blf\b/.test(low)) add('LostFilm');
  if (!out.includes('HDRezka') && /\bhdr\b/.test(low)) add('HDRezka');
  if (!out.includes('TVShows') && /\btvs\b/.test(low)) add('TVShows');
  if (/\beng(?:lish)?\b/.test(low) || /\boriginal\b/.test(low) || /оригинал/.test(low)) add('Оригинал');
  // «HDRezka Studio» уже покрывает и «HDRezka» — лишнее не дублируем.
  if (out.includes('HDRezka Studio')) {
    const i = out.indexOf('HDRezka');
    if (i >= 0) out.splice(i, 1);
  }
  return out;
}

// Алиасы озвучек для сопоставления чипа с дорожками файла (LF/HDr/TVS и т.п.).
const VOICE_ALIASES = {
  'lostfilm': ['lostfilm', 'lf'],
  'newstudio': ['newstudio', 'new studio'],
  'tvshows': ['tvshows', 'tvs'],
  'hdrezka studio': ['hdrezka studio', 'hdrezka', 'hdr'],
  'hdrezka': ['hdrezka', 'hdr'],
  'baibako': ['baibako', 'baiba ko'],
  'gears media': ['gears media', 'gearsmedia', 'gears'],
  'amedia': ['amedia'],
  'jaskier': ['jaskier'],
  'red head sound': ['red head sound', 'red head', 'rhs'],
  'newcomers': ['newcomers'],
  'le-production': ['le-production', 'leproduction', 'le production'],
  'невафильм': ['невафильм'],
  'coldfilm': ['coldfilm'],
  'novamedia': ['novamedia'],
  'сыендук': ['сыендук', 'syenduk', 'съендук'],
  'кураж-бамбей': ['кураж-бамбей', 'кураж бамбей', 'кураж'],
  'кипарис': ['кипарис'],
  'кириллица': ['кириллица', 'кирилица'],
  'rg.paravozik': ['paravozik', 'паравозик', 'паравоз'],
  'котов': ['котов'],
  'яроцкий': ['яроцкий', 'yarotsky', 'яроцкого'],
  'сербин': ['сербин', 'serbin'],
  'доценко': ['доценко', 'dotsenko'],
  'гоблин': ['гоблин', 'goblin', 'пучков'],
  'кубик в кубе': ['кубик в кубе'],
};

// ordinal дорожки файла, соответствующей озвучке («Оригинал» → англ. дорожка); null — не найдена.
function matchVoiceOrdinal(items, voice) {
  const v = String(voice || '').toLowerCase().trim();
  if (!v) return null;
  if (v === 'оригинал' || /original|оригинал/.test(v)) {
    const it = items.find((tr) => {
      const lang = String(tr.language || '').toLowerCase();
      const tl = String(tr.title || '').toLowerCase();
      return lang.indexOf('eng') === 0 || /english|original|оригинал/.test(lang + ' ' + tl);
    });
    if (it) return it.ordinal != null ? it.ordinal : 0;
    return null;
  }
  const aliases = VOICE_ALIASES[v] || [v];
  const esc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  let score = -1;
  let pick = null;
  for (const tr of items) {
    const hay = (((tr.title || '') + ' ' + (tr.language || '')).toLowerCase());
    for (const a of aliases) {
      if (hay.indexOf(a) < 0) continue;
      let sc = a.length >= 4 ? 2 : 1;
      const re = new RegExp('(^|[^a-zа-я0-9])' + esc(a) + '($|[^a-zа-я0-9])');
      if (re.test(hay)) sc += 4; // вхождение целым словом — предпочтительно
      if (sc > score) { score = sc; pick = tr.ordinal != null ? tr.ordinal : 0; }
    }
  }
  return pick;
}

// Раздачи, покрывающие сезон (точный или полный сборник), без дублей по magnet.
// Сортировка: соответствие сезону по числу серий (см. releaseSeasonFit; «убегающие» —
// в конец), затем «богатство» озвучек, точный сезон, разрешение, сиды.
function seasonReleaseSources(items, season) {
  const out = [];
  const seen = new Set();
  for (const s of items) {
    const ss = s.season || 0;
    if (season && ss !== season && ss !== 0) continue;
    if (!season && ss !== 0) continue;
    if (!s.magnet || seen.has(s.magnet)) continue;
    seen.add(s.magnet);
    out.push(s);
  }
  out.sort((a, b) => {
    const fa = seasonFitRank(a.magnet, season), fb = seasonFitRank(b.magnet, season);
    if (fa !== fb) return fb - fa;
    const ra = sourceRichness(a), rb = sourceRichness(b);
    if (ra !== rb) return rb - ra;
    const ea = (a.season || 0) === season ? 1 : 0;
    const eb = (b.season || 0) === season ? 1 : 0;
    if (ea !== eb) return eb - ea;
    const qa = srcHeight(a), qb = srcHeight(b);
    if (qa !== qb) return qb - qa;
    return (b.seeds || 0) - (a.seeds || 0);
  });
  return out;
}

// Озвучки сезона — объединение по его раздачам без дублей; «Оригинал» всегда последним.
function seasonVoices(items, season) {
  const out = [];
  const add = (d) => { if (!out.includes(d)) out.push(d); };
  for (const s of seasonReleaseSources(items, season)) {
    titleVoices(s.title).forEach(add);
  }
  if (!out.includes('Оригинал')) out.push('Оригинал');
  return out;
}

// Раздача сезона с этой озвучкой: сначала живая, затем любая с ней, затем
// просто лучшая раздача сезона (озвучка могла не быть указана в заголовке).
function pickVoiceSource(items, season, voice) {
  const same = seasonReleaseSources(items, season).filter((s) => titleVoices(s.title).includes(voice));
  return same.find((s) => (s.seeds || 0) > 0) || same[0] || seasonSourceOf(items, season);
}
