package rapidyenc

/*
#cgo CFLAGS: -I${SRCDIR}/src
#cgo darwin LDFLAGS: ${SRCDIR}/librapidyenc.a -lstdc++
#cgo windows,amd64 LDFLAGS: ${SRCDIR}/librapidyenc.a -lstdc++
#cgo windows,386   LDFLAGS: ${SRCDIR}/librapidyenc.a -lstdc++
#cgo windows,arm   LDFLAGS: ${SRCDIR}/librapidyenc.a -lstdc++
#cgo linux,amd64   LDFLAGS: ${SRCDIR}/librapidyenc.a -lstdc++
#cgo linux,386     LDFLAGS: ${SRCDIR}/librapidyenc.a -lstdc++
#cgo linux,arm     LDFLAGS: ${SRCDIR}/librapidyenc.a -lstdc++
#cgo linux,arm64   LDFLAGS: ${SRCDIR}/librapidyenc.a -lstdc++
#include "rapidyenc.h"
*/
import "C"
import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"unsafe"

	"golang.org/x/text/transform"
)

const constBufSize = 4096 // const buffer size for Decoder instances

var (
	decoderPool sync.Pool

	// Default buffer size for decoding can be set before calling any AcquireDecoder
	//  via: rapidyenc.DefaultBufSize = 64*1024 // 64 KiB
	DefaultBufSize = int(constBufSize) // Default buffer size for Decoder instances

	// private variables for the package to stay compatible with the old API
	defaultBufSize = DefaultBufSize
)

// Meta is the result of parsing the yEnc headers (ybegin, ypart, yend)
type Meta struct {
	Size  int64  // Total size of the file
	Begin int64  // Part begin offset (0-indexed)
	End   int64  // Part end offset (0-indexed, exclusive)
	Hash  uint32 // CRC32 hash of the decoded data
	Name  string // Name of the file
}

type Decoder struct {
	r io.Reader

	m Meta

	body  bool
	begin bool
	part  bool
	end   bool
	crc   bool

	State       State
	format      Format
	endSize     int64
	actualSize  int64
	expectedCrc uint32
	hash        hash.Hash32

	err error

	// dst[dst0:dst1] contains bytes that have been transformed by Transform but
	// not yet copied out via Read.
	dst        []byte
	dst0, dst1 int

	// src[src0:src1] contains bytes that have been read from r but not
	// yet transformed through Transform.
	src        []byte
	src0, src1 int

	// transformComplete is whether the transformation is complete,
	// regardless of whether it was successful.
	transformComplete bool

	debug1 bool    // debug mode, prints debug messages
	debug2 bool    // debug mode, prints more floody debug messages
	segId  *string // segment ID, if supplied, used for debugging
}

// AcquireDecoder returns an empty Decoder instance from Decoder pool.
//
// The returned Decoder instance may be passed to ReleaseDecoder when it is
// no longer needed. This allows Decoder recycling, reduces GC pressure
// and usually improves performance.
func AcquireDecoder() *Decoder {
	v := decoderPool.Get()
	if v == nil {
		return NewDecoder(defaultBufSize)
	}
	return v.(*Decoder)
}

// AcquireDecoderWithReader returns an empty Decoder instance from Decoder pool
// with the specified reader set.
//
// The returned Decoder instance may be passed to ReleaseDecoder when it is
// no longer needed. This allows Decoder recycling, reduces GC pressure
// and usually improves performance.
// The reader must be set before the first call to Read, otherwise it will panic.
func AcquireDecoderWithReader(reader io.Reader) *Decoder {
	v := decoderPool.Get()
	var dec *Decoder
	if v == nil {
		dec = NewDecoder(defaultBufSize)
	} else {
		dec = v.(*Decoder)
	}
	dec.SetReader(reader)
	return dec
}

// ReleaseDecoder returns dec acquired via AcquireDecoder to Decoder pool.
//
// It is forbidden accessing dec and/or its members after returning
// it to Decoder pool.
func ReleaseDecoder(dec *Decoder) {
	dec.Reset()
	decoderPool.Put(dec)
}

