module github.com/go-while/NZBreX

go 1.27.3

require (
	github.com/go-while/go-cpu-mem-profiler v0.0.0-20240612221627-856954a5fc83
	github.com/go-while/go-loggedrwmutex v0.0.0-20250601032232-b0aa20cdcb2c
	github.com/go-while/go-yenc-simd v0.1.2
	github.com/javi11/nzbparser v0.5.5
	github.com/stretchr/testify v1.12.1
	golang.org/x/crypto v0.58.0
	golang.org/x/text v0.43.0
)

require (
	github.com/gorilla/mux v1.8.1 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/net v0.61.0 // indirect
)

// nzbparser: javi11's fork (active, has its own subject parser) with
// go-while's fixes: branch review-fixes of github.com/go-while/nzbparser,
// open as https://github.com/javi11/nzbparser/pull/2. Drop this replace and
// require a javi11 release once it is merged.
replace github.com/javi11/nzbparser => github.com/go-while/nzbparser v0.5.6-0.20261018190657-7c20501e8eac
