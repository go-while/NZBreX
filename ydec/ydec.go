// Package ydec is a pure-Go yEnc decoder for raw NNTP article bodies, a drop-in
// for rapidyenc's rapidyenc_decode_incremental (plan Phase 8).
//
// It decodes "raw" data: NNTP dot-stuffing ("\r\n.." → "\r\n.") is undone, and
// it stops at the end of the yEnc data ("\r\n=y", also dot-stuffed as
// "\r\n.=y") or of the article ("\r\n.\r\n"). It can be fed in chunks: State
// carries what the previous chunk ended in.
//
// The byte-wise decoder is a small state machine that matches rapidyenc's
// scalar reference (decoder.cc, do_decode_end_scalar<isRaw=true>) state for
// state. Runs of plain bytes (anything but '=', '\r', '\n') take a fast path:
// SIMD with GOEXPERIMENT=simd on amd64 (AVX2/AVX), a plain Go loop otherwise.
package ydec

import "errors"

// State is what the bytes before the current position were. The values are
// those of rapidyenc's RapidYencDecoderState, so they can be passed through.
type State int

const (
	StateCRLF     State = iota // "\r\n": start of a line (the initial state)
	StateEQ                    // "=": the next byte is escaped
	StateCR                    // "\r"
	StateNone                  // inside a line
	StateCRLFDT                // "\r\n."
	StateCRLFDTCR              // "\r\n.\r"
	StateCRLFEQ                // "\r\n=" or "\r\n.="
)

// End reports whether, and how, decoding stopped early.
type End int

const (
	EndNone    End = iota // all of src was decoded
	EndControl            // "\r\n=y" found: nSrc points after the 'y'
	EndArticle            // "\r\n.\r\n" found: nSrc points after the '\n'
)

// ErrDstTooSmall is returned when dst is shorter than src.
var ErrDstTooSmall = errors.New("ydec: destination must be at least the length of source")

// special marks the bytes that end a run of plain bytes.
var special = [256]bool{'=': true, '\r': true, '\n': true}

// DecodeIncremental decodes src into dst, starting in *state, and sets *state
// to the state after the last byte it read. It stops after an end sequence (see
// End). dst must be at least len(src): the output is never longer than the input.
func DecodeIncremental(dst, src []byte, state *State) (nDst, nSrc int, end End, err error) {
	if len(dst) < len(src) {
		return 0, 0, EndNone, ErrDstTooSmall
	}
	nDst, nSrc, end = decode(dst, src, state)
	return nDst, nSrc, end, nil
}

// decodeScalar is the decoder without SIMD: the state machine, with a byte
// loop for runs of plain bytes.
func decodeScalar(dst, src []byte, state *State) (int, int, End) {
	st := *state
	p, i, n := 0, 0, len(src)
	for i < n {
		if st == StateNone {
			k := decodePlainScalar(dst[p:], src[i:])
			i += k
			p += k
			if i >= n {
				break
			}
		}
		var end End
		st, p, end = step(st, src[i], dst, p)
		i++
		if end != EndNone {
			*state = st
			return p, i, end
		}
	}
	*state = st
	return p, n, EndNone
}

// step runs byte c through the state machine in state st, writes its output
// (if any) at dst[p], and returns the new state and output position. It
// matches rapidyenc's scalar decoder (do_decode_end_scalar, raw) byte for byte.
func step(st State, c byte, dst []byte, p int) (State, int, End) {
	switch st {
	case StateCR:
		if c == '\n' {
			return StateCRLF, p, EndNone
		}
	case StateCRLF:
		switch c {
		case '.':
			return StateCRLFDT, p, EndNone
		case '=':
			return StateCRLFEQ, p, EndNone
		}
	case StateCRLFDT:
		switch c {
		case '\r':
			return StateCRLFDTCR, p, EndNone
		case '=':
			return StateCRLFEQ, p, EndNone
		}
		// "\r\n.X": the stuffed dot is dropped, X is an ordinary byte
	case StateCRLFDTCR:
		if c == '\n' {
			return StateCRLF, p, EndArticle
		}
	case StateCRLFEQ:
		if c == 'y' {
			return StateNone, p, EndControl
		}
		fallthrough // "\r\n=X" is an escaped X
	case StateEQ:
		dst[p] = c - 42 - 64
		if c == '\r' {
			return StateCR, p + 1, EndNone // an escaped \r still starts a line break
		}
		return StateNone, p + 1, EndNone
	}
	// an ordinary byte, in any state
	switch c {
	case '\r':
		return StateCR, p, EndNone
	case '\n':
		return StateNone, p, EndNone
	case '=':
		return StateEQ, p, EndNone
	}
	dst[p] = c - 42
	return StateNone, p + 1, EndNone
}

// decodePlainScalar decodes the leading plain bytes of src into dst and returns
// how many there were. len(dst) >= len(src).
func decodePlainScalar(dst, src []byte) int {
	dst = dst[:len(src)]
	for k, c := range src {
		if special[c] {
			return k
		}
		dst[k] = c - 42
	}
	return len(src)
}
