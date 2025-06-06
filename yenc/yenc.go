// another yenc decoder (experimental/testing)
// modded from: github.com/chrisfarms/yenc
// to be used in NZBreX (nzbrefreshX)
package yenc

import (
	"bufio"
	"bytes"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"log"
	"strconv"
	"strings"
)

var (
	Debug1         = false
	Debug2         = false
	Debug3         = false
	DebugThis11    = false
	DebugPartCRCok = false
	SimdMode       = 0 // 0 for default, 1 for SIMD1, 2 for SIMD2 (mode 2 is broken!)
)

type Decoder struct {
	// set <= 0 if unknown or any number but mostly only 1!
	toCheck int64
	// the buffered input
	Buf *bufio.Reader
	// alternative input as []string
	Dat []*string

	// below fields are set by the decoder
	// !! dont set yourself!

	// whether we are decoding multipart
	Multipart bool
	// numer of parts if given
	total int
	// list of parts
	Parts []*Part
	// active part
	Part *Part
	// overall crc check
	Fullcrc32 uint32
	crcHash   hash.Hash32
	// are we waiting for an escaped char
	awaitingSpecial bool
	// supply this when creating a new decoder for a single part!
	SegId *string
}

type Part struct {
	// part num
	Number int
	// size from header
	HeaderSize int64
	// size from part trailer
	Size int64
	// file boundarys
	Begin, End int64
	// filename from yenc header
	Name string
	// line length of part
	cols int
	// crc check for this part
	Crc32   uint32
	crcHash hash.Hash32
	// the decoded data
	Body   []byte
	BadCRC bool
}

// supply only one: ior or in1 or in2!
// toCheck should be <= 0 if unknown or any number but mostly only 1!
// if 'in2 []string' is supplied:
//
//	you have to set 'toCheck 1' or it will not get an EOF and will not release!
func NewDecoder(ior io.Reader, in1 []byte, in2 []*string, toCheck int64) *Decoder {
	var decoder Decoder
	if ior != nil {
		decoder.Buf = bufio.NewReader(ior)
	} else if in1 != nil {
		decoder.Buf = bufio.NewReader(bytes.NewReader(in1))
	} else if in2 != nil {
		decoder.Dat = in2
	} else if len(in1) == 0 && len(in2) == 0 {
		// nothing is given, we will parse lines directly from readDotLines
		decoder.crcHash = crc32.NewIEEE()
		decoder.Part = new(Part)
		decoder.Part.crcHash = crc32.NewIEEE()
	}
	decoder.toCheck = toCheck
	return &decoder
} // end func yenc.NewDecoder(in1, in2)

// return a single part from yenc data
func (d *Decoder) DecodeSlice() (part *Part, badcrc bool, err error) {
	//d := &Decoder{dat: input}
	if badcrc, err = d.run(); err != nil && err != io.EOF && !badcrc {
		log.Printf("Error in yenc.DecodeSlice #1 err='%v'", err)
		return nil, false, err
	}
	if badcrc {
		log.Printf("BadCrc32 return in DecodeSlice!")
		return nil, true, nil
	}
	if len(d.Parts) != 1 {
		if Debug1 {
			log.Printf("Error in yenc.DecodeSlice #2 'len(d.Parts) == 0' err='%v'", err)
		}
		return nil, false, fmt.Errorf("no yenc parts found")
	}
	// validate multipart only if all parts are present
	//if !d.Multipart || len(d.Parts) == d.Parts[len(d.Parts)-1].Number { //  ?????????
	if d.Multipart && len(d.Parts) > 1 && len(d.Parts) == d.Parts[len(d.Parts)-1].Number {
		if Debug3 {
			log.Printf("yenc.DecodeSlice d.Validate() d.Multipart=%t parts=%d", d.Multipart, len(d.Parts))
		}
		if err := d.Validate(); err != nil {
			log.Printf("Error in yenc.DecodeSlice #3 d.Validate err='%v'", err)
			return nil, false, err
		}
	}
	if Debug3 {
		log.Printf("OK yenc.DecodeSlice return yPart.Number=%d Body=%d parts=%d", d.Parts[0].Number, len(d.Parts[0].Body), len(d.Parts))
	}
	return d.Parts[0], false, nil
} // end func DecodeSlice

