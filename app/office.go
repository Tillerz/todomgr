package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strings"
)

// officeText reads the text of a Word (.docx) or LibreOffice / OpenDocument (.odt) file. Both are zip
// files with the text in one XML file, so only Go's own packages are needed. Paragraphs become lines;
// tables, pictures and styles are left out.
func officeText(data []byte, key string) (string, bool) {
	var part, textTag string
	switch {
	case strings.HasSuffix(key, ".docx"):
		part, textTag = "word/document.xml", "t" // Word keeps the words in <w:t>
	case strings.HasSuffix(key, ".odt"):
		part = "content.xml" // OpenDocument keeps them directly in <text:p> and <text:h>
	default:
		return "", false
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", false
	}
	for _, f := range zr.File {
		if f.Name != part {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", false
		}
		defer rc.Close()
		var b strings.Builder
		in := 0                                           // inside an element whose text counts
		dec := xml.NewDecoder(io.LimitReader(rc, 32<<20)) // a zip bomb stops here
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			switch t := tok.(type) {
			case xml.StartElement:
				switch n := t.Name.Local; {
				case textTag != "" && n == textTag, textTag == "" && (n == "p" || n == "h"):
					in++
				case n == "tab":
					b.WriteString("\t")
				case n == "br" || n == "line-break":
					b.WriteString("\n")
				case textTag == "" && n == "s":
					b.WriteString(" ")
				}
			case xml.EndElement:
				switch n := t.Name.Local; {
				case textTag != "" && n == textTag:
					in--
				case n == "p" || n == "h":
					if textTag == "" {
						in--
					}
					b.WriteString("\n")
				}
			case xml.CharData:
				if in > 0 {
					b.Write(t)
				}
			}
		}
		return strings.TrimSpace(b.String()), true
	}
	return "", false
}
