'use strict';

/* Страница фильма/сериала (/film.html?id=…): описание, выбор сезона/озвучки/серии.
 * Сериалы играются прямо здесь (плеер рядом со списком серий, player.js) — отдельной страницы
 * просмотра для них нет; у фильмов остаётся /watch.html. */

const filmTitleEl = document.getElementById('details-title');
const detailsEl = document.getElementById('details');
const detailsPoster = document.getElementById('details-poster');
const detailsTitle = document.getElementById('details-title');
const detailsRating = document.getElementById('details-rating');
const detailsSubtitle = document.getElementById('details-subtitle');
const detailsMeta = document.getElementById('details-meta');
const detailsDirector = document.getElementById('details-director');
const detailsActors = document.getElementById('details-actors');
const detailsPlot = document.getElementById('details-plot');
const detailsNote = document.getElementById('details-note');
const detailsOriginal = document.getElementById('details-original');
const sourcesEl = document.getElementById('sources');
const sourcesTitle = document.getElementById('sources-title');
const sourcesEmpty = document.getElementById('sources-empty');
const sourcesSeasonWrap = document.getElementById('sources-season-wrap');
const sourcesSeason = document.getElementById('sources-season');
const sourcesEpisodesWrap = document.getElementById('sources-episodes-wrap');
const sourcesEpisodes = document.getElementById('sources-episodes');
const sourcesRelWrap = document.getElementById('sources-rel-wrap');
const sourcesRel = document.getElementById('sources-rel');
// Подсказки «загружаю…» в блоках сезона/серий/озвучек (блоки видны всегда).
const seasonNote = document.getElementById('sources-season-note');
const episodesNote = document.getElementById('sources-episodes-note');
const relNote = document.getElementById('sources-rel-note');
const playerWrapEl = document.getElementById('player-wrap');
const watchBtn = document.getElementById('watch-btn');
const resumeBtn = document.getElementById('resume-btn');
const filmNote = document.getElementById('film-note');

// Что просят открыть из URL (переход «следующая серия в другой раздаче», ссылка из истории).
const filmParams = playbackPageParams();
const filmId = (filmParams.get('id') || '').trim();
let wantSeason = parseInt(filmParams.get('season') || '0', 10) || 0;
let wantEp = parseInt(filmParams.get('ep') || '0', 10) || 0;
let wantVoice = filmParams.get('voice') || '';
let wantPlay = filmParams.get('autoplay') === '1' || filmParams.get('play') === '1';
// Продолжение просмотра: конкретная раздача/файл/позиция в адресе — играем её сразу.
let startMagnet = filmParams.get('magnet') || '';
let startFile = filmParams.get('file') !== null && filmParams.get('file') !== '' ? parseInt(filmParams.get('file'), 10) : -1;
let startPos = parseFloat(filmParams.get('pos') || '0') || 0;
let seriesUiReady = false;
let savedPlaybackRestored = false;

let lastSourceItems = [];
let lastSourceId = null;
let selectedSeason = null;
let selectedEpisode = null;
// Токен смены выбранной раздачи: пока грузятся файлы, пользователь мог выбрать другое.
let playToken = 0;

function flashFilmNote(text) {
  if (!filmNote) return;
  filmNote.textContent = text;
  filmNote.hidden = false;
  setTimeout(() => { filmNote.hidden = true; }, 3200);
}

// Подсказка в блоке источников: пустая строка — прячем.
function showNote(el, text) {
  if (!el) return;
  el.textContent = text || '';
  el.hidden = !text;
}

function beginSeriesMetadata(id) {
  if (typeof SeriesCatalog === 'undefined' || !id) return;
  SeriesCatalog.load(id, data => {
    if (!currentItem || (currentItem.imdb_id || currentItem.id) !== id) return;
    if (data.tmdb_id && !currentItem.tmdb_id) {
      currentItem.tmdb_id = String(data.tmdb_id); storeItem(currentItem);
    }
    const counts = {};
    for (const s of data.seasons || []) if (s.season > 0 && s.episodes > 0) counts[s.season] = s.episodes;
    if (Object.keys(counts).length) filmSeasonEps[id] = counts;
    const items = lastSourceId === id ? lastSourceItems : [];
    renderSeasonChips(items,id); renderEpisodeGrid(id);
    if (!Object.keys(counts).length && data.status === 'unavailable' && !allKnownSeasons(id,items).length) {
      showNote(seasonNote, t('seriesMetadataUnavailable'));
      showNote(episodesNote, t('seriesMetadataUnavailable'));
    }
  });
}