func (d *Decoder) Decode() (part *Part, badcrc bool, err error) {
	//d := &Decoder{buf: bufio.NewReader(input)}
	if badcrc, err = d.run(); err != nil && err != io.EOF && !badcrc {
		return nil, false, fmt.Errorf("error in yenc.Decode #1 err='%v'", err)
	}
	if badcrc {
		return nil, true, nil
	}
	if len(d.Parts) == 0 {
		return nil, false, fmt.Errorf("error in yenc.Decode #2 'len(d.Parts) == 0' err='%#v'", err)
	}
	// validate multipart only if all parts are present
	//if !d.Multipart || len(d.Parts) == d.Parts[len(d.Parts)-1].Number { //  ?????????
	if d.Multipart && len(d.Parts) > 1 && len(d.Parts) == d.Parts[len(d.Parts)-1].Number {
		if Debug3 {
			log.Printf("yenc.Decode d.Validate() d.Multipart=%t parts=%d", d.Multipart, len(d.Parts))
		}
		if err := d.Validate(); err != nil {
			return nil, false, fmt.Errorf("error in yenc.Decode #3 d.Validate err='%v'", err)
		}
	}
	if Debug3 {
		log.Printf("OK yenc.Decode return yPart.Number=%d Body=%d parts=%d", d.Parts[0].Number, len(d.Parts[0].Body), len(d.Parts))
	}
	return d.Parts[0], false, nil
} // end func Decode

// internal functions following

func (d *Decoder) LineDecoder(line *string) (badcrc bool, err error) {
	return
}

func (d *Decoder) run() (badcrc bool, err error) {
	// init hash
	d.crcHash = crc32.NewIEEE()
	var checked int64 = 0
	processed := make(map[string]map[int]bool)
	// for each part
	for {
		// create a part
		d.Part = new(Part)

		// read the header
		if err := d.ReadHeader(nil, false); err != nil {
			// when reading from io.reader or with []bytes
			// ^ we use a buffer which clears out while reading
			// : but with []*string we won't hit an io.EOF while iterating over and over again!
			// ! results in oom quickly as it generates new parts and fills them all with the same!
			//log.Printf("Debug ReadHeader err='%v'", err)
			return false, err
		}
		if Debug2 {
			log.Printf("yenc.Decoder.run: #1 done d.readHeader() @Number=%d", d.Part.Number)
		}
		if d.Part.Name == "" {
			return false, fmt.Errorf("ERROR in yenc.Decoder.run() empty Name field fn='%s' part=%d", d.Part.Name, d.Part.Number)
		}
		if processed[d.Part.Name] == nil {
			processed[d.Part.Name] = make(map[int]bool, d.total)
		}
		if processed[d.Part.Name][d.Part.Number] {
			return false, fmt.Errorf("ERROR in yenc.Decoder.run() already processed fn='%s' part=%d", d.Part.Name, d.Part.Number)
		}
		processed[d.Part.Name][d.Part.Number] = true // set it here or later? should not matter as we return on any err

		if Debug2 {
			log.Printf("yenc.Decoder.run: #1 process d.Part.Number=%d", d.Part.Number)
		}

		// read part header if available
		if d.Multipart {
			if err := d.ReadPartHeader(""); err != nil {
				log.Printf("Debug readPartHeader err='%v'", err)
				return false, err
			}
		}
		if Debug2 {
			log.Printf("yenc.Decoder.run: #2 done:d.readPartHeader @Num=%d", d.Part.Number)
		}

		// decode the part body
		if err := d.ReadBody("", false); err != nil {
			log.Printf("Debug readBody err='%v'", err)
			return false, err
		}
		if Debug2 {
			log.Printf("yenc.Decoder.run: #3 done:d.readBody @Num=%d", d.Part.Number)
		}

		// validate part
		if err := d.Part.Validate(d.SegId); err != nil || d.Part.BadCRC {
			/*
			 * d.Part='&yenc.Part{
			 * 		Number:18,
			 * 		HeaderSize:209715695,
			 * 		Size:640000,
			 * 		Begin:10880001,
			 * 		End:11520000,
			 * 		Name:"debian-11.1.0-i386-DVD-1.part01.rev",
			 * 		cols:128,
			 * 		Crc32:0xfd8ec0f3,
			 * 		crcHash:(*crc32.digest)(0xc00092d230), Body: :[]uint8{0x93, 0x9, 0x2d, 0xeb, 0x7d, 0x1d, 0x55, 0xbf, 0xfe, 0xde, 0x89, 0x68, 0x56, 0x84, 0x82, 0xf9, 0xed, ....
			 */
			log.Printf("Error yenc.Decoder.run: validate @Number=%d err='%v'", d.Part.Number, err)
			return false, err
		}
		//log.Printf("yenc.Decoder.run: process #4 d.Part.Number=%d", d.Part.Number)
		if d.Part.BadCRC {
			d.Part = nil
			return true, nil
		}

		// add part to list
		d.Parts = append(d.Parts, d.Part)

		if Debug3 {
			log.Printf("yenc.Decoder.run: #4 done d.Validate @Number=%d parts=%d", d.Part.Number, len(d.Parts))
		}

		checked++
		if d.toCheck > 0 && checked == d.toCheck {
			break
		}
		if Debug3 {
			log.Printf("yenc decoded d.Part.Number=%d SegId='%s'", d.Part.Number, *d.SegId)
		}
	}
	return false, nil
} // end func d.run()

