package rapidyenc

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"time"
)

// BenchDecodeIncremental measures this package's DecodeIncremental (rapidyenc's
// C code in builds with -tags rapidyenc, go-yenc-simd otherwise) for d and
// returns MB/s of input. The input is yencbench's "random128" article: the same
// size bytes from the same generator and seed, 128-byte lines, so the number
// can be compared with yencbench's row of that name.
func BenchDecodeIncremental(d time.Duration, size int) (float64, error) {
	src := benchBody(size)
	dst := make([]byte, len(src))
	once := func() error {
		st := StateCRLF
		nd, _, end, err := DecodeIncremental(dst, src, &st)
		if err != nil || end != EndControl || nd != size {
			return fmt.Errorf("benchmark: DecodeIncremental gave %d of %d bytes, end %d, %v", nd, size, end, err)
		}
		return nil
	}
	if err := once(); err != nil {
		return 0, err
	}
	start := time.Now()
	iters := 0
	var elapsed time.Duration
	for elapsed < d {
		if err := once(); err != nil {
			return 0, err
		}
		iters++
		elapsed = time.Since(start)
	}
	return float64(len(src)) * float64(iters) / elapsed.Seconds() / 1e6, nil
}

// benchBody yEnc-encodes size random bytes like yencbench's random128 input
// (PCG seed 7) and returns the body after the =ybegin line, ending with the
// =yend line.
func benchBody(size int) []byte {
	r := rand.New(rand.NewPCG(7, 8))
	var b bytes.Buffer
	col := 0
	for i := range size {
		o := byte(r.Uint32()) + 42
		last := i == size-1 || col >= 127
		if o == 0 || o == '\n' || o == '\r' || o == '=' ||
			(o == '\t' || o == ' ') && (col == 0 || last) ||
			o == '.' && col == 0 {
			b.WriteByte('=')
			b.WriteByte(o + 64)
			col += 2
		} else {
			b.WriteByte(o)
			col++
		}
		if col >= 128 && i != size-1 {
			b.WriteString("\r\n")
			col = 0
		}
	}
	fmt.Fprintf(&b, "\r\n=yend size=%d\r\n.\r\n", size)
	return b.Bytes()
}

// CDecoderName names rapidyenc's C decoder and its kernel, or "" in builds
// without it.
func CDecoderName() string { return cBackendName() }
