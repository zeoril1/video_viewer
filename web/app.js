'use strict';

const grid = document.getElementById('grid');
const empty = document.getElementById('empty');
const search = document.getElementById('search');
const modal = document.getElementById('modal');
const modalTitle = document.getElementById('modal-title');
const modalClose = document.getElementById('modal-close');
const player = document.getElementById('player');
const details = document.getElementById('details');
const detailsPoster = document.getElementById('details-poster');
const detailsTitle = document.getElementById('details-title');
const detailsRating = document.getElementById('details-rating');
const detailsSubtitle = document.getElementById('details-subtitle');
const detailsMeta = document.getElementById('details-meta');
const detailsDirector = document.getElementById('details-director');
const detailsActors = document.getElementById('details-actors');
const detailsPlot = document.getElementById('details-plot');
const detailsNote = document.getElementById('details-note');
const sourcesEl = document.getElementById('sources');
const sourcesTitle = document.getElementById('sources-title');
const sourcesEmpty = document.getElementById('sources-empty');
const tracksEl = document.getElementById('tracks');
const tracksTitle = document.getElementById('tracks-title');
const tracksList = document.getElementById('tracks-list');
const subsEl = document.getElementById('subs');
const subsTitle = document.getElementById('subs-title');
const subsList = document.getElementById('subs-list');
const episodesEl = document.getElementById('episodes');
const episodesTitle = document.getElementById('episodes-title');
const epSeasons = document.getElementById('ep-seasons');
const epList = document.getElementById('ep-list');
const sourcesSeasonWrap = document.getElementById('sources-season-wrap');
const sourcesSeason = document.getElementById('sources-season');
const sourcesEpisodesWrap = document.getElementById('sources-episodes-wrap');
const sourcesEpisodes = document.getElementById('sources-episodes');
const sourcesAudioWrap = document.getElementById('sources-audio-wrap');
const sourcesAudio = document.getElementById('sources-audio');
// Переключатель раздач (торрентов) сезона сериала.
const sourcesRelWrap = document.getElementById('sources-rel-wrap');
const sourcesRel = document.getElementById('sources-rel');
// Диалог «озвучка не сделана на все серии выбранного сезона».
const tlDialog = document.getElementById('tl-dialog');
const tlTitle = document.getElementById('tl-title');
const tlMessage = document.getElementById('tl-message');
const tlEpisodes = document.getElementById('tl-episodes');
const tlClose = document.getElementById('tl-close');
const tlCloseViewing = document.getElementById('tl-close-viewing');
const playerError = document.getElementById('player-error');
const playerWrap = document.getElementById('player-wrap');
const ctrlPlay = document.getElementById('ctrl-play');
const ctrlBar = document.getElementById('ctrl-bar');
const ctrlBuffered = document.getElementById('ctrl-buffered');
const ctrlPosition = document.getElementById('ctrl-position');
const ctrlThumb = document.getElementById('ctrl-thumb');
const ctrlTime = document.getElementById('ctrl-time');
const ctrlMute = document.getElementById('ctrl-mute');
const ctrlVolume = document.getElementById('ctrl-volume');
const ctrlFullscreen = document.getElementById('ctrl-fullscreen');
const ctrlPrev = document.getElementById('ctrl-prev');
const ctrlNext = document.getElementById('ctrl-next');
const ctrlSeekB = document.getElementById('ctrl-seek-b');
const ctrlSeekF = document.getElementById('ctrl-seek-f');
const ctrlMini = document.getElementById('ctrl-mini');
const ctrlMiniFill = document.getElementById('ctrl-mini-fill');
const ctrlDebug = document.getElementById('ctrl-debug');
const playerDebug = document.getElementById('player-debug');
// Авторизация и история просмотра.
const authOpen = document.getElementById('auth-open');
const userArea = document.getElementById('user-area');
const userNameEl = document.getElementById('user-name');
const authLogout = document.getElementById('auth-logout');
const adminLink = document.getElementById('admin-link');
const authModal = document.getElementById('auth-modal');
const authClose = document.getElementById('auth-close');
const authTitle = document.getElementById('auth-title');
const authTabLogin = document.getElementById('auth-tab-login');
const authTabRegister = document.getElementById('auth-tab-register');
const authForm = document.getElementById('auth-form');
const authUsername = document.getElementById('auth-username');
const authPassword = document.getElementById('auth-password');
const authError = document.getElementById('auth-error');
const authSubmit = document.getElementById('auth-submit');
const continueSec = document.getElementById('continue');
const continueTitle = document.getElementById('continue-title');
const continueList = document.getElementById('continue-list');
const resumeBtn = document.getElementById('resume-btn');
const watchBtn = document.getElementById('watch-btn');
let hlsPlayer = null;
let currentPlay = null; // { id, magnet } активного источника
let totalDuration = 0;  // полная длительность фильма (из /tracks)
// Состояние запроса длительности (key = id|magnet|file): чтобы не дублировать
// /tracks при каждом старте потока (playHls → fetchDuration + loadTracks).
let durationFetch = { key: '', inflight: false };

// Позиция/качество активного HLS-потока. Перемотка в непереданную часть
// перезапускает ffmpeg с позиции streamStart, поэтому «нулевая» точка
// потока смещена от начала фильма.
let streamStart = 0;        // смещение активного потока от начала фильма (с)
let currentTrack = 0;       // активная звуковая дорожка
let currentSubs = -1;       // активная субтитр-дорожка в потоке (-1 — без субтитров)
let currentSubtitles = [];  // список субтитров активного файла (из /tracks)
let currentQuality = 'source'; // активное качество: source|2160|1080|720|480
let currentVideoCodec = ''; // кодек видео активного источника (из /tracks)
let currentVideoHeight = 0; // высота видео активного источника (из /tracks; 0 — неизвестно)
// Автофолбэк H.265→H.264: применяется один раз на источник, чтобы
// mediaError/bufferAppendError не зацикливались на перезапусках.
let h264FallbackDone = false;
// Показывать ли уведомление о перекодировании при старте потока.
let pendingCodecNote = false;

// Автоперезапуск потока после остановки ffmpeg-сессии по простою (пауза
// >90с — cleanup убивает сессию и удаляет временные файлы, и hls.js при
// возобновлении получает 404 на сегмент). Лимит попыток на один успешный
// старт — чтобы реальные сетевые ошибки не зацикливались на перезапусках.
const maxStreamRestarts = 3;
let streamRestarts = 0;

// Выбор «переводов» (озвучек) для просмотра. Для сериала пользователь
// выбирает только сезон и серию, а в зависимости от них предлагаются
// доступные переводы (списка торрентов и выбора качества больше нет).
// null означает «ещё не выбрано» ('' — валидная группа «без озвучки»).
let activeAudio = null;     // выбранный перевод (озвучка)
let selectedSeason = null;  // выбранный сезон в блоке источников (сериал)
let selectedEpisode = null; // выбранная серия в блоке источников (сериал)
let lastSourceId = null;    // id фильма последнего списка источников
// Кэш структуры серий сериала (сезон -> номера серий). Строится лениво из
// реальных файлов репрезентативных раздач и переживает закрытие карточки
// в рамках сессии: id -> { bySeason: {N: [серии]}, done: {N: true}, probing }.
const seriesEpisodes = {};
// Ручной выбор раздачи (торрента) по сезону: season -> magnet. Если
// пользователь сам выбрал раздачу в списке — клики по сериям сезона и
// автозапуск продолжают играть именно её (пока она покрывает сезон).
const relPref = {};
// Ручной выбор озвучки по сезону (label чипа) — по ней автоматически
// подбирается торрент сезона и включается нужная дорожка файла.
const voicePref = {};
// Озвучка, которую надо автоматически включить после анализа дорожек файла
// (loadTracks выбирает её ordinal и перезапускает поток).
let autoVoice = '';
// Последний полученный список аудиодорожек файла (для быстрого переключения
// озвучки внутри уже играющей раздачи, без повторного открытия торрента).
let lastTracksItems = [];
// Точка возобновления просмотра: применяется, когда придут источники
// (выделяем сохранённые сезон/серию и перевод в новом селекторе).
let pendingResume = null; // {season, episode, magnet}

// Серии сериала внутри выбранного источника.
let currentFile = -1;    // индекс файла (серии) в торренте; -1 — авто
let lastFiles = [];      // последний список серий (реальные файлы раздачи)
// Автопереход на следующую серию: защита от повторного срабатывания
// (HLS-поток прогрессивный, video.duration=Infinity, поэтому конец
// отслеживаем по absTime() >= totalDuration, а не по событию ended).
let autoNextFired = false;

// ---- Авторизация и история просмотра ----
let currentUser = null;    // {id, username} текущего пользователя
let authDisabled = false;  // auth отключён (сервис без БД) — скрываем кнопки
let watchHistory = [];     // история просмотра текущего пользователя
let lastProgressSend = 0;  // время последней отправки прогресса (мс)
let authMode = 'login';    // 'login' | 'register'

// ---- Диагностика плеера ----
// Логи воспроизведения: в консоль и (при включённом `?debug` или ошибке)
// в панель #player-debug. Помогает понять, почему фильм не играет.
const DEBUG = new URLSearchParams(location.search).has('debug');
let debugLines = [];

function renderDebug() {
  playerDebug.textContent = debugLines.join('\n');
}

function dbg(msg) {
  const line = '[' + new Date().toLocaleTimeString() + '] ' + msg;
  debugLines.push(line);
  if (debugLines.length > 40) debugLines.shift();
  console.log(line);
  if (!playerDebug.hidden) renderDebug();
}

function showDebug() {
  playerDebug.hidden = false;
  renderDebug();
}
const langBtns = document.querySelectorAll('.lang-btn');
const sectionsEl = document.getElementById('sections');
const genreEl = document.getElementById('genre');
const sortEl = document.getElementById('sort');
const sentinel = document.getElementById('sentinel');
const releasedEl = document.getElementById('released');

// Сколько фильмов на странице.
const PER_PAGE = 30;

// Варианты сортировки каталога. Не применяются к разделу «Популярное»
// (там свой порядок — чарт популярности).
const SORTS = [
  { key: 'year', ru: 'По дате выпуска', en: 'Release date' },
  { key: 'rating', ru: 'По рейтингу', en: 'Rating' },
  { key: 'title', ru: 'По названию', en: 'Title' },
];

// Разделы каталога (по типу контента). Общего «Популярное» НЕТ —
// популярное/лучшее доступно подменю категорий (см. COLLECTIONS).
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

// Подборки категорий («Фильмы»/«Сериалы»), показываются выпадающим меню
// при наведении на кнопку категории: «Лучшие …» — чарт top_rated
// (IMDb+TMDB), «Популярные сериалы» — чарт популярных сериалов. Пустой
// ключ — обычный список категории (в меню не выводится, это сама кнопка).
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

// Главная страница показывает весь каталог («Все»).
let currentSection = 'all';
let currentGenre = '';
let currentCollection = ''; // подборка внутри раздела (best/popular/'');
let metaKinds = {};
let metaGenres = [];
let lastSourceItems = []; // последние найденные варианты (для перерисовки при смене языка)

// ---- Локализация интерфейса ----
const I18N = {
  ru: {
    searchPlaceholder: 'Поиск по каталогу...',
    empty: 'Ничего не найдено.',
    searching: 'Идёт поиск...',
    notFoundImdb: 'Ничего не найдено в каталоге, IMDb и TMDB.',
    searchFailed: 'Не удалось выполнить поиск на IMDb.',
    noPlot: 'Описание пока недоступно.',
    noMagnet: 'К этому фильму пока не привязана магнет-ссылка — стриминг недоступен. Данные получены из IMDb.',
    close: 'Закрыть',
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
    endOfEpisodes: 'Это последняя серия.',
    endOfSeason: 'Конец сезона — следующая серия в другой раздаче.',
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
    episodesNotFound: 'Серии этой раздачи не найдены.',
    translationPartial: 'Озвучка «{audio}» не сделана на все серии — серии S{season}E{episode} в этой раздаче нет. Выберите доступную серию или закройте просмотр.',
    noTranslations: 'Переводы (озвучки) для этого фильма не найдены.',
    detectingAudio: 'Определяю перевод по звуковым дорожкам…',
    endOfSeries: 'Это последняя серия сериала. Просмотр завершён.',
    viewingClosed: 'Просмотр закрыт.',
    closeViewing: 'Закрыть просмотр',
    episodesUnavailable: 'Не удалось получить серии этой раздачи (мало пиров). Попробуйте другой перевод.',
  },
  en: {
    searchPlaceholder: 'Search the catalog...',
    empty: 'Nothing found.',
    searching: 'Searching...',
    notFoundImdb: 'Nothing found in the catalog, IMDb or TMDB.',
    searchFailed: 'IMDb search failed.',
    noPlot: 'Description is not available yet.',
    noMagnet: 'No magnet link is attached to this film yet — streaming is unavailable. Data from IMDb.',
    close: 'Close',
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
    endOfEpisodes: 'This is the last episode.',
    endOfSeason: 'End of season — the next episode is in another release.',
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
    episodesNotFound: 'No episodes found in this release.',
    translationPartial: 'The "{audio}" dub is not available for all episodes — episode S{season}E{episode} is missing from this release. Pick an available episode or close the viewing.',
    noTranslations: 'No translations (dubs) found for this film.',
    detectingAudio: 'Detecting dub from audio tracks…',
    endOfSeries: 'This is the last episode of the series. Playback finished.',
    viewingClosed: 'Viewing closed.',
    closeViewing: 'Close viewing',
    episodesUnavailable: 'Could not load episodes of this release (few peers). Try another translation.',
  },
};

let lang = localStorage.getItem('lang') || 'ru';
if (!I18N[lang]) lang = 'ru';

function t(key) {
  return (I18N[lang] && I18N[lang][key]) || I18N.ru[key] || key;
}

// Переводы жанров для отображения в русском интерфейсе.
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

// Название на другом языке (не выбранном): для EN-интерфейса — русское,
// для RU — английское.
function dispTitleAlt(it) {
  if (lang === 'ru' && it.title) return it.title;
  return it.title_ru || '';
}

// fmtRating — рейтинг в формате «IMDB/TMDB»: 6.7/8.2 (рейтинг Кинопоиска убран).
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

function applyLang() {
  search.placeholder = t('searchPlaceholder');
  modalClose.setAttribute('aria-label', t('close'));
  detailsNote.textContent = t('noMagnet');
  langBtns.forEach((b) => b.classList.toggle('active', b.dataset.lang === lang));
  renderSections(metaKinds);
  populateGenres(metaGenres);
  populateSort();
  render();
  // Перевод элементов с атрибутом data-i18n (поле формы авторизации и т.п.).
  document.querySelectorAll('[data-i18n]').forEach((el) => {
    el.textContent = t(el.dataset.i18n);
  });
  ctrlPrev.title = t('prevEpisode');
  ctrlNext.title = t('nextEpisode');
  updateAuthTabs();
  renderContinue();
  // Обновляем открытую модалку на выбранный язык.
  if (!modal.hidden && currentItem) {
    modalTitle.textContent = dispTitle(currentItem);
    updateResumeBtn(currentItem);
    showDetails(currentItem);
    if (!sourcesEl.hidden) {
      sourcesTitle.textContent = t('sourcesTitle');
      updateSourceLabels();
      if (lastSourceItems.length) {
        renderSources(lastSourceItems, currentItem.imdb_id || currentItem.id);
      } else {
        sourcesEmpty.textContent = t('noSources');
      }
    }
    if (!tracksEl.hidden) tracksTitle.textContent = t('audioTracks');
  }
}

