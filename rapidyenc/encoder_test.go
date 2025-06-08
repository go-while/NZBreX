package rapidyenc

import (
	"testing"

	"github.com/stretchr/testify/require"
)

type encoderCase struct {
	name     string
	input    string
	expected string
}

func TestEncoderSimple(t *testing.T) {
	cases := []encoderCase{
		{"NUL", "\x00", "\x2a"},
		{"SPACE", "\x20", "\x4a"},
	}

	encoder := NewEncoder()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded := encoder.Encode([]byte(tc.input))
			require.Equal(t, []byte(tc.expected), encoded)
		})
	}
}

func TestUUEncodeLine(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty", "", "`\n"},
		{"one byte", "A", "!A\n"},
		{"three bytes", "ABC", "#ABC\n"},
		{"all zero", "\x00\x00\x00", "#   \n"},
		{"all max", "\xff\xff\xff", "#o^_\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(UUEncodeLine([]byte(tc.input)))
			require.Equal(t, tc.expected, got)
		})
	}

	// Test encoding a simple 3-byte input ("ABC")
	input := []byte("ABC")
	expected := "#0V%T\n" // '3' (len=3), then encoded, then newline
	line := string(UUEncodeLine(input))
	require.Equal(t, expected, line)

	// Test encoding less than 3 bytes ("A")
	input = []byte("A")
	expected = "!0\n" // '1' (len=1), then encoded, then newline
	line = string(UUEncodeLine(input))
	require.Equal(t, expected, line)
}

func TestUUEncode(t *testing.T) {
	// Test encoding a short string
	input := []byte("Hello, world!")
	mode := "644"
	filename := "test.txt"
	encoded := UUEncode(input, mode, filename)
	encodedStr := string(encoded)
	// Should start with begin line, end with end, and contain encoded data
	require.Contains(t, encodedStr, "begin 644 test.txt\n")
	require.Contains(t, encodedStr, "end\n")
	// Check that the encoded data decodes back to the original
	lines := splitLines(encodedStr)
	var dataLines [][]byte
	for _, l := range lines {
		if len(l) > 0 && l[0] != 'b' && l != "end" && l[0] != '`' {
			dataLines = append(dataLines, []byte(l))
		}
	}
	var decoded []byte
	for _, l := range dataLines {
		b, err := UUdecode(l)
		require.NoError(t, err)
		decoded = append(decoded, b...)
	}
	require.Equal(t, input, decoded)
}

func TestUUEncodeDecodeRoundTrip(t *testing.T) {
	// Round-trip test: encode and decode
	input := []byte("The quick brown fox jumps over the lazy dog.")
	mode := "600"
	filename := "fox.txt"
	encoded := UUEncode(input, mode, filename)
	encodedStr := string(encoded)
	// Extract data lines
	lines := splitLines(encodedStr)
	var dataLines [][]byte
	for _, l := range lines {
		if len(l) > 0 && l[0] != 'b' && l != "end" && l[0] != '`' {
			dataLines = append(dataLines, []byte(l))
		}
	}
	var decoded []byte
	for _, l := range dataLines {
		b, err := UUdecode(l)
		require.NoError(t, err)
		decoded = append(decoded, b...)
	}
	require.Equal(t, input, decoded)
}

// Helper to split lines (handles both \n and \r\n)
func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
