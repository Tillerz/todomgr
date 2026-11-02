package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type cliArgs struct {
	pos  []string
	opt  map[string]string
	flag map[string]bool
}

// parseArgs: --key value | --key=value | --bool. Flags may appear anywhere.
func parseArgs(args []string, bools ...string) cliArgs {
	a := cliArgs{opt: map[string]string{}, flag: map[string]bool{}}
	isBool := map[string]bool{"json": true, "help": true, "yes": true}
	for _, b := range bools {
		isBool[b] = true
	}
	for i := 0; i < len(args); i++ {
		x := args[i]
		if !strings.HasPrefix(x, "--") || x == "--" {
			a.pos = append(a.pos, x)
			continue
		}
		k := strings.TrimPrefix(x, "--")
		if j := strings.Index(k, "="); j >= 0 {
			a.opt[k[:j]] = k[j+1:]
			continue
		}
		if isBool[k] {
			a.flag[k] = true
		} else if i+1 < len(args) {
			a.opt[k] = args[i+1]
			i++
		} else {
			a.flag[k] = true
		}
	}
	return a
}

type client struct {
	base, token string
	asJSON      bool
}

func newClient(args []string) (*client, error) {
	a := parseArgs(args)
	return clientFrom(a)
}

func clientFrom(a cliArgs) (*client, error) {
	dir := resolveDataDir(a.opt["data"])
	c := &client{asJSON: a.flag["json"]}
	cfg, cerr := LoadConfigReadOnly(dir)
	c.base = firstNonEmpty(a.opt["url"], os.Getenv("TODOMGR_URL"))
	if c.base == "" {
		port := 7437
		if b, err := os.ReadFile(filepath.Join(dir, "server.json")); err == nil {
			var sj struct{ Port int }
			if json.Unmarshal(b, &sj) == nil && sj.Port > 0 {
				port = sj.Port
			}
		} else if cerr == nil && cfg.Port > 0 {
			port = cfg.Port
		}
		c.base = fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	c.base = strings.TrimRight(c.base, "/")
	c.token = firstNonEmpty(a.opt["token"], os.Getenv("TODOMGR_TOKEN"))
	if c.token == "" && cerr == nil {
		c.token = cfg.Token
	}
	if c.token == "" {
		return nil, fmt.Errorf("no token found: run the app once (it creates %s) or pass --token", filepath.Join(dir, "config.json"))
	}
	return c, nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// LoadConfigReadOnly reads config.json without creating or changing anything.
func LoadConfigReadOnly(dir string) (ConfigData, error) {
	var d ConfigData
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return d, err
	}
	return d, json.Unmarshal(b, &d)
}

func (c *client) do(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Actor", "cli")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach the server at %s — start it first with:  todo serve", c.base)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		var e struct{ Error string }
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("server answered %d", resp.StatusCode)
	}
	if out != nil && len(bytes.TrimSpace(b)) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}
func (c *client) get(path string, out any) error { return c.do("GET", path, nil, out) }

// ---------------------------------------------------------------- output

var marks = map[string]string{"open": "[ ]", "progress": "[~]", "waiting": "[w]", "later": "[l]", "done": "[x]", "cancelled": "[-]",
	"raw": "(?)", "exploring": "(~)", "parked": "(p)", "rejected": "(-)", "planning": "<.>", "active": "<~>", "paused": "<w>", "archived": "<->", "set": "(!)"}

func meta(it map[string]any) string {
	var p []string
	if d := str(it["deadline"]); d != "" {
		p = append(p, "due "+d)
	}
	if d := str(it["reminder"]); d != "" {
		p = append(p, "remind "+d)
	}
	if n := num(it["prio"]); n > 0 {
		p = append(p, "!"+[]string{"", "low", "med", "high", "urgent"}[int(n)])
	}
	for _, t := range strs(it["tags"]) {
		p = append(p, "#"+t)
	}
	for _, t := range strs(it["people"]) {
		p = append(p, "@"+t)
	}
	if b, _ := it["blocked"].(bool); b {
		p = append(p, "blocked")
	}
	if pr, ok := it["progress"].(float64); ok && pr > 0 && pr < 1 {
		p = append(p, fmt.Sprintf("%d%%", int(pr*100+0.5)))
	}
	if len(p) == 0 {
		return ""
	}
	return "  " + strings.Join(p, " ")
}

