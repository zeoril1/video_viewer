// Run with Playwright and Chromium installed; see docs/features.md.
const http = require("node:http");
const fs = require("node:fs");
const path = require("node:path");
const assert = require("node:assert/strict");
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || "playwright");
const root = path.join(__dirname, "../web");
const output = process.env.FEATURE_SCREENSHOTS || "/output";
fs.mkdirSync(output, { recursive: true });
let personal = [],
  approved = false;
let signedIn = true;
const requests = [];
const now = new Date(),
  month = now.toISOString().slice(0, 7),
  day = month + "-10";
const film = {
  id: "tt100",
  imdb_id: "tt100",
  tmdb_id: "100",
  title: "New Film",
  title_ru: "Новый фильм",
  kind: "feature",
  release_date: day,
  genres: ["Drama"],
  rating_tmdb: 8,
  plot_ru: "Описание фильма",
};
const series = {
  ...film,
  id: "tt200",
  imdb_id: "tt200",
  tmdb_id: "200",
  kind: "tvSeries",
  title: "Series",
  title_ru: "Новый сериал",
  seasons: 1,
};
const ep = {
  air_date: now.toISOString().slice(0, 10),
  season_number: 1,
  episode_number: 1,
  name: "Начало",
};
const server = http.createServer(async (req, res) => {
  let body = "";
  for await (const b of req) body += b;
  const u = new URL(req.url, "http://test");
  requests.push(u.pathname);
  res.setHeader("Content-Type", "application/json");
  const send = (data, code = 200) => {
    res.statusCode = code;
    res.end(code === 204 ? "" : JSON.stringify(data));
  };
  if (u.pathname === "/api/auth/me")
    return signedIn
      ? send({ user: { id: 1, username: "tester" } })
      : send({}, 401);
  if (u.pathname === "/api/auth/logout") {
    signedIn = false;
    return send(null, 204);
  }
  if (u.pathname === "/api/history") return send({ items: [] });
  if (u.pathname === "/api/personal") return send({ items: personal });
  if (u.pathname.startsWith("/api/personal/")) {
    const parts = u.pathname.split("/"),
      kind = parts[3],
      key = decodeURIComponent(parts[4]);
    personal = personal.filter((x) => x.kind !== kind || x.key !== key);
    if (req.method === "PUT")
      personal.push({
        kind,
        key,
        data: JSON.parse(body),
        updated_at: new Date().toISOString(),
      });
    return send(null, 204);
  }
  if (u.pathname === "/api/auth/device/start")
    return send({
      device_code: "a".repeat(64),
      user_code: "ABCD1234",
      expires_in: 600,
      interval: 1,
      verification_uri: "/device.html?mode=approve",
    });
  if (u.pathname === "/api/auth/device/approve") {
    approved = true;
    return send(null, 204);
  }
  if (u.pathname === "/api/auth/device/poll")
    return send({ status: approved ? "approved" : "pending" });
  if (u.pathname === "/api/discover/calendar") {
    const it = u.searchParams.get("media") === "tv" ? series : film;
    return send({
      items: [it],
      events:
        it === series
          ? [{ date: day, item: series, season: 1, episode: 1, name: "Начало" }]
          : [],
      total_pages: 1,
    });
  }
  if (u.pathname === "/api/discover/episodes")
    return send({
      episodes: ep.air_date.startsWith(u.searchParams.get("month")) ? [ep] : [],
    });
  if (u.pathname === "/api/discover/picks")
    return send({ items: [film, series], total_pages: 1 });
  if (u.pathname.endsWith("/explore"))
    return send({
      items: [series],
      cast: [{ id: 10, name: "Актёр" }],
      directors: [{ id: 20, name: "Режиссёр" }],
      trailer: "https://www.youtube.com/watch?v=abcdefghijk",
    });
  if (u.pathname.endsWith("/sources"))
    return send({ items: [], status: "ready", seasons: [] });
  if (u.pathname === "/api/films/tt100")
    return send({ ...film, poster_url: "" });
  if (u.pathname === "/api/films/tt200")
    return send({ ...series, poster_url: "" });
  if (u.pathname === "/api/iptv/channels")
    return send({
      channels: [
        {
          id: 1,
          name: "Первый",
          catchup_days: 7,
          play_url: "/api/iptv/play/1.m3u8",
        },
        { id: 2, name: "Второй" },
      ],
      groups: [],
      playlists: [],
      now: now.toISOString(),
    });
  if (u.pathname === "/api/iptv/guide")
    return send({
      channels: [
        {
          id: 1,
          name: "Первый",
          catchup_days: 7,
          programs: [
            {
              title: "Передача",
              start: new Date(now.getTime() - 3600000).toISOString(),
              stop: new Date(now.getTime() - 1800000).toISOString(),
            },
          ],
        },
      ],
      now: now.toISOString(),
      has_more: false,
    });
  if (u.pathname.startsWith("/api/"))
    return send({ items: [], sections: {}, genres: [] });
  const name = u.pathname === "/" ? "index.html" : u.pathname.slice(1);
  const full = path.resolve(root, name);
  if (!full.startsWith(root + path.sep) || !fs.existsSync(full)) {
    res.statusCode = 404;
    return res.end();
  }
  res.setHeader(
    "Content-Type",
    name.endsWith(".js")
      ? "application/javascript"
      : name.endsWith(".css")
        ? "text/css"
        : "text/html; charset=utf-8",
  );
  res.end(fs.readFileSync(full));
});
(async () => {
  await new Promise((r) => server.listen(0, "0.0.0.0", r));
  const base = "http://127.0.0.1:" + server.address().port;
  const browser = await chromium.launch({
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ["--no-sandbox"],
  });
  const page = await browser.newPage({
    viewport: { width: 1440, height: 1000 },
  });
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  try {
    await page.goto(base + "/film.html?id=tt100");
    await page
      .getByRole("button", { name: "＋ Смотреть позже", exact: true })
      .click();
    await page
      .getByRole("button", { name: "✓ В списке", exact: true })
      .waitFor();
    await page.locator("#film-explore .feature-carousel").waitFor();
    await page.locator("#trailer-toggle").waitFor();
    assert.ok(
      await page.getByRole("link", { name: "Актёр", exact: true }).count(),
    );
    await page.goto(base + "/library.html");
    await page.locator("#feature-content .feature-card").waitFor();
    await page.locator('[name="voice"]').fill("LostFilm");
    await page.locator('[name="max_quality"]').selectOption("720");
    await page.getByRole("button", { name: "Сохранить", exact: true }).click();
    await page.getByText("Предпочтения сохранены.", { exact: true }).waitFor();
    assert.equal(
      personal.find((x) => x.kind === "preferences").data.voice,
      "LostFilm",
    );
    await page.goto(base + "/film.html?id=tt200");
    await page
      .getByRole("button", { name: "Подписаться на новые серии", exact: true })
      .click();
    await page
      .getByRole("button", { name: "✓ Подписка оформлена", exact: true })
      .waitFor();
    await page.goto(base + "/library.html");
    await page
      .getByRole("button", { name: "Новые серии", exact: true })
      .click();
    await page
      .getByRole("button", { name: "Проверить нужную озвучку", exact: true })
      .waitFor();
    await page.goto(base + "/calendar.html");
    await page.locator(".calendar-event.movie").waitFor();
    await page.locator(".calendar-event.series").first().waitFor();
    await page.screenshot({
      path: path.join(output, "calendar-desktop.png"),
      fullPage: true,
    });
    await page.locator("#feature-toolbar select").first().selectOption("tv");
    await page.waitForFunction(
      () =>
        !document.querySelector(".calendar-event.movie") &&
        document.querySelector(".calendar-event.series"),
    );
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({
      path: path.join(output, "calendar-mobile.png"),
      fullPage: true,
    });
    await page.goto(base + "/discover.html");
    await page.locator(".feature-card").first().waitFor();
    await page.getByRole("button", { name: "Подобрать", exact: true }).click();
    await page.locator(".feature-card").first().waitFor();
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.goto(base + "/device.html");
    await page.getByRole("button", { name: "Получить код" }).click();
    await page.getByText("ABCD-1234", { exact: true }).waitFor();
    await page.goto(base + "/device.html?mode=approve");
    await page.locator('[name="code"]').fill("ABCD1234");
    await page
      .getByRole("button", { name: "Подтвердить вход на телевизоре" })
      .click();
    await page
      .getByText(
        "Вход подтверждён. Телевизор подключится через несколько секунд.",
        { exact: true },
      )
      .waitFor();
    await page.goto(base + "/iptv.html");
    await page.locator(".iptv-card").first().waitFor();
    await page
      .getByRole("button", { name: "В избранное", exact: true })
      .first()
      .click();
    await page
      .getByRole("button", { name: "★ Избранные", exact: true })
      .click();
    await page.waitForFunction(
      () => document.querySelectorAll(".iptv-card").length === 1,
    );
    await page
      .getByRole("button", { name: "Телепрограмма", exact: true })
      .click();
    await page.locator(".epg-table").waitFor();
    await page
      .locator('#iptv-guide input[type="datetime-local"]')
      .fill(new Date(now.getTime() - 2 * 3600000).toISOString().slice(0, 16));
    await page
      .locator('#iptv-guide input[type="datetime-local"]')
      .dispatchEvent("change");
    await page
      .getByRole("button", { name: /Передача/ })
      .first()
      .waitFor();
    await page.screenshot({
      path: path.join(output, "iptv-guide.png"),
      fullPage: true,
    });
    await page.goto(base + "/library.html");
    await page.locator("#feature-content .feature-card").waitFor();
    await page.locator(".profile-toggle").click();
    await page.getByRole("button", { name: "Выйти", exact: true }).click();
    await page.waitForFunction(
      () => document.querySelector("#feature-content").children.length === 0,
    );
    assert.deepEqual(errors, []);
    console.log(
      "Browser checks passed: library, preferences, subscriptions, calendar filters/mobile, discover, TV login, IPTV favourites/guide.",
    );
  } finally {
    await browser.close();
    server.close();
  }
})().catch((e) => {
  console.error(e);
  server.close();
  process.exitCode = 1;
});
