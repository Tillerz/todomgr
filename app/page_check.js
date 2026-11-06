// Page smoke test: runs the whole script of web/index.html in node with a fake browser (no DOM library),
// fills it with data and draws the main views. Catches script errors that only show up when a view is drawn,
// such as a blank Today page. Prints "PASS name" / "FAIL name: why" per check, exit code 1 on failure.
// Usage: node page_check.js                     the page's own demo data
//        node page_check.js data/database.json  a real data file (read only, nothing is written)
'use strict';
const fs = require('fs'), path = require('path');
const web = path.join(__dirname, 'web');
const html = fs.readFileSync(path.join(web, 'index.html'), 'utf8');
const script = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map(m => m[1]).join('\n');

// anything the page touches that is not modelled below: callable, settable, and returns itself
const stub = new Proxy(function () {}, {
  get: (t, k) => k === Symbol.toPrimitive ? () => '' : k === Symbol.iterator ? function* () {} : k === 'then' ? undefined : k === 'length' ? 0 : stub,
  set: () => true, apply: () => stub, construct: () => stub,
});
// elements keep what the page writes into them, so the drawn HTML can be checked
const els = {};
const el = sel => els[sel] || (els[sel] = new Proxy({innerHTML: '', textContent: '', value: '', hidden: false, dataset: {}}, {
  get: (t, k) => k in t ? t[k] : stub, set: (t, k, v) => { t[k] = v; return true; },
}));
const document = new Proxy({}, {get: (t, k) => k === 'querySelector' || k === 'getElementById' ? el : k === 'querySelectorAll' ? () => [] : k === 'title' ? '' : stub});
const env = {document, window: stub, navigator: stub, location: stub, localStorage: {getItem: () => null, setItem() {}, removeItem() {}},
  addEventListener() {}, setInterval() {}, setTimeout() {}, clearTimeout() {}, requestAnimationFrame() {}, fetch: () => new Promise(() => {}),
  matchMedia: () => ({matches: false, addEventListener() {}}), innerWidth: 1200, innerHeight: 800, getComputedStyle: () => stub,
  BroadcastChannel: function () { return stub; }, Notification: stub, Sortable: stub, marked: require(path.join(web, 'marked.umd.js')), console};

let failed = 0;
const check = (name, fn) => {
  let why;
  try { why = fn(); } catch (e) { why = 'error: ' + e.message + ' ' + (e.stack.split('\n')[1] || '').trim(); }
  if (why) { failed++; console.log(`FAIL ${name}: ${why}`); } else console.log('PASS ' + name);
};
const text = h => String(h).replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ');

