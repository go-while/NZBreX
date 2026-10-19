package rapidyenc

import (
	"bytes"
	"math/rand/v2"
	"testing"
	"time"
)

// The -benchmark input decodes back to yencbench's random128 data (PCG seed 7),
// and the measurement returns a speed.
func TestBenchDecodeIncremental(t *testing.T) {
	const size = 100_000
	r := rand.New(rand.NewPCG(7, 8))
	want := make([]byte, size)
	for i := range want {
		want[i] = byte(r.Uint32())
	}
	src := benchBody(size)
	dst := make([]byte, len(src))
	st := StateCRLF
	nd, _, end, err := DecodeIncremental(dst, src, &st)
	if err != nil || end != EndControl || !bytes.Equal(dst[:nd], want) {
		t.Fatalf("decoded %d of %d bytes, end %d, err %v, equal %v", nd, size, end, err, bytes.Equal(dst[:nd], want))
	}
	if mbps, err := BenchDecodeIncremental(10*time.Millisecond, size); err != nil || mbps <= 0 {
		t.Fatalf("BenchDecodeIncremental: %v MB/s, %v", mbps, err)
	}
}
