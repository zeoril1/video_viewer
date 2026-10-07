const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const code = fs.readFileSync(path.join(__dirname,'../web/series-catalog.js'),'utf8');
function setup(fetcher) {
  const context = vm.createContext({AbortController,setTimeout,clearTimeout,Date,fetch:fetcher,
    window:{addEventListener(){}}, encodeURIComponent});
  vm.runInContext(code+'\nglobalThis.catalog=SeriesCatalog;',context);
  return context.catalog;
}
const result = data => ({ok:true,json:async()=>data});
test('canonical catalog coalesces requests and an unavailable refresh preserves persisted rows', async () => {
  let calls=0, complete;
  const catalog=setup(async()=>{calls++;if(calls===1)return new Promise(resolve=>{complete=resolve;});return result({id:'tt1',status:'unavailable',seasons:[]});});
  const updates=[];
  const first=catalog.load('tt1',x=>updates.push(x));
  const second=catalog.load('tt1');
  complete(result({id:'tt1',status:'ready',seasons:[{season:1,episodes:3},{season:2,episodes:2}]}));
  await Promise.all([first,second]);
  assert.equal(calls,1);
  assert.equal(catalog.seasons('tt1').length,2);
  await catalog.load('tt1',x=>updates.push(x),true);
  assert.equal(calls,2);
  assert.equal(catalog.seasons('tt1')[0].episodes,3);
  assert.equal(updates.length,1);
});
test('selected season details are lazy, validated and cached independently',async()=>{
  const calls=[];
  const catalog=setup(async url=>{calls.push(url);return result({id:'tt1',status:'ready',episodes:[{episode:2,name:'Two'},{episode:1,name:'One'},{episode:-1}]});});
  assert.equal(catalog.episodes('tt1',1).length,0);
  await catalog.loadEpisodes('tt1',2);
  await catalog.loadEpisodes('tt1',2);
  assert.deepEqual(calls,['/api/films/tt1/seasons/2']);
  assert.deepEqual(Array.from(catalog.episodes('tt1',2),e=>e.episode),[1,2]);
  assert.equal(catalog.episodes('tt1',1).length,0);
});
test('cold transport failure exits loading while canceled navigation cannot publish results',async()=>{
  const updates=[];
  const offline=setup(async()=>{throw new Error('offline');});
  await offline.load('tt1',x=>updates.push(x));
  assert.equal(updates[0].status,'unavailable');
  let complete;
  const canceled=setup(async()=>new Promise(resolve=>{complete=resolve;}));
  const pending=canceled.load('tt2',x=>updates.push(x));
  canceled.cancel();
  complete(result({id:'tt2',status:'ready',seasons:[{season:1,episodes:3}]}));
  await pending;
  assert.equal(updates.length,1);
  assert.equal(canceled.seasons('tt2').length,0);
});
