rm -rf rapidyenc/rapidyenc/build
mkdir -p rapidyenc/rapidyenc/build
cd rapidyenc && ./crossbuild_rapidyenc_darwin-amd64.sh && cd ../

export GOOS=darwin
export GOARCH=amd64
export CGO_ENABLED=1
#export CC=aarch64-linux-gnu-gcc
#export CXX=aarch64-linux-gnu-g++

go build -o NZBreX_ry -tags "other rapidyenc"  .
exit $?
