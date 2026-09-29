const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const root = path.join(__dirname, '../web');
let fail = false, empty = false, calls = 0;
const film = {id:'tt100',imdb_id:'tt100',title:'Example',title_ru:'Пример',kind:'feature',year:2020};
const order = {title:'Тестовая вселенная',source:'TMDB',source_url:'https://www.themoviedb.org/collection/10',mode:'release',note:'По дате выхода, а не по времени событий.',items:[
  {id:'tt99',title:'Предыдущая часть',date:'2018-01-01'},
  {id:'tt100',title:'Пример',date:'2020-01-01',current:true},
  {title:'Дополнительная история',date:'2021',extra:true},
  {id:'tt101',title:'Следующая часть',date:'2022-01-01'}
]};
const server = http.createServer((req,res) => {
 const u=new URL(req.url,'http://local');
 const json=(data,status=200)=>{res.writeHead(status,{'Content-Type':'application/json'});res.end(JSON.stringify(data));};
 if(u.pathname.endsWith('/watch-order')) {calls++;return json(empty?{items:[]}:order,fail?502:200);}
 if(u.pathname==='/api/auth/me') return json({},401);
 if(u.pathname==='/api/films/tt100') return json(film);
 if(u.pathname.endsWith('/sources')) return json({items:[],status:'ready',seasons:[]});
 if(u.pathname.endsWith('/explore')) return json({items:[]});
 if(u.pathname==='/api/catalog') return json({items:[{...film,id:'tt101',imdb_id:'tt101',title_ru:'Результат поиска'}]});
 if(u.pathname.startsWith('/api/')) return json({items:[]});
 const filename=path.join(root,u.pathname==='/'?'film.html':u.pathname);
 if(!filename.startsWith(root)||!fs.existsSync(filename)){res.writeHead(404);return res.end();}
 res.setHeader('Content-Type',filename.endsWith('.js')?'application/javascript':filename.endsWith('.css')?'text/css':'text/html');
 res.end(fs.readFileSync(filename));
});
(async()=>{
 await new Promise(resolve=>server.listen(0,'0.0.0.0',resolve));
 const browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||undefined,args:['--no-sandbox']});
 const page=await browser.newPage({viewport:{width:390,height:844}});
 const errors=[];page.on('pageerror',e=>errors.push(e.message));
 const base=`http://127.0.0.1:${server.address().port}`;
 try {
  await page.goto(base+'/film.html?id=tt100');
  const box=page.locator('#film-watch-order');
  await box.locator('li').last().waitFor();
  assert.equal(await box.locator('li').count(),4);
  assert.match(await box.locator('[aria-current]').innerText(),/Вы здесь/);
  assert.equal(await box.getByRole('link',{name:'Далее: Следующая часть'}).getAttribute('href'),'/film.html?id=tt101');
  assert.equal(calls,1);
  await box.getByRole('button',{name:'Найти в каталоге'}).click();
  await box.getByRole('link',{name:/Результат поиска/}).waitFor();
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
  await box.scrollIntoViewIfNeeded();
  await page.screenshot({path:path.join(process.env.FEATURE_SCREENSHOTS || '/output','watch-order-mobile.png'),fullPage:true});
  fail=true;await page.reload();
  await box.getByRole('button',{name:'Повторить'}).waitFor();
  fail=false;await box.getByRole('button',{name:'Повторить'}).click();
  await box.locator('li').last().waitFor();
  empty=true;await page.reload();
  await page.waitForFunction(()=>document.getElementById('film-watch-order').hidden);
  assert.deepEqual(errors,[]);
  console.log('Watch order browser checks passed: anonymous access, current/next links, search, mobile, retry, empty result.');
 } finally {await browser.close();server.close();}
})().catch(e=>{console.error(e);server.close();process.exitCode=1;});
