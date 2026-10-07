const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../web/skip-segments.js'), 'utf8');
const mediaKey = 'segments.'+'a'.repeat(40)+'.0';

// Run the full UI module: only browser primitives, media and the account API
// are replaced, so these tests exercise its event and persistence behavior.
function setup({guest = false, fetcher} = {}) {
  const elements = new Map(), events = new Map(), preferences = new Map(), writes = [], requests = [], seeks = [];
  let now = 100000, follower = false;
  class Element {
    constructor(tag = 'div') {this.tagName = tag.toUpperCase();this.hidden = false;this.disabled = false;this.children = [];this.listeners = new Map();this.value = '';this.attributes = {};this.textContent = '';}
    set innerHTML(html) {
      this.children = [];
      for (const m of html.matchAll(/<([\w-]+)[^>]*\bid="([^"]+)"[^>]*>/g)) {
        const node = new Element(m[1]); node.id = m[2]; node.hidden = /\bhidden\b/.test(m[0]);
        elements.set(node.id,node);this.children.push(node);
      }
    }
    setAttribute(key,value) {this.attributes[key] = String(value);}
    append(...nodes) {for (const node of nodes) {if (node.id) elements.set(node.id,node);this.children.push(node);}}
    replaceChildren(...nodes) {this.children = nodes;}
    querySelectorAll() {return this.children.flatMap(node => [node,...node.querySelectorAll()]).filter(node => ['INPUT','SELECT','BUTTON'].includes(node.tagName));}
    addEventListener(name,fn) {if (!this.listeners.has(name)) this.listeners.set(name,[]);this.listeners.get(name).push(fn);}
    dispatch(name) {const event = {key:name,target:this,preventDefault(){},stopPropagation(){}};for (const fn of this.listeners.get(name)||[]) fn(event);if (this['on'+name]) this['on'+name](event);}
    focus() {document.activeElement = this;}
  }
  const documentListeners = new Map();
  const document = {createElement:tag => new Element(tag),getElementById:id => elements.get(id),body:{classList:{contains:() => follower}},
    addEventListener(name,fn) {if(!documentListeners.has(name))documentListeners.set(name,[]);documentListeners.get(name).push(fn);}};
  const video = new Element('video');video.paused = false;video.currentTime = 0;
  const wrap = new Element();elements.set('player',video);elements.set('player-wrap',wrap);
  const state = {id:'tt1',magnet:'magnet:first',file:0,position:0,duration:300,season:1,episode:1,playing:true};
  const VV = {user:guest ? null : {id:'one'}};
  const window = {addEventListener(name,fn) {if (!events.has(name)) events.set(name,[]);events.get(name).push(fn);},dispatchEvent(event) {for(const fn of events.get(event.type)||[])fn(event);}};
  const emit = (type,detail) => window.dispatchEvent({type,detail});
  class Clock extends Date {static now(){return now;}}
  const PP = {available:true,state:() => ({...state}),duration:() => state.duration,isFollower:() => follower,ready:() => true,
    seek(target) {seeks.push(target);state.position = target;},skipSegment(target) {seeks.push(target);state.position=target;return target>=state.duration;}};
  const Personal = {get:(kind,key) => preferences.get(VV.user?.id+'|'+kind+'|'+key),async put(kind,key,data) {
    writes.push({owner:VV.user.id,kind,key,data:JSON.parse(JSON.stringify(data))});preferences.set(VV.user.id+'|'+kind+'|'+key,{data});emit('personalchange');
  }};
  const context = vm.createContext({document,window,PP,VV,Personal,Date:Clock,URLSearchParams,AbortController,
    currentItem:{tmdb_id:'99'},setTimeout:() => 1,clearTimeout(){},
    fetch:async (url,options) => {requests.push({url,options});return fetcher ? fetcher(url,options) : {ok:true,json:async () => ({status:'not_found',segments:[]})};}});
  vm.runInContext(source,context);
  const tracks = segments => emit('playbacksegments',{segments,media_key:mediaKey,duration:state.duration,identity:{...state}});
  const change = (id,value) => {const node=elements.get(id);node.value=value;node.dispatch('change');};
  const listButtons = () => elements.get('skip-segments-list').querySelectorAll();
  return {document,el:id=>elements.get(id),video,wrap,state,VV,PP,emit,tracks,change,seeks,writes,requests,preferences,listButtons,
    key(key,target) {const event={key,target,preventDefault(){},stopPropagation(){}};for(const fn of documentListeners.get('keydown')||[])fn(event);},
    now:value=>{now=value;},follower:value=>{follower=value;emit('playbackrole');},
    submit(type,start,end) {elements.get('skip-edit-type').value=type;elements.get('skip-edit-start').value=start;elements.get('skip-edit-end').value=end;elements.get('skip-segments-form').dispatch('submit');}};
}
const flush = async () => {for(let i=0;i<12;i++)await Promise.resolve();};

