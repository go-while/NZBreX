package rapidyenc

import (
	"bytes"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	yencsimd "github.com/go-while/go-yenc-simd"
)

// go-yenc-simd against this package's C decoder, call for call. (go-yenc-simd
// has its own tests without cgo; these are the cross-check on this machine.)

type decodeFunc func(dst, src []byte, st *State) (int, int, End)

func cDec(dst, src []byte, st *State) (int, int, End) {
	nd, ns, end, err := DecodeIncremental(dst, src, st)
	if err != nil {
		panic(err)
	}
	return nd, ns, end
}

func goDec(dst, src []byte, st *State) (int, int, End) {
	gs := yencsimd.State(*st)
	nd, ns, end, err := yencsimd.DecodeIncremental(dst, src, &gs)
	if err != nil {
		panic(err)
	}
	*st = State(gs)
	return nd, ns, End(end)
}

func compareOnce(t testing.TB, src []byte, st State) {
	t.Helper()
	gDst, cDst := make([]byte, len(src)+64), make([]byte, len(src)+64)
	gSt, cSt := st, st
	gnd, gns, gend := goDec(gDst, src, &gSt)
	cnd, cns, cend := cDec(cDst, src, &cSt)
	if gnd != cnd || gns != cns || gend != cend || gSt != cSt || !bytes.Equal(gDst[:gnd], cDst[:cnd]) {
		t.Fatalf("go-yenc-simd (%s) differs from C, state %d src=%q\n go: nDst=%d nSrc=%d end=%d state=%d\n  c: nDst=%d nSrc=%d end=%d state=%d",
			yencsimd.Kernel(), st, src, gnd, gns, gend, gSt, cnd, cns, cend, cSt)
	}
}

