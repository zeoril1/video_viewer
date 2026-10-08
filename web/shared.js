'use strict';

// Keep original URLs in catalogue/history; only image requests use our server.
function posterSrc(url) {
  if (!url) return '';
  try {
    const parsed = new URL(url);
    if (parsed.protocol === 'https:' && ['image.tmdb.org', 'm.media-amazon.com',
      'images-na.ssl-images-amazon.com', 'ia.media-imdb.com'].includes(parsed.hostname)) {
      return '/api/poster?url=' + encodeURIComponent(url);
    }
  } catch (_) {}
  return url;
}

/* Общий модуль всех страниц сайта (index/film/watch/iptv/login): язык, авторизация,
 * история просмотра, диагностика, утилиты. Подключается ПЕРВЫМ скриптом на каждой
 * странице; страничные модули (catalog.js/film.js/watch.js/...) пользуются его
 * глобальными функциями и вешают свои хуки через onLang()/onAuth(). */

// ---- Диагностика ----
// Логи — в консоль и (при ?debug или ошибке) в панель #player-debug (есть только
// на странице просмотра; на остальных логи идут только в консоль).
const DEBUG = new URLSearchParams(location.search).has('debug');
let debugLines = [];

function debugEl() {
  return document.getElementById('player-debug');
}

function renderDebug() {
  const el = debugEl();
  if (el) el.textContent = debugLines.join('\n');
}

function dbg(msg) {
  const line = '[' + new Date().toLocaleTimeString() + '] ' + msg;
  debugLines.push(line);
  if (debugLines.length > 40) debugLines.shift();
  console.log(line);
  const el = debugEl();
  if (el && !el.hidden) renderDebug();
}

function showDebug() {
  const el = debugEl();
  if (!el) return;
  el.hidden = false;
  renderDebug();
}