// ---- Источники на трекере ----
// Источники ищутся на сервере в ф background: пока не ready (status="searching") — опрашиваем,
// чтобы медленный трекер не «замораживал» карточку.
let sourcesRequest = null;
function cancelSources() {
  if (sourcesRequest) sourcesRequest.abort();
  sourcesRequest = null;
}
async function loadSources(it, opts) {
  cancelSources();
  const want = opts || {};
  const request = new AbortController();
  sourcesRequest = request;
  // Сериал: блоки сезона/серий/озвучек не прячем — они видны всегда (подсказки
  // «загружаю…» ставит showSeriesSkeleton, данные — renderSources).
  if (!want.series) {
    sourcesRelWrap.hidden = true;
    sourcesSeasonWrap.hidden = true;
    sourcesEpisodesWrap.hidden = true;
  }
  const active = () => sourcesRequest === request && !request.signal.aborted && currentItem && (currentItem.imdb_id || currentItem.id) === (it.imdb_id || it.id);
  lastSourceItems = [];
  lastSourceId = null;
  syncWatchBtn();
  sourcesEl.hidden = false;
  sourcesEmpty.hidden = true;
  sourcesTitle.textContent = t('searchingSources');

  const id = it.imdb_id || it.id;
  if (!id) {
    sourcesTitle.textContent = t('sourcesTitle');
    sourcesEmpty.hidden = false;
    sourcesEmpty.textContent = t('noSources');
    return;
  }

  // Сброс выбора — только при старте загрузки, не при перерисовках ниже.
  selectedSeason = PP.playing() ? PP.season() || wantSeason || null : wantSeason || null;
  selectedEpisode = PP.playing() ? PP.episode() || wantEp || null : wantEp || null;
  // Ручной выбор озвучки/раздачи живёт в рамках одной карточки.
  for (const k in relPref) delete relPref[k];
  for (const k in voicePref) delete voicePref[k];

  // /sources отдаёт кэш мгновенно (status=searching) — опрашиваем до готовности (до 3 минут).
  const deadline = Date.now() + 180000;
  let netErrors = 0;
  let lastKey = ''; // ключ последней отрисовки — перерисовываем при изменении
  // Автозапуск (переход со страницы просмотра «следующая серия» или из истории):
  // используем сохранённые источники сразу, не дожидаясь фонового поиска.
  // Список растёт по мере обхода трекеров, поэтому повторяем только при новых данных.
  let playTries = 0;
  let lastTryItems = -1;

  while (Date.now() < deadline) {
    if (!active()) return;
    let data;
    try {
      const res = await fetch('/api/films/' + encodeURIComponent(id) + '/sources', { signal: request.signal });
      if (!res.ok) throw new Error('HTTP ' + res.status);
      data = await res.json();
      if (!active()) return;
      netErrors = 0;
    } catch (e) {
      if (!active()) return;
      dbg('sources: ошибка ' + id + ': ' + e.message);
      if (++netErrors >= 3) {
        sourcesTitle.textContent = t('sourcesTitle');
        sourcesEmpty.hidden = false;
        sourcesEmpty.textContent = t('sourceSearchFailed');
        return;
      }
      await sleep(4000);
      continue;
    }

    const ready = data.status === 'ready';
    // Раздачи без сидов НЕ выбрасываем: часто только у них есть редкая озвучка сезона
    // (TVShows/Novamedia/Jaskier), а играем всё равно с живых (см. playEpisode).
    const items = (data.items || []).filter((s) => s.magnet);

    // Каноническое число серий сезона (TMDB) — по нему строится сетка.
    if (data.seasons && data.seasons.length && !(typeof SeriesCatalog !== 'undefined' && SeriesCatalog.seasons(id).length)) {
      const m = {};
      for (const s of data.seasons) {
        if (s && s.season > 0 && s.episodes > 0) m[s.season] = s.episodes;
      }
      if (Object.keys(m).length) filmSeasonEps[id] = m;
    }

    // Показываем источники сразу, как появились (кэш наполняется ещё до конца поиска).
    const key = JSON.stringify([id, items, filmSeasonEps[id]]);
    if ((items.length || filmSeasonEps[id]) && key !== lastKey) {
      if (seriesEpisodes[id]) seriesEpisodes[id].done = {};
      lastKey = key;
      lastSourceItems = items;
      lastSourceId = id;
      renderSources(items, id);
      syncWatchBtn();
    }

    // Автозапуск: открываем просмотр сразу, как только нашлось что играть.
    if (want.play && items.length
        && (playTries === 0 || items.length > lastTryItems)) {
      playTries++;
      lastTryItems = items.length;
      if (await startWanted(id)) want.play = false;
    }

    if (ready) {
      sourcesTitle.textContent = t('sourcesTitle');
      if (items.length === 0) {
        sourcesEmpty.hidden = false;
        sourcesEmpty.textContent = t('noSources');
        return;
      }
      // Автозапуска нет — только по кнопке «▶ Смотреть», выбору серии или озвучки.
      syncWatchBtn();
      return;
    }

    const sec = Math.max(0, Math.round((deadline - Date.now()) / 1000));
    sourcesTitle.textContent = items.length ? t('sourcesTitle') : t('searchingSources') + ' (' + sec + ' с)';
    await sleep(4000);
  }

  if (!active()) return;
  sourcesTitle.textContent = t('sourcesTitle');
  if (lastSourceItems.length) {
    // Источники уже есть, но обход трекеров не завершился — даём открыть просмотр вручную.
    want.play = false;
    syncWatchBtn();
    flashFilmNote(t('sourceSearchTimeout'));
    return;
  }
  sourcesEmpty.hidden = false;
  sourcesEmpty.textContent = t('sourceSearchTimeout');
}

// ВАЖНО: порядок loadSources → updateSourceLabels → renderSources такой же, как в
// прежнем app.js: tests/sources.test.cjs вырезает код от «let sourcesRequest = null;»
// до «function updateSourceLabels» и подменяет renderSources своим стендом.
function updateSourceLabels() {
  sourcesSeasonWrap.querySelector('.src-group-label').textContent = t('seasonLabel');
  sourcesEpisodesWrap.querySelector('.src-group-label').textContent = t('episodeLabel');
  sourcesRelWrap.querySelector('.src-group-label').textContent = t('voicesLabel');
}

function renderSources(items, id) {
  const request = sourcesRequest;
  if (!currentItem || (currentItem.imdb_id || currentItem.id) !== id) return;
  lastSourceItems = items;
  lastSourceId = id;
  if (typeof FilmFeatures !== 'undefined') FilmFeatures.sources(items, id);
  updateSourceLabels();
  const isSeries = isSeriesKind(currentItem && currentItem.kind);

  if (isSeries) {
    // Блоки видны всегда: чипы сезонов/озвучек и сетка серий — по мере поступления данных.
    sourcesSeasonWrap.hidden = false;
    sourcesEpisodesWrap.hidden = false;
    sourcesRelWrap.hidden = false;
    renderSeasonChips(items, id);
    renderSeasonVoices(items, id);
    renderEpisodeGrid(id);
    ensureSeasonEpisodes(id, selectedSeason, items).then(() => {
      if (sourcesRequest !== request || !currentItem || (currentItem.imdb_id || currentItem.id) !== id) return;
      // Сезоны неизвестны (например, сборник без сезонов в названии) — берём первый из файлов раздач.
      if (!selectedSeason && !filmSeasonEps[id]) {
        const s2 = allKnownSeasons(id, items);
        selectedSeason = s2[0] || null;
        renderSeasonChips(items, id);
      }
      renderEpisodeGrid(id);
      renderSeasonVoices(items, id);
    });
  } else {
    // Фильм: сезонов/озвучек-чипов нет, раздача выбирается автоматически (кнопкой «Смотреть»).
    sourcesSeasonWrap.hidden = true;
    sourcesEpisodesWrap.hidden = true;
    sourcesRelWrap.hidden = true;
    showNote(seasonNote, '');
    showNote(episodesNote, '');
    showNote(relNote, '');
    sourcesEl.hidden = true;
  }
}

