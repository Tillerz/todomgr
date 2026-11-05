package main

import (
	"fmt"
	"strings"
)

// QR codes (ISO/IEC 18004), only what the LAN pairing link needs: byte mode, error correction
// level M, versions 1–10 (up to 213 bytes). Written here so the program needs no other library.

// qrBlocks per version for level M: error correction codewords per block, and groups of
// {number of blocks, data codewords per block}.
var qrBlocks = [11]struct {
	ec     int
	groups [][2]int
}{
	{}, {10, [][2]int{{1, 16}}}, {16, [][2]int{{1, 28}}}, {26, [][2]int{{1, 44}}}, {18, [][2]int{{2, 32}}},
	{24, [][2]int{{2, 43}}}, {16, [][2]int{{4, 27}}}, {18, [][2]int{{4, 31}}}, {22, [][2]int{{2, 38}, {2, 39}}},
	{22, [][2]int{{3, 36}, {2, 37}}}, {26, [][2]int{{4, 43}, {1, 44}}},
}

// qrAlign: centre rows/columns of the alignment patterns per version.
var qrAlign = [11][]int{nil, nil, {6, 18}, {6, 22}, {6, 26}, {6, 30}, {6, 34}, {6, 22, 38}, {6, 24, 42}, {6, 26, 46}, {6, 28, 50}}

// ---------------------------------------------------------------- Reed-Solomon over GF(256)

var gfExp [512]byte
var gfLog [256]byte

func init() {
	x := 1
	for i := 0; i < 255; i++ {
		gfExp[i], gfLog[x] = byte(x), byte(i)
		if x <<= 1; x&0x100 != 0 {
			x ^= 0x11d
		}
	}
	for i := 255; i < 512; i++ {
		gfExp[i] = gfExp[i-255]
	}
}

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

// rsEC returns the n error correction codewords for data.
func rsEC(data []byte, n int) []byte {
	g := []byte{1} // generator polynomial, highest power first: (x - α^0)…(x - α^(n-1))
	for i := 0; i < n; i++ {
		ng := make([]byte, len(g)+1)
		for j, c := range g {
			ng[j] ^= c
			ng[j+1] ^= gfMul(c, gfExp[i])
		}
		g = ng
	}
	r := make([]byte, n)
	for _, d := range data {
		f := d ^ r[0]
		copy(r, r[1:])
		r[n-1] = 0
		for j := 0; j < n; j++ {
			r[j] ^= gfMul(g[j+1], f)
		}
	}
	return r
}

// ---------------------------------------------------------------- encoding

// qrCodewords turns text into the final, interleaved codewords and returns the version.
func qrCodewords(text string) (int, []byte, error) {
	ver := 0
	for v := 1; v <= 10; v++ {
		cc := 8
		if v >= 10 {
			cc = 16
		}
		if 4+cc+8*len(text) <= 8*qrData(v) {
			ver = v
			break
		}
	}
	if ver == 0 {
		return 0, nil, fmt.Errorf("text too long for a QR code: %d bytes", len(text))
	}
	var bits []bool
	put := func(v, n int) {
		for i := n - 1; i >= 0; i-- {
			bits = append(bits, v>>i&1 == 1)
		}
	}
	put(0b0100, 4) // byte mode
	if ver >= 10 {
		put(len(text), 16)
	} else {
		put(len(text), 8)
	}
	for i := 0; i < len(text); i++ {
		put(int(text[i]), 8)
	}
	capBits := 8 * qrData(ver)
	put(0, min(4, capBits-len(bits))) // terminator
	for len(bits)%8 != 0 {
		bits = append(bits, false)
	}
	data := make([]byte, 0, qrData(ver))
	for i := 0; i < len(bits); i += 8 {
		var b byte
		for j := 0; j < 8; j++ {
			if bits[i+j] {
				b |= 0x80 >> j
			}
		}
		data = append(data, b)
	}
	for pad := byte(0xEC); len(data) < qrData(ver); pad ^= 0xEC ^ 0x11 {
		data = append(data, pad)
	}
	// split into blocks, add error correction, interleave
	bl := qrBlocks[ver]
	var dataBlocks, ecBlocks [][]byte
	for _, g := range bl.groups {
		for k := 0; k < g[0]; k++ {
			d := data[:g[1]]
			data = data[g[1]:]
			dataBlocks, ecBlocks = append(dataBlocks, d), append(ecBlocks, rsEC(d, bl.ec))
		}
	}
	var out []byte
	for i := 0; i < 44; i++ { // 44: the longest data block up to version 10
		for _, d := range dataBlocks {
			if i < len(d) {
				out = append(out, d[i])
			}
		}
	}
	for i := 0; i < bl.ec; i++ {
		for _, e := range ecBlocks {
			out = append(out, e[i])
		}
	}
	return ver, out, nil
}

