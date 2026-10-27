'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../web/tv.js'), 'utf8');

// Exercise the public keyboard listener with deterministic element geometry.
// Links in the history rail can have different heights (episode metadata/title wrapping).
function fixture(search = '?tv=1') {
  const listeners = {}, elements = [];
  const document = {
    addEventListener(name, listener) { listeners[name] = listener; },
    getElementById(id) { return elements.find(el => el.id === id) || null; },
    querySelector(selector) {
      if (selector === '.grid .card') return elements.find(el => el.classList.contains('card')) || null;
      if (selector === '.continue .continue-link') return elements.find(el => el.classList.contains('continue-link')) || null;
      return null;
    },
  };
  function element(name, left, top, width, height, options = {}) {
    const attributes = new Map(), classes = new Set(options.classes || []);
    const el = {
      name, id: options.id, tagName: options.tagName || 'A',
      parentNode: options.parent || document.body || document,
      hidden: !!options.hidden, disabled: !!options.disabled,
      classList: { add(value) { classes.add(value); }, contains(value) { return classes.has(value); } },
      getBoundingClientRect() { return { left, top, width, height, right: left + width, bottom: top + height }; },
      getAttribute(key) { return attributes.get(key) || null; },
      hasAttribute(key) { return attributes.has(key); },
      setAttribute(key, value) { attributes.set(key, value); },
      matches(selector) { return selector === ':disabled' && (!!options.disabled || !!options.disabledFieldset); },
      closest() { return null; },
      contains(other) { for (let n = other; n; n = n.parentNode) if (n === this) return true; return false; },
      querySelectorAll() { return elements.filter(candidate => candidate !== this && this.contains(candidate)); },
      focus() { if (!this.disabled && !options.disabledFieldset) document.activeElement = this; },
      scrollIntoView() { this.scrolled = true; },
    };
    if (options.candidate !== false) elements.push(el);
    return el;
  }
  document.documentElement = element('html', 0, 0, 1920, 3000, { candidate: false });
  document.body = element('body', 0, 0, 1920, 3000, { tagName: 'BODY', candidate: false });
  document.body.parentNode = document.documentElement;
  document.activeElement = document.body;
  const window = {
    location: { search }, navigator: { userAgent: '' }, localStorage: { getItem() { return null; } },
    getComputedStyle(el) { return { display: el.hidden ? 'none' : 'block', visibility: 'visible' }; },
  };
  vm.runInNewContext(source, { document, window, URLSearchParams, MutationObserver: class { observe() {} } });
  function press(key, keyCode) {
    const event = { key, keyCode, target: document.activeElement,
      preventDefault() { this.defaultPrevented = true; }, stopPropagation() {} };
    if (listeners.keydown) listeners.keydown(event);
    return event;
  }
  return { document, element, press };
}

test('Down leaves the history row even when a neighbour has more episode metadata', () => {
  const f = fixture();
  const current = f.element('movie', 0, 0, 200, 150, { classes: ['continue-link'] });
  f.element('series', 220, 0, 200, 200, { classes: ['continue-link'] });
  const card = f.element('catalogue', 0, 650, 200, 300, { classes: ['card'] });
  current.focus();
  assert.equal(f.press('ArrowDown').defaultPrevented, true);
  assert.equal(f.document.activeElement, card);
  assert.equal(card.scrolled, true);
});

test('Up leaves the history row instead of selecting a shorter neighbour', () => {
  const f = fixture();
  const header = f.element('header', 0, 0, 200, 40);
  f.element('movie', 220, 650, 200, 150, { classes: ['continue-link'] });
  const current = f.element('series', 0, 650, 200, 200, { classes: ['continue-link'] });
  current.focus();
  f.press('ArrowUp');
  assert.equal(f.document.activeElement, header);
});

test('Down advances through catalogue sections and cards, and Up returns', () => {
  const f = fixture();
  const history = f.element('history', 0, 0, 200, 150, { classes: ['continue-link'] });
  const section = f.element('section', 0, 210, 200, 40, { tagName: 'BUTTON' });
  const first = f.element('first row', 0, 300, 200, 300, { classes: ['card'] });
  const second = f.element('second row', 0, 640, 200, 300, { classes: ['card'] });
  history.focus();
  for (const next of [section, first, second]) {
    f.press('ArrowDown');
    assert.equal(f.document.activeElement, next);
  }
  for (const previous of [first, section, history]) {
    f.press('ArrowUp');
    assert.equal(f.document.activeElement, previous);
  }
});

test('disabled buttons and disabled fieldset descendants cannot block navigation', () => {
  for (const options of [{ disabled: true }, { disabledFieldset: true }]) {
    const f = fixture();
    const current = f.element('current', 0, 0, 200, 150);
    f.element('disabled', 0, 170, 200, 40, { tagName: 'BUTTON', ...options });
    const next = f.element('next', 0, 250, 200, 300);
    current.focus();
    f.press('ArrowDown');
    assert.equal(f.document.activeElement, next);
  }
});

test('navigation skips a focusable ancestor and descendant of the current control', () => {
  const f = fixture();
  const parent = f.element('parent', 0, 0, 200, 300, { tagName: 'ARTICLE' });
  const current = f.element('current', 0, 0, 200, 100, { parent });
  f.element('nested action', 0, 120, 200, 40, { parent: current, tagName: 'BUTTON' });
  const next = f.element('next', 0, 500, 200, 100);
  current.focus();
  f.press('ArrowDown');
  assert.equal(f.document.activeElement, next);
});

test('Right and Left still traverse history cards with unequal heights', () => {
  const f = fixture();
  const first = f.element('movie', 0, 0, 200, 150, { classes: ['continue-link'] });
  const second = f.element('series', 220, 0, 200, 200, { classes: ['continue-link'] });
  first.focus();
  f.press('ArrowRight');
  assert.equal(f.document.activeElement, second);
  f.press('ArrowLeft');
  assert.equal(f.document.activeElement, first);
});

test('the first Down press from the page body uses the first catalogue card', () => {
  const f = fixture();
  const first = f.element('first', 0, 200, 200, 300, { classes: ['card'] });
  f.element('last', 0, 1200, 200, 300, { classes: ['card'] });
  f.press('ArrowDown');
  assert.equal(f.document.activeElement, first);
});

test('legacy WebView key codes also move focus down', () => {
  const f = fixture();
  const current = f.element('current', 0, 0, 200, 150);
  const next = f.element('next', 0, 200, 200, 300);
  current.focus();
  f.press('Unidentified', 40);
  assert.equal(f.document.activeElement, next);
});

test('normal browser and text input arrows keep their native behavior', () => {
  const regular = fixture('');
  assert.equal(regular.press('ArrowDown').defaultPrevented, undefined);
  const tv = fixture();
  const input = tv.element('search', 0, 0, 200, 40, { tagName: 'INPUT' });
  input.focus();
  assert.equal(tv.press('ArrowDown').defaultPrevented, undefined);
  assert.equal(tv.document.activeElement, input);
});