func (p *Part) Validate(segId *string) error {
	// length checks
	if Debug1 {
		log.Printf("yenc.Part.Validate() p.Number=%d c.Crc32=%x", p.Number, p.Crc32)
	}
	if int64(len(p.Body)) != p.Size {
		diff := int64(len(p.Body)) - p.Size
		bodyLarge := int64(len(p.Body)) > p.Size
		bodySmall := int64(len(p.Body)) < p.Size
		return fmt.Errorf("error in yenc.Part.Validate Number=%d: Body size %d did not match expected size %d bodyLarge=%t bodySmall=%t diff=%d", p.Number, len(p.Body), p.Size, bodyLarge, bodySmall, diff)
	}
	// crc check
	if p.Crc32 > 0 || p.crcHash == nil {
		if sum := p.crcHash.Sum32(); sum != p.Crc32 {
			p.BadCRC = true
			log.Printf("CRC32 segId='%s' part=%d expected %x got %x len(p.Body)=%d", *segId, p.Number, p.Crc32, sum, len(p.Body))
			p.Body = nil
			return nil
		}
		if DebugPartCRCok {
			log.Printf("OK yenc.part.validate() seg.Id='%s' p.Number=%d", *segId, p.Number)
		}
		return nil
	}
	return fmt.Errorf("error in yenc.Part.validate: p.Crc32=0 or crcHash is nil segId='%s' p.Number=%d", *segId, p.Number)
}

func (d *Decoder) ReadHeader(line *string, check bool) (err error) {
	var s string
	i := 0
	// find the start of the header
	if d.Buf != nil {
		for {
			s, err = d.Buf.ReadString('\n')
			if err != nil {
				if err != io.EOF {
					log.Printf("ERROR yenc.readHeader SegId='%s' err='%v'", *d.SegId, err)
				}
				return err
			}
			if len(s) >= 7 && s[:7] == "=ybegin" {
				break
			}
		}
	} else if d.Dat != nil {
		for _, sptr := range d.Dat { // s is a line
			if len(*sptr) < 7 { // must be at least 8 chars long
				// a short line can not be a header
				continue
			}
			if string(*sptr)[0] != '=' {
				// line does not start with a "="
				continue
			}
			if strings.HasPrefix(*sptr, "=ybegin") {
				s = string(*sptr)
				break
			}
			i++
		}
	} else if line != nil {
		if check {
			if len(*line) < 7 { // must be at least 8 chars long
				return fmt.Errorf("error in yenc.ReadHeader: line too short len=%d got '%s'", len(s), s)
			}
			s = string(*line)
			if !strings.HasPrefix(s, "=ybegin ") {
				return fmt.Errorf("error in yenc.ReadHeader: line does not start with '=ybegin' got '%s'", s)
			}
		} else {
			// if check is not set we have done the check before
			// probably in readDotLines
			// so we can just use the line as is
			s = string(*line)
			if Debug3 {
				log.Printf("yenc.ReadHeader: mode=3 line='%s' SegId='%s' d.Part.Number=%d i=%d", s, *d.SegId, d.Part.Number, i)
			}
		}
		// pass
	} else {
		return fmt.Errorf("error in yenc.ReadHeader: no input provided")
	}

	// additional check here for len.
	// if upper for loops don't break out: len of "s" will be 0!
	if len(s) < 7 {

		log.Printf("WARN yenc.ReadHeader len(s)=%d d.Part.Number=%d SegId='%s' len(d.Dat)=(%d lines) i=%d", len(s), d.Part.Number, *d.SegId, len(d.Dat), i)

		// REVIEW!
		// return no error here.
		// if part.Name has not been captured
		// we'll check it one layer up! jump to #f852f824
		return nil
	}

	// split on name= to get name first
	parts := strings.SplitN(s[7:], "name=", 2)
	if len(parts) > 1 {
		d.Part.Name = strings.TrimSpace(parts[1])
	}
	// split on sapce for other headers
	parts = strings.Split(parts[0], " ")
	for i := range parts {
		kv := strings.Split(strings.TrimSpace(parts[i]), "=")
		if len(kv) < 2 {
			continue
		}
		switch kv[0] {
		case "size":
			d.Part.HeaderSize, _ = strconv.ParseInt(kv[1], 10, 64)
		case "line":
			d.Part.cols, _ = strconv.Atoi(kv[1])
		case "part":
			d.Part.Number, _ = strconv.Atoi(kv[1])
			d.Multipart = true
		case "total":
			d.total, _ = strconv.Atoi(kv[1])
		}
	}
	return nil
}

