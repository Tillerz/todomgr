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
  if(!nodeMenu(a).some(i=>i.label==='Add person'))return 'single menu: no Add person';a.tags.push('zztag');const rt=nodeMenu(a).find(i=>i.label==='Remove tag');if(!rt)return 'single menu: no Remove tag';rt.sub.find(i=>i.label==='#zztag').fn();a=S.nodes[a.id];if(a.tags.includes('zztag'))return 'Remove tag did not remove #zztag';const sub=nodeMenu(a).find(i=>i.label==='Remove person');if(!sub||sub.sub.map(i=>i.label).join()!=='Ann,Zed')return 'single menu: '+(sub?sub.sub.map(i=>i.label):'missing');
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
check('demo: word goals forecast a real date, not today',()=>DB?'':seed()||allNodes().filter(n=>speedOf(n)).filter(n=>dayKey(speedOf(n).fin)<=addDays(1)).map(n=>n.name+': done ~'+dayKey(speedOf(n).fin)).join('; '));
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
// cheer toasts: the toast function is replaced to record what would show
check('cheer: start, done, all sub-items, many at once, goal, all of today, off',()=>{const shown=[],realToast=toast,bad=[],made=[];
  toast=(msg,o={})=>shown.push({msg,cheer:o.cheer||'',button:!!o.action});
  const T=(name,props,parent='root')=>{const n=mk(name,props);attach(n,parent);made.push(n);return n};
  const run=fn=>{cheerQ=null;shown.length=0;fn();flushCheer();return shown.slice()};
  const is=(t,k)=>t&&CHEERS[k].includes(t.cheer);
  const prefs=S.prefs.cheer,parked=allNodes().filter(x=>x.kind==='todo'&&!isClosed(x)&&['over','today'].includes(dueState(x)));
  try{const P=T('Cheer parent'),a=T('Cheer a',{},P.id),b=T('Cheer b',{},P.id);
    let r=run(()=>setStatus(a,'progress'));if(r.length!==1||!is(r[0],'progress'))bad.push('start: '+JSON.stringify(r));
    r=run(()=>setStatus(a,'done'));if(r.length!==1||!is(r[0],'done'))bad.push('done: '+JSON.stringify(r));
    r=run(()=>setStatus(b,'done'));if(r.length!==1||!is(r[0],'parent')||!r[0].button||!r[0].msg.includes('Cheer parent'))bad.push('all sub-items: '+JSON.stringify(r));
    const many=[1,2,3].map(i=>T('Cheer many '+i));r=run(()=>many.forEach(n=>setStatus(n,'done')));if(r.length!==1)bad.push('many at once: '+r.length+' toasts');
    const g=T('Cheer goal',{prog:{mode:'units',done:0,total:10,unit:'words'}});r=run(()=>setProgress(g,10));
    if(r.length!==1||!is(r[0],'goal')||!r[0].msg.includes('10 words'))bad.push('goal: '+JSON.stringify(r));
    parked.forEach(x=>x.status='later');const d=T('Cheer due',{deadline:addDays(0)});r=run(()=>setStatus(d,'done'));
    if(r.length!==1||!is(r[0],'today'))bad.push('all of today: '+JSON.stringify(r));
    S.prefs.cheer=false;const c=T('Cheer off'),e=T('Cheer off a',{},c.id);r=run(()=>setStatus(T('Cheer off 2'),'done'));if(r.length)bad.push('off, done: '+JSON.stringify(r));
    r=run(()=>setStatus(e,'done'));if(r.length!==1||r[0].cheer||!r[0].button)bad.push('off, all sub-items keeps its button: '+JSON.stringify(r));
    let last='';for(let i=0;i<50;i++){const x=cheerPick('done');if(x===last){bad.push('the same message twice in a row');break}last=x}}
  finally{toast=realToast;S.prefs.cheer=prefs;parked.forEach(x=>x.status='open');made.reverse().forEach(n=>{detach(n);delete S.nodes[n.id]})}
  return bad.join('; ')});
