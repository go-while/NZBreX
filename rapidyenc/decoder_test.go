package rapidyenc

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecode(t *testing.T) {
	space := make([]byte, 800000)
	for i := range space {
		space[i] = 0x20
	}

	cases := []struct {
		name string
		raw  string
		crc  uint32
	}{
		{"foobar", "foobar", 0x9EF61F95},
		{"0x20", string(space), 0x31f365e7},
		{"special", "\x04\x04\x04\x04", 0xca2ee18a},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(tc.raw)

			encoded := bytes.NewReader(body(raw))

			dec := AcquireDecoder()
			dec.SetReader(encoded)
			b := bytes.NewBuffer(nil)
			n, err := io.Copy(b, dec)
			require.Equal(t, int64(len(raw)), n)
			require.NoError(t, err)
			require.Equal(t, raw, b.Bytes())
			require.Equal(t, tc.crc, dec.Meta().Hash)
			require.Equal(t, int64(len(raw)), dec.Meta().End)
			ReleaseDecoder(dec)
		})
	}
}

// TestSplitReads tests "=yend" split across reads
func TestSplitReads(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		crc  uint32
	}{
		{"foobar", "foobar", 0x9EF61F95},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(tc.raw)

			enc := NewEncoder()
			encoded := enc.Encode(raw)

			readers := make([]io.Reader, 0)
			readers = append(readers, strings.NewReader(fmt.Sprintf("=ybegin part=%d line=128 size=%d name=%s\r\n", 1, len(raw), "foo")))
			readers = append(readers, strings.NewReader(fmt.Sprintf("=ypart begin=%d end=%d\r\n", 1, len(raw)+1)))
			readers = append(readers, bytes.NewReader(encoded))
			readers = append(readers, strings.NewReader("\r\n="))
			readers = append(readers, strings.NewReader(fmt.Sprintf("yend size=%d part=%d pcrc32=%08x\r\n", len(raw), 1, crc32.ChecksumIEEE(raw))))
			readers = append(readers, strings.NewReader(".\r\n"))

			reader := io.MultiReader(readers...)

			dec := AcquireDecoder()
			dec.SetReader(reader)
			b := bytes.NewBuffer(nil)
			n, err := io.Copy(b, dec)
			require.Equal(t, int64(len(raw)), n)
			require.NoError(t, err)
			require.Equal(t, raw, b.Bytes())
			require.Equal(t, tc.crc, dec.Meta().Hash)
			require.Equal(t, int64(len(raw)), dec.Meta().End)
			ReleaseDecoder(dec)
		})
	}
}

func BenchmarkSingle(b *testing.B) {
	raw := make([]byte, 1024*1024)
	_, err := rand.Read(raw)
	require.NoError(b, err)

	r := bytes.NewReader(body(raw))

	dec := AcquireDecoder()

	for i := 0; i < b.N; i++ {
		dec.SetReader(r)
		io.Copy(io.Discard, dec)
		r.Seek(0, io.SeekStart)
		dec.Reset()
	}

	ReleaseDecoder(dec)
}

func body(raw []byte) []byte {
	var b bytes.Buffer
	w := bufio.NewWriter(&b)
	enc := NewEncoder()
	encoded := enc.Encode(raw)

	fmt.Fprintf(w, "=ybegin part=%d line=128 size=%d name=%s\r\n", 1, len(raw), "foo")
	fmt.Fprintf(w, "=ypart begin=%d end=%d\r\n", 1, len(raw)+1)
	w.Write(encoded)
	w.Write([]byte("\r\n"))
	fmt.Fprintf(w, "=yend size=%d part=%d pcrc32=%08x\r\n", len(raw), 1, crc32.ChecksumIEEE(raw))
	fmt.Fprintf(w, ".\r\n")

	if err := w.Flush(); err != nil {
		panic(err)
	}

	return b.Bytes()
}

func TestExtractString(t *testing.T) {
	cases := []struct {
		raw      string
		expected string
	}{
		{"", ""},
		{"foo", "foo"},
		{"name=bar", "name=bar"},
		{"foo bar", "foo bar"},
		{"before\x00after", "before"},
	}

	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			b := []byte(fmt.Sprintf("=ybegin part=1 line=128 size=128 name=%s\r\n", tc.raw))
			i, err := extractString(b, []byte(" name="))
			require.NoError(t, err)
			require.Equal(t, tc.expected, i)
		})
	}
}

func TestExtractCRC(t *testing.T) {
	cases := []struct {
		raw      string
		expected uint32
	}{
		{"ffffffffa95d3e50", 0xa95d3e50},
		{"fffffffa95d3e50", 0xa95d3e50},
		{"ffffffa95d3e50", 0xa95d3e50},
		{"fffffa95d3e50", 0xa95d3e50},
		{"ffffa95d3e50", 0xa95d3e50},
		{"fffa95d3e50", 0xa95d3e50},
		{"ffa95d3e50", 0xa95d3e50},
		{"fa95d3e50", 0xa95d3e50},
		{"a95d3e50", 0xa95d3e50},
		{"a95d3e5", 0xa95d3e5},
		{"a95d3e", 0xa95d3e},
		{"a95d3", 0xa95d3},
		{"a95d", 0xa95d},
		{"a95", 0xa95},
		{"a9", 0xa9},
		{"a", 0xa},
		{"", 0},
		{"12345678 ", 0x12345678}, // space at end
	}

	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			b := []byte(fmt.Sprintf("pcrc32=%s", tc.raw))
			i, err := extractCRC(b, []byte("pcrc32="))
			require.NoError(t, err)
			require.Equal(t, tc.expected, i)
		})
	}
}
