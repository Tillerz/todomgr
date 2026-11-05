package main

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"
)

// LAN access (Settings → LAN access, off by default). When on, the server listens on all
// interfaces. This computer still gets the token cookie with the page; other devices get a
// login page and type the token once. MCP stays on this computer only.

// fromLoopback: the request comes from this computer.
func fromLoopback(r *http.Request) bool {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	return err == nil && ap.Addr().Unmap().IsLoopback()
}

// lanHostOK: with LAN on, a request may also name the server by an IP address or by this
// computer's own name. Other domain names are refused, so a web page cannot reach the server
// through DNS rebinding (its own domain pointing at this computer).
func lanHostOK(host string) bool {
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	name, err := os.Hostname()
	if err != nil || name == "" {
		return false
	}
	host, name = strings.ToLower(strings.TrimSuffix(host, ".")), strings.ToLower(name)
	return host == name || host == name+".local"
}

// lanURLs lists the addresses other devices can use: the IPv4 addresses of this computer,
// without loopback and link-local ones.
func lanURLs(port int) []string {
	out := []string{}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(n.IP)
		if ip = ip.Unmap(); !ok || !ip.Is4() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, fmt.Sprintf("http://%s:%d/", ip, port))
	}
	return out
}

// loginLimit slows down guessing: after maxFails wrong tokens from one address, logins from
// it are refused for blockFor.
type loginLimit struct {
	mu sync.Mutex
	m  map[string]*loginTries
}

type loginTries struct {
	fails int
	until time.Time
}

const (
	maxFails = 5
	blockFor = time.Minute
)

func clientIP(r *http.Request) string {
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr().Unmap().String()
	}
	return r.RemoteAddr
}

// blocked returns how long the address must still wait.
func (l *loginLimit) blocked(ip string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t := l.m[ip]; t != nil && now.Before(t.until) {
		return t.until.Sub(now)
	}
	return 0
}

func (l *loginLimit) result(ip string, ok bool, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[string]*loginTries{}
	}
	if ok {
		delete(l.m, ip)
		return
	}
	if len(l.m) > 10000 { // many addresses: forget the ones that are not blocked
		for k, t := range l.m {
			if now.After(t.until) {
				delete(l.m, k)
			}
		}
	}
	t := l.m[ip]
	if t == nil || (!t.until.IsZero() && now.After(t.until)) {
		t = &loginTries{}
		l.m[ip] = t
	}
	if t.fails++; t.fails >= maxFails {
		t.fails, t.until = 0, now.Add(blockFor)
	}
}

func (s *Server) setTokenCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.cfg.Get().Token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: 10 * 365 * 24 * 3600})
}

// pairCodes are one-time login codes for the QR code in Settings: valid for pairFor, and only once.
// A photo of the QR code is useless afterwards, unlike the token itself.
type pairCodes struct {
	mu sync.Mutex
	m  map[string]time.Time // code → valid until
}

const pairFor = 5 * time.Minute

func (p *pairCodes) add(now time.Time) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[string]time.Time{}
	}
	for c, until := range p.m {
		if now.After(until) {
			delete(p.m, c)
		}
	}
	c := newToken()
	p.m[c] = now.Add(pairFor)
	return c
}

// use reports whether code is valid, and makes it invalid.
func (p *pairCodes) use(code string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	until, ok := p.m[code]
	delete(p.m, code)
	return ok && code != "" && !now.After(until)
}

// hPair: POST /api/lan/pair. A new one-time code, and per LAN address a link with it and its QR code.
func (s *Server) hPair(w http.ResponseWriter, r *http.Request) {
	if !s.lan {
		jerr(w, http.StatusBadRequest, errors.New("LAN access is off (or the program was not restarted after switching it on)"))
		return
	}
	code := s.pairs.add(time.Now())
	links := []map[string]any{}
	for _, u := range lanURLs(s.port) {
		link := u + "#pair=" + code // after the #: browsers never send it to the server, so it is in no log
		q, err := NewQR(link)
		if err != nil {
			jerr(w, 500, err)
			return
		}
		links = append(links, map[string]any{"url": u, "link": link, "svg": q.SVG()})
	}
	jout(w, 200, map[string]any{"links": links, "validSeconds": int(pairFor.Seconds())})
}

// hLogin: POST /api/login {token} or {pair}. Sets the cookie when the token or the one-time code is right.
func (s *Server) hLogin(w http.ResponseWriter, r *http.Request) {
	ip, now := clientIP(r), time.Now()
	if d := s.logins.blocked(ip, now); d > 0 {
		w.Header().Set("Retry-After", fmt.Sprint(int(d.Seconds())+1))
		jerr(w, http.StatusTooManyRequests, errors.New("too many wrong tokens: wait a minute and try again"))
		return
	}
	var req struct {
		Token string `json:"token"`
		Pair  string `json:"pair"`
	}
	if err := readBody(r, &req, 4096); err != nil {
		fail(w, err)
		return
	}
	ok := s.tokenEq(strings.TrimSpace(req.Token)) || req.Pair != "" && s.pairs.use(req.Pair, now)
	s.logins.result(ip, ok, now)
	if !ok {
		msg := "wrong token"
		if req.Pair != "" {
			msg = "this QR code has expired or was used already: show a new one in Settings, or type the token"
		}
		jerr(w, http.StatusUnauthorized, errors.New(msg))
		return
	}
	s.setTokenCookie(w)
	jout(w, 200, map[string]any{"ok": true})
}

// serveLogin answers the page address for a device that has not logged in yet.
func (s *Server) serveLogin(w http.ResponseWriter) {
	b, err := fs.ReadFile(s.ui, "login.html")
	if err != nil {
		http.Error(w, "login.html missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// ---------------------------------------------------------------- calendar subscription

// hCalFeed: GET /cal/<key>.ics, the open entries as a calendar that calendar apps subscribe to.
// Calendar apps cannot log in, so the secret key in the address is the access check; it only
// gives read access to the calendar, never to the app. A new key (Settings) ends old links.
func (s *Server) hCalFeed(w http.ResponseWriter, r *http.Request) {
	key, want := strings.TrimSuffix(r.PathValue("file"), ".ics"), s.cfg.Get().CalKey
	if want == "" || subtle.ConstantTimeCompare([]byte(key), []byte(want)) != 1 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(s.st.ExportICSFeed())
}

// calInfo: the subscription links for Settings, for this computer and (with LAN access) the network,
// each also as webcal:// (opens the calendar app) and, for the network ones, as a QR code.
func (s *Server) calInfo(key string) map[string]any {
	if key == "" {
		return map[string]any{"enabled": false}
	}
	path := "/cal/" + key + ".ics"
	links := []map[string]any{{"where": "this computer", "url": fmt.Sprintf("http://localhost:%d%s", s.port, path)}}
	if s.lan {
		for _, u := range lanURLs(s.port) {
			l := map[string]any{"where": "network", "url": strings.TrimSuffix(u, "/") + path}
			if q, err := NewQR("webcal" + strings.TrimPrefix(str(l["url"]), "http")); err == nil {
				l["svg"] = q.SVG()
			}
			links = append(links, l)
		}
	}
	for _, l := range links {
		l["webcal"] = "webcal" + strings.TrimPrefix(str(l["url"]), "http")
	}
	return map[string]any{"enabled": true, "links": links}
}