check('import preview: one row per entry, entries that are already there are greyed out and cannot be ticked',()=>{
  const cal={name:'cal.ics',req:{},p:{skipped:1,items:[{i:0,name:'Stream',kind:'reminder',when:'2026-11-13T19:00',end:'2026-11-13T21:00',known:false},{i:1,name:'Old one',kind:'todo',when:'',known:true,sub:true},{i:2,name:'Kickoff',kind:'reminder',when:'2026-02-20T11:00',past:true}]}};
  showImport([cal]);
  let h=el('#modal').innerHTML;const bad=[];if(modalKind!=='import')bad.push('modal '+modalKind);
  if(!h.includes('Stream')||!h.includes('2026-11-13 19:00 – 2026-11-13 21:00'))bad.push('event row');
  if(!/class="improw known sub"><input type="checkbox" data-imp="1" data-impf="0" disabled>/.test(h)||!h.includes('already there'))bad.push('known row');
  if(!/data-imp="2" data-impf="0" >/.test(h)||!h.includes(' · past'))bad.push('past row: offered, not ticked');
  if(!h.includes('id="impFolder" checked'))bad.push('folder option');
  if(!h.includes('1 skipped'))bad.push('skipped count');closeModal();
  // two files, one of them unreadable: a heading per file, the error in place of its entries
  showImport([cal,{name:'broken.ics',err:'nothing to import'}]);h=el('#modal').innerHTML;
  if(!h.includes('Import 2 files')||!h.includes('class="imphd">cal.ics')||!h.includes('class="imphd">broken.ics')||!h.includes('Cannot import this file: nothing to import'))bad.push('two files');
  if(!h.includes('Put each file into a new project'))bad.push('folder option for two files');closeModal();return bad.join('; ')});
check('tags: suggested from existing tags as whole words, with short endings; short tags only as exact words',()=>{const made=[],bad=[];
  const T=(name,props)=>{const x=mk(name,props);attach(x,'root');made.push(x);return x};
  try{T('Old',{tags:['ai','novel']});
    const n=T('Plan meetings and call Tom about the recall',{notes:'the mail server needs streamlining, my NOVEL, air'});
    const got=tagMatches(n);for(const t of ['meeting','call','mail','novel'])if(!got.includes(t))bad.push('missing '+t);
    for(const t of ['stream','ai','research'])if(got.includes(t))bad.push('wrong '+t);
    n.tags.push('mail');if(tagMatches(n).includes('mail'))bad.push('a tag the entry has already');
    const m=T('An ai idea');if(!tagMatches(m).includes('ai'))bad.push('exact short tag');
    ui.sel=n.id;ui.panel=true;ui.prevFor=n.id;ui.tagSug={id:n.id,tags:['call','mail'],ai:false};renderDetail();const h=el('#detail').innerHTML;
    if(!h.includes('data-act="addsug" data-v="call"')||h.includes('data-v="mail"'))bad.push('suggestion row')}
  finally{ui.tagSug=null;ui.sel=null;made.reverse().forEach(x=>{detach(x);delete S.nodes[x.id]})}
  return bad.join('; ')});
check('quick add in normal words: day and time words become due: and at:, the rest stays as typed',()=>{const bad=[];
  const cases=[['remind me next Friday to call Anna','r: Call Anna due:fri'],['Dentist tomorrow at 3pm','Dentist due:tomorrow at:15:00'],
    ['Zahnarzt morgen um 15 Uhr','Zahnarzt due:tomorrow at:15:00'],['Erinnere mich übermorgen an den Müll','r: Den Müll due:+2d'],
    ['Pay rent on Monday','Pay rent due:mon'],['Friday: team lunch','Team lunch due:fri'],['Submit the report by friday.','Submit the report due:fri'],
    ['Finish the draft in 3 days','Finish the draft due:+3d'],['Steuer in 2 Wochen','Steuer due:+2w'],['Call the bank next week','Call the bank due:+1w'],
    ['Meeting 14:30 tomorrow','Meeting due:tomorrow at:14:30'],['Heute Abend Pizza backen','Pizza backen due:today'],
    ['Abgabe am 20.12.2030','Abgabe due:2030-12-20'],['Release on 2030-01-15','Release due:2030-01-15'],['remind me to stretch','r: Stretch'],
    ['Submit by midnight on Friday','Submit due:fri at:23:59'],['Heute Mittag Arzt','Arzt due:today at:12:00'],['Call Sara tomorrow at noon','Call Sara due:tomorrow at:12:00'],
    ['Lunch with Tom at noon','Lunch with Tom due:'+(pd(addDays(0)+'T12:00')>new Date()?'today':'tomorrow')+' at:12:00'],
    ['Lunch with Tom at 1','Lunch with Tom due:'+(pd(addDays(0)+'T13:00')>new Date()?'today':'tomorrow')+' at:13:00']];
  for(const [inp,want] of cases){const r=quickWords(inp);if(r.quick!==want)bad.push(inp+' → '+(r.quick||'(nothing)')+', want '+want)}
  // nothing to read: stays as typed; time words that the rules cannot read are a hint for the AI
  for(const [inp,hint] of [['Watch Friday Night Lights',true],['Buy milk',false],['Pay the bills at the end of the month',true],['Read chapter 3',false],['Recall the 3 points',false],['Call mum at half past nine',true],['Mittagessen kochen',false],['Um halb zehn Arzt',true]]){
    const r=quickWords(inp);if(r.quick||r.hint!==hint)bad.push(inp+' → '+JSON.stringify(r))}
  if(quickWords('Dentist tomorrow, end of the month').hint!==true)bad.push('hint left after the rules');
  return bad.slice(0,6).join('; ')});
