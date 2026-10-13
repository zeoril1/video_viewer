// Real hls.js + Chromium MSE: a malformed fragment poisons SourceBuffer,
// then the same server stream supplies valid media after bounded recovery.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync } = require('node:child_process');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const web = path.join(__dirname, '../web');
const media = fs.mkdtempSync(path.join(os.tmpdir(), 'kinoteka-mse-'));
execFileSync(process.env.FFMPEG_BINARY || 'ffmpeg', ['-hide_banner', '-loglevel', 'error', '-y',
  '-f', 'lavfi', '-i', 'testsrc2=size=128x96:rate=12', '-f', 'lavfi', '-i', 'sine=frequency=440:sample_rate=48000',
  '-t', '20', '-c:v', 'libx264', '-preset', 'fast', '-g', '24', '-bf', '2', '-pix_fmt', 'yuv420p',
  '-c:a', 'aac', '-f', 'hls', '-hls_time', '2', '-hls_list_size', '0', '-hls_segment_type', 'fmp4',
  '-hls_fmp4_init_filename', 'init.mp4', '-hls_segment_filename', 'seg_%05d.m4s', 'playlist.m3u8'],
  { cwd: media, timeout: 60000, windowsHide: true });

function malformedFragment(data) {
  const bad = Buffer.from(data);
  // An unknown track is a container error in Chromium's asynchronous append
  // path. Keep the box layout intact so hls.js still recognizes fragmented MP4.
  const box = bad.indexOf(Buffer.from('tfhd'));
  assert.ok(box >= 0, 'FFmpeg fixture has a track fragment header');
  bad.writeUInt32BE(999, box + 8);
  return bad;
}

