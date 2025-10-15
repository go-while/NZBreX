#!/usr/bin/env bash
cd rapidyenc || exit 2
rm -rf build
mkdir -p build
cd build || exit 3
cmake .. || exit 4
cmake --build . --config Release || exit 5
ls . rapidyenc_static/
cd ../../ || exit 6
cp -v rapidyenc/build/rapidyenc_static/librapidyenc.a ./librapidyenc_linux_amd64.a || exit 7
ln -sfv ./librapidyenc_linux_amd64.a ./librapidyenc.a
#rm -rf rapidyenc/build
