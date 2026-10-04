// PLAYWRIGHT_MODULE and CHROMIUM_PATH may point to a preinstalled browser runtime.
// SUBTITLE_FIXTURE optionally supplies FFmpeg-generated master.m3u8 + segments.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const assert = require('node:assert/strict');
const web = path.join(__dirname, '../web');
const fixture = process.env.SUBTITLE_FIXTURE;
const items = Array.from({ length: 12 }, (_, i) => ({ id: 'tt' + i, title: 'Фильм ' + i, year: 2026 }));
const bootstrap = `
const VV = {user:null}, DEBUG = false;
const currentItem = {id:'tt123', title:'Проверка', kind:'tv', seasons:3};
const t = key => ({directorLabel:'Режиссёр',actorsLabel:'В ролях',subsOff:'Выкл'}[key] || key);
const dbg = console.log, fmtTime = x => String(Math.round(x || 0)), isHevcCodec = () => false;
const isSeriesKind = () => true, fetchFiles = async () => [], seasonEpisodeCount = () => 0;
const episodeHistoryEntry = () => null, titleVoices = () => [], storeItem = () => {};
const applyLang = () => {}, initAuth = async () => {};
const Personal = {snapshot:x=>x, get:()=>null, preferences:()=>({}), load:async()=>{},
request:async url => (await fetch(url)).json()};
`;
const requests = [];
const server = http.createServer((req, res) => {
  const url = new URL(req.url, 'http://localhost');
  const sendJSON = data => { res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(data)); };
  if (url.pathname.endsWith('/explore')) return sendJSON({ items, directors:[{id:1,name:'Режиссёр'}], cast:[{id:2,name:'Актёр'}], trailer:'https://www.youtube.com/watch?v=abcdefghijk' });
  if (url.pathname === '/api/discover/picks') return sendJSON({items,total_pages:2});
  if (url.pathname.endsWith('/tracks')) return sendJSON({ duration:12, codec:'h264',height:180,items:[{ordinal:0,title:'Original',language:'eng'}],subtitles:[{ordinal:0,title:'English',language:'eng'}] });
  if (url.pathname.includes('/hls/stop')) return res.end('{}');
  if (url.pathname === '/film.html' || url.pathname === '/discover.html') {
    const film = url.pathname === '/film.html';
    let html = fs.readFileSync(path.join(web, url.pathname), 'utf8').replace(/<script\b[^>]*>[\s\S]*?<\/script>/g, '');
    html = html.replace('</body>', `<script>${bootstrap}</script><script src="/features.js"></script>` + (film ? '<script src="/vendor/hls.min.js"></script><script src="/player.js"></script><script src="/film-features.js"></script>' : '') + '</body>');
    res.setHeader('Content-Type','text/html; charset=utf-8'); return res.end(html);
  }
  let file = path.join(web, url.pathname);
  if (fixture && url.pathname.startsWith('/api/films/')) {
    let name = path.basename(url.pathname);
    if (name === 'hls.m3u8') {
      requests.push(Object.fromEntries(url.searchParams));
      name = url.searchParams.has('subs') ? 'master.m3u8' : 'playlist.m3u8';
    }
    file = path.join(fixture, name);
  }
  const types = {'.js':'text/javascript','.css':'text/css','.m3u8':'application/vnd.apple.mpegurl','.vtt':'text/vtt','.mp4':'video/mp4','.m4s':'video/iso.segment'};
  res.setHeader('Content-Type', types[path.extname(file)] || 'application/octet-stream');
  fs.readFile(file, (error, data) => { if (error) {res.statusCode=404;res.end();} else res.end(data); });
});
(async () => {
  await new Promise(resolve => server.listen(0, '0.0.0.0', resolve));
  const browser = await chromium.launch({ executablePath:process.env.CHROMIUM_PATH, headless:true,args:['--no-sandbox','--autoplay-policy=no-user-gesture-required'] });
  try {
    const page = await browser.newPage();
    const errors = [];
    page.on('pageerror', e => errors.push(e.message));
    await page.route('https://www.youtube-nocookie.com/**', route => route.fulfill({body:'Trailer fixture'}));
    const base = 'http://127.0.0.1:' + server.address().port;
    await page.goto(base + '/film.html');
    await page.evaluate(async () => {
      document.getElementById('details').hidden = false;
      FilmFeatures.render(currentItem);
      FilmFeatures.sources([{title:'Раздача',magnet:'test'}], 'tt123');
      await FilmFeatures.explore(currentItem);
    });
    assert.equal(await page.locator('#details-director a').count(),1);
    assert.equal(await page.locator('#film-explore details').count(),0);
    for (const width of [1280,390]) {
      await page.setViewportSize({width,height:900});
      const layout = await page.evaluate(() => {
        const box = document.querySelector('.page-card').getBoundingClientRect();
        const personal = document.querySelector('#film-personal').getBoundingClientRect();
        const rail = document.querySelector('.feature-carousel');
        return { inset:personal.left-box.left,overflow:rail.scrollWidth>rail.clientWidth,
          rows:new Set([...rail.children].map(x=>x.offsetTop)).size,
          pageOverflow:document.documentElement.scrollWidth>innerWidth };
      });
      assert.ok(layout.inset>=20); assert.ok(layout.overflow); assert.equal(layout.rows,1); assert.equal(layout.pageOverflow,false);
      if (process.env.SCREENSHOT_DIR) await page.screenshot({path:path.join(process.env.SCREENSHOT_DIR, 'film-' + width + '.png'),fullPage:true});
    }
    await page.click('#trailer-toggle');
    assert.equal(await page.locator('#trailer-player').isVisible(),true);
    assert.equal(await page.locator('#player').isVisible(),false);
    await page.click('#trailer-toggle');
    assert.equal(await page.locator('#player-wrap').isVisible(),false);
    assert.equal(await page.locator('#trailer-player').getAttribute('src'),null);
    if (fixture) {
      await page.evaluate(() => PP.start({id:'tt123',magnet:'test'}));
      await page.waitForFunction(() => document.querySelector('#player').currentTime > 1);
      const before = await page.evaluate(() => { const p=document.querySelector('#player');p.pause();return p.currentTime; });
      await page.click('#subs-list .sub-btn:nth-child(2)');
      await page.waitForFunction(() => [...document.querySelector('#player').textTracks].some(t => t.mode==='showing' && t.activeCues?.length));
      assert.ok(Math.abs(Number(requests.at(-1).start)-before)<0.5, 'subtitle selection keeps playback position');
      console.log('PASS: real FFmpeg WebVTT cues visible in Chromium');
      await page.click('#trailer-toggle');
      assert.equal(await page.evaluate(() => document.querySelector('#player').paused),true);
      await page.click('#trailer-toggle');
      assert.equal(await page.locator('#player').isVisible(),true);
      await page.evaluate(() => PP.stop());
    }
    await page.goto(base + '/discover.html');
    await page.waitForSelector('.feature-pagination button');
    const gap = await page.evaluate(() => document.querySelector('.feature-pagination').getBoundingClientRect().top-document.querySelector('#feature-content').getBoundingClientRect().bottom);
    assert.ok(gap>=20);
    assert.deepEqual(errors,[]);
    console.log('PASS: credits, responsive spacing, similar carousel, trailer lifecycle, discover pagination');
  } finally { await browser.close(); server.close(); }
})().catch(error => { console.error(error); server.close(); process.exitCode=1; });