// ---- Всё ниже — вне блока, который берут JS-тесты (tests/sources.test.cjs) ----

// «▶ Смотреть» видна, когда есть источники и воспроизведение ещё не начато.
function syncWatchBtn() {
  if (!watchBtn) return;
  const hasItems = Array.isArray(lastSourceItems) && lastSourceItems.length;
  watchBtn.hidden = !(hasItems && !wantPlay && resumeBtn.hidden);
  watchBtn.textContent = t('watch');
}

// Сезон/озвучка из URL (?season=&voice=) — применяем до выбора по умолчанию.
function applyWanted() {
  if (wantSeason && selectedSeason === null) selectedSeason = wantSeason;
  if (wantVoice && selectedSeason && !voicePref[selectedSeason]) voicePref[selectedSeason] = wantVoice;
}

function renderSeasonChips(items, id) {
  applyWanted();
  const seasons = allKnownSeasons(id, items);
  const available = (k) => seasonReleaseSources(items, k).length > 0;
  if (selectedSeason === null || !seasons.includes(selectedSeason)) {
    selectedSeason = seasons.find(available) || seasons[0] || null;
  }
  // Сезоны показываем всегда (в том числе когда сезон один) — блок не прячем.
  sourcesSeasonWrap.hidden = false;
  sourcesSeason.innerHTML = '';
  showNote(seasonNote, seasons.length ? '' : t('seasonsLoading'));
  seasons.forEach((k) => {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'chip' + (k === selectedSeason ? ' active' : '');
    btn.textContent = seasonLabel(k);
    // Browsing canonical seasons must not depend on available torrent sources.
    btn.disabled = !available(k) && !(filmSeasonEps[id] && filmSeasonEps[id][k] > 0);
    if (typeof SeriesCatalog !== 'undefined') {
      const metadata = SeriesCatalog.seasons(id).find(s => s.season === k);
      if (metadata && metadata.name) btn.title = metadata.name;
    }
    btn.addEventListener('click', () => {
      if (k === selectedSeason) return;
      selectedSeason = k;
      selectedEpisode = null;
      renderSeasonChips(items, id);
      renderSeasonVoices(items, id);
      renderEpisodeGrid(id);
      ensureSeasonEpisodes(id, selectedSeason, items).then(() => {
        if (!currentItem || (currentItem.imdb_id || currentItem.id) !== id || selectedSeason !== k) return;
        renderEpisodeGrid(id);
        renderSeasonVoices(items, id);
      });
    });
    sourcesSeason.appendChild(btn);
  });
  if (typeof FilmFeatures !== 'undefined') FilmFeatures.render(currentItem);
}

// Чипы озвучек сезона; раздача подбирается автоматически по озвучке.
// Для сериала блок виден всегда: пока раздач нет — подсказка «загружаю…».
function renderSeasonVoices(items, id) {
  if (!isSeriesKind(currentItem && currentItem.kind)) {
    sourcesRelWrap.hidden = true;
    sourcesRel.innerHTML = '';
    return;
  }
  sourcesRelWrap.hidden = false;
  sourcesRel.innerHTML = '';
  if (!id || !items || !items.length) return;
  const voices = seasonVoices(items, selectedSeason);
  if (!voices.length) {
    showNote(relNote, t('voicesLoading'));
    return;
  }
  showNote(relNote, '');
  const cur = voicePref[selectedSeason];
  voices.forEach((voice) => {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'chip' + (voice === cur ? ' active' : '');
    btn.textContent = voice;
    btn.title = voice;
    btn.addEventListener('click', () => {
      if (voice === voicePref[selectedSeason]) return; // уже выбрана
      switchVoice(id, voice);
    });
    sourcesRel.appendChild(btn);
  });
}

// Пользователь выбрал озвучку: запоминаем и, если серия уже выбрана, сразу открываем просмотр.
async function switchVoice(id, voice) {
  const items = lastSourceItems;
  if (!id || !voice || !items || !items.length) return;
  const season = selectedSeason;
  const src = pickVoiceSource(items, season, voice);
  voicePref[season] = voice;
  if (src && src.magnet) relPref[season] = src.magnet;
  renderSeasonVoices(items, id);
  if (!selectedEpisode) return; // озвучка запомнена — включится при выборе серии
  await playEpisode(id, season, selectedEpisode);
}

