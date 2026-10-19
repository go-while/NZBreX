package main

import (
	"strings"
	tst "testing"
)

func TestSafeFileName(t *tst.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"debian.iso", "debian.iso", true},
		{"../../../escape-test.bin", "escape-test.bin", true},
		{`..\..\win.bin`, "win.bin", true},
		{"/etc/passwd", "passwd", true},
		{"dir/", "dir", true},
		{"..", "", false},
		{".", "", false},
		{"", "", false},
		{"/", "", false},
		{"a\x00b", "", false},
	} {
		got, ok := safeFileName(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("safeFileName(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestValidMessageID(t *tst.T) {
	long := "<" + strings.Repeat("a", 248) + ">"
	for _, c := range []struct {
		id string
		ok bool
	}{
		{"<a@b>", true},
		{"<part1of10.abc$def@news.example.org>", true},
		{"<a<b@c>", true}, // '<' is allowed inside
		{long, true},
		{"<" + strings.Repeat("a", 249) + ">", false}, // 251 bytes
		{"<>", false},
		{"a@b", false},
		{"<a@b", false},
		{"<a b@c>", false},
		{"<a@b>c>", false},
		{"<a\tb@c>", false},
		{"<a\x7fb@c>", false},
		{"<a\xc3\xa4@c>", false},
	} {
		if got := validMessageID(c.id); got != c.ok {
			t.Errorf("validMessageID(%q) = %v, want %v", c.id, got, c.ok)
		}
	}
}

// logQuote escapes what a client could use to fake log lines or send terminal escapes.
func TestLogQuote(t *tst.T) {
	for in, want := range map[string]string{
		"/jobs":             `"/jobs"`,
		"/x\nFAKE LOG LINE": `"/x\nFAKE LOG LINE"`,
		"a\r\nb":            `"a\r\nb"`,
		"name\x1b[31m.nzb":  `"name\x1b[31m.nzb"`,
		"ümlaut.nzb":        `"ümlaut.nzb"`,
	} {
		if got := logQuote(in); got != want || strings.ContainsAny(got, "\r\n\x1b") {
			t.Errorf("logQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