test('skip uses absolute playback time and undo does not trigger an automatic loop', () => {
  const h = setup({guest:true});h.state.position=135;h.video.currentTime=35;
  h.tracks([{type:'intro',start:100,end:150,auto_skip:true}]);
  assert.equal(h.el('skip-segment').hidden,false);assert.equal(h.seeks.length,0);
  h.change('skip-mode-intro','auto');assert.deepEqual(h.seeks,[150]);
  h.el('skip-segment-undo').dispatch('click');assert.deepEqual(h.seeks,[150,135]);
  h.now(110000);h.video.dispatch('timeupdate');assert.deepEqual(h.seeks,[150,135]);
});

test('an intentional backward seek suppresses automatic skipping on that revisit', () => {
  const h=setup({guest:true});h.state.position=60;h.tracks([{type:'intro',start:20,end:50,auto_skip:true}]);
  h.change('skip-mode-intro','auto');h.state.position=25;h.video.dispatch('timeupdate');
  assert.deepEqual(h.seeks,[]);assert.equal(h.el('skip-segment').hidden,false);
  h.el('skip-segment').dispatch('click');assert.deepEqual(h.seeks,[50]);
});

test('multiple credits preserve the scene between blocks and terminal credits use normal completion', () => {
  const h=setup({guest:true});h.state.position=230;
  h.tracks([{type:'credits',start:220,end:240,auto_skip:true},{type:'credits',start:270,end:300,auto_skip:true}]);
  h.el('skip-segment').dispatch('click');assert.deepEqual(h.seeks,[240]);
  h.state.position=250;h.video.dispatch('timeupdate');assert.equal(h.el('skip-segment').hidden,true);
  h.state.position=280;h.video.dispatch('timeupdate');assert.equal(h.el('skip-segment').textContent,'Следующая серия');
  h.el('skip-segment').dispatch('click');assert.deepEqual(h.seeks,[240,300]);assert.equal(h.el('skip-segment-undo').hidden,true);
});

test('external candidates cannot auto skip until their own boundaries are confirmed', async () => {
  const h=setup({fetcher:async () => ({ok:true,json:async () => ({status:'ready',segments:[{type:'intro',start:10,end:40,auto_skip:true}]})})});
  h.state.position=20;h.tracks([]);h.change('skip-mode-intro','auto');await flush();
  assert.deepEqual(h.seeks,[]);assert.match(h.el('skip-segment-hint').textContent,/Проверьте/);
  h.el('skip-segments-open').dispatch('click');h.listButtons().find(b=>b.textContent==='Подтвердить').dispatch('click');await flush();
  assert.equal(h.writes.at(-1).key,mediaKey);assert.equal(h.writes.at(-1).data.segments[0].verified,true);
  h.el('skip-segments-close').dispatch('click');h.video.dispatch('timeupdate');assert.deepEqual(h.seeks,[40]);
});

test('stored audio marks load for an exact file after reload and can automatically skip', async () => {
  const fetcher=async url=>{
    assert.equal(new URL(url,'http://local').searchParams.get('media_key'),mediaKey);
    return {ok:true,json:async()=>({status:'ready',media_key:mediaKey,segments:[{type:'intro',start:10,end:40,source:'audio_match',auto_skip:true}]})};
  };
  for(let reload=0;reload<2;reload++){
    const h=setup({fetcher});h.state.position=20;h.tracks([]);await flush();h.change('skip-mode-intro','auto');
    assert.deepEqual(h.seeks,[40]);assert.match(h.el('skip-segments-source-status').textContent,/этого файла/);
    assert.equal(h.listButtons().some(b=>b.textContent==='Подтвердить'),false);
  }
});

test('audio candidates respect server auto_skip=false until the user confirms their boundaries',async()=>{
  const h=setup({fetcher:async()=>({ok:true,json:async()=>({status:'ready',media_key:mediaKey,
    segments:[{type:'intro',start:10,end:40,source:'audio_match',auto_skip:false,verified:true}]})})});
  h.state.position=20;h.tracks([]);h.change('skip-mode-intro','auto');await flush();
  assert.deepEqual(h.seeks,[]);assert.match(h.el('skip-segment-hint').textContent,/распознанные/);
  h.el('skip-segments-open').dispatch('click');h.listButtons().find(b=>b.textContent==='Подтвердить').dispatch('click');await flush();
  h.el('skip-segments-close').dispatch('click');h.video.dispatch('timeupdate');assert.deepEqual(h.seeks,[40]);
});