// Сетка серий сезона из кэша; число кнопок — по сезону TMDB (раздача может
// покрывать сезон частично, иначе последние серии в сетке не появлялись).
function renderEpisodeGrid(id) {
  if (!id) return;
  if (wantEp && !selectedEpisode) selectedEpisode = wantEp;
  const cache = seriesEpisodes[id];
  const eps = (cache && cache.bySeason[selectedSeason]) || [];
  const can = seasonEpisodeCount(id, selectedSeason);
  let total = eps.reduce((m, e) => (e > m ? e : m), 0);
  if (filmSeasonEps[id]) total = can;
  const list = [];
  for (let e = 1; e <= total; e++) list.push(e);
  // Серии видны всегда: пока список не готов — подсказка «загружаю…».
  sourcesEpisodesWrap.hidden = false;
  const opened = new Set([...sourcesEpisodes.querySelectorAll('.episode-description[open]')].map(el => el.dataset.episode));
  const focused = document.activeElement?.closest('.episode-card');
  const focusedEpisode = focused && sourcesEpisodes.contains(focused) ? focused.dataset.episode : null;
  const focusedDescription = !!document.activeElement?.matches('.episode-description > summary');
  sourcesEpisodes.innerHTML = '';
  showNote(episodesNote, list.length ? '' : t('episodesLoading'));
  if (!list.length) return;
  if (typeof SeriesCatalog !== 'undefined' && can > 0) {
    const season = selectedSeason;
    SeriesCatalog.loadEpisodes(id,season,() => {
      if (currentItem && (currentItem.imdb_id || currentItem.id) === id && selectedSeason === season) renderEpisodeGrid(id);
    });
  }
  list.forEach((ep) => {
    const metadata = typeof SeriesCatalog !== 'undefined'
      ? SeriesCatalog.episodes(id, selectedSeason).find(e => e.episode === ep) : null;
    const entry = typeof episodeHistoryEntry === 'function' ? episodeHistoryEntry(id, selectedSeason, ep) : null;
    const name = metadata?.name || t('episodeLabel') + ' ' + ep;
    const meta = [];
    if (/^\d{4}-\d{2}-\d{2}$/.test(metadata?.air_date || '')) {
      const date = new Date(metadata.air_date + 'T12:00:00');
      if (!Number.isNaN(date.getTime())) meta.push(date.toLocaleDateString(filmText('ru-RU', 'en-US'), { day: 'numeric', month: 'short', year: 'numeric' }));
    }
    if (Number.isFinite(Number(metadata?.runtime)) && metadata.runtime > 0) meta.push(metadata.runtime + ' ' + t('minShort'));
    const row = document.createElement('article');
    row.className = 'episode-card';
    row.dataset.episode = String(ep);
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'ep-btn episode-main' + (ep === selectedEpisode ? ' active' : '');
    btn.setAttribute('aria-label', [t('episodeLabel') + ' ' + ep, name, ...meta].join(' · '));
    if (ep === selectedEpisode) btn.setAttribute('aria-current', 'true');
    if (metadata) btn.title = [metadata.name, metadata.air_date].filter(Boolean).join(' · ');
    const percent = entry && entry.duration > 0 && entry.position > 0
      ? Math.min(100, Math.max(0, Math.round(entry.position / entry.duration * 100))) : 0;
    btn.innerHTML = `<span class="episode-number">${ep}</span><span class="episode-content"><span class="episode-name">${escapeHtml(name)}</span>${meta.length ? `<span class="episode-meta">${escapeHtml(meta.join(' · '))}</span>` : ''}</span><svg class="episode-play" viewBox="0 0 28 28" fill="none" aria-hidden="true"><circle cx="14" cy="14" r="12" stroke="currentColor"/><path d="m11 8 9 6-9 6Z" fill="currentColor"/></svg>${percent ? `<span class="episode-progress"><span style="width:${percent}%"></span></span>` : ''}`;
    btn.disabled = !eps.includes(ep) && !(can > 0 && seasonReleaseSources(lastSourceItems, selectedSeason).length);
    btn.addEventListener('click', () => onPickEpisode(ep));
    row.appendChild(btn);
    if (metadata?.overview) {
      const description = document.createElement('details');
      description.className = 'episode-description';
      description.dataset.episode = String(ep);
      description.open = opened.has(String(ep));
      const summary = document.createElement('summary');
      summary.textContent = filmText('Подробнее о серии', 'Episode details');
      const plot = document.createElement('p');
      plot.textContent = metadata.overview;
      description.append(summary, plot);
      row.appendChild(description);
    }
    sourcesEpisodes.appendChild(row);
  });
  if (focusedEpisode) {
    const selector = focusedDescription ? '.episode-description > summary' : '.ep-btn';
    sourcesEpisodes.querySelector(`.episode-card[data-episode="${focusedEpisode}"] ${selector}`)?.focus({ preventScroll: true });
  }
}

// Выбор серии: ищем раздачу с выбранной озвучкой (или «богатую») и уходим в просмотр.
async function onPickEpisode(ep) {
  const id = lastSourceId;
  const items = lastSourceItems;
  if (!id || !items || !items.length) return;
  const known = ((seriesEpisodes[id] && seriesEpisodes[id].bySeason[selectedSeason]) || []).includes(ep);
  if (!known && !(ep > 0 && ep <= seasonEpisodeCount(id, selectedSeason)
      && seasonReleaseSources(items, selectedSeason).length)) return;
  selectedEpisode = ep;
  renderEpisodeGrid(id);
  await playEpisode(id, selectedSeason, ep);
}