const bootstrap = `
const VV={user:null},DEBUG=false;
let currentItem={id:'tt-mse',kind:'movie'};
const fixture={logs:[],errors:[],recoveries:[],starts:[],stages:[],stops:0};window.fixture=fixture;
window.addEventListener('playbackstage',event=>fixture.stages.push(event.detail));
const t=x=>x,dbg=x=>fixture.logs.push(x),fmtTime=x=>String(x),isHevcCodec=()=>false,isSeriesKind=()=>false;
const episodeHistoryEntry=()=>null,rememberWatchProgress=()=>{},seasonEpisodeCount=()=>0,canonicalByNumber=()=>null,titleVoices=()=>[];
const fetchFiles=async()=>[{index:0,name:'fixture.mkv'}];
const Personal={get:()=>null,rank:x=>x,preferences:()=>({}),put:async()=>{}};
window.Hls=class extends Hls {
 constructor(...args){super(...args);window.activeHls=this;this.on(Hls.Events.ERROR,(_,data)=>{
  fixture.errors.push({details:data.details,fatal:data.fatal,message:data.error?.message,media:this.media?.error?.message});
 });}
 loadSource(url){fixture.starts.push(url);return super.loadSource(url);}
 recoverMediaError(){fixture.recoveries.push(PP.state().position);return super.recoverMediaError();}
};
`;

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH,
    args: ['--no-sandbox', '--autoplay-policy=no-user-gesture-required'] });
  try {
    const context = await browser.newContext();
    let page = await context.newPage();
    const pageErrors = [], streamRequests = [];
    let badRequests = 0, stopped = 0, persistentFailure = false, badRecoveryFloor = 0, allowPausedFailure = false;
    page.on('pageerror', e => pageErrors.push(e.message));
    await context.route('**/*', async route => {
      const u = new URL(route.request().url());
      const json = data => route.fulfill({ contentType: 'application/json', body: JSON.stringify(data) });
      if (u.pathname.endsWith('/tracks')) return json({ duration: 1800, codec: 'h264', height: 96,
        items: [{ ordinal: 0, language: 'eng', title: 'Original' }], subtitles: [] });
      if (u.pathname.endsWith('/hls/stop')) { stopped++; return route.fulfill({ status: 204 }); }
      if (u.pathname === '/watch.html') {
        let html = fs.readFileSync(path.join(web, 'watch.html'), 'utf8').replace(/<script\b[^>]*>[\s\S]*?<\/script>/g, '');
        html = html.replace('</body>', '<script src="/vendor/hls.min.js"></script><script>' + bootstrap +
          '</script><script src="/player.js"></script><script src="/playback-extras.js"></script></body>');
        return route.fulfill({ contentType: 'text/html; charset=utf-8', body: html });
      }
      if (u.pathname.startsWith('/api/films/')) {
        const name = path.basename(u.pathname) === 'hls.m3u8' ? 'playlist.m3u8' : path.basename(u.pathname);
        const file = path.join(media, name);
        if (!fs.existsSync(file)) return route.fulfill({ status: 404 });
        if (name === 'playlist.m3u8') streamRequests.push(Object.fromEntries(u.searchParams));
        let body = fs.readFileSync(file);
        if (name === 'seg_00003.m4s' && (persistentFailure || (await page.evaluate(() => fixture.recoveries.length)) <= badRecoveryFloor)) {
          // Ensure the error happens during playback, rather than before play.
          if (allowPausedFailure) {
            await page.waitForFunction(() => fixture.triggerPausedFailure, null, { timeout: 10000 });
          }
          await page.waitForFunction(pausedAllowed => document.getElementById('player').currentTime > 1 ||
            pausedAllowed && PP.ready(), allowPausedFailure, { timeout: 10000 });
          body = malformedFragment(body); badRequests++;
        }
        return route.fulfill({ contentType: name.endsWith('.m3u8') ? 'application/vnd.apple.mpegurl' : 'video/mp4', body });
      }
      if (u.pathname.startsWith('/api/')) return json({});
      const file = path.join(web, u.pathname);
      return route.fulfill(fs.existsSync(file) ? { contentType: u.pathname.endsWith('.js') ? 'text/javascript' : 'text/css',
        body: fs.readFileSync(file) } : { status: 404 });
    });
    await page.goto('http://fixture.local/watch.html');
    await page.evaluate(() => {
      PP.setFollower(false);
      PP.start({ id: 'tt-mse', magnet: 'magnet:test', file: 0, pos: 1139.51 });
    });
    await page.waitForFunction(() => fixture.recoveries.length > 0, null, { timeout: 20000 }).catch(async error => {
      console.error(await page.evaluate(() => ({ fixture, ready: PP.ready(), state: PP.state(),
        media: { paused: document.getElementById('player').paused,
          readyState: document.getElementById('player').readyState,
          error: document.getElementById('player').error?.message } })));
      throw error;
    });
    const recovered = await page.evaluate(() => ({ errors: fixture.errors, position: fixture.recoveries[0] }));
    assert.ok(recovered.errors.some(e => e.details === 'bufferAppendingError' && !e.fatal), 'real asynchronous SourceBuffer error is reproduced');
    assert.ok(recovered.errors.some(e => e.details === 'bufferAppendError' && e.fatal), 'the real pinned HLS library reports the user failure');
    assert.ok(badRequests > 0);
    await page.waitForFunction(position => PP.ready() && !document.getElementById('player').paused &&
      PP.state().position > position + 1, recovered.position, { timeout: 15000 });
    assert.equal(await page.evaluate(() => fixture.starts.length), 1, 'recovery keeps the existing backend HLS stream');
    assert.equal(stopped, 0, 'recovery does not delete the server session');
    assert.equal(await page.evaluate(() => PP.state().quality), 'source');
    assert.equal(await page.evaluate(() => fixture.recoveries.length), 1);
    assert.equal(await page.locator('#player-error').isVisible(), false);
    assert.ok(streamRequests.every(query => query.start === '1139.51' && query.session === streamRequests[0].session));
    assert.deepEqual(pageErrors, []);
    console.log('PASS: real asynchronous MSE append failure recovers at the same position, source and server session.');

    await page.close();
    persistentFailure = true;
    page = await context.newPage();
    page.on('pageerror', e => pageErrors.push(e.message));
    await page.goto('http://fixture.local/watch.html');
    await page.evaluate(() => {
      PP.setFollower(false);
      PP.start({ id: 'tt-mse', magnet: 'magnet:test', file: 0, pos: 1199.93 });
    });
    await page.waitForFunction(() => !document.getElementById('player-error').hidden, null, { timeout: 20000 });
    const failed = await page.evaluate(() => ({ ready: PP.ready(), position: PP.state().position,
      recoveries: fixture.recoveries, starts: fixture.starts.length, message: document.getElementById('player-error').textContent }));
    assert.equal(failed.recoveries.length, 2, 'a permanently bad fragment cannot cause an infinite recovery loop');
    assert.equal(failed.starts, 1);
    assert.equal(failed.ready, false);
    assert.match(failed.message, /Браузер.*видеофрагмент.*восстановление не помогло/);
    assert.equal(await page.locator('#loading-title').textContent(), 'Просмотр прерван');
    assert.match(await page.locator('#loading-detail').textContent(), /Браузер.*видеофрагмент.*восстановление не помогло/);
    assert.ok(failed.position >= 1199.93 + 1, 'the retry position survives final MSE teardown');
    assert.equal(stopped, 0);
    assert.deepEqual(pageErrors, []);
    console.log('PASS: persistent real MSE append failure stops after two recoveries and retains the retry position.');

    const retryState = await page.evaluate(() => PP.state());
    assert.equal(retryState.paused, true, 'terminal MSE teardown leaves a paused media element');
    persistentFailure = false;
    await page.locator('#loading-retry').click();
    await page.waitForFunction(position => PP.ready() && !document.getElementById('player').paused &&
      PP.state().position > position + 0.5, retryState.position, { timeout: 15000 });
    assert.equal(await page.evaluate(() => fixture.starts.length), 2, 'explicit retry starts exactly one replacement stream');
    assert.equal(await page.evaluate(() => PP.isFollower()), false, 'solo retry does not enter a room role');
    assert.equal(await page.locator('#loading-state').isVisible(), false);
    console.log('PASS: a solo viewer can resume with one explicit retry after terminal MSE failure.');

    // Spreading the paused state from a failed or stopped solo media element
    // expresses a new launch, not a remote host pause. A later intentional local
    // pause, however, must survive an actual asynchronous MSE recovery.
    // Check every status to detect even a brief incorrect host-pause message.
    badRecoveryFloor = await page.evaluate(() => fixture.recoveries.length);
    allowPausedFailure = true;
    const soloRestartPosition = await page.evaluate(() => {
      const video = document.getElementById('player');
      video.pause();
      const saved = PP.state();
      fixture.triggerPausedFailure = false;
      PP.start({ ...saved, pos: saved.position, paused: true });
      return saved.position;
    });
    await page.waitForFunction(position => PP.ready() && !document.getElementById('player').paused &&
      PP.state().position > position + 0.3, soloRestartPosition, { timeout: 15000 });
    const pausedPosition = await page.evaluate(() => {
      document.getElementById('player').pause();
      fixture.triggerPausedFailure = true;
      return PP.state().position;
    });
    await page.waitForFunction(floor => fixture.recoveries.length > floor && PP.ready(), badRecoveryFloor,
      { timeout: 15000 });
    const pausedRecovery = await page.evaluate(() => ({ state: PP.state(), stages: fixture.stages,
      starts: fixture.starts.length, recoveries: fixture.recoveries.length }));
    assert.equal(pausedRecovery.state.paused, true, 'an intentional local pause survives media recovery');
    assert.ok(Math.abs(pausedRecovery.state.position - pausedPosition) < 0.15, 'paused recovery keeps the local position');
    assert.equal(pausedRecovery.starts, 3);
    assert.equal(pausedRecovery.recoveries, badRecoveryFloor + 1);
    assert.ok(pausedRecovery.stages.every(stage => !/ведущ/i.test(stage.message || '')),
      'solo statuses never claim a host paused playback, including transient recovery statuses');
    assert.match(await page.locator('#loading-detail').textContent(), /Просмотр на паузе\./);
    await page.locator('#loading-retry').click();
    await page.waitForFunction(position => !document.getElementById('player').paused &&
      PP.state().position > position + 0.5, pausedPosition, { timeout: 10000 });
    assert.equal(await page.evaluate(() => fixture.starts.length), 3, 'resuming a local pause keeps its HLS stream');
    assert.deepEqual(pageErrors, []);
    console.log('PASS: solo restart ignores stale pause state, while a genuine local pause recovers without host-pause flicker.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