test('audio marks for another file cannot leak into playback and manual overrides keep precedence', async () => {
  const h=setup({fetcher:async()=>({ok:true,json:async()=>({status:'ready',media_key:mediaKey.replace(/\.0$/,'.1'),
    segments:[{type:'intro',start:10,end:40,source:'audio_match',auto_skip:true}]})})});
  h.state.position=20;h.tracks([]);await flush();h.change('skip-mode-intro','auto');
  assert.deepEqual(h.seeks,[]);assert.equal(h.el('skip-segment').hidden,true);
  const manual=setup({fetcher:async()=>({ok:true,json:async()=>({status:'ready',media_key:mediaKey,
    segments:[{type:'intro',start:10,end:40,source:'audio_match',auto_skip:true}]})})});
  manual.preferences.set('one|preferences|'+mediaKey,{data:{overridden_types:['intro'],segments:[{type:'intro',start:5,end:15,source:'manual',verified:true}]}});
  manual.state.position=20;manual.tracks([]);await flush();manual.change('skip-mode-intro','auto');
  assert.deepEqual(manual.seeks,[]);assert.equal(manual.el('skip-segment').hidden,true);
});

test('finishing next-episode analysis refreshes current marks but stale completion does not', async () => {
  let ready=false;
  const h=setup({fetcher:async()=>({ok:true,json:async()=>({status:ready?'ready':'not_found',media_key:mediaKey,
    segments:ready?[{type:'intro',start:10,end:40,source:'audio_match',auto_skip:true}]:[]})})});
  h.state.position=20;h.tracks([]);await flush();h.change('skip-mode-intro','auto');
  ready=true;h.emit('segmentsrefresh',{identity:{id:'tt1',magnet:'stale',file:0}});await flush();assert.equal(h.requests.length,1);
  h.emit('segmentsrefresh',{identity:{...h.state}});await flush();assert.equal(h.requests.length,2);assert.deepEqual(h.seeks,[40]);
});

test('manual marks take precedence and empty overrides suppress a deleted automatic type', async () => {
  const h=setup();h.state.position=20;h.tracks([{type:'intro',start:10,end:30,auto_skip:true}]);await flush();
  h.submit('intro','0:12','0:25.5');await flush();
  assert.equal(h.writes.at(-1).key,mediaKey);assert.equal(h.writes.at(-1).data.segments[1].end,25.5);
  const buttons=h.listButtons().filter(b=>b.textContent==='Удалить');
  buttons[0].dispatch('click');await flush();h.listButtons().find(b=>b.textContent==='Удалить').dispatch('click');await flush();
  assert.equal(h.el('skip-segment').hidden,true);assert.deepEqual(h.writes.at(-1).data.overridden_types,['intro']);
  assert.deepEqual(h.writes.at(-1).data.segments,[]);
  h.tracks([{type:'intro',start:10,end:30,auto_skip:true}]);assert.equal(h.el('skip-segment').hidden,true);
  h.listButtons().find(b=>b.textContent.startsWith('Восстановить')).dispatch('click');await flush();assert.equal(h.el('skip-segment').hidden,false);
});

test('chapter marks outrank external marks only for the same type', async () => {
  const h=setup({fetcher:async () => ({ok:true,json:async () => ({status:'ready',segments:[{type:'intro',start:10,end:30},{type:'recap',start:2,end:8}]})})});
  h.state.position=15;h.tracks([{type:'intro',start:100,end:130,auto_skip:true}]);await flush();assert.equal(h.el('skip-segment').hidden,true);
  h.state.position=4;h.video.dispatch('timeupdate');assert.equal(h.el('skip-segment').textContent,'Пропустить пересказ');
  h.state.position=105;h.video.dispatch('timeupdate');assert.equal(h.el('skip-segment').textContent,'Пропустить заставку');
});

test('range validation rejects invalid and out-of-file marks without any write', () => {
  const h=setup();h.tracks([]);
  for(const [start,end] of [['1:80','3:00'],['20','10'],['0','301'],['x','30'],['','30']])h.submit('intro',start,end);
  assert.equal(h.writes.length,0);assert.match(h.el('skip-segments-status').textContent,/корректное/);
  h.state.position=75.5;h.el('skip-capture-start').dispatch('click');assert.equal(h.el('skip-edit-start').value,'1:15.5');
});

test('follower controls cannot seek, save marks or auto skip; the editor can still close', () => {
  const h=setup();h.state.position=20;h.tracks([{type:'intro',start:10,end:30,auto_skip:true}]);h.follower(true);
  h.change('skip-mode-intro','auto');h.el('skip-segment').dispatch('click');h.submit('intro','11','25');h.video.dispatch('timeupdate');
  assert.deepEqual(h.seeks,[]);assert.equal(h.writes.length,0);assert.equal(h.el('skip-segment').disabled,true);
  h.el('skip-segments-open').dispatch('click');assert.equal(h.el('skip-segments-close').disabled,false);
  h.el('skip-segments-close').dispatch('click');assert.equal(h.el('skip-segments-panel').hidden,true);
});

