"use strict";
(() => {
  if (!PP.available) return;
  const video = document.getElementById("player"),
    box = document.createElement("div");
  box.className = "recovery-offer";
  box.hidden = true;
  box.setAttribute("role", "status");
  document.getElementById("player-wrap").after(box);
  const text = document.createElement("p"),
    button = document.createElement("button");
  button.className = "auth-btn";
  button.textContent = "Попробовать другой источник";
  box.append(text, button);
  let stalled = 0,
    busy = false,
    generation = 0,
    lastKey = "",
    tried = new Set(),
    attempt = null;
  function offer() {
    if (PP.isFollower()) {
      box.hidden = true;
      return;
    }
    if (PP.playing() && !busy) {
      box.hidden = false;
      text.textContent =
        "Воспроизведение задерживается: видео не поступает в плеер вовремя. Даже скачанному файлу нужно соединение с сервером. Можно выбрать другой источник с сохранением позиции.";
    }
  }
  window.addEventListener("playbackstart", () => {
    generation++;
    if (attempt) attempt.abort();
    attempt = null;
    const s = PP.state(),
      key = s.id + ":" + s.season + ":" + s.episode;
    if (key !== lastKey) {
      tried.clear();
      lastKey = key;
    }
    tried.add(s.magnet);
    stalled = Date.now();
    box.hidden = true;
  });
  window.addEventListener("playbackfailure", offer);
  window.addEventListener("requestsourcechange", () => {
    if (PP.isFollower()) return;
    offer();
    button.click();
  });
  window.addEventListener("playbackrole", () => {
    button.disabled = busy || PP.isFollower();
    if (!PP.isFollower()) return;
    generation++;
    box.hidden = true;
    if (attempt) attempt.abort();
  });
  video.addEventListener("error", offer);
  video.addEventListener("waiting", () => {
    if (!stalled) stalled = Date.now();
  });
  video.addEventListener("playing", () => {
    stalled = 0;
    box.hidden = true;
  });
  let lastTime = 0;
  video.addEventListener("timeupdate", () => {
    if (video.currentTime !== lastTime) {
      lastTime = video.currentTime;
      stalled = 0;
    }
  });
  const interval = setInterval(() => {
    if (
      !document.hidden &&
      stalled &&
      Date.now() - stalled > 25000 &&
      !video.ended
    )
      offer();
  }, 2000);
  button.onclick = async () => {
    if (busy || PP.isFollower()) return;
    busy = true;
    button.disabled = true;
    const token = generation,
      original = PP.state();
    tried.add(original.magnet);
    text.textContent = "Ищу другой источник той же серии…";
    attempt = new AbortController();
    const signal = attempt.signal;
    try {
      let sources = [];
      const deadline = Date.now() + 90000;
      while (Date.now() < deadline) {
        const response = await fetch(
          "/api/films/" + encodeURIComponent(original.id) + "/sources",
          { signal },
        );
        if (!response.ok)
          throw new Error("Поиск источников временно недоступен.");
        const data = await response.json();
        sources = Personal.rank(
          (data.items || data.sources || []).filter(
            (x) => !tried.has(x.magnet),
          ),
          original.voice,
        );
        if (sources.length || data.status !== "searching") break;
        await new Promise((resolve) => setTimeout(resolve, 2000));
        if (token !== generation || PP.isFollower()) return;
      }
      for (const source of sources.slice(0, 6)) {
        if (token !== generation || PP.isFollower()) return;
        tried.add(source.magnet);
        const files = await fetchFiles(
          original.id,
          source.magnet,
          source.title,
          { signal },
        );
        if (token !== generation || PP.isFollower()) return;
        const file =
          original.season && original.episode
            ? (files || []).find(
                (f) =>
                  f.season === original.season &&
                  f.episode === original.episode,
              )
            : (files || [])
                .slice()
                .sort(
                  (a, b) =>
                    (b.length || b.size || 0) - (a.length || a.size || 0),
                )[0];
        if (!file) continue;
        const latest = PP.state();
        if (latest.magnet !== original.magnet) return;
        PP.start({
          id: original.id,
          magnet: source.magnet,
          release: source.title,
          file: file.index,
          season: original.season,
          ep: original.episode,
          voice: original.voice,
          pos: Math.max(0, latest.position || original.position || 0),
          quality: "source",
        });
        const p = playbackPageParams();
        p.set('id', original.id);
        p.set("magnet", source.magnet);
        p.set("file", String(file.index));
        p.set("rt", source.title || "");
        if (original.season) p.set('season', String(original.season));
        if (original.episode) p.set('ep', String(original.episode));
        if (original.voice) p.set('voice', original.voice);
        p.set('autoplay', '1');
        p.set(
          "pos",
          String(Math.max(0, latest.position || original.position || 0)),
        );
        savePlaybackPage(p);
        box.hidden = true;
        return;
      }
      text.textContent =
        "Других подходящих источников не найдено. Можно изменить предпочтения или выбрать раздачу вручную.";
    } catch (e) {
      if (e.name !== "AbortError") text.textContent = e.message;
    } finally {
      busy = false;
      button.disabled = PP.isFollower();
    }
  };
  window.addEventListener("pagehide", () => {
    generation++;
    clearInterval(interval);
    if (attempt) attempt.abort();
  });
})();
