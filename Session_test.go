package main

import (
	"bufio"
	"net"
	"strings"
	gotesting "testing" // Workers.go declares a const named testing
	"time"
)

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
