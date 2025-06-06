//go:build rapidyenc

package main

// compile with rapidyenc support
import (
	"fmt"

	"github.com/go-while/NZBreX/rapidyenc" //"github.com/mnightingale/rapidyenc"
)

// compiler flags
const compiledwithRapidyenc bool = true

type Decoder struct {
	RY *rapidyenc.Decoder // pointer to RapidYenc instance
	// other fields can be added as needed
	// e.g., options, state, etc.
}

func NewRapidYencDecoder() (*Decoder, error) {
	decoder := &Decoder{
		RY: nil, // initialize with nil, will be set later
	}
	err := decoder.newRapidYencDecoder() // initialize RapidYenc
	if err != nil || decoder.RY == nil {
		return nil, fmt.Errorf("Failed to initialize RapidYenc err='%v'", err)
	}
	return decoder, nil
}

func (d *Decoder) newRapidYencDecoder() error {
	// create a new RapidYenc instance
	if d.RY != nil {
		return fmt.Errorf("rapidYenc already initialized in this decoder")
	}
	d.RY = rapidyenc.AcquireDecoder()
	return nil
}

func (d *Decoder) ReleaseRapidYencDecoder() error {
	if d.RY == nil {
		return fmt.Errorf("rapidYenc not initialized in this decoder")
	}
	rapidyenc.ReleaseDecoder(d.RY)
	d.RY = nil // reset to nil after release
	return nil
}
