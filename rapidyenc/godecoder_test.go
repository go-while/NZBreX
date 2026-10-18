package rapidyenc

import (
	"bytes"
	"fmt"
	"hash/crc32"
	"io"
	"sync"
	"testing"

	yencsimd "github.com/go-while/go-yenc-simd"
)

func TestStateValuesMatchYencsimd(t *testing.T) {
	pairs := []struct{ c, g int }{
		{int(StateCRLF), int(yencsimd.StateCRLF)}, {int(StateEQ), int(yencsimd.StateEQ)}, {int(StateCR), int(yencsimd.StateCR)},
		{int(StateNone), int(yencsimd.StateNone)}, {int(StateCRLFDT), int(yencsimd.StateCRLFDT)},
		{int(StateCRLFDTCR), int(yencsimd.StateCRLFDTCR)}, {int(StateCRLFEQ), int(yencsimd.StateCRLFEQ)},
		{int(EndNone), int(yencsimd.EndNone)}, {int(EndControl), int(yencsimd.EndControl)}, {int(EndArticle), int(yencsimd.EndArticle)},
	}
	for i, p := range pairs {
		if p.c != p.g {
			t.Fatalf("value %d: C %d != Go %d", i, p.c, p.g)
		}
	}
}

// The package's decoder tests again, with the Go decoder.
func TestDecoderTestsWithGoDecoder(t *testing.T) {
	UseGoDecoder = true
	defer func() { UseGoDecoder = false }()
	for _, tc := range []struct {
		name string
		f    func(*testing.T)
	}{
		{"Decode", TestDecode}, {"SplitReads", TestSplitReads},
		{"MissingYbegin", TestDecodeMissingYbegin}, {"MissingYend", TestDecodeMissingYend},
		{"CRCMismatch", TestDecodeCRCMismatch}, {"SizeMismatch", TestDecodeSizeMismatch},
		{"DecoderFiles", TestRapidyencDecoderFiles},
	} {
		t.Run(tc.name, tc.f)
	}
}

// readerArticle is a ~750 KB single-part yEnc article as it comes off the wire.
var readerArticle, readerSize = func() ([]byte, int) {
	data := make([]byte, 750_000)
	x := uint32(1)
	for i := range data {
		x = x*1664525 + 1013904223
		data[i] = byte(x >> 24)
	}
	body := NewEncoder().Encode(data)
	var b []byte
	b = append(b, fmt.Sprintf("=ybegin line=128 size=%d name=test.bin\r\n", len(data))...)
	for _, line := range bytes.SplitAfter(body, []byte("\r\n")) {
		if len(line) > 0 && line[0] == '.' {
			b = append(b, '.')
		}
		b = append(b, line...)
	}
	if !bytes.HasSuffix(b, []byte("\r\n")) {
		b = append(b, "\r\n"...)
	}
	b = append(b, fmt.Sprintf("=yend size=%d crc32=%08x\r\n.\r\n", len(data), crc32.ChecksumIEEE(data))...)
	return b, len(data)
}()

func benchReader(b *testing.B, goDecoder bool, bufSize int) {
	UseGoDecoder = goDecoder
	saved := DefaultBufSize
	DefaultBufSize = bufSize
	decoderPool = sync.Pool{} // pooled decoders keep their old buffer size
	defer func() { UseGoDecoder, DefaultBufSize, decoderPool = false, saved, sync.Pool{} }()
	buf := make([]byte, 64*1024)
	b.SetBytes(int64(len(readerArticle)))
	b.ReportAllocs()
	for b.Loop() {
		d := AcquireDecoderWithReader(bytes.NewReader(readerArticle))
		n := 0
		var err error
		for err == nil {
			var k int
			k, err = d.Read(buf)
			n += k
		}
		ReleaseDecoder(d)
		if err != io.EOF || n != readerSize {
			b.Fatalf("n=%d err=%v", n, err)
		}
	}
}

// The whole Decoder (headers, CRC32, size checks), C vs Go for the yEnc data.
func BenchmarkReaderC(b *testing.B)     { benchReader(b, false, 4096) }
func BenchmarkReaderGo(b *testing.B)    { benchReader(b, true, 4096) }
func BenchmarkReaderC64K(b *testing.B)  { benchReader(b, false, 64*1024) }
func BenchmarkReaderGo64K(b *testing.B) { benchReader(b, true, 64*1024) }
