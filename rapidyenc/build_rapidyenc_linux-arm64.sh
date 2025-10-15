#!/usr/bin/env bash
cd rapidyenc || exit 2
rm -rf build
mkdir -p build
cd build || exit 3
cmake .. -DCMAKE_TOOLCHAIN_FILE=../toolchain-linux-arm64.cmake || exit 4
cmake --build . --config Release || exit 5
ls . rapidyenc_static/
cd ../../ || exit 6
if [ "$1" = "darwin" ]; then
 cp -v rapidyenc/build/rapidyenc_static/librapidyenc.a ./librapidyenc_darwin_arm64.a || exit 7
 ln -sfv ./librapidyenc_darwin_arm64.a ./librapidyenc.a
else
 cp -v rapidyenc/build/rapidyenc_static/librapidyenc.a ./librapidyenc_linux_arm64.a || exit 7
 ln -sfv ./librapidyenc_linux_arm64.a ./librapidyenc.a
fi
#rm -rf rapidyenc/build
