cd rapidyenc || exit 2
rm -rf rapidyenc/build
mkdir -p build 
cd build || exit 3
cmake .. -DCMAKE_TOOLCHAIN_FILE=../../toolchain-linux-386.cmake
#cmake .. -DCMAKE_SYSTEM_NAME=Linux -DCMAKE_SYSTEM_PROCESSOR=x86 \
#  -DCMAKE_C_COMPILER=i686-linux-gnu-gcc -DCMAKE_CXX_COMPILER=i686-linux-gnu-g++

cmake --build . --config Release || exit 5
ls . rapidyenc_static/
cd ../../ || exit 6
cp -v rapidyenc/build/rapidyenc_static/librapidyenc.a . || exit 7
cp -v rapidyenc/build/librapidyenc.dll . || exit 8