// ---- Локализация интерфейса ----
const I18N = {
  ru: {
    searchPlaceholder: 'Найти фильм или сериал',
    filters: 'Фильтры',
    resetFilters: 'Сбросить фильтры',
    catalogLoading: 'Загружаем каталог…',
    resume: 'Продолжить',
    watching: 'Смотрю',
    watched: 'Просмотрено',
    empty: 'Ничего не найдено.',
    searching: 'Идёт поиск...',
    notFoundImdb: 'Ничего не найдено в каталоге, IMDb и TMDB.',
    searchFailed: 'Не удалось выполнить поиск на IMDb.',
    noPlot: 'Описание пока недоступно.',
    noMagnet: 'К этому фильму пока не привязана магнет-ссылка — стриминг недоступен. Данные получены из IMDb.',
    close: 'Закрыть',
    catalog: 'Каталог',
    allEpisodes: 'Все серии',
    allSections: 'Все',
    allGenres: 'Все жанры',
    onlyReleased: 'Только вышедшие',
    magnet: 'магнет',
    sourcesTitle: 'Доступные варианты для просмотра',
    searchingSources: 'Ищем варианты для просмотра...',
    noSources: 'На трекере ничего не найдено по этому фильму.',
    sourceSearchFailed: 'Не удалось получить варианты для просмотра (трекер недоступен).',
    sourceSearchTimeout: 'Не удалось получить варианты для просмотра (таймаут). Попробуйте позже.',
    seeders: 'сидов',
    watch: '▶ Смотреть',
    audioTracks: 'Звуковые дорожки',
    tracksLoading: 'Получаю дорожки… ищу пиров',
    tracksUnavailable: 'Не удалось получить дорожки (мало пиров). Попробуйте другой вариант или подождите.',
    trackFallback: 'Дорожка',
    subtitles: 'Субтитры',
    subsOff: 'Выкл',
    subsFallback: 'Субтитры',
    audioDub: 'Дублированный',
    audioMulti: 'Многоголосый',
    audioTwo: 'Двухголосый',
    audioOriginal: 'Оригинал',
    audioSingle: 'Одноголосый',
    audioSubs: 'Субтитры',
    audioNone: 'Без озвучки',
    qualitySource: 'Источник',
    episodesTitle: 'Серии',
    seasonLabel: 'Сезон',
    seasonFull: 'Полный',
    episodeLabel: 'Серия',
    prevEpisode: 'Предыдущая серия',
    nextEpisode: 'Следующая серия',
    endOfEpisodes: 'Это последняя серия раздачи.',
    endOfSeason: 'Конец сезона — следующая серия в другой раздаче.',
    endOfSeries: 'Это последняя серия сериала. Просмотр завершён.',
    viewingClosed: 'Просмотр закрыт.',
    ratingImdb: 'IMDB',
    ratingTmdb: 'TMDB',
    directorLabel: 'Режиссёр',
    actorsLabel: 'В ролях',
    hourShort: 'ч',
    minShort: 'мин',
    seasonsOf: 'сез.',
    playbackError: 'Не удалось воспроизвести этот вариант (возможно, видео в H.265/HEVC, который браузер не поддерживает). Выберите вариант с H.264 или другую дорожку.',
    codecFallback: 'Видео в H.265/HEVC — автоматически перекодируем в H.264 (1080p), это может занять время.',
    login: 'Войти',
    register: 'Регистрация',
    logout: 'Выйти',
    username: 'Логин',
    password: 'Пароль',
    authErrorShort: 'Логин: 3–32 символа (буквы, цифры, _ . -). Пароль — не короче 6 символов.',
    authErrorInvalid: 'Неверный логин или пароль.',
    authErrorTaken: 'Этот логин уже занят.',
    authErrorAuth: 'Авторизация недоступна (сервис работает без базы данных).',
    authErrorServer: 'Ошибка сервера. Попробуйте позже.',
    continueWatching: 'Продолжить просмотр',
    removeFromHistory: 'Удалить из истории',
    resumeFrom: 'Продолжить с',
    translationLabel: 'Перевод',
    voicesLabel: 'Озвучки',
    fullCollection: 'Полный сборник',
    episodesLoading: 'Загружаю серии…',
    seriesMetadataUnavailable: 'Данные сезонов временно недоступны.',
    seasonsLoading: 'Загружаю сезоны…',
    voicesLoading: 'Загружаю озвучки…',
    episodesNotFound: 'Серии этой раздачи не найдены.',
    episodesUnavailable: 'Не удалось получить серии этой раздачи (мало пиров). Попробуйте другой перевод.',
    noTranslations: 'Переводы (озвучки) для этого фильма не найдены.',
    iptvTitle: 'IPTV',
    iptvPlaylists: 'Плейлисты IPTV',
    iptvSave: 'Сохранить и обновить',
  },
  en: {
    searchPlaceholder: 'Search the catalog...',
    filters: 'Filters',
    resetFilters: 'Reset filters',
    catalogLoading: 'Loading catalog…',
    resume: 'Continue',
    watching: 'Watching',
    watched: 'Watched',
    empty: 'Nothing found.',
    searching: 'Searching...',
    notFoundImdb: 'Nothing found in the catalog, IMDb or TMDB.',
    searchFailed: 'IMDb search failed.',
    noPlot: 'Description is not available yet.',
    noMagnet: 'No magnet link is attached to this film yet — streaming is unavailable. Data from IMDb.',
    close: 'Close',
    catalog: 'Catalog',
    allEpisodes: 'All episodes',
    allSections: 'All',
    allGenres: 'All genres',
    onlyReleased: 'Only released',
    magnet: 'magnet',
    sourcesTitle: 'Available viewing options',
    searchingSources: 'Searching for viewing options...',
    noSources: 'Nothing found on the tracker for this film.',
    sourceSearchFailed: 'Could not fetch viewing options (tracker unavailable).',
    sourceSearchTimeout: 'Could not load viewing options (timeout). Try again later.',
    seeders: 'seeders',
    watch: '▶ Watch',
    audioTracks: 'Audio tracks',
    tracksLoading: 'Loading audio tracks… finding peers',
    tracksUnavailable: 'Could not load audio tracks (few peers). Try another option or wait.',
    trackFallback: 'Track',
    subtitles: 'Subtitles',
    subsOff: 'Off',
    subsFallback: 'Subtitles',
    audioDub: 'Dubbed',
    audioMulti: 'Multi',
    audioTwo: '2 voices',
    audioOriginal: 'Original',
    audioSingle: 'Single voice',
    audioSubs: 'Subtitles',
    audioNone: 'No dubbing',
    qualitySource: 'Source',
    episodesTitle: 'Episodes',
    seasonLabel: 'Season',
    seasonFull: 'Full',
    episodeLabel: 'Episode',
    prevEpisode: 'Previous episode',
    nextEpisode: 'Next episode',
    endOfEpisodes: 'This is the last episode of the release.',
    endOfSeason: 'End of season — the next episode is in another release.',
    endOfSeries: 'This is the last episode of the series. Playback finished.',
    viewingClosed: 'Viewing closed.',
    ratingImdb: 'IMDB',
    ratingTmdb: 'TMDB',
    directorLabel: 'Director',
    actorsLabel: 'Starring',
    hourShort: 'h',
    minShort: 'min',
    seasonsOf: 'seasons',
    playbackError: 'Could not play this option (possibly H.265/HEVC video not supported by your browser). Try an H.264 option or another track.',
    codecFallback: 'H.265/HEVC video — automatically transcoding to H.264 (1080p), this may take a while.',
    login: 'Log in',
    register: 'Sign up',
    logout: 'Log out',
    username: 'Username',
    password: 'Password',
    authErrorShort: 'Username: 3–32 chars (letters, digits, _ . -). Password must be at least 6 chars.',
    authErrorInvalid: 'Invalid username or password.',
    authErrorTaken: 'This username is already taken.',
    authErrorAuth: 'Auth is unavailable (service is running without a database).',
    authErrorServer: 'Server error. Try again later.',
    continueWatching: 'Continue watching',
    removeFromHistory: 'Remove from history',
    resumeFrom: 'Resume from',
    translationLabel: 'Translation',
    voicesLabel: 'Voices',
    fullCollection: 'Full collection',
    episodesLoading: 'Loading episodes…',
    seriesMetadataUnavailable: 'Season metadata is temporarily unavailable.',
    seasonsLoading: 'Loading seasons…',
    voicesLoading: 'Loading voices…',
    episodesNotFound: 'No episodes found in this release.',
    episodesUnavailable: 'Could not load episodes of this release (few peers). Try another translation.',
    noTranslations: 'No translations (dubs) found for this film.',
    iptvTitle: 'IPTV',
    iptvPlaylists: 'IPTV playlists',
    iptvSave: 'Save and refresh',
  },
};