func printItem(it map[string]any, indent string) {
	m := marks[str(it["status"])]
	if m == "" {
		m = "   "
	}
	fmt.Printf("%-6s %s %s%s%s\n", str(it["id"]), m, indent, str(it["name"]), meta(it))
}

func printTree(t map[string]any, indent string) {
	if str(t["id"]) != "root" {
		printItem(t, indent)
		indent += "  "
	}
	if kids, ok := t["items"].([]any); ok {
		for _, k := range kids {
			printTree(k.(map[string]any), indent)
		}
	} else if n := num(t["children"]); n > 0 {
		fmt.Printf("       %s… %d more (use --depth)\n", indent, int(n))
	}
}

func printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

var reDur = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*([hms]?)`)

// parseDuration: "45m", "1h30m", "90" (minutes), "30s" -> seconds.
func parseDuration(s string) (float64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || strings.Trim(reDur.ReplaceAllString(s, ""), " ") != "" {
		return 0, fmt.Errorf("cannot read duration %q (examples: 45m, 1h30m, 90)", s)
	}
	total := 0.0
	for _, m := range reDur.FindAllStringSubmatch(s, -1) {
		v, _ := strconv.ParseFloat(m[1], 64)
		switch m[2] {
		case "h":
			total += v * 3600
		case "s":
			total += v
		default:
			total += v * 60
		}
	}
	return total, nil
}

// "set" values: numbers and lists need real JSON types.
func parseSetValue(key, val string) (string, any, error) {
	switch key {
	case "prio", "est", "lead":
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			if key == "prio" {
				if p, ok := map[string]float64{"none": 0, "low": 1, "med": 2, "medium": 2, "high": 3, "urgent": 4}[strings.ToLower(val)]; ok {
					return key, p, nil
				}
			}
			return "", nil, fmt.Errorf("%s must be a number", key)
		}
		return key, f, nil
	case "tags", "people", "deps":
		return key, csv(val), nil
	case "collapsed", "discord":
		return key, val == "true" || val == "1", nil
	case "progress":
		// 60%  |  450/1200  |  450/1200:words
		if m := regexp.MustCompile(`^(\d+(?:\.\d+)?)%$`).FindStringSubmatch(val); m != nil {
			f, _ := strconv.ParseFloat(m[1], 64)
			return "prog", map[string]any{"mode": "percent", "done": f, "total": 100.0}, nil
		}
		if m := regexp.MustCompile(`^(\d+(?:\.\d+)?)/(\d+(?:\.\d+)?)(?::(.+))?$`).FindStringSubmatch(val); m != nil {
			d, _ := strconv.ParseFloat(m[1], 64)
			t, _ := strconv.ParseFloat(m[2], 64)
			return "prog", map[string]any{"mode": "units", "done": d, "total": t, "unit": m[3]}, nil
		}
		return "", nil, errors.New("progress must look like 60% or 450/1200 or 450/1200:words")
	}
	return key, val, nil
}

// ---------------------------------------------------------------- commands

func runCLI(cmd string, args []string) error {
	a := parseArgs(args, "all", "overdue", "open", "closed", "trashed", "yes")
	if a.flag["help"] {
		fmt.Print(usageText)
		return nil
	}
	c, err := clientFrom(a)
	if err != nil {
		return err
	}
	show := func(v any) {
		if c.asJSON {
			printJSON(v)
		}
	}
	need := func(n int, usage string) error {
		if len(a.pos) < n {
			return fmt.Errorf("usage: todo %s", usage)
		}
		return nil
	}
	switch cmd {
	case "add", "a":
		if err := need(1, "add \"Call Anna #work @anna !high due:fri\" [--parent ID]"); err != nil {
			return err
		}
		var res map[string]any
		if err := c.do("POST", "/api/quick", map[string]any{"text": strings.Join(a.pos, " "), "parent": a.opt["parent"]}, &res); err != nil {
			return err
		}
		if c.asJSON {
			show(res)
		} else {
			fmt.Printf("added %s  %s\n", res["id"], res["name"])
		}
	case "list", "ls":
		q := url.Values{}
		q.Set("q", strings.Join(a.pos, " "))
		for _, k := range []string{"status", "kind", "tag", "person", "project", "parent", "limit"} {
			if v := a.opt[k]; v != "" {
				q.Set(k, v)
			}
		}
		if !a.flag["all"] && a.opt["status"] == "" && !a.flag["trashed"] {
			q.Set("open", "1")
		}
		if a.flag["overdue"] {
			q.Set("overdue", "1")
		}
		if a.flag["trashed"] {
			q.Set("trashed", "1")
		}
		var res map[string]any
		if err := c.get("/api/nodes?"+q.Encode(), &res); err != nil {
			return err
		}
		if c.asJSON {
			show(res)
			break
		}
		items, _ := res["items"].([]any)
		for _, it := range items {
			printItem(it.(map[string]any), "")
		}
		cnt := len(items)
		noun := "entries"
		if cnt == 1 {
			noun = "entry"
		}
		hint := ""
		if !a.flag["all"] && a.opt["status"] == "" && !a.flag["trashed"] {
			hint = " (open only; --all also shows done, later and reminders)"
		}
		fmt.Printf("%d %s%s\n", cnt, noun, hint)
	case "tree":
		q := url.Values{}
		if len(a.pos) > 0 {
			q.Set("root", a.pos[0])
		}
		if d := a.opt["depth"]; d != "" {
			q.Set("depth", d)
		}
		var res map[string]any
		if err := c.get("/api/tree?"+q.Encode(), &res); err != nil {
			return err
		}
		if c.asJSON {
			show(res)
		} else {
			printTree(res, "")
		}
	case "show":
		if err := need(1, "show ID"); err != nil {
			return err
		}
		var n map[string]any
		if err := c.get("/api/nodes/"+url.PathEscape(a.pos[0]), &n); err != nil {
			return err
		}
		if c.asJSON {
			show(n)
			break
		}
		fmt.Printf("%s  %s %s  (%s, %s)\n", n["id"], marks[str(n["status"])], n["name"], n["kind"], n["status"])
		if p := str(n["path"]); p != "" {
			fmt.Println("  in:       ", p)
		}
		for _, k := range []string{"deadline", "reminder", "url", "location", "recur", "notes"} {
			if v := str(n[k]); v != "" {
				fmt.Printf("  %-9s %s\n", k+":", strings.ReplaceAll(v, "\n", "\n             "))
			}
		}
		if t := strs(n["tags"]); len(t) > 0 {
			fmt.Println("  tags:     ", "#"+strings.Join(t, " #"))
		}
		if t := strs(n["people"]); len(t) > 0 {
			fmt.Println("  people:   ", "@"+strings.Join(t, " @"))
		}
		fmt.Printf("  progress:  %d%%   children: %d\n", int(num(n["progress"])*100+0.5), int(num(n["children"])))
	case "done", "reopen", "status":
		minArgs := 1
		if cmd == "status" {
			minArgs = 2
		}
		if err := need(minArgs, map[string]string{"done": "done ID...", "reopen": "reopen ID...", "status": "status ID STATUS"}[cmd]); err != nil {
			return err
		}
		ids, st := a.pos, "done"
		if cmd == "status" {
			ids, st = a.pos[:1], a.pos[1]
		}
		for _, id := range ids {
			target := st
			if cmd == "reopen" {
				var n map[string]any
				if err := c.get("/api/nodes/"+url.PathEscape(id), &n); err != nil {
					return err
				}
				target = reopenStatus[str(n["kind"])]
			}
			var res map[string]any
			if err := c.do("POST", "/api/nodes/"+url.PathEscape(id)+"/status", map[string]any{"status": target}, &res); err != nil {
				return fmt.Errorf("%s: %w", id, err)
			}
			if c.asJSON {
				show(res)
			} else {
				fmt.Printf("%s  %s %s -> %s\n", id, marks[str(res["status"])], res["name"], res["status"])
			}
		}
	case "set":
		if err := need(2, "set ID key=value ...   (keys: name kind status prio tags people deadline reminder url location notes recur est lead deps progress)"); err != nil {
			return err
		}
		f := map[string]any{}
		for _, kv := range a.pos[1:] {
			i := strings.Index(kv, "=")
			if i < 1 {
				return fmt.Errorf("%q is not key=value", kv)
			}
			k, v, err := parseSetValue(kv[:i], kv[i+1:])
			if err != nil {
				return err
			}
			f[k] = v
		}
		var res map[string]any
		if err := c.do("PATCH", "/api/nodes/"+url.PathEscape(a.pos[0]), f, &res); err != nil {
			return err
		}
		if c.asJSON {
			show(res)
		} else {
			fmt.Printf("updated %s  %s\n", res["id"], res["name"])
		}
	case "mv", "move":
		if err := need(1, "mv ID --to PARENT_ID|root [--index N]"); err != nil {
			return err
		}
		body := map[string]any{"parent": firstNonEmpty(a.opt["to"], "root")}
		if i := a.opt["index"]; i != "" {
			n, _ := strconv.Atoi(i)
			body["index"] = n
		}
		var res map[string]any
		if err := c.do("POST", "/api/nodes/"+url.PathEscape(a.pos[0])+"/move", body, &res); err != nil {
			return err
		}
		fmt.Printf("moved %s  %s -> %s\n", res["id"], res["name"], firstNonEmpty(str(res["path"]), "top level"))
	case "rm", "trash":
		if err := need(1, "rm ID..."); err != nil {
			return err
		}
		for _, id := range a.pos {
			if err := c.do("DELETE", "/api/nodes/"+url.PathEscape(id), nil, nil); err != nil {
				return fmt.Errorf("%s: %w", id, err)
			}
			fmt.Printf("%s moved to trash (restore with: todo restore %s)\n", id, id)
		}
	case "restore":
		if err := need(1, "restore ID"); err != nil {
			return err
		}
		var res map[string]any
		if err := c.do("POST", "/api/nodes/"+url.PathEscape(a.pos[0])+"/restore", map[string]any{}, &res); err != nil {
			return err
		}
		fmt.Printf("restored %s  %s\n", res["id"], res["name"])
	case "log":
		if err := need(2, "log ID 45m   (also 1h30m, 90 = minutes)"); err != nil {
			return err
		}
		secs, err := parseDuration(a.pos[1])
		if err != nil {
			return err
		}
		var res map[string]any
		if err := c.do("POST", "/api/nodes/"+url.PathEscape(a.pos[0])+"/time", map[string]any{"seconds": secs}, &res); err != nil {
			return err
		}
		fmt.Printf("logged %s on %s  %s\n", fmtSecs(secs), res["id"], res["name"])
	case "today":
		var res map[string]any
		if err := c.get("/api/today", &res); err != nil {
			return err
		}
		if c.asJSON {
			show(res)
			break
		}
		fmt.Println("Today", res["date"])
		shown := false
		for _, sec := range [][2]string{{"overdue", "Overdue"}, {"dueToday", "Due today"}, {"inProgress", "In progress"}, {"upcomingReminders", "Upcoming reminders"}, {"nextUp", "Next up"}} {
			items, _ := res[sec[0]].([]any)
			if len(items) == 0 {
				continue
			}
			shown = true
			fmt.Printf("\n%s\n", sec[1])
			for _, it := range items {
				printItem(it.(map[string]any), "  ")
			}
		}
		if !shown {
			fmt.Println("\nNothing urgent.")
		}
	case "stats":
		var res map[string]any
		d := firstNonEmpty(a.opt["days"], "14")
		if err := c.get("/api/stats?days="+d, &res); err != nil {
			return err
		}
		if c.asJSON {
			show(res)
			break
		}
		fmt.Printf("open %v   later %v   overdue %v   closed this week %v   tracked this week %vm   streak %v days\n\n",
			res["open"], res["later"], res["overdue"], res["closedThisWeek"], res["trackedThisWeekMinutes"], res["closingStreakDays"])
		rows, _ := res["days"].([]any)
		for _, r := range rows {
			m := r.(map[string]any)
			fmt.Printf("%s  %-20s closed %v  created %v  %vm\n", m["date"], strings.Repeat("█", int(num(m["activity"]))), m["closed"], m["created"], m["timeMinutes"])
		}
	case "backup":
		var res map[string]any
		if err := c.do("POST", "/api/backups", map[string]any{}, &res); err != nil {
			return err
		}
		fmt.Println("backup created:", res["name"])
	case "backups":
		var res struct {
			Backups []BackupInfo `json:"backups"`
		}
		if err := c.get("/api/backups", &res); err != nil {
			return err
		}
		if c.asJSON {
			show(res)
			break
		}
		for _, b := range res.Backups {
			fmt.Printf("%-40s %-12s %s  %d KB\n", b.Name, b.Kind, b.Time.Format("2006-01-02 15:04"), b.Size/1024)
		}
	case "restore-backup":
		if err := need(1, "restore-backup NAME --yes"); err != nil {
			return err
		}
		if !a.flag["yes"] {
			return errors.New("this replaces ALL current data with the backup (a pre-restore copy is kept). Repeat with --yes to confirm")
		}
		if err := c.do("POST", "/api/backups/restore", map[string]any{"name": a.pos[0]}, nil); err != nil {
			return err
		}
		fmt.Println("restored", a.pos[0])
	case "export":
		path := "/api/export"
		if len(a.pos) > 0 {
			switch strings.ToLower(filepath.Ext(a.pos[0])) {
			case ".csv":
				path += ".csv"
			case ".ics":
				path += ".ics"
			}
		}
		if path != "/api/export" {
			if a.flag["closed"] {
				path += "?closed=1"
			} else if a.flag["open"] {
				path += "?closed=0"
			}
		}
		req, _ := http.NewRequest("GET", c.base+path, nil)
		req.Header.Set("Authorization", "Bearer "+c.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			return fmt.Errorf("export failed: server answered %d", resp.StatusCode)
		}
		if len(a.pos) > 0 {
			return os.WriteFile(a.pos[0], b, 0o600)
		}
		os.Stdout.Write(b)
	case "import":
		if err := need(1, "import FILE --yes"); err != nil {
			return err
		}
		if ext := strings.ToLower(filepath.Ext(a.pos[0])); ext == ".csv" || ext == ".ics" {
			b, err := os.ReadFile(a.pos[0])
			if err != nil {
				return err
			}
			var res map[string]any
			if err := c.do("POST", "/api/import/entries", map[string]any{"format": ext[1:], "name": filepath.Base(a.pos[0]), "data": string(b)}, &res); err != nil {
				return fmt.Errorf("import failed: %w", err)
			}
			fmt.Printf("added %v entries to %q (%v skipped)\n", res["added"], res["name"], res["skipped"])
			return nil
		}
		if !a.flag["yes"] {
			return errors.New("this replaces ALL current data (a pre-import backup is kept). Repeat with --yes to confirm")
		}
		b, err := os.ReadFile(a.pos[0])
		if err != nil {
			return err
		}
		req, _ := http.NewRequest("POST", c.base+"/api/import", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		rb, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			return fmt.Errorf("import failed: %s", strings.TrimSpace(string(rb)))
		}
		fmt.Println("imported")
	case "info":
		var res map[string]any
		if err := c.get("/api/info", &res); err != nil {
			return err
		}
		printJSON(res)
	case "token":
		fmt.Println(c.token)
	default:
		return fmt.Errorf("unknown command %q — run: todo help", cmd)
	}
	return nil
}

func fmtSecs(s float64) string {
	d := time.Duration(s) * time.Second
	if d >= time.Hour {
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()+0.5))
}

var _ = sort.Strings

const usageText = `Tillerz Todo Mgr ` + "v" + Version + `

  todo                        start the app (same as: todo serve --open)
  todo serve [--port N] [--data DIR] [--ui DIR] [--open]

