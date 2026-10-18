// Actual film/watch player DOM with deterministic metadata and an inert HLS transport.
'use strict';
const { test, before, after } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const web = path.resolve(__dirname, '../web');
const series = fs.readFileSync(path.join(web, 'series.js'), 'utf8');
const matchStart = series.indexOf('function matchVoiceOrdinal(');
const matcher = series.slice(matchStart, series.indexOf('\n}', matchStart) + 2);
const bootstrap = `
const VV = {user:null}, DEBUG = false, VOICE_ALIASES = {};
const currentItem = {id:'tt123',kind:'tv'};
const t = key => ({audioTracks:'Звуковые дорожки',audioSelected:'Сейчас выбрана',
  audioNone:'В этом видео нет звуковой дорожки',trackFallback:'Дорожка',audioOriginal:'Оригинал'}[key] || key);
window.__diagnostics = [];
const dbg = line => window.__diagnostics.push(line), fmtTime = x => String(Math.round(x || 0)), isHevcCodec = () => false;
const renderDebug = () => {};
const isSeriesKind = () => true, seasonEpisodeCount = () => 0;
const fetchFiles = async () => [0,1,2,3].map(index => ({index,season:7,episode:index+15}));
const episodeHistoryEntry = () => null, titleVoices = () => [], storeItem = () => {};
const Personal = {preferences:()=>({})};
window.__streams = [];
class Hls {
  static Events = {}; static version = 'test'; static isSupported() {return true;}
  constructor(config) {this.config = config;}
  on() {} attachMedia() {} destroy() {}
  loadSource(url) {window.__streams.push(Object.fromEntries(new URL(url, location.href).searchParams));}
}
window.Hls = Hls;
${matcher}
`;
let server, browser, base;
const tracks = file => file === 0 ? [{ordinal:1,title:'Kravec',language:'rus',codec:'ac3'}]
  : file === 1 ? [{ordinal:0,title:'Kravec',language:'rus',codec:'truehd'},
    {ordinal:1,title:'Kravec',language:'rus',codec:'ac3'}, {ordinal:2,title:'Original',language:'eng',codec:'ac3'}]
    : file === 2 ? [{ordinal:0,title:'Kravec',language:'rus',codec:'ac3'}, {ordinal:4,title:'Original',language:'eng',codec:'ac3'}] : [];

