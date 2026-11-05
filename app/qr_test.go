package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// values from the QR standard
func TestQRParts(t *testing.T) {
	// "01234567", version 1-M: the data codewords and their error correction codewords
	data := []byte{0x10, 0x20, 0x0C, 0x56, 0x61, 0x80, 0xEC, 0x11, 0xEC, 0x11, 0xEC, 0x11, 0xEC, 0x11, 0xEC, 0x11}
	want := []byte{0xA5, 0x24, 0xD4, 0xC1, 0xED, 0x36, 0xC7, 0x87, 0x2C, 0x55}
	if got := rsEC(data, 10); !bytes.Equal(got, want) {
		t.Errorf("Reed-Solomon: % X, want % X", got, want)
	}
	if got := qrFormatBits(0); got != 0b101010000010010 {
		t.Errorf("format bits M/mask 0: %015b", got)
	}
	if got := qrVersionBits(7); got != 0x07C94 {
		t.Errorf("version bits 7: %X", got)
	}
	if _, err := NewQR(strings.Repeat("x", 214)); err == nil {
		t.Error("214 bytes must be too long")
	}
}

// encode texts of many lengths (versions 1 to 10) and read them back with zbarimg, if it is installed
func TestQRReadBack(t *testing.T) {
	zbar, err := exec.LookPath("zbarimg")
	if err != nil {
		t.Skip("zbarimg not installed (apt install zbar-tools)")
	}
	base := "http://192.168.178.20:7437/#pair=Ab3-_xYz09"
	for _, n := range []int{1, 14, 15, 26, 27, 42, 43, 62, 63, 84, 85, 106, 107, 122, 123, 152, 153, 180, 181, 213} {
		text := strings.Repeat(base, n/len(base)+1)[:n]
		q, err := NewQR(text)
		if err != nil {
			t.Fatalf("%d bytes: %v", n, err)
		}
		const scale, border = 6, 4
		w := (q.Size + 2*border) * scale
		img := image.NewGray(image.Rect(0, 0, w, w))
		for y := 0; y < w; y++ {
			for x := 0; x < w; x++ {
				mx, my := x/scale-border, y/scale-border
				c := color.Gray{255}
				if mx >= 0 && my >= 0 && mx < q.Size && my < q.Size && q.Mod[my][mx] {
					c = color.Gray{0}
				}
				img.SetGray(x, y, c)
			}
		}
		f := filepath.Join(t.TempDir(), fmt.Sprintf("qr%d.png", n))
		var buf bytes.Buffer
		_ = png.Encode(&buf, img)
		if err := os.WriteFile(f, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(zbar, "--raw", "-q", f).Output()
		if got := strings.TrimRight(string(out), "\n"); err != nil || got != text {
			t.Errorf("%d bytes (version %d): read %q, err %v", n, (q.Size-17)/4, got, err)
		}
	}
}
