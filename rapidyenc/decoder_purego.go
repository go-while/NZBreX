//go:build !rapidyenc

package rapidyenc

import yencsimd "github.com/go-while/go-yenc-simd"

// The default build: no cgo, the yEnc data is decoded by go-yenc-simd.
// Build with -tags rapidyenc for rapidyenc's C decoder (decoder_c.go).

// CBackend reports whether this build has rapidyenc's C decoder.
const CBackend = false

func cBackendName() string { return "" }

// DecodeIncremental decodes yEnc encoded data incrementally, with
// go-yenc-simd. It stops decoding when a yEnc/NNTP end sequence is found.
func DecodeIncremental(dst, src []byte, state *State) (nDst, nSrc int, end End, err error) {
	if len(src) == 0 {
		return 0, 0, EndNone, nil
	}
	if len(dst) < len(src) {
		return 0, 0, 0, errDestinationTooSmall
	}
	st := yencsimd.State(*state)
	nDst, nSrc, e, err := yencsimd.DecodeIncremental(dst, src, &st)
	*state = State(st)
	return nDst, nSrc, End(e), err
}
