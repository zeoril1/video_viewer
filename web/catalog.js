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
    showCatalogLoading();
  }
  const gen = catalogGen;

  const q = searchEl.value.trim();
  const params = new URLSearchParams({ page: String(page), per_page: String(PER_PAGE) });
  if (q) params.set('q', q);
  // Explicit 'all' also works with servers that mishandle an omitted section.
  params.set('section', currentSection);
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
      grid.setAttribute('aria-busy', 'false');
      updateSentinel();
    }
  }
}

function showCatalogLoading() {
  sentinel.hidden = true;
  grid.setAttribute('aria-busy', 'true');
  grid.innerHTML = Array.from({ length: 6 }, () =>
    '<div class="card catalog-skeleton" aria-hidden="true"><div class="thumb"></div>'
    + '<div class="meta"><div class="skeleton-line"></div><div class="skeleton-line short"></div></div></div>'
  ).join('');
  emptyEl.hidden = false;
  emptyEl.textContent = t('catalogLoading');
}

function render() {
  renderList(items);

  if (items.length === 0) {
    emptyEl.hidden = false;
    // Пока локальных совпадений нет — идёт поиск на IMDb.
    emptyEl.textContent = searchEl.value.trim() ? t('searching') : t('empty');
  }
  if (items.length && !grid.children.length && document.getElementById('hide-watched')?.checked) {
    emptyEl.hidden = false;
    emptyEl.textContent = 'Загруженные фильмы уже просмотрены. Снимите фильтр или загрузите следующие.';
  }
  updateSentinel();
}

// Карточка — ссылка на страницу фильма: ?tv=1 и обычный клик ведут себя одинаково,
// состояние (kind/tmdb_id/…) передаём через sessionStorage (см. VV.storeItem).

// Постер: у TMDB указываем несколько размеров (srcset) — браузер скачает самый лёгкий
// подходящий, поэтому ряд карточек заполняется картинками в разы быстрее (w500 ≈ 80 КБ на
// карточку, w342 ≈ 35 КБ). Чужие адреса (Кинопоиск и т. п.) отдаём как есть.
const TMDB_IMG = 'https://image.tmdb.org/t/p/';

function posterImgHtml(url) {
  if (!url) return '<div class="placeholder">🎬</div>';
  let attrs;
  if (url.indexOf(TMDB_IMG) === 0) {
    const path = url.slice(TMDB_IMG.length).split('/').slice(1).join('/');
    const at = (w) => TMDB_IMG + w + '/' + path;
    attrs = ` src="${escapeHtml(posterSrc(at('w342')))}"` +
      ` srcset="${escapeHtml(posterSrc(at('w185')))} 185w, ${escapeHtml(posterSrc(at('w342')))} 342w, ${escapeHtml(posterSrc(at('w500')))} 500w"` +
      ` data-poster-fallback="${escapeHtml(posterSrc(url))}"` +
      ` sizes="(max-width: 700px) 50vw, (max-width: 1100px) 33vw, 260px"`;
  } else {
    attrs = ` src="${escapeHtml(posterSrc(url))}"`;
  }
  return `<img${attrs} alt="" loading="lazy" decoding="async" />`;
}

// Постер проявляется по факту загрузки: пока картинки нет, видна градиентная заглушка
// (раньше был чёрный прямоугольник — карточка выглядела «прогруженной наполовину»).
function markPosterLoaded(scope) {
  const img = scope.querySelector('.thumb img');
  if (!img) return;
  // If a resized candidate fails, retry the original URL once without srcset.
  const retryOriginal = () => {
    const fallback = img.dataset.posterFallback;
    if (!fallback) return;
    delete img.dataset.posterFallback;
    img.removeAttribute('srcset');
    img.removeAttribute('sizes');
    img.src = fallback;
  };
  img.addEventListener('error', retryOriginal, { once: true });
  if (img.complete && img.naturalWidth > 0) img.classList.add('loaded');
  else {
    img.addEventListener('load', () => img.classList.add('loaded'), { once: true });
    if (img.complete && img.naturalWidth === 0) retryOriginal();
  }
}