func (d *Decoder) ReadPartHeader(line string) (err error) {
	var s string
	if line != "" {
		// if line is given we use it as the header
		s = line
	} else if d.Part == nil {
		// if no line is given and no part is set
		return fmt.Errorf("error in yenc.ReadPartHeader: no line given and no part set")
	}
	// find the start of the header
	if d.Buf != nil {
		for {
			s, err = d.Buf.ReadString('\n')
			if err != nil {
				return err
			}
			if len(s) >= 6 && s[:6] == "=ypart" {
				break
			}
		}
	} else if d.Dat != nil {
		for _, sptr := range d.Dat { // s is a line
			if len(*sptr) >= 6 && string(*sptr)[:6] == "=ypart" {
				s = *sptr
				break
			}
		}
	} else if len(s) > 6 && strings.HasPrefix(s, "=ypart ") {
		// pass
		if Debug3 {
			log.Printf("yenc.ReadPartHeader: mode=3 line='%s' SegId='%s' d.Part.Number=%d", s, *d.SegId, d.Part.Number)
		}
	} else {
		return fmt.Errorf("error in yenc.ReadPartHeader: no input provided")
	}
	// split on space for headers
	parts := strings.Split(s[6:], " ")
	for i := range parts {
		kv := strings.Split(strings.TrimSpace(parts[i]), "=")
		if len(kv) < 2 {
			continue
		}
		switch kv[0] {
		case "begin":
			d.Part.Begin, _ = strconv.ParseInt(kv[1], 10, 64)
		case "end":
			d.Part.End, _ = strconv.ParseInt(kv[1], 10, 64)
		}
	}
	return nil
}

