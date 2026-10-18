"use strict";

(() => {
  const box = document.getElementById("telegram-notifications");
  if (!box) return;
  const endpoint = "/api/notifications/telegram";
  const { el } = FeatureUI;
  const text = (ru, en) => VV.lang === "en" ? en : ru;
  let generation = 0, state = null, busy = false, error = "", pending = null;

  function alive(token, owner) {
    return token === generation && VV.user && VV.user.id === owner;
  }
  function linkURL(value) {
    const url = new URL(value);
    const username = url.pathname.slice(1);
    if (url.protocol !== "https:" || url.hostname !== "t.me" || url.port ||
        url.username || url.password || url.hash || !/^[a-z0-9_]{5,32}$/i.test(username) ||
        (state?.bot_username && username.toLowerCase() !== state.bot_username.replace(/^@/, "").toLowerCase()) ||
        !/^[a-z0-9_-]{1,64}$/i.test(url.searchParams.get("start") || "") ||
        [...url.searchParams.keys()].some(key => key !== "start") || url.searchParams.getAll("start").length !== 1)
      throw new Error(text("Не удалось получить ссылку Telegram. Повторите попытку.", "Could not create a Telegram link. Please try again."));
    return url.href;
  }
  async function run(operation = "refresh") {
    if (!VV.user || busy) return;
    const token = ++generation, owner = VV.user.id;
    busy = true;
    error = "";
    render();
    try {
      if (operation === "link") {
        const data = await Personal.request(endpoint + "/link", { method: "POST" });
        if (!alive(token, owner)) return;
        pending = { url: linkURL(data.url), expires: Date.now() + Math.min(600, Math.max(1, Number(data.expires_in) || 600)) * 1000 };
      } else {
        if (operation !== "refresh") {
          await Personal.request(endpoint, operation === "disconnect" ? { method: "DELETE" } : {
            method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled: operation === "resume" }),
          });
          if (!alive(token, owner)) return;
          // The mutation has succeeded even if refreshing its status later fails.
          state = operation === "disconnect" ? { ...state, linked: false, enabled: false, username: "" } : { ...state, enabled: operation === "resume" };
          pending = null;
        }
        const data = await Personal.request(endpoint);
        if (!alive(token, owner)) return;
        state = data;
        if (state.linked) pending = null;
      }
    } catch (e) {
      if (alive(token, owner)) error = e.message || text("Не удалось обновить настройки. Повторите попытку.", "Could not update settings. Please try again.");
    } finally {
      if (alive(token, owner)) {
        busy = false;
        render();
      }
    }
  }
  function action(label, operation, primary = false) {
    const b = el("button", label, "auth-btn" + (primary ? " auth-btn-primary" : ""));
    b.type = "button";
    b.dataset.telegramAction = operation;
    b.disabled = busy;
    b.addEventListener("click", () => run(operation));
    return b;
  }
  function render() {
    const focus = document.activeElement?.dataset?.telegramAction;
    box.hidden = !VV.user;
    box.setAttribute("aria-busy", String(busy));
    box.replaceChildren();
    if (!VV.user) return;
    const heading = el("div", undefined, "telegram-heading"), title = el("h2", text("Уведомления в Telegram", "Telegram notifications"));
    title.id = "telegram-heading";
    heading.append(title);
    if (state?.linked) heading.append(el("span", state.enabled ? text("Включены", "Enabled") : text("Приостановлены", "Paused"), "telegram-badge" + (state.enabled ? " enabled" : "")));
    box.append(heading, el("p", text(
      "Проверяем ваши подписки каждые 6 часов. Сообщим о выходе фильма или новых серий, укажем качество и озвучку. Если качество улучшится или появится новая озвучка, пришлём обновление.",
      "We check your subscriptions every 6 hours. Get notified about films and new episodes, including quality and audio language or dub. We will also notify you when quality improves or a new dub appears.",
    )));
    const actions = el("div", undefined, "telegram-actions");
    if (state && !state.configured) {
      box.append(el("p", text("Telegram-бот ещё не настроен. Попросите администратора сайта подключить уведомления.", "The Telegram bot is not set up yet. Ask the site administrator to enable notifications."), "telegram-status"));
    } else if (state?.linked) {
      box.append(el("p", text("Telegram подключён", "Telegram connected") + (state.username ? " · @" + state.username.replace(/^@/, "") : "") + ".", "telegram-status"));
      actions.append(action(state.enabled ? text("Приостановить", "Pause") : text("Возобновить", "Resume"), state.enabled ? "pause" : "resume"), action(text("Отключить Telegram", "Disconnect Telegram"), "disconnect"));
    } else if (state?.configured) {
      if (pending && pending.expires > Date.now()) {
        const link = el("a", text("Открыть бота в Telegram", "Open bot in Telegram"), "auth-btn auth-btn-primary telegram-connect");
        link.href = pending.url;
        link.target = "_blank";
        link.rel = "noopener noreferrer";
        actions.append(link, action(text("Я подключил — проверить", "I connected — check"), "refresh"));
        box.append(el("p", text("Откройте бота и нажмите «Запустить». Ссылка действует 10 минут. Затем проверьте подключение здесь.", "Open the bot and press Start. The link is valid for 10 minutes. Then check the connection here."), "telegram-help"));
      } else actions.append(action(text("Подключить Telegram", "Connect Telegram"), "link", true));
    }
    // A refresh also recovers from temporary errors and unconfigured server state.
    if (!pending || pending.expires <= Date.now() || state?.linked)
      actions.append(action(text("Обновить статус", "Refresh status"), "refresh"));
    box.append(actions, el("p", text("Добавляйте фильмы и сериалы в «Подписки» на их страницах. Уведомления приходят только по вашим подпискам.", "Follow films and series on their pages. Notifications cover only your subscriptions."), "telegram-help"));
    if (error || (!state && busy)) {
      const status = el("p", error || text("Проверяю подключение…", "Checking connection…"), "telegram-status" + (error ? " error" : ""));
      status.setAttribute("role", error ? "alert" : "status");
      box.append(status);
    }
    if (focus) box.querySelector('[data-telegram-action="' + focus + '"]')?.focus({ preventScroll: true });
  }
  function accountChanged() {
    generation++;
    state = pending = null;
    busy = false;
    error = "";
    render();
    if (VV.user) run();
  }
  onAuth(accountChanged);
  onLang(render);
  window.addEventListener("focus", () => {
    if (pending && pending.expires > Date.now()) run();
  });
  if (VV.user) accountChanged();
})();
