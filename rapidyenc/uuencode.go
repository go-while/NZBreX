package rapidyenc

import (
	"bytes"
	"fmt"
)

// UUEncode encodes src as UUencoded text with header/footer.
func UUEncode(src []byte, filename string, mode int) []byte {
	var buf bytes.Buffer
	// Write header
	fmt.Fprintf(&buf, "begin %03o %s\r\n", mode, filename)
	// Encode in 45-byte chunks
	for i := 0; i < len(src); i += 45 {
		chunk := src[i:]
		if len(chunk) > 45 {
			chunk = chunk[:45]
		}
		// Write length character
		buf.WriteByte(byte(len(chunk) + 32))
		// Encode chunk
		for j := 0; j < len(chunk); j += 3 {
			var a, b, c byte
			a = chunk[j]
			if j+1 < len(chunk) {
				b = chunk[j+1]
			}
			if j+2 < len(chunk) {
				c = chunk[j+2]
			}
			buf.WriteByte(((a >> 2) & 0x3F) + 32)
			buf.WriteByte((((a << 4) | ((b >> 4) & 0x0F)) & 0x3F) + 32)
			buf.WriteByte((((b << 2) | ((c >> 6) & 0x03)) & 0x3F) + 32)
			buf.WriteByte((c & 0x3F) + 32)
		}
		buf.WriteString("\r\n")
	}
	// Write footer
	buf.WriteString("end\r\n")
	return buf.Bytes()
}
