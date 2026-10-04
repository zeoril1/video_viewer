const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const source=fs.readFileSync(path.join(__dirname,'../web/catalog.js'),'utf8');

test('continue watching is hidden during search and restored when cleared',()=>{
 const ctx=vm.createContext({searchEl:{value:'атака титанов'},VV:{user:{}},watchHistory:[{title:'Example',position:10,duration:100}],
  continueSec:{hidden:false},continueList:{innerHTML:'old',appendChild(){}},continueTitle:{},
  document:{createElement:()=>({addEventListener(){}})},posterImgHtml:()=>'',isSeriesKind:()=>false,
  escapeHtml:x=>x,dispTitle:x=>x.title,t:x=>x,fmtTime:String,markPosterLoaded(){}});
 vm.runInContext(source.slice(source.indexOf('function renderContinue()'),source.indexOf('function resumeItem(')),ctx);
 ctx.renderContinue();assert.equal(ctx.continueSec.hidden,true);assert.equal(ctx.continueList.innerHTML,'');
 ctx.searchEl.value='';ctx.renderContinue();assert.equal(ctx.continueSec.hidden,false);
});

test('all-results requests explicitly include section=all',async()=>{
 let url;
 const ctx=vm.createContext({searchEl:{value:'атака титанов'},PER_PAGE:30,catalogGen:0,currentSection:'all',currentGenre:'',currentCollection:'',currentSort:'year',onlyReleased:true,
  URLSearchParams,fetch:async u=>{url=u;return{ok:true,json:async()=>({items:[],total_pages:1})}},render(){},updateSentinel(){}});
 vm.runInContext(source.slice(source.indexOf('async function fetchPage('),source.indexOf('function render()')),ctx);
 await ctx.fetchPage(1,false);
 const params=new URL(url,'http://localhost').searchParams;
 assert.equal(params.get('section'),'all');assert.equal(params.get('released'),'1');
});

test('metadata uses release filter and ignores responses from an older search',async()=>{
 const pending=[];let displayed;
 const ctx=vm.createContext({searchEl:{value:'old'},currentGenre:'',onlyReleased:true,URLSearchParams,console,
  fetch:url=>new Promise(resolve=>pending.push({url,resolve})),renderSections:x=>{displayed=x;},populateGenres(){}});
 vm.runInContext(source.slice(source.indexOf('let metaGeneration'),source.indexOf('function renderSections(')),ctx);
 const old=ctx.refreshMeta();ctx.searchEl.value='new';const latest=ctx.refreshMeta();
 assert.equal(new URL(pending[1].url,'http://localhost').searchParams.get('released'),'1');
 pending[1].resolve({ok:true,json:async()=>({sections:{cartoon:7,movie:2}})});await latest;
 pending[0].resolve({ok:true,json:async()=>({sections:{movie:999}})});await old;
 assert.equal(displayed.movie,2);assert.equal(displayed.cartoon,7);
});
