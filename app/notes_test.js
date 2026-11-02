// Page tests: runs the md() and bb() code from web/index.html (with the embedded marked) and checks that
// Markdown and BBCode render and that HTML, script links and attribute tricks stay harmless. Also checks
// the page's repeat rules (nextOcc, recurText) against testdata/recur_cases.json, shared with recur_test.go.
// Usage: node notes_test.js  (prints "PASS name" / "FAIL name: why" per case, exit code 1 on failure)
'use strict';
const fs = require('fs'), path = require('path');
const web = path.join(__dirname, 'web');
const html = fs.readFileSync(path.join(web, 'index.html'), 'utf8');
global.marked = require(path.join(web, 'marked.umd.js'));
// entries for the [[id]] link cases
global.S = { nodes: { abc123: { id: 'abc123', name: 'Fix <the> bug' }, tag222: { id: 'tag222', name: '[b]x[/b]' }, old111: { id: 'old111', name: 'Old', trashed: true } } };

// take the page's own esc() and md() code, so the test checks what the browser runs
const cut = (from, to) => {
  const a = html.indexOf(from), b = html.indexOf(to, a);
  if (a < 0 || b < 0) throw new Error('code not found in index.html: ' + from);
  return html.slice(a, b + to.length);
};
const md = new Function(cut('const esc=', ';\n') + cut('// notes preview:', "function md(s){return s?MD.parse(s):''}") + 'return md;')();
const bb = new Function(cut('const esc=', ';\n') + cut('const mdUrl=', ';\n') + cut('const P={', '\n};') + cut('const ic=', ';\n') +
  cut('const RE_ELINK=', ';\n') + cut('function elink(', '</a>`}\n') + cut('// notes preview for BBCode', 'keep[i])}') + 'return bb;')();

