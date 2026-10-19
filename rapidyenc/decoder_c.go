//go:build rapidyenc

package rapidyenc

/*
#cgo CFLAGS: -I${SRCDIR}/src
#cgo darwin LDFLAGS: ${SRCDIR}/librapidyenc_darwin.a -lstdc++
#cgo windows,amd64 LDFLAGS: ${SRCDIR}/librapidyenc_windows_amd64.a -lstdc++ -static-libstdc++ -static-libgcc
#cgo linux,amd64 LDFLAGS: ${SRCDIR}/librapidyenc_linux_amd64.a -lstdc++
#cgo linux,arm64 LDFLAGS: ${SRCDIR}/librapidyenc_linux_arm64.a -lstdc++
#include "rapidyenc.h"

// rapidyenc_decode_incremental with the pointer arithmetic done in C: src and
// dest are passed as Go pointers, so cgo keeps them in place for the call.
// (From mnightingale/rapidyenc ed81518: the Go side used to hand C the
// addresses as uintptr values, which the cgo pointer rules don't cover.)
static RapidYencDecoderEnd rapidyenc_decode_incremental_go(const void* src, void* dest, size_t src_length, size_t* n_src, size_t* n_dest, RapidYencDecoderState* state) {
	const void* in_ptr = src;
	void* out_ptr = dest;
	RapidYencDecoderEnd ended = rapidyenc_decode_incremental(&in_ptr, &out_ptr, src_length, state);
	*n_src = (uintptr_t)in_ptr - (uintptr_t)src;
	*n_dest = (uintptr_t)out_ptr - (uintptr_t)dest;
	return ended;
}
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

	var cnSrc, cnDst C.size_t
	result := End(C.rapidyenc_decode_incremental_go(
		unsafe.Pointer(&src[0]),
		unsafe.Pointer(&dst[0]),
		C.size_t(len(src)),
		&cnSrc,
		&cnDst,
		(*C.RapidYencDecoderState)(unsafe.Pointer(state)),
	))
	return int(cnDst), int(cnSrc), result, nil
}
