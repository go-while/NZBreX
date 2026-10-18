//go:build cgo

package ydec_test

import (
	. "github.com/go-while/NZBreX/ydec"

	"bytes"
	"hash/crc32"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-while/NZBreX/rapidyenc"
)

// Every test here compares ydec with the C library it replaces.

func cDecode(dst, src []byte, st *State) (int, int, End) {
	cs := rapidyenc.State(*st)
	nd, ns, end, err := rapidyenc.DecodeIncremental(dst, src, &cs)
	if err != nil {
		panic(err)
	}
	*st = State(cs)
	return nd, ns, End(end)
}

// compareOne decodes src once from st with both and fails on any difference.
func compareOne(t testing.TB, src []byte, st State) {
	t.Helper()
	gDst := make([]byte, len(src)+64)
	cDst := make([]byte, len(src)+64)
	gSt, cSt := st, st
	gnd, gns, gend, err := DecodeIncremental(gDst, src, &gSt)
	if err != nil {
		t.Fatal(err)
	}
	cnd, cns, cend := cDecode(cDst, src, &cSt)
	if gnd != cnd || gns != cns || gend != cend || gSt != cSt || !bytes.Equal(gDst[:gnd], cDst[:cnd]) {
		t.Fatalf("differs from rapidyenc for state %d src=%q\n go: nDst=%d nSrc=%d end=%d state=%d\n  c: nDst=%d nSrc=%d end=%d state=%d\n go out=%q\n  c out=%q",
			st, src, gnd, gns, gend, gSt, cnd, cns, cend, cSt, gDst[:gnd], cDst[:cnd])
	}
}

// decodeAll feeds src in chunks (cut at the given offsets) until the end or the
// data run out, like the Decoder does. It returns the output, the bytes read and how it ended.
func decodeAll(src []byte, cuts []int, dec func(dst, src []byte, st *State) (int, int, End)) ([]byte, int, End, State) {
	out := make([]byte, 0, len(src))
	dst := make([]byte, len(src)+64)
	st := StateCRLF
	pos := 0
	for _, cut := range append(cuts, len(src)) {
		if cut <= pos {
			continue
		}
		nd, ns, end := dec(dst, src[pos:cut], &st)
		out = append(out, dst[:nd]...)
		pos += ns
		if end != EndNone {
			return out, pos, end, st
		}
	}
	return out, pos, EndNone, st
}

func goDecode(dst, src []byte, st *State) (int, int, End) {
	nd, ns, end, err := DecodeIncremental(dst, src, st)
	if err != nil {
		panic(err)
	}
	return nd, ns, end
}

// randomRaw makes input dense with the bytes that matter (=, \r, \n, ., y) and
// some long plain runs, so the SIMD paths and every boundary get hit.
func randomRaw(r *rand.Rand, n int) []byte {
	alphabet := []byte("=\r\n.y=\r\naZ\x00\xff\t ")
	b := make([]byte, n)
	for i := 0; i < n; {
		if r.IntN(4) == 0 { // a plain run
			run := min(n-i, 1+r.IntN(80))
			for j := 0; j < run; j++ {
				c := byte(r.IntN(256))
				for Special[c] {
					c = byte(r.IntN(256))
				}
				b[i+j] = c
			}
			i += run
			continue
		}
		b[i] = alphabet[r.IntN(len(alphabet))]
		i++
	}
	return b
}

func TestDecodeMatchesRapidyencRandom(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for iter := 0; iter < 200000; iter++ {
		n := r.IntN(300)
		if iter%10 == 0 {
			n = r.IntN(4000) // long enough for rapidyenc's SIMD kernel
		}
		src := randomRaw(r, n)
		compareOne(t, src, State(r.IntN(7)))
	}
}

