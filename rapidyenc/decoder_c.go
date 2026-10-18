//go:build rapidyenc

package rapidyenc

/*
#cgo CFLAGS: -I${SRCDIR}/src
#cgo darwin LDFLAGS: ${SRCDIR}/librapidyenc_darwin.a -lstdc++
#cgo windows,amd64 LDFLAGS: ${SRCDIR}/librapidyenc_windows_amd64.a -lstdc++ -static-libstdc++ -static-libgcc
#cgo linux,amd64 LDFLAGS: ${SRCDIR}/librapidyenc_linux_amd64.a -lstdc++
#cgo linux,arm64 LDFLAGS: ${SRCDIR}/librapidyenc_linux_arm64.a -lstdc++
#include "rapidyenc.h"
*/
import "C"
import (
	"fmt"
	"sync"
	"unsafe"
)

// The C decoder: only in builds with -tags rapidyenc (cgo and the
// librapidyenc_*.a for the target, see the build_rapidyenc_*.sh scripts).

// CBackend reports whether this build has rapidyenc's C decoder.
const CBackend = true

var decodeInitOnce sync.Once

func cBackendName() string {
	return fmt.Sprintf("rapidyenc C (kernel %#x)", DecodeKernel())
}

// cConstants returns the C values of the State and End constants, for the test
// that checks the Go values against them.
func cConstants() []int {
	return []int{
		C.RYDEC_STATE_CRLF, C.RYDEC_STATE_EQ, C.RYDEC_STATE_CR, C.RYDEC_STATE_NONE,
		C.RYDEC_STATE_CRLFDT, C.RYDEC_STATE_CRLFDTCR, C.RYDEC_STATE_CRLFEQ,
		C.RYDEC_END_NONE, C.RYDEC_END_CONTROL, C.RYDEC_END_ARTICLE,
	}
}

// DecodeKernel reports the decode kernel rapidyenc picked for this CPU
// (RYKERN_* in rapidyenc.h, e.g. 0x403 AVX2, 0x603 VBMI2).
func DecodeKernel() int {
	decodeInitOnce.Do(func() {
		C.rapidyenc_decode_init()
	})
	return int(C.rapidyenc_decode_kernel())
}

// DecodeIncremental decodes yEnc encoded data incrementally, with rapidyenc's
// C code. It stops decoding when a yEnc/NNTP end sequence is found.
func DecodeIncremental(dst, src []byte, state *State) (nDst, nSrc int, end End, err error) {
	decodeInitOnce.Do(func() {
		C.rapidyenc_decode_init()
	})

	if len(src) == 0 {
		return 0, 0, EndNone, nil
	}

	if len(dst) < len(src) {
		return 0, 0, 0, errDestinationTooSmall
	}

	srcPointer := uintptr(unsafe.Pointer(&src[0]))
	dstPointer := uintptr(unsafe.Pointer(&dst[0]))

	result := End(C.rapidyenc_decode_incremental(
		(*unsafe.Pointer)(unsafe.Pointer(&srcPointer)),
		(*unsafe.Pointer)(unsafe.Pointer(&dstPointer)),
		C.size_t(len(src)),
		(*C.RapidYencDecoderState)(unsafe.Pointer(state)),
	))

	nSrc = int(srcPointer - uintptr(unsafe.Pointer(&src[0])))
	nDst = int(dstPointer - uintptr(unsafe.Pointer(&dst[0])))

	return nDst, nSrc, result, nil
}
