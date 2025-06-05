export GOOS=windows
export GOARCH=amd64
go build -o NZBreX.exe $(ls *.go|grep -v signals_other)
exit $?
