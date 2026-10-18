package main

import (
	"bufio"
	"net"
	"net/textproto"
	"strings"
	gotesting "testing" // Workers.go declares a const named testing
	"time"
)

// Plan 5.1: textproto's ReadLine has no limit; the old "> 128" check came after
// the whole line was in memory, and 128 was too short for long message-IDs.
func TestReadCommandLineLimit(t *gotesting.T) {
	long := strings.Repeat("x", 600)
	ok := "ARTICLE <" + strings.Repeat("a", 250) + "@x>" // 263 bytes: fine
	br := bufio.NewReaderSize(strings.NewReader(long+"\r\n"+ok+"\r\n"), 64)
	if _, err := readCommandLine(br); err != errLineTooLong {
		t.Fatalf("600-byte line: err=%v, want errLineTooLong", err)
	}
	if line, err := readCommandLine(br); err != nil || line != ok {
		t.Fatalf("line after the long one: %q err=%v", line, err)
	}
}

func TestReadDotLinesLimited(t *gotesting.T) {
	tp := func(s string) *bufio.Reader { return bufio.NewReader(strings.NewReader(s)) }
	r := newTextprotoReader(tp("a\r\n..b\r\n.\r\n"))
	lines, err := readDotLinesLimited(r, 100)
	if err != nil || len(lines) != 2 || lines[1] != ".b" {
		t.Fatalf("lines=%q err=%v", lines, err)
	}
	r = newTextprotoReader(tp(strings.Repeat("0123456789\r\n", 20) + ".\r\n"))
	if _, err := readDotLinesLimited(r, 100); err != errReplyTooLarge {
		t.Fatalf("200-byte block with a 100-byte cap: err=%v", err)
	}
}

// Plan 5.1: the negative cache only shrank on lookups, and a numeric miss
// (keyed by the bare number) hid that number in every group.
func TestNegCacheCapPurgeAndGroupKeys(t *gotesting.T) {
	old := articleNotFound
	articleNotFound = &ArticleNotFound{Map: make(map[string]map[string]time.Time)}
	defer func() { articleNotFound = old }()

	SetArticleNotFoundAtProviderGroup("alt.a:5", "pg")
	if IsArticleNotFoundAtProviderGroup("alt.b:5", "pg") {
		t.Fatal("a miss of article 5 in alt.a hides article 5 in alt.b")
	}
	if !IsArticleNotFoundAtProviderGroup("alt.a:5", "pg") {
		t.Fatal("miss not cached")
	}
	for i := 0; i < negCacheMax+10; i++ {
		SetArticleNotFoundAtProviderGroup(strings.Repeat("k", i%7)+string(rune(i)), "pg2")
	}
	if articleNotFound.n > negCacheMax {
		t.Fatalf("cache holds %d entries, cap %d", articleNotFound.n, negCacheMax)
	}
	purgeNegCache(time.Now().Add(2 * negCacheTTL))
	if articleNotFound.n != 0 || len(articleNotFound.Map) != 0 {
		t.Fatalf("purge left %d entries", articleNotFound.n)
	}
}

// client runs handleConnection on one end of a pipe and returns the other.
func proxyClient(t *gotesting.T) (*bufio.Reader, net.Conn) {
	t.Helper()
	testCfg()
	cli, srv := net.Pipe()
	proxyPreAuthConns.Add(1) // as the accept loop does
	go handleConnection(srv)
	t.Cleanup(func() { cli.Close() })
	return bufio.NewReader(cli), cli
}

func expect(t *gotesting.T, r *bufio.Reader, prefix string) {
	t.Helper()
	line, err := r.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, prefix) {
		t.Fatalf("got %q err=%v, want a line starting with %q", line, err, prefix)
	}
}

func send(t *gotesting.T, c net.Conn, line string) {
	t.Helper()
	c.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte(line + "\r\n")); err != nil {
		t.Fatalf("write %q: %v", line, err)
	}
}

// Plan 5.2: syntax errors, unknown commands and long lines disconnected the
// client; RFC 3977 wants 500/501 and the connection kept.
func TestProxyErrorsKeepTheConnection(t *gotesting.T) {
	r, c := proxyClient(t)
	expect(t, r, "20")
	for _, step := range []struct{ cmd, want string }{
		{"MODE FOO", "501"},
		{strings.Repeat("X", 600), "501"},
		{"AUTHINFO USER", "501"},
		{"AUTHINFO PASS x", "482"},
		{"AUTHINFO USER someone001", "381"},
		{"AUTHINFO PASS pw junk", "501"},
		{"GROUP alt.test", "480"},
		{"MODE READER", "201"},
		{"CAPABILITIES", "101"},
	} {
		send(t, c, step.cmd)
		expect(t, r, step.want)
	}
	for { // rest of the capability list
		line, _ := r.ReadString('\n')
		if line == ".\r\n" {
			break
		}
	}
	send(t, c, "QUIT")
	expect(t, r, "205")
}

// Plan 5.1: a client could stay connected forever without logging in.
func TestProxyLoginTimeout(t *gotesting.T) {
	old := proxyLoginTimeout
	proxyLoginTimeout = 300 * time.Millisecond
	defer func() { proxyLoginTimeout = old }()
	r, _ := proxyClient(t)
	expect(t, r, "20")
	done := make(chan string, 1)
	go func() { line, _ := r.ReadString('\n'); done <- line }()
	select {
	case line := <-done:
		if !strings.HasPrefix(line, "400") {
			t.Fatalf("got %q, want 400 before the close", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no login timeout")
	}
}

func newTextprotoReader(br *bufio.Reader) *textproto.Reader { return textproto.NewReader(br) }
