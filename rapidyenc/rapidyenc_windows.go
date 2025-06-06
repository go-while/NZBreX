//go:build windows
package rapidyenc

// #cgo windows LDFLAGS: -L. -lrapidyenc
// #include "rapidyenc.h"
import "C"