// the page's default settings, as the program sends them (app/defaults.go)
const uiDefaults = JSON.parse(/uiDefaultsJSON = `([\s\S]*?)`/.exec(fs.readFileSync(path.join(__dirname, 'defaults.go'), 'utf8'))[1]);
const file = process.argv[2], db = file ? JSON.parse(fs.readFileSync(file, 'utf8')) : null;
// the checks run inside the page script, so they see its functions and its S and ui
const page = new Function(...Object.keys(env), 'DB', 'check', 'el', 'text', 'UIDEF', script + `
;Object.assign(DEF,UIDEF);if(DB)S={v:3,rev:1,nodes:DB.nodes,activity:[],timer:null,prefs:DB.prefs||{}};else seed();
const live=allNodes();
check('page: data loaded ('+live.length+' entries)',()=>live.length?'':'no entries');
check('page: tree',()=>{ui.view='tree';renderTree();return el('#tree').innerHTML.includes('class="node')?'':'no rows'});
check('page: Today',()=>{ui.todayWeek=false;renderToday();const t=text(el('#vToday').innerHTML);return t.includes('Today')?'':'empty: '+t.slice(0,80)});
check('page: Next 7 days',()=>{ui.todayWeek=true;renderToday();const n=(el('#vToday').innerHTML.match(/<h2>/g)||[]).length;ui.todayWeek=false;return n===7?'':n+' days, want 7'});
check('page: stats',()=>{ui.view='stats';renderStats();return el('#vStats').innerHTML?'':'empty'});
check('page: detail panel of every entry',()=>{const bad=[];ui.panel=true;
  for(const n of live){ui.sel=n.id;ui.prevFor=null;try{renderDetail();if(!el('#detail').innerHTML.includes(esc(n.name)))bad.push(n.id+' (name missing)')}catch(e){bad.push(n.id+': '+e.message)}}
  ui.sel=null;return bad.slice(0,5).join('; ')});
check('tree: sort sub-items, then undo',()=>{const p=live.find(n=>kids(n).length>2);if(!p)return 'no entry with 3 sub-items';
  const before=p.children.join(),names=()=>p.children.map(i=>S.nodes[i].name),bad=[];
  sortChildren(p.id,'name');const nm=names();if(nm.join()!==[...nm].sort((a,b)=>a.localeCompare(b,undefined,{numeric:true,sensitivity:'base'})).join())bad.push('name: '+nm.join(', '));
  sortChildren(p.id,'prio');const pr=p.children.map(i=>S.nodes[i].prio);if(pr.some((x,i)=>i&&x>pr[i-1]))bad.push('prio: '+pr);
  sortChildren(p.id,'due');const du=p.children.map(i=>{const n=S.nodes[i];return n.deadline||n.reminder||''});if(du.some((x,i)=>i&&(du[i-1]===''?x!=='':x&&x<du[i-1])))bad.push('due: '+du);
  undo();undo();undo();if(S.nodes[p.id].children.join()!==before)bad.push('undo did not bring the old order back');return bad.join('; ')});
check('menu: remove person from one and from several entries',()=>{let [a,b]=allNodes().filter(n=>n.kind!=='reminder').slice(0,2);addPerson([a,b],'Zed');addPerson([a],'Ann');
  if(!nodeMenu(a).some(i=>i.label==='Add person'))return 'single menu: no Add person';const sub=nodeMenu(a).find(i=>i.label==='Remove person');if(!sub||sub.sub.map(i=>i.label).join()!=='Ann,Zed')return 'single menu: '+(sub?sub.sub.map(i=>i.label):'missing');
  bulkMenu([a.id,b.id]).find(i=>i.label==='Remove person').sub.find(i=>i.label==='Zed').fn();
  a=S.nodes[a.id];b=S.nodes[b.id];return a.people.includes('Zed')||b.people.includes('Zed')||!a.people.includes('Ann')?'people now: '+a.people+' / '+b.people:''});
check('drop: a mail from a mail program gives one link',()=>{const dt=(uris,files)=>({dataTransfer:{getData:t=>t==='text/uri-list'?uris:'',files}});
  const one=dropLinks(dt('mailbox:///C:/Mail/AI?number=327',[{name:'Hello.eml',size:5}])),two=dropLinks(dt('https://a.org/x',[{name:'a.txt'},{name:'b.txt'}]));
  if(one.length!==1||one[0].title!=='Hello.eml'||one[0].uri!=='mailbox:///C:/Mail/AI?number=327'||!one[0].f)return 'mail drop: '+JSON.stringify(one);
  if(two.length!==3)return 'one URL and two files: '+two.length+' links, want 3';
  const n=allNodes().find(x=>x.kind!=='reminder');n.links.push({title:'Hello.eml',uri:'',file:'0123456789abcdef0123.eml'});ui.sel=n.id;ui.panel=true;renderDetail();
  const h=el('#detail').innerHTML;n.links.pop();ui.sel=null;return h.includes('Hello.eml')&&!h.includes('(no path)')?'':'detail panel does not show the kept file'});
check('icons: the icon list has trash, every listed icon exists, project picker has a filter',()=>{const icons=new Set(Object.keys(P));
  const bad=TAG_ICON_SET.filter(k=>!icons.has(k));if(!TAG_ICON_SET.includes('trash'))bad.push('trash missing');
  const p=allNodes().find(x=>x.kind==='project');ui.sel=p.id;ui.panel=true;ui.icPick=p.id;renderDetail();if(!el('#detail').innerHTML.includes('class="icq"'))bad.push('no filter field');
  ui.icPick=null;ui.sel=null;return bad.join('; ')});
check('search: a tag filter goes away when the tag is removed from every entry',()=>{const n=allNodes().find(x=>x.kind!=='reminder'),bad=[];
  n.tags.push('zzgone');ui.q='#zzgone urgent';renderTree();if(ui.q!=='#zzgone urgent')bad.push('removed too early: '+ui.q);
  n.tags.pop();renderTree();if(ui.q!=='urgent')bad.push('after removing the tag: "'+ui.q+'"');
  ui.q='#notyet';renderTree();renderTree();if(ui.q!=='#notyet')bad.push('a tag being typed was removed');ui.q='';renderTree();return bad.join('; ')});
check('tags: Project tags shows only the tags of the top-level entry of the selection',()=>{const bad=[],A=mk('Area A'),B=mk('Area B'),a=mk('a1',{tags:['onlya']}),b=mk('b1',{tags:['onlyb']});
  attach(A,'root');attach(B,'root');attach(a,A.id);attach(b,B.id);const bar=()=>el('#tagbar').innerHTML;
  S.prefs.projTags=true;ui.sel=a.id;renderTags();if(!bar().includes('#onlya')||bar().includes('#onlyb'))bad.push('on, a selected: '+text(bar()).slice(0,80));
  ui.q='#onlyb';renderTags();if(!bar().includes('#onlyb'))bad.push('the active filter #onlyb is not shown');ui.q='';
  S.prefs.projTags=false;renderTags();if(!/#(?!onlya)/.test(text(bar())))bad.push('off: only the tags of Area A');
  ui.sel=null;[a,b,A,B].forEach(n=>{detach(n);delete S.nodes[n.id]});renderTags();return bad.join('; ')});
check('icons: Settings → Tag icons picker has the filter, every icon and its name',()=>{const t=Object.keys(tagIcons())[0];renderTagIcons(t);const h=el('#tagIcs').innerHTML,bad=[];
  if(!h.includes('class="icq"'))bad.push('no filter field');const miss=TAG_ICON_SET.filter(k=>!h.includes('data-v="'+k+'" data-t="'+esc(t)+'" data-tip="'+k+'"'));
  if(miss.length)bad.push('icons without name or missing: '+miss.slice(0,5).join(', '));return bad.join('; ')});
check('after an event: waits, is due N days later, comes back for the next one',()=>{const day=d=>addDays(d),bad=[];
  const ev=mk('Game night',{kind:'reminder',reminder:day(5)+'T18:00',end:day(5)+'T23:00',recur:'w',atDone:day(-2)+'T18:00'});attach(ev,'root');
  const t=mk('Session diary',{prio:3});attach(t,'root');setAfter(t,ev.id,8);
  if(t.after.start!==day(-2)+'T18:00'||t.deadline!==day(6)||waits(t))bad.push('set: '+JSON.stringify(t.after)+' '+t.deadline);
  setStatus(t,'done');if(t.status!=='open'||t.after.start!==day(5)+'T18:00'||t.deadline!==day(13)||!waits(t)||!blocked(t))bad.push('done: '+t.status+' '+JSON.stringify(t.after)+' '+t.deadline);
  ui.todayWeek=false;renderToday();if(el('#vToday').innerHTML.includes('Session diary'))bad.push('a waiting todo is on Today');
  ui.sel=t.id;ui.panel=true;renderDetail();if(!el('#detail').innerHTML.includes('Waits for “Game night”'))bad.push('detail panel does not say it waits');
  setAfter(t,ev.id,1);if(t.after.start!==day(5)+'T18:00'||t.deadline!==day(6))bad.push('1 day: '+JSON.stringify(t.after)+' '+t.deadline);
  ev.recur='';t.after.start=day(-2)+'T18:00';setStatus(t,'done');if(t.status!=='done')bad.push('single event: status '+t.status);
  ui.sel=null;detach(t);detach(ev);delete S.nodes[t.id];delete S.nodes[ev.id];return bad.join('; ')});
check('after an event: moving the event moves the todo; a todo that becomes an event loses its deadline',()=>{const day=d=>addDays(d),bad=[];
  const ev=mk('Game night',{kind:'reminder',reminder:day(5)+'T18:00',end:day(5)+'T23:00',recur:'w'});attach(ev,'root');
  const t=mk('Session diary');attach(t,'root');setAfter(t,ev.id,8);
  const old=ev.reminder;ev.reminder=day(6)+'T19:00';ev.end=day(7)+'T01:00';afterFollow(ev,old);
  if(t.after.start!==day(6)+'T19:00'||t.deadline!==day(15))bad.push('moved: '+JSON.stringify(t.after)+' '+t.deadline);
  const x=mk('Stream',{deadline:day(3)+'T20:00'});attach(x,'root');setKind(x,'reminder');
  if(x.reminder!==day(3)+'T20:00'||x.deadline)bad.push('todo → event: reminder '+x.reminder+', deadline '+x.deadline);
  [t,ev,x].forEach(n=>{detach(n);delete S.nodes[n.id]});return bad.join('; ')});
// login repair (Store.heal) with fake server answers: [status, body] per path, in order
const fakeNet=routes=>{const log=[];fetch=async p=>{log.push(p);const q=routes[p]||[];const [st,b]=q.length>1?q.shift():q[0]||[404,{}];return{status:st,ok:st<300,json:async()=>b}};return log};
const asyncCheck=async(name,fn)=>{let why;try{why=await fn()}catch(e){why='error: '+e.message}check(name,()=>why)};
return (async()=>{const realFetch=fetch,realLoc=location;APP.instance='aaaa1111';
  await asyncCheck('login repair: wrong cookie on this computer, same data folder: repaired, the save goes through',async()=>{
    const log=fakeNet({'/api/x':[[401,{error:'wrong token'}],[200,{ok:1}]],'/':[[200,{}]],'/api/info':[[200,{instance:'aaaa1111'}]]});
    const r=await Store.api('POST','/api/x',{});return r&&r.ok===1&&!Store.foreign&&log.join()==='/api/x,/,/api/info,/api/x'?'':'result '+JSON.stringify(r)+', requests '+log.join()});
  await asyncCheck('login repair: another data folder at this address: nothing is saved, a notice shows',async()=>{
    fakeNet({'/api/x':[[401,{}]],'/':[[200,{}]],'/api/info':[[200,{instance:'bbbb2222'}]]});let err='';
    try{await Store.api('POST','/api/x',{})}catch(e){err=e.message}
    const blocked=await Store.api('POST','/api/y',{}).then(()=>false,()=>true);
    const r=Store.foreign&&err&&blocked&&modalKind==='foreign'?'':'foreign '+Store.foreign+', error "'+err+'", next save blocked '+blocked+', modal '+modalKind;
    Store.foreign=false;closeModal();return r});
  await asyncCheck('login repair: another device without a valid login: the page reloads (login page)',async()=>{
    let reloaded=false;location={reload(){reloaded=true}};fakeNet({'/api/x':[[401,{}]],'/':[[200,{}]],'/api/info':[[401,{}]]});
    const r=await Store.api('POST','/api/x',{}).then(()=>'saved',()=>'refused');return reloaded&&r==='refused'?'':'reloaded '+reloaded+', '+r});
  fetch=realFetch;location=realLoc})();
`);
const pageAsync = page(...Object.values(env), db, check, el, text, uiDefaults);