func NewDecoder(bufSize int) *Decoder {
	if bufSize <= 0 {
		dlog(always, "rapidyenc.NewDecoder: bufSize must be greater than 0, using constBufSize %d", constBufSize)
		bufSize = constBufSize
	}
	/*
		if bufSize != constBufSize {
			dlog(always, "rapidyenc.NewDecoder: bufSize is not constBufSize %d, using %d", constBufSize, bufSize)
		}
	*/
	segId := "" // empty segment ID pointer by default to prevent nil pointer dereference
	return &Decoder{
		dst:   make([]byte, bufSize),
		src:   make([]byte, bufSize),
		hash:  crc32.NewIEEE(),
		segId: &segId,
	}
}

// SetReader sets the io.Reader for the Decoder instance.
// It must be called before the first call to Read, otherwise it will panic.
func (d *Decoder) SetReader(reader io.Reader) {
	d.r = reader
}

// SetDebug enables debug mode, which prints debug messages to the console.
// This is useful for debugging the yEnc decoding process.
// has to be set before the first call to Read, otherwise it will data race.
func (d *Decoder) SetDebug(debug1 bool, debug2 bool) {
	d.debug1 = debug1
	d.debug2 = debug2
}

// SetSegmentId sets the segment ID for the Decoder instance.
// This is used for debugging purposes, to identify the segment being processed.
// If the segment ID is not set, it will default to an empty string.
// This is useful for debugging the yEnc decoding process, especially when
// multiple segments are being processed concurrently.
func (d *Decoder) SetSegmentId(segId *string) {
	d.segId = segId
}

// Meta returns the Meta information parsed from the yEnc headers.
/*
	// This includes the size of the file, the begin and end offsets, the CRC32 hash,
	// and the name of the file.
	// This is set when the ybegin, ypart, and yend headers are processed.
	// If the headers are not present, the Meta will contain default values.
	// The Meta can be used to verify the integrity of the decoded data and to
	// extract information about the file being decoded.
	// It is recommended to call this after the decoder has processed the yEnc data,
	// typically after the Read method has returned io.EOF.
	// If the yEnc headers are not present, the Meta will contain default values.
	// For example, Size will be 0, Begin and End will be 0, Hash will be 0,
	// and Name will be an empty string.
	// If the yEnc headers are present, the Meta will contain the parsed values.
	// For example, Size will be the total size of the file, Begin will be the
	// part begin offset (0-indexed), End will be the part end offset (0-indexed, exclusive),
	// Hash will be the CRC32 hash of the decoded data, and Name will be the name of the file.
	// This is useful for verifying the integrity of the decoded data and for extracting
	// information about the file being decoded.
*/
func (d *Decoder) Meta() Meta {
	return d.m
}

// ExpectedCrc returns the expected CRC32 hash of the decoded data.
// This is set when the yend header is processed and contains the CRC32 hash
// that the decoder expects to see at the end of the yEnc data.
// If the yend header does not contain a CRC32 hash, this will return 0.
// This is useful for verifying the integrity of the decoded data.
func (d *Decoder) ExpectedCrc() uint32 {
	return d.expectedCrc
}

var (
	ErrDataMissing    = errors.New("no binary data")
	ErrDataCorruption = errors.New("data corruption detected") // .\r\n reached before =yend
	ErrCrcMismatch    = errors.New("crc32 mismatch")
)