langBtns.forEach((b) => {
  b.addEventListener('click', () => {
    lang = b.dataset.lang;
    localStorage.setItem('lang', lang);
    applyLang();
  });
});

// ---- Авторизация и история просмотра ----

// renderAuth переключает кнопки входа/имени пользователя в шапке.
function renderAuth() {
  if (authDisabled) {
    userArea.hidden = true;
    authOpen.hidden = true;
    adminLink.hidden = true;
    return;
  }
  const logged = !!currentUser;
  userArea.hidden = !logged;
  authOpen.hidden = logged;
  if (logged) userNameEl.textContent = currentUser.username;
  // Ссылка на админ-страницу — только для пользователей с ролью admin.
  adminLink.hidden = !(logged && currentUser.role === 'admin');
}

// initAuth при загрузке определяет текущего пользователя по куке сессии.
async function initAuth() {
  try {
    const res = await fetch('/api/auth/me');
    if (res.status === 503) {
      // Сервис без БД — авторизация и история отключены.
      authDisabled = true;
      renderAuth();
      return;
    }
    if (res.ok) {
      const data = await res.json();
      currentUser = data.user || null;
    }
  } catch (e) {
    // Офлайн/недоступен — работаем без аккаунта.
  }
  renderAuth();
  await loadHistory();
}

function openAuth(mode) {
  if (authDisabled) return;
  authMode = mode || 'login';
  authError.hidden = true;
  authUsername.value = '';
  authPassword.value = '';
  updateAuthTabs();
  authModal.hidden = false;
  setTimeout(() => authUsername.focus(), 0);
}

function closeAuth() {
  authModal.hidden = true;
}

function updateAuthTabs() {
  authTabLogin.classList.toggle('active', authMode === 'login');
  authTabRegister.classList.toggle('active', authMode === 'register');
  authTitle.textContent = authMode === 'login' ? t('login') : t('register');
  authSubmit.textContent = authMode === 'login' ? t('login') : t('register');
  authPassword.autocomplete = authMode === 'login' ? 'current-password' : 'new-password';
}

authOpen.addEventListener('click', () => openAuth('login'));
authClose.addEventListener('click', closeAuth);
authModal.addEventListener('click', (e) => {
  if (e.target === authModal) closeAuth();
});
authTabLogin.addEventListener('click', () => { authMode = 'login'; updateAuthTabs(); });
authTabRegister.addEventListener('click', () => { authMode = 'register'; updateAuthTabs(); });

authForm.addEventListener('submit', async (e) => {
  e.preventDefault();
  authError.hidden = true;
  const username = authUsername.value.trim();
  const password = authPassword.value;
  if (!/^[a-zA-Z0-9_.-]{3,32}$/.test(username) || password.length < 6) {
    authError.textContent = t('authErrorShort');
    authError.hidden = false;
    return;
  }
  authSubmit.disabled = true;
  try {
    const res = await fetch('/api/auth/' + (authMode === 'login' ? 'login' : 'register'), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }),
    });
    if (res.status === 503) {
      authError.textContent = t('authErrorAuth');
      authError.hidden = false;
      return;
    }
    if (res.status === 409) {
      authError.textContent = t('authErrorTaken');
      authError.hidden = false;
      return;
    }
    if (!res.ok) {
      authError.textContent = res.status === 401 ? t('authErrorInvalid') : t('authErrorServer');
      authError.hidden = false;
      return;
    }
    const data = await res.json();
    currentUser = data.user || null;
    renderAuth();
    closeAuth();
    loadHistory();
  } catch (err) {
    authError.textContent = t('authErrorServer');
    authError.hidden = false;
  } finally {
    authSubmit.disabled = false;
  }
});

authLogout.addEventListener('click', async () => {
  try {
    await fetch('/api/auth/logout', { method: 'POST' });
  } catch (e) {}
  currentUser = null;
  watchHistory = [];
  renderAuth();
  renderContinue();
});

// loadHistory загружает историю просмотра (блок «Продолжить просмотр»).
async function loadHistory() {
  if (!currentUser) {
    watchHistory = [];
    renderContinue();
    return;
  }
  try {
    const res = await fetch('/api/history');
    if (!res.ok) throw new Error('HTTP ' + res.status);
    const data = await res.json();
    watchHistory = data.items || [];
  } catch (e) {
    watchHistory = [];
  }
  renderContinue();
}

// renderContinue отображает карточки «Продолжить просмотр» для текущего
// пользователя (постер, сезон/серия, полоса прогресса, позиция).
function renderContinue() {
  if (!currentUser || !watchHistory.length) {
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
        removeHistoryEntry(e);
        return;
      }
      resumeItem(e);
    });
    continueList.appendChild(card);
  });
}

// removeHistoryEntry удаляет запись истории с сервера и из списка.
// В истории показывается только последняя просмотренная серия фильма,
// поэтому удаление карточки убирает фильм из истории целиком (иначе на
// его месте появилась бы предпоследняя серия).
async function removeHistoryEntry(e) {
  try {
    await fetch('/api/history/' + encodeURIComponent(e.film_id), { method: 'DELETE' });
  } catch (err) {}
  watchHistory = watchHistory.filter((x) => x.film_id !== e.film_id);
  renderContinue();
  if (currentItem && (currentItem.imdb_id || currentItem.id) === e.film_id) updateResumeBtn(currentItem);
}

// resumeItem открывает фильм и продолжает просмотр с сохранённой позиции
// (сохранённые магнит, серия и позиция берутся из записи истории).
function resumeItem(e) {
  if (!e || !e.magnet) return;
  currentItem = {
    imdb_id: e.film_id,
    id: e.film_id,
    title: e.title,
    title_ru: e.title_ru,
    kind: e.kind,
    year: e.year,
    poster_url: e.poster_url,
  };
  modalTitle.textContent = dispTitle(currentItem);
  playerWrap.hidden = false;
  player.pause();
  player.removeAttribute('src');
  player.load();
  details.hidden = false;
  detailsNote.hidden = true;
  showDetails(currentItem);
  modal.hidden = false;
  updateResumeBtn(currentItem);
  watchBtn.hidden = true; // резюм уже сам включает воспроизведение

  currentPlay = { id: e.film_id, magnet: e.magnet };
  streamStart = 0;
  currentTrack = 0;
  currentSubs = -1;
  currentSubtitles = [];
  currentQuality = 'source';
  currentFile = (typeof e.file === 'number' && e.file >= 0) ? e.file : -1;
  currentVideoCodec = '';
  currentVideoHeight = 0;
  h264FallbackDone = false;
  pendingCodecNote = false;
  streamRestarts = 0; // новый запуск (резюм) — свежие попытки перезапуска
  updateQualityButtons();

  // Догружаем полные детали фильма (описание, постер, режиссёр и т.п.).
  if (e.film_id.indexOf('tt') === 0) {
    fetch('/api/films/' + encodeURIComponent(e.film_id))
      .then((res) => (res.ok ? res.json() : Promise.reject(new Error('HTTP ' + res.status))))
      .then((f) => {
        if (f && (f.plot || f.plot_ru || f.genres || f.poster_url || f.director || f.actors || f.movie_length || (f.countries && f.countries.length))) {
          currentItem = Object.assign(currentItem, f);
          showDetails(currentItem);
        }
      })
      .catch(() => {});
  }

  // Запоминаем точку возобновления: выделим сохранённые сезон/серию и
  // перевод в новом селекторе, когда придут источники.
  pendingResume = { season: e.season || 0, episode: e.episode || 0, magnet: e.magnet };
  // Грузим серии текущей раздачи (для навигации prev/next в плеере), не
  // показывая старый селектор — выбор серий теперь в блоке источников.
  if (isSeriesKind(e.kind) && e.file >= 0) {
    loadEpisodes(e.film_id, e.magnet);
  } else {
    hideEpisodes();
  }
  loadTracks(e.film_id, e.magnet, currentFile);
  playHls(e.film_id, e.magnet, currentFile, 0, e.position, 'source');
  // Список источников — в фоне, БЕЗ автозапуска (иначе перезапишет resume).
  loadSources(currentItem, true);
  if (typeof playerWrap.scrollIntoView === 'function') {
    playerWrap.scrollIntoView({ block: 'nearest' });
  }
}

// updateResumeBtn показывает/прячет кнопку «Продолжить» в шапке модалки,
// если у фильма есть сохранённая позиция просмотра.
function updateResumeBtn(it) {
  const id = it && (it.imdb_id || it.id);
  const entry = id ? watchHistory.find((x) => x.film_id === id) : null;
  if (!currentUser || !entry || !entry.magnet || !id) {
    resumeBtn.hidden = true;
    return;
  }
  // Позиция в «резюмируемом» диапазоне: не в самом начале и не у конца.
  if (entry.position < 30 || (entry.duration > 0 && entry.duration - entry.position < 30)) {
    resumeBtn.hidden = true;
    return;
  }
  resumeBtn.hidden = false;
  resumeBtn.textContent = '▶ ' + t('resumeFrom') + ' ' + fmtTime(entry.position);
  resumeBtn.onclick = () => resumeItem(entry);
}

// maybeSaveProgress отправляет позицию просмотра в историю (не чаще раза
// в 5 секунд; final=true — принудительно при паузе/закрытии плеера).
// Возвращает промис отправки (или null, если отправлять нечего).
function maybeSaveProgress(final) {
  if (!currentUser || !currentPlay) return null;
  const now = Date.now();
  if (!final && now - lastProgressSend < 5000) return null;
  const pos = Math.round(absTime());
  if (pos < 5) return null; // не сохраняем случайные клики в самом начале
  lastProgressSend = now;
  const ep = currentFile >= 0 ? (lastFiles.find((f) => f.index === currentFile) || {}) : {};
  const body = {
    film_id: currentPlay.id,
    magnet: currentPlay.magnet || '',
    file: currentFile >= 0 ? currentFile : -1,
    season: ep.season || 0,
    episode: ep.episode || 0,
    position: pos,
    duration: Math.round(totalDuration || 0),
  };
  return fetch('/api/history/progress', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  }).catch(() => {});
}

// ---- Каталог ----

let items = [];
let currentItem = null; // фильм, открытый в модалке
let currentPage = 1;
let totalPages = 1;
let currentSort = 'year'; // сортировка: year (по умолчанию) | rating | title
let onlyReleased = true; // «только вышедшие» (по умолчанию включено)
let loadingMore = false;  // идёт ли догрузка следующей страницы
let allLoaded = false;    // все страницы загружены (больше догружать нечего)
let catalogGen = 0;       // поколение каталога: устаревшие ответы игнорируются

// Загружает страницу каталога (с учётом поиска, раздела, жанра и
// сортировки). При append=true карточки добавляются в конец сетки
// (догрузка следующей страницы при прокрутке), иначе список заменяется.
async function fetchPage(page, append) {
  if (!append) {
    // Свежая загрузка (смена раздела/жанра/сортировки/поиска) — сбрасываем
    // состояние бесконечной прокрутки и открываем новое поколение запросов.
    catalogGen++;
    loadingMore = false;
    allLoaded = false;
  }
  const gen = catalogGen;

  const q = search.value.trim();
  const params = new URLSearchParams({ page: String(page), per_page: String(PER_PAGE) });
  if (q) params.set('q', q);
  // Секцию шлём всегда (и при поиске) — вкладки фильтруют результаты
  // поиска по разделу. Вкладка «Все» секцию не передаёт (глобально).
  if (currentSection !== 'all') params.set('section', currentSection);
  if (currentGenre) params.set('genre', currentGenre);
  // Сортировку шлём для всех разделов, кроме «Популярное» (порядок чарта).
  if (currentSection !== 'popular') params.set('sort', currentSort);
  // Подборка внутри раздела («Лучшие фильмы/сериалы», «Популярные сериалы»).
  if (currentCollection) params.set('collection', currentCollection);
  // «Только вышедшие» — фильтруем на сервере по дате выпуска.
  params.set('released', onlyReleased ? '1' : '0');

  try {
    const res = await fetch('/api/catalog?' + params.toString());
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data = await res.json();
    // Пока запрос летел, каталог перезагрузили (смена раздела/поиска) —
    // ответ устарел, игнорируем, чтобы не перемешать старый список с новым.
    if (gen !== catalogGen) return;
    const pageItems = data.items || [];
    currentPage = data.page || 1;
    totalPages = data.total_pages || 1;

    // «Популярное» может быть пустым без TMDB-чарта (и при недоступном
    // IMDb moviemeter) — фолбэк на «Все», чтобы главная не была пустой.
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
      empty.textContent = t('empty') + ' (' + err.message + ')';
      empty.hidden = false;
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
    empty.hidden = false;
    // Пока локальных совпадений нет — идёт поиск на IMDb.
    empty.textContent = search.value.trim() ? t('searching') : t('empty');
  }
  updateSentinel();
}

// makeCard создаёт карточку фильма (общий код для полного списка и догрузки).
function makeCard(it) {
  const card = document.createElement('article');
  card.className = 'card';

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

  card.addEventListener('click', () => openItem(it));

  // Лениво догружаем обогащение карточки (длительность/страна/режиссёр/
  // актёры), если данных ещё нет — карточка сразу становится подробной.
  enrichCardWhenVisible(it, card);
  return card;
}

function renderList(list) {
  grid.innerHTML = '';
  for (const it of list) grid.appendChild(makeCard(it));
  empty.hidden = list.length > 0;
}

// appendCards добавляет карточки следующей страницы в конец сетки.
function appendCards(list) {
  for (const it of list) grid.appendChild(makeCard(it));
  empty.hidden = items.length > 0;
}

// updateSentinel показывает/прячет якорь бесконечной прокрутки: он виден,
// только когда есть контент и есть что догружать.
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

// cardMetaHtml — тело маленькой карточки: 5 строк (1) название на
// выбранном языке + рейтинг IMDB/TMDB (6.7/8.2), (2) название на
// другом языке · год · длительность, (3) страна · жанры, (4) режиссёр,
// (5) главные роли — плюс бейджи (сезоны, магнет, размер). Пустые
// строки не выводятся.
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

  const line4 = it.director ? t('directorLabel') + ': ' + escapeHtml(it.director) : '';
  const actors = (it.actors || []).join(', ');
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

// Ленивое обогащение маленьких карточек: для карточек без длительности/
// страны/режиссёра/актёров подгружаем детали по мере появления в кадре
// (GET /api/films/{id}; уже обогащённые отдаются из БД без обращения к
// IMDb/Wikidata). Повторные запросы в рамках сессии исключены.
const enrichedFetched = new Set(); // id, для которых детали уже запрашивались
let enrichObserver = null;

// hasFilmExtras — есть ли в фильме данные обогащения карточки.
function hasFilmExtras(f) {
  return !!(f && (f.director || f.actors || f.movie_length || (f.countries && f.countries.length)));
}

