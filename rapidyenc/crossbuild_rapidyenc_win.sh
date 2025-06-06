cd rapidyenc || exit 2
rm -rf rapidyenc/build
mkdir -p build 
cd build || exit 3
cmake .. make .. -DCMAKE_TOOLCHAIN_FILE=../../toolchain-mingw64.cmake || exit 4
cmake --build . --config Release || exit 5
ls . rapidyenc_static/
cd ../../ || exit 6
cp -v rapidyenc/build/rapidyenc_static/librapidyenc.a . || exit 7
cp -v rapidyenc/build/librapidyenc.dll . || exit 8
