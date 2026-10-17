package main

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	gotesting "testing" // Workers.go declares a const named testing
	"time"

	"github.com/go-while/go-loggedrwmutex"
)

// lockWithin reports whether p.mux can be write-locked within d.
// A deadlocked mutex never becomes free, so the helper goroutine leaks
// in that case; that is acceptable in a failing test.
func lockWithin(p *PROCESSOR, d time.Duration, fn func()) bool {
	done := make(chan struct{})
	go func() {
		p.mux.Lock()
		fn()
		p.mux.Unlock()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// Plan 2.1: watchDirThread used to hold p.mux.RLock() while refreshDir()
// takes p.mux.Lock() (not reentrant), deadlocking on the first tick.
func TestWatchDirThreadPicksUpFilesWithoutDeadlock(t *gotesting.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.nzb"), []byte("<nzb/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &PROCESSOR{
		mux:       &loggedrwmutex.LoggedSyncRWMutex{Name: "TEST-PROCESSOR"},
		nzbDir:    dir,
		seenFiles: make(map[string]bool),
		sessMap:   make(map[uint64]*SESSION),
		stopChan:  make(chan struct{}, 1),
		refresh:   15 * time.Second,
	}
	go p.watchDirThread()

	// first lookup happens after 5 seconds
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var seen bool
		var sessions int
		if !lockWithin(p, 2*time.Second, func() { seen, sessions = p.seenFiles["a.nzb"], len(p.sessMap) }) {
			t.Fatal("PROCESSOR mutex not available: watchDirThread deadlocked")
		}
		if seen && sessions == 1 {
			p.stopChan <- struct{}{}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("a.nzb was not picked up within 10s")
}

// Plan 2.2: CAPABILITIES before AUTHINFO used to dereference a nil
// ps.CliTp and panic, killing the whole process.
func TestProxyCapabilitiesBeforeAuth(t *gotesting.T) {
	srv, cli := net.Pipe()
	defer cli.Close()
	go handleConnection(srv)

	cli.SetDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(cli)
	welcome, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("welcome: %v", err)
	}
	if !strings.HasPrefix(welcome, "20") {
		t.Fatalf("unexpected welcome %q", welcome)
	}
	if _, err := cli.Write([]byte("CAPABILITIES\r\n")); err != nil {
		t.Fatal(err)
	}
	first, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("CAPABILITIES response: %v", err)
	}
	if !strings.HasPrefix(first, "101 ") {
		t.Fatalf("want 101, got %q", first)
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("capability list not terminated: %v", err)
		}
		if line == ".\r\n" {
			break
		}
	}
	cli.Write([]byte("QUIT\r\n"))
}