func (d *Decoder) ReadBody(line string, byline bool) error {
	// read the part body
	if byline {
		if d.Part.Body == nil {
			// if no body is set we create a new one
			d.Part.Body = make([]byte, 0)
		}
		// pass
	} else {
		// setup crc hash
		d.Part.Body = make([]byte, 0)
		d.Part.crcHash = crc32.NewIEEE()
		// reset special
		d.awaitingSpecial = false
	}

	if d.Buf != nil {
		// if we have a buffered reader we read from it
		for {
			line, err := d.Buf.ReadBytes('\n')
			if err != nil {
				log.Printf("Error in yenc.Decoder.ReadBody d.Buf.ReadBytes err='%v'", err)
				return err
			}
			// strip linefeeds (some use CRLF some LF)
			line = bytes.TrimRight(line, "\r\n")
			// check for =yend
			if len(line) >= 5 && string(line[:5]) == "=yend" {
				if Debug1 {
					log.Printf("yenc.Decoder d.Buf =yend d.Part.Body=%d", len(d.Part.Body))
				}
				return d.ParseTrailerNew(string(line))
			}
			// decode
			b := d.DecodeLine(line)
			// update hashs
			d.Part.crcHash.Write(b)
			d.crcHash.Write(b)
			// decode
			d.Part.Body = append(d.Part.Body, b...)
		}

	} else if d.Dat != nil {
		// if we have a list of lines we read from it
		if Debug1 {
			log.Printf("yenc.Decoder readBody lines d.Dat=%d", len(d.Dat))
		}
		b, err := d.DecodeYenc()
		if err != nil {
			return err
		}
		d.Part.crcHash.Write(b)
		d.crcHash.Write(b)
		// decode
		d.Part.Body = append(d.Part.Body, b...)
		return nil

	} else if byline {
		/*
			if len(line) == 0 {
				// probably EOF ! REVIEW !
				log.Printf("yenc.Decoder.ReadBody byline: EOF reached d.Part.Number=%d SegId='%s' BodyLen=%d line='%s'", d.Part.Number, *d.SegId, len(d.Part.Body), line)
				return nil
			}*/
		// if we have got an input line we parse it
		// check for =yend
		if len(line) >= 5 && string(line[:5]) == "=yend" {
			if Debug3 {
				log.Printf("yenc.Decoder yenctest=3 found =yend seg.Id='%s' d.Part.Body=%d", *d.SegId, len(d.Part.Body))
			}
			return d.ParseTrailerNew(line)
		}
		switch SimdMode {
		case 0:
			// if line is given we use it as the body
			b := d.DecodeLineNew(line)
			// update hashs
			d.Part.crcHash.Write(b)
			//d.crcHash.Write(b)
			// decode
			d.Part.Body = append(d.Part.Body, b...)
			if Debug3 {
				log.Printf("yenc.Decoder yenctest=3 SimdMode=%d readBody len(line)=%d d.Part.Body=%d SegId='%s' d.Part.Number=%d lineHex='%x'", SimdMode, len(line), len(d.Part.Body), *d.SegId, d.Part.Number, line)
			}
		case 1:
			// Decode the line using SIMD
			if decoded, ok := DecodeLineSIMD([]byte(line)); !ok {
				return fmt.Errorf("error yenc.Decoder yenctest=3 SimdMode=1 DecodeLineSIMD")
			} else {
				// Update the part body with the decoded data
				d.Part.Body = append(d.Part.Body, decoded...)
				// Update the crc hash
				d.Part.crcHash.Write(decoded)
				d.crcHash.Write(decoded)
			}
		case 2:
			// Decode the line using SIMD2
			decoded := []byte(line)
			// DecodeLineSIMD2 returns true if successful
			if ok := DecodeLineSIMD2(decoded); !ok {
				return fmt.Errorf("error yenc.Decoder mode=3 SimdMode=2 DecodeLineSIMD2")
			} else {
				// Update the part body with the decoded data
				d.Part.Body = append(d.Part.Body, decoded...)
				// Update the crc hash
				d.Part.crcHash.Write(decoded)
				d.crcHash.Write(decoded)
			}
		default:
			return fmt.Errorf("error in yenc.Decoder.ReadBody: unknown SimdMode %d", SimdMode)
		}

		return nil
	}
	if Debug1 {
		log.Printf("ERROR yenc.ReadBody: reached end without finding =yend, d.Part.Number=%d, SegId='%s', BodyLen=%d line='%s'", d.Part.Number, *d.SegId, len(d.Part.Body), line)
	}
	return fmt.Errorf("error unexpected EOF in yenc.Decoder.ReadBody seg.Id='%s' d.Part.Number=%d", *d.SegId, d.Part.Number)
} // end func ReadBody

func (d *Decoder) DecodeLine(line []byte) []byte {
	i, j := 0, 0
	for ; i < len(line); i, j = i+1, j+1 {
		// escaped chars yenc42+yenc64
		if d.awaitingSpecial {
			line[j] = (((line[i] - 42) & 255) - 64) & 255
			d.awaitingSpecial = false
			// if escape char - then skip and backtrack j
		} else if line[i] == '=' {
			d.awaitingSpecial = true
			j--
			continue
			// normal char, yenc42
		} else {
			line[j] = (line[i] - 42) & 255
		}
	}
	// return the new (possibly shorter) slice
	// shorter because of the escaped chars
	return line[:len(line)-(i-j)]
}

