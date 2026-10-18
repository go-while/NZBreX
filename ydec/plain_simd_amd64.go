//go:build goexperiment.simd && amd64

package ydec

import (
	"math/bits"
	"simd/archsimd"
	"unsafe"
)

var (
	hasVBMI2 = archsimd.X86.AVX512VBMI2() // VPCOMPRESSB: packs 64 bytes in one instruction
	hasAVX2  = archsimd.X86.AVX2()
	hasAVX   = archsimd.X86.AVX()
)

// Kernel reports which fast path the decoder uses.
func Kernel() string {
	switch {
	case hasVBMI2:
		return "avx512vbmi2"
	case hasAVX2:
		return "avx2"
	case hasAVX:
		return "avx"
	}
	return "scalar"
}

// lutLo[m] / lutHi[m] are VPSHUFB indices that pack the bytes kept by mask m
// (bit set = keep) of the low / high 8 bytes of a vector to the front.
var lutLo, lutHi [256][16]int8

// count[m] is the number of bits set in m. A table, not bits.OnesCount: its
// fallback call (for CPUs without POPCNT) made the compiler reload every
// vector constant in the loop.
var count [256]uint8

func init() {
	for m := range 256 {
		count[m] = uint8(bits.OnesCount8(uint8(m)))
		k := 0
		for b := range 8 {
			if m&(1<<b) != 0 {
				lutLo[m][k] = int8(b)
				lutHi[m][k] = int8(b + 8)
				k++
			}
		}
		for ; k < 16; k++ {
			lutLo[m][k] = -1
			lutHi[m][k] = -1
		}
	}
}

func decode(dst, src []byte, state *State) (int, int, End) {
	switch {
	case hasVBMI2:
		return decodeVBMI2(dst, src, state)
	case hasAVX:
		return decodeSIMD(dst, src, state)
	}
	return decodeScalar(dst, src, state)
}