function makeCard(it) {
  const card = document.createElement('a');
  card.className = 'card';
  card.href = filmUrl(it);

  const thumb = posterImgHtml(it.poster);

  card.innerHTML = `
    <div class="thumb">
      ${thumb}
      <span class="play">▶</span>
    </div>
    <div class="meta">${cardMetaHtml(it)}</div>
  `;
  markPosterLoaded(card);
  const status = Personal.get('watched', it.imdb_id || it.id) ? t('watched')
    : watchHistory.some(e => e.film_id === (it.imdb_id || it.id)) ? t('watching') : '';
  if (status) {
    const badge = document.createElement('span');
    badge.className = 'catalog-status';
    badge.textContent = status;
    card.querySelector('.thumb').append(badge);
  }

  card.addEventListener('click', () => storeItem(it));

  enrichCardWhenVisible(it, card);
  return card;
}

function renderList(list) {
  grid.innerHTML = '';
  for (const it of list) { if (!document.getElementById('hide-watched')?.checked || !Personal.get('watched', it.imdb_id || it.id)) grid.appendChild(makeCard(it)); }
  emptyEl.hidden = list.length > 0;
}

function appendCards(list) {
  for (const it of list) { if (!document.getElementById('hide-watched')?.checked || !Personal.get('watched', it.imdb_id || it.id)) grid.appendChild(makeCard(it)); }
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
  if (grid.getAttribute('aria-busy') === 'true') return;
  if (currentPage >= totalPages) {
    allLoaded = true;
    updateSentinel();
    return;
  }
  loadingMore = true;
  fetchPage(currentPage + 1, true);
}

