# Todomgr – Concept

A small, portable todo + idea manager. One folder, no installation, no system-wide libraries.

## Decisions (as of 2026-11-01)

| Topic | Decision |
|---|---|
| Core | Go, single static binary (daemon + CLI + MCP in one executable) |
| Runs on | Ubuntu (WSL) first; cross-compile for Windows/macOS later |
| Idea vs. todo | One entity with `kind` (`todo` / `idea`); "Promote to todo" keeps history |
| Time tracking | Yes: per-entry time log, estimates, plus a countdown timer with alarm and a stopwatch |
| UI language | English only |
| Extras | All "forgotten features" from the brainstorm are in scope (see below) |

## Architecture

```
 UI (browser / Neutralino window) ─┐
 CLI  (todo add …)                 ├─► core daemon (127.0.0.1:port, token) ─► data/database.json
 MCP  (todo mcp, stdio)            ┘            │                             data/activity.jsonl
                                                └─ SSE live updates           data/backups/
```

* **Single writer.** Only the core touches `database.json`. UI, CLI and MCP are clients.
* **Operations, not full saves.** Clients send small ops (`patch`, `move`, `add`, `trash`) with the
  revision they last saw; stale revision → 409 + refresh.
* **Atomic writes:** temp file → fsync → rename. Snapshots: debounced after change, hourly/daily/weekly
  retention, forced snapshot before bulk or destructive ops. Start-up validates JSON and falls back
  to the newest good snapshot.
* **Live updates:** SSE stream, so AI (MCP) edits show up immediately in the UI.
* **Security:** bind to 127.0.0.1, token; LAN access opt-in. MCP: optional read-only mode, no hard delete.
* **MCP server is OFF by default.** It has an on/off switch in Settings (state persisted in `config.json`).
  When on, it listens on loopback only (127.0.0.1 / ::1, never 0.0.0.0): remote connections are impossible,
  and `Host`/`Origin` headers are validated to block DNS-rebinding from web pages. A token is still required.
  The stdio variant (`todo mcp`) only proxies to the local daemon and refuses to start while MCP is switched off.
* **Toolchain:** Go 1.27.3 is installed in `~/go` (GOROOT) with `GOPATH=~/gopath` in Ubuntu-24.04 (outside the project,
  usable by every project; `~/.profile` puts it on PATH).
* **Context menu** (UI, works in browser and webview): right-click, long-press, Shift+F10 or the ⋯ button on a row.
  Single entry: open/rename/add, status, priority, color, change type, deadline, add tag, move to…, reorder/indent,
  open link, timer, Discord announce, copy as text/ID, duplicate, trash. Multi-select gets a bulk menu.
* **Instance lock** in the data folder (instance id + stale detection) for synced/USB folders.
* **Schema version** in the file, with migrations.
* **WSL note:** the daemon runs in Ubuntu; the UI is opened from Windows via `http://localhost:port`
  (WSL2 forwards localhost). Neutralino on Linux needs webkit2gtk, so a browser app-window
  (`msedge --app=…`) is the default shell; Neutralino stays optional.

## Data model

Flat map with child-id arrays: `nodes: { id: { …, parent, children: [ids] } }`.

Fields: `name` (required), `kind`, `status`, `hist[{s,at}]` (status history → "date per status"),
`color` (swatch), `prio` (0–4), `tags[]`, `people[]`, `location`, `deadline`, `reminder`
(date + optional time), `deps[]`, `notes` (markdown), `links[{title,uri}]`,
`prog{mode: percent|units, done, total, unit}`, `recur`, `est` (minutes), `time[{at,secs}]`,
`collapsed`, `created`, `updated`.

Rules:
* Status is authoritative; progress *suggests* transitions (>0 → in progress, complete → done).
* A node with children has **derived** progress (k of n children); custom units only on leaves.
* "Blocked" is computed from unfinished dependencies, never stored. Dependencies are cycle-checked.
* Delete = trash (restorable). Undo/redo for every change.
* Kinds: `todo`, `idea`, `project` (a project is a top-level grouping node with its own URL and webhook).
* Statuses – todo: open, in progress, waiting, **later** (postponed, no plan yet; hidden from Today), done, cancelled.
  Idea: raw, exploring, parked, rejected (+ Promote to todo). Project: planning, active, on hold, done, archived.
