@echo off
REM Windows build script for NZBreX
REM Requires: Git, Go 1.19+, MinGW-w64, CMake

echo Building NZBreX for Windows...

REM Check if Go is available
go version >nul 2>&1
if errorlevel 1 (
    echo ERROR: Go not found. Please install Go 1.24.3 or later.
    echo Download from: https://golang.org/dl/
    pause
    exit /b 1
)

REM Check if CMake is available
cmake --version >nul 2>&1
if errorlevel 1 (
    echo ERROR: CMake not found. Please install CMake.
    echo Download from: https://cmake.org/download/
    pause
    exit /b 1
)

REM Check if we're in the right directory
if not exist "rapidyenc" (
    echo ERROR: rapidyenc directory not found. Please run this script from the NZBreX root directory.
    pause
    exit /b 1
)

echo Building rapidyenc library for Windows...
cd rapidyenc
if not exist "rapidyenc" (
    echo ERROR: rapidyenc submodule not initialized. Please run: git submodule update --init --recursive
    pause
    exit /b 1
)

REM Build rapidyenc
rmdir /s /q rapidyenc\build 2>nul
mkdir rapidyenc\build
cd rapidyenc\build

cmake .. -G "MinGW Makefiles"
if errorlevel 1 (
    echo ERROR: CMake configuration failed. Please ensure MinGW-w64 is properly installed.
    echo You may need to install MSYS2 and MinGW-w64 from: https://www.msys2.org/
    pause
    cd ..\..\..\
    exit /b 1
)

cmake --build . --config Release
if errorlevel 1 (
    echo ERROR: Failed to build rapidyenc library.
    pause
    cd ..\..\..\
    exit /b 1
)

REM Copy library files
copy rapidyenc_static\librapidyenc.a ..\..\librapidyenc.a
cd ..\..\..\

echo rapidyenc library built successfully.

echo Building NZBreX executable...
set CGO_ENABLED=1
go build -o NZBreX.exe -tags "windows rapidyenc" .
if errorlevel 1 (
    echo ERROR: Failed to build NZBreX executable.
    pause
    exit /b 1
)

echo.
echo Build completed successfully!
echo Executable: NZBreX.exe
echo.
echo To test the build, run: NZBreX.exe -version
pause