const cases = [
  // [name, notes, must contain (all), must not contain (none)]
  ['bold, italic, code', '**b** *i* `c`', ['<strong>b</strong>', '<em>i</em>', '<code>c</code>'], []],
  ['line break kept', 'one\ntwo', ['one<br>two'], []],
  ['nested list and tick box', '- [x] done\n  - inside', ['type="checkbox"', '<ul>\n<li>inside'], []],
  ['table', '| a | b |\n|---|---|\n| 1 | 2 |', ['<table>', '<td>1</td>'], []],
  ['https link', '[t](https://example.com/a?b=1&c=2)', ['<a href="https://example.com/a?b=1&amp;c=2" target="_blank" rel="noopener">t</a>'], []],
  ['mailto link', '[m](mailto:a@b.c)', ['href="mailto:a@b.c"'], []],
  ['plain URL becomes link', 'see https://example.com', ['<a href="https://example.com"'], []],
  ['alert box', '> [!WARNING]\n> Careful **now**', ['<div class="alert warning"><b>Warning</b><p>Careful <strong>now</strong>'], ['[!WARNING]']],
  ['alert box, lower case and empty line', '> [!note]\n>\n> text', ['<div class="alert note"><b>Note</b><p>text'], ['<p></p>']],
  ['all 5 alert types', '> [!TIP]\n> a\n\n> [!IMPORTANT]\n> b\n\n> [!CAUTION]\n> c\n\n> [!NOTE]\n> d\n\n> [!WARNING]\n> e',
    ['alert tip', 'alert important', 'alert caution', 'alert note', 'alert warning'], []],
  ['plain quote stays a quote', '> just a quote', ['<blockquote>'], ['alert']],
  ['code block is escaped', '```\n<script>x</script>\n```', ['&lt;script&gt;'], ['<script']],
  // safety
  ['script tag shows as text', '<script>alert(1)</script>', ['&lt;script&gt;'], ['<script']],
  ['img onerror shows as text', 'x <img src=x onerror=alert(1)>', ['&lt;img'], ['<img']],
  ['raw HTML link shows as text', '<a href="javascript:alert(1)">a</a>', ['&lt;a href='], ['<a ']],
  ['iframe shows as text', '<iframe src="https://evil.example"></iframe>', ['&lt;iframe'], ['<iframe']],
  ['javascript: link is dropped', '[click](javascript:alert(1))', ['click'], ['<a ', 'javascript:']],
  ['JAVASCRIPT: in capitals is dropped', '[click](JaVaScRiPt:alert(1))', ['click'], ['<a ']],
  ['javascript: with HTML entities is dropped', '[click](jav&#x61;script:alert(1))', ['click'], ['<a ']],
  ['data: link is dropped', '[d](data:text/html,<script>alert(1)</script>)', [], ['<a ', '<script']],
  ['javascript: image is dropped', '![p](javascript:alert(1))', ['p'], ['<img']],
  ['https image is kept', '![p](https://example.com/p.png)', ['<img src="https://example.com/p.png" alt="p">'], []],
  ['quote in URL cannot break out', '[t](https://x.y/"onmouseover="alert(1))', [], ['" onmouseover', '"onmouseover="']],
  ['quote in link title cannot break out', '[t](https://x.y "a\\" onmouseover=\\"alert(1)")', [], ['" onmouseover']],
  ['autolink with javascript: is not a link', '<javascript:alert(1)>', [], ['<a ']],
  // [[id]] links to entries
  ['entry link shows the name', 'see [[abc123]]', ['<a class="elink" data-act="goto" data-v="abc123"', '>Fix &lt;the&gt; bug</a>'], ['[[']],
  ['entry link with own text', '[[abc123|my text]]', ['data-v="abc123"', '>my text</a>'], []],
  ['entry link to a trashed or unknown entry', '[[old111]] [[zzz999]]', ['elink gone', '>Old</a>', '>[[zzz999]]</a>'], ['data-v=']],
  ['entry link inside code stays text', '`[[abc123]]`', ['<code>[[abc123]]</code>'], ['elink']],
  ['entry link text cannot inject HTML', '[[abc123|<img src=x onerror=alert(1)>]]', ['&lt;img'], ['<img']],
];

