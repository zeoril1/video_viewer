'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const read = name => fs.readFileSync(path.join(__dirname, '../web', name), 'utf8');
const series = read('series.js'), film = read('film.js'), player = read('player.js');
const helpers = series.slice(series.indexOf('function sourceRichness('));

// Confirmed audio names from release 1109187, absent from its RSS title.
// HDRezka Studio supplies two translation types; the studio choice is deduplicated.
const described = {
  magnet: 'described-source', season: 2, seeds: 10, quality: '1080',
  title: 'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) WEB-DL 1080p | D, Р, Р2',
  voices: ['MovieDalen', 'HDRezka Studio', 'TVShows', 'HDRezka Studio', 'ViruseProject'],
};
const other = { magnet: 'title-source', season: 2, seeds: 100, quality: '2160', title: 'Фильм (2026) 2160p | MovieDalen' };
const expected = ['MovieDalen', 'HDRezka Studio', 'TVShows', 'ViruseProject'];

function setup(values = {}) {
  const context = vm.createContext({ relPref: {}, seasonFitRank: () => 1,
    srcHeight: source => Number(source.quality) || 0, ...values });
  vm.runInContext(helpers, context);
  return context;
}

test('tracker details expose every named studio without inventing voices from D/P tags', () => {
  const c = setup();
  assert.deepEqual(Array.from(c.titleVoices(described.title)), []);
  assert.deepEqual(Array.from(c.sourceVoices(described)), expected);
  assert.deepEqual(Array.from(c.sourceVoices({ title: described.title })), []);
  assert.ok(c.sourceRichness(described) > c.sourceRichness(other));
});

test('metadata names normalize known aliases and retain unfamiliar explicit studio names', () => {
  const c = setup();
  const source = { title: 'Фильм HDRezka', voices: ['HDRezkaStudio', 'TVS', 'tvs', 'RHS', 'Viruse Project',
    'movie-dalen', 'Студия с трекера', 'студия  с трекера'] };
  assert.deepEqual(Array.from(c.sourceVoices(source)),
    ['HDRezka Studio', 'TVShows', 'Red Head Sound', 'ViruseProject', 'MovieDalen', 'Студия с трекера']);
  for (const voice of ['tvs', 'Viruse-Project', 'СТУДИЯ С ТРЕКЕРА']) assert.equal(c.sourceHasVoice(source, voice), true);
  assert.equal(c.sourceHasVoice(source, 'LostFilm'), false);
  assert.deepEqual(Array.from(c.sourceVoices({ voices: ['D', 'Р', 'Р2', 'D, P, A', 'MVO', 'Дубляж', 'HDR',
    '<b>TVShows</b>', 'https://tracker.invalid/topic', 'bad\nname', null, 12] })), []);
});

test('season voices and source selection include studios named only in details', () => {
  const c = setup();
  const outsideSeason = { ...other, magnet: 'other-season', season: 3, voices: ['LostFilm'] };
  const items = [other, described, outsideSeason];
  assert.deepEqual(Array.from(c.seasonVoices(items, 2)), ['MovieDalen', 'HDRezka Studio', 'TVShows', 'ViruseProject', 'Оригинал']);
  assert.equal(c.pickVoiceSource(items, 2, 'ViruseProject').magnet, described.magnet);
  assert.equal(c.pickVoiceSource(items, 2, 'TVS').magnet, described.magnet);
});

test('personal ranking prefers metadata-only voices over higher quality unrelated sources', () => {
  const c = setup({ document: { querySelector() { return null; } } });
  vm.runInContext(read('personal.js') + '\nthis.personal=Personal;', c);
  assert.equal(c.personal.rank([other, described], 'ViruseProject')[0].magnet, described.magnet);
  assert.equal(c.personal.rank([other, described], 'TVS')[0].magnet, described.magnet);
});

function movieFixture() {
  let c;
  function element() {
    return { children: [], dataset: {}, handlers: {}, attributes: {},
      append(child) { this.children.push(child); },
      replaceChildren() { this.children = []; },
      contains(child) { return this.children.includes(child); },
      setAttribute(name, value) { this.attributes[name] = value; },
      addEventListener(name, fn) { this.handlers[name] = fn; },
      click() { this.handlers.click?.(); },
      focus() { c.document.activeElement = this; },
    };
  }
  const ids = {}, opened = [];
  c = setup({ document: { activeElement: null, createElement: element, getElementById: id => ids[id] ||= element() },
    sourcesRel: element(), sourcesRelWrap: element(), sourcesMovieWrap: element(), relNote: element(),
    movieSourceSelect: element(), movieSourcePlay: element(),
    selectedMovieVoice: '', selectedMovieMagnet: '', movieSelectionReady: false, movieSelectionExplicit: false,
    wantVoice: '', startMagnet: '', lastSourceItems: [other, described], lastSourceId: 'tt22084616',
    VV: { user: null }, historyEntry: () => null, filmText: ru => ru, fmtTime: String,
    showNote() {}, openWatch: (id, source, file, season, episode, voice) => opened.push({ source, voice }),
  });
  c.sourcesMovieWrap.previousElementSibling = c.sourcesRelWrap;
  vm.runInContext(read('personal.js') + '\nthis.Personal=Personal;', c);
  const source = film.slice(film.indexOf('function renderMovieSources('), film.indexOf('// Scroll only the chip row'));
  vm.runInContext(source, c);
  return { c, opened };
}