// Играет серию из раздачи по выбранной озвучке сезона; раздачи, не подходящие
// сезону по числу серий (номера «убегают», см. releaseSeasonFit), не берём.
// Если раздача мертва — пробуем следующую с той же озвучкой, затем любую сезона.
async function playEpisode(id, season, ep) {
  const items = lastSourceItems;
  if (!id || !items || !items.length) return false;
 if(typeof PlaybackExtras!=='undefined')PlaybackExtras.loading('searching','Проверяем источник выбранной серии.');
  const voice = voicePref[season] || Personal.preferences().voice || '';
  let cands;
  if (voice) {
    cands = seasonReleaseSources(items, season).filter((s) => titleVoices(s.title).includes(voice));
    if (!cands.length) cands = seasonReleaseSources(items, season);
  } else {
    const pref = pickSeasonSource(items, season);
    cands = pref
      ? [pref, ...seasonReleaseSources(items, season).filter((s) => s.magnet !== pref.magnet)]
      : seasonReleaseSources(items, season);
  }
  if (!cands.length) cands = items.slice(0, 3);
  // Каноническое число серий сезона (TMDB) — по нему проверяем, что раздача описывает сезон.
  const want = seasonEpisodeCount(id, season);

  const tok = ++playToken;
  let src = null, files = null, f = null;
  let fb = null; // запасной вариант: сезон есть, а нужной серии в раздаче нет
  // Раздачи без сидов — ПОСЛЕДНИМИ: иначе плеер ждёт метаданные до таймаута
  // (среди них «мёртвые» заявители редких озвучек TVShows/Novamedia/Jaskier).
  // Настройки ограничивают качество/размер, но не должны вытеснять нужный сезон.
  cands = probeOrder(Personal.rank(cands, voice), season);
  cands = cands.filter((s) => (s.seeds || 0) > 0).concat(cands.filter((s) => !((s.seeds || 0) > 0)));
  cands = cands.slice(0, episodeProbeLimit);
  for (const c of cands) {
    if (!c || !c.magnet) continue;
    const fs = (await fetchFiles(id, c.magnet, c.title)) || [];
    if (tok !== playToken) return false; // пользователь выбрал другое — выходим
    if (!fs.length) continue; // мёртвая/недоступная — пробуем следующую
    // Раздача должна соответствовать сезону по числу серий: номера «убегают» за его
    // границы (сборник открыли без структуры TMDB) или серии сезона нет — берём другую.
    // Верим итогу только когда запрос ушёл с tmdb_id (см. tmdbMappingOn).
    const fit = releaseSeasonFit(fs, season, want);
    const mapped = tmdbMappingOn();
    if (mapped) rememberSeasonFit(c.magnet, season, fit);
    if (want && mapped && (!fit.fits || (season && fit.count === 0))) {
      dbg('озвучка: раздача «' + (c.title || '?') + '» не подходит сезону ' + season +
        ' (серий сезона ' + fit.count + ' из ' + want + ', за границей сезона ' + fit.overflow + ')');
      continue;
    }
    let ff = null;
    if (season && ep) ff = fs.find((x) => x.season === season && x.episode === ep);
    if (ff) { src = c; files = fs; f = ff; break; }
    if (!season) {
      const any = fs.find((x) => (x.season || 0) === 0) || fs[0];
      if (any) { src = c; files = fs; f = any; break; }
      continue;
    }
    // Сезон покрыт частично — запоминаем как последний шанс и ищем раздачу с нужной серией.
    if (!ep && !fb) {
      const any = fs.find((x) => x.season === season) || fs.find((x) => (x.season || 0) === 0) || fs[0];
      if (any) fb = { c, fs, f: any };
    }
  }
  if ((!src || !f || !files) && fb) { src = fb.c; files = fb.fs; f = fb.f; }
  if (!src || !f || !files) {
    flashFilmNote(t('episodesUnavailable'));
    if(typeof PlaybackExtras!=='undefined')PlaybackExtras.loading('error','Подходящий источник не найден. Выберите другую раздачу или озвучку.');
    return false;
  }
  if (voice) relPref[season] = src.magnet;
  const useSeason = (f.season || 0) > 0 ? f.season : (season || 0);
  const useEp = f.episode || ep || 0;
  dbg('озвучка: S' + (season || '?') + (ep ? ' E' + ep : '') + ' «' + (voice || 'авто') + '» -> "' + (src.title || '?') + '"');
  selectedSeason = useSeason || null;
  selectedEpisode = useEp || null;
  renderSeasonChips(items, id);
  renderEpisodeGrid(id);
  renderSeasonVoices(items, id);
  // Сетку серий дополнят файлы раздачи (хук onFiles у плеера), а сериал играем здесь же.
  if (PP.available) {
    setupSeriesUi(); // хуки плеера могли ещё не включиться (kind пришёл из API позже)
    storeItem(currentItem);
    PP.start({
      id: id,
      magnet: src.magnet,
      release: src.title || '',
      file: f.index,
      season: useSeason,
      ep: useEp,
      voice: voice || '',
    });
    requestAnimationFrame(() => {
      syncFilmLayout();
      playerWrapEl?.scrollIntoView({ block: 'nearest' });
    });
    return true;
  }
  openWatch(id, src, f, useSeason, useEp, voice);
  return true;
}

// Фильм: играем лучшую раздачу (её выбирает сервер) — без выбора сезона/серии.
function openWatch(id, src, file, season, ep, voice) {
  storeItem(currentItem);
  go(watchUrl(id, {
    magnet: src.magnet,
    release: src.title || '',
    file: file && typeof file.index === 'number' ? file.index : -1,
    season: season || 0,
    ep: ep || 0,
    voice: voice || '',
  }));
}

// «▶ Смотреть»: фильм — лучшая раздача, сериал — выбранная/первая серия сезона.
async function watchNow() {
  wantPlay = false; // кнопку прячем: просмотр запущен вручную
  syncWatchBtn();
  if (lastSourceId) await startWanted(lastSourceId);
}
watchBtn?.addEventListener('click', watchNow);

// Открывает запрошенный из URL (или первый доступный) сезон/серию.
// true — просмотр открыт (страница сменилась), false — открыть не удалось.
async function startWanted(id) {
  const items = lastSourceItems;
  if (!id || !items || !items.length) return false;
  if (!isSeriesKind(currentItem && currentItem.kind)) {
    const ranked = Personal.rank(items);
    if (!ranked.length) { flashFilmNote('Нет источников в пределах ваших настроек. Измените предпочтения или выберите вручную.'); return false; }
    openWatch(id, ranked[0], null, 0, 0, Personal.preferences().voice || '');
    return true;
  }
  const season = selectedSeason || wantSeason || allKnownSeasons(id, items)[0] || 0;
  if (!season) {
    flashFilmNote(t('noSources'));
    return false;
  }
  selectedSeason = season;
  if (!selectedEpisode) {
    await ensureSeasonEpisodes(id, season, items);
    const eps = (seriesEpisodes[id] && seriesEpisodes[id].bySeason[season]) || [];
    if (eps.length) {
      selectedEpisode = eps[0];
      renderEpisodeGrid(id);
    }
  }
  return await playEpisode(id, season, selectedEpisode);
}

