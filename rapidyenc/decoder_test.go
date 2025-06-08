package rapidyenc

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func runDecoderTest(t *testing.T, raw []byte, expectedCRC uint32) {
	t.Helper()
	dec := AcquireDecoder()
	defer ReleaseDecoder(dec)
	dec.SetReader(bytes.NewReader(body(raw)))
	b := bytes.NewBuffer(nil)
	n, err := io.Copy(b, dec)
	require.Equal(t, int64(len(raw)), n)
	require.NoError(t, err)
	require.Equal(t, raw, b.Bytes())
	require.Equal(t, expectedCRC, dec.Meta().Hash)
	require.Equal(t, int64(len(raw)), dec.Meta().End)
}

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
			t.Parallel()
			raw := []byte(tc.raw)
			runDecoderTest(t, raw, tc.crc)
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
			t.Parallel()
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
			defer ReleaseDecoder(dec)
			dec.SetReader(reader)
			b := bytes.NewBuffer(nil)
			n, err := io.Copy(b, dec)
			require.Equal(t, int64(len(raw)), n)
			require.NoError(t, err)
			require.Equal(t, raw, b.Bytes())
			require.Equal(t, tc.crc, dec.Meta().Hash)
			require.Equal(t, int64(len(raw)), dec.Meta().End)
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

func TestExtractIntError(t *testing.T) {
	_, err := extractInt([]byte("no size here"), []byte(" size="))
	require.Error(t, err)
}

func TestExtractStringError(t *testing.T) {
	_, err := extractString([]byte("no name here"), []byte(" name="))
	require.Error(t, err)
}

