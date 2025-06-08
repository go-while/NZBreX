#!/usr/bin/env bash
cd rapidyenc || exit 2
rm -rf build
mkdir -p build
cd build || exit 3
cmake .. -DCMAKE_TOOLCHAIN_FILE=../toolchain-linux-arm64.cmake || exit 4
cmake --build . --config Release || exit 5
ls . rapidyenc_static/
cd ../../ || exit 6
cp -v rapidyenc/build/rapidyenc_static/librapidyenc.a . || exit 7
rm -rf rapidyenc/build
