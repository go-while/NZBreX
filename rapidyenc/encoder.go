package rapidyenc

/*
#cgo CFLAGS: -I${SRCDIR}/src
#cgo darwin LDFLAGS: ${SRCDIR}/librapidyenc.a -lstdc++
#cgo windows,amd64 LDFLAGS: ${SRCDIR}/librapidyenc.a -lstdc++
#cgo windows,386   LDFLAGS: ${SRCDIR}/librapidyenc.a -lstdc++
#cgo windows,arm   LDFLAGS: ${SRCDIR}/librapidyenc.a -lstdc++
#cgo linux,amd64   LDFLAGS: ${SRCDIR}/librapidyenc.a -lstdc++
#cgo linux,386     LDFLAGS: ${SRCDIR}/librapidyenc.a -lstdc++
#cgo linux,arm     LDFLAGS: ${SRCDIR}/librapidyenc.a -lstdc++
#cgo linux,arm64   LDFLAGS: ${SRCDIR}/librapidyenc.a -lstdc++
#include "rapidyenc.h"
*/
import "C"
import (
	"bytes"
	"fmt"
	"os"
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

// UUEncode encodes src as UUencoded text with header/footer.
func UUEncode(src []byte, filename string, mode int) []byte {
	var buf bytes.Buffer
	// Write header
	fmt.Fprintf(&buf, "begin %03o %s\r\n", mode, filename)
	// Encode in 45-byte chunks
	for i := 0; i < len(src); i += 45 {
		chunk := src[i:]
		if len(chunk) > 45 {
			chunk = chunk[:45]
		}
		// Write length character
		buf.WriteByte(byte(len(chunk) + 32))
		// Encode chunk
		for j := 0; j < len(chunk); j += 3 {
			var a, b, c byte
			a = chunk[j]
			if j+1 < len(chunk) {
				b = chunk[j+1]
			}
			if j+2 < len(chunk) {
				c = chunk[j+2]
			}
			buf.WriteByte(((a >> 2) & 0x3F) + 32)
			buf.WriteByte((((a << 4) | ((b >> 4) & 0x0F)) & 0x3F) + 32)
			buf.WriteByte((((b << 2) | ((c >> 6) & 0x03)) & 0x3F) + 32)
			buf.WriteByte((c & 0x3F) + 32)
		}
		buf.WriteString("\r\n")
	}
	// Write footer
	buf.WriteString("end\r\n")
	return buf.Bytes()
}

// WriteUUEncodedFile encodes src and writes it as UUencoded data to the given file.
func WriteUUEncodedFile(filename string, src []byte, outPath string, mode int) error {
	encoded := UUEncode(src, filename, mode)
	return os.WriteFile(outPath, encoded, 0644)
}
