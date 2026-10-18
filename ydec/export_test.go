package ydec

// Internals for the tests in package ydec_test (they import rapidyenc, which
// imports ydec, so they can't be in package ydec).
var (
	Special      = special
	DecodeScalar = decodeScalar
)
