'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const series = fs.readFileSync(path.join(__dirname, '../web/series.js'), 'utf8');
const start = series.indexOf('function titleVoices(');
const end = series.indexOf('// Раздачи, покрывающие сезон', start);
const context = vm.createContext({});
vm.runInContext(series.slice(start, end), context);
const voices = title => Array.from(context.titleVoices(title));

// Public release titles from the 26-source Spider-Man: Brand New Day response.
// Keep only names: no magnets, tracker addresses, credentials or session data.
const spiderManTitles = [
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) UHD WEB-DL 2160p | 4K | HDR10+ | Dolby Vision P8 | D | MovieDalen-IMAX',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) WEB-DL 1080p | D, Р',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) WEBRip-HEVC 1080p | D, Р',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) WEB-DL 720p от New-Team | D, P | MovieDalen, HDRezka Studio',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) TS [H.264/1080p] [MVO]',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) WEB-DL-AVC | P | IMAX-LostFilm',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) TS [H.264/1080p] [DVO] [AD]',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) WEB-DL [H.265/2160p] [4K, SDR, 10-bit]',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) WEB-DL [H.265/2160p] [4K, HDR10, DV 8, 10-bit]',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) WEB-DL [H.265/2160p] [4K, HDR10+, DV 8.1, 10-bit]',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) UHD WEB-DL 2160p | 4K | SDR | D-MovieDalen',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) WEB-DL [H.265/2160p] [4K, HDR10, 10-bit] [IMAX]',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) WEB-DL [H.265/2160p] [4K, SDR, 10-bit]',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) UHD WEB-DL [H.264/1080p] [IMAX]',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) WEB-Rip 1080p от New-Team | D-IMAX',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) WEB-DL 1080p | D, Р, Р2',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) WEB-DL 1080p-MovieDalen',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (Дестин Дэниел Креттон) [2026, Фантастика, боевик, приключения, WEB-DL 1080p | MovieDalen]',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) WEB-DL 720p от New-Team | D, P-IMAX',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) WEB-DL 720p-MovieDalen',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) TSRip [H.264] [AD]',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) WEB-DL-AVC от New-Team | D, P-IMAX',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) WEB-DL-AVC от ExKinoRay | D-IMAX',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) WEB-DL-MovieDalen',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) WEB-DL-AVC | КПК | D-IMAX',
  'Человек-паук: Новый день / Spider-Man: Brand New Day (2026) WEB-DL-AVC-MovieDalen',
];

test('all 26 real release titles expose three named studios including MovieDalen', () => {
  assert.equal(spiderManTitles.length, 26);
  assert.deepEqual([...new Set(spiderManTitles.flatMap(voices))].sort(), ['HDRezka Studio', 'LostFilm', 'MovieDalen']);
  assert.equal(spiderManTitles.filter(title => voices(title).includes('MovieDalen')).length, 8);
  assert.equal(spiderManTitles.filter(title => voices(title).includes('HDRezka Studio')).length, 1);
  assert.equal(spiderManTitles.filter(title => voices(title).includes('LostFilm')).length, 1);
});

test('MovieDalen title aliases select the corresponding audio track', () => {
  for (const alias of ['MovieDalen', 'MOVIEDALEN', 'Movie Dalen', 'Movie-Dalen', 'Movie_Dalen', 'Movie.Dalen']) {
    assert.deepEqual(voices('Фильм (2026) WEB-DL 1080p | D-' + alias + '-IMAX'), ['MovieDalen']);
    const tracks = [
      { ordinal: 0, title: 'HDRezka Studio', language: 'rus' },
      { ordinal: 2, title: 'Дубляж ' + alias, language: 'rus' },
      { ordinal: 3, title: 'Original', language: 'eng' },
    ];
    assert.equal(context.matchVoiceOrdinal(tracks, 'MovieDalen'), 2, alias);
  }
});

test('HDR video metadata never creates an HDRezka studio choice', () => {
  for (const quality of ['HDR', 'hdr', 'HDR10', 'HDR10+', 'HDRip', '4K, HDR, 10-bit', 'Dolby Vision | HDR']) {
    assert.deepEqual(voices('Фильм (2026) WEB-DL 2160p | ' + quality + ' | D'), [], quality);
  }
  assert.deepEqual(voices('Фильм (2026) WEB-DL 2160p | HDR | MovieDalen'), ['MovieDalen']);
});

test('legacy HDr and explicit HDRezka studio names retain their labels and track selection', () => {
  assert.deepEqual(voices('Фильм (2026) WEB-DL 1080p | HDr'), ['HDRezka']);
  assert.deepEqual(voices('Фильм (2026) WEB-DL 2160p | HDR | HDRezka Studio'), ['HDRezka Studio']);
  assert.equal(context.matchVoiceOrdinal([{ ordinal: 1, title: 'HDr', language: 'rus' }], 'HDRezka'), 1);
});

test('RHS and Продубляж labels share their track aliases without matching substrings', () => {
  assert.deepEqual(voices('Фильм (2026) 1080p | RHS | Продубляж'), ['Red Head Sound', 'Продубляж']);
  assert.equal(context.matchVoiceOrdinal([{ ordinal: 2, title: 'RHS', language: 'rus' }], 'Red Head Sound'), 2);
  assert.equal(context.matchVoiceOrdinal([{ ordinal: 3, title: 'D | Продубляж', language: 'rus' }], 'Продубляж'), 3);
  for (const title of ['Фильм RHSound', 'Фильм AnotherMovieDalen', 'Фильм Продубляжный', 'Red Head (2026)', 'Фильм ЧужойRHSфильм']) {
    assert.deepEqual(voices(title), [], title);
  }
});

test('translation types do not masquerade as named studios', () => {
  for (const type of ['D, P, A', 'Д, Р, А', 'DUB', 'MVO', 'DVO', 'AVO', 'Дубляж', 'Профессиональный многоголосый']) {
    assert.deepEqual(voices('Фильм (2026) 1080p | ' + type), [], type);
  }
});

test('known studio and original labels remain deduplicated', () => {
  assert.deepEqual(voices('LostFilm LF NewStudio TVShows TVS HDRezka Studio BaibaKo Jaskier Red Head Sound RHS ENG'),
    ['LostFilm', 'NewStudio', 'TVShows', 'HDRezka Studio', 'BaibaKo', 'Jaskier', 'Red Head Sound', 'Оригинал']);
  assert.deepEqual(voices('Novamedia'), ['НоваМедиа']);
  assert.equal(context.matchVoiceOrdinal([{ ordinal: 4, language: 'eng', title: 'English' }], 'Оригинал'), 4);
});