// fetchCardEnrich запрашивает детали карточки. Сервер обогащает их из
// Wikidata В ФОНЕ (не блокируя запрос), поэтому при пустых extras
// переспрашиваем пару раз — данные обычно подтягиваются через 1-3 с.
async function fetchCardEnrich(el, id, retries) {
  try {
    const r = await fetch('/api/films/' + encodeURIComponent(id));
    if (!r.ok) return;
    const f = await r.json();
    if (hasFilmExtras(f)) {
      const meta = el.querySelector('.meta');
      if (meta) meta.innerHTML = cardMetaHtml(f);
      return;
    }
  } catch (e) {
    return;
  }
  if (retries > 0) setTimeout(() => fetchCardEnrich(el, id, retries - 1), 3000);
}

function enrichCardWhenVisible(it, card) {
  if (!it.imdb_id) return;
  if (it.director || it.actors || it.movie_length || (it.countries && it.countries.length)) return;
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

function openItem(it) {
  currentItem = it;
  modalTitle.textContent = dispTitle(it);
  // Новое ручное открытие — не применяем устаревшую точку возобновления.
  pendingResume = null;

  // Всегда показываем детали; плеер появится после выбора варианта.
  playerWrap.hidden = true;
  player.pause();
  player.removeAttribute('src');
  player.load();
  watchBtn.hidden = true; // «▶ Смотреть» появится, когда будут источники
  details.hidden = false;
  detailsNote.hidden = true; // устаревшая заметка «нет магнет-ссылки» не нужна
  showDetails(it);
  modal.hidden = false;
  updateResumeBtn(it);

  // Догружаем полные детали (описание на обоих языках, жанры, рейтинг).
  // Обогащение из Wikidata идёт в фоне — переспрашиваем, чтобы подхватить.
  if (it.imdb_id) {
    fetchFilmDetails(it.imdb_id, 3);
  }

  // Живой поиск доступных вариантов для просмотра на трекере.
  loadSources(it);
}

// syncWatchBtn — видимость кнопки «▶ Смотреть»: показываем, когда для текущей
// карточки есть готовые источники и плеер не играет (автозапуска нет — только
// явный старт).
function syncWatchBtn() {
  if (!watchBtn) return;
  const hasItems = Array.isArray(lastSourceItems) && lastSourceItems.length;
  const idle = !currentPlay || currentPlay.id !== lastSourceId || playerWrap.hidden;
  watchBtn.hidden = !(hasItems && idle && !modal.hidden);
  watchBtn.textContent = t('watch');
}

// watchNow — пользователь нажал «▶ Смотреть»: фильм играет лучшую раздачу,
// сериал — выбранную серию/озвучку (а если серия не выбрана — первую серию
// выбранного сезона).
async function watchNow() {
  const id = lastSourceId;
  const items = lastSourceItems;
  if (!id || !items || !items.length) return;
  if (!isSeriesKind(currentItem && currentItem.kind)) {
    playSource(id, items[0]);
    return;
  }
  // Сериал: без сезона берём первый известный.
  if (!selectedSeason) {
    selectedSeason = allKnownSeasons(id, items)[0] || null;
    if (!selectedSeason) { flashPlayerNote(t('noSources')); return; }
    renderSeasonChips(items, id);
    renderSeasonVoices(items, id);
  }
  // Без выбранной серии дожидаемся сетки и берём первую серию сезона.
  if (!selectedEpisode) {
    await ensureSeasonEpisodes(id, selectedSeason, items);
    const eps = (seriesEpisodes[id] && seriesEpisodes[id].bySeason[selectedSeason]) || [];
    if (eps.length) {
      selectedEpisode = eps[0];
      renderEpisodeGrid(id);
    }
  }
  await playVoiceEpisode(id, selectedSeason, selectedEpisode);
}

// fetchFilmDetails запрашивает детали фильма для модалки; при отсутствии
// обогащённых полей (сервер обогащает в фоне) переспрашивает пару раз.
async function fetchFilmDetails(id, retries) {
  try {
    const r = await fetch('/api/films/' + encodeURIComponent(id));
    if (!r.ok) return;
    const f = await r.json();
    if (f && (f.plot || f.plot_ru || f.genres || f.poster_url || f.director || f.actors || f.movie_length || (f.countries && f.countries.length))) {
      showDetails(f);
      if (!hasFilmExtras(f) && retries > 0) {
        setTimeout(() => fetchFilmDetails(id, retries - 1), 3000);
      }
    }
  } catch (e) {}
}

function showDetails(it) {
  details.hidden = false;
  detailsPoster.src = it.poster_url || it.poster || '';
  detailsPoster.hidden = !detailsPoster.src;

  // 1 строка — название на выбранном языке + рейтинг IMDB/TMDB.
  detailsTitle.textContent = dispTitle(it);
  detailsRating.textContent = fmtRating(it);

  // 2 строка — название на другом языке, год, длительность.
  const alt = dispTitleAlt(it);
  const year = it.year ? String(it.year) : '';
  const dur = fmtDuration(it.movie_length || it.duration);
  detailsSubtitle.textContent = [alt, year, dur].filter(Boolean).join(' · ');

  // 3 строка — страна производства, жанры.
  const countries = (it.countries || []).join(', ');
  const genres = (it.genres || []).map(dispGenre).join(', ');
  detailsMeta.textContent = [countries, genres].filter(Boolean).join(' · ');

  // 4 строка — режиссёр, 5 строка — главные роли.
  detailsDirector.textContent = it.director ? t('directorLabel') + ': ' + it.director : '';
  const actors = (it.actors || []).join(', ');
  detailsActors.textContent = actors ? t('actorsLabel') + ': ' + actors : '';

  detailsPlot.textContent = dispPlot(it) || t('noPlot');
}

// loadSources запрашивает у сервера доступные варианты для просмотра.
// Сервер ищет источники на трекере В ФОНЕ: первый ответ может прийти с
// пустым списком и статусом "searching" — тогда опрашиваем повторно,
// пока не придёт результат ("ready") или не выйдет таймаут. Так медленный
// NNM не «замораживает» страницу: карточка и плеер отрисовываются сразу.
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function loadSources(it, noAutoplay) {
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
  activeAudio = null;
  selectedSeason = null;
  selectedEpisode = null;
  // Ручной выбор озвучки/раздачи живёт в рамках одной карточки.
  for (const k in relPref) delete relPref[k];
  for (const k in voicePref) delete voicePref[k];
  autoVoice = '';
  lastTracksItems = [];

  // Фоновый поиск на трекере (Jackett) запускается на сервере сразу при
  // открытии карточки и идёт НЕ в фоне запроса: /sources отдаёт кэш
  // мгновенно (status=searching), а мы опрашиваем до готовности (до 3
  // минут) и перерисовываем источники сразу, как только они появились.
  const deadline = Date.now() + 180000;
  let netErrors = 0;
  let lastKey = ''; // ключ последней отрисовки — перерисовываем при изменении

  while (Date.now() < deadline) {
    let data;
    try {
      const res = await fetch('/api/films/' + encodeURIComponent(id) + '/sources');
      if (!res.ok) throw new Error('HTTP ' + res.status);
      data = await res.json();
      netErrors = 0;
    } catch (e) {
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
    const items = (data.items || []).filter((s) => s.magnet && s.seeds > 0);

    // Показываем источники сразу, как только они появились/изменились
    // (кэш может что-то содержать ещё до завершения фонового поиска).
    const withAudio = items.filter((s) => s.audio).length;
    const key = id + '|' + items.length + '|' + withAudio + '|' + (items[0] ? (items[0].magnet || '').slice(-24) : '');
    if (items.length && key !== lastKey) {
      lastKey = key;
      lastSourceItems = items;
      lastSourceId = id;
      renderSources(items, id);
      // Кнопка «▶ Смотреть» доступна, как только появились источники
      // (кэш может быть ещё не «готов», фон продолжит поиск).
      syncWatchBtn();
    }

    if (ready) {
      sourcesTitle.textContent = t('sourcesTitle');
      if (items.length === 0) {
        sourcesEmpty.hidden = false;
        sourcesEmpty.textContent = t('noSources');
        return;
      }
      // Автозапуска нет: воспроизведение начинается только по явному
      // действию — кнопке «▶ Смотреть», выбору серии (сериал) или озвучки.
      syncWatchBtn();
      return;
    }

    // Пока сервер ищет в фоне — обратный отсчёт.
    const sec = Math.max(0, Math.round((deadline - Date.now()) / 1000));
    sourcesTitle.textContent = t('searchingSources') + ' (' + sec + ' с)';
    await sleep(4000);
  }

  // Таймаут ожидания фонового поиска.
  sourcesTitle.textContent = t('sourcesTitle');
  sourcesEmpty.hidden = false;
  sourcesEmpty.textContent = t('sourceSearchTimeout');
}

// updateSourceLabels — подписи групп в блоке источников (сезон/серия/перевод).
function updateSourceLabels() {
  sourcesSeasonWrap.querySelector('.src-group-label').textContent = t('seasonLabel');
  sourcesEpisodesWrap.querySelector('.src-group-label').textContent = t('episodeLabel');
  sourcesAudioWrap.querySelector('.src-group-label').textContent = t('translationLabel');
  sourcesRelWrap.querySelector('.src-group-label').textContent = t('voicesLabel');
  tlCloseViewing.textContent = t('closeViewing');
}

// renderSources отображает доступные варианты как «переводы» (озвучки).
// Для сериала пользователь выбирает сезон и серию, и в зависимости от них
// предлагаются доступные переводы. Списка конкретных торрентов и выбора
// качества больше нет: по выбранному переводу автоматически играет лучшая
// раздача.
function renderSources(items, id) {
  lastSourceItems = items;
  lastSourceId = id;
  updateSourceLabels();
  const isSeries = isSeriesKind(currentItem && currentItem.kind);

  // Применяем точку возобновления (resumeItem), если источники не были
  // готовы: выделяем сохранённые сезон/серию и перевод.
  if (pendingResume) {
    const pr = pendingResume;
    pendingResume = null;
    if (isSeries) {
      if (pr.season > 0) selectedSeason = pr.season;
      if (pr.episode > 0) selectedEpisode = pr.episode;
    }
    const src = items.find((s) => s.magnet === pr.magnet);
    if (src) activeAudio = src.audio || '';
  }

  if (isSeries) {
    // Переводы у сериала, как и у фильма, — это звуковые дорожки выбранной
    // раздачи (блок #tracks «Студия (тип) ЯЗЫК»); «типовые» чипы
    // «Дублированный/Многоголосый» не показываем. Клик по серии в сетке
    // авто-запускает лучшую раздачу сезона.
    sourcesAudioWrap.hidden = true;
    sourcesAudio.innerHTML = '';
    renderSeasonChips(items, id);
    renderSeasonVoices(items, id);
    ensureSeasonEpisodes(id, selectedSeason, items).then(() => {
      // Если сезоны ещё не были известны — берём первый из реальных файлов
      // раздач (например, когда есть только полный сборник без сезонов в
      // названии, а число сезонов фильма в БД не заполнено).
      if (!selectedSeason) {
        const s2 = allKnownSeasons(id, items);
        selectedSeason = s2[0] || null;
        renderSeasonChips(items, id);
      }
      renderEpisodeGrid(id);
      renderSeasonVoices(items, id);
    });
  } else {
    // Фильм: переводы — это звуковые дорожки выбранной раздачи (список
    // в блоке #tracks: «Студия (тип) ЯЗЫК»), а не «типовые» чипы
    // «Дублированный/Многоголосый». Блок источников фильму не нужен.
    sourcesSeasonWrap.hidden = true;
    sourcesEpisodesWrap.hidden = true;
    sourcesAudioWrap.hidden = true;
    sourcesRelWrap.hidden = true;
    sourcesEl.hidden = true;
  }
}

// allKnownSeasons — сезоны, для которых известны раздачи или серии:
// из заголовков раздач (сезон N), из числа сезонов фильма в БД (если есть
// полный сборник) и из реально открытых файлов раздач (кэш seriesEpisodes).
function allKnownSeasons(id, items) {
  const set = new Set();
  for (const s of items) if ((s.season || 0) > 0) set.add(s.season);
  const filmN = (currentItem && currentItem.seasons) || 0;
  const hasFull = items.some((s) => (s.season || 0) === 0);
  if (hasFull && filmN > 0) for (let i = 1; i <= filmN; i++) set.add(i);
  const cache = seriesEpisodes[id];
  if (cache) for (const k in cache.bySeason) if (+k > 0) set.add(+k);
  return Array.from(set).sort((a, b) => a - b);
}

// renderSeasonChips — чипы выбора сезона сериала.
function renderSeasonChips(items, id) {
  const seasons = allKnownSeasons(id, items);
  if (selectedSeason === null || !seasons.includes(selectedSeason)) {
    selectedSeason = seasons[0] || null;
  }
  sourcesSeasonWrap.hidden = seasons.length <= 1;
  sourcesSeason.innerHTML = '';
  seasons.forEach((k) => {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'chip' + (k === selectedSeason ? ' active' : '');
    btn.textContent = seasonLabel(k);
    btn.addEventListener('click', () => {
      if (k === selectedSeason) return;
      selectedSeason = k;
      selectedEpisode = null;
      renderSeasonChips(items, id);
      // Сезон сменился — перестраиваем озвучки и подгружаем серии сезона.
      renderSeasonVoices(items, id);
      ensureSeasonEpisodes(id, selectedSeason, items).then(() => {
        renderEpisodeGrid(id);
        renderSeasonVoices(items, id);
      });
    });
    sourcesSeason.appendChild(btn);
  });
}

// srcRankF — рейтинг раздачи на фронтенде (зеркало серверного ранжирования
// «озвучка > качество > сиды»), чтобы выбрать лучшую раздачу внутри перевода.
function srcRankF(s) {
  const audioScore = { dub: 50, multi: 40, two: 30, original: 25, single: 20, subs: 10 }[s.audio] || 0;
  const qScore = { '2160': 40, '1080': 30, '720': 20, '480': 10 }[s.quality];
  return audioScore * 10000 + (qScore === undefined ? 15 : qScore) * 100 + Math.min(s.seeds || 0, 100);
}

// srcHeight — разрешение раздачи (высота) из строки качества. Нужно, чтобы
// авто-выбор раздачи предпочитал более высокое разрешение: тогда у плеера
// есть «доступные качества» (SRC + понижения), а не только SRC у 400p.
function srcHeight(s) {
  const m = String((s && s.quality) || '').match(/(\d{3,4})/);
  return m ? parseInt(m[1], 10) : 0;
}

// pickBestSource — лучшая раздача выбранного перевода для указанного сезона:
// сначала раздача именно этого сезона, затем полный сборник (сезон 0).
// Если раздач с такой меткой озвучки нет (озвучка определена по дорожкам,
// а в названии её не было) — берём лучшую раздачу вообще.
function pickBestSource(items, audio, season) {
  const cand = items.filter((s) => (s.audio || '') === (audio || ''));
  const pool = cand.length ? cand : items;
  let best = null;
  for (const s of pool) {
    const ss = s.season || 0;
    const score = (ss === season ? 1000000 : ss === 0 ? 900000 : 0) + srcRankF(s);
    if (!best || score > best._score) { best = s; best._score = score; }
  }
  return best;
}

// sourceRichness — оценка числа озвучек в раздаче по названию: известные
// студии + слова-типы (MVO/Dub/…). Нужна, чтобы для сезона авто-выбиралась
// раздача с БОЛЬШИМ числом переводов, а не первая одиночная.
function sourceRichness(s) {
  const t = String((s && s.title) || '').toLowerCase();
  const set = new Set();
  const add = (re) => { const m = t.match(re); if (m) m.forEach((x) => set.add(x)); };
  add(/lostfilm|newstudio|tvshows|hdrezka|baibako|amedia|novamedia|jaskier|сыендук|кураж[- ]бамбей|кипарис|red head sound|rhs|newcomers|le-production|невафильм|coldfilm|котов|яроцкий|сербин|доценко|гоблин|кубик в кубе/g);
  add(/\bmvo\b|\bdub\b|\bavo\b|многоголос\w*|дубляж|дублирован\w*|одноголос\w*|двухголос\w*/g);
  return set.size;
}

// seasonSourceOf — лучшая раздача, покрывающая сезон (точный сезон или
// полный сборник). Выбирается раздача с МАКСИМАЛЬНЫМ числом озвучек
// (sourceRichness); при равенстве — точный сезон, затем выше разрешение
// (больше доступных качеств), затем сиды.
function seasonSourceOf(items, season) {
  let best = null;
  for (const s of items) {
    const ss = s.season || 0;
    if (season && ss !== season && ss !== 0) continue;
    if (!season && ss !== 0) continue;
    const tie = (ss === season ? 1000000 : 900000) + srcHeight(s) + (s.seeds || 0) * 10;
    const score = sourceRichness(s) * 1e9 + tie;
    if (!best || score > best._score) { best = s; best._score = score; }
  }
  return best || items[0];
}

// pickSeasonSource — раздача сезона для автозапуска/сетки серий. Если
// пользователь вручную выбрал раздачу этого сезона в списке — уважаем его
// выбор (пока раздача покрывает сезон), иначе — «богатая» по озвучкам.
function pickSeasonSource(items, season) {
  const pref = relPref[season];
  if (pref) {
    const s = items.find((x) => x.magnet === pref && (((x.season || 0) === season) || (x.season || 0) === 0));
    if (s) return s;
  }
  return seasonSourceOf(items, season);
}

// titleVoices — какие озвучки (студии/оригинал) заявлены в заголовке
// раздачи. Используется для подписи раздачи в списке: «LostFilm + HDRezka
// Studio + Оригинал». Озвучка считается один раз (без дублей).
function titleVoices(title) {
  const low = ' ' + String(title || '').toLowerCase().replace(/[`’'']/g, '') + ' ';
  const out = [];
  const add = (d) => { if (!out.includes(d)) out.push(d); };
  const pats = [
    [/lostfilm/, 'LostFilm'], [/newstudi/, 'NewStudio'],
    [/tvshows/, 'TVShows'], [/hdrezka\s*studio/, 'HDRezka Studio'], [/hdrezka/, 'HDRezka'],
    [/baibako/, 'BaibaKo'], [/jaskier/, 'Jaskier'],
    [/novamedia/, 'НоваМедиа'], [/невафильм/, 'Невафильм'], [/coldfilm/, 'ColdFilm'],
    [/newcomers/, 'NewComers'], [/le[- ]production/, 'LE-Production'],
    [/red head sound/, 'Red Head Sound'], [/сыендук/, 'Сыендук'],
    [/кураж[- ]бамбей/, 'Кураж-Бамбей'], [/кипарис/, 'Кипарис'], [/кириллица/, 'Кириллица'],
    [/rg\.?\s?paravozik/, 'RG.Paravozik'], [/котов/, 'Котов'],
    [/яроцкий/, 'Яроцкий'], [/сербин/, 'Сербин'], [/доценко/, 'Доценко'],
    [/гоблин/, 'Гоблин'], [/кубик в кубе/, 'Кубик в кубе'],
  ];
  for (const [re, d] of pats) if (re.test(low)) add(d);
  // Amedia не «прячется» внутри других слов (например novamedia) — ищем
  // границей слова, как и короткие обозначения дорожек LF/HDr/TVS.
  if (!out.includes('Amedia') && /(?:^|[^a-zа-я0-9])amedia/.test(low)) add('Amedia');
  if (!out.includes('LostFilm') && /\blf\b/.test(low)) add('LostFilm');
  if (!out.includes('HDRezka') && /\bhdr\b/.test(low)) add('HDRezka');
  if (!out.includes('TVShows') && /\btvs\b/.test(low)) add('TVShows');
  // Оригинальная (английская) дорожка.
  if (/\beng(?:lish)?\b/.test(low) || /\boriginal\b/.test(low) || /оригинал/.test(low)) add('Оригинал');
  // «HDRezka Studio» уже покрывает и «HDRezka» — лишнее не дублируем.
  if (out.includes('HDRezka Studio')) {
    const i = out.indexOf('HDRezka');
    if (i >= 0) out.splice(i, 1);
  }
  return out;
}

// seasonReleaseSources — раздачи, покрывающие сезон (точный сезон или
// полный сборник), без дублей по magnet. Сортировка: «богатые» по числу
// озвучек первыми (как у авто-выбора), затем точный сезон, затем выше
// разрешение, затем сиды.
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

// Алиасы озвучек для сопоставления чипа с реальными дорожками файла
// (ffprobe): в названиях дорожек встречаются и сокращения (LF/HDr/TVS).
const VOICE_ALIASES = {
  'lostfilm': ['lostfilm', 'lf'],
  'newstudio': ['newstudio', 'new studio'],
  'tvshows': ['tvshows', 'tvs'],
  'hdrezka studio': ['hdrezka studio', 'hdrezka', 'hdr'],
  'hdrezka': ['hdrezka', 'hdr'],
  'baibako': ['baibako', 'baiba ko'],
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

// matchVoiceOrdinal — порядковый номер (ordinal) аудиодорожки файла, которая
// соответствует озвучке voice. «Оригинал» → английская/исходная дорожка.
// Возвращает null, если озвучка в файле не найдена.
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

// seasonVoices — озвучки, доступные для сезона: объединение по всем его
// раздачам, БЕЗ дублей (LostFilm из трёх раздач считается один раз).
// «Оригинал» есть в любом файле — добавляем всегда последним.
function seasonVoices(items, season) {
  const out = [];
  const add = (d) => { if (!out.includes(d)) out.push(d); };
  for (const s of seasonReleaseSources(items, season)) {
    titleVoices(s.title).forEach(add);
  }
  if (!out.includes('Оригинал')) out.push('Оригинал');
  return out;
}

// pickVoiceSource — лучшая раздача сезона, содержащая озвучку voice (первая
// в порядке «богатства»/сидов, чей заголовок её заявляет). Если ни одна
// раздача озвучку не заявляет (напр. «Оригинал» без пометки в названии) —
// «богатая» раздача по умолчанию.
function pickVoiceSource(items, season, voice) {
  for (const s of seasonReleaseSources(items, season)) {
    if (titleVoices(s.title).includes(voice)) return s;
  }
  return seasonSourceOf(items, season);
}

// renderSeasonVoices — чипы озвучек выбранного сезона (только озвучки,
// без списка торрентов). Торрент подбирается автоматически по озвучке:
// клик по серии играет раздачу, где эта озвучка есть, и включает её дорожку.
function renderSeasonVoices(items, id) {
  if (!id) {
    sourcesRelWrap.hidden = true;
    sourcesRel.innerHTML = '';
    return;
  }
  if (!isSeriesKind(currentItem && currentItem.kind) || !items || !items.length) {
    sourcesRelWrap.hidden = true;
    sourcesRel.innerHTML = '';
    return;
  }
  const voices = seasonVoices(items, selectedSeason);
  if (!voices.length) {
    sourcesRelWrap.hidden = true;
    sourcesRel.innerHTML = '';
    return;
  }
  sourcesRelWrap.hidden = false;
  sourcesRel.innerHTML = '';
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

// switchVoice — пользователь выбрал озвучку в чипах: запоминаем выбор и,
// если что-то уже играет/выбрана серия — переключаем просмотр на раздачу с
// этой озвучкой (торрент подбирается сам, дорожка включается автоматически).
async function switchVoice(id, voice) {
  const items = lastSourceItems;
  if (!id || !voice || !items || !items.length) return;
  const season = selectedSeason;
  const src = pickVoiceSource(items, season, voice);
  voicePref[season] = voice;
  if (src && src.magnet) relPref[season] = src.magnet;
  renderSeasonVoices(items, id);
  const activeView = currentPlay && currentPlay.id === id && !playerWrap.hidden;
  const cur = currentFile >= 0 ? lastFiles.find((x) => x.index === currentFile) : null;
  const ep = (cur && cur.episode) || selectedEpisode;
  if (!activeView && !ep) return; // озвучка запомнена — включится при выборе серии
  // Уже играет раздача с этой озвучкой — переключаем дорожку без переоткрытия
  // торрента (быстро, по уже известным дорожкам файла).
  if (activeView && src && currentPlay && src.magnet === currentPlay.magnet) {
    const vo = matchVoiceOrdinal(lastTracksItems, voice);
    if (vo != null && vo !== currentTrack) {
      currentTrack = vo;
      dbg('озвучка: в текущем файле «' + voice + '» -> дорожка #' + vo);
      playHls(id, currentPlay.magnet, currentFile, vo, streamStart, currentQuality, currentSubs);
      return;
    }
    return; // дорожку сопоставить не вышло — не перезапускаем без нужды
  }
  // Нужен другой торрент — переключаем просмотр на раздачу с этой озвучкой.
  await playVoiceEpisode(id, season, ep || null);
}

// playVoiceEpisode — играет серию (ep; null — первая серия сезона) из
// раздачи, подобранной по выбранной озвучке сезона. autoVoice передаётся
// в loadTracks, который включает нужную дорожку файла после анализа.
// Если выбранная раздача недоступна (нет пиров/файлов) — пробуем следующую
// с той же озвучкой, затем любую раздачу сезона (чтобы «ничего не
// происходило», если лучшая раздача мертва).
async function playVoiceEpisode(id, season, ep) {
  const items = lastSourceItems;
  if (!id || !items || !items.length) return;
  const voice = voicePref[season];
  // Кандидаты в порядке предпочтения: с выбранной озвучкой → любые сезона.
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

  const tok = ++playToken;
  let src = null, files = null, f = null;
  for (const c of cands) {
    if (!c || !c.magnet) continue;
    const fs = (await fetchFiles(id, c.magnet)) || [];
    if (tok !== playToken) return; // пользователь выбрал другое — выходим
    if (!fs.length) continue; // мёртвая/недоступная — пробуем следующую
    let ff = null;
    if (season && ep) ff = fs.find((x) => x.season === season && x.episode === ep);
    if (!ff && season) ff = fs.find((x) => x.season === season);
    if (!ff) ff = fs.find((x) => (x.season || 0) === 0) || fs[0];
    if (!ff) continue;
    src = c; files = fs; f = ff; break;
  }
  if (!src || !f || !files) {
    flashPlayerNote(t('episodesUnavailable'));
    return;
  }
  if (voice) relPref[season] = src.magnet;
  autoVoice = voice || '';
  dbg('озвучка: S' + (season || '?') + (ep ? ' E' + ep : '') + ' «' + (voice || 'авто') + '» -> "' + (src.title || '?') + '"');
  lastFiles = files;
  if ((f.season || 0) > 0) selectedSeason = f.season;
  selectedEpisode = f.episode || ep || null;
  activeAudio = src.audio || '';
  // Файлы выбранной раздачи могут отличаться от пробы — обновляем сетку серий.
  const cache = seriesEpisodes[id] || (seriesEpisodes[id] = { bySeason: {}, done: {}, probing: false });
  for (const x of files) {
    if ((x.season || 0) > 0 && (x.episode || 0) > 0) {
      const list = cache.bySeason[x.season] || (cache.bySeason[x.season] = []);
      if (!list.includes(x.episode)) list.push(x.episode);
    }
  }
  renderSeasonChips(items, id);
  renderEpisodeGrid(id);
  renderSeasonVoices(items, id);
  startStream(id, src, f.index); // loadTracks включит дорожку autoVoice
}


// ensureSeasonEpisodes гарантирует, что для сезона известны номера серий:
// открывает реальный торрент репрезентативной раздачи (лучше всего — полный
// сборник или раздача этого сезона) и записывает структуру в кэш
// seriesEpisodes. Кэш переживает закрытие карточки в рамках сессии.
async function ensureSeasonEpisodes(id, season, items) {
  if (!id) return;
  const cache = seriesEpisodes[id] || (seriesEpisodes[id] = { bySeason: {}, done: {}, probing: false });
  if (season && cache.done[season]) return;
  if (cache.probing) {
    if (cache._probe) await cache._probe;
    if (season && cache.done[season]) return;
    return;
  }
  cache.probing = true;
  const probe = (async () => {
    try {
      // Раздача для пробы — та же, что авто-стартует серию (богатая по числу
      // озвучек либо выбранная вручную), чтобы сетка серий соответствовала
      // реальным файлам. Пробуем несколько раздач по порядку: если лучшая
      // мертва/недоступна (нет пиров) — серии берутся из следующей живой.
      const cands = seasonReleaseSources(items, season);
      const fallback = items.filter((s) => s.magnet).slice(0, 3);
      const list = cands.length ? cands.slice(0, 4) : fallback;
      for (const src of list) {
        if (!src || !src.magnet) continue;
        const files = (await fetchFiles(id, src.magnet)) || [];
        if (!files || !files.length) continue;
        for (const f of files) {
          if ((f.season || 0) > 0) {
            const bySeason = cache.bySeason[f.season] || (cache.bySeason[f.season] = []);
            if ((f.episode || 0) > 0 && !bySeason.includes(f.episode)) bySeason.push(f.episode);
          }
        }
        if (season && cache.bySeason[season] && cache.bySeason[season].length) break;
        if (!season && Object.keys(cache.bySeason).length) break;
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

// fetchFiles запрашивает список видеофайлов раздачи (сезоны/серии).
async function fetchFiles(id, magnet) {
  try {
    const res = await fetch(`/api/films/${encodeURIComponent(id)}/files?magnet=${encodeURIComponent(magnet)}`);
    if (!res.ok) throw new Error('HTTP ' + res.status);
    const data = await res.json();
    return data.files || [];
  } catch (e) {
    dbg('files: ошибка ' + id + ': ' + e.message);
    return null;
  }
}

// renderEpisodeGrid — сетка серий выбранного сезона (каноническая, из кэша).
function renderEpisodeGrid(id) {
  const cache = seriesEpisodes[id];
  const eps = (cache && cache.bySeason[selectedSeason]) || [];
  sourcesEpisodesWrap.hidden = !(eps && eps.length);
  sourcesEpisodes.innerHTML = '';
  if (!eps || !eps.length) {
    if (cache && cache.probing) {
      const note = document.createElement('div');
      note.className = 'tracks-note';
      note.textContent = t('episodesLoading');
      sourcesEpisodes.appendChild(note);
    }
    return;
  }
  eps.forEach((ep) => {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'ep-btn' + (ep === selectedEpisode ? ' active' : '');
    btn.textContent = t('episodeLabel') + ' ' + ep;
    btn.addEventListener('click', () => onPickEpisode(ep));
    sourcesEpisodes.appendChild(btn);
  });
}

// onPickEpisode — пользователь выбрал серию: играем её из раздачи, которая
// подобрана по выбранной озвучке сезона (если озвучка не выбрана — из
// «богатой» раздачи по умолчанию). Выбранная озвучка включается как дорожка
// файла автоматически, её реальные дорожки видны в блоке #tracks.
async function onPickEpisode(ep) {
  const id = lastSourceId;
  const items = lastSourceItems;
  if (!id || !items || !items.length) return;
  selectedEpisode = ep;
  renderEpisodeGrid(id);
  await playVoiceEpisode(id, selectedSeason, ep);
}

// renderTranslationChips — типовые чипы перевода больше не используются
// (ни для фильмов, ни для сериалов): переводы — это реальные звуковые
// дорожки выбранной раздачи, они показываются в блоке #tracks.
// Оставлена заглушкой для совместимости с оставшимися вызовами.
function renderTranslationChips(items, id) {
  sourcesAudioWrap.hidden = true;
  sourcesAudio.innerHTML = '';
}

// Кэш фолбэк-переводов, определённых по дорожкам (id -> ключ озвучки).
const fallbackAudioCache = {};

// detectAudioFromTracks — если по названиям раздач озвучка не распознана,
// пробует определить её по реальным звуковым дорожкам лучших раздач
// (ffprobe /tracks). Результат кэшируется на сессию.
async function detectAudioFromTracks(items, id) {
  if (id in fallbackAudioCache) return fallbackAudioCache[id];
  for (const s of items.slice(0, 3)) {
    const data = await fetchTracksJson(id, s.magnet, -1);
    const audio = data ? audioFromTracks(data) : '';
    if (audio) {
      fallbackAudioCache[id] = audio;
      dbg('переводы: по дорожкам раздачи "' + (s.title || '?').slice(0, 40) + '" -> ' + audio);
      return audio;
    }
  }
  fallbackAudioCache[id] = '';
  return '';
}

// fetchTracksJson — полный ответ /tracks (дорожки/длительность) без изменения
// глобального состояния плеера (используется для определения озвучки).
async function fetchTracksJson(id, magnet, file) {
  try {
    const q = new URLSearchParams({ magnet });
    if (typeof file === 'number' && file >= 0) q.set('file', String(file));
    const res = await fetch(`/api/films/${encodeURIComponent(id)}/tracks?` + q.toString());
    if (!res.ok) throw new Error('HTTP ' + res.status);
    return await res.json();
  } catch (e) {
    dbg('tracks(фолбэк): ' + id + ': ' + e.message);
    return null;
  }
}

// audioFromTracks — определяет группу озвучки по реальным звуковым дорожкам
// (ffprobe): язык/заголовок дорожки. Русская дорожка → группа из её
// заголовка (дубляж/многоголосый/двухголосый/одноголосый), иначе «Оригинал».
function audioFromTracks(data) {
  const items = (data && data.items) || [];
  if (!items.length) return '';
  const ru = items.find((tr) => (tr.language || '').toLowerCase().indexOf('ru') === 0)
    || items.find((tr) => /рус|озвуч|перевод/i.test(tr.title || ''));
  if (!ru) return 'original'; // русской дорожки нет — оригинал
  const low = ((ru.title || '') + ' ' + (ru.language || '')).toLowerCase();
  if (/дублирован|дубляж|\bdub\b/.test(low)) return 'dub';
  if (/двухголос|2\s*голос|2vo|dvo/.test(low)) return 'two';
  if (/одноголос|авторск|single\s*voice|avo|svo/.test(low)) return 'single';
  if (/многоголос|multi\s*voice|mvo/.test(low)) return 'multi';
  return 'multi'; // русская дорожка есть, тип не распознан — многоголосый
}

// audioLabel — отображаемое имя группы озвучки.
function audioLabel(k) {
  const map = { dub: 'audioDub', multi: 'audioMulti', two: 'audioTwo', original: 'audioOriginal', single: 'audioSingle', subs: 'audioSubs' };
  return map[k] ? t(map[k]) : t('audioNone');
}

// isHevcCodec — является ли кодек видео HEVC/H.265 (браузеры через MSE
// такой кодек не декодируют, нужен серверный H.264).
function isHevcCodec(codec) {
  return !!codec && (codec.indexOf('hevc') === 0 || codec.indexOf('hvc1') === 0 || codec.indexOf('hev1') === 0);
}

// seasonLabel — подпись чипа сезона (0 — «полный» сборник без сезона).
function seasonLabel(k) {
  if (!k) return t('seasonFull');
  return t('seasonLabel') + ' ' + k;
}

// isSeriesKind — является ли тип контента сериалом (есть сезоны/серии).
// Помимо нормализованных типов учитываем legacy-значения из БД:
// kind='animation', а IMDb-поиск мог записать 'tv mini-series' с дефисом.
// Без этого у таких записей не показывался селектор сезонов/серий.
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

// Токен смены источника: защита от гонок в асинхронных запусках (пока
// открываются файлы/серии, пользователь мог выбрать другой перевод/фильм).
let playToken = 0;

// startStream запускает HLS-поток раздачи s, файла fileIdx (серия; -1 — авто),
// с начала в исходном качестве. Обновляет селектор переводов/серий.
function startStream(id, s, fileIdx) {
  if (!s || !s.magnet) return;
  // Фильмы: переводы — дорожки файла в блоке #tracks, селектор источников
  // (сезоны/чипы) не нужен — скрываем его при старте просмотра.
  if (!isSeriesKind(currentItem && currentItem.kind) && !sourcesEl.hidden) {
    sourcesEl.hidden = true;
  }
  details.hidden = true;
  playerWrap.hidden = false;
  currentPlay = { id: id, magnet: s.magnet };
  streamStart = 0;
  currentTrack = 0;
  currentSubs = -1;
  currentSubtitles = [];
  currentQuality = 'source';
  currentFile = (typeof fileIdx === 'number' && fileIdx >= 0) ? fileIdx : -1;
  currentVideoCodec = '';
  currentVideoHeight = 0; // разрешение нового источника узнаем из /tracks
  h264FallbackDone = false;
  pendingCodecNote = false;
  streamRestarts = 0; // новый источник — свежие попытки перезапуска
  autoNextFired = false;
  updateQualityButtons();
  loadTracks(id, s.magnet, currentFile);
  playHls(id, s.magnet, currentFile, 0, 0, 'source');
  // Подсвечиваем активный перевод и серию в селекторе источников.
  if (!sourcesEl.hidden && lastSourceItems.length && id === (currentItem && (currentItem.imdb_id || currentItem.id))) {
    renderTranslationChips(lastSourceItems, id);
    if (isSeriesKind(currentItem && currentItem.kind) && currentFile >= 0) {
      const f = lastFiles.find((x) => x.index === currentFile);
      if (f && (f.season || 0) > 0) selectedSeason = f.season;
      if (f && (f.episode || 0) > 0) selectedEpisode = f.episode;
      renderEpisodeGrid(id);
    }
  }
  syncWatchBtn(); // плеер виден — кнопку «▶ Смотреть» прячем
  if (typeof playerWrap.scrollIntoView === 'function') {
    playerWrap.scrollIntoView({ block: 'nearest' });
  }
}

// playTranslation запускает просмотр выбранного перевода (озвучки) для
// выбранных сезона/серии. Для сериала открывает реальный торрент лучшей
// раздачи перевода: если в ней нет выбранной серии (озвучка сделана не на
// все серии) — показывает диалог с доступными сериями либо «закрыть просмотр».
async function playTranslation(id, audio) {
  if (!id || audio === null) return;
  const items = lastSourceItems;
  activeAudio = audio;
  if (sourcesEl && !sourcesEl.hidden) renderTranslationChips(items, id);
  const tok = ++playToken;

  const isSeries = isSeriesKind(currentItem && currentItem.kind);
  const s = pickBestSource(items, audio, selectedSeason);
  if (!s) {
    dbg('перевод: ' + id + ' «' + audioLabel(audio) + '» — нет подходящей раздачи');
    return;
  }
  dbg('перевод: ' + id + ' -> «' + audioLabel(audio) + '» раздача "' + (s.title || '?') + '"');

  if (!isSeries) {
    hideEpisodes();
    startStream(id, s, -1);
    return;
  }

  // Открываем реальные файлы раздачи и ищем выбранную серию.
  const files = await fetchFiles(id, s.magnet) || [];
  if (tok !== playToken) return; // пользователь выбрал другое — выходим
  lastFiles = files;
  if (!files.length) {
    flashPlayerNote(t('episodesUnavailable'));
    return;
  }
  // Ищем файл выбранных сезона/серии. Если сезон/серия ещё не выбраны
  // (структура неизвестна) — играем первую серию раздачи.
  let file = null;
  if (selectedSeason && selectedEpisode) {
    file = files.find((f) => f.season === selectedSeason && f.episode === selectedEpisode);
  } else if (selectedSeason) {
    file = files.find((f) => f.season === selectedSeason);
  } else {
    file = files[0];
  }
  if (file) {
    if (file.season > 0) selectedSeason = file.season;
    if (file.episode > 0) selectedEpisode = file.episode;
    startStream(id, s, file.index);
    return;
  }
  // Выбранной серии нет в этой раздаче — озвучка не сделана на все серии.
  showTranslationDialog(id, audio, s, files, selectedSeason, selectedEpisode);
}

// playSource запускает стрим конкретной раздачи без выбора перевода
// (автозапуск фильмов, переход на следующий сезон). Для сериала играет
// первую серию раздачи.
async function playSource(id, s) {
  if (!s || !s.magnet) return;
  dbg('playSource: ' + id + ' -> "' + (s.title || '?') + '" сидов=' + s.seeds);
  const tok = ++playToken;
  const isSeries = isSeriesKind(currentItem && currentItem.kind);
  activeAudio = s.audio || '';
  let fileIdx = -1;
  if (isSeries) {
    fileIdx = await loadEpisodes(id, s.magnet);
    if (tok !== playToken) return; // источник могли сменить, пока грузились серии
  } else {
    hideEpisodes();
  }
  startStream(id, s, fileIdx);
}

// hlsLogSrc — URL манифеста для лога: длинный параметр magnet заменяем на
// info_hash (btih:...), чтобы строка читалась и не засоряла лог.
function hlsLogSrc(src) {
  return src.replace(/magnet=([^&]*)/, (all, val) => {
    let v = val;
    try { v = decodeURIComponent(val); } catch (e) {}
    const h = (v.match(/btih:([0-9a-fA-F]{40})/) || [])[1];
    return 'magnet=' + encodeURIComponent(h ? 'magnet:?xt=urn:btih:' + h : 'magnet');
  });
}

// playHls запускает HLS-поток файла file (серия сериала; -1 — авто),
// звуковой дорожки track, со смещением start (перемотка), качеством
// quality (серверное понижение: source|2160|1080|720|480) и субтитром
// subs (ordinal субтитр-дорожки; -1 — без субтитров). Выбор субтитра
// (как и смена дорожки/качества) перезапускает ffmpeg-поток с нужной
// дорожкой; вкл/выкл текущей дорожки делает hls.js (subtitleTrack).
function playHls(id, magnet, file, track, start, quality, subs) {
  if (hlsPlayer) {
    hlsPlayer.destroy();
    hlsPlayer = null;
  }
  player.pause();
  player.removeAttribute('src');
  player.load();
  playerError.hidden = true;
  // Если это автофолбэк H.265→H.264 — держим уведомление видимым до
  // старта потока (перекодирование идёт не мгновенно).
  const keepCodecNote = pendingCodecNote;
  pendingCodecNote = false;
  if (keepCodecNote) {
    playerError.textContent = t('codecFallback');
    playerError.hidden = false;
  }
  // Показываем плеер при любом запуске — в т.ч. при повторном выборе
  // дорожки после ошибки, чтобы он не остался скрытым.
  playerWrap.hidden = false;

  currentTrack = track || 0;
  currentSubs = (typeof subs === 'number' && subs >= 0) ? subs : -1;
  currentQuality = quality || 'source';
  streamStart = start || 0;
  currentFile = (typeof file === 'number' && file >= 0) ? file : -1;
  autoNextFired = false; // новый поток — автопереход можно снова
  updateQualityButtons();
  // При старте потока из любой позиции (резюм, перемотка, новая серия)
  // всегда запрашиваем общую длительность — иначе шкала/время не покажут её.
  fetchDuration(id, magnet, currentFile);

  const p = new URLSearchParams({ magnet });
  p.set('track', String(currentTrack));
  if (currentFile >= 0) p.set('file', String(currentFile));
  if (currentSubs >= 0) p.set('subs', String(currentSubs));
  if (streamStart > 0) p.set('start', String(streamStart));
  if (currentQuality !== 'source') p.set('quality', currentQuality);
  const src = `/api/films/${encodeURIComponent(id)}/hls.m3u8?` + p.toString();
  dbg('playHls: id=' + id + ' file=' + currentFile + ' track=' + currentTrack + ' subs=' + currentSubs + ' start=' + streamStart + ' q=' + currentQuality);

  if (window.Hls && Hls.isSupported()) {
    const hlsConfig = {
      liveDurationInfinity: true,
      debug: DEBUG,
      manifestLoadingTimeOut: 60000,
      manifestLoadingMaxRetry: 2,
      manifestLoadingRetryDelay: 2000,
      manifestLoadingMaxRetryTimeout: 60000,
      levelLoadingTimeOut: 30000,
      levelLoadingMaxRetry: 4,
      levelLoadingRetryDelay: 2000,
      levelLoadingMaxRetryTimeout: 60000,
      fragLoadingTimeOut: 20000,
      fragLoadingMaxRetry: 6,
      fragLoadingRetryDelay: 1500,
      fragLoadingMaxRetryTimeout: 90000,
      // Наш поток — прогрессивный файл БЕЗ #EXT-X-ENDLIST, поэтому hls.js
      // считает его «живым» и включает latency-контроллер: при старте ставит
      // позицию на liveSyncPosition (край буфера минус задержка) и при
      // отставании делает «догоняющий» seek вперёд — из-за этого видео
      // «перепрыгивает» в тайминге вперёд от точки старта/возобновления/
      // перемотки. Для нас поток — по сути VOD, поэтому живую синхронизацию
      // отключаем полностью: liveSyncDurationCount делаем большим (стартовая
      // позиция всегда 0 = начало потока) и убираем принудительную догоняющую
      // перемотку (liveMaxLatencyDurationCount = Infinity). maxLiveSyncPlaybackRate=1
      // — не ускорять воспроизведение.
      liveSyncDurationCount: 100,
      liveMaxLatencyDurationCount: Infinity,
      maxLiveSyncPlaybackRate: 1,
    };
    // Подробный лог создания потока: версия hls.js, URL манифеста (с
    // сокращённым магнетом до info_hash), применённые таймауты и состояние
    // плеера на момент старта.
    dbg('hls.js: создаём поток v' + (Hls.version || '?') + ' [' + hlsLogSrc(src) + ']'
      + ' | live=' + hlsConfig.liveDurationInfinity
      + ' manifestTO=' + hlsConfig.manifestLoadingTimeOut
      + ' levelTO=' + hlsConfig.levelLoadingTimeOut
      + ' fragTO=' + hlsConfig.fragLoadingTimeOut
      + ' liveSync=' + hlsConfig.liveSyncDurationCount + '/' + hlsConfig.liveMaxLatencyDurationCount
      + ' | плеер readyState=' + player.readyState + ' t=' + Math.round(player.currentTime || 0));
    const hls = new Hls(hlsConfig);
    hlsPlayer = hls;
    hls.loadSource(src);
    hls.attachMedia(player);
    hls.on(Hls.Events.MANIFEST_PARSED, () => {
      // Игнорируем событие от уже заменённого плеера (после автофолбэка
      // H.265→H.264): иначе старый манифест HEVC скрыл бы уведомление о
      // перекодировании ещё до старта H.264-потока.
      if (hlsPlayer !== hls) return;
      streamRestarts = 0; // поток успешно стартовал — свежие попытки перезапуска
      dbg('hls: манифест получен (уровней: ' + (hls.levels ? hls.levels.length : 0) + ' субтитров: ' + (hls.subtitleTracks ? hls.subtitleTracks.length : 0) + ')');
      // Включаем субтитры, если поток запущен с выбранной дорожкой
      // (currentSubs >= 0) и hls.js видит SUBTITLES-рендеринг в манифесте;
      // иначе — выключаем (дорожки нет либо пользователь выбрал «Выкл»).
      if (hls.subtitleTracks) {
        hls.subtitleTrack = (currentSubs >= 0 && hls.subtitleTracks.length) ? 0 : -1;
        hls.subtitleDisplay = true;
      }
      playerError.hidden = true; // поток стартовал — убираем уведомление о перекодировании
      player.play().catch((err) => dbg('player.play(): ' + err));
    });
    hls.on(Hls.Events.LEVEL_SWITCHED, (_e, d) => dbg('hls: уровень переключён: ' + d.level));
    hls.on(Hls.Events.FRAG_BUFFERED, (_e, d) => dbg('hls: фрагмент ' + d.frag.sn + ' @ ' + Math.round(d.frag.start) + 's'));
    hls.on(Hls.Events.ERROR, (_evt, data) => {
      // События от уже заменённого плеера (после автофолбэка/смены дорожки
      // или качества) игнорируем: устаревший mediaError/bufferAppendError от
      // HEVC-потока не должен показывать ложную ошибку поверх работающего
      // H.264-фолбэка.
      if (hlsPlayer !== hls) return;
      const resp = data && data.response ? ' (http ' + data.response.code + ')' : '';
      dbg('hls: ERROR type=' + (data && data.type) + ' details=' + (data && data.details) + resp);
      // Фатальная ошибка (обычно кодек видео) — не фолбэчим на сырой
      // поток: там AC3/DTS, и звука всё равно не будет. Показываем ошибку,
      // НО плеер не скрываем: селектор дорожек и вариантов остаётся
      // доступным, чтобы можно было выбрать другую дорожку или вариант
      // с H.264 (playHls при повторном запуске снова покажет плеер).
      if (data && data.fatal) {
        dbg('hls: ФАТАЛЬНАЯ ошибка, воспроизведение остановлено');
        // Автофолбэк: кодек (обычно H.265/HEVC) не поддерживается браузером
        // (mediaError / bufferAppendError) — вместо ошибки перезапускаем
        // поток с перекодированием в H.264 (1080p). Один раз на источник.
        if (data.type === 'mediaError' && currentQuality === 'source' && !h264FallbackDone && currentPlay) {
          h264FallbackDone = true;
          dbg('hls: H.265/HEVC — автофолбэк на H.264 (1080p)');
          if (hlsPlayer === hls) hlsPlayer = null;
          hls.destroy();
          pendingCodecNote = true;
          playHls(currentPlay.id, currentPlay.magnet, currentFile, currentTrack, streamStart, '1080', currentSubs);
          return;
        }
        // ВОССТАНОВЛЕНИЕ после простоя: ffmpeg-сессия могла быть остановлена
        // cleanup'ом (простаивала >90с — пауза/свёрнутая вкладка), а временные
        // файлы удалены. При возобновлении hls.js получает 404 на сегмент —
        // вместо фатальной ошибки прозрачно перезапускаем поток с текущей
        // позиции: servePlaylist заново поднимет ffmpeg, а если торрент успел
        // выгрузиться из памяти — создастся и новый поток.
        if (data.type === 'networkError' && data.response && data.response.code === 404
            && currentPlay && streamRestarts < maxStreamRestarts) {
          streamRestarts++;
          let restartPos = Math.floor(streamStart + (player.currentTime || 0));
          if (!isFinite(restartPos) || restartPos < 0) restartPos = 0;
          dbg('hls: сессия ffmpeg остановлена по простою — перезапуск с ' + restartPos + 's (попытка ' + streamRestarts + '/' + maxStreamRestarts + ')');
          if (hlsPlayer === hls) hlsPlayer = null;
          hls.destroy();
          playHls(currentPlay.id, currentPlay.magnet, currentFile, currentTrack, restartPos, currentQuality, currentSubs);
          return;
        }
        if (hlsPlayer === hls) hlsPlayer = null;
        hls.destroy();
        const resp = data.response ? ' (http ' + data.response.code + ')' : '';
        const detail = [data.type, data.details].filter(Boolean).join(' / ') + resp;
        // Помимо человекочитаемого текста показываем технические детали
        // ошибки — так видно реальную причину (кодек, 502, таймаут и т.п.).
        playerError.textContent = t('playbackError') + (detail ? ' — ' + detail : '');
        playerError.hidden = false;
        showDebug();
        // Подсказываем, где выбрать другой вариант/дорожку (сообщение
        // идёт прямо перед селекторами дорожек и источников).
        if (typeof playerError.scrollIntoView === 'function') {
          playerError.scrollIntoView({ block: 'nearest' });
        }
      }
    });
  } else if (player.canPlayType && player.canPlayType('application/vnd.apple.mpegurl')) {
    // Нативные HLS (Safari).
    dbg('HLS: нативный (Safari)');
    player.src = src;
    player.play().catch(() => {});
  } else {
    // HLS не поддерживается браузером — сырой поток (без транскодинга).
    dbg('HLS: не поддерживается — сырой поток');
    const qs = new URLSearchParams({ magnet });
    if (currentFile >= 0) qs.set('file', String(currentFile));
    player.src = `/api/stream/${encodeURIComponent(id)}?` + qs.toString();
    player.play().catch(() => {});
  }
}

// fetchDuration запрашивает полную длительность файла (GET /tracks) и
// обновляет totalDuration. Вызывается при каждом старте потока (playHls),
// чтобы при просмотре с ЛЮБОЙ позиции (резюм, перемотка, новая серия,
// смена качества/дорожки) шкала и время всегда показывали общую
// длительность. Не дублирует loadTracks: если запрос для этого файла уже
// идёт (inflight) или длительность уже известна — пропускаем.
async function fetchDuration(id, magnet, file) {
  const key = id + '|' + (magnet || '') + '|' + (typeof file === 'number' ? file : -1);
  if (durationFetch.key === key && durationFetch.inflight) return; // уже идёт
  if (durationFetch.key === key && !durationFetch.inflight && totalDuration > 0) return; // уже известна
  durationFetch.key = key;
  durationFetch.inflight = true;
  try {
    const q = new URLSearchParams({ magnet });
    if (typeof file === 'number' && file >= 0) q.set('file', String(file));
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), 30000);
    let res;
    try {
      res = await fetch(`/api/films/${encodeURIComponent(id)}/tracks?` + q.toString(), { signal: ctrl.signal });
    } finally {
      clearTimeout(timer);
    }
    if (!res.ok) throw new Error('HTTP ' + res.status);
    const data = await res.json();
    if (data.duration > 0) {
      totalDuration = data.duration;
      updatePlayerUI();
      dbg('tracks: длительность ' + id + ' = ' + data.duration + 's');
    }
  } catch (e) {
    // Сброс ключа: при следующем старте потока попробуем снова.
    durationFetch.key = '';
    dbg('tracks: длительность ' + id + ': ' + e.message);
  } finally {
    durationFetch.inflight = false;
  }
}

// loadTracks запрашивает длительность и список звуковых дорожек файла.
async function loadTracks(id, magnet, file) {
  // Заявляем ключ длительности — чтобы fetchDuration в playHls не дублировал
  // этот же запрос (длительность мы получим здесь же).
  durationFetch.key = id + '|' + (magnet || '') + '|' + (typeof file === 'number' ? file : -1);
  durationFetch.inflight = true;
  // У сериалов выбор озвучки — это чипы «Озвучки», а не сырой список дорожек
  // файла (#tracks «Перевод»): показываем его только у фильмов.
  if (isSeriesKind(currentItem && currentItem.kind)) {
    tracksEl.hidden = true;
    tracksList.innerHTML = '';
  } else {
    tracksEl.hidden = false;
    tracksList.innerHTML = '';
    tracksTitle.textContent = t('tracksLoading');
  }
  try {
    const q = new URLSearchParams({ magnet });
    if (typeof file === 'number' && file >= 0) q.set('file', String(file));
    // Таймаут чуть больше серверного (ffprobe ждёт данные до ~90с), чтобы
    // запрос не висел вечно при недоступных пирах и появилась ошибка.
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), 100000);
    let res;
    try {
      res = await fetch(`/api/films/${encodeURIComponent(id)}/tracks?` + q.toString(), { signal: ctrl.signal });
    } finally {
      clearTimeout(timer);
    }
    if (!res.ok) throw new Error('HTTP ' + res.status);
    const data = await res.json();
    // Полная длительность — для отображения в кастомных контролах плеера.
    totalDuration = data.duration || 0;
    updatePlayerUI();
    // Запрос длительности завершён — fetchDuration в playHls больше не нужен.
    durationFetch.inflight = false;
    currentVideoCodec = (data.codec || '').toLowerCase();
    // Реальное разрешение источника — по нему показываем только доступные
    // кнопки качества (нельзя «повысить» 1080p до 4K).
    currentVideoHeight = data.height || 0;
    updateQualityButtons();
    const items = data.items || [];
    lastTracksItems = items;
    const subtitles = data.subtitles || [];
    dbg('tracks: id=' + id + ' duration=' + (data.duration || '?') + 's дорожек=' + items.length + ' субтитров=' + subtitles.length + ' video=' + currentVideoCodec + '/' + currentVideoHeight + 'p');
    // Субтитры (из того же ffprobe) — показываем селектор, если есть
    // текстовые дорожки. Рендерим ДО раннего возврата по аудио-дорожкам,
    // чтобы блок субтитров появлялся даже при одной/нулевой звуковой дорожке.
    currentSubtitles = subtitles;
    renderSubtitles(subtitles, id, magnet);
    // Дорожки отдаются с ПОРЯДКОВЫМ номером среди аудио (ordinal: 0, 1, ...)
    // — это и есть track для /hls.m3u8 (сервер делает -map 0:a:ordinal).
    // Глобальный index потока для маппинга не годится: у MKV видео — индекс
    // 0, аудио 1..N, и track=0 по индексу указывал бы на ВИДЕО (→ в HLS два
    // видео-потока без звука → bufferAppendError). Корректируем дефолт на
    // первый аудио-поток; если аудио нет — track=-1 (video-only).
    if (items.length === 0) {
      currentTrack = -1;
    } else if (!items.some((t) => t.ordinal === currentTrack)) {
      currentTrack = items[0].ordinal;
    }
    // Если озвучка выбрана в чипах (autoVoice) и в файле несколько дорожек —
    // автоматически включаем её дорожку: перезапускаем поток с её ordinal.
    const av = autoVoice;
    autoVoice = '';
    const seriesNow = isSeriesKind(currentItem && currentItem.kind);
    if (av && items.length > 1) {
      const vo = matchVoiceOrdinal(items, av);
      if (vo != null && vo !== currentTrack && currentPlay) {
        currentTrack = vo;
        dbg('tracks: авто-озвучка «' + av + '» -> дорожка #' + vo);
        if (!seriesNow) renderTracks(items, id, magnet);
        playHls(currentPlay.id, currentPlay.magnet, currentFile, vo, streamStart, currentQuality, currentSubs);
        return;
      }
    }
    // Видео в HEVC (H.265) браузер через MSE не играет. Если играем в
    // исходном качестве — упреждающий автофолбэк на H.264 (1080p).
    if (isHevcCodec(currentVideoCodec) && currentQuality === 'source' && !h264FallbackDone && currentPlay) {
      h264FallbackDone = true;
      dbg('tracks: H.265/HEVC — автофолбэк на H.264 (1080p)');
      pendingCodecNote = true;
      playHls(currentPlay.id, currentPlay.magnet, currentFile, currentTrack, streamStart, '1080', currentSubs);
    }
    // У сериалов дорожки файла не показываем списком — только чипы озвучек.
    if (seriesNow) {
      tracksEl.hidden = true;
      tracksList.innerHTML = '';
      return;
    }
    if (items.length <= 1) {
      tracksEl.hidden = true;
      return;
    }
    renderTracks(items, id, magnet);
  } catch (e) {
    dbg('tracks: ошибка ' + id + ': ' + e.message);
    // Сбрасываем ключ: длительность не получена — следующий старт потока
    // (перемотка, смена серии) запросит её заново.
    durationFetch.inflight = false;
    durationFetch.key = '';
    // Субтитры тоже неизвестны — скрываем селектор.
    currentSubtitles = [];
    lastTracksItems = [];
    subsEl.hidden = true;
    subsList.innerHTML = '';
    // Явно показываем, что дорожки недоступны (нет пиров/данных), чтобы
    // плеер не выглядел зависшим и была подсказка выбрать другой вариант.
    // У сериалов этот блок не используется (выбор озвучек — чипами).
    if (isSeriesKind(currentItem && currentItem.kind)) {
      tracksEl.hidden = true;
      tracksList.innerHTML = '';
    } else {
      tracksEl.hidden = false;
      tracksTitle.textContent = t('audioTracks');
      tracksList.innerHTML = '<div class="tracks-note">' + t('tracksUnavailable') + '</div>';
    }
  }
}

// Тип перевода в начале заголовка звуковой дорожки (в русских рипах
// заголовок вида «Дубляж Red Head Sound», «MVO LostFilm», «AVO Юрий
// Сербин», «VO Михаил Яроцкий», «Original») → короткая пометка и студия.
function splitAudioTrackTitle(title) {
  const raw = (title || '').trim();
  const m = raw.match(/^(дубляж|дублирован|продубляж|dub|mvo|многоголос\w*|multi|avo|vo|авторск\w*|одноголос\w*|двухголос\w*|2vo|two|original|оригинал\w*|ost)\s*[:\-–|]?\s*(.*)$/i);
  if (!m) return { type: '', studio: raw };
  const head = m[1].toLowerCase();
  let type = '';
  if (head === 'dub' || head.indexOf('дубляж') === 0 || head.indexOf('дублирован') === 0 || head.indexOf('продубляж') === 0) type = 'д';
  else if (head === 'mvo' || head === 'multi' || head.indexOf('многоголос') === 0) type = 'М';
  else if (head === 'avo' || head === 'vo' || head.indexOf('авторск') === 0 || head.indexOf('одноголос') === 0) type = 'а';
  else if (head === 'two' || head === '2vo' || head.indexOf('двухголос') === 0) type = '2';
  else if (head === 'original' || head === 'ost' || head.indexOf('оригинал') === 0) type = 'orig';
  // Студия: без английского пояснения после «/» (например «/ Movie
  // Dubbing» — дубль уже выведенного типа) и лишней пунктуации.
  const studio = (m[2] || '').split(/\s*\/\s*/)[0].replace(/^[\s(:\-–|]+/, '').replace(/[)\s]+$/, '');
  return { type, studio };
}

// Подпись дорожки как перевода: «Студия (тип) ЯЗЫК» — например
// «Red Head Sound (д) RUS», «LostFilm (М) RUS», «Оригинал ENG».
function trackButtonLabel(tr) {
  const { type, studio } = splitAudioTrackTitle(tr.title);
  const lang = (tr.language || '').toUpperCase();
  let label;
  if (type === 'orig') {
    label = t('audioOriginal'); // «Оригинал»
  } else {
    const name = studio || (t('trackFallback') + ' ' + ((tr.ordinal != null ? tr.ordinal : 0) + 1));
    label = type ? name + ' (' + type + ')' : name;
  }
  return lang ? label + ' ' + lang : label;
}

// renderTracks отображает кнопки выбора звуковой дорожки (перевода).
function renderTracks(items, id, magnet) {
  tracksTitle.textContent = t('translationLabel'); // «Перевод»
  tracksList.innerHTML = '';
  const seen = {}; // одинаковые переводы (например TrueHD и его AC3-core) не дублируем
  items.forEach((tr) => {
    const label = trackButtonLabel(tr);
    if (label in seen) return; // тот же перевод (студия/тип/язык) — пропускаем
    seen[label] = true;
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'track-btn' + (tr.ordinal === currentTrack ? ' active' : '');
    btn.textContent = label;
    btn.addEventListener('click', () => {
      document.querySelectorAll('.track-btn').forEach((b) => b.classList.remove('active'));
      btn.classList.add('active');
      // Смена дорожки сохраняет текущую позицию, серию, качество и субтитры.
      // track — порядковый номер аудио (0 = первая), как в ordinal.
      playHls(id, magnet, currentFile, tr.ordinal, streamStart, currentQuality, currentSubs);
    });
    tracksList.appendChild(btn);
  });
}

// renderSubtitles отображает кнопки выбора субтитров: «Выкл» + каждая
// текстовя субтитр-дорожка файла. Выбор ДРУГОЙ дорожки (или «Выкл»)
// перезапускает поток с нужной субтитр-дорожкой (subs) — сервер включает
// её в HLS (WebVTT), а hls.js показывает (subtitleTrack=0 после парсинга).
function renderSubtitles(items, id, magnet) {
  if (!items.length) {
    subsEl.hidden = true;
    return;
  }
  subsEl.hidden = false;
  subsTitle.textContent = t('subtitles');
  subsList.innerHTML = '';
  const mkBtn = (ordinal, label) => {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'sub-btn' + (ordinal === currentSubs ? ' active' : '');
    btn.textContent = label;
    btn.addEventListener('click', () => {
      if (ordinal === currentSubs) return;
      subsSelect(id, magnet, ordinal);
    });
    return btn;
  };
  // «Выкл» — поток без субтитров.
  subsList.appendChild(mkBtn(-1, t('subsOff')));
  items.forEach((tr) => {
    const lang = tr.language ? tr.language.toUpperCase() : '';
    const label = [tr.title, lang].filter(Boolean).join(' · ') || (t('subsFallback') + ' ' + (tr.ordinal + 1));
    subsList.appendChild(mkBtn(tr.ordinal, label));
  });
}

// subsSelect выбирает субтитр-дорожку (ordinal; -1 — выкл): перезапускает
// поток с сохранением позиции/дорожки/качества. hls.js включает дорожку
// в MANIFEST_PARSED (см. playHls) по currentSubs.
function subsSelect(id, magnet, subs) {
  if (!currentPlay || subs === currentSubs) return;
  dbg('субтитры: ' + id + ' -> ' + (subs >= 0 ? subs : 'выкл'));
  currentSubs = subs;
  renderSubtitles(currentSubtitles, id, magnet);
  playHls(id, magnet, currentFile, currentTrack, streamStart, currentQuality, subs);
}

// ---- Серии сериала ----

// loadEpisodes загружает список видеофайлов раздачи (для навигации prev/next
// в плеере). Возвращает индекс файла по умолчанию (первая серия) или -1.
// Отдельного селектора серий больше нет — серии выбираются в блоке
// источников (каноническая сетка), а здесь нужны реальные файлы раздачи.
async function loadEpisodes(id, magnet) {
  episodesEl.hidden = true;
  epSeasons.hidden = true;
  epSeasons.innerHTML = '';
  epList.innerHTML = '';
  const files = await fetchFiles(id, magnet);
  lastFiles = files || [];
  dbg('episodes: файлов=' + lastFiles.length + ' (id=' + id + ')');
  return lastFiles.length ? lastFiles[0].index : -1;
}

// selectEpisode запускает указанную серию (файл) текущего источника
// (кнопки prev/next и автопереход внутри раздачи).
function selectEpisode(index) {
  if (!currentPlay || currentFile === index) return;
  currentFile = index;
  streamStart = 0;
  currentTrack = 0;
  // Субтитры новой серии могут отличаться (другие дорожки/ординлы) —
  // сбрасываем выбор; селектор перерисуется в loadTracks.
  currentSubs = -1;
  // Сбрасываем длительность: /tracks новой серии ещё грузится, а старая
  // длительность могла бы спровоцировать преждевременный автопереход.
  totalDuration = 0;
  // Подсвечиваем выбранную серию/сезон в канонической сетке источников и
  // синхронизируем активный перевод с текущей раздачей.
  const sel = lastFiles.find((f) => f.index === index);
  if (sel && (sel.season || 0) > 0) selectedSeason = sel.season;
  if (sel && (sel.episode || 0) > 0) selectedEpisode = sel.episode;
  if (sourcesEl && !sourcesEl.hidden && lastSourceItems.length) {
    const src = lastSourceItems.find((s) => s.magnet === currentPlay.magnet);
    if (src) activeAudio = src.audio || '';
    renderEpisodeGrid(currentPlay.id);
    renderSeasonVoices(lastSourceItems, currentPlay.id);
  }
  // Если для сезона выбрана озвучка и играет именно её раздача — сохраняем
  // озвучку и в новой серии (loadTracks включит её дорожку).
  const vp = voicePref[selectedSeason];
  autoVoice = (vp && relPref[selectedSeason] === currentPlay.magnet) ? vp : '';
  dbg('серия: файл #' + index);
  loadTracks(currentPlay.id, currentPlay.magnet, currentFile);
  playHls(currentPlay.id, currentPlay.magnet, currentFile, 0, 0, currentQuality);
}

// episodeNeighbor возвращает файл серии, соседний с текущей (dir=1 —
// следующая, dir=-1 — предыдущая) в списке серий выбранного источника.
function episodeNeighbor(dir) {
  if (!Array.isArray(lastFiles) || lastFiles.length === 0) return null;
  const idx = lastFiles.findIndex((f) => f.index === currentFile);
  if (idx < 0) return dir > 0 ? lastFiles[0] : lastFiles[lastFiles.length - 1];
  return lastFiles[idx + dir] || null;
}

// playEpisodeNeighbor запускает следующую/предыдущую серию текущего
// источника. auto=true — автопереход по окончании серии: если торрент
// кончился (например, сезон в отдельной раздаче), пробуем следующую
// раздачу из списка источников.
function playEpisodeNeighbor(dir, auto) {
  if (!currentPlay) return;
  const next = episodeNeighbor(dir);
  if (next) {
    dbg('серия: ' + (dir > 0 ? 'следующая' : 'предыдущая') + ' -> файл #' + next.index);
    selectEpisode(next.index);
    return;
  }
  if (dir > 0) {
    if (auto && playNextSeasonSource()) return;
    if (auto) {
      // Дошли до конца сериала: прерываем сессию просмотра, но карточку
      // фильма не закрываем (требование 4).
      interruptSession();
      flashPlayerNote(t('endOfSeries'));
    } else {
      flashPlayerNote(t('endOfEpisodes'));
    }
  } else {
    flashPlayerNote(t('prevEpisode'));
  }
}

// playNextSeasonSource пробует запустить первую серию следующего сезона из
// списка источников (когда текущая раздача — только часть сезонов, напр.
// сезон в отдельном торренте). Возвращает true, если источник найден.
// Сначала ищет раздачу следующего сезона с текущим переводом (озвучкой),
// затем любую.
function playNextSeasonSource() {
  if (!currentPlay || !Array.isArray(lastSourceItems) || lastSourceItems.length === 0) return false;
  const curEp = currentFile >= 0 ? lastFiles.find((f) => f.index === currentFile) : null;
  const curSeason = selectedSeason || (curEp && curEp.season) || 0;
  // Продолжаем тем же переводом, если он известен (не «без озвучки»).
  const audio = activeAudio;
  let s = null;
  if (audio) s = lastSourceItems.find((it) => it.season > curSeason && (it.audio || '') === audio);
  if (!s) s = lastSourceItems.find((it) => it.season > curSeason);
  if (!s) return false;
  dbg('серия: конец раздачи, следующий сезон ' + s.season + ' -> ' + (s.title || ''));
  selectedSeason = s.season;
  selectedEpisode = null;
  if (s.audio !== undefined) activeAudio = s.audio;
  playSource(currentPlay.id, s);
  return true;
}

// flashPlayerNote показывает короткое сообщение в панели заметки плеера
// (например, «это последняя серия»).
let playerNoteTimer = null;
function flashPlayerNote(text) {
  playerError.textContent = text;
  playerError.hidden = false;
  if (playerNoteTimer) clearTimeout(playerNoteTimer);
  playerNoteTimer = setTimeout(() => { playerError.hidden = true; }, 2400);
}

// showTranslationDialog — озвучка не сделана на все серии: предлагаем выбрать
// из доступных серий этой раздачи либо закрыть просмотр (требование 3).
function showTranslationDialog(id, audio, s, files, season, ep) {
  tlTitle.textContent = audioLabel(audio);
  tlMessage.textContent = t('translationPartial')
    .replace('{audio}', audioLabel(audio))
    .replace('{season}', season || '?')
    .replace('{episode}', ep || '?');
  tlEpisodes.innerHTML = '';
  // Группируем доступные серии по сезону для читаемости.
  const bySeason = {};
  for (const f of files) {
    const k = f.season || 0;
    (bySeason[k] = bySeason[k] || []).push(f);
  }
  Object.keys(bySeason)
    .sort((a, b) => (a === '0' ? 1e9 : +a) - (b === '0' ? 1e9 : +b))
    .forEach((k) => {
      const label = document.createElement('div');
      label.className = 'tl-season';
      label.textContent = k === '0' ? t('seasonFull') : t('seasonLabel') + ' ' + k;
      tlEpisodes.appendChild(label);
      bySeason[k].forEach((f) => {
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'ep-btn' + (f.season === season && f.episode === ep ? ' active' : '');
        btn.textContent = f.episode ? (t('episodeLabel') + ' ' + f.episode) : f.name;
        btn.addEventListener('click', () => {
          closeTranslationDialog();
          selectedSeason = (f.season || 0) > 0 ? f.season : selectedSeason;
          selectedEpisode = f.episode || selectedEpisode;
          lastFiles = files;
          if (sourcesEl && !sourcesEl.hidden) {
            renderEpisodeGrid(id);
            renderTranslationChips(lastSourceItems, id);
          }
          startStream(id, s, f.index);
        });
        tlEpisodes.appendChild(btn);
      });
    });
  tlDialog.hidden = false;
}

function closeTranslationDialog() {
  tlDialog.hidden = true;
  tlEpisodes.innerHTML = '';
}

// interruptSession — прерывает сессию просмотра (останавливает поток и
// плеер), но НЕ закрывает карточку фильма (требование 4): пользователь
// может выбрать другую серию/перевод или закрыть сам.
function interruptSession() {
  // Сохраняем финальную позицию (для «Продолжить просмотр»).
  maybeSaveProgress(true);
  if (hlsPlayer) {
    hlsPlayer.destroy();
    hlsPlayer = null;
  }
  player.pause();
  player.removeAttribute('src');
  player.load();
  playerWrap.hidden = true;
  if (currentPlay) {
    fetch(`/api/films/${encodeURIComponent(currentPlay.id)}/hls/stop`).catch(() => {});
  }
  details.hidden = false;
  if (sourcesEl) sourcesEl.hidden = false;
}

tlClose.addEventListener('click', closeTranslationDialog);
tlCloseViewing.addEventListener('click', () => {
  closeTranslationDialog();
  // «Закрыть просмотр»: прерываем сессию, карточка фильма остаётся.
  interruptSession();
  flashPlayerNote(t('viewingClosed'));
});
tlDialog.addEventListener('click', (e) => {
  if (e.target === tlDialog) closeTranslationDialog();
});

// updateEpisodeButtons обновляет видимость/доступность кнопок переключения
// серий в плеере: показываются для сериала с несколькими сериями в источнике.
function updateEpisodeButtons() {
  const isSeries = isSeriesKind(currentItem && currentItem.kind);
  const multi = isSeries && Array.isArray(lastFiles) && lastFiles.length > 1;
  ctrlPrev.hidden = ctrlNext.hidden = !multi;
  if (!multi) return;
  const idx = lastFiles.findIndex((f) => f.index === currentFile);
  ctrlPrev.disabled = idx <= 0;
  ctrlNext.disabled = idx < 0 || idx >= lastFiles.length - 1;
}

// Автопереход на следующую серию при окончании текущей (для прогрессивного
// HLS «ended» не срабатывает — конец отслеживается в updatePlayerUI, здесь
// обработчик на всякий случай для обычного потока с конечной длительностью).
player.addEventListener('ended', () => {
  if (autoNextFired) return;
  autoNextFired = true;
  playEpisodeNeighbor(1, true);
});

ctrlPrev.addEventListener('click', (e) => {
  e.stopPropagation();
  playEpisodeNeighbor(-1, false);
});

ctrlNext.addEventListener('click', (e) => {
  e.stopPropagation();
  playEpisodeNeighbor(1, false);
});

function hideEpisodes() {
  episodesEl.hidden = true;
  epSeasons.hidden = true;
  epSeasons.innerHTML = '';
  epList.innerHTML = '';
  lastFiles = [];
}

// ---- Кастомные контролы плеера ----

// fmtTime форматирует секунды в "H:MM:SS" / "M:SS".
function fmtTime(s) {
  if (!isFinite(s) || s < 0) s = 0;
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = Math.floor(s % 60);
  const mm = h > 0 ? String(m).padStart(2, '0') : String(m);
  return (h > 0 ? h + ':' : '') + mm + ':' + String(sec).padStart(2, '0');
}

// bufferedEnd — конец буфера (сколько данных уже передано/доступно).
function bufferedEnd() {
  try {
    const b = player.buffered;
    return b.length ? b.end(b.length - 1) : 0;
  } catch (e) {
    return 0;
  }
}

// absTime — абсолютная позиция фильма (с учётом смещения потока streamStart:
// при перемотке ffmpeg стартует с позиции, а player.currentTime считает от 0).
function absTime() {
  return streamStart + (player.currentTime || 0);
}

// absBufEnd — абсолютный конец переданных данных (полоса «переданного»).
function absBufEnd() {
  return streamStart + bufferedEnd();
}

// updatePlayerUI обновляет полосу прогресса: полная длительность всегда
// видна, переданная часть — отдельной полоской (ctrl-buffered).
function updatePlayerUI() {
  const dur = totalDuration || streamStart + (player.duration || 0);
  const pos = absTime();
  const bufEnd = absBufEnd();
  const pctPos = dur ? Math.min(100, Math.max(0, (pos / dur) * 100)) : 0;
  const pctBuf = dur ? Math.min(100, Math.max(0, (bufEnd / dur) * 100)) : 0;
  ctrlPosition.style.width = pctPos + '%';
  ctrlBuffered.style.width = pctBuf + '%';
  ctrlThumb.style.left = pctPos + '%';
  ctrlMiniFill.style.width = pctPos + '%';
  ctrlTime.textContent = fmtTime(pos) + ' / ' + fmtTime(dur);
  ctrlPlay.textContent = player.paused ? '▶' : '⏸';
  ctrlVolume.value = player.volume;
  ctrlMute.textContent = player.muted || player.volume === 0 ? '🔇' : '🔊';
  updateEpisodeButtons();
  // Автопереход на следующую серию: когда доиграли до конца (HLS-поток
  // прогрессивный и события ended нет — отслеживаем по позиции).
  if (!player.paused && totalDuration > 0 && pos >= totalDuration - 1.5 && !autoNextFired) {
    autoNextFired = true;
    playEpisodeNeighbor(1, true);
  }
  // Автосохранение позиции просмотра в историю (не чаще раза в 5 сек).
  maybeSaveProgress(false);
}

// Клик по полосе — перемотка в любое место полной длительности.
// Обычный seek (внутри текущего потока) возможен только на его временной
// шкале: от streamStart (нулевая точка ffmpeg) до конца переданных данных.
// Назад, раньше streamStart (например, при продолжении просмотра с середины),
// или вперёд за переданную границу — сервер перезапускает ffmpeg с позиции.
ctrlBar.addEventListener('click', (e) => {
  const rect = ctrlBar.getBoundingClientRect();
  const frac = (e.clientX - rect.left) / rect.width;
  const dur = totalDuration || streamStart + (player.duration || 0);
  const target = Math.max(0, Math.min(dur, frac * dur));
  if (target >= streamStart && target <= absBufEnd() + 5) {
    player.currentTime = target - streamStart;
  } else if (isFinite(target) && currentPlay) {
    seekTo(target);
  }
  updatePlayerUI();
});

// seekTo перезапускает HLS-поток с указанной позиции (перемотка сразу
// в середину/конец, даже если ffmpeg ещё не дошёл до неё).
function seekTo(target) {
  // Защита от некорректной позиции (Infinity/NaN — если длительность ещё
  // не получена, dur=Infinity и target тоже); иначе ffmpeg получит -ss +Inf.
  if (!currentPlay || !isFinite(target) || target < 0) return;
  dbg('seek: перезапуск потока с ' + target + 's');
  streamStart = target;
  playHls(currentPlay.id, currentPlay.magnet, currentFile, currentTrack, target, currentQuality, currentSubs);
}

// seekBy — перемотка на delta секунд от текущей позиции (вперёд/назад на 5с):
// внутри уже переданных данных двигаем currentTime, иначе перезапускаем ffmpeg.
function seekBy(delta) {
  if (!currentPlay || playerWrap.hidden) return;
  const dur = totalDuration || streamStart + (player.duration || 0);
  const target = Math.max(0, isFinite(dur) ? Math.min(dur, absTime() + delta) : absTime() + delta);
  if (target >= streamStart && target <= absBufEnd() + 5) {
    player.currentTime = target - streamStart;
  } else if (isFinite(target)) {
    seekTo(target);
  }
  updatePlayerUI();
}

if (ctrlSeekB) ctrlSeekB.addEventListener('click', () => seekBy(-5));
if (ctrlSeekF) ctrlSeekF.addEventListener('click', () => seekBy(5));

ctrlPlay.addEventListener('click', (e) => {
  e.stopPropagation();
  if (player.paused) player.play().catch(() => {}); else player.pause();
});

// toggleFullscreen разворачивает/сворачивает плеер в полноэкранный режим.
function toggleFullscreen() {
  if (document.fullscreenElement) {
    document.exitFullscreen().catch(() => {});
  } else if (playerWrap.requestFullscreen) {
    playerWrap.requestFullscreen().catch(() => {});
  }
}

// Клик по видео — play/pause; двойной клик — развернуть/свернуть плеер.
// Чтобы двойной клик не переключал play/pause дважды, клики дебаунсим:
// второй клик в течение 300 мс считается двойным и только разворачивает.
let lastClickAt = 0;
let clickTimer = null;
player.addEventListener('click', () => {
  const now = Date.now();
  if (now - lastClickAt < 300) {
    lastClickAt = 0;
    if (clickTimer) { clearTimeout(clickTimer); clickTimer = null; }
    toggleFullscreen();
    return;
  }
  lastClickAt = now;
  if (clickTimer) clearTimeout(clickTimer);
  clickTimer = setTimeout(() => {
    clickTimer = null;
    lastClickAt = 0;
    if (player.paused) player.play().catch(() => {}); else player.pause();
    if (playerWrap.classList.contains('fullscreen')) showControls();
  }, 300);
});

ctrlMute.addEventListener('click', (e) => {
  e.stopPropagation();
  player.muted = !player.muted;
  updatePlayerUI();
});

ctrlVolume.addEventListener('input', () => {
  player.volume = parseFloat(ctrlVolume.value);
  player.muted = player.volume === 0;
  updatePlayerUI();
});

// qualityHeightNum возвращает высоту кадра для кнопки качества (0 — source).
function qualityHeightNum(q) {
  return { '2160': 2160, '1080': 1080, '720': 720, '480': 480 }[q] || 0;
}

// updateQualityButtons отражает выбранное качество на кнопках селектора и
// скрывает недоступные: качество нельзя повысить выше исходного разрешения
// (например, для 1080p-источника кнопка 4K не показывается). Пока высота
// источника неизвестна (0), показываем все кнопки.
function updateQualityButtons() {
  document.querySelectorAll('#ctrl-quality .q-btn').forEach((b) => {
    const q = b.dataset.quality;
    const available = q === 'source' || currentVideoHeight <= 0 || qualityHeightNum(q) <= currentVideoHeight;
    b.hidden = !available;
    b.classList.toggle('active', available && q === currentQuality);
  });
}

// Кнопки качества: серверное понижение — перезапуск HLS с качеством quality
// (ffmpeg перекодирует; позиция и дорожка сохраняются).
document.querySelectorAll('#ctrl-quality .q-btn').forEach((b) => {
  b.addEventListener('click', () => {
    if (!currentPlay) return;
    const q = b.dataset.quality;
    if (q === currentQuality) return;
    dbg('качество: ' + currentQuality + ' -> ' + q);
    currentQuality = q;
    updateQualityButtons();
    playHls(currentPlay.id, currentPlay.magnet, currentFile, currentTrack, streamStart, q, currentSubs);
  });
});

ctrlFullscreen.addEventListener('click', (e) => {
  e.stopPropagation();
  toggleFullscreen();
});

// --- Полноэкранный режим: всплывающая панель управления ---
// При разворачивании панель .player-controls плавает поверх видео снизу и
// скрывается через пару секунд бездействия мыши; тонкая полоса #ctrl-mini
// с прогрессом видна всегда. Появляется панель по движению мыши/клику.
let controlsTimer = null;

function showControls() {
  playerWrap.classList.add('controls-visible');
  playerWrap.classList.remove('controls-hidden');
  if (controlsTimer) clearTimeout(controlsTimer);
  controlsTimer = setTimeout(hideControls, 2500);
}

function hideControls() {
  playerWrap.classList.remove('controls-visible');
  playerWrap.classList.add('controls-hidden');
}

playerWrap.addEventListener('mousemove', () => {
  if (playerWrap.classList.contains('fullscreen')) showControls();
});
playerWrap.addEventListener('mouseleave', () => {
  if (playerWrap.classList.contains('fullscreen')) hideControls();
});
playerWrap.addEventListener('fullscreenchange', () => {
  const fs = !!document.fullscreenElement;
  playerWrap.classList.toggle('fullscreen', fs);
  if (fs) {
    showControls();
  } else {
    if (controlsTimer) clearTimeout(controlsTimer);
    playerWrap.classList.remove('controls-visible', 'controls-hidden');
  }
  updatePlayerUI();
});

ctrlDebug.addEventListener('click', (e) => {
  e.stopPropagation();
  playerDebug.hidden = !playerDebug.hidden;
  if (!playerDebug.hidden) renderDebug();
});

player.addEventListener('timeupdate', updatePlayerUI);
player.addEventListener('progress', updatePlayerUI);
player.addEventListener('play', updatePlayerUI);
player.addEventListener('pause', updatePlayerUI);
player.addEventListener('volumechange', updatePlayerUI);
player.addEventListener('durationchange', updatePlayerUI);

async function closePlayer() {
  // Сохраняем финальную позицию просмотра, затем обновляем «Продолжить».
  const saveP = maybeSaveProgress(true);
  if (saveP) saveP.then(() => loadHistory()).catch(() => {});
  else loadHistory();
  modal.hidden = true;
  // Останавливаем ffmpeg/стрим, чтобы освободить память.
  if (currentPlay) {
    fetch(`/api/films/${encodeURIComponent(currentPlay.id)}/hls/stop`).catch(() => {});
  }
  if (hlsPlayer) {
    hlsPlayer.destroy();
    hlsPlayer = null;
  }
  player.pause();
  player.removeAttribute('src');
  player.load();
  playerWrap.hidden = true;
  totalDuration = 0;
  // Сбрасываем состояние запроса длительности — следующий фильм запросит её
  // заново при старте потока.
  durationFetch = { key: '', inflight: false };
  streamStart = 0;
  currentTrack = 0;
  currentSubs = -1;
  currentSubtitles = [];
  currentQuality = 'source';
  currentFile = -1;
  currentVideoCodec = '';
  currentVideoHeight = 0;
  autoNextFired = false;
  updateQualityButtons();
  updatePlayerUI();
  details.hidden = true;
  sourcesEl.hidden = true;
  sourcesEmpty.hidden = true;
  sourcesSeasonWrap.hidden = true;
  sourcesSeason.innerHTML = '';
  sourcesEpisodesWrap.hidden = true;
  sourcesEpisodes.innerHTML = '';
  sourcesAudioWrap.hidden = true;
  sourcesAudio.innerHTML = '';
  tracksEl.hidden = true;
  tracksList.innerHTML = '';
  subsEl.hidden = true;
  subsList.innerHTML = '';
  hideEpisodes();
  closeTranslationDialog();
  playerError.hidden = true;
  playerDebug.hidden = true;
  lastSourceItems = [];
  lastSourceId = null;
  activeAudio = null;
  selectedSeason = null;
  selectedEpisode = null;
  pendingResume = null;
  currentPlay = null;
  currentItem = null;
}

function escapeHtml(s) {
  const div = document.createElement('div');
  div.textContent = s;
  return div.innerHTML;
}

// ---- Разделы и жанры ----

// Обновляет метаданные каталога с учётом активных поиска и жанра
// (счётчики разделов пересчитываются).
async function refreshMeta() {
  const params = new URLSearchParams();
  const q = search.value.trim();
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
  // Сортировка скрыта в подборках «Лучшие…»/«Популярные…» — там порядок чарта.
  sortEl.hidden = currentCollection !== '';
  // Подборки есть только у категорий «Фильмы»/«Сериалы».
  if (currentSection !== 'movie' && currentSection !== 'series') currentCollection = '';
  // «Все» — сумма по секциям (без псевдо-секции popular: её записи уже
  // входят в категории — иначе был бы двойной учёт).
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

  // «Все» — отдельной кнопкой в начале.
  const allBtn = mkButton('all', t('allSections'), total, currentSection === 'all');
  allBtn.addEventListener('click', () => select('all', ''));
  sectionsEl.appendChild(allBtn);

  for (const s of SECTIONS) {
    const c = kinds[s.key] || 0;
    if (c <= 0) continue;
    const label = lang === 'ru' ? s.ru : s.en;
    const active = currentSection === s.key;
    const defs = COLLECTIONS[s.key];
    // Категория без подборок — простая кнопка.
    if (!defs || !defs.length) {
      const b = mkButton(s.key, label, c, active);
      b.addEventListener('click', () => select(s.key, ''));
      sectionsEl.appendChild(b);
      continue;
    }
    // Категория с подборками (Фильмы/Сериалы): кнопка + hover-меню.
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

// populateSort наполняет выпадающий список сортировки (с переводом).
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
search.addEventListener('input', () => {
  clearTimeout(searchTimer);
  const q = search.value.trim();
  if (!q) {
    fetchPage(1);
    refreshMeta();
    return;
  }
  // При начале поиска возвращаемся на вкладку «Все» — ищем по всему
  // каталогу, а вкладки позволяют отфильтровать результат по разделу.
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
  // Показываем «Идёт поиск...» сразу — сервер /api/catalog ищет и в БД,
  // и в IMDb/TMDB (on-demand), это может занять пару секунд.
  grid.innerHTML = '';
  sentinel.hidden = true;
  empty.hidden = false;
  empty.textContent = t('searching');

  await fetchPage(1);
  if (!isCurrentQuery(q)) return;
  if (items.length === 0) {
    empty.textContent = t('notFoundImdb');
  }
}

// isCurrentQuery проверяет, что в поле поиска всё ещё тот же запрос.
function isCurrentQuery(q) {
  return search.value.trim().toLowerCase() === q.toLowerCase();
}

// Постраничная пагинация заменена бесконечной прокруткой: initInfiniteScroll
// и loadMore объявлены в блоке «Каталог» (IntersectionObserver на #sentinel
// догружает следующие страницы при прокрутке вниз).

modalClose.addEventListener('click', closePlayer);
modal.addEventListener('click', (e) => {
  if (e.target === modal) closePlayer();
});
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') closePlayer();
});
// Клавиши ←/→ — перемотка на 5 секунд (когда открыт плеер и курсор не в поле).
document.addEventListener('keydown', (e) => {
  if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
  const tag = ((e.target && e.target.tagName) || '').toLowerCase();
  if (tag === 'input' || tag === 'textarea' || tag === 'select') return;
  if (modal.hidden || playerWrap.hidden || !currentPlay) return;
  e.preventDefault();
  seekBy(e.key === 'ArrowLeft' ? -5 : 5);
});
if (watchBtn) watchBtn.addEventListener('click', watchNow);

applyLang();
refreshMeta();
fetchPage(1);
initInfiniteScroll();
initAuth(); // определяем пользователя по куке и грузим историю просмотра

if (DEBUG) showDebug();
