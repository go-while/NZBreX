package rapidyenc

/*
#cgo CFLAGS: -I${SRCDIR}/src
#cgo darwin LDFLAGS: ${SRCDIR}/librapidyenc.a -lstdc++
#cgo windows,amd64 LDFLAGS: ${SRCDIR}/librapidyenc.a -lstdc++
#cgo windows,386   LDFLAGS: ${SRCDIR}/librapidyenc.a -lstdc++
#cgo linux,amd64   LDFLAGS: ${SRCDIR}/librapidyenc.a -lstdc++
#cgo linux,386     LDFLAGS: ${SRCDIR}/librapidyenc.a -lstdc++
#cgo linux,arm64   LDFLAGS: ${SRCDIR}/librapidyenc.a -lstdc++
#include "rapidyenc.h"
*/
import "C"
import (
	"sync"
	"unsafe"
)

func MaxLength(length, lineLength int) int {
	return int(C.rapidyenc_encode_max_length(C.size_t(length), C.int(lineLength)))
}

type Encoder struct {
	LineLength int
}

func NewEncoder() *Encoder {
	return &Encoder{
		LineLength: 128,
	}
}

var encodeInitOnce sync.Once

func (e *Encoder) Encode(src []byte) []byte {
	encodeInitOnce.Do(func() {
		C.rapidyenc_encode_init()
	})

	dst := make([]byte, MaxLength(len(src), e.LineLength))

	length := C.rapidyenc_encode(
		unsafe.Pointer(&src[0]),
		unsafe.Pointer(&dst[0]),
		C.size_t(len(src)),
	)

	return dst[:length]
}
