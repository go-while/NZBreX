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
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"log"
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

// errors for yEnc decoding
var errCrcNotfound = errors.New("crc not found")

var ErrDataMissing = errors.New("no binary data")              // .\r\n reached before =ybegin or =ypart
var ErrDataCorruption = errors.New("data corruption detected") // .\r\n reached before =yend
var ErrCrcMismatch = errors.New("crc32 mismatch")

// errDestinationTooSmall is returned when the destination buffer is smaller than the source,
// indicating that the destination must be at least as long as the source to proceed.
var errDestinationTooSmall = errors.New("destination must be at least the length of source")

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

	debug1 bool // debug mode, prints debug messages
	// debugSpam enables verbose debug output, printing additional detailed debug messages for troubleshooting purposes.
	debugSpam bool    // debug mode, prints more floody debug messages
	segId     *string // segment ID, if supplied, used for debugging
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
func (d *Decoder) SetDebug(debug1 bool, debugSpam bool) {
	d.debug1 = debug1
	d.debugSpam = debugSpam
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

// Read reads transformed bytes from the Decoder instance.
// It reads from the underlying io.Reader, transforms the data using the Transform method,
// and returns the transformed bytes in the provided byte slice p.
// It returns the number of bytes read and any error encountered during the process.
func (d *Decoder) Read(p []byte) (int, error) {
	n, err := 0, error(nil)
	for {
		// Defensive: clamp d.dst0 and d.dst1 to valid range BEFORE using them
		d.clampDst()

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
			d.clampSrc()

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
			d.clampSrc()
			d.src0, d.src1 = 0, copy(d.src, d.src[d.src0:d.src1])
		}
		n, d.err = d.r.Read(d.src[d.src1:])
		d.src1 += n
	}
} // end func Read

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
			if !d.begin {
				switch d.format {
				case FormatUnknown:
					err = fmt.Errorf("[rapidyenc] FormatUnknown end of article without finding any *begin header: %w", ErrDataMissing)
				case FormatYenc:
					err = fmt.Errorf("[rapidyenc] FormatYenc end of article without finding \"=ybegin\" header: %w", ErrDataMissing)
				case FormatUU:
					err = fmt.Errorf("[rapidyenc] FormatUU end of article without finding \"begin\" header: %w", ErrDataCorruption)
				}
			} else if !d.end {
				switch d.format {
				case FormatUnknown:
					err = fmt.Errorf("[rapidyenc] FormatUnknown end of article without finding any *end header: %w", ErrDataMissing)
				case FormatYenc:
					err = fmt.Errorf("[rapidyenc] FormatYenc end of article without finding \"=yend\" trailer: %w", ErrDataMissing)
				case FormatUU:
					err = fmt.Errorf("[rapidyenc] FormatUU end of article without finding \"end\" trailer: %w", ErrDataCorruption)
				}
			} else if (d.format != FormatUU && d.format != FormatUnknown) && ((!d.part && d.m.Size != d.endSize) || (d.endSize != d.actualSize)) {
				err = fmt.Errorf("[rapidyenc] expected size %d but got %d: %w", d.m.Size, d.actualSize, ErrDataCorruption)
			} else if d.format == FormatYenc && d.crc && d.expectedCrc != d.m.Hash {
				// If we have a segment ID, use it for debugging otherwise use an empty string.
				err = fmt.Errorf("[rapidyenc] ERROR CRC32 expected hash '%#08x' but got '%#08x'! seg.Id='%s' err: %w", d.expectedCrc, d.m.Hash, *d.segId, ErrCrcMismatch)
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

				dlog(d.debugSpam, "DecodeIncremental input: %q\n", bodyLine)
				nd, ns, end, derr := DecodeIncremental(dst[nDst:], bodyLine, &d.State)
				if derr != nil && derr != io.EOF {
					dlog(always, "ERROR in rapidyenc.DecodeIncremental: nd=%d ns=%d end=%v err=%v\n", nd, ns, end, derr)
				}

				if nd > 0 {
					d.hash.Write(dst[nDst : nDst+nd])
					d.actualSize += int64(nd)
					nDst += nd
				}
				goto transform
			}

		case FormatUU:
			if bytes.HasPrefix(line, []byte("begin ")) || bytes.Equal(line, []byte("end\r\n")) {
				d.processYenc(line)
				goto transform
			}
			if d.body {
				bodyLine := line
				if len(bodyLine) >= 2 && bodyLine[len(bodyLine)-2] == '\r' && bodyLine[len(bodyLine)-1] == '\n' {
					bodyLine = bodyLine[:len(bodyLine)-2]
				}
				// Decode the UUencoded line
				decoded, err := UUdecode(bodyLine)
				if err != nil {
					d.err = fmt.Errorf("[rapidyenc] error decoding UUencoded line: %w", err)
					return nDst, nSrc, d.err
				}
				if len(decoded) > 0 {
					d.hash.Write(decoded)
					d.actualSize += int64(len(decoded))
					if nDst+len(decoded) > len(dst) {
						d.err = fmt.Errorf("[rapidyenc] destination buffer too small for UUencoded data: %w", errDestinationTooSmall)
						return nDst, nSrc, d.err
					}
					nDst += copy(dst[nDst:], decoded)
				}
			}
		}
	}

	if atEOF {
		// Check for missing yEnc header at EOF
		// ! REVIEW ! not sure if we even get here since the formatUnknown is checked above
		if !d.begin || !d.end {
			switch d.format {
			case FormatUnknown:
				return nDst, nSrc, fmt.Errorf("[rapidyenc] FormatUnknown end of article without finding any *begin or *end header: %w", ErrDataMissing)
			case FormatYenc:
				return nDst, nSrc, fmt.Errorf("[rapidyenc] FormatYenc end of article without finding \"=ybegin\" or \"=yend\" header: %w", ErrDataMissing)
			case FormatUU:
				return nDst, nSrc, fmt.Errorf("[rapidyenc] FormatUU end of article without finding \"begin\" or \"end\" header: %w", ErrDataMissing)
			}
		}
		return nDst, nSrc, io.EOF
	} else {
		return nDst, nSrc, transform.ErrShortSrc
	}
} // end func Transform

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
	d.debugSpam = false
	if d.segId != nil {
		// Reset the segment ID to an empty string
		// to prevent nil pointer dereference.
		// If a new segment ID is needed, it should be set via SetSegmentId.
		d.segId = new(string)
	}
} // end func Reset

