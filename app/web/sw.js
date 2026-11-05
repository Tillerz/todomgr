// Service worker: makes the page installable as an app ("Install" in Chrome / Edge) and shows a short page
// when the program is not running. It stores nothing: every request goes to the program as before.
const OFFLINE = `<!DOCTYPE html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Tillerz Todo Mgr</title><body style="margin:0;min-height:100vh;display:grid;place-items:center;font:16px/1.5 system-ui,sans-serif;background:#12151c;color:#e6e9f0;padding:16px">
<div style="max-width:420px"><h2>Todo Mgr is not running</h2><p>Start the program on this computer, then reload this window (F5).</p></div>`;
self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', e => e.waitUntil(self.clients.claim()));
self.addEventListener('fetch', e => {
  if (e.request.mode !== 'navigate') return; // API calls, scripts and live updates go to the program untouched
  e.respondWith(fetch(e.request).catch(() => new Response(OFFLINE, {headers: {'Content-Type': 'text/html; charset=utf-8'}})));
});