const bbCases = [
  ['bbcode: inline tags', '[b]B[/b] [i]I[/i] [u]U[/u] [s]S[/s] [small]sm[/small] x[sup]2[/sup] H[sub]2[/sub]',
    ['<strong>B</strong>', '<em>I</em>', '<u>U</u>', '<s>S</s>', '<small>sm</small>', '<sup>2</sup>', '<sub>2</sub>'], []],
  ['bbcode: nested tags', '[b]bold [i]both[/i][/b]', ['<strong>bold <em>both</em></strong>'], []],
  ['bbcode: headings without extra breaks', 'intro\n[h1]Title[/h1]\ntext', ['intro<h1>Title</h1>text'], ['<br><h1>']],
  ['bbcode: line breaks', 'one\ntwo[br]three', ['one<br>two<br>three'], []],
  ['bbcode: lists', '[ul]\n- first\n- second\n[/ul]\n[ol]\n- a\n[/ol]', ['<ul><li>first</li><li>second</li></ul>', '<ol><li>a</li></ol>'], []],
  ['bbcode: colour and background', '[color:red|#ff0]x[/color]', ['<span style="color:red;background:#ff0;">x</span>'], []],
  ['bbcode: quote with author', '[quote]Words.|Mira[/quote]', ['<blockquote>Words.<div class="bb-by">— Mira</div></blockquote>'], []],
  ['bbcode: spoiler with label', '[spoiler]secret|Click me![/spoiler]', ['<details class="bb-spoiler"><summary>Click me!</summary>secret</details>'], []],
  ['bbcode: aloud, code, drop cap', '[aloud]Read this[/aloud][code]a\nb[/code][dc]D[/dc]rop', ['<div class="bb-aloud">Read this</div>', '<div class="bb-code">a b</div>', '<span class="bb-dc">D</span>rop'], []],
  ['bbcode: comment is hidden', 'a /* hidden\ntext */ b', ['a  b'], ['hidden']],
  ['bbcode: noparse keeps tags as text', '[noparse][b]x[/b][/noparse]', ['[b]x[/b]'], ['<strong>']],
  ['bbcode: icon', '[icon:star] [icon:nosuchicon]', ['<span class="tic"><svg', '[icon:nosuchicon]'], []],
  ['bbcode: unknown or unclosed tags stay text', '[b]open [x]y[/x]', ['[b]open [x]y[/x]'], ['<strong>']],
  ['bbcode: link', '[url:https://www.worldanvil.com/w/x?a=1&b=2]My world[/url]', ['<a href="https://www.worldanvil.com/w/x?a=1&amp;b=2" target="_blank" rel="noopener">My world</a>'], []],
  ['bbcode: link without text shows the URL', '[url:https://example.com][/url]', ['>https://example.com</a>'], []],
  ['bbcode: tags inside link text', '[url:https://example.com][b]bold[/b][/url]', ['rel="noopener"><strong>bold</strong></a>'], []],
  // safety
  ['bbcode: javascript: link is dropped', '[url:javascript:alert(1)]click[/url] [url:JaVaScRiPt:x]b[/url] [url:data:text/html,x]c[/url]', ['click', 'b', 'c'], ['<a ', 'javascript:', 'data:']],
  ['bbcode: quote in URL cannot break out', '[url:https://x.y/" onmouseover="alert(1)]t[/url]', ['&quot;'], ['" onmouseover']],
  ['bbcode: HTML shows as text', '<script>alert(1)</script><img src=x onerror=alert(1)>', ['&lt;script&gt;', '&lt;img'], ['<script', '<img']],
  ['bbcode: colour cannot inject CSS', '[color:red;background:url(https://evil.example/x)]a[/color]', ['a'], ['url(', 'style=']],
  ['bbcode: colour cannot break the attribute', '[color:red" onmouseover="alert(1)]a[/color]', [], ['" onmouseover', 'style="color:red"']],
  ['bbcode: noparse cannot smuggle HTML', '[noparse]<script>x</script>[/noparse]', ['&lt;script&gt;'], ['<script']],
  ['bbcode: fake placeholder from the text', 'a\u00000\u0000b[noparse]<i>[/noparse]', ['a0b'], ['<i>']],
  ['bbcode: entry links', '[b][[abc123]][/b] [[abc123|text]] [[zzz999]]', ['<strong><a class="elink" data-act="goto" data-v="abc123"', '>text</a>', 'elink gone'], []],
  ['bbcode: tags in an entry name stay text', '[[tag222]]', ['>[b]x[/b]</a>'], ['<strong>']],
  ['bbcode: entry link text stays harmless', '[[abc123|<b onclick=alert(1)>]]', ['&lt;b onclick'], ['<b onclick']],
  ['bbcode: tags inside a spoiler label stay harmless', '[spoiler]x|<b onclick=alert(1)>[/spoiler]', ['&lt;b onclick'], ['<b onclick']],
];

let failed = 0;
for (const [fn, list] of [[md, cases], [bb, bbCases]]) for (const [name, src, want, never] of list) {
  let out, why = '';
  try { out = fn(src); } catch (e) { why = 'error: ' + e.message; }
  if (!why) why = want.filter(w => !out.includes(w)).map(w => 'missing ' + JSON.stringify(w))
    .concat(never.filter(n => out.includes(n)).map(n => 'contains ' + JSON.stringify(n))).join(', ');
  if (why) { failed++; console.log(`FAIL ${name}: ${why} in ${JSON.stringify(out)}`); } else console.log('PASS ' + name);
}
// repeat rules: the page must compute the same next dates as the server
const rc = new Function(cut('const pad=', ';\n') + cut('const dayKey=', ';\n') + cut('function pd(s)', '}\n') +
  cut('const RECUR=', '[1]}`}\n') + cut('// next date of a repeating entry', '\n  return out(d)}') + 'return {nextOcc, recurText, normRecur, afterDone};')();
