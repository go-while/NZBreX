package main

import (
	"errors"
	"io"
	"net"
	"net/textproto"
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

func testPool(t *gotesting.T, addr string) *ConnPool {
	t.Helper()
	testCfg()
	if GCounter == nil {
		GCounter = NewCounter(10)
	}
	prov := &Provider{Name: "p", TCPMode: "tcp", MaxConns: 1, mux: &loggedrwmutex.LoggedSyncRWMutex{Name: "p"}}
	c := &ConnPool{provider: prov, rserver: addr, pool: make(chan *ConnItem, 1),
		mux: &loggedrwmutex.LoggedSyncRWMutex{Name: "pool-p"}}
	prov.ConnPool = c
	return c
}

// serve accepts one conn, writes greeting (if any), then keeps it open and silent.
func serve(t *gotesting.T, greeting string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		t.Cleanup(func() { conn.Close() })
		if greeting != "" {
			io.WriteString(conn, greeting)
		}
		io.Copy(io.Discard, conn)
	}()
	return ln.Addr().String()
}

func shortHandshake(t *gotesting.T) {
	old := connHandshakeTimeout
	connHandshakeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { connHandshakeTimeout = old })
}

// Plan 3.14: the greeting was read with no deadline (UseNoDeadline), so a server
// that accepts and stays silent blocked connect, and GetConn, forever. It now
// times out and counts as a failed dial, so GetConn can mark the provider down.
func TestConnectSilentServerTimesOut(t *gotesting.T) {
	shortHandshake(t)
	c := testPool(t, serve(t, ""))
	type res struct {
		item *ConnItem
		err  error
	}
	done := make(chan res, 1)
	go func() { it, err := c.connect(); done <- res{it, err} }()
	select {
	case r := <-done:
		if r.item != nil || !errors.Is(r.err, errDial) {
			t.Fatalf("want no conn and an errDial error, got item=%v err=%v", r.item, r.err)
		}
	case <-time.After(5 * time.Second): // connect sleeps up to 1s before dialing
		t.Fatal("connect blocked on a silent server")
	}
}

// Plan 3.14: the greeting was read with ReadCodeLine(200), so "201 no posting"
// came back as an error and the conn was never used (and leaked).
func TestConnectAccepts201Greeting(t *gotesting.T) {
	shortHandshake(t)
	c := testPool(t, serve(t, "201 no posting\r\n"))
	it, err := c.connect()
	if err != nil || it == nil {
		t.Fatalf("201 greeting: want a conn and no error, got item=%v err=%v", it, err)
	}
	it.conn.Close()
}

// Plan 3.14: checkCapabilities held provider.mux across two reads with no
// deadline. A server that never answers CAPABILITIES held the lock forever.
func TestCheckCapabilitiesTimesOutWithoutHoldingLock(t *gotesting.T) {
	shortHandshake(t)
	c := testPool(t, "")
	c.openConns = 1
	a, b := net.Pipe()
	defer b.Close()
	go io.Copy(io.Discard, b) // reads the command, never answers
	item := &ConnItem{c: c, conn: a, srvtp: textproto.NewConn(a)}

	done := make(chan error, 1)
	go func() { done <- checkCapabilities(c.provider, item) }()

	time.Sleep(100 * time.Millisecond)
	locked := make(chan struct{})
	go func() { c.provider.mux.Lock(); c.provider.mux.Unlock(); close(locked) }()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("provider.mux is held while waiting for the server")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want an error from a server that never answers")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("checkCapabilities blocked on a silent server")
	}
}
