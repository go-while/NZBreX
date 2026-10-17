package main

import (
	"bufio"
	"net"
	"net/textproto"
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
