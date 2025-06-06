cd rapidyenc || exit 2
mkdir -p build 
cd build || exit 3
cmake .. || exit 4
cmake --build . --config Release || exit 5
ls . rapidyenc_static/
cd ../../ || exit 6
cp -v rapidyenc/build/rapidyenc_static/librapidyenc.a . || exit 7
