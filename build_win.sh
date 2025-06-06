export GOOS=windows
export GOARCH=amd64
go build -o NZBreX.exe -tags windows .
exit $?