func (d *Decoder) DecodeLineNew(line string) []byte {
	var decoded []byte
	for i := 0; i < len(line); i++ {
		if line[i] == '=' { // Found escape sequence
			if i+1 < len(line) {
				// Handle the escape: Subtract 64 and 42 from the next byte
				decoded = append(decoded, (line[i+1]-42-64)&255)
				i++ // Skip the next byte after processing the escape
			} else {
				// Incomplete escape sequence (should not happen in well-formed data)
				return nil
			}
		} else {
			// Normal yEnc character, subtract 42
			decoded = append(decoded, (line[i]-42)&255)
		}
	}
	return decoded
}

// parseUint32 parses an unsigned integer string in the given base, ensuring it fits in a uint32.
func parseUint32(s string, base int) (uint32, error) {
	v, err := strconv.ParseUint(s, base, 32)
	if err != nil {
		return 0, err
	}
	if v > 0xFFFFFFFF {
		return 0, fmt.Errorf("value '%s' overflows uint32", s)
	}
	return uint32(v), nil
}

func (d *Decoder) ParseTrailerNew(line string) error {
	parts := strings.Split(line, " ")
	for i := range parts {
		kv := strings.Split(strings.TrimSpace(parts[i]), "=")
		if len(kv) < 2 {
			continue
		}
		switch kv[0] {
		case "size":
			// parse size as int64
			if size, err := strconv.ParseInt(kv[1], 10, 64); err != nil {
				return fmt.Errorf("error in yenc.ParseTrailer: size parse error '%s' err='%v'", kv[1], err)
			} else {
				d.Part.Size = size
			}
		case "pcrc32":
			// parse pcrc32 as uint32 in base 16
			// this is the part crc32
			crc, err := parseUint32(kv[1], 16)
			if err != nil {
				return fmt.Errorf("error in yenc.ParseTrailer: pcrc32 parse error '%s' err='%v'", kv[1], err)
			}
			d.Part.Crc32 = crc
		case "crc32":
			// parse crc32 as uint32 in base 16
			// this is the full crc32 of the part body
			crc, err := parseUint32(kv[1], 16)
			if err != nil {
				return fmt.Errorf("error in yenc.ParseTrailer: crc32 parse error '%s' err='%v'", kv[1], err)
			}
			d.Fullcrc32 = crc
		case "part":
			// parse part number as int
			// this is the part number of the part trailer
			// it is used to check the part order
			partNum, _ := strconv.Atoi(kv[1])
			if partNum != d.Part.Number {
				return fmt.Errorf("yenc: =yend header out of order expected part %d got %d", d.Part.Number, partNum)
			}
		}
	}
	return nil
} // end func ParseTrailer

func (d *Decoder) Validate() error {
	if Debug1 {
		log.Printf("yenc.Decoder.Validate() d.Part.Number=%d SegId='%s'", d.Part.Number, *d.SegId)
	}
	if d.Fullcrc32 > 0 {
		if sum := d.crcHash.Sum32(); sum != d.Fullcrc32 {
			return fmt.Errorf("crc check failed expected %x got %x  SegId='%s'", d.Fullcrc32, sum, *d.SegId)
		}
		if Debug1 {
			log.Printf("yenc.Decoder validated d.Part.Number=%d SegId='%s'", d.Part.Number, *d.SegId)
		}
		return nil
	}
	return fmt.Errorf("error in yenc.Decoder.validate d.Fullcrc32 not set d.Part.Number=%d SegId='%s'", d.Part.Number, *d.SegId)
}

func Sub42Bulk(dst, src []byte) {
	for i := range src {
		dst[i] = (src[i] - 42) & 0xFF
	}
}

func DecodeLineSIMD2(line []byte) bool {
	read := 0
	write := 0

	for read < len(line) {
		// Find next escape, process run up to it
		blockStart := read
		for read < len(line) && line[read] != '=' {
			read++
		}
		blockLen := read - blockStart
		if blockLen > 0 {
			// SIMD/bulk subtract 42
			Sub42Bulk(line[write:write+blockLen], line[blockStart:read])
			write += blockLen
		}
		// Handle escape if present
		if read < len(line) && line[read] == '=' {
			if read+1 >= len(line) {
				// Incomplete escape
				return false
			}
			// Escaped byte: subtract 42 and 64
			line[write] = (line[read+1] - 42 - 64) & 0xFF
			write++
			read += 2
		}
	}
	// Truncate to decoded length
	line = line[:write]
	return true
} // end func DecodeLineSIMD2 (written by AI! GPT-4.1)