// Compact catalogue metadata; full credits remain on the film page.
function cardMetaHtml(it) {
  const rating = fmtRating(it);
  const head =
    '<div class="card-head">' +
    '<h3 class="title">' + escapeHtml(dispTitle(it)) + '</h3>' +
    '</div>';
  const year = it.year ? String(it.year) : '';
  const dur = fmtDuration(it.movie_length || it.duration);
  const genres = (it.genres || []).map(dispGenre).join(', ');
  return (
    head +
    '<div class="card-summary">'
    + (rating ? `<span class="card-rating">${escapeHtml(rating)}</span>` : '')
    + (year ? `<span class="card-year">${escapeHtml(year)}</span>` : '')
    + (dur ? `<span class="card-runtime">${escapeHtml(dur)}</span>` : '')
    + '</div>'
    + (genres ? `<div class="card-line">${escapeHtml(genres)}</div>` : '')
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
      const html = cardMetaHtml(f);
      // Перерисовываем только если текст реально изменился (иначе карточка мигает без причины).
      if (meta && meta.innerHTML !== html) meta.innerHTML = html;
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
  if (searchEl.value.trim() || !VV.user || !watchHistory.length) {
    continueSec.hidden = true;
    continueList.innerHTML = '';
    return;
  }
  continueSec.hidden = false;
  continueTitle.textContent = t('continueWatching');
  continueList.innerHTML = '';
  watchHistory.forEach((e) => {
    const card = document.createElement('article');
    card.className = 'continue-card';
    const poster = posterImgHtml(e.poster_url);
    const sub = isSeriesKind(e.kind) && e.season > 0
      ? t('seasonLabel') + ' ' + e.season + (e.episode > 0 ? ' · ' + t('episodeLabel') + ' ' + e.episode : '')
      : '';
    const pct = e.duration > 0 ? Math.min(100, Math.max(0, Math.round((e.position / e.duration) * 100))) : 0;
    card.innerHTML = `
      <a class="continue-link" href="${escapeHtml(resumeUrl(e))}">
      <div class="thumb">${poster}</div>
      <div class="continue-meta">
        <div class="continue-head">
        <div class="continue-name">${escapeHtml(dispTitle(e))}</div>
        <span class="continue-action"><svg viewBox="0 0 28 28" aria-hidden="true"><circle cx="14" cy="14" r="14" fill="currentColor"/><path d="m11 8 9 6-9 6Z" fill="var(--on-accent)"/></svg>${t('resume')}</span>
        </div>
        ${sub ? `<div class="continue-sub">${escapeHtml(sub)}</div>` : ''}
        <div class="continue-progress"><div class="continue-bar" style="width:${pct}%"></div></div>
        <div class="continue-time">${fmtTime(e.position)} / ${fmtTime(e.duration)}</div>
      </div>
      </a>
      <button class="continue-remove" type="button" title="${t('removeFromHistory')}" aria-label="${escapeHtml(t('removeFromHistory') + ': ' + dispTitle(e))}">✕</button>
    `;
    markPosterLoaded(card);
    card.querySelector('.continue-remove').addEventListener('click', () => removeHistoryEntry(e.film_id));
    card.querySelector('.continue-link').addEventListener('click', (ev) => {
      if (ev.ctrlKey || ev.metaKey || ev.shiftKey || ev.altKey) return;
      ev.preventDefault();
      resumeItem(e);
    });
    continueList.appendChild(card);
  });
}

function resumeUrl(e) {
  const p = new URLSearchParams({ id: e.film_id });
  if (e.magnet) p.set('magnet', e.magnet);
  if (typeof e.file === 'number' && e.file >= 0) p.set('file', String(e.file));
  if (typeof e.track === 'number') p.set('track', String(e.track));
  if (e.season) p.set('season', String(e.season));
  if (e.episode) p.set('ep', String(e.episode));
  if (e.voice) p.set('voice', e.voice);
  if (e.position > 0) p.set('pos', String(Math.round(e.position)));
  if (e.quality) p.set('quality', e.quality);
  if (typeof e.subs === 'number') p.set('subs', String(e.subs));
  if (isSeriesKind(e.kind)) {
    p.set('autoplay', '1');
    return '/film.html?' + p.toString();
  }
  return '/watch.html?' + p.toString();
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
  const url = new URL(resumeUrl(e), location.href);
  go(playbackPageUrl(url.pathname, url.searchParams));
}

// ---- Разделы, жанры, сортировка ----

let metaGeneration = 0;
async function refreshMeta() {
  const generation = ++metaGeneration;
  const params = new URLSearchParams();
  const q = searchEl.value.trim();
  if (q) params.set('q', q);
  params.set('released', onlyReleased ? '1' : '0');
  if (currentGenre) params.set('genre', currentGenre);

  try {
    const res = await fetch('/api/catalog/meta?' + params.toString());
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data = await res.json();
    if (generation !== metaGeneration) return;
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
    b.setAttribute('aria-pressed', String(active));
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
    drop.id = 'collection-menu-' + s.key;
    const toggle = document.createElement('button');
    toggle.type = 'button';
    toggle.className = 'collection-toggle';
    toggle.textContent = '⌄';
    toggle.setAttribute('aria-label', (lang === 'ru' ? 'Подборки: ' : 'Collections: ') + label);
    toggle.setAttribute('aria-controls', drop.id);
    toggle.setAttribute('aria-expanded', 'false');
    toggle.addEventListener('click', () => {
      const open = !wrap.classList.contains('menu-open');
      closeCollections();
      wrap.classList.toggle('menu-open', open);
      toggle.setAttribute('aria-expanded', String(open));
    });
    wrap.addEventListener('keydown', ev => {
      if (ev.key === 'Escape') {
        closeCollections();
        toggle.focus();
      }
    });
    wrap.appendChild(toggle);
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

function closeCollections() {
  sectionsEl.querySelectorAll('.menu-open').forEach(menu => {
    menu.classList.remove('menu-open');
    menu.querySelector('.collection-toggle').setAttribute('aria-expanded', 'false');
  });
}
document.addEventListener('click', ev => {
  if (!ev.target.closest('.sec-menu')) closeCollections();
});

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
  refreshMeta();
});

// ---- Поиск: серверный поиск по каталогу → IMDb (on-demand) ----

let searchTimer = null;
searchEl.addEventListener('input', () => {
  clearTimeout(searchTimer);
  catalogGen++;
  metaGeneration++;
  loadingMore = false;
  renderContinue();
  const q = searchEl.value.trim();
  if (!q) {
    fetchPage(1);
    refreshMeta();
    return;
  }
  // Ищем по всему каталогу: вкладки потом отфильтруют результат.
  currentCollection = '';
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

window.addEventListener('personalchange', () => render());
document.getElementById('hide-watched')?.addEventListener('change', () => render());

const filterToggle = document.getElementById('filter-toggle');
filterToggle?.addEventListener('click', () => {
  const open = document.getElementById('catalog-filters').classList.toggle('filters-open');
  filterToggle.setAttribute('aria-expanded', String(open));
});
document.getElementById('catalog-reset')?.addEventListener('click', () => {
  clearTimeout(searchTimer);
  searchEl.value = '';
  currentSection = 'all';
  currentCollection = '';
  currentGenre = '';
  currentSort = 'year';
  onlyReleased = true;
  releasedEl.checked = true;
  document.getElementById('hide-watched').checked = false;
  genreEl.value = '';
  sortEl.value = 'year';
  renderSections(metaKinds);
  renderContinue();
  fetchPage(1);
  refreshMeta();
});
