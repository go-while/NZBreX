#!/usr/bin/env bash
cd rapidyenc || exit 2
rm -rf build
mkdir -p build
cd build || exit 3
cmake .. -DCMAKE_TOOLCHAIN_FILE=../../toolchain-mingw64.cmake || exit 4
cmake --build . --config Release || exit 5
ls . rapidyenc_static/
cd ../../ || exit 6
cp -v rapidyenc/build/rapidyenc_static/librapidyenc.a ./librapidyenc_windows_amd64.a || exit 7
ln -sfv ./librapidyenc_windows_amd64.a ./librapidyenc.a
cp -v rapidyenc/build/librapidyenc.dll . || exit 8
#rm -rf rapidyenc/build