func TestDecodeMatchesRapidyencChunked(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for iter := 0; iter < 20000; iter++ {
		src := randomRaw(r, r.IntN(3000))
		var cuts []int
		for k := r.IntN(8); k > 0; k-- {
			cuts = append(cuts, r.IntN(len(src)+1))
		}
		sortInts(cuts)
		gOut, gPos, gEnd, gSt := decodeAll(src, cuts, goDecode)
		cOut, cPos, cEnd, cSt := decodeAll(src, cuts, cDecode)
		if !bytes.Equal(gOut, cOut) || gPos != cPos || gEnd != cEnd || gSt != cSt {
			t.Fatalf("chunked decode differs: cuts=%v go(pos=%d end=%d st=%d) c(pos=%d end=%d st=%d)\nsrc=%q", cuts, gPos, gEnd, gSt, cPos, cEnd, cSt, src)
		}
	}
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// encodeArticle yEnc-encodes data as rapidyenc does, then dot-stuffs it for
// NNTP and appends the trailer and the article end.
func encodeArticle(data []byte) []byte {
	enc := rapidyenc.NewEncoder()
	body := enc.Encode(data)
	var b bytes.Buffer
	b.WriteString("=ybegin line=128 size=" + itoa(len(data)) + " name=test.bin\r\n")
	for _, line := range bytes.SplitAfter(body, []byte("\r\n")) {
		if len(line) > 0 && line[0] == '.' {
			b.WriteByte('.')
		}
		b.Write(line)
	}
	if !bytes.HasSuffix(body, []byte("\r\n")) {
		b.WriteString("\r\n")
	}
	b.WriteString("=yend size=" + itoa(len(data)) + "\r\n.\r\n")
	return b.Bytes()
}

func itoa(n int) string { return string(appendInt(nil, n)) }
func appendInt(b []byte, n int) []byte {
	if n >= 10 {
		b = appendInt(b, n/10)
	}
	return append(b, byte('0'+n%10))
}

func TestDecodeArticles(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	for iter := 0; iter < 300; iter++ {
		data := make([]byte, 1+r.IntN(800000))
		for i := range data {
			data[i] = byte(r.Uint32())
		}
		if iter%3 == 0 { // many bytes that need escaping
			for i := range data {
				data[i] = []byte{0, '\n', '\r', '=' - 42, '.' - 42, 223 /* '\t'-42 */, 246 /* ' '-42 */, 214}[r.IntN(8)]
			}
		}
		art := encodeArticle(data)
		start := bytes.Index(art, []byte("\r\n")) + 2 // body starts after =ybegin
		body := art[start:]
		var cuts []int
		for k := r.IntN(6); k > 0; k-- {
			cuts = append(cuts, r.IntN(len(body)+1))
		}
		sortInts(cuts)
		gOut, gPos, gEnd, _ := decodeAll(body, cuts, goDecode)
		cOut, cPos, cEnd, _ := decodeAll(body, cuts, cDecode)
		if gEnd != EndControl || !bytes.Equal(gOut, data) {
			t.Fatalf("article %d (%d bytes): end=%d, decoded %d bytes, equal=%v", iter, len(data), gEnd, len(gOut), bytes.Equal(gOut, data))
		}
		if !bytes.Equal(gOut, cOut) || gPos != cPos || gEnd != cEnd {
			t.Fatalf("article %d differs from rapidyenc", iter)
		}
		if crc32.ChecksumIEEE(gOut) != crc32.ChecksumIEEE(data) {
			t.Fatal("crc")
		}
	}
}

// The sample files shipped with rapidyenc decode to the same bytes.
func TestDecodeSampleFiles(t *testing.T) {
	files, _ := filepath.Glob("../rapidyenc/yenc/*.yenc")
	if len(files) == 0 {
		t.Skip("no sample files")
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		raw = bytes.ReplaceAll(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))
		for off := 0; off < len(raw); {
			// decode each body after a =ybegin/=ypart header line
			i := bytes.Index(raw[off:], []byte("\r\n=y"))
			if i < 0 {
				break
			}
			lineEnd := bytes.Index(raw[off+i+2:], []byte("\r\n"))
			if lineEnd < 0 {
				break
			}
			body := raw[off+i+2+lineEnd+2:]
			off += i + 2 + lineEnd + 2
			gOut, gPos, gEnd, _ := decodeAll(body, nil, goDecode)
			cOut, cPos, cEnd, _ := decodeAll(body, nil, cDecode)
			if !bytes.Equal(gOut, cOut) || gPos != cPos || gEnd != cEnd {
				t.Fatalf("%s @%d: differs from rapidyenc (go %d bytes end=%d, c %d bytes end=%d)", f, off, len(gOut), gEnd, len(cOut), cEnd)
			}
		}
	}
}

func FuzzDecodeMatchesRapidyenc(f *testing.F) {
	f.Add([]byte("abc\r\n..def\r\n=yend"), uint8(0))
	f.Add([]byte("=\r\n.\r\n"), uint8(1))
	f.Add([]byte("\r\n.=yx"), uint8(4))
	f.Fuzz(func(t *testing.T, src []byte, st uint8) {
		compareOne(t, src, State(st%7))
	})
}
