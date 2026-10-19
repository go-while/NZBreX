package main

import (
	"io"
	"sync"

	"github.com/go-while/NZBreX/rapidyenc"
)

// Mode 4 collects an article body in one buffer before decoding it. The
// buffers are pooled: one article is about 750 KB, and every download needs one.
var yencBufPool sync.Pool

// yencBufMax: bigger buffers (an unusually large article) are not kept.
const yencBufMax = 4 << 20

// getYencBuf returns an empty buffer for an article of about size bytes (the
// NZB's segment size; 0 if unknown).
func getYencBuf(size int) *[]byte {
	want := size + size/32 + 1024 // headers and line ends the NZB size may leave out
	if b, ok := yencBufPool.Get().(*[]byte); ok {
		if cap(*b) < want {
			*b = make([]byte, 0, want)
		}
		*b = (*b)[:0]
		return b
	}
	b := make([]byte, 0, max(want, 64<<10))
	return &b
}

func putYencBuf(b *[]byte) {
	if b == nil || cap(*b) > yencBufMax {
		return
	}
	yencBufPool.Put(b)
}

// readAllDecoded reads everything d decodes, straight into one slice (the
// output is never longer than the input, so sizeHint is enough). io.EOF is the
// normal end and not returned.
func readAllDecoded(d *rapidyenc.Decoder, sizeHint int) ([]byte, error) {
	out := make([]byte, 0, sizeHint)
	for {
		if len(out) == cap(out) {
			out = append(out, 0)[:len(out)] // grow
		}
		n, err := d.Read(out[len(out):cap(out)])
		out = out[:len(out)+n]
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}