func (d *Decoder) Read(p []byte) (int, error) {
	n, err := 0, error(nil)
	for {
		// Defensive: clamp d.dst0 and d.dst1 to valid range BEFORE using them
		if d.dst0 < 0 {
			d.dst0 = 0
		}
		if d.dst1 > len(d.dst) {
			d.dst1 = len(d.dst)
		}
		if d.dst0 > d.dst1 {
			d.dst0 = d.dst1
		}

		// Copy out any transformed bytes and return the final error if we are done.
		if d.dst0 != d.dst1 {
			n = copy(p, d.dst[d.dst0:d.dst1])
			d.dst0 += n
			if d.dst0 == d.dst1 && d.transformComplete {
				return n, d.err
			}
			return n, nil
		} else if d.transformComplete {
			return 0, d.err
		}

		// Try to transform some source bytes, or to flush the transformer if we
		// are out of source bytes. We do this even if d.d.Read returned an error.
		// As the io.Reader documentation says, "process the n > 0 bytes returned
		// before considering the error".
		if d.src0 != d.src1 || d.err != nil {
			d.dst0 = 0
			d.dst1, n, err = d.Transform(d.dst, d.src[d.src0:d.src1], d.err == io.EOF)
			d.src0 += n
			// Clamp again after increment
			if d.src0 < 0 {
				d.src0 = 0
			}
			if d.src0 > d.src1 {
				d.src0 = d.src1
			}

			switch {
			case err == nil:
				d.transformComplete = d.err != nil
				continue
			case errors.Is(err, transform.ErrShortDst) && (d.dst1 != 0 || n != 0):
				continue
			case errors.Is(err, transform.ErrShortSrc) && d.src1-d.src0 != len(d.src) && d.err == nil:
			default:
				d.transformComplete = true
				if d.err == nil || d.err == io.EOF {
					d.err = err
				}
				continue
			}
		}

		// Move any untransformed source bytes to the start of the buffer
		// and read more bytes.
		if d.src0 != 0 {
			// Clamp d.src0 BEFORE slicing!
			if d.src0 < 0 {
				d.src0 = 0
			}
			if d.src0 > d.src1 {
				d.src0 = d.src1
			}
			d.src0, d.src1 = 0, copy(d.src, d.src[d.src0:d.src1])
		}
		n, d.err = d.r.Read(d.src[d.src1:])
		d.src1 += n
	}
}

// Transform starts by reading line-by-line to parse yEnc headers until it encounters yEnc data.
// It then incrementally decodes chunks of yEnc encoded data before returning to line-by-line processing
// for the footer and EOF pattern (.\r\n)
func (d *Decoder) Transform(dst, src []byte, atEOF bool) (nDst, nSrc int, err error) {
transform:
	// Line by line processing
	for {
		if nSrc < 0 {
			nSrc = 0
		}
		// Article EOF
		if bytes.HasPrefix(src[nSrc:], []byte(".\r\n")) {
			d.m.Hash = d.hash.Sum32()
			if d.format == FormatUU {
				return nDst, nSrc + 3, fmt.Errorf("[rapidyenc] uuencode not implemented")
			} else if !d.begin {
				err = fmt.Errorf("[rapidyenc] end of article without finding \"=begin\" header: %w", ErrDataMissing)
			} else if !d.end {
				err = fmt.Errorf("[rapidyenc] end of article without finding \"=yend\" trailer: %w", ErrDataCorruption)
			} else if (!d.part && d.m.Size != d.endSize) || (d.endSize != d.actualSize) {
				err = fmt.Errorf("[rapidyenc] expected size %d but got %d: %w", d.m.Size, d.actualSize, ErrDataCorruption)
			} else if d.crc && d.expectedCrc != d.m.Hash {
				// If we have a segment ID, use it for debugging
				// otherwise use an empty string.
				errStr := fmt.Sprintf("[rapidyenc] ERROR CRC32 expected hash '%#08x' but got '%#08x'! seg.Id='%s'", d.expectedCrc, d.m.Hash, *d.segId)
				dlog(always, "%s", errStr)
				err = fmt.Errorf("%s: %w", errStr, ErrCrcMismatch)
			} else {
				err = io.EOF
			}
			return nDst, nSrc + 3, err
		}

		endOfLine := bytes.Index(src[nSrc:], []byte("\r\n"))
		if endOfLine == -1 {
			break
		}

		line := src[nSrc : nSrc+endOfLine+2]
		nSrc += endOfLine + 2

		if d.format == FormatUnknown {
			d.format = detectFormat(line)
		}

		switch d.format {
		case FormatYenc:
			// Header/trailer lines
			if bytes.HasPrefix(line, []byte("=ybegin ")) ||
				bytes.HasPrefix(line, []byte("=ypart ")) ||
				bytes.HasPrefix(line, []byte("=yend ")) {
				d.processYenc(line)
				goto transform
			}
			// If we're in the body, decode this line
			if d.body {
				// Remove trailing \r\n for decoding
				bodyLine := line
				if len(bodyLine) >= 2 && bodyLine[len(bodyLine)-2] == '\r' && bodyLine[len(bodyLine)-1] == '\n' {
					bodyLine = bodyLine[:len(bodyLine)-2]
				}

				dlog(d.debug2, "DecodeIncremental input: %q\n", bodyLine)
				nd, ns, end, derr := DecodeIncremental(dst[nDst:], bodyLine, &d.State)
				if derr != nil && derr != io.EOF {
					dlog(always, "ERROR in rapidyenc.DecodeIncremental: nd=%d ns=%d end=%v err=%v\n", nd, ns, end, derr)
				}

				if nd > 0 {
					d.hash.Write(dst[nDst : nDst+nd])
					d.actualSize += int64(nd)
					nDst += nd
				}
				continue
			}
		case FormatUU:
			nDst += copy(dst[nDst:], line)
		}
	}

	if atEOF {
		return nDst, nSrc, io.EOF
	} else {
		return nDst, nSrc, transform.ErrShortSrc
	}
}