let lang = 'ru';
try { lang = localStorage.getItem('lang') === 'en' ? 'en' : 'ru'; }
catch (_) { /* Language selection still works without browser storage. */ }
if (!I18N[lang]) lang = 'ru';

function t(key) {
  return (I18N[lang] && I18N[lang][key]) || I18N.ru[key] || key;
}

const GENRES_RU = {
  'Action': 'Боевик',
  'Adventure': 'Приключения',
  'Animation': 'Мультфильм',
  'anime': 'Аниме',
  'Biography': 'Биография',
  'Comedy': 'Комедия',
  'Crime': 'Криминал',
  'Documentary': 'Документальный',
  'Drama': 'Драма',
  'Family': 'Семейный',
  'Fantasy': 'Фэнтези',
  'Film-Noir': 'Нуар',
  'History': 'Исторический',
  'Horror': 'Ужасы',
  'Music': 'Музыка',
  'Musical': 'Мюзикл',
  'Mystery': 'Детектив',
  'Romance': 'Мелодрама',
  'Sci-Fi': 'Фантастика',
  'Sport': 'Спорт',
  'Thriller': 'Триллер',
  'War': 'Военный',
  'Western': 'Вестерн',
  'Reality-TV': 'Реалити-шоу',
  'Game-Show': 'Игровое шоу',
  'Talk-Show': 'Ток-шоу',
  'News': 'Новости',
};

// Жанр в выбранном языке (значение для фильтра остаётся английским).
function dispGenre(g) {
  if (lang === 'ru' && GENRES_RU[g]) return GENRES_RU[g];
  return g;
}