func qrData(v int) int {
	n := 0
	for _, g := range qrBlocks[v].groups {
		n += g[0] * g[1]
	}
	return n
}

// qrFormatBits: error correction level M (00) and the mask, with BCH bits and the fixed XOR mask.
func qrFormatBits(mask int) int {
	data := mask // level M is 00
	rem := data
	for i := 0; i < 10; i++ {
		rem = rem<<1 ^ (rem>>9)*0x537
	}
	return (data<<10 | rem) ^ 0x5412
}

func qrVersionBits(v int) int {
	rem := v
	for i := 0; i < 12; i++ {
		rem = rem<<1 ^ (rem>>11)*0x1F25
	}
	return v<<12 | rem
}

// QR is a finished symbol: Mod[y][x] true = dark.
type QR struct {
	Size int
	Mod  [][]bool
}

// NewQR encodes text (byte mode, level M) and picks the mask with the lowest penalty.
func NewQR(text string) (*QR, error) {
	ver, cw, err := qrCodewords(text)
	if err != nil {
		return nil, err
	}
	size := 17 + 4*ver
	q := &QR{Size: size, Mod: make([][]bool, size)}
	fn := make([][]bool, size) // function patterns: never masked, never data
	for i := range q.Mod {
		q.Mod[i], fn[i] = make([]bool, size), make([]bool, size)
	}
	set := func(x, y int, dark bool) { q.Mod[y][x], fn[y][x] = dark, true }
	for i := 0; i < size; i++ { // timing patterns
		set(6, i, i%2 == 0)
		set(i, 6, i%2 == 0)
	}
	for _, c := range [][2]int{{3, 3}, {size - 4, 3}, {3, size - 4}} { // finders with their light border
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				x, y := c[0]+dx, c[1]+dy
				if x >= 0 && x < size && y >= 0 && y < size {
					d := max(abs(dx), abs(dy))
					set(x, y, d != 2 && d != 4)
				}
			}
		}
	}
	al := qrAlign[ver]
	for i, ay := range al {
		for j, ax := range al {
			if i == 0 && j == 0 || i == 0 && j == len(al)-1 || i == len(al)-1 && j == 0 {
				continue // these places hold finders
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					set(ax+dx, ay+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	q.drawFormat(fn, 0) // reserve the format areas; drawn again with the chosen mask
	if ver >= 7 {
		vb := qrVersionBits(ver)
		for i := 0; i < 18; i++ {
			a, b, dark := size-11+i%3, i/3, vb>>i&1 == 1
			set(a, b, dark)
			set(b, a, dark)
		}
	}
	// data, in two-column strips from the bottom right, up and down in turn; column 6 is the timing pattern
	i := 0
	for right := size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := 0; vert < size; vert++ {
			for j := 0; j < 2; j++ {
				x, y := right-j, vert
				if (right+1)&2 == 0 {
					y = size - 1 - vert
				}
				if !fn[y][x] && i < len(cw)*8 {
					q.Mod[y][x] = cw[i>>3]>>(7-i&7)&1 == 1
					i++
				}
			}
		}
	}
	best, bestPen := 0, -1
	for m := 0; m < 8; m++ {
		q.applyMask(fn, m)
		q.drawFormat(fn, m)
		if p := q.penalty(); bestPen < 0 || p < bestPen {
			best, bestPen = m, p
		}
		q.applyMask(fn, m) // undo
	}
	q.applyMask(fn, best)
	q.drawFormat(fn, best)
	return q, nil
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func (q *QR) drawFormat(fn [][]bool, mask int) {
	b, n := qrFormatBits(mask), q.Size
	set := func(x, y, i int) { q.Mod[y][x], fn[y][x] = b>>i&1 == 1, true }
	for i := 0; i <= 5; i++ {
		set(8, i, i)
	}
	set(8, 7, 6)
	set(8, 8, 7)
	set(7, 8, 8)
	for i := 9; i < 15; i++ {
		set(14-i, 8, i)
	}
	for i := 0; i < 8; i++ {
		set(n-1-i, 8, i)
	}
	for i := 8; i < 15; i++ {
		set(8, n-15+i, i)
	}
	q.Mod[n-8][8], fn[n-8][8] = true, true // the dark module
}

func (q *QR) applyMask(fn [][]bool, m int) {
	for y := 0; y < q.Size; y++ {
		for x := 0; x < q.Size; x++ {
			var flip bool
			switch m {
			case 0:
				flip = (x+y)%2 == 0
			case 1:
				flip = y%2 == 0
			case 2:
				flip = x%3 == 0
			case 3:
				flip = (x+y)%3 == 0
			case 4:
				flip = (x/3+y/2)%2 == 0
			case 5:
				flip = x*y%2+x*y%3 == 0
			case 6:
				flip = (x*y%2+x*y%3)%2 == 0
			case 7:
				flip = ((x+y)%2+x*y%3)%2 == 0
			}
			if flip && !fn[y][x] {
				q.Mod[y][x] = !q.Mod[y][x]
			}
		}
	}
}

// penalty scores how hard the symbol is to read (the four rules of the standard); lower is better.
func (q *QR) penalty() int {
	n, p, dark := q.Size, 0, 0
	at := func(x, y int, col bool) bool {
		if col {
			return q.Mod[x][y]
		}
		return q.Mod[y][x]
	}
	finder := []bool{true, false, true, true, true, false, true}
	for _, col := range []bool{false, true} {
		for y := 0; y < n; y++ {
			run := 1
			for x := 1; x <= n; x++ {
				if x < n && at(x, y, col) == at(x-1, y, col) {
					run++
					continue
				}
				if run >= 5 {
					p += run - 2
				}
				run = 1
			}
			for x := 0; x+7 <= n; x++ { // dark-light-dark×3-light-dark with 4 light modules on one side
				ok := true
				for k, d := range finder {
					if at(x+k, y, col) != d {
						ok = false
						break
					}
				}
				if !ok {
					continue
				}
				light := func(from, to int) bool {
					for k := from; k < to; k++ {
						if k >= 0 && k < n && at(k, y, col) {
							return false
						}
					}
					return true
				}
				if light(x-4, x) || light(x+7, x+11) {
					p += 40
				}
			}
		}
	}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if q.Mod[y][x] {
				dark++
			}
			if x+1 < n && y+1 < n {
				c := q.Mod[y][x]
				if q.Mod[y][x+1] == c && q.Mod[y+1][x] == c && q.Mod[y+1][x+1] == c {
					p += 3
				}
			}
		}
	}
	total := n * n
	return p + (abs(dark*20-total*10)+total-1)/total*10 - 10
}

// SVG draws the symbol with a 4-module light border, black on white.
func (q *QR) SVG() string {
	var b strings.Builder
	w := q.Size + 8
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges"><rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, w, w, w, w)
	for y := 0; y < q.Size; y++ {
		for x := 0; x < q.Size; x++ {
			if q.Mod[y][x] {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+4, y+4)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String()
}
