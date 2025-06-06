rm -rf rapidyenc/rapidyenc/build
mkdir -p rapidyenc/rapidyenc/build
cd rapidyenc && ./build_rapidyenc_linux.sh && cd ../
export GOOS=linux
export GOARCH=amd64
go build -o NZBreX -tags other .
exit $?
