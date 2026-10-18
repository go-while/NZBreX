//go:build cgo

package ydec_test

import (
	. "github.com/go-while/NZBreX/ydec"

	"bytes"
	"math/rand/v2"
	"testing"

	"github.com/go-while/NZBreX/rapidyenc"
)

// benchBody is a typical article body: ~750 KB of random data, yEnc encoded
// (128-byte lines, ~1.6% escapes) and dot-stuffed, up to and including "=yend".
var benchBody, benchData = func() ([]byte, []byte) {
	r := rand.New(rand.NewPCG(7, 8))
	data := make([]byte, 750_000)
	for i := range data {
		data[i] = byte(r.Uint32())
	}
	art := encodeArticle(data)
	return art[bytes.Index(art, []byte("\r\n"))+2:], data
}()

func benchDecode(b *testing.B, dec func(dst, src []byte, st *State) (int, int, End)) {
	dst := make([]byte, len(benchBody))
	b.SetBytes(int64(len(benchBody)))
	b.ReportAllocs()
	for b.Loop() {
		st := StateCRLF
		nd, _, end := dec(dst, benchBody, &st)
		if end != EndControl || nd != len(benchData) {
			b.Fatalf("end=%d nd=%d", end, nd)
		}
	}
}

func BenchmarkDecodeGo(b *testing.B) {
	b.Logf("kernel=%s", Kernel())
	benchDecode(b, goDecode)
}

func BenchmarkDecodeRapidyencC(b *testing.B) {
	b.Logf("rapidyenc kernel=%#x", rapidyenc.DecodeKernel())
	benchDecode(b, cDecode)
}

// The Go decoder without SIMD, on the same CPU.
func BenchmarkDecodeGoScalar(b *testing.B) {
	benchDecode(b, func(dst, src []byte, st *State) (int, int, End) { return DecodeScalar(dst, src, st) })
}

// Through the whole io.Reader path the downloader uses (headers, CRC, size checks).
func BenchmarkReaderRapidyencC(b *testing.B) {
	art := append([]byte("=ybegin line=128 size=750000 name=test.bin\r\n"), benchBody...)
	art = append(art, []byte(" size=750000\r\n.\r\n")...)
	buf := make([]byte, 64*1024)
	b.SetBytes(int64(len(art)))
	b.ReportAllocs()
	for b.Loop() {
		d := rapidyenc.AcquireDecoderWithReader(bytes.NewReader(art))
		n := 0
		for {
			k, err := d.Read(buf)
			n += k
			if err != nil {
				break
			}
		}
		rapidyenc.ReleaseDecoder(d)
		if n != len(benchData) {
			b.Fatalf("n=%d", n)
		}
	}
}