// Название и описание в выбранном языке (с фолбэком на английский).
function dispTitle(it) {
  if (lang === 'ru' && it.title_ru) return it.title_ru;
  return it.title || '';
}
function dispPlot(it) {
  if (lang === 'ru' && it.plot_ru) return it.plot_ru;
  return it.plot || '';
}

// Название на другом языке (RU-интерфейс → английское, EN → русское).
function dispTitleAlt(it) {
  if (lang === 'ru' && it.title) return it.title;
  return it.title_ru || '';
}

// Режиссёр и актёры на языке сайта: русские имена приходят отдельными полями director_ru/
// actors_ru (таблица person_names на сервере), director/actors — исходное (английское) написание.
function dispDirector(it) {
  if (lang === 'ru' && it.director_ru) return it.director_ru;
  return it.director || '';
}
function dispActors(it) {
  if (lang === 'ru' && it.actors_ru && it.actors_ru.length) return it.actors_ru;
  return it.actors || [];
}

// Имена ещё не переведены: сервер доберёт их из TMDB в фоне за 1–3 с — стоит переспросить карточку.
function peoplePending(it) {
  return lang === 'ru' && !!(it && it.people_pending);
}

// fmtRating — рейтинг в формате «IMDB/TMDB»: 6.7/8.2.
function fmtRating(it) {
  const f = (v) => (v ? Number(v).toFixed(1) : '');
  const imdb = f(it.rating || it.rating_imdb);
  const tm = f(it.rating_tmdb);
  if (imdb && tm) return '★ ' + imdb + '/' + tm;
  if (imdb) return '★ ' + imdb + ' ' + t('ratingImdb');
  if (tm) return '★ ' + tm + ' ' + t('ratingTmdb');
  return '';
}

// fmtDuration — длительность в минутах в «1 ч 48 мин».
function fmtDuration(min) {
  if (!min || min <= 0) return '';
  const h = Math.floor(min / 60);
  const m = min % 60;
  if (h > 0) return h + ' ' + t('hourShort') + ' ' + m + ' ' + t('minShort');
  return m + ' ' + t('minShort');
}

// fmtTime — секунды в «1:02:03» / «12:34».
function fmtTime(s) {
  if (!isFinite(s) || s < 0) s = 0;
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = Math.floor(s % 60);
  const mm = h > 0 ? String(m).padStart(2, '0') : String(m);
  return (h > 0 ? h + ':' : '') + mm + ':' + String(sec).padStart(2, '0');
}

function escapeHtml(s) {
  const div = document.createElement('div');
  div.textContent = s;
  return div.innerHTML;
}

function seasonLabel(k) {
  if (!k) return t('seasonFull');
  return t('seasonLabel') + ' ' + k;
}

function audioLabel(k) {
  const map = { dub: 'audioDub', multi: 'audioMulti', two: 'audioTwo', original: 'audioOriginal', single: 'audioSingle', subs: 'audioSubs' };
  return map[k] ? t(map[k]) : t('audioNone');
}

// HEVC/H.265 через MSE не декодируется — нужен серверный H.264.
function isHevcCodec(codec) {
  return !!codec && (codec.indexOf('hevc') === 0 || codec.indexOf('hvc1') === 0 || codec.indexOf('hev1') === 0);
}