const check = (name, bad) => { if (bad.length) { failed++; console.log(`FAIL ${name}: ${bad.join('; ')}`); } else console.log('PASS ' + name); };
const rcCases = JSON.parse(fs.readFileSync(path.join(__dirname, 'testdata', 'recur_cases.json'), 'utf8')).cases;
check(`repeat: next dates (${rcCases.length} cases shared with Go)`, rcCases.filter(([d, r, want]) => rc.nextOcc(d, r) !== (want || d))
  .map(([d, r, want]) => `nextOcc(${d}, ${r}) = ${rc.nextOcc(d, r)}, want ${want || d}`));
check('repeat: labels', [['w', 'weekly'], ['2w', 'every 2 weeks'], ['3d', 'every 3 days'], ['m:1fr,3fr,5fr', 'monthly on the 1st, 3rd and 5th Friday'],
  ['m:-1fr', 'monthly on the last Friday'], ['m:1mo,-1su', 'monthly on the 1st Monday and last Sunday'], ['x', '']]
  .filter(([r, want]) => rc.recurText(r) !== want).map(([r, want]) => `recurText(${r}) = ${rc.recurText(r)}, want ${want}`));
check('repeat: input', [['weekly', 'w'], [' 2W ', '2w'], ['1w', 'w'], ['m:1FR', 'm:1fr'], ['biweekly', null], ['m:6fr', null], ['0d', null]]
  .filter(([v, want]) => rc.normRecur(v) !== want).map(([v, want]) => `normRecur(${v}) = ${rc.normRecur(v)}, want ${want}`));
const adCases = JSON.parse(fs.readFileSync(path.join(__dirname, 'testdata', 'recur_cases.json'), 'utf8')).afterDone;
check(`repeat: after done (${adCases.length} cases shared with Go)`, adCases.filter(([dl, rem, r, today, wd, wr]) => rc.afterDone(dl, rem, r, today).join('|') !== wd + '|' + wr)
  .map(([dl, rem, r, today, wd, wr]) => `afterDone(${dl}, ${rem}, ${r}, ${today}) = ${rc.afterDone(dl, rem, r, today)}, want ${wd},${wr}`));
check('repeat: after done rules', [['3D+', '3d+'], ['weekly+', 'w+'], ['m:1fr+', 'm:1fr+'], ['+', null], ['d++', null]]
  .filter(([v, want]) => rc.normRecur(v) !== want).map(([v, want]) => `normRecur(${v}) = ${rc.normRecur(v)}, want ${want}`)
  .concat([['3d+', '3 days after done'], ['w+', '1 week after done'], ['m:1fr+', 'monthly on the 1st Friday after done']]
    .filter(([r, want]) => rc.recurText(r) !== want).map(([r, want]) => `recurText(${r}) = ${rc.recurText(r)}, want ${want}`)));

// events with an end: range text and moving the end with the start
const ev = new Function(cut('const pad=', ';\n') + cut('const dayKey=', ';\n') + cut('function pd(s)', '}\n') + cut('const startOfToday=', ';\n') +
  cut('const MON=', ';\n') + cut('function fmtWhen(s)', '\n}') + cut('// "Sat 19:00–21:00"', 'fmtWhen(e)}') + cut('// moves the end of an event', "'')}") +
  'return {whenRange, shiftEnd};')();