check('writing goals: +500 and totals, words today from the start of the day, the Today box and the log window',()=>{const bad=[],made=[];
  for(const [inp,want] of [['+500',1500],['-200',800],['12,500',12500],['12.500',12500],['  +1 000',2000],['abc',null],['',null],['+',null]]){const v=wordLogValue(inp,1000);if(v!==want)bad.push(inp+' → '+v)}
  const dl=addDays(9),n=mk('Draft the novel',{prog:{mode:'units',done:1000,total:11000,unit:'words'},deadline:dl});attach(n,'root');made.push(n);
  try{setProgress(n,1300);if(!n.progDay||n.progDay.d!==addDays(0)||n.progDay.start!==1000)bad.push('progDay: '+JSON.stringify(n.progDay));
    setProgress(n,1500);const t=todayOf(n);if(t.done!==500||t.goal!==1000)bad.push('today: '+JSON.stringify(t)+' want 500 of 1000');
    setProgress(n,2100);const t2=todayOf(n);if(t2.after!==Math.ceil(8900/9)||t2.need!==t2.after)bad.push('after today: '+JSON.stringify(t2)+' want '+Math.ceil(8900/9));
    if(!text(pace(n)).includes('then '+Math.ceil(8900/9).toLocaleString('en')+' words per day for 9 days'))bad.push('detail pane: '+text(pace(n)).slice(0,120));
    renderToday();if(!el('#vToday').innerHTML.includes('then '+Math.ceil(8900/9).toLocaleString('en')+' words/day'))bad.push('Today box: then per day');
    setProgress(n,1500);
    n.progDay={d:addDays(-1),start:200};if(todayOf(n).done!==0)bad.push('an old progDay counts as nothing today');
    ui.todayWeek=false;renderToday();const h=el('#vToday').innerHTML;
    if(!h.includes('Writing goals')||!h.includes('Draft the novel')||!h.includes('data-wlog="'+n.id+'"'))bad.push('Today box');
    if(!h.includes('class="goal-left" data-wlog="'+n.id+'"'))bad.push('words left opens the log too');
    const r=mk('Write 500 words',{prog:{mode:'units',done:0,total:500,unit:'words'},recur:'d',deadline:addDays(0)});attach(r,'root');made.push(r);
    renderToday();if(!el('#vToday').innerHTML.includes('data-wlog="'+r.id+'"'))bad.push('a daily goal due today shows');
    const c6=mk('Chapter six',{prog:{mode:'units',done:0,total:4000,unit:'words'},deps:[n.id]});attach(c6,'root');made.push(c6);
    renderToday();if(el('#vToday').innerHTML.includes('data-wlog="'+c6.id+'"'))bad.push('a goal that waits for another entry must not show');
    c6.deps=[];c6.status='later';renderToday();if(el('#vToday').innerHTML.includes('data-wlog="'+c6.id+'"'))bad.push('a postponed goal must not show');
    r.deadline=addDays(1);renderToday();if(el('#vToday').innerHTML.includes('data-wlog="'+r.id+'"'))bad.push('a daily goal finished for today must leave Today');
    if(!h.includes('data-wtimer="'+n.id+'"'))bad.push('clock button on the goal');
    S.timer={id:n.id,start:now()-25*60000,end:null,min:0,alarmed:false};const t0=n.time.length;stopTimer(true);
    if(S.nodes[n.id].time.length!==t0+1||modalKind!=='wlog'||!el('#modal').innerHTML.includes('You wrote for 25m'))bad.push('timer stop on a goal: time '+(S.nodes[n.id].time.length-t0)+', modal '+modalKind);closeModal();
    S.timer={id:n.id,start:now()-10*60000,end:null,min:0,alarmed:false};startTimer(25,n.id);
    if(modalKind!=='wlog'||!S.timer||S.timer.min!==25)bad.push('a new timer: the old one asks for words after it starts: '+modalKind);closeModal();S.timer=null;
    wordLog(n.id);if(modalKind!=='wlog'||!el('#modal').innerHTML.includes('Now: 1,500 of 11,000 words'))bad.push('log window: '+modalKind);closeModal();
    renderTree();if(!/class="pace-t"/.test(el('#tree').innerHTML))bad.push('words-left tag in the tree')}
  finally{made.forEach(x=>{detach(x);delete S.nodes[x.id]})}
  return bad.join('; ')});
