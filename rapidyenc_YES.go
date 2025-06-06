//go:build rapidyenc1

package main

// compile with rapidyenc support
import (
	"fmt"

	"github.com/go-while/NZBreX/rapidyenc" //"github.com/mnightingale/rapidyenc"
)

// compiler flags
const compiledwithRapidyenc bool = true

type Decoder struct {
	ry *rapidyenc.Decoder // pointer to RapidYenc instance
	// other fields can be added as needed
	// e.g., options, state, etc.
}

func NewDecoder() (*Decoder, error) {
	decoder := &Decoder{
		ry: nil, // initialize with nil, will be set later
	}
	err := decoder.newRapidYencDecoder() // initialize RapidYenc
	if err != nil || decoder.ry == nil {
		return nil, fmt.Errorf("Failed to initialize RapidYenc err='%v'", err)
	}
	return decoder, nil
}

func (d *Decoder) newRapidYencDecoder() error {
	// create a new RapidYenc instance
	if d.ry != nil {
		return fmt.Errorf("rapidYenc already initialized in this decoder")
	}
	d.ry = rapidyenc.AcquireDecoder()
	return nil
}

func (d *Decoder) ReleaseRapidYencDecoder() error {
	if d.ry == nil {
		return fmt.Errorf("rapidYenc not initialized in this decoder")
	}
	rapidyenc.ReleaseDecoder(d.ry)
	d.ry = nil // reset to nil after release
	return nil
}