func (d *Decoder) Reset() {
	d.r = nil
	d.src0, d.src1 = 0, 0
	d.dst0, d.dst1 = 0, 0

	d.body = false
	d.begin = false
	d.part = false
	d.end = false
	d.crc = false

	d.State = StateCRLF
	d.format = FormatUnknown
	d.endSize = 0
	d.actualSize = 0
	d.expectedCrc = 0
	d.hash.Reset()
	d.m.Size = 0
	d.m.Hash = 0
	d.m.Begin = 0
	d.m.End = 0
	d.m.Name = ""

	d.err = nil
	d.transformComplete = false

	d.debug1 = false
	d.debug2 = false
	if d.segId != nil {
		// Reset the segment ID to an empty string
		// to prevent nil pointer dereference.
		// If a new segment ID is needed, it should be set via SetSegmentId.
		d.segId = new(string)
	}
}

func (d *Decoder) processYenc(line []byte) {
	dlog(d.debug2, "rapidyenc.processYenc(%q)", line)
	if bytes.HasPrefix(line, []byte("=ybegin ")) {
		d.begin = true
		d.m.Size, _ = extractInt(line, []byte(" size="))
		d.m.Name, _ = extractString(line, []byte(" name="))
		if _, err := extractInt(line, []byte(" part=")); err != nil {
			d.body = true
			d.m.End = d.m.Size
			dlog(d.debug1, "DEBUG: yEnc single-part, body starts")
		} else {
			dlog(d.debug1, "DEBUG: yEnc multi-part, waiting for =ypart")
		}
	} else if bytes.HasPrefix(line, []byte("=ypart ")) {
		d.part = true
		d.body = true
		if beginPos, err := extractInt(line, []byte(" begin=")); err == nil {
			d.m.Begin = beginPos - 1
		}
		if endPos, err := extractInt(line, []byte(" end=")); err == nil {
			d.m.End = endPos - 1
		}
		dlog(d.debug1, "DEBUG: =ypart found, body starts")
	} else if bytes.HasPrefix(line, []byte("=yend ")) {
		d.end = true
		if d.part {
			if crc, err := extractCRC(line, []byte(" pcrc32=")); err == nil {
				d.expectedCrc = crc
				d.crc = true
			}
		} else if crc, err := extractCRC(line, []byte(" crc32=")); err == nil {
			d.expectedCrc = crc
			d.crc = true
		}
		d.endSize, _ = extractInt(line, []byte(" size="))
		dlog(d.debug1, "DEBUG: =yend found")
	}
}

