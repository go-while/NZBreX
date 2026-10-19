package main

// The -yenctest modes compared on the same article, through readDotLines (the
// path a download takes): correctness first, then speed.
//
//	go test -run YencModes -bench YencModes -benchmem .
//	go test -tags rapidyenc -run YencModes -bench YencModes -benchmem .   # adds 4-go (-yencgo)

import (
	"bufio"
	"bytes"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/textproto"
	"runtime"
	gotesting "testing"
	"time"

	"github.com/go-while/NZBreX/rapidyenc"
	"github.com/go-while/go-loggedrwmutex"
)

// memConn is a net.Conn that reads one article from memory.
type memConn struct{ *bytes.Reader }

func (memConn) Write(p []byte) (int, error)      { return len(p), nil }
func (memConn) Close() error                     { return nil }
func (memConn) LocalAddr() net.Addr              { return nil }
func (memConn) RemoteAddr() net.Addr             { return nil }
func (memConn) SetDeadline(time.Time) error      { return nil }
func (memConn) SetReadDeadline(time.Time) error  { return nil }
func (memConn) SetWriteDeadline(time.Time) error { return nil }

// yencModesArticle is part 2 of 3 of a file, size data bytes (random, PCG seed
// 7), 128-byte lines, dot-stuffed for NNTP, with the final dot. damaged changes
// one data byte after encoding, so the pcrc32 no longer matches.
func yencModesArticle(size int, damaged bool) []byte {
	return yencModesArticleDots(size, damaged, true)
}

// yencModesArticleDots: with escapeDots false, a '.' at the start of a line is
// not yEnc-escaped (allowed: the encoder relies on NNTP dot-stuffing).
func yencModesArticleDots(size int, damaged, escapeDots bool) []byte {
	r := rand.New(rand.NewPCG(7, 8))
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(r.Uint32())
	}
	var b bytes.Buffer
	b.WriteString("Subject: bench [2/3] \"bench.bin\" yEnc (2/3)\r\nMessage-ID: <a@b>\r\n\r\n")
	fmt.Fprintf(&b, "=ybegin part=2 total=3 line=128 size=%d name=bench.bin\r\n", 3*size)
	fmt.Fprintf(&b, "=ypart begin=%d end=%d\r\n", size+1, 2*size)
	var line []byte
	flush := func() {
		if len(line) > 0 && line[0] == '.' {
			b.WriteByte('.') // NNTP dot-stuffing
		}
		b.Write(line)
		b.WriteString("\r\n")
		line = line[:0]
	}
	for i, c := range data {
		o := c + 42
		last := i == len(data)-1 || len(line) >= 127
		if o == 0 || o == '\n' || o == '\r' || o == '=' || (o == '\t' || o == ' ') && (len(line) == 0 || last) || escapeDots && o == '.' && len(line) == 0 {
			line = append(line, '=', o+64)
		} else {
			line = append(line, o)
		}
		if len(line) >= 128 {
			flush()
		}
	}
	flush()
	fmt.Fprintf(&b, "=yend size=%d part=2 pcrc32=%08x\r\n.\r\n", size, crc32.ChecksumIEEE(data))
	art := b.Bytes()
	if damaged {
		i := bytes.Index(art, []byte("=ypart"))
		i += bytes.Index(art[i:], []byte("\r\n")) + 2 + 500 // inside the first data line
		for art[i] == '=' || art[i-1] == '=' || art[i] == '\r' || art[i] == '\n' || art[i] == '.' {
			i++
		}
		art[i]++
		if art[i] == '=' || art[i] == '\r' || art[i] == '\n' || art[i] == 0 {
			art[i] += 2
		}
	}
	return art
}