// ---- Кнопка «Продолжить» и история ----
function updateResumeBtn(it) {
  if (!resumeBtn) return;
  const id = it && (it.imdb_id || it.id);
  const entry = id ? historyEntry(id) : null;
  const saved = !!(VV.user && entry && entry.magnet && id);
  const canResume = saved && entry.position >= 30 && !(entry.duration > 0 && entry.duration - entry.position < 30);
  resumeBtn.hidden = !canResume;
  const series = isSeriesKind(it && it.kind);
  const restart = document.getElementById('startover-btn');
  const note = document.getElementById('film-resume-note');
  const progressWrap = document.getElementById('film-progress-wrap');
  restart.hidden = !saved || !(entry.position > 0);
  note.hidden = !canResume;
  progressWrap.hidden = !saved || !(entry.duration > 0) || !(entry.position > 0);
  if (canResume) {
    resumeBtn.textContent = series && entry.season > 0 && entry.episode > 0
      ? '▶ ' + t('resume') + ' · ' + t('seasonLabel') + ' ' + entry.season + ', ' + t('episodeLabel').toLowerCase() + ' ' + entry.episode
      : '▶ ' + t('resumeFrom') + ' ' + fmtTime(entry.position);
    note.textContent = filmText('Вы остановились на ', 'You stopped at ') + fmtTime(entry.position);
  }
  if (!progressWrap.hidden) {
    const percent = Math.min(100, Math.max(0, Math.round(entry.position / entry.duration * 100)));
    document.getElementById('film-progress-fill').style.width = percent + '%';
    document.getElementById('film-progress').setAttribute('aria-valuenow', String(percent));
    document.getElementById('film-progress-text').textContent = fmtTime(entry.position) + ' / ' + fmtTime(entry.duration);
  }
  const playSaved = position => {
    if (!saved) return;
    storeItem(currentItem);
    const options = {
      id,
      magnet: entry.magnet,
      file: typeof entry.file === 'number' ? entry.file : -1,
      season: entry.season || 0,
      ep: entry.episode || 0,
      pos: position,
      voice: entry.voice || '',
    };
    if (typeof entry.track === 'number') options.track = entry.track;
    if (typeof entry.subs === 'number') options.subs = entry.subs;
    if (entry.quality) options.quality = entry.quality;
    // Сериал продолжаем на этой же странице (раздача и файл берутся из истории).
    if (PP.available && isSeriesKind(currentItem && currentItem.kind)) {
      PP.start(options);
      requestAnimationFrame(() => {
        syncFilmLayout();
        playerWrapEl?.scrollIntoView({ block: 'nearest' });
      });
      return;
    }
    go(watchUrl(id, options));
  };
  resumeBtn.onclick = () => playSaved(entry.position || 0);
  restart.onclick = () => playSaved(0);
  syncWatchBtn();
}

// ---- Карточка фильма ----

function showDetails(it) {
  document.body.classList.toggle('film-series', isSeriesKind(it.kind));
  if (typeof FilmFeatures !== 'undefined') { FilmFeatures.render(it); FilmFeatures.explore(it); }
  detailsEl.hidden = false;
  const poster = posterSrc(it.poster_url || it.poster || '');
  detailsPoster.hidden = !poster;
  if (poster) detailsPoster.src = poster;
  else detailsPoster.removeAttribute('src');

  detailsTitle.textContent = dispTitle(it);
  detailsRating.replaceChildren();
  for (const [label, value, cls] of [['IMDb', it.rating || it.rating_imdb, 'rating-imdb'], ['TMDB', it.rating_tmdb, 'rating-tmdb']]) {
    const number = Number(value);
    if (!Number.isFinite(number) || number <= 0 || number > 10) continue;
    const badge = document.createElement('span');
    badge.className = 'film-rating-badge ' + cls;
    const name = document.createElement('span');
    name.className = 'film-rating-label';
    name.textContent = label;
    const score = document.createElement('span');
    score.className = 'film-rating-value';
    score.textContent = number.toFixed(1);
    badge.append(name, score);
    detailsRating.appendChild(badge);
  }

  const alt = dispTitleAlt(it);
  const year = it.year ? String(it.year) : '';
  const dur = fmtDuration(it.movie_length || it.duration);
  detailsOriginal.textContent = alt;
  detailsOriginal.hidden = !alt || alt.trim() === dispTitle(it).trim();
  const series = isSeriesKind(it.kind);
  const length = series && it.seasons > 0 ? it.seasons + ' ' + filmText('сез.', 'seasons')
    : dur + (series && dur ? filmText(' / серия', ' / episode') : '');
  detailsSubtitle.textContent = [year, length].filter(Boolean).join(' · ');

  const countries = (it.countries || []).join(', ');
  const genres = (it.genres || []).map(dispGenre).join(', ');
  detailsMeta.textContent = genres;
  const country = document.getElementById('details-country');
  country.hidden = !countries;
  country.textContent = filmText('Страна: ', 'Country: ') + countries;

  const director = dispDirector(it);
  detailsDirector.textContent = director ? t('directorLabel') + ': ' + director : '';
  detailsDirector.hidden = !director;
  const actors = dispActors(it).join(', ');
  detailsActors.textContent = actors ? t('actorsLabel') + ': ' + actors : '';
  detailsActors.hidden = !actors;
  if (typeof FilmFeatures !== 'undefined') FilmFeatures.renderCredits();

  detailsPlot.textContent = dispPlot(it) || t('noPlot');
  filmTitleEl.textContent = dispTitle(it);
  document.title = dispTitle(it) + ' — Кинотека';
  detailsNote.hidden = true;
  translateFilmPage();
  syncFilmLayout();
}

function filmText(ru, en) { return lang === 'en' ? en : ru; }

function translateFilmPage() {
  document.getElementById('film-back').textContent = filmText('← Назад в каталог', '← Back to catalog');
  document.getElementById('film-about-title').textContent = isSeriesKind(currentItem?.kind)
    ? filmText('О сериале', 'About the series') : filmText('О фильме', 'About the film');
  document.getElementById('film-options-title').textContent = filmText('Варианты просмотра', 'Playback options');
  document.getElementById('startover-btn').textContent = filmText('С начала', 'Start over');
  document.getElementById('film-progress').setAttribute('aria-label', filmText('Прогресс просмотра', 'Viewing progress'));
  document.getElementById('film-watch-order').setAttribute('aria-label', filmText('Порядок просмотра', 'Watch order'));
}

