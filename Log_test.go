package main

import (
	"log"
	"os"
	"path/filepath"
	tst "testing"
)

// Switching the log file closes the one before (plan 6.2: each -log session leaked one).
func TestLogToFileClosesThePreviousFile(t *tst.T) {
	defer SetLogToTerminal()
	dir := t.TempDir()
	if err := LogToFile(filepath.Join(dir, "a.log"), false); err != nil {
		t.Fatal(err)
	}
	first := logFile
	if err := LogToFile(filepath.Join(dir, "b.log"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Stat(); err == nil {
		t.Fatal("a.log is still open after switching to b.log")
	}
	log.Print("into b")
	second := logFile
	SetLogToTerminal()
	if _, err := second.Stat(); err == nil || logFile != nil {
		t.Fatal("b.log is still open after SetLogToTerminal")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "b.log")); len(b) == 0 {
		t.Fatal("b.log is empty")
	}
}
