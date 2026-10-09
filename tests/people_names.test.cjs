// people_names.test.cjs — имена режиссёра/актёров показываются на языке сайта: русские
// написания приходят с сервера отдельными полями director_ru/actors_ru (таблица person_names),
// а people_pending говорит клиенту, что перевод ещё добирается из TMDB.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const shared = fs.readFileSync(path.join(__dirname, '../web/shared.js'), 'utf8');
const source = shared.slice(shared.indexOf('function dispDirector'), shared.indexOf('function fmtRating'));

function setup(lang) {
  const ctx = vm.createContext({ lang });
  vm.runInContext(source, ctx);
  return ctx;
}

const translated = {
  director: 'Paul Greengrass',
  actors: ['Andrew Garfield', 'Jamie Bell'],
  director_ru: 'Пол Гринграсс',
  actors_ru: ['Эндрю Гарфилд', 'Джейми Белл'],
};

test('русский интерфейс показывает переведённые имена', () => {
  const ctx = setup('ru');
  assert.equal(ctx.dispDirector(translated), 'Пол Гринграсс');
  assert.deepEqual([...ctx.dispActors(translated)], ['Эндрю Гарфилд', 'Джейми Белл']);
});

test('английский интерфейс показывает исходные имена', () => {
  const ctx = setup('en');
  assert.equal(ctx.dispDirector(translated), 'Paul Greengrass');
  assert.deepEqual([...ctx.dispActors(translated)], ['Andrew Garfield', 'Jamie Bell']);
});

test('без перевода остаётся исходное написание', () => {
  const ctx = setup('ru');
  const it = { director: 'Paul Greengrass', actors: ['Andrew Garfield'] };
  assert.equal(ctx.dispDirector(it), 'Paul Greengrass');
  assert.deepEqual([...ctx.dispActors(it)], ['Andrew Garfield']);
  assert.equal(ctx.dispDirector({}), '');
  assert.deepEqual([...ctx.dispActors({})], []);
});

test('peoplePending — только русский интерфейс и незавершённый перевод', () => {
  assert.equal(setup('ru').peoplePending({ people_pending: true }), true);
  assert.equal(setup('ru').peoplePending({ people_pending: false }), false);
  assert.equal(setup('ru').peoplePending(null), false);
  assert.equal(setup('en').peoplePending({ people_pending: true }), false);
});
