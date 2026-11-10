package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Optional AI (Settings → AI, off by default): llama-server from llama.cpp and a model file (.gguf)
// in the folder ai/ next to the program, so the AI moves with the program folder. Todo Mgr starts
// the helper at the first AI request, on a free port of 127.0.0.1 with a random key, and stops it
// after aiIdle without use, on Unload, when AI is switched off and when the program stops.
// The helper forces every answer into a JSON schema, so the answers can always be read.

const (
	aiIdle     = 15 * time.Minute
	aiCtx      = "8192" // tokens the helper keeps in mind: the question plus the answer
	aiMaxNotes = 8000   // characters of notes sent to the AI
)

type aiHelper struct {
	dir   string
	start func(ctx context.Context) (base, key string, exited <-chan struct{}, stop func(), err error) // tests replace it

	starting sync.Mutex // one start at a time; mu stays free meanwhile, so the status can be read
	mu       sync.Mutex
	pick     string // the chosen model file (Settings → AI); "" or missing: the first one
	base     string // http://127.0.0.1:port of the running helper, or ""
	key      string
	exited   <-chan struct{}
	stop     func()
	idle     *time.Timer
}

func newAIHelper() *aiHelper {
	exe, _ := os.Executable()
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	h := &aiHelper{dir: filepath.Join(filepath.Dir(exe), "ai")}
	h.start = h.startProcess
	return h
}

// files finds the helper program and the model in the ai folder ("" when missing). The helper may
// sit in a sub-folder, as its download unpacks. A model is any .gguf file except the mmproj files,
// which are only for images.
func (h *aiHelper) files() (bin, model string) {
	name := "llama-server"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	for _, pat := range []string{name, filepath.Join("*", name), filepath.Join("*", "*", name)} {
		bs, _ := filepath.Glob(filepath.Join(h.dir, pat))
		if len(bs) > 0 {
			bin = bs[0]
			break
		}
	}
	ms := h.models()
	h.mu.Lock()
	pick := h.pick
	h.mu.Unlock()
	for _, m := range ms {
		if filepath.Base(m) == pick {
			return bin, m
		}
	}
	if len(ms) > 0 {
		return bin, ms[0]
	}
	return bin, ""
}

// models lists the model files in the ai folder, sorted by name.
func (h *aiHelper) models() []string {
	ms, _ := filepath.Glob(filepath.Join(h.dir, "*.gguf"))
	sort.Strings(ms)
	var out []string
	for _, m := range ms {
		if !strings.Contains(strings.ToLower(filepath.Base(m)), "mmproj") {
			out = append(out, m)
		}
	}
	return out
}

// choose sets the model to use; a running helper with another model is stopped, so the next
// question loads the new one.
func (h *aiHelper) choose(name string) {
	_, before := h.files()
	h.mu.Lock()
	h.pick = name
	h.mu.Unlock()
	if _, after := h.files(); after != before {
		h.unload()
	}
}

