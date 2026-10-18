//go:build goexperiment.simd && amd64 && cgo

package ydec_test

import (
	"testing"

	. "github.com/go-while/NZBreX/ydec"
)

// withoutVBMI2 runs f on the AVX2/AVX path, also on CPUs that have AVX-512
// VBMI2, so both vector paths get tested and measured there.
func withoutVBMI2(f func()) {
	saved := SetVBMI2(false)
	defer SetVBMI2(saved)
	f()
}

func TestDecodeAVX2PathMatchesRapidyenc(t *testing.T) {
	if !HasVBMI2() {
		t.Skip("the default tests already run the AVX2 path here")
	}
	withoutVBMI2(func() {
		TestDecodeMatchesRapidyencRandom(t)
		TestDecodeMatchesRapidyencChunked(t)
		TestDecodeArticles(t)
	})
}

func BenchmarkDecodeGoAVX2(b *testing.B) {
	if !HasAVX2() {
		b.Skip("no AVX2")
	}
	withoutVBMI2(func() { benchDecode(b, goDecode) })
}
