rm -rf rapidyenc/rapidyenc/build
mkdir -p rapidyenc/rapidyenc/build
cd rapidyenc && ./crossbuild_rapidyenc_win.sh && cd ../

export GOOS=windows
export GOARCH=amd64
go build -o NZBreX_ry.exe -tags windows .
exit $?