func (h *aiHelper) status(enabled bool) map[string]any {
	bin, model := h.files()
	h.mu.Lock()
	running := h.base != ""
	h.mu.Unlock()
	rel := func(p string) string {
		if p == "" {
			return ""
		}
		r, _ := filepath.Rel(h.dir, p)
		return filepath.ToSlash(r)
	}
	models := []string{}
	for _, m := range h.models() {
		models = append(models, filepath.Base(m))
	}
	return map[string]any{"enabled": enabled, "dir": h.dir, "helper": rel(bin), "model": rel(model), "models": models,
		"ready": bin != "" && model != "", "running": running}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// startProcess runs llama-server and waits until the model is loaded. Its output goes to
// ai/llama-server.log; the last line of it is shown when the start fails.
func (h *aiHelper) startProcess(ctx context.Context) (string, string, <-chan struct{}, func(), error) {
	bin, model := h.files()
	if bin == "" || model == "" {
		return "", "", nil, nil, fmt.Errorf("the AI files are missing in %s (see Settings → AI)", h.dir)
	}
	port, err := freePort()
	if err != nil {
		return "", "", nil, nil, err
	}
	key := newToken()
	logPath := filepath.Join(h.dir, "llama-server.log")
	logf, err := os.Create(logPath)
	if err != nil {
		return "", "", nil, nil, err
	}
	cmd := exec.Command(bin, "-m", model, "--host", "127.0.0.1", "--port", strconv.Itoa(port),
		"-c", aiCtx, "--reasoning", "off", "--no-webui")
	cmd.Dir = filepath.Dir(bin) // the helper's library files sit next to it
	cmd.Env = append(os.Environ(), "LLAMA_API_KEY="+key)
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		logf.Close()
		return "", "", nil, nil, fmt.Errorf("cannot start %s: %w", filepath.Base(bin), err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); logf.Close(); close(exited) }()
	stop := func() { _ = cmd.Process.Kill(); <-exited }
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(3 * time.Minute)
	for {
		if req, err := http.NewRequestWithContext(ctx, "GET", base+"/health", nil); err == nil {
			if r, err := http.DefaultClient.Do(req); err == nil {
				r.Body.Close()
				if r.StatusCode == 200 {
					return base, key, exited, stop, nil
				}
			}
		}
		select {
		case <-exited:
			return "", "", nil, nil, fmt.Errorf("the AI helper stopped while starting: %s", lastLine(logPath))
		case <-ctx.Done():
			stop()
			return "", "", nil, nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			stop()
			return "", "", nil, nil, errors.New("the AI helper did not get ready within 3 minutes")
		}
	}
}

func lastLine(path string) string {
	b, _ := os.ReadFile(path)
	var last string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if t := strings.TrimSpace(sc.Text()); t != "" {
			last = t
		}
	}
	if last == "" {
		return "no output (see " + path + ")"
	}
	return last
}

// ensure starts the helper if it is not running, and restarts the idle timer.
func (h *aiHelper) ensure(ctx context.Context) (string, string, error) {
	h.starting.Lock()
	defer h.starting.Unlock()
	h.mu.Lock()
	if h.base != "" {
		select {
		case <-h.exited: // it stopped by itself
			h.base, h.stop = "", nil
		default:
		}
	}
	running := h.base != ""
	h.mu.Unlock()
	if !running {
		base, key, exited, stop, err := h.start(ctx)
		if err != nil {
			return "", "", err
		}
		h.mu.Lock()
		h.base, h.key, h.exited, h.stop = base, key, exited, stop
		h.mu.Unlock()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.idle == nil {
		h.idle = time.AfterFunc(aiIdle, h.unload)
	} else {
		h.idle.Reset(aiIdle)
	}
	return h.base, h.key, nil
}

// unload stops the helper and frees its memory.
func (h *aiHelper) unload() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stop != nil {
		h.stop()
	}
	h.base, h.stop = "", nil
	if h.idle != nil {
		h.idle.Stop()
	}
}

// ask sends one question and reads the answer, which the schema forces into JSON, into out.
// temp: 0 gives the most likely answer, higher values more variety.
func (h *aiHelper) ask(ctx context.Context, temp float64, system, user string, schema map[string]any, out any) error {
	base, key, err := h.ensure(ctx)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{
		"messages":             []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}},
		"response_format":      map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "answer", "schema": schema}},
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
		"temperature":          temp,
		"max_tokens":           4000,
	})
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if r.StatusCode != 200 {
		return fmt.Errorf("the AI helper answered %d: %s", r.StatusCode, strings.TrimSpace(string(b)))
	}
	var res struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(b, &res); err != nil || len(res.Choices) == 0 {
		return errors.New("the AI helper sent no answer")
	}
	if err := json.Unmarshal([]byte(res.Choices[0].Message.Content), out); err != nil {
		return errors.New("the AI answer could not be read")
	}
	return nil
}

// ---------------------------------------------------------------- questions