test('account changes clear transient settings and exact-file guest marks', async () => {
  const h=setup({guest:true});h.tracks([]);h.change('skip-mode-intro','off');h.submit('intro','10','30');
  assert.equal(h.writes.length,0);h.VV.user={id:'two'};h.emit('personalchange');
  assert.equal(h.el('skip-mode-intro').value,'button');h.state.position=20;h.video.dispatch('timeupdate');assert.equal(h.el('skip-segment').hidden,true);
  h.change('skip-mode-credits','off');await flush();assert.equal(h.writes[0].key,'skip_segments');
  assert.equal(h.writes[0].owner,'two');assert.equal(h.writes[0].kind,'preferences');
  h.VV.user=null;h.emit('personalchange');assert.equal(h.el('skip-mode-credits').value,'button');
});

test('late external results and stale tracks never appear in a different playback identity', async () => {
  const pending=[];const h=setup({fetcher:(_,options)=>new Promise(resolve=>pending.push({resolve,options}))});
  h.tracks([]);assert.equal(pending.length,1);
  h.state.file=1;h.state.episode=2;h.emit('playbackchange');assert.equal(pending[0].options.signal.aborted,true);
  pending[0].resolve({ok:true,json:async()=>({status:'ready',segments:[{type:'intro',start:10,end:30}]})});await flush();
  h.state.position=20;h.video.dispatch('timeupdate');assert.equal(h.el('skip-segment').hidden,true);
  h.emit('playbacksegments',{segments:[{type:'intro',start:10,end:30,auto_skip:true}],media_key:mediaKey,duration:300,identity:{id:'tt1',magnet:'magnet:first',file:0}});
  assert.equal(h.el('skip-segment').hidden,true);h.emit('playbackstop');assert.equal(h.wrap.children[0].hidden,true);
});

test('malformed persisted marks are ignored and season zero specials still look up external marks', async () => {
  const h=setup();h.preferences.set('one|preferences|'+mediaKey,{data:{segments:'invalid',overridden_types:'invalid'}});
  h.state.season=0;h.tracks([]);await flush();assert.equal(h.requests.length,1);
  assert.match(h.requests[0].url,/season=0/);assert.equal(h.el('skip-segment').hidden,true);
  h.preferences.set('one|preferences|'+mediaKey,{data:{segments:[null,42,{type:'bad',start:0,end:20}],overridden_types:[]}});
  h.emit('personalchange');assert.equal(h.el('skip-segment').hidden,true);
});

test('TV Back closes the fullscreen editor while Backspace can edit time fields', () => {
  const h=setup();h.tracks([]);h.el('skip-segments-open').dispatch('click');
  h.key('Backspace',h.el('skip-edit-start'));assert.equal(h.el('skip-segments-panel').hidden,false);
  h.key('BrowserBack',h.el('skip-edit-save'));assert.equal(h.el('skip-segments-panel').hidden,true);
  assert.equal(h.document.activeElement,h.el('skip-segments-open'));
});

test('unavailable external lookup can be retried without restarting playback', async () => {
  let calls=0;
  const h=setup({fetcher:async()=>({ok:true,json:async()=>++calls===1
    ? {status:'unavailable',segments:[]} : {status:'ready',segments:[{type:'intro',start:10,end:30}]}})});
  h.state.position=20;h.tracks([]);await flush();
  assert.equal(h.el('skip-segments-retry').hidden,false);assert.equal(h.el('skip-segments-retry').disabled,false);
  h.el('skip-segments-retry').dispatch('click');await flush();
  assert.equal(h.requests.length,2);assert.equal(h.el('skip-segment').hidden,false);assert.equal(h.el('skip-segments-retry').hidden,true);
  assert.deepEqual(h.seeks,[]);
});

test('overlapping types cannot chain automatic skips; manual skip stays available', async () => {
  const h=setup({guest:true});h.state.position=20;h.tracks([{type:'intro',start:10,end:30,auto_skip:true}]);
  h.el('skip-segments-open').dispatch('click');h.submit('recap','15','40');
  h.change('skip-mode-intro','auto');h.change('skip-mode-recap','auto');h.el('skip-segments-close').dispatch('click');h.video.dispatch('timeupdate');
  assert.deepEqual(h.seeks,[]);assert.match(h.el('skip-segment-hint').textContent,/пересекаются/);
  h.el('skip-segment').dispatch('click');assert.deepEqual(h.seeks,[30]);
});
