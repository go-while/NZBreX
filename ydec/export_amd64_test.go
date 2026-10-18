//go:build goexperiment.simd && amd64

package ydec

import "simd/archsimd"

func HasVBMI2() bool { return hasVBMI2 }
func HasAVX2() bool  { return hasAVX2 }

// SetVBMI2 turns the AVX-512 path off, or back on where the CPU has it, and
// returns the old setting.
func SetVBMI2(on bool) bool {
	old := hasVBMI2
	hasVBMI2 = on && archsimd.X86.AVX512VBMI2()
	return old
}
