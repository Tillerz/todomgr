package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func upload(a *apiT, name string, data []byte) string {
	a.t.Helper()
	m := a.call(200, "POST", "/api/files", map[string]any{"name": name, "data": base64.StdEncoding.EncodeToString(data)})
	key, _ := m["file"].(string)
	if !fileKeyRE.MatchString(key) {
		a.t.Fatalf("upload %s: key %q", name, key)
	}
	return key
}

func TestFileTextAndDownload(t *testing.T) {
	a := testServer(t)
	key := upload(a, "Notes.TXT", []byte("Hello\nWörld"))
	if !strings.HasSuffix(key, ".txt") {
		t.Errorf("key %q: want the extension .txt", key)
	}
	if _, err := os.Stat(filepath.Join(a.s.st.dir, "files", key)); err != nil {
		t.Errorf("file not in data/files: %v", err)
	}
	if v := a.call(200, "GET", "/api/files/"+key+"/view", nil); v["kind"] != "text" || v["text"] != "Hello\nWörld" {
		t.Errorf("view: %v", v)
	}
	w := a.do("GET", "/api/files/"+key+"?name=Notes.TXT", nil)
	if w.Code != 200 || w.Body.String() != "Hello\nWörld" {
		t.Fatalf("download: %d %q", w.Code, w.Body.String())
	}
	// always a download, never shown as a page of the app
	if ct, cd := w.Header().Get("Content-Type"), w.Header().Get("Content-Disposition"); ct != "application/octet-stream" || cd != `attachment; filename=Notes.TXT` {
		t.Errorf("download headers: %q, %q", ct, cd)
	}
	// Latin-1 text is shown too
	if v := a.call(200, "GET", "/api/files/"+upload(a, "a.txt", []byte("K\xe4se"))+"/view", nil); v["text"] != "Käse" {
		t.Errorf("latin-1 view: %v", v)
	}
	if v := a.call(200, "GET", "/api/files/"+upload(a, "x.bin", []byte("\x00\x01\x02\x03\x00\x00"))+"/view", nil); v["kind"] != "binary" {
		t.Errorf("binary view: %v", v)
	}
}

func TestFileMail(t *testing.T) {
	a := testServer(t)
	eml := "From: =?iso-8859-1?q?J=F6rg?= <j@example.org>\r\nTo: me@example.org\r\nSubject: =?utf-8?b?R3LDvMOfZQ==?=\r\n" +
		"Date: Thu, 5 Nov 2026 10:00:00 +0100\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"OUT\"\r\n\r\n" +
		"--OUT\r\nContent-Type: multipart/alternative; boundary=\"ALT\"\r\n\r\n" +
		"--ALT\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nGr=FC=DFe aus K=F6ln,=\r\n bis bald\r\n" +
		"--ALT\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>HTML version</p>\r\n--ALT--\r\n" +
		"--OUT\r\nContent-Type: application/pdf; name=\"plan.pdf\"\r\nContent-Disposition: attachment; filename=\"plan.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0x\r\n" +
		"--OUT--\r\n"
	v := a.call(200, "GET", "/api/files/"+upload(a, "A mail.eml", []byte(eml))+"/view", nil)
	h, _ := v["headers"].(map[string]any)
	if v["kind"] != "mail" || h["From"] != "Jörg <j@example.org>" || h["Subject"] != "Grüße" || h["To"] != "me@example.org" {
		t.Errorf("mail headers: %v", v)
	}
	if v["text"] != "Grüße aus Köln, bis bald" {
		t.Errorf("mail text: %q", v["text"])
	}
	if at, _ := v["attachments"].([]any); len(at) != 1 || at[0] != "plan.pdf" {
		t.Errorf("attachments: %v", v["attachments"])
	}
	// HTML only: the text without tags; a base64 body
	html := "<html><head><style>p{}</style></head><body><p>Hi &amp; bye</p><script>x()</script>Line 2</body></html>"
	eml2 := "From: a@b\r\nSubject: s\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		base64.StdEncoding.EncodeToString([]byte(html))[:40] + "\r\n" + base64.StdEncoding.EncodeToString([]byte(html))[40:] + "\r\n"
	if v := a.call(200, "GET", "/api/files/"+upload(a, "b.eml", []byte(eml2))+"/view", nil); v["text"] != "Hi & bye\nLine 2" {
		t.Errorf("html mail text: %q", v["text"])
	}
}

func TestFileLimits(t *testing.T) {
	a := testServer(t)
	big := base64.StdEncoding.EncodeToString(make([]byte, maxAttachment+1))
	a.call(413, "POST", "/api/files", map[string]any{"name": "big.bin", "data": big})
	a.call(400, "POST", "/api/files", map[string]any{"name": "x", "data": "not base64!"})
	a.call(400, "GET", "/api/files/config.json/view", nil)
	a.call(400, "GET", "/api/files/..%2Fconfig.json", nil)
	a.call(404, "GET", "/api/files/0123456789abcdef0123.txt", nil)
	// without the token: no upload, no download
	key := upload(a, "a.txt", []byte("secret"))
	if w := a.do("GET", "/api/files/"+key, nil, "Authorization", ""); w.Code != 401 {
		t.Errorf("download without token: %d", w.Code)
	}
	// the server only takes JSON posts, so a form on another web page cannot upload
	if w := a.do("POST", "/api/files", "data=x", "Content-Type", "application/x-www-form-urlencoded"); w.Code != 415 {
		t.Errorf("form upload: %d", w.Code)
	}
}

func TestFileLinkField(t *testing.T) {
	a := testServer(t)
	id := a.call(201, "POST", "/api/nodes", map[string]any{"name": "Mail todo"})["id"].(string)
	key := upload(a, "m.eml", []byte("From: a@b\r\n\r\nhi"))
	n := a.call(200, "PATCH", "/api/nodes/"+id, map[string]any{"links": []any{map[string]any{"title": "m.eml", "uri": "", "file": key}}})
	l, _ := n["links"].([]any)
	if len(l) != 1 || l[0].(map[string]any)["file"] != key {
		t.Errorf("links: %v", n["links"])
	}
	a.call(400, "PATCH", "/api/nodes/"+id, map[string]any{"links": []any{map[string]any{"title": "x", "file": "../config.json"}}})
	a.call(400, "PATCH", "/api/nodes/"+id, map[string]any{"links": []any{map[string]any{"title": "x"}}})
}
