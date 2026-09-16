'use strict';

/* Страница каталога (/index.html): сетка фильмов, разделы/жанры/сортировка, поиск,
 * «Продолжить просмотр». Карточка ведёт на отдельную страницу фильма
 * (/film.html?id=…), просмотр — на /watch.html. */

const grid = document.getElementById('grid');
const emptyEl = document.getElementById('empty');
const sectionsEl = document.getElementById('sections');
const genreEl = document.getElementById('genre');
const sortEl = document.getElementById('sort');
const sentinel = document.getElementById('sentinel');
const releasedEl = document.getElementById('released');
const continueSec = document.getElementById('continue');
const continueTitle = document.getElementById('continue-title');
const continueList = document.getElementById('continue-list');

const PER_PAGE = 30;

// Варианты сортировки; к «Популярному» не применяются (там порядок чарта).
const SORTS = [
  { key: 'year', ru: 'По дате выпуска', en: 'Release date' },
  { key: 'rating', ru: 'По рейтингу', en: 'Rating' },
  { key: 'title', ru: 'По названию', en: 'Title' },
];

// Разделы каталога; отдельного «Популярное» нет — оно в подменю категорий (COLLECTIONS).
const SECTIONS = [
  { key: 'movie', ru: 'Фильмы', en: 'Movies' },
  { key: 'series', ru: 'Сериалы', en: 'TV Series' },
  { key: 'cartoon', ru: 'Мультфильмы', en: 'Cartoons' },
  { key: 'anime', ru: 'Аниме', en: 'Anime' },
  { key: 'tv_movie', ru: 'ТВ-фильмы', en: 'TV Movies' },
  { key: 'short', ru: 'Короткометражки', en: 'Shorts' },
  { key: 'video', ru: 'Видео', en: 'Video' },
  { key: 'episode', ru: 'Эпизоды', en: 'Episodes' },
  { key: 'other', ru: 'Другое', en: 'Other' },
];

// Подборки категорий (hover-меню): «Лучшие…» — чарт top_rated (IMDb+TMDB),
// «Популярные…» — чарт популярности. Пустой ключ — обычный список категории.
const COLLECTIONS = {
  movie: [
    { key: 'best', ru: 'Лучшие фильмы', en: 'Best movies' },
    { key: 'popular', ru: 'Популярные фильмы', en: 'Popular movies' },
  ],
  series: [
    { key: 'best', ru: 'Лучшие сериалы', en: 'Best series' },
    { key: 'popular', ru: 'Популярные сериалы', en: 'Popular series' },
  ],
};

let items = [];
let currentPage = 1;
let totalPages = 1;
let currentSort = 'year'; // сортировка: year (по умолчанию) | rating | title
let onlyReleased = true;  // «только вышедшие» (по умолчанию включено)
let loadingMore = false;  // идёт ли догрузка следующей страницы
let allLoaded = false;    // все страницы загружены (больше догружать нечего)
let catalogGen = 0;       // поколение каталога: устаревшие ответы игнорируются
let currentSection = 'all';
let currentGenre = '';
let currentCollection = ''; // подборка внутри раздела (best/popular/'');
let metaKinds = {};
let metaGenres = [];