Commands (talk to the running app):
  add "Call Anna #work @anna !high due:fri at:14:00 ~30m"   quick add into the Inbox
        "? idea text"  = idea,  "r: Stream at:19:00 due:thu repeat:weekly" = reminder
        --parent ID    add below an entry instead of the Inbox
  list [words] [--status s,s] [--kind k] [--tag t] [--person p] [--project name]
       [--parent ID] [--overdue] [--all] [--trashed] [--limit N]
  tree [ID] [--depth N]       outline        show ID      details
  done ID...   reopen ID...   status ID STATUS
  set ID key=value ...        keys: name kind status prio tags people deadline reminder
                              url location notes recur est lead deps progress (60% | 450/1200:words)
  mv ID --to PARENT|root [--index N]       rm ID...  (trash)     restore ID
  log ID 45m                  add tracked time (45m, 1h30m, 90)
  today                       what needs attention now          stats [--days N]
  backup | backups | restore-backup NAME --yes
  export [FILE[.json|.csv|.ics]] [--open|--closed]        info | token
  import FILE.json --yes      replace ALL data (a backup is kept)
  import FILE.csv|FILE.ics    add the entries below a new project "Import: FILE, date"
  mcp                         MCP stdio bridge (the MCP server must be switched on in Settings)

Options everywhere: --json (raw output)  --data DIR  --url URL  --token T
The app is found through DIR/server.json and DIR/config.json (default DIR: the "data" folder next to this program).
`