check('today: an event of today that is over moves from Upcoming reminders to Earlier today',()=>{const bad=[],made=[];
  const t=new Date(Date.now()-3*3600e3),hh=x=>String(x.getHours()).padStart(2,'0')+':'+String(x.getMinutes()).padStart(2,'0'),e=new Date(Date.now()-2*3600e3);
  if(t.getDate()!==new Date().getDate())return '';// too early in the day to test
  const ev=mk('Dentist past',{kind:'reminder',status:'set',reminder:addDays(0)+'T'+hh(t),end:addDays(0)+'T'+hh(e)});attach(ev,'root');made.push(ev);
  try{ui.todayWeek=false;renderToday();const h=el('#vToday').innerHTML,iu=h.indexOf('Upcoming reminders'),ie=h.indexOf('Earlier today'),ip=h.indexOf('Dentist past');
    if(ie<0||ip<ie)bad.push('not under Earlier today');if(iu>=0&&iu<ip&&(ie<0||ip<ie))bad.push('still under Upcoming')}
  finally{made.forEach(x=>{detach(x);delete S.nodes[x.id]})}
  return bad.join('; ')});
check('growing pictures: a project counts the words of its goals; book and journey grow; Today and the detail panel',()=>{const bad=[],made=[];
  const T=(name,props,parent='root')=>{const x=mk(name,props);attach(x,parent);made.push(x);return x};
  try{const p=T('Pic novel',{kind:'project',status:'active',pic:'journey'});
    T('Ch one',{prog:{mode:'units',done:3000,total:4000,unit:'words'}},p.id);T('Ch two',{status:'done',prog:{mode:'units',done:0,total:4000,unit:'words'}},p.id);T('Plot notes',{},p.id);
    const r=picProg(p);if(r.txt!=='7,000 of 8,000 words'||Math.abs(r.f-.875)>1e-9)bad.push('project words: '+JSON.stringify(r));
    const q=T('Pic plain',{kind:'project',pic:'book'});T('a',{status:'done'},q.id);T('b',{},q.id);if(Math.abs(picProg(q).f-.5)>1e-9)bad.push('project without word goals: '+picProg(q).f);
    const j0=picJourney(0),j1=picJourney(1);if(!j0.includes('<circle')||j1.includes('<circle')||!j1.includes('M147 1L153'))bad.push('journey: marker before, flag at the end');
    if(picJourney(.3)===picJourney(.31))bad.push('the journey must change with every step');
    const b1=picBook(1,'The <Crown>');if(!b1.includes('The &lt;Crown&gt;')||picBook(.5,'x').includes('text-anchor'))bad.push('book: the title only on the finished book, escaped');
    if(picBook(.4,'x')===picBook(.41,'x'))bad.push('the book must change with every step');
    ui.todayWeek=false;renderToday();const h=el('#vToday').innerHTML;if(!h.includes('class="piccard" data-goto="'+p.id+'"')||!h.includes('7,000 of 8,000 words · 88%'))bad.push('Today card');
    if(h.includes('data-goto="'+made[3].id+'"')&&h.includes('Plot notes'))bad.push('only entries with a picture');
    ui.sel=p.id;ui.panel=true;ui.prevFor=null;renderDetail();const d=el('#detail').innerHTML;
    if(!d.includes('<option value="journey" selected>')||!d.includes('class="picbig"'))bad.push('detail panel');
    p.pic='';renderToday();if(el('#vToday').innerHTML.includes('class="piccard" data-goto="'+p.id+'"'))bad.push('no picture: no card')}
  finally{ui.sel=null;made.reverse().forEach(x=>{detach(x);delete S.nodes[x.id]})}
  return bad.join('; ')});