// Страница каталога; append=true — догрузка в конец сетки при прокрутке.
async function fetchPage(page, append) {
  if (!append) {
    // Смена раздела/жанра/поиска — сбрасываем прокрутку и открываем новое поколение запросов.
    catalogGen++;
    loadingMore = false;
    allLoaded = false;
  }
  const gen = catalogGen;

  const q = searchEl.value.trim();
  const params = new URLSearchParams({ page: String(page), per_page: String(PER_PAGE) });
  if (q) params.set('q', q);
  // Секцию шлём всегда, кроме «Все» — вкладки фильтруют выдачу поиска по разделу.
  if (currentSection !== 'all') params.set('section', currentSection);
  if (currentGenre) params.set('genre', currentGenre);
  if (currentSection !== 'popular') params.set('sort', currentSort);
  if (currentCollection) params.set('collection', currentCollection);
  params.set('released', onlyReleased ? '1' : '0');

  try {
    const res = await fetch('/api/catalog?' + params.toString());
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data = await res.json();
    // Ответ устарел (каталог перезагрузили, пока летел запрос) — не перемешиваем списки.
    if (gen !== catalogGen) return;
    const pageItems = data.items || [];
    currentPage = data.page || 1;
    totalPages = data.total_pages || 1;

    // Чарт «Популярное» может быть пустым (нет TMDB/IMDb) — фолбэк на «Все».
    if (pageItems.length === 0 && !q && currentSection === 'popular' && currentGenre === '') {
      currentSection = 'all';
      renderSections(metaKinds);
      // Счётчики вкладок после смены секции могли измениться — обновляем.
      refreshMeta();
      return fetchPage(1, false);
    }

    if (append) {
      items = items.concat(pageItems);
      appendCards(pageItems);
    } else {
      items = pageItems;
      render();
    }
    allLoaded = currentPage >= totalPages;
  } catch (err) {
    if (gen !== catalogGen) return; // устаревшая ошибка — не трогаем новый список
    if (!append) {
      grid.innerHTML = '';
      emptyEl.textContent = t('empty') + ' (' + err.message + ')';
      emptyEl.hidden = false;
    }
    // При ошибке догрузки список не трогаем — следующий скролл повторит.
  } finally {
    if (gen === catalogGen) {
      loadingMore = false;
      updateSentinel();
    }
  }
}

function render() {
  renderList(items);

  if (items.length === 0) {
    emptyEl.hidden = false;
    // Пока локальных совпадений нет — идёт поиск на IMDb.
    emptyEl.textContent = searchEl.value.trim() ? t('searching') : t('empty');
  }
  updateSentinel();
}

// Карточка — ссылка на страницу фильма: ?tv=1 и обычный клик ведут себя одинаково,
// состояние (kind/tmdb_id/…) передаём через sessionStorage (см. VV.storeItem).
function makeCard(it) {
  const card = document.createElement('a');
  card.className = 'card';
  card.href = filmUrl(it);

  const thumb = it.poster
    ? `<img src="${escapeHtml(it.poster)}" alt="" loading="lazy" />`
    : `<div class="placeholder">🎬</div>`;

  card.innerHTML = `
    <div class="thumb">
      ${thumb}
      <span class="play">▶</span>
    </div>
    <div class="meta">${cardMetaHtml(it)}</div>
  `;

  card.addEventListener('click', () => storeItem(it));

  enrichCardWhenVisible(it, card);
  return card;
}

function renderList(list) {
  grid.innerHTML = '';
  for (const it of list) grid.appendChild(makeCard(it));
  emptyEl.hidden = list.length > 0;
}

function appendCards(list) {
  for (const it of list) grid.appendChild(makeCard(it));
  emptyEl.hidden = items.length > 0;
}

function updateSentinel() {
  sentinel.hidden = allLoaded || items.length === 0;
}

// ---- Бесконечная прокрутка (вместо постраничной пагинации) ----
let loadObserver = null;

function initInfiniteScroll() {
  loadObserver = new IntersectionObserver((entries) => {
    if (entries[0] && entries[0].isIntersecting) loadMore();
  }, { rootMargin: '800px 0px' });
  loadObserver.observe(sentinel);
}

function loadMore() {
  if (loadingMore || allLoaded) return;
  if (currentPage >= totalPages) {
    allLoaded = true;
    updateSentinel();
    return;
  }
  loadingMore = true;
  fetchPage(currentPage + 1, true);
}

