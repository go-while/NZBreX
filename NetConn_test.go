package main

import (
	"bufio"
	"net"
	"net/textproto"
	"runtime"
	"strings"
	gotesting "testing" // Workers.go declares a const named testing
	"time"

	"github.com/go-while/go-loggedrwmutex"
)

// CMD_IHAVE used to return on a non-335 reply without srvtp.EndResponse, so
// the next command on the same connection blocked forever in StartResponse.
// Seen live: a server shutting down answers 436, the upload is retried on the
// same conn, and the session never ends.
func TestIHAVEReplyNot335KeepsConnUsable(t *gotesting.T) {
	testCfg()
	cli, srv := net.Pipe()
	defer cli.Close()
	defer srv.Close()

	go func() {
		r := bufio.NewReader(srv)
		for _, reply := range []string{"436 Retry later", "435 Not wanted"} {
			line, err := r.ReadString('\n')
			if err != nil || !strings.HasPrefix(line, "IHAVE ") {
				return
			}
			if _, err := srv.Write([]byte(reply + "\r\n")); err != nil {
				return
			}
		}
	}()

	prov := &Provider{Name: "fake", mux: &loggedrwmutex.LoggedSyncRWMutex{Name: "fake"}}
	connitem := &ConnItem{
		c:      &ConnPool{provider: prov},
		conn:   cli,
		srvtp:  textproto.NewConn(cli),
		writer: bufio.NewWriter(cli),
	}
	item := testItem("<a@b>")

	for _, want := range []int{436, 435} {
		done := make(chan int, 1)
		go func() {
			code, _, _, _ := CMD_IHAVE(connitem, item)
			done <- code
		}()
		select {
		case code := <-done:
			if code != want {
				t.Fatalf("CMD_IHAVE code=%d, want %d", code, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("CMD_IHAVE expecting %d blocked: previous response was never ended", want)
		}
	}
}

// Plan 3.7: with -yenctest=3/4 an early return from readDotLines (here: the server
// closes the conn mid-article) left the async decoder goroutine blocked forever,
// and with 4 the deferred ReleaseDecoder handed the decoder back while it was still in use.
func TestReadDotLinesEarlyReturnStopsDecoder(t *gotesting.T) {
	testCfg()
	old := *cfg.opt
	defer func() { *cfg.opt = old }()
	cfg.opt.YencCRC, cfg.opt.MaxArtSize, cfg.opt.RapidYencBufSize = true, 1<<20, 4096
	if async_core_chan == nil { // LaunchSession sets it up
		async_core_chan = make(chan struct{}, 1)
		async_core_chan <- struct{}{}
	}

	for _, mode := range []int{3, 4} {
		cfg.opt.YencTest = mode
		cli, srv := net.Pipe()
		go func() {
			srv.Write([]byte("Subject: x\r\n\r\n=ybegin part=1 total=1 line=128 size=3 name=x\r\n=ypart begin=1 end=3\r\n"))
			srv.Write([]byte("abc\r\n"))
			srv.Close() // no "=yend", no final dot: ReadLine fails
		}()
		prov := &Provider{Name: "fake", mux: &loggedrwmutex.LoggedSyncRWMutex{Name: "fake"}}
		connitem := &ConnItem{c: &ConnPool{provider: prov}, conn: cli, srvtp: textproto.NewConn(cli), writer: bufio.NewWriter(cli)}

		done := make(chan error, 1)
		go func() {
			_, _, _, err := readDotLines(connitem, testItem("<a@b>"), cmdARTICLE)
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Fatalf("yenctest=%d: want a read error", mode)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("yenctest=%d: readDotLines blocked", mode)
		}
		cli.Close()

		// the fix waits for the decoder goroutine before returning; allow a little slack anyway
		deadline := time.Now().Add(time.Second)
		for {
			buf := make([]byte, 1<<20)
			stacks := string(buf[:runtime.Stack(buf, true)])
			if !strings.Contains(stacks, ".readDotLines.func") { // package path differs under go test
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("yenctest=%d: async decoder goroutine still running after readDotLines returned", mode)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}
