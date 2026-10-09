'use strict';

// Canonical lists come from the catalog's persistent TMDB cache. Torrent files
// are requested only when selecting a source for actual playback.
const SeriesCatalog = (() => {
  const records = new Map(), tasks = new Map();
  const maxEntries = 128, freshness = 5 * 60 * 1000;
  function key(id, season) { return id + (season === undefined ? '' : ':season:' + season); }
  function remember(k, data) {
    records.delete(k); records.set(k, {data, at:Date.now()});
    while (records.size > maxEntries) records.delete(records.keys().next().value);
  }
  function validSeasons(items) {
    return (Array.isArray(items) ? items : []).filter(s => s && Number.isInteger(s.season) && s.season > 0
      && Number.isInteger(s.episodes) && s.episodes > 0 && s.episodes <= 10000).sort((a,b) => a.season-b.season);
  }
  function validEpisodes(items) {
    return (Array.isArray(items) ? items : []).filter(e => e && Number.isInteger(e.episode) && e.episode > 0)
      .sort((a,b) => a.episode-b.episode);
  }
  async function read(id, season, onUpdate, force) {
    const k = key(id,season), stored = records.get(k);
    if (tasks.has(k)) return tasks.get(k).promise;
    if (!force && stored && Date.now()-stored.at < (stored.data.status === 'unavailable' ? 10000 : freshness)) return stored.data;
    const controller = new AbortController();
    const task = {controller, promise:null, canceled:false};
    tasks.set(k,task);
    task.promise = (async () => {
      const deadline = Date.now()+60000;
      let latest = stored ? stored.data : null;
      try {
        while (!controller.signal.aborted && Date.now() < deadline) {
          const timeout = setTimeout(() => controller.abort(),10000);
          let data;
          try {
            const url = '/api/films/'+encodeURIComponent(id)+'/seasons'+(season === undefined ? '' : '/'+season);
            const response = await fetch(url,{signal:controller.signal});
            if (!response.ok) throw new Error('metadata unavailable');
            data = await response.json();
          } finally { clearTimeout(timeout); }
          if (controller.signal.aborted) return latest;
          if (data.id && data.id !== id) throw new Error('wrong series metadata');
          const items = season === undefined ? validSeasons(data.seasons) : validEpisodes(data.episodes);
          // An unavailable refresh must not erase a previously displayed list.
          if (items.length) {
            const next = {...data, ...(season === undefined ? {seasons:items} : {season,episodes:items})};
            const changed = !latest || JSON.stringify(latest) !== JSON.stringify(next);
            latest = next; remember(k,next);
            if (changed && onUpdate) onUpdate(next);
          } else if (!latest && data.status !== 'loading') {
            latest = {...data, ...(season === undefined ? {seasons:[]} : {season,episodes:[]})};
            remember(k,latest); if (onUpdate) onUpdate(latest);
          }
          if (data.status !== 'loading' && !data.stale) return latest;
          await new Promise(resolve => {
            const stop = () => {clearTimeout(timer); controller.signal.removeEventListener('abort',stop); resolve();};
            const timer = setTimeout(stop,1500);
            controller.signal.addEventListener('abort',stop,{once:true});
          });
        }
      } catch (_) {
        if (!latest && !task.canceled) {
          latest = {id,status:'unavailable', ...(season === undefined ? {seasons:[]} : {season,episodes:[]})};
          remember(k,latest); if (onUpdate) onUpdate(latest);
        }
      } finally { if (tasks.get(k) === task) tasks.delete(k); }
      if (!latest && !task.canceled) {
        latest = {id,status:'unavailable', ...(season === undefined ? {seasons:[]} : {season,episodes:[]})};
        remember(k,latest); if (onUpdate) onUpdate(latest);
      }
      return latest;
    })();
    return task.promise;
  }
  function cancel() { for (const task of tasks.values()) {task.canceled = true; task.controller.abort();} tasks.clear(); }
  window.addEventListener('pagehide',cancel);
  return {
    load:(id,onUpdate,force=false) => read(id,undefined,onUpdate,force),
    loadEpisodes:(id,season,onUpdate,force=false) => Number.isInteger(season) && season > 0 ? read(id,season,onUpdate,force) : Promise.resolve(null),
    seasons:id => (records.get(key(id))?.data.seasons || []),
    episodes:(id,season) => (records.get(key(id,season))?.data.episodes || []),
    cancel,
  };
})();