function syncFilmLayout() {
  const layout = document.getElementById('film-watch-layout');
  const column = document.getElementById('film-playback-column');
  const caption = document.getElementById('film-playing-title');
  const status = document.getElementById('film-source-status');
  if (!layout || !column) return;
  const series = isSeriesKind(currentItem?.kind);
  const visible = playerWrapEl && !playerWrapEl.hidden;
  const trailer = playerWrapEl?.classList.contains('trailer-mode');
  layout.hidden = !visible && !(series && !sourcesEl.hidden);
  layout.classList.toggle('no-player', !visible);
  column.hidden = !visible;
  sourcesEl.classList.toggle('film-movie-sources', !series);
  if (series) {
    const title = filmText('Сезоны и серии', 'Seasons and episodes');
    if (sourcesTitle.textContent !== title) sourcesTitle.textContent = title;
  }
  const state = PP.available ? PP.state() : {};
  caption.hidden = !visible || (!trailer && !state.active);
  caption.textContent = trailer ? filmText('Трейлер', 'Trailer')
    : state.season > 0 && state.episode > 0
      ? t('seasonLabel') + ' ' + state.season + ' · ' + t('episodeLabel') + ' ' + state.episode
      : dispTitle(currentItem || {});
  const message = !series && !sourcesEl.hidden
    ? (sourcesEmpty.hidden ? sourcesTitle.textContent : sourcesEmpty.textContent) : '';
  status.hidden = !message;
  if (status.textContent !== message) status.textContent = message;
}

let filmLayoutFrame = 0;
function scheduleFilmLayout() {
  if (filmLayoutFrame) return;
  filmLayoutFrame = requestAnimationFrame(() => { filmLayoutFrame = 0; syncFilmLayout(); });
}
const filmLayoutObserver = new MutationObserver(scheduleFilmLayout);
filmLayoutObserver.observe(playerWrapEl, { attributes: true, attributeFilter: ['hidden', 'class'] });
filmLayoutObserver.observe(sourcesEl, { attributes: true, attributeFilter: ['hidden'], childList: true, subtree: true });
window.addEventListener('playbackstage', scheduleFilmLayout);
window.addEventListener('playbackstop', scheduleFilmLayout);

function hasFilmExtras(f) {
  return !!(f && (f.director || f.actors || f.movie_length || (f.countries && f.countries.length)));
}

// Детали фильма; сервер обогащает в фоне — переспрашиваем пару раз.
async function fetchFilmDetails(id, retries) {
  try {
    const r = await fetch('/api/films/' + encodeURIComponent(id));
    if (!r.ok) return;
    const f = await r.json();
    // tmdb_id нужен для раскладки серий по сезонам TMDB — иначе раздача «убегает» за границы сезона.
    if (f && f.tmdb_id && currentItem && !currentItem.tmdb_id) currentItem.tmdb_id = f.tmdb_id;
    if (f && typeof f === 'object') {
      currentItem = Object.assign({}, currentItem, f);
      storeItem(currentItem);
      showDetails(currentItem);
      updateResumeBtn(currentItem);
      setupSeriesUi();
      if (!hasFilmExtras(f) && retries > 0) {
        setTimeout(() => fetchFilmDetails(id, retries - 1), 3000);
      } else if (peoplePending(f) && retries > 0) {
        // Имена (режиссёр/актёры) ещё не переведены — фоновая задача сервера доберёт их
        // из TMDB за пару секунд, после чего карточка покажет их по-русски.
        setTimeout(() => fetchFilmDetails(id, retries - 1), 3000);
      }
    }
  } catch (e) { /* нет данных — оставляем то, что пришло из каталога */ }
}

// ---- Сериалы: блоки видны всегда, серия играется на этой же странице ----

// До появления данных в блоках сезона/серий/озвучек висит подсказка «загружаю…»,
// сами блоки не прячем (и сезон один тоже показываем).
function showSeriesSkeleton() {
  sourcesEl.hidden = false;
  sourcesSeasonWrap.hidden = false;
  sourcesEpisodesWrap.hidden = false;
  sourcesRelWrap.hidden = false;
  showNote(seasonNote, t('seasonsLoading'));
  showNote(episodesNote, t('episodesLoading'));
  showNote(relNote, t('voicesLoading'));
  sourcesEmpty.hidden = true;
}

// Плеер и скелет блоков включаем, как только известно, что это сериал (kind приходит
// из sessionStorage или из /api/films, поэтому вызывается и позже).
function setupSeriesUi() {
  if (seriesUiReady) return;
  if (!isSeriesKind(currentItem && currentItem.kind)) return;
  seriesUiReady = true;
  showSeriesSkeleton();
  if (typeof beginSeriesMetadata === 'function') beginSeriesMetadata(filmId);
  if (!PP.available) return; // разметки плеера нет — серия уйдёт на /watch.html
  PP.init({
    onStateChange: onPlayerState,
    onReleaseEnd: onReleaseEnd,
    onFiles: onPlayerFiles,
  });
  resumeSavedPlayback();
}

// Файлы играемой раздачи: дополняем сетку серий и подписи озвучек.
function onPlayerFiles(files, st) {
  const id = lastSourceId;
  if (!id || !files || !files.length) return;
  const cache = seriesEpisodes[id] || (seriesEpisodes[id] = { bySeason: {}, done: {}, probing: false });
  for (const x of files) {
    const xs = x.season || 0;
    const xe = x.episode || 0;
    if (xs <= 0 || xe <= 0) continue;
    const wantXs = seasonEpisodeCount(id, xs);
    if (wantXs && xe > wantXs) continue; // «убежавшие» номера не берём (releaseSeasonFit)
    const list = cache.bySeason[xs] || (cache.bySeason[xs] = []);
    if (!list.includes(xe)) list.push(xe);
  }
  if (st && st.season) selectedSeason = st.season;
  if (st && st.episode) selectedEpisode = st.episode;
  renderEpisodeGrid(id);
}

