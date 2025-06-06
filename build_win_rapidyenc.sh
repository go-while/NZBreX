export GOOS=windows
export GOARCH=amd64
go build -o NZBreX_ry.exe -tags "windows rapidyenc" .
exit $?