// installable app: the manifest, its icons and the service worker
check('app: manifest and icons', () => {
  const m = JSON.parse(fs.readFileSync(path.join(web, 'manifest.json'), 'utf8'));
  const missing = (m.icons || []).map(i => i.src).filter(src => !fs.existsSync(path.join(web, src)));
  for (const f of ['/manifest.json', '/icon.svg', '/apple-touch-icon.png']) if (!html.includes(f)) missing.push('not linked: ' + f);
  if (!script.includes("'/sw.js'")) missing.push('service worker not registered');
  return !m.name || !m.start_url || !m.icons?.some(i => i.sizes === '512x512') ? 'manifest lacks name, start_url or a 512 icon' : missing.join('; ');
});
check('defaults: every default tag icon exists', () => {
  const icons = new Set([...script.matchAll(/^\s*'?([a-z0-9-]+)'?:'</gm)].map(m => m[1]));
  return Object.entries(uiDefaults.tagIcons).filter(([, i]) => !icons.has(i)).map(([t, i]) => `${t}: ${i}`).join('; ');
});
check('app: service worker script', () => { new (require('vm').Script)(fs.readFileSync(path.join(web, 'sw.js'), 'utf8')); });
Promise.resolve(pageAsync).then(() => process.exit(failed ? 1 : 0));
