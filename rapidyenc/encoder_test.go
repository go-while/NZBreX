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