func detectFormat(line []byte) Format {
	if bytes.HasPrefix(line, []byte("=ybegin ")) {
		return FormatYenc
	}

	length := len(line)
	if (length == 62 || length == 63) && (line[62] == '\n' || line[62] == '\r') && line[0] == 'M' {
		return FormatUU
	}

	if bytes.HasPrefix(line, []byte("begin ")) {
		ok := true
		pos := len("begin ")
		for pos < len(line) && line[pos] != ' ' {
			pos++

			if line[pos] < '0' || line[pos] > '7' {
				ok = false
				break
			}
		}
		if ok {
			return FormatUU
		}
	}

	return FormatUnknown
}

type Format int

const (
	FormatUnknown Format = iota
	FormatYenc
	FormatUU
)

// State is the current Decoder State, the values refer to the previously seen
// characters in the stream, which influence how some sequences need to be handled.
//
// The shorthands represent:
// CR (\r), LF (\n), EQ (=), DT (.)
type State int

const (
	StateCRLF     = State(C.RYDEC_STATE_CRLF)
	StateEQ       = State(C.RYDEC_STATE_EQ)
	StateCR       = State(C.RYDEC_STATE_CR)
	StateNone     = State(C.RYDEC_STATE_NONE)
	StateCRLFDT   = State(C.RYDEC_STATE_CRLFDT)
	StateCRLFDTCR = State(C.RYDEC_STATE_CRLFDTCR)
	StateCRLFEQ   = State(C.RYDEC_STATE_CRLFEQ) // may actually be "\r\n.=" in raw Decoder
)

// End is the State for incremental decoding, whether the end of the yEnc data was reached
type End int

const (
	EndNone    = End(C.RYDEC_END_NONE)    // end not reached
	EndControl = End(C.RYDEC_END_CONTROL) // \r\n=y sequence found, src points to byte after 'y'
	EndArticle = End(C.RYDEC_END_ARTICLE) // \r\n.\r\n sequence found, src points to byte after last '\n'
)

var (
	errDestinationTooSmall = errors.New("destination must be at least the length of source")
)

var decodeInitOnce sync.Once

// DecodeIncremental decodes yEnc encoded data incrementally.
// DecodeIncremental stops decoding when a yEnc/NNTP end sequence is found
func DecodeIncremental(dst, src []byte, state *State) (nDst, nSrc int, end End, err error) {
	decodeInitOnce.Do(func() {
		C.rapidyenc_decode_init()
	})

	if len(src) == 0 {
		return 0, 0, EndNone, nil
	}

	if len(dst) < len(src) {
		return 0, 0, 0, errDestinationTooSmall
	}

	srcPointer := uintptr(unsafe.Pointer(&src[0]))
	dstPointer := uintptr(unsafe.Pointer(&dst[0]))

	result := End(C.rapidyenc_decode_incremental(
		(*unsafe.Pointer)(unsafe.Pointer(&srcPointer)),
		(*unsafe.Pointer)(unsafe.Pointer(&dstPointer)),
		C.size_t(len(src)),
		(*C.RapidYencDecoderState)(unsafe.Pointer(state)),
	))

	nSrc = int(srcPointer - uintptr(unsafe.Pointer(&src[0])))
	nDst = int(dstPointer - uintptr(unsafe.Pointer(&dst[0])))

	return nDst, nSrc, result, nil
}

func extractString(data, substr []byte) (string, error) {
	start := bytes.Index(data, substr)
	if start == -1 {
		return "", fmt.Errorf("substr not found: %s", substr)
	}

	data = data[start+len(substr):]
	if end := bytes.IndexAny(data, "\x00\r\n"); end != -1 {
		return string(data[:end]), nil
	}

	return string(data), nil
}

func extractInt(data, substr []byte) (int64, error) {
	start := bytes.Index(data, substr)
	if start == -1 {
		return 0, fmt.Errorf("substr not found: %s", substr)
	}

	data = data[start+len(substr):]
	if end := bytes.IndexAny(data, "\x00\x20\r\n"); end != -1 {
		return strconv.ParseInt(string(data[:end]), 10, 64)
	}

	return strconv.ParseInt(string(data), 10, 64)
}

var (
	errCrcNotfound = errors.New("crc not found")
)