// Плеер сменил серию/раздачу — обновляем выделение в сетке и адрес страницы,
// чтобы F5 продолжил ту же серию (и с тем же ?autoplay=1).
function onPlayerState(st) {
  if (st.season && st.voice) voicePref[st.season] = st.voice;
  if (st.season) selectedSeason = st.season;
  if (st.episode) selectedEpisode = st.episode;
  if (isSeriesKind(currentItem && currentItem.kind)) {
    renderEpisodeGrid(lastSourceId);
    renderSeasonChips(lastSourceItems, lastSourceId);
    renderSeasonVoices(lastSourceItems, lastSourceId);
  }
  syncFilmUrl(st);
  syncFilmLayout();
}

function syncFilmUrl(st) {
  if (!filmId) return;
  const p = new URLSearchParams(location.search);
  p.set('id', filmId);
  p.delete('pos'); // позицию ведёт серверная история просмотра
  if (st.season) p.set('season', String(st.season));
  if (st.episode) p.set('ep', String(st.episode));
  const voice = (st.season && voicePref[st.season]) || wantVoice;
  if (voice) p.set('voice', voice);
  if (st.active) {
    // Пока играет — в адресе и раздача/файл: F5 продолжит ту же серию.
    p.set('autoplay', '1');
    if (st.file >= 0) p.set('file', String(st.file)); else p.delete('file');
    if (st.magnet) p.set('magnet', st.magnet); else p.delete('magnet');
    if (PP.release()) p.set('rt', PP.release()); else p.delete('rt');
  } else {
    p.delete('autoplay');
    p.delete('magnet');
    p.delete('file');
    p.delete('rt');
  }
  try {
    savePlaybackPage(p);
  } catch (e) { /* file:// или запрет history — не критично */ }
}

// Раздача кончилась: ищем следующую серию (при переходе через сезон — в другой раздаче).
async function onReleaseEnd() {
  const id = lastSourceId || filmId;
  const season = PP.season() || selectedSeason || 0;
  const ep = PP.episode() || 0;
  if (!id || !season || !ep || !lastSourceItems.length) {
    flashFilmNote(t('endOfSeries'));
    return;
  }
  const want = seasonEpisodeCount(id, season);
  let nextSeason = season, nextEp = ep + 1;
  if (want && ep >= want) { nextSeason = season + 1; nextEp = 1; }
  selectedSeason = nextSeason;
  selectedEpisode = nextEp;
  renderSeasonChips(lastSourceItems, id);
  renderEpisodeGrid(id);
  const ok = await playEpisode(id, nextSeason, nextEp);
  if (!ok) flashFilmNote(t('endOfSeries'));
}

// Escape на карточке сериала — остановить просмотр (страница остаётся открытой).
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Escape' || e.defaultPrevented || e.target?.closest?.('.profile-menu')) return;
  if (PP.playing()) {
    PP.stop();
    flashFilmNote(t('viewingClosed'));
  }
});

function restoreWatchSelection() {
  const entry = historyEntry(filmId);
  if (!entry) return;
  if (startMagnet === entry.magnet && startFile === entry.file) {
    if (!filmParams.has('pos')) startPos = entry.position || 0;
    if (!wantVoice) wantVoice = entry.voice || '';
  }
  const explicit = ['season', 'ep', 'magnet', 'file'].some((key) => filmParams.has(key));
  if (explicit) return;
  wantSeason = entry.season || 0;
  wantEp = entry.episode || 0;
  if (!wantVoice) wantVoice = entry.voice || '';
  if (wantPlay && entry.magnet) {
    startMagnet = entry.magnet;
    startFile = typeof entry.file === 'number' ? entry.file : -1;
    startPos = entry.position || 0;
  }
}

function resumeSavedPlayback() {
  if (savedPlaybackRestored || !seriesUiReady || !PP.available || !startMagnet) return;
  // Film metadata can arrive after the initial page bootstrap. Restore once
  // when the series player becomes available, without replacing a manual pick.
  savedPlaybackRestored = true;
  wantPlay = false;
  if (PP.playing()) return;
  PP.start({
    id: filmId,
    magnet: startMagnet,
    release: filmParams.get('rt') || '',
    file: startFile,
    season: wantSeason,
    ep: wantEp,
    pos: filmParams.has('pos') || startPos > 0 ? startPos : undefined,
    voice: wantVoice,
  });
}

async function initFilmPage() {
  await loadEpisodeHistory(filmId);
  restoreWatchSelection();
  if (!filmId) {
    filmTitleEl.textContent = t('empty');
    detailsEl.hidden = false;
    if (filmNote) {
      filmNote.textContent = t('empty');
      filmNote.hidden = false;
    }
    return;
  }
  // Каталог положил карточку в sessionStorage; при прямом заходе данных нет — берём из API.
  currentItem = loadStoredItem(filmId) || { id: filmId, imdb_id: filmId.indexOf('tt') === 0 ? filmId : '' };
  showDetails(currentItem);
  updateResumeBtn(currentItem);
  if (filmId.indexOf('tt') === 0) fetchFilmDetails(filmId, 3);
  if (filmId.indexOf('tmdb-') === 0) await fetchFilmDetails(filmId, 0);
  setupSeriesUi();
  // Продолжение просмотра из истории: в адресе есть конкретная раздача — играем её сразу,
  // не дожидаясь поиска источников (сетка серий подтянется вместе с файлами раздачи).
  resumeSavedPlayback();
  loadSources(currentItem, { play: wantPlay && !startMagnet, series: seriesUiReady });
}

onLang(() => {
  if (currentItem) showDetails(currentItem);
  sourcesTitle.textContent = t('sourcesTitle');
  updateSourceLabels();
  if (lastSourceItems.length) renderSources(lastSourceItems, lastSourceId);
  else if (seriesUiReady) showSeriesSkeleton();
  syncWatchBtn();
  updateResumeBtn(currentItem);
  translateFilmPage();
  syncFilmLayout();
  if (seriesUiReady) {
    renderSeasonChips(lastSourceItems, lastSourceId);
    renderSeasonVoices(lastSourceItems, lastSourceId);
    renderEpisodeGrid(lastSourceId);
  }
});
onAuth(() => updateResumeBtn(currentItem));

applyLang();
initAuth().then(() => Personal.load()).catch(() => {}).then(initFilmPage);

window.addEventListener('retryplayback', () => watchNow());