// yencModesSetup configures readDotLines for -crc32 -yenctest=mode and silences
// the per-article log lines (they are still formatted, as in a real run).
func yencModesSetup(tb gotesting.TB) {
	tb.Helper()
	testCfg()
	old := *cfg.opt
	oldOut := log.Writer()
	tb.Cleanup(func() { *cfg.opt = old; log.SetOutput(oldOut); rapidyenc.UseGoDecoder = false })
	log.SetOutput(io.Discard)
	cfg.opt.YencCRC, cfg.opt.MaxArtSize, cfg.opt.RapidYencBufSize = true, 4<<20, 4096
	cfg.opt.YencCpu, cfg.opt.YencAsyncCpu = runtime.NumCPU(), runtime.NumCPU()
	setupCoreLimiters()
}

type yencMode struct {
	name  string
	mode  int  // -yenctest; 0: -crc32 off (lines are only read)
	goDec bool // -yencgo
}

func yencModes() []yencMode {
	m := []yencMode{{"read-only", 0, false}, {"1", 1, false}, {"2", 2, false}, {"3", 3, false}, {"4", 4, false}}
	if rapidyenc.CBackend {
		m = append(m, yencMode{"4-go", 4, true})
	}
	return m
}

func yencModesRead(m yencMode, art []byte) (int, error) {
	cfg.opt.YencCRC = m.mode != 0
	cfg.opt.YencTest = m.mode
	rapidyenc.UseGoDecoder = m.goDec
	conn := memConn{bytes.NewReader(art)}
	prov := &Provider{Name: "bench", mux: &loggedrwmutex.LoggedSyncRWMutex{Name: "bench"}}
	connitem := &ConnItem{c: &ConnPool{provider: prov}, conn: conn, srvtp: textproto.NewConn(conn), writer: bufio.NewWriter(conn)}
	code, _, _, err := readDotLines(connitem, testItem("<a@b>"), cmdARTICLE)
	return code, err
}

// Every mode accepts an intact article and plain text (nothing to check), and
// reports a damaged one (CRC) and a cut-off one (no =yend) as bad (99932).
func TestYencModesAgree(t *gotesting.T) {
	yencModesSetup(t)
	good := yencModesArticle(750_000, false)
	damaged := yencModesArticle(750_000, true)
	cut := bytes.Clone(good)
	cut = append(cut[:bytes.Index(cut, []byte("=yend "))], ".\r\n"...)
	plain := []byte("Subject: plain\r\nMessage-ID: <a@b>\r\n\r\nhello world\r\n..a line starting with a dot\r\n.\r\n")
	cases := []struct {
		name string
		art  []byte
		bad  bool
	}{{"intact", good, false}, {"damaged", damaged, true}, {"cut off", cut, true}, {"plain text", plain, false},
		{"leading dots", yencModesArticleDots(750_000, false, false), false}}
	for _, m := range yencModes() {
		if m.mode == 0 {
			continue
		}
		for _, c := range cases {
			code, err := yencModesRead(m, c.art)
			if c.bad && code != 99932 {
				t.Errorf("mode %s, %s article: code=%d err=%v, want 99932 (bad)", m.name, c.name, code, err)
			}
			if !c.bad && (err != nil || code == 99932) {
				t.Errorf("mode %s, %s article: code=%d err=%v, want accepted", m.name, c.name, code, err)
			}
		}
	}
}

func BenchmarkYencModes(b *gotesting.B) {
	yencModesSetup(b)
	art := yencModesArticle(750_000, false)
	for _, m := range yencModes() {
		b.Run("mode="+m.name, func(b *gotesting.B) {
			b.SetBytes(int64(len(art)))
			b.ReportAllocs()
			for b.Loop() {
				if code, err := yencModesRead(m, art); err != nil || code == 99932 {
					b.Fatalf("code=%d err=%v", code, err)
				}
			}
		})
	}
}

// The same with -doublecheckrapidyenccrc (mode 4 validates its result again).
func TestYencModesAgreeDoubleCheck(t *gotesting.T) {
	yencModesSetup(t)
	cfg.opt.DoubleCheckRapidYencCRC = true
	TestYencModesAgree(t)
}
