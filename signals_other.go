//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

func setupSigusr1Dump() {
	go func() {
		// 'kill -SIGUSR1 $(pidof nzbrex)' to dump running goroutines
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGUSR1)
		<-sigs
		dumpGoroutines()
	}()
}
