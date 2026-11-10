package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"unicode"
)

// Time estimates from your own history, without AI. A repeating todo uses its own past (tracked
// time per time it was done); other todos use finished todos with tracked time that share words in
// the name, tags or project: the middle value (median) of the best five. Only when there is no such
// history, and AI is on, the AI makes a guess.

// estStop: short common words that say nothing about the work (English and German).
var estStop = map[string]bool{}

func init() {
	for _, w := range strings.Fields("the and for with from into about this that then than have make some also " +
		"und der die das den dem des mit für ein eine einen einem zum zur auf aus bei nach von vor über noch") {
		estStop[w] = true
	}
}

func estWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(w)) >= 3 && !estStop[w] {
			out[w] = true
		}
	}
	return out
}

func trackedSecs(n Node) float64 {
	var s float64
	for _, t := range anyList(n["time"]) {
		if m, ok := t.(map[string]any); ok {
			s += num(m["secs"])
		}
	}
	return s
}

func doneCount(n Node) int {
	c := 0
	for _, h := range anyList(n["hist"]) {
		if m, ok := h.(map[string]any); ok && str(m["s"]) == "done" {
			c++
		}
	}
	return c
}

// estRound rounds to steps that fit the size: 5 min below an hour, 15 min below 4 hours, else 30 min.
func estRound(min float64) int {
	step := 5.0
	switch {
	case min >= 240:
		step = 30
	case min >= 60:
		step = 15
	}
	return int(math.Max(5, math.Round(min/step)*step))
}

func fmtMinutes(min float64) string {
	m := int(math.Round(min))
	if m < 60 {
		return fmt.Sprintf("%d min", m)
	}
	if m%60 == 0 {
		return fmt.Sprintf("%d h", m/60)
	}
	return fmt.Sprintf("%d h %d min", m/60, m%60)
}

func (db *DB) topOf(n Node) string {
	for n != nil && n.S("parent") != "root" && n.S("parent") != "" {
		n = db.Nodes[n.S("parent")]
	}
	if n == nil {
		return ""
	}
	return n.S("id")
}

// EstimateFromHistory suggests minutes for a todo, with the reason and the finished todos it used.
// minutes is 0 when there is no history to go by.
func (s *Store) EstimateFromHistory(id string) (minutes int, reason string, used []map[string]any, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.db.Nodes[id]
	if !ok || id == "root" {
		return 0, "", nil, fmt.Errorf("entry %q not found", id)
	}
	if dc, t := doneCount(n), trackedSecs(n); dc > 0 && t > 0 { // a repeating todo: its own past
		m := t / 60 / float64(dc)
		times := "time"
		if dc > 1 {
			times = fmt.Sprintf("%d times", dc)
		}
		return estRound(m), fmt.Sprintf("It took %s on average the last %s.", fmtMinutes(m), times), nil, nil
	}
	words, tags, top := estWords(n.S("name")), map[string]bool{}, s.db.topOf(n)
	for _, t := range strs(n["tags"]) {
		tags[strings.ToLower(t)] = true
	}
	type match struct {
		id, name   string
		min, score float64
		updated    float64
	}
	var ms []match
	for cid, c := range s.db.Nodes {
		if cid == id || cid == "root" || c.S("kind") != "todo" || c.S("status") != "done" || s.db.inTrash(c) {
			continue
		}
		t := trackedSecs(c)
		if t <= 0 {
			continue
		}
		shared, st := 0, 0
		for w := range estWords(c.S("name")) {
			if words[w] {
				shared++
			}
		}
		for _, tg := range strs(c["tags"]) {
			if tags[strings.ToLower(tg)] {
				st++
			}
		}
		if shared == 0 && st == 0 {
			continue
		}
		score := 2*float64(shared) + float64(st)
		if c.S("parent") == n.S("parent") {
			score++
		} else if top != "" && s.db.topOf(c) == top {
			score += 0.5
		}
		ms = append(ms, match{cid, c.S("name"), t / 60 / float64(max(1, doneCount(c))), score, c.N("updated")})
	}
	if len(ms) == 0 {
		return 0, "", nil, nil
	}
	sort.Slice(ms, func(i, j int) bool {
		if ms[i].score != ms[j].score {
			return ms[i].score > ms[j].score
		}
		return ms[i].updated > ms[j].updated // the more recent one
	})
	ms = ms[:min(5, len(ms))]
	vals := make([]float64, len(ms))
	var names []string
	for i, m := range ms {
		vals[i] = m.min
		used = append(used, map[string]any{"id": m.id, "name": m.name, "minutes": math.Round(m.min)})
		names = append(names, fmt.Sprintf("“%s” (%s)", m.name, fmtMinutes(m.min)))
	}
	sort.Float64s(vals)
	med := vals[len(vals)/2]
	if len(vals)%2 == 0 {
		med = (vals[len(vals)/2-1] + vals[len(vals)/2]) / 2
	}
	return estRound(med), "Similar finished todos: " + strings.Join(names, ", ") + ".", used, nil
}

// aiEstimate is the guess when there is no history: minutes and a short reason.
func (s *Server) aiEstimate(ctx context.Context, id string) (int, string, error) {
	n, err := s.st.Get(id)
	if err != nil {
		return 0, "", err
	}
	q := "Todo: " + str(n["name"]) + "\n"
	if p := str(n["path"]); p != "" {
		q += "Part of: " + p + "\n"
	}
	if notes := clip(strings.TrimSpace(str(n["notes"])), 2000); notes != "" {
		q += "Notes:\n" + notes + "\n"
	}
	var out struct {
		Minutes float64 `json:"minutes"`
		Reason  string  `json:"reason"`
	}
	err = s.ai.ask(ctx, 0.3, "You estimate how long one person needs for a todo, in minutes of real work time. Be realistic, "+
		"not optimistic. Give the reason in one short sentence, in the language of the todo.", q,
		map[string]any{"type": "object", "required": []string{"minutes", "reason"}, "properties": map[string]any{
			"minutes": map[string]any{"type": "integer", "minimum": 5, "maximum": 1440},
			"reason":  map[string]any{"type": "string", "minLength": 3, "maxLength": 160}}}, &out)
	if err != nil {
		return 0, "", err
	}
	if out.Minutes <= 0 {
		return 0, "", errors.New("the AI sent no estimate")
	}
	return estRound(out.Minutes), strings.TrimSpace(out.Reason), nil
}

// hEstimate: {id, ai} → {minutes, reason, used, source: "history" | "ai"}; minutes 0: no history
// (and ai not asked or AI off). The page asks without ai first, so history answers at once.
func (s *Server) hEstimate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string
		AI bool
	}
	if err := readBody(r, &req, 1<<12); err != nil {
		fail(w, err)
		return
	}
	m, reason, used, err := s.st.EstimateFromHistory(req.ID)
	if err != nil {
		fail(w, err)
		return
	}
	if m > 0 {
		jout(w, 200, map[string]any{"minutes": m, "reason": reason, "used": used, "source": "history"})
		return
	}
	if req.AI && s.cfg.Get().AIEnabled {
		m, reason, err := s.aiEstimate(r.Context(), req.ID)
		if err != nil {
			fail(w, err)
			return
		}
		jout(w, 200, map[string]any{"minutes": m, "reason": reason, "used": []any{}, "source": "ai"})
		return
	}
	jout(w, 200, map[string]any{"minutes": 0, "reason": "", "used": []any{}, "source": ""})
}