func decodeInChunks(src []byte, cuts []int, dec decodeFunc) ([]byte, int, End, State) {
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

func denseRaw(r *rand.Rand, n int) []byte {
	alphabet := []byte("=\r\n.y=\r\naZ\x00\xff\t ")
	b := make([]byte, n)
	for i := 0; i < n; {
		if r.IntN(4) == 0 {
			run := min(n-i, 1+r.IntN(80))
			for j := 0; j < run; j++ {
				c := byte(r.IntN(256))
				for c == '=' || c == '\r' || c == '\n' {
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

func sortedCuts(r *rand.Rand, n int) []int {
	cuts := make([]int, r.IntN(8))
	for i := range cuts {
		cuts[i] = r.IntN(n + 1)
	}
	for i := 1; i < len(cuts); i++ {
		for j := i; j > 0 && cuts[j] < cuts[j-1]; j-- {
			cuts[j], cuts[j-1] = cuts[j-1], cuts[j]
		}
	}
	return cuts
}

func TestGoDecoderMatchesCRandom(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for iter := 0; iter < 200000; iter++ {
		n := r.IntN(300)
		if iter%10 == 0 {
			n = r.IntN(4000) // long enough for the C SIMD kernel
		}
		compareOnce(t, denseRaw(r, n), State(r.IntN(7)))
	}
}

func TestGoDecoderMatchesCChunked(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for iter := 0; iter < 20000; iter++ {
		src := denseRaw(r, r.IntN(3000))
		cuts := sortedCuts(r, len(src))
		gOut, gPos, gEnd, gSt := decodeInChunks(src, cuts, goDec)
		cOut, cPos, cEnd, cSt := decodeInChunks(src, cuts, cDec)
		if !bytes.Equal(gOut, cOut) || gPos != cPos || gEnd != cEnd || gSt != cSt {
			t.Fatalf("chunked decode differs: cuts=%v go(pos=%d end=%d st=%d) c(pos=%d end=%d st=%d)", cuts, gPos, gEnd, gSt, cPos, cEnd, cSt)
		}
	}
}

// encodeNNTP yEnc-encodes data with this package's C encoder and dot-stuffs it.
func encodeNNTP(data []byte) []byte {
	body := NewEncoder().Encode(data)
	var b bytes.Buffer
	for _, line := range bytes.SplitAfter(body, []byte("\r\n")) {
		if len(line) > 0 && line[0] == '.' {
			b.WriteByte('.')
		}
		b.Write(line)
	}
	if !bytes.HasSuffix(body, []byte("\r\n")) {
		b.WriteString("\r\n")
	}
	b.WriteString("=yend size=0\r\n.\r\n")
	return b.Bytes()
}

func TestGoDecoderMatchesCArticles(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	for iter := 0; iter < 300; iter++ {
		data := make([]byte, 1+r.IntN(800000))
		for i := range data {
			data[i] = byte(r.Uint32())
		}
		if iter%3 == 0 { // many bytes that need escaping
			for i := range data {
				data[i] = []byte{0, '\n', '\r', '=' - 42, '.' - 42, 223, 246, 214}[r.IntN(8)]
			}
		}
		body := encodeNNTP(data)
		cuts := sortedCuts(r, len(body))
		gOut, gPos, gEnd, _ := decodeInChunks(body, cuts, goDec)
		cOut, cPos, cEnd, _ := decodeInChunks(body, cuts, cDec)
		if gEnd != EndControl || !bytes.Equal(gOut, data) {
			t.Fatalf("article %d (%d bytes): end=%d, decoded %d bytes, equal=%v", iter, len(data), gEnd, len(gOut), bytes.Equal(gOut, data))
		}
		if !bytes.Equal(gOut, cOut) || gPos != cPos || gEnd != cEnd {
			t.Fatalf("article %d differs from C", iter)
		}
	}
}

func TestGoDecoderMatchesCSampleFiles(t *testing.T) {
	files, _ := filepath.Glob("yenc/*.yenc")
	if len(files) == 0 {
		t.Skip("no sample files")
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		raw = bytes.ReplaceAll(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))
		bodies := 0
		for off := 0; ; bodies++ {
			i := bytes.Index(raw[off:], []byte("\r\n=y"))
			if i < 0 {
				break
			}
			lineEnd := bytes.Index(raw[off+i+2:], []byte("\r\n"))
			if lineEnd < 0 {
				break
			}
			off += i + 2 + lineEnd + 2
			body := raw[off:]
			gOut, gPos, gEnd, _ := decodeInChunks(body, nil, goDec)
			cOut, cPos, cEnd, _ := decodeInChunks(body, nil, cDec)
			if !bytes.Equal(gOut, cOut) || gPos != cPos || gEnd != cEnd {
				t.Fatalf("%s @%d: differs from C", f, off)
			}
		}
		if bodies == 0 {
			t.Fatalf("%s: no yEnc body found", f)
		}
	}
}

func FuzzGoDecoderMatchesC(f *testing.F) {
	f.Add([]byte("abc\r\n..def\r\n=yend"), uint8(0))
	f.Add([]byte("=\r\n.\r\n"), uint8(1))
	f.Add([]byte("\r\n.=yx"), uint8(4))
	f.Fuzz(func(t *testing.T, src []byte, st uint8) {
		compareOnce(t, src, State(st%7))
	})
}

// The decode loop alone, C vs Go, on a 750 KB article body.
var decodeBenchBody = func() []byte {
	r := rand.New(rand.NewPCG(7, 8))
	data := make([]byte, 750_000)
	for i := range data {
		data[i] = byte(r.Uint32())
	}
	return encodeNNTP(data)
}()

func benchDecodeLoop(b *testing.B, dec decodeFunc) {
	dst := make([]byte, len(decodeBenchBody))
	b.SetBytes(int64(len(decodeBenchBody)))
	for b.Loop() {
		st := StateCRLF
		if _, _, end := dec(dst, decodeBenchBody, &st); end != EndControl {
			b.Fatalf("end=%d", end)
		}
	}
}

func BenchmarkDecodeLoopC(b *testing.B) {
	b.Logf("rapidyenc kernel=%#x", DecodeKernel())
	benchDecodeLoop(b, cDec)
}

func BenchmarkDecodeLoopGo(b *testing.B) {
	b.Logf("go-yenc-simd kernel=%s", yencsimd.Kernel())
	benchDecodeLoop(b, goDec)
}