func TestExtractCRCError(t *testing.T) {
	_, err := extractCRC([]byte("no crc here"), []byte("pcrc32="))
	require.Error(t, err)
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

// TestDecodeMissingYbegin tests decoding data missing the "=ybegin" header
func TestDecodeMissingYbegin(t *testing.T) {
	data := []byte("=yend size=0 part=1 pcrc32=00000000\r\n.\r\n")
	dec := AcquireDecoder()
	defer ReleaseDecoder(dec)
	dec.SetReader(bytes.NewReader(data))
	buf := make([]byte, 10)
	_, err := dec.Read(buf)
	require.Error(t, err)
	require.Contains(t, err.Error(), "without finding \"=begin\" header")
}

// TestDecodeMissingYend tests decoding data missing the "=yend" trailer
func TestDecodeMissingYend(t *testing.T) {
	data := []byte("=ybegin part=1 line=128 size=5 name=foo\r\nabcde\r\n.\r\n")
	dec := AcquireDecoder()
	defer ReleaseDecoder(dec)
	dec.SetReader(bytes.NewReader(data))
	buf := make([]byte, 10)
	_, err := dec.Read(buf)
	require.Error(t, err)
	require.Contains(t, err.Error(), "without finding \"=yend\" trailer")
}

// TestDecodeCRCMismatch tests decoding data with a CRC mismatch
func TestDecodeCRCMismatch(t *testing.T) {
	// Use the encoder to generate a valid yEnc body for 3 bytes
	raw := []byte{1, 2, 3}
	enc := NewEncoder()
	encoded := enc.Encode(raw)
	data := []byte(fmt.Sprintf(
		"=ybegin part=1 line=128 size=3 name=foo\r\n=ypart begin=1 end=4\r\n%s\r\n=yend size=3 part=1 pcrc32=deadbeef\r\n.\r\n",
		string(encoded),
	))
	dec := AcquireDecoder()
	defer ReleaseDecoder(dec)
	dec.SetReader(bytes.NewReader(data))
	buf := make([]byte, 10)
	_, err := dec.Read(buf)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ERROR CRC32 expected hash '0xdeadbeef' but got '0x55bc801d'")
}

// TestDecodeSizeMismatch tests decoding data with a size mismatch
func TestDecodeSizeMismatch(t *testing.T) {
	raw := []byte{1, 2, 3}
	enc := NewEncoder()
	encoded := enc.Encode(raw)
	data := []byte(fmt.Sprintf(
		"=ybegin part=1 line=128 size=5 name=foo\r\n=ypart begin=1 end=4\r\n%s\r\n=yend size=5 part=1 pcrc32=00000000\r\n.\r\n",
		string(encoded),
	))
	dec := AcquireDecoder()
	defer ReleaseDecoder(dec)
	dec.SetReader(bytes.NewReader(data))
	buf := make([]byte, 10)
	_, err := dec.Read(buf)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected size 5 but got 3")
}

// TestUUEncodeNotImplemented tests that uuencode decoding is not implemented.
func TestUUEncodeNotImplemented(t *testing.T) {
	// Minimal valid UUencode message with NNTP EOF
	// "begin 644 file.txt\r\n" + "M" + 61 'A's + "\r\n" + ".\r\n"
	uu := []byte("begin 644 file.txt\r\n" +
		"MAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\r\n" +
		".\r\n")

	dec := NewDecoder(1024)
	dec.SetReader(bytes.NewReader(uu))

	buf := make([]byte, 1024)
	_, err := dec.Read(buf)
	if err == nil || !errors.Is(err, ErrDataMissing) && err.Error() != "[rapidyenc] uuencode not implemented" {
		t.Fatalf("expected uuencode not implemented error, got: %v", err)
	}
}

// TestDetectFormat tests the detectFormat function for various input lines.
func TestDetectFormat(t *testing.T) {
	tests := []struct {
		name string
		line []byte
		want Format
	}{
		{"yEnc header", []byte("=ybegin part=1 line=128 size=128 name=foo.txt\r\n"), FormatYenc},
		{"UUencode 63 bytes, \n", append([]byte{'M'}, make([]byte, 61)...), FormatUU},
		{"UUencode 63 bytes, \r", append([]byte{'M'}, make([]byte, 61)...), FormatUU},
		{"UUencode 62 bytes, \n", append([]byte{'M'}, make([]byte, 60)...), FormatUU},
		{"UUencode 62 bytes, \r", append([]byte{'M'}, make([]byte, 60)...), FormatUU},
		{"UUencode begin header", []byte("begin 644 file.txt"), FormatUU},
		{"Unknown", []byte("random data"), FormatUnknown},
	}

	tests[1].line[62] = '\n'
	tests[2].line[62] = '\r'
	tests[3].line[61] = '\n'
	tests[4].line[61] = '\r'

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := detectFormat(tc.line)
			if got != tc.want {
				t.Errorf("detectFormat(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}

// TestRapidyencDecoderFiles runs rapidyenc decoder tests on sample files.
func TestRapidyencDecoderFiles(t *testing.T) {
	errList := RapidyencDecoderFilesTest(t)
	for _, err := range errList {
		if err != nil {
			t.Errorf("rapidyenc decoder file test failed: %v", err)
		}
	}
}

// RapidyencDecoderFilesTest runs rapidyenc decoder tests on sample files.
// It reads yEnc encoded files, decodes them, and checks for CRC32 integrity.
func RapidyencDecoderFilesTest(t *testing.T) (errs []error) {
	files := []string{
		"yenc/multipart_test.yenc",
		"yenc/multipart_test_badcrc.yenc",
		"yenc/singlepart_test.yenc",
		"yenc/singlepart_test_badcrc.yenc",
	}
	for _, fname := range files {
		t.Logf("\n=== Testing rapidyenc with file: %s ===\n", fname)
		f, err := os.Open(filepath.Clean(fname))
		if err != nil {
			t.Errorf("Failed to open %s: %v\n", fname, err)
			continue
		}

		pipeReader, pipeWriter := io.Pipe()
		decoder := AcquireDecoderWithReader(pipeReader)
		decoder.SetDebug(true, true)
		//defer ReleaseDecoder(decoder) // Moved to returns and end of function, no defer in for loop
		segId := fname
		decoder.SetSegmentId(&segId)

		// Start goroutine to read decoded data
		var decodedData bytes.Buffer
		done := make(chan error, 1)
		go func() {
			buf := make([]byte, DefaultBufSize)
			for {
				n, aerr := decoder.Read(buf)
				if n > 0 {
					decodedData.Write(buf[:n])
				}
				if aerr == io.EOF {
					done <- nil
					return
				}
				if aerr != nil {
					done <- aerr
					return
				}
			}
		}()

		// Write file lines to the pipeWriter
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if _, err := pipeWriter.Write([]byte(line + "\r\n")); err != nil {
				t.Errorf("Error writing to pipe: %v\n", err)
				pipeWriter.Close()
				ReleaseDecoder(decoder)
				return
			}
		}
		if _, err := pipeWriter.Write([]byte(".\r\n")); err != nil { // NNTP end marker
			t.Errorf("Error writing end marker to pipe: %v\n", err)
			pipeWriter.Close()
			ReleaseDecoder(decoder)
			return
		}
		pipeWriter.Close()
		f.Close()
		if aerr := <-done; aerr != nil {
			err = aerr
			var aBadCrc uint32
			meta := decoder.Meta()
			t.Logf("DEBUG Decoder error: '%v' (maybe an expected error, check below)\n", err)
			expectedCrc := decoder.ExpectedCrc()
			if expectedCrc != 0 && expectedCrc != meta.Hash {

				// Set aBadCrc based on the file name
				switch fname {
				case "yenc/singlepart_test_badcrc.yenc":
					aBadCrc = 0x6d04a475
				case "yenc/multipart_test_badcrc.yenc":
					aBadCrc = 0xf6acc027
				}
				if aBadCrc > 0 && aBadCrc != meta.Hash {
					t.Errorf("WARNING1 rapidyenc: CRC mismatch! expected=%#08x | got meta.Hash=%#08x | wanted aBadCrc=%#08x fname: '%s'\n\n", expectedCrc, meta.Hash, aBadCrc, fname)
					errs = append(errs, aerr)
				} else if aBadCrc > 0 && aBadCrc == meta.Hash {
					t.Logf("rapidyenc OK expected=%#08x | got meta.Hash=%#08x | wanted aBadCrc=%#08x fname: '%s'\n\n", expectedCrc, meta.Hash, aBadCrc, fname)
				} else if expectedCrc != meta.Hash {
					t.Logf("WARNING2 rapidyenc: CRC mismatch! expected=%#08x | got meta.Hash=%#08x | wanted aBadCrc=%#08x fname: '%s'\n\n", expectedCrc, meta.Hash, aBadCrc, fname)
					errs = append(errs, aerr)
				} else {
					t.Logf("GOOD CRC matches! aBadCrc=%#08x Name: '%s' fname: '%s'\n", aBadCrc, meta.Name, fname)
				}

			} else if expectedCrc == 0 {
				t.Errorf("WARNING rapidyenc: No expected CRC set, cannot verify integrity. fname: '%s'\n", fname)
				errs = append(errs, aerr)
			}
		} else {
			meta := decoder.Meta()
			t.Logf("OK Decoded %d bytes, CRC32: %#08x, Name: '%s' fname: '%s'\n", decodedData.Len(), meta.Hash, meta.Name, fname)
		}
		ReleaseDecoder(decoder)
	} // end for range files
	return errs
}
