const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function harness() {
  const listeners = {}, elements = [], intervals = [], dispatched = [], started = [];
  let now = 100000, follower = false, networkCalls = 0, cleared = 0;
  class Element {
    constructor() { this.handlers = {}; this.hidden = false; this.disabled = false; elements.push(this); }
    append() {} after() {} setAttribute() {}
    addEventListener(name, callback) { this.handlers[name] = callback; }
    emit(name) { this.handlers[name]?.(); }
    click() { if (!this.disabled) return this.onclick?.(); }
  }
  const video = new Element(), wrap = new Element();
  video.currentTime = 0; video.ended = false;
  const state = { id: 'tt1', magnet: 'old', file: 4, season: 2, episode: 3,
    voice: 'LostFilm', position: 157, quality: 'source' };
  const document = { hidden: false, createElement: () => new Element(),
    getElementById: id => id === 'player' ? video : wrap };
  const window = {
    addEventListener(name, callback) { listeners[name] = callback; },
    dispatchEvent(event) { dispatched.push(event.type); listeners[event.type]?.(event); },
  };
  const context = vm.createContext({ document, window,
    PP: { available: true, isFollower: () => follower, playing: () => true,
      state: () => state, start: options => started.push(options) },
    fetch() { networkCalls++; throw new Error('Recovery must not search sources'); },
    fetchFiles() { networkCalls++; throw new Error('Recovery must not inspect releases'); },
    Event: function Event(type) { this.type = type; },
    setInterval(callback) { intervals.push(callback); return 1; },
    clearInterval() { cleared++; }, Date: { now: () => now },
  });
  vm.runInContext(fs.readFileSync(path.join(__dirname, '../web/recovery.js'), 'utf8'), context);
  return { video, state, document, window, listeners, dispatched, started,
    button: elements.at(-1), box: elements[2], text: elements[3],
    advance: milliseconds => { now += milliseconds; }, interval: () => intervals[0](),
    follow(value = true) { follower = value; listeners.playbackrole(); },
    get networkCalls() { return networkCalls; }, get cleared() { return cleared; },
  };
}

test('the recovery button requests the manual source picker without selecting, loading or starting a release', () => {
  const h = harness(), original = { ...h.state };
  let opened = 0;
  assert.equal(h.listeners.requestsourcechange, undefined, 'The picker module owns this event; recovery must not listen recursively');
  h.window.addEventListener('requestsourcechange', () => { opened++; });
  h.listeners.playbackfailure({ detail: { message: 'Этот фрагмент повторно не удалось обработать.' } });
  h.button.click();
  assert.equal(opened, 1);
  assert.deepEqual(h.dispatched, ['requestsourcechange']);
  assert.equal(h.networkCalls, 0);
  assert.equal(h.started.length, 0);
  assert.deepEqual(h.state, original, 'Opening the selector retains source, episode, voice and position until the user confirms');
  assert.equal(h.button.disabled, false);
  assert.equal(h.text.textContent, 'Этот фрагмент повторно не удалось обработать.');
});

test('source-change requests from another control are not consumed or redispatched by recovery', () => {
  const h = harness();
  h.listeners.playbackfailure({ detail: { message: 'Нужно выбрать источник.' } });
  let opened = 0;
  h.window.addEventListener('requestsourcechange', () => { opened++; });
  h.window.dispatchEvent({ type: 'requestsourcechange' });
  assert.equal(opened, 1);
  assert.deepEqual(h.dispatched, ['requestsourcechange']);
  assert.equal(h.networkCalls, 0); assert.equal(h.started.length, 0);
  assert.equal(h.text.textContent, 'Нужно выбрать источник.');
});

test('room guests cannot see the offer or open the picker even through a direct button handler', () => {
  const h = harness();
  h.listeners.playbackfailure(); assert.equal(h.box.hidden, false);
  h.follow();
  h.listeners.playbackfailure(); h.button.onclick();
  h.video.emit('error'); h.video.emit('waiting'); h.advance(27000); h.interval();
  assert.equal(h.box.hidden, true); assert.equal(h.button.disabled, true);
  assert.equal(h.dispatched.length, 0); assert.equal(h.networkCalls, 0); assert.equal(h.started.length, 0);
  h.follow(false); h.button.click();
  assert.equal(h.button.disabled, false);
  assert.deepEqual(h.dispatched, ['requestsourcechange'], 'Leaving the guest role permits a manual source choice');
});

test('the real repeated-fragment failure survives the stall timer and opening the picker', () => {
  const h = harness();
  h.listeners.playbackstart();
  const message = 'Один и тот же видеофрагмент повторно не удалось обработать. Выберите другой источник — позиция просмотра сохранится.';
  h.listeners.playbackfailure({ detail: { reason: 'repeated_media_fragment', message } });
  h.advance(27000); h.interval(); h.button.click();
  assert.equal(h.box.hidden, false); assert.equal(h.text.textContent, message);
  assert.deepEqual(h.dispatched, ['requestsourcechange']);
  assert.equal(h.networkCalls, 0); assert.equal(h.started.length, 0);
});

test('a failure without details suggests manual selection without asserting a slow network or source', () => {
  const h = harness(); h.listeners.playbackfailure();
  assert.match(h.text.textContent, /Воспроизведение прервалось/);
  assert.match(h.text.textContent, /с сохранением позиции/);
  assert.doesNotMatch(h.text.textContent, /не поступает|соединение|медленно|не отвечает/);
});

test('a new playback or actual resumed playback clears the previous failure message', () => {
  for (const event of ['playbackstart', 'playing']) {
    const h = harness();
    h.listeners.playbackfailure({ detail: { message: 'Предыдущий фрагмент не удалось обработать.' } });
    if (event === 'playbackstart') h.listeners.playbackstart();
    else h.video.emit('playing');
    assert.equal(h.box.hidden, true);
    h.video.emit('waiting'); h.advance(27000); h.interval();
    assert.equal(h.box.hidden, false);
    assert.doesNotMatch(h.text.textContent, /Предыдущий фрагмент/);
    assert.match(h.text.textContent, /Воспроизведение задерживается/);
    assert.match(h.text.textContent, /открыть список источников/);
    assert.doesNotMatch(h.text.textContent, /не поступает|соединение|источник.*не отвечает/);
    assert.equal(h.networkCalls, 0); assert.equal(h.started.length, 0);
  }
});

test('ordinary playback progress clears a stall without offering or choosing another source', () => {
  const h = harness();
  h.listeners.playbackstart(); h.advance(20000);
  h.video.currentTime = 10; h.video.emit('timeupdate');
  h.advance(30000); h.interval();
  assert.equal(h.box.hidden, true);
  assert.equal(h.dispatched.length, 0); assert.equal(h.networkCalls, 0); assert.equal(h.started.length, 0);
});

test('hidden or ended playback does not show a timed offer, and pagehide releases the timer', () => {
  for (const condition of ['hidden', 'ended']) {
    const h = harness(); h.listeners.playbackstart(); h.advance(27000);
    if (condition === 'hidden') h.document.hidden = true; else h.video.ended = true;
    h.interval(); assert.equal(h.box.hidden, true);
    h.listeners.pagehide(); assert.equal(h.cleared, 1);
    assert.equal(h.dispatched.length, 0); assert.equal(h.networkCalls, 0);
  }
});
