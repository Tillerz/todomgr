package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Discord posting happens in the daemon: the webhook URL is a secret and never
// has to leave it (and there are no browser cross-origin problems). Only URLs
// that match reHook (discord.com webhooks over https) are ever contacted, and
// redirects are not followed.

// discordSend is a variable so tests can replace it.
var discordSend = func(hook, content string) error {
	body, _ := json.Marshal(map[string]any{
		"content": content, "username": AppName,
		"allowed_mentions": map[string]any{"parse": []string{}}, // never ping @everyone
	})
	c := &http.Client{Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Post(hook, "application/json", bytes.NewReader(body))
	if err != nil {
		return errors.New("could not reach Discord (no internet connection?)")
	}
	defer resp.Body.Close()
	switch code := resp.StatusCode; {
	case code/100 == 2:
		return nil
	case code == http.StatusTooManyRequests:
		return fmt.Errorf("Discord is rate limiting this webhook, try again in %s s", orDefault(resp.Header.Get("Retry-After"), "a few"))
	case code == http.StatusUnauthorized || code == http.StatusNotFound:
		return errors.New("Discord rejected the webhook — was it deleted? Check the URL in the project")
	default:
		return fmt.Errorf("Discord answered %d", code)
	}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// hookFor finds the Discord webhook that applies to an entry: its own if it is a
// project with one, otherwise the nearest project above it.
func (db *DB) hookFor(id string) (project, hook string) {
	for i := 0; i < 1000 && id != "" && id != "root"; i++ {
		n, ok := db.Nodes[id]
		if !ok {
			break
		}
		if n.S("kind") == "project" && n.S("webhook") != "" {
			return n.S("name"), n.S("webhook")
		}
		id = n.S("parent")
	}
	return "", ""
}

func clip(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return string(r[:max])
	}
	return s
}

// Announce posts "title\nurl" to the Discord channel of the entry's project.
func (s *Store) Announce(id, title, url string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("a title is required")
	}
	u := normURL(url)
	if strings.TrimSpace(url) != "" && u == "" {
		return errors.New("the URL must start with http:// or https://")
	}
	s.mu.RLock()
	n, ok := s.db.Nodes[id]
	var hook string
	if ok && id != "root" && !s.db.inTrash(n) {
		_, hook = s.db.hookFor(id)
	}
	s.mu.RUnlock()
	if !ok || id == "root" {
		return fmt.Errorf("entry %q not found", id)
	}
	if hook == "" {
		return errors.New("no Discord webhook is set for the project of this entry")
	}
	if !reHook.MatchString(hook) { // the page's sync and the JSON import save entries as they are
		return errors.New("the webhook of the project is not a Discord webhook URL")
	}
	content := title
	if u != "" {
		content += "\n" + u
	}
	return discordSend(hook, clip(content, 2000))
}

// announceReminder posts a reminder that has just reached its time.
func (s *Store) announceReminder(id string) error {
	s.mu.RLock()
	n, ok := s.db.Nodes[id]
	var name, url string
	if ok {
		name, url = n.S("name"), n.S("url")
	}
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	return s.Announce(id, "⏰ "+name, url)
}