// aiSubtasks suggests sub-tasks for an entry: a split when it has none, the missing ones when it has some.
func (s *Server) aiSubtasks(ctx context.Context, id string) ([]string, error) {
	n, err := s.st.Get(id)
	if err != nil {
		return nil, err
	}
	var have []string
	for _, c := range anyList(n["childIds"]) {
		if k, err := s.st.Get(str(c)); err == nil {
			have = append(have, str(k["name"]))
		}
	}
	var q strings.Builder
	fmt.Fprintf(&q, "Task: %s\nType: %s\n", str(n["name"]), str(n["kind"]))
	if p := str(n["path"]); p != "" {
		fmt.Fprintf(&q, "Part of: %s\n", p)
	}
	if notes := clip(strings.TrimSpace(str(n["notes"])), aiMaxNotes); notes != "" {
		fmt.Fprintf(&q, "Notes:\n%s\n", notes)
	}
	if len(have) > 0 {
		fmt.Fprintf(&q, "\nIt already has these sub-tasks:\n- %s\nSuggest only sub-tasks that are still missing, not these again.\n", strings.Join(have, "\n- "))
	} else {
		q.WriteString("\nSplit it into 3 to 8 sub-tasks.\n")
	}
	var out struct {
		Subtasks []string `json:"subtasks"`
	}
	err = s.ai.ask(ctx, 0.5, "You help to plan tasks in a todo app. Split the given task into small, concrete sub-tasks that a person can "+
		"finish one at a time, in a sensible order. Each sub-task name is short (at most about 8 words) and starts with a verb. "+
		"Write in the same language as the task. List only the sub-tasks, without numbers or explanations.", q.String(),
		map[string]any{"type": "object", "required": []string{"subtasks"}, "properties": map[string]any{
			"subtasks": map[string]any{"type": "array", "minItems": 1, "maxItems": 10,
				"items": map[string]any{"type": "string", "minLength": 2, "maxLength": 120}}}}, &out)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, h := range have {
		seen[strings.ToLower(strings.TrimSpace(h))] = true
	}
	items := []string{}
	for _, t := range out.Subtasks {
		t = strings.TrimSpace(t)
		if k := strings.ToLower(t); t != "" && !seen[k] {
			seen[k] = true
			items = append(items, t)
		}
	}
	return items, nil
}

