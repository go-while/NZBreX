package main

import tst "testing"

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
