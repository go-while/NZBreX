//go:build !(goexperiment.simd && amd64)

package ydec

// Kernel reports which fast path the decoder uses.
func Kernel() string { return "scalar" }

func decode(dst, src []byte, state *State) (int, int, End) { return decodeScalar(dst, src, state) }