// aiRewrite rewrites the notes of an entry. mode "tidy": spelling, lists, tick boxes and groups, every
// fact kept and nothing added. mode "expand": also adds next steps, things to prepare and open questions,
// from the entry's name and notes; it works with empty notes too.
func (s *Server) aiRewrite(ctx context.Context, id, mode string) (string, error) {
	if mode != "tidy" && mode != "expand" {
		return "", fmt.Errorf("unknown rewrite mode %q (tidy or expand)", mode)
	}
	n, err := s.st.Get(id)
	if err != nil {
		return "", err
	}
	notes := strings.TrimSpace(str(n["notes"]))
	if notes == "" && mode == "tidy" {
		return "", errors.New("the notes are empty")
	}
	if len([]rune(notes)) > aiMaxNotes {
		return "", fmt.Errorf("the notes are too long for the AI (more than %d characters)", aiMaxNotes)
	}
	md := str(n["notesFormat"]) != "bbcode"
	format, tasks := "Markdown", "task list items ('- [ ] task'; keep '- [x] task' for done ones)"
	if !md {
		format, tasks = "BBCode", "a list ([ul] - task [/ul])"
	}
	keep := "Keep every fact, date, name, number and link exactly as it is, including links such as [[abc123]] and [[abc123|text]]. "
	// The list rules for both modes, and an example: small models follow an example better than rules.
	// The notes go in as the whole question, without a frame that the model could copy into its answer.
	rules := "1. Fix spelling and grammar.\n" +
		"2. When a sentence names several items, make them a list with one item per line.\n" +
		"3. Write things that still have to be done as " + tasks + ".\n" +
		"4. When the notes have more than one topic, put the related parts under short headings.\n" +
		"5. Sort the items of a list alphabetically only when their order does not matter. Keep the order of steps, sequences and timelines.\n" +
		"6. Write all dates and times in one style.\n" +
		"7. Use plain lists: never put a second list marker in front of a task, and do not nest lists.\n" +
		"8. Use [[...]] only for links that are already in the notes, and keep them exactly.\n" +
		"9. Do not repeat anything.\n"
	example := ""
	if md {
		example = "\nExample. Notes: \"buy milk, eggs and bread. call Tom friday 3pm about the trip\"\nAnswer:\n" +
			"## Shopping\n- [ ] Bread\n- [ ] Eggs\n- [ ] Milk\n\n## Calls\n- [ ] Call Tom on Friday at 3 pm about the trip\n"
	}
	about := "The notes belong to the entry \"" + str(n["name"]) + "\" (" + str(n["kind"])
	if p := str(n["path"]); p != "" {
		about += ", part of " + p
	}
	about += ").\n"
	size := len([]rune(notes))
	minLen, temp := max(1, size/2), 0.2 // at least half as long: a small model may otherwise answer with a title only
	if mode == "expand" {
		return s.aiExpand(ctx, notes, about, md)
	}
	var out struct {
		Text string `json:"rewritten_notes"`
	}
	err = s.ai.ask(ctx, temp, "You tidy up notes in a todo app. Rewrite the whole notes the user sends, in "+format+", with these rules:\n"+rules+
		keep+"Do not add information and do not leave anything out. Answer with the tidied notes only, in the same language as the notes.\n"+
		about+example, notes, map[string]any{"type": "object", "required": []string{"rewritten_notes"}, "properties": map[string]any{
		"rewritten_notes": map[string]any{"type": "string", "minLength": minLen}}}, &out)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(out.Text)
	if text == "" {
		return "", errors.New("the AI sent an empty text")
	}
	if strings.Contains(text, "<<<") || strings.Contains(text, ">>>") || strings.Contains(text, "The notes belong to the entry") {
		return "", errors.New("the AI answer looks broken (it repeats the question). Please try again")
	}
	return text, nil
}

// aiExpand: the AI fills a form (four short lists, each with a heading in the entry's language) and the
// program writes it as Markdown or BBCode below the unchanged notes. A form cannot copy the notes or
// break the format, as free text from a small model can.
func (s *Server) aiExpand(ctx context.Context, notes, about string, md bool) (string, error) {
	keys := []string{"next_steps", "to_prepare", "open_questions", "keep_in_mind"}
	props, required := map[string]any{}, []string{}
	for i, k := range keys {
		props[k+"_heading"] = map[string]any{"type": "string", "minLength": 2, "maxLength": 40}
		least := 0
		if i == 0 {
			least = 1 // at least one next step
		}
		props[k] = map[string]any{"type": "array", "minItems": least, "maxItems": 6,
			"items": map[string]any{"type": "string", "minLength": 3, "maxLength": 160}}
		required = append(required, k+"_heading", k)
	}
	user := notes
	if user == "" {
		user = "(none)"
	}
	var out map[string]any
	err := s.ai.ask(ctx, 0.5, "You help to plan in a todo app. Read the notes of an entry and list only what is missing to get it done well:\n"+
		"- next_steps: the concrete actions, in a sensible order.\n"+
		"- to_prepare: things to get, bring or arrange.\n"+
		"- open_questions: what still has to be decided or found out.\n"+
		"- keep_in_mind: risks and tips.\n"+
		"Each item is one short line. A list may stay empty when there is nothing useful for it. Do not repeat what the notes already say, "+
		"and do not put an item into two lists. Do not invent facts such as dates, prices, names or places; ask for them as open "+
		"questions instead. The *_heading fields are the headings for the lists, for example \"Next steps\", \"To prepare\", \"Open "+
		"questions\" and \"Keep in mind\". Write the headings and the items in the language of the entry's name and notes.\n"+about+
		"The user sends the notes (or \"(none)\").", user, map[string]any{"type": "object", "required": required, "properties": props}, &out)
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	for _, l := range strings.Split(notes, "\n") { // what the notes already say
		seen[strings.ToLower(strings.Trim(l, " -*[]x\t"))] = true
	}
	var b strings.Builder
	for i, k := range keys {
		var items []string
		for _, x := range anyList(out[k]) {
			t := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(str(x)), "-*• "))
			if key := strings.ToLower(t); t != "" && !seen[key] {
				seen[key] = true
				items = append(items, t)
			}
		}
		if len(items) == 0 {
			continue
		}
		head := strings.TrimSpace(str(out[k+"_heading"]))
		mark := "- "
		if i == 0 && md {
			mark = "- [ ] " // next steps as tick boxes
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		if md {
			b.WriteString("## " + head + "\n")
		} else {
			b.WriteString("[h2]" + head + "[/h2]\n[ul]\n")
		}
		for _, t := range items {
			b.WriteString(mark + t + "\n")
		}
		if !md {
			b.WriteString("[/ul]\n")
		}
	}
	if b.Len() == 0 {
		return "", errors.New("the AI found nothing to add")
	}
	text := strings.TrimSpace(b.String())
	if notes != "" {
		text = notes + "\n\n" + text
	}
	return text, nil
}