// extractCRC converts a hexadecimal representation of a crc32 hash
func extractCRC(data, substr []byte) (uint32, error) {
	start := bytes.Index(data, substr)
	if start == -1 {
		return 0, errCrcNotfound
	}

	data = data[start+len(substr):]
	end := bytes.IndexAny(data, "\x00\x20\r\n")
	if end != -1 {
		data = data[:end]
	}

	// Take up to the last 8 characters
	parsed := data[len(data)-min(8, len(data)):]

	// Left pad unexpected length with 0
	if len(parsed) != 8 {
		padded := []byte("00000000")
		copy(padded[8-len(parsed):], parsed)
		parsed = padded
	}

	_, err := hex.Decode(parsed, parsed)
	return binary.BigEndian.Uint32(parsed), err
}

// debugging functions
const always = true // always log

// dlog is a debug log function that logs messages based on the logthis flag.
// If logthis is true, it logs the formatted message with the provided arguments.
// If logthis is false, it does nothing.
// It is used to control logging behavior in the code, allowing for easy toggling of debug output.
func dlog(logthis bool, format string, a ...any) {
	if !logthis {
		return
	}
	log.Printf(format, a...)
} // end dlog

func TestRapidyencDecoderFiles() (errs []error) {
	files := []string{
		"yenc/multipart_test.yenc",
		"yenc/multipart_test_badcrc.yenc",
		"yenc/singlepart_test.yenc",
		"yenc/singlepart_test_badcrc.yenc",
	}
	for _, fname := range files {
		fmt.Printf("\n=== Testing rapidyenc with file: %s ===\n", fname)
		f, err := os.Open(filepath.Clean(fname))
		if err != nil {
			fmt.Printf("Failed to open %s: %v\n", fname, err)
			continue
		}

		pipeReader, pipeWriter := io.Pipe()
		decoder := AcquireDecoderWithReader(pipeReader)
		decoder.SetDebug(true, true)
		defer ReleaseDecoder(decoder)
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
				fmt.Printf("Error writing to pipe: %v\n", err)
				pipeWriter.Close()
				ReleaseDecoder(decoder)
				return
			}
		}
		if _, err := pipeWriter.Write([]byte(".\r\n")); err != nil { // NNTP end marker
			fmt.Printf("Error writing end marker to pipe: %v\n", err)
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
			dlog(always, "DEBUG Decoder error: '%v' (maybe an expected error, check below)\n", err)
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
					fmt.Printf("WARNING1 rapidyenc: CRC mismatch! expected=%#08x | got meta.Hash=%#08x | wanted aBadCrc=%#08x fname: '%s'\n\n", expectedCrc, meta.Hash, aBadCrc, fname)
					errs = append(errs, aerr)
				} else if aBadCrc > 0 && aBadCrc == meta.Hash {
					fmt.Printf("rapidyenc OK expected=%#08x | got meta.Hash=%#08x | wanted aBadCrc=%#08x fname: '%s'\n\n", expectedCrc, meta.Hash, aBadCrc, fname)
				} else if expectedCrc != meta.Hash {
					fmt.Printf("WARNING2 rapidyenc: CRC mismatch! expected=%#08x | got meta.Hash=%#08x | wanted aBadCrc=%#08x fname: '%s'\n\n", expectedCrc, meta.Hash, aBadCrc, fname)
					errs = append(errs, aerr)
				} else {
					fmt.Printf("GOOD CRC matches! aBadCrc=%#08x Name: '%s' fname: '%s'\n", aBadCrc, meta.Name, fname)
				}

			} else if expectedCrc == 0 {
				fmt.Printf("WARNING rapidyenc: No expected CRC set, cannot verify integrity. fname: '%s'\n", fname)
				errs = append(errs, aerr)
			}
		} else {
			meta := decoder.Meta()
			fmt.Printf("OK Decoded %d bytes, CRC32: %#08x, Name: '%s' fname: '%s'\n", decodedData.Len(), meta.Hash, meta.Name, fname)
		}
		ReleaseDecoder(decoder)
	} // end for range files
	return errs
}