// note drafts (Drafts) with an in-memory browser store; Store.clientId stands for the window
check('note drafts: kept in the browser, put back on the next page load, asks when the note changed elsewhere',()=>{
  const mem={},realLS=localStorage,me=Store.clientId,bad=[];
  localStorage={getItem:k=>k in mem?mem[k]:null,setItem:(k,v)=>{mem[k]=String(v)},removeItem:k=>{delete mem[k]}};
  const n=allNodes().find(x=>x.kind==='todo'),id=n.id,old=n.notes,notes=()=>S.nodes[id].notes,saved=()=>{Store.synced[id]=JSON.stringify(S.nodes[id])};
  try{saved();Drafts.keep(id,'typed text');const d=Drafts.all()[id];
    if(!d||d.base!==old||d.text!=='typed text')bad.push('keep: '+JSON.stringify(d));
    Drafts.restore();if(notes()!==old)bad.push('the draft of a window that is still open was put back');
    Drafts.orphan();Store.clientId='page2';Drafts.restore();if(notes()!=='typed text')bad.push('not put back: '+notes());
    saved();Drafts.sent([id]);if(Drafts.all()[id])bad.push('draft not removed after the save');
    Drafts.keep(id,'my text');Drafts.orphan();Store.clientId='page3';S.nodes[id].notes='their text';Drafts.restore();
    if(modalKind!=='draft'||notes()!=='their text')bad.push('changed elsewhere: modal '+modalKind+', notes '+notes());
    Drafts.pick(true);if(notes()!=='my text'||modalKind)bad.push('keep your text: notes '+notes()+', modal '+modalKind);
    saved();Drafts.sent([id]);if(Drafts.all()[id])bad.push('draft not removed after keep your text');
    Drafts.keep(id,'draft 2');Drafts.orphan();Store.clientId='page4';S.nodes[id].notes='saved 2';Drafts.restore();Drafts.pick(false);
    if(notes()!=='saved 2'||Object.keys(Drafts.all()).length)bad.push('keep the saved text: notes '+notes()+', drafts '+JSON.stringify(Drafts.all()));
    saved();Drafts.keep(id,'phone text');Drafts.orphan();Drafts.claim();Store.clientId='page5';Drafts.restore();
    if(notes()!=='saved 2')bad.push('took the draft of a window that came back to the front');
    Store.clientId='page4';Drafts.orphan();Store.clientId='page6';Drafts.restore();
    if(notes()!=='phone text')bad.push('a background tab that the phone closed: not put back at once, notes '+notes())}
  finally{S.nodes[id].notes=old;saved();localStorage=realLS;Store.clientId=me;closeModal()}
  return bad.join('; ')});
