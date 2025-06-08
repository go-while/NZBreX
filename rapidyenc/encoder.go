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
	"fmt"
	"io"
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

// UUEncodeLine encodes up to 45 bytes as a single UUencoded line.
func UUEncodeLine(src []byte) []byte {
	n := len(src)
	if n > 45 {
		n = 45
	}
	out := make([]byte, 0, 62)
	// Length character
	out = append(out, byte((n&0x3F)+0x20))
	for i := 0; i < n; i += 3 {
		var b [3]byte
		remain := n - i
		copy(b[:], src[i:])
		c1 := ((b[0] >> 2) & 0x3F)
		c2 := (((b[0] << 4) | (b[1] >> 4)) & 0x3F)
		c3 := (((b[1] << 2) | (b[2] >> 6)) & 0x3F)
		c4 := (b[2] & 0x3F)
		// Only output as many chars as needed for the remaining bytes
		out = append(out, c1+0x20)
		if remain > 1 {
			out = append(out, c2+0x20)
		} else {
			break
		}
		if remain > 2 {
			out = append(out, c3+0x20)
			out = append(out, c4+0x20)
		} else {
			break
		}
	}
	out = append(out, '\n')
	return out
}

// UUEncode encodes data as a complete UUencoded block.
func UUEncode(src []byte, mode, filename string) []byte {
	if mode == "" {
		mode = "644"
	}
	if filename == "" {
		filename = "file.uue"
	}
	out := []byte(fmt.Sprintf("begin %s %s\n", mode, filename))
	for i := 0; i < len(src); i += 45 {
		line := UUEncodeLine(src[i:])
		out = append(out, line...)
	}
	out = append(out, byte('`'), '\n') // zero-length line
	out = append(out, []byte("end\n")...)
	return out
}

// UUEncodeFile encodes the contents of srcFile and writes to dstFile as UUencoded data.
func UUEncodeFile(srcFile, dstFile, mode, filename string) error {
	in, err := os.Open(srcFile)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dstFile)
	if err != nil {
		return err
	}
	defer out.Close()
	if filename == "" {
		filename = srcFile
	}
	_, err = fmt.Fprintf(out, "begin %s %s\n", mode, filename)
	if err != nil {
		return err
	}
	buf := make([]byte, 45)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			line := UUEncodeLine(buf[:n])
			if _, werr := out.Write(line); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	_, err = out.Write([]byte("`\nend\n"))
	return err
}