// Тело карточки: название+рейтинг, альт. название·год·длительность, страна·жанры,
// режиссёр, актёры + бейджи; пустые строки не выводятся.
function cardMetaHtml(it) {
  const rating = fmtRating(it);
  const head =
    '<div class="card-head">' +
    '<h3 class="title">' + escapeHtml(dispTitle(it)) + '</h3>' +
    (rating ? '<span class="card-rating">' + rating + '</span>' : '') +
    '</div>';

  const alt = dispTitleAlt(it);
  const year = it.year ? String(it.year) : '';
  const dur = fmtDuration(it.movie_length || it.duration);
  const line2 = [alt, year, dur].filter(Boolean).join(' · ');

  const countries = (it.countries || []).join(', ');
  const genres = (it.genres || []).map(dispGenre).join(', ');
  const line3 = [countries, genres].filter(Boolean).join(' · ');

  const director = dispDirector(it);
  const line4 = director ? t('directorLabel') + ': ' + escapeHtml(director) : '';
  const actors = dispActors(it).join(', ');
  const line5 = actors ? t('actorsLabel') + ': ' + escapeHtml(actors) : '';

  const tags = it.seasons
    ? `<span class="badge">${it.seasons} ${t('seasonsOf')}</span>`
    : '';

  return (
    head +
    (line2 ? `<div class="card-line">${escapeHtml(line2)}</div>` : '') +
    (line3 ? `<div class="card-line">${escapeHtml(line3)}</div>` : '') +
    (line4 ? `<div class="card-line">${line4}</div>` : '') +
    (line5 ? `<div class="card-line">${line5}</div>` : '') +
    (tags ? `<div class="tags">${tags}</div>` : '')
  );
}

// Ленивое обогащение карточек: детали грузятся, когда карточка в кадре (GET /api/films/{id}),
// повторные запросы за сессию исключены.
const enrichedFetched = new Set(); // id, для которых детали уже запрашивались
let enrichObserver = null;

function hasFilmExtras(f) {
  return !!(f && (f.director || f.actors || f.movie_length || (f.countries && f.countries.length)));
}

// Сервер обогащает из Wikidata в фоне — при пустых extras переспрашиваем (данные за 1–3 с);
// так же переспрашиваем, когда данные есть, но имена ещё не переведены (peoplePending).
async function fetchCardEnrich(el, id, retries) {
  try {
    const r = await fetch('/api/films/' + encodeURIComponent(id));
    if (!r.ok) return;
    const f = await r.json();
    if (hasFilmExtras(f)) {
      const meta = el.querySelector('.meta');
      if (meta) meta.innerHTML = cardMetaHtml(f);
      if (!peoplePending(f)) return;
    }
  } catch (e) {
    return;
  }
  if (retries > 0) setTimeout(() => fetchCardEnrich(el, id, retries - 1), 3000);
}

function enrichCardWhenVisible(it, card) {
  if (!it.imdb_id) return;
  const filled = !!(it.director || it.actors || it.movie_length || (it.countries && it.countries.length));
  if (filled && !peoplePending(it)) return;
  if (enrichedFetched.has(it.imdb_id)) return;

  if (!enrichObserver) {
    enrichObserver = new IntersectionObserver((entries, obs) => {
      for (const e of entries) {
        obs.unobserve(e.target);
        const id = e.target.dataset.imdbId;
        if (!id || enrichedFetched.has(id)) continue;
        enrichedFetched.add(id);
        fetchCardEnrich(e.target, id, 3);
      }
    }, { rootMargin: '200px' });
  }
  card.dataset.imdbId = it.imdb_id;
  enrichObserver.observe(card);
}

// ---- «Продолжить просмотр» (история текущего пользователя) ----

