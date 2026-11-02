# Tillerz Todo Mgr

A small, portable todo, idea and project manager. One program, no installation: the UI, the API, the
command line and the (optional) MCP server for AI assistants are all in one executable. The UI runs in
your web browser: start the program and it opens `http://localhost:7437/`. The page comes from your own
computer, so you need no internet connection and no account. Everything you enter is stored in the
`data/` folder next to the program, so you can copy the folder to a USB stick or a synced drive and take
it with you.

![The tree with the detail panel, dark mode](docs/dark.png)

More in [Screenshots](#screenshots).

## Contents

- [Run it](#run-it)
- [Features](#features)
- [Where the data is, and how it is kept safe](#where-the-data-is-and-how-it-is-kept-safe)
- [Command line](#command-line)
- [Build](#build)
- [Screenshots](#screenshots)
- [MCP server for AI assistants](#mcp-server-for-ai-assistants)
- [HTTP API](#http-api)
- [Licence](#licence)
- [Third-party licences](#third-party-licences)

## Run it

```
./todo                 # start and open the browser   (todo.exe on Windows)
./todo serve           # start only; open http://localhost:7437/
./todo serve --data /path/to/data --port 7437
```

Stop it with Ctrl+C. Every change is saved immediately; nothing is lost if the program is killed.
The program listens on `127.0.0.1` only, so it can only be reached from the same computer.

**macOS:** the program is not signed by Apple, so macOS blocks it on the first start. Right-click `todo` in
Finder and choose **Open**, or run `xattr -d com.apple.quarantine todo` once in the program folder.

## Features

### Entries

Everything is an entry in one tree. An entry has a **type**:

| Type | For | Statuses |
|---|---|---|
| **Todo** | something to do | open · in progress · waiting · later · done · cancelled |
| **Idea** | something that might become a todo ("Promote to todo" keeps its history) | raw · exploring · parked · rejected |
| **Project** | a top-level grouping with its own URL and Discord webhook | planning · active · on hold · done · archived |
| **Reminder** | a pure reminder with no status (a weekly stream, a deadline to remember) | – |

"Later" means *postponed with no plan yet*: such todos are hidden from Today but not forgotten.

Each entry can have: a name, a colour, a priority (none, low, medium, high, urgent), tags, people,
a location, a deadline, a reminder, dependencies, notes, a URL, any number of links and a progress
value. Every status change is recorded with its date, so you can always see when something was opened,
started and finished.

**Notes** use GitHub Markdown: headings, bold and italic, lists and tick boxes, tables, code blocks,
quotes and links. A single line break stays a line break. GitHub's notice boxes work too
(`[!NOTE]`, `[!TIP]`, `[!IMPORTANT]`, `[!WARNING]`, `[!CAUTION]`):

```
> [!WARNING]
> Check the map scale before the next session.
```

Notes can use **BBCode** instead, as on World Anvil: click the **Markdown** button above the notes to
switch that entry to BBCode. Supported: `[b]` `[i]` `[u]` `[s]` `[small]` `[sup]` `[sub]` `[dc]` (drop cap),
`[color:red|yellow]`, `[h1]`–`[h4]`, `[quote]…|Author[/quote]`, `[aloud]`, `[code]`, `[spoiler]…|Label[/spoiler]`,
`[url:https://…]text[/url]`, `[br]`, `[ul]` / `[ol]` with `- item` lines, `[noparse]`, `/* hidden comments */` and `[icon:name]` with any
icon name from Settings → Tag icons. Other World Anvil tags (mentions, sections, containers) stay as text.

Click **Preview** above the notes to see the result. HTML in notes is shown as plain text, and only
`http`, `https` and `mailto` links become links, so a note added through the API or MCP can never run
code in your browser.

For long notes, click **Expand** (or press **Alt+Enter**) to edit them in a large window. **Esc** brings
them back to the sidebar.

### The tree

- Nested entries of any depth, **drag and drop** to reorder or re-parent (SortableJS). Rows keep their
  height while you drag.
- Rename in place, collapse and expand single branches or **all at once**.
- **Multi-select:** Ctrl+click adds or removes single entries; Shift+click selects every visible entry from
  the current one to the clicked one (entries inside collapsed branches are left out). Esc clears the
  selection.
- A coloured stripe shows the colour of an entry, and the stripe also spans its sub-entries.
- Keyboard: Tab / Shift+Tab to indent and outdent, Alt+↑/↓ to move, Space to toggle done, Ctrl+K for the
  command palette. A cheat sheet is under ⋯ → Keyboard shortcuts.
- **Right-click menu** (or long-press on touch screens, Shift+F10, or the ⋯ button on a row) with: open,
  rename, add sub-entry, status, priority, colour, change type, deadline, add tag, move to…, reorder and
  indent, open link, start a timer, announce on Discord, print list, copy as text / copy ID, duplicate, trash.
  With several entries selected, right-click one of them: the menu then acts on all of them (mark done,
  status, priority, colour, add or remove a tag, add a person in charge, move to…, trash). Each entry only
  takes a status that its type has.
- A link icon after the name opens the entry's URL (or its single attached link) directly.
- **Tag icons:** entries with certain tags show an icon before the name, for example `#meeting`, `#stream`,
  `#nsfw` (18+), `#call`, `#mail`, `#bug`, `#research` and `#review`. Change the list under Settings.
- A page icon in the row shows that an entry has **notes**. Hover the icon or the name to read the first
  lines of the notes without opening the entry.
- Undo and redo for every change. Deleting only moves an entry (with everything below it) to the **trash**,
  from where it can be restored.

### Quick add

The field above the tree adds to the **Inbox** and understands a small syntax:

```
Call Anna #work @anna !high due:fri at:14:00 ~30m
```

| Part | Meaning |
|---|---|
| `#tag` `@person` | tags and people |
| `!low` `!med` `!high` `!urgent` | priority |
| `due:today` `due:tomorrow` `due:fri` `due:+3d` `due:2026-12-24` | deadline |
| `at:19:00` | time of day |
| `repeat:daily` `repeat:weekly` `repeat:monthly` `repeat:yearly`, `repeat:2w` (every 2 weeks), `repeat:m:1fr,3fr` (1st and 3rd Friday) | recurrence |
| `~30m` `~2h` | time estimate |
| `?` at the start | an idea |
| `r:` at the start | a reminder |

The up and down arrow keys step through your earlier entries, so you can edit and re-send one.

When you type `#` or `@`, a list of your existing tags or people opens, so `#Work` does not become a
second `#work`. Pick one with the arrow keys and **Enter** or **Tab**, or with a click. **Esc** closes
the list.

### Progress

- **Percent** or **units** ("450 of 1200 words"), set per entry.
- An entry with sub-entries shows the **derived** progress of its children, as a bar in the tree.
- Setting an entry to done fills its own bar.
- **Dependencies:** an entry that depends on unfinished entries is shown as blocked. Dependency cycles are
  refused.
- **Recurring** todos reopen with the next deadline instead of staying done. Repeat daily, weekly,
  monthly or yearly, **every N** days, weeks, months or years (every 2 weeks, every 4 weeks…), or
  **monthly on certain weekdays** (the 1st, 3rd and 5th Friday; the last Sunday). Pick *Custom…* in the
  Repeat field for the last two; a month without a 5th Friday is simply skipped.

### Reminders

Reminders are scheduled by the **program itself**, not by the browser page, so they are noticed even when
no window is open.

- A **reminder entry** has a date and time, and pops up *at the time*, plus (if you want) an early warning
  1 day / 2 h / 1 h / 30 / 15 / 10 / 5 minutes before. A recurring reminder then moves on to its next
  occurrence by itself; a one-off one stays in the tree as "passed".
- **Events with an end:** give a reminder an optional *Ends* time, for a stream from 19:00 to 21:00 or a
  convention over three days. The tree shows "Sat 19:00–21:00" or "5–7 Dec", Today lists the event on
  every day it runs, and the pop-up stays at the start. Moving the start moves the end along.
- A normal todo, idea or project can also have a **reminder time** and pops up once.
- A pop-up offers *Open link*, *Snooze 10 min* and *Dismiss* (and *Show* for tasks). If the program was off or no
  window was open, the pop-up waits and appears as **Missed reminder** the next time you open the page.
- When several windows are open, dismissing in one dismisses it everywhere.
- Optionally the browser shows a desktop notification (⋯ → Enable notifications) and plays a sound.
- A reminder inside a project that has a Discord webhook can also be **posted to Discord** at its time –
  even when no window is open (switch in the reminder's panel).

### Time tracking and the timer

- Log time by hand or from the timer; each entry shows *estimate vs. tracked*.
- The **timer** counts down ("45 minutes, go") or counts up as a stopwatch, optionally attached to an
  entry. When it runs out there is a sound, a flashing title, a notification and a dialog with
  *Log time & close*, *+5 min*, *+15 min* and *Discard*. It survives reloads, because it is stored as
  timestamps.

### Today, search and views

- **Today** gathers upcoming reminders, overdue and due-today items, reminders that fired and what is in
  progress. "Later" items are left out.
- **Search** with filters (open, done, blocked, overdue, ideas, todos, reminders, projects, later,
  today, tags, people) and **saved views**.
- A command palette (Ctrl+K) for every action.

### Projects and Discord

A project can have a **URL** (for example your World Anvil world) and a **Discord webhook**.

- When a todo below such a project is set to done – by the checkbox, the status menu, a bulk action or
  progress reaching 100 % – a dialog offers to **announce it on Discord**: the title is filled in, the URL
  goes on the line below, and both can be edited before sending.
- There is also *Announce on Discord…* in the right-click menu, and a **Test** button next to the webhook.
- The posting is done by the program, not by the browser: the webhook URL (which is a secret) is never used
  from the page, and nothing is mentioned with `@everyone`. The program only ever contacts
  `discord.com` webhook URLs over https.

### Statistics

A page with a GitHub-style **heatmap** (closed, created, activity or time), a weekday × hour grid, key
numbers (open, closed this week, overdue, tracked time, streak), cycle time and a breakdown by tag. It is
drawn without a chart library, from the activity log.

### Export and import

Under ⋯ (and in the command palette) and in Settings → Data:

| Export | For | Contents |
|---|---|---|
| **JSON** | backup and moving to another computer | everything, including the activity log; can be imported again |
| **CSV** | Excel, LibreOffice, Google Sheets | one row per entry (including finished ones): id, parent id, path, name, type, status, priority, progress, deadline, reminder, end, repeat, tags, people, location, URL, estimate and tracked minutes, created / updated / closed time, notes. UTF-8 with a byte order mark, so umlauts open correctly; text that starts with `=`, `+`, `-` or `@` is prefixed with `'` so a spreadsheet never runs it as a formula. |
| **Calendar (.ics)** | Outlook, Google / Apple Calendar, Thunderbird, task apps | open entries that have a date: **reminders** become events (with their repeat rule and alarms: early warning and at the time); todos, ideas and projects with a deadline or reminder time become **tasks** (due date, status, priority, tags, notes, link, alarm). Times are "floating", so a calendar shows the same clock time as the app. |
| **Markdown outline** | notes, wikis | the tree as a nested checklist |

The calendar file is a snapshot, not a subscription: export it again after changes. From the command line,
the format follows the file name: `todo export plan.csv`, `todo export plan.ics`. By default the CSV
includes finished entries and the calendar does not; add `--open` or `--closed` to change that.

**Print:** right-click an entry → *Print list…* prints it with everything below it: name, status,
priority, due date, people, tags and the first lines of the notes, indented like the tree, with tick
boxes. The browser's print dialog can also save it as a PDF.

**Import JSON** reads this app's export and replaces everything after taking a backup.

**Import CSV / calendar** (⋯ → *Import CSV / calendar…*, or `todo import FILE.csv`) only **adds** entries.
They go into a new project "Import: file name, date"; nothing that exists is changed. Importing the same
file twice gives the entries twice.

- **Calendar files (.ics)**, for example from Outlook, Google, Apple or Thunderbird: tasks become **todos**
  (title, description, due date, status, priority, categories as tags, location, link, repeat, progress,
  alarm), events become **reminders** (start and end time, repeat, the alarm as early warning). Sub-tasks stay
  sub-entries. Times in UTC or another time zone are converted to local time. **Past one-time events are
  skipped**; repeating events start at their next date. A past alarm of a task goes into its notes instead
  of popping up.
- **CSV files:** this app's own CSV export comes back with its tree. Files from other tools or from Excel
  work too: the columns are found by name (`name`/`title`/`task`, `notes`/`description`, `due`/`deadline`,
  `tags`/`labels`, `priority`, `status`, `assignee`, …). Commas, semicolons and tabs work, and so do
  Excel files saved in the Windows encoding. Dates such as `2026-11-05` or `05.11.2026` are read.
- Anything that has no field here (an unknown date format, a repeat rule such as "only 5 times") is
  written into the notes, so nothing is lost silently.

### Look and feel

- Light and dark mode (follows the system, can be switched), dark scroll bars.
- Responsive: three columns on a wide screen, a drawer on a medium one, a bottom sheet on a phone.
- Tooltips on every control, a row-height slider, a resizable and closable detail panel, a *Hide tags*
  button.
- Works offline: SortableJS, marked (Markdown) and the Lucide tag icons are bundled, no CDN.

### Settings (⋯ → Settings)

- **Row height** of the tree.
- **Tag icons:** which tag shows which icon. Click an icon to choose another one; *Reset* restores the default list.
- **MCP server** on/off (off by default) and read-only switch, with the endpoint, token and a ready-made
  client configuration to copy.
- **Backups:** the snapshot interval, *Back up now* and one-click restore from the list.
- **Data:** export JSON / CSV / calendar, import JSON, load demo data, and **Start with empty data** – clears everything
  (after taking a backup) without restarting the program. Your settings stay.
- **About:** version (linked to the GitHub project), data schema version, time zone and data folder.

### Several clients at once

The browser, the command line and AI assistants can all work at the same time. The program is the only
writer of the data; a change made by one shows up in the others within a moment. If two of them change the
same entry simultaneously, the page tells you and shows the latest data instead of overwriting anything.

## Where the data is, and how it is kept safe

```
data/database.json        the entries (always valid JSON; contains "schema" and "app" version)
data/database.prev.json   the state one save ago
data/activity.jsonl       append-only log of what was done and when (feeds the statistics)
data/config.json          port, access token, MCP switch, backup interval, time zone
data/backups/             snapshots (auto every 10 min when changed, manual, and before import/restore/reset/migration)
```

- A single process owns the files; a lock file stops a second one from starting on the same folder.
- Every save is written to a temporary file, fsynced and renamed, so a crash cannot leave half a file.
- Every change is validated before it is kept; an invalid one is rolled back.
- A damaged data file is replaced automatically from the newest good copy (the damaged one is kept as
  `database.corrupt-….json`).
- Backups: everything from the last 2 hours, then hourly (24 h), daily (30 d), weekly (26 w). Safety copies
  taken before an import, restore, reset or upgrade are kept for at least 7 days.

**Upgrades:** the data file carries its structure version (`schema`). A newer app migrates an older file
automatically, after taking a `pre-migrate` backup. A file from a *newer* app is never touched: the program
refuses to start and tells you to update. (Adding optional fields, like the reminder bookkeeping, needs no
migration: a missing field simply means "off".)

**Time zone:** dates such as `due:today` use the computer's time zone. To use another one, set `timezone`
in `data/config.json`, for example `Europe/Berlin`.

## Command line

The app must be running.

```
todo add "Call Anna #work @anna !high due:fri at:14:00 ~30m"     # quick add into the Inbox
todo add "? Podcast about local history"                         # an idea
todo add "r: Community stream at:19:00 due:thu repeat:weekly"    # a reminder
todo list --project "Novel"   |   todo tree   |   todo today   |   todo stats
todo done ID   |   todo set ID deadline=2026-12-24 tags=a,b progress=450/1200:words
todo mv ID --to PARENT   |   todo rm ID   |   todo restore ID   |   todo log ID 45m
todo backup   |   todo backups   |   todo import backup.json --yes   |   todo import plan.csv   |   todo import calendar.ics
todo export backup.json   |   todo export plan.csv   |   todo export plan.ics [--open|--closed]
todo help
```

## Build

```
./build.sh          # tests + portable builds for Linux, Windows and macOS into dist/, plus release archives
./test.sh           # all tests: Go tests, notes preview safety tests (Node), end-to-end run (temp data, 127.0.0.1 only)
./release.sh        # after build + test + commit: tag v<Version>, push, publish dist/ archives (needs gh)
go test ./...
./todo serve --ui app/web     # develop the UI: serves app/web/ from disk instead of the embedded copy
```

All program code is in `app/`: the Go files and the web page (`app/web/`), which is built into the program.
Go (standard library only) is the only requirement to build. `./test.sh` also needs Node.js for the notes
preview tests (`node app/notes_test.js`).

## Screenshots

### The tree with the detail panel, light mode

![The tree with the detail panel, light mode](docs/tree.png)

### The detail panel of a reminder, with a custom repeat rule

<img alt="The detail panel of a reminder with a custom repeat rule" src="docs/detail-pane.png" width=400px>

### The Today view with reminders

<img alt="The Today view with reminders" src="docs/today.png" width=65%>

### The statistics page with the heatmap

<img alt="The statistics page with the heatmap" src="docs/stats.png" width=70%>

### The narrow layout

<img alt="The narrow layout" src="docs/narrow.png" width=350px>

### The command palette

<img alt="The command palette" src="docs/command-palette.png" width=450px>

### The writing/work timer

<img alt="The writing/work timer" src="docs/timer.png" width=300px>

### The settings page

<img alt="The settings page" src="docs/settings.png" width=450px>

### The menu (top right icon)

<img alt="The menu" src="docs/menu.png" width=350px>

### The printable list

<img alt="The printable list" src="docs/print-list.png" width=450px>

### Notes in BBCode (World Anvil style)

<img alt="Notes in BBCode, preview" src="docs/notes-bbcode.png" width=400px>

---

## MCP server for AI assistants

**Off by default.** Switch it on in the app: ⋯ → Settings → MCP server. It listens only on `127.0.0.1`
(this computer), checks the `Host` and `Origin` headers (which defeats attacks from web pages) and needs the
access token shown in Settings. A **read-only** switch lets AI tools look but not change anything. There is
no hard delete: AI tools can only move entries to the trash, and a Discord webhook URL is hidden from them
(they see `(set)`); they cannot post to Discord.

* HTTP: `http://127.0.0.1:7437/mcp` with the header `Authorization: Bearer <token>`
* stdio clients: command `todo mcp` (bridges to the running app; refuses to start while MCP is off)

Tools: `list_entries`, `get_entry`, `get_tree`, `today`, `stats`, `create_entry` (also accepts the quick-add
text), `update_entry`, `set_status`, `move_entry`, `trash_entry`, `restore_entry`, `log_time`.

## HTTP API

The UI, the CLI and MCP all use the same HTTP API on `127.0.0.1:7437`. Every call needs the token
(`Authorization: Bearer <token>`, or `X-Todo-Token`; the page itself uses a cookie). The token is in
`data/config.json` and in Settings. `POST`, `PUT` and `PATCH` bodies must be `application/json`.
Requests whose `Host` or `Origin` is not local are refused.

| Method and path | Purpose |
|---|---|
| `GET /api/info` | name, version, schema, data folder, port |
| `GET /api/state` | everything the UI needs: entries, timer, settings, activity |
| `POST /api/sync` | the UI's batched changes (with conflict detection) |
| `GET /api/events` | live updates (Server-Sent Events: `hello`, `change`, `reload`) |
| `GET /api/nodes` | list and search entries (`q`, `tag`, `person`, `parent`, `project`, `status`, `kind`, `open`, `overdue`, `trashed`, `limit`) |
| `POST /api/nodes` | create an entry (`name`, `parent`, `index`, and any field) |
| `GET /api/nodes/{id}` | one entry with its path and computed values |
| `PATCH /api/nodes/{id}` | change fields |
| `DELETE /api/nodes/{id}` | move to the trash (never a hard delete) |
| `POST /api/nodes/{id}/status` | `{status}` |
| `POST /api/nodes/{id}/move` | `{parent, index}` |
| `POST /api/nodes/{id}/restore` | take out of the trash |
| `POST /api/nodes/{id}/time` | `{minutes}` or `{seconds}` – log time |
| `POST /api/quick` | `{text, parent}` – quick-add syntax |
| `GET /api/tree`, `/api/today`, `/api/stats` | the tree (`root`, `depth`), the Today lists, statistics (`days`) |
| `POST /api/alerts/ack` | `{id, action: "ok" \| "snooze"}` – dismiss or snooze a reminder pop-up |
| `POST /api/discord` | `{id, title, url}` – post to the Discord channel of the entry's project |
| `GET`/`PUT /api/settings` | MCP switches, backup interval |
| `GET`/`POST /api/backups`, `POST /api/backups/restore` | list, create, restore |
| `GET /api/export`, `POST /api/import` | whole data as JSON / replace everything |
| `POST /api/import/entries` | `{format: "csv"\|"ics", name, data}` – add the entries of a CSV or calendar file below a new project |
| `GET /api/export.csv`, `GET /api/export.ics` | spreadsheet / calendar export (`closed=1` or `closed=0` to include or leave out finished entries; default: CSV with, calendar without) |
| `POST /api/reset` | replace everything with empty data (a backup is taken first) |
| `POST /mcp` | MCP (JSON-RPC 2.0), only when enabled in Settings, header token only |

Entry fields: `name`, `kind` (todo, idea, project, reminder), `status`, `color` (`#rrggbb`), `prio` (0–4),
`tags`, `people`, `location`, `deadline`, `reminder` and `end` (`YYYY-MM-DD` or `YYYY-MM-DDTHH:MM`; `end` is the
end of an event, not before its start), `deps`, `notes`,
`notesFormat` (`markdown` or `bbcode`),
`links` (`[{title, uri}]`), `url` (http/https), `webhook` (Discord, projects), `prog`
(`{mode: percent|units, done, total, unit}`), `recur` (`d`, `w`, `m`, `y`; every N: `2w`, `3d`, `6m`; monthly on weekdays: `m:1fr,3fr,-1fr` with `-1` = last), `est` (minutes), `lead` (minutes
of early warning for reminders), `discord` (true: also post a reminder to its project's channel).

## Licence

Tillerz Todo Mgr is under the [PolyForm Shield License 1.0.0](LICENSE). You may use it, change it and
share it, also at work, free of charge. You may not sell it or offer a product that competes with it.

## Third-party licences

The program uses these open-source parts. They are bundled into the program, and their licence texts
are in [app/web/licenses/](app/web/licenses/). The app shows the same list under Settings → About.

| Part | Used for | Licence |
|---|---|---|
| [Go standard library](https://go.dev/) | the program itself | BSD 3-Clause, [go.txt](app/web/licenses/go.txt) |
| [SortableJS](https://github.com/SortableJS/Sortable) 1.15.2 | drag and drop in the tree | MIT, [sortablejs.txt](app/web/licenses/sortablejs.txt) |
| [marked](https://marked.js.org/) 18.1.2 | Markdown in notes | MIT (and BSD 3-Clause for Markdown), [marked.txt](app/web/licenses/marked.txt) |
| [Lucide](https://lucide.dev/) 1.66.0 | tag icons | ISC, [lucide.txt](app/web/licenses/lucide.txt) |
| [Feather](https://feathericons.com/) | app icons | MIT, [feather.txt](app/web/licenses/feather.txt) |