before(async () => {
  server = http.createServer((req, res) => {
    const url = new URL(req.url, 'http://fixture');
    if (url.pathname.endsWith('/tracks')) {
      res.setHeader('Content-Type', 'application/json');
      return res.end(JSON.stringify({duration:2600,codec:'h264',items:tracks(Number(url.searchParams.get('file'))),subtitles:[]}));
    }
    if (url.pathname.startsWith('/api/')) return res.end('{}');
    const filename = path.resolve(web, '.' + url.pathname);
    if (!filename.startsWith(web + path.sep) || !fs.existsSync(filename)) {res.statusCode=404; return res.end();}
    if (url.pathname.endsWith('.html')) {
      const html = fs.readFileSync(filename, 'utf8').replace(/<script\b[^>]*>[\s\S]*?<\/script>/g, '');
      res.setHeader('Content-Type', 'text/html; charset=utf-8');
      return res.end(html.replace('</body>', '<script>' + bootstrap + '</script><script src="/player.js"></script><script src="/playback-diagnostics.js"></script></body>'));
    }
    res.setHeader('Content-Type', filename.endsWith('.js') ? 'application/javascript' : 'text/css');
    res.end(fs.readFileSync(filename));
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  base = 'http://127.0.0.1:' + server.address().port;
  const executablePath = [process.env.CHROMIUM_PATH, chromium.executablePath(), 'C:/Program Files/Google/Chrome/Application/chrome.exe'].find(value => value && fs.existsSync(value));
  browser = await chromium.launch({executablePath,headless:true});
});
after(async () => {await browser?.close(); await new Promise(resolve => server?.close(resolve));});

for (const pageName of ['film', 'watch']) test(pageName + ': actual audio remains visible and selected through source, retry and episode changes', async () => {
  const page = await browser.newPage({viewport:{width:1100,height:1000}}), errors = [];
  page.on('pageerror', error => errors.push(error.message));
  try {
    await page.goto(base + '/' + pageName + '.html');
    await page.evaluate(() => {
      for (const id of ['details','film-watch-layout','film-playback-column']) {
        const element = document.getElementById(id); if (element) {element.hidden=false; element.classList.remove('no-player');}
      }
      PP.start({id:'tt123',magnet:'one',file:0,voice:'Missing voice',pos:125});
    });
    await page.waitForFunction(() => PP.state().audio?.title === 'Kravec');
    assert.equal(await page.locator('#tracks').isVisible(), true, 'a sole audio track is still identified');
    assert.equal(await page.locator('#tracks-current').textContent(), 'Сейчас выбрана: Kravec RUS');
    assert.equal(await page.evaluate(() => PP.state().voice), 'Kravec', 'unavailable preference cannot mislabel actual audio');
    assert.ok(await page.evaluate(() => window.__diagnostics.some(line => line.startsWith('playback: выбранный источник') && line.includes('audio="Kravec"'))), 'actual selected audio reaches diagnostics');
    await page.locator('#ctrl-debug').click();
    assert.ok(await page.evaluate(() => window.__diagnostics.some(line => line.startsWith('player: кадры @') && line.includes('diagnostics'))), 'diagnostic button captures decoding counters');
    assert.equal(await page.locator('.track-btn[aria-pressed=true]').getAttribute('aria-label'), 'Kravec RUS');

    await page.evaluate(() => PP.start({id:'tt123',magnet:'one',file:1,pos:125}));
    await page.waitForFunction(() => PP.state().audio?.codec === 'ac3' && document.querySelectorAll('.track-btn').length===2);
    assert.equal(await page.locator('.track-btn[aria-pressed=true]').count(), 1, 'selected duplicate codec retains its button');
    assert.equal(await page.evaluate(() => PP.state().track), 1);
    await page.getByRole('button', {name:'Оригинал ENG',exact:true}).click();
    assert.equal(await page.evaluate(() => PP.state().track), 2);
    assert.equal(await page.evaluate(() => PP.state().audio.language), 'eng');
    assert.equal(await page.locator('#tracks-current').textContent(), 'Сейчас выбрана: Оригинал ENG');
    assert.equal(await page.evaluate(() => window.__streams.at(-1).start), '125');
    const count = await page.evaluate(() => window.__streams.length);
    await page.getByRole('button', {name:'Оригинал ENG',exact:true}).click();
    assert.equal(await page.evaluate(() => window.__streams.length), count, 'clicking selected track does not restart playback');
    await page.evaluate(() => PP.start({...PP.state(),pos:125}));
    await page.waitForFunction(() => PP.state().audio?.ordinal === 2);
    await page.evaluate(() => PP.selectEpisode(2));
    await page.waitForFunction(() => PP.state().audio?.ordinal === 4);
    assert.equal(await page.locator('.track-btn[aria-pressed=true]').getAttribute('aria-label'), 'Оригинал ENG');

    await page.evaluate(() => PP.start({id:'tt123',magnet:'two',file:0,voice:'Kravec'}));
    await page.waitForFunction(() => PP.state().audio?.language === 'rus');
    assert.equal(await page.locator('#tracks-current').textContent(), 'Сейчас выбрана: Kravec RUS');
    if (process.env.SCREENSHOT_DIR) {
      fs.mkdirSync(process.env.SCREENSHOT_DIR, {recursive:true});
      await page.locator('#tracks').screenshot({path:path.join(process.env.SCREENSHOT_DIR, 'audio-selected-' + pageName + '.png')});
    }
    await page.evaluate(() => PP.start({id:'tt123',magnet:'two',file:3}));
    await page.waitForFunction(() => document.querySelector('#tracks-current').textContent === 'В этом видео нет звуковой дорожки');
    assert.equal(await page.evaluate(() => PP.state().audio), null);
    assert.equal(await page.evaluate(() => PP.state().voice), '');
    assert.deepEqual(errors, []);
  } finally {await page.close();}
});