function renderContinue() {
  if (!VV.user || !watchHistory.length) {
    continueSec.hidden = true;
    continueList.innerHTML = '';
    return;
  }
  continueSec.hidden = false;
  continueTitle.textContent = t('continueWatching');
  continueList.innerHTML = '';
  watchHistory.forEach((e) => {
    const card = document.createElement('div');
    card.className = 'continue-card';
    const poster = e.poster_url
      ? `<img src="${escapeHtml(e.poster_url)}" alt="" loading="lazy" />`
      : `<div class="placeholder">🎬</div>`;
    const sub = isSeriesKind(e.kind) && e.season > 0
      ? t('seasonLabel') + ' ' + e.season + (e.episode > 0 ? ' · ' + t('episodeLabel') + ' ' + e.episode : '')
      : '';
    const pct = e.duration > 0 ? Math.min(100, Math.max(0, Math.round((e.position / e.duration) * 100))) : 0;
    card.innerHTML = `
      <div class="thumb">${poster}</div>
      <div class="continue-meta">
        <div class="continue-name">${escapeHtml(dispTitle(e))}</div>
        ${sub ? `<div class="continue-sub">${escapeHtml(sub)}</div>` : ''}
        <div class="continue-progress"><div class="continue-bar" style="width:${pct}%"></div></div>
        <div class="continue-time">${fmtTime(e.position)} / ${fmtTime(e.duration)}</div>
      </div>
      <button class="continue-remove" type="button" title="${t('removeFromHistory')}">✕</button>
    `;
    card.addEventListener('click', (ev) => {
      if (ev.target.closest('.continue-remove')) {
        removeHistoryEntry(e.film_id);
        return;
      }
      resumeItem(e);
    });
    continueList.appendChild(card);
  });
}

// Продолжение просмотра: раздача, серия и позиция берутся из записи истории.
// Сериалы продолжаем на карточке фильма (там же сезоны/серии/озвучки и плеер),
// фильмы — на странице просмотра.
function resumeItem(e) {
  if (!e || !e.magnet) return;
  storeItem({
    imdb_id: e.film_id, id: e.film_id, title: e.title, title_ru: e.title_ru,
    kind: e.kind, year: e.year, poster_url: e.poster_url,
  });
  const file = typeof e.file === 'number' ? e.file : -1;
  const pos = e.position || 0;
  if (isSeriesKind(e.kind)) {
    const p = new URLSearchParams({ id: e.film_id });
    if (e.season) p.set('season', String(e.season));
    if (e.episode) p.set('ep', String(e.episode));
    p.set('magnet', e.magnet);
    if (file >= 0) p.set('file', String(file));
    if (pos) p.set('pos', String(Math.round(pos)));
    p.set('autoplay', '1');
    go('/film.html?' + p.toString());
    return;
  }
  go(watchUrl(e.film_id, {
    magnet: e.magnet, file: file, season: e.season || 0, ep: e.episode || 0, pos: pos,
  }));
}

// ---- Разделы, жанры, сортировка ----

async function refreshMeta() {
  const params = new URLSearchParams();
  const q = searchEl.value.trim();
  if (q) params.set('q', q);
  if (currentGenre) params.set('genre', currentGenre);

  try {
    const res = await fetch('/api/catalog/meta?' + params.toString());
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data = await res.json();
    metaKinds = data.sections || {};
    metaGenres = data.genres || [];
    renderSections(metaKinds);
    populateGenres(metaGenres);
  } catch (err) {
    console.warn('load meta failed:', err);
  }
}

