package main

// Attachments: copies of files dropped onto an entry, kept in data/files/. A link points to one with
// {"file": key}. The key is random, so it never comes from a file name or a path. Backups and the JSON
// export hold only database.json, not these files.

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

const maxAttachment = 10 << 20 // 10 MB per file

// fileKeyRE: 20 random hex characters, then the file's extension (kept so the viewer knows a mail)
var fileKeyRE = regexp.MustCompile(`^[0-9a-f]{20}(\.[a-z0-9]{1,8})?$`)

func (s *Server) filesDir() string { return filepath.Join(s.st.dir, "files") }

// hFileUpload stores a file: {name, data: base64} → {file: key}. JSON because the server only accepts
// JSON posts (a web page elsewhere cannot send JSON to it without the browser asking first).
func (s *Server) hFileUpload(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name, Data string }
	if err := readBody(r, &req, maxAttachment*4/3+4096); err != nil {
		jerr(w, http.StatusRequestEntityTooLarge, errors.New("the file is larger than 10 MB"))
		return
	}
	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		jerr(w, http.StatusBadRequest, errors.New("data must be base64"))
		return
	}
	if len(data) > maxAttachment {
		jerr(w, http.StatusRequestEntityTooLarge, errors.New("the file is larger than 10 MB"))
		return
	}
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		fail(w, err)
		return
	}
	key := hex.EncodeToString(b)
	if ext := strings.ToLower(filepath.Ext(req.Name)); regexp.MustCompile(`^\.[a-z0-9]{1,8}$`).MatchString(ext) {
		key += ext
	}
	if err := os.MkdirAll(s.filesDir(), 0o700); err != nil {
		fail(w, err)
		return
	}
	if err := os.WriteFile(filepath.Join(s.filesDir(), key), data, 0o600); err != nil {
		fail(w, err)
		return
	}
	jout(w, 200, map[string]any{"file": key, "size": len(data)})
}

func (s *Server) readFile(w http.ResponseWriter, r *http.Request) ([]byte, string, bool) {
	key := r.PathValue("key")
	if !fileKeyRE.MatchString(key) {
		jerr(w, http.StatusBadRequest, errors.New("invalid file key"))
		return nil, "", false
	}
	data, err := os.ReadFile(filepath.Join(s.filesDir(), key))
	if err != nil {
		jerr(w, http.StatusNotFound, errors.New("file not found (it is not in data/files)"))
		return nil, "", false
	}
	return data, key, true
}

