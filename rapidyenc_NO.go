//go:build !rapidyenc

package main

import (
	"fmt"
)

// placeholder to not compile with rapidyenc

type Decoder struct{}
type Encoder struct{}

func NewDecoder() (*Decoder, error) {
	return nil, fmt.Errorf("error: NewDecoder rapidYenc is not enabled in this build. Please enable it by building with the 'rapidyenc' tag")
}

func NewEncoder() (*Encoder, error) {
	return nil, fmt.Errorf("error: NewEncoder rapidYenc is not enabled in this build. Please enable it by building with the 'rapidyenc' tag")
}