* Kind `reminder`: a pure reminder with no status to manage (e.g. a weekly community stream). Fields: date + time,
  `lead` (minutes before; 0 = only at the time), `recur` (daily/weekly/monthly/yearly), URL, location, notes.
  It pops up `lead` minutes before and again at the time; a recurring one then moves to its next occurrence,
  a one-off stays as "passed". Missed reminders (app was closed) show once as "Missed", with Snooze 10 min.
  Reminders are excluded from progress, Today's todo lists, open counts and dependencies.
  *Core note:* reminders only fire while the daemon/UI is running; the Go core owns the schedule and pushes events (SSE).
* Every entry has an optional `url` (shown as a clickable link icon in the tree). Only http(s) is accepted.
* Projects have an optional Discord `webhook`. When a todo below a project with a webhook is set to done
  (any path: checkbox, status select, bulk, progress reaching 100 %), a dialog offers to post
  title + URL (new line) to the channel; both fields are editable for that message only.
  **In the Go core the webhook call is made server-side** (the secret never leaves the daemon, no CORS issues);
  the prototype calls Discord directly from the browser. The webhook URL is a secret and is part of JSON exports.

## Features

**Core:** nested tree with drag & drop (SortableJS), inline rename everywhere, keyboard moves
(Tab/Shift+Tab indent, Alt+↑/↓), collapse, colors, priorities, tags, people, location,
deadline, reminder, dependencies, notes (markdown-lite), links (drop URLs/files), progress bars.

**Brainstormed extras:** quick capture with inline syntax (`#tag @person !high due:fri ~30m`),
Inbox, Today / Next-up view, search + filters + saved views, command palette (Ctrl+K), multi-select/bulk,
duplicate subtree, templates, archive/hide done, recurring tasks, import/export
(JSON, Markdown outline, CSV, ICS), keyboard shortcuts + cheat sheet, pinning, comment journal,
estimates, mouse-over help everywhere.

**Time:** per-entry log (manual or from timer), estimate vs. tracked, countdown timer
("45 minutes, go") with sound + notification + dialog (+5 min / log / discard), stopwatch mode.
The timer is stored as timestamps, so it survives reloads.

**Statistics:** GitHub-style heatmap (closed / created / activity / time), weekday × hour grid,
KPIs (open, closed this week, overdue, tracked time, streak), cycle time, breakdown by tag.
Hand-written SVG, no chart library.

**UI:** light/dark (`prefers-color-scheme` + override), responsive (3 columns → drawer → bottom sheet),
colour = stripe, priority = flag, status = checkbox; tooltips via one `data-tip` handler.

## Phases

1. **Prototype** – single `index.html`, SortableJS from CDN, `localStorage` behind a `Store` adapter,
   demo data. *(this folder, now)*
2. **Core** – Go daemon: API, SSE, storage, backups, activity log, CLI, MCP; UI switched to API adapter,
   SortableJS vendored (offline).
3. **Packaging** – portable folder per OS (+ optional Neutralino shell).

## Status (2026-11-01)

* Phase 1 done: `prototype/index.html` (standalone, browser storage).
* Phase 2 done (v0.1): Go core in this folder — `web/` UI (embedded, SortableJS vendored), REST API, SSE live updates,
  atomic saves + prev copy + snapshot backups with retention + auto-recovery, lock file, schema versioning with
  migrations (`migrate.go`; newer files are never touched), CLI, MCP (HTTP + stdio bridge; off by default, loopback only),
  unit tests (`go test ./...`), portable builds via `./build.sh`.
* Done since: server-side reminder scheduler (`reminders.go`; state on the entry: `alert`, `preDone`, `atDone`, `remDone`,
  `snoozeUntil`; the page shows `alert` as a pop-up and acknowledges via `POST /api/alerts/ack`), server-side Discord
  posting (`discord.go`, `POST /api/discord`; reminders with `discord:true` post at their time even with no window open;
  webhook URLs are hidden from MCP), Settings → "Start with empty data" (`POST /api/reset`), GitHub link in About.
* Done since: CSV and ICS export (`export.go`; `GET /api/export.csv|.ics`, `todo export FILE.csv|.ics`, Settings → Data).
  ICS: reminders → VEVENT with RRULE + VALARMs, entries with a deadline/reminder → VTODO; floating local times.
* Not yet: Neutralino/window shell (phase 3), LAN access, CSV/ICS import.

## Open items

* Native file-drop paths (Neutralino) to be verified in phase 3; in a browser only file names are available.
* LAN access needs a real login. Today `serveIndex` (server.go) hands the token cookie to anyone who loads the page,
  which is fine on 127.0.0.1 only. With LAN on: give the cookie only to loopback clients; LAN clients get a small
  login page and type the token once. The token is already in `data/config.json` and shown in Settings; also print
  it (or the full URL with it) on daemon start so a headless server can be used.
