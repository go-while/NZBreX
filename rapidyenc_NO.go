//go:build !rapidyenc

package main

import (
	"fmt"
)

// compiler flags
const compiledwithRapidyenc bool = false

// this is a placeholder to ensure rapidyenc is not included in the build.
// The actual functionality is not implemented, and the Decoder and Encoder structs
// will return an error when trying to create a new instance.
type Decoder struct{}
type Encoder struct{}

func NewDecoder() (*Decoder, error) {
	return nil, fmt.Errorf("error: NewDecoder rapidYenc is not enabled in this build")
}

func NewEncoder() (*Encoder, error) {
	return nil, fmt.Errorf("error: NewEncoder rapidYenc is not enabled in this build")
}
