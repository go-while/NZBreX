module github.com/go-while/NZBreX

go 1.27.2

require (
	github.com/go-while/go-cpu-mem-profiler v0.0.0-20240612221627-856954a5fc83
	github.com/go-while/go-loggedrwmutex v0.0.0-20250601032232-b0aa20cdcb2c
	github.com/go-while/go-yenc-simd v0.0.0-20261018191518-50d56c3e835a
	github.com/javi11/nzbparser v0.5.5
	github.com/stretchr/testify v1.11.1
	golang.org/x/crypto v0.52.0
	golang.org/x/text v0.37.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/gorilla/mux v1.8.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/net v0.54.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// nzbparser: javi11's fork (active, has its own subject parser) with
// go-while's fixes: branch review-fixes of github.com/go-while/nzbparser,
// open as https://github.com/javi11/nzbparser/pull/2. Drop this replace and
// require a javi11 release once it is merged.
replace github.com/javi11/nzbparser => github.com/go-while/nzbparser v0.5.6-0.20261018190657-7c20501e8eac