func DecodeLineSIMD(line []byte) ([]byte, bool) {
	decoded := make([]byte, 0, len(line))
	i := 0
	for i < len(line) {
		// Process blocks up to next '='
		start := i
		for i < len(line) && line[i] != '=' {
			i++
		}
		// Bulk process [start:i] (no escapes)
		if start < i {
			block := line[start:i]
			for _, b := range block {
				decoded = append(decoded, (b-42)&0xFF)
			}
			// To SIMD: replace above loop with assembly or simd call
		}
		// Handle escape, if present
		if i < len(line) && line[i] == '=' {
			if i+1 < len(line) {
				decoded = append(decoded, (line[i+1]-42-64)&0xFF)
				i += 2
			} else {
				// Incomplete escape sequence
				return nil, false
			}
		}
	}
	return decoded, true
} // end func DecodeLineSIMD (written by AI! GPT-4.1)

func (d *Decoder) DecodeYencLinesSIMD() error {
	// Iterate over each line of the article
	for i, line := range d.Dat {
		if strings.HasPrefix(*line, "=ybegin") || strings.HasPrefix(*line, "=ypart") {
			continue
		}
		if strings.HasPrefix(*line, "=yend") {
			//log.Printf("... parseTrailer")
			if err := d.ParseTrailerNew(*line); err != nil {
				return fmt.Errorf("error parsing trailer at line %d: %w", i, err)
			}
			continue
		}

		switch SimdMode {
		case 1:
			if decoded, ok := DecodeLineSIMD([]byte(*line)); !ok {
				return fmt.Errorf("error decoding line %d", i)
			} else {
				// Update the part body with the decoded data
				d.Part.Body = append(d.Part.Body, decoded...)
				// Update the crc hash
				d.Part.crcHash.Write(decoded)
				d.crcHash.Write(decoded)
			}

		case 2:
			if SimdMode == 2 {
				// Decode the line using SIMD
				decoded := []byte(*line)
				// Decode the line using SIMD2
				if !DecodeLineSIMD2(decoded) {
					return fmt.Errorf("error decoding line %d: incomplete escape sequence", i)
				}

				// Update the part body with the decoded data
				d.Part.Body = append(d.Part.Body, decoded...)
				// Update the crc hash
				d.Part.crcHash.Write(decoded)
				d.crcHash.Write(decoded)
			}
		}
	}
	return nil
} // end func DecodeYencLinesSIMD (written by AI! GPT-4.1)

func (d *Decoder) DecodeYenc() ([]byte, error) {
	var decoded []byte
	var buffer []byte // Temporary buffer to hold the current lines

	// Iterate over each line of the article
	for _, line := range d.Dat {
		if strings.HasPrefix(*line, "=ybegin") || strings.HasPrefix(*line, "=ypart") {
			continue
		}
		if strings.HasPrefix(*line, "=yend") {
			//log.Printf("... parseTrailer")
			if err := d.ParseTrailerNew(*line); err != nil {
				return nil, err
			}
			continue
		}
		// Append the current line to the buffer
		buffer = append(buffer, []byte(*line)...)

		// Process the current buffer, character by character
		for i := 0; i < len(buffer); i++ {
			if buffer[i] == '=' { // Found escape sequence
				if i+1 < len(buffer) {
					// Handle the escape: Subtract 64 and 42 from the next byte
					decoded = append(decoded, (buffer[i+1]-42-64)&255)
					i++ // Skip the next byte after processing the escape
				} else {
					// Incomplete escape sequence (should not happen in well-formed data)
					return nil, fmt.Errorf("incomplete escape sequence at the end of line")
				}
			} else {
				// Normal yEnc character, subtract 42
				decoded = append(decoded, (buffer[i]-42)&255)
			}
		}

		// Reset the buffer to process the next line of data
		buffer = buffer[:0]
	}
	//log.Printf("decoded=%d", len(decoded))
	return decoded, nil
} // end func decodeYenc (written by AI! GPT-4o)
