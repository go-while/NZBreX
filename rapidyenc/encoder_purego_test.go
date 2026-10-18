//go:build !rapidyenc

package rapidyenc

// A yEnc encoder for the tests of builds without the C library (the real
// Encoder is rapidyenc's C code, encoder.go). Same output format: 128-byte
// lines joined by CRLF, no CRLF at the end; NUL, LF, CR and '=' escaped, and
// tab, space and '.' at a line start, tab and space at a line end, so the
// output needs no dot-stuffing.

type Encoder struct {
	LineLength int
}

func NewEncoder() *Encoder { return &Encoder{LineLength: 128} }

func (e *Encoder) Encode(src []byte) []byte {
	out := make([]byte, 0, len(src)*103/100+len(src)/64+4)
	col := 0
	for i, c := range src {
		o := c + 42
		last := i == len(src)-1 || col+1 >= e.LineLength
		if o == 0 || o == '\n' || o == '\r' || o == '=' ||
			(o == '\t' || o == ' ') && (col == 0 || last) || o == '.' && col == 0 {
			out = append(out, '=', o+64)
			col += 2
		} else {
			out = append(out, o)
			col++
		}
		if col >= e.LineLength && i != len(src)-1 {
			out = append(out, '\r', '\n')
			col = 0
		}
	}
	return out
}
