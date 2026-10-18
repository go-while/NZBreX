package main

import (
	"net"
	gotesting "testing" // Workers.go declares a const named testing
	"time"

	"github.com/go-while/go-loggedrwmutex"
)

// Plan 3.4: CloseConn dereferenced a nil item, and a second close of the same
// item (readDotLines closes on read errors, the caller closes again) decremented
// openConns twice. Negative drift made KillConnPool stop waiting early and let
// GetConn dial past MaxConns. Each call must still pass the shared-conn token on.
func TestCloseConnNilSafeAndDecrementsOnce(t *gotesting.T) {
	testCfg()
	prov := &Provider{Name: "p", MaxConns: 2, OpenConns: 1, mux: &loggedrwmutex.LoggedSyncRWMutex{Name: "p"}}
	c := &ConnPool{provider: prov, openConns: 1, pool: make(chan *ConnItem, 2),
		mux: &loggedrwmutex.LoggedSyncRWMutex{Name: "pool-p"}}
	a, b := net.Pipe()
	defer b.Close()
	item := &ConnItem{c: c, conn: a}
	sharedCC := make(chan *ConnItem, 1)

	token := func(what string) {
		t.Helper()
		select {
		case got := <-sharedCC:
			if got != nil {
				t.Fatalf("%s: want a nil token, got %#v", what, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s: no token sent to sharedCC", what)
		}
	}

	c.CloseConn(item, sharedCC)
	token("first close")
	c.CloseConn(item, sharedCC) // same item again
	token("second close")
	c.CloseConn(nil, sharedCC) // nil item
	token("nil item")
	c.CloseConn(nil, nil) // no panic, nothing to send

	if open, _ := c.GetStats(); open != 0 {
		t.Fatalf("openConns: want 0 after closing one conn, got %d", open)
	}
	time.Sleep(50 * time.Millisecond) // DecrementOpenConns runs in a goroutine
	prov.mux.RLock()
	defer prov.mux.RUnlock()
	if prov.OpenConns != 0 {
		t.Fatalf("provider.OpenConns: want 0, got %d", prov.OpenConns)
	}
}
