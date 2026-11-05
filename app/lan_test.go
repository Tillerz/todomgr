package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// LAN access: this computer gets the cookie with the page, other devices log in with the token
func TestLANAccess(t *testing.T) {
	a := testServer(t)
	dev := func(ip string, hdr ...string) []string { // a request from another device in the network
		return append([]string{"RemoteAddr", ip + ":40000", "Host", "192.168.1.20:7437", "Authorization", ""}, hdr...)
	}

	// LAN off: an IP address as host is refused, as before
	if w := a.do("GET", "/", nil, dev("192.168.1.50")...); w.Code != 403 {
		t.Errorf("LAN off: status %d, want 403", w.Code)
	}
	a.s.lan = true

	w := a.do("GET", "/", nil, dev("192.168.1.50")...)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "not logged in") || w.Header().Get("Set-Cookie") != "" {
		t.Errorf("another device must get the login page and no cookie: status %d, cookie %q", w.Code, w.Header().Get("Set-Cookie"))
	}
	if w := a.do("GET", "/", nil, "Authorization", ""); !strings.Contains(w.Header().Get("Set-Cookie"), cookieName+"="+a.token) {
		t.Error("this computer must still get the cookie with the page")
	}
	if w := a.do("GET", "/api/info", nil, dev("192.168.1.50")...); w.Code != 401 {
		t.Errorf("API without token from another device: status %d, want 401", w.Code)
	}

	// log in: wrong token, then the right one
	if w := a.do("POST", "/api/login", map[string]any{"token": "nope"}, dev("192.168.1.50", "Origin", "http://192.168.1.20:7437")...); w.Code != 401 {
		t.Errorf("wrong token: status %d, want 401", w.Code)
	}
	w = a.do("POST", "/api/login", map[string]any{"token": a.token}, dev("192.168.1.50", "Origin", "http://192.168.1.20:7437")...)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Set-Cookie"), cookieName+"="+a.token) {
		t.Fatalf("right token: status %d, cookie %q", w.Code, w.Header().Get("Set-Cookie"))
	}
	cookie := []string{"Cookie", cookieName + "=" + a.token}
	if w := a.do("GET", "/", nil, dev("192.168.1.50", cookie...)...); w.Code != 200 || !strings.Contains(w.Body.String(), `id="quick"`) {
		t.Errorf("logged in: status %d, want the app page", w.Code)
	}
	if w := a.do("GET", "/api/info", nil, dev("192.168.1.50", cookie...)...); w.Code != 200 {
		t.Errorf("API with the cookie: status %d", w.Code)
	}

	// guessing: after 5 wrong tokens even the right one is refused for a while
	for i := 0; i < maxFails; i++ {
		a.do("POST", "/api/login", map[string]any{"token": "guess"}, dev("192.168.1.66")...)
	}
	if w := a.do("POST", "/api/login", map[string]any{"token": a.token}, dev("192.168.1.66")...); w.Code != 429 {
		t.Errorf("after %d wrong tokens: status %d, want 429", maxFails, w.Code)
	}
	if w := a.do("POST", "/api/login", map[string]any{"token": a.token}, dev("192.168.1.67")...); w.Code != 200 {
		t.Errorf("another device is not blocked: status %d", w.Code)
	}

	// host names: IP addresses and this computer's name, never other domains (DNS rebinding)
	name, _ := os.Hostname()
	for host, want := range map[string]int{"evil.example:7437": 403, "10.0.0.5:7437": 401, strings.ToLower(name) + ":7437": 401, name + ".local:7437": 401, "192.168.1.20:9999": 403} {
		if w := a.do("GET", "/api/info", nil, "RemoteAddr", "192.168.1.50:40000", "Authorization", "", "Host", host); w.Code != want {
			t.Errorf("host %s: status %d, want %d", host, w.Code, want)
		}
	}

	// MCP stays on this computer
	a.call(200, "PUT", "/api/settings", map[string]any{"mcp": map[string]any{"enabled": true}})
	if w := a.do("POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}, "RemoteAddr", "192.168.1.50:40000", "Host", "192.168.1.20:7437"); w.Code != 403 {
		t.Errorf("MCP from another device: status %d, want 403", w.Code)
	}

	// the setting: saved, and shown as "needs a restart" until the program runs with it
	a.s.lan = false
	m := a.call(200, "PUT", "/api/settings", map[string]any{"lan": map[string]any{"enabled": true}})
	if l := m["lan"].(map[string]any); l["enabled"] != true || l["active"] != false {
		t.Errorf("settings lan: %v", l)
	}
}

// QR login: one-time codes that expire
func TestLANPairCodes(t *testing.T) {
	a := testServer(t)
	a.call(400, "POST", "/api/lan/pair", map[string]any{}) // LAN off
	a.s.lan = true
	m := a.call(200, "POST", "/api/lan/pair", map[string]any{})
	if m["validSeconds"] != float64(300) {
		t.Errorf("validSeconds %v", m["validSeconds"])
	}
	for _, l := range m["links"].([]any) { // the test machine may have no network address
		l := l.(map[string]any)
		if !strings.Contains(str(l["link"]), "/#pair=") || !strings.HasPrefix(str(l["svg"]), "<svg") {
			t.Errorf("link %v", l)
		}
	}
	dev := []string{"RemoteAddr", "192.168.1.50:40000", "Host", "192.168.1.20:7437", "Authorization", ""}
	code := a.s.pairs.add(time.Now())
	if w := a.do("POST", "/api/login", map[string]any{"pair": code}, dev...); w.Code != 200 || !strings.Contains(w.Header().Get("Set-Cookie"), cookieName+"="+a.token) {
		t.Fatalf("pair code: status %d", w.Code)
	}
	if w := a.do("POST", "/api/login", map[string]any{"pair": code}, dev...); w.Code != 401 {
		t.Errorf("a pair code must work only once: status %d", w.Code)
	}
	old := a.s.pairs.add(time.Now().Add(-pairFor - time.Second))
	if w := a.do("POST", "/api/login", map[string]any{"pair": old}, dev...); w.Code != 401 {
		t.Errorf("an expired pair code must fail: status %d", w.Code)
	}
}
