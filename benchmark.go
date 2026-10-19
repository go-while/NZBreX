package main

import (
	"fmt"
	"io"
	"time"

	"github.com/go-while/NZBreX/rapidyenc"
	"github.com/go-while/go-yenc-simd/yencbench"
)

// benchSize is the article size of -benchmark, yencbench's default.
const benchSize = 750_000

// runBenchmark prints how fast this machine decodes yEnc (-benchmark):
// go-yenc-simd with every kernel the CPU can run and, in builds with
// -tags rapidyenc, rapidyenc's C decoder on the same input.
func runBenchmark(w io.Writer, per time.Duration) error {
	fmt.Fprintf(w, "%s %s: yEnc decoding speed, %v per value\n\n", appName, appVersion, per)
	if err := yencbench.Run(w, yencbench.Options{Time: per, Size: benchSize}); err != nil {
		return err
	}
	if rapidyenc.CBackend {
		mbps, err := rapidyenc.BenchDecodeIncremental(per, benchSize)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "\n%-30s %12.0f MB/s  %s\n", "C DecodeIncremental random128", mbps, rapidyenc.CDecoderName())
		fmt.Fprintf(w, "\nThis build decodes with rapidyenc's C code; -yencgo switches it to go-yenc-simd.\n")
	} else {
		fmt.Fprintf(w, "\nThis build decodes with go-yenc-simd (fastest kernel). Builds with -tags rapidyenc can also use rapidyenc's C code.\n")
	}
	return nil
}
