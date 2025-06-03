//go:build windows

package main

import "fmt"

// Windows does not support SIGUSR1 signal as linux does
// to get dump of running goroutines to file: goroutines.prof
// so in windows we do nothing here

// This is a placeholder function to maintain compatibility with other platforms.
// It is safe to call this function on Windows, but it will not perform any action.

// It is recommended to use the '-prof and/or -profweb' cmdline flag'
// to analyze CPU and memory profiles on Windows.

func setupSigusr1Dump() {
	if runProf || webProf != "" {
		fmt.Println("Pprof CPU/MEM profiling is enabled.")
		fmt.Printf("If started with -profweb open your browser to %s to view the profile in a web browser.\n", webProf)
	} else {
		fmt.Println("SIGUSR1 signal is not supported on Windows. Use -prof and/or -profweb cmdline flags to analyze CPU and memory profiles.")
	}
	return
}
