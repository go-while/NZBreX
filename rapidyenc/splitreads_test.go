package rapidyenc

import (
	"bytes"
	"hash/crc32"
	"io"
	"math/rand/v2"
	"testing"
	"testing/iotest"
)

// The decoder gets the article in pieces of every size, down to one byte, so
// every split point is hit: inside an escape, between \r and \n, between
// "\r\n." and the next byte, inside "=yend". Both decoders (C in the rapidyenc
// build, and go-yenc-simd).
func TestSplitReadsEverySize(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	raw := make([]byte, 200_000)
	for i := range raw {
		raw[i] = byte(r.Uint32())
	}
	art := body(raw)
	readers := map[string]func(io.Reader) io.Reader{
		"one byte": iotest.OneByteReader,
		"half":     iotest.HalfReader,
		"data+err": iotest.DataErrReader,
		"whole":    func(r io.Reader) io.Reader { return r },
	}
	decoders := []bool{true}
	if CBackend {
		decoders = append(decoders, false)
	}
	for _, goDec := range decoders {
		for name, wrap := range readers {
			UseGoDecoder = goDec
			dec := AcquireDecoderWithReader(wrap(bytes.NewReader(art)))
			got, err := io.ReadAll(dec)
			ReleaseDecoder(dec)
			if err != nil || !bytes.Equal(got, raw) || crc32.ChecksumIEEE(got) != crc32.ChecksumIEEE(raw) {
				t.Errorf("go=%t %s: %d of %d bytes, equal=%t, err=%v", goDec, name, len(got), len(raw), bytes.Equal(got, raw), err)
			}
		}
	}
	UseGoDecoder = false
}
