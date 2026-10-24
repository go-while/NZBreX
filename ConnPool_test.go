package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"os"
	"strings"
	"sync/atomic"
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

// serveScript accepts one conn, greets, then answers each command line with reply(line, conn).
func serveScript(t *gotesting.T, reply func(line string, w net.Conn)) string {
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
		io.WriteString(conn, "200 ready\r\n")
		br := bufio.NewReader(conn)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			reply(strings.TrimRight(line, "\r\n"), conn)
		}
	}()
	return ln.Addr().String()
}

func shortIdle(t *gotesting.T, d time.Duration) {
	old := connIdleTimeout
	connIdleTimeout = d
	t.Cleanup(func() { connIdleTimeout = old })
}

// A server that stops answering mid-session no longer blocks the caller forever.
func TestIdleTimeoutEndsSilentCommand(t *gotesting.T) {
	shortIdle(t, 300*time.Millisecond)
	addr := serveScript(t, func(string, net.Conn) {}) // never answers
	c := testPool(t, addr)
	ci, err := c.connect()
	if err != nil {
		t.Fatal(err)
	}
	defer ci.conn.Close()
	start := time.Now()
	id, err := ci.srvtp.Cmd("STAT <a@b>")
	if err == nil {
		ci.srvtp.StartResponse(id)
		_, _, err = ci.srvtp.ReadCodeLine(223)
		ci.srvtp.EndResponse(id)
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("STAT to a silent server: err=%v, want a deadline error", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v, want about the idle timeout", d)
	}
}

// The timeout is per read: a slow but steady article is not cut off.
// The idle timeout is 10x the gap between lines: with 300 ms it failed on a
// busy single-CPU VM (load 45) whenever one 100 ms sleep overran by 200 ms.
func TestIdleTimeoutKeepsSlowTransfer(t *gotesting.T) {
	shortIdle(t, time.Second)
	addr := serveScript(t, func(_ string, w net.Conn) {
		io.WriteString(w, "222 0 <a@b> body\r\n")
		for i := 0; i < 30; i++ { // 3 s in total, 3x the idle timeout
			time.Sleep(100 * time.Millisecond)
			fmt.Fprintf(w, "line %d\r\n", i)
		}
		io.WriteString(w, ".\r\n")
	})
	c := testPool(t, addr)
	ci, err := c.connect()
	if err != nil {
		t.Fatal(err)
	}
	defer ci.conn.Close()
	id, err := ci.srvtp.Cmd("BODY <a@b>")
	if err != nil {
		t.Fatal(err)
	}
	ci.srvtp.StartResponse(id)
	defer ci.srvtp.EndResponse(id)
	if _, _, err := ci.srvtp.ReadCodeLine(222); err != nil {
		t.Fatal(err)
	}
	lines, err := ci.srvtp.ReadDotLines()
	if err != nil || len(lines) != 30 {
		t.Fatalf("slow body: %d lines, err=%v; want 30 lines", len(lines), err)
	}
}

// connect sets up at most cap(dialSem) connections to a provider at once: the
// others wait for a handshake to finish (they used to start one by one, after a
// random pause of up to 50 ms x MaxConns each).
func TestConnectLimitsParallelDials(t *gotesting.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var cur, peak atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { conn.Close() })
			go func() {
				n := cur.Add(1)
				for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
				}
				time.Sleep(100 * time.Millisecond) // a slow handshake
				cur.Add(-1)                        // before the greeting: the client frees its slot only after reading it
				io.WriteString(conn, "200 test\r\n")
				io.Copy(io.Discard, conn)
			}()
		}
	}()
	c := testPool(t, ln.Addr().String())
	c.dialSem = make(chan struct{}, 2)

	start := time.Now()
	errs := make(chan error, 6)
	for range 6 {
		go func() {
			ci, err := c.connect()
			if err == nil {
				ci.conn.Close()
			}
			errs <- err
		}()
	}
	for range 6 {
		if err := <-errs; err != nil {
			t.Fatalf("connect: %v", err)
		}
	}
	if p := peak.Load(); p > 2 {
		t.Errorf("%d connections in setup at once, limit 2", p)
	}
	if took := time.Since(start); took < 250*time.Millisecond {
		t.Errorf("6 connects with 2 at a time took %v, want 3 rounds of ~100 ms", took)
	}
}