func (d *Decoder) processYenc(line []byte) {
	dlog(d.debugSpam, "rapidyenc.processYenc(%q)", line)
	switch d.format {
	case FormatYenc:
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
	case FormatUU:
		if bytes.HasPrefix(line, []byte("begin ")) {
			// UUencode header: begin <mode> <filename>
			d.begin = true
			d.body = true
			d.format = FormatUU
			// Optionally extract filename and mode
			fields := bytes.Fields(line)
			if len(fields) >= 3 {
				d.m.Name = string(fields[2])
			}
			dlog(d.debug1, "DEBUG: UUencode begin found, body starts")
		} else if bytes.Equal(line, []byte("end\r\n")) || bytes.Equal(line, []byte("end\n")) {
			// UUencode trailer
			d.end = true
			d.body = false
			dlog(d.debug1, "DEBUG: UUencode end found")
		}
	} // end switch d.format
} //end func processYenc

func detectFormat(line []byte) Format {
	if bytes.HasPrefix(line, []byte("=ybegin ")) {
		return FormatYenc
	}

	length := len(line)
	if length >= 1 && line[0] == 'M' &&
		((length == 63 && (line[62] == '\n' || line[62] == '\r')) ||
			(length == 62 && (line[61] == '\n' || line[61] == '\r'))) {
		return FormatUU
	}

	if bytes.HasPrefix(line, []byte("begin ")) {
		pos := len("begin ")
		// Parse mode: must be 3 octal digits
		if pos+3 <= len(line) {
			for i := range 3 {
				if line[pos+i] < '0' || line[pos+i] > '7' {
					return FormatUnknown
				}
			}
			// Next must be a space
			if pos+3 < len(line) && line[pos+3] == ' ' {
				return FormatUU
			}
		}
	}

	return FormatUnknown
} //end func detectFormat

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

// UUdecode decodes a single UUencoded line (e.g., "M<uu-bytes>\r\n").
// It returns the decoded bytes or an error.
func UUdecode(line []byte) ([]byte, error) {
	if len(line) == 0 {
		return nil, nil
	}
	// Remove trailing \r\n if present
	if len(line) >= 2 && line[len(line)-2] == '\r' && line[len(line)-1] == '\n' {
		line = line[:len(line)-2]
	}
	if len(line) == 0 {
		return nil, nil
	}
	// The first byte is the encoded length
	encLen := int(line[0]-0x20) & 0x3F
	if encLen == 0 {
		return []byte{}, nil
	}
	decoded := make([]byte, 0, encLen)
	i := 1
	for encLen > 0 && i+4 <= len(line) {
		// Each group of 4 chars encodes 3 bytes
		var c [4]byte
		for j := 0; j < 4; j++ {
			c[j] = (line[i+j] - 0x20) & 0x3F
		}
		decoded = append(decoded,
			(c[0]<<2)|(c[1]>>4),
			(c[1]<<4)|(c[2]>>2),
			(c[2]<<6)|c[3],
		)
		i += 4
		encLen -= 3
	}
	// Truncate to the actual length
	if encLen < 0 {
		decoded = decoded[:len(decoded)+encLen]
	}
	return decoded, nil
} //end func UUdecode

// extractCRC converts a hexadecimal representation of a crc32 hash
// from the data starting after the given substring.
// It searches for the substring in the data, and if found, it extracts
// the crc32 hash value, ensuring it is 8 characters long.
// Zero-pads the value if it is shorter than 8 characters.
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

// clampDst clamps d.dst0 and d.dst1 to valid ranges
func (d *Decoder) clampDst() {
	if d.dst0 < 0 {
		d.dst0 = 0
	}
	if d.dst1 > len(d.dst) {
		d.dst1 = len(d.dst)
	}
	if d.dst0 > d.dst1 {
		d.dst0 = d.dst1
	}
}

// clampSrc clamps d.src0 to [0, d.src1]
func (d *Decoder) clampSrc() {
	if d.src0 < 0 {
		d.src0 = 0
	}
	if d.src0 > d.src1 {
		d.src0 = d.src1
	}
}
