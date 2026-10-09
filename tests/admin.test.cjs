const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function harness(fetch) {
  const nodes = new Map();
  function element() {
    return {children: [], dataset: {}, events: {}, innerHTML: '',
      addEventListener(name, fn) { this.events[name] = fn; },
      appendChild(child) { this.children.push(child); },
      querySelector() { return element(); },
      classList: {add() {}, remove() {}, toggle() {}}};
  }
  const context = vm.createContext({fetch, console, setTimeout: () => 1, clearTimeout() {},
    document: {
      getElementById(id) { if (!nodes.has(id)) nodes.set(id, element()); return nodes.get(id); },
      createElement: element, createDocumentFragment: element,
    }});
  const source = fs.readFileSync(path.join(__dirname, '../web/admin.js'), 'utf8');
  vm.runInContext(source.replace(/init\(\);\s*$/, ''), context);
  return {context, nodes};
}

test('admin loads all records and renders a queue larger than the former limit', async () => {
  const items = Array.from({length: 605}, (_, i) => ({imdb_id: 'tt'+i, rating: 7.1}));
  let url;
  const {context, nodes} = harness(async u => {url = u; return {ok: true, json: async () => ({items, total: items.length})};});
  await context.loadFilms();
  assert.equal(url, '/api/admin/films/missing');
  const rows = nodes.get('films-body').children[0].children;
  assert.equal(rows.length, 605);
  assert.match(rows[0].innerHTML, /type="hidden" data-f="rating" value="7.1"/);
});

test('failed refresh table shows escaped reason and retry time', () => {
  const {context} = harness();
  const row = context.makeNotFoundRow({imdb_id:'tt1', refresh_error:'<script>error</script>', retry_at:'2026-10-11T12:00:00Z'});
  assert.match(row.innerHTML, /&lt;script&gt;error&lt;\/script&gt;/);
  assert.match(row.innerHTML, /11\.10\.2026/);
  assert.match(row.innerHTML, /Вернуть в очередь/);
});

test('mass update HTTP failure is shown and releases the button', async () => {
  const {nodes} = harness(async () => ({ok:false, text:async () => 'Обновление уже выполняется'}));
  const button = nodes.get('refresh-all');
  button.events.click();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(button.disabled, false);
  assert.ok(nodes.get('admin-log').children.some(el => /Обновление уже выполняется/.test(el.textContent)));
});
