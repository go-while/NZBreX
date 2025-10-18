#!/bin/bash
if [ "$1" != "quick" ]; then
 rm -rf rapidyenc/rapidyenc/build
 mkdir -p rapidyenc/rapidyenc/build
 cd rapidyenc && ./build_rapidyenc_linux-amd64.sh && cd ../
fi
export GOOS=linux
export GOARCH=amd64
go build -race -o NZBreX -tags other . && echo "built ok"
exit $?