// decodeSIMD is the state machine with a vector loop. Once in StateNone it
// decodes 16 bytes per step, without branching on the data: plain bytes,
// escapes ('=' + byte), line breaks and stuffed dots ("\r\n." drops the dot).
// Each step works out escapes and line starts from loads one and two bytes
// back, so a block may start or end anywhere, even inside "\r\n" or after '='.
// A block with anything the vector code doesn't do ends the loop: "==" (which
// '=' escapes which?), an escaped \r or \n (invalid yEnc; rapidyenc has quirks
// for it) and the end sequences "\r\n=y", "\r\n.\r", "\r\n.=". The state
// machine takes it from there, one byte at a time.
//
// Stores write whole vectors at dst[p:]; the bytes past the output are
// overwritten later. That needs len(dst) >= len(src) and p <= i; the loop stops
// 24 bytes before the end of src (p+8+16 <= i+24, and the load at i+1).
//
// Everything is in one function: the compiler doesn't inline helpers of this
// size, and a call per block or per special byte halved the speed.
func decodeSIMD(dst, src []byte, state *State) (int, int, End) {
	eq32 := archsimd.BroadcastUint8x32('=')
	cr32 := archsimd.BroadcastUint8x32('\r')
	lf32 := archsimd.BroadcastUint8x32('\n')
	dot32 := archsimd.BroadcastUint8x32('.')
	ky32 := archsimd.BroadcastUint8x32('y')
	k42x32 := archsimd.BroadcastUint8x32(42)
	k64x32 := archsimd.BroadcastUint8x32(64)

	st := *state
	p, i, n := 0, 0, len(src)
	for i < n {
		// The vector loop needs the bytes before i to mean what they say: in
		// StateNone src[i-1] can't be an unescaped '\r' or end "\r\n", but it
		// can be an escaped '='.
		if st == StateNone && i >= 2 && i+24 <= n && src[i-1] != '=' {
			start := i
			// unchecked loads and stores: i-2 >= 0 and i+1+16 <= i+24 <= n,
			// and p+8+16 <= i+24 <= len(dst) (see above)
			sp := unsafe.Pointer(unsafe.SliceData(src))
			dp := unsafe.Pointer(unsafe.SliceData(dst))
			load := func(off int) archsimd.Uint8x16 {
				return archsimd.LoadUint8x16Array((*[16]uint8)(unsafe.Add(sp, off)))
			}
			load32 := func(off int) archsimd.Uint8x32 {
				return archsimd.LoadUint8x32Array((*[32]uint8)(unsafe.Add(sp, off)))
			}
			for i+24 <= n {
				if hasAVX2 && i+40 <= n {
					// the same as the 16-byte step below, 32 bytes at a time
					// (stores: p+24+16 <= i+40 <= n)
					v := load32(i)
					b1 := load32(i - 1)
					b2 := load32(i - 2)
					a1 := load32(i + 1)
					isEq, isDot := v.Equal(eq32), v.Equal(dot32)
					esc := b1.Equal(eq32)
					line := b1.Equal(lf32).And(b2.Equal(cr32))
					ctl := isEq.Or(v.Equal(cr32)).Or(v.Equal(lf32))
					bad := esc.And(ctl).
						Or(line.And(isEq).And(a1.Equal(ky32))).
						Or(line.And(isDot).And(a1.Equal(cr32).Or(a1.Equal(eq32))))
					if bad.ToBits() == 0 {
						keep := ^ctl.Or(line.And(isDot)).ToBits()
						out := v.Sub(k42x32).Sub(k64x32.Masked(esc))
						lo, hi := out.GetLo(), out.GetHi()
						m0, m1, m2, m3 := keep&0xff, keep>>8&0xff, keep>>16&0xff, keep>>24
						lo.PermuteOrZero(archsimd.LoadInt8x16Array(&lutLo[m0])).StoreArray((*[16]uint8)(unsafe.Add(dp, p)))
						p += int(count[m0])
						lo.PermuteOrZero(archsimd.LoadInt8x16Array(&lutHi[m1])).StoreArray((*[16]uint8)(unsafe.Add(dp, p)))
						p += int(count[m1])
						hi.PermuteOrZero(archsimd.LoadInt8x16Array(&lutLo[m2])).StoreArray((*[16]uint8)(unsafe.Add(dp, p)))
						p += int(count[m2])
						hi.PermuteOrZero(archsimd.LoadInt8x16Array(&lutHi[m3])).StoreArray((*[16]uint8)(unsafe.Add(dp, p)))
						p += int(count[m3])
						i += 32
						continue
					}
					// a bad byte in here: the 16-byte step finds out which half
				}
				// the 16-byte constants are the low halves (no extra registers)
				eq, cr, lf, dot, ky := eq32.GetLo(), cr32.GetLo(), lf32.GetLo(), dot32.GetLo(), ky32.GetLo()
				k42, k64 := k42x32.GetLo(), k64x32.GetLo()
				v := load(i)
				b1 := load(i - 1) // the byte before each byte
				b2 := load(i - 2) // and the one before that
				a1 := load(i + 1) // the byte after
				isEq, isCR, isLF := v.Equal(eq), v.Equal(cr), v.Equal(lf)
				isDot := v.Equal(dot)
				esc := b1.Equal(eq)                    // escaped by the byte before
				line := b1.Equal(lf).And(b2.Equal(cr)) // right after "\r\n"
				ctl := isEq.Or(isCR).Or(isLF)          // '=', '\r', '\n' are never output
				// "==" (the second '=' would be escaped), an escaped \r or \n,
				// "\r\n=y", "\r\n.\r" and "\r\n.="
				bad := esc.And(ctl).
					Or(line.And(isEq).And(a1.Equal(ky))).
					Or(line.And(isDot).And(a1.Equal(cr).Or(a1.Equal(eq))))
				if bad.ToBits() != 0 {
					break
				}
				keep := ^uint32(ctl.Or(line.And(isDot)).ToBits()) & 0xffff
				// escaped bytes lose 42+64, the others 42
				out := v.Sub(k42).Sub(k64.Masked(esc))
				out.PermuteOrZero(archsimd.LoadInt8x16Array(&lutLo[keep&0xff])).StoreArray((*[16]uint8)(unsafe.Add(dp, p)))
				p += int(count[keep&0xff])
				out.PermuteOrZero(archsimd.LoadInt8x16Array(&lutHi[keep>>8])).StoreArray((*[16]uint8)(unsafe.Add(dp, p)))
				p += int(count[keep>>8])
				i += 16
			}
			if i != start {
				st = stateAfter(src, i)
				continue
			}
		}
		// one byte through the state machine
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

// decodeVBMI2 is decodeSIMD for CPUs with AVX-512 VBMI2: 64 bytes per step,
// packed by VPCOMPRESSB in one instruction instead of shuffle tables. It is a
// separate function because a function that mentions 512-bit vectors at all
// uses 512-bit instructions (to zero them) on entry.
func decodeVBMI2(dst, src []byte, state *State) (int, int, End) {
	eq := archsimd.BroadcastUint8x64('=')
	cr := archsimd.BroadcastUint8x64('\r')
	lf := archsimd.BroadcastUint8x64('\n')
	dot := archsimd.BroadcastUint8x64('.')
	ky := archsimd.BroadcastUint8x64('y')
	k42 := archsimd.BroadcastUint8x64(42)
	k64 := archsimd.BroadcastUint8x64(64)

	st := *state
	p, i, n := 0, 0, len(src)
	for i < n {
		if st == StateNone && i >= 2 && i+72 <= n && src[i-1] != '=' {
			start := i
			// unchecked: loads from i-2 to i+65, the store up to p+64 <= i+64
			sp := unsafe.Pointer(unsafe.SliceData(src))
			dp := unsafe.Pointer(unsafe.SliceData(dst))
			load := func(off int) archsimd.Uint8x64 {
				return archsimd.LoadUint8x64Array((*[64]uint8)(unsafe.Add(sp, off)))
			}
			for i+72 <= n {
				v := load(i)
				b1 := load(i - 1)
				b2 := load(i - 2)
				a1 := load(i + 1)
				isEq, isDot := v.Equal(eq), v.Equal(dot)
				esc := b1.Equal(eq)
				line := b1.Equal(lf).And(b2.Equal(cr))
				ctl := isEq.Or(v.Equal(cr)).Or(v.Equal(lf))
				bad := esc.And(ctl).
					Or(line.And(isEq).And(a1.Equal(ky))).
					Or(line.And(isDot).And(a1.Equal(cr).Or(a1.Equal(eq))))
				if bad.ToBits() != 0 {
					break
				}
				keep := ^ctl.Or(line.And(isDot)).ToBits()
				out := v.Sub(k42).Sub(k64.Masked(esc))
				out.Compress(archsimd.Mask8x64FromBits(keep)).StoreArray((*[64]uint8)(unsafe.Add(dp, p)))
				// byte table, not bits.OnesCount64 (see count)
				p += int(count[keep&0xff]) + int(count[keep>>8&0xff]) + int(count[keep>>16&0xff]) + int(count[keep>>24&0xff]) +
					int(count[keep>>32&0xff]) + int(count[keep>>40&0xff]) + int(count[keep>>48&0xff]) + int(count[keep>>56])
				i += 64
			}
			if i+72 > n {
				// the end of src: 16 bytes at a time instead of the byte loop
				// (with 4 KiB reads that was ~15% of the time)
				p, i = steps16(dst, src, p, i)
			}
			if i != start {
				st = stateAfter(src, i)
				continue
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

// steps16 runs the 16-byte vector step (the one in decodeSIMD) from src[i]
// while at least 24 bytes are left and no block needs the state machine. The
// caller guarantees the state machine invariant at i (see decodeSIMD).
func steps16(dst, src []byte, p, i int) (int, int) {
	eq := archsimd.BroadcastUint8x16('=')
	cr := archsimd.BroadcastUint8x16('\r')
	lf := archsimd.BroadcastUint8x16('\n')
	dot := archsimd.BroadcastUint8x16('.')
	ky := archsimd.BroadcastUint8x16('y')
	k42 := archsimd.BroadcastUint8x16(42)
	k64 := archsimd.BroadcastUint8x16(64)
	sp := unsafe.Pointer(unsafe.SliceData(src))
	dp := unsafe.Pointer(unsafe.SliceData(dst))
	load := func(off int) archsimd.Uint8x16 {
		return archsimd.LoadUint8x16Array((*[16]uint8)(unsafe.Add(sp, off)))
	}
	for i+24 <= len(src) {
		v := load(i)
		b1 := load(i - 1)
		b2 := load(i - 2)
		a1 := load(i + 1)
		isEq, isDot := v.Equal(eq), v.Equal(dot)
		esc := b1.Equal(eq)
		line := b1.Equal(lf).And(b2.Equal(cr))
		ctl := isEq.Or(v.Equal(cr)).Or(v.Equal(lf))
		bad := esc.And(ctl).
			Or(line.And(isEq).And(a1.Equal(ky))).
			Or(line.And(isDot).And(a1.Equal(cr).Or(a1.Equal(eq))))
		if bad.ToBits() != 0 {
			break
		}
		keep := ^uint32(ctl.Or(line.And(isDot)).ToBits()) & 0xffff
		out := v.Sub(k42).Sub(k64.Masked(esc))
		out.PermuteOrZero(archsimd.LoadInt8x16Array(&lutLo[keep&0xff])).StoreArray((*[16]uint8)(unsafe.Add(dp, p)))
		p += int(count[keep&0xff])
		out.PermuteOrZero(archsimd.LoadInt8x16Array(&lutHi[keep>>8])).StoreArray((*[16]uint8)(unsafe.Add(dp, p)))
		p += int(count[keep>>8])
		i += 16
	}
	return p, i
}

// stateAfter is the state after the vector loop decoded up to src[i]: it
// follows from the last bytes, since the loop never decoded "==", an escaped
// line break or an end sequence.
func stateAfter(src []byte, i int) State {
	at := func(k int) byte { // src[i-k], or 0 before the start
		if i-k < 0 {
			return 0
		}
		return src[i-k]
	}
	lineStart := func(k int) bool { return at(k+1) == '\n' && at(k+2) == '\r' } // src[i-k] starts a line
	switch at(1) {
	case '=':
		if lineStart(1) || at(2) == '.' && lineStart(2) {
			return StateCRLFEQ
		}
		return StateEQ
	case '\r':
		if at(2) == '.' && lineStart(2) {
			return StateCRLFDTCR
		}
		return StateCR
	case '\n':
		if at(2) == '\r' {
			return StateCRLF
		}
	case '.':
		if lineStart(1) {
			return StateCRLFDT
		}
	}
	return StateNone
}
