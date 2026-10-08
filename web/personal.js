"use strict";

// Account data stays on the server. Nothing private is kept in shared browser storage.
const Personal = (() => {
  let items = [],
    loading = null;
  async function request(path, options) {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 65000);
    try {
      const response = await fetch(path, {
        ...options,
        signal: controller.signal,
      });
      if (!response.ok)
        throw new Error(
          response.status === 401
            ? "Войдите в аккаунт, чтобы сохранить изменения."
            : response.status === 503 && path.includes("/api/discover/")
              ? "Данные новинок недоступны. Проверьте настройку TMDB на сервере."
              : "Не удалось выполнить запрос (" +
                response.status +
                "). Повторите попытку.",
        );
      return response.status === 204 ? null : await response.json();
    } finally {
      clearTimeout(timeout);
    }
  }
  async function load() {
    if (loading) return loading;
    loading = (async () => {
      if (!VV.user) {
        items = [];
        return;
      }
      const uid = VV.user.id;
      const data = await request("/api/personal");
      items = VV.user && VV.user.id === uid ? data.items || [] : [];
    })().finally(() => {
      loading = null;
      window.dispatchEvent(new Event("personalchange"));
    });
    return loading;
  }
  function list(kind) {
    return items.filter((x) => x.kind === kind);
  }
  function get(kind, key) {
    return items.find((x) => x.kind === kind && x.key === String(key));
  }
  async function put(kind, key, data) {
    const uid = VV.user && VV.user.id;
    await request("/api/personal/" + kind + "/" + encodeURIComponent(key), {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(data),
    });
    if (!VV.user || VV.user.id !== uid) return;
    items = items.filter((x) => !(x.kind === kind && x.key === String(key)));
    items.unshift({
      kind,
      key: String(key),
      data,
      updated_at: new Date().toISOString(),
    });
    window.dispatchEvent(new Event("personalchange"));
  }
  async function remove(kind, key) {
    const uid = VV.user && VV.user.id;
    await request("/api/personal/" + kind + "/" + encodeURIComponent(key), {
      method: "DELETE",
    });
    if (!VV.user || VV.user.id !== uid) return;
    items = items.filter((x) => !(x.kind === kind && x.key === String(key)));
    window.dispatchEvent(new Event("personalchange"));
  }
  const preferences = () => (get("preferences", "playback") || {}).data || {};
  function snapshot(it) {
    return {
      id: it.imdb_id || it.id,
      imdb_id: it.imdb_id || "",
      tmdb_id: it.tmdb_id || "",
      title: it.title || "",
      title_ru: it.title_ru || "",
      poster: it.poster || it.poster_url || "",
      kind: it.kind || "",
      year: it.year || 0,
      release_date: it.release_date || "",
    };
  }
  function quality(s) {
    const m = String(s.quality || s.title || "").match(
      /\b(2160|1080|720|480)p?\b/i,
    );
    return m ? Number(m[1]) : /\b4k\b/i.test(s.title || "") ? 2160 : 0;
  }
  function sizeGB(s) {
    const text = String(s.size || "").replace(",", ".");
    const m = text.match(
      /([\d.]+)\s*(TiB|TB|ТБ|GiB|GB|ГБ|MiB|MB|МБ|KiB|KB|КБ|bytes?|B|Б)?/i,
    );
    if (!m) return 0;
    const n = Number(m[1]),
      unit = (m[2] || "").toUpperCase();
    if (/^(T|Т)/.test(unit)) return n * 1024;
    if (/^(G|Г)/.test(unit)) return n;
    if (/^(M|М)/.test(unit)) return n / 1024;
    if (/^(K|К)/.test(unit)) return n / 1048576;
    return n / 1073741824;
  }
  function rank(sources, voice) {
    const p = preferences(),
      preferred = String(voice || p.voice || "").toLowerCase();
    return sources
      .filter(
        (s) =>
          (!p.max_quality || !quality(s) || quality(s) <= p.max_quality) &&
          (!p.max_size_gb || !sizeGB(s) || sizeGB(s) <= p.max_size_gb),
      )
      .map((s, i) => ({
        s,
        i,
        score:
          (preferred &&
          String(s.title || "")
            .toLowerCase()
            .includes(preferred)
            ? 1000000
            : 0) +
          ((s.seeds || 0) > 0 ? 100000 : 0) +
          quality(s) * 10 +
          Math.min(s.seeds || 0, 999),
      }))
      .sort((a, b) => b.score - a.score || a.i - b.i)
      .map((x) => x.s);
  }
  return {
    request,
    load,
    list,
    get,
    put,
    remove,
    preferences,
    snapshot,
    rank,
    quality,
    sizeGB,
  };
})();

(() => {
  if (typeof onAuth === "function")
    onAuth(() => Personal.load().catch(() => {}));
})();
