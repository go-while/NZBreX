//go:build rapidyenc

package rapidyenc

import "testing"

// The Go values of State and End (decoder.go) are the C ones (rapidyenc.h).
func TestStateValuesMatchC(t *testing.T) {
	goValues := []int{
		int(StateCRLF), int(StateEQ), int(StateCR), int(StateNone),
		int(StateCRLFDT), int(StateCRLFDTCR), int(StateCRLFEQ),
		int(EndNone), int(EndControl), int(EndArticle),
	}
	for i, c := range cConstants() {
		if goValues[i] != c {
			t.Fatalf("constant %d: Go %d, C %d", i, goValues[i], c)
		}
	}
}