// hFileGet downloads a stored file. Always as a download, never shown by the browser: a stored HTML
// file must not run as a page of this app.
func (s *Server) hFileGet(w http.ResponseWriter, r *http.Request) {
	data, key, ok := s.readFile(w, r)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = key
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// hFileView returns what the viewer shows: {kind: "mail"|"text"|"binary", text, headers, attachments}.
func (s *Server) hFileView(w http.ResponseWriter, r *http.Request) {
	data, key, ok := s.readFile(w, r)
	if !ok {
		return
	}
	if strings.HasSuffix(key, ".eml") {
		if v, err := viewMail(data); err == nil {
			jout(w, 200, v)
			return
		}
	}
	if t, ok := asText(data); ok {
		jout(w, 200, map[string]any{"kind": "text", "text": t})
		return
	}
	jout(w, 200, map[string]any{"kind": "binary", "size": len(data)})
}

// asText: the file as text, if it looks like text (no zero bytes). UTF-8, or else Latin-1.
func asText(b []byte) (string, bool) {
	head := b[:min(len(b), 8192)]
	if bytes.IndexByte(head, 0) >= 0 {
		return "", false
	}
	if utf8.Valid(b) {
		return string(b), true
	}
	return latin1(b), true
}

func latin1(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

// decodeCharset: text in the charset of a mail part. UTF-8 and ASCII as they are; Latin-1 and
// Windows-1252 (close enough for display) byte by byte; others as they are.
func decodeCharset(b []byte, charset string) string {
	switch strings.ToLower(charset) {
	case "iso-8859-1", "iso-8859-15", "latin1", "windows-1252", "cp1252":
		return latin1(b)
	}
	return strings.ToValidUTF8(string(b), "�")
}

var wordDec = &mime.WordDecoder{CharsetReader: func(cs string, in io.Reader) (io.Reader, error) {
	b, err := io.ReadAll(in)
	return strings.NewReader(decodeCharset(b, cs)), err
}}

// viewMail reads a mail (.eml): the main headers, the text (the plain text part, else the HTML part
// without tags) and the names of attached files.
func viewMail(data []byte) (map[string]any, error) {
	m, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	hd := map[string]string{}
	for _, k := range []string{"From", "To", "Cc", "Date", "Subject"} {
		if v := m.Header.Get(k); v != "" {
			if d, err := wordDec.DecodeHeader(v); err == nil {
				v = d
			}
			hd[k] = v
		}
	}
	var plain, htm string
	atts := []string{}
	var walk func(h map[string][]string, body io.Reader, depth int)
	walk = func(h map[string][]string, body io.Reader, depth int) {
		get := func(k string) string {
			if v := h[k]; len(v) > 0 {
				return v[0]
			}
			return ""
		}
		mt, params, err := mime.ParseMediaType(get("Content-Type"))
		if err != nil {
			mt, params = "text/plain", map[string]string{}
		}
		if strings.HasPrefix(mt, "multipart/") && depth < 10 {
			mr := multipart.NewReader(body, params["boundary"])
			for {
				p, err := mr.NextRawPart() // raw: the encoding is decoded below, the same way for every part
				if err != nil {
					return
				}
				walk(p.Header, p, depth+1)
			}
		}
		disp, dp, _ := mime.ParseMediaType(get("Content-Disposition"))
		name := dp["filename"]
		if name == "" {
			name = params["name"]
		}
		if d, err := wordDec.DecodeHeader(name); err == nil {
			name = d
		}
		if disp == "attachment" || (name != "" && !strings.HasPrefix(mt, "text/")) {
			atts = append(atts, name)
			return
		}
		switch strings.ToLower(get("Content-Transfer-Encoding")) {
		case "base64":
			body = base64.NewDecoder(base64.StdEncoding, &stripSpace{r: body})
		case "quoted-printable":
			body = quotedprintable.NewReader(body)
		}
		b, _ := io.ReadAll(io.LimitReader(body, maxAttachment))
		t := decodeCharset(b, params["charset"])
		switch {
		case mt == "text/plain" && plain == "":
			plain = t
		case mt == "text/html" && htm == "":
			htm = t
		}
	}
	walk(m.Header, m.Body, 0)
	text := plain
	if text == "" && htm != "" {
		text = htmlText(htm)
	}
	return map[string]any{"kind": "mail", "headers": hd, "text": strings.TrimSpace(text), "attachments": atts}, nil
}

// stripSpace drops line breaks and spaces, which base64 bodies of mails contain
type stripSpace struct{ r io.Reader }

func (s *stripSpace) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	j := 0
	for _, c := range p[:n] {
		if c != '\r' && c != '\n' && c != ' ' && c != '\t' {
			p[j] = c
			j++
		}
	}
	return j, err
}

var (
	reHTMLDrop  = regexp.MustCompile(`(?is)<(script|style|head)\b.*?</(script|style|head)>`)
	reHTMLBreak = regexp.MustCompile(`(?i)<(br|/p|/div|/tr|/li|/h[1-6])\b[^>]*>`)
	reHTMLTag   = regexp.MustCompile(`(?s)<[^>]*>`)
	reBlank     = regexp.MustCompile(`\n[ \t]*\n(\s*\n)+`)
)

// htmlText: rough text of an HTML mail, for reading only
func htmlText(h string) string {
	h = reHTMLDrop.ReplaceAllString(h, "")
	h = reHTMLBreak.ReplaceAllString(h, "\n")
	h = html.UnescapeString(reHTMLTag.ReplaceAllString(h, ""))
	h = strings.ReplaceAll(h, "\u00a0", " ")
	return reBlank.ReplaceAllString(h, "\n\n")
}