test('all four movie chips are visible and select a source that actually declares that voice', () => {
  const { c, opened } = movieFixture();
  c.renderMovieSources(c.lastSourceItems, c.lastSourceId);
  assert.deepEqual(c.sourcesRel.children.map(button => button.textContent), ['Авто', ...expected]);
  for (const voice of expected.slice(1)) {
    c.sourcesRel.children.find(button => button.textContent === voice).click();
    assert.equal(c.selectedMovieVoice, voice);
    assert.equal(c.movieSourceSelect.value, described.magnet);
    assert.deepEqual(c.movieSourceSelect.children.map(option => option.value), [described.magnet]);
    c.movieSourcePlay.click();
    assert.equal(opened.at(-1).source.magnet, described.magnet);
    assert.equal(opened.at(-1).voice, voice);
  }
});

test('episode playback probes the metadata voice source before unrelated alternatives', async () => {
  const requested = [], opened = [];
  const c = setup({ lastSourceItems: [other, described], voicePref: { 2: 'ViruseProject' },
    playToken: 0, episodeProbeLimit: 6, seasonEpisodeCount: () => 1, tmdbMappingOn: () => false,
    probeOrder: items => items, releaseSeasonFit: () => ({ fits: true, count: 1 }),
    fetchFiles: async (id, magnet) => { requested.push(magnet); return [{ index: 0, season: 2, episode: 1 }]; },
    dbg() {}, flashFilmNote() {}, t: key => key,
    renderSeasonChips() {}, renderEpisodeGrid() {}, renderSeasonVoices() {}, PP: { available: false },
    openWatch: (id, source, file, season, episode, voice) => opened.push({ source, voice }),
  });
  vm.runInContext(read('personal.js') + '\nthis.Personal=Personal;', c);
  vm.runInContext(film.slice(film.indexOf('async function playEpisode('), film.indexOf('\nfunction openWatch(')), c);
  assert.equal(await c.playEpisode('tt22084616', 2, 1), true);
  assert.deepEqual(requested, [described.magnet]);
  assert.equal(opened[0].voice, 'ViruseProject');
});

function tracksFixture(voice, tracks) {
  const c = setup({ AbortController, URLSearchParams, setTimeout, clearTimeout, CustomEvent: function () {},
    tracksGeneration: 0, durationFetch: {}, lastTracksItems: [], currentSubtitles: [],
    tracksEl: {}, tracksList: {}, tracksCurrent: {}, tracksTitle: {}, subsEl: {}, subsList: {},
    currentPlay: { id: 'tt22084616', magnet: described.magnet }, currentFile: 0,
    currentTrack: 0, selectedAudio: null, autoVoice: voice, selectedVoice: voice,
    pendingPlayback: {}, totalDuration: 0, currentVideoCodec: '',
    t: key => key, dbg() {}, updatePlayerUI() {}, renderSubtitles() {}, renderTracks() {},
    trackButtonLabel: track => track.title || String(track.ordinal), window: { dispatchEvent() {} },
    fetch: async () => ({ ok: true, json: async () => ({ items: tracks, duration: 7200, codec: 'h264' }) }),
  });
  vm.runInContext(player.slice(player.indexOf('  function audioOrdinal('), player.indexOf('  // Тип перевода')), c);
  return c;
}

test('metadata-only ViruseProject selects its named track, independent of metadata order', async () => {
  const c = tracksFixture('ViruseProject', [
    { ordinal: 0, language: 'rus', title: 'MovieDalen' },
    { ordinal: 7, language: 'rus', title: 'MVO | Viruse Project' },
    { ordinal: 2, language: 'eng', title: 'Original' },
  ]);
  assert.equal(await c.loadTracks('tt22084616', described.magnet, 0), true);
  assert.equal(c.currentTrack, 7);
  assert.equal(c.selectedVoice, 'ViruseProject');
});