// Сериал ли это; legacy-значения из БД (kind='animation', 'tv mini-series' с дефисом)
// учитываем — без этого у таких записей не было селектора сезонов/серий.
function isSeriesKind(kind) {
  switch (String(kind || '').toLowerCase()) {
    case 'tvseries':
    case 'tvminiseries':
    case 'tvepisode':
    case 'tvspecial':
    case 'animation':
    case 'tv mini-series':
    case 'tv-miniseries':
    case 'mini-series':
    case 'miniseries':
      return true;
  }
  return false;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---- Хуки страниц ----
// Страничные модули подписываются на смену языка и на загрузку истории просмотра.
const langHooks = [];
const authHooks = [];

function onLang(fn) { langHooks.push(fn); }
function onAuth(fn) { authHooks.push(fn); }

const langBtns = document.querySelectorAll('.lang-btn');
const searchEl = document.getElementById('search');

function applyLang() {
  langBtns.forEach((b) => b.classList.toggle('active', b.dataset.lang === lang));
  if (searchEl) searchEl.placeholder = t('searchPlaceholder');
  document.querySelectorAll('[data-i18n]').forEach((el) => {
    el.textContent = t(el.dataset.i18n);
  });
  document.querySelectorAll('[data-i18n-title]').forEach((el) => {
    el.title = t(el.dataset.i18nTitle);
  });
  // Подписываем другие модули (IPTV) — у них свои подписи и подсказки.
  document.dispatchEvent(new CustomEvent('vv:lang'));
  langHooks.forEach((fn) => {
    try { fn(); } catch (e) { dbg('lang hook: ' + e.message); }
  });
}

langBtns.forEach((b) => {
  b.addEventListener('click', () => {
    lang = b.dataset.lang;
    try { localStorage.setItem('lang', lang); } catch (_) {}
    applyLang();
  });
});

// ---- Авторизация и история просмотра ----
let currentUser = null;    // {id, username, role} текущего пользователя
let authDisabled = false;  // auth отключён (сервис без БД) — скрываем кнопки
let watchHistory = [];     // история просмотра текущего пользователя

// Шапка: либо «Войти» (ссылка на login.html), либо имя + админка + выход.
// initial=true — первый вызов до ответа сервера (используем кэш, чтобы не мигать).
function renderAuth(initial) {
  const userArea = document.getElementById('user-area');
  const authOpen = document.getElementById('auth-open');
  const adminLink = document.getElementById('admin-link');
  const userNameEl = document.getElementById('user-name');
  const shown = currentUser || (initial ? cachedUser() : null);
  if (authDisabled) {
    if (userArea) userArea.hidden = true;
    if (authOpen) authOpen.hidden = true;
    if (adminLink) adminLink.hidden = true;
    return;
  }
  const logged = !!shown;
  if (userArea) userArea.hidden = !logged;
  if (authOpen) {
    authOpen.hidden = logged;
    // Возврат после входа — на текущую страницу (только свой путь, без внешних адресов).
    // На самой странице входа next не подставляем — иначе ссылка ссылается сама на себя.
    const here = location.pathname + location.search;
    authOpen.href = here.indexOf('/login.html') === 0 ? '/login.html' : '/login.html?next=' + encodeURIComponent(here);
  }
  if (logged && userNameEl) userNameEl.textContent = shown.username;
  if (adminLink) adminLink.hidden = !(logged && shown.role === 'admin');
}

// Имя пользователя из прошлого визита — шапка рисуется сразу, не дожидаясь ответа /api/auth/me.
function cachedUser() {
  try {
    const raw = localStorage.getItem('vv_user');
    return raw ? JSON.parse(raw) : null;
  } catch (e) {
    return null;
  }
}

function cacheUser(u) {
  try {
    if (u) localStorage.setItem('vv_user', JSON.stringify({ username: u.username, role: u.role || '' }));
    else localStorage.removeItem('vv_user');
  } catch (e) { /* приватный режим — просто без кэша */ }
}

// Шапка рисуется сразу (до сети): у вошедшего пользователя имя видно из кэша,
// у гостя — кнопка «Войти». Иначе состояние кнопки висело до ответа /api/auth/me.
renderAuth(true);

async function initAuth() {
  // Кэш может быть устаревшим (вышел на другом устройстве) — перепроверяем в фоне.
  const res = await apiGet('/api/auth/me', 6000);
  if (res && res.status === 503) {
    // Сервис без БД — авторизация и история отключены.
    authDisabled = true;
    cacheUser(null);
    renderAuth(true);
    authHooks.forEach((fn) => fn());
    return;
  }
  if (res && res.ok) {
    currentUser = res.data.user || null;
  } else if (res) {
    currentUser = null;
  }
  // res === null: сеть/таймаут — остаёмся на кэше, чтобы шапка не мигала «Войти».
  cacheUser(currentUser);
  renderAuth(true);
  await loadHistory();
}

// apiGet — fetch с таймаутом и разбором JSON; null при ошибке/таймауте.
async function apiGet(url, timeoutMs) {
  const ctrl = new AbortController();
  const timer = timeoutMs ? setTimeout(() => ctrl.abort(), timeoutMs) : null;
  try {
    const res = await fetch(url, { signal: ctrl.signal });
    let data = null;
    if (res.ok) {
      try { data = await res.json(); } catch (e) { data = null; }
    }
    return { ok: res.ok, status: res.status, data };
  } catch (e) {
    return null;
  } finally {
    if (timer) clearTimeout(timer);
  }
}

async function loadHistory() {
  if (!currentUser) {
    watchHistory = [];
    episodeHistory.clear();
    authHooks.forEach((fn) => fn());
    return;
  }
  const res = await apiGet('/api/history', 10000);
  watchHistory = (res && res.ok && res.data && res.data.items) || [];
  authHooks.forEach((fn) => fn());
}

const episodeHistory = new Map();

async function loadEpisodeHistory(id) {
  episodeHistory.delete(id);
  if (!currentUser || !id) return;
  const res = await apiGet('/api/history?film_id=' + encodeURIComponent(id), 10000);
  if (res && res.ok && res.data) episodeHistory.set(id, res.data.items || []);
}

function episodeHistoryEntry(id, season, episode, magnet, file) {
  const entries = episodeHistory.get(id) || [];
  // Season/episode is stable across different releases and voice tracks.
  return entries.find((e) => season > 0 && episode > 0
    ? e.season === season && e.episode === episode
    : e.magnet === magnet && e.file === file) || null;
}

function rememberWatchProgress(progress) {
  const entries = episodeHistory.get(progress.film_id) || [];
  episodeHistory.set(progress.film_id, [progress, ...entries.filter((e) =>
    e.magnet !== progress.magnet || e.file !== progress.file)]);
  const previous = historyEntry(progress.film_id) || {};
  watchHistory = [Object.assign({}, previous, progress), ...watchHistory.filter((x) => x.film_id !== progress.film_id)];
  authHooks.forEach((fn) => fn());
}

function historyEntry(id) {
  if (!id) return null;
  return watchHistory.find((x) => x.film_id === id) || null;
}

// В истории хранится только последняя серия — удаление убирает фильм целиком.
async function removeHistoryEntry(filmId) {
  episodeHistory.delete(filmId);
  try {
    await fetch('/api/history/' + encodeURIComponent(filmId), { method: 'DELETE' });
  } catch (err) { /* игнорируем: локальный список всё равно обновим */ }
  watchHistory = watchHistory.filter((x) => x.film_id !== filmId);
  authHooks.forEach((fn) => fn());
}

const logoutBtn = document.getElementById('auth-logout');
if (logoutBtn) {
  logoutBtn.addEventListener('click', async () => {
    try {
      await fetch('/api/auth/logout', { method: 'POST' });
    } catch (e) { /* всё равно выходим локально */ }
    currentUser = null;
    watchHistory = [];
    cacheUser(null);
    renderAuth();
    authHooks.forEach((fn) => fn());
  });
}

// ---- Переходы между страницами ----
// Ключ фильма — то, что принимает /api/films/{id}: сначала IMDb, затем id каталога.
function filmIdOf(it) {
  return it ? (it.imdb_id || it.id || '') : '';
}

function filmUrl(it) {
  return '/film.html?id=' + encodeURIComponent(filmIdOf(it));
}

// Карточка целиком уезжает в sessionStorage: страница фильма берёт из неё то, что
// не отдаёт /api/films (kind/tmdb_id/prose для записей из каталога магнетов).
function storeItem(it) {
  if (!it) return;
  const id = filmIdOf(it);
  if (!id) return;
  try {
    sessionStorage.setItem('vv:item:' + id, JSON.stringify(it));
  } catch (e) { /* приватный режим/квота — страница догрузит данные сама */ }
}

function loadStoredItem(id) {
  if (!id) return null;
  try {
    const raw = sessionStorage.getItem('vv:item:' + id);
    return raw ? JSON.parse(raw) : null;
  } catch (e) {
    return null;
  }
}

// Просмотр запускается на отдельной странице /watch.html: собираем ссылку с
// параметрами раздачи (сезон/серия/позиция/дорожка).
function playbackPageUrl(path, params) {
  const id = params.get('id');
  if (!id || params.has('room')) return path + '?' + params.toString();
  try { sessionStorage.setItem('vv:navigate:' + path + ':' + id, params.toString()); }
  catch (e) { return path + '?' + params.toString(); }
  return path + '?id=' + encodeURIComponent(id);
}

function savePlaybackPage(params) {
  const state = Object.assign({}, history.state, { playback: params.toString() });
  const visible = new URLSearchParams(params);
  if (!params.has('room')) {
    for (const key of ['magnet', 'rt', 'file', 'season', 'ep', 'pos', 'track', 'subs', 'voice', 'autoplay', 'play']) visible.delete(key);
  }
  history.replaceState(state, '', location.pathname + '?' + visible.toString());
}

function playbackPageParams() {
  const params = new URLSearchParams(location.search);
  const id = params.get('id');
  if (!id || params.has('room')) return params;
  const key = 'vv:navigate:' + location.pathname + ':' + id;
  let pending = '';
  try { pending = sessionStorage.getItem(key) || ''; sessionStorage.removeItem(key); } catch (e) {}
  const saved = new URLSearchParams(pending || (history.state && history.state.playback) || '');
  // Presentation flags (for example ?tv=1) do not replace the stored source.
  // An explicit playback link does, including links to another episode of the
  // same series; inheriting its old magnet/file would open the wrong episode.
  const explicit = ['magnet', 'rt', 'file', 'season', 'ep', 'pos', 'track', 'subs', 'voice', 'autoplay', 'play'].some(key => params.has(key));
  const result = !explicit && saved.get('id') === id ? saved : params;
  if (result === saved) params.forEach((value, key) => result.set(key, value));
  try { savePlaybackPage(result); } catch (e) {}
  return result;
}

function watchUrl(id, opts) {
  const o = opts || {};
  const p = new URLSearchParams();
  p.set('id', id);
  if (o.magnet) p.set('magnet', o.magnet);
  if (o.release) p.set('rt', o.release);
  if (typeof o.file === 'number' && o.file >= 0) p.set('file', String(o.file));
  if (o.season) p.set('season', String(o.season));
  if (o.ep) p.set('ep', String(o.ep));
  if (o.pos) p.set('pos', String(Math.round(o.pos)));
  if (o.track) p.set('track', String(o.track));
  if (o.subs >= 0) p.set('subs', String(o.subs));
  if (o.voice) p.set('voice', o.voice);
  if (o.autoplay) p.set('autoplay', '1');
  return playbackPageUrl('/watch.html', p);
}

function go(url) {
  const target = new URL(url, location.href);
  if (target.origin === location.origin && ['/film.html', '/watch.html'].includes(target.pathname)
      && target.searchParams.size > 1 && !target.searchParams.has('room')) {
    url = playbackPageUrl(target.pathname, target.searchParams);
  }
  location.href = url;
}

// ---- ТВ-режим: запоминаем его в localStorage ----
// Навигация по страницам теряет ?tv=1, а обёртка Android подставляет только его.
if (new URLSearchParams(location.search).get('tv') === '1') {
  try { localStorage.setItem('vv_tv', '1'); } catch (e) { /* приватный режим */ }
}

// Кнопка Back у ТВ-пульта: страницы, кроме каталога, уходят на шаг назад.
function tvBack() {
  if (window.history.length > 1) {
    window.history.back();
    return;
  }
  const back = document.getElementById('back-link');
  location.href = (back && back.getAttribute('href')) || '/';
}

window.VV = {
  t,
  applyLang,
  onLang,
  onAuth,
  dbg,
  showDebug,
  filmIdOf,
  filmUrl,
  watchUrl,
  storeItem,
  loadStoredItem,
  historyEntry,
  removeHistoryEntry,
  go,
  tvBack,
  get lang() { return lang; },
  get user() { return currentUser; },
  get history() { return watchHistory; },
};
