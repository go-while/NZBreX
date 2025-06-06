# Toolchain file for macOS cross-compilation using osxcross

# Set the target platform
set(CMAKE_SYSTEM_NAME Darwin)
set(CMAKE_SYSTEM_VERSION 12)  # or your target macOS version

# Set the root of your osxcross installation
set(OSXCROSS_ROOT /opt/osxcross)  # <-- set this to your actual osxcross path!

# Compilers
set(CMAKE_C_COMPILER ${OSXCROSS_ROOT}/target/bin/o64-clang)
set(CMAKE_CXX_COMPILER ${OSXCROSS_ROOT}/target/bin/o64-clang++)

# SDK location
set(CMAKE_OSX_SYSROOT ${OSXCROSS_ROOT}/target/SDK/MacOSX12.sdk)  # update version if needed

# Set architecture
set(CMAKE_OSX_ARCHITECTURES "x86_64")

# Set minimum macOS deployment target (optional)
set(CMAKE_OSX_DEPLOYMENT_TARGET "10.13")
