rm -rf rapidyenc/rapidyenc/build
mkdir -p rapidyenc/rapidyenc/build
cd rapidyenc && ./crossbuild_rapidyenc_windows-amd64.sh && cd ../

export GOOS=windows
export GOARCH=amd64
export CGO_ENABLED=1
export CC=x86_64-w64-mingw32-gcc

go build -o NZBreX_ry.exe -tags "windows rapidyenc"  .
exit $?
