#!/usr/bin/env bash

# Windows cross-compilation script for NZBreX
# Requires: gcc-mingw-w64-x86-64 g++-mingw-w64-x86-64 cmake

set -e  # Exit on any error

echo "Building NZBreX for Windows (amd64)..."

# Check if required tools are available
if ! command -v x86_64-w64-mingw32-gcc &> /dev/null; then
    echo "ERROR: x86_64-w64-mingw32-gcc not found. Please install MinGW-w64:"
    echo "  Ubuntu/Debian: sudo apt install gcc-mingw-w64-x86-64 g++-mingw-w64-x86-64"
    echo "  CentOS/RHEL:   sudo yum install mingw64-gcc mingw64-gcc-c++"
    exit 1
fi

if ! command -v cmake &> /dev/null; then
    echo "ERROR: cmake not found. Please install cmake:"
    echo "  Ubuntu/Debian: sudo apt install cmake"
    echo "  CentOS/RHEL:   sudo yum install cmake"
    exit 1
fi

# Build rapidyenc for Windows
echo "Building rapidyenc library for Windows..."
rm -rf rapidyenc/rapidyenc/build
mkdir -p rapidyenc/rapidyenc/build
cd rapidyenc 
if ! ./crossbuild_rapidyenc_windows-amd64.sh; then
    echo "ERROR: Failed to build rapidyenc library for Windows"
    exit 1
fi
cd ../

# Check if rapidyenc library was built successfully
if [ ! -f "rapidyenc/librapidyenc.a" ]; then
    echo "ERROR: rapidyenc static library not found at rapidyenc/librapidyenc.a"
    exit 1
fi

echo "rapidyenc library built successfully"

# Set up cross-compilation environment
export GOOS=windows
export GOARCH=amd64
export CGO_ENABLED=1
export CC=x86_64-w64-mingw32-gcc

echo "Cross-compiling Go application for Windows..."
# Build with static linking to avoid dependency on MinGW runtime DLLs
if ! go build -o NZBreX_ry.exe -ldflags "-linkmode external -extldflags '-static'" -tags "windows rapidyenc" .; then
    echo "ERROR: Failed to build Windows executable"
    exit 1
fi

# Verify the executable was created
if [ ! -f "NZBreX_ry.exe" ]; then
    echo "ERROR: Windows executable not created"
    exit 1
fi

echo "Windows executable built successfully: NZBreX_ry.exe"

# Check dependencies (optional, for debugging)
if command -v x86_64-w64-mingw32-objdump &> /dev/null; then
    echo "DLL dependencies:"
    x86_64-w64-mingw32-objdump -p NZBreX_ry.exe | grep "DLL Name" || echo "  (none found)"
fi

echo "Build completed successfully!"
exit 0
