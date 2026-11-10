package main

import (
	"archive/zip"
	"bytes"
	"testing"
)

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Word and LibreOffice files: their text, paragraph by paragraph, without styles
func TestOfficeText(t *testing.T) {
	docx := zipOf(t, map[string]string{"word/document.xml": `<?xml version="1.0"?><w:document xmlns:w="w"><w:body>` +
		`<w:p><w:r><w:rPr><w:b/></w:rPr><w:t>Contract</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t xml:space="preserve">Sign it by </w:t></w:r><w:r><w:t>Friday.</w:t></w:r><w:r><w:tab/><w:t>Thanks</w:t></w:r></w:p>` +
		`</w:body></w:document>`})
	if got, ok := officeText(docx, "0123456789abcdef0123.docx"); !ok || got != "Contract\nSign it by Friday.\tThanks" {
		t.Errorf("docx: %q %v", got, ok)
	}
	odt := zipOf(t, map[string]string{"content.xml": `<?xml version="1.0"?><office:document-content xmlns:office="o" xmlns:text="t">` +
		`<office:body><office:text><text:h>Plan</text:h><text:p>Buy<text:s/>milk, <text:span>bread</text:span></text:p>` +
		`<text:p>Line one<text:line-break/>line two</text:p></office:text></office:body></office:document-content>`})
	if got, ok := officeText(odt, "0123456789abcdef0123.odt"); !ok || got != "Plan\nBuy milk, bread\nLine one\nline two" {
		t.Errorf("odt: %q %v", got, ok)
	}
	if _, ok := officeText([]byte("not a zip"), "x.docx"); ok {
		t.Error("a broken docx must not count as read")
	}
	if _, ok := officeText(docx, "x.zip"); ok {
		t.Error("other file types are not office files")
	}
}
