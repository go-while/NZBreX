//go:build rapidyenc

package main

// placeholder to compile with rapidyenc
import (
	"fmt"

	"github.com/go-while/NZBreX/rapidyenc"
)

type Decoder struct {
	ry *rapidyenc.RapidYenc // pointer to RapidYenc instance
	// other fields can be added as needed
	// e.g., options, state, etc.
}

func NewDecoder() (*Decoder, error) {
	decoder := &Decoder{
		ry: nil, // initialize with nil, will be set later
	}
	err := decoder.NewRapidYencDecoder() // initialize RapidYenc
	if err != nil || decoder.ry == nil {
		return nil, fmt.Errorf("Failed to initialize RapidYenc err='%v'", err)
	}
	return decoder, nil
}

func (d *Decoder) Decode(data []byte) ([]byte, error) {
	// Use rapidyenc's decoder here
	return d.ry.Decode(data)
}

func (d *Decoder) Encode(data []byte) ([]byte, error) {
	// Use rapidyenc's encoder here
	return d.ry.Encode(data)
}

func (d *Decoder) NewRapidYencDecoder() error {
	// create a new RapidYenc instance
	if d.ry != nil {
		return fmt.Errorf("rapidYenc already initialized in this decoder")
	}
	ry := rapidyenc.NewRapidYenc()
	// set some default options if needed
	ry.SetDefaultOptions()
	d.ry = ry
	return nil
}