check('events: range text', [[{reminder: '2030-12-05T19:00', end: '2030-12-05T21:00'}, '5 Dec 2030 19:00–21:00'], [{reminder: '2030-12-05', end: '2030-12-07'}, '5–7 Dec 2030'],
  [{reminder: '2030-12-30', end: '2031-01-02'}, '30 Dec 2030 – 2 Jan 2031'], [{reminder: '2030-12-05T19:00', end: ''}, '5 Dec 2030 19:00']]
  .filter(([n, want]) => ev.whenRange(n) !== want).map(([n, want]) => `whenRange(${n.reminder}, ${n.end}) = ${ev.whenRange(n)}, want ${want}`));
check('events: end moves with the start', [['2030-12-05T19:00', '2030-12-12T19:00', '2030-12-05T21:00', '2030-12-12T21:00'], ['2030-12-05', '2030-12-06', '2030-12-07', '2030-12-08'],
  ['2027-03-27T19:00', '2027-04-03T19:00', '2027-03-27T21:00', '2027-04-03T21:00'], ['2030-12-05T19:00', '', '2030-12-05T21:00', '2030-12-05T21:00']]
  .map(([a, b, e, want]) => { const n = {end: e}; ev.shiftEnd(n, a, b); return n.end === want ? '' : `shiftEnd(${a} → ${b}) end ${n.end}, want ${want}`; }).filter(Boolean));

// Today's plan at 10:00 with a 09:00–17:00 work day: estimates minus tracked time (a parent with sub-items
// does not count), closed and undated todos and all-day events left out, events cut to the rest of the day
{
  const t0 = new Date(); t0.setHours(0, 0, 0, 0);
  const k = `${t0.getFullYear()}-${String(t0.getMonth() + 1).padStart(2, '0')}-${String(t0.getDate()).padStart(2, '0')}`;
  const N = {
    a: {id: 'a', kind: 'todo', status: 'open', est: 120, time: [{secs: 1800}], deadline: k, parent: 'r'},
    p: {id: 'p', kind: 'todo', status: 'open', est: 600, time: [], deadline: k, parent: 'r'},
    c: {id: 'c', kind: 'todo', status: 'open', est: 60, time: [], deadline: k, parent: 'p'},
    x: {id: 'x', kind: 'todo', status: 'done', est: 500, time: [], deadline: k, parent: 'r'},
    l: {id: 'l', kind: 'todo', status: 'open', est: 500, time: [], deadline: '', parent: 'r'},
    e: {id: 'e', kind: 'reminder', reminder: k + 'T08:00', end: k + 'T11:00', parent: 'r'},
    f: {id: 'f', kind: 'reminder', reminder: k + 'T16:00', end: k + 'T18:00', parent: 'r'},
    g: {id: 'g', kind: 'reminder', reminder: k, end: k, parent: 'r'},
  };
  const env = {S: {prefs: {}, nodes: N}, now: () => new Date(t0).setHours(10), startOfToday: () => new Date(t0),
    allNodes: () => Object.values(N), isClosed: n => n.status === 'done', dueState: n => n.deadline ? 'today' : ''};
  const plan = new Function(...Object.keys(env), cut('const pad=', ';\n') + cut('function pd(s)', '}\n') + cut('const fmtDur=', ';\n') +
    cut('const inSubtree=', ';\n') + cut('const remAt=', ';\n') + cut('const evEnd=', ';\n') + cut("// Today's plan, in minutes", '</div></div>`}') +
    'return {dayPlan, planBar};')(...Object.values(env));
  const r = plan.dayPlan(), text = plan.planBar().replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ');
  check('today: plan', [r.left === 420 ? '' : `left ${r.left}, want 420`, r.tasks === 150 ? '' : `tasks ${r.tasks}, want 150`,
    r.events === 120 ? '' : `events ${r.events}, want 120`, text.includes('2h 30m free') ? '' : 'text: ' + text].filter(Boolean));
  env.S.prefs.workDay = {from: '09:00', to: '12:00'};
  check('today: too much', [plan.planBar().includes('1h 30m too much') ? '' : 'no warning'].filter(Boolean));
}

process.exit(failed ? 1 : 0);