function renderSections(kinds) {
  sectionsEl.innerHTML = '';
  // Сортировка скрыта в подборках — там порядок чарта; подборки есть только у «Фильмов»/«Сериалов».
  sortEl.hidden = currentCollection !== '';
  if (currentSection !== 'movie' && currentSection !== 'series') currentCollection = '';
  // «Все» — сумма по секциям, без псевдо-секции popular (её записи уже в категориях — двойной учёт).
  let total = 0;
  for (const k in kinds) if (k !== 'popular') total += kinds[k];

  const select = (key, coll) => {
    const c = coll || '';
    if (currentSection === key && currentCollection === c) return;
    currentSection = key;
    currentCollection = c;
    renderSections(metaKinds);
    fetchPage(1);
  };

  const mkButton = (key, label, count, active) => {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = 'section-btn' + (active ? ' active' : '');
    b.dataset.section = key;
    b.innerHTML = `${escapeHtml(label)} <span class="count">${count}</span>`;
    return b;
  };

  const allBtn = mkButton('all', t('allSections'), total, currentSection === 'all');
  allBtn.addEventListener('click', () => select('all', ''));
  sectionsEl.appendChild(allBtn);

  for (const s of SECTIONS) {
    const c = kinds[s.key] || 0;
    if (c <= 0) continue;
    const label = lang === 'ru' ? s.ru : s.en;
    const active = currentSection === s.key;
    const defs = COLLECTIONS[s.key];
    if (!defs || !defs.length) {
      const b = mkButton(s.key, label, c, active);
      b.addEventListener('click', () => select(s.key, ''));
      sectionsEl.appendChild(b);
      continue;
    }
    const wrap = document.createElement('div');
    wrap.className = 'sec-menu';
    const btn = mkButton(s.key, label, c, active);
    btn.classList.add('has-menu');
    btn.addEventListener('click', () => select(s.key, ''));
    wrap.appendChild(btn);
    const drop = document.createElement('div');
    drop.className = 'sec-drop';
    const ul = document.createElement('ul');
    for (const o of defs) {
      const li = document.createElement('li');
      const item = document.createElement('button');
      item.type = 'button';
      item.className = 'sec-drop-item' + (currentSection === s.key && currentCollection === o.key ? ' active' : '');
      item.textContent = lang === 'ru' ? o.ru : o.en;
      item.addEventListener('click', () => select(s.key, o.key));
      li.appendChild(item);
      ul.appendChild(li);
    }
    drop.appendChild(ul);
    wrap.appendChild(drop);
    sectionsEl.appendChild(wrap);
  }
}

function populateGenres(genres) {
  genreEl.innerHTML = '';
  const all = document.createElement('option');
  all.value = '';
  all.textContent = t('allGenres');
  genreEl.appendChild(all);
  for (const g of genres) {
    const o = document.createElement('option');
    o.value = g; // значение остаётся английским — так фильтрует сервер
    o.textContent = dispGenre(g);
    genreEl.appendChild(o);
  }
  genreEl.value = currentGenre;
}

function populateSort() {
  sortEl.innerHTML = '';
  for (const s of SORTS) {
    const o = document.createElement('option');
    o.value = s.key;
    o.textContent = lang === 'ru' ? s.ru : s.en;
    sortEl.appendChild(o);
  }
  sortEl.value = currentSort;
}

genreEl.addEventListener('change', () => {
  currentGenre = genreEl.value;
  fetchPage(1);
  refreshMeta();
});

sortEl.addEventListener('change', () => {
  currentSort = sortEl.value;
  fetchPage(1);
});

releasedEl.addEventListener('change', () => {
  onlyReleased = releasedEl.checked;
  fetchPage(1);
});

// ---- Поиск: серверный поиск по каталогу → IMDb (on-demand) ----

let searchTimer = null;
searchEl.addEventListener('input', () => {
  clearTimeout(searchTimer);
  const q = searchEl.value.trim();
  if (!q) {
    fetchPage(1);
    refreshMeta();
    return;
  }
  // Ищем по всему каталогу: вкладки потом отфильтруют результат.
  if (currentSection !== 'all') {
    currentSection = 'all';
    renderSections(metaKinds);
  }
  searchTimer = setTimeout(() => {
    runSearch(q);
    refreshMeta();
  }, 350);
});

async function runSearch(q) {
  // «Идёт поиск…» показываем сразу: сервер ищет и в БД, и в IMDb/TMDB (это пара секунд).
  grid.innerHTML = '';
  sentinel.hidden = true;
  emptyEl.hidden = false;
  emptyEl.textContent = t('searching');

  await fetchPage(1);
  if (!isCurrentQuery(q)) return;
  if (items.length === 0) {
    emptyEl.textContent = t('notFoundImdb');
  }
}

function isCurrentQuery(q) {
  return searchEl.value.trim().toLowerCase() === q.toLowerCase();
}

// ---- Инициализация ----
onLang(() => {
  renderSections(metaKinds);
  populateGenres(metaGenres);
  populateSort();
  render();
  renderContinue();
});
onAuth(renderContinue);

applyLang();
refreshMeta();
fetchPage(1);
initInfiniteScroll();
initAuth();

if (DEBUG) showDebug();