check('AI: no AI buttons while AI is off; buttons and menu entry when on; setup help in Settings',()=>{const bad=[],keep=APP.ai;
  const n=allNodes().find(x=>x.kind==='todo'&&x.notes.trim()),r=allNodes().find(x=>x.kind==='reminder');
  const panel=x=>{ui.sel=x.id;ui.panel=true;ui.prevFor=null;renderDetail();ui.mdPrev=true;return el('#detail').innerHTML};
  const menu=x=>nodeMenu(x).some(i=>i.label==='Suggest sub-tasks…');
  try{APP.ai=false;const h=panel(n);if(h.includes('data-act="aisub"')||h.includes('data-act="airewrite"')||menu(n))bad.push('buttons while off');
    APP.ai=true;const h2=panel(n);if(!h2.includes('data-act="aisub"')||!h2.includes('data-act="airewrite"')||!menu(n))bad.push('buttons while on');
    if(nodeMenu(n).find(i=>i.label==='Suggest sub-tasks…').icon!=='bot'||!ctxHTML(nodeMenu(n)).includes('<span class="ck"><svg')||!/data-act="aisub"[^>]*><svg/.test(h2))bad.push('robot icon in the menu and on the button');closeCtx();
    if(r&&(panel(r).includes('data-act="aisub"')||menu(r)))bad.push('a reminder gets Suggest sub-tasks');
    const e=allNodes().find(x=>!x.notes.trim()&&x.kind==='todo');if(e&&!panel(e).includes('data-act="airewrite"'))bad.push('Rewrite hidden for empty notes');
    const st=aiSettings({enabled:true,ready:false,dir:'/x/ai',helper:'',model:'',running:false});
    if(!st.includes('llama-server missing')||!st.includes('Qwen3.5-4B-Q4_K_M.gguf')||!/id="aiTest" disabled/.test(st)||!/id="aiUnload" disabled/.test(st))bad.push('settings, not set up');
    const st2=aiSettings({enabled:true,ready:true,dir:'/x/ai',helper:'llama-server',model:'m.gguf',running:true});
    if(st2.includes('id="aiModel"')||!aiSettings({enabled:true,ready:true,dir:'/x',helper:'h',model:'b.gguf',models:['a.gguf','b.gguf']}).includes('<option value="b.gguf" selected>'))bad.push('model list only for two or more models');
    if(st2.includes('To set it up')||/id="aiTest" disabled/.test(st2)||/id="aiUnload" disabled/.test(st2)||!st2.includes('loaded'))bad.push('settings, set up and loaded')}
  finally{APP.ai=keep;ui.sel=null}
  return bad.join('; ')});
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
  const realSync=Store.sync;Store.sync=async()=>{};
  await asyncCheck('AI: suggested sub-tasks and rewritten notes are shown first; an error closes the thinking window',async()=>{const bad=[];
    const n=allNodes().find(x=>x.kind==='todo'&&x.notes.trim());
    fakeNet({'/api/ai/subtasks':[[200,{items:['Buy the food','Book the room']}]]});await aiSubtasks(n.id);
    const h=el('#modal').innerHTML;if(modalKind!=='aisub'||!h.includes('value="Buy the food"')||!h.includes('value="Book the room"'))bad.push('sub-tasks: modal '+modalKind);closeModal();
    aiRewrite(n.id);const hm=el('#modal').innerHTML;if(modalKind!=='aimode'||!/data-aimode="tidy" >/.test(hm)||!hm.includes('data-aimode="expand"'))bad.push('choice: modal '+modalKind);closeModal();
    const e=allNodes().find(x=>!x.notes.trim()&&x.kind==='todo');if(e){aiRewrite(e.id);if(!/data-aimode="tidy" disabled/.test(el('#modal').innerHTML))bad.push('Tidy up must be off for empty notes');closeModal()}
    fakeNet({'/api/ai/rewrite':[[200,{text:'Clean new text'}]]});await aiRewriteRun(n.id,'tidy');
    const h2=el('#modal').innerHTML;if(modalKind!=='airew'||!h2.includes('Clean new text')||!h2.includes(esc(n.notes).slice(0,20)))bad.push('rewrite: modal '+modalKind);closeModal();
    fakeNet({'/api/ai/subtasks':[[400,{error:'AI is off (Settings → AI)'}]]});const r=await aiRun('x','/api/ai/subtasks',{id:n.id});
    if(r!==null||modalKind)bad.push('error: result '+JSON.stringify(r)+', modal '+modalKind);
    return bad.join('; ')});
  await asyncCheck('estimate: from history at once; with no history and no AI a message and no window',async()=>{const bad=[],keep=APP.ai;
    const n=allNodes().find(x=>x.kind==='todo');
    fakeNet({'/api/estimate':[[200,{minutes:45,reason:'Similar finished todos: x (40 min).',source:'history',used:[]}]]});await estSuggest(n.id);
    const h=el('#modal').innerHTML;if(modalKind!=='est'||!h.includes('45m')||!h.includes('Similar finished todos')||h.includes('AI guess'))bad.push('history: modal '+modalKind);closeModal();
    APP.ai=false;fakeNet({'/api/estimate':[[200,{minutes:0,reason:'',source:'',used:[]}]]});await estSuggest(n.id);if(modalKind)bad.push('no history: modal '+modalKind);
    APP.ai=true;fakeNet({'/api/estimate':[[200,{minutes:0,reason:'',source:'',used:[]}],[200,{minutes:30,reason:'Guess.',source:'ai',used:[]}]]});await estSuggest(n.id);
    if(modalKind!=='est'||!el('#modal').innerHTML.includes('AI guess'))bad.push('AI guess: modal '+modalKind);closeModal();APP.ai=keep;
    if(!nodeMenu(n).some(i=>i.label==='Suggest estimate…'))bad.push('menu entry');
    return bad.join('; ')});
  await asyncCheck('quick add: rules add at once with Keep as typed; the AI preview shows what the app reads',async()=>{const bad=[],keep=APP.ai,realToast=toast;let last=null;
    toast=(m,o)=>{last={m,o}};
    try{const n=quickAdd('Dentist due:tomorrow at:15:00','','Dentist tomorrow at 3pm');
      if(!n||n.deadline!==addDays(1)+'T15:00'||!last.m.includes('Dentist')||last.o?.action?.label!=='Keep as typed')bad.push('rules: '+JSON.stringify(last));
      last.o.action.fn();const k=allNodes().filter(x=>x.name==='Dentist tomorrow at 3pm');if(k.length!==1||allNodes().some(x=>x.name==='Dentist'&&x.deadline))bad.push('keep as typed: '+k.length);
      k.forEach(x=>{detach(x);delete S.nodes[x.id]});
      APP.ai=true;const long='remind me friday to call anna, important\\n'+'x'.repeat(9000)+' THE END';
      fakeNet({'/api/ai/quick':[[200,{quick:'r: Call Anna due:fri @anna !high'}]]});await aiQuick(long);
      const h=el('#modal').innerHTML;if(modalKind!=='aiquick'||!h.includes('value="r: Call Anna due:fri @anna !high"'))bad.push('preview: modal '+modalKind);
      if(!h.includes(esc(long)))bad.push('dropped text must be kept whole as notes');
      el('#aqLine').value='r: Call Anna due:fri @anna !high';el('#aqLine').oninput();const pv=el('#aqPrev').innerHTML;if(!pv.includes('Call Anna')||!pv.includes('Reminder')||!pv.includes('anna')||!pv.includes('High'))bad.push('preview rows: '+text(pv).slice(0,80));closeModal()}
    finally{toast=realToast;APP.ai=keep}
    return bad.join('; ')});
  await asyncCheck('summarize an attachment: summary, points, dates and to-dos; added to the notes and as sub-tasks',async()=>{const bad=[],keep=APP.ai;APP.ai=true;
    const n=mk('Contract mail');attach(n,'root');n.links.push({title:'mail.eml',uri:'',file:'0123456789abcdef0001.eml'});
    try{ui.sel=n.id;ui.panel=true;ui.prevFor=null;renderDetail();if(!el('#detail').innerHTML.includes('data-act="aisum"'))bad.push('robot button on the attachment');
      fakeNet({'/api/ai/summarize':[[200,{warning:'It asks for a money transfer.',summary:'Lena needs the contract.',points:['Board meeting'],todos:['Sign the contract','Send it'],dates:[{when:'Thursday',what:'due'}]}]]});
      await aiSummarizeFile(n.links[0],n.id);const h=el('#modal').innerHTML;
      if(modalKind!=='aisum'||!h.includes('Lena needs the contract.')||!h.includes('Thursday: due')||!h.includes('Check it before you use it')||!h.includes('⚠ It asks for a money transfer.')||!h.includes('data-astodo'))bad.push('result window: '+modalKind);
      el('#modal [data-asnotes]').onclick({target:{}});const x=S.nodes[n.id];
      if(!x.notes.includes('## AI summary of “mail.eml”')||!x.notes.includes('- [ ] Sign the contract')||!x.notes.includes('### Dates'))bad.push('notes: '+x.notes.slice(0,80));
      closeModal()}
    finally{APP.ai=keep;ui.sel=null;const x=S.nodes[n.id];if(x){x.children.forEach(c=>delete S.nodes[c]);detach(x);delete S.nodes[x.id]}}
    return bad.join('; ')});
  Store.sync=realSync;
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
