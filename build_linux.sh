rm -rf rapidyenc/rapidyenc/build
mkdir -p rapidyenc/rapidyenc/build
cd rapidyenc && ./build_rapidyenc_linux.sh && cd ../
go build -o NZBreX -tags other .
exit $?
