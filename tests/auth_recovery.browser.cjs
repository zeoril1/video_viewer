// Real shared.js in Chromium, deterministic auth/history transport; no external services.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const root = path.join(__dirname, '../web');
const html = `<!doctype html><html lang="ru"><head><link rel="stylesheet" href="/style.css"></head><body>
<header class="header"><div class="user-area" id="user-area" hidden><span id="user-name" class="user-name"></span>
<a id="admin-link" hidden>Админка</a><button id="auth-logout" type="button">Выйти</button></div>
<a id="auth-open" href="/login.html" hidden>Войти</a></header><div id="history"></div>
<script src="/shared.js"></script><script>onAuth(()=>{document.getElementById('history').textContent=JSON.stringify(VV.history)});initAuth();</script>
</body></html>`;

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH });
  try {
    const context = await browser.newContext();
    let state = 'available', held = null, pauseResponse = false, authCalls = 0;
    const errors = [];
    await context.route('**/*', async route => {
      const url = new URL(route.request().url());
      const send = (data, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(data) });
      if (url.pathname === '/api/auth/me') {
        authCalls++;
        if (pauseResponse) { held = route; return; }
        if (state === 'outage') return send({ code: 'auth_unavailable' }, 503);
        if (state === 'expired') return send({ error: 'unauthorized' }, 401);
        if (state === 'disabled') return send({ code: 'auth_disabled' }, 503);
        return send({ user: { id: 7, username: 'viewer', role: 'user' } });
      }
      if (url.pathname === '/api/auth/logout') return route.fulfill({ status: 204 });
      if (url.pathname === '/api/auth/login') return send({ code: state === 'disabled' ? 'auth_disabled' : 'auth_unavailable' }, 503);
      if (url.pathname === '/api/history') return send({ items: [{ film_id: 'tt1', position: 42 }] });
      if (url.pathname === '/shared.js' || url.pathname === '/style.css') return route.fulfill({
        contentType: url.pathname.endsWith('.js') ? 'text/javascript' : 'text/css',
        body: fs.readFileSync(path.join(root, url.pathname.slice(1)), 'utf8'),
      });
      if (url.pathname === '/') return route.fulfill({ contentType: 'text/html', body: html });
      if (/\.(html|js|css)$/.test(url.pathname)) {
        const file = path.join(root, path.basename(url.pathname));
        if (fs.existsSync(file)) return route.fulfill({
          contentType: url.pathname.endsWith('.js') ? 'text/javascript' : url.pathname.endsWith('.css') ? 'text/css' : 'text/html',
          body: fs.readFileSync(file, 'utf8'),
        });
      }
      return route.fulfill({ status: 404 });
    });
    const page = await context.newPage();
    page.on('pageerror', error => errors.push(error.message));
    await page.goto('http://kinoteka.test/');
    await page.waitForFunction(() => VV.user && VV.history.length === 1);
    assert.equal(await page.locator('#user-name').textContent(), 'viewer');

    state = 'outage';
    await page.evaluate(() => initAuth());
    assert.equal(await page.evaluate(() => VV.user.id), 7);
    assert.equal(await page.evaluate(() => VV.history[0].position), 42);
    assert.equal(await page.locator('#auth-open').isHidden(), true);
    assert.equal(await page.locator('#auth-status').isVisible(), true);
    const beforeRecovery = authCalls;
    state = 'available';
    await page.waitForFunction(() => document.getElementById('auth-status').hidden, null, { timeout: 6000 });
    assert(authCalls > beforeRecovery, 'scheduled check did not run after the database recovered');
    assert.equal(await page.evaluate(() => VV.user.id), 7);

    state = 'expired';
    await page.evaluate(() => initAuth());
    assert.equal(await page.evaluate(() => VV.user), null);
    assert.equal(await page.locator('#auth-open').isVisible(), true);
    assert.equal(await page.evaluate(() => localStorage.getItem('vv_user')), null);

    state = 'available';
    await page.evaluate(() => initAuth());
    pauseResponse = true;
    await page.evaluate(() => { window.pendingAuth = initAuth(); });
    await page.waitForFunction(() => authRequest !== null);
    while (!held) await new Promise(resolve => setTimeout(resolve, 10));
    await page.locator('#auth-logout').click();
    assert.equal(await page.evaluate(() => VV.user), null);
    await held.fulfill({ contentType: 'application/json', body: JSON.stringify({ user: { id: 7, username: 'viewer' } }) }).catch(() => {});
    await page.evaluate(() => window.pendingAuth);
    assert.equal(await page.evaluate(() => VV.user), null, 'late identity response resurrected the logged-out user');

    pauseResponse = false;
    state = 'disabled';
    await page.evaluate(() => initAuth());
    assert.equal(await page.locator('#auth-open').isHidden(), true);
    assert.equal(await page.locator('#auth-status').isHidden(), true);

    // The actual sign-in page explains an outage without claiming authentication is disabled.
    state = 'outage';
    await page.goto('http://kinoteka.test/login.html');
    await page.locator('#auth-username').fill('viewer');
    await page.locator('#auth-password').fill('password123');
    await page.locator('#auth-submit').click();
    await page.waitForFunction(() => document.getElementById('auth-error').textContent.includes('временно'));
    assert.equal(await page.locator('#auth-submit').isEnabled(), true);
    state = 'disabled';
    await page.locator('#auth-submit').click();
    await page.waitForFunction(() => document.getElementById('auth-error').textContent.includes('отключена'));
    assert.deepEqual(errors, []);
    console.log('Auth browser scenarios passed: outage preserves user/history, automatic recovery, expired session, logout with pending check, disabled service, code-aware sign-in failures.');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