test('RHS release 1115875 adds the eighth studio and its chip resolves the declared source and named track', async () => {
  for (const seeds of [0, 1]) {
    const { c, opened } = movieFixture();
    const previousSeven = { ...described, voices: [...expected, 'LostFilm', 'Смотри кино', 'Dragon Money Studio'] };
    // Inert source/file identifiers; only the confirmed studio names and track
    // label are needed to cover release 1115875 without contacting any media.
    const rhsSource = { magnet: 'release-1115875', quality: '1080', seeds,
      title: 'Человек-паук: Новый день (2026) BDRip 1080p от MegaPeer',
      voices: ['MovieDalen', 'RHS', 'HDRezka Studio'] };
    c.lastSourceItems = [previousSeven, rhsSource];
    c.renderMovieSources(c.lastSourceItems, c.lastSourceId);
    assert.deepEqual(c.sourcesRel.children.slice(1).map(button => button.textContent).sort(),
      [...expected, 'LostFilm', 'Смотри кино', 'Dragon Money Studio', 'Red Head Sound'].sort());
    c.sourcesRel.children.find(button => button.textContent === 'Red Head Sound').click();
    assert.equal(c.selectedMovieVoice, 'Red Head Sound');
    assert.equal(c.movieSourceSelect.value, rhsSource.magnet);
    assert.deepEqual(c.movieSourceSelect.children.map(option => option.value), [rhsSource.magnet]);
    c.movieSourcePlay.click();
    assert.equal(opened.at(-1).source.magnet, rhsSource.magnet);
    assert.equal(opened.at(-1).voice, 'Red Head Sound');

    const tracks = tracksFixture(opened.at(-1).voice, [
      { ordinal: 0, language: 'rus', title: 'DUB | MovieDalen' },
      { ordinal: 1, language: 'rus', title: 'MVO | HDRezka Studio' },
      { ordinal: 6, language: 'rus', title: 'DUB | неофициальный | Red Head Sound' },
    ]);
    tracks.currentPlay.magnet = rhsSource.magnet;
    assert.equal(await tracks.loadTracks(c.lastSourceId, rhsSource.magnet, 0), true);
    assert.equal(tracks.currentTrack, 6, 'track ordinal comes from the named track, not the voices array');
    assert.equal(tracks.selectedVoice, 'Red Head Sound');
  }
});

test('public NNM descriptions add Смотри кино and Dragon Money Studio without requiring studio names in RSS titles', () => {
  const { c } = movieFixture();
  const nnmMvo = { magnet: 'nnm-mvo', title: 'Человек-паук: Новый день (2026) TS [H.264/1080p] [MVO]', voices: ['Смотри кино'] };
  const nnmTsrip = { magnet: 'nnm-tsrip', title: 'Человек-паук: Новый день (2026) TSRip [H.264] [AD]', voices: ['Dragon Money Studio'] };
  const lostFilm = { magnet: 'lostfilm', title: 'Человек-паук: Новый день (2026) WEB-DL-AVC | P | IMAX-LostFilm' };
  c.lastSourceItems = [described, lostFilm, nnmMvo, nnmTsrip];
  c.renderMovieSources(c.lastSourceItems, c.lastSourceId);
  assert.deepEqual(c.sourcesRel.children.slice(1).map(button => button.textContent).sort(),
    [...expected, 'LostFilm', 'Смотри кино', 'Dragon Money Studio'].sort());
  for (const source of [nnmMvo, nnmTsrip]) {
    c.sourcesRel.children.find(button => button.textContent === source.voices[0]).click();
    assert.equal(c.movieSourceSelect.value, source.magnet);
  }
});

test('confirmed NNM studios match spaced and compact names in decorated audio titles', () => {
  const c = setup();
  for (const alias of ['Смотри кино', 'СмотриКино']) {
    assert.deepEqual(Array.from(c.titleVoices('Перевод: Многоголосый (' + alias + ')')), ['Смотри кино']);
    assert.equal(c.matchVoiceOrdinal([{ ordinal: 4, title: 'MVO | ' + alias, language: 'rus' }], 'Смотри кино'), 4);
  }
  for (const alias of ['Dragon Money Studio', 'DragonMoneyStudio', 'Dragon Money']) {
    assert.deepEqual(Array.from(c.titleVoices('Перевод: Дублированный (' + alias + ')')), ['Dragon Money Studio']);
    assert.equal(c.matchVoiceOrdinal([{ ordinal: 6, title: 'Русский (DUB - ' + alias + ')', language: 'rus' }], 'Dragon Money Studio'), 6);
  }
  for (const title of ['Film MyDragonMoneyStudio', 'Film Dragon Moneyland', 'Фильм Пресмотрикино']) {
    assert.deepEqual(Array.from(c.titleVoices(title)), [], title);
  }
});

test('confirmed unfamiliar voice retains its requested studio name instead of a decorated track title', async () => {
  const c = tracksFixture('Студия с трекера', [
    { ordinal: 0, language: 'rus', title: 'MovieDalen' },
    { ordinal: 5, language: 'rus', title: 'MVO | Студия с трекера' },
  ]);
  await c.loadTracks('tt22084616', described.magnet, 0);
  assert.equal(c.currentTrack, 5);
  assert.equal(c.selectedVoice, 'Студия с трекера');
});

test('missing preferred audio reports the actual track and never invents an ordinal', async () => {
  const c = tracksFixture('ViruseProject', [
    { ordinal: 4, language: 'rus', title: 'HDRezka Studio' },
    { ordinal: 2, language: 'eng', title: 'Original' },
  ]);
  await c.loadTracks('tt22084616', described.magnet, 0);
  assert.equal(c.currentTrack, 4);
  assert.equal(c.selectedVoice, 'HDRezka Studio');
});
