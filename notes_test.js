// Notes preview tests: runs the md() code from web/index.html with the embedded marked and checks
// that markdown renders and that HTML, script links and attribute tricks in notes stay harmless.
// Usage: node notes_test.js  (prints "PASS name" / "FAIL name: why" per case, exit code 1 on failure)
'use strict';
const fs = require('fs'), path = require('path');
const web = path.join(__dirname, 'web');
const html = fs.readFileSync(path.join(web, 'index.html'), 'utf8');
global.marked = require(path.join(web, 'marked.umd.js'));

// take the page's own esc() and md() code, so the test checks what the browser runs
const cut = (from, to) => {
  const a = html.indexOf(from), b = html.indexOf(to, a);
  if (a < 0 || b < 0) throw new Error('code not found in index.html: ' + from);
  return html.slice(a, b + to.length);
};
const md = new Function(cut('const esc=', ';\n') + cut('// notes preview:', "function md(s){return s?MD.parse(s):''}") + 'return md;')();

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
];

let failed = 0;
for (const [name, src, want, never] of cases) {
  let out, why = '';
  try { out = md(src); } catch (e) { why = 'error: ' + e.message; }
  if (!why) why = want.filter(w => !out.includes(w)).map(w => 'missing ' + JSON.stringify(w))
    .concat(never.filter(n => out.includes(n)).map(n => 'contains ' + JSON.stringify(n))).join(', ');
  if (why) { failed++; console.log(`FAIL ${name}: ${why} in ${JSON.stringify(out)}`); } else console.log('PASS ' + name);
}
process.exit(failed ? 1 : 0);