func anyList(v any) []any {
	switch l := v.(type) {
	case []any:
		return l
	case []string:
		out := make([]any, len(l))
		for i, x := range l {
			out[i] = x
		}
		return out
	}
	return nil
}

// ---------------------------------------------------------------- HTTP

func (s *Server) aiOn(w http.ResponseWriter) bool {
	if !s.cfg.Get().AIEnabled {
		jerr(w, 400, errors.New("AI is off (Settings → AI)"))
		return false
	}
	return true
}

func (s *Server) hAIStatus(w http.ResponseWriter, r *http.Request) {
	jout(w, 200, s.ai.status(s.cfg.Get().AIEnabled))
}

func (s *Server) hAIUnload(w http.ResponseWriter, r *http.Request) {
	s.ai.unload()
	jout(w, 200, s.ai.status(s.cfg.Get().AIEnabled))
}

func (s *Server) hAITest(w http.ResponseWriter, r *http.Request) {
	if !s.aiOn(w) {
		return
	}
	t := time.Now()
	var out struct {
		Text string `json:"text"`
	}
	if err := s.ai.ask(r.Context(), 0.5, "You are a friendly assistant in a todo app.", "Say hello in at most 8 words.",
		map[string]any{"type": "object", "required": []string{"text"}, "properties": map[string]any{"text": map[string]any{"type": "string"}}}, &out); err != nil {
		fail(w, err)
		return
	}
	jout(w, 200, map[string]any{"text": out.Text, "secs": time.Since(t).Seconds()})
}

func (s *Server) hAISubtasks(w http.ResponseWriter, r *http.Request) {
	var req struct{ ID string }
	if err := readBody(r, &req, 1<<12); err != nil {
		fail(w, err)
		return
	}
	if !s.aiOn(w) {
		return
	}
	items, err := s.aiSubtasks(r.Context(), req.ID)
	if err != nil {
		fail(w, err)
		return
	}
	jout(w, 200, map[string]any{"items": items})
}

func (s *Server) hAIRewrite(w http.ResponseWriter, r *http.Request) {
	var req struct{ ID, Mode string }
	if err := readBody(r, &req, 1<<12); err != nil {
		fail(w, err)
		return
	}
	if !s.aiOn(w) {
		return
	}
	if req.Mode == "" {
		req.Mode = "tidy"
	}
	text, err := s.aiRewrite(r.Context(), req.ID, req.Mode)
	if err != nil {
		fail(w, err)
		return
	}
	jout(w, 200, map[string]any{"text": text})
}
